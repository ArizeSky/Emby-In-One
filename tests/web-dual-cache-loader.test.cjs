const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// Runs the *patched vendored* FetchIOLoader: the inline `__eioDualIO` ledger
// factory plus the real read / readInterval / openReader methods sliced out of
// public/vendor/web/libmedia/avplayer.js, against a fake stream reader.
const BUNDLE = path.join(__dirname, '../public/vendor/web/libmedia/avplayer.js');
// Async cases fail fast instead of hanging the file. A timeout is a failure,
// not a substitute for the real assertions.
const ASYNC = { timeout: 2000 };
const CLASS_HEAD = 'class l extends d.A{constructor(){super(arguments.length>0&&void 0!==arguments[0]?arguments[0]:{})';

function bundleText() {
  return fs.readFileSync(BUNDLE, 'utf8');
}

function sliceHelper(text, name) {
  const begin = `/*__EIO_DUAL_CACHE_${name}_BEGIN__*/`;
  const end = `/*__EIO_DUAL_CACHE_${name}_END__*/`;
  const start = text.indexOf(begin);
  const stop = text.indexOf(end);
  assert.ok(start > -1 && stop > start, `${name} budget helper is missing from the bundle`);
  return text.slice(start + begin.length, stop);
}

function sliceMethod(text, head) {
  return sliceMethodFrom(text, head, 0);
}

// The strict owned-resource helper is the original `__eioOwnedIO` object of the
// vendored bundle. It is evaluated in the same vm as `__eioDualIO` so the real
// IO snapshot reads actual ownership state instead of a harness stub.
function sliceOwnedHelper(text) {
  const head = 'const __eioOwnedIO={';
  const start = text.indexOf(head);
  assert.ok(start > -1, '__eioOwnedIO owned helper is missing from the bundle');
  let depth = 0;
  for (let i = start + head.length - 1; i < text.length; i++) {
    const ch = text[i];
    if (ch === '{') depth++;
    else if (ch === '}') {
      depth--;
      if (depth === 0) return text.slice(start, i + 2);
    }
  }
  throw new Error('unbalanced __eioOwnedIO helper');
}

function ioHelpers(text) {
  return vm.runInNewContext(
    `${sliceHelper(text, 'IO')}\n${sliceOwnedHelper(text)}\n;({ dual: __eioDualIO, owned: __eioOwnedIO });`,
    {
      Map,
      Set,
      Math,
      Number,
      isFinite
    }
  );
}

