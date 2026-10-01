# MMS over VoWiFi — design and effort

**Status: analysis only. No code written, none proposed for merge yet.**
Written up for future reference after a read-only survey of the tree at
`066a70e`. Every file reference below was checked against that commit; where a
claim is a guess rather than a reading, it says so.

---

## The short answer

The hard part is already built. VoWiFi gives VoCat a real IP tunnel to the
carrier's core, and MMS is just HTTP over such a tunnel. What is missing is not
the transport — it is the **second PDN**, the **MMS Encapsulation codec**, and
the **WAP Push inbound path**. None of those exist anywhere in the tree today.

```
outbound  GUI ──▶ m-send.req (binary WSP + MIME multipart/related)
                      │
                      ▼  HTTP POST, socket bound to the MMS tunnel
                  MMSC ──▶ recipient
                      │
                      ▼  m-send.conf  (message-id, response-status)

inbound   MMSC ──WAP Push binary SMS (UDH port 2948)──▶ SIM ──▶ VoCat
                      │  m-notification.ind carries the content location
                      ▼
                  HTTP GET on the MMS tunnel ──▶ m-retrieve.conf ──▶ store
```

---

## Why the transport is the easy part

### The tunnel is a real network interface, not a SIP-only pipe

`internal/vowifi/ike/userspace_linux.go` creates an actual TUN device — the
handle holds `tun *os.File` and `tunFD int` (line 37, 103) — and configures it
with `ip route`, `ip rule` and nft rules through `exec.CommandContext`. Packets
move through `readTUNPacket`/`writeTUNPacket` (lines 689, 734) with
`userspaceTunnelMTU = 1380` (line 21).

That means **arbitrary IP already flows over the tunnel**, not just SIP and
RTP. An HTTP client needs no new transport work; it needs its socket bound to
the right interface. The precedent for that already exists in the tree:
`internal/exportproxy/bind_linux.go:26` uses
`syscall.SetsockoptString(fd, SOL_SOCKET, SO_BINDTODEVICE, networkInterface)`.
The same `Dialer.Control` pattern pointed at the MMS tunnel's interface gives a
`*http.Client` that talks to the MMSC and nothing else.

### The APN is already a parameter

`internal/vowifi/ike/provider.go` takes `APN string` in its config (line 34),
defaults it to `"ims"` (lines 72–77) and sends it as the IKEv2 responder
identity:

```go
requestedIDr := payload{Type: payloadIDr,
    Body: append([]byte{2, 0, 0, 0}, []byte(provider.config.APN)...)}
```

(line 339). Under S2b, **one IKEv2 SA is one PDN connection**, and the APN in
IDr is what selects it. So a second PDN for the MMS APN is **a second
`ike.Provider` instance with a different `APN`** — the architecture already
supports it. This is not a hack around the design; it is the design.

---

## What exists versus what is missing

| Layer | Today | Needed for MMS |
| --- | --- | --- |
| IKEv2 / IPsec to ePDG | `internal/vowifi/ike` — working, APN-parameterised | second instance, MMS APN in IDr |
| TUN device, routing | real TUN + `ip route`/`ip rule`/nft | per-tunnel interface naming so two coexist |
| Socket binding to a tunnel | `SO_BINDTODEVICE` in `exportproxy` | reuse for an `*http.Client` |
| MMS APN / MMSC URL / MMS proxy config | **nothing** — see below | new config fields, plumbed to GUI and profiles |
| MMS Encapsulation (WSP/PDU) codec | **nothing** | full encoder + decoder, new package |
| MIME `multipart/related` body | **nothing** | assemble parts; `mime/multipart` only gets you partway |
| WAP Push inbound (binary SMS, UDH port 2948) | **nothing** | UDH port detection + `m-notification.ind` decode |
| Media blob storage | SMS text rows only | binary attachment store + retention policy |
| Delivery to SIP extensions | SIP MESSAGE (text) | **not possible** — see risks |

### The config gap is concrete

`internal/store/models.go:498` — `CardAPNProfile` carries `APN`, `Username`,
`Password`, `Proxy`, `MCC`, `MNC`, `IPVersion`, `RoamingIPVersion`, `AuthType`
— and **no APN type discriminator**. There is no way today to say "this APN is
the MMS one, that one is the IMS one". That field (or a separate
`CardMMSProfile`) is the first schema change.

The carrier profile schema has the same gap. `carrierProfileDocument`
(`internal/vowifi/carrier_compat.go:84`) models `match`, `identity`, `route`,
`epdg`, `ike` and `ims`. There is no MMS section, so a per-carrier MMSC URL has
nowhere to live in `carrier-profiles.d/` overrides either.

### But the config data source already exists — and is deliberately discarded

