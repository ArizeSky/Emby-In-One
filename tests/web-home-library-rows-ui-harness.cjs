'use strict';
/* Shared fake DOM / VM harness for the /web home library row UI tests.
 *
 * Loads the real author scripts (library-query.js, data.js,
 * home-library-rows.js, home-library-row.js) in an isolated VM with a small
 * Vue-compatible component runtime: fake refs, fake DOM events, a fake
 * IntersectionObserver, a manually drained nextTick queue and a
 * requestAnimationFrame queue. A tiny directive interpreter renders the real
 * template (v-for/v-if/v-else, :bindings, @events, {{ }}) so assertions are
 * made against rendered nodes, not source regexes.
 *
 * The mount helper follows the real component contract: the component observes
 * `this.$el` and measures `this.$refs.scroller`, so both are wired here.
 * Offline only: no app server, no browser, no fetch, no real upstream.
 */
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const WEB = path.join(__dirname, '..', 'public', 'web');
const RUNTIME_FILES = [
  path.join(WEB, 'library-query.js'),
  path.join(WEB, 'data.js'),
  path.join(WEB, 'home-library-rows.js'),
  path.join(WEB, 'home-library-row.js'),
];
const has = file => fs.existsSync(file);

/* ================= tiny template renderer ================= */

const GLOBALS = {
  encodeURIComponent, decodeURIComponent, Math, JSON, String, Number, Boolean,
  Array, Object, Date, RegExp, parseInt, parseFloat, isNaN, isFinite,
  NaN, Infinity, undefined,
};
const COMPILED = new Map();

function compileExpression(expr, statement) {
  const key = (statement ? 's:' : 'e:') + expr;
  let fn = COMPILED.get(key);
  if (!fn) {
    fn = new Function('__scope', 'with (__scope) { '
      + (statement ? expr : 'return (' + expr + ');')
      + ' }');
    COMPILED.set(key, fn);
  }
  return fn;
}

function scopeProxy(instance, scope) {
  const target = Object.assign({}, instance, scope);
  return new Proxy(target, {
    has: () => true,
    get(object, prop) {
      if (prop in object) return object[prop];
      if (typeof prop === 'string' && Object.prototype.hasOwnProperty.call(GLOBALS, prop)) return GLOBALS[prop];
      return globalThis[prop];
    },
  });
}

const evaluate = (instance, scope, expr) => compileExpression(expr, false)(scopeProxy(instance, scope));

// Vue treats a handler written as a bare method reference (`@click="retry"`)
// as `retry($event)`; only real statement bodies (`@click="pageBy(-1)"`,
// `@play="$emit('play', it)"`) are executed as-is. Evaluating a bare name as a
// statement would only read the function and never invoke it.
const BARE_METHOD = /^\s*[A-Za-z_$][\w$]*(?:\.[A-Za-z_$][\w$]*)*\s*$/;

function evaluateStatement(instance, scope, expr, event) {
  const proxy = scopeProxy(instance, { ...scope, $event: event });
  if (BARE_METHOD.test(expr)) {
    const handler = compileExpression(expr, false)(proxy);
    return typeof handler === 'function' ? handler(event) : handler;
  }
  return compileExpression(expr, true)(proxy);
}

function parseAttributes(text) {
  const attrs = {};
  let i = 0;
  while (i < text.length) {
    while (i < text.length && /\s/.test(text[i])) i++;
    if (i >= text.length) break;
    const start = i;
    while (i < text.length && !/[\s=]/.test(text[i])) i++;
    const name = text.slice(start, i);
    if (!name) { i++; continue; }
    while (i < text.length && /\s/.test(text[i])) i++;
    if (text[i] === '=') {
      i++;
      while (i < text.length && /\s/.test(text[i])) i++;
      const quote = text[i];
      let value;
      if (quote === '"' || quote === "'") {
        i++;
        const end = text.indexOf(quote, i);
        value = text.slice(i, end === -1 ? text.length : end);
        i = end === -1 ? text.length : end + 1;
      } else {
        const valueStart = i;
        while (i < text.length && !/\s/.test(text[i])) i++;
        value = text.slice(valueStart, i);
      }
      attrs[name] = value;
    } else {
      attrs[name] = '';
    }
  }
  return attrs;
}

