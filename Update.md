# Emby-In-One 更新日志

## V1.4.1

发布日期：2026-04-18

> V1.4.1 为 V1.4.0 的补丁版本，重点修复 UA 伪装与 passthrough 身份透传安全问题，并修正源码仓库 Docker 构建上下文缺失导致的安装失败。

### Bug 修复

- 修复 `custom` UA 伪装模式仅在管理面板可配置、但未真正作用于后端链路的问题：管理 API、配置持久化、运行时请求头生成、登录认证、健康检查、图片代理与流媒体代理现已全部接通；再次编辑上游服务器时也会正确回填已保存的自定义字段
- 修复源码仓库 Docker 安装在 builder 阶段构建失败的问题：builder 现会显式复制 `public/` 目录参与 Go 编译，避免出现 `package emby-in-one/public is not in std` 错误
- 修复 passthrough 模式下剩余的主动登录入口仍可能以 `infuse-fallback` 身份触碰上游的问题：手动“重连上游”和 401/403 后自动恢复登录现已在无真实客户端身份时安全跳过，避免再次以 `Infuse` 身份误碰上游服务器

### 文档与版本同步

- `README.md` / `README_EN.md`：补充 `custom` 模式当前真实行为说明，并补充源码仓库 Docker 构建上下文要求
- `Update.md`：按补丁版本新增独立的 V1.4.1 条目，保留原始 V1.4.0 发布内容
- 对外显示版本统一更新为 **V1.4.1**

---

## V1.4.0

发布日期：2026-04-13

> V1.4.0 在 V1.3.0 Go 后端基础上新增多用户管理、独立观看历史、并发播放数限制、内容权限过滤、SSH 面板在线更新、管理面板版本号显示和角色权限体系，同时修复了多个稳定性和前端交互问题。

### 新功能：多用户管理

- **UserStore**：基于 SQLite（与 IDStore 共享 `data/mappings.db`）存储用户数据，使用参数化查询防止 SQL 注入，内存缓存加速查询
- **角色系统**：管理员（admin）和普通用户（user）两种角色。管理员拥有全部权限，普通用户仅能访问被分配的服务器
- **认证流程**：三步认证链（管理员匹配 → UserStore 认证 → 401 拒绝），使用 scrypt 密码哈希
- **Token 扩展**：Token 携带 Role 和 AllowedServers 字段，在每次 API 请求中自动过滤可访问的服务器
- **管理 API**：`GET/POST/PUT/DELETE /admin/api/users`，仅管理员可访问（`requireAdmin` 中间件）
- **管理面板**：新增「用户管理」页面，支持创建、编辑、启用/禁用、删除用户，可视化配置可访问服务器
- **SSH 面板**：新增菜单项 12-14，支持查看用户列表、添加用户、删除用户

### 新功能：独立观看历史

由于所有分发用户共享上游 Emby 账户，上游观看进度/已播放/收藏是共享的。V1.4 新增基于本地 SQLite 的独立观看历史系统：

- **管理员**保持上游行为不受影响
- **普通用户**的观看进度、已播放状态、收藏、继续观看和接下来观看完全隔离在本地数据库中
- 所有播放事件和用户操作**双写**至上游服务器和本地数据库
- 播放完成（进度 ≥ 90%）自动标记为"已看"
- 删除用户时自动清除其本地观看数据
- 首次播放某项目时自动从上游获取元数据以支持 NextUp 计算

### 新功能：并发播放数限制

- 每台上游服务器可独立配置最大并发播放数 `maxConcurrent`
- **PlaybackLimiter**：内存中跟踪每个 (用户ID, 服务器索引) 的播放状态
- 播放中和进度上报时自动刷新心跳，3 分钟无心跳自动释放占用
- 管理员豁免 `maxConcurrent` 限制
- 超出限制时返回 `429 Too Many Requests`

### 新功能：内容权限过滤

- 普通用户只能看到和播放被分配服务器上的内容
- `allowedClients(reqCtx)` 根据用户权限过滤在线客户端
- `isServerAllowed(reqCtx, serverIndex)` 单服务器权限检查
- 覆盖所有内容路由：UserViews、Items、搜索、媒体库、PlaybackInfo、流代理、Session 等
- MediaSource 服务器切换时额外验证目标服务器权限

