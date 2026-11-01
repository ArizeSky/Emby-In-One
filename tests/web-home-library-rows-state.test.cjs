'use strict';
/* A-worker state tests for the home library rows data layer.
 *
 * Scope (execution plan §2–§3.5, A1–A12): the shared EIOLibraryQuery builder,
 * the library page's unchanged first-page/paging URLs, the on-demand row state
 * machine, its two-slot queue, and the page-state integration.
 *
 * Everything runs offline in a node:vm context with hand-driven deferred
 * promises: no timers race, no real Vue render, no fetch, no upstream.
 *
 * The reactivity model below is a deliberate proxy-based stand-in for Vue's
 * `ref`/`reactive`: `ref.value` hands out proxies, every object read through a
 * proxy is itself proxied (with a stable per-raw-object identity), and writing
 * through a proxy mutates the raw target. That is exactly the Vue behaviour
 * that turns "compare the row object I built locally" into a permanently
 * failing identity check, so the suite fails if the module ever stops taking
 * its row reference from `rows.value`.
 */
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const ROOT = path.join(__dirname, '..');
const WEB = path.join(ROOT, 'public', 'web');

// Frozen legacy snapshot of the library page parameters (pre-refactor copy).
const LEGACY_FIELDS = 'PrimaryImageAspectRatio,ProductionYear,ProviderIds,CommunityRating,RunTimeTicks,UserData';
const ERROR_TEXT = '预览加载失败，请重试';
const ROW_KEYS = ['error', 'items', 'key', 'libraryType', 'partial', 'status', 'title'];
const SCRIPTS = ['data.js', 'library-query.js', 'library-state.js', 'home-library-rows.js', 'page-state.js'];

/* ================= offline reactivity stand-in ================= */

function makeReactivity() {
  const proxies = new WeakMap();
  function toReactive(value) {
    if (value === null || typeof value !== 'object') return value;
    const cached = proxies.get(value);
    if (cached) return cached;
    const proxy = new Proxy(value, {
      get(target, key) { return toReactive(Reflect.get(target, key)); },
      set(target, key, next) { Reflect.set(target, key, next); return true; },
      has(target, key) { return Reflect.has(target, key); },
      ownKeys(target) { return Reflect.ownKeys(target); },
      getOwnPropertyDescriptor(target, key) { return Reflect.getOwnPropertyDescriptor(target, key); },
    });
    proxies.set(value, proxy);
    return proxy;
  }
  function ref(initial) {
    let raw = initial;
    return {
      get value() { return toReactive(raw); },
      set value(next) { raw = next; },
    };
  }
  function computed(fn) { return { get value() { return fn(); } }; }
  return { ref, computed, toReactive };
}

function createEnv() {
  const context = {
    console,
    URL,
    AbortController,
    DOMException,
    Promise,
    setTimeout,
    clearTimeout,
    setInterval,
    clearInterval,
    location: { origin: 'http://localhost', hash: '#/home' },
    Vue: makeReactivity(),
  };
  context.window = context;
  context.globalThis = context;
  vm.createContext(context);
  for (const file of SCRIPTS) {
    const source = fs.readFileSync(path.join(WEB, file), 'utf8');
    vm.runInContext(source, context, { filename: file });
  }
  return context;
}

/* ================= helpers ================= */

const FLUSH = () => new Promise(resolve => setImmediate(resolve));
async function flush(times = 2) { for (let i = 0; i < times; i += 1) await FLUSH(); }

// Cross-realm copy so strict comparisons only ever see host objects.
const spread = value => ({ ...value });
const paramsOf = url => new URL('http://localhost' + url).searchParams;
const pathOf = url => url.split('?')[0];
const lib = (id, type, name) => ({ Id: id, Name: name === undefined ? id : name, CollectionType: type || '' });

function rowSnap(row) {
  return {
    key: String(row.key),
    title: String(row.title),
    libraryType: String(row.libraryType),
    status: String(row.status),
    error: String(row.error),
    partial: row.partial === true,
    items: Array.from(row.items, item => String(item.Id)),
  };
}

// Fake api: every call is a manually settled deferred, so the tests control
// exactly when a request starts settling. Physical in-flight = unsettled.
function makeApi() {
  const calls = [];
  let inflight = 0;
  let maxInflight = 0;
  function api(url, options = {}) {
    const entry = { url, signal: options.signal, settled: false };
    entry.promise = new Promise((resolve, reject) => {
      entry.resolve = value => {
        if (entry.settled) return;
        entry.settled = true;
        inflight -= 1;
        resolve(value);
      };
      entry.reject = error => {
        if (entry.settled) return;
        entry.settled = true;
        inflight -= 1;
        reject(error);
      };
    });
    inflight += 1;
    maxInflight = Math.max(maxInflight, inflight);
    calls.push(entry);
    return entry.promise;
  }
  return {
    api,
    calls,
    get inflight() { return inflight; },
    get maxInflight() { return maxInflight; },
    pending() { return calls.filter(entry => !entry.settled); },
  };
}

