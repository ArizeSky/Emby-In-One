'use strict';

/*
 * §5.3 schema-freeze tests: the frozen dual-cache snapshot schema and its
 * exact validator. The baseline fixture snapshot is a real observed
 * AVPlayer.getEioDualCacheSnapshot() result (synthetic MKV dual run,
 * 2026-11-05); every mutation below must be caught as a violation.
 */

const test = require('node:test');
const assert = require('node:assert/strict');
const path = require('node:path');

const schema = require(path.join(__dirname, '..', 'web-src',
  'player-dual-cache-snapshot-schema.js'));

function observedSnapshot() {
  return {
    taskId: 'ffde0bfe-512a-46bc-9080-1b898',
    active: true,
    complete: false,
    incomplete: [
      { domain: 'demux', reason: 'INTERVAL_UNACCOUNTED' },
      { domain: 'demux', reason: 'SAMPLE_RELEASE_UNVERIFIED' },
      { domain: 'demux', reason: 'HEADER_UNACCOUNTED' }
    ],
    io: {
      enabled: true, active: true, probe: false, ioBytes: 3000000,
      maxPacketBytes: 512000, residual: 0, residualPushback: 0,
      residualReserved: 0, ownedTotal: 0, chunks: 0, generation: 0,
      readerMode: 'byob', byobCapable: true
    },
    demux: {
      active: true, enabled: true, taskId: 'ffde0bfe',
      demuxBytes: 5000000, maxPacketBytes: 512000, readCeil: 4488000,
      readCeilEffective: 3976000, fixedBytes: 512000, packetBytes: 4488000,
      room: 4488000, held: 0, admissionHeld: 0, cacheTotal: 0,
      pendingTotal: 0, generation: 0, latched: false, errors: 0,
      lastError: '', intervalPacketsAccounted: false,
      sharedBindCount: 0, sharedFreeCount: 0, sharedFreedBytes: 0,
      liveBoundMirrors: 0, intervalBytes: 0, intervalPackets: 0,
      headerBytes: 0, headerPictureBindings: 0, avccNativeBindings: 0,
      avccNativeTemps: 0, ghostSwept: 0, ownershipTransitionCount: 0,
      phase: 0, probeBytes: 0, probeHeld: 0, probeRequests: 0,
      probeExhausted: false, maxForwardSeconds: 12, maxForwardMs: 12000,
      clockKnown: false, clockMs: null, clockEpoch: null,
      bootstrapBaseMs: null, temporalWaits: 0, temporalRejections: 0,
      temporalAuthority: 'native-timeupdate', temporalComplete: false,
      sharedInspectBusy: 0, tHeld: 0, localTokenBytes: 0, outboundBytes: 0,
      unprovenBytes: 0, cancelledBytes: 0, abandonedBytes: 0,
      waiterCount: 0, wakeCount: 0, waitCount: 0, timeoutCount: 0,
      protocolErrors: 0, staleTokens: 0, pointerReuse: 0,
      releaseVerified: false, stopAck: null, producerError: null,
      producerOperations: 0, batchWaiterCount: 0,
      sampleHandles: { liveBytes: 0, liveCount: 0 },
      accounting: { sampleAlloc: true, sampleRelease: false }
    },
    mse: { active: true, complete: false }
  };
}

test('the frozen schema is a frozen literal with a version', () => {
  assert.equal(schema.VERSION, 1);
  assert.equal(schema.KIND, 'eio-dual-cache-snapshot-schema/v1');
  assert.ok(Object.isFrozen(schema.FROZEN_SCHEMA));
  assert.ok(Object.isFrozen(schema.FROZEN_SCHEMA.demux.fields));
});

test('a real observed snapshot validates exactly, complete:false stays honest', () => {
  const result = schema.validate(observedSnapshot());
  assert.deepEqual(result.violations, []);
  assert.equal(result.ok, true);
  assert.equal(result.exactValidation, true);
  assert.equal(result.complete, false, 'complete:false is evidence, not a violation');
  assert.deepEqual(result.incomplete, ['INTERVAL_UNACCOUNTED',
    'SAMPLE_RELEASE_UNVERIFIED', 'HEADER_UNACCOUNTED']);
  assert.equal(result.active, true);
});

test('structural damage is caught: missing, wrong type, negative, null', () => {
  const cases = [
    { mutate: s => { delete s.demux.held; },
      expect: 'demux.held:missing' },
    { mutate: s => { s.demux.held = 'many'; },
      expect: 'demux.held:type(string!=number)' },
    { mutate: s => { s.demux.held = -1; },
      expect: 'demux.held:negative(-1)' },
    { mutate: s => { s.taskId = null; },
      expect: 'taskId:null' },
    { mutate: s => { s.incomplete = 'nope'; },
      expect: 'incomplete:type(string!=array)' },
    { mutate: s => { s.incomplete[0] = { domain: 7, reason: 'X' }; },
      expect: 'incomplete[0].domain:type(number!=string)' }
  ];
  for (const item of cases) {
    const snapshot = observedSnapshot();
    item.mutate(snapshot);
    const result = schema.validate(snapshot);
    assert.equal(result.ok, false);
    assert.ok(result.violations.includes(item.expect),
      item.expect + ' in ' + JSON.stringify(result.violations));
  }
});

test('nullable domains validate as null without violations', () => {
  const snapshot = observedSnapshot();
  snapshot.io = null;
  snapshot.mse = null;
  snapshot.demux = null;
  const result = schema.validate(snapshot);
  assert.deepEqual(result.violations, []);
  assert.equal(result.ok, true);
});

test('demux conservation invariants are enforced only when active', () => {
  const room = observedSnapshot();
  room.demux.admissionHeld = 100;
  room.demux.room = 999;
  let result = schema.validate(room);
  assert.equal(result.ok, false);
  assert.ok(result.violations.some(v => v.indexOf('demux:room!=') === 0),
    'room identity: ' + JSON.stringify(result.violations));

  const held = observedSnapshot();
  held.demux.held = 100;
  held.demux.admissionHeld = 200;
  held.demux.room = 4488000 - 200;
  result = schema.validate(held);
  assert.equal(result.ok, false);
  assert.ok(result.violations.includes('demux:held<admissionHeld(100<200)'));

  const inactive = observedSnapshot();
  inactive.demux.active = false;
  inactive.demux.held = 100;
  inactive.demux.admissionHeld = 200;
  inactive.demux.room = 4488000 - 200;
  result = schema.validate(inactive);
  assert.equal(result.ok, true,
    'inactive ledger values are reported, not conservation-checked');
});

test('bigint-sanitized numeric strings are accepted for number fields', () => {
  const snapshot = observedSnapshot();
  snapshot.demux.clockEpoch = '12345n';
  const result = schema.validate(snapshot);
  assert.deepEqual(result.violations, []);
  assert.equal(result.ok, true);
});

test('non-object roots fail closed', () => {
  for (const value of [null, undefined, 7, 'text', []]) {
    const result = schema.validate(value);
    assert.equal(result.ok, false);
    assert.equal(result.exactValidation, false);
  }
});
