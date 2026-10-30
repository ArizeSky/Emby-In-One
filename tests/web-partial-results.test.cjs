'use strict';
/* W04 — partial merged candidate sets on the Web client.
 *
 * The backend marks a merged response with EioIncompleteResults=true when the
 * candidate set is a lower bound. These tests exercise only data.js and
 * library-state.js in a VM with a fake API: no harness/app.js changes, no
 * network, no upstream.
 */
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const WEB = path.join(__dirname, '..', 'public', 'web');
const flush = () => new Promise(resolve => setTimeout(resolve, 0));
const ids = list => Array.from(list, x => x.Id);

async function until(predicate, label) {
  for (let i = 0; i < 50; i++) {
    if (predicate()) return;
    await flush();
  }
  throw new Error('timed out waiting for ' + (label || 'condition'));
}

function createRuntime(reply, options = {}) {
  const calls = [];
  const attached = [];
  const context = {
    URL, AbortController, DOMException, console, Math, JSON, Promise, Set, Map, Date,
    setTimeout, clearTimeout,
    location: { origin: 'http://localhost' },
    Vue: {
      ref: value => ({ value }),
      computed: fn => ({ get value() { return fn(); } }),
      nextTick: fn => Promise.resolve().then(() => fn && fn()),
    },
  };
  context.window = context;
  vm.createContext(context);
  vm.runInContext(fs.readFileSync(path.join(WEB, 'data.js'), 'utf8'), context);
  vm.runInContext(fs.readFileSync(path.join(WEB, 'library-state.js'), 'utf8'), context);
  const libraries = { value: options.libraries || [
    { Id: 'lib-a', Name: 'Library A', CollectionType: 'movies' },
    { Id: 'lib-b', Name: 'Library B', CollectionType: 'tvshows' },
  ] };
  const api = options.api || (async (url, opts) => {
    if (opts && opts.signal && opts.signal.aborted) throw new DOMException('aborted', 'AbortError');
    const parsed = new URL(url, 'http://localhost');
    calls.push(parsed);
    return reply(parsed, opts, calls.length);
  });
  const lib = context.EIOLibrary.create({
    api,
    query: context.EIOData.query,
    nextTick: fn => Promise.resolve().then(() => fn && fn()),
    formatError: e => (e && e.message) || String(e),
    getUserId: () => 'user',
    isActive: options.isActive || (() => true),
    libraries,
    view: {
      disconnect() {},
      setSentinel() {},
      attachObserver(cb) { attached.push(cb); },
    },
    refreshIcons() {},
  });
  return { context, data: context.EIOData, lib, calls, attached, libraries };
}

function deferredApi() {
  const pending = [];
  const api = (url, opts) => {
    const entry = { url: new URL(url, 'http://localhost') };
    entry.promise = new Promise((resolve, reject) => { entry.resolve = resolve; entry.reject = reject; });
    if (opts && opts.signal) {
      opts.signal.addEventListener('abort', () => entry.reject(new DOMException('aborted', 'AbortError')));
    }
    pending.push(entry);
    return entry.promise;
  };
  const itemRequests = () => pending.filter(entry => entry.url.pathname.endsWith('/Items'));
  return { api, pending, itemRequests };
}

const itemUrl = text => new URL(text, 'http://localhost');
const offsetOf = text => Number(itemUrl(text).searchParams.get('StartIndex'));

test('one item at Total=1 with incomplete=true is partial, not "loaded everything"', async () => {
  const partialPayload = { Items: [{ Id: 'only' }], TotalRecordCount: 1, EioIncompleteResults: true };
  const { lib, data, attached } = createRuntime(url => (url.pathname === '/Genres' ? { Items: [] } : partialPayload));

  await lib.load('lib-a');
  await flush();
  assert.equal(lib.partial.value, true);
  assert.equal(lib.hasMore.value, false, 'the lower bound is reached, but the listing is still partial');
  assert.equal(lib.countText.value, '至少 1 项（部分结果）');
  assert.match(lib.partialNotice.value, /^部分结果/);
  assert.match(lib.partialNotice.value, /可能不完整/);
  assert.equal(lib.partialCanContinue.value, true);
  assert.equal(attached.length, 0, 'partial must not arm the auto-paging observer');

  await assert.rejects(
    data.all(async () => partialPayload, '/Users/user/Items', {}, { signal: new AbortController().signal }),
    err => {
      assert.equal(err.name, 'PartialResultsError');
      assert.ok(err instanceof data.PartialResultsError);
      assert.deepEqual(ids(err.items), ['only']);
      assert.equal(err.partial, true);
      assert.equal(err.lowerBound, 1);
      assert.match(err.message, /部分结果/);
      return true;
    },
  );
});

