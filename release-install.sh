#!/usr/bin/env bash
set -e

# ╔════════════════════════════════════════════════════╗
# ║       Emby In One (Go) Release 一键安装脚本         ║
# ║       https://github.com/ArizeSky/Emby-In-One     ║
# ╚════════════════════════════════════════════════════╝

GITHUB_REPO="ArizeSky/Emby-In-One"
PROJECT_DIR="/opt/emby-in-one"
SERVICE_NAME="emby-in-one"
DEFAULT_PORT=8096

# ── 颜色 ──
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BOLD='\033[1m'
DIM='\033[2m'
NC='\033[0m'

info()  { echo -e "${GREEN}[信息]${NC} $*"; }
warn()  { echo -e "${YELLOW}[警告]${NC} $*"; }
error() { echo -e "${RED}[错误]${NC} $*"; exit 1; }

# ── 检测操作系统 ──
if [[ "$(uname -s)" != "Linux" ]]; then
  error "本脚本仅支持 Linux 系统"
fi

if [[ "$EUID" -ne 0 ]]; then
  error "请使用 root 权限运行此脚本 (sudo bash release-install.sh)"
fi

# ── 检测架构 ──
detect_arch() {
  local machine
  machine=$(uname -m)
  case "$machine" in
    x86_64|amd64)   echo "amd64" ;;
    aarch64|arm64)   echo "arm64" ;;
    armv7*|armv6*)   echo "arm" ;;
    mips)            echo "mips" ;;
    mipsel|mipsle)   echo "mipsle" ;;
    riscv64)         echo "riscv64" ;;
    *)               echo "" ;;
  esac
}

ARCH=$(detect_arch)
if [[ -z "$ARCH" ]]; then
  error "不支持的系统架构: $(uname -m)"
fi

info "系统架构: ${ARCH}"

# ── 检测依赖 ──
for cmd in curl grep sed; do
  if ! command -v "$cmd" &>/dev/null; then
    error "缺少必要工具: ${cmd}，请先安装"
  fi
done

# ── 检测版本（参数或自动获取最新） ──
if [[ -n "$1" ]]; then
  VERSION_TAG="$1"
  info "指定安装版本: ${VERSION_TAG}"
else
  info "正在获取最新稳定版本..."
  VERSION_TAG=$(curl -sL --max-time 15 "https://api.github.com/repos/${GITHUB_REPO}/releases/latest" 2>/dev/null | grep -o '"tag_name" *: *"[^"]*"' | head -1 | sed 's/.*: *"//;s/"$//')
  if [[ -z "$VERSION_TAG" ]]; then
    error "无法获取最新版本信息，请检查网络连接。\n  如果处于 Pre-release 测试期，请指定版本安装: bash release-install.sh V1.3.0"
  fi
  info "最新稳定版本: ${VERSION_TAG}"
fi

# ── 处理大小写差异（GitHub Tag 为 V，文件名 为 v） ──
# 确保 URL 的 Tag 总是大写 V 开头
RELEASE_TAG="$VERSION_TAG"
if [[ "$RELEASE_TAG" =~ ^v(.*) ]]; then
  RELEASE_TAG="V${BASH_REMATCH[1]}"
elif [[ ! "$RELEASE_TAG" =~ ^[Vv] ]]; then
  RELEASE_TAG="V${RELEASE_TAG}"
fi

# 确保文件名的版本总是小写 v 开头
FILE_VERSION="$RELEASE_TAG"
if [[ "$FILE_VERSION" =~ ^V(.*) ]]; then
  FILE_VERSION="v${BASH_REMATCH[1]}"
fi

# 根据 build 目录里的命名规范构建二进制文件名
BINARY_NAME="Emby-In-One-linux-${ARCH}-${FILE_VERSION}"
DOWNLOAD_URL="https://github.com/${GITHUB_REPO}/releases/download/${RELEASE_TAG}/${BINARY_NAME}"

# ── 升级检测 ──
IS_UPGRADE=false
if [[ -d "${PROJECT_DIR}" && -f "${PROJECT_DIR}/emby-in-one" ]]; then
  IS_UPGRADE=true
  warn "检测到已有安装，将执行覆盖安装升级"
fi

# ── 回滚机制 ──
_ROLLBACK_NEEDED=false

cleanup() {
  local exit_code=$?
  if [[ "$_ROLLBACK_NEEDED" == true && $exit_code -ne 0 ]]; then
    warn "安装失败，正在回滚..."
    if [[ "$IS_UPGRADE" == true && -f "${PROJECT_DIR}/emby-in-one.bak" ]]; then
      mv "${PROJECT_DIR}/emby-in-one.bak" "${PROJECT_DIR}/emby-in-one"
      info "已恢复旧版本二进制"
    elif [[ "$IS_UPGRADE" == false ]]; then
      rm -rf "${PROJECT_DIR}"
    fi
    echo -e "${RED}[错误]${NC} 安装已回滚。请查看上方错误信息后重试。"
  fi
}

