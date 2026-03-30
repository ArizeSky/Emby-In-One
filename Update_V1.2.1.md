# Emby-In-One 更新日志

## V1.2.1

发布日期：2026-03-29

---

## Bugfix

### Docker 构建在港机/部分境外服务器失败（exit code 100）

修复在香港等地区服务器上执行 `bash install.sh` 时，Docker 镜像构建阶段 `apt-get update` 报 exit code 100 导致安装失败的问题。

- 原因：旧逻辑无条件将 Debian 源替换为阿里云镜像，而阿里云在港机等境外服务器上网络不通，导致 `apt-get update` 失败
- 修复：改为先尝试默认 Debian 官方源，仅在失败时回退到阿里云镜像源
- 对国内服务器无影响，对境外服务器不再强制替换镜像源

### `--reset-password` 命令行重置密码无效

修复通过 `docker exec` 执行 `node src/index.js --reset-password <新密码>` 后密码并未实际改变的问题。

- 原因：`saveConfig()` 内部通过 Promise 队列异步写入文件，而 `process.exit(0)` 在写入完成前就终止了进程
- 修复：`saveConfig()` 现在返回 Promise，密码重置流程中 `await saveConfig(config)` 等待磁盘写入完成后再退出
- 注意：其余调用处（服务运行中触发的保存）无需 await，行为不变

### 搜索结果仅显示第一个服务器的内容

修复多服务器场景下搜索结果仅展示优先级最高服务器内容的问题。

- 原因：`/Search/Hints` 将所有服务器结果顺序拼接，第一个服务器的结果全部排在最前，客户端默认限制条数后看起来只有一个服务器的结果；同时无去重处理导致同一影片重复出现
- 修复：改为交错（interleave）方式合并各服务器搜索结果，各服务器结果轮流出现
- 同时新增去重逻辑：基于 TMDB ID 或 标题+发行年份 匹配，重复条目关联为同一虚拟 ID 并跳过展示
- 去重逻辑与 Items 合并路由保持一致，避免跨 provider 不匹配

## 本次涉及文件

| 文件 | 修改内容 |
|------|----------|
| `Dockerfile` | apt-get update 先尝试官方源，失败再 fallback 阿里云镜像 |
| `src/config.js` | `saveConfig()` 返回 Promise |
| `src/index.js` | `--reset-password` 路径 `await saveConfig()` |
| `src/routes/library.js` | `/Search/Hints` 改为交错合并 + TMDB/标题去重 |

---

## V1.2

发布日期：2026-03-23

---

## Bugfix

### Passthrough 透传登录重试使用 Infuse 兜底身份导致 403

修复非标准客户端登录后，离线 passthrough 服务器自动重试时始终使用 Infuse 兜底身份、被上游 nginx 拒绝的问题。

- `login()` 新增 `overrideHeaders` 参数，登录重试时直接传入刚捕获的客户端 headers
- `_getPassthroughHeaders()` 新增五级优先级链：live-request → captured-token → last-success → captured-latest → infuse-fallback
- 健康检查和重连优先使用该服务器上次成功登录的 headers（`last-success`）
- 详见 [Bugfix.md](Bugfix.md)

### 客户端身份持久化

新增 passthrough 客户端身份按服务器维度持久化，重启后自动恢复。

- 成功登录后，使用的完整 headers 按服务器名存储到 `data/captured-headers.json`
- 重启时自动加载，passthrough 服务器无需等待用户重新登录
- 同时保留 per-token 的客户端头隔离，确保多设备不串线

### 管理面板编辑 passthrough 服务器返回 500

修复通过管理面板编辑 passthrough 模式服务器（如修改播放模式）时，因登录验证失败导致 500 错误的问题。

- `createValidatedClient` 对 passthrough 服务器允许登录失败后仍保存配置
- passthrough 服务器在管理面板上下文无 captured headers 属正常行为，记录 WARN 日志

### 搜索结果显示上游 HTML 错误页面

修复上游服务器返回 HTML 错误页面（如 Cloudflare 522）时，fallback 路由将原始 HTML 直接发送给客户端的问题。

- 检测响应以 `<!DOCTYPE` 或 `<html` 开头时，返回 502 JSON 错误而非原始 HTML
- 不误拦截合法的 XML/SVG 响应

### 超时错误日志缺少服务器标识

改进所有聚合请求的超时日志，显示具体是哪台服务器失败。

- `requestAll()`、Items/Latest、Search/Hints、Views、Seasons、Episodes 等路由均记录失败服务器名称和原因
- 日志从 `Error in GET Items/Latest: timeout` 改为 `[HXD] Items/Latest failed: timeout of 30000ms exceeded`

### 管理员密码重置机制

修复密码哈希后用户无法在 config.yaml 或 SSH 面板中查看/重置密码的问题。

- 新增 `--reset-password <新密码>` CLI 参数，支持命令行重置
- config.yaml 中写入新的明文密码后重启，系统自动迁移为哈希格式
- install.sh SSH 面板检测到哈希值时显示重置提示
- README 新增"忘记管理员密码"FAQ

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
- 当前请求无实时客户端头时，仅允许回退到”当前 token 对应”的已捕获身份
- 登出、Token 撤销、Token 过期时同步清理对应 captured headers
- 避免多设备、多用户场景下 UA / 设备信息串线

### Admin API CORS 同源校验

修复 `/admin/api/*` 未严格校验请求来源的问题。

