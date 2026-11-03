'use strict';

/*
 * Strict MSE pull/swap lifetime tests.
 *
 * The module under test is the actual shipped UMD source in
 * public/vendor/web/libmedia/mse-pull-lifetime.js, evaluated from its real
 * webpack chunk push. The integration methods are sliced from the actual
 * 26.avplayer.js text (never rewritten into stubs) and executed on a fake
 * native memory/pool and deferred pull IPC. No browser, MSE, real Worker,
 * upstream, application server or production instance is used. The actual
 * generated-Worker acceptance remains a parent step.
 */

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const ROOT = path.join(__dirname, '..');
const MODULE_PATH = path.join(ROOT, 'public', 'vendor', 'web', 'libmedia', 'mse-pull-lifetime.js');
const OWNERSHIP_PATH = path.join(ROOT, 'public', 'vendor', 'web', 'libmedia', 'mse-packet-ownership.js');
const CHUNK26_PATH = path.join(ROOT, 'public', 'vendor', 'web', 'libmedia', '26.avplayer.js');
const AVPLAYER_PATH = path.join(ROOT, 'public', 'vendor', 'web', 'libmedia', 'avplayer.js');
const MODULE_SRC = fs.readFileSync(MODULE_PATH, 'utf8');
const OWNERSHIP_SRC = fs.readFileSync(OWNERSHIP_PATH, 'utf8');
const CHUNK26_SRC = fs.readFileSync(CHUNK26_PATH, 'utf8');
const AVPLAYER_SRC = fs.readFileSync(AVPLAYER_PATH, 'utf8');

function actualModuleText(text, id) {
  const start = text.indexOf(id + ':(e,t,i)=>{');
  assert.ok(start >= 0, 'actual module ' + id);
  const rest = text.slice(start + String(id).length + 1);
  const next = /\},\d+:\(e,t,i\)=>/.exec(rest);
  assert.ok(next, 'actual module ' + id + ' boundary');
  return rest.slice(0, next.index + 1);
}

/* Use the real 54568 o9 symbol, not a crafted numeric property key. */
const ACTUAL_O9 = (() => {
  const factory = vm.runInNewContext('(' + actualModuleText(AVPLAYER_SRC, 54568) + ')', {});
  const exported = {};
  factory({}, exported, {
    d(target, definitions) {
      for (const key of Object.keys(definitions)) Object.defineProperty(target, key, { enumerable: true, get: definitions[key] });
    }
  });
  return exported.o9;
})();

function slice(text, startMarker, endMarker) {
  const start = text.indexOf(startMarker);
  assert.ok(start >= 0, 'missing actual marker ' + startMarker);
  const end = text.indexOf(endMarker, start + startMarker.length);
  assert.ok(end > start, 'missing actual boundary ' + endMarker);
  return text.slice(start, end);
}

function evaluateChunk(source, file) {
  const context = vm.createContext({
    self: { webpackChunkAVPlayer: [] },
    console, setTimeout, clearTimeout, Promise, Map, Set, Object, Array,
    Error, Number, String, BigInt, Math, JSON, Uint8Array
  });
  vm.runInContext(source, context, { filename: file });
  return context.self.webpackChunkAVPlayer[0];
}

function loadOwnershipModule() {
  const push = evaluateChunk(OWNERSHIP_SRC, OWNERSHIP_PATH);
  assert.ok(push && push[0][0] === 90207, 'actual chunk 90207 registration');
  const factory = push[1][90193];
  assert.equal(typeof factory, 'function', 'actual module 90193 factory');
  const moduleObject = { exports: {} };
  factory(moduleObject, moduleObject.exports, () => { throw new Error('unexpected ownership require'); });
  return moduleObject.exports;
}

const OWNERSHIP = loadOwnershipModule();

function loadPullModule(ownership) {
  const push = evaluateChunk(MODULE_SRC, MODULE_PATH);
  assert.ok(push && push[0][0] === 90206, 'actual chunk 90206 registration');
  const factory = push[1][90192];
  assert.equal(typeof factory, 'function', 'actual module 90192 factory');
  const moduleObject = { exports: {} };
  factory(moduleObject, moduleObject.exports, (id) => {
    if (id === 90193) return ownership;
    throw new Error('unexpected webpack require ' + id);
  });
  return moduleObject.exports;
}

const PULL = loadPullModule(OWNERSHIP);
const MUX = require('./helpers/mux-lifecycle.cjs')();

function fakePool() {
  const freed = [];
  return {
    freed,
    alloc() { return 900 + freed.length; },
    release(pointer) {
      if (!(pointer > 0)) throw new Error('invalid packet pointer');
      freed.push(pointer);
      /* Actual 61457 AVPacketPool.release is void, so success is "no throw". */
      return undefined;
    }
  };
}

function memoryReader(memory) {
  return {
    get(pointer) { return memory.get(pointer); },
    set(pointer, value) { memory.set(pointer, value); }
  };
}

function makeNative(memory, pool) {
  const get = (p) => memory.get(p);
  return {
    readI32: (p) => Number(get(p) || 0) | 0,
    readU32: (p) => Number(get(p) || 0) >>> 0,
    readBigInt: (p) => BigInt(get(p) || 0n),
    writeI32: (p, v) => memory.set(p, Number(v) | 0),
    writeBigInt: (p, v) => memory.set(p, BigInt(v)),
    scale: (v) => v,
    scaleInt: (v) => v,
    minBig: (a, b) => a < b ? a : b,
    maxBig: (a, b) => a > b ? a : b,
    diffBig: (a, b) => a > b ? a - b : b - a,
    millisecondsTimeBase: { num: 1, den: 1000 },
    noTimestamp: -1n,
    getSideData: () => 0,
    setSideData: () => {},
    setPacketData: () => {},
    allocBytes: (size) => { const pointer = 5000 + size; return pointer; },
    writeBytes: () => {},
    readBytes: () => new Uint8Array(0),
    copyMemory: () => {},
    copyPacket: () => {},
    isKeyframe: (p) => (Number(get(p + 36) || 0) & 1) === 1,
    isRandomAccessVideo: (stream, p) => (Number(get(p + 36) || 0) & 1) === 1 &&
      (Number(get(Number(stream.codecpar) + 4) || 0) === 27 ||
       Number(get(Number(stream.codecpar) + 4) || 0) === 173),
    codecparId: () => 27,
    releaseCodecpar: () => {},
    videoTimeBase: () => ({ num: 1, den: 1000 }),
    delay: () => Promise.resolve(),
    timeBaseIndex: ACTUAL_O9
  };
}

function makeController(extra) {
  const pool = fakePool();
  const memory = new Map();
  const deps = Object.assign({
    packetPool: pool,
    now: () => 0,
    native: makeNative(memory, pool)
  }, extra || {});
  return { pool, memory, controller: PULL.createMsePullLifetime(deps) };
}

function strictStream(overrides) {
  const output = { index: 0, timeBase: { num: 1, den: 1000 } };
  const stream = Object.assign({
    type: 'video',
    eioOwned: {},
    codecpar: 1234,
    streamIndex: 0,
    oformatContext: { streams: [output] },
    pullQueue: { queue: [], ended: false, index: 0, lastPTS: 0n, lastDTS: 0n, diff: 0n, frameCount: -1n, useSampleRateTimeBase: false },
    track: { reset() {} },
    loop: { stop() {} },
    frontBuffered: false,
    frontPacket: 0,
    backPacket: 0,
    startTimestamp: 0n,
    packetEnded: false,
    ended: false,
    seekSync: null,
    startPTS: 0n,
    lastDTS: -1n,
    enableRawMpeg: true
  }, overrides || {});
  return stream;
}

/* Local native-memory instruments. They keep the real h.Mr/k call shape and
   rational time-base semantics; they do not replace the actual pull algorithm. */
function scaleTicks(value, sourceTimeBase, targetTimeBase) {
  const source = sourceTimeBase || { num: 1, den: 1 };
  const target = targetTimeBase || { num: 1, den: 1 };
  const sourceNum = BigInt(source.num);
  const sourceDen = BigInt(source.den);
  const targetNum = BigInt(target.num);
  const targetDen = BigInt(target.den);
  if (sourceNum === 0n || targetNum === 0n) return 0n;
  return (BigInt(value) * sourceNum * targetDen) / (sourceDen * targetNum);
}

function fakeTimeBaseAt(memory, pointer) {
  return {
    num: Number(memory.get(pointer) || 0),
    den: Number(memory.get(pointer + 4) || 0)
  };
}

function copyFakeTimeBase(memory, pointer, value) {
  if (!value || typeof value !== 'object') throw new Error('fake time-base source missing');
  memory.set(pointer, Number(value.num) | 0);
  memory.set(pointer + 4, Number(value.den) | 0);
}

function setFakePacket(memory, pointer, packet) {
  memory.set(pointer + 8, BigInt(packet.pts));
  memory.set(pointer + 16, BigInt(packet.dts));
  memory.set(pointer + 48, BigInt(packet.duration || 0));
  memory.set(pointer + 64, BigInt(packet.duration || 0));
  memory.set(pointer + 36, Number(packet.flags || 0) | 0);
  const timeBase = packet.timeBase || { num: 1, den: 1000 };
  memory.set(pointer + 72, Number(timeBase.num) | 0);
  memory.set(pointer + 76, Number(timeBase.den) | 0);
}

function fakePacketView(memory, pointer) {
  return {
    pts: BigInt(memory.get(pointer + 8) || 0n),
    dts: BigInt(memory.get(pointer + 16) || 0n),
    duration: BigInt(memory.get(pointer + 64) || 0n),
    durationSentinel: BigInt(memory.get(pointer + 48) || 0n),
    timeBase: fakeTimeBaseAt(memory, pointer + 72)
  };
}

