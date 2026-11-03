'use strict';

/*
 * Module 90193 ownership-proof tests.
 *
 * The module under test is the actual shipped UMD source in
 * public/vendor/web/libmedia/mse-packet-ownership.js, evaluated from its real
 * webpack chunk 90207 push. The packet pool is a fake native pool; no browser,
 * Worker, real AVPacketPool, upstream or production instance is involved.
 */

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const ROOT = path.join(__dirname, '..');
const OWNERSHIP_PATH = path.join(ROOT, 'public', 'vendor', 'web', 'libmedia', 'mse-packet-ownership.js');
const OWNERSHIP_SRC = fs.readFileSync(OWNERSHIP_PATH, 'utf8');

function evaluateChunk(source, file) {
  const context = vm.createContext({
    self: { webpackChunkAVPlayer: [] },
    console, setTimeout, clearTimeout, Promise, Map, Set, Object, Array,
    Error, Number, String, BigInt, Math, JSON, Uint8Array
  });
  vm.runInContext(source, context, { filename: file });
  return context.self.webpackChunkAVPlayer[0];
}

function loadOwnershipModule() {
  const push = evaluateChunk(OWNERSHIP_SRC, OWNERSHIP_PATH);
  assert.ok(push && push[0][0] === 90207, 'actual chunk 90207 registration');
  const factory = push[1][90193];
  assert.equal(typeof factory, 'function', 'actual module 90193 factory');
  const moduleObject = { exports: {} };
  factory(moduleObject, moduleObject.exports, () => {
    throw new Error('unexpected ownership require');
  });
  return moduleObject.exports;
}

const OWNERSHIP = loadOwnershipModule();

function fakePool() {
  const freed = [];
  return {
    freed,
    release(pointer) {
      if (!(pointer > 0)) throw new Error('invalid packet pointer');
      freed.push(pointer);
      return undefined;
    }
  };
}

function makeController(pool) {
  return OWNERSHIP.createMsePacketOwnership({ packetPool: pool || fakePool() });
}

function ownedStream(overrides) {
  return Object.assign({
    eioOwned: {},
    backPacket: 0,
    frontPacket: 0,
    pullQueue: { queue: [] }
  }, overrides || {});
}

function giveSlot(controller, stream, pointer, slot) {
  const ticket = controller.begin(stream, 'proof-slot');
  assert.ok(ticket);
  assert.equal(controller.check(ticket, pointer), pointer);
  assert.equal(controller.transferSlot(ticket, pointer, slot), true);
  return { ticket, record: stream.eioPullPackets.get(pointer) };
}

function giveQueue(controller, stream, pointer, queue) {
  const ticket = controller.begin(stream, 'proof-queue');
  assert.ok(ticket);
  assert.equal(controller.check(ticket, pointer), pointer);
  assert.equal(controller.transferQueue(ticket, pointer, queue || stream.pullQueue.queue), true);
  return { ticket, record: stream.eioPullPackets.get(pointer) };
}

test('baseline non-strict streams are untouched by slot, queue and teardown proof', () => {
  const pool = fakePool();
  const controller = makeController(pool);
  const stream = { backPacket: 11, frontPacket: 22, pullQueue: { queue: [11, 22] } };

  assert.equal(controller.releaseSlot(stream, 'back', 'baseline'), false);
  assert.equal(controller.releaseQueue(stream, 'baseline'), false);
  assert.deepEqual({ ...controller.prepareTeardown(stream, 'baseline') },
    { ok: true, pending: 0, code: null });
  assert.deepEqual(pool.freed, []);
  assert.deepEqual(stream.pullQueue.queue, [11, 22]);
  assert.equal(stream.backPacket, 11);
  assert.equal(stream.frontPacket, 22);
  assert.equal('eioPullPackets' in stream, false);
  assert.equal('eioPullFault' in stream, false);
});

