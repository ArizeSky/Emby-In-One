'use strict';
/* Bounded seek budgets in the real player adapter.
 *
 * The libmedia core seek itself cannot be cancelled. These tests cover what the
 * adapter can guarantee: a finite normalized deadline, safe TimeoutError
 * metadata, no late adapter progress write after that deadline, and immediate
 * AbortError when the engine is replaced or destroyed.
 */
const { test, afterEach } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const livePlayers = [];
afterEach(async () => {
  while (livePlayers.length) {
    try { await livePlayers.pop().destroy(); } catch (error) {}
  }
});

const flush = () => new Promise(resolve => setImmediate(resolve));

function fakeClock() {
  let now = 0;
  let serial = 0;
  const timers = new Map();
  const clock = {
    setTimeout(fn, ms) {
      const id = ++serial;
      const delay = Number(ms);
      const safeDelay = Number.isFinite(delay) ? Math.max(0, delay) : 0;
      timers.set(id, { fn, at: now + safeDelay, delay: safeDelay });
      return id;
    },
    clearTimeout(id) { timers.delete(id); },
    pendingDelays() { return [...timers.values()].map(timer => timer.delay); },
    async advance(ms) {
      now += Number(ms) || 0;
      // Advance without waiting: run every timer due at the new time, including
      // callbacks that schedule another already-due timer.
      for (;;) {
        let chosenId = null;
        let chosen = null;
        for (const [id, timer] of timers) {
          if (timer.at > now) continue;
          if (!chosen || timer.at < chosen.at) { chosenId = id; chosen = timer; }
        }
        if (!chosen) break;
        timers.delete(chosenId);
        chosen.fn();
        await flush();
      }
      await flush();
    },
  };
  return clock;
}

function element() {
  return {
    children: [], clientWidth: 640, clientHeight: 360, style: {},
    append(child) { this.children.push(child); },
    prepend(child) { this.children.unshift(child); },
    replaceChildren() { this.children = []; },
    remove() { this.removed = true; },
    addEventListener() {}, removeEventListener() {},
    removeAttribute() {}, load() {}, pause() {}, play() { return Promise.resolve(); },
  };
}

function harness(options = {}) {
  const instances = [];
  const clock = fakeClock();
  class Core {
    constructor(config) {
      this.options = config;
      this.listeners = {};
      this.volume = 1;
      this.seekCalls = [];
      this.currentTime = 0;
      instances.push(this);
    }
    on(event, handler) { this.listeners[event] = handler; }
    async load(url) { this.url = url; }
    async play() {
      this.playCalls = (this.playCalls || 0) + 1;
      this.listeners.played?.();
      this.listeners.firstVideoRendered?.();
    }
    async pause() { this.listeners.paused?.(); }
    async resume() {}
    async seek(timestamp) {
      this.seekCalls.push(timestamp);
      if (this.seekImpl) return this.seekImpl(timestamp);
      this.currentTime = timestamp;
    }
    async destroy() { this.destroyed = true; }
    getDuration() { return 300000n; }
    getStreams() { return []; }
    hasVideo() { return true; }
    isSuspended() { return false; }
    isMSE() { return true; }
    isHls() { return this.hls === true; }
    getSelectedAudioStreamId() { return this.audioId; }
    async selectAudio(id) { this.audioId = id; }
    setSubtitleEnable() {}
    setVolume(value) { this.volume = value; }
    resize() {}
  }
  const context = {
    window: { devicePixelRatio: 1 },
    location: { origin: 'http://localhost:8096' },
    document: { createElement: element },
    AVPlayer: Core,
    EIOSubtitles: class { select() { return true; } clear() {} render() {} destroy() {} },
    ResizeObserver: class { observe() {} disconnect() {} },
    URL, AbortController, DOMException,
    setInterval, clearInterval,
    setTimeout: clock.setTimeout,
    clearTimeout: clock.clearTimeout,
    console,
  };
  context.window = context;
  vm.createContext(context);
  vm.runInContext(fs.readFileSync(
    path.join(__dirname, '../public/web/player-libmedia.js'), 'utf8'), context);
  const player = new context.window.EIOPlayer(element());
  livePlayers.push(player);
  return { player, instances, clock };
}

const source = { Id: 'source', Container: 'mkv', MediaStreams: [
  { Index: 0, Type: 'Video', Codec: 'hevc' },
  { Index: 3, Type: 'Audio', Codec: 'aac' },
] };
const item = { Id: 'item', RunTimeTicks: 3000000000 };

