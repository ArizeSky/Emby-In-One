'use strict';

/*
 * Focused tests for the common controlled-media Handle facade. Every byte
 * assertion reads the real SharedArrayBuffer created by the ownership core.
 * No worker, server, browser, upstream or production instance is used.
 */

const test = require('node:test');
const assert = require('node:assert/strict');
const path = require('node:path');

const ROOT = path.join(__dirname, '..');
const core = require(path.join(ROOT, 'web-src', 'player-shared-media-ownership.js'));
const owned = require(path.join(ROOT, 'web-src', 'player-owned-media-resources.js'));
const handles = require(path.join(ROOT, 'web-src', 'player-media-resource-handles.js'));

function make(capacityBytes, options) {
  const controller = core.create({ capacityBytes, maxEntries: 16 });
  const ledger = handles.createMediaResourceHandles(
    Object.assign({ controller }, options || {}));
  return { controller, ledger };
}

function held(controller) {
  return controller.snapshot().heldBytes;
}

test('owned acquire binds the shared charge and finalRelease credits exactly once',
  { timeout: 5000 }, () => {
    const { controller, ledger } = make(4096);
    const acquired = ledger.acquire({
      kind: 'owned', size: 1024, label: 'mp4-hevc-hvcc-native',
      owner: 0x10, generation: 1
    });
    assert.equal(acquired.ok, true);
    assert.equal(acquired.handle.bytes, 1024);
    assert.equal(acquired.handle.kind, 'owned');
    assert.equal(acquired.handle.state, 'bound');
    assert.equal(held(controller), 1024);
    assert.deepEqual(
      [controller.snapshot().boundBytes, controller.snapshot().ownedResourceBytes,
        controller.snapshot().countOwnedResources],
      [1024, 1024, 1]);

    assert.equal(acquired.handle.bindNative({ data: 0x1000 }).ok, true);
    assert.equal(acquired.handle.publish().ok, true);
    assert.equal(acquired.handle.state, 'published');
    assert.equal(acquired.handle.isCancelled(), false);

    const released = acquired.handle.finalRelease({ referencesDropped: true });
    assert.equal(released.ok, true);
    assert.equal(released.releasedBytes, 1024);
    assert.equal(held(controller), 0);
    assert.equal(acquired.handle.state, 'released');
    assert.equal(acquired.handle.finalRelease({ referencesDropped: true }).code,
      'already-released');
    assert.equal(held(controller), 0);
  });

test('adopt wraps an owned handle already acquired through OwnedResources',
  { timeout: 5000 }, () => {
    const { controller } = make(4096);
    const manager = owned.createOwnedResources(controller);
    const existing = manager.acquire(512, 'matroska-codec-private-native', 2);
    assert.equal(existing.ok, true);
    assert.equal(held(controller), 512);

    const ledger = handles.createMediaResourceHandles({ controller });
    const adopted = ledger.adopt({
      kind: 'owned', size: 512, label: 'matroska-codec-private-native',
      owner: 0x20, generation: 2, coreHandle: existing.handle,
      isCancelled: () => false
    });
    assert.equal(adopted.ok, true);
    assert.equal(adopted.handle.state, 'bound');
    assert.equal(adopted.handle.bindNative({ data: 0x2000 }).ok, true);
    assert.equal(adopted.handle.finalRelease({ referencesDropped: true }).ok, true);
    assert.equal(held(controller), 0);
    assert.equal(manager.snapshot().liveBytes, 0);
    assert.equal(adopted.handle.finalRelease({ referencesDropped: true }).code,
      'already-released');
  });

