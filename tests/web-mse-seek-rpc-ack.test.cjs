'use strict';

/*
 * Main-thread strict MSE seek ACK guard tests.
 *
 * The methods under test are sliced out of the actual shipped
 * public/vendor/web/libmedia/avplayer.js text and executed on fake RPC objects.
 * No replica of doSeek/seek/select/playUseMSE is used. No browser, Worker,
 * MediaSource, upstream, application server or production instance is touched.
 */

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const BUNDLE = path.join(__dirname, '../public/vendor/web/libmedia/avplayer.js');
const TEXT = fs.readFileSync(BUNDLE, 'utf8');
const ASYNC = { timeout: 3000 };

const METHOD_HEADS = [
  'eioMseStrictSeekState(){',
  'eioMseStrictSeekOwner(owner){',
  'eioMseSeekAckError(stage,ack){',
  'eioMseStrictBeforeSeekAck(ack){',
  'eioMseStrictAfterSeekAck(ack){',
  'eioMseStatusRun(status){',
  'eioMseRestoreStatus(run,mseOnly){',
  'eioMseDoneStatus(run){',
  'async doSeek(e,t){',
  'async seek(e){',
  'async selectVideo(e,t){',
  'async selectAudio(e,t){',
  'async replayTo(e){',
  'eioBeginMseStartup(){',
  'async eioMsePreplay(pending){',
  'async eioAwaitMseStartup(pending){',
  'async playUseMSE(e){'
];

function sliceMethod(text, head) {
  const start = text.indexOf(head, head === 'async seek(e){' ? text.indexOf('async doSeek(e,t){') : 0);
  assert.ok(start > -1, `vendored method ${head} is missing from the bundle`);
  let depth = 0;
  let seen = false;
  for (let i = start; i < text.length; i++) {
    const ch = text[i];
    if (ch === '{') {
      depth++;
      seen = true;
    } else if (ch === '}') {
      depth--;
      if (seen && depth === 0) return text.slice(start, i + 1);
    }
  }
  throw new Error(`unbalanced method ${head}`);
}

function defaultGlobals() {
  return {
    Ue: {},
    Q: { Dh: -1n },
    P: { pq() {}, R8() {}, z3() {}, h2() {}, Yz() {} },
    n: { f: { 15() { return 0; }, 20() { return 0; }, 25() { return 0; } } },
    h: { Q: {} },
    Ae: {
      SEEKING: 'SEEKING',
      SEEKED: 'SEEKED',
      PLAYED: 'PLAYED',
      CHANGING: 'CHANGING',
      CHANGED: 'CHANGED'
    },
    Ce: 'test',
    BigInt,
    Number,
    Error,
    Promise,
    Object,
    Array,
    String,
    Math,
    JSON,
    s: { o9: Symbol('o9') }
  };
}

function loadMethods(overrides = {}) {
  const context = Object.assign(defaultGlobals(), overrides);
  const source = METHOD_HEADS.map((head) => sliceMethod(TEXT, head)).join(',');
  return vm.runInNewContext(`(function(){return {${source}}})()`, context);
}

function makeDomain() {
  const sab = { id: 'strict-sab' };
  return {
    domainId: 'eio:7',
    capacityBytes: 1024,
    wasmMemory: { buffer: { id: 'wasm-heap-buffer' } },
    sab
  };
}

function makeStrictPlayer(overrides = {}) {
  const methods = loadMethods();
  const domain = makeDomain();
  return Object.assign(Object.create(null), methods, {
    taskId: 7,
    useMSE: true,
    status: 6,
    lastStatus: 6,
    currentTime: 0n,
    eioSharedMediaDomain: domain,
    eioSharedMediaDomainController: {},
    eioMseStrictAttach: { taskId: 7, domainId: domain.domainId, sab: domain.sab, wasmMemory: domain.wasmMemory },
    video: { currentTime: 7 },
    audio: null,
    isDash: () => false,
    isHls: () => false,
    isLive: () => false,
    isLive_: false,
    subTaskId: '',
    subtitleTaskId: '',
    selectedSubtitleStream: null,
    selectedVideoStream: null,
    selectedAudioStream: null,
    fire: () => {},
    ...overrides
  });
}

