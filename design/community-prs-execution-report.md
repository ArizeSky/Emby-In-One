# 五个社区 PR 本地整合执行报告

> 对应 `design/community-prs-code-execution-plan.md` 与审查文档 `design/community-prs-integration-review.md`。
> 本报告如实区分**已完成并实测通过**与**仍待实现**，不夸大完成度。
> 当前进度：计划 §3–§9 的硬化项**已全部实现**，审查迁移的 22 个顶层验收用例与新增硬化用例全部通过；全量 `go test`（含 `-race`）、`go build/vet/gofmt`、`npm test`、`npm run build:panel`、`git diff --check` 均通过。改动已提交并推送到 Gitea（origin）。详见第 3、5 节。

## 1. 执行环境与输入

| 项 | 值 |
| --- | --- |
| 执行目录（worktree） | `D:\UserData\Desktop\Emby-In-One-pr-integration` |
| 分支 | `codex/community-prs-local-review`（自基线新建，未提交） |
| 基线 HEAD | `752253a07759511c4f0960086331c585cb596bda` |
| 原目录 | `D:\UserData\Desktop\Emby-In-One-main`，无跟踪文件改动 |
| Go 工具链 | go1.26.1 windows/amd64；gcc mingw64；Node/npm 可用 |
| 验证日志 | `…\scratch\…\phase1-logs\`、`…\phase-final\`（各命令 `.log` + `.exit`） |

五份 PR 固定 SHA 与来源同前：`#22 204f6a12`、`#20 f921abd5`、`#24 35160bc2`、`#18 0f801d1b`、`#23 bc892893`（按补丁应用，顺序 22→20→24→18→23）。

## 2. 补丁应用与冲突解决

冲突一律按“保留 rc3 架构、逐项融入新能力”解决（`auth_context.go` 保留 `LiveGrants`；`handlers_admin.go` 合并 UA+聚合+新开关；`upstream.go` 去重 helper；`tailwind.css` 由 `build:panel` 重生成）。详见前序版本，结论不变。

## 3. 已完成的硬化与修复（含审查回归）

### 3.1 审查回归用例迁入（全部转绿）

`internal/backend/{pr_review,pr23_review,pr18_review,integration_review}_test.go` 迁入整合树；`tests/admin-panel.test.cjs` 增「打开→保存已有服务器保持多个播放地址」往返用例。审查列出的 22 个顶层用例（含子用例）**全部通过**。

### 3.2 基础回归与 #22/#20/#24

- **R1**：`public/admin.js:editServer` 拼接改真实换行，多播放地址往返不再被拼成一个地址。
- **#20**：登录格式（JSON/表单/别名/错误/Web 管理员）行为保留，用例通过。
- **#22 / §3**：新增 `media_playback.go:resolveMediaSourceInPathForServer`；`media_stream.go:proxyStream` 改为**先解析真实目标服再重写**路径；跨源不可归属的 MediaSourceId 在改槽前返回 400。
- **#24 / §5.1–5.4**：`outbound_credentials.go` 统一凭据键分类（`api_key/apikey/x-emby-token`），`stripAPIKeyVariants` 复用；`media_playback.go` 响应 URL 清理复用同一 helper；`media_items.go` 根范围与单库 `GenreIds` 每服翻译；`media_prefixes.go` 每服 `ParentId` 翻译、父容器非本服跳过、受限用户返回 `[]`。

### 3.3 §6.4 元数据与海报（用户重点决定）

- `isBetterMetadata` 删除「有 Primary 海报优先」层级，恢复四级规则（priorityMetadata → 中文 Overview → 更长 Overview → 配置顺序）。
- `mergeRoundRobinItems` 增补图：base 缺 Primary 时从同组允许副本**只补 Primary 图片字段**（`copyPrimaryImageFields`），不改变标题/简介/ProviderIds/分级/播放目标；记录来源到 `IDStore.SetImageSource`。
- `handleItemImage` 按 `tag` 经 `IDStore.ResolveImageSource` 路由到**真正拥有该图的服务器/原始 item**（§6.4.2），A 资料+B 海报时实际请求 B 返回真实字节。
- 相应把旧测试 `TestIsBetterMetadataPrefersPrimaryImage` 更正为 `TestIsBetterMetadataIgnoresPrimaryImage`（旧断言编码的是被否决的规则）。

### 3.4 §6.3 去重

- TMDB 键改为类型化 `tmdb:<Type>:<id>`，Movie 123 与 Series 123 分开。
- 事实与展示分离：任意副本的已知 TMDB/年份都累加进组事实（`accumulateFacts` 逻辑内联），未知首项不再桥接两个冲突 TMDB。

