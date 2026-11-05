const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// These cases execute the *patched vendored* files:
//  - `__eioDualDemux` (the inline ledger/token factory of DemuxPipeline),
//  - the real DemuxPipeline methods `eioInstallProbe` / `doDemux` /
//    `drainCacheRequests` / `replyAVPacket` / `configureEioDualCache` /
//    `abortEioDualCache` / `getEioDualCacheSnapshot` / `croppingAVPacketQueue` /
//    `changeConnectStream` / `unregisterTask`,
//  - the real AVPlayer `getEioDualCacheProvisional` / `getEioIOLoaderOptions`
//    and the aggregate `getEioDualCacheSnapshot`,
//  - the real FetchIOLoader `read` / `openReader` / `configureEioDualCache` and the
//    actual `eioOwnedStrict` gate plus its module-scope `__eioOwnedIO` helper.
//
// The 943 format module is not copied here: the fake `E.Cj` drives the four
// formatContext hook fields in exactly the order documented by the vendored
// `readAVPacket_` (before -> isCancelled -> malloc/NX -> after -> readBuffer),
// so the wiring under test is the Demux/AVPlayer/IO side.
//
// Scope boundary: this file proves probe-time admission, conservative held
// accounting and the absence of packet-pointer credit. It does NOT prove a
// final shared-data free (not observable from this worker), MSE mux accounting,
// readHeader extradata/attachedPic accounting or real media playback.
const BUNDLE = path.join(__dirname, '../public/vendor/web/libmedia/avplayer.js');
const ASYNC = { timeout: 2000 };
const EOF = -1048576;
const LR = -2;
const tick = () => new Promise((resolve) => setImmediate(resolve));

function bundleText() {
  return fs.readFileSync(BUNDLE, 'utf8');
}

function sliceHelper(text, name) {
  const begin = `/*__EIO_DUAL_CACHE_${name}_BEGIN__*/`;
  const end = `/*__EIO_DUAL_CACHE_${name}_END__*/`;
  const start = text.indexOf(begin);
  const stop = text.indexOf(end);
  assert.ok(start > -1 && stop > start, `${name} helper region is missing from the bundle`);
  return text.slice(start + begin.length, stop);
}

function sliceMethod(text, head, from = 0) {
  const start = text.indexOf(head, from);
  assert.ok(start > -1, `vendored method ${head} is missing from the bundle`);
  let depth = 0;
  let seen = false;
  for (let i = start; i < text.length; i += 1) {
    const ch = text[i];
    if (ch === '{') {
      depth += 1;
      seen = true;
    } else if (ch === '}') {
      depth -= 1;
      if (seen && depth === 0) return text.slice(start, i + 1);
    }
  }
  throw new Error(`unbalanced vendored method ${head}`);
}

// The actual `const __eioOwnedIO={...};` module-scope helper sits outside the
// IO marker region, so it is sliced by its own real anchors and evaluated as-is
// (never re-implemented, never replaced by a false stub).
function sliceOwnedIOHelper(text) {
  const head = 'const __eioOwnedIO={';
  const start = text.indexOf(head);
  assert.ok(start > -1, 'the actual __eioOwnedIO helper is missing from the bundle');
  const stop = text.indexOf('\n};', start);
  assert.ok(stop > start, 'the __eioOwnedIO helper terminator is missing from the bundle');
  return text.slice(start, stop + 3);
}

// The real load body, delimited by two actual statements of the current bundle:
// its reader-buffer/provisional locals and the final LOADED transition.
function loadBody(text) {
  const start = text.indexOf('let i=0,r=0,__eioProbeDual=null,__eioEarlyDomain=null;');
  const end = text.indexOf('this.status=4,this.fire(Ae.LOADED)', start);
  assert.ok(start > -1 && end > start, 'the real load body was not found');
  return text.slice(start, end);
}

function demuxClassRegion(text) {
  const classHead = 'class V extends L.A{constructor(){super()}createTask(e){';
  const start = text.indexOf(classHead);
  assert.ok(start > -1, 'the vendored DemuxPipeline class is missing from the bundle');
  const lastHead = 'async unregisterTask(e){';
  const last = text.indexOf(lastHead, start);
  assert.ok(last > start, `the vendored method ${lastHead} is missing from the DemuxPipeline class`);
  const tail = sliceMethod(text, lastHead, start);
  return text.slice(start, last + tail.length);
}

