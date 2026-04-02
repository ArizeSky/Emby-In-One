# Emby-In-One

> **Version: V1.3.1**

[Changelog](Update.md) | [中文文档](README.md) | [Security Policy](SECURITY.md) | [Update Plan](Update%20Plan.md) | [V1.2.1 Documentation](README_EN_V1.2.1.md) | [GitHub](https://github.com/ArizeSky/Emby-In-One)

Multi-server Emby aggregation proxy — merges media libraries from multiple upstream Emby servers into a single unified endpoint accessible by any standard Emby client. (This version is the high-performance V1.3 Go backend refactor Pre-release).

## Demo Site

[Demo Site](https://emby.cothx.eu.cc/)
Emby server address: https://emby.cothx.eu.cc/
Username: admin
Password: 5T5xF4oMxcnrcCPA

## Preview

![Preview 1](https://cdn.nodeimage.com/i/D293pIQcFNx4gXkfskPbnXFzmgCQ1JPx.webp)
![Preview 2](https://cdn.nodeimage.com/i/iDAXrYaIXdm9efhwl2BtqJjRUmGfTSKU.webp)
![Preview 3](https://cdn.nodeimage.com/i/K4jhTTMjv8rkHYiPNbXKUC0kXIzAXgq0.webp)
![Preview 4](https://cdn.nodeimage.com/i/50eO6lJBev4Q5Zb1XhPgVH78kELtR1YK.webp)

> Image hosting provided by [NodeImage](https://www.nodeimage.com). Thanks for the support.

---

## Features Overview

- **Multi-Server Aggregation** — Merge movies, series, and search results across multiple servers into a unified view. Powered by Goroutine parallel requests, aggregation latency depends only on the slowest server.
- **Smart Deduplication & Prioritization** — Identical items are automatically merged while keeping all source versions available. Uses a 4-level metadata priority logic (Tag > Chinese > Length > Order) to pick the best display information.
- **Client Passthrough** — Isolates and passes your real client identity to the upstream server per proxy token to avoid cross-device conflicts. Features an exclusive 5-level persistence chain for auto-reconnects.
- **Advanced UA Spoofing** — Safely disguise as Infuse, or use `custom` mode to independently configure all 5 Emby client identity headers for any upstream server.
- **Network Proxy Pool** — Configure dedicated HTTP/HTTPS proxies separately for each upstream server, complete with a one-click connectivity tester.
- **Dual Playback Modes** — Setup upstreams with Proxy Mode (traffic routed through aggregator, hiding upstreams, supports HLS/seeking) or Redirect Mode (HTTP 302 to upstream direct links, saving proxy bandwidth).
- **Full Control & Operations** — Ships with a modernized SSH CLI menu and Web admin panel, featuring persistent logs and SQLite-backed ID mapping.
- **Security Hardening** — Built-in anti-bruteforce (IP locking), 0600 secure permissions for configs, atomic file writes, request body size limits, and graceful shutdown.

---

## Quick Installation

> **Notice for Legacy Node.js Deployment**: If you wish to deploy the V1.2.1 stable Node.js version, please navigate to the [Releases page](https://github.com/ArizeSky/Emby-In-One/releases) of this repository, download the V1.2.1 Source code archive, extract it, and run `bash install.sh`.

For Linux servers, Release binary deployment is the primary recommendation for V1.3 (no local build required). Docker deployment is suitable when you prefer building images locally.

### Method 1: Release Binary One-Click Install (Primary Recommendation)

```bash
curl -fsSL -o release-install.sh https://raw.githubusercontent.com/ArizeSky/Emby-In-One/main/release-install.sh
sudo bash release-install.sh
```

Optional: install a specific version.

```bash
sudo bash release-install.sh V1.3.0
```

This script automatically:
- Downloads the matching Release binary for your CPU architecture (no local Go build required)
- Initializes `/opt/emby-in-one/{config,data,log}` and generates a random admin password on first install
- Fetches companion assets like `admin.html` and `emby-in-one-cli.sh`
- Installs and starts the `systemd` service (`emby-in-one`) with auto-start enabled
- Detects existing installs, performs backup, and supports rollback-safe upgrades

### Method 2: Source Repo One-Line Install (Recommended for developers/local image build)

```bash
git clone https://github.com/ArizeSky/Emby-In-One.git
cd Emby-In-One
bash install.sh
```

The script automates Docker installation, assigns a random admin password, builds the Go image, and starts the service. To manage your server later, simply type `emby-in-one` in your SSH terminal to load the CLI menu.

### Method 3: Manual Docker Compose Deployment

1. Create project directories:
```bash
mkdir -p /opt/emby-in-one/{config,data}
cd /opt/emby-in-one
```
2. Copy the core files from this repository (including `go.mod`, `cmd/`, `internal/`, `public/`, `Dockerfile`, `docker-compose.yml`, etc.) into the directory.
3. Create the initial configuration `config/config.yaml`:
```yaml
server:
  port: 8096
  name: "Emby-In-One"

admin:
  username: "admin"
  password: "your-strong-password" # Automatically hashed after first boot

playback:
  mode: "proxy"

timeouts:
  api: 30000
  global: 15000
  login: 10000
  healthCheck: 10000
  healthInterval: 60000

proxies: []
upstream: []
```
4. Build and start up:
```bash
docker compose build
docker compose up -d
```

### Method 4: Direct Go Run (For Developers)

Requirements: Go 1.26+ with a C compiler (Debian/Ubuntu: `apt install build-essential`).
```bash
mkdir -p config data
# Create config.yaml inside /config as shown in Method 3
go test ./...
go run ./cmd/emby-in-one
```

**Default Access URLs**:
- Emby Client Endpoint: `http://Your_Server_IP:8096`
- Admin Panel: `http://Your_Server_IP:8096/admin`

---

## System Requirements

**Release Binary Deployment (Recommended):**
- Linux (amd64 / arm64 / arm / mips / mipsle / riscv64)
- No Go toolchain required — runs pre-compiled binaries directly

**Docker Deployment:**
- Docker 20.10+, Docker Compose v2
- Linux: Debian 11/12/13, Ubuntu 22/24 (recommended); other distros may work but are untested
- Windows / macOS supported for development/testing

**Go Source Build:**
- Go 1.26+
- C compiler (CGO for SQLite): Debian/Ubuntu run `apt install build-essential`

---

## Configuration Reference

Config file: `config/config.yaml` (Docker mounts to `/app/config/config.yaml`).

```yaml
server:
  port: 8096
  name: "Emby-In-One"
  # id: Auto-generated on first boot — do not edit manually

admin:
  username: "admin"
  password: "your-strong-password"    # Automatically hashed after first boot

playback:
  mode: "proxy"          # "proxy" or "redirect", global default

timeouts:
  api: 30000             # Single upstream API request timeout (ms)
  global: 15000          # Aggregation total timeout — max wait for all servers (ms)
  login: 10000           # Upstream login timeout (ms)
  healthCheck: 10000     # Health check timeout (ms)
  healthInterval: 60000  # Health check interval (ms)

proxies: []
  # - id: "abc123"
  #   name: "Japan Proxy"
  #   url: "http://user:pass@ip:port"

upstream:
  - name: "Server A"
    url: "https://emby-a.example.com"
    username: "user"
    password: "pass"

  - name: "Server B"
    url: "https://emby-b.example.com"
    apiKey: "your-api-key"
    playbackMode: "redirect"                   # Override global playback mode
    spoofClient: "infuse"                      # none | passthrough | infuse | custom
    streamingUrl: "https://cdn.example.com"    # Dedicated streaming domain (optional)
    followRedirects: true                      # Follow upstream 302 redirects (default: true)
    proxyId: null                              # Associate a proxy from the proxy pool
    priorityMetadata: false                    # Prefer this server's metadata during merge

  - name: "Server C (custom spoof example)"
    url: "https://emby-c.example.com"
    apiKey: "your-api-key"
    spoofClient: "custom"
    customUserAgent: "Infuse/7.7.1 (iPhone; iOS 17.4.1; Scale/3.00)"
    customClient: "Infuse"
    customClientVersion: "7.7.1"
    customDeviceName: "iPhone"
    customDeviceId: "your-custom-device-id"
```

Settings modified in the admin panel take effect immediately — no restart required.

---

## Advanced Configurations & Under The Hood

### Upstream Server Authentication (Complete Mechanism)

Each upstream server supports one of two authentication methods:

| Method | Config Fields | How It Works |
|--------|--------------|--------------|
| Username/Password | `username` + `password` | Aggregator calls the upstream `AuthenticateByName` login API to obtain a Session Token, then reuses the session for subsequent requests |
| API Key | `apiKey` | Sends requests directly with the API Key — no login flow required (Recommended) |

Auth decision and fault-tolerance behavior:
- If both are configured on the same upstream, `apiKey` takes precedence.
- Failed logins are recorded per-upstream and reflected in health status without blocking parallel aggregation on other upstreams.
- Health checks and auto-reconnect reuse the last successful auth context for that upstream.

### Playback Mode Details

`playbackMode` determines how media streams are delivered to clients.

| Mode | How It Works | Best For |
|------|-------------|----------|
| `proxy` | Traffic routes through the aggregator. HLS manifests (`.m3u8`) are rewritten with relative proxy paths. Supports Range requests, subtitles, and attachments. | Upstream has no public IP; need to hide upstream addresses; need reverse-proxy/domain compatibility |
| `redirect` | Client receives an HTTP `302` redirect to the upstream stream URL. Post-redirect traffic bypasses the aggregator. | Client can reach upstream directly; save aggregator bandwidth |

**Priority**: per-upstream `playbackMode` > global `playback.mode` > `"proxy"` (default)

When using `proxy` mode, if the upstream has a dedicated streaming domain (CDN, etc.), set `streamingUrl` — the aggregator will use that domain for stream URLs instead of the API address.

### UA Spoofing Details (`spoofClient`)

Controls which client identity the aggregator presents to upstream servers. Affects login, API requests, health checks, and stream proxying.

| Value | User-Agent | X-Emby-Client | Use Case |
|-------|-----------|----------------|----------|
| `none` | Aggregator default | `Emby Aggregator` | Most servers — no client restrictions |
| `passthrough` | Real client UA (Infuse fallback) | Real client value | Servers with client whitelists |
| `infuse` | `Infuse/7.7.1 (iPhone; iOS 17.4.1; Scale/3.00)` | `Infuse` | Servers allowing only Infuse |
| `custom` | Custom value | Custom value | Servers requiring full control over client identity |

> **Note**: The `official` mode from V1.2 has been migrated to `custom` in V1.3, using the original Emby Web client defaults.

#### Passthrough Mode — How It Works

Passthrough uses a 5-level header resolution chain to ensure the upstream always receives a reasonable client identity:

1. **Live request headers** — If the current request carries `X-Emby-Client` headers (a real Emby client), use them directly.
2. **Captured headers for the current token** — When a real client (Infuse, Emby iOS, etc.) logs in to Emby-In-One, the aggregator captures and stores the client's `User-Agent`, `X-Emby-Client`, `X-Emby-Device-Name`, etc., keyed by proxy token. Subsequent requests from the same token reuse these headers.
3. **Last successful login headers for this upstream** — Each passthrough upstream persists the full headers from its last successful login. After a restart, these are used immediately without waiting for a user to log in again.
4. **Most recently captured headers** — If the current request has no token and the upstream has no login history, the most recently captured headers from any token are used.
5. **Infuse fallback** — If no captured headers exist at all (e.g., a fresh install on first boot), Infuse identity is used as the safe default.

Captured headers are layered on top of the Infuse base profile, so even if a client omits some Emby header fields (e.g., certain third-party apps), a complete client identity is still presented.

When a client logs in, all offline passthrough upstreams automatically retry login using the newly captured headers. Successfully used headers are persisted per-upstream; health checks and reconnects reuse them after restarts. When a token is revoked or expires, its captured headers are cleaned up as well.

### Metadata Priority (`priorityMetadata`)

When the same movie/episode appears on multiple upstream servers, the aggregator must choose one server's metadata (title, overview, images) as the "primary" version. Selection rules:

| Priority | Rule | Reason |
|----------|------|--------|
| 1 | Server with `priorityMetadata: true` | Manually designated preferred metadata source |
| 2 | Overview contains Chinese characters | Prefer localized Chinese metadata |
| 3 | Longer overview text | More complete description wins |
| 4 | Lower server index (earlier in config) | Stable tiebreaker |

This priority only affects which metadata is displayed — all servers' MediaSource versions are always preserved, and users can freely choose any version for playback.

### Media Merge Strategy

| Content Type | Dedup Key | Behavior |
|-------------|-----------|----------|
| **Movies** | TMDB ID, or Title + Year | Merged into one entry with multiple MediaSources |
| **Series** | TMDB ID, or Title + Year | Deduplicated at the series level |
| **Seasons** | Season number `IndexNumber` | Deduplicated by season number |
| **Episodes** | Season:Episode number | Deduplicated; best metadata chosen by priority algorithm above |
| **Libraries (Views)** | — | All preserved, with server name suffix appended for distinction |

Cross-server items are first interleaved (Round-Robin), then deduplicated.

### ID Virtualization

Every upstream Item ID is mapped to a globally unique virtual ID (UUID format). All IDs visible to clients are virtual.

- **Storage**: SQLite (WAL mode) with in-memory cache for fast lookups
- **Mapping**: `virtualId <-> { originalId, serverIndex }`, plus persisted `otherInstances` relationships
- **Persistence**: Mappings survive restarts; both primary and additional instance relationships are restored
- **Cleanup**: Deleting an upstream server automatically purges all its mappings and adjusts subsequent indices

---

## Health Check

- Every 60 seconds (configurable via `timeouts.healthInterval`), all upstreams are checked **in parallel** with `GET /System/Info/Public`
- Passthrough upstreams preferentially use that upstream's last successful login headers (persisted), falling back to the most recently captured client headers to avoid nginx rejections
- State transitions are logged (ONLINE → OFFLINE / OFFLINE → ONLINE)
- Health check timers are automatically cleaned up during graceful shutdown

---

## Logging System

### Log Levels

| Level | Output | Content |
|-------|--------|---------|
| DEBUG | File | All request details, ID resolution, header info |
| INFO | File + Console | Logins, server status changes, config changes |
| WARN | File + Console | 401/403 responses, server disconnections |
| ERROR | File + Console | Request failures, login failures, exceptions |

### Log Files

- Path: `data/emby-in-one.log` (Release deployment: `/opt/emby-in-one/data/`)
- Docker path: `/app/data/emby-in-one.log`
- Max 5MB per file, 1 rotated backup retained (auto-rotation)
- Downloadable and clearable from the admin panel

---

## Admin Panel

Access `http://your-ip:8096/admin` and log in with the admin credentials from the config file.

| Page | Features |
|------|----------|
| **System Overview** | Online server count, ID mapping count, storage engine (SQLite) |
| **Upstream Nodes** | Add / edit / delete / reconnect servers, drag-and-drop reordering |
| **Network Proxies** | HTTP/HTTPS proxy pool management with one-click connectivity testing |
| **Global Settings** | System name, default playback mode, admin account, timeout configuration |
| **Runtime Logs** | Live log viewer with level filtering (ERROR/WARN/INFO/DEBUG), keyword search, raw log file download, and log clearing |

### Admin API

All APIs require authentication (`X-Emby-Token` header or `api_key` query parameter). For security, `/admin/api/*` endpoints follow same-origin policy and do not return permissive CORS headers for arbitrary origins.

| Method | Path | Description |
|--------|------|-------------|
| GET | `/admin/api/status` | System status |
| GET | `/admin/api/upstream` | List upstream servers |
| POST | `/admin/api/upstream` | Add upstream server |
| PUT | `/admin/api/upstream/:index` | Modify upstream server |
| DELETE | `/admin/api/upstream/:index` | Delete upstream server (auto-cleans ID mappings) |
| POST | `/admin/api/upstream/:index/reconnect` | Reconnect upstream server |
| POST | `/admin/api/upstream/reorder` | Reorder servers |
| GET | `/admin/api/proxies` | List proxies |
| POST | `/admin/api/proxies` | Add proxy |
| POST | `/admin/api/proxies/test` | Test proxy connectivity |
| DELETE | `/admin/api/proxies/:id` | Delete proxy |
| GET | `/admin/api/settings` | Get global settings |
| PUT | `/admin/api/settings` | Modify global settings |
| GET | `/admin/api/logs?limit=500` | Retrieve in-memory logs |
| GET | `/admin/api/logs/download` | Download persisted log file |
| DELETE | `/admin/api/logs` | Clear logs |
| GET | `/admin/api/client-info` | Get captured client info |
| POST | `/admin/api/logout` | Admin logout |

---

## FAQ

### Passthrough Upstream Login Failure (403)

On a fresh install there are no captured client headers, so passthrough defaults to the Infuse identity. If the upstream nginx rejects Infuse:
1. Log in to Emby-In-One with any Emby client (Infuse, Emby iOS, etc.)
2. The aggregator automatically captures the client headers and retries passthrough upstream login
3. Once login succeeds, the client identity for that upstream is persisted — no manual action needed after future restarts
4. Check the `source` field in logs to confirm which header source was used (`last-success` = last successful headers, `captured-override` = login retry with captured headers, `infuse-fallback` = no captured headers available)
5. If the captured client UA itself is also rejected by the upstream, log in from a client the upstream allows to capture suitable identity headers

### Playback 403 / 401

Possible causes:
- Upstream token expired → Click "Reconnect" in the admin panel
- Passthrough upstream headers incomplete → Check logs for `Stream headers for [server name]` to confirm header info
- Version switching after merge → MediaSourceId is automatically resolved to the correct upstream server

### Slow Home Page / Incomplete Libraries

- Default request timeout is 15s, aggregation timeout is 20s
- If upstream servers have high network latency, some results may be skipped
- Look for `timeout` or `abort` keywords in the logs
- Increase timeout values in the `timeouts` section of `config.yaml`

### Forgot Admin Password

The admin password is automatically hashed (scrypt) after the first boot. Reset methods:

**Method 1: Edit config file**
1. Edit `config/config.yaml`, replace the hash after `password:` with a new plaintext password
2. Restart the service — the system will automatically re-hash the plaintext password

**Method 2: SSH management menu**
```bash
emby-in-one
# Select the "Change Password" option
```

### Docker Container Cannot Reach Upstream Servers

- Check if the upstream URL uses `localhost` → inside a container, localhost refers to the container itself; use the host machine IP or domain instead
- To access host services, use `host.docker.internal` (Docker Desktop) or the host's actual IP

---

## Disclaimer

> **Notice**: This project communicates with upstream servers by simulating and masking Emby client behavior. There resides inherent risk of upstream operators or associated platforms detecting proxies and enforcing bans against your account or API Key. Utilization of this project equates to your self-assumption of these risks. The author bears zero responsibility for account bans, data loss, or other damages resulting from its use.

---

## Project Architecture (For Developers)

```text
Emby-In-One/
├── cmd/emby-in-one/
│   └── main.go                     # Application entrypoint
├── internal/backend/
│   ├── config.go                   # YAML config load/save/validate/atomic write
│   ├── server.go                   # HTTP routing, middleware, CORS policy
│   ├── auth.go                     # Proxy token issuance & validation
│   ├── auth_manager.go             # Upstream auth management (login/session/API Key)
│   ├── identity.go                 # Client identity capture & Passthrough 5-level resolution
│   ├── identity_persistence.go     # Per-upstream client identity persistence
│   ├── idstore.go                  # SQLite bidirectional ID mapping (virtual ↔ original)
│   ├── id_rewriter.go              # Recursive ID virtualization/devirtualization rewriting
│   ├── query_ids.go                # Batch query ID resolution
│   ├── media.go                    # Media aggregation, deduplication, metadata priority
│   ├── library_image.go            # Image proxy (cache headers)
│   ├── series_userdata.go          # Series-level watch history isolation (Resume/NextUp)
│   ├── session_userdata.go         # Sessions/Playing progress reporting
│   ├── streamproxy.go              # HTTP stream proxy (backpressure, HLS relative path rewriting)
│   ├── fallback_proxy.go           # Fallback route: scan URL/Query for virtual IDs
│   ├── healthcheck.go              # Parallel health checks
│   ├── logger.go                   # Leveled logging (Console + File dual output + rotation)
│   ├── scrypt_local.go             # Admin password scrypt hashing
│   └── upstream_stub.go            # Upstream connection pool & concurrent request orchestration
├── third_party/sqlite/             # SQLite CGO source dependency
├── public/
│   └── admin.html                  # Vue 3 + Tailwind CSS admin panel SPA
├── build/                          # Multi-architecture pre-compiled binaries
├── Dockerfile                      # Go runtime container build
├── docker-compose.yml
├── install.sh                      # Source repo one-click deploy script (Docker)
├── release-install.sh              # Release binary one-click deploy script (systemd)
├── go_install.sh                   # Go environment install helper
└── emby-in-one-cli.sh              # SSH terminal management menu script
```

---

## License

GNU General Public License v3.0
