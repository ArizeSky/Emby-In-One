'use strict';
/* Admin theme management module (public/admin-theme.js).
 *
 * The module owns the whole admin theme state machine: the frozen catalog DTO
 * normalization, the generation/abort guard, the CAS mutations and the fixed
 * preview URLs. admin.js only delegates to it, so these tests drive the module
 * directly with a fake same-origin API and assert the wire contract of
 * internal/backend/theme_api_types.go:
 *
 *   GET    /admin/api/themes                       -> {available,themes,site,quota,health}
 *   POST   /admin/api/themes    application/zip
 *   PUT    /admin/api/themes/{id}/package  application/zip + If-Match
 *   PATCH  /admin/api/themes/{id}  {enabled,expectedRevision}
 *   DELETE /admin/api/themes/{id}  If-Match
 *   PUT    /admin/api/theme-site   {themeId,expectedRevision}
 *
 * No DOM, no localStorage and no real network: the module must not be able to
 * inject theme CSS (or anything else) into the panel document.
 */
const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const MODULE_PATH = path.join(__dirname, '..', 'public', 'admin-theme.js');

function abortError() {
  return new DOMException('The operation was aborted.', 'AbortError');
}

function httpError(status, body) {
  const message = body && (body.message || body.error) ? (body.message || body.error) : 'HTTP ' + status;
  const error = new Error(message);
  error.status = status;
  error.data = body || {};
  return error;
}

function loadModule(extra = {}) {
  const context = {
    console, Promise, JSON, Math, setTimeout, clearTimeout,
    AbortController, DOMException,
    ...extra,
  };
  vm.createContext(context);
  vm.runInContext(fs.readFileSync(MODULE_PATH, 'utf8'), context);
  assert.ok(context.EIOAdminTheme, 'public/admin-theme.js must expose EIOAdminTheme');
  return context.EIOAdminTheme;
}

function builtinDTO() {
  return {
    id: 'builtin', name: '内置主题', author: '', description: '', version: '',
    hash: '', variants: ['light', 'dark'], builtin: true, enabled: true, available: true,
    revision: 0, packageBytes: 0, currentHash: '', previousHash: '', problem: '',
    cssUrl: '', previewUrl: '', backgroundUrl: '', previewLightUrl: '', previewDarkUrl: '',
    definition: null,
  };
}

function themeDTO(overrides = {}) {
  return Object.assign({
    id: 'aurora',
    name: 'Aurora',
    author: 'Studio',
    description: 'Northern lights',
    version: '1.2.0',
    hash: 'hash-a',
    variants: ['light', 'dark'],
    builtin: false,
    enabled: true,
    available: true,
    revision: 4,
    packageBytes: 2048,
    currentHash: 'hash-a',
    previousHash: 'hash-prev',
    problem: '',
    cssUrl: '/web/themes/hash-a/theme.css',
    previewUrl: '/web/themes/hash-a/preview.png',
    backgroundUrl: '/web/themes/hash-a/assets/bg.jpg',
    previewLightUrl: '/web/themes/hash-a/preview.html?mode=light',
    previewDarkUrl: '/web/themes/hash-a/preview.html?mode=dark',
    definition: { variants: { light: { bg0: '#ffffff' } } },
  }, overrides);
}

function catalogDTO(overrides = {}) {
  return Object.assign({
    available: true,
    themes: [builtinDTO(), themeDTO()],
    site: { available: true, themeId: 'builtin', revision: 3 },
    quota: { maxThemes: 20, usedThemes: 1, maxBytes: 64 * 1024 * 1024, usedBytes: 2048 },
    health: { available: true, recovered: 0, missing: 0, orphanDirs: 0, orphanBytes: 0, problems: [] },
  }, overrides);
}

/* Fake same-origin admin API.
 *
 * Every call is recorded with the exact method/headers/body/signal the module
 * used. A test marks the next N calls as manual with defer(); those settle only
 * through call.resolve/call.reject, which is what a slow server looks like.
 * Aborted signals reject with an AbortError, like fetch does.
 */