function makeAttachPlayer(ack, domain = makeDomain()) {
  const s = { o9: Symbol('o9') };
  const Ue = {
    startMSEPipeline: async () => undefined,
    MSEThread: {
      registerTask: { transfer() { return { invoke: async () => undefined }; } },
      registerEioSharedMediaDomain: async () => ack,
      setPlayRate() {},
      getMediaSource: async () => null,
      waitEioSourceOpen: async () => ({ok:true}),
      addStream: { transfer() { return { invoke: async () => undefined }; } }
    },
    DemuxerThread: {
      connectStreamTask: { transfer() { return { invoke: async () => undefined }; } }
    },
    IOThread: {}
  };
  const methods = loadMethods({ Ue, s });
  const player = Object.assign(Object.create(null), methods, {
    taskId: 7,
    useMSE: true,
    status: 6,
    lastStatus: 6,
    options: { enableWorker: false, drmSystemOptions: {} },
    formatContext: { streams: [] },
    GlobalData: { [s.o9]: 0 },
    controller: { getMuxerControlPort: () => 1 },
    jitterBufferController: null,
    seekedTimestamp: 0n,
    playRate: 1,
    isLive_: false,
    isDash: () => false,
    isHls: () => false,
    getEioDualCacheActive: () => null,
    getEioSharedMediaDomain: () => domain,
    findBestStream: () => null,
    fire: () => {},
    createAudio: () => undefined,
    createVideo: () => undefined,
    video: {},
    audio: null,
    selectedAudioStream: null,
    selectedVideoStream: null,
    handleTimeupdate: () => {},
    eioMseStrictAttach: null,
    eioSharedMediaDomain: domain,
    eioSharedMediaDomainController: {}
  });
  return { player, domain, sab: domain ? domain.sab : null };
}

function makeDoSeekPlayer(options = {}) {
  const {
    beforeAck,
    afterAck,
    demuxTime = 100n,
    marker = true,
    beforeSeekHook,
    demuxHook,
    afterSeekHook
  } = options;
  const domain = makeDomain();
  const calls = { before: 0, onBefore: 0, demux: 0, after: 0, order: [], afterTime: null };
  let player = null;
  const Ue = {
    MSEThread: {
      async beforeSeek(taskId) {
        calls.before += 1;
        calls.order.push('before');
        if (beforeSeekHook) await beforeSeekHook(player, taskId, calls);
        return beforeAck;
      },
      async afterSeek(taskId, time) {
        calls.after += 1;
        calls.afterTime = time;
        calls.order.push('after');
        if (afterSeekHook) await afterSeekHook(player, taskId, time, calls);
        return afterAck;
      }
    },
    DemuxerThread: {
      async seek(taskId, time, flags) {
        calls.demux += 1;
        calls.order.push('demux');
        if (demuxHook) await demuxHook(player, taskId, time, flags, calls);
        return demuxTime;
      }
    }
  };
  const methods = loadMethods({ Ue });
  player = Object.assign(Object.create(null), methods, {
    taskId: 7,
    useMSE: true,
    status: 6,
    lastStatus: 6,
    currentTime: 0n,
    eioSharedMediaDomain: domain,
    eioSharedMediaDomainController: {},
    eioMseStrictAttach: marker ? { taskId: 7, domainId: domain.domainId, sab: domain.sab, wasmMemory: domain.wasmMemory } : null,
    video: { currentTime: 7 },
    audio: null,
    isDash: () => false,
    isHls: () => false,
    isLive: () => false,
    isLive_: false,
    subTaskId: '',
    subtitleTaskId: '',
    selectedSubtitleStream: null,
    selectedVideoStream: null,
    selectedAudioStream: null,
    isAttachmentPicture: () => false,
    formatContext: { streams: [{}] },
    fire: () => {}
  });
  return { player, calls, domain };
}

function makeSeekPlayer(doSeekImpl) {
  const methods = loadMethods();
  const events = [];
  const player = Object.assign(Object.create(null), methods, {
    taskId: 7,
    status: 6,
    lastStatus: 6,
    formatContext: { streams: [{}] },
    isLive_: false,
    selectedVideoStream: null,
    selectedAudioStream: null,
    isAttachmentPicture: () => false,
    fire: (event) => events.push(event),
    doSeek: doSeekImpl
  });
  return { player, events };
}

