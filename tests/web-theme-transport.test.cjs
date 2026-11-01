'use strict';
/* T4 /web theme transport: strict site/catalog/preference DTOs, minimal
 * anonymous catalog, CAS saves, immutable stylesheet lifecycle and stale
 * 401/late-response isolation. No network, player or backend is used. */
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const THEME_SRC = fs.readFileSync(path.join(__dirname, '..', 'public', 'web', 'theme-player-controls.js'), 'utf8') + '\n' + fs.readFileSync(path.join(__dirname, '..', 'public', 'web', 'theme.js'), 'utf8');
const TRANSPORT_SRC = fs.readFileSync(path.join(__dirname, '..', 'public', 'web', 'theme-transport.js'), 'utf8');

const HASH = 'a'.repeat(64);
const OTHER_HASH = 'b'.repeat(64);
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
  const children = [];
  const head = {
    children,
    appendChild(element) {
      element.parentNode = head;
      children.push(element);
      return element;
    },
    removeChild(element) {
      const index = children.indexOf(element);
      if (index >= 0) children.splice(index, 1);
      element.parentNode = null;
      return element;
    },
  };
  const document = {
    head,
    documentElement: {
      dataset: {},
      style: {
        props: {},
        setProperty(name, value) { this.props[name] = value; },
        removeProperty(name) { delete this.props[name]; },
      },
    },
    createElement(tagName) {
      const listeners = {};
      return {
        tagName,
        attrs: {},
        parentNode: null,
        listeners,
        setAttribute(name, value) { this.attrs[name] = String(value); },
        addEventListener(type, listener) { listeners[type] = listener; },
        remove() { if (this.parentNode) this.parentNode.removeChild(this); },
      };
    },
  };
  return { document, head, children };
}

function makeRuntime(options = {}) {
  const storage = options.storage || makeStorage();
  const doc = options.document || makeDocument().document;
  const calls = [];
  let currentUserId = options.userId !== undefined
    ? options.userId
    : storage.getItem('eio_user_id');
  let currentToken = options.token !== undefined
    ? options.token
    : storage.getItem('eio_token');
  const unauthorized = [];
  const handler = options.handler || (() => { throw new Error('unexpected request'); });
  const request = (requestPath, requestOptions) => {
    calls.push({ path: requestPath, opts: requestOptions || {} });
    return Promise.resolve().then(() => handler(requestPath, requestOptions || {}, calls));
  };
  const context = {
    console, setTimeout, clearTimeout, Promise, JSON, Math, Set, Map,
    Object, Array, String, Number, URL,
    localStorage: storage,
    document: doc,
  };
  context.window = context;
  vm.createContext(context);
  vm.runInContext(THEME_SRC, context);
  vm.runInContext(TRANSPORT_SRC, context);
  const transport = context.EIOThemeTransport.create({
    request,
    getToken: () => currentToken,
    getUserId: () => currentUserId,
    document: doc,
    window: context,
    onUnauthorized(info) { unauthorized.push(info); },
  });
  return {
    context,
    theme: context.EIOTheme,
    transport,
    storage,
    doc,
    calls,
    unauthorized,
    setIdentity(id, token) { currentUserId = id; currentToken = token; },
    identity() { return { userId: currentUserId, token: currentToken }; },
  };
}

function variant(seed) {
  return {
    bg0: '#010203', bg1: '#040506', bg2: '#070809', bg3: '#0a0b0c',
    text1: '#f1f2f3', text2: '#d1d2d3', text3: '#b1b2b3',
    line: '#111213', line2: '#141516', accent: seed, accentText: '#718191',
  };
}

function publicTheme(overrides = {}) {
  const base = {
    id: 'ocean-1',
    name: 'Ocean',
    author: 'tester',
    description: 'blue theme',
    version: '1.0.0',
    hash: HASH,
    variants: ['light', 'dark'],
    builtin: false,
    cssUrl: '/web/themes/' + HASH + '/theme.css',
    backgroundUrl: '/web/themes/' + HASH + '/assets/bg.jpg',
    previewUrl: '/web/themes/' + HASH + '/preview.jpg',
    previewLightUrl: '/web/themes/' + HASH + '/preview.html?mode=light',
    previewDarkUrl: '/web/themes/' + HASH + '/preview.html?mode=dark',
    definition: {
      variants: {
        light: variant('#123456'),
        dark: variant('#654321'),
      },
      appearance: { radius: 16, density: 'compact' },
      backgroundImage: 'assets/bg.jpg',
    },
  };
  return Object.assign(base, overrides);
}

