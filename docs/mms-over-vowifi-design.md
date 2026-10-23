# MMS over VoWiFi, with vCard (VCF) — design and effort

**Status: analysis only. No code written, none proposed for merge yet.**

- **Revision 2** — reviewed at `f504287`, after the 51-commit upstream merge
  (`813af4c`), and extended with vCard support. Every file:line reference was
  re-checked against that commit.
- **Revision 1** — read-only survey at `066a70e`.

Where a claim is a guess rather than a reading, it says so. One claim is
measured instead of read — how a vCard SMS is stored today — and the
measurement is shown.

---

## Review: what the merge changed

Six findings, most important first. Each is folded into the sections below.
This summary is for whoever reads revision 1 first.

1. **Inbound WAP Push is partly built — for display only.** Upstream
   `12ce8f0` added `internal/smsdecode`, which recognises a WAP Push carrying
   an MMS `m-notification.ind`. Both the cellular and the IMS ingest paths
   use it. But it keeps only the **Subject**: it walks every header and
   *skips* `X-Mms-Content-Location`, the URL a retrieval needs
   (`internal/smsdecode/mms.go:96`). The package comment says so outright:
   *"VoCat cannot retrieve the multimedia body from the operator MMSC."*
   Revision 1 said none of this existed; that is no longer true.
2. **The decoding primitives for the MMS codec now exist.** These cover
   uintvar, value-length, WSP content types, and encoded strings with
   charsets including GB18030, GBK and UTF-16 (`smsdecode/wsp.go`,
   `smsdecode/mms.go`). That is the foundation of the decode half. The encode
   half — `m-send.req` — still has nothing.
3. **APN roles are now split, and the pattern is a trap for MMS.** Upstream
   `875ec3d` found the ePDG tunnel was using the cellular *data* APN. It added
   a dedicated `vowifi.ims_apn` setting (`internal/vowifisettings/settings.go:52`).
   That validates separating APN roles. But the setting is **global**. Global
   works for IMS, where nearly every carrier uses `ims`. It does **not** work
   for MMS, whose APN and MMSC URL differ per carrier. A fleet of 15 SIMs on
   several carriers needs per-carrier MMS config.
4. **The raw bytes survive ingest, in two different places.** The cellular
   path archives every PDU to the new `sms_raw_records` table before anything
   else touches it (`internal/server/sms_api.go:768`). The IMS path keeps
   `raw_tpdu` in `sms_messages.extra_json` (`cmd/vocat/main.go:1315`). But for
   a single-part push, the message's `Text` is *replaced* by the subject. So
   retrieval must parse structure at ingest, not re-derive it from the display
   string.
5. **vCard is unsupported today, and the failure is visible.** A vCard sent
   as a port-addressed SMS (destination port 9204) is stored and shown as a
   **hex dump**. This was measured, not inferred; see the vCard section. The
   cause: `hasApplicationPortAddressing` (`internal/device/sms_pdu.go:1105`)
   detects that a UDH carries a port but never extracts which one, and
   `decodeTextBytes` (`:1031`) refuses to decode any port-addressed payload as
   text.
6. **Unchanged gaps.** There is still no MMS section in the carrier-profile
   schema and no APN type in `CardAPNProfile`. The carrier-bundle `apns` array
   is still discarded (now at `carrier_ipcc.go:679`). SIM authentication on
   the native QMI path is still unlocked (now at `vowifi_qmi.go:160`). A fix
   for the last one was written once and withdrawn on request; it remains
   open.

Line numbers that moved since revision 1 were updated in place.

---

## The short answer

The transport is still the easy part. VoWiFi gives VoCat a real IP tunnel to
the carrier's core, and MMS is HTTP over such a tunnel. What is missing is the
**second PDN**, the **encode half of the MMS codec**, **structured** (not
display-only) notification handling, and **per-carrier MMS config**.

**vCard support should not wait for any of that.** Its most visible failure
today — a vCard SMS shown as hex — is fixable with no MMS at all. That fix
also builds the attachment storage MMS will need.

