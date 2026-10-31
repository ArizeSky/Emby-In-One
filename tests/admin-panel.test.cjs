'use strict';
/* Admin panel refresh aborting.
 *
 * Page switches used to leave the previous page's refreshes running: a late
 * response overwrote whatever the new page had loaded, or emptied its lists
 * through the error path. Every refresh now carries an AbortController signal
 * that the page switch aborts.
 */
const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { makeStorage } = require('./web-ui-harness.cjs');

function adminHarness(options = {}) {
  const seen = [];
  const reply = options.reply || (() => ({}));
  let appOptions;

  const context = {
    console, Math, JSON, Promise, URL, AbortController, DOMException,
    setTimeout, clearTimeout, setInterval, clearInterval,
    localStorage: makeStorage({ eio_admin_token: 'admin-token' }),
    lucide: { createIcons() {} },
    alert() {}, confirm() { return true; },
    navigator: {},
    document: { createElement: () => ({ click() {}, style: {} }) },
    fetch: (url, opts) => new Promise((resolve, reject) => {
      seen.push({ url: String(url), signal: opts && opts.signal, method: opts && opts.method, body: opts && opts.body });
      const signal = opts && opts.signal;
      // Mirror the browser: an aborted signal rejects the request with an
      // AbortError, before or after it was issued.
      const onAbort = () => reject(new DOMException('The operation was aborted.', 'AbortError'));
      if (signal) {
        if (signal.aborted) { onAbort(); return; }
        signal.addEventListener('abort', onAbort);
      }
      const result = reply(String(url), opts);
      if (result && result.__delay) return; // only an abort settles a delayed reply
      const status = result && result.__status ? result.__status : 200;
      const payload = result && Object.prototype.hasOwnProperty.call(result, '__body') ? result.__body : result;
      const bodyText = typeof payload === 'string' ? payload : JSON.stringify(payload);
      resolve({
        ok: status >= 200 && status < 300,
        status,
        headers: { get: () => null },
        json: async () => JSON.parse(bodyText),
        text: async () => bodyText,
      });
    }),
    Vue: {
      createApp: opts => ({ mount() { appOptions = opts; } }),
    },
  };
  context.window = context;
  vm.createContext(context);
  // admin.html loads the K4 activity module before admin.js; mirror that order so
  // the harness exercises the same globals the browser gets.
  vm.runInContext(fs.readFileSync(path.join(__dirname, '..', 'public', 'admin-activity.js'), 'utf8'), context);
  vm.runInContext(fs.readFileSync(path.join(__dirname, '..', 'public', 'admin.js'), 'utf8'), context);

  // Instantiate the options-API component far enough for the refresh flow.
  const vm2 = Object.assign(Object.create(null), appOptions.data());
  for (const [name, fn] of Object.entries(appOptions.methods)) vm2[name] = fn.bind(vm2);
  // Computed properties are getters in Vue; expose them the same way so tests
  // can read the derived activity view directly.
  for (const [name, fn] of Object.entries(appOptions.computed || {})) {
    Object.defineProperty(vm2, name, { get: () => fn.call(vm2), enumerable: true, configurable: true });
  }
  vm2.$nextTick = fn => Promise.resolve().then(() => fn && fn());
  vm2.$refs = {};
  return { vm: vm2, options: appOptions, seen, context };
}

test('page switch aborts the in-flight refresh', async () => {
  const h = adminHarness();
  const vm = h.vm;
  vm.currentPage = 'servers';
  vm.refresh();

  const upstreamCall = h.seen.find(c => c.url.includes('/admin/api/upstream'));
  assert.ok(upstreamCall, 'the servers refresh issued its upstream request');
  assert.ok(upstreamCall.signal, 'the refresh request carried an AbortController signal');

  // Switching pages must abort the in-flight refresh immediately.
  h.options.watch.currentPage.call(vm, 'users');
  assert.strictEqual(upstreamCall.signal.aborted, true, 'the in-flight refresh was not aborted on page switch');
  if (vm._refreshTimer) clearTimeout(vm._refreshTimer);
});

