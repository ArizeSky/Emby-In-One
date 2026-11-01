'use strict';
/**
 * Playback-rate adapter tests for window.EIOPlayer (public/web/player-libmedia.js).
 *
 * The real getter and setter are exercised on an Object.create(prototype)
 * instance. `core` (engine) and `video` (native element) are separate paths:
 * exactly one of them is installed per instance, so a core test can never pass
 * by writing the native element. Core fakes use the real synchronous
 * setPlaybackRate/getPlaybackRate vendor interface; the wrapper is async but
 * does not await core. `_isCurrentEngine` is the real implementation.
 *
 * These are unit tests: no real engine, decoder, audio output, pitch handling,
 * media element, network or running application is involved.
 */
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const PLAYER_FILE = path.join(__dirname, '../public/web/player-libmedia.js');
const RATES = [0.5, 1, 1.25, 1.5, 2];

function loadProto() {
  const noop = () => {};
  const sandbox = {
    console: { log: noop, info: noop, warn: noop, error: noop, debug: noop },
    AbortController,
    AbortSignal,
    DOMException,
    setTimeout,
    clearTimeout,
    setInterval,
    clearInterval,
    queueMicrotask,
    URL,
    location: { origin: 'http://127.0.0.1' },
    navigator: { userAgent: 'node-unit-test' },
    performance: { now: () => 0 },
    fetch: () => Promise.reject(new Error('network disabled in tests')),
    document: {
      createElement: () => ({}),
      createElementNS: () => ({}),
      querySelector: () => null,
      querySelectorAll: () => [],
      getElementById: () => null,
      addEventListener: noop,
      removeEventListener: noop,
    },
  };
  sandbox.window = sandbox;
  sandbox.self = sandbox;
  vm.createContext(sandbox);
  vm.runInContext(fs.readFileSync(PLAYER_FILE, 'utf8'), sandbox, { filename: PLAYER_FILE });
  const Player = sandbox.EIOPlayer || sandbox.window.EIOPlayer;
  assert.equal(typeof Player, 'function', 'module must expose window.EIOPlayer');
  return Player.prototype;
}

const proto = loadProto();
const readRate = (player) => player.playbackRate;

function makeCore(options = {}) {
  const core = { value: 1, sets: [], isLive: () => options.live === true };
  if (options.setter !== false) {
    core.setPlaybackRate = function (rate) {
      core.sets.push(rate);
      core.value = rate;
      if (options.onSet) options.onSet(rate);
    };
  }
  if (options.getter !== false) {
    core.getPlaybackRate = function () {
      return options.getOverride ? options.getOverride(core.value) : core.value;
    };
  }
  return core;
}

function makeVideo(overrideSet, duration) {
  const state = { value: 1, sets: [] };
  const video = duration === undefined ? {} : { duration };
  Object.defineProperty(video, 'playbackRate', {
    configurable: true,
    enumerable: true,
    get() { return state.value; },
    set(rate) {
      state.sets.push(rate);
      if (overrideSet) overrideSet(rate, state);
      else state.value = rate;
    },
  });
  video.state = state;
  return video;
}

function makePlayer({ core = null, video = null, mediaSource = null } = {}) {
  const player = Object.create(proto);
  player._destroyed = false;
  player._engineGeneration = 1;
  player.core = core;
  player.video = video;
  player.mediaSource = mediaSource;
  const emitted = [];
  player._emitState = function () { emitted.push(this.playbackRate); };
  return { player, emitted };
}

async function attempt(player, rate) {
  try {
    return { ok: true, value: await proto.setPlaybackRate.call(player, rate) };
  } catch (error) {
    return { ok: false, error };
  }
}

function assertNoCommit(made, player) {
  assert.equal(player._playbackRate, undefined);
  assert.equal(readRate(player), 1);
  assert.deepEqual(made.emitted, []);
}

