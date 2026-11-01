'use strict';
/* C verification — /web home library rows (component, template and wiring).
 *
 * Component methods and lifecycle hooks execute for real via the shared fake
 * DOM / VM harness; the template is rendered by a tiny directive interpreter
 * so assertions are made against rendered nodes, not against source regexes.
 *
 * Offline only: no app server, no browser, no fetch, no real upstream. The
 * fixture data is synthetic and harmless.
 */
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { harness, delay, SCRIPTS } = require('./web-ui-harness.cjs');
const {
  WEB, byClass, byTag, byAria, textOf, topLevelAriaHidden, findClickableByText, click,
  makeScroller, loadRuntime, flushTicks, flushRaf, settle, mountRow,
} = require('./web-home-library-rows-ui-harness.cjs');

const F = {
  component: path.join(WEB, 'home-library-row.js'),
  rows: path.join(WEB, 'home-library-rows.js'),
  query: path.join(WEB, 'library-query.js'),
  css: path.join(WEB, 'home-library-rows.css'),
  html: path.join(WEB, 'web.html'),
  app: path.join(WEB, 'app.js'),
  uiCss: path.join(WEB, 'ui.css'),
};
const requiredFiles = [F.component, F.rows, F.query, F.css, F.html, F.app, F.uiCss];

test('U0 required production files exist', () => {
  for (const file of requiredFiles) {
    assert.ok(fs.existsSync(file), 'missing ' + path.relative(process.cwd(), file));
  }
});

/* ================= fixtures ================= */

function row(overrides = {}) {
  return Object.assign({
    key: 'lib-a', title: '媒体库 A', libraryType: 'movies',
    items: [], status: 'idle', error: '', partial: false,
  }, overrides);
}

const poster = id => ({ Id: id, Name: 'Poster ' + id, Type: 'Movie' });
const itemIds = list => Array.from(list || [], item => item.Id);

async function flushMicro(rounds = 30) {
  for (let i = 0; i < rounds; i++) await Promise.resolve();
}

/* ================= U1 — registration/contract ================= */

test('U1 EIOHomeLibraryRow.register(app) provides section/imageSrc props and ensure/retry/play emits', () => {
  const runtime = loadRuntime();
  assert.equal(typeof runtime.context.EIOHomeLibraryRow, 'object');
  assert.equal(typeof runtime.context.EIOHomeLibraryRow.register, 'function');
  assert.ok(runtime.definition, 'register(app) must register the EioHomeLibraryRow component');
  const definition = runtime.definition;
  assert.equal(typeof definition.template, 'string');
  assert.ok(definition.template.length > 0);

  const prop = name => {
    if (Array.isArray(definition.props)) return definition.props.includes(name) ? {} : null;
    return definition.props && definition.props[name] != null ? definition.props[name] : null;
  };
  const section = prop('section');
  const imageSrc = prop('imageSrc');
  assert.ok(section, 'section prop is required');
  assert.ok(imageSrc, 'imageSrc prop is required');
  for (const [name, spec] of [['section', section], ['imageSrc', imageSrc]]) {
    if (typeof spec === 'object' && spec !== null) {
      assert.equal(spec.required, true, name + ' must be required');
    }
  }
  const types = spec => (Array.isArray(spec.type) ? spec.type : [spec.type]).filter(Boolean);
  // Constructors cross the VM realm boundary, so identity checks against the
  // host Object/Function would fail; compare the constructor name instead.
  const isCtor = (spec, name) => types(spec)
    .some(type => typeof type === 'function' && type.name === name);
  if (section && section.type) assert.ok(isCtor(section, 'Object'), 'section must be an Object prop');
  if (imageSrc && imageSrc.type) assert.ok(isCtor(imageSrc, 'Function'), 'imageSrc must be a Function prop');

  const emits = Array.isArray(definition.emits) ? definition.emits : Object.keys(definition.emits || {});
  for (const name of ['ensure', 'retry', 'play']) assert.ok(emits.includes(name), 'missing emit ' + name);
});

/* ================= U2 — real links ================= */

