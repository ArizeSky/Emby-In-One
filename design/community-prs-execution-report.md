# 五个社区 PR 本地整合执行报告

> 更新：2026-10-23。本文替代此前“计划 §3–§9 全部完成”的过度结论。
> 当前可重复的结论：完成评审 C1–C10 对应的缺陷修复，原始 12 项边界回归已正式入库并全部通过；前序 22 项审查测试包含在完整 Go 套件中且保持通过。**不据此宣称所有真实客户端、生产容量或原方案全部条款均已验收。**

## 1. 工作树与来源

| 项目 | 当前状态 |
| --- | --- |
| 执行 worktree | `D:\UserData\Desktop\Emby-In-One-pr-integration` |
| 分支 | `codex/community-prs-local-review` |
| 本轮修复起点 | `1f5ad4d`；保留前序提交历史，不改写 |
| 前序整合基线 | `752253a07759511c4f0960086331c585cb596bda` |
| 唯一推送远程 | `origin`：`https://gitea.wmr.ovh/MineNine/Emby-In-One.git` |
| 原工作树 | `D:\UserData\Desktop\Emby-In-One-main`；本轮未改动其文件，保留已有 `AGENT.md` 等本地内容 |
| 评审输入 | 原工作树 `design/community-prs-completion-review.md`（C1–C10）、执行方案与前序整合审查 |
| 工具链 | Go `E:\Program Files\Go\bin\go.exe`；race 使用 `C:\Program Files\mingw64\bin` 的 GCC；Node/npm |

五份社区 PR 固定输入仍为 `#22 204f6a12`、`#20 f921abd5`、`#24 35160bc2`、`#18 0f801d1b`、`#23 bc892893`，前序按 22→20→24→18→23 融入 rc3。此次没有重新套补丁或回滚用户改动。

## 2. 本轮缺陷、实现和证据

### C6/C7：跳转凭据的 origin 信任与并发认证

- `canonicalURLOrigin` / `selfOrigins` 按 scheme、host、有效端口确认配置过的 origin；仅主机相同不构成凭据授权。
- `fetchRedirectTarget` 使用同一互斥锁保护的 `authSnapshot()`，不再直接读取并发更新的 token。
- 图片、流、HLS 资源调用该跳转取回函数。用于地址泄漏/relay 分类的主机比较与用于凭据授权的 origin 比较保持独立。
- `TestFinalReviewRedirectRejectsUnconfiguredPlaintext` 通过；`TestFinalReviewRedirectConcurrentAuth` 已实际以 `-race` 运行通过。

### C1：稀疏 DTO 分级确认与读取入口

- 新增 `parental_read.go`，区分“有可识别分级”“未返回分级字段”“非空但未知分级”。CustomRating 继续覆盖 OfficialRating。
- 缺失分级先按服务器批量补查（每批 ≤200 ID），必要时单项详情回退；一次请求共享 50 次补查预算与 memo。Season/Episode 沿同服父链最多 3 层继承，并去环。
- 未确认成功的项目不作为未分级放行；非空未知分级按 `blockUnratedItems` 决定。
- 接入用户列表（单库/根）、集合、Latest、SearchHints、Similar、ThemeMedia 嵌套结果、Seasons、Episodes，以及普通详情的缺失分级确认。
- `TestFinalReviewParentalReadRoutes` 及其子用例通过。Similar 对**返回的推荐项**逐项过滤；没有新增“源影片详情查不到就拒绝整份推荐”的假定。

### C2：合并详情先审查实际副本

- `handleMergedItemDetail` 在每个实例贡献元数据或 MediaSources 前执行 `checkInstanceParentalAccess`。
- 被拒副本不进入合并；无法确认的副本不进入合并且体现在 `EioIncompleteSources`。
- 全部明确拒绝返回 403；无安全结果且有未确认返回 502。展示 base 使用稳定的 `isBetterMetadata`，不再依赖网络返回顺序。
- `TestFinalReviewMergedDetailMustDropDeniedSource` 通过，允许的 PG 详情不会带出受禁的 R 副本。

### C5：图片提示不再绕过暂停聚合