function sliceMethodFrom(text, head, from) {
  const start = text.indexOf(head, from);
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

// The demux and IO pipelines expose RPCs with the same prefix. Select the real
// IO pipeline occurrence by its `ioLoader` body instead of guessing an offset.
function sliceIoPipelineMethod(text, head) {
  let from = 0;
  for (;;) {
    const start = text.indexOf(head, from);
    assert.ok(start > -1, `IO pipeline method ${head} is missing from the bundle`);
    const method = sliceMethodFrom(text, head, start);
    if (method.includes('ioLoader') && !method.includes('__eioDualDemux')) return method;
    from = start + head.length;
  }
}

function makeReader(chunks) {
  const queue = chunks.slice();
  return {
    reads: 0,
    cancelled: false,
    async read() {
      this.reads++;
      if (!queue.length) return { done: true, value: undefined };
      return { done: false, value: queue.shift() };
    },
    async cancel() {
      this.cancelled = true;
    }
  };
}

// A spec-shaped BYOB body: every read receives the caller's view, and a normal
// chunk can never be longer than that view. `overshoot` is the deliberate
// hostile case that returns a larger backing store than the enforced view.
function makeByobBody(chunks, opts = {}) {
  const queue = chunks.slice();
  const state = { modes: [], reads: 0, views: [], cancelled: false, released: 0, reader: null };
  const body = {
    locked: false,
    getReader(init) {
      if (init && init.mode === 'byob') {
        if (opts.noByob) throw new TypeError('BYOB is not supported');
        state.modes.push('byob');
        const reader = {
          mode: 'byob',
          async read(view) {
            state.reads++;
            state.views.push(view.byteLength);
            const item = queue.shift();
            if (!item) return { done: true, value: undefined };
            if (item.overshoot) return { done: false, value: new Uint8Array(item.overshoot) };
            const out = new Uint8Array(view.buffer, 0, Math.min(item.byteLength, view.byteLength));
            out.set(item.subarray(0, out.byteLength));
            return { done: false, value: out };
          },
          releaseLock() {
            state.released++;
          },
          async cancel() {
            state.cancelled = true;
          }
        };
        state.reader = reader;
        return reader;
      }
      state.modes.push('default');
      return makeReader(queue);
    }
  };
  return { body, state };
}

function installByob(loader, mock) {
  loader.eioBody = mock.body;
  loader.reader = mock.body.getReader({ mode: 'byob' });
  loader.eioReaderMode = 'byob';
  return mock.state;
}

function buildLoader(config, overrides = {}) {
  const text = bundleText();
  const start = text.indexOf(CLASS_HEAD);
  assert.ok(start > -1, 'FetchIOLoader class not found');
  const helpers = ioHelpers(text);
  const helper = helpers.dual;
  const logs = [];
  const bandwidthReads = [];
  const context = {
    __eioDualIO: helper,
    __eioOwnedIO: helpers.owned,
    n: { h2: (message) => { logs.push(message); } },
    c: 'packages/avnetwork/src/ioLoader/FetchIOLoader.ts',
    o: { __: () => {} },
    h: { A: () => 0 },
    s: { A: class { stop() {} } },
    fetch: async () => ({
      ok: true,
      status: 200,
      statusText: 'OK',
      body: overrides.body || { getReader: () => makeReader([]) }
    }),
    setTimeout,
    clearTimeout,
    AbortController: class {
      constructor() {
        this.signal = {};
      }
      abort() {}
    }
  };
  const methods = [
    sliceMethodFrom(text, 'eioOpenReader(e){', start),
    sliceMethodFrom(text, 'async eioEnsureByob(){', start),
    sliceMethodFrom(text, 'eioDualFail(e,t,i,s){', start),
    // Real strict-ownership methods referenced by read / abort / snapshot. They
    // remain inert here because these fixtures attach no shared controller or
    // manager, so the original `eioOwnedStrict()` naturally returns false and
    // the legacy readInterval path still runs.
    sliceMethodFrom(text, 'eioOwnedStrict(){', start),
    sliceMethodFrom(text, 'eioOwnedFail(e,t,i){', start),
    sliceMethodFrom(text, 'eioOwnedBackoff(){', start),
    sliceMethodFrom(text, 'async eioOwnedAcquire(e,t){', start),
    sliceMethodFrom(text, 'async eioOwnedWaitSettled(){', start),
    sliceMethodFrom(text, 'async eioReadOwnedInterval(e){', start),
    sliceMethodFrom(text, 'configureEioDualCache(e){', start),
    sliceMethodFrom(text, 'getEioDualCacheSnapshot(){', start),
    sliceMethodFrom(text, 'async openReader(){', start),
    sliceMethodFrom(text, 'async readInterval(e){', start),
    sliceMethodFrom(text, 'async read(e,t){const i=t&&t.eioDualCache', start)
  ].join(',');
  const loader = Object.assign(
    {
      options: Object.assign(
        {
          isLive: false,
          disableSegment: false,
          preload: 8,
          retryCount: 0,
          retryInterval: 1,
          enableBandwidthReader: false
        },
        overrides.options || {}
      ),
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
        return { tagged: 'bandwidth', reader };
      }
    },
    overrides.loader || {}
  );
  if (config !== undefined) {
    loader.options.eioDualCache = config;
  }
  Object.assign(loader, vm.runInNewContext(`(function(){ return { ${methods} }; })()`, context));
  return { loader, helper, logs, bandwidthReads };
}

test('baseline FetchIOLoader read keeps its pushback behaviour without opt-in', ASYNC, async () => {
  const { loader, logs } = buildLoader(undefined);
  loader.reader = makeReader([new Uint8Array(15).fill(1)]);
  const buffer = new Uint8Array(10);
  const read = await loader.read(buffer);
  assert.equal(read, 10);
  assert.equal(loader.buffers.length, 1);
  assert.equal(loader.buffers[0].length, 5);
  assert.equal(loader.receivedLength, 15);
  assert.equal(logs.length, 0);
  assert.equal(loader.eioDualCacheOptions, undefined);
});