function makeRows(env, api, options = {}) {
  const state = {
    uid: options.uid === undefined ? 'u1' : options.uid,
    active: options.active !== false,
  };
  const rowsModule = env.window.EIOHomeLibraryRows.create({
    api: api.api,
    query: env.window.EIOData.query,
    getUserId: () => state.uid,
    isActive: () => state.active,
  });
  return { rowsModule, state };
}

async function drain(api, value = { Items: [] }) {
  let guard = 0;
  while (api.pending().length && guard < 40) {
    guard += 1;
    api.pending()[0].resolve(value);
    await flush();
  }
}

function makeLibrary(env, options = {}) {
  const calls = [];
  const libraries = env.Vue.ref([{ Id: 'lib', Name: 'L', CollectionType: options.collectionType || 'movies' }]);
  const itemsReply = options.items || (() => ({ Items: [{ Id: 'i0' }, { Id: 'i1' }], TotalRecordCount: 2 }));
  const library = env.window.EIOLibrary.create({
    api: async (url) => {
      calls.push(url);
      if (pathOf(url).endsWith('/Genres')) return { Items: [] };
      return itemsReply(url);
    },
    query: env.window.EIOData.query,
    nextTick: fn => Promise.resolve().then(() => fn && fn()),
    formatError: error => String((error && error.message) || error),
    getUserId: () => 'u1',
    isActive: () => true,
    libraries,
    view: { disconnect() {}, attachObserver() {} },
    refreshIcons() {},
  });
  return { library, calls, libraries };
}

/* ================= A1: shared builder ================= */

test('A1 EIOLibraryQuery keeps the frozen library parameters', () => {
  const env = createEnv();
  const Q = env.window.EIOLibraryQuery;
  assert.equal(Q.PAGE_LIMIT, 50);
  assert.equal(Q.FIELDS, LEGACY_FIELDS);

  assert.deepEqual(spread(Q.pageParams({ id: 'lib1', type: 'movies' })), {
    ParentId: 'lib1',
    Limit: 50,
    SortBy: 'DateCreated',
    SortOrder: 'Descending',
    Genres: '',
    Fields: LEGACY_FIELDS,
    Recursive: true,
    IncludeItemTypes: 'Movie',
    StartIndex: 0,
  });

  assert.equal(Q.pageParams({ id: 'x', type: 'tvshows' }).IncludeItemTypes, 'Series');
  assert.equal(Q.pageParams({ id: 'x', type: 'playlists' }).IncludeItemTypes, '');
  assert.equal(Q.pageParams({ id: 'x', type: 'music' }).IncludeItemTypes, '');
  assert.equal(Q.pageParams({ id: 'x' }).IncludeItemTypes, '');

  const unsorted = Q.pageParams({ id: 'x', sort: 'SortName' });
  assert.equal(unsorted.SortBy, 'SortName');
  assert.equal(unsorted.SortOrder, undefined);
  assert.ok('SortOrder' in unsorted, 'a missing direction keeps the (undefined) key');
  assert.equal(Q.pageParams({ id: 'x', sort: 'ProductionYear,Descending' }).SortOrder, 'Descending');

  assert.equal(Q.pageParams({ id: 'x', filter: 'Action' }).Genres, 'Action');
  assert.equal(Q.pageParams({ id: 'x', filter: '' }).Genres, '');

  const paged = Q.pageParams({ id: 'x', limit: 12, startIndex: 50 });
  assert.equal(paged.Limit, 12);
  assert.equal(paged.StartIndex, 50);
  assert.equal(paged.Fields, LEGACY_FIELDS, 'limit/startIndex overrides never drop Fields');

  const first = Q.pageParams({ id: 'a' });
  const second = Q.pageParams({ id: 'a' });
  assert.notEqual(first, second, 'every call returns a new object');
  assert.deepEqual(spread(first), spread(second), 'the builder keeps no state');
  first.ParentId = 'mutated';
  first.StartIndex = 99;
  assert.equal(second.ParentId, 'a');
  assert.equal(second.StartIndex, 0);
  assert.equal(Q.pageParams({ id: 'a' }).ParentId, 'a');
});

/* ================= A2: library page URL compatibility ================= */

