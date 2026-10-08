# V1.4.4-rc3：/web 前端 UI 改进执行文档

日期：2026-10-08。状态：待实施；本文是执行规格，不代表功能已完成或版本已发布。

## 1. 目标、范围与文档优先级

目标：将 `public/web/` 的现有客户端改造成设计稿中的桌面界面，提高内容浏览、版本选择、播放控制和异常反馈的清晰度，保留现有代理、鉴权、观看进度及播放能力。

本版本固定范围：

- 统一色板、字体层级、间距、按钮、卡片、加载、空态、焦点态。
- 登录分栏、三项桌面侧栏、首页 Hero/续看/媒体库/最新内容、媒体库筛选和列表视图、详情版本卡及季分段、搜索分组、继续观看/收藏列表。
- 播放器控制条、自动隐藏、快捷键、菜单、进度预览、音量、全屏、Toast 和可重试错误页。
- 必要的表现层请求治理：取消、过期结果校验、分页防重复、单季重试；首页新增一条有界 Resume 查询。
- V1.4.4-rc3 版本、缓存参数、更新日志与发布前验证。

以下不进入 rc3：

| 项目 | 本版处理 |
|---|---|
| 外部播放直链、复制链接、VLC/Infuse 唤起、外部播放提示 chip | 用户明确排除；不添加入口、弹窗、协议或获取直链的请求 |
| F1 自动连播、F2 历史页、F3 手动已看/整季标记、F4 相似推荐 | 属于功能扩展，延后；不新增 history 路由、UserData 写入或 Similar 请求 |
| 首页 NextUp 行 | 延后；普通用户端点内部逐剧查询，不属于本次视觉改造的必要条件 |
| 播放过程 buffering 状态和卡顿超时 | 播放器没有完整信号，本版不推测；保留原有启动超时/兼容流回退 |
| 移动端新导航和播放器抽屉、进度缩略图、blur-up | 延后；现有窄屏登录、导航、基础播放不能退化 |
| 后端端点、存储、身份策略、并发限制、解码与字幕内核 | 不修改业务行为 |

执行优先级：用户后续指令 → 本文 → `02-design-notes.md` 中不冲突的规范 → `03-page-previews.html` 的布局参考。`01-visual-proposal.html` 仅为历史色板记录，`04-implementation-review.md` 提供问题依据。不能重新启用本文排除的功能来追求旧稿完整度。

版本格式：文档/发行 tag 为 `V1.4.4-rc3`；构建注入和资产文件名沿用仓库惯例 `v1.4.4-rc3`；npm/CLI 内部数值为 `1.4.4-rc3`。

## 2. 代码基线和改动边界

下列行号均依据撰写时工作区，实施后会变化；定位以文件名、函数名和模板注释为准。动手前重新读取 `AGENT.md` 和对应代码，保留用户已有改动。当前 `app.js` 789 行，单文件尽量不超过 1000 行，单函数尽量不超过 80 行。

### 2.1 当前集成锚点

| 文件及锚点 | 当前职责 | rc3 动作 |
|---|---|---|
| `public/web/web.html:7` 样式引入 | Inter 与共享 Tailwind | 加 Web 专用 CSS，放在共享 CSS 后 |
| `public/web/web.html:58` LOGIN | 表单 | 改分栏、行内错误和密码显隐 |
| `public/web/web.html:84` MAIN APP | 桌面侧栏与窄屏菜单 | 重绘、收敛桌面导航，保留已有窄屏入口 |
| `public/web/web.html:134` HOME | 媒体库、Latest | 增加 Hero 和有界续看行 |
| `public/web/web.html:161` 列表页区域 | 继续观看、收藏 | 共用新卡片与状态组件 |
| `public/web/web.html:176` LIBRARY | 筛选、排序、加载更多 | 重绘、视图切换、有界自动分页 |
| `public/web/web.html:209` ITEM DETAIL | 背景、版本、季集、轨道 | 重排布局、分季显示、单季重试 |
| `public/web/web.html:315` SEARCH | 输入、结果 | 分组、最近搜索、加载/错误区分 |
| `public/web/web.html:335` PLAYER | full-screen 容器、Vue 控件 | 控制层重绘；保留 `ref="playerContainer"` |
| `public/web/web.html:380` scripts | 全局脚本装载 | 注册 UI 模块后再挂载应用 |
| `public/web/app.js:15` `api()` | JSON 请求和鉴权 | 仅给错误增加数值 `status`；保留所有请求/返回约定 |
| `public/web/app.js:39` `apiRaw()` | 图片、媒体 URL 的 api_key | 保留唯一事实源，不新增直链功能 |
| `public/web/app.js:80` `syncFromHash()` | hash 路由、离页取消 | 增加 UI 页面控制器的取消/进入挂接 |
| `public/web/app.js:170` `loadCollection()` | 续看、收藏完整列表 | 保留 EIOData.all，补过期错误防护 |
| `public/web/app.js:185` `loadHome()` | Views、至多三个 Latest 区 | 委托首页控制器，保持公共 refs |
| `public/web/app.js:225` `loadLibrary()` | 库信息、流派、条目 | 保留接口路径，分清初次与分页状态 |
| `public/web/app.js:253` `loadLibraryItems()` | 库分页 | 增加锁、真实游标、总量、重试上下文 |
| `public/web/app.js:290` `loadItem()` | 详情、全季串行预载 | 首数据先显示；抽出季列表/单季加载 helper |
| `public/web/app.js:327` `chooseDetailSource()` | 源与默认轨道 | 保留，是版本卡唯一选择入口 |
| `public/web/app.js:336` `playDetail()` | 预选源和轨道传入播放 | 保留参数与 Series 解析行为 |
| `public/web/app.js:369` `toggleFavorite()` | 上游收藏并更新 UI | 增加 busy、失败 Toast，成功后更新 |
| `public/web/app.js:385` `debouncedSearch()` | 400 ms 搜索 | 委托搜索控制器，加取消/过期防护 |
| `public/web/app.js:402` `imageUrl()`、`:409` `backdropUrl()` | 图片路径 | 保留认证；背景必须有 tag 才渲染 |
| `public/web/app.js:462` `playItem()` | 播放意图与 hash 路由 | 参数不变；Series 无可播集反馈改 Toast |
| `public/web/app.js:506` `startPlayerFromRoute()` | 唯一播放启动入口 | 挂接可见状态、集中错误处理 |
| `public/web/app.js:554` `startPlayback()` | PlaybackInfo、实例/会话、启动 | 保留业务流程，替换反馈与状态挂接 |
| `public/web/app.js:647` `stopPlayer()`、`:665` `exitPlayer()` | 销毁与进度上报/退出 | 增加 UI 清理，不改变上报对象 |
| `public/web/app.js:671` `switchVersion()`、`:679` `selectAudio()` | 切源/切轨 | 保留恢复时间和暂停状态，UI 锁覆盖整个重载 |
| `public/web/app.js:702` `selectSubtitle()` | 字幕选择 | 提交成功后更新；失败恢复旧轨或明确关闭 |
| `public/web/app.js:728` `refreshIcons()` | nextTick 后 lucide 转换 | 保留显式调用机制 |
| `public/web/app.js:732` / `:759` 生命周期 | 登录恢复、事件与取消 | 新增资源随路由/卸载清理 |