test('provisional config allocates a bounded BYOB view before any native read', ASYNC, async () => {
  const mock = makeByobBody([new Uint8Array(5).fill(2)]);
  const { loader, helper } = buildLoader(
    { enabled: true, active: false, probe: true, ioBytes: 12e6, maxPacketBytes: 16e6 },
    { body: mock.body, options: { enableBandwidthReader: true } }
  );
  const read = await loader.read(new Uint8Array(100000));
  assert.equal(read, 5);
  assert.deepEqual(mock.state.modes, ['byob'], 'the provisional phase never opens a native reader');
  assert.deepEqual(mock.state.views, [65536], 'the real BYOB view is still capped at 64KiB');
  assert.equal(loader.eioBandwidthReaderSkipped, true, 'the eager wrapper is skipped in either strict phase');
  const cfg = helper.cfg(loader);
  assert.equal(cfg.active, false);
  assert.equal(cfg.probe, true);
  const snapshot = loader.getEioDualCacheSnapshot();
  assert.equal(snapshot.enabled, true);
  assert.equal(snapshot.active, false);
  assert.equal(snapshot.probe, true);
});

test('opt-in read accepts a chunk that fits residual + in-flight span budget', ASYNC, async () => {
  const mock = makeByobBody([new Uint8Array(5).fill(2)]);
  const { loader, logs, helper } = buildLoader({
    enabled: true,
    active: true,
    ioBytes: 12e6,
    maxPacketBytes: 16e6
  }, { body: mock.body });
  loader.contentLength = undefined;
  const buffer = new Uint8Array(10);
  const read = await loader.read(buffer);
  assert.equal(read, 5);
  assert.equal(loader.buffers.length, 0);
  assert.equal(logs.length, 0);
  assert.equal(helper.cfg(loader).ioBytes, 12e6);
  assert.equal(helper.residual(loader), 0);
  assert.deepEqual(mock.state.modes, ['byob']);
  assert.deepEqual(mock.state.views, [10], 'view = min(64KiB, maxPacket, room, dest need)');
});

test('an oversize single chunk is rejected explicitly and never appended', ASYNC, async () => {
  const mock = makeByobBody([{ overshoot: 20 }]);
  const { loader, logs, helper } = buildLoader({
    enabled: true,
    active: true,
    ioBytes: 1e6,
    maxPacketBytes: 16
  }, { body: mock.body });
  const buffer = new Uint8Array(20);
  const read = await loader.read(buffer);
  assert.equal(read, -1048574);
  assert.equal(loader.status, 3);
  assert.equal(loader.reader, null);
  assert.equal(loader.buffers.length, 0, 'nothing is silently truncated or cached');
  assert.equal(loader.receivedLength, 0);
  assert.match(logs[0], /IO_BYOB_OVERSHOOT/);
  const plain = { options: { eioDualCache: { enabled: true, active: true, ioBytes: 1e6, maxPacketBytes: 16 } } };
  assert.equal(helper.admitChunk(plain, 20).code, 'IO_CHUNK_TOO_LARGE', 'the helper keeps the packet cap check');
  const again = await loader.read(new Uint8Array(10));
  assert.equal(again, -1048574, 'the failure is sticky, no reconnect/retry storm');
  assert.equal(logs.length, 1);
});

test('a residual that fills the quota still satisfies a direct read without a BYOB request', ASYNC, async () => {
  const mock = makeByobBody([new Uint8Array(50)]);
  const { loader, logs, helper } = buildLoader({
    enabled: true,
    active: true,
    ioBytes: 100,
    maxPacketBytes: 1000
  });
  installByob(loader, mock);
  loader.buffers = [new Uint8Array(100).fill(9)];
  const buffer = new Uint8Array(100);
  const read = await loader.read(buffer);
  assert.equal(read, 100);
  assert.equal(buffer.every((byte) => byte === 9), true);
  assert.equal(loader.buffers.length, 0);
  assert.equal(loader.receivedLength, 0);
  assert.equal(mock.state.reads, 0, 'old bytes are drained before deciding whether to read');
  assert.equal(logs.length, 0);
  assert.equal(helper.residual(loader), 0);
});

