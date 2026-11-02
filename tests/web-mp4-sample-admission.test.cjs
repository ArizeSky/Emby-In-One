const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// These cases execute the real, patched vendored `readAVPacket_` method sliced
// out of public/vendor/web/libmedia/943.avplayer.js (IIsobmffFormat). Only the
// sample selector (module 80529), the allocator / packet accessors
// (46626 / 46564 / 6927 / 56238 / 80209) and the IO reader are faked: the
// optional admission hook, the malloc guard and the post-NX handoff are the
// vendored statements under test.
//
// Scope of this file: the single-call hook primitive only. It does not prove
// the start-up byte budget: DemuxPipeline token bookkeeping (held/resident),
// probe adoption, and the real data-release credit are *not* wired yet.
const BUNDLE = path.join(__dirname, '../public/vendor/web/libmedia/943.avplayer.js');
// Async cases fail fast instead of hanging the file. A timeout is a failure,
// not a substitute for the real assertions.
const ASYNC = { timeout: 2000 };
const DH = BigInt('0x7fffffffffffffff');
const LR = -2;
const EOF = -1048576;

// Naive brace matcher: the sliced method only contains balanced `{}` pairs and
// no template literals, so a depth counter is enough to find the method end.
function sliceMethod(text, head) {
  const start = text.indexOf(head);
  assert.ok(start > -1, `vendored method ${head} is missing from the bundle`);
  let depth = 0;
  for (let i = start; i < text.length; i += 1) {
    const ch = text[i];
    if (ch === '{') depth += 1;
    if (ch === '}') {
      depth -= 1;
      if (depth === 0) return text.slice(start, i + 1);
    }
  }
  throw new Error(`unbalanced vendored method ${head}`);
}

let cachedSource = null;

function hookSource() {
  if (cachedSource) return cachedSource;
  const method = sliceMethod(fs.readFileSync(BUNDLE, 'utf8'), 'async readAVPacket_(e,a){');
  assert.match(method, /eioBeforeSampleAlloc/, 'vendored readAVPacket_ is missing the pre-malloc hook');
  assert.match(method, /eioAfterSampleAlloc/, 'vendored readAVPacket_ is missing the post-NX handoff hook');
  assert.match(method, /eioReleaseSampleAlloc/, 'vendored readAVPacket_ is missing the reservation release hook');
  assert.match(method, /eioIsSampleAllocCancelled/, 'vendored readAVPacket_ has no cancel re-check after the await');
  assert.match(method, /if\(!f&&d>0\)/, 'vendored readAVPacket_ is missing the malloc(0) guard');
  assert.ok(!/finally/.test(method), 'budget must never be released from a finally block');
  cachedSource = '(' + method.replace(/^async readAVPacket_/, 'async function readAVPacket_') + ')';
  return cachedSource;
}

function sample(over = {}) {
  // `size` is the sample byte count from samplesIndex; `duration` is the
  // sample duration in the stream time base, never used to derive bytes.
  return { size: 120, pts: BigInt(0), dts: BigInt(0), duration: 1000, flags: 1, pos: BigInt(4096), ...over };
}

const tick = () => new Promise(resolve => setImmediate(resolve));