test('the same one-item payload without the marker still ends normally', async () => {
  const completePayload = { Items: [{ Id: 'only' }], TotalRecordCount: 1 };
  const { lib, data, attached } = createRuntime(url => (url.pathname === '/Genres' ? { Items: [] } : completePayload));

  await lib.load('lib-a');
  await flush();
  assert.equal(lib.partial.value, false);
  assert.equal(lib.hasMore.value, false);
  assert.equal(lib.countText.value, '共 1 项');
  assert.equal(lib.partialNotice.value, '');
  assert.equal(lib.partialCanContinue.value, false);

  const items = await data.all(async () => completePayload, '/Users/user/Items');
  assert.deepEqual(ids(items), ['only']);
});

test('partial growth continues only on user action and never via the observer', async () => {
  const pages = [
    { Items: [{ Id: 'a' }], TotalRecordCount: 1, EioIncompleteResults: true },
    { Items: [{ Id: 'b' }], TotalRecordCount: 2, EioIncompleteResults: true },
    { Items: [{ Id: 'c' }], TotalRecordCount: 3 },
  ];
  const { lib, calls, attached } = createRuntime(url => {
    if (url.pathname === '/Genres') return { Items: [] };
    return pages[Number(url.searchParams.get('StartIndex'))];
  });

  await lib.load('lib-a');
  await flush();
  assert.deepEqual(ids(lib.items.value), ['a']);
  assert.equal(lib.partial.value, true);
  assert.equal(lib.hasMore.value, false, 'Total=1 at the first page is only a lower bound');
  assert.equal(attached.length, 0);
  assert.equal(calls.filter(url => url.pathname.endsWith('/Items')).length, 1);

  await lib.continuePartial();
  await flush();
  assert.deepEqual(ids(lib.items.value), ['a', 'b']);
  assert.equal(lib.partial.value, true);
  assert.equal(lib.hasMore.value, false);
  assert.equal(lib.partialStalled.value, false);
  assert.equal(attached.length, 0, 'user-driven pages do not re-arm auto-paging');
  assert.equal(calls.filter(url => url.pathname.endsWith('/Items')).length, 2);

  await lib.continuePartial();
  await flush();
  assert.deepEqual(ids(lib.items.value), ['a', 'b', 'c']);
  assert.equal(lib.partial.value, false, 'an explicitly complete end page confirms the listing');
  assert.equal(lib.hasMore.value, false);
  assert.equal(lib.countText.value, '共 3 项');
  assert.equal(calls.filter(url => url.pathname.endsWith('/Items')).length, 3);
});

test('partial stays sticky across an intermediate complete page and clears at the complete end', async () => {
  const pages = [
    { Items: [{ Id: 'a' }], TotalRecordCount: 3, EioIncompleteResults: true },
    { Items: [{ Id: 'b' }], TotalRecordCount: 3 },
    { Items: [{ Id: 'c' }], TotalRecordCount: 3 },
  ];
  const { lib } = createRuntime(url => {
    if (url.pathname === '/Genres') return { Items: [] };
    return pages[Number(url.searchParams.get('StartIndex'))];
  });

  await lib.load('lib-a');
  await lib.continuePartial();
  assert.deepEqual(ids(lib.items.value), ['a', 'b']);
  assert.equal(lib.partial.value, true, 'an intermediate complete page does not erase earlier partial results');

  await lib.continuePartial();
  assert.deepEqual(ids(lib.items.value), ['a', 'b', 'c']);
  assert.equal(lib.partial.value, false);
  assert.equal(lib.countText.value, '共 3 项');
});

test('repeated or empty partial pages keep a retry prompt instead of auto-looping', async () => {
  const pages = [
    { Items: [{ Id: 'a' }], TotalRecordCount: 2, EioIncompleteResults: true },
    { Items: [{ Id: 'a' }], TotalRecordCount: 2, EioIncompleteResults: true },
    { Items: [], TotalRecordCount: 2, EioIncompleteResults: true },
  ];
  let itemCall = 0;
  const { lib, calls, attached } = createRuntime(url => {
    if (url.pathname === '/Genres') return { Items: [] };
    return pages[itemCall++] || pages[2];
  });

  await lib.load('lib-a');
  assert.equal(lib.partial.value, true);
  assert.equal(lib.partialStalled.value, false);

  await lib.continuePartial();
  await flush();
  assert.deepEqual(ids(lib.items.value), ['a'], 'duplicates are not appended');
  assert.equal(lib.partial.value, true);
  assert.equal(lib.partialStalled.value, true);
  assert.match(lib.partialNotice.value, /未发现更多新内容/);
  assert.equal(lib.pageError.value, '');
  assert.equal(attached.length, 0);
  assert.equal(calls.filter(url => url.pathname.endsWith('/Items')).length, 2);

  await lib.continuePartial();
  await flush();
  assert.equal(lib.partial.value, true);
  assert.equal(lib.partialStalled.value, true);
  assert.equal(lib.hasMore.value, false);
  assert.equal(attached.length, 0);
  assert.equal(calls.filter(url => url.pathname.endsWith('/Items')).length, 3);
});