trap cleanup EXIT

# ── 开始安装 ──
echo ""
echo -e "${BOLD}${CYAN}╔════════════════════════════════════════════════════╗${NC}"
echo -e "${BOLD}${CYAN}║        Emby In One (Release) 安装程序  ${VERSION_TAG}    ║${NC}"
echo -e "${BOLD}${CYAN}╚════════════════════════════════════════════════════╝${NC}"
echo ""

_ROLLBACK_NEEDED=true

# ── 创建目录 ──
mkdir -p "${PROJECT_DIR}"/{config,data,log}

# ── 下载二进制 ──
info "正在下载 ${BINARY_NAME}..."
TMP_FILE="/tmp/emby-in-one-install-$$"
if ! curl -fSL --max-time 180 --progress-bar -o "${TMP_FILE}" "${DOWNLOAD_URL}"; then
  rm -f "${TMP_FILE}"
  error "下载失败！\n  请检查版本 ${VERSION_TAG} 和架构 ${ARCH} 是否存在该 release。\n  下载地址: ${DOWNLOAD_URL}"
fi

# ── 升级时备份 ──
if [[ "$IS_UPGRADE" == true ]]; then
  # 停止运行中的服务
  if systemctl is-active --quiet "${SERVICE_NAME}" 2>/dev/null; then
    info "正在停止服务..."
    systemctl stop "${SERVICE_NAME}"
  fi
  cp "${PROJECT_DIR}/emby-in-one" "${PROJECT_DIR}/emby-in-one.bak"
  info "已备份旧版可执行文件"
fi

# ── 安装二进制 ──
mv "${TMP_FILE}" "${PROJECT_DIR}/emby-in-one"
chmod +x "${PROJECT_DIR}/emby-in-one"
info "二进制文件已成功安装到 ${PROJECT_DIR}/emby-in-one"

# ── 生成默认配置（仅首次安装） ──
if [[ ! -f "${PROJECT_DIR}/config/config.yaml" ]]; then
  ADMIN_PASS=$(head -c 24 /dev/urandom | base64 | tr -d '/+=' | head -c 16)
  cat > "${PROJECT_DIR}/config/config.yaml" << EOF
server:
  port: ${DEFAULT_PORT}
  name: "Emby-In-One"

admin:
  username: "admin"
  password: '${ADMIN_PASS}'

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
  chmod 600 "${PROJECT_DIR}/config/config.yaml"
  info "已生成默认配置（管理员密码将在下方显示）"
fi

# ── 确保 public 目录文件存在（管理面板） ──
if [[ ! -d "${PROJECT_DIR}/public" ]]; then
  mkdir -p "${PROJECT_DIR}/public"
fi
info "正在获取配套资源文件..."
for ASSET_FILE in admin.html admin.js; do
  ASSET_URL="https://github.com/${GITHUB_REPO}/releases/download/${RELEASE_TAG}/${ASSET_FILE}"
  if ! curl -fsSL --max-time 30 -o "${PROJECT_DIR}/public/${ASSET_FILE}" "${ASSET_URL}" 2>/dev/null; then
    warn "未在 Release ${RELEASE_TAG} 中找到 ${ASSET_FILE}，尝试从 main 分支拉取..."
    ASSET_MAIN_URL="https://raw.githubusercontent.com/${GITHUB_REPO}/main/public/${ASSET_FILE}"
    if ! curl -fsSL --max-time 30 -o "${PROJECT_DIR}/public/${ASSET_FILE}" "${ASSET_MAIN_URL}" 2>/dev/null; then
      if [[ "$ASSET_FILE" == "admin.html" ]]; then
        cat > "${PROJECT_DIR}/public/admin.html" << 'HTMLEOF'
<!DOCTYPE html>
<html><head><meta charset="UTF-8"><title>Emby-in-One</title></head>
<body><h1>Emby-in-One</h1><p>管理面板下载失败。请稍候手动将 public/admin.html 下载到所需目录。</p></body></html>
HTMLEOF
      fi
      warn "${ASSET_FILE} 拉取失败，界面可能不完整"
    fi
  fi
done

# ── 安装 SSH 管理脚本 ──
CLI_URL="https://github.com/${GITHUB_REPO}/releases/download/${RELEASE_TAG}/emby-in-one-cli.sh"
if curl -fsSL --max-time 30 -o "${PROJECT_DIR}/emby-in-one-cli.sh" "${CLI_URL}" 2>/dev/null; then
  chmod +x "${PROJECT_DIR}/emby-in-one-cli.sh"
  cp "${PROJECT_DIR}/emby-in-one-cli.sh" /usr/local/bin/emby-in-one
  chmod +x /usr/local/bin/emby-in-one
  info "SSH 管理菜单已安装 (使用 'emby-in-one' 命令即可呼出)"
