# Emby-In-One 更新日志

## V1.4.4

发布日期：2026-09-18（V1.4.4-rc1 预发布；正式版日期待定）

> V1.4.4 为 V1.4.3 的累积更新，汇总一次全项目审查的修复产出（15 项）与后续跟进项：**上游服务解耦数组下标全面采用持久化唯一 `server_id`**、内容访问控制与 SSRF 加固、普通用户本地观看状态筛选落地、无 `ParentId` 聚合列表的分页缺陷修复、虚拟用户 ID 透传缺陷修复、仓库行尾统一，以及管理面板前端依赖自托管与 CSP 收紧；rc1 预发布期间继续并入：**多推流线路（主线路 + 备用线路）**、**每用户首页库隐藏**，以及安装脚本与 SSH 菜单在非 root 服务下的两处致命问题修复。**管理面板的资源加载方式、安全策略与普通用户的响应身份有变化，升级前请先读「升级须知」。**

### ⚠️ 升级须知

- **管理面板不再从任何第三方地址加载资源**。Vue、lucide、Tailwind CSS 与字体全部改为自托管并内嵌进二进制，CSP 同时收紧，**不再允许内联脚本与内联样式**。
- **只换二进制、不刷新面板文件可能白屏**。新 CSP 会拦掉磁盘上旧版 `public/admin.html` 里的内联 `<style>` 与 CDN 引用。正常升级（`emby-in-one-cli.sh` 更新或重跑安装脚本）会自动刷新面板文件；**万一刷新失败，现在会明确告警**，不再静默跳过。遇到白屏时删掉 `public/admin.html` 即可，面板会改用二进制内嵌版本（内嵌版本永远与新 CSP 匹配）。
- **`public/vendor/` 不需要存在于磁盘**。安装脚本不会创建它，面板会自动回退到二进制内嵌的 vendor 资源。
- **普通用户响应中的用户 ID 取值变化**：`Views`、`PlaybackInfo`、媒体列表、`UserData`、收藏等「当前用户」响应，此前统一填全局管理员占位 ID，现在填该普通用户自己的本地 ID。这是**一致性修复**，不是新增故障；**不需要清客户端缓存**（旧 ID 与本地 ID 都被当作当前用户别名接受，见「虚拟用户 ID 透传缺陷修复」）。
- **四个筛选项对普通用户仍是上游语义**，见「已知限制」。
- **代理模式 HLS 清单重写行为修复**：此前 Go 版把 HLS 清单里的分片 URL 重写为**上游主机的绝对地址**（携带虚拟 ID 与代理 token，客户端实际无法使用，HLS 转码播放会失败）。现恢复为**代理相对路径**——分片请求回到本代理，与 Node 版 V1.2 的既定行为一致。已配置反向代理 / 公网域名的部署无需任何改动。
- **推流线路旧配置无需迁移**：旧配置里的单条 `streamingUrl` 键继续有效，保存时自动并入新的有序列表。

### 安全增强

- **内容访问控制**：新增 `requireServerAccess` 权限检查；聚合结果中会过滤掉当前用户无权访问的 `OtherInstances`，按 ID 取单项的接口也改走同一套检查，堵住通过跨服实例 ID 绕过内容权限的路径
- **SSRF 防护**（新增 `ssrf.go`），区分两套策略，避免“一刀切”误伤自建环境：
  - 管理面板的「代理连通性测试」使用严格策略，拦截 loopback / 私有网段 / link-local 目标，并带 DNS 重绑定防护
  - 访问上游 Emby 使用宽松策略，**只拦 link-local 与未指定地址**，保证局域网或本机回环地址上的 Emby 仍可正常使用
- **登录速率限制**：从 `handlers_user.go` 抽出独立的 `login_limiter.go`；同一 IP 连续失败 **5 次锁定 15 分钟**（最多跟踪 10000 个 IP）。记录表写满时**驱逐最旧记录而不是拒绝新请求**，避免被刷满后限制失效
- **敏感文件权限**：新增 `privatefile.go`，配置与 token 文件在 Unix/Linux 上按 `0600` 写入；SQLite 建库后同步收紧 `-wal` / `-shm` 权限
- **管理 API 审计日志**：非 GET 请求与全部 4xx/5xx 均记录操作者身份（无效 token 记为 `unknown token`，非管理员标注角色），**绝不记录请求体与查询参数**——其中含密码与 API Key
- **管理面板 CSP 收紧**：详见「前端依赖自托管与 CSP 收紧」

### 架构升级：上游解耦数组下标，全面采用持久化唯一 server_id（全库零移位重排与外键级联）

此前 `mappings.db` 和 `user_servers`、`user_watch_progress` 使用服务器在列表中的**数组位置下标（`server_index`）**寻址。这种设计带来严重的架构耦合与脆弱性：每当删除或移动上游服务器时，系统都需要对多张表进行复杂的全库位移与回滚“手术”，一旦进程在配置变更与数据操作之间崩溃，将导致映射与授权静默错位。本次彻底淘汰了数组下标，全面重构为**持久化、唯一的字符串标识符 `server_id`**：

