'use strict';

/*
 * Facade tests for public/web/player-shared-media-release.js.
 *
 * Scope: this step verifies the standalone registry facade only. It does NOT
 * wire or prove the real Q7 / module 13486 last-free branch, and hand calls to
 * prepareDataFree()/commitDataFree() below are facade-level evidence, never
 * proof that a real allocator last-free hook is installed.
 *
 * The tests execute the REAL webpack factory 90187 read from the shipped
 * libmedia source graph (main avplayer.js plus the synchronously loaded
 * 90201..90204 chunks) with its ACTUAL 90190 core dependency, over a genuine
 * shared WebAssembly.Memory, reusing the patterns of
 * tests/web-shared-media-domain.test.cjs.
 * The bump allocator is only a node-test fixture that provides the shared
 * control block; it is labelled as a fixture and is not the module90187 core.
 * No worker, network, application server or production instance is involved.
 * Every test carries an explicit timeout.
 */

const test = require('node:test');
const assert = require('node:assert/strict');
const path = require('node:path');
const readLibmediaSourceGraph = require('./web-libmedia-source-graph.cjs');

const sourceGraph = readLibmediaSourceGraph();
const MODULE_ID = 90187;
const CORE_MODULE_ID = 90190;
const ROOT = path.join(__dirname, '..');
const RELEASE_PATH = path.join(ROOT, 'public', 'web', 'player-shared-media-release.js');
const HEAP_BASE = 1024;
const RELEASE = require(RELEASE_PATH);

if (typeof globalThis.crypto === 'undefined' ||
    typeof globalThis.crypto.getRandomValues !== 'function') {
  Object.defineProperty(globalThis, 'crypto', {
    value: require('node:crypto').webcrypto,
    configurable: true,
    writable: true
  });
}

function extractFactorySource() {
  return sourceGraph.factory(MODULE_ID);
}

/* Evaluate the ACTUAL module 90190 ownership core from chunk 90201; the 90187
 * facade requires this exact module (no stubbed/reconstructed core). */
function loadActualCore() {
  const holder = new Function('return ({' + sourceGraph.factory(CORE_MODULE_ID) + '});')();
  const moduleObject = { exports: {} };
  holder[CORE_MODULE_ID].call(moduleObject.exports, moduleObject, moduleObject.exports,
    function (id) {
      throw new Error('unexpected module90190 dependency ' + String(id));
    });
  return moduleObject.exports;
}

const REAL_CORE = loadActualCore();

function loadApi(memory, allocator) {
  const holder = new Function('return ({' + extractFactorySource() + '});')();
  const moduleObject = { exports: {} };
  const requireStub = function (id) {
    if (id === CORE_MODULE_ID) return REAL_CORE;
    if (id === 11286) return { ce: memory };
    if (id === 46626) return allocator;
    throw new Error('unexpected webpack dependency ' + String(id));
  };
  holder[MODULE_ID].call(moduleObject.exports, moduleObject, moduleObject.exports, requireStub);
  return moduleObject.exports;
}

function makeSharedMemory() {
  return new WebAssembly.Memory({ initial: 1, maximum: 1, shared: true });
}

/* Node-test fixture allocator (NOT the 90187 allocator/core). It only carves
 * aligned bytes out of a real shared WebAssembly.Memory so that the factory
 * control block lives in the same shared domain the factory verifies. */
function createFixtureAllocator(memory) {
  const words = new Int32Array(memory.buffer);
  const bumpWord = 0;
  const freed = [];
  function zero(pointer, bytes) {
    for (let offset = 0; offset < bytes; offset += 4) {
      Atomics.store(words, (pointer + offset) >>> 2, 0);
    }
  }
  return {
    freed,
    Gy(size) {
      const bytes = Math.max(4, Math.ceil(size / 4) * 4);
      for (;;) {
        const current = Atomics.load(words, bumpWord);
        if (current === 0) {
          Atomics.compareExchange(words, bumpWord, 0, HEAP_BASE);
          continue;
        }
        const next = current + bytes;
        if (next > memory.buffer.byteLength) return 0;
        if (Atomics.compareExchange(words, bumpWord, current, next) === current) {
          zero(current, bytes);
          return current;
        }
      }
    },
    Eb(pointer) {
      freed.push(pointer);
      zero(pointer, 32);
    }
  };
}

