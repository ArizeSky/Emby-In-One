'use strict';

/*
 * P2 method-scope tests for the additive, still-unused
 * public/vendor/web/libmedia/avplayer.js module 67669
 * eioCodecparReplace(dest, source, newLease) export.
 *
 * The REAL module 67669 factory is sliced out of the shipped bundle and run
 * over fake WASM memory and a fake allocator. Leases are REAL
 * public/web/player-codecpar-leases.js leases admitted by the REAL shared
 * ownership core through the REAL owned-resource wrapper. Fake/proxy leases are
 * used only to inject release/bind faults and deliberate reentrancy.
 *
 * This is method scope only: no Worker, browser, MSE, BSF, real upstream,
 * production instance or native media is involved, and no test calls the
 * running application.
 */

const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const ROOT = path.join(__dirname, '..');
const AVPLAYER_PATH = path.join(ROOT, 'public', 'vendor', 'web', 'libmedia', 'avplayer.js');
const CHUNK26_PATH = path.join(ROOT, 'public', 'vendor', 'web', 'libmedia', '26.avplayer.js');
const core = require('../public/web/player-shared-media-ownership.js');
const owned = require('../public/web/player-owned-media-resources.js');
const codecpar = require('../public/web/player-codecpar-leases.js');

const avplayerSource = fs.readFileSync(AVPLAYER_PATH, 'utf8');
const chunk26Source = fs.readFileSync(CHUNK26_PATH, 'utf8');

const MEMORY_SIZE = 0x800000;
const STRUCT_BYTES = 168;
const LAYOUT_OFFSET = 112;
const LAYOUT_ORDER_UNSPEC = 0;
const LAYOUT_ORDER_NATIVE = 1;
const LAYOUT_ORDER_CUSTOM = 2;
const LAYOUT_ORDER_AMBISONIC = 3;
const PROOF = Object.freeze({ referencesDropped: true });

function module67669Text(text) {
  const start = text.indexOf('67669:(e,t,i)=>{');
  assert.ok(start >= 0, 'module 67669 not found');
  const end = text.indexOf('},67994:', start);
  assert.ok(end > start, 'module 67669 end not found');
  return text.slice(start + '67669:'.length, end + 1);
}

const moduleText = module67669Text(avplayerSource);

