'use strict';

/*
 * Stub-boundary tests for the strict-only steady-loop diagnostic capture
 * (MSEPipeline.eioRunLoopStep) and the read-only getEioMSEProgress RPC of
 * public/vendor/web/libmedia/26.avplayer.js.
 *
 * Evidence limits (deliberate and explicit):
 *   - Both methods are sliced out of the real chunk text and executed in a vm
 *     context with fake module globals (wallclock C.A clock, AVPacket field
 *     reader s.f[17], timebase converter h.Mr, ms timebase m.i0) plus fake
 *     task/stream/track/SourceBuffer objects. They do NOT prove a real
 *     generated Worker, a real SourceBuffer, a real muxer or real playback
 *     progress. No browser, network, upstream, application server or
 *     production instance is used.
 *   - Nothing here asserts any algorithm change: only observation plumbing,
 *     null-instead-of-invented values, pointer non-exposure and rethrow.
 */

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');

const SOURCE = fs.readFileSync(path.join(__dirname,
  '../public/vendor/web/libmedia/26.avplayer.js'), 'utf8');

function between(text, start, end) {
  const a = text.indexOf(start);
  assert.ok(a >= 0, 'missing marker ' + start);
  const b = text.indexOf(end, a + start.length);
  assert.ok(b > a, 'missing end marker ' + end);
  return text.slice(a, b);
}

const HELPERS = between(SOURCE, '/* EIO-MSE-PROGRESS:BEGIN', '/* EIO-MSE-PROGRESS:END */');
const METHODS = between(SOURCE, 'eioRunLoopStep(t,run){', 'eioOwnedManager(){');
const MUX_METHODS = between(SOURCE, 'eioMuxGate(task){', 'async beforeSeek(taskId){');
const MUX = require('./helpers/mux-lifecycle.cjs')();

/* Fake globals: an unmapped packet offset must be an observable read error, not
 * a zero DTS. The clock is a stub, so no real wallclock time is claimed. */
function fakeApi(options) {
  const opts = options || {};
  const memory = opts.memory || {};
  const calls = opts.calls || { mr: [] };
  const globals = {
    h: {
      Mr: (dts, params, timeBase) => {
        calls.mr.push([dts, params, timeBase]);
        return BigInt(dts) / 1000n;
      }
    },
    s: {
      f: {
        17: (pointer) => {
          if (!Object.prototype.hasOwnProperty.call(memory, pointer)) {
            throw new Error('unmapped fake AVPacket offset');
          }
          return memory[pointer];
        }
      }
    },
    m: { i0: { num: 1, den: 1000 } },
    C: { A: opts.clock || (() => 1234.5) },
    i: (id) => { assert.equal(id, 90198); return MUX; }
  };
  const api = vm.runInNewContext(HELPERS + ';(new (class {' + MUX_METHODS + METHODS + '})())', globals);
  api.calls = calls;
  return api;
}

/* vm objects have their own realm prototypes, so compare observed values as
 * JSON-plain host data instead of cross-realm prototypes. */
function plain(value) {
  return JSON.parse(JSON.stringify(value));
}

function fakeRanges(pairs) {
  return {
    length: pairs.length,
    start: (index) => pairs[index][0],
    end: (index) => pairs[index][1]
  };
}

function fakeStream(overrides) {
  return Object.assign({
    type: 'audio',
    ended: false,
    packetEnded: false,
    frontBuffered: true,
    backPacket: 0,
    frontPacket: 0,
    pullQueue: { queue: [] },
    loop: { isStarted: () => false },
    lastDTS: -1n,
    startTimestamp: 1700000000000n,
    eioLoopError: null,
    track: { getSourceBuffer: () => null }
  }, overrides);
}

const STREAM_KEYS = [
  'backPacketDtsMs', 'backPacketPresent', 'bufferedRanges', 'eioLoopError',
  'ended', 'frontBuffered', 'frontPacketPresent', 'lastDtsMs', 'loopStarted',
  'packetEnded', 'pullQueueLength', 'rangesError', 'sourceUpdating',
  'startTimestampMs', 'type'
];

