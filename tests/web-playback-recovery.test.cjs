'use strict';
/* Work package A: /play entry recovery behaviour.
 *
 * These tests drive the production scripts through the shared harness (same
 * script order as web.html) with a stub engine, and cover the acceptance
 * points that are not already covered by the player/seek suites:
 *  - the series queue is navigation-only and never blocks PlaybackInfo/start;
 *  - the router handoff is consumed once and a failed attempt cannot leak it;
 *  - "兼容播放" only ever plays the refreshed TranscodingUrl;
 *  - a partial (package E) episode list is usable data, never a silent
 *    first-episode guess;
 *  - an engine destroy that hangs or rejects is a bounded, sticky barrier.
 */
const { test } = require('node:test');
const assert = require('node:assert/strict');
const { harness, makeStorage, delay } = require('./web-ui-harness.cjs');

const plain = value => Array.from(value || []);

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
      this.subtitleIndex = config.subtitleIndex ?? -1;
      this.volume = 1;
      this.loadCalls = [];
      this.selectCalls = [];
      this.seekCalls = [];
      instances.push(this);
    }
    async load(url, mediaSource, item, opts = {}) {
      this.loadCalls.push({ url, mediaSource, item, opts });
      this.mediaSource = mediaSource;
      this.playing = true;
      config.emitState?.(this);
      this.onState?.({ playing: true, loading: false, time: 0, duration: this.duration, mode: 'mse' });
    }
    async seek(seconds) { this.seekCalls.push(seconds); this.positionTicks = Math.round(seconds * 10000000); }
    async pause() { this.playing = false; }
    async play() { this.playing = true; }
    setVolume(value) { this.volume = value; }
    destroy() {
      destroyed.push(this);
      return config.destroy ? config.destroy(this) : Promise.resolve();
    }
    selectSubtitle(index) {
      this.selectCalls.push(index);
      return config.selectSubtitle ? config.selectSubtitle(index, this) : true;
    }
    selectAudio() { return true; }
  }
  return { FakePlayer, instances, destroyed };
}

const episode = (id, extra = {}) => ({ Id: id, Name: id, Type: 'Episode', SeriesId: 's1', ...extra });

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

// A reply table that models the endpoints the /play entry touches. `record`
// keeps the request bodies so tests can assert what was actually negotiated.
function makeReply(options = {}) {
  const bodies = [];
  const reply = (url, opts) => {
    let body = null;
    if (typeof opts?.body === 'string') { try { body = JSON.parse(opts.body); } catch (e) {} }
    bodies.push({ url, body });
    if (url.pathname.endsWith('/PlaybackInfo')) {
      return typeof options.playbackInfo === 'function'
        ? options.playbackInfo(body)
        : (options.playbackInfo || { MediaSources: [mediaSource()], PlaySessionId: 'sess' });
    }
    if (url.pathname.endsWith('/Episodes')) {
      return typeof options.episodes === 'function' ? options.episodes(body) : (options.episodes ?? { Items: [episode('e1'), episode('e2')] });
    }
    if (url.pathname.endsWith('/NextUp')) return options.nextUp ?? { Items: [] };
    // Item detail: app.js fetches it whenever the item has no MediaSources yet
    // (episodes coming from a series list). Returning the real episode keeps
    // SeriesId/Id so the queue and PlaybackInfo paths match production.
    const detail = url.pathname.match(/\/Items\/([^/]+)$/);
    if (detail) {
      const id = decodeURIComponent(detail[1]);
      return options.itemDetail ? options.itemDetail(id) : episode(id);
    }
    if (url.pathname.startsWith('/Sessions/Playing')) return {};
    if (url.pathname.startsWith('/Users/')) return { Policy: {} };
    return { Items: [] };
  };
  return { reply, bodies, calls: [] };
}

function partialResults(items) {
  // Mirrors package E's contract: usable items plus a recognisable name.
  const error = new Error('partial candidate set');
  error.name = 'PartialResultsError';
  error.items = items;
  error.partial = true;
  return error;
}