test('A2 the library page still requests the legacy /Items URLs', async () => {
  const env = createEnv();
  const cases = [
    {
      collectionType: 'movies',
      sort: 'DateCreated,Descending',
      filter: '',
      // Legacy snapshot: every parameter in the pre-refactor order; empty
      // Genres is dropped by EIOData.query().
      expected: [
        ['ParentId', 'lib'], ['Limit', '50'], ['SortBy', 'DateCreated'], ['SortOrder', 'Descending'],
        ['Fields', LEGACY_FIELDS], ['Recursive', 'true'], ['IncludeItemTypes', 'Movie'], ['StartIndex', '0'],
      ],
    },
    {
      collectionType: 'tvshows',
      sort: 'SortName,Ascending',
      filter: 'Action',
      expected: [
        ['ParentId', 'lib'], ['Limit', '50'], ['SortBy', 'SortName'], ['SortOrder', 'Ascending'],
        ['Genres', 'Action'], ['Fields', LEGACY_FIELDS], ['Recursive', 'true'],
        ['IncludeItemTypes', 'Series'], ['StartIndex', '0'],
      ],
    },
    {
      collectionType: 'playlists',
      sort: 'ProductionYear,Descending',
      filter: '',
      expected: [
        ['ParentId', 'lib'], ['Limit', '50'], ['SortBy', 'ProductionYear'], ['SortOrder', 'Descending'],
        ['Fields', LEGACY_FIELDS], ['Recursive', 'true'], ['StartIndex', '0'],
      ],
    },
    {
      collectionType: 'music',
      sort: 'CommunityRating,Descending',
      filter: 'Drama',
      expected: [
        ['ParentId', 'lib'], ['Limit', '50'], ['SortBy', 'CommunityRating'], ['SortOrder', 'Descending'],
        ['Genres', 'Drama'], ['Fields', LEGACY_FIELDS], ['Recursive', 'true'], ['StartIndex', '0'],
      ],
    },
  ];

  for (const item of cases) {
    const { library, calls } = makeLibrary(env, { collectionType: item.collectionType });
    library.sort.value = item.sort;
    await library.load('lib');
    // load() always starts from "all": the genre filter is applied afterwards,
    // exactly like the genre pills on the library page do.
    if (item.filter) {
      library.setFilter(item.filter);
      await flush();
    }
    const requests = calls.filter(url => pathOf(url) === '/Users/u1/Items');
    assert.equal(requests.length, item.filter ? 2 : 1, `request count for ${item.collectionType}`);
    assert.deepEqual(Array.from(paramsOf(requests[requests.length - 1]).entries()), item.expected);
    assert.ok(requests.every(url => paramsOf(url).get('StartIndex') === '0'));
    assert.equal(calls.filter(url => url.indexOf('/Items/') >= 0).length, 0, 'no /Items/Latest or /Items/Resume');
    assert.equal(library.items.value.length, 2);
    assert.equal(library.hasMore.value, false, 'a complete end page keeps hasMore false');
    assert.equal(library.partial.value, false);
  }

  // Paging semantics are untouched: the cursor advances by the raw batch length
  // and the next page is a plain StartIndex=50 request with the same params.
  const paged = makeLibrary(env, {
    collectionType: 'movies',
    items: url => {
      const start = Number(paramsOf(url).get('StartIndex')) || 0;
      return {
        Items: Array.from({ length: 50 }, (unused, index) => ({ Id: 'p' + (start + index) })),
        TotalRecordCount: 120,
      };
    },
  });
  await paged.library.load('lib');
  assert.equal(paged.library.hasMore.value, true);
  await paged.library.loadMore();
  const offsets = paged.calls
    .filter(url => pathOf(url) === '/Users/u1/Items')
    .map(url => paramsOf(url).get('StartIndex'));
  assert.deepEqual(offsets, ['0', '50']);
  const pageTwo = Array.from(paramsOf(paged.calls.filter(url => pathOf(url) === '/Users/u1/Items')[1]).entries());
  assert.deepEqual(pageTwo, [
    ['ParentId', 'lib'], ['Limit', '50'], ['SortBy', 'DateCreated'], ['SortOrder', 'Descending'],
    ['Fields', LEGACY_FIELDS], ['Recursive', 'true'], ['IncludeItemTypes', 'Movie'], ['StartIndex', '50'],
  ]);
  assert.equal(paged.library.items.value.length, 100);
});

/* ================= A3: reset shape ================= */

test('A3 reactivity guard: rows.value exposes proxies, never the raw object', () => {
  const env = createEnv();
  const source = { Id: 'x', Name: 'X', CollectionType: 'movies' };
  const list = env.Vue.ref([]);
  list.value = [source];
  const read = list.value[0];
  assert.notEqual(read, source, 'the raw row object is not what rows.value returns');
  assert.equal(read, list.value[0], 'the same proxy is returned on every read');
  read.status = 'ready';
  assert.equal(source.status, 'ready', 'writing through the proxy mutates the row');
});

test('A3 reset keeps Views order, dedupes ids and keeps playlists', () => {
  const env = createEnv();
  const api = makeApi();
  const { rowsModule } = makeRows(env, api);
  rowsModule.reset([
    lib('b', 'movies', '电影 B'),
    lib('a', 'tvshows', '剧集 A'),
    lib('b', 'movies', '重复 B'),
    { Id: null, Name: 'no id' },
    { Id: '   ', Name: 'blank id' },
    { Name: 'missing id' },
    null,
    undefined,
    lib('p', 'playlists', '播放列表'),
    lib('b', 'movies', '再次重复'),
  ]);
  const rows = rowsModule.rows.value;
  assert.equal(rows.length, 3);
  assert.deepEqual(Array.from(rows, row => row.key), ['b', 'a', 'p']);
  assert.deepEqual(Array.from(rows, row => row.libraryType), ['movies', 'tvshows', 'playlists']);
  assert.deepEqual(Array.from(rows, row => row.title), ['电影 B', '剧集 A', '播放列表']);
  for (const row of rows) {
    assert.deepEqual(Array.from(Object.keys(row)).sort(), ROW_KEYS);
    assert.deepEqual(rowSnap(row), {
      key: String(row.key), title: String(row.title), libraryType: String(row.libraryType),
      status: 'idle', error: '', partial: false, items: [],
    });
  }
  assert.notEqual(rows[0].items, rows[1].items, 'each row owns its own items array');
  assert.equal(api.calls.length, 0, 'reset never requests');

  rowsModule.reset([{ Id: 'x' }]);
  const fallback = rowsModule.rows.value[0];
  assert.equal(fallback.title, '媒体库');
  assert.equal(fallback.libraryType, '');
  assert.equal(fallback.key, 'x');

  rowsModule.reset([]);
  assert.equal(rowsModule.rows.value.length, 0);
});

