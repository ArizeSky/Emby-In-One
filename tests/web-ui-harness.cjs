'use strict';
/* Shared VM harness for the /web front-end tests.
 *
 * Loads the production scripts in the same order as web.html so app.js can
 * use EIOData / EIOUI / EIOPages / EIOPlayerUI / EIOComponents. The Vue stub
 * covers exactly the API the modules rely on: ref, computed, reactive,
 * nextTick, lifecycle hooks and createApp.
 */
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const SCRIPTS = ['data.js', 'ui.js', 'page-state.js', 'player-ui.js', 'library-state.js', 'ui-components.js', 'app.js'];

function makeStorage(initial = {}) {
  const store = new Map(Object.entries(initial));
  return {
    getItem: key => (store.has(key) ? store.get(key) : null),
    setItem: (key, value) => { store.set(key, String(value)); },
    removeItem: key => { store.delete(key); },
    clear: () => store.clear(),
    _store: store,
  };
}

function harness(options = {}) {
  const calls = [];
  const reply = options.reply || (() => ({ Items: [] }));
  const storage = options.storage || makeStorage({ eio_token: 'token', eio_user_id: 'user' });
  let app;
  // Lifecycle hooks are captured (not invoked) so tests can drive them; the
  // production scripts only register them during setup().
  const mountedHooks = [];
  const beforeUnmountHooks = [];

  const context = {
    URL, AbortController, DOMException, console, Math, JSON, Promise,
    setTimeout, clearTimeout, setInterval, clearInterval,
    location: { origin: 'http://localhost', hash: '#/home', reload() {} },
    localStorage: storage,
    lucide: { createIcons() {} },
    fetch: async (url, opts) => {
      const parsed = new URL(url, 'http://localhost');
      calls.push(parsed);
      const result = reply(parsed, opts);
      if (result && result.__status) {
        return { ok: false, status: result.__status, statusText: 'Error', text: async () => result.__body || '' };
      }
      return { ok: true, status: 200, json: async () => result };
    },
    Vue: {
      ref: value => ({ value }),
      computed: fn => ({ get value() { return fn(); } }),
      reactive: obj => obj,
      watch() {},
      onMounted(fn) { mountedHooks.push(fn); },
      onBeforeUnmount(fn) { beforeUnmountHooks.push(fn); },
      nextTick: fn => Promise.resolve().then(() => fn && fn()),
      createApp: opts => ({ component() {}, mount() { app = opts.setup(); } }),
    },
    document: { hidden: false, fullscreenElement: null, addEventListener() {}, removeEventListener() {} },
    IntersectionObserver: undefined,
    addEventListener() {}, removeEventListener() {},
  };
  context.window = context;
  context.window.innerWidth = options.width || 1440;
  vm.createContext(context);
  for (const file of SCRIPTS) {
    vm.runInContext(fs.readFileSync(path.join(__dirname, '..', 'public', 'web', file), 'utf8'), context);
  }
  return { app, context, calls, data: context.EIOData, storage, mountedHooks, beforeUnmountHooks };
}

const delay = ms => new Promise(resolve => setTimeout(resolve, ms));

module.exports = { harness, makeStorage, delay, SCRIPTS };