function makeSelectPlayer(doSeekImpl) {
  const Ue = {
    IOThread: {
      async getVideoList() { return { selectedIndex: 0 }; },
      async getAudioList() { return { selectedIndex: 0 }; }
    }
  };
  const methods = loadMethods({ Ue });
  const events = [];
  const player = Object.assign(Object.create(null), methods, {
    taskId: 7,
    status: 6,
    lastStatus: 0,
    useMSE: true,
    currentTime: 0n,
    selectedVideoStream: null,
    selectedAudioStream: null,
    isHls: () => true,
    isDash: () => false,
    isLive: () => false,
    fire: (event) => events.push(event),
    doSeek: doSeekImpl
  });
  return { player, events };
}

test('playUseMSE stores the strict marker only after registerEioSharedMediaDomain returns exactly 1', ASYNC, async () => {
  for (const ack of [0, 2, -1, -2097152, undefined, null, '1', true]) {
    const { player } = makeAttachPlayer(ack);
    await assert.rejects(player.playUseMSE({ video: true, audio: true }), {
      code: 'EIO_SHARED_DOMAIN_ATTACH_FAILED'
    });
    assert.equal(player.eioMseStrictAttach, null, `ACK ${String(ack)} must not arm strict seek`);
  }

  const { player, domain, sab } = makeAttachPlayer(1);
  await player.playUseMSE({ video: true, audio: true });
  assert.deepEqual({ ...player.eioMseStrictAttach }, {
    taskId: 7,
    domainId: domain.domainId,
    sab, wasmMemory: domain.wasmMemory
  });
  assert.equal(player.eioMseStrictSeekState(), player.eioMseStrictAttach);

  const baseline = makeAttachPlayer(1, null);
  await baseline.player.playUseMSE({ video: true, audio: true });
  assert.equal(baseline.player.eioMseStrictAttach, null, 'no domain attach means no strict marker');
});

test('eioMseStrictSeekState rejects a stale physical marker instead of falling back to baseline', () => {
  const base = makeStrictPlayer();
  assert.equal(base.eioMseStrictSeekState(), base.eioMseStrictAttach);

  const mutations = [
    ['taskId', (p) => { p.eioMseStrictAttach.taskId = 8; }],
    ['domainId', (p) => { p.eioSharedMediaDomain.domainId = 'eio:other'; }],
    ['sab', (p) => { p.eioSharedMediaDomain.wasmMemory = { buffer: { id: 'other-sab' } }; }],
    ['controller', (p) => { p.eioSharedMediaDomainController = null; }],
    ['domain', (p) => { p.eioSharedMediaDomain = null; }],
    ['useMSE', (p) => { p.useMSE = false; }]
  ];
  for (const [name, mutate] of mutations) {
    const player = makeStrictPlayer();
    mutate(player);
    assert.throws(() => player.eioMseStrictSeekState(), {
      code: 'EIO_MSE_PULL_DOMAIN_UNVERIFIED'
    }, name);
  }

  const distinctSab = makeStrictPlayer();
  distinctSab.eioMseStrictAttach = { ...distinctSab.eioMseStrictAttach, sab: { id: 'other-sab' } };
  assert.throws(() => distinctSab.eioMseStrictSeekState(), {
    code: 'EIO_MSE_PULL_DOMAIN_UNVERIFIED'
  }, 'a distinct SAB with the same memory and domain is not proof');

  const differentMemory = makeStrictPlayer();
  differentMemory.eioMseStrictAttach = {
    ...differentMemory.eioMseStrictAttach,
    wasmMemory: { buffer: { id: 'other-memory-buffer' } }
  };
  assert.throws(() => differentMemory.eioMseStrictSeekState(), {
    code: 'EIO_MSE_PULL_DOMAIN_UNVERIFIED'
  }, 'the same SAB with a different memory object is not proof');

  const grown = makeStrictPlayer();
  grown.eioSharedMediaDomain.wasmMemory.buffer = { id: 'grown-heap-buffer', byteLength: 2048 };
  assert.equal(grown.eioMseStrictSeekState(), grown.eioMseStrictAttach,
    'the same memory object stays valid across buffer growth');

  const configOnly = makeStrictPlayer({ eioMseStrictAttach: null });
  configOnly.options = { eioDualCache: { enabled: true } };
  assert.equal(configOnly.eioMseStrictSeekState(), null, 'configuration is not physical proof');
});

