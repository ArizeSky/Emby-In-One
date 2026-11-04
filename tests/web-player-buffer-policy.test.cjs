'use strict';
/* Unit coverage for the pure buffer policy in
 * web-src/player-buffer-policy.js.
 *
 * These tests exercise the real module through require() (plus one sandbox load
 * to prove it needs no browser globals). They prove only the pure policy and
 * accounting contract: the module is not wired into the player adapter, and no
 * test here claims that a real pipeline reserves, commits, releases, pauses or
 * seeks anything.
 */
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const policy = require('../web-src/player-buffer-policy.js');
const FILE = path.join(__dirname, '../web-src/player-buffer-policy.js');
const MAX_SAFE = Number.MAX_SAFE_INTEGER;

function empty(generation = 0, maxBytes = policy.DEFAULT_MAX_BYTES) {
  return {
    generation,
    maxBytes,
    reservedBytes: 0,
    heldBytes: 0,
    totalBytes: 0,
    entryCount: 0
  };
}

function snapshotOf(budget) {
  return budget.snapshot();
}

test('module shape: UMD exports the frozen constants and factories', () => {
  assert.equal(policy.DEFAULT_MAX_BYTES, 300000000);
  assert.equal(policy.DEFAULT_MAX_FORWARD_SECONDS, 120);
  assert.equal(typeof policy.createBudget, 'function');
  assert.equal(typeof policy.createFillGate, 'function');
  assert.equal(Object.isFrozen(policy), true);
});

test('UMD: loads in a sandbox without DOM, timers or network and exposes window.EIOBufferPolicy', () => {
  const sandbox = {};
  sandbox.window = sandbox;
  vm.createContext(sandbox);
  vm.runInContext(fs.readFileSync(FILE, 'utf8'), sandbox, { filename: FILE });
  assert.equal(typeof sandbox.EIOBufferPolicy.createBudget, 'function');
  assert.equal(typeof sandbox.EIOBufferPolicy.createFillGate, 'function');
  assert.equal(sandbox.EIOBufferPolicy.createBudget().snapshot().maxBytes, 300000000);
});

test('module source is inert: no DOM, timer, network or engine control references', () => {
  const source = fs.readFileSync(FILE, 'utf8');
  for (const needle of [
    'document.',
    'setTimeout',
    'setInterval',
    'fetch(',
    'XMLHttpRequest',
    'pause(',
    'currentTime',
    'playbackRate'
  ]) {
    assert.equal(source.includes(needle), false, `source must not reference ${needle}`);
  }
});

test('budget defaults: an empty frozen snapshot on every call', () => {
  const budget = policy.createBudget();
  const first = snapshotOf(budget);
  const second = snapshotOf(budget);
  assert.deepEqual(first, empty());
  assert.deepEqual(second, empty());
  assert.notEqual(first, second);
  assert.equal(Object.isFrozen(first), true);
  assert.deepEqual(Object.keys(first).sort(), [
    'entryCount',
    'generation',
    'heldBytes',
    'maxBytes',
    'reservedBytes',
    'totalBytes'
  ]);
});

test('budget reserve: valid keys are unique and counted immediately', () => {
  const budget = policy.createBudget();
  assert.equal(budget.reserve('seg-a', 1000, 0), true);
  assert.deepEqual(snapshotOf(budget), {
    generation: 0,
    maxBytes: policy.DEFAULT_MAX_BYTES,
    reservedBytes: 1000,
    heldBytes: 0,
    totalBytes: 1000,
    entryCount: 1
  });
  // A key owns its reservation until release; reserving it again is refused.
  assert.equal(budget.reserve('seg-a', 1, 0), false);
  assert.deepEqual(snapshotOf(budget).reservedBytes, 1000);
  assert.equal(budget.reserve('seg-b', 2000, 0), true);
  assert.equal(snapshotOf(budget).reservedBytes, 3000);
  assert.equal(snapshotOf(budget).entryCount, 2);
});