/* ================= A4: ensure idempotency ================= */

test('A4 ensure is idempotent and never prefetches idle rows', async () => {
  const env = createEnv();
  const api = makeApi();
  const { rowsModule } = makeRows(env, api);
  rowsModule.reset(['a', 'b', 'c', 'd', 'e', 'f', 'g', 'h', 'i', 'j'].map(key => lib(key, 'movies')));

  rowsModule.ensure('a');
  rowsModule.ensure('a');
  rowsModule.ensure('a');
  assert.equal(api.calls.length, 1, 'repeated ensure issues one request');
  assert.equal(rowsModule.rows.value[0].status, 'loading');
  assert.equal(paramsOf(api.calls[0].url).get('StartIndex'), '0');
  assert.equal(paramsOf(api.calls[0].url).get('Limit'), '50');
  assert.equal(rowsModule.rows.value.filter(row => row.status === 'idle').length, 9,
    'rows that never entered the viewport stay idle with zero requests');

  rowsModule.ensure('unknown');
  rowsModule.ensure('');
  rowsModule.ensure(null);
  rowsModule.ensure(undefined);
  assert.equal(api.calls.length, 1, 'unknown/empty ids are a no-op');

  api.calls[0].resolve({ Items: [{ Id: 'm1' }] });
  await flush();
  assert.equal(rowsModule.rows.value[0].status, 'ready');
  rowsModule.ensure('a');
  assert.equal(api.calls.length, 1, 'ready rows never prefetch');

  rowsModule.ensure('b');
  api.calls[1].resolve({ Items: [] });
  await flush();
  assert.equal(rowsModule.rows.value[1].status, 'empty');
  rowsModule.ensure('b');
  assert.equal(api.calls.length, 2, 'empty rows never re-request');

  rowsModule.ensure('c');
  api.calls[2].resolve({ Items: [{ Id: 'p1' }], EioIncompleteResults: true });
  await flush();
  assert.equal(rowsModule.rows.value[2].status, 'partial');
  rowsModule.ensure('c');
  assert.equal(api.calls.length, 3, 'partial rows only restart through retry');

  rowsModule.ensure('d');
  api.calls[3].reject(new Error('boom'));
  await flush();
  assert.equal(rowsModule.rows.value[3].status, 'error');
  rowsModule.ensure('d');
  assert.equal(api.calls.length, 4, 'error rows only restart through retry');
});

/* ================= A5: two physical slots ================= */

test('A5 at most two physical requests are in flight across all rows', async () => {
  const env = createEnv();
  const api = makeApi();
  const { rowsModule } = makeRows(env, api);
  rowsModule.reset(['a', 'b', 'c', 'd', 'e'].map(key => lib(key, 'movies')));
  for (const key of ['a', 'b', 'c', 'd', 'e']) rowsModule.ensure(key);

  assert.equal(api.calls.length, 2, 'five ensures start only two requests');
  assert.equal(api.inflight, 2);
  assert.deepEqual(Array.from(rowsModule.rows.value, row => row.status),
    ['loading', 'loading', 'loading', 'loading', 'loading']);

  api.calls[0].resolve({ Items: [{ Id: 'a1' }] });
  await flush();
  assert.equal(api.calls.length, 3, 'settling one request frees exactly one slot');
  assert.equal(api.inflight, 2);
  assert.equal(rowsModule.rows.value[0].status, 'ready');

  await drain(api, { Items: [{ Id: 'x' }] });
  assert.equal(api.calls.length, 5);
  assert.equal(api.maxInflight, 2, 'the cap holds for every start');
  assert.equal(api.inflight, 0);
  assert.deepEqual(Array.from(rowsModule.rows.value, row => row.status),
    ['ready', 'ready', 'ready', 'ready', 'ready']);
});

/* ================= A6: abort without settlement still counts ================= */

test('A6 an aborted but unsettled request keeps its slot', async () => {
  const env = createEnv();
  const api = makeApi();
  const { rowsModule } = makeRows(env, api);
  const libraries = ['a', 'b', 'c'].map(key => lib(key, 'movies'));

  rowsModule.reset(libraries);
  for (const key of ['a', 'b', 'c']) rowsModule.ensure(key);
  assert.equal(api.calls.length, 2);

  rowsModule.cancel();
  assert.equal(rowsModule.rows.value.length, 0);
  assert.ok(api.calls.every(entry => entry.signal.aborted), 'cancel aborts in-flight requests');

  // The api ignores the signal and never settles: both slots stay occupied.
  rowsModule.reset(libraries);
  for (const key of ['a', 'b', 'c']) rowsModule.ensure(key);
  assert.equal(api.calls.length, 2, 'no third request while aborted work is unsettled');
  assert.ok(rowsModule.rows.value.every(row => row.status === 'loading'));

  api.calls[0].resolve({ Items: [{ Id: 'old' }] });
  await flush();
  assert.equal(api.calls.length, 3, 'only the settled slot starts the queued row');
  assert.equal(api.inflight, 2);
  assert.ok(rowsModule.rows.value.every(row => row.items.length === 0),
    'the stale generation never writes items');

  await drain(api, { Items: [{ Id: 'new' }] });
  assert.equal(api.maxInflight, 2);
  assert.equal(api.inflight, 0);
  assert.ok(rowsModule.rows.value.every(row => row.status === 'ready'));
});

