'use strict';
/* Series-first continue-watching cards. These tests drive the production VM
 * harness (same script order as web.html) and only inspect derived values or
 * request shapes; they never call a real upstream or the running service. */
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { harness, SCRIPTS } = require('./web-ui-harness.cjs');

const WEB_HTML = fs.readFileSync(path.join(__dirname, '..', 'public', 'web', 'web.html'), 'utf8');
const RESUME_FIELDS = 'PrimaryImageAspectRatio,RunTimeTicks,UserData,SeriesName,SeriesId,SeasonId,IndexNumber,ParentIndexNumber';
const FAVORITE_FIELDS = 'PrimaryImageAspectRatio,RunTimeTicks,UserData,SeriesName';

function cardBlock(needle) {
  const start = WEB_HTML.indexOf(needle);
  assert.ok(start >= 0, `missing template block: ${needle}`);
  const end = WEB_HTML.indexOf('</eio-poster-card>', start);
  assert.ok(end > start, `unterminated card block: ${needle}`);
  return WEB_HTML.slice(start, end);
}

function enterPage(app, navPage) {
  app.page.value = 'app';
  app.navPage.value = navPage;
}

test('resumeCardView links an episode to its series without mutating the row', () => {
  const { resumeCard, calls } = harness();
  const userData = { PlaybackPositionTicks: 250, Played: false };
  const episode = {
    Id: 'episode-1', Name: 'Pilot', Type: 'Episode', SeriesId: 'series-1',
    SeriesName: 'The Show', ParentIndexNumber: 1, IndexNumber: 2,
    RunTimeTicks: 1000, UserData: userData,
  };
  const before = JSON.stringify(episode);
  const beforeCalls = calls.length;

  const view = resumeCard.resumeCardView(episode);

  assert.equal(view.title, 'The Show');
  assert.equal(view.href, '#/item/series-1');
  assert.equal(view.subtitle, 'S01E02');
  assert.equal(episode.Id, 'episode-1');
  assert.equal(episode.SeriesId, 'series-1');
  assert.equal(episode.UserData, userData);
  assert.equal(JSON.stringify(episode), before);
  assert.equal(calls.length, beforeCalls, 'the helper must not perform a request');
});

test('resumeCardView keeps the original card behaviour for movies and unknown types', () => {
  const { resumeCard } = harness();
  const movie = {
    Id: 'movie-1', Name: 'A Movie', Type: 'Movie', SeriesId: 'looks-like-an-id',
    SeriesName: '', RunTimeTicks: 1000, UserData: { PlaybackPositionTicks: 0 },
  };
  const movieView = resumeCard.resumeCardView(movie);
  assert.equal(movieView.title, 'A Movie');
  assert.equal(movieView.href, '#/item/movie-1');
  assert.equal(movieView.subtitle, '');

  const noSeries = {
    Id: 'episode-2', Name: 'Orphan Episode', Type: 'Episode',
    SeriesName: 'Legacy Subtitle', ParentIndexNumber: 1, IndexNumber: 2,
  };
  const noSeriesView = resumeCard.resumeCardView(noSeries);
  assert.equal(noSeriesView.title, 'Orphan Episode');
  assert.equal(noSeriesView.href, '#/item/episode-2');
  assert.equal(noSeriesView.subtitle, 'Legacy Subtitle');

  const seriesObject = {
    Id: 'series-2', Name: 'A Series', Type: 'Series', SeriesId: 'series-2',
    SeriesName: 'A Series',
  };
  const seriesView = resumeCard.resumeCardView(seriesObject);
  assert.equal(seriesView.title, 'A Series');
  assert.equal(seriesView.href, '#/item/series-2');
  assert.equal(seriesView.subtitle, 'A Series');
});