test('exposes the real getter and setPlaybackRate setter', () => {
  const descriptor = Object.getOwnPropertyDescriptor(proto, 'playbackRate');
  assert.equal(typeof descriptor?.get, 'function');
  assert.equal(typeof proto.setPlaybackRate, 'function');
});

test('default playback rate is 1x', () => {
  assert.equal(readRate(makePlayer().player), 1);
});

test('five supported speeds commit through the core engine only', async () => {
  for (const rate of RATES) {
    const core = makeCore();
    const { player, emitted } = makePlayer({ core });
    const result = await attempt(player, rate);
    assert.equal(result.ok, true, `rate ${rate}`);
    assert.equal(result.value, rate);
    assert.deepEqual(core.sets, [rate]);
    assert.equal(core.value, rate);
    assert.equal(readRate(player), rate);
    assert.equal(player._playbackRate, rate);
    assert.deepEqual(emitted, [rate]);
  }
});

test('five supported speeds commit through the native element only', async () => {
  for (const rate of RATES) {
    const video = makeVideo();
    const { player, emitted } = makePlayer({ video });
    const result = await attempt(player, rate);
    assert.equal(result.ok, true, `rate ${rate}`);
    assert.equal(result.value, rate);
    assert.deepEqual(video.state.sets, [rate]);
    assert.equal(video.state.value, rate);
    assert.equal(readRate(player), rate);
    assert.equal(player._playbackRate, rate);
    assert.deepEqual(emitted, [rate]);
  }
});

test('invalid rates are rejected before any engine write', async () => {
  for (const bad of [3, 0, -1, 0.75, '2', NaN, Infinity, null, undefined]) {
    const core = makeCore();
    const { player, emitted } = makePlayer({ core });
    const result = await attempt(player, bad);
    assert.equal(result.ok, false, `rate ${String(bad)}`);
    assert.equal(result.error.name, 'TypeError');
    assert.deepEqual(core.sets, []);
    assert.deepEqual(emitted, []);
    assert.equal(readRate(player), 1);
  }
});

test('live core rejects the change', async () => {
  const core = makeCore({ live: true });
  const { player, emitted } = makePlayer({ core });
  const result = await attempt(player, 1.25);
  assert.equal(result.ok, false);
  assert.match(result.error.message, /直播/);
  assert.deepEqual(core.sets, []);
  assert.deepEqual(emitted, []);
  assert.equal(readRate(player), 1);
});

test('core without a vendor setter or getter is rejected', async () => {
  for (const missing of ['setter', 'getter']) {
    const core = makeCore({ [missing]: false });
    const { player, emitted } = makePlayer({ core });
    const result = await attempt(player, 1.25);
    assert.equal(result.ok, false, `missing ${missing}`);
    assert.match(result.error.message, /不支持/);
    assert.deepEqual(emitted, []);
    assert.equal(readRate(player), 1);
  }
});

test('core setter throw and getter mismatch do not commit state', async () => {
  const throwing = makeCore();
  throwing.setPlaybackRate = () => { throw new Error('vendor set failed'); };
  const first = makePlayer({ core: throwing });
  const thrown = await attempt(first.player, 1.25);
  assert.equal(thrown.ok, false);
  assert.match(thrown.error.message, /vendor set failed/);
  assert.deepEqual(first.emitted, []);
  assert.equal(readRate(first.player), 1);

  const mismatched = makeCore({ getOverride: () => 1.25 });
  const second = makePlayer({ core: mismatched });
  const wrong = await attempt(second.player, 1.5);
  assert.equal(wrong.ok, false);
  assert.match(wrong.error.message, /无法应用/);
  // The readback never confirms the requested rate, so one restore to the
  // previous rate is attempted; it cannot confirm either and must not commit.
  assert.deepEqual(mismatched.sets, [1.5, 1]);
  assert.deepEqual(second.emitted, []);
  assert.equal(readRate(second.player), 1);
});