function makeNativeMemory() {
  const buffer = new Uint8Array(MEMORY_SIZE);
  const view = new DataView(buffer.buffer);
  let bump = 0x3000;
  const memory = {
    buffer,
    view,
    allocs: [],
    frees: [],
    copyWrites: [],
    failedAllocNumbers: new Set(),
    failFreeNumbers: new Set(),
    freeCalls: 0,
    read(pointer, size) {
      if (!Number.isInteger(pointer) || pointer < 0 || !Number.isInteger(size) ||
          size < 0 || pointer + size > MEMORY_SIZE) {
        throw new RangeError('fake native memory read out of range');
      }
    },
    write(pointer, size) {
      if (!Number.isInteger(pointer) || pointer < 0 || !Number.isInteger(size) ||
          size < 0 || pointer + size > MEMORY_SIZE) {
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
      this.copyWrites.push({ dest, source, size });
    },
    memset(pointer, value, size) {
      this.write(pointer, size);
      buffer.fill(value, pointer, pointer + size);
    },
    allocate(size, how) {
      const index = this.allocs.length + 1;
      if (this.failedAllocNumbers.has(index)) {
        this.allocs.push({ index, pointer: 0, size, how, failed: true });
        return 0;
      }
      const pointer = bump;
      bump += Math.max(1, size);
      if (bump > MEMORY_SIZE) throw new RangeError('fake allocator exhausted');
      this.allocs.push({ index, pointer, size, how });
      if (how === 'calloc') buffer.fill(0, pointer, pointer + size);
      return pointer;
    },
    free(pointer) {
      if (!pointer) throw new Error('free(0)');
      this.freeCalls += 1;
      if (this.failFreeNumbers.has(this.freeCalls)) {
        throw new Error('fake native free failure at call ' + this.freeCalls);
      }
      this.frees.push(pointer);
    }
  };
  return memory;
}

function loadModule67669(memory, options) {
  options = options || {};
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
          if (options.falseFree === 'aI') return false;
          const value = memory.getU32(pointer);
          memory.free(value);
          memory.setU32(pointer, 0);
        }
      };
    }
    if (id === 57216) {
      /* Shipped av_channel_layout_uninit(): only order===2 owns a native map. */
      return {
        bl: (pointer) => {
          if (options.falseFree === 'bl') return false;
          if (memory.getI32(pointer) === LAYOUT_ORDER_CUSTOM) {
            const map = memory.getU32(pointer + 8);
            if (map) memory.free(map);
            memory.setU32(pointer + 8, 0);
          }
          memory.memset(pointer, 0, 24);
        }
      };
    }
    if (id === 46564) {
      return {
        NZ: (pointer, countPointer) => {
          if (options.falseFree === 'NZ') return false;
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
  const factory = vm.runInNewContext('(' + moduleText + ')', {});
  const exports = {};
  factory({}, exports, requireStub);
  return exports;
}

function writeLayout(memory, address, layout) {
  memory.setI32(address + LAYOUT_OFFSET, layout.order);
  memory.setI32(address + LAYOUT_OFFSET + 4, layout.nbChannels === undefined ? 0 : layout.nbChannels);
  memory.setU32(address + LAYOUT_OFFSET + 8, layout.mask === undefined ? 0 : layout.mask);
  memory.setU32(address + LAYOUT_OFFSET + 12, 0);
  memory.setU32(address + LAYOUT_OFFSET + 16, layout.opaque === undefined ? 0 : layout.opaque);
  memory.setU32(address + LAYOUT_OFFSET + 20, 0);
}

function writeCodecparStruct(memory, address, fixture) {
  fixture = fixture || {};
  const extra = fixture.extra || [];
  const sides = fixture.sides || [];
  const extraPointer = extra.length ? memory.allocate(extra.length, 'malloc') : 0;
  if (extra.length) memory.buffer.set(extra, extraPointer);
  memory.setI32(address, fixture.codecType === undefined ? 1 : fixture.codecType);
  memory.setI32(address + 4, fixture.codecId === undefined ? 86018 : fixture.codecId);
  memory.setI8(address + 8, fixture.codecTag === undefined ? 0 : fixture.codecTag);
  memory.setU32(address + 12, extraPointer);
  memory.setI32(address + 16, extra.length);
  const payloads = [];
  let table = 0;
  if (sides.length) {
    table = memory.allocate(12 * sides.length, 'calloc');
    sides.forEach((side, index) => {
      const payload = memory.allocate(side.bytes.length, 'malloc');
      if (side.bytes.length) memory.buffer.set(side.bytes, payload);
      payloads.push(payload);
      const entry = table + 12 * index;
      memory.setU32(entry, payload);
      memory.setU32(entry + 4, side.bytes.length);
      memory.setI32(entry + 8, side.type);
    });
    memory.setU32(address + 20, table);
    memory.setI32(address + 24, sides.length);
  }
  writeLayout(memory, address, fixture.layout || {
    order: LAYOUT_ORDER_NATIVE,
    nbChannels: 2,
    mask: 3
  });
  return {
    extra,
    sides,
    extraPointer,
    table,
    payloads,
    bytes: extra.length + sides.reduce((sum, side) => sum + side.bytes.length, 0)
  };
}

function readPayloadPointers(memory, address) {
  const extra = memory.getU32(address + 12);
  const table = memory.getU32(address + 20);
  const count = memory.getI32(address + 24);
  const sides = [];
  for (let index = 0; index < count; index += 1) {
    sides.push(memory.getU32(table + 12 * index));
  }
  return { extra, table, count, sides };
}

function makeRealManager(api, memory, capacityBytes) {
  const control = core.create({ capacityBytes: capacityBytes || 4096, maxEntries: 8 });
  const freesAtRelease = [];
  const allocsAtRelease = [];
  const heldAtRelease = [];
  const resources = owned.createOwnedResources({
    reserve: (bytes, generation) => control.reserve(bytes, generation),
    bindOwnedResource: (token) => control.bindOwnedResource(token),
    snapshot: () => control.snapshot(),
    releaseOwnedResource(handle, proof) {
      freesAtRelease.push(memory.frees.length);
      allocsAtRelease.push(memory.allocs.length);
      heldAtRelease.push(control.snapshot().heldBytes);
      return control.releaseOwnedResource(handle, proof);
    }
  });
  const manager = codecpar.createCodecparLeases({
    resources,
    measureNativePayload: (pointer) => api.eioPayloadBytes(pointer)
  });
  return { control, manager, freesAtRelease, allocsAtRelease, heldAtRelease };
}

/* Delegating lease wrapper: only the injected callback changes. */
function proxyLease(lease, overrides) {
  overrides = overrides || {};
  return {
    bytes: lease.bytes,
    bind(summary) {
      return overrides.bind ? overrides.bind(summary, lease) : lease.bind(summary);
    },
    release(proof) {
      return overrides.release ? overrides.release(proof, lease) : lease.release(proof);
    },
    retainFailure(error) {
      return lease.retainFailure(error);
    }
  };
}

function expectCode(action, code) {
  let observed;
  assert.throws(action, (error) => { observed = error; return error && error.code === code; });
  return observed;
}

/* Build a REAL leased clone of `fixture` at a fresh struct address. */
function leasedClone(memory, api, real, fixture, generation) {
  const source = memory.allocate(STRUCT_BYTES, 'calloc');
  const sourceFixture = writeCodecparStruct(memory, source, fixture);
  const lease = real.manager.acquire(source, generation, 'mse-stream-codecpar');
  assert.equal(lease.bytes, sourceFixture.bytes, 'the admitted claim equals the measured payload');
  const dest = memory.allocate(STRUCT_BYTES, 'calloc');
  api.Yi(dest, source, lease);
  return { dest, source, sourceFixture, lease };
}

const OLD_FIXTURE = Object.freeze({
  codecId: 86018,
  extra: [1, 2, 3],
  sides: [{ bytes: [4, 5], type: 2 }, { bytes: [6], type: 3 }]
});
const NEW_FIXTURE = Object.freeze({
  codecId: 86019,
  extra: [7, 8, 9, 10],
  sides: [{ bytes: [11, 12], type: 2 }]
});

/* -------------------------------------------------------------------------
 * Static shape: additive export, inert, baselines untouched.
 * ---------------------------------------------------------------------- */

test('replace is an inert additive export and keeps the Yi/dn/copy/clear baselines',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    const api = loadModule67669(memory);
    assert.equal(typeof api.eioCodecparReplace, 'function');
    assert.equal(typeof api.eioCodecparAdopt, 'function');
    assert.equal(typeof api.Yi, 'function');
    assert.equal(typeof api.dn, 'function');
    assert.equal(typeof api.eioPayloadBytes, 'function');

    assert.equal((moduleText.match(/eioCodecparReplace/g) || []).length, 3,
      'one definition plus one export pair, no call site');
    assert.equal((avplayerSource.match(/eioCodecparReplace/g) || []).length, 3,
      'the new name only exists inside module 67669');
    assert.equal(chunk26Source.includes('eioCodecparReplace'), false,
      'chunk 26 does not call the replacement yet');
    assert.ok(moduleText.includes(
      'eioCodecparAdopt:()=>eioCodecparAdopt,eioCodecparReplace:()=>eioCodecparReplace}'));

    const start = moduleText.indexOf('function eioCodecparReplace(eioDest, eioSource, eioNewLease) {');
    const end = moduleText.indexOf('function c(eioDest,eioSource,eioLease)', start);
    assert.ok(start > 0 && end > start, 'replace is a separate function');
    const replaceText = moduleText.slice(start, end);
    assert.match(replaceText, /eioOldRecord\.lease\.release\(\{ referencesDropped: !0 \}\)/);
    assert.equal((replaceText.match(/\.release\s*\(/g) || []).length, 1,
      'the only release call is the single old-lease credit');
    assert.doesNotMatch(replaceText, /eioNewLease\.release/);
    assert.doesNotMatch(replaceText, /eioCodecparCopyOriginal/);
    assert.match(replaceText, /eioCodecparCopyLeased\(eioDest, eioSource, eioRescan\)/);
    assert.match(replaceText, /eioCodecparFreePayloads\(eioDest\)/);
    assert.match(replaceText, /eioCodecparPayloadsDisjoint\(eioDest, eioOldScan, eioSource, eioNewScan\)/);
    assert.match(replaceText, /eioCodecparLayoutModeProven/);
    assert.match(replaceText, /eioCodecparScansMatch\(eioNewScan, eioRescan\)/);
    assert.doesNotMatch(replaceText, /eioCodecparLeases\.delete/, 'no record deletion and no reset');
    assert.doesNotMatch(replaceText, /for \(/, 'no retry loop');
    assert.ok(replaceText.indexOf('eioCodecparFreePayloads(eioDest)') <
      replaceText.indexOf('eioCodecparLeases.set(eioDest, eioNewRecord)'),
    'the new record is published only after the old payload free');
    assert.ok(replaceText.indexOf('eioCodecparLeases.set(eioDest, eioNewRecord)') <
      replaceText.indexOf('eioCodecparCopyLeased('),
    'the new copying record is published before any copy allocation');
  });

test('the Yi, dn, original copy and original clear bodies stay byte-identical',
  { timeout: 5000 }, () => {
    assert.ok(moduleText.includes(
      'function c(eioDest,eioSource,eioLease){if(void 0===eioLease){if(eioCodecparLeases&&' +
      'eioCodecparLeases.has(eioDest))throw eioCodecparLeaseError("EIO_CODECPAR_LEASE_DEST_BUSY",' +
      '"codecpar destination already leased");return eioCodecparCopyOriginal(eioDest,eioSource)}'),
    'the two-argument Yi branch is unchanged');
    assert.ok(moduleText.includes(
      'function l(eioPointer){if(!eioPointer||!eioCodecparLeases)return ' +
      'eioCodecparClearOriginal(eioPointer);var eioRecord=eioCodecparLeases.get(eioPointer);' +
      'if(!eioRecord)return eioCodecparClearOriginal(eioPointer);if("bound"!==eioRecord.state)' +
      'throw eioCodecparLeaseError(eioRecord.failureCode||"EIO_CODECPAR_CLONE_UNPROVEN",' +
      '"codecpar clone retained");eioRecord.state="closing";'),
    'the dn entry and its bound-state gate are unchanged');
    assert.ok(moduleText.includes(
      'function eioCodecparCopyOriginal(e,t){a.M[15](e,s.f[15](t)),a.M[15](e+4,s.f[15](t+4)),' +
      'a.M[8](e+8,s.f[8](t+8)),'),
    'the original copy still starts with the unguarded scalar copy');
    assert.ok(moduleText.includes(
      'function eioCodecparClearOriginal(e){e&&(function(e){s.f[20](e+12)&&(0,o.aI)(e+12),' +
      '(0,n.bl)(e+112),(0,h.NZ)(e+20,e+24),(0,r.sO)(e,0,168),'),
    'the original clear keeps its own inline payload-free sequence');
    assert.ok(moduleText.includes(
      'a.M[15](e+48,d.N_),a.M[15](e+52,d.N_),a.M[15](e+160,0)}(e),(0,o.Eb)(e))}}'),
    'the original clear still frees the struct itself');

    const dnStart = moduleText.indexOf('function l(eioPointer){');
    const dnEnd = moduleText.indexOf('function eioCodecparClearOriginal', dnStart);
    assert.ok(dnStart > 0 && dnEnd > dnStart);
    const dnText = moduleText.slice(dnStart, dnEnd);
    assert.doesNotMatch(dnText, /eioCodecparFreePayloads/,
      'dn never routes through the new payload-only helper');
    assert.ok(dnText.includes('eioRecord.freed=!0;'));
    assert.ok(dnText.endsWith('return eioResult}'));
  });

test('baseline two-argument Yi and dn still copy and free without any lease',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    const api = loadModule67669(memory);
    const source = memory.allocate(STRUCT_BYTES, 'calloc');
    writeCodecparStruct(memory, source, OLD_FIXTURE);
    const dest = memory.allocate(STRUCT_BYTES, 'calloc');

    api.Yi(dest, source);
    const pointers = readPayloadPointers(memory, dest);
    assert.deepEqual(memory.buffer.slice(pointers.extra, pointers.extra + 3),
      Uint8Array.from([1, 2, 3]));
    assert.equal(memory.getI32(dest + 16), 3);
    assert.equal(memory.getI32(dest + 24), 2);
    assert.deepEqual(memory.frees, []);

    api.dn(dest);
    assert.deepEqual(memory.frees,
      [pointers.extra, pointers.sides[0], pointers.sides[1], pointers.table, dest],
      'the baseline dn frees payloads, the side table and then the struct');
  });

/* -------------------------------------------------------------------------
 * Happy path.
 * ---------------------------------------------------------------------- */

test('a real leased clone is replaced: old payloads free, old credits, new copies, dn settles both',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    const api = loadModule67669(memory);
    const real = makeRealManager(api, memory);
    const clone = leasedClone(memory, api, real, OLD_FIXTURE, 1);
    const oldLease = clone.lease;
    assert.equal(oldLease.bytes, 6);
    assert.equal(memory.frees.length, 0, 'the Yi clone frees nothing');
    assert.equal(real.manager.snapshot().boundCount, 1);
    const oldPointers = readPayloadPointers(memory, clone.dest);
    assert.equal(oldPointers.count, 2);

    const newSource = memory.allocate(STRUCT_BYTES, 'calloc');
    const newFixture = writeCodecparStruct(memory, newSource, NEW_FIXTURE);
    assert.equal(newFixture.bytes, 6);
    const newLease = real.manager.acquire(newSource, 2, 'mse-stream-codecpar');
    assert.deepEqual(
      [real.control.snapshot().heldBytes, real.control.snapshot().countBound], [12, 2],
      'both the old and the new payload claims are held and bound before the replacement');

    const allocsBefore = memory.allocs.length;
    assert.equal(api.eioCodecparReplace(clone.dest, newSource, newLease), undefined);

    assert.deepEqual(memory.frees,
      [oldPointers.extra, oldPointers.sides[0], oldPointers.sides[1], oldPointers.table],
      'only the old extradata, side payloads and side table are freed, in inner-clear order');
    assert.equal(memory.frees.includes(clone.dest), false,
      'the destination struct is never freed during replace');
    assert.deepEqual(real.freesAtRelease, [4],
      'the old lease credit happens after every old payload free');
    assert.deepEqual(real.heldAtRelease, [12],
      'both claims are still charged when the old lease is credited');
    assert.deepEqual(real.allocsAtRelease, [allocsBefore],
      'no replacement payload is allocated before the old credit');

    const copied = readPayloadPointers(memory, clone.dest);
    assert.equal(memory.getI32(clone.dest + 4), 86019, 'the replacement scalars are copied');
    assert.equal(memory.getI32(clone.dest + 16), 4);
    assert.equal(memory.getI32(clone.dest + 24), 1);
    assert.deepEqual(memory.buffer.slice(copied.extra, copied.extra + 4),
      Uint8Array.from([7, 8, 9, 10]), 'the replacement extradata is a real copy');
    assert.equal(memory.getU32(copied.table + 4), 2);
    assert.deepEqual(memory.buffer.slice(copied.sides[0], copied.sides[0] + 2),
      Uint8Array.from([11, 12]), 'the replacement side payload is a real copy');
    assert.equal(memory.getI32(clone.dest + LAYOUT_OFFSET), LAYOUT_ORDER_NATIVE);
    assert.equal(memory.getI32(clone.dest + LAYOUT_OFFSET + 4), 2);
    assert.equal(memory.getU32(clone.dest + LAYOUT_OFFSET + 8), 3);
    assert.deepEqual(
      [real.manager.snapshot().releasedCount, real.manager.snapshot().liveBytes], [1, 6],
      'only the old claim is credited by the replacement');
    assert.deepEqual(
      [real.control.snapshot().heldBytes, real.control.snapshot().countBound], [6, 1]);

    assert.equal(api.dn(clone.dest), undefined);
    assert.deepEqual(memory.frees.slice(4),
      [copied.extra, copied.sides[0], copied.table, clone.dest],
      'the final dn frees the replacement payloads, table and struct exactly once');
    assert.deepEqual(
      [real.manager.snapshot().releasedCount, real.manager.snapshot().liveBytes], [2, 0]);
    assert.deepEqual(
      [real.control.snapshot().heldBytes, real.control.snapshot().countBound], [0, 0]);
    assert.equal(newLease.release(PROOF), false, 'the replacement lease is never credited twice');
    assert.deepEqual(
      [real.manager.snapshot().releasedCount, real.control.snapshot().heldBytes], [2, 0]);
  });

test('a zero-byte replacement pair is a valid lease and frees nothing before dn',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    const api = loadModule67669(memory);
    const real = makeRealManager(api, memory);
    const clone = leasedClone(memory, api, real, { codecId: 86018, extra: [], sides: [] }, 1);
    assert.equal(clone.lease.bytes, 0);

    const newSource = memory.allocate(STRUCT_BYTES, 'calloc');
    writeCodecparStruct(memory, newSource, { codecId: 86019, extra: [], sides: [] });
    const newLease = real.manager.acquire(newSource, 2, 'mse-stream-codecpar');
    assert.equal(newLease.bytes, 0);

    assert.equal(api.eioCodecparReplace(clone.dest, newSource, newLease), undefined);
    assert.deepEqual(memory.frees, [], 'nothing is freed for a zero-byte payload pair');
    assert.equal(memory.getI32(clone.dest + 4), 86019);
    assert.equal(memory.getU32(clone.dest + 12), 0);
    assert.equal(memory.getI32(clone.dest + 16), 0);
    assert.equal(memory.getI32(clone.dest + 24), 0);
    assert.deepEqual(
      [real.control.snapshot().heldBytes, real.control.snapshot().countBound], [0, 1]);
    assert.deepEqual(
      [real.manager.snapshot().releasedCount, real.manager.snapshot().boundCount], [1, 2]);

    assert.equal(api.dn(clone.dest), undefined);
    assert.deepEqual(memory.frees, [clone.dest], 'only the struct free remains for dn');
    assert.deepEqual(
      [real.manager.snapshot().releasedCount, real.control.snapshot().heldBytes], [2, 0]);
  });

