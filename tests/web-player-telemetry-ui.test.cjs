'use strict';
/* Work package P4: player telemetry HUD + layered progress (static, fake).
 *
 * These tests cover what can be verified without a browser and without real
 * media: the frozen view-model contract of player-telemetry-ui.js (real
 * position vs seek preview, generation/epoch ownership, unknown vs 0, reset),
 * the geometry handed to the template, the HUD/seek markup contract in
 * web.html (single visibility source, unchanged native range semantics), the
 * player-scoped palette defaults in player-progress.css, and the thin app.js
 * hooks through the shared VM harness with a fake engine.
 *
 * Not covered here (reported separately, never claimed by this file): real
 * browser hover/touch matrices, real media buffering, real receive-rate
 * measurement and any production build/deploy step.
 */

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const WEB_DIR = path.join(__dirname, '..', 'public', 'web');
const UI_PATH = path.join(WEB_DIR, 'player-telemetry-ui.js');
const CSS_PATH = path.join(WEB_DIR, 'player-progress.css');
const HTML_PATH = path.join(WEB_DIR, 'web.html');
const UI_CSS_PATH = path.join(WEB_DIR, 'ui.css');

const UI_SRC = fs.readFileSync(UI_PATH, 'utf8');
const CSS_SRC = fs.readFileSync(CSS_PATH, 'utf8');
const HTML_SRC = fs.readFileSync(HTML_PATH, 'utf8');
const UI_CSS_SRC = fs.readFileSync(UI_CSS_PATH, 'utf8');

const UNKNOWN = '\u2014';

function freshModule() {
  delete require.cache[require.resolve(UI_PATH)];
  return require(UI_PATH);
}

function plain(value) {
  return JSON.parse(JSON.stringify(value));
}

/* --------------------------------------------------------------- UMD shape */

test('module is UMD: window global first, CommonJS for tests, single factory', () => {
  const api = freshModule();
  assert.equal(typeof api.create, 'function');
  assert.ok(Object.isFrozen(api), 'the export surface is frozen');
  assert.equal(globalThis.EIOPlayerTelemetry, api, 'the browser global is set');
  // The browser branch reads window.EIOPlayerProgress, never a second copy.
  assert.match(UI_SRC, /root\.EIOPlayerProgress/);
  assert.match(UI_SRC, /require\('\.\/player-progress\.js'\)/);
});

test('a page loaded without the data module degrades to dashes instead of throwing', () => {
  const vm = require('node:vm');
  const context = { console };
  vm.createContext(context);
  vm.runInContext(UI_SRC, context);
  assert.equal(typeof context.EIOPlayerTelemetry.create, 'function');
  const telemetry = context.EIOPlayerTelemetry.create();
  telemetry.update({
    time: 10, duration: 100,
    buffered: { status: 'ok', ranges: [{ start: 0, end: 50 }], epoch: 1 },
    receiveRate: { status: 'ok', bytesPerSec: 1e6, epoch: 1, sampledAt: 1 },
    sourceBitrateMbps: 2,
  });
  assert.equal(telemetry.receiveText, UNKNOWN);
  assert.equal(telemetry.sourceText, UNKNOWN);
  assert.equal(telemetry.bufferText, UNKNOWN);
  assert.equal(telemetry.layers, null);
});

test('browser UMD exports through window even when it differs from globalThis', () => {
  const vm = require('node:vm');
  const progress = require(path.join(WEB_DIR, 'player-progress.js'));
  const windowObject = { EIOPlayerProgress: progress };
  const context = vm.createContext({ console, window: windowObject });
  const globalObject = vm.runInContext('globalThis', context);
  assert.notEqual(windowObject, globalObject, 'the sandbox window is not the VM global');

  vm.runInContext(UI_SRC, context);
  assert.equal(typeof windowObject.EIOPlayerTelemetry.create, 'function');
  assert.equal(globalObject.EIOPlayerTelemetry, undefined, 'the export stays on window');

  const telemetry = windowObject.EIOPlayerTelemetry.create();
  telemetry.update({
    time: 30, duration: 120,
    buffered: { status: 'ok', ranges: [{ start: 0, end: 60 }], epoch: 1 },
    receiveRate: { status: 'ok', bytesPerSec: 2e6, epoch: 1, sampledAt: 1 },
    sourceBitrateMbps: 3,
  });
  // The real (not stubbed) progress module is resolved from the window root.
  assert.equal(telemetry.bufferText, '30s');
  assert.equal(telemetry.receiveText, '2.0 MB/s');
  assert.equal(telemetry.sourceText, '3.0 Mbps');
  assert.equal(telemetry.layers.played.widthPercent, 25);
});

test('a window-rooted page without the data module only degrades to dashes', () => {
  const vm = require('node:vm');
  const windowObject = {};
  const context = vm.createContext({ console, window: windowObject });
  vm.runInContext(UI_SRC, context);
  const telemetry = windowObject.EIOPlayerTelemetry.create();
  assert.doesNotThrow(() => telemetry.update({
    time: 10, duration: 100,
    buffered: { status: 'ok', ranges: [{ start: 0, end: 50 }], epoch: 1 },
  }));
  assert.equal(telemetry.receiveText, UNKNOWN);
  assert.equal(telemetry.sourceText, UNKNOWN);
  assert.equal(telemetry.bufferText, UNKNOWN);
  assert.equal(telemetry.layers, null);
});

test('create() wraps its state and its API with the injected reactive factory', () => {
  const seen = [];
  const api = freshModule().create({ reactive: object => { seen.push(object); return object; } });
  assert.equal(seen.length, 2, 'state + view model are both made reactive');
  assert.equal(typeof seen[1].update, 'function');
  // Without a reactive factory the module stays plain (fake tests / SSR).
  const plainApi = freshModule().create();
  assert.equal(typeof plainApi.update, 'function');
});

/* -------------------------------------------------------- initial and reset */

