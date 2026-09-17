#!/usr/bin/env bash
# pipefail: 源码 tarball 的 `curl | tar` 里 curl 被截断时，
# 退出码不能只由 tar 决定（tar 可能对截断的归档正常退出）。
set -e
set -o pipefail

# ╔════════════════════════════════════════════════════╗
# ║       Emby In One 一键安装脚本                      ║
# ╚════════════════════════════════════════════════════╝

PROJECT_DIR="/opt/emby-in-one"
# 远程安装时使用的 tarball 地址
REPO_URL="https://github.com/ArizeSky/Emby-In-One/archive/refs/heads/main.tar.gz"

# ── 颜色 ──
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

info()  { echo -e "${GREEN}[信息]${NC} $*"; }
warn()  { echo -e "${YELLOW}[警告]${NC} $*"; }
error() { echo -e "${RED}[错误]${NC} $*"; exit 1; }

is_hashed_password() {
  [[ "$1" =~ ^[0-9a-fA-F]{32}:[0-9a-fA-F]{128}$ ]]
}

format_admin_password() {
  local value="$1"
  if is_hashed_password "$value"; then
    echo "已加密存储（当前密码无法直接显示，请通过 SSH 菜单重置）"
  else
    echo "$value"
  fi
}

# ── 回滚机制 ──
_ROLLBACK_NEEDED=false
# 安装前目录是否已存在: 已存在说明是升级/重装，回滚时绝不能整目录删除，
# 否则用户的 config/ data/（上游账号、令牌、观看历史）会一起没掉。
_DIR_PREEXISTED=false
# 本次是否真的把容器拉起来过，只有拉起来过才需要 down
_CONTAINERS_STARTED=false

cleanup() {
  local exit_code=$?
  if [[ "$_ROLLBACK_NEEDED" != true || $exit_code -eq 0 ]]; then
    return
  fi
  warn "安装失败，正在回滚..."
  cd / 2>/dev/null || true
  if [[ "$_CONTAINERS_STARTED" == true && -f "${PROJECT_DIR}/docker-compose.yml" ]]; then
    docker compose -f "${PROJECT_DIR}/docker-compose.yml" down --remove-orphans 2>/dev/null || true
  fi
  if [[ "$_DIR_PREEXISTED" == false ]]; then
    rm -rf "${PROJECT_DIR}"
    echo -e "${RED}[错误]${NC} 安装已回滚，残留文件已清理。请查看上方错误信息后重试。"
  else
    echo -e "${RED}[错误]${NC} 安装已回滚。请查看上方错误信息后重试。"
    warn "${PROJECT_DIR} 在安装前已存在，原有 config/ data/ log/ 等用户数据全部保留"
  fi
}

trap cleanup EXIT

# ── 1. 检测操作系统 ──
if [[ "$(uname -s)" != "Linux" ]]; then
  error "本脚本仅支持 Linux 系统"
fi

if [[ "$EUID" -ne 0 ]]; then
  error "请使用 root 权限运行此脚本 (sudo bash install.sh)"
fi

info "检测到 Linux 系统，开始安装..."

# ── 2. 检测并安装 Docker ──
_install_docker_aliyun() {
  local pkg_mgr
  if command -v apt-get &>/dev/null; then
    pkg_mgr=apt
  elif command -v yum &>/dev/null; then
    pkg_mgr=yum
  elif command -v dnf &>/dev/null; then
    pkg_mgr=dnf
  else
    return 1
  fi

  warn "get.docker.com 安装失败，尝试阿里云镜像源..."

  if [[ "$pkg_mgr" == "apt" ]]; then
    apt-get update -qq
    apt-get install -y -qq ca-certificates curl gnupg lsb-release
    install -m 0755 -d /etc/apt/keyrings
    curl -fsSL https://mirrors.aliyun.com/docker-ce/linux/debian/gpg \
      | gpg --dearmor -o /etc/apt/keyrings/docker.gpg 2>/dev/null
    chmod a+r /etc/apt/keyrings/docker.gpg
    # 兼容 Debian 和 Ubuntu
    local distro
    if grep -qi ubuntu /etc/os-release 2>/dev/null; then
      distro=ubuntu
    else
      distro=debian
    fi
    echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.gpg] \
