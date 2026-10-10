/* Emby-In-One Web Player — single-page application (v1.4.4-rc3)
 *
 * Vue 3 global build, no build step. Loads the shared vendor/ scripts plus
 * libmedia and JASSUB. Player engine: player-libmedia.js (window.EIOPlayer).
 * Presentation state: ui.js / page-state.js / player-ui.js / ui-components.js.
 */
(function () {
  const { createApp, ref, reactive, computed, onMounted, onBeforeUnmount, nextTick } = Vue;

  const API = ''; // same origin
  const { query, all: allItems, episodeOrder, defaultAudio, defaultSubtitle } = EIOData;

  /* ================= helpers ================= */

  async function api(path, opts = {}) {
    const token = localStorage.getItem('eio_token');
    // Authentication uses X-Emby-Token only. The web client deliberately sends
    // no client identity: on passthrough upstreams those headers would win the
    // identity resolution over the captured real-client identity.
    const headers = { 'Content-Type': 'application/json', ...(opts.headers || {}) };
    if (token) headers['X-Emby-Token'] = token;
    const res = await fetch(`${API}${path}`, { ...opts, headers });
    if (!res.ok) {
      const body = await res.text().catch(() => '');
      let detail = body || res.statusText;
      try { const parsed = JSON.parse(body); if (parsed && parsed.message) detail = parsed.message; } catch (e) {}
      const error = new Error(`${res.status}: ${detail}`);
      error.status = res.status;
      throw error;
    }
    if (res.status === 204) return null;
    return res.json();
  }

  // Raw URLs (images, streams, subtitles) authenticate with api_key in the
  // query string — same token, and equally free of client identity.
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

  // Strips any URL/credential-looking fragment from an error before it can be
  // shown in a Toast or error card.
  function safeError(error) {
    return String(error?.message || error || '')
      .replace(/https?:\/\/\S+/gi, '')
      .replace(/api_key=[^\s&]*/gi, '')
      .trim().slice(0, 160);
  }

  const app = createApp({
    setup() {
      /* ---- auth ---- */
      const page = ref('login');
      const navPage = ref('home');
      const loginForm = ref({ user: '', pass: '' });
      const loginError = ref('');
      const loginLoading = ref(false);
      const loginShake = ref(false);
      const passwordVisible = ref(false);
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

      /* ---- navigation ---- */
      const pages = [
        { id: 'home', label: '首页', icon: 'home' },
        { id: 'resume', label: '继续观看', icon: 'history' },
        { id: 'favorites', label: '收藏', icon: 'heart' },
        { id: 'search', label: '搜索', icon: 'search' },
      ];
      // Desktop sidebar shows home/favorites/search only; "继续观看 · 全部" is a
      // home affordance that still routes to the original #/resume page.
      const sidebarPages = computed(() => pages.filter(p => p.id !== 'resume'));
      const mobileMenu = ref(false);
      function navigateTo(id) { window.location.hash = '#/' + id; }
      function openItem(id) { window.location.hash = '#/item/' + id; }
      function openLibrary(id) { window.location.hash = '#/library/' + id; }

      function libraryIcon(lib) {
        const t = (lib?.CollectionType || '').toLowerCase();
        const map = {
          movies: 'film', tvshows: 'tv', music: 'music', musicvideos: 'clapperboard',
          books: 'book-open', audiobooks: 'headphones', comics: 'book', homevideos: 'video',
          photos: 'image', playlists: 'list-music', boxsets: 'folder', livetv: 'radio', channels: 'radio-tower',
        };
        return map[t] || 'library';
      }

      /* ---- toast ---- */
      const toasts = reactive(EIOUI.createToasts());

      /* ---- auth flows ---- */
      function togglePassword() { passwordVisible.value = !passwordVisible.value; }

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
          recentSearches.read();
          window.location.hash = '#/home';
        } catch (e) {
          loginError.value = '登录失败：' + safeError(e);
          loginShake.value = true;
          setTimeout(() => { loginShake.value = false; }, 400);
        }
        loginLoading.value = false;
      }

      function doLogout() {
        home.cancel(); search.cancel();
        toasts.clear();
        stopPlayer(); playerUI.reset(); playerUI.detach();
        localStorage.removeItem('eio_token'); localStorage.removeItem('eio_user_id');
        window.location.hash = '#/';
        location.reload();
      }

      function focusPass() { passWrap.value?.querySelector('input')?.focus(); }

      /* ---- home ---- */
      const libraries = ref([]);
      const latestSections = ref([]);
      const home = reactive(EIOPages.createHome({
        api, query,
        getUserId: () => localStorage.getItem('eio_user_id'),
        isActive: () => navPage.value === 'home' && page.value === 'app',
        libraries, latestSections,
      }));

      const hero = reactive(EIOUI.createHero({
        getItems: () => latestSections.value.flatMap(sec => sec.items),
        isActive: () => navPage.value === 'home' && page.value === 'app',
        onChange: () => refreshIcons(),
      }));

      function loadHome() {
        const pending = home.load();
        Promise.resolve(pending).finally(() => { if (navPage.value === 'home') hero.reset(); });
        return pending;
      }

      /* ---- collection (resume / favorites) ---- */
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
        } catch (e) {
          if (e.name !== 'AbortError' && collectionRequest === request && !request.signal.aborted) {
            collectionError.value = '加载失败：' + safeError(e);
          }
        } finally {
          if (collectionRequest === request) collectionLoading.value = false;
        }
        refreshIcons();
      }

      /* ---- library ---- */
      const libraryScrolled = ref(false);
      const libraryView = reactive(EIOUI.createLibraryView());
      const library = EIOLibrary.create({
        api, query, nextTick,
        formatError: safeError,
        getUserId: () => localStorage.getItem('eio_user_id'),
        isActive: () => navPage.value === 'library',
        libraries, view: libraryView, refreshIcons,
      });
      const latestErrors = computed(() => libraries.value
        .filter(lib => home.latestError[lib.Id])
        .map(lib => ({ lib, message: home.latestError[lib.Id] })));
      function loadLibraryPage(id) { navPage.value = 'library'; return library.load(id); }

      /* ---- item detail ---- */
      const itemDetail = ref(null);
      const itemDetailLoading = ref(false);
      const seriesSeasons = ref([]);
      const seasonEpisodes = ref({});
      const seriesLoading = ref(false);
      const seriesError = ref('');
      const seasonLoading = ref({});
      const seasonLoadBusy = ref(false);
      const currentMediaSourceId = ref(null);
      const detailAudioIndex = ref(-1);
      const detailSubtitleIndex = ref(-1);
      const itemError = ref('');
      const seasonErrors = ref({});
      const favoriteBusyId = ref('');
      let itemRequest = null;
      const detailView = reactive(EIOUI.createDetailView());

      const DETAIL_FIELDS = 'MediaSources,ProviderIds,Overview,Genres,GenreItems,ProductionYear,RunTimeTicks,OfficialRating,CommunityRating,BackdropImageTags,SeriesId,SeasonId,UserData,OriginalTitle,SeriesName,IndexNumber,ParentIndexNumber';

      async function loadItem(id) {
        itemRequest?.abort();
        const request = itemRequest = new AbortController();
        itemError.value = ''; seasonErrors.value = {}; seriesError.value = '';
        seriesSeasons.value = []; seasonEpisodes.value = {};
        seasonLoading.value = {}; seriesLoading.value = false; seasonLoadBusy.value = false;
        detailView.reset();
        navPage.value = 'item'; itemDetailLoading.value = true; itemDetail.value = null;
        try {
          const detail = await api(`/Users/${localStorage.getItem('eio_user_id')}/Items/${id}?Fields=${DETAIL_FIELDS}`, { signal: request.signal });
          if (itemRequest !== request || request.signal.aborted) return;
          itemDetail.value = detail;
          chooseDetailSource(detail.MediaSources?.[0]?.Id || null);
          itemDetailLoading.value = false; // hero shows before seasons finish
          refreshIcons();
          if (detail.Type === 'Series') await loadSeriesSeasons(detail, request);
        } catch (e) {
          if (e.name !== 'AbortError' && itemRequest === request) itemError.value = '加载失败：' + safeError(e);
        } finally {
          if (itemRequest === request) itemDetailLoading.value = false;
        }
      }

      async function loadSeriesSeasons(detail, request) {
        seriesLoading.value = true; seriesError.value = ''; seasonLoadBusy.value = true;
        const uid = localStorage.getItem('eio_user_id');
        try {
          const seasons = await allItems(api, `/Shows/${detail.Id}/Seasons`, { UserId: uid }, { signal: request.signal });
          if (itemRequest !== request || request.signal.aborted) return;
          seriesSeasons.value = seasons;
          if (seasons.length && !detailView.selectedSeasonId) detailView.setSeason(seasons[0].Id);
          // Preload seasons serially and bounded; one slow/failed season never
          // hides the others or the main detail.
          for (const season of seasons) {
            if (itemRequest !== request) return;
            await loadSeasonEpisodes(detail.Id, season.Id, request);
          }
        } catch (e) {
          if (e.name === 'AbortError') return;
          if (itemRequest === request) seriesError.value = '季列表加载失败，请重试';
        } finally {
          if (itemRequest === request) { seriesLoading.value = false; seasonLoadBusy.value = false; }
        }
        if (itemRequest === request) refreshIcons();
      }

      async function loadSeasonEpisodes(seriesId, seasonId, request) {
        const uid = localStorage.getItem('eio_user_id');
        seasonLoading.value = { ...seasonLoading.value, [seasonId]: true };
        const cleared = { ...seasonErrors.value }; delete cleared[seasonId]; seasonErrors.value = cleared;
        try {
          const episodes = await allItems(api, `/Shows/${seriesId}/Episodes`, {
            UserId: uid, SeasonId: seasonId,
            Fields: 'PrimaryImageAspectRatio,RunTimeTicks,SeriesId,SeasonId,UserData',
          }, { signal: request.signal });
          if (itemRequest !== request || request.signal.aborted) return;
          seasonEpisodes.value = { ...seasonEpisodes.value, [seasonId]: episodes.sort(episodeOrder) };
        } catch (e) {
          if (e.name === 'AbortError') return;
          if (itemRequest === request) seasonErrors.value = { ...seasonErrors.value, [seasonId]: '本季加载失败，请重试' };
        } finally {
          if (itemRequest === request && !request.signal.aborted) {
            seasonLoading.value = { ...seasonLoading.value, [seasonId]: false };
          }
        }
      }

      function retrySeason(seasonId) {
        if (seasonLoadBusy.value || !itemRequest || !itemDetail.value) return Promise.resolve();
        seasonLoading.value = { ...seasonLoading.value, [seasonId]: true };
        return loadSeasonEpisodes(itemDetail.value.Id, seasonId, itemRequest);
      }

      function retrySeries() {
        if (!itemDetail.value) return Promise.resolve();
        return loadSeriesSeasons(itemDetail.value, itemRequest);
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

      const isFavorite = computed(() => !!itemDetail.value?.UserData?.IsFavorite);
      const itemDetailIs4K = computed(() => { const s = currentVideoStream.value; return s && s.Width >= 3800; });
      const itemDetailIsHDR = computed(() => { const s = currentVideoStream.value; return s && (s.VideoRange === 'HDR10' || s.VideoRange === 'HLG'); });
      const itemDetailIsDV = computed(() => { const s = currentVideoStream.value; return s && s.VideoRange === 'DOVI'; });
      const itemDetailOfficialRating = computed(() => itemDetail.value?.OfficialRating);
      const runtimeMin = computed(() => itemDetail.value?.RunTimeTicks ? Math.round(itemDetail.value.RunTimeTicks / 600000000) : null);
      const runtimeMin_ = (it) => it?.RunTimeTicks ? Math.round(it.RunTimeTicks / 600000000) : null;
      function epProgress(ep) {
        const duration = ep?.RunTimeTicks || 0;
        if (!duration) return 0;
        const ratio = Math.max(0, Math.min(1, (ep.UserData?.PlaybackPositionTicks || 0) / duration));
        return ep.UserData?.Played || ratio <= 0 || ratio >= 0.95 ? 0 : ratio;
      }

      const currentSource = computed(() => {
        const d = itemDetail.value;
        if (!d?.MediaSources?.length) return null;
        return (currentMediaSourceId.value && d.MediaSources.find(m => m.Id === currentMediaSourceId.value)) || d.MediaSources[0];
      });
      const currentVideoStream = computed(() => currentSource.value?.MediaStreams?.find(s => s.Type === 'Video'));
      const currentMediaStreams = computed(() => currentSource.value?.MediaStreams || []);
      const videoStreams = computed(() => currentMediaStreams.value.filter(s => s.Type === 'Video'));
      const subtitleStreams = computed(() => currentMediaStreams.value.filter(s => s.Type === 'Subtitle'));
      const audioStreams = computed(() => currentMediaStreams.value.filter(s => s.Type === 'Audio'));

      const genreList = computed(() => {
        const d = itemDetail.value;
        if (!d) return [];
        if (Array.isArray(d.Genres) && d.Genres.length) return d.Genres;
        return (d.GenreItems || []).map(g => g.Name);
      });

      const detailResumeSeconds = computed(() => {
        const d = itemDetail.value;
        if (!d || d.Type === 'Series') return 0;
        return Math.floor((d.UserData?.PlaybackPositionTicks || 0) / 10000000);
      });
      const playButtonLabel = computed(() => {
        if (itemDetail.value?.Type === 'Series') return '播放下一集';
        return detailResumeSeconds.value > 0 ? '从 ' + fmtTicks(detailResumeSeconds.value * 10000000) + ' 继续' : '播放';
      });

      function mediaSourceTags(src) {
        const tags = [];
        if (src.Container) tags.push(src.Container.toUpperCase());
        const v = src.MediaStreams?.find(s => s.Type === 'Video');
        if (v) { if (v.Codec) tags.push(v.Codec); if (v.VideoRange === 'HDR10') tags.push('HDR10'); if (v.VideoRange === 'DOVI') tags.push('DV'); }
        const a = src.MediaStreams?.find(s => s.Type === 'Audio');
        if (a) { if (a.Codec) tags.push(a.Codec); if (isAtmos(a)) tags.push('Atmos'); }
        return tags;
      }
      function sourceRate(src) {
        const video = src?.MediaStreams?.find(s => s.Type === 'Video');
        const bitrate = src?.Bitrate || video?.BitRate;
        return bitrate ? (bitrate / 1e6).toFixed(1) + ' Mbps' : '';
      }
      // Only explicit object-audio metadata/name evidence counts as Atmos.
      function isAtmos(stream) {
        const haystack = `${stream?.Profile || ''} ${stream?.DisplayTitle || ''} ${stream?.Title || ''}`.toLowerCase();
        return haystack.includes('atmos');
      }
      function audioTrackLabel(stream) {
        const parts = [stream.DisplayTitle || stream.Language || stream.Codec || '未知'];
        if (stream.ChannelLayout || stream.Channels) parts.push(stream.ChannelLayout || (stream.Channels + 'ch'));
        return parts.filter(Boolean).join(' · ');
      }

      async function toggleFavorite(item, remove) {
        const id = item?.Id;
        if (!id || favoriteBusyId.value === id) return;
        favoriteBusyId.value = id;
        const previous = item.UserData?.IsFavorite;
        try {
          if (remove) await api(`/Users/${localStorage.getItem('eio_user_id')}/FavoriteItems/${id}`, { method: 'DELETE' });
          else await api(`/Users/${localStorage.getItem('eio_user_id')}/FavoriteItems/${id}`, { method: 'POST' });
          item.UserData = { ...(item.UserData || {}), IsFavorite: !remove };
          toasts.push(remove ? '已取消收藏' : '已加入收藏');
          if (navPage.value === 'favorites' && remove) loadCollection('favorites');
        } catch (e) {
          if (item.UserData) item.UserData.IsFavorite = previous;
          toasts.push({ message: '收藏操作失败：' + safeError(e), kind: 'error' });
        } finally {
          favoriteBusyId.value = ''; refreshIcons();
        }
      }

      /* ---- search ---- */
      const searchQuery = ref('');
      const searchResults = ref([]);
      const searchLoading = ref(false);
      const search = reactive(EIOPages.createSearch({
        api, query,
        getUserId: () => localStorage.getItem('eio_user_id'),
        isActive: () => navPage.value === 'search' && page.value === 'app' && searchQuery.value.trim().length > 0,
        searchQuery, searchResults, searchLoading,
      }));
      const recentSearches = reactive(EIOUI.createRecentSearches({ getUserId: () => localStorage.getItem('eio_user_id') }));

      function onSearchInput() { search.schedule(); }
      function submitSearch() { if (searchQuery.value.trim()) recentSearches.add(searchQuery.value); search.schedule(); }
      function useRecent(term) { searchQuery.value = term; search.schedule(); }
      function removeRecent(term) { recentSearches.remove(term); }
      function recordRecent() { if (searchQuery.value.trim()) recentSearches.add(searchQuery.value); }

      /* ---- images ---- */
      function imageUrl(item, width) {
        if (!item) return '';
        const tag = item.ImageTags?.Primary || item.PrimaryImageTag;
        if (!tag) return '';
        return apiRaw(`/Items/${item.Id}/Images/Primary?tag=${tag}&maxWidth=${width}&quality=85`);
      }
      function backdropUrl(item) {
        const tag = item?.BackdropImageTags?.[0];
        if (!item || !tag) return '';
        return apiRaw(`/Items/${item.Id}/Images/Backdrop/0?tag=${tag}&maxWidth=1920&quality=85`);
      }
      // Hero background requires a real backdrop tag; otherwise CSS gradient.
      function heroImageUrl(item) { return item?.BackdropImageTags?.length ? backdropUrl(item) : ''; }

      /* ---- player ---- */
      const playerContainer = ref(null);
      // Template-visible liveness flag: the raw `player` variable is a plain
      // local and must never be referenced from the template (it would read as
      // undefined there and permanently disable the seek bar).
      const playerAlive = ref(false);
      const playerMode = ref('native');
      const playItemId = ref('');
      const playingItem = ref(null);
      const playSrc = ref(null);
      const playMediaSourceId = ref(null);
      const wasmPlaying = ref(false);
      const wasmTime = ref(0);
      const wasmDuration = ref(0);
      const wasmTimeHuman = computed(() => fmtTicks(wasmTime.value * 10000000));
      const wasmDurationHuman = computed(() => fmtTicks(wasmDuration.value * 10000000));
      const wasmSubTrack = ref(-1);
      const playerMessage = ref('');
      const playVersions = ref([]);
      const playAudioIndex = ref(-1);
      const episodeQueue = ref([]);
      const queueError = ref('');
      const trackBusy = ref(false);
      const subtitleBusy = ref(false);
      const playerVolume = ref(1);
      const lastNonZeroVolume = ref(1);
      const seekPreview = ref(null);
      let lastSubtitleIndex = -1;

      const previousEpisode = computed(() => {
        const i = episodeQueue.value.findIndex(e => e.Id === playingItem.value?.Id);
        return i > 0 ? episodeQueue.value[i - 1] : null;
      });
      const nextEpisode = computed(() => {
        const i = episodeQueue.value.findIndex(e => e.Id === playingItem.value?.Id);
        return i >= 0 ? episodeQueue.value[i + 1] || null : null;
      });
      const audioTracks = computed(() => playSrc.value?.MediaStreams?.filter(s => s.Type === 'Audio') || []);
      const subtitleTracks = computed(() => playSrc.value?.MediaStreams?.filter(s => s.Type === 'Subtitle') || []);

      const versionOptions = computed(() => (playVersions.value || []).map(s => ({ value: s.Id, label: s.Name || '默认版本' })));
      const audioOptions = computed(() => audioTracks.value.map(s => ({ value: s.Index, label: audioTrackLabel(s) })));
      const subtitleOptions = computed(() => [{ value: -1, label: '关闭字幕' },
        ...subtitleTracks.value.map(s => ({ value: s.Index, label: audioTrackLabel(s) }))]);

      const playerLoadingText = computed(() => {
        if (playerUI.operation === 'version') return '正在切换版本，进度已保留…';
        if (playerUI.operation === 'audio') return '正在切换音轨，进度已保留…';
        return playerMessage.value || '正在加载播放信息…';
      });
      const seekTitle = computed(() => '跳转至 ' + fmtTicks((seekPreview.value ?? wasmTime.value) * 10000000));
      const seekPercent = computed(() => {
        const dur = wasmDuration.value;
        const value = seekPreview.value ?? wasmTime.value;
        if (!dur || !Number.isFinite(dur) || dur <= 0) return 0;
        return Math.max(0, Math.min(100, (value / dur) * 100));
      });

      let player = null;
      let playSessionId = null;
      let pendingMediaSourceId = null;
      let pendingPlaybackOptions = {};
      let playerItemId = null;
      let playbackGeneration = 0;
      let playbackRequest = null;
      let playerReturnRoute = '#/home';
      let seekCommitTimer = null;
      let seekTarget = null;
      let seekInFlight = null;      // pending engine-seek target: stale time events must not move the thumb
      let seekInFlightTimer = null;
      let subtitleAttempt = null;
      let subtitleSwitchSerial = 0;
      let startupSubtitleError = null;

      const playerUI = reactive(EIOPlayerUI.create({
        getContainer: () => playerContainer.value,
        getPlayer: () => player,
        isActive: () => navPage.value === 'player' && page.value === 'app',
        actions: {
          togglePause: () => wasmTogglePause(),
          seekBy: delta => seekBy(delta),
          adjustVolume: delta => changeVolume(playerVolume.value + delta),
          toggleMute: () => toggleMute(),
          toggleFullscreen: () => toggleFullscreen(),
          toggleSubtitle: () => toggleSubtitleKey(),
          exit: () => exitPlayer(),
        },
      }));

      /* ---- playback entry ---- */
      async function playItem(item, mediaSourceId, options = {}) {
        if (navPage.value !== 'player') playerReturnRoute = window.location.hash || '#/home';
        // A series has no media sources of its own: PlaybackInfo against a
        // series fails upstream. Resolve to NextUp or the first episode.
        if (item?.Type === 'Series') {
          const ep = await resolveSeriesEpisode(item.Id);
          if (!ep) { toasts.push({ message: '该剧集没有可播放的集', kind: 'error' }); return; }
          item = ep; options = {}; mediaSourceId = null;
        }
        playItemId.value = item.Id;
        playingItem.value = item;
        pendingMediaSourceId = mediaSourceId || null;
        pendingPlaybackOptions = options;
        const target = '#/play/' + item.Id;
        if (window.location.hash === target) {
          await startPlayerFromRoute(item.Id, true);
        } else {
          window.location.hash = target;
        }
      }

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

      function buildRetryContext(item, mediaSourceId, options, seconds, paused) {
        return {
          item, mediaSourceId: mediaSourceId || null,
          audioIndex: options?.audioIndex ?? playAudioIndex.value,
          subtitleIndex: options?.subtitleIndex ?? wasmSubTrack.value,
          startSeconds: seconds ?? options?.startSeconds,
          paused: paused ?? options?.paused,
          forceTranscode: options?.forceTranscode,
        };
      }

      function handlePlaybackFailure(error, generation, retryContext) {
        if (generation !== playbackGeneration) return;
        if (error?.name === 'AbortError') return;
        const busy = error?.status === 429;
        const summary = safeError(error);
        stopPlayer();
        playerUI.fail(busy ? 'busy' : 'failed',
          busy ? '服务器并发已满' : (summary ? '无法加载播放内容：' + summary : '无法加载播放内容'),
          retryContext);
        refreshIcons();
      }

      // startPlayerFromRoute is the single entry point that begins playback.
      async function startPlayerFromRoute(id, force) {
        if (!id || !localStorage.getItem('eio_token')) return;
        if (!force && playItemId.value === id && playerItemId === id) return;
        const operation = playerUI.operation; // preserve an in-flight switch across stopPlayer
        stopPlayer();
        const generation = playbackGeneration;
        playbackRequest = new AbortController();
        const signal = playbackRequest.signal;
        const sourceId = pendingMediaSourceId;
        const options = pendingPlaybackOptions;
        playerMessage.value = '';
        playerUI.begin(operation || 'loading');
        playerUI.attach();
        seekPreview.value = null;
        let item = playingItem.value && playingItem.value.Id === id ? playingItem.value : null;
        try {
          if (!item?.MediaSources?.length) {
            item = await api(`/Users/${localStorage.getItem('eio_user_id')}/Items/${id}?Fields=${DETAIL_FIELDS}`, { signal });
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
          handlePlaybackFailure(e, generation, buildRetryContext(item || playingItem.value, sourceId, options));
        } finally {
          if (generation === playbackGeneration) { pendingMediaSourceId = null; pendingPlaybackOptions = {}; }
        }
      }

      async function loadEpisodeQueue(item, signal) {
        queueError.value = '';
        if (!item?.SeriesId) { episodeQueue.value = []; return; }
        try {
          const episodes = await allItems(api, `/Shows/${item.SeriesId}/Episodes`, {
            UserId: localStorage.getItem('eio_user_id'), Fields: 'SeriesId,RunTimeTicks,UserData' }, { signal });
          if (!signal?.aborted) episodeQueue.value = episodes.sort(episodeOrder);
        } catch (e) {
          if (e.name === 'AbortError') throw e;
          episodeQueue.value = []; queueError.value = '相邻剧集加载失败，请重试';
        }
      }

      function retryEpisodeQueue() {
        toasts.push({ message: '正在重试相邻剧集…' });
        return loadEpisodeQueue(playingItem.value, playbackRequest?.signal);
      }

      // A browser DeviceProfile lets the upstream choose direct vs transcode.
      function requestPlaybackInfo(item, mediaSourceId, signal, options) {
        return api(`/Items/${item.Id}/PlaybackInfo`, {
          method: 'POST', signal,
          body: JSON.stringify({
            UserId: localStorage.getItem('eio_user_id'),
            MediaSourceId: mediaSourceId || item.MediaSources?.[0]?.Id,
            DeviceProfile: EIOData.deviceProfile(), AutoOpenLiveStream: true,
            MaxStreamingBitrate: 120000000, MaxWidth: 1920, MaxHeight: 1080,
            AudioStreamIndex: options.audioIndex >= 0 ? options.audioIndex : undefined,
            SubtitleStreamIndex: options.subtitleIndex,
            EnableDirectPlay: options.forceTranscode ? false : undefined,
            EnableDirectStream: options.forceTranscode ? false : undefined,
          }),
        });
      }

      function createPlaybackInstance(item, info, generation) {
        const instance = new EIOPlayer(playerContainer.value);
        player = instance;
        playerAlive.value = true;
        instance.sessionId = info.PlaySessionId;
        playSessionId = info.PlaySessionId;
        playerItemId = item.Id;
        startupSubtitleError = null;
        instance.onState = (state) => {
          if (player !== instance || generation !== playbackGeneration) return;
          playerMode.value = state.mode;
          wasmPlaying.value = state.playing;
          // While an engine seek is pending, stale time events would snap the
          // progress thumb back to the pre-seek position; hold the target instead.
          if (seekInFlight == null || Math.abs(state.time - seekInFlight) < 1) wasmTime.value = state.time;
          wasmDuration.value = state.duration;
          if (state.message) playerMessage.value = state.message;
          if (state.subtitleError) handleSubtitleError(instance, generation, state.subtitleError);
          playerUI.acceptState(state);
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
          if (player !== instance || generation !== playbackGeneration) return;
          handlePlaybackFailure(new Error(msg), generation,
            buildRetryContext(playingItem.value, playMediaSourceId.value, {},
              instance.positionTicks / 10000000, !instance.playing));
        };
        return instance;
      }

      // A startup subtitle failure must not be mistaken for a live track.
      async function syncStartupSubtitle(instance, generation) {
        if (startupSubtitleError && startupSubtitleError.instance === instance) {
          await attemptSubtitleSelection(instance, -1, generation);
          wasmSubTrack.value = -1;
          if (player === instance && generation === playbackGeneration) {
            toasts.push({ message: '默认字幕加载失败，可重新选择', kind: 'error' });
          }
        } else {
          wasmSubTrack.value = instance.subtitleIndex ?? -1;
          if (wasmSubTrack.value >= 0) lastSubtitleIndex = wasmSubTrack.value;
        }
        startupSubtitleError = null;
      }

      async function startPlayback(item, mediaSourceId, generation, signal, options = {}) {
        const info = await requestPlaybackInfo(item, mediaSourceId, signal, options);
        if (generation !== playbackGeneration) return;
        playSrc.value = info.MediaSources?.find(s => s.Id === mediaSourceId) || info.MediaSources?.[0];
        playMediaSourceId.value = playSrc.value?.Id;
        if (!playSrc.value) throw new Error('No media sources');
        playAudioIndex.value = options.audioIndex >= 0 ? options.audioIndex : defaultAudio(playSrc.value);

        const instance = createPlaybackInstance(item, info, generation);
        const container = (playSrc.value.Container || 'mp4').split(',')[0].trim() || 'mp4';
        const streamPath = playSrc.value.DirectStreamUrl
          || `/Videos/${item.Id}/stream.${container}?Static=true&MediaSourceId=${encodeURIComponent(playSrc.value.Id)}`;
        if (options.forceTranscode && !playSrc.value.TranscodingUrl) throw new Error('上游未提供可切换音轨的兼容播放流');
        await instance.load(options.forceTranscode ? apiRaw(playSrc.value.TranscodingUrl) : apiRaw(streamPath), playSrc.value, item, {
          fallbackUrl: playSrc.value.TranscodingUrl ? apiRaw(playSrc.value.TranscodingUrl) : null,
          audioIndex: playAudioIndex.value, subtitleIndex: options.subtitleIndex,
        });
        if (player !== instance || generation !== playbackGeneration) return;
        await syncStartupSubtitle(instance, generation);
        const seconds = options.startSeconds ?? (item.UserData?.PlaybackPositionTicks || 0) / 10000000;
        if (seconds > 0 && seconds < instance.duration) await instance.seek(seconds);
        if (options.paused) await instance.pause();
        instance.setVolume(playerVolume.value);
        await api('/Sessions/Playing', { method: 'POST', body: JSON.stringify({ ItemId: item.Id,
          MediaSourceId: playSrc.value.Id, PlaySessionId: instance.sessionId,
          PositionTicks: instance.positionTicks, IsPaused: !instance.playing }), signal }).catch(() => {});
        if (player === instance && generation === playbackGeneration) {
          playerUI.end();
          playerUI.acceptState({ playing: instance.playing, loading: false });
        }
        refreshIcons();
      }

      function stopPlayer() {
        playbackGeneration++;
        playbackRequest?.abort(); playbackRequest = null;
        if (seekCommitTimer != null) { clearTimeout(seekCommitTimer); seekCommitTimer = null; }
        if (seekInFlightTimer != null) { clearTimeout(seekInFlightTimer); seekInFlightTimer = null; }
        seekTarget = null; seekInFlight = null; seekPreview.value = null;
        engineSeekSerial++; engineSeekBusy = false; engineSeekQueued = null;
        playerMessage.value = '';
        playerAlive.value = false;
        playerUI.closeMenu();
        if (!player) return;
        api('/Sessions/Playing/Stopped', {
          method: 'POST',
          body: JSON.stringify({ ItemId: playerItemId, MediaSourceId: player.mediaSource?.Id,
            PlaySessionId: player.sessionId, PositionTicks: player.positionTicks }),
        }).catch(() => {});
        player.destroy();
        player = null; playerItemId = null; playSessionId = null;
      }

      function exitPlayer() {
        stopPlayer();
        playerUI.reset();
        playerUI.detach();
        window.location.hash = playerReturnRoute.startsWith('#/play/')
          ? '#/item/' + (playingItem.value?.SeriesId || playingItem.value?.Id) : playerReturnRoute;
      }

      function retryPlayback() {
        const context = playerUI.error?.retryContext;
        const item = context?.item || playingItem.value;
        if (!item) return;
        playerUI.reset();
        playItem(item, context?.mediaSourceId || null, {
          startSeconds: context?.startSeconds, paused: context?.paused,
          audioIndex: context?.audioIndex, subtitleIndex: context?.subtitleIndex,
          forceTranscode: context?.forceTranscode,
        });
      }

      function selectErrorSource(id) {
        const context = playerUI.error?.retryContext;
        if (context) context.mediaSourceId = id;
      }

      /* ---- track / version switching ---- */
      async function switchVersion(id) {
        if (!player || id === playMediaSourceId.value) return;
        if (playerUI.busy || trackBusy.value) return;
        const instance = player;
        trackBusy.value = true;
        playerUI.begin('version');
        try {
          await playItem(playingItem.value, id, { startSeconds: instance.positionTicks / 10000000, paused: !instance.playing });
        } catch (e) {
          toasts.push({ message: '版本切换失败：' + safeError(e), kind: 'error' });
        } finally {
          trackBusy.value = false;
        }
      }

      async function selectAudio(index) {
        if (!player || index === playAudioIndex.value) return;
        if (playerUI.busy || trackBusy.value) return;
        const instance = player;
        trackBusy.value = true;
        try {
          let switched = false;
          try { switched = await instance.selectAudio(index); } catch (e) { switched = false; }
          if (switched) { playAudioIndex.value = index; playerUI.end(); return; }
          playerUI.begin('audio');
          await playItem(playingItem.value, playMediaSourceId.value, { audioIndex: index,
            subtitleIndex: wasmSubTrack.value, startSeconds: instance.positionTicks / 10000000,
            paused: !instance.playing, forceTranscode: !!instance.core?.isHls?.() });
        } catch (e) {
          toasts.push({ message: '音轨切换失败：' + safeError(e), kind: 'error' });
        } finally {
          trackBusy.value = false;
        }
      }

      /* ---- subtitles ---- */
      function handleSubtitleError(instance, generation, message) {
        if (player !== instance || generation !== playbackGeneration) return;
        if (subtitleAttempt && subtitleAttempt.instance === instance && subtitleAttempt.generation === generation) {
          subtitleAttempt.failed = true; subtitleAttempt.message = message;
        } else {
          // No user attempt: this is the startup default-subtitle selection.
          startupSubtitleError = { instance, message };
        }
      }

      async function attemptSubtitleSelection(instance, index, generation) {
        const attempt = { instance, generation, serial: ++subtitleSwitchSerial, failed: false, message: '' };
        subtitleAttempt = attempt;
        try {
          await instance.selectSubtitle(index);
        } catch (e) {
          if (attempt.instance === instance && attempt.generation === generation) { attempt.failed = true; attempt.message = safeError(e); }
        }
        if (subtitleAttempt === attempt) subtitleAttempt = null;
        const ok = !attempt.failed && instance === player && generation === playbackGeneration;
        return { ok, message: attempt.message };
      }

      async function selectSubtitle(idx) {
        const instance = player;
        if (!instance || !playSrc.value || subtitleBusy.value) return;
        const oldIndex = wasmSubTrack.value;
        if (idx === oldIndex) return;
        subtitleBusy.value = true;
        const generation = playbackGeneration;
        try {
          const result = await attemptSubtitleSelection(instance, idx, generation);
          if (instance !== player || generation !== playbackGeneration) return;
          if (result.ok) {
            wasmSubTrack.value = idx;
            if (idx >= 0) lastSubtitleIndex = idx;
            return;
          }
          // Failed: restore the previously confirmed track, else turn subtitles off.
          const restore = await attemptSubtitleSelection(instance, oldIndex, generation);
          if (instance !== player || generation !== playbackGeneration) return;
          if (restore.ok) { wasmSubTrack.value = oldIndex; return; }
          const closed = await attemptSubtitleSelection(instance, -1, generation);
          if (instance !== player || generation !== playbackGeneration) return;
          wasmSubTrack.value = -1;
          toasts.push({ message: closed.ok ? '字幕切换失败，已关闭字幕' : '字幕切换失败', kind: 'error' });
        } finally {
          if (generation === playbackGeneration) subtitleBusy.value = false;
          refreshIcons();
        }
      }

      function toggleSubtitleKey() {
        if (!subtitleTracks.value.length) return;
        if (wasmSubTrack.value >= 0) { lastSubtitleIndex = wasmSubTrack.value; selectSubtitle(-1); }
        else if (lastSubtitleIndex >= 0) selectSubtitle(lastSubtitleIndex);
      }

      /* ---- progress / volume / shortcuts ---- */
      function previewSeek(value) { seekPreview.value = Number(value); }

      // The browser fires pointerup before change on a range slider. Clearing the
      // preview on pointerup would let Vue rewrite the input value to the stale
      // engine time before change reads it, snapping the thumb back to the
      // pre-drag position. Commit from the authoritative preview instead; the
      // change event that follows re-commits the same value harmlessly.
      function endScrub() {
        if (seekPreview.value == null) return;
        commitScrubValue(seekPreview.value);
      }

      function cancelScrub() { seekPreview.value = null; }

      function onWasmSeek(value) {
        const seconds = seekPreview.value != null ? seekPreview.value : parseFloat(value);
        commitScrubValue(seconds);
      }

      function commitScrubValue(seconds) {
        if (!Number.isFinite(seconds)) return;
        seekPreview.value = null;
        wasmTime.value = seconds;
        commitSeek(seconds);
      }

      function commitSeek(seconds) {
        seekTarget = seconds;
        if (seekCommitTimer != null) return;
        seekCommitTimer = setTimeout(() => {
          seekCommitTimer = null;
          const target = seekTarget; seekTarget = null;
          if (player) runEngineSeek(target);
        }, 0);
      }

      // Seeks are serialized against the engine: while core.seek() runs, the
      // engine rejects further seeks ("player is seeking now"), so a newer
      // target is queued and applied when the in-flight seek settles. This
      // keeps a quick second drag from being silently dropped.
      let engineSeekBusy = false;
      let engineSeekQueued = null;
      let engineSeekSerial = 0;

      async function runEngineSeek(target) {
        if (!player) return;
        if (engineSeekBusy) { engineSeekQueued = target; seekInFlight = target; return; }
        const instance = player;
        const serial = ++engineSeekSerial;
        const current = () => player === instance && engineSeekSerial === serial;
        engineSeekBusy = true;
        seekInFlight = target;
        if (seekInFlightTimer != null) clearTimeout(seekInFlightTimer);
        seekInFlightTimer = setTimeout(() => { if (current()) { seekInFlight = null; seekInFlightTimer = null; } }, 8000);
        try {
          await instance.seek(target);
        } catch (e) {
          if (current() && e?.name !== 'AbortError') toasts.push({ message: '跳转失败：' + safeError(e), kind: 'error' });
        } finally {
          if (!current()) return;
          const queued = engineSeekQueued; engineSeekQueued = null;
          engineSeekBusy = false;
          if (queued == null) {
            if (seekInFlightTimer != null) { clearTimeout(seekInFlightTimer); seekInFlightTimer = null; }
            seekInFlight = null;
          } else {
            if (queued !== target) runEngineSeek(queued);
            else {
              if (seekInFlightTimer != null) clearTimeout(seekInFlightTimer);
              seekInFlightTimer = null; seekInFlight = null;
            }
          }
        }
      }

      function seekBy(delta) {
        if (!player) return;
        const duration = wasmDuration.value;
        if (!duration || !Number.isFinite(duration) || duration <= 0) return;
        const base = seekPreview.value ?? wasmTime.value;
        const target = Math.max(0, Math.min(duration, base + Number(delta)));
        seekPreview.value = null;
        wasmTime.value = target;
        commitSeek(target);
      }

      function changeVolume(value) {
        const volume = Math.max(0, Math.min(1, Number(value) || 0));
        playerVolume.value = volume;
        if (volume > 0) lastNonZeroVolume.value = volume;
        player?.setVolume(volume);
      }
      function toggleMute() {
        if (playerVolume.value > 0) { lastNonZeroVolume.value = playerVolume.value; changeVolume(0); }
        else changeVolume(lastNonZeroVolume.value || 1);
      }
      function toggleFullscreen() {
        const result = document.fullscreenElement ? document.exitFullscreen() : playerContainer.value?.requestFullscreen();
        result?.catch(e => toasts.push({ message: '无法进入全屏：' + safeError(e), kind: 'error' }));
      }
      function wasmTogglePause() {
        if (!player) return;
        Promise.resolve(player.playing ? player.pause() : player.play())
          .catch(e => toasts.push({ message: '播放失败：' + safeError(e), kind: 'error' }));
      }

      /* ---- lifecycle ---- */
      // lucide only converts <i data-lucide> elements that exist when
      // createIcons() runs. refreshIcons re-runs after each DOM update and must
      // stay explicit — never a MutationObserver (that build loops forever).
      function refreshIcons() {
        nextTick(() => { try { lucide.createIcons(); } catch (e) {} });
      }

      function leavePagesToLogin() {
        home.cancel(); search.cancel();
        library.cancel(); itemRequest?.abort(); collectionRequest?.abort();
        stopPlayer(); playerUI.reset(); playerUI.detach();
        page.value = 'login';
      }

      function syncFromHash() {
        const { r, param } = readHash();
        currentRoute.value = r; routeParam.value = param;
        if (r === '' || r === 'login') { leavePagesToLogin(); return; }
        if (!localStorage.getItem('eio_token')) { leavePagesToLogin(); return; }
        const target = routeMap[r];
        if (!target) return;
        if (target !== 'home') home.cancel();
        if (target !== 'search') search.cancel();
        if (target !== 'library') library.cancel();
        if (target !== 'item') itemRequest?.abort();
        if (target !== 'resume' && target !== 'favorites') collectionRequest?.abort();
        if (navPage.value === 'player' && target !== 'player') { stopPlayer(); playerUI.reset(); playerUI.detach(); }
        page.value = 'app';
        navPage.value = target;
        refreshIcons();
        if (r === 'home') loadHome();
        else if (r === 'resume' || r === 'favorites') loadCollection(r);
        else if (r === 'library') loadLibraryPage(param);
        else if (r === 'item') loadItem(param);
        else if (r === 'search') search.schedule();
        else if (r === 'play') startPlayerFromRoute(param);
      }

      function onScroll() { libraryScrolled.value = window.scrollY > 8; }

      onMounted(async () => {
        window.addEventListener('hashchange', syncFromHash);
        window.addEventListener('scroll', onScroll, { passive: true });
        lucide.createIcons();
        if (localStorage.getItem('eio_token')) {
          try {
            const me = await api(`/Users/${localStorage.getItem('eio_user_id')}`);
            // An admin token must not keep a web session alive: /admin is the
            // only supported admin entry point.
            if (me.Policy?.IsAdministrator) {
              localStorage.removeItem('eio_token');
              localStorage.removeItem('eio_user_id');
              loginError.value = '管理员账号请使用 /admin 管理面板登录。';
              page.value = 'login';
              return;
            }
            currentUser.value = me;
            recentSearches.read();
            const hadHash = !!window.location.hash;
            window.location.hash = window.location.hash || '#/home';
            if (hadHash) syncFromHash();
          } catch (e) { doLogout(); }
        } else { syncFromHash(); }
      });

      onBeforeUnmount(() => {
        window.removeEventListener('hashchange', syncFromHash);
        window.removeEventListener('scroll', onScroll);
        home.dispose(); search.dispose(); hero.dispose(); toasts.dispose();
        libraryView.dispose(); library.dispose(); playerUI.dispose();
        itemRequest?.abort(); collectionRequest?.abort();
        stopPlayer();
      });

      return {
        page, navPage, loginForm, loginError, loginLoading, loginShake, passwordVisible, currentUser, passWrap,
        mobileMenu, pages, sidebarPages, togglePassword, navigateTo, doLogin, doLogout, focusPass, libraryIcon, openItem, openLibrary,
        toasts, home, hero, libraries, latestSections, loadHome,
        collectionItems, collectionLoading, collectionError, loadCollection,
        libraryName: library.name, libraryItems: library.items, libraryLoading: library.loading,
        libraryPageLoading: library.pageLoading, libraryTotal: library.total, libraryRetryAppend: library.retryAppend,
        libFilter: library.filter, libSort: library.sort, libGenres: library.genres, libraryHasMore: library.hasMore,
        libraryError: library.error, libraryPageError: library.pageError, libraryCountText: library.countText,
        libraryScrolled, libraryView, loadLibrary: loadLibraryPage, loadLibraryMore: library.loadMore,
        retryLibraryPage: library.retryPage, retryLibrary: library.retry,
        setLibraryFilter: library.setFilter, setLibrarySort: library.setSort, clearLibraryFilter: library.clearFilter,
        itemDetail, itemDetailLoading, itemError, retryItem, loadItem, seriesSeasons, seasonEpisodes, seasonErrors, seasonLoading,
        seriesLoading, seriesError, seasonLoadBusy, retrySeason, retrySeries, detailView,
        currentMediaSourceId, chooseDetailSource, detailAudioIndex, detailSubtitleIndex, playDetail, playButtonLabel,
        itemDetailIs4K, itemDetailIsHDR, itemDetailIsDV, itemDetailOfficialRating, runtimeMin, runtimeMin_, epProgress,
        latestErrors,
        currentMediaStreams, videoStreams, subtitleStreams, audioStreams, currentVideoStream, mediaSourceTags, sourceRate,
        isAtmos, audioTrackLabel, genreList, toggleFavorite, favoriteBusyId, isFavorite,
        searchQuery, searchResults, searchLoading, search, recentSearches,
        onSearchInput, submitSearch, useRecent, removeRecent, recordRecent,
        imageUrl, backdropUrl, heroImageUrl,
        playerContainer, playerUI, playerAlive, playerMode, playItem, playItemId, playSrc, playingItem, playVersions, playMediaSourceId,
        playAudioIndex, audioTracks, subtitleTracks, versionOptions, audioOptions, subtitleOptions,
        previousEpisode, nextEpisode, queueError, retryEpisodeQueue, trackBusy, subtitleBusy, switchVersion, selectAudio, selectSubtitle,
        wasmSubTrack, seekPreview, seekTitle, seekPercent, playerVolume, previewSeek, changeVolume, toggleMute, toggleFullscreen,
        wasmTime, wasmDuration, wasmTimeHuman, wasmDurationHuman, wasmPlaying, playerLoadingText,
        exitPlayer, retryPlayback, selectErrorSource, wasmTogglePause, onWasmSeek, endScrub, cancelScrub,
      };
    },
  });

  EIOComponents.register(app);
  app.mount('#app');
})();
