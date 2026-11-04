'use strict';

/*
 * Boundary tests for webpack factory 90187 (shared media-domain provenance).
 *
 * This step only adds the factory and its real-memory-domain handshake. It does
 * NOT wire the descriptor into AVPlayer, IO or the Q7/943 last-free paths, and
 * it does not claim generated-Worker (module 39484) bootstrap acceptance.
 *
 * The 90187 facade and the actual 90190 core are read from the shipped
 * libmedia source graph (the main avplayer.js plus the synchronously loaded
 * 90201..90204 chunks) and executed with only the two runtime/allocator
 * dependencies stubbed. Both stubs operate on a real shared
 * WebAssembly.Memory; the 90190 core is the exact extracted standalone
 * helper (never a stub or a reconstructed core). One test runs the same
 * extracted factories in a real node:worker_threads Worker, passing the actual
 * 90190 core source through workerData, with a structured-cloned descriptor.
 *
 * Node tests are serial (default top-level order), every async test has an
 * explicit timeout and every worker is terminated in a finally block. No
 * application server, upstream, browser or production instance is involved.
 */

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { Worker } = require('node:worker_threads');
const readLibmediaSourceGraph = require('./web-libmedia-source-graph.cjs');

const sourceGraph = readLibmediaSourceGraph();
const MODULE_ID = 90187;
const SIGNATURE = '90187:(module,exports,__webpack_require__)=>{';
const CORE_MODULE_ID = 90190;
const CORE_SIGNATURE = '90190:(module,exports,__webpack_require__)=>{';
const ROOT = path.join(__dirname, '..');
const HELPER_PATH = path.join(ROOT, 'web-src', 'player-shared-media-ownership.js');
const HEAP_BASE = 1024;
const ACTOR_SLOTS = 4;
const HELPER = require(HELPER_PATH);

/* Node exposes webcrypto as globalThis.crypto in current releases. Only fill
 * the global for older Node builds so the factory still exercises the real
 * crypto.getRandomValues path instead of an injected entropy hook. */
if (typeof globalThis.crypto === 'undefined' ||
    typeof globalThis.crypto.getRandomValues !== 'function') {
  Object.defineProperty(globalThis, 'crypto', {
    value: require('node:crypto').webcrypto,
    configurable: true,
    writable: true
  });
}

function helperText() {
  return fs.readFileSync(HELPER_PATH, 'utf8');
}

function extractFactorySource() {
  return sourceGraph.factory(MODULE_ID);
}

function extractCoreSource() {
  return sourceGraph.factory(CORE_MODULE_ID);
}

/* Evaluate the ACTUAL module 90190 core factory from chunk 90201. It has no
 * webpack dependencies; the facade in 90202 requires this exact module. */
function loadActualCore() {
  const holder = new Function('return ({' + extractCoreSource() + '});')();
  const moduleObject = { exports: {} };
  holder[CORE_MODULE_ID].call(moduleObject.exports, moduleObject, moduleObject.exports,
    function (id) {
      throw new Error('unexpected module90190 dependency ' + String(id));
    });
  return moduleObject.exports;
}

const REAL_CORE = loadActualCore();

function loadFactory(requireStub, source) {
  const factorySource = source === undefined ? extractFactorySource() : source;
  const holder = new Function('return ({' + factorySource + '});')();
  const factoryMethod = holder[MODULE_ID];
  assert.equal(typeof factoryMethod, 'function',
    'webpack entry 90187 must be a factory function');
  const moduleObject = { exports: {} };
  factoryMethod.call(moduleObject.exports, moduleObject, moduleObject.exports, requireStub);
  return { api: moduleObject.exports, factorySource };
}

function makeRequireStub(memory, allocator) {
  const runtime = { ce: memory };
  return function webpackRequire(id) {
    if (id === CORE_MODULE_ID) return REAL_CORE;
    if (id === 11286) return runtime;
    if (id === 46626) return allocator;
    throw new Error('unexpected webpack dependency ' + String(id));
  };
}

/* Test-only bump allocator inside a real shared WebAssembly.Memory. The
 * control block pointer therefore lives in the same shared memory domain the
 * factory memory handshake inspects. */