### 新功能：聚合宽恕期与后台补全

搜索和媒体聚合不再阻塞等待所有上游服务器响应——引入宽恕期机制，快速服务器的结果即时返回，慢速服务器在宽恕期窗口内继续汇入：

- **三分离可配置宽恕期**：`searchGracePeriod`（搜索聚合，默认 3000ms）、`metadataGracePeriod`（元数据获取，默认 3000ms）、`latestGracePeriod`（最新添加，默认 0=等待所有服务器），管理面板超时设置区域可实时调整
- **通用聚合框架**：新建 `aggregation.go` 封装 `aggregateUpstreams()` 统一处理多上游扇出、宽恕期等待、结果合并
- **后台静默补全**：宽恕期超时后，后台 goroutine 继续收集剩余服务器结果并写入 ID 映射，不阻塞客户端响应，确保数据完整性
- **元数据多实例并行获取**：`handleUserItemByID` 的多实例元数据获取从顺序改为并行 goroutine + 宽恕期，减少请求延迟叠加

### 新功能：管理面板版本号显示

管理面板侧边栏底部显示当前运行版本号（如 `Emby-In-One v1.4.0`），便于快速确认版本。版本号通过编译时注入，支持 `--version` 命令行标志。向后兼容 V1.3.0 旧二进制（不返回 version 字段时不渲染）。

### 安全增强

- **参数化 SQL 查询**：UserStore 使用 `prepare` + `bindAll` + `step` 参数化查询，防止 SQL 注入。移除 `sqlEscape` 函数
- **Token 过期内存清理**：`save()` 遍历 Token 时，同时从内存中删除过期条目
- **防御性权限检查**：`reqCtx == nil` 或 `ProxyUser == nil` 时返回 false / 空列表，防止上下文缺失导致越权
- **并发限制防绕过**：通过聚合搜索选择不同服务器片源时，服务器切换前重新检查 `PlaybackLimiter.TryStart`
- **Admin API CORS 同源检查加固**：管理 API 的 CORS 同源检查改为仅使用 `r.Host`，防止通过 `X-Forwarded-Host` 伪造绕过
- **批量 ID 查询大小限制**：批量 `?Ids=` 查询参数新增 2000 个上限，防止恶意超大请求消耗资源
- **SSH 面板 JSON 注入修复**：SSH 管理菜单中用户名/密码直接拼接到 curl JSON 请求体的安全隐患，已通过 `json_escape()` 转义函数修复
- **UA 透传仅限管理员**：`passthrough` 模式下普通用户登录时不再捕获客户端 UA 标识（`SetCaptured`），仅管理员登录时采集，防止普通用户覆盖管理员已捕获的身份信息

### 安全审计修复

基于完整代码安全审计的修复，共修补 4 项漏洞：

- **密码验证明文回退移除**（HIGH）：`VerifyPassword()` 在存储的密码不是 scrypt 哈希格式时，原先回退到明文常量时间比较。虽然 `ensureAdminPasswordHashed()` 在启动时已将明文密码转为哈希，但此回退路径属于纵深防御缺失。修复后 `VerifyPassword()` 在存储密码不匹配 scrypt 格式时直接返回 `false`
- **代理测试 SSRF 防护**（CRITICAL）：`handleAdminProxyTest` 的 `targetUrl` 参数仅校验 `http(s)://` 格式，未限制为外部地址。已认证管理员可令代理向任意内网地址发请求（如云实例元数据 `169.254.169.254`）。新增 `isPrivateOrReservedIP()` 函数，DNS 解析后检查 `IsLoopback/IsPrivate/IsLinkLocalUnicast/IsUnspecified`，拦截私有/保留地址
- **IP 欺骗绕过登录限速**（HIGH）：`clientIP()` 原先无条件信任 `X-Real-IP` / `X-Forwarded-For`，攻击者可伪造 IP 绕过限速。新增 `server.trustProxy` 配置项（默认 `false`），仅在 `trustProxy: true` 时才信任代理头。**部署在反向代理后的用户需在 `config.yaml` 的 `server` 段添加 `trustProxy: true`**
- **限速器内存耗尽 DoS**（HIGH）：`loginRateLimiter.attempts` map 无容量上限，结合 IP 欺骗可无限增长导致内存耗尽。新增 10000 条上限（`loginMaxTrackedIPs`），超出容量时拒绝新 IP 登录请求

