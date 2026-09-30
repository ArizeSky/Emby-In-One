/* Emby-In-One Web Player — single-page application
 *
 * Vue 3 global build, no build step. Loads the shared vendor/ scripts that the
 * admin panel already ships plus libmedia and JASSUB from vendor/web/.
 * Player logic is in player-libmedia.js (window.EIOPlayer).
 */
(function () {
  const { createApp, ref, computed, onMounted, onBeforeUnmount, nextTick, watch } = Vue;

  const API = ''; // same origin

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
        if (r === '' || r === 'login') { stopPlayer(); page.value = 'login'; return; }
        const target = routeMap[r];
        if (!target) return;
        // Leaving the player page must stop playback: the video element keeps
        // decoding (and the audio graph keeps running) otherwise.
        if (navPage.value === 'player' && target !== 'player') stopPlayer();
        page.value = 'app';
        navPage.value = target;
        refreshIcons();
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
        const uid = localStorage.getItem('eio_user_id');
        // Views and Resume are independent; one failing must not blank the page.
        const [viewsRes, resumeRes] = await Promise.allSettled([
          api(`/Users/${uid}/Views`),
          api(`/Users/${uid}/Items/Resume?Limit=12&Fields=PrimaryImageAspectRatio,MediaSources`),
        ]);
        if (viewsRes.status === 'fulfilled') libraries.value = viewsRes.value.Items || [];
        else console.error('loadHome views', viewsRes.reason);
        if (resumeRes.status === 'fulfilled') resumeItems.value = resumeRes.value.Items || [];
        else console.error('loadHome resume', resumeRes.reason);
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

      async function openLibrary(id) {
        navPage.value = 'library';
        libraryLoading.value = true;
        libOffset = 0; libraryItems.value = [];
        try {
          const lib = await api(`/Users/${localStorage.getItem('eio_user_id')}/Items/${id}`);
          libraryName.value = lib.Name;
        } catch (e) { console.error('openLibrary', e); }
        // Genres are decorative chips: the proxy implements /Genres without the
        // /Users prefix, and a failure here must never block the item list (the
        // old /Users/{id}/Genres path 502'd and aborted the whole page).
        try {
          const genres = await api(`/Genres?ParentId=${id}&UserId=${localStorage.getItem('eio_user_id')}&Limit=50`);
          libGenres.value = (genres.Items || []).map(g => g.Name);
        } catch (e) { libGenres.value = []; console.error('genres', e); }
        try { await loadLibraryItems(); } catch (e) { console.error('loadLibraryItems', e); }
        libraryLoading.value = false;
        refreshIcons();
      }

      async function loadLibraryItems() {
        const parentId = routeParam.value;
        const [sortField, sortDir] = libSort.value.split(',');
        const items = await api(`/Users/${localStorage.getItem('eio_user_id')}/Items?ParentId=${parentId}&Limit=${libLimit.value}&StartIndex=${libOffset}&SortBy=${sortField}&SortOrder=${sortDir}&Fields=PrimaryImageAspectRatio,ProductionYear&Recursive=true`);
        libraryItems.value = items.Items || [];
        refreshIcons();
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
            // The proxy implements the standard /Shows/{id}/Seasons path (no
            // /Users prefix); the old prefixed path fell through to the
            // fallback proxy and 502'd, so no season or episode ever loaded.
            const uid = localStorage.getItem('eio_user_id');
            const seasons = await api(`/Shows/${id}/Seasons?UserId=${uid}`);
            seriesSeasons.value = seasons.Items || [];
            await Promise.all(seriesSeasons.value.map(async (s) => {
              const eps = await api(`/Shows/${id}/Episodes?UserId=${uid}&SeasonId=${s.Id}&Fields=PrimaryImageAspectRatio,RunTimeTicks`);
              seasonEpisodes.value[s.Id] = eps.Items || [];
            }));
          }
        } catch (e) { console.error(e); }
        itemDetailLoading.value = false;
        refreshIcons();
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
      let playbackGeneration = 0;
      let playbackRequest = null;

      async function playItem(item, mediaSourceId) {
        // A series has no media sources of its own: PlaybackInfo against a
        // series fails upstream (500, "cannot cast Series to IHasMediaSources").
        // Resolve to the next-up episode, or the first episode when nothing
        // has been watched yet.
        if (item.Type === 'Series') {
          const ep = await resolveSeriesEpisode(item.Id);
          if (!ep) { alert('该剧集没有可播放的集'); return; }
          item = ep;
        }
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
          const seasons = await api(`/Shows/${seriesId}/Seasons?UserId=${uid}`);
          const season = seasons.Items?.[0];
          if (!season) return null;
          const eps = await api(`/Shows/${seriesId}/Episodes?UserId=${uid}&SeasonId=${season.Id}&Fields=PrimaryImageAspectRatio,RunTimeTicks`);
          return eps.Items?.[0] || null;
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
        try {
          let item = playingItem.value && playingItem.value.Id === id ? playingItem.value : null;
          if (!item) {
            item = await api(`/Users/${localStorage.getItem('eio_user_id')}/Items/${id}?Fields=MediaSources,Overview,RunTimeTicks,UserData`, { signal });
            if (generation !== playbackGeneration) return;
            playingItem.value = item;
          }
          playItemId.value = id;
          await nextTick(); // the player container only exists once navPage renders
          if (generation !== playbackGeneration) return;
          await startPlayback(item, sourceId, generation, signal);
        } catch (e) {
          if (e.name === 'AbortError' || generation !== playbackGeneration) return;
          console.error('startPlayerFromRoute', e);
          alert('无法加载播放内容：' + e.message);
        } finally {
          if (generation === playbackGeneration) pendingMediaSourceId = null;
        }
      }

      async function startPlayback(item, mediaSourceId, generation, signal) {
        try {
          // Declare a browser DeviceProfile so the upstream decides between
          // direct stream and transcode. Without it Emby assumes a client
          // that can direct-play everything and returns no usable stream URL
          // (no DirectStreamUrl, no TranscodingUrl) for the web player.
          const deviceProfile = {
            MaxStreamingBitrate: 120000000,
            DirectPlayProfiles: [
              { Container: 'mp4,m4v', Type: 'Video', VideoCodec: 'h264,avc', AudioCodec: 'aac,mp3' },
            ],
            TranscodingProfiles: [
              { Container: 'mp4', Type: 'Video', VideoCodec: 'h264', AudioCodec: 'aac', Protocol: 'hls' },
              { Container: 'ts', Type: 'Video', VideoCodec: 'h264', AudioCodec: 'aac', Protocol: 'hls' },
            ],
            CodecProfiles: [],
            // The fallback is intentionally bounded; original 4K stays on
            // the direct path instead of being software-decoded after HLS.
            MaxStaticBitrate: 120000000,
            SubtitleProfiles: [
              { Format: 'vtt', Method: 'External' },
              { Format: 'ass', Method: 'External' },
            ],
          };
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
            }),
          });
          if (generation !== playbackGeneration) return;
          playSrc.value = info.MediaSources?.find(s => s.Id === mediaSourceId) || info.MediaSources?.[0];
          playMediaSourceId.value = playSrc.value?.Id;
          if (!playSrc.value) throw new Error('No media sources');

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
              body: JSON.stringify({ ItemId: item.Id, PlaySessionId: instance.sessionId, PositionTicks: ticks }),
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
          await instance.load(url, playSrc.value, item, {
            fallbackUrl: playSrc.value.TranscodingUrl ? apiRaw(playSrc.value.TranscodingUrl) : null,
          });
          if (player !== instance || generation !== playbackGeneration) return;
          wasmSubTrack.value = instance.subtitleIndex ?? -1;
          await api('/Sessions/Playing', { method: 'POST', body: JSON.stringify({ ItemId: item.Id,
            MediaSourceId: playSrc.value.Id, PlaySessionId: instance.sessionId }), signal }).catch(() => {});
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
          body: JSON.stringify({ ItemId: playerItemId, PlaySessionId: player.sessionId, PositionTicks: player.positionTicks }),
        }).catch(() => {});
        player.destroy();
        player = null;
        playerItemId = null;
        playSessionId = null;
      }

      function exitPlayer() {
        stopPlayer();
        window.history.back();
      }

      function selectSubtitle(idx) {
        wasmSubTrack.value = idx;
        if (player && playSrc.value) {
          player.selectSubtitle(idx);
        }
      }

      function wasmTogglePause() {
        if (player) {
          Promise.resolve(player.playing ? player.pause() : player.play()).catch(e => alert('播放失败：' + e.message));
        }
      }

      function onWasmSeek(v) {
        const seconds = parseFloat(v);
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