```
outbound  GUI ──▶ m-send.req (binary WSP + multipart/related; text, image, text/x-vcard parts)
                      │
                      ▼  HTTP POST, socket bound to the MMS tunnel
                  MMSC ──▶ recipient
                      │
                      ▼  m-send.conf  (message-id, response-status)

inbound   MMSC ──WAP Push SMS──▶ SIM ──▶ VoCat   (detected and subject-decoded today)
                      │  m-notification.ind: Content-Location  (walked but discarded today)
                      ▼
                  HTTP GET on the MMS tunnel ──▶ m-retrieve.conf ──▶ parts ──▶ store
                                                                       └─ text/x-vcard ──▶ contact card

vCard     sender ──SMS, UDH dest port 9204──▶ SIM ──▶ VoCat   (stored as hex today)
          sender ──plain-text SMS "BEGIN:VCARD…"──▶ VoCat     (stored as raw vCard text today)
```

---

## Why the transport is the easy part

Unchanged by the merge; references re-checked.

### The tunnel is a real network interface, not a SIP-only pipe

`internal/vowifi/ike/userspace_linux.go` creates an actual TUN device. The
handle holds `tun *os.File` and `tunFD int` (line 37), and the tunnel is
configured with `ip route`, `ip rule` and nft rules through
`exec.CommandContext`. Packets move through `readTUNPacket`/`writeTUNPacket`
(lines 689, 734), with `userspaceTunnelMTU = 1380` (line 21).

So **arbitrary IP already flows over the tunnel**, not just SIP and RTP. An
HTTP client needs no new transport work; it needs its socket bound to the
right interface. The precedent is `internal/exportproxy/bind_linux.go:26`:
`syscall.SetsockoptString(fd, SOL_SOCKET, SO_BINDTODEVICE, networkInterface)`.

### The APN is already a parameter

`internal/vowifi/ike/provider.go` takes `APN string` (line 34), defaults it to
`"ims"` (line 74), and sends it as the IKEv2 responder identity (line 339):

```go
requestedIDr := payload{Type: payloadIDr,
    Body: append([]byte{2, 0, 0, 0}, []byte(provider.config.APN)...)}
```

Under S2b, **one IKEv2 SA is one PDN connection**, and the APN in IDr selects
it. So a second PDN for MMS is a second `ike.Provider` with a different `APN`.

Since `875ec3d`, the IMS provider's APN comes from `vowifisettings.IMSAPN`
rather than the device's data APN. An MMS provider takes its APN from the
per-carrier config described below — **not** from a second global setting.

---

## What exists versus what is missing

| Layer | At `f504287` | Needed for MMS |
| --- | --- | --- |
| IKEv2 / IPsec to ePDG | working, APN-parameterised; IMS APN now its own setting | second instance with the MMS APN in IDr |
| TUN device, routing | real TUN + `ip route`/`ip rule`/nft | per-tunnel naming so two coexist |
| Socket binding to a tunnel | `SO_BINDTODEVICE` in `exportproxy` | reuse for an `*http.Client` |
| MMS APN / MMSC URL / MMS proxy config | **nothing** | per-carrier config + per-card override |
| WAP Push detection | **done** (`12ce8f0`), content-based, cellular + IMS | keep |
| `m-notification.ind` decode | **subject only**; Content-Location walked, discarded | capture Content-Location, Size, Expiry, From, Transaction-ID at ingest |
| WSP primitives | **done**: uintvar, value-length, content type, charset strings | extend well-known type table (`text/x-vCard` 0x07, `multipart/related` 0x33, images) |
| `m-retrieve.conf` + WSP multipart parse | nothing | parts table walk, per-part headers |
| `m-send.req` / `m-notifyresp.ind` / `m-acknowledge.ind` encode | **nothing** | full encoder |
| UDH application-port extraction | detects, doesn't extract (`sms_pdu.go:1105`) | return the destination port |
| Raw PDU retention | cellular `sms_raw_records`; IMS `extra_json.raw_tpdu` | keep; parse structure at ingest |
| Attachment / blob storage | none; SMS rows hold text | `message_attachments` table — built by vCard phase 0 |
| vCard parse / render | **nothing**; vCard SMS stored as hex | parser, contact card, notification fields |
| Delivery to SIP extensions | SIP MESSAGE, `text/plain` | media: not possible. vCard: a text summary is |

