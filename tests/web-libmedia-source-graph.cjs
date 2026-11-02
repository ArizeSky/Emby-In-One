'use strict';

/*
 * Source composition for the physically split libmedia webpack modules.
 *
 * This reads the ACTUAL shipped source text: public/vendor/web/libmedia/
 * avplayer.js plus the four synchronously loaded chunks 90201..90204. It only
 * concatenates source bodies and cuts out real factory entries by their real
 * webpack keys; it does not rewrite, reconstruct, stub or evaluate a fake core.
 *
 * LABEL: source-graph composition for method/factory tests.
 * This is NOT a product loader and NOT evidence that a browser page or a
 * generated libmedia Worker actually loads/collects the chunks. Runtime
 * loading and Worker dependency collection must be proved by separate runtime
 * tests; no assertion in this helper claims them.
 *
 * Fail-closed rules:
 *   - every shipped file must exist and be readable;
 *   - the main bundle must no longer declare the extracted module keys;
 *   - every chunk must declare exactly its one expected factory;
 *   - each expected factory signature must appear exactly once in the graph.
 *
 * Missing or partially extracted files therefore fail loudly instead of
 * falling back to an inline copy or a stub.
 */

const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');

const LIBMEDIA_DIR = path.join(__dirname, '..', 'public', 'vendor', 'web', 'libmedia');
const MAIN_FILE = 'avplayer.js';

const SPLIT_MODULES = Object.freeze({
  90190: Object.freeze({
    chunk: 90201,
    file: 'shared-media-ownership.js',
    signature: '90190:(module,exports,__webpack_require__)=>{',
    endMarker: null
  }),
  90187: Object.freeze({
    chunk: 90202,
    file: 'shared-media-domain.js',
    signature: '90187:(module,exports,__webpack_require__)=>{',
    endMarker: '/*__EIO_SHARED_MEDIA_DOMAIN_FACTORY_END__*/'
  }),
  90189: Object.freeze({
    chunk: 90203,
    file: 'owned-media-resources.js',
    signature: '90189:(module,exports,__webpack_require__)=>{',
    endMarker: null
  }),
  90188: Object.freeze({
    chunk: 90204,
    file: 'shared-media-release-registry.js',
    signature: '90188:(module,exports,__webpack_require__)=>{',
    endMarker: '/*__EIO_SHARED_MEDIA_RELEASE_FACTORY_END__*/'
  })
});

const MODULE_IDS = Object.freeze(Object.keys(SPLIT_MODULES).map(Number));
const CHUNK_IDS = Object.freeze(MODULE_IDS.map((id) => SPLIT_MODULES[id].chunk));

function countOccurrences(text, needle) {
  return text.split(needle).length - 1;
}

function readShippedFile(directory, name) {
  const file = path.join(directory, name);
  assert.ok(fs.existsSync(file),
    'missing actual shipped libmedia source ' + file +
    '; no inline copy or stub is substituted for a missing extraction');
  const text = fs.readFileSync(file, 'utf8');
  assert.ok(text.length > 0, 'shipped libmedia source is empty: ' + file);
  return text;
}

/* Chunk files wrap one real webpack factory:
 *   (self.webpackChunkAVPlayer=...).push([[90202],{
 *   90187:(module,exports,__webpack_require__)=>{ ... }
 *   }]);
 * Locate the exact factory close brace. Modules with a sentinel end at the
 * first '}' after that sentinel; the remaining chunks are single-factory
 * chunks, so their factory close is the last '}' before the chunk trailer.
 * Both paths verify the shape instead of guessing a slice. */
function factoryCloseIndex(text, signature, endMarker, id) {
  const start = text.indexOf(signature);
  assert.notEqual(start, -1, 'factory ' + id + ' signature is missing from its shipped chunk');
  if (endMarker !== null) {
    const marker = text.indexOf(endMarker, start + signature.length);
    assert.notEqual(marker, -1,
      'factory ' + id + ' end marker ' + endMarker + ' is missing from its shipped chunk');
    const close = text.indexOf('}', marker + endMarker.length);
    assert.notEqual(close, -1, 'factory ' + id + ' has no closing brace after its end marker');
    return close;
  }
  const trailer = text.lastIndexOf('}]);');
  assert.ok(trailer > start,
    'factory ' + id + ' chunk has no webpack chunk trailer after the factory');
  let close = trailer - 1;
  while (close > start && /\s/.test(text[close])) close -= 1;
  assert.equal(text[close], '}',
    'factory ' + id + ' must end at the closing brace before the chunk trailer');
  return close;
}

/* A chunk is expected to carry exactly one factory. This keeps the chunk-tail
 * extraction honest and fails if a future layout adds another module there. */