function makeRegistry() {
  const memory = makeSharedMemory();
  const allocator = createFixtureAllocator(memory);
  const api = loadApi(memory, allocator);
  return {
    memory,
    allocator,
    api,
    registry: RELEASE.createReleaseRegistry(api)
  };
}

function makeDescriptor(api, domainId, options) {
  const settings = options || {};
  return api.createDomainDescriptor({
    capacityBytes: settings.capacityBytes === undefined ? 1024 : settings.capacityBytes,
    maxEntries: settings.maxEntries === undefined ? 4 : settings.maxEntries,
    domainId: domainId
  });
}

function bindRecord(controller, bytes, data, buffer) {
  const reservation = controller.reserve(bytes, 1);
  assert.equal(reservation.ok, true, 'test setup reservation: ' + reservation.code);
  const bound = controller.bind(reservation.token, { data, buffer, size: bytes });
  assert.equal(bound.ok, true, 'test setup bind: ' + bound.code);
  return { data, buffer };
}

function domainByName(snapshot, domainId) {
  return snapshot.domains.find((entry) => entry.domainId === domainId);
}

/* Core header words 17..20 are the release facade's per-slot identity scratch. */
function tableScratch(descriptor) {
  const view = new Int32Array(descriptor.sab);
  return [view[17], view[18], view[19], view[20]];
}

/* Deterministic error-path injection ONLY. A wrapper is never used as evidence
 * that a real domain lookup/prepare/commit failed; real-domain evidence is the
 * plain 90187 facade used by the other tests. */
function wrapDomainApi(api, overrides) {
  const handlers = overrides || {};
  return {
    VERSION: api.VERSION,
    LAYOUT: api.LAYOUT,
    attachDomainDescriptor(descriptor, actorSlot) {
      const inner = api.attachDomainDescriptor(descriptor, actorSlot);
      return {
        inner,
        descriptor: inner.descriptor,
        reserve: (bytes, generation) => inner.reserve(bytes, generation),
        bind: (token, payload) => inner.bind(token, payload),
        reclaimUnused: (token) => inner.reclaimUnused(token),
        heldBytes: () => inner.heldBytes(),
        lookup: (key) => handlers.lookup ? handlers.lookup(inner, key) : inner.lookup(key),
        prepareFinalFree: (key) => handlers.prepareFinalFree ?
          handlers.prepareFinalFree(inner, key) : inner.prepareFinalFree(key),
        commitFinalFree: (ticket, evidence) => handlers.commitFinalFree ?
          handlers.commitFinalFree(inner, ticket, evidence) : inner.commitFinalFree(ticket, evidence),
        snapshot: () => handlers.snapshot ? handlers.snapshot(inner) : inner.snapshot()
      };
    }
  };
}

function makeMockRegistry(overrides) {
  const memory = makeSharedMemory();
  const allocator = createFixtureAllocator(memory);
  const api = loadApi(memory, allocator);
  return {
    api,
    registry: RELEASE.createReleaseRegistry(wrapDomainApi(api, overrides))
  };
}

test('the module exports only the frozen factory and requires the verified 90187 facade',
  { timeout: 5000 }, () => {
    assert.equal(Object.isFrozen(RELEASE), true);
    assert.deepEqual(Object.keys(RELEASE), ['createReleaseRegistry']);
    assert.equal(typeof RELEASE.createReleaseRegistry, 'function');

    const { api } = makeRegistry();
    const registry = RELEASE.createReleaseRegistry(api);
    assert.equal(Object.isFrozen(registry), true);
    assert.deepEqual(Object.keys(registry).sort(), [
      'commitDataFree',
      'dataFreeFailed',
      'getReleaseRegistrySnapshot',
      'prepareDataFree',
      'registerDomain',
      'unregisterDomain'
    ]);
    assert.deepEqual(registry.getReleaseRegistrySnapshot(),
      { registeredDomains: 0, faultCount: 0, lastFault: null, domains: [] });

    const domainApiError = (error) => error && error.code === 'EIO_RELEASE_DOMAIN_API';
    for (const bad of [null, undefined, {}, { attachDomainDescriptor() {} }]) {
      assert.throws(() => RELEASE.createReleaseRegistry(bad), domainApiError);
    }
    assert.throws(() => RELEASE.createReleaseRegistry(
      Object.assign({}, api, { VERSION: 2 })), domainApiError);
    assert.throws(() => RELEASE.createReleaseRegistry(
      Object.assign({}, api, { LAYOUT: { headerWords: 23 } })), domainApiError);
    assert.throws(() => RELEASE.createReleaseRegistry(
      Object.assign({}, api, { attachDomainDescriptor: null })), domainApiError);

    const invalidId = (error) => error && error.code === 'EIO_DOMAIN_INVALID_DESCRIPTOR';
    for (const descriptor of [null, {}, { domainId: '' }, { domainId: 'x'.repeat(201) },
      { domainId: 42 }]) {
      assert.throws(() => registry.registerDomain(descriptor, 0), invalidId);
    }
    assert.deepEqual(registry.unregisterDomain('missing'),
      { ok: false, code: 'EIO_DOMAIN_UNKNOWN' });
    assert.equal(registry.getReleaseRegistrySnapshot().registeredDomains, 0);
  });

