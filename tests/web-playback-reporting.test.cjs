'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { harness } = require('./web-ui-harness.cjs');
const { playbackInfo, isSessionEvent } = require('./web-playback-fixture.cjs');
const WEB = path.join(__dirname, '../public/web');
const TICKS = 10000000;

// Real application + EIOPlayer adapter, with an isolated engine at the browser
// boundary. No real server, media, credentials, or upstream identity is used.
function setup() {
  const events = [], negotiations = [], players = [], cores = [];
  const source = { Id: 'ms1', Container: 'mkv', RunTimeTicks: 120 * TICKS,
    DirectStreamUrl: '/Videos/item/stream.mkv', TranscodingUrl: '/Videos/item/master.m3u8',
    MediaStreams: [{ Type: 'Video', Index: 0, Codec: 'h264' },
      { Type: 'Audio', Index: 1, Codec: 'aac', IsDefault: true },
      { Type: 'Audio', Index: 2, Codec: 'aac' }, { Type: 'Subtitle', Index: 3, Codec: 'vtt' }] };
  const item = { Id: 'item', Name: 'fixture', Type: 'Movie', RunTimeTicks: 120 * TICKS, MediaSources: [source] };
  const env = harness({
    reply(url, opts) {
      const body = opts?.body ? JSON.parse(opts.body) : null;
      if (url.pathname.endsWith('/PlaybackInfo')) { negotiations.push(body); return playbackInfo(source); }
      if (isSessionEvent(url.pathname)) events.push({ path: url.pathname, body });
      if (/\/Items\/item$/.test(url.pathname)) return item;
      return { Items: [] };
    },
    beforeApp(ctx) {
      ctx.document.createElement = () => ({ append() {}, prepend() {}, remove() {}, replaceChildren() {} });
      ctx.ResizeObserver = class { observe() {} disconnect() {} };
      ctx.EIOSubtitles = class {
        async select(index) { return index !== 99; }
        render() {} clear() {} destroy() {}
      };
      ctx.AVPlayer = class {
        constructor() { this.listeners = {}; this.rate = 1; this.volume = 1; this.audio = 10; cores.push(this); }
        on(k, fn) { this.listeners[k] = fn; }
        async load(url) { this.url = url; }
        async play() { this.listeners.played?.(); }
        async resume() {}
        async pause() { this.listeners.paused?.(); }
        async seek(ms) { this.currentTime = ms; this.listeners.time?.(ms); }
        async destroy() {}
        isMSE() { return true; } isHls() { return this.url.includes('m3u8'); } isLive() { return false; }
        hasVideo() { return true; } getDuration() { return 120000n; }
        getStreams() { return [{ id: 10, index: 1, mediaType: 'Audio' }, { id: 20, index: 2, mediaType: 'Audio' }]; }
        getSelectedAudioStreamId() { return this.audio; }
        async selectAudio(id) { this.audio = id; }
        setVolume(v) { this.volume = v; } getVolume() { return this.volume; }
        setPlaybackRate(v) { this.rate = v; } getPlaybackRate() { return this.rate; }
        setSubtitleEnable() {} resize() {}
      };
      vm.runInContext(fs.readFileSync(path.join(WEB, 'player-libmedia.js'), 'utf8'), ctx);
      const RealPlayer = ctx.EIOPlayer;
      ctx.EIOPlayer = class extends RealPlayer { constructor(...args) { super(...args); players.push(this); } };
    },
  });
  env.app.playerContainer.value = { prepend() {}, addEventListener() {}, removeEventListener() {}, contains() { return true; } };
  env.app.page.value = 'app'; env.app.navPage.value = 'player';
  env.context.location.hash = '#/play/item';
  const busy = env.app.playerUI.busy;
  Object.defineProperty(env.app.playerUI, 'busy', { get: () => busy.value });
  return { ...env, item, source, players, cores, events, negotiations };
}
const flush = () => new Promise(resolve => setImmediate(resolve));
function close(env) {
  env.app.exitPlayer(); env.app.playerUI.dispose(); env.app.hero.dispose();
  env.app.home.dispose(); env.app.rateUI.dispose();
}