function createHarness(initialCatalog, extraGlobals) {
  const mod = loadModule(extraGlobals);
  const h = {
    mod,
    state: mod.newState(),
    calls: [],
    catalog: initialCatalog || catalogDTO(),
    auto: null,
    manual: 0,
  };

  function api(url, opts = {}) {
    const call = {
      url: String(url),
      method: opts.method || 'GET',
      headers: Object.assign({}, opts.headers || {}),
      body: opts.body,
      signal: opts.signal,
      resolve: null,
      reject: null,
    };
    h.calls.push(call);
    const isManual = h.manual > 0;
    if (isManual) h.manual -= 1;
    return new Promise((resolve, reject) => {
      call.resolve = (value) => resolve(value);
      call.reject = (error) => reject(error);
      const signal = opts.signal;
      if (signal) {
        if (signal.aborted) { reject(abortError()); return; }
        signal.addEventListener('abort', () => reject(abortError()), { once: true });
      }
      if (isManual) return;
      Promise.resolve()
        .then(() => (h.auto ? h.auto(call) : (call.method === 'GET' && call.url === '/admin/api/themes'
          ? h.catalog
          : { success: true })))
        .then((result) => {
          if (result && result.__delay) return; // only an abort settles this reply
          if (result && result.__status) { reject(httpError(result.__status, result.__body)); return; }
          resolve(result);
        }, reject);
    });
  }

  h.api = api;
  h.defer = (count = 1) => { h.manual += count; };
  h.callsTo = (url, method) => h.calls.filter(call => call.url === url && (!method || call.method === method));
  h.lastCall = () => h.calls[h.calls.length - 1];
  h.load = async (catalog) => {
    if (catalog) h.catalog = catalog;
    await mod.refreshThemes(h.state, { api });
    return h.state;
  };
  h.snapshot = (value) => JSON.parse(JSON.stringify(value));
  return h;
}

const tick = () => new Promise(resolve => setTimeout(resolve, 0));

// --- frozen DTO normalization -------------------------------------------

test('catalog load normalizes the frozen admin DTO and drops unknown fields', async () => {
  const h = createHarness();
  h.catalog = catalogDTO();
  // A server response can carry keys the client must not render: only the
  // frozen fields are copied.
  h.catalog = Object.assign({}, h.catalog, {
    themes: [Object.assign(builtinDTO(), { definition: { evil: true } }),
      Object.assign(themeDTO({ revision: 4 }), { definition: { variants: {} }, extra: 'leak' })],
    extraTopLevel: 'leak',
  });
  await h.load();
  const state = h.state;
  assert.strictEqual(state.available, true);
  assert.strictEqual(state.loaded, true);
  assert.strictEqual(state.error, '');
  assert.strictEqual(state.themes.length, 2);
  assert.strictEqual(state.themes[0].builtin, true);
  const row = state.themes[1];
  assert.strictEqual(row.id, 'aurora');
  assert.strictEqual(row.revision, 4);
  assert.strictEqual(row.currentHash, 'hash-a');
  assert.strictEqual(row.previewLightUrl, '/web/themes/hash-a/preview.html?mode=light');
  assert.strictEqual(row.previewDarkUrl, '/web/themes/hash-a/preview.html?mode=dark');
  assert.strictEqual(row.cssUrl, '/web/themes/hash-a/theme.css');
  assert.strictEqual(Object.prototype.hasOwnProperty.call(row, 'definition'), false,
    'the tokens are for /web, not for the admin DOM');
  assert.strictEqual(Object.prototype.hasOwnProperty.call(row, 'extra'), false);
  assert.strictEqual(Object.prototype.hasOwnProperty.call(state, 'extraTopLevel'), false);
  assert.strictEqual(state.site.themeId, 'builtin');
  assert.strictEqual(state.site.revision, 3);
  assert.strictEqual(state.quota.maxThemes, 20);
  assert.strictEqual(state.health.available, true);
  assert.deepStrictEqual(h.snapshot(state.health.problems), []);
});

test('malformed catalog fields never produce undefined view values', async () => {
  const h = createHarness();
  h.catalog = catalogDTO({
    themes: [{ id: 'x', revision: '4', enabled: 'yes', variants: 'light', previewLightUrl: 5 }, null, {}],
    site: null,
    quota: 'nope',
    health: { problems: ['disk', null, 7] },
  });
  await h.load();
  const state = h.state;
  assert.strictEqual(state.themes.length, 1, 'entries without a usable id are dropped');
  const row = state.themes[0];
  assert.strictEqual(row.name, 'x');
  assert.strictEqual(row.revision, 0);
  assert.strictEqual(row.enabled, false);
  assert.deepStrictEqual(h.snapshot(row.variants), []);
  assert.strictEqual(row.previewLightUrl, '');
  assert.strictEqual(state.site.themeId, 'builtin');
  assert.strictEqual(state.site.available, false);
  assert.strictEqual(state.quota.maxThemes, 0);
  assert.deepStrictEqual(h.snapshot(state.health.problems), ['disk']);
});

