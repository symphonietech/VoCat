# SMS over the SIP trunk — design

**Status: proposal. No code written.** SMS arriving on a SIM is delivered to the
softphone registered under that SIM's number, and a softphone can send SMS out
through the SIM whose number it holds. Both directions ride the trunk that
already carries calls, as SIP MESSAGE (RFC 3428).

---

## Both directions in one picture

```
inbound   carrier ──SMS──▶ SIM ──▶ VoCat ──MESSAGE──▶ Asterisk ──MESSAGE──▶ handset
                                   store        Request-URI = the SIM's number

outbound  handset ──MESSAGE──▶ Asterisk ──MESSAGE──▶ VoCat ──▶ SIM ──SMS──▶ carrier
                                          From = the sending extension
                                                       ▲
                                          later: delivery report back the same way
```

The Request-URI user on the way in is the **SIM's own number**, so the extension
named after that number receives it -- the same convention the `did` inbound
call mode uses. One extension per SIM then serves calls, texts and send
authorisation with no second mapping table.

---

## Inbound: SIM to extension

### It is another notification channel

`runSMSNotificationChannel` (`internal/server/sms_notifications.go:66`) is a
cursor-based poller over `ListInboundSMSAfterID` (`internal/store/sms.go:371`),
gated by `smsMessageReadyToNotify`. Adding the forwarder to
`smsOnlyNotificationChannels` (line 27) rather than hooking the ingest sites
gets four things for nothing:

- **Both ingest paths.** It reads the store, so IMS and AT-modem SMS both
  forward with no code in either.
- **Multipart reassembly**, through `ConcatSMSReadyToNotify`: a three-part SMS
  forwards once, whole, rather than as three fragments.
- **No replay on restart.** The cursor initialises to the newest row, so a
  restart does not re-deliver yesterday's messages to somebody's handset.
- The existing error throttling and back-off.

### What VoCat sends

```
MESSAGE sip:15551230000@127.0.0.1:5060 SIP/2.0
Via: SIP/2.0/UDP 127.0.0.1:5062;branch=z9hG4bK…
From: "支付宝" <sip:sms@vocat>;tag=…
To: <sip:15551230000@127.0.0.1:5060>
Call-ID: …
CSeq: 1 MESSAGE
X-VoCat-Device: SLOT1-1
Content-Type: text/plain;charset=UTF-8
Content-Length: <bytes>

<the SMS text>
```

Request-URI user is `LocalPhone`, the SIM's own number. In `internal/siptrunk`
this is a new `SendMessage`: an out-of-dialog transaction, far simpler than the
INVITE path (`inbound.go:429`) -- no dialog, no media, no ACK -- reusing
`via()`, `contactURI()`, `send()` and the response demux, giving up at Timer F
(64*T1, 32s).

### Keeping the true sender

A numeric sender becomes the user part: `<sip:+8613800138000@vocat>`.

An **alphanumeric sender ID** -- `支付宝`, `Verizon`, `HSBC` -- is not a legal
SIP user part, and sanitising it would destroy exactly the information the
recipient needs. So it goes in the **display name**, which is a quoted string
and can hold it, with a fixed `sms@vocat` user part. Most softphones show the
display name, so the true sender stays visible either way.

`MessageSend(pjsip:1001,${MESSAGE(from)})` then passes it through to the
handset unchanged.

### The Asterisk trap

`res_pjsip_messaging` routes an inbound MESSAGE into the endpoint's
**`message_context`**, falling back to `context`. `[vocat]`'s context is
`from-vocat`, which includes `vocat-inbound` -- a *call* dialplan. Without a
separate context an arriving SMS matches `exten => 15551230000` and
**`Dial()`s the handset**: the phone rings when a text arrives.

So `message_context=vocat-messages` on `[vocat]`, and a generated context:

```ini
[vocat-messages]
exten => 15551230000,1,NoOp(SMS for ${EXTEN} from ${MESSAGE(from)})
 same => n,MessageSend(pjsip:15551230000,${MESSAGE(from)})
 same => n,Hangup()
exten => _.,1,Hangup()
```

`modules.conf` sets `autoload=yes`, so `res_pjsip_messaging` is already loaded.

### Modes

| Mode | What it does |
| --- | --- |
| `off` | **Default.** Nothing is forwarded. |
| `did` | Deliver to the extension named after the SIM's number. |
| `trunk` | Deliver out to a named external trunk. |

`off` is the default deliberately: this is new behaviour and must not switch
itself on for a deployment that upgrades.

