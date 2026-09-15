# 虚拟用户 ID 透传缺陷：修复执行方案

基准日期：2026-09-15。修订版：R2，已纳入后续复核意见。状态：待实施。本文只定义修改与验收，尚未修改 Go 代码或初始化 Git。

执行约定：以本文中的动作表、阶段边界和验收结果为准；标为“未分类/残留”的场景不得临时扩展策略。本文不要求创建子任务或使用其他模型。

所有已有文件链接均指向本次核查时的实际行号；实施后用函数名重新定位。标为“待新增”的文件没有现成行号，不虚构定位。工程根目录为 D:/UserData/Desktop/Emby-In-One-main。

**1. 交付目标与边界**

第一批交付：P1 请求身份修复 + P2 认证头与 JSON 处理 + P4 日志脱敏及验收。覆盖已支持接口的 path、query、JSON body、认证头，以及 redirect 生成的上游 URL。

第二批交付：P3 普通用户响应身份一致性修复，独立提交、独立验收、可单独回退；先完成第一批对新旧本地用户 ID 的兼容，再切换响应身份。P3 不阻塞第一批缺陷修复发布。

第三批交付：P5-test 纯扫描函数与测试侧 D。生产 D（P5-prod）的环境开关、运行时接线、限频表与预算配置暂缓实施，待有实际诊断样本后另行评估。D 的扫描通过不代表路由和身份翻译正确。

使用当前选定上游的真实用户 ID 和 token。身份映射不参与选择上游；item、MediaSourceId、PlaySessionId 的资源解析与现有权限检查保持原有职责。用户管理、授权列表中的目标用户不按“当前用户”批量覆盖。

本批支持现有媒体、播放、用户状态接口，以及能确认当前用户语义的兜底请求。不新增任意用户管理接口、不扩展 IDStore 数据模型、不引入完整 Emby schema。未知 body 格式与未知目标用户语义，按下文规则单独处理。

**2. 本次定位后的三个实施约束**

| 事实与位置 | 对实施的影响 |
|---|---|
| [internal/backend/upstream.go:619](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:619) 的 doRequest 是代理自身 HTTP 请求的集中出口 | 正常请求统一在这里完成身份归一化与最终认证头构建 |
| [internal/backend/media_stream.go:84](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_stream.go:84) 的 redirect 使用 [internal/backend/upstream.go:600](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:600) 的 BuildURL | 客户端直连上游的 URL 不经过 doRequest；BuildURL 必须复用同一 URL 处理规则 |
| [internal/backend/aggregation.go:35](D:/UserData/Desktop/Emby-In-One-main/internal/backend/aggregation.go:35) 创建独立的后台 context；[internal/backend/media_items.go:418](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_items.go:418) 仍显式持有 reqCtx | 下传身份必须使用显式 reqCtx，不能假设 context.Context 总带着原始 HTTP 身份 |

需要修改的 doRequest 生产调用点是六处：四处业务入口，加两处认证引导入口。原文所称“四个调用点”只计算了业务入口。

[internal/backend/handlers_admin.go:378](D:/UserData/Desktop/Emby-In-One-main/internal/backend/handlers_admin.go:378) 的管理面板延迟探测另用 client.Get(body.TargetURL)，不转发当前客户端请求的身份载体。本批在该处补注释，明确这是探测专用出口；未来若改为转发业务请求，必须接入公共准备层，不能依赖本次覆盖结论。

**3. 执行顺序、Git 基线与文件落点**

执行顺序：P0 基线与复现 → P1/P2/P4 第一批 → P3 独立第二批 → P5-test。P5-prod 不在本轮实施范围。阶段内的实现和对应测试一起提交，保持每个最终交付提交可构建。

| 阶段 | 主要修改位置 | 交付内容 |
|---|---|---|
| P0 | 工程根目录；[internal/backend/media_userid_forward_test.go:47](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_userid_forward_test.go:47)、[internal/backend/fallback_proxy_test.go:11](D:/UserData/Desktop/Emby-In-One-main/internal/backend/fallback_proxy_test.go:11)、[internal/backend/coverage_gaps_test.go:87](D:/UserData/Desktop/Emby-In-One-main/internal/backend/coverage_gaps_test.go:87) | 建立 Git 基线；补真实缺口复现，另列新策略测试 |
| P1 | [internal/backend/auth_context.go:11](D:/UserData/Desktop/Emby-In-One-main/internal/backend/auth_context.go:11)、[internal/backend/upstream.go:619](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:619)、[internal/backend/upstream.go:600](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:600)、[internal/backend/media_stream.go:84](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_stream.go:84) | 显式上下文、成对认证快照、源侧标识查询、公共 URL 处理、六处接线 |
| P2 | [internal/backend/identity.go:263](D:/UserData/Desktop/Emby-In-One-main/internal/backend/identity.go:263)、[internal/backend/identity.go:308](D:/UserData/Desktop/Emby-In-One-main/internal/backend/identity.go:308)、[internal/backend/fallback_proxy.go:164](D:/UserData/Desktop/Emby-In-One-main/internal/backend/fallback_proxy.go:164)、[internal/backend/session_userdata.go:269](D:/UserData/Desktop/Emby-In-One-main/internal/backend/session_userdata.go:269) | 认证头清理、JSON 当前用户字段处理、明确准备错误状态码 |
| P4（与 P1/P2 同批） | [internal/backend/upstream.go:691](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:691)、[internal/backend/media_stream.go:38](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_stream.go:38)、[internal/backend/media_stream.go:86](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_stream.go:86) | 首批同步脱敏，不等待 P3；完成三维联调与本批验收 |
| P3（独立提交） | [internal/backend/id_rewriter.go:36](D:/UserData/Desktop/Emby-In-One-main/internal/backend/id_rewriter.go:36)、[internal/backend/media.go:124](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media.go:124)、[internal/backend/media_items.go:519](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_items.go:519) | 普通用户响应身份、聚合签名接线、缓存旧别名兼容 |
| P5-test | 新 outbound_diagnostics.go / outbound_diagnostics_test.go | 纯扫描与独立 fixture 的测试侧诊断，不挂生产请求 |
| P5-prod（暂缓） | [internal/backend/server.go:66](D:/UserData/Desktop/Emby-In-One-main/internal/backend/server.go:66)、[internal/backend/upstream.go:90](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:90)、[internal/backend/upstream.go:127](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:127)、[internal/backend/upstream.go:696](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:696) | 仅保留未来接线位置，不创建开关、运行时限频状态或额外配置 |

P0 的基线操作由后续实施者执行：

1. 用 git rev-parse --show-toplevel 确认当前目录是否已属于仓库；已有仓库则沿用，不覆盖现有历史。
2. 当前目录未被仓库管理时，在本工程根目录执行 git init。先新增 .gitignore，排除实际运行配置、.env、tokens.json、captured-headers.json、数据目录、SQLite 运行数据库及 WAL/SHM、日志、构建输出和依赖缓存；配置示例、第三方源码、public 内随项目分发的资源必须保留。
3. 检查待暂存文件清单，只纳入源码、项目资源和文档；不直接把整个目录无差别提交。不要删除或移动被忽略的运行数据。
4. 提交当前含八处定点修复的源码基线；记录基线 commit ID、go build ./... 与 go test ./... -count=1 的结果。基线若已有失败，单独记录后继续能独立推进的工作，不把它写成修复引入。
5. 使用现有 Git 作者配置；缺少配置时报告缺项，不伪造提交者。复现测试的红灯结果记录在执行记录中，测试与其修复一起进入后续绿色交付提交。
6. 回退使用对应变更的 git revert；P3 可单独回退而保持 P1 的新旧 ID 兼容。不要使用清空工作区的 reset --hard 或 clean 来回退。

拟新增文件均位于 D:/UserData/Desktop/Emby-In-One-main/internal/backend/；预算是本方案的实现控制线，不是声称项目已经存在新的强制规则：

| 待新增文件 | 职责 | 建议行数 / 函数数上限 |
|---|---|---|
| upstream_auth_state.go | 带锁读取认证状态与 clientUserID() 访问器 | 80 / 6 |
| identifier_lookup.go | 源存储查询的只读组合视图、两个不同语义的谓词 | 180 / 15 |
| identifier_lookup_test.go | Alice/Bob、资源、凭据、上游归属的独立 fixture | 300 / 15 |
| outbound_identity.go | 当前用户 URL/body 归一化 | 300 / 20 |
| outbound_identity_policy.go | 当前用户、可省略、未分类规则及路径决策表 | 200 / 18 |
| outbound_errors.go | 请求准备错误类型、HTTP 状态映射 | 70 / 8 |
| outbound_identity_test.go | 最终请求、成对快照与 BuildURL 契约 | 500 / 25 |
| outbound_identity_policy_test.go | 空省略集合、未知路径与未知字段默认行为 | 300 / 20 |
| response_identity_test.go | P3 响应与旧客户端缓存兼容 | 350 / 20 |
| outbound_log.go / outbound_log_test.go | URL 脱敏与安全诊断摘要 | 实现 150 / 12；测试 250 / 15 |
| outbound_diagnostics.go / outbound_diagnostics_test.go | P5-test 纯扫描；无生产 hook | 实现 220 / 18；测试 350 / 20 |