function clone(value) {
  return JSON.parse(JSON.stringify(value));
}

// Values handed back by the VM realm have the VM's Object/Array prototypes,
// so node:assert's strict deep equality rejects identical data. Normalize the
// container (not the assertion) through the host realm.
function hostJSON(value) {
  return JSON.parse(JSON.stringify(value));
}

function hostIds(items) {
  return Array.from(items, item => item.id);
}

function preference(scope, userId, themeId, mode, revision) {
  return JSON.stringify({
    schema: 1,
    scope,
    userId,
    themeId,
    mode,
    revision,
    cssHref: null,
    themeRevision: null,
  });
}

function controllerFor(runtime, options = {}) {
  const controller = runtime.theme.createController({
    initialUserId: options.initialUserId === undefined ? null : options.initialUserId,
    storage: options.storage || runtime.storage,
    deps: runtime.transport,
    matchMedia: null,
    document: runtime.doc,
  });
  runtime.transport.attach(controller, options.attach);
  return controller;
}

test('transport: anonymous custom site default becomes the minimal catalog', async () => {
  const siteTheme = publicTheme();
  const runtime = makeRuntime({
    handler(requestPath) {
      if (requestPath === '/web/api/theme-site') {
        return { available: true, themeId: 'ocean-1', revision: 5, theme: siteTheme };
      }
      throw new Error('authenticated catalog must not be called anonymously');
    },
  });
  const controller = controllerFor(runtime);

  await controller.refreshRemote({ timeoutMs: 400 });

  assert.equal(runtime.calls.length, 1);
  assert.equal(runtime.calls[0].path, '/web/api/theme-site');
  assert.equal(runtime.calls[0].opts.anonymous, true);
  assert.equal(runtime.calls[0].opts.method, 'GET');
  assert.ok(!runtime.calls[0].path.includes('?'));

  assert.equal(controller.getResolved().themeId, 'ocean-1');
  assert.equal(controller.getResolved().variant, 'dark');
  assert.equal(controller.getResolved().tokens.bg0, '#010203');
  assert.equal(controller.getPreference().themeId, null, 'null still means follow site');
  assert.deepEqual(controller.getResolved().notice, '');

  const root = runtime.doc.documentElement;
  assert.equal(root.dataset.eioThemeId, 'ocean-1');
  assert.equal(root.style.props['--bg-0'], '#010203');
  assert.equal(root.style.props['--r-card'], '16px');

  const options = hostIds(controller.getThemeOptions());
  assert.deepEqual(options, [null, 'builtin', 'ocean-1']);
  assert.deepEqual(hostJSON(runtime.transport.getSiteStatus()), {
    available: true, usable: true, reason: '', themeId: 'ocean-1', revision: 5, custom: true,
  });

  const link = runtime.doc.head.children[0];
  assert.ok(link, 'the immutable generated stylesheet is attached');
  assert.equal(link.attrs.rel, 'stylesheet');
  assert.equal(link.attrs.href, '/web/themes/' + HASH + '/theme.css');
  controller.destroy();
});

test('transport: builtin site default is usable and keeps the local mode', async () => {
  const runtime = makeRuntime({
    storage: makeStorage({
      eio_theme_anon_v1: preference('anon', null, null, 'light', 0),
    }),
    handler(requestPath) {
      if (requestPath === '/web/api/theme-site') {
        return { available: true, themeId: 'builtin', revision: 4, theme: null };
      }
      throw new Error('anonymous catalog must not be requested');
    },
  });
  const controller = controllerFor(runtime);
  await controller.refreshRemote({ timeoutMs: 400 });

  assert.equal(runtime.calls.length, 1);
  assert.equal(controller.getResolved().themeId, 'builtin');
  assert.equal(controller.getResolved().variant, 'light');
  assert.equal(controller.getRemoteStatus(), 'ok');
  assert.equal(runtime.doc.head.children.length, 0, 'builtin needs no remote stylesheet');
  assert.deepEqual(hostIds(controller.getThemeOptions()), [null, 'builtin']);
  assert.equal(runtime.transport.getSiteStatus().custom, false);
  controller.destroy();
});

