'use strict';

/*
 * Focused tests for public/web/player-codecpar-leases.js.
 *
 * The admission/credit assertions use the REAL owned-resource wrapper and the
 * REAL shared-media ownership core (one SharedArrayBuffer, one HELD counter).
 * The final group also executes the REAL vendored module 67669 Yi()/dn() over
 * a fake WASM memory and fake allocator. No Worker, MSE, BSF runtime, real
 * upstream, production instance or native media is involved, so this is NOT
 * end-to-end playback or global-budget proof.
 *
 * The only non-real pieces are the source-byte scanner (a fixed pointer -> byte
 * table), the fake native memory/allocator, and two documented failure shims:
 * a FLAGS word corruption that makes the real core refuse a release, and a
 * controller wrapper whose releaseOwnedResource throws. Both shims delegate
 * reservation/binding to the real core, so every asserted charge is real.
 */

const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const ROOT = path.join(__dirname, '..');
const HELPER_PATH = path.join(ROOT, 'public', 'web', 'player-codecpar-leases.js');
const core = require('../public/web/player-shared-media-ownership.js');
const owned = require('../public/web/player-owned-media-resources.js');
const codecpar = require('../public/web/player-codecpar-leases.js');

const PROOF = Object.freeze({ referencesDropped: true });

function expectCode(fn, code) {
  try {
    fn();
  } catch (error) {
    assert.equal(error.code, code);
    return error;
  }
  assert.fail('expected error code ' + code);
}

function makeFixture(options) {
  options = options || {};
  const ctl = core.create({
    capacityBytes: options.capacityBytes === undefined ? 4096 : options.capacityBytes,
    maxEntries: options.maxEntries === undefined ? 8 : options.maxEntries
  });
  const resources = owned.createOwnedResources(ctl);
  const payloads = new Map((options.payloads || []).map((entry) => [entry[0], entry[1]]));
  const measured = [];
  const manager = codecpar.createCodecparLeases({
    resources,
    measureNativePayload(pointer) {
      measured.push(pointer);
      if (options.measure) return options.measure(pointer);
      if (!payloads.has(pointer)) {
        const error = new Error('unknown fake codecpar source');
        error.code = 'EIO_CODECPAR_LEASE_INVALID';
        throw error;
      }
      return payloads.get(pointer);
    }
  });
  return { ctl, resources, manager, payloads, measured };
}

function coreEntry(ctl, index) {
  const descriptor = ctl.descriptor;
  return {
    view: new Int32Array(descriptor.sab),
    offset: descriptor.entriesOffset + index * descriptor.entryWords
  };
}

test('factory validates config, declares narrow coverage and stays inert until use',
  { timeout: 5000 }, () => {
    assert.equal(codecpar.KIND, 'eio-codecpar-leases/v1');
    assert.equal(codecpar.VERSION, 1);
    assert.equal(Object.isFrozen(codecpar.COVERAGE), true);
    assert.deepEqual(Object.assign({}, codecpar.COVERAGE), {
      streamLifetimePayload: true,
      atomicHeaderScope: false,
      muxOutputCopy: false,
      serialization: false,
      replacement: false,
      globalTotal: false,
      wallClock: false
    });

    assert.throws(() => codecpar.createCodecparLeases(), { code: 'invalid-argument' });
    assert.throws(() => codecpar.createCodecparLeases({}), { code: 'invalid-argument' });
    assert.throws(() => codecpar.createCodecparLeases({ resources: {}, measureNativePayload() {} }),
      { code: 'invalid-argument' });
    assert.throws(() => codecpar.createCodecparLeases({
      resources: { acquire() {} }, measureNativePayload: 0
    }), { code: 'invalid-argument' });

    const callable = codecpar.createCodecparLeases({
      resources: { acquire() { return { ok: false, code: 'refused', chargedBytes: 0 }; } },
      measureNativePayload: () => 0
    });
    assert.equal(typeof callable, 'function', 'the factory result is the acquirer');
    assert.equal(callable.acquire, callable);
    assert.equal(typeof callable.measured, 'function');
    assert.equal(callable.acquireMeasured, callable.measured,
      'the additive measured entry point is one function');
    assert.equal(typeof callable.snapshot, 'function');
    assert.equal(callable.snapshot().measuredAcquireCount, 0);

    const text = fs.readFileSync(HELPER_PATH, 'utf8');
    assert.equal(/eioActiveOperation|eioMseNewClaim|eioMseDropClaim/.test(text), false,
      'the helper must never consume the atomic header operation/scope');
    assert.equal(/FinalizationRegistry|WeakRef/.test(text), false,
      'credit may not be inferred from GC callbacks');
    assert.equal(/console\./.test(text), false, 'no pointer or diagnostic console logging');
  });

test('measured payload is charged in the real core before bind and credited only after proof',
  { timeout: 5000 }, () => {
    const SOURCE = 0x5100;
    const f = makeFixture({ payloads: [[SOURCE, 5]] });
    const lease = f.manager.acquire(SOURCE, 3, 'mse-codecpar-resident');

    assert.deepEqual(Array.from(Object.keys(lease)).sort(),
      ['bind', 'bytes', 'release', 'retainFailure']);
    assert.equal(Object.isFrozen(lease), true);
    assert.equal(lease.bytes, 5);
    assert.deepEqual(f.measured, [SOURCE]);

    let snapshot = f.ctl.snapshot();
    assert.deepEqual([snapshot.heldBytes, snapshot.boundBytes, snapshot.countBound,
      snapshot.ownedResourceBytes, snapshot.countOwnedResources], [5, 5, 1, 5, 1]);
    snapshot = f.resources.snapshot();
    assert.deepEqual([snapshot.liveBytes, snapshot.liveCount], [5, 1]);
    snapshot = f.manager.snapshot();
    assert.deepEqual([snapshot.liveBytes, snapshot.liveCount, snapshot.acquiredCount,
      snapshot.boundCount, snapshot.releasedCount, snapshot.exact],
      [5, 1, 1, 0, 0, false]);

    assert.equal(lease.bind({ copiedBytes: 5 }), true);
    assert.equal(lease.bind({ copiedBytes: 5 }), false, 'bind is single use');
    assert.equal(f.manager.snapshot().boundCount, 1);

    assert.equal(lease.release({ referencesDropped: false }), false);
    assert.equal(lease.release({}), false);
    assert.equal(lease.release(null), false);
    assert.equal(f.ctl.snapshot().heldBytes, 5, 'missing proof must not credit');

    assert.equal(lease.release(PROOF), true);
    snapshot = f.ctl.snapshot();
    assert.deepEqual([snapshot.heldBytes, snapshot.boundBytes, snapshot.countBound],
      [0, 0, 0]);
    snapshot = f.manager.snapshot();
    assert.deepEqual([snapshot.liveBytes, snapshot.liveCount, snapshot.releasedCount,
      snapshot.releaseFailedCount, snapshot.strandedBytes], [0, 0, 1, 0, 0]);

    assert.equal(lease.release(PROOF), false, 'release is single use');
    assert.equal(f.ctl.snapshot().heldBytes, 0, 'a replay cannot credit twice');
    assert.equal(JSON.stringify(f.manager.snapshot()).includes(String(SOURCE)), false,
      'the diagnostic snapshot must not contain the source pointer');
  });

