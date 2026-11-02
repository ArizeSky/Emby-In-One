'use strict';

/*
 * Regression coverage for the MSE shared-media snapshot diagnostic split.
 *
 * The actual module 26 chunk is registered in a VM, the actual registered
 * 90200 planner factory is invoked with a fresh factory module object, and the
 * actual snapshot/workset method text is evaluated with the real planner import
 * bindings. The planner body is never inlined into module 26: doing that would
 * create a local EIO_PLAN_ASSUMPTIONS and hide a missing import or export.
 * The planner's three actual non-planner dependencies (54568, 46564, 88560) are
 * opaque fail-closed UNEXECUTED proxies. They throw on any property access, so
 * no fake helper behavior can pass a mux operation. Only those three require
 * calls are allowed; there are no core or facade stubs.
 *
 * This is export/metadata proof only. It is not runtime, chunk-load, Worker,
 * MSE, native media, upstream or production proof, it does not execute the
 * planner's mux helpers, and it makes no claim that the method is async. A
 * separate actual-Worker check is verified by the parent harness after the
 * two-input leases 44 B fix.
 */

const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const v8 = require('node:v8');

const LIBMEDIA = path.join(__dirname, '../public/vendor/web/libmedia');
const MSE_FILE = path.join(LIBMEDIA, '26.avplayer.js');
const PLANNER_FILE = path.join(LIBMEDIA, 'mse-mux-planner.js');
const MSE_SOURCE = fs.readFileSync(MSE_FILE, 'utf8');
const PLANNER_SOURCE = fs.readFileSync(PLANNER_FILE, 'utf8');

const NAMED_CHUNK_FILES = Object.freeze([
  'mse-mux-planner.js',
  'shared-media-ownership.js',
  'shared-media-domain.js',
  'owned-media-resources.js',
  'shared-media-release-registry.js',
  'codecpar-leases.js'
]);

const SNAPSHOT_METHOD_HEADS = Object.freeze([
  'getEioSharedMediaSnapshot(t){',
  'eioOwnedSnapshot(t){',
  'eioStreamSnapshot(t){',
  'eioWorksetSnapshot(t){'
]);

const SNAPSHOT_LOCAL_CONSTANTS = Object.freeze([
  'EIO_MSE_FIXED_WINDOW',
  'EIO_MSE_PLAN_LABEL',
  'EIO_MSE_INCOMPLETE',
  'EIO_MSE_INCOMPLETE_REASONS'
]);

const PLANNER_MODULE_ID = 90200;
const UNEXECUTED_PLANNER_DEPENDENCIES = Object.freeze([54568, 46564, 88560]);

let sharedRuntime = null;

/* Same real registration pattern used by web-libmedia-chunk-loading.test.cjs:
 * push the shipped chunk factories into the real webpackChunkAVPlayer queue,
 * then expose the actual webpack runtime at the real entry boundary. The module
 * 26 chunk is included because it also registers module 88560, which the real
 * 90200 planner factory requires; module 26 itself is not executed here. */
function registeredRuntime() {
  const context = vm.createContext({
    self: {},
    console,
    document: {
      currentScript: { tagName: 'SCRIPT', src: 'http://127.0.0.1/vendor/avplayer.js' },
      getElementsByTagName: () => []
    },
    TextEncoder: globalThis.TextEncoder,
    TextDecoder: globalThis.TextDecoder,
    URL: globalThis.URL,
    Blob: globalThis.Blob,
    crypto: globalThis.crypto,
    navigator: { userAgent: '', hardwareConcurrency: 1 }
  });
  for (const file of NAMED_CHUNK_FILES) {
    vm.runInContext(fs.readFileSync(path.join(LIBMEDIA, file), 'utf8'), context);
  }
  vm.runInContext(MSE_SOURCE, context);
  const source = fs.readFileSync(path.join(LIBMEDIA, 'avplayer.js'), 'utf8');
  const entry = 'var a=s(52989);return a.default';
  assert.equal(source.split(entry).length, 2, 'exact real entry boundary');
  vm.runInContext(source.replace(entry, 'return {require:s,modules:s.m}'), context);
  const runtime = context.self.AVPlayer;
  assert.equal(typeof runtime.require, 'function', 'actual webpack runtime is exposed');
  return runtime;
}

function actualRuntime() {
  if (sharedRuntime === null) sharedRuntime = registeredRuntime();
  return sharedRuntime;
}

