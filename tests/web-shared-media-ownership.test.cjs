'use strict';

/*
 * Tests for the shared last-free ownership primitive.
 *
 * Real node:worker_threads workers are used for every cross-worker claim.
 * Async tests have an explicit timeout and terminate all workers in a
 * finally block. No application server, upstream, browser or production
 * instance is involved.
 *
 * The Q7/13486 model harness below is a semantic extraction, not a
 * byte-accurate build of avplayer.js and not vendor coverage. A later test
 * executes the embedded real module 13486 factory with only its require
 * dependencies mocked, after checking that embedding against the worktree
 * libmedia source graph (main avplayer.js plus chunks 90201..90204); even that
 * test does not install the dual-demux hooks and does not claim hook or
 * whole-pipeline coverage.
 */

const test = require('node:test');
const assert = require('node:assert/strict');
const path = require('node:path');
const v8 = require('node:v8');
const { Worker } = require('node:worker_threads');
const readLibmediaSourceGraph = require('./web-libmedia-source-graph.cjs');

const sourceGraph = readLibmediaSourceGraph();
const MODULE_PATH = path.join(__dirname, '..', 'web-src', 'player-shared-media-ownership.js');
const mod = require(MODULE_PATH);
const CHUNK = 1024 * 1024;

const WORKER_SOURCE = String.raw`
'use strict';
const { parentPort, workerData } = require('node:worker_threads');
const ownership = require(workerData.modulePath);
let ctl = null;

function snapshot() {
  return ctl.snapshot();
}

function wrap(result) {
  const out = Object.assign({}, result);
  if (ctl) out.snapshot = ctl.snapshot();
  return out;
}

async function handle(msg) {
  switch (msg.op) {
    case 'create': {
      ctl = ownership.create({ capacityBytes: msg.capacityBytes, maxEntries: msg.maxEntries });
      return { descriptor: ctl.descriptor, snapshot: ctl.snapshot() };
    }
    case 'attach': {
      ctl = ownership.attach(msg.descriptor);
      return { descriptor: ctl.descriptor, snapshot: ctl.snapshot() };
    }
    case 'reserve':
      return wrap(ctl.reserve(msg.bytes, msg.generation));
    case 'reserveRace': {
      const signal = new Int32Array(msg.signal);
      const originalCompareExchange = Atomics.compareExchange;
      let armed = true;
      Atomics.compareExchange = function (array, index, expected, replacement) {
        const result = originalCompareExchange.call(Atomics, array, index, expected, replacement);
        if (armed && index === ownership.LAYOUT.header.HELD &&
            result === expected && expected === msg.oldHeld &&
            replacement === msg.oldHeld + msg.bytes) {
          armed = false;
          Atomics.store(signal, 0, 1);
          Atomics.notify(signal, 0);
          Atomics.wait(signal, 1, 0, 2000);
        }
        return result;
      };
      try {
        return wrap(ctl.reserve(msg.bytes, msg.generation));
      } finally {
        Atomics.compareExchange = originalCompareExchange;
      }
    }
    case 'bind':
      return wrap(ctl.bind(msg.token, { data: msg.data, buffer: msg.buffer, size: msg.size }));
    case 'bindBarrier': {
      const start = new Int32Array(msg.start);
      const ready = new Int32Array(msg.ready);
      Atomics.add(ready, 0, 1);
      Atomics.notify(ready, 0);
      Atomics.wait(start, 0, 0, 2000);
      return wrap(ctl.bind(msg.token, { data: msg.data, buffer: msg.buffer, size: msg.size }));
    }
    case 'reclaim':
      return wrap(ctl.reclaimUnused(msg.token));
    case 'prepare':
      return wrap(ctl.prepareFinalFree({ data: msg.data, buffer: msg.buffer }));
    case 'commit': {
      const evidence = msg.dataFreed === undefined ? undefined : { dataFreed: msg.dataFreed === true };
      return wrap(ctl.commitFinalFree(msg.ticket, evidence));
    }
    case 'snapshot':
      return ctl.snapshot();
    case 'lookup':
      return ctl.lookup({ data: msg.data, buffer: msg.buffer });
    case 'fakeFree':
      return { data: msg.data, buffer: msg.buffer, freed: true };
    case 'burst': {
      const tokens = [];
      const failures = [];
      for (let attempt = 0; attempt < msg.attempts; attempt += 1) {
        const result = ctl.reserve(msg.bytes, msg.generation);
        if (result.ok) {
          tokens.push(result.token);
        } else {
          failures.push(result.code);
          break;
        }
      }
      return { tokens, failures, snapshot: ctl.snapshot() };
    }
    case 'releaseTokens': {
      const results = [];
      for (const token of msg.tokens) {
        const result = ctl.reclaimUnused(token);
        results.push(result.ok ? 'reclaimed' : result.code);
      }
      return { results, snapshot: ctl.snapshot() };
    }
    case 'waitChange':
      return ctl.waitForChange(msg.previousSequence, msg.timeoutMs);
    default:
      throw Object.assign(new Error('unknown worker op ' + String(msg.op)), { code: 'invalid-argument' });
  }
}

parentPort.on('message', (msg) => {
  Promise.resolve()
    .then(() => handle(msg))
    .then(
      (value) => parentPort.postMessage({ id: msg.id, ok: true, value }),
      (error) => parentPort.postMessage({
        id: msg.id,
        ok: false,
        error: {
          message: String((error && error.message) || error),
          code: (error && error.code) || 'error'
        }
      })
    );
});
`;

function codedError(code, message) {
  const error = new Error(message || code);
  error.code = code;
  return error;
}

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function cloneValue(value) {
  if (typeof structuredClone === 'function') return structuredClone(value);
  return v8.deserialize(v8.serialize(value));
}

function entryStateCounts(ctl) {
  const descriptor = ctl.descriptor;
  const view = new Int32Array(descriptor.sab);
  const counts = { reserved: 0, binding: 0, bound: 0, closing: 0, committing: 0, other: 0 };
  // Entries begin after the descriptor's header. mod.LAYOUT.header is a table of
  // header *field indexes* (ENTRIES_OFFSET: 5 is the index of the stored word),
  // so mod.LAYOUT.header.entriesOffset is undefined; use the descriptor value
  // and pin it to the module's header word count.
  assert.ok(Number.isInteger(descriptor.entriesOffset),
    'descriptor.entriesOffset must be an integer word index');
  assert.equal(descriptor.entriesOffset, mod.LAYOUT.headerWords,
    'descriptor.entriesOffset must equal the module header word count');
  const base = descriptor.entriesOffset;
  const words = mod.LAYOUT.entryWords;
  const stateWord = mod.LAYOUT.entry.STATE;
  for (let index = 0; index < descriptor.maxEntries; index += 1) {
    const state = Atomics.load(view, base + index * words + stateWord);
    if (state === mod.STATE.RESERVED) counts.reserved += 1;
    else if (state === mod.STATE.BINDING) counts.binding += 1;
    else if (state === mod.STATE.BOUND) counts.bound += 1;
    else if (state === mod.STATE.CLOSING) counts.closing += 1;
    else if (state === mod.STATE.COMMITTING) counts.committing += 1;
    else if (state !== mod.STATE.FREE) counts.other += 1;
  }
  return counts;
}

async function bindAtBarrier(group, workers, messages) {
  const startSab = new SharedArrayBuffer(8);
  const readySab = new SharedArrayBuffer(8);
  const start = new Int32Array(startSab);
  const ready = new Int32Array(readySab);
  const pending = workers.map((worker, index) => group.request(worker, 'bindBarrier',
    Object.assign({}, messages[index], { start: startSab, ready: readySab }), 4000));
  while (Atomics.load(ready, 0) < workers.length) {
    const current = Atomics.load(ready, 0);
    if (Atomics.wait(ready, 0, current, 2000) === 'timed-out') break;
  }
  assert.equal(Atomics.load(ready, 0), workers.length, 'binders did not reach the barrier');
  Atomics.store(start, 0, 1);
  Atomics.notify(start, 0, workers.length);
  return Promise.all(pending);
}