test('same id/same physical table is accepted; equal-byte copies and foreign tables are refused',
  { timeout: 5000 }, () => {
    const { api, registry, allocator } = makeRegistry();
    const descriptor = makeDescriptor(api, 'a');
    const controller = registry.registerDomain(descriptor, 0);
    assert.equal(typeof controller.lookup, 'function');
    assert.equal(registry.getReleaseRegistrySnapshot().registeredDomains, 1);

    const cloned = structuredClone(descriptor);
    assert.notEqual(cloned.sab, descriptor.sab, 'the clone must be a new JS view object');
    assert.equal(registry.registerDomain(cloned, 0), controller,
      'a cloned descriptor over the same physical table must return the registered controller');
    assert.equal(registry.getReleaseRegistrySnapshot().registeredDomains, 1);

    const copiedSab = new SharedArrayBuffer(descriptor.sab.byteLength);
    new Int32Array(copiedSab).set(new Int32Array(descriptor.sab));
    const copied = Object.assign({}, descriptor, { sab: copiedSab });
    assert.throws(() => registry.registerDomain(copied, 0),
      (error) => error.code === 'EIO_DOMAIN_TABLE_MISMATCH',
      'identical configuration and bytes are not a physical table match');

    const otherId = Object.assign({}, descriptor, { domainId: 'b' });
    assert.throws(() => registry.registerDomain(otherId, 0),
      (error) => error.code === 'EIO_DOMAIN_DUPLICATE_TABLE');

    const domainB = makeDescriptor(api, 'b');
    const controllerB = registry.registerDomain(domainB, 0);
    assert.equal(registry.getReleaseRegistrySnapshot().registeredDomains, 2);
    const crossTable = Object.assign({}, descriptor, { sab: domainB.sab });
    assert.throws(() => registry.registerDomain(crossTable, 0),
      (error) => error.code === 'EIO_DOMAIN_TABLE_MISMATCH');

    const wrongMemory = Object.assign({}, descriptor, { wasmMemory: makeSharedMemory() });
    assert.throws(() => registry.registerDomain(wrongMemory, 0),
      (error) => error.code === 'EIO_MEMORY_DOMAIN_MISMATCH');

    for (const table of [descriptor, cloned, copied, domainB]) {
      assert.deepEqual(tableScratch(table), [0, 0, 0, 0],
        'the identity probe must restore the candidate scratch word');
    }
    const snapshot = registry.getReleaseRegistrySnapshot();
    assert.equal(snapshot.faultCount, 0);
    assert.equal(domainByName(snapshot, 'a').ownership.heldBytes, 0);
    assert.equal(controllerB.heldBytes(), 0);
    assert.deepEqual(allocator.freed, [], 'registration must not free the control block');
  });