test('U2 title, 查看全部 and the row-tail card are real encoded library links', async () => {
  const runtime = loadRuntime({ observe: false });
  const section = row({ key: 'lib/one 中文', title: '电影 / 儿童' });
  const mounted = await mountRow(runtime, section);
  const tree = mounted.render();
  const href = '#/library/' + encodeURIComponent(section.key);

  const title = byClass(tree, 'eio-row-title')[0];
  assert.ok(title, 'missing .eio-row-title');
  assert.equal(title.tag, 'a');
  assert.equal(title.attrs.href, href);
  assert.equal(textOf(title).trim(), section.title);

  const more = byClass(tree, 'eio-row-more')[0];
  assert.ok(more, 'missing .eio-row-more');
  assert.equal(more.tag, 'a');
  assert.equal(more.attrs.href, href);
  assert.match(textOf(more), /查看全部/);

  const enter = byClass(tree, 'eio-home-lib-enter')[0];
  assert.ok(enter, 'missing row-tail .eio-home-lib-enter card');
  assert.equal(enter.tag, 'a');
  assert.equal(enter.attrs.href, href);
  assert.match(textOf(enter), /进入媒体库/);
  assert.match(textOf(enter), /电影 \/ 儿童/);

  for (const anchor of [title, more]) {
    assert.ok(!('@click' in anchor.rawAttrs), 'library links must not use @click routing');
    assert.ok(!('@click.prevent' in anchor.rawAttrs));
    assert.ok(!anchor.listeners.click, 'library links must stay plain anchors');
  }
  assert.ok(!/click\.prevent/.test(runtime.definition.template)
    || !/eio-row-(title|more)[^>]*@click\.prevent/.test(runtime.definition.template));
});

/* ================= U3 — IntersectionObserver ================= */

test('U3 IntersectionObserver: rootMargin 0px, idle emits ensure once, loading/ready never emit', async () => {
  const runtime = loadRuntime();
  const section = row();
  const mounted = await mountRow(runtime, section, {
    scroller: makeScroller({ clientWidth: 800, scrollWidth: 2200 }),
  });
  assert.equal(runtime.observers.length, 1, 'exactly one observer per row instance');
  const observer = runtime.observers[0];
  assert.equal(observer.rootMargin, '0px');
  assert.ok(observer.targets.includes(mounted.el), 'the row root ($el) must be observed');
  assert.equal(mounted.emits.filter(entry => entry.event === 'ensure').length, 0, 'no pre-fetch before intersection');

  observer.trigger(false);
  assert.equal(mounted.emits.filter(entry => entry.event === 'ensure').length, 0);
  observer.trigger(true);
  const ensured = mounted.emits.filter(entry => entry.event === 'ensure');
  assert.equal(ensured.length, 1);
  assert.equal(ensured[0].payload, section.key);
  observer.trigger(true);
  observer.trigger(true);
  assert.equal(mounted.emits.filter(entry => entry.event === 'ensure').length, 1, 'repeat callbacks must not re-emit');
  assert.ok(observer.unobserved.includes(mounted.el), 'the row must unobserve its root after ensuring');

  for (const status of ['loading', 'ready']) {
    const other = loadRuntime();
    const mountedOther = await mountRow(other, row({ key: 'lib-' + status, status, items: [poster('x')] }));
    for (const observerOther of other.observers) observerOther.trigger(true);
    assert.equal(mountedOther.emits.filter(entry => entry.event === 'ensure').length, 0, status + ' must not ensure');
  }
});

test('U3b the same key on a fresh row object (reset) is observed again', async () => {
  const runtime = loadRuntime();
  const first = row({ key: 'lib-a' });
  const mounted = await mountRow(runtime, first, {
    scroller: makeScroller({ clientWidth: 800, scrollWidth: 2000 }),
  });
  const observer = runtime.observers[0];
  observer.trigger(true);
  assert.equal(mounted.emits.filter(entry => entry.event === 'ensure').length, 1);

  // page-state calls reset(): a new row object with the same key/status idle.
  const second = row({ key: 'lib-a', title: '媒体库 A (refresh)' });
  mounted.updateSection(second);
  await flushTicks(runtime);
  flushRaf(runtime);
  await flushTicks(runtime);
  assert.notEqual(first, second);
  const active = runtime.observers.find(candidate => candidate.targets.includes(mounted.el));
  assert.ok(active, 'the new section object must be observed again even when the key is unchanged');
  active.trigger(true);
  assert.equal(mounted.emits.filter(entry => entry.event === 'ensure').length, 2, 'the ensure marker resets with the section object');
});