预算不要求为了凑数拆出无意义文件；接近预算时按 URL、body、headers 等实际职责拆分。禁止将所有新增逻辑继续堆进 upstream.go。除第 5 节标为确定签名的接线外，内部函数命名可微调，但语义动作表与测试预期不得临场改变。

**4. P0：建立能够失败的回归测试**

复用已有测试设施，不连接真实上游：

| 现有设施 | 用法 |
|---|---|
| [internal/backend/server_test.go:19](D:/UserData/Desktop/Emby-In-One-main/internal/backend/server_test.go:19) withTempAppConfig | 启动临时 App；所有数据写入测试临时目录 |
| [internal/backend/server_test.go:50](D:/UserData/Desktop/Emby-In-One-main/internal/backend/server_test.go:50) doJSONRequest | JSON 接口请求；复合认证头和 raw body 测试使用 httptest.NewRequest 自建请求 |
| [internal/backend/multiuser_test.go:14](D:/UserData/Desktop/Emby-In-One-main/internal/backend/multiuser_test.go:14) loginTokenAs | 分别登录管理员和普通用户 |
| [internal/backend/multiuser_test.go:59](D:/UserData/Desktop/Emby-In-One-main/internal/backend/multiuser_test.go:59) singleUpstreamConfig | 单上游用例 |
| [internal/backend/multiuser_test.go:84](D:/UserData/Desktop/Emby-In-One-main/internal/backend/multiuser_test.go:84) dualUpstreamConfig | A/B 返回不同真实用户 ID 和 token，测试逐上游隔离 |
| [internal/backend/session_userdata_test.go:12](D:/UserData/Desktop/Emby-In-One-main/internal/backend/session_userdata_test.go:12) | 扩展已有 session 上报和 capabilities 广播测试 |
| [internal/backend/coverage_gaps_test.go:87](D:/UserData/Desktop/Emby-In-One-main/internal/backend/coverage_gaps_test.go:87) | 扩展已有视频 redirect 测试，并增加音频对应场景 |

上游 stub 通过 channel 回传请求快照，由测试主 goroutine 断言。不要在 HTTP handler goroutine 调用 t.Fatal，也不要无同步读写共享记录变量。

测试预期使用测试文件独立声明的 fixture，角色包括 alice-local、bob-local、legacy-admin、item-virtual、token-local、user-A/token-A、user-B/token-B 及未知值。这些名字是角色标签；需要匹配虚拟 ID 格式的扫描用例使用固定 32 位 hex 值或存储实际签发的值，上游 stub 可以使用 user-A 等可区分字符串。测试源存储查询时，用存储真实生成的 ID 填入 fixture。期望类别、允许别名和目标值用显式表列出，禁止调用被测 Lookup / IsCurrentUserAlias / ClassifyLocalIdentifier 来生成 want。

测试分两类记录：

- 缺陷复现：普通用户自身份的兜底读取路径、兜底 query、session body、带 UserId/Token 的 passthrough 复合头、redirect UserId、P3 当前用户响应不一致。这些用例应在当前基线上显示错误内容；复合头泄漏与响应不一致是请求/响应契约证据，不声称已经复现所有客户端的上游 502。
- 新行为契约：字段重复/大小写规则、准备错误状态码、未知语义透传、空省略集合、旧凭据清理和诊断分类。这些是本方案的设计选择，不能把旧实现没有这些行为包装成已观测事故。

已有 PlaybackInfo / Views 两个测试继续通过。不要为了证明测试有效而临时删除已修代码；第一批测试不要求尚未交付的 P3 变为绿色，P3 用例随其独立提交落地。

**5. P1：请求准备层与 URL 统一处理**

5.1 显式传入身份，并冻结一次认证状态。

在 [internal/backend/auth_context.go:11](D:/UserData/Desktop/Emby-In-One-main/internal/backend/auth_context.go:11) 的 RequestContext 增加 LegacyProxyUserID 字段；[internal/backend/auth_context.go:17](D:/UserData/Desktop/Emby-In-One-main/internal/backend/auth_context.go:17) 的 withContext 从 a.Auth.ProxyUserID() 填入，用于兼容旧响应中发给普通用户的全局虚拟用户 ID。当前用户仍以 ProxyUser.UserID 为准，不能由 query 或 body 决定。该上下文在一次请求及其后台任务中只读使用。

在 upstream_auth_state.go 放认证状态读取，在 outbound_identity.go 放 URL/body 准备；最终认证头构建助手放 identity.go 的同类逻辑旁。契约如下：

    type upstreamAuthSnapshot struct {
        UserID      string
        AccessToken string
    }

    func (c *UpstreamClient) authSnapshot() upstreamAuthSnapshot

    // input、headers 与 body 属于调用方；返回值属于本次出站请求。
    func prepareOutboundURL(
        input *url.URL,
        reqCtx *RequestContext,
        auth upstreamAuthSnapshot,
        policy outboundIdentityPolicy,
    ) (*url.URL, error)

    func prepareOutboundBody(
        body any,
        reqCtx *RequestContext,
        auth upstreamAuthSnapshot,
        policy outboundIdentityPolicy,
    ) (any, error)

    func prepareOutboundHeaders(
        headers http.Header,
        auth upstreamAuthSnapshot,
        mode outboundAuthMode, // normal / passwordLogin / apiKeyValidation
    ) http.Header

clientUserID() 仅供 handler 的带锁单字段读取；一个出站请求中需要成对身份时必须使用一次 authSnapshot，不能分别调用两个访问器再拼对。

authSnapshot 在同一个 c.mu.RLock 下读取 UserID 与 AccessToken。依据是 [internal/backend/upstream.go:536](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:536) 的 setOnline 同时写入这两个字段。

doRequest 签名改为：

    doRequest(ctx context.Context, reqCtx *RequestContext,
        method, path string, params url.Values, body any,
        headers http.Header, stream bool) (*http.Response, error)

六个调用点逐项更新：

| 当前行号 | 修改 |
|---|---|
| [internal/backend/upstream.go:447](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:447) loginWithHeaders | 传 reqCtx；AuthenticateByName 按引导请求处理 |
| [internal/backend/upstream.go:498](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:498) validateAPIKey | 传 reqCtx；Users/Me 允许 UserID 尚为空，保留配置中的上游 API key |
| [internal/backend/upstream.go:553](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:553) RequestJSON | 将已有 reqCtx 显式传入；不能从后台 ctx 重新提取 |
| [internal/backend/upstream.go:578](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:578) Stream | 将已有 reqCtx 显式传入 |
| [internal/backend/fallback_proxy.go:188](D:/UserData/Desktop/Emby-In-One-main/internal/backend/fallback_proxy.go:188) performUpstreamRequest | 从 r.Context() 取 reqCtx 并传入 |
| [internal/backend/session_userdata.go:220](D:/UserData/Desktop/Emby-In-One-main/internal/backend/session_userdata.go:220) performUpstream | 从 HTTP request 的 Context 取 reqCtx 并传入 |

此处还需搜索测试中的直接调用并更新参数，避免通过编译遗漏接线。当前未发现生产代码之外的额外直接调用，但实施时仍以搜索结果为准。

5.1a 标识查询放在签发/登记源一侧，消费者不再分别扫描数据源。

| 源与当前定位 | 本批增加/复用的只读入口 |
|---|---|
| [internal/backend/idstore.go:182](D:/UserData/Desktop/Emby-In-One-main/internal/backend/idstore.go:182)、[internal/backend/idstore.go:240](D:/UserData/Desktop/Emby-In-One-main/internal/backend/idstore.go:240) | ContainsVirtualID(value)：在源存储内部持 RLock 查询 map，不复制 ResolvedID，不逐请求遍历所有映射 |
| [internal/backend/auth_manager.go:21](D:/UserData/Desktop/Emby-In-One-main/internal/backend/auth_manager.go:21)、[internal/backend/auth_manager.go:310](D:/UserData/Desktop/Emby-In-One-main/internal/backend/auth_manager.go:310) | ProxyUserID()、HasIssuedToken(value)：后者在 AuthManager 内查现有 tokens，不用会执行认证逻辑的 ValidateToken 代替成员查询 |
| [internal/backend/user_store.go:115](D:/UserData/Desktop/Emby-In-One-main/internal/backend/user_store.go:115)、[internal/backend/user_store.go:175](D:/UserData/Desktop/Emby-In-One-main/internal/backend/user_store.go:175) | ContainsUserID(value)：在 UserStore 内查询现有 users；不把 List() 全表扫描复制到 policy 或 D |
| [internal/backend/upstream.go:288](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:288)、[internal/backend/upstream.go:536](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:536) | 通过带锁认证快照取得各上游真实用户 ID 与 serverIndex；当前目标的快照以本次出站 authSnapshot 为准 |

在 identifier_lookup.go 提供只读 IdentifierLookup，组合这些源查询并返回结构化事实：是否本地用户、是否本地 token、是否虚拟资源，以及哪些上游具有该真实用户 ID。它不保存另一套持久化登记表，不改变现有签发入口。

