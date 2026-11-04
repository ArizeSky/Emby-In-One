'use strict';

/*
 * Narrow N1 method-scope tests for eioCloneStreamCodecpar(task, source) in
 * public/vendor/web/libmedia/26.avplayer.js.
 *
 * Every clone assertion executes the REAL shipped method sliced out of module
 * 26. The admission/credit assertions use the REAL canonical
 * web-src/player-codecpar-leases.js helper, the REAL owned-resource wrapper
 * and the REAL shared-media ownership core. The native group also runs the REAL
 * vendored module 67669 Yi()/dn() over fake WASM memory and a fake allocator;
 * the serialized group runs the REAL module 26669 T8 (snapshot -> T8 write) and
 * the REAL module 67669 eioCodecparAdopt()/dn() over the same fake memory.
 *
 * This is method scope only, NOT Worker/native playback proof. There is no
 * Worker, MSE, BSF runtime, real upstream, production instance or native media.
 * The only non-real pieces are the fake allocator/memory, the read-only source
 * scanner in the native ordering case, and the destination pointer returned by
 * the fake Gy wrapper. No test calls the running application.
 */

const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const ROOT = path.join(__dirname, '..');
const MSE_PATH = path.join(ROOT, 'public', 'vendor', 'web', 'libmedia', '26.avplayer.js');
const AVPLAYER_PATH = path.join(ROOT, 'public', 'vendor', 'web', 'libmedia', 'avplayer.js');
const HELPER_PATH = path.join(ROOT, 'web-src', 'player-codecpar-leases.js');
const core = require('../web-src/player-shared-media-ownership.js');
const owned = require('../web-src/player-owned-media-resources.js');
const codecpar = require('../web-src/player-codecpar-leases.js');

const mseSource = fs.readFileSync(MSE_PATH, 'utf8');
const avplayerSource = fs.readFileSync(AVPLAYER_PATH, 'utf8');
const helperSource = fs.readFileSync(HELPER_PATH, 'utf8');

const cloneStart = mseSource.indexOf('eioCloneStreamCodecpar(task,source){');
assert.ok(cloneStart >= 0, 'actual eioCloneStreamCodecpar method');
const cloneEnd = mseSource.indexOf('\neioCreateWriter(t,e){', cloneStart);
assert.ok(cloneEnd > cloneStart, 'actual method end boundary');
const CLONE_METHOD = mseSource.slice(cloneStart, cloneEnd).trim();
assert.ok(CLONE_METHOD.endsWith('}'), 'complete method body');

function codedError(code) {
  const error = new Error(code);
  error.code = code;
  return error;
}

function loadCloneMethod(dependencies) {
  const sandbox = Object.assign({ eioMseError: codedError }, dependencies);
  return vm.runInNewContext('new (class {' + CLONE_METHOD + '})()', sandbox);
}

/* REAL owned-resource wrapper over the REAL shared-media ownership core. */
function makeRealResources(options) {
  options = options || {};
  const control = core.create({
    capacityBytes: options.capacityBytes === undefined ? 4096 : options.capacityBytes,
    maxEntries: options.maxEntries === undefined ? 8 : options.maxEntries
  });
  const releaseOrder = [];
  const controller = {
    reserve: (bytes, generation) => control.reserve(bytes, generation),
    bindOwnedResource: (token) => control.bindOwnedResource(token),
    snapshot: () => control.snapshot(),
    releaseOwnedResource(handle, proof) {
      releaseOrder.push(options.freeCount ? options.freeCount() : null);
      return control.releaseOwnedResource(handle, proof);
    }
  };
  const resources = owned.createOwnedResources(controller);
  return { control, resources, releaseOrder };
}

test('actual method preserves baseline pointer/serialized order without helper (method scope only; not Worker proof)',
  { timeout: 5000 }, () => {
    const calls = [];
    let gy = 0;
    let requireCalls = 0;
    let measured = 0;
    const api = loadCloneMethod({
      l: {
        Gy(size) {
          calls.push(['Gy', size]);
          gy += 1;
          return 0x1200 + gy;
        }
      },
      y: {
        A(value) {
          calls.push(['A', value]);
          return value === 0x400;
        }
      },
      g: {
        eioPayloadBytes() {
          measured += 1;
          throw new Error('baseline must not measure');
        },
        Yi(dest, source) {
          calls.push(['Yi', dest, source, arguments.length]);
          return true;
        }
      },
      p: {
        T8(source, dest) {
          calls.push(['T8', source, dest]);
          return true;
        }
      },
      i() {
        requireCalls += 1;
        throw new Error('baseline must not require module 90191');
      }
    });

    assert.equal(api.eioCloneStreamCodecpar({}, 0x400), 0x1201);
    assert.equal(api.eioCloneStreamCodecpar({}, 'serialized-input'), 0x1202);
    assert.deepEqual(calls, [
      ['Gy', 168],
      ['A', 0x400],
      ['Yi', 0x1201, 0x400, 2],
      ['Gy', 168],
      ['A', 'serialized-input'],
      ['T8', 'serialized-input', 0x1202]
    ]);
    assert.equal(gy, 2);
    assert.equal(requireCalls, 0, 'no eioOwnedResources keeps the original no-helper path');
    assert.equal(measured, 0, 'baseline performs no native payload scan');
  });

test('strict native pointer admits into the REAL core before Gy and binds at Yi (method scope only)',
  { timeout: 5000 }, () => {
    const real = makeRealResources({ capacityBytes: 64 });
    const task = { eioOwnedResources: real.resources, eioOwnedGeneration: 7 };
    const calls = [];
    let requireCalls = 0;
    let measured = 0;
    let gy = 0;
    const api = loadCloneMethod({
      l: {
        Gy(size) {
          const acquired = task.eioCodecparLeases.snapshot().acquiredCount;
          calls.push(['Gy', size, real.control.snapshot().heldBytes]);
          assert.equal(size, 168);
          assert.equal(requireCalls, 1, 'lazy helper exists before the native allocation');
          assert.equal(typeof task.eioCodecparLeases, 'function');
          assert.equal(acquired, gy + 1);
          assert.equal(real.control.snapshot().heldBytes, acquired * 7,
            'the real core already holds the measured payload before Gy');
          assert.equal(real.control.snapshot().countBound, acquired);
          gy += 1;
          return 0x2200 + gy;
        }
      },
      y: {
        A(value) {
          return value === 0x400;
        }
      },
      g: {
        eioPayloadBytes(pointer) {
          measured += 1;
          assert.equal(pointer, 0x400);
          return 7;
        },
        Yi(dest, source, lease) {
          calls.push(['Yi', dest, source, lease.bytes]);
          assert.equal(real.control.snapshot().heldBytes, (gy) * 7,
            'the admission charge survives the native copy');
          assert.equal(lease.bind({ copiedBytes: 7 }), true);
          return true;
        }
      },
      p: {
        T8() {
          throw new Error('strict pointer path must not serialize');
        }
      },
      i(id) {
        requireCalls += 1;
        assert.equal(id, 90191);
        return codecpar;
      }
    });

    assert.equal(api.eioCloneStreamCodecpar(task, 0x400), 0x2201);
    assert.deepEqual(calls, [
      ['Gy', 168, 7],
      ['Yi', 0x2201, 0x400, 7]
    ]);
    assert.deepEqual([
      real.control.snapshot().heldBytes,
      real.control.snapshot().countBound,
      task.eioCodecparLeases.snapshot().liveBytes,
      task.eioCodecparLeases.snapshot().boundCount
    ], [7, 1, 7, 1]);

    assert.equal(api.eioCloneStreamCodecpar(task, 0x400), 0x2202);
    assert.equal(requireCalls, 1, 'one lazy helper per task across addStream/reAddStream clones');
    assert.equal(measured, 2);
    assert.equal(task.eioCodecparLeases.snapshot().acquiredCount, 2);
    assert.equal(task.eioCodecparLeases.snapshot().liveBytes, 14);
    assert.deepEqual([real.control.snapshot().heldBytes, real.control.snapshot().countBound],
      [14, 2]);
    assert.deepEqual(calls.slice(2), [
      ['Gy', 168, 14],
      ['Yi', 0x2202, 0x400, 7]
    ]);
  });

