# 发布规范（维护者）

[README](README.md) · [Docker 使用与运维](DOCKER.md) · [English Docker guide](DOCKER_EN.md)

本文是对外公开的发行约定，不是自动化功能清单。**工作流的实际能力以下表为准**；其余门禁、提升及记录由维护者执行。发布、更新标签和部署是三个不同授权边界；运行构建不意味着可修改生产服务器。

## 1. 当前流水线的真实边界

| 流程 | 触发 / 产物 | 现有检查与限制 |
| --- | --- | --- |
| CI | main/master push、PR | Go build/vet/gofmt/race、前端测试、脚本及面板资源检查；不是 Docker 镜像验收 |
| Release | 推送 `V[0-9]*` tag | vet、普通全量 Go 测试、六架构静态二进制、amd64 `--version`、静态链接检查、源码包/脚本/面板与 SHA-256；自动创建公开 Release |
| Docker image | 仅 `workflow_dispatch` | 从选定 ref 构建并立即推送 GHCR，amd64/arm64；**不自动执行测试、版本运行检查或跨架构验收** |
| 私有测试构建 | 若另行配置独立私有仓库 | 与正式发布隔离；Artifact 不等于公开 Release、镜像或生产部署 |

当前 Release 的 tag 含 `-` 就被视为预发布；不要使用不合约定的 tag 测试生产发布流程。Release 不是草稿预演，推 tag 即可能公开。重跑现有 Release 会覆盖同名资产，不能把它当无副作用重试。

Docker 工作流当前：

- 镜像地址为 `ghcr.io/<仓库所有者>/<仓库名>`（小写）；fork 发布到自己的命名空间。
- 只生成短提交 SHA 标签；在默认分支运行还会生成/覆盖 `latest`，**不判断是否稳定版**。
- `version` 输入优先，其次 tag，再其次 `git describe`；该值只是 `main.Version`，不是检出 ref，也不是镜像 tag。默认 OCI 标签由 metadata-action 生成，不保证与输入版本相等。
- 使用 `GITHUB_TOKEN` 的 `contents: read`、`packages: write`；不能让不可信 ref、PR 或未经审查的 Dockerfile/工作流获得上游仓库的发布凭据。
- 没有独立的镜像验收、扫描、签名及可验证供应链证明门禁；存在某种 Buildx 元数据不代表上述门禁已完成。

## 2. 版本身份必须分开记录

| 身份 | 示例 / 规范 |
| --- | --- |
| Git tag / Release 标题 | `V1.4.4`、`V1.4.4-rc3`（大写 V） |
| 程序版本 / 资产文件名 | `v1.4.4`、`Emby-In-One-linux-amd64-v1.4.4`（小写 v） |
| 镜像版本标签（人工提升约定） | `ghcr.io/arizesky/emby-in-one:v1.4.4`、`:v1.4.4-rc3` |
| 源码提交 | 完整 40 位 commit SHA；文档提交可在发布后继续前进，不意味着产物改变 |
| 镜像身份 | 多架构 index digest，外加各平台 digest；不要拿本地 image ID 代替 |
| 附件身份 | 每个文件的 SHA-256；不是镜像 digest，也不是签名 |

**版本号、源码和产物不能张冠李戴。** 禁止从 main 任意提交构建，再仅通过 `version` 输入冒充发布 tag。正式产物使用干净的受审源码，不包含凭据、运行数据、内部报告或未跟踪草稿。

新发布的版本 tag、版本镜像标签和附件应视为不可变；发现问题优先发布递增的 rc 或新版本。移动已发布 tag、替换同名资产或镜像须另行明确批准并公告旧/新提交及摘要。历史 rc3 的特批重新发布不是日常惯例。只修改 Release 正文无需重建产物；已发布源码包和 tag 中的文档仍是构建时快照。

## 3. 发布前检查

- [ ] 确认源分支、工作区与完整 SHA，保留无关本地改动，不强推 main。
- [ ] 划定相对上一版的变更边界。更新日志说明用户可见变化、配置/数据迁移和已知限制；开发期 rc 自身回归修复不必逐条当作新增功能宣传。
- [ ] README 中的版本、安装说明、Compose/build VERSION 和脚本说明一致；指定版本时不误用 `/releases/latest`。
- [ ] 验证前端资源、脚本语法、构建、vet 和相关回归。全量 race 若失败/超时应先分类处理，未通过不能写“全绿”；任何例外必须明示，不悄悄绕门禁。
- [ ] 确认上游账号、普通用户授权、候选版功能（包括年龄分级）及迁移风险的实际验收范围，不用健康检查替代播放验收。
- [ ] 决定本次发布渠道：二进制 / Docker 源码 / GHCR。没有镜像就在说明中写“镜像未发布/待验证”，不给出不存在的拉取地址。
- [ ] 确认回滚版本和配套备份，跨版本数据库兼容性不明时不承诺无损降级。

## 4. Release 与镜像发布顺序

### 4.1 标准 Release

1. 提交并同步经过审阅的源码/更新日志，确认 CI 结果。
2. 对确认过的提交创建新版本 tag，推送一次，记录 Release run ID；不要反复删 tag 重推来重试。
3. 等待 Release 工作流完成；检查提交、预发布标记、版本输出、六架构和附件清单。
4. 每份非校验附件须有同名 `.sha256`，缺失、空文件或下载哈希不匹配不得宣布可用。当前标准集合为 11 份资产及 11 份校验文件；架构调整时一起更新清单。
5. 记录正式稳定版 `/releases/latest`。候选版不能替代稳定版；本项目安装脚本默认走最新稳定 Release。

