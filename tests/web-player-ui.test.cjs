'use strict';
/* Direct coverage for public/web/player-ui.js — the player control-layer
 * state module. The engine tests (web-player.test.cjs) only load
 * player-libmedia.js; this file drives EIOPlayerUI itself with captured
 * timers (the 3s auto-hide and 220ms click-delay fire deterministically)
 * and dispatch stubs for container / window / document events.
 */
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const HIDE_DELAY = 3000;
const CLICK_DELAY = 220;

function harness(options = {}) {
  const actions = [];
  const deps = {
    getContainer: () => container,
    actions: {
      togglePause: () => actions.push('pause'),
      toggleFullscreen: () => actions.push('fullscreen'),
      seekBy: delta => actions.push('seek:' + delta),
      adjustVolume: delta => actions.push('vol:' + delta),
      toggleMute: () => actions.push('mute'),
      toggleSubtitle: () => actions.push('subtitle'),
      exit: () => actions.push('exit'),
    },
    isActive: () => options.inactive ? false : true,
  };

  // Captured timers: tests fire them by delay instead of waiting.
  const timers = [];
  const setTimeout = (fn, delay) => {
    const timer = { fn, delay: delay || 0, cleared: false, fired: false };
    timers.push(timer);
    return timer;
  };
  const clearTimeout = timer => { if (timer) timer.cleared = true; };
  const fire = delay => {
    for (let i = timers.length - 1; i >= 0; i--) {
      const timer = timers[i];
      if (timer.delay === delay && !timer.cleared && !timer.fired) {
        timer.fired = true;
        timer.fn();
        return timer;
      }
    }
    return undefined;
  };

  function makeTarget(name) {
    const listeners = [];
    return {
      name,
      listeners,
      addEventListener: (type, fn) => listeners.push({ type, fn }),
      removeEventListener: (type, fn) => {
        const index = listeners.findIndex(l => l.type === type && l.fn === fn);
        if (index >= 0) listeners.splice(index, 1);
      },
      dispatch: (type, event) => {
        for (const l of [...listeners]) if (l.type === type) l.fn(event);
      },
    };
  }

  const container = makeTarget('container');
  const win = makeTarget('window');
  const doc = makeTarget('document');
  win.innerWidth = options.width || 1440;
  doc.hidden = false;
  doc.fullscreenElement = null;

  const context = {
    console, Math, JSON, Promise, setTimeout, clearTimeout,
    window: win, document: doc,
    Vue: {
      ref: value => ({ value }),
      computed: fn => ({ get value() { return fn(); } }),
    },
  };
  win.Vue = context.Vue;
  vm.createContext(context);
  vm.runInContext(fs.readFileSync(path.join(__dirname, '..', 'public', 'web', 'player-ui.js'), 'utf8'), context);

  const ui = win.EIOPlayerUI.create(deps);
  ui.attach();
  return { ui, deps, actions, timers, fire, container, win, doc };
}

function clickEvent(target) {
  return { target: target || { closest: () => null } };
}

function keyEvent(key, overrides = {}) {
  const event = {
    key,
    ctrlKey: false, altKey: false, metaKey: false, repeat: false,
    target: { tagName: 'DIV', isContentEditable: false },
    prevented: false,
    preventDefault() { this.prevented = true; },
  };
  return Object.assign(event, overrides);
}

test('busy reflects loading status and in-flight operations', () => {
  const { ui } = harness();
  assert.equal(ui.busy.value, false);
  ui.begin('version');
  assert.equal(ui.status.value, 'loading');
  assert.equal(ui.operation.value, 'version');
  assert.equal(ui.busy.value, true);
  ui.acceptState({ loading: false, playing: true });
  assert.equal(ui.status.value, 'playing');
  assert.equal(ui.busy.value, true); // operation still in flight
  ui.end();
  assert.equal(ui.operation.value, null);
  assert.equal(ui.busy.value, false);
});

test('acceptState maps engine states onto the status machine', () => {
  const { ui } = harness();
  ui.acceptState(null);
  assert.equal(ui.status.value, 'idle');
  ui.acceptState({ loading: true });
  assert.equal(ui.status.value, 'loading');
  ui.acceptState({ loading: false, playing: true });
  assert.equal(ui.status.value, 'playing');
  ui.acceptState({ playing: false });
  assert.equal(ui.status.value, 'paused');
  ui.acceptState({ mode: 'ended' });
  assert.equal(ui.status.value, 'ended');
});

test('fail sets the error card and clears transient state', () => {
  const { ui } = harness();
  ui.begin('audio');
  ui.toggleMenu('version');
  ui.fail('playback', 'boom', { item: 'x' });
  assert.equal(ui.status.value, 'error');
  assert.equal(ui.error.value.kind, 'playback');
  assert.equal(ui.error.value.message, 'boom');
  assert.equal(ui.error.value.retryContext.item, 'x');
  assert.equal(ui.operation.value, null);
  assert.equal(ui.openMenu.value, null);
  assert.equal(ui.controlsVisible.value, true);
  ui.fail('playback', 'no-retry');
  assert.equal(ui.error.value.retryContext, null);
});

test('menus toggle and Escape closes the menu before exiting', () => {
  const { ui, win, actions } = harness();
  ui.toggleMenu('version');
  assert.equal(ui.openMenu.value, 'version');
  assert.equal(ui.isMenuOpen('version'), true);
  ui.toggleMenu('version');
  assert.equal(ui.openMenu.value, null);
  ui.toggleMenu('audio');
  assert.equal(ui.openMenu.value, 'audio');
  assert.equal(ui.closeMenu(), true);
  assert.equal(ui.openMenu.value, null);
  assert.equal(ui.closeMenu(), false);

  ui.toggleMenu('subtitle');
  const escape = keyEvent('Escape');
  win.dispatch('keydown', escape);
  assert.equal(ui.openMenu.value, null);
  assert.deepEqual(actions, []); // menu close consumed the Escape, no exit

  win.dispatch('keydown', keyEvent('Escape'));
  assert.deepEqual(actions, ['exit']);
});