test('an unavailable catalog clears the rows instead of caching them', async () => {
  const h = createHarness();
  await h.load();
  assert.strictEqual(h.state.themes.length, 2);
  await h.load(catalogDTO({ available: false, themes: [] }));
  assert.strictEqual(h.state.available, false);
  assert.strictEqual(h.state.themes.length, 0);
  assert.strictEqual(h.state.loaded, false);
  assert.match(h.state.error, /不可用/);
});

test('a malformed catalog payload is an error, not an empty catalog', async () => {
  const h = createHarness();
  h.auto = () => 'not a catalog object';
  await h.mod.refreshThemes(h.state, { api: h.api });
  assert.strictEqual(h.state.loaded, false);
  assert.match(h.state.error, /格式无效/);
  assert.strictEqual(h.state.themes.length, 0);
});

test('a transient refresh failure keeps the loaded rows visible', async () => {
  const h = createHarness();
  await h.load();
  assert.strictEqual(h.state.themes.length, 2);
  h.auto = () => ({ __status: 500, __body: { message: '主题服务暂不可用' } });
  await h.mod.refreshThemes(h.state, { api: h.api });
  assert.strictEqual(h.state.themes.length, 2, 'a failed refresh must not erase the list');
  assert.match(h.state.error, /主题服务暂不可用/);
  assert.strictEqual(h.state.loaded, true);
});

// --- generation / abort isolation --------------------------------------

test('a late catalog response cannot overwrite a newer one', async () => {
  const h = createHarness();
  h.defer(1);
  const first = h.mod.refreshThemes(h.state, { api: h.api });
  await tick();
  const firstCall = h.lastCall();
  assert.ok(firstCall.signal, 'the catalog GET carries an AbortController signal');

  h.catalog = catalogDTO({ themes: [builtinDTO(), themeDTO({ id: 'newer', name: 'Newer' })] });
  const second = h.mod.refreshThemes(h.state, { api: h.api });
  assert.strictEqual(firstCall.signal.aborted, true, 'starting a new load aborts the previous one');
  await second;
  await first;
  assert.strictEqual(h.state.themes.length, 2);
  assert.strictEqual(h.state.themes[1].id, 'newer', 'the late response overwrote newer state');
});

test('leaving the page aborts in-flight requests and clears transient UI', async () => {
  const h = createHarness();
  await h.load();
  h.mod.openUpdateDialog(h.state, h.state.themes[1]);
  h.mod.openPreview(h.state, h.state.themes[1], 'light');
  h.defer(1);
  const pending = h.mod.toggleTheme(h.state, h.state.themes[1], { api: h.api });
  await tick();
  const patch = h.callsTo('/admin/api/themes/aurora', 'PATCH')[0];
  assert.ok(patch && patch.signal);

  h.mod.leave(h.state);
  assert.strictEqual(patch.signal.aborted, true, 'leaving the page must abort the in-flight mutation');
  assert.strictEqual(h.state.dialog, null);
  assert.strictEqual(h.state.preview, null);
  assert.strictEqual(h.state.loading, false);
  assert.deepStrictEqual(h.snapshot(h.state.pending), {});
  assert.strictEqual(h.state.themes.length, 2, 'the list stays cached for the next visit');
  assert.strictEqual(await pending, false);
});

test('logout reset drops every theme row and the previous site/revision', async () => {
  const h = createHarness();
  await h.load();
  h.mod.openPreview(h.state, h.state.themes[1], 'light');
  h.mod.reset(h.state);
  assert.strictEqual(h.state.themes.length, 0);
  assert.strictEqual(h.state.loaded, false);
  assert.strictEqual(h.state.available, false);
  assert.strictEqual(h.state.preview, null);
  assert.strictEqual(h.state.site.themeId, 'builtin');
  assert.strictEqual(h.state.site.revision, 0);
});

