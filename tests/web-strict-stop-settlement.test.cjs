'use strict';
// Strict stop settlement regressions. Every case extracts the real
// `stop(...args)` wrapper that now precedes the renamed `eioStopInternal` body
// in the vendored player class and runs it in a VM against a fake
// `eioStopInternal`. No application, Worker, upstream, media pipeline or real
// owned-resource manager is involved, and nothing here proves that resources
// were freed: the wrapper only settles the shared strict attempt.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const BUNDLE = path.join(__dirname, '../public/vendor/web/libmedia/avplayer.js');
const WRAPPER = 'async stop(...args){';
const INTERNAL = 'async eioStopInternal(){';
const ASYNC = { timeout: 2000 };

const source = fs.readFileSync(BUNDLE, 'utf8');

// The wrapper must be sliced from the vendored player itself, never rebuilt
// from a description, so a regression in the bundle fails these cases.
function wrapperText() {
  const start = source.indexOf(WRAPPER);
  assert.ok(start >= 0, 'strict stop settlement wrapper is missing from the vendored player');
  assert.equal(source.indexOf(WRAPPER, start + WRAPPER.length), -1, 'strict stop settlement wrapper must be unique');
  const stop = source.indexOf(INTERNAL, start);
  assert.ok(stop > start, 'eioStopInternal must immediately follow the settlement wrapper');
  const text = source.slice(start, stop).trimEnd();
  assert.ok(text.endsWith('}'), 'sliced settlement wrapper must close with its own brace');
  assert.equal(text.includes(INTERNAL), false, 'sliced text must end where eioStopInternal begins');
  return text;
}

function loadWrapper() {
  return vm.runInNewContext(`({${wrapperText()}})`, { Promise });
}

function makePlayer(overrides = {}) {
  const player = loadWrapper();
  player.eioSharedMediaDomain = {
    domainId: 'fake-domain',
    heldBytes: 256,
    snapshot() {
      return { domainId: this.domainId, heldBytes: this.heldBytes };
    }
  };
  player.status = 6;
  player.stopPending = null;
  player.source = 'blob:fake-source';
  player.ownership = { heldBytes: 256, entries: 1 };
  player.internalCalls = [];
  Object.assign(player, overrides);
  return player;
}