test('resumeCardView uses SeriesName then Name, and formats indices safely', () => {
  const { resumeCard } = harness();
  const base = { Type: 'Episode', SeriesId: 'series-3', Name: 'Episode Name' };

  assert.equal(resumeCard.resumeCardView({ ...base, SeriesName: 'Series Name' }).title, 'Series Name');
  assert.equal(resumeCard.resumeCardView({ ...base }).title, 'Episode Name');

  const special = resumeCard.resumeCardView({
    ...base, SeriesName: 'Specials', ParentIndexNumber: 0, IndexNumber: 0,
  });
  assert.equal(special.subtitle, 'S00E00');

  const large = resumeCard.resumeCardView({
    ...base, ParentIndexNumber: 12, IndexNumber: 105,
  });
  assert.equal(large.subtitle, 'S12E105');

  for (const item of [
    { ...base, ParentIndexNumber: undefined, IndexNumber: 2 },
    { ...base, ParentIndexNumber: 1, IndexNumber: Number.NaN },
    { ...base, ParentIndexNumber: Number.NaN, IndexNumber: Number.NaN },
    { ...base, ParentIndexNumber: -1, IndexNumber: 2 },
  ]) {
    const view = resumeCard.resumeCardView(item);
    assert.equal(view.subtitle, 'Episode Name');
    assert.ok(!/NaN|undefined|S-|E-/.test(view.subtitle));
  }

  const missingName = resumeCard.resumeCardView({
    Type: 'Episode', SeriesId: 'series-3', SeriesName: 'Only Series',
  });
  assert.equal(missingName.subtitle, 'Only Series');
});

test('resumeCardView keeps the current hash and never guesses a series route', () => {
  const { context, resumeCard } = harness();
  context.location.hash = '#/resume';
  const view = resumeCard.resumeCardView({
    Id: 'episode-4', Name: 'Episode', Type: 'Episode', SeriesId: 'series-4',
    ParentIndexNumber: 2, IndexNumber: 7,
  });
  assert.equal(view.href, '#/item/series-4');
  assert.equal(context.location.hash, '#/resume');
});

test('poster card title keeps title/alt/aria consistent and still emits the original item', () => {
  const { components } = harness();
  const card = components.EioPosterCard;
  assert.ok(card, 'EioPosterCard must be registered');
  assert.ok(card.props.title);
  assert.equal(card.props.title.default, '');

  const item = {
    Name: 'Episode Name', RunTimeTicks: 1000,
    UserData: { PlaybackPositionTicks: 250, Played: false },
  };
  assert.equal(card.computed.displayTitle.call({ title: 'Series Name', item }), 'Series Name');
  assert.equal(card.computed.displayTitle.call({ title: '', item }), 'Episode Name');

  /* Stand-in for a Vue props binding: real Vue exposes `displayTitle` as a
   * computed getter on the instance, so `initial()` sees it. Bind the real
   * computed getter instead of hand-writing the value. */
  const withComputedProps = props => Object.defineProperty({ ...props }, 'displayTitle', {
    configurable: true,
    enumerable: true,
    get() { return card.computed.displayTitle.call(this); },
  });

  assert.equal(card.computed.initial.call(withComputedProps({ title: 'Series Name', item })), 'S');
  assert.equal(card.computed.initial.call(withComputedProps({ title: '', item })), 'E');
  assert.equal(card.computed.ratio.call({ item }), 0.25);
  assert.equal(card.computed.played.call({ item }), false);

  assert.match(card.template, /:aria-label="displayTitle"/);
  assert.match(card.template, /:alt="displayTitle"/);
  assert.match(card.template, /:title="displayTitle"/);
  assert.match(card.template, /\{\{ displayTitle \}\}/);
  assert.match(card.template, /@click="\$emit\('play', item\)"/);
});

