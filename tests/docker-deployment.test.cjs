'use strict';
// No Docker daemon, network, root access or complete installer execution.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const root = path.join(__dirname, '..');
const read = p => fs.readFileSync(path.join(root, p), 'utf8').replace(/\r\n/g, '\n');
const installer = read('install.sh');
const hasBash = spawnSync('bash', ['--version'], { encoding: 'utf8' }).status === 0;

function template(name) {
  const start = installer.indexOf(`${name}() {`);
  assert.ok(start >= 0, `missing ${name}`);
  const end = installer.indexOf('\n}\n', start);
  assert.ok(end > start);
  return installer.slice(start, end + 3);
}

test('source installer Compose template has a services root and uses destination writers only', () => {
  assert.match(template('write_runtime_compose_to'), /<<'EOF'\nservices:\n  emby-in-one:/);
  assert.doesNotMatch(installer, /^\s*write_runtime_dockerfile\s*$/m);
  const layout = template('copy_distribution_layout');
  assert.match(layout, /write_runtime_dockerfile_to "\$\{PROJECT_DIR\}\/Dockerfile"/);
  assert.match(layout, /write_runtime_compose_to "\$\{PROJECT_DIR\}\/docker-compose\.yml"/);
});

test('source installer creates valid fresh templates and preserves customized files on reinstall', { skip: !hasBash }, () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'eio-docker-template-'));
  try {
    // Extract functions only, never source/execute the complete root installer.
    const functions = ['copy_item', 'write_runtime_dockerfile_to', 'write_runtime_compose_to', 'copy_distribution_layout'].map(template).join('\n');
    const script = `set -euo pipefail
info() { :; }
warn() { :; }
${functions}
mkdir -p fixture/cmd fixture/internal fixture/third_party fixture/public target
printf 'module example\n' > fixture/go.mod
PROJECT_DIR="$PWD/target"
copy_distribution_layout "$PWD/fixture"
cp target/Dockerfile first.Dockerfile
cp target/docker-compose.yml first.compose.yml
printf '# operator Dockerfile\n' > target/Dockerfile
printf '# operator compose\n' > target/docker-compose.yml
copy_distribution_layout "$PWD/fixture"
`;
    const run = spawnSync('bash', ['-c', script], { cwd: dir, encoding: 'utf8', timeout: 15000 });
    assert.equal(run.status, 0, `${run.error || ''}\n${run.stderr}`);
    const first = fs.readFileSync(path.join(dir, 'first.compose.yml'), 'utf8');
    assert.match(first, /^services:\n  emby-in-one:/);
    assert.match(first, /build:\n      context: \./);
    assert.match(fs.readFileSync(path.join(dir, 'first.Dockerfile'), 'utf8'), /COPY third_party \.\/third_party/);
    assert.equal(fs.readFileSync(path.join(dir, 'target/Dockerfile'), 'utf8'), '# operator Dockerfile\n');
    assert.equal(fs.readFileSync(path.join(dir, 'target/docker-compose.yml'), 'utf8'), '# operator compose\n');
    assert.equal(fs.readFileSync(path.join(dir, 'target/docker-compose.yml.release'), 'utf8'), first);
    assert.equal(fs.readFileSync(path.join(dir, 'target/Dockerfile.release'), 'utf8'), fs.readFileSync(path.join(dir, 'first.Dockerfile'), 'utf8'));
  } finally {
    fs.rmSync(dir, { recursive: true, force: true });
  }
});

test('published-image Compose requires an explicit image and never builds or defaults to latest', () => {
  const compose = read('docker-compose.image.yml');
  assert.match(compose, /^services:\n  emby-in-one:/m);
  assert.match(compose, /image: "\$\{EIO_IMAGE:\?/);
  assert.doesNotMatch(compose, /^\s*build:/m);
  assert.doesNotMatch(compose, /image:.*:latest/);
  assert.match(compose, /EIO_BIND_ADDRESS:-127\.0\.0\.1/);
  assert.match(compose, /user: "1000:1000"/);
  assert.match(compose, /\.\/config:\/app\/config/);
  assert.match(compose, /\.\/data:\/app\/data/);
  assert.match(compose, /stop_grace_period: 60s/);
});

test('Docker build context excludes environment files and runtime database backups', () => {
  const ignore = read('.dockerignore').split('\n');
  for (const value of ['config', 'data', '.env', '.env.*', 'backups', '*.db', '*.db-*']) {
    assert.ok(ignore.includes(value), `missing exclusion ${value}`);
  }
});

test('README entry points link to deployment and release guidance', () => {
  for (const [name, guide] of [['README.md', 'DOCKER.md'], ['README_EN.md', 'DOCKER_EN.md']]) {
    const text = read(name);
    assert.ok(text.includes(`](${guide})`));
    assert.ok(text.includes('](RELEASING.md)'));
    assert.ok(text.includes('docker-compose.image.yml'));
  }
});