/* ------------------------------------------------------------------ */
/* Strict loop capture                                                 */
/* ------------------------------------------------------------------ */

test('strict loop step records code/planReason/planDetail, redacts URLs and rethrows the same error',
  { timeout: 5000 }, () => {
    const api = fakeApi();
    for (const detail of [0, false, null, undefined]) {
      const stream = { eioOwned: {}, eioMuxTask: { eioOwnedResources: {} } };
      const error = new Error('loop refused by https://upstream.invalid/path?token=secret');
      error.code = 'EIO_MSE_PLAN_UNSUPPORTED';
      error.planReason = 'plan-overflow';
      if (undefined !== detail) error.planDetail = detail;
      let calls = 0;

      assert.throws(() => api.eioRunLoopStep(stream, () => {
        calls += 1;
        throw error;
      }), (raised) => raised === error,
      'the original error object must be rethrown, not copied or wrapped');
      assert.equal(calls, 1, 'the callback must run exactly once');

      assert.deepEqual(Object.keys(stream.eioLoopError).sort(),
        ['code', 'message', 'planDetail', 'planReason'],
        'the record must expose exactly the four diagnostic scalars');
      assert.equal(stream.eioLoopError.code, 'EIO_MSE_PLAN_UNSUPPORTED');
      assert.equal(stream.eioLoopError.planReason, 'plan-overflow');
      assert.equal(Object.is(stream.eioLoopError.planDetail,
        undefined === detail ? null : detail), true,
      'an explicit 0/false/null planDetail must survive; an absent one is null');
      assert.equal(typeof stream.eioLoopError.message, 'string');
      assert.equal(/https?:/.test(stream.eioLoopError.message), false,
        'the recorded message must not carry an upstream URL');
      assert.ok(stream.eioLoopError.message.includes('[redacted-url]'));
      assert.ok(stream.eioLoopError.message.length <= 240);
    }

    const stream = { eioOwned: {}, eioMuxTask: { eioOwnedResources: {} } };
    const plain = new Error('x'.repeat(400));
    assert.throws(() => api.eioRunLoopStep(stream, () => { throw plain; }),
      (raised) => raised === plain);
    assert.equal(stream.eioLoopError.code, null, 'an uncoded error has no invented code');
    assert.equal(stream.eioLoopError.planReason, null);
    assert.equal(stream.eioLoopError.planDetail, null);
    assert.equal(stream.eioLoopError.message.length, 240,
      'a long message must be bounded at 240 characters');

    const rejected = { eioOwned: {}, eioMuxTask: { eioOwnedResources: {} } };
    assert.throws(() => api.eioRunLoopStep(rejected, () => { throw 'plain-string'; }),
      (raised) => raised === 'plain-string',
      'a thrown non-Error must be rethrown unchanged');
    assert.equal(rejected.eioLoopError.message, 'plain-string');
    assert.equal(rejected.eioLoopError.code, null);
  });

test('baseline loop step runs directly, returns the value, and never allocates diagnostics',
  { timeout: 5000 }, () => {
    const api = fakeApi();
    const baseline = {};
    let calls = 0;
    assert.equal(api.eioRunLoopStep(baseline, () => {
      calls += 1;
      return 'ran';
    }), 'ran');
    assert.equal(calls, 1);
    assert.equal(Object.prototype.hasOwnProperty.call(baseline, 'eioLoopError'), false,
      'a baseline stream must not gain loop diagnostics');

    const failure = new Error('baseline failure');
    assert.throws(() => api.eioRunLoopStep(baseline, () => { throw failure; }),
      (raised) => raised === failure);
    assert.equal(Object.prototype.hasOwnProperty.call(baseline, 'eioLoopError'), false,
      'a baseline throw must stay completely unobserved');

    const strict = { eioOwned: {}, eioMuxTask: { eioOwnedResources: {} } };
    const coded = Object.assign(new Error('first'), { code: 'EIO_MSE_PLAN_UNDERRUN' });
    assert.throws(() => api.eioRunLoopStep(strict, () => { throw coded; }),
      (raised) => raised === coded);
    const recorded = strict.eioLoopError;
    assert.equal(api.eioRunLoopStep(strict, () => 7), 7,
      'a strict success must still pass the callback value through');
    assert.equal(strict.eioLoopError, recorded,
      'a later successful tick must not clear or replace the record');
  });