### Bug 修复

- 修复 `custom` UA 伪装模式仅在管理面板可配置、但未真正作用于后端链路的问题：管理 API、配置持久化、运行时请求头生成、登录认证、健康检查、图片代理与流媒体代理现已全部接通；再次编辑上游服务器时也会正确回填已保存的自定义字段
- 修复源码仓库 Docker 安装在 builder 阶段构建失败的问题：builder 现会显式复制 `public/` 目录参与 Go 编译，避免出现 `package emby-in-one/public is not in std` 错误
- 修复 passthrough 模式下剩余的主动登录入口仍可能以 `infuse-fallback` 身份触碰上游的问题：手动“重连上游”和 401/403 后自动恢复登录现已在无真实客户端身份时安全跳过，避免再次以 `Infuse` 身份误碰上游服务器
- 修复跨服务器播放时"继续观看"记录不更新：`translateSessionBodyIDs` 重写为独立解析每个 ID，目标服务器优先级改为 ItemId → MediaSourceId → PlaySessionId → ActiveStream
- 修复前端管理面板交互失效：旧 Token 加载时自动迁移 Role；`api()` 检查 `response.ok`；处理 204 无 body 情况；所有 async 方法添加 try-catch
- 修复前端网络代理检测显示 undefined：后端统一错误响应格式，前端添加 undefined 兜底
- 修复 `install-release.sh` 中 `curl` 命令缺少超时参数
- 修复选择保留配置和数据的卸载选项时，`find` 命令会意外删除整个项目目录
- 修复SSH 面板输出中 ANSI 颜色转义序列未渲染为颜色
- 修复SSH 面板所有交互式输入中退格键不会删除字符，而是输出 `^H`
- 修复管理面板代理连通性测试在上游返回 403（如 Cloudflare 拦截）时误报"连通失败"：改为只要收到 HTTP 响应即视为连通成功
- 修复 Docker 模式下"下载指定版本"下载 Release 二进制而非重建镜像的问题：拆分为 Binary/Docker 双模式下载安装路径
- 修复观看进度 UPSERT 仅更新 `position_ticks` 而未更新 `played` 和 `is_favorite`，导致已完成剧集被进度事件重置为"未看"并反复出现在 Resume 列表
- 修复子用户无本地观看记录时上游管理员的 `Played/IsFavorite/PlaybackPositionTicks` 泄露至普通用户页面——无记录时主动清除上游 UserData
- 修复 `GET /Items/{itemId}` 单条目路由缺少观看状态叠加调用，导致详情页显示上游管理员的观看标记
- 修复 Resume 首页同一部剧集显示多集：新增 SQL 窗口函数系列级聚合（`ROW_NUMBER() OVER PARTITION BY series_name`），每部剧集只保留最近一集进度
- 修复启动时脏数据（播放进度 ≥ 90% 但 `played` 仍为 0）导致 Resume 列表膨胀：启动时自动幂等迁移修正
- 修复管理面板普通用户可登录空白界面：`doLogin()` 增加 `/admin/api/status` 权限验证，非管理员提示并退出
- 修复 Passthrough 模式首次登录使用 Infuse 伪装身份被持久化，导致后续设备受限服务器全部 403：`recordSuccessfulIdentity()` 排除 `infuse-fallback` 来源
- 修复管理员浏览器登录时浏览器 UA 污染透传身份缓存：添加 `hasPassthroughIdentity` 前置检查
- 修复管理面板不显示上游节点地址：`handleAdminStatus` 字段名 `host` → `url` 与前端模板对齐
- 修复上游服务器离线/断开/删除后，以该服务器为元数据源的内容返回 404"找不到项目"：`resolveRouteID()` 和 `resolveFallbackTarget()` 增加 OtherInstances 回退，自动路由到持有相同内容的在线服务器
- 修复上游服务器离线后普通用户的"继续观看"和"接下来观看"全部消失：`enrichWatchItems()` 和 `handleLocalNextUp()` 新增离线重映射逻辑，通过 IDStore 查找在线替代服务器获取元数据

#### 稳定性修复