test('a second refresh aborts the previous one', async () => {
  const h = adminHarness();
  const vm = h.vm;
  vm.currentPage = 'dashboard';
  vm.refresh();
  const first = h.seen.find(c => c.url.includes('/admin/api/status'));
  assert.ok(first && first.signal, 'the first refresh carried a signal');

  vm.refresh();
  assert.strictEqual(first.signal.aborted, true, 'a new refresh must abort the previous one');
  if (vm._refreshTimer) clearTimeout(vm._refreshTimer);
});

test('an aborted refresh does not empty the page lists', async () => {
  const reply = url => {
    if (url.includes('/admin/api/upstream')) return { __delay: true };
    return {};
  };
  const h = adminHarness({ reply });
  const vm = h.vm;
  vm.upstreamList = [{ id: 'srv-1', name: 'A' }];
  vm.currentPage = 'servers';
  const pending = vm.refreshServers(vm.refreshSignal());
  // Leave the page before the upstream response arrives.
  vm.abortRefresh();
  await pending;
  assert.deepStrictEqual(vm.upstreamList, [{ id: 'srv-1', name: 'A' }], 'an aborted refresh emptied the server list');
});


for (const status of [404, 500]) {
  test(`api reports empty HTTP ${status} responses as errors with status`, async () => {
    const h = adminHarness({ reply: () => ({ __status: status, __body: '' }) });
    await assert.rejects(h.vm.api('/test'), error => {
      assert.strictEqual(error.status, status);
      assert.match(error.message, new RegExp(`HTTP ${status}`));
      return true;
    });
  });
}

test('api preserves a JSON error body and HTTP status', async () => {
  const h = adminHarness({ reply: () => ({ __status: 400, __body: { error: 'invalid setting' } }) });
  await assert.rejects(h.vm.api('/test'), error => {
    assert.strictEqual(error.status, 400);
    assert.strictEqual(error.message, 'invalid setting');
    return true;
  });
});

test('api reports status for JSON null error bodies', async () => {
  const h = adminHarness({ reply: () => ({ __status: 502, __body: null }) });
  await assert.rejects(h.vm.api('/test'), error => error.status === 502 && /HTTP 502/.test(error.message));
});

test('api accepts 204 but rejects an empty non-204 success response', async () => {
  const h = adminHarness({ reply: () => ({ __status: 204, __body: '' }) });
  assert.strictEqual((await h.vm.api('/test')).success, true);

  const empty = adminHarness({ reply: () => ({ __status: 200, __body: '' }) });
  await assert.rejects(empty.vm.api('/test'), error => error.status === 200 && /empty response/.test(error.message));
});

test('first settings load failure blocks saving and retry loads settings', async () => {
  let attempts = 0;
  const h = adminHarness({ reply: url => {
    if (!url.includes('/admin/api/settings')) return {};
    attempts++;
    return attempts === 1
      ? { __status: 500, __body: '' }
      : { serverName: 'Recovered', timeouts: { api: 30000 } };
  } });
  const vm = h.vm;
  await vm.refreshSettings();
  assert.strictEqual(vm.settingsLoadState, 'error');
  assert.strictEqual(vm.settingsLoadedOnce, false);
  await vm.saveSettings();
  assert.ok(!h.seen.some(call => call.method === 'PUT' && call.url.includes('/admin/api/settings')),
    'an initial load failure must not issue a settings save');

  await vm.refreshSettings();
  assert.strictEqual(vm.settingsLoadState, 'loaded');
  assert.strictEqual(vm.settingsError, '');
  assert.strictEqual(vm.settings.serverName, 'Recovered');
});

