/* Emby-In-One Web Player — single-page application
 *
 * Vue 3 global build, no build step. Loads the shared vendor/ scripts that the
 * admin panel already ships plus ffmpeg.wasm and JASSUB from vendor/web/.
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

  /* ================= WASM / HDR / PGS helpers ================= */

  const WASM_LIB = { core: null, ready: false, loading: false, loadPromise: null };

  async function ensureFFmpeg() {
    if (WASM_LIB.ready) return WASM_LIB.core;
    if (WASM_LIB.loading) return WASM_LIB.loadPromise;
    WASM_LIB.loading = true;
    WASM_LIB.loadPromise = new Promise((resolve, reject) => {
      const script = document.createElement('script');
      script.src = 'vendor/web/ffmpeg.min.js';
      script.onload = () => {
        if (window.FFmpeg && window.FFmpeg.createFFmpeg) {
          const core = window.FFmpeg.createFFmpeg({
            log: false,
            corePath: 'vendor/web/ffmpeg-core.js',
            wasmPath: 'vendor/web/ffmpeg-core.wasm',
            workerPath: 'vendor/web/ffmpeg-core.worker.js',
          });
          core.load().then(() => { WASM_LIB.core = core; WASM_LIB.ready = true; WASM_LIB.loading = false; resolve(core); }).catch(reject);
        } else reject(new Error('FFmpeg WASM not loaded'));
      };
      script.onerror = () => reject(new Error('Failed to load ffmpeg.min.js'));
      document.head.appendChild(script);
    });
    return WASM_LIB.loadPromise;
  }

  /* HDR tone-mapping shaders */
  const HDR_SHADER = {
    vs: `attribute vec2 aPos; varying vec2 vUV; void main(){ vUV=aPos*0.5+0.5; gl_Position=vec4(aPos,0,1); }`,
    fs_hdr10: `
      precision highp float;
      varying vec2 vUV;
      uniform sampler2D tex;
      uniform float peak;
      const float m1=0.1593017578125, m2=78.84375, c1=0.8359375, c2=18.8515625, c3=18.6875;
      vec3 pq2linear(vec3 v){ vec3 p=pow(v,vec3(1.0/m2)); vec3 n=max(p-c1,vec3(0.0)); vec3 d=c2-c3*p; return pow(n/d,vec3(1.0/m1)); }
      vec3 linear2bt709(vec3 v){
        float a=0.2627*v.x+0.6780*v.y+0.0593*v.z;
        float b=a/10000.0*peak;
        float t=1.0/(1.0+b*b*0.05);
        vec3 col=v*t;
        col=pow(col,vec3(1.0/2.4));
        return clamp(col,0.0,1.0);
      }
      void main(){ vec4 c=texture2D(tex,vUV); vec3 lin=pq2linear(c.rgb); vec3 sdr=linear2bt709(lin); gl_FragColor=vec4(sdr,c.a); }
    `,
    fs_hlg: `
      precision highp float;
      varying vec2 vUV; uniform sampler2D tex; uniform float peak;
      const float a=0.17883277, b=0.28466892, c=0.55991073;
      vec3 hlg2linear(vec3 v){ vec3 r;
        if(v.x<=0.5)r.x=v.x*v.x/3.0; else r.x=(exp((v.x-c)/a)+b)/12.0;
        if(v.y<=0.5)r.y=v.y*v.y/3.0; else r.y=(exp((v.y-c)/a)+b)/12.0;
        if(v.z<=0.5)r.z=v.z*v.z/3.0; else r.z=(exp((v.z-c)/a)+b)/12.0;
        return r*peak/1000.0; }
      vec3 linear2bt709(vec3 v){ float lum=0.2126*v.x+0.7152*v.y+0.0722*v.z; float t=lum>0.0?pow(lum,0.2)/lum:0.0; return clamp(v*t,0.0,1.0); }
      void main(){ vec4 c=texture2D(tex,vUV); vec3 lin=hlg2linear(c.rgb); gl_FragColor=vec4(linear2bt709(lin),c.a); }
    `,
    fs_dovi: `
      precision highp float;
      varying vec2 vUV; uniform sampler2D tex; uniform float peak;
      uniform float lift,gain,gamma; uniform float pivot;
      vec3 pq2linear(vec3 v){ vec3 p=pow(v,vec3(1.0/78.84375)); vec3 n=max(p-0.8359375,vec3(0.0)); vec3 d=18.8515625-18.6875*p; return pow(n/d,vec3(1.0/0.1593017578125)); }
      void main(){ vec4 c=texture2D(tex,vUV); vec3 lin=pq2linear(c.rgb);
        float lum=0.2126*lin.x+0.7152*lin.y+0.0722*lin.z;
        float scaled=(lum-pivot)*gain+pivot; scaled=max(scaled,0.0);
        float t=lum>0.0?scaled/lum:0.0;
        vec3 outCol=lin*t; outCol=pow(outCol,vec3(gamma));
        outCol=mix(outCol*lift,outCol,step(0.0,pivot));
        gl_FragColor=vec4(clamp(outCol,0.0,1.0),c.a); }
    `,
  };

  function initGL(canvas) {
    const gl = canvas.getContext('webgl', { alpha: false, depth: false, stencil: false, antialias: false, preserveDrawingBuffer: false });
    if (!gl) return null;
    return gl;
  }

  function compileShader(gl, type, src) {
    const s = gl.createShader(type);
    gl.shaderSource(s, src);
    gl.compileShader(s);
    return gl.getShaderParameter(s, gl.COMPILE_STATUS) ? s : null;
  }

  function createProgram(gl, vsSrc, fsSrc) {
    const vs = compileShader(gl, gl.VERTEX_SHADER, vsSrc);
    const fs = compileShader(gl, gl.FRAGMENT_SHADER, fsSrc);
    if (!vs || !fs) return null;
    const p = gl.createProgram();
    gl.attachShader(p, vs); gl.attachShader(p, fs); gl.linkProgram(p);
    return gl.getProgramParameter(p, gl.LINK_STATUS) ? p : null;
  }

  function setupHDRRenderer(canvas, rangeType) {
    const gl = initGL(canvas);
    if (!gl) return null;
    const fsSrc = rangeType === 'DOVI' ? HDR_SHADER.fs_dovi : rangeType === 'HLG' ? HDR_SHADER.fs_hlg : HDR_SHADER.fs_hdr10;
    const prog = createProgram(gl, HDR_SHADER.vs, fsSrc);
    if (!prog) return null;
    gl.useProgram(prog);
    const buf = gl.createBuffer();
    gl.bindBuffer(gl.ARRAY_BUFFER, buf);
    gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1,-1, 1,-1, -1,1, 1,1]), gl.STATIC_DRAW);
    const aPos = gl.getAttribLocation(prog, 'aPos');
    gl.enableVertexAttribArray(aPos);
    gl.vertexAttribPointer(aPos, 2, gl.FLOAT, false, 0, 0);
    const tex = gl.createTexture();
    gl.activeTexture(gl.TEXTURE0);
    gl.bindTexture(gl.TEXTURE_2D, tex);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
    return { gl, prog, tex, canvas };
  }

  function renderHDRFrame(hdrCtx, frame) {
    const { gl, canvas, tex } = hdrCtx;
    if (canvas.width !== frame.displayWidth || canvas.height !== frame.displayHeight) {
      canvas.width = frame.displayWidth; canvas.height = frame.displayHeight;
      gl.viewport(0, 0, canvas.width, canvas.height);
    }
    gl.activeTexture(gl.TEXTURE0);
    gl.bindTexture(gl.TEXTURE_2D, tex);
    gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, gl.RGBA, gl.UNSIGNED_BYTE, frame);
    gl.drawArrays(gl.TRIANGLE_STRIP, 0, 4);
  }

  /* PGS subtitle parser */
  class PGSParser {
    constructor() { this.data = null; this.segments = []; }
    async load(url) {
      const res = await fetch(apiRaw(url));
      if (!res.ok) throw new Error('PGS fetch failed');
      this.data = new Uint8Array(await res.arrayBuffer());
      this._parse();
    }
    _parse() {
      const d = this.data; let off = 0;
      while (off < d.length - 13) {
        if (d[off] !== 0x50 || d[off+1] !== 0x47) { off++; continue; }
        const pts = ((d[off+2]&0xFF)<<24)|((d[off+3]&0xFF)<<16)|((d[off+4]&0xFF)<<8)|(d[off+5]&0xFF);
        const type = d[off+8]; const size = ((d[off+9]&0xFF)<<8)|(d[off+10]&0xFF);
        const body = d.subarray(off+11, off+11+size);
        this.segments.push({ pts: pts/90, type, body });
        off += 11 + size;
      }
    }
    renderAt(time) {
      return this.segments.filter(s => s.pts <= time && s.pts + 4000 > time);
    }
  }

  /* AudioWorklet engine */
  class AudioEngine {
    constructor() { this.ctx = null; this.node = null; this.channels = 2; this.sampleRate = 48000; this.buffers = []; }
    async init(channels, sampleRate) {
      this.channels = channels; this.sampleRate = sampleRate;
      const AudioCtx = window.AudioContext || window.webkitAudioContext;
      this.ctx = new AudioCtx({ sampleRate });
      await this.ctx.audioWorklet.addModule(this._workletCode());
      this.node = new AudioWorkletNode(this.ctx, 'eio-audio-processor', { outputChannelCount: [channels] });
      this.node.connect(this.ctx.destination);
      this.node.port.onmessage = (e) => { if (e.data.type === 'request') this._feed(); };
    }
    _workletCode() {
      const code = `
        class EIOAudioProcessor extends AudioWorkletProcessor {
          constructor() { super(); this.buffers=[]; this.idx=0; this.buf=null; }
          process(inputs, outputs) {
            const out=outputs[0];
            if (!this.buf || this.idx>=this.buf[0].length) {
              if(this.buffers.length===0){this.port.postMessage({type:'request'});return true;}
              this.buf=this.buffers.shift();this.idx=0;
            }
            const n=out[0].length;
            for(let ch=0;ch<out.length;ch++){
              const src=this.buf[ch]||this.buf[0];
              for(let i=0;i<n;i++)out[ch][i]=this.idx+i<src.length?src[this.idx+i]:0;
            }
            this.idx+=n;
            return true;
          }
        }
        registerProcessor('eio-audio-processor', EIOAudioProcessor);
      `;
      return URL.createObjectURL(new Blob([code], { type: 'application/javascript' }));
    }
    push(buffer) { this.buffers.push(buffer); if (this.buffers.length > 32) this.buffers.shift(); }
    _feed() {}
    setPlaying(v) { this.playing = v; if (v) this.ctx.resume(); else this.ctx.suspend(); }
    destroy() { this.node?.disconnect(); this.ctx?.close(); }
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
        if (routeMap[r]) page.value = routeMap[r];
        if (r === 'library') openLibrary(param);
        else if (r === 'item') openItem(param);
        else if (r === 'play') openPlayer(param);
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
      function openPlayer(id) { window.location.hash = '#/play/' + id; }

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
      const playerMode = ref('native');
      const playItemId = ref('');
      const playItem = ref(null);
      const playSrc = ref(null);
      const playMediaSourceId = ref(null);
      const nativeVideo = ref(null);
      const hlsVideo = ref(null);
      const wasmCanvas = ref(null);
      const subCanvas = ref(null);
      const hdrCanvas = ref(null);
      const playerContainer = ref(null);

      let nativeSessionId = null;
      let nativePositionTicks = 0;
      let progressTimer = null;

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

      let wasmDemuxer = null;
      let wasmDecoder = null;
      let audioEngine = null;
      let pgsParser = null;
      let jassub = null;
      let hdrCtx = null;
      let wasmRAF = null;
      let wasmAbort = null;

      const subtitleTracks = computed(() => {
        if (!playSrc.value) return [];
        return playSrc.value.MediaStreams?.filter(s => s.Type === 'Subtitle') || [];
      });

      async function playItem(item, mediaSourceId) {
        playItemId.value = item.Id; playItem.value = item;
        window.location.hash = '#/play/' + item.Id;
        await nextTick();
        await startPlayback(item, mediaSourceId);
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
          const src = playSrc.value;
          const isNative = canNativePlay(src);
          if (isNative) {
            playerMode.value = 'native';
            await nextTick();
            startNativePlayer(src);
          } else {
            playerMode.value = 'wasm';
            await nextTick();
            startWasmPlayer(src, item);
          }
          await api('/Sessions/Playing', { method: 'POST', body: JSON.stringify({ ItemId: item.Id, PlaySessionId: info.PlaySessionId }) }).catch(() => {});
          nativeSessionId = info.PlaySessionId;
          startProgressTimer();
        } catch (e) { console.error('startPlayback', e); alert('播放失败：' + e.message); }
      }

      function startProgressTimer() {
        stopProgressTimer();
        progressTimer = setInterval(() => {
          if (playItemId.value && nativePositionTicks > 0) {
            api('/Sessions/Playing/Progress', {
              method: 'POST',
              body: JSON.stringify({ ItemId: playItemId.value, PlaySessionId: nativeSessionId, PositionTicks: nativePositionTicks }),
            }).catch(() => {});
          }
        }, 10000);
      }

      function stopProgressTimer() {
        if (progressTimer) { clearInterval(progressTimer); progressTimer = null; }
      }

      function canNativePlay(src) {
        const container = (src.Container || '').toLowerCase();
        const v = src.MediaStreams?.find(s => s.Type === 'Video');
        const a = src.MediaStreams?.find(s => s.Type === 'Audio');
        if (!container || !v) return false;
        const okContainers = ['mp4', 'm4v', 'webm', 'mov'];
        const okVideo = ['h264', 'avc', 'av1', 'vp9'];
        const okAudio = ['aac', 'mp3', 'opus', 'vorbis', 'eac3', 'ac3'];
        return okContainers.includes(container) && okVideo.includes(v.Codec) && (!a || okAudio.includes(a.Codec));
      }

      function startNativePlayer(src) {
        const url = apiRaw(src.DirectStreamUrl);
        const video = nativeVideo.value;
        if (!video) return;
        video.src = url;
        video.load();
        video.play().catch(e => console.warn('autoplay blocked', e));
      }

      function onNativePlay() {}
      function onNativePause() {}
      function onTimeUpdate(e) { nativePositionTicks = Math.floor(e.target.currentTime * 10000000); }
      function onEnded() { reportStopped(); }
      function onVideoError(e) { console.error('video error', e); }
      function onNativeLoaded() {}

      async function reportStopped() {
        if (!playItemId.value) return;
        await api('/Sessions/Playing/Stopped', { method: 'POST', body: JSON.stringify({ ItemId: playItemId.value, PlaySessionId: nativeSessionId, PositionTicks: nativePositionTicks }) }).catch(() => {});
        stopProgressTimer();
      }

      function exitPlayer() {
        reportStopped();
        wasmAbort?.abort();
        if (wasmDemuxer) { try { wasmDemuxer.exit(); } catch {} wasmDemuxer = null; }
        audioEngine?.destroy(); audioEngine = null;
        jassub?.destroy(); jassub = null;
        hdrCtx = null; hdrShaderActive.value = false;
        if (wasmRAF) cancelAnimationFrame(wasmRAF);
        window.history.back();
      }

      async function startWasmPlayer(src, item) {
        wasmLoading.value = true; wasmLoadingMsg.value = '加载 WASM 解码器...';
        wasmTime.value = 0; wasmDuration.value = item.RunTimeTicks ? item.RunTimeTicks / 10000000 : 0;
        try {
          await ensureFFmpeg();
          wasmLoadingMsg.value = '解析视频流...';
          await wasmDemuxAndPlay(src, item);
        } catch (e) {
          console.error('wasm play failed', e);
          wasmLoadingMsg.value = '播放失败：' + e.message;
        }
      }

      async function wasmDemuxAndPlay(src, item) {
        const url = apiRaw(src.DirectStreamUrl);
        wasmLoadingMsg.value = '拉取视频数据...';
        const res = await fetch(url);
        if (!res.ok) throw new Error('Failed to fetch stream: ' + res.status);
        const data = new Uint8Array(await res.arrayBuffer());
        wasmLoadingMsg.value = '解码中...';

        const vStream = src.MediaStreams?.find(s => s.Type === 'Video');
        const core = WASM_LIB.core;
        const inputName = 'input.' + (src.Container || 'mkv');
        const outputName = 'output.mp4';

        try {
          await core.FS('writeFile', inputName, data);
          await core.run('-i', inputName, '-c', 'copy', '-movflags', 'frag_keyframe+empty_moov+default_base_moof', '-f', 'mp4', outputName);
          const outData = core.FS('readFile', outputName);
          const blob = new Blob([outData.buffer], { type: 'video/mp4' });
          const objUrl = URL.createObjectURL(blob);
          const video = nativeVideo.value;
          video.src = objUrl;
          video.load();
          video.play().catch(() => {});
          wasmLoading.value = false;
          playerMode.value = 'native';
        } catch (e) {
          wasmLoadingMsg.value = '解码失败：' + e.message;
        }
      }

      async function loadPGS(url) {
        pgsParser = new PGSParser();
        await pgsParser.load(url);
        const render = () => {
          if (!pgsParser || !subCanvas.value) return;
          const time = nativeVideo.value?.currentTime || 0;
          const subs = pgsParser.renderAt(time);
          const ctx = subCanvas.value.getContext('2d');
          ctx.clearRect(0, 0, subCanvas.value.width, subCanvas.value.height);
          for (const seg of subs) {
            if (seg.type === 0x16) { /* PCS parse placeholder */ }
          }
        };
        setInterval(render, 100);
      }

      async function loadASS(url) {
        const video = nativeVideo.value;
        if (!video) return;
        jassub = new JASSUB({
          video, subUrl: apiRaw(url),
          workerUrl: 'vendor/web/jassub-worker.js',
          wasmUrl: 'vendor/web/jassub-worker.wasm',
        });
      }

      function selectSubtitle(idx) {
        wasmSubTrack.value = idx;
        if (idx === -1) { jassub?.destroy(); jassub = null; return; }
        const track = subtitleTracks.value.find(s => s.Index === idx);
        if (!track) return;
        if (track.Codec === 'ass' || track.Codec === 'ssa') {
          loadASS(track.DeliveryUrl || `/Videos/${playItemId.value}/${playMediaSourceId.value}/Subtitles/${idx}/Stream.ass`);
        } else if (track.Codec === 'PGS') {
          loadPGS(track.DeliveryUrl || `/Videos/${playItemId.value}/${playMediaSourceId.value}/Subtitles/${idx}/Stream.sup`);
        }
      }

      function onWasmSeek(v) { wasmTime.value = parseFloat(v); }

      function wasmTogglePause() {
        const video = nativeVideo.value;
        if (video) { video.paused ? video.play() : video.pause(); }
        wasmPlaying.value = !wasmPlaying.value;
      }

      /* ---- lifecycle ---- */
      onMounted(async () => {
        window.addEventListener('hashchange', syncFromHash);
        lucide.createIcons();
        if (localStorage.getItem('eio_token')) {
          try {
            const me = await api(`/Users/${localStorage.getItem('eio_user_id')}`);
            currentUser.value = me;
            window.location.hash = window.location.hash || '#/home';
            syncFromHash();
            if (navPage.value === 'home') loadHome();
          } catch { doLogout(); }
        } else { syncFromHash(); }
      });

      onBeforeUnmount(() => { window.removeEventListener('hashchange', syncFromHash); exitPlayer(); });

      return {
        page, navPage, loginForm, loginError, loginLoading, currentUser, passWrap, mobileMenu, pages,
        navigateTo, doLogin, doLogout, focusPass,
        resumeItems, libraries, latestSections, loadHome,
        libraryName, libraryItems, libraryLoading, libFilter, libSort, libGenres, libLimit,
        openLibrary, loadLibraryItems, loadLibraryMore,
        itemDetail, itemDetailLoading, seriesSeasons, seasonEpisodes, openItem,
        itemDetailIs4K, itemDetailIsHDR, itemDetailIsDV, itemDetailOfficialRating, runtimeMin, runtimeMin_,
        currentMediaStreams, subtitleStreams, audioStreams, currentVideoStream, mediaSourceTags,
        toggleFavorite, playItem,
        searchQuery, searchResults, searchLoading, debouncedSearch,
        imageUrl, backdropUrl,
        playerMode, playItemId, playSrc, nativeVideo, hlsVideo, wasmCanvas, subCanvas, hdrCanvas, playerContainer,
        wasmLoading, wasmLoadingMsg, wasmPlaying, wasmTime, wasmDuration, wasmTimeHuman, wasmDurationHuman,
        wasmSubTrack, showSubMenu, hdrShaderActive, subtitleTracks,
        onNativePlay, onNativePause, onTimeUpdate, onEnded, onVideoError, onNativeLoaded,
        exitPlayer, wasmTogglePause, onWasmSeek, selectSubtitle,
        startPlayback,
      };
    },
  }).mount('#app');
})();