- **持久化 ID 自动分配与配置保存**：`UpstreamConfig` 新增 `ID string`（序列化为 YAML 中的 `id:` 字段）。系统启动加载配置时，若发现存量上游缺失 ID，自动为其分配 8 字节 Hex 随机字符串并原子持久化回写 `config.yaml`。上游重命名、换 URL、调序均不改变 ID，彻底免疫历史孤儿化痛点。
- **SQLite 数据库单事务平滑原子迁移**：系统启动检测到旧 schema 时，在单一事务内对 `id_mappings`、`id_additional_instances`、`user_servers`、`user_watch_progress` 全部 4 张表执行平滑迁移，自动把旧下标转换为对应 `server_id`；迁移过程对全部字段增加容错与 `COALESCE` 防护，杜绝历史 NULL 值触发约束而静默丢失数据；同时保留 `user_servers` 关联用户的级联外键（`ON DELETE CASCADE`）。
- **彻底根除“位移手术”，重排变为纯内存零开销**：从 `idstore.go` 和 `user_store.go` 中彻底删除了全部 8 个脆弱的移位与回滚函数（`ShiftServerIndices`、`RemoveByServerIndex`、`ReorderServerIndices` 等）；上游服务器排序彻底脱离数据库变更（Zero DB Mutation），实现平滑无感重排；删除上游统一由 `RemoveByServerID` 与 `RemoveServerGrants` 按 ID 级联清理对应映射与授权。
- **关联已知限制清空**：彻底解决了此前因 `user_watch_progress` 副本下标未同步而导致的排序偶发播放错位问题。

### 新功能：普通用户列表筛选改为本地状态

此前普通用户在客户端点击「收藏 / 已播放 / 未播放」等筛选时，用的是**上游共享账号**的状态，看到的是所有人的聚合结果。本次让代理接管这部分筛选：

- **本地生效**：`IsFavorite`、`IsPlayed`、`IsResumable` 改为按**本地观看记录**计算——上游只负责提供候选集，代理做本地交集、本地排序、本地分页
- **本地排序**：支持 `SortName`、`DateCreated`、`ProductionYear`、`CommunityRating`
- **季 / 集 / 搜索建议**接口同步补上本地观看状态覆盖，避免同一部剧在不同页面显示不一致的观看状态
- 新增 `user_filter.go`；`WatchStore` 新增 `GetPlayedItems` / `GetResumableItems`

### 虚拟用户 ID 透传缺陷修复

代理此前把客户端手里的 EIO **虚拟用户 ID** 原样发给上游，上游查不到该用户；`passthrough` 模式下，客户端复合认证头里的**本地 Token 与 UserId** 也会被带进上游请求。本版在代理出站出口统一归一化请求身份，并让普通用户的响应身份与其登录身份一致。实现落在新增的出站准备层（`outbound_identity.go` / `outbound_identity_policy.go` / `outbound_errors.go` / `outbound_log.go` / `upstream_auth_state.go` / `identifier_lookup.go`），`upstream.go` 只做接线。

**请求身份（全部上游请求）**

- **一次请求只冻结一次认证快照**：UserId 与 AccessToken 在同一把锁内读取，URL、body、认证头全部来自同一次快照，不会出现「早先读到的用户 ID 配上稍后取得的 token」
- **已支持接口的 UserId 归一化**：`Items`、`Shows/NextUp`、`Seasons`、`Episodes`、媒体库读取、`Genres/MusicGenres/Studios/Persons/Artists`、`Search/Hints`、`Views`、`/Users/{id}/...`、`PlaybackInfo`、`Sessions/Playing|Progress|Stopped|Capabilities` 等接口声明的 `UserId`（query 与顶层 JSON body）按目标上游身份写入；字段缺省则不新增
- **路径用户段归一化**：只替换业务 path 中完整的 `/Users/{id}` 用户段，不再对整条字符串做 `ReplaceAll`。旧 fallback 的全局字符串替换补丁已删除——它会把路径中任何位置出现的相同文本一起改掉
- **认证头清理**：复合认证头（`X-Emby-Authorization` / `Authorization`）里的 `UserId`、`Token` 与原始凭据在重建时移除，只保留 Client / Device / DeviceId / Version 等设备信息；captured / last-success / latest **所有身份来源**走同一套清理，旧的持久化捕获文件在加载时也会被清洗
- **`api_key` 只在流请求里出现**：客户端发来的 `api_key` / `ApiKey` 等大小写变体一律清除。**只有流请求**（含 redirect 模式的 `Location` 与 HLS 基准 URL）由同一次快照把上游 token 写入 query；普通 API 请求改由认证头认证，不再把 token 放进 URL——这既少了一处凭据暴露面，也不影响上游认证
- **会话事件状态码**：`Progress` / `Stopped` 无法准备请求时不再吞成 204，改为返回准备错误对应的状态码；上游网络失败仍维持原有 204 约定，停止清理在该路径上继续执行，不会因为上报失败而漏放并发名额

**响应身份（普通用户）**

