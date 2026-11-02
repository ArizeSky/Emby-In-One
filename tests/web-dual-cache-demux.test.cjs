const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// These cases run the *patched vendored* DemuxPipeline code: the inline
// `__eioDualDemux` ledger factory and the real methods doDemux /
// drainCacheRequests / doAttachedPicture / croppingAVPacketQueue /
// unregisterTask, sliced out of public/vendor/web/libmedia/avplayer.js.
const BUNDLE = path.join(__dirname, '../public/vendor/web/libmedia/avplayer.js');
// Async cases fail fast instead of hanging the file. A timeout is a failure,
// not a substitute for the real assertions.
const ASYNC = { timeout: 2000 };

function bundleText() {
  return fs.readFileSync(BUNDLE, 'utf8');
}

// Demux methods must be sliced from the real DemuxPipeline factory only. The
// whole bundle also contains unrelated AudioRender methods with the same names
// (notably unregisterTask), so a first-match search over the full bundle picks
// the wrong implementation.
function demuxText() {
  const text = bundleText();
  const begin = '/*__EIO_DUAL_CACHE_DEMUX_BEGIN__*/';
  const end = '},22105:(';
  const start = text.indexOf(begin);
  const stop = text.indexOf(end, start + begin.length);
  assert.ok(start > -1 && stop > start, 'Demux factory region is missing from the bundle');
  return text.slice(start, stop);
}

function sliceHelper(text, name) {
  const begin = `/*__EIO_DUAL_CACHE_${name}_BEGIN__*/`;
  const end = `/*__EIO_DUAL_CACHE_${name}_END__*/`;
  const start = text.indexOf(begin);
  const stop = text.indexOf(end);
  assert.ok(start > -1 && stop > start, `${name} budget helper is missing from the bundle`);
  return text.slice(start + begin.length, stop);
}

// Naive brace matcher; the sliced methods only contain balanced `${...}` and
// `{}` pairs, so a depth counter is enough to find the method end.
function sliceMethod(text, head) {
  const start = text.indexOf(head);
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
      if (seen && depth === 0) {
        return text.slice(start, i + 1);
      }
    }
  }
  throw new Error(`unbalanced method ${head}`);
}

function buildTask(options = {}) {
  const mem = new Map();
  const releaseCalls = [];
  const demuxErrors = [];
  const replies = [];
  const logs = [];
  const ipcRequests = [];
  const scenario = { queue: [], error: 0 };
  let pointer = 2000;
  let clockMs = 0;
  const fakeClock = {
    now: () => clockMs,
    advance(ms) {
      clockMs += ms;
    }
  };

  const i32 = (addr) => {
    const v = mem.get(addr);
    return typeof v === 'number' ? v : 0;
  };
  const i64 = (addr) => {
    const v = mem.get(addr);
    return typeof v === 'bigint' ? v : 0n;
  };
  const allocPacket = () => {
    pointer += 64;
    return pointer;
  };
  const streams = (options.streams || [{ codecType: 0, codecId: 27 }]).map((s, index) => ({
    index,
    codecpar: { codecType: s.codecType, codecId: s.codecId ?? 2 }
  }));

  const fixedBytes = 2 * 1048576;
  const task = {
    stats: 0,
    eioOptions: {},
    eioLedger: null,
    eioFixedBytes: fixedBytes,
    demuxEnded: false,
    mainTaskId: true,
    formatContext: { streams, destroy: async () => {} },
    ioReader: { flags: 0, abort() {} },
    iBuffer: 0,
    oBuffer: 0,
    eioProducerOperations: new Set(),
    leftIPCPort: {
      // Fake LOOPBACK IPC transport. The real strict teardown asks IO to abort
      // its owned reads; only that request is acknowledged with the explicit 1
      // the source requires, every other method gets an explicit 0 (never a
      // success). `onIpcRequest` lets a case observe the state at request time.
      async request(method, payload) {
        const entry = { method, payload };
        ipcRequests.push(entry);
        if (typeof options.onIpcRequest === 'function') options.onIpcRequest(entry);
        return method === 'eioAbortOwnedReads' ? (options.ipcAbortAck ?? 1) : 0;
      },
      destroy() {}
    },
    controlIPCPort: {
      notify(method, params) {
        demuxErrors.push({ method, params });
      },
      destroy() {}
    },
    rightIPCPorts: new Map(),
    cacheAVPackets: new Map(),
    cacheRequests: new Map(),
    pendingAVPackets: new Map(),
    streamIndexFlush: new Map(),
    tasks: new Map(),
    avpacketPool: {
      alloc() {
        return allocPacket();
      },
      release(packet) {
        releaseCalls.push(packet);
        mem.delete(packet + 28);
        mem.delete(packet + 32);
      }
    },
    loop: {
      stopped: false,
      stop() {
        this.stopped = true;
      },
      start() {},
      emptyTask() {},
      async stopBeforeNextTick() {},
      destroy() {}
    },
    replyAVPacket(owner, port, request, packet) {
      replies.push({ owner, port, request, packet });
    }
  };

  const text = bundleText();
  const demux = demuxText();
  const helper = vm.runInNewContext(
    `${sliceHelper(text, 'DEMUX')}\n;__eioDualDemux;`,
    { Map, Math, Number, isFinite, o: { f: { 15: i32 } }, performance: { now: () => clockMs } }
  );
  if (options.demuxBytes !== undefined) {
    task.eioLedger = helper.make({
      demuxBytes: options.demuxBytes + fixedBytes,
      maxPacketBytes: options.maxPacketBytes ?? 16e6,
      fixedBytes,
      maxForwardSeconds: 120
    });
  }

  const methods = [
    sliceMethod(demux, 'drainCacheRequests(e){'),
    sliceMethod(demux, 'async doDemux(e,t){'),
    sliceMethod(demux, 'doAttachedPicture(e){'),
    sliceMethod(demux, 'async croppingAVPacketQueue(e,t){'),
    sliceMethod(demux, 'async changeConnectStream(e,t,i){'),
    // Current strict teardown helpers: the real unregisterTask asks IO to abort
    // owned reads, proves tracked producers stopped, then resets and frees.
    sliceMethod(demux, 'eioFreeFixedField('),
    sliceMethod(demux, 'eioReleaseOwnedFixed('),
    sliceMethod(demux, 'async eioAbortOwnedReads('),
    sliceMethod(demux, 'async eioRequireProducersStopped('),
    sliceMethod(demux, 'async eioAwaitProducers('),
    sliceMethod(demux, 'async unregisterTask(e){')
  ].join(',');

  const context = {
    setTimeout,
    clearTimeout,
    __eioDualDemux: helper,
    o: { f: { 15: i32, 17: i64, 20: () => 0, 25: () => 0 } },
    E: {
      Cj: async (formatContext, packet) => {
        const spec = scenario.queue.shift();
        if (!spec) return scenario.error || -1;
        mem.set(packet + 28, spec.size);
        mem.set(packet + 32, spec.index);
        mem.set(packet + 36, spec.flags === undefined ? 1 : spec.flags);
        mem.set(packet + 16, BigInt(spec.dts || 0));
        mem.set(packet + 8, BigInt(spec.pts || 0));
        return 0;
      }
    },
    n: { M: { 15() {}, 16() {}, 17() {} } },
    u: { Mr: (value) => value, i0: {} },
    d: { rU: () => 0, iI: () => 0, Ow() {}, rN() {} },
    b: { s3: () => 0, Mr() {}, lW() {} },
    c: { sY: () => 0 },
    P: { T9: (a, b) => (a > b ? a : b) },
    _: { __: (list, fn) => list.forEach(fn) },
    __eioDualDemuxSize: (p) => (p > 0 ? Math.max(i32(p + 28), 0) : 0),
    M: { z3() {}, R8() {}, pq() {}, Yz(...args) { logs.push(args); }, h2() {} },
    A: { sL: () => false },
    v: { sL: () => false },
    y: { sL: () => false },
    g: { i0: {} },
    F: -1,
    $: 'test'
  };

  const runnable = vm.runInNewContext(`(function(){ return { ${methods} }; })()`, context);
  Object.assign(task, runnable);
  task.tasks.set(task, task);
  return { task, helper, mem, scenario, releaseCalls, demuxErrors, replies, logs, i32, fakeClock, ipcRequests };
}