test('malformed measurement fails before any admission, charge or pointer log',
  { timeout: 5000 }, () => {
    const SOURCE = 0x6100;
    const f = makeFixture();
    const error = expectCode(() => f.manager.acquire(SOURCE, 0, 'resident'),
      'EIO_CODECPAR_LEASE_INVALID');
    assert.equal(error.bytes, undefined);
    assert.equal(error.chargedBytes, undefined);
    assert.equal(f.resources.snapshot().acquiredCount, 0,
      'resources.acquire must not be reached on a malformed source');
    assert.equal(f.resources.snapshot().refusedCount, 0);
    let snapshot = f.ctl.snapshot();
    assert.deepEqual([snapshot.heldBytes, snapshot.countReserved, snapshot.countBound],
      [0, 0, 0]);
    snapshot = f.manager.snapshot();
    assert.deepEqual([snapshot.refusedCount, snapshot.acquiredCount, snapshot.liveBytes,
      snapshot.faultCount], [1, 0, 0, 1]);
    assert.deepEqual([snapshot.lastFault.code, snapshot.lastFault.chargedBytes],
      ['EIO_CODECPAR_LEASE_INVALID', 0]);

    const malformed = [-1, 1.5, NaN, Infinity, '5', null, undefined, true, {}];
    for (const value of malformed) {
      const g = makeFixture({ measure: () => value });
      expectCode(() => g.manager.acquire(SOURCE, 0, 'resident'),
        'EIO_CODECPAR_LEASE_INVALID');
      const after = g.ctl.snapshot();
      assert.deepEqual([after.heldBytes, after.countReserved, after.countBound], [0, 0, 0],
        'malformed measurement returned ' + String(value));
    }

    const leaky = makeFixture({
      measure() {
        throw new Error('cannot read source 0x6100 / 24832');
      }
    });
    const leakError = expectCode(() => leaky.manager.acquire(SOURCE, 0, 'resident'),
      'EIO_CODECPAR_LEASE_INVALID');
    const serialized = JSON.stringify(leaky.manager.snapshot());
    for (const needle of ['0x6100', '24832', 'cannot read source']) {
      assert.equal(String(leakError.message).includes(needle), false);
      assert.equal(serialized.includes(needle), false,
        'scanner text/pointers must not be copied into diagnostics');
    }
  });

test('caller-measured admission shares the native gate without invoking the scanner',
  { timeout: 5000 }, () => {
    const SOURCE = 0x5100;
    const f = makeFixture({ payloads: [[SOURCE, 5]] });
    assert.equal(typeof f.manager.acquireMeasured, 'function');
    assert.equal(f.manager.acquireMeasured, f.manager.measured);

    const lease = f.manager.acquireMeasured(5, 1, 'mse-measured-codecpar');
    assert.equal(lease.bytes, 5);
    assert.deepEqual(f.measured, [], 'the measured entry point must not call the scanner');
    assert.deepEqual([f.ctl.snapshot().heldBytes, f.ctl.snapshot().countBound], [5, 1]);
    let snapshot = f.manager.snapshot();
    assert.deepEqual([snapshot.acquiredCount, snapshot.measuredAcquireCount,
      snapshot.liveBytes, snapshot.liveCount], [1, 1, 5, 1]);

    assert.equal(lease.bind({ copiedBytes: 5 }), true);
    assert.equal(lease.release(PROOF), true);
    snapshot = f.manager.snapshot();
    assert.deepEqual([snapshot.releasedCount, snapshot.measuredAcquireCount,
      snapshot.liveBytes, f.ctl.snapshot().heldBytes], [1, 1, 0, 0]);

    const native = f.manager.acquire(SOURCE, 1, 'mse-native-codecpar');
    assert.equal(native.bytes, 5);
    assert.deepEqual(f.measured, [SOURCE], 'the native entry point still measures');
    assert.equal(f.manager.snapshot().measuredAcquireCount, 1,
      'a scanner-measured claim is not a caller-measured claim');
    assert.equal(native.release(PROOF), false, 'an unbound lease cannot be credited');
    native.retainFailure({ code: 'EIO_CODECPAR_CLONE_UNPROVEN' });
    assert.equal(f.ctl.snapshot().heldBytes, 5);
  });

test('caller-measured admission refuses invalid counts before any core call',
  { timeout: 5000 }, () => {
    const f = makeFixture();
    const malformed = [-1, 1.5, NaN, Infinity, -Infinity, '5', null, undefined, true, {},
      Number.MAX_SAFE_INTEGER + 1];
    let expectedRefused = 0;
    for (const value of malformed) {
      const error = expectCode(() => f.manager.measured(value, 1, 'resident'),
        'EIO_CODECPAR_LEASE_INVALID');
      expectedRefused += 1;
      assert.equal(error.bytes, undefined);
      assert.equal(error.chargedBytes, undefined);
      assert.equal(f.resources.snapshot().acquiredCount, 0,
        'resources.acquire must not be reached for ' + String(value));
      assert.equal(f.resources.snapshot().refusedCount, 0);
      const snapshot = f.manager.snapshot();
      assert.equal(snapshot.refusedCount, expectedRefused);
      assert.equal(snapshot.acquiredCount, 0);
      assert.equal(snapshot.measuredAcquireCount, 0);
      assert.equal(snapshot.lastFault.code, 'EIO_CODECPAR_LEASE_INVALID');
      assert.equal(snapshot.lastFault.bytes, 0);
      assert.equal(f.ctl.snapshot().heldBytes, 0);
      assert.equal(f.measured.length, 0,
        'invalid measured input never reaches the scanner');
    }
    assert.equal(f.ctl.snapshot().countReserved, 0);
    assert.equal(f.ctl.snapshot().countBound, 0);
  });