test('normal front/back slot release and queue release free only the proven records', () => {
  const pool = fakePool();
  const controller = makeController(pool);
  const stream = ownedStream();
  const back = giveSlot(controller, stream, 101, 'back');
  const front = giveSlot(controller, stream, 102, 'front');
  const queue = stream.pullQueue.queue;
  const queued = giveQueue(controller, stream, 103, queue);

  assert.equal(controller.releaseSlot(stream, 'back', 'normal'), true);
  assert.equal(stream.eioPullSlotOwners.back, null);
  assert.equal(stream.eioPullSlotOwners.front, front.record);
  assert.equal(stream.eioPullPackets.has(back.record.pointer), false);
  assert.equal(controller.releaseSlot(stream, 'front', 'normal'), true);
  assert.equal(stream.eioPullSlotOwners.front, null);
  assert.equal(stream.eioPullPackets.has(front.record.pointer), false);
  assert.equal(controller.releaseQueue(stream, 'normal'), true);
  assert.deepEqual(queue, []);
  assert.deepEqual(Array.from(stream.eioPullQueueOwners), []);
  assert.equal(stream.eioPullPackets.has(queued.record.pointer), false);
  assert.deepEqual(pool.freed, [101, 102, 103]);
  assert.equal(stream.eioPullPackets.size, 0);
});

test('slot vacancy is safe true while a zero pointer with a live owner is a mismatch', () => {
  const pool = fakePool();
  const controller = makeController(pool);
  const stream = ownedStream();

  assert.equal(controller.releaseSlot(stream, 'back', 'vacant'), true);
  assert.deepEqual(pool.freed, []);

  const given = giveSlot(controller, stream, 300, 'back');
  stream.backPacket = 0;
  assert.equal(controller.releaseSlot(stream, 'back', 'zero-live-owner'), false);
  assert.deepEqual(pool.freed, []);
  assert.equal(stream.eioPullSlotOwners.back, given.record);
  assert.equal(given.record.state, 'transferred');
  assert.equal(stream.eioPullFault.kind, 'slot-pointer');
});