test('real Web session reports confirmed state changes and keeps Stopped within its own DTO', async () => {
  const e = setup();
  try {
    await e.app.playItem(e.item);
    assert.ok(e.events.length, JSON.stringify({status: e.app.playerUI.status, error: e.app.playerUI.error, toasts: e.app.toasts, players: e.players.length, negotiations: e.negotiations.length}));
    const p = e.players[0];
    const start = e.events[0];
    assert.ok(start.path.endsWith('/Sessions/Playing'), 'no Progress before Playing');
    assert.deepEqual(Object.fromEntries(['PlayMethod', 'CanSeek', 'PlaybackRate', 'IsMuted', 'VolumeLevel', 'AudioStreamIndex', 'SubtitleStreamIndex']
      .map(k => [k, start.body[k]])), { PlayMethod: 'DirectStream', CanSeek: true, PlaybackRate: 1,
      IsMuted: false, VolumeLevel: 100, AudioStreamIndex: 1, SubtitleStreamIndex: -1 });
    await p.pause(); await flush();
    assert.equal(e.events.at(-1).body.EventName, 'Pause');
    assert.equal(e.events.at(-1).body.IsPaused, true);
    const before = e.events.length; p._emitState(); await flush();
    assert.equal(e.events.length, before, 'duplicate state notifications do not send requests');
    await p.play(); await flush();
    assert.equal(e.events.at(-1).body.EventName, 'Unpause');
    assert.equal(e.events.at(-1).body.IsPaused, false);
    await e.app.selectPlaybackRate(1.5); await flush();
    assert.equal(e.events.at(-1).body.EventName, 'PlaybackRateChange');
    assert.equal(e.events.at(-1).body.PlaybackRate, 1.5);
    p.setVolume(0); await flush();
    assert.equal(e.events.at(-1).body.EventName, 'VolumeChange');
    assert.equal(e.events.at(-1).body.IsMuted, true);
    assert.equal(e.events.at(-1).body.VolumeLevel, 0);
    await p.selectAudio(2); await flush();
    assert.equal(e.events.at(-1).body.EventName, 'AudioTrackChange');
    assert.equal(e.events.at(-1).body.AudioStreamIndex, 2);
    await p.selectSubtitle(3); await flush();
    assert.equal(e.events.at(-1).body.EventName, 'SubtitleTrackChange');
    assert.equal(e.events.at(-1).body.SubtitleStreamIndex, 3);
    const afterTrack = e.events.length;
    assert.equal(await p.selectSubtitle(99), false); await flush();
    assert.equal(e.events.length, afterTrack, 'failed selection does not claim another track');
    assert.equal(e.context.EIOPlaybackReporting.payload(p, 'Progress').SubtitleStreamIndex, undefined, 'a failed selection leaves no confirmed renderer mapping');
    e.cores[0].listeners.time(15500n); p.onProgress(p.positionTicks); await flush();
    assert.equal(e.events.at(-1).body.PositionTicks, 155000000);
    assert.equal(e.events.at(-1).body.EventName, 'TimeUpdate');
    e.app.exitPlayer(); await flush();
    const stop = e.events.at(-1);
    assert.ok(stop.path.endsWith('/Stopped'));
    assert.deepEqual(Object.keys(stop.body).sort(), ['EioAttemptId','ItemId','MediaSourceId','PlaySessionId','PositionTicks'].sort());
    assert.equal(stop.body.PositionTicks, 155000000);
    assert.equal(stop.body.EioAttemptId, start.body.EioAttemptId);
    const terminal = e.events.length; p.onProgress(0); p._emitState(); await flush();
    assert.equal(e.events.length, terminal, 'destroyed player does not report into a terminal attempt');
  } finally { close(e); }
});

test('compatibility and runtime fallback report Transcode without inventing output-to-source track indexes', async () => {
  const e = setup();
  try {
    await e.app.playItem(e.item, 'ms1', { forceTranscode: true });
    assert.ok(e.events.length, JSON.stringify({status: e.app.playerUI.status, error: e.app.playerUI.error, players: e.players.length, negotiations: e.negotiations.length}));
    assert.equal(e.events[0].body.PlayMethod, 'Transcode');
    assert.equal(e.events[0].body.AudioStreamIndex, undefined);
    assert.equal(e.negotiations[0].MaxWidth, undefined);
    assert.equal(e.negotiations[0].MaxHeight, undefined);
    assert.equal(e.negotiations[0].DeviceProfile.MaxStaticBitrate, undefined);
    assert.ok(e.negotiations[0].DeviceProfile.TranscodingProfiles.every(p => p.MaxWidth === 1920 && p.MaxHeight === 1080));
    e.app.exitPlayer(); await flush();
    e.context.location.hash = '#/play/item';
    await e.app.playItem(e.item);
    const p = e.players.at(-1);
    assert.equal(p.playbackState.PlayMethod, 'DirectStream');
    await p._runtimeError(new Error('synthetic decoder failure')); await flush();
    assert.equal(p.playbackState.PlayMethod, 'Transcode');
    assert.ok(e.events.some(x => x.body.EventName === 'QualityChange' && x.body.PlayMethod === 'Transcode'));
  } finally { close(e); }
});