test('seek budgets stay finite, positive, capped, and honor engine configuration',
  { timeout: 3000 }, async () => {
    const { player, instances, clock } = harness();
    await player.load('/stream.mkv', source, item);
    const core = instances[0];
    const cases = [
      { options: undefined, expected: 10000 },
      { options: { timeout: 2500 }, expected: 2500 },
      { options: { timeout: 30000 }, expected: 30000 },
      { options: { timeout: 60000 }, expected: 60000 },
      { options: { timeout: 120000 }, expected: 60000 },
      { options: { timeout: 0 }, expected: 10000 },
      { options: { timeout: -5 }, expected: 10000 },
      { options: { timeout: Infinity }, expected: 10000 },
      { options: { timeout: NaN }, expected: 10000 },
      { options: { timeout: '2500' }, expected: 2500 },
    ];
    for (const testCase of cases) {
      let finish;
      core.seekImpl = () => new Promise(resolve => { finish = resolve; });
      const pending = player.seek(1, testCase.options);
      pending.catch(() => {});
      await flush();
      assert.ok(clock.pendingDelays().includes(testCase.expected),
        'missing ' + testCase.expected + 'ms budget for ' + JSON.stringify(testCase.options));
      await clock.advance(testCase.expected);
      await assert.rejects(pending, error => error.name === 'TimeoutError'
        && error.timeoutMs === testCase.expected);
      finish();
      await flush();
    }
    await player.destroy();

    const configured = harness();
    await configured.player.load('/stream.mkv', source, item, { seekTimeout: 15000 });
    const configuredCore = configured.instances[0];
    for (const testCase of [
      { options: undefined, expected: 15000 },
      { options: { timeout: 2000 }, expected: 2000 },
    ]) {
      let finish;
      configuredCore.seekImpl = () => new Promise(resolve => { finish = resolve; });
      const pending = configured.player.seek(2, testCase.options);
      pending.catch(() => {});
      await flush();
      assert.ok(configured.clock.pendingDelays().includes(testCase.expected));
      await configured.clock.advance(testCase.expected);
      await assert.rejects(pending, error => error.name === 'TimeoutError'
        && error.timeoutMs === testCase.expected);
      finish();
      await flush();
    }
    await configured.player.destroy();
  });

test('a timed-out seek carries safe metadata and cannot write a late position',
  { timeout: 3000 }, async () => {
    const { player, instances, clock } = harness();
    await player.load('/stream.mkv', source, item);
    const core = instances[0];
    let finish;
    core.seekImpl = () => new Promise(resolve => { finish = resolve; });
    core.currentTime = 999000; // the underlying core would land at 999s

    const pending = player.seek(30, { timeout: 20000, label: 'resume' });
    pending.catch(() => {});
    await flush();
    assert.ok(clock.pendingDelays().includes(20000));
    await clock.advance(20000);
    await assert.rejects(pending, error => error.name === 'TimeoutError'
      && error.label === 'resume' && error.timeoutMs === 20000);

    const before = player.positionTicks;
    finish();
    await flush();
    assert.equal(player.positionTicks, before,
      'the late core completion rewrote adapter progress after its deadline');
    assert.equal(player.positionTicks, 0);

    // An arbitrary label is not copied into diagnostics; the budget is clamped.
    let finishSecond;
    core.seekImpl = () => new Promise(resolve => { finishSecond = resolve; });
    const second = player.seek(40, { timeout: 120000, label: '<script>https://secret' });
    second.catch(() => {});
    await flush();
    assert.ok(clock.pendingDelays().includes(60000));
    await clock.advance(60000);
    await assert.rejects(second, error => error.name === 'TimeoutError'
      && error.label === 'seek' && error.timeoutMs === 60000);
    finishSecond();
    await flush();
    assert.equal(player.positionTicks, 0);
    await player.destroy();
  });

test('replacing the engine aborts a pending seek immediately and keeps the new timeline',
  { timeout: 3000 }, async () => {
    const { player, instances, clock } = harness();
    await player.load('/stream.mkv', source, item, { fallbackUrl: '/master.m3u8' });
    const oldCore = instances[0];
    let finish;
    oldCore.seekImpl = () => new Promise(resolve => { finish = resolve; });
    const stale = player.seek(10);
    stale.catch(() => {});
    await flush();
    assert.ok(clock.pendingDelays().includes(10000));

    // No clock advance: replacement must abort instead of waiting out the seek.
    await player._fallback();
    assert.equal(player.core, instances[1]);
    await assert.rejects(stale, error => error.name === 'AbortError');

    oldCore.currentTime = 999000;
    finish();
    await flush();
    assert.equal(player.positionTicks, 0, 'replaced core wrote its late seek');
    assert.deepEqual(instances[1].seekCalls, []);

    instances[1].listeners.time(5000n);
    assert.equal(player.positionTicks, 50000000);
    await player.seek(20);
    assert.deepEqual(instances[1].seekCalls, [20000n]);
    assert.equal(player.positionTicks, 200000000);
    await player.destroy();
  });

test('destroy aborts a pending seek immediately and ignores its late completion',
  { timeout: 3000 }, async () => {
    const { player, instances, clock } = harness();
    await player.load('/stream.mkv', source, item);
    const core = instances[0];
    let finish;
    core.seekImpl = () => new Promise(resolve => { finish = resolve; });
    const pending = player.seek(30);
    pending.catch(() => {});
    await flush();
    assert.ok(clock.pendingDelays().includes(10000));

    await player.destroy();
    await assert.rejects(pending, error => error.name === 'AbortError');
    core.currentTime = 999000;
    finish();
    await flush();
    assert.equal(player.positionTicks, 0);
    assert.equal(player._destroyed, true);
  });