test('caller-measured refusal reads guarded diagnostic getters instead of escaping',
  { timeout: 5000 }, () => {
    let acquireCalls = 0;
    let measureCalls = 0;
    let next = null;
    const manager = codecpar.createCodecparLeases({
      resources: {
        acquire() {
          acquireCalls += 1;
          return next;
        }
      },
      measureNativePayload() {
        measureCalls += 1;
        throw new Error('the measured entry point must not measure');
      }
    });

    next = { ok: false };
    Object.defineProperty(next, 'code', {
      get() {
        throw null;
      }
    });
    const codeError = expectCode(() => manager.measured(5, 1, 'resident'),
      'EIO_CODECPAR_LEASE_REFUSED');
    assert.deepEqual([codeError.bytes, codeError.chargedBytes], [5, 0]);
    assert.equal(manager.snapshot().lastFault.code, 'EIO_CODECPAR_LEASE_REFUSED');

    next = { ok: false, code: 'capacity-exceeded' };
    Object.defineProperty(next, 'chargedBytes', {
      get() {
        throw new Error('chargedBytes getter exploded');
      }
    });
    const chargedError = expectCode(() => manager.measured(6, 1, 'resident'),
      'capacity-exceeded');
    assert.deepEqual([chargedError.bytes, chargedError.chargedBytes], [6, 0]);
    assert.equal(manager.snapshot().lastFault.code, 'capacity-exceeded');
    assert.equal(manager.snapshot().strandedBytes, 0);

    next = { code: 'capacity-exceeded' };
    Object.defineProperty(next, 'ok', {
      get() {
        throw new Error('ok getter exploded');
      }
    });
    const okError = expectCode(() => manager.measured(7, 1, 'resident'),
      'capacity-exceeded');
    assert.deepEqual([okError.bytes, okError.chargedBytes], [7, 0]);

    assert.equal(acquireCalls, 3);
    assert.equal(measureCalls, 0);
    const snapshot = manager.snapshot();
    assert.deepEqual([snapshot.acquiredCount, snapshot.measuredAcquireCount,
      snapshot.refusedCount, snapshot.liveBytes, snapshot.strandedBytes],
    [0, 0, 3, 0, 0]);
  });

test('caller-measured admission uses a core refusal without stranding credit',
  { timeout: 5000 }, () => {
    const f = makeFixture({ capacityBytes: 4 });
    const error = expectCode(() => f.manager.measured(5, 1, 'resident'),
      'capacity-exceeded');
    assert.deepEqual([error.bytes, error.chargedBytes], [5, 0]);
    assert.deepEqual([f.ctl.snapshot().heldBytes, f.ctl.snapshot().countBound], [0, 0]);
    assert.deepEqual([f.resources.snapshot().refusedCount,
      f.resources.snapshot().acquiredCount], [1, 0]);
    const snapshot = f.manager.snapshot();
    assert.deepEqual([snapshot.refusedCount, snapshot.acquiredCount,
      snapshot.measuredAcquireCount, snapshot.strandedBytes, snapshot.liveBytes],
    [1, 0, 0, 0, 0]);
    assert.equal(snapshot.lastFault.code, 'capacity-exceeded');
    assert.equal(f.measured.length, 0);
  });

test('caller-measured refusal surfaces a stranded charge without fabricating credit',
  { timeout: 5000 }, () => {
    let measureCalls = 0;
    const manager = codecpar.createCodecparLeases({
      resources: {
        acquire() {
          return { ok: false, code: 'bind-failed', chargedBytes: 3 };
        }
      },
      measureNativePayload() {
        measureCalls += 1;
        throw new Error('the measured entry point must not measure');
      }
    });

    const error = expectCode(() => manager.measured(5, 1, 'resident'), 'bind-failed');
    assert.deepEqual([error.bytes, error.chargedBytes], [5, 3]);
    assert.equal(measureCalls, 0);
    const snapshot = manager.snapshot();
    assert.deepEqual([snapshot.refusedCount, snapshot.acquiredCount,
      snapshot.measuredAcquireCount, snapshot.strandedBytes, snapshot.liveBytes],
    [1, 0, 0, 3, 0]);
    assert.deepEqual([snapshot.lastFault.code, snapshot.lastFault.bytes,
      snapshot.lastFault.chargedBytes], ['bind-failed', 5, 3]);
  });

test('caller-measured zero bytes admits and credits exactly one zero-size lease',
  { timeout: 5000 }, () => {
    const f = makeFixture({ capacityBytes: 64 });
    const lease = f.manager.measured(0, 1, 'resident');
    assert.equal(lease.bytes, 0);
    assert.deepEqual([f.ctl.snapshot().heldBytes, f.ctl.snapshot().countBound], [0, 1]);
    assert.equal(f.manager.snapshot().measuredAcquireCount, 1);
    assert.equal(f.measured.length, 0);

    assert.equal(lease.bind({ copiedBytes: 0 }), true, 'exact zero binds');
    assert.equal(lease.bind({ copiedBytes: 0 }), false, 'bind remains single use');
    assert.equal(lease.release(PROOF), true);
    assert.deepEqual([f.ctl.snapshot().heldBytes, f.ctl.snapshot().countBound], [0, 0]);
    assert.deepEqual([f.manager.snapshot().releasedCount,
      f.manager.snapshot().liveCount], [1, 0]);
  });

test('admission refusals are explicit, pre-charge, and leave no stranded credit',
  { timeout: 5000 }, () => {
    const SOURCE = 0x5100;

    const capacity = makeFixture({ capacityBytes: 4, payloads: [[SOURCE, 5]] });
    const over = expectCode(() => capacity.manager.acquire(SOURCE, 1, 'resident'),
      'capacity-exceeded');
    assert.deepEqual([over.bytes, over.chargedBytes], [5, 0]);
    assert.equal(capacity.ctl.snapshot().heldBytes, 0);
    assert.equal(capacity.resources.snapshot().refusedCount, 1);
    assert.deepEqual([capacity.manager.snapshot().refusedCount,
      capacity.manager.snapshot().strandedBytes,
      capacity.manager.snapshot().liveBytes], [1, 0, 0]);

    const entries = makeFixture({
      capacityBytes: 1024,
      maxEntries: 1,
      payloads: [[SOURCE, 0], [0x5200, 1]]
    });
    const first = entries.manager.acquire(SOURCE, 1, 'resident');
    expectCode(() => entries.manager.acquire(0x5200, 1, 'resident'), 'entries-exhausted');
    assert.equal(entries.ctl.snapshot().heldBytes, 0);
    assert.equal(entries.ctl.snapshot().countBound, 1);
    assert.equal(first.bind({ copiedBytes: 0 }), true);
    assert.equal(first.release(PROOF), true);
    assert.equal(entries.ctl.snapshot().countBound, 0);

    const invalid = makeFixture({ payloads: [[SOURCE, 5]] });
    expectCode(() => invalid.manager.acquire(SOURCE, -1, 'resident'), 'invalid-argument');
    expectCode(() => invalid.manager.acquire(SOURCE, 1, 42), 'invalid-argument');
    assert.equal(invalid.ctl.snapshot().heldBytes, 0);
    assert.equal(invalid.manager.snapshot().liveCount, 0);
  });

