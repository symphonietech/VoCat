# SIP trunk (Asterisk integration)

VoCat can expose its SIM-backed calling as an ordinary SIP trunk, so a PBX in
front of it handles the things a PBX is good at — registration, softphones,
dial plans, voicemail, recording — while VoCat stays a gateway between SIP and
a modem's IMS registration.

This is deliberately **not** a registrar. There are no user accounts and no
digest authentication: peers are authorised by source address. Identity is the
PBX's job.

> **Status.** Outbound calls work: the PBX sends an `INVITE`, VoCat places the
> call over the SIM's IMS registration and bridges the audio. The inbound
> direction — a call arriving on the SIM being offered to the PBX — is not here
> yet; inbound calls are still answered from VoCat's own Calls page.

## Enabling it

Off unless an address is set. Two settings, by environment or config file:

```sh
VOCAT_SIP_TRUNK_ADDR=127.0.0.1:5062
VOCAT_SIP_TRUNK_PEERS=127.0.0.1
```

```json
{
  "sip_trunk_address": "127.0.0.1:5062",
  "sip_trunk_peers": ["172.20.0.2"]
}
```

`VOCAT_SIP_TRUNK_PEERS` accepts addresses and CIDR prefixes, comma or space
separated. Startup fails if an address is set with no peers, rather than
starting a listener that would drop every packet.

**Bind it somewhere private.** Source-address authorisation is only as good as
the network it runs on: an interface reachable from the internet would expose
SIM-backed calling — and its call charges — to anyone who can spoof a packet.
Loopback, a container network, or a WireGuard address. Note VoCat's compose
file uses `network_mode: host`, so `127.0.0.1:5062` is the host's loopback and
an Asterisk container needs either host networking too or a private address
listed in the peers.

A request from an address outside the list is dropped without a reply, so a
scanner cannot confirm anything is listening.

## Running Asterisk in a container

There is a ready-made setup in this repo: `asterisk/` (image and config
templates) plus `docker-compose.asterisk.yml`, an overlay on the main compose
file.

```sh
cp .env.example .env
${EDITOR:-nano} .env     # set ASTERISK_SIP_PASSWORD, uncomment COMPOSE_FILE
./scripts/docker-build.sh
```

`COMPOSE_FILE` in `.env` is what makes every `docker compose` command in this
directory load both files with no `-f` flags — including `docker-build.sh`,
which runs a plain `docker compose up`:

```sh
COMPOSE_FILE=docker-compose.yml:docker-compose.asterisk.yml
```

Without it, pass `-f docker-compose.yml -f docker-compose.asterisk.yml` on
every later command, or Compose will not know the Asterisk service exists.

Use `docker-build.sh` rather than `docker compose up -d --build` by hand. Both
build both services — `--build` rebuilds everything with a `build:` section —
but the script derives `VOCAT_TAG` and `VOCAT_BUILD_TIME` from git, and those
become the image tag *and* the version compiled into the binary. Building
by hand tags the image `:latest` over your previous build and reports
`0.1.0-dev` in Settings > System Info.

The overlay also sets `VOCAT_SIP_TRUNK_ADDR` and `VOCAT_SIP_TRUNK_PEERS` on the
VoCat service, so enabling the trunk and starting the PBX are one step.

### Changing the Asterisk config afterwards

The templates are bind-mounted and rendered at every start, so an edit needs
no rebuild:

```sh
${EDITOR:-nano} asterisk/templates/extensions.conf
docker compose restart asterisk
```

A rebuild (`docker compose build asterisk`) is only for changes to
`asterisk/Dockerfile` or `asterisk/entrypoint.sh`.

Then point a softphone at the host:

| | |
| --- | --- |
| Server / domain | this host's LAN IP |
| Username | `1001`, or whatever `ASTERISK_SIP_USER` says |
| Password | `ASTERISK_SIP_PASSWORD` |
| Transport | UDP or TCP (not TLS — no certificate is configured) |

Dial a number and it goes out over the SIM.

### Why both containers use host networking

VoCat already runs with `network_mode: host`, because the export-proxy plugin
needs `SO_BINDTODEVICE` in the host's network namespace. That has a
consequence for the trunk: `127.0.0.1:5062` is the **host's** loopback, and a
container on a bridge network cannot reach it.

Putting Asterisk on host networking too makes the whole thing trivial — both
sides on loopback, no NAT, and the RTP range needs no port publishing at all.
The cost is that Asterisk binds the host's port 5060 directly, so it will fail
to start if something else already has it.

### The bridged alternative

