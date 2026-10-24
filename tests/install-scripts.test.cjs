'use strict';
/* Install/update script hardening (S10, static checks).
 *
 * The scripts under test run as root on Linux; this host (Windows dev box) has
 * no bash, so they are verified by scanning their text for the exact
 * structures the fix plan requires, plus a structural lint that is aware of
 * heredoc bodies. Where bash IS available (CI, Linux), each script also gets
 * a real `bash -n` syntax check.
 *
 * What is pinned here:
 *   - verify_download / verify_sha256 fail closed: missing sha256sum, missing
 *     checksum file, and a multi-file checksum without a matching asset-name
 *     entry all abort. The old "fall back to the first 64-hex token on any
 *     line" escape is gone — in a multi-file release that verifies the wrong
 *     file, which is the same as not verifying.
 *   - do_update never auto-executes an unverified installer: the
 *     raw.githubusercontent branch snapshot fallback is removed, and the
 *     release-asset installer passes verify_download before `bash` runs it.
 *   - Docker upgrades keep the operator's Dockerfile/docker-compose.yml; the
 *     new template lands in a separate *.release file for comparison, and is
 *     auto-created only when no file exists yet.
 *   - Release URLs and the compose build arg use the resolved release_tag
 *     (uppercase V), while file names keep the lowercase-v file_version.
 *   - install.sh verifies the Docker repo GPG key fingerprint before trusting
 *     the mirror's key, and installs the compose binary via
 *     temp-file → same-source sha256 → atomic same-directory rename.
 */

const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');
const { spawnSync } = require('node:child_process');

const root = path.join(__dirname, '..');
const read = name => fs.readFileSync(path.join(root, name), 'utf8');
const cli = read('emby-in-one-cli.sh');
const releaseInstall = read('release-install.sh');
const install = read('install.sh');

const hasBash = (() => {
  try {
    return spawnSync('bash', ['--version'], { encoding: 'utf8' }).status === 0;
  } catch {
    return false;
  }
})();

/* ── Structural lint ─────────────────────────────────────────────────────── */