test('prepare retains the charge and only one exact commit credits it', { timeout: 5000 }, () => {
  const { api, registry } = makeRegistry();
  const controller = registry.registerDomain(makeDescriptor(api, 'a'), 0);
  const key = bindRecord(controller, 128, 0x11110000, 0x22220000);
  assert.equal(controller.heldBytes(), 128);

  const opaque = registry.prepareDataFree(key);
  assert.ok(opaque);
  assert.equal(Object.isFrozen(opaque), true);
  assert.deepEqual(Object.keys(opaque), []);
  assert.equal(controller.heldBytes(), 128, 'prepare must retain the charge');
  let ownership = registry.getReleaseRegistrySnapshot().domains[0].ownership;
  assert.equal(ownership.countBound, 1);
  assert.equal(ownership.countClosing, 1);

  assert.deepEqual(registry.commitDataFree(opaque),
    { ok: true, tracked: true, releasedBytes: 128 });
  assert.equal(controller.heldBytes(), 0);
  ownership = registry.getReleaseRegistrySnapshot().domains[0].ownership;
  assert.equal(ownership.countBound, 0);
  assert.equal(ownership.countClosing, 0);
  assert.equal(registry.getReleaseRegistrySnapshot().faultCount, 0,
    'a normal tracked free is not a fault');

  assert.deepEqual(registry.commitDataFree(opaque),
    { ok: false, code: 'EIO_RELEASE_TICKET_INVALID' });
  assert.equal(controller.heldBytes(), 0, 'a replayed ticket must never credit again');

  const secondKey = bindRecord(controller, 64, 0x33330000, 0x44440000);
  const second = registry.prepareDataFree(secondKey);
  assert.ok(second);
  for (const forged of [Object.assign({}, second), structuredClone(second), Object.freeze({})]) {
    assert.deepEqual(registry.commitDataFree(forged),
      { ok: false, code: 'EIO_RELEASE_TICKET_INVALID' });
  }
  assert.equal(controller.heldBytes(), 64, 'forged or cloned tickets must not credit');
  assert.equal(registry.commitDataFree(second).releasedBytes, 64);

  assert.equal(registry.prepareDataFree({ data: 0xdead, buffer: 0xbeef }), null,
    'an unknown key is a normal untracked free');
  assert.deepEqual(registry.commitDataFree(null), { ok: true, tracked: false });
  assert.deepEqual(registry.dataFreeFailed(null), { ok: true, tracked: false });
  assert.equal(registry.getReleaseRegistrySnapshot().faultCount, 0);
});

test('retire keeps a non-empty entry, zero-byte live records block removal, reservations are not reclaimed',
  { timeout: 5000 }, () => {
    const { api, registry } = makeRegistry();
    const controller = registry.registerDomain(makeDescriptor(api, 'a'), 0);
    const key = bindRecord(controller, 64, 0x11110000, 0x22220000);
    assert.deepEqual(registry.unregisterDomain('a'), { ok: true, retained: true });

    let snapshot = registry.getReleaseRegistrySnapshot();
    assert.equal(snapshot.registeredDomains, 1);
    assert.equal(snapshot.domains[0].retired, true);
    assert.equal(snapshot.domains[0].ownership.heldBytes, 64);
    assert.throws(() => registry.registerDomain(makeDescriptor(api, 'a'), 0),
      (error) => error.code === 'EIO_DOMAIN_RETIRED');

    const released = registry.prepareDataFree(key);
    assert.ok(released, 'a retired entry must still serve its last-free notice');
    assert.equal(registry.commitDataFree(released).releasedBytes, 64);
    snapshot = registry.getReleaseRegistrySnapshot();
    assert.equal(snapshot.registeredDomains, 0,
      'a retired entry is removed only after the empty-following commit');

    const registered = registry.registerDomain(makeDescriptor(api, 'a'), 0);
    assert.equal(typeof registered.lookup, 'function');

    const zero = registry.registerDomain(makeDescriptor(api, 'b'), 0);
    const zeroKey = bindRecord(zero, 0, 0x33330000, 0x44440000);
    assert.equal(zero.heldBytes(), 0);
    assert.deepEqual(registry.unregisterDomain('b'), { ok: true, retained: true },
      'a live zero-byte record must prevent premature removal');
    assert.equal(registry.getReleaseRegistrySnapshot().registeredDomains, 2);
    const zeroTicket = registry.prepareDataFree(zeroKey);
    assert.ok(zeroTicket);
    assert.equal(registry.commitDataFree(zeroTicket).releasedBytes, 0);
    assert.equal(registry.getReleaseRegistrySnapshot().registeredDomains, 1);

    const pending = registry.registerDomain(makeDescriptor(api, 'c'), 0);
    const reservation = pending.reserve(32, 1);
    assert.equal(reservation.ok, true);
    assert.deepEqual(registry.unregisterDomain('c'), { ok: true, retained: true });
    assert.equal(pending.heldBytes(), 32, 'unregister must not reclaim a reservation');
    assert.equal(registry.prepareDataFree({ data: 0x55, buffer: 0x66 }), null);
    assert.equal(registry.getReleaseRegistrySnapshot().faultCount, 0);
    assert.equal(pending.reclaimUnused(reservation.token).ok, true);
    assert.equal(pending.heldBytes(), 0);
    assert.equal(registry.getReleaseRegistrySnapshot().registeredDomains, 2,
      'the unallocated reservation needed its real caller cancellation first');
    assert.deepEqual(registry.unregisterDomain('c'), { ok: true, retained: false });
    assert.equal(registry.getReleaseRegistrySnapshot().registeredDomains, 1);
  });

