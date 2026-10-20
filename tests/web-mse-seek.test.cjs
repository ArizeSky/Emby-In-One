const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// Execute the vendored afterSeek itself, with synthetic packets and tracks.
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
  const bundle = fs.readFileSync(path.join(__dirname, '../public/vendor/web/libmedia/26.avplayer.js'), 'utf8');
  const begin = bundle.indexOf('async afterSeek('), end = bundle.indexOf('async setPlayRate(', begin);
  const method = bundle.slice(begin, end).replace(/^async afterSeek/, 'async function');
  instance.afterSeek = vm.runInNewContext('(' + method + ')', context);
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