- 修复 ID 映射中 `activeStreamServer` 内存泄漏：过期条目未被清理，长时间运行后无限累积。已补充 TTL 淘汰逻辑
- 修复客户端身份持久化数据丢失：JSON 文件损坏时覆盖写入导致数据清零。已改为遇到错误时中止写入
- 修复数据库操作错误静默忽略：关键数据库操作失败时不记录日志。已补充条件日志输出
- 进行了高聚合代码的拆分，保证项目易读性和易维护性
- 新增 `install-release.sh` 中 `systemctl` 可用性检查，不可用时提示用户手动启动
- 增强 `go_install.sh` 密码生成鲁棒性：初始熵从 12 字节增加到 24 字节，确保截取密码长度充足

### Passthrough 延迟登录

`passthrough` 模式的上游不再在 `LoginAll()` 启动时使用 Infuse 身份尝试登录。新增 `HasCapturedHeaders()` 方法检测是否已有捕获的客户端身份，无已捕获身份时跳过登录。上游保持 Offline 状态直到真实客户端连接后自动完成认证，避免在上游 Emby 产生虚假 Infuse 设备记录。

### SSH 管理菜单增强

#### CLI 双模式部署

SSH 管理菜单新增 Binary/Docker 自动检测。所有操作（启动/停止/重启/更新/状态/日志/卸载/用户管理）自动分发到 systemd 或 Docker Compose 对应命令。

- **Binary 模式更新**：下载并执行 `release-install.sh`，自动停止/升级/重启 systemd 服务
- **Docker 模式更新**：源码重建流程——下载最新源码 → 替换源码（保留 config/data/log）→ 重建镜像 → 重启容器
- **Binary 模式状态**：显示 systemd 运行状态、PID、内存占用、运行时长、监听端口
- **菜单显示版本号**：标题栏显示当前版本号

#### CLI 自替换安全修复

修复 `do_update()` 使用 `cp` 直接覆盖正在执行的脚本导致 bash 懒读取出错的问题。改为 temp + `mv` 原子替换模式，`mv` 在同一文件系统上执行 `rename()` 系统调用，运行中的 bash 进程仍持有旧 inode 的文件描述符，可安全读完当前脚本。

---

## V1.3.1
发布日期：2026-04-02

> V1.3.1 针对部分客户端出现间歇性断线/401 的问题，对代理 Token 策略和上游认证恢复机制进行了改进。

### Token 永不过期
- 代理 Token 不再有 48 小时硬性过期限制，改为**永不过期**
- Token 仅在以下场景被撤销：用户主动登出、管理员修改密码、CLI `--reset-password`
- 修复 Yamby TV 等长时间后台挂起的客户端每 48 小时被强制 401 的问题

### 上游认证自动恢复
- 当上游服务器返回 401/403（非登录路径）时，代理自动触发异步重新登录
- 30 秒防抖：短时间内大量 401 不会导致登录风暴
- 登录路径本身的 401 不会触发恢复（避免死循环）
- 修复上游 Token 过期后需要手动重连的问题

### 密码修改安全增强
- 管理面板修改密码后，自动撤销所有已签发的代理 Token（要求所有客户端重新登录）
- CLI `--reset-password` 同步清除 tokens.json 中的所有会话

### 测试与维护
- 修复 `distribution_test.go` 中 `repoRootPath()` 路径层级错误
- 修复 `TestAdminHTMLSaveServerHandlesUpstreamErrors` 测试断言与实际 HTML 不匹配
- 导出 `TokenFileMode()` 确保 CLI 与核心模块使用一致的文件权限策略

---

## V1.3 (Pre-release)
发布日期：2026-03-30

> V1.3 将后端从 Node.js 重构为 Go，性能与并发处理能力大幅提升。以下为相对 V1.2.1 的新增功能与改进。

### 架构升级
- Go 后端取代 Node.js 实现，并发处理能力大幅提升，SQLite 持久化 ID 映射支持重启恢复
- 多服务器请求改为并行（goroutine），聚合延迟取决于最慢服务器而非所有服务器之和

