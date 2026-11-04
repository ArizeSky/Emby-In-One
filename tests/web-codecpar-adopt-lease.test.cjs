'use strict';

/*
 * P1b method-scope tests for the additive public/vendor/web/libmedia/avplayer.js
 * module 67669 eioCodecparAdopt(dest, lease, expectedBytes) export and the
 * module 26669 T8 optional exact-true payload allocation guard.
 *
 * The REAL module 67669 and module 26669 factories are sliced out of the
 * shipped bundle and run over fake WASM memory and a fake allocator. Where
 * feasible the lease is a
 * REAL web-src/player-codecpar-leases.js lease admitted by the REAL shared
 * ownership core through the REAL owned-resource wrapper. Fake leases are used
 * only to inject bind failure, bind null and pre-publication faults.
 *
 * This is method scope only, NOT Worker/native playback/MSE/BSF proof. There is
 * no Worker, browser, real upstream, production instance or native media here.
 * No test calls the running application.
 */

const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const ROOT = path.join(__dirname, '..');
const AVPLAYER_PATH = path.join(ROOT, 'public', 'vendor', 'web', 'libmedia', 'avplayer.js');
const core = require('../web-src/player-shared-media-ownership.js');
const owned = require('../web-src/player-owned-media-resources.js');
const codecpar = require('../web-src/player-codecpar-leases.js');

const avplayerSource = fs.readFileSync(AVPLAYER_PATH, 'utf8');

const NATIVE = Object.freeze({
  dest: 0x100,
  dest2: 0x200,
  src: 0x400,
  extra: 0x800,
  sideTable: 0x900,
  sidePayload: 0xa00
});
const NATIVE_MEMORY_SIZE = 0x20000;
const PROOF = Object.freeze({ referencesDropped: true });

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
    lWrites: [],
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

function setupSource(memory, options) {
  options = options || {};
  const extra = options.extra === undefined ? [1, 2, 3] : options.extra;
  const sides = options.sides === undefined ? [{ bytes: [4, 5], type: 2 }] : options.sides;
  memory.setI32(NATIVE.src, 7);
  memory.setI32(NATIVE.src + 4, 8);
  memory.setI8(NATIVE.src + 8, 9);
  memory.setU32(NATIVE.src + 12, extra.length ? NATIVE.extra : 0);
  memory.setI32(NATIVE.src + 16, extra.length);
  if (extra.length) memory.buffer.set(extra, NATIVE.extra);
  memory.setU32(NATIVE.src + 20, sides.length ? NATIVE.sideTable : 0);
  memory.setI32(NATIVE.src + 24, sides.length);
  sides.forEach((side, index) => {
    const entry = NATIVE.sideTable + 12 * index;
    const payload = NATIVE.sidePayload + index * 0x100;
    memory.setU32(entry, payload);
    memory.setU32(entry + 4, side.bytes.length);
    memory.setI32(entry + 8, side.type);
    if (side.bytes.length) memory.buffer.set(side.bytes, payload);
  });
  return {
    extra,
    sides,
    bytes: extra.length + sides.reduce((sum, side) => sum + side.bytes.length, 0)
  };
}

/* Build a mutating T8-like destination: dirty extradata + side table/payloads. */
function writeDirtyDestination(memory, dest, fixture) {
  const extraPointer = memory.allocate(fixture.extra.length, 'malloc');
  if (fixture.extra.length) memory.buffer.set(fixture.extra, extraPointer);
  memory.setU32(dest + 12, extraPointer);
  memory.setI32(dest + 16, fixture.extra.length);
  const payloads = [];
  let table = 0;
  if (fixture.sides.length) {
    table = memory.allocate(12 * fixture.sides.length, 'calloc');
    fixture.sides.forEach((side, index) => {
      const payload = memory.allocate(side.bytes.length, 'malloc');
      if (side.bytes.length) memory.buffer.set(side.bytes, payload);
      payloads.push(payload);
      const entry = table + 12 * index;
      memory.setU32(entry, payload);
      memory.setU32(entry + 4, side.bytes.length);
      memory.setI32(entry + 8, side.type);
    });
    memory.setU32(dest + 20, table);
    memory.setI32(dest + 24, fixture.sides.length);
  }
  return { extraPointer, table, payloads };
}

