const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

// Two vendored-bundle defects around oversized Matroska attachments:
//
// 1. module 27228 parseEbmlSyntax, EBML BUFFER (case 8): sizes at or above the
//    20 MiB limit returned {pos, size, data: null} without advancing the
//    ioReader, so every following element was parsed at the attachment's first
//    payload byte. The patch keeps the node metadata and advances exactly
//    `size` bytes with awaited skip calls of at most 16 MiB, using a BigInt
//    remaining count (never BigInt.asIntN(32) over the whole size, which wraps
//    above 2 GiB). Each chunk is passed to the real `ioReader.skip` as a
//    Number, its actual argument type; <= 16 MiB keeps that exact. The mock
//    rejects any non-Number skip argument. Skip rejections propagate. The
//    binary read itself is now routed through the actual `e.eioMatroskaOwned`
//    helper when it is installed; fixtures without an owned manager exercise
//    the unchanged `readBuffer(Number(BigInt.asIntN(32, size)))` fallback.
//
// 2. module 25248 analyzeStreams, Attachments: `if (t.data)` copied
//    t.data.data even when the payload was null, allocating a buffer for an
//    attachment that was never read. The guard now requires t.data.data, so an
//    oversized image cover or font keeps its stream/metadata but triggers no
//    malloc/copy; ordinary small attachments still copy their payload.
//
// Both code paths are cut out of the artifact at runtime and executed in a VM.
// The EBML vint reader below is fixture plumbing for the mocked IO; it is not a
// copy of the patched algorithm. No HAR, private media, network, or real
// upstream is used.

const BUNDLE = path.join(__dirname, '../public/vendor/web/libmedia/573.avplayer.js');
const bundle = fs.readFileSync(BUNDLE, 'utf8');

const MiB = 1048576n;
const LIMIT = 20n * MiB; // 20971520, unchanged upstream threshold
const CHUNK = 16n * MiB; // 16777216, per-skip cap
const count = (needle) => bundle.split(needle).length - 1;

const CASE8_OLD =
  'case 8:n={pos:e.ioReader.getPos(),size:d,data:d<o?await e.ioReader.readBuffer(Number(BigInt.asIntN(32,d))):null};break;';
const CASE8_NEW =
  'case 8:{const p=e.ioReader.getPos();if(d<o){const eioApi=e.eioMatroskaOwned,eioMgr=eioApi&&eioApi.manager(),eioN=eioMgr?eioApi.safe(d):Number(BigInt.asIntN(32,d));if(eioMgr&&(null===eioN||eioN<0)){eioApi.error("EIO_OWNED_BINARY_SIZE_INVALID",{size:String(d)});const eioErr=new Error("EIO_OWNED_BINARY_SIZE_INVALID");throw eioErr.code="EIO_OWNED_BINARY_SIZE_INVALID",eioErr}let eioRead=null;if(eioMgr)try{eioRead=await eioApi.readBinary(eioN,"matroska-binary")}catch(err){if(err&&(err.eioCancelled||"EIO_OWNED_ADMISSION_CANCELLED"===err.code))throw err;const eioCode=err&&err.code||"EIO_OWNED_BINARY_READ_FAILED";eioApi.error(eioCode,{need:eioN,cause:eioCode});throw err}n={pos:p,size:d,data:eioRead?eioRead.data:await e.ioReader.readBuffer(eioN)};eioRead&&eioRead.handle&&(n.eioOwned=eioRead.handle)}else{const q=BigInt(16777216);let s=d;for(n={pos:p,size:d,data:null};s>BigInt(0);){const u=s>q?q:s;await e.ioReader.skip(Number(u)),s-=u}}break}';
const ATTACH_OLD = ',t.data)if(p.xz[t.mime])';
const ATTACH_NEW = ',t.data&&t.data.data)if(p.xz[t.mime])';