// --- upload / update ----------------------------------------------------

test('the ZIP picker enforces the 1.5 MiB client limit before any request', async () => {
  const h = createHarness();
  await h.load();
  h.mod.openUploadDialog(h.state);
  assert.strictEqual(h.mod.selectFile(h.state, { size: 1536 * 1024 + 1, name: 'big.zip' }), false);
  assert.match(h.state.dialog.error, /1\.5 MiB/);
  assert.strictEqual(h.calls.length, 1, 'an oversized ZIP must not reach the network');
  assert.strictEqual(h.mod.selectFile(h.state, { size: 0, name: 'empty.zip' }), false);
  assert.match(h.state.dialog.error, /非空/);
  const file = { size: 1024, name: 'aurora.zip' };
  assert.strictEqual(h.mod.selectFile(h.state, file), true);
  assert.strictEqual(h.state.dialog.fileName, 'aurora.zip');
  assert.strictEqual(h.state.dialog.fileSize, 1024);
  assert.strictEqual(h.state.dialog.error, '');
  assert.strictEqual(h.mod.dialogReady(h.state), true);
});

test('install posts the raw ZIP to the collection without If-Match', async () => {
  const h = createHarness();
  await h.load();
  h.mod.openUploadDialog(h.state);
  const file = { size: 2048, name: 'aurora.zip' };
  h.mod.selectFile(h.state, file);
  h.defer(1);
  const pending = h.mod.submitDialog(h.state, { api: h.api });
  await tick();
  const post = h.callsTo('/admin/api/themes', 'POST')[0];
  assert.ok(post, 'the install issued POST /admin/api/themes');
  assert.strictEqual(post.headers['Content-Type'], 'application/zip');
  assert.strictEqual(post.headers['If-Match'], undefined, 'a first install has no revision to match');
  assert.strictEqual(post.body, file, 'the ZIP body is sent unchanged');
  assert.ok(post.signal, 'the upload carries an abort signal');

  h.catalog = catalogDTO({ themes: [builtinDTO(), themeDTO({ revision: 1, name: 'Aurora v1' })] });
  post.resolve(themeDTO({ revision: 1, name: 'Aurora v1' }));
  assert.strictEqual(await pending, true);
  assert.strictEqual(h.state.dialog, null);
  assert.match(h.state.notice, /已安装/);
  const row = h.state.themes.find(theme => theme.id === 'aurora');
  assert.strictEqual(row.revision, 1);
  assert.strictEqual(row.name, 'Aurora v1');
  assert.ok(h.callsTo('/admin/api/themes', 'GET').length >= 2, 'a successful install re-reads the catalog');
});

test('a duplicate save click while the ZIP is uploading issues no second request', async () => {
  const h = createHarness();
  await h.load();
  h.mod.openUploadDialog(h.state);
  const file = { size: 1024, name: 'a.zip' };
  h.mod.selectFile(h.state, file);
  h.defer(1);
  const first = h.mod.submitDialog(h.state, { api: h.api });
  await tick();
  assert.strictEqual(h.state.pending.install, true);
  assert.strictEqual(await h.mod.submitDialog(h.state, { api: h.api }), false);
  assert.strictEqual(h.callsTo('/admin/api/themes', 'POST').length, 1,
    'the second click must reuse the in-flight save, not start another one');

  h.catalog = catalogDTO({ themes: [builtinDTO(), themeDTO({ revision: 1 })] });
  h.callsTo('/admin/api/themes', 'POST')[0].resolve(themeDTO({ revision: 1 }));
  await first;
  assert.strictEqual(h.state.pending.install, undefined);
});

