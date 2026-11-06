'use strict';
/* Playback-rate app integration through the shared production harness.
 * The fake engine follows the real contract the app relies on: a synchronous
 * playbackRate getter, async setPlaybackRate returning the actual rate, and
 * load() reporting playing through onState. Deferred setters let the tests
 * observe ratePending and stale completions without inventing old test APIs.
 */
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { harness, makeStorage } = require('./web-ui-harness.cjs');
const { playbackInfo: strictPlaybackInfo, isSessionEvent } = require('./web-playback-fixture.cjs');
const TICKS = 10000000;

function mediaSource(overrides = {}) {
  return {
    Id: 'ms1', Name: 'v1', Container: 'mp4',
    DirectStreamUrl: '/Videos/e1/stream.mp4?Static=true&MediaSourceId=ms1',
    MediaStreams: [
      { Type: 'Audio', Index: 1, DisplayTitle: 'AAC', IsDefault: true },
      { Type: 'Subtitle', Index: 2, DisplayTitle: '中文', IsDefault: true },
    ],
    ...overrides,
  };
}

function episode(id, extra = {}) {
  return { Id: id, Name: id, Type: 'Episode', RunTimeTicks: 3600 * TICKS,
    MediaSources: [mediaSource()], ...extra };
}

function makePlayerClass(config = {}) {
  const instances = [];
  class FakePlayer {
    constructor() {
      this.mediaSource = null; this.item = null; this.duration = 0;
      this.positionTicks = 0; this.playing = false; this.volume = 1;
      this.subtitleIndex = -1; this._rate = 1;
      this.loadCalls = []; this.seekCalls = []; this.audioCalls = [];
      this.subtitleCalls = []; this.setCalls = [];
      instances.push(this);
    }
    get playbackRate() { return this._rate; }
    async setPlaybackRate(rate) {
      this.setCalls.push(rate);
      let actual = rate;
      if (config.setPlaybackRate) actual = await config.setPlaybackRate(rate, this);
      if (actual === undefined) actual = rate;
      this._rate = actual;
      return actual;
    }
    async load(url, mediaSource, item, opts = {}) {
      this.loadCalls.push({ url, mediaSource, item, opts });
      this.mediaSource = mediaSource; this.item = item; this.opts = opts;
      this.duration = (item?.RunTimeTicks || mediaSource?.RunTimeTicks || 0) / TICKS;
      this.playing = true;
      this.onState?.({ mode: 'mse', playing: true, loading: false,
        time: this.positionTicks / TICKS, duration: this.duration, playbackRate: this.playbackRate });
    }
    async seek(seconds) {
      this.seekCalls.push(seconds); this.positionTicks = Math.round(seconds * TICKS);
      this.onState?.({ mode: 'mse', playing: this.playing, loading: false,
        time: seconds, duration: this.duration, playbackRate: this.playbackRate });
    }
    async play() { this.playing = true; this.onState?.({ mode: 'mse', playing: true, loading: false, time: this.positionTicks / TICKS, duration: this.duration }); }
    async pause() { this.playing = false; this.onState?.({ mode: 'mse', playing: false, loading: false, time: this.positionTicks / TICKS, duration: this.duration }); }
    setVolume(value) { this.volume = value; }
    async selectAudio(index) {
      this.audioCalls.push(index);
      return config.selectAudio ? config.selectAudio(index, this) : true;
    }
    async selectSubtitle(index) {
      this.subtitleCalls.push(index);
      return config.selectSubtitle ? config.selectSubtitle(index, this) : true;
    }
    destroy() { this.destroyed = true; return config.destroy ? config.destroy(this) : Promise.resolve(); }
  }
  return { FakePlayer, instances };
}

function makeReply(options = {}) {
  const bodies = [];
  const reply = (url, opts) => {
    let body = null;
    if (typeof opts?.body === 'string') { try { body = JSON.parse(opts.body); } catch (e) {} }
    bodies.push({ url, body });
    const p = url.pathname;
    if (/\/PlaybackInfo$/.test(p)) {
      return typeof options.playbackInfo === 'function'
        ? options.playbackInfo(body)
        : strictPlaybackInfo(options.sources || [mediaSource()], 'sess');
    }
    if (/\/Episodes$/.test(p) || /\/NextUp$/.test(p)) return { Items: [] };
    const detail = p.match(/\/Items\/([^/]+)$/);
    if (detail) return options.item || episode(decodeURIComponent(detail[1]));
    if (isSessionEvent(p)) return {};
    if (p.startsWith('/Users/')) return { Policy: {} };
    return { Items: [] };
  };
  return { reply, bodies };
}

