'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const root = path.join(__dirname, '..');
const chunk = fs.readFileSync(path.join(root, 'public/vendor/web/libmedia/943.avplayer.js'), 'utf8');
const main = fs.readFileSync(path.join(root, 'public/vendor/web/libmedia/avplayer.js'), 'utf8');
const ASYNC = { timeout: 2000 };
// These tests run the actual header and local helper plus actual AVPacket
// factory 46564. IO, malloc, and AVBuffer 13486 are synthetic. Shared lifetime
// here models refcounts, not WASM atomics or generated-Worker execution.
function method(head) {
  const start = chunk.indexOf(head);
  assert.ok(start >= 0, head);
  let depth = 0;
  for (let i = chunk.indexOf('{', start); i < chunk.length; i++) {
    if (chunk[i] === '{') depth++;
    if (chunk[i] === '}' && --depth === 0) return chunk.slice(start, i + 1);
  }
  throw Error('unbalanced method');
}
function packetFactory() {
  const start = main.indexOf('46564:(e,t,i)=>{');
  const end = main.indexOf('},46626:', start);
  return vm.runInNewContext('({' + main.slice(start, end + 1) + '})')[46564];
}
function ledgerHelper(mem) {
  const begin = '/*__EIO_DUAL_CACHE_DEMUX_BEGIN__*/';
  const end = '/*__EIO_DUAL_CACHE_DEMUX_END__*/';
  const text = main.slice(main.indexOf(begin) + begin.length, main.indexOf(end));
  return vm.runInNewContext(text + ';__eioDualDemux', {
    o: { f: { 15: p => mem.getUint32(p, true) } },
    Map, Math, Number, isFinite, performance: { now: () => 0 }, setTimeout, clearTimeout
  });
}
function harness(kind, options = {}) {
  const heap = new ArrayBuffer(65536), mem = new DataView(heap), bytes = new Uint8Array(heap);
  const events = [], live = new Set(), refs = new Map(), bodies = new Map();
  let bump = 4096, nextRef = 512, nextBody = 2048, readingPicture = false;
  const reads = { 15: p => mem.getInt32(p, true), 20: p => mem.getUint32(p, true),
    17: p => mem.getBigInt64(p, true), 25: p => mem.getUint32(p, true) };
  const writes = { 2: (p, v) => mem.setUint8(p, v), 15: (p, v) => mem.setInt32(p, v, true), 20: (p, v) => {
      if (p === 152 && v && options.nxPartial) throw Error('partial NX after buffer install');
      mem.setUint32(p, v, true);
    },
    17: (p, v) => mem.setBigInt64(p, v, true), 25: (p, v) => mem.setUint32(p, v, true) };
  const alloc = n => {
    events.push('malloc');
    if (options.mallocThrows) throw Error('malloc');
    if (options.mallocZero) return 0;
    const p = bump; bump += n + 128; live.add(p); return p;
  };
  const free = p => { assert.ok(live.delete(p), 'actual data freed once'); events.push('free'); };
  const memory = {
    JW: (p, n) => bytes.subarray(p, p + n),
    lW: (p, n, src) => { events.push('copy'); bytes.set(src.subarray(0, n), p); },
    sO: (p, value, n) => bytes.fill(value, p, p + n)
  };
  let bound = null, sharedHeld = 0, sharedState = null;
  const controller = {
    reserve(n) { sharedHeld += n; sharedState = 'RESERVED'; return { ok: true, token: { id: 1, bytes: n } }; },
    bind(token, payload) {
      events.push('bind');
      if (options.bindFails) return { ok: false, code: 'fixture-failure' };
      assert.equal(reads[20](payload.buffer), payload.data);
      bound = payload.buffer; sharedState = 'BOUND'; return { ok: true };
    },
    reclaimUnused(token) { assert.equal(sharedState, 'RESERVED'); sharedHeld -= token.bytes; sharedState = 'FREE'; return { ok: true, reclaimedBytes: token.bytes }; },
    inspectReservation() { return { ok: true, retired: sharedState === 'FREE' }; },
    snapshot() { return { capacityBytes: 1000, heldBytes: sharedHeld }; }
  };
  const buffer = {
    aw(data) {
      events.push('nx');
      if (options.nxThrows) throw Error('NX');
      const body = nextBody; nextBody += 32;
      const ref = nextRef; nextRef += 16;
      writes[20](ref, body); writes[20](body, data);
      refs.set(ref, body); bodies.set(body, { data, count: 1 }); return ref;
    },
    VG(ref) {
      const body = refs.get(ref), copy = nextRef; nextRef += 16;
      refs.set(copy, body); writes[20](copy, body); bodies.get(body).count++; return copy;
    },
    us() {},
    Q7(pointer) {
      if (!reads[20](pointer)) return;
      if (options.cleanupThrows) throw Error('unref failed');
      const ref = reads[20](pointer), body = refs.get(ref), record = bodies.get(body);
      assert.ok(record); refs.delete(ref); writes[20](pointer, 0);
      if (--record.count === 0) {
        free(record.data); bodies.delete(body);
        if (body === bound) { sharedHeld = 0; sharedState = 'FREE'; events.push('final-free'); }
      }
    }
  };
  const exports = {};
  const deps = { 80209: memory, 56238: { f: reads }, 6927: { M: writes },
    46626: { sY: alloc, Eb: free }, 29688: { Dh: 0x7fffffffffffffffn, N_: -1, SF: 1 }, 13486: buffer };
  const req = id => { assert.ok(deps[id], `unexpected packet dependency ${id}`); return deps[id]; };
  req.d = (target, getters) => { for (const [key, get] of Object.entries(getters)) Object.defineProperty(target, key, { get, enumerable: true }); };
  packetFactory()({}, exports, req);
  // Packet structs are supplied separately so only picture-data mallocs are
  // counted. NX/Up/rN remain the real packet factory implementation.
  const packets = { ...exports, _5: () => 128 };
  const picture = Uint8Array.from([1, 2, 3, 4]);
  const stream = { index: 0, attachedPic: kind === 'cover' ? 0 : undefined,
    disposition: 0, codecpar: { codecType: 0, codecId: 27 }, timeBase: { num: 1, den: 1000 },
    privData: { trackId: 1, samplesIndex: [{ pos: 100n, size: 4, pts: 7n, dts: 6n, flags: 1 }] } };
  let position = 0n;
  const fc = { streams: kind === 'cover' ? [] : [stream], metadata: {}, chapters: [],
    createStream() { this.streams.push(stream); return stream; },
    ioReader: {
      flags: 1, error: -1048576,
      fileSize: async () => 200n, getPos: () => position,
      readUint32: async () => 8, skip: async () => {},
      seek: async p => { position = p; readingPicture = p === 100n; },
      readBuffer: async (size, target) => {
        assert.ok(readingPicture); assert.equal(size, 4);
        assert.ok(target instanceof Uint8Array, 'chapter uses existing native target, no JS copy');
        events.push('read'); if (options.readThrows) throw Error('IO');
        if (options.readWait) await options.readWait;
        target.set(picture); return target;
      }
    } };
  const helper = ledgerHelper(mem);
  const ledger = helper.make({ demuxBytes: 1000, maxPacketBytes: 100 });
  Object.assign(ledger, { phase: 1, packetBytes: 1000, probeBytes: 1000 });
  const task = { eioLedger: ledger, eioSharedController: controller };
  let token;
  if (!options.baseline) {
    fc.eioBeforeSampleAlloc = async info => {
      events.push('before'); if (options.wait) await options.wait;
      if (options.refuse) return null;
      token = helper.before(task, info); return token;
    };
    fc.eioAfterSampleAlloc = (t, info) => { helper.after(task, t, info); if (options.afterThrows) throw Error('after bind'); };
    fc.eioReleaseSampleAlloc = (t, info) => { events.push('release'); return helper.release(task, t, info); };
    fc.eioIsSampleAllocCancelled = t => options.cancel || helper.isCancelled(task, t);
  }
  const scope = { g: packets, h: deps[46626], m: memory, s: { f: reads }, r: { M: writes },
    n: { A: () => -1 }, u: { LR: -2 }, z: { Dh: 0x7fffffffffffffffn },
    I: { z3() {} }, b: 'fixture', p: { A: () => ({}) } };
  const Class = vm.runInNewContext('(class {' + method('async readHeader(e){') + '\n'
    + method('async eioReadHeaderPicture(') + '})', scope);
  const self = new Class();
  self.context = { foundMoov: true, foundMdat: true, fragment: false,
    ...(kind === 'cover' ? { covr: { type: 13, data: picture } } : { chapterTrack: [1] }) };
  self.options = {};
  return { fc, stream, events, live, mem, packets, ledger, controller,
    get held() { return sharedHeld; }, get token() { return token; },
    run: () => self.readHeader(fc),
    clone() { packets.rN(256, stream.attachedPic); },
    free() { packets.Up(stream.attachedPic); }, freeClone() { packets.Up(256); } };
}

