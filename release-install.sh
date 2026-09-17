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
for cmd in curl grep sed sha256sum; do
  if ! command -v "$cmd" &>/dev/null; then
    error "缺少必要工具: ${cmd}，请先安装"
  fi
done

# ── 校验和验证 ──
# 发布流程必须为每个产物生成同名的 .sha256 文件，例如:
#   sha256sum Emby-In-One-linux-amd64-v1.4.5 > Emby-In-One-linux-amd64-v1.4.5.sha256
#   sha256sum admin.html admin.js emby-in-one-cli.sh > <各自同名>.sha256
verify_sha256() {
  local file="$1" url="$2"
  # 校验和文件里记录的是发布产物名，即 URL 的最后一段
  local asset_name
  asset_name=$(basename "${url}")
  local sha_file="${file}.sha256"
  if ! curl -fsSL --max-time 30 -o "${sha_file}" "${url}.sha256" 2>/dev/null; then
    rm -f "${sha_file}"
    warn "未找到校验和文件: ${url}.sha256"
    warn "该 Release 未附带校验和产物，无法确认下载内容是否被篡改，已跳过校验"
    return 0
  fi
  local expected actual
  # 优先取文件名匹配的那一行，兼容"单文件"和"多文件合并"两种校验和格式
  expected=$(awk -v want="${asset_name}" '
    { name=$2; sub(/^\*/, "", name); sub(/^\.\//, "", name) }
    name == want { print $1; exit }
  ' "${sha_file}" | grep -oE '^[0-9a-fA-F]{64}$' | head -1)
  if [[ -z "$expected" ]]; then
    expected=$(awk '{print $1}' "${sha_file}" | grep -oE '^[0-9a-fA-F]{64}$' | head -1)
  fi
  rm -f "${sha_file}"
  if [[ -z "$expected" ]]; then
    error "校验和文件格式无法识别: ${url}.sha256\n  已中止安装。"
  fi
  actual=$(sha256sum "${file}" | awk '{print $1}')
  if [[ "${expected,,}" != "${actual,,}" ]]; then
    error "校验失败！下载内容可能已损坏或被篡改。\n  文件: ${url}\n  期望: ${expected}\n  实际: ${actual}"
  fi
  info "完整性校验通过: ${asset_name}"
}

# 下载并校验单个产物；先落到临时文件，校验通过后才原子替换目标文件，
# 这样下载失败不会破坏磁盘上已有的可用副本（例如已安装的 CLI 脚本）
download_verified() {
  local url="$1" dest="$2"
  shift 2
  local tmp="${dest}.download.$$"
  if ! curl -fsSL --max-time 180 "$@" -o "${tmp}" "${url}"; then
    rm -f "${tmp}"
    return 1
  fi
  verify_sha256 "${tmp}" "${url}"
  mv -f "${tmp}" "${dest}"
}

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
# 目录已存在即视为已有安装: Docker 部署（或上次安装失败残留）的目录里没有
# emby-in-one 二进制，只按二进制判断会把它们误判为全新安装，
# 回滚时整目录删除，用户的 config/data 一并丢失。
IS_UPGRADE=false
_DIR_PREEXISTED=false
if [[ -d "${PROJECT_DIR}" ]]; then
  _DIR_PREEXISTED=true
  IS_UPGRADE=true
  warn "检测到已有安装目录 ${PROJECT_DIR}，将执行覆盖安装升级"
fi

# ── 回滚机制 ──
_ROLLBACK_NEEDED=false
_BINARY_INSTALLED=false
_UNIT_BACKED_UP=false
_UNIT_INSTALLED=false
_SERVICE_WAS_RUNNING=false
_MIGRATED_FROM_ROOT=false

cleanup() {
  local exit_code=$?
  if [[ "$_ROLLBACK_NEEDED" != true || $exit_code -eq 0 ]]; then
    return
  fi
  warn "安装失败，正在回滚..."
  rm -f /tmp/emby-in-one-install-$$* 2>/dev/null || true
  if [[ "$IS_UPGRADE" == true ]]; then
    # 已有安装: 只恢复本次动过的文件，绝不删除 config/data/log
    if [[ -f "${PROJECT_DIR}/emby-in-one.bak" ]]; then
      mv -f "${PROJECT_DIR}/emby-in-one.bak" "${PROJECT_DIR}/emby-in-one"
      info "已恢复旧版本二进制"
    elif [[ "$_BINARY_INSTALLED" == true ]]; then
      rm -f "${PROJECT_DIR}/emby-in-one"
      info "已移除本次写入的二进制"
    fi
  else
    # 全新安装: 清理本次新建的目录
    rm -rf "${PROJECT_DIR}"
    info "已清理本次安装创建的目录"
  fi
  # 还原/清理 systemd unit。必须放在"重新启动服务"之前:
  # 旧部署的 unit 是 User=root，先恢复它再启动才不会再撞上非 root 的目录属主。
  if [[ "$_UNIT_BACKED_UP" == true && -f "/etc/systemd/system/${SERVICE_NAME}.service.bak" ]]; then
    mv -f "/etc/systemd/system/${SERVICE_NAME}.service.bak" "/etc/systemd/system/${SERVICE_NAME}.service"
    if command -v systemctl &>/dev/null; then
      systemctl daemon-reload 2>/dev/null || true
    fi
    info "已还原原有 systemd 服务文件"
  elif [[ "$_UNIT_INSTALLED" == true ]]; then
    rm -f "/etc/systemd/system/${SERVICE_NAME}.service"
    if command -v systemctl &>/dev/null; then
      systemctl daemon-reload 2>/dev/null || true
    fi
    info "已移除本次生成的 systemd 服务文件"
  fi
  if [[ "$IS_UPGRADE" == true ]]; then
    # 升级过程中服务已被停止，回滚后必须重新拉起，否则用户侧一直停机
    if [[ "$_SERVICE_WAS_RUNNING" == true ]] && command -v systemctl &>/dev/null; then
      if systemctl start "${SERVICE_NAME}" 2>/dev/null; then
        info "已重新启动服务"
      else
        warn "旧版本服务启动失败，请手动执行: systemctl start ${SERVICE_NAME}"
      fi
    fi
    warn "原有 config/ data/ log/ 目录已保留"
  fi
  echo -e "${RED}[错误]${NC} 安装已回滚。请查看上方错误信息后重试。"
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

# ── 下载二进制（下载后校验 sha256） ──
info "正在下载 ${BINARY_NAME}..."
TMP_FILE="/tmp/emby-in-one-install-$$"
if ! download_verified "${DOWNLOAD_URL}" "${TMP_FILE}" --progress-bar; then
  error "下载失败！\n  请检查版本 ${VERSION_TAG} 和架构 ${ARCH} 是否存在该 release。\n  下载地址: ${DOWNLOAD_URL}"
fi

# ── 升级时备份 ──
if [[ "$IS_UPGRADE" == true ]]; then
  # 停止运行中的服务
  if systemctl is-active --quiet "${SERVICE_NAME}" 2>/dev/null; then
    info "正在停止服务..."
    systemctl stop "${SERVICE_NAME}"
    _SERVICE_WAS_RUNNING=true
  fi
  # 已有安装目录里可能没有二进制（例如原先是 Docker 部署）
  if [[ -f "${PROJECT_DIR}/emby-in-one" ]]; then
    cp "${PROJECT_DIR}/emby-in-one" "${PROJECT_DIR}/emby-in-one.bak"
    info "已备份旧版可执行文件"
  fi
fi

# ── 安装二进制 ──
mv "${TMP_FILE}" "${PROJECT_DIR}/emby-in-one"
chmod +x "${PROJECT_DIR}/emby-in-one"
_BINARY_INSTALLED=true
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
  if download_verified "${ASSET_URL}" "${PROJECT_DIR}/public/${ASSET_FILE}" -s; then
    continue
  fi
  # 不做无校验的 main 分支回退: admin.js 是管理面板前端，落盘后会优先于二进制内嵌
  # 副本，被篡改的文件等于直接控制管理面板。Release 缺产物时删除磁盘文件，
  # 让二进制内嵌的完整面板生效（磁盘上缺哪个文件就回退到内嵌副本）。
  rm -f "${PROJECT_DIR}/public/${ASSET_FILE}"
  warn "未在 Release ${RELEASE_TAG} 中找到 ${ASSET_FILE}，将使用二进制内嵌副本"
done

# ── 安装 SSH 管理脚本 ──
install_cli_script() {
  chmod +x "${PROJECT_DIR}/emby-in-one-cli.sh"
  cp "${PROJECT_DIR}/emby-in-one-cli.sh" /usr/local/bin/emby-in-one
  chmod +x /usr/local/bin/emby-in-one
}

CLI_URL="https://github.com/${GITHUB_REPO}/releases/download/${RELEASE_TAG}/emby-in-one-cli.sh"
if download_verified "${CLI_URL}" "${PROJECT_DIR}/emby-in-one-cli.sh" -s; then
  install_cli_script
  info "SSH 管理菜单已安装 (使用 'emby-in-one' 命令即可呼出)"
else
  # 同样不做无校验的 main 分支回退（该脚本之后会以 root 身份被执行）。
  # download_verified 失败不会破坏磁盘上已有的副本；升级场景下保留旧版并继续安装，
  # 全新安装则留待手动补充。
  if [[ -f "${PROJECT_DIR}/emby-in-one-cli.sh" ]]; then
    install_cli_script
    warn "未在 Release ${RELEASE_TAG} 中找到 emby-in-one-cli.sh，保留磁盘上已有的副本"
  else
    warn "SSH 管理脚本拉取失败。之后可手动补充。"
  fi
fi

# ── 把本脚本留在安装目录 ──
# SSH 菜单更新时优先复用这份磁盘副本，避免每次更新都从网络拉取脚本。
if [[ -f "$0" ]]; then
  cp -f "$0" "${PROJECT_DIR}/release-install.sh"
  chmod +x "${PROJECT_DIR}/release-install.sh"
fi

# ── 创建专用运行用户（服务不以 root 运行） ──
SERVICE_USER="eio"
if ! getent group "${SERVICE_USER}" &>/dev/null && command -v groupadd &>/dev/null; then
  groupadd -r "${SERVICE_USER}" 2>/dev/null || true
fi
if ! id -u "${SERVICE_USER}" &>/dev/null; then
  if command -v useradd &>/dev/null; then
    useradd -r -s /usr/sbin/nologin "${SERVICE_USER}" 2>/dev/null \
      || useradd -r -s /sbin/nologin "${SERVICE_USER}" 2>/dev/null \
      || error "无法创建专用运行用户 ${SERVICE_USER}"
    info "已创建专用运行用户: ${SERVICE_USER}"
  else
    error "缺少 useradd，无法创建专用运行用户 ${SERVICE_USER}"
  fi
fi

# 升级既有 root 部署时，把属主迁移到专用用户（含 config/data/log）
_OLD_OWNER=$(stat -c '%U' "${PROJECT_DIR}" 2>/dev/null || echo "")
if [[ "$_DIR_PREEXISTED" == true && -n "$_OLD_OWNER" && "$_OLD_OWNER" != "${SERVICE_USER}" ]]; then
  _MIGRATED_FROM_ROOT=true
  info "检测到旧部署属主为 ${_OLD_OWNER}，正在迁移到 ${SERVICE_USER}"
fi
if ! chown -R "${SERVICE_USER}:${SERVICE_USER}" "${PROJECT_DIR}" 2>/dev/null; then
  chown -R "${SERVICE_USER}" "${PROJECT_DIR}" 2>/dev/null \
    || error "无法将 ${PROJECT_DIR} 属主改为 ${SERVICE_USER}"
fi

# ── 创建 systemd 服务 ──
if command -v systemctl &>/dev/null; then
  # 备份旧 unit，回滚时可原样还原
  if [[ -f "/etc/systemd/system/${SERVICE_NAME}.service" ]]; then
    cp -f "/etc/systemd/system/${SERVICE_NAME}.service" "/etc/systemd/system/${SERVICE_NAME}.service.bak"
    _UNIT_BACKED_UP=true
  fi
  cat > /etc/systemd/system/${SERVICE_NAME}.service << EOF
[Unit]
Description=Emby In One Aggregator
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${SERVICE_USER}
WorkingDirectory=${PROJECT_DIR}
ExecStart=${PROJECT_DIR}/emby-in-one
Restart=on-failure
RestartSec=5
LimitNOFILE=65536

# 安全加固
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=${PROJECT_DIR}

[Install]
WantedBy=multi-user.target
EOF
  _UNIT_INSTALLED=true
  systemctl daemon-reload
  systemctl enable "${SERVICE_NAME}" >/dev/null 2>&1
  info "systemd 服务已配置并设为开机启动（运行用户: ${SERVICE_USER}）"
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
  # 非 systemd 环境同样尽量降权运行
  if command -v runuser &>/dev/null; then
    nohup runuser -u "${SERVICE_USER}" -- ./emby-in-one > "${PROJECT_DIR}/log/stdout.log" 2>&1 &
    info "服务已在后台以 ${SERVICE_USER} 身份启动 (PID: $!)"
  else
    nohup ./emby-in-one > "${PROJECT_DIR}/log/stdout.log" 2>&1 &
    warn "未找到 runuser，服务以 root 身份在后台启动"
    info "服务已在后台启动 (PID: $!)"
  fi
fi

_ROLLBACK_NEEDED=false

# 安装成功，清理本次的 unit 备份
rm -f "/etc/systemd/system/${SERVICE_NAME}.service.bak"

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

# 取本机出口地址（不查询第三方 ip.sb，避免把服务器 IP 泄露给外部服务）
PUBLIC_IP=$(ip route get 1.1.1.1 2>/dev/null | grep -oE 'src [0-9.]+' | awk '{print $2}' | head -1)
if [[ -z "$PUBLIC_IP" ]]; then
  PUBLIC_IP=$(hostname -I 2>/dev/null | awk '{print $1}')
fi
PUBLIC_IP=${PUBLIC_IP:-your-server-ip}

echo -e "  ${BOLD}版本号${NC}         ${VERSION_TAG}"
echo -e "  ${BOLD}安装目录${NC}       ${PROJECT_DIR}"
echo -e "  ${BOLD}运行用户${NC}       ${SERVICE_USER} (非 root)"
echo -e "  ${BOLD}用户访问地址${NC}   ${GREEN}http://${PUBLIC_IP}:${PORT}${NC}"
echo -e "  ${BOLD}管理面板地址${NC}   ${GREEN}http://${PUBLIC_IP}:${PORT}/admin${NC}"
if [[ "$PUBLIC_IP" =~ ^(10\.|127\.|192\.168\.|172\.(1[6-9]|2[0-9]|3[01])\.) ]]; then
  echo -e "  ${YELLOW}提示${NC}             检测到内网地址（NAT / 端口映射环境），请以实际公网地址访问"
fi
if [[ "$_MIGRATED_FROM_ROOT" == true ]]; then
  echo ""
  echo -e "  ${YELLOW}说明${NC}             本次升级已把 ${PROJECT_DIR} 属主从 ${_OLD_OWNER} 迁移到 ${SERVICE_USER}，"
  echo -e "                   服务不再以 root 运行；如有外部脚本以 root 读写 config/ data/ 需自行调整"
fi

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
