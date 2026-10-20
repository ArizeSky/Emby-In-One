const { test } = require('node:test');
const assert = require('node:assert/strict');
const { harness, makeStorage, delay } = require('./web-ui-harness.cjs');

function enterHome(app) { app.page.value = 'app'; app.navPage.value = 'home'; }
// Values cross the VM realm boundary (their Array prototype differs), so copy
// them into host arrays before a deep comparison.
const plain = value => Array.from(value || []);

test('T01 home loads partial Latest and one bounded Resume request', async () => {
  const resumeCalls = [];
  const { app, calls } = harness({ reply: url => {
    if (url.pathname.endsWith('/Views')) {
      return { Items: [{ Id: 'lib1', Name: 'Movies', CollectionType: 'movies' },
        { Id: 'lib2', Name: 'TV', CollectionType: 'tvshows' }] };
    }
    if (url.pathname.endsWith('/Items/Latest')) {
      if (url.searchParams.get('ParentId') === 'lib2') throw new Error('boom');
      return [{ Id: 'm1', Name: 'M', Type: 'Movie', BackdropImageTags: ['tag'] }];
    }
    if (url.pathname.endsWith('/Items/Resume')) { resumeCalls.push(url); return { Items: [{ Id: 'r1', Name: 'R', Type: 'Movie' }] }; }
    return { Items: [] };
  } });
  enterHome(app);
  await app.loadHome();
  assert.deepEqual(plain(app.libraries.value).map(l => l.Id), ['lib1', 'lib2']);
  assert.deepEqual(plain(app.latestSections.value).map(s => s.key), ['lib1']);
  assert.ok(app.home.latestError.value.lib2);
  assert.deepEqual(plain(app.home.resumeItems.value).map(i => i.Id), ['r1']);
  assert.equal(resumeCalls.length, 1);
  assert.equal(resumeCalls[0].searchParams.get('Limit'), '12');
  assert.ok(!calls.some(u => u.pathname.includes('NextUp') || u.searchParams.get('Counts')));
  // Retry only the failed library, never the succeeded one.
  const before = calls.filter(u => u.pathname.endsWith('/Items/Latest')).length;
  await app.home.retryLatest({ Id: 'lib2', Name: 'TV', CollectionType: 'tvshows' });
  assert.equal(calls.filter(u => u.pathname.endsWith('/Items/Latest')).length, before + 1);
});

test('T02 stale home responses cannot overwrite a newer page', async () => {
  let resolveResume;
  const { app } = harness({ reply: url => {
    if (url.pathname.endsWith('/Views')) return { Items: [] };
    if (url.pathname.endsWith('/Items/Resume')) return new Promise(resolve => { resolveResume = resolve; });
    return { Items: [] };
  } });
  enterHome(app);
  const pending = app.loadHome();
  app.navPage.value = 'favorites';
  app.home.cancel();
  resolveResume({ Items: [{ Id: 'late', Name: 'Late' }] });
  await pending;
  assert.deepEqual(plain(app.home.resumeItems.value), []);
  assert.equal(app.home.resumeLoading.value, false);
  assert.equal(app.toasts.items.value.length, 0);
});

test('T03 a slow earlier search cannot overwrite the newer query', async () => {
  const deferred = {};
  const { app } = harness({ reply: url => {
    const term = url.searchParams.get('SearchTerm');
    if (term === 'A') return new Promise(resolve => { deferred.A = resolve; });
    if (term === 'B') return { Items: [{ Id: 'b', Name: 'B', Type: 'Movie' }] };
    return { Items: [] };
  } });
  app.page.value = 'app'; app.navPage.value = 'search';
  app.searchQuery.value = 'A';
  app.search.schedule();
  await delay(440);
  app.searchQuery.value = 'B';
  app.search.schedule();
  await delay(440);
  assert.deepEqual(plain(app.searchResults.value).map(i => i.Id), ['b']);
  deferred.A({ Items: [{ Id: 'a', Name: 'A', Type: 'Movie' }] });
  await delay(10);
  assert.deepEqual(plain(app.searchResults.value).map(i => i.Id), ['b']);
  assert.equal(app.searchLoading.value, false);
});

