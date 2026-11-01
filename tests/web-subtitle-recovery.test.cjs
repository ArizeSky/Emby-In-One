'use strict';
/* Subtitle recovery regressions: byte budgets for unknown lengths, aborts,
 * honest boolean track results and a shared engine seek queue. */
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

function canvasElement() {
  const context2d = {
    clearRect() {}, drawImage() {},
    createImageData(width, height) { return { data: new Uint8ClampedArray(width * height * 4) }; },
    putImageData() {},
  };
  return {
    width: 300, height: 150, clientWidth: 640, clientHeight: 360, style: {},
    className: '', removed: false,
    getContext: () => context2d,
    append() {}, remove() { this.removed = true; },
  };
}

function domDocument() {
  return {
    createElement(tag) {
      if (tag === 'textarea') {
        return { _html: '', set innerHTML(value) { this._html = value; }, get value() { return this._html; } };
      }
      if (tag === 'canvas') return canvasElement();
      return { className: '', style: {}, append() {}, prepend() {}, replaceChildren() {}, remove() {} };
    },
  };
}

function containerElement() {
  return {
    children: [], clientWidth: 640, clientHeight: 360, style: {},
    append(child) { this.children.push(child); },
    prepend(child) { this.children.unshift(child); },
    replaceChildren() { this.children = []; },
    remove() { this.removed = true; },
  };
}

function bytes(text) { return new Uint8Array(Buffer.from(text)); }

function streamingResponse(chunks, options = {}) {
  let index = 0;
  const calls = { reads: 0, cancels: [] };
  const body = {
    getReader() {
      return {
        async read() {
          calls.reads++;
          if (index >= chunks.length) return { done: true, value: undefined };
          return { done: false, value: chunks[index++] };
        },
        async cancel(reason) { calls.cancels.push(reason); },
      };
    },
    async cancel(reason) { calls.cancels.push(reason); },
  };
  return {
    ok: options.ok !== false,
    status: options.status || 200,
    // Real fetch responses expose a Headers instance with case-insensitive
    // lookups; plain objects would not exercise that contract.
    headers: options.headers ? new Headers(options.headers) : new Headers(),
    body,
    _calls: calls,
    text: async () => { throw new Error('streaming responses must not call text()'); },
    arrayBuffer: async () => { throw new Error('streaming responses must not call arrayBuffer()'); },
  };
}

function loadScripts(context) {
  vm.createContext(context);
  for (const file of ['subtitles.js', 'subtitle-controller.js']) {
    vm.runInContext(fs.readFileSync(path.join(__dirname, '../public/web', file), 'utf8'), context);
  }
}

function controllerHarness(options = {}) {
  const states = [];
  const source = options.mediaSource || {
    Id: 'source',
    MediaStreams: [
      { Index: 0, Type: 'Video', Codec: 'h264' },
      { Index: 2, Type: 'Subtitle', Codec: 'vtt', DeliveryUrl: '/sub/2.vtt' },
    ],
  };
  const player = {
    container: containerElement(),
    core: options.core || null,
    item: { Id: 'item' },
    mediaSource: source,
    positionTicks: 0,
    playing: false,
    _destroyed: false,
    _engineGeneration: 1,
    _emitState(state) { states.push(state); },
  };
  const context = {
    window: { devicePixelRatio: 1 },
    location: { origin: 'http://localhost:8096' },
    localStorage: { getItem: () => 'local-token' },
    URL, AbortController, DOMException, TextDecoder, Map, console,
    document: domDocument(),
    ResizeObserver: class { observe() {} disconnect() {} },
    fetch: options.fetch || (async () => { throw new Error('unexpected fetch'); }),
  };
  context.window = context;
  loadScripts(context);
  const controller = new context.window.EIOSubtitles(player);
  return { context, controller, player, states };
}

function vttResponse(text) {
  return streamingResponse([bytes(text)]);
}

