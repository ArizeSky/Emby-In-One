'use strict';

/*
 * Step 6 focused coverage: codecpar leases optionally adopt the common
 * media-resource Handle while the real shared-media core remains the only
 * byte ledger and the only releaser.
 *
 * The assertions use the REAL ownership core, the REAL owned-resource wrapper,
 * the REAL common Handle ledger and the REAL codecpar lease helper. The only
 * shim is the throwing release controller in the last test, which delegates
 * reserve/bind to the real core and throws only on release.
 */

const { test } = require('node:test');
const assert = require('node:assert/strict');

const core = require('../web-src/player-shared-media-ownership.js');
const owned = require('../web-src/player-owned-media-resources.js');
const handlesModule = require('../web-src/player-media-resource-handles.js');
const codecpar = require('../web-src/player-codecpar-leases.js');

const PROOF = Object.freeze({ referencesDropped: true });

function makeFixture(options) {
  options = options || {};
  const ctl = core.create({
    capacityBytes: options.capacityBytes === undefined ? 1024 : options.capacityBytes,
    maxEntries: options.maxEntries === undefined ? 8 : options.maxEntries
  });
  const resources = owned.createOwnedResources(ctl);
  const handles = options.handles === undefined ?
    handlesModule.createMediaResourceHandles({ adoptOnly: true }) :
    options.handles;
  const manager = codecpar.createCodecparLeases({
    resources,
    measureNativePayload: () => 5,
    handles
  });
  return { ctl, resources, handles, manager };
}

test('optional handles config is validated but may be omitted or null',
  { timeout: 5000 }, () => {
    const ctl = core.create({ capacityBytes: 1024, maxEntries: 4 });
    const resources = owned.createOwnedResources(ctl);
    const measureNativePayload = () => 5;
    assert.throws(() => codecpar.createCodecparLeases({
      resources, measureNativePayload, handles: {}
    }), { code: 'invalid-argument' });
    assert.throws(() => codecpar.createCodecparLeases({
      resources, measureNativePayload, handles: { adopt: 1 }
    }), { code: 'invalid-argument' });
    assert.throws(() => codecpar.createCodecparLeases({
      resources, measureNativePayload, handles: 0
    }), { code: 'invalid-argument' });
    assert.doesNotThrow(() => codecpar.createCodecparLeases({
      resources, measureNativePayload, handles: null
    }));
  });

test('a successful adoption keeps the core charge and routes final release through the common Handle',
  { timeout: 5000 }, () => {
    const f = makeFixture();
    const lease = f.manager.acquireMeasured(5, 1, 'mse-stream-codecpar');
    assert.equal(lease.bytes, 5);
    assert.equal(f.ctl.snapshot().heldBytes, 5);
    assert.equal(f.ctl.snapshot().countBound, 1);

    let snapshot = f.handles.snapshot();
    assert.deepEqual([snapshot.liveBytes, snapshot.liveCount, snapshot.acquiredCount],
      [5, 1, 1]);
    snapshot = f.manager.snapshot();
    assert.deepEqual([snapshot.handleAdoptions, snapshot.handleAdoptFallbacks], [1, 0]);

    assert.equal(lease.bind({ copiedBytes: 5 }), true);
    assert.equal(lease.release(PROOF), true);
    assert.deepEqual([f.ctl.snapshot().heldBytes, f.ctl.snapshot().countBound], [0, 0]);
    snapshot = f.handles.snapshot();
    assert.deepEqual([snapshot.liveBytes, snapshot.liveCount, snapshot.releasedCount],
      [0, 0, 1]);
    snapshot = f.manager.snapshot();
    assert.deepEqual([snapshot.releasedCount, snapshot.liveBytes, snapshot.handleAdoptions],
      [1, 0, 1]);
    assert.equal(lease.release(PROOF), false, 'release remains single use');
    assert.equal(f.ctl.snapshot().heldBytes, 0, 'no second credit');
  });

test('adopt refusal or throw falls back to the direct core handle without losing the charge',
  { timeout: 5000 }, () => {
    const cases = [
      { adopt() { return { ok: false, code: 'adopt-refused' }; } },
      { adopt() { throw new Error('adopt exploded'); } },
      { adopt() { return { ok: true, handle: {} }; } }
    ];
    for (const handles of cases) {
      const f = makeFixture({ handles });
      const lease = f.manager.acquireMeasured(5, 1, 'mse-stream-codecpar');
      assert.equal(f.ctl.snapshot().heldBytes, 5);
      let snapshot = f.manager.snapshot();
      assert.deepEqual([snapshot.handleAdoptions, snapshot.handleAdoptFallbacks], [0, 1]);
      assert.equal(snapshot.lastFault.code, 'EIO_CODECPAR_LEASE_ADOPT_FALLBACK');
      assert.equal(lease.bind({ copiedBytes: 5 }), true);
      assert.equal(lease.release(PROOF), true);
      assert.equal(f.ctl.snapshot().heldBytes, 0);
      assert.equal(f.manager.snapshot().releasedCount, 1);
    }
  });

test('a failed lease retains both the core charge and the adopted common Handle',
  { timeout: 5000 }, () => {
    const f = makeFixture();
    const lease = f.manager.acquireMeasured(5, 1, 'mse-stream-codecpar');
    assert.equal(lease.bind({ copiedBytes: 5 }), true);
    assert.equal(lease.retainFailure({ code: 'EIO_CODECPAR_CLONE_UNPROVEN' }), false);
    assert.deepEqual([f.ctl.snapshot().heldBytes, f.ctl.snapshot().countBound], [5, 1]);
    assert.deepEqual([f.handles.snapshot().liveBytes, f.handles.snapshot().liveCount], [5, 1]);
    assert.equal(f.manager.snapshot().retainedCount, 1);
    assert.equal(lease.release(PROOF), false);
    assert.deepEqual([f.ctl.snapshot().heldBytes, f.ctl.snapshot().countBound], [5, 1]);
    assert.deepEqual([f.handles.snapshot().liveBytes, f.handles.snapshot().liveCount], [5, 1]);
  });

test('a core release refusal retains the adopted Handle and is never retried',
  { timeout: 5000 }, () => {
    const real = core.create({ capacityBytes: 1024, maxEntries: 1 });
    let releaseCalls = 0;
    const resources = owned.createOwnedResources({
      reserve: (bytes, generation) => real.reserve(bytes, generation),
      bindOwnedResource: (token) => real.bindOwnedResource(token),
      snapshot: () => real.snapshot(),
      releaseOwnedResource() {
        releaseCalls += 1;
        throw new Error('injected core release throw');
      }
    });
    const handles = handlesModule.createMediaResourceHandles({ adoptOnly: true });
    const manager = codecpar.createCodecparLeases({
      resources,
      measureNativePayload: () => 5,
      handles
    });
    const lease = manager.acquireMeasured(5, 1, 'mse-stream-codecpar');
    assert.equal(lease.bind({ copiedBytes: 5 }), true);
    assert.equal(lease.release(PROOF), false);
    assert.equal(releaseCalls, 1);
    assert.equal(real.snapshot().heldBytes, 5);
    assert.equal(handles.snapshot().liveBytes, 5);
    assert.equal(handles.snapshot().liveCount, 1);
    assert.equal(manager.snapshot().releaseFailedCount, 1);
    assert.equal(lease.release(PROOF), false);
    assert.equal(releaseCalls, 1, 'exactly one core release attempt');
    assert.equal(real.snapshot().heldBytes, 5);
    assert.equal(handles.snapshot().liveCount, 1);
  });