function setup(options = {}) {
  const table = makeReply(options);
  const player = makePlayerClass(options.player || {});
  const env = harness({
    storage: makeStorage({ eio_token: 'token', eio_user_id: 'user' }),
    reply: table.reply,
    beforeApp: context => {
      context.EIOPlayer = player.FakePlayer;
      context.EIO_ENGINE_CLEANUP_BUDGET_MS = options.cleanupBudgetMs ?? 1500;
      options.beforeApp?.(context);
    },
  });
  env.player = player;
  env.reply = table;
  env.app.page.value = 'app';
  return env;
}

const playbackInfoBodies = table => table.bodies.filter(x => x.url.pathname.endsWith('/PlaybackInfo')).map(x => x.body);

test('A1 a slow series queue never blocks PlaybackInfo or engine start', async () => {
  let releaseEpisodes;
  const { app, player, reply, context } = setup({
    episodes: () => new Promise(resolve => { releaseEpisodes = resolve; }),
  });
  context.window.location.hash = '#/play/e1';
  await app.playItem(episode('e1'));

  // Playback started while the queue request is still pending.
  assert.equal(player.instances.length, 1);
  assert.equal(player.instances[0].loadCalls.length, 1);
  assert.equal(app.playerUI.status.value, 'playing');
  assert.equal(app.episodeQueueLoading.value, true);
  assert.equal(app.queueError.value, '');
  const paths = reply.bodies.map(x => x.url.pathname);
  assert.ok(paths.findIndex(p => p.endsWith('/PlaybackInfo')) < paths.findIndex(p => p.endsWith('/Episodes')));

  releaseEpisodes({ Items: [episode('e2'), episode('e1')] });
  await delay(10);
  assert.deepEqual(plain(app.episodeQueue.value).map(i => i.Id), ['e1', 'e2']);
  assert.equal(app.episodeQueueLoading.value, false);
  assert.equal(app.episodeQueuePartial.value, false);
});

test('A2 a failed attempt consumes the router handoff instead of reusing it', async () => {
  let fail = true;
  const { app, player, reply, context } = setup({
    playbackInfo: () => {
      if (fail) return { __status: 500, __body: 'upstream down' };
      return { MediaSources: [mediaSource()], PlaySessionId: 'sess' };
    },
  });
  context.window.location.hash = '#/play/e1';
  await app.playItem(episode('e1'), 'ms1', { forceTranscode: true });
  assert.equal(app.playerUI.status.value, 'error');
  assert.equal(player.instances.length, 0);
  assert.equal(playbackInfoBodies(reply)[0].EnableDirectPlay, false);

  // The retry after the failure must negotiate the normal profile again; the
  // failed selection (forceTranscode/source id) is gone.
  fail = false;
  app.retryPlayback();
  await delay(20);
  const retry = playbackInfoBodies(reply)[1];
  assert.equal(retry.EnableDirectPlay, undefined);
  assert.equal(retry.EnableDirectStream, undefined);
  assert.equal(retry.EnableTranscoding, undefined);
  assert.equal(player.instances.length, 1);
  assert.match(player.instances[0].loadCalls[0].url, /stream\.mp4/);
});

test('A3 compatibility playback uses only the transcode URL', async () => {
  const { app, player, reply, context } = setup({
    playbackInfo: () => ({
      MediaSources: [mediaSource({ TranscodingUrl: '/Videos/e1/master.m3u8?MediaSourceId=ms1' })],
      PlaySessionId: 'sess',
    }),
  });
  context.window.location.hash = '#/play/e1';
  await app.playItem(episode('e1'), null, { forceTranscode: true });
  const body = playbackInfoBodies(reply)[0];
  assert.equal(body.EnableDirectPlay, false);
  assert.equal(body.EnableDirectStream, false);
  assert.equal(body.EnableTranscoding, true);
  assert.equal(player.instances.length, 1);
  assert.match(player.instances[0].loadCalls[0].url, /master\.m3u8/);
  assert.doesNotMatch(player.instances[0].loadCalls[0].url, /stream\.mp4/);
  assert.equal(player.instances[0].loadCalls[0].opts.fallbackUrl, null);

  // Without a transcode URL the forced retry fails visibly; the original
  // stream is never substituted.
  const second = setup({ playbackInfo: () => ({ MediaSources: [mediaSource()], PlaySessionId: 'sess' }) });
  second.context.window.location.hash = '#/play/e1';
  await second.app.playItem(episode('e1'), null, { forceTranscode: true });
  assert.equal(second.app.playerUI.status.value, 'error');
  assert.match(second.app.playerUI.error.value.message, /转码/);
  assert.equal(second.player.instances.length, 0);
  assert.match(second.app.playerUI.error.value.message, /兼容播放/);
});

