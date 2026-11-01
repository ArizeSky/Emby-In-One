'use strict';
/* T1 appearance engine: builtin variants, system mode, cache validation,
 * storage failure/recovery, account isolation, generation/abort and the T4
 * dependency-injection seam. No network, player or subtitle engine is used. */
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const THEME_SRC = fs.readFileSync(path.join(__dirname, '..', 'public', 'web', 'theme-player-controls.js'), 'utf8') + '\n' + fs.readFileSync(path.join(__dirname, '..', 'public', 'web', 'theme.js'), 'utf8');
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));

function makeStorage(initial = {}, flags = {}) {
  const map = new Map(Object.entries(initial));
  const removed = [];
  return {
    flags,
    map,
    removed,
    getItem(key) {
      if (this.flags.failReads) throw new Error('denied read');
      return map.has(key) ? map.get(key) : null;
    },
    setItem(key, value) {
      if (this.flags.failWrites) throw new Error('denied write');
      map.set(key, String(value));
    },
    removeItem(key) {
      if (this.flags.failReads) throw new Error('denied remove');
      removed.push(key);
      map.delete(key);
    },
  };
}

function makeDocument() {
  return {
    documentElement: {
      dataset: {},
      style: {
        props: {},
        setProperty(name, value) { this.props[name] = value; },
        removeProperty(name) { delete this.props[name]; },
      },
    },
  };
}

function makeMedia(matches) {
  const listeners = [];
  return {
    matches,
    listeners,
    addEventListener(type, listener) { if (type === 'change') listeners.push(listener); },
    removeEventListener(type, listener) {
      if (type !== 'change') return;
      const index = listeners.indexOf(listener);
      if (index >= 0) listeners.splice(index, 1);
    },
    emit(next) {
      this.matches = next;
      for (const listener of listeners.slice()) listener({ matches: next });
    },
  };
}

function loadTheme(options = {}) {
  const storage = options.storage || makeStorage();
  const document = options.document || makeDocument();
  const media = options.matchMedia === undefined ? null : options.matchMedia;
  const context = {
    console, setTimeout, clearTimeout, setInterval, clearInterval,
    Promise, JSON, Math, Set, Map, AbortController, DOMException, URL,
    localStorage: storage,
    document,
    addEventListener(type, fn) { (this._listeners[type] ||= []).push(fn); },
    removeEventListener(type, fn) {
      const list = this._listeners[type] || [];
      const index = list.indexOf(fn);
      if (index >= 0) list.splice(index, 1);
    },
    _listeners: {},
    matchMedia: media ? () => media : undefined,
  };
  context.window = context;
  if (options.denyLocalStorageGetter) {
    Object.defineProperty(context, 'localStorage', {
      configurable: true,
      get() { throw new Error('SecurityError: localStorage is denied'); },
    });
  }
  vm.createContext(context);
  vm.runInContext(THEME_SRC, context);
  return { theme: context.EIOTheme, context, storage, document, media };
}

function preference(scope, userId, themeId, mode) {
  return JSON.stringify({
    schema: 1, scope, userId: scope === 'user' ? userId : null,
    themeId, mode, revision: 0, cssHref: null, themeRevision: null,
  });
}

function variant(overrides = {}) {
  return Object.assign({
    bg0: '#101010', bg1: '#181818', bg2: '#202020', bg3: '#282828',
    text1: '#ffffff', text2: '#cbd5e1', text3: '#94a3b8',
    line: '#94a3b8', line2: '#94a3b8',
    accent: '#4f46e5', accentText: '#818cf8',
  }, overrides);
}

function customTheme(id = 'ocean-1', overrides = {}) {
  return Object.assign({
    id, name: 'Ocean', author: 'Test', description: 'test theme',
    variants: { light: variant(), dark: variant() },
    appearance: { radius: 14, density: 'comfortable' },
  }, overrides);
}

