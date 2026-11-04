'use strict';

/*
 * Direct true-last-free hook tests (step2 of the isolated candidate A).
 *
 * Scope of this file:
 *   - the embedded module 90188 (release registry, chunk 90204) is
 *     byte-identical to web-src/player-shared-media-release.js;
 *   - the embedded module 90190 (ownership core, chunk 90201) contains the
 *     standalone web-src/player-shared-media-ownership.js bytes;
 *   - module 90187 (chunk 90202) is the domain/release facade: it requires the
 *     ACTUAL module 90190 core first and module 90188 only lazily;
 *   - the ACTUAL vendor module 13486 factory is executed together with the
 *     ACTUAL extracted module 90187 facade, the ACTUAL extracted module 90190
 *     core and the ACTUAL extracted module 90188 factory over a real shared
 *     WebAssembly.Memory.
 *
 * Source text is read through tests/web-libmedia-source-graph.cjs, which
 * composes the shipped main avplayer.js plus its four synchronously loaded
 * chunks. That is source-graph composition for factory tests, not a product
 * loader and not runtime Worker-loading proof.
 *
 * Evidence limits (deliberate):
 *   - The memory allocator below is a TEST FIXTURE. It is not the real WASM
 *     malloc/AVPlayer integration and it does not prove pool retirement,
 *     reallocation or replacement.
 *   - No AVPlayer task descriptor / NX binding is installed or claimed here.
 *     Tests register a domain explicitly; this is fixture setup, not evidence
 *     that the real player binds a domain.
 *   - The Node Workers below are hand-assembled from the real factories. They
 *     are not the generated libmedia worker (module 39484) and no whole-budget
 *     or full-playback completion is claimed.
 *   - "Observer error-path mocks" (Proxy wrappers that throw from one hook
 *     method) are clearly labelled; they are not real-domain accounting
 *     evidence and they must never grant credit.
 *
 * No application server, upstream, browser, network or production instance is
 * used. Every async test has an explicit timeout and terminates its workers in
 * a finally block.
 */

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const { Worker } = require('node:worker_threads');
const readLibmediaSourceGraph = require('./web-libmedia-source-graph.cjs');

const ROOT = path.join(__dirname, '..');
const OWNERSHIP_PATH = path.join(ROOT, 'web-src', 'player-shared-media-ownership.js');
const RELEASE_PATH = path.join(ROOT, 'web-src', 'player-shared-media-release.js');
const OWNERSHIP = require(OWNERSHIP_PATH);
const RELEASE_TEXT = fs.readFileSync(RELEASE_PATH, 'utf8');
const OWNERSHIP_TEXT = fs.readFileSync(OWNERSHIP_PATH, 'utf8');
const sourceGraph = readLibmediaSourceGraph();

const MODULE_90187 = 90187;
const MODULE_90188 = 90188;
const MODULE_90190 = 90190;
const MODULE_13486 = 13486;

const SIGNATURE_90187 = '90187:(module,exports,__webpack_require__)=>{';
const SIGNATURE_90188 = '90188:(module,exports,__webpack_require__)=>{';
const END_90188 = '/*__EIO_SHARED_MEDIA_RELEASE_FACTORY_END__*/';
const SIGNATURE_90190 = '90190:(module,exports,__webpack_require__)=>{';
const SIGNATURE_13486 = '13486:(e,t,i)=>{';

const HEAP_BASE = 4096;
const BUMP_WORD = 0;
const LOG_COUNT_WORD = 1;
const LOG_BASE_WORD = 256;
const LOG_CAPACITY = 512;
const ACTOR_SLOTS = 4;

/* Node exposes webcrypto as globalThis.crypto in current releases. Only fill the
 * global for older Node builds so the module90187 nonce path still uses the
 * real crypto.getRandomValues. */
if (typeof globalThis.crypto === 'undefined' ||
    typeof globalThis.crypto.getRandomValues !== 'function') {
  Object.defineProperty(globalThis, 'crypto', {
    value: require('node:crypto').webcrypto,
    configurable: true,
    writable: true
  });
}

function extract90187Source() {
  return sourceGraph.factory(MODULE_90187);
}

function extract90188Source() {
  return sourceGraph.factory(MODULE_90188);
}

function extract90190Source() {
  return sourceGraph.factory(MODULE_90190);
}

/* Module 13486 was not moved by the physical split and still lives in the main
 * bundle. It has no string literals or comments in its minified body, so a
 * brace counter over the exact factory source is enough to cut it out of the
 * main source without inventing a second embedded copy. */
function extract13486Source() {
  const bundle = sourceGraph.main;
  const start = bundle.indexOf(SIGNATURE_13486);
  assert.notEqual(start, -1, 'module 13486 is missing from the main avplayer.js source');
  assert.equal(bundle.indexOf(SIGNATURE_13486, start + 1), -1,
    'module 13486 must be unique in the main avplayer.js source');
  let depth = 0;
  for (let index = bundle.indexOf('{', start); index < bundle.length; index += 1) {
    const character = bundle[index];
    if (character === '{') depth += 1;
    else if (character === '}') {
      depth -= 1;
      if (depth === 0) return bundle.slice(start, index + 1);
    }
  }
  throw new Error('module 13486 has no terminating closing brace');
}

function evaluateFactoryMap(source) {
  return new Function('return ({' + source + '});')();
}

/* Evaluate the ACTUAL module 90190 ownership core factory from chunk 90201.
 * It has no webpack dependencies; module 90187 requires this exact module. */
function loadActualCore() {
  const holder = evaluateFactoryMap(extract90190Source());
  const moduleObject = { exports: {} };
  holder[MODULE_90190].call(moduleObject.exports, moduleObject, moduleObject.exports,
    function (id) {
      throw new Error('unexpected module90190 dependency ' + String(id));
    });
  return moduleObject.exports;
}

/* Webpack-like lazy module 90188: nothing is evaluated until module 90187
 * actually requires it from registerDomain. */
function createReleaseModuleLoader() {
  let exportsValue = null;
  let evaluations = 0;
  return {
    get evaluations() { return evaluations; },
    load() {
      if (exportsValue === null) {
        const holder = evaluateFactoryMap(extract90188Source());
        const moduleObject = { exports: {} };
        holder[MODULE_90188](moduleObject, moduleObject.exports, function (id) {
          throw new Error('module 90188 must not require anything, got ' + String(id));
        });
        exportsValue = moduleObject.exports;
        evaluations += 1;
      }
      return exportsValue;
    }
  };
}

/* Load the ACTUAL 90187 facade wired to the ACTUAL 90190 core (never a stub or
 * a reconstructed core), mirroring the real webpack require graph. 90188 stays
 * lazy and is only evaluated when registerDomain asks for it. */