/* The serialized-input contract changed in P1c: the strict path is now
 * measured, admitted before Gy(168), written by the real module 26669 T8 from a
 * frozen snapshot and adopted into the real dn() credit. Its regression case
 * lives in the serialized clone section at the end of this file; the baseline
 * (no shared domain) half stays asserted by the first test above. */

test('strict native Gy(168) failure retains the REAL charge and never frees/resets (method scope only)',
  { timeout: 5000 }, () => {
    const real = makeRealResources({ capacityBytes: 64 });
    const task = { eioOwnedResources: real.resources, eioOwnedGeneration: 1 };
    let requireCalls = 0;
    let measured = 0;
    const api = loadCloneMethod({
      l: {
        Gy(size) {
          assert.equal(size, 168);
          assert.equal(real.control.snapshot().heldBytes, 5);
          return 0;
        }
      },
      y: { A: (value) => value === 0x500 },
      g: {
        eioPayloadBytes(pointer) {
          measured += 1;
          assert.equal(pointer, 0x500);
          return 5;
        },
        Yi() {
          throw new Error('no native copy after a zero destination allocation');
        }
      },
      p: { T8() {} },
      i(id) {
        requireCalls += 1;
        assert.equal(id, 90191);
        return codecpar;
      }
    });

    assert.throws(() => api.eioCloneStreamCodecpar(task, 0x500),
      (error) => error.code === 'EIO_CODECPAR_ALLOC_FAILED');
    assert.equal(requireCalls, 1);
    assert.equal(measured, 1);
    assert.deepEqual([real.control.snapshot().heldBytes, real.control.snapshot().countBound],
      [5, 1], 'the admitted real charge is retained');
    const snapshot = task.eioCodecparLeases.snapshot();
    assert.deepEqual([snapshot.retainedCount, snapshot.releasedCount, snapshot.liveBytes],
      [1, 0, 5], 'no fake free, release or credit reset');
    assert.equal(snapshot.faults.some((fault) => fault.code === 'EIO_CODECPAR_CLONE_UNPROVEN'),
      true);
  });

/* -------------------------------------------------------------------------
 * REAL module 67669 over fake WASM memory: native free -> lease credit.
 * The extractor/anchor and fake allocator intentionally mirror
 * web-codecpar-persistent-leases.test.cjs so both suites read the same bundle.
 * ---------------------------------------------------------------------- */

const NATIVE = Object.freeze({
  dest: 0x100,
  src: 0x400,
  extra: 0x800,
  sideTable: 0x900,
  sidePayload: 0xa00
});
const NATIVE_MEMORY_SIZE = 0x20000;

function module67669Text(text) {
  const start = text.indexOf('67669:(e,t,i)=>{');
  assert.ok(start >= 0, 'module 67669 not found');
  const end = text.indexOf('},67994:', start);
  assert.ok(end > start, 'module 67669 end not found');
  return text.slice(start + '67669:'.length, end + 1);
}

function makeNativeMemory() {
  const buffer = new Uint8Array(NATIVE_MEMORY_SIZE);
  const view = new DataView(buffer.buffer);
  let bump = 0x3000;
  let allocCalls = 0;
  const memory = {
    buffer,
    view,
    allocs: [],
    frees: [],
    failedAllocNumbers: new Set(),
    read(pointer, size) {
      if (!Number.isInteger(pointer) || pointer < 0 || !Number.isInteger(size) ||
          size < 0 || pointer + size > NATIVE_MEMORY_SIZE) {
        throw new RangeError('fake native memory read out of range');
      }
    },
    write(pointer, size) {
      if (!Number.isInteger(pointer) || pointer < 0 || !Number.isInteger(size) ||
          size < 0 || pointer + size > NATIVE_MEMORY_SIZE) {
        throw new RangeError('fake native memory write out of range');
      }
    },
    getI8(pointer) { this.read(pointer, 1); return view.getInt8(pointer); },
    getI32(pointer) { this.read(pointer, 4); return view.getInt32(pointer, true); },
    getU32(pointer) { this.read(pointer, 4); return view.getUint32(pointer, true); },
    getBigInt64(pointer) { this.read(pointer, 8); return view.getBigInt64(pointer, true); },
    getF64(pointer) { this.read(pointer, 8); return view.getFloat64(pointer, true); },
    setI8(pointer, value) { this.write(pointer, 1); view.setInt8(pointer, value); },
    setI32(pointer, value) { this.write(pointer, 4); view.setInt32(pointer, value, true); },
    setU32(pointer, value) { this.write(pointer, 4); view.setUint32(pointer, value, true); },
    setBigInt64(pointer, value) {
      this.write(pointer, 8);
      view.setBigInt64(pointer, value, true);
    },
    setF64(pointer, value) {
      this.write(pointer, 8);
      view.setFloat64(pointer, value, true);
    },
    copy(dest, source, size) {
      this.read(source, size);
      this.write(dest, size);
      buffer.copyWithin(dest, source, source + size);
    },
    memset(pointer, value, size) {
      this.write(pointer, size);
      buffer.fill(value, pointer, pointer + size);
    },
    allocate(size, how) {
      allocCalls += 1;
      if (this.failedAllocNumbers.has(allocCalls)) {
        this.allocs.push({ pointer: 0, size, how, failed: true });
        return 0;
      }
      const pointer = bump;
      bump += Math.max(1, size);
      if (bump > NATIVE_MEMORY_SIZE) throw new RangeError('fake allocator exhausted');
      this.allocs.push({ pointer, size, how });
      if (how === 'calloc') buffer.fill(0, pointer, pointer + size);
      return pointer;
    },
    free(pointer) {
      if (!pointer) throw new Error('free(0)');
      this.frees.push(pointer);
    }
  };
  return memory;
}

/* Shared require stub for the real libmedia modules under test (67669 lease
 * registry and 26669 codecpar writer). Getter/setter keys match the bundle's
 * module 56238 accessors and module 6927 mutators. */
