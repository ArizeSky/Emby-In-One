'use strict';
const { createHash } = require('node:crypto');
const semver = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-(?:0|[1-9]\d*|\d*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9]\d*|\d*[A-Za-z-][0-9A-Za-z-]*))*)?$/;
function fail(m) { throw new Error(m); }
function validateIdentity({ tag, expectedSha, resolvedSha }) {
  if (typeof tag !== 'string' || !/^[Vv]/.test(tag) || !semver.test(tag.slice(1))) fail('tag must be an exact SemVer V/v-tag without build metadata (not representable in Docker tags)');
  if (!/^[a-f0-9]{40}$/i.test(expectedSha || '') || !/^[a-f0-9]{40}$/i.test(resolvedSha || '')) fail('full 40-character commit SHA required');
  if (expectedSha.toLowerCase() !== resolvedSha.toLowerCase()) fail('tag resolved SHA differs from expected SHA');
  return { tag, version: `v${tag.slice(1)}`, sha: resolvedSha.toLowerCase() };
}
function planTags({ tag, expectedSha, resolvedSha, repository }) {
  const i = validateIdentity({ tag, expectedSha, resolvedSha });
  if (!/^[a-z0-9_.-]+\/[a-z0-9_.-]+$/.test(repository || '')) fail('lowercase owner/repo required');
  return { ...i, image: `ghcr.io/${repository}`, candidate: `ghcr.io/${repository}:candidate-${i.sha}` };
}
function validateLatest({ version, explicitlyConfirmed, release }) {
  if (explicitlyConfirmed !== true) fail('latest requires explicit confirmation');
  if (!/^v(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)$/.test(version || '')) fail('latest requires stable SemVer');
  if (!release || String(release.tag_name).replace(/^V/, 'v') !== version || release.draft !== false || release.prerelease !== false || release.isCurrentStable !== true) fail('latest requires exact current published stable Release; no downgrade');
  return true;
}
function immutableDecision({ existingDigest, candidateDigest }) {
  if (!/^sha256:[0-9a-f]{64}$/.test(candidateDigest || '')) fail('invalid candidate digest');
  if (existingDigest === null) return 'create';
  if (!/^sha256:[0-9a-f]{64}$/.test(existingDigest || '')) fail('registry inspection error is not absence');
  if (existingDigest === candidateDigest) return 'identical';
  fail('version tag exists with different digest; overwrite refused');
}
function registryParts(ref) {
  const m = /^(?<host>[^/]+)\/(?<repo>[a-z0-9_./-]+)(?::(?<tag>[A-Za-z0-9_.-]+)|@(?<digest>sha256:[a-f0-9]{64}))$/.exec(ref);
  if (!m) fail('explicit fully qualified registry reference required');
  const { host, repo, tag, digest } = m.groups;
  const local = /^(127\.0\.0\.1|localhost):\d+$/.test(host);
  if (!local && host !== 'ghcr.io') fail('only loopback registry or ghcr.io is supported');
  return { host, repo, url: `${local ? 'http' : 'https'}://${host}/v2/${repo}/manifests/${tag || digest}` };
}
async function inspectRegistry(ref, { fetchImpl = fetch, user = process.env.REGISTRY_USER, password = process.env.REGISTRY_PASSWORD } = {}) {
  const p = registryParts(ref);
  const headers = { Accept: 'application/vnd.oci.image.index.v1+json, application/vnd.docker.distribution.manifest.list.v2+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json' };
  let r = await fetchImpl(p.url, { headers, signal: AbortSignal.timeout(30000), redirect: 'error' });
  if (r.status === 401 && p.host === 'ghcr.io') {
    const challenge = r.headers.get('www-authenticate') || '';
    const realm = /realm="([^"]+)"/.exec(challenge)?.[1];
    const service = /service="([^"]+)"/.exec(challenge)?.[1];
    if (realm !== 'https://ghcr.io/token' || service !== 'ghcr.io') fail('untrusted registry auth challenge');
    const auth = user && password ? { Authorization: `Basic ${Buffer.from(`${user}:${password}`).toString('base64')}` } : {};
    const t = await fetchImpl(`${realm}?service=ghcr.io&scope=${encodeURIComponent(`repository:${p.repo}:pull`)}`, { headers: auth, signal: AbortSignal.timeout(30000), redirect: 'error' });
    if (!t.ok) fail(`registry token request failed: ${t.status}`);
    const token = (await t.json()).token; if (!token) fail('missing registry token');
    headers.Authorization = `Bearer ${token}`;
    r = await fetchImpl(p.url, { headers, signal: AbortSignal.timeout(30000), redirect: 'error' });
  }
  if (r.status === 404) {
    const body = await r.json();
    if (body.errors?.some(e => e.code === 'MANIFEST_UNKNOWN')) return null;
    fail('404 without MANIFEST_UNKNOWN is not a missing version');
  }
  if (!r.ok) fail(`registry inspection failed: HTTP ${r.status}`);
  const bytes = Buffer.from(await r.arrayBuffer());
  const digest = `sha256:${createHash('sha256').update(bytes).digest('hex')}`;
  if (r.headers.get('docker-content-digest') !== digest) fail('registry digest/header verification failed');
  return { digest, manifest: JSON.parse(bytes.toString('utf8')) };
}
async function cli() {
  const [cmd, a, b] = process.argv.slice(2);
  if (cmd === 'identity') console.log(JSON.stringify(validateIdentity({ tag: process.env.RELEASE_TAG, expectedSha: process.env.EXPECTED_SHA, resolvedSha: process.env.RESOLVED_SHA })));
  else if (cmd === 'immutable') console.log(immutableDecision({ existingDigest: a === 'absent' ? null : a, candidateDigest: b }));
  else if (cmd === 'inspect') console.log((await inspectRegistry(a))?.digest || 'absent');
  else if (cmd === 'latest') { validateLatest({ version: process.env.RELEASE_VERSION, explicitlyConfirmed: process.env.CONFIRM_LATEST === 'true', release: JSON.parse(process.env.RELEASE_JSON || 'null') }); console.log('allowed'); }
  else fail('usage: identity|immutable existing candidate|inspect reference|latest');
}
module.exports = { validateIdentity, planTags, validateLatest, immutableDecision, inspectRegistry, registryParts };
if (require.main === module) cli().catch(e => { console.error(e.message); process.exitCode = 1; });