const VOID_TAGS = new Set(['img', 'br', 'input', 'hr', 'source', 'path', 'circle', 'line', 'polygon', 'rect', 'use', 'meta', 'link']);

function tokenize(template) {
  const tokens = [];
  const pushText = text => { if (text) tokens.push({ type: 'text', text }); };
  let i = 0;
  while (i < template.length) {
    const lt = template.indexOf('<', i);
    if (lt === -1) { pushText(template.slice(i)); break; }
    if (lt > i) pushText(template.slice(i, lt));
    if (template.startsWith('<!--', lt)) {
      const end = template.indexOf('-->', lt + 4);
      i = end === -1 ? template.length : end + 3;
      continue;
    }
    const close = /^<\/([\w:-]+)\s*>/.exec(template.slice(lt));
    if (close) { tokens.push({ type: 'close', name: close[1] }); i = lt + close[0].length; continue; }
    let j = lt + 1;
    let quote = null;
    while (j < template.length) {
      const ch = template[j];
      if (quote) { if (ch === quote) quote = null; }
      else if (ch === '"' || ch === "'") quote = ch;
      else if (ch === '>') break;
      j++;
    }
    const raw = template.slice(lt + 1, j);
    const selfClose = /\/\s*$/.test(raw) || VOID_TAGS.has((/^([\w:-]+)/.exec(raw) || [])[1]);
    const body = raw.replace(/\/\s*$/, '');
    const nameMatch = /^([\w:-]+)/.exec(body);
    if (!nameMatch) { pushText(template.slice(lt, j + 1)); i = j + 1; continue; }
    tokens.push({ type: 'open', name: nameMatch[1], attrs: parseAttributes(body.slice(nameMatch[0].length)), selfClose });
    i = j + 1;
  }
  return tokens;
}

function elementBounds(tokens, index) {
  const token = tokens[index];
  if (token.selfClose) return { end: index + 1, close: null };
  let depth = 1;
  for (let i = index + 1; i < tokens.length; i++) {
    const token2 = tokens[i];
    if (token2.type === 'open' && !token2.selfClose) depth++;
    else if (token2.type === 'close') {
      depth--;
      if (depth === 0) return { end: i + 1, close: i };
    }
  }
  return { end: tokens.length, close: null };
}

function forList(expr, instance, scope) {
  const match = /^\s*(?:\(([^)]*)\)|([\w$]+))\s+(?:in|of)\s+([\s\S]+)$/.exec(expr);
  if (!match) return [];
  const value = evaluate(instance, scope, match[3]);
  if (typeof value === 'number') return Array.from({ length: value }, (_, index) => [index + 1, index]);
  if (!value) return [];
  return Array.from(value, (item, index) => [item, index]);
}

function forScope(expr, item, index, scope) {
  const match = /^\s*(?:\(([^)]*)\)|([\w$]+))\s+(?:in|of)\s+/.exec(expr);
  const child = { ...scope };
  if (!match) return child;
  const names = (match[1] || match[2] || '').split(',').map(name => name.trim()).filter(Boolean);
  if (names[0]) child[names[0]] = item;
  if (names[1]) child[names[1]] = index;
  return child;
}

function interpolate(text, instance, scope) {
  return text.replace(/\{\{([\s\S]*?)\}\}/g, (_, expr) => {
    const value = evaluate(instance, scope, expr);
    return value == null ? '' : String(value);
  });
}

function classString(value) {
  if (!value) return '';
  if (typeof value === 'string') return value;
  if (Array.isArray(value)) return value.map(classString).filter(Boolean).join(' ');
  if (typeof value === 'object') return Object.keys(value).filter(key => value[key]).join(' ');
  return String(value);
}

