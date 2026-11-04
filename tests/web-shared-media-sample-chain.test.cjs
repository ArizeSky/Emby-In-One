const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// Executes the vendored __eioDualDemux helper. The fake controller models only
// the 90187 reserve/bind/reclaim/inspect contract; fake o.f[15] models the
// actual wasm U32 reads used to prove packet -> ref -> body -> data.
const BUNDLE = path.join(__dirname, '../public/vendor/web/libmedia/avplayer.js');
const CORE = require(path.join(__dirname, '../web-src/player-shared-media-ownership.js'));
const HANDLES = require(path.join(__dirname, '../web-src/player-media-resource-handles.js'));
const ASYNC = { timeout: 2000 };

function bundleText() {
  return fs.readFileSync(BUNDLE, 'utf8');
}

function helperSource() {
  const text = bundleText();
  const begin = '/*__EIO_DUAL_CACHE_DEMUX_BEGIN__*/';
  const end = '/*__EIO_DUAL_CACHE_DEMUX_END__*/';
  const start = text.indexOf(begin);
  const stop = text.indexOf(end, start + begin.length);
  assert.ok(start > -1 && stop > start, 'DEMUX helper region is missing');
  return text.slice(start + begin.length, stop);
}

function loadHelper(mem, modules) {
  const context = {
    Map,
    Math,
    Number,
    isFinite,
    performance: { now: () => 0 },
    o: { f: { 15: (addr) => (mem.get(addr) || 0) >>> 0 } },
    setTimeout,
    clearTimeout,
    i: modules ? (id) => modules[id] : undefined
  };
  return vm.runInNewContext(`${helperSource()}\n;__eioDualDemux;`, context);
}

function makeController(capacityBytes = 1000) {
  const records = new Map();
  let held = 0;
  let nextId = 1;
  const ctrl = {
    capacityBytes,
    failReserve: null,
    bindFailure: null,
    get held() { return held; },
    entry(token) { return token && records.get(token.id); },
    reserve(bytes) {
      if (ctrl.failReserve) return { ok: false, code: ctrl.failReserve };
      if (bytes < 0 || held + bytes > capacityBytes) {
        return { ok: false, code: 'capacity-exceeded' };
      }
      const token = { brand: 'EioSharedMediaOwnership.token', entry: records.size, id: nextId++, bytes };
      records.set(token.id, { state: 'RESERVED', token, key: null });
      held += bytes;
      return { ok: true, token, heldBytes: held };
    },
    bind(token, payload) {
      const rec = records.get(token.id);
      if (!rec || rec.state !== 'RESERVED') return { ok: false, code: 'stale-token' };
      if (ctrl.bindFailure) return { ok: false, code: ctrl.bindFailure };
      rec.state = 'BOUND';
      rec.key = { data: payload.data, buffer: payload.buffer, size: payload.size };
      return { ok: true, key: rec.key, heldBytes: held };
    },
    reclaimUnused(token) {
      const rec = records.get(token.id);
      if (!rec) return { ok: false, code: 'stale-token' };
      if (rec.state !== 'RESERVED') return { ok: false, code: 'bound' };
      rec.state = 'RECLAIMED';
      held -= token.bytes;
      return { ok: true, reclaimedBytes: token.bytes, heldBytes: held };
    },
    inspectReservation(token) {
      const rec = token && records.get(token.id);
      if (!rec) return { ok: true, retired: true, state: 'retired' };
      if (rec.state === 'RESERVED' || rec.state === 'BOUND') {
        return { ok: true, retired: false, state: rec.state };
      }
      return { ok: true, retired: true, state: 'retired' };
    },
    free(token) {
      const rec = token && records.get(token.id);
      if (rec && rec.state === 'BOUND') {
        rec.state = 'FREE';
        held -= token.bytes;
      }
      return ctrl;
    },
    snapshot() {
      return { kind: 'eio-shared-media-ownership/v1', capacityBytes, heldBytes: held };
    }
  };
  return ctrl;
}

