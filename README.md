# unifi-kuma

[![CI](https://github.com/johntdyer/unifi-kuma/actions/workflows/test.yml/badge.svg)](https://github.com/johntdyer/unifi-kuma/actions/workflows/test.yml)
[![codecov](https://codecov.io/gh/johntdyer/unifi-kuma/graph/badge.svg)](https://codecov.io/gh/johntdyer/unifi-kuma)
[![Go Report Card](https://goreportcard.com/badge/github.com/johntdyer/unifi-kuma)](https://goreportcard.com/report/github.com/johntdyer/unifi-kuma)
[![Go 1.25+](https://img.shields.io/badge/Go-1.25+-00ADD8?logo=go)](https://golang.org/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

Automatically create and manage [Uptime Kuma](https://github.com/louislam/uptime-kuma) monitors from [UniFi](https://ui.com/) network tags! 

Tag a device or client in the UniFi UI with `kuma-servers` and unifi-kuma will create a ping monitor for it inside a **Servers** group in Uptime Kuma — and keep everything in sync on a configurable interval.

Inspired by [autokuma](https://github.com/BigBoot/AutoKuma), but source-of-truth is your UniFi controller rather than Docker labels.

---

## Requirements

| Component | Minimum version |
|-----------|----------------|
| UniFi OS / Network Application | 3.x / 8.x (tag API required) |
| Uptime Kuma | any recent version |
| Go (to build from source) | 1.25 |

> Uptime Kuma has no REST API for managing monitors — the web UI (and this tool) talks to it over Socket.IO. unifi-kuma connects the same way, via [`github.com/breml/go-uptime-kuma-client`](https://github.com/breml/go-uptime-kuma-client).

---

## How it works

1. On startup (and every `SYNC_INTERVAL_SECONDS`), unifi-kuma fetches all tags from UniFi whose names start with `SYNC_TAG_PREFIX` (default: `kuma`).
2. For each matching tag (e.g. `kuma-servers`) it finds or creates an Uptime Kuma **monitor group** named after the tag (e.g. `Servers`).
3. For every device or client in that tag it creates a **ping monitor** inside the group — using the device's management IP.
4. Monitors created by this tool are labelled `unifi-kuma` so they can be tracked for optional orphan deletion.

### Example

```
UniFi tags
  kuma-servers     → devices: router (192.168.1.1), nas (192.168.1.10)
  kuma-iot         → devices: thermostat (10.0.1.50)

Uptime Kuma result
  📁 Servers
    ● router       (ping 192.168.1.1)
    ● nas          (ping 192.168.1.10)
  📁 Iot
    ● thermostat   (ping 10.0.1.50)
```

---

## Quick start

### Docker (recommended)

Neither UniFi nor Uptime Kuma can be authenticated with an API key for what this tool needs (see [Authentication](#authentication) for why) — both use username/password:

```bash
docker run -d \
  --name unifi-kuma \
  --restart unless-stopped \
  -e UNIFI_URL=https://192.168.1.1 \
  -e UNIFI_USERNAME=unifi-kuma \
  -e UNIFI_PASSWORD=changeme \
  -e KUMA_URL=http://uptime-kuma:3001 \
  -e KUMA_USERNAME=admin \
  -e KUMA_PASSWORD=changeme \
  ghcr.io/johntdyer/unifi-kuma:latest
```

If your Kuma instance runs with "Disable Auth" enabled, skip Kuma credentials entirely:

```bash
docker run -d \
  --name unifi-kuma \
  --restart unless-stopped \
  -e UNIFI_URL=https://192.168.1.1 \
  -e UNIFI_USERNAME=unifi-kuma \
  -e UNIFI_PASSWORD=changeme \
  -e KUMA_URL=http://uptime-kuma:3001 \
  -e KUMA_DISABLE_AUTH=true \
  ghcr.io/johntdyer/unifi-kuma:latest
```

### Docker Compose

```yaml
services:
  unifi-kuma:
    image: ghcr.io/johntdyer/unifi-kuma:latest
    restart: unless-stopped
    environment:
      UNIFI_URL: https://192.168.1.1
      UNIFI_USERNAME: unifi-kuma      # see "Creating a read-only UniFi user" below
      UNIFI_PASSWORD: changeme
      UNIFI_INSECURE: "true"          # if using self-signed cert
      KUMA_URL: http://uptime-kuma:3001
      KUMA_USERNAME: admin
      KUMA_PASSWORD: changeme
      # KUMA_DISABLE_AUTH: "true"     # instead of username/password, if Kuma has auth disabled
      SYNC_TAG_PREFIX: kuma
      SYNC_INTERVAL_SECONDS: "300"
      SYNC_DRY_RUN: "false"
      SYNC_DELETE_ORPHAN: "false"
```

### Build from source

```bash
git clone https://github.com/johntdyer/unifi-kuma.git
cd unifi-kuma
make build          # binary at ./dist/unifi-kuma
./dist/unifi-kuma -help
```

---

## Configuration

All settings can be provided as environment variables or via a YAML file. Environment variables take precedence over the file.

### Authentication

Neither UniFi nor Uptime Kuma can be authenticated with an API key for what this tool actually needs, so both use username+password:

- **UniFi**: set `UNIFI_USERNAME`/`UNIFI_PASSWORD`. UniFi API keys only work against its newer public Integrations API, which doesn't expose tags at all — the thing this tool is built around — so a real login (the same session-based auth the web UI itself uses) is the only way to read tags. See [Creating a read-only UniFi user](#creating-a-read-only-unifi-user) below for a scoped-down account instead of using your main admin login.
- **Uptime Kuma**: set `KUMA_USERNAME`/`KUMA_PASSWORD` — the same credentials you use to log into the Kuma web UI. (Kuma API keys only cover its push/badge REST endpoints, not the Socket.IO connection this tool uses for monitor management, so they can't be used here either.)
- **Uptime Kuma with "Disable Auth" enabled**: set `KUMA_DISABLE_AUTH=true` instead. Kuma instances with authentication disabled (Settings → Security → Disable Auth) reject any login attempt, so `KUMA_DISABLE_AUTH=true` connects without sending credentials at all. `KUMA_USERNAME`/`KUMA_PASSWORD` are not needed in this mode.

Validation requires both username and password for UniFi; for Kuma, either both username and password, or `KUMA_DISABLE_AUTH=true`.

### Environment variables

| Variable | Default | Description |
|---|---|---|
| `UNIFI_URL` | *(required)* | Controller base URL |
| `UNIFI_USERNAME` | *(required)* | UniFi username — see [read-only user](#creating-a-read-only-unifi-user) |
| `UNIFI_PASSWORD` | *(required)* | UniFi password |
| `UNIFI_SITE` | `default` | UniFi site name |
| `UNIFI_INSECURE` | `false` | Skip TLS certificate verification |
| `KUMA_URL` | *(required)* | Uptime Kuma base URL |
| `KUMA_USERNAME` | *(required if no no-auth)* | Kuma username |
| `KUMA_PASSWORD` | *(required if no no-auth)* | Kuma password |
| `KUMA_DISABLE_AUTH` | `false` | Connect without credentials, for instances with "Disable Auth" enabled |
| `SYNC_TAG_PREFIX` | `kuma` | Prefix of UniFi tags to watch |
| `SYNC_INTERVAL_SECONDS` | `300` | Seconds between sync cycles |
| `SYNC_DRY_RUN` | `false` | Log planned actions without applying them |
| `SYNC_DELETE_ORPHAN` | `false` | Delete monitors with no matching UniFi device |

### YAML config file

```yaml
unifi:
  url: https://192.168.1.1
  username: unifi-kuma
  password: changeme
  site: default
  insecure: true

kuma:
  url: http://uptime-kuma:3001
  username: admin
  password: changeme
  # disable_auth: true   # instead of username/password, if Kuma has auth disabled

sync:
  tag_prefix: kuma
  interval_seconds: 300
  dry_run: false
  delete_orphan: false
```

Pass with `-config /path/to/config.yaml`.

### Creating a read-only UniFi user

unifi-kuma only reads data from UniFi (tags, devices, clients) — it never changes anything on your controller — so it doesn't need a full admin account. Create a scoped-down local user instead of using your primary admin login:

1. On the **UniFi OS console** (not inside the Network app itself), go to **Settings → Admins & Users**.
2. Click **Add Admin** (or **+ Add User**) → choose to create a **local user** — look for a "restrict to local access only" option so this doesn't become a full Ubiquiti cloud (SSO) account with broader reach than your controller.
3. Give it a username and password — these become `UNIFI_USERNAME`/`UNIFI_PASSWORD`.
4. Under application permissions, set the **Network** application role to **View Only** (read-only). Set any other applications (Protect, Access, Talk, etc.) to **None** — unifi-kuma doesn't touch them.
5. Save, and use these credentials in your config.

> Exact menu wording varies a bit by UniFi OS/firmware version. If sync fails with a permissions error under the **View Only** role, the internal API this tool uses may require the fuller **Admin** role for the Network application specifically — this hasn't been verified against every controller version, so try that as a fallback and let us know which one your setup actually needs.

### CLI flags

```
-config string      path to YAML config file
-log-level string   log level: debug, info, warn, error (default "info")
-log-json           output logs as JSON
-version            print version and exit
```

---

## Tagging devices in UniFi

1. Open **UniFi Network** → select a device or client.
2. Under the **Tags** section, add a tag like `kuma-servers`.
3. unifi-kuma will pick it up on the next sync cycle.

> **Note:** The tag API requires UniFi Network Application 8.x or UniFi OS 3.x. If your controller is older, consider upgrading or opening an issue to discuss a notes-based fallback.

---

## Development

```bash
# Run all tests
make test

# Run tests with race detector + coverage
make test-cover

# Run tests in Docker (identical to CI)
make test-docker

# Start a local Uptime Kuma instance for manual testing
make dev-deps
# → Kuma available at http://localhost:3001

# Lint
make lint

# Show all targets
make help
```

---

## CI / CD

| Trigger | Action |
|---|---|
| Pull request | Run tests on Go 1.25, lint, build |
| Push / merge to `main` | Build multi-arch Docker image, push to GHCR as `latest` |
| Push a `v*` tag | Same, plus publish versioned tags (`v1.2.3`, `v1.2`, `v1`) |

Image: `ghcr.io/johntdyer/unifi-kuma`

---

## License

MIT
