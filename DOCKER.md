# Docker 部署与运维

[返回 README](README.md) · [English](DOCKER_EN.md) · [维护者发布规范](RELEASING.md)

本文区分**当前实现**和**使用约定**。有 GitHub Release 不代表同版本 GHCR 镜像已发布；镜像存在与否、支持的平台和 digest 必须另行确认。rc3 的年龄分级功能仍需更多测试与调整，尤其是大库性能、上游限频与客户端兼容性，详见 [更新日志](Update.md)。

## 1. 先选一条路线，不要混用

| 路线 | 文件 / 命令 | 更新方式 | 适用场景 |
| --- | --- | --- | --- |
| 预构建镜像 | `docker-compose.image.yml`，只有 `image:` | 修改固定镜像引用，`pull` + `up -d --no-build` | 对应镜像确已发布；无需在服务器安装 Go/C 编译链 |
| 本地源码构建 | 仓库 `docker-compose.yml`，包含 `build:` | 校验并更新源码，重新构建，再 `up -d` | 定制代码，或目标版本暂无可用镜像 |
| 托管安装 / SSH 菜单 | `install.sh --mode source` 或 `--mode image --image REF` | 保持原部署模式；镜像锁定 digest，源码显式构建 | 新版完整发行目录；旧自定义部署先核对迁移 |
| Release 二进制 | `release-install.sh` + systemd | 下载对应架构二进制 | 不使用容器；见 README |

`Emby-In-One-docker-v*.tar.gz` 是源码包，不是镜像，不能 `docker load`。GitHub 自动生成的 `Source code` 压缩包、上述源码附件、GHCR 镜像也是三种不同产物。

**新版托管部署区分 source 与 image。** 使用 `.eio-deployment.json` 和 `.eio-compose.yml` 记录项目，不执行状态文件。独立手工 Compose 部署不会被静默接管；可显式 `adopt` 纳入启停/日志管理，但旧自定义部署的自动升级被拒绝，须审阅迁移。不要让 systemd 与容器同时写同一份数据。旧 rc3 附件不含本次修复。

## 2. 镜像、标签与版本

- 官方镜像地址：`ghcr.io/arizesky/emby-in-one`。当前已公开发布 `v1.4.4-rc3`，可匿名拉取，支持 `linux/amd64` 和 `linux/arm64`，Docker 自动选择架构；这不等于 Release 二进制的六架构支持。ARMv7/MIPS/RISC-V 请使用对应二进制或自行构建验证。
- Docker 工作流仅手动触发，默认 **validate**；正式发布必须指定准确 tag 和完整预期 SHA。程序版本由 tag 推导，不能任意覆盖。
- 构建与验收无包写权限；独立发布任务接收已验收附件，构建候选多架构 digest 后提升版本标签。已存在版本标签不能被不同 digest 覆盖。
- 不再因为默认分支构建自动写 `latest`。只有明确确认且对应当前已公开正式稳定 Release 才允许提升；候选版永不提升 latest。
- 当前未提供 `latest` 标签。安装时使用 `v1.4.4-rc3` 或下方固定摘要；其他版本须单独确认是否已发布，不因 Release 或构建成功而假定镜像存在。
- digest、镜像 ID、源码提交 SHA、Release 附件 SHA-256 不是同一个值。短 SHA 标签可定位源码，digest 才固定镜像字节；CPU 架构选择由多架构索引处理。

当前 rc3 的多架构索引 digest 为 `sha256:2e9a60aea67a4716b1ec4ba10a2eaf3978acab9b66a038c8aa7b46343e047f0c`。在已安装 Docker/Buildx 的机器上检查：

```bash
IMAGE_REFERENCE='ghcr.io/arizesky/emby-in-one@sha256:2e9a60aea67a4716b1ec4ba10a2eaf3978acab9b66a038c8aa7b46343e047f0c'
docker buildx imagetools inspect "$IMAGE_REFERENCE"
```

本文镜像示例固定为这个已发布的 rc3 索引，不是平台专属 manifest；更换版本时需核对目标版本的摘要。rc3 无需登录 GHCR；403/denied 可能是包权限问题，不一定是网络问题。若使用其他私有包，应以最小 `read:packages` 权限通过 `docker login ghcr.io --password-stdin` 登录，勿将令牌写进 Compose、镜像或 Git。