function makeRealManager(api, memory) {
  const control = core.create({ capacityBytes: 4096, maxEntries: 8 });
  const releaseOrder = [];
  const resources = owned.createOwnedResources({
    reserve: (bytes, generation) => control.reserve(bytes, generation),
    bindOwnedResource: (token) => control.bindOwnedResource(token),
    snapshot: () => control.snapshot(),
    releaseOwnedResource(handle, proof) {
      releaseOrder.push(memory.frees.length);
      return control.releaseOwnedResource(handle, proof);
    }
  });
  const manager = codecpar.createCodecparLeases({
    resources,
    measureNativePayload: (pointer) => api.eioPayloadBytes(pointer)
  });
  return { control, manager, releaseOrder };
}

function makeFakeLease(bytes, overrides) {
  overrides = overrides || {};
  const state = {
    bindCalls: 0,
    bindSummaries: [],
    retainFailures: [],
    releaseCalls: 0
  };
  const lease = {
    bytes,
    bind(summary) {
      state.bindCalls += 1;
      state.bindSummaries.push(summary);
      if (overrides.bind) return overrides.bind(summary, state, lease);
      return true;
    },
    release(proof) {
      state.releaseCalls += 1;
      if (overrides.release) return overrides.release(proof, state, lease);
      return true;
    },
    retainFailure(error) {
      state.retainFailures.push(error);
      if (overrides.retainFailure) return overrides.retainFailure(error, state, lease);
      return false;
    }
  };
  return { lease, state };
}

function expectCode(action, code) {
  assert.throws(action, (error) => error && error.code === code);
}

test('export is additive and adopt publishes the retained record before the untrusted bind',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    const api = loadModule67669(memory);
    assert.equal(typeof api.eioCodecparAdopt, 'function');
    assert.equal(typeof api.Yi, 'function');
    assert.equal(typeof api.dn, 'function');
    assert.equal(typeof api.eioPayloadBytes, 'function');

    const moduleText = module67669Text(avplayerSource);
    const start = moduleText.indexOf('function eioCodecparAdopt(');
    const end = moduleText.indexOf('\nfunction ', start + 1);
    assert.ok(start >= 0 && end > start, 'adopt is a separate function');
    const adoptText = moduleText.slice(start, end);
    assert.doesNotMatch(adoptText, /\.release\s*\(/,
      'adopt never releases or invents a credit');
    assert.match(adoptText, /state:\s*"copying"/);
    assert.match(adoptText, /copiedBytes:\s*eioScan\.bytes/);
    assert.match(adoptText, /eioCodecparLeases\.set\(eioDest,\s*eioRecord\)/, 'real destination record publication');
    assert.ok(adoptText.indexOf('.bind(') >= 0, 'real lease bind remains present');
    assert.ok(adoptText.search(/eioCodecparLeases\.set\(eioDest,\s*eioRecord\)/) <
      adoptText.indexOf('.bind('), 'the record is registered before bind');
    assert.ok(moduleText.includes('function c(eioDest,eioSource,eioLease){eioExtradataExclusive(eioDest);if(void 0===eioLease){'),
      'Yi baseline entry remains intact');
    assert.ok(moduleText.includes(
      'function l(eioPointer){if(!eioPointer||!eioCodecparLeases)return eioCodecparClearOriginal(eioPointer);'),
    'dn baseline entry remains intact');
  });