// Returns the script's code lines: heredoc bodies are data (skipped), comment
// lines and trailing comments are stripped. `<<<` here-strings do not open a
// heredoc body, so they are left alone.
function codeLines(text) {
  const out = [];
  let heredoc = null;
  for (const line of text.split('\n')) {
    if (heredoc !== null) {
      if (line.trim() === heredoc) heredoc = null;
      continue;
    }
    const m = line.match(/<<-?\s*['"]?([A-Za-z_][A-Za-z0-9_]*)['"]?\s*$/);
    if (m) {
      heredoc = m[1];
      out.push(line);
      continue;
    }
    out.push(line.replace(/(^|\s)#.*$/, ''));
  }
  return out.filter(l => l.trim() !== '');
}

function assertBlockStructure(name, text) {
  const code = codeLines(text).join('\n');
  const count = re => (code.match(re) || []).length;

  // Word-boundary counts of shell keywords. awk programs embedded in single
  // quotes can contain `if (...)` — but never `then`/`fi` — so the invariant
  // `then == elif + fi` holds for shell blocks only and ignores awk. One-line
  // `if ...; then ...; fi` bodies are counted correctly this way too.
  const then = count(/\bthen\b/g);
  const elif = count(/\belif\b/g);
  const fi = count(/\bfi\b/g);
  assert.equal(then - elif, fi, `${name}: ${then} then - ${elif} elif != ${fi} fi`);

  const doKw = count(/\bdo\b/g);
  const done = count(/\bdone\b/g);
  assert.equal(doKw, done, `${name}: ${doKw} do != ${done} done`);

  const caseKw = count(/\bcase\b/g);
  const esac = count(/\besac\b/g);
  assert.equal(caseKw, esac, `${name}: ${caseKw} case != ${esac} esac`);
}

// Every `<<WORD` opener must meet its terminator line; an unterminated heredoc
// would swallow the rest of the file (and break the counts above).
function assertHeredocsClosed(name, text) {
  let heredoc = null;
  for (const line of text.split('\n')) {
    if (heredoc !== null) {
      if (line.trim() === heredoc) heredoc = null;
      continue;
    }
    const m = line.match(/<<-?\s*['"]?([A-Za-z_][A-Za-z0-9_]*)['"]?\s*$/);
    if (m) heredoc = m[1];
  }
  assert.equal(heredoc, null, `${name}: heredoc <<${heredoc} is never terminated`);
}

test('structural lint: emby-in-one-cli.sh blocks balance', () => {
  assertBlockStructure('emby-in-one-cli.sh', cli);
  assertHeredocsClosed('emby-in-one-cli.sh', cli);
});
test('structural lint: release-install.sh blocks balance', () => {
  assertBlockStructure('release-install.sh', releaseInstall);
  assertHeredocsClosed('release-install.sh', releaseInstall);
});
test('structural lint: install.sh blocks balance', () => {
  assertBlockStructure('install.sh', install);
  assertHeredocsClosed('install.sh', install);
});

/* ── bash -n when bash exists ────────────────────────────────────────────── */

for (const name of ['emby-in-one-cli.sh', 'release-install.sh', 'install.sh']) {
  test(`bash -n ${name}`, { skip: !hasBash && 'bash not available on this host' }, () => {
    const r = spawnSync('bash', ['-n', path.join(root, name)], { encoding: 'utf8' });
    assert.equal(r.status, 0, `bash -n failed: ${r.stderr || '(no stderr)'}`);
  });
}

/* ── emby-in-one-cli.sh: verify_download fails closed ───────────────────── */

test('cli verify_download: missing sha256sum aborts instead of skipping', () => {
  assert.match(cli, /系统缺少 sha256sum，无法校验下载内容，已中止安装/);
  assert.doesNotMatch(cli, /已跳过完整性校验/);
});

test('cli verify_download: missing checksum file aborts instead of skipping', () => {
  assert.match(cli, /未找到校验和文件: \$\{url\}\.sha256，无校验不安装，已中止/);
  assert.doesNotMatch(cli, /该 Release 未提供 \$\{asset_name\}\.sha256/);
});

test('cli verify_download: multi-file checksums must match the asset name', () => {
  // The first-line fallback verified the wrong file in a multi-file release.
  assert.doesNotMatch(cli, /expected=\$\(awk '\{print \$1\}'/);
  assert.match(cli, /name == want \{ print \$1; exit \}/);
  assert.match(cli, /未找到 \$\{asset_name\} 的条目，或格式无法识别/);
});

test('cli verify_download: pre-V1.4.4 releases stay installable (compat window)', () => {
  // V1.4.3 and earlier never published .sha256 artifacts; skipping for those
  // exact tags is the documented window, resolved from the download URL.
  assert.match(cli, /version_le "\$release_tag" "V1\.4\.3"/);
  assert.match(cli, /release_tag="\$\{url#\*\/releases\/download\/\}"/);
});

/* ── emby-in-one-cli.sh: installer execution gating ──────────────────────── */

test('cli do_update: no unverified raw.githubusercontent installer fallback', () => {
  assert.doesNotMatch(cli, /raw\.githubusercontent\.com/);
});

test('cli do_update: release installer is verified before it is executed', () => {
  const doUpdate = cli.slice(cli.indexOf('do_update() {'), cli.indexOf('do_update_custom() {'));
  assert.ok(doUpdate.length > 0, 'do_update() body not found');
  const verifyIdx = doUpdate.indexOf('verify_download "${tmp_script}" "${installer_url}"');
  const execIdx = doUpdate.indexOf('bash "${installer}"');
  assert.ok(verifyIdx > -1, 'installer is not passed through verify_download');
  assert.ok(execIdx > -1, 'installer execution not found');
  assert.ok(verifyIdx < execIdx, 'installer must be verified before bash runs it');
  assert.match(doUpdate, /安装脚本完整性校验未通过，已中止更新（不执行未校验脚本）/);
});

/* ── emby-in-one-cli.sh: upgrade keeps operator templates ────────────────── */

test('cli docker upgrade: existing Dockerfile/compose are preserved', () => {
  const fn = cli.slice(
    cli.indexOf('download_and_install_docker() {'),
    cli.indexOf('download_and_install_binary() {'),
  );
  assert.ok(fn.length > 0, 'download_and_install_docker() body not found');

  // New template goes to a side file when the operator already has one…
  assert.match(fn, /Dockerfile\.release/);
  assert.match(fn, /docker-compose\.yml\.release/);
  // …and is auto-created only when nothing exists yet.
  assert.match(fn, /if \[\[ ! -f "\$\{PROJECT_DIR\}\/Dockerfile" \]\]/);
  assert.match(fn, /if \[\[ ! -f "\$\{PROJECT_DIR\}\/docker-compose\.yml" \]\]/);
  // The old unconditional overwrite is gone.
  assert.doesNotMatch(fn, /cp -f "\$\{src_dir\}\/Dockerfile" "\$\{PROJECT_DIR\}\/Dockerfile"/);
  assert.doesNotMatch(fn, /cat > "\$\{PROJECT_DIR\}\/docker-compose\.yml" <<EOF/);
});

test('cli docker upgrade: compose build arg uses the resolved release tag', () => {
  const fn = cli.slice(
    cli.indexOf('download_and_install_docker() {'),
    cli.indexOf('download_and_install_binary() {'),
  );
  assert.match(fn, /VERSION: \$\{release_tag\}/);
  assert.doesNotMatch(fn, /VERSION: \$\{version_tag\}/);
});

test('cli binary update: panel assets use the resolved release tag', () => {
  const fn = cli.slice(
    cli.indexOf('download_and_install_binary() {'),
    cli.indexOf('do_status() {'),
  );
  assert.match(fn, /releases\/download\/\$\{release_tag\}\/\$\{asset\}/);
  assert.doesNotMatch(fn, /releases\/download\/\$\{version_tag\}\//);
});

/* ── release-install.sh ──────────────────────────────────────────────────── */

test('release-install verify_sha256: no first-line checksum fallback', () => {
  assert.doesNotMatch(releaseInstall, /expected=\$\(awk '\{print \$1\}'/);
  assert.match(releaseInstall, /未找到 \$\{asset_name\} 的条目，或格式无法识别/);
});

test('release-install: downloads land in temp files and move atomically', () => {
  assert.match(releaseInstall, /local tmp="\$\{dest\}\.download\.\$\$"/);
  assert.match(releaseInstall, /mv -f "\$\{tmp\}" "\$\{dest\}"/);
});

/* ── install.sh ──────────────────────────────────────────────────────────── */

test('install.sh: docker repo GPG key fingerprint is verified independently', () => {
  const dockerFn = install.slice(
    install.indexOf('_install_docker_aliyun() {'),
    install.indexOf('if ! command -v docker &>/dev/null'),
  );
  assert.ok(dockerFn.length > 0, '_install_docker_aliyun() body not found');
  // Both the apt and the yum/dnf mirror paths check the fingerprint before
  // trusting the key: the repo file's own gpgkey URL is same-origin with the
  // repo and cannot be the only check.
  const fingerprintChecks = dockerFn.match(/9DC858229FC7DD38854AE2D88D81803C0EBFCD88/g) || [];
  assert.ok(fingerprintChecks.length >= 2, 'expected fingerprint checks in both apt and yum paths');
  assert.match(dockerFn, /gpg --show-keys --with-colons/);
  assert.match(dockerFn, /指纹不匹配/);
  // The key is imported only after the fingerprint matched.
  const checkIdx = dockerFn.indexOf('9DC858229FC7DD38854AE2D88D81803C0EBFCD88');
  const dearmorIdx = dockerFn.indexOf('gpg --dearmor');
  assert.ok(checkIdx > -1 && dearmorIdx > -1 && checkIdx < dearmorIdx, 'fingerprint check must precede key import');
});

test('install.sh: compose binary is downloaded to a temp file and verified', () => {
  const composeBlock = install.slice(
    install.indexOf('Docker Compose 未安装'),
    install.indexOf('# ── 4. 创建项目目录'),
  );
  assert.ok(composeBlock.length > 0, 'compose install block not found');

  // Temp file lives next to the destination so the final mv is an atomic
  // same-filesystem rename, never a half-written executable.
  assert.match(composeBlock, /COMPOSE_TMP="\/usr\/local\/bin\/docker-compose\.download\.\$\$"/);
  assert.match(composeBlock, /mv -f "\$\{COMPOSE_TMP\}" \/usr\/local\/bin\/docker-compose/);
  // The old direct download into the final path is gone.
  assert.doesNotMatch(composeBlock, /-o \/usr\/local\/bin\/docker-compose"\s*$/);

  // Same-source checksum: fetched, matched by asset name, compared.
  assert.match(composeBlock, /"\$\{COMPOSE_URL\}\.sha256"/);
  assert.match(composeBlock, /name == want \{ print \$1; exit \}/);
  assert.doesNotMatch(composeBlock, /COMPOSE_EXPECTED=\$\(awk '\{print \$1\}'/);
  assert.match(composeBlock, /"\$\{COMPOSE_EXPECTED,,\}" != "\$\{COMPOSE_ACTUAL,,\}"/);
  // Missing sha256sum is an abort, not a skip.
  assert.match(composeBlock, /系统缺少 sha256sum，无法校验 Docker Compose 二进制，已中止安装/);
});

test('install.sh: existing Dockerfile/compose are preserved on re-install', () => {
  const layout = install.slice(
    install.indexOf('copy_distribution_layout() {'),
    install.indexOf('copy_project_files_from() {'),
  );
  assert.ok(layout.length > 0, 'copy_distribution_layout() body not found');
  assert.match(layout, /write_runtime_dockerfile_to "\$\{PROJECT_DIR\}\/Dockerfile\.release"/);
  assert.match(layout, /write_runtime_compose_to "\$\{PROJECT_DIR\}\/docker-compose\.yml\.release"/);
  assert.match(layout, /if \[\[ -f "\$\{PROJECT_DIR\}\/Dockerfile" \]\]/);
  assert.match(layout, /if \[\[ -f "\$\{PROJECT_DIR\}\/docker-compose\.yml" \]\]/);
  // The writers take an explicit destination now.
  assert.match(install, /write_runtime_dockerfile_to\(\) \{\n  local dest="\$1"/);
  assert.match(install, /write_runtime_compose_to\(\) \{\n  local dest="\$1"/);
});
