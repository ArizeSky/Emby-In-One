'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const vm = require('node:vm');
const readGraph = require('./web-libmedia-source-graph.cjs');
const source = readGraph().text;

// Real shipped factories, synthetic IO/channels/allocator. This is not browser
// loading, generated-Worker execution, or an allocator-ledger ACK assertion.
function loader(overrides = {}, globals = {}) {
  const cache = {};
  const context = vm.createContext({ TextEncoder, TextDecoder, Response, ReadableStream,
    AbortController, WebAssembly, setTimeout, clearTimeout, AggregateError, Error, ...globals });
  function req(id) {
    if (Object.hasOwn(overrides, id)) return overrides[id];
    if (cache[id]) return cache[id];
    const signature = `${id}:(e,t,i)=>{`;
    const start = source.indexOf(signature);
    assert.ok(start >= 0, `shipped factory ${id}`);
    const tail = /},\d+:\(e,t,i\)=>\{/.exec(source.slice(start + signature.length));
    assert.ok(tail, `factory boundary ${id}`);
    const end = start + signature.length + tail.index + 1;
    const factory = vm.runInContext('(' + source.slice(start + String(id).length + 1, end) + ')', context);
    const exports = cache[id] = {};
    factory({}, exports, req);
    return exports;
  }
  req.d = (target, getters) => {
    for (const [key, get] of Object.entries(getters)) Object.defineProperty(target, key, { get, enumerable: true });
  };
  req.r = () => {};
  return req;
}
const types = { ai: v => typeof v === 'number', Yj: v => typeof v === 'string',
  Pc: v => typeof v === 'function', sv: v => v instanceof ArrayBuffer };
const objects = { X$: Object.assign, __: (o, fn) => Object.entries(o).forEach(([k, v]) => fn(v, k)) };
const base = { 19371: { A: (o, k, v) => { o[k] = v; } }, 61326: {},
  84777: types, 23902: objects, 51091: { A: {} }, 937: { A: {} },
  34117: { h2: message => { throw Error(message); } }, 70704: {} };

test('real control listener replies once; real IPC rejects and removes its pending request', { timeout: 2000 }, async () => {
  const replies = [];
  function channel() {
    const a = { start() {}, close() {}, postMessage(message) { queueMicrotask(() => b.onmessage?.({ data: message })); } };
    const b = { start() {}, close() {}, postMessage(message) {
      if (message.type === 'reply') replies.push(message);
      queueMicrotask(() => a.onmessage?.({ data: message }));
    } };
    return { port1: a, port2: b };
  }
  class Events {
    constructor() { this.handlers = {}; }
    on(type, fn) { this.handlers[type] = fn; }
    fire(type, data) { this.handlers[type]?.(data); }
  }
  const req = loader({ ...base, 27745: { A: Events }, 72034: { A: v => v !== undefined },
    31088: { A: channel } }, { document: { visibilityState: 'visible', addEventListener() {}, removeEventListener() {} } });
  let reject = true;
  const control = new (req(80327).A)({ isPictureInPicture: () => false,
    onGetDecoderResource: async () => { if (reject) throw Error('secret URL and credentials'); return { module: 'matched' }; } });
  const ipc = new (req(15716).Ay)(control.getDemuxerControlPort());
  try {
    await assert.rejects(ipc.request('getDecoderResource', { mediaType: 0, codecId: 7 }),
      error => error.message === 'EIO_DECODER_RESOURCE_FAILED');
    assert.equal(ipc.requestMap.size, 0);
    assert.equal(replies.length, 1);
    assert.equal(Object.hasOwn(replies[0].data, 'result'), false);
    reject = false;
    assert.deepEqual(await ipc.request('getDecoderResource'), { module: 'matched' });
    assert.equal(replies.length, 2);
  } finally { ipc.destroy(); control.destroy(); }
});

test('a missing observer replies undefined so the worker skips optional cover decode', { timeout: 2000 }, async () => {
  const replies = [];
  function channel() {
    const a = { start() {}, close() {}, postMessage(message) { queueMicrotask(() => b.onmessage?.({ data: message })); } };
    const b = { start() {}, close() {}, postMessage(message) {
      if (message.type === 'reply') replies.push(message);
      queueMicrotask(() => a.onmessage?.({ data: message }));
    } };
    return { port1: a, port2: b };
  }
  class Events {
    constructor() { this.handlers = {}; }
    on(type, fn) { this.handlers[type] = fn; }
    fire(type, data) { this.handlers[type]?.(data); }
  }
  const req = loader({ ...base, 27745: { A: Events }, 72034: { A: v => v !== undefined },
    31088: { A: channel } }, { document: { visibilityState: 'visible', addEventListener() {}, removeEventListener() {} } });
  // No onGetDecoderResource at all: the control listener must resolve
  // undefined (worker `if(!t)continue` skips the cover) instead of
  // rejecting with EIO_DECODER_RESOURCE_FAILED and failing analyzeStreams.
  const control = new (req(80327).A)({ isPictureInPicture: () => false });
  const ipc = new (req(15716).Ay)(control.getDemuxerControlPort());
  try {
    assert.deepEqual(await ipc.request('getDecoderResource', { mediaType: 0, codecId: 7 }), undefined);
    assert.equal(ipc.requestMap.size, 0);
    assert.equal(replies.length, 1);
    assert.equal(replies[0].data.result, undefined, 'a defined undefined result, not an error reply');
    assert.equal(Object.hasOwn(replies[0].data, 'error'), false);
  } finally { ipc.destroy(); control.destroy(); }
});

