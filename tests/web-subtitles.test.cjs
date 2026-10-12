const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

function harness() {
  const drawings = [];
  function canvas() {
    return { width: 300, height: 150, clientWidth: 640, clientHeight: 360,
      getContext: () => ({ clearRect() {}, drawImage(...args) { drawings.push(args); },
        createImageData(w, h) { return { data: new Uint8ClampedArray(w * h * 4) }; },
        putImageData(data) { drawings.push(data); } }) };
  }
  const context = { window: { devicePixelRatio: 1 }, Uint8Array,
    document: { createElement: canvas }, Map, fetch: async () => {}, console };
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, '../public/web/subtitles.js'), 'utf8'), context);
  const surface = canvas();
  const renderer = new context.window.EIOPGSRenderer(surface);
  return { renderer, surface, drawings };
}

const fixture = new Uint8Array(fs.readFileSync(path.join(__dirname, 'fixtures/pgs-fragments.sup')));

test('SUP joins fragmented objects, recognizes final END, and reuses cached objects', () => {
  const { renderer } = harness();
  renderer._parse(fixture);
  assert.deepEqual(Array.from(renderer.sortedPts), [0, 4, 5]);
  assert.equal(renderer.fragments.size, 0);
  const first = renderer.displaySets.get(0);
  assert.equal(first.end, true);
  assert.equal(first.ods[0].pixels.length, 240 * 12);
  assert.ok(first.ods[0].pixels.every(pixel => pixel === 1));
  assert.equal(renderer.displaySets.get(4).pcs.objects.length, 0);
  assert.equal(renderer.displaySets.get(5).end, true);
  assert.equal(renderer.displaySets.get(5).ods[0], first.ods[0]);
});

test('PGS supports short and long colored runs, transparent runs, and row ends', () => {
  const { renderer } = harness();
  const runs = new Uint8Array([1, 0, 0x82, 2, 0, 1, 0, 0xC0, 2, 3, 0, 0]);
  assert.deepEqual(Array.from(renderer._decodeRLE(runs, 6, 1)), [1, 2, 2, 0, 3, 3]);
  assert.throws(() => renderer._decodeRLE(new Uint8Array([0, 0xC0, 2]), 6, 1), /Truncated/);
});

test('PGS clears and redraws when seeking or resizing while preserving aspect ratio', () => {
  const { renderer, surface, drawings } = harness();
  renderer._parse(fixture);
  renderer.render(2);
  assert.equal(renderer.currentDS, 0);
  renderer.render(4);
  assert.equal(renderer.currentDS, 4);
  renderer.render(5);
  const count = drawings.length;
  surface.clientHeight = 480;
  renderer.render(5);
  assert.ok(drawings.length > count);
  assert.deepEqual(drawings.at(-1).slice(1), [0, 60, 640, 360]);
  renderer.render(2);
  assert.equal(renderer.currentDS, 0);
  renderer.destroy();
  assert.equal(renderer.displaySets.size, 0);
  assert.equal(renderer.objects.size, 0);
});

test('malformed SUP cannot allocate oversized compositions or read truncated segments', () => {
  const { renderer } = harness();
  assert.throws(() => renderer._parse(fixture.subarray(0, 18)), /Truncated/);
  assert.throws(() => renderer._parsePCS(new Uint8Array([255, 255, 255, 255, 0, 0, 0, 0, 0, 0, 0])), /Invalid/);
});

test('PGS reuses one offscreen canvas and ImageData across display sets', () => {
  const created = [];
  const imageDataCreated = [];
  function canvas() {
    const ctx = {
      clearRect() {}, drawImage() {},
      createImageData(w, h) { imageDataCreated.push([w, h]); return { data: new Uint8ClampedArray(w * h * 4) }; },
      putImageData() {},
    };
    return { width: 300, height: 150, clientWidth: 640, clientHeight: 360, getContext: () => ctx };
  }
  const context = { window: { devicePixelRatio: 1 }, Uint8Array,
    document: { createElement: tag => { created.push(tag); return canvas(); } }, Map, fetch: async () => {}, console };
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, '../public/web/subtitles.js'), 'utf8'), context);
  const surface = canvas();
  const renderer = new context.window.EIOPGSRenderer(surface);

  renderer._parse(fixture);
  renderer.render(2); // display set 0
  renderer.render(5); // display set 5, same composition size
  assert.equal(created.length, 1, 'each display set allocated a new offscreen canvas');
  assert.equal(imageDataCreated.length, 1, 'each display set allocated a new ImageData');

  renderer.destroy();
  assert.equal(renderer._offscreen, null, 'destroy did not release the offscreen canvas');
  assert.equal(renderer._imgData, null, 'destroy did not release the ImageData');
});

function controllerHarness(core = null, request = async () => {}) {
  const player = { container: { append() {} }, core, positionTicks: 0,
    item: { Id: 'item' }, mediaSource: { Id: 'source', MediaStreams: [
      { Index: 2, Type: 'Subtitle', Codec: 'ass' } ] } };
  const context = { window: {}, location: { origin: 'http://localhost:8096' },
    localStorage: { getItem: () => 'local-token' }, URL, AbortController, fetch: request,
    document: {}, ResizeObserver: class { observe() {} disconnect() {} } };
  vm.runInNewContext(fs.readFileSync(path.join(__dirname, '../public/web/subtitle-controller.js'), 'utf8'), context);
  return { player, controller: new context.window.EIOSubtitles(player) };
}

test('libmedia capitalized Subtitle tracks enable the current track and repopulate cues', async () => {
  const calls = [];
  const core = { currentTime: 2000n, isHls: () => false,
    getStreams: () => [{ mediaType: 'Subtitle', index: 2, id: 7 }],
    getSelectedSubtitleStreamId: () => 7, setSubtitleEnable: value => calls.push(value),
    selectSubtitle: () => assert.fail('already-selected track must not be selected again'),
    seek: time => calls.push(time) };
  const { controller } = controllerHarness(core);
  await controller.select(2);
  assert.deepEqual(calls, [false, true, 2000n]);
  controller.destroy();
});

test('subtitle URLs replace upstream credentials and reject other origins', () => {
  const { controller } = controllerHarness();
  const url = new URL(controller._url({ Index: 2, DeliveryUrl: '/Videos/item/Stream.ass?api_key=upstream' }, 'ass'));
  assert.deepEqual(url.searchParams.getAll('api_key'), ['local-token']);
  assert.throws(() => controller._url({ DeliveryUrl: 'https://upstream.invalid/Stream.ass' }, 'ass'), /代理/);
  controller.destroy();
});

test('clearing an in-flight subtitle fetch cannot recreate the overlay', async () => {
  let finish, signal;
  const { controller, player } = controllerHarness(null, async (_, options) => {
    signal = options.signal;
    return new Promise(resolve => { finish = resolve; });
  });
  player.mediaSource.MediaStreams[0].Codec = 'vtt';
  const pending = controller.select(2);
  controller.clear();
  assert.equal(signal.aborted, true);
  finish({ ok: true, text: async () => 'WEBVTT\n\n00:00.000 --> 00:01.000\nLate cue' });
  await pending;
  assert.equal(controller.layer, null);
  controller.destroy();
});