function libmediaRequire(memory) {
  const getters = {
    8: (pointer) => memory.getI8(pointer),
    10: (pointer) => memory.getF64(pointer),
    15: (pointer) => memory.getI32(pointer),
    17: (pointer) => memory.getBigInt64(pointer),
    20: (pointer) => memory.getU32(pointer),
    25: (pointer) => memory.getU32(pointer)
  };
  const setters = {
    8: (pointer, value) => memory.setI8(pointer, value),
    10: (pointer, value) => memory.setF64(pointer, value),
    15: (pointer, value) => memory.setI32(pointer, value),
    17: (pointer, value) => memory.setBigInt64(pointer, value),
    20: (pointer, value) => memory.setU32(pointer, value),
    25: (pointer, value) => memory.setU32(pointer, value)
  };
  const requireStub = (id) => {
    if (id === 80209) {
      return {
        Mr: (dest, source, size) => memory.copy(dest, source, size),
        sO: (pointer, value, size) => memory.memset(pointer, value, size),
        lW: (dest, size, source) => {
          if (source && typeof source === 'object' && ArrayBuffer.isView(source)) {
            memory.write(dest, size);
            memory.buffer.set(new Uint8Array(source.buffer, source.byteOffset, size), dest);
            return;
          }
          memory.copy(dest, source, size);
        }
      };
    }
    if (id === 56238) return { f: getters };
    if (id === 6927) return { M: setters };
    if (id === 46626) {
      return {
        Eb: (pointer) => memory.free(pointer),
        sY: (size) => memory.allocate(size, 'malloc'),
        Gy: (size) => memory.allocate(size, 'calloc'),
        aI: (pointer) => {
          const value = memory.getU32(pointer);
          memory.free(value);
          memory.setU32(pointer, 0);
        }
      };
    }
    if (id === 57216) return { bl: () => {} };
    if (id === 46564) {
      return {
        NZ: (pointer, countPointer) => {
          const table = memory.getU32(pointer);
          const count = memory.getI32(countPointer);
          if (table) {
            for (let index = 0; index < count; index += 1) {
              memory.free(memory.getU32(table + 12 * index));
            }
            memory.free(table);
          }
          memory.setU32(pointer, 0);
          memory.setI32(countPointer, 0);
        },
        /* Fake of the audited "append one 12-byte entry, grow the table"
           writer: one allocation per appended entry. */
        $W: (pointer, countPointer, type, payload, size) => {
          const count = memory.getI32(countPointer);
          const oldTable = memory.getU32(pointer);
          const table = memory.allocate(12 * (count + 1), 'malloc');
          if (oldTable) {
            memory.copy(table, oldTable, 12 * count);
            memory.free(oldTable);
          }
          memory.setU32(table + 12 * count, payload);
          memory.setU32(table + 12 * count + 4, size);
          memory.setI32(table + 12 * count + 8, type);
          memory.setU32(pointer, table);
          memory.setI32(countPointer, count + 1);
        }
      };
    }
    if (id === 29688) return { N_: 0 };
    throw new Error('unexpected module id ' + id);
  };
  requireStub.d = (target, definitions) => {
    for (const key of Object.keys(definitions)) target[key] = definitions[key]();
  };
  return requireStub;
}

function loadModule67669(memory) {
  const factory = vm.runInNewContext('(' + module67669Text(avplayerSource) + ')', {});
  const exports = {};
  factory({}, exports, libmediaRequire(memory));
  return exports;
}

/* Extract one webpack module factory body by its module id. The end anchor is
 * the next `},<id>:(e,t,i)=>{` module boundary in the same bundle. */
function moduleTextOf(text, id) {
  const start = text.indexOf(id + ':(e,t,i)=>{');
  assert.ok(start >= 0, 'module ' + id + ' not found');
  const rest = text.slice(start + String(id).length + 1);
  const next = /\},\d+:\(e,t,i\)=>/.exec(rest);
  assert.ok(next, 'module ' + id + ' end not found');
  return rest.slice(0, next.index + 1);
}

function loadModule26669(memory) {
  const factory = vm.runInNewContext('(' + moduleTextOf(avplayerSource, 26669) + ')', {});
  const exports = {};
  factory({}, exports, libmediaRequire(memory));
  return exports;
}

function bufferPoke(memory, pointer, bytes) {
  memory.buffer.set(bytes, pointer);
}

function setupNativeSource(memory) {
  const extra = [1, 2, 3];
  const sides = [{ bytes: [4, 5], type: 2 }];
  memory.setI32(NATIVE.src, 7);
  memory.setI32(NATIVE.src + 4, 8);
  memory.setI8(NATIVE.src + 8, 9);
  memory.setI32(NATIVE.src + 28, 11);
  memory.setBigInt64(NATIVE.src + 32, 12n);
  memory.setI32(NATIVE.src + 40, 13);
  memory.setI32(NATIVE.src + 136, 14);
  memory.setU32(NATIVE.src + 12, NATIVE.extra);
  memory.setI32(NATIVE.src + 16, extra.length);
  bufferPoke(memory, NATIVE.extra, extra);
  memory.setU32(NATIVE.src + 20, NATIVE.sideTable);
  memory.setI32(NATIVE.src + 24, sides.length);
  sides.forEach((side, index) => {
    const entry = NATIVE.sideTable + 12 * index;
    const payload = NATIVE.sidePayload + index * 0x100;
    memory.setU32(entry, payload);
    memory.setU32(entry + 4, side.bytes.length);
    memory.setI32(entry + 8, side.type);
    bufferPoke(memory, payload, side.bytes);
  });
  return { bytes: extra.length + sides[0].bytes.length };
}

function nativeCloneHarness(memory) {
  const api67669 = loadModule67669(memory);
  const fixture = setupNativeSource(memory);
  const real = makeRealResources({
    capacityBytes: 4096,
    freeCount: () => memory.frees.length
  });
  const task = { eioOwnedResources: real.resources, eioOwnedGeneration: 3 };
  let requireCalls = 0;
  const clone = loadCloneMethod({
    l: {
      Gy(size) {
        assert.equal(size, 168);
        return NATIVE.dest;
      }
    },
    y: { A: (value) => value === NATIVE.src },
    g: api67669,
    p: {
      T8() {
        throw new Error('native pointer source must not serialize');
      }
    },
    i(id) {
      requireCalls += 1;
      assert.equal(id, 90191);
      return codecpar;
    }
  });
  return {
    api67669,
    fixture,
    real,
    task,
    clone,
    requireCalls: () => requireCalls
  };
}

test('actual method with REAL module 67669 credits only after the real dn free (method scope only; not Worker proof)',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    const harness = nativeCloneHarness(memory);
    assert.ok(harness.fixture.bytes > 0);
    assert.equal(harness.real.control.snapshot().heldBytes, 0);

    const dest = harness.clone.eioCloneStreamCodecpar(harness.task, NATIVE.src);
    assert.equal(dest, NATIVE.dest);
    assert.equal(harness.requireCalls(), 1);
    assert.deepEqual([
      harness.real.control.snapshot().heldBytes,
      harness.real.control.snapshot().countBound
    ], [harness.fixture.bytes, 1]);
    assert.equal(harness.task.eioCodecparLeases.snapshot().boundCount, 1);
    assert.ok(memory.allocs.length > 0);
    assert.ok(memory.getU32(dest + 12) > 0);
    assert.equal(memory.getI32(dest + 16), 3);

    harness.api67669.dn(dest);
    assert.deepEqual([
      harness.real.control.snapshot().heldBytes,
      harness.real.control.snapshot().countBound
    ], [0, 0], 'the REAL dn free credits the admitted payload');
    assert.deepEqual([
      harness.task.eioCodecparLeases.snapshot().releasedCount,
      harness.task.eioCodecparLeases.snapshot().liveBytes
    ], [1, 0]);
    assert.equal(harness.real.releaseOrder.length, 1);
    assert.ok(harness.real.releaseOrder[0] > 0,
      'the core release is requested only after the native payload free');
    assert.equal(memory.frees.includes(dest), true);
  });