If port 5060 is taken, or policy rules out host networking for the PBX, the
trunk can listen on the bridge gateway instead — still a host address, but one
containers can reach:

```yaml
    environment:
      VOCAT_SIP_TRUNK_ADDR: 172.18.0.1:5062
      VOCAT_SIP_TRUNK_PEERS: 172.18.0.0/16
```

with `contact=sip:vocat@172.18.0.1:5062` and `match=172.18.0.1` in
`pjsip.conf`, and the Asterisk service on a user-defined bridge rather than
`network_mode: host`. Check the real gateway first — it is not always
`172.18.0.1`:

```sh
docker network inspect <network> -f '{{range .IPAM.Config}}{{.Gateway}}{{end}}'
```

VoCat's RTP leg binds whatever address the trunk listens on, so the media
follows the signalling onto the bridge without further configuration.

What this does **not** solve is softphones: they are outside Docker, so
Asterisk's own 5060 and its whole RTP range have to be published, and
`docker-proxy` handles a 200-port UDP range badly. Expect to add
`external_media_address` and `external_signaling_address` to the PJSIP
transport as well. Host networking avoids all of it, which is why it is the
default here.

### What is in `asterisk/`

| Path | |
| --- | --- |
| `Dockerfile` | Ubuntu 26.04 LTS plus the packaged Asterisk 22.5, the current upstream LTS — a distribution package rather than a registry image, since a SIM's call charges sit behind this. Not 24.04, whose Asterisk 20 leaves full upstream support on 2026-10-19; not Debian, which dropped Asterisk before bookworm released. Note `asterisk` is in universe, so its security updates are community-maintained rather than Canonical-supported |
| `entrypoint.sh` | Renders the templates into `/etc/asterisk`, then runs Asterisk in the foreground |
| `templates/pjsip.conf` | The trunk endpoint and one softphone account |
| `templates/extensions.conf` | Outbound dial plan, `[from-vocat]` ready for inbound |
| `templates/rtp.conf` | Ports 10000-10200 |
| `templates/modules.conf` | `noload => chan_sip.so` — a no-op on Asterisk 22, which removed it; kept for a rebase onto an older base |

The configs are rendered rather than mounted so the SIP password stays in the
environment and never reaches a git-tracked file. `envsubst` is given an
explicit variable list, without which it would also expand Asterisk's own
`${EXTEN}` and `${VOCATDEV}`. The entrypoint refuses a password under 12
characters: SIP registrars are scanned continuously, and a weak one on a PBX
with a real SIM behind it becomes someone else's long-distance plan.

Everything below is what those files contain, for anyone wiring up an existing
Asterisk instead.

## Asterisk side

In `pjsip.conf`, a transport, an endpoint pointing at VoCat, and an `identify`
so inbound requests from VoCat are attributed to that endpoint:

```ini
[transport-udp]
type=transport
protocol=udp
bind=0.0.0.0:5060

; Softphones do not all default to UDP, and a client set to TCP against a
; UDP-only Asterisk gets its connection refused -- which looks like a bad
; password at the phone and logs nothing at all here.
[transport-tcp]
type=transport
protocol=tcp
bind=0.0.0.0:5060

[vocat]
type=endpoint
context=from-vocat
disallow=all
; VoCat's media layer is G.711 only, which is what its carriers negotiate.
allow=ulaw
allow=alaw
direct_media=no
aors=vocat

[vocat]
type=aor
contact=sip:vocat@127.0.0.1:5062

[vocat]
type=identify
endpoint=vocat
match=127.0.0.1
```

No `type=auth`: the trunk has no credentials to check, and Asterisk does not
need any to reach it.

A softphone account for Linphone, so calls arrive somewhere:

```ini
[1001]
type=endpoint
context=from-internal
disallow=all
allow=ulaw
allow=alaw
auth=1001
aors=1001

[1001]
type=auth
type=userpass
username=1001
password=<a long random password>

[1001]
type=aor
max_contacts=2
```

`max_contacts=2` lets the same account register from a phone and a desktop at
once; Asterisk then rings both and the first to answer wins.

Routing, in `extensions.conf`:

```ini
[from-internal]
; Everything goes out through the SIM, so the pattern is "everything".
; Asterisk warns that _. matches any extension; here that is the intent --
; this context has no feature codes and one destination.
;
; Do not narrow this to _X.: that does not match +E.164, and a softphone
; sending "+1813..." is then answered 404 by Asterisk before the INVITE ever
; reaches VoCat. A bare + in a pattern is unreliable, so match everything and
; let VoCat validate the number.
exten => _.,1,Dial(PJSIP/${EXTEN}@vocat,60)
 same => n,Hangup()

[from-vocat]
; Inbound from the SIM rings the softphone. Not reached yet -- see Status.
exten => _.,1,Dial(PJSIP/1001,30)
 same => n,Hangup()
```

