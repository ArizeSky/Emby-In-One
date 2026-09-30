/* Emby-In-One Web Player — single-page application
 *
 * Vue 3 global build, no build step. Loads the shared vendor/ scripts that the
 * admin panel already ships plus libmedia and JASSUB from vendor/web/.
 * Player logic is in player-libmedia.js (window.EIOPlayer).
 */
(function () {
  const { createApp, ref, computed, onMounted, onBeforeUnmount, nextTick, watch } = Vue;

  const API = ''; // same origin
  const { query, all: allItems, episodeOrder, defaultAudio, defaultSubtitle } = EIOData;

  /* ================= helpers ================= */

  async function api(path, opts = {}) {
    const token = localStorage.getItem('eio_token');
    // Authentication uses X-Emby-Token only. The web client deliberately sends
    // no client identity (no Authorization compound header, no X-Emby-Client /
    // X-Emby-Device-* headers): on passthrough upstreams those headers would
    // win the identity resolution over the captured real-client identity and
    // the upstream would register a fabricated "web" device record. With no
    // identity headers the proxy falls through to the captured real client's
    // UA, which is exactly what passthrough mode promises.
    const headers = { 'Content-Type': 'application/json', ...(opts.headers || {}) };
    if (token) headers['X-Emby-Token'] = token;
    const res = await fetch(`${API}${path}`, { ...opts, headers });
    if (!res.ok) {
      const body = await res.text().catch(() => '');
      let detail = body || res.statusText;
      try { const parsed = JSON.parse(body); if (parsed && parsed.message) detail = parsed.message; } catch (e) {}
      throw new Error(`${res.status}: ${detail}`);
    }
    if (res.status === 204) return null;
    return res.json();
  }

  // Raw URLs (images, streams, subtitle files) authenticate with api_key in
  // the query string — same token, and equally free of client identity.
  function apiRaw(path) {
    const token = localStorage.getItem('eio_token');
    const url = new URL(path, location.origin);
    url.searchParams.set('api_key', token || '');
    return url.pathname + url.search;
  }

  function fmtTicks(ticks) {
    if (!ticks) return '0:00';
    const s = Math.floor(ticks / 10000000);
    const m = Math.floor(s / 60);
    const h = Math.floor(m / 60);
    return h > 0
      ? `${h}:${String(m % 60).padStart(2, '0')}:${String(s % 60).padStart(2, '0')}`
      : `${m}:${String(s % 60).padStart(2, '0')}`;
  }

  /* ================= main app ================= */

  createApp({
    setup() {
      /* ---- auth ---- */
      const page = ref('login');
      const navPage = ref('home');
      const loginForm = ref({ user: '', pass: '' });
      const loginError = ref('');
      const loginLoading = ref(false);
      const currentUser = ref(null);
      const passWrap = ref(null);

      /* ---- router ---- */
      const routeMap = { '': 'login', home: 'home', resume: 'resume', favorites: 'favorites', library: 'library', item: 'item', search: 'search', play: 'player' };
      const currentRoute = ref('');
      const routeParam = ref('');

      function readHash() {
        const h = window.location.hash.replace(/^#\/?/, '');
        const [r, ...rest] = h.split('/');
        return { r, param: rest.join('/') };
      }

      function syncFromHash() {
        const { r, param } = readHash();
        currentRoute.value = r; routeParam.value = param;
        if (r === '' || r === 'login') { stopPlayer(); page.value = 'login'; return; }
        if (!localStorage.getItem('eio_token')) { stopPlayer(); page.value = 'login'; return; }
        const target = routeMap[r];
        if (!target) return;
        if (target !== 'library') { libraryRequest?.abort(); libraryPageRequest?.abort(); }
        if (target !== 'item') itemRequest?.abort();
        if (target !== 'resume' && target !== 'favorites') collectionRequest?.abort();
        // Leaving the player page must stop playback: the video element keeps
        // decoding (and the audio graph keeps running) otherwise.
        if (navPage.value === 'player' && target !== 'player') stopPlayer();
        page.value = 'app';
        navPage.value = target;
        refreshIcons();
        if (r === 'home') loadHome();
        else if (r === 'resume' || r === 'favorites') loadCollection(r);
        else if (r === 'library') loadLibrary(param);
        else if (r === 'item') loadItem(param);
        else if (r === 'play') startPlayerFromRoute(param);
      }

      /* ---- navigation ---- */
      const pages = [
        { id: 'home', label: '首页', icon: 'home' },
        { id: 'resume', label: '继续观看', icon: 'history' },
        { id: 'favorites', label: '收藏', icon: 'heart' },
        { id: 'search', label: '搜索', icon: 'search' },
      ];
      const mobileMenu = ref(false);
      function navigateTo(id) { window.location.hash = '#/' + id; }
      function openItem(id) { window.location.hash = '#/item/' + id; }
      function openLibrary(id) { window.location.hash = '#/library/' + id; }

      // libraryIcon maps a library's CollectionType to a lucide icon name. The
      // home page calls it inline, so it must exist before the first render.
      function libraryIcon(lib) {
        const t = (lib?.CollectionType || '').toLowerCase();
        const map = {
          movies: 'film', tvshows: 'tv', music: 'music', musicvideos: 'clapperboard',
          books: 'book-open', audiobooks: 'headphones', comics: 'book',
          homevideos: 'video', photos: 'image', playlists: 'list-music',
          boxsets: 'folder', livetv: 'radio', channels: 'radio-tower',
        };
        return map[t] || 'library';
      }

      /* ---- auth ---- */
      async function doLogin() {
        if (!loginForm.value.user || !loginForm.value.pass) return;
        loginLoading.value = true; loginError.value = '';
        try {
          // The marker tells the server this login comes from the web player, so
          // admin credentials are refused with 403 here instead of issuing an
          // admin token the client would then have to throw away.
          const res = await api('/Users/AuthenticateByName', {
            method: 'POST',
            headers: { 'X-EIO-Web-Client': '1' },
            body: JSON.stringify({ Username: loginForm.value.user, Pw: loginForm.value.pass }),
          });
          if (res.User?.Policy?.IsAdministrator) {
            loginError.value = '管理员账号请使用 /admin 管理面板登录。';
            loginLoading.value = false; return;
          }
          localStorage.setItem('eio_token', res.AccessToken);
          localStorage.setItem('eio_user_id', res.User.Id);
          currentUser.value = res.User;
          window.location.hash = '#/home';
        } catch (e) { loginError.value = '登录失败：' + e.message; }
        loginLoading.value = false;
      }

      function doLogout() {
        stopPlayer();
        localStorage.removeItem('eio_token'); localStorage.removeItem('eio_user_id');
        window.location.hash = '#/';
        location.reload();
      }

      function focusPass() { passWrap.value?.querySelector('input')?.focus(); }

      /* ---- home ---- */
      const libraries = ref([]);
      const latestSections = ref([]);
      const collectionItems = ref([]);
      const collectionLoading = ref(false);
      const collectionError = ref('');
      let collectionRequest = null;

      async function loadCollection(kind) {
        collectionRequest?.abort();
        const request = collectionRequest = new AbortController();
        collectionLoading.value = true; collectionError.value = ''; collectionItems.value = [];
        const path = `/Users/${localStorage.getItem('eio_user_id')}/Items`;
        try {
          const params = { Fields: 'PrimaryImageAspectRatio,RunTimeTicks,UserData,SeriesName', Recursive: true };
          if (kind === 'favorites') { params.Filters = 'IsFavorite'; params.IncludeItemTypes = 'Movie,Series,Episode'; }
          const items = await allItems(api, kind === 'resume' ? path + '/Resume' : path, params, { signal: request.signal });
          if (collectionRequest === request && !request.signal.aborted) collectionItems.value = items;
        } catch (e) { if (e.name !== 'AbortError') collectionError.value = '加载失败：' + e.message; }
        finally { if (collectionRequest === request) collectionLoading.value = false; }
        refreshIcons();
      }

      async function loadHome() {
        const uid = localStorage.getItem('eio_user_id');
        try { libraries.value = (await api(`/Users/${uid}/Views`)).Items || []; }
        catch (e) { console.error('loadHome views', e); }
        // Latest sections load in parallel and skip non-content libraries
        // (playlists return nothing), so the home fills in fast and one slow
        // upstream never blocks the rest.
        const targets = libraries.value.filter(l => l.CollectionType !== 'playlists').slice(0, 3);
        const results = await Promise.allSettled(targets.map(lib =>
          api(`/Users/${uid}/Items/Latest?Limit=12&ParentId=${lib.Id}&Fields=PrimaryImageAspectRatio`)
            .then(latest => ({ lib, latest }))));
        const secs = [];
        for (const res of results) {
          if (res.status !== 'fulfilled') continue;
          // /Items/Latest answers with a bare array (Emby contract), while
          // other item endpoints answer with { Items: [...] }; accept both.
          const items = Array.isArray(res.value.latest) ? res.value.latest : (res.value.latest.Items || []);
          if (items.length) secs.push({ key: res.value.lib.Id, title: res.value.lib.Name, items });
        }
        latestSections.value = secs;
        refreshIcons();
      }

      /* ---- library ---- */
      const libraryName = ref('');
      const libraryItems = ref([]);
      const libraryLoading = ref(false);
      const libFilter = ref('');
      const libSort = ref('DateCreated,Descending');
      const libGenres = ref([]);
      const libLimit = ref(50);
      let libOffset = 0;

      const libraryId = ref('');
      const libraryType = ref('');
      const libraryHasMore = ref(false);
      const libraryError = ref('');
      let libraryRequest = null;
      let libraryPageRequest = null;

      async function loadLibrary(id) {
        libraryRequest?.abort(); libraryPageRequest?.abort();
        const request = libraryRequest = new AbortController();
        libraryId.value = id; libraryName.value = ''; libraryType.value = '';
        navPage.value = 'library';
        libraryLoading.value = true;
        libOffset = 0; libraryItems.value = []; libFilter.value = ''; libraryError.value = '';
        try {
          const lib = libraries.value.find(l => l.Id === id)
            || await api(`/Users/${localStorage.getItem('eio_user_id')}/Items/${id}`, { signal: request.signal });
          if (libraryRequest !== request) return;
          libraryName.value = lib.Name;
          libraryType.value = lib.CollectionType || '';
        } catch (e) { if (e.name === 'AbortError') return; console.error('loadLibrary', e); }
        // Genres are decorative chips: the proxy implements /Genres without the
        // /Users prefix, and a failure here must never block the item list (the
        // old /Users/{id}/Genres path 502'd and aborted the whole page).
        try {
          const genres = await api(`/Genres?ParentId=${id}&UserId=${localStorage.getItem('eio_user_id')}&Limit=50`, { signal: request.signal });
          if (libraryRequest !== request) return;
          libGenres.value = (genres.Items || []).map(g => g.Name);
        } catch (e) { libGenres.value = []; console.error('genres', e); }
        if (libraryRequest !== request || navPage.value !== 'library') return;
        await loadLibraryItems();
        if (libraryRequest === request) libraryLoading.value = false;
        refreshIcons();
      }

      async function loadLibraryItems(append = false) {
        if (!libraryId.value || navPage.value !== 'library' || libraryRequest?.signal.aborted) return;
        libraryPageRequest?.abort();
        const request = libraryPageRequest = new AbortController();
        const parentId = libraryId.value;
        const [sortField, sortDir] = libSort.value.split(',');
        libraryError.value = '';
        try {
          const params = { ParentId: parentId, Limit: libLimit.value, StartIndex: libOffset,
            SortBy: sortField, SortOrder: sortDir, Genres: libFilter.value,
            Fields: 'PrimaryImageAspectRatio,ProductionYear,ProviderIds', Recursive: true,
            IncludeItemTypes: libraryType.value === 'tvshows' ? 'Series' : libraryType.value === 'movies' ? 'Movie' : '' };
          const result = await api(query(`/Users/${localStorage.getItem('eio_user_id')}/Items`, params), { signal: request.signal });
          if (libraryPageRequest !== request || libraryId.value !== parentId) return;
          const batch = result.Items || [];
          libraryItems.value = append ? [...libraryItems.value, ...batch.filter(x => !libraryItems.value.some(y => y.Id === x.Id))] : batch;
          libraryHasMore.value = libOffset + batch.length < (result.TotalRecordCount ?? Infinity) && batch.length > 0;
        } catch (e) { if (e.name !== 'AbortError') libraryError.value = '加载失败：' + e.message; }
        refreshIcons();
      }

      async function loadLibraryMore() { libOffset = libraryItems.value.length; await loadLibraryItems(true); }

      watch([libFilter, libSort], () => { if (!libraryType.value) return; libOffset = 0; loadLibraryItems(); });

      /* ---- item detail ---- */
      const itemDetail = ref(null);
      const itemDetailLoading = ref(false);
      const seriesSeasons = ref([]);
      const seasonEpisodes = ref({});
      const currentMediaSourceId = ref(null);
      const detailAudioIndex = ref(-1);
      const detailSubtitleIndex = ref(-1);
      const itemError = ref('');
      const seasonErrors = ref({});
      let itemRequest = null;

      async function loadItem(id) {
        itemRequest?.abort();
        const request = itemRequest = new AbortController();
        itemError.value = ''; seasonErrors.value = {};
        navPage.value = 'item'; itemDetailLoading.value = true;
        itemDetail.value = null; seriesSeasons.value = []; seasonEpisodes.value = {};
        try {
          const detail = await api(`/Users/${localStorage.getItem('eio_user_id')}/Items/${id}?Fields=MediaSources,ProviderIds,Overview,Genres,ProductionYear,RunTimeTicks,OfficialRating,CommunityRating,BackdropImageTags,SeriesId,SeasonId,UserData`, { signal: request.signal });
          if (itemRequest !== request) return;
          itemDetail.value = detail;
          chooseDetailSource(detail.MediaSources?.[0]?.Id || null);
          if (detail.Type === 'Series') {
            // The proxy implements the standard /Shows/{id}/Seasons path (no
            // /Users prefix); the old prefixed path fell through to the
            // fallback proxy and 502'd, so no season or episode ever loaded.
            const uid = localStorage.getItem('eio_user_id');
            const seasons = await allItems(api, `/Shows/${id}/Seasons`, { UserId: uid }, { signal: request.signal });
            if (itemRequest !== request || request.signal.aborted) return;
            seriesSeasons.value = seasons;
            // Keep upstream work bounded. A failed season does not hide the rest.
            for (const season of seriesSeasons.value) {
              if (itemRequest !== request) return;
              try {
                const eps = await allItems(api, `/Shows/${id}/Episodes`, { UserId: uid, SeasonId: season.Id,
                  Fields: 'PrimaryImageAspectRatio,RunTimeTicks,SeriesId,SeasonId,UserData' }, { signal: request.signal });
                if (itemRequest === request) seasonEpisodes.value[season.Id] = eps.sort(episodeOrder);
              } catch (e) {
                if (e.name === 'AbortError') return;
                seasonErrors.value[season.Id] = '本季加载失败，请重试';
              }
            }
          }
        } catch (e) { if (e.name !== 'AbortError') itemError.value = '加载失败：' + e.message; }
        if (itemRequest === request) itemDetailLoading.value = false;
        refreshIcons();
      }

      function chooseDetailSource(id) {
        currentMediaSourceId.value = id;
        const source = itemDetail.value?.MediaSources?.find(s => s.Id === id);
        detailAudioIndex.value = defaultAudio(source);
        detailSubtitleIndex.value = defaultSubtitle(source);
      }

      function retryItem() { return loadItem(routeParam.value); }

      function playDetail(sourceId = currentMediaSourceId.value) {
        if (sourceId !== currentMediaSourceId.value) chooseDetailSource(sourceId);
        return playItem(itemDetail.value, sourceId, { audioIndex: detailAudioIndex.value, subtitleIndex: detailSubtitleIndex.value });
      }

      const itemDetailIs4K = computed(() => { const s = currentVideoStream.value; return s && s.Width >= 3800; });
      const itemDetailIsHDR = computed(() => { const s = currentVideoStream.value; return s && (s.VideoRange === 'HDR10' || s.VideoRange === 'HLG'); });
      const itemDetailIsDV = computed(() => { const s = currentVideoStream.value; return s && s.VideoRange === 'DOVI'; });
      const itemDetailOfficialRating = computed(() => itemDetail.value?.OfficialRating);
      const runtimeMin = computed(() => itemDetail.value?.RunTimeTicks ? Math.round(itemDetail.value.RunTimeTicks / 600000000) : null);
      const runtimeMin_ = (it) => it.RunTimeTicks ? Math.round(it.RunTimeTicks / 600000000) : null;

      const currentVideoStream = computed(() => {
        const src = currentMediaSourceId.value ? itemDetail.value?.MediaSources?.find(m => m.Id === currentMediaSourceId.value) : itemDetail.value?.MediaSources?.[0];
        return src?.MediaStreams?.find(s => s.Type === 'Video');
      });
      const currentMediaStreams = computed(() => {
        const src = currentMediaSourceId.value ? itemDetail.value?.MediaSources?.find(m => m.Id === currentMediaSourceId.value) : itemDetail.value?.MediaSources?.[0];
        return src?.MediaStreams || [];
      });
      const subtitleStreams = computed(() => currentMediaStreams.value.filter(s => s.Type === 'Subtitle'));
      const audioStreams = computed(() => currentMediaStreams.value.filter(s => s.Type === 'Audio'));

      function mediaSourceTags(src) {
        const tags = [];
        if (src.Container) tags.push(src.Container.toUpperCase());
        const v = src.MediaStreams?.find(s => s.Type === 'Video');
        if (v) { tags.push(v.Codec); if (v.VideoRange === 'HDR10') tags.push('HDR10'); if (v.VideoRange === 'DOVI') tags.push('DV'); }
        const a = src.MediaStreams?.find(s => s.Type === 'Audio');
        if (a) tags.push(a.Codec);
        return tags;
      }

      async function toggleFavorite(item, remove) {
        try {
          if (remove) await api(`/Users/${localStorage.getItem('eio_user_id')}/FavoriteItems/${item.Id}`, { method: 'DELETE' });
          else await api(`/Users/${localStorage.getItem('eio_user_id')}/FavoriteItems/${item.Id}`, { method: 'POST' });
          item.UserData ||= {};
          item.UserData.IsFavorite = !remove;
          if (navPage.value === 'favorites') loadCollection('favorites');
        } catch (e) { console.error(e); }
      }

      /* ---- search ---- */
      const searchQuery = ref('');
      const searchResults = ref([]);
      const searchLoading = ref(false);
      let searchTimer = null;

      function debouncedSearch() {
        clearTimeout(searchTimer);
        searchTimer = setTimeout(async () => {
          if (!searchQuery.value.trim()) { searchResults.value = []; return; }
          searchLoading.value = true;
          try {
            // Top-level items only: raw search hits are dominated by episodes
            // which are not browsable from the results — series and movies are.
            const res = await api(`/Users/${localStorage.getItem('eio_user_id')}/Items?SearchTerm=${encodeURIComponent(searchQuery.value)}&Limit=30&Recursive=true&IncludeItemTypes=Series,Movie&Fields=PrimaryImageAspectRatio,ProductionYear,Overview`);
            searchResults.value = res.Items || [];
            refreshIcons();
          } catch (e) { console.error(e); }
          searchLoading.value = false;
        }, 400);
      }

      /* ---- images ---- */
      function imageUrl(item, width) {
        if (!item) return '';
        const tag = item.ImageTags?.Primary || item.BackdropImageTags?.[0];
        const type = item.ImageTags?.Primary ? 'Primary' : 'Backdrop';
        if (!tag) return '';
        return apiRaw(`/Items/${item.Id}/Images/${type}?tag=${tag}&maxWidth=${width}&quality=85`);
      }
      function backdropUrl(item) { return apiRaw(`/Items/${item.Id}/Images/Backdrop/0?maxWidth=1920&quality=85`); }

      /* ---- player ---- */
      const playerContainer = ref(null);
      const playerMode = ref('native');
      const playItemId = ref('');
      const playingItem = ref(null);
      const playSrc = ref(null);
      const playMediaSourceId = ref(null);
      const wasmLoading = ref(false);
      const wasmLoadingMsg = ref('');
      const wasmPlaying = ref(false);
      const wasmTime = ref(0);
      const wasmDuration = ref(0);
      const wasmTimeHuman = computed(() => fmtTicks(wasmTime.value * 10000000));
      const wasmDurationHuman = computed(() => fmtTicks(wasmDuration.value * 10000000));
      const wasmSubTrack = ref(-1);
      const showSubMenu = ref(false);
      const hdrShaderActive = ref(false);
      const playVersions = ref([]);
      const playAudioIndex = ref(-1);
      const episodeQueue = ref([]);
      const queueError = ref('');
      const trackBusy = ref(false);
      const previousEpisode = computed(() => {
        const i = episodeQueue.value.findIndex(e => e.Id === playingItem.value?.Id);
        return i > 0 ? episodeQueue.value[i - 1] : null;
      });
      const nextEpisode = computed(() => {
        const i = episodeQueue.value.findIndex(e => e.Id === playingItem.value?.Id);
        return i >= 0 ? episodeQueue.value[i + 1] || null : null;
      });
      const audioTracks = computed(() => playSrc.value?.MediaStreams?.filter(s => s.Type === 'Audio') || []);

      let player = null;
      let playSessionId = null;

      const subtitleTracks = computed(() => {
        if (!playSrc.value) return [];
        return playSrc.value.MediaStreams?.filter(s => s.Type === 'Subtitle') || [];
      });

      // pendingMediaSourceId carries a version (MediaSource) choice from
      // playItem() into startPlayback(), which the route handler cannot know.
      let pendingMediaSourceId = null;
      let pendingPlaybackOptions = {};
      // playerItemId records what the live player instance is playing so the
      // route handler can tell "already playing this" from "switching items".
      let playerItemId = null;
      let playbackGeneration = 0;
      let playbackRequest = null;
      let playerReturnRoute = '#/home';

      async function playItem(item, mediaSourceId, options = {}) {
        if (navPage.value !== 'player') playerReturnRoute = window.location.hash || '#/home';
        // A series has no media sources of its own: PlaybackInfo against a
        // series fails upstream (500, "cannot cast Series to IHasMediaSources").
        // Resolve to the next-up episode, or the first episode when nothing
        // has been watched yet.
        if (item.Type === 'Series') {
          const ep = await resolveSeriesEpisode(item.Id);
          if (!ep) { alert('该剧集没有可播放的集'); return; }
          item = ep;
          options = {}; mediaSourceId = null;
        }
        playItemId.value = item.Id;
        playingItem.value = item;
        pendingMediaSourceId = mediaSourceId || null;
        pendingPlaybackOptions = options;
        const target = '#/play/' + item.Id;
        if (window.location.hash === target) {
          // Same route: no hashchange will fire, so drive playback directly.
          await startPlayerFromRoute(item.Id, true);
        } else {
          window.location.hash = target;
        }
      }

      // resolveSeriesEpisode picks the episode a series play button should
      // start: NextUp (the first unwatched episode) when available, otherwise
      // the first episode of the first season.
      async function resolveSeriesEpisode(seriesId) {
        const uid = localStorage.getItem('eio_user_id');
        try {
          const next = await api(`/Shows/NextUp?SeriesId=${seriesId}&UserId=${uid}&Limit=1`);
          if (next.Items?.length) return next.Items[0];
        } catch (e) { console.error('NextUp', e); }
        try {
          const eps = await allItems(api, `/Shows/${seriesId}/Episodes`, { UserId: uid, Fields: 'PrimaryImageAspectRatio,RunTimeTicks,SeriesId' });
          return eps.sort(episodeOrder)[0] || null;
        } catch (e) { console.error('resolveSeriesEpisode', e); return null; }
      }

      // startPlayerFromRoute is the single entry point that begins playback.
      // Deep links (#/play/{id} typed or reloaded) and in-app navigation both
      // land here; playItem() only records the intent and lets the router call
      // this, which keeps "start playback" from running twice.
      async function startPlayerFromRoute(id, force) {
        if (!id || !localStorage.getItem('eio_token')) return;
        if (!force && playItemId.value === id && playerItemId === id) return;
        stopPlayer();
        const generation = playbackGeneration;
        playbackRequest = new AbortController();
        const signal = playbackRequest.signal;
        const sourceId = pendingMediaSourceId;
        const options = pendingPlaybackOptions;
        wasmLoading.value = true; wasmLoadingMsg.value = '正在加载播放信息…';
        seekPreview.value = null;
        try {
          let item = playingItem.value && playingItem.value.Id === id ? playingItem.value : null;
          if (!item?.MediaSources?.length) {
            item = await api(`/Users/${localStorage.getItem('eio_user_id')}/Items/${id}?Fields=MediaSources,Overview,RunTimeTicks,UserData,SeriesId,SeasonId,SeriesName,IndexNumber,ParentIndexNumber`, { signal });
            if (generation !== playbackGeneration) return;
            playingItem.value = item;
          }
          playItemId.value = id;
          playVersions.value = item.MediaSources || [];
          await loadEpisodeQueue(item, signal);
          await nextTick(); // the player container only exists once navPage renders
          if (generation !== playbackGeneration) return;
          await startPlayback(item, sourceId, generation, signal, options);
        } catch (e) {
          if (e.name === 'AbortError' || generation !== playbackGeneration) return;
          console.error('startPlayerFromRoute', e);
          alert('无法加载播放内容：' + e.message);
        } finally {
          if (generation === playbackGeneration) { pendingMediaSourceId = null; pendingPlaybackOptions = {}; }
        }
      }

      async function loadEpisodeQueue(item, signal) {
        queueError.value = '';
        if (!item.SeriesId) { episodeQueue.value = []; return; }
        try {
          const episodes = await allItems(api, `/Shows/${item.SeriesId}/Episodes`, {
            UserId: localStorage.getItem('eio_user_id'), Fields: 'SeriesId,RunTimeTicks,UserData' }, { signal });
          if (!signal?.aborted) episodeQueue.value = episodes.sort(episodeOrder);
        } catch (e) {
          if (e.name === 'AbortError') throw e;
          episodeQueue.value = []; queueError.value = '相邻剧集加载失败，请重试';
        }
      }

      function retryEpisodeQueue() { return loadEpisodeQueue(playingItem.value, playbackRequest?.signal); }

      async function startPlayback(item, mediaSourceId, generation, signal, options = {}) {
        try {
          // Declare a browser DeviceProfile so the upstream decides between
          // direct stream and transcode. Without it Emby assumes a client
          // that can direct-play everything and returns no usable stream URL
          // (no DirectStreamUrl, no TranscodingUrl) for the web player.
          const deviceProfile = EIOData.deviceProfile();
          const info = await api(`/Items/${item.Id}/PlaybackInfo`, {
            method: 'POST',
            signal,
            body: JSON.stringify({
              UserId: localStorage.getItem('eio_user_id'),
              MediaSourceId: mediaSourceId || item.MediaSources?.[0]?.Id,
              DeviceProfile: deviceProfile,
              AutoOpenLiveStream: true,
              MaxStreamingBitrate: 120000000,
              MaxWidth: 1920,
              MaxHeight: 1080,
              AudioStreamIndex: options.audioIndex >= 0 ? options.audioIndex : undefined,
              SubtitleStreamIndex: options.subtitleIndex,
              EnableDirectPlay: options.forceTranscode ? false : undefined,
              EnableDirectStream: options.forceTranscode ? false : undefined,
            }),
          });
          if (generation !== playbackGeneration) return;
          playSrc.value = info.MediaSources?.find(s => s.Id === mediaSourceId) || info.MediaSources?.[0];
          playMediaSourceId.value = playSrc.value?.Id;
          if (!playSrc.value) throw new Error('No media sources');
          playAudioIndex.value = options.audioIndex >= 0 ? options.audioIndex : defaultAudio(playSrc.value);

          // Create player
          const instance = new EIOPlayer(playerContainer.value);
          player = instance;
          instance.sessionId = info.PlaySessionId;
          playSessionId = info.PlaySessionId;
          playerItemId = item.Id;
          instance.onState = (state) => {
            if (player !== instance || generation !== playbackGeneration) return;
            playerMode.value = state.mode;
            wasmPlaying.value = state.playing;
            wasmTime.value = state.time;
            wasmDuration.value = state.duration;
            if (state.loading != null) wasmLoading.value = state.loading;
            if (state.message) wasmLoadingMsg.value = state.message;
            if (state.subtitleError) alert(state.subtitleError);
          };
          instance.onProgress = (ticks) => {
            api('/Sessions/Playing/Progress', {
              method: 'POST',
              body: JSON.stringify({ ItemId: item.Id, MediaSourceId: instance.mediaSource.Id,
                PlaySessionId: instance.sessionId, PositionTicks: ticks, IsPaused: !instance.playing }),
            }).catch(() => {});
          };
          instance.onEnded = () => { if (player === instance) exitPlayer(); };
          instance.onError = (msg) => {
            if (player !== instance) return;
            alert('播放错误：' + msg);
            exitPlayer();
          };

          // Stream the original container through libmedia; the upstream's
          // browser-compatible HLS URL is available if direct loading fails.
          const container = (playSrc.value.Container || 'mp4').split(',')[0].trim() || 'mp4';
          const streamPath = playSrc.value.DirectStreamUrl
            || `/Videos/${item.Id}/stream.${container}?Static=true&MediaSourceId=${encodeURIComponent(playSrc.value.Id)}`;
          const url = apiRaw(streamPath);
          if (options.forceTranscode && !playSrc.value.TranscodingUrl) throw new Error('上游未提供可切换音轨的兼容播放流');
          await instance.load(options.forceTranscode ? apiRaw(playSrc.value.TranscodingUrl) : url, playSrc.value, item, {
            fallbackUrl: playSrc.value.TranscodingUrl ? apiRaw(playSrc.value.TranscodingUrl) : null,
            audioIndex: playAudioIndex.value,
            subtitleIndex: options.subtitleIndex,
          });
          if (player !== instance || generation !== playbackGeneration) return;
          wasmSubTrack.value = instance.subtitleIndex ?? -1;
          const seconds = options.startSeconds ?? (item.UserData?.PlaybackPositionTicks || 0) / 10000000;
          if (seconds > 0 && seconds < instance.duration) await instance.seek(seconds);
          if (options.paused) await instance.pause();
          instance.setVolume(playerVolume.value);
          await api('/Sessions/Playing', { method: 'POST', body: JSON.stringify({ ItemId: item.Id,
            MediaSourceId: playSrc.value.Id, PlaySessionId: instance.sessionId,
            PositionTicks: instance.positionTicks, IsPaused: !instance.playing }), signal }).catch(() => {});
          refreshIcons();
        } catch (e) {
          if (e.name === 'AbortError' || generation !== playbackGeneration) return;
          console.error('startPlayback', e);
          alert('播放失败：' + e.message);
          exitPlayer();
        }
      }

      // stopPlayer tears the player down and reports the stop upstream. It is
      // separate from exitPlayer because leaving the player route must stop
      // playback without necessarily navigating.
      function stopPlayer() {
        playbackGeneration++;
        playbackRequest?.abort();
        playbackRequest = null;
        wasmLoading.value = false;
        wasmSubTrack.value = -1;
        if (!player) return;
        api('/Sessions/Playing/Stopped', {
          method: 'POST',
          body: JSON.stringify({ ItemId: playerItemId, MediaSourceId: player.mediaSource?.Id,
            PlaySessionId: player.sessionId, PositionTicks: player.positionTicks }),
        }).catch(() => {});
        player.destroy();
        player = null;
        playerItemId = null;
        playSessionId = null;
      }

      function exitPlayer() {
        stopPlayer();
        window.location.hash = playerReturnRoute.startsWith('#/play/')
          ? '#/item/' + (playingItem.value?.SeriesId || playingItem.value?.Id) : playerReturnRoute;
      }

      async function switchVersion(id) {
        if (!player || trackBusy.value || id === playMediaSourceId.value) return;
        trackBusy.value = true;
        try {
          await playItem(playingItem.value, id, { startSeconds: player.positionTicks / 10000000, paused: !player.playing });
        } finally { trackBusy.value = false; }
      }

      async function selectAudio(index) {
        if (!player || trackBusy.value || index === playAudioIndex.value) return;
        trackBusy.value = true;
        const instance = player;
        try {
          if (await instance.selectAudio(index)) playAudioIndex.value = index;
          else await playItem(playingItem.value, playMediaSourceId.value, { audioIndex: index,
            subtitleIndex: wasmSubTrack.value, startSeconds: instance.positionTicks / 10000000,
            paused: !instance.playing, forceTranscode: !!instance.core?.isHls?.() });
        } catch (e) { alert('音轨切换失败：' + e.message); }
        finally { trackBusy.value = false; }
      }

      const seekPreview = ref(null);
      const playerVolume = ref(1);
      const seekTitle = computed(() => '跳转至 ' + fmtTicks((seekPreview.value ?? wasmTime.value) * 10000000));
      function previewSeek(value) { seekPreview.value = Number(value); }
      function changeVolume(value) { playerVolume.value = Number(value); player?.setVolume(playerVolume.value); }
      function toggleFullscreen() {
        const result = document.fullscreenElement ? document.exitFullscreen() : playerContainer.value?.requestFullscreen();
        result?.catch(e => alert('无法进入全屏：' + e.message));
      }

      async function selectSubtitle(idx) {
        if (!player || !playSrc.value) return;
        try { await player.selectSubtitle(idx); wasmSubTrack.value = idx; }
        catch (e) { alert('字幕切换失败：' + e.message); }
      }

      function wasmTogglePause() {
        if (player) {
          Promise.resolve(player.playing ? player.pause() : player.play()).catch(e => alert('播放失败：' + e.message));
        }
      }

      function onWasmSeek(v) {
        const seconds = parseFloat(v);
        seekPreview.value = null;
        wasmTime.value = seconds;
        if (player) Promise.resolve(player.seek(seconds)).catch(e => alert('跳转失败：' + e.message));
      }

      /* ---- lifecycle ---- */
      // lucide only converts <i data-lucide> elements that exist when
      // createIcons() runs, and Vue renders nearly everything after login and
      // after each data load. refreshIcons re-runs the conversion after the
      // next DOM update. It must be called explicitly — never from a
      // MutationObserver: this lucide build keeps data-lucide on the
      // replacement SVG, so an observer would loop forever.
      function refreshIcons() {
        nextTick(() => { try { lucide.createIcons(); } catch (e) {} });
      }

      onMounted(async () => {
        window.addEventListener('hashchange', syncFromHash);
        lucide.createIcons();
        if (localStorage.getItem('eio_token')) {
          try {
            const me = await api(`/Users/${localStorage.getItem('eio_user_id')}`);
            // An admin token issued before the web-player boundary existed
            // (or obtained anywhere else) must not keep a web session alive:
            // the token carries admin rights and /admin is the only supported
            // admin entry point. Drop it and fall back to the login screen.
            if (me.Policy?.IsAdministrator) {
              localStorage.removeItem('eio_token');
              localStorage.removeItem('eio_user_id');
              loginError.value = '管理员账号请使用 /admin 管理面板登录。';
              page.value = 'login';
              return;
            }
            currentUser.value = me;
            const hadHash = !!window.location.hash;
            window.location.hash = window.location.hash || '#/home';
            // When the hash was just set, the hashchange event drives the
            // first sync; calling it here too would load the home data twice.
            if (hadHash) syncFromHash();
          } catch { doLogout(); }
        } else { syncFromHash(); }
      });

      onBeforeUnmount(() => {
        window.removeEventListener('hashchange', syncFromHash);
        libraryRequest?.abort(); libraryPageRequest?.abort(); itemRequest?.abort(); collectionRequest?.abort();
        clearTimeout(searchTimer);
        stopPlayer();
      });

      return {
        page, navPage, loginForm, loginError, loginLoading, currentUser, passWrap, mobileMenu, pages,
        navigateTo, doLogin, doLogout, focusPass, libraryIcon,
        libraries, latestSections, loadHome, collectionItems, collectionLoading, collectionError, loadCollection,
        libraryName, libraryItems, libraryLoading, libFilter, libSort, libGenres, libLimit,
        openLibrary, loadLibrary, loadLibraryItems, loadLibraryMore, libraryHasMore, libraryError,
        itemDetail, itemDetailLoading, seriesSeasons, seasonEpisodes, openItem, loadItem, itemError, seasonErrors,
        currentMediaSourceId, chooseDetailSource, detailAudioIndex, detailSubtitleIndex, playDetail, retryItem,
        itemDetailIs4K, itemDetailIsHDR, itemDetailIsDV, itemDetailOfficialRating, runtimeMin, runtimeMin_,
        currentMediaStreams, subtitleStreams, audioStreams, currentVideoStream, mediaSourceTags,
        toggleFavorite, playItem,
        searchQuery, searchResults, searchLoading, debouncedSearch,
        imageUrl, backdropUrl,
        playerContainer, playerMode, playItemId, playSrc, playingItem, playVersions, playMediaSourceId,
        playAudioIndex, audioTracks, previousEpisode, nextEpisode, queueError, trackBusy, switchVersion, selectAudio, retryEpisodeQueue,
        seekPreview, seekTitle, playerVolume, previewSeek, changeVolume, toggleFullscreen,
        wasmLoading, wasmLoadingMsg, wasmPlaying, wasmTime, wasmDuration, wasmTimeHuman, wasmDurationHuman,
        wasmSubTrack, showSubMenu, hdrShaderActive, subtitleTracks,
        exitPlayer, wasmTogglePause, onWasmSeek, selectSubtitle,
        startPlayback,
      };
    },
  }).mount('#app');
})();