function ledgerSum(task) {
  let sum = 0;
  for (const value of task.eioLedger.c.values()) sum += value;
  for (const value of task.eioLedger.p.values()) sum += value;
  return sum;
}

function containerBytes(task, kind, index, i32) {
  const list = kind === 'c' ? task.cacheAVPackets.get(index) : task.pendingAVPackets.get(index);
  if (!list) return 0;
  let sum = 0;
  for (const packet of list) sum += Math.max(i32(packet + 28), 0);
  return sum;
}

test('vendored demux ledger tracks cache/pending/subtitle/ALL and pull drains exactly', ASYNC, async () => {
  const env = buildTask({
    streams: [{ codecType: 0, codecId: 2 }, { codecType: 1, codecId: 2 }, { codecType: 3, codecId: 2 }],
    demuxBytes: 48e6,
    maxPacketBytes: 16e6
  });
  const { task, demuxErrors, replies } = env;

  env.scenario.queue = [
    { index: 0, size: 1000 },
    { index: 1, size: 400 },
    { index: 2, size: 50 }
  ];
  // stream 0 and 1 have connected consumers, stream 2 is pending
  task.cacheAVPackets.set(0, []);
  task.cacheAVPackets.set(1, []);
  task.pendingAVPackets.set(2, []);
  for (let i = 0; i < 3; i++) assert.equal(await task.doDemux(task, 10), 0);

  assert.equal(task.eioLedger.held, 1450);
  assert.equal(ledgerSum(task), 1450);
  assert.equal(task.eioLedger.c.get(0), 1000);
  assert.equal(task.eioLedger.c.get(1), 400);
  assert.equal(task.eioLedger.p.get(2), 50);
  assert.equal(demuxErrors.length, 0);

  const request = { req: 1 };
  task.cacheRequests.set(0, request);
  assert.equal(task.drainCacheRequests(task), 1);
  assert.equal(replies.length, 1);
  assert.equal(replies[0].request, request);
  assert.equal(task.eioLedger.held, 450);
  assert.equal(ledgerSum(task), 450);
  assert.equal(task.cacheAVPackets.get(0).length, 0);
});

test('vendored subtitle overflow and pending trimming release packets and debit the ledger', ASYNC, async () => {
  const env = buildTask({
    streams: [{ codecType: 3, codecId: 2 }, { codecType: 3, codecId: 2 }],
    demuxBytes: 48e6,
    maxPacketBytes: 16e6
  });
  const { task, releaseCalls } = env;
  task.cacheAVPackets.set(0, []);
  task.pendingAVPackets.set(1, []);
  for (let i = 0; i < 11; i++) {
    env.scenario.queue = [{ index: 0, size: 100 }];
    await task.doDemux(task, 10);
    env.scenario.queue = [{ index: 1, size: 100 }];
    await task.doDemux(task, 10);
  }
  assert.equal(task.cacheAVPackets.get(0).length, 10);
  assert.equal(task.pendingAVPackets.get(1).length, 10);
  assert.equal(containerBytes(task, 'c', 0, env.i32), 1000);
  assert.equal(containerBytes(task, 'p', 1, env.i32), 1000);
  assert.equal(task.eioLedger.held, 2000);
  assert.equal(ledgerSum(task), 2000);
  assert.equal(releaseCalls.length, 2);
});

test('vendored STREAM_INDEX_ALL cache push is counted under the -1 key', ASYNC, async () => {
  const env = buildTask({ streams: [{ codecType: 0, codecId: 2 }], demuxBytes: 48e6, maxPacketBytes: 16e6 });
  const { task } = env;
  task.rightIPCPorts.set(-1, {});
  task.cacheAVPackets.set(-1, []);
  env.scenario.queue = [{ index: 7, size: 300 }];
  await task.doDemux(task, 10);
  assert.equal(task.eioLedger.c.get(-1), 300);
  assert.equal(task.eioLedger.held, 300);
  assert.equal(ledgerSum(task), 300);
});