/* ---------------------------------------------------------------- */
/* Actual 26 integration methods                                     */
/* ---------------------------------------------------------------- */

function actual26Pipeline(options) {
  const scoped = options || {};
  const helpers = slice(CHUNK26_SRC, 'eioPullLifetime(t){', 'async syncToKeyframe(t,eioTickets){');
  const sync = slice(CHUNK26_SRC, 'async syncToKeyframe(t,eioTickets){', 'getSourceOpenHandler(t){');
  const handler = slice(CHUNK26_SRC, 'getSourceOpenHandler(t){', 'getMimeType(t){');
  const swap = slice(CHUNK26_SRC, 'swap(t,e){', 'createLoop(t,e){');
  const pull = slice(CHUNK26_SRC, 'async pullAVPacket(t,e,eioTicket){', 'writeAVPacket(t,e){');
  const startMux = slice(CHUNK26_SRC, 'async startMux(t,e){', 'async resetResource(t,e,eioMutation){');
  const mux = slice(CHUNK26_SRC, 'eioMuxGate(task){', 'async beforeSeek(taskId){');
  const source = '(class {' + mux + helpers + sync + handler + swap + pull + startMux + '})';
  const memory = new Map();
  const pool = fakePool();
  const ops = { scale: 0, copy: 0, logs: [], pulls: [], dts: 0, pts: 0, nal: [] };
  const context = {
    BigInt, Math, Promise, Number, String, Uint8Array, Array, Object,
    i: (id) => id === 90192 ? PULL : id === 90198 ? MUX : null,
    eioMseError: (code) => Object.assign(new Error(code), { code }),
    E: { A: class FakeDelay { constructor(seconds) { this.seconds = seconds; } then(resolve) { resolve(); } } },
    C: { A: () => context.nowMs },
    nowMs: 0,
    s: {
      f: {
        15: (p) => Number(memory.get(p) || 0) | 0,
        17: (p) => BigInt(memory.get(p) || 0n),
        20: (p) => Number(memory.get(p) || 0) >>> 0
      }
    },
    c: {
      M: {
        15: (p, v) => memory.set(p, Number(v) | 0),
        17: (p, v) => memory.set(p, BigInt(v)),
        20: (p, v) => memory.set(p, Number(v) >>> 0)
      }
    },
    h: {
      Mr: (value, pointer, target) => {
        ops.scale += 1;
        return scaleTicks(value, fakeTimeBaseAt(memory, pointer), target);
      },
      k: (value, source, target) => {
        ops.scale += 1;
        return scaleTicks(value, source, target);
      }
    },
    x: { jk: (a, b) => a < b ? a : b, T9: (a, b) => a > b ? a : b, tn: (a, b) => a > b ? a - b : b - a },
    f: { rU: () => 0, Ow: () => {}, rN: () => {}, NX: () => {}, fQ: () => 0 },
    a: {
      s3: () => new Uint8Array(0),
      lW: () => {},
      Mr: (pointer, value) => { ops.copy += 1; copyFakeTimeBase(memory, pointer, value); }
    },
    l: { sY: () => 7000 },
    k: { r8: () => 0 },
    /* Scoped fake-WASM NAL backend. These two probes are the only parser
       instrumentation; the actual RAP branches and packet algorithm stay in
       the sliced 26 source and the real 90192/90193 modules. */
    U: {
      mW: (packet, length) => {
        ops.nal.push({ codec: 27, packet, length });
        if (typeof scoped.h264Nal === 'function') return scoped.h264Nal(packet, length);
        return scoped.h264Nal !== false;
      }
    },
    I: {
      mW: (packet, length) => {
        ops.nal.push({ codec: 173, packet, length });
        if (typeof scoped.hevcNal === 'function') return scoped.hevcNal(packet, length);
        return scoped.hevcNal !== false;
      }
    },
    g: { dn: () => {} },
    n: { A: () => ({ num: 1, den: 1000 }) }, o: { Q: {} }, r: { o9: ACTUAL_O9 },
    m: { Dh: -1n, i0: { num: 1, den: 1000 } },
    S: { A: { safari: !!scoped.safari } }, A: { A: { ios: false } },
    T: {
      Yz: () => {},
      R8: (message) => {
        const text = String(message);
        ops.logs.push(text);
        if (text.includes('got packet with dts')) ops.dts += 1;
        if (text.includes('got video packet with pts')) ops.pts += 1;
      }
    },
    P: { yw: (v) => Number(v) / 1000 },
    L: 'test'
  };
  const Pipeline = vm.runInNewContext(source, context);
  const pipeline = new Pipeline();
  pipeline.pool = pool;
  pipeline.memory = memory;
  pipeline.context = context;
  pipeline.ops = ops;
  return pipeline;
}

function installLifetime(pipeline, task) {
  task.eioPullLifetime = pipeline.eioPullLifetime(task);
  return task.eioPullLifetime;
}

function actualTask(pool, memory, streams) {
  return {
    taskId: 'task-1',
    avpacketPool: pool,
    memory: memory,
    audio: streams.audio || null,
    video: streams.video || null,
    cacheDuration: 500n,
    playRate: 100n,
    isLive: false,
    tasks: new Map(),
    controlIPCPort: { notify() {}, destroy() {} },
    stats: 0,
    maxBuffer: 4,
    minBuffer: 2,
    pausing: false,
    seeking: false
  };
}

/* The real 26 getSourceOpenHandler is captured before streams are installed.
   Strict dispatch must inspect current streams at callback invocation, not at
   handler construction. Exercise both capture timings with real 90192/90193. */
function sourceOpenFixture(options) {
  const settings = Object.assign({ installBeforeStreams: false, failurePointer: null }, options || {});
  const pipeline = actual26Pipeline();
  const memory = pipeline.memory;
  const pool = pipeline.pool;
  const audioOutput = differentialOutput({ outputTimeBase: { num: 1, den: 1000 } });
  const videoOutput = differentialOutput({ outputTimeBase: { num: 1, den: 1000 } });
  videoOutput.index = 1;
  audioOutput.codecpar = { [ACTUAL_O9]: 7001 };
  videoOutput.codecpar = { [ACTUAL_O9]: 7101 };
  const track = () => ({
    reset() {},
    setSourceBuffer() {},
    end() {},
    getBufferedStart() { return 0; },
    insertEnqueueCallback(callback) { if (callback) callback(); }
  });
  const audio = strictStream({
    type: 'audio', codecpar: 7001, streamIndex: 1, pullIPC: 'audio',
    oformatContext: { streams: [audioOutput] }, track: track()
  });
  const video = strictStream({
    type: 'video', codecpar: 7101, streamIndex: 0, pullIPC: 'video',
    oformatContext: { streams: [videoOutput] }, track: track()
  });
  // One-second fake audio frames: sync's audio PTS must overtake video PTS.
  memory.set(audio.codecpar + 144, 1000);
  for (const packet of [
    { pointer: 5001, pts: 1000, dts: 1000, flags: 0 },
    { pointer: 5201, pts: 2000, dts: 2000, flags: 0 },
    { pointer: 5401, pts: 3000, dts: 3000, flags: 0 },
    { pointer: 5601, pts: 4000, dts: 4000, flags: 0 },
    { pointer: 5801, pts: 5000, dts: 5000, flags: 0 },
    { pointer: 6001, pts: 1000, dts: 1000, flags: 1 },
    { pointer: 6201, pts: 2000, dts: 2000, flags: 0 },
    { pointer: 6401, pts: 3000, dts: 3000, flags: 0 },
    { pointer: 6601, pts: 4000, dts: 4000, flags: 0 },
    { pointer: 6801, pts: 5000, dts: 5000, flags: 0 }
  ]) setFakePacket(memory, packet.pointer, packet);
  const sequences = { audio: [5001, 5201, 5401, 5601, 5801], video: [6001, 6201, 6401, 6601, 6801] };
  pipeline.pullAVPacketInternal = async (task, ipc) => {
    const sequence = sequences[ipc];
    const next = sequence.length ? sequence[0] : -1048576;
    if (settings.failurePointer === next) {
      throw Object.assign(new Error('EIO_TEST_INJECTED_PULL'), { code: 'EIO_TEST_INJECTED_PULL' });
    }
    if (sequence.length) sequence.shift();
    pipeline.ops.pulls.push(next);
    return next;
  };
  const task = actualTask(pool, memory, { audio: null, video: null });
  task.eioOwnedResources = {};
  task.cacheDuration = 100n;
  task.tasks.set(task.taskId, task);
  let handler;
  if (settings.installBeforeStreams) {
    handler = pipeline.getSourceOpenHandler(task, BigInt(0));
    task.audio = audio;
    task.video = video;
  } else {
    task.audio = audio;
    task.video = video;
    handler = pipeline.getSourceOpenHandler(task, BigInt(0));
  }
  pipeline.createSourceBuffer = () => ({});
  pipeline.eioHeaderOperation = () => {};
  pipeline.eioMuxFlushProbe = () => {};
  pipeline.eioAddQueue = () => {};
  pipeline.eioProbeWrite = () => {};
  pipeline.createLoop = (stream) => {
    stream.loop = { start() {}, stop() {}, destroy() {} };
  };
  pipeline.seekSeconds = (value) => Number(value) / 1000;
  pipeline.timeNoticeNeeded = () => false;
  return { pipeline, memory, pool, task, audio, video, handler };
}