- 新增 `clientFacingUserID`：已认证请求返回该请求自己的用户，仅在完全没有代理用户的公开/内部路径回退到全局占位 ID
- 聚合改写函数（`rewriteItems` / `mergedItemsPayload` / `mergeRoundRobinItems`）改为显式接收响应身份，后台聚合协程不会再拿到全局值
- 后台迟到结果的 ID 登记（`aggregation.go`）**有意保留全局值**——它只登记映射、不生成客户端响应，是生产代码中唯一的例外，已在代码中注明

**日志脱敏**

- redirect 目标、客户端流查询、上游错误里的 URL 一律经脱敏助手输出；`api_key` / `Token` / `Authorization` / 密码类字段、URL userinfo 与 fragment 不落日志
- `Debugf` 同样进入内存日志，因此同样脱敏，不以“只有 debug”为由保留凭据
- **对客户端的错误串同样脱敏**：`net/http` 的**传输错误**（连接失败、超时等）是按请求 URL 拼出来的，而流请求的 URL 含上游 token，此前该错误会原样转发给客户端；现已在出口统一去掉凭据，同时保留 `errors.Is/As` 语义，取消检测不受影响。已用变异验证覆盖（去掉脱敏后测试立即报出明文 token）

### Bug 修复

- **上游「自动 302」开关不生效**：上游配置里的 `followRedirects` 只在解析、写回与管理 API 之间往返，**没有任何请求路径读它**——`http.Client` 从未设置 `CheckRedirect`，因此无论面板选什么，Go 的默认行为（跟随最多 10 跳）恒定生效。该开关在 Node 版是接线的（`emby-client.js` 的 `maxRedirects: 5/0`），Go 重构时丢的。现已接上：`followRedirects: true`（默认）保持原有跟随行为不变，`false` 时请求停在上游的重定向处并按上游错误处理（502），**不会把上游的重定向地址交给客户端**——那个地址可能带上游自己的凭据
- **面板标签「自动 302」更名为「跟随上游重定向」**：原标签只写状态码，且与同一表单里播放模式的「直连模式 (302)」方向相反——一个指上游回 302 时本代理跟不跟，一个指本代理自己回 302。现标签写明动作与方向，选项为「跟随（默认）／不跟随」，并标注实际覆盖的 301/302/303/307/308
- **无 `ParentId` 的聚合列表分页错误**：普通用户请求不带 `ParentId` 的列表时存在两个缺陷——偏移量被应用两次，翻到第 2 页起可能直接返回空列表；`TotalRecordCount` 只报告当页条数、丢失上游总数，导致客户端无法正确分页。现改为由代理统一接管分页并正确回传总数
- **候选集截断无提示**：聚合扫描超过 5000 条时，现在会输出一条节流 WARN 说明总数与列表尾部可能被截断，而不是静默返回不完整的列表
- **`login` / `healthCheck` 超时未接入请求链路**：两个配置项此前不生效，现已接入；默认值由 10000ms 调整为 **30000ms**（与接线前的实际行为一致）。启动时会检查 `login` 或 `healthCheck` 是否小于 `api` 并给出提示
- **超时配置缺少上限**：现约束为单项最长 **1 小时**、健康检查间隔最长 **24 小时**、宽限期最长 **1 分钟**，避免误填极大值使超时形同虚设
- **管理面板宽限期输入框无法关闭功能**：两个宽限期输入的 `min` 由 1000 改为 0，并标注 `0=禁用`
- **管理面板入口多一跳、地址栏重复 `admin`**：`/admin` 原先 302 到 `/admin/admin.html`，地址栏最终停在带文件名的二级路径上。这一跳是被逼出来的——面板自身的资源引用是相对路径（`vendor/vue.global.prod.js`、`admin.js`），只有在以 `/` 结尾的目录 URL 下才能正确解析，而当时的 `/admin/` 返回的是 `http.FileServer` 的**目录列表**（列出 `admin.html`、`admin.js`、`vendor/`），并不是面板。现在 `/admin` 一跳跳到 `/admin/`，由 `/admin/` 直接返回面板内容，目录列表不再暴露；`/admin/admin.html` 继续可用，旧书签不受影响。`admin.html` 本身一行未改，因此磁盘上任何历史版本的面板文件在新入口下都能正常加载
- **安装脚本文件权限**：`install.sh` 与 `emby-in-one-cli.sh` 补齐 `data/` 与 `config.yaml` 的权限设置，避免脚本重写配置后权限被放宽
- **上游排序会让普通用户的「允许服务器」串位**（越权/失权风险）：`handleAdminUpstreamReorder` 只同步了 ID 映射（`IDStore.ReorderServerIndices`），**没有同步每个用户的允许服务器列表**——而该列表存的是**下标**，删除路径是有对应处理的（`UserStore.ShiftServerIndices`），排序这条漏了。面板上点一次上移/下移，普通用户的权限就悄悄指向另一台上游（实测：允许服务器 1 = "B"，排序后下标仍是 1，却已变成 "A"）。现补 `UserStore.ReorderServerIndices`，并与 ID 映射一起封进 `App.remapServerIndices`，两半不再可能被拆开；重映射会写回 `user_servers` 表（只改内存的话重启后权限又跳回去），且**保持「无限制用户」的空列表不变**——`nil`（允许全部）与空列表（不允许任何服务器）在鉴权里不是一回事
- **上游「认证方式」在面板上无法切换**：面板的 `authType` 只是从「`apiKey` 是否为空」**推导出来的展示值**，不是可写字段；后端更新分支里 `apiKey` 与 `password` 都只在非空时覆盖，没有任何分支能清掉另一种凭据，而校验要求两者**恰好其一**。结果 apiKey ↔ 用户名/密码 两个方向都被 400「上游认证方式必须为 apiKey 或 用户名+密码 二选一」挡下，配好之后只能删掉服务器重建。现在 `authType` 是真正的输入：显式声明的那一种凭据生效，另一种被清空；清除动作在逐字段写入**之后**执行，因此声明值胜过请求里顺带带上的旧凭据（面板切换时会带着旧的 `username`）
- **设置页保存后 `api` / `login` / `healthCheck` / `healthInterval` 不生效**：这四个值在构建上游客户端时被读入（`http.Client.Timeout`、客户端的 `timeouts` 字段、健康检查 ticker），而设置页保存走 `commitConfigSettingsOnly`（不重建上游池）——面板提示「保存成功」，运行中的客户端却一直用旧值，直到重启或碰巧改一次上游。现在设置页保存同样重建上游池（`healthInterval` 由这次重建顺带重启的健康检查循环生效），但**不重新登录**：凭据按 `serverKey` 沿用，上游不会被踢下线（已有用例 `TestAdminSettingsUpdateKeepsUpstreamOnlineAfterCommit` 继续守住这一点）。`global` 与三个宽恕期本来就是每请求读快照，行为不变