## 3. 首次使用预构建镜像

以下为 Linux + Docker Engine + Compose v2 操作；在新目录进行，避免覆盖现有部署。命令涉及属主与受保护目录时使用 root 或 sudo。macOS/Windows Docker Desktop 主要用于开发测试，宿主权限和网络行为需单独验证。

### 3.0 推荐：新版托管安装（完整可信发行目录）

需要 Linux root、Docker/Compose、Python 3、curl、flock 和 tar；安装器可沿用原 Docker/Compose 安装步骤，其余缺少的工具会明确报错。使用当前主分支的安装工具，明确选择 image 模式，无需本机编译；旧 rc3 Release 源码附件不含新版 manager。

```bash
git clone https://github.com/ArizeSky/Emby-In-One.git
cd Emby-In-One
sudo bash install.sh --mode image --image ghcr.io/arizesky/emby-in-one@sha256:2e9a60aea67a4716b1ec4ba10a2eaf3978acab9b66a038c8aa7b46343e047f0c
```

没有 `--mode` 时保留 source 默认路线。镜像拉取失败绝不转为源码构建。首次随机密码只保存在部署目录的 root-only `.eio-initial-password`，不输出到日志。默认监听 `127.0.0.1:8096`；可显式指定 `--port` / `--bind`，不要意外公开管理入口。新状态必须与实际 Compose/容器交叉匹配。

```bash
sudo bash /opt/emby-in-one/eio-docker.sh status
sudo bash /opt/emby-in-one/eio-docker.sh update --image ghcr.io/arizesky/emby-in-one@sha256:2e9a60aea67a4716b1ec4ba10a2eaf3978acab9b66a038c8aa7b46343e047f0c
sudo env EIO_PROJECT_DIR=/opt/emby-in-one emby-in-one
# 自定义目录也可显式传给 manager：--project-dir /opt/eio-test
```

上面的 update 展示显式镜像更新用法；更新到其他版本时替换为其已发布摘要。source 更新用 `update --source-dir <已核验源码目录>`，不能传 `--image`。默认稳定更新要求目标版本确有对应镜像/已校验源码包；缺失则保留现状，不追 latest、不猜版本。旧无校验源码包可能不支持此新管理路径。测试/未知版本不参与自动稳定降级，须指定明确目标。

更新先准备目标、停服确认，再备份 config、完整 data（含存在的 WAL/SHM）、部署配置和旧镜像身份。目标有限时健康、实际版本/镜像/UID/挂载验收失败时，恢复**配套旧数据快照和旧镜像**；失败现场保存在 `.eio-recovery-*`。恢复也失败明确返回非零，不无限重试。自动更新只支持工具管理的标准两份挂载，不支持外部数据库/共享目录/未知写入者。备份时间点后的失败阶段新增写入不保证保留；不承诺零停机或数据库跨版本兼容。

密码重置由 manager 从 stdin 读取并确认停服；完成后恢复原先运行状态。不要用 `--force` 绕过保护。卸载只移除本项目服务，保留 config/data、备份和镜像；备份含敏感信息，不上传 Issue。下面 3.1 起仍是**不由 manager 托管的手工 Compose**路线，二者不要混用。

### 3.1 准备持久化目录和配置

```bash
sudo mkdir -p /opt/emby-in-one/config /opt/emby-in-one/data
cd /opt/emby-in-one
```

将已审阅源码中的 [docker-compose.image.yml](docker-compose.image.yml) 复制到当前目录。不要同时叠加仓库的构建型 `docker-compose.yml`。

创建 `config/config.yaml`（例如 `sudoedit config/config.yaml`），**将密码占位符换成强随机密码**：

```yaml
server:
  port: 8096
  name: "Emby-In-One"
  trustProxy: false
admin:
  username: "admin"
  password: "REPLACE_WITH_A_STRONG_RANDOM_PASSWORD"
playback:
  mode: "proxy"
proxies: []
upstream: []
```

首次启动不会替你创建该配置；程序需要有效的管理员用户名和密码。密码启动后转换为哈希；上游凭据仍需按敏感信息保护。