The profile database is generated from Apple's carrier bundles
(`cmd/sync_carrier_bundles/main.go:22`, from
`dwilliamsuk/ios-carrier-bundles`). Those bundles' `carrier.plist` carries an
`apns` array whose entries hold exactly the three values MMS needs: the MMS
APN, the MMSC URL and the MMS proxy.

VoCat currently **throws them away on purpose**.
`internal/vowifi/carrier_ipcc.go:665`:

```go
case key == "apns":
    warnings.add("apn_settings_ignored",
        "APN settings and credentials are outside the VoCat carrier-profile importer", fullPath)
```

So the per-carrier MMS configuration for hundreds of networks is one importer
change plus a schema section away. That is a genuinely large shortcut — it
removes the "where do we get MMSC URLs from" problem, which on most projects is
the worst part of MMS.

---

## The four layers to build

### 1. A second PDN on the MMS APN

A second `ike.Provider` with `APN` set from the card's MMS profile, its own TUN
device, its own routes. Open it lazily — on the first MMS send or the first
WAP Push — and tear it down on idle, because:

- it is a second IKEv2 SA the carrier may or may not permit (see risks), and
- it is a second SIM authentication consumer (see risks).

Lifecycle is the real work here, not the IKE exchange. The IMS tunnel's
lifecycle is driven by registration state; the MMS tunnel has no registration,
so it needs its own reason to exist and its own reason to go away.

### 2. An HTTP client bound to that tunnel

Small. `net.Dialer` with a `Control` hook doing `SO_BINDTODEVICE` on the MMS
TUN interface, wrapped in an `http.Transport`. Honour the MMS proxy when the
carrier config specifies one (many do — the MMSC is often only reachable
through it).

### 3. MMS Encapsulation codec — the bulk of the work

This is WAP-243 / OMA MMS Encapsulation: a binary header encoding that is
*not* HTTP headers and *not* protobuf. Well-known header field names are
single bytes with the high bit set; values are typed (text-string,
short-integer, long-integer, encoded-string-value with a charset prefix);
lengths are uintvar-encoded. You need, at minimum:

- **`m-send.req`** encoder — `X-Mms-Message-Type`, `X-Mms-Transaction-Id`,
  `X-Mms-MMS-Version`, `From` (usually insert-address-token), `To`, `Subject`,
  `Content-Type: application/vnd.wap.multipart.related`, then the multipart
  body with its own binary part-header table.
- **`m-send.conf`** decoder — `X-Mms-Response-Status` and the assigned
  `Message-ID`. Without this you cannot tell a success from a rejection.
- **`m-notification.ind`** decoder — for inbound: `X-Mms-Content-Location`,
  `X-Mms-Message-Size`, `X-Mms-Expiry`.
- **`m-retrieve.conf`** decoder — the actual inbound message and its parts.
- **`m-notifyresp.ind`** / **`m-acknowledge.ind`** encoder — tell the MMSC you
  got it, or it will re-notify.

There is no Go library in the tree for any of this, and the ones on the public
module proxy are mostly incomplete or abandoned. Assume hand-written, assume a
corpus of captured PDUs as test fixtures.

### 4. Inbound notification over binary SMS

The MMSC does not push the message; it pushes a **WAP Push SMS** and VoCat
fetches the content over HTTP. The notification arrives as a concatenated
binary SMS with a User Data Header carrying destination port **2948**.

`internal/device/sms_pdu.go` is partway there: it stores
`DataCodingScheme` (line 657), builds and parses concatenation UDHs
(`concatUDH`, line 342) and already recognises one class of non-text SMS —
`isSIMDataDownload` (line 676) sets `message.SIMDataDownload` for SIM Data
Download PDUs. That is the precedent: **a binary SMS that is routed somewhere
other than the inbox already exists in this codebase.** What is missing is UDH
*port* parsing (the application-port addressing IEI), and a `WAPPush` flag
beside `SIMDataDownload`, and a consumer for it.

Note the ordering constraint: the notification can arrive while no MMS tunnel
is up, and the content location has an expiry. So inbound requires the lazy
tunnel open from layer 1 to be reliable, not best-effort.

---

## Effort

Measured against the SMS-over-SIP-trunk feature already in the tree (that one
spanned `internal/siptrunk`, `internal/asteriskconf`, `internal/server`,
`internal/store`, the GUI and its tests):

| Scope | Relative effort | Notes |
| --- | --- | --- |
| **Outbound only, VoCat GUI only, one carrier** | **≈1.5–2×** | second PDN + bound HTTP client + `m-send.req`/`m-send.conf` + config fields + GUI compose. The codec dominates. |
| **Plus inbound** | **roughly doubles again** | WAP Push decode, retrieval fetch, acknowledgement, blob storage, retention, notification plumbing. |
| **Production quality across carriers** | **substantially more** | per-carrier MMSC quirks, proxy-only MMSCs, size limits, content adaptation (image downscaling to fit `X-Mms-Message-Size`), retry/expiry semantics. Open-ended. |