function createGroup() {
  const workers = [];
  let sequence = 0;

  function spawn() {
    const worker = new Worker(WORKER_SOURCE, {
      eval: true,
      workerData: { modulePath: MODULE_PATH }
    });
    workers.push(worker);
    return worker;
  }

  function request(worker, op, payload, timeoutMs) {
    const id = ++sequence;
    const limit = timeoutMs === undefined ? 2000 : timeoutMs;
    return new Promise((resolve, reject) => {
      let settled = false;
      let timer = null;
      const finish = (fn, value) => {
        if (settled) return;
        settled = true;
        if (timer) clearTimeout(timer);
        worker.off('message', onMessage);
        worker.off('error', onError);
        worker.off('exit', onExit);
        fn(value);
      };
      const onMessage = (message) => {
        if (!message || message.id !== id) return;
        if (message.ok) {
          finish(resolve, message.value);
        } else {
          const info = message.error || {};
          finish(reject, codedError(info.code || 'worker-error', info.message || 'worker error'));
        }
      };
      const onError = (error) => finish(reject, error);
      const onExit = (code) => finish(reject, codedError(
        'worker-exit',
        'worker exited with code ' + String(code) + ' while handling ' + op
      ));
      timer = setTimeout(() => finish(reject, codedError(
        'test-timeout',
        'timed out waiting for ' + op
      )), limit);
      worker.on('message', onMessage);
      worker.on('error', onError);
      worker.on('exit', onExit);
      worker.postMessage(Object.assign({ id, op }, payload || {}));
    });
  }

  async function terminateAll() {
    await Promise.all(workers.map((worker) => worker.terminate().catch(() => {})));
  }

  return { spawn, request, terminateAll };
}

async function withWorkers(fn, budgetMs) {
  const group = createGroup();
  const budget = budgetMs === undefined ? 4200 : budgetMs;
  let timer = null;
  const watchdog = new Promise((resolve, reject) => {
    timer = setTimeout(() => reject(codedError('test-timeout', 'test budget of ' + budget + 'ms exceeded')), budget);
  });
  const run = Promise.resolve().then(() => fn(group));
  run.catch(() => {});
  try {
    return await Promise.race([run, watchdog]);
  } finally {
    if (timer) clearTimeout(timer);
    await group.terminateAll();
  }
}

/* ------------------------------------------------------------------ */
/* Mock WASM32/Atomics harness for the module 13486 Q7 last-ref branch */
/* ------------------------------------------------------------------ */

function createMock13486Vm() {
  const words = new Int32Array(1 << 16);
  let bump = 32;

  function allocate(wordsCount) {
    const pointer = bump;
    bump += wordsCount;
    if (bump >= words.length) throw new Error('mock heap exhausted');
    for (let index = pointer; index < bump; index += 1) Atomics.store(words, index, 0);
    return pointer;
  }

  function free(pointer) {
    Atomics.store(words, pointer, -1);
  }

  /* AVBuffer metadata: +0 data, +4 size, +8 atomic refcount, +16 pool entry,
   * +24 flags. A 12-word AVBufferRef records the metadata pointer. aw/VG
   * return the AVBufferRef pointer itself; Q7 takes a pointer to a word that
   * holds that ref (pointer-to-AVBufferRef holder), so callers must wrap it. */
  function aw(dataPointer, size) {
    if (!dataPointer) return 0;
    const meta = allocate(32);
    Atomics.store(words, meta + 0, dataPointer);
    Atomics.store(words, meta + 4, size);
    Atomics.store(words, meta + 8, 1);
    Atomics.store(words, meta + 16, 0);
    Atomics.store(words, meta + 24, 0);
    const ref = allocate(12);
    Atomics.store(words, ref + 0, meta);
    Atomics.store(words, ref + 4, dataPointer);
    Atomics.store(words, ref + 8, size);
    return { ref, meta, data: dataPointer, size };
  }

  /* VG: duplicate a ref and take an extra metadata reference. */
  function vg(buffer) {
    Atomics.add(words, buffer.meta + 8, 1);
    const ref = allocate(12);
    Atomics.store(words, ref + 0, buffer.meta);
    Atomics.store(words, ref + 4, buffer.data);
    Atomics.store(words, ref + 8, buffer.size);
    return { ref, meta: buffer.meta, data: buffer.data, size: buffer.size };
  }

  /* Q7: drop one ref. Only the last ref enters the final-free branch. When
   * pool entry +16 is non-zero the payload is returned to the pool and is NOT
   * actually freed, so no commit must happen there. */
  function q7(buffer, hooks) {
    const meta = buffer.meta;
    const previous = Atomics.sub(words, meta + 8, 1);
    const outcome = {
      lastRef: previous === 1,
      dataFreed: false,
      poolReturned: false,
      metadataFreed: false
    };
    if (previous === 1) {
      const flags = Atomics.load(words, meta + 24);
      const poolEntry = Atomics.load(words, meta + 16);
      if (poolEntry !== 0) {
        outcome.poolReturned = true;
        if (hooks && hooks.onPoolReturn) {
          hooks.onPoolReturn({ data: buffer.data, buffer: meta, poolEntry });
        }
      } else {
        free(buffer.data);
        outcome.dataFreed = true;
        if (hooks && hooks.onFinalDataFree) {
          hooks.onFinalDataFree({ data: buffer.data, buffer: meta, size: buffer.size });
        }
        if (!(flags & 2)) {
          free(meta);
          outcome.metadataFreed = true;
        }
      }
    }
    free(buffer.ref);
    return outcome;
  }

  return { words, aw, vg, q7, allocate, free };
}

/* ------------------------------------------------------------------ */
/* Real module 13486 factory                                           */
/* ------------------------------------------------------------------ */

/* Exact vendor factory source extracted from
 * /var/tmp/eio-dual-all-vendor-inspect/avplayer.js.fold400.txt lines 178-184
 * (the original p(e,t) Q7 last-free helper is included verbatim). The fold
 * newlines were removed; the algorithm itself is not rewritten. Only the
 * factory's require dependencies are mocked: WASM32 memory, atomic bitfields
 * and the allocator. No prepare/commit hook is installed in this factory.
 *
 * ABI note: `aw`/`VG` return an AVBufferRef pointer, while `Q7`/`m` takes a
 * pointer to a word holding that ref (pointer-to-AVBufferRef holder):
 * m(e){e&&mem[e]&&p(e,0)} and p reads i=mem[mem[e]], then 46626.aI(holder)
 * frees mem[holder] and writes mem[holder]=0. Tests must pass a holder.
 *
 * The embedding below must stay equal (line breaks aside) to module 13486 in
 * this worktree's actual shipped source graph (main avplayer.js plus the
 * synchronously loaded 90201..90204 chunks), so a stale constant can never
 * stand in for real vendor coverage. */
let shippedGraphWithoutLineBreaks = null;

function shippedGraphText() {
  if (shippedGraphWithoutLineBreaks === null) {
    shippedGraphWithoutLineBreaks = sourceGraph.text.replace(/[\r\n]/g, '');
  }
  return shippedGraphWithoutLineBreaks;
}

/* Only line breaks are normalised. Any other vendor change - inside module
 * 13486, including its object key - must fail this test instead of leaving a
 * constant that silently pretends to be current. */
function assertReal13486SourceMatchesVendor() {
  const expected = REAL_13486_FACTORY_SOURCE.replace(/;\s*$/, '').replace(/[\r\n]/g, '');
  assert.equal(expected.indexOf('13486:(e,t,i)=>'), 0,
    'embedded vendor source must start with the 13486 module key');
  assert.notEqual(expected.indexOf('function p(e,t)'), -1,
    'embedded vendor source must include the real Q7 last-free helper');
  assert.ok(expected.length > 512, 'embedded vendor module 13486 source is too short');
  assert.ok(shippedGraphText().includes(expected),
    'embedded module 13486 source no longer matches the shipped libmedia source graph; ' +
    're-extract it instead of leaving a stale constant');
}