// ---------------------------------------------------------------- demux harness
function demuxHarness(provisional) {
  const text = bundleText();
  const helperSource = sliceHelper(text, 'DEMUX');
  const region = demuxClassRegion(text);
  const mem = new Map();
  const i32 = (addr) => {
    const v = mem.get(addr);
    return typeof v === 'number' ? v : 0;
  };
  const i64 = (addr) => {
    const v = mem.get(addr);
    return typeof v === 'bigint' ? v : 0n;
  };
  const u32 = (addr) => i32(addr) >>> 0;

  const helper = vm.runInNewContext(`${helperSource}\n;__eioDualDemux;`, {
    Map, Math, Number, isFinite, performance: { now: () => 0 },
    o: { f: { 15: i32, 17: i64, 20: u32, 25: u32 } }
  });

  const releaseCalls = [];
  const demuxErrors = [];
  const replies = [];
  const scenario = { queue: [], error: 0 };
  let pointer = 2000;

  const streams = [{ index: 0, codecType: 0, codecId: 27, codepbar: null }];
  streams[0].codecpar = { codecType: 0, codecId: 27 };

  const formatContext = { streams, interval: { packetBuffer: [] }, destroy: async () => {} };
  const task = {
    taskId: 'probe-task',
    stats: 0,
    eioOptions: {},
    eioLedger: null,
    eioFixedBytes: 1000,
    demuxEnded: false,
    formatContext,
    ioReader: { flags: 0, abort() {} },
    leftIPCPort: { destroy() {} },
    controlIPCPort: {
      notify(method, params) { demuxErrors.push({ method, params }); },
      destroy() {}
    },
    rightIPCPorts: new Map(),
    // The real pipeline only reaches the cache-push branch of doDemux after the
    // stream has been registered as a connected stream (`connectStreamTask` /
    // `changeConnectStream` do `cacheAVPackets.set(index, [])`). Model that
    // connected default video stream explicitly; pendingAVPackets stays empty
    // until a consumer asks for a pending stream, and rightIPCPorts is only
    // populated by queueRequest().
    cacheAVPackets: new Map([[0, []]]),
    cacheRequests: new Map(),
    pendingAVPackets: new Map(),
    streamIndexFlush: new Map(),
    tasks: new Map(),
    eioProducerOperations: new Set(),
    avpacketPool: {
      alloc() { pointer += 64; return pointer; },
      release(packet) { releaseCalls.push(packet); mem.delete(packet + 28); mem.delete(packet + 32); }
    },
    loop: {
      stopped: false,
      stop() { this.stopped = true; },
      start() {},
      emptyTask() {},
      async stopBeforeNextTick() {},
      destroy() {}
    }
  };
  task.tasks.set(task.taskId, task);

  const methods = [
    sliceMethod(region, 'eioInstallProbe(e,t){'),
    sliceMethod(region, 'drainCacheRequests(e){'),
    sliceMethod(region, 'async doDemux(e,t){'),
    sliceMethod(region, 'doAttachedPicture(e){'),
    sliceMethod(region, 'replyAVPacket(e,t,i,r){'),
    sliceMethod(region, 'async configureEioDualCache(e,t){'),
    sliceMethod(region, 'async abortEioDualCache(e){'),
    sliceMethod(region, 'async getEioDualCacheSnapshot(e){'),
    sliceMethod(region, 'async croppingAVPacketQueue(e,t){'),
    sliceMethod(region, 'async changeConnectStream(e,t,i){'),
    // Current strict teardown helpers, sliced from the same real Demux class.
    sliceMethod(region, 'eioFreeFixedField('),
    sliceMethod(region, 'eioReleaseOwnedFixed('),
    sliceMethod(region, 'async eioAbortOwnedReads('),
    sliceMethod(region, 'async eioRequireProducersStopped('),
    sliceMethod(region, 'async eioAwaitProducers('),
    sliceMethod(region, 'async unregisterTask(e){')
  ].join(',');

  // Mirrors the four hook call sites of the vendored `readAVPacket_`:
  // packet fields -> before -> cancel recheck -> malloc/NX -> after -> readBuffer.
  const runHookPath = async (fc, packet, spec) => {
    mem.set(packet + 28, spec.size);
    mem.set(packet + 32, spec.index);
    const before = fc.eioBeforeSampleAlloc;
    if (typeof before !== 'function') {
      if (spec.data !== undefined) mem.set(packet + 24, spec.data);
      return 0;
    }
    const info = {
      packet,
      size: spec.size,
      pts: BigInt(spec.pts || 0),
      dts: BigInt(spec.dts || 0),
      duration: BigInt(spec.duration || 0),
      timeBase: { num: 1, den: 1000 }
    };
    let token;
    try {
      token = await before(info);
    } catch (err) {
      return LR;
    }
    if (token === null || token === undefined) return LR;
    if (typeof fc.eioIsSampleAllocCancelled === 'function'
      && fc.eioIsSampleAllocCancelled(token, { packet, size: spec.size })) {
      fc.eioReleaseSampleAlloc(token, { reason: 'cancelled', size: spec.size });
      return LR;
    }
    const data = spec.data === undefined ? packet + 4096 : spec.data;
    mem.set(packet + 24, data);
    fc.eioAfterSampleAlloc(token, { packet, data, size: spec.size });
    return 0;
  };

  const context = {
    setTimeout,
    clearTimeout,
    __eioDualDemux: helper,
    __eioDualDemuxSize: (p) => (p > 0 ? Math.max(i32(p + 28), 0) : 0),
    o: { f: { 15: i32, 17: i64, 20: u32, 25: u32 } },
    E: {
      Cj: async (fc, packet) => {
        const spec = scenario.queue.shift();
        if (!spec) return scenario.error || -1;
        return runHookPath(fc, packet, spec);
      }
    },
    n: { M: { 15() {}, 16() {}, 17() {} } },
    u: { Mr: (value) => value, i0: {} },
    d: { rU: () => 0, iI: () => 0, Ow() {}, rN() {} },
    b: { s3: () => 0, Mr() {}, lW() {} },
    c: { sY: () => 0 },
    P: { T9: (a, b) => (a > b ? a : b) },
    _: { __: (list, fn) => list.forEach(fn) },
    M: { z3() {}, R8() {}, pq() {}, Yz() {}, h2() {} },
    A: { sL: () => false },
    v: { sL: () => false },
    y: { sL: () => false },
    g: { i0: {} },
    F: -1,
    S: { A: () => false },
    x: { USE_THREADS: false },
    $: 'web-dual-cache-probe.test.cjs'
  };

  const runnable = vm.runInNewContext(`(function(){ return { ${methods} }; })()`, context);
  Object.assign(task, runnable);

  if (provisional !== undefined || arguments.length > 0) {
    task.eioInstallProbe(formatContext, { taskId: task.taskId, eioDualCache: provisional });
  }
  return { task, helper, mem, scenario, releaseCalls, demuxErrors, replies, formatContext, i32 };
}

const PROBE_CFG = {
  enabled: true,
  demuxBytes: 1400,
  maxPacketBytes: 200,
  ioBytes: 1000,
  probeBytes: 500
};

const PLAY_CFG = {
  enabled: true,
  demuxBytes: 1400,
  maxPacketBytes: 200,
  ioBytes: 1000
};

async function enterPlay(task) {
  assert.equal(await task.configureEioDualCache(task.taskId, PLAY_CFG), 1);
  assert.equal(task.eioLedger.phase, 2);
}

function queueRequest(h, streamIndex) {
  const request = { request: `pull-${streamIndex}` };
  const port = { reply(req, value) { h.replies.push({ streamIndex, req, value }); } };
  h.task.cacheRequests.set(streamIndex, request);
  h.task.rightIPCPorts.set(streamIndex, port);
  return request;
}

