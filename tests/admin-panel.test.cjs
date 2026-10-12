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
      seen.push({ url: String(url), signal: opts && opts.signal });
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