test('transport: unavailable site or revision 0 is a real fallback, not a fake success', async () => {
  for (const site of [
    { available: false, themeId: 'builtin', revision: 9, theme: null },
    { available: true, themeId: 'ocean-1', revision: 0, theme: publicTheme() },
  ]) {
    const runtime = makeRuntime({
      storage: makeStorage({
        eio_theme_anon_v1: preference('anon', null, null, 'light', 0),
      }),
      handler(requestPath) {
        if (requestPath === '/web/api/theme-site') return site;
        throw new Error('anonymous catalog must not be requested');
      },
    });
    const controller = controllerFor(runtime);
    await controller.refreshRemote({ timeoutMs: 400 });

    assert.equal(controller.getResolved().themeId, 'builtin');
    assert.equal(controller.getResolved().variant, 'light');
    assert.match(controller.getResolved().notice, /站点外观设置无效/);
    assert.equal(controller.getRemoteStatus(), 'fallback');
    const status = runtime.transport.getSiteStatus();
    assert.equal(status.usable, false);
    assert.equal(status.available, site.available === true);
    assert.equal(status.revision, 0);
    assert.equal(runtime.doc.head.children.length, 0);
    controller.destroy();
  }
});

test('transport: authenticated catalog drops unsafe or inconsistent themes', async () => {
  const valid = publicTheme();
  const badHash = publicTheme({ id: 'bad-hash', hash: 'abc', cssUrl: '/web/themes/abc/theme.css' });
  const badQuery = publicTheme({ id: 'bad-query', cssUrl: '/web/themes/' + HASH + '/theme.css?api_key=secret' });
  const absolute = publicTheme({ id: 'absolute-css', cssUrl: 'https://evil.example/theme.css' });
  const mismatchBackground = publicTheme({
    id: 'wrong-background',
    backgroundUrl: '/web/themes/' + OTHER_HASH + '/assets/bg.jpg',
  });
  const badToken = publicTheme({ id: 'bad-token' });
  badToken.definition.variants.dark.accent = '#12345';
  const badVariants = publicTheme({ id: 'bad-variants', variants: ['light'] });
  const missingDefinition = publicTheme({ id: 'missing-definition', definition: null });
  const duplicate = publicTheme();
  const builtin = {
    id: 'builtin', name: '内置主题', author: '', description: '', version: '', hash: '',
    variants: ['light', 'dark'], builtin: true, cssUrl: '', backgroundUrl: '',
    previewUrl: '', previewLightUrl: '', previewDarkUrl: '', definition: null,
  };
  const runtime = makeRuntime({
    storage: makeStorage({
      eio_token: 'token-1', eio_user_id: 'u1',
      'eio_theme_user_v1:u1': preference('user', 'u1', 'ocean-1', 'dark', 6),
    }),
    handler(requestPath) {
      if (requestPath === '/web/api/theme-site') {
        return { available: true, themeId: 'builtin', revision: 2, theme: null };
      }
      if (requestPath === '/web/api/themes') {
        return {
          themes: [
            builtin, valid, badHash, badQuery, absolute, mismatchBackground,
            badToken, badVariants, missingDefinition, duplicate,
          ],
        };
      }
      if (requestPath === '/web/api/appearance') {
        return { themeId: 'ocean-1', mode: 'light', revision: 6 };
      }
      throw new Error('unexpected request ' + requestPath);
    },
  });
  const controller = controllerFor(runtime, { initialUserId: 'u1' });
  await controller.refreshRemote({ timeoutMs: 500 });

  const options = hostIds(controller.getThemeOptions());
  assert.deepEqual(options, [null, 'builtin', 'ocean-1']);
  assert.equal(controller.getResolved().themeId, 'ocean-1');
  assert.equal(controller.getResolved().variant, 'light');
  assert.equal(controller.getRemoteStatus(), 'ok');
  assert.deepEqual(runtime.calls.map(call => call.path), [
    '/web/api/theme-site', '/web/api/themes', '/web/api/appearance',
  ]);
  assert.ok(runtime.calls.every(call => !call.path.includes('?')));
  assert.equal(runtime.calls[0].opts.anonymous, true);
  assert.notEqual(runtime.calls[1].opts.anonymous, true);
  controller.destroy();
});

