'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const MODULE_PATH = path.join(__dirname, '..', 'public', 'web', 'player-progress.js');
const api = require(MODULE_PATH);
const {
  normalizeRanges,
  intersectRanges,
  continuousBufferedSeconds,
  getSourceBitrateMbps,
  formatReceiveRate,
  formatSourceBitrate,
  formatBufferedSeconds,
  progressLayers
} = api;

// Builds expected segments: segs([0, 3], [5, 8]) === [{ start: 0, end: 3 }, ...]
const segs = (...pairs) => pairs.map(([start, end]) => ({ start, end }));
const UNKNOWN = '\u2014';

/* ---------------------------------------------------------------- surface */

test('module exposes a frozen dual API without browser facilities', () => {
  assert.equal(Object.isFrozen(api), true);
  assert.deepEqual(Object.keys(api).sort(), [
    'continuousBufferedSeconds',
    'formatBufferedSeconds',
    'formatReceiveRate',
    'formatSourceBitrate',
    'getSourceBitrateMbps',
    'intersectRanges',
    'normalizeRanges',
    'progressLayers'
  ]);
  assert.equal(globalThis.EIOPlayerProgress, api);

  const source = fs.readFileSync(MODULE_PATH, 'utf8');
  const forbidden = [
    'setTimeout', 'setInterval', 'requestAnimationFrame', 'XMLHttpRequest',
    'localStorage', 'sessionStorage', 'fetch(', 'document', 'window', 'navigator'
  ];
  for (const token of forbidden) {
    assert.equal(source.includes(token), false, 'unexpected module token: ' + token);
  }
});

/* --------------------------------------------------------- normalizeRanges */

test('normalizeRanges returns null for unknown or malformed input', () => {
  const cases = [
    undefined, null, {}, '0-1',
    [{ start: '1', end: 2 }], [[1, '2']], [[NaN, 2]], [[0, Infinity]],
    [[-Infinity, 4]], [[-1, 4]], [[5, 4]], [[1, 2, 3]], [null],
    [[0, 1], [2, NaN]]
  ];
  for (const input of cases) {
    assert.equal(normalizeRanges(input), null, 'expected unknown for ' + JSON.stringify(input));
  }
});

test('normalizeRanges treats [] as known-empty and drops degenerate segments', () => {
  assert.deepEqual(normalizeRanges([]), []);
  assert.deepEqual(normalizeRanges([], 10), []);
  assert.deepEqual(normalizeRanges([[5, 5]]), []);
  assert.deepEqual(normalizeRanges([{ start: 1, end: 1 }, [2, 3]]), segs([2, 3]));
});

test('normalizeRanges accepts both entry shapes and never mutates the input', () => {
  assert.deepEqual(normalizeRanges([[2, 6]]), segs([2, 6]));
  assert.deepEqual(normalizeRanges([{ start: 2, end: 6 }]), segs([2, 6]));
  assert.deepEqual(normalizeRanges([[2, 6], { start: 8, end: 9 }]), segs([2, 6], [8, 9]));

  const input = [[8, 9], [0, 3], [1, 2]];
  const snapshot = JSON.parse(JSON.stringify(input));
  normalizeRanges(input);
  assert.deepEqual(input, snapshot);
});

test('normalizeRanges sorts, merges overlaps and touching segments, keeps gaps', () => {
  assert.deepEqual(normalizeRanges([[0, 2], [2, 3], [5, 8]]), segs([0, 3], [5, 8]));
  assert.deepEqual(normalizeRanges([[0, 2], [1.5, 4], [3, 5.5]]), segs([0, 5.5]));
  assert.deepEqual(normalizeRanges([[0, 1], [1, 2]]), segs([0, 2]));
  assert.deepEqual(normalizeRanges([[0, 1], [1.0001, 2]]), segs([0, 1], [1.0001, 2]));
  assert.deepEqual(normalizeRanges([[9, 10], [0, 1], [4, 4.5]]), segs([0, 1], [4, 4.5], [9, 10]));
});