/* ================= A7: late responses ================= */

test('A7a a cancelled row never receives its late response', async () => {
  const env = createEnv();
  const api = makeApi();
  const { rowsModule } = makeRows(env, api);
  rowsModule.reset([lib('a')]);
  rowsModule.ensure('a');
  rowsModule.cancel();
  assert.equal(rowsModule.rows.value.length, 0);
  api.calls[0].resolve({ Items: [{ Id: 'late' }] });
  await flush();
  assert.equal(rowsModule.rows.value.length, 0);
  assert.equal(api.inflight, 0);
});

test('A7b a response for a replaced row object is dropped', async () => {
  const env = createEnv();
  const api = makeApi();
  const { rowsModule } = makeRows(env, api);
  rowsModule.reset([lib('a')]);
  const oldRow = rowsModule.rows.value[0];
  rowsModule.ensure('a');

  rowsModule.reset([lib('a')]); // same key, brand-new row object
  const newRow = rowsModule.rows.value[0];
  assert.notEqual(oldRow, newRow);
  rowsModule.ensure('a');
  assert.equal(api.calls.length, 2);

  api.calls[0].resolve({ Items: [{ Id: 'old' }] }); // late response, old row
  await flush();
  assert.deepEqual(Array.from(newRow.items), []);
  assert.equal(newRow.status, 'loading', 'the stale finally path must not finish the new row');
  assert.equal(api.calls.length, 2);

  api.calls[1].resolve({ Items: [{ Id: 'fresh' }] });
  await flush();
  assert.equal(newRow.status, 'ready');
  assert.deepEqual(Array.from(newRow.items, item => item.Id), ['fresh']);
  assert.deepEqual(Array.from(oldRow.items), [], 'the orphaned row object is never written');
});

test('A7c an abort-ignoring api cannot overwrite after cancel/reset', async () => {
  const env = createEnv();
  const api = makeApi();
  const { rowsModule } = makeRows(env, api);
  rowsModule.reset([lib('a')]);
  rowsModule.ensure('a');
  rowsModule.cancel(); // aborts; the fake api resolves anyway
  rowsModule.reset([lib('a'), lib('b')]);
  rowsModule.ensure('a');
  assert.equal(api.calls.length, 2);

  api.calls[0].resolve({ Items: [{ Id: 'ghost' }] });
  await flush();
  const row = rowsModule.rows.value[0];
  assert.deepEqual(Array.from(row.items), []);
  assert.equal(row.status, 'loading');

  api.calls[1].resolve({ Items: [{ Id: 'real' }] });
  await flush();
  assert.equal(rowsModule.rows.value[0].status, 'ready');
  assert.deepEqual(Array.from(rowsModule.rows.value[0].items, item => item.Id), ['real']);
  assert.equal(rowsModule.rows.value[1].status, 'idle');
});

test('A7d a user change blocks the late commit but still frees the slot', async () => {
  const env = createEnv();
  const api = makeApi();
  const { rowsModule, state } = makeRows(env, api);
  rowsModule.reset([lib('a')]);
  rowsModule.ensure('a');
  assert.equal(pathOf(api.calls[0].url), '/Users/u1/Items');

  state.uid = 'u2';
  api.calls[0].resolve({ Items: [{ Id: 'other-user' }] });
  await flush();
  const row = rowsModule.rows.value[0];
  assert.deepEqual(Array.from(row.items), []);
  assert.notEqual(row.status, 'ready');
  assert.equal(row.partial, false);

  rowsModule.reset([lib('a')]);
  rowsModule.ensure('a');
  assert.equal(api.calls.length, 2, 'the slot was released even without a commit');
  assert.equal(pathOf(api.calls[1].url), '/Users/u2/Items');
});

test('A7e an inactive page never starts a request and returns the row to idle', async () => {
  const env = createEnv();
  const api = makeApi();
  const { rowsModule, state } = makeRows(env, api);
  rowsModule.reset([lib('a')]);
  state.active = false;
  rowsModule.ensure('a');
  assert.equal(api.calls.length, 0, 'no request is spent on an inactive page');
  assert.equal(rowsModule.rows.value[0].status, 'idle', 'the row is released for a later ensure');
  await flush();
  assert.equal(api.calls.length, 0);
  assert.equal(rowsModule.rows.value[0].status, 'idle');

  state.active = true;
  rowsModule.ensure('a');
  assert.equal(api.calls.length, 1);
  assert.equal(rowsModule.rows.value[0].status, 'loading');
  api.calls[0].resolve({ Items: [{ Id: 'm1' }] });
  await flush();
  assert.equal(rowsModule.rows.value[0].status, 'ready');
});

