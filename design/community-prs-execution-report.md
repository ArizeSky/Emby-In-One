# 五个社区 PR 本地整合执行报告

> 对应 `design/community-prs-code-execution-plan.md` 与审查文档 `design/community-prs-integration-review.md`。
> 本报告如实区分**已完成并实测通过**与**仍待实现**，不夸大完成度。
> 当前进度：审查第 5 节第一至第五阶段的**验收用例全部通过**（审查迁移的 22 个顶层用例 + 既有全套），全量 `go test`（含 `-race`）、`go build/vet/gofmt`、`npm test`、`npm run build:panel`、`git diff --check` 均通过。计划中少数深层项（HLS 外部资源本地代理、fallback 路由分类、父级继承、结构化上游错误/聚合报告/请求预算、`FollowRedirects×DirectRedirect` 全矩阵）仍为待办，见第 6 节。

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

## 5. 明确仍未实现（诚实边界，不可视为完成）

以下为计划正文要求、当前**未实现**的深层项（不影响上述已迁入验收用例通过，但属于完整方案的一部分）：

- **§8.6.1 HLS 外部资源本地代理**：未拆 `stream_resource_store.go`/`stream_resource_proxy.go`；外部 HLS segment/key/MAP/嵌套 manifest 的本地 opaque 映射、逐请求授权、会话租约续期、容量准入 503 未实现。受限用户强制走代理亦未强制。
- **§8.7 fallback 路由分类**：`parental_routes.go`、受限用户未分类内容读取 403 `ContentRouteNotSupportedWithParentalControl` 未实现。
- **§8.3 父级继承/循环/失败**：父级 rating 继承链未实现。
- **§6.2 结构化上游错误与聚合报告**：未引入 `upstreamResponseError{StatusCode}`（按 `errors.As` 判定）、`upstreamItemsResult{Complete,Truncated}`、聚合报告与 `EioIncompleteResults`/`X-EIO-Results-Incomplete` 不完整协议、请求预算/分层批查；当前窗口降级按“首屏错误即重试”处理，未严格区分 400 与 401/403/429/5xx。
- **§7 全矩阵**：`FollowRedirects×DirectRedirect` 全表、可信 origin 集合与自环判定仍为基础版。
- **§6.4 非 Primary 图片类型**：补图仅 Primary；Backdrop/Logo/Thumb/父图的来源路由未参数化。
- **§8.2 原子 Create/Update 与用户字段同事务**：当前 `block_unrated_items` 与 `max_parental_rating` 两列原子写，但与用户名/密码的创建/更新不在同一事务；`CreateWithPolicy/UpdateWithPolicy` 完整形态未落地。
- **§8.4 PlaybackInfo 逐实例分级**：`handlePlaybackInfo` 尚未在选定实例上做分级拦截（播放/详情/列表已做）。
- **§8.9 部分结果 UI 提示**：面板未展示“部分结果”提示。

## 6. 未验证 / 边界声明

- 未执行真实 Android TV / montage / 网盘 / 真实 HLS 客户端验收；未联网访问真实 Emby/TMDB。
- Docker 工作流仅静态核对，未运行、未发布镜像。
- 未运行 `git commit/merge/push`、未创建/合并 PR、未发布。改动仅本地、未提交。

## 7. 交接状态

- 分支 `codex/community-prs-local-review`，基于 `752253a`，改动已暂存、未提交。
- 新增文件：`.github/workflows/docker.yml`、`internal/backend/{media_prefixes,parental,outbound_credentials,parental_policy_test,pr_review_test,pr23_review_test,pr18_review_test,integration_review_test,stream_redirect_test,token_query_test,upstream_window_test}.go`、`design/community-prs-execution-report.md`、`design/community-prs-route-coverage.md`；并修改认证/出站/媒体/流/图片/用户存储/管理端/面板等文件。

## 8. 结论

- **审查迁移的验收用例已全部通过**，全套 `go test`（含 `-race`）、`build/vet/gofmt`、`npm test`、`build:panel`、diff 检查均绿。
- **计划深层项仍有待办**（第 5 节列明）：HLS 本地资源代理、fallback 分类、父级继承、结构化错误/聚合报告/预算、跳转全矩阵、非 Primary 图片类型、PlaybackInfo 逐实例分级、原子 Create/Update、部分结果 UI。这些是后续工作，不虚报为已完成。