test('budget reserve: invalid bytes, keys and generations leave state unchanged', () => {
  const budget = policy.createBudget();
  const badBytes = [-1, 1.5, NaN, Infinity, -Infinity, '10', null, undefined, {}, MAX_SAFE + 1];
  for (const bytes of badBytes) {
    assert.equal(budget.reserve('k', bytes, 0), false, `bytes=${String(bytes)}`);
  }
  for (const key of ['', 0, null, undefined, {}, [], 'x'.repeat(201)]) {
    assert.equal(budget.reserve(key, 10, 0), false, `key=${String(key)}`);
  }
  assert.equal(budget.reserve('k', 10, undefined), false);
  assert.equal(budget.reserve('k', 10, 1), false);
  assert.equal(budget.reserve('k', 10, -1), false);
  assert.equal(budget.reserve('k', 10, 0.5), false);
  assert.deepEqual(snapshotOf(budget), empty());

  // Exactly 200 UTF-16 code units is the inclusive key bound.
  assert.equal(budget.reserve('x'.repeat(200), 0, 0), true);
  assert.equal(budget.reserve('x'.repeat(201), 0, 0), false);
});

test('budget reserve: the cap is inclusive and an over-limit request changes nothing', () => {
  const budget = policy.createBudget({ maxBytes: 100 });
  assert.equal(budget.reserve('a', 60, 0), true);
  assert.equal(budget.reserve('b', 41, 0), false);
  assert.deepEqual(snapshotOf(budget).reservedBytes, 60);
  assert.equal(snapshotOf(budget).entryCount, 1);
  assert.equal(budget.reserve('b', 40, 0), true);
  assert.deepEqual(snapshotOf(budget), {
    generation: 0,
    maxBytes: 100,
    reservedBytes: 100,
    heldBytes: 0,
    totalBytes: 100,
    entryCount: 2
  });
  // Zero bytes still fits; any positive byte is now over the cap.
  assert.equal(budget.reserve('c', 0, 0), true);
  assert.equal(budget.reserve('d', 1, 0), false);
  assert.equal(snapshotOf(budget).totalBytes, 100);
});

test('budget reserve: a huge request cannot overflow the remaining-space check', () => {
  const budget = policy.createBudget({ maxBytes: MAX_SAFE });
  assert.equal(budget.reserve('huge', MAX_SAFE, 0), true);
  assert.equal(snapshotOf(budget).totalBytes, MAX_SAFE);
  assert.equal(budget.reserve('one-more', 1, 0), false);
  assert.equal(budget.reserve('also-huge', MAX_SAFE, 0), false);
  assert.equal(snapshotOf(budget).entryCount, 1);
  assert.equal(snapshotOf(budget).totalBytes, MAX_SAFE);

  const tiny = policy.createBudget({ maxBytes: 0 });
  assert.equal(tiny.reserve('zero', 0, 0), true);
  assert.equal(tiny.reserve('nope', 1, 0), false);
  assert.equal(tiny.snapshot().entryCount, 1);
});

test('budget commit: reservation becomes held bytes and a smaller actual size refunds', () => {
  const budget = policy.createBudget();
  assert.equal(budget.reserve('a', 1000, 0), true);
  assert.equal(budget.reserve('b', 500, 0), true);
  assert.equal(budget.commit('a', 400, 0), true);
  assert.deepEqual(snapshotOf(budget), {
    generation: 0,
    maxBytes: policy.DEFAULT_MAX_BYTES,
    reservedBytes: 500,
    heldBytes: 400,
    totalBytes: 900,
    entryCount: 2
  });
  assert.equal(budget.commit('b', 500, 0), true);
  assert.deepEqual(snapshotOf(budget), {
    generation: 0,
    maxBytes: policy.DEFAULT_MAX_BYTES,
    reservedBytes: 0,
    heldBytes: 900,
    totalBytes: 900,
    entryCount: 2
  });
});

test('budget commit: zero-sized reservations commit once and stay owned', () => {
  const budget = policy.createBudget({ maxBytes: 10 });
  assert.equal(budget.reserve('zero', 0, 0), true);
  assert.equal(budget.commit('zero', 0, 0), true);
  assert.deepEqual(snapshotOf(budget), {
    generation: 0,
    maxBytes: 10,
    reservedBytes: 0,
    heldBytes: 0,
    totalBytes: 0,
    entryCount: 1
  });
  assert.equal(budget.commit('zero', 1, 0), false);
  assert.equal(budget.reserve('zero', 1, 0), false);
  assert.equal(budget.release('zero', 0), true);
  assert.deepEqual(snapshotOf(budget), empty(0, 10));
});