// A tiny linear memory + allocator so the real vendored packet writes and the
// real hook call sites run unchanged. Freed pointers are never reused, so a
// stale or duplicated free is observable instead of silently aliasing.
function harness(options = {}) {
  const HEAP = new ArrayBuffer(1 << 20);
  const bytes = new Uint8Array(HEAP);
  const view = new DataView(HEAP);
  const samples = options.samples || [sample()];
  const live = new Map();
  const state = {
    events: [], live, released: [], tokens: [], ioViews: [],
    allocatedPointers: [], freedPointers: [], packetPointers: [],
    allocCalls: 0, freeCalls: 0, doubleFree: 0, getSampleCalls: 0, cursor: 0,
  };
  let bump = 1 << 15;
  let struct = 0;
  let ioPosition = samples[0].pos;

  function alloc(size) {
    state.allocCalls += 1;
    state.events.push(`malloc:${size}`);
    if (options.mallocFails) return 0;
    const ptr = bump;
    bump += (Math.max(size, 1) + 15) & ~15;
    live.set(ptr, size);
    state.allocatedPointers.push(ptr);
    return ptr;
  }

  function free(ptr) {
    state.freeCalls += 1;
    state.freedPointers.push(ptr);
    state.events.push(`free:${ptr}`);
    if (live.has(ptr)) live.delete(ptr);
    else state.doubleFree += 1;
  }

  const stream = {
    index: 0,
    codecpar: { flags: 0, codecId: options.codecId || 27 },
    timeBase: { num: 1, den: 1000 },
    startTime: DH,
    sideData: [],
    disposition: 0,
    privData: { currentSample: 0, samplesIndex: samples },
  };

  const ioReader = {
    flags: 0,
    error: 0,
    getPos: () => ioPosition,
    skip: async n => { state.events.push(`skip:${n}`); },
    seek: async p => { state.events.push(`seek:${p}`); },
    readBuffer: async (n, target) => {
      state.events.push(`read:${n}`);
      state.ioViews.push(target);
      if (options.ioError) throw new Error('io failure after NX');
      return target;
    },
  };

  const fc = { streams: [stream], ioReader, interval: { packetBuffer: [] }, metadata: {}, duration: DH };

  function getSample() {
    state.getSampleCalls += 1;
    state.events.push('getSample');
    const current = samples[state.cursor];
    state.cursor += 1;
    stream.privData.currentSample = state.cursor;
    if (!current) return { sample: undefined };
    ioPosition = current.pos;
    return { sample: current, stream, encryption: undefined };
  }

  // Mirrors 46564 `NX`/`M` field order: buffer handle at packet+0 first, then
  // data at packet+24, then size at packet+28.
  //
  // All three stores go through 6927 `M`: type 20 is a 32-bit write for both
  // wasm pointers (buffer handle and packet.data), type 15 is a 32-bit write
  // for the size. Of the types this fixture mirrors, only 17 is a 64-bit
  // write (timestamps/pos). Writing the pointers with setBigInt64 would spill
  // packet.data over packet+28 and make pointer reads return
  // `(size << 32) | ptr` instead of the pointer.
  function NX(packet, data, size) {
    state.events.push(`nx:${data}`);
    if (options.nxThrows) {
      if (options.nxWritesBuf) view.setUint32(packet, data + 0x1000000, true);
      if (options.nxPartial) view.setUint32(packet + 24, data, true);
      throw new Error('av_buffer_create failed');
    }
    view.setUint32(packet, data + 0x1000000, true);
    view.setUint32(packet + 24, data, true);
    view.setInt32(packet + 28, size, true);
  }

  const sandbox = {
    d: { G: getSample },
    g: { NX, _5: () => 0, Ow: () => { state.events.push('side-data'); }, rN() {}, Up() {} },
    h: {
      sY: alloc,
      Eb: free,
      Gy: size => {
        const ptr = alloc(size);
        if (ptr) bytes.fill(0, ptr, ptr + size);
        return ptr;
      },
    },
    m: {
      JW: (ptr, size) => bytes.subarray(ptr, ptr + size),
      lW: (ptr, size, src) => bytes.set(src.subarray(0, size), ptr),
      Mr: (dst, src, size) => bytes.copyWithin(dst, src, src + size),
      s3: (ptr, size) => bytes.slice(ptr, ptr + size),
      sO: (ptr, value, size) => bytes.fill(value, ptr, ptr + size),
    },
    r: {
      M: {
        // 6927 write table: only type 17 is write64; 2/15/20/25 are write8/32.
        2: (ptr, value) => view.setUint8(ptr, value),
        15: (ptr, value) => view.setInt32(ptr, value, true),
        17: (ptr, value) => view.setBigInt64(ptr, BigInt(value), true),
        20: (ptr, value) => view.setUint32(ptr, value, true),
        25: (ptr, value) => view.setUint32(ptr, value, true),
      },
    },
    s: {
      f: {
        // 56238 read table: f[20] is `read32(e)>>>0` (an unsigned pointer),
        // f[2] is `readU8`, f[15]/f[25] are 32-bit and f[17] is the only
        // 64-bit read here.
        2: ptr => view.getUint8(ptr),
        15: ptr => view.getInt32(ptr, true),
        17: ptr => view.getBigInt64(ptr, true),
        20: ptr => view.getUint32(ptr, true),
        25: ptr => view.getUint32(ptr, true),
      },
    },
    u: { LR, E$: -1 },
    n: {
      A: tag => {
        let value = 0;
        for (let i = 0; i < 4; i += 1) value = (value << 8) | tag.charCodeAt(i);
        return value;
      },
    },
    z: { Dh: DH },
    v: { Sg: () => 0 },
    w: { zz: () => [] },
    I: { z3: () => {}, R8: () => {} },
    b: 'web-mp4-sample-admission.test.cjs',
  };

  const run = async () => {
    const fn = vm.runInNewContext(hookSource(), sandbox);
    const packet = struct;
    struct += 128;
    bytes.fill(0, packet, packet + 128);
    state.packetPointers.push(packet);
    const self = { context: { currentFragment: null }, firstAfterSeek: false };
    const result = await fn.call(self, fc, packet);
    return { result, packet };
  };

  return { fc, stream, state, samples, run, view, heap: HEAP };
}

