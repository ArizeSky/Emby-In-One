/* Emby-in-One Web client — media library page controller (v1.4.4-rc3).
 *
 * Owns the library list state: name/type, items, first-page vs paging status,
 * sort/filter, the real paging cursor and the sentinel observer. The caller
 * injects api/query, the shared EIOUI library view and the page-activity
 * check; this module never reads auth or builds its own requests.
 */
(function () {
  'use strict';

  const PAGE_LIMIT = 50;
  const FIELDS = 'PrimaryImageAspectRatio,ProductionYear,ProviderIds,CommunityRating,RunTimeTicks,UserData';

  function create(deps) {
    const { ref, computed } = window.Vue;
    const name = ref('');
    const items = ref([]);
    const loading = ref(false);        // first page / switching library
    const pageLoading = ref(false);    // append paging
    const total = ref(null);
    const retryAppend = ref(false);
    const filter = ref('');
    const sort = ref('DateCreated,Descending');
    const genres = ref([]);
    const hasMore = ref(false);
    const error = ref('');
    const pageError = ref('');
    let offset = 0;                    // StartIndex of the next page
    let id = '';
    let type = '';
    let request = null;
    let pageRequest = null;
    const view = deps.view;

    const countText = computed(() => (total.value != null
      ? `共 ${total.value} 项`
      : `已加载 ${items.value.length} 项`));

    function uid() { return deps.getUserId?.(); }
    function active() { return !!deps.isActive?.(); }

    function resetPaging() {
      offset = 0; total.value = null; hasMore.value = false;
      retryAppend.value = false; pageError.value = '';
    }

    function canPage() {
      return active() && hasMore.value && !pageLoading.value && !pageError.value && !loading.value;
    }

    function pageParams(limit) {
      const [sortField, sortDir] = sort.value.split(',');
      return {
        ParentId: id, Limit: limit, SortBy: sortField, SortOrder: sortDir, Genres: filter.value,
        Fields: FIELDS, Recursive: true,
        IncludeItemTypes: type === 'tvshows' ? 'Series' : type === 'movies' ? 'Movie' : '',
      };
    }

    async function load(nextId) {
      request?.abort(); pageRequest?.abort();
      const current = request = new AbortController();
      id = nextId; name.value = ''; type = '';
      items.value = []; error.value = '';
      resetPaging();
      filter.value = ''; genres.value = [];
      view.disconnect();
      loading.value = true;
      try {
        const lib = deps.libraries.value.find(l => l.Id === nextId)
          || await deps.api(`/Users/${uid()}/Items/${nextId}`, { signal: current.signal });
        if (request !== current) return;
        name.value = lib.Name;
        type = lib.CollectionType || '';
      } catch (e) {
        if (e.name === 'AbortError') return;
        if (request === current) error.value = '媒体库信息加载失败';
      }
      // Genres are decorative chips: a failure here must never block items.
      deps.api(`/Genres?ParentId=${nextId}&UserId=${uid()}&Limit=50`, { signal: current.signal })
        .then(res => { if (request === current) genres.value = (res.Items || []).map(g => g.Name); })
        .catch(() => { if (request === current) genres.value = []; });
      await loadItems(false);
      if (request === current) { loading.value = false; deps.refreshIcons?.(); }
    }

    async function loadItems(append) {
      if (!id || !active() || request?.signal.aborted) return;
      if (append) {
        if (!canPage()) return;
        pageLoading.value = true;
      } else {
        pageRequest?.abort();
      }
      const current = pageRequest = new AbortController();
      const parentId = id;
      const requestOffset = append ? offset : 0;
      const filterSnapshot = filter.value;
      pageError.value = ''; retryAppend.value = false;
      try {
        const params = { ...pageParams(PAGE_LIMIT), StartIndex: requestOffset };
        const result = await deps.api(deps.query(`/Users/${uid()}/Items`, params), { signal: current.signal });
        if (pageRequest !== current || id !== parentId || current.signal.aborted) return;
        if (filter.value !== filterSnapshot) return; // filter changed under us
        const batch = result.Items || [];
        const existing = items.value;
        const seen = new Set(existing.map(x => x.Id));
        const fresh = batch.filter(x => !seen.has(x.Id));
        if (append && !fresh.length && batch.length) {
          // Repeated page: stop auto-paging instead of looping forever.
          hasMore.value = false; retryAppend.value = true;
          pageError.value = '分页未推进，请重试';
        } else {
          items.value = append ? [...existing, ...fresh] : batch;
          // The cursor advances by the raw batch length, never by the deduped list.
          offset = requestOffset + batch.length;
          if (result.TotalRecordCount != null) {
            total.value = result.TotalRecordCount;
            hasMore.value = offset < result.TotalRecordCount;
          } else {
            total.value = null;
            hasMore.value = batch.length > 0;
          }
        }
      } catch (e) {
        if (e.name === 'AbortError' || pageRequest !== current) return;
        if (append) { pageError.value = '加载失败：' + deps.formatError(e); retryAppend.value = true; }
        else error.value = '加载失败：' + deps.formatError(e);
      } finally {
        if (pageRequest === current) pageLoading.value = false;
      }
      armObserver();
      deps.refreshIcons?.();
    }

    function armObserver() {
      if (!canPage()) { view.disconnect(); return; }
      deps.nextTick(() => {
        if (!canPage()) { view.disconnect(); return; }
        view.attachObserver(() => { if (canPage()) loadMore(); });
      });
    }

    function loadMore() { if (!canPage()) return; return loadItems(true); }
    function retryPage() { pageError.value = ''; retryAppend.value = false; return loadItems(true); }
    function retry() { return load(id); }

    function set(kind, value) {
      const target = kind === 'sort' ? sort : filter;
      if (target.value === value) return;
      target.value = value;
      resetPaging();
      items.value = []; error.value = '';
      loadItems(false);
    }
    function setFilter(value) { set('filter', value); }
    function setSort(value) { set('sort', value); }
    function clearFilter() { setFilter(''); }

    function cancel() {
      request?.abort(); request = null;
      pageRequest?.abort(); pageRequest = null;
      view.disconnect();
      loading.value = false; pageLoading.value = false;
    }
    function dispose() { cancel(); items.value = []; }

    return { name, items, loading, pageLoading, total, retryAppend, filter, sort, genres, hasMore,
      error, pageError, countText, view, load, loadMore, retryPage, retry,
      setFilter, setSort, clearFilter, cancel, dispose };
  }

  window.EIOLibrary = { create };
})();