### 2.2 文件清单

| 文件 | 类型 | 职责 |
|---|---|---|
| `public/web/web.html` | 修改 | 页面结构、组件、资源装载 |
| `public/web/app.js` | 修改 | 原业务集成入口；API、路由、播放生命周期仍留在这里 |
| `public/web/ui.css` | 新增 | 仅 /web 使用的令牌与组件/页面样式 |
| `public/web/ui-components.js` | 新增 | `window.EIOComponents.register(app)`，注册通用 Vue 组件 |
| `public/web/ui.js` | 新增 | `window.EIOUI`，Toast、Hero、最近搜索、库视图、详情显示状态 |
| `public/web/page-state.js` | 新增 | `window.EIOPages`，首页与搜索请求控制器；依赖注入 api/query/用户与页面判断 |
| `public/web/player-ui.js` | 新增 | `window.EIOPlayerUI`，控制层显示、菜单、快捷键、可见播放状态 |
| `public/vendor/tailwind.css` | 必要时重建 | 新增/删除 Tailwind 类后必须同步生成结果 |
| `tests/web-ui.test.cjs` | 新增 | 新交互的关键状态与清理测试 |
| `tests/web-data.test.cjs` | 修改 | 模块加载 harness；分页/取消行为回归 |
| `README.md`、`README_EN.md`、`Update.md`、`Update Plan.md` | 修改 | 版本与实际交付行为同步 |
| `package.json`、`docker-compose.yml`、`install.sh`、`emby-in-one-cli.sh` | 版本相关修改 | 仅同步相应固定版本值，见 §11 |

`internal/backend/`、`public/web/player-libmedia.js`、`subtitles.js`、`subtitle-controller.js`、所有 vendor JS/WASM 和 `assets/panel.css` 为保护区。`public/embed.go:5` 已嵌入整个 web 目录，新文件无需改 embed 声明；`internal/backend/handlers_web.go:22` 已支持 `.css/.js` 和磁盘/嵌入逐文件回退，无需改静态路由。

### 2.3 装载与依赖约定

在 web.html 使用绝对站内资源路径 `/web/vendor/...`、`/web/web/...`，兼容 `/web/`、`/web/web/web.html` 和服务器已有的无扩展名 SPA 回退。禁止把 `public/web/ui.css` 写成 URL `/web/ui.css`。

新增样式：`/web/web/ui.css?v=v1.4.4-rc3`，在 Tailwind 后加载。原内联样式迁入 ui.css，保留播放器 host/video/canvas/subtitle 的定位规则；不改共享管理端 CSS。

脚本顺序：Vue → lucide → libmedia/JASSUB → subtitles/subtitle-controller/player-libmedia → data.js → ui.js → page-state.js → player-ui.js → ui-components.js → app.js。全部为现有 IIFE/全局构建风格，不引入 npm 运行依赖、打包器或 router。

把现有 `createApp({...}).mount('#app')` 改为保存 app 实例、`EIOComponents.register(app)` 后 `app.mount('#app')`。setup 的 return 显式增加模板引用，保留原公共名称与方法参数，避免大范围重命名。

## 3. 视觉基础与组件合同

### 3.1 令牌

ui.css 在 `body.eio-web` 定义变量；web.html body 添加该类。选择器均限定在该类或 `.eio-*` 组件下，禁止裸 `.card`、`.btn-primary`、全站 `body` 覆盖。

```css
body.eio-web {
  --bg-0:#070a12; --bg-1:#0d1322; --bg-2:#151d31; --bg-3:#1e2942;
  --t1:#f1f5f9; --t2:#94a3b8; --t3:#5b6b85;
  --line:rgba(148,163,184,.14); --line-2:rgba(148,163,184,.24);
  --ac:#4f46e5; --ac2:#7c3aed; --ac-text:#818cf8;
  --ac-grad:linear-gradient(135deg,#4f46e5,#7c3aed);
  --ac-glow:rgba(79,70,229,.35);
  --gold:#f5b544; --dv:#a78bfa; --atmos:#34d399; --danger:#f87171;
  --r-card:14px; --r-btn:10px; --r-pill:999px;
  --ease:cubic-bezier(.22,.61,.36,1);
  --dur-micro:120ms; --dur-base:220ms; --dur-slow:320ms;
}
```

- 正文/必要辅助信息分别用 t1/t2；t3 仅装饰。链接、计数、码率、剧集号和搜索简介不能用 t3。
- 间距基数 4 px；section 间距 32 px；页边距 ≥1024 为 32 px，768–1023 为 24 px，<768 为 16 px。
- 桌面侧栏 216 px；卡片半径 14 px、按钮 10 px；海报 2:3、剧集缩略图 16:9。
- 卡片 hover 上移 6 px，不放大整个卡片；active 可 scale(.98)。文字必须始终可读，hover 只增强信息。
- 动画限 transform/opacity；前 8 个条目的入场延迟为 `i < 8 ? i * 60 : 0` ms。reduced-motion 关闭位移、轮播自动播放和 stagger。
- backdrop-filter 仅用于播放器控制浮层、弹出菜单、Toast；海报、海报徽章、海报播放钮、详情静态轨道和按钮均使用普通半透明底。
- `:focus-visible` 2 px ac-text 描边；交互触点 ≥44×44 px，紧凑图标允许视觉 32/38 px、透明 padding 补足。

### 3.2 层叠与全屏

采用两套层级，不能将旧表中的 400/500 当作播放器外的全局浮层层级：

| 作用域 | 层级 |
|---|---|
| 页面内容 / sticky / 页面菜单 / 全局 Toast / 播放页 / 登录页 | 0 / 50 / 200 / 950 / 900 / 1000 |
| playerContainer 内：媒体 host / 字幕 / 遮罩 / 控件 / 菜单 / Toast | 0 / 100 / 120 / 150 / 200 / 400 |

把控制层、轨道菜单、加载/错误遮罩、播放 Toast 全部放在 `ref="playerContainer"` 内。对播放器容器请求全屏；不对内层 video/host 请求全屏。全屏 Toast 使用同一队列在容器内渲染；页面与播放器两个 ToastStack 用条件互斥，避免重复 DOM/live announcement。

保留字幕覆盖层定位兼容性，针对 EIOPlayer.host 与 `.player-subtitle-text`、字幕 canvas 明确层级；不要用通配 `canvas { z-index:... }` 把所有视频/字幕混成一层。

### 3.3 通用组件

| 注册名 | 输入/事件 | 规则 |
|---|---|---|
| `EioPosterCard` | item、imageSrc、detailHref、showProgress；emit play | 无 API；详情 anchor 与播放 button 为兄弟元素，禁止嵌套按钮/链接 |
| `EioStateBlock` | state=loading/slow/empty/error、message、actionLabel；emit retry | slow 是可重试的等待提示，不能伪装成实际超时错误 |
| `EioToastStack` | items；emit dismiss/hold/release | 一个可见实例；非紧急 role=status，错误 role=alert，aria-atomic |
| `EioTrackMenu` | label、options、value、disabled、open；emit toggle/select/close | option.value 使用实际源 Id 或流 Index；未知/空轨道不产生假选项 |

