# Emby-In-One

> **Version: V1.2.1**

[English](README_EN.md) | [更新日志](Update.md) | [更新计划](Update%20Plan.md) | [GitHub](https://github.com/ArizeSky/Emby-In-One)

多台 Emby 服务器聚合代理，将多个上游 Emby 服务器的媒体库合并为一个统一入口，支持任何标准 Emby 客户端访问。

## 测试站点

[演示站点](https://emby.cothx.eu.cc/)
Emby连接地址：https://emby.cothx.eu.cc/
账号：admin
密码：5T5xF4oMxcnrcCPA

## 预览

![预览图1](https://cdn.nodeimage.com/i/D293pIQcFNx4gXkfskPbnXFzmgCQ1JPx.webp)
![预览图2](https://cdn.nodeimage.com/i/iDAXrYaIXdm9efhwl2BtqJjRUmGfTSKU.webp)
![预览图3](https://cdn.nodeimage.com/i/K4jhTTMjv8rkHYiPNbXKUC0kXIzAXgq0.webp)
![预览图4](https://cdn.nodeimage.com/i/50eO6lJBev4Q5Zb1XhPgVH78kELtR1YK.webp)

> 图床服务由 [NodeImage](https://www.nodeimage.com) 提供，感谢支持。

---

## 功能概览

- **多服务器聚合** — 将多台 Emby 服务器的媒体库、搜索结果、元数据合并展示
- **智能去重** — **仅作用于搜索功能**：搜索时相同影片（基于 TMDB ID 或 标题+年份）自动合并，保留多版本 MediaSource 可选播放；在媒体库页面中点击某个库，数据直接来自该库所属的上游服务器，不做跨库合并
- **系列级观看历史隔离** — 搜索结果中的剧集仍可跨服务器聚合展示，但进入某个剧集后的 `Resume` / `NextUp` 会优先使用该搜索结果所属的主实例；仅当主实例没有有效结果时才顺序回退到同剧的其他实例，不再把多个服务器的观看进度混合显示
- **客户端透传 (Passthrough)** — 将真实客户端身份（UA、设备信息）按代理 Token 隔离透传给上游服务器，避免多设备串用同一份客户端身份；成功登录的客户端身份按服务器维度持久化，重启后无需重新登录
- **UA 伪装** — 支持伪装为 Infuse、Emby Web 官方客户端
- **双播放模式** — 代理模式（流量经代理转发，HLS 清单重写为相对代理路径）/ 直连模式（302 重定向到上游）
- **网络代理池** — 每台上游服务器可独立配置 HTTP/HTTPS 代理
- **持久化日志** — 自动写入文件，5MB 轮转，支持网页下载和清空
- **Web 管理面板** — 服务器增删改查、实时日志查看、全局设置；上游配置采用草稿校验后再提交

---

## 已知问题

- **因Emby服务器服务器不规范导致的剧集混乱**：剧集搜索时的智能去重，由于少数Emby服务器分季不规范，可能导致剧集去重混乱，但不影响顺序和观看。

## 免责声明

> **注意**：本项目通过模拟 Emby 客户端行为与上游服务器通信，存在被上游 Emby 服务器或相关平台识别并封禁账号/API Key 的风险。使用本项目即表示您已知晓并自行承担上述风险，作者不对任何因使用本项目导致的账号封禁、数据丢失或其他损失负责。

---

## 系统要求

**Docker 部署（推荐）：**
- Docker 20.10+，Docker Compose v2
- Linux：Debian 11/12/13、Ubuntu 22/24（推荐），其他发行版需自行验证
- Windows / macOS 也可运行（开发测试用）

**直接运行（Node.js）：**
- Node.js 18+（推荐 20 LTS 或更高）
- `better-sqlite3` 需要编译环境（见 [常见问题](#sqlite-编译失败)）

---

## 安装方式

### 方式一：一键安装脚本（推荐，Linux 服务器）

```bash
git clone https://github.com/ArizeSky/Emby-In-One.git
cd Emby-In-One
bash install.sh
```

脚本会自动：
- 安装 Docker 和 Docker Compose（如未安装，国内网络自动切换阿里云镜像源）
- 创建 `/opt/emby-in-one` 项目目录
- 生成随机管理员密码并写入配置
- 构建镜像并启动容器
- 安装 `emby-in-one` SSH 管理命令

安装完成后在 SSH 终端输入 `emby-in-one` 即可打开管理菜单（启动/停止/重启/查看日志/修改密码等）。

---

### 方式二：Docker Compose 手动部署

```bash
# 1. 创建目录结构
mkdir -p /opt/emby-in-one/{config,data,log}
cd /opt/emby-in-one

# 2. 复制项目文件（src/ public/ package*.json Dockerfile docker-compose.yml）

# 3. 创建配置文件
cat > config/config.yaml << 'EOF'
server:
  port: 8096
  name: "Emby-In-One"

admin:
  username: "admin"
  password: "your-strong-password"    # 首次启动后自动加密存储

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

# 4. 构建并启动
docker compose build
docker compose up -d
```

---

### 方式三：直接用 Node.js 运行（开发 / 测试）

```bash
# 1. 安装依赖
npm install
```

> **注意**: `better-sqlite3` 需要编译环境。Debian/Ubuntu 上执行：
> ```bash
> apt install build-essential python3
> ```
> 如果编译失败，程序会自动降级到内存存储（ID 映射重启后丢失）。

```bash
# 2. 创建配置文件
mkdir -p config
# 编辑 config/config.yaml，参考下方配置说明

# 3. 启动
npm start          # 生产环境
npm run dev        # 开发环境（文件变更自动重启，需 Node.js 18+）
```

启动后访问：
- **管理面板**: `http://localhost:8096/admin`
- **Emby 客户端连接地址**: `http://localhost:8096`

---

## 配置文件说明

配置文件位于 `config/config.yaml`（Docker 部署时挂载到容器内 `/app/config/config.yaml`）。

```yaml
server:
  port: 8096
  name: "Emby-In-One"
  # id: 首次启动自动生成，请勿手动修改

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
    spoofClient: "infuse"                      # none | passthrough | infuse | official
    streamingUrl: "https://cdn.example.com"    # 独立推流域名（可选）
    followRedirects: true                      # 跟随上游 302 重定向（默认 true）
    proxyId: null                              # 关联代理池中的代理 ID
    priorityMetadata: false                    # 合并时优先使用此服务器的元数据
```

在管理面板修改的设置会热生效，无需重启容器。

---

## 上游服务器认证方式

每台上游服务器支持两种认证方式（二选一）：

| 方式 | 配置字段 | 原理 |
|------|---------|------|
| 用户名/密码 | `username` + `password` | 代理通过 `AuthenticateByName` 登录上游，维持 session token |
| API Key | `apiKey` | 直接使用 Emby API Key，无需登录 |

---

## 播放模式详解

`playbackMode` 决定媒体流如何交付给客户端。

| 模式 | 工作原理 | 适用场景 |
|------|---------|---------|
| `proxy` | 流量经代理服务器转发。HLS 清单（`.m3u8`）中的分片 URL 会被重写为相对代理路径，不再依赖 `localhost` 或固定外网域名。支持 Range 请求、字幕、附件。 | 上游无公网 IP；需要对客户端隐藏上游地址；需要兼容反向代理/公网域名 |
| `redirect` | 客户端收到 `302` 重定向，直接连接上游流地址。重定向后流量不经过代理。 | 客户端可直连上游；节省代理带宽 |

**优先级**: 单服务器 `playbackMode` > 全局 `playback.mode` > `"proxy"`（默认）

使用 `proxy` 模式时，如果上游有独立的推流域名（CDN 等），可设置 `streamingUrl`，代理会使用该域名构建流地址而非 API 地址。

---

## UA 伪装详解 (`spoofClient`)

控制代理以什么客户端身份与上游服务器通信。影响登录、API 请求、健康检查和流媒体代理。

| 值 | User-Agent | X-Emby-Client | 使用场景 |
|----|-----------|----------------|---------|
| `none` | 代理默认身份 | `Emby Aggregator` | 大多数服务器——无客户端限制 |
| `passthrough` | 真实客户端 UA（Infuse 兜底） | 真实客户端值 | 有客户端白名单的服务器 |
| `infuse` | `Infuse/7.7.1 (iPhone; iOS 17.4.1; Scale/3.00)` | `Infuse` | 仅允许 Infuse 的服务器 |
| `official` | `Mozilla/5.0 ... Emby/1.0.0` | `Emby Web` v4.8.3.0 | 仅允许官方客户端的服务器 |

### Passthrough 模式工作原理

Passthrough 使用五级 header 解析：

1. **实时请求头** — 如果当前请求携带 `X-Emby-Client` 头（真正的 Emby 客户端），直接通过 `AsyncLocalStorage` 使用这些头。
2. **当前 Token 的已捕获头** — 当真实客户端（Infuse、Emby iOS 等）登录 Emby-in-One 时，代理会按当前代理 Token 捕获并存储客户端的 `User-Agent`、`X-Emby-Client`、`X-Emby-Device-Name` 等头信息；后续仅由同一 Token 的请求复用。
3. **该服务器上次成功的登录头** — 每台 passthrough 服务器成功登录时，使用的完整 headers 会被记住并持久化到 `data/captured-headers.json`。重启后直接使用，无需等待用户重新登录。
4. **最近捕获头** — 如果当前请求无 Token 且该服务器无历史成功记录，使用最近一次任意 Token 的已捕获头。
5. **Infuse 兜底** — 如果没有任何已捕获的客户端头（如全新安装首次启动），使用 Infuse 身份作为安全默认值。

捕获的头会叠加在 Infuse 基础 profile 之上，所以即使客户端没有发送所有 Emby 头字段（如某些第三方 App），也能呈现完整的客户端身份。

当客户端登录时，所有离线的 passthrough 服务器会自动使用新捕获的头重新尝试登录（直接传入已捕获 headers，不依赖请求上下文）。成功登录的 headers 按服务器名持久化存储，重启后健康检查和重连均使用该服务器上次成功的 headers。Token 撤销或过期时，其对应捕获头也会一并清理。

---

## 元数据优先级 (`priorityMetadata`)

当同一集出现在多台服务器上时，代理需要选择一台服务器的元数据（标题、简介、图片）作为"主要"版本。选择规则如下：

| 优先级 | 规则 | 原因 |
|--------|------|------|
| 1 | `priorityMetadata: true` 的服务器 | 手动指定的首选元数据源 |
| 2 | 简介 (Overview) 包含中文字符 | 优先使用中文本地化元数据 |
| 3 | 简介文本更长 | 更完整的描述优先 |
| 4 | 服务器索引更小（配置中排序靠前） | 稳定的兜底规则 |

此优先级仅影响显示哪个元数据——所有服务器的 MediaSource 版本始终保留，用户可自由选择。

---

## 媒体合并策略

| 内容类型 | 去重依据 | 行为 |
|---------|---------|------|
| **电影** | TMDB ID，或 标题+年份 | 合并为一个条目，包含多个 MediaSource |
| **剧集 (Series)** | TMDB ID，或 标题+年份 | 在剧集层级去重 |
| **季 (Seasons)** | 季号 `IndexNumber` | 按季号去重 |
| **集 (Episodes)** | 季号:集号 | 去重后由上述优先级算法选择最佳元数据 |
| **媒体库 (Views)** | — | 全部保留，追加服务器名后缀区分 |

跨服务器的条目先交错合并（Round-Robin），再去重。

---

## ID 虚拟化

每个上游 Item ID 被映射为全局唯一的虚拟 ID（UUID 格式）。客户端看到的所有 ID 都是虚拟的。

- **存储**: SQLite（WAL 模式）优先，不可用时自动降级到内存 `Map`
- **映射关系**: `virtualId <-> { originalId, serverIndex }`，并额外持久化附加实例关系 `otherInstances`
- **持久化**: 重启后无需重新建立映射（使用 SQLite 时）；主实例与附加实例关系都会恢复；内存模式重启后 ID 重置
- **清理**: 删除上游服务器时自动清理该服务器的所有映射并修正后续索引

---

## 健康检查

- 每 60 秒（可通过 `timeouts.healthInterval` 配置）对所有上游服务器**并行**执行 `GET /System/Info/Public`
- Passthrough 服务器优先使用该服务器上次成功登录的 headers（持久化存储），其次使用最近捕获的客户端头，避免被 nginx 拒绝
- 状态变化时记录日志（ONLINE → OFFLINE / OFFLINE → ONLINE）
- 健康检查定时器在 graceful shutdown 时自动清理

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

- 本地运行路径: `data/emby-in-one.log`（或 `log/` 目录，取决于目录结构）
- Docker 路径: `/app/data/emby-in-one.log`
- 单文件最大 5MB，保留 1 个旧文件（自动轮转）
- 管理面板可下载和清空

---

## 管理面板

访问 `http://your-ip:8096/admin`，使用配置文件中的 admin 账户登录。

| 页面 | 功能 |
|------|------|
| **系统概览** | 在线服务器数、ID 映射数、存储引擎（SQLite/Memory） |
| **上游节点** | 添加/编辑/删除/重连服务器，拖拽排序 |
| **网络代理** | HTTP/HTTPS 代理池管理 |
| **全局设置** | 系统名称、默认播放模式、管理员账户、超时配置 |
| **运行日志** | 实时日志查看，支持级别筛选（ERROR/WARN/INFO/DEBUG）、关键词搜索、下载原始日志文件、清空日志 |

### 管理 API

所有 API 需要认证（`X-Emby-Token` 头或 `api_key` 查询参数）。出于安全考虑，`/admin/api/*` 仅按同源方式开放，不再为任意跨域来源返回放行头。

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
| DELETE | `/admin/api/proxies/:id` | 删除代理 |
| GET | `/admin/api/settings` | 获取全局设置 |
| PUT | `/admin/api/settings` | 修改全局设置 |
| GET | `/admin/api/logs?limit=500` | 获取内存日志 |
| GET | `/admin/api/logs/download` | 下载持久化日志文件 |
| DELETE | `/admin/api/logs` | 清空日志 |
| GET | `/admin/api/client-info` | 获取已捕获的客户端信息 |

---

## 常见问题

### Passthrough 服务器登录失败 (403)

首次安装时没有客户端身份记录，passthrough 默认使用 Infuse 身份。如果上游 nginx 拒绝 Infuse：
1. 用任意 Emby 客户端（Infuse、Emby iOS 等）登录一次 Emby-in-One
2. 代理自动捕获客户端头并重试 passthrough 服务器登录
3. 成功登录后，该服务器的客户端身份会持久化到 `data/captured-headers.json`，后续重启无需再次操作
4. 查看日志中 `source` 字段确认使用了哪个头源（`last-success` = 使用上次成功的 headers，`captured-override` = 登录重试使用已捕获头，`infuse-fallback` = 无捕获头时兜底）
5. 如果捕获的客户端 UA 本身也被上游拒绝，需从上游允许的客户端登录一次以捕获合适的身份

### 播放 403 / 401

可能的原因：
- 上游 token 过期 → 在管理面板点击「重连」
- passthrough 服务器的头不完整 → 查看日志中 `Stream headers for [服务器名]` 确认头信息
- 多合一合并后的版本切换 → MediaSourceId 会自动解析到正确的上游服务器

### 首页加载慢 / 媒体库不全

- 默认请求超时 15 秒，聚合超时 20 秒
- 如果上游服务器网络延迟高，部分结果可能被跳过
- 查看日志中 `timeout` 或 `abort` 关键词
- 可在 `config.yaml` 的 `timeouts` 字段适当调大超时值

### 忘记管理员密码

管理员密码在首次启动后自动加密存储。重置方法：

**方法一：编辑配置文件**
1. 编辑 `config/config.yaml`，将 `password:` 后的哈希值改为新的明文密码
2. 重启服务，系统自动将明文密码转为加密格式

**方法二：命令行重置**
```bash
# Docker 部署
docker exec -it emby-in-one node src/index.js --reset-password 新密码

# 直接运行
node src/index.js --reset-password 新密码
```

### SQLite 编译失败

```bash
# Debian / Ubuntu
apt install build-essential python3

# 或者忽略 SQLite，使用内存存储（重启丢失映射）
npm install --ignore-scripts
```

### Docker 容器无法访问上游服务器

- 检查上游 URL 是否使用了 `localhost` → 容器内 localhost 指向容器本身，应改为宿主机 IP 或域名
- 如需访问宿主机服务，使用 `host.docker.internal`（Docker Desktop）或宿主机实际 IP

---

## 项目架构

```
src/
├── index.js                    # 入口：加载配置、初始化、启动服务器
├── config.js                   # YAML 配置加载/保存/校验
├── auth.js                     # 代理层认证（独立于上游 token）
├── emby-client.js              # 单台上游 HTTP 客户端（axios + keepAlive 连接池）
├── id-manager.js               # 双向 ID 映射（SQLite 持久化 + 内存缓存）
├── upstream-manager.js         # 上游管理：并发请求、交错合并、健康检查
├── server.js                   # Express 路由挂载和中间件
├── middleware/
│   ├── auth-middleware.js      # Token 提取和验证
│   └── request-context.js      # 注入 req.resolveId() 等辅助方法
├── routes/
│   ├── system.js               # /System/Info/Public, /System/Ping
│   ├── users.js                # 认证、Views（合并去重）
│   ├── items.js                # Items（ParentId 路由或全量合并）、Resume、Latest、Similar
│   ├── library.js              # Shows/Seasons/Episodes、Search、Genres 等
│   ├── playback.js             # PlaybackInfo（多实例合并）、播放状态上报
│   ├── sessions.js             # Sessions/Playing 进度上报
│   ├── streaming.js            # 视频/音频/HLS/字幕流代理或重定向
│   ├── images.js               # 图片代理（24h 缓存头）
│   ├── fallback.js             # 兜底路由：扫描 URL/Query 中的虚拟 ID
│   └── admin.js                # 管理面板 API + 内存日志 ring buffer
└── utils/
    ├── logger.js               # Winston 日志（Console + File 双 transport）
    ├── id-rewriter.js          # ID 虚拟化/反虚拟化递归重写
    ├── stream-proxy.js         # HTTP 流代理（背压、重定向跟随、HLS 相对路径重写）
    ├── captured-headers.js     # 按 token/服务器捕获客户端请求头（passthrough 持久化）
    ├── cors-policy.js          # Admin/客户端分级 CORS 策略
    └── request-store.js        # AsyncLocalStorage 请求上下文透传

public/
└── admin.html                  # Vue 3 + Tailwind CSS 管理面板 SPA
```

---

## 许可

GNU General Public License v3.0
