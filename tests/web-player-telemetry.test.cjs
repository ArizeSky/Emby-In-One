'use strict';
/*
 * P4 adapter wiring regressions: frozen telemetry state fields, the single host
 * capture refresh, and explicit-unknown fallbacks. Fake DOM/TimeRanges only:
 * no real media, no network, no timers beyond the player's own, and the two real
 * helper modules are executed as-is instead of a reimplemented algorithm.
 */
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const WEB_DIR = path.join(__dirname, '..', 'public', 'web');
const HELPERS = ['player-progress.js', 'player-buffer.js'];

function script(name) {
  return fs.readFileSync(path.join(WEB_DIR, name), 'utf8');
}

// Fixtures cross the vm boundary, so comparisons go through plain JSON values.
function plain(value) {
  return JSON.parse(JSON.stringify(value));
}

function timeRanges(pairs) {
  const list = pairs.map((pair) => ({ start: pair[0], end: pair[1] }));
  return {
    length: list.length,
    start(index) { return list[index].start; },
    end(index) { return list[index].end; },
  };
}

let nextElementId = 0;

// Every fake element is listener aware because the player owns its host and the
// buffer adapter queries the host. Ownership is compared by stable id so the
// fixtures keep working across the vm object boundary.
function element() {
  const el = {
    id: 'fake-' + (nextElementId += 1),
    children: [],
    className: '',
    clientWidth: 640,
    clientHeight: 360,
    listeners: new Map(),
    append(child) { el.children.push(child); },
    prepend(child) { el.children.unshift(child); },
    replaceChildren() { el.children.length = 0; },
    remove() { el.removed = true; },
    removeAttribute() {},
    load() {},
    pause() {},
    play() { return Promise.resolve(); },
    contains(node) {
      if (!node || typeof node !== 'object' || node === el) return false;
      return el.children.some((child) => child && child.id === node.id);
    },
    querySelectorAll(selector) {
      if (selector !== 'video,audio') return [];
      return el.children.slice();
    },
    addEventListener(type, handler, options) {
      const signal = options && options.signal;
      if (signal && signal.aborted) return;
      const entry = { handler, options };
      const list = el.listeners.get(type) || [];
      list.push(entry);
      el.listeners.set(type, list);
      if (signal) {
        signal.addEventListener('abort', () => el.removeEventListener(type, handler), { once: true });
      }
    },
    removeEventListener(type, handler) {
      const list = el.listeners.get(type) || [];
      const index = list.findIndex((entry) => entry.handler === handler);
      if (index !== -1) list.splice(index, 1);
    },
    // Capture-phase stand-in: the player never relies on bubbling, so dispatching
    // straight at the host listener registry is the fake equivalent.
    dispatch(type, target) {
      const list = (el.listeners.get(type) || []).slice();
      for (const entry of list) entry.handler({ type, target: target || el });
      return list.length;
    },
    listenerCount(type) { return (el.listeners.get(type) || []).length; },
  };
  return el;
}

function media(options = {}) {
  const el = element();
  el.readyState = options.readyState === undefined ? 2 : options.readyState;
  el.buffered = options.buffered === undefined ? timeRanges([[0, 10]]) : options.buffered;
  el.duration = options.duration === undefined ? 600 : options.duration;
  return el;
}

function fakeCore(isMSE) {
  return { isMSE: () => isMSE, destroy() {} };
}

function harness(options = {}) {
  const states = [];
  const timers = { intervals: 0, timeouts: 0 };
  const sandbox = {
    // `window` is deliberately a different object from the vm globalThis.
    window: {},
    location: { origin: 'http://localhost:8096' },
    document: { createElement: () => element() },
    ResizeObserver: class { observe() {} disconnect() {} },
    URL,
    AbortController,
    DOMException,
    setInterval(...args) { timers.intervals += 1; return setInterval(...args); },
    clearInterval,
    setTimeout(...args) { timers.timeouts += 1; return setTimeout(...args); },
    clearTimeout,
    console,
  };
  vm.createContext(sandbox);
  if (options.modules !== false) {
    for (const name of HELPERS) vm.runInContext(script(name), sandbox);
  }
  vm.runInContext(script('player-libmedia.js'), sandbox);
  const container = element();
  const player = new sandbox.window.EIOPlayer(container);
  player.onState = (state) => states.push(state);
  return { sandbox, container, player, host: player.host, states, timers };
}

function attachNative(h, video) {
  h.player.mode = 'native';
  h.player.video = video;
  h.host.append(video);
}