test('home resume cards use the helper while preserving request shape and rows', async () => {
  const episodes = [
    {
      Id: 'home-ep-1', Name: 'One', Type: 'Episode', SeriesId: 'series-a',
      SeriesName: 'Show A', ParentIndexNumber: 1, IndexNumber: 1,
      RunTimeTicks: 1000, UserData: { PlaybackPositionTicks: 100 },
    },
    {
      Id: 'home-ep-2', Name: 'Two', Type: 'Episode', SeriesId: 'series-a',
      SeriesName: 'Show A', ParentIndexNumber: 1, IndexNumber: 2,
      RunTimeTicks: 1000, UserData: { PlaybackPositionTicks: 200 },
    },
  ];
  const { app, calls } = harness({ reply: url => {
    if (url.pathname.endsWith('/Views')) return { Items: [] };
    if (url.pathname.endsWith('/Items/Resume')) return { Items: episodes };
    return { Items: [] };
  } });
  enterPage(app, 'home');
  await app.loadHome();

  assert.equal(app.home.resumeItems.value.length, 2);
  assert.equal(app.home.resumeItems.value[0], episodes[0]);
  const request = calls.find(url => url.pathname.endsWith('/Items/Resume'));
  assert.ok(request);
  assert.equal(request.searchParams.get('Limit'), '12');
  assert.equal(request.searchParams.get('Fields'), RESUME_FIELDS);
  assert.equal(request.searchParams.get('SortBy'), null);

  const views = Array.from(app.home.resumeItems.value, item => app.resumeCard(item));
  assert.equal(views.length, 2, 'episodes of one series must not be folded');
  assert.deepEqual(views.map(view => view.href), ['#/item/series-a', '#/item/series-a']);
  assert.deepEqual(views.map(view => view.subtitle), ['S01E01', 'S01E02']);

  const home = cardBlock('v-for="it in home.resumeItems"');
  assert.match(home, /:detail-href="resumeCard\(it\)\.href"/);
  assert.match(home, /:title="resumeCard\(it\)\.title"/);
  assert.match(home, /:sub="resumeCard\(it\)\.subtitle"/);
  assert.match(home, /:show-progress="true"/);
  assert.match(home, /@play="playItem\(it\)"/);
});

test('the full resume route keeps every episode, total and request semantics', async () => {
  const episodes = [
    {
      Id: 'route-ep-1', Name: 'One', Type: 'Episode', SeriesId: 'series-a',
      SeriesName: 'Show A', ParentIndexNumber: 1, IndexNumber: 1,
      RunTimeTicks: 1000, UserData: { PlaybackPositionTicks: 100 },
    },
    {
      Id: 'route-ep-2', Name: 'Two', Type: 'Episode', SeriesId: 'series-a',
      SeriesName: 'Show A', ParentIndexNumber: 1, IndexNumber: 2,
      RunTimeTicks: 1000, UserData: { PlaybackPositionTicks: 200 },
    },
    {
      Id: 'route-ep-3', Name: 'Three', Type: 'Episode', SeriesId: 'series-b',
      SeriesName: 'Show B', ParentIndexNumber: 2, IndexNumber: 1,
      RunTimeTicks: 1000, UserData: { PlaybackPositionTicks: 300 },
    },
  ];
  const userData = episodes[1].UserData;
  const { app, calls } = harness({ reply: url => {
    if (url.pathname.endsWith('/Users/user/Items/Resume')) {
      return { Items: episodes, TotalRecordCount: episodes.length };
    }
    return { Items: [] };
  } });
  enterPage(app, 'resume');
  await app.loadCollection('resume');

  const resumeCalls = calls.filter(url => url.pathname.endsWith('/Users/user/Items/Resume'));
  assert.equal(resumeCalls.length, 1, 'a complete total must stop pagination');
  assert.equal(resumeCalls[0].searchParams.get('Limit'), '200');
  assert.equal(resumeCalls[0].searchParams.get('StartIndex'), '0');
  assert.equal(resumeCalls[0].searchParams.get('Recursive'), 'true');
  assert.equal(resumeCalls[0].searchParams.get('Fields'), RESUME_FIELDS);
  assert.equal(resumeCalls[0].searchParams.get('SortBy'), null);

  assert.equal(app.collectionItems.value.length, 3);
  assert.equal(app.collectionItems.value[1], episodes[1]);
  assert.equal(app.collectionItems.value[1].UserData, userData);
  assert.equal(app.collectionNotice.value, '');

  const views = Array.from(app.collectionItems.value, item => app.resumeCard(item));
  assert.equal(views.length, 3, 'the resume list must not be folded by series');
  assert.deepEqual(views.map(view => view.href), ['#/item/series-a', '#/item/series-a', '#/item/series-b']);

  const collection = cardBlock('v-for="it in collectionItems"');
  assert.match(collection, /navPage==='resume' \? resumeCard\(it\)\.href : '#\/item\/' \+ it\.Id/);
  assert.match(collection, /navPage==='resume' \? resumeCard\(it\)\.title : ''/);
  assert.match(collection, /navPage==='resume' \? resumeCard\(it\)\.subtitle : \(it\.SeriesName \|\| ''\)/);
  assert.match(collection, /:show-progress="navPage==='resume'"/);
  assert.match(collection, /@play="playItem\(it\)"/);
});

