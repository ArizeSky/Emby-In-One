"use strict";
/* Media-resource handle facade; canonical UMD copied from
   web-src/player-media-resource-handles.js. */
(self.webpackChunkAVPlayer=self.webpackChunkAVPlayer||[]).push([[90213],{
90213:(module,exports,__webpack_require__)=>{
/*!
 * player-media-resource-handles.js
 *
 * One lifecycle facade for the three controlled-media paths that currently
 * keep parallel state machines: MP4/Matroska owned resources, codecpar
 * leases, and FFmpeg sample reservations.
 *
 * The facade never owns a second ledger. Every byte is charged by the shared
 * ownership controller; this module only records the local lifecycle, binds a
 * native identity, and forwards the real release/final-free evidence. A handle
 * can be acquired directly (sample/owned) or adopted after another component
 * already obtained the core handle (owned).
 *
 * The demux sample hooks use it through module 90213; the other parsers keep
 * their existing call sites until each migration step moves them over.
 *
 * UMD / CommonJS, pure JS, no environment, network or credential access.
 */
(function (root, factory) {
  'use strict';
  if (typeof module === 'object' && module && module.exports) {
    module.exports = factory();
  } else if (typeof define === 'function' && define.amd) {
    define([], factory);
  } else {
    root.EioMediaResourceHandles = factory();
  }
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
  'use strict';

  var VERSION = 1;
  var KIND = 'eio-media-resource-handles/v1';
  var HANDLE_BRAND = 'EioMediaResourceHandles.handle';
  var MAX_GENERATION = 0x7fffffff;
  var MAX_LABEL_LENGTH = 128;
  var FAULT_LOG_LIMIT = 16;
  var PRESSURE_CODES = ['capacity-exceeded', 'busy', 'entries-exhausted'];

  function codedError(code, message) {
    var error = new Error(message || code);
    error.code = code;
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
    return isFiniteInteger(value) && value > 0 && value <= 0xffffffff;
  }

  function normalizeLabel(label) {
    if (label === undefined || label === null || label === '') {
      return { ok: true, label: '' };
    }
    if (typeof label !== 'string') {
      return failure('invalid-argument', 'label must be a string', { field: 'label' });
    }
    if (label.length > MAX_LABEL_LENGTH) {
      return failure('invalid-argument',
        'label must be at most ' + MAX_LABEL_LENGTH + ' characters', { field: 'label' });
    }
    return { ok: true, label: label };
  }

  function normalizeOwner(owner) {
    if (owner === undefined || owner === null || owner === '') {
      return { ok: true, owner: '' };
    }
    if (typeof owner !== 'string' && typeof owner !== 'number') {
      return failure('invalid-argument', 'owner must be a string or number', { field: 'owner' });
    }
    return { ok: true, owner: owner };
  }

  function checkController(controller) {
    if (!controller || typeof controller !== 'object') {
      throw codedError('invalid-argument',
        'createMediaResourceHandles requires the shared ownership controller');
    }
    var methods = ['reserve', 'bind', 'bindOwnedResource', 'releaseOwnedResource',
      'reclaimUnused', 'prepareFinalFree', 'commitFinalFree', 'snapshot'];
    for (var index = 0; index < methods.length; index += 1) {
      if (typeof controller[methods[index]] !== 'function') {
        throw codedError('invalid-argument',
          'shared ownership controller is missing ' + methods[index] + '()');
      }
    }
    return controller;
  }

  function Ledger(controller, options) {
    this._controller = controller;
    this._maxForwardMs = isFiniteInteger(options.maxForwardMs) && options.maxForwardMs > 0 ?
      options.maxForwardMs : 0;
    this._clockRead = typeof options.clockRead === 'function' ? options.clockRead : null;
    this._live = new Map();
    this._byOwner = new Map();
    this._liveBytes = 0;
    this._retainedLeases = 0;
    this._acquiredCount = 0;
    this._releasedCount = 0;
    this._refusedCount = 0;
    this._releaseFailedCount = 0;
    this._temporalRejections = 0;
    this._faults = [];
    this._faultsEvicted = 0;
    this._faultCount = 0;
    this._faultBytes = 0;
    this._lastFault = null;
    this._clockKnown = false;
    this._clockMs = null;
    this._bootstrapBaseMs = null;
  }

  Ledger.prototype._noteFault = function _noteFault(code, record, chargedBytes) {
    var fault = Object.freeze({
      code: code,
      label: record ? record.label : '',
      bytes: record ? record.bytes : 0,
      chargedBytes: chargedBytes,
      sequence: this._faultCount + 1
    });
    this._faultCount += 1;
    this._faultBytes += chargedBytes;
    this._lastFault = fault;
    if (this._faults.length >= FAULT_LOG_LIMIT) {
      this._faults.shift();
      this._faultsEvicted += 1;
    }
    this._faults.push(fault);
    return fault;
  };

  Ledger.prototype._identityLive = function _identityLive(owner, identity) {
    var byIdentity = this._byOwner.get(owner);
    return !!(byIdentity && byIdentity.has(identity));
  };

  Ledger.prototype._registerIdentity = function _registerIdentity(record, identity) {
    var byIdentity = this._byOwner.get(record.owner);
    if (!byIdentity) {
      byIdentity = new Map();
      this._byOwner.set(record.owner, byIdentity);
    }
    byIdentity.set(identity, record);
    record.identity = identity;
  };

  Ledger.prototype._unregisterIdentity = function _unregisterIdentity(record) {
    if (record.identity === null) return;
    var byIdentity = this._byOwner.get(record.owner);
    if (byIdentity && byIdentity.get(record.identity) === record) {
      byIdentity.delete(record.identity);
      if (!byIdentity.size) this._byOwner.delete(record.owner);
    }
    record.identity = null;
  };

  Ledger.prototype._finish = function _finish(record) {
    if (record.released) return false;
    record.released = true;
    record.state = 'released';
    this._unregisterIdentity(record);
    this._live.delete(record.handle);
    this._liveBytes -= record.bytes;
    this._releasedCount += 1;
    return true;
  };

  function makeHandle(ledger, record) {
    var handle = {};
    Object.defineProperties(handle, {
      brand: { value: HANDLE_BRAND, enumerable: false },
      bytes: { value: record.bytes, enumerable: true },
      label: { value: record.label, enumerable: true },
      kind: { value: record.kind, enumerable: true },
      generation: { value: record.generation, enumerable: true },
      token: {
        enumerable: true,
        get: function () {
          return record.token;
        }
      },
      state: {
        enumerable: true,
        get: function () {
          return record.state;
        }
      },
      bindNative: {
        enumerable: true,
        value: function bindNative(payload) {
          return ledger._bindNative(record, payload);
        }
      },
      bindOwned: {
        enumerable: true,
        value: function bindOwned() {
          return ledger._bindOwned(record);
        }
      },
      publish: {
        enumerable: true,
        value: function publish() {
          return ledger._publish(record);
        }
      },
      isCancelled: {
        enumerable: true,
        value: function isCancelled() {
          return ledger._isCancelled(record);
        }
      },
      retain: {
        enumerable: true,
        value: function retain() {
          return ledger._retain(record);
        }
      },
      drop: {
        enumerable: true,
        value: function drop() {
          return ledger._drop(record);
        }
      },
      prepareFinalFree: {
        enumerable: true,
        value: function prepareFinalFree() {
          return ledger._prepareFinalFree(record);
        }
      },
      commitFinalFree: {
        enumerable: true,
        value: function commitFinalFree(ticket, evidence) {
          return ledger._commitFinalFree(record, ticket, evidence);
        }
      },
      finalRelease: {
        enumerable: true,
        value: function finalRelease(proof) {
          return ledger._finalRelease(record, proof);
        }
      },
      discard: {
        enumerable: true,
        value: function discard(reason) {
          return ledger._discard(record, reason);
        }
      }
    });
    return Object.freeze(handle);
  }

  Ledger.prototype._admitTemporal = function _admitTemporal(metas) {
    if (!(this._maxForwardMs > 0)) return { ok: true };
    var maxPts = null;
    var valid = false;
    for (var index = 0; index < metas.length; index += 1) {
      var ms = metas[index] && metas[index].mediaTime;
      if (!isFiniteInteger(ms)) {
        this._noteFault('EIO_TEMPORAL_META_INVALID', null, 0);
        continue;
      }
      valid = true;
      if (maxPts === null || ms > maxPts) maxPts = ms;
    }
    if (!valid || maxPts === null) return { ok: true };

    var clock = null;
    if (this._clockRead) {
      try {
        clock = this._clockRead();
      } catch (error) {
        clock = null;
      }
    }
    if (clock && clock.known === true && Number.isFinite(clock.mediaMs)) {
      this._clockKnown = true;
      this._clockMs = clock.mediaMs;
      this._bootstrapBaseMs = null;
      var limit = clock.mediaMs + this._maxForwardMs;
      if (maxPts <= limit) return { ok: true };
      this._temporalRejections += 1;
      return { ok: false, code: 'EIO_DEMUX_FORWARD_WINDOW_PENDING',
        ptsMs: maxPts, limitMs: limit };
    }
    this._clockKnown = false;
    if (this._bootstrapBaseMs === null) this._bootstrapBaseMs = maxPts;
    if (maxPts - this._bootstrapBaseMs <= this._maxForwardMs) {
      return { ok: true, bootstrap: true };
    }
    this._temporalRejections += 1;
    return { ok: false, code: 'EIO_DEMUX_CLOCK_UNCONFIRMED', ptsMs: maxPts,
      baseMs: this._bootstrapBaseMs, spanMs: this._maxForwardMs };
  };

  Ledger.prototype.admitTemporal = function admitTemporal(metas) {
    if (!Array.isArray(metas)) {
      return failure('invalid-argument', 'admitTemporal requires an array of metas',
        { field: 'metas' });
    }
    return this._admitTemporal(metas);
  };

  function normalizeSpec(ledger, spec) {
    if (!spec || typeof spec !== 'object') {
      return failure('invalid-argument', 'acquire requires a spec object');
    }
    var kind = spec.kind;
    if (kind !== 'owned' && kind !== 'sample') {
      return failure('invalid-kind', 'kind must be "owned" or "sample"', { field: 'kind' });
    }
    var size = spec.size;
    if (!isFiniteInteger(size) || size < 0) {
      return failure('invalid-argument', 'size must be a non-negative integer',
        { field: 'size' });
    }
    var labelCheck = normalizeLabel(spec.label);
    if (!labelCheck.ok) return labelCheck;
    var ownerCheck = normalizeOwner(spec.owner);
    if (!ownerCheck.ok) return ownerCheck;
    var generation = spec.generation === undefined ? 0 : spec.generation;
    if (!isFiniteInteger(generation) || generation < 0 || generation > MAX_GENERATION) {
      return failure('invalid-argument',
        'generation must be an integer in [0, ' + MAX_GENERATION + ']',
        { field: 'generation' });
    }
    return {
      ok: true,
      kind: kind,
      size: size,
      label: labelCheck.label,
      owner: ownerCheck.owner,
      generation: generation,
      mediaTime: spec.mediaTime,
      isCancelled: spec.isCancelled
    };
  }

  function makeRecord(ledger, spec, core) {
    var record = {
      kind: spec.kind,
      bytes: spec.size,
      label: spec.label,
      owner: spec.owner,
      generation: spec.generation,
      identity: null,
      buffer: 0,
      state: spec.kind === 'owned' ? 'bound' : 'reserved',
      token: core.token || null,
      coreHandle: core.handle || null,
      releaseCore: core.releaseCore || null,
      isCancelledFn: typeof spec.isCancelled === 'function' ? spec.isCancelled : null,
      leases: 0,
      published: false,
      closingTicket: null,
      released: false,
      fault: null,
      releaseFailed: false,
      handle: null
    };
    record.handle = makeHandle(ledger, record);
    ledger._live.set(record.handle, record);
    ledger._liveBytes += record.bytes;
    ledger._acquiredCount += 1;
    return record;
  }

  Ledger.prototype.acquire = function acquire(spec) {
    var checked = normalizeSpec(this, spec);
    if (!checked.ok) {
      this._refusedCount += 1;
      return checked;
    }
    if (!this._controller) {
      this._refusedCount += 1;
      return failure('controller-unavailable',
        'an adopt-only ledger cannot acquire new bytes', {
          bytes: checked.size, label: checked.label, chargedBytes: 0, retry: false
        });
    }
    if (checked.mediaTime !== undefined) {
      var temporal = this._admitTemporal([{ mediaTime: checked.mediaTime }]);
      if (!temporal.ok) {
        this._refusedCount += 1;
        return failure(temporal.code, 'temporal admission refused the resource', {
          bytes: checked.size,
          label: checked.label,
          generation: checked.generation,
          ptsMs: temporal.ptsMs,
          limitMs: temporal.limitMs,
          baseMs: temporal.baseMs,
          spanMs: temporal.spanMs,
          chargedBytes: 0,
          temporal: true
        });
      }
    }

    var reserved;
    try {
      reserved = this._controller.reserve(checked.size, checked.generation);
    } catch (error) {
      this._refusedCount += 1;
      return failure('reserve-failed', 'shared ownership reserve threw', {
        bytes: checked.size, label: checked.label, chargedBytes: 0, retry: false
      });
    }
    if (!reserved || reserved.ok !== true) {
      this._refusedCount += 1;
      var code = reserved && reserved.code || 'reserve-failed';
      return failure(code, reserved && reserved.message || 'shared ownership reserve refused', {
        bytes: checked.size,
        label: checked.label,
        generation: checked.generation,
        chargedBytes: 0,
        retry: PRESSURE_CODES.indexOf(code) !== -1
      });
    }

    if (checked.kind === 'owned') {
      var bound;
      try {
        bound = this._controller.bindOwnedResource(reserved.token);
      } catch (error) {
        bound = null;
      }
      if (!bound || bound.ok !== true) {
        var bindCode = bound && bound.code || 'bind-failed';
        this._refusedCount += 1;
        var bindFault = this._noteFault(bindCode, {
          label: checked.label, bytes: checked.size
        }, checked.size);
        return failure(bindFault.code, 'owned bind failed after the reservation charge', {
          bytes: checked.size,
          label: checked.label,
          generation: checked.generation,
          fault: true,
          chargedBytes: checked.size,
          token: reserved.token
        });
      }
      var ownedRecord = makeRecord(this, checked, {
        token: reserved.token,
        handle: bound.handle,
        releaseCore: this._makeOwnedRelease(bound.handle)
      });
      return { ok: true, handle: ownedRecord.handle };
    }

    var sampleRecord = makeRecord(this, checked, { token: reserved.token });
    return { ok: true, handle: sampleRecord.handle };
  };

  Ledger.prototype._makeOwnedRelease = function _makeOwnedRelease(coreHandle) {
    var controller = this._controller;
    return function releaseCore(proof) {
      return controller.releaseOwnedResource(coreHandle, proof);
    };
  };

  Ledger.prototype.adopt = function adopt(spec) {
    if (!spec || typeof spec !== 'object') {
      return failure('invalid-argument', 'adopt requires a spec object');
    }
    if (spec.kind !== undefined && spec.kind !== 'owned') {
      return failure('invalid-kind', 'adopt currently supports only owned handles',
        { field: 'kind' });
    }
    var checked = normalizeSpec(this, {
      kind: 'owned', size: spec.size, label: spec.label, owner: spec.owner,
      generation: spec.generation
    });
    if (!checked.ok) {
      this._refusedCount += 1;
      return checked;
    }
    var coreHandle = spec.coreHandle;
    if (!coreHandle || typeof coreHandle !== 'object' ||
        typeof coreHandle.release !== 'function') {
      this._refusedCount += 1;
      return failure('invalid-adopted-handle',
        'adopt requires a core handle with release(proof)', { field: 'coreHandle' });
    }
    var coreBytes;
    try {
      coreBytes = coreHandle.bytes;
    } catch (error) {
      coreBytes = undefined;
    }
    if (coreBytes !== undefined &&
        (!isFiniteInteger(coreBytes) || coreBytes !== checked.size)) {
      this._refusedCount += 1;
      return failure('invalid-adopted-handle',
        'adopted handle bytes do not match the declared size', { field: 'size' });
    }
    var record = makeRecord(this, checked, {
      handle: coreHandle,
      releaseCore: function releaseCore(proof) {
        return coreHandle.release(proof);
      }
    });
    if (typeof spec.isCancelled === 'function') record.isCancelledFn = spec.isCancelled;
    return { ok: true, handle: record.handle };
  };

  Ledger.prototype.acquireBatch = function acquireBatch(specs) {
    if (!Array.isArray(specs)) {
      return failure('invalid-argument', 'acquireBatch requires an array of specs',
        { field: 'specs' });
    }
    if (!specs.length) return { ok: true, handles: Object.freeze([]) };
    if (!this._controller) {
      this._refusedCount += 1;
      return failure('controller-unavailable',
        'an adopt-only ledger cannot acquire new bytes', { chargedBytes: 0, retry: false });
    }
    var checked = [];
    var total = 0;
    for (var index = 0; index < specs.length; index += 1) {
      var item = normalizeSpec(this, specs[index]);
      if (!item.ok) {
        this._refusedCount += 1;
        return item;
      }
      if (!isFiniteInteger(total + item.size)) {
        this._refusedCount += 1;
        return failure('invalid-argument', 'batch byte sum is not a safe integer',
          { field: 'size' });
      }
      total += item.size;
      checked.push(item);
    }
    var temporal = this._admitTemporal(checked);
    if (!temporal.ok) {
      this._refusedCount += 1;
      return failure(temporal.code, 'temporal admission refused the batch', {
        bytes: total,
        ptsMs: temporal.ptsMs,
        limitMs: temporal.limitMs,
        baseMs: temporal.baseMs,
        spanMs: temporal.spanMs,
        chargedBytes: 0,
        temporal: true
      });
    }

    var reservations = [];
    for (var reserveIndex = 0; reserveIndex < checked.length; reserveIndex += 1) {
      var reserved;
      try {
        reserved = this._controller.reserve(checked[reserveIndex].size,
          checked[reserveIndex].generation);
      } catch (error) {
        reserved = null;
      }
      if (!reserved || reserved.ok !== true) {
        for (var reclaimIndex = 0; reclaimIndex < reservations.length; reclaimIndex += 1) {
          try {
            this._controller.reclaimUnused(reservations[reclaimIndex].token);
          } catch (error) {
            this._noteFault('EIO_BATCH_RECLAIM_FAILED', null, 0);
          }
        }
        this._refusedCount += 1;
        var code = reserved && reserved.code || 'reserve-failed';
        return failure(code, reserved && reserved.message || 'batch reserve refused', {
          bytes: total,
          chargedBytes: 0,
          retry: PRESSURE_CODES.indexOf(code) !== -1
        });
      }
      reservations.push(reserved);
    }

    var records = [];
    for (var recordIndex = 0; recordIndex < checked.length; recordIndex += 1) {
      var spec = checked[recordIndex];
      if (spec.kind === 'owned') {
        var bound;
        try {
          bound = this._controller.bindOwnedResource(reservations[recordIndex].token);
        } catch (error) {
          bound = null;
        }
        if (!bound || bound.ok !== true) {
          var bindCode = bound && bound.code || 'bind-failed';
          this._noteFault(bindCode, {
            label: spec.label, bytes: spec.size
          }, spec.size);
          for (var dropIndex = 0; dropIndex < records.length; dropIndex += 1) {
            this._finalRelease(records[dropIndex], { referencesDropped: true });
          }
          for (var restIndex = recordIndex + 1; restIndex < reservations.length; restIndex += 1) {
            try {
              this._controller.reclaimUnused(reservations[restIndex].token);
            } catch (error) {
              this._noteFault('EIO_BATCH_RECLAIM_FAILED', null, 0);
            }
          }
          this._refusedCount += 1;
          return failure(bindCode, 'batch owned bind failed after the reservation charge', {
            bytes: total,
            fault: true,
            chargedBytes: spec.size
          });
        }
        records.push(makeRecord(this, spec, {
          token: reservations[recordIndex].token,
          handle: bound.handle,
          releaseCore: this._makeOwnedRelease(bound.handle)
        }));
      } else {
        records.push(makeRecord(this, spec, { token: reservations[recordIndex].token }));
      }
    }
    return {
      ok: true,
      handles: Object.freeze(records.map(function (record) { return record.handle; }))
    };
  };

  Ledger.prototype._bindNative = function _bindNative(record, payload) {
    if (record.released) {
      return failure('already-released', 'cannot bind a released resource');
    }
    if (record.fault) return failure(record.fault, 'resource is already in a failed state');
    if (record.identity !== null) {
      return failure('already-bound', 'this handle already has a native identity');
    }
    if (!payload || typeof payload !== 'object') {
      return failure('invalid-argument', 'bindNative requires { data, buffer } pointers');
    }
    var data = payload.data;
    if (!isUint32(data)) {
      return failure('invalid-argument', 'payload.data must be a non-zero wasm32 pointer',
        { field: 'data' });
    }
    var buffer = payload.buffer === undefined ? 0 : payload.buffer;
    if (buffer !== 0 && !isUint32(buffer)) {
      return failure('invalid-argument', 'payload.buffer must be a non-zero wasm32 pointer',
        { field: 'buffer' });
    }
    if (payload.size !== undefined &&
        (!isFiniteInteger(payload.size) || payload.size !== record.bytes)) {
      return failure('invalid-argument', 'payload.size must equal the reserved bytes',
        { field: 'size' });
    }
    if (this._identityLive(record.owner, data)) {
      record.state = 'ambiguous';
      var collision = this._noteFault('EIO_IDENTITY_COLLISION', record, 0);
      return failure('identity-collision',
        'another live resource under this owner already uses the same native identity',
        { fault: true, chargedBytes: 0, sequence: collision.sequence });
    }

    if (record.kind === 'sample') {
      if (record.state !== 'reserved') {
        return failure('not-reserved', 'sample bind requires an unbound reservation');
      }
      if (buffer === 0) {
        return failure('invalid-argument',
          'sample bind requires the AVBuffer metadata pointer in payload.buffer',
          { field: 'buffer' });
      }
      var bound;
      try {
        bound = this._controller.bind(record.token, {
          data: data, buffer: buffer, size: record.bytes
        });
      } catch (error) {
        bound = null;
      }
      if (!bound || bound.ok !== true) {
        var bindCode = bound && bound.code || 'bind-failed';
        record.fault = bindCode;
        this._noteFault(bindCode, record, record.bytes);
        return failure(bindCode, 'shared ownership refused the native bind', {
          fault: true, chargedBytes: record.bytes
        });
      }
    }

    this._registerIdentity(record, data);
    record.buffer = buffer;
    record.state = 'bound';
    return { ok: true, bytes: record.bytes };
  };

  Ledger.prototype._bindOwned = function _bindOwned(record) {
    if (record.released) {
      return failure('already-released', 'cannot bind a released resource');
    }
    if (record.kind !== 'owned') {
      return failure('invalid-kind', 'bindOwned is only valid for owned resources');
    }
    record.state = 'bound';
    return { ok: true, bytes: record.bytes };
  };

  Ledger.prototype._publish = function _publish(record) {
    if (record.released) {
      return failure('already-released', 'cannot publish a released resource');
    }
    if (record.identity === null) {
      return failure('not-bound', 'publish requires a native identity first');
    }
    if (record.published) {
      return failure('already-published', 'this handle was already published');
    }
    record.published = true;
    record.state = 'published';
    return { ok: true };
  };

  Ledger.prototype._isCancelled = function _isCancelled(record) {
    if (!record.isCancelledFn) return false;
    try {
      return record.isCancelledFn() === true;
    } catch (error) {
      return true;
    }
  };

  Ledger.prototype._retain = function _retain(record) {
    if (record.released) {
      return failure('already-released', 'cannot retain an alias of a released resource');
    }
    record.leases += 1;
    this._retainedLeases += 1;
    return { ok: true, leases: record.leases };
  };

  Ledger.prototype._drop = function _drop(record) {
    if (record.released) {
      return failure('already-released', 'resource was already released');
    }
    if (record.leases <= 0) {
      return failure('no-lease', 'drop without a matching retain is refused');
    }
    record.leases -= 1;
    this._retainedLeases -= 1;
    return { ok: true, leases: record.leases };
  };

  Ledger.prototype._prepareFinalFree = function _prepareFinalFree(record) {
    if (record.released) {
      return failure('already-released', 'cannot prepare a released resource');
    }
    if (record.kind !== 'sample') {
      return failure('invalid-kind', 'prepareFinalFree is only valid for sample resources');
    }
    if (record.identity === null) {
      return failure('not-bound', 'sample final free requires a bound native identity');
    }
    if (record.closingTicket) {
      return failure('already-closing', 'a final-free ticket already exists for this handle');
    }
    var prepared;
    try {
      prepared = this._controller.prepareFinalFree({
        data: record.identity, buffer: record.buffer
      });
    } catch (error) {
      prepared = null;
    }
    if (!prepared || prepared.ok !== true) {
      var code = prepared && prepared.code || 'prepare-final-free-failed';
      this._markReleaseFailed(record, code);
      this._noteFault(code, record, 0);
      return failure(code, 'shared ownership refused the final-free ticket');
    }
    record.closingTicket = prepared.ticket;
    record.state = 'closing';
    return { ok: true, ticket: prepared.ticket };
  };

  Ledger.prototype._commitFinalFree = function _commitFinalFree(record, ticket, evidence) {
    if (record.released) {
      return failure('already-released', 'resource was already released');
    }
    if (record.kind !== 'sample') {
      return failure('invalid-kind', 'commitFinalFree is only valid for sample resources');
    }
    if (!record.closingTicket || ticket !== record.closingTicket) {
      return failure('invalid-ticket', 'commit requires the ticket returned by prepareFinalFree');
    }
    var committed;
    try {
      committed = this._controller.commitFinalFree(ticket, evidence);
    } catch (error) {
      committed = null;
    }
    if (!committed || committed.ok !== true) {
      var code = committed && committed.code || 'commit-final-free-failed';
      this._markReleaseFailed(record, code);
      this._noteFault(code, record, 0);
      return failure(code, 'shared ownership refused the final-free commit', {
        chargedBytes: record.bytes
      });
    }
    this._finish(record);
    return { ok: true, releasedBytes: committed.releasedBytes,
      heldBytes: committed.heldBytes };
  };

  Ledger.prototype._markReleaseFailed = function _markReleaseFailed(record, code) {
    if (!record.releaseFailed) {
      record.releaseFailed = true;
      record.fault = record.fault || code;
      this._releaseFailedCount += 1;
    }
  };

  Ledger.prototype._finalRelease = function _finalRelease(record, proof) {
    if (record.released) {
      return failure('already-released', 'release is single use', {
        releasedBytes: 0, chargedBytes: 0
      });
    }
    if (record.releaseFailed) {
      return failure(record.fault || 'release-failed',
        'a failed release keeps the charge and is never retried', {
          fault: true, chargedBytes: record.bytes
        });
    }
    if (!proof || proof.referencesDropped !== true) {
      return failure('not-dropped',
        'release requires { referencesDropped: true } after the last controlled alias',
        { chargedBytes: record.bytes });
    }
    if (record.leases > 0) {
      return failure('aliases-remain',
        'a retained local alias is still outstanding; release refused without credit',
        { leases: record.leases, chargedBytes: record.bytes });
    }

    /* The true Q7 final-free branch can retire the shared record without the
     * handle's prepare/commit pair. Accept that path only when the controller
     * confirms the reservation is already retired; the core credited the bytes
     * there, so this finish must not credit them a second time. */
    if (record.kind === 'sample' && record.identity !== null &&
        proof.dataFreed === true &&
        typeof this._controller.inspectReservation === 'function') {
      var inspected = null;
      try {
        inspected = this._controller.inspectReservation(record.token);
      } catch (error) {
        inspected = null;
      }
      if (inspected && inspected.ok === true && inspected.retired === true) {
        this._finish(record);
        return { ok: true, releasedBytes: 0, retiredBytes: record.bytes, external: true };
      }
    }

    if (record.kind === 'owned') {
      var released;
      try {
        released = record.releaseCore(proof);
      } catch (error) {
        released = null;
      }
      if (!released || released.ok !== true) {
        var code = released && released.code || 'release-failed';
        record.fault = code;
        this._markReleaseFailed(record, code);
        this._noteFault(code, record, record.bytes);
        return failure(code, 'core refused the owned release; the charge is retained', {
          fault: true, chargedBytes: record.bytes
        });
      }
      this._finish(record);
      return { ok: true, releasedBytes: released.releasedBytes,
        heldBytes: released.heldBytes };
    }

    if (record.state === 'reserved') {
      var reclaimed;
      try {
        reclaimed = this._controller.reclaimUnused(record.token);
      } catch (error) {
        reclaimed = null;
      }
      if (!reclaimed || reclaimed.ok !== true) {
        var reclaimCode = reclaimed && reclaimed.code || 'reclaim-failed';
        record.fault = reclaimCode;
        this._markReleaseFailed(record, reclaimCode);
        this._noteFault(reclaimCode, record, record.bytes);
        return failure(reclaimCode,
          'core refused the unbound reservation reclaim; the charge is retained', {
            fault: true, chargedBytes: record.bytes
          });
      }
      this._finish(record);
      return { ok: true, releasedBytes: reclaimed.reclaimedBytes,
        heldBytes: reclaimed.heldBytes };
    }

    if (proof.dataFreed !== true) {
      return failure('final-free-required',
        'a bound sample needs the real data free first: prepareFinalFree(), free, ' +
        'commitFinalFree(ticket, { dataFreed: true })', {
          chargedBytes: record.bytes
        });
    }
    var prepared = this._prepareFinalFree(record);
    if (!prepared.ok) return prepared;
    return this._commitFinalFree(record, prepared.ticket, proof);
  };

  Ledger.prototype._discard = function _discard(record, reason) {
    if (record.released) {
      return failure('already-released', 'resource was already released');
    }
    if (record.kind === 'sample' && record.identity !== null) {
      return failure('final-free-required',
        'a bound sample cannot be discarded; use prepare/commit after the real free', {
          chargedBytes: record.bytes
        });
    }
    var result = this._finalRelease(record, { referencesDropped: true });
    if (result.ok && reason) result.reason = reason;
    return result;
  };

  Ledger.prototype.releaseOwned = function releaseOwned(reason) {
    var released = 0;
    var refused = 0;
    var targets = [];
    this._live.forEach(function (record, handle) {
      if (record.kind === 'owned') targets.push(handle);
    });
    for (var index = 0; index < targets.length; index += 1) {
      var result = this._finalRelease(this._live.get(targets[index]), {
        referencesDropped: true
      });
      if (result.ok === true) released += 1;
      else refused += 1;
    }
    return Object.freeze({
      ok: refused === 0,
      code: refused === 0 ? null : 'release-owned-refused',
      reason: reason === undefined ? '' : String(reason),
      released: released,
      refused: refused,
      liveCount: this._live.size,
      liveBytes: this._liveBytes,
      retainedLeases: this._retainedLeases
    });
  };

  Ledger.prototype.snapshot = function snapshot() {
    var core = null;
    if (this._controller) {
      try {
        core = this._controller.snapshot();
      } catch (error) {
        core = null;
      }
    }
    return Object.freeze({
      kind: KIND,
      version: VERSION,
      liveBytes: this._liveBytes,
      liveCount: this._live.size,
      retainedLeases: this._retainedLeases,
      acquiredCount: this._acquiredCount,
      releasedCount: this._releasedCount,
      refusedCount: this._refusedCount,
      releaseFailedCount: this._releaseFailedCount,
      temporalRejections: this._temporalRejections,
      clockKnown: this._clockKnown,
      clockMs: this._clockMs,
      bootstrapBaseMs: this._bootstrapBaseMs,
      maxForwardMs: this._maxForwardMs,
      faultCount: this._faultCount,
      faultChargedBytes: this._faultBytes,
      faultsEvicted: this._faultsEvicted,
      lastFault: this._lastFault,
      faults: Object.freeze(this._faults.slice()),
      core: core === null ? null : Object.freeze({
        heldBytes: core.heldBytes,
        peakBytes: core.peakBytes,
        reservedBytes: core.reservedBytes,
        boundBytes: core.boundBytes,
        countReserved: core.countReserved,
        countBound: core.countBound,
        countClosing: core.countClosing,
        ownedResourceBytes: core.ownedResourceBytes,
        countOwnedResources: core.countOwnedResources
      }),
      measurement: 'local-handle-records+nontransactional-core-scan',
      exact: false
    });
  };

  function createMediaResourceHandles(options) {
    if (!options || typeof options !== 'object') {
      throw codedError('invalid-argument',
        'createMediaResourceHandles requires { controller } or { adoptOnly: true }');
    }
    if (options.maxForwardMs !== undefined &&
        (!isFiniteInteger(options.maxForwardMs) || options.maxForwardMs < 0)) {
      throw codedError('invalid-argument',
        'maxForwardMs must be a non-negative integer');
    }
    if (options.clockRead !== undefined && typeof options.clockRead !== 'function') {
      throw codedError('invalid-argument', 'clockRead must be a function');
    }
    if (options.adoptOnly !== undefined && typeof options.adoptOnly !== 'boolean') {
      throw codedError('invalid-argument', 'adoptOnly must be a boolean');
    }
    /* An adopt-only ledger never acquires bytes of its own: every handle is
       already charged by another component's shared-ownership controller.
       It therefore needs no controller, but it also refuses acquire(). */
    var controller = options.adoptOnly === true ? null : checkController(options.controller);
    return new Ledger(controller, options);
  }

  return Object.freeze({
    VERSION: VERSION,
    KIND: KIND,
    HANDLE_BRAND: HANDLE_BRAND,
    FAULT_LOG_LIMIT: FAULT_LOG_LIMIT,
    createMediaResourceHandles: createMediaResourceHandles
  });
});


}}]);