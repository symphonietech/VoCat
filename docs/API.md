# VoCat HTTP API Reference

This document describes every HTTP endpoint exposed by the VoCat server
(`internal/server/*.go`). It is generated from the Go handler source, not from
a separate spec — field names are the exact `json:"..."` tags used on the
wire.

All application endpoints are mounted under `/api/`. A handful of
infrastructure endpoints (`/healthz`, `/readyz`, `/metrics`) are mounted
outside `/api` and are documented separately at the end.

- **Default listen address**: `0.0.0.0:7575` (configurable via `VOCAT_ADDR`).
- **Content type**: request and response bodies are `application/json`.
  `s.decodeJSON` rejects unknown JSON fields and any trailing data after the
  first JSON value, and caps the body at `MaxRequestBodyBytes` (default 1 MiB).
- **Field casing**: every JSON field name in this document is the literal
  wire name (snake_case, as emitted by the Go structs). The bundled web
  frontend (`web/src/api.ts`) transparently converts `camelCase` request
  bodies to `snake_case` and responses back to `camelCase`; that convention
  is a frontend-only convenience layer, not part of the API contract — a
  direct API client (curl, a script, an API-token integration) must use the
  snake_case names shown here.

## Response envelope

Every JSON response (success or error) is one object.

**Success** — `writeJSON` wraps the payload in a `data` key:

```json
{ "data": { "...": "..." } }
```

Some GET/SSE/health endpoints intentionally omit the `data` wrapper (noted
per-endpoint below); this is the exception, not the rule.

**Error** — `writeError` returns:

```json
{ "error": { "code": "invalid_request", "message": "human-readable detail" } }
```

`code` is a stable machine-readable string (e.g. `not_found`,
`invalid_csrf`, `device_not_found`); `message` is a free-text description
and may change wording over time. A handful of endpoints (notably SMS send)
return **both** `data` and `error` on partial failure so the caller can see
what did and did not go through.

Common error codes that apply broadly, beyond each endpoint's own codes:

| HTTP status | code | Meaning |
|---|---|---|
| 400 | `invalid_request` | Body failed to decode (bad JSON, unknown field, wrong type) |
| 401 | `unauthorized` | No/invalid session cookie or bearer token |
| 403 | `invalid_csrf` | Cookie-authenticated mutation missing/mismatched CSRF token |
| 403 | `network_access_denied` | Client IP rejected by the network access policy (`settings/security`) |
| 404 | `not_found` | Generic record-not-found (also used for disabled developer-mode routes, which 404 rather than 403) |
| 405 | `method_not_allowed` | Wrong HTTP verb; response carries an `Allow` header |
| 500 | `internal_error` / `database_error` | Unhandled server-side failure |

## Authentication

VoCat supports two independent authentication mechanisms. A request that
carries an `Authorization: Bearer ...` header is **always** treated as
token auth and never falls back to the session cookie — a bad/expired
token fails outright rather than silently trying the cookie.

### 1. Browser session (cookie + CSRF, double-submit)

This is what the bundled web UI uses.

1. `POST /api/auth/login` with `{"username", "password"}`. On success the
   server sets two cookies:
   - `vocat_session` — `HttpOnly`, `SameSite=Strict`, the session credential.
   - `vocat_csrf` — **not** `HttpOnly` (JS must read it), `SameSite=Strict`.
   The response body's `data.csrf_token` also contains the CSRF token value.
2. Every subsequent request must send the session cookie (the browser does
   this automatically) **and**, for any method other than `GET`/`HEAD`/
   `OPTIONS`, echo the CSRF token in the `X-CSRF-Token` request header. The
   server compares it against the `vocat_csrf` cookie using a constant-time
   comparison (the "double-submit cookie" pattern) — the two must match.
3. `GET /api/auth/session` returns the current principal and refreshes/
   reissues the CSRF cookie+token; useful for silently re-establishing the
   CSRF token after a page reload (the frontend calls this to recover from a
   `403 invalid_csrf`, then retries the original request once).
4. `POST /api/auth/logout` (also requires the CSRF header) clears both
   cookies and revokes the session server-side.

Cookie names and the CSRF header name are fixed constants in
`internal/server/server.go`: `vocat_session`, `vocat_csrf`,
`X-CSRF-Token`.

**Full curl round trip:**

```bash
# 1. Log in, keep the cookie jar, and capture the CSRF token from the body
curl -s -c cookies.txt -X POST http://localhost:7575/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"admin","password":"correct horse battery staple"}' \
  | tee login.json

CSRF=$(jq -r '.data.csrf_token' login.json)

# 2. Read-only request: cookie only, no CSRF header needed
curl -s -b cookies.txt http://localhost:7575/api/devices

# 3. Mutating request: cookie + X-CSRF-Token header
curl -s -b cookies.txt -X POST http://localhost:7575/api/sms/send \
  -H "Content-Type: application/json" \
  -H "X-CSRF-Token: $CSRF" \
  -d '{"device_id":"modem-1","phone":"+15551234567","message":"hi"}'
```

### 2. API token (Bearer)

An addition in this fork/branch, independent of the browser session
mechanism. A token is a long-lived, non-interactive credential intended for
scripts and integrations. It needs no CSRF header (a cross-site page cannot
set a custom `Authorization` header, so bearer auth is inherently immune to
CSRF) and no cookie.

Create one with the CLI (run on the host, against the same database the
running service uses):

```bash
vocat api-token create --name "my-integration" --ttl 720h   # 720h = 30 days
# -> prints the raw token once: vocat_at_...  (it is never shown again)

vocat api-token list
vocat api-token revoke <id>
```

Use it as:

```bash
curl -s -H "Authorization: Bearer vocat_at_XXXXXXXXXXXXXXXXXXXX" \
  http://localhost:7575/api/devices

curl -s -X POST -H "Authorization: Bearer vocat_at_XXXXXXXXXXXXXXXXXXXX" \
  -H "Content-Type: application/json" \
  -d '{"device_id":"modem-1","phone":"+15551234567","message":"hi"}' \
  http://localhost:7575/api/sms/send
```

No CSRF header is required or checked for bearer-token requests, even for
POST/PUT/PATCH/DELETE.

### Endpoints exempt from authentication