test('switching library or filter clears stale partial state and aborted pages cannot land', async () => {
  const source = deferredApi();
  const runtime = createRuntime(null, { api: source.api });
  const requests = source.pending;
  const items = () => requests.filter(entry => entry.url.pathname.endsWith('/Items'));

  const loadA = runtime.lib.load('lib-a');
  await until(() => items().length === 1, 'first items page');
  items()[0].resolve({ Items: [{ Id: 'a' }], TotalRecordCount: 2, EioIncompleteResults: true });
  await loadA;
  await until(() => runtime.lib.partial.value, 'partial state');
  assert.equal(runtime.lib.partialCanContinue.value, true);

  const continuing = runtime.lib.continuePartial();
  await until(() => items().length === 2, 'partial continuation');
  assert.equal(items()[1].url.searchParams.get('StartIndex'), '1');

  const loadB = runtime.lib.load('lib-b');
  assert.equal(runtime.lib.partial.value, false, 'switching library resets partial synchronously');
  await until(() => items().length === 3, 'library B first page');
  items()[2].resolve({ Items: [{ Id: 'b1' }], TotalRecordCount: 1 });
  await loadB;
  await continuing;
  await flush();
  assert.deepEqual(ids(runtime.lib.items.value), ['b1']);
  assert.equal(runtime.lib.partial.value, false);
  assert.equal(runtime.lib.countText.value, '共 1 项');

  const filterRuntime = createRuntime(url => {
    if (url.pathname === '/Genres') return { Items: [] };
    if (url.searchParams.get('Genres') === 'drama') return { Items: [{ Id: 'drama-1' }], TotalRecordCount: 1 };
    return { Items: [{ Id: 'all-1' }], TotalRecordCount: 5, EioIncompleteResults: true };
  });
  await filterRuntime.lib.load('lib-a');
  assert.equal(filterRuntime.lib.partial.value, true);
  filterRuntime.lib.setFilter('drama');
  assert.equal(filterRuntime.lib.partial.value, false, 'changing filter resets partial synchronously');
  await until(() => filterRuntime.lib.items.value[0]?.Id === 'drama-1', 'filtered page');
  assert.equal(filterRuntime.lib.partial.value, false);
  const filtered = filterRuntime.calls.filter(url => url.pathname.endsWith('/Items')).at(-1);
  assert.equal(filtered.searchParams.get('Genres'), 'drama');
  assert.equal(filtered.searchParams.get('StartIndex'), '0');
});

test('an explicitly complete empty append page clears a stale partial state', async () => {
  const pages = [
    { Items: [{ Id: 'a' }, { Id: 'b' }], TotalRecordCount: 3, EioIncompleteResults: true },
    { Items: [], TotalRecordCount: 2 },
  ];
  const { lib, calls } = createRuntime(url => {
    if (url.pathname === '/Genres') return { Items: [] };
    return pages[offsetOf(url.href) === 0 ? 0 : 1];
  });

  await lib.load('lib-a');
  assert.equal(lib.partial.value, true);
  assert.equal(lib.hasMore.value, true);

  await lib.continuePartial();
  assert.equal(lib.partial.value, false, 'the server explicitly confirmed the complete end');
  assert.equal(lib.partialStalled.value, false);
  assert.equal(lib.hasMore.value, false);
  assert.equal(lib.retryAppend.value, false);
  assert.equal(lib.pageError.value, '');
  assert.deepEqual(ids(lib.items.value), ['a', 'b'], 'complete end keeps the loaded items');
  assert.equal(lib.countText.value, '共 2 项');
  assert.equal(calls.filter(url => url.pathname.endsWith('/Items')).length, 2);
});