test('REAL module 67669 allocation failure is retained by the actual method (method scope only)',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    memory.failedAllocNumbers.add(1);
    const harness = nativeCloneHarness(memory);
    assert.ok(harness.fixture.bytes > 0);

    assert.throws(() => harness.clone.eioCloneStreamCodecpar(harness.task, NATIVE.src),
      (error) => error.code === 'EIO_CODECPAR_ALLOC_FAILED');
    assert.equal(memory.allocs[0].failed, true);
    assert.equal(memory.frees.length, 0);
    assert.equal(harness.real.control.snapshot().heldBytes, harness.fixture.bytes,
      'the admitted real charge survives the failed native copy');
    const snapshot = harness.task.eioCodecparLeases.snapshot();
    assert.deepEqual([snapshot.retainedCount, snapshot.releasedCount, snapshot.liveBytes],
      [1, 0, harness.fixture.bytes]);
    assert.equal(snapshot.faults.some((fault) => fault.code === 'EIO_CODECPAR_CLONE_UNPROVEN'),
      true);

    assert.throws(() => harness.api67669.dn(NATIVE.dest),
      (error) => error.code === 'EIO_CODECPAR_CLONE_UNPROVEN');
    assert.equal(memory.frees.length, 0, 'a retained clone is neither freed nor credited');
    assert.equal(harness.real.control.snapshot().heldBytes, harness.fixture.bytes);
  });