test('a fresh HUD is entirely unknown and draws no layers', () => {
  const telemetry = freshModule().create();
  assert.equal(telemetry.receiveText, UNKNOWN);
  assert.equal(telemetry.sourceText, UNKNOWN);
  assert.equal(telemetry.bufferText, UNKNOWN);
  assert.equal(telemetry.layers, null);
});

test('reset() returns every metric to unknown and drops the old generation', () => {
  const telemetry = freshModule().create();
  telemetry.update(snapshot({ epoch: 9, time: 30, duration: 100, ranges: [{ start: 0, end: 80 }], bytesPerSec: 3e6, mbps: 4 }));
  assert.equal(telemetry.receiveText, '3.0 MB/s');
  telemetry.reset();
  assert.equal(telemetry.receiveText, UNKNOWN);
  assert.equal(telemetry.sourceText, UNKNOWN);
  assert.equal(telemetry.bufferText, UNKNOWN);
  assert.equal(telemetry.layers, null);
  // After a reset the next generation starts from scratch instead of being
  // rejected as "stale" against the old epoch.
  telemetry.update(snapshot({ epoch: 1, time: 5, duration: 10, ranges: [{ start: 0, end: 10 }] }));
  assert.equal(telemetry.bufferText, '5s');
});

test('a non-object snapshot is treated as a reset, never as "keep the old data"', () => {
  const telemetry = freshModule().create();
  telemetry.update(snapshot({ epoch: 3, time: 10, duration: 20, ranges: [{ start: 0, end: 20 }], bytesPerSec: 1e6 }));
  assert.notEqual(telemetry.receiveText, UNKNOWN);
  telemetry.update(null);
  assert.equal(telemetry.receiveText, UNKNOWN);
  assert.equal(telemetry.bufferText, UNKNOWN);
});

/* ------------------------------------------------- actual position vs preview */

function snapshot(options = {}) {
  const state = { time: options.time, duration: options.duration };
  if (options.epoch !== undefined || options.ranges !== undefined || options.status !== undefined) {
    state.buffered = {
      status: options.status || 'ok',
      ranges: options.ranges || [],
      epoch: options.epoch === undefined ? 1 : options.epoch,
    };
  }
  if (options.bytesPerSec !== undefined || options.rateStatus !== undefined || options.rateEpoch !== undefined) {
    state.receiveRate = {
      status: options.rateStatus || 'ok',
      bytesPerSec: options.bytesPerSec,
      epoch: options.rateEpoch === undefined ? (options.epoch === undefined ? 1 : options.epoch) : options.rateEpoch,
      sampledAt: 1000,
    };
  }
  if (options.mbps !== undefined) state.sourceBitrateMbps = options.mbps;
  return state;
}

test('the blue played layer follows the real engine position, never a preview', () => {
  const telemetry = freshModule().create();
  // Extra preview-ish keys are ignored: only `time` may move the played layer,
  // otherwise a seek drag would paint progress that the media does not have.
  telemetry.update({
    time: 30, duration: 120, seekPreview: 90, previewTime: 90, previewPercent: 75,
    buffered: { status: 'ok', ranges: [{ start: 0, end: 60 }], epoch: 2 },
  });
  const layers = telemetry.layers;
  assert.equal(layers.played.widthPercent, 25);
  assert.equal(telemetry.bufferText, '30s');
  // An invalid position clears the old geometry instead of freezing it.
  telemetry.update({ time: Number.NaN, duration: 120 });
  assert.equal(telemetry.layers, null);
  assert.equal(telemetry.bufferText, UNKNOWN);
});

test('a zero or unknown duration disables the layers (disabled seek bar)', () => {
  const telemetry = freshModule().create();
  telemetry.update({ time: 0, duration: 0 });
  assert.equal(telemetry.layers, null);
  telemetry.update({ time: 0, duration: null });
  assert.equal(telemetry.layers, null);
  telemetry.update({ time: 0, duration: 100, buffered: { status: 'ok', ranges: [], epoch: 1 } });
  assert.equal(telemetry.layers.played.widthPercent, 0);
  assert.deepEqual(telemetry.layers.buffered.segments, []);
});

/* ------------------------------------------------------------------ buffered */

test('buffered islands stay separate and are never joined into a prefix', () => {
  const telemetry = freshModule().create();
  telemetry.update(snapshot({
    epoch: 4, time: 25, duration: 100,
    ranges: [{ start: 0, end: 10 }, { start: 20, end: 30 }],
  }));
  const segments = telemetry.layers.buffered.segments;
  assert.equal(segments.length, 2);
  assert.deepEqual(plain(segments[0]), { startPercent: 0, widthPercent: 10 });
  assert.deepEqual(plain(segments[1]), { startPercent: 20, widthPercent: 10 });
  // Continuous seconds stop at the end of the island that holds the position.
  assert.equal(telemetry.bufferText, '5s');
  // Inside the gap nothing ahead is buffered.
  telemetry.update(snapshot({
    epoch: 4, time: 15, duration: 100,
    ranges: [{ start: 0, end: 10 }, { start: 20, end: 30 }],
  }));
  assert.equal(telemetry.bufferText, '0s');
  assert.equal(telemetry.layers.played.widthPercent, 15);
});

test('buffered ranges are clamped to the duration and capped safely', () => {
  const telemetry = freshModule().create();
  telemetry.update(snapshot({
    epoch: 1, time: 10, duration: 100,
    ranges: [{ start: 0, end: 10 }, { start: 50, end: 900 }],
  }));
  for (const segment of telemetry.layers.buffered.segments) {
    assert.ok(segment.startPercent >= 0 && segment.startPercent <= 100);
    assert.ok(segment.widthPercent >= 0 && segment.widthPercent <= 100);
    assert.ok(segment.startPercent + segment.widthPercent <= 100.001);
  }
  // More segments than the frozen cap: unknown, never a truncated prefix.
  const many = [];
  for (let i = 0; i < 200; i += 1) many.push({ start: i * 0.4, end: i * 0.4 + 0.2 });
  telemetry.update(snapshot({ epoch: 1, time: 10, duration: 100, ranges: many, status: 'ok' }));
  assert.equal(telemetry.layers.buffered, null);
  assert.equal(telemetry.bufferText, UNKNOWN);
});

