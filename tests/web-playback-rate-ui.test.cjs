'use strict';
/**
 * Unit tests for window.EIOPlaybackRateUI (public/web/player-rate-ui.js).
 *
 * The real API is create({ getPlayer, isActive, onActivity, onSuccess }) with
 * refs playbackRate/ratePending/rateError and async selectPlaybackRate().
 * These tests use a fake player and Vue.ref only; they do not prove real engine
 * rate changes, pitch preservation, rendering or upstream behavior.
 */
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const FILE = path.join(__dirname, '../public/web/player-rate-ui.js');
const source = fs.readFileSync(FILE, 'utf8');
const LEVELS = [0.5, 1, 1.25, 1.5, 2];
const ERR_INVALID = '不支持该播放速度';
const ERR_PLAYER = '播放器尚未就绪，无法设置播放速度';
const ERR_SET = '设置播放速度失败，请稍后重试';

function snapshot(ui) {
  return {
    rate: ui.playbackRate.value,
    pending: ui.ratePending.value,
    error: ui.rateError.value,
  };
}

function runtime({ player, getPlayer, isActive, onActivity, onSuccess } = {}) {
  const sandbox = { console };
  sandbox.window = sandbox;
  sandbox.Vue = { ref(value) { return { value }; } };
  vm.createContext(sandbox);
  vm.runInContext(source, sandbox, { filename: FILE });
  const api = sandbox.EIOPlaybackRateUI;
  assert.equal(typeof api?.create, 'function', 'module must expose EIOPlaybackRateUI.create');
  const ui = api.create({
    getPlayer: getPlayer || (() => player),
    isActive: isActive || (() => true),
    onActivity,
    onSuccess,
  });
  return { api, ui };
}

