'use strict';
/* Engine recovery regressions: user intent across fallback, seek generations,
 * track-result honesty and non-blocking subtitle recovery. */
const { test, afterEach } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
const livePlayers = [];
afterEach(async () => {
  while (livePlayers.length) await livePlayers.pop().destroy();
});

async function waitFor(predicate, timeout = 1000) {
  const started = Date.now();
  while (Date.now() - started < timeout) {
    if (predicate()) return;
    await delay(5);
  }
  assert.fail('condition was not reached before timeout');
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
  const states = [];
  const subtitleCalls = [];
  let subtitleSelect = options.subtitleSelect || (() => true);
  // Browsers expose numeric handles, not Node Timeout.unref(). Keeping that
  // distinction also lets deadline assertions settle on Node 22's test runner.
  const timeouts = new Map();
  let nextTimeout = 0;
  function browserSetTimeout(callback, milliseconds, ...args) {
    const id = ++nextTimeout;
    timeouts.set(id, setTimeout(() => {
      timeouts.delete(id);
      callback(...args);
    }, milliseconds));
    return id;
  }
  function browserClearTimeout(id) {
    clearTimeout(timeouts.get(id));
    timeouts.delete(id);
  }
  class Core {
    constructor(config) {
      this.options = config;
      this.listeners = {};
      this.volume = 1;
      this.seekCalls = [];
      this.streams = [];
      instances.push(this);
    }
    on(event, handler) { this.listeners[event] = handler; }
    async load(url) {
      this.url = url;
      if (url.includes('slow')) await new Promise(resolve => { this.finishLoad = resolve; });
      if (options.loadFailure) throw options.loadFailure;
    }
    async play() {
      this.playCalls = (this.playCalls || 0) + 1;
      this.listeners.played?.();
      this.listeners.firstVideoRendered?.();
    }
    async pause() {
      this.pauseCalls = (this.pauseCalls || 0) + 1;
      this.listeners.paused?.();
    }
    async resume() {}
    async seek(timestamp) {
      this.seekCalls.push(timestamp);
      if (this.seekImpl) return this.seekImpl(timestamp);
      this.currentTime = timestamp;
    }
    getEioSharedMediaDomain() { return options.getDomain?.(this) || null; }
    async destroy() { this.destroyed = true; }
    getDuration() { return 300000n; }
    getStreams() { return this.streams; }
    hasVideo() { return true; }
    isSuspended() { return false; }
    isMSE() { return true; }
    isHls() { return this.hls === true; }
    getSelectedAudioStreamId() { return this.audioId; }
    async selectAudio(id) { this.audioId = id; }
    getSelectedSubtitleStreamId() { return this.subtitleId; }
    async selectSubtitle(id) { this.subtitleId = id; }
    setSubtitleEnable(value) { this.subtitleEnabled = value; }
    setVolume(value) { this.volume = value; }
    resize() {}
  }
  class Subtitles {
    constructor(player) { this.player = player; }
    async select(index, selectOptions) {
      subtitleCalls.push({ index, selectOptions });
      return subtitleSelect(index, selectOptions, this.player);
    }
    clear() {}
    render() {}
    destroy() {}
  }
  const context = {
    window: { devicePixelRatio: 1 },
    location: { origin: 'http://localhost:8096' },
    document: { createElement: element },
    AVPlayer: Core,
    EIOSubtitles: Subtitles,
    ResizeObserver: class { observe() {} disconnect() {} },
    URL, AbortController, DOMException,
    setInterval, clearInterval, setTimeout: browserSetTimeout, clearTimeout: browserClearTimeout, console,
  };
  context.window = context;
  vm.createContext(context);
  vm.runInContext(fs.readFileSync(path.join(__dirname, '../public/web/player-libmedia.js'), 'utf8'), context);
  const container = element();
  const player = new context.window.EIOPlayer(container);
  livePlayers.push(player);
  player.onState = state => states.push(state);
  return {
    player, instances, states, subtitleCalls, container, context,
    setSubtitleSelect(fn) { subtitleSelect = fn; },
  };
}

