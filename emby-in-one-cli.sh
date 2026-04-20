#!/usr/bin/env bash

# ╔══════════════════════════════════════╗
# ║    Emby In One 管理菜单 V1.4.2       ║
# ╚══════════════════════════════════════╝

PROJECT_DIR="/opt/emby-in-one"
VERSION="1.4.2"
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

# ── 检测部署方式：binary（systemd）或 docker ──
detect_deploy_mode() {
  if [[ -x "${PROJECT_DIR}/emby-in-one" ]] && systemctl list-unit-files "${SERVICE_NAME}.service" &>/dev/null 2>&1; then
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

reset_password_via_cli() {
  local new_password="$1"
  if [[ "$DEPLOY_MODE" == "binary" ]]; then
    cd "${PROJECT_DIR}" && ./emby-in-one --reset-password "$new_password"
  elif docker ps --format '{{.Names}}' 2>/dev/null | grep -qx 'emby-in-one'; then
    docker exec -i emby-in-one /app/emby-in-one --reset-password "$new_password"
  else
    cd "${PROJECT_DIR}" && compose_cmd run --rm --no-deps emby-in-one /app/emby-in-one --reset-password "$new_password"
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
    systemctl start "${SERVICE_NAME}"
  else
    cd "${PROJECT_DIR}" && compose_cmd up -d
  fi
  echo ""
  echo -e "${GREEN}✔ 服务已启动${NC}"
}

do_restart() {
  echo -e "${YELLOW}▶ 正在重启服务...${NC}"
  echo ""
  if [[ "$DEPLOY_MODE" == "binary" ]]; then
    systemctl restart "${SERVICE_NAME}"
  else
    cd "${PROJECT_DIR}" && compose_cmd restart
  fi
  echo ""
  echo -e "${GREEN}✔ 服务已重启${NC}"
}

do_stop() {
  echo -e "${RED}▶ 正在关闭服务...${NC}"
  echo ""
  if [[ "$DEPLOY_MODE" == "binary" ]]; then
    systemctl stop "${SERVICE_NAME}"
  else
    cd "${PROJECT_DIR}" && compose_cmd down
  fi
  echo ""
  echo -e "${GREEN}✔ 服务已关闭${NC}"
}

REPO_TARBALL="https://github.com/${GITHUB_REPO}/archive/refs/heads/main.tar.gz"

do_update() {
  if [[ "$DEPLOY_MODE" == "binary" ]]; then
    # ── Binary 模式：通过 release-install.sh 更新 ──
    echo -e "${CYAN}▶ 正在通过 release-install.sh 更新服务...${NC}"
    echo ""
    local tmp_script="/tmp/emby-in-one-release-install-$$.sh"
    if curl -fsSL --max-time 30 -o "${tmp_script}" "https://raw.githubusercontent.com/${GITHUB_REPO}/main/release-install.sh"; then
      bash "${tmp_script}"
      rm -f "${tmp_script}"
    else
      echo -e "${RED}✘ 下载更新脚本失败，请检查网络${NC}"
      return 1
    fi
  else
    # ── Docker 模式：拉取最新源码并重新构建 ──
    echo -e "${CYAN}▶ 正在拉取最新源码并重新构建...${NC}"
    echo ""

    # 1. 下载最新源码到临时目录
    local tmp_dir
    tmp_dir=$(mktemp -d)
    echo -e "  ${DIM}从 GitHub 下载最新代码...${NC}"
    if ! curl -fsSL --max-time 120 "${REPO_TARBALL}" | tar -xz -C "${tmp_dir}" --strip-components=1; then
      rm -rf "${tmp_dir}"
      echo -e "${RED}✘ 下载源码失败，请检查网络${NC}"
      return 1
    fi

    # 2. 定位源码根目录（支持独立发行和根仓库两种结构）
    local src_dir=""
    if [[ -d "${tmp_dir}/cmd" && -d "${tmp_dir}/internal" && -f "${tmp_dir}/go.mod" ]]; then
      src_dir="${tmp_dir}"
    elif [[ -d "${tmp_dir}/Emby-In-One-Go/cmd" && -f "${tmp_dir}/Emby-In-One-Go/go.mod" ]]; then
      src_dir="${tmp_dir}/Emby-In-One-Go"
    fi

    if [[ -z "$src_dir" ]]; then
      rm -rf "${tmp_dir}"
      echo -e "${RED}✘ 下载内容中未找到可部署的 Go 项目文件${NC}"
      return 1
    fi

    # 3. 替换源码（保留 config/ data/ log/ 用户数据）
    echo -e "  ${DIM}更新项目文件...${NC}"
    for item in cmd internal third_party public go.mod; do
      rm -rf "${PROJECT_DIR:?}/${item}"
      if [[ -e "${src_dir}/${item}" ]]; then
        cp -r "${src_dir}/${item}" "${PROJECT_DIR}/"
      fi
    done
    for item in README.md README_EN.md Update.md emby-in-one-cli.sh .dockerignore LICENSE; do
      if [[ -e "${src_dir}/${item}" ]]; then
        cp -f "${src_dir}/${item}" "${PROJECT_DIR}/"
      fi
    done

    # 4. 重新生成 Dockerfile 和 docker-compose.yml
    echo -e "  ${DIM}生成构建文件...${NC}"
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
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata && rm -rf /var/lib/apt/lists/*
WORKDIR /app
RUN mkdir -p /app/config /app/data /app/public
COPY public ./public
COPY --from=builder /out/emby-in-one ./emby-in-one
EXPOSE 8096
CMD ["./emby-in-one"]
DEOF

    cat > "${PROJECT_DIR}/docker-compose.yml" <<'CEOF'
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
CEOF

    rm -rf "${tmp_dir}"

    # 5. 重建镜像并启动
    echo -e "  ${DIM}构建 Docker 镜像（首次可能需要数分钟）...${NC}"
    cd "${PROJECT_DIR}" && compose_cmd build --no-cache || { echo -e "${RED}✘ 构建镜像失败${NC}"; return 1; }
    compose_cmd up -d

    # 6. 安全替换 CLI 脚本（原子操作，避免覆盖运行中脚本）
    if [[ -f "${PROJECT_DIR}/emby-in-one-cli.sh" ]]; then
      local tmp_cli="/usr/local/bin/emby-in-one.tmp.$$"
      cp -f "${PROJECT_DIR}/emby-in-one-cli.sh" "${tmp_cli}"
      mv -f "${tmp_cli}" /usr/local/bin/emby-in-one
      chmod +x /usr/local/bin/emby-in-one
    fi
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

download_and_install_docker() {
  local version_tag="$1"
  local archive_name="Emby-In-One-docker-${version_tag}.tar.gz"
  local download_url="https://github.com/${GITHUB_REPO}/releases/download/${version_tag}/${archive_name}"

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
  for item in emby-in-one-cli.sh; do
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
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata && rm -rf /var/lib/apt/lists/*
WORKDIR /app
RUN mkdir -p /app/config /app/data /app/public
COPY public ./public
COPY --from=builder /out/emby-in-one ./emby-in-one
EXPOSE 8096
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
    ports:
      - "8096:8096"
    volumes:
      - ./config:/app/config
      - ./data:/app/data
    restart: unless-stopped
EOF

  rm -rf "${tmp_dir}"

  # 重建镜像并启动
  echo -e "  ${DIM}构建 Docker 镜像（首次可能需要数分钟）...${NC}"
  cd "${PROJECT_DIR}" && compose_cmd build --no-cache || { echo -e "${RED}✘ 构建镜像失败${NC}"; return 1; }
  compose_cmd up -d

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
  local binary_name="Emby-In-One-linux-${arch}-${version_tag}"
  local download_url="https://github.com/${GITHUB_REPO}/releases/download/${version_tag}/${binary_name}"

  echo ""
  echo -e "${CYAN}▶ 正在下载 ${binary_name}...${NC}"

  local tmp_file="/tmp/emby-in-one-update-$$"
  if ! curl -fSL --max-time 120 --progress-bar -o "${tmp_file}" "${download_url}" 2>&1; then
    rm -f "${tmp_file}"
    echo -e "${RED}[错误] 下载失败，请检查版本号 ${version_tag} 是否存在${NC}"
    echo -e "${DIM}  下载地址: ${download_url}${NC}"
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
  if [[ -d "${PROJECT_DIR}/public" ]]; then
    for asset in admin.html admin.js; do
      local asset_url="https://github.com/${GITHUB_REPO}/releases/download/${version_tag}/${asset}"
      if curl -fsSL --max-time 30 -o "${PROJECT_DIR}/public/${asset}" "${asset_url}" 2>/dev/null; then
        echo -e "${DIM}  已更新 ${asset}${NC}"
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

do_show_ip() {
  local port
  port=$(get_port)
  port=${port:-8096}

  echo -e "${CYAN}▶ 正在获取公网 IP 地址...${NC}"
  local ipv4 ipv6
  ipv4=$(curl -4 -s --max-time 5 ip.sb 2>/dev/null)
  ipv6=$(curl -6 -s --max-time 5 ip.sb 2>/dev/null)

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
  awk -v val="$new_username" '/^  username:/{print "  username: \x27" val "\x27"; next}1' "${PROJECT_DIR}/config/config.yaml" > "${PROJECT_DIR}/config/config.yaml.tmp" && mv "${PROJECT_DIR}/config/config.yaml.tmp" "${PROJECT_DIR}/config/config.yaml"
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
  read -e -rp "  请输入新密码: " new_password
  if [[ -z "$new_password" ]]; then
    echo -e "${YELLOW}密码不能为空，操作取消${NC}"
    return
  fi
  echo -e "${YELLOW}▶ 正在调用内置 reset-password CLI...${NC}"
  if ! reset_password_via_cli "$new_password"; then
    echo -e "${RED}✘ 密码重置失败${NC}"
    return
  fi
  echo ""
  echo -e "${GREEN}✔ 密码已修改${NC}"
  echo -e "${YELLOW}▶ 正在重启服务使配置生效...${NC}"
  if [[ "$DEPLOY_MODE" == "binary" ]]; then
    systemctl restart "${SERVICE_NAME}" >/dev/null 2>&1 || true
  else
    cd "${PROJECT_DIR}" && compose_cmd restart >/dev/null 2>&1 || true
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
  create_result=$(curl -s --max-time 5 -X POST "http://127.0.0.1:${port}/admin/api/users" \
    -H 'Content-Type: application/json' \
    -H "X-Emby-Token: ${token}" \
    -d "{\"username\":\"$(json_escape "${new_user}")\",\"password\":\"$(json_escape "${new_pass}")\"}" 2>/dev/null)
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
  local users_result user_id
  users_result=$(curl -s --max-time 5 "http://127.0.0.1:${port}/admin/api/users" \
    -H "X-Emby-Token: ${token}" 2>/dev/null)
  user_id=$(echo "$users_result" | grep -o "\"id\":\"[^\"]*\",\"username\":\"${del_user}\"" | head -1 | sed 's/.*"id":"//;s/".*//')
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