- `imageSourceUsable` 对 DisplaySource 与 tag 来源统一复核在线状态、当前聚合开关；认证请求另复核服务器授权。
- 历史提示不可用时保留当前可用候选，不能据提示访问已暂停的服务器。匿名图片仍公开可达，但同样遵守聚合开关。
- 展示 DTO 的非 Primary `ImageTags` 及 `BackdropImageTags` 建立逐 tag 归属，以免后续展示胜者变化后只剩一个“最近展示源”。Primary 补图仍保留其准确实际来源，不被展示源覆盖。
- `TestFinalReviewPausedImageSourceNeverFetched` 的匿名/认证请求均不访问暂停源；原跨源海报实际字节、授权和缓存回归保持通过。

### C8：已公开旧 ID 的观看状态兼容

- `IDStore.AliasVirtualIDs` 只沿已验证的 `(originalID, serverID)` 共享关系连接旧公开 ID，不因不同服务器恰好使用相同原始字符串而合并。
- `localWatchState` 汇合当前用户的别名记录：收藏与已播放标志取并集；进度取 LastPlayed 最近且 PositionTicks>0 的记录，单纯收藏记录不覆盖有效进度。没有本地记录时仍清除共享上游账户的 UserData。
- 本地收藏/已播放/续播筛选使用同组汇合状态，避免 canonical DTO 在筛选阶段被误滤掉。
- 添加/移除收藏仍在上游成功后才进行本地双写；`SetFavoriteAliases` 在一个 SQLite 写事务中更新全部同组 ID，避免取消收藏后旧别名又把收藏补回来。
- 没有删除或批量迁移旧观看记录；别名由持久化的 primary/additional-instance 映射恢复。
- `TestFinalReviewKnownGroupKeepsWatchAliases`、`TestIdentityAliasesMergeProgressAndFavoriteRemoval`、`TestIdentityAliasesSurviveRestart`、`TestIdentityAliasFavoriteTransactionRollback` 通过；回滚测试通过触发器故意使第二个别名写入失败，确认没有部分清除。
- 本轮不声称重写了整个 IDStore 的 canonical 建组/持久化 API；验证范围是已有组关系下的公开 ID 兼容、状态合并和收藏事务。

### C3/C4：HLS 会话租约、资源容量与滚动窗口

- 会话键为用户、虚拟影片、实际服务器、实际原始影片的四元组；同会话同 URL 复用 opaque handle，不同用户不复用。
- `peek` 不续租；HTTP 资源请求通过用户/影片绑定、当前服务器授权/聚合开关及实际副本分级检查后才 `touch`。续租作用于整个会话，VOD 未读尾部不再按单个 handle 的初始时间过期。
- manifest 全部 URI 先解析、校验，再在同一锁内计算完整资源需求并原子注册；容量不足不提交本次新 handle 或窗口更新。
- 首层及子 manifest 的容量失败均返回 **503 + `Retry-After: 5`**；非法 URI 等重写错误保持 502；未知/过期 handle 为 410，跨用户为 403。
- 有界总容量 16384、会话空闲 TTL 30 分钟。识别带 MEDIA-SEQUENCE/PART 且无 ENDLIST 的滚动清单；滑出窗口资源保留 **2 分钟追赶宽限**，之后可清理。不同音视频清单保留各自窗口引用；VOD 资源保留到会话空闲过期。
- 受限用户资源走本地授权入口；非受限用户保持原 `RewriteM3U8ForItem`，不额外占用这份资源表。
- 四个原始 HLS 边界用例通过；新增验收覆盖 6×2400 资源用户隔离、第 7 份原子拒绝、160 分钟持续活动后 VOD 尾部仍可读、空闲用户清理、拒绝请求不续租、滚动追赶期、非法 URI 无占位泄漏、16 并发相同清单复用 handle。

### C9/C10：收集器截止时间与完整性标记

- 聚合收集循环自身监听 `bgCtx.Done()`，不依赖所有任务及时响应取消；超时时按尚未收到的任务数记录 TimedOut/Incomplete，保留截止前取得的结果。
- 结果返回前按任务的配置 index 稳定排序，防止网络快慢改变去重的首次公开 ID 选择。
- 成功的 `/Items?Ids=...` 批量响应显式设 `Complete: true`，不再因为结构体零值错误输出不完整标记。
- 原始两个用例和 `TestAggregationDeadlinePreservesCompletedResults` / `TestAggregationConfigOrderIndependentOfArrival` 通过。无法响应 context 的任意函数本身不能被 Go 强制杀死；这里验收的是**收集器及时返回**，不是声称能消灭任意任务 goroutine。

## 3. PlaybackInfo 的准确占槽说明