可重复的季分段、版本卡和剧集行使用模板/小组件按需要拆分；不要求为每段 CSS 建一个组件。

海报：缺图/加载失败显示渐变加标题首字；imageSrc 变化重置失败标记。标题最多两行、title 提供完整文本；评分/年份/规格缺失即隐藏。首屏 Hero eager，其余图片 lazy、固定比例避免布局跳动。

进度 `durationTicks > 0 ? positionTicks/durationTicks : 0`，限定 0–1。仅 `!Played && 0 < ratio < .95` 显示条；未知时长不显示百分比。已看 ✓ 可显示但本版不可点击改状态。卡片详情链接与播放按钮都能 Tab 到达，`:focus-within` 显示播放钮。

## 4. UI 和请求控制器接口

新增模块的最低公共合同如下。实现可补私有 helper，但不能在 UI 文件复制 api/apiRaw 或自行添加身份头。

| 工厂 | 依赖 | 返回状态与方法 |
|---|---|---|
| `EIOUI.createToasts()` | 当前全局计时器 | items、push、dismiss、hold、release、clear、dispose |
| `EIOUI.createHero({getItems,isActive,onChange})` | 首页已加载候选 | index、item、paused、select、hold、release、setAuto、reset、dispose |
| `EIOUI.createRecentSearches({getUserId})` | 当前普通用户 id | terms、read、add、remove、clear；storage 异常回退内存 |
| `EIOUI.createLibraryView()` | 无 | mode=grid/list、sentinel ref、attachObserver、disconnect；回调由 app 注入 |
| `EIOUI.createDetailView()` | 无 | selectedSeasonId、overviewExpanded、reset；纯显示状态 |
| `EIOPages.createHome({...})` | api、query、getUserId、isActive、libraries、latestSections、onChange | resumeItems、各块 loading/error、load、cancel、dispose |
| `EIOPages.createSearch({...})` | api、query、getUserId、isActive、searchQuery/results/loading、onChange | error、submittedQuery、movieResults、seriesResults、schedule、retry、cancel、dispose |
| `EIOPlayerUI.create({...})` | getPlayer/getContainer、getState、动作回调、Toast | status、error、operation、controlsVisible、openMenu、begin、acceptState、fail、end、activity、reset、dispose |

工厂在 Vue setup 中调用；内部响应式字段用 ref/computed，在 app setup 以 `reactive()` 包装返回对象后供模板使用，模板访问 `home.resumeItems`、`playerUI.status` 等不带 `.value`。私有逻辑仍使用原 refs，原有 setup 顶层 refs 的方式保留；更新 VM harness 对 reactive 的支持。不能混用普通对象里的嵌套 ref 与 Vue 顶层自动解包。

页面控制器自己持有 AbortController 与递增代次。每次 load/schedule/retry 前取消前一代；只有“控制器仍属于当前代、signal 未取消、页面仍正确、用户 id 未改变”才能提交成功、失败或 finally 状态。不得只保护成功分支而让旧 catch/finally 覆盖新页面。

`syncFromHash()` 离开 home/search 时调用各自 cancel；离开 library/item/collection 仍保留现有 abort。进入 search 时，有非空查询则重新 schedule。`doLogout()` 在清登录态前 cancel 页面任务、清 Toast、停止播放。卸载 dispose 全部模块。

## 5. 页面实施

### 5.1 登录与导航

- LOGIN 模板参考 03 登录分栏：左品牌和普通渐变背景，右 form。小于 768 px 隐藏/压缩品牌列，表单不能横向溢出。
- 为用户名/密码设置关联 label、autocomplete=username/current-password；密码显示按钮 type=button、aria-label 随状态变化。新增 `passwordVisible` ref；保留 `focusPass()` 的 passWrap 引用。
- 表单 submit 调 `doLogin()`；loading 禁止重复提交；错误放在输入下，aria-describedby 指向文本，错误时 aria-invalid。错误可轻微抖动一次，reduced-motion 禁用。保持普通用户/管理员登录限制。
- 原 pages 数组保留 home/resume/favorites/search，新增 `sidebarPages` 仅过滤 resume；不要删掉 resume 路由或测试中的 pages 公共对象。桌面侧栏显示首页/收藏/搜索，底部用户与退出。
- 首页面“继续观看 · 全部”始终可达 `#/resume`，包括空态。已有窄屏菜单仍能进入这四个现有页面；不实施底部新标签栏。
- 不在 UI 加服务器配置、代理模式或凭据管理入口。

### 5.2 首页：Hero → 继续观看 → 媒体库 → 最新

将 `loadHome()` 保留为 `return home.load()` 的集成入口，libraries/latestSections 原 refs 继续用于库页和模板。

请求预算固定：一条 Views；对非 playlists 的前 3 个库分别一条 Latest（每库 Limit=12，维持原上限）；新增一条 `/Users/{uid}/Items/Resume?Limit=12&Fields=PrimaryImageAspectRatio,RunTimeTicks,UserData,SeriesName`。Resume 使用一次 api，不调用 EIOData.all，不调用 loadCollection，不查询计数。

Latest 原请求的 Fields 扩展为 `PrimaryImageAspectRatio,ProductionYear,CommunityRating,Overview,Genres,RunTimeTicks,UserData,BackdropImageTags,OfficialRating,OriginalTitle`。字段是展示请求，不逐条查详情，不请求 MediaSources 来填首页徽章。缺失 4K/DV/Atmos 数据时隐藏规格，禁止按片名猜规格。

Views/Latest 任务链与 Resume 同时开始，互不 await；Latest 库间 Promise.allSettled。每块独立 loading/error，成功内容先显示；失效代次不能更新 libraries/latestSections。单块失败保留其他块并显示局部重试；Latest 某库失败记录该库失败，不把失败当空库。重试成功部分不重复请求，整页重试只用于全部内容任务失败。

Hero 从已加载 Latest 的 Movie/Series 中按出现顺序去重、取前 5 个；有 backdrop tag 用背景，无背景用渐变，空候选不渲染 Hero。不自动查询一条详情补简介。字段映射：Name 标题，OriginalTitle 可选副标题，ProductionYear/Genres/RunTimeTicks/CommunityRating/OfficialRating 元信息，Overview 三行。

Hero 播放调用现有 playItem(item)，详情是原生 `#/item/{Id}` 链接。8 秒轮播、320 ms 交叉淡入；只有 home 激活且 document 可见才计时，hover/focus、用户停止自动播放、reduced-motion 均暂停。提供有 aria-label 的指示按钮和暂停/恢复按钮；离页销毁 timer，候选更新时将 index 夹到有效范围，不启动重复 timer。

续看行复用 EioPosterCard 和真实进度，不显示 NextUp。空时显示简洁空态，保留“全部”；失败显示本区重试，不清空成功的 Latest。媒体库卡只显示 Views 中名称及可用图标/图片，不显示数量。Latest 行名维持“库名 · 最新”。

