/* Emby-in-One Web client — page request controllers (v1.4.4-rc3).
 *
 * Each controller owns an AbortController and a monotonically increasing
 * generation. Every load/schedule/retry bumps the generation and aborts the
 * previous request; a result may only commit while it still belongs to the
 * current generation, its signal is live, the page is still active and the
 * user id has not changed. Failure/finally paths are guarded the same way so
 * a late response can never overwrite a newer page.
 */
(function () {
  'use strict';

  const LATEST_FIELDS = 'PrimaryImageAspectRatio,ProductionYear,CommunityRating,Overview,Genres,RunTimeTicks,UserData,BackdropImageTags,OfficialRating,OriginalTitle';
  const RESUME_FIELDS = 'PrimaryImageAspectRatio,RunTimeTicks,UserData,SeriesName';
  const SEARCH_FIELDS = 'PrimaryImageAspectRatio,ProductionYear,Overview';
  const SLOW_MS = 8000;

  function slowFlag() {
    const { ref } = window.Vue;
    const slow = ref(false);
    let timer = null;
    return {
      slow,
      start() { slow.value = false; this.stop(); timer = setTimeout(() => { slow.value = true; }, SLOW_MS); },
      stop() { if (timer != null) { clearTimeout(timer); timer = null; } slow.value = false; },
    };
  }

  /* Home: one Views + up to three Latest + one bounded Resume request. */
  function createHome(deps) {
    const { ref } = window.Vue;
    const resumeItems = ref([]);
    const viewsLoading = ref(false);
    const viewsError = ref('');
    const resumeLoading = ref(false);
    const resumeError = ref('');
    const latestLoading = ref({});
    const latestError = ref({});
    const viewsSlow = slowFlag();
    const resumeSlow = slowFlag();
    const latestSlow = slowFlag();
    let generation = 0;
    let controller = null;

    function uid() { return deps.getUserId?.(); }
    function live(gen, signal) { return gen === generation && !signal.aborted && !!deps.isActive?.(); }

    async function loadResume(gen, signal) {
      resumeLoading.value = true; resumeError.value = ''; resumeSlow.start();
      try {
        const res = await deps.api(deps.query(`/Users/${uid()}/Items/Resume`, { Limit: 12, Fields: RESUME_FIELDS }), { signal });
        if (!live(gen, signal)) return;
        resumeItems.value = res.Items || [];
      } catch (e) {
        if (e.name === 'AbortError' || !live(gen, signal)) return;
        resumeError.value = '继续观看加载失败';
      } finally {
        if (gen === generation && !signal.aborted) { resumeLoading.value = false; resumeSlow.stop(); }
      }
    }

    async function loadViews(gen, signal) {
      viewsLoading.value = true; viewsError.value = ''; viewsSlow.start();
      try {
        const res = await deps.api(`/Users/${uid()}/Views`, { signal });
        if (!live(gen, signal)) return;
        deps.libraries.value = res.Items || [];
      } catch (e) {
        if (e.name === 'AbortError' || !live(gen, signal)) return;
        viewsError.value = '媒体库加载失败';
      } finally {
        if (gen === generation && !signal.aborted) { viewsLoading.value = false; viewsSlow.stop(); }
      }
    }

    function mergeSection(lib, items) {
      const rest = deps.latestSections.value.filter(sec => sec.key !== lib.Id);
      if (items.length) rest.push({ key: lib.Id, title: lib.Name, items });
      deps.latestSections.value = rest;
    }

    async function loadLatest(gen, signal, lib) {
      latestLoading.value = { ...latestLoading.value, [lib.Id]: true };
      latestSlow.start();
      const cleared = { ...latestError.value }; delete cleared[lib.Id]; latestError.value = cleared;
      try {
        const res = await deps.api(deps.query(`/Users/${uid()}/Items/Latest`, { Limit: 12, ParentId: lib.Id, Fields: LATEST_FIELDS }), { signal });
        if (!live(gen, signal)) return;
        // /Items/Latest answers with a bare array; accept both shapes.
        const items = Array.isArray(res) ? res : (res?.Items || []);
        mergeSection(lib, items);
      } catch (e) {
        if (e.name === 'AbortError' || !live(gen, signal)) return;
        latestError.value = { ...latestError.value, [lib.Id]: '本区加载失败，请重试' };
      } finally {
        if (gen === generation && !signal.aborted) {
          latestLoading.value = { ...latestLoading.value, [lib.Id]: false };
          const anyLoading = Object.values(latestLoading.value).some(Boolean);
          if (!anyLoading) latestSlow.stop();
        }
      }
    }

    async function loadLatestGroup(gen, signal) {
      const targets = deps.libraries.value.filter(lib => lib.CollectionType !== 'playlists').slice(0, 3);
      await Promise.allSettled(targets.map(lib => loadLatest(gen, signal, lib)));
    }

    function cancel() {
      generation++;
      controller?.abort();
      controller = null;
      viewsSlow.stop(); resumeSlow.stop(); latestSlow.stop();
      viewsLoading.value = false; resumeLoading.value = false;
    }

    function load() {
      cancel();
      const gen = generation;
      const ctrl = controller = new AbortController();
      const signal = ctrl.signal;
      latestLoading.value = {}; latestError.value = {};
      // Views/Latest and Resume start together and never await each other.
      const viewsChain = (async () => {
        await loadViews(gen, signal);
        if (!live(gen, signal)) return;
        await loadLatestGroup(gen, signal);
      })();
      return Promise.allSettled([viewsChain, loadResume(gen, signal)]);
    }

    function retryResume() {
      const gen = generation;
      const signal = controller?.signal;
      if (!signal) return Promise.resolve();
      return loadResume(gen, signal);
    }
    function retryLatest(lib) {
      const gen = generation;
      const signal = controller?.signal;
      if (!signal) return Promise.resolve();
      return loadLatest(gen, signal, lib);
    }
    function dispose() { cancel(); }

    return { resumeItems, viewsLoading, viewsError, resumeLoading, resumeError,
      latestLoading, latestError, viewsSlow, resumeSlow, latestSlow,
      load, cancel, retryResume, retryLatest, dispose };
  }

  /* Search: generation-guarded debounce + request, grouped results. */
  function createSearch(deps) {
    const { ref, computed } = window.Vue;
    const error = ref('');
    const submittedQuery = ref('');
    const waiting = ref(false);
    const slow = slowFlag();
    let generation = 0;
    let controller = null;
    let debounceTimer = null;

    const movieResults = computed(() => deps.searchResults.value.filter(item => item.Type === 'Movie'));
    const seriesResults = computed(() => deps.searchResults.value.filter(item => item.Type === 'Series'));

    function clearResults() {
      deps.searchResults.value = [];
      deps.searchLoading.value = false;
      waiting.value = false;
      slow.stop();
    }

    function cancel() {
      generation++;
      controller?.abort(); controller = null;
      if (debounceTimer != null) { clearTimeout(debounceTimer); debounceTimer = null; }
      slow.stop();
      deps.searchLoading.value = false;
      waiting.value = false;
    }

    function schedule() {
      const term = String(deps.searchQuery.value || '').trim();
      generation++;
      controller?.abort(); controller = null;
      if (debounceTimer != null) { clearTimeout(debounceTimer); debounceTimer = null; }
      error.value = ''; slow.stop();
      if (!term) { clearResults(); submittedQuery.value = ''; return; }
      waiting.value = true;
      deps.searchLoading.value = true;
      debounceTimer = setTimeout(() => { debounceTimer = null; run(term); }, 400);
    }

    async function run(term) {
      const gen = generation;
      const ctrl = controller = new AbortController();
      const signal = ctrl.signal;
      waiting.value = false;
      deps.searchLoading.value = true;
      error.value = '';
      slow.start();
      try {
        const path = deps.query(`/Users/${deps.getUserId()}/Items`, {
          SearchTerm: term, Limit: 30, Recursive: true,
          IncludeItemTypes: 'Series,Movie', Fields: SEARCH_FIELDS,
        });
        const res = await deps.api(path, { signal });
        if (gen !== generation || signal.aborted || !deps.isActive?.()) return;
        deps.searchResults.value = res.Items || [];
        submittedQuery.value = term;
      } catch (e) {
        if (e.name === 'AbortError' || gen !== generation) return;
        error.value = '搜索失败：' + e.message;
        submittedQuery.value = term;
      } finally {
        if (gen === generation && !signal.aborted) {
          deps.searchLoading.value = false;
          slow.stop();
        }
      }
    }

    function retry() {
      const term = submittedQuery.value || String(deps.searchQuery.value || '').trim();
      if (!term) return Promise.resolve();
      generation++;
      controller?.abort(); controller = null;
      if (debounceTimer != null) { clearTimeout(debounceTimer); debounceTimer = null; }
      return run(term);
    }

    function dispose() { cancel(); }

    return { error, submittedQuery, waiting, slow, movieResults, seriesResults, schedule, retry, cancel, dispose };
  }

  window.EIOPages = { createHome, createSearch };
})();