---

## Configuration

### The gap is unchanged

`internal/store/models.go:500`: `CardAPNProfile` carries `APN`, `Username`,
`Password`, `Proxy`, `MCC`, `MNC`, `IPVersion`, `RoamingIPVersion` and
`AuthType`, but **no APN type**. The carrier-profile rule
(`internal/vowifi/carrier_compat.go:95`) models only `match`, `match_any`,
`identity`, `route`, `epdg`, `ike` and `ims`; there is no `mms` section.

### The data source exists, and is still discarded

The profile database is generated from Apple's carrier bundles
(`cmd/sync_carrier_bundles/main.go:22`, source
`dwilliamsuk/ios-carrier-bundles`). Their `carrier.plist` `apns` array holds
the MMS APN, the MMSC URL and the MMS proxy. VoCat drops it on purpose, at
`internal/vowifi/carrier_ipcc.go:679`:

```go
case key == "apns":
    warnings.add("apn_settings_ignored", ...)
```

### Where MMS config must live — and where it must not

`875ec3d` makes a tempting precedent: one `vowifi.mms_apn` beside
`vowifi.ims_apn`. **Do not follow it.**

- **IMS APN** is effectively a constant (`ims`), so one global setting is fine.
- **MMS APN, MMSC URL and MMS proxy** are per-carrier. A global value is wrong
  for every SIM but one in a mixed fleet.

The design is three layers, most specific wins:

1. Per-card override — `CardAPNProfile` gains a `Type` (`data` / `mms`), or a
   sibling `CardMMSProfile`.
2. Per-carrier profile — a new `mms` section in the carrier-profile rule
   (`apn`, `mmsc`, `proxy`, `max_message_size`), so `carrier-profiles.d/`
   overrides work as they already do for `epdg` and `ike`.
3. Imported default — fill (2) from the bundles' `apns` array instead of
   discarding it.

The profile lint added earlier (`carrier_profile_lint.go`) will flag a
misspelled key in a hand-written `mms` section, as it already does elsewhere.

---

## The layers to build

### 1. A second PDN on the MMS APN

Unchanged in substance. A second `ike.Provider` with the MMS APN, its own TUN
device and its own routes. Open it lazily — on the first send, or on the first
notification — and tear it down when idle.

Lifecycle is the real work, not the IKE exchange. The IMS tunnel's lifecycle
follows registration state. The MMS tunnel has no registration, so it needs
its own reason to exist and its own reason to go away.

### 2. An HTTP client bound to that tunnel

Small. A `net.Dialer` whose `Control` hook does `SO_BINDTODEVICE` on the MMS
TUN interface, inside an `http.Transport`. Honour the MMS proxy when the
carrier config names one; many MMSCs are reachable only through it.

### 3. The MMS Encapsulation codec

This is OMA MMS Encapsulation (WAP-209 / MMS 1.x ENC): binary WSP-encoded
headers, typed values and uintvar lengths.

**Decode side — the foundation exists now.** `internal/smsdecode` already
parses value-length, uintvar, text strings, well-known content types and
charset-encoded strings, including the Chinese charsets. Still needed:

- Turn `decodeMMSNotificationText` (`mms.go:57`) into a **structured** parse
  that returns `Content-Location`, `X-Mms-Message-Size`, `X-Mms-Expiry`,
  `From` and `Transaction-ID` instead of skipping them. Small: every one is
  already walked at `mms.go:81-100`.
- An `m-retrieve.conf` parser, including the **WSP multipart** body: entry
  count, then per part a headers length, a data length, a content type,
  headers and data.
- Grow the well-known content-type table, which has four entries
  (`wsp.go:16-19`): `text/x-vCard` (0x07), `text/x-vCalendar` (0x06),
  `application/vnd.wap.multipart.related` (0x33), and the common image
  types.
- An `m-send.conf` decoder (response status, Message-ID).

**Encode side — nothing yet.** `m-send.req` (headers plus a multipart body),
`m-notifyresp.ind` and `m-acknowledge.ind`. Without the last two, the MMSC
re-notifies.

Upstream's tests are a good model; keep the corpus-of-captured-PDUs
approach.