test('in-flight read span is refused before the request when residual bytes already fill the budget', ASYNC, async () => {
  const mock = makeByobBody([new Uint8Array(10)]);
  const { loader, logs } = buildLoader({
    enabled: true,
    active: true,
    ioBytes: 100,
    maxPacketBytes: 1000
  });
  installByob(loader, mock);
  loader.buffers = [new Uint8Array(100).fill(4)];
  // Another in-flight read already owns the whole quota. Consuming the
  // residual pushback must not free room for a new pre-read span.
  loader.eioReadReserved = 100;
  const out = new Uint8Array(150);
  const read = await loader.read(out);
  assert.equal(read, -1048574);
  assert.equal(out.subarray(0, 100).every((byte) => byte === 4), true, 'directly consumable residual keeps its order');
  assert.equal(loader.buffers.length, 0);
  assert.equal(loader.status, 3);
  assert.match(logs[0], /IO_BUDGET_REJECTED/);
  assert.equal(mock.state.reads, 0, 'no network read is issued past the budget');
});

test('a read satisfied by residual bytes never issues the would-be-overflowing chunk', ASYNC, async () => {
  const { loader, logs } = buildLoader({
    enabled: true,
    active: true,
    ioBytes: 100,
    maxPacketBytes: 1000
  });
  loader.buffers = [new Uint8Array(60).fill(5)];
  const reader = makeReader([new Uint8Array(50).fill(6)]);
  loader.reader = reader;
  const buffer = new Uint8Array(30);
  const read = await loader.read(buffer);
  assert.equal(read, 30);
  assert.equal(buffer.every((byte) => byte === 5), true, 'the whole read is filled from residual bytes');
  assert.equal(loader.buffers.length, 1);
  assert.equal(loader.buffers[0].length, 30, 'only the consumed residual is removed');
  assert.equal(loader.receivedLength, 0);
  assert.equal(reader.reads, 0, 'no upstream read is issued when residual satisfies the request');
  assert.equal(logs.length, 0);
});

test('an arriving chunk larger than the whole IO quota fails explicitly and is never truncated', ASYNC, async () => {
  const mock = makeByobBody([{ overshoot: 150 }]);
  const { loader, logs } = buildLoader({
    enabled: true,
    active: true,
    ioBytes: 100,
    maxPacketBytes: 1000
  }, { body: mock.body });
  const read = await loader.read(new Uint8Array(150));
  assert.equal(read, -1048574);
  assert.equal(loader.status, 3);
  assert.equal(loader.reader, null);
  assert.equal(loader.buffers.length, 0, 'the oversize chunk is neither cached nor truncated');
  assert.equal(loader.receivedLength, 0);
  assert.equal(mock.state.reads, 1);
  assert.equal(mock.state.cancelled, true);
  assert.match(logs[0], /IO_BYOB_OVERSHOOT: cap 100/);
});

test('the per-read ioloaderOptions carry the activation from the demux task', ASYNC, async () => {
  const mock = makeByobBody([{ overshoot: 4 }]);
  const { loader, helper } = buildLoader({
    enabled: true,
    active: false,
    ioBytes: 1,
    maxPacketBytes: 1
  }, { body: mock.body });
  assert.equal(helper.cfg(loader), null, 'load-time config is inert until armed');
  loader.contentLength = undefined;
  const read = await loader.read(new Uint8Array(10), {
    eioDualCache: { enabled: true, active: true, ioBytes: 1000, maxPacketBytes: 3 }
  });
  assert.equal(read, -1048574, 'the per-read (armed) config rejected the 4 byte chunk');
  assert.equal(loader.eioDualCacheOptions.active, true);
  assert.equal(helper.cfg(loader).maxPacketBytes, 3);
  assert.deepEqual(mock.state.views, [3], 'the per-read maxPacket bounds the real BYOB view');
});

