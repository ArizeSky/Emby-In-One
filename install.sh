#!/usr/bin/env bash
set -e

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

cleanup() {
  local exit_code=$?
  if [[ "$_ROLLBACK_NEEDED" == true && $exit_code -ne 0 ]]; then
    warn "安装失败，正在回滚..."
    cd / 2>/dev/null || true
    if [[ -f "${PROJECT_DIR}/docker-compose.yml" ]]; then
      docker compose -f "${PROJECT_DIR}/docker-compose.yml" down --remove-orphans 2>/dev/null || true
    fi
    rm -rf "${PROJECT_DIR}"
    echo -e "${RED}[错误]${NC} 安装已回滚，残留文件已清理。请查看上方错误信息后重试。"
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
    COMPOSE_VERSION=$(curl -s https://api.github.com/repos/docker/compose/releases/latest | grep tag_name | cut -d'"' -f4)
    curl -fsSL "https://github.com/docker/compose/releases/download/${COMPOSE_VERSION}/docker-compose-$(uname -s)-$(uname -m)" \
      -o /usr/local/bin/docker-compose
    chmod +x /usr/local/bin/docker-compose
  fi
  info "Docker Compose 安装完成"
fi

# ── 4. 创建项目目录 ──
info "创建项目目录: ${PROJECT_DIR}"
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
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata && rm -rf /var/lib/apt/lists/*
WORKDIR /app
RUN mkdir -p /app/config /app/data /app/public
COPY public ./public
COPY --from=builder /out/emby-in-one ./emby-in-one
EXPOSE 8096
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
        VERSION: v1.4.2
    container_name: emby-in-one
    ports:
      - "8096:8096"
    volumes:
      - ./config:/app/config
      - ./data:/app/data
    restart: unless-stopped
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
  ADMIN_PASS=$(tr -dc 'A-Za-z0-9' </dev/urandom | head -c 16)
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
  login: 10000
  healthCheck: 10000
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
find "${PROJECT_DIR}" -type f -exec chmod 644 {} +
chmod 600 "${PROJECT_DIR}/config/config.yaml" 2>/dev/null || true

# ── 9. 启动容器 ──
info "构建并启动容器..."
cd "${PROJECT_DIR}"
docker compose build --quiet
docker compose up -d

# 安装成功，禁用回滚
_ROLLBACK_NEEDED=false

# ── 10. 打印凭据 ──
SERVER_IP=$(curl -4 -s --max-time 5 ip.sb 2>/dev/null || echo '<服务器IP>')
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

