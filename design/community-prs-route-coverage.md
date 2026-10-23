# 路由、分级与资源覆盖表

> 更新：2026-10-23；对应 `codex/community-prs-local-review` 的 R1–R9 修复，保留此前 C1–C10 成果。
> 本表区分实现位置和测试证据，不将“有过滤函数”直接等同于全字段、全客户端已验收。详细命令与边界见 `community-prs-execution-report.md`。

## 1. 统一语义

- `withContext` 读取本次 HTTP 请求的 ceiling 与 `blockUnratedItems` 快照；所有 goroutine/嵌套列表共用一份状态。下个请求重新读取当前策略。非空 CustomRating 覆盖 OfficialRating，显式未知不能被父级覆盖。
- 区分缺字段、显式未知、确认未分级、查询失败。缺失先按真实服务器批量补查（≤200 ID/批），必要时详情回退；父链按层合批且原始资料按实际 client/ID 在本请求内 memo，同键并发共享读取。50 次补查调用预算同时消耗共享的 100 次目录调用预算；Season/Episode 同服继承最多 3 层并去环。
- “无法确认”不当成“允许未分级”：无安全结果且存在无法确认时为 502；有安全部分时附不完整头和对象字段；全部明确拒绝为完整空列表。详情/播放/认证图片实际副本无法确认为 502、明确拒绝为 403。确认未分级则按 blockUnratedItems 判断。
- 列表先登记身份事实，再按实际副本过滤，最后从可见副本选展示资料；过滤不改变上游候选顺序。
- 所有来源提示都不是权限：仍须符合在线状态、当前聚合开关，以及认证用户的服务器授权。
- 本地 UserData 仅汇合当前用户的已验证同组别名，不读取其他用户状态；收藏取并集、进度取最近有效正进度。

## 2. 内容读取与播放入口