test('A7f a queued row returns to idle when its page became inactive', async () => {
  const env = createEnv();
  const api = makeApi();
  const { rowsModule, state } = makeRows(env, api);
  rowsModule.reset([lib('a'), lib('b'), lib('c')]);
  rowsModule.ensure('a');
  rowsModule.ensure('b');
  rowsModule.ensure('c');
  assert.equal(api.calls.length, 2);
  assert.equal(rowsModule.rows.value[2].status, 'loading', 'a queued row waits as loading');

  state.active = false;
  api.calls[0].resolve({ Items: [{ Id: 'a1' }] });
  await flush();
  assert.equal(api.calls.length, 2, 'the queued row never starts on an inactive page');
  assert.equal(rowsModule.rows.value[0].status, 'ready', 'a settled row still commits; isActive only gates starts');
  assert.equal(rowsModule.rows.value[2].status, 'idle');
});

/* ================= A8: order, dedupe, 50 cap ================= */

test('A8 first page keeps response order, dedupes and truncates at 50', async () => {
  const env = createEnv();
  const api = makeApi();
  const { rowsModule } = makeRows(env, api);
  rowsModule.reset([lib('a', 'movies')]);
  rowsModule.ensure('a');

  const ids = [];
  for (let index = 0; index < 55; index += 1) ids.push('id' + index);
  ids.splice(3, 0, 'id0', 'id1'); // late duplicates must be dropped
  api.calls[0].resolve({ Items: ids.map(id => ({ Id: id, Name: id })), TotalRecordCount: 999 });
  await flush();

  const row = rowsModule.rows.value[0];
  const seen = Array.from(row.items, item => item.Id);
  const firstOccurrence = [];
  for (const id of ids) if (!firstOccurrence.includes(id)) firstOccurrence.push(id);
  assert.equal(firstOccurrence.length, 55);
  assert.equal(seen.length, 50);
  assert.deepEqual(seen, firstOccurrence.slice(0, 50));
  assert.equal(api.calls.length, 1, 'no automatic second page');
  assert.equal(paramsOf(api.calls[0].url).get('StartIndex'), '0');
  assert.equal(paramsOf(api.calls[0].url).get('Limit'), '50');
  assert.equal(paramsOf(api.calls[0].url).get('IncludeItemTypes'), 'Movie');
  assert.ok(!('TotalRecordCount' in row), 'the total is never stored on the row');
});

/* ================= A9: empty vs partial ================= */

test('A9 empty and partial first pages stay distinguishable', async () => {
  const env = createEnv();
  const api = makeApi();
  const { rowsModule } = makeRows(env, api);
  rowsModule.reset([lib('empty'), lib('partialEmpty'), lib('partialItems')]);

  rowsModule.ensure('empty');
  api.calls[0].resolve({ Items: [], TotalRecordCount: 0 });
  await flush();
  const empty = rowsModule.rows.value[0];
  assert.equal(empty.status, 'empty');
  assert.equal(empty.partial, false);
  assert.equal(empty.items.length, 0);

  rowsModule.ensure('partialEmpty');
  api.calls[1].resolve({ Items: [], TotalRecordCount: 12, EioIncompleteResults: true });
  await flush();
  const partialEmpty = rowsModule.rows.value[1];
  assert.equal(partialEmpty.status, 'partial', 'an empty incomplete page is partial, not empty');
  assert.equal(partialEmpty.partial, true);
  assert.equal(partialEmpty.items.length, 0);

  rowsModule.ensure('partialItems');
  api.calls[2].resolve({
    Items: [{ Id: 'x1' }, { Id: 'x1' }, { Id: 'x2' }],
    TotalRecordCount: 40,
    EioIncompleteResults: true,
  });
  await flush();
  const partialItems = rowsModule.rows.value[2];
  assert.equal(partialItems.status, 'partial');
  assert.equal(partialItems.partial, true);
  assert.deepEqual(Array.from(partialItems.items, item => item.Id), ['x1', 'x2']);

  for (const row of rowsModule.rows.value) {
    assert.ok(!('TotalRecordCount' in row));
    const serialized = JSON.stringify(rowSnap(row));
    assert.ok(!serialized.includes('TotalRecordCount'));
    assert.ok(!serialized.includes('共'));
  }
});

/* ================= A10: errors and retry ================= */