test('zero bytes is a measured payload with one admitted lease, never "nothing allocated"',
  { timeout: 5000 }, () => {
    const SOURCE = 0x5100;
    const f = makeFixture({ payloads: [[SOURCE, 0]] });
    const lease = f.manager.acquire(SOURCE, 1, 'resident');
    assert.equal(lease.bytes, 0);
    assert.equal(f.ctl.snapshot().heldBytes, 0);
    assert.equal(f.ctl.snapshot().countBound, 1,
      'zero bytes still consumes one admitted ownership record');
    assert.deepEqual([f.resources.snapshot().liveBytes, f.resources.snapshot().liveCount],
      [0, 1]);
    assert.deepEqual([f.manager.snapshot().liveBytes, f.manager.snapshot().liveCount],
      [0, 1]);

    assert.equal(lease.bind({ copiedBytes: 0 }), true, 'exact zero binds');
    assert.equal(lease.bind({ copiedBytes: 0 }), false, 'bind remains single use');
    assert.equal(lease.release(PROOF), true);
    assert.equal(f.ctl.snapshot().countBound, 0);
    assert.equal(f.manager.snapshot().releasedCount, 1);

    const retained = f.manager.acquire(SOURCE, 1, 'resident');
    assert.equal(retained.bind(null), false);
    assert.equal(retained.release(PROOF), false);
    retained.retainFailure({ code: 'EIO_CODECPAR_CLONE_UNPROVEN' });
    assert.equal(f.ctl.snapshot().heldBytes, 0);
    assert.equal(f.ctl.snapshot().countBound, 1, 'a failed zero-byte lease is still retained');
    assert.deepEqual([f.manager.snapshot().retainedCount, f.manager.snapshot().liveCount,
      f.manager.snapshot().releasedCount], [1, 1, 1]);
  });

test('bind requires one exact copiedBytes and a failed bind can never be credited',
  { timeout: 5000 }, () => {
    const SOURCE = 0x5100;
    const f = makeFixture({ payloads: [[SOURCE, 5]] });
    const lease = f.manager.acquire(SOURCE, 1, 'resident');
    assert.equal(lease.bind({ copiedBytes: 6 }), false);
    assert.equal(lease.bind({ copiedBytes: 5 }), false, 'a failed bind is not retried');
    assert.equal(lease.release(PROOF), false);
    let snapshot = f.ctl.snapshot();
    assert.deepEqual([snapshot.heldBytes, snapshot.countBound], [5, 1]);
    snapshot = f.manager.snapshot();
    assert.deepEqual([snapshot.boundCount, snapshot.retainedCount, snapshot.liveBytes,
      snapshot.faults[0].code], [0, 1, 5, 'EIO_CODECPAR_LEASE_BIND_FAILED']);

    lease.retainFailure({ code: 'EIO_CODECPAR_CLONE_UNPROVEN' });
    snapshot = f.manager.snapshot();
    assert.equal(snapshot.retainedCount, 1, 'retention is not counted twice');
    assert.equal(snapshot.faults[snapshot.faults.length - 1].code,
      'EIO_CODECPAR_CLONE_UNPROVEN');
    assert.equal(f.ctl.snapshot().heldBytes, 5);

    const g = makeFixture({ payloads: [[SOURCE, 5]] });
    const throwing = g.manager.acquire(SOURCE, 1, 'resident');
    const summary = {};
    Object.defineProperty(summary, 'copiedBytes', {
      get() {
        throw new Error('summary getter at 0x5100 failed');
      }
    });
    assert.equal(throwing.bind(summary), false);
    assert.equal(g.ctl.snapshot().heldBytes, 5);
    assert.equal(g.manager.snapshot().retainedCount, 1);
    assert.equal(JSON.stringify(g.manager.snapshot()).includes('0x5100'), false);
  });

test('reentrant bind or release is refused, poisons the lease and keeps the real charge',
  { timeout: 5000 }, () => {
    const SOURCE = 0x5100;

    const f = makeFixture({ payloads: [[SOURCE, 5]] });
    let lease = null;
    const summary = {};
    Object.defineProperty(summary, 'copiedBytes', {
      get() {
        assert.equal(lease.release(PROOF), false, 'reentrant release is refused');
        return 5;
      }
    });
    lease = f.manager.acquire(SOURCE, 1, 'resident');
    assert.equal(lease.bind(summary), false, 'the outer bind fails closed');
    assert.equal(lease.bind({ copiedBytes: 5 }), false);
    assert.equal(lease.release(PROOF), false);
    let snapshot = f.ctl.snapshot();
    assert.deepEqual([snapshot.heldBytes, snapshot.countBound], [5, 1]);
    snapshot = f.manager.snapshot();
    assert.deepEqual([snapshot.boundCount, snapshot.reentrantCount, snapshot.releasedCount],
      [0, 1, 0]);

    const g = makeFixture({ payloads: [[SOURCE, 5]] });
    const bound = g.manager.acquire(SOURCE, 1, 'resident');
    assert.equal(bound.bind({ copiedBytes: 5 }), true);
    const proof = {};
    Object.defineProperty(proof, 'referencesDropped', {
      get() {
        assert.equal(bound.bind({ copiedBytes: 5 }), false, 'reentrant bind is refused');
        return true;
      }
    });
    assert.equal(bound.release(proof), false, 'the outer release fails closed');
    assert.equal(g.ctl.snapshot().heldBytes, 5);
    assert.equal(g.ctl.snapshot().countBound, 1);
    assert.deepEqual([g.manager.snapshot().reentrantCount,
      g.manager.snapshot().releaseFailedCount, g.manager.snapshot().releasedCount],
      [1, 0, 0]);
    assert.equal(bound.release(PROOF), false, 'a poisoned lease is never credited later');
    assert.equal(g.ctl.snapshot().heldBytes, 5);

    const h = makeFixture({ payloads: [[SOURCE, 5]] });
    const second = h.manager.acquire(SOURCE, 1, 'resident');
    assert.equal(second.bind({ copiedBytes: 5 }), true);
    const nested = {};
    Object.defineProperty(nested, 'referencesDropped', {
      get() {
        assert.equal(second.release(PROOF), false);
        return true;
      }
    });
    assert.equal(second.release(nested), false);
    assert.equal(h.ctl.snapshot().heldBytes, 5);
    assert.equal(h.ctl.snapshot().countBound, 1);
  });