test('A4 the "兼容播放" button re-negotiates a transcode stream', async () => {
  const sources = [
    { MediaSources: [mediaSource()], PlaySessionId: 'sess' },
    { MediaSources: [mediaSource({ TranscodingUrl: '/Videos/e1/master.m3u8?MediaSourceId=ms1' })], PlaySessionId: 'sess' },
  ];
  let index = 0;
  const { app, player, reply, context } = setup({ playbackInfo: () => sources[Math.min(index++, 1)] });
  context.window.location.hash = '#/play/e1';
  await app.playItem(episode('e1'));
  assert.match(player.instances[0].loadCalls[0].url, /stream\.mp4/);
  // Simulate the engine failing after direct play.
  app.playerUI.fail('failed', '无法加载播放内容', { item: episode('e1'), mediaSourceId: 'ms1', startSeconds: 42, paused: true });
  app.retryPlayback(true);
  await delay(20);
  assert.equal(player.instances.length, 2);
  assert.match(player.instances[1].loadCalls[0].url, /master\.m3u8/);
  assert.doesNotMatch(player.instances[1].loadCalls[0].url, /stream\.mp4/);
  const last = playbackInfoBodies(reply).at(-1);
  assert.equal(last.EnableDirectPlay, false);
  assert.equal(last.EnableTranscoding, true);
  // Resume is a single client-side seek from the start of the transcode. The
  // new transcode URL must not also carry an upstream start tick: the engine
  // has no absolute-time mapping for a trimmed stream yet.
  assert.equal(last.StartTimeTicks, undefined, 'no second upstream start position');
  assert.deepEqual(player.instances[1].seekCalls, [42], 'exactly one client resume seek');
});

test('A5 a partial (package E) episode list never auto-picks an episode', async () => {
  const env = setup({
    beforeApp: context => {
      context.EIOData.all = async (api, path) => {
        if (String(path).endsWith('/Episodes')) throw partialResults([episode('e2'), episode('e3')]);
        return [];
      };
    },
  });
  const { app, player, context } = env;
  context.window.location.hash = '#/home';
  await app.playItem({ Id: 's1', Name: 'Show', Type: 'Series' });

  assert.equal(player.instances.length, 0, 'no engine may start from an incomplete list');
  assert.equal(context.window.location.hash, '#/item/s1');
  assert.match(app.toasts.items.value.at(-1).message, /未完整/);
  assert.deepEqual(plain(app.episodeQueue.value), []);
});

test('A6 a complete episode list still resolves to the first episode', async () => {
  const env = setup({
    beforeApp: context => {
      context.EIOData.all = async (api, path) => {
        if (String(path).endsWith('/Episodes')) return [episode('e2', { IndexNumber: 2 }), episode('e1', { IndexNumber: 1 })];
        return [];
      };
    },
  });
  const { app, player, context } = env;
  context.window.location.hash = '#/play/e1';
  await app.playItem({ Id: 's1', Name: 'Show', Type: 'Series' });
  assert.equal(player.instances.length, 1);
  assert.equal(player.instances[0].loadCalls[0].item.Id, 'e1');
});