withContext 建立只读 RequestContext.Identifiers 查询视图，供显式 reqCtx 下传；视图的源入口可共享，不能把“当前目标上游”写回共享 reqCtx，因为多上游会并发使用它。尚未完成认证的 bootstrap 或公开图片路径允许视图为空；已支持当前用户接口仍可凭显式 policy 和上游快照完成处理。

两个消费者谓词都调用这个查询视图，禁止另写一份源扫描：

    IsCurrentUserAlias(value, reqCtx, targetAuth, lookup) bool
    ClassifyLocalIdentifier(value, reqCtx, targetAuth, lookup) IdentifierClass

第一项只允许本次可信 ProxyUser.UserID、LegacyProxyUserID 与本次目标上游 UserID；本次请求已冻结的可信别名可直接作为查询事实的一部分，即使存储在请求期间发生变化也不改写成另一个用户。Bob 的本地 ID 不会因为“登记过”就变成 Alice 的当前用户别名。

第二项区分 local-user、local-token、virtual-resource、target-upstream、foreign-upstream 和 unknown。target-upstream 是合法目标值，不能报警为本地泄漏；foreign-upstream 指值仅匹配其他上游而不匹配当前目标。若两台上游真实 UserID 字符串相同，应优先判当前目标匹配，不能只凭字符串判错服。

本次 authSnapshot 是目标认证的权威值，其他上游快照只提供诊断线索，不用它决定路由或授权。未知接口中识别到 foreign-upstream 也不会因此自动改写；已支持当前用户接口不依赖别名匹配，会归一化到目标 authSnapshot。

以上共享的是事实查询，两个谓词的语义和允许集合仍不同。新签发来源未接入查询仍可能漏检，查询入口统一不能证明未来集合完备。

5.2 doRequest 内部的确定顺序。

| 当前位置 | 修改要求 |
|---|---|
| [internal/backend/upstream.go:619](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:619) | 获取本次 authSnapshot，确定接口 policy |
| [internal/backend/upstream.go:630](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:630) | 解析 URL；保留 BaseURL 中已有的路径前缀 |
| [internal/backend/upstream.go:634](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:634) | 先合并 URL 自带 query 与 params，再调用 prepareOutboundURL |
| [internal/backend/upstream.go:644](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:644) | 先调用 prepareOutboundBody，再进行现有 JSON 编码或 raw body 序列化 |
| [internal/backend/upstream.go:666](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:666) | clone headers 后，以同一次 authSnapshot 完成 prepareOutboundHeaders；不要再单独读取最新 token |
| [internal/backend/upstream.go:662](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:662)、[internal/backend/upstream.go:681](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:681) | 用最终 URL、body、headers 构造请求；按上述顺序整理原有代码块 |
| [internal/backend/upstream.go:691](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:691) | 打印脱敏的最终目标与发生过改写的载体 |
| [internal/backend/upstream.go:696](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:696) | 发送；P5-test 不挂钩子，未来 P5-prod 的候选检查点在发送之前 |

已知当前用户接口中，调用方提前写入的真实 UserId 也归一化到本次快照，防止早先读取的用户 ID 与稍后取得的 token 混用。

全库运行时 UpstreamClient.UserID 读取改用 clientUserID() 或成对 authSnapshot，不局限于“本批碰巧触及”的行。最终归一化可以修正线上的值，但不能消除之前发生的数据竞争。带锁内部字段访问保留，禁止在已持 c.mu 的函数里再次调用访问器造成重入死锁。

| 必查文件与当前读取锚点 | 动作 |
|---|---|
| [internal/backend/media_items.go:93](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_items.go:93)、[internal/backend/media_items.go:153](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_items.go:153)、[internal/backend/media_items.go:154](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_items.go:154)、[internal/backend/media_items.go:180](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_items.go:180)、[internal/backend/media_items.go:181](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_items.go:181)、[internal/backend/media_items.go:217](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_items.go:217)、[internal/backend/media_items.go:218](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_items.go:218)、[internal/backend/media_items.go:251](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_items.go:251)、[internal/backend/media_items.go:252](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_items.go:252)、[internal/backend/media_items.go:354](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_items.go:354)、[internal/backend/media_items.go:380](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_items.go:380)、[internal/backend/media_items.go:410](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_items.go:410)、[internal/backend/media_items.go:418](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_items.go:418) | query 与拼接路径里的字段读取均替换 |
| [internal/backend/handlers_user.go:130](D:/UserData/Desktop/Emby-In-One-main/internal/backend/handlers_user.go:130)、[internal/backend/handlers_user.go:131](D:/UserData/Desktop/Emby-In-One-main/internal/backend/handlers_user.go:131)、[internal/backend/library_image.go:97](D:/UserData/Desktop/Emby-In-One-main/internal/backend/library_image.go:97)、[internal/backend/library_image.go:133](D:/UserData/Desktop/Emby-In-One-main/internal/backend/library_image.go:133)、[internal/backend/library_image.go:219](D:/UserData/Desktop/Emby-In-One-main/internal/backend/library_image.go:219)、[internal/backend/library_image.go:321](D:/UserData/Desktop/Emby-In-One-main/internal/backend/library_image.go:321) | 同上 |
| [internal/backend/media_nextup.go:163](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_nextup.go:163)、[internal/backend/media_nextup.go:202](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_nextup.go:202)、[internal/backend/media_playback.go:68](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_playback.go:68)、[internal/backend/media_playback.go:70](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_playback.go:70)、[internal/backend/media_resume.go:35](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_resume.go:35)、[internal/backend/media_resume.go:38](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_resume.go:38) | 同上，保留原定点修复行为 |
| [internal/backend/session_userdata.go:161](D:/UserData/Desktop/Emby-In-One-main/internal/backend/session_userdata.go:161)、[internal/backend/session_userdata.go:422](D:/UserData/Desktop/Emby-In-One-main/internal/backend/session_userdata.go:422)、[internal/backend/session_userdata.go:454](D:/UserData/Desktop/Emby-In-One-main/internal/backend/session_userdata.go:454)、[internal/backend/session_userdata.go:488](D:/UserData/Desktop/Emby-In-One-main/internal/backend/session_userdata.go:488)、[internal/backend/session_userdata.go:527](D:/UserData/Desktop/Emby-In-One-main/internal/backend/session_userdata.go:527) | 同上 |
| [internal/backend/fallback_proxy.go:37](D:/UserData/Desktop/Emby-In-One-main/internal/backend/fallback_proxy.go:37) | 旧补丁删除后，该处直接读取同时消失 |
| [internal/backend/upstream.go:144](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:144)、[internal/backend/upstream.go:375](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:375)、[internal/backend/upstream.go:396](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:396)、[internal/backend/upstream.go:536](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:536) | Reload、snapshot、IsOnline、setOnline 的锁内访问保留并列为审计例外 |

实施时用全库搜索核对别名变量下的读取；测试读离线快照、tokenInfo.UserID 和 UserStore 的用户字段不能误改成 UpstreamClient 访问器。race 用例须并发执行 setOnline 与出站准备；是否检测到竞争由运行结果决定，不提前声称基线必然 race 失败。

5.3 URL 的具体规则与路径动作表。

已支持当前用户 query：遍历所有键，对 EqualFold(key, "UserId") 的变体统一处理。字段缺省则不新增；字段存在但值为空、重复或大小写不同，删除这些变体并写一个规范 UserId=auth.UserID。处理的是最终合并 query，不能只修改 params。需要归一化而 auth.UserID 为空时返回 503 准备错误，不发出残留本地身份。

路径只识别业务 path 的完整 /Users/{id} 用户段，不在整条字符串里 ReplaceAll。业务 path 由接口识别与 BaseURL 部署前缀分别处理；维护 Path / RawPath，不能双重编码或改坏其他段。原有资源解析、权限检查和多上游歧义拒绝仍先执行，以下“透传”指身份层不额外改变已经路由准备的用户段，不撤销已有资源路由行为。

| 路径状态 | 固定动作 |
|---|---|
| Users/AuthenticateByName、Users/Me、Users/Public、Users/New 等明确静态路由 | 保持静态段；登录与 API key 走其各自引导规则 |
| 已支持当前用户接口的 /Users/{id}/... | 用户段归一化为本次 auth.UserID，不由传入值选上游 |
| 未分类兜底路径，用户段是 IsCurrentUserAlias 明确识别的本次用户/legacy 别名 | 使用精确段替换保持当前用户别名兼容；标记 legacy/self-alias-compat，不宣称该未知接口其他字段已适配 |
| 未分类 /Users/{未知值}/...，包括其他本地用户 ID 或 foreign-upstream 值 | 用户段原样透传，记录 unclassified 或 foreign-upstream；不删除、不按长度猜测、不新增 400 |
| 无法构造合法 URL 或缺少已支持映射所必需的上游认证状态 | 走第 6.3 节 400/503 准备错误；这不是“未知值一律拒绝” |

路径没有“删除 UserId 字段”的分支。legacy 别名仅代表当前已认证本地用户，不赋予管理员权限、不作为资源路由依据。未知值的透传保持兼容性，也意味着此范围仍可能发生上游拒绝或身份语义错误；在变更说明列明，不写成全量安全。

5.4 三分类字段策略：已确认当前用户 / 已验证省略等价 / 未分类。