The honest read: outbound-to-one-carrier is a well-bounded project. "MMS
works" across the carrier set in `carrier_profiles.json` is not, and the
difference is mostly the long tail of MMSC behaviour, not VoCat's own code.

---

## Risks, in the order they would bite

### 1. Will the carrier grant a second simultaneous S2b PDN?

**Unverified, and it gates everything.** VoCat's ePDG connection is an S2b
(untrusted non-3GPP) attachment. Whether the ePDG will establish a *second*
child SA for a non-IMS APN from the same UE is carrier policy, not spec-
mandated behaviour. Several operators run the ePDG as IMS-APN-only. If the
answer is no for a given carrier, MMS over VoWiFi is impossible there
regardless of how good the codec is.

**Test this first, before writing a line of the codec.** The cheapest probe is
a second `ike.Provider` with the MMS APN in IDr and nothing behind it; if the
IKE_AUTH exchange returns a child-SA failure or the ePDG refuses the second
IDr, the feature is dead for that carrier and you have spent a day, not a
month.

### 2. SIM authentication contention

`internal/device/vowifi_qmi.go:73`:

```go
func (manager *Manager) AuthenticateNativeQMI(ctx context.Context, id string, aid, apdu []byte) (response []byte, err error)
```

**No locking was found around this.** Both tunnels need EAP-AKA' runs against
the same USIM, and a second tunnel means concurrent `AuthenticateNativeQMI`
calls where today there is at most one. Two interleaved AUTHENTICATE APDUs on
one card is a plausible way to corrupt both, and the failure mode would look
like random VoWiFi registration loss — i.e. it would be blamed on the carrier,
not on this change.

A mutex around card authentication is cheap and should land **before** the
second PDN, independent of whether MMS is ever built. Worth doing on its own
merits.

### 3. MMS cannot reach SIP extensions

SMS forwarding to softphones rides SIP MESSAGE (RFC 3428) with a `text/plain`
body. There is no equivalent path for a multi-megabyte `multipart/related`
payload: SIP MESSAGE is not a bulk transport, and no softphone in the expected
set renders MMS.

So MMS would be **VoCat GUI only** — visible in the web UI, not on the
handsets. That is a real product limitation and worth deciding on *before*
building, because it changes who the feature is for. Options, none free: a
notification channel carrying a VoCat link; an MM4/MM7-style gateway; or
accept GUI-only.

### 4. VoWiFi has no attempt cap, so a second tunnel doubles the dead-loop surface

A SIM that cannot establish VoWiFi retries indefinitely today — that is exactly
the dead loop seen with the Dito card, and the escape hatch was a manual
`PATCH /api/devices/<id>/vowifi` with `{"enabled": false}`. A second tunnel with
its own retry loop and no cap doubles that exposure, and the MMS tunnel has no
registration state to report "it isn't working" through.

Give the MMS tunnel a bounded attempt budget from day one, and surface its state
in the GUI. Do not inherit the IMS tunnel's retry posture.

---

## Suggested order of work

1. **Probe the second PDN** (risk 1). One day. If it fails, stop.
2. **Lock SIM authentication** (risk 2). Lands independently, useful regardless.
3. **Import `apns` from the carrier bundles** — schema section, importer change
   at `carrier_ipcc.go:665`, `CardAPNProfile` type discriminator, GUI fields.
   This is the config foundation and is independently testable.
4. **MMS codec**, outbound only, against captured fixtures. No network.
5. **Bound HTTP client + outbound send**, end to end, one carrier.
6. **Decide the extension-delivery question** (risk 3) before building inbound.
7. **Inbound**: UDH port parsing, `m-notification.ind`, retrieval, blob store.

Steps 1–3 are worth doing even if MMS is never finished: the probe answers a
real question about the ePDG, the lock fixes a latent bug, and the APN import
closes a gap the importer currently warns about.

---

## Things this document is not sure about

- Whether any specific carrier's ePDG permits a second APN. Entirely untested.
- Whether the Quectel firmware in use will even surface a second ePDG tunnel's
  traffic correctly through the existing TUN path — the IMS tunnel is the only
  one ever exercised.
- Whether `concatUDH`'s existing UDH handling can be extended to port
  addressing without touching concatenation reassembly. It looks separable; it
  was not traced end to end.
- Effort multiples are judgement, anchored to the SMS-over-trunk feature's
  actual size. They are not estimates from a broken-down task list.