test('normalizeRanges clamps to a usable duration and ignores unusable ones', () => {
  assert.deepEqual(normalizeRanges([[0, 5]], 10), segs([0, 5]));
  assert.deepEqual(normalizeRanges([[8, 20]], 10), segs([8, 10]));
  assert.deepEqual(normalizeRanges([[0, 5]], 3), segs([0, 3]));
  assert.deepEqual(normalizeRanges([[10, 20]], 10), []);
  assert.deepEqual(normalizeRanges([[0, 4], [3, 20]], 5), segs([0, 5]));
  assert.deepEqual(normalizeRanges([[0, 4], [8, 20]], 5), segs([0, 4]));
  assert.deepEqual(normalizeRanges([[8, 20]], 5), []);
  // Unusable durations must not clamp anything.
  for (const duration of [0, -5, NaN, Infinity, '10', undefined, null]) {
    assert.deepEqual(normalizeRanges([[0, 50]], duration), segs([0, 50]), String(duration));
  }
});

test('normalizeRanges caps the segment count at 128 without truncating', () => {
  const many = [];
  for (let i = 0; i < 129; i += 1) many.push([i * 2, i * 2 + 1]);
  assert.equal(normalizeRanges(many), null);
  assert.equal(normalizeRanges(many.slice(0, 128)).length, 128);
  // Too many entries is unknown even when merging would shrink the result.
  assert.equal(normalizeRanges(many.concat([[0, 400]])), null);
});

/* --------------------------------------------------------- intersectRanges */

test('intersectRanges reports unknown without tracks or with unknown tracks', () => {
  assert.equal(intersectRanges(undefined), null);
  assert.equal(intersectRanges(null), null);
  assert.equal(intersectRanges([]), null);
  assert.equal(intersectRanges([[[0, 5]], [[NaN, 2]]]), null);
  assert.equal(intersectRanges([[[0, 5]], null]), null);
  // Any unknown track wins over a known-empty one, in either order.
  assert.equal(intersectRanges([[], null]), null);
  assert.equal(intersectRanges([null, []]), null);
  assert.equal(intersectRanges([[], [[NaN, 2]]]), null);
  assert.equal(intersectRanges([[[NaN, 2]], []]), null);
  assert.equal(intersectRanges([[[0, 5]], [], null]), null);
  assert.equal(intersectRanges([null, [], [[0, 5]]]), null);
});

test('intersectRanges caps the number of active tracks at 128', () => {
  const groups = [];
  for (let i = 0; i < 129; i += 1) groups.push([[0, 5]]);
  assert.equal(intersectRanges(groups), null);
  assert.deepEqual(intersectRanges(groups.slice(0, 128)), segs([0, 5]));
});

test('intersectRanges keeps a single group and short-circuits known-empty groups', () => {
  assert.deepEqual(intersectRanges([[[0, 5], [7, 9]]]), segs([0, 5], [7, 9]));
  assert.equal(intersectRanges([[[]]]), null, 'a malformed segment is unknown, not an empty group');
  assert.deepEqual(intersectRanges([[]]), []);
  assert.deepEqual(intersectRanges([[[0, 5]], []]), []);
  assert.deepEqual(intersectRanges([[], [[0, 5]]]), []);
  assert.deepEqual(intersectRanges([[], [], []]), []);
  assert.deepEqual(intersectRanges([[[0, 5]], [], [[1, 2]]]), []);
});

test('intersectRanges intersects segments across one or many active tracks', () => {
  assert.deepEqual(intersectRanges([[[0, 10]], [[2, 4], [6, 20]]]), segs([2, 4], [6, 10]));
  assert.deepEqual(intersectRanges([[[0, 3], [4, 8]], [[2, 5]]]), segs([2, 3], [4, 5]));
  assert.deepEqual(intersectRanges([[[0, 5]], [[5, 10]]]), []);
  assert.deepEqual(intersectRanges([[[0, 10]], [[2, 8]], [[4, 6]]]), segs([4, 6]));
  assert.deepEqual(intersectRanges([[[8, 30]], [[0, 20]]], 10), segs([8, 10]));
});