test('budget commit: larger-than-reserved, invalid, unknown and repeat commits change nothing', () => {
  const budget = policy.createBudget();
  assert.equal(budget.reserve('k', 500, 0), true);
  const before = snapshotOf(budget);
  assert.equal(budget.commit('k', 501, 0), false);
  assert.equal(budget.commit('k', -1, 0), false);
  assert.equal(budget.commit('k', 1.5, 0), false);
  assert.equal(budget.commit('k', NaN, 0), false);
  assert.equal(budget.commit('k', '1', 0), false);
  assert.equal(budget.commit('unknown', 1, 0), false);
  assert.equal(budget.commit('k', 500, 1), false);
  assert.deepEqual(snapshotOf(budget), before);
  assert.equal(budget.commit('k', 500, 0), true);
  const committed = snapshotOf(budget);
  assert.deepEqual(committed, {
    generation: 0,
    maxBytes: policy.DEFAULT_MAX_BYTES,
    reservedBytes: 0,
    heldBytes: 500,
    totalBytes: 500,
    entryCount: 1
  });
  // A reservation is credited at most once.
  assert.equal(budget.commit('k', 0, 0), false);
  assert.deepEqual(snapshotOf(budget), committed);
});

test('budget release: clears reservation or held bytes, is unknown-safe and never double-credits', () => {
  const budget = policy.createBudget();
  assert.equal(budget.reserve('reserved', 300, 0), true);
  assert.equal(budget.reserve('held', 200, 0), true);
  assert.equal(budget.commit('held', 200, 0), true);
  assert.equal(snapshotOf(budget).totalBytes, 500);

  assert.equal(budget.release('reserved', 0), true);
  assert.equal(budget.release('reserved', 0), false);
  assert.deepEqual(snapshotOf(budget).totalBytes, 200);
  assert.equal(budget.release('unknown', 0), false);
  assert.equal(budget.release('', 0), false);
  assert.equal(budget.release('held', 1), false);
  assert.equal(snapshotOf(budget).totalBytes, 200);

  assert.equal(budget.release('held', 0), true);
  assert.deepEqual(snapshotOf(budget), empty());
  assert.equal(budget.release('held', 0), false);
  assert.deepEqual(snapshotOf(budget), empty());
});

test('budget generations: stale calls are ignored without touching the current ledger', () => {
  const budget = policy.createBudget({ generation: 7 });
  assert.equal(budget.reserve('old', 10, 6), false);
  assert.equal(budget.reserve('old', 10, 0), false);
  assert.deepEqual(snapshotOf(budget), empty(7));
  assert.equal(budget.reserve('k', 10, 7), true);
  assert.equal(budget.commit('k', 10, 6), false);
  assert.equal(budget.release('k', 6), false);
  assert.deepEqual(snapshotOf(budget), {
    generation: 7,
    maxBytes: policy.DEFAULT_MAX_BYTES,
    reservedBytes: 10,
    heldBytes: 0,
    totalBytes: 10,
    entryCount: 1
  });

  budget.reset(8);
  assert.deepEqual(snapshotOf(budget), empty(8));
  assert.equal(budget.reserve('late-old', 10, 7), false);
  assert.deepEqual(snapshotOf(budget), empty(8));
  assert.equal(budget.reserve('fresh', 10, 8), true);
  assert.equal(budget.snapshot().totalBytes, 10);
});

test('budget reset: strictly increasing safe integers only, and it clears owned entries', () => {
  const budget = policy.createBudget({ generation: 3 });
  assert.equal(budget.reserve('a', 5, 3), true);
  for (const next of [3, 2, 0, -1, 1.5, NaN, Infinity, '4', null, MAX_SAFE + 1]) {
    assert.throws(() => budget.reset(next), RangeError, `reset(${String(next)})`);
  }
  assert.equal(budget.snapshot().totalBytes, 5);
  budget.reset(4);
  assert.deepEqual(snapshotOf(budget), empty(4));
  assert.equal(budget.release('a', 4), false);
  assert.equal(budget.reserve('a', 5, 4), true);
  assert.equal(budget.snapshot().totalBytes, 5);
});