const source = { Id: 'source', Container: 'mkv', MediaStreams: [
  { Index: 0, Type: 'Video', Codec: 'hevc' },
  { Index: 3, Type: 'Audio', Codec: 'aac' },
] };
const item = { Id: 'item', RunTimeTicks: 3000000000 };

test('play and pause during startup wait for the actual loaded engine', { timeout: 3000 }, async () => {
  const { player, instances } = harness();
  const loading = player.load('/slow.mkv', source, item);
  const core = instances[0];
  const playing = player.play();
  const paused = player.pause();
  await delay(0);
  assert.equal(core.playCalls, undefined, 'transport play reached a loading core');
  assert.equal(core.pauseCalls, undefined, 'transport pause reached a loading core');
  core.finishLoad();
  await Promise.all([loading, playing, paused]);
  assert.equal(core.playCalls, 2, 'startup play and queued user play both settled');
  assert.equal(core.pauseCalls, 1);
  assert.equal(player.playing, false);
  assert.equal(player._pauseRequested, true);
});

test('a queued play cannot reach a core destroyed during startup', { timeout: 3000 }, async () => {
  const { player, instances } = harness();
  const loading = player.load('/slow.mkv', source, item);
  const playing = player.play();
  const stoppedLoad = assert.rejects(loading, error => error.name === 'AbortError');
  const stoppedPlay = assert.rejects(playing, error => error.name === 'AbortError');
  await player.destroy();
  await Promise.all([stoppedLoad, stoppedPlay]);
  instances[0].finishLoad();
  await delay(0);
  assert.equal(instances[0].playCalls, undefined);
});

test('failed startup preserves its actual failure for queued and later play controls', { timeout: 3000 }, async () => {
  const failure = Object.assign(new Error('media request failed'), { code: 'fixture-http-500' });
  const { player, instances } = harness({ loadFailure: failure });
  const loading = player.load('/slow.mkv', source, item);
  const playing = player.play();
  const failedLoad = assert.rejects(loading, error => error === failure);
  const failedPlay = assert.rejects(playing, error => error === failure);
  instances[0].finishLoad();
  await Promise.all([failedLoad, failedPlay]);
  await assert.rejects(player.play(), error => error === failure);
  assert.equal(instances[0].playCalls, undefined);
});

test('transport play joins an active seek before resuming its core', { timeout: 3000 }, async () => {
  const { player, instances } = harness();
  await player.load('/stream.mkv', source, item);
  const core = instances[0];
  let finishSeek, resumes = 0;
  core.seekImpl = () => new Promise(resolve => { finishSeek = resolve; });
  core.resume = async () => { resumes++; };
  const seeking = player.seek(60);
  await waitFor(() => finishSeek);
  const playing = player.play();
  await delay(0);
  assert.equal(resumes, 0, 'play crossed the in-flight seek');
  finishSeek();
  await Promise.all([seeking, playing]);
  assert.equal(resumes, 1);
  assert.equal(player.playing, true);
});

test('a seek deadline cannot let play cross a still-pending core seek', { timeout: 3000 }, async () => {
  const { player, instances } = harness();
  await player.load('/stream.mkv', source, item, { seekTimeout: 20 });
  const core = instances[0];
  let finishSeek, resumes = 0;
  core.seekImpl = () => new Promise(resolve => { finishSeek = resolve; });
  core.resume = async () => { resumes++; };
  await assert.rejects(player.seek(60), error => error.name === 'TimeoutError');
  await assert.rejects(player.play(), error => error.name === 'TimeoutError' && error.label === 'play');
  assert.equal(resumes, 0);
  finishSeek();
  await delay(0);
  assert.equal(resumes, 0, 'a timed-out queued play must not execute late');
  await player.play();
  assert.equal(resumes, 1, 'a settled native seek permits a fresh user request');
});

