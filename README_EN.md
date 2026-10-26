# Emby-In-One

> **Current release candidate: V1.4.4-rc3 (Pre-release) · Current stable release: V1.4.3**

[![License: GPL v3](https://img.shields.io/github/license/ArizeSky/Emby-In-One?color=blue)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.23+-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![SQLite](https://img.shields.io/badge/SQLite-3-003B57?logo=sqlite&logoColor=white)](https://www.sqlite.org/)
[![Docker](https://img.shields.io/badge/Docker-20.10+-2496ED?logo=docker&logoColor=white)](https://www.docker.com/)
[![GitHub Release](https://img.shields.io/github/v/release/ArizeSky/Emby-In-One?color=green)](https://github.com/ArizeSky/Emby-In-One/releases)
[![GitHub Stars](https://img.shields.io/github/stars/ArizeSky/Emby-In-One?style=social)](https://github.com/ArizeSky/Emby-In-One)

[Docker operations](DOCKER_EN.md) | [Release policy](RELEASING.md) | [Changelog](Update.md) | [中文文档](README.md) | [Security Policy](SECURITY.md) | [Update Plan](Update%20Plan.md) | [V1.2.1 Legacy Docs](README_V1.2.1.md) | [GitHub](https://github.com/ArizeSky/Emby-In-One)

Based on Go language, it implements a multi-server Emby aggregation proxy — merges media libraries from multiple upstream Emby servers into a single unified endpoint accessible by any standard Emby client. Supports multi-user management, independent watch history, UA spoofing, concurrent playback limits, and role-based access control.

## Table of Contents

- [Preview](#preview)
- [Features Overview](#features-overview)
- [Quick Installation](#quick-installation)
- [Reverse Proxy (Nginx)](#reverse-proxy-nginx)
- [System Requirements](#system-requirements)
- [Configuration Reference](#configuration-reference)
- [Multi-User Management](#multi-user-management)
- [Advanced Config & Core Principles](#advanced-config--core-principles)
- [Health Check](#health-check)
- [Security Hardening](#security-hardening)
- [Logging System](#logging-system)
- [Admin Panel](#admin-panel)
- [SSH Management Menu](#ssh-management-menu)
- [Data Directory Description](#data-directory-description)
- [FAQ](#faq)
- [Disclaimer](#disclaimer)
- [Project Architecture](#project-architecture-developer-reference)
- [Star History](#star-history)
- [License](#license)

## Preview

![Preview 1](https://cdn.nodeimage.com/i/D293pIQcFNx4gXkfskPbnXFzmgCQ1JPx.webp)
![Preview 2](https://cdn.nodeimage.com/i/iDAXrYaIXdm9efhwl2BtqJjRUmGfTSKU.webp)
![Preview 3](https://cdn.nodeimage.com/i/K4jhTTMjv8rkHYiPNbXKUC0kXIzAXgq0.webp)
![Preview 4](https://cdn.nodeimage.com/i/jCilzHTw7vzRJYaQFtbvd8ZOEaTxZvk6.webp)

> Image hosting provided by [NodeImage](https://www.nodeimage.com), thanks for the support.

---

## Features Overview

- **Multi-User Management** — Supports creating regular users, each independently configurable with an accessible set of upstream servers; Admins can manage users via Admin panel, REST API, and SSH menu.
- **Per-User Home Library Hiding** — Admins can pick, per user (including themselves), which media libraries are hidden from the Emby client home screen (grouped by server, whole-server hiding supported); only the home entries are hidden — search, Latest Additions and Continue Watching stay unaffected, and changes take effect on the next refresh.
- **Independent User Accounts** — Regular users possess independent watch progress, played status, favorites, and "Continue Watching / Next Up", isolated from other users and upstream shared accounts; Admins retain original upstream behavior. (Queries that **filter** lists by "Favorite / Played / Resumable" are answered from those same local records; a few filters such as "Unplayed" are still decided by the upstream account — see [Independent Watch History](#independent-watch-history))
- **Concurrent Playback Limits** — Each upstream server can configure a maximum concurrent playback count (`maxConcurrent`). Playback requests exceeding this limit return 429; Auto-releases based on heartbeat timeout.
- **Role-Based Access Control** — Admins have full access to all servers and the management panel; Regular users can only access their assigned servers and cannot access the admin API.
- **Multi-Server Aggregation** — Merges and displays media libraries and search results from multiple servers. Uses Goroutine concurrent requests with configurable grace periods — fast servers return first, slower servers contribute within the grace window; timed-out data is silently backfilled in the background, so aggregation latency depends on the fastest server plus grace period rather than the slowest. When an upstream goes offline, already-aggregated content automatically falls back to other online servers via OtherInstances — Resume and NextUp remain unaffected.
- **Smart Deduplication & Prioritization** — Identical videos are automatically merged with multiple version sources retained; Supports a 4-level metadata priority logic (Designated Tag > Chinese > Length > Order) to smartly pick the best display information.
 - **Client Identity Passthrough** — The proxy talks to upstreams with the identity captured when the admin logs in with a real client (passthrough), and `custom` mode can independently define all 5 Emby client identity headers per upstream. The proxy itself no longer ships any fabricated client identity.
- **Network Proxy Pool** — Configure dedicated HTTP/HTTPS proxies separately for each upstream server, complete with a built-in one-click connectivity tester.
- **Dual Playback Modes** — Proxy mode (traffic relayed, hides upstream, supports HLS/segments) or Redirect mode (302 redirects to upstream, saves proxy machine bandwidth; **it exposes the upstream account credential — see [Playback Mode Explained](#playback-mode-explained)**).
- **Token Management & Session Stability** — Proxy tokens never expire (only removed on logout, password change, or manual revocation), preventing frequent 401 errors on long-idle devices; upstream tokens auto-recover via async re-login with 30-second debounce when expired; admin password changes automatically revoke all issued tokens.
 - **Passthrough Delayed Login** — Upstream servers in passthrough mode (password or API key) never log in or send requests before a real client identity has been captured; they authenticate automatically once the admin logs in once with a real client, avoiding phantom device records on upstream Emby.
- **Full Control & Operations** — Built-in modern SSH CLI menu and Web admin panel; comes with persistent logs and SQLite ID mapping. SSH menu auto-detects Binary/Docker deployment mode, dispatching all operations to systemd or Docker Compose commands accordingly.
- **Built-in Web Client** — Browse libraries, search, manage favorites and continue watching in your browser without installing a separate client. Supports seasons, multiple source versions and independent watch progress, following the regular user's permissions.
- **Browser Playback & Subtitles** — Select versions, audio tracks and subtitles, with seeking, keyboard shortcuts, volume and fullscreen controls. Supports common text subtitles, ASS and PGS, and can attempt upstream HLS when original-media playback fails. Compatibility depends on the browser, device and upstream capabilities.

---

## Quick Installation

Choose one route. For an existing deployment, check its directory and ports first; never let two instances share writable configuration or data.

| Installation method | Use case | Local compilation required? |
| --- | --- | --- |
| **Method 1: Release binary (primary recommendation)** | Standard Linux deployment managed by systemd | No |
| **Method 2: Prebuilt Docker image** | Container-based deployment and management | No |
| **Method 3: Docker source build** | Code changes or custom image builds | Yes |
| **Method 4: Run Go source** | Development and debugging | Yes |

> **Version selection:** The current stable release is **V1.4.3**; the current release candidate is **V1.4.4-rc3**. The binary installer defaults to the latest stable Release; prereleases must be selected explicitly. Parental controls in rc3 need further testing; see the [changelog](Update.md) for limitations. Releases and images are published independently; a Release does not guarantee a matching image.

### Method 1: Release Binary One-Click Install (Primary Recommendation)

Downloads the precompiled binary for your CPU architecture; no Go build environment is needed.

```bash
curl -fsSL -o release-install.sh https://raw.githubusercontent.com/ArizeSky/Emby-In-One/main/release-install.sh
sudo bash release-install.sh
```

To try the current release candidate:

```bash
sudo bash release-install.sh V1.4.4-rc3
```

The script initializes `/opt/emby-in-one/{config,data,log}`, generates a random administrator password, and installs a systemd service. Save the initial password shown in the terminal and follow the installation output to access the admin panel. Run `emby-in-one` to open the SSH management menu later.

> The historical official V1.4.3 binary reports `dev` through `--version`; this does not mean the development branch was installed.

### Method 2: Prebuilt Docker Image (No Local Compilation)

**`ghcr.io/arizesky/emby-in-one:v1.4.4-rc3`** is published for **linux/amd64 and linux/arm64**. Docker selects the matching architecture automatically.

Get the current repository's installation tools and explicitly select image mode:

```bash
git clone https://github.com/ArizeSky/Emby-In-One.git
cd Emby-In-One

sudo bash install.sh \
  --mode image \
  --image ghcr.io/arizesky/emby-in-one:v1.4.4-rc3
```

This route only pulls the prebuilt image and **does not compile the project source**. The installer resolves and pins the image digest; subsequent updates stay on the image route.

- Default deployment directory: `/opt/emby-in-one`
- Default listener: `127.0.0.1:8096`, not directly exposed to the Internet
- Administrator username: `admin`
- Initial random password: saved in `.eio-initial-password` in the deployment directory, readable only by root and not printed to installation logs

Read the initial password and check service status:

```bash
sudo cat /opt/emby-in-one/.eio-initial-password
sudo bash /opt/emby-in-one/eio-docker.sh status
```

> Use the installation tools from the current main branch; the old rc3 Release source assets do not include the new Docker installer and manager changes. No `latest` tag is currently provided; select a version explicitly. See [Docker deployment and operations](DOCKER_EN.md) for manual Compose, digest pinning, updates and recovery.

### Method 3: Docker Source Build (For Developers)

Use the complete project source when changing code or building a custom image:

```bash
git clone https://github.com/ArizeSky/Emby-In-One.git
cd Emby-In-One

sudo bash install.sh --mode source
```

This route compiles on the local machine. Allow enough build resources and access to base images and dependency repositories. Ordinary source builds default to `dev`, not an official Release identity.

> If you only want to run the project, prefer Method 1 or Method 2. See [Docker deployment and operations](DOCKER_EN.md) for manual builds and Compose configuration.

### Method 4: Run Go Source (For Developers)

Requires Go 1.23+ and a C toolchain for SQLite's CGO support (on Debian/Ubuntu, install the C toolchain with `sudo apt install build-essential`).

```bash
mkdir -p config data
# Create config/config.yaml using the Configuration Reference below
go test ./...
go run ./cmd/emby-in-one
```

### Access After Installation

- **Admin panel:** `/admin/`, using an administrator account.
- **Web client (current release candidate):** `/web/`, using a regular user created by the administrator. Administrator accounts cannot sign in to the Web client.
- **Emby clients:** use the address through which you actually expose the service.

Managed Docker installs listen only on localhost by default. For initial remote access, create an SSH tunnel from your own computer:

```bash
ssh -L 18096:127.0.0.1:8096 user@server-ip
```

Then open `http://127.0.0.1:18096/admin/`. Binary deployments listen on port `8096` by default and can also be accessed through an SSH tunnel.

For regular use, expose the service through an [HTTPS reverse proxy](#reverse-proxy-nginx). To make Docker listen on another interface directly, explicitly set `--bind` and configure access controls; do not expose the admin panel without protection.

> For legacy Node.js V1.2.1 installation and usage, see the [legacy documentation](README_V1.2.1.md). The repository's `legacy/` directory is reference-only and is not part of Go builds or installation.

---

## Reverse Proxy (Nginx)

This project itself listens on `8096` (HTTP). In production, place it behind Nginx and let Nginx terminate TLS and forward. Below is a complete, ready-to-use configuration.

### 1. Enable `trustProxy` in the backend first

Behind Nginx, the real client IP can only be obtained from `X-Real-IP` / `X-Forwarded-For`; the server address returned by `/System/Info/Public` depends on `X-Forwarded-Proto` / `X-Forwarded-Host`. **Only when `trustProxy: true` does the backend trust these headers**; otherwise login rate limiting is computed from Nginx's IP (all visitors behind the same IP share the quota), and the external address falls back to the internal Host.

`config/config.yaml`:

```yaml
server:
  port: 8096
  trustProxy: true
```

> Security prerequisite: enable `trustProxy` only when "the backend can be reached solely by your own Nginx". If `8096` is exposed directly to the public internet, anyone can forge `X-Real-IP` to bypass login rate limiting.

### 2. Nginx configuration

`/etc/nginx/conf.d/emby-in-one.conf` (in the `http {}` context, which `conf.d` is by default):

```nginx
upstream emby_in_one {
    server 127.0.0.1:8096;
    keepalive 32;
}

# HTTP -> HTTPS
server {
    listen 80;
    listen [::]:80;
    server_name media.example.com;
    return 301 https://$host$request_uri;
}

server {
    listen 443 ssl;
    listen [::]:443 ssl;
    http2 on;                    # nginx < 1.25.1: change to: listen 443 ssl http2;
    server_name media.example.com;

    ssl_certificate     /etc/letsencrypt/live/media.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/media.example.com/privkey.pem;
    ssl_protocols       TLSv1.2 TLSv1.3;
    ssl_ciphers         HIGH:!aNULL:!MD5;
    ssl_session_cache   shared:SSL:10m;

    # Request body: the backend itself limits API requests to 2 MiB; leave headroom so Nginx does not return 413 first
    client_max_body_size 16m;
    # Emby token / UA headers are long
    large_client_header_buffers 4 16k;

    # ---- Forward to Emby-In-One ----
    proxy_http_version 1.1;
    proxy_set_header Host              $host;
    proxy_set_header X-Real-IP         $remote_addr;
    proxy_set_header X-Forwarded-For   $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_set_header X-Forwarded-Host  $host;
    # Empty Connection enables upstream keepalive (backend has no WebSocket, no Upgrade needed)
    proxy_set_header Connection        "";

    # Long playback: relax timeouts
    proxy_connect_timeout 60s;
    proxy_send_timeout    3600s;
    proxy_read_timeout    3600s;
    send_timeout          3600s;

    # Do not buffer or spool media streams: send as received, ensuring seek and long playback
    proxy_buffering         off;
    proxy_request_buffering off;
    proxy_max_temp_file_size 0;

    # Pass through Range / If-Range (Nginx forwards client headers by default; written out explicitly for clarity)
    proxy_set_header Range    $http_range;
    proxy_set_header If-Range $http_if_range;

    # ⚠ Never hide the following response headers the backend sets for /web/, or the WASM player's
    #   SharedArrayBuffer (multi-threaded decoding) will break:
    #     Cross-Origin-Opener-Policy: same-origin
    #     Cross-Origin-Embedder-Policy: require-corp
    #   Do not configure proxy_hide_header Cross-Origin-*.

    location / {
        proxy_pass http://emby_in_one;
    }
}
```

Replace `media.example.com` and the certificate paths, then reload:

```bash
sudo nginx -t && sudo systemctl reload nginx
```

Then visit `https://media.example.com/web` (Web client) and `https://media.example.com/admin` (admin panel).

### 3. Key points

- **HTTPS is required**: the `/web` player relies on `SharedArrayBuffer`, which is only available in a secure context (HTTPS or localhost); a plain HTTP deployment degrades WASM multi-threaded decoding.
- **Do not strip COOP/COEP**: these two headers are set by the backend for `/web/` and are a prerequisite for `SharedArrayBuffer`; Nginx passes them through by default — just do not add `proxy_hide_header`.
- **Streaming must disable buffering**: `proxy_buffering off` + a large `proxy_read_timeout` keep HLS/large direct files flowing as received, so seeking and long playback are not interrupted.
- **`trustProxy` and rate limiting**: when enabled, login rate limiting is computed from the real client IP; when disabled, all requests through Nginx share a single IP quota.
- **WebSocket**: the backend currently does not implement WebSocket, so real-time push / remote-control features degrade for Emby clients through this proxy; no extra Upgrade configuration is needed on the Nginx side.
- **HTTP only (no TLS)**: if you really want no TLS, remove the 80→443 redirect, change the `listen 443 ssl...` server block to `listen 80;` and drop the `ssl_*` directives; but you lose WASM multi-threading (see above).

---

## System Requirements

**Release Binary Deployment (Recommended):**
- Linux (amd64 / arm64 / arm / mips / mipsle / riscv64)
- No Go compilation environment needed, directly run pre-compiled binaries

**Docker Deployment:**
- Docker 20.10+, Docker Compose v2; published images target linux/amd64 and linux/arm64
- Pulling needs no Go/C toolchain; local builds need extra CPU/RAM and access to base images/package repositories, separate from runtime requirements
- Linux: Debian 11/12/13, Ubuntu 22/24 (recommended), other distros need self-verification
- Windows / macOS can also run (for dev and testing)

**Go Source Build:**
- Go 1.23+
- C Toolchain (CGO used for SQLite): Debian/Ubuntu run `apt install build-essential`

---

## Configuration Reference

The config file is located at `config/config.yaml` (mounted into the container at `/app/config/config.yaml` when using Docker).

```yaml
# dataDir: "/opt/emby-in-one/data"    # Runtime data directory (top-level key; defaults are in "Data Directory" below)

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
  login: 30000           # Upstream login timeout (ms) — applies to login and API-key validation; exceeding it fails the login
  healthCheck: 30000     # Health check timeout (ms) — applies to reconnect probes of offline servers
  healthInterval: 60000  # Health check interval (ms)
  searchGracePeriod: 3000     # Search aggregation grace period — wait for other servers after first result (ms), 0 disables it
  metadataGracePeriod: 3000   # Metadata fetch grace period (ms), 0 disables it
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
    spoofClient: "passthrough"                  # passthrough | custom | resident
    streamingUrls:                               # Streaming lines (optional, ordered; a single one can also be written as streamingUrl: "...")
      - "https://cdn.example.com"                # 1st entry is the primary line
      - "https://backup.example.com"             # the rest are fallbacks
    followRedirects: true                      # Follow upstream 301/302/303/307/308 (default true; when false the redirect is reported as an upstream error instead of being forwarded to the client)
    proxyId: null                              # Associate with proxy ID from proxy pool
    priorityMetadata: false                    # Prefer using this server's metadata when merging
    maxConcurrent: 3                           # Max concurrent playbacks, 0 means unlimited (affects regular users only)

  - name: "Server C (custom spoof example)"
    url: "https://emby-c.example.com"
    apiKey: "your-api-key"
    spoofClient: "custom"
    customUserAgent: "MyClient/1.0 (Windows; Win64; x64)"
    customClient: "MyClient"
    customClientVersion: "1.0.0"
    customDeviceName: "My Device"
    customDeviceId: "your-custom-device-id"
```

Settings modified in the admin panel take effect hotly, no service restart required. The one exception is the global default playback mode, which is only the initial value for a new upstream — see Playback Modes.

### Reverse Proxy Trust (`trustProxy`)

| Value | Behavior | Applicable Scenario |
|-------|----------|--------------------|
| `false` (default) | Login rate limiting uses the TCP connection IP (`RemoteAddr`) | Directly exposed to the internet, no reverse proxy |
| `true` | Login rate limiting trusts `X-Real-IP` / `X-Forwarded-For` headers | Deployed behind Nginx / Caddy or other reverse proxies |

> **Important**: If your Emby-In-One instance is behind a reverse proxy (Nginx, Caddy, Cloudflare, etc.), you **must** add `trustProxy: true` under the `server` section in `config.yaml`. Otherwise all client requests will appear to come from the same IP, and after 5 failed login attempts all users will be rate-limited for 15 minutes.

> **Precondition: the reverse proxy must _overwrite_ these headers.** This program prefers `X-Real-IP` and otherwise takes the **first** entry of `X-Forwarded-For`. Nginx's most common form, `proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;`, **appends** — whatever the client sent stays first, so anyone can forge an IP: rotate fake IPs to bypass the login rate limit, or aim one at a victim to lock that IP out for 15 minutes.
>
> Overwrite instead:
> ```nginx
> proxy_set_header X-Real-IP $remote_addr;
> proxy_set_header X-Forwarded-For $remote_addr;
> ```
> As long as the proxy sets `X-Real-IP` (preferred here, and not forgeable by appending), the rate limit can be trusted.
>
> **Conversely: when there is no trusted reverse proxy in front, it must stay `false`.** `trustProxy: true` makes the server take `X-Real-IP` / `X-Forwarded-For` on faith, with no check of where the request came from. If the instance is directly exposed to the internet (or nothing along the path overwrites those headers), anyone can supply an arbitrary IP: rotate a fake IP on every failed login to bypass the failure counter and the 15-minute lockout on `POST /Users/AuthenticateByName`, or put someone else's IP there to lock that IP out.
>
> The test is simple: **enable it only when the last hop that can reach this service is certain to be your own reverse proxy.** If you are not sure, leave it off.

### Data Directory (`dataDir`)

`dataDir` is a **top-level key** in the config file (a sibling of `server`, `admin` and `playback`) that decides where runtime data is written.

| Item | Value |
|------|-------|
| Default | `/app/data` when that directory exists (the official Docker image creates it); otherwise `data/` under the process working directory |
| Contents | `mappings.db` (virtual ID mappings, user data, watch history), `tokens.json` (proxy-layer tokens), `captured-headers.json` (passthrough client headers), `emby-in-one.log` (log file) |

> **This key has nothing to do with the config file itself.** `config.yaml` always lives at `config/config.yaml` (`/app/config/config.yaml` inside the Docker container) and does not move with `dataDir`. See [Data Directory Description](#data-directory-description) for what each file holds.

**When to change it**:

- **Binary / source deployments**: `data/` is resolved against the **process working directory**. If the service starts from a directory other than the project directory (for example a systemd `WorkingDirectory` of `/opt/emby-in-one`) and you want the data pinned to an absolute path, or mounted separately from `config/`, set `dataDir` explicitly.
- **Docker deployments**: the container already defaults to `/app/data`, and `docker-compose.yml` mounts the host's `./data` there, so you normally **do not** need to change it; only a custom mount point would require it.
- **Migrating / reusing existing data**: point `dataDir` at the directory that already holds your data — no need to move files by hand.

**Notes**:

- Setting it in the config file is enough (`dataDir: "/opt/emby-in-one/data"`); the admin panel does not expose it, and a **service restart** is required;
- Prefer an **absolute path** in production — a relative path follows the startup working directory and can look like data loss when it is really just a different directory being read and written;
- The directory must be readable and writable by the process user.

---

## Multi-User Management

V1.4 adds multi-user support, allowing admins to create multiple regular users, each independently configurable with accessible upstream servers.

### Role Descriptions

| Role | Permissions |
|------|-------------|
| Admin (admin) | Can access all servers, admin panel, admin API; Watch history shared with upstream servers |
| Regular User (user) | Can only access assigned servers; Has independent watch history (isolated from other users and upstream accounts) |

> **Tokens expire immediately when a user is disabled or deleted**: besides revoking the user's issued tokens, the proxy re-checks the account status on every request carrying a user-role token — a disabled or deleted account gets 401 right away, without waiting for the token to age out.

### Independent Watch History

Because all distributed users share the same upstream Emby account, upstream watch progress, played status, and favorites are shared. Starting from V1.4, a regular user's watch data is **local state**, isolated from the upstream account:

| Feature | Admin | Regular User |
|---------|-------|--------------|
| Resume (Continue Watching) | Upstream server data | Local independent data |
| Next Up | Upstream server data | Calculated based on local progress |
| Played Status | Upstream server data | Local independent record |
| Favorite | Upstream server data | Local independent record |
| UserData in browsing pages | Direct passthrough from upstream | Overlay local state over it |
| List filtering (favorites / played / resumable) | Upstream server filtering | Local record filtering |

**List filtering (local since V1.4.4):**

- `Filters=IsFavorite`, `IsPlayed` and `IsResumable` are answered from the **local records**: the proxy fetches the candidate set for that container, intersects it with the local set, then sorts, pages and recounts locally. "Favorites only" therefore returns that user's own favorites, including items favorited locally that the upstream account never favorited. Container constraints such as `ParentId`, `Recursive` and `IncludeItemTypes` are still applied upstream.
- Sorting: `SortName`, `DateCreated`, `ProductionYear` and `CommunityRating` are sorted locally; any other sort key (e.g. `DatePlayed`) degrades to the local "recently played / favorited" order.
- Unrecognized filter values (e.g. `IsFolder`) are forwarded upstream untouched, so the client's intent is never silently dropped.

> **Known limitation**: `Filters=IsUnplayed` is a **complement** ("unwatched = everything minus watched") and the local records only hold items the user has touched, so there is no way to enumerate the full set — it is still decided by the shared upstream account. `IsFavoriteOrLiked` is the same story: the "liked" state is not recorded locally. When such a filter is used, the proxy returns the `X-Emby-In-One-Filter-Notice` response header and writes one WARN log line (at most one per filter per minute) saying so.

**Paging:** the aggregated list without a `ParentId` (`GET /Users/{id}/Items` with no container) is paged by the proxy **after** merging and deduplicating, so `TotalRecordCount` is the merged total and `StartIndex` counts in merged order. Local filtering works the same way: the candidate set is fetched first, then sorted and paged locally.

> One more trade-off: both of those paths fetch a **candidate set** from the upstream (rather than letting it filter and return one page), capped at 5000 items per request; past the cap the reported total is only a lower bound and the tail of the list may be unreachable, which is logged.

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

> **The global `playback.mode` is only the initial value for a new upstream.** Once an upstream exists its `playbackMode` has already been written with the value of that moment, so changing the global default later does **not** affect any existing upstream (same for the "default playback mode" field at the top of the panel). To change one upstream's mode, use the playback-mode dropdown in that server's edit dialog — it takes effect immediately.

> ⚠ **Security warning for `redirect` (direct playback) mode**: direct playback mode writes the upstream account credential (`api_key`) into the `302` redirect link, and **any user who can play can extract it** — including users restricted by `AllowedServers` — then bypass this proxy and reach the upstream directly (equivalent to upstream admin rights). Therefore:
>
> - Use a **dedicated, restricted account for that upstream** (grant only the media-library playback permissions it needs, no admin rights, and cap concurrency where possible); never reuse the upstream's admin account or a shared one;
> - Once leaked, the credential can only be revoked by changing the password or revoking the API key on the upstream side;
> - If that risk is unacceptable, keep the default `proxy` mode.

An upstream can configure **multiple streaming lines** (`streamingUrls`, an ordered list): the first entry is the primary line, the rest are fallbacks. All lines must point to the same Emby server (multiple lines are multiple routes to one server, not mirrored servers — transcoding sessions live on the server itself, so switching lines across mirrors causes 404s).

- **Proxy mode**: on a connect-level failure of the primary line (connection refused / timeout / TLS error) the proxy automatically switches to the next fallback, invisibly to the client. Any HTTP status returned by the upstream (including 404/403) is not treated as a line failure.
- **Redirect mode**: the line is chosen by liveness — every health-check cycle probes fallback lines at the connect level (any HTTP response counts as alive, including the 403/404 returned by split-tunnel reverse proxies that only forward `/Videos/` and `/Audio/`). A line marked dead is skipped for 60 seconds, then becomes a candidate again. After the 302 the traffic no longer passes through the proxy; a line failure mid-playback is handled naturally when the player re-fetches the manifest.
- When left empty the stream base equals `url` (the front-end address), same as a single `streamingUrl`.

### Client Identity Explained (`spoofClient`)
 
 Controls what client identity the proxy communicates with the upstream server. Affects login, API requests, health checks, and stream proxying.
 
 | Value | User-Agent | X-Emby-Client | Usage Scenario |
 |-------|------------|---------------|----------------|
 | `passthrough` (default) | Real client UA captured at admin login | Real client value | Servers with client whitelists; all users share the captured identity |
 | `custom` | Custom value (required) | Custom value | Servers needing complete control over client markings |
| `resident` | Fixed resident identity | Resident identity value | Servers that need multiple servers to share one stable, controllable identity |
 
 > **Note**: The `official` mode from V1.2 has been automatically migrated to `custom` in V1.3, using the original Emby Web official client's default values. As of V1.4.4 the built-in `none` (Emby Aggregator) and `infuse` (fabricated Infuse) modes are removed, and existing configurations migrate to `passthrough` automatically.
 >
 > **Current behavior**: In `custom` mode, the configured `User-Agent`, `X-Emby-Client`, `X-Emby-Client-Version`, `X-Emby-Device-Name`, and `X-Emby-Device-Id` are applied to upstream login, normal API requests, health checks, image proxying, and stream proxying. After saving in the admin panel, these values are persisted to the config file and correctly restored when editing the upstream again. `customUserAgent` is required — a custom identity without a User-Agent would degrade into a UA-less outbound request, which is exactly the behaviour that was removed.
 
#### Passthrough Mode Principles
 
 Passthrough uses a four-level header resolution to present the real client identity to the upstream; when no level yields anything it **fabricates nothing and sends nothing** — login is deferred until a real client connects:
 
 1. **Live Request Header** — If the current request carries the `X-Emby-Client` header (a genuine Emby client), direct usage.
 2. **Current Token Captured Header** — When a real client (Infuse, Emby iOS, etc.) logs in to Emby-in-One, the proxy captures and stores the client's `User-Agent`, `X-Emby-Client`, `X-Emby-Device-Name`, etc. based on the proxy Token; future requests heavily tied to the same Token will reuse these.
 3. **Server's Last Successful Login Header** — Every time a passthrough server successfully logs in, the complete headers used are remembered and persisted. Will be used straight after reboot, without waiting for users to re-login.
 4. **Most Recent Captured Header** — If the current request lacks a Token and the server has no historical successful records, it uses the lastly captured header from any Token.
 
 Emby header fields the client did not send stay empty — the proxy presents exactly what the client actually sent, with no built-in base identity layered on top.
 
 When a client logs in, all offline passthrough servers automatically use the newly captured headers to attempt re-login (API-key upstreams included: the `/Users/Me` validation carries the captured identity and records it on success). The successfully logged-in headers are persistently stored per-server; both health checks and reconnections post-reboot will use that server's last successful headers. Upon token revocation or expiration, its respective captured headers are cleaned as well.

#### Resident Mode (`spoofClient: resident`)

`resident` mode lets one or more upstream servers share a single **resident client identity** saved by the administrator. The identity comes from the "UA Management" page: after the administrator logs in to this proxy with a real client, the proxy records the 10 most recent full client identities (`User-Agent`, `X-Emby-Client`, `X-Emby-Client-Version`, `X-Emby-Device-Name`, `X-Emby-Device-Id`, `Accept`, `Accept-Language`), and the first capture automatically saves one resident identity; afterwards the administrator can save, rename, update, or delete resident identities in the panel and bind one to a server via "Assign" (config key `uaProfileId`).

- The resident identity is fixed on outbound: login, API, images, health checks, and stream proxying all use the same identity, unaffected by live requests, token cache, or the latest capture.
- A resident identity captured more than 30 days ago still works normally; it only hints that you may capture a newer UA, and is never auto-disabled or deleted.
- A resident identity referenced by a server cannot be deleted directly; unbind it or rebind to another resident identity first.
- Example config:

```yaml
upstream:
  - name: "Server A"
    url: "https://emby-a.example.com"
    username: "user"
    password: "pass"
    spoofClient: "resident"
    uaProfileId: "the resident UA ID (see UA Management)"
```

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

Each upstream Item ID is mapped globally to a lone virtual ID — 16 random bytes (128 bits) from `crypto/rand`, rendered as a 32-character lowercase hex string with no dashes. Any IDs visible to clients are virtual.

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
emby-in-one --reset-password <new-password|-> [--force]
# Or use the SSH menu option "Change Admin Password" (the menu stops the service, resets, then starts it again)
```

  - A password of `-` is **read from stdin**, so it never shows up in the process list (`ps`) or the shell history — this is the form the install and management scripts use: `printf '%s' "$pass" | emby-in-one --reset-password -`
  - By default it first probes `127.0.0.1:<port from config.yaml>/System/Info/Public` and **refuses to run while the service is still up**, telling you to `systemctl stop emby-in-one` first (see below for why)
  - `--force` skips that probe; use it only when you are sure you need it
  - The reset clears `tokens.json` with an **atomic write** (keeping `_proxyUserId`), so a truncated file can no longer stop the service from booting; **every issued proxy token is invalidated** and clients must sign in again

  > A running instance can rewrite tokens from memory: verify stop before resetting. Managed Docker deployments use menu/manager `reset-password` with stdin; do not bypass the guard with force.

- **Stricter `data/tokens.json` permissions**: Written with `0600` permissions on Unix/Linux
- **Secure `config.yaml` writes**: Atomic replacement + `0600` permissions, reducing corruption risk and preventing other users from reading passwords
- **Request body size limit**: All API request bodies limited to 2MB (`http.MaxBytesReader`), preventing malicious large requests from consuming memory
- **Login rate limiting**: After 5 consecutive login failures from the same IP, locked for 15 minutes with `429 Too Many Requests`; atomic operations prevent TOCTOU race conditions; supports real IP detection behind reverse proxies (`X-Real-IP` / `X-Forwarded-For` / IPv6)
- **`trustProxy` only behind a trusted reverse proxy**: login rate limiting counts by IP, and with `server.trustProxy: true` the server takes the first entry of `X-Real-IP` / `X-Forwarded-For` **without checking where it came from**. Enabling it with no trusted reverse proxy in front hands the rate-limit key to the client — an attacker can rotate forged IPs to bypass the 5-failure lockout, or aim one at a victim to lock that IP out. See [Reverse Proxy Trust](#reverse-proxy-trust-trustproxy) for the configuration details
- **Unauthenticated image endpoint (a known trade-off, not an oversight)**: `GET /Items/{itemId}/Images/{imageType}` **does not require a token**. Clients embed image URLs in their UI and cache them for a long time, so requiring auth would leave posters broken across the board once a token rotates or a cache entry expires. Its safety rests on the **virtual ID being a capability URL**: the `itemId` in the path is a 128-bit `crypto/rand` value that only a user who actually fetched that item's metadata can know. Two consequences worth stating plainly: (1) **anyone who obtains that URL can fetch that image**, even without a token — so an unauthenticated request skips the `AllowedServers` check (authenticated requests are still checked as usual); (2) the fetch goes upstream under that upstream's shared identity. If per-user, revocable image access is needed, this can move to short-lived signed URLs
- **Graceful shutdown**: On receiving `SIGINT` / `SIGTERM`, drains active connections (up to 10 seconds) before closing the HTTP server and health check timers
- **Admin panel CSP**: the admin panel returns a strict `Content-Security-Policy` - `default-src 'self'`, with no third-party origin and no `'unsafe-inline'` in `script-src` / `style-src` / `font-src` / `connect-src`. Vue, lucide, the compiled Tailwind CSS and the Inter font are all self-hosted under `public/vendor/`, so the panel loads nothing from and connects to nothing on an external origin. Only `'unsafe-eval'` remains, which Vue needs to compile its in-DOM template at runtime
- **Stream URL cache auto-eviction**: `IDStore` stream URL cache entries expire after 4 hours, cleaned every 30 minutes, preventing unbounded memory growth during long-running operation
- **Proxy connectivity test SSRF protection**: The admin panel's proxy test endpoint includes DNS rebinding protection, blocking connections to private/reserved IP addresses (`127.x`, `10.x`, `172.16-31.x`, `192.168.x`, etc.); the pre-check DNS lookup is bounded by the request context plus a 5-second cap, so a stuck resolver fails open quickly (the connect-time check remains the enforcement point) instead of hanging the admin request
- **Log file permissions**: `data/emby-in-one.log` and its rotated backups are written `0600` (same class as `tokens.json`); files left world-readable by older builds are tightened on open and rotation. Windows keeps its default
- **Credential redaction in logs and errors**: `user:pass@` credentials inside proxy URLs are redacted before reaching log lines or error messages; outbound request logs redact sensitive query parameters (tokens, passwords) the same way
- **Immediate token invalidation on user disable/delete**: besides revoking a user's issued tokens, the proxy re-checks the account status on every request carrying a user-role token — a disabled or deleted account gets 401 right away
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
- Up to 10MB per file, retaining 3 rotated backups (`emby-in-one.log.1` ~ `.3`), auto-rotation
- Capable of being downloaded and cleared inside the admin panel (clearing also deletes the backups)
- Permissions: written `0600` on Unix/Linux (logs can carry parameterized URLs), rotated backups included; Windows keeps its default

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
  - LOG_MAX_SIZE_MB=10   # per-file size limit (MB), default 10
  - LOG_KEEP=3           # rotated backups to keep, default 3
```

---

## Admin Panel

Access `http://your-ip:8096/admin`, logging in with the admin credentials from the config file.

| Page | Functions |
|------|-----------|
| **System Overview** | Online server count, ID mapping count, storage engine (SQLite) |
| **Upstream Nodes** | Add / edit / delete / reconnect servers, drag-and-drop ordering; Supports configuring maximum concurrent playback (`maxConcurrent`); each row has an aggregation toggle |
| **User Mgmt** | Create, edit, enable/disable, delete regular users; Visually configure accessible servers |
| **Network Proxies** | HTTP/HTTPS proxy pool management, supports one-click connectivity testing |
| **Global Settings** | System name, default playback mode, admin account, timeout & grace period configuration |
| **Runtime Logs** | Real-time log viewing, supports level filtering (ERROR/WARN/INFO/DEBUG), keyword search, downloading raw log files, and clearing logs |

> The admin panel sidebar displays the current running version number. For adding/editing `spoofClient: passthrough` upstreams (password or API key), if there is no captured client identity available, the admin API will still save the configuration but return a warning, keeping the upstream as offline until the admin logs in once with a real client and triggers automatic retry; a brand-new deployment can also switch to `custom` mode and define the identity explicitly.

**Upstream aggregation switch**: each row in the upstream node list has an aggregation toggle (keyboard accessible). When off, the server keeps its configuration, online checks, manual reconnect, UA binding and history, but no longer participates in content aggregation for `/web` and Emby clients (libraries, search, latest, resume, next-up, cross-server copies, playback version lists, etc.), and admin content access is equally restricted; already-started playback progress/stop, `PlayingItems` delete and transcoding cleanup still work under authorization only. The switch takes effect immediately — no restart, no re-login, no full upstream reconnect. The persisted field is `aggregationDisabled` (default / `false` means aggregation is on), bound to the persistent server ID, unaffected by reordering or renaming.

### Admin API

All APIs require authentication (`X-Emby-Token` header or `api_key` query parameter). For security reasons, `/admin/api/*` endpoints only open to same-origin requests, denying unhindered cross-origin acceptances.

| Method | Path | Description |
|--------|------|-------------|
| GET | `/admin/api/status` | System status |
| GET | `/admin/api/upstream` | List upstream servers |
| POST | `/admin/api/upstream` | Add upstream server |
| PUT | `/admin/api/upstream/:index` | Modify upstream server |
| DELETE | `/admin/api/upstream/:index` | Delete upstream server (auto-cleans ID mappings) |
| POST | `/admin/api/upstream/:index/reconnect` | Reconnect upstream server |
| PATCH | `/admin/api/upstream/:id/aggregation` | Toggle upstream aggregation for that server |
| GET | `/admin/api/ua` | List recent captures, resident UAs, and server bindings |
| POST | `/admin/api/ua/resident` | Save a recent capture as a resident UA |
| PATCH | `/admin/api/ua/resident/:id` | Rename a resident UA |
| POST | `/admin/api/ua/resident/:id/replace` | Update the resident identity from a recent capture |
| DELETE | `/admin/api/ua/resident/:id` | Delete a resident UA (returns 409 if referenced by a server) |
| POST | `/admin/api/ua/resident/:id/assignments` | Batch assign/unbind server bindings |
| POST | `/admin/api/upstream/reorder` | Adjust server ordering |
| GET | `/admin/api/proxies` | List proxies |
| POST | `/admin/api/proxies` | Add proxy |
| POST | `/admin/api/proxies/test` | Test proxy connectivity |
| DELETE | `/admin/api/proxies/:id` | Delete proxy |
| GET | `/admin/api/settings` | Get global settings |
| PUT | `/admin/api/settings` | Modify global settings |
| GET | `/admin/api/logs?limit=500` | Get in-memory logs |
| GET | `/admin/api/logs/download` | Download the persistent log file |
| DELETE | `/admin/api/logs` | Clear logs |
| GET | `/admin/api/client-info` | Get captured client info |
| GET | `/admin/api/users` | List all regular users |
| POST | `/admin/api/users` | Create a regular user |
| PUT | `/admin/api/users/:id` | Modify a regular user |
| DELETE | `/admin/api/users/:id` | Delete a regular user (auto-clears watch data) |
| POST | `/admin/api/logout` | Admin logout |

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
- View logs
- Uninstall service (supports preserving config and data)

> New Docker menus identify source/image through `.eio-deployment.json` and read the actual runtime version. `EIO_PROJECT_DIR` selects the project; unknown/custom deployments cannot auto-upgrade. Binary/systemd management remains independent. See [Docker guide](DOCKER_EN.md).

---

## Data Directory Description

Runtime directories:

- `config/` — Stores config file `config.yaml`
- `data/` — Stores runtime data:
  - `mappings.db` — Virtual ID mappings, additional instances interactions, user data (UserStore), and watch history (WatchStore)
  - `tokens.json` — Proxy layer token storage
  - `captured-headers.json` — Passthrough client headers persistence
  - `emby-in-one.log` — Log file

The actual location of `data/` can be changed with the top-level [`dataDir`](#data-directory-datadir) key in the config file.

---

## FAQ

### Passthrough Upstream Login Failure (403)

 On a fresh installation no client identity has been captured yet, so passthrough upstreams stay offline and wait for a capture (no fabricated identity is used to log in). If the upstream requires a specific client identity:
 1. Log into Emby-in-One using any Emby client (Infuse, Emby iOS, etc.) with the **admin** account
 2. The proxy will automatically capture the client header arrays and retry login over the passthrough server
 3. Upon a successful log-in, this server's specific client identity will become persisted; requiring no manual re-attempts after future restarts
 4. Monitor logs mapping `source` fields to verify which header source was used (`last-success` = last successful headers, `override` = retry using captured headers, `no-identity` = no captured identity, login deferred)
 5. If the naturally captured client UA itself is also rejected by the upstream, explicitly log in using an allowed client to capture appropriate identities safely
 6. On a brand-new deployment where a seed login is not possible yet, switch to `custom` mode and define the identity explicitly

### Upstream Shows Offline / Login Timeout

`timeouts.login` (30s default) and `timeouts.healthCheck` (30s default) used to have no effect — every request inherited `timeouts.api`. Both are enforced now, and this is the most likely reason an upstream goes offline right after an upgrade:

- A login that takes longer than `timeouts.login` fails → the server shows offline. Raise it in "Global Settings" for upstreams that need 10–30s to log in
- Health-check probes only run for **offline** servers and are bounded by `timeouts.healthCheck`; a timeout merely means that round did not recover, and the next round retries — online servers are never marked offline by it
- These values can only tighten the timeout below `timeouts.api`; raise `api` as well to allow longer probes
- `Timeouts are now enforced` in the startup log means your config carries values below `api`

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
3. Verify the reverse proxy **overwrites** `X-Real-IP` or `X-Forwarded-For` — with the appending form `$proxy_add_x_forwarded_for` a client can forge its IP (bypassing the limit, or locking someone else out). See [Reverse Proxy Trust](#reverse-proxy-trust-trustproxy)

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
│   ├── middleware.go               # HTTP middleware (CORS, logging, status capture, CSP)
│   ├── ssrf.go                     # SSRF policy and safe dialers (proxy probe / upstreams)
│   ├── auth.go                     # Proxy token issuance & validation
│   ├── auth_context.go             # Per-request auth context injection & extraction
│   ├── auth_manager.go             # Upstream auth management (login/session/API Key)
│   ├── identity.go                 # Client identity capture & Passthrough 4-level resolution
│   ├── identity_persistence.go     # Per-upstream client identity persistence
│   ├── ua_library.go               # UA library core (recent capture / resident UA, fingerprint & validation)
│   ├── ua_library_persistence.go   # UA library persistence (atomic JSON read/write)
│   ├── user_store.go               # Multi-user storage (CRUD, password hashing, memory index + SQLite)
│   ├── handlers_admin.go           # Admin API handlers (upstream server CRUD)
│   ├── handlers_admin_home_libraries.go  # Per-user home-library hiding admin endpoint & library cache
│   ├── handlers_admin_ua.go        # UA library admin API (recent capture / resident UA / assignments)
│   ├── handlers_admin_aggregation.go  # Aggregation switch admin API (PATCH /admin/api/upstream/:id/aggregation)
│   ├── admin_ua_assignments.go     # Resident UA to server binding & targeted upstream client rebuilds
│   ├── handlers_system.go          # System info endpoints (/System/Info)
│   ├── handlers_user.go            # User login rate limiting & user-related handlers
│   ├── handlers_web.go             # /web/ static assets (disk-first, embedded fallback, COOP/COEP)
│   ├── root_page.go                # Root landing page (/ dual-entry card: web client + admin panel)
│   ├── admin_validation.go         # Admin input validation & helper utilities
│   ├── idstore.go                  # SQLite bidirectional ID mapping (virtual ↔ original)
│   ├── id_rewriter.go              # Recursive ID virtualization/devirtualization rewriting
│   ├── query_ids.go                # Batch query ID resolution
│   ├── identifier_lookup.go        # Multi-source identifier lookup (each store under its lock)
│   ├── media.go                    # Media aggregation, dedup, metadata priority selection
│   ├── aggregation.go              # Common aggregation framework (grace period + background backfill)
│   ├── aggregation_policy.go       # Upstream aggregation switch policy (config-only, immediate effect)
│   ├── media_items.go              # Media item queries (multi-upstream fan-out merge)
│   ├── media_resume.go             # Resume Items proxy & multi-upstream merge
│   ├── media_nextup.go             # Next Up proxy & multi-upstream merge
│   ├── media_playback.go           # PlaybackInfo query & concurrent playback limit check
│   ├── media_source.go             # MediaSource selection & playback session ownership
│   ├── media_stream.go             # Video/audio stream proxy (virtual ID route resolution)
│   ├── item_detail.go              # Merged item detail (multi-instance MediaSource aggregation)
│   ├── item_instances.go           # OtherInstances discovery
│   ├── show_items.go               # Episode list merging for shows (merge before paging)
│   ├── library_image.go            # Image proxy (cache headers)
│   ├── library_filter.go           # Per-request hidden-library resolution (home hiding)
│   ├── library_visibility.go       # Per-user home-library visibility storage (admin included)
│   ├── user_filter.go              # Local user-state filters (IsFavorite/IsPlayed/IsResumable)
│   ├── series_userdata.go          # Series-level watch history isolation (Resume/NextUp)
│   ├── session_userdata.go         # Sessions/Playing progress reporting
│   ├── session_cleanup.go          # PlayingItems session-slot release (bypasses aggregation filter)
│   ├── watch_store.go              # Per-user watch progress storage & persistence
│   ├── playback_limiter.go         # Concurrent playback limiter (heartbeat timeout auto-release)
│   ├── login_limiter.go            # Per-IP login failure limiter (evicts instead of blocking)
│   ├── streamproxy.go              # HTTP stream proxy (backpressure, HLS relative path rewriting)
│   ├── stream_probe.go             # Streaming-line liveness probes (redirect-mode line choice)
│   ├── hls_segment_proxy.go        # VOD segment timeline rewriting & oversized-segment streaming fallback
│   ├── hls_segment_time.go         # fMP4 timeline parse/rewrite (tfdt/sidx, seek timeline restore)
│   ├── fallback_proxy.go           # Fallback route: scan URL/Query for virtual IDs
│   ├── healthcheck.go              # Parallel health checks
│   ├── logger.go                   # Leveled logging (Console + File dual output + rotation)
│   ├── logfile.go                  # Rotating log file (0600 mode, backup shifting & tightening)
│   ├── privatefile.go              # Private file mode (0600) & tightening of legacy files
│   ├── atomicfile.go               # Atomic file writes (temp file + rename)
│   ├── atomicfile_owner_unix.go    # Owner preservation on atomic replace (Unix)
│   ├── atomicfile_owner_windows.go # Owner-preservation no-op for Windows
│   ├── db_migration.go             # SQLite schema migration (table/column probing & backfill)
│   ├── scrypt_local.go             # Admin password scrypt hashing
│   ├── sqlite_cgo.go               # CGO embedded SQLite compilation & low-level bindings
│   ├── upstream.go                 # Upstream connection pool & concurrent request orchestration
│   ├── upstream_outbound.go        # Outbound request-header building (identity injection, timeouts, redirect policy)
│   ├── upstream_resident_identity.go  # Apply resident identity snapshot to outbound requests (spoofClient: resident)
│   ├── upstream_ua_updates.go      # Targeted reconnect jobs for UA changes (prepare/publish/reconnect)
│   ├── upstream_auth_state.go      # Consistent snapshot of an upstream's auth state
│   ├── outbound_identity.go        # Outbound identity carrier rewriting & naming
│   ├── outbound_identity_policy.go # Outbound auth modes & identity rules
│   ├── outbound_errors.go          # Outbound error normalization (preparation vs transport)
│   ├── outbound_log.go             # Outbound request logs (sensitive parameter redaction)
│   └── outbound_diagnostics.go     # Outbound diagnostics scanning (bounded input)
├── third_party/sqlite/             # SQLite CGO source dependency
├── public/
│   ├── embed.go                    # go:embed directive (compiles admin.html, admin.js, vendor/ and web/ into the binary)
│   ├── admin.html                  # Vue 3 + Tailwind CSS admin panel template
│   ├── admin.js                    # Vue 3 application logic (extracted from admin.html)
│   ├── web/                        # Built-in Web client (/web/, Vue 3 global build, no bundler)
│   │   ├── web.html                #   Single-page application shell
│   │   ├── app.js                  #   App entry (routing, page assembly, player wiring)
│   │   ├── data.js                 #   Shared data operations (API wrappers)
│   │   ├── ui.js                   #   Shared UI state factories (toast, recent searches)
│   │   ├── page-state.js           #   Page request controllers (AbortController + generations)
│   │   ├── library-state.js        #   Library page controller (grid/list, bounded paging)
│   │   ├── ui-components.js        #   Reusable Vue components
│   │   ├── player-ui.js            #   Player control-layer state (state machine, menus, shortcuts)
│   │   ├── player-libmedia.js      #   Player engine (native MP4, libmedia streaming, HLS fallback)
│   │   ├── subtitle-controller.js  #   Track selection & subtitle clocks
│   │   ├── subtitles.js            #   Subtitle overlays (SRT/VTT/PGS, ASS rendering)
│   │   └── ui.css                  #   Web client styles
│   └── vendor/                     # Self-hosted frontend dependencies: Vue, lucide, Tailwind output, Inter font;
│                                   # web/ holds the libmedia player, the JASSUB subtitle engine and Noto Sans SC
├── tests/                          # Node front-end behavior tests (node --test, 10 files / 67 tests)
│   ├── web-ui-harness.cjs          #   Shared VM harness (Vue stubs, scripts loaded in web.html order)
│   ├── make-web-fixtures.py        #   ffmpeg synthetic sample generation (samples stay out of releases)
│   └── *.test.cjs                  #   data / ui / player / player-ui / subtitles / mse-seek /
│                                   #   mse-duration / seek-queue / session / admin-panel
├── assets/panel.css                # Tailwind input (holds the panel styles moved out of admin.html)
├── tailwind.config.js              # Tailwind content config (run npm run build:panel after editing admin.html/admin.js)
├── package.json                    # Root package.json provides build:panel / test scripts (Node deps moved to legacy/)
├── Dockerfile                      # Go runtime container build
├── docker-compose.yml              # Local source build
├── docker-compose.image.yml        # Published-image deployment (explicit image/digest)
├── DOCKER.md / DOCKER_EN.md         # Docker deployment, upgrades, backups and rollback
├── RELEASING.md                    # Public release policy and workflow limitations
├── install.sh                      # Source repo one-click deploy script (Docker)
├── release-install.sh              # Release binary one-click deploy script (systemd)
├── emby-in-one-cli.sh              # SSH terminal management menu script
└── legacy/                         # V1.2.1 Node.js implementation: reference only, built into nothing
    ├── README.md                   #   Why it is kept, and why it takes part in no build
    ├── src/                        #   Old Express implementation (the reference for the Go ID virtualization)
    ├── tests/                      #   Old Node tests (most no longer pass)
    └── package.json                #   Node dependencies (only used by npm --prefix legacy install)
```

---

## Star History

[![Star History Chart](https://api.star-history.com/svg?repos=ArizeSky/Emby-In-One&type=Date)](https://star-history.com/#ArizeSky/Emby-In-One&Date)

---

## License

GNU General Public License v3.0