https://mirrors.aliyun.com/docker-ce/linux/${distro} \
$(lsb_release -cs) stable" > /etc/apt/sources.list.d/docker.list
    apt-get update -qq
    apt-get install -y -qq docker-ce docker-ce-cli containerd.io docker-compose-plugin
  elif [[ "$pkg_mgr" == "yum" || "$pkg_mgr" == "dnf" ]]; then
    "$pkg_mgr" install -y yum-utils 2>/dev/null || true
    "$pkg_mgr"-config-manager --add-repo \
      https://mirrors.aliyun.com/docker-ce/linux/centos/docker-ce.repo 2>/dev/null || true
    "$pkg_mgr" install -y docker-ce docker-ce-cli containerd.io docker-compose-plugin
  fi
}

if ! command -v docker &>/dev/null; then
  info "Docker 未安装，正在安装..."
  if curl -fsSL --max-time 60 https://get.docker.com | bash; then
    systemctl enable docker
    systemctl start docker
  else
    _install_docker_aliyun || error "Docker 安装失败，请手动安装后重试"
    systemctl enable docker
    systemctl start docker
  fi
  info "Docker 安装完成"
else
  info "Docker 已安装: $(docker --version)"
fi

# ── 3. 检测并安装 Docker Compose ──
if docker compose version &>/dev/null 2>&1; then
  info "Docker Compose (plugin) 已安装"
elif command -v docker-compose &>/dev/null; then
  info "Docker Compose (standalone) 已安装"
