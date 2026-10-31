'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const path = require('node:path');

const buffer = require(path.join(__dirname, '..', 'public', 'web', 'player-buffer.js'));

function makeRanges(pairs) {
  const list = pairs.map((pair) => ({ start: pair[0], end: pair[1] }));
  return {
    length: list.length,
    start(index) { return list[index].start; },
    end(index) { return list[index].end; },
  };
}

function makeVideo(options = {}) {
  const video = {
    readyState: options.readyState === undefined ? 2 : options.readyState,
    buffered: options.buffered === undefined ? makeRanges([[0, 10]]) : options.buffered,
  };
  if (options.ended !== undefined) video.ended = options.ended;
  if (options.paused !== undefined) video.paused = options.paused;
  return video;
}

function makeHost(children, owned) {
  const ownedList = owned === undefined ? children : owned;
  return {
    contains(element) { return ownedList.indexOf(element) !== -1; },
    querySelectorAll(selector) {
      return selector === 'video,audio' ? children.slice() : [];
    },
  };
}

function nativePlayer(options = {}) {
  const video = options.video === undefined ? makeVideo() : options.video;
  const host = options.host === undefined ? makeHost([video]) : options.host;
  return {
    mode: 'native',
    video: video,
    host: host,
    duration: options.duration === undefined ? 600 : options.duration,
    _destroyed: Boolean(options.destroyed),
    _engineGeneration: options.generation === undefined ? 7 : options.generation,
  };
}

function msePlayer(options = {}) {
  const tracks = options.tracks === undefined ? [makeVideo()] : options.tracks;
  const host = options.host === undefined ? makeHost(tracks) : options.host;
  const core = options.core === undefined ? { isMSE() { return true; } } : options.core;
  return {
    mode: 'libmedia',
    core: core,
    host: host,
    duration: options.duration === undefined ? 600 : options.duration,
    _destroyed: Boolean(options.destroyed),
    _engineGeneration: options.generation === undefined ? 3 : options.generation,
  };
}

function assertUnknown(result, epoch) {
  assert.equal(result.status, 'unknown');
  assert.deepEqual(result.ranges, []);
  if (epoch !== undefined) assert.equal(result.epoch, epoch);
}

test('native reports multiple buffered islands exactly', () => {
  const video = makeVideo({ buffered: makeRanges([[0, 10], [20, 30]]) });
  const result = buffer.snapshot(nativePlayer({ video }));
  assert.equal(result.status, 'ok');
  assert.deepEqual(result.ranges, [{ start: 0, end: 10 }, { start: 20, end: 30 }]);
  assert.equal(result.epoch, 7);
});

test('native real empty buffered with metadata is exact ok empty', () => {
  for (const buffered of [makeRanges([]), { length: 0 }]) {
    const video = makeVideo({ buffered, readyState: 1 });
    const result = buffer.snapshot(nativePlayer({ video }));
    assert.equal(result.status, 'ok');
    assert.deepEqual(result.ranges, []);
  }
});

test('native without metadata or ownership is unknown', () => {
  const noMetadata = makeVideo({ readyState: 0 });
  assertUnknown(buffer.snapshot(nativePlayer({ video: noMetadata })));
  for (const readyState of ['2', undefined]) {
    const video = makeVideo();
    video.readyState = readyState;
    assertUnknown(buffer.snapshot(nativePlayer({ video })));
  }
  const video = makeVideo();
  assertUnknown(buffer.snapshot(nativePlayer({ video, host: makeHost([], []) })));
  assertUnknown(buffer.snapshot(nativePlayer({ video, host: { querySelectorAll() { return []; } } })));
  assertUnknown(buffer.snapshot(nativePlayer({ video, host: null })));
  assertUnknown(buffer.snapshot(nativePlayer({ video: null })));
  assertUnknown(buffer.snapshot(null));
});

test('readyState must be a finite integer between 1 and 4', () => {
  for (const readyState of [Infinity, -Infinity, NaN, 1.5, 4.5, 5, -1]) {
    const video = makeVideo();
    video.readyState = readyState;
    assertUnknown(buffer.snapshot(nativePlayer({ video })));
    const track = makeVideo();
    track.readyState = readyState;
    assertUnknown(buffer.snapshot(msePlayer({ tracks: [track] })));
  }
  for (const readyState of [1, 2, 3, 4]) {
    const result = buffer.snapshot(nativePlayer({ video: makeVideo({ readyState }) }));
    assert.equal(result.status, 'ok');
  }
});

test('destroyed player is unknown and keeps the captured epoch', () => {
  const result = buffer.snapshot(nativePlayer({ destroyed: true, generation: 9 }));
  assertUnknown(result, 9);
});

test('epoch mirrors engine generation and falls back to null', () => {
  const ok = buffer.snapshot(nativePlayer({ video: makeVideo(), generation: 11 }));
  assert.equal(ok.status, 'ok');
  assert.equal(ok.epoch, 11);
  const odd = buffer.snapshot(nativePlayer({ video: makeVideo(), generation: '11' }));
  assert.equal(odd.status, 'ok');
  assert.equal(odd.epoch, null);
});

test('throwing engine generation getter reports unknown without an epoch', () => {
  const player = nativePlayer({ video: makeVideo() });
  Object.defineProperty(player, '_engineGeneration', {
    get() { throw new Error('boom'); },
  });
  assertUnknown(buffer.snapshot(player), null);
});

test('native clamps ranges to a valid duration only', () => {
  const video = makeVideo({ buffered: makeRanges([[0, 90]]) });
  const clamped = buffer.snapshot(nativePlayer({ video, duration: 30 }));
  assert.deepEqual(clamped.ranges, [{ start: 0, end: 30 }]);
  for (const duration of [NaN, '30', -1, 0]) {
    const unbounded = buffer.snapshot(nativePlayer({ video, duration }));
    assert.deepEqual(unbounded.ranges, [{ start: 0, end: 90 }]);
  }
});