```bash
sudo chown -R 1000:1000 config data
sudo chmod 700 config data
sudo chmod 600 config/config.yaml
```

配置目录也要可写：程序会原子更新配置、生成服务器 ID、保存密码哈希及面板设置。不要单独只读挂载 `config.yaml`。默认 `data/` 在容器内为 `/app/data`；若设置 `dataDir`，必须使用**容器内路径**并增加匹配的持久化挂载，不能填写未挂载的宿主路径。

### 3.2 固定镜像引用并启动

创建 `.env.image`（以下固定到当前已发布的 rc3），再运行：

```dotenv
EIO_IMAGE=ghcr.io/arizesky/emby-in-one@sha256:2e9a60aea67a4716b1ec4ba10a2eaf3978acab9b66a038c8aa7b46343e047f0c
EIO_BIND_ADDRESS=127.0.0.1
EIO_HTTP_PORT=8096
```

这些是 Compose 插值变量，不是程序的管理员密码或上游配置变量。不要在 shell 中导出不同的同名变量覆盖文件；`config` 检查实际生效的引用。

后续示例均在部署目录、同一个 Bash 会话中执行；新会话请重新定义函数：

```bash
dc() { docker compose --env-file .env.image -f docker-compose.image.yml "$@"; }
dc config --quiet
dc config --images
dc pull
dc up -d --no-build
dc ps
dc exec -T emby-in-one /app/emby-in-one --version
dc logs --tail=100 emby-in-one
curl -fsS http://127.0.0.1:8096/System/Info/Public
```

若 Docker 权限不足，给函数内的 `docker` 加 `sudo`，或使用具备授权的管理会话。Docker 组本身近似宿主 root 权限，不要为方便任意授予。切勿用 `--privileged`、挂载 Docker socket、`chmod -R 777` 或强制 root 来规避普通目录权限问题；rootless Docker / userns-remap 的 UID 映射需另行调整。

样例默认**仅绑定宿主回环地址**。远程直连需明确设为 `EIO_BIND_ADDRESS=0.0.0.0` 并配置防火墙/TLS；有宿主反向代理时通常保持回环绑定。另一容器内的代理应使用共享 Docker 网络和服务名，`127.0.0.1` 不会指向 EIO 容器。不要把已有 HTTP 反代错误改成跳过 TLS 校验。

只改宿主端口，例如 `EIO_HTTP_PORT=18096` 时，容器内仍保持 8096，检查 URL 对应改为 18096。若改配置中的 `server.port`，还须一起调整端口映射和容器 HEALTHCHECK；否则服务可能正常却显示 unhealthy。