test('bandwidth reader prefetch is disabled while the opt-in budget is armed', ASYNC, async () => {
  const armedMock = makeByobBody([]);
  const armed = buildLoader({
    enabled: true,
    active: true,
    ioBytes: 12e6,
    maxPacketBytes: 16e6
  }, { options: { enableBandwidthReader: true }, body: armedMock.body });
  await armed.loader.openReader();
  assert.equal(armed.bandwidthReads.length, 0, 'no unbounded preread stream in opt-in mode');
  assert.equal(armed.loader.eioBandwidthReaderSkipped, true);
  assert.equal(armed.loader.eioReaderMode, 'byob');
  assert.ok(armed.loader.reader && armed.loader.reader.tagged === undefined);

  const plainMock = makeByobBody([]);
  const plain = buildLoader(undefined, { options: { enableBandwidthReader: true }, body: plainMock.body });
  await plain.loader.openReader();
  assert.equal(plain.bandwidthReads.length, 1, 'baseline keeps enableBandwidthReader untouched');
  assert.ok(!plain.loader.eioBandwidthReaderSkipped);
});

test('IO snapshot reports the derived residual, not a separate counter', () => {
  const { loader, helper } = buildLoader({
    enabled: true,
    active: true,
    ioBytes: 100,
    maxPacketBytes: 50
  });
  loader.buffers = [new Uint8Array(7), new Uint8Array(3)];
  const snapshot = loader.getEioDualCacheSnapshot();
  assert.equal(snapshot.enabled, true);
  assert.equal(snapshot.ioBytes, 100);
  assert.equal(snapshot.maxPacketBytes, 50);
  assert.equal(snapshot.residual, 10);
  loader.buffers.length = 0;
  assert.equal(helper.residual(loader), 0);
});

test('every IO budget hook exists in the vendored loader (source coverage)', () => {
  const text = bundleText();
  const hooks = [
    'const eioCap=__eioDualIO.cap(this,e.length-i)',
    'const eioChunk=__eioDualIO.admitChunk(this,eioLen)',
    'this.eioReadReserved=eioCap,this.eioReadBuffer=null',
    'const eioView=new Uint8Array(eioCap)',
    'if(this.eioDualError)return-1048574',
    'async read(e,t){const i=t&&t.eioDualCache',
    '__eioDualIO.cfg(this)?(this.eioBandwidthReaderSkipped=!0,this.eioOpenReader(t.body))',
    'return e.eioDualCacheDisabled=!0,e.eioDualCacheOptions=null',
    'eioDualError="IO_DISABLE_READ_IN_FLIGHT"',
    'eioDualError="IO_DISABLE_READER_LOCKED"'
  ];
  for (const hook of hooks) assert.ok(text.includes(hook), `missing hook: ${hook}`);
});