test('an incomplete resume route keeps its partial rows and lower-bound notice', async () => {
  const episodes = [
    { Id: 'partial-1', Name: 'One', Type: 'Episode', SeriesId: 'series-a', SeriesName: 'Show A', ParentIndexNumber: 1, IndexNumber: 1 },
    { Id: 'partial-2', Name: 'Two', Type: 'Episode', SeriesId: 'series-a', SeriesName: 'Show A', ParentIndexNumber: 1, IndexNumber: 2 },
  ];
  const { app, calls } = harness({ reply: url => {
    if (url.pathname.endsWith('/Users/user/Items/Resume')) {
      return { Items: episodes, TotalRecordCount: 5, EioIncompleteResults: true };
    }
    return { Items: [] };
  } });
  enterPage(app, 'resume');
  await app.loadCollection('resume');

  assert.equal(app.collectionItems.value.length, 2);
  assert.equal(app.collectionNotice.value, '列表可能不完整，可稍后重试补全');
  assert.ok(calls.filter(url => url.pathname.endsWith('/Users/user/Items/Resume')).length >= 1);
  const views = Array.from(app.collectionItems.value, item => app.resumeCard(item));
  assert.deepEqual(views.map(view => view.href), ['#/item/series-a', '#/item/series-a']);
});

test('favorites keep the original fields and never use the resume helper', async () => {
  const episode = {
    Id: 'favorite-episode', Name: 'Favorite', Type: 'Episode', SeriesId: 'series-f',
    SeriesName: 'Favorites Show', ParentIndexNumber: 1, IndexNumber: 1,
  };
  const { app, calls } = harness({ reply: url => {
    if (url.pathname.endsWith('/Users/user/Items')) {
      return { Items: [episode], TotalRecordCount: 1 };
    }
    return { Items: [] };
  } });
  enterPage(app, 'favorites');
  await app.loadCollection('favorites');

  const request = calls.find(url => url.pathname.endsWith('/Users/user/Items'));
  assert.ok(request);
  assert.equal(request.searchParams.get('Fields'), FAVORITE_FIELDS);
  assert.equal(request.searchParams.get('Filters'), 'IsFavorite');
  assert.equal(request.searchParams.get('IncludeItemTypes'), 'Movie,Series,Episode');
  assert.equal(app.collectionItems.value[0], episode);
});

test('web.html and the harness register resume-card before components and app', () => {
  const resumeAt = WEB_HTML.indexOf('/web/web/resume-card.js');
  const componentsAt = WEB_HTML.indexOf('/web/web/ui-components.js');
  const appAt = WEB_HTML.indexOf('/web/web/app.js');
  assert.ok(resumeAt >= 0 && resumeAt < componentsAt);
  assert.ok(componentsAt < appAt);

  assert.equal(SCRIPTS.indexOf('resume-card.js') + 1, SCRIPTS.indexOf('ui-components.js'));
  assert.ok(SCRIPTS.indexOf('resume-card.js') < SCRIPTS.indexOf('app.js'));

  const searchAt = WEB_HTML.indexOf('<!-- ===== SEARCH ===== -->');
  assert.ok(searchAt > 0);
  const searchBlock = WEB_HTML.slice(searchAt);
  assert.doesNotMatch(searchBlock, /resumeCard\(/);
  assert.match(searchBlock, /:href="'#\/item\/' \+ it\.Id"/);

  const libraryAt = WEB_HTML.indexOf('<!-- ===== LIBRARY ===== -->');
  const itemAt = WEB_HTML.indexOf('<!-- ===== ITEM DETAIL ===== -->');
  assert.ok(libraryAt > 0 && itemAt > libraryAt);
  const libraryBlock = WEB_HTML.slice(libraryAt, itemAt);
  assert.doesNotMatch(libraryBlock, /resumeCard\(/);
  assert.match(libraryBlock, /:detail-href="'#\/item\/' \+ it\.Id"/);

  const collectionBlock = cardBlock('v-for="it in collectionItems"');
  assert.match(collectionBlock, /navPage==='resume' \? resumeCard\(it\)\.href : '#\/item\/' \+ it\.Id/);
  assert.match(collectionBlock, /navPage==='resume' \? resumeCard\(it\)\.subtitle : \(it\.SeriesName \|\| ''\)/);
});