- `GET /api/health` — liveness/DB-readiness probe.
- `POST /api/auth/login` — obviously, to obtain a session.
- `GET /api/settings/preferences` (**GET only**) — the UI language
  preference is exposed unauthenticated specifically so the login page can
  render in the user's saved language before they sign in. `PUT
  /api/settings/preferences` still requires authentication.
- `GET /healthz`, `GET /readyz`, `GET /metrics` — outside `/api`, see the
  System section at the end; these never require authentication.

Everything else under `/api/` requires either a valid session cookie or a
valid bearer token, checked by `requireAuthenticated` before any route
dispatch happens.

### Network access policy

Independent of authentication, every request (API, SPA, websheets) passes
through an IP allow-list (`GET/PUT /api/settings/security`, default mode
`internal`: loopback + RFC1918 + link-local + ULA only). A request from a
disallowed source address is rejected with `403 network_access_denied`
before authentication is even checked.

### Developer-mode-gated endpoints

Several endpoints are only available when developer mode is active
(`s.developerActive()`, which additionally requires the binary to have been
started with developer mode allowed **and** `vocat develop on` to have been
run to persist the toggle). Where noted below, a disabled endpoint responds
with `404 not_found` (`settings/developer`, `settings/https*`) or
`403 developer_mode_required` (device network/roaming-data endpoints,
Export Proxy, traffic analysis) rather than exposing the feature. These are
called out per-endpoint.

---

## Auth

Dedicated top-level handlers registered directly in `server.go` (not behind
the generic `/api` dispatcher, though they still live under `/api/auth/`).

| Method | Path | Description |
|---|---|---|
| POST | `/api/auth/login` | Authenticate with `{"username","password"}`. Rate-limited per (client IP, username) with account lockout; returns `429 too_many_attempts` with a `Retry-After` header when locked. On success, sets session+CSRF cookies and returns `data.user`, `data.csrf_token`, `data.expires_at`, `data.authenticated`, `data.status`. |
| GET | `/api/auth/session` | Returns the current session's `data.user`, `data.csrf_token` (freshly issued/rotated), `data.expires_at`, `data.authenticated`. Requires the session cookie; reissues the CSRF cookie. |
| POST | `/api/auth/logout` | Requires session cookie + `X-CSRF-Token`. Revokes the session and clears both cookies. Returns `data.logged_out: true`. |

---

## Devices

Device endpoints live at `/api/devices` and `/api/devices/{deviceId}/...`
(`internal/server/device_api.go`). `{deviceId}` is the user-assigned
configured device ID (1–64 chars: letters, digits, `.`, `_`, `-`), not a
kernel/USB path.

### Dashboard & listing

| Method | Path | Description | Response `data` |
|---|---|---|---|
| GET | `/api/dashboard/devices` | Compact per-device dashboard summary (id, name, operator, signal, VoWiFi active, public IP, etc). | array of summary objects |
| GET | `/api/dashboard/host` | Host hardware identity + live CPU/mem/disk/net utilization. | `{host: {cpu_model, board_model, memory_model, disk_model}, perf: {cpu_percent, memory_percent, memory_used_bytes, memory_total_bytes, disk_percent, disk_used_bytes, disk_total_bytes, net_rx_bps, net_tx_bps}}` |
| GET | `/api/devices` | List configured devices (merged with live hardware state). | `{device_limit: int, devices: [<device summary>]}` |
| POST | `/api/devices` | Add a device. See below. | `{status:"created", id, discovery_key, physical_device, warning}` |
| GET | `/api/devices/discovered` | Re-scan hardware now and list every discovered physical modem/reader (whether configured or not). | `{devices: [{hardware_kind, reader_name, device_type, discovery_key, control_path, net_interface, usb_path, vendor_id, product_id, driver_name, at_ports, at_port, imei, mode, network_capable, configured, configured_id, degraded, discovery_issue}]}` |
| POST | `/api/devices/actions/rescan` | Force a hardware re-scan (no persistence). | `{status:"ok", devices: <count>}` |
| POST | `/api/device-mgmt/discovered/fix-usbnet` | Force a USB net mode on a discovered-but-unmanaged modem by its AT port, e.g. to rescue a modem stuck in the wrong mode. Body: `{at_port, mode}` (mode optional, default 0). | USB-net-mode result object |

**`POST /api/devices` body** — `{"config": <deviceConfigPayload>}` where
`deviceConfigPayload` is:

```jsonc
{
  "id": "string (required, 1-64 safe chars)",
  "name": "string",
  "device_type": "string (required)",
  "interface": "string", "control_device": "string", "at_port": "string",
  "usb_path": "string", "audio_device": "string", "modem_imei": "string",
  "sim_pin": "string", "apn": "string",
  "proxy_port": 0, "baud_rate": 0, "data_bits": 0, "stop_bits": 0, "parity": "string",
  "device_backend": "string", "esim_transport": "string",
  "qmi_use_proxy": false, "qmi_proxy_path": "string", "qmi_proxy_executable": "string",
  "network_enabled": false, "sms_enabled": false, "vowifi_enabled": false
}
```
The server matches this payload against a freshly discovered physical
device (by IMEI, USB path, control device, or AT port) and rejects
`404 device_not_found` if no match. Newly added hardware always starts with
`vowifi_enabled: true` and RF disabled (fail-closed); `network_enabled` is
force-cleared unless developer mode is active. Errors: `400 invalid_device_id`,
`400 invalid_device_type`, `409 device_exists`, `409 device_limit_reached`.

### Per-device: config, status, lifecycle

| Method | Path | Description |
|---|---|---|
| DELETE | `/api/devices/{id}` | Remove a device's stored configuration. Does **not** touch physical hardware. `data: {deleted:true, physical_device_untouched:true}` |
| PUT | `/api/devices/{id}` | Update a device's stored configuration. Same `{"config": <deviceConfigPayload>}` body; `id` is immutable (`409 immutable_device_id` if it differs). `data: {status:"saved", config: <stored config>}` |
| GET | `/api/devices/{id}/overview` | Full live overview (summary + live traffic + `public_ip_info`, etc), wrapped as `{devices: [<overview>]}`. |
| GET | `/api/devices/{id}/overview/stream` | Server-Sent Events (`text/event-stream`) pushing a fresh `overview` payload every ~2s. Events: `connected`, `overview`. |
| GET | `/api/devices/{id}/status` | A narrower live status object (`healthy, public_ip, network_connected, network_phase, network_error, modem, vowifi, last_hardware_refresh, ...`). |
| GET | `/api/devices/{id}/config` | Stored config only (`sim_pin` returned masked as `store.SecretMask` when set). `data: {config: <stored config>}` |
| POST | `/api/devices/{id}/actions/refresh` | Force a fresh hardware snapshot read. `data: <device.Snapshot>` |
| POST | `/api/devices/{id}/actions/reboot` | Reboot the modem (`AT+CFUN=1,1` equivalent). `202 Accepted`, `data: {status:"rebooting"}` |

### Per-device: raw AT / USSD

| Method | Path | Description |
|---|---|---|
| POST | `/api/devices/{id}/actions/at` | Send a raw AT command. Body: `{"cmd":"AT+...", "timeout_ms": 0, "force": false}`. `cmd` must start with `AT`, ≤512 chars, one line. Without `force:true`, several dangerous commands are blocked (`+QCFG="USBNET"`, `+CFUN=`, `+CGATT=`, `+CMGS`, dial/answer/hangup shorthands, etc) → `400 unsafe_at_command`. A modem `ERROR`/`+CME ERROR` reply is still returned as `200 OK` with the error text in `data.response` (an AT terminal must see it). `data: {response, final, duration_ms, urcs}` |
| POST | `/api/devices/{id}/actions/ussd` | Send a USSD code. Body: `{"command":"*100#", "timeout_ms": 0}`. Special-cased locally: `*#06#` returns the IMEI, `*#0000#` returns the firmware version, without touching the modem. Routed over IMS/USSI when VoWiFi is enabled and IMS is registered, else cellular `AT+CUSD`. `data: {result: {status, text, raw, dcs, continueable}, session_id}` |
| POST | `/api/devices/{id}/actions/ussd/continue` | Continue a multi-round USSD dialog. Body: `{"session_id", "input"}` (also accepts `sessionId`/`command` aliases). Same response shape as the USSD action. |
| POST | `/api/devices/{id}/actions/ussd/cancel` | Abort an open USSD dialog. Body: `{"session_id"}`. `data: {cancelled:true, session_id}` |

