const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const BUNDLE = path.join(__dirname, '../public/vendor/web/libmedia/26.avplayer.js');
const BUNDLE_SRC = fs.readFileSync(BUNDLE, 'utf8');

// Cut an actual class method out of the artifact and turn only its declaration
// into a function. The body stays byte-for-byte the vendored implementation;
// the harness supplies the instance it runs on.
function extractActualMethod(bundle, startMarker, endMarker, name, isAsync) {
  const start = bundle.indexOf(startMarker);
  assert.ok(start >= 0, `${name} method not found in bundle`);
  const end = bundle.indexOf(endMarker, start);
  assert.ok(end > start, `method boundary after ${name} not found`);
  const src = bundle.slice(start, end).trimEnd();
  const prefix = `${isAsync ? 'async ' : ''}${name}`;
  assert.ok(src.startsWith(`${prefix}(`), `${name} extraction must start at the actual method`);
  assert.ok(src.endsWith('}'), `${name} extraction must end at a method boundary`);
  return `${isAsync ? 'async ' : ''}function ${name}${src.slice(prefix.length)}`;
}

// Execute the vendored afterSeek itself, with synthetic packets and tracks.
// Its actual helpers (eioAfterSeekWrite/eioAfterSeekFlush/eioAddQueue) are
// extracted alongside it, so the non-owned branches run the real code.
// AAC packets are shorter than video frames: one shared loop stopped too early.
function pipeline(audio = true, video = true) {
  const memory = new Map();
  let pointer = 1000;
  const track = step => {
    const stream = { next: 0, step, startPTS: 0n, codecpar: {}, packetEnded: false,
      oformatContext: { streams: [{ index: 0, timeBase: {} }] },
      loop: { start() {} }, muxEnd: 0,
      track: { end: 0, addBuffer(end, done) { this.end = Math.max(this.end, end); done?.(); },
        getBufferedStart() { return 0; }, getBufferedEnd() { return this.end; }, insertEnqueueCallback(done) { done(); } },
    };
    stream.bufferQueue = { flush: () => stream.muxEnd };
    return stream;
  };
  // No `eioOwned` field on the streams: the extracted helpers must take their
  // non-owned branches, matching the original fake write/flush/queue order.
  const state = { audio: audio ? track(21) : null, video: video ? track(66) : null,
    cacheDuration: 500n, pausing: true, playRate: 100n, avpacketPool: { release() {} } };
  const instance = { tasks: new Map([['task', state]]),
    async pullAVPacket(stream) {
      if (stream.next >= 8000) return -1;
      pointer += 100;
      memory.set(pointer + 8, BigInt(stream.next));
      stream.next += stream.step;
      return pointer;
    },
    async syncToKeyframe(task) {
      for (const stream of [task.audio, task.video].filter(Boolean)) stream.backPacket = await this.pullAVPacket(stream);
      return true;
    },
    writeAVPacket(packet, stream) { stream.muxEnd = (Number(memory.get(packet + 8)) + stream.step) / 1000; },
  };
  const context = { BigInt, Math, Promise,
    s: { f: { 17: pointer => memory.get(pointer) } }, c: { M: { 15() {} } },
    h: { Mr: x => x, k: () => 0n }, m: { i0: {} },
    x: { T9: (a, b) => a > b ? a : b }, Q: { bX() {} }, C: { A: () => 0 },
    P: { yw: ms => Number(ms) / 1000 }, T: { Yz() {} }, L: 'test',
  };
  const afterSeekSrc = extractActualMethod(
    BUNDLE_SRC,
    'async afterSeek(',
    'async setPlayRate(',
    'afterSeek',
    true,
  );
  const afterSeekWriteSrc = extractActualMethod(
    BUNDLE_SRC,
    'async eioAfterSeekWrite(',
    'async eioAfterSeekFlush(',
    'eioAfterSeekWrite',
    true,
  );
  const afterSeekFlushSrc = extractActualMethod(
    BUNDLE_SRC,
    'async eioAfterSeekFlush(',
    'async unregisterTask(',
    'eioAfterSeekFlush',
    true,
  );
  const addQueueSrc = extractActualMethod(
    BUNDLE_SRC,
    'eioAddQueue(t,e,i){',
    'eioDiscardQueue(',
    'eioAddQueue',
    false,
  );
  const strictPredicateSrc = extractActualMethod(
    BUNDLE_SRC, 'eioPullHasStrict(', 'seekSeconds(', 'eioPullHasStrict', false,
  );
  instance.eioPullHasStrict = vm.runInNewContext('(' + strictPredicateSrc + ')', context);
  instance.eioAfterSeekWrite = vm.runInNewContext('(' + afterSeekWriteSrc + ')', context);
  instance.eioAfterSeekFlush = vm.runInNewContext('(' + afterSeekFlushSrc + ')', context);
  instance.eioAddQueue = vm.runInNewContext('(' + addQueueSrc + ')', context);
  instance.afterSeek = vm.runInNewContext('(' + afterSeekSrc + ')', context);
  return { instance, state };
}

test('paused MSE seeking buffers audio and video through the requested time independently', async () => {
  const { instance, state } = pipeline();
  assert.equal(await instance.afterSeek('task', 4000n), 4);
  assert.ok(state.audio.track.end >= 4.5);
  assert.ok(state.video.track.end >= 4.5);
  assert.ok(state.audio.frontPacket > 0 && state.video.frontPacket > 0);
});

test('MSE seeking handles EOF and single-stream media without getting stuck', async () => {
  for (const [audio, video] of [[true, true], [true, false], [false, true]]) {
    const { instance } = pipeline(audio, video);
    assert.equal(await instance.afterSeek('task', 7900n), 7.9);
  }
});