`trunk` uses `MessageSend`'s endpoint-and-URI form against the trunk objects
the external trunk feature already builds:

```ini
MessageSend(pjsip:trunk-acme/sip:2001@203.0.113.10,${MESSAGE(from)})
```

There is no `all` mode. Ringing every handset for a call is a reasonable
fallback; copying every text to every handset is not.

---

## Outbound: extension to SIM

### The rule

VoCat accepts a MESSAGE from the trunk only when the **sending extension's
number equals a SIM's number**. It then sends through that SIM. Otherwise it
refuses.

| Outcome | Response |
| --- | --- |
| No SIM has that number | `403 Forbidden` |
| Accepted for delivery | `202 Accepted` |

`403` rather than `404`: the destination exists, the sender is not allowed to
use it. `202` rather than `200`: VoCat has accepted the message for delivery,
not delivered it -- the SIM submission and the carrier's delivery both come
later.

Once accepted it goes to the same send path `/api/sms/send` uses, so
IMS-or-modem selection, multipart segmentation and history come free.

### Where the identity comes from

Registration and authentication already establish which extension sent a
message; Asterisk knows it, and nothing extra is needed to prove it. One
subtlety decides how that knowledge reaches VoCat:

**`${MESSAGE(from)}` is the From header the handset wrote, not Asterisk's
conclusion about who it is.** Asterisk authenticates the endpoint, uses that to
choose the `message_context`, and then hands the dialplan the claimed From
unchanged. So the generated dialplan must not forward that variable:

```ini
; Wrong -- forwards whatever the handset claimed.
MessageSend(pjsip:vocat/sip:${EXTEN}@127.0.0.1:5062,${MESSAGE(from)})
```

Each extension therefore gets its own generated message context with its
identity written in, which is how the authenticated endpoint reaches VoCat:

```ini
[vocat-msg-1001]
exten => _.,1,MessageSend(pjsip:vocat/sip:${EXTEN}@127.0.0.1:5062,sip:1001@vocat)
 same => n,Hangup()
```

with `message_context=vocat-msg-1001` on that extension's endpoint.

Two honest notes on this choice:

- It was **not verified** whether Asterisk exposes the authenticated endpoint
  name as a dialplan variable inside a message context. If it does, one shared
  context works and is simpler. The per-extension form works regardless of that
  answer, which is the reason to prefer it.
- The exposure it closes is narrow: it needs someone holding a *valid*
  extension credential who wants to send as a *different* extension. In a
  single-operator deployment that is nobody. It matters in the multi-user
  setup `docs/rbac-design.md` anticipates, where different people hold
  different extensions against different SIMs and different bills.

---

## Delivery reports

`ApplySMSDeliveryReport` (`internal/store/sms.go:400`) already correlates the
carrier's TP-STATUS report to the original submission by `MessageReference`,
aggregates multipart, and returns the updated row. So:

1. When the trunk accepts a message, record the originating extension in the
   row's `Extra`.
2. When a report lands and `Extra` names an extension, send a MESSAGE back to
   it.

**Plain text**, not IMDN:

```
Delivered 10:06 — "meeting at 3…"
Failed 10:06 — "meeting at 3…" (absent subscriber)
```

RFC 5438 IMDN inside `message/cpim` is the protocol-correct receipt, but
softphone support is rare and a phone that cannot parse it shows a blank or
garbled message. Plain text works everywhere.

The limitation to state rather than discover: it arrives as **a new text, not a
tick beside the original**. SIP has no way to mark an earlier message delivered
that handsets actually implement.

---

## Generated configuration

One new file, `messages.conf`, holding both halves -- they regenerate from the
same two lists and both belong to `pbx_config`.

| Section | Generated from |
| --- | --- |
| `[vocat-messages]` | The inbound mode plus the extension list |
| `[vocat-msg-<ext>]`, one per extension | The extension list |

Plus `message_context=vocat-messages` on `[vocat]` in
`asterisk/templates/pjsip.conf`, and `message_context=vocat-msg-<name>` on each
generated extension endpoint in `endpoints.conf`.

Adding a generated file means the entrypoint has to seed it so its `#include`
always resolves, and `entrypoint.sh` is baked into the image:

```sh
docker compose build asterisk && docker compose up -d asterisk
```

---

## SMS history: a tab on the Asterisk page

Three panes, the shape `SmsPage` already uses. `.sms-main-layout`
(`web/src/vocat.css:421,456`) is already `260px 340px minmax(0,1fr)` on desktop
and one column on mobile, and `ContactList.tsx` / `ThreadPanel.tsx` are already
separate components.