test('createLoop wraps only the existing synchronous callback body with the strict wrapper',
  { timeout: 5000 }, () => {
    const head = between(SOURCE, 'createLoop(t,e){t.eioMuxTask=e;t.loop=new z.A((()=>{',
      'const i=eioMseBufferedDuration(');
    assert.equal(head, 'createLoop(t,e){t.eioMuxTask=e;t.loop=new z.A((()=>{return this.eioRunLoopStep(t,()=>{',
      'the stream must capture its task before the callback enters the real producer gate');

    const tail = between(SOURCE, 'this.eioOperationSeal(t),this.swap(t,e)}',
      'async startMux(t,e){');
    assert.equal(tail,
      'this.eioOperationSeal(t),this.swap(t,e)}else this.eioOperationSeal(t),t.loop.emptyTask()})}),0,0)}',
      'the original body must stay byte-identical and only gain the closing arrow');

    assert.equal((SOURCE.match(/eioRunLoopStep/g) || []).length, 2,
      'one definition plus exactly one call site; no other try/catch site');
  });

/* ------------------------------------------------------------------ */
/* Progress RPC                                                        */
/* ------------------------------------------------------------------ */

test('progress RPC reads two tracks independently and exposes only observed scalars',
  { timeout: 5000 }, async () => {
    const audioPointer = 4096;
    const videoPointer = 8192;
    const memory = {};
    memory[audioPointer + 16] = 1500000n;
    memory[videoPointer + 16] = 2500000n;
    const api = fakeApi({ memory, clock: () => 4242.5 });

    const audioStream = fakeStream({
      type: 'audio',
      backPacket: audioPointer,
      frontPacket: 0,
      pullQueue: { queue: ['a', 'b', 'c'] },
      loop: { isStarted: () => true },
      lastDTS: 4321n,
      startTimestamp: 1700000000000n,
      eioLoopError: {
        code: 'EIO_MSE_PLAN_UNSUPPORTED',
        message: 'captured',
        planReason: 'plan-overflow',
        planDetail: false
      },
      track: { getSourceBuffer: () => ({ updating: false, buffered: fakeRanges([[0.043, 0.555]]) }) }
    });
    const videoStream = fakeStream({
      type: 'video',
      ended: true,
      packetEnded: true,
      frontBuffered: false,
      backPacket: -1048576,
      frontPacket: -1048576,
      pullQueue: { queue: [] },
      loop: { isStarted: () => false },
      lastDTS: -1n,
      track: { getSourceBuffer: () => ({ updating: true, buffered: fakeRanges([]) }) }
    });
    api.tasks = new Map([['task-1', {
      playRate: 100n,
      currentTime: 0.5,
      audio: audioStream,
      video: videoStream
    }]]);

    const progress = await api.getEioMSEProgress('task-1');
    assert.deepEqual(Object.keys(progress).sort(),
      ['cachedCurrentTimeSec', 'playRateScaled', 'sampledClockMs', 'streams', 'taskId']);
    assert.equal(progress.taskId, 'task-1');
    assert.equal(progress.sampledClockMs, 4242.5);
    assert.equal(progress.playRateScaled, '100', 'the scaled play rate is a decimal string');
    assert.equal(progress.cachedCurrentTimeSec, 0.5,
      'the task currentTime is reported, but only as the cached value');

    const audio = progress.streams.audio;
    assert.deepEqual(Object.keys(audio).sort(), STREAM_KEYS,
      'the stream record must not gain pointer/track/host/plan fields');
    assert.equal(audio.type, 'audio');
    assert.equal(audio.ended, false);
    assert.equal(audio.packetEnded, false);
    assert.equal(audio.frontBuffered, true);
    assert.equal(audio.backPacketPresent, true);
    assert.equal(audio.frontPacketPresent, false);
    assert.equal(audio.pullQueueLength, 3);
    assert.equal(audio.loopStarted, true);
    assert.equal(audio.lastDtsMs, '4321', 'lastDTS is the current field, in ms');
    assert.equal(audio.startTimestampMs, '1700000000000');
    assert.deepEqual(plain(audio.eioLoopError), {
      code: 'EIO_MSE_PLAN_UNSUPPORTED',
      message: 'captured',
      planReason: 'plan-overflow',
      planDetail: false
    });
    assert.deepEqual(plain(audio.bufferedRanges), [{ start: 0.043, end: 0.555 }]);
    assert.equal(audio.rangesError, null);
    assert.equal(audio.sourceUpdating, false);
    assert.equal(audio.backPacketDtsMs, '1500',
      'the pending packet DTS is measured through h.Mr(s.f[17](ptr+16), ptr+72, m.i0) in ms');
    assert.deepEqual(api.calls.mr, [[1500000n, audioPointer + 72, { num: 1, den: 1000 }]]);
    for (const forbidden of ['backPacket', 'frontPacket', 'track', 'loop', 'host',
      'codecpar', 'buffers', 'plan', 'writer']) {
      assert.equal(Object.prototype.hasOwnProperty.call(audio, forbidden), false,
        'the stream record must not expose ' + forbidden);
    }

    const video = progress.streams.video;
    assert.deepEqual(Object.keys(video).sort(), STREAM_KEYS);
    assert.equal(video.type, 'video');
    assert.equal(video.ended, true);
    assert.equal(video.packetEnded, true);
    assert.equal(video.frontBuffered, false);
    assert.equal(video.backPacketPresent, false,
      'a negative pending pointer is not a present packet');
    assert.equal(video.frontPacketPresent, false);
    assert.equal(video.backPacketDtsMs, null,
      'no DTS is measured for a non-positive pending pointer');
    assert.equal(video.pullQueueLength, 0, 'a real empty queue is an observed zero');
    assert.equal(video.loopStarted, false);
    assert.equal(video.lastDtsMs, '-1');
    assert.equal(video.startTimestampMs, '1700000000000');
    assert.deepEqual(plain(video.bufferedRanges), [],
      'an empty real TimeRanges is observed as an empty array');
    assert.equal(video.rangesError, null);
    assert.equal(video.sourceUpdating, true);

    const serialized = JSON.stringify(progress);
    for (const pointer of [audioPointer, videoPointer, audioPointer + 72, videoPointer + 72]) {
      assert.equal(serialized.includes(String(pointer)), false,
        'no packet address may leave the RPC');
    }
  });