### 5.3 媒体库与分页

维持 `/Genres?ParentId=...&UserId=...` 和 `/Users/{uid}/Items?ParentId=...`，保留 Recursive、IncludeItemTypes、Genres、SortBy、SortOrder。Genres 失败显示无筛选项但不阻塞内容。

新增 `libraryPageLoading`、`libraryTotal`（未知为 null）、`libraryRetryAppend`；保持 libraryLoading 只描述首屏/切库阶段。工具栏显示库名、真实总量或“已加载 N 项”、流派胶囊、原四种排序与 grid/list 切换；滚动后的 sticky 仅背景/阴影变化，保持高度 56 px，避免旧稿 56→44 的布局跳动。

分页规则：

1. `libOffset` 明确定义为下一页的 StartIndex；初始化/筛选/排序重置为 0。请求捕获 requestOffset、parentId、请求控制器与筛选快照。
2. 同一状态仅一条分页请求；重复 IO/点击直接返回。切库/筛选取消旧请求并发起新首屏，不沿用旧游标。
3. 成功后按 Id 合并卡片，游标推进为 `requestOffset + batch.length`，不能用去重后的 libraryItems.length 覆盖游标。失败不推进。
4. TotalRecordCount 已知时用新游标比较；未知时仅空批结束，非空短页不能擅自认定结束。重复页无新 Id 时停止自动分页并显示“分页未推进，请重试”，避免无限请求。
5. IO 观察 sentinel，root 为实际滚动区（当前页面滚动用 null），rootMargin=600px。仅 navPage=library、hasMore、无 pageLoading/error 时调用 loadLibraryMore。
6. 每页渲染完成重新检查 sentinel；attachObserver 先 disconnect，离开库页/卸载断开。加载更多按钮始终作为可用兜底，不因支持 IO 而永久隐藏。
7. 失败后暂停 IO；重试调用 `loadLibraryItems(libraryRetryAppend)`。追加失败保留已有卡片，错误条放列表下方；首屏失败用状态组件。

条目卡字段可补 CommunityRating/RunTimeTicks/UserData 至现有 Fields，不能引入每卡额外请求。列表视图复用相同 item 数据和播放行为。清除筛选只清 libFilter、保持当前排序。

### 5.4 详情、版本、季集和轨道

主详情路径、Fields 和默认源选择机制保持；允许在原 Fields 加 OriginalTitle/SeriesName/IndexNumber/ParentIndexNumber 作为展示字段，不额外请求。

主数据成功后立即结束 itemDetailLoading 并显示英雄区，季列表/季集使用独立 loading/error；禁止等待所有季请求完成才显示详情。元信息优先用 `Genres` 字符串数组，缺失再取 `GenreItems[].Name`；评分、年份、时长、分级缺失即隐藏。长简介三行加展开/收起，不足三行不画无效按钮。

版本卡按 MediaSources 渲染，使用 fieldset/legend 与原生 radio；选择只能调用 chooseDetailSource(src.Id)。显示 src.Name、mediaSourceTags(src)、可用码率（优先 src.Bitrate、其次视频 BitRate，除以 1e6 为 Mbps）；未知隐藏。<640 px 使用原 select，保证一个可访问选择控件，不同时让隐藏 radio 可聚焦。EioIncompleteSources 用 amber 行内提示与 retryItem。

播放按钮调用 playDetail()，非 Series 有真实 PlaybackPositionTicks 时显示“从 mm:ss 继续”；Series 保留现有 resolveSeriesEpisode，不显示未经解析的具体 S/E 或“已看 N 集”总结。收藏按钮保留原 API：请求中按 item.Id 禁用，同一项不重复提交；成功后更新 UserData，失败保留旧值并 Toast；无需新增乐观更新。

季集加载从 loadItem 中抽出：`loadSeriesSeasons(detail, request)`、`loadSeasonEpisodes(seriesId, season, request)`；新增 `seriesLoading/seriesError`、`seasonLoading`、`seasonLoadBusy`。保留 EIOData.all 和原接口 UserId/SeasonId/Fields，按季串行预载，不改成并发全季请求。

- 季列表到达后 selectedSeasonId 选第一季；切 tab 只切显示，不重新 fetch。首次 seasonEpisodes 尚无键时显示骨架，有键且空数组才显示本季为空。
- 每季完成立即展示数据或错误，错误季 tab 加可读标签/错误点，其余季正常。
- 新增 `retrySeason(seasonId)`，仅请求该季 Episodes，不调用 retryItem。预载串行队列还运行时暂禁 retry，防止重试形成额外并发；队列完成后可重试失败季。
- 季列表本身失败用 seriesError，重试只重载季列表和季集，保留主详情。
- 每个提交都检查 itemRequest===request、signal、当前详情 Id；离页取消不得残留 seasonLoading 或覆盖新详情。
- 剧集行保留详情链接和独立播放按钮；显示真实 E 编号、时长、进度、Played ✓。已看只降图片/非关键信息明度，标题与操作仍满足可读性；不提供标记按钮。

视频信息、音频和字幕区保留 currentMediaStreams/audioStreams/subtitleStreams 的源依赖；选择详情轨道仍只更新 detailAudioIndex/detailSubtitleIndex，启动播放时才应用。字幕含关闭选项；无轨道清楚显示“无字幕”，不伪造语言/格式。不要把所有 Dolby TrueHD 都标成 Atmos；只有明确对象音频元数据/名称依据才显示 Atmos，否则显示真实编码。

### 5.5 搜索

保持 400 ms、Limit=30、Recursive=true、IncludeItemTypes=Series,Movie；新增的 Fields 仅供展示。输入 placeholder 改“搜索电影、剧集”，不承诺当前未实现的演员搜索。

schedule 在输入发生时立即递增代次、取消旧 fetch 和旧 debounce，清除过期错误；非空输入的等待期间显示加载/待搜索态，避免提前出现“无结果”。实际请求捕获 trim 后词；成功/错误/finally 只提交当前代。空输入同步清结果/loading/error，不发请求；离页 cancel。

按 Type 分成电影/剧集两区，计数仅为本次返回的组长度，不宣称服务器总结果。初始提示、等待、失败重试、成功空结果四态分开。失败重试使用 submittedQuery，不拿另一个尚未提交的输入覆盖它。

最近搜索使用 `eio_recent_searches_{userId}`，最多 8 条去重；只在 Enter 提交非空词或打开有效结果时写入，不把每次防抖词存成历史。doLogin 成功设置用户 id 后、onMounted 恢复普通用户后调用 read，退出清内存但不删除用户自己的记录。空输入聚焦时显示，可单条删除，storage 读写失败保持内存可用。不得存 token、服务器或媒体 URL。点击最近词填写输入并 schedule。

### 5.6 继续观看和收藏

保留 `#/resume`、`#/favorites` 与 loadCollection(kind)。继续观看走 Items/Resume，收藏走 Filters=IsFavorite；保持完整分页和后端返回顺序，不做 LastPlayedDate 重排。