test('an empty complete page before the reported total is an anomaly, not the end', async () => {
  const pages = [
    { Items: [{ Id: 'a' }, { Id: 'b' }], TotalRecordCount: 4, EioIncompleteResults: true },
    { Items: [], TotalRecordCount: 4 },
  ];
  const { lib, calls, attached } = createRuntime(url => {
    if (url.pathname === '/Genres') return { Items: [] };
    return pages[offsetOf(url.href) === 0 ? 0 : 1];
  });

  await lib.load('lib-a');
  await lib.continuePartial();
  await flush();
  assert.equal(lib.partial.value, true, 'a short empty page is not proof of completeness');
  assert.equal(lib.partialStalled.value, true);
  assert.equal(lib.hasMore.value, false, 'no automatic retry of the same stalled window');
  assert.equal(lib.retryAppend.value, true);
  assert.match(lib.pageError.value, /分页未推进/);
  assert.equal(lib.error.value, '');
  assert.deepEqual(ids(lib.items.value), ['a', 'b'], 'existing results are not cleared');
  assert.match(lib.countText.value, /^至少 4 项（部分结果）$/);
  assert.equal(attached.length, 0);
  assert.equal(calls.filter(url => url.pathname.endsWith('/Items')).length, 2);
});

test('known-total partial paging advances the cursor on each continuePartial', async () => {
  const pages = [
    { Items: [{ Id: 'a' }], TotalRecordCount: 3, EioIncompleteResults: true },
    { Items: [{ Id: 'b' }], TotalRecordCount: 3, EioIncompleteResults: true },
    { Items: [{ Id: 'c' }], TotalRecordCount: 3 },
  ];
  const { lib, calls, attached } = createRuntime(url => {
    if (url.pathname === '/Genres') return { Items: [] };
    return pages[offsetOf(url.href)];
  });
  const itemCalls = () => calls.filter(url => url.pathname.endsWith('/Items'));

  await lib.load('lib-a');
  assert.equal(lib.hasMore.value, true, 'lower bound total still has a cursor');
  await lib.continuePartial();
  await flush();
  assert.deepEqual(ids(lib.items.value), ['a', 'b']);
  assert.equal(lib.partial.value, true);

  await lib.continuePartial();
  await flush();
  assert.deepEqual(ids(lib.items.value), ['a', 'b', 'c']);
  assert.equal(lib.partial.value, false);
  assert.equal(lib.countText.value, '共 3 项');
  assert.deepEqual(itemCalls().map(url => url.searchParams.get('StartIndex')), ['0', '1', '2']);
  assert.equal(itemCalls().length, 3, 'each continue fetches exactly one new window');
  assert.equal(attached.length, 0);
});

test('all() is bounded and carries usable items for a growing partial total', async () => {
  const { data } = createRuntime(() => ({ Items: [] }));
  const offsets = [];
  await assert.rejects(
    data.all(async text => {
      const offset = offsetOf(text);
      offsets.push(offset);
      return { Items: [{ Id: 'item-' + offset }], TotalRecordCount: offset + 2, EioIncompleteResults: true };
    }, '/Users/user/Items', {}, { maxPages: 3 }),
    err => {
      assert.equal(err.name, 'PartialResultsError');
      assert.deepEqual(ids(err.items), ['item-0', 'item-1', 'item-2']);
      return true;
    },
  );
  assert.deepEqual(offsets, [0, 1, 2], 'bounded paging never exceeds maxPages');
});

test('all() returns full results when the final page proves the listing complete', async () => {
  const { data } = createRuntime(() => ({ Items: [] }));
  const offsets = [];
  const results = await data.all(async text => {
    const offset = offsetOf(text);
    offsets.push(offset);
    if (offset === 0) return { Items: [{ Id: 'a' }], TotalRecordCount: 2, EioIncompleteResults: true };
    return { Items: [{ Id: 'b' }], TotalRecordCount: 2 };
  }, '/Users/user/Items');
  assert.deepEqual(ids(results), ['a', 'b']);
  assert.deepEqual(offsets, [0, 1]);
});