test('budget snapshots are frozen copies with no live map or entry reference', () => {
  const budget = policy.createBudget();
  assert.equal(budget.reserve('k', 7, 0), true);
  const snap = snapshotOf(budget);
  assert.equal(Object.isFrozen(snap), true);
  assert.throws(() => { snap.reservedBytes = 0; }, TypeError);
  assert.throws(() => { snap.heldBytes = 999; }, TypeError);
  assert.throws(() => { snap.extra = true; }, TypeError);
  assert.equal(budget.snapshot().reservedBytes, 7);
  assert.equal(budget.snapshot().heldBytes, 0);
  assert.notEqual(budget.snapshot(), budget.snapshot());
});

test('budget instances and configurations are isolated', () => {
  const one = policy.createBudget({ maxBytes: 100 });
  const two = policy.createBudget({ maxBytes: 100, generation: 2 });
  assert.equal(one.reserve('k', 100, 0), true);
  assert.deepEqual(snapshotOf(two), empty(2, 100));
  assert.equal(two.reserve('k', 100, 0), false);
  assert.equal(two.reserve('k', 100, 2), true);
  assert.equal(one.snapshot().totalBytes, 100);
  assert.equal(two.snapshot().totalBytes, 100);
  one.reset(1);
  assert.deepEqual(snapshotOf(two), {
    generation: 2,
    maxBytes: 100,
    reservedBytes: 100,
    heldBytes: 0,
    totalBytes: 100,
    entryCount: 1
  });
});

test('budget config: invalid options throw RangeError before any ledger exists', () => {
  const bad = [
    null,
    'budget',
    [],
    { maxBytes: -1 },
    { maxBytes: 1.5 },
    { maxBytes: NaN },
    { maxBytes: Infinity },
    { maxBytes: '300' },
    { maxBytes: MAX_SAFE + 1 },
    { maxForwardSeconds: -1 },
    { maxForwardSeconds: NaN },
    { maxForwardSeconds: Infinity },
    { maxForwardSeconds: '120' },
    { resumeBytes: -1 },
    { resumeBytes: 0.5 },
    { resumeBytes: NaN },
    { resumeBytes: '1' },
    { resumeBytes: MAX_SAFE + 1 },
    { resumeForwardSeconds: -1 },
    { resumeForwardSeconds: NaN },
    { resumeForwardSeconds: Infinity },
    { generation: -1 },
    { generation: 0.5 },
    { generation: NaN },
    { generation: MAX_SAFE + 1 },
    { generation: '0' }
  ];
  for (const options of bad) {
    assert.throws(() => policy.createBudget(options), RangeError);
  }
  const hinge = policy.createBudget({ generation: MAX_SAFE });
  assert.equal(hinge.snapshot().generation, MAX_SAFE);
  assert.throws(() => hinge.reset(MAX_SAFE), RangeError);
});

test('budget config: shared low/high pairs must stay ordered', () => {
  // Defaults are derived from the effective high marks, so a smaller high keeps
  // the pair valid: 20s high defaults to a 15s low, not the 90s constant.
  assert.doesNotThrow(() => policy.createBudget({ maxForwardSeconds: 20 }));
  assert.doesNotThrow(() => policy.createBudget({
    maxForwardSeconds: 20,
    resumeForwardSeconds: 15
  }));
  assert.doesNotThrow(() => policy.createBudget({
    maxForwardSeconds: 20,
    resumeForwardSeconds: 20
  }));
  assert.doesNotThrow(() => policy.createBudget({ maxBytes: 100, resumeBytes: 100 }));
  // Derived byte and time defaults still fit their zero high marks.
  assert.doesNotThrow(() => policy.createBudget({ maxBytes: 0 }));
  assert.doesNotThrow(() => policy.createBudget({ maxForwardSeconds: 0 }));

  // An explicitly inverted pair is rejected instead of disabling the latch.
  assert.throws(
    () => policy.createBudget({ maxBytes: 100, resumeBytes: 101 }),
    RangeError
  );
  assert.throws(
    () => policy.createBudget({ maxForwardSeconds: 20, resumeForwardSeconds: 20.1 }),
    RangeError
  );
  assert.throws(
    () => policy.createBudget({ maxForwardSeconds: 0, resumeForwardSeconds: 1 }),
    RangeError
  );
});