const REAL_13486_FACTORY_SOURCE = '13486:(e,t,i)=>{i.d(t,{A6:()=>A,D$:()=>u,Q7:()=>m,VG:()=>f,aw:()=>l,us:()=>g});var r=i(80209),s=i(56238),a=i(6927),o=i(46626),n=i(33817),h=i(62560),d=i(90582),E=i(90187);const c=1;function l(e,t){let i=arguments.length>2&&void 0!==arguments[2]?arguments[2]:0,r=arguments.length>3&&void 0!==arguments[3]?arguments[3]:0,s=arguments.length>4&&void 0!==arguments[4]?arguments[4]:0;const h=(0,o.Gy)(28);if(!h)return 0;const d=function(e,t,i){let r=arguments.length>3&&void 0!==arguments[3]?arguments[3]:0,s=arguments.length>4&&void 0!==arguments[4]?arguments[4]:0,h=arguments.length>5&&void 0!==arguments[5]?arguments[5]:0;a.M[20](e,t),a.M[25](e+4,i),a.M[20](e+12,r),a.M[20](e+16,s),n.M_(e+8,1,9,2),a.M[15](e+20,h);const d=(0,o.Gy)(12);return d?(a.M[20](d,e),a.M[20](d+4,t),a.M[25](d+8,i),d):0}(h,e,t,i,r,s);return d||((0,o.Eb)(h),0)}function u(e){return l((0,o.sY)(e),e)}function f(e){const t=(0,o.Gy)(12);return(0,r.Mr)(t,e,12),n.WQ(s.f[20](e)+8,1,9,2),t}function p(e,t){const i=s.f[20](s.f[20](e));if(t?((0,r.Mr)(s.f[20](e),s.f[20](t),12),(0,o.aI)(t)):(0,o.aI)(e),1===n.jb(i+8,1,9,2)){const e=!(2&s.f[15](i+24)),eioPool=s.f[20](i+16);if(eioPool)v(eioPool,s.f[20](i));else{const eioData=s.f[20](i);let eioOpaque=null;try{eioOpaque=E.prepareDataFree({data:eioData,buffer:i})}catch(eioPrepareIgnore){eioOpaque=null}try{(0,o.Eb)(eioData)}catch(eioFreeError){try{E.dataFreeFailed(eioOpaque)}catch(eioFailedIgnore){}throw eioFreeError}try{E.commitDataFree(eioOpaque)}catch(eioCommitIgnore){}}e&&(0,o.Eb)(i)}}function m(e){e&&s.f[20](e)&&p(e,0)}function g(e,t){const i=s.f[20](e);if(!t)return m(e),0;if(i&&s.f[20](i+4)===s.f[20](t+4))return a.M[20](i+4,s.f[20](t+4)),a.M[25](i+8,s.f[25](t+8)),0;const r=f(t);return m(e),a.M[20](e,r),0}function A(e,t){if(!s.f[20](e)){const i=l((0,o.cz)(0,t),t);return a.M[20](e,i),a.M[15](s.f[20](i)+24,s.f[15](s.f[20](i)+24)|c),0}const i=s.f[20](e),h=s.f[20](i);if(s.f[25](i+8)===t)return 0;if(!(s.f[15](h+24)&c&&function(e){return 1&s.f[15](s.f[20](e)+20)?0:1===n.Hh(s.f[20](e)+8,9,2)?1:0}(i)&&s.f[20](i+4)===s.f[20](h))){let i=d.z(4);a.M[20](i,0);const o=A(i,t);return o<0?(d.t(4),o):((0,r.Mr)(s.f[20](s.f[20](i)+4),s.f[20](h),Math.min(t,s.f[25](h+4))),p(e,i),d.t(4),0)}const u=(0,o.cz)(s.f[20](h),t);return a.M[20](i+4,u),a.M[20](h,u),a.M[25](i+8,t),a.M[25](h+4,t),0}function v(e,t){const i=e,r=s.f[20](i+12);h.s2(r),a.M[20](i+16,s.f[20](r+4)),a.M[20](r+4,i),h.Tl(r),1===n.jb(r+8,1,9,2)&&function(e){!function(e){for(;s.f[20](e+4);){const t=s.f[20](e+4);a.M[20](e+4,s.f[20](t+16)),s.f[20](t+4)?v(s.f[20](t+4),s.f[20](t)):(0,o.Eb)(s.f[20](t)),(0,o.Eb)(t)}}(e),h.zr(e),(0,o.Eb)(e)}(r)}}';

function createReal13486Harness() {
  const words = new Int32Array(1 << 18);
  const memory = new Uint8Array(words.buffer);
  let bump = 64;
  const freed = [];

  function allocate(size, kind) {
    const span = (size + 7) & ~7;
    const pointer = bump;
    bump += span;
    if (bump > memory.length) throw new Error('real-13486 mock heap exhausted');
    memory.fill(0, pointer, bump);
    return pointer;
  }

  function free(pointer, kind) {
    if (!pointer) return;
    freed.push({ pointer, kind });
    Atomics.store(words, pointer >>> 2, -1);
  }

  function load32(pointer) { return Atomics.load(words, pointer >>> 2); }
  function store32(pointer, value) { Atomics.store(words, pointer >>> 2, value | 0); }
  function store64(pointer, value) { store32(pointer, value); store32(pointer + 4, 0); }
  function load64(pointer) { return load32(pointer) >>> 0; }
  function copy(destination, source, length) {
    memory.copyWithin(destination, source, source + length);
  }

  function fieldMask(bits, shift) { return ((1 << bits) - 1) << shift; }
  function setField(pointer, value, bits, shift) {
    const mask = fieldMask(bits, shift);
    const index = pointer >>> 2;
    const next = (value << shift) & mask;
    for (;;) {
      const current = Atomics.load(words, index);
      if (Atomics.compareExchange(words, index, current, (current & ~mask) | next) === current) return;
    }
  }
  function addField(pointer, delta, bits, shift) {
    const mask = fieldMask(bits, shift);
    const index = pointer >>> 2;
    for (;;) {
      const current = Atomics.load(words, index);
      const value = ((current & mask) >>> shift) + delta;
      const next = (value << shift) & mask;
      if (Atomics.compareExchange(words, index, current, (current & ~mask) | next) === current) return;
    }
  }
  function subField(pointer, delta, bits, shift) {
    const mask = fieldMask(bits, shift);
    const index = pointer >>> 2;
    for (;;) {
      const current = Atomics.load(words, index);
      const previous = (current & mask) >>> shift;
      const next = ((previous - delta) << shift) & mask;
      if (Atomics.compareExchange(words, index, current, (current & ~mask) | next) === current) {
        return previous;
      }
    }
  }
  function getField(pointer, bits, shift) {
    return (Atomics.load(words, pointer >>> 2) & fieldMask(bits, shift)) >>> shift;
  }

  const { facade: domainFacade, core: domainCore } = loadReal90187Facade();
  const dependencies = {
    80209: { Mr: copy },
    56238: { f: Object.assign([], { 15: load32, 20: load32, 25: load64 }) },
    6927: { M: Object.assign([], { 15: store32, 20: store32, 25: store64 }) },
    46626: {
      Gy: (size) => allocate(size, 'vendor-alloc'),
      /* 46626.Eb/l(e) is a plain allocator free of the given pointer. */
      Eb: (pointer) => free(pointer, 'data-or-metadata'),
      /* 46626.aI/u(e){free(mem[e]); mem[e]=0} is the pointer-to-holder ABI:
       * it frees the ref stored in the word and clears that word. */
      aI: (pointer) => {
        const ref = load32(pointer);
        if (ref) free(ref, 'ref');
        store32(pointer, 0);
      },
      sY: (size) => allocate(size, 'vendor-data'),
      cz: (pointer, size) => allocate(size, 'vendor-data')
    },
    33817: { M_: setField, WQ: addField, jb: subField, Hh: getField },
    62560: { s2() {}, Tl() {}, zr() {}, Ts() {} },
    90582: { z: (size) => allocate(size, 'vendor-temp'), t: (pointer) => free(pointer, 'vendor-temp') },
    /* The vendor 13486 factory now requires the real extracted 90187 facade.
     * No domain is registered in it here, so the direct-free hook stays on its
     * benign unregistered path and cannot credit bytes in these semantics
     * tests. */
    90187: domainFacade
  };

  return {
    words,
    dependencies,
    /* Real extracted module 90187 facade with NO registered domain: the new
     * vendor last-free hook therefore stays on the unregistered path here. */
    domainFacade,
    domainCore,
    allocate,
    free,
    load32,
    store32,
    store64,
    load64,
    getField,
    freed,
    isFreed(pointer) {
      return freed.some((entry) => entry.pointer === pointer);
    },
    freedKinds(pointer) {
      return freed.filter((entry) => entry.pointer === pointer).map((entry) => entry.kind);
    }
  };
}

/* The physical split moves the ownership primitive to module 90190 in chunk
 * 90201 and leaves the domain/release facade as module 90187 in chunk 90202.
 * The facade starts with `const core=__webpack_require__(90190);`, so this
 * helper evaluates the ACTUAL core factory and exposes that same instance;
 * nothing is reconstructed or stubbed. */
function loadReal90190Core() {
  const source = sourceGraph.factory(90190);
  const holder = new Function('return ({' + source + '});')();
  const moduleObject = { exports: {} };
  holder[90190].call(moduleObject.exports, moduleObject, moduleObject.exports, function (id) {
    throw new Error('unexpected module90190 dependency ' + String(id));
  });
  return moduleObject.exports;
}

function loadReal90187Facade() {
  const core = loadReal90190Core();
  const source = sourceGraph.factory(90187);
  const holder = new Function('return ({' + source + '});')();
  const moduleObject = { exports: {} };
  const require90187 = (id) => {
    if (id === 90190) return core;
    throw new Error('unexpected module90187 dependency ' + String(id) +
      ' while loading the unused-domain release facade');
  };
  holder[90187].call(moduleObject.exports, moduleObject, moduleObject.exports, require90187);
  const facade = moduleObject.exports;
  assert.equal(facade.create, core.create,
    'the facade must expose the actual module90190 core create(), not a reconstructed core');
  assert.equal(facade.attach, core.attach,
    'the facade must expose the actual module90190 core attach()');
  assert.equal(facade.LAYOUT, core.LAYOUT,
    'the facade must expose the actual module90190 core layout');
  assert.equal(facade.VERSION, core.VERSION);
  assert.equal(facade.KIND, core.KIND);
  return { facade, core };
}