test('transport: preference responses need a positive revision and valid mode/theme', async () => {
  function runtimeWith(pref) {
    return makeRuntime({
      storage: makeStorage({ eio_token: 't1', eio_user_id: 'u1' }),
      handler(requestPath) {
        if (requestPath === '/web/api/appearance') return pref;
        throw new Error('unexpected request');
      },
    });
  }

  const good = runtimeWith({ themeId: null, mode: 'system', revision: 4 });
  assert.deepEqual(hostJSON(await good.transport.loadPreferences({ userId: 'u1' })),
    { themeId: null, mode: 'system', revision: 4 });

  for (const bad of [
    { themeId: null, mode: 'system', revision: 0 },
    { themeId: null, mode: 'system', revision: -1 },
    { themeId: null, mode: 'auto', revision: 4 },
    { themeId: '../evil', mode: 'dark', revision: 4 },
    { themeId: 'builtin', mode: 'dark', revision: '4' },
  ]) {
    const runtime = runtimeWith(bad);
    assert.equal(await runtime.transport.loadPreferences({ userId: 'u1' }), null);
  }
});

test('transport: save sends the exact CAS body and only resolves after completion', async () => {
  let resolveSave;
  const pending = new Promise(resolve => { resolveSave = resolve; });
  const runtime = makeRuntime({
    storage: makeStorage({
      eio_token: 't1', eio_user_id: 'u1',
      'eio_theme_user_v1:u1': preference('user', 'u1', 'builtin', 'dark', 3),
    }),
    handler(requestPath, requestOptions) {
      if (requestPath === '/web/api/appearance' && requestOptions.method === 'PUT') return pending;
      throw new Error('unexpected request');
    },
  });
  const controller = controllerFor(runtime, { initialUserId: 'u1' });

  let settled = false;
  const saving = controller.saveRemote({ timeoutMs: 500 }).then(result => {
    settled = true;
    return result;
  });
  await delay(5);
  assert.equal(settled, false, 'a save must not be reported before the response');
  assert.equal(runtime.calls.length, 1);
  const call = runtime.calls[0];
  assert.equal(call.path, '/web/api/appearance');
  assert.equal(call.opts.method, 'PUT');
  assert.ok(!call.path.includes('?'));
  const body = JSON.parse(call.opts.body);
  assert.deepEqual(Object.keys(body).sort(), ['expectedRevision', 'mode', 'themeId']);
  assert.equal(body.themeId, 'builtin');
  assert.equal(body.mode, 'dark');
  assert.equal(body.expectedRevision, 3);

  resolveSave({ themeId: 'builtin', mode: 'dark', revision: 4 });
  const result = await saving;
  assert.equal(result.saved, true);
  assert.equal(controller.getPreference().revision, 4);
  controller.destroy();
});

test('transport: 409 is surfaced once, never silently saved or retried', async () => {
  const runtime = makeRuntime({
    storage: makeStorage({
      eio_token: 't1', eio_user_id: 'u1',
      'eio_theme_user_v1:u1': preference('user', 'u1', 'builtin', 'light', 5),
    }),
    handler(requestPath, requestOptions) {
      if (requestPath === '/web/api/appearance' && requestOptions.method === 'PUT') {
        const error = new Error('409: 主题状态已变化，请刷新后重试');
        error.status = 409;
        throw error;
      }
      throw new Error('unexpected request');
    },
  });
  const controller = controllerFor(runtime, { initialUserId: 'u1' });
  const result = await controller.saveRemote({ timeoutMs: 500 });

  assert.equal(result.saved, false);
  assert.equal(result.error.status, 409);
  assert.equal(controller.getPreference().revision, 5);
  assert.equal(controller.getPreference().mode, 'light');
  assert.match(controller.getNotice(), /保存外观设置失败/);
  assert.equal(runtime.calls.length, 1);
  await delay(30);
  assert.equal(runtime.calls.length, 1, 'a conflict must not be retried automatically');
  controller.destroy();
});

test('transport: a zero revision is seeded with GET before the CAS PUT', async () => {
  const runtime = makeRuntime({
    storage: makeStorage({
      eio_token: 't1', eio_user_id: 'u1',
      'eio_theme_user_v1:u1': preference('user', 'u1', null, 'light', 0),
    }),
    handler(requestPath, requestOptions) {
      if (requestPath !== '/web/api/appearance') throw new Error('unexpected request');
      if (requestOptions.method === 'GET') return { themeId: null, mode: 'dark', revision: 7 };
      const body = JSON.parse(requestOptions.body);
      assert.equal(body.expectedRevision, 7);
      assert.equal(body.mode, 'light');
      assert.equal(body.themeId, null);
      return { themeId: null, mode: 'light', revision: 8 };
    },
  });
  const controller = controllerFor(runtime, { initialUserId: 'u1' });
  const result = await controller.saveRemote({ timeoutMs: 500 });

  assert.equal(result.saved, true);
  assert.deepEqual(runtime.calls.map(call => call.opts.method), ['GET', 'PUT']);
  assert.equal(controller.getPreference().revision, 8);
  controller.destroy();
});