test('actual addStream/reAddStream wiring and incomplete wording stay narrow (method scope only; not Worker proof)',
  { timeout: 5000 }, () => {
    assert.equal(mseSource.includes(
      'const k=(0,l.Gy)(168);(0,y.A)(i)?(0,g.Yi)(k,i):(0,p.T8)(i,k);'), false,
    'addStream no longer clones directly');
    assert.equal(mseSource.includes(
      'const d=(0,l.Gy)(168);(0,y.A)(i)?(0,g.Yi)(d,i):(0,p.T8)(i,d);'), false,
    'reAddStream no longer clones directly');
    assert.equal(mseSource.split('const k=this.eioCloneStreamCodecpar(f,i);').length, 2);
    assert.equal(mseSource.split('const d=this.eioCloneStreamCodecpar(c,i);').length, 2);
    assert.equal(mseSource.split('(0,g.Yi)(I.codecpar[r.o9],k)').length, 2,
      'the mux-output second Yi remains unleased');
    assert.equal(mseSource.split('(0,g.Yi)(i.codecpar[r.o9],d)').length, 2,
      'the in-place output destination remains unleased');
    assert.doesNotMatch(CLONE_METHOD, /\.release\(/,
      'the method never manually releases; module 67669 credits after the real dn free');
    assert.match(CLONE_METHOD, /EIO_CODECPAR_ALLOC_FAILED/);
    assert.match(CLONE_METHOD, /retainFailure\(\{code:"EIO_CODECPAR_CLONE_UNPROVEN"\}\)/);
    assert.match(CLONE_METHOD, /task\.eioCodecparLeases=i\(90191\)/);
    assert.match(CLONE_METHOD, /"mse-stream-codecpar"/);
    assert.match(CLONE_METHOD, /acquireMeasured/,
      'the serialized branch admits through the caller-measured entry point');
    assert.match(CLONE_METHOD, /eioSerializedCodecparPayloadBytes/,
      'the serialized branch measures the frozen snapshot, not the live object');
    assert.match(CLONE_METHOD, /g\.eioCodecparAdopt\(eioDest,eioLease,eioBytes\)/,
      'the serialized branch adopts the real dn()-credited lease after T8');
    assert.match(mseSource, /\(0,p\.T8\)\(eioSnapshot,eioDest,!0\)/,
      'strict serialized T8 keeps the additive exact-allocation guard');
    assert.match(mseSource, /else\(0,p\.T8\)\(source,eioDest\);/,
      'baseline serialized T8 stays the raw 2-argument call');
    assert.match(mseSource,
      /if\(eioFixed\.ok!==!0\)\{[\s\S]{0,260}?this\.eioDiscardUninstalledClone\(k\)/,
      'a fixed-window refusal frees the uninstalled input clone before throwing');
    assert.match(mseSource, /catch\(err\)\{this\.eioDiscardUninstalledClone\(k\);throw err\}/,
      'an eioAcquire throw also frees the uninstalled input clone');

    assert.match(helperSource, /streamLifetimePayload: true/);
    /* The helper-wide serialization flag must stay false: this contract covers
       only the native + serialized-INPUT codecpar destination path. The other
       initial serialized copies (Wu) and mux-output/in-place copies remain
       outside both the charge and the coverage claim. */
    assert.match(helperSource, /serialization: false/);
    assert.doesNotMatch(helperSource, /serialization: true/);
    assert.match(helperSource, /muxOutputCopy: false/);
    assert.match(helperSource, /replacement: false/);

    assert.match(mseSource, /codecparLeases:eioCodecparLeases/,
      'the read-only shared-media snapshot reports the codecpar lease diagnostics');
    assert.match(mseSource,
      /EIO_MSE_INCOMPLETE=Object\.freeze\(\[[^\]]*"stream-codecpar-clones"/);
    const reason = mseSource.match(/"stream-codecpar-clones":"([^"]+)"/);
    assert.ok(reason, 'existing stream-codecpar-clones reason remains');
    assert.match(reason[1], /NATIVE input codecpar payload clones/i);
    assert.match(reason[1], /serialized-input/i);
    assert.match(reason[1], /mux-output/i);
    assert.match(reason[1], /in-place/i);
    assert.match(reason[1], /uncovered/i);
  });

/* -------------------------------------------------------------------------
 * Strict serialized input: REAL module 26669 T8 + REAL module 67669
 * eioCodecparAdopt()/dn() over fake WASM memory.
 * ---------------------------------------------------------------------- */

const SERIALIZED_EXTRA = [1, 2, 3, 4, 5];
const SERIALIZED_SIDE = [6, 7];
const SERIALIZED_BYTES = SERIALIZED_EXTRA.length + SERIALIZED_SIDE.length;

function serializedSourceFixture() {
  return {
    codecType: 0,
    codecId: 27,
    codecTag: 0,
    format: 1,
    bitrate: 0n,
    bitsPerCodedSample: 8,
    bitsPerRawSample: 8,
    profile: 100,
    level: 40,
    width: 1920,
    height: 1080,
    sampleAspectRatio: { den: 1, num: 1 },
    framerate: { den: 25, num: 1 },
    fieldOrder: 1,
    colorRange: 1,
    colorPrimaries: 1,
    colorTrc: 1,
    colorSpace: 1,
    chromaLocation: 1,
    videoDelay: 0,
    chLayout: { order: 1, nbChannels: 2, u: 0 },
    sampleRate: 48000,
    blockAlign: 4,
    frameSize: 1024,
    initialPadding: 0,
    trailingPadding: 0,
    seekPreroll: 0,
    flags: 0,
    extradata: new Uint8Array([99, ...SERIALIZED_EXTRA]).subarray(1),
    codedSideData: [{ type: 2, data: new Uint8Array([99, ...SERIALIZED_SIDE]).subarray(1) }]
  };
}

test('strict serialized clone measures before Gy, writes through the real T8 and adopts into the real dn credit (method scope only; not Worker proof)',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    const api26669 = loadModule26669(memory);
    const api67669 = loadModule67669(memory);
    assert.equal(typeof api67669.eioCodecparAdopt, 'function',
      'the concurrent 67669 worker must export eioCodecparAdopt');
    const fixture = serializedSourceFixture();
    const expectedViews = [fixture.extradata, fixture.codedSideData[0].data].map((view) => ({
      buffer: view.buffer, byteOffset: view.byteOffset, byteLength: view.byteLength
    }));
    let hostileReads = 0;
    for (const key of ['length', 'byteLength', 'BYTES_PER_ELEMENT']) {
      Object.defineProperty(fixture.extradata, key, { configurable: true, get() {
        hostileReads += 1;
        throw new Error('hostile extradata ' + key);
      } });
    }
    Object.setPrototypeOf(fixture.codedSideData[0].data, Object.create(Uint8Array.prototype, {
      length: { get() { hostileReads += 1; throw new Error('subclass length'); } },
      byteLength: { get() { hostileReads += 1; throw new Error('subclass byteLength'); } },
      BYTES_PER_ELEMENT: { get() { hostileReads += 1; throw new Error('subclass BYTES_PER_ELEMENT'); } }
    }));
    const real = makeRealResources({
      capacityBytes: 4096,
      freeCount: () => memory.frees.length
    });
    const task = { eioOwnedResources: real.resources, eioOwnedGeneration: 5 };
    const calls = [];
    const exactFlags = [];
    let requireCalls = 0;
    let nativeScanCalls = 0;
    let adoptCalls = 0;
    let observedSnapshot = null;
    const api = loadCloneMethod({
      l: {
        Gy(size) {
          calls.push(['Gy', size, real.control.snapshot().heldBytes]);
          assert.equal(size, 168);
          assert.equal(real.control.snapshot().heldBytes, SERIALIZED_BYTES,
            'the exact measured payload is charged before Gy(168)');
          assert.equal(memory.allocs.length, 0,
            'snapshot/measure allocate no payload before Gy');
          return memory.allocate(size, 'calloc');
        }
      },
      y: { A: () => false },
      g: Object.assign({}, api67669, {
        eioPayloadBytes(pointer) {
          nativeScanCalls += 1;
          return api67669.eioPayloadBytes(pointer);
        },
        eioCodecparAdopt(dest, lease, bytes) {
          adoptCalls += 1;
          calls.push(['adopt', dest, lease.bytes, bytes]);
          assert.equal(real.control.snapshot().heldBytes, SERIALIZED_BYTES,
            'the adopted lease is still charged when the real registry binds it');
          return api67669.eioCodecparAdopt(dest, lease, bytes);
        }
      }),
      p: {
        T8(source, dest, exact) {
          observedSnapshot = source;
          exactFlags.push(exact);
          calls.push(['T8', dest, exact]);
          assert.notEqual(source, fixture, 'T8 receives the frozen snapshot, not the live source');
          assert.equal(Object.isFrozen(source), true, 'the snapshot is frozen plain metadata');
          for (const [alias, original] of [[source.extradata, expectedViews[0]],
            [source.codedSideData[0].data, expectedViews[1]]]) {
            assert.equal(Object.prototype.toString.call(alias), '[object Uint8Array]',
              'the snapshot holds a canonical fixed-length Uint8Array alias');
            assert.equal(alias.buffer, original.buffer,
              'the alias shares the backing buffer, never a copied payload');
            assert.equal(alias.byteOffset, original.byteOffset);
            assert.equal(alias.byteLength, original.byteLength);
          }
          assert.equal(hostileReads, 0,
            'no own/prototype length, byteLength or BYTES_PER_ELEMENT getter ran');
          assert.equal(Object.isFrozen(source.sampleAspectRatio), true);
          assert.equal(memory.allocs.length, 1, 'T8 is the first payload writer');
          return api26669.T8(source, dest, exact);
        }
      },
      i(id) {
        requireCalls += 1;
        assert.equal(id, 90191);
        return codecpar;
      }
    });

    const dest = api.eioCloneStreamCodecpar(task, fixture);
    assert.deepEqual(calls.map((call) => call[0]), ['Gy', 'T8', 'adopt'],
      'order: admission -> Gy -> T8 -> adopt');
    assert.equal(requireCalls, 1, 'one lazy 90191 manager per task');
    assert.equal(nativeScanCalls, 0, 'the serialized path never scans a native pointer');
    assert.equal(adoptCalls, 1);
    assert.deepEqual(exactFlags, [true],
      'strict serialized T8 asks for the additive exact-allocation guard');
    assert.equal(observedSnapshot.codecType, 0);
    assert.equal(observedSnapshot.bitrate, 0n);

    assert.deepEqual(memory.allocs.map((alloc) => [alloc.size, alloc.how]), [
      [168, 'calloc'],
      [SERIALIZED_EXTRA.length, 'malloc'],
      [SERIALIZED_SIDE.length, 'malloc'],
      [12, 'malloc']
    ], '1 struct + extradata + one side payload + 12*n table');
    const pointers = memory.allocs.map((alloc) => alloc.pointer);
    const struct = pointers[0];
    const extradata = pointers[1];
    const sidePayload = pointers[2];
    const sideTable = pointers[3];
    assert.equal(dest, struct);
    assert.equal(memory.getU32(struct + 12), extradata);
    assert.equal(memory.getI32(struct + 16), SERIALIZED_EXTRA.length);
    assert.equal(memory.getU32(struct + 20), sideTable);
    assert.equal(memory.getI32(struct + 24), 1);
    assert.equal(memory.getU32(sideTable), sidePayload);
    assert.equal(memory.getU32(sideTable + 4), SERIALIZED_SIDE.length);
    assert.equal(memory.getI32(sideTable + 8), 2);
    assert.deepEqual(
      Array.from(memory.buffer.slice(extradata, extradata + SERIALIZED_EXTRA.length)),
      SERIALIZED_EXTRA);
    assert.deepEqual(
      Array.from(memory.buffer.slice(sidePayload, sidePayload + SERIALIZED_SIDE.length)),
      SERIALIZED_SIDE);
    assert.equal(memory.getI32(struct + 4), 27);
    assert.equal(memory.getI32(struct + 56), 1920);
    assert.equal(memory.getI32(struct + 60), 1080);

    const leased = task.eioCodecparLeases.snapshot();
    assert.deepEqual([leased.acquiredCount, leased.measuredAcquireCount, leased.boundCount,
      leased.liveBytes, leased.strandedBytes], [1, 1, 1, SERIALIZED_BYTES, 0],
    'one caller-measured admission, never a native scan');
    assert.deepEqual([real.control.snapshot().heldBytes, real.control.snapshot().countBound],
      [SERIALIZED_BYTES, 1]);
    assert.equal(memory.frees.length, 0, 'adopt does not free anything');

    api67669.dn(dest);
    assert.deepEqual([real.control.snapshot().heldBytes, real.control.snapshot().countBound],
      [0, 0], 'the REAL dn credits exactly once');
    assert.equal(task.eioCodecparLeases.snapshot().releasedCount, 1);
    assert.deepEqual(memory.frees, [extradata, sidePayload, sideTable, struct],
      'free order: extradata, side payload, table, struct');
    assert.deepEqual(real.releaseOrder, [4],
      'the core credit is requested only after all native frees');
    const zeroViews = api.eioSerializedCodecparSnapshot(Object.assign(serializedSourceFixture(), {
      extradata: new Uint8Array(0),
      codedSideData: [{ type: 1, data: new Uint8Array(0) }]
    }));
    assert.equal(zeroViews.extradata.byteLength, 0, 'zero-length extradata is a valid alias');
    assert.equal(zeroViews.codedSideData[0].data.byteLength, 0, 'zero side payload is valid');
  });

test('strict serialized shape and size refusals precede any charge or allocation (method scope only)',
  { timeout: 5000 }, () => {
    const base = () => ({
      sampleAspectRatio: { den: 1, num: 1 },
      framerate: { den: 1, num: 1 },
      chLayout: { order: 1, nbChannels: 1, u: 0 },
      codedSideData: []
    });
    const withBase = (patch) => Object.assign(base(), patch);
    let coercions = 0;
    const coercing = () => ({
      valueOf() { coercions += 1; throw new Error('valueOf invoked'); },
      [Symbol.toPrimitive]() { coercions += 1; throw new Error('toPrimitive invoked'); }
    });
    const detachedBuffer = new ArrayBuffer(2);
    const detachedView = new Uint8Array(detachedBuffer);
    structuredClone(detachedBuffer, { transfer: [detachedBuffer] });
    const throwingRoot = withBase({});
    Object.defineProperty(throwingRoot, 'profile', {
      get() { throw new Error('root getter exploded'); }
    });
    const throwingNested = withBase({
      sampleAspectRatio: { get den() { throw new Error('nested getter exploded'); }, num: 1 }
    });
    const cases = [
      ['non-object source', null, 'EIO_CODECPAR_LEASE_SHAPE_UNSUPPORTED'],
      ['array source', [], 'EIO_CODECPAR_LEASE_SHAPE_UNSUPPORTED'],
      ['missing codedSideData', withBase({ codedSideData: undefined }),
        'EIO_CODECPAR_LEASE_SHAPE_UNSUPPORTED'],
      ['codedSideData not an array', withBase({ codedSideData: {} }),
        'EIO_CODECPAR_LEASE_SHAPE_UNSUPPORTED'],
      ['side-data count over 65536', withBase({ codedSideData: new Array(65537) }),
        'EIO_CODECPAR_LEASE_SHAPE_UNSUPPORTED'],
      ['side entry not an object', withBase({ codedSideData: [null] }),
        'EIO_CODECPAR_LEASE_SHAPE_UNSUPPORTED'],
      ['missing sampleAspectRatio', withBase({ sampleAspectRatio: undefined }),
        'EIO_CODECPAR_LEASE_SHAPE_UNSUPPORTED'],
      ['missing framerate', withBase({ framerate: undefined }),
        'EIO_CODECPAR_LEASE_SHAPE_UNSUPPORTED'],
      ['missing chLayout', withBase({ chLayout: undefined }),
        'EIO_CODECPAR_LEASE_SHAPE_UNSUPPORTED'],
      ['throwing root getter', throwingRoot, 'EIO_CODECPAR_LEASE_SHAPE_UNSUPPORTED'],
      ['throwing nested getter', throwingNested, 'EIO_CODECPAR_LEASE_SHAPE_UNSUPPORTED'],
      ['object-valued profile scalar', withBase({ profile: coercing() }),
        'EIO_CODECPAR_LEASE_SHAPE_UNSUPPORTED'],
      ['object-valued nested chLayout scalar',
        withBase({ chLayout: { order: 1, nbChannels: coercing(), u: 0 } }),
        'EIO_CODECPAR_LEASE_SHAPE_UNSUPPORTED'],
      ['object-valued side type scalar',
        withBase({ codedSideData: [{ type: coercing(), data: new Uint8Array(0) }] }),
        'EIO_CODECPAR_LEASE_SHAPE_UNSUPPORTED'],
      ['extradata not a view', withBase({ extradata: [1, 2] }),
        'EIO_CODECPAR_LEASE_SIZE_UNSUPPORTED'],
      ['extradata multi-byte view', withBase({ extradata: new Uint16Array(2) }),
        'EIO_CODECPAR_LEASE_SIZE_UNSUPPORTED'],
      ['extradata DataView', withBase({ extradata: new DataView(new ArrayBuffer(2)) }),
        'EIO_CODECPAR_LEASE_SIZE_UNSUPPORTED'],
      ['extradata detached view', withBase({ extradata: detachedView }),
        'EIO_CODECPAR_LEASE_SIZE_UNSUPPORTED'],
      ['side data not a view', withBase({ codedSideData: [{ type: 1, data: 'payload' }] }),
        'EIO_CODECPAR_LEASE_SIZE_UNSUPPORTED'],
      ['fake 1-byte view object with unsafe length',
        withBase({ extradata: { BYTES_PER_ELEMENT: 1, length: -1 } }),
        'EIO_CODECPAR_LEASE_SIZE_UNSUPPORTED'],
      ['fake 1-byte view object with NaN length',
        withBase({ extradata: { BYTES_PER_ELEMENT: 1, length: NaN } }),
        'EIO_CODECPAR_LEASE_SIZE_UNSUPPORTED']
    ];

    for (const [name, source, code] of cases) {
      const real = makeRealResources({ capacityBytes: 4096 });
      const task = { eioOwnedResources: real.resources, eioOwnedGeneration: 1 };
      let gy = 0;
      let t8 = 0;
      let adopts = 0;
      let requireCalls = 0;
      const api = loadCloneMethod({
        l: { Gy() { gy += 1; return 0x4000; } },
        y: { A: () => false },
        g: {
          eioPayloadBytes() { throw new Error('no native scan on the serialized path'); },
          Yi() { throw new Error('no native pointer clone'); },
          eioCodecparAdopt() { adopts += 1; }
        },
        p: { T8() { t8 += 1; } },
        i() { requireCalls += 1; return codecpar; }
      });
      assert.throws(() => api.eioCloneStreamCodecpar(task, source),
        (error) => error.code === code, name);
      assert.equal(gy, 0, name + ': no Gy');
      assert.equal(t8, 0, name + ': no T8');
      assert.equal(adopts, 0, name + ': no adopt');
      assert.equal(requireCalls, 0, name + ': refused before the 90191 require');
      assert.equal(task.eioCodecparLeases, undefined, name + ': no manager was created');
      assert.deepEqual([real.control.snapshot().heldBytes, real.control.snapshot().countBound],
        [0, 0], name + ': no charge');
    }
    assert.equal(coercions, 0, 'no valueOf/toPrimitive hook ran before admission');
  });

test('strict serialized snapshot reads each live T8 field once and never re-reads the source (method scope only)',
  { timeout: 5000 }, () => {
    const fixture = serializedSourceFixture();
    const counts = new Map();
    const qualify = (prefix, key) => prefix ? prefix + '.' + String(key) : String(key);
    const wrap = (target, prefix) => new Proxy(target, {
      get(inner, key) {
        const name = qualify(prefix, key);
        counts.set(name, (counts.get(name) || 0) + 1);
        const value = inner[key];
        return value && typeof value === 'object' && !Array.isArray(value) &&
          !ArrayBuffer.isView(value) ? wrap(value, name) : value;
      }
    });
    const source = wrap(fixture, '');
    const real = makeRealResources({ capacityBytes: 4096 });
    const task = { eioOwnedResources: real.resources, eioOwnedGeneration: 1 };
    let snapshot = null;
    const api = loadCloneMethod({
      l: { Gy: () => 0x5000 },
      y: { A: () => false },
      g: {
        eioPayloadBytes() { throw new Error('no native scan'); },
        eioCodecparAdopt() {}
      },
      p: {
        T8(candidate, dest, exact) {
          snapshot = candidate;
          assert.equal(exact, true);
          assert.equal(dest, 0x5000);
          return dest;
        }
      },
      i(id) { assert.equal(id, 90191); return codecpar; }
    });

    assert.equal(api.eioCloneStreamCodecpar(task, source), 0x5000);
    assert.notEqual(snapshot, source);
    assert.equal(Object.isFrozen(snapshot), true);
    const expectedCounts = new Map();
    for (const key of [
      'codecType', 'codecId', 'codecTag', 'format', 'bitrate', 'bitsPerCodedSample',
      'bitsPerRawSample', 'profile', 'level', 'width', 'height', 'sampleAspectRatio',
      'framerate', 'fieldOrder', 'colorRange', 'colorPrimaries', 'colorTrc', 'colorSpace',
      'chromaLocation', 'videoDelay', 'chLayout', 'sampleRate', 'blockAlign', 'frameSize',
      'initialPadding', 'trailingPadding', 'seekPreroll', 'flags', 'extradata',
      'codedSideData', 'sampleAspectRatio.den', 'sampleAspectRatio.num',
      'framerate.den', 'framerate.num', 'chLayout.order', 'chLayout.nbChannels',
      'chLayout.u'
    ]) expectedCounts.set(key, 1);
    for (const key of counts.keys())
      assert.ok(expectedCounts.has(key), 'unexpected live-source read: ' + String(key));
    for (const [key, count] of expectedCounts)
      assert.equal(counts.get(key), count, 'live-source read count for ' + key);
  });

test('strict serialized admission refusal allocates and writes nothing and fabricates no credit (method scope only)',
  { timeout: 5000 }, () => {
    const real = makeRealResources({ capacityBytes: 1 });
    const task = { eioOwnedResources: real.resources, eioOwnedGeneration: 9 };
    const fixture = serializedSourceFixture();
    let gy = 0;
    let t8 = 0;
    let adopts = 0;
    const api = loadCloneMethod({
      l: { Gy() { gy += 1; return 0x6000; } },
      y: { A: () => false },
      g: {
        eioPayloadBytes() { throw new Error('no native scan'); },
        eioCodecparAdopt() { adopts += 1; }
      },
      p: { T8() { t8 += 1; } },
      i() { return codecpar; }
    });

    assert.throws(() => api.eioCloneStreamCodecpar(task, fixture),
      (error) => error.code === 'capacity-exceeded',
      'the refusal keeps the real core capacity code, still before Gy/T8/adopt');
    assert.equal(gy, 0, 'no Gy after an admission refusal');
    assert.equal(t8, 0, 'no T8 after an admission refusal');
    assert.equal(adopts, 0);
    assert.deepEqual([real.control.snapshot().heldBytes, real.control.snapshot().countBound], [0, 0]);
    const snapshot = task.eioCodecparLeases.snapshot();
    assert.deepEqual([snapshot.acquiredCount, snapshot.boundCount, snapshot.liveBytes,
      snapshot.strandedBytes], [0, 0, 0, 0]);
    assert.equal(snapshot.refusedCount, 1);
    assert.ok(snapshot.faultCount >= 1, 'the refusal stays diagnosable');
  });

test('strict serialized clone refuses a missing adopt API before Gy and before any charge (method scope only)',
  { timeout: 5000 }, () => {
    const real = makeRealResources({ capacityBytes: 4096 });
    const task = { eioOwnedResources: real.resources, eioOwnedGeneration: 6 };
    let gy = 0;
    let t8 = 0;
    const api = loadCloneMethod({
      l: { Gy() { gy += 1; return 0x6100; } },
      y: { A: () => false },
      g: {
        eioPayloadBytes() { throw new Error('no native scan'); },
        Yi() { throw new Error('no native pointer clone'); }
      },
      p: { T8() { t8 += 1; } },
      i() { return codecpar; }
    });

    assert.throws(() => api.eioCloneStreamCodecpar(task, serializedSourceFixture()),
      (error) => error.code === 'EIO_CODECPAR_LEASE_MEASURED_UNSUPPORTED');
    assert.equal(gy, 0, 'an unsupported measured/adopt API refuses before Gy');
    assert.equal(t8, 0);
    assert.deepEqual([real.control.snapshot().heldBytes, real.control.snapshot().countBound], [0, 0]);
    assert.equal(task.eioCodecparLeases.snapshot().acquiredCount, 0);
  });

test('strict serialized zero-byte payload still admits one lease and credits it on the real dn (method scope only)',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    const api26669 = loadModule26669(memory);
    const api67669 = loadModule67669(memory);
    assert.equal(typeof api67669.eioCodecparAdopt, 'function');
    const real = makeRealResources({ capacityBytes: 4096, freeCount: () => memory.frees.length });
    const task = { eioOwnedResources: real.resources, eioOwnedGeneration: 7 };
    const fixture = serializedSourceFixture();
    fixture.extradata = null;
    fixture.codedSideData = [];
    const api = loadCloneMethod({
      l: { Gy(size) { return memory.allocate(size, 'calloc'); } },
      y: { A: () => false },
      g: Object.assign({}, api67669, {
        eioPayloadBytes() { throw new Error('no native scan'); }
      }),
      p: { T8: (source, dest, exact) => api26669.T8(source, dest, exact) },
      i() { return codecpar; }
    });

    const dest = api.eioCloneStreamCodecpar(task, fixture);
    assert.deepEqual(memory.allocs.map((alloc) => alloc.size), [168],
      'a zero-byte payload allocates only the 168-byte struct');
    assert.deepEqual([memory.getU32(dest + 12), memory.getU32(dest + 20)], [0, 0]);
    const leased = task.eioCodecparLeases.snapshot();
    assert.deepEqual([leased.acquiredCount, leased.boundCount, leased.liveBytes], [1, 1, 0]);
    assert.equal(real.control.snapshot().heldBytes, 0);

    api67669.dn(dest);
    assert.equal(task.eioCodecparLeases.snapshot().releasedCount, 1,
      'a zero-byte lease is a measured lease and is credited by the real free');
    assert.deepEqual(memory.frees, [dest]);
    assert.deepEqual(real.releaseOrder, [1]);
  });