/* ------------------------------------------------ continuousBufferedSeconds */

test('continuousBufferedSeconds is null for unknown ranges or invalid positions', () => {
  assert.equal(continuousBufferedSeconds(null, 3, 10), null);
  assert.equal(continuousBufferedSeconds(undefined, 3, 10), null);
  assert.equal(continuousBufferedSeconds([[0, 5]], NaN, 10), null);
  assert.equal(continuousBufferedSeconds([[0, 5]], '3', 10), null);
  assert.equal(continuousBufferedSeconds([[0, 5]], Infinity, 10), null);
  assert.equal(continuousBufferedSeconds([[0, 5]], -1, 10), null);
  assert.equal(continuousBufferedSeconds([[0, 5]], 11, 10), null);
  assert.equal(continuousBufferedSeconds([[0, '5']], 3, 10), null);
});

test('continuousBufferedSeconds measures to the current segment only', () => {
  assert.equal(continuousBufferedSeconds([], 3, 10), 0);
  assert.equal(continuousBufferedSeconds([[0, 6]], 0, 10), 6);
  assert.equal(continuousBufferedSeconds([[0, 6]], 2, 10), 4);
  // A following segment must not be added across the gap.
  assert.equal(continuousBufferedSeconds([[0, 6], [8, 9]], 2, 10), 4);
  // A segment end and a position inside a gap both report 0.
  assert.equal(continuousBufferedSeconds([[0, 6]], 6, 10), 0);
  assert.equal(continuousBufferedSeconds([[0, 6], [8, 9]], 7, 10), 0);
  assert.equal(continuousBufferedSeconds([[0, 20]], 8, 10), 2);
  assert.equal(continuousBufferedSeconds([[2, 3]], 2.5), 0.5);
});

/* ------------------------------------------------ bitrate and formatting */

test('getSourceBitrateMbps trusts MediaSource.Bitrate only', () => {
  assert.equal(getSourceBitrateMbps({ Bitrate: 8000000 }), 8);
  assert.equal(getSourceBitrateMbps({ Bitrate: 1500000 }), 1.5);
  assert.equal(getSourceBitrateMbps({ Bitrate: 0 }), null);
  assert.equal(getSourceBitrateMbps({ Bitrate: -5 }), null);
  assert.equal(getSourceBitrateMbps({ Bitrate: '8000000' }), null);
  assert.equal(getSourceBitrateMbps({ Bitrate: NaN }), null);
  assert.equal(getSourceBitrateMbps({ Bitrate: Infinity }), null);
  assert.equal(getSourceBitrateMbps({ bitrate: 8000000 }), null);
  assert.equal(getSourceBitrateMbps({}), null);
  assert.equal(getSourceBitrateMbps(null), null);
  assert.equal(getSourceBitrateMbps('8000000'), null);
});

test('formatReceiveRate uses decimal MB/s with one decimal for ok samples', () => {
  assert.equal(formatReceiveRate(0, 'ok'), '0.0 MB/s');
  assert.equal(formatReceiveRate(1500000, 'ok'), '1.5 MB/s');
  assert.equal(formatReceiveRate(999999, 'ok'), '1.0 MB/s');
  assert.equal(formatReceiveRate(123456789, 'ok'), '123.5 MB/s');
  for (const bad of [NaN, Infinity, '1500000', -1, undefined, null]) {
    assert.equal(formatReceiveRate(bad, 'ok'), UNKNOWN, String(bad));
  }
});