test('repeated replacements with fresh claims stay accounted and never leak the struct',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    const api = loadModule67669(memory);
    const real = makeRealManager(api, memory);
    const clone = leasedClone(memory, api, real, { extra: [1, 2], sides: [] }, 1);
    const fixtures = [
      { extra: [3, 4, 5], sides: [{ bytes: [6], type: 2 }] },
      { extra: [], sides: [{ bytes: [7, 8], type: 3 }] },
      { extra: [9], sides: [] }
    ];
    fixtures.forEach((fixture, index) => {
      const source = memory.allocate(STRUCT_BYTES, 'calloc');
      writeCodecparStruct(memory, source, fixture);
      const lease = real.manager.acquire(source, index + 2, 'mse-stream-codecpar');
      assert.equal(api.eioCodecparReplace(clone.dest, source, lease), undefined);
    });
    assert.equal(real.manager.snapshot().releasedCount, fixtures.length,
      'each superseded claim is credited exactly once');
    assert.equal(memory.frees.filter((pointer) => pointer === clone.dest).length, 0,
      'the struct is still owned by the destination before dn');
    api.dn(clone.dest);
    assert.equal(memory.frees.filter((pointer) => pointer === clone.dest).length, 1,
      'the struct is freed exactly once');
    assert.deepEqual(
      [real.manager.snapshot().releasedCount, real.manager.snapshot().liveBytes], [4, 0]);
    assert.deepEqual(
      [real.control.snapshot().heldBytes, real.control.snapshot().countBound], [0, 0]);
  });

test('a 65536-side replacement proves disjointness by numeric adjacent sort',
  { timeout: 20000 }, () => {
    const memory = makeNativeMemory();
    const api = loadModule67669(memory);
    const real = makeRealManager(api, memory, 1 << 20);
    const sideCount = 65536;
    const sides = Array.from({ length: sideCount }, (_, index) => ({
      bytes: [index & 255],
      type: 2
    }));
    const clone = leasedClone(memory, api, real, { extra: [1], sides }, 1);
    const oldPointers = readPayloadPointers(memory, clone.dest);
    assert.equal(oldPointers.count, sideCount);
    const newSource = memory.allocate(STRUCT_BYTES, 'calloc');
    writeCodecparStruct(memory, newSource, { extra: [2], sides });
    const newLease = real.manager.acquire(newSource, 2, 'mse-stream-codecpar');

    assert.equal(api.eioCodecparReplace(clone.dest, newSource, newLease), undefined);
    assert.equal(memory.frees.length, sideCount + 2,
      'one extradata, every old side payload and the old table are freed exactly once');
    assert.equal(memory.frees.includes(clone.dest), false,
      'the destination struct is not freed by the many-side replacement');
    assert.deepEqual(
      [real.manager.snapshot().releasedCount, real.control.snapshot().countBound], [1, 1]);
    const copied = readPayloadPointers(memory, clone.dest);
    assert.equal(copied.count, sideCount);
    assert.ok(copied.extra > 0 && copied.table > 0);

    api.dn(clone.dest);
    assert.equal(memory.frees.filter((pointer) => pointer === clone.dest).length, 1,
      'the replacement struct is freed exactly once');
    assert.deepEqual(
      [real.manager.snapshot().releasedCount, real.control.snapshot().heldBytes], [2, 0]);
  });