### Per-device: radio, network, operator

| Method | Path | Description |
|---|---|---|
| PATCH | `/api/devices/{id}/flight-mode` | Toggle airplane mode. Body: `{"enabled": bool}`. `409 vowifi_owns_airplane_mode` if VoWiFi is enabled (VoWiFi always keeps CFUN=4). `data: <device.FlightResult>` |
| GET | `/api/devices/{id}/network` | Read roaming cellular-data state (**developer mode required**, else `403 developer_mode_required`). `data: {enabled, desired_enabled, connected, phase, modem_phase, maintenance_phase, last_error, interface, apn, export_proxy_only:true}` |
| PATCH/PUT | `/api/devices/{id}/network` | Enable/disable roaming cellular data. Body: `{"enabled": bool, "apn": "string"}`. `409 vowifi_owns_radio` if VoWiFi is enabled; `409 export_proxy_active` if disabling while an Export Proxy is bound. `202 Accepted`, `data: {enabled, desired_enabled, connected, phase, modem_phase, maintenance_phase, revision, interface, backend, export_proxy_only:true}` |
| GET | `/api/devices/{id}/network/apns` | Parse the modem's configured PDP contexts (`AT+CGDCONT?`). `data: {items: [{cid, apn, ip_version}]}` |
| GET/POST | `/api/devices/{id}/network/public-ip` | Read (GET) or actively detect (POST) the roaming public IP over the cellular interface (**developer mode required**). GET: `data: {detected, ip, country_code, region, city, organization}`. POST errors: `409 cellular_data_disabled`, `409 sim_identity_unavailable`, `409 cellular_interface_missing`, `502 public_ip_lookup_failed`. |
| GET/PATCH | `/api/devices/{id}/usbnet-mode` | Read/set the modem's USB networking mode. PATCH body: `{"mode": int}`. PATCH response includes `reboot_required:true`. `data: <device.USBNetMode>` (GET) or `{mode, name, reboot_required}` (PATCH) |
| GET | `/api/devices/{id}/operator_selection` | Current PLMN selection. `data: {mode:"automatic"|"manual", plmn, access_technology}` |
| POST/PUT/PATCH | `/api/devices/{id}/operator_selection` | Set automatic/manual operator selection. Body accepts either `{"mode":"manual"|"automatic","plmn":"...","access_technology":int}` or legacy `{"automatic":bool,"plmn":"...","rat":"LTE"|"NR5G"|...}`. Can block for tens of seconds while the modem searches. |
| POST | `/api/devices/{id}/operator_selection/reregister` | Force cellular re-registration. Same response shape as GET `operator_selection`. |
| GET | `/api/devices/{id}/operator_selection/scan` | Blocking operator scan. `data: {scanId, status, candidates: [{status, operatorName, shortName, plmn, countryCode, rats, includesPcsDigit}]}` |
| GET | `/api/devices/{id}/operator_selection/scan/stream` | SSE progress for an operator scan. Events: `operator_scan` with `{scanId, status:"running"|"complete"|"failed", candidates?, message?, retryable?}`. |
| PATCH | `/api/devices/{id}/cellular-ims` | Set the native-modem cellular IMS (VoLTE) mode (only on backends implementing `cellularIMSController`, else `501 cellular_ims_unsupported`). Body: `{"mode": "mbn_default"|"force_enabled"|"force_disabled"}` (also accepts legacy `{"enabled": bool}`). May trigger a modem reboot (`202 Accepted` + auto-recovery) when the mode changes. |
| GET | `/api/devices/{id}/cellular-ims` | Read cellular IMS status. `data: {iccid, mode, desired_enabled, supported, configured, registered, volte_capable, cs_known, cs_registered, changed, rebooting}` |

### Per-device: VoWiFi

| Method | Path | Description |
|---|---|---|
| PATCH | `/api/devices/{id}/vowifi` | Enable/disable VoWiFi. Body: `{"enabled": bool}`. `409 cellular_data_active` if roaming data is on; `403 region_blocked` if the SIM's IMSI is region-blocked. Enabling forces the modem into airplane mode first (synchronously). `202 Accepted`, `data: {accepted:true, enabled, status:"starting"|"stopping"|"in_progress", runtime: <VoWiFi runtime state>}` |
| POST | `/api/devices/{id}/vowifi/actions/reconnect` | Force the active VoWiFi/IMS session to reconnect. `409 vowifi_disabled` if VoWiFi isn't enabled. `202 Accepted`, `data: {accepted:true, status:"reconnecting", runtime}` |
| POST | `/api/devices/{id}/vowifi/e911/websheet` | Create a short-lived, token-authenticated self-hosted E911 address-entry session (embedded in an iframe by the frontend; VoCat cannot reproduce the real operator websheet). `data: {id, token, embed_url, expires_at}`. The embedded form itself is served outside `/api` at `/websheets/{id}` — see the **Websheets** note below. |

The VoWiFi runtime state object (`runtime` above, and the `vowifi_runtime`
field on device summaries) has this shape: `device_id, phase, enabled,
active, carrier_profile, carrier_profile_from, dataplane_mode, iccid, imsi,
sim_ready, access_ready, tunnel_ready, ims_ready, sms_ready, reg_status,
reg_status_text, network_mode, local_phone, phone_number_source,
last_error_class, last_error, last_reason, updated_at, tunnel, imscore,
smsip`.

### Per-device: voice calls