/* ================= U4 — no IntersectionObserver ================= */

test('U4 without IntersectionObserver only a manual 加载内容 button emits ensure', async () => {
  const runtime = loadRuntime({ observe: false });
  const section = row();
  const mounted = await mountRow(runtime, section);
  await flushTicks(runtime);
  assert.equal(runtime.observers.length, 0, 'no observer may be created when the API is absent');
  let tree = mounted.render();
  assert.equal(byTag(tree, 'eio-poster-card').length, 0);
  const manual = findClickableByText(tree, '加载内容');
  assert.ok(manual, 'idle rows need a manual load button without IntersectionObserver');
  assert.equal(manual.tag, 'button');
  click(manual);
  const ensured = mounted.emits.filter(entry => entry.event === 'ensure');
  assert.equal(ensured.length, 1);
  assert.equal(ensured[0].payload, section.key);
  assert.equal(runtime.calls.fetch, 0, 'the component itself must never fetch');

  mounted.updateSection(row({ key: section.key, status: 'loading' }));
  await settle(runtime);
  tree = mounted.render();
  assert.equal(findClickableByText(tree, '加载内容'), null, 'the manual action disappears once the row is no longer idle');
});

/* ================= U5 — statuses ================= */

test('U5 idle skeleton, empty/ready/error/partial rendering and retry wiring', async () => {
  const runtime = loadRuntime({ observe: false });

  const idle = await mountRow(runtime, row({ key: 'lib-idle' }));
  let tree = idle.render();
  assert.equal(byTag(tree, 'eio-poster-card').length, 0);
  const hidden = topLevelAriaHidden(tree);
  assert.equal(hidden.length, 1, 'exactly one static skeleton placeholder (never 50)');
  assert.equal(textOf(tree).includes('共 '), false);

  const empty = await mountRow(runtime, row({ key: 'lib-empty', status: 'empty' }));
  tree = empty.render();
  assert.match(textOf(tree), /暂无内容/);
  assert.equal(byTag(tree, 'eio-poster-card').length, 0);
  assert.equal(findClickableByText(tree, '重试'), null);

  const readyItems = [poster('a'), poster('b')];
  const ready = await mountRow(runtime, row({ key: 'lib-ready', status: 'ready', items: readyItems }));
  tree = ready.render();
  const cards = byTag(tree, 'eio-poster-card');
  assert.equal(cards.length, readyItems.length);
  assert.equal(cards[0].attrs['detail-href'], '#/item/a');
  assert.equal(cards[0].attrs['image-src'], 'poster:a:300');
  assert.ok(ready.imageCalls.every(call => call.size === 300), 'poster images are requested at size 300');
  assert.equal(findClickableByText(tree, '重试'), null);
  assert.ok(!/共\s*\d+\s*项/.test(textOf(tree)), 'the row must not render an exact total');
  assert.ok(!textOf(tree).includes('TotalRecordCount'));

  const errorSection = row({
    key: 'lib-error', status: 'error', error: '加载失败：boom', items: [poster('a'), poster('b')],
  });
  const failed = await mountRow(runtime, errorSection);
  tree = failed.render();
  assert.equal(byTag(tree, 'eio-poster-card').length, 2, 'old cards stay visible in an error state');
  assert.match(textOf(tree), /加载失败：boom/);
  const retry = findClickableByText(tree, '重试');
  assert.ok(retry, 'error rows need a retry button');
  click(retry);
  assert.deepEqual(failed.emits.filter(entry => entry.event === 'retry').map(entry => entry.payload), ['lib-error']);

  const partialSection = row({
    key: 'lib-partial', status: 'partial', partial: true, items: [poster('a')],
  });
  const partial = await mountRow(runtime, partialSection);
  tree = partial.render();
  assert.match(textOf(tree), /预览可能不完整/);
  assert.equal(byTag(tree, 'eio-poster-card').length, 1);
  const partialRetry = findClickableByText(tree, '重试');
  assert.ok(partialRetry);
  click(partialRetry);
  assert.deepEqual(partial.emits.filter(entry => entry.event === 'retry').map(entry => entry.payload), ['lib-partial']);
});