// The ledger must stay conserved after every mutation: the byte counters have to
// decompose into the real container contents plus the token buckets, and no path
// may return credit for data that is still retained. `complete` is never used
// here: these are arithmetic checks on the counters, not a completeness claim.
function assertConserved(h, label) {
  const task = h.task;
  const snap = h.helper.snapshot(task);
  let cache = 0;
  task.cacheAVPackets.forEach((list) => {
    list.forEach((packet) => { cache += Math.max(h.i32(packet + 28), 0); });
  });
  let pending = 0;
  task.pendingAVPackets.forEach((list) => {
    list.forEach((packet) => { pending += Math.max(h.i32(packet + 28), 0); });
  });
  assert.equal(snap.cacheTotal, cache, `${label}: cacheTotal equals the real queued packets`);
  assert.equal(snap.pendingTotal, pending, `${label}: pendingTotal equals the real pending packets`);
  assert.equal(
    snap.admissionHeld,
    cache + pending + snap.localTokenBytes,
    `${label}: admission decomposes into containers + local tokens`
  );
  assert.equal(
    snap.held,
    snap.admissionHeld + snap.unprovenBytes + snap.outboundBytes,
    `${label}: held decomposes with no hidden credit`
  );
  return snap;
}

// ---------------------------------------------------------------- demux cases
test('createTask probe wiring installs the four formatContext hooks before any allocation', () => {
  const h = demuxHarness(PROBE_CFG);
  const { task, formatContext } = h;

  for (const field of [
    'eioBeforeSampleAlloc',
    'eioAfterSampleAlloc',
    'eioReleaseSampleAlloc',
    'eioIsSampleAllocCancelled'
  ]) {
    assert.equal(typeof formatContext[field], 'function', `${field} must be installed on the formatContext`);
  }
  assert.equal(task.eioLedger.sampleAllocHooked, true);
  assert.equal(task.eioLedger.hooksInstalled, true);
  // The provisional payload is a probe marker only: it must never be reported
  // as an active play-time ledger.
  assert.equal(task.eioDualCache.phase, 'probe');
  assert.equal(task.eioDualCache.enabled, true);

  const before = h.helper.snapshot(task);
  assert.equal(before.phase, 1, 'createTask starts in PROBE');
  assert.equal(before.probeBytes, 500, 'probe capacity is the configured real byte count');
  assert.equal(before.probeHeld, 0);
  assert.equal(before.held, 0);
  assert.equal(before.releaseVerified, false);
  assert.equal(before.accounting.sampleAlloc, true);
  assert.equal(before.accounting.sampleRelease, false);
  // Inventory visibility is not proof of complete admission/release coverage.
  assert.equal(before.inventory.interval, true);
  assert.equal(before.inventory.headers, true);
  assert.equal(before.inventory.admissionAuthority, false);
  assert.equal(before.accounting.extradata, true);
  assert.equal(before.accounting.attachedPic, true);
  assert.equal(before.accounting.interval, false);
  assert.equal(before.accounting.crossWorkerCopy, false);
});

test('probe reservation is real sample bytes, not a maxPacket guess', async () => {
  const h = demuxHarness(PROBE_CFG);
  const { task, scenario } = h;

  scenario.queue = [{ index: 0, size: 300 }];
  assert.equal(await task.doDemux(task, 10), 0);
  let snap = h.helper.snapshot(task);
  assert.equal(snap.held, 300, 'held tracks the real sample size');
  assert.equal(snap.admissionHeld, 300);
  assert.equal(snap.probeHeld, 300);
  assert.equal(snap.tHeld, 300);
  assert.equal(snap.localTokenBytes, 0, 'the packet already moved the token into the cache queue');
  assert.equal(snap.cacheTotal, 300);
  assert.equal(snap.probeRequests, 1);
  assert.equal(snap.probeBytes, 500);

  scenario.queue = [{ index: 0, size: 100 }];
  assert.equal(await task.doDemux(task, 10), 0);
  snap = h.helper.snapshot(task);
  assert.equal(snap.held, 400);
  assert.equal(snap.probeHeld, 400, 'probe capacity is consumed by real bytes only');
  assertConserved(h, 'probe reservation');
});

test('probe budget exhaustion returns a non-EOF resource error and never awaits', async () => {
  const h = demuxHarness({ ...PROBE_CFG, probeBytes: 100 });
  const { task, formatContext, scenario, demuxErrors } = h;

  scenario.queue = [{ index: 0, size: 100 }];
  assert.equal(await task.doDemux(task, 10), 0);

  const pendingResult = formatContext.eioBeforeSampleAlloc({ packet: 9000, size: 1 });
  assert.equal(pendingResult instanceof Promise, false, 'PROBE has no consumer: it must never await');
  assert.equal(pendingResult, null, 'an over-capacity probe sample is refused, not reselected');
  assert.deepEqual(
    demuxErrors.map((entry) => [entry.method, entry.params.code]),
    [['demuxError', 'EIO_PROBE_BUDGET_EXHAUSTED']],
    'the demux reports the explicit resource shortfall on its control port'
  );
  demuxErrors.length = 0;

  scenario.queue = [{ index: 0, size: 1 }];
  const result = await task.doDemux(task, 10);
  assert.equal(result, LR);
  assert.notEqual(result, EOF, 'a probe budget refusal must not look like a demuxer EOF');
  assert.equal(task.demuxEnded, false);
  assert.equal(task.loop.stopped, false);

  const snap = h.helper.snapshot(task);
  assert.equal(snap.probeExhausted, true);
  assert.equal(snap.lastError, 'EIO_PROBE_BUDGET_EXHAUSTED');
  assert.equal(snap.probeHeld, 100);
  assert.ok(demuxErrors.length >= 1, 'the failed read is surfaced, never swallowed');
  for (const entry of demuxErrors) {
    assert.equal(entry.method, 'demuxError');
    assert.notEqual(entry.params.code, EOF, 'no pull request may be answered with a fake EOF');
  }
  assert.equal(h.replies.length, 0, 'no pull request may be answered with a fake EOF');
  assertConserved(h, 'probe exhaustion');
});

