'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const clock = require('../public/web/player-native-clock.js');
const sources = require('../public/web/player-native-clock-source.js');
const root = path.join(__dirname, '..');
const bundle = fs.readFileSync(path.join(root, 'public/vendor/web/libmedia/avplayer.js'), 'utf8');

function fixture() {
  class NativeElement {
    constructor() { this.state = { currentTime: 1.1, seeking: false, readyState: 4, paused: false }; this.listeners = new Map(); }
    addEventListener(kind, fn) { this.listeners.set(kind, fn); }
    removeEventListener(kind, fn) { if (this.listeners.get(kind) === fn) this.listeners.delete(kind); }
  }
  for (const name of ['currentTime', 'seeking', 'readyState', 'paused']) {
    Object.defineProperty(NativeElement.prototype, name, { get() { return this.state[name]; } });
  }
  const start = bundle.indexOf('eioInitNativeClock(element){');
  const end = bundle.indexOf('eioMseStrictSeekState(){', start);
  assert.ok(start > 0 && end > start, 'actual main lifecycle methods required');
  const player = vm.runInNewContext('new (class {' + bundle.slice(start, end) + '})()', {
    i(id) { if (id === 90194) return clock; if (id === 90195) return sources; throw new Error('unexecuted dependency ' + id); },
    HTMLMediaElement: NativeElement
  });
  const nonce = [1, -2, 3, -4];
  const descriptor = clock.create(nonce);
  Object.assign(player, { taskId: 'test-clock', eioMseStrictAttach: {},
    eioSharedMediaDomain: { nativeClockSab: descriptor.sab, nonce }, video: new NativeElement() });
  return { player, NativeElement };
}

test('actual main methods use intrinsic native getters, invalidate seeks, and reject old elements', () => {
  const { player, NativeElement } = fixture();
  player.eioInitNativeClock(player.video);
  Object.defineProperty(player.video, 'currentTime', { value: 100000 });
  player.eioNativeClockSample('timeupdate', player.video);
  assert.equal(player.getEioNativeClock().mediaMs, 1100, 'own-property clocks are not authoritative');
  player.eioNativeClockSeek(40);
  player.video.state.currentTime = 40;
  player.eioNativeClockSample('timeupdate', player.video);
  assert.equal(player.getEioNativeClock().known, false, 'programmatic target is not confirmation');
  player.eioNativeClockTarget(39.5);
  player.video.state.currentTime = 39.6;
  player.eioNativeClockSample('timeupdate', player.video);
  assert.equal(player.getEioNativeClock().mediaMs, 39600);
  const old = player.video;
  const oldPause = old.listeners.get('pause');
  player.video = new NativeElement();
  player.eioInitNativeClock(player.video);
  assert.equal(old.listeners.size, 0);
  assert.equal(player.getEioNativeClock().known, false);
  player.eioNativeClockSample('timeupdate', old);
  oldPause();
  assert.equal(player.getEioNativeClock().known, false);
  player.eioNativeClockSample('timeupdate', player.video);
  assert.equal(player.getEioNativeClock().mediaMs, 1100);
  player.eioDisposeNativeClock();
  assert.equal(player.getEioNativeClock().known, false);
  player.eioInitNativeClock(player.video);
  player.eioNativeClockSample('timeupdate', player.video);
  assert.equal(player.getEioNativeClock().mediaMs, 1100, 'same domain reuses its one writer after stop');
});

test('native clock descriptor is bound to four signed domain nonce words', () => {
  const descriptor = clock.create([1, -2, 3, -4]);
  assert.equal(clock.read(descriptor.sab, [1, -2, 3, -4]).known, false);
  for (const nonce of [[1, -2, 3, 4], [1, 2], [1, -2, 3, 4294967292]]) {
    assert.throws(() => clock.read(descriptor.sab, nonce), { code: 'EIO_NATIVE_CLOCK_DOMAIN_MISMATCH' });
    assert.throws(() => clock.writer(descriptor.sab, nonce), { code: 'EIO_NATIVE_CLOCK_DOMAIN_MISMATCH' });
  }
  assert.equal(clock.writer(descriptor.sab, [1, -2, 3, -4]).publish(7, 0, false), true);
});

test('shipped clock modules contain identical canonical bodies and real webpack factories', () => {
  for (const [sourceName, vendorName, chunk, id] of [
    ['player-native-clock.js', 'native-playback-clock.js', 90208, 90194],
    ['player-native-clock-source.js', 'native-playback-clock-source.js', 90209, 90195]
  ]) {
    const source = fs.readFileSync(path.join(root, 'public/web', sourceName), 'utf8');
    const shipped = fs.readFileSync(path.join(root, 'public/vendor/web/libmedia', vendorName), 'utf8');
    assert.ok(shipped.includes(source), 'canonical body parity: ' + sourceName);
    const entries = [];
    vm.runInNewContext(shipped, { self: { webpackChunkAVPlayer: entries } });
    assert.equal(entries[0][0][0], chunk);
    const module = { exports: {} };
    entries[0][1][id](module, module.exports, () => { throw new Error('unexpected dependency'); });
    assert.equal(typeof module.exports.create, 'function');
    assert.ok(bundle.includes(`${chunk}: "${vendorName}"`), 'generated Worker URL mapping');
    assert.ok(bundle.includes(`${chunk}:"${vendorName}"`), 'main URL mapping');
    assert.ok(fs.readFileSync(path.join(root, 'public/web/web.html'), 'utf8').includes('/' + vendorName + '?v='));
  }
});
