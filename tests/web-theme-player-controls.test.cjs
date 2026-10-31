'use strict';

const test = require('node:test');
const assert = require('node:assert');
const vm = require('node:vm');

const mod = require('../public/web/theme-player-controls.js');
const { normalizeThemeFields, DEFAULT_PALETTE, contrastRatio } = mod;

const GOOD = {
  played: '#3b82f6',
  buffered: '#f3f4f6',
  track: '#111827',
  thumb: '#3b82f6'
};

function v2(fields) {
  return normalizeThemeFields({ themeApiVersion: 2, playerControls: fields });
}

test('module shape: UMD exports normalizeThemeFields', () => {
  assert.strictEqual(typeof normalizeThemeFields, 'function');
  assert.strictEqual(typeof mod, 'object');
});

test('default: missing version falls back to v1', () => {
  assert.deepStrictEqual(normalizeThemeFields({}), { themeApiVersion: 1 });
  assert.deepStrictEqual(normalizeThemeFields({ foo: 1 }), { themeApiVersion: 1 });
});

test('default: explicit undefined version is treated as missing', () => {
  assert.deepStrictEqual(normalizeThemeFields({ themeApiVersion: undefined }), {
    themeApiVersion: 1
  });
});

test('builtin: version defaults to 0 and accepts explicit 0', () => {
  assert.deepStrictEqual(normalizeThemeFields({}, true), { themeApiVersion: 0 });
  assert.deepStrictEqual(normalizeThemeFields({ themeApiVersion: 0 }, true), {
    themeApiVersion: 0
  });
  assert.deepStrictEqual(normalizeThemeFields({ themeApiVersion: undefined }, true), {
    themeApiVersion: 0
  });
});

test('builtin: rejects version 1 and 2 and any playerControls block', () => {
  assert.strictEqual(normalizeThemeFields({ themeApiVersion: 1 }, true), null);
  assert.strictEqual(normalizeThemeFields({ themeApiVersion: 2 }, true), null);
  assert.strictEqual(
    normalizeThemeFields({ playerControls: GOOD }, true),
    null
  );
  assert.strictEqual(
    normalizeThemeFields({ playerControls: null }, true),
    null
  );
});

test('custom: explicit 0 / null / string / NaN / 3 are rejected', () => {
  for (const bad of [0, null, '1', '2', NaN, 3, -1, 1.5, true, {}]) {
    assert.strictEqual(
      normalizeThemeFields({ themeApiVersion: bad }),
      null,
      `version ${String(bad)} must be rejected`
    );
  }
});

test('builtin: explicit 0 is not confused with custom 0 rejection', () => {
  assert.deepStrictEqual(normalizeThemeFields({ themeApiVersion: 0 }, true), {
    themeApiVersion: 0
  });
  assert.strictEqual(normalizeThemeFields({ themeApiVersion: 0 }), null);
});

test('non-object input is rejected', () => {
  for (const bad of [null, undefined, 1, 'x', [], true]) {
    assert.strictEqual(normalizeThemeFields(bad), null);
  }
});

test('v1: any own playerControls field rejects, including null/undefined', () => {
  assert.strictEqual(normalizeThemeFields({ themeApiVersion: 1, playerControls: null }), null);
  assert.strictEqual(
    normalizeThemeFields({ themeApiVersion: 1, playerControls: undefined }),
    null
  );
  assert.strictEqual(normalizeThemeFields({ themeApiVersion: 1, playerControls: GOOD }), null);
  assert.deepStrictEqual(normalizeThemeFields({ themeApiVersion: 1 }), {
    themeApiVersion: 1
  });
});

test('v2: missing block is allowed and injects no defaults', () => {
  const out = normalizeThemeFields({ themeApiVersion: 2 });
  assert.deepStrictEqual(out, { themeApiVersion: 2 });
  assert.strictEqual(Object.prototype.hasOwnProperty.call(out, 'playerControls'), false);
});

test('v2: full valid block is normalized and lowercased', () => {
  const out = v2({ played: '#3B82F6', buffered: '#F3F4F6', track: '#111827', thumb: '#3B82F6' });
  assert.deepStrictEqual(out, {
    themeApiVersion: 2,
    playerControls: {
      played: '#3b82f6',
      buffered: '#f3f4f6',
      track: '#111827',
      thumb: '#3b82f6'
    }
  });
});

test('v2: block must be non-null plain object', () => {
  for (const bad of [null, undefined, [], [GOOD], 'x', 1, true]) {
    assert.strictEqual(v2(bad), null, `block ${String(bad)} must be rejected`);
  }
});

test('v2: partial, unknown, and invalid color fields are rejected', () => {
  assert.strictEqual(v2({ played: '#3b82f6' }), null);
  const partial = Object.assign({}, GOOD);
  delete partial.thumb;
  assert.strictEqual(v2(partial), null);
  assert.strictEqual(v2(Object.assign({}, GOOD, { extra: '#3b82f6' })), null);
  assert.strictEqual(v2(Object.assign({}, GOOD, { played: null })), null);
  assert.strictEqual(v2(Object.assign({}, GOOD, { played: undefined })), null);
});

test('v2: rejects CSS, URLs, short hex, whitespace, and non-strings', () => {
  const badValues = [
    'red',
    '#fff',
    '#3b82f',
    '#3b82f60',
    ' #3b82f6',
    '#3b82f6 ',
    'rgb(59,130,246)',
    'url(#3b82f6)',
    '#gggggg',
    '3b82f6',
    '',
    0x3b82f6,
    {},
    []
  ];
  for (const bad of badValues) {
    const fields = Object.assign({}, GOOD, { played: bad });
    assert.strictEqual(v2(fields), null, `value ${String(bad)} must be rejected`);
  }
});