重绘标题、卡片和 loading/error/empty。已有卡片可显示已加载计数，但不承诺“完整观看历史”。收藏失败刷新仍可重试，移除收藏成功再 reload；取消/旧请求失败不可污染另一集合页面。列表不添加历史 tab、已看写入或外部播放入口。

## 6. 播放器控制层实施

### 6.1 DOM 与状态合同

EIOPlayer 仍只创建并拥有独立 host，Vue 拥有其余 UI；不对 playerContainer 使用 replaceChildren/innerHTML，不修改引擎创建元素顺序。保护 `public/web/player-libmedia.js:9` 的 host 机制、AbortController、超时回退、MSE/WebCodecs 路径。

UI status 限定为 idle/loading/playing/paused/ended/error；另设 operation=null/version/audio，error 含 kind/message/retryContext。没有 buffering/successful-autoplay 等推测状态。

- startPlayerFromRoute 开始：保存当前切换 operation 后 stopPlayer，恢复 operation 并 begin('loading')。请求 PlaybackInfo 时显示“正在加载播放信息…”；引擎 onState 的 loading/message 如实显示。
- onState 必须保留 `player===instance && generation===playbackGeneration` 校验，映射 mode/time/duration/playing；loading 为真优先于 playing。不能因 time 更新关闭仍在加载的遮罩。
- startPlayback 成功且当前代有效后结束 operation，并按 playing 判为 playing/paused。结束 onEnded 保留现有 exitPlayer()，不出现连播卡或计时器。
- stopPlayer 取消 controller、清 UI 计时器/菜单/拖动预览，仍使用实例自己的 item/source/session 上报 Stopped，并销毁实例。不能把 UI 销毁当作新播放上报。

### 6.2 启动和运行错误

api 保留现有 Error 文本，仅增加 `error.status=res.status`；429 判定使用该属性，不用中文字符串匹配，不新建全局自动重试。

新增 app 内 `handlePlaybackFailure(error, generation, retryContext)`。有效代失败时保存重试上下文、调用 stopPlayer 释放/销毁已经存在的实例、清 pending 意图，再显示 error，navPage/hash 保持 player。无实例时不伪造 Stopped 上报。

startPlayback 的有效错误交给外层 startPlayerFromRoute 处理，避免两个 catch 各显示一次；实例运行 onError 也走同一函数。AbortError、过期代次和已销毁实例不显示错误。同一次失败只生成一张错误卡。

| 场景 | 显示/动作 |
|---|---|
| 429 | “服务器并发已满”；重试；多版本时可选其他版本，不显示自动释放倒计时 |
| PlaybackInfo/原始流/兼容流最终失败 | “无法加载播放内容”；当前安全错误摘要；重试、返回详情 |
| 播放中最终错误 | 停止引擎并保留错误卡；不 alert 后自动退出 |
| 队列加载失败 | 非阻塞提示 + retryEpisodeQueue；前后集不可用时 disabled |
| 字幕、暂停/继续、seek、全屏操作失败 | Toast；保留播放器和可用控件 |

重试上下文捕获 item、selectedMediaSourceId、audio/subtitle、startSeconds、paused/forceTranscode；初次失败从原请求意图重试，运行错误从最后确认的时间重试。重试使用 playItem(item,id,options) 或同一路由 force 启动，不能建立第二个播放入口。返回详情到 `#/item/{SeriesId||Id}`；关闭按钮仍按 playerReturnRoute 返回。

错误状态没有 live player，选择其他版本不能调用依赖 player 的旧 switchVersion；用错误卡中的源选择更新 retryContext，再调用同一重试入口。

### 6.3 自动隐藏与菜单

播放器进入/暂停/加载/错误时控制条常显。playing 且无活动 3 秒、未拖动、未 hover/focus 控件、无菜单、无 trackBusy/operation 时隐藏。mousemove、pointerdown、键盘有效操作唤醒；document 隐藏时取消 timer，恢复后再安排。

hidden 时 opacity=0、pointer-events=none、画面 cursor=none；键盘 Tab 到控件之前必须唤醒（监听焦点/键盘），有控件焦点时绝不隐藏。页退出、实例切换、卸载清理 timer/listener。

桌面画面单击播放/暂停，双击全屏；单击延迟 220 ms 并在双击时取消，且只接受非控件/菜单/字幕链接区域。窄屏画面单击仅切换控制层，保留原基础触控可用性。

轨道/版本菜单同时只开一个；button 有 aria-expanded/aria-controls，开时焦点落到当前项，Esc/外部 pointerdown 关闭并还焦点。选项使用原生 radio group 或等价完整键盘实现；max-height=60vh，长名称截断加 title。无音轨/字幕时隐藏对应入口。菜单关闭是 Esc 优先动作，不误退出播放器。

### 6.4 切源与切轨

保留 switchVersion 的 startSeconds、paused，以及 selectAudio 热切失败后的原 playItem 回退；不改 forceTranscode 条件或 PlaybackInfo DeviceProfile。

重载开始设置 operation=version/audio，loading 遮罩显示“正在切换版本/音轨，进度已保留”。禁用所有源/轨道选择及重复切换；锁至少维持到当前 startPlayerFromRoute 最终成功/失败。

注意旧 playItem 改变 hash 后立即返回，原 finally 会过早清 trackBusy：因此禁用条件必须包含 `loading || trackBusy || operation != null`，operation 由有效加载代次结束时清，不能只依赖旧 finally。stopPlayer 过程中为本次重载保存/恢复 operation；旧代次 finally 不得解锁新重载。

字幕必须按现有实际合同接入：`public/web/player-libmedia.js:218` 的 selectSubtitle 先设置 subtitleIndex，catch 后通过 onState.subtitleError 通知并吞掉错误；因此 await 成功返回及 instance.subtitleIndex 都不能单独证明切换成功。`public/web/subtitle-controller.js:53` 的 select 又会先 clear，失败仅还原 UI 值会留下假状态。

在 app 增加独立 subtitleBusy、subtitleSwitchSerial 和 `subtitleAttempt={instance,generation,serial,failed,message}`，新增 `attemptSubtitleSelection(instance,index,generation)` helper：先建立 attempt，调用实例 selectSubtitle，兼容 Promise 拒绝；onState 中已有实例/代次校验通过后，将 subtitleError 写入当前 attempt 的 failed/message。await 返回后同时检查 attempt.failed、实例与代次，返回真实结果；finally 只清自己的 attempt，不碰新代状态。

selectSubtitle(idx) 在期间禁止并行选择，保存当前源最后一次确认成功的 oldIndex。新选择成功才更新 wasmSubTrack；失败则用同一 helper 尝试 oldIndex，恢复成功再显示旧选择，恢复也失败则尝试 -1、明确显示关闭并发一次错误 Toast。恢复过程的 subtitleError 只进入 attempt，不在 onState 立即重复 Toast。离页/实例改变不继续恢复，不污染新实例。