| 路由 | Handler / 实现位置 | 当前行为与边界 | 本轮或保留的证据 |
| --- | --- | --- | --- |
| GET `/Users/{userId}/Items`（根/ParentId） | `handleUserItems` / `handleRestrictedContainerItems` | 根列表以可见实例合并；无限制单库保留上游分页总数；受限单库扫描候选、过滤后本地分页，截断明确不完整；GenreIds 每服翻译 | 第三轮 Confirmed/CommonParent/AllowedSibling/UnrestrictedPagination/FilterOrder；`TestParentalContainerFiltersBeforePagination` |
| GET `/Items`（Ids/Search） | `handleItemsCollection` | Ids 每服翻译，结果逐项确认；成功批量任务显式 `Complete:true`；存在部分失败/超时才按报告标记不完整 | `TestFinalReviewCompleteBatchDoesNotClaimIncomplete`、聚合回归 |
| GET `/Items`（非 Ids/Search） | `handleFallbackProxy` | 不因本轮修改成为新聚合入口；受限内容请求遵守 fallback 白名单 | 既有 fallback 回归 |
| GET `/Users/{userId}/Items/Latest` | `handleUserItemsLatest` | 单库/多库在原始 DTO 上按真实 client 确认后重写 ID、叠加本地状态；数组响应用头传递不完整状态 | 完整 Go 套件 |
| GET `/Search/Hints` | `handleSearchHints` | 原始 Hints 逐实例确认，再选可见资料；返回项叠加本地状态 | `TestFinalReviewParentalReadRoutes/search-hints` |
| GET `/Shows/{seriesId}/Seasons`、`/Episodes` | `handleShowsSeasons/Episodes` | 先登记索引身份，再逐实例确认/继承，选择允许资料并记录实际图片来源；SeasonId 每服翻译；叠加本地状态后分页 | 原始 Seasons/Episodes 子用例；完整 Go 套件 |
| GET `/Users/{userId}/Items/{itemId}`、`/Items/{itemId}` | `handleUserItemByID` / `handleItemByID` | 单副本详情经 `parentalDetailAllowed` 确认；季/集缺失时继承父级；合并详情逐实际副本检查后才贡献字段和 MediaSources | 原始 episode-detail / merged-detail 用例 |
| GET `/Items/{itemId}/Similar` | `handleItemSimilar` | 对推荐结果逐项确认，不声称预先补查源影片详情；源 route 仍执行服务器授权 | 原始 Similar 子用例 |
| GET `/Items/{itemId}/ThemeMedia` | `handleItemThemeMedia` | 过滤嵌套 ThemeVideosResult、ThemeSongsResult、SoundtrackSongsResult 等项目 | 原始 ThemeMedia 子用例 |
| GET `/Users/{userId}/Items/Resume` | `handleLocalResume` / `fetchWatchMetadata` | 本人历史按批扫描，按真实服务器补资料并确认当前分级，过滤后补足 Limit；同号原始 ID 的资料按服务器隔离；失败与扫描截断显式反馈 | 第三轮 LocalWatch/resume；跨 100 条拒绝候选补足、补资料失败 502、同号不同服务器资料隔离；旧暂停源回归 |
| GET `/Shows/NextUp` | `handleLocalNextUp` / `fetchNextUpSeason` / `firstAllowedNextEpisode` | 普通用户先确定播放游标，再从后续候选确认当前策略，拒绝后继续找允许集；单系列和全系列均接入；目录分页共用预算，跨系列保持有界并发 | 第三轮 LocalWatch/next-up；`TestParentalNextUpRefillsAfterDeniedEpisode`；旧并发/游标回归 |
| Resume/NextUp 上游回退 | `handleUserItemsResume` / `handleShowsNextUp` / `writeMergedParentalResponse` | 本地 WatchStore 不可用时也确认分级；带 ParentId/SeriesId 的单系列请求按实际来源过滤；无条件的多库请求以可见副本合并；无限制管理员仍保留原首选源行为 | `TestParentalWatchRoutesWithoutLocalStoreStillFilter` 的四个路径；既有跨源回退与首选源回归 |
| GET/POST `/Items/{itemId}/PlaybackInfo` | `handlePlaybackInfo` | 逐实例分级；显式选定拒绝 403、未确认 502；**先 TryStart 预留，再检查，拒绝或全失败时释放**，不是“检查通过前不占槽” | 既有分级、占槽与选源回归 |
| GET `/Videos/{itemId}/{rest...}`、`/videos/...` | `handleVideoProxy` / `proxyStream` | 实际选定源分级；未确认 502；检查通过后切换实际播放源；受限用户强制代理；资源 handle 走专用入口 | 原始 stream-inherits、既有选源/跳转回归 |
| GET `/Audio/{itemId}/{rest...}`、`/audio/...` | `handleAudioProxy` | 沿用实际源解析与播放授权路径 | 既有 Audio/流测试 |
| GET `/Items/{itemId}/Images/{type}[/{index}]` | `handleItemImage` | 主源按当前在线/聚合解析；DisplaySource/tag 提示另经 `imageSourceUsable` 复核；认证请求再查实际副本分级；不可用提示不再访问暂停源 | 原始 paused-image 匿名/认证测试；跨源海报真实字节与缓存回归 |

## 3. HLS 专用资源入口