test('strict stop settlement wrapper and internal stop head stay wired together', () => {
  assert.equal(source.indexOf(INTERNAL, source.indexOf(INTERNAL) + 1), -1, 'eioStopInternal must be defined once');
  const destroy = /async destroy\(\)\{P\.pq\(`call destroy, taskId: \$\{this\.taskId\}`,Ce,\d+\),2!==this\.status\?\(await this\.stop\(\)/.exec(source);
  assert.ok(destroy, 'player destroy is missing');
  assert.ok(destroy.index > source.indexOf(INTERNAL), 'destroy must stay in the same player class after the wrapped stop');
  assert.equal(
    source.split('eioStopInternal').length - 1,
    3,
    'internal stop is referenced only by its definition and the two wrapper branches'
  );
});

test('concurrent strict stops share one failing attempt and rethrow the original error', ASYNC, async () => {
  const error = new Error('EIO_TASK_TEARDOWN_UNVERIFIED');
  error.code = 'EIO_TASK_TEARDOWN_UNVERIFIED';
  const player = makePlayer();
  const domain = player.eioSharedMediaDomain;
  const before = domain.snapshot();
  let releaseGate;
  const gate = new Promise((resolve) => {
    releaseGate = resolve;
  });
  let orphanSettled = false;
  player.eioStopInternal = async function (...args) {
    player.internalCalls.push(args);
    // The real body assigns a resolve-only manual promise before teardown.
    const orphan = new Promise(() => {});
    orphan.then(() => {
      orphanSettled = true;
    });
    player.stopPending = orphan;
    await gate;
    throw error;
  };
  const first = player.stop(true);
  const second = player.stop(true);
  const both = Promise.allSettled([first, second]);
  assert.equal(player.internalCalls.length, 0, 'strict stops are scheduled, not invoked inline');
  await Promise.resolve();
  assert.equal(player.internalCalls.length, 1, 'the shared attempt runs the internal stop exactly once');
  releaseGate();
  const settled = await both;
  assert.equal(settled[0].status, 'rejected');
  assert.equal(settled[1].status, 'rejected');
  assert.equal(settled[0].reason, error, 'first caller sees the original error');
  assert.equal(settled[1].reason, error, 'concurrent caller sees the same original error');
  await Promise.resolve();
  assert.equal(orphanSettled, false, 'the manual stopPending is dropped, not claimed as settled');
  assert.equal(player.stopPending, null, 'the orphaned manual stopPending is cleared on failure');
  assert.equal(player.eioStrictStopPending, null, 'the shared strict attempt settles instead of hanging');
  assert.equal(player.eioSharedMediaDomain, domain, 'domain binding is untouched by settlement');
  assert.deepEqual(domain.snapshot(), before, 'fake domain accounting is unchanged');
  assert.equal(player.status, 6, 'status is untouched by settlement');
  assert.deepEqual(player.ownership, { heldBytes: 256, entries: 1 }, 'held bytes are untouched by settlement');
  assert.equal(player.source, 'blob:fake-source', 'source is untouched by settlement');
  assert.deepEqual(player.internalCalls, [[true]], 'arguments reach the internal stop unchanged');
});

test('a later explicit stop retries after a failed strict attempt', ASYNC, async () => {
  const failure = new Error('once');
  const player = makePlayer();
  let attempts = 0;
  player.eioStopInternal = async function (...args) {
    player.internalCalls.push(args);
    attempts += 1;
    if (attempts === 1) {
      player.stopPending = new Promise(() => {});
      throw failure;
    }
    player.stopPending = null;
    player.status = 0;
    return 'stopped';
  };
  await assert.rejects(player.stop(), (reason) => reason === failure);
  assert.equal(player.stopPending, null);
  assert.equal(player.eioStrictStopPending, null);
  const result = await player.stop(false);
  assert.equal(result, 'stopped', 'explicit retry after failure is allowed and succeeds');
  assert.equal(player.internalCalls.length, 2, 'retry runs the internal stop a second time');
  assert.equal(player.stopPending, null);
  assert.equal(player.eioStrictStopPending, null);
});

test('concurrent strict stops settle successfully without leaving a strict promise', ASYNC, async () => {
  const player = makePlayer();
  let releaseGate;
  const gate = new Promise((resolve) => {
    releaseGate = resolve;
  });
  player.eioStopInternal = async function (...args) {
    player.internalCalls.push(args);
    player.stopPending = new Promise(() => {});
    await gate;
    player.stopPending = null;
    player.status = 0;
    return 'stopped';
  };
  const both = Promise.all([player.stop(), player.stop()]);
  await Promise.resolve();
  assert.equal(player.internalCalls.length, 1, 'the shared attempt runs the internal stop exactly once');
  releaseGate();
  assert.deepEqual(await both, ['stopped', 'stopped']);
  assert.equal(player.eioStrictStopPending, null, 'the settled strict attempt is cleared');
  assert.equal(player.stopPending, null);
  player.eioStopInternal = async function (...args) {
    player.internalCalls.push(args);
    return 'again';
  };
  assert.equal(await player.stop(), 'again', 'a settled attempt does not block later stops');
});

test('no-domain baseline invokes the internal stop inline and keeps its return/failure semantics', ASYNC, async () => {
  const failure = new Error('baseline-failure');
  const player = makePlayer({ eioSharedMediaDomain: null });
  const orphan = new Promise(() => {});
  player.stopPending = orphan;
  let calls = 0;
  player.eioStopInternal = function (flag) {
    calls += 1;
    assert.equal(flag, 'keep');
    throw failure;
  };
  const rejected = player.stop('keep');
  assert.equal(calls, 1, 'the baseline path invokes eioStopInternal without a scheduler hop');
  assert.equal(player.eioStrictStopPending, undefined, 'the baseline path never creates a strict promise');
  await assert.rejects(rejected, (reason) => reason === failure);
  assert.equal(player.stopPending, orphan, 'the baseline failure path leaves stopPending exactly as it was');
  assert.equal(player.eioStrictStopPending, undefined);
  player.eioStopInternal = function (...args) {
    calls += 1;
    return args.length;
  };
  assert.equal(await player.stop('a', 'b'), 2, 'the baseline path forwards arguments and the inner return value');
  assert.equal(calls, 2);
});