test('runtime fallback preserves an explicitly paused user and volume', { timeout: 3000 }, async () => {
  const { player, instances } = harness();
  await player.load('/stream.mkv', source, item, { fallbackUrl: '/master.m3u8' });
  player.setVolume(0.2);
  await player.pause();
  assert.equal(instances[0].volume, 0.2);
  assert.equal(player.playing, false);

  instances[0].listeners.error(new Error('decode failed'));
  await waitFor(() => instances.length === 2 && player._recovering === false);
  assert.equal(instances[1].volume, 0.2, 'fallback engine reset volume');
  assert.ok(instances[1].pauseCalls > 0, 'fallback resumed a user-paused stream');
  assert.equal(player.playing, false);
  await player.destroy();
});

test('an emitted startup error interrupts a pending load without waiting for the watchdog', { timeout: 3000 }, async () => {
  const { player, instances } = harness();
  const failure = Object.assign(new Error('upstream media HTTP 401'), { status: 401 });
  const loading = player.load('/slow.mkv', source, item, { loadTimeout: 20000 });
  const rejected = assert.rejects(loading, error => error === failure);
  const core = instances[0];
  core.listeners.error(failure);
  await rejected;
  core.finishLoad();
  await delay(0);
  assert.equal(core.playCalls, undefined, 'late load revived an engine after its emitted failure');
  await player.destroy();
});

test('runtime fallback keeps a playing stream playing with the saved volume', { timeout: 3000 }, async () => {
  const { player, instances } = harness();
  await player.load('/stream.mkv', source, item, { fallbackUrl: '/master.m3u8' });
  player.setVolume(0.35);
  assert.equal(player.playing, true);

  instances[0].listeners.error(new Error('decode failed'));
  await waitFor(() => instances.length === 2 && player._recovering === false);
  assert.equal(instances[1].volume, 0.35);
  assert.equal(instances[1].pauseCalls, undefined, 'fallback paused without user intent');
  assert.equal(player.playing, true);
  await player.destroy();
});

test('the first load fallback does not inherit a false pause intent', { timeout: 3000 }, async () => {
  const { player, instances } = harness();
  await player.load('/slow.mkv', source, item, { fallbackUrl: '/master.m3u8', loadTimeout: 5 });
  assert.equal(instances.length, 2);
  assert.equal(instances[1].pauseCalls, undefined);
  assert.equal(player.playing, true);
  await player.destroy();
});

test('a timed-out attempt cannot be revived by its late load continuation', { timeout: 3000 }, async () => {
  const { player, instances } = harness();
  // The second attempt is the successful fallback; the first must stay dead.
  await player.load('/slow.mkv', source, item, { fallbackUrl: '/master.m3u8', loadTimeout: 10 });
  assert.equal(instances.length, 2);
  const generation = player._engineGeneration;
  const replacement = player.core;
  assert.equal(replacement, instances[1]);
  // Let the abandoned core.load() settle after the watchdog moved on.
  instances[0].finishLoad();
  await delay(10);
  assert.equal(player._engineGeneration, generation);
  assert.equal(player.core, replacement);
  assert.equal(instances[0].playCalls, undefined);
  await player.destroy();
});

test('a stale core seek rejects and cannot move the replacement timeline', { timeout: 3000 }, async () => {
  const { player, instances } = harness();
  await player.load('/stream.mkv', source, item, { fallbackUrl: '/master.m3u8' });
  const oldCore = instances[0];
  let finishSeek;
  oldCore.seekImpl = () => new Promise(resolve => { finishSeek = resolve; });
  const stale = player.seek(10);
  await delay(0);
  assert.deepEqual(oldCore.seekCalls, [10000n]);

  await player._fallback();
  assert.equal(player.core, instances[1]);
  assert.equal(player._destroyed, false);
  finishSeek();
  await assert.rejects(stale, error => error.name === 'AbortError');
  assert.equal(player.positionTicks, 0, 'stale seek moved the new timeline');

  instances[1].listeners.time(5000n);
  assert.equal(player.positionTicks, 50000000);
  await player.seek(20);
  assert.deepEqual(instances[1].seekCalls, [20000n]);
  assert.equal(player.positionTicks, 200000000);
  await player.destroy();
});