test('failed settings reload keeps the last successful data until retry succeeds', async () => {
  let attempts = 0;
  const h = adminHarness({ reply: url => {
    if (!url.includes('/admin/api/settings')) return {};
    attempts++;
    if (attempts === 1) return { serverName: 'Known', timeouts: { api: 25000 } };
    if (attempts === 2) return { __status: 500, __body: '' };
    return { serverName: 'Retried', timeouts: { api: 15000 } };
  } });
  const vm = h.vm;
  await vm.refreshSettings();
  await vm.refreshSettings();
  assert.strictEqual(vm.settingsLoadState, 'error');
  assert.strictEqual(vm.settings.serverName, 'Known');
  assert.strictEqual(vm.settings.timeouts.api, 25000);
  await vm.refreshSettings();
  assert.strictEqual(vm.settingsLoadState, 'loaded');
  assert.strictEqual(vm.settings.serverName, 'Retried');
});

test('stale settings responses cannot overwrite the latest request', async () => {
  const h = adminHarness();
  const vm = h.vm;
  const pending = [];
  vm.api = () => new Promise(resolve => pending.push(resolve));
  const oldRequest = vm.refreshSettings();
  const newRequest = vm.refreshSettings();
  assert.strictEqual(vm.settingsLoadState, 'loading');
  pending[1]({ serverName: 'newest', timeouts: { api: 20000 } });
  await newRequest;
  pending[0]({ serverName: 'stale', timeouts: { api: 10000 } });
  await oldRequest;
  assert.strictEqual(vm.settings.serverName, 'newest');
  assert.strictEqual(vm.settingsLoadState, 'loaded');
});

test('failed list refresh preserves known entries and reports the error', async () => {
  const h = adminHarness({ reply: url => {
    if (url.includes('/admin/api/upstream')) return { __status: 500, __body: '' };
    if (url.includes('/admin/api/proxies')) return [];
    if (url.includes('/admin/api/ua')) return { resident: [], servers: [] };
    return {};
  } });
  const vm = h.vm;
  vm.upstreamList = [{ id: 'known', name: 'Known server' }];
  await vm.refreshServers();
  assert.strictEqual(vm.upstreamList[0].id, 'known');
  assert.match(vm.listErrors.upstream, /HTTP 500/);
});
// --- Aggregation switch ---------------------------------------------------

test('aggregation switch sends an explicit desired state', async () => {
  const h = adminHarness({
    reply: url => url.includes('/aggregation') ? { success: true, id: 's1', aggregationEnabled: false } : {},
  });
  const vm = h.vm;
  vm.currentPage = 'logs'; // no post-toggle page refresh
  vm.stats = { upstream: [{ id: 's1', name: 'A', aggregationEnabled: true }] };
  vm.upstreamList = [{ id: 's1', name: 'A', aggregationEnabled: true }];
  await vm.setServerAggregation(vm.upstreamList[0]);
  const call = h.seen.find(c => c.url.includes('/aggregation'));
  assert.ok(call, 'a PATCH was issued');
  assert.strictEqual(call.method, 'PATCH');
  assert.deepStrictEqual(JSON.parse(call.body), { aggregationEnabled: false });
  assert.ok(!String(call.body).includes('password'));
  assert.strictEqual(vm.upstreamList[0].aggregationEnabled, false);
  assert.strictEqual(vm.aggregationPending['s1'], undefined);
});

test('aggregation switch sends true for a disabled row', async () => {
  const h = adminHarness({
    reply: url => url.includes('/aggregation') ? { success: true, id: 's2', aggregationEnabled: true } : {},
  });
  const vm = h.vm;
  vm.currentPage = 'logs';
  vm.stats = { upstream: [{ id: 's2', name: 'B', aggregationEnabled: false }] };
  vm.upstreamList = [{ id: 's2', name: 'B', aggregationEnabled: false }];
  await vm.setServerAggregation(vm.upstreamList[0]);
  const call = h.seen.find(c => c.url.includes('/aggregation'));
  assert.deepStrictEqual(JSON.parse(call.body), { aggregationEnabled: true });
  assert.strictEqual(vm.upstreamList[0].aggregationEnabled, true);
});