确认健康后进入 `/admin/` 添加上游、创建普通用户；`/web/` 只用普通用户。passthrough 上游须由管理员使用真实 Emby 客户端完成身份采集，未采集时等待登录不等于容器故障。反向代理设置见 [README](README.md#反向代理-nginx)。

## 4. 日常管理、版本核验与健康边界

```bash
dc restart emby-in-one
dc stop -t 60 emby-in-one
dc up -d --no-build
dc logs --since=30m --tail=200 emby-in-one
cid=$(dc ps -q emby-in-one)
docker inspect --format '{{.Config.Image}} {{.Image}} {{.State.Health.Status}}' "$cid"
```

- `pull` 只下载，不替换运行容器；`restart` 只重启旧容器，不应用新镜像、端口或挂载；更新必须 `up -d --no-build`。
- 同时核验容器引用、镜像 digest、程序 `--version`，管理面板 `/admin/api/status`（需认证）可交叉确认。版本字符串和元数据标签均可人为指定，不能代替源码/digest 记录。
- Docker healthy 只代表公开健康端点能响应，不代表所有上游在线、普通用户可播放、字幕正常或分级策略验收通过。`restart: unless-stopped` 不会仅因 unhealthy 自动重启。
- 挂载整个 `config/`、`data/`。SQLite 保存用户、观看历史和虚拟 ID；`tokens.json`、`captured-headers.json`、`ua-library.json` 等也需保留。删数据库可能使客户端缓存 ID 失效，并非无害清缓存。
- `json-file` 轮转只控制 Docker 标准输出日志，不控制 `data/` 的应用日志；应用自身轮转配置见 README。反馈问题时隐藏令牌、上游密码/地址和用户隐私，不公开整个数据目录。
- 镜像已带管理页面，不要随意挂载旧 `public/` 覆盖新资源。应用不在本机执行视频转码；代理模式仍消耗本机带宽，硬件需求由条目数、并发和取数策略决定，而非只看上游数量。

### 镜像部署重置管理员密码

优先使用管理面板正常改密。忘记密码时先做备份、停止所有写入实例，再在一次性容器里调用同版本 CLI；不是直接编辑密码哈希或删 tokens 文件：

```bash
cid=$(dc ps --all -q emby-in-one) || exit 1
dc stop -t 60 emby-in-one || exit 1
test "$(docker inspect --format '{{.State.Status}}' "$cid")" = exited || { echo 'Container is not stopped' >&2; exit 1; }
# 确认已停止，且没有其他实例写同一份数据后继续。
read -r -s -p 'New administrator password: ' new_password; printf '\n'
if printf '%s\n' "$new_password" | dc run --rm --no-deps -T emby-in-one /app/emby-in-one --reset-password -; then
  unset new_password
  dc up -d --no-build
else
  unset new_password
  echo 'Password reset failed; inspect the error before restarting.' >&2
fi
```

密码不放命令行参数或 shell 历史；不要开启 shell xtrace。重置会撤销代理令牌，客户端需要重新登录。不要用 `--force` 绕过仍在运行实例的保护。

## 5. 备份、升级与回滚

### 5.1 升级前备份（也适用于本地构建）

记录旧镜像引用/digest、版本与 Compose/.env 配置。暂停所有使用这些数据的写入实例；不要运行中只复制主 `mappings.db`，也不要随意删除 WAL/SHM 文件。下面假设只有这一个 EIO 实例：

```bash
stamp=$(date -u +%Y%m%dT%H%M%SZ)
cid=$(dc ps -q emby-in-one)
old_id=$(docker inspect --format '{{.Image}}' "$cid")
docker image tag "$old_id" "emby-in-one-rollback:$stamp"
sudo install -d -m 700 /opt/emby-in-one-backups
dc stop -t 60 emby-in-one || exit 1
# 若没有成功停止，先排查，不要继续备份。
test "$(docker inspect --format '{{.State.Status}}' "$cid")" = exited || { echo 'Container is not stopped' >&2; exit 1; }
sudo sh -c 'umask 077; tar -C /opt/emby-in-one -czf "$1" config data docker-compose.image.yml .env.image' \
  sh "/opt/emby-in-one-backups/$stamp.tar.gz"
dc up -d --no-build
```

核验备份可列出、可恢复，并保存到独立安全位置；包含账号凭据和令牌，不上传 Issue。目录重命名、Compose 项目名改变也可能创建新容器，部署时保持项目身份一致。

### 5.2 更新镜像

1. 阅读目标更新日志，确认是稳定版还是候选版，检查数据库兼容性和架构。
2. 完成上述备份，保留旧镜像；不要先 `prune`。
3. 把 `.env.image` 的 `EIO_IMAGE` 改为目标**已核验** digest；保存其他变量。
4. 执行 `dc config --quiet`、`dc config --images`、`dc pull`，失败则停止，不运行后续命令。
5. 执行 `dc up -d --no-build`，核验版本、健康、登录、媒体库和普通用户授权。播放验收需使用自己有权测试的片源，仅做小范围验证。
6. 留出观察时间，确认无问题才清理旧镜像和过期备份。

### 5.3 回滚

手工 Compose 路线（不经过新版 manager）没有本项目专用的自动回滚。新镜像启动失败时先保存脱敏日志，再停服务，将 `EIO_IMAGE` 改回旧 digest，或本机保留的 `emby-in-one-rollback:<备份时间>`，用 `dc up -d --no-build` 恢复（本地回滚标签不需要 pull）。

**应用回滚不等于数据回滚。** 不承诺旧程序兼容新数据库；有迁移或兼容性不明时，将当前失败状态另存后，从升级前的配套备份恢复配置、数据和部署文件，再启动旧镜像。先停止所有实例，在隔离目录核验备份；不要把旧数据解压覆盖到仍在运行的新版本中，也不要混用两个时间点的 config/data。

## 6. 本地构建：保留，但不要冒充拉镜像

仓库的 `docker-compose.yml` 是本地构建模板。完整构建上下文需含 `go.mod`、`cmd/`、`internal/`、`third_party/`、`public/` 和 Dockerfile；不要只复制 Go 文件。编译 Go/C 发生在 builder 容器中，宿主不要求装 Go，但需要构建资源和基础镜像、Debian 软件源网络。

在已审阅、干净的源码树（发布 tag 或记录下来的提交）中准备 config/data 后：

```bash
# 用实际检出的源码身份命名本地产物，不要对任意源码硬填一个发布版本。
VERSION="local-$(git rev-parse --short=12 HEAD)"
docker compose config --quiet
docker compose build --build-arg VERSION="$VERSION"
docker compose up -d --no-build
docker compose exec -T emby-in-one /app/emby-in-one --version
```

正式 tag 构建可显式传入匹配的小写 `v` 版本。当前模板以 `${EIO_VERSION:-dev}` 注入构建版本；未经核验的源码不继承旧发布版本。开发构建应记录是否有未提交修改，base image、apt 包和工具链也未锁定到 digest，因此相同源码重建不保证字节一致。

新版 manager 保持原 source/image 路线，目标准备好后才停服，保存配套数据快照并验收健康和身份；失败按第 3.0 节恢复。旧自定义模板保留且不自动迁移。手工源码 Compose 仍须人工核对源码/版本，不会被管理器悄悄重写；不能把任何本地版本字符串当作 GHCR 标签存在的证据。

`install.sh` 是 Linux root 级源码安装入口，可能安装 Docker/Compose、写 `/opt/emby-in-one` 和管理命令。请审阅固定来源后运行；从远程取源码时必须提供匹配 tarball 的 `SOURCE_SHA256`，不建议使用 `ALLOW_UNVERIFIED_SOURCE=true`。本文所述脚本修正以当前 main 为准，旧 tag/已发布压缩包不会自动更新。自建生产镜像优先按上述手动构建步骤验证，不要把运行安装脚本当作无副作用的检查。

## 7. 迁移、卸载与常见故障

- **源码构建切换到镜像**：先备份并记录 config/data 的真实路径和 server ID；停旧容器，复制并核验相同数据和属主，在独立部署目录使用本文镜像 Compose 启动。改宿主端口测试时也不要让两个实例同时写同一个数据库。验证通过后才移除旧部署；反向迁移同理。
- **卸载**：`dc down` 移除当前项目容器/网络，不会自动删除本文 bind mount 数据。保留备份和目录即可重装。不要把 `down -v` 当作备份或清理数据库的方法；使用 named volumes 的自定义部署可能因此丢数据。删除数据或镜像必须单独确认。
- **`manifest unknown` / `not found`**：先确认仓库/标签/包可见性；Release 存在不代表镜像已发布。不要靠把版本改成 latest 掩盖问题。
- **`no matching manifest` / `exec format error`**：核对宿主 CPU 和镜像平台；不要靠强制 `platform: linux/amd64` 假装 ARM 原生支持，模拟运行需独立评估。
- **`permission denied` / 重启循环**：检查 config/data UID、目录写权限、SELinux 标签或 rootless 映射。SELinux 挂载需管理员按环境配置 `:Z` 等标签，不要一律关闭 SELinux。
- **健康失败但容器运行**：检查配置端口、挂载配置文件、健康检查 URL、首次哈希/数据库初始化耗时及日志；不盲目提高重试或超时。
- **上游访问异常**：容器里的 localhost 指容器自己；宿主 VPN/代理规则未必覆盖 Docker 网络。优先检查 DNS、路由和应用配置，不使用关闭 TLS 验证等不安全绕过。
- **年龄限制后慢/空/失败**：记录账号策略、客户端、接口、耗时和脱敏错误；当前有取数与核验开销/兼容性限制，不能靠取消权限检查或全切 `pagedScan` 解决。