此前路由表“检查通过前不占槽”的说法不符合实际代码。当前 `handlePlaybackInfo`：

1. 完成目标/服务器身份校验后，先调用 `PlaybackLimiter.TryStart` 预留名额。
2. 对候选实际副本检查分级，并构建允许的 MediaSources。
3. 显式选源被拒/未确认时调用 `releaseSlot`；最终没有成功结果时也通过 `Stop` 释放预留。

这是**先预留、失败释放**，不是“分级检查后才 TryStart”。本轮修正文档，不把已有释放逻辑描述为不存在，也没有宣称改变该顺序。

## 4. 自动化验收（最终代码 `0df3b72`）

原始 12 项测试原样从临时评审文件迁入 `internal/backend/final_review_test.go`，今后运行仓库测试即可，不依赖 overlay。前序四个 review 测试文件共 22 个顶层用例保留；新增专题测试与旧套件一同运行。

| 本轮实际命令/检查 | 结果 |
| --- | --- |
| `go test -p 1 -parallel 2 ./... -count=1 -timeout 15m` | 通过；最终 cmd 0.590s、backend 58.909s |
| `go test ./internal/backend -run '^TestFinalReview' -v -count=1` | 12/12 顶层用例通过（含读取路由子用例） |
| `go test -race -p 1 -parallel 2 ./internal/backend -run 'TestFinalReview\|TestResource\|TestIdentityAliases\|TestIdentityAliasFavorite\|TestAggregation\|Test.*Filter' -count=1 -timeout 10m` | 通过；最终 104.823s，无 race 报告 |
| `go vet ./...` | 通过 |
| `go build -o <scratch>/emby-in-one-verification.exe ./cmd/emby-in-one` | 通过；二进制仅放 scratch |
| `gofmt -l cmd internal` | 空输出 |
| `npm test` | 68/68 通过；本轮未改前端代码 |
| `git diff --check` | 通过 |
| 一次只读定向复核 | 未发现具体在范围内的新增缺陷；复核者未运行命令，实际执行结果由主会话验证 |

本轮**未重新运行全仓库 `-race` 或 `npm run build:panel`**；旧版本报告中的相关历史结果不作为当前提交的验收证据。

日志位于本会话 scratch `C:\Users\Administrator\.pi-desktop\scratch\d29264d2-7cc7-465f-acf9-bae6091da5e0`：`final-go-test.log`、`final-go-race.log`、`final-go-vet.log`、`final-go-build.log`、`final-npm-test.log`。scratch 属于会话临时资料，不作为仓库持久附件。

## 5. 分步提交与推送

每个可独立验证的步骤先测试，再提交并推送 `origin/codex/community-prs-local-review`，无 force-push、无历史改写、未推送 GitHub：

| 提交 | 步骤 |
| --- | --- |
| `f6d989b` | C6/C7：origin 信任与认证快照 |
| `dac9d92` | C1：读取分级确认 |
| `247fda3` | C2：合并详情逐副本授权 |
| `469898a` | C5：图片来源实时聚合检查与 tag 归属 |
| `e5797d6` | C8：别名状态与收藏事务 |
| `2c88276` | C3/C4：会话租约、原子资源准入与追赶期 |
| `9fce0da` | C9/C10：聚合截止时间、顺序与 Complete |
| `cb36d7e` | 12 项原始边界测试正式入库 |
| `0df3b72` | 别名收藏筛选、重启/事务回滚验收、格式规范化 |

WireGuard 出站期间遇到 DNS 解析失败时使用有上限间隔的退避重试；例如 C8 推送第 5 次成功，C9/C10 第 2 次成功。规范已写入根目录 `AGENTS.md`。

## 6. 验收边界与交接

- 本轮 C1–C10 的 12 项可重复缺陷验收均已转绿，无已知阻塞这些验收的问题。
- 未运行真实 Android TV、montage、网盘或真实 HLS 播放器，也未使用真实 Emby/TMDB 做外部验收。
- HLS 长 VOD、滚动窗口和多用户容量为假时钟/受控 HTTP 的自动化验收，不是生产负载压测；尚未覆盖所有 HLS 变体及真实客户端长时追赶行为。
- 未构建/运行 Docker 或发布镜像；未向 GitHub 镜像推送。
- 完整路由入口说明见 `community-prs-route-coverage.md`；未逐项验证的原方案范围不能仅因当前测试转绿而宣布全部完成。