function loadReal13486Factory(harness) {
  assertReal13486SourceMatchesVendor();
  const vm = harness || createReal13486Harness();
  const require13486 = (id) => {
    if (!Object.prototype.hasOwnProperty.call(vm.dependencies, id)) {
      throw new Error('unexpected require ' + String(id));
    }
    return vm.dependencies[id];
  };
  require13486.d = function (target, definitions) {
    Object.keys(definitions).forEach((key) => {
      target[key] = definitions[key]();
    });
  };

  const factoryHolder = new Function('return ({' + REAL_13486_FACTORY_SOURCE + '});')();
  const exportsTarget = {};
  factoryHolder[13486]({}, exportsTarget, require13486);
  return { harness: vm, vendor: exportsTarget };
}

/* ------------------------------------------------------------------ */
/* Tests                                                               */
/* ------------------------------------------------------------------ */

test('cross-worker: one cloned descriptor, held drops only after real-data-free commit', { timeout: 5000 }, async () => {
  await withWorkers(async (group) => {
    const owner = group.spawn();
    const created = await group.request(owner, 'create', { capacityBytes: 4 * CHUNK, maxEntries: 8 });
    const descriptor = created.descriptor;
    assert.equal(descriptor.kind, mod.KIND);
    assert.equal(created.snapshot.heldBytes, 0);

    const peer = group.spawn();
    const attached = await group.request(peer, 'attach', { descriptor });
    assert.equal(attached.descriptor.capacityBytes, 4 * CHUNK);
    assert.equal(attached.descriptor.sab.byteLength, descriptor.sab.byteLength);
    assert.equal(attached.snapshot.heldBytes, 0);

    const local = mod.attach(cloneValue(descriptor));
    assert.equal(local.snapshot().heldBytes, 0);

    const reservation = await group.request(owner, 'reserve', { bytes: CHUNK, generation: 1 });
    assert.equal(reservation.ok, true);
    assert.equal(reservation.snapshot.heldBytes, CHUNK);
    assert.equal(reservation.snapshot.peakBytes, CHUNK);

    const bound = await group.request(owner, 'bind', {
      token: reservation.token,
      data: 0x11110000,
      buffer: 0x22220000
    });
    assert.equal(bound.ok, true);
    assert.equal(bound.snapshot.heldBytes, CHUNK);
    assert.equal(bound.snapshot.countBound, 1);
    assert.equal(bound.snapshot.countReserved, 0);

    const peerSnapshot = await group.request(peer, 'snapshot');
    assert.equal(peerSnapshot.heldBytes, CHUNK);
    assert.equal(peerSnapshot.countBound, 1);
    assert.equal(local.snapshot().heldBytes, CHUNK);

    const prepared = await group.request(peer, 'prepare', {
      data: 0x11110000,
      buffer: 0x22220000
    });
    assert.equal(prepared.ok, true);
    assert.equal(prepared.snapshot.heldBytes, CHUNK);
    assert.equal(prepared.snapshot.countClosing, 1);

    const duplicatePrepare = await group.request(peer, 'prepare', {
      data: 0x11110000,
      buffer: 0x22220000
    });
    assert.equal(duplicatePrepare.ok, false);
    assert.equal(duplicatePrepare.code, 'already-closing');
    assert.equal((await group.request(owner, 'snapshot')).heldBytes, CHUNK);

    const earlyCommit = await group.request(peer, 'commit', {
      ticket: prepared.ticket,
      dataFreed: false
    });
    assert.equal(earlyCommit.ok, false);
    assert.equal(earlyCommit.code, 'not-freed');
    assert.equal(earlyCommit.snapshot.heldBytes, CHUNK);

    const freed = await group.request(owner, 'fakeFree', {
      data: 0x11110000,
      buffer: 0x22220000
    });
    assert.equal(freed.freed, true);

    const sequence = (await group.request(owner, 'snapshot')).notifySequence;
    const waitPromise = group.request(owner, 'waitChange', {
      previousSequence: sequence,
      timeoutMs: 2000
    }, 3000);
    await delay(50);
    const committed = await group.request(peer, 'commit', {
      ticket: prepared.ticket,
      dataFreed: true
    });
    assert.equal(committed.ok, true);
    assert.equal(committed.releasedBytes, CHUNK);
    assert.equal(committed.snapshot.heldBytes, 0);

    const waited = await waitPromise;
    assert.equal(waited.changed, true);
    assert.equal(waited.heldBytes, 0);

    const after = await group.request(owner, 'snapshot');
    assert.equal(after.heldBytes, 0);
    assert.equal(after.peakBytes, CHUNK);
    assert.equal(after.countBound, 0);
    assert.equal(after.countClosing, 0);

    const staleReclaim = await group.request(owner, 'reclaim', { token: reservation.token });
    assert.equal(staleReclaim.ok, false);
    assert.equal(staleReclaim.code, 'stale-token');
    assert.equal(staleReclaim.snapshot.heldBytes, 0);

    const duplicateCommit = await group.request(peer, 'commit', {
      ticket: prepared.ticket,
      dataFreed: true
    });
    assert.equal(duplicateCommit.ok, false);
    assert.equal(duplicateCommit.code, 'stale-ticket');
    assert.equal(duplicateCommit.snapshot.heldBytes, 0);

    const prepareAfter = await group.request(peer, 'prepare', {
      data: 0x11110000,
      buffer: 0x22220000
    });
    assert.equal(prepareAfter.ok, false);
    assert.equal(prepareAfter.code, 'unknown-record');
    assert.equal(prepareAfter.snapshot.heldBytes, 0);
  });
});

test('reserve semantics: zero bytes, capacity, entries, invalid arguments, no drift', { timeout: 5000 }, async () => {
  await withWorkers(async () => {
    const zeroCtl = mod.create({ capacityBytes: 64, maxEntries: 2 });
    const zeroA = zeroCtl.reserve(0, 1);
    assert.equal(zeroA.ok, true);
    let zeroSnapshot = zeroCtl.snapshot();
    assert.equal(zeroSnapshot.heldBytes, 0);
    assert.equal(zeroSnapshot.peakBytes, 0);
    assert.equal(zeroSnapshot.reservedBytes, 0);
    assert.equal(zeroSnapshot.countReserved, 1);
    const zeroB = zeroCtl.reserve(0, 1);
    assert.equal(zeroB.ok, true);
    const zeroC = zeroCtl.reserve(0, 1);
    assert.equal(zeroC.ok, false);
    assert.equal(zeroC.code, 'entries-exhausted');

    const zeroBound = zeroCtl.bind(zeroA.token, { data: 0x10, buffer: 0x20 });
    assert.equal(zeroBound.ok, true);
    zeroSnapshot = zeroCtl.snapshot();
    assert.equal(zeroSnapshot.heldBytes, 0);
    assert.equal(zeroSnapshot.countBound, 1);
    assert.equal(zeroSnapshot.countReserved, 1);

    const zeroPrepared = zeroCtl.prepareFinalFree({ data: 0x10, buffer: 0x20 });
    assert.equal(zeroPrepared.ok, true);
    const zeroCommitted = zeroCtl.commitFinalFree(zeroPrepared.ticket, { dataFreed: true });
    assert.equal(zeroCommitted.ok, true);
    assert.equal(zeroCommitted.releasedBytes, 0);
    assert.equal(zeroCtl.snapshot().heldBytes, 0);
    assert.equal(zeroCtl.reclaimUnused(zeroB.token).ok, true);

    const capCtl = mod.create({ capacityBytes: 100, maxEntries: 4 });
    const first = capCtl.reserve(60, 1);
    assert.equal(first.ok, true);
    const tooBig = capCtl.reserve(50, 1);
    assert.equal(tooBig.ok, false);
    assert.equal(tooBig.code, 'capacity-exceeded');
    let capSnapshot = capCtl.snapshot();
    assert.equal(capSnapshot.heldBytes, 60);
    assert.equal(capSnapshot.peakBytes, 60);
    assert.equal(capSnapshot.countReserved, 1);

    const second = capCtl.reserve(40, 1);
    assert.equal(second.ok, true);
    assert.equal(capCtl.snapshot().heldBytes, 100);
    const full = capCtl.reserve(1, 1);
    assert.equal(full.ok, false);
    assert.equal(full.code, 'capacity-exceeded');
    capSnapshot = capCtl.snapshot();
    assert.equal(capSnapshot.heldBytes, 100);
    assert.equal(capSnapshot.peakBytes, 100);

    assert.equal(capCtl.reclaimUnused(first.token).ok, true);
    assert.equal(capCtl.snapshot().heldBytes, 40);
    assert.equal(capCtl.reclaimUnused(second.token).ok, true);
    assert.equal(capCtl.snapshot().heldBytes, 0);
    assert.equal(capCtl.snapshot().peakBytes, 100);
    assert.equal(capCtl.snapshot().countReserved, 0);

    assert.equal(capCtl.reserve(-1, 1).code, 'invalid-argument');
    assert.equal(capCtl.reserve(1.5, 1).code, 'invalid-argument');
    assert.equal(capCtl.reserve('10', 1).code, 'invalid-argument');
    assert.equal(capCtl.reserve(101, 1).code, 'capacity-exceeded');
    assert.equal(capCtl.reserve(mod.MAX_CAPACITY_BYTES + 1, 1).code, 'invalid-argument');
    assert.equal(capCtl.reserve(10, -1).code, 'invalid-argument');

    const entryCtl = mod.create({ capacityBytes: 1000, maxEntries: 2 });
    const entryA = entryCtl.reserve(10, 1);
    assert.equal(entryA.ok, true);
    assert.equal(entryCtl.reserve(10, 1).ok, true);
    const entryFull = entryCtl.reserve(10, 1);
    assert.equal(entryFull.ok, false);
    assert.equal(entryFull.code, 'entries-exhausted');
    assert.equal(entryCtl.reclaimUnused(entryA.token).ok, true);
    assert.equal(entryCtl.reserve(10, 1).ok, true);
    assert.equal(entryCtl.snapshot().heldBytes, 20);
  });
});

