/* Emby-in-One Web client — player control-layer state (V1.4.4-rc2).
 *
 * Owns only presentation/state for the player page: status, error card,
 * in-flight operation, control visibility, open menus, screen-click and
 * keyboard handling. It never creates, replaces or destroys player engine
 * DOM — the engine (player-libmedia.js) keeps ownership of its host.
 */
(function () {
  'use strict';

  const HIDE_DELAY = 3000;
  const CLICK_DELAY = 220;

  function isEditable(el) {
    if (!el) return false;
    const tag = el.tagName;
    if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return true;
    return !!el.isContentEditable;
  }

  function create(deps = {}) {
    const { ref, computed } = window.Vue;
    const status = ref('idle');       // idle|loading|playing|paused|ended|error
    const error = ref(null);          // { kind, message, retryContext }
    const operation = ref(null);      // null|version|audio
    const controlsVisible = ref(true);
    const openMenu = ref(null);       // null|version|audio|subtitle

    const busy = computed(() => status.value === 'loading' || operation.value != null);

    let containerEl = null;
    let hideTimer = null;
    let clickTimer = null;
    let hovering = false;
    let focusWithin = false;
    let disposed = false;

    /* ---- visibility ---- */
    function clearHide() { if (hideTimer != null) { clearTimeout(hideTimer); hideTimer = null; } }
    function canHide() {
      return !disposed && status.value === 'playing' && !hovering && !focusWithin
        && !openMenu.value && operation.value == null;
    }
    function scheduleHide() {
      clearHide();
      if (canHide()) hideTimer = setTimeout(() => { controlsVisible.value = false; }, HIDE_DELAY);
    }
    function show() { controlsVisible.value = true; clearHide(); }
    function activity() {
      show();
      if (status.value === 'playing') scheduleHide();
    }

    /* ---- engine state ---- */
    function begin(kind) {
      if (kind && kind !== 'loading') operation.value = kind;
      status.value = 'loading';
      error.value = null;
      openMenu.value = null;
      show();
    }
    function acceptState(state) {
      if (!state) return;
      if (state.loading === true) {
        status.value = 'loading'; show();
      } else if (state.loading === false && status.value === 'loading') {
        status.value = state.playing ? 'playing' : 'paused';
      }
      if (status.value !== 'loading' && state.playing != null) {
        status.value = state.playing ? 'playing' : 'paused';
      }
      if (state.ended || state.mode === 'ended') status.value = 'ended';
      if (status.value === 'playing') scheduleHide(); else show();
    }
    function fail(kind, message, retryContext) {
      status.value = 'error';
      error.value = { kind, message, retryContext: retryContext || null };
      operation.value = null;
      openMenu.value = null;
      show();
    }
    function end() { operation.value = null; }

    /* ---- menus ---- */
    function toggleMenu(name) { openMenu.value = openMenu.value === name ? null : name; if (openMenu.value) show(); }
    function closeMenu() { if (openMenu.value != null) { openMenu.value = null; return true; } return false; }
    function isMenuOpen(name) { return openMenu.value === name; }

    /* ---- screen click: single = pause, double = fullscreen ---- */
    function isNarrow() { return typeof window !== 'undefined' && window.innerWidth < 768; }
    function onScreenClick(event) {
      const target = event.target;
      if (target && target.closest && target.closest('button, a, input, select, textarea, [role="menu"], .eio-menu, .eio-play-top, .eio-play-bottom')) return;
      if (isNarrow()) { activity(); return; }
      if (clickTimer != null) {
        clearTimeout(clickTimer); clickTimer = null;
        deps.actions?.toggleFullscreen?.();
        return;
      }
      clickTimer = setTimeout(() => { clickTimer = null; deps.actions?.togglePause?.(); }, CLICK_DELAY);
    }

    /* ---- keyboard ---- */
    function onKeydown(event) {
      if (disposed || deps.isActive && !deps.isActive()) return;
      if (event.ctrlKey || event.altKey || event.metaKey) return;
      if (isEditable(event.target)) return;
      const key = event.key;
      const isToggle = key === ' ' || key === 'k' || key === 'K' || key === 'm' || key === 'M' || key === 'f' || key === 'F' || key === 'c' || key === 'C';
      if (event.repeat && isToggle) { event.preventDefault(); return; }
      const run = fn => { event.preventDefault(); activity(); fn(); };
      switch (key) {
        case ' ':
        case 'k': case 'K': run(() => deps.actions?.togglePause?.()); break;
        case 'ArrowLeft': run(() => deps.actions?.seekBy?.(-10)); break;
        case 'ArrowRight': run(() => deps.actions?.seekBy?.(10)); break;
        case 'j': case 'J': run(() => deps.actions?.seekBy?.(-10)); break;
        case 'l': case 'L': run(() => deps.actions?.seekBy?.(10)); break;
        case 'ArrowUp': run(() => deps.actions?.adjustVolume?.(0.05)); break;
        case 'ArrowDown': run(() => deps.actions?.adjustVolume?.(-0.05)); break;
        case 'm': case 'M': run(() => deps.actions?.toggleMute?.()); break;
        case 'f': case 'F': run(() => deps.actions?.toggleFullscreen?.()); break;
        case 'c': case 'C': run(() => deps.actions?.toggleSubtitle?.()); break;
        case 'Escape':
          if (closeMenu()) { event.preventDefault(); }
          else if (typeof document !== 'undefined' && document.fullscreenElement) { /* browser exits */ }
          else { event.preventDefault(); deps.actions?.exit?.(); }
          break;
        default: break;
      }
    }

    function onFullscreenChange() { show(); }
    function onVisibility() { if (typeof document !== 'undefined' && document.hidden) clearHide(); else scheduleHide(); }
    function onMove() { activity(); }
    function onPointerDown() { activity(); }
    function onEnter() { hovering = true; show(); }
    function onLeave() { hovering = false; scheduleHide(); }
    function onFocusIn() { focusWithin = true; show(); }
    function onFocusOut() { focusWithin = false; scheduleHide(); }

    /* ---- listener lifecycle ---- */
    function detach() {
      if (containerEl) {
        containerEl.removeEventListener('pointermove', onMove);
        containerEl.removeEventListener('pointerdown', onPointerDown);
        containerEl.removeEventListener('pointerenter', onEnter);
        containerEl.removeEventListener('pointerleave', onLeave);
        containerEl.removeEventListener('click', onScreenClick);
        containerEl.removeEventListener('focusin', onFocusIn);
        containerEl.removeEventListener('focusout', onFocusOut);
        containerEl = null;
      }
      if (typeof window !== 'undefined') window.removeEventListener('keydown', onKeydown);
      if (typeof document !== 'undefined') {
        document.removeEventListener('fullscreenchange', onFullscreenChange);
        document.removeEventListener('visibilitychange', onVisibility);
      }
      clearHide(); clearClick();
    }
    function clearClick() { if (clickTimer != null) { clearTimeout(clickTimer); clickTimer = null; } }

    function attach() {
      detach();
      containerEl = deps.getContainer?.() || null;
      if (containerEl) {
        containerEl.addEventListener('pointermove', onMove);
        containerEl.addEventListener('pointerdown', onPointerDown);
        containerEl.addEventListener('pointerenter', onEnter);
        containerEl.addEventListener('pointerleave', onLeave);
        containerEl.addEventListener('click', onScreenClick);
        containerEl.addEventListener('focusin', onFocusIn);
        containerEl.addEventListener('focusout', onFocusOut);
      }
      if (typeof window !== 'undefined') window.addEventListener('keydown', onKeydown);
      if (typeof document !== 'undefined') {
        document.addEventListener('fullscreenchange', onFullscreenChange);
        document.addEventListener('visibilitychange', onVisibility);
      }
      activity();
    }

    function reset() {
      status.value = 'idle';
      error.value = null;
      operation.value = null;
      openMenu.value = null;
      controlsVisible.value = true;
      clearHide(); clearClick();
    }

    function dispose() {
      disposed = true;
      detach();
      reset();
    }

    return {
      status, error, operation, controlsVisible, openMenu, busy,
      begin, acceptState, fail, end,
      toggleMenu, closeMenu, isMenuOpen,
      show, activity, scheduleHide,
      attach, detach, reset, dispose,
    };
  }

  window.EIOPlayerUI = { create };
})();
