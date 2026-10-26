#!/usr/bin/env bash
set -euo pipefail
[[ $# -ge 2 && $# -le 3 ]] || { echo 'usage: docker-image-smoke.sh IMAGE VERSION [REVISION]' >&2; exit 2; }
image=$1 version=$2 revision=${3:-}
work=$(mktemp -d); name="eio-smoke-$$-$RANDOM"
cleanup(){ docker rm -f "$name" >/dev/null 2>&1 || true; if [[ $EUID == 0 ]]; then rm -rf "$work"; else sudo rm -rf "$work"; fi; }
trap cleanup EXIT
platform=(); [[ -z "${EIO_PLATFORM:-}" ]] || platform=(--platform "$EIO_PLATFORM")
mkdir -p "$work/config" "$work/data"
password=$(head -c 24 /dev/urandom | base64)
printf 'server:\n  port: 8096\nadmin:\n  username: admin\n  password: "%s"\nupstream: []\nproxies: []\n' "$password" > "$work/config/config.yaml"
unset password
chmod 700 "$work/config" "$work/data"; chmod 600 "$work/config/config.yaml"
if [[ $EUID == 0 ]]; then chown -R 1000:1000 "$work/config" "$work/data"; else sudo chown -R 1000:1000 "$work/config" "$work/data"; fi
[[ "$(docker run --rm "${platform[@]}" --entrypoint /app/emby-in-one "$image" --version)" == "$version" ]]
[[ "$(docker run --rm "${platform[@]}" --entrypoint id "$image" -u)" == 1000 ]]
if [[ -n "$revision" ]]; then [[ "$(docker image inspect -f '{{index .Config.Labels "org.opencontainers.image.revision"}}' "$image")" == "$revision" ]]; fi
docker run -d "${platform[@]}" --name "$name" --network none --user 1000:1000 -v "$work/config:/app/config" -v "$work/data:/app/data" "$image" >/dev/null
for ((i=0;i<60;i++)); do
  if docker exec "$name" wget -qO- -T 2 http://127.0.0.1:8096/System/Info/Public >/dev/null 2>&1; then
    [[ "$(docker exec "$name" /app/emby-in-one --version)" == "$version" ]]
    docker exec "$name" sh -c 'test -w /app/config && test -w /app/data && printf persistent > /app/data/.smoke-persistence'
    docker restart "$name" >/dev/null
    [[ "$(docker exec "$name" cat /app/data/.smoke-persistence)" == persistent ]]
    printf 'Smoke passed image=%s version=%s platform=%s\n' "$image" "$version" "${EIO_PLATFORM:-native}"
    exit 0
  fi
  sleep 2
done
echo 'bounded image health smoke timed out' >&2
exit 1