### 4. Inbound: from notification to stored message

Revision 1 called this "UDH port detection + decode". Detection is done. What
remains:

- **Parse structure at ingest.** For a single-part push, the message `Text` is
  overwritten with the subject (`sms_pdu.go:913`). Retrieval therefore cannot
  start from the stored text. Persist a structured notification row —
  `mms_notifications`: device, content location, size, expiry, from,
  transaction id, state — at the same point the display text is set.
- **Multipart pushes.** Upstream keeps 8-bit multipart segments as hex until
  reassembly, then decodes on read (`smsdecode.Preview`). Structured parsing
  belongs at reassembly, not at read.
- **Expiry.** The content location expires. If the MMS tunnel can't come up
  before then, record that and say so in the UI; don't retry silently forever.

---

## vCard (VCF) support

### Where vCards arrive

| Path | Shape | Today |
| --- | --- | --- |
| **A. MMS part** | `text/x-vcard` / `text/vcard` part in `m-retrieve.conf` | unreachable: no retrieval |
| **B. Port-addressed SMS** | 8-bit SMS, UDH destination port **9204** (Nokia Smart Messaging), payload is vCard text | **stored as hex** — measured |
| **C. Plain-text SMS** | ordinary SMS whose body is `BEGIN:VCARD…` | stored and shown as raw vCard text |

A is the common case today; B and C come from older phones and some
gateways.

### What B looks like today — measured

A real SMS-DELIVER TPDU was built and run through the production decoder,
`device.DecodeSMSDeliverTPDU`. It carried a 16-bit port UDH
(`05 04 23F4 0000`, destination 9204), an 8-bit DCS, and a vCard 2.1 payload:

```
8-bit DCS 0x04:            err=<nil> encoding=8bit_pdu
    stored text: "424547494E3A56434152440D0A56455253494F4E3A322E310D0A4E3A5761..."
8-bit DCS 0xF5 (class 1):  err=<nil> encoding=8bit_pdu
    stored text: "424547494E3A56434152440D0A..."
```

`424547494E3A5643…` is `BEGIN:VC…`. The contact is intact but stored as hex,
and every consumer shows the hex: inbox, notifications, export, and forwarding
to extensions.

The cause is two lines. `decodeTextBytes` (`sms_pdu.go:1031`) returns early
whenever `hasApplicationPortAddressing` (`:1105`) is true. That function
answers *whether* a port is present, never *which* one. The IMS path calls the
same decoder (`DecodeSMSDeliverTPDU`, `ims/sms_runtime.go:515`), so it fails the
same way over VoWiFi.

### Design

**1. Extract the port.** Change `hasApplicationPortAddressing` to return the
destination port (IEI `0x05`: 16-bit; `0x04`: 8-bit). Route by port:

| Port | Meaning | Action |
| --- | --- | --- |
| 2948 | WAP Push | as today — and lets the content sniffing be confirmed by port |
| 9204 | vCard | vCard decode |
| 9205 | vCalendar | out of scope; keep hex |
| other | — | keep hex, as today |

Port routing also catches path C, by sniffing for `BEGIN:VCARD` in an
unaddressed text SMS.

**2. Reassemble first.** Most real vCards exceed one segment. A
single-part port-addressed SMS carries 133 bytes of payload; once the UDH also
holds the concatenation header, each segment carries 128. Two phone numbers, an email
and an organisation are past that. Upstream already keeps 8-bit multipart as
hex until it is joined, so vCard decoding belongs at reassembly. Decoding
segment one alone would yield half a card.

**3. A shared parser, `internal/vcard`,** used by paths A, B and C:

- **Versions.**
  - 2.1: bare type parameters (`TEL;CELL:`), `ENCODING=QUOTED-PRINTABLE` with
    soft line breaks, and `CHARSET=`. Smart Messaging senders are 2.1.
  - 3.0 (RFC 2426): `TYPE=`, `ENCODING=b`.
  - 4.0 (RFC 6350): UTF-8 only.
- **Unfolding.** CRLF followed by a space or tab continues the line.
  Unescape `\,`, `\;`, `\n` and `\\` in 3.0/4.0.