test('an allocator failure keeps the CLOSING charge and permanently invalidates its ticket',
  { timeout: 5000 }, () => {
    const { api, registry } = makeRegistry();
    const controller = registry.registerDomain(makeDescriptor(api, 'f'), 0);
    const key = bindRecord(controller, 64, 0x11110000, 0x22220000);
    const opaque = registry.prepareDataFree(key);
    assert.ok(opaque);

    assert.deepEqual(registry.dataFreeFailed(opaque), { ok: true, tracked: true });
    assert.equal(controller.heldBytes(), 64, 'a failed allocator free must not credit');
    let snapshot = registry.getReleaseRegistrySnapshot();
    assert.equal(snapshot.domains[0].ownership.countBound, 1);
    assert.equal(snapshot.domains[0].ownership.countClosing, 1);
    assert.equal(snapshot.domains[0].faultCount, 1);
    assert.equal(snapshot.domains[0].lastFault, 'EIO_RELEASE_ALLOCATOR_FAILED');
    assert.equal(snapshot.faultCount, 0, 'the domain is uniquely known here');

    assert.deepEqual(registry.commitDataFree(opaque),
      { ok: false, code: 'EIO_RELEASE_TICKET_INVALID' });
    assert.deepEqual(registry.dataFreeFailed(opaque),
      { ok: false, code: 'EIO_RELEASE_TICKET_INVALID' });
    snapshot = registry.getReleaseRegistrySnapshot();
    assert.equal(snapshot.faultCount, 1);
    assert.equal(snapshot.lastFault, 'EIO_RELEASE_TICKET_INVALID',
      'an invalid/consumed ticket has no domain free-failure evidence');
    assert.equal(snapshot.domains[0].ownership.heldBytes, 64);

    assert.equal(registry.prepareDataFree(key), null,
      'no retry ticket may be issued for a failed allocator free');
    assert.equal(controller.heldBytes(), 64);
    snapshot = registry.getReleaseRegistrySnapshot();
    assert.equal(snapshot.domains[0].faultCount, 2);
    assert.equal(snapshot.lastFault, 'EIO_RELEASE_TICKET_INVALID',
      'a domain-only prepare fault must not overwrite the global fault log');
    assert.deepEqual(registry.dataFreeFailed(null), { ok: true, tracked: false });
  });

test('error-path mocks: lookup faults are global and never throw into the allocator caller',
  { timeout: 5000 }, () => {
    const thrown = makeMockRegistry({ lookup() { throw new Error('lookup injected'); } });
    const throwing = thrown.registry.registerDomain(makeDescriptor(thrown.api, 'm1'), 0);
    bindRecord(throwing, 64, 0x11110000, 0x22220000);
    assert.equal(thrown.registry.prepareDataFree({ data: 0x11110000, buffer: 0x22220000 }), null);
    let snapshot = thrown.registry.getReleaseRegistrySnapshot();
    assert.equal(snapshot.faultCount, 1);
    assert.equal(snapshot.lastFault, 'EIO_RELEASE_LOOKUP_FAILED');
    assert.equal(snapshot.domains[0].ownership.countBound, 1);
    assert.equal(snapshot.domains[0].ownership.countClosing, 0);
    assert.equal(throwing.inner.heldBytes(), 64);
    assert.deepEqual(thrown.registry.commitDataFree(null), { ok: true, tracked: false });

    const malformed = makeMockRegistry({ lookup: () => ({ ok: false, code: 'invalid-argument' }) });
    const broken = malformed.registry.registerDomain(makeDescriptor(malformed.api, 'm2'), 0);
    bindRecord(broken, 32, 0x33330000, 0x44440000);
    assert.equal(malformed.registry.prepareDataFree(null), null,
      'a malformed key must not throw into the allocator caller');
    snapshot = malformed.registry.getReleaseRegistrySnapshot();
    assert.equal(snapshot.faultCount, 1);
    assert.equal(snapshot.lastFault, 'EIO_RELEASE_LOOKUP_FAILED');
    assert.equal(broken.inner.heldBytes(), 32);
    assert.equal(snapshot.domains[0].ownership.countClosing, 0);
  });