function assertSingleFactoryChunk(text, id, info) {
  assert.ok(text.includes(
    '(self.webpackChunkAVPlayer=self.webpackChunkAVPlayer||[]).push([[' + info.chunk + '],{'),
  'chunk ' + info.file + ' must be the real webpackChunkAVPlayer push for ' + info.chunk);
  const keys = Array.from(
    text.matchAll(/(\d+):\(module,exports,__webpack_require__\)=>\{/g),
    (match) => Number(match[1])
  );
  assert.deepEqual(keys, [id],
    'chunk ' + info.file + ' must declare exactly the expected physical module ' + id);
  assert.equal(countOccurrences(text, info.signature), 1,
    'factory ' + id + ' must appear exactly once in ' + info.file);
}

function staticRequires(source) {
  const ids = [];
  const pattern = /__webpack_require__\(\s*(\d+)\s*\)/g;
  let match = null;
  while ((match = pattern.exec(source)) !== null) {
    const id = Number(match[1]);
    if (!ids.includes(id)) ids.push(id);
  }
  return Object.freeze(ids);
}

function readLibmediaSourceGraph(options) {
  const settings = options || {};
  const directory = settings.directory === undefined ? LIBMEDIA_DIR : settings.directory;

  const main = readShippedFile(directory, MAIN_FILE);

  /* The extraction is complete only when the main bundle no longer declares
   * these factories. An empty placeholder key is still a declaration and must
   * fail here, because source tests would otherwise read fake inline text. */
  for (const id of MODULE_IDS) {
    assert.equal(countOccurrences(main, SPLIT_MODULES[id].signature), 0,
      'main ' + MAIN_FILE + ' still declares extracted module ' + id +
      '; the physical split is incomplete');
  }
  assert.equal(countOccurrences(main, '13486:(e,t,i)=>'), 1,
    'the real module 13486 factory must stay in main ' + MAIN_FILE);

  const chunks = {};
  const factories = {};
  const requires = {};
  const chunkTexts = [];

  for (const id of MODULE_IDS) {
    const info = SPLIT_MODULES[id];
    const text = readShippedFile(directory, info.file);
    assertSingleFactoryChunk(text, id, info);
    const start = text.indexOf(info.signature);
    const close = factoryCloseIndex(text, info.signature, info.endMarker, id);
    factories[id] = Object.freeze(text.slice(start, close + 1));
    requires[id] = staticRequires(factories[id]);
    chunks[id] = Object.freeze({ chunk: info.chunk, file: info.file, text });
    chunkTexts.push(text);
  }

  const text = [main].concat(chunkTexts).join('\n');

  for (const id of MODULE_IDS) {
    assert.equal(countOccurrences(text, SPLIT_MODULES[id].signature), 1,
      'factory ' + id + ' must appear exactly once in the composed shipped graph');
  }

  /* Structural wiring of the actual extracted factories. Module 90189 takes
   * the owned-resource core controller from its call sites, so it has no
   * static webpack require of its own; 90187 must statically reach the core
   * module 90190 and lazily reach the release registry 90188. */
  assert.ok(requires[90190].length === 0,
    'module 90190 core must not require another webpack module');
  assert.ok(requires[90188].length === 0,
    'module 90188 release registry must not require another webpack module');
  assert.ok(requires[90189].length === 0,
    'module 90189 owned resources must receive its controller, not require a module');
  assert.ok(requires[90187].includes(90190),
    'module 90187 facade must require the actual core module 90190');
  assert.ok(requires[90187].includes(90188),
    'module 90187 facade must require the actual release registry 90188');
  assert.ok(factories[90187].indexOf('__webpack_require__(90190)') <
    factories[90187].indexOf('__webpack_require__(90188)'),
  'module 90187 must require its core before the lazy release registry');

  const graph = {
    mode: 'source-composition',
    runtimeLoadingProved: false,
    directory,
    mainFile: MAIN_FILE,
    main,
    chunks,
    chunkTexts,
    factories,
    text,
    moduleIds: MODULE_IDS,
    chunkIds: CHUNK_IDS,
    hasModule(id) {
      return Object.prototype.hasOwnProperty.call(SPLIT_MODULES, String(id));
    },
    signature(id) {
      assert.ok(graph.hasModule(id), 'module ' + id + ' is not a split module');
      return SPLIT_MODULES[id].signature;
    },
    chunkOf(id) {
      assert.ok(graph.hasModule(id), 'module ' + id + ' is not a split module');
      return chunks[id];
    },
    factory(id) {
      assert.ok(graph.hasModule(id), 'module ' + id + ' is not a split module');
      return factories[id];
    },
    factoryBody(id) {
      const source = graph.factory(id);
      const signature = SPLIT_MODULES[id].signature;
      assert.ok(source.startsWith(signature),
        'factory ' + id + ' must start with its exact webpack signature');
      return source.slice(signature.length, -1);
    },
    requires(id) {
      assert.ok(graph.hasModule(id), 'module ' + id + ' is not a split module');
      return requires[id];
    }
  };

  return Object.freeze(graph);
}

module.exports = readLibmediaSourceGraph;
module.exports.readLibmediaSourceGraph = readLibmediaSourceGraph;
module.exports.SPLIT_MODULES = SPLIT_MODULES;
module.exports.MODULE_IDS = MODULE_IDS;
module.exports.CHUNK_IDS = CHUNK_IDS;
module.exports.LIBMEDIA_DIR = LIBMEDIA_DIR;
