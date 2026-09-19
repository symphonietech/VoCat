# Role-based GUI access — design

**Status: proposal. No code written.** This describes how to add user groups
with per-page GUI permissions to VoCat, what it costs, and the decisions that
have to be made before implementation starts.

Read [What exists today](#what-exists-today) first. The design that follows is
shaped by three properties of the current code, and it would look quite
different without them.

---

## Goal

Several GUI accounts instead of one. Each account belongs to a group; each
group is granted a set of pages from the left-hand menu; a member sees and can
reach only those pages. Within a page, read and write are separate grants, so a
group can be allowed to look at something without being allowed to change it.

Out of scope for this proposal: per-device or per-SIM scoping (group A sees only
modems 1–4), and anything about who may log in over which network — that is
already handled separately by the access policy in
`internal/server/access_control.go`.

---

## What exists today

Facts, not assumptions — each one checked against the tree at the time of
writing.

**One authentication choke point.** Every authenticated API request enters
through `mux.HandleFunc("/api/", server.handleAPI)`
(`internal/server/server.go:196`), and `handleAPI` calls `requireAuthenticated`
exactly once (`internal/server/server.go:403`). Across the whole non-test
codebase there are only **four** call sites of `requireAuthenticated`. There is
therefore one place to put an authorization gate for the entire API. This is the
single most important factor in the estimate below.

**Identity is already resolved and then discarded.** `auth.Authenticate`
returns an `AuthenticatedSession` carrying `Principal{ID, Username}`
(`internal/auth/service.go:190-210`), and `handleAPI` throws it away with
`if _, err := s.auth.Authenticate(...)`. The lookup needed to know *who* is
asking already happens on every request.

**Routing is prefix-based and roughly page-shaped.** `routeGeneralAPI`
(`internal/server/general_api.go:25`) dispatches on a cleaned path, trying each
area in turn: automatic tasks, extensions, export proxy, SMS, SMS test, proxy,
settings, Asterisk routes, Asterisk extensions, then a `switch` over individual
paths. `routeDeviceAPI` (`internal/server/device_api.go:155`) handles everything
device-shaped.

**The menu is a single array, already filtered.** `NAV` at
`web/src/components/shell/AuthenticatedShell.tsx:32` holds ten entries;
line 125 conditionally splices in Export Proxy behind the `developer` flag. The
frontend routes are 1:1 with it (`web/src/App.tsx:117-128`).

**One admin, enforced by the schema.** `admins` has
`id INTEGER PRIMARY KEY CHECK (id = 1)` (`internal/store/migrations.go:7-13`).
`sessions.admin_id` already foreign-keys to it (`migrations.go:14-22`). Current
schema version is 27 (`internal/store/store.go:17`).

**API tokens are CLI-only.** `api_tokens` (`migrations.go:430`) is written only
by `vocat api-token create` (`cmd/vocat/api_token.go`); there is no HTTP route
that mints, lists or revokes one. A GUI user — whatever their group — cannot
issue a token. This is better than it might have been, and it removes the
obvious privilege-escalation path. Tokens carry no group, so they authenticate
as full access.

---

## Permission model

A permission is `area:action`, where action is `read` or `write`.

Page-level grants alone are not enough, for a concrete reason established
earlier: anyone who can *edit* an SMS Delivery Test endpoint can point its URL
at a host they control and trigger a run, and the gateway credential is then
delivered to them. Read access to that page is harmless; write access is
equivalent to knowing the password. The same asymmetry applies to Asterisk
config, automatic tasks and proxy rules. So read/write is not a nicety here, it
is the point.

### Menu page → permission area

| Menu page | Area | Notes |
|---|---|---|
| Dashboard | `dashboard` | Read-only in practice. Needs `devices:read`. |
| Devices | `devices` | The big one; see [the shared-devices problem](#the-shared-devices-problem). |
| Proxy | `proxy` | |
| Export Proxy | `exportproxy` | Today also gated by the `developer` flag. |
| SMS Test | `sms` | Inbound SMS inspection (`/sms`). |
| SMS Delivery Test | `smstest` | Plus `smstest.endpoints:write`, below. |
| Voice Calls | `calls` | |
| Asterisk | `asterisk` | |
| Automatic Tasks | `tasks` | |
| Live Logs | `logs` | Logs quote request paths and device identifiers; a group denied Devices can learn a lot here. Treat as sensitive. |
| Settings | `settings` | See [Settings is not one thing](#settings-is-not-one-thing). |

### The shared-devices problem

This is the one genuinely hard part of the design, and it is a modelling
decision rather than a coding difficulty.

`devices` is the most-referenced API prefix in the frontend (12 call sites) and
it backs five pages: Dashboard, Devices, SMS Test, SMS Delivery Test and Voice
Calls. A call cannot be placed without reading the device list; an SMS thread is
meaningless without knowing which modem it came from. `dashboard/devices` and
`dashboard/host` are themselves inside `routeDeviceAPI`.

Three options:

1. **Implied read.** Granting any of the five pages implies `devices:read`.
   Simple, matches how the UI actually works, and honest: you cannot hide the
   modem inventory from someone who is allowed to make calls on it.
2. **Separate `devices:read` grant**, which an administrator must remember to
   add alongside Voice Calls. Stricter on paper; in practice it produces pages
   that half-load with console errors, and administrators will grant it
   universally anyway.
3. **A narrowed device summary** — a new read-only endpoint returning just
   `{id, name, number}` for the pages that only need a picker, with the full
   device payload behind `devices:read`. The correct answer, and the most work:
   it means auditing what each of the five pages actually reads.

**Recommendation: (1) for the first release, with (3) as a follow-up** if the
modem inventory itself turns out to be something worth hiding. Write it down in
the group editor UI — "Voice Calls also grants read access to the device list" —
rather than leaving an administrator to discover it.

### Settings is not one thing

`/settings` covers the admin password, HTTPS certificates, the network access
policy (`settings/security`), developer settings, logging, notifications, SMS
settings, VoWiFi settings and card policies. Granting the page grants all of it,
including the ability to change the network access policy and — once multi-user
exists — potentially other people's passwords.

Split it:

- `settings:read` / `settings:write` — the operational half: logging,
  notifications, SMS, VoWiFi, card policies, preferences.
- `security:write` — HTTPS, the access policy, developer settings.
- `users:write` — create and edit accounts and groups.

`security:write` and `users:write` should be grantable only to a built-in
`owner` group, at least initially. A group that can edit groups can grant itself
everything, which makes every other permission decorative.

### Always allowed

Some endpoints are infrastructure for the shell itself and must be reachable by
any authenticated user regardless of group, or nothing renders:

- `GET /api/auth/session`, `POST /api/auth/logout`
- `GET|PUT /api/settings/preferences` (UI language and theme)
- `GET /api/system/info` (version string, `developer` flag)
- `POST /api/settings/password` — **own** password only. Changing anyone
  else's belongs to `users:write`.

---

## The permission table

One map, the single source of truth, consulted by the gate. Longest prefix
wins, so a specific entry can override the area default.

```
# path prefix                          read            write
dashboard/                             dashboard       —
devices                                devices         devices
device-mgmt/                            devices         devices
proxy, upstream-prox*                   proxy           proxy
export-proxies                          exportproxy     exportproxy
sms/                                    sms             sms
smstest/results, smstest/schedules      smstest         smstest
smstest/endpoints                       smstest         smstest.endpoints
calls/                                  calls           calls
asterisk/                               asterisk        asterisk
automatic-tasks                         tasks           tasks
logs/                                   logs            —
system/info                             (always)        —
system/update/                          —               security
settings/preferences                    (always)        (always)
settings/password                       —               (always, own only)
settings/security                       security        security
settings/developer                      security        security
settings/https*                         security        security
settings/*                              settings        settings
cards/policies                          settings        settings
traffic/analysis                        proxy           —
extensions*                             (per plugin)    security
websheets/*                             own token       own token
```

`smstest/endpoints` write mapping to `smstest.endpoints` rather than `smstest`
is the whole point of the read/write split: a monitoring group gets
`smstest:read` and can watch delivery results forever without ever being able
to redirect the gateway URL.

### Method → action

`GET` and `HEAD` are reads; everything else is a write. Two classes of
exception have to be listed explicitly, or the gate is wrong:

- **A GET that is really a write.** `devices/{id}/calls/media` is a WebSocket
  upgrade carrying bidirectional audio — it must require `calls:write`.
  `overview/stream` and `operator_selection/scan/stream` are SSE reads, but
  `operator_selection/scan` (POST) commands the modem and is a write.
- **A POST that is really a read.** `upstream-proxy-probe` tests connectivity
  and changes nothing. Cheap to leave as a write; list it if operators complain.

Anything not in the table is **denied**. See [Testing](#testing) for why that
default needs defending.

---

## Data model and migration

Schema version 28. SQLite cannot drop a CHECK constraint in place, so `admins`
is rebuilt — the same pattern this repo already uses for `card_apn_profiles`
(`internal/store/migrations.go:343`).

```sql
CREATE TABLE user_groups (
  id          INTEGER PRIMARY KEY AUTOINCREMENT,
  name        TEXT NOT NULL UNIQUE,
  permissions TEXT NOT NULL,          -- JSON array of "area:action"
  builtin     INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL
);

-- The single-admin CHECK has to go, so the table is rebuilt.
CREATE TABLE admins_new (
  id            INTEGER PRIMARY KEY AUTOINCREMENT,
  username      TEXT NOT NULL UNIQUE,
  password_hash BLOB NOT NULL,
  group_id      INTEGER NOT NULL REFERENCES user_groups(id),
  disabled      INTEGER NOT NULL DEFAULT 0,
  created_at    INTEGER NOT NULL,
  updated_at    INTEGER NOT NULL
);

INSERT INTO user_groups (id, name, permissions, builtin, created_at, updated_at)
  VALUES (1, 'owner', '["*"]', 1, :now, :now);

INSERT INTO admins_new (id, username, password_hash, group_id, created_at, updated_at)
  SELECT id, username, password_hash, 1, created_at, updated_at FROM admins;

DROP TABLE admins;
ALTER TABLE admins_new RENAME TO admins;
```

Two things worth being deliberate about:

**The existing admin becomes the sole member of a built-in `owner` group
holding `*`.** Nobody's access changes on upgrade and nobody is locked out —
the migration is invisible until a second account is created. A `builtin` group
cannot be deleted or have its permissions edited.

**`sessions.admin_id` already foreign-keys to `admins(id)` with
`ON DELETE CASCADE`**, so deleting a user ends their sessions for free. Editing
a *group's* permissions does not, and should: either revoke that group's
sessions on change, or have the gate read permissions fresh per request. Reading
fresh is simpler and the cost is one indexed lookup — but it then belongs in the
same query as the session load, not as a second round trip.

---

## Enforcement

One function, called from `handleAPI` immediately after the existing CSRF block
and before the first `route*API` call:

```go
principal, ok := s.requireAuthenticated(w, r)   // now returns the principal
if !ok {
    return
}
if !s.authorize(w, r, principal, cleanPath) {   // 403 and logs the denial
    return
}
```

`requireAuthenticated` changes signature to return the principal it already
computes. `authorize` resolves the group, looks the path up in the table, maps
the method to an action, and fails closed.

Three properties to hold onto:

- **Fail closed.** An unmapped path is 403, never 200. A route added next year
  must be denied until someone adds it to the table.
- **403, not 404.** Hiding existence buys nothing here — every user knows the
  pages exist, they can read the menu source — and 404 makes support calls
  unreadable.
- **Audit the denial.** `internal/server/audit.go` already resolves a principal
  per request; a denied authorization is exactly the event worth recording.

Frontend, for the same reason but not as a boundary:

- `GET /api/auth/session` returns the caller's permission set; the shell filters
  `NAV` against it and the router refuses unpermitted paths, reusing the
  `developer`-flag pattern already at `AuthenticatedShell.tsx:125`.
- Write-gated controls render disabled rather than hidden, so a read-only user
  can see that a thing exists and ask for access instead of filing a bug.

**Hiding a menu item protects nothing.** The user can type the URL, or call the
API with `curl` and their own session cookie. The gate in `handleAPI` is the
entire security control; everything in the browser is presentation. Any review
of this work should read the Go side and treat the React side as cosmetics.

---

## API tokens

Tokens carry no group and authenticate as full access
(`internal/server/server.go:527-537`). They are CLI-issued only, so no GUI user
can mint one, and the boundary is intact as long as that stays true.

Still worth doing, in order of value:

1. Document plainly that a bearer token is full access and that issuing one is
   equivalent to handing over the owner account.
2. Add `group_id` to `api_tokens` and a `--group` flag on
   `vocat api-token create`, so an integration can be scoped to `sms:read`.
3. If a GUI surface for tokens is ever added, it belongs behind `users:write`,
   and it must not be able to issue a token more privileged than the issuer.

---

## Surfaces outside `/api/`

- **`/websheets/`** (`internal/server/e911_api.go:106`) — E911 address sheets,
  authenticated by their own per-session token, not the admin cookie. The
  session is created from a device action, so it inherits whatever
  authorization created it. Leave as-is; note it, and make sure creating one
  requires `devices:write`.
- **`/plugin-assets/`** — static plugin assets. Decide whether an unpermitted
  plugin's assets should 403; low risk either way.
- **Plugin sidebar contributions** are injected dynamically
  (`AuthenticatedShell.tsx:117-121`) and need permissions of their own, most
  simply one area per plugin ID.

---

## Testing

The repo already has per-area `_test.go` files, and adding an authorization
dimension to all of them would be a lot of churn for little signal. Two focused
tests are worth more:

1. **Table-driven gate test.** For each (path, method, group) triple, assert
   allowed or 403. This is where the read/write split gets pinned down — in
   particular that `smstest:read` cannot `PUT /api/smstest/endpoints/{id}`.
2. **Coverage test.** Every API path the frontend calls must resolve to an
   entry in the table. The paths can be extracted from `web/src` by the same
   grep used to size this document (`api("/…")` call sites), kept as a fixture
   list. Without this test, fail-closed turns into "the feature someone broke
   silently in six months" — a new page 403s and the fix looks like a bug report
   rather than a missing table entry.

---

## Work breakdown

| Piece | Size |
|---|---|
| Migration 28: groups table, `admins` rebuild, owner bootstrap | S |
| Store layer: users and groups CRUD, permission load | S |
| `authorize` gate plus the permission table | M |
| Gate and coverage tests | M |
| Users and groups API (`users:write`) | M |
| Users and groups UI, a new Settings tab | L |
| Session payload, `NAV` filter, route guard | S |
| Disabled-state handling on write-gated controls | M |
| Docs: `docs/API.md`, a section here on granting access | S |

**Estimate: 2–4 focused days**, most of it in the group editor UI and in
hardening the table rather than in the gate itself.

Risk is concentrated in three places, all called out above: the shared-`devices`
decision, keeping the default deny honest as the API grows, and making sure
`users:write` and `security:write` stay out of ordinary groups.

---

## Decisions needed before coding

1. **Shared devices** — implied `devices:read` (recommended), explicit grant, or
   a narrowed summary endpoint?
2. **Settings split** — the four-way split proposed above, or coarser?
3. **Live Logs** — logs quote paths and device identifiers. Own area
   (recommended), or owner-only?
4. **Group permission changes** — revoke that group's sessions, or read
   permissions fresh per request (recommended)?
5. **Read-only UI** — disable write controls (recommended) or hide them?
6. **Token scoping** — document-only for now, or add `group_id` in this pass?
