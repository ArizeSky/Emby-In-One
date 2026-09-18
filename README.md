# Emby-In-One

> **Version: V1.4.4-rc1 (Pre-release)**

[![License: GPL v3](https://img.shields.io/github/license/ArizeSky/Emby-In-One?color=blue)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.23+-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![SQLite](https://img.shields.io/badge/SQLite-3-003B57?logo=sqlite&logoColor=white)](https://www.sqlite.org/)
[![Docker](https://img.shields.io/badge/Docker-20.10+-2496ED?logo=docker&logoColor=white)](https://www.docker.com/)
[![GitHub Release](https://img.shields.io/github/v/release/ArizeSky/Emby-In-One?color=green)](https://github.com/ArizeSky/Emby-In-One/releases)
[![GitHub Stars](https://img.shields.io/github/stars/ArizeSky/Emby-In-One?style=social)](https://github.com/ArizeSky/Emby-In-One)

[更新日志](Update.md) | [English README](README_EN.md) | [安全策略](SECURITY.md) | [更新计划](Update%20Plan.md) | [V1.2.1 旧版文档](README_V1.2.1.md) | [GitHub](https://github.com/ArizeSky/Emby-In-One)

基于Go语言实现的多台 Emby 服务器聚合代理，将多个上游 Emby 服务器的媒体库合并为一个统一入口，支持任何标准 Emby 客户端访问。支持多用户管理、独立观看历史、UA伪装、并发播放数限制和角色权限隔离。

## 目录

- [测试站点](#测试站点)
- [预览](#预览)
- [功能概览](#功能概览)
- [快速安装](#快速安装)
- [系统要求](#系统要求)
- [配置文件说明](#配置文件说明)
- [多用户管理](#多用户管理)
- [进阶配置与核心原理](#进阶配置与核心原理)
- [健康检查](#健康检查)
- [安全加固](#安全加固)
- [日志系统](#日志系统)
- [管理面板](#管理面板)
- [SSH 管理菜单](#ssh-管理菜单)
- [数据目录说明](#数据目录说明)
- [常见问题](#常见问题)
- [免责声明](#免责声明)
- [项目架构](#项目架构-供开发者查阅)
- [Star History](#star-history)
- [许可证](#许可证)

## 测试站点

[演示站点](https://emby.cothx.eu.cc/)
Emby 连接地址：https://emby.cothx.eu.cc/

> **演示站账号不再随仓库公开。** 出于安全考虑，本仓库不提供明文账号与密码；如需体验，请通过 GitHub [Issues](https://github.com/ArizeSky/Emby-In-One/issues) 联系维护者获取**定期轮换**的临时账号。请勿在公开渠道传播演示站凭据。

## 预览

![预览图1](https://cdn.nodeimage.com/i/D293pIQcFNx4gXkfskPbnXFzmgCQ1JPx.webp)
![预览图2](https://cdn.nodeimage.com/i/iDAXrYaIXdm9efhwl2BtqJjRUmGfTSKU.webp)
![预览图3](https://cdn.nodeimage.com/i/K4jhTTMjv8rkHYiPNbXKUC0kXIzAXgq0.webp)
![预览图4](https://cdn.nodeimage.com/i/jCilzHTw7vzRJYaQFtbvd8ZOEaTxZvk6.webp)

> 图床服务由 [NodeImage](https://www.nodeimage.com) 提供，感谢支持。

---

## 功能概览

- **多用户管理** — 支持创建多个普通用户，每个用户可独立配置可访问的上游服务器；管理员可通过管理面板、REST API 和 SSH 菜单管理用户。
- **每用户首页库隐藏** — 管理员可为每个用户（含自己）单独勾选哪些媒体库不在 Emby 客户端首页显示（按服务器分组、支持整服务器隐藏）；仅隐藏首页库入口，搜索、最新添加、继续观看不受影响，保存后实时生效。
- **独立用户账户** — 普通用户拥有独立的观看进度、已播放状态、收藏和"继续观看 / 接下来观看"，与其他用户及上游共享账户隔离；管理员保持原有上游行为。（按「收藏 / 已播放 / 继续观看中」**筛选**列表的查询同样以本地记录为准；「未观看」等少数筛选仍由上游账户决定，详见[独立观看历史](#独立观看历史)）
- **并发播放数限制** — 每台上游服务器可独立配置最大并发播放数（`maxConcurrent`），超出限制时返回 429；基于心跳超时自动释放占用。
- **角色权限隔离** — 管理员拥有所有服务器和管理面板的完整访问权限；普通用户只能访问被分配的服务器，无法访问管理 API。
- **多服务器聚合** — 合并并展示多台服务器的媒体库与搜索结果。使用 Goroutine 并发请求配合可配置宽恕期——快速服务器优先返回，慢速服务器在宽恕期窗口内继续汇入；超时数据在后台静默补全，聚合延迟取决于最快服务器加宽恕期而非最慢服务器。当某台上游离线时，已聚合内容自动通过 OtherInstances 回退到其他在线服务器——继续观看和接下来观看不受影响。
- **智能去重与优先级** — 相同影片自动合并，保留多版本片源；支持 4 级元数据优先级逻辑（指定标记 > 中文 > 长度 > 顺序）智能选择最佳展示信息。
- **高级 UA 伪装** — 支持 Infuse 伪装（高风险，不建议！）和客户端 UA 透传。还可使用 `custom` 模式为每台上游独立定义全部 5 个 Emby 客户端身份头，绕过常见 Emby UA 限制。
- **网络代理池** — 可为每台上游服务器单独配置 HTTP/HTTPS 代理，内置一键连通性测试。
- **双播放模式** — 代理模式（流量转发、隐藏上游、支持 HLS/分片）或直连模式（302 重定向至上游，节省代理带宽；**会向上游暴露账号凭据，详见[播放模式详解](#播放模式详解)**）。
- **Token 管理与会话稳定** — 代理 Token 永不过期（仅在登出、改密或手动撤销时移除），防止长时间空闲设备频繁 401；上游 Token 过期时通过 30 秒防抖的异步重登录自动恢复；管理员改密后自动撤销所有已签发 Token。
- **Passthrough 延迟登录** — passthrough 模式的上游不再在启动时使用 Infuse 身份尝试登录；而是等待真实客户端连接后再认证，避免在上游 Emby 产生虚假设备记录。
- **全面管控与运维** — 内置现代化 SSH CLI 菜单和 Web 管理面板；配备持久化日志和 SQLite ID 映射。SSH 菜单自动检测 Binary/Docker 部署模式，所有操作自动分发到 systemd 或 Docker Compose 对应命令。

---

## 快速安装

> **旧版 Node.js 部署说明**：如果您希望部署基于 Node.js 的 V1.2.1 稳定版，请前往本仓库的 [Releases 页面](https://github.com/ArizeSky/Emby-In-One/releases) 下载 V1.2.1 的 Source code 源码压缩包，解压后同样运行 `bash install.sh` 即可。 仓库中的 `legacy/` 目录保留了 V1.2.1 的 Node.js 源码，**仅供对照参考**（Go 版的 ID 虚拟化以它为蓝本），它不参与 Go 版的构建、镜像或安装流程，详见 `legacy/README.md`。

本项目优先推荐在 Linux 服务器直接使用 Release 二进制部署 V1.4.4-rc1（无需本地编译；预发布测试版，正式版发布前供尝鲜验证）；Docker 方式适合希望自行构建镜像的场景。

### 方式一：Release 二进制一键安装（首推）

```bash
curl -fsSL -o release-install.sh https://raw.githubusercontent.com/ArizeSky/Emby-In-One/main/release-install.sh
sudo bash release-install.sh
```

可选：指定版本安装。

```bash
sudo bash release-install.sh V1.4.4-rc1
```

该脚本会自动完成：
- 按 CPU 架构下载对应 Release 二进制（无需本地编译 Go）
- 初始化 `/opt/emby-in-one/{config,data,log}` 并首次生成随机管理员密码
- 拉取 `admin.html`、`admin.js` 与 `emby-in-one-cli.sh` 配套资源（二进制已内嵌完整管理面板，含 `public/vendor/` 下的前端依赖；磁盘上缺哪个文件就自动回退到内嵌副本，因此外部文件只是可选覆盖）
- 安装并启动 `systemd` 服务（`emby-in-one`），支持开机自启
- 若检测到旧版本，自动备份并执行可回滚升级

### 方式二：源码仓库一键安装脚本（推荐开发者/希望本地构建镜像）

```bash
git clone https://github.com/ArizeSky/Emby-In-One.git
cd Emby-In-One
bash install.sh
```

脚本将为您自动安装 Docker 环境、分配随机管理员密码、构建 Go 版镜像并启动服务。后续如需管理，通过 SSH 输入 `emby-in-one` 即可呼出管理菜单。

> **说明**：源码仓库安装脚本在 builder 阶段会同时复制 `cmd/`、`internal/`、`third_party/` 和 `public/` 参与 Go 编译。若您自行定制 `Dockerfile` 或手动复制文件，请确保 `public/` 目录也被包含在构建上下文中，否则会在构建时出现 `package emby-in-one/public is not in std` 错误。

### 方式三：手动 Docker Compose 部署

1. 创建项目目录并交给容器运行用户（容器以 uid 1000 运行，挂载目录不可写会导致启动时无法写入 `tokens.json` / `mappings.db` 而失败）：
```bash
mkdir -p /opt/emby-in-one/{config,data}
chown -R 1000:1000 /opt/emby-in-one/config /opt/emby-in-one/data
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
  login: 30000
  healthCheck: 30000
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
# dataDir: "/opt/emby-in-one/data"    # 运行时数据目录（顶层键，默认值见下方「数据目录」说明）

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
  login: 30000           # 上游登录超时（ms）——作用于登录与 API Key 校验，超过即判为登录失败
  healthCheck: 30000     # 健康检查超时（ms）——作用于离线服务器的重连探测
  healthInterval: 60000  # 健康检查间隔（ms）
  searchGracePeriod: 3000     # 搜索聚合宽恕期——收到首个结果后继续等待其他服务器的时长（ms），0 表示禁用
  metadataGracePeriod: 3000   # 元数据获取宽恕期（ms），0 表示禁用
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
    streamingUrls:                               # 推流线路（可选，有序；单条也可写 streamingUrl: "..."）
      - "https://cdn.example.com"                # 第 1 条为主线路
      - "https://backup.example.com"             # 其余为备用线路
    followRedirects: true                      # 是否跟随上游的 301/302/303/307/308（默认 true；false 时按上游错误处理，不把重定向地址转发给客户端）
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

在管理面板修改的设置会热生效，无需重启服务。唯一例外是顶部的「默认播放模式」——它只是**新增上游时的初始值**，不影响已存在的上游，详见「播放模式详解」。

### 反向代理信任 (`trustProxy`)

| 配置值 | 行为 | 适用场景 |
|--------|------|----------|
| `false`（默认） | 登录限速使用 TCP 直连 IP（`RemoteAddr`） | 直接暴露在公网，无反向代理 |
| `true` | 登录限速信任 `X-Real-IP` / `X-Forwarded-For` 头 | 部署在 Nginx / Caddy 等反向代理之后 |

> **重要**：如果您的 Emby-In-One 部署在反向代理后面（Nginx、Caddy、Cloudflare 等），**必须**在 `config.yaml` 的 `server` 段添加 `trustProxy: true`，否则所有客户端请求将被视为来自同一 IP，5 次登录失败后所有用户均会被限速 15 分钟。

> **前提：反向代理必须“覆写”这两个头**。本程序优先取 `X-Real-IP`，没有时取 `X-Forwarded-For` 的**第一段**。而 Nginx 最常见的写法 `proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;` 是**追加**语义——客户端自己送的值会留在最前面，于是任何人都能伪造 IP：既可以不断换假 IP 绕过登录限流，也可以反过来把某个 IP 定向锁死 15 分钟。
>
> 推荐改成覆写：
> ```nginx
> proxy_set_header X-Real-IP $remote_addr;
> proxy_set_header X-Forwarded-For $remote_addr;
> ```
> 只要代理写入了 `X-Real-IP`（本程序优先使用，且该头无法靠追加伪造），限流就值得信任。
>
> **反过来说：前面没有可信反向代理时，必须保持 `false`。** `trustProxy: true` 意味着服务端**无条件**采信请求头里的 `X-Real-IP` / `X-Forwarded-For`，不做任何来源校验。如果实例直接暴露在公网（或链路中没有任何一层会覆写这两个头），任何人都能自行填入任意 IP：每次登录失败换一个假 IP，就能绕过 `POST /Users/AuthenticateByName` 的失败计数与 15 分钟锁定；也可以填上别人的 IP，定向把那个 IP 锁死。
>
> 判断标准很简单：**只有当「能访问到本服务的最后一道入口必然是您自己的反向代理」时才开启它**；不能确定就不要开。

### 数据目录 (`dataDir`)

`dataDir` 是配置文件里的**顶层键**（与 `server`、`admin`、`playback` 同级），决定运行时数据的落盘位置。

| 项 | 值 |
|----|-----|
| 默认值 | 若 `/app/data` 存在（官方 Docker 镜像内置该目录）则用 `/app/data`；否则用进程工作目录下的 `data/` |
| 落盘内容 | `mappings.db`（虚拟 ID 映射、用户数据、观看历史）、`tokens.json`（代理层 token）、`captured-headers.json`（passthrough 客户端头）、`emby-in-one.log`（日志文件） |

> **该键与配置文件本身无关。** `config.yaml` 始终位于 `config/config.yaml`（Docker 容器内为 `/app/config/config.yaml`），不会随 `dataDir` 移动。各文件的说明见[数据目录说明](#数据目录说明)。

**什么时候需要改**：

- **二进制 / 源码部署**：`data/` 是相对**进程工作目录**解析的。如果服务的启动目录不是项目目录（例如 systemd 的 `WorkingDirectory` 指向 `/opt/emby-in-one`），而你想把数据固定到某个绝对路径、或与 `config/` 分开挂载，就显式指定 `dataDir`。
- **Docker 部署**：容器内默认即 `/app/data`，而 `docker-compose.yml` 已把宿主的 `./data` 挂载到这里，通常**不需要**改；只有自定义挂载点时才需要。
- **迁移 / 复用旧数据**：把 `dataDir` 指向已有数据所在目录即可，无需手动搬文件。

**注意事项**：

- 只写在配置文件里即可（`dataDir: "/opt/emby-in-one/data"`），管理面板不提供此项，修改后需**重启服务**生效；
- 生产环境请使用**绝对路径**——相对路径会随启动时的工作目录变化，可能表现为「数据丢失」（实际是换了个目录读写）；
- 该目录需要进程用户可读写。

---

## 多用户管理

V1.4 新增多用户支持，允许管理员创建多个普通用户，每个用户可独立配置可访问的上游服务器。

### 角色说明

| 角色 | 权限 |
|------|------|
| 管理员 (admin) | 可访问所有服务器、管理面板、管理 API；观看历史与上游服务器共享 |
| 普通用户 (user) | 仅可访问被分配的服务器；拥有独立的观看历史（与其他用户、上游账户隔离） |

### 独立观看历史

由于所有分发用户共享同一个上游 Emby 账户，上游的观看进度、已播放状态和收藏是共享的。V1.4 起，普通用户的观看数据以**本地记录**为准，隔离在上游账户之外：

| 功能 | 管理员 | 普通用户 |
|------|--------|----------|
| 继续观看 (Resume) | 上游服务器数据 | 本地独立数据 |
| 接下来观看 (NextUp) | 上游服务器数据 | 基于本地进度计算 |
| 已播放状态 (Played) | 上游服务器数据 | 本地独立记录 |
| 收藏 (Favorite) | 上游服务器数据 | 本地独立记录 |
| 浏览页面中的 UserData | 直接透传上游 | 叠加本地状态覆盖 |
| 列表筛选（只看收藏 / 已观看 / 继续观看中） | 上游服务器筛选 | 本地记录筛选 |

**列表筛选（V1.4.4 起本地化）：**

- `Filters=IsFavorite`、`IsPlayed`、`IsResumable` 由**本地记录**筛选：代理先向该目录取候选集，再与本地集合取交集，随后在本地排序、分页并修正总数——"只看收藏"返回的就是该用户自己的收藏，本地收藏但上游账户没收藏的条目同样会出现。`ParentId`、`Recursive`、`IncludeItemTypes` 等目录约束仍由上游执行。
- 排序：`SortName`、`DateCreated`、`ProductionYear`、`CommunityRating` 在本地排序；其余排序键（如 `DatePlayed`）降级为按本地"最近播放 / 收藏时间"排序。
- 无法识别的筛选值（如 `IsFolder`）原样转发上游，客户端意图不会被静默丢弃。

> **已知限制**：`Filters=IsUnplayed`（未观看）仍是**补集**语义——"未观看 = 全部 − 已观看"，而本地只记录用户碰过的条目，拿不到"全集"，因此它仍由上游共享账户决定；`IsFavoriteOrLiked`（含"喜欢"）同理，本地没有记录"喜欢"状态。使用这些筛选时，代理会返回响应头 `X-Emby-In-One-Filter-Notice` 并写入一条 WARN 日志（同一筛选每分钟最多一条），提示该筛选未按用户隔离。

**分页：** 不带 `ParentId` 的聚合列表（`GET /Users/{id}/Items` 不指定目录）由代理在**合并、去重之后**统一切页，因此 `TotalRecordCount` 是合并后的总数、`StartIndex` 也按合并后的顺序生效。本地筛选同样如此（先取候选集，再本地排序分页）。

> 另一个取舍：上述两条路径都要先向上游取**候选集**（而不是让上游筛完返回一页），单次最多 5000 条；超过上限时总数只是下界、列表尾部可能取不到，并在日志中告警。

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

**优先级**：单服务器 `playbackMode` > 全局 `playback.mode` > `"proxy"`（默认）。

> **全局 `playback.mode` 只是新增上游时的初始值。** 上游一旦建立，它的 `playbackMode` 就已经被写成了当时的值，此后修改全局默认**不会影响任何已存在的上游**（面板顶部的「默认播放模式」同理）。要改变某台上游的模式，请在该服务器的编辑框里用「播放模式」下拉——它是即时生效的。

> ⚠ **`redirect`（直连播放模式）的安全警告**：直连播放模式会把上游账号凭据（`api_key`）写入 302 跳转链接，**任何能播放的用户都可从中提取凭据**——包括被 `AllowedServers` 限制的用户——并绕过本代理直连上游（相当于上游管理员权限）。因此：
>
> - 请**为该上游使用专用的受限账号**（只授予所需的媒体库播放权限、不授予管理权限，必要时限制并发），不要复用上游管理员账号或多人共用的账号；
> - 该凭据一旦泄露，只能在上游侧修改密码或吊销 API Key 才能失效；
> - 无法接受该风险时，请保持默认的 `proxy` 模式。

上游可以配置**多条推流线路**（`streamingUrls`，有序列表）：第 1 条为主线路，其余为备用。所有线路必须指向同一台 Emby 服务器（多条线路 = 到同一服务器的多条路由，而不是多台镜像服务器——转码会话存在服务器本地，跨镜像切换线路会导致 404）。

- **代理模式**：主线路连接失败（连接拒绝/超时/TLS 错误）时自动切换备用线路，客户端无感知；上游返回的任何 HTTP 状态（含 404/403）不视为线路故障。
- **直连模式**：按线路健康状态选择——每个健康检查周期对所有备用线路做连接级探测（任何 HTTP 响应都算存活，包括只转发 `/Videos/`、`/Audio/` 的分流反代返回的 403/404），被标记死亡的线路 60 秒内不再选用，之后自动恢复候选。302 发出后流量不经过代理，播放中途的线路故障由播放器重新拉取清单时自然切换。
- 留空时与 `url`（前端地址）一致，行为与单条 `streamingUrl` 相同。

### UA 伪装详解 (`spoofClient`)

控制代理以什么客户端身份与上游服务器通信。影响登录、API 请求、健康检查和流媒体代理。

| 值 | User-Agent | X-Emby-Client | 使用场景 |
|----|-----------|----------------|---------|
| `none` | 代理默认身份 | `Emby Aggregator` | 大多数服务器——无客户端限制 |
| `passthrough` | 真实客户端 UA（Infuse 兜底） | 真实客户端值 | 有客户端白名单的服务器 |
| `infuse` | `Infuse/7.7.1 (iPhone; iOS 17.4.1; Scale/3.00)` | `Infuse` | 仅允许 Infuse 的服务器 |
| `custom` | 自定义值 | 自定义值 | 需要完全控制客户端标识的服务器 |

> **注**：V1.2 中的 `official` 模式已在 V1.3 中自动迁移为 `custom`，使用原 Emby Web 官方客户端的默认值。
>
> **当前行为**：`custom` 模式下配置的 `User-Agent`、`X-Emby-Client`、`X-Emby-Client-Version`、`X-Emby-Device-Name`、`X-Emby-Device-Id` 会同时应用于上游登录认证、常规 API 请求、健康检查、图片代理和流媒体代理；管理面板保存后会持久化到配置文件，并在再次编辑时正确回填。

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

每个上游 Item ID 被映射为全局唯一的虚拟 ID——由 `crypto/rand` 生成的 16 字节（128 位）随机数构成，对外表现为 32 位小写十六进制字符串（不带连字符）。客户端看到的所有 ID 都是虚拟的。

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
emby-in-one --reset-password <new-password|-> [--force]
# 或通过 SSH 菜单选择「修改管理员密码」（菜单会自动先停服务、重置、再启动）
```

  - 密码传 `-` 时**从 stdin 读取**，不进入进程列表（`ps`）也不进入 shell 历史——安装/管理脚本用的就是这个形式：`printf '%s' "$pass" | emby-in-one --reset-password -`
  - 默认会先探测 `127.0.0.1:<config.yaml 里的 port>/System/Info/Public`：**只要服务还在运行就拒绝执行**，并提示先 `systemctl stop emby-in-one`（原因见下方说明）
  - `--force` 跳过该探测；仅在明确知道自己在做什么时使用
  - 重置时 `tokens.json` 以**原子写**方式清空（保留 `_proxyUserId`），不会再出现文件写坏导致服务起不来的情况；**所有已签发的代理 Token 随之失效**，客户端需要重新登录

  > **为什么运行中必须拒绝**：运行中的实例把 token 存在内存里，会在下一次登录或登出时把整个 `tokens.json` 写回去——刚清掉的令牌会被原样恢复，重置等于没做。所以 CLI 宁可报错也不静默「重置失败」。Docker 部署请用 SSH 菜单，或按菜单失败时打印的命令手动执行（`docker compose ... run --rm -T emby-in-one /app/emby-in-one --reset-password - --force`）。

- **`data/tokens.json` 权限更严格**：Unix/Linux 上按 `0600` 写入
- **`config.yaml` 安全写入**：原子替换方式保存 + `0600` 权限，减少配置损坏风险并防止其他用户读取密码
- **请求体大小限制**：所有 API 请求体限制 2MB（`http.MaxBytesReader`），防止恶意大请求消耗内存
- **登录速率限制**：同一 IP 连续登录失败 5 次后锁定 15 分钟，返回 `429 Too Many Requests`；原子操作避免 TOCTOU 竞态条件；支持反向代理场景下的真实 IP 识别（`X-Real-IP` / `X-Forwarded-For` / IPv6）
- **`trustProxy` 只在可信反代之后开启**：登录限流按 IP 计数，而 `server.trustProxy: true` 时服务端对来源**不做任何校验**就采信 `X-Real-IP` / `X-Forwarded-For` 的第一段。前面没有可信反向代理却开启它，等于把限流的键交给客户端——攻击者可以不断伪造 IP 绕过 5 次失败锁定，也可以定向锁死他人 IP。配置细节见[反向代理信任](#反向代理信任-trustproxy)
- **图片端点免认证（已知取舍，非疏漏）**：`GET /Items/{itemId}/Images/{imageType}` **不要求 token**。原因是客户端会把图片 URL 内嵌进界面并长期缓存，若强制鉴权，token 轮换或缓存失效后客户端会大面积刷不出海报。它的安全性建立在**虚拟 ID 本身就是能力 URL** 之上：URL 里的 `itemId` 是 `crypto/rand` 生成的 128 位随机数，只有真正取到过该媒体元数据的用户才知道它，猜不出来。两点明确含义：(1) **任何拿到该 URL 的人都能取到那张图**，即使他没有 token——因此未认证请求不会经过 `AllowedServers` 白名单校验（已认证请求仍会正常校验）；(2) 取图时使用该上游的共享身份向上游请求。如需按用户签发、可撤销的图片访问控制，可改为短时效签名 URL
- **优雅关机**：收到 `SIGINT` / `SIGTERM` 信号后，先排空当前活动连接（最多等待 10 秒），再关闭 HTTP 服务器和健康检查定时器
- **管理面板 CSP**：Admin 面板返回严格的 `Content-Security-Policy`——`default-src 'self'`，且 `script-src` / `style-src` / `font-src` / `connect-src` 中不含任何第三方源、不含 `'unsafe-inline'`。Vue、lucide、Tailwind CSS 产物与 Inter 字体全部自托管于 `public/vendor/`，面板既不加载也不外连任何外部地址。仅保留 `'unsafe-eval'`（Vue 运行时编译 DOM 内模板所需）
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
- 单文件最大 10MB，保留 3 个备份（`emby-in-one.log.1` ~ `.3`），自动轮转
- 管理面板可下载和清空（清空会连同备份一起删除）

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
  - LOG_MAX_SIZE_MB=10   # 单个日志文件上限（MB），默认 10
  - LOG_KEEP=3           # 保留的轮转备份数，默认 3
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
- 查看日志
- 卸载服务（支持保留配置和数据）

> SSH 菜单自动检测当前部署方式（Binary / Docker），所有操作自动分发到 systemd 或 Docker Compose 对应命令。Docker 模式下更新采用源码重建流程。菜单没有单独的「查看版本」选项——当前版本号直接显示在菜单标题栏上（形如 `Emby In One 管理菜单 v1.4.4`）。

---

## 数据目录说明

运行时目录：

- `config/` — 保存配置文件 `config.yaml`
- `data/` — 保存以下运行时数据：
  - `mappings.db` — 虚拟 ID 与附加实例映射 + 用户数据（UserStore）+ 观看历史数据（WatchStore）
  - `tokens.json` — 代理层 token 存储
  - `captured-headers.json` — passthrough 客户端头持久化
  - `emby-in-one.log` — 日志文件

`data/` 的实际位置可以通过配置文件顶层的 [`dataDir`](#数据目录-datadir) 键改写。

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
| GET | `/admin/api/users` | 列出所有普通用户 |
| POST | `/admin/api/users` | 创建普通用户 |
| PUT | `/admin/api/users/:id` | 修改普通用户 |
| DELETE | `/admin/api/users/:id` | 删除普通用户（自动清除观看数据） |
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

### 上游服务器显示离线 / 登录超时

`timeouts.login`（默认 30 秒）与 `timeouts.healthCheck`（默认 30 秒）此前未真正生效——所有请求都继承 `timeouts.api`。现在两者已生效，升级后如遇上游突然显示离线，多半是这个原因：

- 上游登录耗时超过 `timeouts.login` 即判为登录失败 → 服务器显示离线。登录较慢（10–30 秒）的上游请在「全局设置」调大该值
- 健康检查探测只针对**已离线**的服务器，受 `timeouts.healthCheck` 约束；超时只是"本轮未恢复"，下一轮会重试，不会把在线服务器误判为离线
- 这两个值只能把超时收紧到 `timeouts.api` 以下；若要放宽探测，需同时调大 `api`
- 启动日志里出现 `Timeouts are now enforced` 即表示你的配置中这两个值小于 `api`

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
3. 确认反向代理**覆写**了 `X-Real-IP` 或 `X-Forwarded-For` 头——用追加语义的 `$proxy_add_x_forwarded_for` 会让客户端可以伪造 IP（既能绕过限流，也能定向锁死他人），详见[反向代理信任](#反向代理信任-trustproxy)

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
│   ├── middleware.go               # HTTP 中间件（CORS、日志、状态码捕获、CSP）
│   ├── ssrf.go                     # SSRF 防护策略与安全拨号器（代理测试 / 上游连接）
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
│   ├── login_limiter.go            # 登录失败限流（按 IP；容量满时逐出最旧记录）
│   ├── streamproxy.go              # HTTP 流代理（背压、HLS 相对路径重写）
│   ├── fallback_proxy.go           # 兜底路由：扫描 URL/Query 中的虚拟 ID
│   ├── healthcheck.go              # 并行健康检查
│   ├── logger.go                   # 分级日志（Console + File 双输出 + 轮转）
│   ├── scrypt_local.go             # 管理员密码 scrypt 加密
│   ├── sqlite_cgo.go               # CGO 嵌入式 SQLite 编译与底层绑定
│   └── upstream.go                 # 上游连接池 & 并发请求编排
├── third_party/sqlite/             # SQLite CGO 源码依赖
├── public/
│   ├── embed.go                    # go:embed 指令（将 admin.html、admin.js 与 vendor/ 编译进二进制）
│   ├── admin.html                  # Vue 3 + Tailwind CSS 管理面板模板
│   ├── admin.js                    # Vue 3 应用逻辑（从 admin.html 拆分）
│   └── vendor/                     # 自托管前端依赖：Vue、lucide、Tailwind 产物、Inter 字体
├── assets/panel.css                # Tailwind 输入文件（含从 admin.html 迁出的面板自定义样式）
├── tailwind.config.js              # Tailwind 扫描配置（改动 admin.html/admin.js 后跑 npm run build:panel）
├── package.json                    # 根 package.json 仅保留 build:panel 一个脚本（Node 依赖已移至 legacy/）
├── Dockerfile                      # Go 环境容器构建
├── docker-compose.yml
├── install.sh                      # 源码仓库一键部署脚本（Docker）
├── release-install.sh              # Release 二进制一键部署脚本（systemd）
├── emby-in-one-cli.sh              # SSH 终端管理面板脚本
└── legacy/                         # V1.2.1 Node.js 遗留实现：仅供对照参考，不参与构建 / 镜像 / 安装
    ├── README.md                   #   保留原因与「不参与任何构建」的说明
    ├── src/                        #   旧 Express 实现（Go 版 ID 虚拟化的对照参考）
    ├── tests/                      #   旧 Node 测试（多数已失效）
    └── package.json                #   Node 依赖（只有 npm --prefix legacy install 才会用到）
```

---

## Star History

[![Star History Chart](https://api.star-history.com/svg?repos=ArizeSky/Emby-In-One&type=Date)](https://star-history.com/#ArizeSky/Emby-In-One&Date)

---

## 许可证

GNU General Public License v3.0