function setup(options = {}) {
  const table = makeReply(options);
  const player = makePlayerClass(options.player || {});
  const env = harness({
    storage: makeStorage({ eio_token: 'token', eio_user_id: 'user' }),
    reply: table.reply,
    beforeApp: context => { context.EIOPlayer = player.FakePlayer; options.beforeApp?.(context); },
  });
  env.player = player; env.reply = table;
  env.app.page.value = 'app'; env.app.navPage.value = 'player';
  env.context.window.location.hash = options.hash || '#/play/e1';
  // The shared VM stub's reactive() is identity, so it does not unwrap the
  // computed like real Vue does and playerUI.busy stays a truthy ref object.
  // Bridge only that getter here: production code and the global harness stay
  // untouched, and busy is still derived from status/operation rather than
  // being forced to false to bypass the guard.
  const busy = env.app.playerUI.busy;
  Object.defineProperty(env.app.playerUI, 'busy', {
    configurable: true,
    get: () => busy.value,
  });
  return env;
}

function cleanup(env) {
  const { app } = env;
  try { app.exitPlayer(); } catch (e) {}
  try { app.playerUI.dispose(); } catch (e) {}
  try { app.hero.dispose(); } catch (e) {}
  try { app.home.dispose(); } catch (e) {}
  try { app.rateUI.dispose(); } catch (e) {}
}

async function run(options, body) {
  const env = setup(options);
  try { return await body(env); } finally { cleanup(env); }
}

test('R1 a new playback session starts at the default 1x', async () => {
  await run({}, async ({ app, player }) => {
    await app.playItem(episode('e1'));
    assert.equal(player.instances.length, 1);
    assert.equal(app.playbackRate.value, 1);
    assert.equal(player.instances[0].playbackRate, 1);
    assert.equal(app.ratePending.value, false);
  });
});

test('R2 each of the five rates applies and ratePending brackets the setter', async () => {
  let release = null;
  await run({ player: { setPlaybackRate: () => new Promise(resolve => { release = resolve; }) } },
    async ({ app, player, context }) => {
      assert.deepEqual(Array.from(context.EIOPlaybackRateUI.RATES), [0.5, 1, 1.25, 1.5, 2]);
      await app.playItem(episode('e1'));
      for (const rate of context.EIOPlaybackRateUI.RATES) {
        const prev = app.playbackRate.value;
        const pending = app.selectPlaybackRate(rate);
        assert.equal(app.ratePending.value, true, `pending for ${rate}`);
        assert.equal(app.playbackRate.value, prev, `pending keeps the confirmed rate for ${rate}`);
        release();
        assert.equal(await pending, true);
        assert.equal(app.ratePending.value, false);
        assert.equal(app.rateError.value, '');
        assert.equal(app.playbackRate.value, rate);
        assert.equal(player.instances[0].playbackRate, rate);
      }
    });
});

test('R3 switchVersion restores the confirmed rate and seeks the plain position', async () => {
  const sources = [mediaSource(), mediaSource({ Id: 'ms2', Name: 'v2' })];
  const item = episode('e1', { MediaSources: sources });
  await run({ sources }, async ({ app, player }) => {
    await app.playItem(item);
    assert.equal(await app.selectPlaybackRate(1.5), true);
    assert.equal(app.playerUI.busy, false);
    const old = player.instances[0]; old.positionTicks = 123 * TICKS;
    await app.switchVersion('ms2');
    assert.equal(player.instances.length, 2);
    const next = player.instances[1];
    assert.equal(next.mediaSource.Id, 'ms2');
    assert.equal(next.playbackRate, 1.5);
    assert.equal(app.playbackRate.value, 1.5);
    assert.deepEqual(next.seekCalls, [123]);
    assert.equal(next.positionTicks, 123 * TICKS);
    assert.equal(app.wasmTime.value, 123);
  });
});

test('R3b a busy playerUI blocks version switching', async () => {
  const sources = [mediaSource(), mediaSource({ Id: 'ms2', Name: 'v2' })];
  const item = episode('e1', { MediaSources: sources });
  await run({ sources }, async ({ app, player }) => {
    await app.playItem(item);
    app.playerUI.begin('version');
    assert.equal(app.playerUI.busy, true);
    await app.switchVersion('ms2');
    assert.equal(player.instances.length, 1);
    assert.equal(app.playMediaSourceId.value, 'ms1');
    assert.equal(app.playerUI.busy, true);
  });
});

test('R4 the same item played again is a new session at 1x', async () => {
  await run({}, async ({ app, player }) => {
    await app.playItem(episode('e1'));
    assert.equal(await app.selectPlaybackRate(2), true);
    assert.equal(app.playbackRate.value, 2);
    await app.playItem(episode('e1'));
    assert.equal(player.instances.length, 2);
    assert.equal(app.playbackRate.value, 1);
    assert.equal(player.instances[1].playbackRate, 1);
  });
});