下表针对 query 与可解析 JSON body 的具体字段。认证头中的客户端凭据始终按 P2 清理，不因业务接口未分类而保留；路径始终使用第 5.3 节的独立动作表。

| 分类 | 首批成员与动作 |
|---|---|
| 已确认当前用户 | 按下列接口规则将声明的 UserId 映射到本次上游身份。缺省不新增；保留已有 PlaybackInfo 等定点设置行为 |
| 已验证省略等价 | 首批集合明确为空。PlaybackInfo、Views 已归入第一类，不再重复放入删除类。不要为填充此集合临时降低证据标准 |
| 未分类 | 不自动删除、注入或拒绝 query/body 的 UserId；保留原值并记录 unclassified。历史兼容例外仅见下文“兜底读取中的精确自身份别名” |

未来向“省略等价”集合新增成员，必须同时记录方法、精确路由/字段、上游版本、认证方式、载体与输入形态，并提供“显式身份 vs 省略身份”的响应语义和副作用等价测试。仅两边都返回 200 不够；不能将某接口的 query 结论推广到另一个接口或 body。首批不要额外实现通用字段删除框架，使用空表/空规则和一个禁止误删除的契约测试即可。

| 首批当前用户规则 / 兼容例外 | 适用路径与处理 |
|---|---|
| 媒体读取 | GET/HEAD 的 Items、Shows/NextUp、Shows/{id}/Seasons、Shows/{id}/Episodes、Library 已注册读取接口、Genres/MusicGenres/Studios/Persons/Artists、Search/Hints；只处理声明的 UserId 参数 |
| 当前用户路径 | GET/HEAD /Users/{id}/Items 及其媒体读取子接口、Views；用户段按第 5.3 节处理 |
| 播放与进度 | GET/POST /Items/{id}/PlaybackInfo；POST /Sessions/Playing、Progress、Stopped、Capabilities、Capabilities/Full 的顶层 UserId |
| 用户媒体状态 | 已注册 /Users/{id}/Items/{itemId}/UserData、FavoriteItems/{itemId}、PlayingItems/{itemId}，只覆盖现有方法与声明字段 |
| 流与图片 | GET/HEAD Videos/Audio 流及 Items 图片 query 的 UserId；保留现有公开图片认证约定，不能因为 reqCtx 新字段要求所有图片登录 |
| 认证引导 | AuthenticateByName 不带旧 token；Users/Me 使用配置上游 API key，不要求预先已有 UserID |
| 兜底读取中的精确自身份别名 | 未分类 GET/HEAD 的 query.UserId 仅当所有出现值均明确匹配本次用户/legacy/目标上游别名时，归一化为目标用户，标记 self-alias-compat；缺省、未知、其他用户、foreign-upstream、混合已知/未知值保持原值并标记 unclassified |
| 未知写接口或任意授权目标字段 | query/body 原样保留并标记 unclassified；不把登记过的其他用户当成当前用户，也不因为出现已知本地 ID 就新增 400。路径独立使用第 5.3 节 |

精确自身份兼容例外是为覆盖已发现的普通用户兜底读取缺口，不能扩展为“所有本地 ID 都可以改成当前用户”。未分类 JSON body 即使值等于本地 ID，也不凭数值匹配猜测它是当前用户还是授权目标。

现有本地用户配置/策略接口由 [internal/backend/routes.go:19](D:/UserData/Desktop/Emby-In-One-main/internal/backend/routes.go:19)、[internal/backend/routes.go:20](D:/UserData/Desktop/Emby-In-One-main/internal/backend/routes.go:20) 接管，继续本地处理。全局代理入口的既有权限验证和 fallback 歧义判定仍先执行，参考 [internal/backend/fallback_proxy.go:102](D:/UserData/Desktop/Emby-In-One-main/internal/backend/fallback_proxy.go:102)。本轮不放宽现有权限校验，也不为未知业务新增批量拒绝策略。

“未分类保持现状”是显式残留边界。执行者不得临场将它改成删除或 400；日志须区分 supported、self-alias-compat 与 unclassified。即使识别出 foreign-upstream，也只有已确认当前用户的接口才能据目标快照覆盖它。

5.5 BuildURL 与 redirect 共用 URL 准备函数。

将 [internal/backend/upstream.go:600](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:600) 改为接收 reqCtx 并返回 (string, error)，内部也获取认证快照、合并 query、调用 prepareOutboundURL。流 URL 中 api_key 的大小写变体统一删除后，由该次快照写入上游 token，避免 query token 与 UserId 分别取自两次认证状态。

| 当前位置 | 修改 |
|---|---|
| [internal/backend/media_stream.go:68](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_stream.go:68) | token 替换职责移到公共 URL 准备层；业务代码只负责选好目标和解析媒体 ID |
| [internal/backend/media_stream.go:84](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_stream.go:84) | 传 reqCtx、处理 BuildURL 错误；只在 URL 完成准备后发 302 |
| [internal/backend/media_stream.go:124](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_stream.go:124) | HLS 基准 URL 同步采用新签名，准备失败时不把空 URL 交给 RewriteM3U8ForItem |
| [internal/backend/fallback_proxy.go:37](D:/UserData/Desktop/Emby-In-One-main/internal/backend/fallback_proxy.go:37) | 删除旧的全局 proxyUserID 字符串替换补丁，由公共用户段处理覆盖 |

doRequest 的流请求与 BuildURL 对 api_key 使用同一规则；普通 API 请求移除客户端 api_key 变体，以最终上游认证头认证。合并结果中 api_key（大小写变体）无论来自 params 还是 BaseURL，都按该规则处理；BaseURL 的其他 query 保留，不在本轮猜测其业务语义。不能把 EIO 的本地 token 带入上游认证参数。

HLS 基准 URL 优先使用成功上游响应的最终 Request.URL；测试手工构造的 response 没有 Request 时再调用 BuildURL。这样无需在收到响应后重新读取认证状态来重建原请求。

**6. P2：认证头、JSON body 与请求准备错误**

6.1 复合认证头。

清理分支的输入是否存在必须按实际请求判断：有的客户端会发送含 Token/UserId 的 Emby 或 MediaBrowser 复合头，有的只在独立 X-Emby-Token 携带 EIO token。不能宣称 passthrough 每次都同时携带两个复合字段，也不能按客户端品牌推断请求形态。

| 位置 | 修改要求 |
|---|---|
| [internal/backend/identity.go:308](D:/UserData/Desktop/Emby-In-One-main/internal/backend/identity.go:308) normalizeCapturedHeaders | 先从复合头提取 Client/Version/Device/DeviceId，再移除其中的 UserId、Token 和原始 Authorization 凭据；持久化只保留设备/客户端信息 |
| [internal/backend/identity.go:263](D:/UserData/Desktop/Emby-In-One-main/internal/backend/identity.go:263) mergePassthroughHeaders | live、captured-token、last-success、latest 等所有来源均执行相同清理；不能只修新登录 |
| [internal/backend/identity.go:281](D:/UserData/Desktop/Emby-In-One-main/internal/backend/identity.go:281) parseAuthorizationIdentity | 仅从客户端/设备字段白名单构建新头；当前 strings.Split 解析器不能作为任意带引号参数的安全清洗器，新增含逗号、引号与转义的解析用例 |
| [internal/backend/identity.go:235](D:/UserData/Desktop/Emby-In-One-main/internal/backend/identity.go:235) hydrateCapturedEntry | 旧持久化记录加载后走同一清理；无需改变磁盘 schema |
| [internal/backend/upstream.go:446](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:446) 登录头构建 | 复用统一的设备身份头构造；用户名密码登录不复用客户端 Token 或上次上游 token |
| [internal/backend/upstream.go:666](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:666) 最终出站头 | 删除客户端 Authorization、X-Emby-Authorization、X-Emby-Token、X-MediaBrowser-Token 残留；重建一个规范 X-Emby-Authorization，UserId 来自本次快照，认证 token 仅由上游快照设置 |

保留 User-Agent、Client、Version、Device、DeviceId、Accept、Accept-Language 等客户端行为信息。设备信息解析以显式独立头优先，避免把有效客户端错误退回默认 Infuse。quoted-string 的构造需正确转义，不能把未转义值直接 fmt.Sprintf 进复合头。

对用户名密码登录，UserId 为空且不发送旧 token；对 API key 验证，保留本次配置的上游 key，UserId 可为空。普通请求的头、query、body、path 使用同一认证快照。

6.2 JSON body。

先覆盖下列已有入口：