| Method | Path | Description |
|---|---|---|
| GET | `/api/devices/{id}/calls` | List current calls. Uses the active VoWiFi IMS call list when IMS is registered (`transport:"vowifi"`), else `AT+CLCC` (`transport:"cellular"`). `data: {device_id, transport, calls: [...], raw?}` (`raw` present only for the cellular path — raw `AT+CLCC` text). Both transports report the same vocabulary: `direction` is `incoming`/`outgoing` and `state` is `active`/`held`/`dialing`/`ringing`/`waiting`. The cellular path also carries `state_code`, `direction_code`, `mode`, `index` and `raw` from 27.007, and **drops non-voice records** — EC20/EC25 firmware lists an active packet-data session as a call, which otherwise reads as a call in progress for as long as mobile data is connected |
| POST | `/api/devices/{id}/calls/dial` | Dial. Body: `{"number":"+1...", "duration_seconds": 0}` (0 = no automatic hang-up, else 1–600s auto-hangup timer). `400 invalid_number` / `400 invalid_duration` on bad input. `202 Accepted`, `data: {accepted:true, action:"dial", number, call_id?, duration_seconds, transport, call?}` |
| POST | `/api/devices/{id}/calls/answer` | Answer a ringing call. Body: `{"call_id": "string, optional — auto-resolved to a ringing call if omitted"}`. |
| POST | `/api/devices/{id}/calls/hangup` | Hang up. Body: `{"call_id": "string, optional — resolved to any active call if omitted"}`. |
| POST | `/api/devices/{id}/calls/dtmf` | Send keypad digits into a live call as RFC 4733 telephone events. Body: `{"call_id": "string, optional — resolved to the active call if omitted", "digits": "*123#"}`. Digits are `0-9`, `*`, `#`, `A-D` (max 64); anything else is `400 invalid_digits`. `409 dtmf_failed` means the carrier declined `telephone-event` in its SDP answer, so this call cannot carry digits at all. `501 dtmf_unavailable` means the call is on the modem's circuit-switched path rather than IMS, where there is no RTP stream to put events on. Tones are never sent as audio — see [docs/sip-trunk.md](sip-trunk.md#keypad-digits). `data: {sent:true, digits, call_id}` |
| GET | `/api/devices/{id}/calls/media` | **WebSocket upgrade**, not a normal HTTP response. Query param `call_id` (required). Only available for an active VoWiFi IMS call (else `501 call_media_unavailable`). Bridges raw PCM audio: each WS binary message is little-endian signed 16-bit, 8 kHz, mono samples in both directions — the browser sends microphone audio and receives the call's downlink audio. |

### eSIM (`/api/devices/{id}/esim/...`)

Handled by `handleESIM` in `internal/server/esim_api.go`.

| Method | Path | Description |
|---|---|---|
| GET | `/api/devices/{id}/esim` | eUICC chip info + profile groups. `data: {chipInfo: {eids: [{eid, aid, freeNvramBytes?, freeNvram?, manufacturer?, certificates?, trustedCiKeyIds?, defaultSmdpAddress?, rootDsAddress?, sasAccreditationNumber?}], firmware?}, profiles: [{eid, aidHex, profiles: [{iccid, name, serviceProviderName, state, stateText, classText}]}]}` |
| GET | `/api/devices/{id}/esim/profiles` | Just the `profiles` array from above (same group shape), unwrapped as `data`. |
| PATCH | `/api/devices/{id}/esim/profiles/{iccid}` | Rename (nickname) a profile. Body: `{"name":"string (required)", "aid_hex"|"aidHex":"string, optional"}`. |
| DELETE | `/api/devices/{id}/esim/profiles/{iccid}` | Delete a disabled profile. Query param `?aid_hex=`. `data: {status:"deleted", iccid, spaceDelta:{direction:"reclaimed", bytes}, warning?}` |
| GET | `/api/devices/{id}/esim/notifications` | List pending eUICC SM-DP+ notifications. `data: {items: [{sequenceNumber, event, iccid, address, aidHex, canRetry}]}` |
| POST | `/api/devices/{id}/esim/notifications/{sequenceNumber}/actions/retry` | Re-send a pending notification to the operator, then drop it from the eUICC's pending list. Query param `?aid_hex=`. `data: {status:"sent", message}` |
| POST | `/api/devices/{id}/esim/actions/switch` | Switch (enable) an already-installed profile by ICCID (no authentication key required). Body: `{"iccid":"string (required)", "aid_hex"|"aidHex":"string, optional"}`. This is a heavyweight, multi-step operation: quiesces VoWiFi, forces airplane mode, verifies the ICCID after switching, restores the target profile's saved card policy and VoWiFi/airplane state. Can take tens of seconds. `data: {status:"switched", iccid, verified:true, card_policy: <card policy>}` |
| POST | `/api/devices/{id}/esim/actions/disable` | Disable the currently active profile. Body: `{"iccid":"string (required)", "aid_hex"|"aidHex":"string, optional"}`. `data: {status:"disabled", iccid, recovering:true}` |
| GET | `/api/devices/{id}/esim/actions/download` | **Server-Sent Events**, driven by query params (not a JSON body): `smdp` (required), `matching_id`, `confirmation_code`, `aid_hex`, `imei`. Streams `event: progress` with `data: {step, msg, pct, code?, space_delta?, warning?}` as the profile (写卡/download) proceeds; terminal event has `step:"done"` or `step:"error"`. |

USB SIM Reader devices (`device_type: "usb_sim_reader"`) reject the
cellular-only operations (`network*`, `operator_selection*`, raw AT,
USSD, reboot, calls, etc) with `409 wifi_calling_only_device` — they only
support VoWiFi calling, IMS SMS and calls. Native OpenStick 410 devices
similarly reject `calls`, `actions/reboot`, and `cellular-ims` with
`501 device_feature_unsupported`.

---

## SMS

`internal/server/sms_api.go`. Not scoped under a device path — filtered by
optional `device_id` query params instead, because SMS history persists
across device renames/reconfiguration (keyed internally by modem IMEI).

| Method | Path | Description |
|---|---|---|
| GET | `/api/sms/contacts` | List SMS conversation threads (one row per peer number). Query: `device_id` (optional, `"all"`/omitted = every device), `limit` (default 100, max 1000). `data`: array of `{device_id, device_name, modem_imei, iccid, imsi, local_phone, peer, display_name, last_message, last_content, last_timestamp, direction, last_type:"sms", last_sms_id, unread_count, message_count}` |
| GET | `/api/sms/thread` | List messages in one thread. Query: `peer` (**required**), `device_id`, `modem_imei`, `iccid`, `imsi`, `limit` (default 100), `before_id` (pagination cursor). Marks fetched inbound messages as read as a side effect. `data`: array of `{id, message_id, device_id, modem_imei, iccid, imsi, local_phone, peer, direction, body, content, sender, recipient, type:"sms", timestamp, status, source, parts_total, delivery_state}` |
| DELETE | `/api/sms/thread` | Delete an entire thread (query params same as GET, `peer` required). Also deletes the messages from modem storage where applicable. `data: {deleted: <count>}` |
| POST | `/api/sms/send` | Send an SMS. Body: `{"device_id":"string (required)", "phone":"string", "message":"string"}`. SMS to `+86`/`0086` (China) destinations is blocked (`400 blocked_destination`). Globally rate-limited (rolling 1-hour window, developer-configurable limit) → `429 sms_rate_limited` with `Retry-After` header when exceeded. Prefers VoWiFi/IMS delivery when ready, else cellular `AT+CMGS`. Response is `202 Accepted` on full success, or `502` with both `data` and `error` on a submission the server cannot confirm — inspect `data.part_results`/`data.outcome` before ever retrying, since a retry after partial submission can duplicate a message. `data`: `{message_id, id, parts_total, parts_attempted, parts_accepted, all_parts_accepted, concat_reference, part_results, delivery_state, submission_state, message_reference?, reference_known?, submission_accepted, delivery_confirmed, outcome, transport}` |
| DELETE | `/api/sms/messages/{id}` | Delete one stored SMS message (and its modem-storage copy, if any) by its numeric database `id`. `data: {deleted:true}` |

---

## Cards (per-ICCID policy & APN profiles)

`internal/server/settings_api.go`. A "card" is one SIM/eSIM profile
addressed by ICCID (10–32 decimal digits), independent of which physical
device currently hosts it.