test('bound records cannot be reclaimed; duplicate prepare/commit/stale tokens never credit', { timeout: 5000 }, async () => {
  await withWorkers(async () => {
    const ctl = mod.create({ capacityBytes: CHUNK, maxEntries: 4 });
    const reservation = ctl.reserve(100, 1);
    assert.equal(reservation.ok, true);
    assert.equal(ctl.bind(reservation.token, { data: 0x1000, buffer: 0x2000 }).ok, true);

    const boundReclaim = ctl.reclaimUnused(reservation.token);
    assert.equal(boundReclaim.ok, false);
    assert.equal(boundReclaim.code, 'bound');
    assert.equal(ctl.snapshot().heldBytes, 100);

    const prepared = ctl.prepareFinalFree({ data: 0x1000, buffer: 0x2000 });
    assert.equal(prepared.ok, true);
    assert.equal(ctl.snapshot().heldBytes, 100);

    const duplicate = ctl.prepareFinalFree({ data: 0x1000, buffer: 0x2000 });
    assert.equal(duplicate.ok, false);
    assert.equal(duplicate.code, 'already-closing');
    assert.equal(ctl.snapshot().heldBytes, 100);

    const closingReclaim = ctl.reclaimUnused(reservation.token);
    assert.equal(closingReclaim.ok, false);
    assert.equal(closingReclaim.code, 'bound');
    assert.equal(ctl.snapshot().heldBytes, 100);

    const missingEvidence = ctl.commitFinalFree(prepared.ticket);
    assert.equal(missingEvidence.ok, false);
    assert.equal(missingEvidence.code, 'not-freed');
    assert.equal(ctl.snapshot().heldBytes, 100);
    assert.equal(ctl.lookup({ data: 0x1000, buffer: 0x2000 }).state, 'closing');

    const emptyEvidence = ctl.commitFinalFree(prepared.ticket, {});
    assert.equal(emptyEvidence.ok, false);
    assert.equal(emptyEvidence.code, 'not-freed');
    assert.equal(ctl.snapshot().heldBytes, 100);

    const forgedTicket = {
      brand: mod.LAYOUT.ticketBrand,
      entry: prepared.ticket.entry,
      id: prepared.ticket.id,
      data: 0x1000,
      buffer: 0x9999,
      bytes: prepared.ticket.bytes,
      generation: prepared.ticket.generation
    };
    const forgedCommit = ctl.commitFinalFree(forgedTicket, { dataFreed: true });
    assert.equal(forgedCommit.ok, false);
    assert.equal(forgedCommit.code, 'stale-ticket');
    assert.equal(ctl.snapshot().heldBytes, 100);
    assert.equal(ctl.lookup({ data: 0x1000, buffer: 0x2000 }).state, 'closing');

    const early = ctl.commitFinalFree(prepared.ticket, { dataFreed: false });
    assert.equal(early.ok, false);
    assert.equal(early.code, 'not-freed');
    assert.equal(ctl.snapshot().heldBytes, 100);
    assert.equal(ctl.lookup({ data: 0x1000, buffer: 0x2000 }).state, 'closing');

    const committed = ctl.commitFinalFree(prepared.ticket, { dataFreed: true });
    assert.equal(committed.ok, true);
    assert.equal(committed.releasedBytes, 100);
    assert.equal(ctl.snapshot().heldBytes, 0);
    assert.equal(ctl.snapshot().peakBytes, 100);

    const duplicateCommit = ctl.commitFinalFree(prepared.ticket, { dataFreed: true });
    assert.equal(duplicateCommit.ok, false);
    assert.equal(duplicateCommit.code, 'stale-ticket');
    assert.equal(ctl.snapshot().heldBytes, 0);

    const afterPrepare = ctl.prepareFinalFree({ data: 0x1000, buffer: 0x2000 });
    assert.equal(afterPrepare.ok, false);
    assert.equal(afterPrepare.code, 'unknown-record');

    const staleReclaim = ctl.reclaimUnused(reservation.token);
    assert.equal(staleReclaim.ok, false);
    assert.equal(staleReclaim.code, 'stale-token');
    assert.equal(ctl.snapshot().heldBytes, 0);

    const unknown = ctl.prepareFinalFree({ data: 0xdead, buffer: 0xbeef });
    assert.equal(unknown.ok, false);
    assert.equal(unknown.code, 'unknown-record');
    assert.equal(ctl.snapshot().heldBytes, 0);

    const unused = ctl.reserve(50, 2);
    assert.equal(unused.ok, true);
    const reclaimed = ctl.reclaimUnused(unused.token);
    assert.equal(reclaimed.ok, true);
    assert.equal(reclaimed.reclaimedBytes, 50);
    assert.equal(ctl.reclaimUnused(unused.token).code, 'stale-token');
    assert.equal(ctl.snapshot().heldBytes, 0);

    assert.equal(ctl.prepareFinalFree({ data: 0, buffer: 1 }).code, 'invalid-argument');
    assert.equal(ctl.prepareFinalFree({ data: 1, buffer: 0x100000000 }).code, 'invalid-argument');
  });
});

test('key includes AVBuffer metadata: same data with different metadata is distinct, ABA stays distinct', { timeout: 5000 }, async () => {
  await withWorkers(async () => {
    const ctl = mod.create({ capacityBytes: CHUNK, maxEntries: 8 });
    const first = ctl.reserve(100, 1);
    assert.equal(ctl.bind(first.token, { data: 0x1000, buffer: 0x2000 }).ok, true);
    const second = ctl.reserve(100, 1);
    assert.equal(ctl.bind(second.token, { data: 0x1000, buffer: 0x3000 }).ok, true);

    const duplicate = ctl.reserve(100, 1);
    const duplicateBind = ctl.bind(duplicate.token, { data: 0x1000, buffer: 0x2000 });
    assert.equal(duplicateBind.ok, false);
    assert.equal(duplicateBind.code, 'duplicate-key');
    let snapshot = ctl.snapshot();
    assert.equal(snapshot.heldBytes, 300);
    assert.equal(snapshot.countBound, 2);
    assert.equal(snapshot.countReserved, 1);
    assert.equal(ctl.reclaimUnused(duplicate.token).ok, true);
    assert.equal(ctl.snapshot().heldBytes, 200);

    const firstTicket = ctl.prepareFinalFree({ data: 0x1000, buffer: 0x2000 });
    assert.equal(firstTicket.ok, true);
    assert.equal(ctl.commitFinalFree(firstTicket.ticket, { dataFreed: true }).ok, true);
    assert.equal(ctl.snapshot().heldBytes, 100);
    assert.equal(ctl.lookup({ data: 0x1000, buffer: 0x3000 }).state, 'bound');
    assert.equal(ctl.lookup({ data: 0x1000, buffer: 0x2000 }).found, false);

    const reused = ctl.reserve(100, 1);
    assert.equal(ctl.bind(reused.token, { data: 0x1000, buffer: 0x2000 }).ok, true);
    assert.equal(ctl.snapshot().heldBytes, 200);

    const replay = ctl.commitFinalFree(firstTicket.ticket, { dataFreed: true });
    assert.equal(replay.ok, false);
    assert.equal(replay.code, 'stale-ticket');
    assert.equal(ctl.snapshot().heldBytes, 200);
    const staleReclaim = ctl.reclaimUnused(first.token);
    assert.equal(staleReclaim.ok, false);
    assert.equal(staleReclaim.code, 'stale-token');
    assert.equal(ctl.snapshot().heldBytes, 200);

    assert.equal(ctl.commitFinalFree(ctl.prepareFinalFree({ data: 0x1000, buffer: 0x3000 }).ticket, { dataFreed: true }).ok, true);
    assert.equal(ctl.snapshot().heldBytes, 100);
    assert.equal(ctl.commitFinalFree(ctl.prepareFinalFree({ data: 0x1000, buffer: 0x2000 }).ticket, { dataFreed: true }).ok, true);
    assert.equal(ctl.snapshot().heldBytes, 0);
  });
});