test('update sends the row revision in If-Match and encodes the theme id', async () => {
  const h = createHarness();
  await h.load(catalogDTO({ themes: [builtinDTO(), themeDTO({ id: 'weird id', revision: 4 })] }));
  const row = h.state.themes[1];
  assert.strictEqual(h.mod.openUpdateDialog(h.state, row), true);
  assert.strictEqual(h.state.dialog.expectedRevision, 4);
  h.mod.selectFile(h.state, { size: 512, name: 'w.zip' });
  h.defer(1);
  const pending = h.mod.submitDialog(h.state, { api: h.api });
  await tick();
  const put = h.callsTo('/admin/api/themes/weird%20id/package', 'PUT')[0];
  assert.ok(put, 'the update issued PUT /admin/api/themes/{id}/package');
  assert.strictEqual(put.headers['Content-Type'], 'application/zip');
  assert.strictEqual(put.headers['If-Match'], '4');
  h.catalog = catalogDTO({ themes: [builtinDTO(), themeDTO({ id: 'weird id', revision: 5 })] });
  put.resolve(themeDTO({ id: 'weird id', revision: 5 }));
  await pending;
  assert.strictEqual(h.state.themes[1].revision, 5);
  assert.match(h.state.notice, /revision 5/);
});

test('an update dialog without a usable revision cannot be submitted', async () => {
  const h = createHarness();
  await h.load();
  h.mod.openUpdateDialog(h.state, h.state.themes[1]);
  h.mod.selectFile(h.state, { size: 512, name: 'w.zip' });
  h.state.dialog.expectedRevision = 0;
  assert.strictEqual(await h.mod.submitDialog(h.state, { api: h.api }), false);
  assert.match(h.state.dialog.error, /revision/);
  assert.strictEqual(h.callsTo('/admin/api/themes/aurora/package', 'PUT').length, 0);
});

test('a full quota blocks the install dialog without a request', async () => {
  const h = createHarness();
  await h.load(catalogDTO({
    quota: { maxThemes: 1, usedThemes: 1, maxBytes: 1024, usedBytes: 2048 },
  }));
  assert.strictEqual(h.mod.canInstall(h.state), false);
  h.mod.openUploadDialog(h.state);
  h.mod.selectFile(h.state, { size: 512, name: 'a.zip' });
  assert.strictEqual(await h.mod.submitDialog(h.state, { api: h.api }), false);
  assert.match(h.state.dialog.error, /配额/);
  assert.strictEqual(h.callsTo('/admin/api/themes', 'POST').length, 0);
});

// --- row mutations ------------------------------------------------------

test('toggle sends the explicit target state with the row revision', async () => {
  const h = createHarness();
  await h.load();
  h.defer(1);
  const pending = h.mod.toggleTheme(h.state, h.state.themes[1], { api: h.api });
  await tick();
  const patch = h.callsTo('/admin/api/themes/aurora', 'PATCH')[0];
  assert.ok(patch);
  assert.deepStrictEqual(h.snapshot(JSON.parse(patch.body)), { enabled: false, expectedRevision: 4 });
  h.catalog = catalogDTO({ themes: [builtinDTO(), themeDTO({ enabled: false })] });
  patch.resolve(themeDTO({ enabled: false }));
  assert.strictEqual(await pending, true);
  assert.strictEqual(h.state.themes[1].enabled, false);
  assert.match(h.state.notice, /已停用/);
});

test('a 409 refresh never pretends the toggle succeeded', async () => {
  const h = createHarness();
  await h.load();
  h.defer(1);
  const pending = h.mod.toggleTheme(h.state, h.state.themes[1], { api: h.api });
  await tick();
  h.callsTo('/admin/api/themes/aurora', 'PATCH')[0].reject(httpError(409, { message: '主题状态已变化' }));
  assert.strictEqual(await pending, false);
  const row = h.state.themes[1];
  assert.strictEqual(row.enabled, true, 'the row kept the server state, not the optimistic click');
  assert.match(h.state.actionError, /列表已刷新/);
  assert.ok(h.callsTo('/admin/api/themes', 'GET').length >= 2, 'a 409 refreshes the catalog');
});

test('a 409 on update adopts the refreshed revision into the dialog', async () => {
  const h = createHarness();
  await h.load();
  h.mod.openUpdateDialog(h.state, h.state.themes[1]);
  h.mod.selectFile(h.state, { size: 512, name: 'a.zip' });
  h.defer(1);
  const pending = h.mod.submitDialog(h.state, { api: h.api });
  await tick();
  h.catalog = catalogDTO({ themes: [builtinDTO(), themeDTO({ revision: 9 })] });
  h.callsTo('/admin/api/themes/aurora/package', 'PUT')[0].reject(httpError(409, { message: '主题状态已变化' }));
  assert.strictEqual(await pending, false);
  assert.ok(h.state.dialog, 'the update dialog stays open for a retry');
  assert.strictEqual(h.state.dialog.expectedRevision, 9, 'the retry uses the refreshed revision');
  assert.match(h.state.dialog.error, /列表已刷新/);
});