// Stand-in for the future DemuxPipeline ledger: records what the vendored call
// site hands over; no credit is ever invented here.
function attachLedger(h) {
  const token = { id: `tok-${h.state.tokens.length + 1}`, state: 'RESERVED' };
  h.state.tokens.push(token);
  h.fc.eioBeforeSampleAlloc = args => {
    h.state.events.push('before');
    h.state.beforeArgs = args;
    return token;
  };
  h.fc.eioAfterSampleAlloc = (handed, info) => {
    h.state.events.push('after');
    h.state.afterInfo = info;
    assert.equal(handed, token, 'after hook must receive the same token object');
    token.state = 'HANDED_OFF';
  };
  h.fc.eioReleaseSampleAlloc = (released, info) => {
    h.state.events.push(`release:${info.reason}`);
    h.state.released.push({ token: released, info });
    assert.equal(released, token, 'release hook must receive the same token object');
  };
  return token;
}

test('without hooks the read path keeps malloc/NX/read order and returns 0', ASYNC, async () => {
  const h = harness({ samples: [sample({ size: 400, pts: BigInt(1000), dts: BigInt(900) })] });
  const { result, packet } = await h.run();
  const [data] = h.state.allocatedPointers;
  assert.equal(result, 0);
  assert.deepEqual(h.state.events, ['getSample', 'malloc:400', `nx:${data}`, 'read:400']);
  assert.equal(h.state.ioViews.length, 1);
  assert.equal(h.state.ioViews[0].buffer, h.heap);
  assert.equal(h.state.ioViews[0].byteOffset, data);
  assert.equal(h.state.ioViews[0].length, 400);
  assert.equal(h.view.getUint32(packet + 24, true), data);
  assert.equal(h.view.getInt32(packet + 28, true), 400);
  assert.equal(h.view.getUint32(packet + 28, true), 400, 'the data pointer must not spill into the 32-bit size field');
  assert.equal(h.state.freeCalls, 0);
  assert.equal(h.state.released.length, 0);
  assert.equal(h.state.getSampleCalls, 1);
  assert.equal(h.state.cursor, 1);
  assert.equal(h.state.doubleFree, 0);
});

