#!/usr/bin/env bash

# ╔══════════════════════════════════════╗
# ║    Emby In One 管理菜单 V1.3.6       ║
# ╚══════════════════════════════════════╝

PROJECT_DIR="/opt/emby-in-one"
VERSION="1.3.6"

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

SERVICE_NAME="emby-in-one"

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
  printf "  ${DIM}%-14s${NC} %s\n" "$label" "$value"
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

do_update() {
  if [[ "$DEPLOY_MODE" == "binary" ]]; then
    echo -e "${CYAN}▶ 正在通过 release-install.sh 更新服务...${NC}"
    echo ""
    local tmp_script="/tmp/emby-in-one-release-install-$$.sh"
    if curl -fsSL --max-time 30 -o "${tmp_script}" "https://raw.githubusercontent.com/ArizeSky/Emby-In-One/main/release-install.sh"; then
      bash "${tmp_script}"
      rm -f "${tmp_script}"
    else
      echo -e "${RED}✘ 下载更新脚本失败，请检查网络${NC}"
      return 1
    fi
  else
    echo -e "${CYAN}▶ 正在重新构建并更新服务...${NC}"
    echo ""
    cd "${PROJECT_DIR}" && compose_cmd build --no-cache
    compose_cmd up -d
  fi
  echo ""
  echo -e "${GREEN}✔ 服务已更新${NC}"
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
  read -rp "  请输入新用户名: " new_username
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
  read -rp "  请输入新密码: " new_password
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

do_uninstall() {
  echo -e "${RED}${BOLD}⚠  即将卸载 Emby In One${NC}"
  echo ""
  if [[ "$DEPLOY_MODE" == "binary" ]]; then
    echo -e "  此操作将停止 systemd 服务并删除二进制文件。"
  else
    echo -e "  此操作将停止并删除容器和镜像。"
  fi
  echo ""

  read -rp "  确认卸载？(输入 yes 继续): " confirm
  if [[ "$confirm" != "yes" ]]; then
    echo -e "${YELLOW}操作已取消${NC}"
    return
  fi

  echo ""

  read -rp "  是否删除配置和数据？(y/N): " del_data

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
    echo -e "${YELLOW}▶ 保留 config/ 和 data/ 目录，删除其他文件...${NC}"
    find "${PROJECT_DIR}" -maxdepth 1 ! -name config ! -name data ! -name log ! -name . -exec rm -rf {} +
  fi

  echo -e "${YELLOW}▶ 正在删除 CLI 工具...${NC}"
  rm -f /usr/local/bin/emby-in-one
  hash -d emby-in-one 2>/dev/null

  echo ""
  echo -e "${GREEN}✔ 卸载完成${NC}"
  if [[ ! "$del_data" =~ ^[yY] ]]; then
    echo -e "${DIM}  配置和数据已保留在 ${PROJECT_DIR}/config 和 ${PROJECT_DIR}/data${NC}"
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
  echo -e "    ${GREEN}3${NC}) 关闭服务          ${GREEN}4${NC}) 更新服务"
  echo ""
  echo -e "  ${BOLD}信息查看${NC}"
  echo -e "    ${CYAN}5${NC}) 查看服务状态      ${CYAN}6${NC}) 查看服务器 IP"
  echo ""
  echo -e "  ${BOLD}账号管理${NC}"
  echo -e "    ${MAGENTA}7${NC}) 查看管理员凭据    ${MAGENTA}8${NC}) 修改管理员密码"
  echo -e "    ${MAGENTA}9${NC}) 修改管理员账号"
  echo ""
  echo -e "  ${BOLD}系统维护${NC}"
  echo -e "   ${YELLOW}10${NC}) 查看日志         ${RED}11${NC}) 卸载 Emby In One"
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
  read -rp "请选择操作 [0-11]: " choice
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
    0) clear; echo -e "${GREEN}再见！${NC}"; exit 0 ;;
    *) echo -e "${RED}无效选择，请重试${NC}"; pause_return ;;
  esac
done