function newTask(helper, controller) {
  const ledger = helper.make({ demuxBytes: 1000, maxPacketBytes: 100 });
  ledger.high = 1000;
  ledger.packetBytes = 1000;
  ledger.maxPacket = 100;
  ledger.readCeil = 900;
  ledger.readCeilEff = 900;
  ledger.low = 800;
  ledger.lowEff = 800;
  ledger.probeBytes = 50;
  ledger.phase = 2;
  return { taskId: 'chain-task', eioLedger: ledger, eioSharedController: controller };
}

function wire(mem, packet, ref, body, data) {
  mem.set(packet, ref);
  mem.set(ref, body);
  mem.set(body, data);
}

test('cancel vs bound: shared reserve precedes local charge; only unbound tokens reclaim', ASYNC, async () => {
  const mem = new Map();
  wire(mem, 100, 200, 300, 400);
  wire(mem, 500, 600, 700, 400);
  const ctrl = makeController(100);
  const h = loadHelper(mem);
  const task = newTask(h, ctrl);
  const ledger = task.eioLedger;

  const bound = h.reserve(task, 10, { packet: 100 }, 2);
  assert.equal(bound.sharedToken.id, 1);
  assert.equal(ledger.held, 10);
  assert.equal(ctrl.held, 10);
  h.after(task, bound, { packet: 100, data: 400, size: 10 });
  assert.equal(bound.sharedBound, true);

  ctrl.failReserve = 'capacity-exceeded';
  assert.equal(h.reserve(task, 20, { packet: 101 }, 2), null);
  assert.equal(ledger.held, 10, 'shared refusal must not charge the local ledger');
  assert.equal(ctrl.held, 10);

  ledger.phase = 1;
  assert.equal(h.before(task, { size: 20, packet: 101 }), null);
  assert.equal(ledger.probeExhausted, true, 'PROBE shared refusal is explicit exhaustion');

  ledger.phase = 2;
  const pending = h.before(task, { size: 20, packet: 101 });
  assert.equal(typeof pending.then, 'function');
  assert.equal(ledger.waiterCount, 1);
  assert.ok(ledger.sharedPulseTimer != null, 'PLAY waiter gets the bounded reconcile pulse');
  h.cancelWaiters(task, 'test');
  assert.equal(await pending, null);
  assert.equal(ledger.waiterCount, 0);
  assert.equal(ledger.sharedPulseTimer, null);

  ctrl.failReserve = null;
  const unbound = h.reserve(task, 16, { packet: 102 }, 2);
  const ctrlHeld = ctrl.held;
  assert.equal(h.releaseToken(task, unbound, { reason: 'cancelled' }), 16);
  assert.equal(ledger.sharedReclaimedBytes, 16);
  assert.equal(ctrl.held, ctrlHeld - 16);
  assert.equal(ledger.t.has(unbound.id), false);

  assert.equal(h.releaseToken(task, bound, { reason: 'late' }), 0);
  assert.equal(ctrl.entry(bound.sharedToken).state, 'BOUND');

  const bad = h.reserve(task, 32, { packet: 500 }, 2);
  assert.throws(
    () => h.after(task, bad, { packet: 500, data: 999, size: 32 }),
    (error) => error && error.code === 'EIO_SHARED_BIND_FAILED'
  );
  assert.equal(bad.sharedBindFailed, true);
  const heldAfterFailure = ledger.held;
  assert.equal(h.releaseToken(task, bad, { reason: 'cancel' }), 0);
  assert.equal(ledger.held, heldAfterFailure, 'failed bind keeps the reservation');
  assert.equal(ctrl.entry(bad.sharedToken).state, 'RESERVED');
});