function assertSourceOpenSlots(fixture, expected) {
  const { audio, video } = fixture;
  assert.equal(audio.backPacket, expected.audioBack);
  assert.equal(audio.frontPacket, expected.audioFront);
  assert.equal(video.backPacket, expected.videoBack);
  assert.equal(video.frontPacket, expected.videoFront);
  assert.equal(audio.eioPullQueueOwners.length, 0);
  assert.equal(video.eioPullQueueOwners.length, 0);
  assert.deepEqual(audio.pullQueue.queue, []);
  assert.deepEqual(video.pullQueue.queue, []);
  assert.equal(audio.eioPullTickets.size, 0, 'no pending/settled sourceOpen ticket is leaked for audio');
  assert.equal(video.eioPullTickets.size, 0, 'no pending/settled sourceOpen ticket is leaked for video');
  assert.equal(audio.eioPullSlotOwners.back && audio.eioPullSlotOwners.back.pointer, expected.audioBack);
  assert.equal(audio.eioPullSlotOwners.front && audio.eioPullSlotOwners.front.pointer, expected.audioFront);
  assert.equal(video.eioPullSlotOwners.back && video.eioPullSlotOwners.back.pointer, expected.videoBack);
  assert.equal(video.eioPullSlotOwners.front && video.eioPullSlotOwners.front.pointer, expected.videoFront);
  assert.deepEqual(fixture.pool.freed.slice().sort((a, b) => a - b), expected.freed);
}

function differentialOutput(definition) {
  const timeBase = {
    num: definition.outputTimeBase.num,
    den: definition.outputTimeBase.den
  };
  timeBase[ACTUAL_O9] = timeBase;
  return { index: 0, timeBase };
}

async function runDifferentialSide(definition, strict) {
  const pipeline = actual26Pipeline({ h264Nal: definition.h264Nal, hevcNal: definition.hevcNal });
  const memory = pipeline.memory;
  const pool = pipeline.pool;
  const output = differentialOutput(definition);
  const stream = strictStream(Object.assign({
    type: definition.type,
    codecpar: definition.codecpar,
    oformatContext: { streams: [output] },
    pullQueue: {
      queue: [],
      ended: false,
      index: 0,
      lastPTS: definition.lastPTS,
      lastDTS: definition.lastDTS,
      diff: definition.diff,
      frameCount: definition.frameCount,
      useSampleRateTimeBase: definition.useSampleRateTimeBase === true
    }
  }, definition.stream || {}, { eioOwned: strict ? {} : null }));

  if (definition.codecparFields) {
    for (const offset of Object.keys(definition.codecparFields)) {
      memory.set(definition.codecpar + Number(offset), definition.codecparFields[offset]);
    }
  }
  for (const packet of definition.packets) setFakePacket(memory, packet.pointer, packet);

  const task = actualTask(pool, memory, { [definition.type]: stream });
  task.isLive = false;
  const lifetime = strict ? installLifetime(pipeline, task) : null;
  const sequence = definition.sequence.slice();
  pipeline.pullAVPacketInternal = async () => {
    const next = sequence.length ? sequence.shift() : -1048576;
    pipeline.ops.pulls.push(next);
    return next;
  };

  const results = [];
  for (let index = 0; index < definition.pullCount; index += 1) {
    const ticket = strict ? lifetime.begin(stream, 'differential') : null;
    if (strict) assert.ok(ticket, 'differential strict ticket');
    const value = await pipeline.pullAVPacket(stream, task, ticket || undefined);
    if (strict) assert.notEqual(value, PULL.STALE, 'differential case must not be revoked');
    const record = {
      value,
      pulls: pipeline.ops.pulls.length,
      ended: stream.pullQueue.ended === true,
      packetEnded: stream.packetEnded === true,
      queue: stream.pullQueue.queue.slice(),
      diff: stream.pullQueue.diff,
      frameCount: stream.pullQueue.frameCount
    };
    if (value > 0) Object.assign(record, fakePacketView(memory, value));
    results.push(record);
    if (strict) lifetime.complete(ticket, 'differential');
  }
  return { results, ops: pipeline.ops, stream, pool };
}

function comparableDifferential(side) {
  return side.results.map((result) => ({
    value: result.value,
    pulls: result.pulls,
    pts: result.pts,
    dts: result.dts,
    duration: result.duration,
    durationSentinel: result.durationSentinel,
    timeBase: result.timeBase,
    queue: result.queue,
    ended: result.ended,
    packetEnded: result.packetEnded,
    diff: result.diff,
    frameCount: result.frameCount
  }));
}

async function runSyncDifferentialSide(definition, strict) {
  const pipeline = actual26Pipeline({ h264Nal: definition.h264Nal, hevcNal: definition.hevcNal });
  const memory = pipeline.memory;
  const pool = pipeline.pool;
  const output = differentialOutput(definition);
  const stream = strictStream(Object.assign({
    type: 'video',
    codecpar: definition.codecpar,
    oformatContext: { streams: [output] },
    pullQueue: {
      queue: [],
      ended: false,
      index: 0,
      lastPTS: definition.lastPTS === undefined ? 0n : definition.lastPTS,
      lastDTS: definition.lastDTS === undefined ? 0n : definition.lastDTS,
      diff: definition.diff === undefined ? 0n : definition.diff,
      frameCount: definition.frameCount === undefined ? -1n : definition.frameCount,
      useSampleRateTimeBase: false
    }
  }, definition.stream || {}, { eioOwned: strict ? {} : null }));

  if (definition.codecparFields) {
    for (const offset of Object.keys(definition.codecparFields)) {
      memory.set(definition.codecpar + Number(offset), definition.codecparFields[offset]);
    }
  }
  for (const packet of definition.packets) setFakePacket(memory, packet.pointer, packet);

  const task = actualTask(pool, memory, { video: stream });
  task.isLive = false;
  const sequence = definition.sequence.slice();
  pipeline.pullAVPacketInternal = async () => {
    const next = sequence.length ? sequence.shift() : -1048576;
    pipeline.ops.pulls.push(next);
    return next;
  };

  const value = await pipeline.syncToKeyframe(task);
  return {
    value,
    pulls: pipeline.ops.pulls.slice(),
    freed: pool.freed.slice(),
    parser: pipeline.ops.nal.slice(),
    backPacket: stream.backPacket,
    frontPacket: stream.frontPacket,
    packetEnded: stream.packetEnded,
    queue: stream.pullQueue.queue.slice(),
    ended: stream.pullQueue.ended === true,
    stream,
    ops: pipeline.ops
  };
}

test('both real modules register and expose a non-numeric frozen stale identity', { timeout: 5000 }, () => {
  assert.equal(OWNERSHIP.KIND, 'eio-mse-packet-ownership/v1');
  assert.equal(typeof OWNERSHIP.createMsePacketOwnership, 'function');
  assert.equal(PULL.KIND, 'eio-mse-pull-lifetime/v1');
  assert.equal(MODULE_SRC.includes('__webpack_require__(90193)'), true,
    '90192 must explicitly require the real ownership module');
  assert.equal(PULL.STALE, OWNERSHIP.STALE, '90192 uses the real 90193 stale identity');
  for (const method of ['begin', 'beginTickets', 'revoke', 'stale', 'check', 'transferSlot',
    'transferQueue', 'takeQueue', 'releaseSlot', 'releaseQueue', 'prepareTeardown',
    'releasePulled', 'complete', 'reclaim', 'drain', 'closePool', 'finishTickets', 'guard']) {
    assert.equal(typeof OWNERSHIP.createMsePacketOwnership({ packetPool: fakePool() })[method], 'function',
      '90193 ownership interface ' + method);
  }
  assert.equal(Object.isFrozen(PULL.STALE), true);
  assert.equal('object', typeof PULL.STALE);
  assert.equal(Number.isNaN(Number(PULL.STALE)), true, 'a stale object must not coerce into a packet or EOF number');
  for (const source of [MODULE_SRC, OWNERSHIP_SRC]) {
    for (const forbidden of ['eioQuiesce', 'eioSettleToken', 'eioDualStopAck', 'quiesced', 'EIO_PRODUCER_STOP']) {
      assert.equal(source.includes(forbidden), false, 'module must not introduce ' + forbidden);
    }
    assert.equal(source.includes('releases the 88-byte AVPacket header'), false,
      'ownership must not claim a native header/payload free it does not observe');
  }
  const numericReturns = Array.from(MODULE_SRC.matchAll(/return\s+(-?\d+)\b/g), (match) => Number(match[1]));
  assert.ok(numericReturns.length > 0, 'the real EOF return stays explicit');
  for (const value of numericReturns) {
    assert.equal(value, -1048576, 'the helper must only return the explicit EOF code, never a fabricated negative');
  }
});

test('native adapter validation accepts the real BigInt/object/Symbol ABI values', { timeout: 5000 }, () => {
  assert.equal(typeof ACTUAL_O9, 'symbol', 'actual 54568 o9 is the native address Symbol');
  const pool = fakePool();
  const native = makeNative(new Map(), pool);
  assert.equal(typeof native.noTimestamp, 'bigint');
  assert.equal(typeof native.millisecondsTimeBase, 'object');
  assert.equal(typeof native.timeBaseIndex, 'symbol');
  assert.doesNotThrow(() => PULL.createMsePullLifetime({ packetPool: pool, now: () => 0, native }));
  assert.throws(() => PULL.createMsePullLifetime({
    packetPool: pool, now: () => 0, native: Object.assign({}, native, { noTimestamp: -1 })
  }), { code: 'EIO_MSE_PULL_NATIVE_INVALID' });
  assert.throws(() => PULL.createMsePullLifetime({
    packetPool: pool, now: () => 0,
    native: Object.assign({}, native, { millisecondsTimeBase: { num: 0, den: 1000 } })
  }), { code: 'EIO_MSE_PULL_NATIVE_INVALID' });
  assert.throws(() => PULL.createMsePullLifetime({
    packetPool: pool, now: () => 0, native: Object.assign({}, native, { timeBaseIndex: {} })
  }), { code: 'EIO_MSE_PULL_NATIVE_INVALID' });
});