test('sample reservation requires the real free evidence and commits the ticket once',
  { timeout: 5000 }, () => {
    const { controller, ledger } = make(4096);
    const acquired = ledger.acquire({
      kind: 'sample', size: 256, label: 'mp4-sample', owner: 0x30, generation: 3
    });
    assert.equal(acquired.ok, true);
    assert.equal(acquired.handle.state, 'reserved');
    assert.equal(held(controller), 256);

    assert.equal(acquired.handle.finalRelease({ referencesDropped: true }).ok, true);
    assert.equal(held(controller), 0);

    const bound = ledger.acquire({
      kind: 'sample', size: 256, label: 'mp4-sample', owner: 0x30, generation: 3
    });
    assert.equal(bound.ok, true);
    assert.equal(bound.handle.bindNative({ data: 0x3000 }).code, 'invalid-argument');
    assert.equal(bound.handle.bindNative({ data: 0x3000, buffer: 0x4000 }).ok, true);
    assert.equal(bound.handle.state, 'bound');
    assert.equal(bound.handle.finalRelease({ referencesDropped: true }).code,
      'final-free-required');
    assert.equal(held(controller), 256);

    const prepared = bound.handle.prepareFinalFree();
    assert.equal(prepared.ok, true);
    assert.equal(bound.handle.state, 'closing');
    assert.equal(bound.handle.commitFinalFree(prepared.ticket, { dataFreed: false }).code,
      'not-freed');
    assert.equal(held(controller), 256);
    assert.equal(bound.handle.commitFinalFree(prepared.ticket, { dataFreed: true }).ok,
      true);
    assert.equal(held(controller), 0);
    assert.equal(bound.handle.state, 'released');
    assert.equal(bound.handle.commitFinalFree(prepared.ticket, { dataFreed: true }).code,
      'already-released');
  });

test('unbound sample discard reclaims the reservation without native pointers',
  { timeout: 5000 }, () => {
    const { controller, ledger } = make(4096);
    const acquired = ledger.acquire({
      kind: 'sample', size: 128, label: 'cancelled-before-malloc', generation: 4
    });
    assert.equal(acquired.ok, true);
    assert.equal(held(controller), 128);
    const discarded = acquired.handle.discard('cancelled');
    assert.equal(discarded.ok, true);
    assert.equal(discarded.reason, 'cancelled');
    assert.equal(held(controller), 0);
    assert.equal(acquired.handle.state, 'released');
    assert.equal(acquired.handle.discard('again').code, 'already-released');
  });

test('a duplicate native identity under one owner is refused and keeps both charges',
  { timeout: 5000 }, () => {
    const { controller, ledger } = make(4096);
    const first = ledger.acquire({
      kind: 'owned', size: 64, label: 'first', owner: 7, generation: 1
    });
    const second = ledger.acquire({
      kind: 'owned', size: 64, label: 'second', owner: 7, generation: 1
    });
    assert.equal(first.handle.bindNative({ data: 0x5000 }).ok, true);
    const collision = second.handle.bindNative({ data: 0x5000 });
    assert.equal(collision.code, 'identity-collision');
    assert.equal(collision.chargedBytes, 0);
    assert.equal(second.handle.state, 'ambiguous');
    assert.equal(held(controller), 128);

    assert.equal(second.handle.finalRelease({ referencesDropped: true }).ok, true);
    assert.equal(held(controller), 64);
    assert.equal(first.handle.finalRelease({ referencesDropped: true }).ok, true);
    assert.equal(held(controller), 0);
  });

test('retained aliases block release until the matching drop', { timeout: 5000 }, () => {
  const { controller, ledger } = make(4096);
  const acquired = ledger.acquire({
    kind: 'owned', size: 128, label: 'retained', generation: 1
  });
  assert.equal(acquired.handle.retain().ok, true);
  const blocked = acquired.handle.finalRelease({ referencesDropped: true });
  assert.equal(blocked.code, 'aliases-remain');
  assert.equal(blocked.leases, 1);
  assert.equal(held(controller), 128);
  assert.equal(acquired.handle.drop().ok, true);
  assert.equal(acquired.handle.finalRelease({ referencesDropped: true }).ok, true);
  assert.equal(held(controller), 0);
});

