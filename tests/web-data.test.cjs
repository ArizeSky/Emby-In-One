const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');

function harness(reply) {
  let app;
  const context = { URL, AbortController, setTimeout, clearTimeout, console,
    location: { origin: 'http://localhost', hash: '#/home', reload() {} },
    localStorage: { getItem: key => key === 'eio_token' ? 'token' : 'user', removeItem() {} },
    lucide: { createIcons() {} },
    fetch: async (url, options) => ({ ok: true, status: 200, json: () => reply(new URL(url, 'http://localhost'), options) }),
    Vue: { ref: value => ({ value }), computed: fn => ({ get value() { return fn(); } }),
      watch() {}, onMounted() {}, onBeforeUnmount() {}, nextTick: async fn => fn?.(),
      createApp: options => ({ mount() { app = options.setup(); } }) },
  };
  context.window = context;
  vm.createContext(context);
  for (const file of ['data.js', 'app.js']) vm.runInContext(fs.readFileSync(path.join(__dirname, '../public/web', file), 'utf8'), context);
  return { app, context, data: context.EIOData };
}

test('library navigation keeps ParentId and appends later pages within that library', async () => {
  const calls = [];
  const { app, context } = harness(url => {
    calls.push(url);
    if (url.pathname === '/Genres') return { Items: [] };
    const offset = Number(url.searchParams.get('StartIndex'));
    return { Items: [{ Id: 'item-' + offset }], TotalRecordCount: 2 };
  });
  app.libraries.value = [{ Id: 'korea', Name: '韩国 (Server 1)', CollectionType: 'tvshows' }];
  app.openLibrary('korea');
  assert.equal(context.location.hash, '#/library/korea');
  await app.loadLibrary('korea');
  await app.loadLibraryMore();
  assert.deepEqual(Array.from(app.libraryItems.value, x => x.Id), ['item-0', 'item-1']);
  const requests = calls.filter(url => url.pathname.endsWith('/Items'));
  assert.equal(requests.length, 2);
  assert.ok(requests.every(url => url.searchParams.get('ParentId') === 'korea' && url.searchParams.get('IncludeItemTypes') === 'Series'));
  assert.equal(requests[1].searchParams.get('StartIndex'), '1');
  assert.equal(app.libraryHasMore.value, false);
});

test('complete show paging follows upstream totals even when pages are shorter than requested', async () => {
  const { data } = harness(() => ({}));
  const offsets = [];
  const episodes = await data.all(async text => {
    const offset = Number(new URL(text, 'http://localhost').searchParams.get('StartIndex'));
    offsets.push(offset);
    return { Items: Array.from({ length: Math.min(2, 5 - offset) }, (_, i) => ({ Id: offset + i })), TotalRecordCount: 5 };
  }, '/Shows/a/Episodes');
  assert.equal(episodes.length, 5);
  assert.deepEqual(offsets, [0, 2, 4]);
  await assert.rejects(data.all(async () => ({ Items: [{ Id: 1 }], TotalRecordCount: 99 }), '/Shows/a/Episodes'), /分页未推进/);
});

test('favorites and continue watching have independent full-list queries', async () => {
  const calls = [];
  const { app } = harness(url => { calls.push(url); return { Items: [{ Id: 'saved' }], TotalRecordCount: 1 }; });
  await app.loadCollection('favorites');
  assert.equal(calls[0].searchParams.get('Filters'), 'IsFavorite');
  assert.equal(calls[0].searchParams.get('IncludeItemTypes'), 'Movie,Series,Episode');
  await app.loadCollection('resume');
  assert.equal(calls[1].pathname, '/Users/user/Items/Resume');
  assert.ok(app.pages.some(p => p.id === 'favorites'));
  assert.ok(app.pages.some(p => p.id === 'resume'));
});

test('detail track choices reset to the selected version defaults', async () => {
  const { app } = harness(() => ({}));
  app.itemDetail.value = { MediaSources: [
    { Id: 'A', DefaultAudioStreamIndex: 1, DefaultSubtitleStreamIndex: 2 },
    { Id: 'B', DefaultAudioStreamIndex: 4, DefaultSubtitleStreamIndex: -1 },
  ] };
  app.chooseDetailSource('A');
  assert.equal(app.detailAudioIndex.value, 1);
  assert.equal(app.detailSubtitleIndex.value, 2);
  app.chooseDetailSource('B');
  assert.equal(app.detailAudioIndex.value, 4);
  assert.equal(app.detailSubtitleIndex.value, -1);
});

test('episode ordering keeps season boundaries before episode numbers', () => {
  const { data } = harness(() => ({}));
  const episodes = [ { Id: 's2e1', ParentIndexNumber: 2, IndexNumber: 1 },
    { Id: 's1e10', ParentIndexNumber: 1, IndexNumber: 10 }, { Id: 's1e2', ParentIndexNumber: 1, IndexNumber: 2 } ];
  assert.deepEqual(episodes.sort(data.episodeOrder).map(x => x.Id), ['s1e2', 's1e10', 's2e1']);
});