test('destroy during a pending seek rejects it without an unhandled rejection', { timeout: 3000 }, async () => {
  const { player, instances } = harness();
  await player.load('/stream.mkv', source, item);
  const core = instances[0];
  let finishSeek;
  core.seekImpl = () => new Promise(resolve => { finishSeek = resolve; });
  const pending = player.seek(30);
  await delay(0);
  await player.destroy();
  await assert.rejects(pending, error => error.name === 'AbortError');
  finishSeek();
  await delay(10);
  assert.equal(player.positionTicks, 0);
});

test('libmedia reports a missing audio track instead of assuming selection', { timeout: 3000 }, async () => {
  const { player, instances, states } = harness();
  instances.length = 0;
  await player.load('/stream.mkv', source, item, { audioIndex: 99 });
  assert.ok(states.some(state => typeof state.audioError === 'string' && state.audioError.length));
  assert.equal(player.audioIndex, undefined);
  assert.equal(player.options.audioIndex, 99);
  await player.destroy();
});

test('HLS audio selection returning false is not reported as a client failure', { timeout: 3000 }, async () => {
  const { player, instances, states } = harness();
  const original = instances.push.bind(instances);
  instances.push = function (core) {
    core.hls = true;
    return original(core);
  };
  await player.load('/master.m3u8', source, item, { audioIndex: 3 });
  assert.equal(await player.selectAudio(3), false);
  assert.equal(states.some(state => state.audioError), false);
  assert.equal(player.audioIndex, undefined);
  await player.destroy();
});

test('fallback re-selects subtitles in the background instead of blocking recovery', { timeout: 3000 }, async () => {
  const { player, instances, setSubtitleSelect, states } = harness();
  await player.load('/stream.mkv', source, item, { fallbackUrl: '/master.m3u8' });
  await player.selectSubtitle(2);
  assert.equal(player.subtitleIndex, 2);
  setSubtitleSelect(() => new Promise(() => {}));
  const started = Date.now();
  await player._runtimeError(new Error('decode failed'));
  assert.ok(Date.now() - started < 500, 'recovery waited for subtitle loading');
  assert.equal(player.core, instances[1]);
  assert.ok(states.some(state => state.loading === false));
  assert.ok(player._defaultSubtitle);
  await player.destroy();
});

test('default subtitle loading is asynchronous with an independent timeout', { timeout: 3000 }, async () => {
  const { player, states, setSubtitleSelect } = harness({ subtitleSelect: () => new Promise(() => {}) });
  const loading = player.load('/stream.mkv', source, item, { subtitleIndex: 2, subtitleTimeout: 20 });
  await loading;
  assert.equal(player.playing, true, 'subtitle wait blocked playback');
  await waitFor(() => states.some(state => state.subtitleError === '字幕加载超时'));
  await player.destroy();
});

test('destroy aborts a pending default subtitle without a subtitle error', { timeout: 3000 }, async () => {
  const { player, states } = harness({
    subtitleSelect: (index, options) => new Promise((_, reject) => {
      options.signal.addEventListener('abort', () => reject(new DOMException('cancelled', 'AbortError')));
    }),
  });
  await player.load('/stream.mkv', source, item, { subtitleIndex: 2, subtitleTimeout: 500 });
  await player.destroy();
  await delay(20);
  assert.equal(states.some(state => state.subtitleError), false);
});

