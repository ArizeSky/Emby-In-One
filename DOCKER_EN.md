# Docker deployment and operations

[README](README_EN.md) · [中文](DOCKER.md) · [Maintainer release policy (Chinese)](RELEASING.md)

This guide separates current behavior from operating conventions. A GitHub Release does **not** mean a matching GHCR image exists. Verify availability, platforms and digest separately. Parental controls in rc3 still need testing and adjustment, particularly for large libraries, upstream rate limits and client compatibility; see [the changelog](Update.md).

## Managed installation and current release protection

The current main branch supports `install.sh --mode source|image`; existing rc3 Release source assets do not contain the new manager. No mode still means source. Image mode requires `--image <published-tag-or-digest>`, pins the resolved digest, and never falls back to a source build. Source builds default to `dev` unless an exact trusted Git release identity is verified. For the published rc3 image, obtain the current tools and select image mode:

```bash
git clone https://github.com/ArizeSky/Emby-In-One.git
cd Emby-In-One
sudo bash install.sh --mode image --image ghcr.io/arizesky/emby-in-one@sha256:2e9a60aea67a4716b1ec4ba10a2eaf3978acab9b66a038c8aa7b46343e047f0c
sudo bash /opt/emby-in-one/eio-docker.sh status
sudo bash /opt/emby-in-one/eio-docker.sh update --image ghcr.io/arizesky/emby-in-one@sha256:2e9a60aea67a4716b1ec4ba10a2eaf3978acab9b66a038c8aa7b46343e047f0c
```

Requires Linux root, Docker/Compose, Python 3, curl, flock and tar. New installs bind loopback by default and use UID 1000. The random initial password is written only to root-only `.eio-initial-password`. State is non-executable JSON, cross-checked with Compose and the actual container; all lifecycle commands use a project/file/service context, not a fixed container name. `--project-dir` supports isolated deployments. The binary/systemd CLI remains independent.

The update command above illustrates explicit image updates; replace its reference with the published digest of the intended version when upgrading. The SSH menu supports new managed source/image deployments. A hand-written Compose project is not silently adopted: explicit `adopt --compose-file <absolute-path>` allows lifecycle operations for a recognized standard project, but automatic legacy/custom migration is refused. Do not mix manager and manual Compose operations or share writable config/data with another instance. Source updates accept a verified `--source-dir`, never an image; image updates only pull. Stable updates require the exact stable Release image/source asset to exist and pass verification; no latest fallback or silent downgrade.

Updates prefetch/build before stopping, verify stop, snapshot complete config/data (including existing WAL/SHM), then validate health plus actual version/image/UID/mounts. Failure restores the paired old data/config snapshot and image, retaining failed-stage evidence in `.eio-recovery-*`. A failed rollback returns nonzero and retains evidence. Only managed standard mounts qualify; external databases/shared writers/custom layouts require manual work. Post-backup failed-stage writes may be lost; this is not zero-downtime or a cross-version database compatibility guarantee. Password reset uses stdin, verified stop and prior-running-state restoration, never automatic force. Uninstall retains data/backups/images.

The Docker workflow is manual-only and defaults to validation. Publication requires an exact tag/full SHA, immutable version tags and a separate package-write job; branches do not auto-write latest. Explicit latest promotion requires the exact current published stable Release, never a prerelease. Successful builds or isolated registry tests do not prove that a public GHCR version exists. The manual sections below describe unmanaged Compose; use them only for that route.

## 1. Choose one deployment route

| Route | Entry point | Updates |
| --- | --- | --- |
| Published image | `docker-compose.image.yml`, `image:` only | Pin a new reference, then `pull` + `up -d --no-build` |
| Local source build | Repository `docker-compose.yml`, with `build:` | Verify/update source, build locally, recreate container |
| Managed installer / SSH menu | `install.sh --mode source` or `--mode image --image REF` | Preserves source/image mode; bounded health and paired recovery |
| Release binary | `release-install.sh` + systemd | No container; see README |

`Emby-In-One-docker-v*.tar.gz` is source, not a `docker load` image. GitHub's automatically generated Source code archives, the project's Docker source asset, and GHCR images are different artifacts.