/* ------------------------------------------------------- unknown versus zero */

test('unknown metrics read as a dash, a valid zero reads as 0', () => {
  const telemetry = freshModule().create();
  // Declared unknown, even with a number attached, is never shown as a value.
  telemetry.update(snapshot({
    epoch: 2, time: 0, duration: 100, status: 'unknown', ranges: [], rateStatus: 'unknown', bytesPerSec: 0,
  }));
  assert.equal(telemetry.bufferText, UNKNOWN);
  assert.equal(telemetry.receiveText, UNKNOWN);
  assert.equal(telemetry.layers.buffered, null, 'no fake white bar for an unknown buffer');
  assert.equal(telemetry.layers.played.widthPercent, 0, 'played still renders');

  // A known empty buffer and a known zero rate are real zeros.
  telemetry.update(snapshot({ epoch: 2, time: 0, duration: 100, status: 'ok', ranges: [], bytesPerSec: 0, mbps: 2 }));
  assert.equal(telemetry.bufferText, '0s');
  assert.equal(telemetry.receiveText, '0.0 MB/s');
  assert.equal(telemetry.sourceText, '2.0 Mbps');

  // Absent / non-positive source bitrate is unknown, not 0 Mbps.
  for (const mbps of [null, 0, -1, Number.NaN, '3']) {
    telemetry.update(snapshot({ epoch: 2, time: 0, duration: 100, mbps }));
    assert.equal(telemetry.sourceText, UNKNOWN, 'source bitrate ' + String(mbps));
  }
});

test('malformed telemetry clears the previous value instead of keeping it', () => {
  const telemetry = freshModule().create();
  const good = snapshot({ epoch: 3, time: 10, duration: 100, ranges: [{ start: 0, end: 90 }], bytesPerSec: 2e6, mbps: 5 });
  telemetry.update(good);
  assert.equal(telemetry.receiveText, '2.0 MB/s');

  // Invalid ranges (NaN / reversed / non-array) => unknown buffer.
  telemetry.update(snapshot({ epoch: 3, time: 10, duration: 100, ranges: [{ start: Number.NaN, end: 5 }] }));
  assert.equal(telemetry.layers.buffered, null);
  telemetry.update(good);
  telemetry.update({ time: 10, duration: 100, buffered: { status: 'ok', ranges: 'nope', epoch: 3 } });
  assert.equal(telemetry.layers.buffered, null);

  // Invalid rate => unknown rate.
  telemetry.update(good);
  telemetry.update(snapshot({ epoch: 3, time: 10, duration: 100, bytesPerSec: -5 }));
  assert.equal(telemetry.receiveText, UNKNOWN);
  telemetry.update(good);
  telemetry.update(snapshot({ epoch: 3, time: 10, duration: 100, bytesPerSec: '5' }));
  assert.equal(telemetry.receiveText, UNKNOWN);

  // Structurally broken fields are unknown too.
  telemetry.update(good);
  telemetry.update({ time: 10, duration: 100, buffered: null, receiveRate: null, sourceBitrateMbps: null });
  assert.equal(telemetry.bufferText, UNKNOWN);
  assert.equal(telemetry.receiveText, UNKNOWN);
  assert.equal(telemetry.sourceText, UNKNOWN);
});

test('a snapshot without telemetry fields (e.g. a subtitle-only emission) keeps the last sample', () => {
  const telemetry = freshModule().create();
  telemetry.update(snapshot({ epoch: 6, time: 20, duration: 100, ranges: [{ start: 0, end: 50 }], bytesPerSec: 1e6, mbps: 3 }));
  telemetry.update({ time: 21, duration: 100, subtitleIndex: 2 });
  assert.equal(telemetry.receiveText, '1.0 MB/s');
  assert.equal(telemetry.sourceText, '3.0 Mbps');
  assert.equal(telemetry.bufferText, '29s');
});

/* -------------------------------------------------------------------- epochs */

test('an older epoch is stale and never rewrites a newer generation', () => {
  const telemetry = freshModule().create();
  telemetry.update(snapshot({ epoch: 5, time: 20, duration: 100, ranges: [{ start: 0, end: 60 }], bytesPerSec: 2e6 }));
  assert.equal(telemetry.bufferText, '40s');
  telemetry.update(snapshot({ epoch: 4, time: 20, duration: 100, ranges: [{ start: 0, end: 90 }], bytesPerSec: 9e6 }));
  assert.equal(telemetry.bufferText, '40s', 'stale ranges are dropped');
  assert.equal(telemetry.receiveText, '2.0 MB/s', 'stale rate is dropped');
});

test('a new epoch first discards the old ranges and rate', () => {
  const telemetry = freshModule().create();
  telemetry.update(snapshot({ epoch: 7, time: 20, duration: 100, ranges: [{ start: 0, end: 90 }], bytesPerSec: 4e6 }));
  assert.equal(telemetry.bufferText, '70s');

  // The new generation only reports "unknown" so far: nothing old survives.
  telemetry.update({
    time: 20, duration: 100,
    buffered: { status: 'unknown', ranges: [], epoch: 8 },
    receiveRate: { status: 'unknown', bytesPerSec: null, epoch: 8, sampledAt: 2000 },
  });
  assert.equal(telemetry.bufferText, UNKNOWN);
  assert.equal(telemetry.receiveText, UNKNOWN);
  assert.equal(telemetry.layers.buffered, null);

  // A rate sample from before the generation switch cannot reappear either.
  telemetry.update({ time: 20, duration: 100, receiveRate: { status: 'ok', bytesPerSec: 4e6, epoch: 7, sampledAt: 2000 } });
  assert.equal(telemetry.receiveText, UNKNOWN);

  // The new generation's own samples apply normally.
  telemetry.update(snapshot({ epoch: 8, time: 20, duration: 100, ranges: [{ start: 0, end: 30 }], bytesPerSec: 1.5e6 }));
  assert.equal(telemetry.bufferText, '10s');
  assert.equal(telemetry.receiveText, '1.5 MB/s');
});