test('real workers: one token bound concurrently with different keys has one winner and no key overwrite', { timeout: 5000 }, async () => {
  await withWorkers(async (group) => {
    const ctl = mod.create({ capacityBytes: 4096, maxEntries: 4 });
    const reservation = ctl.reserve(100, 1);
    assert.equal(reservation.ok, true);
    const descriptor = cloneValue(ctl.descriptor);
    const workers = [group.spawn(), group.spawn()];
    await Promise.all(workers.map((worker) => group.request(worker, 'attach', { descriptor }, 2500)));

    const results = await bindAtBarrier(group, workers, [
      { token: reservation.token, data: 0x1000, buffer: 0x2000 },
      { token: reservation.token, data: 0x3000, buffer: 0x4000 }
    ]);

    const winners = results.filter((result) => result.ok);
    const losers = results.filter((result) => !result.ok);
    assert.equal(winners.length, 1);
    assert.equal(losers.length, 1);
    assert.ok(['already-bound', 'stale-token'].indexOf(losers[0].code) >= 0,
      'unexpected loser code ' + losers[0].code);

    const snapshot = ctl.snapshot();
    assert.equal(snapshot.countBound, 1);
    assert.equal(snapshot.countReserved, 0);
    assert.equal(snapshot.heldBytes, 100);
    assert.equal(snapshot.boundBytes, 100);

    const counts = entryStateCounts(ctl);
    assert.equal(counts.binding, 0);
    assert.equal(counts.bound, 1);
    assert.equal(counts.closing, 0);
    assert.equal(counts.committing, 0);

    const winningKey = winners[0].key;
    const losingKey = winningKey.data === 0x1000
      ? { data: 0x3000, buffer: 0x4000 }
      : { data: 0x1000, buffer: 0x2000 };
    assert.equal(ctl.lookup(winningKey).state, 'bound');
    assert.equal(ctl.lookup(losingKey).found, false);
  });
});

test('real workers: different tokens concurrently binding the same key never double-BOUND', { timeout: 5000 }, async () => {
  await withWorkers(async (group) => {
    const ctl = mod.create({ capacityBytes: 4096, maxEntries: 4 });
    const tokenA = ctl.reserve(100, 1);
    const tokenB = ctl.reserve(100, 2);
    assert.equal(tokenA.ok, true);
    assert.equal(tokenB.ok, true);
    const descriptor = cloneValue(ctl.descriptor);
    const workers = [group.spawn(), group.spawn()];
    await Promise.all(workers.map((worker) => group.request(worker, 'attach', { descriptor }, 2500)));

    const results = await bindAtBarrier(group, workers, [
      { token: tokenA.token, data: 0x7000, buffer: 0x8000 },
      { token: tokenB.token, data: 0x7000, buffer: 0x8000 }
    ]);

    const successes = results.filter((result) => result.ok);
    const failures = results.filter((result) => !result.ok);
    assert.ok(successes.length <= 1, 'same key was bound twice');
    for (const failure of failures) assert.equal(failure.code, 'duplicate-key');

    const snapshot = ctl.snapshot();
    assert.equal(snapshot.countBound, successes.length);
    assert.equal(snapshot.countReserved, 2 - successes.length);
    assert.equal(snapshot.boundBytes, successes.length * 100);
    assert.equal(snapshot.heldBytes, 200);

    const counts = entryStateCounts(ctl);
    assert.equal(counts.binding, 0);
    assert.equal(counts.bound, successes.length);
    assert.equal(counts.closing, 0);
    assert.equal(counts.committing, 0);
    assert.equal(ctl.lookup({ data: 0x7000, buffer: 0x8000 }).found, successes.length === 1);

    const tokens = [tokenA.token, tokenB.token];
    results.forEach((result, index) => {
      if (result.ok) {
        const prepared = ctl.prepareFinalFree(result.key);
        assert.equal(prepared.ok, true);
        assert.equal(ctl.commitFinalFree(prepared.ticket, { dataFreed: true }).ok, true);
      } else {
        assert.equal(ctl.reclaimUnused(tokens[index]).ok, true);
      }
    });
    assert.equal(ctl.snapshot().heldBytes, 0);
    assert.equal(ctl.snapshot().countBound, 0);
    assert.equal(ctl.snapshot().countReserved, 0);
  });
});

test('seek/new generation keeps the old owner charged until its real free', { timeout: 5000 }, async () => {
  await withWorkers(async () => {
    const ctl = mod.create({ capacityBytes: CHUNK, maxEntries: 4 });
    const oldGeneration = ctl.reserve(100, 1);
    assert.equal(oldGeneration.ok, true);
    assert.equal(ctl.bind(oldGeneration.token, { data: 0x5000, buffer: 0x6000 }).ok, true);

    const newGeneration = ctl.reserve(200, 2);
    assert.equal(newGeneration.ok, true);
    assert.equal(ctl.reclaimUnused(newGeneration.token).ok, true);
    assert.equal(ctl.snapshot().heldBytes, 100);
    // 100 old held + 200 new reservation = 300 at the successful charge CAS.
    // The reclaim leaves held at 100, but peak must remember the real peak 300.
    assert.equal(ctl.snapshot().peakBytes, 300);

    const differentGeneration = ctl.reserve(300, 3);
    assert.equal(differentGeneration.ok, true);
    const duplicate = ctl.bind(differentGeneration.token, { data: 0x5000, buffer: 0x6000 });
    assert.equal(duplicate.ok, false);
    assert.equal(duplicate.code, 'duplicate-key');
    assert.equal(ctl.snapshot().heldBytes, 400);
    assert.equal(ctl.reclaimUnused(differentGeneration.token).ok, true);
    assert.equal(ctl.snapshot().heldBytes, 100);
    assert.equal(ctl.snapshot().peakBytes, 400);
    assert.equal(ctl.lookup({ data: 0x5000, buffer: 0x6000 }).generation, 1);

    const prepared = ctl.prepareFinalFree({ data: 0x5000, buffer: 0x6000 });
    assert.equal(prepared.ok, true);
    assert.equal(ctl.commitFinalFree(prepared.ticket, { dataFreed: true }).ok, true);
    assert.equal(ctl.snapshot().heldBytes, 0);

    const replacement = ctl.reserve(300, 4);
    assert.equal(replacement.ok, true);
    assert.equal(ctl.bind(replacement.token, { data: 0x5000, buffer: 0x6000 }).ok, true);
    assert.equal(ctl.snapshot().heldBytes, 300);
    assert.equal(ctl.lookup({ data: 0x5000, buffer: 0x6000 }).generation, 4);
    assert.equal(ctl.snapshot().peakBytes, 400);
  });
});

test('parallel workers racing reservations never exceed capacity or charge phantom bytes', { timeout: 5000 }, async () => {
  await withWorkers(async (group) => {
    const capacity = 3 * CHUNK;
    const ctl = mod.create({ capacityBytes: capacity, maxEntries: 8 });
    const descriptor = cloneValue(ctl.descriptor);
    const workerCount = 4;
    const workers = [];
    for (let index = 0; index < workerCount; index += 1) workers.push(group.spawn());
    await Promise.all(workers.map((worker) => group.request(worker, 'attach', { descriptor }, 2500)));

    const bursts = await Promise.all(workers.map((worker) => group.request(worker, 'burst', {
      bytes: CHUNK,
      generation: 1,
      attempts: 3
    }, 2500)));

    let successes = 0;
    const failures = [];
    for (const burst of bursts) {
      successes += burst.tokens.length;
      for (const code of burst.failures) failures.push(code);
    }
    assert.ok(successes >= 1);
    assert.ok(successes <= 3);
    assert.ok(failures.length >= 1);
    for (const code of failures) {
      assert.ok(['capacity-exceeded', 'entries-exhausted', 'busy'].indexOf(code) >= 0, 'unexpected failure ' + code);
    }

    let snapshot = ctl.snapshot();
    assert.equal(snapshot.heldBytes, successes * CHUNK);
    assert.equal(snapshot.peakBytes, successes * CHUNK);
    assert.ok(snapshot.heldBytes <= capacity);
    assert.equal(snapshot.countReserved, successes);

    await Promise.all(workers.map((worker, index) => group.request(worker, 'releaseTokens', {
      tokens: bursts[index].tokens
    }, 2500)));

    snapshot = ctl.snapshot();
    assert.equal(snapshot.heldBytes, 0);
    assert.equal(snapshot.reservedBytes, 0);
    assert.equal(snapshot.boundBytes, 0);
    assert.equal(snapshot.countReserved, 0);
    assert.equal(snapshot.countBound, 0);
    assert.equal(snapshot.peakBytes, successes * CHUNK);
  });
});