function renderRange(tokens, start, end, instance, scope, parent) {
  let i = start;
  let chain = null;
  while (i < end) {
    const token = tokens[i];
    if (token.type === 'text') {
      const text = interpolate(token.text, instance, scope);
      if (text.trim()) parent.children.push({ tag: '#text', text });
      i++;
      continue;
    }
    if (token.type === 'close') { i++; continue; }
    const bounds = elementBounds(tokens, i);
    const attrs = token.attrs || {};
    if (attrs['v-for'] !== undefined) {
      chain = null;
      for (const [item, index] of forList(attrs['v-for'], instance, scope)) {
        const childScope = forScope(attrs['v-for'], item, index, scope);
        if (attrs['v-if'] !== undefined && !evaluate(instance, childScope, attrs['v-if'])) continue;
        renderElement(tokens, i, bounds, instance, childScope, parent);
      }
      i = bounds.end;
      continue;
    }
    let show = true;
    if (attrs['v-if'] !== undefined) {
      chain = { matched: false };
      show = !!evaluate(instance, scope, attrs['v-if']);
      chain.matched = show;
    } else if (attrs['v-else-if'] !== undefined) {
      if (!chain) chain = { matched: false };
      show = !chain.matched && !!evaluate(instance, scope, attrs['v-else-if']);
      if (show) chain.matched = true;
    } else if (attrs['v-else'] !== undefined) {
      if (!chain) chain = { matched: false };
      show = !chain.matched;
      chain.matched = true;
    } else {
      chain = null;
    }
    if (show) renderElement(tokens, i, bounds, instance, scope, parent);
    i = bounds.end;
  }
}

function renderElement(tokens, index, bounds, instance, scope, parent) {
  const token = tokens[index];
  if (token.name === 'template') {
    renderRange(tokens, index + 1, bounds.close == null ? bounds.end : bounds.close, instance, scope, parent);
    return;
  }
  const node = {
    tag: token.name,
    attrs: {},
    rawAttrs: token.attrs,
    listeners: {},
    children: [],
  };
  const boundClass = [];
  for (const [name, raw] of Object.entries(token.attrs)) {
    if (name === 'class') continue;
    if (name.startsWith('@')) {
      const eventName = name.slice(1).split('.')[0];
      node.listeners[eventName] = event => evaluateStatement(instance, scope, raw, event);
      continue;
    }
    if (name.startsWith('v-on:')) {
      const eventName = name.slice(5).split('.')[0];
      node.listeners[eventName] = event => evaluateStatement(instance, scope, raw, event);
      continue;
    }
    if (name.startsWith(':')) {
      const target = name.slice(1);
      const value = evaluate(instance, scope, raw);
      if (target === 'class') { boundClass.push(classString(value)); continue; }
      if (target === 'key') continue;
      if (value === false || value === null || value === undefined) continue;
      node.attrs[target] = value === true ? '' : String(value);
      continue;
    }
    if (name.startsWith('v-bind:')) {
      const target = name.slice(7);
      const value = evaluate(instance, scope, raw);
      if (target === 'class') { boundClass.push(classString(value)); continue; }
      if (target === 'key') continue;
      if (value === false || value === null || value === undefined) continue;
      node.attrs[target] = value === true ? '' : String(value);
      continue;
    }
    if (name === 'v-show') { node._show = !!evaluate(instance, scope, raw); continue; }
    if (name.startsWith('v-') || name === 'ref' || name === 'key') continue;
    node.attrs[name] = raw;
  }
  const classes = [token.attrs.class || '', ...boundClass].filter(Boolean).join(' ').trim();
  if (classes) node.attrs.class = classes;
  if (node._show === false) node.attrs.style = [node.attrs.style, 'display:none'].filter(Boolean).join(';');
  delete node._show;
  parent.children.push(node);
  if (!token.selfClose) {
    renderRange(tokens, index + 1, bounds.close == null ? bounds.end : bounds.close, instance, scope, node);
  }
}

