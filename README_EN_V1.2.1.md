# Emby-In-One

> **Version: V1.2.1**

[中文文档](README.md) | [Changelog](Update.md) | [Update Plan](Update%20Plan.md) | [GitHub](https://github.com/ArizeSky/Emby-In-One)

Multi-server Emby aggregation proxy — merges libraries from multiple upstream Emby servers into a single endpoint accessible by any standard Emby client.

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

## Features

- **Multi-Server Aggregation** — Combine media libraries, search results, and metadata from multiple Emby servers into one unified view
- **Smart Deduplication** — **Search only**: identical titles (matched by TMDB ID or Title + Year) are automatically merged in search results, preserving multiple MediaSource versions; when browsing a library, data comes directly from the upstream server that owns that library — no cross-library merging is applied
- **Series-Level History Isolation** — Search results can still aggregate the same series across multiple servers, but once you enter a specific series, `Resume` / `NextUp` now prefers the primary instance of that search result and only falls back to other same-series instances when needed, instead of mixing progress from every server together
- **Client Passthrough** — Forward real client identity (UA, device info) to upstream servers with proxy-token isolation, preventing one device from reusing another device's captured identity
- **UA Spoofing** — Impersonate Infuse or official Emby Web client for servers with strict client policies
- **Dual Playback Modes** — Proxy mode (traffic routed through the aggregator, with HLS manifests rewritten to proxy-relative paths) or Redirect mode (302 redirect to upstream)
- **Proxy Pool** — Assign HTTP/HTTPS proxies per upstream server for geo-restricted or network-segmented environments
- **Persistent Logging** — Auto-rotating log files (5 MB), with web-based viewing, downloading, and clearing
- **Web Admin Panel** — Full server management UI: add/edit/delete/reorder upstream servers, live logs, global settings, and draft-first upstream validation before saving

---

## Known Issues

- **Series duplication due to non-standard upstream servers**: Intelligent deduplication during series search might be messy for a few upstream servers with non-standard season divisions, but this doesn't affect the viewing order and experience.

## Disclaimer

> **Notice**: This project communicates with upstream servers by simulating Emby client behavior. There is a risk that upstream Emby servers or related platforms may detect this and ban your account or API Key. By using this project, you acknowledge and accept these risks. The author is not responsible for any account bans, data loss, or other damages resulting from the use of this project.

---

## Requirements

**Docker Deployment (Recommended):**
- Docker 20.10+, Docker Compose v2
- Linux: Debian 11/12/13, Ubuntu 22/24 (recommended)
- Windows / macOS also work for development/testing

**Node.js (Direct):**
- Node.js 18+ (20 LTS recommended)
- Build tools for `better-sqlite3` (see [Troubleshooting](#sqlite-build-failure))

---

## Installation

### Option 1: One-Line Install Script (Recommended for Linux Servers)

```bash
git clone https://github.com/ArizeSky/Emby-In-One.git
cd Emby-In-One
bash install.sh
```

The script will:
- Install Docker and Docker Compose if missing (falls back to Alibaba Cloud mirror if `get.docker.com` is slow)
- Create the `/opt/emby-in-one` project directory
- Generate a random admin password and write the config
- Build the image and start the container
- Install the `emby-in-one` CLI management command

After installation, use `emby-in-one` in SSH to manage the service (start / stop / restart / view logs / change credentials).

---

### Option 2: Docker Compose (Manual)

```bash
# 1. Create directory structure
mkdir -p /opt/emby-in-one/{config,data,log}
cd /opt/emby-in-one

# 2. Copy project files (src/ public/ package*.json Dockerfile docker-compose.yml)

# 3. Create config file
cat > config/config.yaml << 'EOF'
server:
  port: 8096
  name: "Emby-In-One"

admin:
  username: "admin"
  password: "your-strong-password"

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
EOF

# 4. Build and start
docker compose build
docker compose up -d
```

---

### Option 3: Node.js (Development / Testing)

```bash
# 1. Install dependencies
npm install
```

> **Note:** `better-sqlite3` requires a C++ build toolchain. On Debian/Ubuntu:
> ```bash
> apt install build-essential python3
> ```
> If it fails, the app automatically falls back to in-memory storage (ID mappings are lost on restart).

```bash
# 2. Create config
mkdir -p config
# Edit config/config.yaml (see Configuration section below)

# 3. Start
npm start          # Production
npm run dev        # Development (auto-restart on file changes, requires Node.js 18+)
```

After startup:
- **Admin Panel**: `http://localhost:8096/admin`
- **Emby Client Endpoint**: `http://localhost:8096`

---

## Configuration

Config file: `config/config.yaml` (Docker: mounted at `/app/config/config.yaml`)

```yaml
server:
  port: 8096
  name: "Emby-In-One"
  # id: auto-generated on first start, do not edit manually

admin:
  username: "admin"
  password: "your-strong-password"

playback:
  mode: "proxy"          # "proxy" or "redirect" — global default

timeouts:
  api: 30000             # Per-request timeout for upstream API calls (ms)
  global: 15000          # Aggregation timeout — max wait for all servers (ms)
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
    spoofClient: "infuse"                      # none | passthrough | infuse | official
    streamingUrl: "https://cdn.example.com"    # Separate streaming domain (optional)
    followRedirects: true                      # Follow 302s from upstream (default: true)
    proxyId: null                              # Link to a proxy from the pool
    priorityMetadata: false                    # Prefer this server's metadata in merges
```

Settings changed via the admin panel are hot-reloaded — no restart required.

---

## Upstream Authentication

Each upstream server uses one of two authentication methods:

| Method | Fields | How it works |
|--------|--------|-------------|
| Username / Password | `username` + `password` | Proxy logs in via `AuthenticateByName` and maintains a session token |
| API Key | `apiKey` | Uses an Emby API key directly — no login needed |

---

## Playback Modes

The `playbackMode` setting determines how media streams are delivered to the client.

| Mode | How it works | Best for |
|------|-------------|----------|
| `proxy` | Stream traffic flows through the aggregator. HLS manifests (`.m3u8`) are rewritten into proxy-relative paths, so playback no longer depends on `localhost` or a hard-coded public origin. Supports Range requests, subtitles, and attachments. | Upstream has no public IP; you want to hide upstream URLs from clients; you need reverse-proxy/public-domain compatibility |
| `redirect` | Client receives a `302` redirect pointing directly to the upstream stream URL. No traffic passes through the aggregator after the redirect. | Client can reach upstream directly; saves proxy bandwidth |

**Priority order:** per-server `playbackMode` > global `playback.mode` > `"proxy"` (default)

When using `proxy` mode with a separate streaming domain, set `streamingUrl` on the upstream server config. The aggregator will build stream URLs using that domain instead of the main API URL.

---

## UA Spoofing (`spoofClient`)

Controls which client identity the aggregator presents to each upstream server. This affects login, API requests, health checks, and streaming.

| Value | User-Agent | X-Emby-Client | Use Case |
|-------|-----------|----------------|----------|
| `none` | (aggregator default) | `Emby Aggregator` | Most servers — no restrictions |
| `passthrough` | Real client UA (with Infuse fallback) | Real client value | Servers with client whitelists |
| `infuse` | `Infuse/7.7.1 (iPhone; iOS 17.4.1; Scale/3.00)` | `Infuse` | Servers that only allow Infuse |
| `official` | `Mozilla/5.0 ... Emby/1.0.0` | `Emby Web` v4.8.3.0 | Servers that only allow official clients |

### Passthrough Mode — How It Works

Passthrough uses a 3-tier header resolution:

1. **Live request headers** — If the current request comes from a real Emby client (detected by the presence of `X-Emby-Client` header), those headers are used directly via `AsyncLocalStorage`.
2. **Captured headers for the current token** — When a real client (Infuse, Emby iOS, etc.) logs into Emby-in-One, the proxy captures and stores that client's `User-Agent`, `X-Emby-Client`, `X-Emby-Device-Name`, and related headers under the current proxy token. Only requests with the same token reuse them.
3. **Infuse fallback** — If no real client headers are available (e.g. right after server start), the Infuse profile is used as a safe default.

Captured headers are merged on top of the Infuse base profile, so any missing fields are automatically filled by Infuse values. This means even clients that don't send all Emby headers (like some third-party apps) will present a complete identity.

When a client logs in, all offline passthrough servers automatically re-attempt login with the newly captured headers. When a token is revoked or expires, its captured headers are removed too.

---

## Metadata Priority (`priorityMetadata`)

When the same episode exists on multiple servers, the aggregator must pick one server's metadata (title, overview, images) as the "primary" version. The selection follows this priority:

| Priority | Rule | Reason |
|----------|------|--------|
| 1 | `priorityMetadata: true` | Manually designated as the preferred metadata source |
| 2 | Has Chinese characters in Overview | Prefer localized Chinese metadata |
| 3 | Longer Overview text | More complete description wins |
| 4 | Lower server index (higher in config list) | Stable tiebreaker |

This only affects which metadata is displayed — all MediaSource versions from all servers are always preserved and selectable.

---

## Media Merge Strategy

| Content Type | Dedup Key | Behavior |
|-------------|-----------|----------|
| **Movies** | TMDB ID, or Title + Year | Merged into one item with multiple MediaSources |
| **Series** | TMDB ID, or Title + Year | Deduplicated at series level |
| **Seasons** | Season `IndexNumber` | Deduplicated by season number |
| **Episodes** | `Season:Episode` number | Deduplicated; metadata chosen by priority algorithm above |
| **Views (Libraries)** | — | All kept, server name appended as suffix |

Items are merged using interleaved (round-robin) ordering across servers, then deduplicated.

---

## ID Virtualization

Every upstream Item ID is mapped to a globally unique virtual ID (UUID format). Clients only ever see virtual IDs.

- **Storage:** SQLite (WAL mode) preferred; automatic fallback to in-memory `Map`
- **Mapping:** `virtualId <-> { originalId, serverIndex }`, plus persisted `otherInstances` relationships for additional cross-server instances
- **Persistence:** Survives restarts with SQLite, including additional-instance relationships used by multi-source details and fallback logic; in-memory mode loses mappings on restart
- **Cleanup:** Deleting an upstream server automatically purges its mappings and adjusts remaining indices

---

## Health Checks

- Runs every 60 seconds (configurable via `timeouts.healthInterval`) against all upstream servers in parallel
- Endpoint: `GET /System/Info/Public`
- Passthrough servers use captured client headers (to avoid nginx rejection)
- Status changes are logged: `ONLINE → OFFLINE` / `OFFLINE → ONLINE`

---

## Logging

### Log Levels

| Level | Output | Content |
|-------|--------|---------|
| DEBUG | File only | All request details, ID resolution, header info |
| INFO | File + Terminal | Login events, server status changes, config changes |
| WARN | File + Terminal | 401/403 responses, servers going offline |
| ERROR | File + Terminal | Request failures, login failures, exceptions |

### Log Files

- Local: `data/emby-in-one.log` (or `log/` directory depending on setup)
- Docker: `/app/data/emby-in-one.log`
- Max 5 MB per file, 1 rotated backup
- Downloadable and clearable from the admin panel

---

## Admin Panel

Access at `http://your-ip:8096/admin`, log in with the admin credentials from your config.

| Page | Features |
|------|----------|
| **Dashboard** | Online server count, ID mapping stats, storage engine (SQLite / Memory) |
| **Upstream Servers** | Add / edit / delete / reconnect servers, drag to reorder |
| **Proxies** | HTTP/HTTPS proxy pool management |
| **Settings** | Server name, default playback mode, admin credentials, timeout tuning |
| **Logs** | Real-time log viewer with level filtering (ERROR/WARN/INFO/DEBUG), keyword search, download raw log file, clear logs |

### Admin API

All endpoints require authentication via `X-Emby-Token` header or `api_key` query parameter. For security, `/admin/api/*` is same-origin only and no longer reflects arbitrary cross-origin requests.

| Method | Path | Description |
|--------|------|-------------|
| GET | `/admin/api/status` | System status |
| GET | `/admin/api/upstream` | List upstream servers |
| POST | `/admin/api/upstream` | Add upstream server |
| PUT | `/admin/api/upstream/:index` | Edit upstream server |
| DELETE | `/admin/api/upstream/:index` | Delete upstream server (auto-cleans ID mappings) |
| POST | `/admin/api/upstream/:index/reconnect` | Reconnect upstream server |
| POST | `/admin/api/upstream/reorder` | Reorder servers |
| GET | `/admin/api/proxies` | List proxies |
| POST | `/admin/api/proxies` | Add proxy |
| DELETE | `/admin/api/proxies/:id` | Delete proxy |
| GET | `/admin/api/settings` | Get global settings |
| PUT | `/admin/api/settings` | Update global settings |
| GET | `/admin/api/logs?limit=500` | Get in-memory logs |
| GET | `/admin/api/logs/download` | Download persistent log file |
| DELETE | `/admin/api/logs` | Clear logs |
| GET | `/admin/api/client-info` | Get captured client info |

---

## Troubleshooting

### Passthrough Server Login Failure (403)

On startup, no real client has connected yet, so passthrough defaults to the Infuse identity. If upstream nginx rejects Infuse:
1. Log in to Emby-in-One with a real Emby client (Infuse, Emby iOS, etc.)
2. The proxy captures the client headers and automatically retries login for offline passthrough servers
3. Check the log for `header source` to confirm which header source was used

### Playback 403 / 401

- Upstream token expired → Click "Reconnect" in the admin panel
- Passthrough headers incomplete → Check `Stream headers for [ServerName]` in logs
- Multi-source version switching → MediaSourceId is automatically resolved to the correct upstream server

### Slow Loading / Missing Libraries

- Default API timeout is 15s, aggregation timeout is 20s
- High-latency upstream servers may be skipped when they exceed the timeout
- Search logs for `timeout` or `abort`
- Increase values in `config.yaml` under `timeouts`

### SQLite Build Failure

```bash
# Debian / Ubuntu
apt install build-essential python3

# Skip SQLite, use in-memory storage (mappings lost on restart)
npm install --ignore-scripts
```

### Docker Container Can't Reach Upstream

- If upstream URL uses `localhost` → inside the container, localhost refers to the container itself. Use the host machine's IP or domain name instead.
- To access host services from Docker, use `host.docker.internal` (Docker Desktop) or the host's actual IP.

---

## Project Structure

```
src/
├── index.js                    # Entry point: load config, init, start server
├── config.js                   # YAML config load / save / validate
├── auth.js                     # Proxy-layer auth (independent of upstream tokens)
├── emby-client.js              # Single upstream HTTP client (axios + keepAlive pool)
├── id-manager.js               # Bidirectional ID mapping (SQLite + memory cache)
├── upstream-manager.js         # Upstream orchestration: concurrent requests, merge, health checks
├── server.js                   # Express route mounting and middleware
├── middleware/
│   ├── auth-middleware.js      # Token extraction and validation
│   └── request-context.js      # Inject req.resolveId() helpers
├── routes/
│   ├── system.js               # /System/Info/Public, /System/Ping
│   ├── users.js                # Auth, Views (merged + deduped)
│   ├── items.js                # Items (ParentId routing or full merge), Resume, Latest, Similar
│   ├── library.js              # Shows/Seasons/Episodes, Search, Genres
│   ├── playback.js             # PlaybackInfo (multi-source merge), playback state reporting
│   ├── sessions.js             # Sessions/Playing progress reporting
│   ├── streaming.js            # Video/Audio/HLS/Subtitle stream proxy or redirect
│   ├── images.js               # Image proxy (24h cache headers)
│   ├── fallback.js             # Catch-all: scan URL/query for virtual IDs
│   └── admin.js                # Admin panel API + in-memory log ring buffer
└── utils/
    ├── logger.js               # Winston logging (Console + File dual transport)
    ├── id-rewriter.js          # Recursive ID virtualization / de-virtualization
    ├── stream-proxy.js         # HTTP stream proxy (backpressure, redirects, relative HLS rewriting)
    ├── captured-headers.js     # Store real client headers per proxy token (for passthrough)
    ├── cors-policy.js          # Split CORS policy for admin vs client routes
    └── request-store.js        # AsyncLocalStorage for per-request context forwarding

public/
└── admin.html                  # Vue 3 + Tailwind CSS admin panel SPA
```

---

## License

GNU General Public License v3.0