```
┌────────────┬───────────────┬──────────────────────┐
│ Extensions │ Conversations │  Messages            │
│            │               │                      │
│ 1555123…  ●│ +86138…    2  │  ← inbound  10:04    │
│ 1555124…   │ 10086         │  → outbound 10:06 ✓  │
│ 1001  ⚠    │ 支付宝         │  → outbound 10:09 ✗  │
└────────────┴───────────────┴──────────────────────┘
```

**Left is extensions, not devices** -- the one real change from `SmsPage`, and
it earns its place: an extension with **no matching SIM** is listed greyed with
"no SIM -- cannot send or receive", which surfaces the outbound rule exactly
where someone would otherwise wonder why their softphone cannot text.

**Middle and right** are `ContactList` and `ThreadPanel` unchanged:
conversations keyed by peer, then the thread.

**What makes the tab worth having** rather than a copy of the SMS page is the
trunk state on each message:

| Direction | States |
| --- | --- |
| Inbound | forwarded / not forwarded (mode was `off`) / forward failed |
| Outbound | accepted 202 → submitted to the SIM → delivered ✓ or failed ✗ |

The default view is the **whole conversation on that SIM**, with badges marking
what the trunk touched, plus a filter for "only messages that crossed the
trunk". Delivery state is already stored and multipart-aggregated, so the ✓/✗
is a read rather than new plumbing.

Rows are marked as they pass: `Extra.sip_extension` for outbound accepted from
a handset, `Extra.sip_forwarded_to` for inbound delivered to one. `SMSFilter`
(`internal/store/models.go:189`) has no way to select on an `Extra` marker, so
it needs a field -- the only storage-adjacent work, and no migration, since
`Extra` is already JSON.

---

## Limitations, written down rather than discovered

1. **Best-effort, no store-and-forward.** Handset offline, the MESSAGE fails
   and the SMS is not queued. Asterisk has no SMS spool. VoCat's own history
   stays the system of record, which is the honest answer -- and it means "I
   never got the text" has a real cause.
2. **The softphone must support SIP MESSAGE.** Zoiper, Linphone and Bria do;
   others return `405`.
3. **Inbound routing needs `LocalPhone`.** No SIM number known means no
   Request-URI user, so nothing is forwarded: skip and log rather than guess.
   The same dependency the `did` call mode already has.
4. **UTF-8 bodies with a byte-counted `Content-Length`.** Not optional for
   Chinese SMS, and the easiest thing to get wrong. Large concatenated messages
   over UDP are fine *because the trunk is loopback* (MTU 65536); they would
   not be on a routed trunk.
5. **A 200 from Asterisk means Asterisk accepted it**, not that the handset
   received it. `MessageSend`'s own result is separate.
6. **Extension names now do triple duty**: call routing, SMS delivery, and send
   authorisation. Consistent -- one extension per SIM, named after its number
   -- but renaming an extension silently changes who may spend that SIM. The
   extension editor should say so at the rename.

---

## Work breakdown

| Piece | Size |
| --- | --- |
| `SendMessage` and MESSAGE handling in `internal/siptrunk` (`bridge.go:106`) | M |
| `messages.conf` renderer, both halves, plus tests | M |
| Inbound forwarder as a notification channel | S |
| Outbound accept: identity lookup, 403/202, existing send path | M |
| Delivery report back to the extension | S |
| `SMSFilter` marker and the three-pane history tab | M |
| API, GUI mode selector, docs | M |

**Roughly 2.5-3 days.** Needs an Asterisk image rebuild.

---

## Decisions taken

1. **Inbound modes are `off`, `did` and `trunk`**; `off` is the default and
   there is no `all`.
2. **The true sender is preserved**, as the user part when numeric and as the
   display name when not.
3. **Outbound is accepted only when the extension's number equals a SIM's
   number**; otherwise `403`. Acceptance is `202`, not `200`.
4. **Identity is asserted by the generated per-extension message context**,
   never taken from `${MESSAGE(from)}`.
5. **Delivery reports come back as plain text**, not IMDN.
6. **The history tab shows whole conversations** with trunk-state badges, not
   only the messages that crossed the trunk.

## Open questions

1. Does the inbound `trunk` mode need a destination number, the way the
   forward-a-call mode does, or does the SIM's number always pass through?
2. Should an extension be able to send to a destination the SIM's carrier will
   reject -- premium numbers, international -- or does outbound want a
   destination allowlist like an external trunk has?
3. Is a rate limit needed per extension? A compromised handset credential can
   currently spend a SIM's SMS allowance as fast as it can send.