/* -------------------------------------------------------------------------
 * Precondition refusals: no free, no copy, no credit, old record intact.
 * ---------------------------------------------------------------------- */

test('an unbound destination is refused before any mutation', { timeout: 5000 }, () => {
  const memory = makeNativeMemory();
  const api = loadModule67669(memory);
  const real = makeRealManager(api, memory);
  const source = memory.allocate(STRUCT_BYTES, 'calloc');
  const sourceFixture = writeCodecparStruct(memory, source, OLD_FIXTURE);
  const lease = real.manager.acquire(source, 1, 'mse-stream-codecpar');
  const dest = memory.allocate(STRUCT_BYTES, 'calloc');
  writeCodecparStruct(memory, dest, { extra: [9, 9, 9], sides: [] });
  const allocsBefore = memory.allocs.length;

  expectCode(() => api.eioCodecparReplace(dest, source, lease), 'EIO_CODECPAR_LEASE_DEST_UNBOUND');
  assert.deepEqual(memory.frees, [], 'the refusal frees nothing');
  assert.equal(memory.allocs.length, allocsBefore, 'the refusal allocates nothing');
  assert.deepEqual(
    [real.manager.snapshot().retainedCount, real.manager.snapshot().boundCount,
      real.manager.snapshot().releasedCount], [1, 0, 0]);
  assert.equal(real.manager.snapshot().faults[
    real.manager.snapshot().faults.length - 1].code, 'EIO_CODECPAR_LEASE_DEST_UNBOUND');
  assert.equal(lease.release(PROOF), false, 'a refused claim cannot be credited later');
  assert.equal(real.control.snapshot().heldBytes, sourceFixture.bytes);

  api.dn(dest);
  assert.equal(real.manager.snapshot().releasedCount, 0,
    'the unleased destination frees without any lease credit');
});

test('a non-bound destination record is refused and its retained record still refuses dn',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    const api = loadModule67669(memory);
    const real = makeRealManager(api, memory);
    const dest = memory.allocate(STRUCT_BYTES, 'calloc');
    writeCodecparStruct(memory, dest, { extra: [1, 2, 3], sides: [] });
    const retainedClaim = real.manager.acquireMeasured(3, 1, 'mse-stream-codecpar');
    const failingBind = proxyLease(retainedClaim, { bind() { return false; } });
    expectCode(() => api.eioCodecparAdopt(dest, failingBind, 3),
      'EIO_CODECPAR_LEASE_BIND_FAILED');

    const newSource = memory.allocate(STRUCT_BYTES, 'calloc');
    writeCodecparStruct(memory, newSource, { extra: [4, 5, 6], sides: [] });
    const newLease = real.manager.acquire(newSource, 2, 'mse-stream-codecpar');
    expectCode(() => api.eioCodecparReplace(dest, newSource, newLease),
      'EIO_CODECPAR_LEASE_DEST_BUSY');
    assert.deepEqual(memory.frees, []);
    expectCode(() => api.dn(dest), 'EIO_CODECPAR_CLONE_UNPROVEN');
    assert.deepEqual(memory.frees, [], 'the retained record is never freed');
    assert.equal(newLease.release(PROOF), false);
    assert.equal(real.manager.snapshot().releasedCount, 0);
  });

test('pointer and size precondition faults retain the claim and mutate nothing',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    const api = loadModule67669(memory);
    const real = makeRealManager(api, memory);
    const clone = leasedClone(memory, api, real, OLD_FIXTURE, 1);
    const pointers = readPayloadPointers(memory, clone.dest);

    const fake = {
      bytes: 6,
      bind() { return true; },
      release() { return true; },
      retainFailure() { return false; }
    };
    expectCode(() => api.eioCodecparReplace(clone.dest, clone.dest, fake),
      'EIO_CODECPAR_LEASE_INVALID');
    expectCode(() => api.eioCodecparReplace(0, clone.source, fake),
      'EIO_CODECPAR_LEASE_INVALID');
    expectCode(() => api.eioCodecparReplace(clone.dest, clone.source, {
      bytes: -1, bind() { return true; }, release() { return true; }, retainFailure() {}
    }), 'EIO_CODECPAR_LEASE_INVALID');
    expectCode(() => api.eioCodecparReplace(clone.dest, clone.source, {
      bytes: 1.5, bind() { return true; }, release() { return true; }, retainFailure() {}
    }), 'EIO_CODECPAR_LEASE_INVALID');
    assert.deepEqual(memory.frees, []);
    assert.equal(memory.getU32(clone.dest + 12), pointers.extra, 'the old backing is untouched');

    const otherSource = memory.allocate(STRUCT_BYTES, 'calloc');
    writeCodecparStruct(memory, otherSource, { extra: [1, 2], sides: [] });
    const shortLease = real.manager.acquire(otherSource, 2, 'mse-stream-codecpar');
    assert.equal(shortLease.bytes, 2);
    expectCode(() => api.eioCodecparReplace(clone.dest, clone.source, shortLease),
      'EIO_CODECPAR_LEASE_SIZE_MISMATCH');
    assert.deepEqual(memory.frees, []);
    assert.equal(shortLease.release(PROOF), false);

    /* A destination payload that changed after bind is not the leased set. */
    const mutatedLease = real.manager.acquire(clone.source, 3, 'mse-stream-codecpar');
    memory.setI32(clone.dest + 16, 4);
    expectCode(() => api.eioCodecparReplace(clone.dest, otherSource, mutatedLease),
      'EIO_CODECPAR_LEASE_SIZE_MISMATCH');
    assert.deepEqual(memory.frees, [], 'no partially matching destination is freed');
    assert.equal(mutatedLease.release(PROOF), false);
    memory.setI32(clone.dest + 16, 3);

    const leasedRecord = readPayloadPointers(memory, clone.dest);
    api.dn(clone.dest);
    assert.deepEqual(memory.frees,
      [leasedRecord.extra, leasedRecord.sides[0], leasedRecord.sides[1],
        leasedRecord.table, clone.dest],
    'the untouched old record still settles through the ordinary dn path');
    assert.equal(real.manager.snapshot().releasedCount, 1, 'only the old clone was ever credited');
  });

test('reusing the destination lease is refused without poisoning or releasing it',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    const api = loadModule67669(memory);
    const real = makeRealManager(api, memory);
    const clone = leasedClone(memory, api, real, OLD_FIXTURE, 1);
    const faultCount = real.manager.snapshot().faultCount;

    expectCode(() => api.eioCodecparReplace(clone.dest, clone.source, clone.lease),
      'EIO_CODECPAR_LEASE_SAME_LEASE');
    expectCode(() => api.eioCodecparReplace(clone.dest, clone.dest, clone.lease),
      'EIO_CODECPAR_LEASE_SAME_LEASE');
    assert.deepEqual(memory.frees, []);
    assert.equal(real.manager.snapshot().faultCount, faultCount,
      'the same-lease misuse is not turned into a retained failure');
    assert.equal(real.manager.snapshot().retainedCount, 0,
      'the existing old lease is not poisoned');

    const pointers = readPayloadPointers(memory, clone.dest);
    api.dn(clone.dest);
    assert.deepEqual(memory.frees,
      [pointers.extra, pointers.sides[0], pointers.sides[1], pointers.table, clone.dest]);
    assert.deepEqual(
      [real.manager.snapshot().releasedCount, real.control.snapshot().heldBytes], [1, 0],
      'the old lease stayed fully creditable through the normal dn path');
  });

/* -------------------------------------------------------------------------
 * Disjointness: aliased payloads are refused before any old free.
 * ---------------------------------------------------------------------- */