function load90187(memory, allocator, options) {
  const settings = options || {};
  const releaseModule = settings.releaseModule || createReleaseModuleLoader();
  const core = settings.core || loadActualCore();
  const state = { require90188: 0, require90190: 0 };
  const require90187 = (id) => {
    if (id === MODULE_90190) {
      state.require90190 += 1;
      return core;
    }
    if (id === 11286) return { ce: memory };
    if (id === 46626) return allocator;
    if (id === MODULE_90188) {
      state.require90188 += 1;
      return releaseModule.load();
    }
    throw new Error('unexpected module90187 dependency ' + String(id));
  };
  const holder = evaluateFactoryMap(extract90187Source());
  const moduleObject = { exports: {} };
  holder[MODULE_90187].call(moduleObject.exports, moduleObject, moduleObject.exports, require90187);
  const api = moduleObject.exports;
  assert.equal(state.require90190, 1,
    'loading the 90187 facade must require the actual 90190 core exactly once');
  assert.equal(api.create, core.create,
    'the facade must expose the actual 90190 core create(), not a reconstructed core');
  assert.equal(api.attach, core.attach,
    'the facade must expose the actual 90190 core attach()');
  assert.equal(api.LAYOUT, core.LAYOUT,
    'the facade must expose the actual 90190 core layout');
  return { api, state, releaseModule, core };
}

function makeSharedMemory(words) {
  const pages = words === undefined ? 1 : words;
  return new WebAssembly.Memory({ initial: pages, maximum: pages, shared: true });
}

/* Test-fixture heap. The bump counter and the free log live in the same shared
 * memory as the AVBuffer metadata, so two realms stay disjoint and one shared
 * log records the exact free order. The allocator is NOT the real WASM
 * allocator and never claims pool/reallocation semantics. */