test('set site default sends the site revision and applies the server response', async () => {
  const h = createHarness();
  await h.load();
  h.defer(1);
  const pending = h.mod.setSiteDefault(h.state, h.state.themes[1], { api: h.api });
  await tick();
  const put = h.callsTo('/admin/api/theme-site', 'PUT')[0];
  assert.ok(put);
  assert.deepStrictEqual(h.snapshot(JSON.parse(put.body)), { themeId: 'aurora', expectedRevision: 3 });
  h.catalog = catalogDTO({ site: { available: true, themeId: 'aurora', revision: 4 } });
  put.resolve({ available: true, themeId: 'aurora', revision: 4 });
  assert.strictEqual(await pending, true);
  assert.strictEqual(h.state.site.themeId, 'aurora');
  assert.strictEqual(h.state.site.revision, 4);
  assert.match(h.state.notice, /站点默认/);
});

test('a disabled or unavailable custom theme cannot be made the site default', async () => {
  const h = createHarness();
  await h.load();
  const disabled = Object.assign({}, h.state.themes[1], { enabled: false });
  assert.strictEqual(await h.mod.setSiteDefault(h.state, disabled, { api: h.api }), false);
  assert.match(h.state.actionError, /已启用且可用/);
  const unavailable = Object.assign({}, h.state.themes[1], { available: false });
  assert.strictEqual(await h.mod.setSiteDefault(h.state, unavailable, { api: h.api }), false);
  assert.strictEqual(h.callsTo('/admin/api/theme-site', 'PUT').length, 0);
});

test('delete uses If-Match and removes the row only from the server response', async () => {
  const h = createHarness();
  await h.load();
  h.mod.openPreview(h.state, h.state.themes[1], 'light');
  h.defer(1);
  const pending = h.mod.deleteTheme(h.state, h.state.themes[1], { api: h.api });
  await tick();
  const del = h.callsTo('/admin/api/themes/aurora', 'DELETE')[0];
  assert.ok(del);
  assert.strictEqual(del.headers['If-Match'], '4');
  assert.ok(!del.body, 'delete carries no request body');
  assert.strictEqual(h.state.themes.length, 2, 'nothing is removed before the server confirms');
  h.catalog = catalogDTO({ themes: [builtinDTO()], quota: { maxThemes: 20, usedThemes: 0, maxBytes: 1024, usedBytes: 0 } });
  del.resolve({ success: true });
  assert.strictEqual(await pending, true);
  assert.strictEqual(h.state.themes.length, 1);
  assert.strictEqual(h.state.preview, null, 'the deleted theme preview is closed');
  assert.match(h.state.notice, /已删除/);
});

test('the builtin theme can never be toggled, updated or deleted', async () => {
  const h = createHarness();
  await h.load();
  const builtin = h.state.themes[0];
  assert.strictEqual(await h.mod.toggleTheme(h.state, builtin, { api: h.api }), false);
  assert.strictEqual(h.mod.openUpdateDialog(h.state, builtin), false);
  assert.strictEqual(await h.mod.deleteTheme(h.state, builtin, { api: h.api }), false);
  assert.strictEqual(h.calls.filter(call => call.method !== 'GET').length, 0);
});

// --- preview ------------------------------------------------------------

test('preview accepts only same-origin immutable theme documents', async () => {
  const h = createHarness();
  await h.load();
  const row = h.state.themes[1];
  for (const bad of ['https://evil.example/preview.html', '//evil.example/preview.html',
    '/admin/admin.html', 'javascript:alert(1)', '']) {
    const theme = Object.assign({}, row, { previewLightUrl: bad, previewDarkUrl: bad });
    assert.strictEqual(h.mod.openPreview(h.state, theme, 'light'), false, 'accepted ' + bad);
    assert.match(h.state.actionError, /没有生成预览文档/);
  }
  assert.strictEqual(h.mod.openPreview(h.state, row, 'light'), true);
  assert.strictEqual(h.mod.previewSrc(h.state), '/web/themes/hash-a/preview.html?mode=light');
  assert.strictEqual(h.mod.setPreviewMode(h.state, 'dark'), true);
  assert.strictEqual(h.mod.previewSrc(h.state), '/web/themes/hash-a/preview.html?mode=dark');
  assert.strictEqual(h.mod.setPreviewMode(h.state, 'auto'), false);
  h.mod.closePreview(h.state);
  assert.strictEqual(h.mod.previewSrc(h.state), '');
});