| Method | Path | Description |
|---|---|---|
| GET | `/api/cards/policies` | List every stored card policy. `data`: array of `{iccid, network_enabled:false, vowifi_enabled, airplane_enabled, apn, ip_version, custom_phone_number, source, created_at?, updated_at?}` |
| GET | `/api/cards/{iccid}/policy` | Read one card's policy (defaults are synthesized if none stored: `vowifi_enabled:true, airplane_enabled:true, ip_version:"IPV4V6"`). If the ICCID is currently live on a present device, `vowifi_enabled`/`airplane_enabled` reflect that device's *live* state rather than the stored value. |
| PUT | `/api/cards/{iccid}/policy` | Update a card's policy. Body (all fields optional, at least one required): `{"vowifi_enabled":bool, "airplane_enabled":bool, "apn":string, "ip_version":"IP"\|"IPV6"\|"IPV4V6", "custom_phone_number":string}`. Enabling `vowifi_enabled` forces `airplane_enabled:true`. |
| GET | `/api/cards/{iccid}/apns` | List saved APN profiles for a card. `data: {items: [<APN profile>]}` |
| POST | `/api/cards/{iccid}/apns` | Create an APN profile. Body: `{"apn":"string (required)", "username", "password", "clear_password":bool, "proxy", "mcc":"3 digits", "mnc":"2-3 digits", "ip_version":"IP"\|"IPV6"\|"IPV4V6" (default IPV4V6), "roaming_ip_version": same enum (default IP), "auth_type":"NONE"\|"PAP"\|"CHAP"\|"PAP_OR_CHAP" (default NONE)}`. `201 Created`. |
| GET/PATCH/PUT/DELETE | `/api/cards/{iccid}/apns/{profileId}` | Read/update/delete one APN profile. `PATCH`/`PUT` take the same body as POST. Deleting or renaming the profile the card policy currently points at also updates the policy. |

APN profile response shape: `{id, iccid, apn, username, has_password: bool,
proxy, mcc, mnc, ip_version, roaming_ip_version, auth_type, created_at,
updated_at}` (the raw password is never returned, only `has_password`).

---

## Proxy (upstream SOCKS5 for VoWiFi routing)

`internal/server/proxy_api.go`. These are *upstream* SOCKS5 proxies used to
route a profile's VoWiFi traffic through a specific egress (e.g. for
country-matched IMS routing) — distinct from the Export Proxy feature
below, which exposes a device's *own* cellular data as a local SOCKS
proxy.

| Method | Path | Description |
|---|---|---|
| GET | `/api/upstream-proxies` | List configured upstream proxies (password redacted). |
| POST | `/api/upstream-proxies` | Create one. Body: `{"id":"string (required, 1-64 safe chars)", "name", "addr":"host:port", "username", "password", "enabled":bool}`. Saves, then probes the proxy (SOCKS5 auth + a real UDP ASSOCIATE round-trip, since VoWiFi needs UDP) and returns the probe result alongside. |
| PUT | `/api/upstream-proxies/{id}` | Update one (same body; `id` is immutable). Reconnects any VoWiFi sessions bound to profiles using this proxy. |
| PATCH | `/api/upstream-proxies/{id}` | Toggle `{"enabled": bool}` only. |
| DELETE | `/api/upstream-proxies/{id}` | Delete. Reconnects any bound VoWiFi sessions (they fall back to direct routing). |
| POST | `/api/upstream-proxies/{id}/actions/probe` | Re-probe connectivity for a stored proxy (no body). `data: {status:"probed", probe: <probe result>, message}` |
| POST | `/api/upstream-proxy-probe` | Probe proxy settings **from the editor form**, before saving — same body shape as create, so an existing proxy's blank/`***` password field falls back to the stored secret. Same response shape as the per-id probe. |
| GET | `/api/upstream-proxy-countries` | Static reference list of supported countries and their MCCs, for the country-rules UI. `data`: array of `{country_code, country_name, mccs: [string]}` |
| GET | `/api/upstream-proxy-country-rules` | List per-country routing rules. `data`: array of `{country_code, country_name, upstream_proxy_id, enabled}` |
| PUT | `/api/upstream-proxy-country-rules/{countryCode}` | Set/replace the rule for one country. Body: `{"upstream_proxy_id":"string (required, must exist)", "enabled":bool}`. |
| DELETE | `/api/upstream-proxy-country-rules/{countryCode}` | Remove a country rule. |
| GET | `/api/upstream-proxy-profile-bindings` | List per-profile (ICCID) proxy bindings. `data`: array of `{device_id, iccid, profile_name, upstream_proxy_id}` |
| POST | `/api/upstream-proxy-profile-bindings` | Bind 1–200 profiles to one upstream proxy at once. Body: `{"upstream_proxy_id":"string (required, must be enabled)", "bindings":[{"device_id","iccid","profile_name"}]}`. `409 profile_already_bound` if an ICCID is bound to a different proxy. |
| DELETE | `/api/upstream-proxy-profile-bindings` | Remove 1–200 bindings. Body: `{"upstream_proxy_id":"string, optional filter", "iccids":["string", ...]}`. |

The upstream proxy response object: `{id, name, addr, username, password,
enabled}` (password is the redacted/masked form on list/read, per
`Redacted()`).

## Export Proxy (developer mode only)

`internal/server/export_proxy_api.go`. Exposes one configured device's
cellular data as a local SOCKS5/HTTP proxy listener for external clients.
**All paths under `export-proxies` require developer mode**, otherwise
`403 developer_mode_required`.

| Method | Path | Description |
|---|---|---|
| GET | `/api/export-proxies` | List configured export-proxy listeners. `data: {configs: [<Config>]}` |
| POST | `/api/export-proxies` | Create one. Body is an `exportproxy.Config`: `{"id","name","device_id","interface","mode","listen_host","listen_port","enabled":bool,"auth_enabled":bool,"username","password"}`. Rejects `409 wifi_calling_only_device` for USB SIM Reader devices (no cellular data to export). `201 Created`. |
| GET | `/api/export-proxies/status` | Runtime status of every listener. `data: {configs: [{id,name,mode,enabled,running,listen,error?,started_at?}]}` |
| PUT | `/api/export-proxies/{id}` | Update a config (same body). |
| DELETE | `/api/export-proxies/{id}` | Remove a listener. `data: {deleted:true}` |

---

## Settings

`internal/server/settings_api.go`, `access_control.go`, `logging_api.go`,
`https_settings.go`, `developer_settings.go`, `vowifi_settings.go`,
`general_api.go`. All under `/api/settings/*` unless noted.