test('vendored doAttachedPicture accounts for the packets it pushes', ASYNC, async () => {
  const env = buildTask({ streams: [{ codecType: 0, codecId: 2 }], demuxBytes: 48e6, maxPacketBytes: 16e6 });
  const { task } = env;
  const pointer = task.avpacketPool.alloc();
  env.mem.set(pointer + 28, 512);
  task.avpacketPool.alloc = () => pointer;
  task.formatContext.streams = [
    {
      index: 4,
      disposition: 1024,
      attachedPic: { data: 1 },
      codecpar: { codecType: 0, codecId: 2 }
    }
  ];
  task.cacheAVPackets.set(4, []);
  task.doAttachedPicture(task);
  assert.equal(task.cacheAVPackets.get(4).length, 2);
  assert.equal(task.cacheAVPackets.get(4)[1], -1048576);
  assert.equal(task.eioLedger.c.get(4), 512);
  assert.equal(task.eioLedger.held, 512);
});

test('vendored doDemux rejects an oversized single packet with the existing demuxError channel', ASYNC, async () => {
  const env = buildTask({ demuxBytes: 48e6, maxPacketBytes: 16e6 });
  const { task, demuxErrors, releaseCalls } = env;
  task.cacheAVPackets.set(0, []);
  env.scenario.queue = [{ index: 0, size: 16e6 + 1 }];
  const result = await task.doDemux(task, 10);
  assert.equal(result, undefined);
  assert.equal(releaseCalls.length, 1);
  assert.equal(task.cacheAVPackets.get(0).length, 0);
  assert.equal(task.eioLedger.held, 0);
  assert.equal(task.loop.stopped, true);
  assert.equal(demuxErrors.length, 1);
  assert.equal(demuxErrors[0].method, 'demuxError');
  assert.equal(demuxErrors[0].params.code, 'DEMUX_PACKET_TOO_LARGE');
  assert.equal(demuxErrors[0].params.need, 16e6 + 1);
});

test('vendored doDemux refuses to store past the partition, keeps held bytes, and never truncates', ASYNC, async () => {
  const env = buildTask({ demuxBytes: 1000, maxPacketBytes: 800 });
  const { task, demuxErrors } = env;
  assert.equal(task.eioLedger.high, 2 * 1048576 + 1000, 'the fixed reader buffers are inside the partition');
  assert.equal(task.eioLedger.packetBytes, 1000, 'only the remaining packet partition is scalable');
  assert.equal(task.eioLedger.fixedBytes, 2 * 1048576);
  task.cacheAVPackets.set(0, []);
  env.scenario.queue = [{ index: 0, size: 600 }];
  await task.doDemux(task, 10);
  assert.equal(task.eioLedger.held, 600);
  env.scenario.queue = [{ index: 0, size: 600 }];
  const result = await task.doDemux(task, 10);
  assert.equal(result, undefined);
  assert.equal(task.eioLedger.held, 600);
  assert.equal(containerBytes(task, 'c', 0, env.i32), 600);
  assert.equal(demuxErrors[0].params.code, 'DEMUX_BUDGET_REJECTED');
  assert.equal(task.loop.stopped, true);
});

test('a pending pull is answered from held packets even at the byte ceiling', ASYNC, async () => {
  const env = buildTask({ demuxBytes: 1000, maxPacketBytes: 800 });
  const { task, replies } = env;
  task.cacheAVPackets.set(0, []);
  env.scenario.queue = [{ index: 0, size: 600 }];
  await task.doDemux(task, 10);
  const request = { req: 'needed' };
  task.cacheRequests.set(0, request);
  env.scenario.queue = [{ index: 0, size: 600 }];
  await task.doDemux(task, 10);
  assert.equal(replies.length, 1);
  assert.equal(replies[0].packet > 0, true);
  assert.equal(task.eioLedger.held, 600, 'direct replies never enter the resident ledger');
});

test('latch stops reading at the high watermark and only resumes at <=0.8*quota', () => {
  const env = buildTask({ demuxBytes: 48e6, maxPacketBytes: 16e6 });
  const { task, helper } = env;
  helper.add(task, 'c', 0, 32e6);
  assert.equal(helper.gate(task, 10), 1);
  helper.add(task, 'c', 0, 16e6);
  assert.equal(helper.gate(task, 10), 2);
  assert.equal(task.eioLedger.latched, true);
  helper.del(task, 'c', 0, 5e6);
  assert.equal(helper.gate(task, 10), 2, 'latched until the resume watermark');
  helper.del(task, 'c', 0, 5e6);
  assert.equal(helper.gate(task, 10), 2, 'still above the resume watermark');
  assert.equal(task.eioLedger.latched, true);
  helper.del(task, 'c', 0, 6e6);
  assert.equal(helper.gate(task, 10), 1);
  assert.equal(task.eioLedger.latched, false);
});

test('the pre-read reservation keeps a full max-sized packet admissible', () => {
  const env = buildTask({ demuxBytes: 48e6, maxPacketBytes: 16e6 });
  const { task, helper } = env;
  // readCeil = 48e6 - 16e6 = 32e6
  helper.add(task, 'c', 0, 32e6 + 1);
  assert.equal(helper.gate(task, 10), 2);
  assert.equal(task.eioLedger.latched, false);
  helper.del(task, 'c', 0, 1);
  assert.equal(helper.gate(task, 10), 1);
});

