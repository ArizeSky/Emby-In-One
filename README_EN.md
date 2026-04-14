# Emby-In-One

> **Version: V1.4.0**

[Changelog](Update.md) | [中文文档](README.md) | [Security Policy](SECURITY.md) | [Update Plan](Update%20Plan.md) | [V1.2.1 Legacy Docs](README_V1.2.1.md) | [GitHub](https://github.com/ArizeSky/Emby-In-One)

Based on Go language, it implements a multi-server Emby aggregation proxy — merges media libraries from multiple upstream Emby servers into a single unified endpoint accessible by any standard Emby client. Supports multi-user management, independent watch history, UA spoofing, concurrent playback limits, and role-based access control.

## Demo Site

[Demo Site](https://emby.cothx.eu.cc/)
Emby Connection Address: https://emby.cothx.eu.cc/
Account: admin
Password: 5T5xF4oMxcnrcCPA

## Preview

![Preview 1](https://cdn.nodeimage.com/i/D293pIQcFNx4gXkfskPbnXFzmgCQ1JPx.webp)
![Preview 2](https://cdn.nodeimage.com/i/iDAXrYaIXdm9efhwl2BtqJjRUmGfTSKU.webp)
![Preview 3](https://cdn.nodeimage.com/i/K4jhTTMjv8rkHYiPNbXKUC0kXIzAXgq0.webp)
![Preview 4](https://cdn.nodeimage.com/i/jCilzHTw7vzRJYaQFtbvd8ZOEaTxZvk6.webp)

> Image hosting provided by [NodeImage](https://www.nodeimage.com), thanks for the support.

---

## Features Overview

- **Multi-User Management** — Supports creating regular users, each independently configurable with an accessible set of upstream servers; Admins can manage users via Admin panel, REST API, and SSH menu.
- **Independent User Accounts** — Regular users possess independent watch progress, played status, favorites, and "Continue Watching / Next Up", fully isolated from other users and upstream shared accounts; Admins retain original upstream behavior.
- **Concurrent Playback Limits** — Each upstream server can configure a maximum concurrent playback count (`maxConcurrent`). Playback requests exceeding this limit return 429; Auto-releases based on heartbeat timeout.
- **Role-Based Access Control** — Admins have full access to all servers and the management panel; Regular users can only access their assigned servers and cannot access the admin API.
- **Multi-Server Aggregation** — Merges and displays media libraries and search results from multiple servers. Uses Goroutine concurrent requests with configurable grace periods — fast servers return first, slower servers contribute within the grace window; timed-out data is silently backfilled in the background, so aggregation latency depends on the fastest server plus grace period rather than the slowest. When an upstream goes offline, already-aggregated content automatically falls back to other online servers via OtherInstances — Resume and NextUp remain unaffected.
- **Smart Deduplication & Prioritization** — Identical videos are automatically merged with multiple version sources retained; Supports a 4-level metadata priority logic (Designated Tag > Chinese > Length > Order) to smartly pick the best display information.
- **Advanced UA Spoofing** — Supports Infuse spoofing and client UA passthrough. Can also use `custom` mode to independently define all 5 Emby client identity headers for each upstream, bypassing common Emby UA restrictions.
- **Network Proxy Pool** — Configure dedicated HTTP/HTTPS proxies separately for each upstream server, complete with a built-in one-click connectivity tester.
- **Dual Playback Modes** — Proxy mode (traffic relayed, hides upstream, supports HLS/segments) or Redirect mode (302 redirects to upstream, saves proxy machine bandwidth).
- **Token Management & Session Stability** — Proxy tokens never expire (only removed on logout, password change, or manual revocation), preventing frequent 401 errors on long-idle devices; upstream tokens auto-recover via async re-login with 30-second debounce when expired; admin password changes automatically revoke all issued tokens.
- **Passthrough Delayed Login** — Upstream servers in passthrough mode no longer attempt login with Infuse identity at startup; they wait for a real client connection before authenticating, avoiding phantom device records on upstream Emby.
- **Full Control & Operations** — Built-in modern SSH CLI menu and Web admin panel; comes with persistent logs and SQLite ID mapping. SSH menu auto-detects Binary/Docker deployment mode, dispatching all operations to systemd or Docker Compose commands accordingly.

---

## Quick Installation

> **Notice for Legacy Node.js Deployment**: If you wish to deploy the V1.2.1 stable Node.js version, please navigate to the [Releases page](https://github.com/ArizeSky/Emby-In-One/releases) of this repository, download the V1.2.1 Source code archive, extract it, and run `bash install.sh`.

This project primarily recommends using Release binaries for V1.4.0 deployment directly on Linux servers (no local Go build required); Docker deployment is suitable for scenarios where you want to build the image yourself.

### Method 1: Release Binary One-Click Install (Primary Recommendation)

```bash
curl -fsSL -o release-install.sh https://raw.githubusercontent.com/ArizeSky/Emby-In-One/main/release-install.sh
sudo bash release-install.sh
```

Optional: install a specific version.

```bash
sudo bash release-install.sh V1.3.0
```

This script will automatically:
- Download the matching Release binary based on your CPU architecture (no local Go compilation needed)
- Initialize `/opt/emby-in-one/{config,data,log}` and generate a random admin password on the first run
- Fetch companion resources `admin.html`, `admin.js` and `emby-in-one-cli.sh` (the binary already embeds the admin panel; external files are optional override updates)
- Install and start the `systemd` service (`emby-in-one`), supporting auto-start on boot
- Auto-backup and perform a rollback-safe upgrade if an older version is detected

### Method 2: Source Repo One-Click Install Script (Recommended for developers / local image build)

```bash
git clone https://github.com/ArizeSky/Emby-In-One.git
cd Emby-In-One
bash install.sh
```

The script will automatically install the Docker environment, assign a random admin password, build the Go version image, and start the service. To manage your server later, type `emby-in-one` via SSH to call up the management menu.

### Method 3: Manual Docker Compose Deployment

1. Create project directories:
```bash
mkdir -p /opt/emby-in-one/{config,data}
cd /opt/emby-in-one
```
2. Copy all core files from this repository (including `go.mod`, `cmd/`, `internal/`, `public/`, `Dockerfile`, `docker-compose.yml`, etc.) to this directory.
3. Create the initial configuration `config/config.yaml`:
```yaml
server:
  port: 8096
  name: "Emby-In-One"
  # trustProxy: true        # Set to true when deployed behind a reverse proxy (Nginx/Caddy etc.)

admin:
  username: "admin"
  password: "your-strong-password" # Automatically encrypted after first boot

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
4. Build and start:
```bash
docker compose build
docker compose up -d
```

### Method 4: Direct Go Source Run (For Developers)

Requirements: Go 1.23+ and a C toolchain (Debian/Ubuntu run `apt install build-essential`).
```bash
mkdir -p config data
# Create config.yaml in the config folder as instructed in Method 3
go test ./...
go run ./cmd/emby-in-one
```

**Default Access URLs**:
- Emby Client Connection Address: `http://Server_IP:8096`
- Admin Panel: `http://Server_IP:8096/admin`

---

## System Requirements

**Release Binary Deployment (Recommended):**
- Linux (amd64 / arm64 / arm / mips / mipsle / riscv64)
- No Go compilation environment needed, directly run pre-compiled binaries

**Docker Deployment:**
- Docker 20.10+, Docker Compose v2
- Linux: Debian 11/12/13, Ubuntu 22/24 (recommended), other distros need self-verification
- Windows / macOS can also run (for dev and testing)

**Go Source Build:**
- Go 1.23+
- C Toolchain (CGO used for SQLite): Debian/Ubuntu run `apt install build-essential`

---

## Configuration Reference

The config file is located at `config/config.yaml` (mounted into the container at `/app/config/config.yaml` when using Docker).

```yaml
server:
  port: 8096
  name: "Emby-In-One"
  # id: Auto-generated on first boot, do not modify manually
  # trustProxy: true        # Set to true when behind a reverse proxy (see below)

admin:
  username: "admin"
  password: "your-strong-password"    # Automatically encrypted after first boot

playback:
  mode: "proxy"          # "proxy" or "redirect", global default

timeouts:
  api: 30000             # Single upstream API request timeout (ms)
  global: 15000          # Aggregation request max total timeout — waiting for all servers (ms)
  login: 10000           # Upstream login timeout (ms)
  healthCheck: 10000     # Health check timeout (ms)
  healthInterval: 60000  # Health check interval (ms)
  searchGracePeriod: 3000     # Search aggregation grace period — wait for other servers after first result (ms)
  metadataGracePeriod: 3000   # Metadata fetch grace period (ms)
  latestGracePeriod: 0        # "Latest Added" grace period — 0 means wait for all servers (ms)

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
    playbackMode: "redirect"                   # Overrides global playback mode
    spoofClient: "infuse"                      # none | passthrough | infuse | custom
    streamingUrl: "https://cdn.example.com"    # Independent streaming domain (optional)
    followRedirects: true                      # Follow upstream 302 redirects (default true)
    proxyId: null                              # Associate with proxy ID from proxy pool
    priorityMetadata: false                    # Prefer using this server's metadata when merging
    maxConcurrent: 3                           # Max concurrent playbacks, 0 means unlimited (affects regular users only)

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

Settings modified in the admin panel take effect hotly, no service restart required.

### Reverse Proxy Trust (`trustProxy`)

| Value | Behavior | Applicable Scenario |
|-------|----------|--------------------|
| `false` (default) | Login rate limiting uses the TCP connection IP (`RemoteAddr`) | Directly exposed to the internet, no reverse proxy |
| `true` | Login rate limiting trusts `X-Real-IP` / `X-Forwarded-For` headers | Deployed behind Nginx / Caddy or other reverse proxies |

> **Important**: If your Emby-In-One instance is behind a reverse proxy (Nginx, Caddy, Cloudflare, etc.), you **must** add `trustProxy: true` under the `server` section in `config.yaml`. Otherwise all client requests will appear to come from the same IP, and after 5 failed login attempts all users will be rate-limited for 15 minutes.

---

## Multi-User Management

V1.4 adds multi-user support, allowing admins to create multiple regular users, each independently configurable with accessible upstream servers.

### Role Descriptions

| Role | Permissions |
|------|-------------|
| Admin (admin) | Can access all servers, admin panel, admin API; Watch history shared with upstream servers |
| Regular User (user) | Can only access assigned servers; Has independent watch history (isolated from other users and upstream accounts) |

### Independent Watch History

Because all distributed users share the same upstream Emby account, upstream watch progress, played status, and favorites are shared. Starting from V1.4, regular users' watch data is completely isolated in the local SQLite database:

| Feature | Admin | Regular User |
|---------|-------|--------------|
| Resume (Continue Watching) | Upstream server data | Local independent data |
| Next Up | Upstream server data | Calculated based on local progress |
| Played Status | Upstream server data | Local independent record |
| Favorite | Upstream server data | Local independent record |
| UserData in browsing pages | Direct passthrough from upstream | Overlay local state over it |

**Working Principle:**

- Playback events (start, progress, stop) simultaneously write to the upstream server and the local database (dual write)
- Playback completion (progress ≥ 90%) is automatically marked as "played"
- Mark played / favorite and other user operations are also dual written
- When a user is deleted, their local watch data is automatically cleared
- Upon first playback of an item, the system automatically fetches metadata from upstream (series name, seasons, episodes) to support NextUp calculations

### Creating Regular Users

Admins can create and manage regular users through the following ways:

1. **Admin Panel** — Visual operations in the "User Management" page
2. **SSH Menu** — Use the `emby-in-one` command, select "Add Regular User" or "Delete Regular User"
3. **REST API** — `POST /admin/api/users` (requires admin Token)

### Configuring Accessible Servers

Each regular user can be assigned a set of accessible upstream servers (via server index list). After the user logs in, they can only see and play content on assigned servers. Users without any assigned servers (empty `allowedServers` list) cannot access any content.

### Concurrent Playback Limits

Each upstream server can independently configure `maxConcurrent` (maximum concurrent playback number):

- `0` (default): No limit
- Positive integer: Limits the number of regular users playing simultaneously on that server
- Admins are not subject to this limit
- Returning `429 Too Many Requests` when limit exceeded
- Based on 3-minute heartbeat timeout for auto-release of occupation

---

## Advanced Config & Core Principles

### Upstream Server Authentication (Complete Mechanism)

Each upstream server supports two authentication methods (choose one):

| Method | Config Fields | Working Principle |
|--------|--------------|-------------------|
| Username/Password | `username` + `password` | The proxy calls upstream's `AuthenticateByName` login interface for a Session Token, then reuses the session for future requests |
| API Key | `apiKey` | Directly carries API Key for requests, no login flow needed (Recommended) |

Authentication decision and fault tolerance logic:
- If both are configured, `apiKey` takes precedence.
- When login fails, an error is recorded and affects health check, but does not block concurrent aggregation of other upstreams.
- Health checking and auto-reconnect reuse the context from the most recent successful authentication for that upstream.

### Playback Mode Explained

`playbackMode` determines how the media stream is delivered to the client.

| Mode | Working Principle | Applicable Scenarios |
|------|-------------------|----------------------|
| `proxy` | Traffic is forwarded via the proxy server. Fragment URLs in HLS manifests (`.m3u8`) are rewritten as relative proxy paths. Supports Range requests, subtitles, and attachments. | Upstream lacks public IP; Need to hide upstream address from clients; Requires reverse proxy/public domain compatibility |
| `redirect` | The client receives a `302` redirect, connecting directly to the upstream stream URL. Traffic does not pass via the proxy after redirection. | Clients can directly connect upstream; Saves proxy server bandwidth |

**Priority**: Single server `playbackMode` > Global `playback.mode` > `"proxy"` (default)

When using `proxy` mode, if the upstream has a separate streaming domain (CDN, etc.), you can set `streamingUrl`, and the proxy will construct stream URLs using that domain instead of the API address.

### UA Spoofing Explained (`spoofClient`)

Controls what client identity the proxy communicates with the upstream server. Affects login, API requests, health checks, and stream proxying.

| Value | User-Agent | X-Emby-Client | Usage Scenario |
|-------|------------|---------------|----------------|
| `none` | Proxy default identity | `Emby Aggregator` | Most servers — no client restrictions |
| `passthrough` | True client UA (Infuse fallback) | True client value | Servers with client whitelists |
| `infuse` | `Infuse/7.7.1 (iPhone; iOS 17.4.1; Scale/3.00)` | `Infuse` | Servers strictly allowing Infuse |
| `custom` | Custom value | Custom value | Servers needing complete control over client markings |

> **Note**: The `official` mode from V1.2 has been automatically migrated to `custom` in V1.3, using the original Emby Web official client's default values.

#### Passthrough Mode Principles

Passthrough uses a five-level header resolution to ensure a reasonable client identity is offered to the upstream under any conditions:

1. **Live Request Header** — If the current request carries the `X-Emby-Client` header (a genuine Emby client), direct usage.
2. **Current Token Captured Header** — When a real client (Infuse, Emby iOS, etc.) logs in to Emby-in-One, the proxy captures and stores the client's `User-Agent`, `X-Emby-Client`, `X-Emby-Device-Name`, etc. based on the proxy Token; future requests heavily tied to the same Token will reuse these.
3. **Server's Last Successful Login Header** — Every time a passthrough server successfully logs in, the complete headers used are remembered and persisted. Will be used straight after reboot, without waiting for users to re-login.
4. **Most Recent Captured Header** — If the current request lacks a Token and the server has no historical successful records, it uses the lastly captured header from any Token.
5. **Infuse Fallback** — If there are entirely no captured client headers (e.g. freshly installed first boot), the Infuse identity acts as a safe default.

Captured headers overlay the basic Infuse profile, ensuring even if the client hasn't sent all Emby header fields (like certain third-party Apps), a fully fleshed client identity can still be presented.

When a client logs in, all offline passthrough servers automatically use the newly captured headers to attempt re-login. The successfully logged-in headers are persistently stored per-server; both health checks and reconnections post-reboot will use that server's last successful headers. Upon token revocation or expiration, its respective captured headers are cleaned as well.

### Metadata Priority (`priorityMetadata`)

When the exact same movie/episode appears on multiple servers, the proxy needs to pick one server's metadata (title, summary, picture) as the "primary" version. The rules are:

| Priority | Rule | Reason |
|----------|------|--------|
| 1 | Server styled with `priorityMetadata: true` | Manually designated preferred metadata source |
| 2 | Overview contains Chinese characters | Prioritize using Chinese localized metadata |
| 3 | Overview text is longer | A more complete description prioritized |
| 4 | Smaller server index (ordered ahead in config) | Stable fallback rule |

This priority solely affects which metadata to display—all servers' MediaSource versions are uniformly retained and clients can pick flexibly.

### Media Merge Strategy

| Content Type | Dedup Criterion | Behavior |
|--------------|-----------------|----------|
| **Movies** | TMDB ID, or Title + Year | Merged into a solitary entry containing multiple MediaSources |
| **Series** | TMDB ID, or Title + Year | Deduplicated at the series layer |
| **Seasons** | Season Number `IndexNumber` | Deduplication by season number |
| **Episodes** | Season:Episode number | Deduplicated; greatest metadata grabbed by the priority algorithm above |
| **Libraries (Views)** | — | Fully preserved, appending server names as suffixes for distinction |

Cross-server entries are initially interleaved (Round-Robin) before duplicated merging.

### ID Virtualization

Each upstream Item ID is mapped globally to a lone virtual ID (UUID layout). Any IDs visible to clients are virtual.

- **Storage**: SQLite (WAL pattern) persistence tied with memory cache aiding lookup speeds
- **Mapping**: `virtualId <-> { originalId, serverIndex }`, saving additionally persisted `otherInstances` mapping interactions
- **Persistence**: Re-mapping unnecessary post-restart; chief and appendage instance relationships are retrieved
- **Cleanup**: Purging upstream servers automatically clears mappings belonging to that server and recalibrates later indices

---

## Health Check

- Operates `GET /System/Info/Public` **in parallel** across all upstreams every 60 seconds (configurable via `timeouts.healthInterval`)
- Passthrough servers preferentially apply the server's prior successful login headers (persisted storage), relying next on the most recently captured headers guarding against nginx declines
- Traces logs on state alterations (ONLINE → OFFLINE / OFFLINE → ONLINE)
- Timers for health assessment instantly clear during graceful shutdowns

---

## Security Hardening

- **Admin plaintext password auto-hashed on startup**: The Go backend automatically migrates plaintext `admin.password` to scrypt hash format on service startup, without waiting for the first login
- **CLI password reset support**:

```bash
emby-in-one --reset-password <new-password>
# Or use the SSH menu option "Change Admin Password"
```

- **Stricter `data/tokens.json` permissions**: Written with `0600` permissions on Unix/Linux
- **Secure `config.yaml` writes**: Atomic replacement + `0600` permissions, reducing corruption risk and preventing other users from reading passwords
- **Request body size limit**: All API request bodies limited to 2MB (`http.MaxBytesReader`), preventing malicious large requests from consuming memory
- **Login rate limiting**: After 5 consecutive login failures from the same IP, locked for 15 minutes with `429 Too Many Requests`; atomic operations prevent TOCTOU race conditions; supports real IP detection behind reverse proxies (`X-Real-IP` / `X-Forwarded-For` / IPv6)
- **Graceful shutdown**: On receiving `SIGINT` / `SIGTERM`, drains active connections (up to 10 seconds) before closing the HTTP server and health check timers
- **Admin panel CSP**: Admin panel returns `Content-Security-Policy` headers, restricting script and style sources to reduce XSS risk
- **Stream URL cache auto-eviction**: `IDStore` stream URL cache entries expire after 4 hours, cleaned every 30 minutes, preventing unbounded memory growth during long-running operation
- **Proxy connectivity test SSRF protection**: The admin panel's proxy test endpoint includes DNS rebinding protection, blocking connections to private/reserved IP addresses (`127.x`, `10.x`, `172.16-31.x`, `192.168.x`, etc.)
- **YAML comment-safe parsing**: Config file parsing correctly handles `#` characters inside quotes, no longer incorrectly truncating values containing `#`

---

## Logging System

### Log Levels

| Level | Output | Content |
|-------|--------|---------|
| DEBUG | File   | All request details, ID resolution, header info |
| INFO  | File + Console | Logins, server status changes, config changes |
| WARN  | File + Console | 401/403 responses, server disconnections |
| ERROR | File + Console | Request failures, login failures, exceptions |

### Log Files

- Path: `data/emby-in-one.log` (Release sets `data/` at `/opt/emby-in-one/data/`)
- Docker path: `/app/data/emby-in-one.log`
- Up to 5MB per file, retaining 1 rotated backup (auto-rotation)
- Capable of being downloaded and cleared inside the admin panel

### Log Configuration

Default log level is `info`. Enable full debug logging via environment variables when troubleshooting:

```bash
LOG_LEVEL=debug FILE_LOG_LEVEL=debug
```

In Docker Compose:

```yaml
environment:
  - LOG_LEVEL=debug
  - FILE_LOG_LEVEL=debug
```

---

## Admin Panel

Access `http://your-ip:8096/admin`, logging in with the admin credentials from the config file.

| Page | Functions |
|------|-----------|
| **System Overview** | Online server count, ID mapping count, storage engine (SQLite) |
| **Upstream Nodes** | Add / edit / delete / reconnect servers, drag-and-drop ordering; Supports configuring maximum concurrent playback (`maxConcurrent`) |
| **User Mgmt** | Create, edit, enable/disable, delete regular users; Visually configure accessible servers |
| **Network Proxies** | HTTP/HTTPS proxy pool management, supports one-click connectivity testing |
| **Global Settings** | System name, default playback mode, admin account, timeout & grace period configuration |
| **Runtime Logs** | Real-time log viewing, supports level filtering (ERROR/WARN/INFO/DEBUG), keyword search, downloading raw log files, and clearing logs |

> The admin panel sidebar displays the current running version number. For adding/editing `spoofClient: passthrough` upstreams, if there is no captured client identity available, the admin API will still save the configuration but return a warning, keeping the upstream as offline until a real client logs in and triggers automatic retry.

### Admin API

All APIs require authentication (`X-Emby-Token` header or `api_key` query parameter). For security reasons, `/admin/api/*` endpoints only open to same-origin requests, denying unhindered cross-origin acceptances.

---

## SSH Management Menu

After installation, use:

```bash
emby-in-one
```

Available commands:

- Start / restart / stop service
- Online update (latest version) / download specific version
- View service status, public IP
- View admin credentials, modify admin username / password
- View user list, add regular user, delete regular user
- View logs, check version (`--version`)
- Uninstall service (supports preserving config and data)

> The SSH menu auto-detects the current deployment method (Binary / Docker), dispatching all operations to the corresponding systemd or Docker Compose commands. Docker mode updates use a source-rebuild workflow. The menu title bar displays the current version number.

---

## Data Directory Description

Runtime directories:

- `config/` — Stores config file `config.yaml`
- `data/` — Stores runtime data:
  - `mappings.db` — Virtual ID mappings, additional instances interactions, user data (UserStore), and watch history (WatchStore)
  - `tokens.json` — Proxy layer token storage
  - `captured-headers.json` — Passthrough client headers persistence
  - `emby-in-one.log` — Log file

| Method | Path | Description |
|--------|------|-------------|
| GET | `/admin/api/status` | System status |
| GET | `/admin/api/upstream` | List upstream servers |
| POST | `/admin/api/upstream` | Add upstream server |
| PUT | `/admin/api/upstream/:index` | Modify upstream server |
| DELETE | `/admin/api/upstream/:index` | Delete upstream server (auto-cleans ID mappings) |
| POST | `/admin/api/upstream/:index/reconnect` | Reconnect upstream server |
| POST | `/admin/api/upstream/reorder` | Adjust server ordering |
| GET | `/admin/api/proxies` | List proxies |
| POST | `/admin/api/proxies` | Add proxy |
| POST | `/admin/api/proxies/test` | Test proxy connectivity |
| DELETE | `/admin/api/proxies/:id` | Delete proxy |
| GET | `/admin/api/settings` | Retrieve global settings |
| PUT | `/admin/api/settings` | Modify global settings |
| GET | `/admin/api/logs?limit=500` | Fetch in-memory logs |
| GET | `/admin/api/logs/download` | Download persisted log files |
| DELETE | `/admin/api/logs` | Clear logs |
| GET | `/admin/api/client-info` | Get currently captured client information |
| POST | `/admin/api/logout` | Admin logout |

---

## FAQ

### Passthrough Upstream Login Failure (403)

If no client identities have been logged upon the first installation, passthrough will default to using the Infuse identity. If the upstream nginx rejects Infuse:
1. Log into Emby-in-One using any Emby client (Infuse, Emby iOS, etc.)
2. The proxy will automatically capture the client header arrays and retry login over the passthrough server
3. Upon a successful log-in, this server's specific client identity will become persisted; requiring no manual re-attempts after future restarts
4. Monitor logs mapping `source` fields to verify which header source was used (`last-success` = last cleared traits, `captured-override` = retry overrides matching fully tracked headers, `infuse-fallback` = defaulting maneuvers completely devoid of captured targets)
5. If the naturally captured client UA itself is also rejected by the upstream, explicitly log in using an allowed client to capture appropriate identities safely

### Only Admin Login Can Capture Client UA

In passthrough mode, the proxy needs to capture the real client's UA / Device and other Emby identity headers. **Only when logging in with the admin account will the proxy capture and store these client header information**. Regular user logins do not trigger UA capture.

Reason: The admin is the only role that maps directly to the upstream Emby account. Only the admin's login session needs to maintain a real client identity to pass through to upstream servers. Regular users' requests are sent by the proxy using the admin's previously captured client identity.

If your passthrough upstream consistently fails to auto-login, please verify:
1. You have logged into Emby-in-One using a real Emby client (Infuse, Emby iOS, etc.) with the **admin** account
2. Check the admin panel "Captured Client Info" page to confirm records exist
3. To change the captured client identity, log in once with the desired client using the admin account

### Playback 403 / 401

Possible causes:
- Upstream token expired → Click "Reconnect" in the admin panel
- Passthrough server headers incomplete → Check the logs querying `Stream headers for [Server Name]` to confirm proper header capture
- Media merge switching → MediaSourceId translates precisely and points correctly to the mapped upstream

### Loading Delay / Incomplete Library Merging

- Default search grace period is 3 seconds — after the first server responds, up to 3 more seconds are allowed for remaining servers; timed-out server data is silently backfilled in the background
- If upstream servers have generally high latency, increase `searchGracePeriod` and `metadataGracePeriod` in the admin panel "Global Settings" or `config.yaml` `timeouts` section
- `latestGracePeriod` defaults to 0 (wait for all servers); set to a positive value if "Latest Added" on the home page loads slowly
- Check logs for `timeout` or `abort` keywords
- You can also increase `api` (single request timeout) and `global` (aggregation total timeout) values

### Admin Password Lost

After first boot, the Admin password automatically hashes (scrypt). Recovery methods:

**Method 1: File Modifications**
1. Edit `config/config.yaml`, swapping the hash after `password:` directly to an explicit plaintext entry 
2. Execute an application restart—the system natively identifies and hashes the plaintext properly.

**Method 2: Command Line Menu**
```bash
emby-in-one
# Opt maneuvering toward the "Change Password" toggles directly
```

### Reverse Proxy Users Rate-Limited (429)

If all users receive `429 Too Many Requests` after 5 failed login attempts, `trustProxy` is not enabled:
1. Add `trustProxy: true` under the `server` section in `config.yaml`
2. Restart the service
3. Verify that your reverse proxy correctly sets the `X-Real-IP` or `X-Forwarded-For` header

### Docker Containers Fail to Reach Upstream

- Check if the upstream URL uses `localhost` → Inside a container, `localhost` points to the container itself. Use the host's actual IP or domain.
- To access host-machine services, use `host.docker.internal` (Docker Desktop) or the actual local IP address.

---

## Disclaimer

> **Notice**: This project communicates with upstream servers by simulating and masking Emby client behavior. There resides inherent risk of upstream operators or associated platforms detecting proxies and enforcing bans against your account or API Key. Utilization of this project equates to your self-assumption of these risks. The author bears zero responsibility for account bans, data loss, or other damages resulting from its use.

---

## Project Architecture (Developer Reference)

```text
Emby-In-One/
├── cmd/emby-in-one/
│   └── main.go                     # Application entrypoint
├── internal/backend/
│   ├── config.go                   # YAML config load/save/validate/atomic write
│   ├── server.go                   # HTTP server startup & graceful shutdown
│   ├── routes.go                   # Route registry (URL → Handler mapping)
│   ├── middleware.go               # HTTP middleware (CORS, logging, status capture, SSRF protection, CSP)
│   ├── auth.go                     # Proxy token issuance & validation
│   ├── auth_context.go             # Per-request auth context injection & extraction
│   ├── auth_manager.go             # Upstream auth management (login/session/API Key)
│   ├── identity.go                 # Client identity capture & Passthrough 5-level resolution
│   ├── identity_persistence.go     # Per-upstream client identity persistence
│   ├── user_store.go               # Multi-user storage (CRUD, password hashing, memory index + SQLite)
│   ├── handlers_admin.go           # Admin API handlers (upstream server CRUD)
│   ├── handlers_system.go          # System info endpoints (/System/Info)
│   ├── handlers_user.go            # User login rate limiting & user-related handlers
│   ├── admin_validation.go         # Admin input validation & helper utilities
│   ├── idstore.go                  # SQLite bidirectional ID mapping (virtual ↔ original)
│   ├── id_rewriter.go              # Recursive ID virtualization/devirtualization rewriting
│   ├── query_ids.go                # Batch query ID resolution
│   ├── media.go                    # Media aggregation, dedup, metadata priority selection
│   ├── aggregation.go              # Common aggregation framework (grace period + background backfill)
│   ├── media_items.go              # Media item queries (multi-upstream fan-out merge)
│   ├── media_resume.go             # Resume Items proxy & multi-upstream merge
│   ├── media_nextup.go             # Next Up proxy & multi-upstream merge
│   ├── media_playback.go           # PlaybackInfo query & concurrent playback limit check
│   ├── media_stream.go             # Video/audio stream proxy (virtual ID route resolution)
│   ├── library_image.go            # Image proxy (cache headers)
│   ├── series_userdata.go          # Series-level watch history isolation (Resume/NextUp)
│   ├── session_userdata.go         # Sessions/Playing progress reporting
│   ├── watch_store.go              # Per-user watch progress storage & persistence
│   ├── playback_limiter.go         # Concurrent playback limiter (heartbeat timeout auto-release)
│   ├── streamproxy.go              # HTTP stream proxy (backpressure, HLS relative path rewriting)
│   ├── fallback_proxy.go           # Fallback route: scan URL/Query for virtual IDs
│   ├── healthcheck.go              # Parallel health checks
│   ├── logger.go                   # Leveled logging (Console + File dual output + rotation)
│   ├── scrypt_local.go             # Admin password scrypt hashing
│   ├── sqlite_cgo.go               # CGO embedded SQLite compilation & low-level bindings
│   └── upstream_stub.go            # Upstream connection pool & concurrent request orchestration
├── third_party/sqlite/             # SQLite CGO source dependency
├── public/
│   ├── embed.go                    # go:embed directive (compiles admin.html and admin.js into binary)
│   ├── admin.html                  # Vue 3 + Tailwind CSS admin panel template
│   └── admin.js                    # Vue 3 application logic (extracted from admin.html)
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