test('reserve peak uses the captured post-charge value when a peer frees before publication', { timeout: 5000 }, async () => {
  await withWorkers(async (group) => {
    const ctl = mod.create({ capacityBytes: 4096, maxEntries: 4 });
    const peer = ctl.reserve(100, 1);
    assert.equal(peer.ok, true);
    assert.equal(ctl.snapshot().heldBytes, 100);

    const signalSab = new SharedArrayBuffer(8);
    const signal = new Int32Array(signalSab);
    const writer = group.spawn();
    await group.request(writer, 'attach', { descriptor: cloneValue(ctl.descriptor) });

    const racePromise = group.request(writer, 'reserveRace', {
      bytes: 200,
      generation: 2,
      oldHeld: 100,
      signal: signalSab
    }, 4000);

    const waitResult = Atomics.wait(signal, 0, 0, 2000);
    assert.notEqual(waitResult, 'timed-out', 'reserve charge CAS did not happen');

    // The charge CAS already succeeded (held 300). Free the peer reservation
    // now, before the writer raises peak, exactly the interleaving that used
    // to lose the real peak.
    assert.equal(ctl.reclaimUnused(peer.token).ok, true);
    assert.equal(ctl.snapshot().heldBytes, 200);
    Atomics.store(signal, 1, 1);
    Atomics.notify(signal, 1);

    const raced = await racePromise;
    assert.equal(raced.ok, true);
    assert.equal(raced.snapshot.heldBytes, 200);
    assert.equal(raced.snapshot.peakBytes, 300);
  });
});

/* Deterministic raisePeak contention: a peer wins more than the old 128-attempt
 * CAS budget. Every losing attempt publishes a higher real PEAK in the same
 * shared memory and returns that value, exactly as a genuine competing raise
 * would. Only the PEAK word is intercepted, and the wrapper is always restored. */
test('peak raise survives more than 128 genuine PEAK CAS losses and does not stop at the last observed peak', { timeout: 5000 }, async () => {
  const ctl = mod.create({ capacityBytes: 4096, maxEntries: 4 });
  const sab = ctl.descriptor.sab;
  const peakWord = mod.LAYOUT.header.PEAK;
  const realCompareExchange = Atomics.compareExchange;
  const interceptedFailures = 129; /* one more than the former MAX_CAS_RETRIES */
  let pendingFailures = interceptedFailures;
  let peakAttempts = 0;

  Atomics.compareExchange = function (array, index, expected, replacement) {
    if (index === peakWord && array && array.buffer === sab) {
      peakAttempts += 1;
      if (pendingFailures > 0) {
        pendingFailures -= 1;
        /* A peer raised the real peak above `expected`; report it as observed. */
        const observed = expected + 1;
        assert.equal(realCompareExchange.call(Atomics, array, index, expected, observed),
          expected, 'the simulated peer raise must really install the higher peak');
        return observed;
      }
      return realCompareExchange.call(Atomics, array, index, expected, replacement);
    }
    return realCompareExchange.call(Atomics, array, index, expected, replacement);
  };

  let reservation;
  let snapshot;
  try {
    /* reserve(200) is the first reserve on this controller, so charge succeeds
     * with held=0+200 and its raise starts from PEAK=0. */
    reservation = ctl.reserve(200, 1);
    snapshot = ctl.snapshot();
  } finally {
    Atomics.compareExchange = realCompareExchange;
  }

  assert.equal(Atomics.compareExchange, realCompareExchange,
    'the PEAK compare-exchange wrapper must be restored');
  assert.equal(pendingFailures, 0, 'the raise did not consume every simulated failure');
  assert.ok(peakAttempts > 128,
    'expected more than 128 PEAK CAS attempts, saw ' + peakAttempts);

  assert.equal(reservation.ok, true);
  assert.equal(snapshot.heldBytes, 200);
  assert.notEqual(snapshot.peakBytes, interceptedFailures,
    'peak stopped at the last observed contending value instead of the charged held bytes');
  assert.equal(snapshot.peakBytes, 200,
    'peak must reach the charged held value after >128 monotonic CAS losses');
  assert.equal(snapshot.countReserved, 1);

  /* A capacity failure is rejected before any charge or peak raise, so it must
   * leave both held and peak untouched. The wrapper is gone by now, and no
   * other test observes a patched Atomics.compareExchange. */
  const capacityFailure = ctl.reserve(4096, 1);
  assert.equal(capacityFailure.ok, false);
  assert.equal(capacityFailure.code, 'capacity-exceeded');
  const afterFailure = ctl.snapshot();
  assert.equal(afterFailure.heldBytes, 200);
  assert.equal(afterFailure.peakBytes, 200);
  assert.equal(afterFailure.countReserved, 1);

  assert.equal(ctl.reclaimUnused(reservation.token).ok, true);
  const reclaimed = ctl.snapshot();
  assert.equal(reclaimed.heldBytes, 0);
  assert.equal(reclaimed.peakBytes, 200);
});

/* This model is a semantic extraction, not vendor coverage. The next test
 * executes the extracted real module 13486 factory instead. */
test('Q7 model (model only, not vendor coverage): two refs, only the last-ref real free prepares and commits; pool return never commits', { timeout: 5000 }, async () => {
  await withWorkers(async () => {
    const vm = createMock13486Vm();
    const ctl = mod.create({ capacityBytes: CHUNK, maxEntries: 8 });
    const payload = vm.allocate(256);
    const primary = vm.aw(payload, 256);
    const reservation = ctl.reserve(256, 7);
    assert.equal(reservation.ok, true);
    assert.equal(ctl.bind(reservation.token, { data: primary.data, buffer: primary.meta }).ok, true);

    const second = vm.vg(primary);
    assert.equal(Atomics.load(vm.words, primary.meta + 8), 2);

    let finalFreeCalls = 0;
    const hooks = {
      onFinalDataFree(point) {
        finalFreeCalls += 1;
        const prepared = ctl.prepareFinalFree({ data: point.data, buffer: point.buffer });
        assert.equal(prepared.ok, true);
        assert.equal(ctl.snapshot().countClosing, 1);
        const committed = ctl.commitFinalFree(prepared.ticket, { dataFreed: true });
        assert.equal(committed.ok, true);
        assert.equal(committed.releasedBytes, 256);
      }
    };

    const firstUnref = vm.q7(primary, hooks);
    assert.equal(firstUnref.lastRef, false);
    assert.equal(firstUnref.dataFreed, false);
    assert.equal(finalFreeCalls, 0);
    assert.equal(ctl.snapshot().heldBytes, 256);
    assert.equal(ctl.lookup({ data: primary.data, buffer: primary.meta }).state, 'bound');

    const lastUnref = vm.q7(second, hooks);
    assert.equal(lastUnref.lastRef, true);
    assert.equal(lastUnref.dataFreed, true);
    assert.equal(finalFreeCalls, 1);
    const after = ctl.snapshot();
    assert.equal(after.heldBytes, 0);
    assert.equal(after.peakBytes, 256);
    assert.equal(after.countBound, 0);
    assert.equal(after.countClosing, 0);
    assert.equal(ctl.lookup({ data: primary.data, buffer: primary.meta }).found, false);

    const poolVm = createMock13486Vm();
    const poolCtl = mod.create({ capacityBytes: 4096, maxEntries: 4 });
    const poolData = poolVm.allocate(128);
    const pooled = poolVm.aw(poolData, 128);
    Atomics.store(poolVm.words, pooled.meta + 16, 0x7000);
    const poolReservation = poolCtl.reserve(128, 3);
    assert.equal(poolReservation.ok, true);
    assert.equal(poolCtl.bind(poolReservation.token, { data: pooled.data, buffer: pooled.meta }).ok, true);

    let pooledFinals = 0;
    let poolReturns = 0;
    const poolOutcome = poolVm.q7(pooled, {
      onFinalDataFree() { pooledFinals += 1; },
      onPoolReturn(point) {
        poolReturns += 1;
        assert.equal(point.poolEntry, 0x7000);
      }
    });
    assert.equal(poolOutcome.lastRef, true);
    assert.equal(poolOutcome.poolReturned, true);
    assert.equal(poolOutcome.dataFreed, false);
    assert.equal(pooledFinals, 0);
    assert.equal(poolReturns, 1);
    const poolSnapshot = poolCtl.snapshot();
    assert.equal(poolSnapshot.heldBytes, 128);
    assert.equal(poolSnapshot.countBound, 1);
    assert.equal(poolSnapshot.countClosing, 0);
    assert.equal(poolCtl.lookup({ data: pooled.data, buffer: pooled.meta }).state, 'bound');
  });
});

