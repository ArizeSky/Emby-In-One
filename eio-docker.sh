#!/usr/bin/env bash
# Explicit Docker deployment. No source/eval of state, no registry credentials.
set -Eeuo pipefail
umask 077
SELF_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
PROJECT_DIR=${EIO_PROJECT_DIR:-/opt/emby-in-one}
ACTION='' REQUEST_MODE='' REQUEST_IMAGE='' SOURCE_DIR='' REQUEST_VERSION=dev
PORT=8096 BIND=127.0.0.1 PROJECT='' COMPOSE='' MODE='' IMAGE='' IMAGE_ID='' VERSION='' INPUT='' RECOVERY='' MANAGED=true
SERVICE=emby-in-one
usage() {
  cat <<'EOF'
Usage: bash eio-docker.sh [options] ACTION [options]
Actions: install, update, status, start, stop, restart, logs, reset-password,
         update-stable, adopt, uninstall, menu
Options: --project-dir ABSOLUTE_DIR --mode source|image --image TAG_OR_DIGEST
         --source-dir VERIFIED_DIRECTORY --version dev|test-SHA|vSEMVER
         --port HOST_PORT --bind 127.0.0.1|0.0.0.0 --project-name NAME
         --compose-file FILE (adopt only)
Image installs/updates require an explicit non-latest reference and pin its digest.
Source updates require a verified local source directory and never pull an app image.
Initial password is saved in .eio-initial-password (root-only), never printed.
EOF
}
fail(){ printf 'eio-docker: %s\n' "$*" >&2; exit 1; }
while (($#)); do
  case "$1" in
    --project-dir|--mode|--image|--source-dir|--version|--port|--bind|--project-name|--compose-file)
      (($# >= 2)) || fail "missing value for $1"
      case "$1" in
        --project-dir) PROJECT_DIR=$2;; --mode) REQUEST_MODE=$2;; --image) REQUEST_IMAGE=$2;;
        --source-dir) SOURCE_DIR=$2;; --version) REQUEST_VERSION=$2;; --port) PORT=$2;;
        --bind) BIND=$2;; --project-name) PROJECT=$2;; --compose-file) COMPOSE=$2;;
      esac; shift 2;;
    --help|-h) usage; exit 0;;
    install|update|status|start|stop|restart|logs|reset-password|update-stable|adopt|uninstall|menu|change-username)
      [[ -z "$ACTION" ]] || fail 'multiple actions'; ACTION=$1; shift;;
    *) fail "unknown argument: $1";;
  esac