### 稳定性与可维护性

- **日志轮转**：新增 `logfile.go`，按大小轮转（默认 **10 MiB × 3**），可用 `LOG_MAX_SIZE_MB` / `LOG_KEEP` 调整；进程重启后能正确接续计数，不再从头覆盖
- **去重与收敛**：聚合扇出统一为泛型 `fanOutClients`；流媒体代理的多条路径统一为 `proxyStream`，消除重复实现
- **面板样式移出模板**：原先内联在 `admin.html` 里的样式规则移入构建产物，这也是 CSP 能去掉内联样式许可的前提

### 前端依赖自托管与 CSP 收紧

管理面板此前从四个第三方地址加载资源，其中两个是**浮动版本**——上游发新版会直接改变用户面板行为，且无法复现：

| 依赖 | 原来源 | 现在 |
|---|---|---|
| Tailwind CSS | Play CDN（官方标注仅限开发使用） | 构建期预生成静态 CSS，**407 KB JS → 18.9 KB CSS** |
| Vue 3 | `vue@3`（浮动版本） | 自托管，锁定 **3.5.42** |
| lucide 图标 | `lucide@latest`（浮动版本） | 自托管，锁定 **1.46.0** |
| Inter 字体 | Google Fonts | 自托管 latin 子集（面板是中文界面，Inter 无 CJK 字形，其余回落系统字体） |

新的 `Content-Security-Policy`：

```
default-src 'self'; script-src 'self' 'unsafe-eval'; style-src 'self';
font-src 'self'; img-src 'self' data:; connect-src 'self';
object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'self'
```

- 两个 `'unsafe-inline'` 均已移除，且**不再出现任何第三方来源**：面板既不从外部加载，也不向外部发送
- 同时移除 `X-XSS-Protection` 响应头——浏览器已移除该过滤器，而它当年在 Safari 上启用的遗留审计器反而引入了绕过
- 以上响应头**只作用于 `/admin` 路径，代理流量完全不受影响**
- **保留 `'unsafe-eval'`**：面板使用 DOM 内模板，Vue 需要在运行时编译它。去掉它要把模板改为构建期预编译，属于后续独立改动

### 仓库工程

- **行尾与 BOM 统一**：全仓库 **46 个文件**的行尾/BOM 问题一次性清理，现为 **100% LF、零 BOM**；另有 **14 个 Go 文件**此前未格式化，一并 `gofmt`
- 新增 **`.gitattributes`**（`* text=auto eol=lf` + 显式后缀 + 二进制声明 + `third_party/** -text`）与 **`.editorconfig`**，防止问题复发

### 测试与验证

- 新增测试文件：`user_filter_test.go`、`media_items_pagination_test.go`、`userdata_overlay_test.go`、`admin_panel_assets_test.go`
- 已 fresh 通过以下 Go 定向测试：
  - `go test ./internal/backend -run 'TestUserItems|TestItemsCollection|TestUserDataOverlay' -count=1`
  - `go test ./internal/backend -run 'TestAdminPanel|TestAdminResponses|TestAdminStatus|TestAdminUpstreamList|TestAdminProxyList' -count=1`