const PARSE_HEAD =
  /async function ([A-Za-z_$][\w$]*)\(e,t,a\)\{let c=arguments\.length>3&&void 0!==arguments\[3\]/;
const LIMIT_DECL = /([A-Za-z_$][\w$]*)=BigInt\(20971520\)/g;

// The parse function is the last export of module 27228, immediately followed
// by module 35734, so the module boundary gives an exact end without guessing
// at braces inside the minified body.
function extractParseEbml() {
  const match = bundle.match(PARSE_HEAD);
  assert.ok(match, 'parseEbmlSyntax function not found in bundle');
  const name = match[1];
  const start = match.index;
  const boundaryPattern = /,(\d{3,6}):\(e,t,a\)=>\{/g;
  boundaryPattern.lastIndex = start;
  const boundary = boundaryPattern.exec(bundle);
  assert.ok(boundary && boundary.index > start, 'module boundary after parseEbmlSyntax not found');
  const src = bundle.slice(start, boundary.index - 1);
  assert.ok(src.endsWith('c}'), 'parseEbmlSyntax must still return the accumulated syntax object');
  assert.ok(src.includes(CASE8_NEW), 'extracted parseEbmlSyntax must carry the patched BUFFER branch');
  assert.ok(!src.includes(CASE8_OLD), 'extracted parseEbmlSyntax must not carry the unadvanced BUFFER branch');
  assert.ok(src.includes('const eioApi=e.eioMatroskaOwned'), 'BUFFER branch must use the actual owned-read helper');
  assert.ok(src.includes('eioApi.readBinary(eioN,"matroska-binary")'), 'BUFFER branch must call the actual owned binary read');
  assert.ok(src.includes('n.eioOwned=eioRead.handle'), 'owned binary reads must retain the returned handle');
  assert.ok(
    src.includes('eioRead?eioRead.data:await e.ioReader.readBuffer(eioN)'),
    'fixtures without an owned manager must keep the original readBuffer fallback',
  );
  return { name, src };
}

function findLimitName() {
  const matches = [...bundle.matchAll(LIMIT_DECL)];
  assert.equal(matches.length, 1, 'exactly one 20 MiB limit constant expected');
  return matches[0][1];
}

const parseEbml = extractParseEbml();
const LIMIT_GLOBAL = findLimitName();

// ---- EBML fixtures and mocked IO (no large allocations) -------------------

class MockReader {
  constructor() {
    this.pos = 0n;
    this.segments = [];
    this.skips = [];
    this.reads = [];
    this.failSkipAt = 0;
    this.skipError = null;
  }

  addSegment(start, bytes) {
    this.segments.push({ start: BigInt(start), bytes, end: BigInt(start) + BigInt(bytes.length) });
  }

  getPos() {
    return this.pos;
  }

  readByte() {
    for (const segment of this.segments) {
      if (this.pos >= segment.start && this.pos < segment.end) {
        const byte = segment.bytes[Number(this.pos - segment.start)];
        this.pos += 1n;
        return byte;
      }
    }
    throw new Error(`mock reader has no materialized byte at ${this.pos}`);
  }

  async skip(size) {
    // The real IOReader.skip takes a Number. Accept nothing else, so a BigInt
    // argument (or a wrapped/negative/oversized one) fails the test here.
    assert.equal(typeof size, 'number', 'IOReader.skip must receive a Number');
    assert.ok(Number.isSafeInteger(size), 'skip size must be a safe integer');
    assert.ok(size > 0, 'skip size must be positive');
    const chunk = BigInt(size);
    this.skips.push(chunk);
    if (this.failSkipAt && this.skips.length === this.failSkipAt) {
      throw this.skipError || new Error('mock skip failed');
    }
    this.pos += chunk;
    return chunk;
  }

  async readBuffer(size) {
    const length = Number(size);
    assert.ok(Number.isInteger(length) && length >= 0, 'readBuffer size must be a small integer');
    this.reads.push(length);
    this.pos += BigInt(length);
    // Stub instead of a real allocation; the real bundle would read the bytes.
    return length === 0 ? new Uint8Array(0) : { byteLength: length, virtual: true };
  }

  async readExact(length) {
    const out = new Uint8Array(length);
    for (let i = 0; i < length; i += 1) out[i] = this.readByte();
    return out;
  }
}

function readElementId(reader, maxLength) {
  const first = reader.readByte();
  let length = 1;
  let mask = 0x80;
  while (length <= 8 && !(first & mask)) {
    length += 1;
    mask >>= 1;
  }
  assert.ok(length <= maxLength, 'element id length exceeds maxIdLength');
  let id = first;
  for (let i = 1; i < length; i += 1) id = id * 256 + reader.readByte();
  return id;
}

function readElementSize(reader, maxLength) {
  const first = reader.readByte();
  let length = 1;
  let mask = 0x80;
  while (length <= 8 && !(first & mask)) {
    length += 1;
    mask >>= 1;
  }
  assert.ok(length <= maxLength, 'element size length exceeds maxSizeLength');
  let value = BigInt(first & (0xff >> length));
  for (let i = 1; i < length; i += 1) value = value * 256n + BigInt(reader.readByte());
  return value;
}

async function readUint(format, size) {
  const bytes = await format.ioReader.readExact(Number(size));
  let value = 0;
  for (const byte of bytes) value = value * 256 + byte;
  return value;
}

function encodeSize(value) {
  const target = BigInt(value);
  let length = 1;
  while (length < 8 && target >= 1n << BigInt(7 * length)) length += 1;
  const bytes = new Uint8Array(length);
  let rest = target;
  for (let i = length - 1; i >= 0; i -= 1) {
    bytes[i] = Number(rest & 0xffn);
    rest >>= 8n;
  }
  bytes[0] |= 0x80 >> (length - 1);
  return bytes;
}

function bytesOf(parts) {
  const total = parts.reduce((sum, part) => sum + part.length, 0);
  const out = new Uint8Array(total);
  let at = 0;
  for (const part of parts) {
    out.set(part, at);
    at += part.length;
  }
  return out;
}

const ATTACHMENT_ID = Uint8Array.from([0x46, 0x5c]); // binary FileData-like element
const NEXT_ID = Uint8Array.from([0xa3]); // unsigned integer element
const SYNTAX = {
  0x465c: { type: 8, filedName: 'fileData', isArray: false },
  0xa3: { type: 1, filedName: 'nextValue', isArray: false },
};

function makeFixture(size, nextPayload = null) {
  const reader = new MockReader();
  const attachmentHeader = bytesOf([ATTACHMENT_ID, encodeSize(size)]);
  reader.addSegment(0n, attachmentHeader);
  const dataStart = BigInt(attachmentHeader.length);
  const nextStart = dataStart + BigInt(size);
  let total = nextStart;
  if (nextPayload) {
    const payload = Uint8Array.from(nextPayload);
    const nextHeader = bytesOf([NEXT_ID, encodeSize(BigInt(payload.length))]);
    reader.addSegment(nextStart, bytesOf([nextHeader, payload]));
    total = nextStart + BigInt(nextHeader.length + payload.length);
  }
  // Deliberately no `eioMatroskaOwned` manager: the actual BUFFER branch must
  // take its unchanged `readBuffer(eioN)` fallback for these fixtures.
  const format = {
    privateData: { header: { maxIdLength: 4, maxSizeLength: 8 } },
    ioReader: reader,
  };
  return { format, reader, dataStart, nextStart, total };
}

function buildParse(reader) {
  const sandbox = {
    i: { ai: (value) => typeof value === 'bigint' },
    S: (format, maxLength) => readElementId(format.ioReader, maxLength),
    b: (ioReader, maxLength) => readElementSize(ioReader, maxLength),
    B: (format, size) => readUint(format, size),
    P: async () => '',
  };
  sandbox[LIMIT_GLOBAL] = LIMIT;
  const fn = vm.runInNewContext(`${parseEbml.src}\n${parseEbml.name}`, sandbox);
  assert.equal(typeof fn, 'function', 'extracted parseEbmlSyntax must evaluate to a function');
  return (format, total) => fn(format, total, SYNTAX);
}

// ---- parseEbmlSyntax execution tests -------------------------------------

test('below the 20 MiB limit the payload is still read and the node keeps pos/size', async () => {
  for (const size of [0n, LIMIT - 1n]) {
    const { format, reader, dataStart, total } = makeFixture(size);
    const parse = buildParse(reader);
    const node = await parse(format, total);
    assert.equal(node.fileData.pos, dataStart);
    assert.equal(node.fileData.size, size);
    assert.ok(node.fileData.data, 'payload below the limit must be read');
    assert.deepEqual(reader.reads, [Number(size)], 'readBuffer must receive the exact small size');
    assert.deepEqual(reader.skips, [], 'no skip below the limit');
    assert.equal(reader.getPos(), total, 'reader ends after the payload');
  }
});

test('at or above the 20 MiB limit skips exactly size bytes in <=16 MiB chunks', async () => {
  for (const size of [LIMIT, LIMIT + 1n, 2n ** 31n + 1n, 2n ** 32n + 1n]) {
    const { format, reader, dataStart, total } = makeFixture(size);
    const parse = buildParse(reader);
    const node = await parse(format, total);
    assert.equal(node.fileData.pos, dataStart, 'payload position is retained');
    assert.equal(node.fileData.size, size, 'payload size is retained');
    assert.equal(node.fileData.data, null, 'oversized payload must stay null');
    assert.deepEqual(reader.reads, [], 'oversized payload must not be read');
    assert.ok(reader.skips.length >= 1, 'oversized payload must be skipped');
    let sum = 0n;
    for (const chunk of reader.skips) {
      assert.equal(typeof chunk, 'bigint', 'recorded skip chunks must stay BigInt for exact summation');
      assert.ok(chunk > 0n, 'skip chunk must be positive');
      assert.ok(chunk <= CHUNK, `skip chunk ${chunk} exceeds 16 MiB`);
      sum += chunk;
    }
    assert.equal(sum, size, 'skip chunks must sum to the exact element size');
    assert.equal(reader.getPos(), total, 'reader must end at the computed element end');
  }
});

test('the 20 MiB threshold is exact: 20 MiB - 1 reads, 20 MiB skips', async () => {
  const below = makeFixture(LIMIT - 1n);
  await buildParse(below.reader)(below.format, below.total);
  assert.equal(below.reader.reads.length, 1);
  assert.equal(below.reader.skips.length, 0);

  const at = makeFixture(LIMIT);
  await buildParse(at.reader)(at.format, at.total);
  assert.equal(at.reader.reads.length, 0);
  assert.equal(at.reader.skips.length, 2, '20 MiB = 16 MiB + 4 MiB');
  assert.deepEqual(at.reader.skips, [CHUNK, LIMIT - CHUNK]);
});

test('the element immediately after an oversized attachment parses at the exact offset', async () => {
  for (const size of [LIMIT, 2n ** 32n + 1n]) {
    const { format, reader, dataStart, nextStart, total } = makeFixture(size, [0x2a]);
    const parse = buildParse(reader);
    const node = await parse(format, total);
    assert.equal(node.fileData.pos, dataStart);
    assert.equal(node.fileData.size, size);
    assert.equal(node.fileData.data, null);
    assert.equal(node.nextValue, 42, 'following element must be read at the post-skip offset');
    assert.equal(nextStart, dataStart + size);
    assert.equal(reader.getPos(), total, 'both elements consumed exactly');
  }
});

test('a skip rejection propagates and does not continue parsing', async () => {
  const size = LIMIT + 1n;
  const { format, reader, dataStart, total } = makeFixture(size, [0x2a]);
  reader.failSkipAt = 2;
  reader.skipError = new Error('io skip failed');
  const parse = buildParse(reader);

  await assert.rejects(parse(format, total), /io skip failed/);
  assert.equal(reader.skips.length, 2, 'no skip after the failure');
  assert.equal(reader.getPos(), dataStart + CHUNK, 'position stops after the first successful chunk');
  assert.equal(reader.reads.length, 0, 'no read fallback after a failed skip');
});

// ---- analyzeStreams attachment branch ------------------------------------

const ATTACH_START = 'this.context.attachments&&_.__(this.context.attachments.entry';
const ATTACH_END = 't.data.data)}))';

function extractAttachmentBranch() {
  const start = bundle.indexOf(ATTACH_START);
  assert.ok(start >= 0, 'attachment analysis branch not found');
  const methodStart = bundle.lastIndexOf('analyzeStreams(e){', start);
  assert.ok(methodStart >= 0 && methodStart < start, 'attachment branch must live in analyzeStreams');
  const methodEnd = bundle.indexOf('async readHeader(', methodStart);
  assert.ok(methodEnd > start, 'analyzeStreams must be followed by readHeader');
  const method = bundle.slice(methodStart, methodEnd);
  assert.equal(method.split(ATTACH_START).length - 1, 1, 'exactly one attachment branch in the method');
  const end = bundle.indexOf(ATTACH_END, start);
  assert.ok(end > start && end < methodEnd, 'attachment branch must end inside analyzeStreams');
  const snippet = bundle.slice(start, end + ATTACH_END.length);
  assert.ok(snippet.includes(ATTACH_NEW), 'extracted branch must carry the payload guard');
  assert.ok(!snippet.includes(ATTACH_OLD), 'extracted branch must not copy a null payload');
  return snippet;
}

const ATTACHMENT_BRANCH = extractAttachmentBranch();
// The branch names the bundle's dependency variables directly, so they must be
// this function's parameters: `runAttachmentBranch.call(context, e, _...)`
// binds `this` and supplies the real mocks. Wrapping the branch in a
// zero-argument `return function(){...}()` factory would invoke it with no
// arguments, freezing e/_/p/... to undefined before any test call.
const runAttachmentBranch = new Function(
  'e',
  '_',
  'p',
  'd',
  'I',
  'r',
  'y',
  'n',
  ATTACHMENT_BRANCH,
);

const IMAGE_MIME = 'image/jpeg';
const FONT_MIME = 'application/x-truetype-font';

function attachment({ mime, data, name = 'file.bin', description = 'desc' }) {
  return { name, mime, description, data };
}

function analyzeAttachments(entry) {
  const calls = { malloc: [], copy: [], nx: [], writes: [], streams: [] };
  // The real branch runs with `this` bound to the IMatroskaFormat instance and
  // reads `this.context.attachments`; keep that exact shape so a wrong this
  // binding fails instead of silently passing.
  const context = { context: { attachments: { entry } } };
  const e = {
    createStream() {
      const stream = {
        index: calls.streams.length,
        codecpar: { codecType: 0, codecId: 0, extradata: null, extradataSize: 0 },
        metadata: {},
        disposition: 0,
        attachedPic: null,
        privData: null,
      };
      calls.streams.push(stream);
      return stream;
    },
  };
  const _ = {
    __: (list, callback) => {
      for (const item of list) callback(item);
    },
  };
  const p = { xz: { [IMAGE_MIME]: 42 } };
  const d = {
    M: {
      15: (address, value) => {
        assert.ok(Number.isInteger(address) && address > 0, 'field address must be a stream offset');
        assert.equal(typeof value, 'number', 'field value must be a Number');
        calls.writes.push([address, value]);
      },
    },
  };
  const I = {
    sY: (size) => {
      assert.equal(typeof size, 'number', 'sY must receive a Number size');
      assert.ok(Number.isSafeInteger(size) && size >= 0, 'sY size must be a non-negative safe integer');
      calls.malloc.push(size);
      return { size };
    },
  };
  const r = {
    lW: (dst, len, src) => {
      assert.equal(typeof len, 'number', 'lW length must be a Number');
      assert.ok(Number.isSafeInteger(len) && len >= 0, 'lW length must be a non-negative safe integer');
      assert.ok(src && typeof src.byteLength === 'number', 'lW source must be real payload bytes');
      assert.equal(dst.size, len, 'lW destination must be sized for the copied payload');
      calls.copy.push({ dst, len, src });
    },
  };
  const y = {
    _5: () => 7,
    NX: (pic, buf, len) => {
      assert.equal(typeof len, 'number', 'NX length must be a Number');
      assert.ok(Number.isSafeInteger(len) && len >= 0, 'NX length must be a non-negative safe integer');
      calls.nx.push({ pic, buf, len });
    },
  };
  const n = { f: { 15: () => 0 } };

  runAttachmentBranch.call(context, e, _, p, d, I, r, y, n);
  return calls;
}

test('a null-payload image attachment keeps its stream/metadata but allocates nothing', () => {
  const att = attachment({ mime: IMAGE_MIME, data: { pos: 10n, size: LIMIT + 1n, data: null } });
  const calls = analyzeAttachments([att]);
  assert.equal(calls.streams.length, 1);
  const stream = calls.streams[0];

  assert.equal(stream.codecpar.codecType, 4, 'attachment codec type preserved');
  assert.equal(stream.privData, att, 'privData keeps the pos/size/null-payload metadata');
  assert.equal(stream.codecpar.codecId, 0, 'no cover codec without payload');
  assert.equal(stream.disposition & 1024, 0);
  assert.equal(stream.attachedPic, null);
  assert.deepEqual(calls.malloc, [], 'no buffer for an unread cover');
  assert.deepEqual(calls.copy, [], 'no copy from a null payload');
  assert.deepEqual(calls.nx, []);
  assert.deepEqual(calls.writes, []);

  assert.equal(stream.metadata.title, 'file.bin');
  assert.equal(stream.metadata.mime, IMAGE_MIME);
  assert.equal(stream.metadata.description, 'desc');
});

test('a null-payload font attachment keeps metadata and skips extradata allocation', () => {
  const att = attachment({ mime: FONT_MIME, data: { pos: 0n, size: LIMIT, data: null } });
  const calls = analyzeAttachments([att]);
  const stream = calls.streams[0];

  assert.equal(stream.codecpar.codecType, 4);
  assert.equal(stream.codecpar.extradataSize, 0, 'extradata size must stay untouched');
  assert.equal(stream.codecpar.extradata, null, 'no extradata buffer');
  assert.deepEqual(calls.malloc, []);
  assert.deepEqual(calls.copy, []);
  assert.equal(stream.metadata.mime, FONT_MIME);
});

test('an attachment without a data node keeps its stream and allocates nothing', () => {
  const att = { name: 'note.txt', mime: 'text/plain', description: null };
  const calls = analyzeAttachments([att]);
  const stream = calls.streams[0];
  assert.equal(stream.codecpar.codecType, 4);
  assert.equal(stream.privData, att);
  assert.deepEqual(calls.malloc, []);
  assert.deepEqual(calls.copy, []);
  assert.equal(stream.metadata.title, 'note.txt');
});

test('an ordinary image attachment still copies its payload and marks the cover', () => {
  const payload = Uint8Array.from([9, 8, 7]);
  const att = attachment({ mime: IMAGE_MIME, data: { pos: 5n, size: 3n, data: payload } });
  const calls = analyzeAttachments([att]);
  const stream = calls.streams[0];

  assert.equal(stream.codecpar.codecId, 42);
  assert.equal(stream.codecpar.codecType, 0);
  assert.equal(stream.disposition & 1024, 1024);
  assert.equal(stream.attachedPic, 7);
  assert.deepEqual(calls.malloc, [3]);
  assert.equal(calls.copy.length, 1);
  assert.equal(calls.copy[0].src, payload, 'copy source must be the original payload');
  assert.equal(calls.copy[0].len, 3);
  assert.equal(calls.copy[0].dst.size, 3);
  assert.equal(calls.nx.length, 1);
  assert.equal(calls.nx[0].len, 3);
  assert.deepEqual(calls.writes, [[39, 0], [43, 1]], 'cover index and disposition are written at the AVFrame offsets');
});

test('an ordinary font attachment still copies its payload into extradata', () => {
  const payload = Uint8Array.from([1, 2]);
  const att = attachment({ mime: FONT_MIME, data: { pos: 1n, size: 2n, data: payload } });
  const calls = analyzeAttachments([att]);
  const stream = calls.streams[0];

  assert.equal(stream.codecpar.extradataSize, 2);
  assert.equal(stream.codecpar.extradata.size, 2);
  assert.deepEqual(calls.malloc, [2]);
  assert.equal(calls.copy.length, 1);
  assert.equal(calls.copy[0].src, payload);
  assert.equal(calls.copy[0].len, 2);
  assert.deepEqual(calls.nx, [], 'fonts are not attached pictures');
});

// ---- artifact guards ------------------------------------------------------

test('the bundle carries exactly the two patched attachment branches and keeps the threshold', () => {
  assert.equal(count(CASE8_OLD), 0, 'unadvanced oversized BUFFER branch must be gone');
  assert.equal(count(CASE8_NEW), 1, 'patched BUFFER branch must appear exactly once');
  assert.equal(count(ATTACH_OLD), 0, 'unguarded attachment payload copy must be gone');
  assert.equal(count(ATTACH_NEW), 1, 'guarded attachment payload copy must appear exactly once');
  assert.equal(count('BigInt(20971520)'), 1, '20 MiB limit must be unchanged and unique');
  assert.ok(
    parseEbml.src.includes('await e.ioReader.skip(Number(u))'),
    'oversized branch must await skip with a Number',
  );
  assert.ok(
    parseEbml.src.includes('eioApi.readBinary(eioN,"matroska-binary")'),
    'actual owned binary wrapper must be present in the executed BUFFER branch',
  );
  assert.ok(!parseEbml.src.includes('skip(u)'), 'oversized branch must not pass the raw BigInt chunk');
  assert.ok(!parseEbml.src.includes('BigInt.asIntN(32,d)):null'), 'skip must not wrap the size to 32 bit');
});

test('the executed code is a contiguous slice of the vendored artifact', () => {
  assert.equal(typeof parseEbml.name, 'string');
  assert.ok(parseEbml.src.length > 500, 'parseEbmlSyntax body expected');

  const start = bundle.indexOf(ATTACH_START);
  assert.ok(start >= 0);
  assert.equal(bundle.slice(start, start + ATTACHMENT_BRANCH.length), ATTACHMENT_BRANCH);
  assert.ok(bundle.lastIndexOf('analyzeStreams(e){', start) >= 0);
  assert.ok(bundle.indexOf('async readHeader(', start) > start);
});