test('frozen state keeps legacy fields and defaults to unknown without the modules', async () => {
  const h = harness({ modules: false });
  const video = media();
  attachNative(h, video);
  h.player.positionTicks = 125000000;
  h.player.duration = 600;
  h.player.mediaSource = { Bitrate: 8000000 };
  h.player._emitState({ loading: true, message: '正在加载媒体…' });

  const state = h.states.at(-1);
  const epoch = h.player._engineGeneration;
  assert.equal(state.mode, 'native');
  assert.equal(state.playing, false);
  assert.equal(state.time, 12.5);
  assert.equal(state.duration, 600);
  assert.equal(state.loading, true);
  assert.equal(state.message, '正在加载媒体…');
  assert.deepEqual(plain(state.buffered), { status: 'unknown', ranges: [], epoch });
  assert.equal(state.sourceBitrateMbps, null);
  assert.deepEqual(plain(state.receiveRate),
    { status: 'unknown', bytesPerSec: null, epoch, sampledAt: null });
  await h.player.destroy();
});

test('native buffered ranges come from the real buffer module snapshot', async () => {
  const h = harness();
  const video = media({ buffered: timeRanges([[0, 10], [20, 30]]) });
  attachNative(h, video);
  h.player.duration = 600;
  h.player.mediaSource = { Bitrate: 8000000 };
  h.player._emitState();

  const state = h.states.at(-1);
  const buffered = plain(state.buffered);
  assert.equal(buffered.status, 'ok');
  assert.deepEqual(buffered.ranges, [{ start: 0, end: 10 }, { start: 20, end: 30 }]);
  assert.equal(buffered.epoch, h.player._engineGeneration);
  assert.equal(state.sourceBitrateMbps, 8);

  // The frozen fields are measured last, so an `extra` payload cannot substitute
  // an optimistic buffered/sourceBitrate/receiveRate value for a measured one.
  h.player._emitState({
    buffered: { status: 'ok', ranges: [{ start: 0, end: 999 }] },
    sourceBitrateMbps: 99,
    receiveRate: { status: 'ok', bytesPerSec: 1 },
  });
  const frozen = h.states.at(-1);
  assert.deepEqual(plain(frozen.buffered).ranges, [{ start: 0, end: 10 }, { start: 20, end: 30 }]);
  assert.equal(frozen.sourceBitrateMbps, 8);
  assert.equal(plain(frozen.receiveRate).status, 'unknown');
  await h.player.destroy();
});

test('known empty buffered stays distinct from unknown', async () => {
  const h = harness();
  h.player.duration = 600;

  const empty = media({ readyState: 1, buffered: timeRanges([]) });
  attachNative(h, empty);
  h.player._emitState();
  assert.equal(plain(h.states.at(-1).buffered).status, 'ok');
  assert.deepEqual(plain(h.states.at(-1).buffered).ranges, []);

  const emptyRangeObject = media({ readyState: 2, buffered: { length: 0 } });
  attachNative(h, emptyRangeObject);
  h.player._emitState();
  assert.equal(plain(h.states.at(-1).buffered).status, 'ok');
  assert.deepEqual(plain(h.states.at(-1).buffered).ranges, []);

  const noMetadata = media({ readyState: 0 });
  attachNative(h, noMetadata);
  h.player._emitState();
  assert.equal(plain(h.states.at(-1).buffered).status, 'unknown');
  assert.deepEqual(plain(h.states.at(-1).buffered).ranges, []);

  // A real TimeRanges object that the player does not own is not this player's
  // buffer, so it must not become a "known" range list.
  const foreign = media();
  h.player.mode = 'native';
  h.player.video = foreign;
  h.player._emitState();
  assert.equal(plain(h.states.at(-1).buffered).status, 'unknown');
  assert.deepEqual(plain(h.states.at(-1).buffered).ranges, []);

  await h.player.destroy();
});

test('MSE buffered intersects two real tracks and keeps the gap', async () => {
  const h = harness();
  const video = media({ buffered: timeRanges([[0, 10], [20, 30]]) });
  const audio = media({ buffered: timeRanges([[5, 15], [18, 25]]) });
  h.host.append(video);
  h.host.append(audio);
  h.player.mode = 'libmedia';
  h.player.core = fakeCore(true);
  h.player.duration = 600;
  h.player._emitState();

  const buffered = plain(h.states.at(-1).buffered);
  assert.equal(buffered.status, 'ok');
  // Gap between 10 and 20 is not filled: islands stay separate.
  assert.deepEqual(buffered.ranges, [{ start: 5, end: 10 }, { start: 20, end: 25 }]);
  await h.player.destroy();
});