| Method | Path | Description |
|---|---|---|
| GET/PUT | `/api/settings/preferences` | UI language preference. Body: `{"language":"en"\|"zh"}`. **GET is exempt from authentication** (see Authentication section). `data: {language}` |
| GET/PUT | `/api/settings/security` | Network access policy. Body: `{"mode":"internal"\|"public", "allowed_cidrs":["string CIDR or IP", ...], "trust_proxy_headers":bool}`. `data: {mode, allowed_cidrs, trust_proxy_headers, client_ip, client_allowed}` |
| GET/PUT | `/api/settings/logging` | Log retention policy. Body: `{"mode":"unlimited"\|"count"\|"days", "count":int, "days":int}`. `data: {mode, count, days, stored_logs, max_logs}` |
| GET/PUT | `/api/settings/vowifi` | VoWiFi MTU-compatibility toggle. Body: `{"mtu_compatibility": bool}` (required on PUT). `data: {mtu_compatibility}` |
| GET/PUT | `/api/settings/sms` | SMS behavior settings. Body: `{"auto_clear_modem_storage": bool}` (required on PUT) — whether delivered SMS are auto-deleted from modem storage after being persisted. `data: {auto_clear_modem_storage}` |
| GET/PUT | `/api/settings/https` | Self-signed HTTPS toggle (**developer mode required — 404 otherwise**; also `503 https_unavailable` if the HTTPS manager isn't configured). Body: `{"enabled": bool}`. `data`: `httpsmode.Manager.State(...)` shape (enabled state, certificate info, etc). |
| GET | `/api/settings/https/certificate` | Download the self-signed certificate as `application/x-pem-file` (**developer mode required**). Not JSON-wrapped — raw PEM bytes with `Content-Disposition: attachment`. |
| GET/PUT | `/api/settings/developer` | Developer-mode limits (**only reachable when developer mode is already active — 404 otherwise**). Body: `{"device_limit":int, "sms_hourly_limit":int, "auto_clear_modem_storage":bool}` (at least one required). `data: {device_limit, default_device_limit, max_device_limit, sms_hourly_limit, default_sms_hourly_limit, max_sms_hourly_limit, auto_clear_modem_storage}` |
| POST | `/api/settings/password` | Change the administrator password. Body: `{"old_password","new_password","confirm_password"}`. `400 password_mismatch` if new≠confirm; `401 invalid_credentials` if old is wrong; `400 weak_password`/`400 password_reused`. Clears auth cookies on success (`reauthentication_required:true`) — every session, including the caller's, must sign in again. |
| GET | `/api/system/info` | Build/runtime info. `data: {version, build_time, config, os, architecture, uptime, developer: bool}` |
| GET | `/api/system/update/check` | Check for a software update against the configured update repository. `data: {available, current_version?, version, message, repository?, is_docker?}` (all-false/placeholder response if no update source is configured). |
| POST | `/api/system/update/apply` | Download and apply an update, then schedule a restart. `409 container_update_required` if running in Docker (pull a new image instead). `409 update_busy` if already applying. On success, revokes **every** session and restarts the process; response includes `reauthentication_required:true`. |
| GET/DELETE | `/api/logs/history` | GET: recent persisted log entries. Query: `lines` (default 500, max 2000), `level` (`debug`\|`info`\|`warn`\|`error`, min-level filter), `search` (substring, case-insensitive, over message/caller/fields). `data: {logs: [<loghub.Entry>]}`. DELETE: clear all persisted logs. `data: {cleared:true, deleted:<count>}` |
| GET | `/api/logs/stream` | **Server-Sent Events**, live log tail. Query: `level`. Events: `connected`, then `log` events each carrying one JSON `loghub.Entry`; periodic comment-only keepalive lines. |
| GET | `/api/settings/notifications` | Read all notification-channel configs (secrets redacted). `data`: object keyed by channel (`telegram, email, webhook, bark, pushplus, wecom, lark`), each `{enabled: bool, ...channel-specific fields}`. |
| PUT | `/api/settings/notifications` | Update one or more channels at once. Body: object keyed by channel name, each value `{"enabled": bool (required), ...channel-specific fields}`. Per-channel field schemas: <br>• `telegram`: `bot_token, chat_id, admin_id, base_url, proxy` (all string) <br>• `email`: `use_ssl`(bool), `smtp_host`, `smtp_port`(int), `username`, `password`, `from_address`, `to_addresses`(string[]) <br>• `webhook`: `urls`(string[]), `secret`, `timeout_ms`(int), `retry_max`(int), `text_template`, `headers`(map[string]string) <br>• `bark`: `urls`(string[]), `group`, `icon`, `level` <br>• `pushplus`: `token`, `topic`, `channel` <br>• `wecom`: `urls`(string[]), `payload_template` <br>• `lark`: `url`, `signing_enabled`(bool), `secret`, `payload_template` <br>Sending an unrecognized field or channel name returns `400 invalid_notification_config`/`400 invalid_notification_channel`. |
| POST | `/api/settings/notifications/{channel}/test` | Send a test message through one channel (`webhook`, `telegram`, `email`, `bark`, `wecom`, `lark` — others return `501 notification_test_unsupported`). Body is the channel's config object (same shape as PUT; blank/masked secret fields fall back to the stored value so you can test without re-entering a saved secret). Outbound requests are restricted to public, non-local addresses (SSRF guard) → `400 unsafe_destination` if the resolved address is private/loopback/link-local/etc. `data: {channel, success:true, tested_at}` on success. |
| GET | `/api/traffic/analysis` | Aggregated RX/TX traffic buckets (**developer mode required**). Query: `range` (`hour`\|`day`\|`week`\|`month`, default `day`), `device_id` (optional filter). `data: {status:"ok", range, buckets: [{bucket, period_start, rx_bytes, tx_bytes, total_bytes}]}` |

---

## Automatic Tasks

`internal/server/automatic_tasks.go`. Scheduled, retried, unattended
operations (send an SMS, place a call, or sample the roaming public IP) run
against a specific device + eSIM profile on a repeating schedule, switching
profiles and toggling VoWiFi/cellular as needed and restoring the prior
state afterward.

| Method | Path | Description |
|---|---|---|
| GET | `/api/automatic-tasks` | List all tasks. Non-developer-mode callers only see tasks whose `task_type`/`environment` don't require developer mode (i.e. `public_ip` tasks and `environment:"cellular"` tasks are hidden). `data: {tasks: [<AutomaticTask>]}` |
| POST | `/api/automatic-tasks` | Create a task. See body shape below. `201 Created`, `data: <AutomaticTask>` |
| PUT | `/api/automatic-tasks/{id}` | Update a task (same body). |
| DELETE | `/api/automatic-tasks/{id}` | Delete a task. `data: {deleted:true}` |
| GET | `/api/automatic-tasks/runs` | List task run history. Query: `limit`, `offset`. Non-developer callers see only runs of developer-unrestricted tasks. `data: {runs: [<AutomaticTaskRun>], total: int}` |
| POST | `/api/automatic-tasks/{id}/run` | Queue an immediate out-of-schedule run. `409`-style errors surface as `409 wifi_calling_only_device` (device/task capability mismatch) or `404 task_unavailable` (needs developer mode). `202 Accepted`, `data: <AutomaticTaskRun>` |

**Task body** (`POST`/`PUT`):
```jsonc
{
  "name": "string (required)",
  "enabled": true,
  "device_id": "string (required, must exist)",
  "profile_iccid": "string (required)",
  "profile_aid": "string",
  "task_type": "sms" | "call" | "public_ip",
  "environment": "vowifi" | "cellular",   // public_ip requires "cellular"
  "interval_days": 1,          // 1-365
  "start_date": "YYYY-MM-DD",
  "run_time": "HH:MM",
  "timezone": "IANA tz name (default: server local)",
  "retry_count": 0,            // 0-10
  "notify": false,
  "payload": {                 // shape depends on task_type
    "phone": "string",           // sms, call
    "message": "string",         // sms
    "duration_seconds": 0         // call, 1-600
  }
}
```
`environment:"cellular"` and `task_type:"public_ip"` require developer mode
both at save time and at run time. USB SIM Reader devices are restricted to
`environment:"vowifi"` and `task_type` `sms`/`call`.

`AutomaticTask` response fields: `id, name, enabled, device_id,
profile_iccid, profile_aid, task_type, environment, interval_days,
start_date, run_time, timezone, payload, retry_count, notify, next_run_at,
last_run_at, last_status, last_error, created_at, updated_at`.

`AutomaticTaskRun` response fields: `id, task_id, device_id, scheduled_at,
started_at, finished_at, status, attempts, output, error, created_at,
updated_at`.

---

## Call records

`internal/server/call_records_api.go`. Every call is recorded — placed from
the browser, through the SIP trunk, or by an automatic task — by sampling
live IMS call state every two seconds rather than by the code that dials,
answers or hangs up. See [docs/sip-trunk.md](sip-trunk.md#call-records) for
why.

Read-only by design: a history that could be edited would be a claim rather
than a record. Records are pruned after 90 days.

| Method | Path | Description |
|---|---|---|
| GET | `/api/calls/records` | List call history, newest first. Query: `device_id`, `direction` (`outgoing`/`incoming`), `disposition` (`answered`/`no_answer`/`busy`/`cancelled`/`failed`), `search` (substring of the peer number), `limit` (1–200, default 50), `offset`. `data: {records: [...], total, limit, offset}` |

Each record: `{id, call_id, device_id, device_name, direction, source, peer_number, started_at, answered_at?, ended_at?, duration_seconds, disposition, sip_code?, reason?}`.

- `source` is `browser` or `trunk` — who drove the call.
- `duration_seconds` counts from the **answer**, not the first packet, so a
  call that rang for a minute and was never picked up is `0`.
- `disposition` is empty while a call is still in progress: the field means
  how a call finished, not how it looked at the last sample.

---

## Asterisk (PBX integration)

`internal/server/asterisk_*.go`. These endpoints read and configure the
Asterisk container in front of VoCat over its manager interface (AMI). They
are **not** related to the "Extensions / Plugins" section below — here an
*extension* is a SIP account on the PBX.

Everything requires `VOCAT_ASTERISK_AMI_ADDR` (and the matching user and
secret). Without it, reads report `configured:false` and writes that need a
reload return `501 ami_not_configured` — VoCat still runs normally, it just
cannot see or drive the PBX. Configuration is written to
`VOCAT_ASTERISK_DIALPLAN_DIR`, a directory both containers share; with that
unset, settings still save to VoCat's database but nothing is written for
Asterisk to read.

Full narrative documentation, including what each generated file contains and
what a wrong setting does: [docs/sip-trunk.md](sip-trunk.md).

### Status, channels and history

| Method | Path | Description |
|---|---|---|
| GET | `/api/asterisk/status` | Live PBX state over AMI. Never fails for an unreachable PBX — "Asterisk is down" is the answer this exists to give, not a request failure. `data: {configured, reachable?, address?, version?, error?, contacts_error?, channels_error?, core: {startup_time, reload_time, calls}, endpoints: [...], channels: [...], unpaired_contacts?: [...]}` |
| POST | `/api/asterisk/channels/hangup` | End one live channel. Body: `{"channel": "PJSIP/1001-0000000a", "cause": 16}` (`cause` optional, Q.850 1–127, default 16 "normal clearing"). The name is matched against the live channel list rather than passed through, because AMI's `Hangup` treats a value wrapped in slashes as a **regular expression** — `404 channel_not_found` is also the answer for one. `data: {hungup:true, channel, cause}` |
| GET | `/api/asterisk/registrations` | Endpoint contact state changes over time, newest first — one row per **change**, not per poll. Query: `endpoint`, `limit` (1–500, default 50). `data: {registrations: [{endpoint, contact_uri, status, previous_status, user_agent, via_address, roundtrip_ms, changed_at}], recording}`. `recording:false` means no manager address is configured, so an empty list means "off" rather than "quiet". |

Each endpoint in `status`: `{name, aor, state, active_channels, transport, contacts: [{uri, status, roundtrip_ms, expires, user_agent, via_address, fields}], registered, reachable, fields}`. `fields` on both is the raw AMI message, so a key that moved between Asterisk versions stays visible rather than blanking a row.

### Outbound routes

Which dialled numbers leave through which SIMs. Rendered to `routes.conf`.

| Method | Path | Description |
|---|---|---|
| GET | `/api/asterisk/routes` | `data: {routes: [...], preview, pending, path, writable, can_apply, unknown_devices?, error?}`. `pending` means the file on disk no longer matches what is stored, which is what Apply resolves. `unknown_devices` names devices a route uses that no longer exist — the dialplan is still valid and Apply still succeeds, so this is the only place that mistake is visible before a caller hears it. |
| PUT | `/api/asterisk/routes` | Replace the route list. Body: `{"routes":[{"pattern":"_1NXXNXXXXXX","devices":["usb-...","usb-..."],"timeout_seconds":60,"comment":""}]}`. Rendered before it is stored, so a rejected route is never saved: `400 invalid_route` names the offending rule. Patterns must start with `_`; several devices rotate. |
| POST | `/api/asterisk/routes/apply` | Rewrite the file and reload `pbx_config` over AMI. Calls in progress are unaffected. `502 reload_failed` / `502 ami_unreachable` on failure. |

### Extensions (SIP accounts)

Softphone accounts. Rendered to `endpoints.conf` (PJSIP), `internal.conf`
(extension-to-extension dialling) and `inbound.conf` (where a call arriving
on a SIM rings) — all three from the same list, so an account cannot exist
without being dialable.

| Method | Path | Description |
|---|---|---|
| GET | `/api/asterisk/extensions` | `data: {extensions: [...], preview, internal_preview, inbound_preview, inbound, pending, seeded?, path, can_apply, min_password_length, error?}`. **Passwords are never returned** — each entry carries `has_password` instead, and `preview` renders `password=<hidden>`. `seeded:true` means the file Asterisk is using came from the container entrypoint (the account in `.env`), not from VoCat. |
| PUT | `/api/asterisk/extensions` | Replace the account list. Body: `{"extensions":[{"name":"1001","password":"...","caller_id":"Front Desk","max_contacts":2,"comment":""}], "replace_seeded": false}`. An entry sent **with no password keeps the stored one** — the browser never had it, so every edit would otherwise wipe the credential it never saw. A new account with no password is `400 password_required`. Saving an *empty* list over a seeded file is `409 seeded_extensions` unless `replace_seeded` is set: every softphone would stop registering, and the symptom appears minutes later with nothing pointing at the cause. |
| GET | `/api/asterisk/extensions/candidates` | SIM numbers VoCat has learned from IMS registration, offered as accounts to create. `data: {candidates: [{number, device_id, device_name, iccid, exists}]}`. `number` is reduced to digits so it can be a PJSIP section name; one that already has an extension is listed and marked rather than hidden. |
| POST | `/api/asterisk/extensions/apply` | Rewrite all three files and reload **both** `res_pjsip` and `pbx_config` — one save touches an account list and a dialplan, and reloading only the first leaves a new account registering perfectly and unreachable from every handset. |

Refused names: `vocat`, `transport-udp`, `transport-tcp`, `global`, `system`,
`general` (already section names in the shipped `pjsip.conf` — a duplicate
object makes Asterisk refuse both) and the emergency numbers `911`, `933`,
`112`, `999`, `000`, `110`, `119` (internal dialling is consulted before the
outbound routes, so such an account would shadow the route that reaches
emergency services). Passwords are printable ASCII, no spaces, no `;`, at
least 12 characters — a `;` starts a comment in an Asterisk config file, so
one would be silently truncated and the phone could never register.

### Inbound routing

Where a call arriving on a SIM rings. Rendered to `inbound.conf`.

| Method | Path | Description |
|---|---|---|
| PUT | `/api/asterisk/inbound` | Body: `{"mode":"did"\|"ring_all"\|"hunt", "extensions":["1001","1002"], "ring_seconds":30, "hunt_seconds":15}`. Validated against the configured extensions: a ring group naming an account that does not exist rings nothing, and a caller who reaches no one is the only other signal (`400 invalid_inbound` names it). `data: {saved:true, written, inbound, inbound_preview}` |

- `did` rings the extension **named after the dialled number**. No mapping
  table: VoCat puts the dialled number in the request URI, so the extension
  name is the map. Both the bare and `+E.164` forms are matched.
- `ring_all` ignores the DID. An empty `extensions` list means *every*
  configured extension, so a handset added later joins without a second edit.
- `hunt` rings `extensions` in order, moving on when one does not answer.
  The list is required.

The current plan is returned by `GET /api/asterisk/extensions` under
`inbound`, since the two are always edited against the same account list.

---

## Extensions / Plugins

`internal/server/extensions_api.go`. Third-party plugins with an optional
sandboxed backend process, proxied through the server.

| Method | Path | Description |
|---|---|---|
| GET | `/api/extensions` | List installed plugins. `503 extensions_unavailable` if the plugin manager isn't configured. `data`: array of `<Plugin>` (below). |
| POST | `/api/extensions/install-url` | Install a plugin package fetched from a URL. Body: `{"url":"string", "sha256":"string, optional integrity check"}`. `201 Created`, `data: <Plugin>` |
| POST | `/api/extensions/upload` | Install a plugin package uploaded directly. `multipart/form-data`, field `package` (the plugin archive, ≤64 MiB) and optional field `sha256`. `201 Created`, `data: <Plugin>` |
| PUT | `/api/extensions/{id}` | Enable/disable a plugin. Body: `{"enabled": bool}`. `404 plugin_not_found` if unknown. |
| DELETE | `/api/extensions/{id}` | Uninstall a plugin. `data: {uninstalled:true}` |
| ANY | `/api/extensions/{id}/backend/...` | Reverse-proxied straight through to the plugin's own backend process (path/method/body defined by the plugin itself, not by VoCat). |
| GET/HEAD | `/plugin-assets/{id}/{path}` | Serves a static asset bundled with an installed plugin. **Not under `/api`**; still requires authentication (checked explicitly in the handler, independent of the `/api` dispatcher). |

`Plugin` object: the plugin's manifest (`schema_version, id, name, version,
description?, author?, homepage?, permissions?, contributions,
backend?`) plus `enabled, backend_available, backend_running,
backend_error?, installed_at, sha256`.

---

## System / Health

Infrastructure endpoints registered directly on the mux, **outside** `/api`
and **never behind authentication**.

| Method | Path | Description |
|---|---|---|
| GET | `/healthz` | Bare liveness probe. `{"status":"ok"}` — no `data` wrapper. |
| GET | `/readyz` | Database-readiness probe. `{"status":"ready"}` or `503 {"status":"not_ready"}` — no `data` wrapper. |
| GET | `/metrics` | Prometheus text exposition format (`text/plain; version=0.0.4`). Exposes only non-identifying process metrics: `vocat_up`, `vocat_ready`, `vocat_uptime_seconds`, `vocat_go_goroutines`. No device/SIM/phone/proxy data. |
| GET | `/api/health` | JSON health check, exempt from authentication. `data: {status:"ok", database:"ok", time: <RFC3339>}`, or `503` with `error.code:"unavailable"` if the database isn't ready. |

---

## Websheets (E911 address form)

`internal/server/e911_api.go`. Not under `/api` — these are
token-authenticated by a random token embedded in the URL/query string
(`?token=...` or header `X-Websheet-Token`), created by `POST
/api/devices/{id}/vowifi/e911/websheet` above. Sessions expire 15 minutes
after creation.

| Method | Path | Description |
|---|---|---|
| GET | `/websheets/{sessionId}?token=...` | Serves the self-hosted HTML E911 address-entry form (VoCat cannot embed the real carrier-hosted form, so it self-hosts a minimal equivalent). |
| POST | `/websheets/{sessionId}/callback?token=...` | Submits the entered address (`name, street, city, state, zip, country` — any string keys, ≤256 chars each). Persists it against the originating device as `e911_address:{deviceId}` in app settings. `data: {received:true}` |
| POST | `/websheets/{sessionId}/done?token=...` | Marks the session complete. `data: {done:true}` |

---

## Notes on ambiguous or dynamic response shapes

A few endpoints intentionally return loosely-typed or backend-dependent
payloads and are documented above at the field-name level rather than with
a fixed schema, because the Go handler itself builds the response as
`map[string]any` from data whose exact shape varies by hardware backend
(AT vs QMI vs PC/SC) or IMS provider:

- Device summary/overview/status objects (`modem`, `vowifi_runtime`,
  `traffic*` fields) — assembled per-request from whatever the live
  hardware snapshot exposes; absent fields are omitted or zero-valued
  rather than causing an error.
- `data.probe` on the upstream-proxy probe endpoints — mirrors
  `localproxy.ProbeResult` via a JSON round-trip, plus an `error` key when
  the probe failed.
- Notification channel config objects under `/api/settings/notifications` —
  each channel's fields are validated against a fixed schema
  (`notificationFields` in `settings_api.go`, reproduced above), but the
  response is a generic redacted JSON document, not a typed struct.

No endpoints were found or skipped due to genuine ambiguity beyond the
above — every route reachable from `handleAPI` (via `routeDeviceAPI` and
`routeGeneralAPI`, transitively including `routeSettingsAPI`,
`routeSMSAPI`, `routeProxyAPI`, `routeExportProxyAPI`,
`routeAutomaticTasksAPI`, `routeExtensionAPI`, `routeAsteriskRoutesAPI`
and `routeAsteriskExtensionsAPI`) is documented above.