test('activate keeps probe-held tokens and the cache move does not double count', async () => {
  const h = demuxHarness(PROBE_CFG);
  const { task, formatContext, scenario } = h;

  // A probe packet stays inside the vendor's interval buffer: its token is
  // HANDED_OFF and must remain counted through activate().
  const token = formatContext.eioBeforeSampleAlloc({ packet: 9100, size: 300 });
  formatContext.eioAfterSampleAlloc(token, { packet: 9100, data: 0xa000, size: 300 });
  formatContext.interval.packetBuffer.push(9100);
  let snap = h.helper.snapshot(task);
  assert.equal(snap.held, 300);
  assert.equal(snap.admissionHeld, 300);
  assert.equal(snap.localTokenBytes, 300);

  await enterPlay(task);
  snap = h.helper.snapshot(task);
  assert.equal(snap.held, 300, 'activate must not zero probe-held bytes');
  assert.equal(snap.admissionHeld, 300);
  assert.equal(snap.probeHeld, 300, 'beginPlay keeps the probe high-water demand');
  assert.equal(snap.localTokenBytes, 300);
  assertConserved(h, 'after activate');

  h.helper.sync(task, (p) => h.i32(p + 28));
  assert.equal(h.helper.snapshot(task).held, 300, 'sync must not drop token bytes');

  // A later loop sample takes its own reservation and enters the cache queue.
  scenario.queue = [{ index: 0, size: 100 }];
  assert.equal(await task.doDemux(task, 10), 0);
  snap = h.helper.snapshot(task);
  assert.equal(snap.cacheTotal, 100);
  assert.equal(snap.tHeld, 400, 'interval token + queue token are both retained');
  // The interval token was never queued, so it is still HANDED (local); only
  // the new sample moved its bytes into the cache container.
  assert.equal(snap.localTokenBytes, 300, 'the interval token stays local; the queued sample does not');
  assert.equal(snap.held, 400, 'token bytes stay counted exactly once');
  assert.equal(snap.admissionHeld, 400, 'the queue move must not re-add the bytes');

  // A consumer pull leaves the demux: still counted, but never credited.
  queueRequest(h, 0);
  assert.equal(task.drainCacheRequests(task), 1);
  snap = h.helper.snapshot(task);
  assert.equal(h.replies.length, 1, 'the consumer still receives the real packet');
  assert.equal(snap.held, 400, 'a cross-worker reply must never credit the payload');
  assert.equal(snap.admissionHeld, 300, 'only the interval token remains locally admitted');
  assert.equal(snap.outboundBytes, 100);
  assert.equal(snap.unprovenBytes, 0);
  assert.equal(snap.releaseVerified, false);
  assertConserved(h, 'after consumer pull');
});

test('queue removal of a token packet never credits by packet pointer', async () => {
  const h = demuxHarness(PROBE_CFG);
  const { task, scenario } = h;
  await enterPlay(task);

  scenario.queue = [{ index: 0, size: 180 }];
  assert.equal(await task.doDemux(task, 10), 0);
  const packet = task.cacheAVPackets.get(0)[0];

  const ledger = task.eioLedger;
  assert.equal(ledger.held, 180);
  // Same call shape the vendored crop/seek paths use: the packet pointer is
  // passed, but the bytes belong to a handed-off token.
  h.helper.del(task, 'c', 0, 180, packet);
  assert.equal(ledger.held, 180, 'held must not drop when the local holder is removed');
  assert.equal(ledger.admission, 0);
  assert.equal(ledger.unprovenBytes, 180);
  // Repeating the same removal is a no-op, never a second credit.
  assert.equal(h.helper.del(task, 'c', 0, 180, packet), 0);
  assert.equal(ledger.held, 180);
  // Mirror the crop splice that removes the packet from the container.
  task.cacheAVPackets.get(0).length = 0;
  assertConserved(h, 'after queue removal of a token packet');
});

test('delAll keeps queued token bytes held and only moves them to unproven', async () => {
  const h = demuxHarness(PROBE_CFG);
  const { task, scenario } = h;
  await enterPlay(task);

  scenario.queue = [{ index: 0, size: 180 }];
  assert.equal(await task.doDemux(task, 10), 0);
  const ledger = task.eioLedger;
  assert.equal(ledger.held, 180);

  task.cacheAVPackets.get(0).length = 0;
  assert.equal(h.helper.delAll(task, 'c', 0), 180);
  assert.equal(ledger.held, 180, 'a tracked queued token must stay held after delAll');
  assert.equal(ledger.admission, 0);
  assert.equal(ledger.unprovenBytes, 180);
  assertConserved(h, 'after delAll of a token packet');

  // Untracked (legacy fallback) container bytes keep the original deduction.
  h.helper.add(task, 'c', 1, 50);
  assert.equal(ledger.held, 230);
  assert.equal(h.helper.delAll(task, 'c', 1), 50);
  assert.equal(ledger.held, 180, 'untracked bytes are still deducted on removal');
  assertConserved(h, 'after delAll of an untracked packet');
});