| 场景 | 当前行为 | 证据 |
| --- | --- | --- |
| 受限用户首层及子 manifest | 全部可取回 URI（属性与分片行）转为 `/Videos/<vid>/eio-resource/<opaque>`；先解析全部 URI 再原子注册 | `TestRewriteM3U8ForItemWithResources`、原始 atomic/reuse 用例 |
| 资源请求 | `peek` → 用户/影片绑定 → 服务器授权/聚合 → 在线/实际分级 → `touch` → 取字节；拒绝不得续租 | `TestResourceDeniedRequestDoesNotRenewSession` |
| 会话租约 | 键为用户/影片/实际服务器/实际原始影片；30 分钟空闲过期；授权活动续整个会话 | 原始 unread-tail；160 分钟长 VOD 测试 |
| URL 复用及隔离 | 同会话相同 URL 复用；不同用户/源不共用 handle | 原始 repeated-manifest；16 并发复用；多用户隔离 |
| 容量 | 总容量 16384；完整 manifest 原子准入；清理过期后仍不足返回 **503 + Retry-After:5**；不产生半份 manifest 或本次部分占位 | 原始 full-resource-map / atomic；6×2400 + 第 7 份拒绝 |
| 滚动窗口 | 首层用跳转前授权请求身份（排除代理认证参数），子清单用绑定资源 handle；默认完整 URL 保留 query，音轨/画质互不退休；移出资源保留 2 分钟，VOD 保留到会话空闲过期 | 第三轮 HLSQueryDistinctWindows；`TestHLSWindowSignedRedirectsRetireOnlyTheirOwnTrack`；旧追赶期回归 |
| 窗口容量 | 全部会话的窗口记录总数也限制为 16384；资源和窗口在更新前共同检查，拒绝不产生部分状态；会话过期释放窗口额度 | `TestHLSWindowCapacityIsAtomicAndReclaimed` |
| 未知/过期/跨用户 | 410 / 403；不因失败请求续租 | 生命周期及授权测试 |
| 重写或上游错误 | 非法 URI 等重写问题 502；容量压力单独按 503；不是所有重写错误都返回 503 | 非法 URI 无占位回归与容量入口测试 |
| 非受限用户 | 保留原 `RewriteM3U8ForItem` 输出，不额外注册资源表 | 既有 M3U8/流测试 |

跳转取回凭据只授权给配置中的可信 origin（scheme/host/有效端口），且认证状态通过锁保护的快照读取。原始 plaintext-origin 和 concurrent-auth（race）测试通过。

## 4. 图片身份与缓存边界

- 携带无效/撤销 token 的请求返回 401，不降级成匿名。
- 认证成功为 `private, no-store`；匿名成功为 `public, max-age=86400`；错误（含匿名 404）为 `no-store`。
- 匿名仍受服务器暂停聚合规则约束；认证请求另受服务器授权和实际副本分级约束。
- DisplaySource 作为无 tag 时的展示来源提示；已记录的非 Primary ImageTags 与 BackdropImageTags 按 tag 找实际归属；Primary 补图另记录准确来源。
- 现有受控测试通过不代表所有客户端的旧 DTO、图类型/index 组合已做真实设备验收。

## 5. 导航、控制与 fallback

| 路由/类别 | 处理边界 |
| --- | --- |
| `/Library/*`、Genres/Studios/Persons/Artists 等 | 库/分类导航，聚合与 ParentId 翻译沿用现有路径；不把导航容器等同于完成影视分级确认的 DTO |
| `/Items/Prefixes` | 受限用户返回 `[]`，避免不透明内容索引泄露 |
| 用户状态/收藏 | 逐本人权限校验；本地筛选与 DTO 叠加兼容已验证别名；取消收藏对全部别名在同一 SQLite 事务中执行 |
| AuthenticateByName、System/Ping、用户配置/策略、播放会话进度等 | 保留显式控制端点；普通用户不能经用户配置接口修改本地家长限制 |
| 受限 fallback | `parental_routes.go` 白名单以外的未知内容请求返回 403 `ContentRouteNotSupportedWithParentalControl`，不透明二进制不因推断放行 |
| `admin/api/*` | `requireAdmin`；分级 10 档和 blockUnratedItems 管理端往返沿用前序实现 |

## 6. 结论边界

第三轮 10 项原始边界测试、此前 12 项与 22 项均保留；新增请求状态/观看入口/HLS 身份专题测试 11 项。最终实际检查与日志以执行报告为准。这里记录受控自动化与入口接线，原方案跨请求元数据缓存、完整 canonical 建组 API、真实客户端/生产压测和 Docker 发布未据此宣布完成。