test('transport: an anonymous save is local-only and never touches the network', async () => {
  const runtime = makeRuntime();
  const controller = controllerFor(runtime);
  const result = await controller.saveRemote({ timeoutMs: 200 });
  assert.deepEqual(hostJSON(result), { saved: false, local: true });
  assert.equal(runtime.calls.length, 0);
  controller.destroy();
});

test('transport: stale 401 from an old account cannot log out the new account', async () => {
  let rejectSave;
  const pending = new Promise((_, reject) => { rejectSave = reject; });
  const runtime = makeRuntime({
    storage: makeStorage({ eio_token: 't1', eio_user_id: 'u1' }),
    handler(requestPath, requestOptions) {
      if (requestPath === '/web/api/appearance' && requestOptions.method === 'PUT') return pending;
      if (requestPath === '/web/api/appearance' && requestOptions.method === 'GET') {
        const error = new Error('401: expired');
        error.status = 401;
        throw error;
      }
      throw new Error('unexpected request');
    },
  });

  const saving = runtime.transport.savePreferences({
    userId: 'u1', themeId: null, mode: 'dark', expectedRevision: 3,
  });
  // Attach a rejection listener before any await: the in-flight request must
  // be observed, and an early failure must never become an unhandledRejection.
  const settled = saving.then(
    value => ({ status: 'fulfilled', value }),
    error => ({ status: 'rejected', error }),
  );
  await delay(5);
  assert.equal(runtime.calls.length, 1, 'the old account request was actually sent');
  assert.equal(runtime.calls[0].opts.method, 'PUT');

  runtime.setIdentity('u2', 't2');
  const stale = new Error('401: old account');
  stale.status = 401;
  rejectSave(stale);
  const outcome = await settled;
  assert.equal(outcome.status, 'rejected');
  assert.equal(outcome.error.status, 401);
  assert.equal(runtime.identity().userId, 'u2');
  assert.equal(runtime.identity().token, 't2');
  assert.equal(runtime.unauthorized.length, 0, 'the new account must stay logged in');

  runtime.setIdentity('u2', 't2');
  await assert.rejects(runtime.transport.loadPreferences({ userId: 'u2' }), error => error.status === 401);
  assert.equal(runtime.unauthorized.length, 1);
  assert.equal(runtime.unauthorized[0].identity, 'u2');
  await assert.rejects(runtime.transport.loadPreferences({ userId: 'u2' }), error => error.status === 401);
  assert.equal(runtime.unauthorized.length, 1, 'a 401 storm is reported once per token');
});

test('transport: an old-account save cancelled before dispatch never reaches the network', async () => {
  const runtime = makeRuntime({
    storage: makeStorage({ eio_token: 't1', eio_user_id: 'u1' }),
    handler() { throw new Error('a cancelled save must not send a request'); },
  });
  const saving = runtime.transport.savePreferences({
    userId: 'u1', themeId: null, mode: 'dark', expectedRevision: 3,
  });
  const settled = saving.then(
    value => ({ status: 'fulfilled', value }),
    error => ({ status: 'rejected', error }),
  );
  runtime.setIdentity('u2', 't2');
  const outcome = await settled;
  assert.equal(outcome.status, 'rejected');
  assert.equal(outcome.error.name, 'AbortError');
  assert.equal(runtime.calls.length, 0, 'no request was issued for the old account');
  assert.equal(runtime.unauthorized.length, 0);
  assert.equal(runtime.identity().userId, 'u2');
  assert.equal(runtime.identity().token, 't2');
});

