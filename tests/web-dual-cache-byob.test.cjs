const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// v2: runs the *patched* vendored FetchIOLoader methods (openReader / read /
// readInterval / eioEnsureByob / abort / seek) against a spec-shaped BYOB
// stream mock. Every admission/bounds assertion below goes through the real
// runtime code, not through a source marker.
const BUNDLE = path.join(__dirname, '../public/vendor/web/libmedia/avplayer.js');
const CLASS_HEAD = 'class l extends d.A{constructor(){super(arguments.length>0&&void 0!==arguments[0]?arguments[0]:{})';

const bundleText = () => fs.readFileSync(BUNDLE, 'utf8');

function sliceHelper(text, name) {
  const begin = `/*__EIO_DUAL_CACHE_${name}_BEGIN__*/`;
  const end = `/*__EIO_DUAL_CACHE_${name}_END__*/`;
  const start = text.indexOf(begin);
  const stop = text.indexOf(end);
  assert.ok(start > -1 && stop > start, `${name} helper is missing`);
  return text.slice(start + begin.length, stop);
}

function sliceMethodFrom(text, head, from = 0) {
  const start = text.indexOf(head, from);
  assert.ok(start > -1, `vendored method ${head} is missing`);
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

// The strict owned-resource helper is the original `__eioOwnedIO` object of the
// vendored bundle. It is evaluated in the same vm as `__eioDualIO` so the real
// IO snapshot reads actual ownership state instead of a harness stub.
function sliceOwnedHelper(text) {
  const head = 'const __eioOwnedIO={';
  const start = text.indexOf(head);
  assert.ok(start > -1, '__eioOwnedIO owned helper is missing');
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

function demuxHelper(text, sizes) {
  return vm.runInNewContext(`${sliceHelper(text, 'DEMUX')}\n;__eioDualDemux;`, {
    Map,
    Math,
    Number,
    isFinite,
    o: { f: { 15: (addr) => sizes.get(addr - 28) || 0 } }
  });
}

// ---------------------------------------------------------------------------
// BYOB stream mock. `read(view)` must fill the caller-owned view (bounded),
// and hands back a view whose backing store is exactly `view.buffer`.
// ---------------------------------------------------------------------------
function chunkBytes(item) {
  if (item instanceof Uint8Array) return item;
  const out = new Uint8Array(item.bytes || 0);
  out.fill(item.fill === undefined ? 0 : item.fill);
  return out;
}

function fillView(view, bytes) {
  const n = Math.min(bytes.byteLength, view.byteLength);
  view.set(bytes.subarray(0, n), 0);
  return new Uint8Array(view.buffer, 0, n);
}

function makeBody(chunks, opts = {}) {
  const state = { modes: [], byob: null, native: null, locked: false };
  const body = {
    locked: false,
    getReader(init) {
      if (init && init.mode !== 'byob') throw new TypeError('bad mode');
      if (init && init.mode === 'byob') {
        if (opts.noByob) throw new TypeError('BYOB is not supported');
        state.modes.push('byob');
        const reader = {
          mode: 'byob',
          reads: 0,
          views: [],
          cancelled: false,
          released: 0,
          async read(view) {
            this.reads++;
            this.views.push(view.byteLength);
            assert.ok(view && view.byteLength > 0, 'BYOB read needs a non-empty view');
            const item = chunks.shift();
            if (!item) return { done: true, value: undefined };
            if (item.overshoot) {
              return { done: false, value: new Uint8Array(item.overshoot) };
            }
            if (item.hang) {
              return new Promise((resolve) => {
                this.release = (result) => resolve(result);
              });
            }
            return { done: false, value: fillView(view, chunkBytes(item)) };
          },
          releaseLock() {
            this.released++;
          },
          async cancel() {
            this.cancelled = true;
            // ReadableStreamBYOBReader.cancel() settles a pending read as done.
            if (this.release) this.release({ done: true, value: undefined });
          }
        };
        state.byob = reader;
        return reader;
      }
      state.modes.push('default');
      if (opts.noNative) throw new TypeError('native reader is unavailable');
      const reader = {
        mode: 'default',
        reads: 0,
        views: [],
        cancelled: false,
        released: 0,
        async read(view) {
          this.reads++;
          this.views.push(view);
          const item = chunks.shift();
          if (!item) return { done: true, value: undefined };
          if (item.hang) {
            return new Promise((resolve) => {
              this.release = (result) => resolve(result);
            });
          }
          return { done: false, value: chunkBytes(item) };
        },
        releaseLock() {
          this.released++;
        },
        async cancel() {
          this.cancelled = true;
          // A default reader also settles a pending read as done on cancel.
          if (this.release) this.release({ done: true, value: undefined });
        }
      };
      state.native = reader;
      return reader;
    }
  };
  return { body, state, chunks };
}

function sliceFn(text, head) {
  return vm.runInNewContext(`({ ${sliceMethodFrom(text, head)} })`);
}

const LOADER_HEADS = [
  'getBandwidthReader(e,t){',
  'eioOpenReader(e){',
  'async eioEnsureByob(){',
  'eioDualFail(e,t,i,s){',
  // Real strict-ownership methods referenced by read / abort / snapshot. They
  // remain inert here because the fixtures attach no shared controller or
  // manager, so the original `eioOwnedStrict()` naturally returns false and the
  // legacy readInterval path still runs.
  'eioOwnedStrict(){',
  'eioOwnedFail(e,t,i){',
  'eioOwnedBackoff(){',
  'async eioOwnedAcquire(e,t){',
  'async eioOwnedWaitSettled(){',
  'async eioReadOwnedInterval(e){',
  'configureEioDualCache(e){',
  'getEioDualCacheSnapshot(){',
  'async openReader(){',
  'async readInterval(e){',
  'async read(e,t){',
  'async seek(e){',
  'abortSleep(){',
  'async abort(){',
  'async stop(){'
];

function buildLoader(config, body, overrides = {}, page = {}) {
  const text = bundleText();
  const start = text.indexOf(CLASS_HEAD);
  assert.ok(start > -1, 'FetchIOLoader class not found');
  const helpers = ioHelpers(text);
  const helper = helpers.dual;
  const logs = [];
  const context = {
    __eioDualIO: helper,
    __eioOwnedIO: helpers.owned,
    n: { h2: (message) => logs.push(message) },
    c: 'packages/avnetwork/src/ioLoader/FetchIOLoader.ts',
    o: { __: () => {} },
    h: { A: () => 0 },
    s: { A: class { stop() {} } },
    a: { nr: -1048575, NU: -1 },
    ReadableStream: globalThis.ReadableStream,
    fetch: page.fetch || (async () => ({
      ok: true,
      status: 200,
      statusText: 'OK',
      body
    })),
    setTimeout,
    clearTimeout,
    AbortController: class {
      constructor() {
        this.signal = {};
      }
      abort() {}
    }
  };
  const methods = LOADER_HEADS.map((head) => sliceMethodFrom(text, head, start)).join(',');
  const loader = Object.assign({
    options: Object.assign({
      isLive: false,
      disableSegment: false,
      preload: 8,
      retryCount: 0,
      retryInterval: 1,
      enableBandwidthReader: false
    }, overrides.options || {}),
    buffers: [],
    status: 2,
    contentLength: undefined,
    range: { from: 0, to: -1 },
    startBytes: 0,
    endBytes: 1000,
    eofIndex: 1000,
    receivedLength: 0,
    reader: null,
    aborted: false,
    info: { url: 'http://fixture.invalid/media', httpOptions: {} }
  }, overrides.loader || {});
  if (config !== undefined) loader.options.eioDualCache = config;
  Object.assign(loader, vm.runInNewContext(`(function(){ return { ${methods} }; })()`, context));
  return { loader, helper, logs, body, page };
}

const ACTIVE = { enabled: true, active: true, ioBytes: 100, maxPacketBytes: 40 };
const PROVISIONAL = { enabled: true, active: false, probe: true, ioBytes: 100, maxPacketBytes: 40 };
// Every async case fails fast instead of hanging the whole file. A timeout is a
// normal test failure; it never replaces or weakens an assertion.
const ASYNC = { timeout: 2000 };

// ---------------------------------------------------------------------------
// IO: strict pre-read admission and real BYOB ownership
// ---------------------------------------------------------------------------
test('baseline (no opt-in) keeps the original native reader path untouched', ASYNC, async () => {
  const mock = makeBody([new Uint8Array(4).fill(9)]);
  const { loader, logs } = buildLoader(undefined, mock.body);
  const read = await loader.read(new Uint8Array(8));
  assert.equal(read, 4);
  assert.deepEqual(mock.state.modes, ['default']);
  assert.equal(loader.eioReadReserved || 0, 0);
  assert.equal(loader.getEioDualCacheSnapshot(), null);
  assert.equal(loader.eioDualCacheOptions, undefined);
  assert.equal(loader.options.eioDualCache, undefined);
  assert.equal(logs.length, 0);
});

test('active mode allocates a bounded BYOB view before the network read', ASYNC, async () => {
  const mock = makeBody([new Uint8Array(10).fill(1)]);
  const { loader, helper } = buildLoader(Object.assign({}, ACTIVE), mock.body);
  const read = await loader.read(new Uint8Array(10));
  assert.equal(read, 10);
  assert.deepEqual(mock.state.modes, ['byob']);
  assert.deepEqual(mock.state.byob.views, [10], 'view capacity = min(64KiB, maxPacket, room, remaining dest)');
  assert.equal(helper.residual(loader), 0);
  assert.equal(loader.eioReadReserved, 0);
  assert.equal(loader.eioReadCap, 10);
});

test('active mode caps even a huge destination at 64KiB before allocating', ASYNC, async () => {
  const mock = makeBody([new Uint8Array(5).fill(1)]);
  const { loader, helper } = buildLoader(
    { enabled: true, active: true, ioBytes: 12e6, maxPacketBytes: 16e6 },
    mock.body
  );
  const read = await loader.read(new Uint8Array(100000));
  assert.equal(read, 5);
  assert.deepEqual(mock.state.byob.views, [65536], 'the real BYOB view is capped at 64KiB');
  assert.equal(loader.eioReadCap, 65536);
  assert.equal(loader.eioReadReserved, 0);
  assert.equal(helper.residual(loader), 0, 'the whole backing store is released after the tail is consumed');
});

test('a fulfilled residual never issues an unmeasured read', ASYNC, async () => {
  const mock = makeBody([new Uint8Array(50)]);
  const { loader } = buildLoader(Object.assign({}, ACTIVE), mock.body);
  loader.reader = mock.body.getReader({ mode: 'byob' });
  loader.eioReaderMode = 'byob';
  loader.buffers = [new Uint8Array(30).fill(7)];
  const out = new Uint8Array(30);
  const read = await loader.read(out);
  assert.equal(read, 30);
  assert.equal(mock.state.byob.reads, 0, 'no network read when residual is enough');
  assert.equal(out.every((b) => b === 7), true);
});

test('an in-flight reservation is refused before read when no room remains', ASYNC, async () => {
  const mock = makeBody([new Uint8Array(10)]);
  const { loader } = buildLoader({ enabled: true, active: true, ioBytes: 20, maxPacketBytes: 40 }, mock.body);
  loader.reader = mock.body.getReader({ mode: 'byob' });
  loader.eioReaderMode = 'byob';
  loader.eioReadReserved = 20;
  const read = await loader.read(new Uint8Array(10));
  assert.equal(read, -1048574);
  assert.equal(mock.state.byob.reads, 0, 'no read is issued past the budget');
  assert.equal(loader.status, 3);
  assert.equal(loader.eioDualError, 'IO_BUDGET_REJECTED');
  assert.equal(loader.eioReadReserved, 0);
});

test('bounds are validated after arrival and overshoot is never accepted as strict', ASYNC, async () => {
  const mock = makeBody([{ overshoot: 45 }]);
  const { loader } = buildLoader(Object.assign({}, ACTIVE), mock.body);
  const read = await loader.read(new Uint8Array(10));
  assert.equal(read, -1048574);
  assert.equal(loader.eioDualError, 'IO_BYOB_OVERSHOOT');
  assert.equal(loader.buffers.length, 0);
  assert.equal(loader.receivedLength, 0);
});

test('owned bytes count the whole BYOB backing store, not only the tail view', ASYNC, async () => {
  const mock = makeBody([new Uint8Array(5).fill(3)]);
  const { loader, helper } = buildLoader(Object.assign({}, ACTIVE), mock.body);
  const backing = new Uint8Array(40);
  backing.set([1, 2, 3, 4]);
  loader.reader = mock.body.getReader({ mode: 'byob' });
  loader.eioReaderMode = 'byob';
  loader.buffers = [backing.subarray(0, 4)];
  assert.equal(helper.residual(loader), 40, 'the whole backing store is owned, not only the 4-byte view');
  const snap = loader.getEioDualCacheSnapshot();
  assert.equal(snap.residualPushback, 40);
  assert.equal(snap.chunks, 1);
  const out = new Uint8Array(4);
  const first = await loader.read(out);
  assert.equal(first, 4);
  assert.deepEqual(Array.from(out), [1, 2, 3, 4]);
  assert.equal(mock.state.byob.reads, 0, 'residual bytes are consumed before any BYOB read');
  assert.equal(helper.residual(loader), 0, 'consumption releases the backing store');
});

test('switching a native reader to BYOB keeps residual order and does not rewrite options', ASYNC, async () => {
  const mock = makeBody([new Uint8Array([4, 5, 6, 7])]);
  const { loader } = buildLoader(undefined, mock.body);
  assert.equal(await loader.openReader(), undefined);
  const native = loader.reader;
  assert.equal(loader.eioReaderMode, 'native');
  assert.equal(loader.eioBody, mock.body);
  assert.deepEqual(mock.state.modes, ['default'], 'openReader starts on the native reader before opt-in');
  loader.buffers = [new Uint8Array([1, 2, 3])];
  const perRead = { eioDualCache: Object.assign({}, ACTIVE) };
  const out = new Uint8Array(6);
  const read = await loader.read(out, perRead);
  assert.equal(read, 6);
  assert.deepEqual(Array.from(out), [1, 2, 3, 4, 5, 6], 'residual bytes keep their order');
  assert.equal(native.released, 1, 'the native reader lock is released exactly once before BYOB');
  assert.equal(native.reads, 0, 'the upgrade never reads through the old reader');
  assert.equal(mock.state.byob.reads, 1, 'the upgrade issues exactly one BYOB read');
  assert.deepEqual(mock.state.modes, ['default', 'byob']);
  assert.equal(loader.eioDualCacheOptions, perRead.eioDualCache);
  assert.equal(loader.eioDualCacheOptions.active, true);
  assert.equal(loader.options.eioDualCache, undefined, 'switching must not write options state');
});

test('unsupported BYOB fails explicitly instead of splitting an unknown chunk', ASYNC, async () => {
  const mock = makeBody([new Uint8Array(10)], { noByob: true });
  const { loader } = buildLoader(Object.assign({}, ACTIVE), mock.body);
  const read = await loader.read(new Uint8Array(10));
  assert.equal(read, -1048574);
  assert.equal(loader.eioDualError, 'IO_BYOB_UNSUPPORTED');
  assert.equal(loader.buffers.length, 0);
  const snap = loader.getEioDualCacheSnapshot();
  assert.equal(snap.unsupported, 'IO_BYOB_UNSUPPORTED');
});

test('an active bandwidth wrapper is an explicit capability error, never a silent swap', ASYNC, async () => {
  const mock = makeBody([new Uint8Array(10)]);
  const { loader } = buildLoader(Object.assign({}, ACTIVE), mock.body, { options: { enableBandwidthReader: true } });
  loader.eioBandwidthReaderActive = true;
  loader.eioByobCapable = false;
  const read = await loader.read(new Uint8Array(10));
  assert.equal(read, -1048574);
  assert.equal(loader.eioDualError, 'IO_BANDWIDTH_WRAPPER_UNSUPPORTED');
  assert.equal(mock.state.modes.indexOf('byob'), -1, 'no silent reader swap');
  assert.equal(loader.getEioDualCacheSnapshot().unsupported, 'IO_BANDWIDTH_WRAPPER_UNSUPPORTED');
});

test('abort during an in-flight BYOB read releases the reservation without EOF or data', ASYNC, async () => {
  const mock = makeBody([{ hang: true }]);
  const { loader } = buildLoader(Object.assign({}, ACTIVE), mock.body);
  const pending = loader.read(new Uint8Array(10));
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(loader.eioReadPending, true);
  assert.equal(loader.eioReadReserved, 10, 'the reservation is the bounded BYOB view, not maxPacket');
  const generation = loader.eioGen;
  await loader.abort();
  assert.equal(loader.eioGen, generation + 1);
  assert.equal(await pending, -1048576);
  assert.equal(loader.eioReadReserved, 0);
  assert.equal(loader.eioReadPending, false);
  assert.equal(loader.buffers.length, 0);
  assert.equal(loader.receivedLength, 0);
});

test('seek during an in-flight BYOB read keeps the old call from writing new stream state', ASYNC, async () => {
  const mock = makeBody([{ hang: true }]);
  const { loader } = buildLoader(Object.assign({}, ACTIVE), mock.body);
  // The real loader sets supportRange in getTotalSize/open(). Without it seek()
  // returns -1 without aborting and leaves this read pending forever.
  loader.supportRange = true;
  const pending = loader.read(new Uint8Array(10));
  await new Promise((resolve) => setImmediate(resolve));
  const oldGeneration = loader.eioGen;
  const seek = loader.seek(500);
  const [seekResult, readResult] = await Promise.all([seek, pending]);
  assert.equal(seekResult, 0);
  assert.equal(readResult, -1048576, 'the stale read reports cancellation, never a chunk');
  assert.equal(loader.eioGen, oldGeneration + 1);
  assert.equal(loader.eioReadReserved, 0);
  assert.equal(loader.eioReadPending, false);
  assert.equal(loader.buffers.length, 0, 'the stale continuation never rewrites buffers');
  assert.equal(loader.receivedLength, 500, 'seek installs the new stream position');
});

test('provisional phase opens a bounded BYOB reader before any native read', ASYNC, async () => {
  const mock = makeBody([new Uint8Array(10).fill(2)]);
  const { loader, helper } = buildLoader(Object.assign({}, PROVISIONAL), mock.body, {
    options: { enableBandwidthReader: true }
  });
  const read = await loader.read(new Uint8Array(10));
  assert.equal(read, 10);
  assert.deepEqual(mock.state.modes, ['byob'], 'the provisional phase never opens a native reader');
  assert.deepEqual(mock.state.byob.views, [10], 'the view is bounded by destination/maxPacket');
  assert.equal(loader.eioBandwidthReaderSkipped, true, 'no eager bandwidth wrapper under provisional config');
  const phase = helper.cfg(loader);
  assert.equal(phase.active, false);
  assert.equal(phase.probe, true);
  const snap = loader.getEioDualCacheSnapshot();
  assert.equal(snap.enabled, true);
  assert.equal(snap.active, false);
  assert.equal(snap.probe, true);
});

test('provisional phase fails on unsupported BYOB before any native payload read', ASYNC, async () => {
  const mock = makeBody([new Uint8Array(10)], { noByob: true });
  const { loader } = buildLoader(Object.assign({}, PROVISIONAL), mock.body);
  const read = await loader.read(new Uint8Array(10));
  assert.equal(read, -1048574);
  assert.equal(loader.eioDualError, 'IO_BYOB_UNSUPPORTED');
  assert.deepEqual(mock.state.modes, [], 'the native reader is never opened');
  assert.equal(mock.state.native, null);
  assert.equal(loader.eioReaderMode, 'none');
  assert.equal(loader.buffers.length, 0);
});

test('provisional phase rejects an overshooting BYOB chunk without native fallback', ASYNC, async () => {
  const mock = makeBody([{ overshoot: 45 }]);
  const { loader } = buildLoader(Object.assign({}, PROVISIONAL), mock.body);
  const read = await loader.read(new Uint8Array(10));
  assert.equal(read, -1048574);
  assert.equal(loader.eioDualError, 'IO_BYOB_OVERSHOOT');
  assert.deepEqual(mock.state.modes, ['byob']);
  assert.equal(loader.buffers.length, 0);
  assert.equal(loader.receivedLength, 0);
});

test('provisional to active keeps residual bytes and ignores stale probe flags', ASYNC, async () => {
  const mock = makeBody([new Uint8Array(10).fill(4)]);
  const { loader, helper } = buildLoader(Object.assign({}, PROVISIONAL), mock.body);
  loader.buffers = [new Uint8Array([1, 2, 3])];
  assert.equal(loader.configureEioDualCache(Object.assign({}, ACTIVE)), true);
  const phase = helper.cfg(loader);
  assert.equal(phase.active, true);
  assert.equal(phase.probe, false);
  assert.equal(loader.eioDualCacheProbe, false);
  assert.equal(loader.getEioDualCacheSnapshot().residual, 3);
  assert.equal(helper.residual(loader), 3, 'the buffered backing stays charged through activation');
  const out = new Uint8Array(6);
  const read = await loader.read(out, {
    eioDualCache: { enabled: true, active: false, probe: true, ioBytes: 100, maxPacketBytes: 40 },
    eioDualCacheProbe: { enabled: true }
  });
  assert.equal(read, 6);
  assert.deepEqual(Array.from(out), [1, 2, 3, 4, 4, 4]);
  assert.equal(helper.cfg(loader).active, true, 'a stale per-read probe cannot downgrade active');
  assert.equal(loader.eioDualCacheProbe, false);
});

test('disable converts a nonpending BYOB reader to native and keeps residual order', ASYNC, async () => {
  const mock = makeBody([new Uint8Array([7, 8, 9, 10])]);
  const { loader, helper } = buildLoader(Object.assign({}, ACTIVE), mock.body);
  await loader.openReader();
  const byob = loader.reader;
  assert.equal(loader.eioReaderMode, 'byob');
  loader.buffers = [new Uint8Array([1, 2, 3])];
  assert.equal(loader.configureEioDualCache({ enabled: true, active: false }), 1);
  assert.equal(byob.released, 1, 'the BYOB lock is released exactly once');
  assert.equal(loader.eioReaderMode, 'native');
  assert.equal(loader.reader, mock.state.native);
  assert.deepEqual(mock.state.modes, ['byob', 'default']);
  assert.equal(helper.cfg(loader), null);
  assert.equal(loader.eioDualCacheDisabled, true);
  assert.equal(helper.residual(loader), 3, 'old residual is retained, never credited');
  const out = new Uint8Array(6);
  assert.equal(await loader.read(out), 6);
  assert.deepEqual(Array.from(out), [1, 2, 3, 7, 8, 9]);
  assert.equal(mock.state.native.reads, 1);
  assert.equal(mock.state.byob.reads, 0, 'the released BYOB reader is never read again');
});

test('disable refuses while a read is pending and succeeds after it completes', ASYNC, async () => {
  const mock = makeBody([{ hang: true }]);
  const { loader, helper } = buildLoader(Object.assign({}, ACTIVE), mock.body);
  const pending = loader.read(new Uint8Array(10));
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(loader.eioReadPending, true);
  assert.equal(loader.configureEioDualCache({ enabled: true, active: false }), 0);
  assert.equal(loader.eioDualError, 'IO_DISABLE_READ_IN_FLIGHT');
  assert.equal(helper.cfg(loader).active, true, 'a refused disable leaves strict state intact');
  assert.equal(loader.reader, mock.state.byob, 'the in-flight BYOB reader is not cancelled');
  assert.equal(mock.state.byob.cancelled, false);
  mock.state.byob.release({ done: false, value: new Uint8Array([1, 2, 3]) });
  assert.equal(await pending, 3);
  assert.equal(loader.eioReadPending, false);
  assert.equal(loader.eioReadReserved, 0);
  assert.equal(loader.configureEioDualCache({ enabled: true, active: false }), 1);
  assert.equal(loader.eioReaderMode, 'native');
  assert.equal(loader.eioDualError, 0);
  assert.equal(loader.eioDualCacheDisabled, true);
});

test('a failed BYOB to native conversion is explicit and credits no residual', ASYNC, async () => {
  const mock = makeBody([new Uint8Array(4)], { noNative: true });
  const { loader, helper } = buildLoader(Object.assign({}, ACTIVE), mock.body);
  await loader.openReader();
  loader.buffers = [new Uint8Array([1, 2, 3])];
  assert.equal(loader.configureEioDualCache({ enabled: true, active: false }), 0);
  assert.equal(loader.eioDualError, 'IO_DISABLE_READER_LOCKED');
  assert.equal(loader.reader, null, 'no view-less read is possible on the released BYOB reader');
  assert.equal(loader.eioReaderMode, 'none');
  assert.equal(helper.cfg(loader).active, true, 'strict state is preserved on failure');
  assert.equal(helper.residual(loader), 3, 'the refused disable credits nothing');
  assert.equal(loader.eioDualCacheDisabled, undefined);
});

test('stale per-read probe flags cannot reactivate a disabled loader', ASYNC, async () => {
  const mock = makeBody([new Uint8Array([5, 6, 7, 8])]);
  const { loader, helper } = buildLoader(Object.assign({}, ACTIVE), mock.body);
  await loader.openReader();
  assert.equal(loader.configureEioDualCache({ enabled: true, active: false }), 1);
  const out = new Uint8Array(4);
  const read = await loader.read(out, {
    eioDualCache: { enabled: true, active: false, probe: true, ioBytes: 100, maxPacketBytes: 40 },
    eioDualCacheProbe: { enabled: true }
  });
  assert.equal(read, 4);
  assert.deepEqual(Array.from(out), [5, 6, 7, 8]);
  assert.equal(helper.cfg(loader), null);
  assert.equal(loader.eioDualCacheOptions, null);
  assert.equal(loader.eioDualCacheProbe, false);
  assert.equal(loader.eioDualCacheDisabled, true);
  assert.equal(mock.state.native.reads, 1, 'the disabled loader stays on the native path');
  assert.equal(mock.state.byob.reads, 0);
});

test('an explicit active bind clears the disabled override', () => {
  const mock = makeBody([]);
  const { loader, helper } = buildLoader(Object.assign({}, ACTIVE), mock.body);
  assert.equal(loader.configureEioDualCache({ enabled: true, active: false }), 1);
  assert.equal(loader.eioDualCacheDisabled, true);
  assert.equal(loader.configureEioDualCache(Object.assign({}, ACTIVE)), true);
  assert.equal(loader.eioDualCacheDisabled, false);
  assert.equal(helper.cfg(loader).active, true);
});

// ---------------------------------------------------------------------------
// Demux: fixed resident reader buffers and late activation of probe data
// ---------------------------------------------------------------------------
function buildDemuxTask(sizes, seed) {
  const task = {
    taskId: 'task-a',
    eioLedger: null,
    eioFixedBytes: 2 * 1048576,
    eioIBufferBytes: 1048576,
    eioOBufferBytes: 1048576,
    eioReaderExtraBytes: 0,
    cacheAVPackets: new Map(),
    pendingAVPackets: new Map(),
    cacheRequests: new Map(),
    eioOptions: {},
    stats: 0,
    controlIPCPort: { notify: () => {} }
  };
  if (seed) {
    task.cacheAVPackets.set(0, [1000]);
    sizes.set(1000, seed);
  }
  return task;
}

test('demux activation subtracts the audited resident reader buffers from admission', () => {
  const sizes = new Map();
  const text = bundleText();
  const helper = demuxHelper(text, sizes);
  const task = buildDemuxTask(sizes);
  helper.activate(task, {
    enabled: true,
    demuxBytes: 48e6,
    maxPacketBytes: 16e6,
    ioBytes: 12e6
  });
  const ledger = task.eioLedger;
  assert.equal(ledger.high, 48e6, 'the configured partition remains visible');
  assert.equal(ledger.readCeil, 32e6, 'legacy watermark kept for the partition contract');
  assert.equal(ledger.fixedBytes, 2 * 1048576);
  assert.equal(ledger.packetBytes, 48e6 - 2 * 1048576);
  assert.equal(ledger.readCeilEff, 48e6 - 2 * 1048576 - 16e6);
  assert.equal(task.eioDualCache.fixedBytes, 2 * 1048576);
  assert.equal(task.eioOptions.ioloaderOptions.eioDualCache.active, true);
  const snap = helper.snapshot(task);
  assert.equal(snap.fixedBytes, 2 * 1048576);
  assert.equal(snap.fixed.iBufferBytes, 1048576);
  assert.equal(snap.fixed.oBufferBytes, 1048576);
  assert.equal(snap.fixed.ioReaderExtraBytes, 0);
  assert.equal(snap.fixed.ioReaderSharesIBuffer, true);
  assert.equal(snap.room, ledger.packetBytes);
});

test('late activation counts already-held packets and rejects an over-room packet before storing', () => {
  const sizes = new Map();
  const text = bundleText();
  const helper = demuxHelper(text, sizes);
  const size = (addr) => sizes.get(addr - 28) || 0;
  const maxPacket = 1000;
  const seed = 600;
  const task = buildDemuxTask(sizes, seed);
  // Make the remaining quota (maxPacket - 1) the first limit hit by this
  // packet while keeping the packet itself at or below the per-packet cap.
  helper.activate(task, {
    enabled: true,
    demuxBytes: task.eioFixedBytes + maxPacket + seed - 1,
    maxPacketBytes: maxPacket,
    ioBytes: 12e6
  });
  assert.equal(task.eioLedger.held, seed, 'probe/probe-stage packets are not zeroed');
  assert.equal(task.eioLedger.packetBytes, maxPacket + seed - 1);
  const room = task.eioLedger.packetBytes - seed;
  assert.equal(room, maxPacket - 1);
  sizes.set(2000, room + 1);
  const rejected = helper.admitPacket(task, 2000, size, 0, false);
  assert.equal(rejected.code, 'DEMUX_BUDGET_REJECTED');
  assert.equal(rejected.need, room + 1);
  assert.equal(rejected.avail, room);
  assert.ok(rejected.need <= task.eioLedger.maxPacket, 'the over-room packet stays under the packet cap');
  assert.equal(task.eioLedger.held, seed, 'the rejected over-room packet is never counted as held');
  assert.equal(task.cacheAVPackets.get(0).length, 1, 'the rejected over-room packet is never stored');
  sizes.set(3000, room);
  assert.equal(helper.admitPacket(task, 3000, size, 0, false), null);
  assert.equal(task.eioLedger.held, seed, 'admission alone does not debit or double-count held bytes');
});

test('late activation rejects an oversized packet with PACKET_TOO_LARGE ahead of the quota check', () => {
  const sizes = new Map();
  const text = bundleText();
  const helper = demuxHelper(text, sizes);
  const size = (addr) => sizes.get(addr - 28) || 0;
  const maxPacket = 1000;
  const seed = 600;
  const task = buildDemuxTask(sizes, seed);
  helper.activate(task, {
    enabled: true,
    demuxBytes: task.eioFixedBytes + maxPacket + seed - 1,
    maxPacketBytes: maxPacket,
    ioBytes: 12e6
  });
  assert.equal(task.eioLedger.held, seed, 'probe/probe-stage packets are not zeroed');
  const room = task.eioLedger.packetBytes - task.eioLedger.held;
  sizes.set(4000, maxPacket + 1);
  const rejected = helper.admitPacket(task, 4000, size, 0, false);
  assert.equal(rejected.code, 'DEMUX_PACKET_TOO_LARGE');
  assert.equal(rejected.need, maxPacket + 1);
  assert.equal(rejected.avail, maxPacket);
  assert.ok(rejected.need > room, 'this negative case also exceeds the remaining quota');
  assert.equal(task.eioLedger.held, seed, 'the oversized packet is never counted as held');
  assert.equal(task.cacheAVPackets.get(0).length, 1, 'the oversized packet is never stored');
});

test('demux gate uses the effective ceiling derived from fixed reader ownership', () => {
  const sizes = new Map();
  const text = bundleText();
  const helper = demuxHelper(text, sizes);
  const task = buildDemuxTask(sizes);
  helper.activate(task, { enabled: true, demuxBytes: 48e6, maxPacketBytes: 16e6, ioBytes: 12e6 });
  helper.add(task, 'c', 0, task.eioLedger.readCeilEff + 1);
  assert.equal(helper.gate(task, 10), 2);
});

test('early pure-configure binds the demux ledger without starting the loop', ASYNC, async () => {
  const sizes = new Map();
  const text = bundleText();
  const demux = demuxHelper(text, sizes);
  const configure = vm.runInNewContext(
    `({ ${sliceMethodFrom(text, 'async configureEioDualCache(e,t){const i=this.tasks.get(e)')} })`,
    { __eioDualDemux: demux }
  ).configureEioDualCache;
  const task = buildDemuxTask(sizes);
  const ctx = { tasks: new Map([['task-a', task]]) };
  assert.equal(await configure.call(ctx, 'task-a', { enabled: true, demuxBytes: 48e6, maxPacketBytes: 16e6, ioBytes: 12e6 }), 1);
  assert.equal(task.eioLedger.packetBytes, 48e6 - 2 * 1048576);
  assert.equal(task.loop, undefined, 'configure must not start the demux loop');
  assert.equal(task.eioOptions.ioloaderOptions.eioDualCache.active, true);
  const fresh = buildDemuxTask(sizes);
  const ctx2 = { tasks: new Map([['task-a', fresh]]) };
  assert.equal(await configure.call(ctx2, 'task-a', { enabled: true, demuxBytes: 48e6, maxPacketBytes: 16e6, ioBytes: 12e6 }), 1);
  assert.equal(fresh.eioOptions.ioloaderOptions.eioDualCache.active, true);
  const broken = buildDemuxTask(sizes);
  const ctx3 = { tasks: new Map([['task-a', broken]]) };
  assert.equal(await configure.call(ctx3, 'task-a', { enabled: true, demuxBytes: 1000, maxPacketBytes: 800, ioBytes: 12e6 }), 0, 'fixed reader buffers cannot fit a 1000-byte partition');
  assert.equal(broken.eioLedger, null);
  const invalid = buildDemuxTask(sizes);
  const ctx4 = { tasks: new Map([['task-a', invalid]]) };
  assert.equal(await configure.call(ctx4, 'task-a', { enabled: false }), 0);
  assert.equal(invalid.eioLedger, null);
});

// ---------------------------------------------------------------------------
// Snapshot RPCs: real worker methods, plain objects, explicit null / incomplete
// ---------------------------------------------------------------------------
test('demux and IO snapshot RPCs return plain objects or null, never fake zero caps', ASYNC, async () => {
  const text = bundleText();
  const sizes = new Map();
  const demux = demuxHelper(text, sizes);
  const demuxMethod = vm.runInNewContext(
    `({ ${sliceMethodFrom(text, 'async getEioDualCacheSnapshot(e){const t=this.tasks.get(e);if(!t||!t.eioLedger)')} })`,
    { __eioDualDemux: demux }
  ).getEioDualCacheSnapshot;
  const demuxCtx = { tasks: new Map() };
  assert.equal(await demuxMethod.call(demuxCtx, 'absent'), null);
  const task = buildDemuxTask(sizes);
  demux.activate(task, { enabled: true, demuxBytes: 48e6, maxPacketBytes: 16e6, ioBytes: 12e6 });
  demuxCtx.tasks.set('task-a', task);
  const demuxSnap = await demuxMethod.call(demuxCtx, 'task-a');
  assert.equal(demuxSnap.active, true);
  assert.equal(demuxSnap.generation, 0);
  assert.equal(structuredClone(demuxSnap).fixedBytes, 2 * 1048576);

  const ioMethod = vm.runInNewContext(
    `({ ${sliceMethodFrom(text, 'async getEioDualCacheSnapshot(e){const t=this.tasks.get(e);if(!t)return')} })`
  ).getEioDualCacheSnapshot;
  const ioCtx = { tasks: new Map() };
  const ioAbsent = await ioMethod.call(ioCtx, 'absent');
  assert.equal(ioAbsent.active, false);
  assert.equal(ioAbsent.reason, 'NO_TASK', 'a missing IO task is explicit, never a fabricated ledger');
  const fakeLoader = { getEioDualCacheSnapshot: () => ({ active: false, unsupported: 'IO_BYOB_UNSUPPORTED' }) };
  ioCtx.tasks.set('task-a', { ioLoader: fakeLoader });
  const iSnap = await ioMethod.call(ioCtx, 'task-a');
  assert.equal(iSnap.active, false);
  assert.equal(iSnap.unsupported, 'IO_BYOB_UNSUPPORTED');
  assert.equal(JSON.stringify(iSnap).includes('http'), false, 'snapshots never carry the source URL');
});

test('AVPlayer aggregation is fail-closed when a required IO, demux or MSE domain has no active snapshot', ASYNC, async () => {
  const text = bundleText();
  const head = 'async getEioDualCacheSnapshot(){const t=Number(this.taskId)||this.taskId,i={taskId:t';
  const dual = () => ({ getEioDualCacheActive: () => ({ enabled: true }) });
  const ioActive = { getEioDualCacheSnapshot: async () => ({ active: true }) };
  const demuxNull = { getEioDualCacheSnapshot: async () => null };

  const absent = vm.runInNewContext(
    `({ ${sliceMethodFrom(text, head)} })`,
    { Ue: {}, setTimeout, clearTimeout }
  ).getEioDualCacheSnapshot;
  const noThreads = await absent.call({ taskId: 'task-a', ...dual() });
  assert.equal(noThreads.complete, false);
  assert.deepEqual(Array.from(noThreads.incomplete, (item) => item.domain), ['io', 'demux', 'mse']);

  const noMse = vm.runInNewContext(
    `({ ${sliceMethodFrom(text, head)} })`,
    {
      Ue: { IOThread: ioActive, DemuxerThread: demuxNull, MSEThread: { getEioDualCacheSnapshot: async () => undefined } },
      setTimeout,
      clearTimeout
    }
  ).getEioDualCacheSnapshot;
  const incomplete = await noMse.call({ taskId: 'task-a', ...dual() });
  assert.equal(incomplete.complete, false);
  assert.deepEqual(Array.from(incomplete.incomplete, (item) => [item.domain, item.reason]).sort(), [
    ['demux', 'NO_SNAPSHOT'],
    ['mse', 'METHOD_MISSING']
  ].sort());
  assert.equal(incomplete.mse, null);

  const withMse = vm.runInNewContext(
    `({ ${sliceMethodFrom(text, head)} })`,
    {
      Ue: {
        IOThread: ioActive,
        DemuxerThread: demuxNull,
        MSEThread: { getEioDualCacheSnapshot: async () => ({ active: false, unsupported: 'NOT_ACTIVE' }) }
      },
      setTimeout,
      clearTimeout
    }
  ).getEioDualCacheSnapshot;
  const inactiveMse = await withMse.call({ taskId: 'task-a', ...dual() });
  assert.equal(inactiveMse.complete, false, 'an inactive MSE snapshot is not a complete proof');
  assert.equal(inactiveMse.active, true);
  assert.equal(inactiveMse.mse.unsupported, 'NOT_ACTIVE');
  assert.deepEqual(Array.from(inactiveMse.incomplete, (item) => [item.domain, item.reason]), [
    ['demux', 'NO_SNAPSHOT'],
    ['mse', 'DOMAIN_INACTIVE']
  ]);

  const allActive = vm.runInNewContext(
    `({ ${sliceMethodFrom(text, head)} })`,
    {
      Ue: {
        IOThread: ioActive,
        DemuxerThread: { getEioDualCacheSnapshot: async () => ({ active: true, intervalPacketsAccounted: true }) },
        MSEThread: { getEioDualCacheSnapshot: async () => ({ active: true }) }
      },
      setTimeout,
      clearTimeout
    }
  ).getEioDualCacheSnapshot;
  const complete = await allActive.call({ taskId: 'task-a', ...dual() });
  assert.equal(complete.complete, true);
  assert.equal(complete.active, true);
  assert.deepEqual(Array.from(complete.incomplete), []);

  const noByob = vm.runInNewContext(
    `({ ${sliceMethodFrom(text, head)} })`,
    {
      Ue: {
        IOThread: { getEioDualCacheSnapshot: async () => ({ active: true, byobCapable: false, unsupported: 'IO_BYOB_UNSUPPORTED' }) },
        DemuxerThread: { getEioDualCacheSnapshot: async () => ({ active: true, intervalPacketsAccounted: true }) },
        MSEThread: { getEioDualCacheSnapshot: async () => ({ active: true }) }
      },
      setTimeout,
      clearTimeout
    }
  ).getEioDualCacheSnapshot;
  const ioConfigured = await noByob.call({ taskId: 'task-a', ...dual() });
  assert.equal(ioConfigured.active, true, 'a configured IO snapshot stays visible as active');
  assert.equal(ioConfigured.complete, false, 'configured IO without BYOB capability is not a complete proof');
  assert.ok(ioConfigured.incomplete.some(
    (item) => item.domain === 'io' && item.reason === 'CAPABILITY_INCOMPLETE'
  ));

  const ioNull = vm.runInNewContext(
    `({ ${sliceMethodFrom(text, head)} })`,
    {
      Ue: {
        IOThread: { getEioDualCacheSnapshot: async () => null },
        DemuxerThread: { getEioDualCacheSnapshot: async () => ({ active: true, intervalPacketsAccounted: true }) },
        MSEThread: { getEioDualCacheSnapshot: async () => ({ active: true }) }
      },
      setTimeout,
      clearTimeout
    }
  ).getEioDualCacheSnapshot;
  const missingIo = await ioNull.call({ taskId: 'task-a', ...dual() });
  assert.equal(missingIo.complete, false, 'a null required IO snapshot must never be skipped');
  assert.equal(missingIo.active, true, 'other active domains remain visible evidence');
  assert.deepEqual(Array.from(missingIo.incomplete, (item) => [item.domain, item.reason]), [
    ['io', 'NO_SNAPSHOT']
  ]);

  const intervalUnaccounted = vm.runInNewContext(
    `({ ${sliceMethodFrom(text, head)} })`,
    {
      Ue: {
        IOThread: ioActive,
        DemuxerThread: { getEioDualCacheSnapshot: async () => ({ active: true }) },
        MSEThread: { getEioDualCacheSnapshot: async () => ({ active: true }) }
      },
      setTimeout,
      clearTimeout
    }
  ).getEioDualCacheSnapshot;
  const interval = await intervalUnaccounted.call({ taskId: 'task-a', ...dual() });
  assert.equal(interval.complete, false, 'analyzeStreams interval packets remain unaccounted until demux adopts them');
  assert.ok(interval.incomplete.some((item) => item.domain === 'demux' && item.reason === 'INTERVAL_UNACCOUNTED'));

  const allInactive = vm.runInNewContext(
    `({ ${sliceMethodFrom(text, head)} })`,
    {
      Ue: {
        IOThread: { getEioDualCacheSnapshot: async () => null },
        DemuxerThread: { getEioDualCacheSnapshot: async () => null },
        MSEThread: { getEioDualCacheSnapshot: async () => null }
      },
      setTimeout,
      clearTimeout
    }
  ).getEioDualCacheSnapshot;
  const inactive = await allInactive.call({ taskId: 'task-a', ...dual() });
  assert.equal(inactive.active, false, 'all-null must never become an active proof');
  assert.equal(inactive.complete, false, 'all-null is not a passing snapshot');
  assert.deepEqual(Array.from(inactive.incomplete, (item) => [item.domain, item.reason]), [
    ['io', 'NO_SNAPSHOT'],
    ['demux', 'NO_SNAPSHOT'],
    ['mse', 'NO_SNAPSHOT']
  ]);

  const nonDual = vm.runInNewContext(
    `({ ${sliceMethodFrom(text, head)} })`,
    {
      Ue: {
        IOThread: { getEioDualCacheSnapshot: async () => null },
        DemuxerThread: { getEioDualCacheSnapshot: async () => null },
        MSEThread: { getEioDualCacheSnapshot: async () => null }
      },
      setTimeout,
      clearTimeout
    }
  ).getEioDualCacheSnapshot;
  const baseline = await nonDual.call({ taskId: 'task-a' });
  assert.equal(baseline.complete, false, 'no opt-in config never yields a complete proof');
  assert.deepEqual(Array.from(baseline.incomplete), []);

  const stale = vm.runInNewContext(
    `({ ${sliceMethodFrom(text, head)} })`,
    {
      Ue: {
        IOThread: { getEioDualCacheSnapshot: () => new Promise((resolve) => setImmediate(() => resolve({ active: true }))) },
        DemuxerThread: demuxNull,
        MSEThread: { getEioDualCacheSnapshot: async () => null }
      },
      setTimeout,
      clearTimeout
    }
  ).getEioDualCacheSnapshot;
  const ctx = { taskId: 'task-a', ...dual() };
  const racing = stale.call(ctx);
  ctx.taskId = 'task-b';
  const moved = await racing;
  assert.equal(moved.io, null);
  assert.equal(moved.complete, false);
  assert.ok(moved.incomplete.some((item) => item.domain === 'io' && item.reason === 'STALE_TASK'));

  const hanging = vm.runInNewContext(
    `({ ${sliceMethodFrom(text, head)} })`,
    {
      Ue: {
        IOThread: ioActive,
        DemuxerThread: demuxNull,
        MSEThread: { getEioDualCacheSnapshot: () => new Promise(() => {}) }
      },
      setTimeout,
      clearTimeout
    }
  ).getEioDualCacheSnapshot;
  const timedOut = await hanging.call({ taskId: 'task-a', ...dual() });
  assert.equal(timedOut.complete, false);
  assert.ok(timedOut.incomplete.some((item) => item.domain === 'mse' && item.reason === 'RPC_TIMEOUT'));
});
