# AGENTS.md — 仓库执行约定

## 远程与工作树

- 主仓库为 Gitea：`origin = https://gitea.wmr.ovh/MineNine/Emby-In-One.git`。
- `github = https://github.com/ArizeSky/Emby-In-One.git` 仅作只读镜像。未经用户明确要求，不向 GitHub 推送。
- 禁止 `git push --all`、`git push --mirror`、force-push 或改写已有提交，除非用户明确授权相应操作。
- 开始修改前确认当前 worktree、分支和 `git status`；保留无关的用户改动。不要因为默认分支是 main 就把当前任务分支切到 main。
- 社区 PR 整合任务在 `codex/community-prs-local-review` 分支执行，未获授权不合并回 main。

## 每个完成步骤必须独立提交并推送 Gitea

1. 将任务拆成可独立验证的具体步骤，维护简短进度清单。
2. 完成一个步骤后先运行该步骤相关测试/检查；修复确定的在范围内失败，不扩大为无止境审查。
3. 检查差异，只暂存本步骤的任务文件；不得顺带提交用户已有草稿、未跟踪内容、token、真实配置或本地日志。
4. 使用有明确目的的英文提交信息（`fix(...)`、`test(...)`、`docs` 等）提交。
5. **立即将该步骤提交推送到 Gitea `origin` 的当前任务分支，确认成功后再开始下一步骤。不要将多个已完成步骤攒到最终一次推送。**
6. 向用户报告已完成步骤、相关验证与提交号；最终核对远程任务分支与本地 HEAD 同步。

标准推送示例（PowerShell）：

```powershell
$branch = git branch --show-current
if (-not $branch) { throw 'Detached HEAD: confirm the task branch first' }
git push origin $branch
```

### WireGuard 出站 / DNS 波动

- `Could not resolve host`、连接超时等暂时网络问题应持续退避重试，不因一次 DNS 失败就认定仓库不可用。
- 分批重试，间隔逐渐增加并封顶约 30 秒；一批仍失败时保留本地提交，向用户报告当前网络状态，继续后续重试，直到成功、用户中止，或确认并非可恢复网络故障。
- 不擅自改 DNS、WireGuard、Git 凭据、远程地址或改推 GitHub；不绕过 TLS 校验，不 force-push。
- 鉴权拒绝、无仓库权限或 non-fast-forward 应按真实错误处理，不能伪装为 DNS 问题重试覆盖远程。发现远程新提交先检查并与用户意图协调。
- 确认成功以 Git 原生命令退出码和远程引用为准；不要因 PowerShell 管道处理 stderr 的表现误报成功/失败。

## 验证与文档

- Go：相关测试完成后，最终运行完整 `go test`、`go vet`、构建、`gofmt` 和 `git diff --check`；涉及并发状态必须实际运行相应 `-race` 测试。
- 前端改动应执行相关 Node 测试；本仓库 `npm test` 运行 `tests/*.test.cjs`。
- Windows 使用 PowerShell 语法；本地工具链可显式指定 Go 路径，race 需要 MinGW GCC 在 PATH。
- 只声明实际执行过的检查，注明全量或定向范围；静态复核不能冒充执行测试，受控假时钟测试不能冒充生产压力验收。
- 更新执行报告/路由矩阵时按实际调用顺序记录，不沿用已经被评审证伪的“全部完成”结论；明确未做的真实客户端、部署和性能验收。

## 代码与文件边界

- 保持 rc3 架构和现有职责划分；优先小而聚焦的修改，不引入不必要框架或无关重构。
- 尽量单文件不超过 1000 行、单函数不超过 80 行；已有大文件的新增职责优先拆到独立文件，不机械重排无关代码。
- 秘密凭据不进入日志、响应 URL、提交或报告；跨 origin 跳转不得扩大上游认证信任。
- 临时脚本、测试日志和构建验证产物放会话 scratch，不放项目根目录；交付文件才进入工作树。
- 不直接使用 `rm` / `Remove-Item` 永久删除用户文件；需删除时按用户约定移入回收站。未经确认不提交本地设计草稿。
