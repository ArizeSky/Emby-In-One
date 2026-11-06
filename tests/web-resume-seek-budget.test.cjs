'use strict';
/* Startup-resume budget at the app boundary.
 *
 * The real app.js is driven through the shared browser harness with a recording
 * engine. Startup resume must use its own 30s budget, an interactive seek must
 * stay on the adapter default, a timeout still fails/destroys, and the timeout
 * log may contain only the static marker plus safe numbers.
 */
const { test } = require('node:test');
const assert = require('node:assert/strict');
const { harness, makeStorage, delay } = require('./web-ui-harness.cjs');
const { playbackInfo: strictPlaybackInfo, isSessionEvent } = require('./web-playback-fixture.cjs');

function makePlayerClass(config = {}) {
  const instances = [];
  const destroyed = [];
  class FakePlayer {
    constructor() {
      this.sessionId = 'ps-' + (instances.length + 1);
      this.mediaSource = null;
      this.positionTicks = 0;
      this.duration = 3600;
      this.playing = false;
      this.subtitleIndex = -1;
      this.volume = 1;
      this.seekCalls = [];
      this.destroyCalls = 0;
      instances.push(this);
    }
    async load(url, mediaSource, item, opts = {}) {
      this.loadCalls = (this.loadCalls || 0) + 1;
      this.mediaSource = mediaSource;
      this.playing = true;
      this.onState?.({ playing: true, loading: false, time: 0, duration: this.duration, mode: 'mse' });
    }
    async seek(seconds, options) {
      this.seekCalls.push({ seconds, options });
      if (config.seekImpl) return config.seekImpl(seconds, options, this);
      this.positionTicks = Math.round(seconds * 10000000);
    }
    async pause() { this.playing = false; }
    async play() { this.playing = true; }
    setVolume(value) { this.volume = value; }
    selectSubtitle(index) { this.subtitleIndex = index; return true; }
    selectAudio() { return true; }
    destroy() {
      this.destroyCalls++;
      destroyed.push(this);
      return config.destroy ? config.destroy(this) : Promise.resolve();
    }
  }
  return { FakePlayer, instances, destroyed };
}

function mediaSource(overrides = {}) {
  return {
    Id: 'ms1',
    Container: 'mp4',
    DirectStreamUrl: '/Videos/e1/stream.mp4?Static=true&MediaSourceId=ms1',
    MediaStreams: [
      { Type: 'Audio', Index: 1, DisplayTitle: 'AAC', IsDefault: true },
      { Type: 'Subtitle', Index: 2, DisplayTitle: '中文', IsDefault: true },
    ],
    ...overrides,
  };
}

function episode(id, extra = {}) {
  return { Id: id, Name: id, Type: 'Episode', MediaSources: [mediaSource()], ...extra };
}

function makeReply(options = {}) {
  return url => {
    if (url.pathname.endsWith('/PlaybackInfo')) {
      return options.playbackInfo || strictPlaybackInfo([mediaSource()], 'sess');
    }
    if (isSessionEvent(url.pathname)) return {};
    if (url.pathname.startsWith('/Users/')) return { Policy: {} };
    const detail = url.pathname.match(/\/Items\/([^/]+)$/);
    if (detail) return options.itemDetail || episode(decodeURIComponent(detail[1]));
    return { Items: [] };
  };
}

function setup(options = {}) {
  const logs = [];
  const player = makePlayerClass(options.player || {});
  const env = harness({
    storage: makeStorage({ eio_token: 'token', eio_user_id: 'user' }),
    reply: makeReply(options),
    beforeApp: context => {
      context.EIOPlayer = player.FakePlayer;
      context.EIO_ENGINE_CLEANUP_BUDGET_MS = options.cleanupBudgetMs ?? 1500;
      context.console = {
        warn: (...args) => logs.push({ level: 'warn', args }),
        error: (...args) => logs.push({ level: 'error', args }),
        log() {}, info() {}, debug() {},
      };
      options.beforeApp?.(context);
    },
  });
  env.player = player;
  env.logs = logs;
  env.app.page.value = 'app';
  return env;
}

async function waitFor(predicate, timeout = 1000) {
  const started = Date.now();
  while (Date.now() - started < timeout) {
    if (predicate()) return;
    await delay(5);
  }
  assert.fail('condition was not reached before timeout');
}

