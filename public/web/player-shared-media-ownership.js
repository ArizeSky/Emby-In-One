/*!
 * player-shared-media-ownership.js
 *
 * Shared last-free accounting primitive for compressed-media playback (the
 * actual MSE target of this player; not a WebCodecs pipeline).
 *
 * This is NOT a full media-budget broker and it does NOT install any hooks.
 * It only tracks the byte ownership of one logical payload across workers so
 * that the worker that reserved the payload and the worker that performs the
 * real final free (module 13486 / Q7 last-reference branch) agree on held
 * bytes. The caller is responsible for calling prepareFinalFree() at the
 * actual final-free branch and commitFinalFree() only after the payload data
 * pointer has really been freed.
 *
 * Keying: the opaque record key is the 32-bit payload pointer AND the 32-bit
 * AVBuffer metadata pointer. Although FFmpeg/av.js keep a stable data pointer
 * for one buffer, the data pointer can be recycled by another thread while
 * the old AVBuffer metadata is still alive (pooled/reallocated metadata).
 * Keying on the data pointer or on a packet/ref pointer alone is therefore
 * unsafe.
 *
 * Two record kinds share this one accounting domain (one HELD counter, one
 * entry table, not a second ledger):
 *   - FLAGS=0  normal FFmpeg sample: reserve() -> bind() with real wasm32
 *              (data, buffer) pointers -> prepareFinalFree()/commitFinalFree().
 *   - FLAGS=1  owned resource: reserve() -> bindOwnedResource() for backing
 *              allocated and owned by the caller (IO/demux). DATA and BUFFER
 *              stay 0 because no wasm32 payload exists, so the key-based
 *              sample final-free path can never match such a record; it is
 *              released only by releaseOwnedResource() after the caller
 *              really dropped its last alias or the allocator free returned.
 * Borrowed counters, capacity, ids and generations are identical for both
 * kinds; an owned record is never credited by the sample or unused-reclaim
 * paths.
 *
 * UMD / CommonJS, pure JS, no environment or credentials access.
 */