function actualPlannerImportNames() {
  const marker = '/* Separate pure planner module;';
  const markerStart = MSE_SOURCE.indexOf(marker);
  assert.ok(markerStart >= 0, 'actual planner import marker');
  const declarationStart = MSE_SOURCE.indexOf('const {', markerStart);
  assert.ok(declarationStart >= 0, 'actual planner destructuring');
  const declarationEnd = MSE_SOURCE.indexOf('}=i(90200);', declarationStart);
  assert.ok(declarationEnd > declarationStart, 'actual planner import terminator');
  const body = MSE_SOURCE.slice(declarationStart + 'const {'.length, declarationEnd);
  const names = body.split(',').map((name) => name.trim()).filter(Boolean);
  for (const name of names) {
    assert.match(name, /^[A-Za-z_$][A-Za-z0-9_$]*$/, 'planner import is a bare binding: ' + name);
  }
  return names;
}

function actualPlannerExportNames() {
  const marker = 'module.exports={';
  const start = PLANNER_SOURCE.indexOf(marker);
  assert.ok(start >= 0, 'actual planner export map');
  const end = PLANNER_SOURCE.indexOf('};', start);
  assert.ok(end > start, 'actual planner export map terminator');
  const body = PLANNER_SOURCE.slice(start + marker.length, end);
  const names = body.split(',').map((name) => name.trim()).filter(Boolean);
  for (const name of names) {
    assert.match(name, /^[A-Za-z_$][A-Za-z0-9_$]*$/, 'planner export is a bare binding: ' + name);
  }
  return names;
}

/* Invoke the ACTUAL registered 90200 factory without touching
 * runtime.require(90200), which would cache a failed recursive DOM/WASM init.
 * The factory body and its export map stay authoritative; only its three
 * non-planner helpers are replaced by fail-closed UNEXECUTED proxies. */
function loadActualPlannerExports(runtime) {
  const factory = runtime.modules[PLANNER_MODULE_ID];
  assert.equal(typeof factory, 'function', 'actual registered 90200 factory');

  const required = [];
  const dependencies = new Map();
  const requireStub = (id) => {
    assert.ok(UNEXECUTED_PLANNER_DEPENDENCIES.includes(id),
      'actual 90200 may require only its three UNEXECUTED dependencies: ' + id);
    required.push(id);
    if (!dependencies.has(id)) dependencies.set(id, unexecutedPlannerDependency(id));
    return dependencies.get(id);
  };

  const module = { exports: {} };
  factory.call(module.exports, module, module.exports, requireStub);
  assert.deepEqual(required, UNEXECUTED_PLANNER_DEPENDENCIES,
    'actual 90200 factory required exactly its three UNEXECUTED dependencies');
  assert.equal(typeof module.exports, 'object', 'actual 90200 exports an object');
  assert.notEqual(module.exports, null, 'actual 90200 exports are not null');
  return module.exports;
}

function unexecutedPlannerDependency(id) {
  const label = 'UNEXECUTED planner dependency ' + id;
  const reject = (operation) => {
    throw new Error(label + ' was accessed (' + operation +
      '); this metadata harness does not execute planner mux helpers');
  };
  return new Proxy(function unexecutedPlannerDependencyStub() {}, {
    get(target, property) { reject('get ' + String(property)); },
    set(target, property) { reject('set ' + String(property)); },
    has(target, property) { reject('has ' + String(property)); },
    deleteProperty(target, property) { reject('delete ' + String(property)); },
    ownKeys() { reject('ownKeys'); },
    getOwnPropertyDescriptor(target, property) {
      reject('getOwnPropertyDescriptor ' + String(property));
    },
    defineProperty(target, property) { reject('defineProperty ' + String(property)); },
    getPrototypeOf() { reject('getPrototypeOf'); },
    setPrototypeOf() { reject('setPrototypeOf'); },
    isExtensible() { reject('isExtensible'); },
    preventExtensions() { reject('preventExtensions'); },
    apply() { reject('apply'); },
    construct() { reject('construct'); }
  });
}

function actualPlannerBindings() {
  const runtime = actualRuntime();
  const planner = loadActualPlannerExports(runtime);
  const imported = actualPlannerImportNames();
  const bindings = {};
  for (const name of imported) {
    assert.ok(Object.prototype.hasOwnProperty.call(planner, name),
      'actual 90200 export for imported planner binding ' + name);
    bindings[name] = planner[name];
  }
  return { planner, bindings };
}