test('core release refusal or throw retains with exactly one release attempt and no retry credit',
  { timeout: 5000 }, () => {
    const SOURCE = 0x5100;

    // Real core refusal: corrupt the FLAGS word of the only bound record.
    const f = makeFixture({ capacityBytes: 1024, maxEntries: 1, payloads: [[SOURCE, 5]] });
    const lease = f.manager.acquire(SOURCE, 1, 'resident');
    assert.equal(lease.bind({ copiedBytes: 5 }), true);
    const entry = coreEntry(f.ctl, 0);
    const flagsOffset = entry.offset + core.LAYOUT.entry.FLAGS;
    Atomics.store(entry.view, flagsOffset, 2);
    assert.equal(lease.release(PROOF), false);
    let snapshot = f.ctl.snapshot();
    assert.deepEqual([snapshot.heldBytes, snapshot.countBound], [5, 1]);
    snapshot = f.manager.snapshot();
    assert.deepEqual([snapshot.releaseFailedCount, snapshot.releasedCount,
      snapshot.lastFault.code], [1, 0, 'corrupt-record']);
    Atomics.store(entry.view, flagsOffset, core.LAYOUT.flags.OWNED_RESOURCE);
    assert.equal(lease.release(PROOF), false, 'the refused release is not retried');
    assert.equal(f.ctl.snapshot().heldBytes, 5, 'no retry credit after the core is repaired');
    assert.equal(f.ctl.snapshot().countBound, 1);

    // Throwing core release: real reserve/bind, only the release call throws.
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
    const manager = codecpar.createCodecparLeases({
      resources,
      measureNativePayload: () => 5
    });
    const throwing = manager.acquire(SOURCE, 1, 'resident');
    assert.equal(real.snapshot().heldBytes, 5);
    assert.equal(throwing.bind({ copiedBytes: 5 }), true);
    assert.equal(throwing.release(PROOF), false);
    assert.equal(releaseCalls, 1);
    assert.equal(throwing.release(PROOF), false);
    assert.equal(releaseCalls, 1, 'exactly one core release attempt');
    assert.equal(real.snapshot().heldBytes, 5);
    snapshot = manager.snapshot();
    assert.deepEqual([snapshot.releaseFailedCount, snapshot.lastFault.code],
      [1, 'EIO_CODECPAR_LEASE_RELEASE_FAILED']);
  });

test('release guards a thrown result.ok getter or null result after one attempt',
  { timeout: 5000 }, () => {
    const SOURCE = 0x5100;
    const badResults = [
      () => {
        const result = {};
        Object.defineProperty(result, 'ok', {
          get() {
            throw new Error('ok getter for 0x5100 exploded');
          }
        });
        return result;
      },
      () => {
        const result = {};
        Object.defineProperty(result, 'ok', {
          get() {
            throw null;
          }
        });
        return result;
      },
      () => null,
      () => undefined
    ];

    for (const makeResult of badResults) {
      const real = core.create({ capacityBytes: 1024, maxEntries: 1 });
      const ownedResources = owned.createOwnedResources(real);
      let releaseCalls = 0;
      const resources = {
        acquire(bytes, label, generation) {
          const acquired = ownedResources.acquire(bytes, label, generation);
          if (!acquired.ok) return acquired;
          return {
            ok: true,
            handle: {
              release() {
                releaseCalls += 1;
                return makeResult();
              }
            }
          };
        },
        snapshot: () => ownedResources.snapshot()
      };
      const manager = codecpar.createCodecparLeases({
        resources,
        measureNativePayload: () => 5
      });
      const lease = manager.acquire(SOURCE, 1, 'resident');
      assert.equal(lease.bind({ copiedBytes: 5 }), true);
      assert.equal(real.snapshot().heldBytes, 5);

      assert.equal(lease.release(PROOF), false);
      assert.equal(releaseCalls, 1);
      assert.equal(lease.release(PROOF), false, 'the failed result read is not retried');
      assert.equal(releaseCalls, 1, 'exactly one core release attempt');
      assert.equal(real.snapshot().heldBytes, 5, 'no fabricated credit');

      const snapshot = manager.snapshot();
      assert.deepEqual([snapshot.releaseFailedCount, snapshot.releasedCount,
        snapshot.retainedCount, snapshot.liveBytes, snapshot.liveCount],
      [1, 0, 1, 5, 1]);
      assert.equal(snapshot.lastFault.code, 'EIO_CODECPAR_LEASE_RELEASE_FAILED');
      assert.equal(snapshot.lastFault.chargedBytes, 5);
      assert.equal(JSON.stringify(snapshot).includes('0x5100'), false);
    }
  });

test('retained failures stay diagnosable in a bounded scalar log with no reset',
  { timeout: 5000 }, () => {
    const SOURCE = 0x5100;
    const EMPTY = 0x5200;
    const f = makeFixture({
      capacityBytes: 4096,
      maxEntries: 40,
      payloads: [[SOURCE, 5], [EMPTY, 0]]
    });
    const lease = f.manager.acquire(SOURCE, 1, 'resident');
    assert.equal(lease.bind({ copiedBytes: 5 }), true);
    lease.retainFailure({ code: 'EIO_CODECPAR_FREE_UNPROVEN' });
    assert.equal(lease.release(PROOF), false);
    let snapshot = f.manager.snapshot();
    assert.deepEqual([snapshot.retainedCount, snapshot.releasedCount, snapshot.liveBytes,
      snapshot.liveCount, snapshot.faultCount], [1, 0, 5, 1, 1]);
    assert.equal(f.ctl.snapshot().heldBytes, 5);

    lease.retainFailure({ code: 'EIO_CODECPAR_RELEASE_UNVERIFIED' });
    snapshot = f.manager.snapshot();
    assert.equal(snapshot.retainedCount, 1, 'one lease is one retained failure');
    assert.equal(snapshot.faultCount, 2);
    assert.equal(snapshot.faults[0].code, 'EIO_CODECPAR_FREE_UNPROVEN');
    assert.equal(f.ctl.snapshot().heldBytes, 5);

    for (let index = 0; index < 20; index += 1) {
      const zero = f.manager.acquire(EMPTY, 1, 'resident');
      zero.retainFailure({ code: 'EIO_CODECPAR_CLONE_UNPROVEN' });
    }
    snapshot = f.manager.snapshot();
    assert.equal(snapshot.faults.length, codecpar.FAULT_LOG_LIMIT);
    assert.equal(snapshot.faultsEvicted, snapshot.faultCount - codecpar.FAULT_LOG_LIMIT);
    assert.deepEqual([snapshot.liveBytes, snapshot.liveCount, snapshot.releasedCount],
      [5, 21, 0]);
    for (const fault of snapshot.faults) {
      assert.deepEqual(Array.from(Object.keys(fault)).sort(),
        ['bytes', 'chargedBytes', 'code', 'label', 'sequence']);
      assert.equal(typeof fault.code, 'string');
      assert.equal(typeof fault.label, 'string');
      assert.ok(Number.isSafeInteger(fault.bytes));
      assert.ok(Number.isSafeInteger(fault.chargedBytes));
      assert.ok(Number.isSafeInteger(fault.sequence));
    }
    assert.equal(JSON.stringify(snapshot).includes(String(SOURCE)), false);
    assert.equal(JSON.stringify(snapshot).includes(String(EMPTY)), false);
    assert.equal(f.ctl.snapshot().heldBytes, 5, 'logging failures never credits core bytes');
  });