test('R5 an audio-track fallback keeps the confirmed rate and position', async () => {
  const source = mediaSource({ MediaStreams: [
    { Type: 'Audio', Index: 1, DisplayTitle: 'AAC', IsDefault: true },
    { Type: 'Audio', Index: 2, DisplayTitle: 'AC3' },
    { Type: 'Subtitle', Index: 3, DisplayTitle: '中文', IsDefault: true },
  ] });
  const item = episode('e1', { MediaSources: [source] });
  await run({ sources: [source], player: { selectAudio: () => false } },
    async ({ app, player }) => {
      await app.playItem(item);
      assert.equal(await app.selectPlaybackRate(1.25), true);
      const old = player.instances[0]; old.positionTicks = 42 * TICKS;
      await app.selectAudio(2);
      assert.deepEqual(old.audioCalls, [2]);
      assert.equal(player.instances.length, 2);
      const next = player.instances[1];
      assert.equal(next.loadCalls[0].opts.audioIndex, 2);
      assert.equal(app.playAudioIndex.value, 2);
      assert.equal(next.playbackRate, 1.25);
      assert.equal(app.playbackRate.value, 1.25);
      assert.deepEqual(next.seekCalls, [42]);
    });
});

test('R6 an unsupported setter keeps playing, then the next source starts at 1x', async () => {
  const playerConfig = { setPlaybackRate: rate => {
    if (rate === 2) throw new Error('engine does not support 2x');
    return rate;
  } };
  await run({ player: playerConfig }, async ({ app, player, context }) => {
    await app.playItem(episode('e1'));
    const old = player.instances[0];
    assert.equal(app.wasmPlaying.value, true);
    assert.equal(await app.selectPlaybackRate(2), false);
    assert.equal(app.ratePending.value, false);
    assert.equal(app.rateError.value, '设置播放速度失败，请稍后重试');
    assert.equal(app.playbackRate.value, 1);
    assert.equal(old.playing, true);
    assert.equal(app.wasmPlaying.value, true);
    assert.equal(app.playerUI.status.value, 'playing');
    context.window.location.hash = '#/play/e2';
    await app.playItem(episode('e2'));
    assert.equal(player.instances.length, 2);
    assert.equal(app.playbackRate.value, 1);
    assert.equal(app.rateError.value, '');
    assert.equal(player.instances[1].playbackRate, 1);
  });
});

test('R7 a stale pending resolve cannot write a new rate or close its menu', async () => {
  let release = null;
  await run({ player: { setPlaybackRate: () => new Promise(resolve => { release = resolve; }) } },
    async ({ app, player, context }) => {
      await app.playItem(episode('e1'));
      const stale = app.selectPlaybackRate(2);
      assert.equal(app.ratePending.value, true);
      app.exitPlayer();
      context.window.location.hash = '#/play/e1';
      await app.playItem(episode('e1'));
      assert.equal(player.instances.length, 2);
      assert.equal(app.playbackRate.value, 1);
      app.playerUI.toggleMenu('rate');
      assert.equal(app.playerUI.isMenuOpen('rate'), true);
      release();
      assert.equal(await stale, false);
      assert.equal(app.playbackRate.value, 1);
      assert.equal(app.ratePending.value, false);
      assert.equal(app.playerUI.isMenuOpen('rate'), true);
      assert.equal(player.instances[0].playbackRate, 2);
    });
});

test('R8 a stale pending reject is silent and does not close the new menu', async () => {
  let rejectOld = null;
  await run({ player: { setPlaybackRate: () => new Promise((resolve, reject) => { rejectOld = reject; }) } },
    async ({ app, player, context }) => {
      await app.playItem(episode('e1'));
      const stale = app.selectPlaybackRate(2);
      assert.equal(app.ratePending.value, true);
      app.exitPlayer();
      context.window.location.hash = '#/play/e1';
      await app.playItem(episode('e1'));
      app.playerUI.toggleMenu('rate');
      rejectOld(new Error('late setter failure'));
      assert.equal(await stale, false);
      assert.equal(app.rateError.value, '');
      assert.equal(app.playbackRate.value, 1);
      assert.equal(app.ratePending.value, false);
      assert.equal(app.playerUI.isMenuOpen('rate'), true);
      assert.equal(player.instances[1].playbackRate, 1);
    });
});

test('H1 the production template exposes five literal rates in load order', () => {
  const html = fs.readFileSync(path.join(__dirname, '..', 'public', 'web', 'web.html'), 'utf8');
  assert.ok(html.indexOf('/web/web/player-rate-ui.js') < html.indexOf('/web/web/app.js'));
  assert.ok(html.indexOf('/web/web/player-ui.js') < html.indexOf('/web/web/player-rate-ui.js'));
  assert.match(html, /v-for="rate in \[0\.5, 1, 1\.25, 1\.5, 2\]"/);
  assert.match(html, /@click="selectPlaybackRate\(rate\)"/);
  // A rate is only marked selected once it is confirmed: a failed or
  // in-flight set must not advertise itself as the current rate.
  assert.match(html, /:aria-pressed="!rateError && !ratePending && rate === playbackRate \? 'true' : 'false'"/);
  assert.match(html, /v-if="!rateError && !ratePending && rate === playbackRate">✓/);
  // The trigger keeps the confirmed rate visible and reports pending/failed
  // states in its own label instead of silently showing a new rate.
  assert.match(html, /ratePending \? '设置中…' : rateError \? '倍速未确认' : \('倍速 ' \+ playbackRate \+ '×'\)/);
  assert.match(html, /:aria-expanded="playerUI\.isMenuOpen\('rate'\) \? 'true' : 'false'"/);
});