test('aggregation switch blocks duplicate clicks while pending', async () => {
  const h = adminHarness({ reply: url => url.includes('/aggregation') ? { __delay: true } : {} });
  const vm = h.vm;
  vm.currentPage = 'logs';
  vm.stats = { upstream: [] };
  vm.upstreamList = [{ id: 's1', name: 'A', aggregationEnabled: true }];
  const first = vm.setServerAggregation(vm.upstreamList[0]); // stays in flight
  await vm.setServerAggregation(vm.upstreamList[0]); // blocked by pending guard
  const patches = h.seen.filter(c => c.url.includes('/aggregation'));
  assert.strictEqual(patches.length, 1, 'the duplicate click issued no request');
  assert.strictEqual(vm.aggregationPending['s1'], true, 'pending stays set while in flight');
  void first;
});

test('aggregation failure preserves the row state', async () => {
  const h = adminHarness({
    reply: url => url.includes('/aggregation') ? { __status: 500, __body: { error: 'nope' } } : {},
  });
  const vm = h.vm;
  vm.currentPage = 'logs';
  vm.stats = { upstream: [{ id: 's1', name: 'A', aggregationEnabled: true }] };
  vm.upstreamList = [{ id: 's1', name: 'A', aggregationEnabled: true }];
  await vm.setServerAggregation(vm.upstreamList[0]);
  assert.strictEqual(vm.upstreamList[0].aggregationEnabled, true, 'row state unchanged after failure');
  assert.strictEqual(vm.aggregationPending['s1'], undefined, 'pending cleared after failure');
});

test('server edit does not submit aggregation state', async () => {
  const h = adminHarness();
  const vm = h.vm;
  vm.editID = 's1';
  vm.editIndex = 0;
  vm.uaResident = [];
  vm.serverForm = {
    name: 'A', url: 'http://x', streamingUrlsText: '', spoofClient: 'passthrough',
    uaProfileId: '', aggregationEnabled: true,
  };
  await vm.saveServer();
  const put = h.seen.find(c => c.method === 'PUT' && c.url.includes('/admin/api/upstream'));
  assert.ok(put, 'the edit issued a PUT');
  assert.ok(!('aggregationEnabled' in JSON.parse(put.body)), 'edit payload must not carry aggregationEnabled');
});

test('aggregation success aborts stale refreshes', async () => {
  const h = adminHarness({
    reply: url => url.includes('/aggregation') ? { success: true, aggregationEnabled: false } : { __delay: true },
  });
  const vm = h.vm;
  vm.currentPage = 'logs';
  vm.stats = { upstream: [{ id: 's1', name: 'A', aggregationEnabled: true }] };
  vm.upstreamList = [{ id: 's1', name: 'A', aggregationEnabled: true }];
  const signal = vm.refreshSignal();
  const pending = vm.refreshServers(signal); // upstream GET stays in flight
  const upstreamCall = h.seen.find(c => c.url.includes('/admin/api/upstream') && !c.url.includes('/aggregation'));
  assert.ok(upstreamCall && upstreamCall.signal && !upstreamCall.signal.aborted);
  await vm.setServerAggregation({ id: 's1', name: 'A', aggregationEnabled: true });
  assert.strictEqual(upstreamCall.signal.aborted, true, 'the stale refresh was aborted');
  await pending.catch(() => {});
  if (vm._refreshTimer) clearTimeout(vm._refreshTimer);
});

// --- UA management --------------------------------------------------------

test('ua save blocks duplicate submits while pending', async () => {
  const h = adminHarness({ reply: url => url.includes('/admin/api/ua/resident') ? { __delay: true } : {} });
  const vm = h.vm;
  vm.uaSaveDialog = { rename: false, recentId: 'r1', name: 'N' };
  const first = vm.saveRecentUA(); // stays in flight
  await vm.saveRecentUA(); // blocked by pending guard
  const posts = h.seen.filter(c => c.method === 'POST' && c.url.includes('/admin/api/ua/resident'));
  assert.strictEqual(posts.length, 1, 'duplicate submit issued no request');
  assert.strictEqual(vm.uaPending.save, true);
  void first;
});

