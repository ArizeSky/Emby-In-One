'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const { Worker } = require('node:worker_threads');
const api = require('../web-src/player-payload-transfer.js');
const core = require('../web-src/player-shared-media-ownership.js');
const owned = require('../web-src/player-owned-media-resources.js');
function fixture(capacityBytes = 100) {
  const control = core.create({ capacityBytes, maxEntries: 32 });
  const resources = owned.createOwnedResources(control);
  const manager = api.create({ resources, domain: 'synthetic-test-domain' });
  return { control, resources, manager };
}
function localSend(payloads, buffers, identity) {
  const received = structuredClone(payloads, { transfer: buffers });
  return api.receive(received, identity);
}
test('cancel cannot credit a backing already transferred outside send', () => {
  const f = fixture(), item = f.manager.prepare([new Uint8Array(8)], 0);
  const received = structuredClone(item.payloads, { transfer: item.buffers });
  assert.throws(() => item.cancel(), { code: 'EIO_PAYLOAD_CANCEL_OWNERSHIP_UNPROVEN' });
  assert.equal(received[0].byteLength, 8);
  assert.equal(f.control.snapshot().heldBytes, 8);
  assert.equal(f.manager.snapshot().retained, 1);
});
test('exact admission precedes independent compressed copies and receiver detachment precedes credit', async () => {
  const f = fixture();
  const source = new Uint8Array([1, 2, 3]);
  const transfer = f.manager.prepare([source, new Uint8Array([4, 5])], 0);
  assert.equal(f.control.snapshot().heldBytes, 5);
  assert.notEqual(transfer.payloads[0].buffer, source.buffer);
  assert.deepEqual([...transfer.payloads[0]], [1, 2, 3]);
  await transfer.send((payloads, buffers, identity) => {
    assert.equal(f.control.snapshot().heldBytes, 5);
    const received = structuredClone(payloads, { transfer: buffers });
    assert.equal(payloads[0].buffer.detached, true);
    assert.equal(f.control.snapshot().heldBytes, 5, 'transfer does not free receiver backing');
    const ack = api.receive(received, identity);
    assert.equal(received[0].buffer.detached, true);
    assert.equal(f.control.snapshot().heldBytes, 5, 'receiver proof has not reached sender yet');
    return ack;
  });
  assert.equal(f.control.snapshot().heldBytes, 0);
  assert.deepEqual([...source], [1, 2, 3]);
});
test('byte-first refusal leaves source intact and performs no transport', () => {
  const f = fixture(4), source = new Uint8Array(5);
  assert.throws(() => f.manager.prepare([source], 0), { code: 'EIO_PAYLOAD_ADMISSION_REFUSED' });
  assert.equal(source.buffer.detached, false);
  assert.equal(f.control.snapshot().heldBytes, 0);
});
test('cancellation before send intrinsically detaches all private copies exactly once', async () => {
  const f = fixture(), item = f.manager.prepare([new Uint8Array(8)], 0);
  assert.equal(item.cancel(), true);
  assert.equal(item.payloads[0].buffer.detached, true);
  assert.equal(item.cancel(), false);
  await assert.rejects(item.send(localSend), { code: 'EIO_PAYLOAD_STATE_INVALID' });
  assert.equal(f.control.snapshot().heldBytes, 0);
});
test('missing, malformed and stale acknowledgments retain the exact charge', async () => {
  for (const mutate of [() => undefined, () => -2097152,
    ack => ({ ...ack, operation: ack.operation + 1 }), ack => ({ ...ack, instance: '0'.repeat(32) }),
    ack => ({ ...ack, bytes: ack.bytes + 1 })]) {
    const f = fixture(), item = f.manager.prepare([new Uint8Array(7)], 0);
    await assert.rejects(item.send((payloads, buffers, identity) => mutate(localSend(payloads, buffers, identity))),
      { code: 'EIO_PAYLOAD_ACK_UNVERIFIED' });
    assert.equal(f.control.snapshot().heldBytes, 7);
    assert.equal(f.manager.snapshot().retained, 1);
  }
});
test('ACK alone cannot credit undetached sender payloads', async () => {
  const f = fixture(), item = f.manager.prepare([new Uint8Array(3)], 0);
  await assert.rejects(item.send((_payloads, buffers, identity) => ({ ...identity,
    eioPayloadAck: 1, buffers: buffers.length, detached: true })), { code: 'EIO_PAYLOAD_SENDER_STILL_OWNS' });
  assert.equal(f.control.snapshot().heldBytes, 3);
});
test('send rejection after transfer retains receiver bytes; no cancel/reset credit', async () => {
  const f = fixture(), item = f.manager.prepare([new Uint8Array(3)], 0);
  let received;
  await assert.rejects(item.send((payloads, buffers) => {
    received = structuredClone(payloads, { transfer: buffers });
    throw new Error('synthetic send failure');
  }));
  assert.equal(received[0].byteLength, 3);
  assert.equal(item.cancel(), false);
  assert.equal(f.control.snapshot().heldBytes, 3);
});
test('receiver refuses duplicates, shared, resizable and oversized backing before any detachment', () => {
  const f = fixture(), item = f.manager.prepare([new Uint8Array(4), new Uint8Array(4)], 0);
  for (const payloads of [[item.payloads[0], item.payloads[0]],
    [new Uint8Array(new SharedArrayBuffer(8))],
    [new Uint8Array(new ArrayBuffer(8, { maxByteLength: 8 }))],
    [new Uint8Array(new ArrayBuffer(16), 0, 8)]]) {
    assert.throws(() => api.receive(payloads, item.identity), { code: 'EIO_PAYLOAD_BACKING_INVALID' });
  }
  assert.equal(item.payloads[0].buffer.detached, false);
  item.cancel();
});
test('a real Node Worker transfers and explicitly detaches source buffers before ACK', { timeout: 5000 }, async t => {
  const f = fixture(), item = f.manager.prepare([new Uint8Array([7, 8, 9]), new Uint8Array(0)], 0);
  const worker = new Worker(`const {parentPort,workerData}=require('node:worker_threads');
    const api=require(workerData);parentPort.on('message',message=>{
      parentPort.postMessage(api.receive(message.payloads,message.identity));
    });`, { eval: true, workerData: require.resolve('../web-src/player-payload-transfer.js') });
  t.after(() => worker.terminate());
  await item.send((payloads, buffers, identity) => new Promise((resolve, reject) => {
    worker.once('message', resolve); worker.once('error', reject);
    worker.postMessage({ payloads, identity }, buffers);
  }));
  assert.equal(f.control.snapshot().heldBytes, 0);
  assert.equal(f.manager.snapshot().pending, 0);
});