### 新功能
- 4 级元数据优先级选择：priorityMetadata 标记 → 中文简介 → 更长简介 → 服务器顺序
- UA 伪装新增 custom 模式，可独立配置全部 5 个 Emby 身份标识字段（旧版仅支持 infuse/none）
- SSH 管理菜单：支持服务管理、账号管理、系统维护、更新服务
- 登录速率限制：连续 5 次失败锁定 IP 15 分钟，支持反向代理 IP 识别
- 优雅关机：收到 SIGINT/SIGTERM 信号后先排空活动连接再退出
- 搜索分页支持 StartIndex/Limit 参数

### 安全增强
- 请求体大小限制（2MB，超限返回 413）
- 配置文件权限收紧（0o600，防止其他用户读取密码）

### 管理工具改进
- 管理面板全面汉化
- SSH 面板现代化重写，新增更新服务功能
- 代理池管理面板支持连通性测试

---

## V1.2.1
发布日期：2026-03-29

### Bug 修复
- 修复港机/境外服务器 Docker 构建失败（exit code 100）：改为先尝试官方 Debian 源，失败再回退阿里云镜像
- 修复 --reset-password 命令行重置密码无效：saveConfig() 改为返回 Promise，等待磁盘写入后再退出
- 修复多服务器搜索结果仅显示第一个服务器内容：/Search/Hints 改为交错合并 + TMDB/标题去重

---

## V1.2

发布日期：2026-03-23

---

## 稳定性修复

### 搜索进入剧集时的观看历史隔离

修复通过搜索进入聚合剧集时，系列级观看历史混入多个上游服务器进度的问题。

- `GET /Users/:userId/Items/Resume?ParentId=...` 改为“主实例优先，顺序回退”
- `GET /Shows/NextUp?SeriesId=...` 改为“主实例优先，顺序回退”
- 当主实例返回了不属于当前剧集的条目时，会先过滤，再继续尝试同剧的下一实例
- 不再出现搜索进入剧集后把多个服务器的观看进度排在一起，导致集数重复或倒排（如 `2,1,2,3`）的情况

### HLS 代理清单重写修复

修复代理模式下 HLS 清单被重写为 `localhost` 绝对地址的问题，避免反向代理或公网域名部署时播放失败。

- `rewriteM3u8()` 改为输出代理相对路径，而不是 `http://localhost:<port>`
- 保留 `api_key` 替换逻辑，但不再向客户端暴露 `localhost` 或上游域名
- 代理模式下更适合直连、反向代理和公网域名部署场景

### 跨服附加实例关系持久化

修复 `otherInstances` 仅保存在内存中、重启后丢失的问题。

- 新增 SQLite 表 `id_additional_instances`
- `associateAdditionalInstance()` 现在同时写入内存和 SQLite
- 启动时自动恢复附加实例关系
- 重启后仍能保留多版本 `MediaSources`、同剧 fallback 与附加实例可见性

### 上游配置草稿验证

修复管理面板新增/编辑上游服务器失败后污染运行时配置的问题。

- 新增上游：先构造草稿并验证登录成功，再写入 `config.upstream` 与 `upstreamManager.clients`
- 编辑上游：先复制旧配置生成 draft，通过验证后再原子替换
- 失败时不再留下脏内存配置，也不会在后续 `saveConfig()` 时被意外落盘

## 安全修复

### Passthrough 客户端身份按 Token 隔离

修复 passthrough 模式下所有设备共享同一份已捕获客户端身份的问题。

- `captured-headers` 从全局单槽改为 `token -> headers` 映射
- 当前请求无实时客户端头时，仅允许回退到“当前 token 对应”的已捕获身份
- 登出、Token 撤销、Token 过期时同步清理对应 captured headers
- 避免多设备、多用户场景下 UA / 设备信息串线

### Admin API CORS 真正同源化

修复 `/admin/api/*` 反射任意 `Origin` 的问题。

- Admin API 不再反射任意来源的 `Access-Control-Allow-Origin`
- 管理面板接口回归真正的 same-origin 策略
- 普通 Emby 客户端接口仍保持宽松 CORS 兼容性

## 文档更新

- `README.md` 同步补充本次 HLS、Passthrough、持久化与 Admin 安全修复说明
- `README_EN.md` 同步补充本次 HLS、Passthrough、持久化与 Admin 安全修复说明
- 两份 README 中的 passthrough、HLS、ID 持久化描述已更新为当前实现

