(function (root, factory) {
  'use strict';
  if (typeof module === 'object' && module.exports) module.exports = factory();
  else root.EioNativePlaybackClock = factory();
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
  'use strict';

  // Control metadata only. No compressed payload, byte credit, wall clock or gate.
  var WORDS = 16;
  var MAGIC = 0x45494f43;
  var VERSION = 1;
  var SEQ = 2;
  var EPOCH = 3;
  var VALID = 4;
  var LOW = 5;
  var HIGH = 6;
  var PAUSED = 7;
  var WRITER = 8;
  var TWO32 = 4294967296;
  var LIMIT = 2147483646;

  function invalid(code) {
    var error = new Error(code);
    error.code = code;
    return error;
  }

  function integer(value) {
    return Number.isSafeInteger(value) && value >= 0;
  }

  function attach(sab) {
    if (typeof SharedArrayBuffer !== 'function' || !(sab instanceof SharedArrayBuffer) ||
        sab.byteLength !== WORDS * 4) throw invalid('EIO_NATIVE_CLOCK_DESCRIPTOR_INVALID');
    var view = new Int32Array(sab);
    if (Atomics.load(view, 0) !== MAGIC || Atomics.load(view, 1) !== VERSION) {
      throw invalid('EIO_NATIVE_CLOCK_DESCRIPTOR_INVALID');
    }
    if (arguments.length > 1 && arguments[1] !== undefined) {
      var nonce = arguments[1];
      if (!Array.isArray(nonce) || nonce.length !== 4) throw invalid('EIO_NATIVE_CLOCK_DOMAIN_MISMATCH');
      for (var index = 0; index < 4; index += 1) {
        if (!Number.isInteger(nonce[index]) || nonce[index] < -2147483648 || nonce[index] > 2147483647 ||
            Atomics.load(view, 9 + index) !== nonce[index]) throw invalid('EIO_NATIVE_CLOCK_DOMAIN_MISMATCH');
      }
    }
    return view;
  }

  function create(nonce) {
    var sab = new SharedArrayBuffer(WORDS * 4);
    var view = new Int32Array(sab);
    Atomics.store(view, 0, MAGIC);
    Atomics.store(view, 1, VERSION);
    if (nonce !== undefined) {
      if (!Array.isArray(nonce) || nonce.length !== 4 || nonce.some(function (word) {
        return !Number.isInteger(word) || word < -2147483648 || word > 2147483647;
      })) throw invalid('EIO_NATIVE_CLOCK_DOMAIN_MISMATCH');
      for (var index = 0; index < 4; index += 1) Atomics.store(view, 9 + index, nonce[index]);
    }
    return Object.freeze({ sab: sab, version: VERSION });
  }

  function read(sab, nonce) {
    var view = attach(sab, nonce);
    for (var attempt = 0; attempt < 8; attempt += 1) {
      var first = Atomics.load(view, SEQ);
      if (first & 1) continue;
      var epoch = Atomics.load(view, EPOCH);
      var valid = Atomics.load(view, VALID) === 1;
      var low = Atomics.load(view, LOW) >>> 0;
      var high = Atomics.load(view, HIGH);
      var paused = Atomics.load(view, PAUSED) === 1;
      if (first !== Atomics.load(view, SEQ)) continue;
      var mediaMs = high * TWO32 + low;
      if (!integer(mediaMs) || epoch < 0 || first < 0) valid = false;
      return Object.freeze({ version: VERSION, epoch: epoch, sequence: first,
        known: valid, mediaMs: valid ? mediaMs : null, paused: paused,
        source: valid ? 'native-timeupdate' : null,
        admissionAuthority: false, complete: false });
    }
    return Object.freeze({ version: VERSION, epoch: null, sequence: null,
      known: false, mediaMs: null, paused: null, source: null,
      admissionAuthority: false, complete: false });
  }

  // One main-thread writer, no writes from readers. Writer identity and native
  // element lifecycle belong to the caller; attaching does not grant byte credit.
  function writer(sab, nonce) {
    var view = attach(sab, nonce);
    var closed = false;
    var epoch = Atomics.load(view, EPOCH);
    var sequence = Atomics.load(view, SEQ);
    var anchor = null;
    if (sequence !== 0 || epoch !== 0) throw invalid('EIO_NATIVE_CLOCK_WRITER_ALREADY_USED');
    if (Atomics.compareExchange(view, WRITER, 0, 1) !== 0) {
      throw invalid('EIO_NATIVE_CLOCK_WRITER_ALREADY_USED');
    }

    function store(known, mediaMs, paused) {
      if (closed) return false;
      if (sequence > LIMIT - 2) {
        Atomics.store(view, SEQ, 2147483647);
        Atomics.store(view, VALID, 0);
        closed = true;
        throw invalid('EIO_NATIVE_CLOCK_SEQUENCE_EXHAUSTED');
      }
      Atomics.store(view, SEQ, ++sequence);
      Atomics.store(view, VALID, known ? 1 : 0);
      Atomics.store(view, EPOCH, epoch);
      Atomics.store(view, LOW, known ? mediaMs % TWO32 : 0);
      Atomics.store(view, HIGH, known ? Math.floor(mediaMs / TWO32) : 0);
      Atomics.store(view, PAUSED, paused ? 1 : 0);
      Atomics.store(view, SEQ, ++sequence);
      return true;
    }

    function invalidate() {
      if (closed) return false;
      if (epoch >= LIMIT) {
        Atomics.store(view, SEQ, 2147483647);
        Atomics.store(view, VALID, 0);
        closed = true;
        throw invalid('EIO_NATIVE_CLOCK_EPOCH_EXHAUSTED');
      }
      epoch += 1;
      anchor = null;
      return store(false, 0, false);
    }

    function publish(mediaMs, expectedEpoch, paused) {
      if (closed || expectedEpoch !== epoch || !integer(mediaMs) ||
          (anchor !== null && mediaMs < anchor)) return false;
      anchor = mediaMs;
      return store(true, mediaMs, paused === true);
    }

    function close() {
      if (closed) return;
      invalidate();
      closed = true;
    }

    return Object.freeze({ publish: publish, invalidate: invalidate, close: close,
      epoch: function () { return epoch; }, snapshot: function () { return read(sab, nonce); } });
  }

  return Object.freeze({ VERSION: VERSION, create: create, read: read, writer: writer });
});
