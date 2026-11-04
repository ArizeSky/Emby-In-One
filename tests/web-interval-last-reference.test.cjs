'use strict';

// MP4/MKV interval packets share this NX -> rN -> Qe lifetime path.
// Execute the shipped factories, not the old direct-free test or a mock Qe.
// Only wasm malloc/free, memory access and atomics are fixture plumbing. This
// proves the common interval packet ownership chain, not container parsing,
// browser/Worker loading, real wasm malloc, playback or a release/ACK gate.
const test = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const graph = require('./web-libmedia-source-graph.cjs')();

if (!globalThis.crypto || !globalThis.crypto.getRandomValues) {
  Object.defineProperty(globalThis, 'crypto', {
    value: require('node:crypto').webcrypto, configurable: true
  });
}

function vendorFactory(id, nextId) {
  const signature = `${id}:(e,t,i)=>{`;
  const start = graph.main.indexOf(signature);
  const end = graph.main.indexOf(`},${nextId}:`, start);
  assert.ok(start >= 0 && end > start, `missing shipped factory ${id}`);
  return graph.main.slice(start, end + 1);
}

const factories = new Function('return ({' + [
  vendorFactory(13486, 14077), vendorFactory(46564, 46626),
  graph.factory(90187), graph.factory(90190), graph.factory(90188)
].join(',') + '})')();

function harness() {
  const memory = new WebAssembly.Memory({ initial: 2, maximum: 2, shared: true });
  const words = new Int32Array(memory.buffer);
  const bytes = new Uint8Array(memory.buffer);
  const view = new DataView(memory.buffer);
  const live = new Set();
  const freed = [];
  let bump = 4096;
  let onFree = () => {};
  function allocate(size) {
    const pointer = bump;
    bump += Math.max(8, (Number(size) + 7) & ~7);
    assert.ok(bump <= bytes.length, 'bounded fixture heap');
    bytes.fill(0, pointer, bump);
    live.add(pointer);
    return pointer;
  }
  function free(pointer) {
    assert.ok(live.delete(pointer), `allocator must free live pointer once: ${pointer}`);
    onFree(pointer);
    freed.push(pointer);
  }
  const load = pointer => Atomics.load(words, pointer >>> 2);
  const store = (pointer, value) => Atomics.store(words, pointer >>> 2, Number(value));
  const reads = {
    15: load, 20: pointer => load(pointer) >>> 0,
    17: pointer => view.getBigInt64(pointer, true),
    25: pointer => load(pointer) >>> 0
  };
  const writes = {
    15: store, 20: store,
    17: (pointer, value) => view.setBigInt64(pointer, value, true),
    // libmedia type 25 is the wasm32 size_t, not an eight-byte timestamp.
    25: store
  };
  function field(pointer, bits, shift) {
    return (load(pointer) >>> shift) & ((1 << bits) - 1);
  }
  function changeField(pointer, value, bits, shift, add) {
    const mask = ((1 << bits) - 1) << shift;
    for (;;) {
      const current = load(pointer);
      const previous = (current & mask) >>> shift;
      const next = ((add ? previous + value : value) << shift) & mask;
      if (Atomics.compareExchange(words, pointer >>> 2, current,
        (current & ~mask) | next) === current) return previous;
    }
  }
  const allocator = {
    Gy: allocate, sY: allocate, Eb: free,
    aI(pointer) {
      const value = load(pointer);
      if (value) free(value);
      store(pointer, 0);
    }
  };
  const dependencies = {
    11286: { ce: memory },
    46626: allocator,
    80209: {
      Mr: (destination, source, length) => bytes.copyWithin(destination, source, source + length),
      sO: (pointer, value, length) => bytes.fill(value, pointer, pointer + length)
    },
    56238: { f: reads }, 6927: { M: writes },
    33817: {
      M_: (p, v, b, s) => changeField(p, v, b, s, false),
      WQ: (p, v, b, s) => changeField(p, v, b, s, true),
      jb: (p, v, b, s) => changeField(p, -v, b, s, true),
      Hh: field
    },
    // The tested path is direct/non-pool; entering pool or realloc is an error.
    62560: {}, 90582: {},
    29688: { N_: -1, Dh: -9223372036854775808n, SF: 1 }
  };
  const cache = new Map();
  function requireModule(id) {
    if (cache.has(id)) return cache.get(id).exports;
    if (Object.hasOwn(dependencies, id)) return dependencies[id];
    assert.equal(typeof factories[id], 'function', `unexpected dependency ${id}`);
    const module = { exports: {} };
    cache.set(id, module);
    factories[id](module, module.exports, requireModule);
    return module.exports;
  }
  requireModule.d = (target, getters) => {
    for (const [key, get] of Object.entries(getters)) {
      Object.defineProperty(target, key, { get, enumerable: true });
    }
  };
  return {
    memory, allocate, load, field, bytes, requireModule,
    freeCount: pointer => freed.filter(value => value === pointer).length,
    observeFree(callback) { onFree = callback; }
  };
}

function demuxHelper(h) {
  const begin = '/*__EIO_DUAL_CACHE_DEMUX_BEGIN__*/';
  const end = '/*__EIO_DUAL_CACHE_DEMUX_END__*/';
  const start = graph.main.indexOf(begin);
  const stop = graph.main.indexOf(end, start + begin.length);
  assert.ok(start >= 0 && stop > start, 'missing shipped demux helper');
  return vm.runInNewContext(graph.main.slice(start + begin.length, stop) + ';__eioDualDemux', {
    o: { f: { 15: h.load } }, Map, Math, Number, isFinite,
    performance: { now: () => 0 }, setTimeout, clearTimeout
  }, { timeout: 1000 });
}

