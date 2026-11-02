/*!
 * player-owned-media-resources.js
 *
 * Local ownership wrapper for resources whose backing the caller allocates
 * itself (IO source buffers, WASM demux i/o buffers) but whose bytes must be
 * charged to the same shared compressed-media budget as FFmpeg samples.
 *
 * One accounting domain: every acquire() goes through the shared-media
 * ownership controller (reserve -> bindOwnedResource), so owned resources and
 * samples share one HELD counter, one capacity and one entry table. This module
 * never keeps a second ledger and never invents a fraction of a buffer.
 *
 * There is deliberately no implicit credit here: no FinalizationRegistry, no
 * WeakRef, no GC guess, no timer. A handle reports its bytes only; byte credit
 * happens when the caller calls handle.release({ referencesDropped: true })
 * after it really dropped its last controlled alias (or after the allocator
 * free returned) and the core controller accepted the release.
 *
 * The handle is opaque on purpose: { bytes, label, release(proof) } are the
 * public fields. It exposes no pointer/size arithmetic, so callers cannot
 * pretend an alias was dropped.
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
    root.EioOwnedMediaResources = factory();
  }
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
  'use strict';

  var VERSION = 1;
  var KIND = 'eio-owned-media-resources/v1';
  var HANDLE_BRAND = 'EioOwnedMediaResources.handle';
  var MAX_GENERATION = 0x7fffffff;
  var MAX_LABEL_LENGTH = 128;
  /* Bounded log of unresolved accounting faults only. The log is diagnostics;
   * budget admission is still the core controller's entry table, so this is not
   * a second registry cap. Evictions are counted, never silently dropped. */
  var FAULT_LOG_LIMIT = 16;

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

  function checkController(controller) {
    if (!controller || typeof controller !== 'object' ||
        typeof controller.reserve !== 'function' ||
        typeof controller.bindOwnedResource !== 'function' ||
        typeof controller.releaseOwnedResource !== 'function' ||
        typeof controller.snapshot !== 'function') {
      throw codedError('invalid-argument',
        'createOwnedResources requires a shared-media ownership controller ' +
        '(reserve/bindOwnedResource/releaseOwnedResource/snapshot)');
    }
    return controller;
  }

  /* label is a display/diagnostic name only; it never influences accounting. */
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

  function OwnedResources(controller) {
    this._controller = controller;
    /* Live handle -> record. A record leaves this map only after the core
     * controller confirmed the release, so a refused release can never show up
     * as free locally. */
    this._live = new Map();
    this._liveBytes = 0;
    this._retainedLeases = 0;
    this._faults = [];
    this._faultsEvicted = 0;
    this._faultCount = 0;
    this._faultBytes = 0;
    this._lastFault = null;
    this._acquiredCount = 0;
    this._releasedCount = 0;
    this._refusedCount = 0;
    /* Deliberately not Object.freeze(this): the counters above are own data
     * properties that release()/acquire() must be able to update in strict
     * mode. Handles and snapshot results are the frozen public objects. */
  }

  OwnedResources.prototype._noteFault = function _noteFault(code, message, bytes, label) {
    var fault = Object.freeze({
      code: code,
      message: message || code,
      bytes: bytes,
      label: label,
      chargedBytes: bytes,
      sequence: this._faultCount + 1
    });
    this._faultCount += 1;
    this._faultBytes += bytes;
    this._lastFault = fault;
    if (this._faults.length >= FAULT_LOG_LIMIT) {
      this._faults.shift();
      this._faultsEvicted += 1;
    }
    this._faults.push(fault);
    return fault;
  };

  function makeHandle(owner, record) {
    var handle = {};
    Object.defineProperties(handle, {
      brand: { value: HANDLE_BRAND, enumerable: false },
      bytes: { value: record.bytes, enumerable: true },
      label: { value: record.label, enumerable: true },
      release: {
        enumerable: true,
        value: function release(proof) {
          return owner._release(handle, record, proof);
        }
      },
      /* Optional local lease counter. It can only block a release; it never
       * releases by itself, so an outstanding alias can never be credited. */
      retain: {
        enumerable: false,
        value: function retain() {
          return owner._retain(record);
        }
      },
      drop: {
        enumerable: false,
        value: function drop() {
          return owner._drop(record);
        }
      },
      leases: {
        enumerable: false,
        get: function () {
          return record.leases;
        }
      }
    });
    return Object.freeze(handle);
  }

  /* Reserve (admission) first and bind second. Nothing is allocated here: the
   * core charge is the only byte accounting, and an admission refusal returns
   * an explicit result with chargedBytes 0 and no handle. */
  OwnedResources.prototype.acquire = function acquire(bytes, label, generation) {
    var controller = this._controller;
    if (!isFiniteInteger(bytes) || bytes < 0) {
      this._refusedCount += 1;
      return failure('invalid-argument', 'bytes must be a non-negative integer',
        { field: 'bytes', bytes: bytes, label: typeof label === 'string' ? label : '',
          chargedBytes: 0, fault: false });
    }
    var labelCheck = normalizeLabel(label);
    if (!labelCheck.ok) {
      this._refusedCount += 1;
      return failure(labelCheck.code, labelCheck.message,
        { field: 'label', bytes: bytes, chargedBytes: 0, fault: false });
    }
    var name = labelCheck.label;
    var gen = generation === undefined ? 0 : generation;
    if (!isFiniteInteger(gen) || gen < 0 || gen > MAX_GENERATION) {
      this._refusedCount += 1;
      return failure('invalid-argument',
        'generation must be an integer in [0, ' + MAX_GENERATION + ']',
        { field: 'generation', bytes: bytes, label: name, chargedBytes: 0, fault: false });
    }

    var reserved = controller.reserve(bytes, gen);
    if (!reserved.ok) {
      this._refusedCount += 1;
      return failure(reserved.code || 'reserve-failed',
        reserved.message || 'shared budget refused the owned resource',
        { bytes: bytes, label: name, generation: gen, chargedBytes: 0, fault: false });
    }

    var bound = controller.bindOwnedResource(reserved.token);
    if (!bound.ok) {
      /* bindOwnedResource may have published a record before refusing, so
       * reclaiming here could credit bytes that are still referenced. Keep the
       * charge, keep the reservation token for diagnostics and record the fault
       * explicitly: this path never reports 0 charged bytes. */
      var fault = this._noteFault(bound.code || 'bind-failed',
        bound.message || 'owned resource bind failed after the reservation charge',
        bytes, name);
      return failure(fault.code, fault.message, {
        bytes: bytes,
        label: name,
        generation: gen,
        fault: true,
        chargedBytes: bytes,
        token: reserved.token
      });
    }

    var record = {
      bytes: bytes,
      label: name,
      generation: gen,
      receipt: bound.handle,
      released: false,
      leases: 0
    };
    var handle = makeHandle(this, record);
    this._live.set(handle, record);
    this._liveBytes += bytes;
    this._acquiredCount += 1;
    return { ok: true, handle: handle };
  };

  OwnedResources.prototype._release = function _release(handle, record, proof) {
    if (record.released) {
      return failure('already-released',
        'this owned resource handle was already released; release is single use',
        { bytes: record.bytes, label: record.label, releasedBytes: 0, chargedBytes: 0 });
    }
    if (!proof || proof.referencesDropped !== true) {
      /* Not a fault: the caller simply has not dropped the alias yet, and the
       * charge stays. The handle remains live so a later release can succeed. */
      return failure('not-dropped',
        'handle.release requires { referencesDropped: true } after the last controlled alias was dropped',
        { bytes: record.bytes, label: record.label, chargedBytes: record.bytes });
    }
    if (record.leases > 0) {
      return failure('aliases-remain',
        'a retained local alias is still outstanding; release refused without credit',
        { bytes: record.bytes, label: record.label, leases: record.leases,
          chargedBytes: record.bytes });
    }

    var result = this._controller.releaseOwnedResource(record.receipt, proof);
    if (!result.ok) {
      /* Core refused (stale/corrupt/busy): the handle stays live and charged
       * and the accounting anomaly is logged instead of masked as free. */
      var fault = this._noteFault(result.code || 'release-failed',
        result.message || 'core controller refused the owned resource release',
        record.bytes, record.label);
      return failure(fault.code, fault.message, {
        bytes: record.bytes,
        label: record.label,
        fault: true,
        chargedBytes: record.bytes
      });
    }

    record.released = true;
    this._live.delete(handle);
    this._liveBytes -= record.bytes;
    this._releasedCount += 1;
    return {
      ok: true,
      releasedBytes: result.releasedBytes,
      heldBytes: result.heldBytes,
      liveBytes: this._liveBytes,
      liveCount: this._live.size
    };
  };

  OwnedResources.prototype._retain = function _retain(record) {
    if (record.released) {
      return failure('already-released', 'cannot retain an alias of a released owned resource');
    }
    record.leases += 1;
    this._retainedLeases += 1;
    return { ok: true, leases: record.leases };
  };

  OwnedResources.prototype._drop = function _drop(record) {
    if (record.released) {
      return failure('already-released', 'owned resource was already released');
    }
    if (record.leases <= 0) {
      return failure('no-lease', 'drop without a matching retain is refused');
    }
    record.leases -= 1;
    this._retainedLeases -= 1;
    return { ok: true, leases: record.leases };
  };

  /* Local ownership + a nontransactional core observation. Neither number is a
   * full media measurement: the core scan can tear across a concurrent release,
   * and resources owned by peers are not visible in liveBytes at all. */
  OwnedResources.prototype.snapshot = function snapshot() {
    var core = null;
    try {
      core = this._controller.snapshot();
    } catch (error) {
      core = null;
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
      faultCount: this._faultCount,
      faultChargedBytes: this._faultBytes,
      faultsEvicted: this._faultsEvicted,
      lastFault: this._lastFault,
      faults: Object.freeze(this._faults.slice()),
      core: core === null ? null : Object.freeze({
        heldBytes: core.heldBytes,
        boundBytes: core.boundBytes,
        countBound: core.countBound,
        countReserved: core.countReserved,
        ownedResourceBytes: core.ownedResourceBytes,
        countOwnedResources: core.countOwnedResources,
        ownedResourceScan: core.ownedResourceScan
      }),
      measurement: core === null ? 'local-owner-records-only' :
        'local-owner-records+nontransactional-core-scan',
      exact: false
    });
  };

  function createOwnedResources(controller) {
    return new OwnedResources(checkController(controller));
  }

  return Object.freeze({
    VERSION: VERSION,
    KIND: KIND,
    HANDLE_BRAND: HANDLE_BRAND,
    FAULT_LOG_LIMIT: FAULT_LOG_LIMIT,
    createOwnedResources: createOwnedResources
  });
});
