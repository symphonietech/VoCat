<p align="center">
  <img src="web/public/favicon.svg" width="96" alt="Vocat">
</p>

<h1 align="center">VoCat</h1>

<p align="center">
  <img alt="Go" src="https://img.shields.io/badge/Go-1.25-00ADD8?style=flat-square&logo=go&logoColor=white">
  <img alt="React" src="https://img.shields.io/badge/React-19-61DAFB?style=flat-square&logo=react&logoColor=111111">
  <img alt="TypeScript" src="https://img.shields.io/badge/TypeScript-5.8-3178C6?style=flat-square&logo=typescript&logoColor=white">
  <img alt="Vite" src="https://img.shields.io/badge/Vite-7-646CFF?style=flat-square&logo=vite&logoColor=white">
  <img alt="Tailwind CSS" src="https://img.shields.io/badge/Tailwind_CSS-3-06B6D4?style=flat-square&logo=tailwindcss&logoColor=white">
  <img alt="SQLite" src="https://img.shields.io/badge/SQLite-Embedded-003B57?style=flat-square&logo=sqlite&logoColor=white">
</p>

<p align="center">
  <img alt="Linux" src="https://img.shields.io/badge/Linux-amd64_%7C_386_%7C_arm64_%7C_aarch64_%7C_armv7-FCC624?style=flat-square&logo=linux&logoColor=111111">
  <img alt="Docker" src="https://img.shields.io/badge/Docker-Multi--Arch-2496ED?style=flat-square&logo=docker&logoColor=white">
  <img alt="WiFi Calling" src="https://img.shields.io/badge/WiFi_Calling-IMS_SMS-7B1FA2?style=flat-square">
  <img alt="eSIM" src="https://img.shields.io/badge/eSIM-LPA_%2F_eUICC-009688?style=flat-square">
  <img alt="Telegram" src="https://img.shields.io/badge/Telegram-Bot-26A5E4?style=flat-square&logo=telegram&logoColor=white">
  <img alt="GitHub Actions" src="https://img.shields.io/badge/GitHub_Actions-Release-2088FF?style=flat-square&logo=githubactions&logoColor=white">
</p>

**English** | [العربية](docs/README.ar.md) | [简体中文](docs/README.zh-CN.md) | [繁體中文](docs/README.zh-TW.md) | [Français](docs/README.fr.md) | [Русский](docs/README.ru.md) | [Español](docs/README.es.md) | [日本語](docs/README.ja.md)

Vocat is an open-source web control panel and engineering toolkit for Quectel EC20/EC25-class and China Mobile ML307 series cellular modems. It combines modem discovery, live radio status, AT and USSD terminals, SMS, WiFi Calling, eSIM management, network selection, proxy routing, notifications, audit logs, and release automation in one self-contained service.

The backend is written in Go, the interface is built with React and TypeScript, and the production frontend is embedded into the Go binary. A single executable contains the web application and uses SQLite for persistent state.

<p align="center">
  <img src="img\image.png">
  <img src="img\image-1.png">
</p>

## Features

| Area | What Vocat provides |
| --- | --- |
| Device management | Automatic serial/USB discovery, multiple modem support, friendly device names, live overview updates, module restart, flight mode, and USB networking mode controls. |
| Radio and network | Registration status, operator, signal metrics, RSRP/RSRQ/SINR, network mode, band, channel, operator scanning, automatic or manual network selection, and LTE cell querying and single-cell lock/unlock (EARFCN/PCI). |
| AT and USSD | Interactive AT terminal, command history, raw modem responses, USSD start/continue/cancel flows, and clear modem error reporting. |
| SMS | Direct cellular and IMS SMS transmission, inbound synchronization, multipart handling, delivery reports, conversation history, unread state, timestamps, and per-message delivery status. |
| WiFi Calling | IKEv2/ePDG tunnel setup, EAP-AKA authentication, IMS registration, IMS SMS, IMS voice calls with browser audio, reconnect controls, status diagnostics, and per-device routing. |
| SIP trunk | Outbound calls from a PBX over a SIM's IMS registration, G.711 media bridging, source-address peer authorisation, and a ready-made Asterisk container. |
| eSIM and eUICC | eUICC discovery, EID and production information, certificate metadata, multi-eUICC inventory, installed profile listing, enable/disable/switch operations, download, rename, and delete operations when supported by the card. |
| Card policy | ICCID-based WiFi Calling and flight-mode behavior with immediate policy application. |
| Proxy routing | Upstream SOCKS routing, device bindings, country rules, TCP reachability checks, and UDP Associate checks for WiFi Calling data paths. |
| Notifications | New inbound SMS forwarding through Telegram, Bark, email, Pushplus, and signed webhooks. Each SMS is delivered as an individual notification. |
| Telegram bot | Device status, installed-profile listing and switching, WiFi Calling controls, and SMS sending. Sensitive actions require administrator confirmation. |
| Operations | Password and SSO authentication (Pocket ID, GitHub, Generic OAuth2), CSRF protection, access policies, audit events, live logs, log retention, health checks, responsive layout, dark mode, and English/Chinese application UI. |
| Distribution | Static Linux binaries, systemd installation script, self-update with SHA-256 verification, Docker image, GHCR publishing, and GitHub Actions release builds. |

## Supported hardware

