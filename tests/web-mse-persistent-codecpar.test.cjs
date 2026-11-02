'use strict';

/*
 * Narrow N1 method-scope tests for eioCloneStreamCodecpar(task, source) in
 * public/vendor/web/libmedia/26.avplayer.js.
 *
 * Every clone assertion executes the REAL shipped method sliced out of module
 * 26. The admission/credit assertions use the REAL canonical
 * public/web/player-codecpar-leases.js helper, the REAL owned-resource wrapper
 * and the REAL shared-media ownership core. The final group also runs the REAL
 * vendored module 67669 Yi()/dn() over fake WASM memory and a fake allocator.
 *
 * This is method scope only, NOT Worker/native playback proof. There is no
 * Worker, MSE, BSF runtime, real upstream, production instance or native media.
 * The only non-real pieces are the fake allocator/memory, the read-only source
 * scanner in the ordering cases, and the destination pointer returned by the
 * fake Gy wrapper. No test calls the running application.
 */

const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const ROOT = path.join(__dirname, '..');
const MSE_PATH = path.join(ROOT, 'public', 'vendor', 'web', 'libmedia', '26.avplayer.js');
const AVPLAYER_PATH = path.join(ROOT, 'public', 'vendor', 'web', 'libmedia', 'avplayer.js');
const HELPER_PATH = path.join(ROOT, 'public', 'web', 'player-codecpar-leases.js');
const core = require('../public/web/player-shared-media-ownership.js');
const owned = require('../public/web/player-owned-media-resources.js');
const codecpar = require('../public/web/player-codecpar-leases.js');

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
  return vm.runInNewContext('({' + CLONE_METHOD + '})', sandbox);
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

test('strict serialized source stays unchanged and claims no coverage (method scope only; not Worker proof)',
  { timeout: 5000 }, () => {
    const real = makeRealResources({ capacityBytes: 64 });
    const task = { eioOwnedResources: real.resources, eioOwnedGeneration: 2 };
    const calls = [];
    const api = loadCloneMethod({
      l: {
        Gy(size) {
          calls.push(['Gy', size, real.control.snapshot().heldBytes]);
          assert.equal(real.control.snapshot().heldBytes, 0);
          assert.equal(task.eioCodecparLeases, undefined,
            'serialized input does not create the stream lease manager');
          return 0x3300;
        }
      },
      y: { A: () => false },
      g: {
        eioPayloadBytes() {
          throw new Error('serialized input must not be measured as a native pointer');
        },
        Yi() {
          throw new Error('serialized input must not use the native lease clone');
        }
      },
      p: {
        T8(source, dest) {
          calls.push(['T8', source, dest]);
          return true;
        }
      },
      i() {
        throw new Error('serialized path must not require module 90191');
      }
    });

    const source = 'serialized-input';
    assert.equal(api.eioCloneStreamCodecpar(task, source), 0x3300);
    assert.deepEqual(calls, [
      ['Gy', 168, 0],
      ['T8', source, 0x3300]
    ]);
    assert.deepEqual([real.control.snapshot().heldBytes, real.control.snapshot().countBound],
      [0, 0]);
    assert.equal(task.eioCodecparLeases, undefined);
  });

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
    setI8(pointer, value) { this.write(pointer, 1); view.setInt8(pointer, value); },
    setI32(pointer, value) { this.write(pointer, 4); view.setInt32(pointer, value, true); },
    setU32(pointer, value) { this.write(pointer, 4); view.setUint32(pointer, value, true); },
    setBigInt64(pointer, value) {
      this.write(pointer, 8);
      view.setBigInt64(pointer, value, true);
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

function loadModule67669(memory) {
  const getters = {
    8: (pointer) => memory.getI8(pointer),
    15: (pointer) => memory.getI32(pointer),
    17: (pointer) => memory.getBigInt64(pointer),
    20: (pointer) => memory.getU32(pointer),
    25: (pointer) => memory.getU32(pointer)
  };
  const setters = {
    8: (pointer, value) => memory.setI8(pointer, value),
    15: (pointer, value) => memory.setI32(pointer, value),
    17: (pointer, value) => memory.setBigInt64(pointer, value),
    20: (pointer, value) => memory.setU32(pointer, value),
    25: (pointer, value) => memory.setU32(pointer, value)
  };
  const requireStub = (id) => {
    if (id === 80209) {
      return {
        Mr: (dest, source, size) => memory.copy(dest, source, size),
        sO: (pointer, value, size) => memory.memset(pointer, value, size)
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
        }
      };
    }
    if (id === 29688) return { N_: 0 };
    throw new Error('unexpected module id ' + id);
  };
  requireStub.d = (target, definitions) => {
    for (const key of Object.keys(definitions)) target[key] = definitions[key]();
  };
  const factory = vm.runInNewContext('(' + module67669Text(avplayerSource) + ')', {});
  const exports = {};
  factory({}, exports, requireStub);
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

    assert.match(helperSource, /streamLifetimePayload: true/);
    assert.match(helperSource, /serialization: false/);
    assert.match(helperSource, /muxOutputCopy: false/);
    assert.match(helperSource, /replacement: false/);

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