test('ua save failure keeps the dialog and selection', async () => {
  const h = adminHarness({ reply: url => url.includes('/admin/api/ua/resident') ? { __status: 500, __body: { error: 'nope' } } : {} });
  const vm = h.vm;
  vm.uaSaveDialog = { rename: false, recentId: 'r1', name: 'My UA' };
  await vm.saveRecentUA();
  assert.ok(vm.uaSaveDialog, 'the dialog survived the failure');
  assert.strictEqual(vm.uaSaveDialog.name, 'My UA', 'the user selection survived');
  assert.strictEqual(vm.uaPending.save, false, 'pending cleared after failure');
});

test('ua assignments build explicit increments with premises', async () => {
  const h = adminHarness({ reply: () => ({ success: true }) });
  const vm = h.vm;
  vm.uaServers = [
    { id: 's1', name: 'A', spoofClient: 'passthrough', uaProfileId: '' },
    { id: 's2', name: 'B', spoofClient: 'resident', uaProfileId: 'p1' },
  ];
  vm.uaAssignmentDialog = { id: 'p1', name: 'P', revision: 3, rows: [
    { id: 's1', name: 'A', currentSpoof: 'passthrough', currentProfile: '', bound: false, checked: true },
    { id: 's2', name: 'B', currentSpoof: 'resident', currentProfile: 'p1', bound: true, checked: false },
  ] };
  await vm.saveUAAssignments();
  const post = h.seen.find(c => c.method === 'POST' && c.url.includes('/assignments'));
  assert.ok(post, 'the assignment POST was issued');
  const body = JSON.parse(post.body);
  assert.deepStrictEqual(body.assignServerIds, ['s1']);
  assert.deepStrictEqual(body.releaseServerIds, ['s2']);
  assert.strictEqual(body.expectedProfileRevision, 3);
  assert.deepStrictEqual(body.expectedBindings, [
    { serverId: 's1', spoofClient: 'passthrough', uaProfileId: '' },
    { serverId: 's2', spoofClient: 'resident', uaProfileId: 'p1' },
  ]);
  vm.stopUAReconnectPoll();
});

test('ua delete guard blocks a second click while pending', async () => {
  const h = adminHarness({ reply: url => url.includes('/admin/api/ua/resident/') && !url.includes('/replace') && !url.includes('/assignments') ? { __delay: true } : {} });
  const vm = h.vm;
  const first = vm.deleteResidentUA({ id: 'p1', name: 'P', revision: 2 });
  await vm.deleteResidentUA({ id: 'p1', name: 'P', revision: 2 });
  const deletes = h.seen.filter(c => c.method === 'DELETE');
  assert.strictEqual(deletes.length, 1, 'the duplicate delete issued no request');
  void first;
});

test('ua reconnect poll starts and stops', async () => {
  const h = adminHarness({ reply: () => ({}) });
  const vm = h.vm;
  vm.isLoggedIn = true;
  vm.startUAReconnectPoll();
  assert.ok(vm._uaPollTimer, 'the poll timer was created');
  vm.stopUAReconnectPoll();
  assert.strictEqual(vm._uaPollTimer, null);
});

test('aggregation success refreshes the servers page', async () => {
  const h = adminHarness({ reply: url => url.includes('/aggregation')
    ? { success: true, aggregationEnabled: false }
    : (url.includes('/admin/api/upstream') ? [{ id: 's1', name: 'A', aggregationEnabled: false }] : {}) });
  const vm = h.vm;
  vm.currentPage = 'servers';
  vm.stats = { upstream: [{ id: 's1', name: 'A', aggregationEnabled: true }] };
  vm.upstreamList = [{ id: 's1', name: 'A', aggregationEnabled: true }];
  await vm.setServerAggregation(vm.upstreamList[0]);
  assert.strictEqual(vm.upstreamList[0].aggregationEnabled, false);
  assert.ok(h.seen.some(c => c.url.includes('/admin/api/upstream') && !c.url.includes('/aggregation')),
    'the servers page re-fetched the upstream list');
  if (vm._refreshTimer) clearTimeout(vm._refreshTimer);
});