- 全量：`go test ./...` 全绿；`go test -race ./internal/backend/` → **414.4s，exit 0**
- 虚拟用户 ID 透传修复新增测试：`outbound_identity_test.go`、`outbound_identity_policy_test.go`、`identifier_lookup_test.go`、`response_identity_test.go`、`outbound_diagnostics_test.go`、`stream_identity_test.go`、`stream_hls_identity_test.go`、`passthrough_identity_test.go`
- 该批验证：`go build ./...` exit 0；`go test ./... -count=1` 全绿；`go test -race ./internal/backend -count=1` → **494.0s，exit 0**，无 DATA RACE
- 上游重定向开关新增 `upstream_redirect_test.go`：默认跟随、关闭时不跟随且回 502 且不把重定向目标当流返回、流请求连接失败时错误串不含上游 token、`errors.Is` 穿透脱敏包装；面板文案用 `admin_panel_assets_test.go` 的用例守住。两处均做变异验证：把标签改回「自动 302」、去掉错误脱敏，对应用例分别失败
- 该项复查后再次全量：`go build ./...` / `go vet ./...` / `gofmt` 干净；`go test ./... -count=1` 全绿；`go test -race ./internal/backend -count=1` → **510.8s，exit 0**，无 DATA RACE
- 面板设置接线复查（上游排序权限、认证方式切换、超时即时生效）新增测试：`admin_settings_reload_test.go`、`admin_upstream_auth_test.go`、`user_store_test.go` 的 `TestUserStoreReorderServers`、`multiuser_test.go` 的 `TestUpstreamReorderKeepsUserPermissionsOnTheSameServer`
- 该批四处均做**变异验证**：设置页保存改回不重建上游池 → 四个超时断言全部失败；移除 `applyDeclaredAuthType` → 两个方向的切换都退回 400；排序只同步 ID 映射 → 权限串回 "A"；索引只改内存不落库 → 重启后的持久化断言失败
- 该批全量：`go build ./...` / `go vet ./...` / `gofmt` 干净；`go test ./... -count=1` 全绿；`go test -race ./internal/backend -count=1` → **537.7s，exit 0**，无 DATA RACE
- 关键修复均做**变异验证**（故意破坏 → 确认测试失败 → 还原），覆盖：CSP 放回 `'unsafe-inline'`、样式表指回 CDN、面板资源改名、磁盘/内嵌回退方向反转、移除反斜杠守卫、移除 `/admin/{$}` 精确路由（确认 `/admin/` 退回目录列表时测试失败）
- **上游解耦持久化 `server_id` 重构验证**：
  - 新增测试：`server_id_migration_test.go`（旧版 SQLite 数据库原子平滑迁移与字段兼容防护）、`server_id_cascade_test.go`（删除上游时的 ID 映射与用户授权级联清理）、`server_id_reorder_test.go`（上游排序零数据库开销与映射授权稳定性）
  - 全量回归：`go test ./...` 100% 通过（ok 28.5s）；`go vet ./...` 0 错误 0 告警；二进制构建成功

### 已知限制

- **`IsUnplayed` / `Likes` / `Dislikes` / `IsFavoriteOrLiked` 对普通用户仍是上游语义**：
  - `IsUnplayed` 的语义是「未观看」= 全部 − 已观看，本地库无法枚举全集（这是补集语义，不是白名单语义）
  - `Likes` / `Dislikes` / `IsFavoriteOrLiked` 本地没有对应数据

  这些筛选会透传上游并返回共享账号的结果，同时给出响应头提示与节流 WARN 日志（仅普通用户，管理员不受影响）
- **`'unsafe-eval'` 仍在 CSP 中**，原因见上
- **管理面板 token 仍存放于 localStorage**：任何同源脚本都可读取。本次收紧 CSP 降低了被注入脚本触发的概率，但没有改变这一点
- **未分类接口的 UserId 保持原值**：动作表之外的 query / body / path 用户值**原样透传**并记录 `unclassified`。这意味着未知接口仍可能把本地 ID 或跨上游真实 ID 发到不理解它的上游；标记不等于修复
- **redirect 会把上游 token 放进直连 URL**：客户端因此可能得知上游真实用户 ID，不能把「客户端永远不知道上游 ID」当作安全前提
- **响应改写器仍按字段名推断语义**：未知接口中的多用户实体需要后续独立适配，替换响应身份参数不等于解决全部响应语义
- **raw body 不是「物理不可改写」**：JSON 声明以外的原始字节按原样转发（含前后空白），已知格式之外的 UserId 本轮不做适配
- **身份来源覆盖有限**：临时 session ID、已删除标识、重置数据库前的缓存值与过期 token 不在查询覆盖内
- **面板「默认播放模式」对已有上游无效（本次未修）**：全局模式只在**新建上游**时作为初始值写入（加载期 `normalizeUpstream` 把全局值填进每个上游），此后该上游的 `PlaybackMode` 非空，`streamPlaybackMode` 的「回退到全局」分支永远走不到；设置页保存又不重建上游池，因此改全局模式**既不立刻生效、重启也不生效**。更麻烦的是这次保存会把当时的旧模式当成「显式覆盖」写进每个上游（序列化条件 `upstream.PlaybackMode != cfg.Playback.Mode`），等于把全局设置**永久钉死**。要改某台上游的模式，请用该服务器的「播放模式」下拉（**即时生效**）。彻底修法需要区分「显式设置」与「继承全局」（空值=继承、校验允许空、序列化只写显式值），留待后续版本；新增服务器对话框的「播放模式」下拉目前也固定显示「代理模式」，与全局默认无关，同属这一处
- **删除网络代理不会清理上游对它的引用**：上游配置里的 `proxyId` 会留下悬空值，运行时静默回退为直连（只写一条日志），面板列表显示「不使用」，但配置文件里始终留着那个已删除的 id