function createSharedAllocator(memory, options) {
  const settings = options || {};
  const words = new Int32Array(memory.buffer);
  const BUMP_WORD = 0;
  let gyCalls = 0;
  const freed = [];

  function zero(pointer, bytes) {
    for (let offset = 0; offset < bytes; offset += 4) {
      Atomics.store(words, (pointer + offset) >>> 2, 0);
    }
  }

  return {
    freed,
    get gyCalls() { return gyCalls; },
    Gy(size) {
      gyCalls += 1;
      if (settings.failGy) return 0;
      const bytes = Math.max(4, Math.ceil(size / 4) * 4);
      for (;;) {
        const current = Atomics.load(words, BUMP_WORD);
        if (current === 0) {
          Atomics.compareExchange(words, BUMP_WORD, 0, HEAP_BASE);
          continue;
        }
        const next = current + bytes;
        if (next > memory.buffer.byteLength) return 0;
        if (Atomics.compareExchange(words, BUMP_WORD, current, next) === current) {
          zero(current, bytes);
          return current;
        }
      }
    },
    Eb(pointer) {
      freed.push(pointer);
      if (typeof settings.onEb === 'function') settings.onEb(pointer);
      zero(pointer, 32);
      if (typeof settings.ebThrows === 'number' && settings.ebThrows > 0) {
        settings.ebThrows -= 1;
        throw new Error('allocator free failed');
      }
    }
  };
}

function makeSharedMemory() {
  return new WebAssembly.Memory({ initial: 1, maximum: 1, shared: true });
}

function controlSnapshot(memory, pointer) {
  return Array.from(new Int32Array(memory.buffer, pointer, 8));
}

function heldOf(descriptor) {
  return Atomics.load(new Int32Array(descriptor.sab), HELPER.LAYOUT.header.HELD);
}

function withFakeCrypto(implementation, fn) {
  const original = Object.getOwnPropertyDescriptor(globalThis, 'crypto');
  const fake = {
    getRandomValues(target) {
      return implementation(target);
    }
  };
  Object.defineProperty(globalThis, 'crypto', { value: fake, configurable: true, writable: true });
  try {
    return fn();
  } finally {
    if (original) Object.defineProperty(globalThis, 'crypto', original);
    else delete globalThis.crypto;
  }
}

function codedError(code, message) {
  const error = new Error(message || code);
  error.code = code;
  return error;
}

const WORKER_SOURCE = String.raw`
'use strict';
const { parentPort, workerData } = require('node:worker_threads');
const HEAP_BASE = 1024;

function createSharedAllocator(memory) {
  const words = new Int32Array(memory.buffer);
  const BUMP_WORD = 0;
  const freed = [];
  function zero(pointer, bytes) {
    for (let offset = 0; offset < bytes; offset += 4) {
      Atomics.store(words, (pointer + offset) >>> 2, 0);
    }
  }
  return {
    freed,
    Gy(size) {
      const bytes = Math.max(4, Math.ceil(size / 4) * 4);
      for (;;) {
        const current = Atomics.load(words, BUMP_WORD);
        if (current === 0) {
          Atomics.compareExchange(words, BUMP_WORD, 0, HEAP_BASE);
          continue;
        }
        const next = current + bytes;
        if (next > memory.buffer.byteLength) return 0;
        if (Atomics.compareExchange(words, BUMP_WORD, current, next) === current) {
          zero(current, bytes);
          return current;
        }
      }
    },
    Eb(pointer) {
      freed.push(pointer);
      zero(pointer, 32);
    }
  };
}

function loadWorkerCore() {
  const coreHolder = new Function('return ({' + workerData.coreSource + '});')();
  const coreModule = { exports: {} };
  coreHolder[90190].call(coreModule.exports, coreModule, coreModule.exports, function (id) {
    throw new Error('unexpected module90190 dependency ' + String(id));
  });
  return coreModule.exports;
}

const memory = workerData.descriptor.wasmMemory;
const allocator = createSharedAllocator(memory);
const runtime = { ce: memory };
const coreApi = loadWorkerCore();
const api = (function () {
  const holder = new Function('return ({' + workerData.factorySource + '});')();
  const moduleObject = { exports: {} };
  holder[90187].call(moduleObject.exports, moduleObject, moduleObject.exports, function (id) {
    if (id === 90190) return coreApi;
    if (id === 11286) return runtime;
    if (id === 46626) return allocator;
    throw new Error('unexpected webpack dependency ' + String(id));
  });
  return moduleObject.exports;
})();

let controller = null;

function handle(message) {
  switch (message.op) {
    case 'attach':
      controller = api.attachDomainDescriptor(message.descriptor, message.actorSlot);
      return { heldBytes: controller.heldBytes(), snapshot: controller.snapshot() };
    case 'reserve':
      return controller.reserve(message.bytes, message.generation);
    case 'bind':
      return controller.bind(message.token, {
        data: message.data,
        buffer: message.buffer,
        size: message.size
      });
    case 'lookup':
      return controller.lookup({ data: message.data, buffer: message.buffer });
    case 'snapshot':
      return controller.snapshot();
    case 'destroy':
      try {
        return { ok: true, result: api.destroyDomainDescriptor(message.descriptor) };
      } catch (error) {
        return {
          ok: false,
          code: error && error.code,
          message: String(error && error.message || error),
          freedCount: allocator.freed.length
        };
      }
    default:
      throw new Error('unknown worker op ' + String(message.op));
  }
}

parentPort.on('message', function (message) {
  Promise.resolve()
    .then(function () { return handle(message); })
    .then(
      function (value) {
        parentPort.postMessage({ id: message.id, ok: true, value: value });
      },
      function (error) {
        parentPort.postMessage({
          id: message.id,
          ok: false,
          error: {
            code: error && error.code,
            message: String(error && error.message || error)
          }
        });
      }
    );
});
`;