test('retainFailure poisons before reading error.code so a nested release cannot credit',
  { timeout: 5000 }, () => {
    const SOURCE = 0x5100;
    const real = core.create({ capacityBytes: 1024, maxEntries: 1 });
    let releaseCalls = 0;
    const resources = owned.createOwnedResources({
      reserve: (bytes, generation) => real.reserve(bytes, generation),
      bindOwnedResource: (token) => real.bindOwnedResource(token),
      snapshot: () => real.snapshot(),
      releaseOwnedResource(handle, proof) {
        releaseCalls += 1;
        return real.releaseOwnedResource(handle, proof);
      }
    });
    const manager = codecpar.createCodecparLeases({
      resources,
      measureNativePayload: () => 5
    });
    const lease = manager.acquire(SOURCE, 1, 'resident');
    assert.equal(lease.bind({ copiedBytes: 5 }), true);
    assert.equal(real.snapshot().heldBytes, 5);

    const error = {};
    let getterRuns = 0;
    let nestedResult = null;
    let nestedThrew = false;
    Object.defineProperty(error, 'code', {
      get() {
        getterRuns += 1;
        /* The hazard: a credit-taking release from inside the code getter.
         * The result is recorded, never asserted in here: anything thrown by
         * this getter is (correctly) swallowed into the stable fallback code. */
        try {
          nestedResult = lease.release(PROOF);
        } catch (thrown) {
          nestedThrew = true;
        }
        return 'EIO_CODECPAR_FREE_UNPROVEN';
      }
    });

    assert.equal(lease.retainFailure(error), false);
    assert.equal(getterRuns, 1);
    assert.equal(nestedThrew, false, 'the nested release must not throw');
    assert.equal(nestedResult, false, 'the nested release is refused');
    assert.equal(releaseCalls, 0, 'the poison is set before the getter can reach the core');
    let snapshot = real.snapshot();
    assert.deepEqual([snapshot.heldBytes, snapshot.countBound], [5, 1],
      'the charge stays HELD');
    snapshot = manager.snapshot();
    assert.deepEqual([snapshot.releasedCount, snapshot.retainedCount, snapshot.boundCount,
      snapshot.liveBytes, snapshot.liveCount], [0, 1, 1, 5, 1]);
    assert.equal(snapshot.lastFault.code, 'EIO_CODECPAR_FREE_UNPROVEN',
      'a meaningful code is normalized only after the failure is irrevocable');
    assert.equal(snapshot.faults[0].chargedBytes, 5,
      'the first retention reports the bytes it stranded');

    assert.equal(lease.release(PROOF), false, 'no later credit on the poisoned lease');
    assert.equal(lease.bind({ copiedBytes: 5 }), false, 'the poisoned lease cannot bind');
    assert.equal(lease.retainFailure({ code: 'EIO_CODECPAR_CLONE_UNPROVEN' }), false);
    snapshot = manager.snapshot();
    assert.equal(snapshot.retainedCount, 1, 'one lease is one retained failure');
    assert.equal(releaseCalls, 0, 'no core release attempt for this lease');
    assert.deepEqual([real.snapshot().heldBytes, real.snapshot().countBound], [5, 1]);
  });

test('retainFailure guards a throwing code getter with a stable fallback',
  { timeout: 5000 }, () => {
    const SOURCE = 0x5100;
    const throwing = {};
    Object.defineProperty(throwing, 'code', {
      get() {
        throw new Error('code getter for 0x5100 exploded');
      }
    });

    const f = makeFixture({ payloads: [[SOURCE, 5]] });
    const lease = f.manager.acquire(SOURCE, 1, 'resident');
    assert.equal(lease.bind({ copiedBytes: 5 }), true);
    let retained;
    assert.doesNotThrow(() => {
      retained = lease.retainFailure(throwing);
    }, 'a throwing getter must not escape retainFailure');
    assert.equal(retained, false);
    let snapshot = f.ctl.snapshot();
    assert.deepEqual([snapshot.heldBytes, snapshot.countBound], [5, 1]);
    snapshot = f.manager.snapshot();
    assert.deepEqual([snapshot.releasedCount, snapshot.retainedCount, snapshot.liveBytes],
      [0, 1, 5]);
    assert.equal(snapshot.lastFault.code, 'EIO_CODECPAR_CLONE_UNPROVEN',
      'the stable fallback code is logged instead');
    assert.equal(JSON.stringify(snapshot).includes('0x5100'), false,
      'getter text and pointers are never copied into diagnostics');
    assert.equal(lease.release(PROOF), false, 'no subsequent credit');
    assert.equal(f.ctl.snapshot().heldBytes, 5);

    /* Any thrown value, including the null sentinel, is guarded the same way. */
    const g = makeFixture({ payloads: [[SOURCE, 5]] });
    const nullThrowingLease = g.manager.acquire(SOURCE, 1, 'resident');
    assert.equal(nullThrowingLease.bind({ copiedBytes: 5 }), true);
    const nullCode = {};
    Object.defineProperty(nullCode, 'code', {
      get() {
        throw null;
      }
    });
    assert.equal(nullThrowingLease.retainFailure(nullCode), false);
    assert.equal(g.ctl.snapshot().heldBytes, 5);
    assert.equal(g.manager.snapshot().lastFault.code, 'EIO_CODECPAR_CLONE_UNPROVEN');

    /* An already credited lease keeps its credit and still logs guarded. */
    const h = makeFixture({ payloads: [[SOURCE, 5]] });
    const credited = h.manager.acquire(SOURCE, 1, 'resident');
    assert.equal(credited.bind({ copiedBytes: 5 }), true);
    assert.equal(credited.release(PROOF), true);
    assert.equal(h.ctl.snapshot().heldBytes, 0);
    const releasedCode = {};
    Object.defineProperty(releasedCode, 'code', {
      get() {
        throw new Error('released lease code getter exploded');
      }
    });
    let afterRelease;
    assert.doesNotThrow(() => {
      afterRelease = credited.retainFailure(releasedCode);
    });
    assert.equal(afterRelease, false);
    assert.equal(h.ctl.snapshot().heldBytes, 0, 'a credited lease is never un-credited');
    snapshot = h.manager.snapshot();
    assert.deepEqual([snapshot.releasedCount, snapshot.retainedCount], [1, 0]);
    assert.equal(snapshot.lastFault.code, 'EIO_CODECPAR_RELEASE_UNVERIFIED',
      'the released path also uses a stable fallback');
  });