- **Charsets.** 2.1 names them (`UTF-8`, `GB2312`, `GBK`, `BIG5`). Map names
  to the decoders `smsdecode` already uses, rather than adding new ones.
- **Several cards in one payload.** A `.vcf` can hold more than one.
- **Output.** A summary — `FN`, `N`, `TEL[]` with types, `EMAIL[]`, `ORG`,
  `NOTE` — plus the raw bytes.

**4. Treat it as untrusted input.**

- Cap the raw size (64 KiB proposed) and the number of cards (8 proposed).
- An embedded `PHOTO` is base64 and counts against the cap. Decide: keep a
  thumbnail, or drop it.
- **Never fetch** `PHOTO;VALUE=uri`, `LOGO`, `SOUND` or `URL`. Fetching a
  sender-chosen URL from inside the carrier network is SSRF, and it confirms
  to the sender that the message was opened.
- Render as text through React, never as HTML.
- Guard CSV export against formula injection — a field starting with
  `=`, `+`, `-` or `@`.

**5. Store it as an attachment.** Add a `message_attachments` table: message
id (FK, `ON DELETE CASCADE`), content type, filename, size, sha256, data, and
the parsed summary. Small blobs live in SQLite. It would be migration **32**;
the schema is at 31 after the merge.

This is the same table MMS parts need, which is why vCard should go first.

Also set the message's `Text` to a readable summary, such as
`联系人：Leo Wang +63 917 123 4567`. That mirrors what upstream did for WAP
Push. Every existing consumer then gets readable text for free: inbox, search,
export, notifications, extension forwarding. And because the raw card lives in
the attachment, nothing is lost — avoiding the lossy-ingest trap in review
finding 4.

**6. Where it shows up.**

| Consumer | Behaviour |
| --- | --- |
| SMS thread (`ThreadPanel`) | contact card: name, numbers, email; **Download .vcf**, copy number, send SMS |
| Telegram | `sendContact` with the first number and the name, plus the raw card in `vcard` (Bot API caps it at 2048 bytes; fall back to the summary above that). New: VoCat calls only `sendMessage` today |
| Webhook | a structured `contact` object beside the text summary |
| SIP extension | the text summary via `forwardSMSToPBX` (`internal/server/sms_trunk.go:134`). Not the raw card: RFC 3428 limits a MESSAGE over UDP to about 1300 bytes, and few softphones render `text/vcard` |
| JSON/HTML export (`1fa3bf6`) | include the summary and the attachment |

### Outbound: sharing a contact

VoCat has no address book, so the composer needs **Share contact**: upload a
`.vcf`, or fill a small name-and-numbers form. Three ways to send it:

| Method | Reach | Needs MMS | Recommendation |
| --- | --- | --- | --- |
| **Text summary SMS** | every phone | no | **default now** |
| **MMS `text/x-vcard` part** | every MMS-capable phone, imports natively | yes | the right answer, once MMS exists |
| **SMS to port 9204** | poor: iPhones don't handle port-addressed vCards, and Android depends on the vendor's app (believed, not tested) | no | opt-in only |

---

## Effort

Same anchor as before: the SMS-over-SIP-trunk feature (`internal/siptrunk`,
`internal/asteriskconf`, `internal/server`, `internal/store`, GUI, tests).

| Scope | Relative effort | Notes |
| --- | --- | --- |
| **vCard phase 0** — inbound B + C, parser, attachment table, contact card, notifications | **≈0.5×** | no carrier dependency; fixes a visible bug; builds MMS's attachment storage |
| **vCard outbound as text summary** | **≈0.1×** | composer form/upload + formatter |
| **MMS outbound only**, GUI only, one carrier | **≈1.5–2×** | unchanged — the encode half got no help from the merge |
| **Plus MMS inbound** | **+≈0.7–1×** (was "roughly doubles") | detection, header walking and charset decoding now exist; structured capture, retrieval, multipart, ack and expiry remain |
| **vCard as MMS part** (both directions) | **small, given the above** | the parser exists from phase 0; this is part plumbing |
| **Production quality across carriers** | **substantially more** | MMSC quirks, proxy-only MMSCs, size limits, content adaptation, retry and expiry. Open-ended |