test('a hung decoder destroy blocks engine replacement until it releases', { timeout: 3000 }, async () => {
  const { player, instances } = harness();
  await player.load('/stream.mkv', source, item, { fallbackUrl: '/master.m3u8', destroyTimeout: 30 });
  const errors = [];
  player.onError = message => errors.push(message);
  let release;
  instances[0].destroy = () => new Promise(resolve => { release = resolve; });

  instances[0].listeners.error(new Error('decode failed'));
  await waitFor(() => player.isEngineWedged());
  assert.equal(player._recovering, false, 'recovery stayed stuck on the hung teardown');
  assert.equal(instances.length, 1, 'replacement engine was created over a live decoder');
  assert.ok(errors.length >= 1, 'hung teardown did not surface an error');
  assert.equal(player.engineTeardown.released, false);
  assert.equal(player.engineTeardown.wedged, true);
  assert.equal(player.engineTeardown.error.name, 'EngineTeardownError');

  // Sticky gate: retries must keep failing instead of allowing one attempt.
  await assert.rejects(player.load('/stream.mkv', source, item, { destroyTimeout: 30 }),
    error => error.name === 'EngineTeardownError');
  await assert.rejects(player.load('/stream.mkv', source, item, { destroyTimeout: 30 }),
    error => error.name === 'EngineTeardownError');
  assert.equal(instances.length, 1, 'a wedged gate allowed a new engine');

  release();
  await delay(20);
  assert.equal(player.isEngineWedged(), false, 'latch did not clear after real release');
  assert.equal(player.engineTeardown.released, true);
  await player.load('/stream.mkv', source, item);
  assert.equal(instances.length, 2);
  await player.destroy();
});

test('destroy rejects diagnosably when the decoder outlives its deadline', { timeout: 3000 }, async () => {
  const { player, instances } = harness();
  await player.load('/stream.mkv', source, item, { destroyTimeout: 30 });
  let release;
  instances[0].destroy = () => new Promise(resolve => { release = resolve; });

  await assert.rejects(player.destroy(), error => error.name === 'EngineTeardownError'
    && error.wedged === true && error.generation >= 1);
  assert.equal(player._destroyed, true);
  assert.equal(player.isEngineWedged(), true);
  // A second cleanup call must stay diagnosably rejected, not resolve.
  await assert.rejects(player.destroy(), error => error.name === 'EngineTeardownError');
  release();
  await delay(20);
  assert.equal(player.isEngineWedged(), false);
  // The rejected cleanup promise is intentional; keep it out of the automatic
  // afterEach teardown, which always awaits destroy().
  const index = livePlayers.indexOf(player);
  if (index >= 0) livePlayers.splice(index, 1);
});

test('opt-in fallback reuses the ownership descriptor without inventing teardown credit', { timeout: 3000 }, async () => {
  // Descriptor stub tests adapter handoff only, not physical-memory verification.
  const domain = Object.freeze({ domainId: 'modeled-session-domain' });
  const budget = { enabled: true, maxBytes: 300000000, maxForwardSeconds: 120 };
  const { player, instances } = harness({ getDomain: core => core.options.eioSharedMediaDomain || domain });
  await player.load('/stream.mkv', source, item, { fallbackUrl: '/master.m3u8', eioDualCache: budget });
  assert.equal(instances[0].options.eioDualCache.enabled, true);
  assert.notEqual(instances[0].options.eioDualCache, budget, 'caller options are not mutated');
  assert.equal(instances[0].options.eioSharedMediaDomain, undefined);
  instances[0].listeners.error(new Error('modeled rebuild'));
  await waitFor(() => instances.length === 2 && player._recovering === false);
  assert.equal(instances[0].destroyed, true);
  assert.equal(instances[1].options.eioSharedMediaDomain, domain);
  assert.equal(player._eioOwnershipDomain, domain);
  await player.destroy();
  assert.equal(player._eioOwnershipDomain, domain, 'destroy completion never resets shared ownership');
});