test('error-path mocks: prepare refusal/throw faults the one known domain and prepares nothing',
  { timeout: 5000 }, () => {
    const thrown = makeMockRegistry({
      prepareFinalFree() { throw new Error('prepare injected'); }
    });
    const throwing = thrown.registry.registerDomain(makeDescriptor(thrown.api, 'p1'), 0);
    bindRecord(throwing, 64, 0x11110000, 0x22220000);
    assert.equal(thrown.registry.prepareDataFree({ data: 0x11110000, buffer: 0x22220000 }), null);
    let snapshot = thrown.registry.getReleaseRegistrySnapshot();
    assert.equal(snapshot.faultCount, 0);
    assert.equal(snapshot.domains[0].faultCount, 1);
    assert.equal(snapshot.domains[0].lastFault, 'EIO_RELEASE_PREPARE_FAILED');
    assert.equal(snapshot.domains[0].ownership.countBound, 1);
    assert.equal(snapshot.domains[0].ownership.countClosing, 0);
    assert.equal(throwing.inner.heldBytes(), 64);

    const refused = makeMockRegistry({
      prepareFinalFree: () => ({ ok: false, code: 'unknown-record' })
    });
    const refusing = refused.registry.registerDomain(makeDescriptor(refused.api, 'p2'), 0);
    bindRecord(refusing, 32, 0x33330000, 0x44440000);
    assert.equal(refused.registry.prepareDataFree({ data: 0x33330000, buffer: 0x44440000 }), null);
    snapshot = refused.registry.getReleaseRegistrySnapshot();
    assert.equal(snapshot.domains[0].faultCount, 1);
    assert.equal(snapshot.domains[0].lastFault, 'EIO_RELEASE_PREPARE_FAILED');
    assert.equal(refusing.inner.heldBytes(), 32);
    assert.equal(snapshot.domains[0].ownership.countClosing, 0);
  });

test('a real two-domain key match is an ambiguous global fault and never prepares either record',
  { timeout: 5000 }, () => {
    const { api, registry } = makeRegistry();
    const controllerA = registry.registerDomain(makeDescriptor(api, 'amb-a'), 0);
    const controllerB = registry.registerDomain(makeDescriptor(api, 'amb-b'), 0);
    bindRecord(controllerA, 64, 0x0a0a0a0a, 0x0b0b0b0b);
    bindRecord(controllerB, 64, 0x0a0a0a0a, 0x0b0b0b0b);

    assert.equal(registry.prepareDataFree({ data: 0x0a0a0a0a, buffer: 0x0b0b0b0b }), null);
    const snapshot = registry.getReleaseRegistrySnapshot();
    assert.equal(snapshot.faultCount, 1);
    assert.equal(snapshot.lastFault, 'EIO_RELEASE_AMBIGUOUS_DOMAIN');
    assert.equal(controllerA.lookup({ data: 0x0a0a0a0a, buffer: 0x0b0b0b0b }).state, 'bound');
    assert.equal(controllerB.lookup({ data: 0x0a0a0a0a, buffer: 0x0b0b0b0b }).state, 'bound');
    assert.equal(controllerA.heldBytes(), 64);
    assert.equal(controllerB.heldBytes(), 64);
    assert.equal(snapshot.domains[0].ownership.countClosing, 0);
    assert.equal(snapshot.domains[1].ownership.countClosing, 0);
    assert.deepEqual(registry.commitDataFree(null), { ok: true, tracked: false });
  });

