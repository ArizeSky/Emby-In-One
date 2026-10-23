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
      if (result && result.__status) {
        resolve({ ok: false, status: result.__status, statusText: 'Error', headers: { get: () => null }, json: async () => result.__body || {} });
        return;
      }
      resolve({ ok: true, status: 200, headers: { get: () => null }, json: async () => result });
    }),
    Vue: {
      createApp: opts => ({ mount() { appOptions = opts; } }),
    },
  };
  context.window = context;
  vm.createContext(context);
  vm.runInContext(fs.readFileSync(path.join(__dirname, '..', 'public', 'admin.js'), 'utf8'), context);

  // Instantiate the options-API component far enough for the refresh flow.
  const vm2 = Object.assign(Object.create(null), appOptions.data());
  for (const [name, fn] of Object.entries(appOptions.methods)) vm2[name] = fn.bind(vm2);
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