test('strict beforeSeek accepts only the settled ACK object and runs before onBeforeSeek/demux', ASYNC, async () => {
  const badBefore = [
    undefined,
    0,
    1,
    -1,
    -2097152,
    NaN,
    null,
    {},
    { eioAck: 1 },
    { eioAck: 1, pullSettled: false },
    { eioAck: 2, pullSettled: true }
  ];
  for (const beforeAck of badBefore) {
    const { player, calls } = makeDoSeekPlayer({ beforeAck, afterAck: { eioAck: 1, seekTime: 12.5 } });
    await assert.rejects(
      player.doSeek(0n, -1, { onBeforeSeek: async () => { calls.onBefore += 1; } }),
      { code: 'EIO_MSE_PULL_BEFORESEEK_UNVERIFIED' },
      `before ACK ${String(beforeAck)}`
    );
    assert.equal(calls.onBefore, 0, 'onBeforeSeek must not run after a bad ACK');
    assert.equal(calls.demux, 0, 'demux seek must not run after a bad ACK');
    assert.equal(calls.after, 0, 'afterSeek must not run after a bad ACK');
    assert.equal(player.video.currentTime, 7, 'media currentTime must stay untouched');
  }

  const { player, calls } = makeDoSeekPlayer({
    beforeAck: { eioAck: 1, pullSettled: true },
    afterAck: { eioAck: 1, seekTime: 12.5 }
  });
  await player.doSeek(0n, -1, { onBeforeSeek: async () => { calls.onBefore += 1; calls.order.push('onBefore'); } });
  assert.deepEqual(calls.order, ['before', 'onBefore', 'demux', 'after']);
  assert.equal(player.video.currentTime, 12.5);
});

test('strict afterSeek accepts only {eioAck:1, seekTime: finite non-negative number}', ASYNC, async () => {
  const badAfter = [
    -1,
    -2097152,
    undefined,
    1,
    NaN,
    0,
    null,
    {},
    { eioAck: 1, seekTime: -1 },
    { eioAck: 1, seekTime: NaN },
    { eioAck: 1, seekTime: Infinity },
    { eioAck: 1, seekTime: '1' },
    { eioAck: 1 },
    { eioAck: 2, seekTime: 1 }
  ];
  for (const afterAck of badAfter) {
    const { player, calls } = makeDoSeekPlayer({
      beforeAck: { eioAck: 1, pullSettled: true },
      afterAck,
      demuxTime: 100n
    });
    await assert.rejects(
      player.doSeek(0n, -1, { onBeforeSeek: async () => { calls.onBefore += 1; } }),
      { code: 'EIO_MSE_PULL_AFTERSEEK_UNVERIFIED' },
      `after ACK ${String(afterAck)}`
    );
    assert.equal(calls.after, 1, 'the worker ACK is observed before the guard rejects');
    assert.equal(player.video.currentTime, 7, 'bad ACK must not mutate media currentTime');
  }

  const negativeDemux = makeDoSeekPlayer({
    beforeAck: { eioAck: 1, pullSettled: true },
    afterAck: -2097152,
    demuxTime: -1n
  });
  await assert.rejects(
    negativeDemux.player.doSeek(0n, -1, { onBeforeSeek: async () => { negativeDemux.calls.onBefore += 1; } }),
    { code: 'EIO_MSE_PULL_AFTERSEEK_UNVERIFIED' }
  );
  assert.equal(negativeDemux.player.video.currentTime, 7, 'a<0 branch still never writes media');

  const zero = makeDoSeekPlayer({
    beforeAck: { eioAck: 1, pullSettled: true },
    afterAck: { eioAck: 1, seekTime: 0 },
    demuxTime: 100n
  });
  await zero.player.doSeek(0n, -1, { onBeforeSeek: async () => { zero.calls.onBefore += 1; } });
  assert.equal(zero.player.video.currentTime, 0, 'a valid zero-second ACK is accepted');

  const fractional = makeDoSeekPlayer({
    beforeAck: { eioAck: 1, pullSettled: true },
    afterAck: { eioAck: 1, seekTime: 12.5 },
    demuxTime: 100n
  });
  await fractional.player.doSeek(0n, -1, { onBeforeSeek: async () => { fractional.calls.onBefore += 1; } });
  assert.equal(fractional.player.video.currentTime, 12.5);
});