test('an unusable epoch marker is unknown, never an accepted sample', () => {
  const telemetry = freshModule().create();
  telemetry.update(snapshot({ epoch: 2, time: 10, duration: 100, ranges: [{ start: 0, end: 40 }] }));
  telemetry.update({ time: 10, duration: 100, buffered: { status: 'ok', ranges: [{ start: 0, end: 90 }], epoch: '3' } });
  assert.equal(telemetry.bufferText, UNKNOWN, 'a non-numeric epoch clears the buffer');
  telemetry.update({ time: 10, duration: 100, receiveRate: { status: 'ok', bytesPerSec: 1e6, epoch: null } });
  assert.equal(telemetry.receiveText, UNKNOWN);
});

test('a stale epoch drops the whole frame, including time, duration and source', () => {
  const telemetry = freshModule().create();
  telemetry.update(snapshot({
    epoch: 5, time: 20, duration: 100,
    ranges: [{ start: 0, end: 60 }], bytesPerSec: 2e6, mbps: 3,
  }));
  const before = plain(telemetry.layers);
  telemetry.update(snapshot({
    epoch: 4, time: 90, duration: 200,
    ranges: [{ start: 0, end: 190 }], bytesPerSec: 9e6, mbps: 9,
  }));
  assert.deepEqual(plain(telemetry.layers), before, 'no HUD or played geometry moved');
  assert.equal(telemetry.bufferText, '40s');
  assert.equal(telemetry.receiveText, '2.0 MB/s');
  assert.equal(telemetry.sourceText, '3.0 Mbps');

  // A rate-only stale frame is gated by the same frame epoch too.
  telemetry.update({
    time: 95, duration: 200, sourceBitrateMbps: 9,
    receiveRate: { status: 'ok', bytesPerSec: 9e6, epoch: 4, sampledAt: 2 },
  });
  assert.deepEqual(plain(telemetry.layers), before);
  assert.equal(telemetry.bufferText, '40s');
  assert.equal(telemetry.sourceText, '3.0 Mbps');
});

test('a new epoch accepts its own time/source but never inherits a missing source', () => {
  const telemetry = freshModule().create();
  telemetry.update(snapshot({
    epoch: 1, time: 10, duration: 100,
    ranges: [{ start: 0, end: 20 }], bytesPerSec: 1e6, mbps: 4,
  }));
  telemetry.update({
    time: 12, duration: 100,
    buffered: { status: 'ok', ranges: [{ start: 0, end: 30 }], epoch: 2 },
    receiveRate: { status: 'ok', bytesPerSec: 2e6, epoch: 2, sampledAt: 3 },
  });
  assert.equal(telemetry.sourceText, UNKNOWN, 'missing source is cleared by the new epoch');
  assert.equal(telemetry.layers.played.widthPercent, 12);
  assert.equal(telemetry.bufferText, '18s');
  assert.equal(telemetry.receiveText, '2.0 MB/s');

  // The same new generation may still supply a valid time/source sample.
  telemetry.update({
    time: 13, duration: 100, sourceBitrateMbps: 5,
    buffered: { status: 'ok', ranges: [{ start: 0, end: 40 }], epoch: 2 },
    receiveRate: { status: 'ok', bytesPerSec: 3e6, epoch: 2, sampledAt: 4 },
  });
  assert.equal(telemetry.sourceText, '5.0 Mbps');
  assert.equal(telemetry.layers.played.widthPercent, 13);
  assert.equal(telemetry.bufferText, '27s');
});

test('a mixed-epoch frame keeps its new buffer and refuses the old rate', () => {
  const telemetry = freshModule().create();
  telemetry.update(snapshot({
    epoch: 1, time: 10, duration: 100,
    ranges: [{ start: 0, end: 20 }], bytesPerSec: 1e6, mbps: 4,
  }));
  telemetry.update({
    time: 30, duration: 100,
    buffered: { status: 'ok', ranges: [{ start: 0, end: 50 }], epoch: 2 },
    receiveRate: { status: 'ok', bytesPerSec: 9e6, epoch: 1, sampledAt: 2 },
  });
  assert.equal(telemetry.bufferText, '20s', 'the new-epoch buffer still applies');
  assert.equal(telemetry.receiveText, UNKNOWN, 'the old-epoch rate is dropped');
  assert.equal(telemetry.sourceText, UNKNOWN, 'the new epoch did not inherit source');

  // A pure-time emission keeps the valid new buffer while an old rate sample
  // cannot be revived later.
  telemetry.update({ time: 31, duration: 100, subtitleIndex: 1 });
  assert.equal(telemetry.bufferText, '19s');
  telemetry.update({
    time: 32, duration: 100,
    receiveRate: { status: 'ok', bytesPerSec: 8e6, epoch: 1, sampledAt: 3 },
  });
  assert.equal(telemetry.receiveText, UNKNOWN);
  assert.equal(telemetry.bufferText, '19s');
  assert.equal(telemetry.layers.played.widthPercent, 31);
});

test('a pure-time frame without telemetry keeps its continuous buffer and rate', () => {
  const telemetry = freshModule().create();
  telemetry.update(snapshot({
    epoch: 3, time: 20, duration: 100,
    ranges: [{ start: 0, end: 60 }], bytesPerSec: 1e6, mbps: 2,
  }));
  telemetry.update({ time: 21, duration: 100, subtitleIndex: 4 });
  assert.equal(telemetry.bufferText, '39s');
  assert.equal(telemetry.receiveText, '1.0 MB/s');
  assert.equal(telemetry.sourceText, '2.0 Mbps');
  assert.equal(telemetry.layers.played.widthPercent, 21);
});