test('transport: a late site response cannot overwrite a new session', async () => {
  let resolveSite;
  const pending = new Promise(resolve => { resolveSite = resolve; });
  const runtime = makeRuntime({
    storage: makeStorage({ eio_token: 't1', eio_user_id: 'u1' }),
    handler(requestPath) {
      if (requestPath === '/web/api/theme-site') return pending;
      throw new Error('unexpected request');
    },
  });
  const controller = controllerFor(runtime, { initialUserId: 'u1' });
  const refreshing = controller.refreshRemote({ timeoutMs: 400 });
  await delay(5);
  controller.enterSession('u2');
  resolveSite({ available: true, themeId: 'ocean-1', revision: 5, theme: publicTheme() });
  await refreshing;

  assert.equal(controller.getIdentity(), 'u2');
  assert.equal(controller.getResolved().themeId, 'builtin');
  assert.equal(controller.getResolved().variant, 'dark');
  assert.equal(runtime.identity().userId, 'u1');
  assert.equal(runtime.identity().token, 't1');
  assert.equal(runtime.storage.getItem('eio_theme_user_v1:u2'), null,
    'the stale response did not write the new account cache');
  const root = runtime.doc.documentElement;
  assert.equal(root.dataset.eioThemeId, 'builtin');
  assert.equal(root.style.props['--eio-theme-bg-image'], 'none',
    'the stale payload did not paint a background');
  assert.equal(runtime.transport.getSiteStatus(), null, 'the stale payload was never committed');
  assert.equal(runtime.doc.head.children.length, 0);
  controller.destroy();
});

test('transport: the immutable stylesheet link follows identity and is removed late', async () => {
  const siteTheme = publicTheme();
  const runtime = makeRuntime({
    handler(requestPath) {
      if (requestPath === '/web/api/theme-site') {
        return { available: true, themeId: 'ocean-1', revision: 5, theme: siteTheme };
      }
      throw new Error('unexpected request');
    },
  });
  const controller = controllerFor(runtime);
  await controller.refreshRemote({ timeoutMs: 400 });

  assert.equal(runtime.doc.head.children.length, 1);
  const first = runtime.doc.head.children[0];
  assert.equal(first.attrs['data-eio-theme-css'], '1');

  controller.enterSession('u1');
  assert.equal(runtime.doc.head.children.length, 0, 'switching account removes the old stylesheet');
  let removed = 0;
  const originalRemove = first.remove;
  first.remove = function () { removed += 1; return originalRemove.call(this); };
  first.listeners.load();
  assert.equal(first.parentNode, null, 'a late load event cannot resurrect the link');

  controller.enterSession(null);
  await controller.refreshRemote({ timeoutMs: 400 });
  assert.equal(runtime.doc.head.children.length, 1);
  runtime.transport.destroy();
  assert.equal(runtime.doc.head.children.length, 0, 'transport destroy removes the stylesheet');
  controller.destroy();
});

test('transport: stylesheet href validation rejects query, fragment, foreign and short hashes', () => {
  const runtime = makeRuntime();
  const api = runtime.context.EIOThemeTransport;

  assert.equal(api.safeCssUrl('/web/themes/' + HASH + '/theme.css', HASH), '/web/themes/' + HASH + '/theme.css');
  for (const value of [
    '/web/themes/' + HASH + '/theme.css?api_key=secret',
    '/web/themes/' + HASH + '/theme.css#x',
    'https://evil.example/web/themes/' + HASH + '/theme.css',
    '//evil.example/web/themes/' + HASH + '/theme.css',
    '/web/themes/' + 'a'.repeat(63) + '/theme.css',
    '/web/themes/' + 'A'.repeat(64) + '/theme.css',
    '/web/themes/' + OTHER_HASH + '/theme.css',
  ]) {
    assert.equal(api.safeCssUrl(value, HASH), null, value);
  }

  assert.equal(api.safeAssetUrl('/web/themes/' + HASH + '/assets/bg.png', HASH),
    '/web/themes/' + HASH + '/assets/bg.png');
  assert.equal(api.safeAssetUrl('/web/themes/' + HASH + '/assets/bg.png?x=1', HASH), null);
  assert.equal(api.safeAssetUrl('/web/themes/' + OTHER_HASH + '/assets/bg.png', HASH), null);
  assert.equal(api.safeAssetUrl('/web/themes/' + HASH + '/theme.css', HASH), null);

  assert.equal(api.safePreviewUrl('/web/themes/' + HASH + '/preview.html?mode=dark', HASH),
    '/web/themes/' + HASH + '/preview.html?mode=dark');
  assert.equal(api.safePreviewUrl('/web/themes/' + HASH + '/preview.html?mode=auto', HASH), null);
  assert.equal(api.safePreviewUrl('/web/themes/' + HASH + '/preview.png?api_key=t', HASH), null);
  assert.equal(api.safePreviewUrl('/web/themes/' + OTHER_HASH + '/preview.png', HASH), null);
});