test('dirty destination adopt succeeds once and the real dn frees payloads/table/struct before credit',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    const api = loadModule67669(memory);
    const fixture = setupSource(memory);
    const dirty = writeDirtyDestination(memory, NATIVE.dest, fixture);
    const real = makeRealManager(api, memory);
    const lease = real.manager.acquire(NATIVE.src, 1, 'mse-stream-codecpar');
    assert.equal(lease.bytes, fixture.bytes);
    assert.ok(fixture.bytes > 0);
    assert.deepEqual([real.control.snapshot().heldBytes, real.control.snapshot().countBound],
      [fixture.bytes, 1]);

    api.eioCodecparAdopt(NATIVE.dest, lease, lease.bytes);
    assert.equal(memory.frees.length, 0, 'adopt itself frees nothing');
    assert.equal(real.manager.snapshot().boundCount, 1);
    assert.equal(real.manager.snapshot().releasedCount, 0);
    assert.equal(real.control.snapshot().heldBytes, fixture.bytes);

    api.dn(NATIVE.dest);
    assert.deepEqual(memory.frees,
      [dirty.extraPointer, dirty.payloads[0], dirty.table, NATIVE.dest],
      'real dn frees extradata, side payload, table, then the struct');
    assert.deepEqual(real.releaseOrder, [4],
      'the core release is requested only after all native frees');
    assert.deepEqual([real.control.snapshot().heldBytes, real.control.snapshot().countBound],
      [0, 0]);
    assert.deepEqual([real.manager.snapshot().releasedCount, real.manager.snapshot().liveBytes],
      [1, 0]);
  });

test('second adopt on a leased pointer is refused, retains the new lease and cannot overwrite',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    const api = loadModule67669(memory);
    const fixture = setupSource(memory);
    const dirty = writeDirtyDestination(memory, NATIVE.dest, fixture);
    const real = makeRealManager(api, memory);
    const first = real.manager.acquire(NATIVE.src, 1, 'mse-stream-codecpar');
    const second = real.manager.acquire(NATIVE.src, 2, 'mse-stream-codecpar');
    assert.equal(real.control.snapshot().heldBytes, fixture.bytes * 2);

    api.eioCodecparAdopt(NATIVE.dest, first, first.bytes);
    expectCode(() => api.eioCodecparAdopt(NATIVE.dest, second, second.bytes),
      'EIO_CODECPAR_LEASE_DEST_BUSY');
    assert.equal(memory.frees.length, 0, 'the busy refusal frees nothing');
    assert.deepEqual([
      real.manager.snapshot().boundCount,
      real.manager.snapshot().retainedCount,
      real.manager.snapshot().releasedCount,
      real.control.snapshot().heldBytes
    ], [1, 1, 0, fixture.bytes * 2]);
    assert.equal(real.manager.snapshot().faults[0].code,
      'EIO_CODECPAR_LEASE_DEST_BUSY');

    api.dn(NATIVE.dest);
    assert.deepEqual(memory.frees,
      [dirty.extraPointer, dirty.payloads[0], dirty.table, NATIVE.dest]);
    assert.deepEqual([real.manager.snapshot().releasedCount, real.manager.snapshot().liveBytes],
      [1, fixture.bytes], 'only the first record is credited');
    assert.equal(real.control.snapshot().heldBytes, fixture.bytes);
    assert.equal(second.release(PROOF), false,
      'the refused and retained lease is never credited later');
    assert.equal(real.control.snapshot().heldBytes, fixture.bytes);
  });

