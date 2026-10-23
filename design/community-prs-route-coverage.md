# 路由、分级与资源覆盖表

> 更新：2026-10-23；对应 `codex/community-prs-local-review` 本轮 C1–C10 修复后的实现。
> 本表区分实现位置和测试证据，不将“有过滤函数”直接等同于全字段、全客户端已验收。详细命令与边界见 `community-prs-execution-report.md`。

## 1. 统一语义

- `parentalPolicyFor` 同时读取 ceiling 与 `blockUnratedItems`；非空 CustomRating 覆盖 OfficialRating。
- 本轮读取确认路径：缺失分级先按服务器批量补查（≤200 ID/批），必要时详情回退；请求级 50 次补查预算与 memo；Season/Episode 同服继承最多 3 层并去环。
- “无法确认”不当成“允许未分级”：列表丢弃无法安全确认的项；详情/播放/认证图片的实际副本无法确认时失败关闭（502），明确拒绝时 403。
- 所有来源提示都不是权限：仍须符合在线状态、当前聚合开关，以及认证用户的服务器授权。
- 本地 UserData 仅汇合当前用户的已验证同组别名，不读取其他用户状态；收藏取并集、进度取最近有效正进度。

## 2. 内容读取与播放入口

| 路由 | Handler / 实现位置 | 当前行为与边界 | 本轮或保留的证据 |
| --- | --- | --- | --- |
| GET `/Users/{userId}/Items`（根/ParentId） | `handleUserItems` | 单库/根均走逐项分级确认；ParentId/GenreIds 每服翻译；叠加本地别名状态 | `TestFinalReviewParentalReadRoutes` 的稀疏根列表；完整 Go 套件 |
| GET `/Items`（Ids/Search） | `handleItemsCollection` | Ids 每服翻译，结果逐项确认；成功批量任务显式 `Complete:true`；存在部分失败/超时才按报告标记不完整 | `TestFinalReviewCompleteBatchDoesNotClaimIncomplete`、聚合回归 |
| GET `/Items`（非 Ids/Search） | `handleFallbackProxy` | 不因本轮修改成为新聚合入口；受限内容请求遵守 fallback 白名单 | 既有 fallback 回归 |
| GET `/Users/{userId}/Items/Latest` | `handleUserItemsLatest` | 单库/合并结果接入读取确认与本地状态叠加 | 完整 Go 套件 |
| GET `/Search/Hints` | `handleSearchHints` | Hints 缺失分级不能直接放行，进入确认；返回项再叠加本地状态 | `TestFinalReviewParentalReadRoutes/search-hints` |
| GET `/Shows/{seriesId}/Seasons`、`/Episodes` | `handleShowsSeasons/Episodes` | 返回季/集逐项确认并继承；SeasonId 每服翻译；普通用户叠加本地状态 | 原始 Seasons/Episodes 子用例 |
| GET `/Users/{userId}/Items/{itemId}`、`/Items/{itemId}` | `handleUserItemByID` / `handleItemByID` | 单副本详情经 `parentalDetailAllowed` 确认；季/集缺失时继承父级；合并详情逐实际副本检查后才贡献字段和 MediaSources | 原始 episode-detail / merged-detail 用例 |
| GET `/Items/{itemId}/Similar` | `handleItemSimilar` | 对推荐结果逐项确认，不声称预先补查源影片详情；源 route 仍执行服务器授权 | 原始 Similar 子用例 |
| GET `/Items/{itemId}/ThemeMedia` | `handleItemThemeMedia` | 过滤嵌套 ThemeVideosResult、ThemeSongsResult、SoundtrackSongsResult 等项目 | 原始 ThemeMedia 子用例 |
| GET `/Users/{userId}/Items/Resume` | `handleUserItemsResume` / 本地 WatchStore 路径 | 保留现有 Resume 扫描、取源、补元数据路径；普通用户来自本地状态；本轮不是将该入口整体重写为 `parentalFilterItems` | 既有 Resume/暂停源/分页回归，包含在完整套件；12 项边界测试不单独覆盖所有 Resume 情形 |
| GET `/Shows/NextUp` | `handleShowsNextUp` / 本地观看路径 | 保留已有单系列/跨服/本地观看处理；不把本轮读取 helper 的入口清单扩大到未接线位置 | 既有 NextUp 回归，包含在完整套件 |
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
| 滚动窗口 | MEDIA-SEQUENCE/PART 且非 ENDLIST 的清单使用各自窗口引用；移出资源保留 2 分钟追赶宽限；VOD 资源保留到会话空闲过期 | `TestResourceRollingWindowCatchupAndAtomicValidation` |
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

本轮 12 项边界回归和前序 22 项审查测试均保留并通过；完整 Go 套件、定向 race、vet、构建及 68 项前端测试通过。这里记录的是受控自动化与静态入口核对，不是全原方案或所有真实播放器行为的完成证书。未执行真实客户端、生产压测、Docker 发布；详细证据和提交列表见执行报告。