test('batch reservation is all-or-nothing on capacity pressure', { timeout: 5000 }, () => {
  const { controller, ledger } = make(1000);
  const batch = ledger.acquireBatch([
    { kind: 'sample', size: 100, label: 'a', generation: 1 },
    { kind: 'sample', size: 200, label: 'b', generation: 1 },
    { kind: 'sample', size: 300, label: 'c', generation: 1 }
  ]);
  assert.equal(batch.ok, true);
  assert.equal(batch.handles.length, 3);
  assert.equal(held(controller), 600);
  for (const handle of batch.handles) assert.equal(handle.discard('test').ok, true);
  assert.equal(held(controller), 0);

  const tight = make(100);
  const refused = tight.ledger.acquireBatch([
    { kind: 'sample', size: 60, label: 'a', generation: 1 },
    { kind: 'sample', size: 60, label: 'b', generation: 1 }
  ]);
  assert.equal(refused.ok, false);
  assert.equal(refused.code, 'capacity-exceeded');
  assert.equal(refused.retry, true);
  assert.equal(held(tight.controller), 0);
});

test('temporal admission uses the confirmed clock and the bootstrap span',
  { timeout: 5000 }, () => {
    const confirmed = make(4096, {
      maxForwardMs: 120000,
      clockRead: () => ({ known: true, mediaMs: 10000 })
    });
    const allowed = confirmed.ledger.acquire({
      kind: 'owned', size: 1, label: 'at-limit', generation: 1, mediaTime: 130000
    });
    assert.equal(allowed.ok, true);
    assert.equal(allowed.handle.finalRelease({ referencesDropped: true }).ok, true);
    const refused = confirmed.ledger.acquire({
      kind: 'owned', size: 1, label: 'past-limit', generation: 1, mediaTime: 130001
    });
    assert.equal(refused.ok, false);
    assert.equal(refused.code, 'EIO_DEMUX_FORWARD_WINDOW_PENDING');
    assert.equal(refused.limitMs, 130000);
    assert.equal(refused.chargedBytes, 0);
    assert.equal(held(confirmed.controller), 0);

    const bootstrap = make(4096, {
      maxForwardMs: 120000,
      clockRead: () => ({ known: false })
    });
    for (const mediaTime of [0, 120000]) {
      const ok = bootstrap.ledger.acquire({
        kind: 'owned', size: 1, label: 'bootstrap', generation: 1, mediaTime
      });
      assert.equal(ok.ok, true);
      assert.equal(ok.handle.finalRelease({ referencesDropped: true }).ok, true);
    }
    const beyond = bootstrap.ledger.acquire({
      kind: 'owned', size: 1, label: 'bootstrap-limit', generation: 1, mediaTime: 120001
    });
    assert.equal(beyond.ok, false);
    assert.equal(beyond.code, 'EIO_DEMUX_CLOCK_UNCONFIRMED');
    assert.equal(beyond.baseMs, 0);
    assert.equal(beyond.spanMs, 120000);
  });

test('adopted cancellation is delegated, not inferred from the generation',
  { timeout: 5000 }, () => {
    const { controller } = make(4096);
    const manager = owned.createOwnedResources(controller);
    const existing = manager.acquire(32, 'adopted-cancelled', 9);
    assert.equal(existing.ok, true);
    const ledger = handles.createMediaResourceHandles({ controller });
    const adopted = ledger.adopt({
      kind: 'owned', size: 32, label: 'adopted-cancelled', generation: 9,
      coreHandle: existing.handle, isCancelled: () => true
    });
    assert.equal(adopted.ok, true);
    assert.equal(adopted.handle.isCancelled(), true);
    assert.equal(adopted.handle.finalRelease({ referencesDropped: true }).ok, true);
    assert.equal(held(controller), 0);
  });

