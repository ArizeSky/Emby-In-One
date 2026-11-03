'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { Worker } = require('node:worker_threads');
const clock = require('../public/web/player-native-clock.js');
const sources = require('../public/web/player-native-clock-source.js');

function fixture() {
  const descriptor = clock.create();
  const writer = clock.writer(descriptor.sab);
  let active = true;
  const state = { seconds: 1, seeking: false, readyState: 4, paused: false };
  const source = sources.create({ writer, isCurrent: () => active,
    readNativeState: () => ({ ...state }) });
  return { descriptor, writer, state, source, retire: () => { active = false; },
    snapshot: () => clock.read(descriptor.sab) };
}

test('clock starts unknown, permits only one writer, and preserves exact safe milliseconds', () => {
  const f = fixture();
  assert.equal(f.snapshot().mediaMs, null);
  assert.throws(() => clock.writer(f.descriptor.sab), { code: 'EIO_NATIVE_CLOCK_WRITER_ALREADY_USED' });
  assert.equal(f.writer.publish(Number.MAX_SAFE_INTEGER, 0, false), true);
  assert.equal(f.snapshot().mediaMs, Number.MAX_SAFE_INTEGER);
  assert.equal(f.writer.publish(1, 0, false), false, 'no backward movement within an epoch');
  f.writer.invalidate();
  assert.equal(f.snapshot().known, false);
  assert.equal(f.writer.publish(1, 0, false), false, 'old epoch cannot republish');
  assert.equal(f.writer.publish(0, 1, true), true, 'zero is a valid confirmed position');
  assert.equal(f.snapshot().mediaMs, 0);
  assert.equal(f.snapshot().paused, true);
  f.source.dispose();
  assert.equal(f.snapshot().known, false);
  assert.equal(f.writer.publish(10, 2, false), false);
});

test('native source rejects unavailable, seeking, unready and nonfinite samples', () => {
  const f = fixture();
  for (const state of [{ seconds: NaN }, { seconds: Infinity }, { seconds: -1 },
    { seconds: 1, seeking: true }, { seeking: false, readyState: 2 }]) {
    Object.assign(f.state, state);
    assert.equal(f.source.sample('timeupdate'), false);
    assert.equal(f.snapshot().known, false);
  }
  Object.assign(f.state, { seconds: 1.25, readyState: 4, seeking: false });
  assert.equal(f.source.sample('timeupdate'), true);
  assert.equal(f.snapshot().mediaMs, 1250);
  f.retire();
  f.state.seconds = 50;
  assert.equal(f.source.sample('timeupdate'), false);
  assert.equal(f.snapshot().mediaMs, 1250, 'an old element cannot raise the anchor');
});

test('seek targets and seeked events cannot become confirmed anchors', () => {
  const f = fixture();
  assert.equal(f.source.sample('timeupdate'), true);
  assert.equal(f.source.beginSeek(40), true);
  assert.equal(f.snapshot().mediaMs, null);
  f.state.seconds = 40;
  assert.equal(f.source.sample('seeked'), false);
  assert.equal(f.source.sample('timeupdate'), false);
  assert.equal(f.snapshot().known, false);
  f.source.setTarget(39.5);
  f.state.seconds = 39.5;
  assert.equal(f.source.sample('timeupdate'), false);
  f.state.seconds = 39.501;
  assert.equal(f.source.sample('timeupdate'), true);
  assert.equal(f.snapshot().mediaMs, 39501);
});

test('pause only reaffirms an existing position and rate never multiplies media time', () => {
  const f = fixture();
  f.state.paused = true;
  assert.equal(f.source.sample('pause'), false);
  f.state.paused = false;
  f.state.playbackRate = 2;
  assert.equal(f.source.sample('timeupdate'), true);
  assert.equal(f.snapshot().mediaMs, 1000);
  f.state.paused = true;
  f.state.seconds = 1.02;
  assert.equal(f.source.sample('pause'), true);
  assert.equal(f.snapshot().mediaMs, 1000);
  f.state.seconds = 3;
  assert.equal(f.source.sample('pause'), false);
  assert.equal(f.snapshot().mediaMs, 1000);
});

test('a structured-cloned reader in a real Node Worker sees publish and invalidation', { timeout: 5000 }, async t => {
  const f = fixture();
  const worker = new Worker(`const { parentPort, workerData } = require('node:worker_threads');
    const api = require(workerData.path);
    parentPort.on('message', () => parentPort.postMessage(api.read(workerData.sab)));`,
    { eval: true, workerData: { path: require.resolve('../public/web/player-native-clock.js'), sab: f.descriptor.sab } });
  t.after(() => worker.terminate());
  function receive() {
    return new Promise((resolve, reject) => {
      worker.once('message', resolve);
      worker.once('error', reject);
      worker.postMessage('read');
    });
  }
  assert.equal((await receive()).known, false);
  f.source.sample('timeupdate');
  assert.equal((await receive()).mediaMs, 1000);
  f.source.beginSeek(20);
  assert.equal((await receive()).mediaMs, null);
});

test('malformed descriptors and a write-in-progress snapshot fail closed', () => {
  assert.throws(() => clock.read(new SharedArrayBuffer(64)), { code: 'EIO_NATIVE_CLOCK_DESCRIPTOR_INVALID' });
  const f = fixture();
  f.source.sample('timeupdate');
  Atomics.store(new Int32Array(f.descriptor.sab), 2, 3);
  assert.equal(f.snapshot().known, false);
  assert.equal(f.snapshot().mediaMs, null);
});