test('a theme with only one preview variant opens the available one', async () => {
  const h = createHarness();
  await h.load();
  const row = Object.assign({}, h.state.themes[1], { previewLightUrl: '' });
  assert.strictEqual(h.mod.openPreview(h.state, row, 'light'), true);
  assert.strictEqual(h.state.preview.mode, 'dark');
  assert.strictEqual(h.mod.previewSrc(h.state), '/web/themes/hash-a/preview.html?mode=dark');
});

// --- refresh vs open dialogs -------------------------------------------

test('a background refresh cannot rewrite the revision an update dialog holds', async () => {
  const h = createHarness();
  await h.load();
  h.mod.openUpdateDialog(h.state, h.state.themes[1]);
  h.catalog = catalogDTO({
    themes: [builtinDTO(), themeDTO({ revision: 9, name: 'Renamed', enabled: false })],
  });
  await h.mod.refreshThemes(h.state, { api: h.api, silent: true });
  const row = h.state.themes[1];
  assert.strictEqual(row.revision, 4, 'the row revision under an open dialog must not move');
  assert.strictEqual(row.enabled, true, 'nor its enabled snapshot');
  assert.strictEqual(row.name, 'Renamed', 'other display fields still refresh');
  assert.strictEqual(h.state.dialog.expectedRevision, 4);
});

test('a vanished update target closes the dialog instead of keeping a stale revision', async () => {
  const h = createHarness();
  await h.load();
  h.mod.openUpdateDialog(h.state, h.state.themes[1]);
  h.mod.selectFile(h.state, { size: 512, name: 'a.zip' });
  h.catalog = catalogDTO({ themes: [builtinDTO()] });
  await h.mod.refreshThemes(h.state, { api: h.api, silent: true });
  assert.strictEqual(h.state.dialog, null);
  assert.match(h.state.actionError, /已不存在/);
  assert.strictEqual(h.mod.dialogReady(h.state), false);
});

// --- view helpers and DOM isolation ------------------------------------

test('view helpers format quota, byte sizes and row states', async () => {
  const h = createHarness();
  assert.strictEqual(h.mod.formatBytes(0), '0 B');
  assert.strictEqual(h.mod.formatBytes(1536), '1.5 KiB');
  assert.strictEqual(h.mod.formatBytes(2 * 1024 * 1024), '2.00 MiB');
  await h.load();
  assert.strictEqual(h.mod.quotaText(h.state), '1 / 20 个主题 · 2.0 KiB / 64.00 MiB');
  assert.strictEqual(h.mod.themeStateText(h.state.themes[0]), '内置');
  assert.strictEqual(h.mod.themeStateText(h.state.themes[1]), '已启用');
  assert.strictEqual(h.mod.themeStateText(Object.assign({}, h.state.themes[1], { enabled: false })), '已停用');
  assert.strictEqual(h.mod.themeStateText(Object.assign({}, h.state.themes[1], { available: false })), '不可用');
  assert.strictEqual(h.mod.isDefaultTheme(h.state, h.state.themes[0]), true);
  assert.strictEqual(h.mod.isDefaultTheme(h.state, h.state.themes[1]), false);
  assert.match(h.mod.themeStateClass(h.state.themes[1]), /green/);
});

test('the module never touches the admin DOM or localStorage', async () => {
  const trap = (name) => new Proxy({}, {
    get() { throw new Error('admin-theme.js must not touch ' + name); },
    set() { throw new Error('admin-theme.js must not touch ' + name); },
  });
  const h = createHarness(undefined, { document: trap('document'), localStorage: trap('localStorage'), window: trap('window') });
  await h.load();
  h.mod.openPreview(h.state, h.state.themes[1], 'light');
  h.mod.openUploadDialog(h.state);
  h.mod.selectFile(h.state, { size: 128, name: 'a.zip' });
  h.mod.closeDialog(h.state);
  h.mod.leave(h.state);
  assert.strictEqual(h.state.themes.length, 2);
});
