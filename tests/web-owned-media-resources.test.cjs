'use strict';

/*
 * Focused tests for owned-resource accounting (FLAGS=1) in the changed paths:
 * public/web/player-shared-media-ownership.js (bindOwnedResource /
 * releaseOwnedResource / owned-resource snapshot scan) and
 * public/web/player-owned-media-resources.js (createOwnedResources).
 *
 * Every byte assertion reads the real SharedArrayBuffer created by the core
 * helper. No worker, server, browser, upstream or production instance is used.
 * The two injections patch Atomics in this one isolate and are restored in a
 * finally block; they are labelled shims for the pre/post-CAS integrity checks
 * and the bind-failure path, not worker concurrency and not the real IO
 * integration.
 */

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const ROOT = path.join(__dirname, '..');
const CORE_PATH = path.join(ROOT, 'public', 'web', 'player-shared-media-ownership.js');
const WRAPPER_PATH = path.join(ROOT, 'public', 'web', 'player-owned-media-resources.js');
const core = require(CORE_PATH);
const owned = require(WRAPPER_PATH);
const E = core.LAYOUT.entry;
const FLAGS = core.LAYOUT.flags;
const PROOF = { referencesDropped: true };

function slot(ctl, entry) {
  const descriptor = ctl.descriptor;
  return {
    view: new Int32Array(descriptor.sab),
    offset: descriptor.entriesOffset + entry * descriptor.entryWords
  };
}

function forgedHandle(token, overrides) {
  return Object.assign({
    brand: token.brand, owned: true, entry: token.entry,
    id: token.id, bytes: token.bytes, generation: token.generation
  }, overrides || {});
}