test('a distinct source struct aliasing the old backing is refused with no free and no credit',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    const api = loadModule67669(memory);
    const real = makeRealManager(api, memory);
    const clone = leasedClone(memory, api, real, OLD_FIXTURE, 1);
    const pointers = readPayloadPointers(memory, clone.dest);

    const aliasExtradata = memory.allocate(STRUCT_BYTES, 'calloc');
    memory.setU32(aliasExtradata + 12, pointers.extra);
    memory.setI32(aliasExtradata + 16, 3);
    writeLayout(memory, aliasExtradata, { order: LAYOUT_ORDER_NATIVE, nbChannels: 2, mask: 3 });
    const extraLease = real.manager.acquire(aliasExtradata, 2, 'mse-stream-codecpar');
    assert.equal(extraLease.bytes, 3);

    expectCode(() => api.eioCodecparReplace(clone.dest, aliasExtradata, extraLease),
      'EIO_CODECPAR_PAYLOAD_OVERLAP');
    assert.deepEqual(memory.frees, [], 'no old backing is freed while the source aliases it');
    assert.equal(extraLease.release(PROOF), false);
    assert.equal(real.manager.snapshot().releasedCount, 0);
    assert.equal(real.manager.snapshot().retainedCount, 1);

    const aliasTable = memory.allocate(STRUCT_BYTES, 'calloc');
    memory.setU32(aliasTable + 12, pointers.table);
    memory.setI32(aliasTable + 16, 12);
    writeLayout(memory, aliasTable, { order: LAYOUT_ORDER_NATIVE, nbChannels: 2, mask: 3 });
    const tableLease = real.manager.acquire(aliasTable, 3, 'mse-stream-codecpar');
    assert.equal(tableLease.bytes, 12);
    expectCode(() => api.eioCodecparReplace(clone.dest, aliasTable, tableLease),
      'EIO_CODECPAR_PAYLOAD_OVERLAP');
    assert.deepEqual(memory.frees, [], 'the side table overlap is refused as well');
    assert.equal(tableLease.release(PROOF), false);

    assert.equal(memory.getU32(clone.dest + 12), pointers.extra, 'the old clone is unchanged');
    api.dn(clone.dest);
    assert.deepEqual(memory.frees,
      [pointers.extra, pointers.sides[0], pointers.sides[1], pointers.table, clone.dest]);
    assert.equal(real.manager.snapshot().releasedCount, 1);
  });

/* -------------------------------------------------------------------------
 * Channel-layout ownership modes.
 * ---------------------------------------------------------------------- */

test('a destination channel layout that owns a native map refuses before any mutation',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    const api = loadModule67669(memory);
    const real = makeRealManager(api, memory);
    const clone = leasedClone(memory, api, real, OLD_FIXTURE, 1);
    const pointers = readPayloadPointers(memory, clone.dest);
    const mapPointer = memory.allocate(48, 'calloc');
    memory.setI32(clone.dest + LAYOUT_OFFSET, LAYOUT_ORDER_CUSTOM);
    memory.setI32(clone.dest + LAYOUT_OFFSET + 4, 2);
    memory.setU32(clone.dest + LAYOUT_OFFSET + 8, mapPointer);

    const newSource = memory.allocate(STRUCT_BYTES, 'calloc');
    writeCodecparStruct(memory, newSource, NEW_FIXTURE);
    const newLease = real.manager.acquire(newSource, 2, 'mse-stream-codecpar');
    const allocsBefore = memory.allocs.length;

    expectCode(() => api.eioCodecparReplace(clone.dest, newSource, newLease),
      'EIO_CODECPAR_LAYOUT_UNSUPPORTED');
    assert.deepEqual(memory.frees, [], 'the custom layout map and every old payload stay allocated');
    assert.equal(memory.allocs.length, allocsBefore);
    assert.equal(memory.getI32(clone.dest + LAYOUT_OFFSET), LAYOUT_ORDER_CUSTOM);
    assert.equal(memory.getU32(clone.dest + LAYOUT_OFFSET + 8), mapPointer);
    assert.equal(memory.getU32(clone.dest + 12), pointers.extra);
    assert.equal(newLease.release(PROOF), false);
    assert.equal(real.manager.snapshot().releasedCount, 0);
  });

test('a CUSTOM source layout or an unknown layout order is refused before any mutation',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    const api = loadModule67669(memory);
    const real = makeRealManager(api, memory);
    const clone = leasedClone(memory, api, real, OLD_FIXTURE, 1);
    const pointers = readPayloadPointers(memory, clone.dest);

    /* The scanner is the acquisition boundary: an unsupported CUSTOM source
       must fail before a claim is reserved, not merely before replace copies. */
    const customSource = memory.allocate(STRUCT_BYTES, 'calloc');
    writeCodecparStruct(memory, customSource, {
      extra: [7, 8],
      sides: [],
      layout: { order: LAYOUT_ORDER_CUSTOM, nbChannels: 2, mask: memory.allocate(48, 'calloc') }
    });
    const allocsBeforeScan = memory.allocs.length;
    const freesBeforeScan = memory.frees.length;
    const heldBeforeScan = real.control.snapshot().heldBytes;
    expectCode(() => api.eioPayloadBytes(customSource), 'EIO_CODECPAR_LAYOUT_UNSUPPORTED');
    expectCode(() => real.manager.acquire(customSource, 2, 'mse-stream-codecpar'),
      'EIO_CODECPAR_LEASE_INVALID');
    assert.equal(memory.allocs.length, allocsBeforeScan,
      'the refused scanner performs no payload allocation');
    assert.equal(memory.frees.length, freesBeforeScan,
      'the refused scanner performs no native free');
    assert.equal(real.control.snapshot().heldBytes, heldBeforeScan,
      'the failed measurement reserves no replacement claim');

    const customReplaceSource = memory.allocate(STRUCT_BYTES, 'calloc');
    writeCodecparStruct(memory, customReplaceSource, NEW_FIXTURE);
    const customLease = real.manager.acquire(customReplaceSource, 2, 'mse-stream-codecpar');
    const allocsBefore = memory.allocs.length;
    memory.setI32(customReplaceSource + LAYOUT_OFFSET, LAYOUT_ORDER_CUSTOM);
    memory.setU32(customReplaceSource + LAYOUT_OFFSET + 8, 0);
    expectCode(() => api.eioCodecparReplace(clone.dest, customReplaceSource, customLease),
      'EIO_CODECPAR_LAYOUT_UNSUPPORTED');
    assert.equal(customLease.release(PROOF), false);
    assert.equal(memory.allocs.length, allocsBefore);

    const unknownReplaceSource = memory.allocate(STRUCT_BYTES, 'calloc');
    writeCodecparStruct(memory, unknownReplaceSource, NEW_FIXTURE);
    const unknownLease = real.manager.acquire(unknownReplaceSource, 3, 'mse-stream-codecpar');
    memory.setI32(unknownReplaceSource + LAYOUT_OFFSET, 7);
    expectCode(() => api.eioCodecparReplace(clone.dest, unknownReplaceSource, unknownLease),
      'EIO_CODECPAR_LAYOUT_UNSUPPORTED');
    assert.deepEqual(memory.frees, [], 'no old payload and no layout map is freed');
    assert.equal(unknownLease.release(PROOF), false);
    assert.equal(memory.getU32(clone.dest + 12), pointers.extra);
    assert.equal(real.manager.snapshot().releasedCount, 0);
  });

test('the scanner and leased Yi refuse unsupported layout ownership before measurement',
  { timeout: 5000 }, () => {
    for (const order of [LAYOUT_ORDER_CUSTOM, 7, -1]) {
      const memory = makeNativeMemory();
      const api = loadModule67669(memory);
      const source = memory.allocate(STRUCT_BYTES, 'calloc');
      writeCodecparStruct(memory, source, {
        extra: [1, 2],
        sides: [{ bytes: [3], type: 2 }],
        layout: {
          order,
          nbChannels: 2,
          mask: order === LAYOUT_ORDER_CUSTOM ? memory.allocate(48, 'calloc') : 0
        }
      });
      const allocsBefore = memory.allocs.length;
      const freesBefore = memory.frees.length;

      expectCode(() => api.eioPayloadBytes(source), 'EIO_CODECPAR_LAYOUT_UNSUPPORTED');
      assert.equal(memory.allocs.length, allocsBefore,
        'order ' + order + ' measurement allocates no payload');
      assert.equal(memory.frees.length, freesBefore,
        'order ' + order + ' measurement performs no native free');

      const real = makeRealManager(api, memory);
      const heldBefore = real.control.snapshot().heldBytes;
      expectCode(() => real.manager.acquire(source, 1, 'mse-stream-codecpar'),
        'EIO_CODECPAR_LEASE_INVALID');
      assert.equal(real.control.snapshot().heldBytes, heldBefore,
        'the native input manager fails before admission/charge');
      assert.equal(real.control.snapshot().countBound, 0);
      assert.equal(real.manager.snapshot().acquiredCount, 0,
        'no lease claim exists for an unsupported layout');

      const dest = memory.allocate(STRUCT_BYTES, 'calloc');
      let bindCalls = 0;
      let releaseCalls = 0;
      const fake = {
        bytes: 3,
        bind() { bindCalls += 1; return true; },
        release() { releaseCalls += 1; return true; },
        retainFailure() {}
      };
      const allocsBeforeYi = memory.allocs.length;
      expectCode(() => api.Yi(dest, source, fake), 'EIO_CODECPAR_LAYOUT_UNSUPPORTED');
      assert.equal(bindCalls, 0, 'leased Yi refuses before bind/copy');
      assert.equal(releaseCalls, 0, 'leased Yi refuses before any credit');
      assert.equal(memory.allocs.length, allocsBeforeYi, 'leased Yi allocates nothing');
      assert.deepEqual(memory.frees, [], 'leased Yi frees nothing');
      assert.equal(memory.getU32(dest + 12), 0, 'the refused destination stays empty');
    }
  });

