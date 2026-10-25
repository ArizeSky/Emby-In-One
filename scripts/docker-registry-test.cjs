'use strict';
// Only local, ephemeral runner registry. Never writes GHCR or a remote registry.
const assert = require('node:assert/strict');
const { spawnSync } = require('node:child_process');
const { inspectRegistry, immutableDecision, validateLatest } = require('./docker-release-policy.cjs');
const run = args => { const r = spawnSync('docker', args, { encoding: 'utf8', timeout: 120000 }); if (r.status !== 0) throw Error(r.stderr || String(r.error)); return r.stdout.trim(); };
async function main() {
  const [repo, candidateTag, version] = process.argv.slice(2);
  assert.match(repo || '', /^(127\.0\.0\.1|localhost):\d+\/[a-z0-9_/-]+$/);
  assert.match(candidateTag || '', /^[A-Za-z0-9_.-]+$/);
  assert.match(version || '', /^v\d+\.\d+\.\d+-[A-Za-z0-9.-]+$/);
  const candidate = await inspectRegistry(`${repo}:${candidateTag}`); assert.ok(candidate);
  const platforms = candidate.manifest.manifests.map(x => x.platform.architecture).sort();
  assert.deepEqual(platforms, ['amd64', 'arm64']);
  const ref = `${repo}:${version}`;
  const before = await inspectRegistry(ref);
  const decision = immutableDecision({ existingDigest: before?.digest ?? null, candidateDigest: candidate.digest });
  if (decision === 'create') run(['buildx', 'imagetools', 'create', '--tag', ref, `${repo}@${candidate.digest}`]);
  assert.equal((await inspectRegistry(ref)).digest, candidate.digest);
  assert.equal(immutableDecision({ existingDigest: candidate.digest, candidateDigest: candidate.digest }), 'identical');
  assert.throws(() => immutableDecision({ existingDigest: candidate.digest, candidateDigest: `sha256:${'0'.repeat(64)}` }));
  assert.equal((await inspectRegistry(ref)).digest, candidate.digest, 'conflict cannot mutate version');
  assert.throws(() => validateLatest({ version, explicitlyConfirmed: true, release: { tag_name: version, draft: false, prerelease: true, isCurrentStable: true } }));
  assert.equal(await inspectRegistry(`${repo}:nonexistent-acceptance-tag`), null);
  console.log(JSON.stringify({ registry: 'loopback-only', version, digest: candidate.digest, platforms, immutable: true, prereleaseLatestRefused: true }));
}
main().catch(e => { console.error(e.message); process.exitCode = 1; });