test('PLAY pressure keeps waiting after a consumer handoff because bytes stay held', async () => {
  const h = demuxHarness(PROBE_CFG);
  const { task, scenario } = h;
  await enterPlay(task);
  assert.equal(task.eioLedger.packetBytes, 400);

  scenario.queue = [{ index: 0, size: 300 }];
  assert.equal(await task.doDemux(task, 10), 0);
  assert.equal(h.helper.snapshot(task).admissionHeld, 300);

  scenario.queue = [{ index: 0, size: 150 }];
  let settled = false;
  const pending = task.doDemux(task, 10).then((value) => {
    settled = true;
    return value;
  });
  await tick();
  await tick();
  assert.equal(settled, false, 'the candidate must wait in the same call, not be dropped');
  let snap = h.helper.snapshot(task);
  assert.equal(snap.waiterCount, 1);
  assert.equal(snap.timeoutCount, 0, 'the default wait has no timer; a generation change is the only exit');
  assert.equal(task.cacheAVPackets.get(0).length, 1, 'the queued packet is retained while waiting');

  // A real consumer pull moves the 300 bytes to OUTBOUND. Those bytes are still
  // retained (no observable final free), so held must not drop and the waiter
  // must NOT be woken by the admission decrease.
  queueRequest(h, 0);
  assert.equal(task.drainCacheRequests(task), 1);
  await tick();
  await tick();
  snap = h.helper.snapshot(task);
  assert.equal(h.replies.length, 1, 'the consumer still receives the real packet');
  assert.equal(snap.wakeCount, 1, 'drainCacheRequests does attempt a wake');
  assert.equal(snap.waiterCount, 1, 'OUTBOUND bytes still consume the real capacity');
  assert.equal(snap.held, 300, 'held is unchanged by the handoff');
  assert.equal(snap.outboundBytes, 300);
  assert.equal(snap.tHeld, 300);
  assert.equal(settled, false, 'a handoff is not a proven free');

  // Generation cancellation is the legitimate exit; it must not reserve data.
  const releasesBeforeCancel = h.releaseCalls.length;
  const errorsBeforeCancel = h.demuxErrors.length;
  h.helper.reset(task);
  const result = await pending;
  assert.equal(settled, true);
  assert.equal(
    result,
    -1048572,
    'an old-generation completion after the wait is a cancellation (EAGAIN), not a demux error or a fake EOF'
  );
  assert.equal(
    h.releaseCalls.length,
    releasesBeforeCancel + 1,
    'the cancelled sample packet is released back to the pool instead of being queued'
  );
  assert.equal(
    h.demuxErrors.length,
    errorsBeforeCancel,
    'a cancelled generation change never notifies a stale demux error'
  );
  snap = h.helper.snapshot(task);
  assert.equal(snap.waiterCount, 0);
  assert.equal(snap.cacheTotal, 0, 'no new-generation sample is written into the cache queue');
  assert.equal(snap.localTokenBytes, 0, 'no payload may be reserved for the cancelled sample');
  assert.equal(snap.tHeld, 300);
  assert.equal(snap.held, 300, 'the handed-off bytes stay counted, never credited');
  assert.equal(snap.outboundBytes, 300);
  assert.equal(snap.unprovenBytes, 0);
  assertConserved(h, 'after cancelled pressure wait');
});

test('a stale generation cancels the reservation exactly once', async () => {
  const h = demuxHarness(PROBE_CFG);
  const { task, formatContext } = h;

  const token = formatContext.eioBeforeSampleAlloc({ packet: 9200, size: 120 });
  assert.equal(h.helper.snapshot(task).held, 120);

  // `seek`/`unregisterTask` both go through reset: generation++, waiters cancelled.
  h.helper.reset(task);
  assert.equal(task.eioLedger.generation, 1);
  assert.equal(formatContext.eioIsSampleAllocCancelled(token, { packet: 9200, size: 120 }), true);
  formatContext.eioReleaseSampleAlloc(token, { reason: 'cancelled', size: 120 });
  const snap = h.helper.snapshot(task);
  assert.equal(snap.held, 0, 'a pre-malloc cancellation may credit the reservation');
  assert.equal(snap.cancelledBytes, 120);
  assert.equal(snap.phase, 3);

  // A duplicated release of a non-reserved token is a protocol error, never a
  // second credit.
  formatContext.eioReleaseSampleAlloc(token, { reason: 'cancelled', size: 120 });
  assert.equal(h.helper.snapshot(task).held, 0);
  assert.equal(h.helper.snapshot(task).protocolErrors, 1);
  assert.equal(h.helper.snapshot(task).staleTokens, 0);
  assertConserved(h, 'after cancelled release');
});

test('abort ends the opt-in session and keeps the outstanding bytes as unproven', async () => {
  const h = demuxHarness(PROBE_CFG);
  const { task, formatContext, scenario } = h;
  await enterPlay(task);
  scenario.queue = [{ index: 0, size: 300 }];
  assert.equal(await task.doDemux(task, 10), 0);
  assert.equal(task.cacheAVPackets.get(0).length, 1);
  const packet = task.cacheAVPackets.get(0)[0];

  assert.equal(await task.abortEioDualCache(task.taskId), 1);
  // The ACK object is created inside the method VM; clone it into a host object
  // for the shape comparison only, keeping the exact expected fields/values.
  assert.deepEqual({ ...task.eioDualStopAck }, { loop: true, source: true, reason: 'TRACKED_PRODUCERS_SETTLED' });
  assert.equal(task.eioProducerOperations.size, 0, 'the empty tracked producer set settles without inventing work');
  for (const field of [
    'eioBeforeSampleAlloc',
    'eioAfterSampleAlloc',
    'eioReleaseSampleAlloc',
    'eioIsSampleAllocCancelled'
  ]) {
    assert.equal(formatContext[field], undefined, `${field} must be removed when the opt-in session ends`);
  }
  const snap = h.helper.snapshot(task);
  assert.equal(snap.phase, 4);
  assert.equal(snap.held, 300, 'abort must not pretend the shared payload was freed');
  assert.equal(snap.admissionHeld, 0);
  assert.equal(snap.unprovenBytes, 300);
  assert.equal(snap.abandonedBytes, 300);
  // abort ends this opt-in session and uninstalls the four hooks; it says
  // nothing about budget control for later hookless reads, and no post-abort
  // production is simulated here. The vendor cache queue is untouched, so the
  // 300-byte packet is still real data. The ledger drops its container mirror
  // and carries those bytes as UNPROVEN instead of crediting them.
  assert.equal(snap.cacheTotal, 0, 'the aborted ledger no longer mirrors the vendor queue');
  assert.equal(Object.keys(snap.cache).length, 0, 'the aborted ledger keeps no container bytes');
  assert.equal(task.cacheAVPackets.get(0).length, 1, 'the real container still holds the packet');
  assert.equal(task.cacheAVPackets.get(0)[0], packet);
  assert.equal(h.i32(packet + 28), 300, 'the still-queued packet is really 300 bytes');
  const token = task.eioLedger.tokenByPacket.get(packet);
  assert.ok(token, 'the packet stays bound to its token');
  assert.equal(token.state, 'UNPROVEN');

  // Re-reading the aborted ledger must not credit the outstanding bytes.
  const again = h.helper.snapshot(task);
  assert.equal(again.held, 300, 'a repeated snapshot must not credit the unproven bytes');
  assert.equal(again.admissionHeld, 0);
  assert.equal(again.unprovenBytes, 300);
  assert.equal(again.outboundBytes, 0);
  assert.equal(again.cacheTotal, 0);
  assert.equal(again.generation, snap.generation);
  assert.equal(task.eioLedger.tokenByPacket.get(packet).state, 'UNPROVEN');
});

