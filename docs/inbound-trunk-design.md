# Inbound SIP trunk — design

**Status: proposal. No code written.** Calls arrive on an external SIP trunk
(an ITSP or another PBX) and VoCat relays them out through a SIM to the PSTN —
a SIP-to-cellular gateway — with the trunk configured from the web UI.

Read [Toll fraud shapes this design](#toll-fraud-shapes-this-design) before the
rest. It is not a caveat at the end; it decides the structure.

---

## The hard part already exists

A call entering Asterisk and leaving through a SIM is exactly what
`[vocat-routes]` does today. The only thing that changes is **where the call
enters Asterisk**. VoCat's trunk, the `X-VoCat-Device` selection, the RTP
bridge and the call records need no changes at all.

What is new:

1. A PJSIP endpoint in Asterisk for the external peer.
2. A routing context per trunk.
3. A GUI and API to manage both.
4. Admission control — the part that does not exist in any form today.

---

## Toll fraud shapes this design

An inbound trunk that can dial out through SIMs is the most attacked
configuration in telephony. If the peer address is spoofable, or credentials
leak, or a context is too permissive, **someone else runs up the SIM's call
charges** — and unlike a SIP provider there is nobody to claw it back from.

Five rules follow, and the design below exists to enforce them:

- **A trunk never reaches `from-internal`.** Each gets its own context that can
  reach nothing but its own permitted destinations.
- **Destinations are allowlisted, never denylisted.** `_1NXXNXXXXXX`, not
  `_X.` with exclusions.
- **SIMs are allowlisted per trunk**, so one compromised peer cannot reach
  every card.
- **Every trunk carries a concurrency cap**, so one peer cannot drain the pool.
- **A new trunk routes nowhere until destinations are added.** The GUI's
  default state is deny.

---

## Dialplan: one context per trunk

**Decided: a private context per trunk, sharing nothing.** No
`include => vocat-internal`, no `include => vocat-routes`. A trunk cannot dial
a softphone extension and cannot reach a route pattern configured for internal
use.

```ini
[from-trunk-acme]
exten => _1NXXNXXXXXX,1,Set(GROUP(trunk)=acme)
 same => n,GotoIf($[${GROUP_COUNT(acme@trunk)} > 4]?full)
 same => n,Set(__VOCATDEV=SLOT1-1 SLOT2-1)
 same => n,Dial(PJSIP/${EXTEN}@vocat,60,b(vocat-predial^s^1))
 same => n,Hangup()
 same => n(full),Congestion()
exten => _.,1,Hangup(1)

[from-trunk-beta]
exten => _44X.,1,Set(GROUP(trunk)=beta)
 same => n,GotoIf($[${GROUP_COUNT(beta@trunk)} > 2]?full)
 same => n,Set(__VOCATDEV=SLOT3-1)
 same => n,Dial(PJSIP/${EXTEN}@vocat,60,b(vocat-predial^s^1))
 same => n,Hangup()
 same => n(full),Congestion()
exten => _.,1,Hangup(1)
```

Two details that are easy to get wrong:

**Device IDs are separated by spaces, not commas.** A comma inside an
`extensions.conf` application call is an argument separator and would break the
`Set()` it lands in. This is the exact bug commit `4be901b` fixed for
`VOCAT_DEVICE`; the renderer must not reintroduce it.

**The `b()` pre-dial handler is mandatory.** `PJSIP_HEADER(add,...)` applies to
the channel it runs on, so the header has to be added on the outbound leg.
`[vocat-predial]` already exists and is reused unchanged.

The cost of private contexts is duplication — five trunks with the same NANP
rules produce five copies of the same patterns. That is acceptable precisely
because the file is generated: the GUI holds each trunk's rules once, and
duplication in generated output buys isolation for nothing.

A per-trunk **"also allow the shared outbound routes"** checkbox may be offered,
emitting `include => vocat-routes` into that context. It defaults to **off**: it
re-opens the path the private context closes.

---

## Admission control: three separate limits

These are often conflated. They answer different questions and are enforced in
different places. A call must pass all three.

| Limit | Question | Where enforced |
| --- | --- | --- |
| Per trunk | Has *this peer* exceeded its share? | Asterisk dialplan, `GROUP_COUNT` |
| Per SIM pool | Are two trunks oversubscribing shared cards? | Asterisk dialplan, `GROUP_COUNT` |
| Per SIM (always 1) | Can any card actually take this call? | VoCat, `ResolveDevice` |

### Why `GROUP_COUNT` for the first two

`GROUP()` tags the current channel; `GROUP_COUNT()` counts live channels with
that tag. The useful property is that a channel leaves its group automatically
on hangup — no counters to maintain and nothing to leak when a call dies badly.

The alternatives do not work here:

- **`call-limit` in `sip.conf`** belonged to `chan_sip`, removed upstream in
  Asterisk 21, and this repo `noload`s it anyway
  (`asterisk/templates/modules.conf:24`).
- **PJSIP `device_state_busy_at`** looks right and is not: it changes when the
  endpoint *reports* busy device state, for hints, BLF and queues. It does not
  reject calls.

PJSIP has no native maximum-concurrent-calls setting, so admission control
lives in the dialplan.

**Limitation, worth stating plainly:** `GROUP_COUNT` is check-then-act, not an
atomic semaphore. Two calls arriving in the same instant can both read the count
before either is admitted, so the cap can be exceeded by one or two. That is
fine for fraud containment and overload protection, which is what it is for. It
is not a hard licence limit. The count is also in-memory per Asterisk instance
and resets on restart.

### Why the third limit cannot live in the dialplan

Asterisk sees one trunk endpoint and has no idea which SIMs are busy.
`GROUP_COUNT` would happily admit a call when every modem is occupied. Only
VoCat knows, so that check belongs in `ResolveDevice`.

---

## The idle filter in `ResolveDevice`

### What exists today

`ResolveDevice` (`internal/server/sip_trunk_gateway.go:69`) filters candidates
on `callTransport(id) == "vowifi"`, which is `state.IMSReady`
(`internal/server/call_api.go:224`) — **registration only**. `Dial` then calls
`controller.DialCall` with no busy check at all. A second call can therefore be
handed to a modem already carrying one.

### The data is already reachable, and reading it is cheap

`VoWiFiCallController` (`internal/server/server.go:225`) already includes
`Calls(string) ([]vowifi.Call, error)`, and `sipTrunkGateway.controller()`
already type-asserts to that interface to obtain `DialCall`. Nothing new has to
be plumbed.

The cost is an in-memory read, not a modem round trip. The signature is the
tell (`internal/vowifi/types.go:447`):

```go
type CallController interface {
    Calls() []Call                                  // no ctx, no error
    DialCall(context.Context, string) (Call, error) // ctx -> does I/O
}
```

`Calls()` takes no context and returns no error because it cannot block or
fail — it hands back the IMS session's already-maintained list. No `+CLCC`, no
AT command, no serialisation on the modem mux. `Orchestrator.Calls()`
(`internal/vowifi/orchestrator.go:592`) is a mutex-guarded field read;
`Manager.Calls(deviceID)` (`internal/vowifi/runtime/manager.go:352`) adds an
`Ensure` and a map lookup.

**So the check runs per inbound INVITE**, which is the right choice. It costs
microseconds against a SIP transaction that has seconds of Timer B budget, and
it is strictly better than caching: a cache would reintroduce exactly the
staleness that makes pushed registration state the wrong answer.

### Finding: a count of calls is the wrong predicate

`Session.Calls()` (`internal/vowifi/ims/call_runtime.go:50`) **deliberately
retains finished calls** for `terminalCallRetention = 30 * time.Second`
(line 22), pruning them only on a later call once that window has passed.

`len(calls) > 0` would therefore mark every SIM busy for **30 seconds after each
call completes** — roughly halving trunk throughput on short calls, and looking
exactly like a flaky modem. This is a real trap and is not visible from the
`Calls()` signature.

### The predicate

Every state that occupies the modem counts as busy. Terminal states do not.

| State | Busy? |
| --- | --- |
| `dialing`, `ringing`, `accepted`, `active`, `held` | **yes** — the modem is committed |
| `ended`, `failed` | no — retained for reporting only |

Test it by `EndedAt`, not by a string allowlist:

```go
// Fragile: a state added later defaults to idle.
busy := state != "ended" && state != "failed"

// Preferred: EndedAt is the runtime's own terminal marker.
busy := call.EndedAt == nil
```

`EndedAt` is set exactly when a call reaches a terminal state
(`call_runtime.go:969`) and cleared again if it ever leaves one (lines 918-919).
A future state such as `holding` or `transferring` is then busy by default,
which is the safe direction. A string allowlist would silently treat it as idle.

Sketch, at the point the readiness filter already runs:

```go
calls, err := controller.Calls(config.ID)
if err != nil {
    continue // cannot ask -> do not use it; failing open dials a modem blind
}
for _, call := range calls {
    if call.EndedAt == nil {
        busy = true
        break
    }
}
```

When every named device is busy, return an error so the trunk replies `503` and
the dialplan can fall through.

**Put the predicate in one exported helper** — `vowifi.CallOccupiesDevice(call)`
or similar — rather than open-coding it in `ResolveDevice`. The trunk gateway, a
future "SIM busy" indicator in the GUI and any concurrency accounting must all
agree, or the 30-second retention gets rediscovered the hard way by whoever
writes the second one.

### Not `noteTrunkCall`

`callOrigin` (`internal/server/call_records.go:153`) is a **provenance map for
the call recorder** — it records that a call came from the trunk rather than the
browser. It is keyed by device and call ID and bounded **by age, not by call
lifetime**: entries are evicted only when the map fills, and only if older than
24 hours. Using it for busy detection would mark a SIM busy for a day. It is not
a liveness signal.

### Residual race

Two INVITEs arriving in the same instant can both see the same SIM idle. The
window is microseconds rather than the seconds a pushed-state design would
have, and the loser gets a failure from the modem that surfaces as `503`, which
the dialplan already handles. Not worth a lock.

---

## Generated files

Two new files, following the existing pattern exactly. The templates gain one
`#include` each, beside the four already there (`extensions.conf:12,16,20`,
`pjsip.conf:77`).

| File | Contains | Included from | Reloaded by |
| --- | --- | --- | --- |
| `trunks.conf` | PJSIP `endpoint` / `auth` / `aor` / `identify` per trunk | `pjsip.conf` | `res_pjsip` |
| `trunk-routes.conf` | One `[from-trunk-<id>]` context per trunk | `extensions.conf` | `pbx_config` |

Holding credentials, `trunks.conf` is written `0600`, exactly as
`endpoints.conf` is.

Generated shape for an IP-authenticated trunk:

```ini
[trunk-acme]
type=endpoint
context=from-trunk-acme      ; its own context, never from-internal
disallow=all
allow=ulaw
allow=alaw
direct_media=no              ; VoCat bridges the audio itself
aors=trunk-acme

[trunk-acme]
type=identify
endpoint=trunk-acme
match=203.0.113.10
```

Three authentication modes, in likely order of need. Ship the first two:

1. **IP identify** — the provider sends from a fixed address (`type=identify`).
2. **Inbound auth** — the provider authenticates with credentials (`type=auth`).
3. **Outbound registration** — VoCat's Asterisk registers to them
   (`type=registration`). Add when someone needs it.

---

## Data model, API and GUI

**No schema migration.** Asterisk configuration in this repo is JSON in
`app_settings` under keys like `asterisk.routes`
(`internal/server/asterisk_routes_api.go:22`), read and written through
`s.store.AppSetting` / `UpsertAppSetting`. A new `asterisk.trunks` key follows
suit. This makes the feature meaningfully cheaper than anything needing a
migration.

`GET|PUT /api/asterisk/trunks` and `POST /api/asterisk/trunks/apply`, mirroring
the routes endpoints. Apply writes both files and reloads both modules — one
edit changes a PJSIP object list *and* a dialplan, so both reloads are needed,
the same way saving an extension does today.

A new tab on the existing **Asterisk** page: trunk list plus an editor for
name, peer host/port, transport, auth mode, credentials, codecs, permitted
destination patterns, permitted SIMs, and maximum concurrent calls.
Credentials follow the write-only pattern already used for extension
passwords.

---

## Container consequence: this needs a rebuild

Templates are bind-mounted and re-rendered at every start, so the two new
`#include` lines are a `docker compose restart asterisk`.

But `entrypoint.sh` must also seed both new files on first start, so the
unconditional includes always resolve — the pattern every one of the four
existing generated files follows. `entrypoint.sh` is baked into the image, so:

```sh
docker compose build asterisk && docker compose up -d asterisk
```

This puts the feature in the same category as `137eaf9` and `dc9313e`, the
commits that added the extension and inbound files.

---

## Work breakdown

| Piece | Size |
| --- | --- |
| `asteriskconf` renderers for both files, plus tests | M |
| `asterisk.trunks` storage, API, apply and reload | M |
| Template includes and entrypoint seeding | S |
| Idle filter in `ResolveDevice` plus the shared predicate helper | S |
| GUI tab and editor | L |
| Security controls: destination and SIM allowlists, concurrency caps | M |
| Docs in `docs/sip-trunk.md` | S |

**Roughly 2-3 focused days.** Cheaper than the RBAC work — no migration, no new
auth model — but the security controls are not optional trim and the GUI is the
bulk of it.

---

## Decisions taken

1. **One private context per trunk**, sharing no includes. Settled.
2. **All non-terminal call states count as busy**, tested via `EndedAt == nil`.
   Settled.
3. **`GROUP_COUNT` for per-trunk and per-pool caps**; the per-SIM limit stays in
   VoCat. Settled.
4. **No pushed registration state.** VoCat evaluates at dial time, which is
   fresher than any push and has no staleness window.

## Open questions

1. Does the per-SIM-pool cap ship in the first version, or only the per-trunk
   one?
2. Does the "also allow shared outbound routes" checkbox ship at all, or wait
   for someone to ask?
3. Outbound registration (`type=registration`) — first version or later?
4. When every permitted SIM is busy, is `503` right, or should the trunk get
   `486 Busy Here` so the peer's own failover treats it as congestion rather
   than an outage?