test('software decode reports unknown buffered instead of a fake white bar', async () => {
  const h = harness();
  const video = media({ buffered: timeRanges([[0, 10]]) });
  h.host.append(video);
  h.player.mode = 'libmedia';
  h.player.core = fakeCore(false);
  h.player.mediaSource = { Bitrate: 1500000 };
  h.player.duration = 600;
  h.player._emitState();

  const buffered = plain(h.states.at(-1).buffered);
  assert.equal(buffered.status, 'unknown');
  assert.deepEqual(buffered.ranges, []);
  // Source bitrate is independent of the buffer route and still measured.
  assert.equal(h.states.at(-1).sourceBitrateMbps, 1.5);
  await h.player.destroy();
});

test('source bitrate only trusts a finite positive selected Bitrate', async () => {
  const h = harness();
  const cases = [
    [{ Bitrate: 8000000 }, 8],
    [{ Bitrate: 1500000 }, 1.5],
    [{ Bitrate: 0 }, null],
    [{ Bitrate: -8000000 }, null],
    [{ Bitrate: '8000000' }, null],
    [{ Bitrate: Infinity }, null],
    [{ Bandwidth: 8000000, bufferReceiveBytes: 120000 }, null],
    [{}, null],
    [undefined, null],
  ];
  for (const [mediaSource, expected] of cases) {
    h.player.mediaSource = mediaSource;
    h.player._emitState();
    assert.equal(h.states.at(-1).sourceBitrateMbps, expected);
  }
  await h.player.destroy();
});

test('receive rate stays an explicit unknown and never uses IO counters', async () => {
  const h = harness();
  const video = media({ buffered: timeRanges([[0, 30]]) });
  attachNative(h, video);
  h.player.duration = 600;
  h.player.mediaSource = { Bitrate: 8000000, bufferReceiveBytes: 999999 };
  h.player.core = {
    isMSE: () => true,
    bufferReceiveBytes: 123456,
    getBufferReceiveBytes() { return 42n; },
    destroy() {},
  };
  h.player._emitState();

  const state = h.states.at(-1);
  assert.deepEqual(plain(state.receiveRate), {
    status: 'unknown', bytesPerSec: null, epoch: h.player._engineGeneration, sampledAt: null,
  });
  assert.equal(state.sourceBitrateMbps, 8);
  assert.equal(plain(state.buffered).status, 'ok');
  await h.player.destroy();
});

test('a missing or malformed buffer adapter never interrupts state emission', async () => {
  const h = harness();
  const video = media({ buffered: timeRanges([[0, 24]]) });
  attachNative(h, video);
  h.player.duration = 600;

  const cases = [
    [{ snapshot() { throw new Error('boom'); } }, 'unknown', []],
    [{ snapshot: () => 'nope' }, 'unknown', []],
    [{ snapshot: () => ({ status: 'ok', ranges: [{ start: '0', end: 1 }] }) }, 'unknown', []],
    [{ snapshot: () => ({ status: 'ok', ranges: [{ start: 0, end: 1 }] }) }, 'ok', [{ start: 0, end: 1 }]],
    [{ snapshot: () => ({ status: 'ok', ranges: [] }) }, 'ok', []],
    [{ snapshot: () => ({ status: 'unknown', ranges: [{ start: 0, end: 1 }], epoch: 3 }) }, 'unknown', []],
  ];
  for (const [adapter, status, ranges] of cases) {
    h.sandbox.window.EIOPlayerBuffer = adapter;
    assert.doesNotThrow(() => h.player._emitState());
    const buffered = plain(h.states.at(-1).buffered);
    assert.equal(buffered.status, status);
    assert.deepEqual(buffered.ranges, ranges);
    assert.equal(buffered.epoch, h.player._engineGeneration);
  }

  delete h.sandbox.window.EIOPlayerBuffer;
  assert.doesNotThrow(() => h.player._emitState());
  assert.equal(plain(h.states.at(-1).buffered).status, 'unknown');
  assert.doesNotThrow(() => h.player._emitState());
  assert.equal(h.states.at(-1).mode, 'native');
  await h.player.destroy();
});