Vocat targets Qualcomm-based Quectel and China Mobile ML307 series modules that expose compatible AT, QMI, serial, and USB networking interfaces, including:

- Quectel EC20
- Quectel EC25
- Quectel EG25 family
- Compatible EG600 and related modules
- China Mobile ML307 series (e.g. ML307A-E5, various OEMs)

Available features depend on the module firmware, USB composition, SIM/eSIM capabilities, host drivers, radio network, and carrier configuration.

## Installation

### One-click Linux installation

As root (including OpenWrt/Kwrt, where `sudo` is normally absent):

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh | bash
```

From a normal user on a distribution with sudo:

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh | sudo bash
```

Check the host's VoWiFi/XFRM prerequisites without installing VoCat:

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh | bash -s -- --check-env
```

Install a specific version:

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh -o install.sh
sudo bash install.sh 0.0.2
```

VoWiFi IMS requires Linux XFRM/IPsec. On OpenWrt/Kwrt the installer attempts
to install matching `ip-full`, `kmod-ipsec`, `kmod-ipsec4/6`,
`kmod-crypto-authenc`, AES-CBC and SHA1 packages from the firmware's own feed.
If matching kernel modules are unavailable, use a firmware that includes them;
never force-install kmods built for a different kernel.

If your kernel cannot provide XFRM/IPsec and you only need non-VoWiFi features
such as cellular SMS or data, install with `--skip-vowifi-check`:

```bash
curl -fsSL https://raw.githubusercontent.com/MengMengCode/VoCat/master/scripts/install.sh -o install.sh
sudo bash install.sh --skip-vowifi-check
```

The installer:

- detects `amd64`, `386`, `arm64`, `aarch64`, or `armv7`;
- downloads the matching GitHub Release binary;
- verifies it against `SHA256SUMS`;
- installs Vocat under `/opt/vocat`;
- creates a hardened systemd service with the hardware and network access required by Vocat;
- stores runtime configuration in `/etc/vocat/env`;
- generates a random initial administrator password on first installation.

After installation, open:

```text
http://<server-address>:7575
```

### Manual binary installation

Download the matching binary and `SHA256SUMS` from GitHub Releases:

| Platform | Release file |
| --- | --- |
| Linux x86-64 | `vocat-linux-amd64` |
| Linux x86 32-bit | `vocat-linux-386` |
| Linux ARM64 | `vocat-linux-arm64` |
| Linux AArch64 | `vocat-linux-aarch64` |
| Linux ARMv7 | `vocat-linux-armv7` |

Verify and install it:

```bash
sha256sum -c SHA256SUMS --ignore-missing
sudo install -d -m 0755 /opt/vocat/bin /opt/vocat/data
sudo install -m 0755 vocat-linux-amd64 /opt/vocat/bin/vocat
read -rsp "Admin password: " VOCAT_BOOTSTRAP_PASSWORD; echo
printf '%s\n' "$VOCAT_BOOTSTRAP_PASSWORD" | sudo /opt/vocat/bin/vocat bootstrap-admin
unset VOCAT_BOOTSTRAP_PASSWORD
sudo env \
  VOCAT_DATABASE_PATH=/opt/vocat/data/vocat.db \
  /opt/vocat/bin/vocat serve
```

This manual command runs Vocat in the foreground. Use `vocat serve` so the
process starts the server directly; running `vocat` without arguments as root
on a TTY opens the interactive management menu instead. Use the one-click
installer when a managed systemd service and automatic restart are required.

### Docker

For a Linux host that must discover every attached supported Quectel modem and
continue seeing USB hot-plug events, run Vocat in hardware-access mode:

```bash
docker pull ghcr.io/mengmengcode/vocat:latest

read -rsp "Admin password: " VOCAT_BOOTSTRAP_PASSWORD; echo
printf '%s\n' "$VOCAT_BOOTSTRAP_PASSWORD" | docker run --rm -i \
  --user 0:0 \
  -v vocat-data:/opt/vocat/data \
  --entrypoint /opt/vocat/bin/vocat \
  ghcr.io/mengmengcode/vocat:latest bootstrap-admin
unset VOCAT_BOOTSTRAP_PASSWORD

docker run -d \
  --name vocat \
  --restart unless-stopped \
  --network host \
  --privileged \
  --user 0:0 \
  -v vocat-data:/opt/vocat/data \
  -v /dev:/dev \
  -v /sys:/sys:ro \
  ghcr.io/mengmengcode/vocat:latest
```

Open `http://<server-address>:7575` after the container starts. Host networking
is required so QMI network interfaces remain visible to Vocat, while privileged
device access is required for serial ports, QMI control nodes, TUN interfaces,
network configuration, and devices added after the container starts. The
`/dev` bind mount makes new `ttyUSB*`, `ttyACM*`, `cdc-wdm*`, and MHI
`wwan*` nodes visible without recreating the container.

This mode intentionally gives Vocat broad access to the host's devices and
network stack. Use it only on a trusted Linux host. The automatic discovery
identifies supported Quectel USB modems (USB vendor ID `2c7c`) and China Mobile ML307 series modules by product string, and PCIe/MHI
modems exposed through the Linux WWAN subsystem; it does not identify arbitrary
modem layouts. Mapping only individual nodes with `--device`, such as
`/dev/ttyUSB2`, `/dev/cdc-wdm0`, or `/dev/wwan0qmi0`, limits the container to
those fixed nodes and does not provide complete multi-device or hot-plug discovery.

