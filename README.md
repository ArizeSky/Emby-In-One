# Emby-In-One

> **Version: V1.4.0**

[更新日志](Update.md) | [English README](README_EN.md) | [安全策略](SECURITY.md) | [更新计划](Update%20Plan.md) | [V1.2.1 旧版文档](README_V1.2.1.md) | [GitHub](https://github.com/ArizeSky/Emby-In-One)

基于Go语言实现的多台 Emby 服务器聚合代理，将多个上游 Emby 服务器的媒体库合并为一个统一入口，支持任何标准 Emby 客户端访问。支持多用户管理、独立观看历史、UA伪装、并发播放数限制和角色权限隔离。

## 测试站点

[演示站点](https://emby.cothx.eu.cc/)
Emby连接地址：https://emby.cothx.eu.cc/
账号：admin
密码：5T5xF4oMxcnrcCPA

## 预览

![预览图1](https://cdn.nodeimage.com/i/D293pIQcFNx4gXkfskPbnXFzmgCQ1JPx.webp)
![预览图2](https://cdn.nodeimage.com/i/iDAXrYaIXdm9efhwl2BtqJjRUmGfTSKU.webp)
![预览图3](https://cdn.nodeimage.com/i/K4jhTTMjv8rkHYiPNbXKUC0kXIzAXgq0.webp)
![预览图4](https://cdn.nodeimage.com/i/jCilzHTw7vzRJYaQFtbvd8ZOEaTxZvk6.webp)

> 图床服务由 [NodeImage](https://www.nodeimage.com) 提供，感谢支持。

---

## 快速安装

> **旧版 Node.js 部署说明**：如果您希望部署基于 Node.js 的 V1.2.1 稳定版，请前往本仓库的 [Releases 页面](https://github.com/ArizeSky/Emby-In-One/releases) 下载 V1.2.1 的 Source code 源码压缩包，解压后同样运行 `bash install.sh` 即可。

本项目优先推荐在 Linux 服务器直接使用 Release 二进制部署 V1.4.0（无需本地编译）；Docker 方式适合希望自行构建镜像的场景。

### 方式一：Release 二进制一键安装（首推）

```bash
curl -fsSL -o release-install.sh https://raw.githubusercontent.com/ArizeSky/Emby-In-One/main/release-install.sh
sudo bash release-install.sh
```

可选：指定版本安装。

```bash
sudo bash release-install.sh V1.3.0
```

该脚本会自动完成：
- 按 CPU 架构下载对应 Release 二进制（无需本地编译 Go）
- 初始化 `/opt/emby-in-one/{config,data,log}` 并首次生成随机管理员密码
- 拉取 `admin.html`、`admin.js` 与 `emby-in-one-cli.sh` 配套资源（二进制已内嵌管理面板，外部文件为可选覆盖更新）
- 安装并启动 `systemd` 服务（`emby-in-one`），支持开机自启
- 若检测到旧版本，自动备份并执行可回滚升级

### 方式二：源码仓库一键安装脚本（推荐开发者/希望本地构建镜像）

```bash
git clone https://github.com/ArizeSky/Emby-In-One.git
cd Emby-In-One
bash install.sh
```

脚本将为您自动安装 Docker 环境、分配随机管理员密码、构建 Go 版镜像并启动服务。后续如需管理，通过 SSH 输入 `emby-in-one` 即可呼出管理菜单。

### 方式三：手动 Docker Compose 部署

1. 创建项目目录：
```bash
mkdir -p /opt/emby-in-one/{config,data}
cd /opt/emby-in-one
```
2. 拷贝本仓库下的所有核心文件（包括 `go.mod`, `cmd/`, `internal/`, `public/`, `Dockerfile`, `docker-compose.yml` 等）至该目录。
3. 创建初始配置文件 `config/config.yaml`：
```yaml
server:
  port: 8096
  name: "Emby-In-One"
  # trustProxy: true        # 部署在反向代理（Nginx/Caddy 等）后面时设为 true

admin:
  username: "admin"
  password: "your-strong-password" # 首次启动后自动加密存储

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
4. 构建并启动：
```bash
docker compose build
docker compose up -d
```

### 方式四：Go 源码直接运行（适合开发者）

环境要求：Go 1.23+ 且具备 C 编译链（Debian/Ubuntu 运行 `apt install build-essential`）。
```bash
mkdir -p config data
# 按方式三的说明在 config 文件夹下创建 config.yaml
go test ./...
go run ./cmd/emby-in-one
```

**默认访问地址**：
- Emby 客户端连接地址：`http://服务器IP:8096`
- 管理面板：`http://服务器IP:8096/admin`

---

## 系统要求

**Release 二进制部署（推荐）：**
- Linux（amd64 / arm64 / arm / mips / mipsle / riscv64）
- 无需 Go 编译环境，直接运行预编译二进制

**Docker 部署：**
- Docker 20.10+，Docker Compose v2
- Linux：Debian 11/12/13、Ubuntu 22/24（推荐），其他发行版需自行验证
- Windows / macOS 也可运行（开发测试用）

**Go 源码编译：**
- Go 1.23+
- C 编译链（CGO 用于 SQLite）：Debian/Ubuntu 运行 `apt install build-essential`

---

## 配置文件说明

配置文件位于 `config/config.yaml`（Docker 部署时挂载到容器内 `/app/config/config.yaml`）。

```yaml
server:
  port: 8096
  name: "Emby-In-One"
  # id: 首次启动自动生成，请勿手动修改
  # trustProxy: true        # 部署在反向代理后面时设为 true（见下方说明）

admin:
  username: "admin"
  password: "your-strong-password"    # 首次启动后自动加密存储

playback:
  mode: "proxy"          # "proxy" 或 "redirect"，全局默认值

timeouts:
  api: 30000             # 单次上游 API 请求超时（ms）
  global: 15000          # 聚合请求总超时——等待所有服务器的最大时长（ms）
  login: 10000           # 上游登录超时（ms）
  healthCheck: 10000     # 健康检查超时（ms）
  healthInterval: 60000  # 健康检查间隔（ms）
  searchGracePeriod: 3000     # 搜索聚合宽恕期——收到首个结果后继续等待其他服务器的时长（ms）
  metadataGracePeriod: 3000   # 元数据获取宽恕期（ms）
  latestGracePeriod: 0        # "最新添加"宽恕期——0 表示等待全部服务器（ms）

proxies: []
  # - id: "abc123"
  #   name: "日本代理"
  #   url: "http://user:pass@ip:port"

upstream:
  - name: "服务器A"
    url: "https://emby-a.example.com"
    username: "user"
    password: "pass"

  - name: "服务器B"
    url: "https://emby-b.example.com"
    apiKey: "your-api-key"
    playbackMode: "redirect"                   # 覆盖全局播放模式
    spoofClient: "infuse"                      # none | passthrough | infuse | custom
    streamingUrl: "https://cdn.example.com"    # 独立推流域名（可选）
    followRedirects: true                      # 跟随上游 302 重定向（默认 true）
    proxyId: null                              # 关联代理池中的代理 ID
    priorityMetadata: false                    # 合并时优先使用此服务器的元数据
    maxConcurrent: 3                           # 最大并发播放数，0表示不限制（仅影响普通用户）

  - name: "服务器C（custom 伪装示例）"
    url: "https://emby-c.example.com"
    apiKey: "your-api-key"
    spoofClient: "custom"
    customUserAgent: "Infuse/7.7.1 (iPhone; iOS 17.4.1; Scale/3.00)"
    customClient: "Infuse"
    customClientVersion: "7.7.1"
    customDeviceName: "iPhone"
    customDeviceId: "your-custom-device-id"
```

在管理面板修改的设置会热生效，无需重启服务。

### 反向代理信任 (`trustProxy`)

| 配置值 | 行为 | 适用场景 |
|--------|------|----------|
| `false`（默认） | 登录限速使用 TCP 直连 IP（`RemoteAddr`） | 直接暴露在公网，无反向代理 |
| `true` | 登录限速信任 `X-Real-IP` / `X-Forwarded-For` 头 | 部署在 Nginx / Caddy 等反向代理之后 |

> **重要**：如果您的 Emby-In-One 部署在反向代理后面（Nginx、Caddy、Cloudflare 等），**必须**在 `config.yaml` 的 `server` 段添加 `trustProxy: true`，否则所有客户端请求将被视为来自同一 IP，5 次登录失败后所有用户均会被限速 15 分钟。

---

## 多用户管理

V1.4 新增多用户支持，允许管理员创建多个普通用户，每个用户可独立配置可访问的上游服务器。

### 角色说明

| 角色 | 权限 |
|------|------|
| 管理员 (admin) | 可访问所有服务器、管理面板、管理 API；观看历史与上游服务器共享 |
| 普通用户 (user) | 仅可访问被分配的服务器；拥有独立的观看历史（与其他用户、上游账户隔离） |

### 独立观看历史

由于所有分发用户共享同一个上游 Emby 账户，上游的观看进度、已播放状态和收藏是共享的。V1.4 起，普通用户的观看数据完全隔离在本地 SQLite 数据库中：

| 功能 | 管理员 | 普通用户 |
|------|--------|----------|
| 继续观看 (Resume) | 上游服务器数据 | 本地独立数据 |
| 接下来观看 (NextUp) | 上游服务器数据 | 基于本地进度计算 |
| 已播放状态 (Played) | 上游服务器数据 | 本地独立记录 |
| 收藏 (Favorite) | 上游服务器数据 | 本地独立记录 |
| 浏览页面中的 UserData | 直接透传上游 | 叠加本地状态覆盖 |

**工作原理：**

- 播放事件（开始、进度、停止）会同时写入上游服务器和本地数据库（双写）
- 播放完成（进度 ≥ 90%）自动标记为"已看"
- 标记已播放 / 收藏等用户操作同样双写
- 删除用户时，其本地观看数据自动清除
- 首次播放某项目时，系统自动从上游获取元数据（剧名、季数、集数）以支持 NextUp 计算

### 创建普通用户

管理员可通过以下方式创建和管理普通用户：

1. **管理面板** — 在「用户管理」页面可视化操作
2. **SSH 菜单** — 使用 `emby-in-one` 命令，选择「添加普通用户」或「删除普通用户」
3. **REST API** — `POST /admin/api/users`（需管理员 Token）

### 配置可访问服务器

每个普通用户可被分配一组可访问的上游服务器（通过服务器索引列表）。用户登录后，只能看到和播放被分配服务器上的内容。未分配任何服务器的用户（`allowedServers` 为空列表）将无法访问任何内容。

### 并发播放数限制

每台上游服务器可独立配置 `maxConcurrent`（最大并发播放数）：

- `0`（默认）：不限制
- 正整数：限制该服务器上同时播放的普通用户数量
- 管理员不受此限制
- 超出限制时返回 `429 Too Many Requests`
- 基于 3 分钟心跳超时自动释放占用

---

## 进阶配置与核心原理

### 上游服务器认证（完整机制）

每台上游服务器支持两种认证方式（二选一）：

| 方式 | 配置字段 | 工作原理 |
|------|---------|---------|
| 用户名/密码 | `username` + `password` | 代理向上游调用 `AuthenticateByName` 登录接口换取 Session Token，后续请求复用会话 |
| API Key | `apiKey` | 直接携带 API Key 请求上游，无需登录流程（推荐） |

认证决策与容错逻辑：
- 同一上游若同时填写，优先使用 `apiKey`。
- 登录失败时会记录错误并参与健康检查，不影响其他上游并发聚合。
- 健康检查与自动重连沿用该上游最近一次成功认证上下文。

### 播放模式详解

`playbackMode` 决定媒体流如何交付给客户端。

| 模式 | 工作原理 | 适用场景 |
|------|---------|---------|
| `proxy` | 流量经代理服务器转发。HLS 清单（`.m3u8`）中的分片 URL 会被重写为相对代理路径。支持 Range 请求、字幕、附件。 | 上游无公网 IP；需要对客户端隐藏上游地址；需要兼容反向代理/公网域名 |
| `redirect` | 客户端收到 `302` 重定向，直接连接上游流地址。重定向后流量不经过代理。 | 客户端可直连上游；节省代理带宽 |

**优先级**：单服务器 `playbackMode` > 全局 `playback.mode` > `"proxy"`（默认）

使用 `proxy` 模式时，如果上游有独立的推流域名（CDN 等），可设置 `streamingUrl`，代理会使用该域名构建流地址而非 API 地址。

### UA 伪装详解 (`spoofClient`)

控制代理以什么客户端身份与上游服务器通信。影响登录、API 请求、健康检查和流媒体代理。

| 值 | User-Agent | X-Emby-Client | 使用场景 |
|----|-----------|----------------|---------|
| `none` | 代理默认身份 | `Emby Aggregator` | 大多数服务器——无客户端限制 |
| `passthrough` | 真实客户端 UA（Infuse 兜底） | 真实客户端值 | 有客户端白名单的服务器 |
| `infuse` | `Infuse/7.7.1 (iPhone; iOS 17.4.1; Scale/3.00)` | `Infuse` | 仅允许 Infuse 的服务器 |
| `custom` | 自定义值 | 自定义值 | 需要完全控制客户端标识的服务器 |

> **注**：V1.2 中的 `official` 模式已在 V1.3 中自动迁移为 `custom`，使用原 Emby Web 官方客户端的默认值。

#### Passthrough 模式工作原理

Passthrough 使用五级 header 解析，确保在任何状态下都能向上游提供合理的客户端身份：

1. **实时请求头** — 如果当前请求携带 `X-Emby-Client` 头（真正的 Emby 客户端），直接使用这些头。
2. **当前 Token 的已捕获头** — 当真实客户端（Infuse、Emby iOS 等）登录 Emby-in-One 时，代理会按当前代理 Token 捕获并存储客户端的 `User-Agent`、`X-Emby-Client`、`X-Emby-Device-Name` 等头信息；后续仅由同一 Token 的请求复用。
3. **该服务器上次成功的登录头** — 每台 passthrough 服务器成功登录时，使用的完整 headers 会被记住并持久化。重启后直接使用，无需等待用户重新登录。
4. **最近捕获头** — 如果当前请求无 Token 且该服务器无历史成功记录，使用最近一次任意 Token 的已捕获头。
5. **Infuse 兜底** — 如果没有任何已捕获的客户端头（如全新安装首次启动），使用 Infuse 身份作为安全默认值。

捕获的头会叠加在 Infuse 基础 profile 之上，所以即使客户端没有发送所有 Emby 头字段（如某些第三方 App），也能呈现完整的客户端身份。

当客户端登录时，所有离线的 passthrough 服务器会自动使用新捕获的头重新尝试登录。成功登录的 headers 按服务器维度持久化存储，重启后健康检查和重连均使用该服务器上次成功的 headers。Token 撤销或过期时，其对应捕获头也会一并清理。

### 元数据优先级 (`priorityMetadata`)

当同一影片/集出现在多台服务器上时，代理需要选择一台服务器的元数据（标题、简介、图片）作为"主要"版本。选择规则如下：

| 优先级 | 规则 | 原因 |
|--------|------|------|
| 1 | `priorityMetadata: true` 的服务器 | 手动指定的首选元数据源 |
| 2 | 简介 (Overview) 包含中文字符 | 优先使用中文本地化元数据 |
| 3 | 简介文本更长 | 更完整的描述优先 |
| 4 | 服务器索引更小（配置中排序靠前） | 稳定的兜底规则 |

此优先级仅影响显示哪个元数据——所有服务器的 MediaSource 版本始终保留，用户可自由选择。

### 媒体合并策略

| 内容类型 | 去重依据 | 行为 |
|---------|---------|------|
| **电影** | TMDB ID，或 标题+年份 | 合并为一个条目，包含多个 MediaSource |
| **剧集 (Series)** | TMDB ID，或 标题+年份 | 在剧集层级去重 |
| **季 (Seasons)** | 季号 `IndexNumber` | 按季号去重 |
| **集 (Episodes)** | 季号:集号 | 去重后由上述优先级算法选择最佳元数据 |
| **媒体库 (Views)** | — | 全部保留，追加服务器名后缀区分 |

跨服务器的条目先交错合并（Round-Robin），再去重。

### ID 虚拟化

每个上游 Item ID 被映射为全局唯一的虚拟 ID（UUID 格式）。客户端看到的所有 ID 都是虚拟的。

- **存储**：SQLite（WAL 模式）持久化，配合内存缓存加速访问
- **映射关系**：`virtualId <-> { originalId, serverIndex }`，并额外持久化附加实例关系 `otherInstances`
- **持久化**：重启后无需重新建立映射；主实例与附加实例关系都会恢复
- **清理**：删除上游服务器时自动清理该服务器的所有映射并修正后续索引

---

## 健康检查

- 每 60 秒（可通过 `timeouts.healthInterval` 配置）对所有上游服务器**并行**执行 `GET /System/Info/Public`
- Passthrough 服务器优先使用该服务器上次成功登录的 headers（持久化存储），其次使用最近捕获的客户端头，避免被 nginx 拒绝
- 状态变化时记录日志（ONLINE → OFFLINE / OFFLINE → ONLINE）
- 健康检查定时器在优雅关机（graceful shutdown）时自动清理

---

## 安全加固

- **管理员明文密码启动即哈希化**：Go 后端在服务启动时自动把明文 `admin.password` 迁移为 scrypt 哈希格式，无需等待首次登录
- **支持 CLI 重置密码**：

```bash
emby-in-one --reset-password <new-password>
# 或通过 SSH 菜单选择「修改管理员密码」
```

- **`data/tokens.json` 权限更严格**：Unix/Linux 上按 `0600` 写入
- **`config.yaml` 安全写入**：原子替换方式保存 + `0600` 权限，减少配置损坏风险并防止其他用户读取密码
- **请求体大小限制**：所有 API 请求体限制 2MB（`http.MaxBytesReader`），防止恶意大请求消耗内存
- **登录速率限制**：同一 IP 连续登录失败 5 次后锁定 15 分钟，返回 `429 Too Many Requests`；原子操作避免 TOCTOU 竞态条件；支持反向代理场景下的真实 IP 识别（`X-Real-IP` / `X-Forwarded-For` / IPv6）
- **优雅关机**：收到 `SIGINT` / `SIGTERM` 信号后，先排空当前活动连接（最多等待 10 秒），再关闭 HTTP 服务器和健康检查定时器
- **管理面板 CSP**：Admin 面板返回 `Content-Security-Policy` 头，限制脚本和样式来源，降低 XSS 风险
- **流媒体 URL 缓存自动淘汰**：`IDStore` 中的 `streamURLs` 缓存条目 4 小时后自动过期，每 30 分钟清理一次，防止长期运行后内存无限增长
- **代理连通性测试 SSRF 防护**：管理面板的代理测试接口内置 DNS 重绑定防护，阻止请求连接到私有/保留 IP 地址（`127.x`、`10.x`、`172.16-31.x`、`192.168.x` 等）
- **YAML 注释安全解析**：配置文件解析时正确处理引号内的 `#` 字符，不再错误截断含 `#` 的值

---

## 日志系统

### 日志级别

| 级别 | 输出位置 | 内容 |
|------|---------|------|
| DEBUG | 文件 | 所有请求详情、ID 解析、头信息 |
| INFO | 文件 + 终端 | 登录、服务器状态、配置变更 |
| WARN | 文件 + 终端 | 401/403 响应、服务器掉线 |
| ERROR | 文件 + 终端 | 请求失败、登录失败、异常 |

### 日志文件

- 路径：`data/emby-in-one.log`（Release 部署在 `/opt/emby-in-one/data/`）
- Docker 路径：`/app/data/emby-in-one.log`
- 单文件最大 5MB，保留 1 个旧文件（自动轮转）
- 管理面板可下载和清空

### 日志配置

默认日志级别为 `info`。排查故障时通过环境变量开启完整调试日志：

```bash
LOG_LEVEL=debug FILE_LOG_LEVEL=debug
```

Docker Compose 中设置：

```yaml
environment:
  - LOG_LEVEL=debug
  - FILE_LOG_LEVEL=debug
```

---

## 管理面板

访问 `http://your-ip:8096/admin`，使用配置文件中的 admin 账户登录。

| 页面 | 功能 |
|------|------|
| **系统概览** | 在线服务器数、ID 映射数、存储引擎（SQLite） |
| **上游节点** | 添加/编辑/删除/重连服务器，拖拽排序；支持配置最大并发数 `maxConcurrent` |
| **用户管理** | 创建、编辑、启用/禁用、删除普通用户，可视化配置可访问服务器 |
| **网络代理** | HTTP/HTTPS 代理池管理，支持一键连通性测试 |
| **全局设置** | 系统名称、默认播放模式、管理员账户、超时与宽恕期配置 |
| **运行日志** | 实时日志查看，支持级别筛选（ERROR/WARN/INFO/DEBUG）、关键词搜索、下载原始日志文件、清空日志 |

> 管理面板侧边栏底部显示当前运行版本号。对于 `spoofClient: passthrough` 的新增/编辑，如果当前没有已捕获的客户端身份，管理 API 仍会保存配置，但会返回 warning，并把该上游保留为 offline，等待真实客户端登录后自动重试。

### 管理 API

所有 API 需要认证（`X-Emby-Token` 头或 `api_key` 查询参数）。出于安全考虑，`/admin/api/*` 仅按同源方式开放，不为任意跨域来源返回放行头。

---

## SSH 管理菜单

安装脚本执行完成后，可直接使用：

```bash
emby-in-one
```

可执行：

- 启动 / 重启 / 停止服务
- 在线更新（最新版）/ 下载指定版本
- 查看服务状态、公网 IP
- 查看管理员凭据、修改管理员用户名 / 密码
- 查看用户列表、添加普通用户、删除普通用户
- 查看日志、查看版本号（`--version`）
- 卸载服务（支持保留配置和数据）

> SSH 菜单自动检测当前部署方式（Binary / Docker），所有操作自动分发到 systemd 或 Docker Compose 对应命令。Docker 模式下更新采用源码重建流程。菜单标题栏显示当前版本号。

---

## 数据目录说明

运行时目录：

- `config/` — 保存配置文件 `config.yaml`
- `data/` — 保存以下运行时数据：
  - `mappings.db` — 虚拟 ID 与附加实例映射 + 用户数据（UserStore）+ 观看历史数据（WatchStore）
  - `tokens.json` — 代理层 token 存储
  - `captured-headers.json` — passthrough 客户端头持久化
  - `emby-in-one.log` — 日志文件

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/admin/api/status` | 系统状态 |
| GET | `/admin/api/upstream` | 列出上游服务器 |
| POST | `/admin/api/upstream` | 添加上游服务器 |
| PUT | `/admin/api/upstream/:index` | 修改上游服务器 |
| DELETE | `/admin/api/upstream/:index` | 删除上游服务器（自动清理 ID 映射） |
| POST | `/admin/api/upstream/:index/reconnect` | 重连上游服务器 |
| POST | `/admin/api/upstream/reorder` | 调整服务器顺序 |
| GET | `/admin/api/proxies` | 列出代理 |
| POST | `/admin/api/proxies` | 添加代理 |
| POST | `/admin/api/proxies/test` | 测试代理连通性 |
| DELETE | `/admin/api/proxies/:id` | 删除代理 |
| GET | `/admin/api/settings` | 获取全局设置 |
| PUT | `/admin/api/settings` | 修改全局设置 |
| GET | `/admin/api/logs?limit=500` | 获取内存日志 |
| GET | `/admin/api/logs/download` | 下载持久化日志文件 |
| DELETE | `/admin/api/logs` | 清空日志 |
| GET | `/admin/api/client-info` | 获取已捕获的客户端信息 |
| POST | `/admin/api/logout` | 管理员登出 |

---

## 常见问题

### Passthrough 服务器登录失败 (403)

首次安装时没有客户端身份记录，passthrough 默认使用 Infuse 身份。如果上游 nginx 拒绝 Infuse：
1. 用任意 Emby 客户端（Infuse、Emby iOS 等）登录一次 Emby-in-One
2. 代理自动捕获客户端头并重试 passthrough 服务器登录
3. 成功登录后，该服务器的客户端身份会持久化，后续重启无需再次操作
4. 查看日志中 `source` 字段确认使用了哪个头源（`last-success` = 使用上次成功的 headers，`captured-override` = 登录重试使用已捕获头，`infuse-fallback` = 无捕获头时兜底）
5. 如果捕获的客户端 UA 本身也被上游拒绝，需从上游允许的客户端登录一次以捕获合适的身份

### 只有 Admin 登录能录入客户端 UA

Passthrough 模式下，代理需要捕获真实客户端的 UA / Device 等 Emby 标识头。**只有通过管理员（admin）账户登录时，代理才会捕获并存储这些客户端头信息**。普通用户登录不会触发 UA 捕获。

原因：管理员是唯一直接映射到上游 Emby 账户的角色，只有管理员的登录会话需要与上游保持真实的客户端身份传递。普通用户的请求由代理使用已捕获的管理员客户端身份代为发送。

如果您的 passthrough 上游始终未能自动登录，请确认：
1. 您已使用真实 Emby 客户端（Infuse、Emby iOS 等）以 **admin** 身份登录过 Emby-in-One
2. 查看管理面板「已捕获的客户端信息」确认是否有记录
3. 如需更换客户端身份，以 admin 身份使用目标客户端登录一次即可

### 播放 403 / 401

可能的原因：
- 上游 token 过期 → 在管理面板点击「重连」
- passthrough 服务器的头不完整 → 查看日志中 `Stream headers for [服务器名]` 确认头信息
- 多合一合并后的版本切换 → MediaSourceId 会自动解析到正确的上游服务器

### 首页加载慢 / 媒体库不全

- 默认搜索宽恕期 3 秒——收到第一个服务器的结果后，最多再等 3 秒让其余服务器响应；超时服务器的数据会在后台静默补全
- 如果上游服务器网络延迟普遍较高，可在管理面板「全局设置」或 `config.yaml` 的 `timeouts` 中调大 `searchGracePeriod`、`metadataGracePeriod`
- `latestGracePeriod` 默认为 0（等待全部服务器），如首页"最新添加"加载慢可设为正数
- 查看日志中 `timeout` 或 `abort` 关键词
- 也可适当调大 `api`（单次请求超时）和 `global`（聚合总超时）值

### 忘记管理员密码

管理员密码在首次启动后自动加密存储（scrypt 哈希）。重置方法：

**方法一：编辑配置文件**
1. 编辑 `config/config.yaml`，将 `password:` 后的哈希值改为新的明文密码
2. 重启服务，系统自动将明文密码转为加密格式

**方法二：SSH 管理菜单**
```bash
emby-in-one
# 选择"修改密码"选项
```

### 反向代理用户登录被限速 (429)

如果所有用户在 5 次登录失败后都收到 `429 Too Many Requests`，说明 `trustProxy` 未开启：
1. 在 `config.yaml` 的 `server` 段添加 `trustProxy: true`
2. 重启服务
3. 确认反向代理正确设置了 `X-Real-IP` 或 `X-Forwarded-For` 头

### Docker 容器无法访问上游服务器

- 检查上游 URL 是否使用了 `localhost` → 容器内 localhost 指向容器本身，应改为宿主机 IP 或域名
- 如需访问宿主机服务，使用 `host.docker.internal`（Docker Desktop）或宿主机实际 IP

---

## 免责声明

> **注意**：本项目通过模拟 Emby 客户端行为与上游服务器通信，存在被上游或相关平台识别并封禁账号/API Key 的风险。使用本项目即表示您已自行承担上述风险，对于因使用不当或上游政策调整导致的封号及数据损失，作者不承担任何责任。

---

## 项目架构 (供开发者查阅)

```text
Emby-In-One/
├── cmd/emby-in-one/
│   └── main.go                     # 程序入口
├── internal/backend/
│   ├── config.go                   # YAML 配置加载/保存/校验/原子写入
│   ├── server.go                   # HTTP 服务器启动与优雅关机
│   ├── routes.go                   # 路由注册总表（URL → Handler 映射）
│   ├── middleware.go               # HTTP 中间件（CORS、日志、状态码捕获、SSRF 防护、CSP）
│   ├── auth.go                     # 代理 Token 签发与校验
│   ├── auth_context.go             # 请求级认证上下文注入与提取
│   ├── auth_manager.go             # 上游认证管理（登录/Session/API Key）
│   ├── identity.go                 # 客户端身份捕获与 Passthrough 五级解析
│   ├── identity_persistence.go     # 客户端身份按服务器维度持久化
│   ├── user_store.go               # 多用户存储（CRUD、密码哈希、内存索引 + SQLite）
│   ├── handlers_admin.go           # 管理后台 API 处理器（上游服务器增删改查）
│   ├── handlers_system.go          # 系统信息接口（/System/Info）
│   ├── handlers_user.go            # 用户登录限速与用户相关接口处理器
│   ├── admin_validation.go         # 管理后台输入校验与辅助工具
│   ├── idstore.go                  # SQLite 双向 ID 映射（虚拟 ID ↔ 原始 ID）
│   ├── id_rewriter.go              # 递归 ID 虚拟化/反虚拟化重写
│   ├── query_ids.go                # 批量查询 ID 解析
│   ├── media.go                    # 媒体聚合、去重、元数据优先级选择
│   ├── aggregation.go              # 通用聚合框架（宽恕期 + 后台静默补全）
│   ├── media_items.go              # 媒体条目查询（多上游扇出合并）
│   ├── media_resume.go             # "继续观看"接口代理与多上游合并
│   ├── media_nextup.go             # "接下来观看"接口代理与多上游合并
│   ├── media_playback.go           # PlaybackInfo 查询与并发播放限制检查
│   ├── media_stream.go             # 视频/音频流代理（虚拟 ID 路由解析）
│   ├── library_image.go            # 图片代理（缓存头）
│   ├── series_userdata.go          # 系列级观看历史隔离（Resume/NextUp）
│   ├── session_userdata.go         # Sessions/Playing 进度上报
│   ├── watch_store.go              # 每用户观看进度存储与持久化
│   ├── playback_limiter.go         # 并发播放数限制（心跳超时自动释放）
│   ├── streamproxy.go              # HTTP 流代理（背压、HLS 相对路径重写）
│   ├── fallback_proxy.go           # 兜底路由：扫描 URL/Query 中的虚拟 ID
│   ├── healthcheck.go              # 并行健康检查
│   ├── logger.go                   # 分级日志（Console + File 双输出 + 轮转）
│   ├── scrypt_local.go             # 管理员密码 scrypt 加密
│   ├── sqlite_cgo.go               # CGO 嵌入式 SQLite 编译与底层绑定
│   └── upstream_stub.go            # 上游连接池 & 并发请求编排
├── third_party/sqlite/             # SQLite CGO 源码依赖
├── public/
│   ├── embed.go                    # go:embed 指令（将 admin.html 和 admin.js 编译进二进制）
│   ├── admin.html                  # Vue 3 + Tailwind CSS 管理面板模板
│   └── admin.js                    # Vue 3 应用逻辑（从 admin.html 拆分）
├── Dockerfile                      # Go 环境容器构建
├── docker-compose.yml
├── install.sh                      # 源码仓库一键部署脚本（Docker）
├── release-install.sh              # Release 二进制一键部署脚本（systemd）
├── go_install.sh                   # Go 环境安装辅助脚本
└── emby-in-one-cli.sh              # SSH 终端管理面板脚本
```

---

## 许可证

GNU General Public License v3.0