test('non-Error throw sentinels still fail measurement, bind, proof and core release',
  { timeout: 5000 }, () => {
    const SOURCE = 0x5100;

    for (const sentinel of [null, undefined]) {
      const f = makeFixture({
        measure() {
          throw sentinel;
        }
      });
      expectCode(() => f.manager.acquire(SOURCE, 1, 'resident'),
        'EIO_CODECPAR_LEASE_INVALID');
      assert.equal(f.resources.snapshot().acquiredCount, 0,
        'a thrown measurement is never admitted');
      assert.equal(f.ctl.snapshot().heldBytes, 0);
      assert.equal(f.manager.snapshot().lastFault.code, 'EIO_CODECPAR_LEASE_INVALID');
    }

    const g = makeFixture({ payloads: [[SOURCE, 5]] });
    const lease = g.manager.acquire(SOURCE, 1, 'resident');
    for (const sentinel of [null, undefined]) {
      const summary = {};
      Object.defineProperty(summary, 'copiedBytes', {
        get() {
          throw sentinel;
        }
      });
      assert.equal(lease.bind(summary), false,
        'a thrown summary read cannot look like a completed bind');
    }
    let snapshot = g.manager.snapshot();
    assert.deepEqual([snapshot.boundCount, snapshot.retainedCount], [0, 1]);
    assert.deepEqual([g.ctl.snapshot().heldBytes, g.ctl.snapshot().countBound], [5, 1]);
    assert.equal(lease.release(PROOF), false);
    assert.equal(g.ctl.snapshot().heldBytes, 5);

    const h = makeFixture({ payloads: [[SOURCE, 5]] });
    const proofLease = h.manager.acquire(SOURCE, 1, 'resident');
    assert.equal(proofLease.bind({ copiedBytes: 5 }), true);
    const proof = {};
    Object.defineProperty(proof, 'referencesDropped', {
      get() {
        throw null;
      }
    });
    assert.equal(proofLease.release(proof), false, 'a thrown proof proves nothing');
    assert.deepEqual([h.ctl.snapshot().heldBytes, h.ctl.snapshot().countBound], [5, 1]);
    snapshot = h.manager.snapshot();
    assert.deepEqual([snapshot.releasedCount, snapshot.lastFault.code],
      [0, 'EIO_CODECPAR_LEASE_PROOF_INVALID']);
    assert.equal(proofLease.release(PROOF), true, 'the untouched lease may still release');
    assert.equal(h.ctl.snapshot().heldBytes, 0);

    for (const sentinel of [null, undefined]) {
      const real = core.create({ capacityBytes: 1024, maxEntries: 1 });
      let releaseCalls = 0;
      const resources = owned.createOwnedResources({
        reserve: (bytes, generation) => real.reserve(bytes, generation),
        bindOwnedResource: (token) => real.bindOwnedResource(token),
        snapshot: () => real.snapshot(),
        releaseOwnedResource() {
          releaseCalls += 1;
          throw sentinel;
        }
      });
      const manager = codecpar.createCodecparLeases({
        resources,
        measureNativePayload: () => 5
      });
      const held = manager.acquire(SOURCE, 1, 'resident');
      assert.equal(held.bind({ copiedBytes: 5 }), true);
      assert.equal(held.release(PROOF), false);
      assert.equal(releaseCalls, 1);
      assert.equal(held.release(PROOF), false);
      assert.equal(releaseCalls, 1, 'exactly one core release attempt');
      assert.equal(real.snapshot().heldBytes, 5);
      snapshot = manager.snapshot();
      assert.deepEqual([snapshot.releaseFailedCount, snapshot.lastFault.code],
        [1, 'EIO_CODECPAR_LEASE_RELEASE_FAILED']);
    }
  });

/* -------------------------------------------------------------------------
 * Real module 67669 over fake WASM memory: lease compatibility only.
 * ---------------------------------------------------------------------- */