test('positive result is registered before stale check and reclaimed exactly once', { timeout: 5000 }, () => {
  const { pool, controller } = makeController();
  const stream = strictStream();
  const ticket = controller.begin(stream, 'swap');
  assert.ok(ticket);
  controller.revoke(stream, 'test');
  assert.equal(controller.check(ticket, 101), PULL.STALE);
  assert.deepEqual(pool.freed, [101], 'the just-resolved positive packet is freed exactly once');
  assert.equal(stream.eioPullPackets.has(101), false);
  assert.equal(ticket.state, 'settled');
});

test('a duplicate live pointer is a protocol fault, not a stale/EOF result', { timeout: 5000 }, () => {
  const { pool, controller } = makeController();
  const stream = strictStream();
  const first = controller.begin(stream, 'pull');
  assert.ok(controller.check(first, 250) > 0);
  const original = stream.eioPullPackets.get(250);
  const second = controller.begin(stream, 'pull');
  assert.throws(() => controller.check(second, 250), { code: 'EIO_MUX_PULL_OWNERSHIP_CONFLICT' });
  assert.deepEqual(pool.freed, [], 'the duplicate is retained, not double-freed');
  assert.equal(stream.eioPullPackets.get(250), original, 'the original record keeps ownership');
  assert.equal(second.state, 'settled', 'only the conflicting ticket is reclaimed');
  controller.reclaim(first, 'cleanup');
  assert.deepEqual(pool.freed, [250]);
});

test('pointer reuse is ABA-safe: an old record cannot free a new record at the same pointer', { timeout: 5000 }, () => {
  const { pool, controller } = makeController();
  const stream = strictStream();
  const first = controller.begin(stream, 'swap');
  assert.ok(controller.check(first, 202) > 0);
  const oldRecord = stream.eioPullPackets.get(202);
  assert.ok(oldRecord);
  controller.reclaim(first, 'old-freed');
  assert.deepEqual(pool.freed, [202]);

  const second = controller.begin(stream, 'swap');
  assert.ok(controller.check(second, 202) > 0);
  const newRecord = stream.eioPullPackets.get(202);
  assert.notEqual(newRecord, oldRecord);
  assert.equal(controller.releaseSlot(stream, 'back', 'never-owned-by-slot'), true,
    'an empty slot with no native pointer is a safe idempotent vacancy');
  assert.deepEqual(pool.freed, [202], 'the vacancy release made no pool call');
  assert.equal(stream.eioPullPackets.get(202), newRecord, 'the vacancy release keeps the new record owned');
  /* The old continuation still holds its old record identity. Releasing that
     expected identity must refuse instead of freeing the current record. */
  const rejected = controller.releasePulled(first, 202, 'old-continuation');
  assert.equal(rejected, false);
  assert.deepEqual(pool.freed, [202], 'the new record is not freed by the old identity');
  assert.equal(stream.eioPullPackets.get(202), newRecord);
  controller.reclaim(second, 'new-owner');
  assert.deepEqual(pool.freed, [202, 202]);
});

test('lookahead queue reclaim removes exact identities and frees owned/inflight packets once', { timeout: 5000 }, () => {
  const { pool, controller } = makeController();
  const stream = strictStream();
  const ticket = controller.begin(stream, 'pull');
  const queue = stream.pullQueue.queue;
  queue.push(9999);
  stream.eioPullQueueOwners.push(null);
  assert.ok(controller.check(ticket, 301) > 0);
  assert.ok(controller.check(ticket, 302) > 0);
  assert.ok(controller.check(ticket, 303) > 0);
  assert.equal(controller.transferQueue(ticket, 302, queue), true);
  assert.equal(controller.transferQueue(ticket, 303, queue), true);
  assert.deepEqual(queue, [9999, 302, 303]);
  controller.revoke(stream, 'lookahead');
  controller.reclaim(ticket, 'lookahead');
  assert.deepEqual(queue, [9999], 'only exact queued identities are removed; the unknown pointer is retained');
  assert.equal(stream.eioPullQueueOwners.length, 1);
  assert.deepEqual(pool.freed.sort((a, b) => a - b), [301, 302, 303]);
});

test('strict pull accepts only the real EOF code; cancellation/RPC negatives are coded faults, not EOF', { timeout: 5000 }, async () => {
  async function pullResult(value) {
    const pipeline = actual26Pipeline();
    const stream = strictStream({ type: 'video' });
    const task = actualTask(pipeline.pool, pipeline.memory, { video: stream });
    const lifetime = installLifetime(pipeline, task);
    pipeline.pullAVPacketInternal = async () => value;
    const ticket = lifetime.begin(stream, 'pull');
    try {
      const result = await pipeline.pullAVPacket(stream, task, ticket);
      return { pipeline, stream, ticket, result };
    } catch (error) {
      return { pipeline, stream, ticket, error };
    }
  }

  const eof = await pullResult(-1048576);
  assert.equal(eof.result, -1048576);
  assert.equal(eof.stream.pullQueue.ended, true, 'the proven EOF code ends the queue');
  assert.equal(eof.stream.packetEnded, false, 'pullAVPacket itself does not fake packetEnded');

  for (const negative of [-1048572, -2097152, -1]) {
    const failed = await pullResult(negative);
    assert.equal(failed.error && failed.error.code, 'EIO_MUX_PULL_NATIVE_FAILURE');
    assert.equal(failed.error.nativeCode, negative);
    assert.equal(failed.stream.pullQueue.ended, false, negative + ' must not mark EOF');
    assert.equal(failed.stream.packetEnded, false, negative + ' must not mark packetEnded');
    assert.deepEqual(failed.pipeline.pool.freed, [], 'a negative result has no packet to free');
    assert.equal(failed.ticket.state, 'settled', 'the fault ticket is reclaimed, not left pending');
  }
});

test('normal ticket completion preserves stream-owned queue records; only reclaim cancels them', { timeout: 5000 }, () => {
  const { pool, controller } = makeController();
  const stream = strictStream();
  const ticket = controller.begin(stream, 'pull');
  assert.ok(controller.check(ticket, 701) > 0);
  assert.equal(controller.transferQueue(ticket, 701, stream.pullQueue.queue), true);
  controller.complete(ticket, 'normal');
  assert.deepEqual(stream.pullQueue.queue, [701], 'a successfully completed ticket leaves the lookahead queue owned by the stream');
  assert.deepEqual(pool.freed, [], 'completion does not free a transferred queue packet');
  controller.releaseQueue(stream, 'later');
  assert.deepEqual(pool.freed, [701]);
});

test('actual 26 pullAVPacket uses the real module for video PTS repair and exact lookahead ownership', { timeout: 5000 }, async () => {
  const pipeline = actual26Pipeline();
  const memory = pipeline.memory;
  const pool = pipeline.pool;
  const stream = strictStream({
    type: 'video',
    codecpar: 4000,
    pullQueue: { queue: [], ended: false, index: 0, lastPTS: 5000n, lastDTS: 5000n, diff: 0n, frameCount: -1n, useSampleRateTimeBase: false }
  });
  const task = actualTask(pool, memory, { video: stream });
  const lifetime = installLifetime(pipeline, task);
  memory.set(4004, 27); /* H.264 codecpar plus the keyframe flag selects the real baseline RAP branch */
  memory.set(100 + 16, 0n);
  memory.set(100 + 8, 1000n);
  memory.set(100 + 36, 1);
  memory.set(200 + 16, 0n);
  memory.set(200 + 8, 6000n);
  memory.set(200 + 36, 1);
  memory.set(300 + 16, 0n);
  memory.set(300 + 8, 6500n);
  memory.set(300 + 36, 1);
  const pulls = [100, 200, 300];
  pipeline.pullAVPacketInternal = async () => pulls.shift();
  const ticket = lifetime.begin(stream, 'pull');
  const value = await pipeline.pullAVPacket(stream, task, ticket);
  assert.equal(value, 100);
  assert.deepEqual(Array.from(stream.pullQueue.queue), [200, 300]);
  assert.equal(stream.eioPullQueueOwners.length, 2);
  assert.deepEqual(Array.from(stream.eioPullQueueOwners, (r) => r.pointer), [200, 300]);
  lifetime.revoke(stream, 'test');
  lifetime.reclaim(ticket, 'test');
  assert.deepEqual(stream.pullQueue.queue, []);
  assert.deepEqual(pool.freed.sort((a, b) => a - b), [100, 200, 300]);
});

test('actual 26 seek host provides the platform buffer-removal predicate', () => {
  assert.equal(actual26Pipeline().removeAllBufferNeeded(), false);
  assert.equal(actual26Pipeline({ safari: true }).removeAllBufferNeeded(), true);
});
test('actual 26 swap defers a pull, revokes on seek, and never installs or replays the stale result', { timeout: 5000 }, async () => {
  const pipeline = actual26Pipeline();
  const pool = pipeline.pool;
  const stream = strictStream({ frontBuffered: true, frontPacket: 0, backPacket: 0 });
  const task = actualTask(pool, pipeline.memory, { video: stream });
  const lifetime = installLifetime(pipeline, task);
  let resolvePull = null;
  pipeline.pullAVPacketInternal = () => new Promise((resolve) => { resolvePull = resolve; });
  let seekSyncCalls = 0;
  stream.seekSync = () => { seekSyncCalls += 1; };
  const beforeTimestamp = stream.startTimestamp;
  assert.equal(pipeline.swap(stream, task), true);
  const pending = stream.eioSwapPending;
  assert.ok(pending);
  lifetime.revoke(stream, 'beforeSeek');
  stream.oformatContext.streams[0] = { index: 1, timeBase: { num: 1, den: 90000 } };
  resolvePull(401);
  await new Promise(setImmediate);
  assert.equal(stream.eioSwapPending, null);
  assert.equal(stream.frontPacket, 0, 'stale packet is never installed in frontPacket');
  assert.equal(stream.frontBuffered, false);
  assert.equal(stream.startTimestamp, beforeTimestamp, 'stale continuation does not mutate startTimestamp');
  assert.equal(seekSyncCalls, 0, 'revoked swap never fakes seek readiness');
  assert.deepEqual(pool.freed, [401]);
});