### 文档与版本同步

- `README.md` / `README_EN.md`：当前版本与推荐部署版本同步更新为 **V1.4.4**；补充 `trustProxy` 使用前提与 nginx 配置片段、日志轮转说明、超时参考与「上游服务器显示离线 / 登录超时」FAQ；`followRedirects` 的注释写清实际覆盖的状态码与关闭后的行为
- `docker-compose.yml` / `install.sh` / `emby-in-one-cli.sh`：默认构建版本与 CLI 展示版本同步更新为 **V1.4.4**
- `Update Plan.md`：当前稳定版本同步更新为 **V1.4.4**

### 新功能：多推流线路（`streamingUrls`）

上游配置新增 `streamingUrls` 有序列表（管理面板为多行输入框，每行一条，也支持逗号分隔）：第 1 条为主线路，其余为备用线路。所有线路必须指向**同一台** Emby 服务器——多条线路是到同一服务器的多条路由，不是镜像集群（转码会话存在服务器本地，跨镜像换线路会导致 404）。

- **代理模式——连接级故障转移**：主线路连接失败（连接拒绝 / 超时 / TLS 错误）时自动改用备用线路拉流，客户端无感知；上游返回的任何 HTTP 状态（含 404/403，例如转码会话尚未就绪）**不**视为线路故障，不触发切换
- **直连模式——线路健康选择**：每个健康检查周期对推流线路做连接级存活探测（`GET /Videos/probe`，不带任何凭据）。**任何 HTTP 响应都算存活**——包括只反代 `/Videos/`、`/Audio/` 的分流线路返回的 403/404，不会被误杀；只有 DNS / TCP / TLS 失败才判死。被标记死亡的线路 60 秒内不再选用，之后自动恢复候选；全部被标记死时仍按配置顺序尝试（探测误判不会导致无线路可用）
- 302 直连发出后流量不经过代理，播放中途的线路故障由播放器重新拉取清单时自然切换
- 单条线路配置（或留空 = 与 `url` 相同）行为与旧版完全一致

**代理模式 HLS 清单重写修复（阻塞性）**：`RewriteM3U8ForItem` 输出的分片 URL 恢复为代理相对路径（虚拟 ID + 代理 token），上游主机名与上游 token 不再出现在客户端可见的清单中；无法路由的分片行（非 `/Videos/{id}` / `/Audio/{id}` 形态）原样透传但剥离上游凭据；上游部署在子路径（如 `/emby`）时该前缀不再泄漏进代理路径。测试同步补上主机名断言（旧断言只查子串、恰好被 `127.0.0.1` 测试地址绕过）

**涉及文件（多推流线路）**

| 文件 | 修改内容 |
|------|----------|
| `internal/backend/streamproxy.go` | HLS 清单重写改为代理相对路径；剥离不可路由行的凭据；丢弃上游路径前缀 |
| `internal/backend/config.go` | `StreamingURLs` 有序列表：流式序列解析、渲染、旧键迁移去重、快照深拷贝 |
| `internal/backend/upstream.go` | `StreamBaseURLs` 候选与死亡标记；`doRequestForMode` 拆分单次执行并在 stream 模式按候选故障转移；`BuildURLForMode` 选首个存活线路 |
| `internal/backend/stream_probe.go`（新增） | 连接级存活探测：任何 HTTP 响应即存活 |
| `internal/backend/healthcheck.go` | 健康检查周期接入线路探测（在线上游） |
| `internal/backend/admin_validation.go` / `handlers_admin.go` | `streamingUrls` 输入归一化（换行/逗号）、逐条校验、列表回显 |
| `public/admin.html` / `public/admin.js` | 推流地址多行输入框，数组与文本互转 |
| `internal/backend/streaming_urls_test.go`（新增） | 故障转移、HTTP 状态不触发切换、探测语义（403 存活）、redirect 选线、HLS 集成、配置往返 |
| `internal/backend/streamproxy_test.go` / `media_test.go` | 清单重写断言更新为代理相对路径 + 主机名守卫 |

### 新功能：每用户独立的首页库隐藏

管理员可为**每个用户（含管理员自己）**单独配置哪些媒体库不在 Emby 客户端首页显示，解决多上游聚合后首页库过多的问题。