function requestWorker(worker, op, payload, timeoutMs) {
  requestWorker.sequence = (requestWorker.sequence || 0) + 1;
  const id = requestWorker.sequence;
  const limit = timeoutMs === undefined ? 3000 : timeoutMs;
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
      'worker exited with ' + String(code) + ' while handling ' + op
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

async function withWorker(workerData, fn) {
  const worker = new Worker(WORKER_SOURCE, { eval: true, workerData });
  try {
    return await fn({
      request(op, payload, timeoutMs) {
        return requestWorker(worker, op, payload, timeoutMs);
      }
    });
  } finally {
    await worker.terminate().catch(() => {});
  }
}

/* ------------------------------------------------------------------ */
/* Factory parity and facade                                           */
/* ------------------------------------------------------------------ */

test('module 90187 facade requires the actual 90190 core; the core body embeds the standalone helper byte-for-byte',
  { timeout: 5000 }, () => {
    const bundle = sourceGraph.text;
    assert.equal((bundle.match(/90187:\(module,exports,__webpack_require__\)=>\{/g) || []).length, 1,
      'factory 90187 must appear exactly once with the required signature');
    assert.equal((bundle.match(/90190:\(module,exports,__webpack_require__\)=>\{/g) || []).length, 1,
      'factory 90190 must appear exactly once with the required signature');
    assert.equal(bundle.indexOf('90187:(e,t,i)=>'), -1,
      'factory 90187 must not reuse the three-argument webpack signature');

    const source = extractFactorySource();
    const body = source.slice(SIGNATURE.length);
    assert.ok(body.replace(/^\s+/, '').startsWith('const core=__webpack_require__(90190);'),
      'the facade body must start by requiring the actual extracted module 90190 core');

    /* The physical split moved the exact standalone ownership UMD bytes from
     * 90187 into 90190. Byte parity therefore targets the actual 90190 body;
     * only the wrapper separator whitespace around it may differ. */
    const helper = helperText();
    const coreBody = extractCoreSource().slice(CORE_SIGNATURE.length, -1);
    const helperAt = coreBody.indexOf(helper);
    assert.notEqual(helperAt, -1,
      'module 90190 body must contain the exact standalone helper bytes (no folded strings)');
    assert.equal(coreBody.indexOf(helper), coreBody.search(/\S/),
      'the module 90190 body must begin with the exact standalone helper bytes');
    assert.equal(coreBody.slice(helperAt + helper.length).trim(), '',
      'nothing but wrapper whitespace may follow the exact standalone helper bytes');
    assert.equal(bundle.split(helper).length - 1, 1,
      'the standalone helper must be embedded exactly once in the shipped source graph');
    assert.deepEqual(Object.keys(REAL_CORE).sort(), Object.keys(HELPER).sort(),
      'the actual module 90190 core must expose exactly the standalone core API');

    // Module 13486 is deliberately untouched by this step; its current vendor
    // export header must still be present so the 13 ownership tests keep their
    // exact-factory parity target.
    assert.ok(bundle.includes(
      '13486:(e,t,i)=>{i.d(t,{A6:()=>A,D$:()=>u,Q7:()=>m,VG:()=>f,aw:()=>l,us:()=>g});'
    ), 'module 13486 must remain byte-identical at its export header');

    const memory = makeSharedMemory();
    const allocator = createSharedAllocator(memory);
    const { api } = loadFactory(makeRequireStub(memory, allocator), source);
    assert.equal(Object.isFrozen(api), true, 'the facade must be frozen');
    assert.equal(api.create, REAL_CORE.create,
      'the facade must expose the actual module 90190 core create(), not a reconstructed core');
    assert.equal(api.attach, REAL_CORE.attach,
      'the facade must expose the actual module 90190 core attach()');
    assert.equal(api.LAYOUT, REAL_CORE.LAYOUT,
      'the facade must expose the actual module 90190 core layout');
    assert.deepEqual(
      Object.keys(api).sort(),
      Object.keys(HELPER)
        .concat(['createDomainDescriptor', 'attachDomainDescriptor', 'destroyDomainDescriptor'])
        .concat(['registerDomain', 'unregisterDomain', 'prepareDataFree', 'commitDataFree',
          'dataFreeFailed', 'getReleaseRegistrySnapshot'])
        .sort(),
      'the facade must expose every core export plus exactly the three domain operations ' +
      'and the six release-registry operations'
    );
    assert.equal(api.VERSION, HELPER.VERSION);
    assert.equal(api.KIND, HELPER.KIND);
    assert.equal(api.MAX_CAPACITY_BYTES, HELPER.MAX_CAPACITY_BYTES);
    assert.equal(api.MAX_ENTRIES, HELPER.MAX_ENTRIES);
    assert.equal(typeof api.create, 'function');
    assert.equal(typeof api.attach, 'function');
    assert.equal(typeof api.createDomainDescriptor, 'function');
    assert.equal(typeof api.attachDomainDescriptor, 'function');
    assert.equal(typeof api.destroyDomainDescriptor, 'function');

    const ctl = api.create({ capacityBytes: 128, maxEntries: 2 });
    const reservation = ctl.reserve(64, 1);
    assert.equal(reservation.ok, true);
    assert.equal(ctl.bind(reservation.token, { data: 0x100, buffer: 0x200 }).ok, true);
    assert.equal(ctl.heldBytes(), 64, 'the original core API must still work through the facade');
  });

/* ------------------------------------------------------------------ */
/* Real Worker: clone + shared domain                                  */
/* ------------------------------------------------------------------ */

test('a structured-cloned descriptor proves the shared domain in a real Worker',
  { timeout: 10000 }, async () => {
    const memory = makeSharedMemory();
    const allocator = createSharedAllocator(memory);
    const { api } = loadFactory(makeRequireStub(memory, allocator));
    const descriptor = api.createDomainDescriptor({
      capacityBytes: 4096,
      maxEntries: 8,
      domainId: 'player-1'
    });
    assert.equal(descriptor.domainId, 'player-1');
    assert.equal(descriptor.wasmMemory, memory);
    assert.ok(descriptor.proofPointer >= HEAP_BASE);
    assert.equal(descriptor.proofPointer % 4, 0);
    assert.equal(descriptor.nonce.length, 4);
    assert.ok(descriptor.nonce.some((value) => value !== 0));
    assert.deepEqual(controlSnapshot(memory, descriptor.proofPointer),
      descriptor.nonce.concat([0, 0, 0, 0]),
      'the 32-byte control block must hold four nonce words then four zero scratch words');

    const mainController = api.attachDomainDescriptor(descriptor, 0);
    const first = mainController.reserve(1000, 1);
    assert.equal(first.ok, true);
    assert.equal(mainController.bind(first.token, {
      data: 0x11110000,
      buffer: 0x22220000,
      size: 1000
    }).ok, true);

    const cloned = structuredClone(descriptor);
    assert.ok(cloned.wasmMemory instanceof WebAssembly.Memory,
      'structuredClone must transfer the runtime WebAssembly.Memory');
    assert.ok(cloned.sab instanceof SharedArrayBuffer,
      'structuredClone must transfer the shared ownership table');
    assert.equal(new Int32Array(cloned.sab)[HELPER.LAYOUT.header.HELD], 1000,
      'the cloned table must be the live table, not a copy');
    const probeWord = (descriptor.proofPointer >>> 2) + 7;
    Atomics.store(new Int32Array(cloned.wasmMemory.buffer), probeWord, 0x1234);
    assert.equal(Atomics.load(new Int32Array(memory.buffer), probeWord), 0x1234,
      'the cloned WebAssembly.Memory must share the runtime backing store');
    Atomics.store(new Int32Array(memory.buffer), probeWord, 0);

    await withWorker({ factorySource: extractFactorySource(), coreSource: extractCoreSource(),
      descriptor: cloned }, async (worker) => {
      const attached = await worker.request('attach', { descriptor: cloned, actorSlot: 1 });
      assert.equal(attached.heldBytes, 1000);
      assert.equal(attached.snapshot.countBound, 1);

      const reserved = await worker.request('reserve', { bytes: 500, generation: 2 });
      assert.equal(reserved.ok, true);
      const bound = await worker.request('bind', {
        token: reserved.token,
        data: 0x33330000,
        buffer: 0x44440000,
        size: 500
      });
      assert.equal(bound.ok, true);

      assert.equal(mainController.heldBytes(), 1500,
        'a reservation in the worker realm must be visible in the owner table');
      assert.equal(mainController.lookup({ data: 0x33330000, buffer: 0x44440000 }).state, 'bound');
      assert.equal((await worker.request('snapshot')).heldBytes, 1500);

      const crossRealmDestroy = await worker.request('destroy', { descriptor: cloned });
      assert.equal(crossRealmDestroy.ok, false);
      assert.equal(crossRealmDestroy.code, 'EIO_DOMAIN_NOT_OWNED');
      assert.equal(crossRealmDestroy.freedCount, 0,
        'a descriptor copy in another realm must not free the owner control block');
    });

    assert.throws(() => api.destroyDomainDescriptor(descriptor),
      (error) => error.code === 'EIO_DOMAIN_STILL_HELD');
    for (const key of [
      { data: 0x11110000, buffer: 0x22220000 },
      { data: 0x33330000, buffer: 0x44440000 }
    ]) {
      const prepared = mainController.prepareFinalFree(key);
      assert.equal(prepared.ok, true);
      assert.equal(mainController.commitFinalFree(prepared.ticket, { dataFreed: true }).ok, true);
    }
    assert.equal(mainController.heldBytes(), 0);
    assert.equal(api.destroyDomainDescriptor(descriptor), true);
    assert.deepEqual(allocator.freed, [descriptor.proofPointer],
      'the owner realm must free the control block exactly once');
  });

/* ------------------------------------------------------------------ */
/* Physical domain mismatch                                            */
/* ------------------------------------------------------------------ */

test('two distinct shared memories with matching nonce and address fail the physical proof',
  { timeout: 5000 }, () => {
    const memoryA = makeSharedMemory();
    const memoryB = makeSharedMemory();
    const allocatorA = createSharedAllocator(memoryA);
    const allocatorB = createSharedAllocator(memoryB);
    const apiA = loadFactory(makeRequireStub(memoryA, allocatorA)).api;
    const apiB = loadFactory(makeRequireStub(memoryB, allocatorB)).api;
    const descriptor = apiA.createDomainDescriptor({
      capacityBytes: 1024,
      maxEntries: 4,
      domainId: 'A'
    });
    const pointer = descriptor.proofPointer;
    const word = pointer >>> 2;
    assert.deepEqual(controlSnapshot(memoryA, pointer),
      descriptor.nonce.concat([0, 0, 0, 0]),
      'the control block layout must be nonce words then scratch words');

    /* Copy the same nonce to the same wasm32 address in the unrelated memory.
     * This defeats nonce equality alone; only the scratch write/read proof can
     * distinguish the two physical domains. */
    const viewB = new Int32Array(memoryB.buffer);
    for (let index = 0; index < 8; index += 1) {
      Atomics.store(viewB, word + index, index < 4 ? descriptor.nonce[index] : 0);
    }
    const forged = Object.assign({}, descriptor, { wasmMemory: memoryB });

    assert.throws(() => apiA.attachDomainDescriptor(forged, 0),
      (error) => error.code === 'EIO_MEMORY_DOMAIN_MISMATCH',
      'a descriptor Memory that is not the runtime Memory must fail');
    assert.throws(() => apiB.attachDomainDescriptor(descriptor, 1),
      (error) => error.code === 'EIO_MEMORY_DOMAIN_MISMATCH',
      'a runtime Memory that is not the descriptor Memory must fail');

    assert.deepEqual(controlSnapshot(memoryA, pointer).slice(4), [0, 0, 0, 0],
      'the descriptor memory scratch words must be left zero');
    assert.deepEqual(controlSnapshot(memoryB, pointer).slice(4), [0, 0, 0, 0],
      'the runtime memory scratch words must never be written');
    assert.deepEqual(controlSnapshot(memoryA, pointer).slice(0, 4), descriptor.nonce);
    assert.deepEqual(controlSnapshot(memoryB, pointer).slice(0, 4), descriptor.nonce,
      'a failed proof must not alter either nonce');
    assert.equal(heldOf(descriptor), 0, 'a failed domain proof must not charge media bytes');
    assert.deepEqual(allocatorA.freed, []);
    assert.deepEqual(allocatorB.freed, []);
  });

/* ------------------------------------------------------------------ */
/* Input validation                                                    */
/* ------------------------------------------------------------------ */

test('nonshared or fake memories and invalid pointers, nonces and slots are refused',
  { timeout: 5000 }, () => {
    const memory = makeSharedMemory();
    const allocator = createSharedAllocator(memory);
    const api = loadFactory(makeRequireStub(memory, allocator)).api;
    const descriptor = api.createDomainDescriptor({
      capacityBytes: 1024,
      maxEntries: 4,
      domainId: 'valid'
    });
    const pointer = descriptor.proofPointer;
    const before = controlSnapshot(memory, pointer);

    const nonshared = new WebAssembly.Memory({ initial: 1, maximum: 1 });
    const fakeMemory = { buffer: new ArrayBuffer(64) };
    for (const wasmMemory of [nonshared, fakeMemory, null, 'memory']) {
      assert.throws(
        () => api.attachDomainDescriptor(Object.assign({}, descriptor, { wasmMemory }), 0),
        (error) => error.code === 'EIO_MEMORY_DOMAIN_MISMATCH'
      );
    }
    for (const proofPointer of [0, 2, pointer + 2, 0xfffffff0, 0x100000, 'pointer', null]) {
      assert.throws(
        () => api.attachDomainDescriptor(Object.assign({}, descriptor, { proofPointer }), 0),
        (error) => error.code === 'EIO_MEMORY_DOMAIN_MISMATCH'
      );
    }
    for (const nonce of [[0, 0, 0, 0], [1, 2, 3], [1, 2, 3, '4'],
      [1, 2, 3, 4294967296], null]) {
      assert.throws(
        () => api.attachDomainDescriptor(Object.assign({}, descriptor, { nonce }), 0),
        (error) => error.code === 'EIO_MEMORY_DOMAIN_MISMATCH'
      );
    }
    for (const actorSlot of [-1, 4, 1.5, '0', null, undefined]) {
      assert.throws(
        () => api.attachDomainDescriptor(descriptor, actorSlot),
        (error) => error.code === 'invalid-argument'
      );
    }
    assert.throws(
      () => api.attachDomainDescriptor(Object.assign({}, descriptor, { kind: 'other' }), 0),
      (error) => error.code === 'invalid-descriptor',
      'malformed core descriptors keep the core error'
    );
    assert.throws(
      () => api.attachDomainDescriptor(
        Object.assign({}, descriptor, { sab: new ArrayBuffer(8) }), 0),
      (error) => error.code === 'unsupported',
      'the core still refuses a non-shared table'
    );

    assert.deepEqual(controlSnapshot(memory, pointer), before,
      'failed attaches must not write either memory view');
    assert.equal(heldOf(descriptor), 0);
    assert.deepEqual(allocator.freed, []);
  });

test('every actor slot proves the shared domain and a failed nonce check writes nothing',
  { timeout: 5000 }, () => {
    const memory = makeSharedMemory();
    const allocator = createSharedAllocator(memory);
    const api = loadFactory(makeRequireStub(memory, allocator)).api;
    const descriptor = api.createDomainDescriptor({
      capacityBytes: 1024,
      maxEntries: 4,
      domainId: 'slots'
    });
    const pointer = descriptor.proofPointer;

    const controllers = [];
    for (let actorSlot = 0; actorSlot < ACTOR_SLOTS; actorSlot += 1) {
      controllers.push(api.attachDomainDescriptor(descriptor, actorSlot));
      assert.deepEqual(controlSnapshot(memory, pointer).slice(4), [0, 0, 0, 0],
        'slot ' + actorSlot + ' must reset its scratch word');
    }
    const reservation = controllers[0].reserve(64, 1);
    assert.equal(reservation.ok, true);
    for (const controller of controllers) {
      assert.equal(controller.heldBytes(), 64,
        'all attached controllers must see the same shared table');
    }

    const view = new Int32Array(memory.buffer);
    const nonceWord = pointer >>> 2;
    const original = Atomics.load(view, nonceWord);
    Atomics.store(view, nonceWord, (original + 1) | 0);
    const tampered = controlSnapshot(memory, pointer);
    assert.throws(() => api.attachDomainDescriptor(descriptor, 2),
      (error) => error.code === 'EIO_MEMORY_DOMAIN_MISMATCH');
    assert.deepEqual(controlSnapshot(memory, pointer), tampered,
      'a failed nonce check must not alter either memory');
    Atomics.store(view, nonceWord, original);

    assert.equal(controllers[0].reclaimUnused(reservation.token).ok, true);
    assert.equal(controllers[0].heldBytes(), 0);
  });

/* ------------------------------------------------------------------ */
/* Create failure paths                                                */
/* ------------------------------------------------------------------ */

test('failed create validation, allocation and entropy release the control block',
  { timeout: 5000 }, () => {
    const memory = makeSharedMemory();
    const allocator = createSharedAllocator(memory);
    const api = loadFactory(makeRequireStub(memory, allocator)).api;

    assert.throws(
      () => api.createDomainDescriptor({ capacityBytes: -1, maxEntries: 1, domainId: 'x' }),
      (error) => error.code === 'invalid-argument'
    );
    assert.equal(allocator.gyCalls, 0,
      'malformed core config must be rejected before the control malloc');
    assert.deepEqual(allocator.freed, []);

    for (const domainId of ['', 'x'.repeat(201), 42, null]) {
      assert.throws(
        () => api.createDomainDescriptor({ capacityBytes: 64, maxEntries: 1, domainId }),
        (error) => error.code === 'invalid-argument'
      );
    }
    assert.equal(allocator.gyCalls, 0, 'domainId validation must not allocate');

    const failingAllocator = createSharedAllocator(memory, { failGy: true });
    const failingApi = loadFactory(makeRequireStub(memory, failingAllocator)).api;
    assert.throws(
      () => failingApi.createDomainDescriptor({ capacityBytes: 64, maxEntries: 1, domainId: 'x' }),
      (error) => error.code === 'unsupported'
    );
    assert.equal(failingAllocator.gyCalls, 1);
    assert.deepEqual(failingAllocator.freed, [],
      'a failed control malloc has nothing to release');

    const gyBefore = allocator.gyCalls;
    const freedBefore = allocator.freed.length;
    withFakeCrypto((target) => { target.fill(0); return target; }, () => {
      assert.throws(
        () => api.createDomainDescriptor({ capacityBytes: 64, maxEntries: 1, domainId: 'x' }),
        (error) => error.code === 'EIO_MEMORY_DOMAIN_NONCE',
        'two all-zero entropy draws must fail closed'
      );
    });
    assert.equal(allocator.gyCalls, gyBefore + 1);
    assert.equal(allocator.freed.length, freedBefore + 1,
      'an all-zero nonce must release the control block exactly once');

    withFakeCrypto(() => { throw new Error('entropy unavailable'); }, () => {
      assert.throws(
        () => api.createDomainDescriptor({ capacityBytes: 64, maxEntries: 1, domainId: 'x' }),
        /entropy unavailable/
      );
    });
    assert.equal(allocator.freed.length, freedBefore + 2,
      'throwing entropy must release the control block exactly once');

    const missingRuntime = loadFactory(makeRequireStub(null, allocator)).api;
    assert.throws(
      () => missingRuntime.createDomainDescriptor({ capacityBytes: 64, maxEntries: 1, domainId: 'x' }),
      (error) => error.code === 'unsupported'
    );
    const nonsharedRuntime = loadFactory(makeRequireStub(
      new WebAssembly.Memory({ initial: 1, maximum: 1 }), allocator)).api;
    assert.throws(
      () => nonsharedRuntime.createDomainDescriptor({ capacityBytes: 64, maxEntries: 1, domainId: 'x' }),
      (error) => error.code === 'unsupported'
    );
    assert.equal(allocator.gyCalls, gyBefore + 2,
      'a rejected runtime memory must not allocate a control block');

    const descriptor = api.createDomainDescriptor({
      capacityBytes: 64,
      maxEntries: 1,
      domainId: 'ok'
    });
    assert.equal(heldOf(descriptor), 0,
      'creating a domain descriptor is not a media byte reservation');
    assert.equal(descriptor.nonce.length, 4);
    assert.ok(descriptor.nonce.some((value) => value !== 0));
    assert.ok(descriptor.proofPointer >= HEAP_BASE);
    assert.deepEqual(controlSnapshot(memory, descriptor.proofPointer),
      descriptor.nonce.concat([0, 0, 0, 0]));
  });

/* ------------------------------------------------------------------ */
/* Destroy rules                                                       */
/* ------------------------------------------------------------------ */

test('owned destroy invalidates the nonce, frees only the control block and refuses copies',
  { timeout: 5000 }, () => {
    const memory = makeSharedMemory();
    let reentrantCode = null;
    let ebSnapshot = null;
    let descriptor = null;
    let api = null;
    const allocator = createSharedAllocator(memory, {
      onEb(pointer) {
        ebSnapshot = controlSnapshot(memory, pointer);
        assert.deepEqual(ebSnapshot.slice(0, 4), [0, 0, 0, 0],
          'the control nonce must be invalidated before the allocator free');
        try {
          api.destroyDomainDescriptor(descriptor);
        } catch (error) {
          reentrantCode = error.code;
        }
      }
    });
    api = loadFactory(makeRequireStub(memory, allocator)).api;
    descriptor = api.createDomainDescriptor({
      capacityBytes: 128,
      maxEntries: 2,
      domainId: 'destroy'
    });
    const pointer = descriptor.proofPointer;
    const controller = api.attachDomainDescriptor(descriptor, 0);
    const reservation = controller.reserve(100, 1);
    assert.equal(reservation.ok, true);

    assert.throws(() => api.destroyDomainDescriptor(descriptor),
      (error) => error.code === 'EIO_DOMAIN_STILL_HELD');
    assert.equal(controller.heldBytes(), 100, 'a refused destroy must not change held bytes');
    assert.deepEqual(allocator.freed, [], 'a held domain must not be freed');
    assert.deepEqual(controlSnapshot(memory, pointer).slice(0, 4), descriptor.nonce,
      'a refused destroy must not invalidate the nonce');

    const clone = Object.assign({}, descriptor);
    assert.throws(() => api.destroyDomainDescriptor(clone),
      (error) => error.code === 'EIO_DOMAIN_NOT_OWNED');
    assert.throws(() => api.destroyDomainDescriptor(null),
      (error) => error.code === 'EIO_DOMAIN_NOT_OWNED');
    assert.deepEqual(allocator.freed, []);

    const peakBefore = controller.snapshot().peakBytes;
    assert.equal(controller.reclaimUnused(reservation.token).ok, true);
    assert.equal(api.destroyDomainDescriptor(descriptor), true);
    assert.deepEqual(allocator.freed, [pointer], 'the control block must be freed exactly once');
    assert.ok(ebSnapshot !== null);
    assert.ok(ebSnapshot.slice(4).every((value) => value === 0),
      'the scratch words must stay zero through destroy');
    assert.equal(reentrantCode, 'EIO_DOMAIN_FREE_UNVERIFIED',
      'a reentrant destroy during CLOSING must not retry the free');
    assert.ok(memory.buffer instanceof SharedArrayBuffer,
      'destroy must not free the shared WebAssembly.Memory');

    const after = controller.snapshot();
    assert.equal(after.heldBytes, 0);
    assert.equal(after.peakBytes, peakBefore, 'destroy must not reset the ownership table');

    assert.throws(() => api.destroyDomainDescriptor(descriptor),
      (error) => error.code === 'EIO_DOMAIN_NOT_OWNED');
    assert.deepEqual(allocator.freed, [pointer]);

    for (const stale of [descriptor, clone]) {
      assert.throws(() => api.attachDomainDescriptor(stale, 0),
        (error) => error.code === 'EIO_MEMORY_DOMAIN_MISMATCH',
        'a destroyed or copied descriptor must not re-prove the domain');
    }
    assert.deepEqual(allocator.freed, [pointer]);
  });

test('an allocator free that throws leaves an unverified fault and is never retried',
  { timeout: 5000 }, () => {
    const memory = makeSharedMemory();
    const allocator = createSharedAllocator(memory, { ebThrows: 1 });
    const api = loadFactory(makeRequireStub(memory, allocator)).api;
    const descriptor = api.createDomainDescriptor({
      capacityBytes: 64,
      maxEntries: 1,
      domainId: 'fault'
    });
    const pointer = descriptor.proofPointer;
    const controller = api.attachDomainDescriptor(descriptor, 0);
    assert.equal(controller.heldBytes(), 0);

    assert.throws(() => api.destroyDomainDescriptor(descriptor), /allocator free failed/);
    assert.equal(allocator.freed.length, 1);
    assert.deepEqual(controlSnapshot(memory, pointer).slice(0, 4), [0, 0, 0, 0],
      'the nonce stays invalidated on an unverified free');

    for (let attempt = 0; attempt < 3; attempt += 1) {
      assert.throws(() => api.destroyDomainDescriptor(descriptor),
        (error) => error.code === 'EIO_DOMAIN_FREE_UNVERIFIED',
        'a FAULTED control block must be refused, not freed again');
    }
    assert.equal(allocator.freed.length, 1,
      'a FAULTED control block must never receive a second free');
    assert.throws(() => api.destroyDomainDescriptor(Object.assign({}, descriptor)),
      (error) => error.code === 'EIO_DOMAIN_NOT_OWNED');
    assert.equal(controller.heldBytes(), 0, 'a faulted destroy must not credit media bytes');
    assert.equal(allocator.freed.length, 1);
  });
