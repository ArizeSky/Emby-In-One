#!/usr/bin/env bash

# ╔══════════════════════════════════════╗
# ║   Emby In One 管理菜单 V1.4.4-rc1    ║
# ╚══════════════════════════════════════╝

PROJECT_DIR="/opt/emby-in-one"
VERSION="1.4.4-rc1"
SERVICE_NAME="emby-in-one"
GITHUB_REPO="ArizeSky/Emby-In-One"

# ── 颜色 ──
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
BLUE='\033[0;34m'
MAGENTA='\033[0;35m'
BOLD='\033[1m'
DIM='\033[2m'
NC='\033[0m'

# ── 权限检查 ──
# binary 模式要操作 systemd 并读取 0600 的 config.yaml；docker 模式下
# docker 组成员能借 --reset-password 重置管理员密码（提权），因此一律要求 root。
if [[ "$EUID" -ne 0 ]]; then
  echo -e "${RED}[错误] 请使用 root 权限运行管理菜单 (sudo emby-in-one)${NC}"
  exit 1
fi

# ── 检测部署方式：binary（systemd）或 docker ──
detect_deploy_mode() {
  # 注意: systemctl list-unit-files <unit> 在没有匹配项时同样返回 0，
  # 必须自己匹配输出，否则没有 systemd 服务的 Docker 部署会被误判为 binary。
  if [[ -x "${PROJECT_DIR}/emby-in-one" ]] \
    && systemctl list-unit-files --type=service 2>/dev/null | grep -q "^${SERVICE_NAME}\.service"; then
    echo "binary"
  else
    echo "docker"
  fi
}

DEPLOY_MODE=$(detect_deploy_mode)

# ── 检测 compose 命令 ──
compose_cmd() {
  if docker compose version &>/dev/null; then
    docker compose "$@"
  elif command -v docker-compose &>/dev/null; then
    docker-compose "$@"
  else
    echo -e "${RED}[错误] 未找到 Docker Compose${NC}"
    return 1
  fi
}

# ── JSON 安全转义（防注入）──
json_escape() {
  local s="$1"
  s="${s//\\/\\\\}"   # \ -> \\
  s="${s//\"/\\\"}"   # " -> \\"
  printf '%s' "$s"
}