function makePlayer() {
  const player = { playbackRate: 1, calls: [] };
  player.setPlaybackRate = (rate) => {
    player.calls.push(rate);
    player.playbackRate = rate;
    return Promise.resolve(rate);
  };
  return player;
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

test('exposes five rates and initial refs without touching the player', () => {
  const player = makePlayer();
  const { api, ui } = runtime({ player });
  assert.deepEqual(Array.from(api.RATES), LEVELS);
  assert.deepEqual(snapshot(ui), { rate: 1, pending: false, error: '' });
  assert.deepEqual(player.calls, []);
});

test('five supported rates confirm the setter return and getter', async () => {
  for (const rate of LEVELS) {
    const player = makePlayer();
    const successes = [];
    const { ui } = runtime({ player, onSuccess: (value) => successes.push(value) });
    const ok = await ui.selectPlaybackRate(rate);
    assert.equal(ok, true, `rate ${rate}`);
    assert.equal(player.playbackRate, rate, `getter ${rate}`);
    assert.deepEqual(player.calls, [rate]);
    assert.equal(ui.playbackRate.value, rate);
    assert.equal(ui.ratePending.value, false);
    assert.equal(ui.rateError.value, '');
    assert.deepEqual(successes, [rate]);
  }
});

test('invalid rates fail without calling the player', async () => {
  const player = makePlayer();
  const { ui } = runtime({ player });
  for (const bad of [3, 0, 0.75, '1.5', NaN, Infinity, null, undefined]) {
    assert.equal(await ui.selectPlaybackRate(bad), false, `rate ${String(bad)}`);
    assert.equal(ui.rateError.value, ERR_INVALID);
    assert.equal(ui.ratePending.value, false);
    assert.equal(ui.playbackRate.value, 1);
  }
  assert.deepEqual(player.calls, []);
});

test('missing player or setter reports the player error', async () => {
  let activity = 0;
  const missing = runtime({ getPlayer: () => null, onActivity: () => { activity += 1; } });
  assert.equal(await missing.ui.selectPlaybackRate(1.25), false);
  assert.equal(missing.ui.rateError.value, ERR_PLAYER);
  assert.equal(activity, 0);

  const noSetter = runtime({
    player: { playbackRate: 1 },
    onActivity: () => { activity += 1; },
  });
  assert.equal(await noSetter.ui.selectPlaybackRate(1.25), false);
  assert.equal(noSetter.ui.rateError.value, ERR_PLAYER);
  assert.equal(activity, 0);
});

test('inactive UI ignores selection without writing refs', async () => {
  const player = makePlayer();
  let activity = 0;
  const { ui } = runtime({
    player,
    isActive: () => false,
    onActivity: () => { activity += 1; },
  });
  assert.equal(await ui.selectPlaybackRate(1.25), false);
  assert.deepEqual(snapshot(ui), { rate: 1, pending: false, error: '' });
  assert.deepEqual(player.calls, []);
  assert.equal(activity, 0);
});

test('setter rejection and unconfirmed getter both fail closed', async () => {
  const throwing = makePlayer();
  throwing.setPlaybackRate = () => { throw new Error('vendor failed'); };
  const first = runtime({ player: throwing });
  assert.equal(await first.ui.selectPlaybackRate(1.25), false);
  assert.equal(first.ui.rateError.value, ERR_SET);
  assert.equal(first.ui.ratePending.value, false);
  assert.equal(first.ui.playbackRate.value, 1);

  const staleGetter = makePlayer();
  staleGetter.setPlaybackRate = (rate) => {
    staleGetter.calls.push(rate);
    return Promise.resolve(rate);
  };
  const second = runtime({ player: staleGetter });
  assert.equal(await second.ui.selectPlaybackRate(1.25), false);
  assert.equal(second.ui.rateError.value, ERR_SET);
  assert.equal(second.ui.playbackRate.value, 1);
  assert.deepEqual(staleGetter.calls, [1.25]);

  const wrongRate = makePlayer();
  wrongRate.setPlaybackRate = () => {
    wrongRate.playbackRate = 1.5;
    return Promise.resolve(1.5);
  };
  const third = runtime({ player: wrongRate });
  assert.equal(await third.ui.selectPlaybackRate(1.25), false);
  assert.equal(third.ui.rateError.value, ERR_SET);
  assert.equal(third.ui.playbackRate.value, 1);
});

test('pending call is rejected and reset invalidates the old result', async () => {
  const pending = deferred();
  const player = makePlayer();
  player.setPlaybackRate = (rate) => {
    player.calls.push(rate);
    return pending.promise;
  };
  let activity = 0;
  let successes = 0;
  const { ui } = runtime({
    player,
    onActivity: () => { activity += 1; },
    onSuccess: () => { successes += 1; },
  });

  const first = ui.selectPlaybackRate(1.25);
  assert.equal(ui.ratePending.value, true);
  assert.equal(ui.playbackRate.value, 1);
  assert.equal(activity, 1);

  assert.equal(await ui.selectPlaybackRate(1.5), false);
  assert.equal(ui.ratePending.value, true);
  assert.equal(activity, 1);
  assert.deepEqual(player.calls, [1.25]);

  ui.reset();
  assert.deepEqual(snapshot(ui), { rate: 1, pending: false, error: '' });
  pending.resolve(1.25);
  assert.equal(await first, false);
  assert.deepEqual(snapshot(ui), { rate: 1, pending: false, error: '' });
  assert.equal(activity, 1);
  assert.equal(successes, 0);
});

test('a replaced player releases pending without committing a late result', async () => {
  const pending = deferred();
  const firstPlayer = makePlayer();
  firstPlayer.setPlaybackRate = (rate) => {
    firstPlayer.calls.push(rate);
    return pending.promise;
  };
  const secondPlayer = makePlayer();
  let current = firstPlayer;
  let activity = 0;
  let successes = 0;
  const { ui } = runtime({
    getPlayer: () => current,
    onActivity: () => { activity += 1; },
    onSuccess: () => { successes += 1; },
  });

  const first = ui.selectPlaybackRate(1.25);
  current = secondPlayer;
  pending.resolve(1.25);
  assert.equal(await first, false);
  assert.deepEqual(snapshot(ui), { rate: 1, pending: false, error: '' });
  assert.equal(activity, 1);
  assert.equal(successes, 0);
  assert.deepEqual(secondPlayer.calls, []);
  assert.equal(secondPlayer.playbackRate, 1);
});

test('deactivation still releases pending and does not wake inactive controls', async () => {
  const pending = deferred();
  const player = makePlayer();
  player.setPlaybackRate = (rate) => {
    player.calls.push(rate);
    return pending.promise;
  };
  let active = true;
  let activity = 0;
  let successes = 0;
  const { ui } = runtime({
    player,
    isActive: () => active,
    onActivity: () => { activity += 1; },
    onSuccess: () => { successes += 1; },
  });

  const first = ui.selectPlaybackRate(1.25);
  assert.equal(ui.ratePending.value, true);
  assert.equal(activity, 1);

  active = false;
  pending.resolve(1.25);
  assert.equal(await first, false);
  assert.deepEqual(snapshot(ui), { rate: 1, pending: false, error: '' });
  assert.equal(activity, 1);
  assert.equal(successes, 0);

  player.setPlaybackRate = (rate) => {
    player.calls.push(rate);
    player.playbackRate = rate;
    return Promise.resolve(rate);
  };
  active = true;
  assert.equal(await ui.selectPlaybackRate(1.5), true);
  assert.deepEqual(snapshot(ui), { rate: 1.5, pending: false, error: '' });
  // First request start (1), second request start + finally (2 + 1) = 3.
  assert.equal(activity, 3);
  assert.equal(successes, 1);
  assert.deepEqual(player.calls, [1.25, 1.5]);
});

test('a stale result cannot clear pending or wake controls of a newer selection', async () => {
  const older = deferred();
  const newer = deferred();
  const player = makePlayer();
  player.setPlaybackRate = (rate) => {
    player.calls.push(rate);
    return rate === 1.25 ? older.promise : newer.promise;
  };
  let activity = 0;
  let successes = 0;
  const { ui } = runtime({
    player,
    onActivity: () => { activity += 1; },
    onSuccess: () => { successes += 1; },
  });

  const first = ui.selectPlaybackRate(1.25);
  assert.equal(ui.ratePending.value, true);
  ui.reset();
  const second = ui.selectPlaybackRate(1.5);
  assert.equal(ui.ratePending.value, true);
  assert.equal(activity, 2);

  older.resolve(1.25);
  assert.equal(await first, false);
  assert.deepEqual(snapshot(ui), { rate: 1, pending: true, error: '' });
  assert.equal(activity, 2);
  assert.equal(successes, 0);

  player.playbackRate = 1.5;
  newer.resolve(1.5);
  assert.equal(await second, true);
  assert.deepEqual(snapshot(ui), { rate: 1.5, pending: false, error: '' });
  assert.equal(activity, 3);
  assert.equal(successes, 1);
  assert.deepEqual(player.calls, [1.25, 1.5]);
});

test('dispose blocks late success and any new activity', async () => {
  const pending = deferred();
  const player = makePlayer();
  player.setPlaybackRate = (rate) => {
    player.calls.push(rate);
    return pending.promise;
  };
  let activity = 0;
  let successes = 0;
  const { ui } = runtime({
    player,
    onActivity: () => { activity += 1; },
    onSuccess: () => { successes += 1; },
  });

  const first = ui.selectPlaybackRate(1.5);
  assert.equal(activity, 1);
  assert.equal(ui.ratePending.value, true);

  ui.dispose();
  ui.dispose();
  assert.deepEqual(snapshot(ui), { rate: 1, pending: false, error: '' });

  pending.resolve(1.5);
  assert.equal(await first, false);
  assert.deepEqual(snapshot(ui), { rate: 1, pending: false, error: '' });
  assert.equal(activity, 1);
  assert.equal(successes, 0);

  assert.equal(await ui.selectPlaybackRate(1.25), false);
  assert.deepEqual(player.calls, [1.5]);
  assert.equal(activity, 1);
});