// This test does not inject cfg into the loader. The demux task is activated by
// the real vendored helper, the real onFlush closure builds the read request,
// and a mock IPC transport hands the exact ioloaderOptions to the real loader
// read. The real IOPipeline snapshot and configure RPCs are then executed.
test('real demux onFlush and IO configure RPC carry config through IPC to the real loader snapshot', ASYNC, async () => {
  const text = bundleText();
  const demux = vm.runInNewContext(`${sliceHelper(text, 'DEMUX')}\n;__eioDualDemux;`, {
    Map,
    Math,
    Number,
    isFinite,
    o: { f: { 15: () => 0 } }
  });
  const config = {
    enabled: true,
    active: true,
    demuxBytes: 5e6,
    maxPacketBytes: 512000,
    ioBytes: 3e6
  };
  const taskOptions = { isLive: false, ioloaderOptions: { mediaType: 0 } };
  const demuxTask = { eioOptions: taskOptions, eioLedger: null };
  assert.ok(demux.activate(demuxTask, config));
  assert.equal(taskOptions.ioloaderOptions.eioDualCache.active, true);

  const mock = makeByobBody([new Uint8Array(1000).fill(9)]);
  const built = buildLoader(undefined, { body: mock.body });
  built.loader.eioBody = mock.body;
  built.loader.eioReaderMode = 'native';
  built.loader.reader = mock.body.getReader();

  const flushHead = 'l.onFlush=async i=>{';
  const flushStart = text.indexOf(flushHead, text.indexOf('class V extends L.A'));
  assert.ok(flushStart > -1, 'real demux onFlush closure not found');
  const flushEnd = text.indexOf(',l.onSeek=', flushStart);
  assert.ok(flushEnd > flushStart, 'real demux onFlush closure end not found');
  const flushSource = text.slice(flushStart + 'l.onFlush='.length, flushEnd);

  const requests = [];
  const ipc = {
    async request(method, params) {
      assert.equal(method, 'read');
      requests.push(params);
      const dst = new Uint8Array(params.length);
      return built.loader.read(dst, params.ioloaderOptions);
    }
  };
  const onFlush = vm.runInNewContext(`(${flushSource})`, {
    e: taskOptions,
    t: ipc,
    I: { ai: (value) => typeof value === 'number' },
    b: { lW: () => { throw new Error('numeric reply must not take the Uint8Array copy path'); } },
    Uint8Array
  });
  const destination = new Uint8Array(100000);
  const read = await onFlush(destination);
  assert.equal(read, 1000);
  assert.equal(requests.length, 1);
  assert.equal(requests[0].ioloaderOptions.eioDualCache.active, true, 'the read request carries the activated config');
  assert.equal(built.loader.eioDualCacheOptions.active, true, 'the real loader was armed by the read IPC, not by the test');
  assert.deepEqual(mock.state.views, [65536], 'the native reader was upgraded with a real 64 KiB reservation');

  const ioHead = 'async getEioDualCacheSnapshot(e){const t=this.tasks.get(e);if(!t)return';
  const ioMethod = vm.runInNewContext(`({ ${sliceMethodFrom(text, ioHead)} })`).getEioDualCacheSnapshot;
  const ioSnap = await ioMethod.call(
    { tasks: new Map([['task-a', { ioLoader: built.loader }]]) },
    'task-a'
  );
  assert.equal(ioSnap.active, true);
  assert.equal(ioSnap.ioBytes, config.ioBytes);
  assert.equal(ioSnap.maxPacketBytes, config.maxPacketBytes);
  assert.equal(ioSnap.readerMode, 'byob');

  const fresh = buildLoader(undefined);
  const configure = vm.runInNewContext(
    `({ ${sliceIoPipelineMethod(text, 'async configureEioDualCache(e,t){const i=this.tasks.get(e)')} })`
  ).configureEioDualCache;
  const tasks = new Map([['task-b', { ioLoader: fresh.loader }]]);
  assert.equal(await configure.call({ tasks }, 'task-b', config), 1);
  assert.equal(fresh.loader.eioDualCacheOptions.active, true);
  const bound = await ioMethod.call({ tasks }, 'task-b');
  assert.equal(bound.active, true, 'the configure RPC binds the real remote loader before the first read');

  const adopt = buildLoader(undefined);
  adopt.loader.buffers = [new Uint8Array(1000)];
  const adoptTasks = new Map([['task-c', { ioLoader: adopt.loader }]]);
  assert.equal(await configure.call({ tasks: adoptTasks }, 'task-c', config), 1);
  const adopted = await ioMethod.call({ tasks: adoptTasks }, 'task-c');
  assert.equal(adopted.active, true);
  assert.equal(adopted.residual, 1000, 'existing residual is audited and counted, not discarded');

  const over = buildLoader(undefined);
  over.loader.buffers = [new Uint8Array(4000)];
  const overTasks = new Map([['task-d', { ioLoader: over.loader }]]);
  const small = Object.assign({}, config, { ioBytes: 3000 });
  assert.equal(await configure.call({ tasks: overTasks }, 'task-d', small), 0);
  const overSnap = await ioMethod.call({ tasks: overTasks }, 'task-d');
  assert.equal(overSnap.active, false);
  assert.equal(overSnap.unsupported, 'IO_BUDGET_REJECTED');

  const bandwidth = buildLoader(undefined);
  bandwidth.loader.eioBandwidthReaderActive = true;
  bandwidth.loader.eioReaderMode = 'bandwidth';
  const bandwidthTasks = new Map([['task-e', { ioLoader: bandwidth.loader }]]);
  assert.equal(await configure.call({ tasks: bandwidthTasks }, 'task-e', config), 0);
  const bandwidthSnap = await ioMethod.call({ tasks: bandwidthTasks }, 'task-e');
  assert.equal(bandwidthSnap.active, false);
  assert.equal(bandwidthSnap.unsupported, 'IO_BANDWIDTH_WRAPPER_UNSUPPORTED', 'an old private prefetch queue is never relabelled as bounded');

  const inflight = buildLoader(undefined);
  inflight.loader.eioNativeReadPending = true;
  const inflightTasks = new Map([['task-f', { ioLoader: inflight.loader }]]);
  assert.equal(await configure.call({ tasks: inflightTasks }, 'task-f', config), 0);
  const inflightSnap = await ioMethod.call({ tasks: inflightTasks }, 'task-f');
  assert.equal(inflightSnap.active, false);
  assert.equal(inflightSnap.unsupported, 'IO_READ_IN_FLIGHT_UNSUPPORTED');

  // Explicit disable must survive the real RPC unchanged (no forced
  // active:true) and a real BYOB loader must convert to native without credit.
  const disarmMock = makeByobBody([new Uint8Array(4).fill(6)]);
  const disarm = buildLoader(
    { enabled: true, active: true, ioBytes: 1000, maxPacketBytes: 40 },
    { body: disarmMock.body }
  );
  await disarm.loader.openReader();
  assert.equal(disarm.loader.eioReaderMode, 'byob');
  disarm.loader.buffers = [new Uint8Array([1, 2, 3])];
  const disarmTasks = new Map([['task-g', { ioLoader: disarm.loader }]]);
  assert.equal(await configure.call({ tasks: disarmTasks }, 'task-g', { enabled: true, active: false }), 1);
  assert.equal(disarm.loader.eioDualCacheOptions, null);
  assert.equal(disarm.loader.eioDualCacheProbe, false);
  assert.equal(disarm.loader.eioDualCacheDisabled, true);
  assert.equal(disarm.loader.eioReaderMode, 'native');
  assert.equal(disarmMock.state.released, 1, 'the BYOB lock is released exactly once');
  assert.equal(disarm.loader.buffers.length, 1, 'disable never clears residual bytes');
  assert.equal(await ioMethod.call({ tasks: disarmTasks }, 'task-g'), null, 'disabled IO exposes no active proof');

  // A probe marker is passed through, but the loader refuses it as an
  // activation (0), so the RPC returns 0 and the provisional phase stays.
  const probe = buildLoader(
    { enabled: true, active: false, probe: true, ioBytes: 1000, maxPacketBytes: 40 },
    { body: makeByobBody([]).body }
  );
  const probeTasks = new Map([['task-h', { ioLoader: probe.loader }]]);
  assert.equal(await configure.call(
    { tasks: probeTasks },
    'task-h',
    { enabled: true, active: false, probe: true, ioBytes: 1000, maxPacketBytes: 40 }
  ), 0);
  assert.equal(probe.loader.eioDualCacheOptions, undefined, 'the RPC must not convert probe into active');
  assert.equal(probe.helper.cfg(probe.loader).probe, true);
  assert.equal(probe.helper.cfg(probe.loader).active, false);

  const passthrough = [];
  const spyTasks = new Map([[
    'task-i',
    { ioLoader: { configureEioDualCache(payload) { passthrough.push(payload); return 1; } } }
  ]]);
  assert.equal(await configure.call({ tasks: spyTasks }, 'task-i', { enabled: true, active: false, probe: true }), 1);
  assert.equal(passthrough[0].active, false, 'explicit active:false is never rewritten');
  assert.equal(passthrough[0].probe, true, 'the probe marker is preserved');
  assert.equal(await configure.call({ tasks: spyTasks }, 'task-i', config), 1);
  assert.equal(passthrough[1].active, true, 'explicit active:true is passed through unchanged');

  const missing = [];
  const missingTasks = new Map([[
    'task-j',
    { ioLoader: { configureEioDualCache(payload) { missing.push(payload); return 1; } } }
  ]]);
  const noActive = { enabled: true, ioBytes: 1000, maxPacketBytes: 40 };
  assert.equal(await configure.call({ tasks: missingTasks }, 'task-j', noActive), 1);
  assert.equal(Object.prototype.hasOwnProperty.call(missing[0], 'active'), false, 'the RPC never invents active:true');
  const noActiveLoader = buildLoader(undefined);
  const noActiveTasks = new Map([['task-k', { ioLoader: noActiveLoader.loader }]]);
  assert.equal(await configure.call({ tasks: noActiveTasks }, 'task-k', noActive), 0, 'missing active is not implicit activation');
  assert.equal(noActiveLoader.loader.eioDualCacheOptions, undefined);
  assert.equal(noActiveLoader.helper.cfg(noActiveLoader.loader), null);
});