test('strict serialized T8 failure retains the charge and never frees or credits the destination (method scope only)',
  { timeout: 5000 }, () => {
    const real = makeRealResources({ capacityBytes: 4096 });
    const task = { eioOwnedResources: real.resources, eioOwnedGeneration: 2 };
    const fixture = serializedSourceFixture();
    const nativeFailure = new Error('native T8 write failed');
    let dnCalls = 0;
    let adopts = 0;
    const api = loadCloneMethod({
      l: { Gy(size) { assert.equal(size, 168); return 0x7000; } },
      y: { A: () => false },
      g: {
        eioPayloadBytes() { throw new Error('no native scan'); },
        dn() { dnCalls += 1; return true; },
        eioCodecparAdopt() { adopts += 1; }
      },
      p: { T8() { throw nativeFailure; } },
      i() { return codecpar; }
    });

    assert.throws(() => api.eioCloneStreamCodecpar(task, fixture),
      (error) => error === nativeFailure);
    assert.equal(dnCalls, 0, 'an unproved partial destination is never manually freed');
    assert.equal(adopts, 0, 'no adopt after a failed write');
    assert.equal(real.control.snapshot().heldBytes, SERIALIZED_BYTES, 'the charge is retained');
    const snapshot = task.eioCodecparLeases.snapshot();
    assert.deepEqual([snapshot.retainedCount, snapshot.releasedCount, snapshot.boundCount,
      snapshot.liveBytes], [1, 0, 0, SERIALIZED_BYTES]);
    assert.deepEqual(snapshot.faults.map((fault) => fault.code),
      ['EIO_CODECPAR_CLONE_UNPROVEN']);
  });