test('deferred beforeSeek ACK with a replaced marker/taskId is stale before onBeforeSeek/demux/DOM', ASYNC, async () => {
  let release;
  const gate = new Promise((resolve) => { release = resolve; });
  const { player, calls, domain } = makeDoSeekPlayer({
    beforeAck: { eioAck: 1, pullSettled: true },
    afterAck: { eioAck: 1, seekTime: 12.5 },
    beforeSeekHook: async (current) => {
      await gate;
      current.taskId = 8;
      current.eioMseStrictAttach = {
        taskId: 8,
        domainId: domain.domainId,
        sab: domain.sab,
        wasmMemory: domain.wasmMemory
      };
    }
  });
  const pending = player.doSeek(0n, -1, { onBeforeSeek: async () => { calls.onBefore += 1; } });
  release();
  await assert.rejects(pending, { code: 'EIO_MSE_PULL_SEEK_STALE' });
  assert.equal(calls.onBefore, 0, 'onBeforeSeek must not run after a stale owner');
  assert.equal(calls.demux, 0, 'demux must not run after a stale owner');
  assert.equal(calls.after, 0, 'afterSeek must not run after a stale owner');
  assert.equal(player.video.currentTime, 7, 'media must stay untouched after a stale owner');
});

test('a marker replacement during awaited onBeforeSeek is stale before demux', ASYNC, async () => {
  const { player, calls } = makeDoSeekPlayer({
    beforeAck: { eioAck: 1, pullSettled: true },
    afterAck: { eioAck: 1, seekTime: 12.5 }
  });
  await assert.rejects(player.doSeek(0n, -1, {
    onBeforeSeek: async () => {
      calls.onBefore += 1;
      player.eioMseStrictAttach = { ...player.eioMseStrictAttach };
    }
  }), { code: 'EIO_MSE_PULL_SEEK_STALE' });
  assert.equal(calls.onBefore, 1);
  assert.equal(calls.demux, 0, 'demux must not run after the marker changes');
  assert.equal(calls.after, 0, 'afterSeek must not run after the marker changes');
  assert.equal(player.video.currentTime, 7);
});

test('a marker replacement after the demux result is stale before afterSeek/DOM', ASYNC, async () => {
  let release;
  const gate = new Promise((resolve) => { release = resolve; });
  const { player, calls } = makeDoSeekPlayer({
    beforeAck: { eioAck: 1, pullSettled: true },
    afterAck: { eioAck: 1, seekTime: 12.5 },
    demuxHook: async (current) => {
      await gate;
      current.eioMseStrictAttach = { ...current.eioMseStrictAttach };
    }
  });
  const pending = player.doSeek(0n, -1, { onBeforeSeek: async () => { calls.onBefore += 1; } });
  release();
  await assert.rejects(pending, { code: 'EIO_MSE_PULL_SEEK_STALE' });
  assert.equal(calls.onBefore, 1);
  assert.equal(calls.demux, 1, 'the demux result was already observed');
  assert.equal(calls.after, 0, 'afterSeek must not run after the marker changes');
  assert.equal(player.video.currentTime, 7);
});

test('a marker replacement after the afterSeek result is stale before DOM/progress/status changes', ASYNC, async () => {
  let release;
  const gate = new Promise((resolve) => { release = resolve; });
  const { player, calls } = makeDoSeekPlayer({
    beforeAck: { eioAck: 1, pullSettled: true },
    afterAck: { eioAck: 1, seekTime: 12.5 },
    afterSeekHook: async (current) => {
      await gate;
      current.eioMseStrictAttach = { ...current.eioMseStrictAttach };
    }
  });
  const pending = player.seek(0n);
  release();
  await assert.rejects(pending, { code: 'EIO_MSE_PULL_SEEK_STALE' });
  assert.equal(calls.after, 1);
  assert.equal(player.video.currentTime, 7, 'media must stay untouched');
  assert.equal(player.status, 6, 'the older run restores only its own status');
  assert.equal(player.eioMseSeekRunToken, null);
});