test('UNSPEC, NATIVE and AMBISONIC layouts own no mapping and may be replaced',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    const api = loadModule67669(memory);
    const real = makeRealManager(api, memory);
    const clone = leasedClone(memory, api, real, {
      extra: [1, 2],
      sides: [],
      layout: { order: LAYOUT_ORDER_UNSPEC, nbChannels: 0, mask: 0 }
    }, 1);
    const newSource = memory.allocate(STRUCT_BYTES, 'calloc');
    writeCodecparStruct(memory, newSource, {
      extra: [3, 4, 5],
      sides: [],
      layout: { order: LAYOUT_ORDER_AMBISONIC, nbChannels: 4, mask: 0 }
    });
    const newLease = real.manager.acquire(newSource, 2, 'mse-stream-codecpar');

    assert.equal(api.eioCodecparReplace(clone.dest, newSource, newLease), undefined);
    assert.equal(memory.getI32(clone.dest + LAYOUT_OFFSET), LAYOUT_ORDER_AMBISONIC);
    assert.equal(memory.getI32(clone.dest + LAYOUT_OFFSET + 4), 4);
    api.dn(clone.dest);
    assert.deepEqual(
      [real.manager.snapshot().releasedCount, real.control.snapshot().heldBytes], [2, 0]);
  });

/* -------------------------------------------------------------------------
 * Free, release, copy and bind failures.
 * ---------------------------------------------------------------------- */

test('a partial old payload free retains the record and never retries or copies',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    const api = loadModule67669(memory);
    const real = makeRealManager(api, memory);
    const clone = leasedClone(memory, api, real, OLD_FIXTURE, 1);
    const pointers = readPayloadPointers(memory, clone.dest);
    const newSource = memory.allocate(STRUCT_BYTES, 'calloc');
    writeCodecparStruct(memory, newSource, NEW_FIXTURE);
    const newLease = real.manager.acquire(newSource, 2, 'mse-stream-codecpar');
    const allocsBefore = memory.allocs.length;

    memory.failFreeNumbers.add(2);
    expectCode(() => api.eioCodecparReplace(clone.dest, newSource, newLease),
      'EIO_CODECPAR_FREE_UNPROVEN');
    assert.deepEqual(memory.frees, [pointers.extra], 'the second native free threw');
    expectCode(() => api.dn(clone.dest), 'EIO_CODECPAR_FREE_UNPROVEN');
    assert.deepEqual(memory.frees, [pointers.extra],
      'the retained record never retries the free or frees the struct');
    assert.equal(memory.allocs.length, allocsBefore, 'no copy allocation follows a partial free');
    assert.deepEqual(
      [real.manager.snapshot().releasedCount, real.manager.snapshot().boundCount], [0, 1]);
    assert.equal(real.manager.snapshot().retainedCount, 2,
      'old and new claims are both retained');
    assert.equal(clone.lease.release(PROOF), false, 'the partially freed old lease is never credited');
    assert.equal(newLease.release(PROOF), false);
    assert.equal(real.control.snapshot().heldBytes, clone.lease.bytes + newLease.bytes);
  });

test('an exact false from a native payload-free API retains FREE_UNPROVEN before credit',
  { timeout: 5000 }, () => {
    for (const failure of ['aI', 'bl', 'NZ']) {
      const memory = makeNativeMemory();
      const api = loadModule67669(memory, { falseFree: failure });
      const real = makeRealManager(api, memory);
      const clone = leasedClone(memory, api, real, OLD_FIXTURE, 1);
      const pointers = readPayloadPointers(memory, clone.dest);
      const newSource = memory.allocate(STRUCT_BYTES, 'calloc');
      writeCodecparStruct(memory, newSource, NEW_FIXTURE);
      const newLease = real.manager.acquire(newSource, 2, 'mse-stream-codecpar');
      const allocsBefore = memory.allocs.length;

      expectCode(() => api.eioCodecparReplace(clone.dest, newSource, newLease),
        'EIO_CODECPAR_FREE_UNPROVEN');
      assert.equal(memory.allocs.length, allocsBefore,
        failure + ' refusal never starts the replacement copy');
      assert.equal(memory.frees.includes(clone.dest), false,
        failure + ' refusal never frees the destination struct');
      const freesAfterFailure = memory.frees.slice();
      expectCode(() => api.dn(clone.dest), 'EIO_CODECPAR_FREE_UNPROVEN');
      assert.deepEqual(memory.frees, freesAfterFailure,
        failure + ' retained record is never retried');
      assert.equal(clone.lease.release(PROOF), false);
      assert.equal(newLease.release(PROOF), false);
      assert.equal(real.manager.snapshot().releasedCount, 0);
      assert.equal(real.manager.snapshot().retainedCount, 2);
      assert.equal(real.control.snapshot().heldBytes, clone.lease.bytes + newLease.bytes);
      assert.equal(memory.getU32(clone.dest + 12), failure === 'aI' ? pointers.extra : 0,
        failure + ' only the already-attempted prefix may have been cleared');
    }
  });

function runReleaseFailure(override) {
  const memory = makeNativeMemory();
  const api = loadModule67669(memory);
  const real = makeRealManager(api, memory);
  const oldSource = memory.allocate(STRUCT_BYTES, 'calloc');
  writeCodecparStruct(memory, oldSource, OLD_FIXTURE);
  const oldReal = real.manager.acquire(oldSource, 1, 'mse-stream-codecpar');
  const oldLease = proxyLease(oldReal, override);
  const dest = memory.allocate(STRUCT_BYTES, 'calloc');
  api.Yi(dest, oldSource, oldLease);
  const pointers = readPayloadPointers(memory, dest);
  const newSource = memory.allocate(STRUCT_BYTES, 'calloc');
  writeCodecparStruct(memory, newSource, NEW_FIXTURE);
  const newLease = real.manager.acquire(newSource, 2, 'mse-stream-codecpar');
  const allocsBefore = memory.allocs.length;

  expectCode(() => api.eioCodecparReplace(dest, newSource, newLease),
    'EIO_CODECPAR_RELEASE_UNVERIFIED');
  assert.deepEqual(memory.frees,
    [pointers.extra, pointers.sides[0], pointers.sides[1], pointers.table],
    'every old payload is freed before the failed credit, and the struct is not');
  assert.equal(memory.allocs.length, allocsBefore, 'no copy happens after the failed credit');
  assert.equal(memory.getU32(dest + 12), 0, 'the old payload pointers are cleared');
  assert.equal(memory.getI32(dest + 24), 0);
  expectCode(() => api.dn(dest), 'EIO_CODECPAR_RELEASE_UNVERIFIED');
  assert.equal(memory.frees.length, 4, 'dn refuses the retained record without a second free');
  assert.equal(newLease.release(PROOF), false, 'the replacement claim is retained, not credited');
  assert.equal(oldReal.release(PROOF), false, 'the old claim is never retried or credited');
  assert.equal(real.manager.snapshot().releasedCount, 0);
  assert.equal(real.control.snapshot().heldBytes, oldReal.bytes + newLease.bytes);
}

test('a throwing, refusing or throwing-null old release retains both claims and never copies',
  { timeout: 5000 }, () => {
    runReleaseFailure({ release() { throw new Error('release exploded'); } });
    runReleaseFailure({ release() { return false; } });
    runReleaseFailure({ release() { throw null; } });
    runReleaseFailure({ release() { throw undefined; } });
    runReleaseFailure({ release() { return null; } });
  });