else
  # 同样尝试回退拉取 main 分支的脚本
  CLI_MAIN_URL="https://raw.githubusercontent.com/${GITHUB_REPO}/main/emby-in-one-cli.sh"
  if curl -fsSL --max-time 30 -o "${PROJECT_DIR}/emby-in-one-cli.sh" "${CLI_MAIN_URL}" 2>/dev/null; then
    chmod +x "${PROJECT_DIR}/emby-in-one-cli.sh"
    cp "${PROJECT_DIR}/emby-in-one-cli.sh" /usr/local/bin/emby-in-one
    chmod +x /usr/local/bin/emby-in-one
    info "已从 main 下载 SSH 管理菜单 (使用 'emby-in-one' 命令即可呼出)"
  else
    warn "SSH 管理脚本拉取失败。之后可手动补充。"
  fi
fi

# ── 创建 systemd 服务 ──
if command -v systemctl &>/dev/null; then
  cat > /etc/systemd/system/${SERVICE_NAME}.service << EOF
[Unit]
Description=Emby In One Aggregator
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
WorkingDirectory=${PROJECT_DIR}
ExecStart=${PROJECT_DIR}/emby-in-one
Restart=on-failure
RestartSec=5
LimitNOFILE=65536

# 安全加固
NoNewPrivileges=true
ProtectSystem=strict
ReadWritePaths=${PROJECT_DIR}

[Install]
WantedBy=multi-user.target
EOF
  systemctl daemon-reload
  systemctl enable "${SERVICE_NAME}" >/dev/null 2>&1
  info "systemd 服务已配置并设为开机启动"
fi

# ── 启动服务 ──
if command -v systemctl &>/dev/null; then
  systemctl start "${SERVICE_NAME}"
  sleep 2
  if systemctl is-active --quiet "${SERVICE_NAME}"; then
    info "服务已成功启动！"
  else
    warn "服务可能没有正常运行，请输入: systemctl status ${SERVICE_NAME} 检查原因。"
  fi
else
  cd "${PROJECT_DIR}"
  nohup ./emby-in-one > "${PROJECT_DIR}/log/stdout.log" 2>&1 &
  info "服务已在后台启动 (PID: $!)"
fi

_ROLLBACK_NEEDED=false

# ── 安装完成 ──
echo ""
echo -e "${BOLD}${GREEN}╔════════════════════════════════════════════════════╗${NC}"
echo -e "${BOLD}${GREEN}║           安装与启动完成！                          ║${NC}"
echo -e "${BOLD}${GREEN}╚════════════════════════════════════════════════════╝${NC}"
echo ""

PORT=${DEFAULT_PORT}
if [[ -f "${PROJECT_DIR}/config/config.yaml" ]]; then
  CONFIGURED_PORT=$(grep "^  port:" "${PROJECT_DIR}/config/config.yaml" 2>/dev/null | head -1 | sed 's/.*port:[[:space:]]*//' | tr -d "'\"")
  if [[ -n "$CONFIGURED_PORT" ]]; then
    PORT=$CONFIGURED_PORT
  fi
fi

# 获取公网 IP
PUBLIC_IP=$(curl -4 -s --max-time 5 ip.sb 2>/dev/null || echo "your-server-ip")

echo -e "  ${BOLD}版本号${NC}         ${VERSION_TAG}"
echo -e "  ${BOLD}安装目录${NC}       ${PROJECT_DIR}"
echo -e "  ${BOLD}用户访问地址${NC}   ${GREEN}http://${PUBLIC_IP}:${PORT}${NC}"
echo -e "  ${BOLD}管理面板地址${NC}   ${GREEN}http://${PUBLIC_IP}:${PORT}/admin${NC}"

if [[ -n "${ADMIN_PASS:-}" ]]; then
  echo ""
  echo -e "  ${BOLD}管理员账号${NC}     admin"
  echo -e "  ${BOLD}初始随机密码${NC}   ${YELLOW}${ADMIN_PASS}${NC}"
  echo -e "  ${RED}🚨 请务必保存！由于使用了哈希加密，关闭提示后无法查询，只能使用 SSH 菜单重置${NC}"
fi

echo ""
echo -e "  ${DIM}------------------- 日常用法备忘 -------------------${NC}"
echo -e "  ${DIM}1. 呼出管理菜单并重置密码: ${BOLD}emby-in-one${NC}"
echo -e "  ${DIM}2. 查看引擎实时运行情况:   journalctl -u ${SERVICE_NAME} -f${NC}"
echo -e "  ${DIM}3. 重启/启停聚合代理服务:  systemctl {start|stop|restart} ${SERVICE_NAME}${NC}"
echo ""