---

## Risks, in the order they would bite

### 1. Will the carrier grant a second simultaneous S2b PDN?

**Unverified, and it gates all of MMS — but not vCard phase 0.** Whether an
ePDG accepts a second child SA for a non-IMS APN is carrier policy. Several
operators run IMS-APN-only ePDGs. Probe it first: a second `ike.Provider` with
the MMS APN in IDr and nothing behind it. One day, before any codec work.

### 2. SIM authentication contention

`internal/device/vowifi_qmi.go:160` runs the native QMI AKA exchange — open
channel, AUTHENTICATE, procedure-byte follow-up, close channel — with **no
lock**. Upstream's `e6eba35` rewrote the surrounding file and did not add one.

The mutex meant for exactly this already exists. `uiccMu`, exposed as
`LockUICC`, names the VoWiFi AKA adapter as its client. Only the AT adapter
takes it. A second tunnel doubles the AKA callers per card.

Interleaving fails as `eap_authentication_rejected`, which reads exactly like
the carrier refusing the SIM. A ~25-line fix with tests was written once and
withdrawn on request. It should land before the second PDN.

### 3. MMS media cannot reach SIP extensions

Unchanged for images and audio: SIP MESSAGE is not a bulk transport, so MMS
media stays **VoCat GUI only** unless a link or gateway is added. Decide this
before building inbound.

**vCard is the exception.** A contact reduces to a short text summary, which
the existing forwarder can carry.

### 4. VoWiFi has no attempt cap

Unchanged. Give the MMS tunnel a bounded attempt budget and visible state from
day one. Don't inherit the IMS tunnel's retry posture.

### 5. The global-setting trap

New, from finding 3. Copying `vowifi.ims_apn` into a `vowifi.mms_apn` would
look finished and test fine on one SIM. It would be wrong for every other
carrier in the fleet.

### 6. Lossy ingest

New, from finding 4. Display decoding overwrites `Text`. Any MMS or vCard
feature that later needs the original bytes must get them from the structured
row or the attachment written at ingest, not by re-parsing the display
string.

### 7. vCard content is attacker-controlled

New. Cap the size, never fetch URIs, render as text. Covered in design step 4.

---

## Suggested order of work

0. **vCard phase 0.** Port extraction, `internal/vcard`, `message_attachments`
   (migration 32), contact card, Telegram `sendContact`, webhook `contact`.
   This needs no carrier cooperation, fixes the hex-dump bug now, and builds
   the storage MMS needs. Outbound text-summary sharing fits here too.
1. **Probe the second PDN** (risk 1). One day. If it fails, MMS stops here;
   step 0 has still shipped.
2. **Lock SIM authentication** (risk 2). Independent; useful regardless.
3. **Per-carrier MMS config.** A `mms` profile section, a `CardAPNProfile`
   type, and importing `apns` at `carrier_ipcc.go:679` instead of discarding
   it. Not a global setting.
4. **Codec, encode half,** against captured fixtures. No network.
5. **Bound HTTP client + outbound send,** end to end, one carrier.
6. **Decide extension delivery for media** (risk 3).
7. **Inbound.** Structured notification capture at ingest and reassembly,
   retrieval, `m-notifyresp`/ack, parts into `message_attachments`.
8. **vCard as an MMS part,** both directions. Small by this point.

Steps 0–3 are worth doing even if MMS is never finished.

---

## Things this document is not sure about

- Whether any specific carrier's ePDG permits a second APN. Entirely untested.
- Whether the Quectel firmware routes a second ePDG tunnel's traffic correctly
  through the TUN path. Only the IMS tunnel has ever run.
- Current-phone support for port-9204 vCards. The table above is belief, not
  testing. It matters only if port-addressed sending is ever offered.
- The proposed caps (64 KiB, 8 cards) are guesses to be tuned against real
  traffic.
- Whether vCards on these carriers arrive mostly as MMS parts (A) or as SMS
  (B, C). Phase 0's value depends on B/C being common enough. The
  `sms_raw_records` archive can answer this from your own traffic before
  anything is built.
- Effort multiples are judgement anchored to the SMS-over-trunk feature, not
  estimates from a broken-down task list.
