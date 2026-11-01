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

  // A focused button (or anything inside one) owns Space/Enter activation.
  function isButtonTarget(el) {
    if (!el) return false;
    if (el.tagName === 'BUTTON') return true;
    return typeof el.closest === 'function' && !!el.closest('button, [role="button"]');
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
    let pressed = false;        // pointer held on the container
    let keyboardIntent = false; // last interaction was keyboard (Tab)
    let focusWithin = false;    // keyboard focus is locked inside the container
    let disposed = false;

    /* ---- visibility ---- */
    function clearHide() { if (hideTimer != null) { clearTimeout(hideTimer); hideTimer = null; } }
    function clearClick() { if (clickTimer != null) { clearTimeout(clickTimer); clickTimer = null; } }
    function canHide() {
      return !disposed && status.value === 'playing' && !pressed && !focusWithin
        && !openMenu.value && operation.value == null;
    }
    function scheduleHide() {
      if (disposed) return;
      clearHide();
      if (!canHide()) return;
      hideTimer = setTimeout(() => {
        hideTimer = null;
        // Re-validate: state may have changed while the timer was pending.
        if (canHide()) controlsVisible.value = false;
      }, HIDE_DELAY);
    }
    function show() {
      if (disposed) return;
      controlsVisible.value = true;
      clearHide();
    }
    function activity() {
      if (disposed) return;
      show();
      if (status.value === 'playing') scheduleHide();
    }

    /* ---- engine state ---- */
    function begin(kind) {
      if (disposed) return;
      if (kind && kind !== 'loading') operation.value = kind;
      status.value = 'loading';
      error.value = null;
      openMenu.value = null;
      show();
    }
    function acceptState(state) {
      if (disposed || !state) return;
      const prev = status.value;
      let next = prev;
      if (state.loading === true) {
        next = 'loading';
      } else if (state.loading === false && prev === 'loading') {
        next = state.playing ? 'playing' : 'paused';
      }
      if (next !== 'loading' && state.playing != null) {
        next = state.playing ? 'playing' : 'paused';
      }
      if (state.ended || state.mode === 'ended') next = 'ended';
      status.value = next;
      if (next === 'playing') {
        // Only a transition into playing (re)arms the hide timer; repeated
        // time updates that report playing must not keep rescheduling it.
        if (prev !== 'playing') scheduleHide();
      } else {
        show();
      }
    }
    function fail(kind, message, retryContext) {
      if (disposed) return;
      status.value = 'error';
      error.value = { kind, message, retryContext: retryContext || null };
      operation.value = null;
      openMenu.value = null;
      show();
    }
    function end() {
      if (disposed) return;
      operation.value = null;
      activity();
    }

    /* ---- menus ---- */
    function toggleMenu(name) {
      if (disposed) return;
      openMenu.value = openMenu.value === name ? null : name;
      activity();
    }
    function closeMenu() {
      if (disposed || openMenu.value == null) return false;
      openMenu.value = null;
      activity();
      return true;
    }
    function isMenuOpen(name) { return openMenu.value === name; }

    /* ---- screen click: single = pause, double = fullscreen ---- */
    function isNarrow() { return typeof window !== 'undefined' && window.innerWidth < 768; }
    function onScreenClick(event) {
      if (disposed) return;
      const target = event.target;
      if (target && target.closest && target.closest('button, a, input, select, textarea, [role="menu"], .eio-menu, .eio-play-top, .eio-play-bottom')) return;
      // A blank stage click dismisses an open menu first, so that dismissing
      // (e.g.) the rate menu does not also pause playback.
      if (closeMenu()) return;
      if (isNarrow()) { activity(); return; }
      if (clickTimer != null) {
        clearClick();
        deps.actions?.toggleFullscreen?.();
        return;
      }
      clickTimer = setTimeout(() => {
        clickTimer = null;
        deps.actions?.togglePause?.();
      }, CLICK_DELAY);
    }

    /* ---- keyboard ---- */
    function onKeydown(event) {
      if (disposed) return;
      if (deps.isActive && !deps.isActive()) return;
      if (event.key === 'Tab') {
        keyboardIntent = true;
        const active = typeof document !== 'undefined' ? document.activeElement : null;
        if (active && containerEl && typeof containerEl.contains === 'function' && containerEl.contains(active)) {
          focusWithin = true;
        }
        activity();
      }
      if (event.ctrlKey || event.altKey || event.metaKey) return;
      if (isEditable(event.target)) return;
      const key = event.key;
      // Leave Space/Enter on a focused button to the browser's native activation
      // (it dispatches a click) instead of treating it as a global pause.
      if ((key === ' ' || key === 'Enter') && isButtonTarget(event.target)) return;
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

    /* ---- pointer ---- */
    function onFullscreenChange() { activity(); }
    function onVisibility() {
      if (disposed) return;
      if (typeof document !== 'undefined' && document.hidden) clearHide();
      else scheduleHide();
    }
    function onMove() { activity(); }
    function onEnter() { activity(); }
    function onPointerDown() {
      if (disposed) return;
      // A pointer interaction revokes keyboard focus intent, including a
      // focusWithin lock left over from a previous Tab-driven focus.
      keyboardIntent = false;
      focusWithin = false;
      pressed = true;
      activity();
    }
    function onPointerUp() {
      if (disposed) return;
      if (!pressed) return;
      pressed = false;
      activity();
    }

    /* ---- focus: keyboard intent only ---- */
    function onFocusIn() {
      if (disposed || !keyboardIntent) return;
      focusWithin = true;
      show();
    }
    function onFocusOut(event) {
      if (disposed) return;
      const next = event && event.relatedTarget;
      if (next && containerEl && typeof containerEl.contains === 'function' && containerEl.contains(next)) return;
      if (!focusWithin) return;
      focusWithin = false;
      keyboardIntent = false;
      scheduleHide();
    }

    /* ---- listener lifecycle ---- */
    function detach() {
      if (containerEl) {
        containerEl.removeEventListener('pointermove', onMove);
        containerEl.removeEventListener('pointerdown', onPointerDown);
        containerEl.removeEventListener('pointerenter', onEnter);
        containerEl.removeEventListener('click', onScreenClick);
        containerEl.removeEventListener('focusin', onFocusIn);
        containerEl.removeEventListener('focusout', onFocusOut);
        containerEl = null;
      }
      if (typeof window !== 'undefined') window.removeEventListener('keydown', onKeydown);
      if (typeof document !== 'undefined') {
        document.removeEventListener('pointerup', onPointerUp);
        document.removeEventListener('pointercancel', onPointerUp);
        document.removeEventListener('fullscreenchange', onFullscreenChange);
        document.removeEventListener('visibilitychange', onVisibility);
      }
      pressed = false;
      keyboardIntent = false;
      focusWithin = false;
      clearHide(); clearClick();
    }

    function attach() {
      if (disposed) return;
      detach();
      containerEl = deps.getContainer?.() || null;
      if (containerEl) {
        containerEl.addEventListener('pointermove', onMove);
        containerEl.addEventListener('pointerdown', onPointerDown);
        containerEl.addEventListener('pointerenter', onEnter);
        containerEl.addEventListener('click', onScreenClick);
        containerEl.addEventListener('focusin', onFocusIn);
        containerEl.addEventListener('focusout', onFocusOut);
      }
      if (typeof window !== 'undefined') window.addEventListener('keydown', onKeydown);
      if (typeof document !== 'undefined') {
        document.addEventListener('pointerup', onPointerUp);
        document.addEventListener('pointercancel', onPointerUp);
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
      pressed = false;
      keyboardIntent = false;
      focusWithin = false;
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
