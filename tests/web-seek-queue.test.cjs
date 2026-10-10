const { test } = require('node:test');
const assert = require('node:assert/strict');
const { harness, delay } = require('./web-ui-harness.cjs');

function playbackHarness() {
  const source = { Id: 'source', Container: 'mp4', MediaStreams: [] };
  const item = { Id: 'movie', Type: 'Movie', RunTimeTicks: 20000000000, MediaSources: [source] };
  const env = harness({ reply: () => ({ MediaSources: [source], PlaySessionId: 'session' }) });
  const instances = [];
  const { app, context } = env;
  context.addEventListener = context.removeEventListener = () => {};
  app.playerContainer.value = { addEventListener() {}, removeEventListener() {} };
  context.EIOPlayer = class {
    constructor() { this.duration = 2000; this.positionTicks = 0; this.playing = true; this.calls = []; instances.push(this); }
    async load(url, mediaSource) { this.mediaSource = mediaSource; this.state(0); }
    state(time) { this.onState({ mode: 'native', playing: true, duration: this.duration, time }); }
    seek(target) { return new Promise((resolve, reject) => this.calls.push({ target, resolve, reject })); }
    setVolume() {}
    destroy() {}
  };
  const start = async () => { context.location.hash = '#/play/movie'; await app.playItem(item); return instances.at(-1); };
  return { ...env, start };
}

test('seek queue retains only the latest target and holds its preview during the first seek', async t => {
  const { app, start } = playbackHarness();
  t.after(() => { app.exitPlayer(); app.toasts.dispose(); });
  const player = await start();
  app.previewSeek('100'); app.onWasmSeek('0'); app.endScrub(); // preview wins over a stale DOM value
  await delay(10);
  assert.deepEqual(player.calls.map(c => c.target), [100]);
  app.onWasmSeek('200'); await delay(10);
  app.onWasmSeek('300'); await delay(10);
  player.state(100);
  assert.equal(app.wasmTime.value, 300);
  assert.equal(player.calls.length, 1);
  player.calls[0].resolve(); await delay(0);
  assert.deepEqual(player.calls.map(c => c.target), [100, 300]);
  player.state(300); player.calls[1].resolve(); await delay(0);
  player.state(301);
  assert.equal(app.wasmTime.value, 301);
});

test('an old player seek cannot clear a new player queue or report its error', async t => {
  const { app, start } = playbackHarness();
  t.after(() => { app.exitPlayer(); app.toasts.dispose(); });
  const oldPlayer = await start();
  app.onWasmSeek('100'); await delay(10);
  const newPlayer = await start();
  app.onWasmSeek('500'); await delay(10);
  app.onWasmSeek('600'); await delay(10);
  oldPlayer.calls[0].reject(new Error('old player stopped')); await delay(0);
  assert.equal(app.toasts.items.value.length, 0);
  newPlayer.state(100);
  assert.equal(app.wasmTime.value, 600);
  newPlayer.calls[0].resolve(); await delay(0);
  assert.deepEqual(newPlayer.calls.map(c => c.target), [500, 600]);
  newPlayer.calls[1].resolve(); await delay(0);
});