test('budget entry cap: a positive safe integer default bounds metadata, not bytes', () => {
  assert.equal(policy.DEFAULT_MAX_ENTRIES, 65536);
  // The default is only a configuration value; no test allocates 65536 entries.
  assert.doesNotThrow(() => policy.createBudget());
  assert.doesNotThrow(() => policy.createBudget({ maxEntries: 1 }));
  assert.doesNotThrow(() => policy.createBudget({ maxEntries: MAX_SAFE }));
  assert.deepEqual(snapshotOf(policy.createBudget({ maxEntries: 2 })), empty());
});

test('budget entry cap: zero-byte reservations share slots with normal ones and release frees a slot', () => {
  const budget = policy.createBudget({ maxEntries: 2 });
  assert.equal(budget.reserve('zero-a', 0, 0), true);
  assert.equal(budget.reserve('normal', 500, 0), true);
  const full = snapshotOf(budget);
  assert.deepEqual(full, {
    generation: 0,
    maxBytes: policy.DEFAULT_MAX_BYTES,
    reservedBytes: 500,
    heldBytes: 0,
    totalBytes: 500,
    entryCount: 2
  });

  // A zero-byte request still owns a Map entry, so it cannot bypass the cap.
  assert.equal(budget.reserve('zero-b', 0, 0), false);
  assert.equal(budget.reserve('next', 1, 0), false);
  assert.deepEqual(snapshotOf(budget), full);

  // A committed entry keeps its slot; only its byte accounting changes.
  assert.equal(budget.commit('normal', 500, 0), true);
  assert.equal(budget.reserve('zero-b', 0, 0), false);
  assert.equal(snapshotOf(budget).entryCount, 2);

  // Releasing a zero-byte entry frees exactly one metadata slot.
  assert.equal(budget.release('zero-a', 0), true);
  assert.equal(budget.reserve('zero-b', 0, 0), true);
  assert.deepEqual(snapshotOf(budget), {
    generation: 0,
    maxBytes: policy.DEFAULT_MAX_BYTES,
    reservedBytes: 0,
    heldBytes: 500,
    totalBytes: 500,
    entryCount: 2
  });

  // Releasing a held entry frees its slot for a new reservation.
  assert.equal(budget.release('normal', 0), true);
  assert.equal(budget.reserve('third', 7, 0), true);
  assert.equal(snapshotOf(budget).entryCount, 2);
  assert.equal(snapshotOf(budget).totalBytes, 7);
  assert.equal(budget.reserve('fourth', 0, 0), false);

  // reset clears owned keys, so the metadata slots are reusable afterwards.
  budget.reset(1);
  assert.deepEqual(snapshotOf(budget), empty(1));
  assert.equal(budget.reserve('a', 0, 1), true);
  assert.equal(budget.reserve('b', 0, 1), true);
  assert.equal(budget.reserve('c', 0, 1), false);
  assert.equal(budget.release('a', 1), true);
  assert.equal(budget.reserve('c', 0, 1), true);
  assert.equal(budget.snapshot().entryCount, 2);
});

test('budget entry cap: invalid values throw RangeError and caps stay per instance', () => {
  const bad = [0, -1, 1.5, NaN, Infinity, -Infinity, '2', null, true, {}, MAX_SAFE + 1];
  for (const maxEntries of bad) {
    assert.throws(
      () => policy.createBudget({ maxEntries }),
      RangeError,
      `maxEntries=${String(maxEntries)}`
    );
  }

  // Each ledger owns its own metadata limit; filling one leaves the others free.
  const limited = policy.createBudget({ maxEntries: 2 });
  const other = policy.createBudget({ maxEntries: 2, maxBytes: 50 });
  const defaulted = policy.createBudget();
  assert.equal(limited.reserve('a', 0, 0), true);
  assert.equal(limited.reserve('b', 0, 0), true);
  assert.equal(limited.reserve('c', 0, 0), false);
  assert.equal(other.reserve('c', 0, 0), true);
  assert.equal(other.reserve('d', 10, 0), true);
  assert.equal(other.reserve('e', 0, 0), false);
  assert.equal(defaulted.reserve('x', 0, 0), true);
  assert.equal(limited.snapshot().entryCount, 2);
  assert.equal(other.snapshot().entryCount, 2);
  assert.equal(defaulted.snapshot().entryCount, 1);
  assert.equal(other.snapshot().totalBytes, 10);
});