test('native setter throw and getter mismatch do not commit state', async () => {
  const throwing = makeVideo(() => { throw new Error('native set failed'); });
  const first = makePlayer({ video: throwing });
  const thrown = await attempt(first.player, 1.25);
  assert.equal(thrown.ok, false);
  assert.match(thrown.error.message, /native set failed/);
  assert.deepEqual(first.emitted, []);
  assert.equal(readRate(first.player), 1);

  const mismatched = makeVideo((rate, state) => { state.value = 1.25; });
  const second = makePlayer({ video: mismatched });
  const wrong = await attempt(second.player, 1.5);
  assert.equal(wrong.ok, false);
  assert.match(wrong.error.message, /无法应用/);
  // One restore attempt to the previous rate, still unconfirmed (the element
  // keeps reporting 1.25), so nothing may be committed.
  assert.deepEqual(mismatched.state.sets, [1.5, 1]);
  assert.equal(mismatched.state.value, 1.25);
  assert.deepEqual(second.emitted, []);
  assert.equal(readRate(second.player), 1);
});

test('infinite stream rejects even when the core is finite', async () => {
  const core = makeCore();
  const mediaSource = { IsInfiniteStream: true };
  const made = makePlayer({ core, mediaSource });
  const result = await attempt(made.player, 1.25);
  assert.equal(result.ok, false);
  assert.match(result.error.message, /直播/);
  assert.deepEqual(core.sets, []);
  assert.deepEqual(made.emitted, []);
  assertNoCommit(made, made.player);
  assert.equal(readRate(made.player), 1);
});

test('infinite native video duration rejects without a write', async () => {
  const video = makeVideo(undefined, Infinity);
  const made = makePlayer({ video });
  const result = await attempt(made.player, 1.5);
  assert.equal(result.ok, false);
  assert.match(result.error.message, /直播/);
  assert.deepEqual(video.state.sets, []);
  assert.deepEqual(made.emitted, []);
  assertNoCommit(made, made.player);
  assert.equal(readRate(made.player), 1);
});

test('reentrant generation replacement aborts the commit', async () => {
  let player;
  const replacement = makeCore();
  const core = makeCore({
    onSet: () => {
      player._engineGeneration += 1;
      player.core = replacement;
    },
  });
  const made = makePlayer({ core });
  player = made.player;
  const result = await attempt(player, 2);
  assert.equal(result.ok, false);
  assert.equal(result.error.name, 'AbortError');
  assert.deepEqual(core.sets, [2]);
  assert.deepEqual(replacement.sets, []);
  assert.deepEqual(made.emitted, []);
  assert.equal(readRate(player), 1);
});

test('destroyed or engine-less players reject without writes', async () => {
  const core = makeCore();
  const destroyed = makePlayer({ core });
  destroyed.player._destroyed = true;
  const dead = await attempt(destroyed.player, 1.25);
  assert.equal(dead.ok, false);
  assert.equal(dead.error.name, 'AbortError');
  assert.deepEqual(core.sets, []);
  assert.deepEqual(destroyed.emitted, []);

  const empty = makePlayer();
  const missing = await attempt(empty.player, 1.25);
  assert.equal(missing.ok, false);
  assert.match(missing.error.message, /尚未就绪/);
  assert.deepEqual(empty.emitted, []);
  assert.equal(readRate(empty.player), 1);
});

test('repeated changes converge on the last confirmed rate', async () => {
  const core = makeCore();
  const { player, emitted } = makePlayer({ core });
  assert.equal((await attempt(player, 1.25)).ok, true);
  assert.equal((await attempt(player, 2)).ok, true);
  assert.deepEqual(core.sets, [1.25, 2]);
  assert.equal(readRate(player), 2);
  assert.deepEqual(emitted, [1.25, 2]);
});

