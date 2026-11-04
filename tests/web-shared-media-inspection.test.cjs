'use strict';

/*
 * Stage 3a tests for the non-mutating reservation-retirement observer
 * (Controller.prototype.inspectReservation) of
 * web-src/player-shared-media-ownership.js.
 *
 * Scope of this file:
 *   - module 90187 must still begin with the EXACT standalone ownership bytes
 *     (including the new method) and still be followed by the core capture;
 *   - the module export surface is unchanged: the method lives on the
 *     controller prototype, not on the factory result;
 *   - live/reserved/bound/closing/retired observations, cancellation labelling,
 *     slot reuse, zero-byte records, cloned tokens and malformed tokens;
 *   - deterministic single-isolate Atomics.load injections for torn reads,
 *     unknown states and partially cleared multiword fields (restored in a
 *     finally block; these are labelled shims, NOT real worker concurrency);
 *   - one real node:worker_threads worker attached to the same shared
 *     descriptor, which observes the token live at CLOSING and only retired
 *     after the actual shared commit.
 *
 * Evidence limits (deliberate):
 *   - The worker below is hand-assembled from the standalone helper. It is not
 *     the generated libmedia worker, not the Q7/13486 allocator, and it does
 *     not install a task descriptor/NX binding or register a real domain.
 *     The "allocator success" step is an explicit fixture message, not a real
 *     free.
 *   - No application server, upstream, browser, network or production instance
 *     is used. Every async test has an explicit timeout and terminates its
 *     workers in a finally block.
 */

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const v8 = require('node:v8');
const { Worker } = require('node:worker_threads');

const ROOT = path.join(__dirname, '..');
const OWNERSHIP_PATH = path.join(ROOT, 'web-src', 'player-shared-media-ownership.js');
const OWNERSHIP_TEXT = fs.readFileSync(OWNERSHIP_PATH, 'utf8');
const mod = require(OWNERSHIP_PATH);
const readLibmediaSourceGraph = require('./web-libmedia-source-graph.cjs');

const sourceGraph = readLibmediaSourceGraph();
const SIGNATURE_90187 = '90187:(module,exports,__webpack_require__)=>{';
const SIGNATURE_90188 = '90188:(module,exports,__webpack_require__)=>{';

const E = mod.LAYOUT.entry;

function codedError(code, message) {
  const error = new Error(message || code);
  error.code = code;
  return error;
}

function cloneValue(value) {
  if (typeof structuredClone === 'function') return structuredClone(value);
  return v8.deserialize(v8.serialize(value));
}

function slotOffset(ctl, entryIndex) {
  const descriptor = ctl.descriptor;
  assert.equal(descriptor.entriesOffset, mod.LAYOUT.headerWords,
    'descriptor.entriesOffset must equal the module header word count');
  return descriptor.entriesOffset + entryIndex * descriptor.entryWords;
}

/* Full snapshot of every shared word, so a "non-mutating" claim covers more
 * than held/peak counters. */
function wordTable(ctl) {
  return Int32Array.from(new Int32Array(ctl.descriptor.sab));
}

function topLevelKeys(value) {
  return Object.keys(value).sort();
}

function assertNoWrite(ctl, action) {
  const before = { snapshot: ctl.snapshot(), words: wordTable(ctl) };
  const result = action();
  assert.deepEqual(ctl.snapshot(), before.snapshot,
    'inspection must not change any shared counter');
  assert.deepEqual(wordTable(ctl), before.words,
    'inspection must not write any shared word');
  return result;
}

/* ------------------------------------------------------------------ */
/* Source parity: exact 90190 core bytes, 90187 facade wiring           */
/* ------------------------------------------------------------------ */