test('transport: an unsafe preview URL makes the site theme unusable', async () => {
  const bad = publicTheme({ previewLightUrl: '/web/themes/' + HASH + '/preview.png?api_key=secret' });
  const runtime = makeRuntime({
    handler(requestPath) {
      if (requestPath === '/web/api/theme-site') {
        return { available: true, themeId: 'ocean-1', revision: 5, theme: bad };
      }
      throw new Error('unexpected request');
    },
  });
  const controller = controllerFor(runtime);
  await controller.refreshRemote({ timeoutMs: 400 });

  assert.equal(controller.getResolved().themeId, 'builtin');
  assert.equal(runtime.transport.getSiteStatus().usable, false);
  assert.equal(runtime.doc.head.children.length, 0);
  controller.destroy();
});

test('transport: a validated theme background and density reach page surfaces, builtin clears them', async () => {
  const siteTheme = publicTheme();
  const backgroundUrl = '/web/themes/' + HASH + '/assets/bg.jpg';
  const runtime = makeRuntime({
    handler(requestPath) {
      if (requestPath === '/web/api/theme-site') {
        return { available: true, themeId: 'ocean-1', revision: 5, theme: siteTheme };
      }
      throw new Error('unexpected request');
    },
  });
  const controller = controllerFor(runtime);
  await controller.refreshRemote({ timeoutMs: 400 });

  const root = runtime.doc.documentElement;
  assert.equal(hostJSON(controller.getResolved()).backgroundUrl, backgroundUrl);
  assert.equal(runtime.doc.head.children.length, 1, 'the immutable stylesheet is still linked');
  assert.equal(root.style.props['--eio-theme-bg-image'], 'url("' + backgroundUrl + '")');
  assert.equal(root.style.props['--eio-theme-bg-scrim'], 'rgba(1, 2, 3, 0.74)');
  assert.equal(root.style.props['--eio-density-space'], '10px');
  assert.equal(root.style.props['--eio-density-gap'], '10px');

  controller.setTheme('builtin');
  assert.equal(controller.getResolved().themeId, 'builtin');
  assert.equal(runtime.doc.head.children.length, 0, 'builtin removes the remote stylesheet');
  assert.equal(root.style.props['--eio-theme-bg-image'], 'none', 'builtin restores no background');
  assert.equal(root.style.props['--eio-theme-bg-scrim'], 'transparent');
  assert.equal(root.style.props['--eio-density-space'], '20px', 'builtin is comfortable');
  assert.equal(root.style.props['--eio-density-gap'], '20px');

  runtime.transport.destroy();
  assert.equal(runtime.doc.head.children.length, 0);
  assert.equal(root.style.props['--eio-theme-bg-image'], 'none');
  controller.destroy();
});

test('transport: ui.css consumes background/density only on non-player page surfaces', () => {
  const css = fs.readFileSync(path.join(__dirname, '..', 'public', 'web', 'ui.css'), 'utf8');
  const playerStart = css.indexOf('/* ================= player');
  const playerEnd = css.indexOf('/* ================= appearance entry');
  assert.ok(playerStart > 0 && playerEnd > playerStart);
  const player = css.slice(playerStart, playerEnd);
  assert.equal(/--eio-(?:theme-bg|density)/.test(player), false,
    'player, subtitle and media selectors never consume theme background/density');

  assert.match(css, /\.eio-shell\s*\{[^}]*var\(--eio-theme-bg-image\)/s);
  assert.match(css, /\.eio-login\s*\{[^}]*var\(--eio-theme-bg-image\)/s);
  assert.match(css, /\.eio-page\s*\{[^}]*padding:\s*var\(--eio-density-space\)/s);
  assert.match(css, /\.eio-grid\s*\{[^}]*gap:\s*var\(--eio-density-gap\)/s);
  assert.match(css, /--eio-theme-bg-image:\s*none/);
  assert.match(css, /--eio-theme-bg-scrim:\s*transparent/);
  assert.equal(css.includes('/web/themes/'), false, 'ui.css never hard-codes an immutable theme asset URL');
  assert.equal(/url\(\s*['"]?\/web\//.test(css), false);
});