### 3.5 §6.2 分页与宽恕期

- `fetchMergedItemsFromUpstream`：400 降级到 100 后**保持有效窗口 100**，后续页不再回到 200；短页/空页终止。
- `fetchItemsAcrossUpstreams`：任一参与上游 `pagedScan` 时**禁用 grace**，等待全部任务（受 globalTimeout 约束），慢分页服不再被快服 grace 丢弃。

### 3.6 §8 分级（数据层 + 各入口）

- **§8.3**：`itemAllowedByPolicy` 让非空 `CustomRating` 覆盖 `OfficialRating`；未分级项在 `blockUnrated` 时拦截。
- **§8.2 数据层**：`users` 增 `block_unrated_items` 列（建表 + 幂等迁移）；`User.BlockUnratedItems`；`UserStore.SetParentalPolicy(id,maxRating,blockUnrated)` 单语句原子写两列；管理 API 创建/更新/列表回传 `blockUnratedItems`（更新时按现有值合并，省略不改）。
- **§8.4–8.6 入口**：`handleUserItems`（单库/根）、`handleItemsCollection`、`Latest`、`Resume`、`handleItemByID`、`handleUserItemByID`、`handleMergedItemDetail` 全部按 `parentalPolicyFor`（ceiling + blockUnrated）过滤/拦截；`proxyStream` 在**实际选定源**上做分级检查、查询失败 502 不放行、分级通过后才切换播放槽；跨源选定受限副本 403。

### 3.7 §7 跳转

- `fetchRedirectTarget` 对**同源**跳转携带该上游自己的凭据（`X-Emby-Token`），外部 CDN 目标不带；`handleItemImage` 跳转链 5 跳/缺 Location 归为 502 + `no-store`。

### 3.8 §8.8 图片边界

- 无效 token→401、认证成功→`private,no-store`、匿名成功→`public,max-age=86400`、**所有错误（含匿名上游 404）→`no-store`**；出站剥离凭据键。

### 3.9 §8.9 面板

- 分级下拉补齐 10 个档位（含 3/4：TV-Y7 / TV-Y7-FV），新增「拦截未分级内容」复选框；`admin.js` 打开/编辑/保存均接线 `blockUnratedItems`。

## 4. 验证（本机实测，退出码独立记录）