test('core: owned records are FLAGS=1 with zero pointers, share HELD, release exactly once',
  { timeout: 5000 }, () => {
    const ctl = core.create({ capacityBytes: 8 * 1024, maxEntries: 8 });
    const reserved = ctl.reserve(1024, 3);
    assert.equal(reserved.ok, true);
    const bound = ctl.bindOwnedResource(reserved.token);
    assert.equal(bound.ok, true);
    assert.equal(Object.isFrozen(bound.handle), true);
    assert.deepEqual(
      [bound.handle.entry, bound.handle.id, bound.handle.bytes, bound.handle.generation],
      [reserved.token.entry, reserved.token.id, 1024, 3]);

    const record = slot(ctl, bound.handle.entry);
    const word = (name) => Atomics.load(record.view, record.offset + E[name]);
    assert.deepEqual([word('STATE'), word('FLAGS'), word('DATA'), word('BUFFER')],
      [core.STATE.BOUND, FLAGS.OWNED_RESOURCE, 0, 0]);

    let snapshot = ctl.snapshot();
    assert.deepEqual(
      [snapshot.heldBytes, snapshot.reservedBytes, snapshot.boundBytes, snapshot.countReserved,
        snapshot.countBound, snapshot.ownedResourceBytes, snapshot.countOwnedResources,
        snapshot.ownedResourceScan],
      [1024, 0, 1024, 0, 1, 1024, 1, 'nontransactional'], 'binding must conserve HELD');

    // Zero-key records never reach the sample key paths, and a bound owned
    // record is not an unused reclaim.
    assert.equal(ctl.prepareFinalFree({ data: 0, buffer: 0 }).code, 'invalid-argument');
    assert.equal(ctl.prepareFinalFree({ data: 0x1000, buffer: 0x2000 }).code, 'unknown-record');
    assert.equal(ctl.lookup({ data: 0x1000, buffer: 0x2000 }).found, false);
    assert.equal(ctl.reclaimUnused(reserved.token).code, 'bound');

    // A normal FLAGS=0 sample in the same domain is refused by owned release.
    const sample = ctl.reserve(512, 4);
    assert.equal(ctl.bind(sample.token, { data: 0x1000, buffer: 0x2000 }).ok, true);
    assert.equal(ctl.releaseOwnedResource(forgedHandle(sample.token), PROOF).code, 'not-owned');
    assert.equal(ctl.snapshot().heldBytes, 1536);

    // Proof is mandatory; identity, flags and pointers are checked before the
    // claim and nothing is credited on refusal.
    assert.equal(ctl.releaseOwnedResource(bound.handle).code, 'not-dropped');
    assert.equal(ctl.releaseOwnedResource(bound.handle, {}).code, 'not-dropped');
    assert.equal(ctl.releaseOwnedResource(bound.handle, { referencesDropped: false }).code,
      'not-dropped');
    assert.equal(ctl.releaseOwnedResource(forgedHandle(bound.handle, { generation: 99 }), PROOF).code,
      'stale-token');
    assert.equal(ctl.releaseOwnedResource(forgedHandle(bound.handle, { id: bound.handle.id + 7 }),
      PROOF).code, 'stale-token');
    Atomics.store(record.view, record.offset + E.FLAGS, 2);
    assert.equal(ctl.releaseOwnedResource(bound.handle, PROOF).code, 'corrupt-record');
    assert.equal(word('STATE'), core.STATE.BOUND);
    assert.equal(ctl.snapshot().heldBytes, 1536, 'refusals must not credit');
    Atomics.store(record.view, record.offset + E.FLAGS, FLAGS.OWNED_RESOURCE);

    // Shim: the record is corrupted after the BOUND->COMMITTING CAS won, so the
    // post-CAS verification must restore BOUND and credit nothing.
    const realCas = Atomics.compareExchange;
    Atomics.compareExchange = function (array, index, expected, replacement) {
      const result = realCas.call(Atomics, array, index, expected, replacement);
      if (array && array.buffer === ctl.descriptor.sab && index === record.offset + E.STATE &&
          expected === core.STATE.BOUND && replacement === core.STATE.COMMITTING &&
          result === expected) {
        Atomics.store(record.view, record.offset + E.DATA, 0x4000);
      }
      return result;
    };
    let torn;
    try {
      torn = ctl.releaseOwnedResource(bound.handle, PROOF);
    } finally {
      Atomics.compareExchange = realCas;
      Atomics.store(record.view, record.offset + E.DATA, 0);
    }
    assert.equal(Atomics.compareExchange, realCas, 'the CAS shim must be restored');
    assert.equal(torn.code, 'corrupt-record');
    assert.equal(word('STATE'), core.STATE.BOUND, 'a post-CAS mismatch restores BOUND');
    assert.equal(ctl.snapshot().heldBytes, 1536);

    const released = ctl.releaseOwnedResource(bound.handle, PROOF);
    assert.deepEqual([released.ok, released.releasedBytes, released.heldBytes], [true, 1024, 512]);
    snapshot = ctl.snapshot();
    assert.deepEqual(
      [snapshot.countBound, snapshot.boundBytes, snapshot.ownedResourceBytes,
        snapshot.countOwnedResources, snapshot.peakBytes],
      [1, 512, 0, 0, 1536]);

    // Single use: the replay cannot subtract twice and no clamp hides it.
    const replay = ctl.releaseOwnedResource(bound.handle, PROOF);
    assert.deepEqual([replay.ok, replay.code], [false, 'stale-token']);
    assert.deepEqual([ctl.snapshot().heldBytes, ctl.snapshot().countBound], [512, 1]);

    const prepared = ctl.prepareFinalFree({ data: 0x1000, buffer: 0x2000 });
    assert.equal(ctl.commitFinalFree(prepared.ticket, { dataFreed: true }).ok, true);
    assert.equal(ctl.snapshot().heldBytes, 0);
  });