test('formatReceiveRate requires an explicit ok status before showing a number', () => {
  assert.equal(formatReceiveRate(1500000), UNKNOWN, 'missing status');
  assert.equal(formatReceiveRate(1500000, 'error'), UNKNOWN);
  assert.equal(formatReceiveRate(1500000, 'receiving'), UNKNOWN);
  assert.equal(formatReceiveRate(1500000, 'paused'), UNKNOWN);
  assert.equal(formatReceiveRate(1500000, 'unknown'), UNKNOWN);
  assert.equal(formatReceiveRate(1500000, false), UNKNOWN);
  assert.equal(formatReceiveRate(1500000, 'OK'), UNKNOWN);
  assert.equal(formatReceiveRate(1500000, true), UNKNOWN);
  // A real zero-byte sample with an ok status is still a valid rate.
  assert.equal(formatReceiveRate(0, 'ok'), '0.0 MB/s');
  assert.equal(formatReceiveRate(1000000, 'ok'), '1.0 MB/s');
});

test('formatSourceBitrate and formatBufferedSeconds are bounded and safe', () => {
  assert.equal(formatSourceBitrate(8), '8.0 Mbps');
  assert.equal(formatSourceBitrate(4.26), '4.3 Mbps');
  assert.equal(formatSourceBitrate(4.24), '4.2 Mbps');
  for (const bad of [0, -3, NaN, '8']) assert.equal(formatSourceBitrate(bad), UNKNOWN);

  assert.equal(formatBufferedSeconds(0), '0s');
  assert.equal(formatBufferedSeconds(0.9), '0s');
  assert.equal(formatBufferedSeconds(12.9), '12s');
  for (const bad of [NaN, -0.5, '12']) assert.equal(formatBufferedSeconds(bad), UNKNOWN);
});

/* --------------------------------------------------------- progressLayers */

function assertPercent(value, label) {
  assert.equal(typeof value, 'number', label);
  assert.ok(Number.isFinite(value), label + ' must be finite');
  assert.ok(value >= 0 && value <= 100, label + ' must stay in [0, 100]');
}

function assertSafeSegments(segments) {
  assert.ok(Array.isArray(segments));
  assert.ok(segments.length <= 128);
  for (const segment of segments) {
    assert.equal(typeof segment, 'object');
    assert.deepEqual(Object.keys(segment).sort(), ['startPercent', 'widthPercent']);
    assertPercent(segment.startPercent, 'startPercent');
    assertPercent(segment.widthPercent, 'widthPercent');
  }
}