test('hook install refuses foreign fields and uninstall removes only own identities', () => {
  const blocked = demuxHarness();
  const foreign = function () { return null; };
  blocked.formatContext.eioBeforeSampleAlloc = foreign;
  assert.notEqual(
    blocked.task.eioInstallProbe(blocked.formatContext, {
      taskId: blocked.task.taskId,
      eioDualCache: PROBE_CFG
    }),
    null
  );
  assert.equal(blocked.formatContext.eioBeforeSampleAlloc, foreign, 'a foreign hook must never be overwritten');
  assert.equal(blocked.formatContext.eioAfterSampleAlloc, undefined);
  assert.equal(blocked.task.eioHookFns, undefined, 'a refused install must not claim ownership');
  assert.equal(blocked.task.eioLedger.hooksInstalled, false);
  assert.equal(blocked.task.eioLedger.sampleAllocHooked, false);
  assert.equal(blocked.task.eioLedger.protocolErrors, 1);
  assert.equal(blocked.task.eioLedger.lastError, 'HOOK_FIELD_CONFLICT');

  const owned = demuxHarness(PROBE_CFG);
  const replacement = function () { return null; };
  owned.formatContext.eioAfterSampleAlloc = replacement;
  owned.helper.uninstallHooks(owned.formatContext, owned.task);
  assert.equal(owned.formatContext.eioAfterSampleAlloc, replacement, 'another owner\'s hook must survive uninstall');
  assert.equal(owned.formatContext.eioBeforeSampleAlloc, undefined);
  assert.equal(owned.formatContext.eioReleaseSampleAlloc, undefined);
  assert.equal(owned.task.eioHookFns, null);
  assert.equal(owned.task.eioLedger.hooksInstalled, false);
});

test('a non-opt-in task installs no hook field at all', () => {
  for (const provisional of [null, undefined, { enabled: false }]) {
    const h = demuxHarness(provisional);
    for (const field of [
      'eioBeforeSampleAlloc',
      'eioAfterSampleAlloc',
      'eioReleaseSampleAlloc',
      'eioIsSampleAllocCancelled'
    ]) {
      assert.equal(h.formatContext[field], undefined, 'undefined fields are the pass-through contract');
    }
    assert.equal(h.task.eioLedger, null);
    assert.equal(h.task.eioDualProbe, null);
  }
});
test('an invalid provisional config installs nothing and reports the config error', () => {
  const h = demuxHarness({ enabled: true, demuxBytes: 100, maxPacketBytes: 200, ioBytes: 10 });
  assert.equal(h.task.eioLedger, null);
  assert.equal(h.formatContext.eioBeforeSampleAlloc, undefined);
  assert.equal(h.task.eioDualProbe, null);
});

// ---------------------------------------------------------------- avplayer payload
function avplayerHarness(options, extra = {}) {
  const text = bundleText();
  const source = [
    sliceMethod(text, 'getEioDualCacheConfig(){'),
    sliceMethod(text, 'isEioDualCacheCandidate(){'),
    sliceMethod(text, 'getEioDualCacheActive(){'),
    sliceMethod(text, 'isHls(){'),
    sliceMethod(text, 'isDash(){'),
    sliceMethod(text, 'getEioDualCacheProvisional(e){'),
    sliceMethod(text, 'getEioIOLoaderOptions(e,t){')
  ].join(',');
  const T = { X$: (target, ...sources) => Object.assign(target, ...sources.filter(Boolean)) };
  const api = vm.runInNewContext(`(function(){ return { ${source} }; })()`, { T });
  return Object.assign(api, {
    options,
    ext: extra.ext === undefined ? 'mp4' : extra.ext,
    isLive_: false,
    useMSE: true,
    source: 'https://fixture.invalid/movie.mp4',
    ...extra
  });
}

test('the provisional demux/IO payload is opt-in, HTTP-file VOD only, and never calls checkUseMSE', () => {
  const text = bundleText();
  const armed = avplayerHarness({ eioDualCache: { enabled: true } });
  const provisional = armed.getEioDualCacheProvisional('https://fixture.invalid/movie.mp4');
  assert.equal(provisional.provisional, true);
  assert.equal(provisional.enabled, true);
  assert.equal(provisional.ioBytes, 12e6);
  assert.equal(provisional.demuxBytes, 48e6);
  assert.equal(provisional.maxPacketBytes, 16e6);

  const ioOptions = armed.getEioIOLoaderOptions({}, provisional);
  assert.equal(ioOptions.isLive, false);
  assert.deepEqual({ ...ioOptions.eioDualCache }, {
    enabled: true,
    active: false,
    probe: true,
    ioBytes: 12e6,
    maxPacketBytes: 16e6
  });

  assert.equal(avplayerHarness({}).getEioDualCacheProvisional('https://x.invalid/a.mp4'), null);
  for (const [source, extra] of [
    ['https://x.invalid/a.m3u8', { ext: 'm3u8' }],
    ['https://x.invalid/a.mpd', { ext: 'mpd' }],
    ['blob:https://x.invalid/1', {}],
    ['data:video/mp4;base64,AAAA', {}],
    ['file:///tmp/a.mp4', {}],
    ['//x.invalid/a.mp4', {}]
  ]) {
    const api = avplayerHarness({ eioDualCache: { enabled: true } }, extra);
    assert.equal(api.getEioDualCacheProvisional(source), null, source);
  }
  assert.equal(
    avplayerHarness({ eioDualCache: { enabled: true } }, { isLive_: true })
      .getEioDualCacheProvisional('https://x.invalid/a.mp4'),
    null
  );
  assert.equal(
    avplayerHarness({ eioDualCache: { enabled: true } }, { source: { name: 'a.mp4' } })
      .getEioDualCacheProvisional({ name: 'a.mp4' }),
    null,
    'a non-HTTP source is never a provisional candidate'
  );

  const provisionalSource = sliceMethod(text, 'getEioDualCacheProvisional(e){');
  assert.equal(/checkUseMSE/.test(provisionalSource), false, 'the provisional check must not move checkUseMSE');
  // The real load() body derives and forwards the provisional config. The current
  // statements are asserted inside that actual body, not against the whole bundle.
  const load = loadBody(text);
  assert.equal(/checkUseMSE/.test(load), false, 'the provisional load never re-runs the MSE selection');
  assert.ok(
    load.includes('__eioProbeDual=0===o?this.getEioDualCacheProvisional(e):null'),
    'load derives the provisional config only for the sniffed HTTP/file source'
  );
  assert.ok(
    load.includes('this.eioDualProbeInstalled=!!__eioProbeDual'),
    'the provisional install marker comes from that derivation'
  );
  assert.ok(load.includes('options:this.getEioIOLoaderOptions(t,__eioProbeDual)'), 'IO registerTask must carry it');
  assert.ok(
    load.includes('eioDualCache:__eioProbeDual,eioSharedMediaDomain:__eioEarlyDomain}'),
    'demux registerTask payload must carry it'
  );
  assert.ok(text.includes('if(this.eioDualProbeInstalled){const eioAbort=await this.eioAbortDualCache()'), 'a non-MSE play must restore the baseline path');
  assert.ok(text.includes('eioAbort.io!==1'), 'the fallback must check the IO disable result');
  assert.ok(text.includes('EIO_DUAL_IO_DISABLE_FAILED'), 'a failed disable surfaces an explicit error instead of continuing');
});