- **按库勾选，按服务器分组**：管理面板"用户管理"页新增配置入口——每个用户的编辑弹窗内含"首页隐藏库"区块（按服务器分组列出库，每组带全选/全不选）；页面顶部的"管理员（我）"卡片可配置管理员自己的首页。勾掉一台服务器的全部库 = 该服务器从该用户首页整体消失
- **只隐藏首页库入口**：搜索、"最新添加"行、继续观看、接下来观看、播放、详情全部不受影响，被隐藏库的内容保持可达（已知局限：隐藏库的**新增内容仍会出现在"最新添加"行**，因 `/Items/Latest` 响应不携带库归属，逐条查询开销不可接受）
- **过滤覆盖 5 个列库端点**：`/Users/{id}/Views`（主路径）、`/Library/MediaFolders`、`/Library/VirtualFolders`、`/Library/SelectableRemoteLibraries`，以及 `/Users/{id}/Items` 根目录浏览（部分客户端如 Kodi Emby 插件经此旁路列库，按 `UserView`/`CollectionFolder` 类型过滤）
- **实时生效**：隐藏配置不写入 Token（Token 永不过期，快照会导致改配置后必须重新登录），每次请求实时读取内存快照，保存后下一次刷新首页即生效
- **保存契约（Patch 语义）**：提交中某服务器的 key 存在（含空数组 = 全部取消隐藏）则更新该服务器；key 缺席或为 `null` 则完全不触碰——离线服务器的已存配置由此天然保留，不会被误清空
- **库列表 TTL 缓存**：管理面板拉取上游库列表带 10 分钟内存缓存（`?refresh=1` 强制刷新）；服务器离线时回退到缓存副本，无缓存才报错。上游修改连接信息或删除时缓存自动失效
- **数据清理联动**：删除上游服务器 / 删除用户时自动清理对应的隐藏记录；收窄用户"允许服务器"时同步清理不再可访问服务器的记录（空列表 = 全部允许，不触发清理）

**技术实现（首页库隐藏）**

- 存储为共享 SQLite 库新表 `user_hidden_libraries(user_id, server_id, library_id)`，一行一条隐藏记录，无二级索引（复合主键前缀即索引）；管理员配置存于保留常量 `__admin__`（与易失的 `tokens.json` 解耦，用户 ID 为 32 位十六进制永不碰撞）
- 内存缓存采用 COW（`atomic.Pointer` 全量快照）：读路径零锁，写路径先提交 SQLite 事务再深拷贝换快照，任何路径不得原地修改已发布快照
- 过滤发生在 ID 虚拟化之前（此时 `item.Id` 仍是上游原始库 ID，即配置存储的键）

**涉及文件（首页库隐藏）**

| 文件 | 修改内容 |
|------|----------|
| `internal/backend/library_visibility.go`（新增） | 存储层：建表、COW 快照、Patch 写入、清理联动、`__admin__` 映射 |
| `internal/backend/library_filter.go`（新增） | 请求期过滤助手（5 个过滤点共用，含库类型判定） |
| `internal/backend/handlers_admin_home_libraries.go`（新增） | 管理 API：上游库列表（TTL 缓存）、管理员自助读写、Patch 语义应用 |
| `internal/backend/handlers_user.go` | `handleUserViews` 过滤（主路径） |
| `internal/backend/library_image.go` | `handleLibraryNamedArray` / `handleLibraryMediaFolders` 过滤 |
| `internal/backend/media_items.go` | `handleUserItems` 根目录旁路过滤（仅无 ParentId 分支） |
| `internal/backend/handlers_admin.go` | 用户列表回显 `hiddenLibraries`、用户更新接受 Patch 载荷并联动清理、上游更新/删除时缓存失效与记录清理 |
| `internal/backend/server.go` / `routes.go` | App 装配与新路由注册 |
| `public/admin.html` / `public/admin.js` | "管理员（我）"卡片、用户弹窗库勾选区块（仅编辑时显示）、管理员自助弹窗、离线组保留提示 |
| `internal/backend/library_visibility_test.go`（新增） | 存储层：增删改、Patch 清空、持久化、清理、COW 并发（-race） |
| `internal/backend/library_filter_test.go`（新增） | 5 个端点过滤命中/未命中、ParentId 路径不过滤、无 DB 降级 |
| `internal/backend/admin_home_libraries_test.go`（新增） | 库列表端点与 TTL/离线回退、Patch 语义（缺席/null/空数组）、用户更新回显、删除联动、权限 403 |

### 修复：安装脚本与 SSH 菜单在三处场景下的致命问题

- **全新安装失败（`无法创建专用运行用户 eio` 后回滚）**：安装脚本先用 `groupadd` 建了同名组，随后 `useradd` 未带 `-g`，其默认行为是再建一个同名用户组，撞上已存在的组即失败（真实报错 `group eio exists - if you want to add this user to that group, use -g.` 被 `2>/dev/null` 吞掉，只显示笼统的"无法创建"）。现组已存在时显式传 `-g`，组不存在时仍由 `useradd` 自建
- **无版本参数一键安装卡在旧稳定版的校验环节**：V1.4.4 正式版发布前，不指定版本的安装会经 `releases/latest` 解析到 V1.4.3，而 `.sha256` 校验和产物自 V1.4.4-rc1 起才随 Release 发布——旧版 Release 无校验和可校验。现对 V1.4.3 及以下版本默认跳过完整性校验（输出一条明确警告），保证一键安装可用；V1.4.4-rc1 及以上版本仍强制校验
- **SSH 菜单改密码/改账号后服务崩溃循环（`open config/config.yaml: permission denied`）**：菜单以 root 运行，而服务以专用用户 `eio`（binary 部署）或 uid 1000（Docker 部署）运行。选项 8（改密码）经内置 `--reset-password` 重写 `config.yaml` 与 `tokens.json`、选项 9（改账号）经 `awk+mv` 重写 `config.yaml`，root 重写后文件属主变为 root，服务重启即因读权限被拒而崩溃循环。两层修复：
  - **二进制层（治本）**：`WriteFileAtomic` 以 root 运行时在 rename 前把原文件的 uid/gid 转移到临时文件，原子写不再改变属主——覆盖 `--reset-password` 与运行期全部落盘路径
  - **菜单脚本层（兼容已部署的旧版二进制）**：新增 `restore_ownership`，选项 8/9 与在线更新写入后按 systemd unit 的 `User=`（无 systemd 时退回目录属主）还原属主；Docker 模式还原为 `1000:1000`

