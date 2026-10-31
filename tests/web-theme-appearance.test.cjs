'use strict';
/* T1 appearance controller and thin app wiring. The controller only changes
 * appearance state; the app test proves a switch does not mount/stop/seek the
 * player or change the route. */
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { harness, makeStorage } = require('./web-ui-harness.cjs');

const THEME_SRC = fs.readFileSync(path.join(__dirname, '..', 'public', 'web', 'theme.js'), 'utf8');
const APPEARANCE_SRC = fs.readFileSync(path.join(__dirname, '..', 'public', 'web', 'appearance.js'), 'utf8');

function loadAppearance() {
  const listeners = {};
  const documentElement = {
    dataset: {},
    style: { props: {}, setProperty(name, value) { this.props[name] = value; } },
  };
  const document = {
    documentElement,
    addEventListener(type, fn) { (listeners[type] ||= []).push(fn); },
    removeEventListener(type, fn) {
      const list = listeners[type] || [];
      const index = list.indexOf(fn);
      if (index >= 0) list.splice(index, 1);
    },
  };
  const context = {
    console, setTimeout, clearTimeout, Promise, JSON, Math, Set, Map, Object, Array, String,
    localStorage: makeStorage(), document,
    Vue: { reactive: value => value },
    addEventListener() {}, removeEventListener() {},
  };
  context.window = context;
  vm.createContext(context);
  vm.runInContext(THEME_SRC, context);
  vm.runInContext(APPEARANCE_SRC, context);
  const theme = context.EIOTheme.createController({ initialUserId: null, matchMedia: null });
  const appearance = context.EIOAppearance.createController({ theme });
  return { context, theme, appearance, listeners, documentElement };
}

test('appearance: entry controller opens, validates choices, resets and destroys', () => {
  const { appearance, theme, listeners, documentElement } = loadAppearance();
  assert.equal(appearance.state.open, false);
  assert.equal(appearance.state.mode, 'dark');
  assert.ok(appearance.state.modeOptions.some(option => option.id === 'system'));
  assert.ok(appearance.state.themeOptions.some(option => option.id === 'builtin'));
  assert.ok(appearance.state.themeOptions.some(option => option.id === null));

  appearance.open();
  assert.equal(appearance.state.open, true);
  const onKeydown = listeners.keydown[0];
  assert.equal(typeof onKeydown, 'function');
  onKeydown({ key: 'Escape' });
  assert.equal(appearance.state.open, false);
  appearance.toggle();
  appearance.close();
  assert.equal(appearance.state.open, false);

  appearance.chooseMode('light');
  assert.equal(appearance.state.mode, 'light');
  assert.equal(theme.getPreference().mode, 'light');
  assert.equal(documentElement.dataset.eioTheme, 'light');
  appearance.chooseMode('auto');
  assert.equal(appearance.state.mode, 'light');

  appearance.chooseTheme('builtin');
  assert.equal(theme.getPreference().themeId, 'builtin');
  appearance.resetBuiltin();
  assert.match(appearance.state.notice, /恢复内置主题/);

  appearance.enterSession('u1');
  assert.equal(theme.getIdentity(), 'u1');
  assert.equal(appearance.state.mode, 'dark', 'a new account starts from its own default; the anonymous light must not leak');
  assert.equal(theme.getPreference().mode, 'dark');

  appearance.leaveSession();
  assert.equal(theme.getIdentity(), null);
  assert.equal(appearance.state.mode, 'light', 'leaving a session restores the anonymous preference, not the account default');
  assert.equal(theme.getPreference().mode, 'light');

  appearance.destroy();
  assert.equal(appearance.isDestroyed(), true);
  assert.equal(theme.isDestroyed(), true);
  assert.equal((listeners.keydown || []).length, 0);
  appearance.chooseMode('dark');
  assert.equal(appearance.state.mode, 'light', 'a destroyed controller must not change appearance');
});

test('appearance: component registration exposes the shared controller component', () => {
  const { context } = loadAppearance();
  const registered = [];
  context.EIOAppearance.register({ component(name, component) { registered.push({ name, component }); } });
  assert.equal(registered.length, 1);
  assert.equal(registered[0].name, 'EioAppearance');
  assert.equal(registered[0].component.props.controller.required, true);
  assert.equal(registered[0].component.props.surface.default, 'sidebar');
  assert.match(registered[0].component.template, /恢复内置主题/);
  assert.match(registered[0].component.template, /role="dialog"/);
});

test('app wiring: appearance is exposed and switching never loads/stops/seeks the player', () => {
  let playerLoads = 0;
  const { app, context, documentElement } = harness({
    beforeApp(ctx) { ctx.EIOPlayer = function () { playerLoads++; }; },
  });
  assert.ok(app.appearance, 'app setup returns the appearance controller');
  assert.equal(documentElement.dataset.eioTheme, 'dark');
  assert.equal(app.appearance.state.remoteStatus, 'local');
  const hashBefore = context.location.hash;
  const pageBefore = app.page.value;
  const navBefore = app.navPage.value;
  const statusBefore = app.playerUI.status.value;
  const menuBefore = app.playerUI.openMenu.value;

  app.appearance.chooseMode('light');
  assert.equal(documentElement.dataset.eioTheme, 'light');
  assert.equal(app.appearance.state.mode, 'light');
  app.appearance.chooseMode('system');
  app.appearance.chooseTheme('builtin');
  app.appearance.resetBuiltin();

  assert.equal(playerLoads, 0);
  assert.equal(app.playerUI.status.value, statusBefore);
  assert.equal(app.playerUI.openMenu.value, menuBefore);
  assert.equal(context.location.hash, hashBefore);
  assert.equal(app.page.value, pageBefore);
  assert.equal(app.navPage.value, navBefore);
});

test('app wiring: login/logout enter and leave per-account caches', async () => {
  let playerLoads = 0;
  const { app, storage } = harness({
    beforeApp(ctx) { ctx.EIOPlayer = function () { playerLoads++; }; },
    reply(url, opts) {
      if (url.pathname.endsWith('/Users/AuthenticateByName')) {
        const name = JSON.parse(opts.body).Username;
        return { AccessToken: 'token-' + name, User: { Id: name, Name: name, Policy: {} } };
      }
      return { Items: [] };
    },
  });

  app.loginForm.value = { user: 'u1', pass: 'p' };
  await app.doLogin();
  assert.equal(storage._store.get('eio_token'), 'token-u1');
  assert.equal(storage._store.get('eio_user_id'), 'u1');
  assert.equal(app.appearance.theme.getIdentity(), 'u1');
  app.appearance.chooseMode('light');
  assert.equal(JSON.parse(storage._store.get('eio_theme_user_v1:u1')).mode, 'light');

  app.doLogout();
  assert.equal(storage._store.has('eio_token'), false);
  assert.equal(app.appearance.theme.getIdentity(), null);
  assert.equal(app.appearance.state.mode, 'dark');

  app.loginForm.value = { user: 'u2', pass: 'p' };
  await app.doLogin();
  assert.equal(app.appearance.theme.getIdentity(), 'u2');
  assert.equal(app.appearance.state.mode, 'dark', 'u1 preference must not leak to u2');

  app.doLogout();
  app.loginForm.value = { user: 'u1', pass: 'p' };
  await app.doLogin();
  assert.equal(app.appearance.theme.getIdentity(), 'u1');
  assert.equal(app.appearance.state.mode, 'light', 'u1 cache is restored only for u1');
  assert.equal(playerLoads, 0);
});
