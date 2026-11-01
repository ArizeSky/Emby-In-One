'use strict';
/* T1 head bootstrap + static page/CSS wiring. The bootstrap is loaded in a VM
 * with a minimal document and must never hide the page, call the network or
 * accept another account's cache. */
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const BOOT_SRC = fs.readFileSync(path.join(__dirname, '..', 'public', 'web', 'theme-boot.js'), 'utf8');
const WEB_HTML = fs.readFileSync(path.join(__dirname, '..', 'public', 'web', 'web.html'), 'utf8');
const UI_CSS = fs.readFileSync(path.join(__dirname, '..', 'public', 'web', 'ui.css'), 'utf8');

function makeStorage(initial = {}, deny = false) {
  const map = new Map(Object.entries(initial));
  return {
    map,
    getItem(key) {
      if (deny) throw new Error('denied');
      return map.has(key) ? map.get(key) : null;
    },
    setItem(key, value) {
      if (deny) throw new Error('denied');
      map.set(key, String(value));
    },
    removeItem(key) {
      if (deny) throw new Error('denied');
      map.delete(key);
    },
  };
}

function makeBoot(options = {}) {
  const documentElement = { dataset: {}, style: {} };
  const context = {
    console, JSON, Promise, Math, String, Object,
    localStorage: options.storage || makeStorage(),
    document: { documentElement },
    matchMedia: options.media ? () => options.media : undefined,
  };
  context.window = context;
  vm.createContext(context);
  vm.runInContext(BOOT_SRC, context);
  return { boot: context.EIOThemeBoot, context, documentElement };
}

function cache(mode, themeId = null, scope = 'anon', userId = null) {
  return JSON.stringify({ schema: 1, scope, userId, themeId, mode, revision: 0 });
}

test('boot: default first paint is builtin dark without touching the network', () => {
  const { boot, documentElement } = makeBoot();
  assert.equal(documentElement.dataset.eioTheme, 'dark');
  assert.equal(documentElement.dataset.eioThemeId, 'builtin');
  assert.equal(documentElement.dataset.eioMode, 'dark');
  assert.equal(boot.validThemeId('builtin'), true);
  assert.equal(boot.validThemeId('default'), false);
  assert.equal(boot.validThemeId('Bad ID'), false);
});

test('boot: validates cached light/dark and system mode before first CSS', () => {
  const light = makeBoot({ storage: makeStorage({ eio_theme_anon_v1: cache('light') }) });
  assert.equal(light.documentElement.dataset.eioTheme, 'light');
  assert.equal(light.documentElement.dataset.eioMode, 'light');

  const system = makeBoot({
    storage: makeStorage({ eio_theme_anon_v1: cache('system') }),
    media: { matches: false },
  });
  assert.equal(system.documentElement.dataset.eioTheme, 'light');
  assert.equal(system.documentElement.dataset.eioMode, 'system');
});

test('boot: user cache requires the matching session token and user id', () => {
  const userKey = 'eio_theme_user_v1:u1';
  const noToken = makeBoot({
    storage: makeStorage({
      [userKey]: cache('light', null, 'user', 'u1'),
      eio_theme_anon_v1: cache('dark'),
    }),
  });
  assert.equal(noToken.documentElement.dataset.eioTheme, 'dark');
  assert.equal(noToken.documentElement.dataset.eioMode, 'dark');

  const signedIn = makeBoot({
    storage: makeStorage({
      eio_token: 'token', eio_user_id: 'u1',
      [userKey]: cache('light', null, 'user', 'u1'),
    }),
  });
  assert.equal(signedIn.documentElement.dataset.eioTheme, 'light');
  assert.equal(signedIn.documentElement.dataset.eioMode, 'light');
});

test('boot: corrupt cache is removed and denied storage falls back in memory', () => {
  const storage = makeStorage({ eio_theme_anon_v1: '{not-json' });
  const corrupt = makeBoot({ storage });
  assert.equal(corrupt.documentElement.dataset.eioTheme, 'dark');
  assert.equal(storage.map.has('eio_theme_anon_v1'), false);

  const denied = makeBoot({ storage: makeStorage({}, true) });
  assert.equal(denied.documentElement.dataset.eioTheme, 'dark');
});

test('web.html: external boot runs before CSS, modules are in order, no inline script', () => {
  const head = WEB_HTML.slice(0, WEB_HTML.indexOf('</head>'));
  const bootAt = head.indexOf('/web/web/theme-boot.js');
  const cssAt = head.indexOf('<link rel="stylesheet"');
  assert.ok(bootAt >= 0, 'theme-boot must be linked from head');
  assert.ok(cssAt >= 0 && bootAt < cssAt, 'theme-boot must precede the first stylesheet');
  assert.equal(/<script(?![^>]*\bsrc=)[^>]*>/i.test(WEB_HTML), false, 'no inline scripts (CSP)');
  assert.equal(/<style\b/i.test(WEB_HTML), false, 'no inline theme style block');

  const themeAt = WEB_HTML.indexOf('/web/web/theme.js');
  const appearanceAt = WEB_HTML.indexOf('/web/web/appearance.js');
  const appAt = WEB_HTML.indexOf('/web/web/app.js');
  const componentsAt = WEB_HTML.indexOf('/web/web/ui-components.js');
  assert.ok(componentsAt >= 0 && themeAt > componentsAt);
  assert.ok(appearanceAt > themeAt);
  assert.ok(appAt > appearanceAt);
});

test('web.html: login, desktop sidebar and mobile menu expose appearance entry points', () => {
  assert.match(WEB_HTML, /v-if="page==='login'"[\s\S]*?<eio-appearance[^>]*surface="login"/);
  assert.match(WEB_HTML, /<div class="eio-nav-foot">[\s\S]*?<eio-appearance[^>]*surface="sidebar"/);
  assert.match(WEB_HTML, /eio-mobile-menu[\s\S]*?<eio-appearance[^>]*surface="mobile"/);
  assert.match(WEB_HTML, /<script src="\/web\/web\/theme-boot\.js/);
});

test('ui.css: data-driven first paint, fixed player domain and safe accent-on text', () => {
  assert.match(UI_CSS, /html\[data-eio-theme="light"\]/);
  assert.match(UI_CSS, /html\[data-eio-theme="light"\][\s\S]*?--bg-0:\s*#f5f7fb/);
  assert.match(UI_CSS, /body\.eio-web \.player-container,\s*body\.eio-web \.eio-play-layer\s*\{[\s\S]*?--t1:\s*#f1f5f9/);
  assert.match(UI_CSS, /body\.eio-web \.player-container\s*\{\s*position:\s*relative;\s*background:\s*#000/);
  assert.match(UI_CSS, /\.player-subtitle-text[^}]*color:\s*#fff/);
  assert.match(UI_CSS, /\.eio-seek-bubble[^}]*background:\s*rgba\(7, 10, 18, \.92\)/);
  assert.match(UI_CSS, /\.eio-btn-primary\s*\{[^}]*color:\s*var\(--ac-on\)/);
  assert.match(UI_CSS, /\.eio-nav-link\.active\s*\{[^}]*color:\s*var\(--ac-on\)/);
  assert.doesNotMatch(UI_CSS, /\.eio-btn-primary\s*\{[^}]*--ac-text/);
  assert.match(UI_CSS, /\.eio-appearance\.is-login/);
  assert.match(UI_CSS, /\.eio-appearance\.is-sidebar/);
  assert.match(UI_CSS, /\.eio-appearance\.is-mobile/);
});