test('all() throws partial results for empty or repeated pages that stay incomplete', async () => {
  const { data } = createRuntime(() => ({ Items: [] }));
  const emptyOffsets = [];
  await assert.rejects(
    data.all(async text => {
      emptyOffsets.push(offsetOf(text));
      if (offsetOf(text) === 0) return { Items: [{ Id: 'a' }], TotalRecordCount: 2, EioIncompleteResults: true };
      return { Items: [], TotalRecordCount: 2, EioIncompleteResults: true };
    }, '/Users/user/Items'),
    err => {
      assert.equal(err.name, 'PartialResultsError');
      assert.deepEqual(ids(err.items), ['a']);
      return true;
    },
  );
  assert.deepEqual(emptyOffsets, [0, 1]);

  const repeatedOffsets = [];
  await assert.rejects(
    data.all(async text => {
      repeatedOffsets.push(offsetOf(text));
      return { Items: [{ Id: 'same' }], TotalRecordCount: 99, EioIncompleteResults: true };
    }, '/Users/user/Items'),
    err => {
      assert.equal(err.name, 'PartialResultsError');
      assert.deepEqual(ids(err.items), ['same']);
      return true;
    },
  );
  assert.deepEqual(repeatedOffsets, [0, 1], 'a stalled partial page is not retried internally');
});

test('all() never overshoots maxResults on a single page of two items', async () => {
  const { data } = createRuntime(() => ({ Items: [] }));
  const offsets = [];
  await assert.rejects(
    data.all(async text => {
      offsets.push(offsetOf(text));
      return { Items: [{ Id: 'a' }, { Id: 'b' }], TotalRecordCount: 2 };
    }, '/Users/user/Items', {}, { maxResults: 1 }),
    err => {
      assert.equal(err.name, 'PartialResultsError');
      assert.ok(err instanceof data.PartialResultsError);
      assert.deepEqual(ids(err.items), ['a'], 'only the collected prefix is carried');
      assert.equal(err.partial, true);
      return true;
    },
  );
  assert.deepEqual(offsets, [0], 'the budget stops paging after the first page');
});

test('all() succeeds when the exact budget is also the complete listing', async () => {
  const { data } = createRuntime(() => ({ Items: [] }));
  const offsets = [];
  const results = await data.all(async text => {
    offsets.push(offsetOf(text));
    return { Items: [{ Id: 'a' }, { Id: 'b' }], TotalRecordCount: 2 };
  }, '/Users/user/Items', {}, { maxResults: 2, maxPages: 1 });
  assert.ok(Array.isArray(results));
  assert.deepEqual(ids(results), ['a', 'b']);
  assert.deepEqual(offsets, [0]);
});

test('all() rejects non-finite or non-positive maxResults/maxPages without calling the API', async () => {
  const { data } = createRuntime(() => ({ Items: [] }));
  let calls = 0;
  const api = async () => { calls++; return { Items: [], TotalRecordCount: 0 }; };
  const bad = [0, -1, 1.5, Infinity, NaN, '2', null];
  for (const value of bad) {
    await assert.rejects(data.all(api, '/Users/user/Items', {}, { maxResults: value }), err => {
      assert.notEqual(err.name, 'PartialResultsError', 'invalid options must not look like partial data');
      assert.match(err.message, /maxResults/);
      return true;
    });
    await assert.rejects(data.all(api, '/Users/user/Items', {}, { maxPages: value }), err => {
      assert.notEqual(err.name, 'PartialResultsError');
      assert.match(err.message, /maxPages/);
      return true;
    });
  }
  assert.equal(calls, 0, 'invalid options are rejected before any fetch');
});

test('all() keeps the partial prefix when a later page is anomalous without the marker', async () => {
  const { data } = createRuntime(() => ({ Items: [] }));
  const emptyOffsets = [];
  await assert.rejects(
    data.all(async text => {
      emptyOffsets.push(offsetOf(text));
      if (offsetOf(text) === 0) return { Items: [{ Id: 'a' }], TotalRecordCount: 3, EioIncompleteResults: true };
      return { Items: [], TotalRecordCount: 3 };
    }, '/Users/user/Items'),
    err => {
      assert.equal(err.name, 'PartialResultsError');
      assert.deepEqual(ids(err.items), ['a'], 'the earlier partial prefix is not dropped');
      assert.equal(err.partial, true);
      return true;
    },
  );
  assert.deepEqual(emptyOffsets, [0, 1]);

  const repeatedOffsets = [];
  await assert.rejects(
    data.all(async text => {
      repeatedOffsets.push(offsetOf(text));
      if (offsetOf(text) === 0) return { Items: [{ Id: 'a' }], TotalRecordCount: 3, EioIncompleteResults: true };
      return { Items: [{ Id: 'a' }], TotalRecordCount: 3 };
    }, '/Users/user/Items'),
    err => {
      assert.equal(err.name, 'PartialResultsError');
      assert.deepEqual(ids(err.items), ['a'], 'a stalled page after a partial prefix keeps the prefix');
      return true;
    },
  );
  assert.deepEqual(repeatedOffsets, [0, 1]);
});