On OpenWrt hosts, the fw4 input chain drops new connections from interfaces it
does not manage, including Vocat's per-session VoWiFi TUN. Vocat therefore
inserts narrow `vocat-ims-*` accept rules (P-CSCF to the negotiated inner
address) with the `nft` bundled in the image. Without them IMS registration and
outgoing SMS still work, but incoming SMS and calls are rejected. If the bundled
`nft` cannot read the host ruleset, Vocat logs `nft could not inspect the
OpenWrt fw4 input chain`; point `VOCAT_NFT` at the host's own nft instead:

```bash
cat > /opt/vocat-host-nft <<'EOF'
#!/bin/sh
exec /host/lib/ld-musl-$(uname -m).so.1 --library-path /host/usr/lib:/host/lib /host/usr/sbin/nft "$@"
EOF
chmod 755 /opt/vocat-host-nft
# Add to the docker run command above:
#   -v /usr:/host/usr:ro -v /lib:/host/lib:ro \
#   -v /opt/vocat-host-nft:/usr/local/bin/host-nft:ro \
#   -e VOCAT_NFT=/usr/local/bin/host-nft
```

The GHCR image is published for `linux/amd64` and `linux/arm64`.

> [!TIP]
> **NAS / QNAP Container Station Deployment Note**:
> On NAS operating systems like QNAP QTS / QuTS hero (Container Station), custom non-root administrator accounts and volume isolation mechanisms may cause Docker named volumes (e.g. `-v vocat-data:/opt/vocat/data`) to resolve to different isolated paths between the one-off `bootstrap-admin` initialization and the daemon service container, leading to "Incorrect password" errors during Web login.
> For NAS environments, it is strongly recommended to replace named volumes with a host absolute path bind mount (e.g. `-v /share/Container/vocat/data:/opt/vocat/data` on QNAP) for both initialization and runtime to guarantee consistent SQLite database persistence.

### Docker Compose

This branch ships a `docker-compose.yml` that builds from the checkout rather
than pulling a published image, which is what you want while running code that
is not in a release yet.

From a checkout of this branch:

```bash
./scripts/docker-build.sh
```

Then open `http://<server-address>:7676` and sign in. On a fresh database this
branch auto-provisions `admin` / `admin123` on first start and logs a warning —
change it immediately from Settings on anything reachable beyond your own
machine. To choose the password up front, initialise the database before the
first start (it is read from stdin, never from argv or the environment):

```bash
read -rsp "Admin password: " P; echo
printf '%s\n' "$P" | docker compose run --rm -T \
  --entrypoint /opt/vocat/bin/vocat vocat bootstrap-admin
unset P
```

Use `scripts/docker-build.sh` rather than `docker compose up -d --build` by
hand. The script derives `VOCAT_TAG` and `VOCAT_BUILD_TIME` from the current
git state, and those become both the image tag and the version compiled into
the binary. Building by hand tags the image `:latest` over your previous build
and reports `0.1.0-dev` in **Settings → System Info**.

The compose file differs from the `docker run` flow above in two ways worth
knowing: it listens on **7676** rather than 7575 (override with `VOCAT_PORT`),
and it bind-mounts `./carrier-profiles.d` over the profile directory inside the
data volume, so carrier overrides can be edited with a normal editor and
tracked in git.

| Path | Kind | Holds |
| --- | --- | --- |
| `vocat-data` | named volume | SQLite database, plugins, TLS material — everything persistent |
| `./carrier-profiles.d` | bind mount | Carrier profile overrides, loaded once at startup |

Two sharp edges on that bind mount are documented in the compose file itself:
it shadows any `carrier-profiles.d` already inside the named volume, and a
malformed `.json` aborts startup rather than being skipped — which, with
`restart: unless-stopped`, becomes a restart loop. Validate before restarting:

```bash
python3 -m json.tool < carrier-profiles.d/your-file.json
```

In-container binary self-update is deliberately disabled (`VOCAT_CONTAINER=docker`
makes the server return `409` on the apply endpoint), because a replaced binary
would not survive the next recreate. Update by rebuilding:

```bash
git pull && ./scripts/docker-build.sh
```

`docker compose down` stops and removes the containers but keeps named volumes,
so the database survives. `docker compose down -v` deletes them, database
included.

### SIP trunk and Asterisk

VoCat can expose its SIM-backed calling as an ordinary SIP trunk, so a PBX
handles registration, softphones and dial plans while VoCat stays a gateway
between SIP and a modem's IMS registration. A softphone such as Linphone then
places calls over the SIM.

It is **off unless an address is configured**, and it authorises peers by
source address rather than credentials — so bind it to loopback, a container
network or a WireGuard address, never an interface reachable from the
internet.

A ready-made Asterisk container is included:

```bash
cp -n .env.example .env

# setenv replaces a key if present (commented or not) and appends it if not.
# A plain `sed s/...` silently does nothing when the key is missing, which is
# what an .env created from an older .env.example looks like.
setenv() { sed -i "/^#*$1=/d" .env; printf '%s=%s\n' "$1" "$2" >> .env; }

setenv ASTERISK_SIP_PASSWORD "$(openssl rand -base64 24)"
setenv COMPOSE_FILE docker-compose.yml:docker-compose.asterisk.yml
./scripts/docker-build.sh
```

