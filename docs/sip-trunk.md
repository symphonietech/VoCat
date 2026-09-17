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
| Transport | UDP |

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
| `Dockerfile` | Debian bookworm plus the packaged Asterisk 20 (an LTS release that gets Debian's security updates) — not a third-party image, since a SIM's call charges sit behind this |
| `entrypoint.sh` | Renders the templates into `/etc/asterisk`, then runs Asterisk in the foreground |
| `templates/pjsip.conf` | The trunk endpoint and one softphone account |
| `templates/extensions.conf` | Outbound dial plan, `[from-vocat]` ready for inbound |
| `templates/rtp.conf` | Ports 10000-10200 |

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
; Anything that looks like a number goes out through the SIM.
exten => _X.,1,Dial(PJSIP/${EXTEN}@vocat,60)
 same => n,Hangup()

[from-vocat]
; Inbound from the SIM rings the softphone. Not reached yet -- see Status.
exten => _X.,1,Dial(PJSIP/1001,30)
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

VoCat logs each handled request:

```
siptrunk handled a request  peer=127.0.0.1:5060 method=OPTIONS status=200
```

and says why it dropped one:

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

## Why a trunk rather than a registrar

Putting registration in Asterisk keeps the security-sensitive part in software
that has had twenty years of attack on it, and keeps VoCat's side to what it
uniquely does: driving a SIM. It also means anything that speaks SIP — a
softphone, a desk phone, a WebRTC page — works without further work here.
