# 路由与分级覆盖表（当前整合树状态）

> 对应 `design/community-prs-code-execution-plan.md` §8.7。本表记录**当前整合树**各内容路由的归属与分级检查状态。
> 状态图例：`完整` = 逐实例/逐项按 `parentalPolicyFor`（ceiling + blockUnrated）检查；`列表` = 对返回列表整体过滤（非逐实例归属）；`无` = 尚未接入分级；`部分` = 仅身份/缓存边界。

## 内容读取路由

| Method + 路径 | Handler | 类别 | 归属依据 | 分级位置（现状） | 备注 / 待办 |
| --- | --- | --- | --- | --- | --- |
| GET /Users/{userId}/Items | handleUserItems | 列表 | ParentId/根 GenreIds 每服翻译 | **列表**（ceiling+blockUnrated） | 单库/根均过滤；未逐实例/父级继承 |
| GET /Items | handleItemsCollection | 列表(Ids/Search) | Ids 批量翻译 | **列表** | — |
| GET /Users/{userId}/Items/Resume | handleUserItemsResume | 列表 | enrichWatchItems | **列表** | — |
| GET /Users/{userId}/Items/Latest | handleUserItemsLatest | 列表 | 每服查询 | **列表** | “先过滤再取 Limit” 待细化 |
| GET /Shows/NextUp | handleShowsNextUp | 列表 | SeriesId 每服 | 无 | 待补 |
| GET /Shows/{seriesId}/Seasons、/Episodes | handleShowsSeasons/Episodes | 列表 | resolveRouteID/season 翻译 | 无 | 待补 |
| GET /Search/Hints | handleSearchHints | 列表(Hints) | 每服 item | 无 | Hints 常缺分级 |
| GET /Users/{userId}/Items/{itemId} | handleUserItemByID | 详情 | resolveRouteID | **完整**（单副本）；合并走 handleMergedItemDetail | — |
| GET /Items/{itemId} | handleItemByID | 详情 | resolveRouteID | **完整** | — |
| GET /Items/{itemId}/Similar | handleItemSimilar | 列表 | resolveRouteID + 结果 | 无 | 待补 |
| GET /Items/{itemId}/ThemeMedia | handleItemThemeMedia | 嵌套资源 | resolveRouteID | 无 | 待补 |
| GET/POST /Items/{itemId}/PlaybackInfo | handlePlaybackInfo | 播放信息 | selectedPlaybackSource | 无（**待 §8.6 逐实例**） | 未在选定实例上分级 |
| GET /Videos/{itemId}/{rest...}、/videos/... | handleVideoProxy | 二进制流 | resolveStreamTarget（含跨源重写/归属400） | **完整**：实际选定源分级、查询失败502、分级后再切槽 | 受限用户强制代理、HLS 本地资源代理待 §8.6.1 |
| GET /Audio/{itemId}/{rest...}、/audio/... | handleAudioProxy | 二进制流 | resolveStreamTarget | **完整**（同上） | 同上 |
| GET /Items/{itemId}/Images/{type}[/{index}] | handleItemImage | 图片 | resolveRouteID + tag 来源 | **部分→改进**：401/private/anonymous-public/所有错误 no-store；tag 路由到真实来源 | 匿名候选范围、非 Primary 类型来源路由待补 |
| GET /Library/* | handleLibrary* | 库导航 | 聚合 | 无 | — |
| GET /Genres、/Studios… | handleLibraryTaxonomy | 分类 | ParentId 每服翻译 | 无 | — |
| GET /Items/Prefixes | handleItemsPrefixes | 索引 | 每服 ParentId 翻译 | **受限用户（ceiling>0 或 blockUnrated）返回 `[]`** | — |
| GET / （fallback） | handleFallbackProxy | 通用代理 | 无 | 无 | §8.7 分类（**未实现**）：受限用户未分类内容读取应 403 |

## 控制/会话/用户路由

| Method + 路径 | Handler | 说明 |
| --- | --- | --- |
| POST /Users/AuthenticateByName | handleAuthenticateByName | 登录（#20 已硬化） |
| GET /Users/Public、/Users/{userId}、/Views、/GroupingOptions | handleUser* | 用户配置读取 |
| POST /Users/{userId}/Configuration、/Policy | handleUserConfiguration / handleUserPolicy | 普通用户不得自我解限（策略已落库；测试待补） |
| GET/POST /System/Ping、/System/Info、/System/Endpoint | handleSystem* | 兼容控制 |
| admin/api/* | handleAdmin* | 管理端，`requireAdmin`；用户分级 10 档 + blockUnratedItems 往返 |

> Sessions/Playing、Progress、Stopped、Capabilities/Full、Users PlayingItems start/stop、UserData/FavoriteItems 等仍由 fallback 处理或无专门 handler，完整分级需按 §8.7 逐条核对（**未完成**）。

## 当前结论

- **已接入分级**：列表（用户/集合/Latest/Resume）、普通详情、合并详情、直接流（实际选定源）、图片（身份+错误缓存+tag 来源）、Prefixes（受限 `[]`）。
- **未接入/待补**：PlaybackInfo 逐实例、Similar/ThemeMedia、NextUp/Seasons/Episodes、SearchHints、库导航/分类统计、fallback 分类（§8.7）、HLS 外部资源代理（§8.6.1）、父级继承（§8.3）、非 Primary 图片类型来源路由（§6.4）。
- 本表随实现推进更新。