test('interval NX -> real bind -> rN -> Qe(source) retains charge -> Qe(target) frees/credits once',
  { timeout: 5000 }, () => {
    const h = harness();
    const api = h.requireModule(90187);
    const packets = h.requireModule(46564);
    const descriptor = api.createDomainDescriptor({
      capacityBytes: 4096, maxEntries: 8, domainId: 'interval-last-reference'
    });
    assert.equal(descriptor.wasmMemory, h.memory, 'owned descriptor uses the actual shared wasm heap');
    const controller = api.registerDomain(descriptor, 0);
    const ownership = () => api.getReleaseRegistrySnapshot().domains[0].ownership;
    const demux = demuxHelper(h);
    const ledger = demux.make({ demuxBytes: 4096, maxPacketBytes: 1024 });
    Object.assign(ledger, { phase: 1, packetBytes: 4096, probeBytes: 4096 });
    const task = { eioLedger: ledger, eioSharedController: controller,
      formatContext: { interval: { packetBuffer: [] } } };
    const size = 256;
    const source = packets._5();
    const target = packets._5();
    const token = demux.before(task, { size, packet: source });
    assert.ok(token && token.sharedToken, 'real demux reserves before payload allocation');
    assert.equal(ownership().heldBytes, size);
    const data = h.allocate(size + 64); // NX requires libmedia's padding.
    h.bytes.fill(0x5a, data, data + size);
    packets.NX(source, data, size);
    const sourceRef = h.load(source);
    const body = h.load(sourceRef);
    assert.ok(sourceRef > 0 && body > 0);
    assert.equal(h.load(source + 24), data);
    assert.equal(h.load(sourceRef + 4), data);
    assert.equal(h.load(body), data);
    assert.equal(h.load(body + 16), 0, 'direct, non-pool AVBuffer');
    assert.equal(h.field(body + 8, 9, 2), 1);

    // after() derives the actual body via packet -> ref -> body and calls the
    // real domain controller.bind; no simulated body or fake controller.
    demux.after(task, token, { packet: source, data, size });
    assert.equal(token.sharedBound, true);
    assert.equal(controller.lookup({ data, buffer: body }).state, 'bound');
    assert.equal(ownership().countBound, 1);
    task.formatContext.interval.packetBuffer.push(source);

    packets.rN(target, source);
    const targetRef = h.load(target);
    assert.notEqual(targetRef, sourceRef);
    assert.equal(h.load(targetRef), body);
    assert.equal(h.load(target + 24), data);
    assert.equal(h.load(target + 28), size);
    assert.equal(h.field(body + 8, 9, 2), 2);
    assert.equal(ownership().heldBytes, size, 'rN must not reserve/charge twice');
    assert.equal(ledger.held, size);

    packets.Qe(task.formatContext.interval.packetBuffer.shift());
    assert.equal(h.freeCount(source), 1);
    assert.equal(h.freeCount(sourceRef), 1);
    assert.equal(h.freeCount(data), 0);
    assert.equal(h.freeCount(body), 0);
    assert.equal(h.field(body + 8, 9, 2), 1);
    assert.equal(ownership().heldBytes, size, 'source packet free is NOT last payload free');
    assert.equal(controller.lookup({ data, buffer: body }).state, 'bound');
    assert.equal(demux.reconcileShared(task), 0);
    assert.equal(ledger.held, size, 'local charge also survives source Qe');

    // One negative control: a real unbound packet free cannot retire this token.
    const unrelated = packets._5();
    const unrelatedData = h.allocate(80);
    packets.NX(unrelated, unrelatedData, 16);
    packets.Qe(unrelated);
    assert.equal(h.freeCount(unrelatedData), 1);
    assert.equal(ownership().heldBytes, size, 'unbound payload free grants no credit');
    assert.equal(demux.reconcileShared(task), 0);

    let payloadFreeObservations = 0;
    h.observeFree(pointer => {
      if (pointer !== data) return;
      payloadFreeObservations++;
      assert.equal(ownership().heldBytes, size, 'allocator runs before credit');
      assert.equal(ownership().countClosing, 1, 'prepare precedes payload free');
    });
    packets.Qe(target);
    assert.equal(payloadFreeObservations, 1);
    for (const pointer of [data, body, targetRef, target]) assert.equal(h.freeCount(pointer), 1);
    assert.equal(ownership().heldBytes, 0);
    assert.equal(ownership().countBound, 0);
    assert.equal(ownership().countClosing, 0);
    assert.equal(controller.lookup({ data, buffer: body }).found, false);
    assert.equal(controller.inspectReservation(token.sharedToken).retired, true);
    assert.equal(ledger.held, size, 'local retirement is reconciled, not guessed from packet free');
    assert.equal(demux.reconcileShared(task), 1);
    assert.equal(ledger.held, 0);
    assert.equal(ledger.sharedFreeCount, 1);
    assert.equal(ledger.sharedFreedBytes, size);
    assert.equal(ledger.t.has(token.id), false);
    assert.equal(ledger.tokenByPacket.has(source), false);
    assert.equal(demux.reconcileShared(task), 0, 'retirement cannot grant credit a second time');
    assert.equal(ledger.sharedFreeCount, 1);
    assert.equal(ledger.sharedFreedBytes, size);
    assert.equal(h.freeCount(data), 1);
    assert.equal(api.getReleaseRegistrySnapshot().faultCount, 0);
    assert.equal(task.formatContext.interval.packetBuffer.length, 0);
    api.unregisterDomain(descriptor.domainId);
    api.destroyDomainDescriptor(descriptor);

  });
