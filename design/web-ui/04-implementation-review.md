# /web UI 落地评审

评审日期：2026-10-08。依据当前工作区代码、02 v1.5 规范和 03 v1.2 预览。

结论：桌面视觉方案可落地，现有 Vue 3 全局构建、Tailwind、lucide 足够承接，无须引入新框架。完整功能方案需要修订若干数据、会话和播放器状态假设；不宜按“全部零后端改动、300–350 行表现层 JS、5.5 人日”直接承诺交付。

本次只做评审，未改业务实现。浏览器查看了登录、首页和播放器预览；现有预览使用渐变占位图，真实海报对比度和实播交互仍待验收。

## 可直接实施的部分

| 范围 | 判断 | 实施条件 |
|---|---|---|
| 色板、间距、按钮、侧栏、卡片、登录分栏 | 可实施 | CSS 限定在 Web 客户端，避免影响共享样式的管理端 |
| 版本卡、季分段、音轨/字幕菜单 | 可实施 | 复用现有 MediaSources、seasonEpisodes、轨道索引；保留失败和禁用状态 |
| Hero、搜索分组、最近搜索、无限滚动 | 可实施 | 字段缺失降级；新增定时器、观察器和请求须随页面退出清理 |
| 自动隐藏、快捷键、Toast | 可实施 | 保留播放代次校验；焦点位于菜单/控件时避免快捷键冲突 |
| 自动连播、相似推荐 | 可实施 | 复用 nextEpisode 和 /Similar；补计时器清理、异步结果归属校验 |
| 已看列表 | 可实施 | 复用 IsPlayed，保留后端本地顺序；“完整历史”另见下文 |
| 外部播放直链 | 有条件实施 | 改掉弹窗获取 PlaybackInfo 的副作用，验证目标平台协议 |

## 实施前须修订的问题

### 1. 外部播放弹窗会占用并发名额

02 §16.5 称弹窗可独立请求 PlaybackInfo 而“不占播放会话”。实际 `media_playback.go:39–51` 在返回播放信息前调用 `PlaybackLimiter.TryStart`；限制启用时，打开弹窗即可能占位或返回 429。没有心跳的占位约 3 分钟后才失效（`playback_limiter.go:8`），且限制器按用户和服务器复用名额，简单发送 Stopped 也可能干扰正在进行的网页播放。

建议：从已选 MediaSource 构造原文件代理 URL，复用现有 `streamPath` 规则和 `apiRaw()`，不要为了展示链接调用会占位的接口。若目标源确实需要额外解析，增加明确不占位的后端解析路径。复制链接与实际外部播放的并发计数/释放边界必须分别说明。

### 2. NextUp 不是一次轻量上游请求

02 §15.2 的“一次请求、无扩散”只描述前端。普通用户走 `media_nextup.go:55` 的本地 NextUp：读取所有追剧记录，在 `:109` 循环逐剧调用 `fetchNextEpisode`，跨季还可能再请求一次。Limit=12 限的是成功结果数，许多剧无下一集时，尝试次数可能超过 12。

建议：首页 NextUp 独立异步加载，不阻塞 Hero/Latest；限制请求时长和扫描量，按实测决定是否增加短期缓存。零后端改动可作为首版选择，但不能附带“上游单次轻量请求”的保证。

首页“继续观看”也需新增 Resume 查询：现有 `app.js:207` 的 loadHome 只有 Views/Latest，列表页的 loadCollection 并不自动给首页提供数据。D11 的豁免须包含这一项，避免为首页直接调用会全量分页的 loadCollection。

### 3. 手动标记未播放过的剧集，不能完整驱动 NextUp

`watch_store.go:141` 的 MarkPlayed 新建记录只存用户、条目、played 和时间，不补 item_type、series_virtual_id、series_original_id、季集号等元数据。NextUp 查询要求 Episode 和系列信息（`:305`、`:351`），首页循环还跳过缺失 SeriesOriginalID 的记录。

因此，“从未播放过的整季 → 整季标记已看 → 接下来观看跳到下一季”不能仅靠当前 UserData 接口保证。

建议：后端标记成功时补齐可信条目元数据，并验证单集、整季和跨季行为。若首版不补，须明确手动标记的作用范围。

另外，02 §16.3 的双写顺序已过时：当前 `session_userdata.go:561–575` 是上游接受后才写本地。前端失败回滚可以保留，但不能宣称上游失败仍保存本地标记。

### 4. 历史排序应直接保留本地后端顺序

02 §16.2 建议前端按 UserData.LastPlayedDate 排序。当前 `session_userdata.go:693–731` 只覆盖 PlaybackPositionTicks、Played、IsFavorite，没有将 LastPlayedDate 替换为个人记录时间；有本地记录时可能仍带上游共享账户时间。