test('fill gate defaults: filling by default and unknown forward blocks without a latch', () => {
  const gate = policy.createFillGate();
  const filling = gate.update({ forwardSeconds: 0 });
  assert.deepEqual(filling, { filling: true, reason: 'filling' });
  assert.equal(Object.isFrozen(filling), true);
  assert.deepEqual(Object.keys(filling).sort(), ['filling', 'reason']);

  for (const forwardSeconds of [undefined, NaN, Infinity, -1, '5', null, {}]) {
    const result = gate.update({ forwardSeconds });
    assert.deepEqual(result, { filling: false, reason: 'unknown' });
  }
  assert.deepEqual(gate.update({}), { filling: false, reason: 'unknown' });
  assert.deepEqual(gate.update(), { filling: false, reason: 'unknown' });
  assert.deepEqual(gate.update({ forwardSeconds: 0 }), { filling: true, reason: 'filling' });
});

test('fill gate latches at the high mark and resumes only at or below the low mark', () => {
  const gate = policy.createFillGate();
  assert.deepEqual(gate.update({ forwardSeconds: 119 }), { filling: true, reason: 'filling' });
  assert.deepEqual(gate.update({ forwardSeconds: 120 }), { filling: false, reason: 'timeBlocked' });
  assert.deepEqual(gate.update({ forwardSeconds: 119 }), { filling: false, reason: 'timeBlocked' });
  assert.deepEqual(gate.update({ forwardSeconds: 100 }), { filling: false, reason: 'timeBlocked' });
  assert.deepEqual(gate.update({ forwardSeconds: 91 }), { filling: false, reason: 'timeBlocked' });
  assert.deepEqual(gate.update({ forwardSeconds: 90 }), { filling: true, reason: 'filling' });
  // Released latches only re-arm at the high mark.
  assert.deepEqual(gate.update({ forwardSeconds: 100 }), { filling: true, reason: 'filling' });
  assert.deepEqual(gate.update({ forwardSeconds: 120.5 }), { filling: false, reason: 'timeBlocked' });
  assert.deepEqual(gate.update({ forwardSeconds: 90 }), { filling: true, reason: 'filling' });
});

test('fill gate honours custom high/low marks', () => {
  const gate = policy.createFillGate({ maxForwardSeconds: 30, resumeForwardSeconds: 10 });
  assert.deepEqual(gate.update({ forwardSeconds: 29.9 }), { filling: true, reason: 'filling' });
  assert.deepEqual(gate.update({ forwardSeconds: 30 }), { filling: false, reason: 'timeBlocked' });
  assert.deepEqual(gate.update({ forwardSeconds: 11 }), { filling: false, reason: 'timeBlocked' });
  assert.deepEqual(gate.update({ forwardSeconds: 10 }), { filling: true, reason: 'filling' });
  assert.deepEqual(gate.update({ forwardSeconds: 11 }), { filling: true, reason: 'filling' });
});

test('fill gate: unknown forward blocks and never clears a latched time block', () => {
  const gate = policy.createFillGate();
  assert.equal(gate.update({ forwardSeconds: 120 }).reason, 'timeBlocked');
  assert.deepEqual(gate.update({ forwardSeconds: undefined }), { filling: false, reason: 'unknown' });
  assert.deepEqual(gate.update({ forwardSeconds: NaN }), { filling: false, reason: 'unknown' });
  // 90 < 105 < 120 is the middle band: it keeps the latched time block.
  assert.deepEqual(gate.update({ forwardSeconds: 105 }), { filling: false, reason: 'timeBlocked' });
  assert.deepEqual(gate.update({ forwardSeconds: 90 }), { filling: true, reason: 'filling' });
  // At or below the 90s low mark a latched time block is released; 60s is a
  // legal low sample, not a middle-band sample.
  assert.equal(gate.update({ forwardSeconds: 120 }).reason, 'timeBlocked');
  assert.deepEqual(gate.update({ forwardSeconds: 60 }), { filling: true, reason: 'filling' });
  // Unknown while unlatched leaves it unlatched; a later low sample fills.
  assert.deepEqual(gate.update({ forwardSeconds: 'unknown' }), { filling: false, reason: 'unknown' });
  assert.deepEqual(gate.update({ forwardSeconds: 60 }), { filling: true, reason: 'filling' });
});

