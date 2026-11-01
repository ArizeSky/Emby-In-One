'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const publicDir = path.join(path.resolve(__dirname, '..'), 'public');
const html = fs.readFileSync(path.join(publicDir, 'admin.html'), 'utf8');

function makeStorage(initial = {}) {
  const values = new Map(Object.entries(initial));
  return {
    getItem: key => (values.has(key) ? values.get(key) : null),
    setItem: (key, value) => values.set(key, String(value)),
    removeItem: key => values.delete(key),
    _values: values,
  };
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

// Minimal harness: real panel scripts in one VM, createApp captured, no net/DOM.
function loadPanel() {
  let options;
  const timers = [];
  const storage = makeStorage({ eio_admin_token: 'test-token' });
  const sandbox = {
    console, AbortController, localStorage: storage,
    setTimeout: (fn, ms) => { timers.push({ fn, ms }); return timers.length; },
    clearTimeout: () => {},
    fetch: () => { throw new Error('no network in tests'); },
    navigator: { clipboard: { writeText: async () => {} } },
    alert: () => {}, confirm: () => true, lucide: { createIcons: () => {} },
    Vue: { createApp: value => { options = value; return { mount() {} }; } },
  };
  sandbox.globalThis = sandbox;
  vm.createContext(sandbox);
  for (const file of ['admin-activity.js', 'admin-theme.js', 'admin.js']) {
    vm.runInContext(fs.readFileSync(path.join(publicDir, file), 'utf8'), sandbox, { filename: file });
  }
  assert.ok(options, 'admin.js must call createApp');
  const instance = Object.assign({}, options.data(), options.methods);
  instance.$nextTick = fn => { if (fn) fn(); };
  instance.stopUAReconnectPoll = () => {};
  instance.closeActivity = () => {};
  instance.themeLeave = () => {};
  instance.themeReset = () => {};
  instance.runWatch = page => options.watch.currentPage.call(instance, page);
  instance._timers = timers;
  instance._storage = storage;
  return instance;
}
test('筛选标签与 UA 模板首载合约', () => {
  assert.match(html, /<span class="text-slate-500">筛选<\/span>/);
  assert.doesNotMatch(html, /风险筛选/);
  assert.match(html, /v-if="!uaLoadedOnce && uaLoading"/);
  assert.match(html, /v-else-if="!uaLoadedOnce && uaError"/);
  const firstLoad = html.slice(html.indexOf('!uaLoadedOnce && uaLoading'), html.indexOf('<template v-else>'));
  assert.ok(firstLoad.length > 0);
  assert.doesNotMatch(firstLoad, /暂无常驻 UA/, 'empty state must not render before a successful load');
  assert.ok(html.indexOf('暂无常驻 UA') > html.indexOf('<template v-else>'));
});
test('主题刷新/上传按钮使用确定的内联 SVG', () => {
  const refresh = html.match(/<button @click="themeRefresh\(\)"[^>]*:disabled="themeAdmin\.loading"[^>]*>[\s\S]*?<span>刷新<\/span>[\s\S]*?<\/button>/)[0];
  const upload = html.match(/<button @click="themeOpenUpload\(\)"[\s\S]*?<\/button>/)[0];
  for (const button of [refresh, upload]) {
    assert.match(button, /<svg[^>]*viewBox="0 0 24 24"/);
    assert.match(button, /width="16"/);
    assert.match(button, /height="16"/);
    assert.match(button, /fill="none"/);
    assert.match(button, /stroke="currentColor"/);
    assert.match(button, /aria-hidden="true"/);
    assert.match(button, /focusable="false"/);
    assert.doesNotMatch(button, /data-lucide/);
  }
  assert.match(refresh, /:disabled="themeAdmin\.loading"/);
  assert.match(upload, /:disabled="!themeCanInstall"/);
});
test('进入 UA 页同步 loading，首次成功/空/失败状态正确', async () => {
  const panel = loadPanel();
  panel.runWatch('ua');
  assert.equal(panel.uaLoading, true);
  assert.equal(panel.uaLoadedOnce, false);
  assert.equal(panel._timers.length, 1, 'refresh must stay deferred');
  const empty = deferred();
  panel.api = () => empty.promise;
  const pending = panel.refreshUAs();
  assert.equal(panel.uaLoading, true);
  empty.resolve({ recent: [], resident: [], servers: [] });
  await pending;
  assert.equal(panel.uaLoadedOnce, true);
  assert.equal(panel.uaLoading, false);
  assert.equal(panel.uaError, '');
  assert.deepEqual(panel.uaResident, []);
  const nonempty = deferred();
  panel.api = () => nonempty.promise;
  const next = panel.refreshUAs();
  nonempty.resolve({ resident: [{ id: 'resident-1' }], recent: [{ id: 'recent-1' }] });
  await next;
  assert.deepEqual(panel.uaResident.map(row => row.id), ['resident-1']);
  const failed = loadPanel();
  const error = deferred();
  failed.api = () => error.promise;
  const failing = failed.refreshUAs();
  error.reject(new Error('first load failed'));
  await failing;
  assert.equal(failed.uaLoadedOnce, false);
  assert.equal(failed.uaLoading, false);
  assert.equal(failed.uaError, 'first load failed');
  assert.deepEqual(Array.from(failed.uaResident), []);
});
test('后台刷新保留已加载数据，刷新失败保留数据并暴露错误', async () => {
  const panel = loadPanel();
  const first = deferred();
  panel.api = () => first.promise;
  const load = panel.refreshUAs();
  first.resolve({ resident: [{ id: 'old' }] });
  await load;
  const background = deferred();
  panel.api = () => background.promise;
  const refreshing = panel.refreshUAs();
  assert.equal(panel.uaLoading, true);
  assert.deepEqual(panel.uaResident.map(row => row.id), ['old']);
  background.resolve({ resident: [{ id: 'new' }] });
  await refreshing;
  assert.deepEqual(panel.uaResident.map(row => row.id), ['new']);
  const failure = deferred();
  panel.api = () => failure.promise;
  const failedRefresh = panel.refreshUAs();
  failure.reject(new Error('refresh failed'));
  await failedRefresh;
  assert.deepEqual(panel.uaResident.map(row => row.id), ['new']);
  assert.equal(panel.uaError, 'refresh failed');
  assert.equal(panel.uaLoadedOnce, true);
  assert.equal(panel.uaLoading, false);
});
test('A/B 错序响应与旧 finally 不清新 loading', async () => {
  const panel = loadPanel();
  const a = deferred();
  const b = deferred();
  const queue = [a, b];
  panel.api = () => queue.shift().promise;
  const first = panel.refreshUAs();
  const second = panel.refreshUAs();
  a.resolve({ resident: [{ id: 'A' }] });
  await first;
  assert.deepEqual(Array.from(panel.uaResident), []);
  assert.equal(panel.uaLoading, true, 'stale finally must not clear the new request loading');
  b.resolve({ resident: [{ id: 'B' }] });
  await second;
  assert.deepEqual(panel.uaResident.map(row => row.id), ['B']);
  assert.equal(panel.uaLoading, false);
});

test('被 abort 但忽略 signal 的迟到响应不写列表', async () => {
  const panel = loadPanel();
  const late = deferred();
  panel.api = () => late.promise;
  const controller = new AbortController();
  const pending = panel.refreshUAs(controller.signal);
  controller.abort();
  late.resolve({ resident: [{ id: 'late' }] });
  await pending;
  assert.deepEqual(Array.from(panel.uaResident), []);
  assert.equal(panel.uaLoadedOnce, false);
  assert.equal(panel.uaLoading, true, 'an aborted request no longer owns the loading state');
});

test('离开 UA 后快速返回：旧 abort 的 finally 不清新 loading', async () => {
  const panel = loadPanel();
  const late = deferred();
  panel.api = () => late.promise;
  const pending = panel.refreshUAs(panel.refreshSignal());
  panel.runWatch('servers');
  panel.runWatch('ua');
  assert.equal(panel.uaLoading, true);
  assert.equal(panel._timers.length, 2, 'both page switches stay deferred');
  late.reject(Object.assign(new Error('aborted'), { name: 'AbortError' }));
  await pending;
  assert.equal(panel.uaLoading, true, 'old aborted finally must not clear the re-entry loading');
  assert.equal(panel.uaLoadedOnce, false);
});

test('logout 后旧账号迟到响应不写 UA 缓存', async () => {
  const panel = loadPanel();
  const late = deferred();
  panel.api = () => late.promise;
  const pending = panel.refreshUAs();
  panel.logout();
  late.resolve({ resident: [{ id: 'old-account' }] });
  await pending;
  assert.deepEqual(Array.from(panel.uaResident), []);
  assert.equal(panel.uaLoadedOnce, false);
  assert.equal(panel.uaLoading, false);
  assert.equal(panel._storage.getItem('eio_admin_token'), null);
});

test('旧 UA options 预取迟到不覆盖新 refreshUAs 结果', async () => {
  const panel = loadPanel();
  const optionsLate = deferred();
  panel.api = () => optionsLate.promise;
  const optionsRequest = panel.refreshServerUAOptions();
  const fresh = deferred();
  panel.api = () => fresh.promise;
  const refresh = panel.refreshUAs();
  fresh.resolve({ resident: [{ id: 'fresh' }], servers: [{ id: 'fresh-server' }] });
  await refresh;
  optionsLate.resolve({ resident: [{ id: 'stale' }], servers: [{ id: 'stale-server' }] });
  await optionsRequest;
  assert.deepEqual(panel.uaResident.map(row => row.id), ['fresh']);
  assert.deepEqual(panel.uaServers.map(row => row.id), ['fresh-server']);
});

test('旧 UA options 预取在 logout 后迟到不得回填', async () => {
  const panel = loadPanel();
  const late = deferred();
  panel.api = () => late.promise;
  const pending = panel.refreshServerUAOptions();
  panel.logout();
  late.resolve({ resident: [{ id: 'old-account' }], servers: [{ id: 'old-server' }] });
  await pending;
  assert.deepEqual(Array.from(panel.uaResident), []);
  assert.deepEqual(Array.from(panel.uaServers), []);
  assert.equal(panel._storage.getItem('eio_admin_token'), null);
});

test('refreshServers 的 UA 预取在 abort 后即使 api 忽略 signal 也不回填', async () => {
  const panel = loadPanel();
  const upstream = deferred();
  const proxies = deferred();
  const ua = deferred();
  let uaRequested = false;
  panel.api = path => {
    if (path === '/admin/api/upstream') return upstream.promise;
    if (path === '/admin/api/proxies') return proxies.promise;
    if (path === '/admin/api/ua') { uaRequested = true; return ua.promise; }
    throw new Error('unexpected request: ' + path);
  };
  const controller = new AbortController();
  const pending = panel.refreshServers(controller.signal);
  upstream.resolve([{ id: 'server-1' }]);
  await new Promise(resolve => setImmediate(resolve));
  proxies.resolve([]);
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(uaRequested, true, 'UA 预取应在列表与代理请求之后发出');
  controller.abort();
  ua.resolve({ resident: [{ id: 'late' }], servers: [{ id: 'late-server' }] });
  await pending;
  assert.deepEqual(Array.from(panel.uaResident), []);
  assert.deepEqual(Array.from(panel.uaServers), []);
  assert.deepEqual(Array.from(panel.upstreamList).map(row => row.id), ['server-1']);
});