test('an unreadable buffered marker cannot hide a stale rate epoch', () => {
  const telemetry = freshModule().create();
  telemetry.update(snapshot({
    epoch: 5, time: 20, duration: 100,
    ranges: [{ start: 0, end: 60 }], bytesPerSec: 2e6, mbps: 3,
  }));
  telemetry.update({
    time: 90, duration: 200, sourceBitrateMbps: 9,
    buffered: { status: 'ok', ranges: [{ start: 0, end: 190 }], epoch: 4.5 },
    receiveRate: { status: 'ok', bytesPerSec: 9e6, epoch: 4, sampledAt: 1 },
  });
  // The frame epoch comes from the only readable marker, so the whole frame is
  // stale rather than silently falling back to an unreadable buffer epoch.
  assert.equal(telemetry.layers.played.widthPercent, 20);
  assert.equal(telemetry.bufferText, '40s');
  assert.equal(telemetry.receiveText, '2.0 MB/s');
  assert.equal(telemetry.sourceText, '3.0 Mbps');
});

test('an unsafe or fractional epoch cannot bypass the generation gate', () => {
  for (const epoch of [4.5, -1, 2 ** 53, '5']) {
    const telemetry = freshModule().create();
    telemetry.update(snapshot({
      epoch: 5, time: 20, duration: 100,
      ranges: [{ start: 0, end: 60 }], bytesPerSec: 2e6, mbps: 3,
    }));
    telemetry.update({
      time: 90, duration: 200, sourceBitrateMbps: 9,
      buffered: { status: 'ok', ranges: [{ start: 0, end: 190 }], epoch },
    });
    assert.equal(telemetry.layers.played.widthPercent, 20, 'played for ' + String(epoch));
    assert.equal(telemetry.sourceText, '3.0 Mbps', 'source for ' + String(epoch));
    assert.equal(telemetry.bufferText, UNKNOWN, 'buffer for ' + String(epoch));
    assert.equal(telemetry.receiveText, '2.0 MB/s', 'rate for ' + String(epoch));
  }
});

test('a server revision is never used as the generation marker', () => {
  const telemetry = freshModule().create();
  telemetry.update(snapshot({
    epoch: 2, time: 10, duration: 100,
    ranges: [{ start: 0, end: 20 }], bytesPerSec: 1e6, mbps: 2,
  }));
  telemetry.update({
    time: 30, duration: 100, revision: 9000,
    buffered: { status: 'ok', ranges: [{ start: 0, end: 40 }], epoch: 2, revision: 9000 },
    receiveRate: { status: 'ok', bytesPerSec: 2e6, epoch: 2, revision: 9000, sampledAt: 2 },
  });
  assert.equal(telemetry.layers.played.widthPercent, 30);
  assert.equal(telemetry.bufferText, '10s');
  assert.equal(telemetry.receiveText, '2.0 MB/s');
});

test('the receive rate stays live while paused (still receiving bytes)', () => {
  const telemetry = freshModule().create();
  telemetry.update({
    time: 30, duration: 100, playing: false, paused: true,
    receiveRate: { status: 'ok', bytesPerSec: 2.5e6, epoch: 1, sampledAt: 500 },
  });
  assert.equal(telemetry.receiveText, '2.5 MB/s');
});

/* ---------------------------------------------------------------- geometry */