test('hook resolves before malloc; handoff is synchronous and precedes IO', ASYNC, async () => {
  const h = harness({ samples: [sample({ size: 300, pts: BigInt(7000), dts: BigInt(6000), duration: 1500 })] });
  let resolveHook;
  const token = { id: 'tok-1', bytes: 300 };
  h.fc.eioBeforeSampleAlloc = args => {
    h.state.beforeArgs = args;
    h.state.events.push('before');
    return new Promise(resolve => { resolveHook = resolve; });
  };
  h.fc.eioAfterSampleAlloc = (handed, info) => {
    h.state.events.push('after');
    assert.equal(handed, token);
    assert.equal(info.size, 300);
    assert.equal(info.data, h.state.allocatedPointers[0]);
    assert.equal(h.state.live.has(info.data), true, 'data must be resident at handoff');
    assert.equal(h.view.getUint32(info.packet + 24, true), info.data);
    assert.equal(h.view.getInt32(info.packet + 28, true), 300);
    assert.equal(h.view.getUint32(info.packet + 28, true), 300, 'the data pointer must not spill into the 32-bit size field');
  };
  h.fc.eioReleaseSampleAlloc = (released, info) => {
    h.state.events.push(`release:${info.reason}`);
    h.state.released.push({ released, info });
  };

  const pending = h.run();
  await tick();
  await tick();
  assert.equal(h.state.allocCalls, 0, 'no sample buffer may be allocated while the hook is pending');
  assert.deepEqual(h.state.events, ['getSample', 'before']);
  assert.equal(h.state.getSampleCalls, 1);
  assert.equal(h.state.cursor, 1);
  assert.equal(h.state.beforeArgs.size, 300, 'hook size is the sample byte size, not a time-derived value');
  assert.equal(h.state.beforeArgs.pts, BigInt(7000));
  assert.equal(h.state.beforeArgs.dts, BigInt(6000));
  assert.equal(h.state.beforeArgs.duration, BigInt(1500));
  assert.equal(h.state.beforeArgs.timeBase.num, 1);
  assert.equal(h.state.beforeArgs.timeBase.den, 1000);
  assert.equal(h.state.beforeArgs.packet, h.state.packetPointers[0]);
  assert.notEqual(h.state.beforeArgs.size, Number(h.state.beforeArgs.duration));

  resolveHook(token);
  const { result } = await pending;
  const [data] = h.state.allocatedPointers;
  assert.equal(result, 0);
  assert.deepEqual(h.state.events, ['getSample', 'before', 'malloc:300', `nx:${data}`, 'after', 'read:300']);
  assert.equal(h.state.freeCalls, 0);
  assert.equal(h.state.released.length, 0, 'a handed-off packet must not be credited by the format layer');
  assert.equal(h.state.doubleFree, 0);
});

test('hook rejection ends the old read without malloc, re-sample or pressure EOF', ASYNC, async () => {
  const h = harness({ samples: [sample({ size: 256 }), sample({ size: 257, pts: BigInt(1000) })] });
  h.fc.eioBeforeSampleAlloc = () => {
    h.state.events.push('before');
    return Promise.reject(new Error('seek cancelled the pending operation'));
  };
  h.fc.eioAfterSampleAlloc = () => { h.state.events.push('after'); };
  h.fc.eioReleaseSampleAlloc = (token, info) => {
    h.state.events.push(`release:${info.reason}`);
    h.state.released.push({ token, info });
  };

  const { result, packet } = await h.run();
  assert.equal(result, LR);
  assert.notEqual(result, EOF, 'cancellation must not look like a demuxer EOF');
  assert.equal(result > 0, false);
  assert.deepEqual(h.state.events, ['getSample', 'before']);
  assert.equal(h.state.allocCalls, 0);
  assert.equal(h.state.freeCalls, 0);
  assert.equal(h.state.ioViews.length, 0);
  assert.equal(h.state.released.length, 0, 'the caller owns its reservation when it rejects the hook');
  assert.equal(h.state.getSampleCalls, 1, 'a rejected hook must not re-select the sample');
  assert.equal(h.state.cursor, 1, 'the cursor already advanced exactly once inside the same call');
  assert.equal(h.view.getUint32(packet + 24, true), 0);
});

test('cancel decided between hook resolve and resume never allocates', ASYNC, async () => {
  const h = harness({ samples: [sample({ size: 300 })] });
  const token = { id: 'tok-cancel' };
  let resolveHook;
  let cancelled = false;
  h.fc.eioBeforeSampleAlloc = () => {
    h.state.events.push('before');
    return new Promise(resolve => { resolveHook = resolve; });
  };
  h.fc.eioAfterSampleAlloc = () => { h.state.events.push('after'); };
  h.fc.eioReleaseSampleAlloc = (released, info) => {
    h.state.events.push(`release:${info.reason}`);
    h.state.released.push({ released, info });
  };
  h.fc.eioIsSampleAllocCancelled = tok => {
    h.state.events.push('isCancelled');
    assert.equal(tok, token, 'the cancel check must see the resolved token');
    return cancelled;
  };

  const pending = h.run();
  await tick();
  assert.equal(h.state.allocCalls, 0);
  // Race window: resolve the hook, then mark the generation stale before the
  // awaiting continuation gets to run. Rejection cannot express this, so the
  // explicit cancel check is the contract.
  resolveHook(token);
  cancelled = true;
  const { result } = await pending;
  assert.equal(result, LR);
  assert.deepEqual(h.state.events, ['getSample', 'before', 'isCancelled', 'release:cancelled']);
  assert.equal(h.state.allocCalls, 0, 'a stale generation must not allocate');
  assert.equal(h.state.ioViews.length, 0);
  assert.equal(h.state.released.length, 1);
  assert.equal(h.state.released[0].info.reason, 'cancelled');
  assert.equal(h.state.getSampleCalls, 1);
  assert.equal(h.state.cursor, 1);
  assert.equal(h.state.doubleFree, 0);
});