test('real module 13486 factory: aw/VG/Q7 last-ref data free observed; pool return never credits', { timeout: 5000 }, async () => {
  await withWorkers(async () => {
    const { harness, vendor } = loadReal13486Factory();
    const ctl = mod.create({ capacityBytes: 4096, maxEntries: 4 });
    const payload = harness.allocate(256, 'payload');
    const primary = vendor.aw(payload, 256);
    const meta = harness.load32(primary);
    const data = harness.load32(primary + 4);
    assert.equal(data, payload);
    assert.equal(harness.getField(meta + 8, 9, 2), 1);

    const reservation = ctl.reserve(256, 7);
    assert.equal(reservation.ok, true);
    assert.equal(ctl.bind(reservation.token, { data, buffer: meta }).ok, true);

    const second = vendor.VG(primary);
    assert.equal(harness.getField(meta + 8, 9, 2), 2);

    // Q7 receives a holder word. The real path drops the ref through aI even
    // on a non-last ref, so the holder is cleared and the ref is freed while
    // the payload and the AVBuffer struct (metadata) stay alive.
    const primaryHolder = harness.allocate(4, 'ref-holder');
    harness.store32(primaryHolder, primary);
    vendor.Q7(primaryHolder);
    assert.equal(harness.load32(primaryHolder), 0);
    assert.equal(harness.isFreed(primary), true);
    assert.equal(harness.getField(meta + 8, 9, 2), 1);
    assert.equal(harness.isFreed(payload), false);
    assert.equal(harness.isFreed(meta), false);
    assert.equal(ctl.snapshot().heldBytes, 256);
    assert.equal(ctl.lookup({ data, buffer: meta }).state, 'bound');

    const secondHolder = harness.allocate(4, 'ref-holder');
    harness.store32(secondHolder, second);
    vendor.Q7(secondHolder);
    assert.equal(harness.load32(secondHolder), 0);
    assert.equal(harness.isFreed(second), true);
    assert.equal(harness.getField(meta + 8, 9, 2), 0);
    assert.equal(harness.isFreed(payload), true);
    assert.equal(harness.isFreed(meta), true);
    // The extracted vendor factory now contains the direct-free hook, but no
    // domain is registered in the harness 90187 facade, so the hook stays on
    // its benign unregistered path. The manual prepare/commit below is only an
    // integration checkpoint after observing the real allocator free; it is
    // not evidence that a registered hook ran.
    assert.equal(ctl.snapshot().heldBytes, 256);
    const prepared = ctl.prepareFinalFree({ data, buffer: meta });
    assert.equal(prepared.ok, true);
    assert.equal(ctl.commitFinalFree(prepared.ticket, { dataFreed: true }).ok, true);
    assert.equal(ctl.snapshot().heldBytes, 0);

    const poolHarness = createReal13486Harness();
    const poolVendor = loadReal13486Factory(poolHarness).vendor;
    const poolCtl = mod.create({ capacityBytes: 4096, maxEntries: 4 });
    const poolPayload = poolHarness.allocate(128, 'payload');
    const poolPrimary = poolVendor.aw(poolPayload, 128);
    const poolMeta = poolHarness.load32(poolPrimary);
    const poolData = poolHarness.load32(poolPrimary + 4);
    const poolEntry = poolHarness.allocate(32, 'pool-entry');
    const poolLock = poolHarness.allocate(16, 'pool-lock');
    poolHarness.store32(poolMeta + 16, poolEntry);
    poolHarness.store32(poolEntry + 12, poolLock);

    const poolReservation = poolCtl.reserve(128, 3);
    assert.equal(poolReservation.ok, true);
    assert.equal(poolCtl.bind(poolReservation.token, { data: poolData, buffer: poolMeta }).ok, true);

    const poolHolder = poolHarness.allocate(4, 'ref-holder');
    poolHarness.store32(poolHolder, poolPrimary);
    poolVendor.Q7(poolHolder);
    assert.equal(poolHarness.load32(poolHolder), 0);
    assert.equal(poolHarness.isFreed(poolPrimary), true);
    assert.equal(poolHarness.isFreed(poolData), false);
    assert.equal(poolHarness.getField(poolMeta + 8, 9, 2), 0);
    // This unref is the last one (1 -> 0), so the AVBuffer struct is released
    // even though the payload goes back to the pool instead of being freed.
    assert.equal(poolHarness.isFreed(poolMeta), true);
    const poolSnapshot = poolCtl.snapshot();
    assert.equal(poolSnapshot.heldBytes, 128);
    assert.equal(poolSnapshot.countBound, 1);
    assert.equal(poolSnapshot.countClosing, 0);
    assert.equal(poolCtl.lookup({ data: poolData, buffer: poolMeta }).state, 'bound');
  });
});

test('descriptor validation, unsupported non-shared input, id wrap fails closed', { timeout: 5000 }, async () => {
  await withWorkers(async () => {
    assert.throws(() => mod.create({ capacityBytes: mod.MAX_CAPACITY_BYTES + 1, maxEntries: 1 }), (error) => error.code === 'invalid-argument');
    assert.throws(() => mod.create({ capacityBytes: 100, maxEntries: 0 }), (error) => error.code === 'invalid-argument');
    assert.throws(() => mod.attach(null), (error) => error.code === 'invalid-descriptor');
    assert.throws(() => mod.attach({
      kind: mod.KIND,
      version: mod.VERSION,
      capacityBytes: 16,
      maxEntries: 1,
      sab: new ArrayBuffer(16)
    }), (error) => error.code === 'unsupported');

    const ctl = mod.create({ capacityBytes: 64, maxEntries: 2 });
    const descriptor = cloneValue(ctl.descriptor);
    const attached = mod.attach(descriptor);
    assert.equal(attached.snapshot().capacityBytes, 64);
    assert.equal(attached.descriptor.sab.byteLength, ctl.descriptor.sab.byteLength);
    assert.equal(ctl.reserve(8, 1).ok, true);
    assert.equal(attached.snapshot().heldBytes, 8);

    const tamperedCapacity = Object.assign({}, descriptor, { capacityBytes: descriptor.capacityBytes + 4 });
    assert.throws(() => mod.attach(tamperedCapacity), (error) => error.code === 'invalid-descriptor');
    const tamperedKind = Object.assign({}, descriptor, { kind: 'other' });
    assert.throws(() => mod.attach(tamperedKind), (error) => error.code === 'invalid-descriptor');
    assert.throws(() => mod.attach({
      kind: mod.KIND,
      version: mod.VERSION,
      capacityBytes: 64,
      maxEntries: 2,
      sab: new SharedArrayBuffer(descriptor.byteLength)
    }), (error) => error.code === 'invalid-descriptor');

    const wrapCtl = mod.create({ capacityBytes: 64, maxEntries: 2 });
    const view = new Int32Array(wrapCtl.descriptor.sab);
    Atomics.store(view, mod.LAYOUT.header.ID_COUNTER, 0x7ffffffd);
    const exhausted = wrapCtl.reserve(10, 1);
    assert.equal(exhausted.ok, false);
    assert.equal(exhausted.code, 'id-exhausted');
    let wrapSnapshot = wrapCtl.snapshot();
    assert.equal(wrapSnapshot.heldBytes, 0);
    assert.equal(wrapSnapshot.countReserved, 0);
    assert.equal(wrapSnapshot.peakBytes, 0);

    Atomics.store(view, mod.LAYOUT.header.ID_COUNTER, 0);
    const recovered = wrapCtl.reserve(10, 1);
    assert.equal(recovered.ok, true);
    assert.equal(wrapCtl.reclaimUnused(recovered.token).ok, true);
    wrapSnapshot = wrapCtl.snapshot();
    assert.equal(wrapSnapshot.heldBytes, 0);
    assert.equal(wrapSnapshot.idCounter, 1);
  });
});