test('destination payload mismatch retains the lease and registers nothing',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    const api = loadModule67669(memory);
    const source = setupSource(memory);
    const dirtyFixture = {
      extra: [1, 2, 3, 4],
      sides: [{ bytes: [5, 6], type: 2 }]
    };
    const dirty = writeDirtyDestination(memory, NATIVE.dest, dirtyFixture);
    const real = makeRealManager(api, memory);
    const lease = real.manager.acquire(NATIVE.src, 1, 'mse-stream-codecpar');
    assert.equal(lease.bytes, source.bytes);
    assert.notEqual(lease.bytes, 6);

    expectCode(() => api.eioCodecparAdopt(NATIVE.dest, lease, lease.bytes),
      'EIO_CODECPAR_LEASE_SIZE_MISMATCH');
    assert.equal(memory.frees.length, 0, 'the mismatch refusal frees nothing');
    assert.deepEqual([
      real.manager.snapshot().retainedCount,
      real.manager.snapshot().boundCount,
      real.manager.snapshot().releasedCount,
      real.control.snapshot().heldBytes
    ], [1, 0, 0, source.bytes]);
    assert.equal(real.manager.snapshot().faults[0].code,
      'EIO_CODECPAR_LEASE_SIZE_MISMATCH');

    api.dn(NATIVE.dest);
    assert.deepEqual(memory.frees,
      [dirty.extraPointer, dirty.payloads[0], dirty.table, NATIVE.dest],
      'with no record dn only performs the ordinary destination free');
    assert.equal(real.manager.snapshot().releasedCount, 0);
    assert.equal(real.control.snapshot().heldBytes, source.bytes,
      'no record means no fake credit');
    assert.equal(lease.release(PROOF), false,
      'an unbound lease can never be credited');
    assert.equal(real.control.snapshot().heldBytes, source.bytes);
  });

test('bind throw and bind null retain the registered record and refuse a later dn free',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    const api = loadModule67669(memory);
    const fixture = setupSource(memory);
    writeDirtyDestination(memory, NATIVE.dest, fixture);

    const thrown = new Error('bind exploded');
    const throwing = makeFakeLease(fixture.bytes, {
      bind() { throw thrown; }
    });
    assert.throws(() => api.eioCodecparAdopt(NATIVE.dest, throwing.lease, fixture.bytes),
      (error) => error === thrown);
    assert.equal(throwing.state.bindCalls, 1);
    assert.deepEqual({ ...throwing.state.bindSummaries[0] }, { copiedBytes: fixture.bytes });
    assert.deepEqual(throwing.state.retainFailures.map((error) => error.code),
      ['EIO_CODECPAR_CLONE_UNPROVEN']);
    assert.equal(memory.frees.length, 0);
    expectCode(() => api.dn(NATIVE.dest), 'EIO_CODECPAR_CLONE_UNPROVEN');
    assert.equal(memory.frees.length, 0, 'the retained record is never freed or credited');
    assert.equal(throwing.state.releaseCalls, 0);

    writeDirtyDestination(memory, NATIVE.dest2, fixture);
    const nullBind = makeFakeLease(fixture.bytes, {
      bind() { return null; }
    });
    expectCode(() => api.eioCodecparAdopt(NATIVE.dest2, nullBind.lease, fixture.bytes),
      'EIO_CODECPAR_LEASE_BIND_FAILED');
    assert.deepEqual(nullBind.state.retainFailures.map((error) => error.code),
      ['EIO_CODECPAR_CLONE_UNPROVEN']);
    assert.equal(memory.frees.length, 0);
    expectCode(() => api.dn(NATIVE.dest2), 'EIO_CODECPAR_CLONE_UNPROVEN');
    assert.equal(memory.frees.length, 0);
    assert.equal(nullBind.state.releaseCalls, 0);
  });