test('before -> modeled packet pointers -> bind -> simulated final free -> reconcile exact once', () => {
  const mem = new Map();
  wire(mem, 100, 200, 1200, 400);
  wire(mem, 300, 800, 900, 1000);
  const ctrl = makeController();
  const h = loadHelper(mem);
  const task = newTask(h, ctrl);
  const ledger = task.eioLedger;

  // before() is the real reservation point before the 943 sample malloc.
  const queued = h.before(task, { size: 40, packet: 100 });
  assert.ok(queued && queued.sharedToken);
  assert.equal(ledger.held, 40);
  h.after(task, queued, { packet: 100, data: 400, size: 40 });
  assert.equal(queued.sharedBound, true);
  assert.equal(ledger.sharedBindCount, 1);
  h.add(task, 'c', 0, 40, 100);
  ctrl.free(queued.sharedToken);
  assert.equal(h.reconcileShared(task), 1);
  assert.equal(ledger.held, 0);
  assert.equal(ledger.sharedFreeCount, 1);
  assert.equal(ledger.sharedFreedBytes, 40);
  assert.equal(ledger.t.has(queued.id), false);
  assert.equal(ledger.tokenByPacket.has(100), false);
  assert.equal(ledger.c.get(0), undefined);
  assert.equal(h.reconcileShared(task), 0, 'retirement is credited exactly once');

  const outbound = h.before(task, { size: 20, packet: 300 });
  h.after(task, outbound, { packet: 300, data: 1000, size: 20 });
  h.add(task, 'c', 1, 20, 300);
  h.outbound(task, 300);
  ctrl.free(outbound.sharedToken);
  assert.equal(h.reconcileShared(task), 1);
  assert.equal(ledger.outbound, 0);
  assert.equal(ledger.held, 0);

  const mismatch = h.before(task, { size: 30, packet: 300 });
  h.after(task, mismatch, { packet: 300, data: 1000, size: 30 });
  h.add(task, 'c', 1, 30, 300);
  mismatch.key = 0; // stale mapping; the real bucket still has key 1
  ctrl.free(mismatch.sharedToken);
  const heldBeforeMismatch = ledger.held;
  const errorsBeforeMismatch = ledger.protocolErrors;
  assert.equal(h.reconcileShared(task), 0);
  assert.equal(ledger.held, heldBeforeMismatch, 'bucket mismatch is not clamped away');
  assert.ok(ledger.protocolErrors > errorsBeforeMismatch);
  assert.equal(ledger.t.has(mismatch.id), true);
  mismatch.key = 1;
  assert.equal(h.reconcileShared(task), 1);
  assert.equal(ledger.held, 0);
});

test('the common handle owns reserve, bind and external final-free retirement', () => {
  const mem = new Map();
  wire(mem, 100, 200, 300, 400);
  const ctrl = CORE.create({ capacityBytes: 1000, maxEntries: 16 });
  const h = loadHelper(mem, { 90213: HANDLES });
  const task = newTask(h, ctrl);
  const ledger = task.eioLedger;

  const queued = h.before(task, { size: 40, packet: 100 });
  assert.ok(queued && queued.sharedHandle, 'the real controller uses a common Handle');
  assert.equal(queued.sharedToken.id, queued.sharedHandle.token.id);
  assert.equal(ledger.held, 40);
  assert.equal(ctrl.snapshot().heldBytes, 40);

  h.after(task, queued, { packet: 100, data: 400, size: 40 });
  assert.equal(queued.sharedBound, true);
  assert.equal(queued.sharedHandle.state, 'published');
  h.add(task, 'c', 0, 40, 100);

  const prepared = ctrl.prepareFinalFree({ data: 400, buffer: 300 });
  assert.equal(prepared.ok, true);
  assert.equal(ctrl.commitFinalFree(prepared.ticket, { dataFreed: true }).ok, true);
  assert.equal(ctrl.snapshot().heldBytes, 0);

  assert.equal(h.reconcileShared(task), 1);
  assert.equal(ledger.held, 0);
  assert.equal(ledger.sharedFreeCount, 1);
  assert.equal(ledger.sharedFreedBytes, 40);
  assert.equal(queued.sharedHandle.state, 'released');
  assert.equal(ledger.t.has(queued.id), false);
  assert.equal(h.reconcileShared(task), 0, 'the handle finishes exactly once');
});