test('a setter throwing after a partial write restores the previous rate once', async () => {
  const core = makeCore();
  const write = core.setPlaybackRate;
  core.setPlaybackRate = function (rate) {
    write(rate);
    if (rate !== 1) throw new Error('vendor set failed after write');
  };
  const first = makePlayer({ core });
  const thrown = await attempt(first.player, 1.25);
  assert.equal(thrown.ok, false);
  assert.match(thrown.error.message, /vendor set failed after write/);
  assert.deepEqual(core.sets, [1.25, 1]);
  assert.equal(core.value, 1);
  assertNoCommit(first, first.player);

  const video = makeVideo((rate, state) => {
    state.value = rate;
    if (rate !== 1) throw new Error('native set failed after write');
  });
  const second = makePlayer({ video });
  const nativeThrown = await attempt(second.player, 1.25);
  assert.equal(nativeThrown.ok, false);
  assert.match(nativeThrown.error.message, /native set failed after write/);
  assert.deepEqual(video.state.sets, [1.25, 1]);
  assert.equal(video.state.value, 1);
  assertNoCommit(second, second.player);
});

test('a mismatched readback restores the previous rate once and keeps the fixed error', async () => {
  const core = makeCore({ getOverride: value => (value === 1.5 ? 1.25 : value) });
  const first = makePlayer({ core });
  const wrong = await attempt(first.player, 1.5);
  assert.equal(wrong.ok, false);
  assert.match(wrong.error.message, /无法应用/);
  assert.deepEqual(core.sets, [1.5, 1]);
  assert.equal(core.value, 1);
  assertNoCommit(first, first.player);

  const video = makeVideo((rate, state) => { state.value = rate === 1.5 ? 1.25 : rate; });
  const second = makePlayer({ video });
  const nativeWrong = await attempt(second.player, 1.5);
  assert.equal(nativeWrong.ok, false);
  assert.match(nativeWrong.error.message, /无法应用/);
  assert.deepEqual(video.state.sets, [1.5, 1]);
  assert.equal(video.state.value, 1);
  assertNoCommit(second, second.player);
});

test('a failing rollback is attempted once and never commits the failed rate', async () => {
  const core = makeCore();
  core.setPlaybackRate = function (rate) {
    core.sets.push(rate);
    core.value = rate;
    throw new Error('vendor set failed');
  };
  const first = makePlayer({ core });
  const thrown = await attempt(first.player, 1.5);
  assert.equal(thrown.ok, false);
  assert.equal(thrown.error.message, 'vendor set failed');
  // Exactly one write plus one bounded rollback attempt; no retry loop.
  assert.deepEqual(core.sets, [1.5, 1]);
  assertNoCommit(first, first.player);

  const video = makeVideo((rate, state) => {
    state.value = rate;
    throw new Error('native set failed');
  });
  const second = makePlayer({ video });
  const nativeThrown = await attempt(second.player, 1.5);
  assert.equal(nativeThrown.ok, false);
  assert.equal(nativeThrown.error.message, 'native set failed');
  assert.deepEqual(video.state.sets, [1.5, 1]);
  assertNoCommit(second, second.player);
});

test('a replaced generation is never rolled back on the old engine', async () => {
  let player;
  const replacement = makeCore();
  const core = makeCore();
  core.setPlaybackRate = function (rate) {
    core.sets.push(rate);
    core.value = rate;
    player._engineGeneration += 1;
    player.core = replacement;
    throw new Error('vendor set failed');
  };
  const first = makePlayer({ core });
  player = first.player;
  const thrown = await attempt(player, 2);
  assert.equal(thrown.ok, false);
  assert.equal(thrown.error.message, 'vendor set failed');
  assert.deepEqual(core.sets, [2]);
  assert.deepEqual(replacement.sets, []);
  assertNoCommit(first, player);

  let nativePlayer;
  const video = makeVideo((rate, state) => {
    state.value = rate;
    nativePlayer._engineGeneration += 1;
  });
  const second = makePlayer({ video });
  nativePlayer = second.player;
  const nativeChanged = await attempt(nativePlayer, 1.5);
  assert.equal(nativeChanged.ok, false);
  assert.equal(nativeChanged.error.name, 'AbortError');
  assert.deepEqual(video.state.sets, [1.5]);
  assertNoCommit(second, nativePlayer);
});