test('real streaming loader terminates synthetic 404 before reading or transforming', { timeout: 2000 }, async () => {
  let gets = 0;
  const req = loader(base, { fetch: async () => ({ ok: false, status: 404,
    statusText: 'private detail', body: { getReader() { gets++; throw Error('must not read'); } } }) });
  await assert.rejects(req(1538).A({ source: 'fixture:missing-decoder' }), /EIO_DECODER_RESOURCE_HTTP_404/);
  await new Promise(resolve => setImmediate(resolve));
  assert.equal(gets, 0);
});

test('real transformer and compiler preserve failures and cancel the source; matched minimal WASM compiles', { timeout: 3000 }, async () => {
  const wasm = Uint8Array.from([0, 97, 115, 109, 1, 0, 0, 0]);
  const valid = loader(base);
  assert.ok((await valid(1538).A({ source: wasm })).module instanceof WebAssembly.Module);
  const failure = Error('synthetic source read failure');
  let cancelled = 0;
  const req = loader(base, { fetch: async () => ({ ok: true, status: 200, body: {
    getReader: () => ({ read: async () => { throw failure; }, cancel: async () => { cancelled++; } })
  } }) });
  await assert.rejects(req(1538).A({ source: 'fixture:broken-stream' }), error => error === failure);
  assert.equal(cancelled, 1);
  // Syntactically invalid WASM reaches the real compiler, not a false EOF success.
  await assert.rejects(valid(1538).A({ source: Uint8Array.from([1, 2, 3, 4, 5, 6, 7, 8]) }), WebAssembly.CompileError);
  await assert.rejects(valid(1538).A({ source: new Uint8Array(0) }), WebAssembly.CompileError);
});

function demuxHarness({ attachedDecodeFails = false, firstResourceFails = false } = {}) {
  const failure = Error('decoder resource failure');
  const released = [], closed = [];
  const fields = new Map();
  let next = 100, read = 0;
  const req = loader({ ...base,
    56238: { f: { 15: p => fields.get(p) || 0, 17: p => fields.get(p) ?? 0n } },
    6927: { M: { 17: (p, v) => fields.set(p, v) } },
    46564: { _5: () => { const p = next; next += 100; return p; }, Up() {}, Qe: p => released.push(p) },
    54568: { o9: 'pointer' }, 55982: {}, 49103: {}, 92086: {},
    93014: { $: () => true }, 37191: {}, 91528: {}, 26159: { A: () => 0 },
    2281: {}, 80209: {}, 35924: {}, 86523: {}, 98901: { k: value => value },
    29688: { Dh: -1n }, 83796: {},
    57969: { A: class { async open() { return 0; } decode() { return 0; } close() { closed.push('audio'); } } },
    9822: { A: class { async open() { return 0; } decode() {} async flush() { throw failure; } close() { closed.push('picture'); } } }
  });
  const audio = { index: 0, codecpar: { codecType: 1, codecId: 1, videoDelay: 0,
    chLayout: { nbChannels: 1, u: {} } }, metadata: {}, startTime: -1n };
  const picture = { index: 1, disposition: 1024, attachedPic: 900, codecpar: { codecType: 0, codecId: 2 } };
  const fc = { streams: attachedDecodeFails ? [picture] : [audio, picture],
    options: { fastOpen: attachedDecodeFails, maxAnalyzeDuration: 15000 },
    interval: { packetBuffer: [] }, ioReader: { flags: 0 },
    getStreamByIndex: index => [audio, picture][index],
    getDecoderResource: async (type) => { if (firstResourceFails || (type === 0 && !attachedDecodeFails)) throw failure; return {}; },
    iformat: { getAnalyzeStreamsCount: () => 1, readAVPacket: async (fc, p) => {
      if (read++) return -1048576;
      fields.set(p + 28, 4); fields.set(p + 8, 10n); fields.set(p + 16, 10n); return 0;
    } } };
  return { analyze: () => req(64750).Ib(fc), fc, failure, released, closed };
}

test('real demux analysis restores sampled packet ownership and closes decoder on cover acquisition rejection', { timeout: 2000 }, async () => {
  const h = demuxHarness();
  await assert.rejects(h.analyze(), error => error === h.failure);
  assert.deepEqual(Array.from(h.fc.interval.packetBuffer), [100]);
  assert.deepEqual(h.released, [200], 'only the unsampled scratch packet is freed');
  assert.deepEqual(h.closed, ['audio']);
});

test('resource rejection while current packet is sampled restores it without double-free', { timeout: 2000 }, async () => {
  const h = demuxHarness({ firstResourceFails: true });
  await assert.rejects(h.analyze(), error => error === h.failure);
  assert.deepEqual(Array.from(h.fc.interval.packetBuffer), [100]);
  assert.deepEqual(h.released, []);
});

test('real attached-picture decoder closes when flush rejects', { timeout: 2000 }, async () => {
  const h = demuxHarness({ attachedDecodeFails: true });
  await assert.rejects(h.analyze(), error => error === h.failure);
  assert.deepEqual(h.closed, ['picture']);
});