test('slot release refuses every non-exact association before any native free', () => {
  const cases = [
    ['slot-pointer', (stream) => { stream.backPacket = 999; }],
    ['slot-state', (stream, record) => { record.state = 'owned'; }],
    ['slot-index', (stream, record) => { record.slot = 'front'; }],
    ['slot-queue', (stream, record) => { record.queue = []; }],
    ['slot-map', (stream) => { stream.eioPullPackets.set(301, { pointer: 301 }); }]
  ];

  for (const [kind, mutate] of cases) {
    const pool = fakePool();
    const controller = makeController(pool);
    const stream = ownedStream();
    const given = giveSlot(controller, stream, 301, 'back');
    mutate(stream, given.record);
    const pointerBefore = stream.backPacket;
    const mapBefore = stream.eioPullPackets.get(301);

    assert.equal(controller.releaseSlot(stream, 'back', 'mismatch'), false, kind);
    assert.deepEqual(pool.freed, [], kind + ' must not free');
    assert.equal(stream.backPacket, pointerBefore, kind + ' must not zero the pointer');
    assert.equal(stream.eioPullSlotOwners.back, given.record, kind + ' must retain the owner');
    assert.equal(stream.eioPullPackets.get(301), mapBefore, kind + ' must retain the map identity');
    assert.equal(stream.eioPullFault.code, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT');
    assert.equal(stream.eioPullFault.kind, kind);
  }
});

test('queue release refuses pointer, reference, state, slot, map and length mismatches before free', () => {
  const cases = [
    ['queue-pointer', (stream) => { stream.pullQueue.queue[0] = 999; }],
    ['queue-reference', (stream, record) => { record.queue = []; }],
    ['queue-state', (stream, record) => { record.state = 'owned'; }],
    ['queue-slot', (stream, record) => { record.slot = 'back'; }],
    ['queue-map', (stream) => { stream.eioPullPackets.set(401, { pointer: 401 }); }],
    ['queue-length', (stream) => { stream.pullQueue.queue.push(999); }]
  ];

  for (const [kind, mutate] of cases) {
    const pool = fakePool();
    const controller = makeController(pool);
    const stream = ownedStream();
    const given = giveQueue(controller, stream, 401);
    mutate(stream, given.record);
    const queueBefore = stream.pullQueue.queue.slice();
    const ownersBefore = stream.eioPullQueueOwners.slice();

    assert.equal(controller.releaseQueue(stream, 'mismatch'), false, kind);
    assert.deepEqual(pool.freed, [], kind + ' must not free');
    assert.deepEqual(stream.pullQueue.queue, queueBefore, kind + ' must not mutate the queue');
    assert.deepEqual(stream.eioPullQueueOwners, ownersBefore, kind + ' must not mutate queue owners');
    assert.equal(stream.eioPullFault.code, 'EIO_MUX_PULL_OWNERSHIP_CONFLICT');
    assert.equal(stream.eioPullFault.kind, kind);
  }
});

test('reentrant map replacement after native release is a conflict, not blind success', () => {
  const pool = fakePool();
  const controller = makeController(pool);
  const stream = ownedStream();
  const given = giveSlot(controller, stream, 501, 'back');
  const oldRecord = given.record;
  const replacement = {
    pointer: 777, ticketId: 999, ticket: null, state: 'transferred',
    slot: 'back', queue: null, reason: null
  };
  pool.release = function (pointer) {
    pool.freed.push(pointer);
    stream.eioPullPackets = new Map([[777, replacement]]);
    stream.eioPullSlotOwners = { back: replacement, front: null };
    stream.backPacket = 777;
    return undefined;
  };

  assert.equal(controller.releaseSlot(stream, 'back', 'reentrant-map'), false);
  assert.deepEqual(pool.freed, [501], 'the native pool saw exactly one old pointer');
  assert.equal(oldRecord.state, 'freed', 'the native free is not hidden');
  assert.equal(stream.eioPullFault.kind, 'release-association');
  assert.equal(stream.eioPullSlotOwners.back, replacement);
  assert.equal(stream.backPacket, 777);
  assert.equal(stream.eioPullPackets.get(777), replacement);
  assert.equal(stream.eioPullPackets.has(501), false);
  assert.equal(controller.releasePulled(given.ticket, 501, oldRecord, 'old-handle'), false);
  assert.deepEqual(pool.freed, [501], 'the old record is never freed twice');
});

test('reentrant slot or queue replacement after native release leaves the new association intact', () => {
  {
    const pool = fakePool();
    const controller = makeController(pool);
    const stream = ownedStream();
    const given = giveSlot(controller, stream, 502, 'back');
    const replacement = {
      pointer: 888, ticketId: 999, ticket: null, state: 'transferred',
      slot: 'back', queue: null, reason: null
    };
    pool.release = function (pointer) {
      pool.freed.push(pointer);
      stream.eioPullSlotOwners = { back: replacement, front: null };
      stream.backPacket = 888;
      return undefined;
    };

    assert.equal(controller.releaseSlot(stream, 'back', 'reentrant-slot'), false);
    assert.deepEqual(pool.freed, [502]);
    assert.equal(stream.eioPullFault.kind, 'slot-association');
    assert.equal(stream.eioPullSlotOwners.back, replacement);
    assert.equal(stream.backPacket, 888);
    assert.equal(stream.eioPullPackets.has(given.record.pointer), false);
  }

  {
    const pool = fakePool();
    const controller = makeController(pool);
    const stream = ownedStream();
    const given = giveQueue(controller, stream, 503);
    const replacementQueue = [888];
    const replacement = {
      pointer: 888, ticketId: 999, ticket: null, state: 'transferred',
      slot: 'queue', queue: replacementQueue, reason: null
    };
    pool.release = function (pointer) {
      pool.freed.push(pointer);
      stream.pullQueue.queue = replacementQueue;
      stream.eioPullQueueOwners = [replacement];
      return undefined;
    };

    assert.equal(controller.releaseQueue(stream, 'reentrant-queue'), false);
    assert.deepEqual(pool.freed, [503]);
    assert.equal(stream.eioPullFault.kind, 'queue-association');
    assert.deepEqual(stream.pullQueue.queue, replacementQueue);
    assert.equal(stream.eioPullQueueOwners[0], replacement);
    assert.equal(stream.eioPullQueueOwners.length, 1);
    assert.equal(stream.eioPullPackets.has(given.record.pointer), false);
  }
});

test('reentrant same-map replacement is not deleted or claimed as verified success', () => {
  const pool = fakePool();
  const controller = makeController(pool);
  const stream = ownedStream();
  const given = giveSlot(controller, stream, 504, 'back');
  const replacement = {
    pointer: 504, ticketId: 999, ticket: null, state: 'transferred',
    slot: 'back', queue: null, reason: null
  };
  pool.release = function (pointer) {
    pool.freed.push(pointer);
    stream.eioPullPackets.set(504, replacement);
    return undefined;
  };

  assert.equal(controller.releaseSlot(stream, 'back', 'reentrant-same-map'), false);
  assert.deepEqual(pool.freed, [504]);
  assert.equal(stream.eioPullFault.kind, 'release-association');
  assert.equal(stream.eioPullPackets.get(504), replacement);
  assert.equal(stream.eioPullSlotOwners.back, given.record);
  assert.equal(stream.backPacket, 504);
});

test('prepareTeardown reuses exact slot and queue proof and clears only proven pointers', () => {
  const pool = fakePool();
  const controller = makeController(pool);
  const stream = ownedStream();
  giveSlot(controller, stream, 601, 'back');
  giveSlot(controller, stream, 602, 'front');
  giveQueue(controller, stream, 603);

  assert.deepEqual({ ...controller.prepareTeardown(stream, 'teardown') },
    { ok: true, pending: 0, code: null });
  assert.deepEqual(pool.freed, [601, 602, 603]);
  assert.equal(stream.backPacket, 0);
  assert.equal(stream.frontPacket, 0);
  assert.deepEqual(stream.pullQueue.queue, []);
  assert.deepEqual(Array.from(stream.eioPullQueueOwners), []);
  assert.equal(stream.eioPullPackets.size, 0);

  const slotPool = fakePool();
  const slotController = makeController(slotPool);
  const slotStream = ownedStream();
  const slot = giveSlot(slotController, slotStream, 604, 'back');
  slotStream.backPacket = 999;
  const slotProof = slotController.prepareTeardown(slotStream, 'teardown-mismatch');
  assert.equal(slotProof.ok, false);
  assert.equal(slotProof.kind, 'slot');
  assert.deepEqual(slotPool.freed, []);
  assert.equal(slotStream.backPacket, 999);
  assert.equal(slotStream.eioPullSlotOwners.back, slot.record);
  assert.equal(slot.record.state, 'transferred');

  const queuePool = fakePool();
  const queueController = makeController(queuePool);
  const queueStream = ownedStream();
  const queued = giveQueue(queueController, queueStream, 605);
  queueStream.pullQueue.queue[0] = 999;
  const queueProof = queueController.prepareTeardown(queueStream, 'teardown-mismatch');
  assert.equal(queueProof.ok, false);
  assert.equal(queueProof.kind, 'queue');
  assert.deepEqual(queuePool.freed, []);
  assert.deepEqual(queueStream.pullQueue.queue, [999]);
  assert.equal(queueStream.eioPullQueueOwners[0], queued.record);
  assert.equal(queued.record.state, 'transferred');
});

test('existing retained records block teardown without a native free', () => {
  const pool = fakePool();
  const controller = makeController(pool);
  const stream = ownedStream();
  const given = giveSlot(controller, stream, 606, 'back');
  given.record.state = 'retained';
  given.record.reason = 'pool-closed';

  const proof = controller.prepareTeardown(stream, 'teardown-retained');
  assert.equal(proof.ok, false);
  assert.equal(proof.pending, 1);
  assert.deepEqual(pool.freed, []);
  assert.equal(stream.eioPullPackets.get(606), given.record);
  assert.equal(stream.eioPullSlotOwners.back, given.record);
  assert.equal(stream.backPacket, 606, 'the retained slot pointer was not zeroed');
});

test('void pool success is accepted while false and null are retained without map deletion', () => {
  for (const refusal of [undefined, false, null]) {
    const pool = fakePool();
    const controller = makeController(pool);
    const stream = ownedStream();
    const given = giveSlot(controller, stream, 701, 'back');
    if (refusal !== undefined) {
      pool.release = function (pointer) {
        pool.freed.push(pointer);
        return refusal;
      };
    }

    const released = controller.releaseSlot(stream, 'back', 'pool-abi');
    if (refusal === undefined) {
      assert.equal(released, true);
      assert.equal(given.record.state, 'freed');
      assert.equal(stream.eioPullPackets.has(701), false);
    } else {
      assert.equal(released, false);
      assert.equal(given.record.state, 'retained');
      assert.equal(stream.eioPullPackets.get(701), given.record);
      assert.equal(stream.eioPullSlotOwners.back, given.record);
      assert.equal(stream.eioPullFault.kind, 'release');
    }
    assert.deepEqual(pool.freed, [701]);
  }
});

test('releasePulled expected record identity refuses pointer reuse and accepts only the exact handle', () => {
  const pool = fakePool();
  const controller = makeController(pool);
  const stream = ownedStream();
  const first = controller.begin(stream, 'aba');
  assert.equal(controller.check(first, 801), 801);
  const oldRecord = stream.eioPullPackets.get(801);
  controller.reclaim(first, 'old-freed');
  assert.deepEqual(pool.freed, [801]);

  const second = controller.begin(stream, 'aba');
  assert.equal(controller.check(second, 801), 801);
  const newRecord = stream.eioPullPackets.get(801);
  assert.notEqual(newRecord, oldRecord);

  assert.equal(controller.releasePulled(first, 801, 'old-continuation'), false);
  assert.equal(controller.releasePulled(first, 801, oldRecord, 'old-handle'), false);
  assert.equal(controller.releasePulled(second, 801, 'wrong-handle', oldRecord), false);
  assert.deepEqual(pool.freed, [801]);
  assert.equal(stream.eioPullPackets.get(801), newRecord);

  assert.equal(controller.releasePulled(second, 801, 'exact-handle', newRecord), true);
  assert.deepEqual(pool.freed, [801, 801]);
  assert.equal(stream.eioPullPackets.has(801), false);

  const third = controller.begin(stream, 'aba');
  assert.equal(controller.check(third, 802), 802);
  const thirdRecord = stream.eioPullPackets.get(802);
  assert.equal(controller.releasePulled(third, thirdRecord, 'object-handle'), true);
  assert.deepEqual(pool.freed, [801, 801, 802]);
});

test('takeQueue, moveSlot and moveSlotToQueue refuse desynchronized ownership identities', () => {
  {
    const pool = fakePool();
    const controller = makeController(pool);
    const stream = ownedStream();
    const given = giveQueue(controller, stream, 901);
    stream.eioPullPackets.set(901, { pointer: 901 });
    assert.throws(() => controller.takeQueue(stream, given.ticket),
      { code: 'EIO_MUX_PULL_OWNERSHIP_CONFLICT' });
    assert.deepEqual(stream.pullQueue.queue, [901]);
    assert.equal(stream.eioPullQueueOwners[0], given.record);
    assert.equal(given.record.state, 'transferred');
  }

  {
    const pool = fakePool();
    const controller = makeController(pool);
    const stream = ownedStream();
    const given = giveSlot(controller, stream, 902, 'front');
    given.record.slot = 'back';
    assert.equal(controller.moveSlot(stream, 'front', 'back'), false);
    assert.equal(stream.frontPacket, 902);
    assert.equal(stream.backPacket, 0);
    assert.equal(stream.eioPullSlotOwners.front, given.record);
  }

  {
    const pool = fakePool();
    const controller = makeController(pool);
    const stream = ownedStream();
    const given = giveSlot(controller, stream, 903, 'back');
    stream.eioPullQueueOwners.push(null);
    assert.throws(() => controller.moveSlotToQueue(stream, 'back', stream.pullQueue.queue),
      { code: 'EIO_MUX_PULL_OWNERSHIP_CONFLICT' });
    assert.equal(stream.backPacket, 903);
    assert.equal(stream.eioPullSlotOwners.back, given.record);
    assert.deepEqual(stream.pullQueue.queue, []);
  }
});
