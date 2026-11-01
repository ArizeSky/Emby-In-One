'use strict';

// Playback-rate coverage for engine rebuilds: default 1, rate carried into a
// freshly built libmedia core, fallback rebuild, explicit refusal without the
// engine API, post-destroy refusal and the native element metadata path.
// Independent harness only; the existing seek/cue suites are untouched.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const PLAYER_SOURCE = path.join(__dirname, '../public/web/player-libmedia.js');

function domElement() {
  return {
    children: [], clientWidth: 640, clientHeight: 360,
    append(child) { this.children.push(child); },
    prepend(child) { this.children.unshift(child); },
    replaceChildren() { this.children = []; },
    remove() { this.removed = true; },
  };
}

// Media element simulation driven by microtasks: the native adapter must be
// exercised through real element events, never by guessing an API.
function mediaElement() {
  const listeners = {};
  let src = '';
  const element = {
    children: [], playbackRate: 1, currentTime: 0, duration: 300, volume: 1,
    controls: false, playsInline: false, className: '',
    addEventListener(type, handler) { (listeners[type] || (listeners[type] = [])).push(handler); },
    removeEventListener(type, handler) {
      listeners[type] = (listeners[type] || []).filter(entry => entry !== handler);
    },
    emit(type) { for (const handler of listeners[type] || []) handler({ type, target: element }); },
    removeAttribute(name) { if (name === 'src') src = ''; },
    load() {},
    remove() { element.removed = true; },
    pause() { element.emit('pause'); },
    async play() { element.playCalls = (element.playCalls || 0) + 1; element.emit('play'); },
  };
  Object.defineProperty(element, 'src', {
    get() { return src; },
    set(value) {
      src = value;
      queueMicrotask(() => { if (src === value) element.emit('loadedmetadata'); });
    },
  });
  return element;
}

function coreClass(instances) {
  class Core {
    constructor(options) {
      this.options = options;
      this.listeners = {};
      this._playbackRate = 1;
      instances.push(this);
      // Real synchronous rate API. Setting the class flag keeps the legacy
      // shape where an old core simply has no such methods at all.
      if (Core.supportsRateApi) {
        this.setPlaybackRate = rate => { this._playbackRate = rate; };
        this.getPlaybackRate = () => this._playbackRate;
      }
    }
    on(event, handler) { this.listeners[event] = handler; }
    async load(url) {
      this.url = url;
      if (url.includes('slow')) await new Promise(resolve => { this.finishLoad = resolve; });
    }
    async play() {
      this.playCalls = (this.playCalls || 0) + 1;
      this.listeners.played?.();
      this.listeners.time?.(1500n);
      this.listeners.firstVideoRendered?.();
    }
    async pause() { this.listeners.paused?.(); }
    async resume() {}
    async seek(timestamp) { this.seekTimestamp = timestamp; }
    async destroy() { this.destroyed = true; }
    getDuration() { return 300000n; }
    getStreams() { return []; }
    hasVideo() { return true; }
    isLive() { return false; }
    isSuspended() { return false; }
    isMSE() { return true; }
    isHls() { return this.url?.includes('m3u8'); }
    getSelectedAudioStreamId() { return this.audioId; }
    async selectAudio(id) { this.audioId = id; }
    setSubtitleEnable() {}
    setVolume(volume) { this.volume = volume; }
    resize() {}
  }
  Core.supportsRateApi = true;
  return Core;
}

function harness(t) {
  const instances = [];
  const Core = coreClass(instances);
  const context = {
    window: {}, location: { origin: 'http://localhost:8096' },
    document: { createElement: tag => (tag === 'video' ? mediaElement() : domElement()) },
    AVPlayer: Core,
    EIOSubtitles: class { select() {} clear() {} destroy() {} render() {} },
    ResizeObserver: class { observe() {} disconnect() {} },
    URL, AbortController, DOMException, setInterval, clearInterval, setTimeout, clearTimeout, console,
  };
  vm.runInNewContext(fs.readFileSync(PLAYER_SOURCE, 'utf8'), context);
  const container = domElement();
  const player = new context.window.EIOPlayer(container);
  const states = [];
  player.onState = state => states.push(state);
  // Always release deferred loads and every engine so no interval or pending
  // promise outlives the test. Superseded cores are not owned by the player's
  // latest engine, so they are released here as well.
  t.after(async () => {
    for (const core of instances) core.finishLoad?.();
    await player.destroy().catch(() => {});
    for (const core of instances) {
      if (!core.destroyed) await core.destroy().catch(() => {});
    }
    await new Promise(resolve => setImmediate(resolve));
  });
  return { player, instances, states, container, Core };
}

const libmediaSource = { Container: 'mkv', MediaStreams: [{ Type: 'Video', Codec: 'hevc' }] };
const nativeSource = {
  Container: 'mp4',
  MediaStreams: [
    { Type: 'Video', Codec: 'h264' },
    { Type: 'Audio', Codec: 'aac', Index: 1, IsDefault: true },
  ],
};
const item = { Id: 'item', RunTimeTicks: 3000000000 };