test('startup resume uses one independent 30s seek; interactive seek keeps the adapter default',
  { timeout: 3000 }, async () => {
    const { app, player, context } = setup();
    context.window.location.hash = '#/play/e1';
    const item = episode('e1', { UserData: { PlaybackPositionTicks: 3362330000 } });

    await app.playItem(item, 'ms1');

    assert.equal(player.instances.length, 1);
    const instance = player.instances[0];
    assert.equal(instance.seekCalls.length, 1,
      'startup resume must seek exactly once with its own budget');
    // The seek options object is created inside the app's vm realm, so compare
    // it scalar-by-scalar (or JSON-normalized) instead of by prototype identity.
    assert.equal(instance.seekCalls[0].seconds, 336.233);
    assert.deepEqual(JSON.parse(JSON.stringify(instance.seekCalls[0].options)),
      { timeout: 30000, label: 'resume' },
      'startup resume must carry its own 30s budget and a resume label');
    assert.equal(app.playerUI.status.value, 'playing');

    // A normal scrub does not inherit the startup budget: the adapter default
    // (10s, or the engine's explicit seekTimeout) remains in charge.
    app.wasmTime.value = 100;
    app.onWasmSeek(120);
    await delay(20);
    assert.equal(instance.seekCalls.length, 2);
    assert.deepEqual(instance.seekCalls[1], { seconds: 120, options: undefined });
  });

test('a startup seek timeout still fails/destroys and logs only safe numbers',
  { timeout: 3000 }, async () => {
    const sensitive = 'https://upstream.example/secret?api_key=TOKEN item-id-42 user-7';
    const timeoutError = Object.assign(new Error(sensitive), {
      name: 'TimeoutError', label: 'resume', timeoutMs: 30000,
    });
    const { app, player, context, logs } = setup({
      player: { seekImpl: () => Promise.reject(timeoutError) },
    });
    context.window.location.hash = '#/play/e1';
    const item = episode('e1', { UserData: { PlaybackPositionTicks: 3366000000 } });

    await app.playItem(item, 'ms1');

    const instance = player.instances[0];
    assert.equal(instance.seekCalls.length, 1, 'a timed-out resume must not be retried');
    assert.equal(instance.destroyCalls, 1, 'the failed startup still destroys its engine');
    assert.equal(app.playerUI.status.value, 'error');

    const startupLogs = logs.filter(entry => String(entry.args[0]).includes('startup resume seek timeout'));
    assert.equal(startupLogs.length, 1);
    assert.equal(startupLogs[0].level, 'warn');
    const logged = JSON.stringify(startupLogs[0].args);
    assert.doesNotMatch(logged, /secret|api_key|TOKEN|item-id-42|user-7|https?:\/\//i,
      'the timeout log leaked error text, URL, id or credentials');
    assert.match(logged, /"timeoutMs":30000/);
    assert.match(logged, /"targetSeconds":337/);
  });

test('an ordinary startup seek error is not logged as a timeout and still fails',
  { timeout: 3000 }, async () => {
    const { app, player, context, logs } = setup({
      player: { seekImpl: () => Promise.reject(new Error('decoder unavailable')) },
    });
    context.window.location.hash = '#/play/e1';
    const item = episode('e1', { UserData: { PlaybackPositionTicks: 1000000000 } });

    await app.playItem(item, 'ms1');

    assert.equal(player.instances[0].seekCalls.length, 1, 'ordinary errors are not retried');
    assert.equal(app.playerUI.status.value, 'error');
    assert.equal(player.instances[0].destroyCalls, 1);
    assert.equal(logs.filter(entry =>
      String(entry.args[0]).includes('startup resume seek timeout')).length, 0,
    'a non-timeout error must not use the startup timeout marker');
  });

test('a stale-generation startup timeout is silent and never retries the seek',
  { timeout: 3000 }, async () => {
    let rejectSeek;
    const { app, player, context, logs } = setup({
      player: {
        seekImpl: () => new Promise((resolve, reject) => { rejectSeek = reject; }),
      },
    });
    context.window.location.hash = '#/play/e1';
    const item = episode('e1', { UserData: { PlaybackPositionTicks: 2000000000 } });

    const loading = app.playItem(item, 'ms1');
    await waitFor(() => player.instances.length === 1
      && player.instances[0].seekCalls.length === 1);

    // Leaving the player bumps the generation and starts teardown while the
    // startup seek is still pending.
    app.exitPlayer();
    rejectSeek(Object.assign(new Error('late timeout'), {
      name: 'TimeoutError', label: 'resume', timeoutMs: 30000,
    }));
    await loading;

    assert.equal(player.instances.length, 1, 'a stale seek must not create a new engine');
    assert.equal(player.instances[0].seekCalls.length, 1, 'a stale seek must not be retried');
    assert.equal(logs.filter(entry =>
      String(entry.args[0]).includes('startup resume seek timeout')).length, 0,
    'a stale generation logged the startup timeout marker');
  });