else
  info "Docker Compose 未安装，正在安装..."
  installed=false
  if command -v apt-get &>/dev/null; then
    apt-get update -qq && apt-get install -y -qq docker-compose-plugin && installed=true
  elif command -v yum &>/dev/null; then
    yum install -y docker-compose-plugin 2>/dev/null && installed=true || true
  elif command -v dnf &>/dev/null; then
    dnf install -y docker-compose-plugin 2>/dev/null && installed=true || true
  fi
  if [[ "$installed" == false ]]; then
    warn "包管理器安装失败，尝试下载二进制..."
    # `|| true` + 显式判空: 开启 pipefail 后接口失败会让管道非零，
    # 否则 set -e 会静默退出，看不到任何原因。
    COMPOSE_VERSION=$(curl -s --max-time 15 https://api.github.com/repos/docker/compose/releases/latest | grep tag_name | cut -d'"' -f4 || true)
    if [[ -z "$COMPOSE_VERSION" ]]; then
      error "无法获取 Docker Compose 版本号，请检查网络连接后重试（或手动安装 docker-compose）"
    fi
    curl -fsSL "https://github.com/docker/compose/releases/download/${COMPOSE_VERSION}/docker-compose-$(uname -s)-$(uname -m)" \
      -o /usr/local/bin/docker-compose
    chmod +x /usr/local/bin/docker-compose
  fi
  info "Docker Compose 安装完成"
fi

# ── 4. 创建项目目录 ──
info "创建项目目录: ${PROJECT_DIR}"
if [[ -d "${PROJECT_DIR}" ]]; then
  _DIR_PREEXISTED=true
fi
mkdir -p "${PROJECT_DIR}"
_ROLLBACK_NEEDED=true

# ── 5. 复制/下载项目文件 ──
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

copy_item() {
  local src="$1"
  local dst="$2"
  if [[ -e "$src" ]]; then
    cp -r "$src" "$dst"
  fi
}

write_runtime_dockerfile() {
  cat > "${PROJECT_DIR}/Dockerfile" <<'EOF'
FROM golang:1.23-bookworm AS builder
ARG VERSION=dev
RUN apt-get update && apt-get install -y --no-install-recommends build-essential ca-certificates && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY go.mod ./
COPY third_party ./third_party
COPY cmd ./cmd
COPY internal ./internal
COPY public ./public
RUN CGO_ENABLED=1 go build -ldflags="-s -w -X main.Version=${VERSION}" -o /out/emby-in-one ./cmd/emby-in-one

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata wget && rm -rf /var/lib/apt/lists/*
WORKDIR /app
RUN mkdir -p /app/config /app/data /app/public
COPY public ./public
COPY --from=builder /out/emby-in-one ./emby-in-one
# 非 root 运行 (uid 1000，与 docker-compose.yml 的 user 一致)
RUN useradd -r -u 1000 -U -s /usr/sbin/nologin eio && chown -R eio:eio /app
USER eio
EXPOSE 8096
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8096/System/Info/Public || exit 1
CMD ["./emby-in-one"]
EOF
}

write_runtime_compose() {
  cat > "${PROJECT_DIR}/docker-compose.yml" <<'EOF'
services:
  emby-in-one:
    build:
      context: .
      args:
        VERSION: v1.4.4
    container_name: emby-in-one
    # 容器以 uid 1000 运行，挂载目录需先 chown 1000:1000（install.sh 已处理）
    user: "1000:1000"
    ports:
      - "8096:8096"
    volumes:
      - ./config:/app/config
      - ./data:/app/data
    restart: unless-stopped
    security_opt:
      - no-new-privileges:true
    logging:
      driver: json-file
      options:
        max-size: "10m"
        max-file: "3"
EOF
}

copy_distribution_layout() {
  local base="$1"
  local required=(cmd internal third_party public go.mod)
  for item in "${required[@]}"; do
    [[ -e "${base}/${item}" ]] || return 1
  done

  info "从 ${base} 复制 Go 独立发行目录文件..."
  for item in cmd internal third_party public go.mod README.md README_EN.md Update.md emby-in-one-cli.sh .dockerignore LICENSE; do
    copy_item "${base}/${item}" "${PROJECT_DIR}/"
  done
  write_runtime_dockerfile
  write_runtime_compose
  return 0
}

copy_project_files_from() {
  local base="$1"
  if copy_distribution_layout "$base"; then
    return 0
  fi
  if [[ -d "${base}/Emby-In-One-Go" ]] && copy_distribution_layout "${base}/Emby-In-One-Go"; then
    return 0
  fi
  return 1
}

if copy_project_files_from "${SCRIPT_DIR}"; then
  :
else
  if [[ "$REPO_URL" == *"<owner>"* ]]; then
    error "未找到本地项目文件，且 REPO_URL 尚未配置，无法远程安装"
  fi
  info "未找到本地项目文件，从远程下载..."
  TMP_DIR=$(mktemp -d)
  curl -fsSL "${REPO_URL}" | tar -xz -C "${TMP_DIR}" --strip-components=1
  if ! copy_project_files_from "${TMP_DIR}"; then
    rm -rf "${TMP_DIR}"
    error "下载内容中未找到可部署的 Go 项目文件"
  fi
  rm -rf "${TMP_DIR}"
fi
# ── 6. 创建数据目录 ──
mkdir -p "${PROJECT_DIR}/config"
mkdir -p "${PROJECT_DIR}/data"

# ── 7. 生成配置文件 ──
if [[ ! -f "${PROJECT_DIR}/config/config.yaml" ]]; then
  info "生成默认配置文件..."
  ADMIN_USER="admin"
  # 注意: 不要写成 `tr ... | head -c 16` — 开启 pipefail 后 tr 会因 SIGPIPE(141) 让脚本退出。
  # 这里由 head 先取定长字节（正常结束），tr 读完全部输入，cut 再截前 16 位。
  ADMIN_PASS=$(head -c 4096 /dev/urandom | tr -dc 'A-Za-z0-9' | cut -c1-16)
  cat > "${PROJECT_DIR}/config/config.yaml" <<EOF
server:
  port: 8096
  name: "Emby-In-One"

admin:
  username: "${ADMIN_USER}"
  password: "${ADMIN_PASS}"

playback:
  mode: "proxy"

timeouts:
  api: 30000
  global: 15000
  login: 30000
  healthCheck: 30000
  healthInterval: 60000

proxies: []

upstream: []
EOF
else
  info "配置文件已存在，跳过生成"
  ADMIN_USER=$(grep '  username:' "${PROJECT_DIR}/config/config.yaml" | head -1 | sed "s/^  username:[[:space:]]*//" | sed "s/^'//;s/'$//;s/^\"//;s/\"$//")
  ADMIN_PASS=$(grep '  password:' "${PROJECT_DIR}/config/config.yaml" | head -1 | sed "s/^  password:[[:space:]]*//" | sed "s/^'//;s/'$//;s/^\"//;s/\"$//")
fi

ADMIN_PASS_DISPLAY=$(format_admin_password "$ADMIN_PASS")

# ── 8. 设置权限 ──
find "${PROJECT_DIR}" -type d -exec chmod 755 {} +
# data/ 存放 tokens、捕获的客户端头、密码哈希和每用户状态，
# 文件权限由服务自己写为 0600，不要覆盖。
find "${PROJECT_DIR}" -path "${PROJECT_DIR}/data" -prune -o -type f -exec chmod 644 {} +
chmod 600 "${PROJECT_DIR}/config/config.yaml" 2>/dev/null || true
# 容器以非 root (uid 1000) 运行，bind mount 的 config/ data/ 必须归 1000 所有，
# 否则服务写不了 config.yaml 与映射数据库。
chown -R 1000:1000 "${PROJECT_DIR}/config" "${PROJECT_DIR}/data" 2>/dev/null || {
  warn "无法把 config/ data/ 属主改为 1000:1000，容器内服务可能无法写入，请手动执行:"
  warn "  chown -R 1000:1000 ${PROJECT_DIR}/config ${PROJECT_DIR}/data"
}

# ── 9. 启动容器 ──
info "构建并启动容器..."
cd "${PROJECT_DIR}"
docker compose build --quiet
docker compose up -d
_CONTAINERS_STARTED=true

# 以非 root 运行时权限不对会让容器立刻退出，这里明确提示而不是假报成功
sleep 2
CONTAINER_STATE=$(docker inspect --format '{{.State.Status}}' emby-in-one 2>/dev/null || echo "")
if [[ "$CONTAINER_STATE" != "running" ]]; then
  warn "容器当前状态: ${CONTAINER_STATE:-未知}，请检查日志: docker compose -f ${PROJECT_DIR}/docker-compose.yml logs"
fi

# 安装成功，禁用回滚
_ROLLBACK_NEEDED=false

# ── 10. 打印凭据 ──
# 取本机出口地址（不查询第三方 ip.sb，避免把服务器 IP 泄露给外部服务）
# pipefail 下 `ip`/`grep` 失败会让管道非零并触发 set -e，这里必须兜住。
SERVER_IP=""
if command -v ip &>/dev/null; then
  SERVER_IP=$(ip route get 1.1.1.1 2>/dev/null | grep -oE 'src [0-9.]+' | awk '{print $2}' | head -1 || true)
fi
if [[ -z "$SERVER_IP" ]]; then
  SERVER_IP=$(hostname -I 2>/dev/null | awk '{print $1}' || true)
fi
SERVER_IP=${SERVER_IP:-'<服务器IP>'}
echo ""
echo -e "${BOLD}╔══════════════════════════════════════════════════════╗${NC}"
echo -e "${BOLD}║         ${GREEN}Emby In One 安装完成！${NC}${BOLD}                        ║${NC}"
echo -e "${BOLD}╠══════════════════════════════════════════════════════╣${NC}"
echo -e "${BOLD}║${NC}                                                      ${BOLD}║${NC}"
echo -e "${BOLD}║${NC}  管理员用户名: ${CYAN}${ADMIN_USER}${NC}"
echo -e "${BOLD}║${NC}  管理员密码:   ${CYAN}${ADMIN_PASS_DISPLAY}${NC}"
echo -e "${BOLD}║${NC}                                                      ${BOLD}║${NC}"
echo -e "${BOLD}║${NC}  访问地址:     ${CYAN}http://${SERVER_IP}:8096${NC}"
echo -e "${BOLD}║${NC}  管理面板:     ${CYAN}http://${SERVER_IP}:8096/admin${NC}"
echo -e "${BOLD}║${NC}                                                      ${BOLD}║${NC}"
echo -e "${BOLD}║${NC}  ${YELLOW}请妥善保管以上凭据！${NC}                                ${BOLD}║${NC}"
echo -e "${BOLD}╚══════════════════════════════════════════════════════╝${NC}"
if is_hashed_password "$ADMIN_PASS"; then
  echo -e "${YELLOW}提示：当前配置中的管理员密码已加密存储，无法直接查看。如需重置，请使用 SSH 菜单 emby-in-one。${NC}"
fi
if [[ "$SERVER_IP" =~ ^(10\.|127\.|192\.168\.|172\.(1[6-9]|2[0-9]|3[01])\.) ]]; then
  echo -e "${YELLOW}提示：检测到内网地址（NAT / 端口映射环境），请以实际公网地址访问。${NC}"
fi
echo ""

# ── 11. 安装 CLI 管理脚本 ──
if [[ -e "${SCRIPT_DIR}/emby-in-one-cli.sh" ]]; then
  cp "${SCRIPT_DIR}/emby-in-one-cli.sh" /usr/local/bin/emby-in-one
  chmod +x /usr/local/bin/emby-in-one
  info "SSH 管理脚本已安装，输入 ${CYAN}emby-in-one${NC} 即可使用管理菜单"
elif [[ -e "${PROJECT_DIR}/emby-in-one-cli.sh" ]]; then
  cp "${PROJECT_DIR}/emby-in-one-cli.sh" /usr/local/bin/emby-in-one
  chmod +x /usr/local/bin/emby-in-one
  info "SSH 管理脚本已安装，输入 ${CYAN}emby-in-one${NC} 即可使用管理菜单"
fi

info "安装完成！"