test('strict swap retains an unproved slot instead of zeroing or moving raw pointers', () => {
  for (const slot of ['back', 'front']) {
    const pipeline = actual26Pipeline();
    const stream = strictStream({ frontBuffered: true, [slot + 'Packet']: 5000 });
    const task = actualTask(pipeline.pool, pipeline.memory, { video: stream });
    installLifetime(pipeline, task);
    assert.throws(() => pipeline.swap(stream, task), { code: 'EIO_MUX_PULL_OWNERSHIP_UNPROVEN' });
    assert.equal(stream[slot + 'Packet'], 5000);
    assert.deepEqual(pipeline.pool.freed, []);
    assert.equal(stream.eioPullTickets ? stream.eioPullTickets.size : 0, 0);
  }
});
test('actual 26 strict swap consumes an already-registered pull exactly once', async () => {
  const pipeline = actual26Pipeline();
  const stream = strictStream({ frontBuffered: true });
  const task = actualTask(pipeline.pool, pipeline.memory, { video: stream });
  const lifetime = installLifetime(pipeline, task);
  const setup = lifetime.begin(stream, 'setup');
  lifetime.register(setup, 1000, 'setup-back');
  lifetime.transferSlot(setup, 1000, 'back');
  lifetime.register(setup, 2000, 'setup-front');
  lifetime.transferSlot(setup, 2000, 'front');
  lifetime.complete(setup, 'setup');
  setFakePacket(pipeline.memory, 3000, { pts: -1, dts: -1, flags: 0 });
  pipeline.pullAVPacketInternal = async () => 3000;
  assert.equal(pipeline.swap(stream, task), true);
  await new Promise(setImmediate);
  assert.deepEqual(pipeline.pool.freed, [1000], 'only the consumed back packet is released');
  assert.equal(stream.backPacket, 2000);
  assert.equal(stream.frontPacket, 3000, 'the real pull result becomes the next front packet');
  assert.equal(stream.frontBuffered, true);
  assert.equal(stream.eioSwapPending, null);
  assert.equal(stream.eioPullTickets.size, 0);
  assert.equal(stream.eioPullPackets.size, 2);
  assert.equal(stream.eioPullSlotOwners.front, stream.eioPullPackets.get(3000));
  assert.equal(lifetime.releaseSlot(stream, 'back', 'cleanup'), true);
  assert.equal(lifetime.releaseSlot(stream, 'front', 'cleanup'), true);
  assert.deepEqual(pipeline.pool.freed, [1000, 2000, 3000]);
});
test('actual 26 baseline swap remains the original direct-pool behavior', { timeout: 5000 }, async () => {
  const pipeline = actual26Pipeline();
  const pool = pipeline.pool;
  const stream = strictStream({ eioOwned: null, backPacket: 222, frontPacket: 111, frontBuffered: true });
  const task = actualTask(pool, pipeline.memory, { video: stream });
  pipeline.pullAVPacket = async () => 333;
  assert.equal(pipeline.swap(stream, task), true);
  await Promise.resolve();
  await Promise.resolve();
  assert.deepEqual(pool.freed, [222], 'baseline releases the old back directly');
  assert.equal(stream.backPacket, 111);
  assert.equal(stream.frontPacket, 333);
  assert.equal(stream.frontBuffered, true);
  assert.equal(Object.prototype.hasOwnProperty.call(stream, 'eioPullTickets'), false,
    'baseline streams gain no strict lifetime fields');
});

/* Differential pull semantics: the baseline side executes the actual 26
   pullAVPacket body for a non-eioOwned stream; the strict side executes the
   actual 90192/90193 delegate. Each side has independent fake native memory and
   packet sequence. h.Mr/k are local rational/memory instruments with the real
   adapter call shape, not a substitute pull algorithm. No Worker, browser,
   native ABI, upstream or cancellation pressure is involved here. */
test('differential pull matches the actual 26 baseline across native packet sequences', { timeout: 10000 }, async () => {
  const outputMs = { num: 1, den: 1000 };
  const cases = [
    {
      name: 'audio sample-rate time-base and frame count',
      type: 'audio',
      codecpar: 4000,
      codecparFields: { 144: 1024 },
      outputTimeBase: { num: 1, den: 48000 },
      lastPTS: 0n,
      lastDTS: 0n,
      diff: 0n,
      frameCount: -1n,
      useSampleRateTimeBase: true,
      packets: [
        { pointer: 1101, pts: 0n, dts: 1000n, duration: 21n, flags: 0, timeBase: outputMs },
        { pointer: 1102, pts: 0n, dts: 1024n, duration: 21n, flags: 0, timeBase: outputMs },
        { pointer: 1103, pts: 0n, dts: 2048n, duration: 21n, flags: 0, timeBase: outputMs }
      ],
      sequence: [1101, 1102, 1103],
      pullCount: 3,
      check(baseline, strict, view) {
        assert.equal(strict.ops.scale, 1, 'first sample-rate packet scales DTS into the output time base');
        assert.equal(strict.ops.copy, 3, 'every sample-rate packet copies the output time base');
        assert.equal(baseline.ops.scale, strict.ops.scale);
        assert.equal(baseline.ops.copy, strict.ops.copy);
        assert.equal(view[0].frameCount, 48000n);
        assert.equal(view[1].frameCount, 49024n);
        assert.equal(view[2].frameCount, 50048n);
        for (const result of view) {
          assert.equal(result.duration, 21n, 'duration at +64 is preserved');
          assert.equal(result.durationSentinel, -1n, 'positive pull resets the duration sentinel at +48');
          assert.deepEqual(result.timeBase, { num: 1, den: 48000 });
        }
      }
    },
    {
      name: 'video RAP out-of-order PTS with queued lookahead diff',
      type: 'video',
      codecpar: 4100,
      codecparFields: { 4: 27 },
      outputTimeBase: outputMs,
      lastPTS: 10000n,
      lastDTS: 8000n,
      diff: 0n,
      frameCount: -1n,
      useSampleRateTimeBase: false,
      packets: [
        { pointer: 1201, pts: 9000n, dts: 9000n, duration: 40n, flags: 1, timeBase: outputMs },
        { pointer: 1202, pts: 7500n, dts: 8500n, duration: 40n, flags: 0, timeBase: outputMs },
        { pointer: 1203, pts: 8000n, dts: 8800n, duration: 40n, flags: 0, timeBase: outputMs }
      ],
      sequence: [1201, 1202, 1203],
      pullCount: 3,
      check(baseline, strict, view) {
        assert.equal(view[0].pulls, 3, 'the first RAP pull performs the queued lookahead pulls');
        assert.equal(baseline.ops.pulls.length, strict.ops.pulls.length);
        assert.ok(baseline.ops.pts > 0, 'baseline PTS repair branch executed');
        assert.ok(strict.ops.pts > 0, 'strict PTS repair branch executed');
        assert.ok(strict.ops.dts > 0, 'DTS regression branch executed');
        assert.deepEqual(view[0].queue, [1202, 1203]);
        assert.equal(view[0].diff, 1000n);
        assert.equal(view[0].pts, 11000n);
        assert.equal(view[1].pts, 8500n);
        assert.equal(view[2].pts, 9000n);
      }
    },
    {
      name: 'video in-order RAP resets the queued diff',
      type: 'video',
      codecpar: 4200,
      codecparFields: { 4: 27 },
      outputTimeBase: outputMs,
      lastPTS: 1000n,
      lastDTS: 1000n,
      diff: 777n,
      frameCount: -1n,
      useSampleRateTimeBase: false,
      packets: [
        { pointer: 1301, pts: 2000n, dts: 2000n, duration: 40n, flags: 1, timeBase: outputMs },
        { pointer: 1302, pts: 2500n, dts: 1500n, duration: 40n, flags: 0, timeBase: outputMs }
      ],
      sequence: [1301, 1302],
      pullCount: 2,
      check(baseline, strict, view) {
        assert.equal(view[0].diff, 0n, 'in-order RAP resets the lookahead diff');
        assert.equal(view[0].pts, 2000n);
        assert.equal(view[1].pts, 2500n, 'non-RAP PTS uses the reset diff');
        assert.ok(strict.ops.dts > 0, 'DTS regression branch executed');
        assert.ok(baseline.ops.dts > 0, 'baseline DTS regression branch executed');
        assert.equal(strict.ops.pts, 0, 'this case does not take the PTS repair branch');
        assert.equal(baseline.ops.pulls.length, strict.ops.pulls.length);
      }
    },
    {
      name: 'negative DTS, zero DTS and normal EOF',
      type: 'audio',
      codecpar: 4300,
      outputTimeBase: outputMs,
      lastPTS: 0n,
      lastDTS: 0n,
      diff: 0n,
      frameCount: -1n,
      useSampleRateTimeBase: false,
      packets: [
        { pointer: 1401, pts: -200n, dts: -100n, duration: 23n, flags: 0, timeBase: { num: 1, den: 90000 } },
        { pointer: 1402, pts: 0n, dts: 0n, duration: 23n, flags: 0, timeBase: outputMs },
        { pointer: 1403, pts: 0n, dts: 1000n, duration: 23n, flags: 0, timeBase: outputMs }
      ],
      sequence: [1401, 1402, 1403, -1048576],
      pullCount: 5,
      check(baseline, strict, view) {
        assert.equal(view[0].pts, -200n);
        assert.equal(view[0].dts, -100n);
        assert.equal(view[0].duration, 23n, 'duration at +64 is preserved');
        assert.equal(view[0].durationSentinel, 23n,
          'negative DTS returns before the duration sentinel at +48 is written');
        assert.deepEqual(view[0].timeBase, { num: 1, den: 90000 },
          'negative DTS returns before the time-base copy');
        assert.equal(view[1].pts, 0n);
        assert.equal(view[1].dts, 0n);
        assert.equal(view[1].durationSentinel, -1n,
          'the zero-DTS packet takes the normal positive duration sentinel write');
        assert.equal(view[2].pts, 1000n);
        assert.equal(view[3].value, -1048576);
        assert.equal(view[3].ended, true);
        assert.equal(view[4].value, -1048576);
        assert.equal(view[4].pulls, 4, 'post-EOF pull is the ended short-circuit');
        assert.equal(baseline.ops.pulls.length, strict.ops.pulls.length);
      }
    },
    {
      name: 'non-NAL codec id 99 key flag does not enter the DTS repair branch',
      type: 'video',
      codecpar: 4400,
      codecparFields: { 4: 99 },
      h264Nal: () => { throw new Error('codec 99 must not probe the H.264 NAL parser'); },
      hevcNal: () => { throw new Error('codec 99 must not probe the HEVC NAL parser'); },
      outputTimeBase: outputMs,
      lastPTS: 10000n,
      lastDTS: 8000n,
      diff: 0n,
      frameCount: -1n,
      useSampleRateTimeBase: false,
      packets: [
        { pointer: 1501, pts: 5000n, dts: 6000n, duration: 40n, flags: 1, timeBase: outputMs }
      ],
      sequence: [1501],
      pullCount: 1,
      check(baseline, strict, view) {
        assert.equal(view[0].pts, 5000n, 'the original id-99 fixture keeps its PTS');
        assert.equal(strict.ops.pts, 0, 'strict does not run the video PTS repair branch');
        assert.equal(baseline.ops.pts, 0, 'baseline does not run the video PTS repair branch');
        assert.equal(strict.ops.nal.length, 0, 'codec 99 probes no NAL');
        assert.equal(baseline.ops.nal.length, 0, 'baseline codec 99 probes no NAL');
        assert.equal(view[0].pulls, 1, 'no lookahead pull is issued');
        assert.equal(view[0].diff, 0n, 'the queued diff is untouched');
      }
    },
    {
      name: 'codec 27 key flag 0 with NAL parser true does not enter the DTS repair branch',
      type: 'video',
      codecpar: 4500,
      codecparFields: { 4: 27 },
      h264Nal: () => true,
      hevcNal: () => { throw new Error('codec 27 must not probe the HEVC NAL parser'); },
      outputTimeBase: outputMs,
      lastPTS: 10000n,
      lastDTS: 8000n,
      diff: 0n,
      frameCount: -1n,
      useSampleRateTimeBase: false,
      packets: [
        { pointer: 1601, pts: 5000n, dts: 6000n, duration: 40n, flags: 0, timeBase: outputMs }
      ],
      sequence: [1601],
      pullCount: 1,
      check(baseline, strict, view) {
        assert.equal(view[0].pts, 5000n, 'key flag 0 keeps the original PTS even when the NAL parser would accept it');
        assert.equal(strict.ops.pts, 0, 'strict pull requires the outer key flag before the NAL branch');
        assert.equal(baseline.ops.pts, 0, 'baseline pull requires the outer key flag before the NAL branch');
        assert.equal(strict.ops.nal.length, 0, 'the outer flag short-circuits the NAL probe');
        assert.equal(baseline.ops.nal.length, 0, 'baseline also short-circuits the NAL probe');
        assert.equal(view[0].pulls, 1, 'no lookahead pull is issued');
        assert.equal(view[0].diff, 0n, 'the queued diff is untouched');
      }
    }
  ];

  for (const definition of cases) {
    const baseline = await runDifferentialSide(definition, false);
    const strict = await runDifferentialSide(definition, true);
    assert.deepEqual(comparableDifferential(strict), comparableDifferential(baseline),
      definition.name + ': strict pull matches the actual 26 baseline');
    assert.deepEqual(strict.stream.pullQueue.queue, baseline.stream.pullQueue.queue,
      definition.name + ': lookahead queue order');
    assert.equal(strict.stream.pullQueue.ended, baseline.stream.pullQueue.ended,
      definition.name + ': EOF flag');
    definition.check(baseline, strict, strict.results);
  }
});