test('a newer main status run makes an older strict seek stale before the next RPC', ASYNC, async () => {
  let release;
  const gate = new Promise((resolve) => { release = resolve; });
  const { player, calls } = makeDoSeekPlayer({
    beforeAck: { eioAck: 1, pullSettled: true },
    afterAck: { eioAck: 1, seekTime: 12.5 },
    beforeSeekHook: async (current) => {
      await gate;
      current.eioMseStatusRun(10);
      current.status = 10;
    }
  });
  const pending = player.seek(0n);
  release();
  await assert.rejects(pending, { code: 'EIO_MSE_PULL_SEEK_STALE' });
  assert.equal(calls.onBefore, 0);
  assert.equal(calls.demux, 0);
  assert.equal(player.status, 10, 'the older run must not overwrite the newer status');
});

test('strict bad ACK reached through seek restores status 9 and never mutates media', ASYNC, async () => {
  const cases = [
    {
      beforeAck: -2097152,
      afterAck: { eioAck: 1, seekTime: 12.5 },
      code: 'EIO_MSE_PULL_BEFORESEEK_UNVERIFIED'
    },
    {
      beforeAck: { eioAck: 1, pullSettled: true },
      afterAck: -2097152,
      code: 'EIO_MSE_PULL_AFTERSEEK_UNVERIFIED'
    }
  ];
  for (const item of cases) {
    const { player } = makeDoSeekPlayer(item);
    await assert.rejects(player.seek(0n), { code: item.code });
    assert.equal(player.status, 6, `${item.code} restores the previous status`);
    assert.equal(player.video.currentTime, 7, `${item.code} never writes media`);
    assert.equal(player.eioMseSeekRunToken, null);
  }
});

test('baseline MSE doSeek keeps its call order and uses the raw afterSeek number', ASYNC, async () => {
  const { player, calls } = makeDoSeekPlayer({
    beforeAck: undefined,
    afterAck: 12.5,
    demuxTime: 100n,
    marker: false
  });
  await player.doSeek(0n, -1, { onBeforeSeek: async () => { calls.onBefore += 1; calls.order.push('onBefore'); } });
  assert.deepEqual(calls.order, ['before', 'onBefore', 'demux', 'after']);
  assert.equal(player.video.currentTime, 12.5);
  assert.equal(calls.afterTime, 100n);
});

test('seek restores status 9 on failure, rethrows the original error and emits no SEEKED/PLAYED', ASYNC, async () => {
  const failure = new Error('seek failed');
  const failed = makeSeekPlayer(async () => { throw failure; });
  await assert.rejects(failed.player.seek(0n), (error) => error === failure);
  assert.equal(failed.player.status, 6);
  assert.deepEqual(failed.events, ['SEEKING']);
  assert.equal(failed.player.eioMseSeekRunToken, null);

  const ok = makeSeekPlayer(async () => undefined);
  await ok.player.seek(0n);
  assert.equal(ok.player.status, 6);
  assert.deepEqual(ok.events, ['SEEKING', 'SEEKED']);
});

test('seek failure does not clobber a newer reentrant status run', ASYNC, async () => {
  const failure = new Error('seek failed');
  let newer;
  const { player } = makeSeekPlayer(async function () {
    newer = this.eioMseStatusRun(10);
    this.status = 10;
    throw failure;
  });
  await assert.rejects(player.seek(0n), (error) => error === failure);
  assert.equal(player.status, 10, 'the older run must not restore over the newer run');
  assert.equal(player.eioMseSeekRunToken, newer, 'the newer run keeps ownership');
});