instance.load 内的默认字幕选择也可能发 subtitleError，此时没有用户 attempt：onState 将其记到该实例的 startupSubtitleError。load 返回后且当前代仍有效，清到 -1、显示关闭并发一次“默认字幕加载失败，可重新选择”的 Toast，视频继续加载；不能再把 line 627 的 instance.subtitleIndex 当成成功轨道。所有处理位于 app/UI 适配层，保持播放器和字幕内核不变。

版本切换后默认轨道仍按新源执行既有规则，不把旧源 Index 直接映射到新源。所有菜单 value 与真实 Id/Index 一致。

### 6.5 进度、音量和快捷键

保留 seekPreview 与 onWasmSeek：input 仅更新预览，change 提交一次实际 seek；pointerup 只清拖动状态，不能再次提交；step=any。新增气泡位置按进度控件 getBoundingClientRect 计算并限制边缘，展示 fmtTicks 时间；键盘调整也有可读时间。duration 未知/为零时禁用 seek，不能造 1 秒可播进度。

本版不显示缓冲条和缩略图：未暴露完整缓冲信号，不能画假缓存。range 提供 aria-label、aria-valuetext；只在 hover/focus/拖动时增粗 thumb，不改变周围布局。

音量沿用 0–1 数值，UI 百分比与图标按真实值；新增 lastNonZeroVolume（默认 1）用于静音/恢复；按钮和 range 都调用 changeVolume。不修改引擎音频管线。

| 按键 | 行为 |
|---|---|
| Space/K | wasmTogglePause |
| Left/Right/J/L | 前后 10 秒，夹到 0–duration；未知 duration 不执行 |
| Up/Down | 音量 ±0.05，夹到 0–1 |
| M | 静音/恢复 |
| F | toggleFullscreen |
| C | 关闭/恢复当前源最近成功字幕；轨道不存在时不执行 |
| Esc | 先关闭菜单，再退出实际浏览器全屏，再退出播放器 |

仅 navPage=player 且存在 live player/相应可用操作时处理；对输入、textarea、select、contenteditable 和控件内 Space/Enter/方向键交给原生行为，避免按钮双执行或 range 同时跳时间/改音量。Ctrl/Alt/Meta 组合不接管。toggle 类忽略 repeat；方向连续 seek 允许但合并未完成提交，只保留最新目标，不能形成几十个并行 seek。

由浏览器自行消费的全屏 Esc 通过 fullscreenchange 更新状态，不在同一次按键继续 exitPlayer；手动退出全屏后仍留在播放页。keyup/外部点击/页面失焦不遗留拖动状态。

## 7. Toast、加载与异常态一致性

Toast 最多 3 条；普通 2.5 秒、错误 4 秒，hover/focus 暂停，关闭/清队列/卸载清 timer。桌面右下，窄屏顶部，不挡主要播放控制。异步结果只能对仍有效页面/实例发通知；禁止在模板直接调用产生副作用的 push。

以 `rg -n -F 'alert(' public/web/app.js public/web/ui.js public/web/page-state.js public/web/player-ui.js public/web/ui-components.js` 检查生产 Web 入口，完成后不留 alert。媒体/API 完整 URL、api_key/token 不能放在 Toast 或测试截图；UI 文案用有限错误摘要。

首次加载骨架与最终比例相同；等待超过 8 秒可显示“加载较慢”与重试，原请求继续处于 loading，不冒充已失败。重试取消旧请求后开始新一代。完成/取消必须清 slow timer；播放器使用原引擎真实 loading，不加独立 8 秒重试来干扰原 20 秒回退。

状态要求：有数据但新请求失败保留可用内容；空态仅用于成功空响应；失败显示重试；聚合内部失败若响应没有可见完整性字段，不宣称前端知道所有服务器状态。EioIncompleteSources 和 seasonErrors 是明确可显示的异常来源。

## 8. 资源生命周期清单

| 资源 | 归属 | 清理时机 |
|---|---|---|
| 首页 Views/Latest/Resume controller、slow timer | EIOPages home | 再次加载、离首页、退出、dispose |
| Hero interval、visibility handler | EIOUI Hero | 候选切换重排、暂停、离首页、dispose |
| 搜索 debounce/controller/slow timer | EIOPages search | 任意输入、离搜索、退出、dispose |
| 库请求/分页 controller、sentinel observer | app + library UI | 切库/筛选、离库、退出、dispose |
| 详情 request、季加载状态/显示状态 | app + detail UI | 新 Id、离详情、退出、dispose |
| controls timer、画面单击 timer、全局键/焦点/菜单 handler | player UI | stopPlayer、离播放、退出、dispose |
| Toast timer、hold 状态 | EIOUI Toast | dismiss、挤出旧条、退出、dispose |

监听器每个模块只安装一组；再次进入通过 activate/状态判断复用或先移除再安装，不逐次累加。所有外部 handler 保存引用用于 removeEventListener。保留 onBeforeUnmount 现有 AbortController 和 searchTimer 清理，若 searchTimer 已迁入控制器则删除该旧变量及重复清理。

lucide 不启用 MutationObserver；组件挂载/选项更新调用显式 onChange/refreshIcons。需要频繁变化的播放、静音、收藏图标优先用 Vue 管理的内联 SVG 或键控组件，不让 lucide 替换 DOM 后产生旧图标缓存和无限转换。

## 9. 实施阶段与完成条件

所有阶段在同一 rc3 工作分支完成，可按职责拆提交。代码改动前保存基线验证结果；本执行文档本身不授权发布 tag、推送或替换正在运行的服务。

| 阶段 | 必做工作 | 完成条件 |
|---|---|---|
| P0 基线 | 确认 git 改动/分支；读取代码；选真实亮/暗/高饱和海报；确定 protected 文件 | 明确本次文件清单与已有改动，无新功能混入 |
| P1 基础 | ui.css、组件注册、Toast、资源 URL、登录/侧栏、真实海报卡 | 1440/1024 px 布局可读，焦点可达，管理端不变 |
| P2 页面 | 首页、库页、详情、搜索、续看/收藏；接取消/重试 | 关键请求/分页/季失败测试通过，所有成功/空/失败页面可用 |
| P3 播放器 | 控制层、菜单、隐藏/快捷键、错误重试、全屏、切轨锁 | 原播放/字幕回归通过，切源恢复时间及暂停，不出现孤立实例 |
| P4 交付 | 版本与缓存、构建、CI/截图、rc3 更新日志 | §10–12 全部满足，未完成项有明确记录且不能冒充完成 |

P1 可以先搭组件再迁模板；页面只在对应阶段启用，不留下半接通按钮。不要为了保留旧 CSS 在最终模板同时渲染两套页面或两份播放器控件。

## 10. 验证计划

### 10.1 现有回归与新测试

保持已有四个前端测试文件有意义地通过：

```powershell
node --test tests/web-data.test.cjs tests/web-player.test.cjs tests/web-subtitles.test.cjs tests/web-mse-seek.test.cjs tests/web-ui.test.cjs
```

`tests/web-data.test.cjs:7` 的 harness 当前只加载 data.js/app.js，迁入新依赖后依照生产脚本顺序加载 ui/page-state/player-ui/ui-components。Vue stub 增加 reactive 和 app.component；补必要 EventTarget、document、storage、计时器的可控 stub。不能为了通过删掉原库范围、完整分页、收藏/续看、默认轨道、季顺序断言。

