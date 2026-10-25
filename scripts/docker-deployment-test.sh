#!/usr/bin/env bash
# Real integration; image mode NEVER builds. Secrets/raw logs remain in protected work dir.
set -Eeuo pipefail
umask 077
ROOT=$(cd "$(dirname "$0")/.." && pwd)
route=${1:?image|source}; shift
if [[ "$route" == image ]]; then base=${1:?}; upgrade=${2:?}; bad=${3:?}; out=${4:-$(mktemp -d)}
elif [[ "$route" == source ]]; then source=${1:?}; out=${2:-$(mktemp -d)}
else exit 2; fi
mkdir -p "$out"; out=$(cd "$out" && pwd)
work=$(mktemp -d "${EIO_ACCEPTANCE_ROOT:-/tmp}/eio-acceptance-XXXXXX")
project="$work/deployment"; export EIO_HEALTH_ATTEMPTS=10
real_docker=$(command -v docker); real_tar=$(command -v tar)
name="eioaccept$(date +%s)$$"
port=$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1]);s.close()')
export REAL_DOCKER="$real_docker" REAL_TAR="$real_tar" EIO_COMMAND_LOG="$work/commands" EIO_FAULT_COUNT="$work/fault-count"
mkdir "$work/bin"
cat > "$work/bin/docker" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$EIO_COMMAND_LOG"
case "${EIO_FAULT:-}" in
  pull|permission|network) if [[ "$1" == pull ]]; then echo "injected ${EIO_FAULT} failure" >&2; exit 91; fi;;
  stop) if [[ " $* " == *' stop '* ]]; then exit 92; fi;;
  build) if [[ "$1" == build ]]; then exit 97; fi;;
  rollback) if [[ " $* " == *' up '* ]]; then n=$(cat "$EIO_FAULT_COUNT" 2>/dev/null || echo 0); n=$((n+1)); echo "$n" > "$EIO_FAULT_COUNT"; if ((n>1)); then exit 93; fi; fi;;
esac
if [[ "${EIO_TEST_ROUTE:-}" == image && "$1" == build ]]; then echo 'image route attempted a build' >&2; exit 94; fi
if [[ "${EIO_TEST_ROUTE:-}" == source && "$1" == pull ]]; then echo 'source route attempted app pull' >&2; exit 95; fi
exec "$REAL_DOCKER" "$@"
EOF
cat > "$work/bin/tar" <<'EOF'
#!/usr/bin/env bash
if [[ "${EIO_FAULT:-}" == backup && "$1" == -cpf ]]; then exit 96; fi
exec "$REAL_TAR" "$@"
EOF
chmod 700 "$work/bin/"*
export PATH="$work/bin:$PATH" EIO_TEST_ROUTE="$route"
manager(){ bash "$ROOT/eio-docker.sh" --project-dir "$project" "$@"; }
cleanup(){
  local rc=$?
  "$real_docker" ps --all --filter "label=com.docker.compose.project=$name" -q | xargs -r "$real_docker" rm -f >/dev/null 2>&1 || true
  "$real_docker" network rm "${name}_default" >/dev/null 2>&1 || true
  printf 'route=%s\nexit=%s\nprotected_evidence=%s\n' "$route" "$rc" "$work" >> "$out/$route-summary.txt"
}
trap cleanup EXIT
diagnose(){
  printf 'Integration failed at line %s; protected evidence: %s\n' "$1" "$work" >&2
  # Manager messages contain only fixed categories, not supplied credentials.
  for log in "$work/install.log" "$work/reset.log" "$work/update.log" "$work/bad.log"; do
    [[ ! -f "$log" ]] || grep -E '^eio-docker:|^ROLLBACK FAILED' "$log" >&2 || true
  done
}
trap 'diagnose "$LINENO"' ERR
if [[ "$route" == image ]]; then
  EIO_INSTALL_CLI=false bash "$ROOT/install.sh" --project-dir "$project" --mode image --image "$base" --project-name "$name" --port "$port" > "$work/install.log" 2>&1
else
  EIO_INSTALL_CLI=false bash "$source/install.sh" --project-dir "$project" --mode source --project-name "$name" --port "$port" > "$work/install.log" 2>&1
fi
manager status > "$work/status.log"
manager logs > "$work/app.log"
getstate(){ python3 -c 'import json,sys;print(json.load(open(sys.argv[1]))[sys.argv[2]])' "$project/.eio-deployment.json" "$1"; }
old_id=$(getstate image_id)
if [[ "$route" == image ]]; then [[ "$(getstate image)" == *@sha256:* ]]; fi
password=$(cat "$project/.eio-initial-password")
printf '%s' "$password" > "$work/password"
auth(){
  python3 - "$port" "$work/password" <<'PY'
import json,urllib.request,sys
p=open(sys.argv[2]).read()
r=urllib.request.Request('http://127.0.0.1:'+sys.argv[1]+'/Users/AuthenticateByName',data=json.dumps({'Username':'admin','Pw':p}).encode(),headers={'Content-Type':'application/json'})
with urllib.request.urlopen(r,timeout=15) as h:
 d=json.load(h); assert d.get('AccessToken'),'missing token'
PY
}
auth
newpw=$(head -c 24 /dev/urandom | base64)
printf '%s' "$newpw" | manager reset-password > "$work/reset.log" 2>&1
printf '%s' "$newpw" > "$work/password"; unset newpw password
auth
# Stopped service remains stopped after a password reset.
manager stop
printf 'new-stopped-test-password-%s' "$RANDOM" > "$work/password"
manager reset-password < "$work/password" >> "$work/reset.log" 2>&1
[[ -z "$("$real_docker" ps --filter "label=com.docker.compose.project=$name" -q)" ]]
manager start; auth
printf 'persistent\n' > "$project/data/acceptance-sentinel"
chown 1000:1000 "$project/data/acceptance-sentinel"
manager restart
[[ "$(cat "$project/data/acceptance-sentinel")" == persistent ]]
# Add a logical SQLite fixture only while stopped; preserve it through restart/recovery.
manager stop
python3 - "$project/data/mappings.db" <<'PY'
import sqlite3,sys
with sqlite3.connect(sys.argv[1]) as c:
 c.execute('CREATE TABLE IF NOT EXISTS acceptance_marker (value TEXT)')
 c.execute('DELETE FROM acceptance_marker'); c.execute('INSERT INTO acceptance_marker VALUES (?)',('persistent',))