`COMPOSE_FILE` is what makes every later `docker compose` command in that
directory load both files; without it, Compose sees no `asterisk` service.
`.env` is gitignored, so this is a one-time setup per machine.

Both containers run on host networking. That is forced rather than chosen:
VoCat uses `network_mode: host` for the export-proxy's `SO_BINDTODEVICE`, so
its trunk on `127.0.0.1:5062` is the *host's* loopback and a bridged container
cannot reach it at all.

Outbound is verified end to end: a softphone through Asterisk, out over a SIM's
IMS registration to the PSTN, with two-way audio. Inbound works in both places
at once: a call arriving on a SIM is offered to the PBX as an INVITE *and*
raised on VoCat's own Calls page, and whichever side answers first takes it.
Set `VOCAT_SIP_TRUNK_PBX` to empty to keep inbound calls off the PBX
entirely. The one thing that cannot be shared is a single call's audio, so
connecting browser audio to a call the trunk is carrying returns `409`.

Inbound routing is a mode on the Asterisk page: ring the extension named after
the dialled number, ring every extension at once, or ring a list in order. The
extension editor can create one account per SIM straight from the numbers
VoCat already learned, each with its own generated password.

Every call is recorded — browser, trunk or automatic task alike — with its
direction, numbers, SIM, timings, duration and outcome. History is on the
Calls page, filterable by number and outcome, and pruned after 90 days.

Keypad digits work in both places: the Calls page has a keypad that composes a
number before a call and sends tones during one, and a digit pressed on a
softphone crosses the trunk to the SIM. They
travel as RFC 4733 telephone events rather than tones, because the speech
codecs on these legs do not carry tones reliably.

With `ASTERISK_AMI_SECRET` set, VoCat's **Asterisk** page manages the PBX
itself: live endpoint and registration status, the outbound routes (which
numbers leave through which SIMs), and the softphone accounts. Each editor has
an Apply button that reloads the relevant Asterisk module, so a change does
not need a container restart. Extension passwords are write-only — set here,
never shown again — and extensions can dial each other, because the internal
dialplan is generated from the same account list.

`ASTERISK_SIP_USER` and `ASTERISK_SIP_PASSWORD` seed the first account once,
on first start; after that the web UI owns it.

**[docs/sip-trunk.md](docs/sip-trunk.md) is the manual for all of this** — how
the pieces fit together, every setting and what breaks when each is wrong,
both call directions, and troubleshooting. Read it front to back if you are
new to this part. The HTTP endpoints behind the pages are in
[docs/API.md](docs/API.md).

### USB SIM readers

USB SIM readers use the Linux PC/SC service. The one-click installer installs
and starts `pcscd` plus the CCID driver automatically on supported package
managers. On Debian/Ubuntu, the equivalent manual setup is
`apt install pcscd libccid`. If USB sees a CCID reader but PC/SC is unavailable,
VoCat keeps the reader visible in the add-device dialog and reports the missing
service or driver instead of silently hiding it.

### QMI command-line utilities

VoCat uses `qmicli` to verify that a QMI control channel is ready and
`qmi-proxy` to multiplex access to it. Packet-data sessions are managed by the
built-in QMI WDS client instead of `qmi-network` CID/PDH state files. The
one-click installer installs and verifies the corresponding utilities. For manual deployment,
Debian/Ubuntu uses `apt install libqmi-utils`; Arch Linux uses
`pacman -S libqmi`, Alpine uses `apk add qmi-utils`, and OpenWrt uses
`opkg install qmi-utils`.

`vocat doctor --repair-dji-qmi` checks for `qmicli` before changing any USB
driver binding or asserting DTR. If the utility is unavailable, the command
stops with an installation hint and leaves the current device state untouched.

### AT cellular host data

AT-backed ECM/RNDIS data requires Linux, iproute2 and BusyBox with `udhcpc`.
VoCat must exclusively manage the modem network interface; disable competing
DHCP clients or network-manager profiles for that interface. VoCat maintains
an IPv4 lease and marked policy routes without changing the system default
route or resolver. This path currently requires IPv4-capable service.

## Configuration

Vocat reads an optional JSON configuration file from `VOCAT_CONFIG`, then applies `VOCAT_*` environment variables. Environment variables take precedence.