## Choosing which SIM places a call

With one IMS-registered device VoCat picks it, and the dial plan above is all
there is to it. With more than one, VoCat refuses to guess — picking for you
would put a real, billed call on whichever SIM happened to sort first — so the
dial plan has to name one. Either an ID or the device name works.

A header. Note the `b()` pre-dial handler: `PJSIP_HEADER(add,...)` applies to
the channel it runs on, so setting it in `[from-internal]` would attach the
header to the *softphone's* leg, where VoCat never sees it. `b()` runs on the
outbound channel, just before the INVITE leaves.

```ini
[from-internal]
exten => _X.,1,Set(__VOCATDEV=SLOT1-1)
 same => n,Dial(PJSIP/${EXTEN}@vocat,60,b(vocat-predial^s^1))
 same => n,Hangup()

[vocat-predial]
; The __ prefix is what makes VOCATDEV inherit onto the outbound channel.
exten => s,1,Set(PJSIP_HEADER(add,X-VoCat-Device)=${VOCATDEV})
 same => n,Return()
```

Or a URI parameter, which needs no handler because it is part of the dial
string. The `;` has to be escaped, or `extensions.conf` reads the rest of the
line as a comment:

```ini
exten => _X.,1,Dial(PJSIP/vocat/sip:${EXTEN}@127.0.0.1:5062\;device=SLOT1-1,60)
```

A per-SIM outbound route is usually clearer than either: give each SIM its own
extension pattern and set `__VOCATDEV` there.

## What the PBX sees

| Situation | Status | Meaning |
| --- | --- | --- |
| Call bridged | `200 OK` | Answered, with an SDP answer VoCat's RTP leg is listening on |
| Named SIM absent, or not IMS-registered | `404 Not Found` | A dial plan mistake: try another route, do not retry this one |
| No SIM named and more than one available | `404 Not Found` | Add the header or URI parameter above |
| Offer with no G.711 | `488 Not Acceptable Here` | Fix `allow=` on the endpoint |
| Nobody answered, busy, rejected | `480 Temporarily Unavailable` | The VoCat log carries the real SIP status from the carrier |
| IMS session cannot signal or carry media | `503` / `500` | VoCat's own fault; the log says which |

VoCat answers only once the SIM's own call is both answered **and** carrying
RTP. Answering earlier would bridge a leg with nowhere to send audio, which is
exactly the silent call this path exists to avoid.

## Verifying

Start VoCat with the trunk enabled, then from the Asterisk CLI:

```
pjsip show endpoint vocat
pjsip qualify vocat
```

The endpoint should read **Avail**. That means Asterisk's `OPTIONS` reached
VoCat, was accepted from a trusted address, and was answered — the signalling
path, before any call uses it.

`qualify_frequency=60` on the `vocat` AOR is what makes this work: Asterisk
only learns a peer is reachable by sending it OPTIONS, and without it the
contact reads `NonQual` for ever no matter how healthy the trunk is.

VoCat logs those at **debug**, since a keepalive every minute would otherwise
bury the handful of lines that describe an actual call:

```
siptrunk handled a request  peer=127.0.0.1:5060 method=OPTIONS status=200
```

It says at info level why it dropped a request:

```
siptrunk rejected an untrusted peer  peer=192.168.1.50:5060
```

If the endpoint stays **Unavail**, work through it in this order: the log will
show `rejected an untrusted peer` if `match=` and `VOCAT_SIP_TRUNK_PEERS`
disagree, and nothing at all if the packets never arrive — which on host
networking usually means the port, and on bridged networking usually means the
container cannot reach the host's loopback.

Then place a call from the softphone. VoCat logs the call as it goes:

```
siptrunk placed a call    call_id=... device_id=SLOT1-1 number=+15551234 ims_call_id=...
siptrunk bridged a call   call_id=... ims_call_id=... rtp_port=41234
siptrunk released a call  call_id=...
```

`placed` without `bridged` means the SIM leg never reached answered-with-media;
the preceding `siptrunk call failed` line carries the reason. No `placed` line
at all means the INVITE was refused before dialling — the status table above
says which case that was.

### Asterisk answers 404 before VoCat sees anything

A capture shows the INVITE reaching Asterisk, a `100 Trying`, then:

```
SIP/2.0 404 Not Found
Reason: Q.850;cause=3
```