test('theme: builtin dark is the default and writes html data/tokens only', () => {
  const { theme, document } = loadTheme();
  const controller = theme.createController({ initialUserId: null });
  const resolved = controller.getResolved();
  assert.equal(resolved.themeId, 'builtin');
  assert.equal(resolved.mode, 'dark');
  assert.equal(resolved.variant, 'dark');
  assert.equal(controller.getPreference().themeId, null);
  assert.equal(document.documentElement.dataset.eioTheme, 'dark');
  assert.equal(document.documentElement.dataset.eioThemeId, 'builtin');
  assert.equal(document.documentElement.dataset.eioMode, 'dark');
  assert.equal(document.documentElement.style.props['--bg-0'], '#070a12');
  assert.equal(document.documentElement.style.props['--ac-on'], '#ffffff');
  controller.destroy();
});

test('theme: light/dark choice persists and survives a new controller', () => {
  const storage = makeStorage();
  const first = loadTheme({ storage });
  const controller = first.theme.createController({ initialUserId: null });
  controller.setPreference({ mode: 'light' });
  assert.equal(controller.getResolved().variant, 'light');
  assert.equal(first.document.documentElement.dataset.eioTheme, 'light');
  assert.equal(first.document.documentElement.style.props['--bg-0'], '#f5f7fb');
  assert.equal(JSON.parse(storage.map.get('eio_theme_anon_v1')).mode, 'light');
  controller.destroy();

  const second = loadTheme({ storage });
  const restored = second.theme.createController({ initialUserId: null });
  assert.equal(restored.getPreference().mode, 'light');
  assert.equal(restored.getResolved().variant, 'light');
  restored.destroy();
});

test('theme: system follows matchMedia changes only while mode=system and cleans up', () => {
  const media = makeMedia(false);
  const { theme } = loadTheme({ matchMedia: media });
  const controller = theme.createController({ initialUserId: null, matchMedia: media });
  controller.setPreference({ mode: 'system' });
  assert.equal(controller.getResolved().variant, 'light');
  media.emit(true);
  assert.equal(controller.getResolved().variant, 'dark');
  media.emit(false);
  assert.equal(controller.getResolved().variant, 'light');
  controller.setPreference({ mode: 'dark' });
  media.emit(false);
  assert.equal(controller.getResolved().variant, 'dark');
  media.emit(true);
  assert.equal(controller.getResolved().variant, 'dark');
  assert.equal(media.listeners.length, 1);
  controller.destroy();
  assert.equal(media.listeners.length, 0);
  media.emit(false);
  assert.equal(controller.getResolved().variant, 'dark');
});

test('theme: denied storage still switches in-memory and recovers after writes work', () => {
  const storage = makeStorage({}, { failWrites: true });
  const { theme, document } = loadTheme({ storage });
  const controller = theme.createController({ initialUserId: null });
  controller.setPreference({ mode: 'light' });
  assert.equal(controller.getResolved().variant, 'light');
  assert.equal(document.documentElement.dataset.eioTheme, 'light');
  assert.match(controller.getStorageError(), /无法保存/);
  assert.match(controller.getResolved().notice, /无法保存/);

  storage.flags.failWrites = false;
  controller.setPreference({ mode: 'dark' });
  assert.equal(controller.getStorageError(), '');
  assert.doesNotMatch(controller.getResolved().notice, /无法保存/);
  assert.equal(JSON.parse(storage.map.get('eio_theme_anon_v1')).mode, 'dark');
  controller.destroy();
});