function renderTemplate(template, instance) {
  const tokens = tokenize(template);
  const root = { tag: '#root', attrs: {}, listeners: {}, children: [] };
  renderRange(tokens, 0, tokens.length, instance, {}, root);
  return root;
}

function walk(node, visit) {
  visit(node);
  for (const child of node.children || []) walk(child, visit);
}

const classList = node => (node.attrs && typeof node.attrs.class === 'string' ? node.attrs.class.split(/\s+/) : []);

function byClass(root, name) {
  const out = [];
  walk(root, node => { if (node !== root && classList(node).includes(name)) out.push(node); });
  return out;
}

function byTag(root, tag) {
  const out = [];
  walk(root, node => { if (node.tag === tag) out.push(node); });
  return out;
}

function byAria(root, label) {
  const out = [];
  walk(root, node => { if (node.attrs && node.attrs['aria-label'] === label) out.push(node); });
  return out;
}

function textOf(node) {
  if (node.tag === '#text') return node.text;
  return (node.children || []).map(textOf).join('');
}

const DECORATIVE_TAGS = new Set(['svg', 'path', 'circle', 'line', 'polygon', 'rect', 'use']);

/* A single placeholder row may contain several decorative/aria-hidden children;
 * only the outermost hidden element (the row itself) counts as one skeleton. */
function topLevelAriaHidden(root) {
  const found = [];
  const visit = (node, insideHidden) => {
    const hidden = !!(node.attrs && node.attrs['aria-hidden'] === 'true');
    const decorative = DECORATIVE_TAGS.has(node.tag);
    if (hidden && !insideHidden && !decorative) found.push(node);
    for (const child of node.children || []) visit(child, insideHidden || hidden || decorative);
  };
  visit(root, false);
  return found;
}

function findClickableByText(root, needle) {
  let found = null;
  walk(root, node => {
    if (found || node.tag === '#text' || node.tag === '#root') return;
    if (node.listeners.click && textOf(node).includes(needle)) found = node;
  });
  return found;
}

function click(node, event = {}) {
  assert.ok(node && node.listeners && typeof node.listeners.click === 'function', 'expected a click handler');
  node.listeners.click({ preventDefault() {}, stopPropagation() {}, target: node, ...event });
}

/* ================= fake DOM / Vue runtime ================= */

function makeScroller(options = {}) {
  const listeners = {};
  const scroller = {
    listeners,
    scrollLeft: options.scrollLeft || 0,
    scrollWidth: options.scrollWidth == null ? 800 : options.scrollWidth,
    clientWidth: options.clientWidth == null ? 800 : options.clientWidth,
    scrollByCalls: [],
    style: {},
    addEventListener(type, fn) { (listeners[type] ||= []).push(fn); },
    removeEventListener(type, fn) {
      const list = listeners[type] || [];
      const index = list.indexOf(fn);
      if (index >= 0) list.splice(index, 1);
    },
    dispatch(type, event = {}) {
      for (const fn of [...(listeners[type] || [])]) fn({ type, target: scroller, ...event });
    },
    scrollBy(arg, maybeY) {
      const amount = typeof arg === 'number' ? arg : (arg && typeof arg.left === 'number' ? arg.left : 0);
      this.scrollByCalls.push({ arg, amount, y: maybeY });
      const max = Math.max(0, this.scrollWidth - this.clientWidth);
      this.scrollLeft = Math.max(0, Math.min(max, this.scrollLeft + amount));
    },
    querySelectorAll() { return []; },
    querySelector() { return null; },
    contains() { return true; },
    getBoundingClientRect() {
      return { left: 0, top: 0, width: this.clientWidth, height: 300, right: this.clientWidth, bottom: 300 };
    },
    focus() {},
  };
  return scroller;
}