# ── 读取配置（正确处理 YAML 引号）──
get_config_value() {
  local key="$1"
  local raw
  raw=$(grep "^  ${key}:" "${PROJECT_DIR}/config/config.yaml" 2>/dev/null | head -1 | sed "s/^  ${key}:[[:space:]]*//" )
  # 去除 YAML 单引号或双引号包裹
  raw="${raw#\'}" ; raw="${raw%\'}"
  raw="${raw#\"}" ; raw="${raw%\"}"
  # 去除行尾空白
  raw="${raw%"${raw##*[![:space:]]}"}"
  echo "$raw"
}

get_port() {
  get_config_value "port"
}

is_hashed_password() {
  [[ "$1" =~ ^[0-9a-fA-F]{32}:[0-9a-fA-F]{128}$ ]]
}

# 从 tokens.json 读取管理员 Token（无需密码登录）
get_admin_token() {
  local token_file="${PROJECT_DIR}/data/tokens.json"
  [[ -f "$token_file" ]] || return 1
  # tokens.json 由 Go json.MarshalIndent 生成，格式固定：
  #   "hextoken": {
  #     ...
  #     "role": "admin",
  awk -F'"' '
    /^  "[0-9a-fA-F]+".*\{/ { key=$2 }
    /"role".*"admin"/ { if (key != "") { print key; exit } }
  ' "$token_file"
}

# ── 当前运行版本对应的 Release Tag ──
# 用于按 tag 拉取脚本（不再跟随 main 分支）。
running_version_tag() {
  local v=""
  if [[ "$DEPLOY_MODE" == "binary" && -x "${PROJECT_DIR}/emby-in-one" ]]; then
    v=$("${PROJECT_DIR}/emby-in-one" --version 2>/dev/null | head -1 | tr -d '[:space:]')
  fi
  if [[ -z "$v" || "$v" == "dev" ]]; then
    v="${VERSION}"
  fi
  # GitHub Release 的 tag 惯例为大写 V 开头（见 release-install.sh 的大小写处理）。
  # 两步前缀处理同时兼容 v1.4.4 / V1.4.4 / 1.4.4 三种输入，避免拼出 VV 前缀。
  v="V${v#v}"; v="V${v#V}"
  echo "${v}"
}

# ── 最新稳定版本的 Release Tag ──
# 与 release-install.sh 的"获取最新稳定版本"逻辑一致，用于 Docker 模式的在线更新。
latest_release_tag() {
  local tag=""
  tag=$(curl -sL --max-time 15 "https://api.github.com/repos/${GITHUB_REPO}/releases/latest" 2>/dev/null \
    | grep -o '"tag_name" *: *"[^"]*"' | head -1 | sed 's/.*: *"//;s/"$//')
  if [[ -n "${tag}" ]]; then
    tag="V${tag#v}"; tag="V${tag#V}"
  fi
  echo "${tag}"
}

reset_password_via_cli() {
  local new_password="$1"
  # 密码经 stdin 传给内置 CLI（`--reset-password -`），不进进程列表、不进 shell 历史。
  # 注意: CLI 默认会探测本机配置端口上是否已有实例在跑并拒绝重置，
  # 因此调用方必须先停服务（见 do_change_password）。
  if [[ "$DEPLOY_MODE" == "binary" ]]; then
    ( cd "${PROJECT_DIR}" && printf '%s' "$new_password" | ./emby-in-one --reset-password - )
  elif docker ps --format '{{.Names}}' 2>/dev/null | grep -qx 'emby-in-one'; then
    printf '%s' "$new_password" | docker exec -i emby-in-one /app/emby-in-one --reset-password -
  else
    # -T: stdin 是管道而非 TTY，必须关掉 TTY 分配
    ( cd "${PROJECT_DIR}" && printf '%s' "$new_password" | compose_cmd run --rm --no-deps -T emby-in-one /app/emby-in-one --reset-password - )
  fi
}

# ── 按任意键返回 ──
pause_return() {
  echo ""
  read -n 1 -s -r -p "按任意键返回主菜单..."
  echo ""
}

# ── 分隔线辅助 ──
print_line() {
  echo -e "${CYAN}──────────────────────────────────────────${NC}"
}

print_kv() {
  local label="$1"
  local value="$2"
  printf "  ${DIM}%-14s${NC} %b\n" "$label" "$value"
}

# ── 将秒数转为可读时长 ──
format_duration() {
  local total=$1
  local days=$((total / 86400))
  local hours=$(( (total % 86400) / 3600 ))
  local mins=$(( (total % 3600) / 60 ))
  local result=""
  if (( days > 0 )); then result="${days} 天 "; fi
  if (( hours > 0 )); then result="${result}${hours} 小时 "; fi
  if (( days == 0 )); then result="${result}${mins} 分钟"; fi
  echo "$result"
}

# ── 菜单函数 ──

do_start() {
  echo -e "${GREEN}▶ 正在启动服务...${NC}"
  echo ""
  if [[ "$DEPLOY_MODE" == "binary" ]]; then
    if ! systemctl start "${SERVICE_NAME}"; then
      echo -e "${RED}✘ 启动失败，请查看: journalctl -u ${SERVICE_NAME} -n 50${NC}"
      return 1
    fi
  else
    if ! ( cd "${PROJECT_DIR}" && compose_cmd up -d ); then
      echo -e "${RED}✘ 启动失败，请检查容器日志 (菜单选项 [10])${NC}"
      return 1
    fi
  fi
  echo ""
  echo -e "${GREEN}✔ 服务已启动${NC}"
}

do_restart() {
  echo -e "${YELLOW}▶ 正在重启服务...${NC}"
  echo ""
  if [[ "$DEPLOY_MODE" == "binary" ]]; then
    if ! systemctl restart "${SERVICE_NAME}"; then
      echo -e "${RED}✘ 重启失败，请查看: journalctl -u ${SERVICE_NAME} -n 50${NC}"
      return 1
    fi
  else
    if ! ( cd "${PROJECT_DIR}" && compose_cmd restart ); then
      echo -e "${RED}✘ 重启失败，请检查容器日志 (菜单选项 [10])${NC}"
      return 1
    fi
  fi
  echo ""
  echo -e "${GREEN}✔ 服务已重启${NC}"
}

do_stop() {
  echo -e "${RED}▶ 正在关闭服务...${NC}"
  echo ""
  if [[ "$DEPLOY_MODE" == "binary" ]]; then
    if ! systemctl stop "${SERVICE_NAME}"; then
      echo -e "${RED}✘ 关闭失败，请查看: systemctl status ${SERVICE_NAME}${NC}"
      return 1
    fi
  else
    if ! ( cd "${PROJECT_DIR}" && compose_cmd down ); then
      echo -e "${RED}✘ 关闭失败，请检查容器状态 (菜单选项 [5])${NC}"
      return 1
    fi
  fi
  echo ""
  echo -e "${GREEN}✔ 服务已关闭${NC}"
}

do_update() {
  if [[ "$DEPLOY_MODE" == "binary" ]]; then
    # ── Binary 模式：通过 release-install.sh 更新 ──
    echo -e "${CYAN}▶ 正在通过 release-install.sh 更新服务...${NC}"
    echo ""
    local tmp_script="" installer="" tag
    if [[ -f "${PROJECT_DIR}/release-install.sh" ]]; then
      # 1) 优先复用磁盘上已安装的脚本（release-install.sh 安装时会留在安装目录）
      installer="${PROJECT_DIR}/release-install.sh"
      echo -e "  ${DIM}使用已安装的 ${installer}${NC}"
    else
      # 2) 其次按当前运行版本的 tag 拉取，不再跟随 main 分支
      tag=$(running_version_tag)
      tmp_script="/tmp/emby-in-one-release-install-$$.sh"
      echo -e "  ${DIM}从 Release ${tag} 获取更新脚本...${NC}"
      if ! curl -fsSL --max-time 30 -o "${tmp_script}" \
        "https://github.com/${GITHUB_REPO}/releases/download/${tag}/release-install.sh"; then
        # Release 未附带该脚本时退到同一 tag 的仓库快照（仍是版本锁定，不是 main）
        if ! curl -fsSL --max-time 30 -o "${tmp_script}" \
          "https://raw.githubusercontent.com/${GITHUB_REPO}/${tag}/release-install.sh"; then
          rm -f "${tmp_script}"
          echo -e "${RED}✘ 获取更新脚本失败（Release ${tag} 可能未附带 release-install.sh）${NC}"
          echo -e "${DIM}  可改用菜单选项 [15] 指定版本更新${NC}"
          return 1
        fi
      fi
      installer="${tmp_script}"
    fi
    bash "${installer}"
    local rc=$?
    if [[ -n "$tmp_script" ]]; then
      rm -f "${tmp_script}"
    fi
    if [[ $rc -ne 0 ]]; then
      echo -e "${RED}✘ 更新脚本执行失败 (退出码 ${rc})${NC}"
      return 1
    fi
  else
    # ── Docker 模式：下载最新 Release 的源码包（带校验和）并重新构建 ──
    echo -e "${CYAN}▶ 正在获取最新 Release 并重新构建...${NC}"
    echo ""
    # 不再拉 main 分支 tarball：该路径无校验和，且内容与运行版本不对应。
    # Release 的 docker 源码包走 download_and_install_docker 的 sha256 校验。
    local latest_tag
    latest_tag=$(latest_release_tag)
    if [[ -z "${latest_tag}" ]]; then
      echo -e "${RED}✘ 无法获取最新 Release 版本号，请检查网络（或改用菜单选项 [15] 指定版本）${NC}"
      return 1
    fi
    echo -e "  ${DIM}最新 Release: ${latest_tag}${NC}"
    download_and_install_docker "${latest_tag}" || return 1
  fi
  echo ""
  echo -e "${GREEN}✔ 服务已更新${NC}"
}

do_update_custom() {
  if [[ "$DEPLOY_MODE" == "binary" ]]; then
    echo -e "${CYAN}▶ 下载自定义版本（Release 二进制）${NC}"
  else
    echo -e "${CYAN}▶ 下载自定义版本（Docker 源码重建）${NC}"
  fi
  echo ""

  local arch
  arch=$(detect_arch)
  if [[ -z "$arch" ]]; then
    echo -e "${RED}[错误] 无法识别当前系统架构${NC}"
    return
  fi

  echo -e "  系统架构: ${CYAN}${arch}${NC}"
  echo -e "  ${DIM}输入版本号即可下载，例如输入 \"1.4\" 将下载 v1.4${NC}"
  echo ""
  read -e -rp "  请输入版本号: " ver_input
  if [[ -z "$ver_input" ]]; then
    echo -e "${YELLOW}版本号不能为空，操作取消${NC}"
    return
  fi

  # 自动补全 v 前缀
  local version_tag="v${ver_input#v}"

  echo ""
  echo -e "  将下载版本: ${GREEN}${version_tag}${NC}"
  read -e -rp "  确认下载并安装？(y/N): " confirm
  if [[ ! "$confirm" =~ ^[yY] ]]; then
    echo -e "${YELLOW}操作已取消${NC}"
    return
  fi

  download_and_install "${version_tag}" "${arch}"
}

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

download_and_install() {
  local version_tag="$1"
  local arch="$2"

  if [[ "$DEPLOY_MODE" == "docker" ]]; then
    download_and_install_docker "${version_tag}"
  else
    download_and_install_binary "${version_tag}" "${arch}"
  fi
}

# ── 下载产物的 sha256 校验 ──
# 返回 0: 校验通过，或该 Release 未提供 .sha256（已告警，兼容旧版本）
# 返回 1: 校验失败/校验和文件无法识别，调用方必须中止安装
verify_download() {
  local file="$1" url="$2"
  if ! command -v sha256sum &>/dev/null; then
    echo -e "${YELLOW}  ⚠ 系统缺少 sha256sum，已跳过完整性校验${NC}"
    return 0
  fi
  local asset_name="${url##*/}"
  local sha_file="${file}.sha256" expected actual
  if ! curl -fsSL --max-time 30 -o "${sha_file}" "${url}.sha256" 2>/dev/null; then
    rm -f "${sha_file}"
    echo -e "${YELLOW}  ⚠ 该 Release 未提供 ${asset_name}.sha256，已跳过完整性校验${NC}"
    return 0
  fi
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
    echo -e "${RED}✘ 校验和文件格式无法识别: ${url}.sha256${NC}"
    return 1
  fi
  actual=$(sha256sum "${file}" | awk '{print $1}')
  if [[ "${expected,,}" != "${actual,,}" ]]; then
    echo -e "${RED}✘ 校验失败！下载内容可能已损坏或被篡改${NC}"
    echo -e "${DIM}    期望: ${expected}${NC}"
    echo -e "${DIM}    实际: ${actual}${NC}"
    return 1
  fi
  echo -e "${DIM}  完整性校验通过: ${asset_name}${NC}"
  return 0
}

download_and_install_docker() {
  local version_tag="$1"
  # GitHub Release 的 tag 为大写 V 开头，归档文件名中的版本为小写 v 开头
  # （与 release-install.sh 的大小写处理一致；tag 大小写错误会 404）
  local release_tag="V${version_tag#v}"
  release_tag="V${release_tag#V}"
  local file_version="v${release_tag#V}"
  local archive_name="Emby-In-One-docker-${file_version}.tar.gz"
  local download_url="https://github.com/${GITHUB_REPO}/releases/download/${release_tag}/${archive_name}"

  echo ""
  echo -e "${CYAN}▶ 正在下载 Docker 源码包 ${archive_name}...${NC}"

  local tmp_dir
  tmp_dir=$(mktemp -d)
  if ! curl -fSL --max-time 120 --progress-bar -o "${tmp_dir}/${archive_name}" "${download_url}" 2>&1; then
    rm -rf "${tmp_dir}"
    echo -e "${RED}[错误] 下载失败，请检查版本号 ${version_tag} 是否存在${NC}"
    echo -e "${DIM}  下载地址: ${download_url}${NC}"
    return 1
  fi
  if ! verify_download "${tmp_dir}/${archive_name}" "${download_url}"; then
    rm -rf "${tmp_dir}"
    echo -e "${RED}[错误] 完整性校验未通过，已中止安装${NC}"
    return 1
  fi

  # 解压源码包
  echo -e "  ${DIM}解压源码包...${NC}"
  if ! tar -xzf "${tmp_dir}/${archive_name}" -C "${tmp_dir}" 2>/dev/null; then
    rm -rf "${tmp_dir}"
    echo -e "${RED}[错误] 解压失败${NC}"
    return 1
  fi

  # 定位源码根目录
  local src_dir=""
  if [[ -d "${tmp_dir}/emby-in-one/cmd" && -f "${tmp_dir}/emby-in-one/go.mod" ]]; then
    src_dir="${tmp_dir}/emby-in-one"
  elif [[ -d "${tmp_dir}/cmd" && -f "${tmp_dir}/go.mod" ]]; then
    src_dir="${tmp_dir}"
  fi

  if [[ -z "$src_dir" ]]; then
    rm -rf "${tmp_dir}"
    echo -e "${RED}✘ 源码包中未找到可部署的 Go 项目文件${NC}"
    return 1
  fi

  # 替换源码（保留 config/ data/ log/ 用户数据）
  echo -e "  ${DIM}更新项目文件...${NC}"
  for item in cmd internal third_party public go.mod; do
    rm -rf "${PROJECT_DIR:?}/${item}"
    if [[ -e "${src_dir}/${item}" ]]; then
      cp -r "${src_dir}/${item}" "${PROJECT_DIR}/"
    fi
  done
  for item in emby-in-one-cli.sh .dockerignore; do
    if [[ -e "${src_dir}/${item}" ]]; then
      cp -f "${src_dir}/${item}" "${PROJECT_DIR}/"
    fi
  done

  # 使用源码包中的 Dockerfile，或生成默认的
  if [[ -f "${src_dir}/Dockerfile" ]]; then
    cp -f "${src_dir}/Dockerfile" "${PROJECT_DIR}/Dockerfile"
  else
    cat > "${PROJECT_DIR}/Dockerfile" <<'DEOF'
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
DEOF
  fi

  # 生成 docker-compose.yml（注入目标版本号）
  cat > "${PROJECT_DIR}/docker-compose.yml" <<EOF
services:
  emby-in-one:
    build:
      context: .
      args:
        VERSION: ${version_tag}
    container_name: emby-in-one
    # 容器以 uid 1000 运行，挂载目录需先 chown 1000:1000
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

  rm -rf "${tmp_dir}"

  # 重建镜像并启动
  echo -e "  ${DIM}构建 Docker 镜像（首次可能需要数分钟）...${NC}"
  # 容器以非 root (uid 1000) 运行，挂载目录必须可写
  chown -R 1000:1000 "${PROJECT_DIR}/config" "${PROJECT_DIR}/data" 2>/dev/null || true
  cd "${PROJECT_DIR}" && compose_cmd build --no-cache || { echo -e "${RED}✘ 构建镜像失败${NC}"; return 1; }
  compose_cmd up -d || { echo -e "${RED}✘ 启动容器失败${NC}"; return 1; }

  # 安全替换 CLI 脚本（原子操作）
  if [[ -f "${PROJECT_DIR}/emby-in-one-cli.sh" ]]; then
    local tmp_cli="/usr/local/bin/emby-in-one.tmp.$$"
    cp -f "${PROJECT_DIR}/emby-in-one-cli.sh" "${tmp_cli}"
    mv -f "${tmp_cli}" /usr/local/bin/emby-in-one
    chmod +x /usr/local/bin/emby-in-one
  fi

  echo ""
  echo -e "${GREEN}✔ Docker 更新完成！版本: ${version_tag}${NC}"
}

download_and_install_binary() {
  local version_tag="$1"
  local arch="$2"
  # Release tag 大写 V、文件名版本小写 v（与 release-install.sh 一致；tag 大小写错误会 404）
  local release_tag="V${version_tag#v}"
  release_tag="V${release_tag#V}"
  local file_version="v${release_tag#V}"
  local binary_name="Emby-In-One-linux-${arch}-${file_version}"
  local download_url="https://github.com/${GITHUB_REPO}/releases/download/${release_tag}/${binary_name}"

  echo ""
  echo -e "${CYAN}▶ 正在下载 ${binary_name}...${NC}"

  local tmp_file="/tmp/emby-in-one-update-$$"
  if ! curl -fSL --max-time 120 --progress-bar -o "${tmp_file}" "${download_url}" 2>&1; then
    rm -f "${tmp_file}"
    echo -e "${RED}[错误] 下载失败，请检查版本号 ${version_tag} 是否存在${NC}"
    echo -e "${DIM}  下载地址: ${download_url}${NC}"
    return 1
  fi
  if ! verify_download "${tmp_file}" "${download_url}"; then
    rm -f "${tmp_file}"
    echo -e "${RED}[错误] 完整性校验未通过，已中止更新（服务未改动）${NC}"
    return 1
  fi

  # 停止当前服务
  local was_running=false
  if systemctl is-active --quiet emby-in-one 2>/dev/null; then
    was_running=true
    echo -e "${YELLOW}▶ 正在停止服务...${NC}"
    systemctl stop emby-in-one
  fi

  # 备份旧二进制
  if [[ -f "${PROJECT_DIR}/emby-in-one" ]]; then
    cp "${PROJECT_DIR}/emby-in-one" "${PROJECT_DIR}/emby-in-one.bak"
    echo -e "${DIM}  已备份旧版本到 emby-in-one.bak${NC}"
  fi

  # 安装新二进制
  mv "${tmp_file}" "${PROJECT_DIR}/emby-in-one"
  chmod +x "${PROJECT_DIR}/emby-in-one"
  echo -e "${GREEN}✔ 二进制文件已更新${NC}"

  # 更新 CLI 脚本（安全原子替换，避免覆盖运行中脚本）
  if [[ -f "${PROJECT_DIR}/emby-in-one-cli.sh" ]]; then
    local tmp_cli="/usr/local/bin/emby-in-one.tmp.$$"
    cp -f "${PROJECT_DIR}/emby-in-one-cli.sh" "${tmp_cli}" 2>/dev/null || true
    mv -f "${tmp_cli}" /usr/local/bin/emby-in-one 2>/dev/null || true
    chmod +x /usr/local/bin/emby-in-one 2>/dev/null || true
  fi

  # 更新管理面板前端文件
  # 先下载到临时文件再原子替换，避免下载中断在 public/ 留下半个文件。
  # 面板 HTML 与二进制的 CSP 是配套的，所以刷新失败必须明确告警：
  # 留着旧副本会让新 CSP 拦掉旧页面的内联样式与 CDN 引用，表现为白屏。
  if [[ -d "${PROJECT_DIR}/public" ]]; then
    for asset in admin.html admin.js; do
      local asset_url="https://github.com/${GITHUB_REPO}/releases/download/${version_tag}/${asset}"
      local asset_tmp="${PROJECT_DIR}/public/${asset}.tmp.$$"
      if curl -fsSL --max-time 30 -o "${asset_tmp}" "${asset_url}" 2>/dev/null; then
        if ! verify_download "${asset_tmp}" "${asset_url}"; then
          rm -f "${asset_tmp}"
          echo -e "${RED}  ✘ ${asset} 校验未通过，磁盘上的旧副本保持原样${NC}"
          continue
        fi
        mv -f "${asset_tmp}" "${PROJECT_DIR}/public/${asset}"
        echo -e "${DIM}  已更新 ${asset}${NC}"
      else
        rm -f "${asset_tmp}"
        echo -e "${YELLOW}  ⚠ ${asset} 更新失败，磁盘上的旧副本保持原样${NC}"
        echo -e "${DIM}    旧面板可能与新版本的 CSP 不匹配；删除 ${PROJECT_DIR}/public/${asset} 即可改用二进制内嵌版本${NC}"
      fi
    done
  fi

  # 重启服务
  if [[ "$was_running" == true ]]; then
    echo -e "${YELLOW}▶ 正在重启服务...${NC}"
    systemctl start "${SERVICE_NAME}"
  fi

  echo ""
  echo -e "${GREEN}✔ 更新完成！版本: ${version_tag}${NC}"
  echo -e "${DIM}  如需回滚，将 emby-in-one.bak 改名为 emby-in-one 并重启${NC}"
}

do_status() {
  echo -e "${CYAN}▶ 正在获取服务状态...${NC}"
  echo ""

  if [[ "$DEPLOY_MODE" == "binary" ]]; then
    local active_state sub_state pid mem uptime_display="N/A"
    active_state=$(systemctl show -p ActiveState --value "${SERVICE_NAME}" 2>/dev/null)
    sub_state=$(systemctl show -p SubState --value "${SERVICE_NAME}" 2>/dev/null)
    pid=$(systemctl show -p MainPID --value "${SERVICE_NAME}" 2>/dev/null)

    local status_text
    if [[ "$active_state" == "active" ]]; then
      status_text="${GREEN}● 运行中 (${sub_state})${NC}"
      local started_at
      started_at=$(systemctl show -p ActiveEnterTimestamp --value "${SERVICE_NAME}" 2>/dev/null)
      if [[ -n "$started_at" ]]; then
        local start_epoch now_epoch diff
        start_epoch=$(date -d "$started_at" +%s 2>/dev/null)
        now_epoch=$(date +%s)
        if [[ -n "$start_epoch" ]]; then
          diff=$((now_epoch - start_epoch))
          uptime_display=$(format_duration "$diff")
        fi
      fi
      if [[ -n "$pid" && "$pid" != "0" ]]; then
        mem=$(ps -o rss= -p "$pid" 2>/dev/null | awk '{printf "%.1f MB", $1/1024}')
      fi
    elif [[ "$active_state" == "inactive" || "$active_state" == "failed" ]]; then
      status_text="${RED}● 未运行 (${active_state})${NC}"
    else
      status_text="${YELLOW}● ${active_state}${NC}"
    fi

    local port
    port=$(get_port)
    port=${port:-8096}

    print_line
    echo -e "  ${BOLD}Emby In One 服务状态${NC}  ${DIM}(Binary 部署)${NC}"
    print_line
    echo -e "  服务状态     ${status_text}"
    print_kv "运行时长" "$uptime_display"
    print_kv "监听端口" "$port"
    [[ -n "$pid" && "$pid" != "0" ]] && print_kv "PID" "$pid"
    [[ -n "$mem" ]] && print_kv "内存占用" "$mem"
    print_kv "安装目录" "${PROJECT_DIR}"
    print_line
    return
  fi

  # Docker 部署
  local container
  container=$(cd "${PROJECT_DIR}" && compose_cmd ps -q 2>/dev/null | head -1)

  if [[ -z "$container" ]]; then
    print_line
    echo -e "  ${BOLD}Emby In One 服务状态${NC}"
    print_line
    echo -e "  容器状态     ${RED}● 未运行${NC}"
    print_line
    return
  fi

  local status started_at image container_id
  status=$(docker inspect --format '{{.State.Status}}' "$container" 2>/dev/null)
  started_at=$(docker inspect --format '{{.State.StartedAt}}' "$container" 2>/dev/null)
  image=$(docker inspect --format '{{.Config.Image}}' "$container" 2>/dev/null)
  container_id=$(docker inspect --format '{{.Id}}' "$container" 2>/dev/null)
  container_id="${container_id:0:12}"

  local port_display
  port_display=$(docker inspect --format '{{range $p, $conf := .NetworkSettings.Ports}}{{$p}} -> {{(index $conf 0).HostPort}}{{"\n"}}{{end}}' "$container" 2>/dev/null | head -1)
  if [[ -z "$port_display" ]]; then
    port_display="无端口映射"
  else
    port_display=$(echo "$port_display" | sed 's|/tcp||g; s|/udp||g')
  fi

  local uptime_display="N/A"
  if [[ "$status" == "running" && -n "$started_at" ]]; then
    local start_epoch now_epoch diff
    start_epoch=$(date -d "$started_at" +%s 2>/dev/null)
    now_epoch=$(date +%s)
    if [[ -n "$start_epoch" ]]; then
      diff=$((now_epoch - start_epoch))
      uptime_display=$(format_duration "$diff")
    fi
  fi

  local status_text
  if [[ "$status" == "running" ]]; then
    status_text="${GREEN}● 运行中${NC}"
  elif [[ "$status" == "exited" ]]; then
    status_text="${RED}● 已停止${NC}"
  else
    status_text="${YELLOW}● ${status}${NC}"
  fi

  print_line
  echo -e "  ${BOLD}Emby In One 服务状态${NC}  ${DIM}(Docker 部署)${NC}"
  print_line
  echo -e "  容器状态     ${status_text}"
  print_kv "运行时长" "$uptime_display"
  print_kv "端口映射" "$port_display"
  print_kv "镜像" "$image"
  print_kv "容器 ID" "$container_id"
  print_line
}

# ── 本机出口地址 ──
# 只查本机路由表，不再查询第三方 ip.sb（避免把服务器 IP 泄露给外部服务）
detect_outbound_ip() {
  local family="$1" ip=""
  if command -v ip &>/dev/null; then
    if [[ "$family" == "6" ]]; then
      ip=$(ip -6 route get 2001:4860:4860::8888 2>/dev/null | grep -oE 'src [0-9a-fA-F:]+' | awk '{print $2}' | head -1)
    else
      ip=$(ip route get 1.1.1.1 2>/dev/null | grep -oE 'src [0-9.]+' | awk '{print $2}' | head -1)
    fi
  fi
  if [[ -z "$ip" && "$family" == "4" ]] && command -v hostname &>/dev/null; then
    ip=$(hostname -I 2>/dev/null | awk '{print $1}')
  fi
  echo "$ip"
}

do_show_ip() {
  local port
  port=$(get_port)
  port=${port:-8096}

  echo -e "${CYAN}▶ 正在获取本机地址...${NC}"
  local ipv4 ipv6
  ipv4=$(detect_outbound_ip 4)
  ipv6=$(detect_outbound_ip 6)

  echo ""
  print_line
  echo -e "  ${BOLD}服务器 IP 地址${NC}"
  print_line
  if [[ -n "$ipv4" ]]; then
    print_kv "IPv4" "${GREEN}${ipv4}${NC}"
  else
    print_kv "IPv4" "${RED}无法获取${NC}"
  fi
  if [[ -n "$ipv6" ]]; then
    print_kv "IPv6" "${GREEN}${ipv6}${NC}"
  else
    print_kv "IPv6" "${YELLOW}无法获取或不支持${NC}"
  fi
  echo ""
  echo -e "  ${BOLD}访问地址${NC}"
  print_line
  if [[ -n "$ipv4" ]]; then
    print_kv "客户端地址" "${GREEN}http://${ipv4}:${port}${NC}"
    print_kv "管理面板" "${GREEN}http://${ipv4}:${port}/admin${NC}"
  fi
  if [[ -n "$ipv6" ]]; then
    print_kv "IPv6 访问" "${GREEN}http://[${ipv6}]:${port}${NC}"
  fi
  print_line
  echo ""
  echo -e "  ${DIM}说明: 地址取自本机路由表（未查询第三方服务）；NAT 环境下显示的是内网地址${NC}"
  echo ""
}

do_show_admin() {
  local username password password_display
  username=$(get_config_value "username")
  password=$(get_config_value "password")

  if is_hashed_password "$password"; then
    password_display="${DIM}已加密存储（不可直接查看）${NC}"
  else
    password_display="$password"
  fi

  echo ""
  print_line
  echo -e "  ${BOLD}管理员凭据${NC}"
  print_line
  print_kv "用户名" "$username"
  echo -e "  ${DIM}密码${NC}           $password_display"
  print_line
  if is_hashed_password "$password"; then
    echo -e "  ${YELLOW}提示：密码已加密存储，如需重置请使用菜单选项 [8]${NC}"
  fi
  echo ""
}

do_change_username() {
  local current
  current=$(get_config_value "username")
  echo -e "  当前用户名: ${CYAN}${current}${NC}"
  echo ""
  read -e -rp "  请输入新用户名: " new_username
  if [[ -z "$new_username" ]]; then
    echo -e "${YELLOW}用户名不能为空，操作取消${NC}"
    return
  fi
  # 用户名会写进 config.yaml，必须先限定字符集: 否则含引号/换行的输入会写出非法 YAML，
  # 反斜杠还会被 awk -v 解释成转义序列（与 Go 侧用户名校验保持一致）。
  if [[ ! "$new_username" =~ ^[A-Za-z0-9_-]+$ ]]; then
    echo -e "${RED}用户名只能包含字母、数字、下划线和连字符${NC}"
    return
  fi
  awk -v val="$new_username" '/^  username:/{print "  username: \x27" val "\x27"; next}1' "${PROJECT_DIR}/config/config.yaml" > "${PROJECT_DIR}/config/config.yaml.tmp" && mv "${PROJECT_DIR}/config/config.yaml.tmp" "${PROJECT_DIR}/config/config.yaml"
  chmod 600 "${PROJECT_DIR}/config/config.yaml" 2>/dev/null || true
  echo ""
  echo -e "${GREEN}✔ 用户名已修改为: ${new_username}${NC}"
  echo -e "${YELLOW}▶ 正在重启服务使配置生效...${NC}"
  if [[ "$DEPLOY_MODE" == "binary" ]]; then
    systemctl restart "${SERVICE_NAME}"
  else
    cd "${PROJECT_DIR}" && compose_cmd restart
  fi
  echo -e "${GREEN}✔ 完成${NC}"
}

do_change_password() {
  read -s -e -rp "  请输入新密码: " new_password
  echo ""
  if [[ -z "$new_password" ]]; then
    echo -e "${YELLOW}密码不能为空，操作取消${NC}"
    return
  fi
  # 内置 CLI 默认拒绝在服务运行中重置（避免与运行实例竞争 config/tokens），
  # 所以先停服务，重置完成后无论成败都重新拉起。
  echo -e "${YELLOW}▶ 正在停止服务...${NC}"
  if [[ "$DEPLOY_MODE" == "binary" ]]; then
    systemctl stop "${SERVICE_NAME}" >/dev/null 2>&1 || true
  else
    ( cd "${PROJECT_DIR}" && compose_cmd stop ) >/dev/null 2>&1 || true
  fi
  echo -e "${YELLOW}▶ 正在调用内置 reset-password CLI...${NC}"
  if reset_password_via_cli "$new_password"; then
    echo ""
    echo -e "${GREEN}✔ 密码已修改${NC}"
  else
    echo ""
    echo -e "${RED}✘ 密码重置失败${NC}"
    if [[ "$DEPLOY_MODE" == "binary" ]]; then
      echo -e "${DIM}  若因服务仍在运行而失败，可停服后加 --force 重试: ./emby-in-one --reset-password - --force${NC}"
    else
      echo -e "${DIM}  若因服务仍在运行而失败，可手动执行 (服务需先停):${NC}"
      echo -e "${DIM}  docker compose -f ${PROJECT_DIR}/docker-compose.yml run --rm -T emby-in-one /app/emby-in-one --reset-password - --force${NC}"
    fi
  fi
  echo -e "${YELLOW}▶ 正在启动服务...${NC}"
  if [[ "$DEPLOY_MODE" == "binary" ]]; then
    systemctl start "${SERVICE_NAME}" >/dev/null 2>&1 || true
  else
    ( cd "${PROJECT_DIR}" && compose_cmd up -d ) >/dev/null 2>&1 || true
  fi
  echo -e "${GREEN}✔ 完成${NC}"
}

do_logs() {
  echo -e "${CYAN}显示最近 50 条日志 (Ctrl+C 退出):${NC}"
  echo ""
  if [[ "$DEPLOY_MODE" == "binary" ]]; then
    journalctl -u "${SERVICE_NAME}" -f -n 50
  else
    cd "${PROJECT_DIR}" && compose_cmd logs -f --tail 50
  fi
}

do_user_list() {
  echo -e "${CYAN}▶ 正在获取用户列表...${NC}"
  echo ""
  local service_running=false
  if [[ "$DEPLOY_MODE" == "binary" ]]; then
    systemctl is-active --quiet "${SERVICE_NAME}" 2>/dev/null && service_running=true
  else
    docker ps --format '{{.Names}}' 2>/dev/null | grep -qx 'emby-in-one' && service_running=true
  fi
  if [[ "$service_running" != true ]]; then
    echo -e "${RED}[错误] 服务未运行，请先启动服务${NC}"
    return
  fi
  local port
  port=$(get_port)
  port=${port:-8096}
  local result
  result=$(curl -s --max-time 5 "http://127.0.0.1:${port}/Users/Public" 2>/dev/null)
  if [[ -z "$result" ]]; then
    echo -e "${RED}[错误] 无法连接服务${NC}"
    return
  fi
  print_line
  echo -e "  ${BOLD}可用用户列表${NC}"
  print_line
  echo "$result" | grep -o '"Name" *: *"[^"]*"' | sed 's/.*: *"//;s/"$//' | while read -r name; do
    echo -e "  ${GREEN}●${NC} $name"
  done
  print_line
}

do_user_add() {
  local service_running=false
  if [[ "$DEPLOY_MODE" == "binary" ]]; then
    systemctl is-active --quiet "${SERVICE_NAME}" 2>/dev/null && service_running=true
  else
    docker ps --format '{{.Names}}' 2>/dev/null | grep -qx 'emby-in-one' && service_running=true
  fi
  if [[ "$service_running" != true ]]; then
    echo -e "${RED}[错误] 服务未运行，请先启动服务${NC}"
    return
  fi
  echo ""
  read -e -rp "  请输入新用户名: " new_user
  if [[ -z "$new_user" ]]; then
    echo -e "${YELLOW}用户名不能为空，操作取消${NC}"
    return
  fi
  read -e -rsp "  请输入密码: " new_pass
  echo ""
  if [[ -z "$new_pass" ]]; then
    echo -e "${YELLOW}密码不能为空，操作取消${NC}"
    return
  fi

  # 获取管理员 token（优先从 tokens.json 读取，无需密码登录）
  local port token
  port=$(get_port)
  port=${port:-8096}
  token=$(get_admin_token)
  if [[ -z "$token" ]]; then
    echo -e "${RED}[错误] 未找到有效的管理员令牌，请先通过 Web 面板或 Emby 客户端以管理员身份登录一次${NC}"
    return
  fi

  local create_result
  # 密码经 stdin (--data @-) 传给 curl，避免出现在进程列表里
  create_result=$(curl -s --max-time 5 -X POST "http://127.0.0.1:${port}/admin/api/users" \
    -H 'Content-Type: application/json' \
    -H "X-Emby-Token: ${token}" \
    --data @- 2>/dev/null <<JSON
{"username":"$(json_escape "${new_user}")","password":"$(json_escape "${new_pass}")"}
JSON
)
  if echo "$create_result" | grep -q '"error"'; then
    local err
    err=$(echo "$create_result" | grep -o '"error" *: *"[^"]*"' | head -1 | sed 's/.*: *"//;s/"$//')
    echo -e "${RED}✘ 创建失败: ${err}${NC}"
  else
    echo -e "${GREEN}✔ 用户 ${new_user} 创建成功${NC}"
  fi
}

do_user_delete() {
  local service_running=false
  if [[ "$DEPLOY_MODE" == "binary" ]]; then
    systemctl is-active --quiet "${SERVICE_NAME}" 2>/dev/null && service_running=true
  else
    docker ps --format '{{.Names}}' 2>/dev/null | grep -qx 'emby-in-one' && service_running=true
  fi
  if [[ "$service_running" != true ]]; then
    echo -e "${RED}[错误] 服务未运行，请先启动服务${NC}"
    return
  fi
  echo ""
  read -e -rp "  请输入要删除的用户名: " del_user
  if [[ -z "$del_user" ]]; then
    echo -e "${YELLOW}用户名不能为空，操作取消${NC}"
    return
  fi

  # 获取管理员 token（优先从 tokens.json 读取，无需密码登录）
  local port token
  port=$(get_port)
  port=${port:-8096}
  token=$(get_admin_token)
  if [[ -z "$token" ]]; then
    echo -e "${RED}[错误] 未找到有效的管理员令牌，请先通过 Web 面板或 Emby 客户端以管理员身份登录一次${NC}"
    return
  fi

  # 获取用户列表查找 ID
  # 用户名用 grep -F 做字面匹配: 直接把用户输入拼进正则时，输入 `.*` 会匹配到别的用户。
  # 记录按 {...} 切分（该接口没有嵌套对象），再按用户名筛选，不依赖字段顺序。
  local users_result user_id
  users_result=$(curl -s --max-time 5 "http://127.0.0.1:${port}/admin/api/users" \
    -H "X-Emby-Token: ${token}" 2>/dev/null)
  user_id=$(echo "$users_result" \
    | grep -oE '\{[^{}]*"username":"[^"]*"[^{}]*\}' \
    | grep -F "\"username\":\"${del_user}\"" \
    | head -1 \
    | grep -oE '"id":"[^"]*"' \
    | head -1 \
    | sed 's/.*"id":"//;s/"$//')
  if [[ -z "$user_id" ]]; then
    echo -e "${RED}[错误] 未找到用户 ${del_user}（注意：不能删除管理员账号）${NC}"
    return
  fi

  read -e -rp "  确认删除用户 ${del_user}？(y/N): " confirm
  if [[ ! "$confirm" =~ ^[yY] ]]; then
    echo -e "${YELLOW}操作已取消${NC}"
    return
  fi

  local del_result
  del_result=$(curl -s --max-time 5 -X DELETE "http://127.0.0.1:${port}/admin/api/users/${user_id}" \
    -H "X-Emby-Token: ${token}" 2>/dev/null)
  if echo "$del_result" | grep -q '"success"'; then
    echo -e "${GREEN}✔ 用户 ${del_user} 已删除${NC}"
  else
    local err
    err=$(echo "$del_result" | grep -o '"error" *: *"[^"]*"' | head -1 | sed 's/.*: *"//;s/"$//')
    echo -e "${RED}✘ 删除失败: ${err:-未知错误}${NC}"
  fi
}

do_uninstall() {
  echo -e "${RED}${BOLD}⚠  即将卸载 Emby In One${NC}"
  echo ""
  if [[ "$DEPLOY_MODE" == "binary" ]]; then
    echo -e "  此操作将停止 systemd 服务并删除二进制文件。"
  else
    echo -e "  此操作将停止并删除容器和镜像。"
  fi
  echo ""

  read -e -rp "  确认卸载？(输入 yes 继续): " confirm
  if [[ "$confirm" != "yes" ]]; then
    echo -e "${YELLOW}操作已取消${NC}"
    return
  fi

  echo ""

  read -e -rp "  是否删除配置和数据？(y/N): " del_data

  echo ""
  if [[ "$DEPLOY_MODE" == "binary" ]]; then
    echo -e "${YELLOW}▶ 正在停止并禁用 systemd 服务...${NC}"
    systemctl stop "${SERVICE_NAME}" 2>/dev/null || true
    systemctl disable "${SERVICE_NAME}" 2>/dev/null || true
    rm -f "/etc/systemd/system/${SERVICE_NAME}.service"
    systemctl daemon-reload 2>/dev/null || true
  else
    echo -e "${YELLOW}▶ 正在停止并删除容器和镜像...${NC}"
    cd "${PROJECT_DIR}" && compose_cmd down --rmi all 2>/dev/null
  fi

  if [[ "$del_data" =~ ^[yY] ]]; then
    echo -e "${YELLOW}▶ 正在删除所有数据和配置...${NC}"
    rm -rf "${PROJECT_DIR}"
  else
    echo -e "${YELLOW}▶ 保留 config/ data/ log/ 目录，删除其他文件...${NC}"
    find "${PROJECT_DIR}" -mindepth 1 -maxdepth 1 ! -name config ! -name data ! -name log -exec rm -rf {} +
  fi

  echo -e "${YELLOW}▶ 正在删除 CLI 工具...${NC}"
  rm -f /usr/local/bin/emby-in-one
  hash -d emby-in-one 2>/dev/null

  echo ""
  echo -e "${GREEN}✔ 卸载完成${NC}"
  if [[ ! "$del_data" =~ ^[yY] ]]; then
    echo -e "${DIM}  配置和数据已保留在 ${PROJECT_DIR}/config、${PROJECT_DIR}/data 和 ${PROJECT_DIR}/log${NC}"
  fi
  echo ""
  echo -e "${DIM}  提示: 如果当前 shell 仍能找到 emby-in-one 命令，请执行 hash -r 或重新打开终端${NC}"
  echo ""
  exit 0
}

# ── 主菜单 ──
show_menu() {
  echo ""
  echo -e "${BOLD}${BLUE}  ┌──────────────────────────────────────┐${NC}"
  echo -e "${BOLD}${BLUE}  │     Emby In One 管理菜单  ${DIM}v${VERSION}${NC}${BOLD}${BLUE}     │${NC}"
  echo -e "${BOLD}${BLUE}  └──────────────────────────────────────┘${NC}"
  echo ""
  echo -e "  ${BOLD}服务管理${NC}"
  echo -e "    ${GREEN}1${NC}) 启动服务          ${GREEN}2${NC}) 重启服务"
  echo -e "    ${GREEN}3${NC}) 关闭服务          ${GREEN}4${NC}) 在线更新（最新版）"
  echo ""
  echo -e "  ${BOLD}信息查看${NC}"
  echo -e "    ${CYAN}5${NC}) 查看服务状态      ${CYAN}6${NC}) 查看服务器 IP"
  echo ""
  echo -e "  ${BOLD}账号管理${NC}"
  echo -e "    ${MAGENTA}7${NC}) 查看管理员凭据    ${MAGENTA}8${NC}) 修改管理员密码"
  echo -e "    ${MAGENTA}9${NC}) 修改管理员账号"
  echo ""
  echo -e "  ${BOLD}多用户管理${NC}"
  echo -e "   ${MAGENTA}12${NC}) 查看用户列表     ${MAGENTA}13${NC}) 添加普通用户"
  echo -e "   ${MAGENTA}14${NC}) 删除普通用户"
  echo ""
  echo -e "  ${BOLD}系统维护${NC}"
  echo -e "   ${YELLOW}10${NC}) 查看日志         ${RED}11${NC}) 卸载 Emby In One"
  echo -e "   ${CYAN}15${NC}) 下载指定版本"
  echo ""
  echo -e "    ${DIM}0${NC}) 退出"
  echo ""
}

# ── 检查项目目录 ──
if [[ ! -d "${PROJECT_DIR}" ]]; then
  echo -e "${RED}[错误] 项目目录 ${PROJECT_DIR} 不存在${NC}"
  echo -e "${YELLOW}请先运行 install.sh 安装 Emby In One${NC}"
  exit 1
fi

# ── 主循环 ──
while true; do
  clear
  show_menu
  read -e -rp "请选择操作 [0-15]: " choice
  echo ""
  case $choice in
    1) do_start; pause_return ;;
    2) do_restart; pause_return ;;
    3) do_stop; pause_return ;;
    4) do_update; pause_return ;;
    5) do_status; pause_return ;;
    6) do_show_ip; pause_return ;;
    7) do_show_admin; pause_return ;;
    8) do_change_password; pause_return ;;
    9) do_change_username; pause_return ;;
    10) do_logs; pause_return ;;
    11) do_uninstall ;;
    12) do_user_list; pause_return ;;
    13) do_user_add; pause_return ;;
    14) do_user_delete; pause_return ;;
    15) do_update_custom; pause_return ;;
    0) clear; echo -e "${GREEN}再见！${NC}"; exit 0 ;;
    *) echo -e "${RED}无效选择，请重试${NC}"; pause_return ;;
  esac
done
