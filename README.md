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

Vocat is an open-source web control panel and engineering toolkit for Quectel EC20/EC25-class cellular modems. It combines modem discovery, live radio status, AT and USSD terminals, SMS, WiFi Calling, eSIM management, network selection, proxy routing, notifications, audit logs, and release automation in one self-contained service.

The backend is written in Go, the interface is built with React and TypeScript, and the production frontend is embedded into the Go binary. A single executable contains the web application and uses SQLite for persistent state.

<p align="center">
  <img src="img\image.png">
  <img src="img\image-1.png">
</p>

## Features

| Area | What Vocat provides |
| --- | --- |
| Device management | Automatic serial/USB discovery, multiple modem support, friendly device names, live overview updates, module restart, flight mode, and USB networking mode controls. |
| Radio and network | Registration status, operator, signal metrics, RSRP/RSRQ/SINR, network mode, band, channel, operator scanning, and automatic or manual network selection. |
| AT and USSD | Interactive AT terminal, command history, raw modem responses, USSD start/continue/cancel flows, and clear modem error reporting. |
| SMS | Direct cellular and IMS SMS transmission, inbound synchronization, multipart handling, delivery reports, conversation history, unread state, timestamps, and per-message delivery status. |
| WiFi Calling | IKEv2/ePDG tunnel setup, EAP-AKA authentication, IMS registration, IMS SMS, IMS voice calls with browser audio, reconnect controls, status diagnostics, and per-device routing. |
| SIP trunk | Outbound calls from a PBX over a SIM's IMS registration, G.711 media bridging, source-address peer authorisation, and a ready-made Asterisk container. |
| eSIM and eUICC | eUICC discovery, EID and production information, certificate metadata, multi-eUICC inventory, installed profile listing, enable/disable/switch operations, download, rename, and delete operations when supported by the card. |
| Card policy | ICCID-based WiFi Calling and flight-mode behavior with immediate policy application. |
| Proxy routing | Upstream SOCKS routing, device bindings, country rules, TCP reachability checks, and UDP Associate checks for WiFi Calling data paths. |
| Notifications | New inbound SMS forwarding through Telegram, Bark, email, Pushplus, and signed webhooks. Each SMS is delivered as an individual notification. |
| Telegram bot | Device status, installed-profile listing and switching, WiFi Calling controls, and SMS sending. Sensitive actions require administrator confirmation. |
| Operations | Authentication, CSRF protection, access policies, audit events, live logs, log retention, health checks, responsive layout, dark mode, and English/Chinese application UI. |
| Distribution | Static Linux binaries, systemd installation script, self-update with SHA-256 verification, Docker image, GHCR publishing, and GitHub Actions release builds. |

## Supported hardware

Vocat targets Qualcomm-based Quectel modules that expose compatible AT, QMI, serial, and USB networking interfaces, including:

- Quectel EC20
- Quectel EC25
- Quectel EG25 family
- Compatible EG600 and related modules

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
identifies supported Quectel USB modems (USB vendor ID `2c7c`) and PCIe/MHI
modems exposed through the Linux WWAN subsystem; it does not identify arbitrary
modem layouts. Mapping only individual nodes with `--device`, such as
`/dev/ttyUSB2`, `/dev/cdc-wdm0`, or `/dev/wwan0qmi0`, limits the container to
those fixed nodes and does not provide complete multi-device or hot-plug discovery.

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
# Edit in place. Appending would leave two ASTERISK_SIP_PASSWORD lines, and
# which one Compose uses is not visible from the file.
sed -i "s|^ASTERISK_SIP_PASSWORD=.*|ASTERISK_SIP_PASSWORD=$(openssl rand -base64 24)|" .env
sed -i "s|^#*COMPOSE_FILE=.*|COMPOSE_FILE=docker-compose.yml:docker-compose.asterisk.yml|" .env
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
IMS registration to the PSTN, with two-way audio. Inbound (a call arriving on
the SIM being offered to the PBX) is not implemented yet; those are still
answered from VoCat's own Calls page. The Calls page and the trunk work at the same time,
with one exception: a single call's audio cannot be bridged to both, so
connecting browser audio to a call the trunk is carrying returns `409`.

Full setup, dial plan, multi-SIM selection and the status table the PBX sees:
[docs/sip-trunk.md](docs/sip-trunk.md).

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

## Configuration

Vocat reads an optional JSON configuration file from `VOCAT_CONFIG`, then applies `VOCAT_*` environment variables. Environment variables take precedence.

| Environment variable | Default | Description |
| --- | --- | --- |
| `VOCAT_CONFIG` | empty | Path to an optional strict JSON configuration file, read before the variables below. |
| `VOCAT_ADDR` | `0.0.0.0:7575` | HTTP listen address. The bundled compose file sets `0.0.0.0:7676`. |
| `VOCAT_DATABASE_PATH` | `./data/vocat.db` | SQLite database path. |
| `VOCAT_SESSION_TTL` | `24h` | Authentication session lifetime. |
| `VOCAT_SECURE_COOKIES` | `false` | Marks session cookies as secure when HTTPS is used. |
| `VOCAT_SHUTDOWN_TIMEOUT` | `10s` | Graceful shutdown timeout. |
| `VOCAT_MAX_REQUEST_BODY_BYTES` | `1048576` | Maximum API request body size. |
| `VOCAT_SIP_TRUNK_ADDR` | empty | UDP address for the SIP trunk. Empty keeps the trunk off entirely. |
| `VOCAT_SIP_TRUNK_PEERS` | empty | Addresses or CIDR prefixes allowed to use the trunk, comma or space separated. Startup fails if an address is set with no peers. |
| `VOCAT_ASTERISK_AMI_ADDR` | empty | Asterisk manager interface to read live PBX status from, normally `127.0.0.1:5038`. Empty disables the Asterisk page. |
| `VOCAT_ASTERISK_AMI_USER` | empty | Manager account name. |
| `VOCAT_ASTERISK_AMI_SECRET` | empty | Manager account secret. Bind AMI to loopback: it crosses a plain connection in the clear. |
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

For scripted or automated access that should not go through the browser
login flow, issue a long-lived API token instead of using the admin session
cookie:

```bash
vocat api-token create --name "my script" --ttl 720h   # 720h = 30 days (default)
vocat api-token list
vocat api-token revoke <id>
```

The raw token is printed once at creation time; only its hash is stored.
Send it as a bearer credential instead of logging in through the UI:

```bash
curl -H "Authorization: Bearer <token>" http://<server-address>:7575/api/devices
```

API tokens authenticate as the single administrator and need no CSRF token
or cookie, unlike browser sessions. In Docker, run the command inside the
running container so it reads the same database as the service:

```bash
docker exec -it vocat vocat api-token create --name "my script"
```

Do not store Telegram tokens, SMTP passwords, webhook secrets, SIM credentials, or other private data in the repository. Configure them through the application settings or protected environment files.

## Telegram bot

When Telegram notifications are enabled and both Chat ID and Admin ID are configured, the bot supports:

```text
/status [device]
/esim <device>
/switch <device> <iccid>
/wfc <device> <status|on|off|reconnect>
/sms <device> <number> <message>
```

Profile switching and SMS submission use one-time confirmation buttons. The bot does not expose eSIM download, delete, or rename commands.

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