test('A10 errors keep old items and retry re-requests only the first page', async () => {
  const env = createEnv();
  const api = makeApi();
  const { rowsModule } = makeRows(env, api);
  rowsModule.reset([lib('a')]);
  rowsModule.ensure('a');
  api.calls[0].reject(new Error('boom'));
  await flush();

  const row = rowsModule.rows.value[0];
  assert.equal(row.status, 'error');
  assert.equal(row.error, ERROR_TEXT);
  assert.equal(row.partial, false);
  assert.deepEqual(Array.from(row.items), []);

  rowsModule.ensure('a');
  assert.equal(api.calls.length, 1, 'ensure cannot restart an error row');

  rowsModule.retry('a');
  assert.equal(api.calls.length, 2);
  assert.equal(row.status, 'loading');
  assert.equal(row.error, '');
  assert.equal(row.partial, false);
  assert.equal(paramsOf(api.calls[1].url).get('StartIndex'), '0');
  assert.equal(paramsOf(api.calls[1].url).get('Limit'), '50');

  rowsModule.ensure('a');
  rowsModule.retry('a');
  assert.equal(api.calls.length, 2, 'ensure/retry are no-ops while the retry runs');

  api.calls[1].resolve({ Items: [{ Id: 'r1' }, { Id: 'r2' }] });
  await flush();
  assert.equal(row.status, 'ready');
  assert.deepEqual(Array.from(row.items, item => item.Id), ['r1', 'r2']);

  // Partial -> retry keeps the old items on screen; a failure keeps them too.
  rowsModule.reset([lib('p')]);
  rowsModule.ensure('p');
  api.calls[2].resolve({ Items: [{ Id: 'p1' }, { Id: 'p2' }], EioIncompleteResults: true });
  await flush();
  const partialRow = rowsModule.rows.value[0];
  assert.equal(partialRow.status, 'partial');

  rowsModule.retry('p');
  assert.equal(partialRow.status, 'loading');
  assert.equal(partialRow.partial, false);
  assert.deepEqual(Array.from(partialRow.items, item => item.Id), ['p1', 'p2'],
    'the previous first page survives while the retry runs');
  api.calls[3].reject(new Error('still broken'));
  await flush();
  assert.equal(partialRow.status, 'error');
  assert.equal(partialRow.error, ERROR_TEXT);
  assert.deepEqual(Array.from(partialRow.items, item => item.Id), ['p1', 'p2'],
    'a failed retry keeps the old items');

  rowsModule.retry('p');
  api.calls[4].resolve({ Items: [{ Id: 'fresh' }] });
  await flush();
  assert.equal(partialRow.status, 'ready');
  assert.equal(partialRow.partial, false);
  assert.deepEqual(Array.from(partialRow.items, item => item.Id), ['fresh'],
    'a successful retry replaces the page instead of appending');

  for (const index of [2, 3, 4]) {
    const params = paramsOf(api.calls[index].url);
    assert.equal(params.get('StartIndex'), '0');
    assert.equal(params.get('Limit'), '50');
  }

  const settled = api.calls.length;
  rowsModule.retry('p'); // ready: no-op
  rowsModule.retry('idle-unknown'); // unknown: no-op
  assert.equal(api.calls.length, settled);
  assert.equal(rowsModule.rows.value[0].status, 'ready');
});

/* ================= A11: cancel / reset / dispose ================= */

test('A11 cancel, reset and dispose clear rows, abort and release slots', async () => {
  const env = createEnv();
  const api = makeApi();
  const { rowsModule } = makeRows(env, api);
  rowsModule.reset(['a', 'b', 'c'].map(key => lib(key, 'movies')));
  for (const key of ['a', 'b', 'c']) rowsModule.ensure(key);
  assert.equal(api.calls.length, 2);

  rowsModule.cancel();
  assert.equal(rowsModule.rows.value.length, 0);
  assert.ok(api.calls.every(entry => entry.signal.aborted));

  // Aborted-but-unsettled work keeps both slots.
  rowsModule.reset([lib('a'), lib('b')]);
  rowsModule.ensure('a');
  rowsModule.ensure('b');
  assert.equal(api.calls.length, 2);

  api.calls[0].resolve({ Items: [] });
  api.calls[1].resolve({ Items: [] });
  await flush();
  assert.equal(api.calls.length, 4, 'both new rows start once the old slots settle');
  assert.equal(api.inflight, 2);

  api.calls[2].resolve({ Items: [] });
  api.calls[3].resolve({ Items: [] });
  await flush();
  assert.equal(api.inflight, 0);
  assert.deepEqual(Array.from(rowsModule.rows.value, row => row.status), ['empty', 'empty']);

  // reset aborts the previous generation and replaces the rows.
  rowsModule.reset([lib('a')]);
  rowsModule.ensure('a');
  assert.equal(api.calls.length, 5);
  rowsModule.reset([lib('b')]);
  assert.ok(api.calls[4].signal.aborted);
  assert.deepEqual(Array.from(rowsModule.rows.value, row => row.key), ['b']);
  assert.equal(rowsModule.rows.value[0].status, 'idle');

  api.calls[4].resolve({ Items: [{ Id: 'late' }] });
  await flush();
  assert.deepEqual(Array.from(rowsModule.rows.value[0].items), []);

  // A fresh request still starts: the aborted slot was released.
  rowsModule.ensure('b');
  assert.equal(api.calls.length, 6);
  api.calls[5].resolve({ Items: [{ Id: 'b1' }] });
  await flush();
  assert.equal(rowsModule.rows.value[0].status, 'ready');

  rowsModule.cancel();
  rowsModule.dispose();
  const before = api.calls.length;
  rowsModule.ensure('b');
  rowsModule.retry('b');
  rowsModule.reset([lib('c')]);
  rowsModule.cancel();
  await flush();
  assert.equal(api.calls.length, before, 'a disposed module accepts no new work');
  assert.equal(rowsModule.rows.value.length, 0);
  assert.equal(api.inflight, 0);
  assert.equal(api.maxInflight, 2);
});