新测试采用 node:test + VM 的既有风格，可将共用 harness 抽到 `tests/web-ui-harness.cjs`；不新增测试框架。测试主要验证行为和竞态，CSS 色值/类名不逐条写镜像断言。

| 编号 | 输入/操作 | 必须断言 |
|---|---|---|
| T01 | 首页一个 Latest 失败、Resume 成功 | 成功内容可提交，失败可重试；Resume 仅一次 Limit=12，无 all/NextUp/Counts |
| T02 | 首页切出后旧请求成功/失败 | libraries/latest/resume/error/loading 均不被旧代覆盖，无 Toast |
| T03 | 搜索 A 慢、输入 B、A 晚到 | 仅 B 结果可见；空输入/离页取消，旧 finally 不结束 B 的 loading |
| T04 | 库 sentinel 连续触发/按钮重复点 | 同状态只有一个追加请求；切筛选取消旧追加 |
| T05 | 库追加去重/失败/重试 | 请求游标按原 batch 推进；失败不推进；重复页不无限请求 |
| T06 | 两季，其中一季失败，再单季重试 | 另一季和详情保留；重试只请求目标季 Episodes |
| T07 | 轨道选择/版本切换 | 默认轨道按新源重置；期间锁不提前释放；旧 finally 不解锁新操作 |
| T08 | PlaybackInfo 429/流最终失败 | 一张错误卡；不 alert/自动跳离；有实例才上报 Stopped；重试复用原源/选项 |
| T09 | 播放中停止/离页、旧 onState/onError 迟到 | 不恢复已销毁实例，不写新代状态 |
| T10 | playing 空闲/暂停/控件焦点/菜单打开 | 3 秒隐藏只在允许条件；交互唤醒；退出无活动 timer |
| T11 | 输入/range/按钮焦点、全屏与菜单 Esc | 原生键不被双处理；Esc 按优先级处理，不一键连退全屏和页面 |
| T12 | 字幕 Promise 正常返回但 onState 发 subtitleError；旧轨恢复也失败；启动默认字幕失败 | 按信号判失败，正确还原或明确关闭；只一次 Toast，启动视频继续，不读取假成功 index |
| T13 | Toast 第四条、hover、dismiss、dispose | 最多三条，时间暂停可恢复，所有被移出项 timer 清理 |
| T14 | 最近搜索 storage 抛异常/不同用户 | 搜索仍可用；记录去重/≤8，用户互不读取；不存媒体 URL/token |

现有 web-player 测试保护直接流超时一次回退、退出取消、流索引映射和高分辨率软解约束；web-subtitles 与 web-mse-seek 保护字幕/seek 管线。不要修改 vendor 或内核来“适配”新 UI 测试。

### 10.2 构建和 Go 检查

每次改 Tailwind 类后运行 `npm run build:panel` 并保留应提交的 CSS 差异；build:panel 不生成 ui.css。若使用 Vue 动态类，使用完整字面量或 UI CSS，不拼接 `bg-${name}` 让 Tailwind 漏扫。

```powershell
npm run build:panel
node --check public/web/app.js
node --check public/web/ui.js
node --check public/web/page-state.js
node --check public/web/player-ui.js
node --check public/web/ui-components.js
$env:GOMAXPROCS='2'
go build -p 1 -ldflags '-X main.Version=v1.4.4-rc3' -o build/emby-in-one-v1.4.4-rc3.exe ./cmd/emby-in-one
go vet -p 1 ./...
go test -p 1 -parallel 2 -count=1 ./...
& './build/emby-in-one-v1.4.4-rc3.exe' --version
```

--version 必须输出 `v1.4.4-rc3`。本地低并发以免与实播争用 CPU；不要在 4K 播放时运行构建/全量测试。CI 仍须通过 `.github/workflows/ci.yml` 的 build/vet、Linux `go test -race -timeout 20m ./...`、Tailwind 重建一致性和 shell 语法检查；不得因本版没有后端改动而删除原检查。

版本变更的脚本逐个检查：`bash -n install.sh`、`bash -n release-install.sh`、`bash -n emby-in-one-cli.sh`。不能把多个脚本作为 bash -n 的连续参数，否则只检查第一个。本地没有 Bash/C 工具链时记录环境限制，不将未运行项写成通过。

### 10.3 浏览器矩阵

使用 agent-browser 技能进行浏览器验证，实际读取技能/CLI 文档后操作；以下是验收目标，不要求采用未经安装的测试工具。生产代码不包含测试网络 stub。

| 维度 | 必测内容 |
|---|---|
| 1440×900、1024×768 | 登录、首页、库 grid/list、电影/Series/Episode 详情、搜索、收藏/续看、播放器 |
| 768 px、375 px | 基础页面不横向溢出；既有菜单、表单、播放与退出可用；不按桌面大稿强行 min-width=900 |
| 数据 | 缺图、坏图、长标题、无评分/年份/轨道、空库、超多流派/版本、长轨道名 |
| 网络 | 首页局部失败、搜索乱序/失败、分页追加失败、某季失败/重试、PlaybackInfo 429/500 |
| 播放 | 原生 MP4、libmedia MKV/HEVC、兼容 HLS、暂停 seek、跨季前后集、源切换、音轨、字幕关闭/恢复 |
| 浮层 | 正常与浏览器全屏均能看见错误/Toast/菜单；外部点击/Esc/focus 返回正确 |
| 可达性 | Tab/Enter 全流程、按钮真实名称、range 键盘、当前选项、reduced-motion、暂停 Hero 自动轮播 |
| 存储 | localStorage 禁用/损坏、切换普通用户、现有登录恢复、管理员不能进 Web |
| 发布资源 | 磁盘新文件加载、无磁盘 public 的纯嵌入构建、管理端共享 CSS 不回归 |

网络失败/乱序优先用已有测试夹具或浏览器拦截构造，不修改真实上游和账号配置。真实播放选已获授权的测试账号和小片源；高分辨率只短时验证硬解，不做持续软解压力测试。

真实海报必须覆盖亮部、高饱和、暗部三类；按实际图片与遮罩抽查文字对比度/可读性。03 的渐变占位预览和 node 测试不能代替该验收。

纯嵌入验证：在独立临时工作目录放入刚构建的二进制和隔离测试配置，使用空上游与独立 data、非生产端口启动；确认 `/web/`、`/web/web/ui.css`、四个新 JS 的 Content-Type/内容正确，不能把返回的 SPA HTML 误认为 JS 成功。不要移动/隐藏实际项目 public 或 config 来模拟缺文件。

截图和记录放 `build/qa-v1.4.4-rc3/`，不得包含 token/媒体直链/密码；至少包括登录、带真实海报首页、库两种视图、电影/剧集详情、搜索空/失败、播放器普通/全屏菜单及错误态。按状态列出 PASS/FAIL/NOT RUN，未实测的真实片源能力注明边界。

## 11. 版本与文档同步

这些是实施代码完成后的动作；本次编写执行文档不提前把生产 README 标成已发布。