| 位置 | 修改要求 |
|---|---|
| [internal/backend/media_playback.go:67](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_playback.go:67) | 保留逐 instance 克隆和已做的 query/body 修复，新增收口仍需保证幂等 |
| [internal/backend/session_userdata.go:46](D:/UserData/Desktop/Emby-In-One-main/internal/backend/session_userdata.go:46) | 继续只负责资源 ID 与目标服务器推断；不要在目标选定前猜 UserId |
| [internal/backend/session_userdata.go:295](D:/UserData/Desktop/Emby-In-One-main/internal/backend/session_userdata.go:295)、[internal/backend/session_userdata.go:335](D:/UserData/Desktop/Emby-In-One-main/internal/backend/session_userdata.go:335)、[internal/backend/session_userdata.go:371](D:/UserData/Desktop/Emby-In-One-main/internal/backend/session_userdata.go:371) | 三个进度事件最终由 prepareOutboundBody 设置现有 UserId 字段 |
| [internal/backend/session_userdata.go:386](D:/UserData/Desktop/Emby-In-One-main/internal/backend/session_userdata.go:386)、[internal/backend/session_userdata.go:397](D:/UserData/Desktop/Emby-In-One-main/internal/backend/session_userdata.go:397) | capabilities 广播逐上游生成副本，不修改共享 body |
| [internal/backend/session_userdata.go:427](D:/UserData/Desktop/Emby-In-One-main/internal/backend/session_userdata.go:427)、[internal/backend/session_userdata.go:473](D:/UserData/Desktop/Emby-In-One-main/internal/backend/session_userdata.go:473)、[internal/backend/session_userdata.go:512](D:/UserData/Desktop/Emby-In-One-main/internal/backend/session_userdata.go:512) | UserData / 收藏等明确当前用户的 JSON 适配 |
| [internal/backend/fallback_proxy.go:177](D:/UserData/Desktop/Emby-In-One-main/internal/backend/fallback_proxy.go:177) | Content-Type 为 JSON 时走结构化路径；未知类型不盲目字符串替换 |

prepareOutboundBody 首批只修改声明为“当前用户”的顶层 UserId（含大小写变体），不递归覆盖任意子对象的用户字段。字段缺省保持缺省；字段存在时生成 map 副本并使用 auth.UserID。只修改顶层时浅复制足够，嵌套值不修改；若某接口未来要改嵌套字段，只复制被修改的分支。

不要直接复用 [internal/backend/media_playback.go:201](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_playback.go:201) 的 deepCloneMap 作为通用 body 拷贝器：它忽略 JSON 编解码错误，会改变部分 Go 数值类型。新助手必须显式返回序列化错误。非 map 的 JSON 根对象、数组、null 不强行转成对象；需要 UserId 的接口若不接受该形态，由对应接口验证处理。

6.3 raw body 和错误处理。

首批继续保证 rawRequestBody 的 Content-Type 与原始字节语义。[internal/backend/fallback_proxy.go:173](D:/UserData/Desktop/Emby-In-One-main/internal/backend/fallback_proxy.go:173) 当前会在判断格式之前 TrimSpace；应改为只用裁剪结果检测“是否为空”，非 JSON raw 返回原始字节，避免改坏二进制或签名内容。

fallback 中 JSON 声明但格式错误沿用现有 400；session 的解码失败状态继续遵循其原约定，见下表。非 JSON 数据不通过“看起来像 JSON”自动改写；表单/XML 等另按 Content-Type 增加显式适配后才能纳入支持范围。

在 outbound_errors.go 新增 outboundPreparationError，供请求准备层返回，接口层用一个公共助手映射状态码。不能因为未分类 UserId 或未知路径用户段而生成此错误；第 5.3/5.4 节的默认动作是透传与标记。

| 场景 | 本批固定行为 |
|---|---|
| 客户端输入导致已支持请求无法构造合法 URL、或无法按已声明形态准备 | 准备错误 400；消息只给类别与字段名，不输出凭据、原 body、完整 URL。上游配置导致的 URL 构造故障仍按上游故障 502 处理，不能归咎于客户端 |
| normal 请求需要当前用户归一化，但上游认证快照缺少 UserID/token | 准备错误 503；不发送混合/残留身份；按下面的恢复规则处理 |
| 未分类 query/body/path 的用户值 | 保留原行为，不新增 400/503；如果原有路由/权限检查拒绝，仍保留其状态 |
| 用户名密码登录、API key 验证、健康检查中的引导登录 | 走独立 outboundAuthMode，不能因预期为空的 UserID 被 normal 规则拦截 |
| 原有 JSON 解码失败或请求 body 超限 | 保持现有各接口约定；本批不顺带统一所有 session 的输入协议 |

以下 session 状态码变化必须写入变更说明，并用表驱动测试固定：

| 接口 / 事件 | 准备错误 | 网络/上游错误 | 原有本地状态与停止清理 |
|---|---|---|---|
| [internal/backend/session_userdata.go:295](D:/UserData/Desktop/Emby-In-One-main/internal/backend/session_userdata.go:295) Playing | 新增明确 400 或 503 | 保持当前记录后 204 的约定 | 保持本地进度与 limiter 原语义 |
| [internal/backend/session_userdata.go:335](D:/UserData/Desktop/Emby-In-One-main/internal/backend/session_userdata.go:335) Progress | 从吞错误的 204 改为 400 或 503，仅限上述准备错误 | 保持 204 | 合法进度继续写本地，不能因无法上报而改为上游用户归属 |
| [internal/backend/session_userdata.go:371](D:/UserData/Desktop/Emby-In-One-main/internal/backend/session_userdata.go:371) Stopped | 从吞错误的 204 改为 400 或 503，仅限上述准备错误 | 保持 204 | 使用 defer/统一出口保证已有停止清理继续运行，避免提前 return 留下并发名额 |
| [internal/backend/session_userdata.go:386](D:/UserData/Desktop/Emby-In-One-main/internal/backend/session_userdata.go:386)、[internal/backend/session_userdata.go:397](D:/UserData/Desktop/Emby-In-One-main/internal/backend/session_userdata.go:397) Capabilities 广播 | 遇到准备错误记录并返回其明确状态；不把所有准备都失败伪装成成功 | 保持现有尽力广播后的 204 | 准备错误后不重放已发送的写操作 |

fallback、流入口及其他已支持 body 入口同样用公共错误映射助手，避免准备错误被错误包成网络 502。非法输入尚未形成有效播放事件时，不凭空记录进度；对已完成解码与资源/权限检查的事件，本地记录和停止清理保持上表约定。不要改动其他既有 204 分支来配合新测试。

6.4 认证恢复链路的边界。

[internal/backend/upstream.go:706](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:706) 当前仅在收到业务响应 401/403 时调 onAuthError。新增“发送前返回”的正常缺认证状态错误没有上游响应，不能依赖这条路径自行触发恢复。

- 对 normal 模式缺少必要认证状态的 503：若 c.onAuthError 非空，调度既有恢复回调，复用 [internal/backend/upstream.go:162](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:162) 的去抖、离线标记及 passthrough 身份可用性检查；不在请求 goroutine 同步 Login，不递归重发原写请求。
- 对用户输入导致的 400 与 unclassified 值：不触发认证重连。
- 引导请求失败维持 loginWithHeaders/validateAPIKey 原有处理；不要再由准备层循环触发自身登录。
- [internal/backend/healthcheck.go:50](D:/UserData/Desktop/Emby-In-One-main/internal/backend/healthcheck.go:50) 的离线检测与 [internal/backend/healthcheck.go:76](D:/UserData/Desktop/Emby-In-One-main/internal/backend/healthcheck.go:76) 的登录重试、[internal/backend/upstream.go:319](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:319) 的手动重连、收到 401/403 的旧回调都保留。
- 新增确定性测试验证缺认证状态仅进入既有去抖恢复、密码登录/API key 初始验证不被阻断、健康周期能恢复离线客户端、输入错误不造成重连风暴。使用测试替身/通知通道验证，不靠长时间 sleep 等计时器。

**7. P3：普通用户响应身份（独立第二批）**

这是已有代码可证明的当前用户身份不一致；具体客户端是否出现过滤/缓存/校验失败尚未全部实测。交付说明标为“一致性修复，普通用户响应身份值发生变化”，不能写成所有客户端均已复现故障，也不以短时间观察不到症状为由否定不一致。

P1 先上线兼容当前用户 ID 与 legacy 全局别名，P3 再切换响应；可以同一开发周期提交，但必须独立变更单元，且 P3 不阻塞第一批发布。第二批联调同时验证新登录与缓存旧 UserId 的请求，不能要求用户清缓存才能通过。

新增 a.clientFacingUserID(reqCtx)：存在已认证 ProxyUser 时返回 ProxyUser.UserID；仅无用户的公开/内部路径回退到 a.Auth.ProxyUserID()。不要用请求 path/query 里的 UserId 作为响应身份。

已明确为当前用户媒体/状态响应的调用，把 rewriteResponseIDs 的最后一个参数改为该值。低层函数仍负责资源 ID 的一次改写，不要在最终 writeJSON 前再对整棵树重复执行资源改写。