test('controls auto-hide only while playing and not hovered', () => {
  const { ui, fire, container } = harness();
  ui.acceptState({ playing: false }); // paused
  ui.activity();
  assert.equal(fire(HIDE_DELAY), undefined); // no hide timer while paused
  assert.equal(ui.controlsVisible.value, true);

  ui.acceptState({ playing: true });
  ui.activity();
  assert.ok(fire(HIDE_DELAY), 'hide timer scheduled while playing');
  assert.equal(ui.controlsVisible.value, false);

  ui.activity();
  assert.equal(ui.controlsVisible.value, true);
  container.dispatch('pointerenter', {}); // hovering blocks hiding
  ui.activity();
  assert.equal(fire(HIDE_DELAY), undefined);
  assert.equal(ui.controlsVisible.value, true);
  container.dispatch('pointerleave', {}); // leaving re-arms the hide
  assert.ok(fire(HIDE_DELAY));
  assert.equal(ui.controlsVisible.value, false);
});

test('screen click: single pauses after the delay, double goes fullscreen', () => {
  const { container, fire, actions } = harness();
  container.dispatch('click', clickEvent());
  assert.deepEqual(actions, []); // not before the click delay elapses
  fire(CLICK_DELAY);
  assert.deepEqual(actions, ['pause']);

  container.dispatch('click', clickEvent());
  container.dispatch('click', clickEvent()); // second click cancels the pause
  fire(CLICK_DELAY);
  assert.deepEqual(actions, ['pause', 'fullscreen']);
});

test('screen click ignores control surfaces and narrow screens', () => {
  const wide = harness();
  wide.container.dispatch('click', clickEvent({ closest: () => ({}) })); // inside a button
  wide.fire(CLICK_DELAY);
  assert.deepEqual(wide.actions, []);

  const narrow = harness({ width: 500 });
  narrow.container.dispatch('click', clickEvent());
  narrow.fire(CLICK_DELAY);
  assert.deepEqual(narrow.actions, []); // narrow screens only wake controls
  assert.equal(narrow.ui.controlsVisible.value, true);
});

test('keyboard shortcuts map to player actions and prevent default', () => {
  const { win, actions } = harness();
  const cases = [
    [' ', 'pause'], ['k', 'pause'], ['K', 'pause'],
    ['ArrowLeft', 'seek:-10'], ['ArrowRight', 'seek:10'], ['j', 'seek:-10'], ['l', 'seek:10'],
    ['ArrowUp', 'vol:0.05'], ['ArrowDown', 'vol:-0.05'],
    ['m', 'mute'], ['M', 'mute'], ['f', 'fullscreen'], ['c', 'subtitle'],
  ];
  for (const [key, want] of cases) {
    const event = keyEvent(key);
    win.dispatch('keydown', event);
    assert.equal(event.prevented, true, key + ' must preventDefault');
    assert.equal(actions.at(-1), want, key + ' -> ' + want);
  }
});

test('keyboard handling skips modifiers, editable targets, repeats and inactive pages', () => {
  const { win, actions, ui } = harness();
  const ignored = [
    keyEvent(' ', { ctrlKey: true }),
    keyEvent('k', { altKey: true }),
    keyEvent('f', { metaKey: true }),
    keyEvent(' ', { target: { tagName: 'INPUT' } }),
    keyEvent(' ', { target: { tagName: 'TEXTAREA' } }),
  ];
  for (const event of ignored) {
    win.dispatch('keydown', event);
    assert.equal(event.prevented, false);
  }
  assert.deepEqual(actions, []);

  // A held toggle key still suppresses the browser default (no scroll, no
  // button activation) but must not re-fire the action.
  const held = keyEvent('k', { repeat: true });
  win.dispatch('keydown', held);
  assert.equal(held.prevented, true);
  assert.deepEqual(actions, []);

  ui.dispose();
  win.dispatch('keydown', keyEvent(' ')); // disposed ignores keys
  assert.deepEqual(actions, []);
});

test('attach registers listeners, detach and dispose remove them', () => {
  const { ui, container, win, doc } = harness();
  const containerEvents = container.listeners.map(l => l.type).sort();
  assert.deepEqual(containerEvents, ['click', 'focusin', 'focusout', 'pointerdown', 'pointerenter', 'pointerleave', 'pointermove']);
  assert.deepEqual(win.listeners.map(l => l.type), ['keydown']);
  assert.deepEqual(doc.listeners.map(l => l.type).sort(), ['fullscreenchange', 'visibilitychange']);

  ui.detach();
  assert.equal(container.listeners.length, 0);
  assert.equal(win.listeners.length, 0);
  assert.equal(doc.listeners.length, 0);

  ui.attach();
  assert.equal(container.listeners.length, 7);
  ui.dispose();
  assert.equal(container.listeners.length, 0);
  assert.equal(ui.status.value, 'idle');
  assert.equal(ui.controlsVisible.value, true);
});

test('reset restores the initial state without detaching listeners', () => {
  const { ui, container } = harness();
  ui.begin('version');
  ui.fail('playback', 'boom');
  ui.toggleMenu('audio');
  ui.reset();
  assert.equal(ui.status.value, 'idle');
  assert.equal(ui.error.value, null);
  assert.equal(ui.operation.value, null);
  assert.equal(ui.openMenu.value, null);
  assert.equal(ui.controlsVisible.value, true);
  assert.equal(container.listeners.length, 7); // reset keeps the DOM attached
});