test('ledger accepts the p bucket, repeated del is idempotent and move preserves bytes', () => {
  const env = buildTask({ demuxBytes: 48e6, maxPacketBytes: 16e6 });
  const { task, helper } = env;
  assert.equal(helper.bucket(task, 'p'), task.eioLedger.p);
  assert.equal(helper.bucket(task, 1), task.eioLedger.p);
  assert.equal(helper.bucket(task, 'c'), task.eioLedger.c);
  assert.equal(helper.bucket(task, 'x'), null);

  assert.equal(helper.add(task, 'p', 9, 250), 250);
  assert.equal(helper.add(task, 'p', 9, 50), 50);
  assert.equal(task.eioLedger.held, 300);
  assert.equal(helper.del(task, 'p', 9, 100), 100);
  assert.equal(task.eioLedger.p.get(9), 200);
  assert.equal(helper.del(task, 'p', 9, 1000), 200);
  assert.equal(task.eioLedger.p.has(9), false);
  assert.equal(task.eioLedger.held, 0);
  assert.equal(helper.del(task, 'p', 9, 10), 0, 'a repeated del of an absent key is a no-op');
  assert.equal(task.eioLedger.held, 0);

  helper.add(task, 'c', 0, 120);
  assert.equal(helper.move(task, 'c', 0, 'c', 0), 120, 'same key move reports but does not double count');
  assert.equal(task.eioLedger.held, 120);
  assert.equal(task.eioLedger.c.get(0), 120);
  assert.equal(helper.move(task, 'c', 0, 'c', 1), 120, 'different key in the same bucket');
  assert.equal(task.eioLedger.c.has(0), false);
  assert.equal(task.eioLedger.c.get(1), 120);
  assert.equal(helper.move(task, 'c', 1, 'p', 1), 120, 'different bucket move');
  assert.equal(task.eioLedger.c.has(1), false);
  assert.equal(task.eioLedger.p.get(1), 120);
  assert.equal(task.eioLedger.held, 120, 'moves never debit held');
  assert.equal(helper.move(task, 'c', 99, 'p', 99), 0, 'missing source is a no-op');
  assert.equal(task.eioLedger.held, 120);
});

test('activate seeds the ledger from pre-existing cache/pending containers', () => {
  const env = buildTask();
  const { task, helper, mem, i32 } = env;
  assert.equal(task.eioLedger, null);
  const cached = task.avpacketPool.alloc();
  const pending = task.avpacketPool.alloc();
  mem.set(cached + 28, 120);
  mem.set(pending + 28, 200);
  task.cacheAVPackets.set(0, [cached]);
  task.pendingAVPackets.set(1, [pending]);

  helper.activate(task, {
    enabled: true,
    demuxBytes: 2 * 1048576 + 1000,
    maxPacketBytes: 100,
    ioBytes: 10,
    maxForwardSeconds: 120
  });
  assert.equal(task.eioLedger.held, 320);
  assert.equal(task.eioLedger.c.get(0), 120);
  assert.equal(task.eioLedger.p.get(1), 200);
  assert.equal(task.eioLedger.high, 2 * 1048576 + 1000);
  assert.equal(task.eioLedger.packetBytes, 1000);
  assert.equal(task.eioLedger.low, Math.min(Math.floor((2 * 1048576 + 1000) * 0.8), 2 * 1048576 + 900));
  assert.equal(task.eioLedger.readCeil, 2 * 1048576 + 900);

  const seen = [];
  assert.equal(
    helper.sync(task, (packet) => {
      seen.push(packet);
      return i32(packet + 28);
    }),
    320
  );
  assert.deepEqual(seen, [cached, pending]);
  assert.equal(seen.includes(cached + 28), false, 'sync passes the packet pointer, the size wrapper adds +28');
  assert.equal(seen.includes(pending + 28), false, 'sync passes the packet pointer, the size wrapper adds +28');
});

test('ordinary pressure during a 3s/60s consumer pause is not a fatal starvation', () => {
  const env = buildTask({ demuxBytes: 1000, maxPacketBytes: 100 });
  const { task, helper, demuxErrors, releaseCalls, fakeClock } = env;
  helper.add(task, 'c', 0, 1000);
  task.cacheRequests.set(0, { waiting: true });
  assert.equal(fakeClock.now(), 0, 'the clock starts at a real zero');
  for (let i = 0; i < 100; i++) assert.equal(helper.gate(task, 10), 2);
  assert.equal(demuxErrors.length, 0, 'rapid ticks at t=0 do not fabricate an error');
  assert.equal(task.loop.stopped, false);

  fakeClock.advance(3000);
  for (let i = 0; i < 100; i++) assert.equal(helper.gate(task, 10), 2);
  assert.equal(demuxErrors.length, 0, '3s of pending demand is ordinary capacity pressure');
  assert.equal(task.loop.stopped, false, 'ordinary pressure never stops the demux loop');
  assert.equal(task.demuxEnded, false);

  fakeClock.advance(57000);
  assert.equal(fakeClock.now(), 60000);
  for (let i = 0; i < 100; i++) assert.equal(helper.gate(task, 10), 2);
  assert.equal(demuxErrors.length, 0, '60s of pending demand is still not a fatal error');
  assert.equal(task.loop.stopped, false);
  assert.equal(task.eioLedger.held, 1000, 'no packet is evicted to relieve pressure');
  assert.equal(ledgerSum(task), 1000, 'byte ownership stays stable');
  assert.equal(releaseCalls.length, 0, 'no owned packet is released');
  assert.equal(task.eioLedger.errors, 0);
  assert.equal(task.eioLedger.latched, true);
  assert.ok(task.eioLedger.starve > 0, 'starve diagnostics are still counted');
  assert.equal(task.eioLedger.starveSince, 0, 'diagnostic time is not a failure deadline');

  assert.equal(helper.del(task, 'c', 0, 201), 201);
  assert.equal(helper.gate(task, 10), 1, 'a real release below the low watermark resumes reads');
  assert.equal(task.eioLedger.latched, false);
  assert.equal(task.eioLedger.starve, 0);
  assert.equal(demuxErrors.length, 0);
  assert.equal(task.loop.stopped, false);

  helper.add(task, 'c', 0, 201);
  assert.equal(helper.gate(task, 10), 2);
  fakeClock.advance(60000);
  assert.equal(helper.gate(task, 10), 2);
  assert.equal(demuxErrors.length, 0, 'a later window is still non-fatal');
  helper.reset(task);
  assert.equal(task.eioLedger.held, 0);
  assert.equal(helper.gate(task, 10), 1, 'reset still wakes the gate');
  assert.equal(task.eioLedger.starve, 0);
  assert.equal(demuxErrors.length, 0);
  assert.equal(task.loop.stopped, false);
});