test('a false cancel check proceeds normally', ASYNC, async () => {
  const h = harness({ samples: [sample({ size: 100 })] });
  attachLedger(h);
  h.fc.eioIsSampleAllocCancelled = () => {
    h.state.events.push('isCancelled');
    return false;
  };
  const { result } = await h.run();
  const [data] = h.state.allocatedPointers;
  assert.equal(result, 0);
  assert.deepEqual(h.state.events, ['getSample', 'before', 'isCancelled', 'malloc:100', `nx:${data}`, 'after', 'read:100']);
  assert.equal(h.state.released.length, 0);
  assert.equal(h.state.freeCalls, 0);
});

test('a present before hook without after/release (or a cancelled check) is a protocol error', ASYNC, async () => {
  const cases = [
    fc => { fc.eioBeforeSampleAlloc = () => ({ id: 1 }); fc.eioReleaseSampleAlloc = () => {}; },
    fc => { fc.eioBeforeSampleAlloc = () => ({ id: 1 }); fc.eioAfterSampleAlloc = () => {}; },
    fc => {
      fc.eioBeforeSampleAlloc = () => ({ id: 1 });
      fc.eioAfterSampleAlloc = () => {};
      fc.eioReleaseSampleAlloc = () => {};
      fc.eioIsSampleAllocCancelled = 'not-a-function';
    },
    fc => { fc.eioBeforeSampleAlloc = 'not-a-function'; },
    fc => { fc.eioBeforeSampleAlloc = null; },
    fc => { fc.eioAfterSampleAlloc = () => {}; },
    fc => { fc.eioReleaseSampleAlloc = () => {}; },
    fc => { fc.eioIsSampleAllocCancelled = () => false; },
    fc => {
      fc.eioBeforeSampleAlloc = () => ({ id: 1 });
      fc.eioAfterSampleAlloc = () => {};
      fc.eioReleaseSampleAlloc = () => {};
      fc.eioIsSampleAllocCancelled = null;
    },
  ];
  for (const setup of cases) {
    const h = harness({ samples: [sample({ size: 100 })] });
    setup(h.fc);
    const { result } = await h.run();
    assert.equal(result, LR);
    assert.deepEqual(h.state.events, ['getSample'], 'the protocol must be validated before any hook runs');
    assert.equal(h.state.allocCalls, 0);
    assert.equal(h.state.ioViews.length, 0);
    assert.equal(h.state.released.length, 0);
  }
});

test('a hook that resolves null/undefined is a protocol error, not a silent opt-out', ASYNC, async () => {
  for (const value of [undefined, null]) {
    const h = harness({ samples: [sample({ size: 200 })] });
    attachLedger(h);
    h.fc.eioBeforeSampleAlloc = () => {
      h.state.events.push('before');
      return value;
    };
    const { result } = await h.run();
    assert.equal(result, LR);
    assert.notEqual(result, EOF);
    assert.deepEqual(h.state.events, ['getSample', 'before']);
    assert.equal(h.state.allocCalls, 0, 'a protocol violation must not allocate');
    assert.equal(h.state.released.length, 0, 'there is no token to release');
    assert.equal(h.state.getSampleCalls, 1);
    assert.equal(h.state.cursor, 1);
  }
});

