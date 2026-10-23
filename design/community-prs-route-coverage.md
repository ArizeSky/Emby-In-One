# 路由与分级覆盖表（当前整合树状态）

> 对应 `design/community-prs-code-execution-plan.md` §8.7。本表记录**当前整合树**各内容路由的归属与分级检查状态。
> 状态图例：`完整` = 逐实例/逐项按 `parentalPolicyFor`（ceiling + blockUnrated）检查；`列表` = 对返回列表整体过滤（非逐实例归属）；`无` = 尚未接入分级；`部分` = 仅身份/缓存边界。

## 内容读取路由

| Method + 路径 | Handler | 类别 | 归属依据 | 分级位置（现状） | 备注 |
| --- | --- | --- | --- | --- | --- |
| GET /Users/{userId}/Items | handleUserItems | 列表 | ParentId/根 GenreIds 每服翻译 | **列表**（ceiling+blockUnrated） | 单库/根均过滤 |
| GET /Items | handleItemsCollection | 列表(Ids/Search) | Ids 批量翻译 | **列表** | 不完整响应带 `EioIncompleteResults` |
| GET /Users/{userId}/Items/Resume | handleUserItemsResume | 列表 | enrichWatchItems | **列表** | 不完整响应带标记 |
| GET /Users/{userId}/Items/Latest | handleUserItemsLatest | 列表 | 每服查询 | **列表** | 不完整响应带 `X-EIO-Results-Incomplete` |
| GET /Shows/NextUp | handleShowsNextUp | 列表 | SeriesId 每服 | **列表** | 不完整响应带标记 |
| GET /Shows/{seriesId}/Seasons、/Episodes | handleShowsSeasons/Episodes | 列表 | resolveRouteID/season 翻译 | **列表** | 展示源记录 `SetDisplaySource` |
| GET /Search/Hints | handleSearchHints | 列表(Hints) | 每服 item | **列表** | Hints 常缺分级 |
| GET /Users/{userId}/Items/{itemId} | handleUserItemByID | 详情 | resolveRouteID | **完整**（单副本）；合并走 handleMergedItemDetail | — |
| GET /Items/{itemId} | handleItemByID | 详情 | resolveRouteID | **完整** | — |
| GET /Items/{itemId}/Similar | handleItemSimilar | 列表 | resolveRouteID + 结果 | **列表** | 源与推荐分别检查 |
| GET /Items/{itemId}/ThemeMedia | handleItemThemeMedia | 嵌套资源 | resolveRouteID | **列表** | — |
| GET/POST /Items/{itemId}/PlaybackInfo | handlePlaybackInfo | 播放信息 | selectedPlaybackSource | **完整**：逐实例分级；显式选定被拒 403、未确认 502、全拒 403 | 检查通过前不占槽 |
| GET /Videos/{itemId}/{rest...}、/videos/... | handleVideoProxy | 二进制流 | resolveStreamTarget（含跨源重写/归属400） | **完整**：实际选定源分级、查询失败502、分级后再切槽 | 受限用户强制代理；`eio-resource/` 走本地资源代理 |
| GET /Audio/{itemId}/{rest...}、/audio/... | handleAudioProxy | 二进制流 | resolveStreamTarget | **完整**（同上） | 同上 |
| GET /Items/{itemId}/Images/{type}[/{index}] | handleItemImage | 图片 | resolveRouteID + display/tag 来源 | **完整**：401/private/anonymous-public/所有错误 no-store；认证请求对实际副本实时分级；非 Primary 走展示源、tag 命中覆盖 | — |
| GET /Library/* | handleLibrary* | 库导航 | 聚合 | 无（容器放行，子项逐项） | — |
| GET /Genres、/Studios… | handleLibraryTaxonomy | 分类 | ParentId 每服翻译 | 无（容器） | — |
| GET /Items/Prefixes | handleItemsPrefixes | 索引 | 每服 ParentId 翻译 | **受限用户（ceiling>0 或 blockUnrated）返回 `[]`** | — |
| GET / （fallback） | handleFallbackProxy | 通用代理 | 无 | **§8.7 已实现**：受限用户仅放行显式控制端点，其余 403 `ContentRouteNotSupportedWithParentalControl` | 分类见 `parental_routes.go` |

## 控制/会话/用户路由（受限用户 fallback 放行清单）

| Method + 路径 | Handler / 分类 | 说明 |
| --- | --- | --- |
| POST /Users/AuthenticateByName | handleAuthenticateByName | 登录（#20 已硬化） |
| GET /Users/Public、/Users/{userId}、/Views、/GroupingOptions | handleUser* | 用户配置读取 |
| POST /Users/{userId}/Configuration、/Policy | handleUserConfiguration / handleUserPolicy | 普通用户不得自我解限（`parental_policy_test` 覆盖数据层；本地两字段不可经此改） |
| GET/POST /System/Ping、/System/Info、/System/Endpoint | handleSystem* | 兼容控制 |
| POST /Sessions/Playing、/Playing/Progress、/Playing/Stopped、/Capabilities/Full | fallback（放行） | 控制端点；GET /Sessions 视为内容（受限拒绝） |
| POST /Users/{id}/PlayingItems/*、/UserData、/FavoriteItems/*、DisplayPreferences | fallback（放行） | 控制端点，按本人权限 |
| admin/api/* | handleAdmin* | 管理端，`requireAdmin`；用户分级 10 档 + blockUnratedItems 往返 |

## 当前结论

- **已接入分级**：列表（用户/集合/Latest/Resume/NextUp/Seasons/Episodes/SearchHints/Similar/ThemeMedia）、普通详情、合并详情、PlaybackInfo 逐实例、直接流（实际选定源 + 强制代理）、图片（身份+错误缓存+展示源/tag 来源+实际副本分级）、Prefixes（受限 `[]`）、fallback 分类（§8.7）。
- **父级继承（§8.3）**：Season/Episode 自身无分级时沿同服父链（≤3 层、去环、父项查询失败即失败）继承。
- **HLS 外部资源代理（§8.6.1）**：受限用户 manifest 全部资源（含外部 URL）重写到 `/Videos/<vid>/eio-resource/<opaque>`，逐请求复核授权与分级，未知/过期 410、跨用户 403、容量满 503。
- 本表随实现推进更新。