工作流当前自动公开 Release，不能实现“镜像和二进制验收后一起原子公开”。因此先说明二进制可用、镜像待验收，镜像完成后再补充经过核验的 digest；不要声称已协调发布。

### 4.2 手动构建 GHCR 镜像

使用 GitHub CLI/Actions UI，**选择准确的发布 tag，不是 main**。下面示例仅在维护者获得该版本发布授权后执行：

```bash
TAG=V1.4.4-rc3
VERSION="v${TAG#V}"
git ls-remote origin "refs/tags/$TAG" "refs/tags/$TAG^{}"
gh workflow run docker.yml --repo ArizeSky/Emby-In-One --ref "$TAG" -f version="$VERSION"
```

这是实际发布操作：当前 `push: true`，没有 dry-run。确认运行的 ref/SHA；记录 run ID、日志中的完整 index digest 和生成标签。不要因列表尚未出现 run 就重复 dispatch。失败先看 job 日志，区分网络、权限、编译或发布失败；不通过关闭 TLS 验证等方式“修复”。

**候选版禁止在默认分支运行 Docker 工作流**，以免覆盖 latest。即使稳定版也优先 tag 构建，验收后再人工提升 stable 别名。

### 4.3 验收后提升标签（当前为人工步骤）

1. `docker buildx imagetools inspect <image>@sha256:<index-digest>`，确认真实存在 `linux/amd64` 和 `linux/arm64`；证明是构建成功还不等于两平台运行验收。
2. 在对应平台运行相同 digest 的 `--version`；使用隔离 config/data 做健康、登录和必要业务冒烟测试，不挂生产数据库，不带真实上游凭据做公共 CI 测试。
3. 核对 OCI source/revision、run 检出 SHA 和程序版本；不要仅凭版本字符串相信来源。Dockerfile 未锁定所有基础依赖，同源码不保证得到相同 digest。
4. 通过后将**已构建的 digest**提升为版本标签，不重新构建一份同版本镜像：

```bash
# 将占位符换成已验收的真实 index digest；这会写入注册表标签。
IMAGE=ghcr.io/arizesky/emby-in-one
DIGEST='sha256:REPLACE_WITH_VERIFIED_INDEX_DIGEST'
VERSION=v1.4.4-rc3
docker buildx imagetools create --tag "$IMAGE:$VERSION" "$IMAGE@$DIGEST"
docker buildx imagetools inspect "$IMAGE:$VERSION"
```

提升后重新核验目标 digest（工具可能重建索引，不能假定未变）；记录最终身份。首次 GHCR 包可能不是 public，需确认匿名 pull 权限。不为方便把 registry 管理 token 给普通用户。

5. **仅正式稳定版**可在额外确认后用同样方式提升 `latest`；候选版永不提升 latest。当前工作流没有稳定版语义保护，这是一条人工发布禁令。
6. Release 正文补充：源 commit、镜像完整引用/最终 digest、平台、版本标签、Actions 链接和验证边界。未完成某平台运行验证就明示。

镜像内 `Dockerfile` 源码编译与 Release 静态二进制是独立构建，镜像不直接打包 Release 二进制；不能要求两者文件哈希相同。Docker 源码包有 SHA-256 是下载完整性校验，不是独立签名或无条件安全背书。

## 5. 发布后验收与撤回

- 核验 Release 非 draft、prerelease 是否正确；候选版发布前后 stable Latest 不变。
- 抽查下载校验、程序版本和源码包关键文件；文档链接可达。实际用户播放和权限测试结果单独记录，不能由 build 成功代替。
- 源码包不含所有仓库文档/测试/工作流，具体以 `release.yml` 的 archive 白名单为准；新运维文档和镜像 Compose 不会自动出现在旧附件中。
- 故障撤回要分别处理 **Release、Git tag、GHCR 标签/digest、镜像别名、已部署实例**。删除其中一个不会删除其他渠道，也无法收回用户已拉取的镜像。
- 先停止继续推广，保存诊断与摘要，说明影响和回滚/替代版本。若必须移动 stable 别名，只指向经过验收的旧 digest，不能删所有版本后让用户猜替代品。
- 生产回滚须另行授权，先停写并备份失败状态；需要时恢复升级前数据。不要远程替用户批量降级或自动删数据库。

## 6. 后续自动化改进（尚未实现）

1. 校验发布 tag、源码 SHA 与版本字符串一致，拒绝不可信 ref；将包写权限限于实际发布 job。
2. 镜像构建与发布拆分：先完成版本/平台冒烟，后推送或提升正式别名；保留纯构建/Artifact 测试通道。
3. 自动生成版本镜像标签，对 prerelease 禁止 latest；从人工约定升级为工作流强制规则。
4. 在 Release 中自动记录源码 SHA、构建 run 和镜像 digest，增加必要的漏洞扫描、SBOM/签名与来源验证说明。
5. 安装/SSH 菜单增加显式部署类型与 image/digest 更新，避免将拉镜像部署误当源码重建；增加健康验收和有边界的回滚。

上述改进需独立审阅实施；本文不会把尚未实现的门禁、签名、镜像更新或性能优化写成已经可用。