test('U5b a real error row produced by home-library-rows keeps its old posters', async () => {
  const runtime = loadRuntime();
  assert.ok(runtime.context.EIOHomeLibraryRows, 'home-library-rows.js must define EIOHomeLibraryRows');
  const pending = [];
  const api = url => new Promise((resolve, reject) => { pending.push({ url, resolve, reject }); });
  const rows = runtime.context.EIOHomeLibraryRows.create({
    api,
    query: runtime.context.EIOData.query,
    getUserId: () => 'user',
    isActive: () => true,
  });
  rows.reset([{ Id: 'lib-a', Name: 'A', CollectionType: 'movies' }]);
  const key = String(rows.rows.value[0].key);
  rows.ensure(key);
  await flushMicro();
  assert.equal(pending.length, 1);
  pending[0].resolve({ Items: [poster('a'), poster('b')], EioIncompleteResults: true });
  await flushMicro();
  assert.equal(rows.rows.value[0].status, 'partial');
  assert.deepEqual(itemIds(rows.rows.value[0].items), ['a', 'b']);

  rows.retry(key);
  await flushMicro();
  assert.equal(pending.length, 2);
  pending[1].reject(new Error('boom'));
  await flushMicro();
  const errorRow = rows.rows.value[0];
  assert.equal(errorRow.status, 'error');
  assert.deepEqual(itemIds(errorRow.items), ['a', 'b'], 'a failed retry keeps the previous page');

  const mounted = await mountRow(runtime, errorRow);
  const tree = mounted.render();
  assert.equal(byTag(tree, 'eio-poster-card').length, 2, 'error must not hide the old posters');
  const retry = findClickableByText(tree, '重试');
  assert.ok(retry);
  click(retry);
  assert.deepEqual(mounted.emits.filter(entry => entry.event === 'retry').map(entry => entry.payload), [key]);
});

/* ================= U6 — arrow buttons ================= */

test('U6 arrows: edge disabled states, scroll/resize recalculation and ~90% scrollBy', async () => {
  const scroller = makeScroller({ clientWidth: 800, scrollWidth: 800 });
  const runtime = loadRuntime();
  const mounted = await mountRow(runtime, row({ status: 'ready', items: [poster('a')] }), { scroller });

  let tree = mounted.render();
  let left = byAria(tree, '向左滚动')[0];
  let right = byAria(tree, '向右滚动')[0];
  assert.ok(left || right, 'arrow controls must exist');
  if (left || right) {
    assert.ok(left && right, 'both arrows must render together');
    assert.equal(left.tag, 'button');
    assert.equal(right.tag, 'button');
    assert.equal(left.attrs.type, 'button');
    assert.equal(right.attrs.type, 'button');
    assert.ok('disabled' in left.attrs && 'disabled' in right.attrs, 'no overflow: arrows are disabled or hidden');
  }

  scroller.scrollWidth = 2400;
  scroller.dispatch('scroll');
  await settle(runtime);
  tree = mounted.render();
  left = byAria(tree, '向左滚动')[0];
  right = byAria(tree, '向右滚动')[0];
  assert.ok(left && right, 'arrows must render when content overflows');
  assert.ok('disabled' in left.attrs, 'left is disabled at the start');
  assert.ok(!('disabled' in right.attrs), 'right is enabled when content overflows');

  scroller.scrollLeft = 100;
  scroller.dispatch('scroll');
  await settle(runtime);
  tree = mounted.render();
  left = byAria(tree, '向左滚动')[0];
  right = byAria(tree, '向右滚动')[0];
  assert.ok(!('disabled' in left.attrs));
  assert.ok(!('disabled' in right.attrs));

  click(right);
  assert.equal(scroller.scrollByCalls.length, 1);
  const call = scroller.scrollByCalls[0];
  assert.ok(call.amount > 0);
  assert.ok(Math.abs(call.amount) >= 600 && Math.abs(call.amount) <= 800, 'scroll step is about 90% of the viewport');
  assert.equal(call.arg.behavior, 'smooth');

  scroller.scrollLeft = 1600;
  scroller.dispatch('scroll');
  await settle(runtime);
  tree = mounted.render();
  left = byAria(tree, '向左滚动')[0];
  right = byAria(tree, '向右滚动')[0];
  assert.ok(!('disabled' in left.attrs));
  assert.ok('disabled' in right.attrs, 'right is disabled at the end');

  scroller.clientWidth = 1000;
  scroller.scrollLeft = 0;
  for (const fn of [...(runtime.windowListeners.resize || [])]) fn({ type: 'resize' });
  await settle(runtime);
  tree = mounted.render();
  right = byAria(tree, '向右滚动')[0];
  assert.ok(!('disabled' in right.attrs), 'resize must recalculate canRight');
});