function balancedMethod(source, head) {
  const start = source.indexOf(head);
  assert.ok(start >= 0, 'actual method ' + head);
  const open = start + head.length - 1;
  assert.equal(source[open], '{', 'method head opens at its real brace: ' + head);
  let depth = 0;
  for (let index = open; index < source.length; index += 1) {
    const ch = source[index];
    if (ch === '{') depth += 1;
    else if (ch === '}') {
      depth -= 1;
      if (depth === 0) return source.slice(start, index + 1);
    }
  }
  assert.fail('unbalanced actual method ' + head);
}

function actualLocalConst(name) {
  const marker = 'const ' + name + '=';
  const start = MSE_SOURCE.indexOf(marker);
  assert.ok(start >= 0, 'actual local constant ' + name);
  let depth = 0;
  let quote = null;
  let escaped = false;
  for (let index = start + marker.length; index < MSE_SOURCE.length; index += 1) {
    const ch = MSE_SOURCE[index];
    if (quote !== null) {
      if (escaped) escaped = false;
      else if (ch === '\\') escaped = true;
      else if (ch === quote) quote = null;
      continue;
    }
    if (ch === '"' || ch === "'" || ch === '`') {
      quote = ch;
      continue;
    }
    if (ch === '{' || ch === '[' || ch === '(') depth += 1;
    else if (ch === '}' || ch === ']' || ch === ')') depth -= 1;
    else if (ch === ';' && depth === 0) return MSE_SOURCE.slice(start, index + 1);
  }
  assert.fail('unterminated actual local constant ' + name);
}

/* Evaluate the ACTUAL method text. The only injected planner names are the ones
 * the ACTUAL module 26 destructuring imports, and their values come from the
 * ACTUAL 90200 exports. A missing import therefore stays a free identifier and
 * throws ReferenceError instead of being hidden by a local planner copy. */
function loadActualSnapshotClass(importBindings, domainApi) {
  const constants = SNAPSHOT_LOCAL_CONSTANTS.map(actualLocalConst).join('\n');
  const methods = SNAPSHOT_METHOD_HEADS.map((head) => balancedMethod(MSE_SOURCE, head)).join('\n');
  const sandbox = Object.assign({}, importBindings, {
    __actualEioSharedMediaDomainApi: domainApi
  });
  const code = [
    constants,
    'const __eioSharedMediaDomainApi=__actualEioSharedMediaDomainApi;',
    'class ActualMsePipeline {',
    methods,
    '}',
    'ActualMsePipeline'
  ].join('\n');
  return vm.runInNewContext(code, sandbox);
}

function cloneValue(value) {
  if (typeof structuredClone === 'function') return structuredClone(value);
  return v8.deserialize(v8.serialize(value));
}