test('reentrant dn and replace during the old release cannot free, copy or credit',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    const api = loadModule67669(memory);
    const real = makeRealManager(api, memory);
    const oldSource = memory.allocate(STRUCT_BYTES, 'calloc');
    writeCodecparStruct(memory, oldSource, OLD_FIXTURE);
    const oldReal = real.manager.acquire(oldSource, 1, 'mse-stream-codecpar');
    let dnError = null;
    let replaceError = null;
    let otherLease = null;
    const oldLease = proxyLease(oldReal, {
      release(proof, lease) {
        try {
          api.dn(dest);
        } catch (error) {
          dnError = error;
        }
        try {
          api.eioCodecparReplace(dest, otherSource, otherLease);
        } catch (error) {
          replaceError = error;
        }
        return lease.release(proof);
      }
    });
    const dest = memory.allocate(STRUCT_BYTES, 'calloc');
    api.Yi(dest, oldSource, oldLease);
    const otherSource = memory.allocate(STRUCT_BYTES, 'calloc');
    writeCodecparStruct(memory, otherSource, { extra: [1, 2, 3], sides: [{ bytes: [4, 5], type: 2 }] });
    otherLease = real.manager.acquire(otherSource, 2, 'mse-stream-codecpar');
    const newSource = memory.allocate(STRUCT_BYTES, 'calloc');
    writeCodecparStruct(memory, newSource, NEW_FIXTURE);
    const newLease = real.manager.acquire(newSource, 3, 'mse-stream-codecpar');

    const pointers = readPayloadPointers(memory, dest);
    const freesBefore = memory.frees.length;
    assert.equal(api.eioCodecparReplace(dest, newSource, newLease), undefined);
    assert.ok(dnError);
    assert.equal(dnError.code, 'EIO_CODECPAR_REPLACE_IN_PROGRESS');
    assert.ok(replaceError);
    assert.equal(replaceError.code, 'EIO_CODECPAR_LEASE_DEST_BUSY');
    assert.deepEqual(memory.frees.slice(freesBefore),
      [pointers.extra, pointers.sides[0], pointers.sides[1], pointers.table],
      'neither nested call freed anything');
    assert.equal(otherLease.release(PROOF), false,
      'the nested replacement claim is retained and never credited');

    const copied = readPayloadPointers(memory, dest);
    api.dn(dest);
    assert.deepEqual(memory.frees.slice(freesBefore + 4),
      [copied.extra, copied.sides[0], copied.table, dest],
      'the successful outer replacement is the only later free/credit path');
    assert.deepEqual(
      [real.manager.snapshot().releasedCount, real.control.snapshot().heldBytes],
      [2, otherLease.bytes]);
  });

test('a replacement malloc failure retains the new record without dn or a fake free',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    const api = loadModule67669(memory);
    const real = makeRealManager(api, memory);
    const clone = leasedClone(memory, api, real, OLD_FIXTURE, 1);
    const pointers = readPayloadPointers(memory, clone.dest);
    const newSource = memory.allocate(STRUCT_BYTES, 'calloc');
    writeCodecparStruct(memory, newSource, NEW_FIXTURE);
    const newLease = real.manager.acquire(newSource, 2, 'mse-stream-codecpar');
    const allocsBefore = memory.allocs.length;
    memory.failedAllocNumbers.add(allocsBefore + 1);

    /* The copy helper's internal allocation code is deliberately not the
       replacement contract: every copy failure is exposed through one fixed,
       bounded public code and the underlying cause remains non-diagnostic. */
    const copyError = expectCode(() => api.eioCodecparReplace(clone.dest, newSource, newLease),
      'EIO_CODECPAR_CLONE_UNPROVEN');
    assert.equal(copyError.message, 'codecpar replacement copy unproven');
    assert.equal(memory.allocs[allocsBefore].failed, true);
    assert.equal(memory.getU32(clone.dest + 12), 0, 'the failed payload pointer is never published');
    assert.equal(memory.getI32(clone.dest + 16), 0);
    assert.deepEqual(memory.frees,
      [pointers.extra, pointers.sides[0], pointers.sides[1], pointers.table],
      'only the old payloads were freed');
    expectCode(() => api.dn(clone.dest), 'EIO_CODECPAR_CLONE_UNPROVEN');
    assert.equal(memory.frees.includes(clone.dest), false, 'the partial struct is never freed');
    assert.deepEqual(
      [real.manager.snapshot().releasedCount, real.manager.snapshot().boundCount], [1, 1]);
    assert.equal(newLease.release(PROOF), false);
    assert.equal(real.control.snapshot().heldBytes, newLease.bytes);
  });

test('a refusing bind retains the new record and never retries, resets or frees',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    const api = loadModule67669(memory);
    const real = makeRealManager(api, memory);
    const clone = leasedClone(memory, api, real, OLD_FIXTURE, 1);
    const newSource = memory.allocate(STRUCT_BYTES, 'calloc');
    writeCodecparStruct(memory, newSource, NEW_FIXTURE);
    const realLease = real.manager.acquire(newSource, 2, 'mse-stream-codecpar');
    const newLease = proxyLease(realLease, { bind() { return false; } });

    expectCode(() => api.eioCodecparReplace(clone.dest, newSource, newLease),
      'EIO_CODECPAR_LEASE_BIND_FAILED');
    const copied = readPayloadPointers(memory, clone.dest);
    assert.ok(copied.extra > 0, 'the copy completed before the refused bind');
    const allocsAfterCopy = memory.allocs.length;
    const freesAfterCopy = memory.frees.length;
    expectCode(() => api.dn(clone.dest), 'EIO_CODECPAR_CLONE_UNPROVEN');
    expectCode(() => api.dn(clone.dest), 'EIO_CODECPAR_CLONE_UNPROVEN');
    assert.equal(memory.frees.length, freesAfterCopy, 'the retained record is never freed');
    assert.equal(memory.allocs.length, allocsAfterCopy, 'there is no second copy attempt');
    assert.equal(realLease.release(PROOF), false, 'the unbound claim is never credited');
    assert.deepEqual(
      [real.manager.snapshot().releasedCount, real.manager.snapshot().retainedCount], [1, 1]);
    assert.equal(real.control.snapshot().heldBytes, realLease.bytes);
  });

test('a throwing bind retains the new record without freeing or crediting',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    const api = loadModule67669(memory);
    const real = makeRealManager(api, memory);
    const clone = leasedClone(memory, api, real, OLD_FIXTURE, 1);
    const newSource = memory.allocate(STRUCT_BYTES, 'calloc');
    writeCodecparStruct(memory, newSource, NEW_FIXTURE);
    const realLease = real.manager.acquire(newSource, 2, 'mse-stream-codecpar');
    const newLease = proxyLease(realLease, { bind() { throw null; } });

    expectCode(() => api.eioCodecparReplace(clone.dest, newSource, newLease),
      'EIO_CODECPAR_CLONE_UNPROVEN');
    expectCode(() => api.dn(clone.dest), 'EIO_CODECPAR_CLONE_UNPROVEN');
    assert.equal(memory.frees.includes(clone.dest), false);
    assert.equal(realLease.release(PROOF), false);
    assert.equal(real.manager.snapshot().releasedCount, 1);
  });

test('a reentrant dn during the replacement bind sees the copying record only',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    const api = loadModule67669(memory);
    const real = makeRealManager(api, memory);
    const clone = leasedClone(memory, api, real, OLD_FIXTURE, 1);
    const newSource = memory.allocate(STRUCT_BYTES, 'calloc');
    writeCodecparStruct(memory, newSource, NEW_FIXTURE);
    const realLease = real.manager.acquire(newSource, 2, 'mse-stream-codecpar');
    let reentrantError = null;
    const newLease = proxyLease(realLease, {
      bind(summary, lease) {
        try {
          api.dn(clone.dest);
        } catch (error) {
          reentrantError = error;
        }
        return lease.bind(summary);
      }
    });

    const pointers = readPayloadPointers(memory, clone.dest);
    assert.equal(api.eioCodecparReplace(clone.dest, newSource, newLease), undefined);
    assert.ok(reentrantError);
    assert.equal(reentrantError.code, 'EIO_CODECPAR_CLONE_UNPROVEN');
    assert.deepEqual(memory.frees,
      [pointers.extra, pointers.sides[0], pointers.sides[1], pointers.table],
      'the nested dn freed nothing while the replacement was copying');
    const copied = readPayloadPointers(memory, clone.dest);
    api.dn(clone.dest);
    assert.deepEqual(memory.frees.slice(4), [copied.extra, copied.sides[0], copied.table, clone.dest]);
    assert.deepEqual(
      [real.manager.snapshot().releasedCount, real.control.snapshot().heldBytes], [2, 0]);
  });

/* -------------------------------------------------------------------------
 * Untrusted source mutation across the old-release callback.
 * ---------------------------------------------------------------------- */