async function playerHarness(options = {}) {
  const instances = [];
  const states = [];
  const fetchCalls = [];
  class Core {
    constructor() {
      this.listeners = {};
      this.seekCalls = [];
      this.streams = options.streams || [];
      instances.push(this);
    }
    on(event, handler) { this.listeners[event] = handler; }
    async load(url) { this.url = url; }
    async play() {
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
    getStreams() { return this.streams; }
    hasVideo() { return true; }
    isSuspended() { return false; }
    isMSE() { return true; }
    isHls() { return false; }
    getSelectedAudioStreamId() { return this.audioId; }
    async selectAudio(id) { this.audioId = id; }
    getSelectedSubtitleStreamId() { return this.subtitleId; }
    async selectSubtitle(id) { this.subtitleId = id; }
    setSubtitleEnable(value) { this.subtitleEnabled = value; }
    setVolume() {}
    resize() {}
  }
  const context = {
    window: { devicePixelRatio: 1 },
    location: { origin: 'http://localhost:8096' },
    localStorage: { getItem: () => 'local-token' },
    URL, AbortController, DOMException, TextDecoder, Map, console,
    document: domDocument(),
    ResizeObserver: class { observe() {} disconnect() {} },
    AVPlayer: Core,
    fetch: async (url, fetchOptions) => {
      fetchCalls.push({ url: String(url), options: fetchOptions });
      return options.fetch(url, fetchOptions, fetchCalls.length);
    },
    setInterval, clearInterval, setTimeout, clearTimeout,
  };
  context.window = context;
  vm.createContext(context);
  for (const file of ['subtitles.js', 'subtitle-controller.js', 'player-libmedia.js']) {
    vm.runInContext(fs.readFileSync(path.join(__dirname, '../public/web', file), 'utf8'), context);
  }
  const player = new context.window.EIOPlayer(containerElement());
  livePlayers.push(player);
  player.onState = state => states.push(state);
  const source = {
    Id: 'source', Container: 'mkv',
    MediaStreams: [
      { Index: 0, Type: 'Video', Codec: 'hevc' },
      { Index: 2, Type: 'Subtitle', Codec: 'vtt', IsExternal: true, DeliveryUrl: '/sub/old.vtt' },
      { Index: 3, Type: 'Subtitle', Codec: 'vtt', IsExternal: true, DeliveryUrl: '/sub/new.vtt' },
    ],
  };
  return { player, instances, states, context, source, fetchCalls };
}

test('a declared subtitle size above the text budget is rejected before reading', { timeout: 3000 }, async () => {
  const response = streamingResponse([bytes('WEBVTT\n')], {
    headers: { 'Content-Length': String(16 * 1024 * 1024 + 1) },
  });
  const { controller } = controllerHarness({ fetch: async () => response });
  await assert.rejects(controller.select(2), /字幕文件过大/);
  assert.equal(response._calls.reads, 0);
  assert.ok(response._calls.cancels.length > 0);
  controller.destroy();
});

test('unknown-length text and PGS streams stop exactly at the byte budget', { timeout: 3000 }, async () => {
  const { context } = controllerHarness();
  const text = streamingResponse([bytes('12345'), bytes('67890')]);
  await assert.rejects(context.window.EIOReadLimited(text, 8, { mode: 'text' }), /字幕文件过大/);
  assert.ok(text._calls.cancels.length > 0);

  const binary = streamingResponse([new Uint8Array(5), new Uint8Array(6)]);
  await assert.rejects(context.window.EIOReadLimited(binary, 8, { mode: 'bytes' }), /字幕文件过大/);
  assert.ok(binary._calls.cancels.length > 0);
});

test('aborting a streaming subtitle read cancels the reader with AbortError', { timeout: 3000 }, async () => {
  const { context } = controllerHarness();
  const controller = new AbortController();
  const chunks = [bytes('WEBVTT\n')];
  const response = streamingResponse(chunks);
  const original = response.body.getReader;
  response.body.getReader = () => {
    const reader = original();
    reader.read = (() => {
      let call = 0;
      return async () => {
        call++;
        if (call > 1) return new Promise(() => {});
        return { done: false, value: chunks[0] };
      };
    })();
    return reader;
  };
  const pending = context.window.EIOReadLimited(response, 1024, { mode: 'text', signal: controller.signal });
  await delay(5);
  controller.abort();
  await assert.rejects(pending, error => error.name === 'AbortError');
  assert.equal(response._calls.cancels.length, 1);
  assert.equal(response._calls.cancels[0].name, 'AbortError');
});

test('unknown-length VTT and PGS samples load normally', { timeout: 3000 }, async () => {
  const vtt = '<!doctype html>';
  let response = vttResponse('WEBVTT\n\n00:00.000 --> 00:01.000\nHello');
  const { controller } = controllerHarness({ fetch: async () => response });
  assert.equal(await controller.select(2), true);
  assert.equal(controller.cues.length, 1);
  assert.equal(controller.cues[0].text, 'Hello');
  controller.destroy();

  const fixture = new Uint8Array(fs.readFileSync(path.join(__dirname, 'fixtures/pgs-fragments.sup')));
  response = streamingResponse([fixture]);
  const pgsSource = {
    Id: 'source',
    MediaStreams: [{ Index: 2, Type: 'Subtitle', Codec: 'pgs', DeliveryUrl: '/sub/2.sup' }],
  };
  const pgs = controllerHarness({ fetch: async () => response, mediaSource: pgsSource });
  assert.equal(await pgs.controller.select(2), true);
  assert.deepEqual(Array.from(pgs.controller.pgs.sortedPts), [0, 4, 5]);
  pgs.controller.destroy();
});

test('ASS font budget rejects an oversized font without creating an overlay', { timeout: 3000 }, async () => {
  const assSource = {
    Id: 'source',
    MediaStreams: [{ Index: 2, Type: 'Subtitle', Codec: 'ass', IsExternal: true, DeliveryUrl: '/sub/2.ass' }],
  };
  const response = streamingResponse([bytes('[Script Info]\nTitle: test')]);
  const font = streamingResponse([new Uint8Array(16)], {
    headers: { 'Content-Length': String(16 * 1024 * 1024 + 1) },
  });
  const { controller, context } = controllerHarness({
    mediaSource: assSource,
    fetch: async url => (String(url).includes('NotoSans') ? font : response),
  });
  let constructed = false;
  context.JASSUB = class { constructor() { constructed = true; } destroy() {} addEventListener() {} resize() {} };
  await assert.rejects(controller.select(2), /字幕文件过大/);
  assert.equal(constructed, false);
  assert.equal(font._calls.reads, 0);
  controller.destroy();
});

test('oversized PGS is rejected by the shared streaming budget', { timeout: 3000 }, async () => {
  const pgsSource = {
    Id: 'source',
    MediaStreams: [{ Index: 2, Type: 'Subtitle', Codec: 'pgs', DeliveryUrl: '/sub/2.sup' }],
  };
  const response = streamingResponse([new Uint8Array(8)], {
    headers: { 'Content-Length': String(64 * 1024 * 1024 + 1) },
  });
  const { controller } = controllerHarness({ fetch: async () => response, mediaSource: pgsSource });
  await assert.rejects(controller.select(2), /字幕文件过大/);
  assert.equal(response._calls.reads, 0);
  controller.destroy();
});

test('a missing subtitle track returns false and notifies subtitleError', { timeout: 3000 }, async () => {
  const { player, states } = await playerHarness({ fetch: async () => vttResponse('WEBVTT\n') });
  await player.load('/stream.mkv', sourceFor(), itemFor());
  const before = player.subtitleIndex;
  assert.equal(await player.selectSubtitle(99), false);
  assert.ok(states.some(state => state.subtitleError === '字幕轨道不可用'));
  assert.equal(player.subtitleIndex, before);
  assert.equal(await player.selectSubtitle(-1), true);
  assert.equal(player.subtitleIndex, -1);
});

test('a failed HTTP subtitle returns false with a diagnostic', { timeout: 3000 }, async () => {
  const { player, states } = await playerHarness({
    fetch: async () => ({ ok: false, status: 500, headers: {}, body: null }),
  });
  await player.load('/stream.mkv', sourceFor(), itemFor());
  assert.equal(await player.selectSubtitle(2), false);
  assert.ok(states.some(state => /字幕加载失败/.test(state.subtitleError || '')));
});

test('a superseded same-generation subtitle request cannot overwrite the new choice', { timeout: 3000 }, async () => {
  let finishOld;
  const { player, states } = await playerHarness({
    fetch: async url => {
      if (String(url).includes('old.vtt')) {
        return new Promise(resolve => { finishOld = () => resolve(vttResponse('WEBVTT\n\n00:00.000 --> 00:01.000\nOld')); });
      }
      return vttResponse('WEBVTT\n\n00:00.000 --> 00:01.000\nNew');
    },
  });
  await player.load('/stream.mkv', sourceFor(), itemFor());
  const old = player.selectSubtitle(2, { timeout: 1000 });
  await delay(5);
  assert.equal(await player.selectSubtitle(3, { timeout: 1000 }), true);
  assert.equal(player.subtitleIndex, 3);
  finishOld();
  await assert.rejects(old, error => error.name === 'AbortError');
  assert.equal(player.subtitleIndex, 3);
  assert.equal(states.filter(state => state.subtitleIndex === 2).length, 0);
});

test('an old request completion cannot clear the newer selection timeout', { timeout: 3000 }, async () => {
  let finishOld;
  const { player, states } = await playerHarness({
    fetch: async url => {
      if (String(url).includes('old.vtt')) {
        return new Promise(resolve => { finishOld = () => resolve(vttResponse('WEBVTT\n')); });
      }
      return new Promise(() => {});
    },
  });
  await player.load('/stream.mkv', sourceFor(), itemFor());
  const old = player.selectSubtitle(2, { timeout: 1000 });
  await delay(5);
  const fresh = player.selectSubtitle(3, { timeout: 40 });
  setTimeout(() => finishOld(), 5);
  await assert.rejects(old, error => error.name === 'AbortError');
  const result = await Promise.race([fresh, delay(300).then(() => 'hung')]);
  assert.equal(result, false, 'new subtitle timeout was cleared by the old request');
  assert.ok(states.some(state => state.subtitleError === '字幕加载超时'));
});

test('a late old request cannot detach the newer external abort listener', { timeout: 3000 }, async () => {
  let finishOld;
  const calls = [];
  const { player, states } = await playerHarness({
    fetch: (url, options) => new Promise(resolve => {
      calls.push({ url: String(url), options, resolve });
      if (String(url).includes('old.vtt')) {
        finishOld = () => resolve(vttResponse('WEBVTT\n\n00:00.000 --> 00:01.000\nOld'));
      }
    }),
  });
  await player.load('/stream.mkv', sourceFor(), itemFor());
  const before = player.subtitleIndex;
  const old = player.selectSubtitle(2, { timeout: 2000 });
  await delay(5);
  // Same engine generation: the second choice supersedes the first while the
  // first fetch is still in flight.
  const fresh = player.selectSubtitle(3, { timeout: 2000 });
  await delay(5);
  assert.equal(calls.length, 2);
  // The superseded request settles late, after the newer one linked itself.
  finishOld();
  await assert.rejects(old, error => error.name === 'AbortError');
  // Aborting the newer request must still reach the fetch signal that this
  // request linked; the old finally must not have removed its listener.
  player._subtitleAbort.abort();
  assert.equal(calls[1].options.signal.aborted, true,
    'old finally detached the newer external abort listener');
  calls[1].resolve(vttResponse('WEBVTT\n\n00:00.000 --> 00:01.000\nNew'));
  await assert.rejects(fresh, error => error.name === 'AbortError');
  // The late old result must not resurrect its overlay or track choice.
  assert.equal(player.subtitles.cues, null);
  assert.equal(player.subtitles.layer, null);
  assert.equal(player.subtitleIndex, before);
  assert.equal(states.filter(state => state.subtitleIndex === 2).length, 0);
});

test('default subtitle fetch does not block load and times out independently', { timeout: 3000 }, async () => {
  const { player, states } = await playerHarness({ fetch: () => new Promise(() => {}) });
  const source = sourceFor();
  const started = Date.now();
  await player.load('/stream.mkv', source, itemFor(), { subtitleIndex: 2, subtitleTimeout: 25 });
  assert.ok(Date.now() - started < 500);
  await waitFor(() => states.some(state => state.subtitleError === '字幕加载超时'));
});

test('subtitle repositioning shares the engine seek queue and reads the latest position', { timeout: 3000 }, async () => {
  const streams = [{ mediaType: 'Subtitle', index: 2, id: 7 }];
  const { player, instances } = await playerHarness({ streams, fetch: async () => vttResponse('WEBVTT\n') });
  const source = sourceFor();
  source.MediaStreams = [
    { Index: 0, Type: 'Video', Codec: 'hevc' },
    { Index: 2, Type: 'Subtitle', Codec: 'srt' },
  ];
  await player.load('/stream.mkv', source, itemFor());
  const core = instances[0];
  core.currentTime = 2000n;
  const pending = [];
  core.seekImpl = timestamp => new Promise(resolve => pending.push({ timestamp, resolve }));

  const userSeek = player.seek(100);
  await delay(0);
  assert.deepEqual(core.seekCalls, [100000n]);
  const subtitle = player.selectSubtitle(2);
  await delay(0);
  assert.equal(core.seekCalls.length, 1, 'subtitle seek bypassed the engine queue');

  core.currentTime = 100000n;
  pending.shift().resolve();
  await userSeek;
  await waitFor(() => core.seekCalls.length === 2);
  assert.deepEqual(core.seekCalls, [100000n, 100000n]);
  pending.shift().resolve();
  assert.equal(await subtitle, true);
});

function sourceFor() {
  return {
    Id: 'source', Container: 'mkv',
    MediaStreams: [
      { Index: 0, Type: 'Video', Codec: 'hevc' },
      { Index: 2, Type: 'Subtitle', Codec: 'vtt', IsExternal: true, DeliveryUrl: '/sub/old.vtt' },
      { Index: 3, Type: 'Subtitle', Codec: 'vtt', IsExternal: true, DeliveryUrl: '/sub/new.vtt' },
    ],
  };
}

function itemFor() { return { Id: 'item', RunTimeTicks: 3000000000 }; }
