'use strict';
/* Control-visibility contract for public/web/player-ui.js.
 *
 * These fakes are deliberately inert: the clock only runs callbacks created
 * through its own setTimeout, and the event targets only notify their own
 * listeners. Nothing here emulates a browser / Vue / Chromium fixture; the
 * tests pin the module's own rules for when controls may hide.
 */
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const HIDE_DELAY = 3000;
const CLICK_DELAY = 220;

function createClock() {
  let now = 0;
  let seq = 0;
  const timers = new Set();
  return {
    setTimeout(fn, delay) {
      const timer = {
        id: ++seq, fn, delay: delay || 0, due: now + (delay || 0),
        cleared: false, fired: false,
      };
      timers.add(timer);
      return timer;
    },
    clearTimeout(timer) {
      if (!timer) return;
      timer.cleared = true;
      timers.delete(timer);
    },
    pending(delay) {
      const all = [...timers].filter(t => !t.cleared && !t.fired);
      return delay == null ? all : all.filter(t => t.delay === delay);
    },
    advance(ms) {
      const target = now + ms;
      for (;;) {
        const due = [...timers].filter(t => !t.cleared && !t.fired && t.due <= target)
          .sort((a, b) => a.due - b.due || a.id - b.id)[0];
        if (!due) break;
        now = due.due; due.fired = true; timers.delete(due); due.fn();
      }
      now = target;
    },
  };
}

function makeTarget(name) {
  const listeners = [];
  return {
    name, listeners,
    addEventListener(type, fn) { listeners.push({ type, fn }); },
    removeEventListener(type, fn) {
      const index = listeners.findIndex(l => l.type === type && l.fn === fn);
      if (index >= 0) listeners.splice(index, 1);
    },
    dispatch(type, event) {
      for (const listener of [...listeners]) if (listener.type === type) listener.fn(event);
    },
  };
}