| 当前调用位置 | 落地方式 |
|---|---|
| [internal/backend/handlers_user.go:138](D:/UserData/Desktop/Emby-In-One-main/internal/backend/handlers_user.go:138) | Views 使用 reqCtx 的用户 ID |
| [internal/backend/media_playback.go:193](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_playback.go:193) | PlaybackInfo 使用本次请求用户 ID |
| [internal/backend/media_items.go:224](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_items.go:224)、[internal/backend/media_items.go:318](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_items.go:318)、[internal/backend/media_items.go:339](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_items.go:339)、[internal/backend/media_items.go:361](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_items.go:361)、[internal/backend/media_items.go:387](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_items.go:387) | 单项、多实例详情、Similar、ThemeMedia 的当前用户字段一致 |
| [internal/backend/library_image.go:58](D:/UserData/Desktop/Emby-In-One-main/internal/backend/library_image.go:58)、[internal/backend/library_image.go:82](D:/UserData/Desktop/Emby-In-One-main/internal/backend/library_image.go:82)、[internal/backend/library_image.go:111](D:/UserData/Desktop/Emby-In-One-main/internal/backend/library_image.go:111) | 已适配媒体库/分类读取响应传入当前用户 ID |
| [internal/backend/library_image.go:186](D:/UserData/Desktop/Emby-In-One-main/internal/backend/library_image.go:186)、[internal/backend/library_image.go:194](D:/UserData/Desktop/Emby-In-One-main/internal/backend/library_image.go:194)、[internal/backend/library_image.go:296](D:/UserData/Desktop/Emby-In-One-main/internal/backend/library_image.go:296)、[internal/backend/library_image.go:304](D:/UserData/Desktop/Emby-In-One-main/internal/backend/library_image.go:304) | Seasons/Episodes 合并前后两种分支均覆盖，保留原去重与本地 UserData 覆盖顺序 |
| [internal/backend/media_resume.go:152](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_resume.go:152)、[internal/backend/media_nextup.go:82](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_nextup.go:82)、[internal/backend/media_nextup.go:114](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_nextup.go:114) | 本地 Resume/NextUp 补全响应使用普通用户 ID |
| [internal/backend/session_userdata.go:469](D:/UserData/Desktop/Emby-In-One-main/internal/backend/session_userdata.go:469)、[internal/backend/session_userdata.go:508](D:/UserData/Desktop/Emby-In-One-main/internal/backend/session_userdata.go:508)、[internal/backend/session_userdata.go:547](D:/UserData/Desktop/Emby-In-One-main/internal/backend/session_userdata.go:547) | UserData / Favorite 响应使用请求身份 |
| [internal/backend/fallback_proxy.go:94](D:/UserData/Desktop/Emby-In-One-main/internal/backend/fallback_proxy.go:94) | 已确定当前用户语义的 fallback 响应使用请求身份；未知用户列表/授权对象不做“全部改成当前用户”的扩展 |

公共聚合函数要改签名，否则只修外层 handler 仍会回到全局管理员 ID：

    rewriteItems(items, serverIndex, clientUserID)
    mergedItemsPayload(results, clientUserID)
    mergeRoundRobinItems(results, clientUserID)

其中 items/results 类型保持原有定义，clientUserID 为显式 string 参数，不用全局变量保存“当前用户”。

| 定义与内部修改 | 需要接线的调用点 |
|---|---|
| [internal/backend/media.go:124](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media.go:124) rewriteItems | [internal/backend/media_items.go:98](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_items.go:98)、[internal/backend/media_items.go:160](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_items.go:160)、[internal/backend/media_items.go:186](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_items.go:186)、[internal/backend/media_resume.go:44](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_resume.go:44)、[internal/backend/media_nextup.go:41](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_nextup.go:41) |
| [internal/backend/media_items.go:519](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_items.go:519) mergedItemsPayload | [internal/backend/media_items.go:57](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_items.go:57)、[internal/backend/media_items.go:115](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_items.go:115)、[internal/backend/media_resume.go:53](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_resume.go:53)、[internal/backend/media_nextup.go:50](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_nextup.go:50)、测试 [internal/backend/parity_fix_test.go:254](D:/UserData/Desktop/Emby-In-One-main/internal/backend/parity_fix_test.go:254) |
| [internal/backend/media_items.go:532](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_items.go:532) mergeRoundRobinItems | 内部三处改写 [internal/backend/media_items.go:558](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_items.go:558)、[internal/backend/media_items.go:568](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_items.go:568)、[internal/backend/media_items.go:580](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_items.go:580)；另接线 [internal/backend/library_image.go:343](D:/UserData/Desktop/Emby-In-One-main/internal/backend/library_image.go:343) 与 mergedItemsPayload |

[internal/backend/aggregation.go:105](D:/UserData/Desktop/Emby-In-One-main/internal/backend/aggregation.go:105) 的 registerBackgroundIDs 仅登记迟到结果，不生成客户端响应。本批可保留它使用全局值，但必须注释其用途，并保留为“生产代码中有意的例外”。[internal/backend/handlers_user.go:79](D:/UserData/Desktop/Emby-In-One-main/internal/backend/handlers_user.go:79) 的公开用户列表和 AuthManager 的管理员用户对象同样不能机械替换成当前用户。

响应改写器本身仍有按字段名推断语义的历史限制；未知接口中的多用户实体，需要后续独立适配，不宣称通过替换最后一个参数就解决全部响应语义。

**8. P4：日志与第一批验收**

8.1 日志修改。

新增 formatOutboundURLForLog 等助手。使用最终 URL 副本生成日志，不修改实际请求。只显示必要的 ID/路由查询字段，其余值隐藏；api_key、ApiKey、Token、AccessToken、Authorization、密码相关字段按大小写无关规则隐藏，URL 的 userinfo 和 fragment 不输出。

| 当前位置 | 修改 |
|---|---|
| [internal/backend/upstream.go:691](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:691) | 输出最终 path、安全 query、目标 serverIndex、stream 标记，以及 changed=path/query/body/auth 等载体摘要 |
| [internal/backend/upstream.go:699](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:699) | 网络错误可能自带带 token 的 URL，输出前清理；不能把原始 URL 错误直接写日志 |
| [internal/backend/media_stream.go:38](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_stream.go:38) | 当前原样打印客户端 query，改用同一脱敏助手 |
| [internal/backend/media_stream.go:86](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_stream.go:86) | 当前打印含上游 api_key 的 redirectURL，必须同步脱敏 |
| [internal/backend/logger.go:181](D:/UserData/Desktop/Emby-In-One-main/internal/backend/logger.go:181) | Debugf 仍会进入内存日志，不能以“只有 debug”作为保存凭据的理由 |

不记录完整 body 或认证头。body 只记是否处理、字段路径、支持/未覆盖状态和大小；日志不得包含完整本地 token 或上游 token。

8.2 测试矩阵与明确的断言。

| 用例 / 建议测试名 | 文件落点 | 必须断言 |
|---|---|---|
| TestOutboundIdentityFinalQuery | 新 outbound_identity_test.go | URL 与 params 两边都有 UserId；混合大小写、重复值、空首值；最终只剩一个正确字段 |
| TestOutboundIdentityAbsentUserID | 同上 | 缺省 UserId 不新增；nil params/body 不 panic；原 query 不变 |
| TestOutboundIdentitySemanticPath | 同上 | 普通用户与 legacy 别名只替换完整用户段；未知段透传并标 unclassified；Users/Me、部署前缀与其他相同文本不误替换 |
| TestOutboundIdentityPolicy | 新 outbound_identity_policy_test.go | 当前用户规则生效；未知 query/body 不自动删除/注入/400；省略等价表初始为空；精确别名例外按第 5.4 节执行 |
| TestFallbackCurrentUserIdentity | 扩展 fallback_proxy_test.go | 普通用户 path/query 的已支持与精确别名场景归一化；未知写 body 保持原值；上游可确定才出站，多上游歧义依旧不出站 |
| TestSessionForwardsUpstreamUserID | 扩展 session_userdata_test.go | Playing/Progress/Stopped body.UserId 是目标上游用户；本地进度仍归本地用户 |
| TestOutboundIdentityPerUpstream | 新 outbound_identity_test.go | 同一 body/query 并发发 A/B：user-A/token-A 与 user-B/token-B 分别匹配；调用方数据不变 |
| TestOutboundIdentityDetachedContext | 同上 | context.Background() 搭配显式 reqCtx，仍保留普通用户身份；防止聚合后台任务丢上下文 |
| TestPassthroughAuthorizationSanitized | 扩展 identity_test.go + 新边界测试 | live、captured、last-success、latest 各测复合头存在/缺省；复合头覆盖 UserId+Token、仅 UserId、仅 Token、两者缺省；单独 X-Emby-Token 输入仍正常；最终身份来自上游，设备信息不变 |
| TestPersistedIdentityStripsLegacyCredentials | 扩展 identity_persistence_test.go | 旧格式捕获文件加载后，不把旧 Token/UserId 发出，也不把新凭据重新写入捕获文件 |
| TestOutboundIdentityBootstrap | 扩展 upstream_auth_test.go | AuthenticateByName 无旧 token；Users/Me 保留上游 API key；不注入空业务身份 |
| TestStreamRedirectUserID | 扩展 coverage_gaps_test.go | 视频/音频 302 Location 含真实 UserId、上游 token，无本地 UserId/token；关闭自动跟随重定向 |
| TestStreamHLSIdentityBaseURL | 扩展 media_stream_test.go | 基准 URL 归一化成功；生成代理 HLS 地址的现有行为不被破坏 |
| TestResponseIdentityPerUser（第二批） | 新 response_identity_test.go | 管理员/alice/bob 的登录与当前用户响应一致；相同 item 虚拟 ID 不按用户重复创建；缓存 legacy UserId 的请求在切换响应后仍有效 |
| TestIdentifierLookupFixtures | 新 identifier_lookup_test.go | 独立 fixture 中 Bob 是 local-user 但不是 Alice 别名，目标真实 ID 合法、其他上游 ID 分别分类，token 不当用户 ID |
| TestOutboundIdentityForeignUser | 新 outbound_identity_test.go | 已支持接口收到 user-A 却路由到 B，归一化为 user-B；未知接口保持 user-A 并标 foreign-upstream；同值跨服不误判 |
| TestOutboundIdentitySessionStatus | 扩展 session_userdata_test.go | 第 6.3 节各状态码精确匹配；正常网络失败仍 204；停止清理在准备失败后继续执行 |
| TestOutboundIdentityRecovery | 新 outbound_identity_test.go | normal 缺认证状态按既有去抖触发恢复；400 不触发；bootstrap、离线健康检查与收到 401/403 的链路仍可运行 |
| TestOutboundRawBodyPreserved | 新 outbound_identity_test.go | 未知格式原始字节、前后空白与 Content-Type 保持；不会强制解析为 JSON |
| TestOutboundIdentityLogging | 新 outbound_log_test.go | Logger.Entries 与日志文件都不出现哨兵 token、API key、密码；包含必要的目标与改写载体 |