test('every emitted style value is a fixed colour token or a bounded number', () => {
  const telemetry = freshModule().create();
  telemetry.update(snapshot({
    epoch: 1, time: 33, duration: 100,
    ranges: [{ start: 0, end: 12.5 }, { start: 40, end: 99.5 }],
  }));
  const layers = telemetry.layers;
  assert.deepEqual(Object.keys(layers).sort(), ['buffered', 'played', 'track']);
  for (const color of [layers.track.backgroundColor, layers.buffered.backgroundColor, layers.played.backgroundColor]) {
    assert.match(color, /^var\(--eio-progress-[a-z]+, #[0-9a-f]{6}\)$/);
  }
  assert.equal(layers.played.widthPercent, 33);
  for (const segment of layers.buffered.segments) {
    assert.deepEqual(Object.keys(segment).sort(), ['startPercent', 'widthPercent']);
    for (const value of [segment.startPercent, segment.widthPercent]) {
      assert.equal(typeof value, 'number');
      assert.ok(Number.isFinite(value) && value >= 0 && value <= 100);
    }
  }
});

/* -------------------------------------------------------------------- HTML */

function region(startPattern, endPattern) {
  const start = HTML_SRC.search(startPattern);
  assert.ok(start >= 0, 'missing start marker ' + startPattern);
  const end = HTML_SRC.slice(start).search(endPattern);
  assert.ok(end >= 0, 'missing end marker ' + endPattern);
  return HTML_SRC.slice(start, start + end);
}

test('web.html: the HUD lives in the one control layer and has no visibility state of its own', () => {
  const layer = region(/<div class="eio-play-layer"/, /<script src="\/web\/vendor\/vue/);
  // aria-hidden follows the same controlsVisible state that hides the layer;
  // it is not a permanent hard-coded true or a second visibility source.
  assert.match(layer, /<div class="eio-play-hud" :aria-hidden="!playerUI\.controlsVisible">/);
  assert.doesNotMatch(layer, /<div class="eio-play-hud"[^>]*\saria-hidden="true"/);
  // It rides inside the already-hidden top control row.
  const top = region(/<div class="eio-play-top">/, /<div v-if="playerUI.status==='loading'"/);
  assert.match(top, /class="eio-play-hud"/);
  // No second visibility mechanism: no v-if/v-show/independent class binding.
  assert.doesNotMatch(HTML_SRC, /<div[^>]*eio-play-hud[^>]*\sv-(if|show)/);
  assert.doesNotMatch(HTML_SRC, /<div[^>]*\sv-(if|show)[^>]*eio-play-hud/);
  assert.doesNotMatch(HTML_SRC, /eio-play-hud[^>]*:class=/);
  // No aria-live anywhere: nothing is announced every second.
  assert.doesNotMatch(HTML_SRC, /aria-live/i);
  // Values come from the view-model getters only.
  assert.match(layer, /\{\{ playerTelemetry\.receiveText \}\}/);
  assert.match(layer, /\{\{ playerTelemetry\.sourceText \}\}/);
  assert.match(layer, /\{\{ playerTelemetry\.bufferText \}\}/);
  assert.match(layer, /接收/);
  assert.match(layer, /源码率（约）/);
  assert.match(layer, /缓冲/);
});

test('web.html: the seek bar keeps the native input, ARIA and every existing handler', () => {
  const seek = region(/<div class="eio-seek">/, /<div class="eio-play-row">/);
  assert.match(seek, /class="eio-seek-rail" aria-hidden="true"/);
  assert.match(seek, /v-for="\(seg, i\) in \(playerTelemetry\.layers\?\.buffered\?\.segments \|\| \[\]\)"/);
  assert.match(seek, /class="eio-seek-buffered"[^>]*:style="\{ left: seg\.startPercent \+ '%', width: seg\.widthPercent \+ '%' \}"/);
  assert.match(seek, /class="eio-seek-played"[^>]*:style="\{ width: \(playerTelemetry\.layers\?\.played\?\.widthPercent \|\| 0\) \+ '%' \}"/);
  const input = seek.match(/<input[^>]*class="eio-seek-input"[\s\S]*?>/);
  assert.ok(input, 'the native range input is still there');
  const markup = input[0];
  assert.match(markup, /type="range"/);
  assert.match(markup, /min="0"/);
  assert.match(markup, /step="any"/);
  assert.match(markup, /:max="Math\.max\(wasmDuration, 0\)"/);
  assert.match(markup, /:value="seekPreview \?\? wasmTime"/);
  assert.match(markup, /:disabled="!playerAlive \|\| !wasmDuration"/, '0 duration disables the bar');
  assert.match(markup, /aria-label="播放进度"/);
  assert.match(markup, /:aria-valuetext="wasmTimeHuman \+ ' \/ ' \+ wasmDurationHuman"/);
  assert.match(markup, /@input="previewSeek\(\$event\.target\.value\)"/);
  assert.match(markup, /@change="onWasmSeek\(\$event\.target\.value\)"/);
  assert.match(markup, /@pointerup="endScrub"/);
  assert.match(markup, /@pointercancel="cancelScrub"/);
  // The rail sits before the input, so the input stays on top for pointer/touch.
  assert.ok(seek.indexOf('eio-seek-rail') < seek.indexOf('eio-seek-input'));
});

test('web.html: module and stylesheet order is progress -> buffer -> telemetry -> libmedia', () => {
  const head = HTML_SRC.slice(0, HTML_SRC.indexOf('</head>'));
  const uiCss = head.indexOf('/web/web/ui.css');
  const progressCss = head.indexOf('/web/web/player-progress.css');
  assert.ok(uiCss >= 0 && progressCss > uiCss, 'player-progress.css loads after ui.css');

  const progressAt = HTML_SRC.indexOf('/web/web/player-progress.js');
  const bufferAt = HTML_SRC.indexOf('/web/web/player-buffer.js');
  const telemetryAt = HTML_SRC.indexOf('/web/web/player-telemetry-ui.js');
  const libmediaAt = HTML_SRC.indexOf('/web/web/player-libmedia.js');
  const appAt = HTML_SRC.indexOf('/web/web/app.js');
  assert.ok(progressAt >= 0 && bufferAt > progressAt);
  assert.ok(telemetryAt > bufferAt, 'telemetry loads after its data modules');
  assert.ok(libmediaAt > telemetryAt, 'all three load before player-libmedia.js');
  assert.ok(appAt > libmediaAt);
  assert.ok(HTML_SRC.includes('/web/web/theme-player-controls.js'), 'the existing theme hook stays loaded');
});

/* -------------------------------------------------------------------- CSS */

function cssRules(source) {
  const rules = [];
  const pattern = /([^{}]+)\{([^{}]*)\}/g;
  let match;
  while ((match = pattern.exec(source)) !== null) {
    rules.push({
      selector: match[1].replace(/\/\*[\s\S]*?\*\//g, '').trim(),
      body: match[2],
    });
  }
  return rules;
}

const PROGRESS_VARS = ['--eio-progress-played', '--eio-progress-buffered', '--eio-progress-track', '--eio-progress-thumb'];

test('player-progress.css declares the defaults once, in the player domain only', () => {
  const defaultRules = cssRules(CSS_SRC).filter(rule =>
    PROGRESS_VARS.every(name => rule.body.includes(name + ':')));
  assert.equal(defaultRules.length, 1, 'exactly one default palette block');
  const selector = defaultRules[0].selector.replace(/\s+/g, ' ');
  assert.equal(selector, '.player-container, .eio-play-layer');
  // Lower specificity than the generated theme block, so a v2 theme always
  // wins regardless of load order; and no descendant re-declares a default,
  // which would shadow the inherited theme value.
  assert.doesNotMatch(selector, /body\.eio-web/);
  assert.doesNotMatch(selector, /\.eio-seek/);
  assert.deepEqual(
    PROGRESS_VARS.map(name => defaultRules[0].body.match(new RegExp(name + ':\\s*([^;]+);'))[1].trim()),
    ['#3b82f6', '#f3f4f6', '#111827', '#3b82f6']
  );
  for (const rule of cssRules(CSS_SRC)) {
    if (rule.selector.includes('.eio-seek')) {
      assert.doesNotMatch(rule.body, /--eio-progress-[a-z]+:\s*#/, 'no seek-scoped default: ' + rule.selector);
    }
  }
});

test('player-progress.css keeps the three layers themed and the thumb outlined', () => {
  const rules = cssRules(CSS_SRC);
  const find = selector => rules.find(rule => rule.selector === selector);
  assert.match(find('.eio-seek-rail').body, /background:\s*var\(--eio-progress-track, #111827\)/);
  assert.match(find('.eio-seek-buffered').body, /background:\s*var\(--eio-progress-buffered, #f3f4f6\)/);
  assert.match(find('.eio-seek-played').body, /background:\s*var\(--eio-progress-played, #3b82f6\)/);
  assert.match(find('.eio-seek-rail').body, /pointer-events:\s*none/, 'the rail never captures pointer input');

  const webkitThumb = find('.eio-seek input.eio-seek-input[type=range]::-webkit-slider-thumb');
  const mozThumb = find('.eio-seek input.eio-seek-input[type=range]::-moz-range-thumb');
  for (const thumb of [webkitThumb, mozThumb]) {
    assert.match(thumb.body, /background:\s*var\(--eio-progress-thumb, #3b82f6\)/);
    assert.match(thumb.body, /box-shadow:\s*0 0 0 2px #000, 0 0 0 4px #fff/, 'fixed two-tone outline');
  }
  const focus = find('.eio-seek input.eio-seek-input[type=range]:focus-visible');
  assert.match(focus.body, /outline:\s*2px solid #fff/);
  assert.match(focus.body, /box-shadow:\s*0 0 0 4px #000/);
  // The transparent native input must stay above the painted layers.
  const input = find('.eio-seek input.eio-seek-input[type=range]');
  assert.match(input.body, /background:\s*transparent/);
  assert.match(input.body, /z-index:\s*2/);
});

test('player-progress.css keeps HUD colours fixed instead of inheriting page tokens', () => {
  const rules = cssRules(CSS_SRC);
  const find = selector => rules.find(rule => rule.selector === selector);
  const hud = find('.eio-play-hud');
  const label = find('.eio-play-hud-label');
  const value = find('.eio-play-hud-value');
  for (const [name, rule] of [['hud', hud], ['label', label], ['value', value]]) {
    assert.ok(rule, name + ' rule exists');
    assert.doesNotMatch(
      rule.body,
      /var\(--(?:t1|t2|line-2)\b/,
      name + ' must not follow light/dark page colour tokens'
    );
  }
  // Fixed grey-white values stay readable on the always-dark player surface.
  assert.match(hud.body, /border:\s*1px solid #[0-9a-f]{6}/i);
  assert.match(hud.body, /color:\s*#[0-9a-f]{6}/i);
  assert.match(label.body, /color:\s*#[0-9a-f]{6}/i);
  assert.match(value.body, /color:\s*#[0-9a-f]{6}/i);
  // Layout/transition tokens are unrelated to page colours and remain allowed.
  assert.match(hud.body, /transition:\s*opacity var\(--dur-base, \.18s\) var\(--ease, ease\)/);
});

test('player-progress.css never hides the HUD on its own and stays out of the page', () => {
  for (const rule of cssRules(CSS_SRC)) {
    if (rule.selector.includes('.eio-play-hud')) {
      assert.match(rule.selector, /^\.eio-play-hud/, 'only HUD-scoped selectors');
      assert.doesNotMatch(rule.body, /display:\s*none|visibility:\s*hidden/);
    }
    assert.doesNotMatch(rule.selector, /eio-(nav|sidebar|login|library|admin)/);
  }
  const hud = cssRules(CSS_SRC).find(rule => rule.selector === '.eio-play-hud');
  assert.match(hud.body, /pointer-events:\s*none/, 'the HUD cannot widen hover/touch capture');
  assert.doesNotMatch(hud.body, /position:\s*fixed/);
  // The single visibility source stays the existing control-layer rule.
  assert.match(UI_CSS_SRC, /\.eio-play-layer\.hidden \.eio-play-top/);
  assert.match(UI_CSS_SRC, /\.eio-play-layer\.hidden \.eio-play-bottom/);
});

/* ------------------------------------------------------------------ app.js */

test('app.js wires the HUD with three thin hooks and no new state', () => {
  const APP_SRC = fs.readFileSync(path.join(WEB_DIR, 'app.js'), 'utf8');
  const creates = APP_SRC.match(/EIOPlayerTelemetry\.create\(\{ reactive \}\)/g) || [];
  assert.equal(creates.length, 1, 'one telemetry view model per app setup');
  assert.match(APP_SRC, /const playerTelemetry = EIOPlayerTelemetry\.create/);
  // Created next to playerUI, before the first playback entry point.
  assert.ok(APP_SRC.indexOf('EIOPlayerTelemetry.create') < APP_SRC.indexOf('async function playItem'));
  const updates = APP_SRC.match(/playerTelemetry\.update\(/g) || [];
  const resets = APP_SRC.match(/playerTelemetry\.reset\(/g) || [];
  assert.equal(updates.length, 1, 'one update call site');
  assert.equal(resets.length, 1, 'one reset call site');
  // The snapshot is forwarded only after the instance/generation guard.
  const onStateAt = APP_SRC.indexOf('instance.onState = (state) => {');
  assert.ok(onStateAt > 0);
  const guardAt = APP_SRC.indexOf('if (player !== instance || generation !== playbackGeneration) return;', onStateAt);
  const updateAt = APP_SRC.indexOf('playerTelemetry.update(state);', onStateAt);
  assert.ok(guardAt > onStateAt && updateAt > guardAt, 'ownership is checked before the HUD sees data');
  // Reset lives in stopPlayer, which every new load / stop / leave goes through.
  assert.ok(APP_SRC.indexOf('playerTelemetry.reset();') > APP_SRC.indexOf('function stopPlayer()'));
  assert.match(APP_SRC, /playerContainer, playerUI, playerTelemetry,/);
});

/* ------------------------------------------------- harness + fake engine ---- */

const { harness, makeStorage, delay } = require('./web-ui-harness.cjs');

function makePlayerClass() {
  const instances = [];
  class FakePlayer {
    constructor() {
      this.sessionId = 'ps-' + (instances.length + 1);
      this.mediaSource = null;
      this.positionTicks = 0;
      this.duration = 3600;
      this.playing = false;
      this.subtitleIndex = -1;
      instances.push(this);
    }
    async load(url, mediaSource) {
      this.mediaSource = mediaSource;
      this.playing = true;
      this.onState?.({ playing: true, loading: false, time: 0, duration: this.duration, mode: 'mse' });
    }
    async seek(seconds) { this.positionTicks = Math.round(seconds * 10000000); }
    async pause() { this.playing = false; }
    async play() { this.playing = true; }
    setVolume() {}
    destroy() { return Promise.resolve(); }
    selectSubtitle() { return true; }
    selectAudio() { return true; }
  }
  return { FakePlayer, instances };
}

const episode = id => ({ Id: id, Name: id, Type: 'Episode', SeriesId: 's1' });

function mediaSource() {
  return {
    Id: 'ms1',
    Container: 'mp4',
    DirectStreamUrl: '/Videos/e1/stream.mp4?Static=true&MediaSourceId=ms1',
    MediaStreams: [{ Type: 'Audio', Index: 1, DisplayTitle: 'AAC', IsDefault: true }],
  };
}

function setup() {
  const engine = makePlayerClass();
  const env = harness({
    storage: makeStorage({ eio_token: 'token', eio_user_id: 'user' }),
    reply: url => {
      // Item detail first: app.js re-fetches the episode because the list entry
      // carries no MediaSources (same shape as the recovery suite).
      const detail = url.pathname.match(/\/Items\/([^/]+)$/);
      if (detail) return episode(decodeURIComponent(detail[1]));
      if (url.pathname.endsWith('/PlaybackInfo')) return { MediaSources: [mediaSource()], PlaySessionId: 'sess' };
      if (url.pathname.startsWith('/Sessions/Playing')) return {};
      if (url.pathname.startsWith('/Users/')) return { Policy: {} };
      return { Items: [] };
    },
    beforeApp: context => { context.EIOPlayer = engine.FakePlayer; context.EIO_ENGINE_CLEANUP_BUDGET_MS = 1000; },
  });
  env.player = engine;
  env.app.page.value = 'app';
  return env;
}

test('harness: the real telemetry module is loaded before app.js', () => {
  const env = setup();
  assert.equal(typeof env.telemetry.create, 'function');
  assert.equal(typeof env.progress.progressLayers, 'function');
  assert.equal(typeof env.bufferModule.snapshot, 'function');
  assert.ok(env.app.playerTelemetry, 'app exposes the view model to the template');
  assert.equal(env.app.playerTelemetry.receiveText, UNKNOWN);
  assert.equal(env.app.playerTelemetry.layers, null);
});

test('harness: engine snapshots drive the HUD and leaving playback resets it', async () => {
  const { app, player, context } = setup();
  context.window.location.hash = '#/play/e1';
  await app.playItem(episode('e1'));
  assert.equal(player.instances.length, 1);
  const instance = player.instances[0];

  instance.onState({
    mode: 'mse', playing: true, time: 42, duration: 100,
    buffered: { status: 'ok', ranges: [{ start: 0, end: 60 }], epoch: 5 },
    sourceBitrateMbps: 3.25,
    receiveRate: { status: 'ok', bytesPerSec: 2e6, epoch: 5, sampledAt: 10 },
  });
  assert.equal(app.playerTelemetry.receiveText, '2.0 MB/s');
  assert.equal(app.playerTelemetry.sourceText, '3.3 Mbps');
  assert.equal(app.playerTelemetry.bufferText, '18s');
  assert.equal(app.playerTelemetry.layers.played.widthPercent, 42);

  // Unknown rate from the adapter: a dash, not 0.
  instance.onState({
    mode: 'mse', playing: true, time: 43, duration: 100,
    buffered: { status: 'ok', ranges: [{ start: 0, end: 60 }], epoch: 5 },
    sourceBitrateMbps: null,
    receiveRate: { status: 'unknown', bytesPerSec: null, epoch: 5, sampledAt: 11 },
  });
  assert.equal(app.playerTelemetry.receiveText, UNKNOWN);
  assert.equal(app.playerTelemetry.sourceText, UNKNOWN);
  assert.equal(app.playerTelemetry.bufferText, '17s');

  // Leaving the player resets every metric.
  app.exitPlayer();
  assert.equal(app.playerTelemetry.receiveText, UNKNOWN);
  assert.equal(app.playerTelemetry.bufferText, UNKNOWN);
  assert.equal(app.playerTelemetry.layers, null);

  // A superseded engine can no longer write into the HUD.
  instance.onState({
    mode: 'mse', playing: true, time: 12, duration: 100,
    buffered: { status: 'ok', ranges: [{ start: 0, end: 100 }], epoch: 9 },
    receiveRate: { status: 'ok', bytesPerSec: 7e6, epoch: 9, sampledAt: 12 },
  });
  assert.equal(app.playerTelemetry.receiveText, UNKNOWN);
  assert.equal(app.playerTelemetry.bufferText, UNKNOWN);
});

test('harness: a new load starts from unknown instead of the previous generation', async () => {
  const { app, player, context } = setup();
  context.window.location.hash = '#/play/e1';
  await app.playItem(episode('e1'));
  player.instances[0].onState({
    mode: 'mse', playing: true, time: 30, duration: 100,
    buffered: { status: 'ok', ranges: [{ start: 0, end: 80 }], epoch: 3 },
    receiveRate: { status: 'ok', bytesPerSec: 1e6, epoch: 3, sampledAt: 1 },
  });
  assert.equal(app.playerTelemetry.bufferText, '50s');

  // Route changes start the next load through stopPlayer, which resets the HUD.
  context.window.location.hash = '#/play/e2';
  await app.playItem(episode('e2'));
  await delay(10);
  assert.equal(player.instances.length, 2);
  assert.equal(app.playerTelemetry.receiveText, UNKNOWN);
  assert.equal(app.playerTelemetry.bufferText, UNKNOWN);
  // The new engine reports an unknown buffer: still a dash, no stale islands.
  player.instances[1].onState({
    mode: 'mse', playing: true, time: 5, duration: 100,
    buffered: { status: 'unknown', ranges: [], epoch: 1 },
    sourceBitrateMbps: 1.5,
    receiveRate: { status: 'unknown', bytesPerSec: null, epoch: 1, sampledAt: 2 },
  });
  assert.equal(app.playerTelemetry.receiveText, UNKNOWN);
  assert.equal(app.playerTelemetry.layers.buffered, null);
  assert.equal(app.playerTelemetry.sourceText, '1.5 Mbps');
});