test('a reentrant dn during bind sees the retained record and cannot free or credit',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    const api = loadModule67669(memory);
    const fixture = setupSource(memory);
    const dirty = writeDirtyDestination(memory, NATIVE.dest, fixture);
    const real = makeRealManager(api, memory);
    const admitted = real.manager.acquire(NATIVE.src, 1, 'mse-stream-codecpar');
    let reentrantError = null;
    const lease = {
      bytes: admitted.bytes,
      bind(summary) {
        assert.deepEqual({ ...summary }, { copiedBytes: admitted.bytes });
        try {
          api.dn(NATIVE.dest);
          assert.fail('reentrant dn must refuse the copying record');
        } catch (error) {
          reentrantError = error;
        }
        return admitted.bind(summary);
      },
      release(proof) {
        return admitted.release(proof);
      },
      retainFailure(error) {
        return admitted.retainFailure(error);
      }
    };

    api.eioCodecparAdopt(NATIVE.dest, lease, lease.bytes);
    assert.ok(reentrantError);
    assert.equal(reentrantError.code, 'EIO_CODECPAR_CLONE_UNPROVEN');
    assert.equal(memory.frees.length, 0,
      'the reentrant dn freed nothing while bind was still running');
    assert.deepEqual([
      real.manager.snapshot().boundCount,
      real.manager.snapshot().retainedCount,
      real.manager.snapshot().releasedCount,
      real.control.snapshot().heldBytes
    ], [1, 0, 0, fixture.bytes]);

    api.dn(NATIVE.dest);
    assert.deepEqual(memory.frees,
      [dirty.extraPointer, dirty.payloads[0], dirty.table, NATIVE.dest]);
    assert.deepEqual([real.manager.snapshot().releasedCount, real.manager.snapshot().liveBytes],
      [1, 0]);
    assert.deepEqual([real.control.snapshot().heldBytes, real.control.snapshot().countBound],
      [0, 0]);
  });

test('invalid destination pointers and unsafe expected byte counts retain and refuse',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    const api = loadModule67669(memory);
    const fixture = setupSource(memory);
    const cases = [
      [0, fixture.bytes],
      [-1, fixture.bytes],
      [0xffffffff, fixture.bytes],
      [NATIVE.dest, -1],
      [NATIVE.dest, Number.MAX_SAFE_INTEGER + 1],
      [NATIVE.dest, '5']
    ];
    for (const [dest, bytes] of cases) {
      const fake = makeFakeLease(fixture.bytes);
      expectCode(() => api.eioCodecparAdopt(dest, fake.lease, bytes),
        'EIO_CODECPAR_LEASE_INVALID');
      assert.equal(fake.state.bindCalls, 0);
      assert.deepEqual(fake.state.retainFailures.map((error) => error.code),
        ['EIO_CODECPAR_LEASE_INVALID'], 'a valid lease is retained, never released');
      assert.equal(fake.state.releaseCalls, 0);
      assert.equal(memory.frees.length, 0);
    }
  });

/* -------------------------------------------------------------------------
 * REAL module 26669 T8: optional exact-true payload allocation guard.
 * The same fake WASM memory serves the REAL T8 factory; only the allocator,
 * $W table append and lW byte copy are fake.
 * ---------------------------------------------------------------------- */

function module26669Text(text) {
  const marker = '26669:(e,t,i)=>{';
  const start = text.indexOf(marker);
  assert.ok(start >= 0, 'module 26669 not found');
  const bodyStart = start + marker.length - 1;
  let depth = 0;
  let quote = null;
  let escaped = false;
  for (let index = bodyStart; index < text.length; index += 1) {
    const character = text[index];
    if (quote !== null) {
      if (escaped) {
        escaped = false;
      } else if (character === '\\') {
        escaped = true;
      } else if (character === quote) {
        quote = null;
      }
      continue;
    }
    if (character === '/' && text[index + 1] === '*') {
      const end = text.indexOf('*/', index + 2);
      assert.notEqual(end, -1, 'unterminated factory comment');
      index = end + 1;
      continue;
    }
    if (character === '/' && text[index + 1] === '/') {
      index = text.indexOf('\n', index + 2);
      assert.notEqual(index, -1, 'unterminated factory line comment');
      continue;
    }
    if (character === '"' || character === "'" || character === '`') {
      quote = character;
    } else if (character === '{') {
      depth += 1;
    } else if (character === '}') {
      depth -= 1;
      if (depth === 0) return '(e,t,i)=>' + text.slice(bodyStart, index + 1);
    }
  }
  assert.fail('module 26669 end not found');
}