function loadRuntime(options = {}) {
  const compiled = [];
  const observers = [];
  const ticks = [];
  const rafQueue = new Map();
  const windowListeners = {};
  const documentListeners = {};
  const calls = { fetch: 0 };
  let rafId = 0;

  class FakeIntersectionObserver {
    constructor(callback, init = {}) {
      assert.equal(typeof callback, 'function');
      this.callback = callback;
      this.rootMargin = init.rootMargin;
      this.threshold = init.threshold;
      this.targets = [];
      this.unobserved = [];
      this.disconnected = false;
      observers.push(this);
    }
    observe(target) { if (!this.targets.includes(target)) this.targets.push(target); }
    unobserve(target) {
      this.unobserved.push(target);
      const index = this.targets.indexOf(target);
      if (index >= 0) this.targets.splice(index, 1);
    }
    disconnect() { this.disconnected = true; this.targets = []; }
    takeRecords() { return []; }
    trigger(isIntersecting = true) {
      this.callback([{ isIntersecting, target: this.targets[0] || null }], this);
    }
  }

  const context = {
    URL, AbortController, DOMException, console, Math, JSON, Promise, Set, Map, WeakMap, Date, Reflect,
    setTimeout, clearTimeout, clearInterval,
    setInterval() { calls.setInterval = (calls.setInterval || 0) + 1; return 0; },
    requestAnimationFrame(fn) {
      const id = ++rafId;
      rafQueue.set(id, fn);
      return id;
    },
    cancelAnimationFrame(id) { rafQueue.delete(id); },
    matchMedia(query) {
      return {
        media: query,
        matches: !!(options.reducedMotion && /prefers-reduced-motion/.test(query)),
        addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {},
      };
    },
    location: { origin: 'http://localhost', hash: '#/home' },
    localStorage: { getItem: () => null, setItem() {}, removeItem() {} },
    fetch() { calls.fetch++; throw new Error('network is not allowed in this test'); },
    addEventListener(type, fn) { (windowListeners[type] ||= []).push(fn); },
    removeEventListener(type, fn) {
      const list = windowListeners[type] || [];
      const index = list.indexOf(fn);
      if (index >= 0) list.splice(index, 1);
    },
    document: {
      hidden: false,
      fullscreenElement: null,
      documentElement: { dataset: {}, style: { setProperty() {}, removeProperty() {} } },
      addEventListener(type, fn) { (documentListeners[type] ||= []).push(fn); },
      removeEventListener(type, fn) {
        const list = documentListeners[type] || [];
        const index = list.indexOf(fn);
        if (index >= 0) list.splice(index, 1);
      },
    },
    IntersectionObserver: options.observe === false ? undefined : FakeIntersectionObserver,
    Vue: {
      ref: value => ({ value }),
      reactive: object => object,
      computed: fn => ({ get value() { return fn(); } }),
      watch() {},
      defineComponent: definition => definition,
      unref: value => (value && typeof value === 'object' && 'value' in value ? value.value : value),
      h: () => null,
      nextTick: fn => new Promise(resolve => {
        ticks.push(() => { try { if (fn) fn(); } finally { resolve(); } });
      }),
      onMounted() {},
      onBeforeUnmount() {},
    },
  };
  context.window = context;
  context.window.innerWidth = 1440;
  context.window.scrollY = 0;
  context.window.scrollTo = function scrollTo() {
    calls.windowScrollTo = (calls.windowScrollTo || 0) + 1;
  };
  context.document.defaultView = context;

  vm.createContext(context);
  for (const file of RUNTIME_FILES) {
    if (!has(file)) continue;
    vm.runInContext(fs.readFileSync(file, 'utf8'), context, { filename: file });
    compiled.push(path.basename(file));
  }

  const registered = {};
  const app = { component(name, definition) { registered[name] = definition; } };
  if (context.EIOHomeLibraryRow && typeof context.EIOHomeLibraryRow.register === 'function') {
    context.EIOHomeLibraryRow.register(app);
  }

  return {
    context, app, registered, compiled, observers, ticks, rafQueue,
    windowListeners, documentListeners, calls, scroller: makeScroller,
    definition: registered.EioHomeLibraryRow,
    nextTick: fn => context.Vue.nextTick(fn),
  };
}