test('default rate is 1 and a legacy core without the rate API still loads', async t => {
  const { player, instances, states, Core } = harness(t);
  Core.supportsRateApi = false;
  await player.load('/legacy.mkv', libmediaSource, item);
  const core = instances[0];
  assert.equal(typeof core.setPlaybackRate, 'undefined');
  assert.equal(typeof core.getPlaybackRate, 'undefined');
  assert.equal(player.playbackRate, 1);
  assert.equal(states.at(-1).playbackRate, 1);
  await assert.rejects(player.setPlaybackRate(1.5), /当前引擎不支持切换播放速度/);
  assert.equal(player.playbackRate, 1);
});

test('reloading a source on the same instance rebuilds the core at the applied rate', async t => {
  const { player, instances, states } = harness(t);
  await player.load('/one.mkv', libmediaSource, item);
  assert.equal(instances[0].getPlaybackRate(), 1);
  assert.equal(await player.setPlaybackRate(1.5), 1.5);
  assert.equal(instances[0].getPlaybackRate(), 1.5);
  assert.equal(states.at(-1).playbackRate, 1.5);

  await player.load('/two.mkv', libmediaSource, item);
  assert.equal(instances.length, 2);
  assert.notEqual(instances[1], instances[0]);
  assert.equal(player.core, instances[1]);
  // The actual rebuilt engine was set, not merely the reported player field.
  assert.equal(instances[1].getPlaybackRate(), 1.5);
  assert.equal(player.playbackRate, 1.5);
  assert.equal(states.at(-1).playbackRate, 1.5);
  // A stale engine event may not overwrite the rebuilt timeline.
  const rebuiltPosition = player.positionTicks;
  instances[0].listeners.time?.(90000n);
  assert.equal(player.positionTicks, rebuiltPosition);
});

test('fallback rebuild keeps the rate and a late old load cannot cross-write', async t => {
  const { player, instances, states } = harness(t);
  await player.load('/first.mkv', libmediaSource, item);
  assert.equal(await player.setPlaybackRate(1.5), 1.5);
  assert.equal(instances[0].getPlaybackRate(), 1.5);

  await player.load('/slow.mkv', libmediaSource, item, { fallbackUrl: '/fallback.m3u8', loadTimeout: 5 });
  assert.equal(instances.length, 3);
  const slowCore = instances[1];
  assert.equal(slowCore.destroyed, true);
  assert.match(slowCore.url, /slow\.mkv$/);
  const rebuilt = instances[2];
  assert.equal(player.core, rebuilt);
  assert.match(rebuilt.url, /fallback\.m3u8$/);
  assert.equal(rebuilt.getPlaybackRate(), 1.5);
  assert.equal(player.playbackRate, 1.5);
  assert.equal(states.at(-1).playbackRate, 1.5);

  // The timed-out load may still settle; it must not resume the old decoder
  // or rewrite the rebuilt engine's rate/position.
  slowCore.finishLoad();
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(slowCore.playCalls, undefined);
  assert.equal(player.core, rebuilt);
  assert.equal(player.playbackRate, 1.5);
  assert.equal(rebuilt.getPlaybackRate(), 1.5);
});

test('a rate-carrying rebuild with a core lacking the API rejects instead of faking success', async t => {
  const { player, instances, states, Core } = harness(t);
  await player.load('/one.mkv', libmediaSource, item);
  assert.equal(await player.setPlaybackRate(1.5), 1.5);
  Core.supportsRateApi = false;

  await assert.rejects(player.load('/two.mkv', libmediaSource, item), /当前引擎不支持切换播放速度/);
  assert.equal(instances.length, 2);
  assert.equal(typeof instances[1].setPlaybackRate, 'undefined');
  // The new engine never claimed loaded state and the old rate was not faked.
  const lastLoading = states.map(state => state.loading).lastIndexOf(true);
  assert.ok(lastLoading >= 0);
  assert.equal(states.slice(lastLoading + 1).some(state => state.loading === false), false);
  assert.equal(player.playbackRate, 1.5);
});

test('destroyed players refuse rate changes', async t => {
  const { player, instances } = harness(t);
  await player.load('/one.mkv', libmediaSource, item);
  assert.equal(await player.setPlaybackRate(2), 2);
  await player.destroy();
  assert.equal(instances[0].destroyed, true);
  await assert.rejects(player.setPlaybackRate(1), error => error.name === 'AbortError');
  assert.equal(player.playbackRate, 2);
});

test('native metadata reload reapplies the rate on the current element', async t => {
  const { player, instances, states } = harness(t);
  await player.load('/movie.mp4', nativeSource, item);
  assert.equal(player.mode, 'native');
  assert.equal(instances.length, 0);
  const first = player.video;
  assert.equal(first.playbackRate, 1);
  assert.equal(first.playCalls, 1);

  assert.equal(await player.setPlaybackRate(1.5), 1.5);
  assert.equal(first.playbackRate, 1.5);
  assert.equal(states.at(-1).playbackRate, 1.5);

  await player.load('/movie-2.mp4', nativeSource, item);
  assert.notEqual(player.video, first);
  // Read the rate from the element the player actually owns now.
  assert.equal(player.video.playbackRate, 1.5);
  assert.equal(player.playbackRate, 1.5);
  assert.equal(states.at(-1).playbackRate, 1.5);
});