test('token 0 is a valid opaque token', ASYNC, async () => {
  const h = harness({ samples: [sample({ size: 128 })] });
  let handed = null;
  h.fc.eioBeforeSampleAlloc = () => {
    h.state.events.push('before');
    return 0;
  };
  h.fc.eioAfterSampleAlloc = (token, info) => {
    h.state.events.push('after');
    handed = token;
    assert.equal(token, 0, 'a falsy token is still a token when it is not null/undefined');
    assert.equal(info.size, 128);
  };
  h.fc.eioReleaseSampleAlloc = () => { h.state.events.push('release'); };
  const { result } = await h.run();
  const [data] = h.state.allocatedPointers;
  assert.equal(result, 0);
  assert.equal(handed, 0);
  assert.deepEqual(h.state.events, ['getSample', 'before', 'malloc:128', `nx:${data}`, 'after', 'read:128']);
  assert.equal(h.state.released.length, 0);
  assert.equal(h.state.freeCalls, 0);
});

test('malloc failure neither NXes nor reads: token is released and an error is returned', ASYNC, async () => {
  const withHook = harness({ samples: [sample({ size: 512 })], mallocFails: true });
  const token = attachLedger(withHook);
  const { result, packet } = await withHook.run();
  assert.equal(result, LR);
  assert.notEqual(result, EOF);
  assert.deepEqual(withHook.state.events, ['getSample', 'before', 'malloc:512', 'release:malloc-failed']);
  assert.equal(withHook.state.released.length, 1);
  assert.equal(withHook.state.released[0].token, token);
  assert.equal(withHook.state.released[0].info.size, 512);
  assert.equal(withHook.state.freeCalls, 0, 'nothing was allocated, so nothing may be freed');
  assert.equal(withHook.state.ioViews.length, 0);
  assert.equal(withHook.view.getUint32(packet + 24, true), 0);
  assert.equal(withHook.state.getSampleCalls, 1);

  const withoutHook = harness({ samples: [sample({ size: 512 })], mallocFails: true });
  const plain = await withoutHook.run();
  assert.equal(plain.result, LR, 'the malloc guard is the one behaviour change on the hookless path');
  assert.deepEqual(withoutHook.state.events, ['getSample', 'malloc:512']);
  assert.equal(withoutHook.state.freeCalls, 0);
  assert.equal(withoutHook.state.doubleFree, 0);
});

test('NX failure frees and credits nothing: ownership is unprovable, the reservation stays', ASYNC, async () => {
  for (const mode of [{ nxThrows: true }, { nxThrows: true, nxPartial: true }, { nxThrows: true, nxWritesBuf: true }]) {
    const h = harness({ samples: [sample({ size: 900 })], ...mode });
    attachLedger(h);
    await assert.rejects(h.run(), /av_buffer_create failed/);
    const [data] = h.state.allocatedPointers;
    assert.deepEqual(h.state.events, ['getSample', 'before', 'malloc:900', `nx:${data}`]);
    assert.equal(h.state.freeCalls, 0, 'NX may already own the data: never free it here');
    assert.equal(h.state.released.length, 0, 'unknown ownership must not credit (over-counting is the safe side)');
    assert.equal(h.state.live.has(data), true, 'possible leak, disclosed in the step notes');
    assert.equal(h.state.ioViews.length, 0);
    assert.equal(h.state.doubleFree, 0);
    assert.equal(h.state.getSampleCalls, 1);
    assert.equal(h.state.cursor, 1);
  }
});

test('NX that wrote the buffer handle but not packet.data still frees and credits nothing', ASYNC, async () => {
  const h = harness({ samples: [sample({ size: 640 })], nxThrows: true, nxWritesBuf: true });
  attachLedger(h);
  await assert.rejects(h.run(), /av_buffer_create failed/);
  const [packet] = h.state.packetPointers;
  const [data] = h.state.allocatedPointers;
  assert.notEqual(h.view.getUint32(packet, true), 0, 'the buffer handle is already stored in the packet');
  assert.equal(h.view.getUint32(packet + 24, true), 0, 'packet.data was not written yet');
  assert.equal(h.state.freeCalls, 0);
  assert.equal(h.state.released.length, 0);
  assert.equal(h.state.live.has(data), true);
  assert.equal(h.state.doubleFree, 0);
});