test('buffered ranges accept 128 entries and reject 129 without traversing', async () => {
  const h = harness();
  const generation = h.player._engineGeneration;
  const accepted = Array.from({ length: 128 }, (ignored, index) => ({
    start: index,
    end: index + 0.5,
  }));
  h.sandbox.window.EIOPlayerBuffer = {
    snapshot: () => ({ status: 'ok', ranges: accepted, epoch: generation }),
  };
  const ok = plain(h.player._bufferedSnapshot());
  assert.equal(ok.status, 'ok');
  assert.equal(ok.ranges.length, 128);
  assert.deepEqual(ok.ranges[127], { start: 127, end: 127.5 });

  let reads = 0;
  const oversized = new Array(129);
  Object.defineProperty(oversized, 0, {
    get() {
      reads += 1;
      return { start: 0, end: 1 };
    },
  });
  h.sandbox.window.EIOPlayerBuffer = {
    snapshot: () => ({ status: 'ok', ranges: oversized, epoch: generation }),
  };
  const rejected = plain(h.player._bufferedSnapshot());
  assert.equal(rejected.status, 'unknown');
  assert.deepEqual(rejected.ranges, []);
  assert.equal(reads, 0, 'an over-budget list must be rejected before traversal');
  await h.player.destroy();
});

test('an explicit stale or malformed snapshot epoch is never backfilled', async () => {
  const h = harness();
  const generation = h.player._engineGeneration;
  const ranges = [{ start: 0, end: 10 }];
  const invalidEpochs = [
    generation - 1, generation + 1, -1, 1.5, Infinity,
    Number.MAX_SAFE_INTEGER + 1, '0', null, undefined,
  ];
  for (const epoch of invalidEpochs) {
    h.sandbox.window.EIOPlayerBuffer = {
      snapshot: () => ({ status: 'ok', ranges, epoch }),
    };
    const buffered = plain(h.player._bufferedSnapshot());
    assert.equal(buffered.status, 'unknown', 'epoch ' + String(epoch));
    assert.deepEqual(buffered.ranges, []);
    assert.equal(buffered.epoch, generation, 'never rewrite an epoch');
  }

  // Old fake adapters without an epoch property keep the compatibility fallback.
  h.sandbox.window.EIOPlayerBuffer = {
    snapshot: () => ({ status: 'ok', ranges }),
  };
  const fallback = plain(h.player._bufferedSnapshot());
  assert.equal(fallback.status, 'ok');
  assert.equal(fallback.epoch, generation);
  await h.player.destroy();
});

test('a generation change while ranges are copied makes the snapshot unknown', async () => {
  const h = harness();
  const generation = h.player._engineGeneration;
  let changed = false;
  const ranges = [{
    get start() {
      if (!changed) {
        changed = true;
        h.player._bumpEngineGeneration();
      }
      return 0;
    },
    end: 10,
  }];
  h.sandbox.window.EIOPlayerBuffer = {
    snapshot: () => ({ status: 'ok', ranges, epoch: generation }),
  };
  const buffered = plain(h.player._bufferedSnapshot());
  assert.equal(changed, true);
  assert.equal(buffered.status, 'unknown');
  assert.deepEqual(buffered.ranges, []);
  assert.equal(buffered.epoch, h.player._engineGeneration);
  assert.equal(buffered.epoch, generation + 1);
  await h.player.destroy();
});

test('a non-safe engine generation never labels buffered data as ok', async () => {
  const h = harness();
  const ranges = [{ start: 0, end: 10 }];
  for (const generation of [-1, 1.5, Infinity, Number.MAX_SAFE_INTEGER + 1, '3']) {
    h.player._engineGeneration = generation;
    h.sandbox.window.EIOPlayerBuffer = {
      snapshot: () => ({ status: 'ok', ranges, epoch: generation }),
    };
    const buffered = plain(h.player._bufferedSnapshot());
    assert.equal(buffered.status, 'unknown', 'generation ' + String(generation));
    assert.deepEqual(buffered.ranges, []);
    assert.equal(buffered.epoch, null);
  }
  h.player._engineGeneration = 0;
  await h.player.destroy();
});