and **no INVITE to port 5062 at all**. That is Asterisk's own "no matching
extension" — the call never got as far as the trunk, so nothing in VoCat is
at fault.

Ask Asterisk directly whether the number matches:

```sh
docker compose exec asterisk asterisk -rx "dialplan show from-internal"
docker compose exec asterisk asterisk -rx "dialplan show +18133659364@from-internal"
```

The usual cause is a pattern that does not cover the leading `+` of an E.164
number, which is why the shipped dialplan matches `_.` rather than trying to
enumerate first characters. If `dialplan show from-internal` prints nothing at
all, `extensions.conf` did not load and `docker compose logs asterisk` will
say why.

### Softphone cannot register

```
chan_sip.c:29060 handle_request_register: Registration from 'sip:1001@...'
failed for '...' - Wrong password
```

This cannot happen on the shipped image any more — Asterisk 21 removed
chan_sip and this image is on 22 — but it is exactly what an older base or an
existing Asterisk 20 install will do.

**`chan_sip.c` is the tell, and the message is a lie.** This setup is
PJSIP-only, so a registration answered by `chan_sip` was answered by a driver
that has never heard of extension 1001. chan_sip's `alwaysauthreject` default
challenges an unknown peer and then reports "Wrong password" rather than "no
such peer" — deliberate, so a scanner cannot enumerate valid extensions, but
it sends anyone debugging their own setup after a credential problem that does
not exist.

Asterisk 20 still ships chan_sip, and the packaged `sip.conf` has it bind
5060 — the port the PJSIP transport wants. Whichever driver loads first takes
it. `templates/modules.conf` noloads chan_sip for exactly this reason; if you
see the message above, that file is not in effect. Check:

```sh
docker compose exec asterisk asterisk -rx "module show like chan_sip"
docker compose exec asterisk asterisk -rx "pjsip show endpoints"
```

The first should list nothing, the second should show `vocat` and your
softphone extension. If `pjsip show endpoints` errors, `res_pjsip` did not
load at all and `docker compose logs asterisk` will say why.

A genuine wrong password from PJSIP looks different — it names the endpoint
and comes from `res_pjsip`, not `chan_sip`.

### Another SIP server already owns port 5060

If the container's log is **completely empty** while the phone reports a
credential failure, the packet is very likely being answered by something
else. This container uses host networking, so 5060 is the host's port: an
Asterisk installed on the host, or any other PBX, wins the bind, and this
container then runs with no SIP transport at all. `pjsip show endpoints` still
lists everything, which makes it look healthy.

The entrypoint now refuses to start in that situation and says so, but on an
older image, prove it with a capture rather than a log:

```sh
sudo timeout 30 tcpdump -ni any port 5060 -vv
```

The `Server:` header in the response is the giveaway — it names the version
that actually answered:

```
Server: Asterisk PBX 16.2.1~dfsg-2ubuntu1     <- a host install
Server: Asterisk PBX 22.5.2                   <- this container
```

Find and remove the other one:

```sh
sudo ss -lunp | grep :5060
sudo systemctl disable --now asterisk
docker compose up -d asterisk
```

Verify the container is what answers:

```sh
docker compose exec asterisk asterisk -rx "core show version"
```

Upgrading the host install rather than removing it does not help: two SIP
servers cannot share port 5060, whatever versions they are. Pick one.

## The trunk and the Calls page together

Both work at once, and neither has to be off for the other to run. The Calls
page places and answers calls exactly as before; the trunk is inert unless
`VOCAT_SIP_TRUNK_ADDR` is set, and even then it only touches calls a PBX
placed through it.

The one thing that cannot be shared is a single call's **audio**. An IMS call
has one RTP bridge and its downlink is a single channel, so a second reader
would take roughly half the frames and leave the PBX's audio as choppy as the
browser's. Connecting browser audio to a call the trunk is carrying therefore
returns:

```
409 call_media_in_use
this call's audio is bridged to the SIP trunk; use the PBX's own client, or
hang up from the PBX first
```

The call still appears on the Calls page with its state, duration and SIP
status, and hanging up from there still works — it is only the audio bridge
that is exclusive. Calls placed from the Calls page are never claimed by the
trunk, so browser audio on those is unaffected.

## Why a trunk rather than a registrar

Putting registration in Asterisk keeps the security-sensitive part in software
that has had twenty years of attack on it, and keeps VoCat's side to what it
uniquely does: driving a SIM. It also means anything that speaks SIP — a
softphone, a desk phone, a WebRTC page — works without further work here.