| Environment variable | Default | Description |
| --- | --- | --- |
| `VOCAT_CONFIG` | empty | Path to an optional strict JSON configuration file, read before the variables below. |
| `VOCAT_ADDR` | `0.0.0.0:7575` | HTTP listen address. The bundled compose file sets `0.0.0.0:7676`. |
| `VOCAT_DATABASE_PATH` | `./data/vocat.db` | SQLite database path. |
| `VOCAT_SESSION_TTL` | `24h` | Authentication session lifetime. |
| `VOCAT_SECURE_COOKIES` | `false` | Optional force-Secure override for all entries; HTTP/HTTPS cookies otherwise adapt automatically. |
| `VOCAT_SHUTDOWN_TIMEOUT` | `10s` | Graceful shutdown timeout. |
| `VOCAT_MAX_REQUEST_BODY_BYTES` | `1048576` | Maximum API request body size. |
| `VOCAT_SIP_TRUNK_ADDR` | empty | UDP address for the SIP trunk. Empty keeps the trunk off entirely. |
| `VOCAT_SIP_TRUNK_PEERS` | empty | Addresses or CIDR prefixes allowed to use the trunk, comma or space separated. Startup fails if an address is set with no peers. |
| `VOCAT_SIP_TRUNK_PBX` | empty | Where a call arriving on a SIM is offered, normally `127.0.0.1:5060`. Empty leaves the inbound direction off and such calls are answered only from VoCat's Calls page. |
| `VOCAT_ASTERISK_AMI_ADDR` | empty | Asterisk manager interface to read live PBX status from, normally `127.0.0.1:5038`. Empty disables the Asterisk page. |
| `VOCAT_ASTERISK_AMI_USER` | empty | Manager account name. |
| `VOCAT_ASTERISK_AMI_SECRET` | empty | Manager account secret. Bind AMI to loopback: it crosses a plain connection in the clear. |
| `VOCAT_ASTERISK_DIALPLAN_DIR` | empty | Directory both containers share, where VoCat writes the generated `routes.conf`, `endpoints.conf` and `internal.conf`. The Asterisk compose overlay sets it; with it empty, the editors still save to VoCat's database but write nothing for Asterisk to read. |
| `VOCAT_MCP_ENABLED` | `true` | Serve the MCP endpoint at `/mcp` for AI agents. Set `false` to disable it. |
| `VOCAT_PROBE_UNKNOWN_USB` | `false` | Opt in to probing unrecognized USB serial devices with AT commands. Probing changes serial settings and clears input; enable only when those ports are dedicated to modems. Verified modem identities are discovered automatically. JSON key: `probe_unknown_usb`. |
| `VOCAT_REPO` | `MengMengCode/VoCat` | Trusted GitHub repository used by the self-updater, in `owner/name` form. |
| `GITHUB_TOKEN` | empty | Optional GitHub token for private repositories or higher API limits. |

The JSON file uses snake_case keys for the same settings, for example:

```json
{
  "address": "0.0.0.0:7676",
  "sip_trunk_address": "127.0.0.1:5062",
  "sip_trunk_peers": ["127.0.0.1"]
}
```

User-supplied Apple carrier bundles can be converted into reviewable,
allow-listed carrier profiles with `vocat carrier import-ipcc`; see
[docs/CARRIER_IPCC_IMPORT.md](docs/CARRIER_IPCC_IMPORT.md).

Administrator credentials are stored only in SQLite. Initialize an empty
database once with `vocat bootstrap-admin`; environment variables and JSON
configuration cannot set or overwrite the administrator username or password.

### API tokens