test('differential syncToKeyframe preserves independent key-flag and codec/NAL RAP acceptance', { timeout: 10000 }, async () => {
  const outputMs = { num: 1, den: 1000 };
  const cases = [
    {
      name: 'key flag 1 on non-H.264 codec 99 is accepted without a NAL probe',
      codecpar: 4700,
      codecparFields: { 4: 99 },
      outputTimeBase: outputMs,
      h264Nal: () => { throw new Error('codec 99 must not probe the H.264 NAL parser'); },
      hevcNal: () => { throw new Error('codec 99 must not probe the HEVC NAL parser'); },
      packets: [
        { pointer: 2101, pts: 1000n, dts: 1000n, duration: 40n, flags: 1, timeBase: outputMs }
      ],
      sequence: [2101],
      expected: {
        value: true,
        backPacket: 2101,
        freed: [],
        parser: []
      }
    },
    {
      name: 'key flag 0 on codec 27 with NAL parser true is accepted immediately',
      codecpar: 4800,
      codecparFields: { 4: 27 },
      outputTimeBase: outputMs,
      h264Nal: () => true,
      hevcNal: () => { throw new Error('codec 27 must not probe the HEVC NAL parser'); },
      packets: [
        { pointer: 2201, pts: 1000n, dts: 1000n, duration: 40n, flags: 0, timeBase: outputMs }
      ],
      sequence: [2201],
      expected: {
        value: true,
        backPacket: 2201,
        freed: [],
        parser: [{ codec: 27, packet: 2201, length: 4 }]
      }
    },
    {
      name: 'key flag 0 codec 27 parser false skips to the next RAP and returns original false',
      codecpar: 4900,
      codecparFields: { 4: 27 },
      outputTimeBase: outputMs,
      h264Nal: (packet) => packet !== 2301,
      hevcNal: () => { throw new Error('codec 27 must not probe the HEVC NAL parser'); },
      packets: [
        { pointer: 2301, pts: 1000n, dts: 1000n, duration: 40n, flags: 0, timeBase: outputMs },
        { pointer: 2302, pts: 2000n, dts: 2000n, duration: 40n, flags: 1, timeBase: outputMs }
      ],
      sequence: [2301, 2302],
      expected: {
        value: false,
        backPacket: 2302,
        freed: [2301],
        parser: [
          { codec: 27, packet: 2301, length: 4 },
          { codec: 27, packet: 2302, length: 4 }
        ]
      }
    }
  ];

  for (const definition of cases) {
    const baseline = await runSyncDifferentialSide(definition, false);
    const strict = await runSyncDifferentialSide(definition, true);
    assert.equal(strict.value, baseline.value, definition.name + ': return value');
    assert.equal(strict.value, definition.expected.value, definition.name + ': baseline RAP contract');
    assert.equal(strict.backPacket, baseline.backPacket, definition.name + ': selected backPacket');
    assert.equal(strict.backPacket, definition.expected.backPacket, definition.name + ': expected RAP packet');
    assert.deepEqual(strict.pulls, baseline.pulls, definition.name + ': pull sequence');
    assert.deepEqual(strict.freed, baseline.freed, definition.name + ': released packets');
    assert.deepEqual(strict.freed, definition.expected.freed, definition.name + ': expected rejected packets');
    assert.deepEqual(strict.parser, baseline.parser, definition.name + ': scoped NAL probes');
    assert.deepEqual(strict.parser, definition.expected.parser, definition.name + ': expected NAL probes');
    assert.deepEqual(strict.queue, baseline.queue, definition.name + ': lookahead queue');
    assert.equal(strict.ended, baseline.ended, definition.name + ': EOF flag');
    assert.equal(strict.packetEnded, baseline.packetEnded, definition.name + ': packetEnded flag');
  }
});

test('beforeSeek release failure keeps the packet/queue state and does not advance seeking', { timeout: 5000 }, async () => {
  const calls = [];
  const pool = {
    alloc() { return 900; },
    release() { throw new Error('native release failed'); }
  };
  const memory = new Map();
  const native = makeNative(memory, pool);
  const controller = PULL.createMsePullLifetime({ packetPool: pool, now: () => 0, native });
  const stream = strictStream({
    type: 'audio', backPacket: 0, packetEnded: true, timestampOffsetUpdated: true,
    track: { reset() { calls.push('track-reset'); } }
  });
  stream.pullQueue.ended = true;
  stream.pullQueue.index = 7;
  stream.pullQueue.lastPTS = 123n;
  stream.pullQueue.lastDTS = 456n;
  const task = actualTask(pool, memory, { audio: stream });
  task.seeking = false;
  const setup = controller.begin(stream, 'setup');
  assert.ok(controller.check(setup, 800) > 0);
  assert.equal(controller.transferSlot(setup, 800, 'back'), true);
  controller.complete(setup, 'setup');
  assert.equal(stream.backPacket, 800);
  const host = {
    eioCancelWake: () => calls.push('wake'),
    eioAfterSeekFlush: async () => calls.push('flush'),
    eioDiscardQueue: () => calls.push('discard'),
    removeAllBufferNeeded: () => false,
    logSeekEnd: () => calls.push('seek-end')
  };
  await assert.rejects(controller.beforeSeek(task, host),
    { code: 'EIO_MUX_PULL_OWNERSHIP_UNPROVEN' });
  assert.equal(stream.backPacket, 800, 'failed release leaves the packet pointer owned');
  assert.equal(stream.eioPullSlotOwners.back.pointer, 800, 'failed release keeps the slot owner');
  assert.equal(stream.packetEnded, true, 'packetEnded is not reset after failed release');
  assert.equal(stream.timestampOffsetUpdated, true, 'timestamp state is not reset after failed release');
  assert.equal(stream.pullQueue.ended, true, 'queue EOF state is not reset after failed release');
  assert.equal(stream.pullQueue.index, 7);
  assert.equal(stream.pullQueue.lastPTS, 123n);
  assert.equal(stream.pullQueue.lastDTS, 456n);
  assert.equal(task.seeking, false, 'seeking is not advanced on ownership failure');
  assert.equal(calls.includes('seek-end'), false);
  assert.equal(calls.includes('track-reset'), true, 'the original flush/reset path still ran before release proof');
});

