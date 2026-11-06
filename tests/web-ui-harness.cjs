'use strict';
/* Shared VM harness for the /web front-end tests.
 *
 * Loads the production scripts in the same order as web.html so app.js can
 * use EIOData / EIOUI / EIOPages / EIOPlayerUI / EIOComponents. The Vue stub
 * covers exactly the API the modules rely on: ref, computed, reactive,
 * nextTick, lifecycle hooks and createApp.
 *
 * `options.skipTelemetry` omits player-telemetry-ui.js from the load order
 * (imitating a blocked/stale script); the default loads every script.
 */
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// Load order mirrors web.html. library-query.js feeds library-state/page-state,
// home-library-rows.js must exist before page-state.js creates the controller,
// home-library-row.js must exist before app.js registers the component, and
// playback-session.js must exist before app.js creates the playback session,
// and playback-identity.js must exist before playback-session.js reads it.
const SCRIPTS = ['data.js', 'ui.js', 'library-query.js', 'home-library-rows.js', 'page-state.js', 'player-ui.js', 'library-state.js', 'resume-card.js', 'ui-components.js', 'home-library-row.js', 'playback-orchestrator.js', 'player-progress.js', 'player-buffer.js', 'player-telemetry-ui.js', 'theme-player-controls.js', 'theme.js', 'theme-transport.js', 'appearance.js', 'player-rate-ui.js', 'playback-identity.js', 'playback-session.js', 'app.js'];

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
  const components = {};
  // Lifecycle hooks are captured (not invoked) so tests can drive them; the
  // production scripts only register them during setup().
  const mountedHooks = [];
  const beforeUnmountHooks = [];
  const listeners = { window: {}, document: {} };

  const documentElement = {
    dataset: {},
    style: {
      _props: {},
      setProperty(name, value) { this._props[name] = value; },
      removeProperty(name) { delete this._props[name]; },
    },
  };

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
      createApp: opts => ({
        component(name, definition) { components[name] = definition; },
        mount() { app = opts.setup(); },
      }),
    },
    document: {
      hidden: false, fullscreenElement: null, documentElement,
      addEventListener(type, fn) { (listeners.document[type] ||= []).push(fn); },
      removeEventListener(type, fn) {
        const list = listeners.document[type] || [];
        const index = list.indexOf(fn);
        if (index >= 0) list.splice(index, 1);
      },
    },
    IntersectionObserver: undefined,
    addEventListener(type, fn) { (listeners.window[type] ||= []).push(fn); },
    removeEventListener(type, fn) {
      const list = listeners.window[type] || [];
      const index = list.indexOf(fn);
      if (index >= 0) list.splice(index, 1);
    },
  };
  if (options.matchMedia) context.matchMedia = options.matchMedia;
  if (options.EIOThemeDeps) context.EIOThemeDeps = options.EIOThemeDeps;
  context.window = context;
  context.window.innerWidth = options.width || 1440;
  vm.createContext(context);
  for (const file of SCRIPTS) {
    if (file === 'player-telemetry-ui.js' && options.skipTelemetry) continue;
    if (file === 'app.js' && options.beforeApp) options.beforeApp(context);
    const fullPath = path.join(__dirname, '..', 'public', 'web', file);
    vm.runInContext(fs.readFileSync(fullPath, 'utf8'), context);
  }
  return {
    app, context, calls, components, resumeCard: context.EIOResumeCard,
    data: context.EIOData, storage, mountedHooks, beforeUnmountHooks, listeners, documentElement,
    progress: context.EIOPlayerProgress,
    bufferModule: context.EIOPlayerBuffer,
    telemetry: context.EIOPlayerTelemetry,
  };
}

const delay = ms => new Promise(resolve => setTimeout(resolve, ms));

module.exports = { harness, makeStorage, delay, SCRIPTS };