## 2. Image names, versions and platforms

- Official image: `ghcr.io/arizesky/emby-in-one`. The published public `v1.4.4-rc3` image supports anonymous pulls for `linux/amd64` and `linux/arm64`; Docker selects the matching architecture. This is not the six-architecture Release binary matrix. ARMv7/MIPS/RISC-V users need a matching binary or a separately verified local build.
- Version is derived from the exact release tag and verified full SHA; arbitrary version overrides are not supported. Validation does not publish.
- No `latest` tag is currently provided. Use `v1.4.4-rc3` or its fixed multi-platform index digest below. Other versions must be checked independently; a Release or successful build does not guarantee a published image. Although the workflow refuses conflicting version-tag overwrites, a digest pins the exact manifest bytes.
- An image ID, index digest, source commit SHA and Release asset checksum are different identifiers. Record them separately. Base images/packages/toolchains are not fully pinned, so rebuilding the same source does not promise byte-for-byte reproduction.

The current rc3 multi-platform index digest is `sha256:2e9a60aea67a4716b1ec4ba10a2eaf3978acab9b66a038c8aa7b46343e047f0c`. Inspect it on a machine with Docker/Buildx:

```bash
IMAGE_REFERENCE='ghcr.io/arizesky/emby-in-one@sha256:2e9a60aea67a4716b1ec4ba10a2eaf3978acab9b66a038c8aa7b46343e047f0c'
docker buildx imagetools inspect "$IMAGE_REFERENCE"
```

Image examples in this guide pin that published rc3 index, not a single-platform manifest. Verify the intended version's digest when changing releases. No GHCR login is needed for rc3. For other private packages, use appropriate `read:packages` access with `docker login ghcr.io --password-stdin`; never put registry tokens in Compose, build arguments or Git. A 403/denied response may indicate package permissions rather than a network problem.

## 3. First installation using a published image

The following assumes Linux, Docker Engine and Compose v2, in a new deployment directory. Use sudo/root where directory ownership requires it. Docker Desktop, rootless Docker, user namespace remapping and SELinux require environment-specific validation.

```bash
sudo mkdir -p /opt/emby-in-one/config /opt/emby-in-one/data
cd /opt/emby-in-one
```

Copy the reviewed [docker-compose.image.yml](docker-compose.image.yml) here. Do not layer the source-build Compose file onto it. Create `config/config.yaml`, for example with `sudoedit`, replacing the password placeholder:

```yaml
server:
  port: 8096
  name: "Emby-In-One"
  trustProxy: false
admin:
  username: "admin"
  password: "REPLACE_WITH_A_STRONG_RANDOM_PASSWORD"
playback:
  mode: "proxy"
proxies: []
upstream: []
```

The application requires this file and valid admin credentials; the image does not generate them. The admin password is hashed after startup, while upstream secrets must still be protected.

```bash
sudo chown -R 1000:1000 config data
sudo chmod 700 config data
sudo chmod 600 config/config.yaml
```

Mount the **whole writable config directory**, not just a read-only config file: atomic config updates, server ID creation and password hashing require it. Mount data at `/app/data`; a custom `dataDir` must use a container path with its own persistent mount, not an unmounted host path. Do not work around permission problems using root, privileged mode, a Docker socket mount or chmod 777. Docker-group access is effectively host-root access.

Create `.env.image` (the example pins the published rc3 image):

```dotenv
EIO_IMAGE=ghcr.io/arizesky/emby-in-one@sha256:2e9a60aea67a4716b1ec4ba10a2eaf3978acab9b66a038c8aa7b46343e047f0c
EIO_BIND_ADDRESS=127.0.0.1
EIO_HTTP_PORT=8096
```

These variables configure Compose, not application credentials. Shell-exported variables can override this file; inspect the effective image. All remaining commands assume the deployment directory and the same Bash session; redefine the helper in new sessions:

```bash
dc() { docker compose --env-file .env.image -f docker-compose.image.yml "$@"; }
dc config --quiet
dc config --images
dc pull
dc up -d --no-build
dc ps
dc exec -T emby-in-one /app/emby-in-one --version
dc logs --tail=100 emby-in-one
curl -fsS http://127.0.0.1:8096/System/Info/Public
```

