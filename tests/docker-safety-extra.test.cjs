'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { createHash } = require('node:crypto');
const { spawnSync } = require('node:child_process');
const { inspectRegistry, immutableDecision, validateLatest, validateIdentity } = require('../scripts/docker-release-policy.cjs');
const root = path.resolve(__dirname, '..');
const read = p => fs.readFileSync(path.join(root, p), 'utf8');
test('only an explicit MANIFEST_UNKNOWN response is registry absence', async () => {
  const ref = '127.0.0.1:5000/test:version';
  const response = (status, body) => async () => new Response(JSON.stringify(body), { status });
  assert.equal(await inspectRegistry(ref, { fetchImpl: response(404, { errors: [{ code: 'MANIFEST_UNKNOWN' }] }) }), null);
  for (const status of [401,403,429,500,503]) await assert.rejects(inspectRegistry(ref, { fetchImpl: response(status, { errors: [{ code: 'DENIED' }] }) }));
  await assert.rejects(inspectRegistry(ref, { fetchImpl: response(404, { message: 'not found' }) }));
  await assert.rejects(inspectRegistry(ref, { fetchImpl: async () => { throw Error('network timeout'); } }));
});
test('registry bytes must match exact digest header', async () => {
  const ref = '127.0.0.1:5000/test:version', body = JSON.stringify({ schemaVersion: 2 });
  const digest = `sha256:${createHash('sha256').update(body).digest('hex')}`;
  const f = value => async () => new Response(body, { headers: { 'docker-content-digest': value } });
  assert.equal((await inspectRegistry(ref, { fetchImpl: f(digest) })).digest, digest);
  await assert.rejects(inspectRegistry(ref, { fetchImpl: f(`sha256:${'0'.repeat(64)}`) }));
  await assert.rejects(inspectRegistry('attacker.invalid/x:v1', { fetchImpl: f(digest) }));
});
test('latest requires stable current public Release, including uppercase V tag convention', () => {
  const release = { tag_name: 'V1.4.4', draft: false, prerelease: false, isCurrentStable: true };
  assert.equal(validateLatest({ version: 'v1.4.4', explicitlyConfirmed: true, release }), true);
  for (const version of ['v1.4.4-rc3','dev-main','v01.4.4']) assert.throws(() => validateLatest({ version, explicitlyConfirmed: true, release }));
  assert.throws(() => immutableDecision({ existingDigest: '', candidateDigest: `sha256:${'1'.repeat(64)}` }));
  assert.throws(() => immutableDecision({ existingDigest: null, candidateDigest: 'invalid' }));
  assert.throws(() => validateIdentity({ tag: 'V1.4.4+not-docker-safe', expectedSha: 'a'.repeat(40), resolvedSha: 'a'.repeat(40) }));
});
test('trusted publish workflow remains manual with build and package credentials separated', () => {
  const w = read('.github/workflows/docker.yml');
  assert.match(w, /workflow_dispatch:/);
  assert.doesNotMatch(w, /^  (push|pull_request|schedule):/m);
  assert.match(w, /default: validate/);
  assert.match(w, /packages: write/);
  const build = w.slice(w.indexOf('  build:'), w.indexOf('  publish:'));
  assert.doesNotMatch(build, /packages: write|docker\/login-action/);
  assert.match(w, /immutable/); assert.match(w, /promote_latest/);
  assert.doesNotMatch(w, /type=raw,value=latest/);
});
test('manager state is parsed, route identity and rollback safety remain explicit', () => {
  const m = read('eio-docker.sh');
  assert.match(m, /json\.load/); assert.doesNotMatch(m, /^\s*(eval|source)\s/m);
  assert.match(m, /flock -n/);
  assert.match(m, /backup failed; target not applied/);
  assert.match(m, /ROLLBACK FAILED/);
  assert.match(m, /tar -cpf.*config data/);
  assert.match(m, /mv "\$PROJECT_DIR\/config" "\$PROJECT_DIR\/data" "\$BACKUP\/failed\/"/);
  assert.match(m, /image mode requires --image and forbids --source-dir/);
  assert.match(m, /source mode never accepts an image update/);
  assert.doesNotMatch(m, /docker (system|image) prune|--privileged|chmod.*777|--reset-password.*--force/);
  assert.match(read('.dockerignore'), /^\.eio-\*/m);
});
test('installer and manager expose help without build, network or root operations', () => {
  for (const file of ['install.sh','eio-docker.sh']) {
    const r = spawnSync('bash', [path.join(root, file), '--help'], { encoding: 'utf8', timeout: 5000 });
    assert.equal(r.status, 0, r.stderr || String(r.error));
    assert.match(r.stdout, /source\|image/);
  }
});