test('A7 a hung engine destroy blocks re-entry/retry until it settles', async () => {
  let release;
  const hung = new Promise(resolve => { release = resolve; });
  const { app, player, context } = setup({ cleanupBudgetMs: 20, player: { destroy: () => hung } });
  context.window.location.hash = '#/play/e1';
  await app.playItem(episode('e1'));
  assert.equal(player.instances.length, 1);

  app.exitPlayer(); // leave the player: the destroy never settles
  context.window.location.hash = '#/play/e1';
  await app.playItem(episode('e1'));
  assert.equal(player.instances.length, 1, 'no second decoder while the first is alive');
  assert.equal(app.playerUI.status.value, 'error');
  assert.match(app.playerUI.error.value.message, /尚未释放资源/);

  // A retry is not a recovery: still bounded, still no new decoder.
  app.retryPlayback();
  await delay(60);
  assert.equal(player.instances.length, 1);
  assert.match(app.playerUI.error.value.message, /尚未释放资源/);

  // A pending cleanup is not permanent: once it really settles, a later entry
  // may create a new engine.
  release();
  await delay(20);
  context.window.location.hash = '#/play/e1';
  await app.playItem(episode('e1'));
  assert.equal(player.instances.length, 2);
  assert.equal(app.playerUI.status.value, 'playing');
});

test('A8 a rejected engine destroy stays blocking across exit and re-entry', async () => {
  const { app, player, context } = setup({
    cleanupBudgetMs: 20,
    player: { destroy: () => Promise.reject(new Error('engine wedged')) },
  });
  context.window.location.hash = '#/play/e1';
  await app.playItem(episode('e1'));
  assert.equal(player.instances.length, 1);

  app.exitPlayer();
  await delay(10); // let the tracked destroy reject
  context.window.location.hash = '#/play/e1';
  await app.playItem(episode('e1'));
  assert.equal(player.instances.length, 1, 'a rejected teardown must not allow a new decoder');
  assert.match(app.playerUI.error.value.message, /刷新页面/);

  // Retrying must not silently clear the barrier.
  app.retryPlayback();
  await delay(40);
  assert.equal(player.instances.length, 1);
  assert.match(app.playerUI.error.value.message, /刷新页面/);

  // Leaving and navigating back is not a product-licensed recovery either:
  // the leaked decoder is still alive, so the barrier stays up.
  app.exitPlayer();
  await delay(10);
  context.window.location.hash = '#/home';
  context.window.location.hash = '#/play/e1';
  await app.playItem(episode('e1'));
  assert.equal(player.instances.length, 1);
  assert.match(app.playerUI.error.value.message, /刷新页面/);
});

test('A9 cleanup gate: a hung cleanup recovers after settling; a rejection stays permanent', async () => {
  const gate = harness().context.EIOPlayback.createCleanupGate({ budgetMs: 15 });
  let release;
  const hung = new Promise(resolve => { release = resolve; });
  gate.track(hung);
  assert.equal((await gate.waitForIdle()).reason, 'timeout');
  assert.equal((await gate.waitForIdle()).reason, 'timeout', 'a hung cleanup keeps blocking');
  assert.equal(gate.outstanding, 1);
  release();
  await delay(0);
  assert.equal((await gate.waitForIdle()).ok, true, 'a genuinely settled cleanup can recover');
  assert.equal(gate.unsafe, false);

  const rejecting = harness().context.EIOPlayback.createCleanupGate({ budgetMs: 15 });
  await rejecting.track(Promise.reject(new Error('wedged')));
  assert.equal(rejecting.unsafe, true);
  assert.equal((await rejecting.waitForIdle()).reason, 'failed');
  assert.equal((await rejecting.waitForIdle()).reason, 'failed', 'a rejection is permanent, not a one-off warning');
  // A later successful cleanup does not hide the unsafe state either, and
  // there is deliberately no reset() escape hatch for UI navigation.
  await rejecting.track(Promise.resolve());
  assert.equal((await rejecting.waitForIdle()).reason, 'failed');
  assert.equal(typeof rejecting.reset, 'undefined');
});