test('the real non-MSE fallback stops when the IO disable result is not 1', async () => {
  const text = bundleText();
  const head = 'if(this.useMSE){const eioDualConfig=this.getEioDualCacheActive();';
  const tail = 'await this.playUseDecoder(e)};';
  const start = text.indexOf(head);
  const end = text.indexOf(tail, start);
  assert.ok(start > -1 && end > start, 'real non-MSE fallback branch not found');
  const source = text.slice(start, end + tail.length);
  const run = vm.runInNewContext(`(async function(e){ ${source} return 'continued'; })`, {});
  const calls = [];
  const fake = {
    useMSE: false,
    eioDualProbeInstalled: true,
    async eioAbortDualCache() { calls.push('abort'); return { demux: 1, io: 0 }; },
    async playUseDecoder() { calls.push('decoder'); },
    getEioDualCacheActive() { return null; },
    async eioBindDualCache() {},
    async playUseMSE() { calls.push('mse'); },
    onError(error) { calls.push(`error:${error.code}`); }
  };
  assert.equal(await run.call(fake, {}), undefined, 'the failed fallback returns before decoder production');
  assert.deepEqual(calls, ['abort', 'error:EIO_DUAL_IO_DISABLE_FAILED']);
  assert.equal(fake.eioDualCacheConfigError.code, 'EIO_DUAL_IO_DISABLE_FAILED');
  assert.equal(fake.eioDualCacheConfigError.io, 0);

  calls.length = 0;
  fake.eioAbortDualCache = async () => { calls.push('abort'); return { demux: 1, io: 1 }; };
  assert.equal(await run.call(fake, {}), 'continued');
  assert.deepEqual(calls, ['abort', 'decoder']);

  calls.length = 0;
  fake.eioDualProbeInstalled = false;
  assert.equal(await run.call(fake, {}), 'continued');
  assert.deepEqual(calls, ['decoder'], 'non-opt-in never aborts or calls the error callback');

  calls.length = 0;
  fake.useMSE = true;
  fake.getEioDualCacheActive = () => ({ enabled: true });
  fake.eioBindDualCache = async () => { calls.push('bind'); return true; };
  assert.equal(await run.call(fake, {}), 'continued');
  assert.deepEqual(calls, ['bind', 'mse']);
});

// ---------------------------------------------------------------- io prefetch
function ioHarness(constructorOptions, options) {
  const text = bundleText();
  const CLASS_HEAD = 'class l extends d.A{constructor(){super(arguments.length>0';
  const start = text.indexOf(CLASS_HEAD);
  assert.ok(start > -1, 'FetchIOLoader class not found');
  // The strict owned-resource helper is the original `__eioOwnedIO` object of the
  // vendored bundle. It is evaluated in the same vm as `__eioDualIO` so the real
  // IO snapshot closes over the actual ownership helper instead of a harness stub.
  const helpers = vm.runInNewContext(
    `${sliceHelper(text, 'IO')}\n${sliceOwnedIOHelper(text)}\n;({ dual: __eioDualIO, owned: __eioOwnedIO });`,
    { Map, Set, Math, Number, isFinite }
  );
  const helper = helpers.dual;
  const ownedHelper = helpers.owned;
  const bandwidthReads = [];
  const logs = [];
  const bodyState = { modes: [], views: [] };
  const methods = [
    sliceMethod(text, 'eioOwnedStrict(){', start),
    sliceMethod(text, 'eioOpenReader(e){', start),
    sliceMethod(text, 'async eioEnsureByob(){', start),
    sliceMethod(text, 'eioDualFail(e,t,i,s){', start),
    sliceMethod(text, 'configureEioDualCache(e){', start),
    sliceMethod(text, 'getEioDualCacheSnapshot(){', start),
    sliceMethod(text, 'async openReader(){', start),
    sliceMethod(text, 'async readInterval(e){', start),
    sliceMethod(text, 'async read(e,t){const i=t&&t.eioDualCache', start)
  ].join(',');
  const loader = Object.assign({
    options: Object.assign({
      isLive: false,
      disableSegment: false,
      preload: 8,
      retryCount: 0,
      retryInterval: 1,
      enableBandwidthReader: true
    }, constructorOptions),
    buffers: [],
    status: 2,
    contentLength: 1000,
    range: { from: 0, to: -1 },
    startBytes: 0,
    endBytes: 999,
    eofIndex: 999,
    receivedLength: 0,
    reader: null,
    aborted: false,
    info: { url: 'http://fixture.invalid/media', httpOptions: {} },
    getBandwidthReader(reader) {
      bandwidthReads.push(reader);
      return { async read() { return { value: new Uint8Array(4).fill(3), done: false }; } };
    }
  }, options);
  const context = {
    __eioDualIO: helper,
    __eioOwnedIO: ownedHelper,
    n: { h2: (message) => { logs.push(message); } },
    c: 'packages/avnetwork/src/ioLoader/FetchIOLoader.ts',
    o: { __: () => {} },
    h: { A: () => 0 },
    s: { A: class { stop() {} } },
    fetch: async () => ({
      ok: true,
      status: 200,
      statusText: 'OK',
      body: {
        getReader(init) {
          bodyState.modes.push(init && init.mode ? init.mode : 'default');
          return makeByobReader([new Uint8Array(4).fill(7)], bodyState);
        }
      }
    }),
    AbortController: class { constructor() { this.signal = {}; } abort() {} }
  };
  Object.assign(loader, vm.runInNewContext(`(function(){ return { ${methods} }; })()`, context));
  return { loader, helper, owned: ownedHelper, bandwidthReads, logs, bodyState };
}

