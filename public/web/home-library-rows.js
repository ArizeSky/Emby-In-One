/* Emby-in-One Web client — home library row preview state (V1.4.4-rc2).
 *
 * Owns one "first page of a complete library" preview per visible library row:
 * the per-row status machine (idle/loading/ready/empty/partial/error), a FIFO
 * queue with a hard cap of two shared physical requests, and the generation /
 * row-identity / user guards that keep a late, aborted or cross-account
 * response from ever writing into a newer page.
 *
 * It never renders, never reads auth, never builds headers and never touches
 * /Items/Latest: page-state.js injects api/query/identity and only this module
 * knows the row protocol. There is no auto-paging and no exact total here;
 * rows are the first page only.
 */
(function () {
  'use strict';

  // Two physical requests may be in flight across all rows. An aborted but not
  // yet settled request still counts, because abort does not settle a promise
  // synchronously; its own finally releases the slot.
  const MAX_INFLIGHT = 2;
  const FALLBACK_LIMIT = 50;
  const ERROR_TEXT = '预览加载失败，请重试';

  function create(deps) {
    const { ref } = window.Vue;
    const rows = ref([]);
    const queue = [];
    const inflight = new Set();
    let generation = 0;
    let disposed = false;

    function uid() { return deps.getUserId?.(); }
    function active() { return !!deps.isActive?.(); }

    function pageLimit() {
      const value = window.EIOLibraryQuery && window.EIOLibraryQuery.PAGE_LIMIT;
      return Number.isInteger(value) && value > 0 ? value : FALLBACK_LIMIT;
    }

    // Always resolves the current row through rows.value: a Vue ref turns rows
    // into reactive proxies, so a locally built raw object is never identical
    // to the object a commit later reads back.
    function findRow(key) {
      for (const row of rows.value) if (row && row.key === key) return row;
      return null;
    }

    function buildRows(libraries) {
      const source = Array.isArray(libraries) ? libraries : [];
      const seen = new Set();
      const built = [];
      for (const lib of source) {
        if (!lib) continue;
        if (lib.Id === null || lib.Id === undefined) continue;
        const key = String(lib.Id);
        if (!key.trim() || seen.has(key)) continue;
        seen.add(key);
        built.push({
          key,
          title: lib.Name || '媒体库',
          libraryType: lib.CollectionType || '',
          items: [],
          status: 'idle',
          error: '',
          partial: false,
        });
      }
      return built;
    }

    /* ================= public state transitions ================= */

    function reset(libraries) {
      if (disposed) return;
      invalidate();
      rows.value = buildRows(libraries);
    }

    function ensure(id) {
      if (disposed) return;
      const key = id === null || id === undefined ? '' : String(id);
      if (!key) return;
      const row = findRow(key);
      if (!row || row.status !== 'idle') return;
      row.status = 'loading';
      enqueue(row, key, row.libraryType);
    }

    function retry(id) {
      if (disposed) return;
      const key = id === null || id === undefined ? '' : String(id);
      if (!key) return;
      const row = findRow(key);
      if (!row || (row.status !== 'error' && row.status !== 'partial')) return;
      // The previously loaded first page stays on screen while the retry runs;
      // a successful retry replaces it, a failed one keeps it.
      row.status = 'loading';
      row.error = '';
      row.partial = false;
      enqueue(row, key, row.libraryType);
    }

    function cancel() {
      invalidate();
      rows.value = [];
    }

    function dispose() {
      if (disposed) return;
      cancel();
      disposed = true;
    }

    /* ================= generation / queue ================= */

    // A new generation invalidates queued work and aborts in-flight requests.
    // Aborted requests keep their physical slot until they really settle.
    function invalidate() {
      generation++;
      queue.length = 0;
      for (const task of inflight) task.controller.abort();
    }

    function enqueue(rowRef, key, libraryType) {
      queue.push({
        gen: generation,
        key,
        libraryType: libraryType || '',
        rowRef,
        uid: uid(),
        controller: new AbortController(),
      });
      pump();
    }

    function pump() {
      if (disposed) return;
      while (inflight.size < MAX_INFLIGHT && queue.length) start(queue.shift());
    }

    function start(task) {
      if (task.gen !== generation || task.controller.signal.aborted) return;
      // Pre-start gate: a page that is no longer active, has no user, or now
      // belongs to another user must not spend a slot (the queued task captured
      // the uid it was ensured for, and its row belongs to that account). The
      // row goes back to idle so re-entering the page can ensure it again.
      if (!active() || !uid() || uid() !== task.uid) { revertToIdle(task); return; }
      inflight.add(task);
      run(task);
    }

    async function run(task) {
      try {
        const result = await request(task);
        commit(task, result);
      } catch (e) {
        if (!e || e.name !== 'AbortError') fail(task);
      } finally {
        // The slot is released even when the owner checks above failed: an
        // aborted-but-settled request must never pin the queue.
        inflight.delete(task);
        pump();
      }
    }

    function request(task) {
      const params = window.EIOLibraryQuery.pageParams({
        id: task.key,
        type: task.libraryType,
        limit: pageLimit(),
        startIndex: 0,
      });
      return deps.api(deps.query(`/Users/${task.uid}/Items`, params), { signal: task.controller.signal });
    }

    /* ================= commit guards ================= */

    function owns(task) {
      return !disposed
        && task.gen === generation
        && !task.controller.signal.aborted
        && findRow(task.key) === task.rowRef
        && uid() === task.uid;
    }

    function commit(task, result) {
      if (!owns(task)) return;
      const items = normalize(result);
      const incomplete = result?.EioIncompleteResults === true;
      // An incomplete response is partial even when Items is empty: an empty
      // partial page is not proof that the library has no content.
      const row = task.rowRef;
      row.items = items;
      row.error = '';
      row.partial = incomplete;
      row.status = incomplete ? 'partial' : (items.length ? 'ready' : 'empty');
    }

    function fail(task) {
      if (!owns(task)) return;
      const row = task.rowRef;
      row.error = ERROR_TEXT;
      row.partial = false;
      row.status = 'error';
    }

    function revertToIdle(task) {
      if (disposed || task.gen !== generation || task.controller.signal.aborted) return;
      if (findRow(task.key) !== task.rowRef) return;
      if (task.rowRef.status !== 'loading') return;
      task.rowRef.status = 'idle';
      task.rowRef.error = '';
      task.rowRef.partial = false;
    }

    // Response order, first occurrence wins, capped at the first-page limit.
    // No sorting, no page merging, no total is ever kept.
    function normalize(result) {
      const batch = Array.isArray(result?.Items) ? result.Items : [];
      const max = pageLimit();
      const seen = new Set();
      const kept = [];
      for (const item of batch) {
        if (!item || item.Id === null || item.Id === undefined) continue;
        const key = String(item.Id);
        if (seen.has(key)) continue;
        seen.add(key);
        kept.push(item);
        if (kept.length >= max) break;
      }
      return kept;
    }

    return { rows, reset, ensure, retry, cancel, dispose };
  }

  window.EIOHomeLibraryRows = { create };
})();