test('U6b prefers-reduced-motion switches scrollBy to behavior auto', async () => {
  const runtime = loadRuntime({ reducedMotion: true });
  const mounted = await mountRow(runtime, row({ status: 'ready', items: [poster('a')] }), {
    scroller: makeScroller({ clientWidth: 800, scrollWidth: 2000 }),
  });
  const tree = mounted.render();
  click(byAria(tree, '向右滚动')[0]);
  assert.equal(mounted.scroller.scrollByCalls[0].arg.behavior, 'auto');
});

/* ================= U7 — focus + unmount ================= */

test('U7 focusin scrolls horizontally only and beforeUnmount removes every listener/observer', async () => {
  const scroller = makeScroller({ clientWidth: 800, scrollWidth: 2400, scrollLeft: 0 });
  const runtime = loadRuntime();
  const mounted = await mountRow(runtime, row({ status: 'ready', items: [poster('a')] }), { scroller });

  let intoView = 0;
  const focused = {
    offsetLeft: 1800,
    offsetWidth: 150,
    offsetParent: scroller,
    dataset: {},
    closest() { return focused; },
    focus() {},
    scrollIntoView() { intoView++; },
    getBoundingClientRect() { return { left: 1800, top: 0, width: 150, height: 200, right: 1950, bottom: 200 }; },
  };
  scroller.dispatch('focusin', { target: focused });
  await settle(runtime);
  assert.ok(scroller.scrollLeft > 0, 'focus inside the row scrolls the horizontal track');
  assert.ok(scroller.scrollLeft < 2400, 'the focus adjustment is bounded by the track');
  assert.equal(intoView, 0, 'focus handling must not call scrollIntoView');
  assert.equal(runtime.context.window.scrollY, 0);
  assert.equal(runtime.calls.windowScrollTo || 0, 0);

  // Pending animation frames must be cancelled on unmount.
  scroller.dispatch('scroll');
  mounted.unmount();
  await Promise.resolve();
  assert.ok(runtime.observers.length === 0 || runtime.observers.every(observer => observer.disconnected), 'observer must be disconnected');
  assert.equal((scroller.listeners.scroll || []).length, 0);
  assert.equal((scroller.listeners.focusin || []).length, 0);
  assert.equal((runtime.windowListeners.resize || []).length, 0);
  assert.equal(runtime.rafQueue.size, 0, 'pending requestAnimationFrame work is cancelled');
  const before = scroller.scrollLeft;
  scroller.dispatch('scroll');
  scroller.dispatch('focusin', { target: focused });
  assert.equal(scroller.scrollLeft, before);
});

/* ================= U8 — static safety properties ================= */