test('editing and saving an existing server preserves separate streaming URLs', async () => {
  const h = adminHarness();
  const vm = h.vm;
  const urls = ['https://stream-a.example', 'https://stream-b.example'];
  vm.editServer({ id: 'srv', streamingUrls: urls, spoofClient: 'passthrough' });
  await vm.saveServer();
  const save = h.seen.find(c => c.url.includes('/admin/api/upstream') && c.method === 'PUT');
  assert.ok(save, 'saving an existing server issued a PUT');
  const body = JSON.parse(save.body);
  assert.deepStrictEqual(Array.from(body.streamingUrls), urls,
    'opening and saving an existing server must preserve its separate streaming URLs');
  if (vm._refreshTimer) clearTimeout(vm._refreshTimer);
});

// --- Upstream viewing activity (K4) ---------------------------------------

function activityDTO(overrides = {}) {
  return Object.assign({
    serverNow: '2025-01-01T00:00:00Z', available: true, state: 'normal',
    enabled: true, intervalUnit: 'days', intervalValue: 30, minWatchSeconds: 60,
    revision: 7, lastActivityAt: '2024-12-30T10:00:00Z', lastQualifiedAt: '2024-12-30T09:00:00Z',
    manualReferenceAt: null, referenceAt: '2024-12-30T09:00:00Z', referenceSource: 'observed',
    dueAt: '2025-01-29T09:00:00Z', remainingRatio: 0.94,
  }, overrides);
}

test('servers page filters/sorts a derived view without touching upstreamList', () => {
  const h = adminHarness();
  const vm = h.vm;
  vm.upstreamList = [
    { id: 'a', name: 'A', activity: activityDTO({ state: 'normal' }) },
    { id: 'b', name: 'B', activity: activityDTO({ state: 'overdue', dueAt: '2024-12-01T00:00:00Z' }) },
    { id: 'c', name: 'C', activity: activityDTO({ state: 'warning' }) },
  ];
  const original = vm.upstreamList.map(row => row.id);

  vm.activitySort = 'risk';
  // activityRows comes from the VM realm module; Array.from re-homes it before
  // the strict deep compare.
  assert.deepStrictEqual(Array.from(vm.activityRows.map(row => row.server.id)), ['b', 'c', 'a']);
  assert.strictEqual(vm.activityReorderEnabled, false);

  vm.activityFilter = 'attention';
  assert.deepStrictEqual(Array.from(vm.activityRows.map(row => row.server.id)), ['b', 'c']);
  assert.deepStrictEqual(Array.from(vm.activityRows.map(row => row.index)), [1, 2],
    'reorder/edit keep using original indexes, not the derived row position');

  vm.activityFilter = 'all';
  vm.activitySort = 'default';
  assert.deepStrictEqual(Array.from(vm.activityRows.map(row => row.server.id)), original);
  assert.strictEqual(vm.activityReorderEnabled, true);
  assert.deepStrictEqual(vm.upstreamList.map(row => row.id), original,
    'the source list order must never change for a display-only filter/sort');
});