## 本次涉及文件

| 文件 | 修改内容 |
|------|----------|
| `src/utils/series-userdata.js` | 系列级用户态选择 helper |
| `src/routes/items.js` | 修复带 `ParentId` 的 `Resume` 聚合逻辑 |
| `src/routes/library.js` | 修复带 `SeriesId` 的 `NextUp` 聚合逻辑 |
| `src/utils/stream-proxy.js` | HLS 清单改为相对路径重写 |
| `src/routes/streaming.js` | 移除 `localhost` HLS 重写依赖 |
| `src/utils/captured-headers.js` | 改为 token 级客户端头隔离 |
| `src/emby-client.js` | passthrough 头优先级调整 |
| `src/routes/users.js` | 登录成功后按 token 捕获客户端头 |
| `src/auth.js` | Token 撤销/过期时清理 captured headers |
| `src/routes/admin.js` | 上游草稿校验提交流程 |
| `src/id-manager.js` | 附加实例关系持久化到 SQLite |
| `src/utils/cors-policy.js` | 新增 Admin/客户端分级 CORS 策略 |
| `src/server.js` | 接入新的 CORS 与请求上下文逻辑 |
| `tests/routes/items.resume-series.test.js` | `Resume` 回归测试 |
| `tests/routes/library.nextup-series.test.js` | `NextUp` 回归测试 |
| `tests/utils/stream-proxy.test.js` | HLS 重写测试 |
| `tests/utils/captured-headers.test.js` | passthrough token 隔离测试 |
| `tests/routes/admin.upstream-draft.test.js` | Admin 草稿提交流程测试 |
| `tests/id-manager.persistence.test.js` | `otherInstances` 持久化恢复测试 |
| `tests/utils/cors-policy.test.js` | Admin CORS 策略测试 |
| `README.md` | 同步更新功能与实现说明 |
| `README_EN.md` | 同步更新功能与实现说明 |

---

## V1.1

发布日期：2026-03-23

---

## 安全增强

### 管理员密码哈希存储

管理员密码不再以明文形式存储在 `config.yaml` 中。系统现使用 Node.js 内置 `crypto.scryptSync` 算法进行加盐哈希处理。

- 当前默认 Go 后端会在**服务启动时**自动将明文密码迁移为哈希格式，无需等待首次登录
- 使用 `crypto.timingSafeEqual` 进行安全的常量时间比较，防止时序攻击
- 每次哈希使用 16 字节随机盐，格式为 `salt:hash`

### Token 过期与撤销机制

代理认证 Token 现具有 48 小时有效期，超时后自动失效。

- `validateToken` 增加 TTL 校验，过期 Token 自动清除
- 持久化保存时过滤已过期 Token，避免 `tokens.json` 无限膨胀
- 新增 `revokeToken` 方法，支持主动撤销 Token
- 新增 `POST /admin/api/logout` 登出端点

### 密码修改安全验证

通过管理面板修改管理员密码时，现需提供当前密码进行身份确认。

- `PUT /admin/api/settings` 接口在检测到密码修改请求时，要求携带 `currentPassword` 字段
- 当前密码验证失败返回 `403 Forbidden`
- 新密码自动以哈希格式存储，无需额外处理
- 管理面板已同步更新，密码输入框下方动态显示「当前密码（验证）」输入框

### 配置文件写入保护

`saveConfig` 函数引入 Promise 链序列化机制，防止并发写入导致配置文件损坏。

- 多个管理操作（添加服务器、修改设置等）同时触发时，写入操作按队列顺序依次执行
- 写入失败时捕获异常并记录日志，不影响服务运行

### Redirect 模式安全提示

在管理面板中选择「直连模式 (302)」时，新增可视化安全警告。

- 全局设置和单服务器设置中的播放模式选择器均已添加警告
- 明确告知管理员：直连模式会将上游服务器的 Access Token 暴露在重定向 URL 中

---

## 访问控制

### Fallback 路由认证加固

兜底路由（未匹配到特定路由的请求）现要求有效的代理认证 Token。

- 此前未认证请求可通过 Fallback 路由直接转发至上游服务器
- 现由 `requireAuth` 中间件拦截，未认证请求返回 `401 Unauthorized`