test('U8 no wheel hijack, autoplay, auto-paging or duplicated poster-card DOM', () => {
  const source = fs.readFileSync(F.component, 'utf8');
  const template = loadRuntime().definition.template;
  assert.ok(!/addEventListener\s*\(\s*['"]wheel/.test(source), 'no wheel listener');
  assert.ok(!/@wheel\b/.test(template), 'no wheel template binding');
  assert.ok(!/\bsetInterval\s*\(/.test(source), 'no interval/autoplay');
  assert.ok(!/\bautoplay\b/i.test(source));
  assert.ok(!/\.scrollIntoView\s*\(/.test(source), 'focus handling must not scroll the page');
  assert.ok(!/StartIndex\s*[:=]/.test(source), 'the component never pages');
  assert.ok(!/['"`]\/Users\//.test(source), 'the component never builds item requests');
  assert.match(template, /eio-poster-card/);
  assert.ok(!/class="eio-card"/.test(template), 'poster DOM must be reused, not copied');
  assert.match(template, /eio-row-title/);
  assert.match(template, /eio-row-more/);
  assert.match(template, /eio-home-lib-enter/);
  assert.match(template, /(fill|stroke)="currentColor"/);
});

test('U8b the component registers no wheel listener at runtime', async () => {
  const runtime = loadRuntime();
  const mounted = await mountRow(runtime, row({ status: 'ready', items: [poster('a')] }), {
    scroller: makeScroller({ clientWidth: 800, scrollWidth: 2000 }),
  });
  assert.equal(mounted.scroller.listeners.wheel, undefined);
  assert.equal(runtime.windowListeners.wheel, undefined);
  assert.equal(runtime.documentListeners.wheel, undefined);
});

/* ================= U9 — CSS ================= */

const ALLOWED_TOKENS = new Set([
  '--bg-2', '--bg-3', '--t1', '--t2', '--line', '--line-2', '--ac', '--ac-text',
  '--ac-on', '--ac-glow', '--r-card', '--r-btn', '--surface-2',
  '--eio-density-gap', '--dur-micro', '--dur-base', '--ease',
  '--danger', '--gold',
]);

function cssSelectors(css) {
  const clean = css.replace(/\/\*[\s\S]*?\*\//g, '');
  const selectors = [];
  let depth = 0;
  let start = 0;
  for (let i = 0; i < clean.length; i++) {
    const char = clean[i];
    if (char === '{') {
      const head = clean.slice(start, i).trim();
      if (head && !head.startsWith('@')) selectors.push(head);
      depth++;
      start = i + 1;
    } else if (char === '}') {
      depth = Math.max(0, depth - 1);
      start = i + 1;
    }
  }
  return selectors;
}

test('U9 home-library-rows.css is scoped, token-only, responsive and reduced-motion aware', () => {
  const css = fs.readFileSync(F.css, 'utf8');
  const usedTokens = Array.from(css.matchAll(/var\(\s*(--[\w-]+)/g), match => match[1]);
  for (const token of usedTokens) {
    assert.ok(ALLOWED_TOKENS.has(token), 'unexpected theme token ' + token);
  }
  for (const token of ['--surface-2', '--line-2', '--eio-density-gap', '--t1']) {
    assert.ok(usedTokens.includes(token), 'expected token ' + token + ' to drive the row styling');
  }
  assert.ok(!/#[0-9a-fA-F]{3,8}\b/.test(css), 'colors must come from theme tokens, not hex literals');
  assert.match(css, /@media\s*\(\s*max-width\s*:\s*767px\s*\)/);
  assert.match(css, /@media\s*\(\s*max-width\s*:\s*767px\s*\)[\s\S]*?display\s*:\s*none/, 'arrows hide on mobile');
  assert.match(css, /@media\s*\(\s*max-width\s*:\s*767px\s*\)[\s\S]*?112px/, 'mobile card basis 112px');
  assert.match(css, /@media\s*\(\s*prefers-reduced-motion\s*:\s*reduce\s*\)/);
  assert.match(css, /@media\s*\(\s*prefers-reduced-motion\s*:\s*reduce\s*\)[\s\S]*?scroll-behavior\s*:\s*auto/);
  assert.match(css, /overflow-x\s*:\s*auto/);
  assert.match(css, /overflow-y\s*:\s*hidden/);
  assert.match(css, /flex\s*:\s*0\s+0\s+(150|168)px/);

  const selectors = cssSelectors(css).flatMap(head => head.split(',')).map(selector => selector.trim()).filter(Boolean);
  assert.ok(selectors.length > 0);
  for (const selector of selectors) {
    assert.ok(selector.startsWith('body.eio-web .eio-home-lib-'), 'unscoped selector: ' + selector);
  }
  const uiCss = fs.readFileSync(F.uiCss, 'utf8');
  assert.ok(!uiCss.includes('eio-home-lib'), 'ui.css must not grow row-specific rules');
});

/* ================= U10 — web.html / app.js wiring + script order ================= */

function orderedAssetUrls(html, kind) {
  const pattern = kind === 'css'
    ? /<link\b[^>]*href="([^"]+)"/g
    : /<script\b[^>]*src="([^"]+)"/g;
  return Array.from(html.matchAll(pattern), match => match[1]);
}

const assetPath = url => new URL(url, 'http://localhost').pathname;
const assetIndex = (urls, file) => urls.findIndex(url => assetPath(url).endsWith('/' + file));

test('U10 web.html loads the new scripts/styles in contract order and replaces the old latest blocks', () => {
  const html = fs.readFileSync(F.html, 'utf8');
  const css = orderedAssetUrls(html, 'css');
  const scripts = orderedAssetUrls(html, 'js');

  const uiCssIndex = assetIndex(css, 'ui.css');
  const rowsCssIndex = assetIndex(css, 'home-library-rows.css');
  assert.ok(uiCssIndex >= 0 && rowsCssIndex >= 0, 'home-library-rows.css must be linked');
  assert.ok(uiCssIndex < rowsCssIndex, 'home-library-rows.css must come after ui.css');
  assert.match(css[rowsCssIndex], /\?v=/);
  assert.equal(new URL(css[rowsCssIndex], 'http://localhost').search, new URL(css[uiCssIndex], 'http://localhost').search);

  const queryIndex = assetIndex(scripts, 'library-query.js');
  const libraryStateIndex = assetIndex(scripts, 'library-state.js');
  const pageStateIndex = assetIndex(scripts, 'page-state.js');
  const rowsIndex = assetIndex(scripts, 'home-library-rows.js');
  const rowIndex = assetIndex(scripts, 'home-library-row.js');
  const componentsIndex = assetIndex(scripts, 'ui-components.js');
  const appIndex = assetIndex(scripts, 'app.js');
  assert.ok(queryIndex >= 0 && rowsIndex >= 0 && rowIndex >= 0, 'all new scripts must be included');
  assert.ok(queryIndex < libraryStateIndex, 'library-query.js before library-state.js');
  assert.ok(queryIndex < pageStateIndex, 'library-query.js before page-state.js');
  assert.ok(rowsIndex < pageStateIndex, 'home-library-rows.js before page-state.js');
  assert.ok(componentsIndex < rowIndex, 'home-library-row.js after ui-components.js');
  assert.ok(rowIndex < appIndex, 'home-library-row.js before app.js');

  // The harness must mirror the production order.
  assert.ok(SCRIPTS.indexOf('library-query.js') < SCRIPTS.indexOf('library-state.js'));
  assert.ok(SCRIPTS.indexOf('library-query.js') < SCRIPTS.indexOf('page-state.js'));
  assert.ok(SCRIPTS.indexOf('home-library-rows.js') < SCRIPTS.indexOf('page-state.js'));
  assert.ok(SCRIPTS.indexOf('ui-components.js') < SCRIPTS.indexOf('home-library-row.js'));
  assert.ok(SCRIPTS.indexOf('home-library-row.js') < SCRIPTS.indexOf('app.js'));

  assert.ok(!/v-for="sec in latestSections"/.test(html), 'the old latestSections block must be replaced');
  assert.ok(!/v-for="entry in latestErrors"/.test(html), 'the old latestErrors block must be replaced');
  assert.match(html, /<section[^>]*v-for="row in home\.libraryRows\.rows"[^>]*>/);
  assert.match(html, /<eio-home-library-row\b/);
  assert.match(html, /:section="row"/);
  assert.match(html, /@ensure="home\.libraryRows\.ensure"/);
  assert.match(html, /@retry="home\.libraryRows\.retry"/);
  assert.match(html, /@play="playItem"/);

  // hero / resume / library-entry blocks stay intact.
  assert.match(html, /class="eio-hero"/);
  assert.match(html, /eio-hero-actions/);
  assert.ok(html.includes('继续观看'));
  assert.ok(html.includes('还没有观看记录。'));
  assert.match(html, /<h2 class="eio-row-title">媒体库<\/h2>/);
  assert.ok(html.includes('@click="openLibrary(lib.Id)"'));

  const appSource = fs.readFileSync(F.app, 'utf8');
  assert.match(appSource, /window\.EIOHomeLibraryRow/);
  assert.match(appSource, /EIOHomeLibraryRow\.register\(app\)/);
  assert.ok(!/EIOHomeLibraryRows\.create/.test(appSource), 'state creation belongs to page-state.js, not app.js');
});

/* ================= U11 — hero channel unchanged ================= */

test('U11 home still issues at most 3 hero Latest (Limit=12) + one Resume and rows stay idle', async (t) => {
  const latest = [];
  const resume = [];
  // The shared harness resolves every reply automatically (its fetch returns a
  // resolved promise), so loadHome() is awaited directly; there is no pending
  // request queue to drain.
  const { app, calls } = harness({ reply: url => {
    if (url.pathname.endsWith('/Views')) {
      return { Items: [
        { Id: 'm1', Name: 'Movies', CollectionType: 'movies' },
        { Id: 'p1', Name: 'Playlists', CollectionType: 'playlists' },
        { Id: 't1', Name: 'TV', CollectionType: 'tvshows' },
        { Id: 'x1', Name: 'Music', CollectionType: 'music' },
        { Id: 'm2', Name: 'Movies 2', CollectionType: 'movies' },
      ] };
    }
    if (url.pathname.endsWith('/Items/Latest')) {
      latest.push(url);
      return { Items: [{ Id: 'l' + latest.length, Name: 'L', Type: 'Movie' }] };
    }
    if (url.pathname.endsWith('/Items/Resume')) {
      resume.push(url);
      return { Items: [] };
    }
    return { Items: [] };
  } });
  assert.ok(app, 'app.js must mount the application');
  // Three real hero candidates make createHero() start its autoplay interval;
  // without disposal that live interval keeps the test process alive after the
  // runner timeout. Register the cleanup before any further assertion so a
  // failure cannot leak it.
  t.after(() => {
    app.home.dispose();
    app.hero.dispose();
    app.toasts.dispose();
  });
  app.page.value = 'app';
  app.navPage.value = 'home';
  await app.loadHome();
  await delay(0);

  assert.equal(latest.length, 3, 'hero keeps at most three Items/Latest requests');
  for (const url of latest) assert.equal(url.searchParams.get('Limit'), '12');
  assert.deepEqual(latest.map(url => url.searchParams.get('ParentId')), ['m1', 't1', 'x1']);
  assert.ok(!latest.some(url => url.searchParams.get('ParentId') === 'p1'), 'playlists are never a hero Latest target');
  assert.equal(resume.length, 1);
  assert.equal(resume[0].searchParams.get('Limit'), '12');
  assert.equal(calls.filter(url => url.pathname.endsWith('/Items')).length, 0,
    'unobserved library rows must not prefetch');
  assert.equal(typeof app.home.retryLatest, 'function');
  assert.equal(typeof app.home.retryResume, 'function');
  assert.ok(app.home.libraryRows, 'page-state must expose the row controller');
  assert.deepEqual(
    Array.from(app.home.libraryRows.rows.value, ownRow => ownRow.key),
    ['m1', 'p1', 't1', 'x1', 'm2'],
    'rows keep Views order and include playlists',
  );
  assert.ok(Array.from(app.home.libraryRows.rows.value).every(ownRow => ownRow.status === 'idle'));
  const sections = Array.from(app.latestSections.value || []);
  assert.ok(sections.every(section => ['m1', 't1', 'x1'].includes(String(section.key))),
    'latestSections stays the hero-only Latest source');
});
