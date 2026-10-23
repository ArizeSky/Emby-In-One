# 五个社区 PR 本地整合执行报告（R1–R9 修复）

更新：2026-10-23。本次直接修复 `community-prs-c1-c10-rereview.md` 的 R1–R9。此前 C1–C10 的成果保留；此前“固定回归通过即可整体验收”的结论已由复审纠正。本文替换旧执行报告，历史版本保留在 Git 中。

## 1. 执行范围

- 工作树：`D:\UserData\Desktop\Emby-In-One-pr-integration`。
- 分支：`codex/community-prs-local-review`；起点 `99bbba87d6810d448cafaf15991f67d3441dfc4f`。
- 仅推送 Gitea `origin` 当前任务分支；每个步骤验证后独立提交并立即推送。没有推送 GitHub、合并 main、改写历史、发布或修改真实配置/数据。
- 原工作树 `Emby-In-One-main` 的业务代码不变；本次没有重新套用五份 PR 补丁。
- 复审新增的 `third_review_test.go` 原样迁入正式仓库，保留 10 个顶层测试及原断言；前序 `final_review_test.go` 的 12 项及四个 review 文件中的 22 项保留。

## 2. R1–R9 对应实现与证据

| 问题 | 修复位置和实际行为 | 主要回归 |
| --- | --- | --- |
| R1 显式未知分级被继承覆盖 | `parental_read.go` 的 `classifyItemRating`、`decideConfirmedItem` 与 `parental_access.go` 的 `checkInstanceParentalAccess` 共用状态；非空 CustomRating 优先且未知时终止继承，只有缺失才查父级 | `TestThirdReviewExplicitUnknownRatingCannotInheritToAllowStream`；实际视频入口返回 403 |
| R2 Resume/NextUp 读取泄露 | `media_resume.go`、`watch_parental_metadata.go` 在真实来源的原始 DTO 上过滤；`watch_parental.go` 先找观看游标，再过滤后续候选；拒绝后继续扫描，不让拒绝项占满 Limit；本地观看存储不可用时，上游回退路径同样过滤 | 原始 LocalWatch 子用例；`TestParentalResumeRefillsAfterDeniedPage` 的 100 个拒绝项后允许项；`TestParentalNextUpRefillsAfterDeniedEpisode` 的单系列/首页；`TestParentalWatchRoutesWithoutLocalStoreStillFilter` 的四种回退路径 |
| R3 未分级和继承结果误拒 | 明确区分稀疏缺失、显式未知、确认未分级、查询失败；Movie 批量资料仍缺失时详情回退；`item_detail.go` 不再用纯 DTO 检查覆盖逐实例允许结论 | `TestThirdReviewConfirmedUnratedRemainsVisible`、`TestThirdReviewMergedEpisodeUsesInheritedAllowedRating` |
| R4 父级重复查询耗尽预算 | `parental_session.go` 按实际 client 指针与原始 ID 缓存本请求原始资料/错误，并对并发同键合并读取；`parental_metadata.go` 分服务器及父级层次批量补查 | 80 集共享一个 PG 父级全部返回，父级详情调用恰好 1 次；32 个并发同键调用恰好 1 次；不同 client/请求不复用 |
| R5 查询失败伪装空库 | `parentalResponseReady` 从请求中的允许/无法确认统计决定响应；全部无法确认且无允许结果为 502；有安全结果为 200，带不完整响应头及对象字段；全部明确拒绝仍是完整空列表 | 原始 UnconfirmedCatalog；`TestParentalSessionPartialAndDeniedLists`；Resume 补资料失败为 502 |
| R6 受禁展示副本挤掉允许副本 | `parentalVisibleResults` 先登记身份事实，再逐真实副本过滤，最后仅以允许资料选展示/补图；Seasons/Episodes 单独登记索引身份，过滤后选资料并记录实际图片来源；Similar/ThemeMedia 在 ID 重写前按真实 client 过滤 | 原始 AllowedSibling；原有合并详情和图片回归；同号上游 Resume 资料隔离回归 |
| R7 总数/分页错误 | 无限制 ParentId 透传保留上游 TotalRecordCount；受限 ParentId 经共享目录扫描、过滤后本地分页，候选截断时明确不完整 | 原始管理员 Limit=2/total=100；`TestParentalContainerFiltersBeforePagination` 的第二个允许项和截断标记 |
| R8 过滤破坏排序 | 批量确认后按原始 target 顺序保留允许项，不把缺字段项追加到后面 | `TestThirdReviewFilterPreservesOrder` |
| R9 HLS 查询参数窗口冲突 | `stream_manifest_identity.go` 为首层清单保留原请求路径与查询语义，仅排除代理认证参数；子清单以绑定资源 handle 为身份；无显式身份时保留完整 URL query。相对资源仍按最终响应 URL 解析。窗口记录也受容量限制，拒绝时不更新任何窗口或新增 handle | 原始 HLSQueryDistinctWindows；`TestHLSWindowSignedRedirectsRetireOnlyTheirOwnTrack`；`TestHLSWindowCapacityIsAtomicAndReclaimed` |

## 3. 请求共享状态与预算的准确含义