Scripts and AI clients authenticate with a bearer token instead of the
browser session. Create, list and revoke them in Settings → API tokens, or
with `vocat token` as described in [API tokens and MCP](#api-tokens-and-mcp)
below. `vocat api-token` is kept as an alias of `vocat token`.

Symfive tokens start with `sym_`. Tokens issued by earlier builds of this fork
stay valid exactly as printed, because a token is matched by the hash of the
whole string: the prefix-less 43-character form and the original `vocat_at_`
form. The `sym_at_` form is recognised as well.

Upgrading to schema 32 converts existing tokens to the new table. They keep
full access and their expiry. Tokens are now revoked by name, and names must
be 1-64 letters, digits, dots, underscores or hyphens; a name that breaks
those rules or repeats an earlier one is renamed `legacy-<id>` (check with
`vocat token list`). Changing the administrator password revokes every token.

In Docker, run the command inside the running container so it reads the same
database as the service:

```bash
docker exec -it vocat vocat token create my-script
```

Do not store Telegram tokens, SMTP passwords, webhook secrets, SIM credentials, or other private data in the repository. Configure them through the application settings or protected environment files.

## Single sign-on (SSO)

VoCat supports Pocket ID through OpenID Connect, GitHub.com OAuth Apps, and
Generic OAuth2 providers. Select one provider in **Settings → Single sign-on
(SSO)**. SSO starts disabled and password login remains available. Sign in with
the local administrator first, save a provider, and explicitly bind an external
account before enabling SSO. Other external accounts cannot register or become
administrators, including accounts sharing the bound account's email address.

### Pocket ID

1. Use an HTTPS origin for VoCat, such as `https://vocat.example.com`, and set
   the HTTPS entry as the public origin. Cookie security is selected automatically for each browser entry; `VOCAT_SECURE_COOKIES=true` is only an optional global force-Secure override. In the SSO settings choose **Pocket ID**. The
   public origin initially uses the current browser address; edit the visible
   **VoCat public origin** field if necessary. The compact callback URL and Copy
   button appear immediately, so the URL can be copied
   before creating the OIDC client or saving any credentials:
   `https://vocat.example.com/api/auth/sso/callback`.
2. In Pocket ID create a separate **Confidential** OIDC client for VoCat. Register
   the exact callback URL and verify that the intended user or group is allowed
   to use this client. VoCat and Komari need separate clients and local sessions.
3. In VoCat enter Pocket ID's exact public `APP_URL` as the Issuer, and enter the
   Client ID and Client Secret issued for the client created in step 2. The
   Client ID field includes a reminder of where to find this value.
   Scopes and PKCE S256 are handled by VoCat.
   Token authentication is selected from validated Discovery metadata, preferring
   `client_secret_basic` over `client_secret_post`. Save while SSO remains disabled.
   Origin edits update the displayed callback immediately; they take effect for
   authorization after saving.
4. Check the saved configuration; success displays **Configuration check passed**.
   Then bind the account. Complete Pocket ID's Passkey login and consent, and
   confirm the returned binding in VoCat.
   Configuration checking validates metadata and connectivity; it cannot verify
   a client secret or user authorization without this login.
5. Enable SSO and save. Use a private browser window to verify SSO login, return
   to the requested page, and the first state-changing request.

The settings page shows the bound account and an unbind action. To change the
external account, unbind it first, then bind the intended account and enable SSO
again.

The Issuer is matched exactly, including its path and trailing slash. Pocket ID
login uses the verified ID Token's `issuer + sub`; a UserInfo endpoint or email
claim is not required. Discovery and its authorization, Token and JWKS endpoints
must use the configured Issuer's origin. Deployments using `INTERNAL_APP_URL`
with a different backend origin are not supported in this release. Keep the
public `APP_URL` as the Issuer. Existing explicitly
configured authentication methods and scopes are preserved when saving.

### GitHub and Generic OAuth2

For GitHub, create an OAuth App on GitHub.com with the same exact callback URL,
select **GitHub**, enter its Client ID and Secret, and follow the save, check,
bind, enable sequence above. Endpoints are fixed to GitHub.com; the default
scope is `read:user`, and PKCE S256 is required. The binding follows the stable
GitHub numeric account ID, so changing a login name does not change the account.
GitHub Enterprise is outside this release's scope.

For Generic OAuth2, supply the authorization, Token, and UserInfo URLs in addition
to the client credentials. Scopes, the top-level user ID field (default `id`),
Token authentication and PKCE compatibility options are displayed when Generic
OAuth2 is selected.
UserInfo is requested with a Bearer access token. The ID must be a
nonempty string or an integer; large integer IDs retain their exact value.
The default Token authentication is `client_secret_post` and PKCE is S256.
Disable PKCE only for a provider that lacks PKCE support.
Generic OAuth2 does not perform OIDC ID Token verification.

### Multiple browser entries for SSO

One saved provider and account binding can serve the configured HTTPS public origin and explicit local/private IP entries over HTTP or HTTPS. Register each complete callback URL (`/api/auth/sso/callback`) with the identity provider. Authorization and token exchange use the same callback frozen at flow creation; callbacks on a different entry are rejected. HTTP is accepted only for localhost, loopback and literal private IP addresses, not arbitrary public HTTP hosts.

IPv6 callback hosts use the browser's compressed spelling, including hexadecimal IPv4-mapped addresses. Register the callback shown in settings with the identity provider. Existing configurations using an expanded IPv6 spelling are recognized automatically, but the provider must accept the normalized callback URL.

Direct TLS, the configured HTTPS public origin behind a proxy, and trusted `X-Forwarded-Proto` select Secure cookies automatically. Untrusted forwarded headers are ignored. Proxies must preserve Host. Password sessions use their request transport, the explicit override, and same-authority HTTPS Origin/Referer hints that can only upgrade Cookie protection. These browser hints do not authorize requests, change network access policy, or select callback destinations.

A session persists its Secure-cookie requirement at sign-in and can only upgrade it on later HTTPS requests, including when a missing or stale CSRF cookie must be replaced without scheme hints. Existing sessions keep working after the additive database migration; their previous transport cannot be inferred, so HTTPS users with an existing session should sign in again or make a refresh with a recognized HTTPS signal.

Saving or disabling SSO does not change password-client Cookie behavior. A proxy that removes all browser and transport scheme information must explicitly provide a trusted HTTPS signal. The configured-origin fallback is limited to SSO sign-in and SSO-session CSRF refreshes. Separate LAN HTTP entries remain usable without a process restart or configuration switch; remove a legacy force-Secure override when using both protocols. HTTP credentials travel without transport encryption.

### Network, credentials and sessions

VoCat's existing source-network policy also applies to SSO routes. Configuring
SSO does not make an installation publicly accessible. The browser must reach
VoCat and the authorization endpoint; the VoCat process must reach Discovery,
Token and JWKS/UserInfo endpoints with trusted TLS certificates. Saving the exact
Pocket ID HTTPS Issuer or Generic endpoint URLs approves
those addresses for HTTPS connections, including RFC1918/IPv6 ULA private
addresses. GitHub's fixed endpoints remain restricted to public addresses.

For a Generic provider's internal HTTP service, explicitly enter its full HTTP URL
in the Token/UserInfo field.
Every DNS candidate and actual connection for these HTTP destinations must be a
permitted private address. Pocket ID's Discovery cannot approve another origin;
its endpoints must use the configured Issuer's origin. The Issuer, Discovery fetch
URL and browser authorization endpoint always require HTTPS. Loopback, link-local,
cloud metadata and other special-use destinations remain blocked for IdP
connections. Install private CA certificates in the host/container trust store;
there is no TLS verification bypass option. The browser's localhost or HTTPS
origin for VoCat is independent of these IdP connection permissions.

The supplied Compose deployment uses host networking. Verify DNS, routing and CA
trust from the actual container; do not assume bridge-network service names are
available. Reverse proxies must preserve the configured public origin. VoCat
does not derive callback URLs from forwarded host headers. Root-path deployment
is required. For local Vite development, use one consistent loopback origin,
such as `http://127.0.0.1:5173`, for the public origin and callback registration;
IdP transport exceptions belong only in test fixtures.

Session and CSRF cookies retain `SameSite=Strict`; short-lived authorization
cookies use `SameSite=Lax` for top-level GET callbacks. Saving any SSO
configuration, including disabling it or rotating the secret, revokes all SSO
sessions. Unbinding an account revokes that binding's SSO sessions.
Password sessions and API tokens survive those actions. Changing the local
administrator's password revokes all sessions and API tokens, cancels in-flight
authorizations, and preserves the external binding. The UI requests sign-in
again when a successful operation revokes the current SSO session.

Leave the secret input empty to preserve an existing secret, or enter a new
secret to replace it. The settings page does not offer a secret-clearing control.
Changing the provider, client
ID, Issuer, or Generic identity endpoints/ID field requires a new identity
namespace, new credentials, and a new binding. Existing bindings require explicit
reset confirmation. To change the bound account, unbind it and then create a new
binding. Client secrets are held only on the server in the protected SQLite
database; the sensitive-setting flag redacts responses and does not encrypt the
database. Third-party tokens are not persisted or returned to the browser.

Signing out ends only the current VoCat session. Disabling an IdP account or
signing out of Pocket ID does not immediately revoke an existing VoCat session;
it expires according to `VOCAT_SESSION_TTL`, or can be revoked by disabling SSO
or unbinding in VoCat. The public capabilities endpoint is
`GET /api/auth/providers`. SSO settings and binding APIs require a browser
session and CSRF for writes; Bearer tokens cannot manage them. Configuration
revision conflicts return `409 sso_config_changed`; changed identity sources
return `409 sso_rebind_required`.

### Upgrade and rollback

SSO adds SQLite schema version 29. Upgrades preserve the existing administrator,
sessions, API tokens and device data; SSO remains disabled until configured.
Before upgrading, record the old binary or image digest and back up configuration,
CA material and the complete persistent data directory while VoCat is stopped,
or use SQLite's backup API/`.backup` to obtain a consistent live snapshot. Copying
only a running WAL-mode database file is not a reliable backup.

Disable SSO in the new version to recover from provider problems while keeping
password login and current business data. Returning to a schema-28 binary
requires stopping VoCat and restoring the matching pre-upgrade data and
configuration snapshot. Merely reverting the container image or lowering
`PRAGMA user_version` cannot downgrade the database. Restore into a separate
directory/volume where possible and retain the newer database for investigation.
Restoring a snapshot loses messages and task changes recorded after that backup.
For Docker, update by pulling the image and recreating the container.

Before a production release, complete the real Pocket ID Passkey/access-policy
test and an upgrade/rollback rehearsal for that deployment.

## API tokens and MCP

Non-browser clients authenticate against the same `/api/...` endpoints with
a bearer token instead of the browser's session cookie and CSRF flow:

```bash
vocat token create --read-only --ttl 720h ci-runner   # prints the token once
curl -H "Authorization: Bearer sym_..." http://127.0.0.1:7575/api/devices
```

`vocat token list` and `vocat token revoke <name>` manage the inventory.
Tokens act with administrator privileges; a `--read-only` token may only
issue `GET` requests (one documented exception: reading an SMS thread marks
it read locally — the rule is "no hardware or network mutations"). Rotating
the administrator password revokes every token. Create tokens after the
first `bootstrap-admin`. Remote bearer clients also need a network access
policy that admits their source address; the default `internal` mode only
serves private networks.

By default (`VOCAT_MCP_ENABLED=false` to disable), the same service
exposes an MCP (Model Context Protocol) endpoint for AI agents at `http://<host>:7575/mcp` using
the Streamable HTTP transport and bearer-token authentication. Tools cover
device status, SMS threads and guarded sending, eSIM profile listing and
switching, and WiFi Calling status/reconnect; sensitive operations (raw AT,
eSIM download/delete, SIM PIN, network selection, system update) are not
exposed, and tool output never includes IMSI, SIM PIN, or activation codes.
A read-only token receives a tool list without the write tools. SMS sending
follows the same blocked-destination policy and global hourly limit as the
web UI and sends over the cellular AT path (the web UI's IMS-preferred
routing when WiFi Calling is enabled stays web-side, and messages sent
through MCP are not yet written to the web conversation history).

Client configuration (Claude Code as the example):

```json
{
  "mcpServers": {
    "vocat": {
      "type": "http",
      "url": "http://192.168.1.10:7575/mcp",
      "headers": { "Authorization": "Bearer sym_..." }
    }
  }
}
```

See [docs/API_MCP_DEVELOPMENT_PLAN.md](docs/API_MCP_DEVELOPMENT_PLAN.md) for
the design record.

## Telegram bot

When Telegram notifications are enabled and both Chat ID and Admin ID are configured, the bot supports:

```text
/status [device]
/esim <device>
/switch <device> <iccid>
/wfc <device> <status|on|off|reconnect>
/sms <device> <number> <message>
/ussd <device> <code>
/ussd_reply <session> <input>
/ussd_cancel <session>
```

Profile switching and SMS submission use one-time confirmation buttons. The bot does not expose eSIM download, delete, or rename commands.

The web terminal and Telegram share USSD routing. With WiFi Calling enabled, requests use IMS/USSI and return a readiness error until IMS registers, without falling back to cellular AT+CUSD. With WiFi Calling disabled, requests use cellular USSD. Menu replies and cancellation stay bound to the original device and dialog. Cancellation errors are reported and retain the session for retry; an uncertain submission is never automatically resubmitted over another channel.

USSI dialog handling follows [3GPP TS 24.390](https://www.etsi.org/deliver/etsi_ts/124300_124399/124390/18.00.00_60/ts_124390v180000p.pdf). A failed BYE retains a cancellation record, but further menu input is blocked once cancellation has started ([RFC 3261, section 15.1.1](https://www.rfc-editor.org/rfc/rfc3261.html#section-15.1.1)).

## Updating

Check for a newer GitHub Release:

```bash
vocat update --check --repo MengMengCode/VoCat
```

Install the latest release:

```bash
sudo vocat update --repo MengMengCode/VoCat
```

The updater downloads the binary matching the current Linux architecture, verifies it with the published `SHA256SUMS`, replaces the executable atomically, and restarts the `vocat` systemd service when available.

For Docker installations:

```bash
docker pull ghcr.io/mengmengcode/vocat:latest
```

Recreate the container after pulling the new image.

For the Docker Compose deployment, which builds from the checkout rather than
pulling, rebuild instead:

```bash
git pull && ./scripts/docker-build.sh
```

## Development

Requirements:

- Go 1.25 or newer
- Node.js 20 or newer
- npm

Run the frontend development server:

```bash
cd web
npm install
npm run dev
```

Build the embedded frontend and start the backend:

```bash
cd web
npm run build
cd ..
go run ./cmd/vocat
```

Run all tests:

```bash
go test ./...
```

Build a production binary:

```bash
go build -trimpath -ldflags "-s -w" -o vocat ./cmd/vocat
```

## Release automation

Pushing a version tag starts two GitHub Actions workflows:

- `release-binaries` builds and publishes `amd64`, `386`, `arm64`, `aarch64`, and `armv7` binaries plus `SHA256SUMS`.
- `docker` builds and publishes a multi-architecture image to GitHub Container Registry.

```bash
git tag v0.2.0
git push origin v0.2.0
```

## Project layout

```text
cmd/vocat/                  Application entry point and CLI
internal/device/            Modem discovery and device control
internal/modem/             AT session and response handling
internal/server/            HTTP API, notifications, and embedded web server
internal/store/             SQLite persistence
internal/siptrunk/          SIP trunk toward a PBX, and its RTP bridge
internal/ami/               Asterisk Manager Interface client
internal/asteriskconf/      Generates Asterisk dialplan and PJSIP config
internal/dtmf/              RFC 4733 telephone events, shared by both RTP paths
internal/g711/              A-law and mu-law, shared by both RTP paths
internal/update/            GitHub Release self-updater
internal/vowifi/            IKE, EAP-AKA, IMS, and WiFi Calling runtime
asterisk/                   Asterisk container image and config templates
scripts/install.sh          Linux installer and updater
scripts/docker-build.sh     Compose build with a git-derived version
web/src/                    React and TypeScript frontend
.github/workflows/          Binary and Docker release automation
```

## Responsible use

Cellular modem and eSIM operations can affect subscriber service, stored profiles, network registration, and hardware state. Keep backups, review destructive actions carefully, and use the software only in lawful environments where you are permitted to operate the connected hardware and network resources.

Vocat does not bypass carrier authentication, network policy, hardware security, or eSIM trust requirements. Support for an operation means that Vocat can request it from the modem or eUICC; the device, profile, network, or carrier may still reject it.

## Contributing

Issues and pull requests are welcome. Keep changes focused, include tests where practical, avoid committing credentials or subscriber data, and document hardware-specific behavior clearly.

Before submitting a change:

```bash
go test ./...
cd web && npm run build
```

## Thanks
- [Nodeseek.com](https://www.nodeseek.com) — A community dedicated to servers
- [Linux.do](https://linux.do) — An inspiring tech community
- [iniwex5](https://github.com/iniwex5) — Style and Functionality Guidelines

## Buy me a coffee

| Network | Address |
| ------- | ------- |
| USDT-TRON (TRC20) | `TWSAkvzVsFc7KqncDLmUfRxpPQbpV5CgTB` |
| USDT-BSC (BEP20) | `0xb43031387342ebb1ff536fb9ad6440b9e6377139` |
| USDT-Polygon | `0xb43031387342ebb1ff536fb9ad6440b9e6377139` |

## License

See [LICENSE](LICENSE).

## Star History

<a href="https://www.star-history.com/?repos=mengmengcode%2Fvocat&type=date&legend=top-left">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/chart?repos=mengmengcode/vocat&type=date&theme=dark&legend=top-left" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/chart?repos=mengmengcode/vocat&type=date&legend=top-left" />
   <img alt="Star History Chart" src="https://api.star-history.com/chart?repos=mengmengcode/vocat&type=date&legend=top-left" />
 </picture>
</a>