| 文件/锚点 | rc3 修改 |
|---|---|
| `README.md:3`、`README_EN.md:3` | 当前版本改为 V1.4.4-rc3 (Pre-release) |
| README “内置 Web 客户端”段 | 写实际三项桌面导航、首页续看入口、视觉/控制层变化、原播放能力与验收边界 |
| README 显式预发布安装例子 | 已存在的 rc1 指定版本例子同步到 V1.4.4-rc3；保留预发布需显式指定的说明 |
| `Update.md` | 顶部新增精确 `## V1.4.4-rc3`；只记录已实现 UI 与验证，不覆盖 rc2 历史 |
| `Update Plan.md:3` | 保持“稳定版 V1.4.3”；将括号内预发布改 rc3。未来计划按实际交付更新，不宣称正式 V1.4.4 已发布 |
| `package.json` version | 1.0.0 → 1.4.4-rc3；scripts/运行依赖策略保留 |
| `docker-compose.yml:6` build.args.VERSION | v1.4.4-rc1 → v1.4.4-rc3 |
| `install.sh:237` 生成 compose 的 VERSION | v1.4.4-rc1 → v1.4.4-rc3；不改变下载/安装流程 |
| `emby-in-one-cli.sh:4` 标题、`:8` VERSION | 标题 rc3，变量 `1.4.4-rc3`；保留动态 version_tag 的 Docker 更新路径 |
| `cmd/emby-in-one/main.go:18` | 保持 Version="dev"，发布/本地构建用 ldflags 注入；不硬编码发行号 |
| `Dockerfile:2`、安装器/CLI 内 Dockerfile ARG VERSION | dev 默认保持，正式构建由已有 build arg 传入 |
| `release-install.sh` | 版本来自 Release tag，不增加 rc3 硬编码常量 |
| `internal/backend/handlers_system.go` Emby Version | 保持兼容声明 4.7.14.0，不能换成项目版本 |
| `.github/workflows/release.yml` | 现有 V* tag/小写资产/rc 日志解析支持 rc3，无需改 workflow 或主动触发发布 |

Web 缓存：改变内容的 app.js/data.js/新 UI 模块与 ui.css 引用统一 `?v=v1.4.4-rc3`；共享 tailwind 内容变更时其 Web 引用同步。未修改的内核/vendor 资源保持现有参数，不伪装升级 libmedia/JASSUB。所有引用改为 §2.3 的站内绝对路径后检查直接文档入口。

`Update.md` 新节至少包括：发行范围、UI/交互改动、保留的播放器链路、新增有界首页 Resume 请求、测试结果与未测边界。不得写外部播放器/自动连播/历史页/手动已看/相似推荐已完成。不要将设计预计值写成测试结果。

新版本发布若另获授权，按 AGENT.md 只写主仓库 origin，github 只读；不要在 UI 执行过程中自动推 tag、创建 Release 或改发布远程。

## 12. 最终验收和交付清单

以下全部勾选才可将本 UI 任务标为完成：

- [ ] 色板、卡片、按钮、登录、侧栏和全部目标页面使用同一体系；Web 与管理端 CSS 隔离。
- [ ] 首页只有 Views/既有至多三个 Latest/新增一条有界 Resume；Hero 不补逐卡详情、计数或规格请求。
- [ ] 三项桌面导航与首页“全部”能到原 resume；收藏/续看深链和登录恢复可用。
- [ ] 库筛选、四种排序、grid/list、分页错误/重试、IO/按钮防重复全部通过。
- [ ] 详情首数据先显示，版本选择决定真实源与默认轨道，季分段/单季失败重试不拖垮其他内容。
- [ ] 搜索不混入 Episode，当前词结果不被旧响应覆盖；最近搜索存储失败仍能搜索。
- [ ] 缺字段、坏图、空响应、局部失败有正确状态，必要文字可读，无假规格/假缓冲/假总量。
- [ ] 播放器正常/全屏控制与菜单可用；隐藏时键盘唤醒；切源/切轨忙态覆盖完整重载。
- [ ] 启动/运行错误可重试、429 独立表达，不自动跳走；字幕失败恢复或明确关闭。
- [ ] 页面切换/播放退出不会继续解码、复活旧请求、遗留计时器/监听器或重复会上报。
- [ ] 原生/libmedia/HLS、字幕/seek 原回归保持；保护文件无未经范围解释的改动。
- [ ] Tab/Enter、range、菜单 Esc、reduced-motion 和基础窄屏可用性通过；真实海报视觉已验证。
- [ ] 四个现有前端回归、新行为测试、语法、Tailwind、Go build/vet/test 和既有 CI 检查完成。
- [ ] V1.4.4-rc3 版本、缓存、README/Update 同步；纯嵌入资源验证通过。
- [ ] 无外部播放入口/弹窗/deep link，无 history/标记/连播/Similar/首页 NextUp 新功能。
- [ ] 交付 diff、验证记录、截图和明确的未测边界；不在此任务中自动发布或替换服务。

交付说明：列出改动文件与行为、测试/截图位置、实际 UI 版本及已知限制。若有范围内未完成项，明确标记而非只交一张正常态首页截图。

## 13. 给执行者的起始指令

> 按 `design/web-ui/05-execution-plan-v1.4.4-rc3.md` 实施 V1.4.4-rc3 的 /web 前端 UI 改进。先重新读取 AGENT.md、工作区差异及本文涉及的源文件，以本执行文档覆盖旧设计中冲突的范围。完成本文固定页面、播放器控制层、状态、请求清理与验证；不实现外部播放直链，也不搭载本文列出的功能扩展。保持 Go 后端与播放/字幕内核业务行为，按模块拆分保持文件职责与行数约束。执行 P0–P4，完成验收与版本同步，提供代码 diff、测试记录和浏览器截图。本任务不含自动发布、推送或替换当前运行服务。

## 14. 升级资源与回滚边界

发布候选二进制必须在所有 UI 文件与生成 CSS 完成后重新 build，确保 public.Assets 内嵌的是本次文件。仅复制 rc3 的 app.js 或只换 HTML 都不是完整升级包。

当前 `internal/backend/handlers_web.go:66`/`:120` 允许磁盘 public/web/web.html 覆盖内嵌文档。如果部署已有旧 public/web，单换二进制仍可能显示旧界面。升级说明必须要求备份后同步本版 public/web 全部文件和相应共享 Tailwind，或在独立配置验证后采用纯内嵌部署；本 UI 任务不修改安装器更新策略或擅自移走生产 public。新旧入口与模块需要成套交付，避免缓存参数更新而实际文件仍旧。

代码回滚仅针对本次提交或保存的补丁，不使用全工作区 reset/clean 覆盖用户已有改动。运行版本回滚如另获授权，使用成套的前一版本二进制及 Web/CSS 备份，保持 config/data 不变；遵守 AGENT.md 的回收站删除规则。未修改数据库/后端模式，因此不引入数据迁移回滚。

无需同时维护两套永久生产 UI。各阶段提交应可追溯；交付时注明所验证的 commit/工作区版本，实际发布操作另行执行。