test('error-path mocks: commit refusal/throw consumes the ticket, faults the domain and never throws',
  { timeout: 5000 }, () => {
    const thrown = makeMockRegistry({
      commitFinalFree() { throw new Error('commit injected'); }
    });
    const throwing = thrown.registry.registerDomain(makeDescriptor(thrown.api, 'c1'), 0);
    bindRecord(throwing, 64, 0x11110000, 0x22220000);
    const opaque = thrown.registry.prepareDataFree({ data: 0x11110000, buffer: 0x22220000 });
    assert.ok(opaque);
    assert.deepEqual(thrown.registry.commitDataFree(opaque),
      { ok: false, code: 'EIO_RELEASE_COMMIT_FAILED' });
    assert.deepEqual(thrown.registry.commitDataFree(opaque),
      { ok: false, code: 'EIO_RELEASE_TICKET_INVALID' },
      'a failed commit must consume the opaque identity');
    let snapshot = thrown.registry.getReleaseRegistrySnapshot();
    assert.equal(snapshot.domains[0].faultCount, 1);
    assert.equal(snapshot.domains[0].lastFault, 'EIO_RELEASE_COMMIT_FAILED');
    assert.equal(snapshot.domains[0].ownership.heldBytes, 64);
    assert.equal(snapshot.domains[0].ownership.countClosing, 1);
    assert.equal(snapshot.faultCount, 0);

    const refused = makeMockRegistry({
      commitFinalFree: () => ({ ok: false, code: 'stale-ticket' })
    });
    const refusing = refused.registry.registerDomain(makeDescriptor(refused.api, 'c2'), 0);
    bindRecord(refusing, 32, 0x33330000, 0x44440000);
    const second = refused.registry.prepareDataFree({ data: 0x33330000, buffer: 0x44440000 });
    assert.ok(second);
    assert.deepEqual(refused.registry.commitDataFree(second),
      { ok: false, code: 'EIO_RELEASE_COMMIT_FAILED' });
    snapshot = refused.registry.getReleaseRegistrySnapshot();
    assert.equal(snapshot.domains[0].faultCount, 1);
    assert.equal(snapshot.domains[0].lastFault, 'EIO_RELEASE_COMMIT_FAILED');
    assert.equal(refusing.inner.heldBytes(), 32);
    assert.equal(refusing.inner.snapshot().countClosing, 1);
  });

test('the registry holds at most 64 domains and never evicts an existing entry',
  { timeout: 5000 }, () => {
    const { api, registry } = makeRegistry();
    const controllers = [];
    const descriptors = [];
    for (let index = 0; index < 64; index += 1) {
      const descriptor = makeDescriptor(api, 'd' + index);
      descriptors.push(descriptor);
      controllers.push(registry.registerDomain(descriptor, index % 4));
    }
    assert.equal(registry.getReleaseRegistrySnapshot().registeredDomains, 64);

    const extra = makeDescriptor(api, 'd64');
    assert.throws(() => registry.registerDomain(extra, 0),
      (error) => error.code === 'EIO_DOMAIN_REGISTRY_FULL');
    let snapshot = registry.getReleaseRegistrySnapshot();
    assert.equal(snapshot.registeredDomains, 64, 'a refused 65th domain must not evict');
    assert.equal(domainByName(snapshot, 'd0').retired, false);
    assert.equal(registry.registerDomain(structuredClone(descriptors[0]), 0), controllers[0],
      'an active id may still re-register its own physical table while full');

    assert.deepEqual(registry.unregisterDomain('d0'), { ok: true, retained: false });
    assert.equal(registry.getReleaseRegistrySnapshot().registeredDomains, 63);
    const registered = registry.registerDomain(extra, 0);
    assert.equal(typeof registered.lookup, 'function');
    snapshot = registry.getReleaseRegistrySnapshot();
    assert.equal(snapshot.registeredDomains, 64);
    assert.equal(domainByName(snapshot, 'd0'), undefined);
    assert.ok(domainByName(snapshot, 'd64'));
    for (const entry of snapshot.domains) {
      assert.equal(entry.ownership.heldBytes, 0);
      assert.equal(entry.faultCount, 0);
    }
  });

/* Directed regression: the four charge counters are NOT a transaction. In the
 * verified core, bind() first CASes the slot RESERVED -> BINDING, then a
 * zero-byte record reaches an all-zero counter view when COUNT_RESERVED is
 * decremented to 0 before COUNT_BOUND is incremented (and before the slot
 * STATE becomes BOUND), so the live slot is BINDING/non-FREE at that instant.
 * In commitFinalFree the analogous COUNT_BOUND decrement happens after
 * releaseSlot() already reset the slot to FREE and while COUNT_CLOSING is still
 * 1, so the only all-zero window that can still hide a LIVE slot is this
 * zero-byte bind transition. A counters-only empty predicate drops the domain
 * there; the slot scan must keep it.
 *
 * The Atomics.sub hook below is a DETERMINISTIC single-interleaving injection,
 * not concurrent-worker acceptance. It uses the real core token/table and only
 * inspects this one view/index. */