test('invalid or unsupported buffered data is unknown', () => {
  const bad = [
    makeRanges([[NaN, 10]]),
    makeRanges([[0, Infinity]]),
    makeRanges([[-1, 5]]),
    makeRanges([[0, -5]]),
    { length: 1, start() { return '0'; }, end() { return 10; } },
    { length: '1', start() { return 0; }, end() { return 10; } },
    { length: 1.5, start() { return 0; }, end() { return 10; } },
    { length: NaN, start() { return 0; }, end() { return 10; } },
    { length: 1 },
    makeRanges([[10, 5]]),
    null,
  ];
  for (const buffered of bad) {
    const video = makeVideo({ buffered });
    assertUnknown(buffer.snapshot(nativePlayer({ video })));
  }
});

test('time range segment budget is enforced', () => {
  const pairs = Array.from({ length: 128 }, (ignored, index) => [index, index + 0.5]);
  const ok = buffer.snapshot(nativePlayer({ video: makeVideo({ buffered: makeRanges(pairs) }) }));
  assert.equal(ok.status, 'ok');
  assert.equal(ok.ranges.length, 128);
  const over = buffer.snapshot(nativePlayer({
    video: makeVideo({ buffered: makeRanges(pairs.concat([[200, 201]])) }),
  }));
  assertUnknown(over);
});

test('throwing getters leave the player untouched and report unknown', () => {
  const video = makeVideo({ ended: false, paused: true });
  const player = nativePlayer({ video });
  Object.defineProperty(video, 'buffered', {
    get() { throw new Error('boom'); },
  });
  assertUnknown(buffer.snapshot(player));
  assert.equal(player._destroyed, false);
});

test('generation change during the buffered getter is unknown', () => {
  const video = makeVideo();
  const player = nativePlayer({ video, generation: 4 });
  Object.defineProperty(video, 'buffered', {
    get() {
      player._engineGeneration = 5;
      return makeRanges([[0, 10]]);
    },
  });
  assertUnknown(buffer.snapshot(player));
});

test('host replacement during the buffered getter is unknown', () => {
  const video = makeVideo();
  const player = nativePlayer({ video });
  const replacement = makeHost([video]);
  Object.defineProperty(video, 'buffered', {
    get() {
      player.host = replacement;
      return makeRanges([[0, 10]]);
    },
  });
  assertUnknown(buffer.snapshot(player));
});

test('ended and paused do not affect known buffered ranges', () => {
  const video = makeVideo({ buffered: makeRanges([[1, 2]]), ended: true, paused: true });
  const result = buffer.snapshot(nativePlayer({ video }));
  assert.equal(result.status, 'ok');
  assert.deepEqual(result.ranges, [{ start: 1, end: 2 }]);
});

test('mse reads a single active track', () => {
  const track = makeVideo({ buffered: makeRanges([[5, 15]]) });
  const result = buffer.snapshot(msePlayer({ tracks: [track] }));
  assert.equal(result.status, 'ok');
  assert.deepEqual(result.ranges, [{ start: 5, end: 15 }]);
});

test('mse intersects two active tracks', () => {
  const first = makeVideo({ buffered: makeRanges([[0, 10], [20, 30]]) });
  const second = makeVideo({ buffered: makeRanges([[5, 15], [18, 25]]) });
  const result = buffer.snapshot(msePlayer({ tracks: [first, second] }));
  assert.equal(result.status, 'ok');
  assert.deepEqual(result.ranges, [{ start: 5, end: 10 }, { start: 20, end: 25 }]);
});

test('mse without an active element or with too many is unknown', () => {
  assertUnknown(buffer.snapshot(msePlayer({ tracks: [] })));
  assertUnknown(buffer.snapshot(msePlayer({ tracks: [makeVideo(), makeVideo(), makeVideo()] })));
  const host = { contains() { return true; } };
  assertUnknown(buffer.snapshot(msePlayer({ host })));
});

test('mse requires a bounded integer active element count', () => {
  const track = makeVideo();
  for (const length of [NaN, 1.5, 0.5, Infinity, 0, 3, '1']) {
    const nodes = { length, 0: track };
    const host = {
      contains() { return true; },
      querySelectorAll() { return nodes; },
    };
    assertUnknown(buffer.snapshot(msePlayer({ tracks: [track], host })));
  }
});

test('mse requires metadata and ownership for every element', () => {
  for (const readyState of [0, '2', undefined]) {
    const track = makeVideo();
    track.readyState = readyState;
    assertUnknown(buffer.snapshot(msePlayer({ tracks: [track] })));
  }
  const stale = makeVideo();
  const active = makeVideo();
  const host = makeHost([stale, active], [active]);
  assertUnknown(buffer.snapshot(msePlayer({ host })));
});

test('mse requires the public isMSE flag to be exactly true', () => {
  for (const core of [
    { isMSE() { return false; } },
    { isMSE() { return 1; } },
    {},
    { isMSE() { throw new Error('boom'); } },
  ]) {
    assertUnknown(buffer.snapshot(msePlayer({ core })));
  }
});

test('core replacement during an mse buffered getter is unknown', () => {
  const track = makeVideo();
  const player = msePlayer({ tracks: [track] });
  Object.defineProperty(track, 'buffered', {
    get() {
      player.core = { isMSE() { return true; } };
      return makeRanges([[0, 10]]);
    },
  });
  assertUnknown(buffer.snapshot(player));
});