(function (root, factory) {
  'use strict';
  if (typeof module === 'object' && module && module.exports) {
    module.exports = factory();
  } else if (typeof define === 'function' && define.amd) {
    define([], factory);
  } else {
    root.EioSharedMediaOwnership = factory();
  }
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
  'use strict';

  var VERSION = 1;
  var KIND = 'eio-shared-media-ownership/v1';
  var TOKEN_BRAND = 'EioSharedMediaOwnership.token';
  var TICKET_BRAND = 'EioSharedMediaOwnership.ticket';
  var MAGIC = 0x534d4f31; /* "SMO1" */

  /* Entry FLAGS word. FLAGS=0 is a normal FFmpeg sample bound to real wasm32
   * data/buffer pointers. FLAGS=1 marks an owned-resource record whose backing
   * belongs to the caller: DATA and BUFFER must stay 0, so the key scan of
   * prepareFinalFree() (both pointers non-zero) can never match it. Any other
   * flag value is corrupt and fails closed instead of guessing a credit. */
  var FLAGS_NONE = 0;
  var FLAGS_OWNED_RESOURCE = 1;

  /* Capacity greater than this is rejected: held bytes must stay a positive
   * 32-bit safety integer. */
  var MAX_CAPACITY_BYTES = 300000000;
  var MAX_ENTRIES_LIMIT = 65536;
  /* Positive int32 ids; wrapping would allow ABA, so the counter fails closed. */
  var MAX_ID_COUNTER = 0x7ffffffd;
  /* Bounded budget for chargeHeld()/takeId() only; both fail closed with
   * 'busy'. raisePeak() is deliberately unbudgeted: its loop only repeats when
   * a peer published a strictly higher peak, so it has monotonic progress. */
  var MAX_CAS_RETRIES = 128;
  var PREPARE_SCAN_ROUNDS = 8;

  var HEADER_WORDS = 24;
  var ENTRY_WORDS = 8;

  var H = Object.freeze({
    MAGIC: 0,
    VERSION: 1,
    CAPACITY: 2,
    MAX_ENTRIES: 3,
    ENTRY_WORDS: 4,
    ENTRIES_OFFSET: 5,
    TOTAL_WORDS: 6,
    HELD: 7,
    PEAK: 8,
    RESERVED_BYTES: 9,
    BOUND_BYTES: 10,
    COUNT_RESERVED: 11,
    COUNT_BOUND: 12,
    COUNT_CLOSING: 13,
    ID_COUNTER: 14,
    INIT: 15,
    NOTIFY_SEQ: 16
  });

  var E = Object.freeze({
    STATE: 0,
    ID: 1,
    DATA: 2,
    BUFFER: 3,
    BYTES: 4,
    GENERATION: 5,
    FLAGS: 6,
    SPARE: 7
  });

  var STATE = Object.freeze({
    FREE: 0,
    RESERVING: 1,
    RESERVED: 2,
    BINDING: 3,
    BOUND: 4,
    CLOSING: 5,
    RECLAIMING: 6,
    COMMITTING: 7
  });

  var STATE_NAMES = Object.freeze({
    0: 'free',
    1: 'reserving',
    2: 'reserved',
    3: 'binding',
    4: 'bound',
    5: 'closing',
    6: 'reclaiming',
    7: 'committing'
  });

  function stateName(state) {
    var name = STATE_NAMES[state];
    return name === undefined ? 'unknown(' + String(state) + ')' : name;
  }

  function codedError(code, message, extra) {
    var error = new Error(message || code);
    error.code = code;
    if (extra) {
      Object.keys(extra).forEach(function (key) {
        error[key] = extra[key];
      });
    }
    return error;
  }

  function failure(code, message, extra) {
    var out = { ok: false, code: code, message: message || code };
    if (extra) {
      Object.keys(extra).forEach(function (key) {
        out[key] = extra[key];
      });
    }
    return out;
  }

  function isFiniteInteger(value) {
    return typeof value === 'number' && Number.isFinite(value) && Number.isInteger(value);
  }

  function isUint32(value) {
    return isFiniteInteger(value) && value >= 0 && value <= 0xffffffff;
  }

  function hasSharedMemory() {
    return typeof SharedArrayBuffer === 'function' &&
      typeof Atomics === 'object' && Atomics !== null &&
      typeof Atomics.load === 'function' &&
      typeof Atomics.compareExchange === 'function' &&
      typeof Atomics.wait === 'function' &&
      typeof Atomics.notify === 'function';
  }

  function isSharedArrayBuffer(value) {
    if (!hasSharedMemory() || !value) return false;
    try {
      return value instanceof SharedArrayBuffer ||
        Object.prototype.toString.call(value) === '[object SharedArrayBuffer]';
    } catch (error) {
      return false;
    }
  }

  function computeSpec(capacityBytes, maxEntries, errorCode) {
    var code = errorCode || 'invalid-argument';
    if (!isFiniteInteger(capacityBytes) || capacityBytes < 0 || capacityBytes > MAX_CAPACITY_BYTES) {
      throw codedError(code,
        'capacityBytes must be an integer in [0, ' + MAX_CAPACITY_BYTES + ']',
        { field: 'capacityBytes' });
    }
    if (!isFiniteInteger(maxEntries) || maxEntries < 1 || maxEntries > MAX_ENTRIES_LIMIT) {
      throw codedError(code,
        'maxEntries must be an integer in [1, ' + MAX_ENTRIES_LIMIT + ']',
        { field: 'maxEntries' });
    }
    var totalWords = HEADER_WORDS + maxEntries * ENTRY_WORDS;
    var byteLength = totalWords * 4;
    if (!Number.isSafeInteger(byteLength) || byteLength > 0x7fffffff) {
      throw codedError(code, 'shared layout is too large');
    }
    return Object.freeze({
      capacityBytes: capacityBytes,
      maxEntries: maxEntries,
      entryWords: ENTRY_WORDS,
      entriesOffset: HEADER_WORDS,
      totalWords: totalWords,
      byteLength: byteLength
    });
  }

  function entryOffset(spec, entryIndex) {
    return spec.entriesOffset + entryIndex * ENTRY_WORDS;
  }

  function makeToken(entryIndex, id, bytes, generation) {
    return Object.freeze({
      brand: TOKEN_BRAND,
      entry: entryIndex,
      id: id,
      bytes: bytes,
      generation: generation
    });
  }

  function makeTicket(entryIndex, id, data, buffer, bytes, generation) {
    return Object.freeze({
      brand: TICKET_BRAND,
      entry: entryIndex,
      id: id,
      data: data,
      buffer: buffer,
      bytes: bytes,
      generation: generation
    });
  }

  function checkToken(token, spec) {
    if (!token || typeof token !== 'object' || token.brand !== TOKEN_BRAND) {
      return failure('invalid-argument', 'token is not a reservation token');
    }
    if (!isFiniteInteger(token.entry) || token.entry < 0 || token.entry >= spec.maxEntries) {
      return failure('invalid-argument', 'token.entry is out of range', { field: 'entry' });
    }
    if (!isFiniteInteger(token.id) || token.id <= 0) {
      return failure('invalid-argument', 'token.id is invalid', { field: 'id' });
    }
    if (!isFiniteInteger(token.bytes) || token.bytes < 0) {
      return failure('invalid-argument', 'token.bytes is invalid', { field: 'bytes' });
    }
    return { ok: true, offset: entryOffset(spec, token.entry) };
  }

  function checkTicket(ticket, spec) {
    if (!ticket || typeof ticket !== 'object' || ticket.brand !== TICKET_BRAND) {
      return failure('invalid-argument', 'ticket is not a final-free ticket');
    }
    if (!isFiniteInteger(ticket.entry) || ticket.entry < 0 || ticket.entry >= spec.maxEntries) {
      return failure('invalid-argument', 'ticket.entry is out of range', { field: 'entry' });
    }
    if (!isFiniteInteger(ticket.id) || ticket.id <= 0) {
      return failure('invalid-argument', 'ticket.id is invalid', { field: 'id' });
    }
    if (!isFiniteInteger(ticket.bytes) || ticket.bytes < 0) {
      return failure('invalid-argument', 'ticket.bytes is invalid', { field: 'bytes' });
    }
    if (!isUint32(ticket.data) || ticket.data === 0 || !isUint32(ticket.buffer) || ticket.buffer === 0) {
      return failure('invalid-argument', 'ticket key pointers are invalid');
    }
    return { ok: true, offset: entryOffset(spec, ticket.entry) };
  }

  function checkKey(key) {
    if (!key || typeof key !== 'object') {
      return failure('invalid-argument', 'expected { data, buffer } wasm32 pointers');
    }
    if (!isUint32(key.data) || key.data === 0) {
      return failure('invalid-argument', 'data must be a non-zero wasm32 pointer', { field: 'data' });
    }
    if (!isUint32(key.buffer) || key.buffer === 0) {
      return failure('invalid-argument', 'buffer must be the non-zero AVBuffer metadata pointer, not a ref/packet pointer', { field: 'buffer' });
    }
    return { ok: true, data: key.data >>> 0, buffer: key.buffer >>> 0 };
  }

  function readUint32(view, wordOffset) {
    return Atomics.load(view, wordOffset) >>> 0;
  }

  function clearEntry(view, offset) {
    Atomics.store(view, offset + E.ID, 0);
    Atomics.store(view, offset + E.DATA, 0);
    Atomics.store(view, offset + E.BUFFER, 0);
    Atomics.store(view, offset + E.BYTES, 0);
    Atomics.store(view, offset + E.GENERATION, 0);
    Atomics.store(view, offset + E.FLAGS, 0);
    Atomics.store(view, offset + E.SPARE, 0);
    Atomics.store(view, offset + E.STATE, STATE.FREE);
  }

  function releaseSlot(view, offset) {
    clearEntry(view, offset);
  }

  /* Returns { ok: true, heldAfterCharge } or { ok: false, code }. Only the
   * caller's bytes are ever charged; a failed compare-exchange never changes
   * held. heldAfterCharge is the held value produced by the successful CAS,
   * so a peer free that lands before raisePeak() cannot hide the real peak. */
  function chargeHeld(view, spec, bytes) {
    for (var attempt = 0; attempt < MAX_CAS_RETRIES; attempt += 1) {
      var held = Atomics.load(view, H.HELD);
      var next = held + bytes;
      if (next > spec.capacityBytes) return { ok: false, code: 'capacity-exceeded' };
      if (Atomics.compareExchange(view, H.HELD, held, next) === held) {
        return { ok: true, heldAfterCharge: next };
      }
    }
    return { ok: false, code: 'busy' };
  }

  /* Monotonic CAS loop with no retry budget. Nothing in this module ever lowers
   * PEAK, and a failed compare-exchange returns the peer's newly installed peak,
   * which is used as the expected value of the next attempt. Each failure
   * therefore strictly increases the observed peak, and peak is bounded by the
   * capacity (<= MAX_CAPACITY_BYTES), so the loop makes finite monotonic
   * progress and cannot silently drop a real peak the way a fixed retry budget
   * could. This is not a budget-based busy poll: it only repeats when a peer
   * actually published a higher peak. */
  function raisePeak(view, value) {
    var peak = Atomics.load(view, H.PEAK);
    while (peak < value) {
      var observed = Atomics.compareExchange(view, H.PEAK, peak, value);
      if (observed === peak) return;
      peak = observed;
    }
  }

  function takeId(view) {
    for (var attempt = 0; attempt < MAX_CAS_RETRIES; attempt += 1) {
      var current = Atomics.load(view, H.ID_COUNTER);
      if (current >= MAX_ID_COUNTER) return { ok: false, code: 'id-exhausted' };
      if (Atomics.compareExchange(view, H.ID_COUNTER, current, current + 1) === current) {
        return { ok: true, id: current + 1 };
      }
    }
    return { ok: false, code: 'busy' };
  }

  function claimFreeEntry(view, spec) {
    for (var index = 0; index < spec.maxEntries; index += 1) {
      var offset = entryOffset(spec, index);
      if (Atomics.load(view, offset + E.STATE) === STATE.FREE &&
          Atomics.compareExchange(view, offset + E.STATE, STATE.FREE, STATE.RESERVING) === STATE.FREE) {
        return offset;
      }
    }
    return -1;
  }

  function notifyChange(view) {
    Atomics.add(view, H.NOTIFY_SEQ, 1);
    Atomics.notify(view, H.NOTIFY_SEQ);
  }

  /* Identity/integrity check for an owned-resource (FLAGS=1) record. Used both
   * before the BOUND->COMMITTING claim and again after it, so a slot that was
   * recycled or corrupted mid-release can never be credited. Returns a failure
   * object or null. */
  function ownedRecordMismatch(view, offset, handle) {
    if (Atomics.load(view, offset + E.ID) !== handle.id) {
      return failure('stale-token',
        'owned record id no longer matches the handle; refusing to credit');
    }
    if (Atomics.load(view, offset + E.BYTES) !== handle.bytes) {
      return failure('wrong-state',
        'owned record bytes do not match the handle; refusing to credit');
    }
    if (Atomics.load(view, offset + E.GENERATION) !== handle.generation) {
      return failure('stale-token',
        'owned record generation no longer matches the handle; refusing to credit');
    }
    var flags = Atomics.load(view, offset + E.FLAGS);
    if (flags !== FLAGS_OWNED_RESOURCE) {
      if (flags === FLAGS_NONE) {
        return failure('not-owned',
          'record is a normal sample record (FLAGS=0); only FLAGS=1 owned resources release here',
          { flags: flags });
      }
      return failure('corrupt-record', 'record FLAGS is not recognized; failing closed',
        { flags: flags });
    }
    if (readUint32(view, offset + E.DATA) !== 0 || readUint32(view, offset + E.BUFFER) !== 0) {
      return failure('corrupt-record',
        'owned record carries non-zero data/buffer pointers; failing closed');
    }
    return null;
  }

  function makeOwnedHandle(entryIndex, id, bytes, generation) {
    return Object.freeze({
      brand: TOKEN_BRAND,
      owned: true,
      entry: entryIndex,
      id: id,
      bytes: bytes,
      generation: generation
    });
  }

  function tokenMismatch(view, offset, token, boundCode) {
    var state = Atomics.load(view, offset + E.STATE);
    var id = Atomics.load(view, offset + E.ID);
    if (id !== token.id) return 'stale-token';
    if (state === STATE.BOUND || state === STATE.CLOSING || state === STATE.COMMITTING) {
      return boundCode || 'bound';
    }
    if (state === STATE.RESERVED) return 'busy';
    return 'stale-token';
  }

  /* Key collision check for bind(). Every already BOUND/CLOSING/COMMITTING
   * record with the same key conflicts. A BINDING record with the same key
   * also conflicts conservatively: two concurrent binders may both lose, but
   * they must never both publish the same live key. */
  function findKeyCollision(view, spec, ownOffset, data, buffer) {
    for (var index = 0; index < spec.maxEntries; index += 1) {
      var offset = entryOffset(spec, index);
      if (offset === ownOffset) continue;
      var state = Atomics.load(view, offset + E.STATE);
      if (state !== STATE.BINDING && state !== STATE.BOUND &&
          state !== STATE.CLOSING && state !== STATE.COMMITTING) {
        continue;
      }
      if (readUint32(view, offset + E.DATA) !== data) continue;
      if (readUint32(view, offset + E.BUFFER) !== buffer) continue;
      return { entry: index, reason: state === STATE.BINDING ? 'binding' : 'bound' };
    }
    return null;
  }

  /* Nontransactional slot scan for owned-resource (FLAGS=1) records. It is a
   * best-effort observation: it can tear across a concurrent release, so it is
   * labelled as such and never used to decide a credit. */
  function scanOwnedRecords(view, spec) {
    var bytes = 0;
    var count = 0;
    for (var index = 0; index < spec.maxEntries; index += 1) {
      var offset = entryOffset(spec, index);
      var state = Atomics.load(view, offset + E.STATE);
      if (state !== STATE.BOUND && state !== STATE.CLOSING && state !== STATE.COMMITTING) {
        continue;
      }
      if (Atomics.load(view, offset + E.FLAGS) !== FLAGS_OWNED_RESOURCE) continue;
      bytes += Atomics.load(view, offset + E.BYTES);
      count += 1;
    }
    return { bytes: bytes, count: count };
  }

  function describe(spec, view) {
    return Object.freeze({
      kind: KIND,
      version: VERSION,
      capacityBytes: spec.capacityBytes,
      maxEntries: spec.maxEntries,
      entryWords: spec.entryWords,
      entriesOffset: spec.entriesOffset,
      totalWords: spec.totalWords,
      byteLength: spec.byteLength,
      sab: view.buffer
    });
  }

  function Controller(spec, view) {
    this._spec = spec;
    this._view = view;
    Object.freeze(this);
  }

  Object.defineProperty(Controller.prototype, 'descriptor', {
    enumerable: true,
    get: function () {
      return describe(this._spec, this._view);
    }
  });

  Controller.prototype.snapshot = function snapshot() {
    var view = this._view;
    var spec = this._spec;
    var owned = scanOwnedRecords(view, spec);
    return Object.freeze({
      kind: KIND,
      version: VERSION,
      capacityBytes: spec.capacityBytes,
      maxEntries: spec.maxEntries,
      heldBytes: Atomics.load(view, H.HELD),
      peakBytes: Atomics.load(view, H.PEAK),
      reservedBytes: Atomics.load(view, H.RESERVED_BYTES),
      boundBytes: Atomics.load(view, H.BOUND_BYTES),
      countReserved: Atomics.load(view, H.COUNT_RESERVED),
      countBound: Atomics.load(view, H.COUNT_BOUND),
      countClosing: Atomics.load(view, H.COUNT_CLOSING),
      /* FLAGS=1 ownership of non-sample backing, observed by a nontransactional
       * slot scan: informational only, never a credit decision. */
      ownedResourceBytes: owned.bytes,
      countOwnedResources: owned.count,
      ownedResourceScan: 'nontransactional',
      idCounter: Atomics.load(view, H.ID_COUNTER),
      notifySequence: Atomics.load(view, H.NOTIFY_SEQ)
    });
  };

  Controller.prototype.heldBytes = function heldBytes() {
    return Atomics.load(this._view, H.HELD);
  };

  Controller.prototype.peakBytes = function peakBytes() {
    return Atomics.load(this._view, H.PEAK);
  };

  Controller.prototype.reserve = function reserve(bytes, generation) {
    var spec = this._spec;
    var view = this._view;
    if (!isFiniteInteger(bytes) || bytes < 0 || bytes > MAX_CAPACITY_BYTES) {
      return failure('invalid-argument',
        'bytes must be an integer in [0, ' + MAX_CAPACITY_BYTES + ']',
        { field: 'bytes' });
    }
    var gen = generation === undefined ? 0 : generation;
    if (!isFiniteInteger(gen) || gen < 0 || gen > 0x7fffffff) {
      return failure('invalid-argument', 'generation must be an integer in [0, 2147483647]', { field: 'generation' });
    }
    if (bytes > spec.capacityBytes) {
      return failure('capacity-exceeded', 'reservation exceeds shared capacity',
        { bytes: bytes, capacityBytes: spec.capacityBytes });
    }

    var offset = claimFreeEntry(view, spec);
    if (offset < 0) {
      return failure('entries-exhausted', 'no free ownership record entry remains',
        { maxEntries: spec.maxEntries });
    }

    /* Claim the slot, take the monotonic id, then charge. An id failure
     * releases the slot without touching held. The id counter is deliberately
     * not rolled back (no ABA); a failed reservation may consume an id. */
    var idResult = takeId(view);
    if (!idResult.ok) {
      releaseSlot(view, offset);
      return failure(idResult.code, 'ownership record id space is exhausted; failing closed');
    }
    var id = idResult.id;

    var charged = chargeHeld(view, spec, bytes);
    if (!charged.ok) {
      releaseSlot(view, offset);
      if (charged.code === 'capacity-exceeded') {
        return failure('capacity-exceeded', 'reservation exceeds shared capacity',
          { bytes: bytes, capacityBytes: spec.capacityBytes });
      }
      return failure('busy', 'shared held counter was contended; reservation was not charged');
    }

    /* Use the held value captured by the successful charge CAS. Loading HELD
     * here would let a peer free between charge and peak raise hide the peak.
     * Capacity failures happen before any charge and cannot change peak. */
    raisePeak(view, charged.heldAfterCharge);
    Atomics.store(view, offset + E.ID, id);
    Atomics.store(view, offset + E.DATA, 0);
    Atomics.store(view, offset + E.BUFFER, 0);
    Atomics.store(view, offset + E.BYTES, bytes);
    Atomics.store(view, offset + E.GENERATION, gen);
    Atomics.store(view, offset + E.FLAGS, 0);
    Atomics.store(view, offset + E.SPARE, 0);
    Atomics.add(view, H.RESERVED_BYTES, bytes);
    Atomics.add(view, H.COUNT_RESERVED, 1);
    Atomics.store(view, offset + E.STATE, STATE.RESERVED);

    return {
      ok: true,
      token: makeToken((offset - spec.entriesOffset) / ENTRY_WORDS, id, bytes, gen),
      heldBytes: Atomics.load(view, H.HELD)
    };
  };

  Controller.prototype.bind = function bind(token, payload) {
    var spec = this._spec;
    var view = this._view;
    var tokenCheck = checkToken(token, spec);
    if (!tokenCheck.ok) return tokenCheck;
    if (!payload || typeof payload !== 'object') {
      return failure('invalid-argument', 'bind requires { data, buffer } wasm32 pointers');
    }
    if (!isUint32(payload.data) || payload.data === 0) {
      return failure('invalid-argument', 'payload.data must be a non-zero wasm32 payload pointer', { field: 'data' });
    }
    if (!isUint32(payload.buffer) || payload.buffer === 0) {
      return failure('invalid-argument', 'payload.buffer must be the non-zero AVBuffer metadata pointer (not a ref/packet pointer)', { field: 'buffer' });
    }
    if (payload.size !== undefined &&
        (!isFiniteInteger(payload.size) || payload.size !== token.bytes)) {
      return failure('invalid-argument', 'payload.size must equal the reserved bytes', { field: 'size' });
    }

    var offset = tokenCheck.offset;
    var data = payload.data >>> 0;
    var buffer = payload.buffer >>> 0;

    /* Take exclusive ownership of the slot before writing any key fields. A
     * stale/duplicate caller must never overwrite the current owner's key. */
    var claimed = Atomics.compareExchange(view, offset + E.STATE, STATE.RESERVED, STATE.BINDING);
    if (claimed !== STATE.RESERVED) {
      return failure(tokenMismatch(view, offset, token, 'already-bound'),
        'reservation is no longer in the RESERVED state');
    }

    /* We hold BINDING. Validate the record identity before publishing a key.
     * On mismatch restore the pre-CAS RESERVED state without touching the
     * key fields, which may belong to the current owner of a reused slot. */
    if (Atomics.load(view, offset + E.ID) !== token.id ||
        Atomics.load(view, offset + E.BYTES) !== token.bytes ||
        Atomics.load(view, offset + E.GENERATION) !== token.generation) {
      Atomics.store(view, offset + E.STATE, STATE.RESERVED);
      return failure('stale-token', 'reservation id/bytes/generation changed (ABA); bind refused');
    }

    Atomics.store(view, offset + E.DATA, payload.data | 0);
    Atomics.store(view, offset + E.BUFFER, payload.buffer | 0);

    var collision = findKeyCollision(view, spec, offset, data, buffer);
    if (collision) {
      Atomics.store(view, offset + E.DATA, 0);
      Atomics.store(view, offset + E.BUFFER, 0);
      Atomics.store(view, offset + E.STATE, STATE.RESERVED);
      return failure('duplicate-key',
        'another ownership record already owns this (data, buffer) key',
        { conflictingEntry: collision.entry, reason: collision.reason });
    }

    Atomics.sub(view, H.RESERVED_BYTES, token.bytes);
    Atomics.sub(view, H.COUNT_RESERVED, 1);
    Atomics.add(view, H.BOUND_BYTES, token.bytes);
    Atomics.add(view, H.COUNT_BOUND, 1);
    Atomics.store(view, offset + E.STATE, STATE.BOUND);

    return {
      ok: true,
      key: { data: data, buffer: buffer },
      heldBytes: Atomics.load(view, H.HELD)
    };
  };

  /* Bind a reservation to a caller-owned backing allocation.
   *
   * This is the FLAGS=1 counterpart of bind(): the caller charged real bytes
   * for backing it allocates itself (WASM heap block, IO source buffer, demux
   * i/o buffer), so there is no wasm32 payload/metadata pair to publish. DATA
   * and BUFFER therefore stay 0 and FLAGS=1 marks the record kind. HELD is not
   * touched here: the reservation already charged it and the transfer only
   * moves the reserved counters to the bound counters, so a successful
   * bindOwnedResource() is neither a new charge nor a credit.
   *
   * Same protection as bind(): the slot is claimed BOUND<->RESERVED via CAS,
   * and id/bytes/generation are re-verified while BINDING. A corrupt FLAGS word
   * or a non-zero key word on a supposedly clean reservation fails closed and
   * leaves the charge in place (the caller still holds the reservation token
   * for reclaimUnused). */
  Controller.prototype.bindOwnedResource = function bindOwnedResource(token) {
    var spec = this._spec;
    var view = this._view;
    var tokenCheck = checkToken(token, spec);
    if (!tokenCheck.ok) return tokenCheck;
    if (!isFiniteInteger(token.generation) || token.generation < 0) {
      return failure('invalid-argument', 'token.generation is invalid', { field: 'generation' });
    }
    var offset = tokenCheck.offset;

    if (Atomics.compareExchange(view, offset + E.STATE, STATE.RESERVED, STATE.BINDING) !== STATE.RESERVED) {
      return failure(tokenMismatch(view, offset, token, 'already-bound'),
        'reservation is no longer in the RESERVED state');
    }

    /* Still BINDING: verify identity, then require a clean unbound record
     * before publishing FLAGS=1. BINDING keeps peers out of the slot. */
    if (Atomics.load(view, offset + E.ID) !== token.id ||
        Atomics.load(view, offset + E.BYTES) !== token.bytes ||
        Atomics.load(view, offset + E.GENERATION) !== token.generation) {
      Atomics.store(view, offset + E.STATE, STATE.RESERVED);
      return failure('stale-token',
        'reservation id/bytes/generation changed (ABA); owned bind refused');
    }
    var flags = Atomics.load(view, offset + E.FLAGS);
    if (flags !== FLAGS_NONE ||
        readUint32(view, offset + E.DATA) !== 0 ||
        readUint32(view, offset + E.BUFFER) !== 0) {
      Atomics.store(view, offset + E.STATE, STATE.RESERVED);
      return failure('corrupt-record',
        'reservation is not a clean unbound record; owned bind refused and the charge kept',
        { flags: flags });
    }

    Atomics.store(view, offset + E.FLAGS, FLAGS_OWNED_RESOURCE);
    Atomics.sub(view, H.RESERVED_BYTES, token.bytes);
    Atomics.sub(view, H.COUNT_RESERVED, 1);
    Atomics.add(view, H.BOUND_BYTES, token.bytes);
    Atomics.add(view, H.COUNT_BOUND, 1);
    Atomics.store(view, offset + E.STATE, STATE.BOUND);

    return {
      ok: true,
      handle: makeOwnedHandle((offset - spec.entriesOffset) / ENTRY_WORDS,
        token.id, token.bytes, token.generation),
      heldBytes: Atomics.load(view, H.HELD)
    };
  };

  /* Release one owned-resource (FLAGS=1) record, exactly once.
   *
   * Preconditions, all checked and refused without any credit:
   *   - the handle must be a token-shaped owned handle (not a forged object);
   *   - proof.referencesDropped === true, i.e. the caller has really dropped
   *     its last controlled alias (or the allocator free already returned);
   *   - the record must still be BOUND and match id/bytes/generation/FLAGS and
   *     keep DATA=BUFFER=0.
   * The record is claimed with CAS BOUND->COMMITTING and the same
   * identity/FLAGS/zero-pointer checks run again afterwards; a mismatch reverts
   * to BOUND without crediting. Only the successful claim releases the slot and
   * subtracts HELD/BOUND_BYTES once and COUNT_BOUND once (no clamping, no
   * inference from a stale count). Single use follows from the freed slot plus
   * monotonic ids: a replay is 'stale-token' and never credits twice.
   *
   * Normal sample records (FLAGS=0) are refused as 'not-owned'; they may only
   * be credited by commitFinalFree() after the real data free. An unbound
   * reservation is refused by reclaimUnused(); this method never reclaims. */
  Controller.prototype.releaseOwnedResource = function releaseOwnedResource(handle, proof) {
    var spec = this._spec;
    var view = this._view;
    var handleCheck = checkToken(handle, spec);
    if (!handleCheck.ok) return handleCheck;
    if (!isFiniteInteger(handle.generation) || handle.generation < 0) {
      return failure('invalid-argument', 'handle.generation is invalid', { field: 'generation' });
    }
    if (!proof || proof.referencesDropped !== true) {
      return failure('not-dropped',
        'releaseOwnedResource requires proof that every controlled alias was dropped ({ referencesDropped: true })');
    }
    var offset = handleCheck.offset;

    if (Atomics.load(view, offset + E.ID) !== handle.id) {
      return failure('stale-token',
        'owned record id no longer matches the handle; refusing to credit');
    }
    var state = Atomics.load(view, offset + E.STATE);
    if (state !== STATE.BOUND) {
      if (state === STATE.CLOSING || state === STATE.COMMITTING) {
        return failure('busy', 'owned record is already being released; no bytes were credited');
      }
      return failure(tokenMismatch(view, offset, handle),
        'owned record is not BOUND; release refused without credit');
    }
    var pre = ownedRecordMismatch(view, offset, handle);
    if (pre) return pre;

    if (Atomics.compareExchange(view, offset + E.STATE, STATE.BOUND, STATE.COMMITTING) !== STATE.BOUND) {
      return failure('busy', 'owned record changed while releasing; no bytes were credited');
    }
    var post = ownedRecordMismatch(view, offset, handle);
    if (post) {
      Atomics.compareExchange(view, offset + E.STATE, STATE.COMMITTING, STATE.BOUND);
      return post;
    }

    var bytes = Atomics.load(view, offset + E.BYTES);
    releaseSlot(view, offset);
    Atomics.sub(view, H.HELD, bytes);
    Atomics.sub(view, H.BOUND_BYTES, bytes);
    Atomics.sub(view, H.COUNT_BOUND, 1);
    notifyChange(view);

    return {
      ok: true,
      releasedBytes: bytes,
      heldBytes: Atomics.load(view, H.HELD)
    };
  };

  Controller.prototype.reclaimUnused = function reclaimUnused(token) {
    var spec = this._spec;
    var view = this._view;
    var tokenCheck = checkToken(token, spec);
    if (!tokenCheck.ok) return tokenCheck;
    var offset = tokenCheck.offset;
    /* Verify identity before interpreting the state: a stale token that lands
     * on a reused BOUND slot must be stale-token, not bound. */
    if (Atomics.load(view, offset + E.ID) !== token.id) {
      return failure('stale-token', 'reservation id no longer matches the token; refusing to credit');
    }
    var state = Atomics.load(view, offset + E.STATE);
    if (state === STATE.BOUND || state === STATE.CLOSING || state === STATE.COMMITTING) {
      return failure('bound', 'a bound/closing record can only be released by the final-free path');
    }
    if (state !== STATE.RESERVED) {
      return failure(tokenMismatch(view, offset, token),
        'reservation is not an unbound matching reservation; refusing to credit');
    }
    if (Atomics.compareExchange(view, offset + E.STATE, STATE.RESERVED, STATE.RECLAIMING) !== STATE.RESERVED) {
      return failure('busy', 'reservation changed while reclaiming');
    }
    if (Atomics.load(view, offset + E.ID) !== token.id) {
      Atomics.store(view, offset + E.STATE, STATE.RESERVED);
      return failure('stale-token', 'reservation id changed (ABA); reclaim refused');
    }
    var bytes = Atomics.load(view, offset + E.BYTES);
    if (bytes !== token.bytes) {
      Atomics.store(view, offset + E.STATE, STATE.RESERVED);
      return failure('wrong-state', 'reservation bytes do not match token; refusing to credit');
    }

    releaseSlot(view, offset);
    Atomics.sub(view, H.HELD, bytes);
    Atomics.sub(view, H.RESERVED_BYTES, bytes);
    Atomics.sub(view, H.COUNT_RESERVED, 1);
    notifyChange(view);

    return {
      ok: true,
      reclaimedBytes: bytes,
      heldBytes: Atomics.load(view, H.HELD)
    };
  };

  /* Non-mutating observation of one reservation token's retirement.
   *
   * This is the API prerequisite for reconciling an outbound sample only after
   * its shared token retires. On its own it is neither budget proof nor free
   * proof.
   *
   * It inspects the token's original slot only (never a key scan), performs one
   * bounded double-read of id/state/bytes/generation, and never mutates,
   * credits, reclaims, frees, waits or notifies. It returns no payload
   * pointers and it is not a transactional snapshot.
   *
   * retired:true means only that the token retired: the slot id changed or the
   * slot is FREE again. An unbound reservation can retire by cancellation
   * (reclaimUnused), so a caller may read this as evidence that its controlled
   * payload went through the verified final-free protocol only if it
   * separately knows that this token was successfully BOUND.
   *
   * Fail-closed details: a torn double-read is 'busy' and is never reported as
   * retired; an unrecognized state is 'wrong-state' even when the id differs;
   * live bytes/generation that no longer match the token are 'busy' because
   * clearEntry() may be in progress. No release is inferred from torn multiword
   * fields, and ids are monotonic, so an old identity cannot resurrect.
   */
  Controller.prototype.inspectReservation = function inspectReservation(token) {
    var spec = this._spec;
    var view = this._view;
    var tokenCheck = checkToken(token, spec);
    if (!tokenCheck.ok) return tokenCheck;
    var offset = tokenCheck.offset;

    /* One bounded attempt: no retry loop and no wait. */
    var id1 = Atomics.load(view, offset + E.ID);
    var state1 = Atomics.load(view, offset + E.STATE);
    var bytes = Atomics.load(view, offset + E.BYTES);
    var generation = Atomics.load(view, offset + E.GENERATION);
    var id2 = Atomics.load(view, offset + E.ID);
    var state2 = Atomics.load(view, offset + E.STATE);
    if (id1 !== id2 || state1 !== state2) {
      return failure('busy', 'ownership record changed while inspecting; no retirement was observed');
    }

    /* Require a recognized state before drawing any conclusion: an unknown
     * state can be a transition in progress and must never infer retirement. */
    if (STATE_NAMES[state1] === undefined) {
      return failure('wrong-state', 'ownership record state is not recognized', { state: state1 });
    }

    if (id1 !== token.id || state1 === STATE.FREE) {
      return Object.freeze({
        ok: true,
        retired: true,
        state: 'retired',
        reason: id1 !== token.id ? 'id-changed' : 'slot-free'
      });
    }

    /* A recognized non-FREE state is one of the live states. Keep the check
     * explicit so a future enum addition cannot silently become 'live'. */
    if (state1 < STATE.RESERVING || state1 > STATE.COMMITTING) {
      return failure('wrong-state', 'ownership record is not in a recognized live state', { state: state1 });
    }
    if (bytes !== token.bytes || generation !== token.generation) {
      return failure('busy', 'ownership record fields do not match the token yet; a clear or publish may be in progress');
    }

    return Object.freeze({
      ok: true,
      retired: false,
      state: stateName(state1),
      bytes: token.bytes,
      generation: token.generation
    });
  };

  Controller.prototype.prepareFinalFree = function prepareFinalFree(key) {
    var spec = this._spec;
    var view = this._view;
    var keyCheck = checkKey(key);
    if (!keyCheck.ok) return keyCheck;
    var data = keyCheck.data;
    var buffer = keyCheck.buffer;

    for (var round = 0; round < PREPARE_SCAN_ROUNDS; round += 1) {
      var found = -1;
      var closing = false;
      for (var index = 0; index < spec.maxEntries; index += 1) {
        var offset = entryOffset(spec, index);
        var state = Atomics.load(view, offset + E.STATE);
        if (state !== STATE.BOUND && state !== STATE.CLOSING && state !== STATE.COMMITTING) {
          continue;
        }
        if (readUint32(view, offset + E.DATA) !== data) continue;
        if (readUint32(view, offset + E.BUFFER) !== buffer) continue;
        if (state === STATE.BOUND) {
          found = offset;
          break;
        }
        closing = true;
      }
      if (found < 0) {
        if (closing) {
          return failure('already-closing', 'this record already has a final-free ticket');
        }
        return failure('unknown-record', 'no bound ownership record matches this (data, buffer) key');
      }

      var id = Atomics.load(view, found + E.ID);
      if (id <= 0) continue;
      if (Atomics.compareExchange(view, found + E.STATE, STATE.BOUND, STATE.CLOSING) !== STATE.BOUND) {
        continue; /* bounded rescan; another final free may have won */
      }
      /* The scan can tear across slot reuse. After claiming CLOSING, require
       * id, data and buffer to still match the captured key. Any mismatch
       * reverts to BOUND and rescans; never return a ticket for a new slot. */
      if (Atomics.load(view, found + E.ID) !== id ||
          readUint32(view, found + E.DATA) !== data ||
          readUint32(view, found + E.BUFFER) !== buffer) {
        Atomics.compareExchange(view, found + E.STATE, STATE.CLOSING, STATE.BOUND);
        continue;
      }
      var bytes = Atomics.load(view, found + E.BYTES);
      Atomics.add(view, H.COUNT_CLOSING, 1);
      return {
        ok: true,
        ticket: makeTicket((found - spec.entriesOffset) / ENTRY_WORDS, id, data, buffer, bytes,
          Atomics.load(view, found + E.GENERATION)),
        heldBytes: Atomics.load(view, H.HELD)
      };
    }
    return failure('busy', 'record state was contended while preparing the final free');
  };

  Controller.prototype.commitFinalFree = function commitFinalFree(ticket, evidence) {
    var spec = this._spec;
    var view = this._view;
    var ticketCheck = checkTicket(ticket, spec);
    if (!ticketCheck.ok) return ticketCheck;
    /* A missing/undefined evidence is not-freed. Callers must pass the
     * explicit integration protocol { dataFreed: true } after the real free;
     * this flag is evidence from the call site, not proof by itself. */
    if (!evidence || evidence.dataFreed !== true) {
      return failure('not-freed', 'commitFinalFree requires the actual data free first ({ dataFreed: true })');
    }

    var offset = ticketCheck.offset;
    if (Atomics.load(view, offset + E.STATE) !== STATE.CLOSING ||
        Atomics.load(view, offset + E.ID) !== ticket.id) {
      return failure('stale-ticket', 'final-free ticket no longer matches a closing record');
    }
    if (Atomics.compareExchange(view, offset + E.STATE, STATE.CLOSING, STATE.COMMITTING) !== STATE.CLOSING) {
      return failure('stale-ticket', 'final-free ticket lost the closing race');
    }
    if (Atomics.load(view, offset + E.ID) !== ticket.id ||
        readUint32(view, offset + E.DATA) !== ticket.data ||
        readUint32(view, offset + E.BUFFER) !== ticket.buffer) {
      Atomics.store(view, offset + E.STATE, STATE.CLOSING);
      return failure('stale-ticket', 'record id/key changed while committing; no bytes were credited');
    }
    var bytes = Atomics.load(view, offset + E.BYTES);
    if (bytes !== ticket.bytes) {
      Atomics.store(view, offset + E.STATE, STATE.CLOSING);
      return failure('wrong-state', 'record bytes do not match ticket; no bytes were credited');
    }

    releaseSlot(view, offset);
    Atomics.sub(view, H.HELD, bytes);
    Atomics.sub(view, H.BOUND_BYTES, bytes);
    Atomics.sub(view, H.COUNT_BOUND, 1);
    Atomics.sub(view, H.COUNT_CLOSING, 1);
    notifyChange(view);

    return {
      ok: true,
      releasedBytes: bytes,
      heldBytes: Atomics.load(view, H.HELD)
    };
  };

  Controller.prototype.lookup = function lookup(key) {
    var spec = this._spec;
    var view = this._view;
    var keyCheck = checkKey(key);
    if (!keyCheck.ok) {
      return { ok: false, code: keyCheck.code, message: keyCheck.message, found: false };
    }
    for (var index = 0; index < spec.maxEntries; index += 1) {
      var offset = entryOffset(spec, index);
      var state = Atomics.load(view, offset + E.STATE);
      if (state !== STATE.BINDING && state !== STATE.BOUND &&
          state !== STATE.CLOSING && state !== STATE.COMMITTING) {
        continue;
      }
      if (readUint32(view, offset + E.DATA) !== keyCheck.data) continue;
      if (readUint32(view, offset + E.BUFFER) !== keyCheck.buffer) continue;
      return Object.freeze({
        ok: true,
        found: true,
        entry: index,
        state: stateName(state),
        id: Atomics.load(view, offset + E.ID),
        bytes: Atomics.load(view, offset + E.BYTES),
        generation: Atomics.load(view, offset + E.GENERATION)
      });
    }
    return Object.freeze({ ok: true, found: false });
  };

  Controller.prototype.waitForChange = function waitForChange(previousSequence, timeoutMs) {
    var view = this._view;
    if (!isFiniteInteger(previousSequence) || previousSequence < 0) {
      return failure('invalid-argument', 'previousSequence must be a non-negative integer', { field: 'previousSequence' });
    }
    var timeout = timeoutMs === undefined ? 0 : timeoutMs;
    if (!isFiniteInteger(timeout) || timeout < 0 || timeout > 5000) {
      return failure('invalid-argument', 'timeoutMs must be an integer in [0, 5000]', { field: 'timeoutMs' });
    }
    var changed = Atomics.load(view, H.NOTIFY_SEQ) !== previousSequence;
    if (!changed && timeout > 0) {
      Atomics.wait(view, H.NOTIFY_SEQ, previousSequence, timeout);
      changed = Atomics.load(view, H.NOTIFY_SEQ) !== previousSequence;
    }
    return {
      ok: true,
      changed: changed,
      sequence: Atomics.load(view, H.NOTIFY_SEQ),
      heldBytes: Atomics.load(view, H.HELD),
      peakBytes: Atomics.load(view, H.PEAK)
    };
  };

  function create(options) {
    if (!hasSharedMemory()) {
      throw codedError('unsupported',
        'SharedArrayBuffer and Atomics are required; no non-shared fallback exists');
    }
    if (!options || typeof options !== 'object') {
      throw codedError('invalid-argument', 'create requires { capacityBytes, maxEntries }');
    }
    var spec = computeSpec(options.capacityBytes, options.maxEntries, 'invalid-argument');
    var sab;
    try {
      sab = new SharedArrayBuffer(spec.byteLength);
    } catch (error) {
      throw codedError('unsupported', 'unable to allocate the shared ownership buffer',
        { cause: String(error && error.message || error) });
    }
    var view = new Int32Array(sab);
    view.fill(0);
    Atomics.store(view, H.MAGIC, MAGIC);
    Atomics.store(view, H.VERSION, VERSION);
    Atomics.store(view, H.CAPACITY, spec.capacityBytes);
    Atomics.store(view, H.MAX_ENTRIES, spec.maxEntries);
    Atomics.store(view, H.ENTRY_WORDS, spec.entryWords);
    Atomics.store(view, H.ENTRIES_OFFSET, spec.entriesOffset);
    Atomics.store(view, H.TOTAL_WORDS, spec.totalWords);
    Atomics.store(view, H.INIT, 1);
    return new Controller(spec, view);
  }

  function attach(descriptor) {
    if (!hasSharedMemory()) {
      throw codedError('unsupported',
        'SharedArrayBuffer and Atomics are required; no non-shared fallback exists');
    }
    if (!descriptor || typeof descriptor !== 'object') {
      throw codedError('invalid-descriptor', 'attach requires a shared ownership descriptor');
    }
    var sab;
    try {
      sab = descriptor.sab;
    } catch (error) {
      throw codedError('invalid-descriptor', 'descriptor.sab is not readable');
    }
    if (!isSharedArrayBuffer(sab)) {
      throw codedError('unsupported',
        'descriptor.sab must be a SharedArrayBuffer; refusing a non-shared substitute');
    }
    if (descriptor.kind !== KIND || descriptor.version !== VERSION) {
      throw codedError('invalid-descriptor', 'descriptor kind/version mismatch',
        { expectedKind: KIND, expectedVersion: VERSION });
    }
    var spec = computeSpec(descriptor.capacityBytes, descriptor.maxEntries, 'invalid-descriptor');
    if (sab.byteLength !== spec.byteLength ||
        (descriptor.totalWords !== undefined && descriptor.totalWords !== spec.totalWords) ||
        (descriptor.entriesOffset !== undefined && descriptor.entriesOffset !== spec.entriesOffset) ||
        (descriptor.entryWords !== undefined && descriptor.entryWords !== spec.entryWords)) {
      throw codedError('invalid-descriptor', 'descriptor geometry does not match its SharedArrayBuffer');
    }
    var view = new Int32Array(sab);
    if (Atomics.load(view, H.MAGIC) !== MAGIC ||
        Atomics.load(view, H.VERSION) !== VERSION ||
        Atomics.load(view, H.INIT) !== 1 ||
        (Atomics.load(view, H.CAPACITY) >>> 0) !== spec.capacityBytes ||
        Atomics.load(view, H.MAX_ENTRIES) !== spec.maxEntries ||
        Atomics.load(view, H.TOTAL_WORDS) !== spec.totalWords) {
      throw codedError('invalid-descriptor', 'shared ownership header is not initialized or is inconsistent');
    }
    return new Controller(spec, view);
  }

  return Object.freeze({
    VERSION: VERSION,
    KIND: KIND,
    MAX_CAPACITY_BYTES: MAX_CAPACITY_BYTES,
    MAX_ENTRIES: MAX_ENTRIES_LIMIT,
    STATE: STATE,
    LAYOUT: Object.freeze({
      headerWords: HEADER_WORDS,
      entryWords: ENTRY_WORDS,
      header: H,
      entry: E,
      flags: Object.freeze({
        NONE: FLAGS_NONE,
        OWNED_RESOURCE: FLAGS_OWNED_RESOURCE
      }),
      tokenBrand: TOKEN_BRAND,
      ticketBrand: TICKET_BRAND
    }),
    create: create,
    attach: attach
  });
});