test('a source changed during the old release is revalidated and retained, not copied',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    const api = loadModule67669(memory);
    const real = makeRealManager(api, memory);
    const oldSource = memory.allocate(STRUCT_BYTES, 'calloc');
    writeCodecparStruct(memory, oldSource, OLD_FIXTURE);
    const oldReal = real.manager.acquire(oldSource, 1, 'mse-stream-codecpar');
    let newSource = null;
    let mutations = 0;
    const oldLease = proxyLease(oldReal, {
      release(proof, lease) {
        const replacement = memory.allocate(5, 'malloc');
        memory.buffer.set([7, 7, 7, 7, 7], replacement);
        memory.setU32(newSource + 12, replacement);
        memory.setI32(newSource + 16, 5);
        mutations += 1;
        return lease.release(proof);
      }
    });
    const dest = memory.allocate(STRUCT_BYTES, 'calloc');
    api.Yi(dest, oldSource, oldLease);
    const pointers = readPayloadPointers(memory, dest);
    newSource = memory.allocate(STRUCT_BYTES, 'calloc');
    writeCodecparStruct(memory, newSource, NEW_FIXTURE);
    const newLease = real.manager.acquire(newSource, 2, 'mse-stream-codecpar');

    expectCode(() => api.eioCodecparReplace(dest, newSource, newLease),
      'EIO_CODECPAR_SOURCE_CHANGED');
    assert.equal(mutations, 1);
    assert.deepEqual(memory.frees,
      [pointers.extra, pointers.sides[0], pointers.sides[1], pointers.table],
      'the old payloads stay freed; the new copy never ran');
    assert.equal(real.manager.snapshot().releasedCount, 1, 'the old claim was credited once');
    assert.equal(memory.getU32(dest + 12), 0, 'the destination holds no replacement payload');
    assert.equal(memory.getI32(dest + 24), 0);
    assert.equal(memory.frees.includes(dest), false, 'the destination struct is not freed');

    expectCode(() => api.dn(dest), 'EIO_CODECPAR_SOURCE_CHANGED');
    assert.equal(memory.frees.length, 4, 'dn refuses the retained replacement record');
    assert.equal(newLease.release(PROOF), false);
    assert.equal(real.manager.snapshot().retainedCount, 1);
    assert.equal(real.control.snapshot().heldBytes, newLease.bytes);
  });

test('a source layout ownership flip during the old release is refused before copy',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    const api = loadModule67669(memory);
    const real = makeRealManager(api, memory);
    const oldSource = memory.allocate(STRUCT_BYTES, 'calloc');
    writeCodecparStruct(memory, oldSource, OLD_FIXTURE);
    const oldReal = real.manager.acquire(oldSource, 1, 'mse-stream-codecpar');
    let newSource = null;
    const oldLease = proxyLease(oldReal, {
      release(proof, lease) {
        memory.setI32(newSource + LAYOUT_OFFSET, LAYOUT_ORDER_CUSTOM);
        memory.setU32(newSource + LAYOUT_OFFSET + 8, 0);
        return lease.release(proof);
      }
    });
    const dest = memory.allocate(STRUCT_BYTES, 'calloc');
    api.Yi(dest, oldSource, oldLease);
    const pointers = readPayloadPointers(memory, dest);
    newSource = memory.allocate(STRUCT_BYTES, 'calloc');
    writeCodecparStruct(memory, newSource, NEW_FIXTURE);
    const newLease = real.manager.acquire(newSource, 2, 'mse-stream-codecpar');

    expectCode(() => api.eioCodecparReplace(dest, newSource, newLease),
      'EIO_CODECPAR_SOURCE_CHANGED');
    assert.deepEqual(memory.frees,
      [pointers.extra, pointers.sides[0], pointers.sides[1], pointers.table],
      'old payloads were already freed, but the unproven layout is never copied');
    assert.equal(memory.getU32(dest + 12), 0, 'no replacement payload was published');
    assert.equal(memory.getI32(dest + 24), 0);
    assert.equal(memory.frees.includes(dest), false);
    expectCode(() => api.dn(dest), 'EIO_CODECPAR_SOURCE_CHANGED');
    assert.equal(newLease.release(PROOF), false);
    assert.equal(real.manager.snapshot().retainedCount, 1);
    assert.equal(real.control.snapshot().heldBytes, newLease.bytes);
  });

test('a source side-table moved onto an old freed table during release is refused',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    const api = loadModule67669(memory);
    const real = makeRealManager(api, memory);
    const oldSource = memory.allocate(STRUCT_BYTES, 'calloc');
    writeCodecparStruct(memory, oldSource, OLD_FIXTURE);
    const oldReal = real.manager.acquire(oldSource, 1, 'mse-stream-codecpar');
    let newSource = null;
    let newPointers = null;
    let oldTable = 0;
    const oldLease = proxyLease(oldReal, {
      release(proof, lease) {
        /* Reuse the freed old table as the source table and rewrite its entries
           so the scan payloads still match; only the table pointer changed. */
        for (let index = 0; index < newPointers.count; index += 1) {
          const oldEntry = oldTable + 12 * index;
          const newEntry = newPointers.table + 12 * index;
          memory.setU32(oldEntry, memory.getU32(newEntry));
          memory.setU32(oldEntry + 4, memory.getU32(newEntry + 4));
          memory.setI32(oldEntry + 8, memory.getI32(newEntry + 8));
        }
        memory.setU32(newSource + 20, oldTable);
        return lease.release(proof);
      }
    });
    const dest = memory.allocate(STRUCT_BYTES, 'calloc');
    api.Yi(dest, oldSource, oldLease);
    const pointers = readPayloadPointers(memory, dest);
    oldTable = pointers.table;
    newSource = memory.allocate(STRUCT_BYTES, 'calloc');
    writeCodecparStruct(memory, newSource, {
      extra: [7, 8, 9, 10],
      sides: [{ bytes: [11, 12], type: 2 }, { bytes: [13], type: 3 }]
    });
    newPointers = readPayloadPointers(memory, newSource);
    const newLease = real.manager.acquire(newSource, 2, 'mse-stream-codecpar');

    expectCode(() => api.eioCodecparReplace(dest, newSource, newLease),
      'EIO_CODECPAR_SOURCE_CHANGED');
    assert.deepEqual(memory.frees,
      [pointers.extra, pointers.sides[0], pointers.sides[1], pointers.table],
      'the stale table alias is refused after the old payloads were freed');
    assert.equal(memory.getU32(dest + 12), 0, 'no copy ran from the old freed table');
    assert.equal(memory.getI32(dest + 24), 0);
    assert.equal(memory.frees.includes(dest), false);
    expectCode(() => api.dn(dest), 'EIO_CODECPAR_SOURCE_CHANGED');
    assert.equal(newLease.release(PROOF), false);
    assert.equal(real.manager.snapshot().retainedCount, 1);
    assert.equal(real.control.snapshot().heldBytes, newLease.bytes);
  });

test('a lease bytes getter that reenters dn leaves no stale record to mutate',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    const api = loadModule67669(memory);
    const real = makeRealManager(api, memory);
    const clone = leasedClone(memory, api, real, OLD_FIXTURE, 1);
    const pointers = readPayloadPointers(memory, clone.dest);
    const newSource = memory.allocate(STRUCT_BYTES, 'calloc');
    writeCodecparStruct(memory, newSource, NEW_FIXTURE);
    const realLease = real.manager.acquire(newSource, 2, 'mse-stream-codecpar');
    let reads = 0;
    const hostileLease = {
      get bytes() {
        reads += 1;
        if (reads === 1) {
          try {
            api.dn(clone.dest);
          } catch (error) {
            assert.fail('the bound record must settle through dn: ' + error);
          }
        }
        return realLease.bytes;
      },
      bind(summary) { return realLease.bind(summary); },
      release(proof) { return realLease.release(proof); },
      retainFailure(error) { return realLease.retainFailure(error); }
    };

    expectCode(() => api.eioCodecparReplace(clone.dest, newSource, hostileLease),
      'EIO_CODECPAR_LEASE_DEST_UNBOUND');
    assert.deepEqual(memory.frees,
      [pointers.extra, pointers.sides[0], pointers.sides[1], pointers.table, clone.dest],
      'the reentrant dn behaved exactly like the ordinary dn path');
    assert.equal(real.manager.snapshot().releasedCount, 1);
    assert.equal(memory.getU32(clone.dest + 12), 0);
    assert.equal(realLease.release(PROOF), false, 'the refused claim is retained');
    assert.equal(real.control.snapshot().heldBytes, realLease.bytes);
  });