test('theme: a throwing localStorage getter still builds an in-memory controller', () => {
  const { theme, document } = loadTheme({ denyLocalStorageGetter: true });
  const controller = theme.createController({ initialUserId: null });
  assert.equal(controller.getResolved().themeId, 'builtin');
  assert.equal(controller.getResolved().variant, 'dark');

  controller.setPreference({ mode: 'light' });
  assert.equal(controller.getResolved().variant, 'light');
  assert.equal(document.documentElement.dataset.eioTheme, 'light');
  assert.equal(document.documentElement.style.props['--bg-0'], '#f5f7fb');
  assert.match(controller.getStorageError(), /无法保存/);

  controller.setPreference({ mode: 'dark' });
  assert.equal(controller.getResolved().variant, 'dark');
  assert.equal(document.documentElement.dataset.eioTheme, 'dark');
  controller.destroy();
  assert.equal(controller.isDestroyed(), true);
});

test('theme: explicit storage:null disables caching without re-reading window.localStorage', () => {
  const storage = makeStorage({
    eio_theme_anon_v1: preference('anon', null, null, 'light'),
  });
  const { theme } = loadTheme({ storage });
  const controller = theme.createController({ initialUserId: null, storage: null });
  // The cached light choice was not read and the shared store was not written.
  assert.equal(controller.getPreference().mode, 'dark');
  assert.equal(controller.getResolved().variant, 'dark');
  controller.setPreference({ mode: 'dark' });
  assert.match(controller.getStorageError(), /无法保存/);
  assert.equal(JSON.parse(storage.map.get('eio_theme_anon_v1')).mode, 'light');
  controller.destroy();
});

test('theme: malformed cache is removed and falls back to builtin dark', () => {
  const storage = makeStorage({
    eio_theme_anon_v1: JSON.stringify({ schema: 1, scope: 'anon', userId: null, themeId: 'Bad ID', mode: 'dark' }),
  });
  const { theme } = loadTheme({ storage });
  const controller = theme.createController({ initialUserId: null });
  assert.equal(controller.getResolved().themeId, 'builtin');
  assert.equal(controller.getResolved().variant, 'dark');
  assert.match(controller.getResolved().notice, /缓存无效/);
  assert.ok(storage.removed.includes('eio_theme_anon_v1'));
  assert.equal(storage.map.has('eio_theme_anon_v1'), false);
  controller.destroy();
});

test('theme: anonymous and per-user caches never leak across accounts', () => {
  const storage = makeStorage({
    eio_token: 'token', eio_user_id: 'u1',
    eio_theme_anon_v1: preference('anon', null, null, 'dark'),
    'eio_theme_user_v1:u1': preference('user', 'u1', 'builtin', 'light'),
  });
  const { theme } = loadTheme({ storage });
  const controller = theme.createController({ initialUserId: 'u1' });
  assert.equal(controller.getIdentity(), 'u1');
  assert.equal(controller.getPreference().mode, 'light');

  controller.enterSession('u2');
  assert.equal(controller.getIdentity(), 'u2');
  assert.equal(controller.getPreference().mode, 'dark');
  assert.equal(controller.getPreference().themeId, null);
  controller.setPreference({ mode: 'system' });
  assert.equal(JSON.parse(storage.map.get('eio_theme_user_v1:u2')).mode, 'system');

  controller.enterSession('u1');
  assert.equal(controller.getPreference().mode, 'light');
  controller.enterSession(null);
  assert.equal(controller.getIdentity(), null);
  assert.equal(controller.getPreference().mode, 'dark');
  assert.equal(controller.getPreference().userId, null);
  controller.destroy();
});

test('theme: a late remote preference cannot overwrite a newer account', async () => {
  let resolvePreference;
  const deps = {
    loadPreferences() { return new Promise(resolve => { resolvePreference = resolve; }); },
  };
  const { theme } = loadTheme();
  const controller = theme.createController({ initialUserId: 'u1', deps });
  const pending = controller.refreshRemote({ timeoutMs: 200 });
  controller.enterSession('u2');
  resolvePreference({ themeId: 'builtin', mode: 'light', revision: 9 });
  await pending;
  await delay(5);
  assert.equal(controller.getIdentity(), 'u2');
  assert.equal(controller.getPreference().mode, 'dark');
  assert.equal(controller.getResolved().variant, 'dark');
  controller.destroy();
});