function assertSafeLayer(layer) {
  assert.equal(typeof layer, 'object');
  for (const key of Object.keys(layer)) {
    const value = layer[key];
    if (key === 'backgroundColor') {
      assert.match(value, /^var\(--eio-progress-[a-z]+, #[0-9a-f]{6}\)$/);
    } else if (key === 'widthPercent') {
      assertPercent(value, 'widthPercent');
    } else if (key === 'segments') {
      assertSafeSegments(value);
    } else {
      assert.fail('unexpected layer key: ' + key);
    }
  }
}

test('progressLayers returns null for invalid duration or position', () => {
  assert.equal(progressLayers(), null);
  assert.equal(progressLayers(null), null);
  assert.equal(progressLayers({}), null);
  for (const duration of [0, -1, NaN, Infinity, '10']) {
    assert.equal(progressLayers({ currentTime: 5, duration }), null, String(duration));
  }
  for (const currentTime of [NaN, Infinity, '5', -1]) {
    assert.equal(progressLayers({ currentTime, duration: 10 }), null, String(currentTime));
  }
});

test('progressLayers stacks track, buffered and played with fixed CSS variables', () => {
  const layers = progressLayers({ currentTime: 4, duration: 10, ranges: [[0, 8]] });
  assert.deepEqual(Object.keys(layers), ['track', 'buffered', 'played']);
  assert.equal(layers.track.backgroundColor, 'var(--eio-progress-track, #111827)');
  assert.equal(layers.buffered.backgroundColor, 'var(--eio-progress-buffered, #f3f4f6)');
  assert.equal(layers.played.backgroundColor, 'var(--eio-progress-played, #3b82f6)');
  assert.equal(layers.played.widthPercent, 40);
  assert.deepEqual(layers.buffered.segments, [
    { startPercent: 0, widthPercent: 80 }
  ]);
  assertSafeLayer(layers.track);
  assertSafeLayer(layers.buffered);
  assertSafeLayer(layers.played);
});

test('progressLayers keeps played but drops buffer when ranges are unknown', () => {
  for (const ranges of [undefined, null, [[0, 'x']], '0-8', [[NaN, 2]]]) {
    const layers = progressLayers({ currentTime: 4, duration: 10, ranges });
    assert.equal(layers.buffered, null, 'buffer must stay unknown');
    assert.equal(layers.played.widthPercent, 40);
  }
  const oversized = [];
  for (let i = 0; i < 500; i += 1) oversized.push([i, i + 0.5]);
  const bounded = progressLayers({ currentTime: 2, duration: 1000, ranges: oversized });
  assert.equal(bounded.buffered, null);
  assert.equal(bounded.played.widthPercent, 0.2);
  assert.ok(JSON.stringify(bounded).length < 400, 'unknown output must stay bounded');
});

test('progressLayers follows the real position and never a preview', () => {
  const layers = progressLayers({
    currentTime: 2.5,
    previewTime: 9.5,
    preview: 9.5,
    duration: 10,
    ranges: [[0, 6]]
  });
  assert.equal(layers.played.widthPercent, 25);
  assert.deepEqual(layers.buffered.segments, [
    { startPercent: 0, widthPercent: 60 }
  ]);
  assert.equal(JSON.stringify(layers).includes('9.5'), false);
});

test('progressLayers handles gaps, empty buffers, overrun and rounding', () => {
  const gap = progressLayers({ currentTime: 7, duration: 10, ranges: [[0, 6], [8, 9]] });
  assert.equal(gap.played.widthPercent, 70);
  assert.deepEqual(gap.buffered.segments, [
    { startPercent: 0, widthPercent: 60 },
    { startPercent: 80, widthPercent: 10 }
  ]);

  const empty = progressLayers({ currentTime: 3, duration: 10, ranges: [] });
  assert.equal(empty.played.widthPercent, 30);
  assert.deepEqual(empty.buffered.segments, []);

  const overrun = progressLayers({ currentTime: 20, duration: 10, ranges: [[0, 10]] });
  assert.equal(overrun.played.widthPercent, 100);
  assert.deepEqual(overrun.buffered.segments, [
    { startPercent: 0, widthPercent: 100 }
  ]);

  const rounded = progressLayers({ currentTime: 1, duration: 3, ranges: [[0, 3]] });
  assert.equal(rounded.played.widthPercent, 33.333);
  assert.deepEqual(rounded.buffered.segments, [
    { startPercent: 0, widthPercent: 100 }
  ]);
});

test('progressLayers keeps every buffered island and never fills a gap', () => {
  const layers = progressLayers({ currentTime: 1, duration: 10, ranges: [[0, 2], [7, 9]] });
  assert.equal(layers.played.widthPercent, 10);
  assert.deepEqual(layers.buffered.segments, [
    { startPercent: 0, widthPercent: 20 },
    { startPercent: 70, widthPercent: 20 }
  ]);
  const covers = (percent) => layers.buffered.segments.some(
    (segment) => segment.startPercent <= percent &&
      percent < segment.startPercent + segment.widthPercent
  );
  assert.equal(covers(10), true, 'first island stays buffered');
  assert.equal(covers(40), false, 'the gap must not be painted white');
  assert.equal(covers(80), true, 'the later island stays buffered');
  assertSafeLayer(layers.buffered);
});

test('progressLayers cannot be fed arbitrary colours or URLs', () => {
  const injected = progressLayers({
    currentTime: 1,
    duration: 4,
    ranges: [['url(javascript:alert(1))', 3]],
    playedColor: 'red',
    bufferedColor: 'javascript:alert(2)'
  });
  assert.equal(injected.buffered, null);
  assertSafeLayer(injected.track);
  assertSafeLayer(injected.played);
  const serialized = JSON.stringify(injected);
  assert.equal(serialized.includes('javascript'), false);
  assert.equal(serialized.includes('url('), false);
  assert.equal(/\bred\b/.test(serialized), false);
});