test('bounded beforeSeek refusal happens before any destructive release; later result is reclaimed once', { timeout: 5000 }, async () => {
  const { pool, controller } = makeController();
  const stream = strictStream({ type: 'audio' });
  const task = actualTask(pool, new Map(), { audio: stream });
  const calls = [];
  task.eioCancelWake = () => calls.push('wake');
  const host = {
    eioCancelWake: (s) => calls.push(s === stream ? 'wake-audio' : 'wake-other'),
    eioAfterSeekFlush: async () => calls.push('flush'),
    eioDiscardQueue: () => calls.push('discard'),
    removeAllBufferNeeded: () => false,
    logSeekEnd: () => calls.push('end')
  };
  const ticket = controller.begin(stream, 'swap');
  const started = Date.now();
  await assert.rejects(controller.beforeSeek(task, host), { code: 'EIO_MUX_PULL_SETTLE_TIMEOUT' });
  const elapsed = Date.now() - started;
  assert.ok(elapsed >= 1400 && elapsed < 4000, 'the local bound is the strict 1500 ms drain');
  assert.equal(task.seeking, false);
  assert.equal(calls.includes('flush') || calls.includes('discard'), false,
    'no format flush or queue discard ran before the refusal');
  assert.equal(stream.track.reset.called, undefined);
  assert.deepEqual(pool.freed, [], 'no packet free occurred on the timeout path');
  assert.equal(stream.eioPullTickets.has(ticket), true, 'the revoked ticket stays tracked for its eventual result');
  controller.check(ticket, 402);
  assert.deepEqual(pool.freed, [402]);
});

test('afterSeek revocation during sync cannot restart the old loop', { timeout: 5000 }, async () => {
  const { controller } = makeController();
  const stream = strictStream({ type: 'video', loop: { start() { started += 1; } } });
  let started = 0;
  const task = actualTask(fakePool(), new Map(), { video: stream });
  let resolveSync = null;
  const host = {
    syncToKeyframe: () => new Promise((resolve) => { resolveSync = resolve; }),
    eioAfterSeekWrite: async () => {},
    eioAfterSeekFlush: async () => {},
    eioAddQueue: () => {},
    seekSeconds: () => 0,
    logSeekDone: () => {}
  };
  const pending = controller.afterSeek(task, host, BigInt(0));
  const ticket = Array.from(stream.eioPullTickets)[0];
  assert.ok(ticket);
  controller.revoke(stream, 'afterSeek-race');
  resolveSync(true);
  await assert.rejects(pending, { code: 'EIO_MUX_PULL_STALE' });
  assert.equal(started, 0, 'a revoked afterSeek never calls loop.start()');
  assert.equal(ticket.state, 'settled');
});

test('strict seek callback waits cancel through the shipped lifecycle adapter without advancing state', { timeout: 5000 }, async () => {
  for (const phase of ['remove', 'queue', 'insert']) {
    const { pool, memory, controller } = makeController();
    let callback;
    let registered;
    const ready = new Promise((resolve) => { registered = resolve; });
    const calls = [];
    const hold = (done) => { callback = done; registered(); };
    const stream = strictStream({
      type: 'audio', packetEnded: phase !== 'insert',
      loop: { start() { calls.push('start'); }, stop() { calls.push('stop'); } },
      track: {
        reset() { calls.push('reset'); },
        removeAllBuffer: hold,
        insertEnqueueCallback: hold,
        getBufferedStart() { return 0; },
        getBufferedEnd() { return 1; }
      }
    });
    const task = actualTask(pool, memory, { audio: stream });
    task.eioOwnedResources = {};
    task.cacheDuration = 0n;
    task.seeking = true;
    let pointer = 800;
    const host = {
      eioMuxWait: MUX.waitCallback,
      eioCancelWake() {},
      eioAfterSeekFlush: async () => {},
      eioDiscardQueue() {},
      removeAllBufferNeeded: () => true,
      syncToKeyframe: async () => true,
      pullAVPacket: async (s, t, ticket) => {
        const value = pointer;
        pointer += 200;
        return controller.check(ticket, value);
      },
      eioAddQueue(track, s, done) {
        if (done) { if (phase === 'queue') hold(done); else done(); }
      },
      seekSeconds: () => 0,
      logSeekDone() { calls.push('done'); },
      logSeekEnd() { calls.push('end'); }
    };
    const pending = MUX.run(task, 'seek-test', () => phase === 'remove'
      ? controller.beforeSeek(task, host) : controller.afterSeek(task, host, 0n));
    const rejected = assert.rejects(pending, { code: 'EIO_MUX_CALLBACK_CANCELLED' });
    await ready;
    assert.equal(task.eioMuxCallbackWaits.size, 1, phase + ': actual adapter owns the wait');
    MUX.gate(task).close('test-cancel');
    MUX.cancelWaits(task);
    await rejected;
    await MUX.gate(task).drain(100);
    assert.equal(task.eioMuxCallbackWaits.size, 0);
    assert.equal(task.seeking, true, phase + ': cancellation never reports seek completion');
    assert.equal(stream.eioPullTickets.size, 0, phase + ': pull scope is finished');
    assert.equal(calls.includes('done') || calls.includes('end') || calls.includes('reset'), false);
    if (phase === 'insert') {
      assert.deepEqual(calls, ['start', 'stop'], 'only the loop started by this run is stopped');
      assert.equal(stream.backPacket, 800);
      assert.equal(stream.frontPacket, 1000);
      assert.deepEqual(pool.freed, [], 'cancellation does not free transferred slots');
      assert.equal(controller.releaseSlot(stream, 'back', 'test-cleanup'), true);
      assert.equal(controller.releaseSlot(stream, 'front', 'test-cleanup'), true);
      assert.deepEqual(pool.freed, [800, 1000]);
    }
    callback();
    await Promise.resolve();
    assert.equal(task.seeking, true, phase + ': a late callback cannot resume the cancelled run');
  }
});

function teardownFixture(pool, stream, calls) {
  const memory = new Map();
  const native = makeNative(memory, pool);
  native.releaseCodecpar = (pointer) => calls.push('codecpar:' + pointer);
  const controller = PULL.createMsePullLifetime({ packetPool: pool, now: () => 0, native });
  const task = actualTask(pool, memory, { audio: stream });
  task.cacheDuration = 500n;
  task.tasks.set(task.taskId, task);
  task.controlIPCPort = { notify() {}, destroy() { calls.push('control-port'); } };
  stream.loop = { stop() { calls.push('loop-stop'); }, destroy() { calls.push('loop-destroy'); } };
  stream.oformatContext = Object.assign({}, stream.oformatContext, {
    destroy: async () => { calls.push('format-destroy'); }
  });
  stream.pullIPC = { destroy() { calls.push('pull-ipc'); } };
  stream.codecpar = 700;
  const host = {
    eioCancelWake: () => calls.push('wake'),
    eioAbortStaleOperation: () => calls.push('abort-stale'),
    eioReleaseStream: () => calls.push('release-stream'),
    tasks: task.tasks
  };
  return { controller, task, stream, host, calls };
}

test('unregister drain timeout performs no native/context teardown and can retry after settlement', { timeout: 7000 }, async () => {
  const calls = [];
  const pool = fakePool();
  const stream = strictStream({ type: 'audio' });
  const fixture = teardownFixture(pool, stream, calls);
  const ticket = fixture.controller.begin(stream, 'swap');
  assert.ok(ticket);
  await assert.rejects(fixture.controller.unregister(fixture.task, fixture.host),
    { code: 'EIO_MUX_PULL_TEARDOWN_UNSETTLED' });
  assert.deepEqual(pool.freed, [], 'no packet free on an unsettled drain');
  assert.equal(stream.eioPullPoolClosed, false, 'pool is not closed on timeout');
  assert.equal(fixture.task.tasks.has(fixture.task.taskId), true, 'task is retained');
  for (const destructive of ['format-destroy', 'loop-destroy', 'pull-ipc', 'control-port',
    'release-stream']) {
    assert.equal(calls.includes(destructive), false, destructive + ' must not run on timeout');
  }
  assert.equal(calls.some((c) => c.startsWith('codecpar:')), false,
    'codecpar must not be released on timeout');

  fixture.controller.reclaim(ticket, 'test-settled-after-timeout');
  await fixture.controller.unregister(fixture.task, fixture.host);
  assert.equal(fixture.task.tasks.has(fixture.task.taskId), false, 'settled retry completes teardown');
  for (const destructive of ['format-destroy', 'loop-destroy', 'pull-ipc', 'control-port',
    'release-stream', 'codecpar:700']) {
    assert.equal(calls.includes(destructive), true, destructive + ' must run after real settle');
  }
  assert.equal(stream.eioPullPoolClosed, true);
});

