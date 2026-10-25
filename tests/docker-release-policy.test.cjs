'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const { spawnSync } = require('node:child_process');
const { validateIdentity, planTags, validateLatest, immutableDecision } = require('../scripts/docker-release-policy.cjs');
const sha = 'a'.repeat(40);
test('identity accepts exact SemVer V/v tag and requires matching full SHA', () => {
  assert.deepEqual(validateIdentity({ tag: 'V1.4.4-rc3', expectedSha: sha, resolvedSha: sha.toUpperCase() }), { tag: 'V1.4.4-rc3', version: 'v1.4.4-rc3', sha });
  for (const tag of ['v1.2', 'v01.2.3', 'v1.2.3-foo bar', 'branch']) assert.throws(() => validateIdentity({ tag, expectedSha: sha, resolvedSha: sha }));
  assert.throws(() => validateIdentity({ tag: 'v1.2.3', expectedSha: 'abc', resolvedSha: sha }));
  assert.throws(() => validateIdentity({ tag: 'v1.2.3', expectedSha: sha, resolvedSha: 'b'.repeat(40) }));
});
test('tag plan has only candidate plus exact version identity', () => {
  assert.deepEqual(planTags({ tag: 'v1.4.4', expectedSha: sha, resolvedSha: sha, repository: 'owner/repo' }), { tag: 'v1.4.4', version: 'v1.4.4', sha, image: 'ghcr.io/owner/repo', candidate: `ghcr.io/owner/repo:candidate-${sha}` });
});
test('latest requires explicit stable exact published current release', () => {
  const release = { tag_name: 'v1.4.4', draft: false, prerelease: false, isCurrentStable: true };
  assert.equal(validateLatest({ version: 'v1.4.4', explicitlyConfirmed: true, release }), true);
  for (const input of [{ explicitlyConfirmed: false, release }, { explicitlyConfirmed: true, release: { ...release, prerelease: true } }, { explicitlyConfirmed: true, release: { ...release, isCurrentStable: false } }, { explicitlyConfirmed: true, version: 'v1.4.5-rc1', release }]) assert.throws(() => validateLatest({ version: 'v1.4.4', ...input }));
});
test('immutable tag decision is idempotent only for same valid digest, fails closed otherwise', () => {
  const digest = `sha256:${'1'.repeat(64)}`;
  assert.equal(immutableDecision({ existingDigest: null, candidateDigest: digest }), 'create');
  assert.equal(immutableDecision({ existingDigest: digest, candidateDigest: digest }), 'identical');
  assert.throws(() => immutableDecision({ existingDigest: `sha256:${'2'.repeat(64)}`, candidateDigest: digest }));
  assert.throws(() => immutableDecision({ existingDigest: 'registry unavailable', candidateDigest: digest }));
});
test('temporary git fixture resolves annotated tag to exact commit and rejects moved tag', () => {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'docker-policy-git-'));
  const git = (...args) => { const r = spawnSync('git', args, { cwd: dir, encoding: 'utf8' }); assert.equal(r.status, 0, r.stderr); return r.stdout.trim(); };
  try { git('init'); git('config', 'user.email', 'test@example.invalid'); git('config', 'user.name', 'test'); fs.writeFileSync(path.join(dir, 'file'), 'one'); git('add', 'file'); git('commit', '-m', 'one'); const first = git('rev-parse', 'HEAD'); git('tag', '-a', 'v1.0.0', '-m', 'release'); assert.equal(git('rev-parse', 'v1.0.0^{commit}'), first); fs.writeFileSync(path.join(dir, 'file'), 'two'); git('commit', '-am', 'two'); const second = git('rev-parse', 'HEAD'); git('tag', '-f', 'v1.0.0', second); assert.throws(() => validateIdentity({ tag: 'v1.0.0', expectedSha: first, resolvedSha: git('rev-parse', 'v1.0.0^{commit}') })); } finally { fs.rmSync(dir, { recursive: true, force: true }); }
});
