/* Emby-In-One Web Player — single-page application
 *
 * Vue 3 global build, no build step. Loads the shared vendor/ scripts that the
 * admin panel already ships plus ffmpeg.wasm and JASSUB from vendor/web/.
 * Player logic is in player.js (window.EIOPlayer).
 */
(function () {
  const { createApp, ref, computed, onMounted, onBeforeUnmount, nextTick, watch } = Vue;

  const API = ''; // same origin

  /* ================= helpers ================= */

  async function api(path, opts = {}) {
    const token = localStorage.getItem('eio_token');
    const userId = localStorage.getItem('eio_user_id');
    const headers = { 'Content-Type': 'application/json', ...(opts.headers || {}) };
    if (token) headers['Authorization'] = `MediaBrowser Token="${token}", Client="Emby-in-One Web", Device="web", DeviceId="${deviceId}", Version="1.0"`;
    if (userId) headers['X-Emby-User-Id'] = userId;
    const res = await fetch(`${API}${path}`, { ...opts, headers });
    if (!res.ok) {
      const body = await res.text().catch(() => '');
      throw new Error(`${res.status}: ${body || res.statusText}`);
    }
    if (res.status === 204) return null;
    return res.json();
  }

  function apiRaw(path) {
    const token = localStorage.getItem('eio_token');
    const sep = path.includes('?') ? '&' : '?';
    return `${API}${path}${sep}api_key=${encodeURIComponent(token || '')}`;
  }

  const deviceId = (() => {
    let id = localStorage.getItem('eio_device_id');
    if (!id) { id = crypto.randomUUID ? crypto.randomUUID() : Math.random().toString(36).slice(2); localStorage.setItem('eio_device_id', id); }
    return id;
  })();

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
      const routeMap = { '': 'login', home: 'home', library: 'library', item: 'item', search: 'search', play: 'player' };
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
        if (r === '' || r === 'login') { page.value = 'login'; return; }
        const target = routeMap[r];
        if (!target) return;
        // Leaving the player page must stop playback: the video element keeps
        // decoding (and the audio graph keeps running) otherwise.
        if (navPage.value === 'player' && target !== 'player') stopPlayer();
        page.value = 'app';
        navPage.value = target;
        if (r === 'home') loadHome();
        else if (r === 'library') openLibrary(param);
        else if (r === 'item') openItem(param);
        else if (r === 'play') startPlayerFromRoute(param);
      }

      /* ---- navigation ---- */
      const pages = [
        { id: 'home', label: '首页', icon: 'home' },
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
          const res = await api('/Users/AuthenticateByName', {
            method: 'POST',
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
        localStorage.removeItem('eio_token'); localStorage.removeItem('eio_user_id');
        window.location.hash = '#/';
        location.reload();
      }

      function focusPass() { passWrap.value?.querySelector('input')?.focus(); }

      /* ---- home ---- */
      const resumeItems = ref([]);
      const libraries = ref([]);
      const latestSections = ref([]);

      async function loadHome() {
        try {
          const views = await api(`/Users/${localStorage.getItem('eio_user_id')}/Views`);
          libraries.value = views.Items || [];
          const resume = await api(`/Users/${localStorage.getItem('eio_user_id')}/Items/Resume?Limit=12&Fields=PrimaryImageAspectRatio,MediaSources`);
          resumeItems.value = resume.Items || [];
          const secs = [];
          for (const lib of libraries.value.slice(0, 3)) {
            const latest = await api(`/Users/${localStorage.getItem('eio_user_id')}/Items/Latest?Limit=12&ParentId=${lib.Id}&Fields=PrimaryImageAspectRatio`);
            if (latest.Items?.length) secs.push({ key: lib.Id, title: lib.Name, items: latest.Items });
          }
          latestSections.value = secs;
        } catch (e) { console.error('loadHome', e); }
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

      async function openLibrary(id) {
        navPage.value = 'library';
        libraryLoading.value = true;
        libOffset = 0; libraryItems.value = [];
        try {
          const lib = await api(`/Users/${localStorage.getItem('eio_user_id')}/Items/${id}`);
          libraryName.value = lib.Name;
          const genres = await api(`/Users/${localStorage.getItem('eio_user_id')}/Genres?ParentId=${id}&Limit=50`);
          libGenres.value = (genres.Items || []).map(g => g.Name);
          await loadLibraryItems();
        } catch (e) { console.error(e); }
        libraryLoading.value = false;
      }

      async function loadLibraryItems() {
        const parentId = routeParam.value;
        const [sortField, sortDir] = libSort.value.split(',');
        const items = await api(`/Users/${localStorage.getItem('eio_user_id')}/Items?ParentId=${parentId}&Limit=${libLimit.value}&StartIndex=${libOffset}&SortBy=${sortField}&SortOrder=${sortDir}&Fields=PrimaryImageAspectRatio,ProductionYear&Recursive=true`);
        libraryItems.value = items.Items || [];
      }

      async function loadLibraryMore() { libOffset += libLimit.value; await loadLibraryItems(); }

      watch([libFilter, libSort], () => { libOffset = 0; loadLibraryItems(); });

      /* ---- item detail ---- */
      const itemDetail = ref(null);
      const itemDetailLoading = ref(false);
      const seriesSeasons = ref([]);
      const seasonEpisodes = ref({});
      const currentMediaSourceId = ref(null);

      async function openItem(id) {
        navPage.value = 'item'; itemDetailLoading.value = true;
        itemDetail.value = null; seriesSeasons.value = []; seasonEpisodes.value = {};
        try {
          const detail = await api(`/Users/${localStorage.getItem('eio_user_id')}/Items/${id}?Fields=MediaSources,Overview,Genres,ProductionYear,RunTimeTicks,OfficialRating,CommunityRating,BackdropImageTags,SeriesId,SeasonId,UserData`);
          itemDetail.value = detail;
          currentMediaSourceId.value = detail.MediaSources?.[0]?.Id || null;
          if (detail.Type === 'Series') {
            const seasons = await api(`/Users/${localStorage.getItem('eio_user_id')}/Shows/${id}/Seasons`);
            seriesSeasons.value = seasons.Items || [];
            for (const s of seriesSeasons.value) {
              const eps = await api(`/Users/${localStorage.getItem('eio_user_id')}/Shows/${id}/Episodes?SeasonId=${s.Id}&Fields=PrimaryImageAspectRatio,RunTimeTicks`);
              seasonEpisodes.value[s.Id] = eps.Items || [];
            }
          }
        } catch (e) { console.error(e); }
        itemDetailLoading.value = false;
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
          if (itemDetail.value?.Id === item.Id) { itemDetail.value.UserData.IsFavorite = !remove; }
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
            const res = await api(`/Users/${localStorage.getItem('eio_user_id')}/Items?SearchTerm=${encodeURIComponent(searchQuery.value)}&Limit=30&Recursive=true&Fields=PrimaryImageAspectRatio,ProductionYear,Overview`);
            searchResults.value = res.Items || [];
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

      let player = null;
      let playSessionId = null;

      const subtitleTracks = computed(() => {
        if (!playSrc.value) return [];
        return playSrc.value.MediaStreams?.filter(s => s.Type === 'Subtitle') || [];
      });

      // pendingMediaSourceId carries a version (MediaSource) choice from
      // playItem() into startPlayback(), which the route handler cannot know.
      let pendingMediaSourceId = null;
      // playerItemId records what the live player instance is playing so the
      // route handler can tell "already playing this" from "switching items".
      let playerItemId = null;

      async function playItem(item, mediaSourceId) {
        playItemId.value = item.Id;
        playingItem.value = item;
        pendingMediaSourceId = mediaSourceId || null;
        const target = '#/play/' + item.Id;
        if (window.location.hash === target) {
          // Same route: no hashchange will fire, so drive playback directly.
          await startPlayerFromRoute(item.Id, true);
        } else {
          window.location.hash = target;
        }
      }

      // startPlayerFromRoute is the single entry point that begins playback.
      // Deep links (#/play/{id} typed or reloaded) and in-app navigation both
      // land here; playItem() only records the intent and lets the router call
      // this, which keeps "start playback" from running twice.
      async function startPlayerFromRoute(id, force) {
        if (!id || !localStorage.getItem('eio_token')) return;
        if (!force && playItemId.value === id && playerItemId === id) return;
        try {
          let item = playingItem.value && playingItem.value.Id === id ? playingItem.value : null;
          if (!item) {
            item = await api(`/Users/${localStorage.getItem('eio_user_id')}/Items/${id}?Fields=MediaSources,Overview,RunTimeTicks,UserData`);
            playingItem.value = item;
          }
          playItemId.value = id;
          await nextTick(); // the player container only exists once navPage renders
          await startPlayback(item, pendingMediaSourceId);
        } catch (e) {
          console.error('startPlayerFromRoute', e);
          alert('无法加载播放内容：' + e.message);
        } finally {
          pendingMediaSourceId = null;
        }
      }

      async function startPlayback(item, mediaSourceId) {
        try {
          const info = await api(`/Items/${item.Id}/PlaybackInfo`, {
            method: 'POST',
            body: JSON.stringify({ UserId: localStorage.getItem('eio_user_id'), MediaSourceId: mediaSourceId || item.MediaSources?.[0]?.Id }),
          });
          playSrc.value = info.MediaSources?.[0];
          playMediaSourceId.value = mediaSourceId || item.MediaSources?.[0]?.Id || info.MediaSources?.[0]?.Id;
          if (!playSrc.value) throw new Error('No media sources');

          // Report playback start
          await api('/Sessions/Playing', { method: 'POST', body: JSON.stringify({ ItemId: item.Id, PlaySessionId: info.PlaySessionId }) }).catch(() => {});
          playSessionId = info.PlaySessionId;

          // Create player
          stopPlayer();
          player = new EIOPlayer(playerContainer.value);
          playerItemId = item.Id;
          player.onProgress = (ticks) => {
            api('/Sessions/Playing/Progress', {
              method: 'POST',
              body: JSON.stringify({ ItemId: item.Id, PlaySessionId: playSessionId, PositionTicks: ticks }),
            }).catch(() => {});
          };
          player.onEnded = () => {
            api('/Sessions/Playing/Stopped', {
              method: 'POST',
              body: JSON.stringify({ ItemId: item.Id, PlaySessionId: playSessionId, PositionTicks: player.positionTicks }),
            }).catch(() => {});
            exitPlayer();
          };
          player.onError = (msg) => {
            alert('播放错误：' + msg);
            exitPlayer();
          };

          const url = apiRaw(playSrc.value.DirectStreamUrl);
          await player.load(url, playSrc.value, item);
          playerMode.value = player.mode;
          wasmDuration.value = player.duration;
        } catch (e) { console.error('startPlayback', e); alert('播放失败：' + e.message); }
      }

      // stopPlayer tears the player down and reports the stop upstream. It is
      // separate from exitPlayer because leaving the player route must stop
      // playback without necessarily navigating.
      function stopPlayer() {
        if (!player) return;
        api('/Sessions/Playing/Stopped', {
          method: 'POST',
          body: JSON.stringify({ ItemId: playItemId.value, PlaySessionId: playSessionId, PositionTicks: player.positionTicks }),
        }).catch(() => {});
        player.destroy();
        player = null;
        playerItemId = null;
      }

      function exitPlayer() {
        stopPlayer();
        window.history.back();
      }

      function selectSubtitle(idx) {
        wasmSubTrack.value = idx;
        if (player && playSrc.value) {
          player.selectSubtitle(idx, playSrc.value);
        }
      }

      function wasmTogglePause() {
        if (player) {
          if (player.playing) player.pause();
          else player.play();
          wasmPlaying.value = player.playing;
        }
      }

      function onWasmSeek(v) {
        const seconds = parseFloat(v);
        wasmTime.value = seconds;
        if (player) player.seek(seconds);
      }

      /* ---- lifecycle ---- */
      onMounted(async () => {
        window.addEventListener('hashchange', syncFromHash);
        lucide.createIcons();
        if (localStorage.getItem('eio_token')) {
          try {
            const me = await api(`/Users/${localStorage.getItem('eio_user_id')}`);
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
        stopPlayer();
      });

      return {
        page, navPage, loginForm, loginError, loginLoading, currentUser, passWrap, mobileMenu, pages,
        navigateTo, doLogin, doLogout, focusPass, libraryIcon,
        resumeItems, libraries, latestSections, loadHome,
        libraryName, libraryItems, libraryLoading, libFilter, libSort, libGenres, libLimit,
        openLibrary, loadLibraryItems, loadLibraryMore,
        itemDetail, itemDetailLoading, seriesSeasons, seasonEpisodes, openItem,
        itemDetailIs4K, itemDetailIsHDR, itemDetailIsDV, itemDetailOfficialRating, runtimeMin, runtimeMin_,
        currentMediaStreams, subtitleStreams, audioStreams, currentVideoStream, mediaSourceTags,
        toggleFavorite, playItem,
        searchQuery, searchResults, searchLoading, debouncedSearch,
        imageUrl, backdropUrl,
        playerContainer, playerMode, playItemId, playSrc,
        wasmLoading, wasmLoadingMsg, wasmPlaying, wasmTime, wasmDuration, wasmTimeHuman, wasmDurationHuman,
        wasmSubTrack, showSubMenu, hdrShaderActive, subtitleTracks,
        exitPlayer, wasmTogglePause, onWasmSeek, selectSubtitle,
        startPlayback,
      };
    },
  }).mount('#app');
})();