test('ordinary pressure resumes immediately when bytes are released below the low watermark', () => {
  const env = buildTask({ demuxBytes: 1000, maxPacketBytes: 100 });
  const { task, helper, demuxErrors, fakeClock } = env;
  helper.add(task, 'c', 0, 1000);
  task.cacheRequests.set(0, { waiting: true });
  fakeClock.advance(1000);
  assert.equal(helper.gate(task, 10), 2);
  assert.equal(demuxErrors.length, 0);

  fakeClock.advance(59000);
  assert.equal(helper.gate(task, 10), 2);
  assert.equal(demuxErrors.length, 0, 'the long wait is still ordinary pressure');
  assert.equal(task.loop.stopped, false);

  assert.equal(helper.del(task, 'c', 0, 201), 201);
  assert.equal(helper.gate(task, 10), 1, 'below the low watermark reading resumes');
  assert.equal(task.eioLedger.latched, false);
  assert.equal(task.eioLedger.starve, 0);
  assert.equal(demuxErrors.length, 0);
  assert.equal(task.loop.stopped, false);

  helper.add(task, 'c', 0, 201);
  assert.equal(helper.gate(task, 10), 2);
  fakeClock.advance(60000);
  assert.equal(helper.gate(task, 10), 2);
  assert.equal(demuxErrors.length, 0, 'a later pressure window never inherits a fatal state');
  assert.equal(task.loop.stopped, false);
  assert.equal(task.eioLedger.held, 1000, 'the re-added bytes stay owned');

  assert.equal(helper.del(task, 'c', 0, 201), 201);
  assert.equal(helper.gate(task, 10), 1, 'timely release still resumes reading');
  assert.equal(demuxErrors.length, 0);
  assert.equal(task.loop.stopped, false);
});

test('a disappearing pending demand is never error evidence and a new demand is not poisoned', () => {
  const env = buildTask({ demuxBytes: 1000, maxPacketBytes: 100 });
  const { task, helper, demuxErrors, fakeClock } = env;
  helper.add(task, 'c', 0, 1000);
  task.cacheRequests.set(0, { waiting: true });
  fakeClock.advance(1500);
  assert.equal(helper.gate(task, 10), 2);
  assert.equal(demuxErrors.length, 0);

  task.cacheRequests.clear();
  fakeClock.advance(50000);
  assert.equal(helper.gate(task, 10), 2, 'held bytes still gate reads');
  assert.equal(demuxErrors.length, 0, 'without consumer demand there is no error evidence');
  assert.equal(task.eioLedger.starve, 0);
  assert.equal(task.loop.stopped, false);

  task.cacheRequests.set(0, { waiting: true });
  assert.equal(helper.gate(task, 10), 2, 'the new demand starts its diagnostic window at the current clock');
  fakeClock.advance(60000);
  assert.equal(helper.gate(task, 10), 2);
  assert.equal(demuxErrors.length, 0, 'the new demand never inherits a fatal deadline');
  assert.equal(task.loop.stopped, false);
  assert.equal(task.eioLedger.held, 1000, 'ownership is unchanged by the pending request');

  assert.equal(helper.del(task, 'c', 0, 201), 201);
  assert.equal(helper.gate(task, 10), 1, 'real release still wakes the gate');
  assert.equal(demuxErrors.length, 0);
  assert.equal(task.loop.stopped, false);
});

test('maxPacket overage remains the explicit fatal counterexample', ASYNC, async () => {
  const env = buildTask({ demuxBytes: 1000, maxPacketBytes: 100 });
  const { task, demuxErrors, releaseCalls } = env;
  task.cacheAVPackets.set(0, []);
  env.scenario.queue = [{ index: 0, size: 101 }];
  assert.equal(await task.doDemux(task, 10), undefined);
  assert.equal(releaseCalls.length, 1);
  assert.equal(task.cacheAVPackets.get(0).length, 0);
  assert.equal(task.eioLedger.held, 0);
  assert.equal(task.loop.stopped, true);
  assert.equal(demuxErrors.length, 1);
  assert.equal(demuxErrors[0].method, 'demuxError');
  assert.equal(demuxErrors[0].params.code, 'DEMUX_PACKET_TOO_LARGE');
  assert.equal(demuxErrors[0].params.need, 101);
  assert.equal(demuxErrors[0].params.avail, 100);
});

test('vendored croppingAVPacketQueue splice debits exactly the removed packets', ASYNC, async () => {
  const env = buildTask({ streams: [{ codecType: 0, codecId: 2 }], demuxBytes: 48e6, maxPacketBytes: 16e6 });
  const { task, releaseCalls } = env;
  const list = [];
  task.cacheAVPackets.set(0, list);
  const specs = [
    { dts: 0, size: 100, flags: 0 },
    { dts: 1000, size: 100, flags: 1 },
    { dts: 2000, size: 100, flags: 0 }
  ];
  for (const spec of specs) {
    env.scenario.queue = [{ index: 0, size: spec.size, dts: spec.dts, flags: spec.flags }];
    await task.doDemux(task, 10);
  }
  assert.equal(task.eioLedger.held, 300);
  await task.croppingAVPacketQueue(task, 0);
  assert.equal(releaseCalls.length, 1);
  assert.equal(list.length, 2);
  assert.equal(task.eioLedger.held, 200);
  assert.equal(ledgerSum(task), 200);
  assert.equal(containerBytes(task, 'c', 0, env.i32), 200);
});