test('a zero-byte slot transition that reads all-zero counters keeps the domain (slot scan, not counters alone)',
  { timeout: 5000 }, () => {
    const { api, registry } = makeRegistry();
    const descriptor = makeDescriptor(api, 'zero-window');
    const controller = registry.registerDomain(descriptor, 0);
    const reservation = controller.reserve(0, 1);
    assert.equal(reservation.ok, true, 'a zero-byte reservation is a real live record');
    assert.equal(controller.heldBytes(), 0);
    const countReservedWord = api.LAYOUT.header.COUNT_RESERVED;
    const headerEntriesOffsetWord = api.LAYOUT.header.ENTRIES_OFFSET;
    const entriesOffset = descriptor.entriesOffset;
    const entryWords = descriptor.entryWords;
    const slotIndex = reservation.token.entry;
    const table = new Int32Array(descriptor.sab);
    assert.equal(descriptor.entriesOffset, api.LAYOUT.headerWords,
      'the descriptor entry base must be the core header size');
    assert.equal(entriesOffset, Atomics.load(table, headerEntriesOffsetWord),
      'the descriptor entry base must match the table header ENTRY offset');
    assert.equal(slotIndex, 0, 'the first claim must use slot 0; the state load is token.entry based');
    assert.equal(Atomics.load(table, entriesOffset + slotIndex * entryWords), 2,
      'the reserved slot (token.entry) must sit in the RESERVED state before bind');

    const originalSub = Atomics.sub;
    const observed = { fired: 0, error: null, slotState: -1, result: null, registeredAfter: -1 };
    Atomics.sub = function injectedZeroWindow(target, index, value) {
      const resultBeforeSub = originalSub.call(Atomics, target, index, value);
      if (observed.fired === 0 &&
          index === countReservedWord &&
          target.buffer === descriptor.sab &&
          Atomics.load(target, index) === 0) {
        observed.fired += 1;
        try {
          const counters = controller.snapshot();
          observed.counters = Object.freeze({
            heldBytes: counters.heldBytes,
            countReserved: counters.countReserved,
            countBound: counters.countBound,
            countClosing: counters.countClosing
          });
          observed.slotState = Atomics.load(
            new Int32Array(descriptor.sab), entriesOffset + slotIndex * entryWords);
          observed.result = registry.unregisterDomain('zero-window');
          observed.registeredAfter =
            registry.getReleaseRegistrySnapshot().registeredDomains;
        } catch (error) {
          observed.error = error;
        }
      }
      return resultBeforeSub;
    };
    let bound = null;
    try {
      bound = controller.bind(reservation.token, {
        data: 0x51510000,
        buffer: 0x52520000,
        size: 0
      });
    } finally {
      Atomics.sub = originalSub;
    }

    assert.equal(Atomics.sub, originalSub, 'the Atomics.sub injection must be restored');
    assert.equal(observed.error, null, 'the injected interleaving must not throw');
    assert.equal(observed.fired, 1, 'the zero-byte bind window must be observed exactly once');
    assert.deepEqual(observed.counters, {
      heldBytes: 0,
      countReserved: 0,
      countBound: 0,
      countClosing: 0
    }, 'the counters alone read all-zero in this window');
    assert.equal(observed.slotState, 3,
      'the live slot is BINDING (non-FREE) at that instant, so a counters-only predicate is wrong');
    assert.deepEqual(observed.result, { ok: true, retained: true },
      'unregister must retain the domain because a live slot exists');
    assert.equal(observed.registeredAfter, 1,
      'the registered domain must not be dropped in the all-zero counter window');

    assert.equal(bound.ok, true, 'the original bind must complete after the injection');
    assert.equal(controller.lookup({ data: 0x51510000, buffer: 0x52520000 }).state, 'bound');
    let snapshot = registry.getReleaseRegistrySnapshot();
    assert.equal(snapshot.registeredDomains, 1);
    assert.equal(snapshot.domains[0].retired, true, 'the retained entry stays retired and reachable');
    assert.equal(snapshot.domains[0].ownership.countBound, 1);

    /* Finish the real lifecycle: prepare/commit credits 0 bytes and the empty
     * retired entry is removed only after every slot is FREE again. */
    const opaque = registry.prepareDataFree({ data: 0x51510000, buffer: 0x52520000 });
    assert.ok(opaque);
    assert.deepEqual(registry.commitDataFree(opaque),
      { ok: true, tracked: true, releasedBytes: 0 });
    snapshot = registry.getReleaseRegistrySnapshot();
    assert.equal(snapshot.registeredDomains, 0);
    assert.equal(snapshot.faultCount, 0);
    assert.deepEqual(tableScratch(descriptor), [0, 0, 0, 0]);
  });