test('paused progress, loadedmetadata and durationchange refresh state once each', async () => {
  const h = harness();
  const video = media({ buffered: timeRanges([[0, 10]]) });
  attachNative(h, video);
  h.player.duration = 600;
  h.player.playing = false;
  h.player._emitState();

  assert.equal(h.states.length, 1);
  assert.equal(h.host.listenerCount('progress'), 1);
  assert.equal(h.host.listenerCount('loadedmetadata'), 1);
  assert.equal(h.host.listenerCount('durationchange'), 1);

  video.buffered = timeRanges([[0, 10], [12, 40]]);
  h.host.dispatch('progress', video);
  assert.equal(h.states.length, 2);
  assert.equal(h.states.at(-1).playing, false);
  assert.deepEqual(plain(h.states.at(-1).buffered).ranges,
    [{ start: 0, end: 10 }, { start: 12, end: 40 }]);

  video.buffered = timeRanges([[0, 60]]);
  h.host.dispatch('loadedmetadata', video);
  h.player.duration = 900;
  h.host.dispatch('durationchange', video);
  assert.equal(h.states.length, 4);
  assert.deepEqual(plain(h.states.at(-1).buffered).ranges, [{ start: 0, end: 60 }]);
  assert.equal(h.states.at(-1).duration, 900);

  // Emitting repeatedly must not install more capture listeners or timers.
  for (let index = 0; index < 5; index += 1) h.player._emitState();
  assert.equal(h.states.length, 9);
  assert.equal(h.host.listenerCount('progress'), 1);
  assert.equal(h.host.listenerCount('loadedmetadata'), 1);
  assert.equal(h.host.listenerCount('durationchange'), 1);
  assert.equal(h.timers.intervals, 0);
  assert.equal(h.timers.timeouts, 0);
  await h.player.destroy();
});

test('host refresh ignores foreign targets and the host itself', async () => {
  const h = harness();
  const video = media();
  attachNative(h, video);
  h.player._emitState();
  const baseline = h.states.length;

  const foreign = media();
  h.host.dispatch('progress', foreign);
  h.host.dispatch('loadedmetadata', foreign);
  h.host.dispatch('durationchange', h.host);
  assert.equal(h.states.length, baseline);

  h.host.dispatch('progress', video);
  assert.equal(h.states.length, baseline + 1);
  await h.player.destroy();
});

test('destroy removes capture listeners and ignores late events', async () => {
  const h = harness();
  const video = media();
  attachNative(h, video);
  h.player._emitState();
  const baseline = h.states.length;
  assert.equal(h.host.listenerCount('progress'), 1);

  await h.player.destroy();
  assert.equal(h.host.listenerCount('progress'), 0);
  assert.equal(h.host.listenerCount('loadedmetadata'), 0);
  assert.equal(h.host.listenerCount('durationchange'), 0);

  assert.doesNotThrow(() => h.host.dispatch('progress', video));
  assert.equal(h.states.length, baseline);
  assert.doesNotThrow(() => h.player._emitState());
  assert.equal(h.states.length, baseline);
});

test('engine generation changes are visible on the next state', async () => {
  const h = harness();
  const video = media();
  attachNative(h, video);
  h.player.duration = 600;
  h.player._emitState();

  const epoch = h.player._engineGeneration;
  assert.equal(plain(h.states.at(-1).buffered).epoch, epoch);
  assert.equal(plain(h.states.at(-1).receiveRate).epoch, epoch);

  h.player._bumpEngineGeneration();
  h.player._emitState();
  assert.equal(plain(h.states.at(-1).buffered).epoch, epoch + 1);
  assert.equal(plain(h.states.at(-1).receiveRate).epoch, epoch + 1);
  await h.player.destroy();
});

test('the helper UMD prefers window and keeps the CommonJS path', () => {
  const withWindow = { location: { origin: 'http://localhost:8096' }, window: {} };
  vm.createContext(withWindow);
  for (const name of HELPERS) vm.runInContext(script(name), withWindow);
  assert.equal(typeof withWindow.window.EIOPlayerProgress, 'object');
  assert.equal(typeof withWindow.window.EIOPlayerProgress.getSourceBitrateMbps, 'function');
  assert.equal(typeof withWindow.window.EIOPlayerBuffer, 'object');
  assert.equal(typeof withWindow.window.EIOPlayerBuffer.snapshot, 'function');
  // The page global alone must not be required to receive the modules.
  assert.equal(withWindow.EIOPlayerProgress, undefined);
  assert.equal(withWindow.EIOPlayerBuffer, undefined);

  const noWindow = { location: { origin: 'http://localhost:8096' } };
  vm.createContext(noWindow);
  for (const name of HELPERS) vm.runInContext(script(name), noWindow);
  assert.equal(typeof noWindow.EIOPlayerProgress.getSourceBitrateMbps, 'function');
  assert.equal(typeof noWindow.EIOPlayerBuffer.snapshot, 'function');

  const progress = require(path.join(WEB_DIR, 'player-progress.js'));
  const buffer = require(path.join(WEB_DIR, 'player-buffer.js'));
  assert.equal(progress.getSourceBitrateMbps({ Bitrate: 4000000 }), 4);
  assert.equal(typeof buffer.snapshot, 'function');
});