Use sudo inside the helper if needed. Stop on command failures rather than running subsequent steps.

The sample binds only to host loopback. Remote access requires an explicit `EIO_BIND_ADDRESS=0.0.0.0` and suitable firewall/TLS, or a reverse proxy. A proxy in another container must use a shared Docker network/service name; its localhost does not refer to EIO. See [reverse proxy configuration](README_EN.md#reverse-proxy-nginx), including deliberate `trustProxy` use.

Changing only `EIO_HTTP_PORT`, e.g. to 18096, leaves the internal port at 8096; adjust host check URLs accordingly. Changing `server.port` also requires adjusting port mapping and HEALTHCHECK.

Open `/admin/`, add upstreams and create a regular user for `/web/`. Passthrough upstreams wait for identity capture from an administrator logging in with a real Emby client; that wait is not container failure.

## 4. Operations and acceptance checks

```bash
dc restart emby-in-one
dc stop -t 60 emby-in-one
dc up -d --no-build
dc logs --since=30m --tail=200 emby-in-one
cid=$(dc ps -q emby-in-one)
docker inspect --format '{{.Config.Image}} {{.Image}} {{.State.Health.Status}}' "$cid"
```

- `pull` only downloads; `restart` does not apply a new image or changed mounts/ports. Use `up -d --no-build` to recreate.
- Check the image reference/digest, `--version`, and authenticated `/admin/api/status`. Version strings/labels are configurable and do not replace provenance.
- A healthy public endpoint does not establish upstream availability, playback, subtitles, or parental-policy acceptance. `unless-stopped` does not restart a container merely because it becomes unhealthy.
- Persist both config and data. SQLite includes users, history and virtual IDs; `tokens.json`, `captured-headers.json`, `ua-library.json` and other state also matter. Deleting mappings is not harmless cache cleanup.
- Docker log rotation covers stdout, not the application's own data-directory logs. Redact tokens, credentials and private user/upstream details before sharing logs.
- Do not mount stale `public/` files over bundled UI assets. EIO is not a local video-transcoding service, but proxy playback still consumes host bandwidth. Capacity depends on workload and concurrency, not just upstream count.

### Resetting the administrator password

Prefer normal password changes in the admin panel. If locked out, back up first, stop all writers, then use the same version's CLI in a one-off container. Do not manually edit hashes or delete token files:

```bash
cid=$(dc ps --all -q emby-in-one) || exit 1
dc stop -t 60 emby-in-one || exit 1
test "$(docker inspect --format '{{.State.Status}}' "$cid")" = exited || { echo 'Container is not stopped' >&2; exit 1; }
# Confirm that it stopped and no other instance writes the same data.
read -r -s -p 'New administrator password: ' new_password; printf '\n'
if printf '%s\n' "$new_password" | dc run --rm --no-deps -T emby-in-one /app/emby-in-one --reset-password -; then
  unset new_password
  dc up -d --no-build
else
  unset new_password
  echo 'Password reset failed; inspect the error before restarting.' >&2
fi
```

This avoids password arguments/history; keep shell xtrace disabled. Resetting revokes proxy tokens, so clients must log in again. Do not use `--force` to bypass protection against a live writer.

## 5. Backup, upgrade and rollback

### Backup before changing versions

Record the old reference/digest and version; retain the old image. Stop **all writers** before copying SQLite. Do not copy only the live main DB or delete WAL/SHM files. The example assumes this is the only EIO instance:

```bash
stamp=$(date -u +%Y%m%dT%H%M%SZ)
cid=$(dc ps -q emby-in-one)
old_id=$(docker inspect --format '{{.Image}}' "$cid")
docker image tag "$old_id" "emby-in-one-rollback:$stamp"
sudo install -d -m 700 /opt/emby-in-one-backups
dc stop -t 60 emby-in-one || exit 1
# If stopping failed, do not continue.
test "$(docker inspect --format '{{.State.Status}}' "$cid")" = exited || { echo 'Container is not stopped' >&2; exit 1; }
sudo sh -c 'umask 077; tar -C /opt/emby-in-one -czf "$1" config data docker-compose.image.yml .env.image' \
  sh "/opt/emby-in-one-backups/$stamp.tar.gz"
dc up -d --no-build
```

Verify that the backup can be read/restored, and keep a secure independent copy. It contains secrets; never attach it to an Issue. Keep the Compose project identity stable, since changing directories/project names may create another deployment.

### Upgrade

1. Read the target changelog, prerelease warning, architecture and migration requirements.
2. Make the above backup; do not prune rollback images yet.
3. Change only the intended settings, including a verified target digest in `.env.image`.
4. Run `dc config --quiet`, `dc config --images`, `dc pull`; stop if any fails.
5. Run `dc up -d --no-build`; verify version, health, login, library loading and regular-user authorization. Test playback only on authorized sources and with bounded requests.
6. Observe before deleting old images/backups.

### Rollback

There is no project-specific automatic Compose rollback. Save sanitized failure logs, stop the service, restore the previous reference or local `emby-in-one-rollback:<timestamp>` tag, then `dc up -d --no-build`. A local rollback tag needs no pull.

**Image rollback is not data rollback.** Older versions are not guaranteed to understand newer databases. If migration compatibility is unknown, preserve the failed state separately and restore the matching pre-upgrade config/data/deployment files before starting the old image. Verify the backup in an isolated directory, stop every instance, and never overlay old data onto a running new service.

## 6. Local source builds and the installer

For a local build, the context needs `go.mod`, `cmd/`, `internal/`, `third_party/`, `public/` and Dockerfile. Go/C compilation runs inside the builder, but needs host build resources and access to base images/Debian packages. Prepare config/data as above in a reviewed clean checkout:

```bash
VERSION="local-$(git rev-parse --short=12 HEAD)"
docker compose config --quiet
docker compose build --build-arg VERSION="$VERSION"
docker compose up -d --no-build
docker compose exec -T emby-in-one /app/emby-in-one --version
```

A release-tag checkout can use its matching lowercase-v version explicitly. The build Compose template uses `${EIO_VERSION:-dev}`; an unverified checkout must not inherit an old release identity. Record local modifications instead of labeling arbitrary source as an official release.

The new manager keeps the selected source/image route, prefetches before stop, snapshots stopped data, and validates health/identity before success. Legacy custom templates are preserved and automatic migration is refused. Manual source Compose still needs explicit source/version review; the manager does not silently rewrite it. See the managed-installation section above.

`install.sh` runs as Linux root and may install Docker/Compose, write `/opt/emby-in-one` and install the management command. Review a fixed source before running it. Remote source downloads require a matching `SOURCE_SHA256`; do not default to `ALLOW_UNVERIFIED_SOURCE=true`. Installer fixes described here apply to current main, not old tags or already-published archives. Prefer the explicit local-build route when validating an older release.

## 7. Migration, removal and troubleshooting

- **Source build to image:** back up actual config/data paths, stop the old container, copy and verify state/ownership in a separate deployment directory, and start the pinned image. Do not run two instances against one DB even on different ports. Remove the old deployment only after acceptance; reverse migration follows the same rules.
- **Removal:** `dc down` removes project containers/networks but leaves these bind-mounted directories. Data and image deletion require separate confirmation. Do not treat `down -v` as a backup or DB cleanup; custom named volumes may be deleted.
- **Manifest missing / denied:** verify tag and package visibility; a Release alone does not prove image availability. Do not silently switch to latest.
- **No matching manifest / exec format error:** check host architecture and published platforms; forced amd64 emulation is not native ARM support.
- **Permissions/restart loops:** check UID ownership, writable directories, rootless mappings and SELinux labels. Apply appropriate `:Z` labels where required rather than disabling SELinux.
- **Unhealthy but running:** inspect config, port/health URL, startup initialization and logs; do not blindly extend timeouts.
- **Upstream network failure:** localhost means the current container; host VPN/proxy rules may not include Docker traffic. Check routing/DNS without disabling TLS verification.
- **Parental-control slowness/failures:** record policy, client, endpoint, duration and redacted error. Existing candidate-scan/metadata costs and compatibility limits are not solved by disabling access checks or globally enabling `pagedScan`.