test('T04 concurrent paging triggers only issue one request', async () => {
  let resolveAppend;
  const { app, calls } = harness({ reply: url => {
    if (url.pathname.endsWith('/Genres')) return { Items: [] };
    if (url.pathname.endsWith('/Items')) {
      const offset = Number(url.searchParams.get('StartIndex'));
      if (offset === 0) return { Items: [{ Id: 'i0' }], TotalRecordCount: 3 };
      return new Promise(resolve => { resolveAppend = resolve; });
    }
    return { Items: [] };
  } });
  app.libraries.value = [{ Id: 'lib', Name: 'L', CollectionType: 'movies' }];
  await app.loadLibrary('lib');
  assert.equal(app.libraryHasMore.value, true);
  const p1 = app.loadLibraryMore();
  const p2 = app.loadLibraryMore();
  const itemCalls = calls.filter(u => u.pathname.endsWith('/Items')).length;
  resolveAppend({ Items: [{ Id: 'i1' }], TotalRecordCount: 3 });
  await Promise.all([p1, p2]);
  assert.equal(calls.filter(u => u.pathname.endsWith('/Items')).length, itemCalls);
  assert.deepEqual(plain(app.libraryItems.value).map(i => i.Id), ['i0', 'i1']);
});

test('T05 append advances the cursor by batch length and stops on a repeated page', async () => {
  const offsets = [];
  const { app } = harness({ reply: url => {
    if (url.pathname.endsWith('/Genres')) return { Items: [] };
    const offset = Number(url.searchParams.get('StartIndex'));
    offsets.push(offset);
    if (offset === 0) return { Items: [{ Id: 'i0' }, { Id: 'i1' }], TotalRecordCount: 4 };
    return { Items: [{ Id: 'i0' }, { Id: 'i1' }], TotalRecordCount: 4 }; // repeated page
  } });
  app.libraries.value = [{ Id: 'lib', Name: 'L', CollectionType: 'movies' }];
  await app.loadLibrary('lib');
  assert.deepEqual(plain(app.libraryItems.value).map(i => i.Id), ['i0', 'i1']);
  await app.loadLibraryMore();
  assert.deepEqual(offsets, [0, 2]);
  assert.equal(app.libraryRetryAppend.value, true);
  assert.equal(app.libraryHasMore.value, false);
  assert.equal(app.libraryPageError.value, '分页未推进，请重试');
});

test('T06 a failing season retries alone and keeps the other season', async () => {
  const episodeCalls = [];
  const { app } = harness({ reply: url => {
    if (url.pathname.endsWith('/Seasons')) return { Items: [{ Id: 's1', Name: 'S1' }, { Id: 's2', Name: 'S2' }] };
    if (url.pathname.endsWith('/Episodes')) {
      const season = url.searchParams.get('SeasonId');
      episodeCalls.push(season);
      if (season === 's2' && episodeCalls.filter(s => s === 's2').length === 1) throw new Error('fail');
      return { Items: [{ Id: season + '-e1', ParentIndexNumber: 1, IndexNumber: 1 }] };
    }
    if (url.pathname.includes('/Items/')) return { Id: 'series', Name: 'S', Type: 'Series', MediaSources: [] };
    return { Items: [] };
  } });
  app.page.value = 'app'; app.navPage.value = 'item';
  await app.loadItem('series');
  assert.ok(app.seasonEpisodes.value.s1);
  assert.ok(app.seasonErrors.value.s2);
  assert.deepEqual(plain(episodeCalls), ['s1', 's2']);
  await app.retrySeason('s2');
  assert.deepEqual(plain(episodeCalls), ['s1', 's2', 's2']);
  assert.ok(app.seasonEpisodes.value.s2);
  assert.equal(app.seasonErrors.value.s2, undefined);
});

test('T13 toast queue is bounded and disposing clears timers and items', async () => {
  const { app } = harness();
  const toasts = app.toasts;
  toasts.push('a'); toasts.push('b');
  const heldId = toasts.push('c');
  toasts.push('d');
  assert.equal(toasts.items.value.length, 3);
  assert.deepEqual(plain(toasts.items.value).map(i => i.message), ['b', 'c', 'd']);
  toasts.hold(heldId);
  await delay(20);
  toasts.release(heldId);
  toasts.dispose();
  assert.equal(toasts.items.value.length, 0);
});

test('T14 recent searches stay per-user and survive broken storage', async () => {
  const storage = makeStorage({ eio_token: 't', eio_user_id: 'u1' });
  const { app } = harness({ storage });
  app.recentSearches.add('hello');
  app.recentSearches.add('hello');
  app.recentSearches.add('world');
  assert.deepEqual(plain(app.recentSearches.terms.value), ['world', 'hello']);
  storage.setItem('eio_user_id', 'u2');
  app.recentSearches.read();
  assert.deepEqual(plain(app.recentSearches.terms.value), []);
  storage.getItem = () => { throw new Error('denied'); };
  app.recentSearches.read();
  assert.deepEqual(plain(app.recentSearches.terms.value), []);
  app.recentSearches.add('x');
  assert.deepEqual(plain(app.recentSearches.terms.value), ['x']);
});