test('selectVideo/selectAudio restore status 10 on failure and leave a newer run untouched', ASYNC, async () => {
  for (const kind of ['video', 'audio']) {
    const failure = new Error(`${kind} select failed`);
    const failed = makeSelectPlayer(async () => { throw failure; });
    const call = kind === 'video' ? failed.player.selectVideo(1, false) : failed.player.selectAudio(1, false);
    await assert.rejects(call, (error) => error === failure);
    assert.equal(failed.player.status, 6, `${kind} status restored`);
    assert.deepEqual(failed.events, ['CHANGING'], `${kind} emits no CHANGED on failure`);

    let newer;
    const reentrant = makeSelectPlayer(async function () {
      newer = this.eioMseStatusRun(9);
      this.status = 9;
      throw failure;
    });
    const reentrantCall = kind === 'video'
      ? reentrant.player.selectVideo(1, false)
      : reentrant.player.selectAudio(1, false);
    await assert.rejects(reentrantCall, (error) => error === failure);
    assert.equal(reentrant.player.status, 9, `${kind} older run must not restore over newer state`);
    assert.equal(reentrant.player.eioMseSeekRunToken, newer);
  }
});

test('actual main startup helpers reject a pending native play on worker failure and never report ready early', ASYNC, async()=>{
  let reply;
  const methods=loadMethods({Ue:{MSEThread:{waitEioSourceOpen:()=>new Promise(r=>{reply=r})}}});
  const events=[];
  const player=Object.assign({},methods,{taskId:7,status:5,eioMseStrictAttach:{},onError:e=>events.push(e.code)});
  player.eioBeginMseStartup();
  const wait=player.eioAwaitMseStartup(new Promise(()=>{}));
  reply({ok:false,code:'EIO_PROBE_BUDGET_EXHAUSTED'});
  await assert.rejects(wait,{code:'EIO_PROBE_BUDGET_EXHAUSTED'});
  assert.deepEqual(events,['EIO_PROBE_BUDGET_EXHAUSTED']);
});
test('actual main startup helpers require both readiness and native play and reject obsolete continuation', ASYNC, async()=>{
  let reply,native;
  const methods=loadMethods({Ue:{MSEThread:{waitEioSourceOpen:()=>new Promise(r=>{reply=r})}}});
  const player=Object.assign({},methods,{taskId:7,status:5,eioMseStrictAttach:{},onError(){}});
  player.eioBeginMseStartup();
  let complete=false;
  const wait=player.eioAwaitMseStartup(new Promise(r=>{native=r})).then(()=>{complete=true});
  reply({ok:true});await Promise.resolve();assert.equal(complete,false);
  player.status=2;native();await assert.rejects(wait,/EIO_MSE_STARTUP_CANCELLED/);
});
test('actual replay re-arms startup for the replacement source and rejects its failure', ASYNC, async()=>{
  let reply;
  const methods=loadMethods({Ue:{DemuxerThread:{seek:async()=>0n},MSEThread:{restart:async()=>{},getMediaSource:async()=>({}),waitEioSourceOpen:()=>new Promise(r=>{reply=r})}},y:{A:{workerMSE:false}},URL:{revokeObjectURL(){},createObjectURL(){return 'blob:synthetic'}}});
  const player=Object.assign({},methods,{taskId:7,status:7,useMSE:true,eioMseStrictAttach:{},video:{src:'blob:old',play:()=>new Promise(()=>{})},playRate:1,isDash:()=>false,isHls:()=>false,fire(){},onError(){}});
  const replay=player.replayTo(0n);
  while(!reply)await new Promise(r=>setTimeout(r,0));
  reply({ok:false,code:'EIO_PROBE_BUDGET_EXHAUSTED'});
  await assert.rejects(replay,{code:'EIO_PROBE_BUDGET_EXHAUSTED'});
});
test('startup cancellation still rejects the waiter without emitting a playback error', ASYNC, async()=>{
  const methods=loadMethods({Ue:{MSEThread:{waitEioSourceOpen:async()=>({ok:false,code:'EIO_MSE_STARTUP_CANCELLED'})}}});
  let errors=0;
  const player=Object.assign({},methods,{taskId:7,status:5,eioMseStrictAttach:{},onError(){errors++}});
  player.eioBeginMseStartup();await assert.rejects(player.eioAwaitMseStartup(new Promise(()=>{})),{code:'EIO_MSE_STARTUP_CANCELLED'});
  assert.equal(errors,0);
});