test('wrapper: explicit results, faults keep the real charge, leases block, release is single use',
  { timeout: 5000 }, () => {
    assert.equal(typeof owned.createOwnedResources, 'function');
    assert.equal(owned.KIND, 'eio-owned-media-resources/v1');
    assert.equal(/new\s+FinalizationRegistry|new\s+WeakRef/.test(fs.readFileSync(WRAPPER_PATH, 'utf8')),
      false, 'the wrapper must not credit bytes from GC callbacks');
    assert.throws(() => owned.createOwnedResources({}), { code: 'invalid-argument' });

    const ctl = core.create({ capacityBytes: 2048, maxEntries: 4 });
    const resources = owned.createOwnedResources(ctl);
    const acquired = resources.acquire(1000, 'io-source', 1);
    assert.equal(acquired.ok, true);
    const handle = acquired.handle;
    assert.equal(Object.isFrozen(handle), true);
    assert.deepEqual(Object.keys(handle), ['bytes', 'label', 'release']);
    assert.deepEqual([handle.bytes, handle.label], [1000, 'io-source']);
    let local = resources.snapshot();
    assert.deepEqual(
      [local.liveBytes, local.liveCount, local.exact,
        local.core.heldBytes, local.core.ownedResourceBytes], [1000, 1, false, 1000, 1000]);

    // Admission refusals are explicit results: no handle, no charge, no alloc.
    const overCapacity = resources.acquire(1100, 'too-big', 1);
    assert.deepEqual([overCapacity.ok, overCapacity.code, overCapacity.chargedBytes,
      overCapacity.fault], [false, 'capacity-exceeded', 0, false]);
    assert.equal(resources.acquire(-1).code, 'invalid-argument');
    assert.equal(resources.acquire(1.5).code, 'invalid-argument');
    assert.equal(resources.acquire(10, 42).code, 'invalid-argument');
    assert.equal(resources.acquire(10, 'x', -1).code, 'invalid-argument');
    assert.deepEqual([ctl.snapshot().heldBytes, resources.snapshot().liveBytes,
      resources.snapshot().refusedCount], [1000, 1000, 5]);

    // Missing proof and an outstanding alias both keep the charge and the handle.
    assert.deepEqual([handle.release({}).code, handle.release({}).chargedBytes],
      ['not-dropped', 1000]);
    assert.equal(handle.retain().ok, true);
    const aliased = handle.release(PROOF);
    assert.deepEqual([aliased.code, aliased.chargedBytes], ['aliases-remain', 1000]);
    assert.deepEqual([ctl.snapshot().heldBytes, resources.snapshot().liveCount], [1000, 1]);
    assert.equal(handle.drop().ok, true);
    assert.equal(handle.drop().code, 'no-lease');

    const released = handle.release(PROOF);
    assert.deepEqual([released.ok, released.releasedBytes], [true, 1000]);
    local = resources.snapshot();
    assert.deepEqual([local.liveBytes, local.liveCount, local.faultCount,
      ctl.snapshot().heldBytes], [0, 0, 0, 0]);
    assert.deepEqual([handle.release(PROOF).code, handle.release(PROOF).releasedBytes],
      ['already-released', 0]);

    // Shim: a bind failure after the real reservation charge. reserve() is the
    // real core call, so the bytes are genuinely charged; only the bind step is
    // forced to fail. That pending charge must stay tracked, never be reclaimed
    // here and never be reported as 0.
    const failingController = {
      reserve: (bytes, generation) => ctl.reserve(bytes, generation),
      bindOwnedResource: () => ({ ok: false, code: 'busy', message: 'injected bind failure' }),
      releaseOwnedResource: (h, proof) => ctl.releaseOwnedResource(h, proof),
      snapshot: () => ctl.snapshot()
    };
    const faults = owned.createOwnedResources(failingController);
    const bindFailed = faults.acquire(200, 'demux-oBuffer', 2);
    assert.deepEqual([bindFailed.ok, bindFailed.fault, bindFailed.chargedBytes, bindFailed.code],
      [false, true, 200, 'busy']);
    assert.equal(ctl.snapshot().heldBytes, 200, 'the real charge must survive the bind failure');
    const faultSnapshot = faults.snapshot();
    assert.deepEqual([faultSnapshot.liveBytes, faultSnapshot.faultCount,
      faultSnapshot.faultChargedBytes], [0, 1, 200]);
    assert.deepEqual(Object.keys(faultSnapshot.lastFault).sort(),
      ['bytes', 'chargedBytes', 'code', 'label', 'message', 'sequence']);
    assert.equal(faultSnapshot.lastFault.label, 'demux-oBuffer');
    // The pending entry is still tracked by the core: only the core may decide.
    assert.equal(ctl.reclaimUnused(bindFailed.token).reclaimedBytes, 200);
    assert.equal(ctl.snapshot().heldBytes, 0);

    // Entry admission is the core entry table, not a second local cap.
    const small = owned.createOwnedResources(core.create({ capacityBytes: 64, maxEntries: 1 }));
    assert.equal(small.acquire(10, 'mv').ok, true);
    const exhausted = small.acquire(10, 'mv');
    assert.deepEqual([exhausted.code, exhausted.chargedBytes], ['entries-exhausted', 0]);
    assert.equal(small.snapshot().core.heldBytes, 10);
  });