test('activity dialog opens from the list and saves with the server revision', async () => {
  const h = adminHarness({ reply: (url, opts) => {
    if (url.includes('/activity')) {
      return opts && opts.method === 'PUT' ? activityDTO({ revision: 8, intervalValue: 45 }) : activityDTO();
    }
    return {};
  } });
  const vm = h.vm;
  vm.currentPage = 'servers';
  vm.openActivity({ id: 's1', name: 'A' });
  await new Promise(resolve => setTimeout(resolve, 0));
  assert.strictEqual(vm.activityDialog.serverId, 's1');
  assert.strictEqual(vm.activityDialog.loaded, true);
  assert.match(vm.activityDialog.notice || '', /^$/);
  assert.strictEqual(vm.activityCanSave, true);

  vm.activityDialog.form.intervalValue = 45;
  await vm.saveActivitySettings();
  const put = h.seen.find(call => call.method === 'PUT' && call.url.includes('/activity'));
  assert.ok(put, 'the dialog issued the activity PUT');
  const body = JSON.parse(put.body);
  assert.strictEqual(body.expectedRevision, 7);
  assert.strictEqual(body.intervalValue, 45);
  assert.strictEqual(vm.activityDialog.form.expectedRevision, 8);
  assert.strictEqual(vm.activityDialog.savedNotice, '保活设置已保存');
  assert.ok(h.seen.some(call => call.url.includes('/admin/api/upstream') && !call.url.includes('/activity')),
    'a successful save refreshes the server list');
  vm.closeActivity();
});

test('activity dialog falls back to the row index when a server has no id yet', async () => {
  const h = adminHarness({ reply: () => activityDTO() });
  const vm = h.vm;
  vm.openActivity({ index: 2, name: 'Unpersisted' });
  await new Promise(resolve => setTimeout(resolve, 0));
  assert.strictEqual(vm.activityDialog.serverId, 2);
  assert.ok(h.seen.some(call => call.url.includes('/admin/api/upstream/2/activity')));
  vm.closeActivity();
});

test('closing the activity dialog aborts its request and keeps it closed', async () => {
  const h = adminHarness({ reply: () => ({ __delay: true }) });
  const vm = h.vm;
  vm.currentPage = 'servers';
  vm.openActivity({ id: 's1', name: 'A' });
  // The GET is issued from a promise microtask; flush once so the recorded call
  // carries the AbortController signal that closing must abort.
  await new Promise(resolve => setTimeout(resolve, 0));
  const call = h.seen.find(entry => entry.url.includes('/activity'));
  assert.ok(call && call.signal, 'the GET carried an AbortController signal');
  vm.closeActivity();
  assert.strictEqual(call.signal.aborted, true);
  await new Promise(resolve => setTimeout(resolve, 0));
  assert.strictEqual(vm.activityDialog.open, false);
  assert.strictEqual(vm.activityDialog.dto, null);
});

test('leaving the servers page or logging out closes the activity dialog', async () => {
  const h = adminHarness({ reply: () => activityDTO() });
  const vm = h.vm;
  vm.currentPage = 'servers';
  vm.isLoggedIn = true;
  vm.openActivity({ id: 's1', name: 'A' });
  await new Promise(resolve => setTimeout(resolve, 0));
  assert.strictEqual(vm.activityDialog.open, true);

  h.options.watch.currentPage.call(vm, 'dashboard');
  assert.strictEqual(vm.activityDialog.open, false, 'leaving the servers page must close the dialog');
  if (vm._refreshTimer) clearTimeout(vm._refreshTimer);

  vm.currentPage = 'servers';
  vm.openActivity({ id: 's1', name: 'A' });
  await new Promise(resolve => setTimeout(resolve, 0));
  vm.logout();
  assert.strictEqual(vm.isLoggedIn, false);
  assert.strictEqual(vm.activityDialog.open, false);
  assert.strictEqual(vm.activityDialog.dto, null, 'logout must not leave activity data for the next session');
});

test('a 401 on the activity request logs out and drops the dialog data', async () => {
  const h = adminHarness({ reply: () => ({ __status: 401, __body: { error: 'unauthorized' } }) });
  const vm = h.vm;
  vm.currentPage = 'servers';
  vm.isLoggedIn = true;
  vm.openActivity({ id: 's1', name: 'A' });
  await new Promise(resolve => setTimeout(resolve, 0));
  assert.strictEqual(vm.isLoggedIn, false);
  assert.strictEqual(vm.activityDialog.open, false);
  assert.strictEqual(vm.activityDialog.dto, null);
});
