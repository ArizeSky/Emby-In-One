'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const root = path.join(__dirname, '../public/vendor/web/libmedia');
const namedChunks = Object.freeze({
  90200: 'mse-mux-planner.js',
  90201: 'shared-media-ownership.js',
  90202: 'shared-media-domain.js',
  90203: 'owned-media-resources.js',
  90204: 'shared-media-release-registry.js',
  90205: 'codecpar-leases.js',
  90206: 'mse-pull-lifetime.js',
  90207: 'mse-packet-ownership.js',
  90208: 'native-playback-clock.js',
  90209: 'native-playback-clock-source.js',
  90210: 'payload-transfer.js',
  90211: 'mux-producers.js',
  90212: 'mux-producer-lifecycle.js'
});

function registeredRuntime(skipId) {
  const context = vm.createContext({
    self: {}, console,
    document: {
      currentScript: { tagName: 'SCRIPT', src: 'http://127.0.0.1/vendor/avplayer.js' },
      getElementsByTagName: () => []
    }
  });
  for (const [id, file] of Object.entries(namedChunks)) {
    if (Number(id) !== 90200 && Number(id) !== skipId) {
      vm.runInContext(fs.readFileSync(path.join(root, file), 'utf8'), context);
    }
  }
  const source = fs.readFileSync(path.join(root, 'avplayer.js'), 'utf8');
  const entry = 'var a=s(52989);return a.default';
  assert.equal(source.split(entry).length, 2, 'exact real entry boundary');
  // Observe actual registration/collector without starting an application.
  vm.runInContext(source.replace(entry, 'return {require:s,modules:s.m}'), context);
  return context.self.AVPlayer;
}

test('queued split factories register before the real entry boundary', () => {
  const runtime = registeredRuntime();
  for (const id of [90190, 90187, 90189, 90188]) assert.equal(typeof runtime.modules[id], 'function');
  assert.equal(typeof runtime.require(90191).createCodecparLeases, 'function');
  assert.equal(typeof runtime.require(90192).createMsePullLifetime, 'function');
  assert.equal(typeof runtime.require(90193).createMsePacketOwnership, 'function');
  assert.equal(runtime.require(90190).VERSION, 1);
  assert.equal(typeof runtime.require(90189).createOwnedResources, 'function');
  assert.equal(typeof runtime.require(90188).createReleaseRegistry, 'function');
  const ownership = fs.readFileSync(path.join(root, 'mse-packet-ownership.js'), 'utf8');
  assert.match(ownership, /90193:\(module,exports,__webpack_require__\)=>\{/);
  assert.match(ownership, /createMsePacketOwnership/);
  assert.match(ownership, /EIO_MUX_PULL_OWNERSHIP_CONFLICT/);
});

test('strict pull module cannot load without the real ownership module', () => {
  const runtime = registeredRuntime(90207);
  assert.throws(() => runtime.require(90192));
});

test('actual generated Worker collector includes the split transitive factories', () => {
  const runtime = registeredRuntime();
  const code = runtime.require(39484).A(90187, { varName: 'collected' });
  for (const id of [90187, 90190, 90188, 90194]) assert.match(code, new RegExp('(?:"' + id + '"|' + id + ')\\s*:'));
  assert.match(code, /if \(chunkId === 26\) promises.push\(__webpack_require__\.e\(90200\)\)/);
  assert.match(code, /if \(chunkId === 26\) promises.push\(__webpack_require__\.e\(90205\), __webpack_require__\.e\(90206\), __webpack_require__\.e\(90207\), __webpack_require__\.e\(90208\), __webpack_require__\.e\(90209\), __webpack_require__\.e\(90210\), __webpack_require__\.e\(90211\), __webpack_require__\.e\(90212\)\)/);
  const pullCode = runtime.require(39484).A(90192, { varName: 'collectedPull' });
  for (const id of [90192, 90193]) assert.match(pullCode, new RegExp('(?:"' + id + '"|' + id + ')\\s*:'));
  assert.match(pullCode, /__webpack_require__\(90193\)/);
  assert.match(pullCode, /createMsePacketOwnership/);
  const mainSource = fs.readFileSync(path.join(root, 'avplayer.js'), 'utf8');
  assert.match(mainSource, /s\.f\.eioDependencies=\(e,t\)=>\{26===e&&t\.push\(s\.e\(90200\),s\.e\(90205\),s\.e\(90206\),s\.e\(90207\),s\.e\(90208\),s\.e\(90209\),s\.e\(90210\),s\.e\(90211\),s\.e\(90212\)\)\}/);
  const shippedLease = fs.readFileSync(path.join(root, 'codecpar-leases.js'), 'utf8');
  const canonicalLease = fs.readFileSync(path.join(__dirname, '../public/web/player-codecpar-leases.js'), 'utf8');
  assert.equal(shippedLease.slice(shippedLease.indexOf('/*!'), shippedLease.lastIndexOf('\n}\n}]);')).trimEnd(), canonicalLease.trimEnd(), 'shipped 90191 body must match canonical helper byte-for-byte');
  assert.match(code, /const core=__webpack_require__\(90190\)/);
});

test('main and actual generated Worker resolve identical named and upstream chunk URLs', () => {
  const runtime = registeredRuntime();
  const code = runtime.require(39484).A(90190, { varName: 'collected' });
  const entry = 'var f = __webpack_require__(__webpack_require__.s =';
  assert.equal(code.split(entry).length, 2);
  // Expose the generated loader before its entry, preserving its actual URL function.
  const exposed = code.replace(entry, 'return __webpack_require__; var f = __webpack_require__(__webpack_require__.s =');
  const context = vm.createContext({ console });
  vm.runInContext(exposed, context);
  for (const [id, file] of Object.entries(namedChunks)) {
    assert.equal(runtime.require.u(Number(id)), file + '?v=eio-native-clock-1');
    assert.equal(context.collected.u(Number(id)), runtime.require.u(Number(id)));
  }
  for (const id of [26, 573, 943]) {
    assert.equal(runtime.require.u(id), id + '.avplayer.js?v=eio-native-clock-1');
    assert.equal(context.collected.u(id), runtime.require.u(id));
  }
});

test('page registers all ownership chunks before AVPlayer', () => {
  const html = fs.readFileSync(path.join(__dirname, '../public/web/web.html'), 'utf8');
  const entry = html.indexOf('/web/vendor/web/libmedia/avplayer.js?');
  assert.ok(entry >= 0);
  for (const [id, file] of Object.entries(namedChunks)) {
    if (Number(id) === 90200) continue;
    const at = html.indexOf('/web/vendor/web/libmedia/' + file + '?');
    assert.ok(at >= 0 && at < entry);
  }
  const ownership = html.indexOf('/web/vendor/web/libmedia/mse-packet-ownership.js?');
  const pull = html.indexOf('/web/vendor/web/libmedia/mse-pull-lifetime.js?');
  assert.ok(ownership >= 0 && pull >= 0 && ownership < pull && pull < entry,
    'ownership must register before the pull module, and the pull module before AVPlayer');
});