/* ================= A12: page-state integration ================= */

test('A12 createHome exposes libraryRows while the hero Latest channel is unchanged', async () => {
  const env = createEnv();
  const views = [
    { Id: 'mv1', Name: '电影一', CollectionType: 'movies' },
    { Id: 'pl', Name: '播放列表', CollectionType: 'playlists' },
    { Id: 'tv1', Name: '剧集一', CollectionType: 'tvshows' },
    { Id: 'mv2', Name: '电影二', CollectionType: 'movies' },
    { Id: 'mv3', Name: '电影三', CollectionType: 'movies' },
  ];
  const calls = [];
  const api = (url, options = {}) => {
    calls.push({ url, signal: options.signal });
    const target = pathOf(url);
    if (target.endsWith('/Views')) return Promise.resolve({ Items: views });
    if (target.endsWith('/Items/Latest')) {
      return Promise.resolve([{ Id: 'latest-' + paramsOf(url).get('ParentId') }]);
    }
    if (target.endsWith('/Items/Resume')) return Promise.resolve({ Items: [{ Id: 'resume-1' }] });
    return Promise.resolve({ Items: [{ Id: 'row-' + paramsOf(url).get('ParentId') }] });
  };
  const libraries = env.Vue.ref([]);
  const latestSections = env.Vue.ref([]);
  const home = env.window.EIOPages.createHome({
    api,
    query: env.window.EIOData.query,
    getUserId: () => 'u1',
    isActive: () => true,
    libraries,
    latestSections,
  });

  assert.ok(home.libraryRows && home.libraryRows.rows, 'createHome returns libraryRows');
  await home.load();
  await flush();

  const latestCalls = () => calls.filter(call => pathOf(call.url).endsWith('/Items/Latest'));
  const rowCalls = () => calls.filter(call => pathOf(call.url) === '/Users/u1/Items');

  // Hero channel: still at most three non-playlist Latest requests, Limit=12.
  const latest = latestCalls();
  assert.equal(latest.length, 3);
  const latestParams = latest.map(call => paramsOf(call.url));
  assert.deepEqual(latestParams.map(params => params.get('ParentId')), ['mv1', 'tv1', 'mv2']);
  assert.ok(latestParams.every(params => params.get('Limit') === '12'));
  assert.deepEqual(Array.from(latestSections.value, section => section.key).sort(),
    ['mv1', 'mv2', 'tv1'], 'latestSections still come from /Items/Latest only');

  // Row channel: every visible library, in Views order, playlists included.
  const rows = home.libraryRows.rows.value;
  assert.deepEqual(Array.from(rows, row => row.key), ['mv1', 'pl', 'tv1', 'mv2', 'mv3']);
  assert.deepEqual(Array.from(rows, row => row.libraryType),
    ['movies', 'playlists', 'tvshows', 'movies', 'movies']);
  assert.ok(rows.every(row => row.status === 'idle'));
  assert.equal(rowCalls().length, 0, 'nothing below the fold is prefetched');

  // A playlist row loads through the complete-library first page, not Latest.
  home.libraryRows.ensure('pl');
  await flush();
  assert.equal(rowCalls().length, 1);
  const playlistParams = paramsOf(rowCalls()[0].url);
  assert.equal(playlistParams.get('ParentId'), 'pl');
  assert.equal(playlistParams.get('Limit'), '50');
  assert.equal(playlistParams.get('StartIndex'), '0');
  assert.equal(playlistParams.get('Recursive'), 'true');
  assert.equal(playlistParams.get('SortBy'), 'DateCreated');
  assert.equal(playlistParams.get('SortOrder'), 'Descending');
  assert.equal(playlistParams.has('IncludeItemTypes'), false);
  assert.equal(latestCalls().length, 3, 'a row ensure never issues a Latest request');

  home.libraryRows.ensure('mv1');
  await flush();
  assert.equal(rowCalls().length, 2);
  assert.equal(paramsOf(rowCalls()[1].url).get('IncludeItemTypes'), 'Movie');
  home.libraryRows.ensure('tv1');
  await flush();
  assert.equal(paramsOf(rowCalls()[2].url).get('IncludeItemTypes'), 'Series');

  home.cancel();
  assert.equal(home.libraryRows.rows.value.length, 0, 'cancel clears the rows');
  assert.equal(home.viewsLoading.value, false);

  await home.load();
  await flush();
  assert.equal(home.libraryRows.rows.value.length, 5, 'a committed Views response rebuilds the rows');
  assert.equal(latestCalls().length, 6);
  assert.equal(Array.from(latestSections.value).length, 3);

  home.dispose();
  const before = calls.length;
  home.libraryRows.ensure('mv1');
  home.libraryRows.retry('mv1');
  home.libraryRows.reset([{ Id: 'x' }]);
  await flush();
  assert.equal(calls.length, before, 'a disposed home issues no further row requests');
  assert.equal(home.libraryRows.rows.value.length, 0);
});