- Admin API 的 CORS 头仅在 Origin 与 Host 匹配时设置
- 非匹配来源的跨域请求不会收到 `Access-Control-Allow-Origin` 响应头
- 管理面板新增 `X-Content-Type-Options`、`X-Frame-Options`、`X-XSS-Protection` 安全响应头

### API 响应脱敏

修复管理面板 API 响应中泄露上游服务器内部信息的问题。

- `/api/status` 不再返回上游服务器 URL 和 userId
- `/api/upstream` 的 URL 字段自动去除凭据（`user:pass@`）
- `/api/proxies` 的代理 URL 中密码显示为 `****`
- 上游登录失败日志不再打印完整响应体，仅记录 message 字段

### 管理员密码启动时自动加密

- 密码从”首次登录时迁移”改为”启动时立即迁移”，消除明文窗口期
- 密码哈希格式检测从 `includes(':')` 改为正则匹配 `hex(32):hex(128)`，支持含冒号的密码
- Token 持久化文件 `tokens.json` 写入时指定 `mode: 0o600`
- 配置文件写入改为原子操作（临时文件 + rename），Windows 兼容降级

### Admin API 输入校验加固

- `playbackMode` 仅接受 `proxy` / `redirect`
- `serverName` 限制 1-100 字符
- `adminUsername` 限制 1-50 字符
- 代理 URL 仅接受 http/https 协议
- `parseInt` 统一加 radix 和 NaN 检查
- `reorder` 端点校验 fromIndex/toIndex 为整数类型
- 上游配置 `normalizeUpstream` 新增 url 必填、URL 格式、apiKey/username 二选一校验

## 其他改进

- `favicon.ico` 请求静默返回 204，不再产生大量 401 日志
- `rewriteRequestIds` 增加循环引用保护（`seen` Set）
- 日志级别环境变量增加有效值校验
- `playSessions` 移除每次注册时的多余全量清理
- `System/Info` 的 `LocalAddress` 改为动态使用请求 Host
- `images.js` 删除重复的 `/emby/` 前缀路由
- CDN 资源（Tailwind、Vue、Lucide）锁定版本号，使用 Vue 生产构建
- YAML 配置加载显式指定 `JSON_SCHEMA`
- 上游错误响应不再原样转发给客户端

## 文档更新

- `README.md` 更新 Passthrough 五级优先级说明、客户端身份持久化、健康检查描述、密码自动加密、忘记密码 FAQ
- `Bugfix.md` 记录所有 bug 修复细节
- `Update Plan.md` 更新 V1.2 版本说明

## 本次涉及文件

| 文件 | 修改内容 |
|------|----------|
| `src/emby-client.js` | passthrough 五级优先级链、login overrideHeaders、_lastSuccessHeaders 持久化、日志脱敏 |
| `src/utils/captured-headers.js` | per-token + per-server 持久化、init/load/save、buildInfo null guard |
| `src/routes/admin.js` | API 响应脱敏、输入校验、parseIndex、createValidatedClient passthrough 放行 |
| `src/config.js` | YAML 安全加载、原子写入、上游配置校验、启动时密码哈希迁移 |
| `src/auth.js` | isHashed() 正则检测、token 文件权限 0o600 |
| `src/server.js` | CORS 中间件、安全响应头、favicon 204 |
| `src/utils/cors-policy.js` | Admin Origin 同源校验 |
| `src/utils/stream-proxy.js` | sanitizeUrl 日志脱敏、M3U8 缓冲限制 |
| `src/routes/fallback.js` | 上游错误脱敏、HTML 响应拦截 |
| `src/routes/images.js` | 删除重复 /emby/ 路由 |
| `src/routes/users.js` | 登录重试传入 captured headers、Views 失败日志 |
| `src/routes/items.js` | Items/Latest 等聚合失败日志 |
| `src/routes/library.js` | Search/Seasons/Episodes 聚合失败日志 |
| `src/upstream-manager.js` | requestAll 失败日志、stopHealthChecks |
| `src/id-manager.js` | close() 方法 |
| `src/index.js` | --reset-password CLI、capturedHeaders.init、shutdown 清理 |
| `src/utils/logger.js` | 日志级别环境变量校验 |
| `src/utils/id-rewriter.js` | rewriteRequestIds 循环引用保护 |
| `src/routes/playback.js` | 移除多余 inline cleanup |
| `src/routes/system.js` | LocalAddress 动态化 |
| `public/admin.html` | CDN 版本锁定 |
| `install.sh` | SSH 面板兼容哈希密码 |
| `README.md` | Passthrough 五级解析、持久化、密码 FAQ |
| `Bugfix.md` | 新增修复记录 |
| `Update.md` | 更新日志 |

---

## V1.1

发布日期：2026-03-23

---

## 安全增强

### 管理员密码哈希存储

管理员密码不再以明文形式存储在 `config.yaml` 中。系统现使用 Node.js 内置 `crypto.scryptSync` 算法进行加盐哈希处理。

- 首次登录时自动将明文密码迁移为哈希格式，无需手动操作
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

1. **密码自动迁移**：升级后首次使用管理员账号登录，系统会自动将 `config.yaml` 中的明文密码转换为哈希格式，无需手动操作。
2. **已有 Token 过期**：升级后已发放的 Token 将在 48 小时后自动失效，需重新登录。
3. **Docker 用户**：重新构建镜像即可（`docker compose build && docker compose up -d`），镜像体积会明显减小。
4. **日志级别**：文件日志默认降至 `info`，如需调试级别日志，设置环境变量 `FILE_LOG_LEVEL=debug`。
5. **无破坏性变更**：所有 Emby 客户端接口行为保持不变，升级对终端用户透明。