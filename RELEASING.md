# 发布规范（维护者）

[README](README.md) · [Docker 使用与运维](DOCKER.md) · [English Docker guide](DOCKER_EN.md)

本文说明维护者如何校验发行身份、发布镜像与提升稳定标签。源码合并、工作流验证、镜像发布和实例部署是不同操作；只有实际发布并核验的镜像引用才能提供给用户。

## 1. 流水线边界

| 流程 | 触发 / 产物 | 检查与限制 |
| --- | --- | --- |
| CI | main/master push、PR | Go build/vet/gofmt/race、前端/脚本等；不是镜像运行验收 |
| Release | 推送 `V[0-9]*` tag | 普通 Go 测试、六架构静态二进制、脚本/面板/源码包和 SHA-256；自动公开，不是无副作用预演 |
| Docker | 仅 workflow_dispatch，默认 validate | 准确 tag/SHA、双架构构建、隔离版本/健康运行，验证附件；独立 publish job 才能写 GHCR |

Release tag 含 `-` 被视为预发布。重跑现有 Release 可能覆盖资产，不能为了测试脚本随意推 tag。新安装包需包含 manager 和镜像模板；旧发布附件不会随 main 文档变化而更新。

## 2. 身份与不可变约定

| 身份 | 示例 |
| --- | --- |
| Git tag | `V1.4.4`、`V1.4.4-rc3`；工作流也接受合规小写 v |
| 程序 / 镜像版本标签 | `v1.4.4`、`v1.4.4-rc3` |
| 源码提交 | 完整 40 位 commit SHA；annotated tag 必须解析到 commit |
| 构建配方 | 工作流所在受审提交；旧 tag 的应用源码可用新版受审 Dockerfile 构建，二者须分别记录 |
| 镜像 | 多架构 index digest、各平台 manifest digest、本地 image ID 是不同身份 |
| 附件 | 每份归档/二进制自己的 SHA-256；不是镜像摘要或签名 |

禁止从任意 main 代码填一个发布版本号冒充正式源码。`expected_sha` 必须与准确 tag 解析结果相同；版本从 tag 推导，没有任意 version override。SemVer build metadata 含 `+` 无法直接用作 Docker 标签，本工作流拒绝这类 tag，而不悄悄改写。

已发布的 Git tag、版本镜像标签和附件视为不可变。错误优先通过递增 rc/新版本修复；移动 tag 或替换产物须额外授权。后续文档提交不改变已有附件快照。

## 3. Docker 工作流的强制门禁

工作流必须从默认分支的受审版本手动运行；应用源码通过输入 tag/SHA 选择，避免在不可信分支/旧 tag 上执行带发布权限的工作流。

1. `mode=validate` 为默认，只读权限，不登录 GHCR。无 tag 的开发验证仅产生 `dev-<sha>`，不能进入发布阶段。
2. 发布需 `mode=publish`、准确 `release_tag`、完整 `expected_sha`；tag 与 SHA 不一致直接失败。
3. 构建 job 无包写权限，不接触注册表凭据。使用准确源码与受审构建配方，检查 amd64/arm64 版本、UID 1000、持久化挂载和隔离健康，导出附带校验和的镜像附件。
4. publish job 只接收同次成功验收的附件，重新核对 tag/SHA、校验和与 revision；不重新编译。该 job 才有 `packages: write`。
5. 先推带 SHA/run/attempt 的唯一候选平台标签，生成候选多架构 index，核对真实 amd64/arm64，再提升**同一 digest**为准确版本标签。
6. 版本标签已存在且 digest 不同则拒绝覆盖；相同 digest 允许幂等确认。只有精确 `MANIFEST_UNKNOWN` 才视为不存在，鉴权、网络、429、5xx 或异常 404 都失败。
7. 同仓库 Docker 发布串行执行，避免本工作流间竞争；这不能防止仓库外有权限的操作者另行改标签。发现最终 digest 不符必须报错。
8. `latest` 不再随默认分支构建自动变化。只有显式 `promote_latest=true` 且目标为当前已公开、非 draft、非 prerelease 的正式稳定 Release 才允许，提升前再次核对。rc/beta/alpha、分支及稳定降级均拒绝。

不同注册表标签不是一个原子事务。发布中途失败可能留下候选或已完成的标签；保存日志/run/digest，检查实际状态再决定补救，不能把失败当成“什么都没写”。不自动清空 GHCR，不以重新构建冒充同一产物。

## 4. 操作示例（需要单独的公开发布授权）

下面命令会调度验证工作流。先确认默认分支上的工作流已审阅，并将示例 tag 和 SHA 替换为实际待验证版本：

```bash
# 工作流定义来自受审默认分支，应用源码来自 release_tag 指定的准确 tag。
gh workflow run docker.yml --repo ArizeSky/Emby-In-One --ref main \
  -f mode=validate -f release_tag=V1.4.4-rc3 \
  -f expected_sha=REPLACE_WITH_VERIFIED_FULL_COMMIT_SHA
```

验收授权与正式发布授权分开。明确获准后才把 `mode` 改为 `publish`；候选版不传 latest 确认。正式稳定版提升 latest 也需明确确认。CLI dispatch 成功后记录 run ID，不因列表暂时没出现而重复调度。

检查最终 index digest 和平台，匿名 pull 权限也要确认。首次 GHCR 包可能不是 public；不要将注册表管理 token 给普通用户。对用户公布实际引用，不猜测同名镜像存在。Docker 源码包不是可 `docker load` 的镜像。

## 5. 发布前与发布后检查

- [ ] 源码/分支、完整 SHA、变更边界、源码/构建配方与版本对应。
- [ ] 脚本/Node/Compose、普通 Go 检查、构建与真实 Docker 验收结果完整；全量 race 未通过就明确写出，不删门禁或暗中放行。
- [ ] 明确发布哪些渠道：Release、Docker 源码包、GHCR 相互独立。没有镜像就明确写尚未发布。
- [ ] 预发布功能（尤其年龄分级）的性能、上游限频和客户端兼容限制仍保留；健康不能代替用户播放与授权验收。
- [ ] 升级前停止写入并保存配套数据和旧镜像；数据库兼容不明不承诺无损降级。
- [ ] 公开 Release prerelease/draft 与 stable Latest 正确；版本标签/index、平台、版本、匿名拉取和附件校验正确。
- [ ] 当前未来标准附件清单仍为 11 份资产及 11 份校验文件；源码包内新增文件不等于增加独立附件。

撤回须分别检查 Release、Git tag、GHCR 版本/别名/digest、已部署实例；删除一个不会撤回其他渠道，更无法收回用户已拉取的镜像。生产操作须另行授权。保留诊断，公告影响及替代版本，不批量删库或自动降级。

## 6. 能力边界

签名、SBOM、漏洞扫描和可验证供应链证明尚未加入；Buildx 元数据不能代替这些门禁。构建及隔离注册表测试不代表 GHCR 鉴权、包可见性和匿名拉取已经通过；发布后仍须实际核验。容器健康检查也不能代替用户播放、授权或生产升级验收。
