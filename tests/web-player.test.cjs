const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function harness() {
  const instances = [];
  function element() {
    return { children: [], clientWidth: 640, clientHeight: 360,
      append(child) { this.children.push(child); }, prepend(child) { this.children.unshift(child); },
      replaceChildren() { this.children = []; }, remove() { this.removed = true; } };
  }
  class Core {
    constructor(options) { this.options = options; this.listeners = {}; instances.push(this); }
    on(event, handler) { this.listeners[event] = handler; }
    async load(url) {
      this.url = url;
      if (url.includes('slow')) await new Promise(resolve => { this.finishLoad = resolve; });
    }
    async play() { this.playCalls = (this.playCalls || 0) + 1; this.listeners.played?.(); this.listeners.firstVideoRendered?.(); }
    async pause() { this.listeners.paused?.(); }
    async resume() {}
    async seek(timestamp) { this.seekTimestamp = timestamp; }
    async destroy() { this.destroyed = true; }
    getDuration() { return 300000n; }
    getStreams() { return []; }
    hasVideo() { return true; }
    isSuspended() { return false; }
    isMSE() { return true; }
    setSubtitleEnable() {}
    resize() {}
  }
  const context = { window: {}, location: { origin: 'http://localhost:8096' },
    document: { createElement: element }, AVPlayer: Core,
    EIOSubtitles: class { select() {} clear() {} destroy() {} render() {} },
    ResizeObserver: class { observe() {} disconnect() {} },
    URL, AbortController, DOMException, setInterval, clearInterval, setTimeout, clearTimeout, console };
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, '../public/web/player-libmedia.js'), 'utf8'), context);
  const container = element();
  const controls = element();
  container.append(controls);
  return { player: new context.window.EIOPlayer(container), instances, container, controls };
}

const source = { Container: 'mkv', MediaStreams: [{ Type: 'Video', Codec: 'hevc' }] };
const item = { Id: 'item', RunTimeTicks: 3000000000 };

test('timed-out direct stream falls back once and preserves Vue controls', async () => {
  const { player, instances, container, controls } = harness();
  await player.load('/slow.mkv', source, item, { fallbackUrl: '/master.m3u8', loadTimeout: 5 });
  assert.equal(instances.length, 2);
  assert.equal(instances[0].destroyed, true);
  assert.equal(instances[1].url, 'http://localhost:8096/master.m3u8');
  assert.equal(instances[1].options.checkUseMSE(), true);
  assert.ok(container.children.includes(controls));
  assert.equal(controls.removed, undefined);
  await player.destroy();
  instances[0].finishLoad();
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(instances[0].playCalls, undefined);
});

test('closing while loading cancels immediately and cannot resurrect playback', async () => {
  const { player, instances } = harness();
  const loading = player.load('/slow.mkv', source, item, { fallbackUrl: '/master.m3u8' });
  await player.destroy();
  await assert.rejects(loading, error => error.name === 'AbortError');
  instances[0].finishLoad();
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(instances.length, 1);
  assert.equal(instances[0].playCalls, undefined);
  assert.equal(player.host.removed, true);
});

test('libmedia milliseconds are converted to seconds and Emby ticks', async () => {
  const { player, instances } = harness();
  const states = [];
  player.onState = state => states.push(state);
  await player.load('/stream.mkv', source, item);
  instances[0].listeners.time(12500n);
  assert.equal(player.positionTicks, 125000000);
  assert.equal(states.at(-1).time, 12.5);
  assert.equal(player.duration, 300);
  await player.seek(60);
  assert.equal(instances[0].seekTimestamp, 60000n);
  await player.pause();
  assert.equal(player.playing, false);
  await player.destroy();
});

test('failed HLS fallback propagates the error instead of retrying forever', async () => {
  const { player, instances } = harness();
  const loading = player.load('/slow.mkv', source, item, { fallbackUrl: '/slow.m3u8', loadTimeout: 5 });
  await assert.rejects(loading, /加载超时/);
  assert.equal(instances.length, 2);
  await player.destroy();
  instances.forEach(core => core.finishLoad?.());
});

test('4K software decode is rejected while native MSE and 1080p stay allowed', async () => {
  const { player, instances } = harness();
  await player.load('/stream.mkv', source, item);
  const core = instances[0];
  core.isMSE = () => false;
  core.getStreams = () => [{ mediaType: 'Video', codecparProxy: { width: 3840, height: 2160 } }];
  core.VideoDecoderThread = { getTasksInfo: async () => [{ hardware: false }] };
  await assert.rejects(player._requireHardware(core), /硬件解码/);
  core.VideoDecoderThread.getTasksInfo = async () => [{ hardware: true }];
  await player._requireHardware(core);
  core.VideoDecoderThread.getTasksInfo = async () => [{ hardware: false }];
  core.isMSE = () => true;
  await player._requireHardware(core);
  core.isMSE = () => false;
  core.getStreams = () => [{ mediaType: 'Video', codecparProxy: { width: 1920, height: 1080 } }];
  await player._requireHardware(core);
  await player.destroy();
});