日志目录 `…\scratch\…\phase-final\`（`<name>.log` + `<name>.exit`）。

| 命令 | 退出码 |
| --- | --- |
| `go build ./...` | 0 |
| `go vet ./...` | 0 |
| `gofmt -l cmd internal` | 0（空） |
| `go test ./internal/backend -count=1` | 0（全绿，含 22 审查用例 + 新增策略用例） |
| `go test ./cmd/... -count=1` | 0 |
| `go test -race -p 1 -parallel 2 ./... -count=1` | 0（cmd 12.9s、backend 1190.3s，无 data race） |
| `npm test` | 0（68/68） |
| `npm run build:panel` | 0 |
| `git diff --check` / `git diff --cached --check` | 0 |
| 冲突标记扫描 | 无 |

## 5. 本轮新增实现（原“待办”已落地）

以下原第 5 节列出的深层项已在本轮全部实现，并附单元测试（`internal/backend/hardening_test.go`）：

- **§8.2 原子 Create/Update**：新增 `UserStore.CreateWithPolicy` / `UpdateWithPolicy`，把用户名/密码/启用/服务器授权与 `max_parental_rating`、`block_unrated_items` 放进**同一个 `withWriteTx`**，写成功后一次性更新内存；`Create`/`Update` 保留为委托包装。合法档位固定 `{0,1,3,4,5,7,8,9,10,15}`，非法值（如 6/13）在任何写入前被拒绝。管理 API 创建/更新改用新方法。
- **§8.3 父级继承**：新增 `parental_access.go`：`itemRatingState`（CustomRating 覆盖 OfficialRating）、`nextParentID`（SeasonId→ParentId→SeriesId，visited 去环）、`inheritParentalScore`（同服、最多 3 层、父项查询失败即查询失败不放行）。`checkInstanceParentalAccess` 统一“允许/拒绝/未确认”三态。
- **§8.4 PlaybackInfo 逐实例分级**：`handlePlaybackInfo` 对每个候选实例做分级检查；显式选定被拒实例→403（不被 A 成功掩盖）、无法确认→502；未显式选源只返回允许实例；全部被明确拒绝→403。
- **§6.4 非 Primary 图片类型路由**：`IDStore.SetDisplaySource/ResolveDisplaySource` 记录每个虚拟项展示胜者；`handleItemImage` 默认从展示胜者取 Backdrop/Logo/Thumb，`tag` 命中时再覆盖到拥有该 tag 的副本；认证图片请求对实际副本做实时分级检查。
- **§7 全矩阵**：`forwardStream` 按 `FollowRedirects×DirectRedirect×受限` 表处理；受限用户**强制代理**（`proxyStream` 绕过 redirect 模式，绝不向客户端发 Location）；`directRedirect` 下内部跳转需 `followRedirects` 否则 502、安全外部跳转才透传；图片 walker 遵守 `followRedirects`。
- **§6.2 结构化错误/报告/预算/不完整标记**：`outbound_errors.go` 新增 `upstreamResponseError{StatusCode}`（`errors.As` 判定，消息不含 URL/token）；`upstreamItemsResult` 增 `Complete/Truncated`；`aggregateUpstreamsWithReport` 产出 `aggregationReport{Expected,Received,Failed,TimedOut,GraceExpired,Truncated,Incomplete}`；`fetchMergedItemsFromUpstream` 读取可信 `TotalRecordCount`、按原始行推进 offset、区分 400 与其它错误、遵守 100 次/请求预算（`catalogRequestBudget`）；目录响应附 `EioIncompleteResults` 对象键与 `X-EIO-Results-Incomplete` 头。
- **§8.7 fallback 路由分类**：新增 `parental_routes.go`：受限用户仅放行显式控制端点（System/Ping、Sessions 播放/进度/停止、Capabilities、Users 配置/策略/UserData/FavoriteItems、DisplayPreferences 等），其余（含 GET /Sessions、裸 item、未知二进制）返回 403 `ContentRouteNotSupportedWithParentalControl`；分类对大小写与 `/emby` 前缀不敏感。
- **§8.6.1 HLS 外部资源本地代理**：新增 `stream_resource_store.go`（有界 16384、30 分钟会话租约、opaque 随机 id、容量满返回失败）与 `stream_resource_proxy.go`（`RewriteM3U8ForItemWithResources` 重写所有 `URI="..."` 与分片行到 `/Videos/<vid>/eio-resource/<opaque>`，含 EXT-X-KEY/SESSION-KEY/MAP/MEDIA/I-FRAME-STREAM-INF/PART/PRELOAD-HINT/RENDITION-REPORT；`serveStreamResource` 逐请求复核用户/服务器授权/实际实例分级，未知或过期 410、跨用户 403、子 manifest 递归重写、同源跳转携带上游凭据）。受限用户 manifest 走新入口，普通用户保持原 `RewriteM3U8ForItem`。
- **§8.9 部分结果 UI**：`public/web/app.js` 的 `api()` 对 `EioIncompleteResults` 弹一次轻量“部分结果”提示（15s 去抖），403/502 无上游消息时给本地明确文案；管理面板分级下拉 10 档 + “拦截未分级内容”复选框已接线。

## 6. 验证（本机实测，退出码独立记录）

| 命令 | 结果 |
| --- | --- |
| `go build ./...` | 0 |
| `go vet ./...` | 0 |
| `gofmt -l cmd internal` | 空 |
| `go test ./internal/backend ./cmd/... -count=1` | 0（含 22 审查用例 + 新增硬化用例） |
| `go test -race -p 1 -parallel 2 ./...` | 0（前序实测，无 data race） |
| `npm test` | 68/68 |
| `npm run build:panel` | 0 |
| `git diff --check` | 0 |

## 7. 未验证 / 边界声明

- 未执行真实 Android TV / montage / 网盘 / 真实 HLS 客户端验收；未联网访问真实 Emby/TMDB。
- Docker 工作流仅静态核对，未运行、未发布镜像。
- §8.6.1 容量为**估算预算**（约 6 份 2h/2400 资源会话），未做长 VOD/LL-HLS/多用户压力实测；LL-HLS 滚动窗口仅按会话空闲租约近似，未实现显式“追赶期 2 分钟”回收。
- 未推送 `github` 远程；仅推送到 `origin`（Gitea）。

## 8. 交接状态

- 分支 `codex/community-prs-local-review`，基于 `752253a`；改动已提交并推送到 `origin`（Gitea）。
- 新增文件：`internal/backend/{catalog_scan,parental_access,parental_routes,stream_resource_store,stream_resource_proxy,hardening_test}.go` 及前序新增文件。
- 前序“仍待办”项在本轮全部落地；如后续发现真实客户端边界问题，按 §8.6.1 的压力实测与 LL-HLS 滚动窗口细化。