test('strict serialized adopt size mismatch and bind failure retain without credit (method scope only)',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    const api26669 = loadModule26669(memory);
    const api67669 = loadModule67669(memory);
    assert.equal(typeof api67669.eioCodecparAdopt, 'function');

    const mismatchReal = makeRealResources({ capacityBytes: 4096 });
    const mismatchTask = { eioOwnedResources: mismatchReal.resources, eioOwnedGeneration: 3 };
    const mismatchApi = loadCloneMethod({
      l: { Gy(size) { return memory.allocate(size, 'calloc'); } },
      y: { A: () => false },
      g: Object.assign({}, api67669, {
        eioPayloadBytes() { throw new Error('no native scan'); },
        eioCodecparAdopt(dest, lease) {
          return api67669.eioCodecparAdopt(dest, lease, SERIALIZED_BYTES + 1);
        }
      }),
      p: { T8: (source, dest, exact) => api26669.T8(source, dest, exact) },
      i() { return codecpar; }
    });

    assert.throws(() => mismatchApi.eioCloneStreamCodecpar(mismatchTask, serializedSourceFixture()),
      (error) => error.code === 'EIO_CODECPAR_LEASE_SIZE_MISMATCH');
    assert.equal(memory.frees.length, 0,
      'the caller never frees a destination whose adopt attestation failed');
    assert.equal(mismatchReal.control.snapshot().heldBytes, SERIALIZED_BYTES,
      'the mismatch keeps the charge');
    const mismatchLeases = mismatchTask.eioCodecparLeases.snapshot();
    assert.deepEqual([mismatchLeases.retainedCount, mismatchLeases.boundCount,
      mismatchLeases.releasedCount], [1, 0, 0]);

    const bindReal = makeRealResources({ capacityBytes: 4096 });
    const bindTask = { eioOwnedResources: bindReal.resources, eioOwnedGeneration: 4 };
    let dnCalls = 0;
    const bindApi = loadCloneMethod({
      l: { Gy: () => 0x7100 },
      y: { A: () => false },
      g: {
        eioPayloadBytes() { throw new Error('no native scan'); },
        dn() { dnCalls += 1; return true; },
        eioCodecparAdopt(dest, lease) {
          assert.equal(lease.bind({ copiedBytes: 0 }), false,
            'a wrong copied-bytes attestation must be refused by the real lease');
          const failure = new Error('EIO_CODECPAR_LEASE_BIND_FAILED');
          failure.code = 'EIO_CODECPAR_LEASE_BIND_FAILED';
          throw failure;
        }
      },
      p: { T8() {} },
      i() { return codecpar; }
    });

    assert.throws(() => bindApi.eioCloneStreamCodecpar(bindTask, serializedSourceFixture()),
      (error) => error.code === 'EIO_CODECPAR_LEASE_BIND_FAILED');
    assert.equal(dnCalls, 0, 'a poisoned destination is not freed by the caller');
    assert.equal(bindReal.control.snapshot().heldBytes, SERIALIZED_BYTES);
    const bindLeases = bindTask.eioCodecparLeases.snapshot();
    assert.deepEqual([bindLeases.retainedCount, bindLeases.boundCount,
      bindLeases.releasedCount, bindLeases.liveBytes], [1, 0, 0, SERIALIZED_BYTES]);
  });

