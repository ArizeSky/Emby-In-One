/* Emby-In-One Web Player — single-page application (V1.4.4-rc2)
 *
 * Vue 3 global build, no build step. Loads the shared vendor/ scripts plus
 * libmedia and JASSUB. Player engine: player-libmedia.js (window.EIOPlayer).
 * Playback session (player region): playback-session.js
 * (window.EIOPlaybackSession) — injected and destructured below.
 * Presentation state: ui.js / page-state.js / player-ui.js / ui-components.js.
 */
(function () {
  const { createApp, ref, reactive, computed, onMounted, onBeforeUnmount, nextTick } = Vue;

  const API = ''; // same origin
  const { query, all: allItems, episodeOrder, defaultAudio, defaultSubtitle } = EIOData;
  const {
    isPartialResults, createPendingSelection, createEpisodeQueue,
    compatibleStream, createCleanupGate,
  } = EIOPlayback;
  const { resumeCardView } = window.EIOResumeCard;
  // Set by setup() so a partial-results response can surface a lightweight toast
  // without api() needing the reactive toast store directly (plan §8.9).
  let notifyPartialResults = null;


  // Server-side playback feature flags (/System/PlaybackFeatures). The
  // dual-cache flag is opt-in and absent means off; a fetch failure keeps
  // baseline playback and never blocks the player.
  const playbackFeatures = { dualCache: false, dualCacheBudget: null };
  // Fallback budget only for older backends that do not send
  // dualCacheBudget; the server-controlled values are the primary source.
  // These mirror the Go-side constants in handlers_system.go and satisfy the
  // vendor rules (ioBytes+demuxBytes < maxBytes, maxPacketBytes < demuxBytes;
  // mseBytes is derived by the vendor as maxBytes-ioBytes-demuxBytes).
  const DEFAULT_DUAL_CACHE_BUDGET = {
    maxBytes: 300000000,
    ioBytes: 12000000,
    demuxBytes: 48000000,
    maxPacketBytes: 16000000,
    maxForwardSeconds: 120,
  };
  // Resolve the server-delivered budget with per-field fallback so a partial
  // or missing payload never disables the strict-admission limits.
  function dualCacheBudget() {
    const b = playbackFeatures.dualCacheBudget;
    const num = (v, d) => (Number.isFinite(Number(v)) && Number(v) > 0 ? Number(v) : d);
    return {
      maxBytes: num(b?.maxBytes, DEFAULT_DUAL_CACHE_BUDGET.maxBytes),
      ioBytes: num(b?.ioBytes, DEFAULT_DUAL_CACHE_BUDGET.ioBytes),
      demuxBytes: num(b?.demuxBytes, DEFAULT_DUAL_CACHE_BUDGET.demuxBytes),
      maxPacketBytes: num(b?.maxPacketBytes, DEFAULT_DUAL_CACHE_BUDGET.maxPacketBytes),
      maxForwardSeconds: num(b?.maxForwardSeconds, DEFAULT_DUAL_CACHE_BUDGET.maxForwardSeconds),
    };
  }
  async function refreshPlaybackFeatures() {
    try {
      const res = await fetch(`${API}/System/PlaybackFeatures`, { headers: { 'X-Emby-Token': localStorage.getItem('eio_token') || '' } });
      if (!res.ok) return;
      const data = await res.json();
      playbackFeatures.dualCache = data?.dualCache === true;
      playbackFeatures.dualCacheBudget = data?.dualCacheBudget ?? null;
    } catch (e) { /* keep baseline */ }
  }
  // A startup resume can race a cold/transcoding engine and is not an
  // interactive drag. It gets its own bounded budget; this stays an internal
  // constant instead of a window/external setting.
  const STARTUP_SEEK_TIMEOUT_MS = 30000;

  /* ================= helpers ================= */

  async function api(path, opts = {}) {
    const token = localStorage.getItem('eio_token');
    // Anonymous theme-site reads deliberately carry no token; every other
    // request authenticates with X-Emby-Token only. The web client sends no
    // client identity: on passthrough upstreams those headers would win the
    // identity resolution over the captured real-client identity.
    const { anonymous, ...requestOptions } = opts;
    const headers = { 'Content-Type': 'application/json', ...(requestOptions.headers || {}) };
    if (token && !anonymous) headers['X-Emby-Token'] = token;
    const res = await fetch(`${API}${path}`, { ...requestOptions, headers });
    if (!res.ok) {
      const body = await res.text().catch(() => '');
      let detail = body || res.statusText;
      let hasMessage = false;
      try { const parsed = JSON.parse(body); if (parsed && parsed.message) { detail = parsed.message; hasMessage = true; } } catch (e) {}
      // A bare 403/502 (no upstream message) gets a clear local explanation so a
      // parental restriction is not shown as a generic network failure (plan §8.9).
      if (!hasMessage) {
        if (res.status === 403) detail = '内容受限，无法访问该内容';
        else if (res.status === 502) detail = '暂时无法确认内容，请稍后重试';
      }
      const error = new Error(`${res.status}: ${detail}`);
      error.status = res.status;
      throw error;
    }
    if (res.status === 204) return null;
    const data = await res.json();
    // A partial merged candidate set is still usable; surface it lightly rather
    // than rendering the failure as an empty library (plan §8.9).
    if (data && data.EioIncompleteResults && typeof notifyPartialResults === 'function') {
      notifyPartialResults();
    }
    return data;
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
      /* ---- appearance (T1 controller, T4 transport) ---- */
      // The transport owns only the /web theme endpoints and the immutable
      // stylesheet link. It receives the app's existing token/identity access
      // and 401 hook instead of reading localStorage itself, and the T1
      // controller stays the only preference/generation authority.
      const themeTransport = typeof window !== 'undefined' && window.EIOThemeTransport
        && typeof window.EIOThemeTransport.create === 'function'
        ? window.EIOThemeTransport.create({
          request: (path, options) => api(path, options),
          getToken: () => localStorage.getItem('eio_token'),
          getUserId: () => localStorage.getItem('eio_user_id'),
          document,
          window,
          onUnauthorized: handleThemeUnauthorized,
        })
        : null;
      const themeDeps = themeTransport
        || (typeof window !== 'undefined' && window.EIOThemeDeps)
        || {};
      // The theme module reads window.localStorage through its own guarded
      // getter: a browser that denies site storage must leave setup working
      // with an in-memory appearance instead of throwing before mount.
      const theme = typeof window !== 'undefined' && window.EIOTheme
        && typeof window.EIOTheme.createController === 'function'
        ? window.EIOTheme.createController({
          window, document, deps: themeDeps,
        })
        : null;
      const appearance = theme && window.EIOAppearance
        && typeof window.EIOAppearance.createController === 'function'
        ? window.EIOAppearance.createController({ theme })
        : null;
      if (theme && themeTransport && typeof themeTransport.attach === 'function') {
        themeTransport.attach(theme, {
          onCssError() {
            toasts.push({ message: '自定义主题样式加载失败，已保留主题配色', kind: 'info' });
          },
        });
      }

      function refreshAppearanceRemote() {
        if (!appearance) return;
        // A short timeout keeps an unavailable theme endpoint from delaying the
        // page; a late response is discarded by the controller generation.
        Promise.resolve(appearance.refreshRemote({ timeoutMs: 3000 })).catch(() => {});
      }

      // The transport already proved this 401 belongs to the current token and
      // account, so a stale response from an old login cannot log out a newer
      // session. The existing logout path stays the only one that clears it.
      function handleThemeUnauthorized() {
        doLogout();
      }

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
      // Surface a single lightweight notice when a list response is partial
      // (plan §8.9); debounce so rapid page loads do not stack toasts.
      let partialNoticeShownAt = 0;
      notifyPartialResults = () => {
        const now = Date.now();
        if (now - partialNoticeShownAt < 15000) return;
        partialNoticeShownAt = now;
        toasts.push({ message: '部分结果：部分服务器响应较慢，列表可能不完整', kind: 'info' });
      };

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
          if (appearance) appearance.enterSession(res.User.Id);
          refreshAppearanceRemote();
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
        // Drop back to the anonymous appearance before clearing the token so a
        // later logout cannot expose the previous account's cached choice.
        if (appearance) appearance.leaveSession();
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
      // A partial (incomplete) list is shown with a non-blocking notice; it is
      // never pretended to be the complete collection.
      const collectionNotice = ref('');
      let collectionRequest = null;

      async function loadCollection(kind) {
        collectionRequest?.abort();
        const request = collectionRequest = new AbortController();
        collectionLoading.value = true; collectionError.value = ''; collectionNotice.value = '';
        collectionItems.value = [];
        const path = `/Users/${localStorage.getItem('eio_user_id')}/Items`;
        try {
          const params = {
            Fields: kind === 'resume'
              ? 'PrimaryImageAspectRatio,RunTimeTicks,UserData,SeriesName,SeriesId,SeasonId,IndexNumber,ParentIndexNumber'
              : 'PrimaryImageAspectRatio,RunTimeTicks,UserData,SeriesName',
            Recursive: true,
          };
          if (kind === 'favorites') { params.Filters = 'IsFavorite'; params.IncludeItemTypes = 'Movie,Series,Episode'; }
          let items;
          try {
            items = await allItems(api, kind === 'resume' ? path + '/Resume' : path, params, { signal: request.signal });
          } catch (e) {
            // A partial candidate set is still usable; show it with a notice
            // instead of an empty page or an exact-looking total (W04 handoff).
            if (!isPartialResults(e)) throw e;
            items = e.items.slice();
            if (collectionRequest === request) collectionNotice.value = '列表可能不完整，可稍后重试补全';
          }
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
          let seasons;
          try {
            seasons = await allItems(api, `/Shows/${detail.Id}/Seasons`, { UserId: uid }, { signal: request.signal });
          } catch (e) {
            if (!isPartialResults(e)) throw e;
            seasons = e.items.slice();
            if (itemRequest === request) seriesError.value = '季列表可能不完整，可重试补全';
          }
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
          let episodes;
          try {
            episodes = await allItems(api, `/Shows/${seriesId}/Episodes`, {
              UserId: uid, SeasonId: seasonId,
              Fields: 'PrimaryImageAspectRatio,RunTimeTicks,SeriesId,SeasonId,UserData',
            }, { signal: request.signal });
          } catch (e) {
            if (!isPartialResults(e)) throw e;
            episodes = e.items.slice();
            // Episodes stay clickable; the notice makes clear the season is
            // not the complete, exact episode list.
            if (itemRequest === request) {
              seasonErrors.value = { ...seasonErrors.value, [seasonId]: '本季剧集可能不完整，可重试补全' };
            }
          }
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
      // The /play lifecycle lives in public/web/playback-session.js. Its
      // factory is pure: the Vue reactive factories, the api helpers, the page
      // refs, the data/orchestrator helpers, the playback feature flags and the
      // toast sink are injected here, and the returned identifiers are
      // destructured back into the names the template and the lifecycle hooks
      // already use.
      const {
        playerContainer, playerUI, playerTelemetry, playerAlive, playerMode, playItemId, playingItem,
        playSrc, playMediaSourceId, wasmPlaying, wasmTime, wasmDuration, wasmTimeHuman, wasmDurationHuman,
        wasmSubTrack, playerMessage, playVersions, playAudioIndex, episodeQueue, episodeQueueLoading,
        episodeQueuePartial, queueError, trackBusy, subtitleBusy, playerVolume, lastNonZeroVolume,
        seekPreview, previousEpisode, nextEpisode, audioTracks, subtitleTracks, versionOptions,
        audioOptions, subtitleOptions, playerLoadingText, seekTitle, seekPercent, rateUI,
        playItem, startPlayerFromRoute, stopPlayer, exitPlayer, retryPlayback, selectErrorSource,
        retryEpisodeQueue, switchVersion, selectAudio, selectSubtitle, previewSeek, endScrub,
        cancelScrub, onWasmSeek, changeVolume, toggleMute, toggleFullscreen, wasmTogglePause,
        pendingSelection,
      } = EIOPlaybackSession.create({
        ref, computed, reactive, nextTick,
        api, apiRaw, safeError,
        allItems, episodeOrder, defaultAudio, audioTrackLabel, fmtTicks,
        isPartialResults, createPendingSelection, createEpisodeQueue,
        compatibleStream, createCleanupGate,
        page, navPage, toasts,
        playbackFeatures, refreshPlaybackFeatures, dualCacheBudget,
        openItem, refreshIcons,
        DETAIL_FIELDS, STARTUP_SEEK_TIMEOUT_MS,
      });

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
        stopPlayer(); playerUI.reset(); playerUI.detach(); pendingSelection.clear();
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
        if (navPage.value === 'player' && target !== 'player') {
          stopPlayer(); playerUI.reset(); playerUI.detach(); pendingSelection.clear();
        }
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
              if (appearance) appearance.leaveSession();
              loginError.value = '管理员账号请使用 /admin 管理面板登录。';
              page.value = 'login';
              refreshAppearanceRemote();
              return;
            }
            currentUser.value = me;
            refreshPlaybackFeatures();
            if (appearance) appearance.syncIdentity(localStorage.getItem('eio_user_id'));
            refreshAppearanceRemote();
            recentSearches.read();
            const hadHash = !!window.location.hash;
            window.location.hash = window.location.hash || '#/home';
            if (hadHash) syncFromHash();
          } catch (e) {
            // Only an explicit 401 ends the session. A transient failure (a
            // restarting server, a network blip, a 502) must not destroy a
            // valid token: the user stays on the login form with the session
            // intact, and a refresh retries the check.
            if (e && e.status === 401) { doLogout(); return; }
            loginError.value = '暂时无法连接服务器，请刷新重试。';
            page.value = 'login';
          }
        } else {
          syncFromHash();
          refreshAppearanceRemote();
        }
      });

      onBeforeUnmount(() => {
        window.removeEventListener('hashchange', syncFromHash);
        window.removeEventListener('scroll', onScroll);
        home.dispose(); search.dispose(); hero.dispose(); toasts.dispose();
        libraryView.dispose(); library.dispose(); playerUI.dispose(); rateUI.dispose();
        itemRequest?.abort(); collectionRequest?.abort();
        stopPlayer();
        if (appearance) appearance.destroy();
        if (themeTransport) themeTransport.destroy();
      });

      return {
        page, navPage, loginForm, loginError, loginLoading, loginShake, passwordVisible, currentUser, passWrap,
        appearance,
        mobileMenu, pages, sidebarPages, togglePassword, navigateTo, doLogin, doLogout, focusPass, libraryIcon, openItem, openLibrary,
        toasts, home, hero, libraries, latestSections, loadHome,
        collectionItems, collectionLoading, collectionError, collectionNotice, loadCollection, resumeCard: resumeCardView,
        libraryName: library.name, libraryItems: library.items, libraryLoading: library.loading,
        libraryPageLoading: library.pageLoading, libraryTotal: library.total, libraryRetryAppend: library.retryAppend,
        libFilter: library.filter, libSort: library.sort, libGenres: library.genres, libraryHasMore: library.hasMore,
        libraryError: library.error, libraryPageError: library.pageError, libraryCountText: library.countText,
        libraryScrolled, libraryView, loadLibrary: loadLibraryPage, loadLibraryMore: library.loadMore,
        retryLibraryPage: library.retryPage, retryLibrary: library.retry,
        // Package E owns the library partial state (library-state.js); package A
        // only re-exports it so the shared template can bind to it without E
        // touching app.js.
        libraryPartial: library.partial,
        libraryPartialNotice: library.partialNotice,
        libraryPartialCanContinue: library.partialCanContinue,
        continueLibraryPartial: (...args) => library.continuePartial?.(...args),
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
        playerContainer, playerUI, playerTelemetry, playerAlive, playerMode,
        rateUI, playbackRate: rateUI.playbackRate, ratePending: rateUI.ratePending, rateError: rateUI.rateError, selectPlaybackRate: rateUI.selectPlaybackRate, playItem, playItemId, playSrc, playingItem, playVersions, playMediaSourceId,
        playAudioIndex, audioTracks, subtitleTracks, versionOptions, audioOptions, subtitleOptions,
        previousEpisode, nextEpisode, queueError, retryEpisodeQueue, episodeQueue, episodeQueueLoading, episodeQueuePartial, trackBusy, subtitleBusy, switchVersion, selectAudio, selectSubtitle,
        wasmSubTrack, seekPreview, seekTitle, seekPercent, playerVolume, previewSeek, changeVolume, toggleMute, toggleFullscreen,
        wasmTime, wasmDuration, wasmTimeHuman, wasmDurationHuman, wasmPlaying, playerLoadingText,
        exitPlayer, retryPlayback, selectErrorSource, wasmTogglePause, onWasmSeek, endScrub, cancelScrub,
      };
    },
  });

  EIOComponents.register(app);
  if (window.EIOHomeLibraryRow && typeof window.EIOHomeLibraryRow.register === 'function') {
    window.EIOHomeLibraryRow.register(app);
  }
  if (window.EIOAppearance && typeof window.EIOAppearance.register === 'function') {
    window.EIOAppearance.register(app);
  }
  app.mount('#app');
})();