async function flushTicks(runtime, rounds = 30) {
  for (let round = 0; round < rounds; round++) {
    if (!runtime.ticks.length) { await Promise.resolve(); if (!runtime.ticks.length) return; }
    const queued = runtime.ticks.splice(0, runtime.ticks.length);
    for (const run of queued) run();
    await Promise.resolve();
    await Promise.resolve();
  }
}

function flushRaf(runtime) {
  while (runtime.rafQueue.size) {
    const queued = [...runtime.rafQueue.entries()];
    runtime.rafQueue.clear();
    for (const [, fn] of queued) fn(16);
  }
}

async function settle(runtime) {
  await flushTicks(runtime);
  flushRaf(runtime);
  await flushTicks(runtime);
}

/* Vue fires a property watcher only when that property actually changes, so
 * the harness mirrors that instead of replaying every watcher on every update. */
function fireWatch(definition, instance, name, next, previous) {
  const spec = (definition.watch || {})[name];
  if (!spec) return;
  const handler = typeof spec === 'function' ? spec : spec.handler;
  if (typeof handler === 'function') handler.call(instance, next, previous);
}

async function mountRow(runtime, section, options = {}) {
  const definition = runtime.definition;
  assert.ok(definition, 'EioHomeLibraryRow must be registered on the app');
  const scroller = options.scroller || makeScroller();
  const emits = [];
  const imageCalls = [];
  const imageSrc = options.imageSrc || ((item, size) => {
    imageCalls.push({ id: item.Id, size });
    return 'poster:' + item.Id + ':' + size;
  });
  const instance = {};
  Object.assign(instance, { section, imageSrc });
  if (typeof definition.data === 'function') Object.assign(instance, definition.data.call(instance));
  instance.$props = { section, imageSrc };
  instance.$refs = { scroller };
  // home-library-row.js observes this.$el (the row root) and measures
  // this.$refs.scroller; keep them distinct so an observer-target regression
  // cannot hide behind a shared fake node.
  instance.$el = options.el || { tag: 'row-root' };
  instance.$emit = (event, payload) => { emits.push({ event, payload }); };
  instance.$nextTick = fn => runtime.nextTick(fn);
  instance.$forceUpdate = () => {};
  instance.$watch = () => () => {};
  for (const [name, spec] of Object.entries(definition.computed || {})) {
    const getter = typeof spec === 'function' ? spec : spec.get;
    Object.defineProperty(instance, name, { enumerable: true, get: () => getter.call(instance) });
  }
  for (const [name, fn] of Object.entries(definition.methods || {})) instance[name] = fn.bind(instance);
  const mounted = { instance, runtime, definition, scroller, el: instance.$el, emits, imageCalls };
  mounted.render = () => renderTemplate(definition.template, instance);
  mounted.unmount = () => {
    if (typeof definition.beforeUnmount === 'function') definition.beforeUnmount.call(instance);
    if (typeof definition.unmounted === 'function') definition.unmounted.call(instance);
  };
  mounted.updateSection = next => {
    const previous = instance.section;
    instance.section = next;
    instance.$props.section = next;
    if (next !== previous) fireWatch(definition, instance, 'section', next, previous);
    if (next.key !== previous.key) fireWatch(definition, instance, 'section.key', next.key, previous.key);
    if ((next.status || 'idle') !== (previous.status || 'idle')) {
      fireWatch(definition, instance, 'section.status', next.status, previous.status);
    }
    if (next.items !== previous.items) {
      fireWatch(definition, instance, 'section.items', next.items, previous.items);
    }
  };
  if (typeof definition.mounted === 'function') definition.mounted.call(instance);
  await flushTicks(runtime);
  flushRaf(runtime);
  await flushTicks(runtime);
  return mounted;
}

module.exports = {
  WEB,
  renderTemplate,
  walk,
  byClass,
  byTag,
  byAria,
  textOf,
  topLevelAriaHidden,
  findClickableByText,
  click,
  makeScroller,
  loadRuntime,
  flushTicks,
  flushRaf,
  settle,
  mountRow,
};