test('missing task, absent stream, unknown values and a throwing getter degrade to null only',
  { timeout: 5000 }, async () => {
    const api = fakeApi();
    api.tasks = new Map([['task-2', { audio: null }]]);

    assert.equal(await api.getEioMSEProgress('missing-task'), null,
      'an unknown task id must return null, not an invented record');

    const sparse = await api.getEioMSEProgress('task-2');
    assert.equal(sparse.streams.audio, null, 'an absent audio stream stays null');
    assert.equal(sparse.streams.video, null, 'an absent video stream stays null');
    assert.equal(sparse.playRateScaled, null);
    assert.equal(sparse.cachedCurrentTimeSec, null,
      'a missing cached currentTime is null, never an invented zero');
    assert.equal(sparse.sampledClockMs, 1234.5);

    const blank = api.eioMseProgressStream({ track: { getSourceBuffer: () => null } });
    assert.deepEqual(Object.keys(blank).sort(), STREAM_KEYS);
    for (const key of ['type', 'ended', 'packetEnded', 'frontBuffered',
      'backPacketPresent', 'frontPacketPresent', 'pullQueueLength', 'loopStarted',
      'lastDtsMs', 'startTimestampMs', 'backPacketDtsMs', 'eioLoopError',
      'bufferedRanges', 'rangesError', 'sourceUpdating']) {
      assert.equal(blank[key], null,
        key + ' must degrade to null instead of false/0 when unobservable');
    }

    const zeroPointer = api.eioMseProgressStream({
      backPacket: 0,
      frontPacket: 0,
      pullQueue: { queue: null },
      track: {}
    });
    assert.equal(zeroPointer.backPacketPresent, false,
      'an observed zero pointer is not a pending packet');
    assert.equal(zeroPointer.frontPacketPresent, false);
    assert.equal(zeroPointer.pullQueueLength, null,
      'a non-array queue is unknown, not an invented zero length');
    assert.equal(zeroPointer.bufferedRanges, null,
      'a track without getSourceBuffer must not fabricate an empty range list');
    assert.equal(zeroPointer.rangesError, null);
    assert.equal(zeroPointer.sourceUpdating, null);

    const unreadable = api.eioMseProgressStream({
      backPacket: 64,
      track: { getSourceBuffer: () => null }
    });
    assert.equal(unreadable.backPacketPresent, true);
    assert.equal(unreadable.backPacketDtsMs, null,
      'an unreadable packet DTS is null, not a fabricated 0');

    const throwing = {
      type: 'video',
      ended: false,
      pullQueue: { queue: ['x', 'y'] },
      track: {
        getSourceBuffer: () => {
          throw new Error('SourceBuffer unavailable at https://native.invalid/sb');
        }
      }
    };
    const failed = api.eioMseProgressStream(throwing);
    assert.equal(failed.bufferedRanges, null);
    assert.equal(typeof failed.rangesError, 'string');
    assert.equal(/https?:/.test(failed.rangesError), false,
      'the rangesError must be sanitized like every other captured message');
    assert.ok(failed.rangesError.length <= 240);
    assert.equal(failed.ended, false, 'a ranges failure must not erase other observations');
    assert.equal(failed.pullQueueLength, 2);
    assert.equal(failed.sourceUpdating, null);

    const badLoop = api.eioMseProgressStream({
      loop: { isStarted: () => { throw new Error('isStarted failed'); } },
      track: { getSourceBuffer: () => null }
    });
    assert.equal(badLoop.loopStarted, null,
      'a throwing isStarted must not reject the whole RPC');

    api.tasks = new Map([['task-3', {
      playRate: 100n,
      currentTime: 0,
      audio: throwing,
      video: null
    }]]);
    const resolved = await api.getEioMSEProgress('task-3');
    assert.equal(resolved.streams.audio.rangesError.length > 0, true,
      'the RPC must resolve with a recorded rangesError instead of rejecting');
    assert.equal(resolved.cachedCurrentTimeSec, 0,
      'an explicitly observed zero cached time stays zero');
  });

test('a throwing wallclock clock stub degrades to null without rejecting the RPC',
  { timeout: 5000 }, async () => {
    const api = fakeApi({
      clock: () => { throw new Error('clock unavailable'); }
    });
    api.tasks = new Map([['task-4', { playRate: 125n, currentTime: 0.25 }]]);

    const progress = await api.getEioMSEProgress('task-4');
    assert.equal(progress.sampledClockMs, null);
    assert.equal(progress.playRateScaled, '125');
    assert.equal(progress.cachedCurrentTimeSec, 0.25);
    assert.equal(progress.streams.audio, null);
    assert.equal(progress.streams.video, null);
  });
