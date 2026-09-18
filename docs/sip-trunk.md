# SIP trunk (Asterisk integration)

VoCat can expose its SIM-backed calling as an ordinary SIP trunk, so a PBX in
front of it handles the things a PBX is good at — registration, softphones,
dial plans, voicemail, recording — while VoCat stays a gateway between SIP and
a modem's IMS registration.

This is deliberately **not** a registrar. There are no user accounts and no
digest authentication: peers are authorised by source address. Identity is the
PBX's job.

> **Status.** Outbound works, verified end to end: a Linphone softphone through
> Asterisk, out over a SIM's IMS registration to the PSTN, two minutes of
> two-way audio, torn down by the far end's `BYE`. Multi-SIM selection by
> `X-VoCat-Device` is proven on the same path.
>
> The inbound direction — a call arriving on the SIM being offered to the PBX —
> is **not implemented**. Nothing reaches the `[from-vocat]` context yet, so
> incoming calls are still answered from VoCat's own Calls page.

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

That account is a **seed**, not the permanent home of the configuration: the
entrypoint writes it once, on first start, and everything after that is
managed on the Asterisk page under **Extension accounts**. See
[Extensions in the web UI](#extensions-in-the-web-ui).

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

### Spreading calls across several SIMs

Name more than one and VoCat rotates across them, one call each in turn.
Write it with commas or spaces; the entrypoint normalises commas to spaces
before rendering the dial plan, because a comma inside an `extensions.conf`
application call is an argument separator and would break the `Set()` it
lands in:

```sh
VOCAT_DEVICE=usb-2c7c-0125-3-4-5,usb-2c7c-0125-3-4-6,usb-2c7c-0125-3-4-7
```

or every registered device, without listing IDs:

```sh
VOCAT_DEVICE=*
```

The same values work in the header and the URI parameter, so a dial plan can
rotate over one set of SIMs for one route and a different set for another:

Writing the list straight into a dial plan needs **spaces**, not commas —
nothing normalises it for you there, and commas would be parsed as extra
arguments to `Set`:

```ini
exten => _1NXXNXXXXXX,1,Set(__VOCATDEV=usb-2c7c-0125-3-4-5 usb-2c7c-0125-3-4-6)
exten => _011.,1,Set(__VOCATDEV=usb-2c7c-0125-3-4-7)
```

Two properties worth knowing:

- **Rotation skips SIMs that are not currently IMS-registered.** A card
  dropping out costs one device from the rotation, rather than every Nth call
  failing on a SIM that cannot dial.
- **An empty hint still refuses** when several devices are registered.
  Rotation is opt-in: naming several is the operator saying they have decided
  which SIMs may carry which calls, and `*` says the same about all of them.
  Silence is not that decision.

Rotation counts calls, not seconds, so it does not balance *load* — a
three-hour call and a ten-second one each consume one turn. For even airtime
rather than even call counts, use a per-route split in the dial plan.

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

### Is this container even running the build you think it is?

Every start logs it, as the first line:

```sh
docker compose logs vocat | grep "VoCat starting"
```

```
"msg":"VoCat starting","version":"v1.0.0.0-20-gd3809c2","build_time":"..."
```

`version: 0.1.0-dev` means the binary was built without `VOCAT_TAG`, which
`scripts/docker-build.sh` sets from git — so it is almost certainly not the
build you just made.

This matters more than it sounds. `docker compose up -d` **without `--build`**
resolves `image: ghcr.io/mengmengcode/vocat:${VOCAT_TAG:-latest}`, and with no
`VOCAT_TAG` in the environment that is `:latest` — whatever image happens to
be lying around under that tag, which may be days old. The container comes up
clean and reproduces bugs that were fixed long ago. Use
`scripts/docker-build.sh`, which sets `VOCAT_TAG` from `git describe` and
passes `--build`.

A trunk symptom with a known signature: a burst of `ACK` → `501 Not
Implemented` between Asterisk and port 5062, thousands per second. Answering
an ACK at all was a bug fixed in `8f8607c`; seeing it now means the binary
predates that. Current builds take every ACK silently, and also cap outbound
packets per peer, so a loop stops on its own and logs `siptrunk is dropping
packets to a peer`.

### "Could not read registrations: No Contacts found"

Not an error, and no longer reported as one. Asterisk answers an empty
listing with `Response: Error` and a message of that shape rather than an
empty success, so a PBX whose softphones have all unregistered looked broken.

A phone going `Unavailable` on its own is usually a mobile client that has
backgrounded: Linphone on iOS unregisters and relies on push, so it appears
only while the app is in the foreground or a call is up.

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

## Managing outbound routes from the web UI

The **Asterisk** page has an outbound route table: a match pattern, the SIMs
to rotate between, and a ring timeout. Save writes the dialplan; **Apply**
reloads it in Asterisk.

| Pattern | Devices | Timeout |
| --- | --- | --- |
| `_1NXXNXXXXXX` | `slot-4 slot-5` | 60 |
| `_011.` | `slot-6` | 90 |
| `_.` | `slot-4 slot-5 slot-6` | 60 |

Patterns must start with `_`. Without it Asterisk treats the value as a
literal extension that matches that exact string and nothing else — a rule
that looks right and never fires, so it is refused rather than saved.

**Apply reloads only `pbx_config`**, so calls in progress are unaffected and
no other module is touched. It needs the manager interface; without it the
page says so and the change takes effect on the next
`docker compose restart asterisk`.

Apply is disabled while there are unsaved edits. Reloading the previously
saved rules and reporting success would be worse than refusing.

### How it fits together

```
VoCat  ──writes──>  vocat-dialplan volume  <──reads──  Asterisk
                    routes.conf                        #include "vocat/routes.conf"
       ──AMI Reload (Module: pbx_config)──>
```

`[from-internal]` contains nothing but `include => vocat-routes`. That is
deliberate: Asterisk picks the best match *within* a context and only walks
includes when the context itself matches nothing, so any pattern left in
`[from-internal]` would make every generated route unreachable.

The include must always resolve, or `[vocat-routes]` is undefined and every
call is answered 404. The entrypoint therefore writes a default `routes.conf`
— everything out through `VOCAT_DEVICE`, matching the behaviour before the UI
existed — but only when the file is absent, so restarting cannot discard what
was configured.

### What the editor refuses, and why

Route patterns come from a browser and land in a file that can invoke
`System()`. Validation is a strict allowlist rather than escaping: commas,
semicolons, newlines, quoting and every form of variable expansion are
rejected outright. A comma alone would end the extension field and turn the
rest into arguments — which broke this deployment's entire dialplan once
already.

Duplicate patterns are refused too: Asterisk keeps one priority 1 per
extension, so the second would silently never run.

### Routes naming a SIM that is gone

Remove a device and any route still naming it keeps rendering a perfectly
valid dialplan. Apply succeeds, `dialplan show` looks right, and the call
fails at dial time with an error only the caller hears.

So the routes page reports device names that match no configured device,
checked the same way the gateway matches at dial time — by ID or by name,
case-insensitively on the name — rather than as a second opinion that could
disagree with it. `*` is not a name and is skipped: it means whatever is
registered at the time.

Nothing is removed automatically. A device that is unplugged today may be back
tomorrow, and deleting the route would lose a rule that was deliberate.

## The Asterisk page in VoCat

VoCat can show live PBX state — which extensions are registered, whether the
trunk is reachable, how many calls are up — on its own **Asterisk** page, next
to Voice calls. It reads this over Asterisk's manager interface (AMI).

It is **off unless a secret is set**, and the page says "not configured"
rather than pretending something is broken:

```sh
# setenv replaces a key if present (commented or not) and appends it if not.
# A plain `sed s/...` silently does nothing when the key is missing, which is
# what an .env created from an older .env.example looks like.
setenv() { sed -i "/^#*$1=/d" .env; printf '%s=%s\n' "$1" "$2" >> .env; }

setenv ASTERISK_AMI_SECRET "$(openssl rand -base64 24)"
./scripts/docker-build.sh
```

The delete-then-append matters: an `.env` copied from an older
`.env.example` has no `ASTERISK_AMI_SECRET` line at all, and a plain
`sed s/.../.../` against a missing key exits 0 having changed nothing. The
page then still reports "not configured" and nothing says why.

Check what actually reached the container rather than trusting the edit:

```sh
docker compose exec vocat printenv | grep ASTERISK_AMI_ADDR
```

Empty means the secret is not set, or the container predates the change —
environment is fixed when a container is created, so `docker compose restart`
will not pick it up. `scripts/docker-build.sh` recreates.

The same secret enables AMI in Asterisk and points VoCat at it, so there is
one value to set rather than two to keep in step.

### Live channels

The Asterisk page lists every channel the PBX is carrying, with a button to
end one. Both come from the same AMI poll the rest of the page already makes —
a second connection every few seconds would be one for nothing.

`CoreShowChannels` and `Hangup` are both covered by the `system` grant the
manager account already has (`EVENT_FLAG_SYSTEM | EVENT_FLAG_CALL` for Hangup,
`EVENT_FLAG_SYSTEM | EVENT_FLAG_REPORTING` for the listing), so nothing here
needs the `command` class that was deliberately withheld.

The channel name is **not** passed through. AMI's `Hangup` treats the value as
a regular expression when it is wrapped in slashes, so a channel of `/./`
would end every call on the PBX. VoCat lists the live channels first and
requires an exact match against one of them, which makes that unreachable: no
real channel name starts with a slash. It also gives the right answer for the
ordinary case, a channel that ended between the poll and the click.

Hang-ups use Q.850 cause 16, normal clearing — the call ended the way a call
normally ends, which is what pressing a button means.

### Registration history

The page shows what is true now, which answers "is it registered" and not
"when did it stop". A handset that drops for ninety seconds every hour is
invisible unless someone happens to be watching at that moment.

So a recorder samples contact state every thirty seconds, whether anyone is
looking or not, and writes **one row per change** — not one per poll. A table
with a row every thirty seconds answers "was it up at 03:14" and buries the
thing anyone actually looks for.

Four details that keep the history honest:

- **Seeded from what was last recorded.** Without that, the first poll after a
  restart writes a change for every contact that did not change.
- **A PBX that is down records nothing.** That is the recorder's view of the
  world, not the handsets', and filling the history with it would drown the
  real transitions.
- **An unqualified contact is skipped.** A contact with no status yet would
  otherwise produce a transition every time Asterisk restarts, which says
  nothing about the handset.
- **A contact that vanishes is recorded as `Unregistered`.** Asterisk simply
  stops mentioning one that has gone, so absence is the only signal there is.

Kept for 30 days. `GET /api/asterisk/registrations` reads it, and the response
says whether recording is on at all — an empty list on a PBX with no manager
interface means "off", not "quiet", and the two look identical otherwise.

### Why AMI rather than a shell

VoCat runs in its own container. Reading `asterisk -rx` output would mean
either a Docker socket in VoCat (root on the host, for a container that is
already privileged and host-networked) or a shell between containers. AMI is
a normal TCP interface designed for this, and it stays on loopback because
both containers share the host network namespace.

Three deliberate restrictions:

- **`bindaddr = 127.0.0.1`.** An AMI password crosses a plain connection in
  the clear, and AMI is privileged.
- **No `command` permission** on the manager account. `Command` runs arbitrary
  CLI inside the Asterisk container, which is shell-equivalent; the account
  gets `system,reporting`, which covers status and reloads.
- **No manager account at all when the secret is unset.** The entrypoint
  replaces `manager.conf` with `enabled = no` rather than leaving an inert
  account declared.

### VOCAT_DEVICE once routes are configured

`VOCAT_DEVICE` in `.env` seeds the **default** `routes.conf` that the
entrypoint writes when no routes file exists. Once routes are saved from the
web UI, that file is VoCat's and `VOCAT_DEVICE` no longer affects outbound
routing at all — the per-route device lists replace it.

It still matters for a fresh deployment, and for the one-line fallback if the
routes file is ever deleted.

### Field names

The labelled rows follow the Asterisk 22 manager documentation — `EndpointList`
gives `ObjectName`, `Transport`, `Aor`, `DeviceState`, `ActiveChannels`;
`ContactList` gives `Endpoint`, `Uri`, `Status`, `RoundtripUsec`, `UserAgent`,
`ViaAddr`, `ExpirationTime`.

Each value is still read through a short list of candidate names, documented
one first. Asterisk has renamed manager fields between versions, the fallbacks
cost one map lookup, and a row that blanks because a key moved is worse than
one showing a value under an older name.

Every endpoint also has a **Raw fields** toggle showing exactly what AMI
returned, so a field VoCat does not label is visible rather than dropped. If
something useful only appears there, say so and it can be promoted.

### Why the trunk needs a second query

`PJSIPShowContacts` lists only contacts that **registered**. The trunk's
contact is written into `pjsip.conf`, so it never appears there — on a PBX
with no softphone registered at all the listing is refused outright with
`No Contacts found`, which is a normal state and not an error.

The endpoint's own `EndpointList` event names its contact, so the row shows
the URI, but nothing in either listing says whether that contact answers a
qualify. The trunk therefore read "Not qualified" for ever while it was
carrying calls.

`PJSIPShowEndpoint`, one endpoint at a time, answers with a
`ContactStatusDetail` per contact — `URI`, `Status`, `RoundtripUsec`,
`UserAgent`, `ViaAddress`, `RegExpire` — which covers static and dynamic
contacts alike. VoCat issues it only for endpoints that still have a contact
with no status, so a PBX whose phones are all registered spends no extra
round trips, and at most 16 per page load in any case.

Note the spellings: this event says `URI`, `ViaAddress` and `RegExpire` where
`ContactList` says `Uri`, `ViaAddr` and `ExpirationTime`. Lookups are
case-insensitive and each field is read through a candidate list, so both
arrive under the same labelled row.

Anything the contact listing already reported wins — it is the more specific
source, and letting the detail overwrite it would make the round-trip flip
between two measurements on every poll.

## Extensions in the web UI

Softphone accounts are edited on the Asterisk page, next to the outbound
routes. Each row is a name, a password, an optional display name, and how
many devices may register it at once; **Apply** reloads PJSIP so the change
takes effect without restarting the container.

VoCat renders them into two files in the directory both containers share:
`endpoints.conf`, which `pjsip.conf` includes, and `internal.conf`, which
`extensions.conf` includes. One account is three PJSIP objects with the same
name — the endpoint (what it may do), the auth (how it proves who it is) and
the AOR (where it is) — which is how PJSIP models an account; they are
generated together so a missing one cannot happen.

### Dialling another extension

Saving also writes `internal.conf`, a `[vocat-internal]` dialplan context with
one exact-match rule per account:

```
exten => 1002,1,Dial(PJSIP/1002,30)
 same => n,Hangup()
```

Note the absence of `@vocat`. That suffix is what sends a call out through the
trunk to a SIM; an internal call stays on the PBX.

`[from-internal]` includes `vocat-internal` **before** `vocat-routes`, and the
order is what makes this work. Asterisk searches a context's own extensions
first, and only then walks its includes — taking the first include that
matches, not the best match across all of them. So `1002` resolves in the
internal context and never reaches the route patterns, while `12125551234`
matches nothing internal and falls through to the routes.

Exact rules rather than a pattern like `_1XXX`, because a pattern has to guess
the numbering plan: guess too narrow and an account is silently not dialable,
guess too wide and Asterisk tries to ring an endpoint that does not exist. The
account list is right there, so neither is necessary.

Both files come from that one list, which is the point — adding 1003 in the UI
makes it registerable *and* dialable from 1001 in the same Apply, with no
second file to remember. Apply therefore reloads two modules: `res_pjsip` for
the accounts and `pbx_config` for the dialplan. Reloading only the first would
leave a new account registering perfectly and unreachable from every handset.

Emergency numbers — `911`, `933`, `112`, `999`, `000`, `110`, `119` — are
refused as extension names. Internal dialling is consulted first, so an
account called `911` would shadow the route that reaches emergency services,
and the only way to discover that is to dial it for real.

The same shadowing is possible with any name that collides with a number you
route outbound: an extension named `18005551212` would ring a handset instead
of placing the call. Emergency numbers are guarded because the consequence is
not recoverable; the rest is left to whoever picks the numbering plan.

### Passwords are write-only

A password is accepted on save and **never returned**. The listing carries
`has_password` and nothing else, the preview shows `password=<hidden>`, and
the generated file is written `0600`.

So copy a password before saving it. The field is deliberately not masked
while you type — this is the only moment it is readable — and the key button
generates one from the browser's CSPRNG, out of an alphabet with no
ambiguous characters, because it gets typed into a handset by hand.

Stored in VoCat's database, marked sensitive so the generic settings API
cannot hand it out with everything else. It is stored recoverably rather than
hashed because Asterisk needs the password itself to answer a digest
challenge; a VoCat database backup therefore contains SIP credentials.

### Replacing the seeded account

Until something is saved here, `endpoints.conf` is the one the entrypoint
seeded from `ASTERISK_SIP_USER` and `ASTERISK_SIP_PASSWORD`. The page says so,
because saving replaces the file whole.

Saving an **empty** list over a seeded file is refused once and asks for
confirmation: the symptom otherwise is a handset that quietly stops
registering at its next attempt, some minutes later, with nothing in the UI
that points at the cause. Saving a list that has accounts in it replaces the
seeded one without asking — typing an account in is already the decision.

`ASTERISK_SIP_USER` still names the extension the inbound context rings
(`[from-vocat]` in `extensions.conf`), which is unused today because the
trunk does not offer inbound calls yet. If you rename the account here, that
line needs the same edit.

### Names that are refused

`vocat`, `transport-udp`, `transport-tcp`, `global`, `system` and `general`
are already section names in the shipped `pjsip.conf`. A second object with
one of those names is not an override — Asterisk refuses the duplicate, and
what else it takes down with it depends on where it gives up. They are
refused here instead.

Names are letters, digits, `_`, `-` and `.`; passwords are printable ASCII
with no spaces and no `;`, at least 12 characters. A `;` starts a comment in
an Asterisk config file, so a password containing one would be silently
truncated: Asterisk would load happily and the phone would never register.

## Inbound: a call on the SIM offered to the PBX

Set `VOCAT_SIP_TRUNK_PBX` (the Asterisk overlay defaults it to
`127.0.0.1:5060`) and a call arriving on a SIM becomes an INVITE toward
Asterisk:

```
INVITE sip:13105557777@127.0.0.1:5060 SIP/2.0
From: <sip:12125551234@127.0.0.1:5062>;tag=...
X-VoCat-Device: usb-2c7c-0125-3-4-4
X-VoCat-Device-Name: SLOT1-1
```

The request URI is the **dialled** number — the SIM's own — so a dialplan can
route by DID. The From is the caller, which becomes the PBX's caller ID. The
two `X-VoCat-Device` headers name the SIM, the same header the outbound
direction reads, in the opposite direction.

Asterisk's `[vocat] type=identify match=127.0.0.1` is what places the call in
`[from-vocat]`, which includes the generated `[vocat-inbound]`. What that
rings is a mode chosen on the Asterisk page — see
[Inbound routing](#inbound-routing).

### The SIM is answered last

VoCat does **not** answer the call when it offers it. It waits for the PBX's
200 OK — a handset actually picking up — and only then answers the carrier's
INVITE.

The order matters twice over. Answering first would connect the caller to
silence while the phone was still ringing, and it would start billing them for
it. It also means the caller hears the carrier's own ringback, which is the
correct tone for their network, rather than whatever the PBX would generate.

Every failure path releases the SIM leg: a PBX that rejects the call, one that
never answers, one that is not running at all. A call left ringing at the
carrier because VoCat gave up quietly is the failure this is written to avoid.

### It rings in both places

The Calls page still raises the call. Both are live at once and whichever
answers first takes it; the other sees the call disappear. That is deliberate
— the browser is the fallback when the PBX is misconfigured, which is exactly
when you need one.

Set `VOCAT_SIP_TRUNK_PBX=` (empty) in `.env` to keep inbound calls off the PBX
entirely.

### What the INVITE does on the wire

UDP, so it is retransmitted at 500 ms, doubling to a 4 s ceiling, until
something answers — Timer B at 32 s gives up if nothing ever does. A 2xx is
ACKed to the Contact it carried, and a retransmitted 2xx (which means the ACK
was lost) is answered with the same ACK rather than ignored; without that the
PBX tears the call down about thirty seconds in.

The SDP offer lists **both** G.711 flavours, unlike the answer the outbound
direction sends. The trunk is asking rather than agreeing here, and which one
a PBX prefers is its own configuration; the RTP leg takes its codec from
whichever the answer picks.

## Inbound routing

Where a call arriving on a SIM rings is a mode on the Asterisk page, not a
mapping table. The three are exclusive — there is no fallback between them,
because a fallback is exactly what makes "why did that call ring the wrong
phone" unanswerable.

### Ring the extension named after the number

The DID *is* the extension name, so this needs no configuration at all: VoCat
puts the dialled number in the request URI, and the generated context has one
exact rule per extension.

```
exten => 13105557777,1,Dial(PJSIP/13105557777,30)
exten => +13105557777,1,Dial(PJSIP/13105557777,30)
```

Both forms are matched because which one the carrier sends is its choice, not
something an operator should have to guess at. A number with no extension of
its own is rejected with cause 1, "unallocated number" — true, and more useful
to the carrier than a silent drop.

The side effect is worth knowing: `[vocat-internal]` is consulted before the
outbound routes, so once extension `13105557777` exists, another handset
dialling that number reaches the desk phone rather than going out over the
SIM. For your own number that is the right answer; it does mean never naming
an extension after a number you do not own.

### Ring every extension at once

Ignores what was dialled. An empty ring group means *every* configured
extension, so a handset added later joins without a second edit; naming a
group explicitly overrides that.

This is the default, and deliberately so: it is the mode least likely to lose
a call, which matters for whatever runs before anyone configures anything.

### Ring the list in order

Each in turn, moving on when one does not answer. The generated context checks
`DIALSTATUS` between steps:

```
 same => n,Dial(PJSIP/1001,15)
 same => n,GotoIf($["${DIALSTATUS}" = "ANSWER"]?done)
 same => n,Dial(PJSIP/1002,15)
 same => n(done),Hangup()
```

Without that check the next handset would start ringing the moment the first
one hung up — `Dial` returns when an answered call *ends*, not only when it
fails.

There is no round-robin. Round-robin distributes load; inbound wants someone
to answer, and a rotation will send a call to the one handset nobody is near
while the others sit idle. A real one also needs persistent state in the
dialplan, which is racy under concurrent calls and resets on reload. That is
what `app_queue` is for.

### When a ring group loses an extension

A group naming an account that does not exist rings nothing, and a caller who
reaches no one is the only signal. So the plan is validated against the
configured extensions when it is saved, and revalidated whenever the extension
list changes: deleting an extension a group names falls back to ringing
everything rather than leaving a file that rings nothing.

### Creating an extension per SIM

VoCat already learns each SIM's own number from IMS registration, so the
extension editor offers them directly — copying numbers between two screens is
how a digit gets transposed and a DID silently rings nothing.

**Import from SIM numbers** adds a row per number that has no extension yet,
each with its own generated password, reduced to digits so it can be a PJSIP
section name. The rows are filled in rather than saved: those passwords are
visible exactly once, because they are write-only the moment they are stored.
Copy them, then Save.

Rows carry a checkbox for deleting several at once. Deleting every extension
is allowed — the inbound file then rejects calls with the same truthful cause
rather than the save being refused as if it were a mistake.

## Hold and resume

A PBX holds a call with a re-INVITE that changes the media direction, and
resumes with another. The trunk answers both in their own transaction and
leaves the RTP legs alone — tearing them down and rebuilding would drop audio
on resume for nothing.

Before this, a re-INVITE was mistaken for a retransmitted INVITE and answered
by replaying the original 200. That is worse than ignoring it: the replayed
response carries the *first* INVITE's CSeq, so it matches no transaction the
PBX has open. The PBX retransmits, times out, and tears the call down. A
re-INVITE is told apart by its CSeq number — a retransmission repeats it, a
renegotiation increments it.

The direction is mirrored, as RFC 3264 §6.1 requires: a peer that says it will
only send is told it will only receive. Backwards is how a held call comes
back with audio in one direction and nothing to explain it.

What each direction does to the bridge:

| The PBX offers | Audio to the PBX | Audio to the SIM |
| --- | --- | --- |
| `sendrecv` | yes | yes |
| `sendonly` (music on hold) | no | yes |
| `recvonly` | yes | no |
| `inactive` | no | no |

Both directions keep **reading** while a call is held; only the forwarding
stops. A reader that stopped would let the queue behind it fill, and resume
would then replay whatever was said during the hold.

`c=0.0.0.0` is accepted too. It is how RFC 2543 held a call and plenty of
equipment still does it; refusing it as malformed would tear down a call that
was only asking for silence. It parses to no address at all, which is exactly
what inactive means, so the leg simply sends nowhere.

This works in both directions. A re-INVITE on a call the *trunk* offered — a
softphone holding an inbound call — used to match no dialog, fall through to
the new-call path, and place a second call out through a SIM.

`UPDATE` is still answered 501. Asterisk uses re-INVITE for hold, and an
UPDATE has different rules about when it may arrive; it is not worth guessing
at until something sends one.

## Keypad digits

Digits cross both legs as RFC 4733 telephone events — a named event in its own
RTP payload type, not audio.

They cannot be tones. Every leg here runs a speech codec: G.711 at best, and
an IMS call usually negotiates AMR, which mangles a pair of pure tones just
enough that the IVR on the far end hears nothing, or hears a different digit.
A digit that silently does not arrive is worse than one that is refused, so
VoCat refuses rather than falling back to tones.

### Across the trunk

The PBX leg offers `telephone-event/8000` as payload 101 and answers with
whatever number the PBX's own offer used — an answer may only name a payload
type the offer listed, so a PBX with DTMF turned off gets a call with no
telephone events rather than a renegotiation it did not ask for.

The two legs negotiate their event types separately, and their clocks are
unrelated, so a digit is **re-generated** on the far leg rather than forwarded
packet for packet. One press becomes eight 20 ms tone packets sharing a
timestamp, then the end packet repeated three times, then a three-frame gap —
the gap is the only thing that makes `11` two digits rather than one held key.

### From the browser

The Calls page has a keypad on an answered call. It does not need browser
audio connected: the digit is generated server-side on the call's own RTP
stream, so it works whether or not anyone is listening through the browser.

```
POST /api/devices/{id}/calls/dtmf
{"call_id": "...", "digits": "*123#"}
```

`409` means the carrier declined `telephone-event` in its SDP answer, so this
call cannot carry digits at all. `501` means the call is on the modem's
circuit-switched path rather than IMS, where VoCat has no RTP stream to put
events on.

## Call records

Every call lands in `call_records`, whoever placed it: VoCat's own Calls page,
a softphone through the trunk, or an automatic task. The history is on the
Calls page, filterable by number and outcome.

They are written by **observing** live call state, not by the code that dials,
answers or hangs up. Each of those knows one moment and none of them sees a
call the far end ended, one that failed while ringing, or one the PBX placed.
The IMS session already tracks all of it and keeps a finished call in its list
for thirty seconds, so sampling that every two seconds is the one place that
catches everything — including a rejection that never rang.

A few decisions worth knowing:

- **Duration counts from the answer**, not from the first packet. A call that
  rang for a minute and was never picked up lasted zero seconds, which is what
  a carrier bills and what the list shows as a dash.
- **A call in progress has no disposition.** The column means how a call
  finished, not how it looked at the last poll, so a live call is blank rather
  than provisionally "no answer".
- **Later writes never erase earlier ones.** One call is seen several times as
  it progresses, and a poll catching it mid-teardown can report less than the
  one before. Without that rule a completed call ends up a blank row.
- **The source says who drove it.** The trunk notes the IMS calls it places
  and answers, and that note deliberately outlives the hang-up — the recorder
  sees the finished call afterwards, and the trunk's own claim on the call is
  released the moment it hangs up.

Records are pruned after 90 days. The API is read-only
(`GET /api/calls/records`): a history that could be edited would be a claim
rather than a record.

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
