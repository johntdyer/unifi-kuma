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

Using a UniFi API key (no UniFi username/password needed — see [Authentication](#authentication)) and Kuma username/password:

```bash
docker run -d \
  --name unifi-kuma \
  --restart unless-stopped \
  -e UNIFI_URL=https://192.168.1.1 \
  -e UNIFI_API_KEY=changeme \
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
  -e UNIFI_API_KEY=changeme \
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
      UNIFI_API_KEY: changeme         # preferred — omit UNIFI_USERNAME/PASSWORD if set
      # UNIFI_USERNAME: admin         # only needed if not using UNIFI_API_KEY
      # UNIFI_PASSWORD: changeme      # only needed if not using UNIFI_API_KEY
      UNIFI_INSECURE: "true"          # if using self-signed cert
      KUMA_URL: http://uptime-kuma:3001
      KUMA_USERNAME: admin            # Kuma has no API-key auth for this — see Authentication
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

UniFi and Uptime Kuma support different auth options, because they're different protocols under the hood — UniFi is REST (so API keys work), Kuma is Socket.IO (so only the same username+password the web UI uses works; **Kuma API keys only cover its push/badge REST endpoints, not monitor management, so they can't be used here**).

- **UniFi**: set `UNIFI_API_KEY` (Settings → Control Plane → API Keys, requires UniFi OS 3.2+) — recommended, avoids storing a password. `UNIFI_USERNAME`/`UNIFI_PASSWORD` are **not required** when an API key is set; they're only a fallback for older controllers that don't support API keys.
- **Uptime Kuma**: set `KUMA_USERNAME`/`KUMA_PASSWORD` — the same credentials you use to log into the Kuma web UI.
- **Uptime Kuma with "Disable Auth" enabled**: set `KUMA_DISABLE_AUTH=true` instead. Kuma instances with authentication disabled (Settings → Security → Disable Auth) reject any login attempt, so `KUMA_DISABLE_AUTH=true` connects without sending credentials at all. `KUMA_USERNAME`/`KUMA_PASSWORD` are not needed in this mode.

Validation requires: for UniFi, either the API key or both username and password; for Kuma, either both username and password, or `KUMA_DISABLE_AUTH=true`.

### Environment variables

| Variable | Default | Description |
|---|---|---|
| `UNIFI_URL` | *(required)* | Controller base URL |
| `UNIFI_API_KEY` | *(optional)* | UniFi API key — if set, `UNIFI_USERNAME`/`UNIFI_PASSWORD` are not needed |
| `UNIFI_USERNAME` | *(required if no API key)* | UniFi admin username |
| `UNIFI_PASSWORD` | *(required if no API key)* | UniFi admin password |
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
  api_key: changeme     # preferred — omit username/password if set
  # username: admin      # only needed if not using api_key
  # password: changeme   # only needed if not using api_key
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