for (const kind of ['cover', 'chapter']) {
  test(`${kind}: native header baseline remains unhooked`, ASYNC, async () => {
    const h = harness(kind, { baseline: true }); assert.equal(await h.run(), 0);
    assert.deepEqual(h.events, kind === 'cover' ? ['malloc', 'copy', 'nx'] : ['malloc', 'read', 'nx']);
    h.free(); assert.equal(h.live.size, 0);
  });
  test(`${kind}: refusal is non-EOF and precedes malloc`, ASYNC, async () => {
    const h = harness(kind, { refuse: true }); assert.equal(await h.run(), -2);
    assert.deepEqual(h.events, ['before']);
  });
  test(`${kind}: cancellation while awaiting admission prevents malloc`, ASYNC, async () => {
    let resolve; const options = { wait: new Promise(r => { resolve = r; }) };
    const h = harness(kind, options), result = h.run();
    await new Promise(r => setImmediate(r)); assert.deepEqual(h.events, ['before']);
    options.cancel = true; resolve(); assert.equal(await result, -2);
    assert.deepEqual(h.events, ['before', 'release']); assert.equal(h.held, 0);
  });
  test(`${kind}: actual packet bind and last-reference ownership`, ASYNC, async () => {
    const h = harness(kind); assert.equal(await h.run(), 0);
    assert.equal(h.token.state, 'HANDED'); assert.equal(h.token.sharedBound, true);
    assert.equal(h.fc.eioHeaderPictureBindings, 1, 'diagnostic follows successful bind hook');
    assert.equal(h.held, 4); h.clone(); h.free(); assert.equal(h.held, 4);
    h.freeClone(); assert.equal(h.held, 0); assert.equal(h.live.size, 0);
    assert.equal(h.events.filter(e => e === 'release').length, 0);
    assert.equal(h.events.filter(e => e === 'final-free').length, 1);
  });
  // Both call sites use the same cleanup helper; exercise failures once.
  if (kind === 'cover') for (const failure of ['mallocZero', 'mallocThrows', 'nxThrows', 'nxPartial', 'bindFails', 'afterThrows']) {
    test(`${kind}: ${failure} cleanup without invented credit`, ASYNC, async () => {
      const h = harness(kind, { [failure]: true }); assert.equal(await h.run(), -2);
      assert.equal(h.fc.eioHeaderPictureBindings, undefined, 'failed hook is not a binding diagnostic');
      assert.equal(h.live.size, 0);
      if (failure === 'nxThrows' || failure === 'nxPartial' || failure === 'bindFails') {
        assert.equal(h.held, 4, 'ambiguous ownership conservatively retained');
        assert.ok(!h.events.includes('release'));
      } else {
        assert.equal(h.held, 0);
        if (failure === 'afterThrows') {
          assert.equal(h.token.state, 'HANDED'); assert.ok(!h.events.includes('release'));
        }
      }
    });
  }
}
test('chapter: read failure frees native allocation before reclaiming unused reservation', ASYNC, async () => {
  const h = harness('chapter', { readThrows: true }); assert.equal(await h.run(), -2);
  assert.equal(h.live.size, 0); assert.equal(h.held, 0);
  assert.ok(h.events.indexOf('free') < h.events.indexOf('release'));
});
test('chapter: generation cancellation during IO frees raw data before release, never binds', ASYNC, async () => {
  let resolve; const options = { readWait: new Promise(r => { resolve = r; }) };
  const h = harness('chapter', options), result = h.run();
  await new Promise(r => setImmediate(r)); assert.ok(h.events.includes('read'));
  options.cancel = true; resolve(); assert.equal(await result, -2);
  assert.equal(h.live.size, 0); assert.equal(h.held, 0);
  assert.ok(!h.events.includes('nx')); assert.ok(!h.events.includes('bind'));
  assert.ok(h.events.indexOf('free') < h.events.indexOf('release'));
});
test('baseline IO failure keeps readHeader error semantics after cleanup', ASYNC, async () => {
  const h = harness('chapter', { baseline: true, readThrows: true });
  assert.equal(await h.run(), -1048576); assert.equal(h.live.size, 0);
});
test('unproven unref cleanup retains allocation and reservation until later cleanup', ASYNC, async () => {
  const options = { bindFails: true, cleanupThrows: true };
  const h = harness('cover', options); assert.equal(await h.run(), -2);
  assert.equal(h.live.size, 1); assert.equal(h.held, 4); assert.ok(!h.events.includes('release'));
  options.cleanupThrows = false; h.free(); assert.equal(h.live.size, 0);
  assert.equal(h.held, 4, 'failed bind never invents retirement credit');
});
test('incomplete hooks fail closed before native allocation', ASYNC, async () => {
  const h = harness('cover'); delete h.fc.eioAfterSampleAlloc;
  assert.equal(await h.run(), -2); assert.deepEqual(h.events, []);
});