test('vendored changeConnectStream moves cache and pending bytes without losing them', ASYNC, async () => {
  const env = buildTask({ streams: [{ codecType: 0, codecId: 2 }, { codecType: 1, codecId: 2 }], demuxBytes: 48e6, maxPacketBytes: 16e6 });
  const { task } = env;
  task.cacheAVPackets.set(0, []);
  task.pendingAVPackets.set(1, []);
  task.rightIPCPorts.set(0, {});
  task.rightIPCPorts.set(1, {});
  env.scenario.queue = [{ index: 0, size: 100 }, { index: 1, size: 200 }];
  await task.doDemux(task, 10);
  await task.doDemux(task, 10);
  assert.equal(task.eioLedger.c.get(0), 100);
  assert.equal(task.eioLedger.p.get(1), 200);

  await task.changeConnectStream(task, 1, 0);
  assert.equal(task.eioLedger.held, 300, 'bytes are moved, never dropped');
  assert.equal(task.eioLedger.c.get(1), 200);
  assert.equal(task.eioLedger.p.get(0), 100);
  assert.equal(ledgerSum(task), 300);

  // non-forced switch with no pending queue keeps the bytes under the new index
  const second = buildTask({ streams: [{ codecType: 0, codecId: 2 }, { codecType: 1, codecId: 2 }], demuxBytes: 48e6, maxPacketBytes: 16e6 });
  second.task.cacheAVPackets.set(0, []);
  second.task.rightIPCPorts.set(0, {});
  second.task.rightIPCPorts.set(1, {});
  second.scenario.queue = [{ index: 0, size: 90 }];
  await second.task.doDemux(second.task, 10);
  await second.task.changeConnectStream(second.task, 1, 0, false);
  assert.equal(second.task.eioLedger.held, 90);
  assert.equal(second.task.eioLedger.c.get(1), 90);
  assert.equal(ledgerSum(second.task), 90);
  assert.equal(second.task.cacheAVPackets.get(0), undefined);
  assert.equal(second.task.cacheAVPackets.get(1).length, 1);
});

test('vendored Demux unregisterTask resets the ledger, frees cache/pending, and tears the task down', ASYNC, async () => {
  const sequence = [];
  const ipcState = [];
  let loopStops = 0;
  let formatDestroyed = 0;
  let loopDestroyed = 0;
  let rightDestroyed = 0;
  const rightReplies = [];
  const env = buildTask({
    demuxBytes: 48e6,
    maxPacketBytes: 16e6,
    onIpcRequest(entry) {
      // Snapshot what the real ledger/containers looked like when the strict
      // teardown asked IO to abort its owned reads.
      ipcState.push({
        method: entry.method,
        generation: env.task.eioLedger.generation,
        phase: env.task.eioLedger.phase,
        released: env.releaseCalls.length,
        loopStops
      });
      sequence.push(`ipc:${entry.method}`);
    }
  });
  const { task, releaseCalls, logs, ipcRequests } = env;
  // An attached shared domain is what makes the actual eioAbortOwnedReads issue
  // its LOOPBACK request; without it the real method returns before any request
  // (covered separately below).
  task.eioSharedController = {};
  task.cacheAVPackets.set(0, []);
  task.pendingAVPackets.set(1, []);
  env.scenario.queue = [{ index: 0, size: 100 }, { index: 1, size: 200 }];
  await task.doDemux(task, 10);
  await task.doDemux(task, 10);
  assert.equal(task.eioLedger.held, 300);

  const releasePacket = task.avpacketPool.release;
  task.avpacketPool.release = function (packet) {
    sequence.push('release');
    return releasePacket.call(this, packet);
  };
  task.formatContext.destroy = async () => {
    sequence.push('formatDestroy');
    formatDestroyed++;
  };
  task.loop.stopBeforeNextTick = async () => {
    sequence.push('loopStop');
    loopStops++;
  };
  task.loop.destroy = () => {
    sequence.push('loopDestroy');
    loopDestroyed++;
  };
  task.rightIPCPorts.set(0, {
    destroy() {
      rightDestroyed++;
    }
  });
  task.rightIPCPorts.set(9, {
    destroy() {
      rightDestroyed++;
    },
    reply(request, value) {
      rightReplies.push({ request, value });
    }
  });
  const request = { req: 'pending' };
  task.cacheRequests.set(9, request);

  await task.unregisterTask.call(task, task);

  // Ordering: the source asks IO to abort owned reads (and awaits the exact 1)
  // before the loop/producer stop proof, before the ledger reset and before any
  // packet is freed or the task is torn down.
  assert.deepEqual(sequence, [
    'ipc:eioAbortOwnedReads',
    'loopStop',
    'loopStop',
    'loopDestroy',
    'release',
    'release',
    'formatDestroy'
  ], 'the owned-read abort request precedes the loop stop proof, the ledger reset and the frees');
  assert.deepEqual(ipcRequests.map((entry) => entry.method), ['eioAbortOwnedReads']);
  assert.equal(ipcState.length, 1, 'the IO owned-read abort is requested exactly once');
  assert.equal(ipcState[0].generation, 1, 'the demux session abort already ran before IO was asked to abort');
  assert.equal(ipcState[0].phase, 4);
  assert.equal(ipcState[0].released, 0, 'no owned packet may be freed before the ACK');
  assert.equal(ipcState[0].loopStops, 0, 'the loop stop proof follows the ACK');
  // The ACK object is created inside the method VM, so clone it into a host
  // object before the deep comparison: the same fields/values must match, while
  // the foreign outer realm prototype must not decide the assertion.
  assert.deepEqual({ ...task.eioDualStopAck }, { loop: true, source: true, reason: 'TRACKED_PRODUCERS_SETTLED' });
  assert.equal(task.eioProducerOperations.size, 0, 'the empty tracked producer set settles without inventing work');

  assert.equal(task.eioLedger.held, 0);
  assert.equal(task.eioLedger.c.size, 0);
  assert.equal(task.eioLedger.p.size, 0);
  // The current teardown aborts the opt-in session twice (before the IO ACK and
  // after the producer proof) and then resets the ledger; each step advances the
  // generation, so the post-teardown ledger is at 3.
  assert.equal(task.eioLedger.generation, 3);
  assert.equal(task.eioLedger.phase, 4);
  assert.equal(releaseCalls.length, 2, 'cache and pending packets are released');
  assert.equal(releaseCalls.includes(task.cacheAVPackets.get(0)[0]), true, 'cache packet is freed');
  assert.equal(releaseCalls.includes(task.pendingAVPackets.get(1)[0]), true, 'pending packet is freed');
  assert.deepEqual(rightReplies, [{ request, value: -1048576 }]);
  assert.equal(task.cacheRequests.size, 0);
  assert.equal(formatDestroyed, 1);
  // The current teardown stops the loop once for the producer stop proof and once
  // more before destroying it; the fixture counts both real calls.
  assert.equal(loopStops, 2);
  assert.equal(loopDestroyed, 1);
  assert.equal(rightDestroyed, 2);
  assert.equal(task.tasks.size, 0);
  assert.equal(logs.some((args) => String(args[0]).includes('unregisterTask')), true);
});