function loadModule26669(memory) {
  const getters = {
    8: (pointer) => memory.getI8(pointer),
    10: (pointer) => memory.getI32(pointer),
    15: (pointer) => memory.getI32(pointer),
    17: (pointer) => memory.getBigInt64(pointer),
    20: (pointer) => memory.getU32(pointer),
    25: (pointer) => memory.getU32(pointer)
  };
  const setters = {
    8: (pointer, value) => memory.setI8(pointer, value),
    10: (pointer, value) => memory.setI32(pointer, value),
    15: (pointer, value) => memory.setI32(pointer, value),
    17: (pointer, value) => memory.setBigInt64(pointer, value),
    20: (pointer, value) => memory.setU32(pointer, value),
    25: (pointer, value) => memory.setU32(pointer, value)
  };
  const requireStub = (id) => {
    if (id === 56238) return { f: getters };
    if (id === 6927) return { M: setters };
    if (id === 80209) {
      return {
        sO: (pointer, value, size) => memory.memset(pointer, value, size),
        lW: (dest, size, data) => {
          memory.write(dest, size);
          memory.lWrites.push({ dest, size });
          for (let index = 0; index < size; index += 1) {
            memory.buffer[dest + index] = data[index];
          }
        },
        s3: () => new Uint8Array(0)
      };
    }
    if (id === 46564) {
      return {
        $W: (pointer, countPointer, type, payload, size) => {
          const table = memory.getU32(pointer);
          const count = memory.getI32(countPointer);
          const next = memory.allocate(12 * (count + 1), 'calloc');
          if (table) memory.copy(next, table, 12 * count);
          const entry = next + 12 * count;
          memory.setI32(entry + 8, type);
          memory.setU32(entry + 4, size);
          memory.setU32(entry, payload);
          memory.setU32(pointer, next);
          memory.setI32(countPointer, count + 1);
        },
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
    if (id === 46626) {
      return {
        sY: (size) => memory.allocate(size, 'malloc'),
        Eb: (pointer) => memory.free(pointer)
      };
    }
    throw new Error('unexpected module id ' + id);
  };
  requireStub.d = (target, definitions) => {
    for (const key of Object.keys(definitions)) target[key] = definitions[key]();
  };
  const factory = vm.runInNewContext('(' + module26669Text(avplayerSource) + ')', {});
  const exports = {};
  factory({}, exports, requireStub);
  return exports;
}

function makeSerializedSource(options) {
  options = options || {};
  const extra = options.extra === undefined ? new Uint8Array([1, 2, 3]) : options.extra;
  const sides = options.sides === undefined
    ? [{ type: 2, data: new Uint8Array([4, 5]) }]
    : options.sides;
  return {
    codecType: 1,
    codecId: 2,
    codecTag: 3,
    format: 4,
    bitrate: 5n,
    bitsPerCodedSample: 6,
    bitsPerRawSample: 7,
    profile: 8,
    level: 9,
    width: 10,
    height: 11,
    sampleAspectRatio: { den: 12, num: 13 },
    framerate: { den: 14, num: 15 },
    fieldOrder: 16,
    colorRange: 17,
    colorPrimaries: 18,
    colorTrc: 19,
    colorSpace: 20,
    chromaLocation: 21,
    videoDelay: 22,
    chLayout: { order: 23, nbChannels: 24, u: 25 },
    sampleRate: 26,
    blockAlign: 27,
    frameSize: 28,
    initialPadding: 29,
    trailingPadding: 30,
    seekPreroll: 31,
    flags: 32,
    extradata: extra,
    codedSideData: sides
  };
}

test('actual T8 exacttrue copies payloads and matches baseline allocation/copy behavior',
  { timeout: 5000 }, () => {
    const source = makeSerializedSource();
    const exactMemory = makeNativeMemory();
    const baselineMemory = makeNativeMemory();
    const exact = loadModule26669(exactMemory);
    const baseline = loadModule26669(baselineMemory);

    assert.equal(exact.T8(source, NATIVE.dest, true), NATIVE.dest);
    assert.equal(baseline.T8(source, NATIVE.dest), NATIVE.dest);
    assert.deepEqual(exactMemory.allocs.map((alloc) => alloc.size),
      baselineMemory.allocs.map((alloc) => alloc.size),
      'exacttrue adds no allocation, reservation or copy');
    assert.deepEqual(exactMemory.lWrites, baselineMemory.lWrites);
    assert.deepEqual(
      exactMemory.buffer.slice(NATIVE.dest, NATIVE.dest + 168),
      baselineMemory.buffer.slice(NATIVE.dest, NATIVE.dest + 168));
    assert.equal(exactMemory.getI32(NATIVE.dest + 16), 3);
    assert.equal(exactMemory.getI32(NATIVE.dest + 24), 1);
    assert.equal(exactMemory.getU32(NATIVE.dest + 20) > 0, true);
  });

test('actual T8 exacttrue refuses malloc0 for a positive extradata before any copy',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    memory.failedAllocNumbers.add(1);
    const api = loadModule26669(memory);
    const source = makeSerializedSource();

    expectCode(() => api.T8(source, NATIVE.dest, true),
      'EIO_CODECPAR_ALLOC_FAILED');
    assert.equal(memory.allocs[0].failed, true);
    assert.equal(memory.allocs[0].size, 3);
    assert.equal(memory.getU32(NATIVE.dest + 12), 0,
      'the failed payload pointer is never published');
    assert.equal(memory.getI32(NATIVE.dest + 16), 0);
    assert.deepEqual(memory.lWrites, [], 'no payload copy starts after malloc0');
    assert.equal(memory.frees.length, 0);
  });

test('actual T8 exacttrue refuses malloc0 for a positive side payload before that copy',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    memory.failedAllocNumbers.add(2);
    const api = loadModule26669(memory);
    const source = makeSerializedSource();

    expectCode(() => api.T8(source, NATIVE.dest, true),
      'EIO_CODECPAR_ALLOC_FAILED');
    assert.equal(memory.allocs[0].failed, undefined);
    assert.equal(memory.allocs[1].failed, true);
    assert.equal(memory.allocs[1].size, 2);
    assert.equal(memory.getU32(NATIVE.dest + 12) > 0, true);
    assert.equal(memory.getI32(NATIVE.dest + 16), 3);
    assert.deepEqual(memory.lWrites.map((write) => write.size), [3],
      'the extradata copy completed before the side malloc0, the side copy did not');
    assert.equal(memory.getU32(NATIVE.dest + 20), 0);
    assert.equal(memory.getI32(NATIVE.dest + 24), 0);
    assert.equal(memory.frees.length, 0);
  });

test('baseline T8 without exacttrue keeps the original unchecked malloc behavior',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    memory.failedAllocNumbers.add(2);
    const api = loadModule26669(memory);
    const source = makeSerializedSource();

    assert.equal(api.T8(source, NATIVE.dest), NATIVE.dest,
      'the baseline call shape and unchecked behavior are unchanged');
    assert.equal(memory.allocs[1].failed, true);
    assert.deepEqual(memory.lWrites.map((write) => write.size), [3, 2],
      'baseline still attempts the original side copy');
    assert.equal(memory.getU32(NATIVE.dest + 20) > 0, true);
    assert.equal(memory.getI32(NATIVE.dest + 24), 1);
    const table = memory.getU32(NATIVE.dest + 20);
    assert.equal(memory.getU32(table), 0, 'baseline stores the unchecked malloc0 pointer');
    assert.equal(memory.getU32(table + 4), 2);
  });