test('A10 package E library partial state is re-exported from app', async () => {
  const partial = { value: true };
  const partialNotice = { get value() { return '列表可能不完整'; } };
  const partialCanContinue = { value: true };
  const calls = [];
  const { app } = setup({
    beforeApp: context => {
      const original = context.EIOLibrary.create;
      context.EIOLibrary = {
        create: deps => Object.assign(original(deps), {
          partial, partialNotice, partialCanContinue,
          continuePartial: (...args) => { calls.push(args); return 'continued'; },
        }),
      };
    },
  });
  assert.equal(app.libraryPartial, partial);
  assert.equal(app.libraryPartialNotice, partialNotice);
  assert.equal(app.libraryPartialCanContinue, partialCanContinue);
  assert.equal(app.continueLibraryPartial(5), 'continued');
  assert.deepEqual(calls, [[5]]);
});

test('A11 selectSubtitle false/undefined never fake-selects a track', async () => {
  const { app, player, context } = setup({
    player: {
      subtitleIndex: 2,
      selectSubtitle: index => {
        if (index === 3) return false;
        if (index === 4) return undefined;
        return true;
      },
    },
  });
  context.window.location.hash = '#/play/e1';
  await app.playItem(episode('e1'));
  assert.equal(player.instances.length, 1);
  assert.equal(app.wasmSubTrack.value, 2, 'the default track stays the engine-reported one');

  const before = player.instances[0].selectCalls.length;
  await app.selectSubtitle(3); // engine says false
  assert.equal(app.wasmSubTrack.value, 2, 'false must not become a selected track');
  assert.deepEqual(player.instances[0].selectCalls.slice(before), [3, 2]);
  assert.equal(app.toasts.items.value.filter(t => t.kind === 'error').length, 0);

  const beforeUndefined = player.instances[0].selectCalls.length;
  await app.selectSubtitle(4); // engine did not confirm
  assert.equal(app.wasmSubTrack.value, 2);
  assert.deepEqual(player.instances[0].selectCalls.slice(beforeUndefined), [4, 2]);

  await app.selectSubtitle(5); // confirmed
  assert.equal(app.wasmSubTrack.value, 5);
});

test('A12 a background default-subtitle failure does not abort playback', async () => {
  const { app, player, context } = setup({
    player: {
      subtitleIndex: 2,
      selectSubtitle: () => Promise.reject(new Error('subtitle engine down')),
      emitState: self => self.onState?.({
        playing: true, loading: false, time: 0, duration: 3600, mode: 'mse', subtitleError: 'subtitle engine down',
      }),
    },
  });
  context.window.location.hash = '#/play/e1';
  await app.playItem(episode('e1'));
  assert.equal(app.playerUI.status.value, 'playing');
  assert.equal(app.wasmSubTrack.value, -1);
  assert.match(app.toasts.items.value.at(-1).message, /字幕/);
  assert.equal(player.instances.length, 1);
});

test('A13 onState drives background subtitle index and audio errors', async () => {
  const { app, player, context } = setup({
    player: {
      emitState: self => self.onState?.({
        playing: true, loading: false, time: 0, duration: 3600, mode: 'mse',
        subtitleIndex: 2, audioError: '音轨加载失败：解码器不可用',
      }),
    },
  });
  context.window.location.hash = '#/play/e1';
  await app.playItem(episode('e1'));
  const instance = player.instances[0];
  assert.equal(app.wasmSubTrack.value, 2, 'the background default subtitle is consumed');
  assert.equal(app.playerUI.status.value, 'playing', 'an audio diagnostic does not fail playback');
  assert.match(app.toasts.items.value.at(-1).message, /音轨加载失败/);

  // A late background subtitle report must update the UI even after the
  // startup sync captured its earlier snapshot; the audio error is not spammed
  // by every later state event.
  const toastCount = app.toasts.items.value.length;
  instance.onState({ playing: true, loading: false, time: 1, duration: 3600, mode: 'mse', subtitleIndex: 3 });
  assert.equal(app.wasmSubTrack.value, 3);
  instance.onState({
    playing: true, loading: false, time: 2, duration: 3600, mode: 'mse',
    subtitleIndex: 3, audioError: '音轨加载失败：解码器不可用',
  });
  assert.equal(app.wasmSubTrack.value, 3);
  assert.equal(app.toasts.items.value.length, toastCount);
});