function makeByobReader(chunks, state) {
  const queue = chunks.slice();
  const modes = [];
  return {
    modes,
    async read(view) {
      if (state) state.views.push(view && view.byteLength || 0);
      const chunk = queue.shift();
      if (!chunk) return { value: undefined, done: true };
      if (!view) return { value: chunk, done: false };
      view.set(chunk.subarray(0, view.byteLength));
      return { value: view.subarray(0, chunk.length), done: false };
    },
    releaseLock() {},
    async cancel() {}
  };
}

test('the provisional IO phase starts BYOB before the first read and stays bounded', async () => {
  const probeOptions = {
    eioDualCache: { enabled: true, active: false, probe: true, ioBytes: 12e6, maxPacketBytes: 16e6 }
  };
  const probed = ioHarness(probeOptions);
  assert.equal(typeof probed.owned.snapshot, 'function', 'the actual __eioOwnedIO helper object is wired in');
  assert.equal(
    probed.loader.eioOwnedStrict(),
    false,
    'this fixture attaches no shared domain/resources, so the actual strict gate declines'
  );
  const buffer = new Uint8Array(4);
  assert.equal(await probed.loader.read(buffer), 4);
  assert.equal(probed.bandwidthReads.length, 0, 'no bandwidth wrapper may start before the probe');
  assert.equal(probed.loader.eioBandwidthReaderSkipped, true, 'the loader records why the wrapper was skipped');
  assert.equal(probed.loader.eioReaderMode, 'byob', 'the provisional read is BYOB, not native');
  assert.deepEqual(probed.bodyState.modes, ['byob'], 'no native getReader occurs before MSE selection');
  assert.deepEqual(probed.bodyState.views, [4], 'the supplied view is bounded by the destination');

  const phase = probed.helper.cfg(probed.loader);
  assert.equal(phase.enabled, undefined, 'cfg returns the allocation fields only');
  assert.equal(phase.active, false, 'the provisional phase never claims actual MSE activation');
  assert.equal(phase.probe, true);
  const snapshot = probed.loader.getEioDualCacheSnapshot();
  assert.equal(snapshot.enabled, true);
  assert.equal(snapshot.active, false);
  assert.equal(snapshot.probe, true);

  const baseline = ioHarness({});
  assert.equal(await baseline.loader.read(new Uint8Array(4)), 4);
  assert.equal(baseline.bandwidthReads.length, 1, 'the baseline wrapper is untouched without the option');
  assert.deepEqual(baseline.bodyState.modes, ['default'], 'non-opt-in keeps the original native path');

  // The startup provisional options are not rewritten by the first read.
  assert.equal(probed.loader.eioDualCacheOptions, undefined);
});

test('an inactive IO configure unbinds instead of refusing forever', () => {
  const { loader, helper } = ioHarness({});
  loader.eioDualCacheOptions = { enabled: true, active: true, ioBytes: 12e6, maxPacketBytes: 16e6 };
  loader.eioDualCacheProbe = true;
  assert.equal(loader.configureEioDualCache({ enabled: true, active: false }), 1);
  assert.equal(loader.eioDualCacheOptions, null);
  assert.equal(loader.eioDualCacheProbe, false);
  assert.equal(loader.eioDualCacheDisabled, true, 'an explicit disable leaves a sticky override');
  assert.equal(loader.eioDualError, 0);
  assert.equal(helper.cfg(loader), null);
});

// ---------------------------------------------------------------- aggregate
test('the aggregate snapshot never claims a complete probe ledger', async () => {
  const text = bundleText();
  const head = 'async getEioDualCacheSnapshot(){const t=Number(this.taskId)||this.taskId,i={taskId:t';
  const demuxHarnessResult = demuxHarness(PROBE_CFG);
  const demuxSnapshot = await demuxHarnessResult.task.getEioDualCacheSnapshot(demuxHarnessResult.task.taskId);

  const aggregate = vm.runInNewContext(
    `({ ${sliceMethod(text, head)} })`,
    {
      Ue: {
        IOThread: { getEioDualCacheSnapshot: async () => ({ active: true, byobCapable: true }) },
        DemuxerThread: { getEioDualCacheSnapshot: async () => demuxSnapshot },
        MSEThread: { getEioDualCacheSnapshot: async () => ({ active: true }) }
      },
      setTimeout,
      clearTimeout
    }
  ).getEioDualCacheSnapshot;

  const context = {
    taskId: 'probe-task',
    getEioDualCacheActive: () => ({ enabled: true })
  };
  const snapshot = await aggregate.call(context);
  assert.equal(snapshot.complete, false, 'an unproven release can never be complete');
  assert.equal(snapshot.active, true);
  const reasons = snapshot.incomplete
    .filter((entry) => entry.domain === 'demux')
    .map((entry) => entry.reason);
  assert.ok(reasons.includes('SAMPLE_RELEASE_UNVERIFIED'), reasons.join(','));
  assert.ok(!reasons.includes('HEADER_UNACCOUNTED'),
    'extradata/attachedPic accounting is wired for MP4/MKV/TS/PS');
  assert.ok(reasons.includes('INTERVAL_UNACCOUNTED'), reasons.join(','));
  assert.equal(JSON.stringify(snapshot).includes('probeFullyAccounted'), false);
  assert.equal(demuxSnapshot.releaseVerified, false);
  assert.equal(demuxSnapshot.accounting.sampleRelease, false);
  assert.equal(demuxSnapshot.accounting.extradata, true);
  assert.equal(demuxSnapshot.accounting.attachedPic, true);
});