PY
chown 1000:1000 "$project/data/mappings.db"
manager start
if [[ "$route" == image ]]; then
  for fault in pull permission network stop backup; do
    printf 'Checking injected fault: %s\n' "$fault"
    if EIO_FAULT="$fault" manager update --image "$upgrade" > "$work/failure-$fault.log" 2>&1; then echo "fault $fault unexpectedly passed" >&2; exit 1; fi
    [[ "$(getstate image_id)" == "$old_id" ]]
    curl -fsS --max-time 3 "http://127.0.0.1:$port/System/Info/Public" >/dev/null
  done
  if manager update --image "${base%:*}:definitely-missing-acceptance-image" > "$work/missing.log" 2>&1; then exit 1; fi
  [[ "$(getstate image_id)" == "$old_id" ]]
  manager update --image "$upgrade" > "$work/update.log" 2>&1
  upgraded_id=$(getstate image_id); [[ "$upgraded_id" != "$old_id" ]]
  auth
  if manager update --image "$bad" > "$work/bad.log" 2>&1; then echo 'bad health was accepted' >&2; exit 1; fi
  [[ "$(getstate image_id)" == "$upgraded_id" ]]
  [[ ! -e "$project/data/failure-created" ]]
  curl -fsS --max-time 3 "http://127.0.0.1:$port/System/Info/Public" >/dev/null
  python3 - "$project/data/mappings.db" <<'PY'
import sqlite3,sys
with sqlite3.connect(sys.argv[1]) as c: assert c.execute('SELECT value FROM acceptance_marker').fetchall()==[('persistent',)]
PY
  auth
  rm -f "$EIO_FAULT_COUNT"
  if EIO_FAULT=rollback manager update --image "$bad" > "$work/rollback-failure.log" 2>&1; then exit 1; fi
  grep -q 'ROLLBACK FAILED' "$work/rollback-failure.log"
  [[ "$(getstate image_id)" == "$upgraded_id" ]]
  # Recovery failure must be explicit, then the operator can start the restored old state.
  manager start; auth
  if manager update --mode source --source-dir "$ROOT" > "$work/mode-refused.log" 2>&1; then exit 1; fi
  printf 'image_install=digest-pinned\nlogin=pass\npassword_reset=pass\npersistent_sqlite=pass\nmissing_pull_permission_network=old-preserved\nstop_backup_failures=old-preserved\nupgrade=pass\nhealth_failure=rollback-verified\nrollback_failure=explicit-nonzero\nmode_switch=refused\n' > "$out/image-summary.txt"
else
  if manager update --image example.invalid/app:never > "$work/source-refused.log" 2>&1; then exit 1; fi
  [[ "$(getstate mode)" == source ]]
  if EIO_FAULT=build manager update --source-dir "$source" > "$work/build-failure.log" 2>&1; then exit 1; fi
  [[ "$(getstate image_id)" == "$old_id" ]]
  curl -fsS --max-time 3 "http://127.0.0.1:$port/System/Info/Public" >/dev/null
  manager update --source-dir "$source" --version test-source-acceptance > "$work/source-update.log" 2>&1
  [[ "$(getstate image_id)" != "$old_id" ]]
  auth
  printf 'source_install=real-build-on-github\nsource_update=real-build-on-github\nbuild_failure=old-preserved\nlogin=pass\npassword_reset=pass\npersistence=pass\nimage_fallback=refused\n' > "$out/source-summary.txt"
fi
# Reinstall respects custom Compose: refusal, not overwrite.
printf '\n# operator customization\n' >> "$project/.eio-compose.yml"
before=$(sha256sum "$project/.eio-compose.yml" | awk '{print $1}')
# Normal reinstall must preserve operator customization as well as config/data.
if [[ "$route" == image ]]; then
  EIO_INSTALL_CLI=false bash "$ROOT/install.sh" --project-dir "$project" --mode image --image "$upgrade" > "$work/reinstall.log" 2>&1
else
  EIO_INSTALL_CLI=false bash "$source/install.sh" --project-dir "$project" --mode source > "$work/reinstall.log" 2>&1
fi
auth
# An incompatible mode request is refused too.
if manager install --mode invalid > "$work/reinstall-refused.log" 2>&1; then exit 1; fi
[[ "$(sha256sum "$project/.eio-compose.yml" | awk '{print $1}')" == "$before" ]]
printf 'custom_files=retained\n' >> "$out/$route-summary.txt"
printf '%s deployment acceptance passed. Protected runtime evidence: %s\n' "$route" "$work"