test('unregisterTask skips the IO owned-read abort when no shared domain is attached', ASYNC, async () => {
  const env = buildTask({ demuxBytes: 48e6, maxPacketBytes: 16e6 });
  const { task, releaseCalls, ipcRequests } = env;
  task.cacheAVPackets.set(0, []);
  env.scenario.queue = [{ index: 0, size: 100 }];
  await task.doDemux(task, 10);

  await task.unregisterTask.call(task, task);

  // No eioSharedController means the real eioAbortOwnedReads returns before it
  // touches the transport: nothing is faked into a successful acknowledgement.
  assert.deepEqual(ipcRequests, []);
  assert.equal(releaseCalls.length, 1);
  assert.equal(task.eioLedger.generation, 3);
  assert.equal(task.eioLedger.held, 0);
  assert.equal(task.tasks.size, 0);
});

test('an unproven IO abort ACK keeps the demux task, its packets and its teardown', ASYNC, async () => {
  const env = buildTask({ demuxBytes: 48e6, maxPacketBytes: 16e6, ipcAbortAck: 0 });
  const { task, releaseCalls, ipcRequests } = env;
  task.eioSharedController = {};
  task.cacheAVPackets.set(0, []);
  task.pendingAVPackets.set(1, []);
  env.scenario.queue = [{ index: 0, size: 100 }, { index: 1, size: 200 }];
  await task.doDemux(task, 10);
  await task.doDemux(task, 10);

  let formatDestroyed = 0;
  let loopDestroyed = 0;
  task.formatContext.destroy = async () => {
    formatDestroyed++;
  };
  task.loop.destroy = () => {
    loopDestroyed++;
  };

  await assert.rejects(task.unregisterTask.call(task, task), { code: 'EIO_IO_ABORT_UNVERIFIED' });

  assert.deepEqual(ipcRequests.map((entry) => entry.method), ['eioAbortOwnedReads']);
  // Host-clone the VM-generated ACK only for this shape comparison; every
  // retention/prototype-independent check below stays unchanged.
  assert.deepEqual({ ...task.eioDualStopAck }, { loop: true, source: false, reason: 'IO_ABORT_UNVERIFIED' });
  // The source aborts the opt-in session before it asks IO, so the ledger mirrors
  // are already dropped; the real packet containers below are untouched.
  assert.equal(task.eioLedger.phase, 4);
  assert.equal(task.eioLedger.generation, 1);
  assert.equal(task.cacheAVPackets.get(0).length, 1, 'a cache packet is never freed without proof');
  assert.equal(task.pendingAVPackets.get(1).length, 1, 'a pending packet is never freed without proof');
  assert.equal(releaseCalls.length, 0);
  assert.equal(formatDestroyed, 0);
  assert.equal(loopDestroyed, 0);
  assert.equal(task.tasks.has(task), true, 'the task stays registered when the ACK is unproven');
});

test('activation arms the demux ledger and the IO read path only for the MSE HTTP VOD opt-in', () => {
  const text = bundleText();
  const helperSource = sliceHelper(text, 'DEMUX');
  const helper = vm.runInNewContext(`${helperSource}\n;__eioDualDemux;`, { Map, Math, Number, isFinite });
  const task = { eioOptions: { ioloaderOptions: { mediaType: 0 } }, eioLedger: null };
  assert.equal(helper.activate(task, null), undefined);
  assert.equal(task.eioLedger, null, 'no opt-in, no ledger: baseline behaviour');
  helper.activate(task, {
    enabled: true,
    demuxBytes: 48e6,
    maxPacketBytes: 16e6,
    ioBytes: 12e6,
    maxForwardSeconds: 120,
    mseBytes: 240e6
  });
  assert.equal(task.eioLedger.high, 48e6);
  assert.equal(task.eioLedger.low, 32e6, 'resume watermark never exceeds the pre-read reservation ceiling');
  assert.equal(task.eioLedger.readCeil, 32e6);
  const armed = task.eioOptions.ioloaderOptions.eioDualCache;
  assert.equal(armed.enabled, true);
  assert.equal(armed.active, true);
  assert.equal(armed.ioBytes, 12e6);
  assert.equal(armed.maxPacketBytes, 16e6);
  const snapshot = helper.snapshot(task);
  assert.equal(snapshot.held, 0);
  assert.equal(snapshot.generation, 0);
});

function avplayerMethods() {
  const text = bundleText();
  const source = [
    sliceMethod(text, 'getEioDualCacheConfig(){'),
    sliceMethod(text, 'isEioDualCacheCandidate(){'),
    sliceMethod(text, 'getEioDualCacheActive(){'),
    sliceMethod(text, 'isHls(){'),
    sliceMethod(text, 'isDash(){')
  ].join(',');
  return vm.runInNewContext(`(function(){ return { ${source} }; })()`, {});
}

function avplayer(options, extra = {}) {
  return Object.assign(avplayerMethods(), {
    options,
    ext: 'mp4',
    isLive_: false,
    useMSE: true,
    source: 'https://fixture.invalid/movie.mp4',
    ...extra
  });
}