test('module 90190 starts with the exact standalone ownership bytes; module 90187 requires the actual core',
  { timeout: 5000 }, () => {
    const bundle = sourceGraph.text;
    assert.equal((bundle.match(/90187:\(module,exports,__webpack_require__\)=>\{/g) || []).length, 1,
      'factory 90187 must appear exactly once with the required signature');
    assert.equal((bundle.match(/90188:\(module,exports,__webpack_require__\)=>\{/g) || []).length, 1,
      'factory 90188 must still be present exactly once');
    assert.equal((bundle.match(/90190:\(module,exports,__webpack_require__\)=>\{/g) || []).length, 1,
      'factory 90190 must be the single physical ownership core');
    assert.equal(bundle.split(OWNERSHIP_TEXT).length - 1, 1,
      'the standalone ownership helper must stay embedded exactly once');

    /* The exact UMD bytes moved verbatim from 90187 to 90190; only wrapper
     * separator whitespace around the extracted body may differ. */
    const coreBody = sourceGraph.factoryBody(90190);
    const coreAt = coreBody.indexOf(OWNERSHIP_TEXT);
    assert.notEqual(coreAt, -1,
      'module 90190 must contain the exact standalone ownership core bytes');
    assert.equal(coreAt, coreBody.search(/\S/),
      'module 90190 body must start with the exact standalone ownership core bytes');
    assert.equal(coreBody.slice(coreAt + OWNERSHIP_TEXT.length).trim(), '',
      'nothing but wrapper whitespace may follow the exact ownership core bytes');

    const body = sourceGraph.factory(90187).slice(SIGNATURE_90187.length);
    assert.ok(body.replace(/^\s+/, '')
      .startsWith('const core=__webpack_require__(90190);'),
    'module 90187 must start the facade by requiring the actual extracted core 90190');

    assert.ok(OWNERSHIP_TEXT.includes('Controller.prototype.inspectReservation = function inspectReservation(token) {'),
      'the synced prefix must actually contain the new observer');
    assert.ok(OWNERSHIP_TEXT.split('inspectReservation').length - 1 >= 2,
      'the observer must be defined once and named (declaration + function name)');

    /* The nonoptin/lazy release path must stay untouched by this step. */
    assert.equal((body.match(/__webpack_require__\(90188\)/g) || []).length, 1,
      'module 90187 must still require 90188 exactly once');
    const coreRequireAt = body.indexOf('const core=__webpack_require__(90190);');
    const releaseRequireAt = body.indexOf('__webpack_require__(90188)');
    assert.ok(coreRequireAt > 0 && releaseRequireAt > coreRequireAt,
      'the 90188 require must stay in the facade extension, after the core require');
    assert.ok(body.includes('eioReleaseRegistry===null'),
      'the lazy release registry guard must stay unchanged');
  });

test('the public export surface is unchanged; the observer is a prototype method',
  { timeout: 5000 }, () => {
    assert.deepEqual(Object.keys(mod).sort(), [
      'KIND', 'LAYOUT', 'MAX_CAPACITY_BYTES', 'MAX_ENTRIES', 'STATE', 'VERSION', 'attach', 'create'
    ], 'the module export surface must not gain a top-level entry');
    assert.equal(mod.inspectReservation, undefined,
      'inspectReservation must not be exported from the factory result');
    assert.equal(mod.LAYOUT.headerWords, 24, 'the header layout must not change');
    assert.equal(mod.LAYOUT.entryWords, 8, 'the entry layout must not change');

    const ctl = mod.create({ capacityBytes: 1 << 20, maxEntries: 8 });
    assert.equal(typeof ctl.inspectReservation, 'function',
      'the attached controller must expose the observer on its prototype');
    assert.equal(Object.prototype.hasOwnProperty.call(ctl, 'inspectReservation'), false,
      'the observer must be a prototype method, not an own property');
    assert.equal(Object.getPrototypeOf(ctl).inspectReservation,
      mod.create({ capacityBytes: 1 << 20, maxEntries: 8 }).inspectReservation,
      'every controller must share the same prototype method');
  });

/* ------------------------------------------------------------------ */
/* Lifecycle observation                                               */
/* ------------------------------------------------------------------ */

test('reserved -> bound -> closing stays live, and only the real commit retires the token',
  { timeout: 5000 }, () => {
    const ctl = mod.create({ capacityBytes: 1 << 20, maxEntries: 8 });
    const reserved = ctl.reserve(4096, 7);
    assert.equal(reserved.ok, true);
    const token = reserved.token;

    const live = assertNoWrite(ctl, () => ctl.inspectReservation(token));
    assert.deepEqual(live, { ok: true, retired: false, state: 'reserved', bytes: 4096, generation: 7 });
    assert.equal(Object.isFrozen(live), true, 'a live observation must be frozen');
    assert.deepEqual(topLevelKeys(live), ['bytes', 'generation', 'ok', 'retired', 'state'],
      'a live observation must not expose payload pointers or credits');
    assert.equal(ctl.heldBytes(), 4096, 'inspection must not release the reservation charge');

    const bound = ctl.bind(token, { data: 0x1000, buffer: 0x2000, size: 4096 });
    assert.equal(bound.ok, true);
    const boundView = assertNoWrite(ctl, () => ctl.inspectReservation(token));
    assert.equal(boundView.retired, false);
    assert.equal(boundView.state, 'bound');
    assert.equal(ctl.heldBytes(), 4096);

    const prepared = ctl.prepareFinalFree({ data: 0x1000, buffer: 0x2000 });
    assert.equal(prepared.ok, true);
    const closingView = assertNoWrite(ctl, () => ctl.inspectReservation(token));
    assert.equal(closingView.retired, false);
    assert.equal(closingView.state, 'closing');
    assert.equal(closingView.bytes, 4096);
    assert.equal(ctl.heldBytes(), 4096, 'the closing charge must still be held');
    assert.equal(ctl.snapshot().countClosing, 1);

    const committed = ctl.commitFinalFree(prepared.ticket, { dataFreed: true });
    assert.equal(committed.ok, true);
    const retired = assertNoWrite(ctl, () => ctl.inspectReservation(token));
    assert.equal(retired.ok, true);
    assert.equal(retired.retired, true);
    assert.equal(retired.state, 'retired');
    assert.ok(retired.reason === 'id-changed' || retired.reason === 'slot-free',
      'retirement must be labelled id-changed or slot-free, got ' + String(retired.reason));
    assert.equal(ctl.heldBytes(), 0, 'only the commit may release the payload bytes');
  });

test('an unbound reclaim retires the token as cancellation, not as free proof',
  { timeout: 5000 }, () => {
    const ctl = mod.create({ capacityBytes: 1 << 20, maxEntries: 8 });
    const reserved = ctl.reserve(512, 0);
    assert.equal(reserved.ok, true);

    const reclaimed = ctl.reclaimUnused(reserved.token);
    assert.equal(reclaimed.ok, true);
    assert.equal(reclaimed.reclaimedBytes, 512);

    const retired = assertNoWrite(ctl, () => ctl.inspectReservation(reserved.token));
    assert.deepEqual(topLevelKeys(retired), ['ok', 'reason', 'retired', 'state'],
      'retirement must not claim credit, bytes released or payload identity');
    assert.equal(retired.retired, true);
    assert.equal(retired.state, 'retired');
    assert.equal(retired.releasedBytes, undefined);
    assert.equal(retired.bytes, undefined);
    /* Only the token retirement is observable; the cancellation was an unbound
     * reclaim, so this must NOT be read as final-free evidence. */
    assert.equal(ctl.heldBytes(), 0);
  });

test('a stale token stays retired after slot reuse while the new token stays live',
  { timeout: 5000 }, () => {
    const ctl = mod.create({ capacityBytes: 1 << 20, maxEntries: 8 });
    const first = ctl.reserve(1024, 1);
    assert.equal(first.ok, true);
    assert.equal(first.token.entry, 0, 'the first reservation takes the first slot');
    assert.equal(ctl.reclaimUnused(first.token).ok, true);

    const second = ctl.reserve(1024, 1);
    assert.equal(second.ok, true);
    assert.equal(second.token.entry, first.token.entry, 'the same slot must be reused');
    assert.ok(second.token.id > first.token.id, 'ids must stay monotonic across reuse');

    const stale = assertNoWrite(ctl, () => ctl.inspectReservation(first.token));
    assert.equal(stale.retired, true, 'the old identity must never resurrect');
    assert.equal(stale.reason, 'id-changed');

    const fresh = assertNoWrite(ctl, () => ctl.inspectReservation(second.token));
    assert.equal(fresh.retired, false);
    assert.equal(fresh.state, 'reserved');
    assert.equal(fresh.bytes, 1024);
    assert.equal(ctl.heldBytes(), 1024);

    const zero = ctl.reserve(0, 0);
    assert.equal(zero.ok, true);
    const zeroView = assertNoWrite(ctl, () => ctl.inspectReservation(zero.token));
    assert.equal(zeroView.retired, false, 'a live zero-byte record must not be read as retired');
    assert.equal(zeroView.state, 'reserved');
    assert.equal(zeroView.bytes, 0);
  });

/* ------------------------------------------------------------------ */
/* Invalid and cloned tokens                                           */
/* ------------------------------------------------------------------ */

test('invalid tokens fail normally and malformed clones never mutate shared state',
  { timeout: 5000 }, () => {
    const ctl = mod.create({ capacityBytes: 1 << 20, maxEntries: 8 });
    const reserved = ctl.reserve(256, 2);
    assert.equal(reserved.ok, true);
    const token = reserved.token;
    const before = { snapshot: ctl.snapshot(), words: wordTable(ctl) };

    const invalid = [
      undefined,
      null,
      0,
      'token',
      {},
      { brand: 'not-a-token', entry: 0, id: 1, bytes: 0, generation: 0 },
      Object.assign({}, token, { entry: -1 }),
      Object.assign({}, token, { entry: ctl.descriptor.maxEntries }),
      Object.assign({}, token, { entry: 0.5 }),
      Object.assign({}, token, { id: 0 }),
      Object.assign({}, token, { id: -3 }),
      Object.assign({}, token, { id: 'x' }),
      Object.assign({}, token, { bytes: -1 }),
      Object.assign({}, token, { bytes: Number.NaN })
    ];
    for (const candidate of invalid) {
      const result = ctl.inspectReservation(candidate);
      assert.equal(result.ok, false, 'an invalid token must produce a failure object');
      assert.equal(result.code, 'invalid-argument');
      assert.equal(result.retired, undefined, 'an invalid token must never be reported as retired');
    }

    const malformedClone = cloneValue(token);
    malformedClone.id = String(token.id);
    const malformed = ctl.inspectReservation(malformedClone);
    assert.equal(malformed.ok, false);
    assert.equal(malformed.code, 'invalid-argument');

    assert.deepEqual(ctl.snapshot(), before.snapshot, 'failures must not change counters');
    assert.deepEqual(wordTable(ctl), before.words, 'failures must not write shared words');

    /* Identity is the ID/entry pair, not object identity: a legitimate
     * structured clone of the token stays observable. */
    const cloned = cloneValue(token);
    assert.notEqual(cloned, token);
    const clonedView = assertNoWrite(ctl, () => ctl.inspectReservation(cloned));
    assert.equal(clonedView.retired, false);
    assert.equal(clonedView.state, 'reserved');
    assert.equal(clonedView.bytes, 256);
    assert.equal(clonedView.generation, 2);

    const wrongGeneration = ctl.inspectReservation(Object.assign(cloneValue(token), { generation: 3 }));
    assert.equal(wrongGeneration.ok, false);
    assert.equal(wrongGeneration.code, 'busy', 'a generation mismatch is a torn/unpublished record, not retirement');
    const wrongBytes = ctl.inspectReservation(Object.assign(cloneValue(token), { bytes: 260 }));
    assert.equal(wrongBytes.ok, false);
    assert.equal(wrongBytes.code, 'busy', 'a bytes mismatch is a torn/unpublished record, not retirement');
    assert.equal(ctl.heldBytes(), 256);
    assert.deepEqual(ctl.snapshot(), before.snapshot, 'mismatch failures must not change counters');
  });

/* ------------------------------------------------------------------ */
/* Deterministic Atomics.load injections (labelled shims)               */
/* ------------------------------------------------------------------ */

function withLoadShim(handler, action) {
  const original = Atomics.load;
  Atomics.load = function shimmedLoad(array, index) {
    return handler(original, array, index);
  };
  try {
    return action();
  } finally {
    Atomics.load = original;
  }
}

test('injected torn id/state reads report busy and never a false retirement',
  { timeout: 5000 }, () => {
    const ctl = mod.create({ capacityBytes: 1 << 20, maxEntries: 8 });
    const reserved = ctl.reserve(4096, 5);
    assert.equal(reserved.ok, true);
    const token = reserved.token;
    const offset = slotOffset(ctl, token.entry);
    const idWord = offset + E.ID;
    const stateWord = offset + E.STATE;
    const bytesWord = offset + E.BYTES;
    const generationWord = offset + E.GENERATION;
    const before = { snapshot: ctl.snapshot(), words: wordTable(ctl) };
    const realLoad = Atomics.load;

    /* Torn id: the first ID read is the real one, the second is a peer's new
     * id. This is exactly the id1 !== id2 case and must be busy. */
    let idReads = 0;
    const tornId = withLoadShim((load, array, index) => {
      const value = load(array, index);
      if (index !== idWord) return value;
      idReads += 1;
      return idReads === 2 ? token.id + 1 : value;
    }, () => ctl.inspectReservation(token));
    assert.equal(idReads, 2, 'the observer must perform exactly two id reads');
    assert.equal(tornId.ok, false);
    assert.equal(tornId.code, 'busy');
    assert.equal(tornId.retired, undefined, 'a torn id read must never be reported as retired');
    assert.equal(Atomics.load, realLoad, 'the Atomics.load injection must be restored');

    /* Torn state: the second STATE read reports FREE, which would look like a
     * retirement if the double-read check were missing. */
    let stateReads = 0;
    const tornState = withLoadShim((load, array, index) => {
      const value = load(array, index);
      if (index !== stateWord) return value;
      stateReads += 1;
      return stateReads === 2 ? mod.STATE.FREE : value;
    }, () => ctl.inspectReservation(token));
    assert.equal(stateReads, 2, 'the observer must perform exactly two state reads');
    assert.equal(tornState.ok, false);
    assert.equal(tornState.code, 'busy');
    assert.equal(tornState.retired, undefined, 'a torn state read must never invent retirement');

    /* Stable but unknown state with a changed id: fail closed with
     * wrong-state, never retirement (priority: state recognition first). */
    const unknown = withLoadShim((load, array, index) => {
      if (index === stateWord) return 42;
      if (index === idWord) return token.id + 9;
      return load(array, index);
    }, () => ctl.inspectReservation(token));
    assert.equal(unknown.ok, false);
    assert.equal(unknown.code, 'wrong-state');
    assert.equal(unknown.retired, undefined);
    assert.equal(unknown.state, 42, 'the unrecognized state must be reported back');

    /* Synthetic: a stable FREE state whose id word still matches the token.
     * Only a torn/partial record can look like this, and it must be reported
     * as slot-free retirement. Labelled synthetic, not a real transition. */
    const slotFree = withLoadShim((load, array, index) => {
      if (index === stateWord) return mod.STATE.FREE;
      return load(array, index);
    }, () => ctl.inspectReservation(token));
    assert.deepEqual(slotFree, { ok: true, retired: true, state: 'retired', reason: 'slot-free' });

    /* Transiently cleared multiword fields while id/state stay live: busy, no
     * release inference from a half-cleared entry. */
    const partialBytes = withLoadShim((load, array, index) => {
      if (index === bytesWord) return 0;
      return load(array, index);
    }, () => ctl.inspectReservation(token));
    assert.equal(partialBytes.ok, false);
    assert.equal(partialBytes.code, 'busy');

    const partialGeneration = withLoadShim((load, array, index) => {
      if (index === generationWord) return token.generation + 1;
      return load(array, index);
    }, () => ctl.inspectReservation(token));
    assert.equal(partialGeneration.ok, false);
    assert.equal(partialGeneration.code, 'busy');

    assert.equal(Atomics.load, realLoad, 'the global Atomics.load must be the real function again');
    assert.deepEqual(ctl.snapshot(), before.snapshot, 'read injections must not change counters');
    assert.deepEqual(wordTable(ctl), before.words, 'read injections must not write shared words');
    assert.equal(ctl.heldBytes(), 4096, 'no injected observation may release bytes');
  });

/* ------------------------------------------------------------------ */
/* Real Node Worker attached to the same shared descriptor             */
/* ------------------------------------------------------------------ */

const WORKER_SOURCE = String.raw`
'use strict';
const { parentPort, workerData } = require('node:worker_threads');
const ownership = require(workerData.modulePath);
let ctl = null;
let ticket = null;

function handle(msg) {
  switch (msg.op) {
    case 'attach': {
      ctl = ownership.attach(msg.descriptor);
      return { attached: true, heldBytes: ctl.heldBytes() };
    }
    case 'inspect':
      return ctl.inspectReservation(msg.token);
    case 'prepare': {
      const result = ctl.prepareFinalFree({ data: msg.data, buffer: msg.buffer });
      if (result.ok) ticket = result.ticket;
      return { ok: result.ok, code: result.code, ticketBytes: result.ok ? result.ticket.bytes : null };
    }
    case 'allocatorSuccess':
      // Fixture allocator result only; nothing is freed in this test.
      return { ok: true, dataFreed: msg.dataFreed === true, fixture: true };
    case 'commit': {
      const result = ctl.commitFinalFree(ticket, { dataFreed: msg.dataFreed === true });
      return { ok: result.ok, code: result.code, releasedBytes: result.releasedBytes, heldBytes: ctl.heldBytes() };
    }
    case 'snapshot':
      return ctl.snapshot();
    default:
      throw Object.assign(new Error('unknown worker op ' + String(msg.op)), { code: 'invalid-argument' });
  }
}

parentPort.on('message', (msg) => {
  let value;
  try {
    value = handle(msg);
  } catch (error) {
    parentPort.postMessage({
      id: msg.id,
      ok: false,
      error: { message: String((error && error.message) || error), code: (error && error.code) || 'error' }
    });
    return;
  }
  parentPort.postMessage({ id: msg.id, ok: true, value });
});
`;

function createWorkerHandle() {
  const worker = new Worker(WORKER_SOURCE, { eval: true, workerData: { modulePath: OWNERSHIP_PATH } });
  let sequence = 0;

  function request(op, payload, timeoutMs) {
    const id = ++sequence;
    const limit = timeoutMs === undefined ? 3000 : timeoutMs;
    return new Promise((resolve, reject) => {
      let settled = false;
      let timer = null;
      const finish = (fn, value) => {
        if (settled) return;
        settled = true;
        if (timer) clearTimeout(timer);
        worker.off('message', onMessage);
        worker.off('error', onError);
        worker.off('exit', onExit);
        fn(value);
      };
      const onMessage = (message) => {
        if (!message || message.id !== id) return;
        if (message.ok) {
          finish(resolve, message.value);
        } else {
          const info = message.error || {};
          finish(reject, codedError(info.code || 'worker-error', info.message || 'worker error'));
        }
      };
      const onError = (error) => finish(reject, error);
      const onExit = (code) => finish(reject, codedError(
        'worker-exit', 'worker exited with code ' + String(code) + ' while handling ' + op));
      timer = setTimeout(() => finish(reject, codedError('test-timeout',
        'timed out waiting for ' + op)), limit);
      worker.on('message', onMessage);
      worker.on('error', onError);
      worker.on('exit', onExit);
      worker.postMessage(Object.assign({ id, op }, payload || {}));
    });
  }

  async function terminate() {
    await worker.terminate().catch(() => {});
  }

  return { request, terminate };
}

test('a real attached worker keeps the token live at CLOSING and retires it only after the shared commit',
  { timeout: 9000 }, async () => {
    const ctl = mod.create({ capacityBytes: 1 << 20, maxEntries: 8 });
    const handle = createWorkerHandle();
    let timer = null;
    const watchdog = new Promise((resolve, reject) => {
      timer = setTimeout(() => reject(codedError('test-timeout',
        'worker inspection test budget of 8000ms exceeded')), 8000);
    });

    const run = (async () => {
      const attached = await handle.request('attach', { descriptor: cloneValue(ctl.descriptor) });
      assert.equal(attached.attached, true, 'the worker must attach to the same shared descriptor');
      assert.equal(attached.heldBytes, 0);

      const reserved = ctl.reserve(2048, 3);
      assert.equal(reserved.ok, true);
      const token = reserved.token;
      assert.equal(ctl.bind(token, { data: 0x1000, buffer: 0x2000, size: 2048 }).ok, true);

      const remoteBound = await handle.request('inspect', { token: cloneValue(token) });
      assert.deepEqual(remoteBound, { ok: true, retired: false, state: 'bound', bytes: 2048, generation: 3 },
        'the attached worker must observe the same live BOUND record');

      const prepared = await handle.request('prepare', { data: 0x1000, buffer: 0x2000 });
      assert.equal(prepared.ok, true);
      assert.equal(prepared.ticketBytes, 2048);

      const closing = ctl.inspectReservation(token);
      assert.equal(closing.ok, true);
      assert.equal(closing.retired, false, 'CLOSING is still a live state for the owner observer');
      assert.equal(closing.state, 'closing');
      assert.equal(ctl.heldBytes(), 2048, 'the closing charge must still be held before the commit');
      assert.equal(ctl.snapshot().countClosing, 1);

      /* Explicit fixture message: the allocator reports success. Nothing is
       * really freed here; only the shared commit runs after this. */
      const success = await handle.request('allocatorSuccess', { dataFreed: true });
      assert.equal(success.fixture, true);
      assert.equal(success.dataFreed, true);

      const committed = await handle.request('commit', { dataFreed: success.dataFreed });
      assert.equal(committed.ok, true);
      assert.equal(committed.releasedBytes, 2048);
      assert.equal(committed.heldBytes, 0);

      const retired = ctl.inspectReservation(token);
      assert.equal(retired.ok, true);
      assert.equal(retired.retired, true, 'the owner must see retirement only after the shared commit');
      assert.equal(retired.state, 'retired');
      assert.equal(ctl.heldBytes(), 0);
      assert.equal(ctl.snapshot().countBound, 0);
      assert.equal(ctl.snapshot().countClosing, 0);

      const remoteFinal = await handle.request('snapshot', {});
      assert.deepEqual(remoteFinal, ctl.snapshot(),
        'both threads must observe the same final shared table');
    })();
    run.catch(() => {});

    try {
      await Promise.race([run, watchdog]);
    } finally {
      if (timer) clearTimeout(timer);
      await handle.terminate();
    }
  });