- `withContext` 在 HTTP 请求开始读取一份本地分级策略；该请求的 goroutine 共用不可变策略和同一个 `parentalReadSession`。下一个请求重新读取当前策略。
- 元数据只在本请求缓存，不跨用户/请求保存“允许”决定。client 指针区分同号但不同上游/账号实例；上限为 20000 条本请求资料记录。同键并发读取等待同一次结果。
- 分级批量/详情/父级补查共享 50 次调用预算，且共同消耗目录扫描的 100 次调用预算；分级批次 ≤200 ID，补查并发槽为 4。这里统计业务调用，不声称认证协议内部重试也严格只产生 100 个网络请求。
- ThemeMedia 三个嵌套列表共用上述 50 次预算，不会各自重置；回归实际统计 60 个未知条目只发出 50 次补查调用，并返回安全部分和不完整标记。
- 根目录扫描、受限 ParentId 扫描、受限季/集分页、受限 NextUp 和 Resume 元数据读取共享本请求目录预算。单独入口原有错误/超时处理保留，不宣称重写了全部路由的端到端 deadline。
- 结构性导航容器不因缺少影视分级而隐藏；Episode/Season 缺失分级才在同服父链最多查 3 层，显式未知终止继承。
- 受限 ParentId 的计数是已扫描允许候选数；候选不全时有 `X-EIO-Results-Incomplete:true` 和 `EioIncompleteResults:true`，不能当作完整库精确总数。
- HLS 同会话资源 handle 与窗口记录分别最多 16384；整个 manifest 的准入原子执行。会话空闲 30 分钟释放；滚动窗口移出资源保留 2 分钟；VOD 租约、授权后续租与原有隔离规则保留。

## 4. 实际验证

日志位于 `C:\Users\Administrator\AppData\Local\Temp\eio-repair-20261023`；没有使用 overlay、跳过原断言或将日志提交仓库。

| 检查 | 结果/日志 |
| --- | --- |
| 完整 `go test -p 1 -parallel 2 ./... -count=1 -timeout=15m` | 通过；cmd 0.603s、backend 60.490s；`go-all.log`、`go-all.exit=0` |
| 第一步分级/继承/详情回归 | 通过；`step1.log` |
| 请求状态、读取、预算及旧边界定向 race | 通过，52.070s；`step2-verified.log` |
| Resume/NextUp、单库分页及相关读取定向 race | 通过，103.296s；`step3-race.log` |
| 全部 10 项第三轮审查用例、HLS 身份/容量及资源生命周期定向 race | 通过，28.768s；`step4-race.log` |
| 观看存储不可用的回退路径、既有上游首选源行为定向 race | 通过，19.971s；`watch-fallback-race.log` |
| `go vet ./...` / `go build ./...` | 均通过；`vet.log`、`build.log` |
| `gofmt -l cmd internal` / `git diff --check` | 空输出/通过 |
| `npm test` | 68/68；`npm-final.log` |

新增补充测试为 `parental_session_test.go` 4 项、`watch_parental_test.go` 5 项、`stream_manifest_identity_test.go` 2 项，共 11 项。它们与原样迁入的 10 项、已有 12 项/22 项一同保留。中间日志 `step3-initial.log` 唯一失败是当时尚待第四步处理的 HLSQueryDistinctWindows；最终结果以完整套件与后续 race 日志为准。

## 5. 分步提交

| 提交 | 步骤 | 推送 |
| --- | --- | --- |
| `59102a4` | 统一显式未知/缺失继承，移除合并详情重复拒绝，迁入第三轮 10 项测试 | 已推送 origin |
| `8609b92` | 请求共享资料/预算、父级合批与 memo、可见副本视图、完整性响应、排序与无限制总数 | 已推送 origin |
| `6bf8a07` | 本地观看入口过滤后补足候选；真实服务器资料隔离；受限单库过滤后分页 | 已推送 origin |
| `d82abbc` | HLS 稳定窗口身份、保留 query 语义、窗口容量原子准入与清理 | 已推送 origin |
| `e6a47f5` | 本地观看存储不可用时，Resume/NextUp 上游回退仍执行分级检查 | 已推送 origin |

两次推送出现 DNS 失败，均等待后正常重试成功；未改 DNS/WireGuard、TLS 或远程地址。文档提交在代码验证后单独提交并立即推送。

## 6. 完成边界

本轮交付是 R1–R9 的代码修复和对应自动化验证；不是五个 PR 原始架构方案的所有条款已经全部实现。

- 本请求 memo/同键并发合并已实现；原方案跨请求 8192 条/TTL30s 元数据缓存与显式 clientGeneration 缓存失效机制尚未实现。
- 身份登记、现有 verified alias 状态兼容保留；没有重写整个 IDStore 的原子 canonical 建组/持久化 API。
- HLS 无依据时保留不同 query 的不同身份，不猜测任意上游签名参数的等价关系；大量不同入口仍可能触发明确的容量 503，而不会无界累积窗口。
- 未重新跑全仓库 race；已实际跑上述涉及本次并发改动的定向 race。未改前端，未重建面板。
- 未连接真实 Emby/TMDB、真实 TV/手机/HLS 播放器，未做生产压测、Docker 构建/发布或 GitHub PR 合并。
- 路由接线、授权顺序和响应边界见 `community-prs-route-coverage.md`。