test('fill gate: byte and quota blockers cannot be bypassed by a low forward buffer', () => {
  const gate = policy.createFillGate();
  assert.deepEqual(gate.update({ forwardSeconds: 0, bytesBlocked: true }), {
    filling: false,
    reason: 'bytesBlocked'
  });
  assert.deepEqual(gate.update({ forwardSeconds: 0, bytesBlocked: true, quotaBlocked: true }), {
    filling: false,
    reason: 'bytesBlocked'
  });
  assert.deepEqual(gate.update({ forwardSeconds: 0, bytesBlocked: false, quotaBlocked: true }), {
    filling: false,
    reason: 'quotaBlocked'
  });
  assert.deepEqual(gate.update({ forwardSeconds: 0, quotaBlocked: false }), {
    filling: true,
    reason: 'filling'
  });
  assert.deepEqual(gate.update({ forwardSeconds: 120, bytesBlocked: true }), {
    filling: false,
    reason: 'bytesBlocked'
  });
});

test('fill gate: releasing byte/quota blockers still respects a latched time block', () => {
  const gate = policy.createFillGate();
  assert.equal(gate.update({ forwardSeconds: 120 }).reason, 'timeBlocked');
  // 90 < 105 < 120 keeps the latch while the independent blockers are sampled.
  assert.deepEqual(gate.update({ forwardSeconds: 105, bytesBlocked: true }), {
    filling: false,
    reason: 'bytesBlocked'
  });
  assert.deepEqual(gate.update({ forwardSeconds: 105, bytesBlocked: false }), {
    filling: false,
    reason: 'timeBlocked'
  });
  assert.deepEqual(gate.update({ forwardSeconds: 105, quotaBlocked: true }), {
    filling: false,
    reason: 'quotaBlocked'
  });
  assert.deepEqual(gate.update({ forwardSeconds: 105, quotaBlocked: false }), {
    filling: false,
    reason: 'timeBlocked'
  });
  assert.deepEqual(gate.update({ forwardSeconds: 90 }), { filling: true, reason: 'filling' });
  // A genuine low sample (<=90s) releases the time block once blockers clear.
  assert.equal(gate.update({ forwardSeconds: 120 }).reason, 'timeBlocked');
  assert.deepEqual(gate.update({ forwardSeconds: 60, bytesBlocked: true }), {
    filling: false,
    reason: 'bytesBlocked'
  });
  assert.deepEqual(gate.update({ forwardSeconds: 60, bytesBlocked: false }), {
    filling: true,
    reason: 'filling'
  });
});

test('fill gate generations: stale samples block safely and cannot clear the latch', () => {
  const gate = policy.createFillGate();
  assert.deepEqual(gate.update({ forwardSeconds: 0, generation: 1 }), {
    filling: false,
    reason: 'staleGeneration'
  });
  assert.equal(gate.update({ forwardSeconds: 120, generation: 0 }).reason, 'timeBlocked');
  assert.deepEqual(gate.update({ forwardSeconds: 60, generation: 1 }), {
    filling: false,
    reason: 'staleGeneration'
  });
  // 90 < 105 < 120 keeps the latch for the current generation.
  assert.equal(gate.update({ forwardSeconds: 105, generation: 0 }).reason, 'timeBlocked');

  gate.reset(1);
  assert.deepEqual(gate.update({ forwardSeconds: 60, generation: 0 }), {
    filling: false,
    reason: 'staleGeneration'
  });
  // reset cleared the latch for the new generation.
  assert.deepEqual(gate.update({ forwardSeconds: 60, generation: 1 }), {
    filling: true,
    reason: 'filling'
  });
  for (const next of [1, 0, -1, 1.5, NaN, Infinity, '2', MAX_SAFE + 1]) {
    assert.throws(() => gate.reset(next), RangeError, `reset(${String(next)})`);
  }
  gate.reset(2);
  assert.deepEqual(gate.update({ forwardSeconds: 120, generation: 1 }), {
    filling: false,
    reason: 'staleGeneration'
  });
  assert.equal(gate.update({ forwardSeconds: 120, generation: 2 }).reason, 'timeBlocked');
  // The current generation may still release the latch with a legal low sample.
  assert.deepEqual(gate.update({ forwardSeconds: 60, generation: 2 }), {
    filling: true,
    reason: 'filling'
  });
});

