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
      tokenBrand: TOKEN_BRAND,
      ticketBrand: TICKET_BRAND
    }),
    create: create,
    attach: attach
  });
});