反而 `user_filter.go:302` 已按 WatchStore.LastPlayed 默认降序排序，再分页。首版不指定其他主排序，保留响应顺序即可，无须前端重排或新增 DatePlayed 排序列。

“完整观看历史”需缩小表述：IsPlayed 是已看列表，不含所有未完成观看；本地筛选的上游候选扫描还有 5000 条上限（`user_filter.go:30`），前端加载所有响应页不能突破这个上限。若必须覆盖大库全部历史，需从本地记录取 ID 并批量补媒体信息等后端优化。

### 5. 完整 buffering 状态缺少播放器信号

02 §14.2 要求 buffering 状态及 20 秒后错误。目前 `player-libmedia.js` 对外状态包含 playing/time/duration/loading/message；native 路径没有接出 waiting/stalled，libmedia 路径也没有向应用暴露缓冲状态。现有 20 秒超时针对启动加载，不能直接等同于播放过程中卡顿超时。

建议：首版保留 loading/playing/paused/ended/error，缓冲区取不到就隐藏；完整 buffering 需允许对播放器适配层做小幅信号补充。不能根据时间没变化推断卡顿，暂停和 seek 会造成误判。

### 6. 浮层层级需要明确作用域和全屏归属

02 §2.5 设置 player-page=900、toast=400、modal=500。若 Toast/外部播放弹窗作为根节点同级浮层渲染，会被播放器页面遮挡；若位于播放器内部，这些值可作为局部层级，但需明确层叠上下文。

`app.js:697` 对 playerContainer 请求全屏，放在其外的浮层即使 z-index 更高也无法进入全屏显示树。

建议：将播放器相关弹窗和 Toast 挂在实际全屏容器内，分别定义页面与播放器内部层级，验收普通播放和全屏播放两条路径。

### 7. 预览 CSS 不能原样作为生产验收依据

- 03 `.np-badge` 和 `.np-play` 使用 backdrop-filter，与 D4“列表卡片一律不用”的性能规则冲突；首版改成普通半透明底。
- “全部”、库计数、版本码率、搜索简介等必要信息仍用 t3，与 02 规定的必要信息使用 t2 冲突。
- 导航和海报以 div 为主，卡片播放钮只有 hover 显示；生产版须补链接/按钮语义、focus-visible 展示和键盘操作。
- stagger 使用 `(i % 8)`，与“前 8 项后归零”不一致，应改为 `i < 8` 的有界延迟。
- 03 尚未预览 v1.3–v1.5 新增的 NextUp、历史 tab、连播卡和外部播放弹窗；移动端明确延后，不能作为全端交付方案。

建议在 P1 用真实亮/暗/高饱和海报先验证 scrim、文字和卡片密度，再扩展所有页面。

## 实施边界与排期

“保持 API 调用方式、hash 路由体系、AbortController 与播放代次机制”是合理约束；“不修改路由、请求结构或播放生命周期”无法同时容纳 history 路由、首页 Resume/NextUp、Similar 和连播计时器。

当前 app.js 789 行；基础交互预计 300–350 行，F1–F5 又预计约 285 行，不能视作同一份总量。按 AGENT.md 的约 1000 行文件上限，应将 UI、历史/标记、外部链接等职责拆出独立脚本，保留 app.js 为集成入口。无需新增构建框架。

建议顺序：

1. 修订上述接口/会话假设，补晚期功能状态稿和真实海报验证。
2. 落地桌面令牌、组件及页面，含字段缺失、空态、失败重试。
3. 落地播放器控制层、连播、Toast、全屏浮层。
4. 接入已看标记、历史、NextUp 和外部播放，针对数据及并发边界验收。

估算：桌面视觉和基础交互约 3–5 人日；包括上述功能修订、少量后端补齐及播放回归的完整桌面版约 7–10 人日。该估算基于代码范围，并非实测工时；移动端和复杂真实片源兼容性验收另计。原 5.5 人日更接近顺利情况下的首轮实现。

## 验证记录

- 浏览器：打开自包含预览，检查登录、首页、播放器及页签切换；未连接真实上游播放。
- `node --test tests/web-data.test.cjs tests/web-player.test.cjs`：11 项通过。
- Go：本地已看筛选、分页/排序、双写失败/成功、NextUp 存储和并发限制共 8 个定向测试通过（`GOMAXPROCS=2 go test -p 1 -parallel 2 ./internal/backend -run 'Test(UserItemsPlayedFilterUsesLocalState|UserItemsFilterPaginationAndTotalCount|UserItemsFilterSortsLocally|UserDataDualWriteHappensAfterForward|UserDataDualWriteStillHappensOnSuccess|PlaybackLimiterExpiry|PlaybackLimiterRejectsBeyondLimit|WatchStoreGetNextUpForSeries)$' -count=1`）。
- 测试验证现有能力，不代表设计中尚未实现的功能已通过验收。
