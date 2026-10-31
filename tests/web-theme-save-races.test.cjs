'use strict';
/* Real in-flight appearance races: a save/GET response that started before a
 * newer local choice must keep that choice while still advancing the CAS
 * revision, and no task started for an older session may touch a new account. */
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const THEME_SRC = fs.readFileSync(path.join(__dirname, '..', 'public', 'web', 'theme-player-controls.js'), 'utf8') + '\n' + fs.readFileSync(path.join(__dirname, '..', 'public', 'web', 'theme.js'), 'utf8');
const APPEARANCE_SRC = fs.readFileSync(path.join(__dirname, '..', 'public', 'web', 'appearance.js'), 'utf8');
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));

function makeStorage(initial = {}) {
  const map = new Map(Object.entries(initial));
  return {
    map,
    getItem(key) { return map.has(key) ? map.get(key) : null; },
    setItem(key, value) { map.set(key, String(value)); },
    removeItem(key) { map.delete(key); },
  };
}

function makeDocument() {
  return {
    documentElement: {
      dataset: {},
      style: {
        props: {},
        setProperty(name, value) { this.props[name] = value; },
      },
    },
  };
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise(function (res, rej) {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function loadRig(options = {}) {
  const storage = options.storage || makeStorage();
  const context = {
    console, setTimeout, clearTimeout, Promise, JSON, Math, Set, Map, Object, Array, String,
    AbortController, DOMException, URL,
    localStorage: storage,
    document: makeDocument(),
    addEventListener() {},
    removeEventListener() {},
  };
  context.window = context;
  vm.createContext(context);
  vm.runInContext(THEME_SRC, context);
  vm.runInContext(APPEARANCE_SRC, context);
  const theme = context.EIOTheme.createController({
    initialUserId: options.initialUserId === undefined ? null : options.initialUserId,
    deps: options.deps || {},
    matchMedia: null,
  });
  const appearance = context.EIOAppearance.createController({ theme });
  return { context, theme, appearance, storage, document: context.document };
}

function raceDeps(overrides = {}) {
  const saves = [];
  const deps = {
    loadPreferences() { return { themeId: null, mode: 'dark', revision: 2 }; },
    savePreferences(payload) {
      const call = deferred();
      saves.push({ payload, call });
      return call.promise;
    },
  };
  for (const key of Object.keys(overrides)) deps[key] = overrides[key];
  return { deps, saves };
}

test('theme save race: old in-flight save keeps the latest intent and queued PUT uses the advanced revision', async () => {
  const { deps, saves } = raceDeps();
  const { theme, appearance, storage } = loadRig({ initialUserId: 'u1', deps });
  await theme.refreshRemote({ timeoutMs: 200 });
  assert.equal(theme.getRemoteStatus(), 'ok');

  appearance.chooseMode('light');
  const first = appearance.save();
  await delay(0);
  assert.equal(saves.length, 1);
  assert.equal(saves[0].payload.mode, 'light');
  assert.equal(saves[0].payload.expectedRevision, 2);

  appearance.chooseMode('dark');
  const second = appearance.save();
  assert.equal(appearance.state.mode, 'dark');

  saves[0].call.resolve({ themeId: null, mode: 'light', revision: 6 });
  await first;
  assert.equal(appearance.state.mode, 'dark', 'the old response must not override the newer choice');
  assert.equal(theme.getPreference().mode, 'dark');
  assert.equal(theme.getPreference().revision, 6, 'an old success must still advance the CAS revision');
  assert.match(appearance.state.notice, /较早的选择/);
  assert.equal(JSON.parse(storage.map.get('eio_theme_user_v1:u1')).mode, 'dark');

  await delay(0);
  assert.equal(saves.length, 2);
  assert.equal(saves[1].payload.mode, 'dark');
  assert.equal(saves[1].payload.expectedRevision, 6);

  saves[1].call.resolve({ themeId: null, mode: 'dark', revision: 7 });
  await second;
  assert.equal(appearance.state.mode, 'dark');
  assert.equal(theme.getPreference().revision, 7);
  assert.equal(appearance.state.notice, '已保存到账号');
  assert.equal(appearance.state.saving, false);
});

test('theme refresh race: a late initial GET keeps a newer local intent but advances the revision', async () => {
  const remote = deferred();
  const deps = { loadPreferences() { return remote.promise; } };
  const { theme, storage } = loadRig({ initialUserId: 'u1', deps });

  const refresh = theme.refreshRemote({ timeoutMs: 200 });
  theme.setPreference({ mode: 'light' });
  assert.equal(theme.getPreference().revision, 0);

  remote.resolve({ themeId: null, mode: 'dark', revision: 4 });
  await refresh;
  assert.equal(theme.getPreference().mode, 'light');
  assert.equal(theme.getResolved().variant, 'light');
  assert.equal(theme.getPreference().revision, 4);
  assert.equal(theme.getRemoteStatus(), 'ok');
  assert.equal(JSON.parse(storage.map.get('eio_theme_user_v1:u1')).mode, 'light');
  assert.equal(JSON.parse(storage.map.get('eio_theme_user_v1:u1')).revision, 4);
});

test('theme save race: an older account 409 cannot notify, refresh or write the new account', async () => {
  let loads = 0;
  const { deps, saves } = raceDeps({
    loadPreferences() {
      loads += 1;
      return { themeId: null, mode: 'dark', revision: 2 };
    },
  });
  const { theme, appearance, storage } = loadRig({ initialUserId: 'u1', deps });
  await theme.refreshRemote({ timeoutMs: 200 });

  appearance.chooseMode('light');
  const saving = appearance.save();
  await delay(0);
  assert.equal(saves.length, 1);
  assert.equal(saves[0].payload.userId, 'u1');

  appearance.enterSession('u2');
  assert.equal(theme.getIdentity(), 'u2');
  const noticeBefore = appearance.state.notice;
  saves[0].call.reject(Object.assign(new Error('conflict'), { status: 409 }));
  await saving;
  await delay(0);

  assert.equal(loads, 1, 'the old 409 must not trigger a refresh for u2');
  assert.equal(saves.length, 1, 'the old save must not issue another request for u2');
  assert.equal(appearance.state.notice, noticeBefore);
  assert.equal(appearance.state.saving, false);
  assert.equal(theme.getPreference().mode, 'dark');
  assert.equal(storage.map.has('eio_theme_user_v1:u2'), false);
});

test('theme save race: A to B to A old success cannot update the re-entered session', async () => {
  const { deps, saves } = raceDeps();
  const { theme, appearance, storage } = loadRig({ initialUserId: 'u1', deps });
  await theme.refreshRemote({ timeoutMs: 200 });

  appearance.chooseMode('light');
  const saving = appearance.save();
  await delay(0);
  assert.equal(saves.length, 1);

  appearance.enterSession('u2');
  appearance.enterSession('u1');
  theme.setPreference({ mode: 'dark' });
  assert.equal(appearance.state.mode, 'dark');

  saves[0].call.resolve({ themeId: null, mode: 'light', revision: 9 });
  await saving;
  await delay(0);

  assert.equal(theme.getPreference().mode, 'dark');
  assert.equal(theme.getPreference().revision, 2, 'a new session epoch must not adopt the old revision');
  assert.equal(appearance.state.mode, 'dark');
  assert.equal(appearance.state.notice, '');
  assert.equal(JSON.parse(storage.map.get('eio_theme_user_v1:u1')).mode, 'dark');
});

test('appearance queue: a save queued before an account switch is not sent for the new account', async () => {
  let loads = 0;
  const { deps, saves } = raceDeps({
    loadPreferences() {
      loads += 1;
      return { themeId: null, mode: 'dark', revision: 2 };
    },
  });
  const { theme, appearance, storage } = loadRig({ initialUserId: 'u1', deps });
  await theme.refreshRemote({ timeoutMs: 200 });

  appearance.chooseMode('light');
  const first = appearance.save();
  await delay(0);
  const second = appearance.save();
  appearance.enterSession('u2');

  saves[0].call.resolve({ themeId: null, mode: 'light', revision: 5 });
  await first;
  await second;
  await delay(0);

  assert.equal(saves.length, 1, 'the queued task must not PUT the old intent for u2');
  assert.equal(loads, 1);
  assert.equal(theme.getPreference().mode, 'dark');
  assert.equal(appearance.state.notice, '');
  assert.equal(storage.map.has('eio_theme_user_v1:u2'), false);
});

test('appearance timer: a pending 220ms save is dropped after an account switch', async () => {
  const { deps, saves } = raceDeps();
  const { theme, appearance } = loadRig({ initialUserId: 'u1', deps });
  await theme.refreshRemote({ timeoutMs: 200 });

  appearance.chooseMode('light');
  appearance.enterSession('u2');
  await delay(260);

  assert.equal(saves.length, 0);
  assert.equal(appearance.state.saving, false);
  assert.equal(theme.getIdentity(), 'u2');
});

test('appearance refresh: a late refresh completion cannot revive the previous session', async () => {
  const site = deferred();
  const deps = {
    loadSite() { return site.promise; },
    loadPreferences() { return { themeId: null, mode: 'light', revision: 9 }; },
  };
  const { theme, appearance } = loadRig({ initialUserId: 'u1', deps });

  const refresh = appearance.refreshRemote({ timeoutMs: 200 });
  appearance.enterSession('u2');
  site.resolve({ themeId: 'builtin', mode: 'dark', revision: 1 });
  await refresh;
  await delay(0);

  assert.equal(theme.getIdentity(), 'u2');
  assert.equal(theme.getPreference().mode, 'dark');
  assert.equal(appearance.state.notice, '');
});

test('theme save race: a smaller opaque revision from an old PUT is adopted exactly and seeds the next PUT', async () => {
  const { deps, saves } = raceDeps({
    loadPreferences() { return { themeId: null, mode: 'dark', revision: 9000 }; },
  });
  const { theme, appearance } = loadRig({ initialUserId: 'u1', deps });
  await theme.refreshRemote({ timeoutMs: 200 });
  assert.equal(theme.getPreference().revision, 9000);

  appearance.chooseMode('light');
  const first = appearance.save();
  await delay(0);
  assert.equal(saves.length, 1);
  assert.equal(saves[0].payload.mode, 'light');
  assert.equal(saves[0].payload.expectedRevision, 9000);

  appearance.chooseMode('dark');
  const second = appearance.save();
  saves[0].call.resolve({ themeId: null, mode: 'light', revision: 7 });
  await first;
  assert.equal(appearance.state.mode, 'dark', 'the older light PUT must not override the newer dark choice');
  assert.equal(theme.getPreference().mode, 'dark');
  assert.equal(theme.getPreference().revision, 7, 'the opaque revision must decrease to the exact server value');
  assert.match(appearance.state.notice, /较早的选择/);

  await delay(0);
  assert.equal(saves.length, 2);
  assert.equal(saves[1].payload.mode, 'dark');
  assert.equal(saves[1].payload.expectedRevision, 7, 'the next CAS PUT must use the exact smaller revision');

  saves[1].call.resolve({ themeId: null, mode: 'dark', revision: 5 });
  await second;
  assert.equal(theme.getPreference().mode, 'dark');
  assert.equal(theme.getPreference().revision, 5, 'a save response also replaces, never maxes, the opaque revision');
  assert.equal(appearance.state.saving, false);
});

test('theme refresh race: a server revision below the cached one is adopted exactly while a local choice wins', async () => {
  const remote = deferred();
  const storage = makeStorage({
    'eio_theme_user_v1:u1': JSON.stringify({
      schema: 1, scope: 'user', userId: 'u1', themeId: null, mode: 'dark', revision: 9000,
    }),
  });
  const { theme } = loadRig({
    initialUserId: 'u1',
    storage,
    deps: { loadPreferences() { return remote.promise; } },
  });
  assert.equal(theme.getPreference().revision, 9000);

  const refresh = theme.refreshRemote({ timeoutMs: 200 });
  theme.setPreference({ mode: 'light' });
  remote.resolve({ themeId: null, mode: 'dark', revision: 3 });
  await refresh;

  assert.equal(theme.getPreference().mode, 'light', 'the local change during the GET must survive');
  assert.equal(theme.getResolved().variant, 'light');
  assert.equal(theme.getPreference().revision, 3, 'the cached 9000 must be replaced by the exact GET revision 3');
  assert.equal(theme.getRemoteStatus(), 'ok');
  assert.equal(JSON.parse(storage.map.get('eio_theme_user_v1:u1')).mode, 'light');
  assert.equal(JSON.parse(storage.map.get('eio_theme_user_v1:u1')).revision, 3);
});
