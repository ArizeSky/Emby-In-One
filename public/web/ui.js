/* Emby-in-One Web client — shared UI state factories (V1.4.4-rc2).
 *
 * These factories own presentation-only state: Toast queue, home Hero,
 * recent-search memory, library view mode and item-detail display state.
 * They never perform network requests or read auth; callers inject data.
 *
 * All factories run inside a Vue setup() and return plain objects holding
 * refs. The caller wraps the result with reactive() so templates can read
 * e.g. `home.resumeItems` without `.value`.
 */
(function () {
  'use strict';

  function toastDuration(kind) { return kind === 'error' ? 4000 : 2500; }

  /* One visible Toast queue, max 3 items, hover/focus pauses timers. */
  function createToasts(options = {}) {
    const { ref } = window.Vue;
    const max = options.max || 3;
    const items = ref([]);
    const timers = new Map();
    const remaining = new Map();
    let seq = 0;

    function clearTimer(id) {
      const timer = timers.get(id);
      if (timer != null) { clearTimeout(timer); timers.delete(id); }
    }
    function arm(id, ms) {
      clearTimer(id);
      const startedAt = Date.now();
      remaining.set(id, { total: ms, startedAt });
      timers.set(id, setTimeout(() => dismiss(id), ms));
    }
    function dismiss(id) {
      clearTimer(id);
      remaining.delete(id);
      items.value = items.value.filter(item => item.id !== id);
    }
    function push(input) {
      const opts = typeof input === 'string' ? { message: input } : (input || {});
      if (!opts.message) return null;
      const kind = opts.kind === 'error' ? 'error' : 'status';
      const id = ++seq;
      items.value = [...items.value, { id, message: opts.message, kind }];
      // Evict the oldest entries so the queue never exceeds its bound.
      while (items.value.length > max) {
        const oldest = items.value[0];
        clearTimer(oldest.id);
        remaining.delete(oldest.id);
        items.value = items.value.slice(1);
      }
      arm(id, opts.duration || toastDuration(kind));
      return id;
    }
    function hold(id) {
      const timer = timers.get(id);
      if (timer == null) return;
      const entry = remaining.get(id);
      clearTimer(id);
      if (entry) entry.remaining = entry.total - (Date.now() - entry.startedAt);
    }
    function release(id) {
      if (timers.has(id)) return;
      const entry = remaining.get(id);
      if (!entry || !items.value.some(item => item.id === id)) return;
      arm(id, Math.max(400, entry.remaining ?? entry.total));
    }
    function clear() {
      for (const id of timers.keys()) clearTimeout(timers.get(id));
      timers.clear(); remaining.clear(); items.value = [];
    }
    function dispose() { clear(); }
    return { items, push, dismiss, hold, release, clear, dispose };
  }

  /* Home Hero over the already-loaded Latest candidates (no requests). */
  function createHero(options = {}) {
    const { ref, computed } = window.Vue;
    const index = ref(0);
    const paused = ref(false);
    const interval = options.interval || 8000;
    let timer = null;
    let disposed = false;

    const candidates = computed(() => {
      const seen = new Set();
      const out = [];
      for (const item of (options.getItems?.() || [])) {
        if (!item || seen.has(item.Id)) continue;
        if (item.Type !== 'Movie' && item.Type !== 'Series') continue;
        seen.add(item.Id);
        out.push(item);
        if (out.length >= 5) break;
      }
      return out;
    });
    const item = computed(() => candidates.value[index.value] || null);

    function clearTimer() { if (timer != null) { clearInterval(timer); timer = null; } }
    function emit() { options.onChange?.(index.value, item.value); }
    function clamp() { if (index.value >= candidates.value.length) index.value = 0; }
    function canAuto() {
      return !disposed && !paused.value && candidates.value.length > 1
        && !!options.isActive?.() && typeof document !== 'undefined' && !document.hidden;
    }
    function schedule() {
      clearTimer();
      if (canAuto()) timer = setInterval(advance, interval);
    }
    function advance() {
      clamp();
      const size = candidates.value.length;
      if (size < 2) { schedule(); return; }
      index.value = (index.value + 1) % size;
      emit();
      schedule();
    }
    function select(i) {
      if (!Number.isInteger(i) || i < 0 || i >= candidates.value.length) return;
      index.value = i; emit(); schedule();
    }
    function hold() { paused.value = true; clearTimer(); }
    function release() { paused.value = false; schedule(); }
    function setAuto(enabled) { if (enabled) release(); else hold(); }
    function reset() { clamp(); emit(); schedule(); }
    function onVisibility() { schedule(); }
    if (typeof document !== 'undefined') document.addEventListener('visibilitychange', onVisibility);
    schedule();

    function dispose() {
      disposed = true;
      clearTimer();
      if (typeof document !== 'undefined') document.removeEventListener('visibilitychange', onVisibility);
    }
    return { index, item, candidates, paused, select, hold, release, setAuto, reset, dispose };
  }

  /* Recent searches, scoped per user id. Storage failures stay in memory. */
  function createRecentSearches(options = {}) {
    const { ref } = window.Vue;
    const terms = ref([]);
    const limit = options.limit || 8;

    function storageKey() {
      try {
        const uid = options.getUserId?.();
        return uid ? `eio_recent_searches_${uid}` : null;
      } catch (e) { return null; }
    }
    function read() {
      const key = storageKey();
      if (!key) { terms.value = []; return terms.value; }
      try {
        const raw = localStorage.getItem(key);
        const parsed = raw ? JSON.parse(raw) : [];
        terms.value = Array.isArray(parsed)
          ? parsed.filter(term => typeof term === 'string' && term.trim()).slice(0, limit)
          : [];
      } catch (e) { terms.value = []; }
      return terms.value;
    }
    function persist() {
      const key = storageKey();
      if (!key) return;
      try { localStorage.setItem(key, JSON.stringify(terms.value)); } catch (e) { /* keep memory */ }
    }
    function add(term) {
      const value = String(term || '').trim();
      if (!value) return;
      terms.value = [value, ...terms.value.filter(existing => existing !== value)].slice(0, limit);
      persist();
    }
    function remove(term) { terms.value = terms.value.filter(existing => existing !== term); persist(); }
    function clear() {
      terms.value = [];
      const key = storageKey();
      if (key) { try { localStorage.removeItem(key); } catch (e) { /* ignore */ } }
    }
    return { terms, read, add, remove, clear };
  }

  /* Grid/list mode plus a bounded IntersectionObserver over a sentinel. */
  function createLibraryView() {
    const { ref } = window.Vue;
    const mode = ref('grid');
    const sentinel = ref(null);
    let observer = null;

    function disconnect() { if (observer) { observer.disconnect(); observer = null; } }
    function setSentinel(el) { sentinel.value = el || null; }
    function setMode(value) { mode.value = value === 'list' ? 'list' : 'grid'; }
    function toggle() { setMode(mode.value === 'grid' ? 'list' : 'grid'); }
    function attachObserver(callback) {
      disconnect();
      const target = sentinel.value;
      if (!target || typeof IntersectionObserver === 'undefined') return;
      observer = new IntersectionObserver(entries => {
        for (const entry of entries) if (entry.isIntersecting) callback?.();
      }, { rootMargin: '600px' });
      observer.observe(target);
    }
    function dispose() { disconnect(); sentinel.value = null; }
    return { mode, sentinel, setSentinel, setMode, toggle, attachObserver, disconnect, dispose };
  }

  /* Pure display state for the item detail page. */
  function createDetailView() {
    const { ref } = window.Vue;
    const selectedSeasonId = ref(null);
    const overviewExpanded = ref(false);
    function setSeason(id) { selectedSeasonId.value = id || null; }
    function reset() { selectedSeasonId.value = null; overviewExpanded.value = false; }
    function toggleOverview() { overviewExpanded.value = !overviewExpanded.value; }
    return { selectedSeasonId, overviewExpanded, setSeason, reset, toggleOverview };
  }

  window.EIOUI = { createToasts, createHero, createRecentSearches, createLibraryView, createDetailView };
})();