test('addStream uninstalled-clone cleanup credits once and swallows a retained dn refusal (method scope only)',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    const harness = nativeCloneHarness(memory);
    const dest = harness.clone.eioCloneStreamCodecpar(harness.task, NATIVE.src);
    assert.equal(harness.real.control.snapshot().heldBytes, harness.fixture.bytes);

    harness.clone.eioDiscardUninstalledClone(dest);
    assert.deepEqual([harness.real.control.snapshot().heldBytes,
      harness.real.control.snapshot().countBound], [0, 0],
    'only the real dn() free credits the uninstalled clone');
    assert.equal(harness.task.eioCodecparLeases.snapshot().releasedCount, 1);
    assert.equal(memory.frees.includes(dest), true);

    let dnCalls = 0;
    const retained = new Error('EIO_CODECPAR_CLONE_UNPROVEN');
    retained.code = 'EIO_CODECPAR_CLONE_UNPROVEN';
    const api = loadCloneMethod({
      l: { Gy: () => 0x4000 },
      y: { A: () => false },
      g: {
        eioPayloadBytes() {},
        dn() { dnCalls += 1; throw retained; }
      },
      p: { T8() {} },
      i() { return codecpar; }
    });
    assert.doesNotThrow(() => api.eioDiscardUninstalledClone(0x4000),
      'a retained dn refusal is swallowed instead of inventing a credit');
    assert.equal(dnCalls, 1);
    assert.doesNotThrow(() => api.eioDiscardUninstalledClone(0));
    assert.equal(dnCalls, 1, 'a null clone is never passed to dn');
  });

test('baseline and strict serialized clones coexist: raw 2-arg T8 vs frozen 3-arg T8 (method scope only)',
  { timeout: 5000 }, () => {
    const fixture = serializedSourceFixture();
    const baselineCalls = [];
    let baselineRequires = 0;
    const baseline = loadCloneMethod({
      l: { Gy(size) { baselineCalls.push(['Gy', size]); return 0x8000; } },
      y: { A: () => false },
      g: {
        eioPayloadBytes() { throw new Error('baseline must not measure'); },
        Yi() { throw new Error('a serialized baseline source must not use Yi'); },
        eioCodecparAdopt() { throw new Error('baseline must not adopt'); }
      },
      p: {
        T8(source, dest) {
          baselineCalls.push(['T8', source, dest, arguments.length,
            Object.isFrozen(source)]);
          return true;
        }
      },
      i() { baselineRequires += 1; throw new Error('baseline must not require 90191'); }
    });

    assert.equal(baseline.eioCloneStreamCodecpar({}, fixture), 0x8000);
    assert.deepEqual(baselineCalls, [
      ['Gy', 168],
      ['T8', fixture, 0x8000, 2, false]
    ], 'baseline keeps the raw source and the 2-argument T8 call');
    assert.equal(baselineRequires, 0);

    const real = makeRealResources({ capacityBytes: 4096 });
    const task = { eioOwnedResources: real.resources, eioOwnedGeneration: 4 };
    let strictSnapshot = null;
    let adopted = 0;
    const strict = loadCloneMethod({
      l: { Gy: () => 0x9000 },
      y: { A: () => false },
      g: {
        eioPayloadBytes() { throw new Error('no native scan'); },
        eioCodecparAdopt() { adopted += 1; }
      },
      p: {
        T8(source, dest, exact) {
          strictSnapshot = source;
          assert.equal(exact, true);
          assert.equal(arguments.length, 3);
          return true;
        }
      },
      i() { return codecpar; }
    });

    assert.equal(strict.eioCloneStreamCodecpar(task, fixture), 0x9000);
    assert.notEqual(strictSnapshot, fixture, 'the strict path never hands the live object to T8');
    assert.equal(Object.isFrozen(strictSnapshot), true);
    assert.equal(adopted, 1);
    assert.equal(real.control.snapshot().heldBytes, SERIALIZED_BYTES);
    assert.equal(task.eioCodecparLeases.snapshot().acquiredCount, 1);
  });

test('strict serialized CUSTOM and unknown layout ownership refuse before admission or allocation',
  { timeout: 5000 }, () => {
    for (const order of [2, -1, 4, undefined, '1']) {
      const real = makeRealResources({ capacityBytes: 4096 });
      const task = { eioOwnedResources: real.resources, eioOwnedGeneration: 9 };
      const source = serializedSourceFixture();
      source.chLayout.order = order;
      let allocations = 0;
      let writes = 0;
      let managerLoads = 0;
      const api = loadCloneMethod({
        l: { Gy() { allocations += 1; return 0x9000; } },
        y: { A: () => false },
        g: { eioCodecparAdopt() { writes += 1; } },
        p: { T8() { writes += 1; } },
        i() { managerLoads += 1; return codecpar; }
      });
      assert.throws(() => api.eioCloneStreamCodecpar(task, source),
        error => error.code === 'EIO_CODECPAR_LAYOUT_UNSUPPORTED');
      assert.equal(allocations, 0);
      assert.equal(writes, 0);
      assert.equal(managerLoads, 0, 'ownership refusal precedes lease manager acquisition');
      assert.equal(real.control.snapshot().heldBytes, 0);
      assert.equal(real.control.snapshot().countBound, 0);
      assert.equal(task.eioCodecparLeases, undefined);
    }
  });