test('AVPlayer normalizes the opt-in and only arms HTTP file MSE VOD', () => {
  assert.equal(avplayer({}).getEioDualCacheConfig(), null, 'absent option keeps baseline behaviour');
  assert.equal(avplayer({ eioDualCache: { enabled: false } }).getEioDualCacheConfig(), null);
  assert.equal(
    avplayer({ eioDualCache: { enabled: true } }, { useMSE: false }).getEioDualCacheActive(),
    null,
    'not armed without MSE'
  );

  const defaults = avplayer({ eioDualCache: { enabled: true } }).getEioDualCacheConfig();
  assert.equal(defaults.enabled, true);
  assert.equal(defaults.maxBytes, 3e8);
  assert.equal(defaults.maxForwardSeconds, 120);
  assert.equal(defaults.ioBytes, 12e6);
  assert.equal(defaults.demuxBytes, 48e6);
  assert.equal(defaults.mseBytes, 24e7);
  assert.equal(defaults.maxPacketBytes, 16e6);

  const overflow = avplayer({
    eioDualCache: {
      enabled: true,
      maxBytes: 100,
      ioBytes: 60,
      demuxBytes: 50,
      mseBytes: 50,
      maxPacketBytes: 100e6
    }
  });
  assert.equal(overflow.getEioDualCacheConfig(), null, 'io + demux must not exceed maxBytes');
  assert.equal(overflow.eioDualCacheConfigError.code, 'EIO_DUAL_CONFIG_PARTITION_OVERFLOW');
  assert.equal(overflow.getEioDualCacheActive(), null);

  const valid = avplayer({
    eioDualCache: {
      enabled: true,
      maxBytes: 1000,
      ioBytes: 60,
      demuxBytes: 100,
      mseBytes: 840,
      maxPacketBytes: 50
    }
  }).getEioDualCacheConfig();
  assert.deepEqual({ ...valid }, {
    enabled: true,
    maxBytes: 1000,
    maxForwardSeconds: 120,
    ioBytes: 60,
    demuxBytes: 100,
    mseBytes: 840,
    maxPacketBytes: 50
  });

  const base = {
    enabled: true,
    maxBytes: 1000,
    ioBytes: 60,
    demuxBytes: 100,
    mseBytes: 840,
    maxPacketBytes: 50
  };
  const invalidValues = [-1, 0, Number.NaN, 1.5, '1000', 2 ** 53];
  for (const field of ['maxBytes', 'ioBytes', 'demuxBytes', 'maxPacketBytes', 'maxForwardSeconds', 'mseBytes']) {
    for (const value of invalidValues) {
      const candidate = avplayer({ eioDualCache: { ...base, [field]: value } });
      assert.equal(candidate.getEioDualCacheConfig(), null, `${field}=${String(value)} must fail closed`);
      assert.equal(
        candidate.eioDualCacheConfigError.code,
        'EIO_DUAL_CONFIG_INVALID_NUMBER',
        `${field}=${String(value)}`
      );
    }
  }

  const armed = avplayer({ eioDualCache: { enabled: true } });
  assert.equal(armed.isEioDualCacheCandidate(), true);
  assert.equal(armed.getEioDualCacheActive().demuxBytes, 48e6);
  assert.equal(avplayer({ eioDualCache: { enabled: true } }, { useMSE: false }).getEioDualCacheActive(), null);
  for (const source of ['https://fixture.invalid/movie.mp4', 'http://fixture.invalid/movie.mp4']) {
    assert.equal(
      avplayer({ eioDualCache: { enabled: true } }, { source }).isEioDualCacheCandidate(),
      true,
      source
    );
  }
  for (const source of [
    'blob:https://fixture.invalid/1234',
    'data:video/mp4;base64,AAAA',
    'file:///tmp/movie.mp4',
    'ws://fixture.invalid/live',
    'wss://fixture.invalid/live',
    '//fixture.invalid/movie.mp4'
  ]) {
    assert.equal(
      avplayer({ eioDualCache: { enabled: true } }, { source }).isEioDualCacheCandidate(),
      false,
      source
    );
  }
  assert.equal(avplayer({ eioDualCache: { enabled: true } }, { ext: 'm3u8' }).isEioDualCacheCandidate(), false);
  assert.equal(avplayer({ eioDualCache: { enabled: true } }, { ext: 'mpd' }).isEioDualCacheCandidate(), false);
  assert.equal(avplayer({ eioDualCache: { enabled: true } }, { isLive_: true }).isEioDualCacheCandidate(), false);
  assert.equal(
    avplayer({ eioDualCache: { enabled: true } }, { source: { name: 'movie.mp4' } }).isEioDualCacheCandidate(),
    false,
    'local File / custom loader sources are not HTTP file'
  );
});

test('every demux container mutation point is hooked (source coverage)', () => {
  const text = bundleText();
  const hooks = [
    '__eioDualDemux.add(e,"c",r,', // doDemux cache push
    '__eioDualDemux.add(e,"p",r,', // doDemux pending push
    '__eioDualDemux.add(e,"c",F,', // STREAM_INDEX_ALL push
    '__eioDualDemux.add(e,"c",r.index,', // doAttachedPicture
    '__eioDualDemux.del(r,"c",e.streamIndex,', // pull shift
    '__eioDualDemux.del(e,"c",r,', // subtitle overflow
    '__eioDualDemux.del(e,"p",r,', // pending trim
    '__eioDualDemux.del(i,i.cacheAVPackets.has(t)?"c":"p",', // cropping splice
    '__eioDualDemux.delAll(r,"c",t),', // connectStreamTask reconnect
    '__eioDualDemux.delAll(a,"c",i),', // changeConnectStream clear
    '__eioDualDemux.move(a,"c",i,"p",i)', // changeConnectStream move
    '__eioDualDemux.move(a,"p",t,"c",t)',
    '__eioDualDemux.reset(s),', // seek reset
    '__eioDualDemux.reset(t),', // unregisterTask
    '__eioDualDemux.sync(a,__eioDualDemuxSize),', // changeConnectStream rebalance
    'startDemux(e,t,i,n){const r=this.tasks.get(e);if(r){__eioDualDemux.activate(r,n)',
    'const eioGate=__eioDualDemux.gate(r,i)',
    'this.drainCacheRequests(r)'
  ];
  for (const hook of hooks) assert.ok(text.includes(hook), `missing hook: ${hook}`);
  assert.equal(text.includes('DEMUX_STARVED'), false, 'ordinary pressure must not invent a fatal demux error');
});
