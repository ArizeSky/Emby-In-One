'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const { create } = require('../public/web/player-mux-producers.js');

const code = (value) => ({ code: value });

test('close/drain waits for every delayed producer, not just pull', async () => {
  // Exercise the browser UMD entry as well as CommonJS in the other tests.
  const browser = vm.createContext({ setTimeout, clearTimeout });
  vm.runInContext(fs.readFileSync(require.resolve(
    '../public/web/player-mux-producers.js'), 'utf8'), browser);
  const gate = browser.EioMuxProducers.create();
  await assert.rejects(gate.drain(10), code('ERR_MUX_PRODUCERS_NOT_CLOSED'));
  const pull = gate.enter('pull');
  const copy = gate.enter('delayed output copy');
  const nested = gate.enter('nested producer');
  assert.equal(gate.snapshot().active, 3);
  gate.close('stop');
  assert.throws(() => gate.enter('late'), code('ERR_MUX_PRODUCERS_CLOSED'));
  assert.equal(pull.finish(), true);
  await assert.rejects(gate.drain(5), code('ERR_MUX_PRODUCERS_DRAIN_TIMEOUT'));
  assert.equal(gate.snapshot().closed, true);
  assert.equal(gate.snapshot().active, 2);
  assert.equal(gate.snapshot().waiters, 0);
  assert.throws(() => gate.reopen(), code('ERR_MUX_PRODUCERS_BUSY'));
  assert.throws(() => gate.beginMutation('replace'), code('ERR_MUX_PRODUCERS_BUSY'));
  let drained = false;
  const waiting = gate.drain(500).then(() => { drained = true; });
  const secondWaiter = gate.drain(500);
  copy.finish();
  await Promise.resolve();
  assert.equal(drained, false);
  assert.equal(nested.finish(), true);
  await Promise.all([waiting, secondWaiter]);
  assert.equal(drained, true);
  assert.equal(gate.snapshot().active, 0);
  assert.equal(nested.finish(), false);
});

test('epoch invalidation persists through reopen and finish is synchronous/idempotent', async () => {
  const gate = create();
  const stale = gate.enter('old epoch');
  const epoch = gate.snapshot().epoch;
  assert.equal(stale.valid(), true);
  assert.equal(stale.guard(), true);
  gate.close('seek');
  assert.ok(gate.snapshot().epoch > epoch);
  assert.equal(stale.valid(), false);
  assert.throws(() => stale.guard(), code('ERR_MUX_PRODUCERS_STALE'));
  assert.equal(stale.finish(), true);
  assert.equal(gate.snapshot().active, 0);
  await gate.drain(0);
  gate.reopen();
  assert.equal(stale.valid(), false);
  const fresh = gate.enter('new epoch');
  assert.equal(stale.finish(), false);
  assert.equal(gate.snapshot().active, 1);
  const snapshot = gate.snapshot();
  snapshot.producers[0].label = 'tampered';
  assert.equal(gate.snapshot().producers[0].label, 'new epoch');
  assert.equal(fresh.guard(), true);
  fresh.finish();
  assert.equal(fresh.valid(), false);
});

test('exclusive mutation retains ownership on invalidation, timeout and retirement', async () => {
  const gate = create();
  assert.throws(() => gate.beginMutation('early'), code('ERR_MUX_PRODUCERS_NOT_CLOSED'));
  gate.close('transition');
  const mutation = gate.beginMutation('replace');
  assert.equal(mutation.guard(), true);
  assert.throws(() => gate.beginMutation('overlap'), code('ERR_MUX_PRODUCERS_BUSY'));
  assert.throws(() => gate.reopen(), code('ERR_MUX_PRODUCERS_BUSY'));
  gate.close('invalidate mutation');
  assert.equal(mutation.valid(), false);
  assert.throws(() => mutation.guard(), code('ERR_MUX_PRODUCERS_STALE'));
  await assert.rejects(gate.drain(5), code('ERR_MUX_PRODUCERS_DRAIN_TIMEOUT'));
  assert.equal(gate.snapshot().mutation.label, 'replace');
  assert.throws(() => gate.beginMutation('still owned'), code('ERR_MUX_PRODUCERS_BUSY'));
  assert.equal(gate.retire(), true);
  assert.equal(gate.retire(), false);
  assert.equal(gate.snapshot().mutation.label, 'replace');
  assert.throws(() => gate.enter('retired'), code('ERR_MUX_PRODUCERS_RETIRED'));
  assert.throws(() => gate.reopen(), code('ERR_MUX_PRODUCERS_RETIRED'));
  assert.throws(() => gate.beginMutation('retired'), code('ERR_MUX_PRODUCERS_RETIRED'));
  const waiting = gate.drain(500);
  assert.equal(mutation.finish(), true);
  assert.equal(mutation.finish(), false);
  await waiting;
  assert.equal(gate.snapshot().mutation, null);
  assert.equal(gate.snapshot().retired, true);
  assert.equal(gate.snapshot().closed, true);
  assert.throws(() => gate.reopen(), code('ERR_MUX_PRODUCERS_RETIRED'));
});