test('v2: insufficient contrast between played/buffered/track is rejected', () => {
  // played vs track: both mid-dark blues -> low contrast
  assert.strictEqual(
    v2({ played: '#1f2937', buffered: '#374151', track: '#111827', thumb: '#f9fafb' }),
    null
  );
  // buffered vs track low contrast
  assert.strictEqual(
    v2({ played: '#fafafa', buffered: '#1f2937', track: '#111827', thumb: '#fafafa' }),
    null
  );
  // thumb vs track too low
  assert.strictEqual(
    v2({ played: '#3b82f6', buffered: '#f3f4f6', track: '#1f2937', thumb: '#111827' }),
    null
  );
});

test('v2: thumb may equal played', () => {
  const out = v2({ played: '#3b82f6', buffered: '#f3f4f6', track: '#111827', thumb: '#3b82f6' });
  assert.ok(out);
  assert.strictEqual(out.playerControls.thumb, out.playerControls.played);
});

test('default palette satisfies WCAG contrast >= 3', () => {
  assert.ok(contrastRatio(DEFAULT_PALETTE.played, DEFAULT_PALETTE.buffered) >= 3);
  assert.ok(contrastRatio(DEFAULT_PALETTE.played, DEFAULT_PALETTE.track) >= 3);
  assert.ok(contrastRatio(DEFAULT_PALETTE.buffered, DEFAULT_PALETTE.track) >= 3);
  assert.ok(contrastRatio(DEFAULT_PALETTE.thumb, DEFAULT_PALETTE.track) >= 3);
  const out = v2(DEFAULT_PALETTE);
  assert.ok(out);
});

test('result is a fresh object and input is not mutated', () => {
  const input = {
    themeApiVersion: 2,
    playerControls: { played: '#3B82F6', buffered: '#F3F4F6', track: '#111827', thumb: '#3B82F6' }
  };
  const snapshot = JSON.stringify(input);
  const out = normalizeThemeFields(input);
  assert.notStrictEqual(out, input);
  assert.notStrictEqual(out.playerControls, input.playerControls);
  assert.strictEqual(JSON.stringify(input), snapshot);
  assert.strictEqual(input.playerControls.played, '#3B82F6');

  // repeated calls do not share state
  const a = normalizeThemeFields({ themeApiVersion: 2, playerControls: DEFAULT_PALETTE });
  const b = normalizeThemeFields({ themeApiVersion: 2, playerControls: DEFAULT_PALETTE });
  assert.notStrictEqual(a.playerControls, b.playerControls);
  a.playerControls.played = '#ffffff';
  assert.strictEqual(b.playerControls.played, '#3b82f6');
});

test('no untrusted string content is ever executed or returned raw', () => {
  // Prototype-polluting and code-like payloads must simply be rejected.
  const payloads = [
    'globalThis.__pwned = 1',
    '${process.exit(1)}',
    '#3b82f6;process.exit(1)',
    '__proto__',
    'constructor'
  ];
  for (const p of payloads) {
    assert.strictEqual(v2(Object.assign({}, GOOD, { played: p })), null);
  }
  const weird = { themeApiVersion: 2, playerControls: JSON.parse('{"__proto__":{"x":1}}') };
  assert.strictEqual(normalizeThemeFields(weird), null);
  assert.strictEqual(globalThis.__pwned, undefined);
});

test('plain objects from another VM realm are accepted', () => {
  const foreign = vm.runInNewContext(
    '({ themeApiVersion: 2, playerControls: {' +
      ' played: "#3B82F6", buffered: "#F3F4F6",' +
      ' track: "#111827", thumb: "#3B82F6" } })'
  );
  // Sanity: this really is a different realm, not this realm's Object.prototype.
  assert.notStrictEqual(Object.getPrototypeOf(foreign), Object.prototype);
  assert.notStrictEqual(
    Object.getPrototypeOf(foreign.playerControls),
    Object.prototype
  );
  assert.deepStrictEqual(normalizeThemeFields(foreign), {
    themeApiVersion: 2,
    playerControls: {
      played: '#3b82f6',
      buffered: '#f3f4f6',
      track: '#111827',
      thumb: '#3b82f6'
    }
  });
});

test('another realm still rejects non-plain objects', () => {
  for (const src of ['new Date()', '[1, 2]', 'Object.create({})', 'new (class Weird {})()']) {
    assert.strictEqual(normalizeThemeFields(vm.runInNewContext(src)), null, src);
  }
});

test('class instances, custom prototypes, and Date are rejected', () => {
  class Weird {
    constructor() {
      this.themeApiVersion = 2;
    }
  }
  const bad = [
    new Weird(),
    Object.create({}),
    Object.create({ themeApiVersion: 2 }),
    new Date()
  ];
  for (const value of bad) {
    assert.strictEqual(normalizeThemeFields(value), null);
    // Also rejected when nested as the playerControls block.
    assert.strictEqual(v2(value), null);
  }
});

test('null-prototype inputs are accepted', () => {
  const root = Object.create(null);
  root.themeApiVersion = 2;
  const controls = Object.create(null);
  controls.played = '#3b82f6';
  controls.buffered = '#f3f4f6';
  controls.track = '#111827';
  controls.thumb = '#3b82f6';
  root.playerControls = controls;

  assert.strictEqual(Object.getPrototypeOf(root), null);
  assert.deepStrictEqual(normalizeThemeFields(root), {
    themeApiVersion: 2,
    playerControls: {
      played: '#3b82f6',
      buffered: '#f3f4f6',
      track: '#111827',
      thumb: '#3b82f6'
    }
  });
});