**涉及文件（安装与菜单修复）**

| 文件 | 修改内容 |
|------|----------|
| `release-install.sh` | 组已存在时 `useradd` 显式 `-g`，修复全新安装必失败；新增 `version_lte` 版本比较，V1.4.3 及以下默认跳过 sha256 校验（旧 Release 无校验和产物） |
| `internal/backend/atomicfile.go` | `WriteFileAtomic` rename 前调用 `preserveOwner` |
| `internal/backend/atomicfile_owner_unix.go`（新增）/ `atomicfile_owner_windows.go`（新增） | 属主保留的平台实现（Windows 为空操作） |
| `internal/backend/atomicfile_owner_unix_test.go`（新增） | root 下跨用户属主保留的回归测试（非 root 自动跳过） |
| `emby-in-one-cli.sh` | 新增 `restore_ownership` 并接入选项 8/9 与二进制在线更新 |

---

## V1.4.3

发布日期：2026-04-27

> V1.4.3 为 V1.4.2 的补丁版本，重点修复代理模式下独立字幕流仍暴露上游 `api_key`、导致部分 Emby 客户端选择字幕后自动回退为“无字幕”的问题，并同步更新对外版本号。

### Bug 修复

- 修复代理模式下 `PlaybackInfo` 返回的 `MediaStreams[].DeliveryUrl` 仅重写 item / `MediaSourceId`、却保留上游 `api_key` 的问题：现在会统一替换为代理 token，避免客户端后续拉取独立字幕流时因鉴权失败而自动取消字幕选择
- 修复 `.ass` 外挂字幕在部分 Emby 客户端中选择后立即回退为“无字幕”的问题；同类走 `/Subtitles/.../Stream.xxx` 独立字幕流链路的格式（如 `.ssa`、`.srt`、`.vtt` 等）也一并受益

### 测试与验证

- 已 fresh 通过以下 Go 定向测试：
  - `go test ./internal/backend -run 'TestPlaybackInfoAndMasterPlaylistProxy|TestPlaybackInfoRewritesSubtitleDeliveryURLForASSTracks|TestSubtitlePathResolvesMediaSourceID|TestVideoStreamFailsExplicitlyWhenMediaSourceTargetServerIsUnavailable' -count=1`

### 文档与版本同步

- `README.md` / `README_EN.md`：当前版本与推荐部署版本同步更新为 **V1.4.3**
- `docker-compose.yml` / `install.sh` / `emby-in-one-cli.sh`：默认构建版本与 CLI 展示版本同步更新为 **V1.4.3**
- `Update Plan.md`：当前稳定版本同步更新为 **V1.4.3**

---

## V1.4.2

发布日期：2026-04-20

> V1.4.2 为 V1.4.1 的补丁版本，重点修复播放状态双写对上游失败过度敏感的问题，并收紧跨服 `MediaSourceId` 的流路由行为，补充 PlaybackInfo 诊断日志。

### Bug 修复

- 修复普通用户标记已播放 / 未播放时，本地 `WatchStore` 状态更新会被上游 `UserData` HTML / 502 错误连带阻断的问题：本地 Played 状态现可独立落盘，不再依赖上游成功
- 修复 `WatchStore.MarkPlayed()` 仅执行 `UPDATE`、在无现有记录时无法创建播放状态的问题：现已改为 UPSERT，可直接写入首条 Played/Unplayed 记录
- 修复 stream 路径中 `MediaSourceId` 明确指向其他上游但目标服务器不可用时，代码仍可能继续沿用原服务器尝试请求的问题：现改为显式返回 `Target media source server is unavailable`
- 为 `PlaybackInfo` 增加按实例失败日志，记录具体失败的上游服务器与原始 item id，便于定位 `all upstream requests failed` 的真实来源

### 测试与验证

- 已 fresh 通过以下 Go 定向测试：
  - `go test ./internal/backend -run 'TestWatchStoreMarkPlayedUpsertsWithoutPriorRecord|TestUserDataPlayedStillUpdatesLocalStateWhenUpstreamFails|TestVideoStreamFailsExplicitlyWhenMediaSourceTargetServerIsUnavailable|TestPlaybackInfoAndMasterPlaylistProxy|TestAdminUpstream(Create|Update)PassthroughWithoutCapturedHeadersDoesNotCallUpstream|TestPassthrough(Reconnect|AuthErrorRecovery)WithoutCapturedHeadersDoesNotCallUpstream' -count=1`

---

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
