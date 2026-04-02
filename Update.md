# Emby-In-One 更新日志

## V1.3.1
发布日期：2026-04-02

> V1.3.1 针对部分客户端（Yamby TV、SenPlayer 等）出现间歇性断线/401 的问题，对代理 Token 策略和上游认证恢复机制进行了改进。

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