test('theme: T4 injected site/catalog/prefs resolve custom theme and save CAS revision', async () => {
  const media = makeMedia(true);
  const saved = [];
  const deps = {
    loadSite() { return { themeId: 'builtin', mode: 'dark', revision: 2 }; },
    loadCatalog() { return [customTheme('ocean-1')]; },
    loadPreferences() { return { themeId: 'ocean-1', mode: 'light', revision: 3 }; },
    savePreferences(payload) {
      saved.push(payload);
      return { themeId: 'ocean-1', mode: 'system', revision: 4 };
    },
  };
  const { theme } = loadTheme({ matchMedia: media });
  const controller = theme.createController({ initialUserId: 'u1', deps, matchMedia: media });
  await controller.refreshRemote({ timeoutMs: 200 });
  assert.equal(controller.getResolved().themeId, 'ocean-1');
  assert.equal(controller.getResolved().variant, 'light');
  assert.equal(controller.getPreference().revision, 3);
  assert.equal(controller.getRemoteStatus(), 'ok');

  const result = await controller.saveRemote({ timeoutMs: 200 });
  assert.equal(result.saved, true);
  assert.equal(saved.length, 1);
  assert.equal(saved[0].expectedRevision, 3);
  assert.equal(saved[0].userId, 'u1');
  assert.equal(controller.getPreference().mode, 'system');
  assert.equal(controller.getResolved().variant, 'dark');
  controller.destroy();
});

test('theme: invalid mode/theme tokens fall back visibly to builtin', () => {
  const { theme } = loadTheme();
  const controller = theme.createController({ initialUserId: null });
  controller.setPreference({ themeId: 'not a theme' });
  assert.equal(controller.getResolved().themeId, 'builtin');
  assert.match(controller.getResolved().notice, /无效主题/);
  controller.setPreference({ themeId: 'missing-theme' });
  assert.equal(controller.getResolved().themeId, 'builtin');
  assert.match(controller.getResolved().notice, /主题不可用/);
  controller.setPreference({ mode: 'auto' });
  assert.equal(controller.getPreference().mode, 'dark');
  assert.match(controller.getResolved().notice, /无效明暗/);
  controller.destroy();
});

test('theme: accentText stays link/emphasis text; --ac-on is derived WCAG-safe', () => {
  const { theme } = loadTheme();
  assert.equal(theme.readableOn('#4f46e5'), '#ffffff');
  assert.equal(theme.readableOn('#facc15'), '#000000');
  assert.equal(theme.readableOn('#111827'), '#ffffff');
  assert.ok(theme.contrastRatio('#4f46e5', '#ffffff') >= 4.5);

  const darkVars = theme.variantToCssVars(theme.BUILTIN.variants.dark, 'dark', { radius: 14 });
  assert.equal(darkVars['--ac-text'], '#818cf8');
  assert.equal(darkVars['--ac-on'], '#ffffff');
  assert.equal(darkVars['--ac-grad'], 'linear-gradient(135deg, #4f46e5, #4f46e5)');
  assert.doesNotMatch(darkVars['--ac-grad'], /818cf8/);

  const lightVars = theme.variantToCssVars(theme.BUILTIN.variants.light, 'light', { radius: 14 });
  assert.equal(lightVars['--ac-text'], '#4338ca');
  assert.equal(lightVars['--ac-on'], '#ffffff');

  const lightAccent = theme.variantToCssVars(variant({
    accent: '#facc15', accentText: '#1f2937',
  }), 'light', { radius: 14 });
  assert.equal(lightAccent['--ac-text'], '#1f2937');
  assert.equal(lightAccent['--ac-on'], '#000000');
  assert.equal(theme.normalizeTheme(customTheme('ocean-2', {
    cssHref: 'https://evil.example/theme.css',
  })), null);
});