test('adopt-only ledger wraps an existing charge without a second controller',
  { timeout: 5000 }, () => {
    const controller = core.create({ capacityBytes: 4096, maxEntries: 16 });
    const manager = owned.createOwnedResources(controller);
    const existing = manager.acquire(64, 'adopt-only', 4);
    assert.equal(existing.ok, true);
    assert.equal(held(controller), 64);

    const ledger = handles.createMediaResourceHandles({ adoptOnly: true });
    assert.equal(ledger.acquire({
      kind: 'owned', size: 8, label: 'must-refuse'
    }).code, 'controller-unavailable');
    assert.equal(ledger.acquireBatch([{
      kind: 'owned', size: 8, label: 'must-refuse'
    }]).code, 'controller-unavailable');
    assert.equal(ledger.snapshot().core, null);

    const adopted = ledger.adopt({
      kind: 'owned', size: 64, label: 'adopt-only', generation: 4,
      coreHandle: existing.handle
    });
    assert.equal(adopted.ok, true);
    assert.equal(adopted.handle.bindNative({ data: 0x8000 }).ok, true);
    assert.equal(adopted.handle.publish().ok, true);
    assert.equal(adopted.handle.finalRelease({ referencesDropped: true }).ok, true);
    assert.equal(held(controller), 0);
    assert.equal(ledger.snapshot().liveBytes, 0);
    assert.throws(() => handles.createMediaResourceHandles({ adoptOnly: 'yes' }),
      /adoptOnly must be a boolean/);
  });

test('snapshot reports local records plus the nontransactional core scan',
  { timeout: 5000 }, () => {
    const { controller, ledger } = make(4096);
    const acquired = ledger.acquire({
      kind: 'owned', size: 100, label: 'snapshot', generation: 1
    });
    let snapshot = ledger.snapshot();
    assert.deepEqual(
      [snapshot.liveBytes, snapshot.liveCount, snapshot.acquiredCount,
        snapshot.releasedCount, snapshot.core.heldBytes],
      [100, 1, 1, 0, 100]);
    assert.equal(snapshot.exact, false);
    assert.equal(acquired.handle.finalRelease({ referencesDropped: true }).ok, true);
    snapshot = ledger.snapshot();
    assert.deepEqual(
      [snapshot.liveBytes, snapshot.liveCount, snapshot.releasedCount,
        snapshot.core.heldBytes],
      [0, 0, 1, 0]);
  });

test('invalid specs and forged handles are refused before any charge',
  { timeout: 5000 }, () => {
    const { controller, ledger } = make(4096);
    assert.equal(ledger.acquire({ kind: 'other', size: 1 }).code, 'invalid-kind');
    assert.equal(ledger.acquire({ kind: 'owned', size: -1 }).code, 'invalid-argument');
    assert.equal(ledger.acquire({
      kind: 'owned', size: 1, label: 'x'.repeat(129)
    }).code, 'invalid-argument');
    assert.equal(ledger.acquire({
      kind: 'owned', size: 1, generation: 0x80000000
    }).code, 'invalid-argument');
    assert.equal(ledger.adopt({
      kind: 'owned', size: 1, coreHandle: {}
    }).code, 'invalid-adopted-handle');
    assert.equal(ledger.acquireBatch('nope').code, 'invalid-argument');
    assert.equal(held(controller), 0);

    const acquired = ledger.acquire({ kind: 'owned', size: 16, label: 'dup' });
    assert.equal(acquired.handle.bindNative({ data: 0x6000 }).ok, true);
    assert.equal(acquired.handle.bindNative({ data: 0x7000 }).code, 'already-bound');
    assert.equal(acquired.handle.finalRelease({ referencesDropped: true }).ok, true);
    assert.equal(held(controller), 0);

    const sized = ledger.acquire({ kind: 'owned', size: 16, label: 'size-mismatch' });
    assert.equal(sized.handle.bindNative({ data: 0x7000, size: 17 }).code,
      'invalid-argument');
    assert.equal(sized.handle.finalRelease({ referencesDropped: true }).ok, true);
    assert.equal(held(controller), 0);
  });