沿用 [internal/backend/media_userid_forward_test.go:47](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_userid_forward_test.go:47) 和 [internal/backend/media_userid_forward_test.go:80](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_userid_forward_test.go:80) 两个原回归测试；加入多用户、多上游后，不降低其断言强度。

8.3 验证命令与阶段记录。

从 D:/UserData/Desktop/Emby-In-One-main 执行；这些是后续实施验收命令，本次文档修订没有运行尚不存在的修复测试。

第一批：先跑相关用例，再运行构建、全量测试与并发检查。

    go test ./internal/backend -run 'Test(IdentifierLookupFixtures|OutboundIdentity|FallbackCurrentUserIdentity|SessionForwardsUpstreamUserID|PassthroughAuthorizationSanitized|PersistedIdentityStripsLegacyCredentials|StreamRedirectUserID|StreamHLSIdentityBaseURL|OutboundRawBodyPreserved)' -count=1
    go build ./...
    go test ./... -count=1
    go test -race ./internal/backend -run 'Test(IdentifierLookupFixtures|OutboundIdentity|FallbackCurrentUserIdentity|SessionForwardsUpstreamUserID)' -count=1

P3 独立完成后：

    go test ./internal/backend -run 'Test(ResponseIdentityPerUser|MultiUser|SeriesRoutesOverlayLocalUserState|FallbackCurrentUserIdentity|OutboundIdentity)' -count=1
    go test ./... -count=1

P5-test 只运行其纯函数和 fixture 测试；不因测试侧诊断上线而引入服务器初始化、运行时开关等修改。新增独立并发共享状态时才补其相应 race 验收。

race 在具备 Go/CGO 工具链的环境运行；缺工具链时明确记录未运行，并在可用构建环境补齐，不能写成通过。中间阶段先跑相关测试，每批最终交付再跑该批完整验收。

实施后搜索检查遗漏：

    rg -n '\.doRequest\(|BuildURL\(' internal/backend -g '*.go'
    rg -n 'rewriteResponseIDs|rewriteItems\(|mergedItemsPayload\(|mergeRoundRobinItems\(' internal/backend -g '*.go'
    rg -n '\.UserID|ReplaceAll|Authorization|query.Encode\(\)|redirectURL' internal/backend -g '*.go'
    rg -n 'IsCurrentUserAlias|ClassifyLocalIdentifier|ContainsVirtualID|ContainsUserID|HasIssuedToken' internal/backend -g '*.go'

确认六处 doRequest 接线、两个 BuildURL 使用位置、P3 显式用户参数、所有运行时上游 UserID 读取、旧补丁和日志，以及两个谓词没有各自复制源查询逻辑。原有锁内访问与非 UpstreamClient 的 UserID 字段列为例外；搜索计数不构成测试通过。

每批交付记录：commit ID、文件清单、缺陷复现与新策略测试的分类、命令结果、真实联调覆盖、未测项目、兼容性变化、已知残留及回退方式。缺真实实例或真实客户端时，先完成代码和本地验证，标“代码完成 / 联调待验”，不能宣称兼容性验收完成，也不应停下所有能独立推进的实现工作。

8.4 真实实例联调：三维矩阵，必须观察真实请求形态。

维度独立：spoofClient（普通配置 / passthrough）× playbackMode（proxy / redirect）× 已认证业务请求的认证头形态（复合头 / 独立 token）。不能把 proxy 与 passthrough 当作互斥模式。

| 编号 | 上游 spoofClient | playbackMode | 客户端实际发送的业务请求头 |
|---|---|---|---|
| M1 | 普通配置（如 none） | proxy | Emby/MediaBrowser 复合头，明确含本地 Token 与 UserId |
| M2 | 普通配置（如 none） | proxy | 只以 X-Emby-Token 携带本地 token；无两种复合 Authorization 头 |
| M3 | 普通配置（如 none） | redirect | 含 Token/UserId 的复合头 |
| M4 | 普通配置（如 none） | redirect | 只以 X-Emby-Token 携带本地 token |
| M5 | passthrough | proxy | 含 Token/UserId 的复合头 |
| M6 | passthrough | proxy | 只以 X-Emby-Token 携带本地 token |
| M7 | passthrough | redirect | 含 Token/UserId 的复合头 |
| M8 | passthrough | redirect | 只以 X-Emby-Token 携带本地 token |

至少分别使用一个实测发送复合头、一个实测只发送独立 token 的真实客户端版本或配置，记录名称、版本、平台和捕获到的头字段名。Emby/MediaBrowser 风格、Infuse 风格仅是形态示例，不能仅凭品牌勾选矩阵；同一客户端版本可能使用不同请求格式。

这里的“含本地 Token/UserId”针对已经登录后的业务请求；“只以 X-Emby-Token 携带 token”只限制认证形态，仍可带 Client/Device 等独立设备头。登录前尚未签发 token，不能为了满足表格给初次登录人为注入凭据。初次登录与 API key 验证另外按 bootstrap 用例验证。

每个矩阵格执行：管理员与一个普通用户登录 → Views/媒体库 → PlaybackInfo → 播放 → 进度 → 停止，使用至少两台具有不同真实用户 ID 的测试上游验证目标归属。第一批普通用户可以继续看到 legacy 响应身份，但其请求必须被兼容；第二批再验证返回值切换与旧缓存兼容。

passthrough 的 M5–M8 是 P2 的兼容性验收门槛，不能只完成 M1–M4。至少在一台 passthrough 上游完成一次冷启动捕获身份、一次重启后 last-success 身份恢复，并验证密码登录与已有 API key 配置均不被 normal 规则拦截。

验收记录必须有以下可核对证据：

- 入站请求确实进入了预期的“复合头存在 / 缺省”分支；在客户端只发独立 token 的格子里，明确确认 Authorization 与 X-Emby-Authorization 缺省。
- 上游实际看到的 Client、Device、DeviceId、Version 与预期一致，认证成功；以字段名与不可还原的摘要记录敏感值，不把原始 token 写入文档或日志。
- Views 有预期内容，播放成功，进度归正确本地用户，停止释放资源；redirect 的 Location 使用正确上游身份，proxy 仍经 EIO 传输。
- 冷启动、复用捕获身份及重启恢复都实际完成，不用一次 live-request 成功代替全部来源。
- 用测试侧注入/重放补测复合头的 UserId-only、Token-only、两者缺省等分支；这种补测只能证明分支行为，不能替代缺失的真实客户端兼容性格子。

httptest 只能证明“发出了什么”，真实联调用于确认 Emby 与客户端仍接受设备与认证身份。没有对应客户端时明确标该格未测，不假定其他客户端等价。联调只用开发实例做播放与读取，不用删除用户、修改授权等未知写接口当探针。

第一批发布门槛：该批自动测试通过，M1–M8 的实际覆盖有记录，尤其 passthrough 两类头形态均通过；目标服务器/用户 ID/token 成对匹配，准备错误与未知字段行为符合动作表，日志无凭据。

第二批发布门槛：保留第一批兼容结果，并在已用客户端上验证普通用户响应身份与登录一致，以及缓存旧 UserId 的请求继续可用。第三批测试侧 D 不承担真实兼容性证明。

**9. P5-test：先交付测试侧 D，生产接线暂缓**

9.1 本轮只实现纯扫描与测试侧使用。

新增 outbound_diagnostics.go 的纯函数：输入最终 URL、选定头字段、已经编码的 body 字节、只读 IdentifierLookup 与显式扫描预算，返回发现列表和覆盖状态。扫描不能修改输入、消费实际请求 reader、打印日志、发送请求或持有全局可变状态。

测试中在 stub/请求记录器处调用它，复用第 5.1a 节签发/登记源的查询入口。P1 的策略和 D 的分类都使用这套源事实查询，不再分别遍历 IDStore/Auth/UserStore。测试预期仍由独立 fixture 写明，不能调用这个扫描函数或分类函数构造 want。