done
[[ -n "$ACTION" ]] || { usage; exit 2; }
[[ "$EUID" == 0 ]] || fail 'root required for protected configuration and backups'
# Capture sensitive stdin before Compose preflight can touch the inherited stream.
PASSWORD_INPUT=''
if [[ "$ACTION" == reset-password || "$ACTION" == change-username ]]; then PASSWORD_INPUT=$(cat); fi
for tool in docker python3 curl flock tar sha256sum; do command -v "$tool" >/dev/null || fail "$tool is required"; done
[[ "$PROJECT_DIR" == /* && "$PROJECT_DIR" != / && "$PROJECT_DIR" != *$'\n'* ]] || fail 'unsafe project directory'
mkdir -p "$PROJECT_DIR"
PROJECT_DIR=$(cd "$PROJECT_DIR" && pwd -P)
STATE="$PROJECT_DIR/.eio-deployment.json"
exec 9>"$PROJECT_DIR/.eio-operation.lock"
flock -n 9 || fail 'another project operation is active'
compose(){
  if docker compose version >/dev/null 2>&1; then docker compose "$@"
  elif command -v docker-compose >/dev/null; then docker-compose "$@"
  else fail 'Docker Compose required'; fi
}
dc(){ EIO_IMAGE="$IMAGE" EIO_HTTP_PORT="$PORT" EIO_BIND_ADDRESS="$BIND" compose --project-directory "$PROJECT_DIR" -p "$PROJECT" -f "$COMPOSE" "$@"; }
cid(){ dc ps --all -q "$SERVICE"; }
is_running(){ [[ -n "${1:-}" && "$(docker inspect -f '{{.State.Running}}' "$1")" == true ]]; }
state_load(){
  local fields
  fields=$(python3 - "$STATE" "$PROJECT_DIR" <<'PY'
import json,sys,re,os
s=json.load(open(sys.argv[1])); keys=['mode','project','compose','image','image_id','version','input','port','bind','recovery','managed']
if set(s)!=set(keys) or s['mode'] not in ['source','image']: raise SystemExit('invalid deployment state')
if not re.fullmatch(r'[a-z0-9][a-z0-9_-]*',s['project']): raise SystemExit('invalid project')
if os.path.dirname(os.path.realpath(s['compose']))!=sys.argv[2]: raise SystemExit('compose must be in deployment directory')
if not re.fullmatch(r'sha256:[0-9a-f]{64}',s['image_id']): raise SystemExit('invalid image identity')
if not (str(s['port']).isdigit() and 0<int(s['port'])<65536): raise SystemExit('invalid port')
if s['bind'] not in ['127.0.0.1','0.0.0.0']: raise SystemExit('invalid bind')
for k in keys:
 v=str(s[k]);
 if '\n' in v or '\r' in v: raise SystemExit('invalid state value')
 print(v if v else '-')
PY
) || fail 'deployment state is invalid (never executed)'
  local a; mapfile -t a <<< "$fields"
  MODE=${a[0]} PROJECT=${a[1]} COMPOSE=${a[2]} IMAGE=${a[3]} IMAGE_ID=${a[4]} VERSION=${a[5]} INPUT=${a[6]} PORT=${a[7]} BIND=${a[8]} RECOVERY=${a[9]} MANAGED=${a[10]}
  [[ "$RECOVERY" != - ]] || RECOVERY=''
}
state_write(){
  python3 - "$STATE" "$MODE" "$PROJECT" "$COMPOSE" "$IMAGE" "$IMAGE_ID" "$VERSION" "$INPUT" "$PORT" "$BIND" "$RECOVERY" "$MANAGED" <<'PY'
import json,sys,os
p=sys.argv[1]; keys=['mode','project','compose','image','image_id','version','input','port','bind','recovery','managed']
t=p+'.tmp'; fd=os.open(t,os.O_WRONLY|os.O_CREAT|os.O_TRUNC,0o600)
with os.fdopen(fd,'w') as f: json.dump(dict(zip(keys,sys.argv[2:])),f,indent=2); f.write('\n'); f.flush(); os.fsync(f.fileno())
os.replace(t,p)
PY
}
validate_context(){
  [[ -f "$COMPOSE" && ! -L "$COMPOSE" ]] || fail 'missing/unsafe Compose file'
  dc config --format json | python3 -c '
import json,sys,os
c=json.load(sys.stdin); p=sys.argv[1]; s=c["services"]
if set(s)!={"emby-in-one"}: raise SystemExit("only a single managed service is supported")
x=s["emby-in-one"]
if str(x.get("user"))!="1000:1000": raise SystemExit("UID must be 1000:1000")
v=x.get("volumes",[])
if len(v)!=2 or {z.get("target") for z in v}!={"/app/config","/app/data"}: raise SystemExit("nonstandard mounts: automatic operation refused")
for z in v:
 if z.get("type")!="bind" or os.path.realpath(z["source"])!=os.path.join(p,z["target"].split("/")[-1]) or z.get("read_only",False): raise SystemExit("nonstandard/shared mount")
if x.get("image")!=sys.argv[2] and not (sys.argv[3]=="false" and "build" in x and not x.get("image")): raise SystemExit("state and Compose image disagree")
if "build" in x and sys.argv[3]=="true": raise SystemExit("managed runtime must not build implicitly")
' "$PROJECT_DIR" "$IMAGE" "$MANAGED" || fail 'Compose/state cross-check failed'
  [[ ! -L "$PROJECT_DIR/config" && ! -L "$PROJECT_DIR/data" ]] || fail 'symlink data directories unsupported'
  if grep -Eq '^[[:space:]]*dataDir:' "$PROJECT_DIR/config/config.yaml"; then
    grep -Eq '^[[:space:]]*dataDir:[[:space:]]*["\x27]?(/app/data|data)["\x27]?[[:space:]]*$' "$PROJECT_DIR/config/config.yaml" || fail 'external dataDir is not managed'
  fi
}
check_writers(){
  local allowed=${1:-} id source ids
  if command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet emby-in-one.service 2>/dev/null; then
    [[ "$(systemctl show -p WorkingDirectory --value emby-in-one.service)" != "$PROJECT_DIR" ]] || fail 'systemd instance writes this deployment'
  fi
  [[ ! -L "$PROJECT_DIR/config/config.yaml" ]] || fail 'symlink config file unsupported'
  ids=$(docker ps --no-trunc -q) || fail 'cannot verify running container writers'
  while read -r id; do
    [[ -n "$id" && "$id" != "$allowed" ]] || continue
    docker inspect "$id" | python3 -c 'import json,sys,os;x=json.load(sys.stdin)[0];p=sys.argv[1];targets=[p+"/config",p+"/data"];assert all(not z.get("RW",False) or not any(os.path.commonpath([os.path.realpath(z["Source"]),t]) in [os.path.realpath(z["Source"]),t] for t in targets) for z in x["Mounts"] if z.get("Type")=="bind"),"another container writes overlapping data"' "$PROJECT_DIR" || fail 'cannot exclude another writer to deployment data'
  done <<< "$ids"
}
stop_checked(){ local id; id=$(cid); [[ -n "$id" ]] || return 0; dc stop -t 60 "$SERVICE" >/dev/null || return 1; ! is_running "$id"; }
health(){
  local id i actual
  id=$(cid); [[ -n "$id" && "$id" != *$'\n'* ]] || return 1
  for ((i=0;i<${EIO_HEALTH_ATTEMPTS:-45};i++)); do
    if is_running "$id" && curl -fsS --max-time 2 "http://127.0.0.1:$PORT/System/Info/Public" >/dev/null 2>&1; then
      actual=$(docker exec "$id" /app/emby-in-one --version 2>/dev/null || true)
      if [[ "$actual" == "$VERSION" && "$(docker inspect -f '{{.Image}}' "$id")" == "$IMAGE_ID" && "$(docker inspect -f '{{.Config.User}}' "$id")" == 1000:1000 ]]; then
        docker inspect "$id" | python3 -c 'import json,sys,os;x=json.load(sys.stdin)[0];p=sys.argv[1];m=x["Mounts"];assert len(m)==2 and {z["Destination"] for z in m}=={"/app/config","/app/data"};assert all(z["Type"]=="bind" and z["RW"] and os.path.realpath(z["Source"])==os.path.join(p,z["Destination"].split("/")[-1]) for z in m)' "$PROJECT_DIR" || return 1
        return 0
      fi
    fi
    sleep 2
  done
  return 1
}
prepare_image(){
  local req=$1
  [[ "$req" =~ ^[a-zA-Z0-9][a-zA-Z0-9._:/@-]*$ && ( "$req" == *@sha256:* || "$req" == *:* ) && "$req" != *:latest ]] || fail 'explicit image tag/digest required; latest is not accepted'
  docker pull "$req" || fail 'pull failed; old deployment untouched (no source fallback)'
  TARGET_IMAGE=$(docker image inspect "$req" --format '{{json .RepoDigests}}' | python3 -c 'import json,sys; a=json.load(sys.stdin); repo=sys.argv[1].split("@")[0]; repo=repo[:repo.rfind(":")] if ":" in repo.split("/")[-1] else repo; matches=[x for x in a or [] if x.split("@")[0]==repo]; print(matches[0] if len(matches)==1 else "")' "$req")
  [[ "$TARGET_IMAGE" == *@sha256:* ]] || fail 'registry did not supply an unambiguous immutable digest'
  TARGET_ID=$(docker image inspect -f '{{.Id}}' "$TARGET_IMAGE")
  TARGET_VERSION=$(docker run --rm --entrypoint /app/emby-in-one "$TARGET_IMAGE" --version) || fail 'target cannot execute on this architecture'
  [[ -n "$TARGET_VERSION" && "$TARGET_VERSION" != *$'\n'* ]] || fail 'invalid target version'
}
prepare_source(){
  [[ -n "$SOURCE_DIR" && -f "$SOURCE_DIR/Dockerfile" && -f "$SOURCE_DIR/go.mod" ]] || fail 'source mode requires a trusted complete source directory'
  local revision=unknown actual
  if git -C "$SOURCE_DIR" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    revision=$(git -C "$SOURCE_DIR" rev-parse HEAD)
    if [[ "$REQUEST_VERSION" == v* || "$REQUEST_VERSION" == V* ]]; then
      actual=$(git -C "$SOURCE_DIR" describe --tags --exact-match HEAD) || fail 'release version requires exact Git tag'
      [[ "${actual#[Vv]}" == "${REQUEST_VERSION#[Vv]}" ]] || fail 'source version/tag mismatch'
      [[ -z "$(git -C "$SOURCE_DIR" status --porcelain --untracked-files=no)" ]] || fail 'release source must be clean'
    fi
  elif [[ "$REQUEST_VERSION" != dev && "$REQUEST_VERSION" != test-* ]]; then fail 'unverified source must use dev/test identity'; fi
  [[ "$REQUEST_VERSION" =~ ^(dev|test-[a-zA-Z0-9.-]+|[vV][0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.-]+)?)$ ]] || fail 'invalid version'
  TARGET_VERSION=${REQUEST_VERSION/#V/v}
  TARGET_IMAGE="eio-source:${PROJECT}-$(date +%s)-$$"
  docker build --build-arg "VERSION=$TARGET_VERSION" --build-arg "REVISION=$revision" -t "$TARGET_IMAGE" "$SOURCE_DIR" || fail 'source build failed; old deployment untouched'
  TARGET_ID=$(docker image inspect -f '{{.Id}}' "$TARGET_IMAGE")
}
write_compose(){
  local tpl="$SELF_DIR/docker-compose.image.yml"
  [[ -f "$tpl" ]] || fail 'trusted image Compose template missing'
  if [[ -e "$COMPOSE" ]]; then cmp -s "$tpl" "$COMPOSE" || fail 'existing custom managed Compose retained; review manually'; else cp "$tpl" "$COMPOSE"; fi
}
init_config(){
  mkdir -p "$PROJECT_DIR/config" "$PROJECT_DIR/data"
  if [[ ! -f "$PROJECT_DIR/config/config.yaml" ]]; then
    local pass; pass=$(head -c 32 /dev/urandom | base64 | tr -d '\n')
    printf '%s\n' "$pass" > "$PROJECT_DIR/.eio-initial-password"
    printf 'server:\n  port: 8096\n  name: Emby-In-One\nadmin:\n  username: admin\n  password: "%s"\nplayback:\n  mode: proxy\nproxies: []\nupstream: []\n' "$pass" > "$PROJECT_DIR/config/config.yaml"
    unset pass
  fi
  chmod 700 "$PROJECT_DIR/config" "$PROJECT_DIR/data"; chmod 600 "$PROJECT_DIR/config/config.yaml"
  chown -R 1000:1000 "$PROJECT_DIR/config" "$PROJECT_DIR/data"
}
backup(){
  local needed free
  needed=$(du -sk "$PROJECT_DIR/config" "$PROJECT_DIR/data" | awk '{s+=$1}END{print s*2+102400}')
  free=$(df -Pk "$PROJECT_DIR" | awk 'END{print $4}')
  (( free > needed )) || return 1
  BACKUP="$PROJECT_DIR/.eio-recovery-$(date -u +%Y%m%dT%H%M%SZ)-$$"
  mkdir -m 700 "$BACKUP" || return 1
  cp -p "$STATE" "$BACKUP/state.json" || return 1
  cp -p "$COMPOSE" "$BACKUP/compose.yml" || return 1
  tar -cpf "$BACKUP/data.tar" -C "$PROJECT_DIR" config data || return 1
  tar -tf "$BACKUP/data.tar" >/dev/null || return 1
  docker tag "$IMAGE_ID" "eio-rollback:${PROJECT}-$(date +%s)-$$" || return 1
}
SWITCHING=false BACKUP='' OLD_RUNNING=false
restore(){
  SWITCHING=false
  printf 'Deployment failed; restoring paired pre-upgrade image/config/data snapshot.\n' >&2
  stop_checked || { printf 'Cannot stop failed target. Do NOT restore over a writer. Evidence: %s\n' "$BACKUP" >&2; return 1; }
  mkdir -m 700 "$BACKUP/failed"
  mv "$PROJECT_DIR/config" "$PROJECT_DIR/data" "$BACKUP/failed/" || return 1
  cp -p "$STATE" "$BACKUP/failed/state.json" || true
  tar -xpf "$BACKUP/data.tar" -C "$PROJECT_DIR" || return 1
  cp -p "$BACKUP/state.json" "$STATE"; state_load
  cp -p "$BACKUP/compose.yml" "$COMPOSE"
  docker image inspect "$IMAGE_ID" >/dev/null || return 1
  dc up -d --no-build --pull never "$SERVICE" >/dev/null || return 1
  health || return 1
  if [[ "$OLD_RUNNING" == false ]]; then stop_checked || return 1; fi
  printf 'Rollback verified; failed-stage evidence: %s\n' "$BACKUP" >&2
}
on_exit(){
  local code=$?; trap - EXIT INT TERM
  if [[ "$SWITCHING" == true ]]; then restore || { printf 'ROLLBACK FAILED; preserve evidence at %s\n' "$BACKUP" >&2; code=1; }; fi
  exit "$code"
}
trap on_exit EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
if [[ -f "$STATE" ]]; then state_load; fi
[[ -n "$PROJECT" ]] || PROJECT="$(basename "$PROJECT_DIR" | tr '[:upper:]_' '[:lower:]-')"
[[ "$PROJECT" =~ ^[a-z0-9][a-z0-9_-]*$ ]] || fail 'invalid Compose project name'
[[ "$PORT" =~ ^[0-9]{1,5}$ ]] && ((10#$PORT>0 && 10#$PORT<65536)) || fail 'invalid port'
[[ "$BIND" == 127.0.0.1 || "$BIND" == 0.0.0.0 ]] || fail 'unsupported bind address'
require_state(){ [[ -f "$STATE" ]] || fail 'no managed state; use adopt with an explicit Compose file, or install in a fresh directory'; validate_context; }
apply(){
  local exists=false
  if [[ -f "$STATE" ]]; then
    exists=true; validate_context
    [[ "$MANAGED" == true ]] || fail 'legacy custom deployment: update requires reviewed migration; no implicit conversion'
    [[ -z "$REQUEST_MODE" || "$REQUEST_MODE" == "$MODE" ]] || fail 'deployment modes cannot change silently'
  else
    [[ "$ACTION" == install ]] || fail 'install before update'
    [[ "$REQUEST_MODE" == source || "$REQUEST_MODE" == image ]] || fail 'install requires --mode source|image'
    MODE=$REQUEST_MODE COMPOSE="$PROJECT_DIR/.eio-compose.yml"
    [[ ! -e "$PROJECT_DIR/docker-compose.yml" && ! -e "$PROJECT_DIR/docker-compose.image.yml" && ! -e "$COMPOSE" ]] || fail 'existing Compose retained; adopt explicitly or use a fresh directory'
  fi
  # Fetch/build BEFORE stopping or changing any old config/state.
  if [[ "$MODE" == image ]]; then [[ -n "$REQUEST_IMAGE" && -z "$SOURCE_DIR" ]] || fail 'image mode requires --image and forbids --source-dir'; prepare_image "$REQUEST_IMAGE"
  else [[ -z "$REQUEST_IMAGE" ]] || fail 'source mode never accepts an image update'; prepare_source; fi
  local old_id=''
  if [[ "$exists" == true ]]; then
    old_id=$(cid); check_writers "$old_id"
    if is_running "$old_id"; then OLD_RUNNING=true; fi
    stop_checked || fail 'old service did not stop; backup/switch refused'
    if ! backup; then
      if [[ "$OLD_RUNNING" == true ]]; then
        dc start "$SERVICE" >/dev/null && health || fail 'backup failed and old service did not recover health'
      fi
      fail 'backup failed; target not applied'
    fi
    SWITCHING=true
  else
    check_writers
    init_config
    write_compose
  fi
  IMAGE=$TARGET_IMAGE IMAGE_ID=$TARGET_ID VERSION=$TARGET_VERSION
  if [[ "$MODE" == image ]]; then INPUT=$REQUEST_IMAGE; else INPUT=$SOURCE_DIR; fi
  RECOVERY=$BACKUP MANAGED=true
  state_write
  validate_context
  dc up -d --no-build --pull never "$SERVICE" >/dev/null || fail 'target start failed'
  if ! health; then
    if [[ "$exists" == false ]]; then stop_checked || true; fi
    fail 'target failed bounded health/version/image validation'
  fi
  if [[ "$exists" == true && "$OLD_RUNNING" == false ]]; then stop_checked || fail 'could not preserve prior stopped state'; fi
  SWITCHING=false
  printf 'Verified deployment mode=%s version=%s image=%s\n' "$MODE" "$VERSION" "$IMAGE"
}
reset_password(){
  local pw old=false id rc=0
  pw=$PASSWORD_INPUT; unset PASSWORD_INPUT; [[ -n "$pw" ]] || fail 'password must be supplied via stdin'
  id=$(cid); check_writers "$id"
  if is_running "$id"; then old=true; fi
  stop_checked || fail 'service still running; refusing password reset'
  local diagnostic="$PROJECT_DIR/.eio-password-diagnostic.$$"
  printf '%s' "$pw" | dc run --rm --no-deps -T --pull never "$SERVICE" /app/emby-in-one --reset-password - >"$diagnostic" 2>&1 || rc=$?
  unset pw
  if ((rc!=0)); then
    printf 'eio-docker: password reset CLI exit=%s\n' "$rc" >&2
    if grep -qi 'unknown.*flag' "$diagnostic"; then printf 'eio-docker: reset diagnostic=unsupported-compose-option\n' >&2; fi
    if grep -qi 'still running' "$diagnostic"; then printf 'eio-docker: reset diagnostic=service-stop-guard\n' >&2; fi
    if grep -qi 'permission denied' "$diagnostic"; then printf 'eio-docker: reset diagnostic=permission-denied\n' >&2; fi
    if grep -qi 'password.*empty' "$diagnostic"; then printf 'eio-docker: reset diagnostic=empty-stdin\n' >&2; fi
  else rm -f "$diagnostic"; fi
  if [[ "$old" == true ]]; then dc start "$SERVICE" >/dev/null && health || fail 'password operation ended but service recovery failed'; fi
  ((rc==0)) || fail 'password reset failed'
  printf 'Password reset completed; prior running state preserved.\n'
}
update_stable(){
  local tag tmp ref
  tag=$(curl -fsS --max-time 20 https://api.github.com/repos/ArizeSky/Emby-In-One/releases/latest | python3 -c 'import sys,json,re; r=json.load(sys.stdin); t=r["tag_name"]; assert not r["draft"] and not r["prerelease"] and re.fullmatch(r"[Vv][0-9]+\.[0-9]+\.[0-9]+",t); print(t)') || fail 'cannot verify latest stable Release'
  python3 - "$VERSION" "$tag" <<'PY'
import re,sys
if not re.fullmatch(r'[vV]?\d+\.\d+\.\d+(?:-[A-Za-z0-9.-]+)?',sys.argv[1]): raise SystemExit('unknown current version: specify an explicit target')
a,b=[tuple(map(int,re.match(r'[vV]?(\d+)\.(\d+)\.(\d+)',v).groups())) for v in sys.argv[1:]]
if b<a: raise SystemExit('stable update would downgrade current version')
PY
  if [[ "$MODE" == image ]]; then REQUEST_IMAGE="ghcr.io/arizesky/emby-in-one:v${tag#[Vv]}"; apply
  else
    tmp=$(mktemp -d); ref="https://github.com/ArizeSky/Emby-In-One/releases/download/$tag/Emby-In-One-docker-v${tag#[Vv]}.tar.gz"
    curl -fsS --max-time 120 "$ref" -o "$tmp/source.tar.gz" && curl -fsS --max-time 30 "$ref.sha256" -o "$tmp/source.sha256" || { rm -rf "$tmp"; fail 'verified source package unavailable'; }
    python3 - "$tmp" "${ref##*/}" <<'PY'
import hashlib,sys,pathlib,tarfile
p=pathlib.Path(sys.argv[1]); rows=[l.split() for l in (p/'source.sha256').read_text().splitlines()]; matches=[r[0] for r in rows if len(r)==2 and r[1].lstrip('*')==sys.argv[2]]
assert len(matches)==1 and matches[0].lower()==hashlib.sha256((p/'source.tar.gz').read_bytes()).hexdigest(),'source checksum mismatch'
with tarfile.open(p/'source.tar.gz') as t:
 for m in t.getmembers():
  assert not m.issym() and not m.islnk() and not m.name.startswith('/') and '..' not in pathlib.PurePosixPath(m.name).parts,'unsafe source archive'
 t.extractall(p/'source',filter='data')
PY
    SOURCE_DIR=$(find "$tmp/source" -name go.mod -print -quit); SOURCE_DIR=${SOURCE_DIR%/go.mod}
    # Archive bytes were verified, but no trusted Git identity is imported: label dev.
    REQUEST_VERSION=dev; apply; rm -rf "$tmp"
  fi
}
case "$ACTION" in
  install|update) apply;;
  adopt)
    [[ ! -f "$STATE" && -n "$COMPOSE" && "$COMPOSE" == "$PROJECT_DIR/"* ]] || fail 'adopt requires an explicit in-project Compose file and no existing state'
    MANAGED=false
    local_json=$(compose --project-directory "$PROJECT_DIR" -p "$PROJECT" -f "$COMPOSE" config --format json)
    MODE=$(printf '%s' "$local_json" | python3 -c 'import json,sys;s=json.load(sys.stdin)["services"]["emby-in-one"];b="build" in s;i="image" in s; assert b!=i,"ambiguous build/image"; print("source" if b else "image")')
    old=$(compose --project-directory "$PROJECT_DIR" -p "$PROJECT" -f "$COMPOSE" ps --all -q "$SERVICE")
    [[ -n "$old" && "$old" != *$'\n'* ]] || fail 'adopt needs exactly one existing service container'
    IMAGE=$(docker inspect -f '{{.Config.Image}}' "$old"); IMAGE_ID=$(docker inspect -f '{{.Image}}' "$old"); VERSION=$(docker exec "$old" /app/emby-in-one --version); INPUT=$IMAGE
    validate_context; state_write; printf 'Legacy deployment adopted for lifecycle only; updates require reviewed migration.\n';;
  status)
    require_state; dc ps --all "$SERVICE"; id=$(cid)
    if [[ -n "$id" ]]; then
      [[ "$(docker inspect -f '{{.Image}}' "$id")" == "$IMAGE_ID" ]] || fail 'running/container image differs from managed state'
      if is_running "$id"; then
        actual=$(docker exec "$id" /app/emby-in-one --version)
        [[ "$actual" == "$VERSION" ]] || fail 'runtime version differs from state'
        printf 'runtime_version=%s\n' "$actual"
      fi
    fi
    printf 'mode=%s recorded_version=%s image=%s recovery=%s\n' "$MODE" "$VERSION" "$IMAGE" "$RECOVERY";;
  start|restart) require_state; check_writers "$(cid)"; if [[ "$ACTION" == restart ]]; then stop_checked || fail 'stop failed'; fi; dc up -d --no-build --pull never "$SERVICE" >/dev/null; health || fail 'service did not become healthy';;
  stop) require_state; stop_checked || fail 'service did not stop';;
  logs) require_state; dc logs --tail 100 "$SERVICE";;
  reset-password) require_state; reset_password;;
  change-username)
    require_state; username=$PASSWORD_INPUT; unset PASSWORD_INPUT; [[ "$username" =~ ^[A-Za-z0-9_-]+$ ]] || fail 'invalid username'
    id=$(cid); running=false; if is_running "$id"; then running=true; fi
    check_writers "$id"; stop_checked || fail 'stop failed; username unchanged'
    awk -v val="$username" '/^  username:/{print "  username: " val; next}1' "$PROJECT_DIR/config/config.yaml" > "$PROJECT_DIR/config/config.yaml.tmp"
    chmod 600 "$PROJECT_DIR/config/config.yaml.tmp"; chown 1000:1000 "$PROJECT_DIR/config/config.yaml.tmp"; mv "$PROJECT_DIR/config/config.yaml.tmp" "$PROJECT_DIR/config/config.yaml"
    if [[ "$running" == true ]]; then dc start "$SERVICE" >/dev/null; health || fail 'restart failed'; fi;;
  update-stable) require_state; update_stable;;
  uninstall) require_state; dc down; printf 'Service removed; config/data/backups/images retained at %s\n' "$PROJECT_DIR";;
  menu)
    require_state
    # Release the operation lock between menu actions; children take their own lock.
    flock -u 9
    while :; do
      printf '\nEmby In One (%s) %s\n1 start  2 restart  3 stop  4 stable update  5 status  7 username  8 reset password  9 logs  15 explicit update  0 exit\n' "$MODE" "$VERSION"
      read -r -p '> ' choice || break
      case "$choice" in
        0) break;; 1) cmd=start;; 2) cmd=restart;; 3) cmd=stop;; 4) cmd=update-stable;; 5) cmd=status;; 9) cmd=logs;;
        7) read -r -p 'New username: ' username; printf '%s' "$username" | bash "$0" --project-dir "$PROJECT_DIR" change-username; continue;;
        8) read -r -s -p 'New password: ' pw; printf '\n'; printf '%s' "$pw" | bash "$0" --project-dir "$PROJECT_DIR" reset-password; unset pw; continue;;
        15) if [[ "$MODE" == image ]]; then read -r -p 'Exact image tag/digest (no latest): ' ref; bash "$0" --project-dir "$PROJECT_DIR" update --image "$ref" || true; else read -r -p 'Verified extracted source directory: ' ref; bash "$0" --project-dir "$PROJECT_DIR" update --source-dir "$ref" || true; fi; continue;;
        *) continue;;
      esac
      bash "$0" --project-dir "$PROJECT_DIR" "$cmd" || true
    done;;
  *) fail 'unsupported action';;
esac