function harness(options = {}) {
  const clock = createClock();
  const actions = [];
  const container = makeTarget('container');
  const win = makeTarget('window');
  const doc = makeTarget('document');

  const inside = new Set();
  const body = { tagName: 'BODY' };
  const child = { tagName: 'BUTTON' };
  inside.add(child);
  container.contains = el => el === container || inside.has(el);
  win.innerWidth = options.width || 1440;
  doc.hidden = false;
  doc.fullscreenElement = null;
  doc.activeElement = body;

  const context = {
    console, Math, JSON, Promise,
    setTimeout: clock.setTimeout, clearTimeout: clock.clearTimeout,
    window: win, document: doc,
    Vue: { ref: value => ({ value }), computed: fn => ({ get value() { return fn(); } }) },
  };
  win.Vue = context.Vue;
  vm.createContext(context);
  vm.runInContext(fs.readFileSync(path.join(__dirname, '..', 'public', 'web', 'player-ui.js'), 'utf8'), context);

  const ui = win.EIOPlayerUI.create({
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
    isActive: () => (options.inactive ? false : true),
  });
  ui.attach();
  return { ui, actions, clock, container, win, doc, child, body };
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

function focusEvent(target, relatedTarget) {
  return { target, relatedTarget };
}

test('harness fakes are inert contracts, not a browser', () => {
  const { container, clock, child, body } = harness();
  assert.equal(typeof globalThis.document, 'undefined');
  assert.equal(typeof globalThis.window, 'undefined');
  assert.equal(typeof container.contains, 'function');
  assert.equal(container.contains(child), true);
  assert.equal(container.contains(body), false);
  const tab = keyEvent('Tab');
  assert.equal(typeof tab.preventDefault, 'function');
  tab.preventDefault();
  assert.equal(tab.prevented, true);
  let seen;
  const onFocus = event => { seen = event; };
  container.addEventListener('focusin', onFocus);
  container.dispatch('focusin', focusEvent(child, body));
  assert.equal(seen.relatedTarget, body);
  container.removeEventListener('focusin', onFocus);
  let ran = 0;
  clock.setTimeout(() => { ran += 1; }, 10);
  clock.advance(9); assert.equal(ran, 0);
  clock.advance(1); assert.equal(ran, 1);
  clock.clearTimeout(clock.setTimeout(() => { ran += 1; }, 10));
  clock.advance(50); assert.equal(ran, 1);
});

test('Space/Enter on a rate button stay native; blank Space and K still pause', () => {
  const { ui, actions, win } = harness();
  ui.acceptState({ playing: true });
  const rateButton = { tagName: 'BUTTON', isContentEditable: false };
  for (const key of [' ', 'Enter']) {
    const event = keyEvent(key, { target: rateButton });
    win.dispatch('keydown', event);
    assert.equal(event.prevented, false, key);
  }
  // No pause is triggered from keydown: the browser's native click does the activation.
  assert.deepEqual(actions, []);
  // A descendant of a role=button control is resolved through closest().
  const nested = {
    tagName: 'SPAN', isContentEditable: false,
    closest: selector => (/button/.test(selector) ? { tagName: 'BUTTON' } : null),
  };
  const nestedSpace = keyEvent(' ', { target: nested });
  win.dispatch('keydown', nestedSpace);
  assert.equal(nestedSpace.prevented, false);
  assert.deepEqual(actions, []);
  // A target without closest() is ordinary page content and still pauses.
  const plainSpace = keyEvent(' ', { target: { tagName: 'DIV', isContentEditable: false } });
  win.dispatch('keydown', plainSpace);
  assert.equal(plainSpace.prevented, true);
  assert.deepEqual(actions, ['pause']);
  // Other shortcuts are not suppressed by button focus.
  const k = keyEvent('k', { target: rateButton });
  win.dispatch('keydown', k);
  assert.equal(k.prevented, true);
  assert.deepEqual(actions, ['pause', 'pause']);
  // Escape still closes an open menu from a focused rate button.
  ui.toggleMenu('rate');
  const escape = keyEvent('Escape', { target: rateButton });
  win.dispatch('keydown', escape);
  assert.equal(ui.openMenu.value, null);
  assert.equal(escape.prevented, true);
});

test('repeated time updates for 3s do not reschedule the hide timer', () => {
  const { ui, clock } = harness();
  ui.acceptState({ playing: true });
  const [timer] = clock.pending(HIDE_DELAY);
  assert.ok(timer);
  assert.equal(ui.controlsVisible.value, true);
  for (let i = 0; i < 29; i += 1) {
    clock.advance(100);
    ui.acceptState({ playing: true, position: i / 10 });
  }
  assert.deepEqual(clock.pending(HIDE_DELAY), [timer]);
  assert.equal(ui.controlsVisible.value, true);
  clock.advance(99);
  assert.equal(ui.controlsVisible.value, true);
  clock.advance(1);
  assert.equal(ui.controlsVisible.value, false);
});

test('stationary pointer and mouse focus do not pin controls visible', () => {
  const { ui, clock, container, doc, actions, child, body } = harness();
  ui.acceptState({ playing: true });
  container.dispatch('pointerenter', {});
  container.dispatch('pointermove', {});
  clock.advance(HIDE_DELAY - 1);
  assert.equal(ui.controlsVisible.value, true);
  clock.advance(1);
  assert.equal(ui.controlsVisible.value, false); // pointer stayed, still hides
  ui.activity();
  container.dispatch('pointerdown', { target: child });
  container.dispatch('focusin', focusEvent(child, body)); // mouse focus, no Tab
  doc.dispatch('pointerup', { target: child });
  assert.equal(clock.pending(HIDE_DELAY).length, 1);
  clock.advance(HIDE_DELAY);
  assert.equal(ui.controlsVisible.value, false);
  assert.deepEqual(actions, []);
});

test('Tab locks controls; pointerdown unlocks; a later Tab wakes again', () => {
  const { ui, clock, container, doc, win, child, body } = harness();
  ui.acceptState({ playing: true });
  clock.advance(HIDE_DELAY);
  assert.equal(ui.controlsVisible.value, false);
  win.dispatch('keydown', keyEvent('Tab')); // wakes, focus follows inside
  assert.equal(ui.controlsVisible.value, true);
  container.dispatch('focusin', focusEvent(child, body));
  ui.activity();
  assert.equal(clock.pending(HIDE_DELAY).length, 0); // locked
  clock.advance(HIDE_DELAY * 2);
  assert.equal(ui.controlsVisible.value, true);
  container.dispatch('focusout', focusEvent(child, child)); // move within
  clock.advance(HIDE_DELAY * 2);
  assert.equal(ui.controlsVisible.value, true);
  container.dispatch('pointerdown', { target: child }); // pointer revokes lock
  assert.equal(ui.controlsVisible.value, true);
  doc.dispatch('pointerup', { target: child });
  assert.equal(clock.pending(HIDE_DELAY).length, 1);
  clock.advance(HIDE_DELAY);
  assert.equal(ui.controlsVisible.value, false);
  win.dispatch('keydown', keyEvent('Tab'));
  container.dispatch('focusin', focusEvent(child, body));
  assert.equal(ui.controlsVisible.value, true);
  assert.equal(clock.pending(HIDE_DELAY).length, 0);
  container.dispatch('focusout', focusEvent(child, body));
  assert.equal(clock.pending(HIDE_DELAY).length, 1);
  clock.advance(HIDE_DELAY);
  assert.equal(ui.controlsVisible.value, false);
});

test('container press locks; document up/cancel releases without unrelated wakes', () => {
  const { ui, clock, container, doc } = harness();
  ui.acceptState({ playing: true });
  container.dispatch('pointerdown', {});
  container.dispatch('pointermove', {});
  clock.advance(HIDE_DELAY * 3);
  assert.equal(ui.controlsVisible.value, true);
  assert.equal(clock.pending(HIDE_DELAY).length, 0);
  doc.dispatch('pointerup', {});
  assert.equal(clock.pending(HIDE_DELAY).length, 1);
  clock.advance(HIDE_DELAY);
  assert.equal(ui.controlsVisible.value, false);
  container.dispatch('pointerdown', {});
  clock.advance(HIDE_DELAY * 3);
  assert.equal(ui.controlsVisible.value, true);
  doc.dispatch('pointercancel', {});
  clock.advance(HIDE_DELAY);
  assert.equal(ui.controlsVisible.value, false);
  doc.dispatch('pointerup', {}); // no press: must not wake hidden controls
  doc.dispatch('pointercancel', {});
  assert.equal(ui.controlsVisible.value, false);
  assert.equal(clock.pending(HIDE_DELAY).length, 0);
});

test('closing a menu and fullscreen changes re-arm the 3s hide', () => {
  const { ui, clock, doc, win, container } = harness();
  assert.equal(doc.fullscreenElement, null);
  ui.acceptState({ playing: true });
  ui.toggleMenu('rate');
  clock.advance(HIDE_DELAY * 2);
  assert.equal(ui.controlsVisible.value, true); // open menu blocks hiding
  assert.equal(ui.closeMenu(), true);
  assert.equal(clock.pending(HIDE_DELAY).length, 1);
  clock.advance(HIDE_DELAY - 1);
  assert.equal(ui.controlsVisible.value, true);
  clock.advance(1);
  assert.equal(ui.controlsVisible.value, false);
  ui.activity();
  doc.fullscreenElement = container;
  doc.dispatch('fullscreenchange', {});
  assert.equal(clock.pending(HIDE_DELAY).length, 1);
  clock.advance(HIDE_DELAY);
  assert.equal(ui.controlsVisible.value, false);
  ui.toggleMenu('subtitle');
  assert.equal(clock.pending(HIDE_DELAY).length, 0);
  win.dispatch('keydown', keyEvent('Escape'));
  assert.equal(ui.openMenu.value, null);
  assert.equal(clock.pending(HIDE_DELAY).length, 1);
});

test('paused, loading, error and ended states never auto-hide', () => {
  const { ui, clock } = harness();
  ui.acceptState({ playing: true });
  assert.equal(clock.pending(HIDE_DELAY).length, 1);
  ui.acceptState({ playing: false });
  assert.equal(clock.pending(HIDE_DELAY).length, 0);
  clock.advance(HIDE_DELAY * 2);
  assert.equal(ui.controlsVisible.value, true);
  ui.acceptState({ loading: true });
  clock.advance(HIDE_DELAY * 2);
  assert.equal(ui.controlsVisible.value, true);
  ui.acceptState({ loading: false, playing: true });
  assert.equal(clock.pending(HIDE_DELAY).length, 1);
  ui.fail('playback', 'boom');
  assert.equal(clock.pending(HIDE_DELAY).length, 0);
  clock.advance(HIDE_DELAY * 2);
  assert.equal(ui.controlsVisible.value, true);
  ui.acceptState({ playing: true });
  assert.equal(clock.pending(HIDE_DELAY).length, 1);
  ui.acceptState({ mode: 'ended' });
  assert.equal(clock.pending(HIDE_DELAY).length, 0);
  clock.advance(HIDE_DELAY * 2);
  assert.equal(ui.controlsVisible.value, true);
  ui.acceptState({ playing: true });
  ui.begin('rate'); // loading + in-flight operation
  assert.equal(clock.pending(HIDE_DELAY).length, 0);
  clock.advance(HIDE_DELAY * 2);
  assert.equal(ui.controlsVisible.value, true);
});

test('a stale hide callback cannot hide controls once playback is not playing', () => {
  const { ui, clock } = harness();
  ui.acceptState({ playing: true });
  const [timer] = clock.pending(HIDE_DELAY);
  assert.ok(timer);
  const stale = timer.fn;
  for (const state of [{ playing: false }, { loading: true }, { mode: 'ended' }]) {
    ui.acceptState({ playing: true });
    ui.acceptState(state);
    stale(); // fire the old callback manually
    assert.equal(ui.controlsVisible.value, true, JSON.stringify(state));
  }
  ui.acceptState({ playing: true });
  ui.fail('playback', 'boom');
  stale();
  assert.equal(ui.controlsVisible.value, true);
  ui.acceptState({ playing: true });
  ui.toggleMenu('rate');
  stale();
  assert.equal(ui.controlsVisible.value, true);
  assert.equal(ui.openMenu.value, 'rate');
});

test('reset, detach and dispose clear pending timers and listeners', () => {
  const { ui, clock, container, win, doc } = harness();
  ui.acceptState({ playing: true });
  container.dispatch('click', clickEvent());
  assert.equal(clock.pending(HIDE_DELAY).length, 1);
  assert.equal(clock.pending(CLICK_DELAY).length, 1);
  ui.reset();
  assert.equal(clock.pending().length, 0);
  assert.equal(container.listeners.length, 6);
  assert.equal(win.listeners.length, 1);
  assert.equal(doc.listeners.length, 4);
  ui.attach();
  ui.acceptState({ playing: true });
  container.dispatch('click', clickEvent());
  ui.detach();
  assert.equal(clock.pending().length, 0);
  assert.equal(container.listeners.length, 0);
  assert.equal(win.listeners.length, 0);
  assert.equal(doc.listeners.length, 0);
  ui.attach();
  ui.acceptState({ playing: true });
  container.dispatch('click', clickEvent());
  ui.dispose();
  assert.equal(clock.pending().length, 0);
  assert.equal(container.listeners.length, 0);
  clock.advance(HIDE_DELAY * 2);
  assert.equal(ui.controlsVisible.value, true);
});

test('blank click closes an open menu before pause/fullscreen; narrow too', () => {
  const { ui, actions, container, clock } = harness();
  ui.acceptState({ playing: true });
  ui.toggleMenu('rate');
  container.dispatch('click', clickEvent());
  assert.equal(ui.openMenu.value, null);
  assert.equal(clock.pending(CLICK_DELAY).length, 0); // no pause scheduled
  clock.advance(CLICK_DELAY);
  assert.deepEqual(actions, []);
  container.dispatch('click', clickEvent()); // consumed: next click is single
  clock.advance(CLICK_DELAY);
  assert.deepEqual(actions, ['pause']);
  container.dispatch('click', clickEvent()); // double click unchanged
  container.dispatch('click', clickEvent());
  clock.advance(CLICK_DELAY);
  assert.deepEqual(actions, ['pause', 'fullscreen']);
  ui.toggleMenu('rate');
  container.dispatch('click', clickEvent({ closest: () => ({}) }));
  assert.equal(ui.openMenu.value, 'rate'); // control surfaces neither close nor pause
  clock.advance(CLICK_DELAY);
  assert.deepEqual(actions, ['pause', 'fullscreen']);
  const narrow = harness({ width: 500 });
  narrow.ui.acceptState({ playing: true });
  narrow.ui.toggleMenu('rate');
  narrow.container.dispatch('click', clickEvent());
  assert.equal(narrow.ui.openMenu.value, null);
  narrow.clock.advance(CLICK_DELAY);
  assert.deepEqual(narrow.actions, []);
  assert.equal(narrow.ui.controlsVisible.value, true);
});
