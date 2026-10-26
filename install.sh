#!/usr/bin/env bash
set -euo pipefail
umask 077
PROJECT_DIR="${EIO_PROJECT_DIR:-/opt/emby-in-one}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
MODE=source
ARGS=()
while (($#)); do
  case "$1" in
    --mode) [[ $# -ge 2 ]] || exit 2; MODE=$2; ARGS+=("$1" "$2"); shift 2;;
    --project-dir) [[ $# -ge 2 ]] || exit 2; PROJECT_DIR=$2; shift 2;;
    --help|-h) printf 'Usage: bash install.sh [--mode source|image] [--image REF] [--project-dir DIR] [--port PORT]\n'; exit 0;;
    *) ARGS+=("$1"); shift;;
  esac
done
[[ "$MODE" == source || "$MODE" == image ]] || { echo 'Invalid deployment mode' >&2; exit 2; }
[[ "$EUID" == 0 && "$(uname -s)" == Linux ]] || { echo 'Linux root required' >&2; exit 1; }
RED='' GREEN='' YELLOW='' CYAN='' BOLD='' NC=''
info(){ printf '%s\n' "$*"; }
warn(){ printf '%s\n' "$*" >&2; }
error(){ printf '%s\n' "$*" >&2; exit 1; }
printf 'Deployment route: docker-%s (no automatic fallback)\n' "$MODE"
compose_cmd(){ if docker compose version >/dev/null 2>&1; then docker compose "$@"; elif command -v docker-compose >/dev/null; then docker-compose "$@"; else error 'Compose missing'; fi; }
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
    # 包源 key 指纹独立确认: 先下载 key、校验指纹，再导入 keyring。
    # 指纹为 Docker 官方公布的 9DC8 5822 9FC7 DD38 854A E2D8 8D81 803C 0EBF CD88，
    # 镜像源分发的也是同一把 key；不匹配即中止，被替换的源 key 无法静默生效。
    local key_tmp key_fpr
    key_tmp=$(mktemp)
    if ! curl -fsSL --max-time 30 https://mirrors.aliyun.com/docker-ce/linux/debian/gpg -o "${key_tmp}"; then
      rm -f "${key_tmp}"
      return 1
    fi
    key_fpr=$(gpg --show-keys --with-colons "${key_tmp}" 2>/dev/null | awk -F: '$1=="fpr" {print $10; exit}')
    if [[ "${key_fpr}" != "9DC858229FC7DD38854AE2D88D81803C0EBFCD88" ]]; then
      rm -f "${key_tmp}"
      warn "Docker 仓库 GPG key 指纹不匹配 (实际: ${key_fpr:-未解析})，已中止镜像源安装"
      return 1
    fi
    gpg --dearmor < "${key_tmp}" > /etc/apt/keyrings/docker.gpg
    rm -f "${key_tmp}"
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
    # 同 apt 路径: 先独立校验 key 指纹再启用仓库。repo 文件里的 gpgkey 与
    # 仓库本身同源，不能作为唯一校验渠道。
    local key_tmp key_fpr
    key_tmp=$(mktemp)
    if ! curl -fsSL --max-time 30 https://mirrors.aliyun.com/docker-ce/linux/centos/gpg -o "${key_tmp}"; then
      rm -f "${key_tmp}"
      return 1
    fi
    key_fpr=$(gpg --show-keys --with-colons "${key_tmp}" 2>/dev/null | awk -F: '$1=="fpr" {print $10; exit}')
    if [[ "${key_fpr}" != "9DC858229FC7DD38854AE2D88D81803C0EBFCD88" ]]; then
      rm -f "${key_tmp}"
      warn "Docker 仓库 GPG key 指纹不匹配 (实际: ${key_fpr:-未解析})，已中止镜像源安装"
      return 1
    fi
    rpm --import "${key_tmp}"
    rm -f "${key_tmp}"
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
    # 临时文件 → 校验同源 .sha256 → 原子安装。compose 官方 Release 为每个
    # 二进制附带同名 .sha256；哈希与资产同源，能检出传输损坏与篡改，
    # 不代表官方源被攻破时仍安全。校验不过不落盘生效。
    if ! command -v sha256sum &>/dev/null; then
      error "系统缺少 sha256sum，无法校验 Docker Compose 二进制，已中止安装"
    fi
    COMPOSE_ASSET="docker-compose-$(uname -s)-$(uname -m)"
    COMPOSE_URL="https://github.com/docker/compose/releases/download/${COMPOSE_VERSION}/${COMPOSE_ASSET}"
    # 临时文件放在目标同目录: mv 在同一文件系统上是原子重命名，
    # 不会在 /usr/local/bin 留下写了一半的可执行文件
    COMPOSE_TMP="/usr/local/bin/docker-compose.download.$$"
    if ! curl -fsSL --max-time 180 -o "${COMPOSE_TMP}" "${COMPOSE_URL}"; then
      rm -f "${COMPOSE_TMP}"
      error "下载 Docker Compose 二进制失败: ${COMPOSE_URL}"
    fi
    COMPOSE_SHA_TMP="${COMPOSE_TMP}.sha256"
    if ! curl -fsSL --max-time 30 -o "${COMPOSE_SHA_TMP}" "${COMPOSE_URL}.sha256"; then
      rm -f "${COMPOSE_TMP}" "${COMPOSE_SHA_TMP}"
      error "未找到 Docker Compose 校验和文件: ${COMPOSE_URL}.sha256，无校验不安装"
    fi
    # 校验和条目必须与本资产名对应，不退回任意首行
    COMPOSE_EXPECTED=$(awk -v want="${COMPOSE_ASSET}" '
      { name=$2; sub(/^\*/, "", name); sub(/^\.\//, "", name) }
      name == want { print $1; exit }
    ' "${COMPOSE_SHA_TMP}" | grep -oE '^[0-9a-fA-F]{64}$' | head -1)
    rm -f "${COMPOSE_SHA_TMP}"
    if [[ -z "${COMPOSE_EXPECTED}" ]]; then
      rm -f "${COMPOSE_TMP}"
      error "Docker Compose 校验和文件格式无法识别（未找到 ${COMPOSE_ASSET} 的条目）"
    fi
    COMPOSE_ACTUAL=$(sha256sum "${COMPOSE_TMP}" | awk '{print $1}')
    if [[ "${COMPOSE_EXPECTED,,}" != "${COMPOSE_ACTUAL,,}" ]]; then
      rm -f "${COMPOSE_TMP}"
      error "Docker Compose 校验失败！期望 ${COMPOSE_EXPECTED}，实际 ${COMPOSE_ACTUAL}"
    fi
    mv -f "${COMPOSE_TMP}" /usr/local/bin/docker-compose
    chmod +x /usr/local/bin/docker-compose
  fi
  info "Docker Compose 安装完成"
fi


# Backward-compatible distribution template helpers.
copy_item() {
  local src="$1"
  local dst="$2"
  if [[ -e "$src" ]]; then
    cp -r "$src" "$dst"
  fi
}

write_runtime_dockerfile_to() {
  local dest="$1"
  cat > "${dest}" <<'EOF'
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

write_runtime_compose_to() {
  local dest="$1"
  cat > "${dest}" <<'EOF'
services:
  emby-in-one:
    build:
      context: .
      args:
        VERSION: ${EIO_VERSION:-dev}
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
  # 已有 Dockerfile/docker-compose.yml 不自动覆盖（用户可能改过端口/挂载/构建
  # 参数），新模板放独立 .release 文件供比对；全新安装才自动创建。
  if [[ -f "${PROJECT_DIR}/Dockerfile" ]]; then
    write_runtime_dockerfile_to "${PROJECT_DIR}/Dockerfile.release"
    warn "已有 Dockerfile 保持不变，新模板已写入 ${PROJECT_DIR}/Dockerfile.release 供比对"
  else
    write_runtime_dockerfile_to "${PROJECT_DIR}/Dockerfile"
  fi
  if [[ -f "${PROJECT_DIR}/docker-compose.yml" ]]; then
    write_runtime_compose_to "${PROJECT_DIR}/docker-compose.yml.release"
    warn "已有 docker-compose.yml 保持不变，新模板已写入 ${PROJECT_DIR}/docker-compose.yml.release 供比对"
  else
    write_runtime_compose_to "${PROJECT_DIR}/docker-compose.yml"
  fi
}


TEMP_SOURCE=''
cleanup(){ [[ -z "$TEMP_SOURCE" ]] || rm -rf "$TEMP_SOURCE"; }
trap cleanup EXIT
if [[ "$MODE" == source && ! -f "$SCRIPT_DIR/go.mod" ]]; then
  TEMP_SOURCE=$(mktemp -d)
  url="${EIO_SOURCE_URL:-https://github.com/ArizeSky/Emby-In-One/archive/refs/heads/main.tar.gz}"
  curl -fsSL --max-time 180 "$url" -o "$TEMP_SOURCE/source.tar.gz"
  if [[ -n "${SOURCE_SHA256:-}" ]]; then
    [[ "$(sha256sum "$TEMP_SOURCE/source.tar.gz" | awk '{print $1}')" == "${SOURCE_SHA256,,}" ]] || error 'Source checksum mismatch'
  elif [[ "${ALLOW_UNVERIFIED_SOURCE:-false}" == true ]]; then warn 'Explicit unverified-source override enabled'
  else error 'SOURCE_SHA256 required for remote source'; fi
  python3 - "$TEMP_SOURCE" <<'PYCODE'
import sys,tarfile,pathlib
p=pathlib.Path(sys.argv[1])
with tarfile.open(p/'source.tar.gz') as t:
 for m in t.getmembers():
  assert not m.issym() and not m.islnk() and not m.name.startswith('/') and '..' not in pathlib.PurePosixPath(m.name).parts,'unsafe source archive'
 t.extractall(p/'source',filter='data')
PYCODE
  SCRIPT_DIR=$(find "$TEMP_SOURCE/source" -name go.mod -print -quit); SCRIPT_DIR=${SCRIPT_DIR%/go.mod}
fi
[[ -f "$SCRIPT_DIR/eio-docker.sh" && -f "$SCRIPT_DIR/docker-compose.image.yml" ]] || error 'Complete trusted new deployment bundle required (manager absent in older release assets)'
if [[ "$MODE" == source ]]; then ARGS+=(--source-dir "$SCRIPT_DIR"); fi
[[ " ${ARGS[*]} " == *' --mode '* ]] || ARGS+=(--mode "$MODE")
bash "$SCRIPT_DIR/eio-docker.sh" --project-dir "$PROJECT_DIR" install "${ARGS[@]}"
# Keep manager and template beside the deployed CLI, not in an untrusted image URL.
for f in eio-docker.sh docker-compose.image.yml emby-in-one-cli.sh; do
  if [[ "$SCRIPT_DIR/$f" != "$PROJECT_DIR/$f" ]]; then
    if [[ "$f" == docker-compose.image.yml && -f "$PROJECT_DIR/$f" ]]; then
      cmp -s "$SCRIPT_DIR/$f" "$PROJECT_DIR/$f" || cp "$SCRIPT_DIR/$f" "$PROJECT_DIR/$f.release"
    else cp "$SCRIPT_DIR/$f" "$PROJECT_DIR/$f"; fi
  fi
done
if [[ "${EIO_INSTALL_CLI:-true}" == true ]]; then
  install -m 755 "$PROJECT_DIR/emby-in-one-cli.sh" /usr/local/bin/emby-in-one
fi
printf 'Verified installation. New generated credentials, if any: %s/.eio-initial-password (root-only).\n' "$PROJECT_DIR"