test('fill gate instances are isolated', () => {
  const first = policy.createFillGate();
  const second = policy.createFillGate({ maxForwardSeconds: 30, resumeForwardSeconds: 10 });
  assert.equal(first.update({ forwardSeconds: 120 }).reason, 'timeBlocked');
  assert.deepEqual(second.update({ forwardSeconds: 20 }), { filling: true, reason: 'filling' });
  assert.deepEqual(second.update({ forwardSeconds: 10 }), { filling: true, reason: 'filling' });
  // 90 < 105 < 120 keeps the first gate latched without touching the second.
  assert.deepEqual(first.update({ forwardSeconds: 105 }), { filling: false, reason: 'timeBlocked' });
  assert.equal(second.update({ forwardSeconds: 30 }).reason, 'timeBlocked');
  assert.deepEqual(first.update({ forwardSeconds: 90 }), { filling: true, reason: 'filling' });
  assert.equal(first.update({ forwardSeconds: 120 }).reason, 'timeBlocked');
  assert.deepEqual(first.update({ forwardSeconds: 60 }), { filling: true, reason: 'filling' });
  assert.equal(second.update({ forwardSeconds: 10 }).reason, 'filling');
});

test('fill gate config: invalid options throw RangeError', () => {
  const bad = [
    null,
    'gate',
    [],
    { maxForwardSeconds: -1 },
    { maxForwardSeconds: NaN },
    { maxForwardSeconds: Infinity },
    { maxForwardSeconds: '120' },
    { resumeForwardSeconds: -1 },
    { resumeForwardSeconds: NaN },
    { resumeForwardSeconds: Infinity },
    { resumeForwardSeconds: '90' }
  ];
  for (const options of bad) {
    assert.throws(() => policy.createFillGate(options), RangeError);
  }
  const zero = policy.createFillGate({ maxForwardSeconds: 0, resumeForwardSeconds: 0 });
  assert.equal(zero.update({ forwardSeconds: 0 }).reason, 'timeBlocked');
});

test('fill gate config: the default low mark follows a smaller high mark', () => {
  const gate = policy.createFillGate({ maxForwardSeconds: 20 });
  assert.deepEqual(gate.update({ forwardSeconds: 15.1 }), { filling: true, reason: 'filling' });
  assert.deepEqual(gate.update({ forwardSeconds: 20 }), {
    filling: false,
    reason: 'timeBlocked'
  });
  assert.deepEqual(gate.update({ forwardSeconds: 15 }), { filling: true, reason: 'filling' });

  // A zero high mark derives a zero low mark, so 0/0 remains a legal pair.
  const zero = policy.createFillGate({ maxForwardSeconds: 0 });
  assert.equal(zero.update({ forwardSeconds: 0 }).reason, 'timeBlocked');
});

test('fill gate config: an explicit low mark above the high mark throws RangeError', () => {
  assert.doesNotThrow(() => policy.createFillGate({
    maxForwardSeconds: 20,
    resumeForwardSeconds: 20
  }));
  assert.doesNotThrow(() => policy.createFillGate({
    maxForwardSeconds: 0,
    resumeForwardSeconds: 0
  }));
  assert.throws(
    () => policy.createFillGate({ maxForwardSeconds: 20, resumeForwardSeconds: 20.1 }),
    RangeError
  );
  assert.throws(
    () => policy.createFillGate({ maxForwardSeconds: 30, resumeForwardSeconds: 90 }),
    RangeError
  );
  assert.throws(
    () => policy.createFillGate({ maxForwardSeconds: 0, resumeForwardSeconds: 1 }),
    RangeError
  );
});
