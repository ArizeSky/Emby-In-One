const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// Run the real vendored fragment finalizer. A restarted HLS job can deliver a
// packet earlier than the pending sample; its duration must not wrap to uint32.
function finalize(type, next) {
  const scale = type === 'audio' ? 44100 : 12800;
  const stream = { index: 0, timeBase: { num: 1, den: scale },
    codecpar: { codecType: type === 'audio' ? 1 : 0, frameSize: 1024,
      sampleRate: 44100, framerate: { num: 25, den: 1 } },
    privData: { lastDts: BigInt(scale * 1200), trackId: 1 } };
  const writer = { getPos: () => 0n, flush() {}, writeUint32() {}, writeBuffer() {} };
  const track = { streamIndex: 0, sampleCount: 1, sampleSizes: [12],
    sampleDurations: [], sampleFlags: [], sampleCompositionTimeOffset: [],
    ioWriter: writer, buffers: [new Uint8Array(12)] };
  const state = { currentFragment: { firstWrote: true, tracks: [track], size: 0, sequence: 0 } };
  let duration;
  const context = { BigInt, Math, Number, Uint8Array,
    y: { __: (list, fn) => list.forEach(fn) },
    v: { k: (n, from, to) => {
      const numerator = n * BigInt(from.num * to.den), denominator = BigInt(from.den * to.num);
      return (numerator + denominator / 2n) / denominator;
    },
      lb: rate => rate.num / rate.den },
    U: { SF: 1000000, KR: { num: 1, den: 1000000 } },
    h: { A: list => list.every(x => x === list[0]) },
    d: { Ro: () => { duration = track.defaultSampleDuration; } },
    x: { A: () => new Uint8Array(12) }, p: { A() {} },
    c: { A: () => 0 }, l: { A() {} } };
  const bundle = fs.readFileSync(path.join(__dirname, '../public/vendor/web/libmedia/26.avplayer.js'), 'utf8');
  const begin = bundle.indexOf('updateCurrentFragment(t,e){');
  const end = bundle.indexOf('handleEAC3(', begin);
  const method = bundle.slice(begin, end).replace(/^updateCurrentFragment/, 'function');
  const finalizeFragment = vm.runInNewContext('(' + method + ')', context);
  finalizeFragment.call({ context: state, options: {} }, { streams: [stream], ioWriter: writer }, next);
  return duration;
}

test('backward or duplicate audio DTS finalizes the pending sample with one AAC frame', () => {
  assert.equal(finalize('audio', 52523100n), 1024); // observed 1200s restart boundary
  assert.equal(finalize('audio', 52920000n), 1024);
});

test('backward video DTS uses frame cadence and continuous DTS keeps its real duration', () => {
  assert.equal(finalize('video', 15206400n), 512);
  assert.equal(finalize('audio', 52921323n), 1323);
});