test('IO failure after a successful NX frees nothing and never credits early', ASYNC, async () => {
  const h = harness({ samples: [sample({ size: 1024 })], ioError: true });
  attachLedger(h);
  await assert.rejects(h.run(), /io failure after NX/);
  const [data] = h.state.allocatedPointers;
  assert.deepEqual(h.state.events, ['getSample', 'before', 'malloc:1024', `nx:${data}`, 'after', 'read:1024']);
  assert.equal(h.state.freeCalls, 0, 'the packet owns the buffer; the format layer must not free it');
  assert.equal(h.state.released.length, 0, 'budget stays held until the real data release notification');
  assert.equal(h.state.live.has(data), true);
  assert.equal(h.state.ioViews[0].byteOffset, data);
  assert.equal(h.state.getSampleCalls, 1);
  assert.equal(h.state.doubleFree, 0);
});

test('a zero-length sample keeps the original vendor semantics', ASYNC, async () => {
  const plain = harness({ samples: [sample({ size: 0, duration: 0 })] });
  const plainRun = await plain.run();
  const [plainData] = plain.state.allocatedPointers;
  assert.equal(plainRun.result, 0);
  assert.deepEqual(plain.state.events, ['getSample', 'malloc:0', `nx:${plainData}`, 'read:0']);
  assert.equal(plain.state.ioViews[0].length, 0);
  assert.equal(plain.state.freeCalls, 0);

  // A zero-size request may legitimately come back as pointer 0: that must not
  // be turned into an allocation-failure error on the hookless path.
  const zeroPtr = harness({ samples: [sample({ size: 0, duration: 0 })], mallocFails: true });
  const zeroRun = await zeroPtr.run();
  assert.equal(zeroRun.result, 0);
  assert.deepEqual(zeroPtr.state.events, ['getSample', 'malloc:0', 'nx:0', 'read:0']);
  assert.equal(zeroPtr.state.freeCalls, 0);

  // With a hook the zero-byte reservation is returned before malloc and is not
  // handed off, so `data === 0` can never become an ambiguous ledger key.
  const hooked = harness({ samples: [sample({ size: 0, duration: 0 })] });
  const token = attachLedger(hooked);
  const hookedRun = await hooked.run();
  assert.equal(hookedRun.result, 0);
  assert.deepEqual(hooked.state.events.slice(0, 3), ['getSample', 'before', 'release:zero-size']);
  assert.equal(hooked.state.released.length, 1);
  assert.equal(hooked.state.released[0].token, token);
  assert.equal(hooked.state.released[0].info.reason, 'zero-size');
  assert.equal(hooked.state.events.includes('after'), false);
  assert.equal(hooked.state.freeCalls, 0);
  assert.equal(hooked.state.doubleFree, 0);
});

test('sequential reads keep one sample selection, one token and one handoff per call', ASYNC, async () => {
  const h = harness({
    samples: [sample({ size: 100, pts: BigInt(0) }), sample({ size: 150, pts: BigInt(1000), pos: BigInt(8192) })],
  });
  h.state.tokens = [];
  const seen = [];
  let nextId = 0;
  h.fc.eioBeforeSampleAlloc = args => {
    nextId += 1;
    const token = { id: nextId, bytes: args.size, state: 'RESERVED' };
    h.state.events.push(`before:${token.id}`);
    seen.push(args.size);
    return token;
  };
  h.fc.eioAfterSampleAlloc = (token, info) => {
    h.state.events.push(`after:${token.id}`);
    token.state = 'HANDED_OFF';
    token.data = info.data;
    h.state.tokens.push(token);
  };
  h.fc.eioReleaseSampleAlloc = () => { h.state.events.push('release'); };

  const first = await h.run();
  const second = await h.run();
  const [firstData, secondData] = h.state.allocatedPointers;
  assert.equal(first.result, 0);
  assert.equal(second.result, 0);
  assert.notEqual(first.packet, second.packet);
  assert.notEqual(firstData, secondData);
  assert.deepEqual(seen, [100, 150]);
  assert.equal(h.state.tokens.length, 2);
  assert.deepEqual(h.state.tokens.map(token => token.data), [firstData, secondData]);
  assert.equal(h.state.getSampleCalls, 2);
  assert.equal(h.state.cursor, 2);
  assert.deepEqual(h.state.events, [
    'getSample', 'before:1', 'malloc:100', `nx:${firstData}`, 'after:1', 'read:100',
    'getSample', 'before:2', 'malloc:150', `nx:${secondData}`, 'after:2', 'read:150',
  ]);
  assert.equal(h.state.freeCalls, 0);
  assert.equal(h.state.doubleFree, 0);
});