test('actual 26 planner import bindings match the actual 90200 export map', () => {
  const imported = actualPlannerImportNames();
  const exported = actualPlannerExportNames();

  assert.equal(new Set(imported).size, imported.length, 'no duplicate planner imports');
  assert.equal(new Set(exported).size, exported.length, 'no duplicate planner exports');
  assert.deepEqual([...imported].sort(), [...exported].sort(),
    'module 26 imports exactly the actual 90200 exports');
  assert.ok(imported.includes('EIO_PLAN_ASSUMPTIONS'),
    'module 26 must import the actual planner assumptions list');
  assert.ok(exported.includes('EIO_PLAN_ASSUMPTIONS'),
    '90200 must export the actual planner assumptions list');

  const localDeclarations = new Set();
  for (const match of MSE_SOURCE.matchAll(
    /\b(?:const|let|var|function|class)\s+([A-Za-z_$][A-Za-z0-9_$]*)/g)) {
    localDeclarations.add(match[1]);
  }
  const references = new Set(Array.from(
    MSE_SOURCE.matchAll(/\bEIO_PLAN_[A-Z0-9_]+\b/g),
    (match) => match[0]
  ));
  for (const name of references) {
    assert.ok(imported.includes(name) || localDeclarations.has(name),
      name + ' must resolve to an actual planner import or a local declaration');
    assert.ok(!(imported.includes(name) && localDeclarations.has(name)),
      name + ' must not be shadowed by a second fake local constant');
  }
  assert.ok(references.has('EIO_PLAN_ASSUMPTIONS'), 'the workset snapshot must reference the assumptions');
  assert.ok(!localDeclarations.has('EIO_PLAN_ASSUMPTIONS'),
    'module 26 must not re-declare the planner assumptions constant');
  assert.equal(
    (PLANNER_SOURCE.match(/\bconst EIO_PLAN_ASSUMPTIONS\s*=\s*Object\.freeze\(\[/g) || []).length,
    1,
    'the planner remains the single source of the assumptions constant'
  );
  assert.equal(
    (MSE_SOURCE.match(/\b(?:const|let|var)\s+EIO_PLAN_ASSUMPTIONS\b/g) || []).length,
    0,
    'module 26 must not contain an inline EIO_PLAN_ASSUMPTIONS declaration'
  );
});

test('actual 90200 exports the frozen four-entry assumptions list', () => {
  const runtime = actualRuntime();
  const planner = loadActualPlannerExports(runtime);
  const imported = actualPlannerImportNames();

  for (const name of imported) {
    assert.ok(Object.prototype.hasOwnProperty.call(planner, name),
      'actual imported planner binding is exported: ' + name);
  }
  assert.ok(Object.isFrozen(planner.EIO_PLAN_ASSUMPTIONS),
    'actual assumptions list is frozen');
  assert.equal(planner.EIO_PLAN_ASSUMPTIONS.length, 4,
    'actual assumptions list has four entries');
  for (const assumption of planner.EIO_PLAN_ASSUMPTIONS) {
    assert.equal(typeof assumption, 'string');
    assert.ok(assumption.length > 0);
  }
  assert.equal(planner.EIO_PLAN_METADATA_MAX_ENTRIES, 1 << 16);
});

test('actual snapshot and workset methods use the actual 90200 binding', () => {
  const runtime = actualRuntime();
  const { planner, bindings } = actualPlannerBindings();
  const domainApi = runtime.require(90187);
  const Pipeline = loadActualSnapshotClass(bindings, domainApi);
  const pipeline = new Pipeline();
  pipeline.tasks = new Map();

  const taskId = 'mse-task';
  const task = {
    eioSharedController: { snapshot: () => ({ kind: 'owner-snapshot', heldBytes: 0 }) },
    eioSharedMediaDomain: { domainId: 'domain-1' },
    audio: null,
    video: null,
    eioCodecparLeases: null
  };
  pipeline.tasks.set(taskId, task);

  const snapshot = pipeline.getEioSharedMediaSnapshot(taskId);
  assert.equal(typeof snapshot, 'object');
  assert.notEqual(snapshot, null);
  assert.equal(snapshot.domainId, 'domain-1');
  assert.equal(snapshot.ownership.kind, 'owner-snapshot');
  assert.equal(snapshot.release, null);
  assert.equal(snapshot.owned, null);
  assert.equal(snapshot.streams.length, 0);
  assert.equal(snapshot.codecparLeases, null);
  assert.equal(snapshot.complete, false);
  assert.equal(snapshot.incomplete.length, 7);
  assert.equal(snapshot.incompleteReasons.length, 7);
  assert.equal(snapshot.conservativeWorkset.label, 'mse-plan-workset');
  assert.equal(snapshot.conservativeWorkset.bytes, 1048576);
  assert.equal(snapshot.conservativeWorkset.measured, false);
  assert.equal(snapshot.workset.assumptions, planner.EIO_PLAN_ASSUMPTIONS);
  assert.equal(snapshot.workset.assumptions.length, 4);
  assert.ok(Object.isFrozen(snapshot.workset.assumptions));
  assert.equal(pipeline.getEioSharedMediaSnapshot('missing-task'), null);

  const workset = pipeline.eioWorksetSnapshot(task);
  assert.equal(workset.assumptions, planner.EIO_PLAN_ASSUMPTIONS);
  assert.equal(workset.measured, false);
  assert.equal(workset.exact, false);
  assert.equal(workset.kind, null);
  assert.equal(workset.reservedUpperBound, 0);
  assert.equal(workset.streams.audio, null);
  assert.equal(workset.streams.video, null);
  assert.equal(workset.incomplete.length, 7);

  const audioOwner = {
    scope: {
      atomic: true,
      bytes: 123,
      remaining: 45,
      kind: 'mse-audio',
      retained: true,
      fault: null
    },
    lastPlan: { kind: 'packet' },
    writers: [],
    refusals: 0,
    lastRefusal: null
  };
  const ownerTask = Object.assign({}, task, {
    audio: { type: 'audio', eioOwned: audioOwner },
    video: null
  });
  const ownerWorkset = pipeline.eioWorksetSnapshot(ownerTask);
  assert.equal(ownerWorkset.kind, 'mse-audio');
  assert.equal(ownerWorkset.reservedUpperBound, 123);
  assert.equal(ownerWorkset.streams.audio.attached, true);
  assert.equal(ownerWorkset.streams.audio.reservedUpperBound, 123);
  assert.equal(ownerWorkset.streams.audio.remaining, 45);
  assert.equal(ownerWorkset.streams.audio.retained, true);
  assert.equal(ownerWorkset.streams.audio.lastPlan, audioOwner.lastPlan);
  assert.equal(ownerWorkset.streams.video, null);

  /* Pre-fix shape: the actual method text, actual local constants and actual
   * planner exports, but the actual imported binding is absent (as it was
   * before the planner export/import fix). The free identifier must throw. */
  const preFixBindings = Object.assign({}, bindings);
  delete preFixBindings.EIO_PLAN_ASSUMPTIONS;
  const PreFixPipeline = loadActualSnapshotClass(preFixBindings, domainApi);
  assert.throws(
    () => new PreFixPipeline().eioWorksetSnapshot({ audio: null, video: null }),
    (error) => error && error.name === 'ReferenceError' &&
      /EIO_PLAN_ASSUMPTIONS/.test(error.message),
    'a missing planner import must be a ReferenceError, not a silent undefined'
  );
});

test('populated codecpar lease diagnostic is clone-safe and uncharged', () => {
  const runtime = actualRuntime();
  const { bindings } = actualPlannerBindings();
  const domainApi = runtime.require(90187);
  const Pipeline = loadActualSnapshotClass(bindings, domainApi);
  const pipeline = new Pipeline();
  pipeline.tasks = new Map();

  let acquireCalls = 0;
  const resources = {
    acquire() {
      acquireCalls += 1;
      return {
        ok: true,
        handle: {
          release(proof) {
            return { ok: !!(proof && proof.referencesDropped === true) };
          }
        }
      };
    },
    snapshot() {
      return {
        liveBytes: 8,
        liveCount: 1,
        acquiredCount: 1,
        refusedCount: 0,
        faultCount: 0,
        core: null
      };
    }
  };
  const leasesModule = runtime.require(90191);
  const leases = leasesModule.createCodecparLeases({
    resources,
    measureNativePayload: () => 8
  });
  const lease = leases.acquireMeasured(8, 0, 'mse-snapshot-test');
  assert.equal(lease.bytes, 8);
  assert.equal(lease.bind({ copiedBytes: 8 }), true);

  const before = cloneValue(leases.snapshot());
  assert.equal(before.liveCount, 1);
  assert.equal(before.liveBytes, 8);
  assert.equal(before.boundCount, 1);
  const acquiresBefore = acquireCalls;

  const taskId = 'mse-task-leases';
  const task = {
    eioSharedController: { snapshot: () => ({ kind: 'owner-snapshot' }) },
    eioSharedMediaDomain: { domainId: 'domain-leases' },
    audio: null,
    video: null,
    eioCodecparLeases: leases
  };
  pipeline.tasks.set(taskId, task);

  const snapshot = pipeline.getEioSharedMediaSnapshot(taskId);
  assert.equal(snapshot.codecparLeases.liveCount, 1);
  assert.equal(snapshot.codecparLeases.liveBytes, 8);
  assert.equal(snapshot.codecparLeases.boundCount, 1);
  assert.equal(snapshot.workset.assumptions.length, 4);
  assert.equal(snapshot.complete, false);
  assert.equal(acquireCalls, acquiresBefore, 'snapshot must not acquire or charge a lease');

  /* The whole reply object, including the populated codecpar diagnostic, must
   * be structured-clone safe (plain objects, arrays, scalars and null only). */
  const clonedSnapshot = cloneValue(snapshot);
  assert.deepEqual(cloneValue(snapshot.codecparLeases), before);
  assert.equal(clonedSnapshot.codecparLeases.liveCount, 1);
  assert.equal(clonedSnapshot.codecparLeases.liveBytes, 8);
  assert.equal(clonedSnapshot.workset.assumptions.length, 4);

  const after = cloneValue(leases.snapshot());
  assert.deepEqual(after, before, 'the diagnostic getter must not change lease counters');
  assert.equal(after.liveBytes, before.liveBytes);
  assert.equal(after.acquiredCount, before.acquiredCount);
});