const AVPLAYER_PATH = path.join(ROOT, 'public', 'vendor', 'web', 'libmedia', 'avplayer.js');
const avplayerSource = fs.readFileSync(AVPLAYER_PATH, 'utf8');

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
    reads: [],
    writes: [],
    read(pointer, size) {
      if (!Number.isInteger(pointer) || pointer < 0 || !Number.isInteger(size) ||
          size < 0 || pointer + size > NATIVE_MEMORY_SIZE) {
        throw new RangeError('fake native memory read out of range');
      }
      this.reads.push({ pointer, size });
    },
    write(pointer, size) {
      if (!Number.isInteger(pointer) || pointer < 0 || !Number.isInteger(size) ||
          size < 0 || pointer + size > NATIVE_MEMORY_SIZE) {
        throw new RangeError('fake native memory write out of range');
      }
      this.writes.push({ pointer, size });
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

function setupNativeSource(memory, options) {
  options = options || {};
  const extra = options.extra === undefined ? [1, 2, 3] : options.extra;
  const sides = options.sides === undefined ? [{ bytes: [4, 5], type: 2 }] : options.sides;
  memory.setI32(NATIVE.src, 7);
  memory.setI32(NATIVE.src + 4, 8);
  memory.setI8(NATIVE.src + 8, 9);
  memory.setI32(NATIVE.src + 28, 11);
  memory.setBigInt64(NATIVE.src + 32, 12n);
  memory.setI32(NATIVE.src + 40, 13);
  memory.setI32(NATIVE.src + 136, 14);
  const extraPointer = options.extradataPointer === undefined
    ? (extra ? NATIVE.extra : 0) : options.extradataPointer;
  const extraSize = options.extradataSize === undefined
    ? (extra ? extra.length : 0) : options.extradataSize;
  memory.setU32(NATIVE.src + 12, extraPointer);
  memory.setI32(NATIVE.src + 16, extraSize);
  if (extra && extra.length) buffer_poke(memory, NATIVE.extra, extra);
  const sidePointer = options.sidePointer === undefined
    ? (sides.length ? NATIVE.sideTable : 0) : options.sidePointer;
  const sideCount = options.sideCount === undefined ? sides.length : options.sideCount;
  memory.setU32(NATIVE.src + 20, sidePointer);
  memory.setI32(NATIVE.src + 24, sideCount);
  sides.forEach((side, index) => {
    const entry = NATIVE.sideTable + 12 * index;
    const payload = NATIVE.sidePayload + index * 0x100;
    memory.setU32(entry, payload);
    memory.setU32(entry + 4, side.bytes.length);
    memory.setI32(entry + 8, side.type);
    if (side.bytes.length) buffer_poke(memory, payload, side.bytes);
  });
  const bytes = (extra ? extra.length : 0) +
    sides.reduce((sum, side) => sum + side.bytes.length, 0);
  return { extra, sides, bytes, extraPointer, extraSize };
}

function buffer_poke(memory, pointer, bytes) {
  memory.buffer.set(bytes, pointer);
}

test('real module 67669 accepts the lease, charges before copy and credits after its dn free',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    const api = loadModule67669(memory);
    const fixture = setupNativeSource(memory);
    assert.ok(fixture.bytes > 0);

    const real = core.create({ capacityBytes: 4096, maxEntries: 8 });
    const releaseOrder = [];
    const resources = owned.createOwnedResources({
      reserve: (bytes, generation) => real.reserve(bytes, generation),
      bindOwnedResource: (token) => real.bindOwnedResource(token),
      snapshot: () => real.snapshot(),
      releaseOwnedResource(handle, proof) {
        releaseOrder.push(memory.frees.length);
        return real.releaseOwnedResource(handle, proof);
      }
    });
    const manager = codecpar.createCodecparLeases({
      resources,
      measureNativePayload: (pointer) => api.eioPayloadBytes(pointer)
    });

    const lease = manager.acquire(NATIVE.src, 1, 'mse-codecpar-resident');
    assert.equal(lease.bytes, fixture.bytes);
    assert.equal(memory.allocs.length, 0, 'admission precedes every native allocation');
    assert.deepEqual([real.snapshot().heldBytes, real.snapshot().countBound],
      [fixture.bytes, 1]);

    api.Yi(NATIVE.dest, NATIVE.src, lease);
    assert.equal(real.snapshot().heldBytes, fixture.bytes,
      'the charge survives the native copy');
    assert.equal(manager.snapshot().boundCount, 1);
    assert.ok(memory.allocs.length > 0);
    assert.ok(memory.getU32(NATIVE.dest + 12) > 0);
    assert.equal(memory.getI32(NATIVE.dest + 16), fixture.extra.length);

    api.dn(NATIVE.dest);
    assert.deepEqual([real.snapshot().heldBytes, real.snapshot().countBound], [0, 0]);
    assert.deepEqual([manager.snapshot().releasedCount, manager.snapshot().liveBytes],
      [1, 0]);
    assert.equal(releaseOrder.length, 1);
    assert.ok(releaseOrder[0] > 0, 'release is requested only after the native free');
    assert.ok(memory.frees.includes(NATIVE.dest));
  });

test('real module 67669 zero-byte lease still admits, binds and releases a zero-size clone',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    const api = loadModule67669(memory);
    const fixture = setupNativeSource(memory, {
      extra: [],
      sides: [],
      extradataPointer: NATIVE.extra,
      extradataSize: 0
    });
    assert.equal(fixture.bytes, 0);

    const ctl = core.create({ capacityBytes: 4096, maxEntries: 8 });
    const resources = owned.createOwnedResources(ctl);
    const manager = codecpar.createCodecparLeases({
      resources,
      measureNativePayload: (pointer) => api.eioPayloadBytes(pointer)
    });
    const lease = manager.acquire(NATIVE.src, 1, 'mse-codecpar-resident');
    assert.equal(lease.bytes, 0);
    assert.deepEqual([ctl.snapshot().heldBytes, ctl.snapshot().countBound], [0, 1],
      'zero bytes is a measured payload with one admitted record');

    api.Yi(NATIVE.dest, NATIVE.src, lease);
    assert.equal(memory.allocs.length, 1,
      'the original zero-length extradata allocation still happened');
    assert.equal(memory.allocs[0].size, 0);
    assert.equal(ctl.snapshot().heldBytes, 0);
    assert.equal(manager.snapshot().boundCount, 1);
    assert.equal(manager.snapshot().releasedCount, 0);

    api.dn(NATIVE.dest);
    assert.deepEqual([ctl.snapshot().heldBytes, ctl.snapshot().countBound], [0, 0]);
    assert.deepEqual([manager.snapshot().releasedCount, manager.snapshot().liveCount],
      [1, 0]);
  });

test('real module 67669 allocation failure retains the lease and never frees or credits early',
  { timeout: 5000 }, () => {
    const memory = makeNativeMemory();
    memory.failedAllocNumbers.add(1);
    const api = loadModule67669(memory);
    const fixture = setupNativeSource(memory);
    assert.ok(fixture.bytes > 0);

    const ctl = core.create({ capacityBytes: 4096, maxEntries: 8 });
    const resources = owned.createOwnedResources(ctl);
    const manager = codecpar.createCodecparLeases({
      resources,
      measureNativePayload: (pointer) => api.eioPayloadBytes(pointer)
    });
    const lease = manager.acquire(NATIVE.src, 1, 'mse-codecpar-resident');
    assert.equal(ctl.snapshot().heldBytes, fixture.bytes);

    expectCode(() => api.Yi(NATIVE.dest, NATIVE.src, lease), 'EIO_CODECPAR_ALLOC_FAILED');
    assert.equal(ctl.snapshot().heldBytes, fixture.bytes, 'failure retains the real charge');
    assert.equal(memory.frees.length, 0);
    assert.equal(manager.snapshot().retainedCount, 1);
    assert.equal(manager.snapshot().boundCount, 0);
    assert.equal(manager.snapshot().faults[0].code, 'EIO_CODECPAR_CLONE_UNPROVEN');

    expectCode(() => api.dn(NATIVE.dest), 'EIO_CODECPAR_CLONE_UNPROVEN');
    assert.equal(memory.frees.length, 0, 'a retained clone is not freed or credited');
    assert.equal(ctl.snapshot().heldBytes, fixture.bytes);
    assert.equal(lease.release(PROOF), false);
    assert.equal(ctl.snapshot().heldBytes, fixture.bytes);
    assert.equal(manager.snapshot().releasedCount, 0);
  });
