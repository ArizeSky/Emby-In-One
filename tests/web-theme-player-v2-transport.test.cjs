'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const ROOT = path.resolve(__dirname, '..');
const ORIGIN = 'https://eio.invalid';
const HASH = 'a'.repeat(64);
const MODULES = ['theme-player-controls.js', 'theme.js', 'theme-transport.js'];

// Fixed contract: these are the four playerControls keys and default colors.
const PLAYER_KEYS = ['played', 'buffered', 'track', 'thumb'];
const DEFAULT_PLAYER_CONTROLS = {
  played: '#3b82f6',
  buffered: '#f3f4f6',
  track: '#111827',
  thumb: '#3b82f6',
};

function loadContext() {
  const ctx = vm.createContext({ console, URL, setTimeout, clearTimeout });
  ctx.window = ctx;
  for (const file of MODULES) {
    const source = fs.readFileSync(path.join(ROOT, 'public', 'web', file), 'utf8');
    vm.runInContext(source, ctx, { filename: file });
  }
  return ctx;
}

function variantTokens() {
  return {
    bg0: '#010203', bg1: '#040506', bg2: '#070809', bg3: '#0a0b0c',
    text1: '#f1f2f3', text2: '#d1d2d3', text3: '#b1b2b3',
    line: '#111213', line2: '#141516', accent: '#123456', accentText: '#718191',
  };
}

// Legacy-safe DTO: no definition.playerControls and no themeApiVersion by default.
function makeDto(extra) {
  return Object.assign({
    id: 'ocean-1',
    name: 'Ocean',
    author: 'test',
    description: '',
    version: '1.0',
    hash: HASH,
    variants: ['light', 'dark'],
    builtin: false,
    cssUrl: '/web/themes/' + HASH + '/theme.css',
    backgroundUrl: '',
    previewUrl: '',
    previewLightUrl: '',
    previewDarkUrl: '',
    definition: {
      variants: { light: variantTokens(), dark: variantTokens() },
      appearance: { radius: 14, density: 'comfortable' },
      backgroundImage: '',
    },
  }, extra);
}

function withDefinition(dto, extra) {
  const copy = Object.assign({}, dto);
  copy.definition = Object.assign({}, dto.definition, extra);
  return copy;
}

function plain(value) {
  return JSON.parse(JSON.stringify(value));
}

function adapt(ctx, dto) {
  return ctx.EIOThemeTransport.adaptPublicTheme(dto, { origin: ORIGIN });
}

function v2Dto(playerControls) {
  return withDefinition(makeDto({ themeApiVersion: 2 }), { playerControls });
}

test('valid v2 DTO normalizes definition.playerControls and survives a cross-realm round trip', () => {
  const ctx = loadContext();
  const base = plain(DEFAULT_PLAYER_CONTROLS);
  assert.deepEqual(Object.keys(base).sort(), PLAYER_KEYS.slice().sort());

  const res = adapt(ctx, v2Dto(plain(base)));
  assert.notEqual(res, null);
  assert.equal(res.version, '1.0');
  assert.equal(res.hash, HASH);
  assert.equal(res.theme.themeApiVersion, 2);
  assert.equal(res.theme.id, 'ocean-1');
  assert.equal(res.theme.name, 'Ocean');
  assert.deepEqual(plain(res.theme.playerControls), base);

  const shouted = {};
  for (const key of PLAYER_KEYS) shouted[key] = base[key].toUpperCase();
  const normalized = adapt(ctx, v2Dto(shouted));
  assert.notEqual(normalized, null);
  assert.deepEqual(plain(normalized.theme.playerControls), base);

  const copy = plain(res.theme);
  const again = ctx.EIOTheme.normalizeTheme(copy);
  assert.notEqual(again, null);
  assert.deepEqual(plain(again), copy);
  copy.playerControls.played = '#000000';
  assert.deepEqual(plain(res.theme.playerControls), base);
});

test('missing outer themeApiVersion is treated as legacy version 1', () => {
  const ctx = loadContext();
  const res = adapt(ctx, makeDto());
  assert.notEqual(res, null);
  assert.equal(res.theme.themeApiVersion, 1);
  assert.equal(res.version, '1.0');
  assert.equal(res.hash, HASH);
  assert.equal(res.theme.id, 'ocean-1');
  assert.equal(res.theme.playerControls, undefined);
});

test('unsupported outer themeApiVersion values are rejected', () => {
  const ctx = loadContext();
  for (const bad of [0, 3, null, '2', NaN]) {
    assert.equal(adapt(ctx, makeDto({ themeApiVersion: bad })), null, String(bad));
  }
});

test('v2 playerControls block must be complete, known and consistent', () => {
  const ctx = loadContext();
  const base = plain(DEFAULT_PLAYER_CONTROLS);
  assert.equal(adapt(ctx, v2Dto(null)), null);
  assert.equal(adapt(ctx, v2Dto({})), null);

  const partial = plain(base);
  delete partial.played;
  partial.unknownColor = '#123456';
  assert.equal(adapt(ctx, v2Dto(partial)), null);

  const clash = plain(base);
  clash.track = clash.played;
  assert.equal(adapt(ctx, v2Dto(clash)), null);

  const legacyBlock = withDefinition(makeDto(), { playerControls: plain(base) });
  assert.equal(adapt(ctx, legacyBlock), null);
});

test('outer metadata is authoritative over definition.themeApiVersion', () => {
  const ctx = loadContext();
  const base = plain(DEFAULT_PLAYER_CONTROLS);

  const nestedOnly = withDefinition(makeDto(), { themeApiVersion: 2, playerControls: plain(base) });
  assert.equal(adapt(ctx, nestedOnly), null);

  const spoofed = withDefinition(makeDto({ themeApiVersion: 1 }), {
    themeApiVersion: 2,
    playerControls: plain(base),
  });
  assert.equal(adapt(ctx, spoofed), null);

  const trusted = withDefinition(makeDto({ themeApiVersion: 2 }), {
    themeApiVersion: 3,
    playerControls: plain(base),
  });
  const res = adapt(ctx, trusted);
  assert.notEqual(res, null);
  assert.equal(res.theme.themeApiVersion, 2);
  assert.equal(res.version, '1.0');
  assert.equal(res.hash, HASH);
  assert.deepEqual(plain(res.theme.playerControls), base);
});