test('a retained packet record blocks unregister before any destructive cleanup', { timeout: 5000 }, async () => {
  const calls = [];
  let refused = false;
  const pool = {
    alloc() { return 900; },
    release(pointer) {
      if (!refused) { refused = true; throw new Error('release refused'); }
      return undefined;
    }
  };
  const stream = strictStream({ type: 'audio' });
  const fixture = teardownFixture(pool, stream, calls);
  const ticket = fixture.controller.begin(stream, 'swap');
  assert.ok(fixture.controller.check(ticket, 601) > 0);
  fixture.controller.reclaim(ticket, 'retain-on-release-failure');
  assert.equal(stream.eioPullPackets.size, 1, 'failed release stays retained');
  await assert.rejects(fixture.controller.unregister(fixture.task, fixture.host),
    { code: 'EIO_MUX_PULL_TEARDOWN_UNSETTLED' });
  assert.equal(fixture.task.tasks.has(fixture.task.taskId), true);
  assert.equal(stream.eioPullPoolClosed, false);
  for (const destructive of ['format-destroy', 'loop-destroy', 'pull-ipc', 'control-port',
    'release-stream']) {
    assert.equal(calls.includes(destructive), false, destructive + ' blocked by retained ownership');
  }
});

test('unregister revocation/loop stop order and beforeSeek revoke-before-release are present in the actual source', { timeout: 5000 }, () => {
  const beforeSeek = slice(CHUNK26_SRC, 'async eioBeforeSeekInternal(t){', 'async eioAfterSeekInternal(t,e){');
  assert.ok(beforeSeek.includes('try{await this.eioPullLifetime(e).beforeSeek(e,this)}catch(error){this.eioRecordPullError(e,"beforeSeek",error);throw error}return{eioAck:1,pullSettled:!0}'));
  assert.ok(beforeSeek.indexOf('if(!e)return;') < beforeSeek.indexOf('var i,a;const r=[];'));

  const swap = slice(CHUNK26_SRC, 'swap(t,e){', 'createLoop(t,e){');
  assert.ok(swap.startsWith('swap(t,e){if(t.eioOwned)return this.eioPullLifetime(e).swap(e,t,this);if(t.backPacket'));
  assert.ok(swap.includes('if(t.backPacket&&e.avpacketPool.release(t.backPacket)'));

  const pull = slice(CHUNK26_SRC, 'async pullAVPacket(t,e,eioTicket){', 'writeAVPacket(t,e){');
  assert.ok(pull.startsWith('async pullAVPacket(t,e,eioTicket){if(t.eioOwned)return this.eioPullLifetime(e).pullAVPacket(t,e,this,eioTicket);'));

  const afterSeek = slice(CHUNK26_SRC, 'async eioAfterSeekInternal(t,e){', 'async setPlayRate(t,e){');
  assert.ok(afterSeek.startsWith('async eioAfterSeekInternal(t,e){const i=this.tasks.get(t);if(!i)return 0;if(this.eioPullHasStrict(i)){const l=await this.eioPullLifetime(i).afterSeek(i,this,e);'));
  assert.ok(afterSeek.includes('EIO_MUX_SEEK_RESULT_INVALID'));
  assert.ok(afterSeek.includes('return{eioAck:1,seekTime:l}'));

  const unregister = slice(CHUNK26_SRC, 'async eioUnregisterInternal(t){', '}}},2633:');
  assert.ok(unregister.startsWith('async eioUnregisterInternal(t){const e=this.tasks.get(t);if(!e)return;if(this.eioPullHasStrict(e))return this.eioPullLifetime(e).unregister(e,this);'));

  const reset = slice(CHUNK26_SRC, 'async resetResource(t,e,eioMutation){', 'async eioAddStreamInternal(');
  assert.ok(reset.startsWith('async resetResource(t,e,eioMutation){if(t&&t.eioOwned){this.eioRequireMuxMutation(e,eioMutation);this.eioPullLifetime(e).resetResource(t,e);await t.oformatContext.oformat.destroy(t.oformatContext);this.eioRequireMuxMutation(e,eioMutation)}'),
    'strict reset requires exclusive mutation ownership before release and after async format destruction');

  const handler = slice(CHUNK26_SRC, 'getSourceOpenHandler(t){', 'getMimeType(t){');
  assert.ok(handler.startsWith('getSourceOpenHandler(t){let e=arguments.length>1'));

  const sourceOpen = slice(MODULE_SRC, 'async function sourceOpen(task, host, startTime) {', 'async function startMux(stream, task, host, startTime, ticket) {');
  assert.ok(sourceOpen.indexOf('await host.syncToKeyframe(task, tickets);') < sourceOpen.indexOf("task.eioSourceOpenStage = 'synced';"));
  assert.ok(sourceOpen.includes('guard(tickets, \'sourceOpen-sync\')'));

  const unregisterModule = slice(MODULE_SRC, 'async function unregister(task, host) {', 'async function teardownStream(task, stream, host) {');
  const stop = unregisterModule.indexOf('task.audio.loop.stop()');
  const destroy = unregisterModule.indexOf('await task.audio.oformatContext.destroy()');
  assert.ok(stop >= 0 && destroy > stop, 'strict unregister stops loops before the first format destroy await');
  assert.ok(unregisterModule.indexOf("revoke(streams[index], 'unregister')") < destroy);
  const drainThrow = unregisterModule.indexOf('throw unsettled;');
  const prepare = unregisterModule.indexOf('prepareTeardown(streams[teardownIndex]');
  assert.ok(drainThrow >= 0 && drainThrow < prepare && prepare < destroy,
    'drain timeout throws before packet proof, and packet proof precedes format destroy');
  assert.ok(unregisterModule.includes('planReason: proof.planReason'),
    'ownership-unproven teardown is a distinct bounded failure');
});

test('actual 26 sourceOpen pulls strict post-sync audio/video back packets for both handler capture paths', { timeout: 10000 }, async () => {
  for (const installBeforeStreams of [true, false]) {
    const fixture = sourceOpenFixture({ installBeforeStreams });
    await fixture.handler();
    assert.equal(fixture.task.eioSourceOpenStage, 'ready',
      'sourceOpen reaches ready when the handler is installed ' +
      (installBeforeStreams ? 'before addStream (actual createTask order)' : 'after addStream'));
    assert.notEqual(fixture.task.eioSourceOpenError && fixture.task.eioSourceOpenError.code,
      'EIO_MUX_PULL_TICKET_REQUIRED', 'strict pulls always use a real ticket');
    assertSourceOpenSlots(fixture, {
      audioBack: 5601,
      audioFront: 5801,
      videoBack: 6601,
      videoFront: 6801,
      freed: [5001, 5201, 5401, 6001, 6201, 6401]
    });
    assert.equal(fixture.audio.eioPullPackets.size, 2, 'audio keeps only the two stream-owned slot packets');
    assert.equal(fixture.video.eioPullPackets.size, 2, 'video keeps only the two stream-owned slot packets');
    const lifetime = fixture.task.eioPullLifetime;
    assert.equal(lifetime.releaseSlot(fixture.audio, 'back', 'test-cleanup'), true);
    assert.equal(lifetime.releaseSlot(fixture.audio, 'front', 'test-cleanup'), true);
    assert.equal(lifetime.releaseSlot(fixture.video, 'back', 'test-cleanup'), true);
    assert.equal(lifetime.releaseSlot(fixture.video, 'front', 'test-cleanup'), true);
    assert.equal(fixture.audio.eioPullPackets.size, 0);
    assert.equal(fixture.video.eioPullPackets.size, 0);
    assert.deepEqual(fixture.pool.freed.slice().sort((a, b) => a - b),
      [5001, 5201, 5401, 5601, 5801, 6001, 6201, 6401, 6601, 6801],
      'each sourceOpen packet is released exactly once after cleanup');
  }
});

test('actual sourceOpen failure before/after a post-sync pull reclaims or retains by exact identity', { timeout: 10000 }, async () => {
  const before = sourceOpenFixture({ failurePointer: 5601 });
  await assert.rejects(before.handler(), { code: 'EIO_TEST_INJECTED_PULL' });
  assert.equal(before.task.eioSourceOpenStage, 'synced',
    'the failure lands in the post-sync backPacket pull, matching the reported stage');
  assert.equal(before.audio.backPacket, 0);
  assert.equal(before.audio.frontPacket, 0);
  assert.equal(before.video.backPacket, 0);
  assert.equal(before.audio.eioPullTickets.size, 0, 'the failed implicit/explicit pull scope is reclaimed');
  assert.equal(before.video.eioPullTickets.size, 0);
  assert.equal(before.audio.eioPullPackets.size, 0, 'no packet exists before the failing pull');
  assert.deepEqual(before.pool.freed.slice().sort((a, b) => a - b),
    [5001, 5201, 5401, 6001, 6201, 6401]);

  const after = sourceOpenFixture({ failurePointer: 5801 });
  await assert.rejects(after.handler(), { code: 'EIO_TEST_INJECTED_PULL' });
  assert.equal(after.task.eioSourceOpenStage, 'synced');
  assert.equal(after.audio.backPacket, 5601, 'the packet transferred before the failure stays in its exact slot');
  assert.equal(after.audio.frontPacket, 0);
  assert.equal(after.video.backPacket, 0);
  assert.equal(after.audio.eioPullSlotOwners.back.pointer, 5601);
  assert.equal(after.audio.eioPullTickets.size, 0, 'the failed ticket is finished once, not left pending');
  assert.equal(after.video.eioPullTickets.size, 0);
  assert.equal(after.audio.eioPullPackets.size, 1, 'only the exact retained slot record remains');
  assert.deepEqual(after.pool.freed.slice().sort((a, b) => a - b),
    [5001, 5201, 5401, 6001, 6201, 6401],
    'a transferred packet is retained rather than reclaimed by the failed scope');
  const lifetime = after.task.eioPullLifetime;
  assert.equal(lifetime.releaseSlot(after.audio, 'back', 'test-cleanup'), true);
  assert.equal(after.audio.eioPullPackets.size, 0);
  assert.deepEqual(after.pool.freed.slice().sort((a, b) => a - b),
    [5001, 5201, 5401, 5601, 6001, 6201, 6401],
    'the retained identity is released exactly once by its slot owner');
});