对已声明身份字段，测试直接断言上游服务器、真实用户 ID 和 token 相互匹配；D 对自由文本命中只返回诊断，不自动 t.Fatal。真实 ID 发到错误上游时，即使没有本地虚拟 ID，也必须被契约测试发现。

默认只覆盖可解析 URL/query、声明头字段、JSON 字符串和原始文本中完整的已登记标识；编码支持与扫描预算写进结果。超过输入预算时返回 truncated=true；不把截断或未支持编码报告成全量检查通过。可以先提取候选再查源，禁止逐请求对整个 IDStore 做字符串遍历。

9.2 身份分类与完备性。

IdentifierLookup 事实由 [internal/backend/idstore.go:240](D:/UserData/Desktop/Emby-In-One-main/internal/backend/idstore.go:240)、[internal/backend/auth_manager.go:21](D:/UserData/Desktop/Emby-In-One-main/internal/backend/auth_manager.go:21)、[internal/backend/user_store.go:175](D:/UserData/Desktop/Emby-In-One-main/internal/backend/user_store.go:175) 的源入口及上游认证快照提供；本次 reqCtx 的用户、legacy 别名与 token 也作为可信请求快照事实。源不可用时返回覆盖缺项，不伪装成“确认不属于本地”。

返回类别至少有 local-user、local-token、virtual-resource、target-upstream、foreign-upstream、unknown；可包含字段路径/载体、支持状态和截断标记，避免返回凭据明文给日志调用方。属于 target-upstream 的真实 ID 是合法出站值，不能等同本地泄漏。

[internal/backend/auth_manager.go:147](D:/UserData/Desktop/Emby-In-One-main/internal/backend/auth_manager.go:147) 和 [internal/backend/auth_manager.go:236](D:/UserData/Desktop/Emby-In-One-main/internal/backend/auth_manager.go:236) 的临时 SessionInfo.Id 目前没有登记；已删除标识、重置数据库前缓存值、过期 token 和未知编码也不保证识别。统一查询不自动补全这些来源，更不承诺“解决守卫守卫”。

未来新增签发来源必须同时加入源查询和独立 fixture 测试；若要覆盖临时 session ID，先设计生命周期与重启语义，不建立无限增长的全局集合。本轮不改 IDStore schema、不注册临时 session ID。

D 的最少测试：

- 已登记本地用户、资源、当前/其他本地 token 在受支持载体中能被分类。
- Alice 请求中的 Bob ID 属于 local-user，但不是允许的当前用户别名。
- 当前目标真实用户 ID 合法，其他上游用户 ID 分类为 foreign-upstream；两台服务器 ID 同值时不误判。
- 未知 ID、合法自由文本的命中不触发业务失败。
- 不支持的编码、不可用数据源、超预算输入返回明确覆盖状态。
- 输入 URL/header/body 没有被修改；请求记录 fixture 与 expected 分类相互独立。
- 用正确 token 配上错误上游用户/对象 ID 的用例必须失败于契约断言，而不是假装 D 可以发现所有映射错误。

P5-test 的代码只有测试调用，不能顺带改 server.go 初始化或 UpstreamPool 的 Reload。运行命令：

    go test ./internal/backend -run 'Test(OutboundDiagnostics|IdentifierLookupFixtures)' -count=1

9.3 P5-prod：本轮不实施，只保留后续落点。

后续确有生产诊断需求时再评估 [internal/backend/server.go:66](D:/UserData/Desktop/Emby-In-One-main/internal/backend/server.go:66) 的 App 注入、[internal/backend/upstream.go:90](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:90) 的构建、[internal/backend/upstream.go:127](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:127) 的 Reload、[internal/backend/upstream.go:696](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:696) 的发送前检查和 [internal/backend/upstream.go:600](D:/UserData/Desktop/Emby-In-One-main/internal/backend/upstream.go:600) 的 URL 输出检查。

那时才确定环境开关、默认关闭、限频与容量上限、扫描预算、日志字段、并发发布与重启行为。不能因为本文保留了 file:line 就在本轮创建 EIO_OUTBOUND_ID_DIAGNOSTICS 或生产限频表；也不能让暂缓的生产 D 阻塞已知缺陷修复。

**10. 交付检查表与执行记录**

本轮实施者按阶段记录；勾选必须有代码/测试/联调证据。P5-prod 明确留待后续。

- [ ] P0：先检查现有 Git 状态；建立排除配置、凭据、运行数据的本地源码基线，记录 commit ID。
- [ ] P0：原有八处定点修复保留；缺陷复现与新策略测试分开记录，预期来自独立 fixture。
- [ ] 第一批 P1：六处 doRequest 显式 reqCtx 接线完成，后台 context 用例通过。
- [ ] 第一批 P1：标识查询落在签发/登记源，两个谓词共享查询但保持不同语义；Bob 不成为 Alice 的别名。
- [ ] 第一批 P1：所有运行时 UpstreamClient.UserID 无锁读取完成审计与替换，锁内例外列明。
- [ ] 第一批 P1：BuildURL、redirect Location、HLS 基准 URL 使用公共 URL 规则。
- [ ] 第一批 P1：未知路径用户段透传并标 unclassified；未知 query/body 不自动删除、注入或新增 400；省略等价集合为空。
- [ ] 第一批 P1/P2：已支持当前用户载体使用同一认证快照，身份不参与资源路由；旧 fallback ReplaceAll 删除。
- [ ] 第一批 P2：复合头存在/缺省与各字段组合都有覆盖；密码登录、API key 引导、健康恢复保持正确。
- [ ] 第一批 P2：session 准备错误状态码、网络失败 204、本地进度归属及停止清理均按表验证。
- [ ] 第一批 P4：query、redirect、网络错误以及内存日志脱敏；未延后到 P3。
- [ ] 第一批：targeted/build/full/race 有明确结果；M1–M8 尤其 passthrough 两类真实头形态有覆盖证据，未测格不勾选完成。
- [ ] 第二批 P3：独立提交；先有 P1 新旧别名兼容再切换响应，普通用户与管理员响应身份分别正确。
- [ ] 第二批 P3：聚合参数接线及旧客户端缓存兼容验证通过；行为变更和回退方式已记录。
- [ ] 第三批 P5-test：纯扫描使用源查询，预期不由被测分类器生成，无生产钩子。
- [ ] 残留项和支持边界写入变更说明；不宣称 unknown raw body 或未知用户管理已修复。
- [ ] 更新两份分析材料中“六处/八处”“唯一出口”“客户端不可能持有上游真实 ID”“raw body 物理不可改写”等过强结论，不把文档当作实现已完成的证据。
- [ ] P5-prod：保持暂缓，不实施环境开关、限频表和运行时接线。

建议在实施提交说明或单独执行记录中填写下表；本次修订不创建虚假的测试结果：

| 批次 | Commit | 自动测试 | 真实矩阵/客户端版本 | 未测与残留 | 回退说明 |
|---|---|---|---|---|---|
| P0 基线 | 待实施填写 | 待运行 | 不适用 | 原有失败单列 | 基线保留 |
| P1/P2/P4 | 待实施填写 | 待运行 | M1–M8 待验 | 第 11 节 | 回退本批 |
| P3 | 待实施填写 | 待运行 | 旧缓存兼容待验 | 响应行为变化 | 可独立回退 P3 |
| P5-test | 待实施填写 | 待运行 | 不承担客户端证明 | 扫描覆盖限制 | 独立回退 |
| P5-prod | 不在本轮 | 不实施 | 不适用 | 另行评估 | 不适用 |

**11. 必须记录的残留与不承诺项**

1. 未分类 query/body/path 保持原值，可能把未知本地 ID 或跨上游真实 ID 发到不理解它的上游。diagnostic 标记不等于修复；第一批只保证动作表中已支持与明确兼容的场景。
2. redirect 会通过 [internal/backend/media_stream.go:71](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_stream.go:71) 至 [internal/backend/media_stream.go:88](D:/UserData/Desktop/Emby-In-One-main/internal/backend/media_stream.go:88) 把上游 token 放进直连 URL。客户端可能因此取得并回发真实上游用户 ID；不能把“客户端永远不知道上游 ID”作为安全前提。已支持当前用户接口直接按目标快照归一化，未分类接口仅标 foreign-upstream 并保持原值，不擅自决定跨服映射。
3. [internal/backend/id_rewriter.go:5](D:/UserData/Desktop/Emby-In-One-main/internal/backend/id_rewriter.go:5) 的 simpleIDFields 包含通用 Id，未知用户列表/用户对象会落入资源虚拟化逻辑。P3 只修已确认当前用户响应，不解决全部目标用户对象语义；不为修这个残留把所有用户 ID 塞进资源路由模型。
4. rawRequestBody 是字节包装，不是“物理不可改写”。已知格式可显式适配；本轮未知格式保持原始字节，不能保证修复其 UserId。D 对未知编码同样无法保证检测。
5. 身份去重、临时 session、删除后/过期/重置前缓存标识可能超出当前查询覆盖。共享源入口及其测试只能验证已列出的来源，不能证明未来所有签发路径完备。
6. 真实客户端兼容性必须由实际请求形态与联调记录支撑；“某品牌通常用某种头”“两次都是 200”“只有一个正常播放”都不能代替矩阵覆盖。