### CORS 策略分级

Admin API 和 Emby 客户端路由采用差异化 CORS 策略。

- `/admin/api/*` 路径：仅允许同源请求，`Access-Control-Allow-Origin` 设为请求来源
- 其他 Emby 客户端路由：保持 `Access-Control-Allow-Origin: *`，确保各类 Emby 客户端兼容

---

## 稳定性修复

### ID Manager 迭代删除修复

修复 `removeByServerIndex` 在迭代 Map 时同时删除元素可能跳过条目的问题。

- 改为先收集所有待删除的 key，再统一执行删除操作
- 确保删除上游服务器时所有关联的 ID 映射被完整清理

### Admin 端点边界校验完善

- `POST /api/upstream/:index/reconnect`：新增索引范围检查，越界返回 `404`
- `POST /api/upstream/reorder`：新增 `fromIndex` / `toIndex` 范围检查，越界返回 `400`
- `POST /api/upstream`：新增 URL 协议校验，仅允许 `http://` 和 `https://` 前缀

### PlaySession 定时清理

`playSessions` Map 新增 30 分钟周期性清理，独立于请求触发。

- 此前仅在注册新 PlaySession 时附带清理过期条目
- 现通过 `setInterval` 主动清理，使用 `.unref()` 不阻止进程退出
- 防止长时间无新播放请求时过期 Session 持续占用内存

---

## 性能优化

### 日志文件级别调整

文件日志 transport 默认级别从 `debug` 调整为 `info`，大幅减少生产环境日志文件体积。

- 支持通过环境变量 `FILE_LOG_LEVEL` 自定义文件日志级别
- 控制台日志级别不变，仍由 `LOG_LEVEL` 环境变量控制（默认 `info`）

### ID 重写器内存优化

`rewriteResponseArray` 中的循环引用检测 Set 改为按 item 隔离创建。

- 此前所有 item 共享一个 `seen` Set，导致前序 item 的对象引用无法被 GC 回收
- 现每个 item 使用独立 Set，处理完即可释放，降低大型响应的峰值内存占用

### BufferTransport 防重复注册

`createAdminRoutes` 中的 Winston BufferTransport 添加重复检测守卫。

- 通过检查现有 transport 的构造函数名称避免重复添加
- 防止热重载等场景下产生重复日志条目

### Dockerfile 多阶段构建

Docker 镜像改为两阶段构建，运行时镜像不再包含编译工具链。

- **builder 阶段**：安装 `build-essential`、`python3`、`g++`、`make`，编译 `better-sqlite3` 等原生模块
- **runtime 阶段**：基于干净的 `node:20-slim`，仅拷贝编译好的 `node_modules` 和源码
- 最终镜像体积显著减小

---

## 修改文件清单

| 文件 | 修改内容 |
|------|----------|
| `src/auth.js` | 密码哈希、Token TTL、撤销机制 |
| `src/config.js` | 写入队列序列化 |
| `src/server.js` | CORS 分级策略、传递 authManager |
| `src/routes/admin.js` | 登出端点、密码验证、URL 校验、边界检查、Transport 防重 |
| `src/routes/fallback.js` | 添加 requireAuth |
| `src/routes/playback.js` | playSessions 定时清理 |
| `src/id-manager.js` | 迭代删除修复 |
| `src/utils/logger.js` | 文件日志默认级别调整 |
| `src/utils/id-rewriter.js` | seen Set 按 item 隔离 |
| `public/admin.html` | Redirect 警告、当前密码验证框 |
| `Dockerfile` | 多阶段构建 |

---

## 升级说明

### 从 V1.0 升级

1. **密码自动迁移**：当前默认 Go 后端会在服务启动时自动将 `config.yaml` 中的明文密码转换为哈希格式，无需等待首次登录。
2. **已有 Token 过期**：升级后已发放的 Token 将在 48 小时后自动失效，需重新登录。
3. **Docker 用户**：重新构建镜像即可（`docker compose build && docker compose up -d`），镜像体积会明显减小。
4. **日志级别**：文件日志默认降至 `info`，如需调试级别日志，设置环境变量 `FILE_LOG_LEVEL=debug`。
5. **无破坏性变更**：所有 Emby 客户端接口行为保持不变，升级对终端用户透明。