function createHarness(memory, options) {
  const settings = options || {};
  const words = new Int32Array(memory.buffer);
  const bytes = new Uint8Array(memory.buffer);
  const freed = [];
  let allocations = 0;
  let throwOnPointer = 0;
  let throwError = null;

  function load32(pointer) { return Atomics.load(words, pointer >>> 2); }
  function store32(pointer, value) { Atomics.store(words, pointer >>> 2, value | 0); }
  function load64(pointer) { return load32(pointer) >>> 0; }
  function store64(pointer, value) { store32(pointer, value); store32(pointer + 4, 0); }
  function copy(destination, source, length) {
    bytes.copyWithin(destination, source, source + length);
  }

  function allocate(size) {
    allocations += 1;
    const span = Math.max(8, (Number(size) + 7) & ~7);
    for (;;) {
      const current = Atomics.load(words, BUMP_WORD);
      if (current === 0) {
        Atomics.compareExchange(words, BUMP_WORD, 0, HEAP_BASE);
        continue;
      }
      const next = current + span;
      if (next > memory.buffer.byteLength) return 0;
      if (Atomics.compareExchange(words, BUMP_WORD, current, next) === current) {
        bytes.fill(0, current, next);
        return current;
      }
    }
  }

  function free(pointer) {
    if (!pointer) return;
    const index = Atomics.add(words, LOG_COUNT_WORD, 1);
    if (index < LOG_CAPACITY) Atomics.store(words, LOG_BASE_WORD + index, pointer);
    freed.push(pointer);
    if (typeof settings.onFree === 'function') settings.onFree(pointer);
    if (throwOnPointer !== 0 && throwOnPointer === pointer) {
      throwOnPointer = 0;
      throw throwError;
    }
  }

  function readFreeLog() {
    const count = Atomics.load(words, LOG_COUNT_WORD);
    const limit = Math.min(count, LOG_CAPACITY);
    const log = [];
    for (let index = 0; index < limit; index += 1) {
      log.push(Atomics.load(words, LOG_BASE_WORD + index));
    }
    return log;
  }

  function fieldMask(bits, shift) { return ((1 << bits) - 1) << shift; }
  function setField(pointer, value, bits, shift) {
    const mask = fieldMask(bits, shift);
    const index = pointer >>> 2;
    const next = (value << shift) & mask;
    for (;;) {
      const current = Atomics.load(words, index);
      if (Atomics.compareExchange(words, index, current, (current & ~mask) | next) === current) {
        return;
      }
    }
  }
  function addField(pointer, delta, bits, shift) {
    const mask = fieldMask(bits, shift);
    const index = pointer >>> 2;
    for (;;) {
      const current = Atomics.load(words, index);
      const value = ((current & mask) >>> shift) + delta;
      const next = (value << shift) & mask;
      if (Atomics.compareExchange(words, index, current, (current & ~mask) | next) === current) {
        return;
      }
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

  const allocator = {
    Gy: (size) => allocate(size),
    Eb: (pointer) => free(pointer),
    /* 46626.aI / u(e){free(mem[e]); mem[e]=0}: pointer-to-holder ABI. */
    aI: (pointer) => {
      const reference = load32(pointer);
      if (reference) free(reference);
      store32(pointer, 0);
    },
    sY: (size) => allocate(size),
    cz: (pointer, size) => allocate(size)
  };

  const poolCalls = [];
  const dependencies = {
    80209: { Mr: copy },
    56238: { f: { 15: load32, 20: load32, 25: load64 } },
    6927: { M: { 15: store32, 20: store32, 25: store64 } },
    46626: allocator,
    33817: { M_: setField, WQ: addField, jb: subField, Hh: getField },
    /* Fixture instrumented 62560: record the real s2/Tl/zr calls and the pool
     * link observed at each call so the test can prove the lock/unlock path
     * really ran instead of asserting that an unlock frees the lock. */
    62560: {
      s2(pointer) { poolCalls.push({ op: 's2', pointer, link: load32(pointer + 4) }); },
      Tl(pointer) { poolCalls.push({ op: 'Tl', pointer, link: load32(pointer + 4) }); },
      zr(pointer) { poolCalls.push({ op: 'zr', pointer, link: load32(pointer + 4) }); },
      Ts(pointer) { poolCalls.push({ op: 'Ts', pointer, link: load32(pointer + 4) }); }
    },
    90582: { z: (size) => allocate(size), t: (pointer) => free(pointer) }
  };

  return {
    memory,
    words,
    allocator,
    dependencies,
    freed,
    poolCalls,
    load32,
    store32,
    store64,
    load64,
    getField,
    setField,
    readFreeLog,
    freeCount(pointer) {
      return readFreeLog().filter((entry) => entry === pointer).length;
    },
    get allocations() { return allocations; },
    armPayloadThrow(pointer, error) {
      throwOnPointer = pointer;
      throwError = error;
    }
  };
}

/* Executes the ACTUAL vendor module 13486 factory with the ACTUAL module 90187
 * facade as its explicit dependency. Only low-level wasm/allocator plumbing is
 * a fixture (see createHarness). */
function load13486(harness, domainApi) {
  const holder = evaluateFactoryMap(extract13486Source());
  const exportsTarget = {};
  const require13486 = (id) => {
    if (id === MODULE_90187) return domainApi;
    if (!Object.prototype.hasOwnProperty.call(harness.dependencies, id)) {
      throw new Error('unexpected module 13486 dependency ' + String(id));
    }
    return harness.dependencies[id];
  };
  require13486.d = function define(target, definitions) {
    Object.keys(definitions).forEach((key) => {
      target[key] = definitions[key]();
    });
  };
  holder[MODULE_13486]({}, exportsTarget, require13486);
  for (const name of ['aw', 'VG', 'Q7', 'A6', 'D$', 'us']) {
    assert.equal(typeof exportsTarget[name], 'function',
      'module 13486 export ' + name + ' must be a function');
  }
  return exportsTarget;
}

/* TEST-ONLY error-path mock. It injects a throw from ONE observer method and
 * delegates everything else to the real 90187 facade. It is not real-domain
 * evidence and it must never grant credit. */
function observerErrorMock(api, method, error) {
  return new Proxy(api, {
    get(target, key) {
      if (key === method) {
        return function injectedObserverFailure() { throw error; };
      }
      return Reflect.get(target, key);
    }
  });
}

/* TEST-ONLY instrumentation wrapper: counts prepare calls, delegates anyway.
 * Used only to prove the pool branch never reaches the direct-free hooks. */
function countingObserver(api) {
  const state = { prepare: 0, commit: 0, failed: 0 };
  const wrapped = new Proxy(api, {
    get(target, key) {
      if (key === 'prepareDataFree') {
        return function countedPrepare(payload) {
          state.prepare += 1;
          return target.prepareDataFree(payload);
        };
      }
      if (key === 'commitDataFree') {
        return function countedCommit(opaque) {
          state.commit += 1;
          return target.commitDataFree(opaque);
        };
      }
      if (key === 'dataFreeFailed') {
        return function countedFailed(opaque) {
          state.failed += 1;
          return target.dataFreeFailed(opaque);
        };
      }
      return Reflect.get(target, key);
    }
  });
  return { api: wrapped, state };
}

function liveRecord(api) {
  const snapshot = api.getReleaseRegistrySnapshot();
  assert.equal(snapshot.domains.length, 1, 'exactly one registered domain is expected');
  return snapshot.domains[0].ownership;
}

/* ------------------------------------------------------------------ */
/* Source parity and explicit dependencies                              */
/* ------------------------------------------------------------------ */

test('90188 and the 90190 core embed the standalone helpers byte-for-byte; 90187 requires the actual core and release lazily',
  { timeout: 5000 }, () => {
    const graph = sourceGraph.text;
    assert.equal((graph.match(/90187:\(module,exports,__webpack_require__\)=>\{/g) || []).length, 1,
      'factory 90187 must appear exactly once with the required signature');
    assert.equal((graph.match(/90188:\(module,exports,__webpack_require__\)=>\{/g) || []).length, 1,
      'factory 90188 must appear exactly once with the required signature');
    assert.equal((graph.match(/90190:\(module,exports,__webpack_require__\)=>\{/g) || []).length, 1,
      'factory 90190 must appear exactly once with the required signature');
    assert.equal(graph.indexOf('90188:(e,t,i)=>'), -1,
      'factory 90188 must not reuse the three-argument webpack signature');
    assert.equal(graph.indexOf('90187:(e,t,i)=>'), -1,
      'factory 90187 must not reuse the three-argument webpack signature');
    assert.equal(graph.split(RELEASE_TEXT).length - 1, 1,
      'the standalone release helper must be embedded exactly once');
    assert.equal(graph.split(OWNERSHIP_TEXT).length - 1, 1,
      'the standalone ownership helper must be embedded exactly once');

    /* Module 90188 wrapper parity (chunk 90204): the exact standalone release
     * bytes start the body and only its sentinel plus wrapper whitespace may
     * follow them. */
    const source90188 = extract90188Source();
    const body90188 = source90188.slice(SIGNATURE_90188.length, source90188.length - 1);
    const releaseAt = body90188.indexOf(RELEASE_TEXT);
    assert.notEqual(releaseAt, -1,
      'module 90188 must contain the exact standalone release bytes');
    assert.equal(releaseAt, body90188.search(/\S/),
      'module 90188 body must begin with the exact standalone release bytes');
    assert.equal(body90188.slice(releaseAt + RELEASE_TEXT.length).trim(), END_90188,
      'module 90188 must end at its sentinel after the exact standalone release bytes');

    /* The physical split moved the exact standalone ownership UMD bytes from
     * 90187 into the actual core module 90190 (chunk 90201). Byte parity
     * therefore targets that core; only wrapper whitespace may surround the
     * exact standalone bytes. */
    const source90190 = extract90190Source();
    const body90190 = source90190.slice(SIGNATURE_90190.length, source90190.length - 1);
    const ownershipAt = body90190.indexOf(OWNERSHIP_TEXT);
    assert.notEqual(ownershipAt, -1,
      'module 90190 must contain the exact standalone ownership bytes');
    assert.equal(ownershipAt, body90190.search(/\S/),
      'module 90190 body must begin with the exact standalone ownership bytes');
    assert.equal(body90190.slice(ownershipAt + OWNERSHIP_TEXT.length).trim(), '',
      'nothing but wrapper whitespace may follow the ownership bytes in the core');
    assert.equal(source90190.indexOf('createDomainDescriptor'), -1,
      'the extracted core must not contain the 90187 domain facade');

    const core = loadActualCore();
    assert.deepEqual(Object.keys(core).sort(), Object.keys(OWNERSHIP).sort(),
      'the actual module 90190 core must expose exactly the standalone core API');
    assert.equal(core.VERSION, OWNERSHIP.VERSION);
    assert.equal(core.KIND, OWNERSHIP.KIND);
    assert.deepEqual(core.LAYOUT, OWNERSHIP.LAYOUT);

    /* Module 90187 (chunk 90202) is now a pure facade over the real core. */
    const source90187 = extract90187Source();
    const body90187 = source90187.slice(SIGNATURE_90187.length);
    assert.ok(body90187.replace(/^\s+/, '')
      .startsWith('const core=__webpack_require__(90190);'),
    'module 90187 must start the facade by requiring the actual extracted core');
    assert.equal(source90187.indexOf(OWNERSHIP_TEXT), -1,
      'the ownership core must no longer be embedded in the 90187 facade');
    const coreRequires = source90187.match(/__webpack_require__\(90190\)/g) || [];
    assert.equal(coreRequires.length, 1,
      'module 90187 must require the actual core module 90190 exactly once');
    const releaseRequires = source90187.match(/__webpack_require__\(90188\)/g) || [];
    assert.equal(releaseRequires.length, 1,
      'module 90187 must contain exactly one explicit webpack require of module 90188');
    assert.ok(source90187.indexOf('__webpack_require__(90188)') >
      source90187.indexOf('const core=__webpack_require__(90190);'),
    'the 90188 require must live in the facade extension, after the core require');
    assert.equal((source90188.match(/__webpack_require__\(/g) || []).length, 0,
      'module 90188 must not require anything');
    assert.equal((source90190.match(/__webpack_require__\(/g) || []).length, 0,
      'module 90190 must not require anything');
    for (const name of ['registerDomain', 'unregisterDomain', 'prepareDataFree',
      'commitDataFree', 'dataFreeFailed', 'getReleaseRegistrySnapshot']) {
      assert.notEqual(source90187.indexOf(name + '='), -1,
        'module 90187 must expose the release facade method ' + name);
    }
    assert.ok(source90187.includes('eioReleaseRegistry===null'),
      'module 90187 must guard every release method behind the lazy registry');

    const source13486 = extract13486Source();
    assert.ok(source13486.startsWith(SIGNATURE_13486),
      'module 13486 must keep its factory key and arguments');
    assert.ok(source13486.includes(',E=i(90187);'),
      'module 13486 must require module 90187 through a collision-free local binding');
    const prepareAt = source13486.indexOf('E.prepareDataFree({data:eioData,buffer:i})');
    const freeAt = source13486.indexOf('(0,o.Eb)(eioData)');
    const failedAt = source13486.indexOf('E.dataFreeFailed(eioOpaque)');
    const commitAt = source13486.indexOf('E.commitDataFree(eioOpaque)');
    const metadataAt = source13486.indexOf('e&&(0,o.Eb)(i)');
    assert.ok(prepareAt > 0, 'the payload free branch must prepare before freeing');
    assert.ok(prepareAt < freeAt, 'prepare must precede the original allocator free');
    assert.ok(failedAt > freeAt && failedAt < commitAt,
      'dataFreeFailed must only run after a throwing allocator free, before commit');
    assert.ok(commitAt < metadataAt,
      'commit must follow the payload free and precede the metadata free');
    assert.ok(source13486.includes('if(eioPool)v(eioPool,s.f[20](i));'),
      'the pool branch must keep the original v(pool, data) call and never prepare/commit');
    assert.equal(source13486.indexOf('importScripts'), -1,
      'module 13486 must not add a dynamic global/script loader');
    assert.equal(source13486.indexOf('new Function'), -1,
      'module 13486 must not add a dynamic loader');
    assert.equal(source13486.indexOf('globalThis'), -1,
      'module 13486 must not reach for a global domain registry');
    assert.ok(graph.includes(
      '13486:(e,t,i)=>{i.d(t,{A6:()=>A,D$:()=>u,Q7:()=>m,VG:()=>f,aw:()=>l,us:()=>g});'
    ), 'module 13486 must keep its byte-identical export header');
  });

/* ------------------------------------------------------------------ */
/* Real factories over a real shared WebAssembly.Memory                */
/* ------------------------------------------------------------------ */

test('actual 13486 + 90187 + 90190 + 90188: prepare precedes the payload free, held drops only after it, commit credits once',
  { timeout: 10000 }, () => {
    const memory = makeSharedMemory();
    let api = null;
    const atFree = { data: 0, meta: 0, held: -1, closing: -1, metaHeld: -1 };
    const harness = createHarness(memory, {
      onFree(pointer) {
        if (atFree.data !== 0 && pointer === atFree.data) {
          atFree.held = liveRecord(api).heldBytes;
          atFree.closing = liveRecord(api).countClosing;
        }
        if (atFree.meta !== 0 && pointer === atFree.meta) {
          /* The AVBuffer body free follows the committed payload free and is a
           * different operation; it must not add or remove media credit. */
          atFree.metaHeld = liveRecord(api).heldBytes;
        }
      }
    });

    const loader = load90187(memory, harness.allocator);
    api = loader.api;
    assert.equal(loader.state.require90188, 0,
      'loading the module 90187 facade must not require module 90188');
    const empty = api.getReleaseRegistrySnapshot();
    assert.equal(Object.isFrozen(empty), true, 'the pre-registration snapshot must be frozen');
    assert.deepEqual(empty, { registeredDomains: 0, faultCount: 0, lastFault: null, domains: [] });
    assert.equal(Object.isFrozen(empty.domains), true,
      'the pre-registration domain list must be frozen');
    assert.equal('complete' in empty, false, 'the snapshot must not claim a complete aggregate');

    const descriptor = api.createDomainDescriptor({
      capacityBytes: 4096,
      maxEntries: 8,
      domainId: 'direct-main'
    });
    assert.equal(loader.state.require90188, 0,
      'creating a domain descriptor must not create the release registry');
    const controller = api.registerDomain(descriptor, 0);
    assert.equal(loader.state.require90188, 1,
      'the first registerDomain must require the actual module 90188 exactly once');
    assert.equal(loader.releaseModule.evaluations, 1,
      'module 90188 must be evaluated exactly once');
    assert.equal(api.getReleaseRegistrySnapshot().registeredDomains, 1);

    const vendor = load13486(harness, api);
    const data = harness.allocator.Gy(256);
    assert.ok(data >= HEAP_BASE, 'the fixture payload must live above the shared metadata words');
    const primary = vendor.aw(data, 256);
    assert.ok(primary > 0, 'aw must build a real AVBuffer ref in the fixture heap');
    const meta = harness.load32(primary);
    assert.ok(meta > 0 && meta !== data, 'aw must allocate a separate AVBuffer body');
    assert.equal(harness.load32(primary + 4), data, 'the ref must point at the payload');
    assert.equal(harness.getField(meta + 8, 9, 2), 1, 'aw must start with one reference');
    assert.equal(harness.load32(meta + 16), 0,
      'the direct branch requires a zero non-pool pointer in the AVBuffer body');
    atFree.data = data;
    atFree.meta = meta;

    const second = vendor.VG(primary);
    assert.ok(second > 0);
    assert.equal(harness.getField(meta + 8, 9, 2), 2, 'VG must add exactly one reference');

    const reservation = controller.reserve(256, 11);
    assert.equal(reservation.ok, true);
    assert.equal(controller.bind(reservation.token, { data, buffer: meta }).ok, true);
    assert.equal(liveRecord(api).heldBytes, 256, 'binding charges the payload once');
    assert.equal(liveRecord(api).countBound, 1);

    const firstHolder = harness.allocator.Gy(4);
    harness.store32(firstHolder, primary);
    vendor.Q7(firstHolder);
    assert.equal(harness.load32(firstHolder), 0, 'the first Q7 must clear its holder word');
    assert.equal(harness.getField(meta + 8, 9, 2), 1);
    assert.equal(harness.freeCount(primary), 1, 'the first Q7 frees only its AVBufferRef');
    assert.equal(harness.freeCount(data), 0, 'a mere ref drop must not free the payload');
    assert.equal(harness.freeCount(meta), 0, 'a mere ref drop must not free the AVBuffer body');
    assert.equal(liveRecord(api).heldBytes, 256, 'a mere ref drop must not credit held bytes');
    assert.equal(liveRecord(api).countClosing, 0);
    assert.equal(controller.lookup({ data, buffer: meta }).state, 'bound');

    const secondHolder = harness.allocator.Gy(4);
    harness.store32(secondHolder, second);
    vendor.Q7(secondHolder);
    assert.equal(harness.load32(secondHolder), 0, 'the last Q7 must clear its holder word');
    assert.equal(harness.getField(meta + 8, 9, 2), 0);

    assert.equal(atFree.held, 256,
      'the held charge must still be present while the original allocator runs');
    assert.equal(atFree.closing, 1,
      'the record must already be CLOSING when the original allocator runs (prepare first)');
    assert.equal(harness.freeCount(data), 1, 'the payload must be freed exactly once');
    assert.equal(harness.freeCount(meta), 1, 'the AVBuffer body must still be freed once');
    assert.equal(atFree.metaHeld, 0,
      'the AVBuffer metadata free must not add or remove media byte credit');
    assert.deepEqual(harness.readFreeLog(), [primary, second, data, meta],
      'free order must be ref, ref, payload, then AVBuffer metadata');

    const after = api.getReleaseRegistrySnapshot();
    assert.equal(after.faultCount, 0, 'the successful direct free must not record a fault');
    const ownership = after.domains[0].ownership;
    assert.equal(ownership.heldBytes, 0, 'commit must credit the payload exactly once');
    assert.equal(ownership.peakBytes, 256);
    assert.equal(ownership.countBound, 0);
    assert.equal(ownership.countClosing, 0);
    assert.equal(controller.lookup({ data, buffer: meta }).found, false);
    assert.equal(api.prepareDataFree({ data, buffer: meta }), null,
      'a key without a live record is an ordinary untracked data free');
    assert.equal(api.commitDataFree(null).tracked, false);
    assert.equal(api.dataFreeFailed(undefined).tracked, false);
    assert.equal(api.getReleaseRegistrySnapshot().faultCount, 0,
      'null untracked tickets must not be accounted as faults');
  });

test('a throwing original allocator is rethrown unchanged: CLOSING/held retained, allocator fault visible, no retry',
  { timeout: 10000 }, () => {
    const memory = makeSharedMemory();
    const harness = createHarness(memory);
    const loader = load90187(memory, harness.allocator);
    const api = loader.api;
    const descriptor = api.createDomainDescriptor({
      capacityBytes: 4096,
      maxEntries: 8,
      domainId: 'direct-throw'
    });
    const controller = api.registerDomain(descriptor, 0);
    const vendor = load13486(harness, api);

    const data = harness.allocator.Gy(256);
    const primary = vendor.aw(data, 256);
    const meta = harness.load32(primary);
    const second = vendor.VG(primary);
    const reservation = controller.reserve(256, 12);
    assert.equal(reservation.ok, true);
    assert.equal(controller.bind(reservation.token, { data, buffer: meta }).ok, true);

    const firstHolder = harness.allocator.Gy(4);
    harness.store32(firstHolder, primary);
    vendor.Q7(firstHolder);
    assert.equal(harness.freeCount(data), 0);

    const injected = new Error('fixture allocator threw on the payload free');
    harness.armPayloadThrow(data, injected);
    const secondHolder = harness.allocator.Gy(4);
    harness.store32(secondHolder, second);
    let thrown = null;
    try {
      vendor.Q7(secondHolder);
    } catch (error) {
      thrown = error;
    }
    assert.equal(thrown, injected, 'the ORIGINAL allocator exception must be rethrown unchanged');
    assert.equal(harness.load32(secondHolder), 0, 'the holder still drops its ref');
    assert.equal(harness.freeCount(data), 1, 'the payload free is attempted exactly once');
    assert.equal(harness.freeCount(meta), 0,
      'metadata must not be freed after the original allocator threw');

    const snapshot = api.getReleaseRegistrySnapshot();
    const ownership = snapshot.domains[0].ownership;
    assert.equal(ownership.heldBytes, 256, 'the CLOSING/held charge must be retained');
    assert.equal(ownership.countClosing, 1);
    assert.equal(ownership.countBound, 1,
      'the verified core keeps COUNT_BOUND until commit releases the slot; prepare only adds COUNT_CLOSING');
    assert.equal(snapshot.domains[0].faultCount, 1,
      'the uniquely known domain must carry the allocator-failure fault');
    assert.equal(snapshot.domains[0].lastFault, 'EIO_RELEASE_ALLOCATOR_FAILED');
    assert.equal(snapshot.faultCount, 0,
      'a uniquely known domain fault must not be copied into the global fault log');

    vendor.Q7(secondHolder);
    assert.equal(harness.freeCount(data), 1, 'a failed payload free must never be retried');
    assert.equal(harness.freeCount(meta), 0);

    /* A forged/cloned ticket is refused without credit; the retained charge
     * cannot be cleared by calling the hook by hand. */
    const forged = Object.freeze({});
    const refused = api.commitDataFree(forged);
    assert.equal(refused.ok, false);
    assert.equal(refused.code, 'EIO_RELEASE_TICKET_INVALID');
    assert.equal(liveRecord(api).heldBytes, 256,
      'an invalid ticket must not manufacture a held-byte credit');
    assert.equal(harness.freeCount(data), 1);
  });

test('injected observer wrappers (error-path mocks) cannot block the original payload free',
  { timeout: 10000 }, () => {
    /* (a) prepare observer throws: the allocator free must still run, but the
     * thrown observer must not grant any credit. */
    const memoryA = makeSharedMemory();
    const harnessA = createHarness(memoryA);
    const loaderA = load90187(memoryA, harnessA.allocator);
    const apiA = loaderA.api;
    const descriptorA = apiA.createDomainDescriptor({
      capacityBytes: 4096,
      maxEntries: 8,
      domainId: 'direct-observer-prepare'
    });
    const controllerA = apiA.registerDomain(descriptorA, 0);
    const prepareError = new Error('injected prepare observer failure');
    const vendorA = load13486(harnessA,
      observerErrorMock(apiA, 'prepareDataFree', prepareError));
    const dataA = harnessA.allocator.Gy(128);
    const primaryA = vendorA.aw(dataA, 128);
    const metaA = harnessA.load32(primaryA);
    const reservationA = controllerA.reserve(128, 13);
    assert.equal(reservationA.ok, true);
    assert.equal(controllerA.bind(reservationA.token, { data: dataA, buffer: metaA }).ok, true);
    const holderA = harnessA.allocator.Gy(4);
    harnessA.store32(holderA, primaryA);
    vendorA.Q7(holderA);
    assert.equal(harnessA.load32(holderA), 0);
    assert.equal(harnessA.freeCount(dataA), 1,
      'an injected prepare-observer throw must not block the original payload free');
    assert.equal(harnessA.freeCount(metaA), 1);
    assert.equal(liveRecord(apiA).heldBytes, 128,
      'an error-path mock prepare must not authorize a held-byte credit');
    assert.equal(liveRecord(apiA).countClosing, 0);

    /* (b) commit observer throws AFTER the real prepare: the payload free still
     * happens, metadata cleanup still happens, but no credit is granted. */
    const memoryB = makeSharedMemory();
    const harnessB = createHarness(memoryB);
    const loaderB = load90187(memoryB, harnessB.allocator);
    const apiB = loaderB.api;
    const descriptorB = apiB.createDomainDescriptor({
      capacityBytes: 4096,
      maxEntries: 8,
      domainId: 'direct-observer-commit'
    });
    const controllerB = apiB.registerDomain(descriptorB, 0);
    const commitError = new Error('injected commit observer failure');
    const vendorB = load13486(harnessB,
      observerErrorMock(apiB, 'commitDataFree', commitError));
    const dataB = harnessB.allocator.Gy(128);
    const primaryB = vendorB.aw(dataB, 128);
    const metaB = harnessB.load32(primaryB);
    const secondB = vendorB.VG(primaryB);
    const reservationB = controllerB.reserve(128, 14);
    assert.equal(reservationB.ok, true);
    assert.equal(controllerB.bind(reservationB.token, { data: dataB, buffer: metaB }).ok, true);
    const firstHolderB = harnessB.allocator.Gy(4);
    harnessB.store32(firstHolderB, primaryB);
    vendorB.Q7(firstHolderB);
    const secondHolderB = harnessB.allocator.Gy(4);
    harnessB.store32(secondHolderB, secondB);
    vendorB.Q7(secondHolderB);
    assert.equal(harnessB.load32(secondHolderB), 0);
    assert.equal(harnessB.freeCount(dataB), 1,
      'an injected commit-observer throw must not block the original payload free');
    assert.equal(harnessB.freeCount(metaB), 1,
      'metadata cleanup must still happen after an injected commit-observer throw');
    assert.equal(liveRecord(apiB).heldBytes, 128,
      'an error-path mock commit must not authorize a held-byte credit');
    assert.equal(liveRecord(apiB).countClosing, 1,
      'the real prepare already moved the record to CLOSING');
  });

test('pool return keeps the data charge and never calls the direct-free hooks',
  { timeout: 10000 }, () => {
    const memory = makeSharedMemory();
    const harness = createHarness(memory);
    const loader = load90187(memory, harness.allocator);
    const api = loader.api;
    const descriptor = api.createDomainDescriptor({
      capacityBytes: 4096,
      maxEntries: 8,
      domainId: 'direct-pool'
    });
    const controller = api.registerDomain(descriptor, 0);
    const observer = countingObserver(api);
    const vendor = load13486(harness, observer.api);

    const data = harness.allocator.Gy(128);
    const primary = vendor.aw(data, 128);
    const meta = harness.load32(primary);
    const poolEntry = harness.allocator.Gy(32);
    const poolLock = harness.allocator.Gy(16);
    harness.store32(meta + 16, poolEntry);
    harness.store32(poolEntry + 12, poolLock);
    /* Live pool lock refcount (verified core packs 9 bits at shift 2). The
     * unref 2 -> 1 keeps the pool alive; only old 1 triggers the real pool
     * destruction, so a live return only unlocks and never frees the lock. */
    harness.setField(poolLock + 8, 2, 9, 2);
    const reservation = controller.reserve(128, 15);
    assert.equal(reservation.ok, true);
    assert.equal(controller.bind(reservation.token, { data, buffer: meta }).ok, true);

    const holder = harness.allocator.Gy(4);
    harness.store32(holder, primary);
    vendor.Q7(holder);
    assert.equal(harness.load32(holder), 0);
    assert.equal(harness.getField(meta + 8, 9, 2), 0, 'the pool return still drops the last ref');
    assert.equal(harness.getField(poolLock + 8, 9, 2), 1,
      'the live pool lock must be unrefed 2 -> 1');
    assert.equal(harness.freeCount(data), 0,
      'returning the payload to a live pool must not free the data pointer');
    assert.equal(harness.freeCount(meta), 1, 'the AVBuffer body is still released');
    assert.equal(harness.freeCount(poolLock), 0,
      'a live pool return only unlocks; it must not free the pool lock');
    assert.equal(harness.freeCount(poolEntry), 0,
      'a live pool return must not free the linked pool entry');
    assert.deepEqual(harness.poolCalls.map((call) => call.op), ['s2', 'Tl'],
      'the original pool lock/unlock calls must really run');
    assert.equal(harness.poolCalls[0].pointer, poolLock);
    assert.equal(harness.poolCalls[1].pointer, poolLock);
    assert.equal(harness.poolCalls[0].link, 0,
      'the lock link is still empty when s2 runs');
    assert.equal(harness.poolCalls[1].link, poolEntry,
      'the pool linked-list entry is attached before Tl runs');
    assert.equal(harness.load32(poolLock + 4), poolEntry,
      'the linked pool entry stays reachable in the live pool');
    assert.equal(observer.state.prepare, 0,
      'the non-pool direct-free prepare hook must never run for a pool return');
    assert.equal(observer.state.commit, 0);
    assert.equal(observer.state.failed, 0);
    const ownership = liveRecord(api);
    assert.equal(ownership.heldBytes, 128, 'the pool return keeps the data charge');
    assert.equal(ownership.countBound, 1);
    assert.equal(ownership.countClosing, 0);
    assert.equal(api.getReleaseRegistrySnapshot().faultCount, 0);
    assert.equal(controller.lookup({ data, buffer: meta }).state, 'bound');
  });

/* ------------------------------------------------------------------ */
/* Two realms over one physical table (hand-assembled Workers)         */
/* ------------------------------------------------------------------ */

const WORKER_SOURCE = String.raw`
'use strict';
const { parentPort, workerData } = require('node:worker_threads');

const HEAP_BASE = workerData.heapBase;
const BUMP_WORD = workerData.bumpWord;
const LOG_COUNT_WORD = workerData.logCountWord;
const LOG_BASE_WORD = workerData.logBaseWord;
const LOG_CAPACITY = workerData.logCapacity;

const memory = workerData.descriptor.wasmMemory;
const words = new Int32Array(memory.buffer);
const bytes = new Uint8Array(memory.buffer);

function load32(pointer) { return Atomics.load(words, pointer >>> 2); }
function store32(pointer, value) { Atomics.store(words, pointer >>> 2, value | 0); }
function load64(pointer) { return load32(pointer) >>> 0; }
function store64(pointer, value) { store32(pointer, value); store32(pointer + 4, 0); }
function copy(destination, source, length) {
  bytes.copyWithin(destination, source, source + length);
}

function allocate(size) {
  const span = Math.max(8, (Number(size) + 7) & ~7);
  for (;;) {
    const current = Atomics.load(words, BUMP_WORD);
    if (current === 0) {
      Atomics.compareExchange(words, BUMP_WORD, 0, HEAP_BASE);
      continue;
    }
    const next = current + span;
    if (next > memory.buffer.byteLength) return 0;
    if (Atomics.compareExchange(words, BUMP_WORD, current, next) === current) {
      bytes.fill(0, current, next);
      return current;
    }
  }
}

function free(pointer) {
  if (!pointer) return;
  const index = Atomics.add(words, LOG_COUNT_WORD, 1);
  if (index < LOG_CAPACITY) Atomics.store(words, LOG_BASE_WORD + index, pointer);
}

function readFreeLog() {
  const count = Atomics.load(words, LOG_COUNT_WORD);
  const limit = Math.min(count, LOG_CAPACITY);
  const log = [];
  for (let index = 0; index < limit; index += 1) {
    log.push(Atomics.load(words, LOG_BASE_WORD + index));
  }
  return log;
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

const allocator = {
  Gy: (size) => allocate(size),
  Eb: (pointer) => free(pointer),
  aI: (pointer) => {
    const reference = load32(pointer);
    if (reference) free(reference);
    store32(pointer, 0);
  },
  sY: (size) => allocate(size),
  cz: (pointer, size) => allocate(size)
};

function evaluateFactoryMap(source) {
  return new Function('return ({' + source + '});')();
}

let releaseExports = null;
let require90188Count = 0;
function require90188() {
  require90188Count += 1;
  if (releaseExports === null) {
    const holder = evaluateFactoryMap(workerData.module90188);
    const moduleObject = { exports: {} };
    holder[90188](moduleObject, moduleObject.exports, function (id) {
      throw new Error('module 90188 must not require ' + String(id));
    });
    releaseExports = moduleObject.exports;
  }
  return releaseExports;
}

/* The worker evaluates the ACTUAL module 90190 core from chunk 90201 and hands
 * that same instance to the ACTUAL 90187 facade through its require. */
function loadWorkerCore() {
  const holder = evaluateFactoryMap(workerData.module90190);
  const moduleObject = { exports: {} };
  holder[90190].call(moduleObject.exports, moduleObject, moduleObject.exports, function (id) {
    throw new Error('unexpected module90190 dependency ' + String(id));
  });
  return moduleObject.exports;
}
const coreApi = loadWorkerCore();

const require90187 = function (id) {
  if (id === 90190) return coreApi;
  if (id === 11286) return { ce: memory };
  if (id === 46626) return allocator;
  if (id === 90188) return require90188();
  throw new Error('unexpected module90187 dependency ' + String(id));
};
const module90187 = { exports: {} };
evaluateFactoryMap(workerData.module90187)[90187]
  .call(module90187.exports, module90187, module90187.exports, require90187);
const api = module90187.exports;

const dependencies = {
  80209: { Mr: copy },
  56238: { f: { 15: load32, 20: load32, 25: load64 } },
  6927: { M: { 15: store32, 20: store32, 25: store64 } },
  46626: allocator,
  33817: { M_: setField, WQ: addField, jb: subField, Hh: getField },
  62560: { s2() {}, Tl() {}, zr() {}, Ts() {} },
  90582: { z: (size) => allocate(size), t: (pointer) => free(pointer) },
  90187: api
};
const require13486 = function (id) {
  if (!Object.prototype.hasOwnProperty.call(dependencies, id)) {
    throw new Error('unexpected module 13486 dependency ' + String(id));
  }
  return dependencies[id];
};
require13486.d = function (target, definitions) {
  Object.keys(definitions).forEach((key) => { target[key] = definitions[key](); });
};
const module13486Exports = {};
evaluateFactoryMap(workerData.module13486)[13486]({}, module13486Exports, require13486);

const descriptor = workerData.descriptor;

function handle(message) {
  switch (message.op) {
    case 'state':
      return {
        require90188: require90188Count,
        snapshot: api.getReleaseRegistrySnapshot()
      };
    case 'register': {
      api.registerDomain(descriptor, 1);
      return {
        require90188: require90188Count,
        snapshot: api.getReleaseRegistrySnapshot()
      };
    }
    case 'q7': {
      const holder = allocator.Gy(4);
      store32(holder, message.ref);
      module13486Exports.Q7(holder);
      const log = readFreeLog();
      return {
        holder,
        holderValue: load32(holder),
        snapshot: api.getReleaseRegistrySnapshot(),
        freeLog: log,
        payloadFrees: log.filter((entry) => entry === message.data).length,
        metadataFrees: log.filter((entry) => entry === message.meta).length
      };
    }
    case 'snapshot':
      return { snapshot: api.getReleaseRegistrySnapshot() };
    default:
      throw new Error('unknown worker op ' + String(message.op));
  }
}

parentPort.on('message', function (message) {
  Promise.resolve()
    .then(function () { return handle(message); })
    .then(
      function (value) { parentPort.postMessage({ id: message.id, ok: true, value: value }); },
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
  const limit = timeoutMs === undefined ? 5000 : timeoutMs;
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
    const coded = (code, message) => {
      const error = new Error(message);
      error.code = code;
      return error;
    };
    const onMessage = (message) => {
      if (!message || message.id !== id) return;
      if (message.ok) {
        finish(resolve, message.value);
      } else {
        const info = message.error || {};
        finish(reject, coded(info.code || 'worker-error', info.message || 'worker error'));
      }
    };
    const onError = (error) => finish(reject, error);
    const onExit = (code) => finish(reject,
      coded('worker-exit', 'worker exited with ' + String(code) + ' while handling ' + op));
    timer = setTimeout(() => finish(reject,
      coded('test-timeout', 'timed out waiting for worker op ' + op)), limit);
    worker.on('message', onMessage);
    worker.on('error', onError);
    worker.on('exit', onExit);
    worker.postMessage(Object.assign({ id, op }, payload || {}));
  });
}

test('two realms over the same physical table: first real Q7 retains, last real Q7 (worker) releases and commits',
  { timeout: 20000 }, async () => {
    const memory = makeSharedMemory();
    const harness = createHarness(memory);
    const loader = load90187(memory, harness.allocator);
    const api = loader.api;
    const descriptor = api.createDomainDescriptor({
      capacityBytes: 8192,
      maxEntries: 8,
      domainId: 'direct-cross-realm'
    });
    const controller = api.registerDomain(descriptor, 0);
    const vendor = load13486(harness, api);

    const data = harness.allocator.Gy(256);
    const primary = vendor.aw(data, 256);
    const meta = harness.load32(primary);
    const second = vendor.VG(primary);
    const reservation = controller.reserve(256, 16);
    assert.equal(reservation.ok, true);
    assert.equal(controller.bind(reservation.token, { data, buffer: meta }).ok, true);
    assert.equal(liveRecord(api).heldBytes, 256);

    const workerData = {
      descriptor: structuredClone(descriptor),
      module90187: extract90187Source(),
      module90188: extract90188Source(),
      module90190: extract90190Source(),
      module13486: extract13486Source(),
      heapBase: HEAP_BASE,
      bumpWord: BUMP_WORD,
      logCountWord: LOG_COUNT_WORD,
      logBaseWord: LOG_BASE_WORD,
      logCapacity: LOG_CAPACITY
    };
    const worker = new Worker(WORKER_SOURCE, { eval: true, workerData });
    try {
      const beforeRegister = await requestWorker(worker, 'state');
      assert.equal(beforeRegister.require90188, 0,
        'the worker 90187 facade must not require 90188 before an explicit register');
      assert.equal(beforeRegister.snapshot.registeredDomains, 0);

      const registered = await requestWorker(worker, 'register');
      assert.equal(registered.require90188, 1,
        'the worker first registerDomain must require the actual module 90188');
      assert.equal(registered.snapshot.registeredDomains, 1);
      assert.equal(registered.snapshot.domains[0].ownership.heldBytes, 256,
        'the worker must see the shared held charge through the cloned descriptor');

      /* First real Q7 in this realm: only the AVBufferRef is dropped. */
      const mainHolder = harness.allocator.Gy(4);
      harness.store32(mainHolder, primary);
      vendor.Q7(mainHolder);
      assert.equal(harness.load32(mainHolder), 0);
      assert.equal(harness.freeCount(data), 0);
      assert.equal(harness.freeCount(meta), 0);
      assert.equal(liveRecord(api).heldBytes, 256,
        'the first Q7 must keep the charge in the shared table');
      const workerBeforeLast = await requestWorker(worker, 'snapshot');
      assert.equal(workerBeforeLast.snapshot.domains[0].ownership.heldBytes, 256);

      /* Last real Q7 inside the worker realm: the worker's own registry
       * prepares before the worker allocator free and commits after it. */
      const released = await requestWorker(worker, 'q7', {
        ref: second,
        data,
        meta
      });
      assert.equal(released.holderValue, 0, 'the worker holder word must be cleared');
      assert.equal(released.payloadFrees, 1, 'the payload must be freed exactly once');
      assert.equal(released.metadataFrees, 1, 'the AVBuffer body must be freed exactly once');
      assert.deepEqual(released.freeLog, [primary, second, data, meta],
        'the shared free log must show ref drop, ref drop, payload, metadata');
      const workerOwnership = released.snapshot.domains[0].ownership;
      assert.equal(workerOwnership.heldBytes, 0,
        'the worker commit must credit the shared table');
      assert.equal(workerOwnership.countBound, 0);
      assert.equal(workerOwnership.countClosing, 0);
      assert.equal(released.snapshot.faultCount, 0);

      /* The owner realm must observe the worker's real free and commit. */
      const after = api.getReleaseRegistrySnapshot();
      assert.equal(after.domains[0].ownership.heldBytes, 0);
      assert.equal(after.domains[0].ownership.peakBytes, 256);
      assert.equal(after.faultCount, 0);
      assert.equal(controller.lookup({ data, buffer: meta }).found, false);
      assert.equal(harness.freeCount(data), 1);
      assert.equal(harness.freeCount(meta), 1);
      assert.deepEqual(harness.readFreeLog(), [primary, second, data, meta],
        'the owner realm must see the worker free order through the shared log');
    } finally {
      await worker.terminate().catch(() => {});
    }
  });

/* ------------------------------------------------------------------ */
/* No opt-in, no SharedArrayBuffer, no crypto                          */
/* ------------------------------------------------------------------ */

test('no-opt-in module 90187 + real 13486 works with SAB/crypto absent: no 90188 require, no registry, no allocation',
  { timeout: 10000 }, () => {
    const sharedDescriptor = Object.getOwnPropertyDescriptor(globalThis, 'SharedArrayBuffer');
    const cryptoDescriptor = Object.getOwnPropertyDescriptor(globalThis, 'crypto');
    const plainMemory = { buffer: new ArrayBuffer(1 << 18) };
    const harness = createHarness(plainMemory);
    try {
      Object.defineProperty(globalThis, 'SharedArrayBuffer', {
        value: undefined, configurable: true, writable: true
      });
      Object.defineProperty(globalThis, 'crypto', {
        value: undefined, configurable: true, writable: true
      });
      assert.equal(typeof SharedArrayBuffer, 'undefined');
      assert.equal(typeof globalThis.crypto, 'undefined');

      const allocationsBefore = harness.allocations;
      const loader = load90187(plainMemory, harness.allocator);
      const api = loader.api;
      assert.equal(loader.state.require90188, 0,
        'requiring module 90187 must not require module 90188');
      assert.equal(harness.allocations, allocationsBefore,
        'requiring module 90187 must not allocate');

      assert.deepEqual(api.unregisterDomain('missing'),
        { ok: false, code: 'EIO_DOMAIN_UNKNOWN' });
      assert.equal(api.prepareDataFree({ data: 0x100, buffer: 0x200 }), null);
      assert.deepEqual(api.commitDataFree(null), { ok: true, tracked: false });
      assert.deepEqual(api.commitDataFree(undefined), { ok: true, tracked: false });
      assert.deepEqual(api.dataFreeFailed(null), { ok: true, tracked: false });
      assert.deepEqual(api.commitDataFree({ forged: true }),
        { ok: false, code: 'EIO_RELEASE_TICKET_INVALID' });
      assert.deepEqual(api.dataFreeFailed({ forged: true }),
        { ok: false, code: 'EIO_RELEASE_TICKET_INVALID' });
      const snapshot = api.getReleaseRegistrySnapshot();
      assert.equal(Object.isFrozen(snapshot), true);
      assert.deepEqual(snapshot, { registeredDomains: 0, faultCount: 0, lastFault: null, domains: [] });
      assert.equal(loader.state.require90188, 0,
        'benign unregistered release calls must not create the registry');
      assert.equal(harness.allocations, allocationsBefore,
        'benign unregistered release calls must not allocate');

      /* The real non-pool last-ref branch must keep its original free
       * semantics and never reach a registry/hook credit here. */
      const vendor = load13486(harness, api);
      assert.equal(loader.state.require90188, 0);
      const data = harness.allocator.Gy(128);
      const primary = vendor.aw(data, 128);
      const meta = harness.load32(primary);
      assert.equal(harness.getField(meta + 8, 9, 2), 1);
      const holder = harness.allocator.Gy(4);
      harness.store32(holder, primary);
      vendor.Q7(holder);
      assert.equal(harness.load32(holder), 0);
      assert.equal(harness.getField(meta + 8, 9, 2), 0);
      assert.equal(harness.freeCount(data), 1);
      assert.equal(harness.freeCount(meta), 1);
      assert.deepEqual(harness.readFreeLog(), [primary, data, meta],
        'without a registered domain the real last-free path keeps the original order');
      assert.equal(loader.state.require90188, 0,
        'the no-opt-in last-free path must not require module 90188');
      assert.equal(loader.releaseModule.evaluations, 0);
      assert.deepEqual(api.getReleaseRegistrySnapshot(),
        { registeredDomains: 0, faultCount: 0, lastFault: null, domains: [] });
    } finally {
      if (sharedDescriptor) Object.defineProperty(globalThis, 'SharedArrayBuffer', sharedDescriptor);
      else delete globalThis.SharedArrayBuffer;
      if (cryptoDescriptor) Object.defineProperty(globalThis, 'crypto', cryptoDescriptor);
      else delete globalThis.crypto;
    }
  });
