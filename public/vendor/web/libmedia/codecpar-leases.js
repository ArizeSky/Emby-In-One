"use strict";
/* Persistent codecpar payload leases; canonical UMD lives in public/web. */
(self.webpackChunkAVPlayer=self.webpackChunkAVPlayer||[]).push([[90205],{
90191:(module,exports,__webpack_require__)=>{
/*!
 * player-codecpar-leases.js
 *
 * Stream-lifetime admission + lease wrapper for one persistent native codecpar
 * payload clone (extradata + side-data payloads). The native entry point
 * measures the source codecpar with a read-only scanner; the additive measured
 * entry point accepts an already-measured byte count. Both validate before any
 * core call, charge the exact bytes through the existing owned-resource/
 * shared-media manager BEFORE the native Gy()/copy, and return the four-field
 * lease that module 67669's Yi()/dn() already accept:
 *
 *   { bytes, bind(summary), release(proof), retainFailure(error) }
 *
 * Byte credit happens only when the real native destination has been freed and
 * the caller proves it dropped its last alias:
 *   release({ referencesDropped: true }) -> true only if the core accepted it.
 * A false/refused/throwing release retains the charge; there is no retry credit
 * and no fabricated zero. Failed leases stay diagnosable through the bounded
 * scalar fault log (codes, byte counts, labels) - never a pointer or a second
 * ledger. Failure ordering is part of the contract: the irrevocable failed
 * state is set before any caller-supplied error code is read, so a code getter
 * that reenters release()/retainFailure() cannot turn a retained charge into a
 * credit and a throwing getter cannot escape unpoisoned.
 *
 * Scope: this is the persistent stream-residence payload claim. It deliberately
 * does NOT consume an atomic header operation/scope, and it makes no
 * mux-output, serialization, in-place replacement, global-total or wall-clock
 * claim. Zero measured payload is valid and still creates one admitted lease
 * (zero bytes is a measured payload, not "nothing was allocated").
 *
 * UMD / CommonJS, pure JS, no environment, network or credential access. The
 * module is inert until a caller explicitly creates a lease manager.
 */
(function (root, factory) {
  'use strict';
  if (typeof module === 'object' && module && module.exports) {
    module.exports = factory();
  } else if (typeof define === 'function' && define.amd) {
    define([], factory);
  } else {
    root.EioCodecparLeases = factory();
  }
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
  'use strict';

  var VERSION = 1;
  var KIND = 'eio-codecpar-leases/v1';
  var MAX_LABEL_LENGTH = 128;
  var MAX_CODE_LENGTH = 64;
  /* Diagnostics only. Admission and the real charge remain the shared core's
   * entry table; this log never decides a credit and never resets one. */
  var FAULT_LOG_LIMIT = 16;

  /* Explicit non-coverage declaration; do not add a true flag without a new
   * contract and a new measurement site. */
  var COVERAGE = Object.freeze({
    streamLifetimePayload: true,
    atomicHeaderScope: false,
    muxOutputCopy: false,
    serialization: false,
    replacement: false,
    globalTotal: false,
    wallClock: false
  });

  function codedError(code, message) {
    var error = new Error(message || code);
    error.code = code;
    return error;
  }

  function isCount(value) {
    return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0;
  }

  function scalarOrNull(value) {
    return isCount(value) ? value : null;
  }

  function boundedLabel(value) {
    return typeof value === 'string' ? value.slice(0, MAX_LABEL_LENGTH) : '';
  }

  function boundedCode(value, fallback) {
    if (typeof value !== 'string' || value.length === 0) return fallback;
    return value.slice(0, MAX_CODE_LENGTH);
  }

  /* Diagnostic-only read of an untrusted code property. The caller must have
   * established the lease's final state first: a getter here may reenter the
   * lease or throw, so the read is guarded and every missing/refused value
   * falls back to a stable bounded code. It never decides a credit. */
  function safeErrorCode(error, fallback) {
    var code = '';
    if (error !== null && error !== undefined &&
        (typeof error === 'object' || typeof error === 'function')) {
      try {
        code = error.code;
      } catch (ignored) {
        code = '';
      }
    }
    return boundedCode(code, fallback);
  }

  /* Guarded property read for untrusted admission/release results. A getter may
   * throw any value, including null; a failed read is reported as !ok so the
   * caller fails closed instead of letting the throw escape. */
  function safeField(object, key) {
    if (object === null || object === undefined) return { ok: false, value: undefined };
    try {
      return { ok: true, value: object[key] };
    } catch (ignored) {
      return { ok: false, value: undefined };
    }
  }

  function safeChargedBytes(result) {
    var field = safeField(result, 'chargedBytes');
    return field.ok && isCount(field.value) ? field.value : 0;
  }

  /* Config checks are argument errors, not accounting faults: nothing has been
   * measured or charged at this point. */
  function checkConfig(config) {
    if (!config || typeof config !== 'object') {
      throw codedError('invalid-argument',
        'createCodecparLeases requires { resources, measureNativePayload }');
    }
    var resources = config.resources;
    if (!resources || typeof resources !== 'object' ||
        typeof resources.acquire !== 'function') {
      throw codedError('invalid-argument',
        'createCodecparLeases requires resources.acquire(bytes, label, generation)');
    }
    if (typeof config.measureNativePayload !== 'function') {
      throw codedError('invalid-argument',
        'createCodecparLeases requires a read-only measureNativePayload(sourcePtr)');
    }
    var handles = config.handles;
    if (handles !== undefined && handles !== null &&
        (typeof handles !== 'object' || typeof handles.adopt !== 'function')) {
      throw codedError('invalid-argument',
        'createCodecparLeases handles must expose adopt({kind,size,label,generation,coreHandle})');
    }
    return {
      resources: resources,
      measure: config.measureNativePayload,
      handles: handles === undefined ? null : handles
    };
  }

  function CodecparLeases(resources, measure, handles) {
    this._resources = resources;
    this._measure = measure;
    this._handles = handles;
    this._handleAdoptions = 0;
    this._handleAdoptFallbacks = 0;
    this._liveCount = 0;
    this._liveBytes = 0;
    this._strandedBytes = 0;
    this._acquiredCount = 0;
    this._measuredAcquireCount = 0;
    this._refusedCount = 0;
    this._boundCount = 0;
    this._releasedCount = 0;
    this._retainedCount = 0;
    this._releaseFailedCount = 0;
    this._reentrantCount = 0;
    this._faults = [];
    this._faultsEvicted = 0;
    this._faultCount = 0;
    this._faultBytes = 0;
    this._lastFault = null;
  }

  CodecparLeases.prototype._noteFault = function _noteFault(code, label, bytes, chargedBytes) {
    var fault = Object.freeze({
      code: boundedCode(code, 'EIO_CODECPAR_LEASE_FAILED'),
      label: boundedLabel(label),
      bytes: isCount(bytes) ? bytes : 0,
      chargedBytes: isCount(chargedBytes) ? chargedBytes : 0,
      sequence: this._faultCount + 1
    });
    this._faultCount += 1;
    this._faultBytes += fault.chargedBytes;
    this._lastFault = fault;
    if (this._faults.length >= FAULT_LOG_LIMIT) {
      this._faults.shift();
      this._faultsEvicted += 1;
    }
    this._faults.push(fault);
    return fault;
  };

  /* Irrevocable failure state. Touches no untrusted input, so it stays safe to
   * run before any caller-supplied property is read; returns the bytes that
   * this transition newly stranded for the diagnostic log. */
  CodecparLeases.prototype._markFailed = function _markFailed(record, code) {
    var chargedBytes = record.failed ? 0 : record.bytes;
    if (!record.failed) {
      record.failed = true;
      this._retainedCount += 1;
    }
    if (!record.fault) record.fault = boundedCode(code, 'EIO_CODECPAR_LEASE_FAILED');
    return chargedBytes;
  };

  /* A lease that cannot prove a successful bind can never be credited. Keep
   * the charge, keep the first stable fault and stop all later transitions. */
  CodecparLeases.prototype._poison = function _poison(record, code) {
    return this._noteFault(code, record.label, record.bytes,
      this._markFailed(record, code));
  };

  CodecparLeases.prototype._bind = function _bind(record, summary) {
    /* Reentrancy is observed through a getter on the caller's summary. Refuse
     * the transition before any state change, retain the charge and poison the
     * lease so a nested call can never turn a failed bind into a credit. */
    if (record.inCall) {
      this._reentrantCount += 1;
      this._poison(record, 'EIO_CODECPAR_LEASE_REENTRANT');
      return false;
    }
    if (record.bound || record.failed || record.released || record.releaseAttempted) {
      return false;
    }

    record.inCall = true;
    var copiedBytes;
    /* Boolean sentinel: a getter may throw any value including null, and a
     * thrown value must never look like "the read completed". */
    var readFailed = false;
    try {
      copiedBytes = summary && typeof summary === 'object' ? summary.copiedBytes : undefined;
    } catch (error) {
      readFailed = true;
    }
    var poisoned = record.failed;
    record.inCall = false;

    if (readFailed || poisoned) {
      this._poison(record, 'EIO_CODECPAR_LEASE_BIND_FAILED');
      return false;
    }
    if (copiedBytes !== record.bytes) {
      this._poison(record, 'EIO_CODECPAR_LEASE_BIND_FAILED');
      return false;
    }

    record.bound = true;
    this._boundCount += 1;
    return true;
  };

  CodecparLeases.prototype._release = function _release(record, proof) {
    if (record.inCall) {
      this._reentrantCount += 1;
      this._poison(record, 'EIO_CODECPAR_LEASE_REENTRANT');
      return false;
    }
    if (!record.bound || record.failed || record.released || record.releaseAttempted) {
      return false;
    }

    record.inCall = true;
    var dropped = false;
    var proofError = false;
    try {
      dropped = !!proof && proof.referencesDropped === true;
    } catch (error) {
      dropped = false;
      proofError = true;
    }
    var poisoned = record.failed;
    record.inCall = false;

    if (poisoned) return false;
    if (proofError) {
      /* A throwing proof getter proved nothing. Keep the charge and report the
       * malformed proof without starting a core release. */
      this._noteFault('EIO_CODECPAR_LEASE_PROOF_INVALID', record.label, record.bytes, 0);
      return false;
    }
    /* Not a fault: the caller has not proved that its last alias is gone, so
     * the handle stays live and a later release may still succeed. */
    if (!dropped) return false;

    /* Exactly one core release attempt for this lease. Set the flag before the
     * call so a throw or a refusal cannot be retried into a second credit. */
    record.releaseAttempted = true;
    var result = null;
    /* Boolean sentinel: the core may throw null/undefined, which must still
     * count as a failed release attempt, not as a completed call. */
    var releaseFailed = false;
    try {
      result = record.commonHandle ?
        record.handle.finalRelease({ referencesDropped: true }) :
        record.handle.release({ referencesDropped: true });
    } catch (error) {
      releaseFailed = true;
    }

    if (releaseFailed) {
      this._releaseFailedCount += 1;
      this._poison(record, 'EIO_CODECPAR_LEASE_RELEASE_FAILED');
      return false;
    }
    /* releaseAttempted is already set: read the core result ok flag through the
     * guarded getter so a throwing ok accessor fails closed, poisons the lease
     * and can never reach a second release attempt. */
    var okField = safeField(result, 'ok');
    var resultPoisoned = record.failed;
    if (!okField.ok || okField.value !== true || resultPoisoned) {
      this._releaseFailedCount += 1;
      /* Poison before the core's diagnostic code is read, so a code getter can
       * neither escape with a still-creditable charge nor change the outcome. */
      var strandedBytes = this._markFailed(record, 'EIO_CODECPAR_LEASE_RELEASE_FAILED');
      this._noteFault(safeErrorCode(result, 'EIO_CODECPAR_LEASE_RELEASE_FAILED'),
        record.label, record.bytes, strandedBytes);
      return false;
    }

    record.released = true;
    record.handle = null;
    this._liveCount -= 1;
    this._liveBytes -= record.bytes;
    this._releasedCount += 1;
    return true;
  };

  CodecparLeases.prototype._retainFailure = function _retainFailure(record, error) {
    if (record.released) {
      /* The core already credited this lease; never un-credit it here. The code
       * is diagnostic only, so it is read guarded after the terminal state is
       * known and cannot change it. */
      this._noteFault(safeErrorCode(error, 'EIO_CODECPAR_RELEASE_UNVERIFIED'),
        record.label, record.bytes, 0);
      return false;
    }
    /* Poison with a stable fallback BEFORE touching error.code. A getter there
     * may reenter release()/retainFailure(); because the failed state is
     * already irrevocable, that nested call is refused instead of crediting a
     * charge whose failure is established. A meaningful code is normalized
     * only afterwards and only for the log. */
    var strandedBytes = this._markFailed(record, 'EIO_CODECPAR_CLONE_UNPROVEN');
    this._noteFault(safeErrorCode(error, record.fault), record.label, record.bytes,
      strandedBytes);
    return false;
  };

  function makeLease(owner, record) {
    var lease = {};
    Object.defineProperties(lease, {
      bytes: { value: record.bytes, enumerable: true },
      bind: {
        enumerable: true,
        value: function bind(summary) {
          return owner._bind(record, summary);
        }
      },
      release: {
        enumerable: true,
        value: function release(proof) {
          return owner._release(record, proof);
        }
      },
      retainFailure: {
        enumerable: true,
        value: function retainFailure(error) {
          return owner._retainFailure(record, error);
        }
      }
    });
    return Object.freeze(lease);
  }

  /* Read-only measurement first: a malformed source must fail before any
   * admission or charge. The scanner throws its own bounded code; no pointer is
   * copied into the error, the lease or the fault log. */
  CodecparLeases.prototype._measureSource = function _measureSource(sourcePtr, label) {
    var bytes;
    /* Boolean sentinel: a scanner may throw null/undefined, and an undefined
     * byte count must still be refused rather than read as a valid result. */
    var measureFailed = false;
    try {
      bytes = this._measure(sourcePtr);
    } catch (error) {
      measureFailed = true;
    }
    if (measureFailed || !isCount(bytes)) {
      this._refusedCount += 1;
      this._noteFault('EIO_CODECPAR_LEASE_INVALID', label, 0, 0);
      throw codedError('EIO_CODECPAR_LEASE_INVALID',
        'codecpar payload measurement failed or did not return a safe byte count');
    }
    return bytes;
  };

  /* Optional alignment with the common media-resource Handle. The core handle
   * remains the sole releaser; adoption only gives that same handle the common
   * final-release path. If adoption refuses or throws, keep the direct core
   * handle and record a bounded diagnostic instead of stranding the charge. */
  CodecparLeases.prototype._adoptHandle = function _adoptHandle(record, bytes, generation, label) {
    if (!this._handles) return;
    var adopted = null;
    try {
      adopted = this._handles.adopt({
        kind: 'owned',
        size: bytes,
        label: label,
        generation: generation,
        coreHandle: record.handle
      });
    } catch (error) {
      adopted = null;
    }
    var okField = safeField(adopted, 'ok');
    var handleField = safeField(adopted, 'handle');
    var finalField = safeField(handleField.value, 'finalRelease');
    if (okField.ok && okField.value === true && handleField.ok &&
        handleField.value !== null && handleField.value !== undefined &&
        finalField.ok && typeof finalField.value === 'function') {
      record.handle = handleField.value;
      record.commonHandle = true;
      this._handleAdoptions += 1;
      return;
    }
    this._handleAdoptFallbacks += 1;
    this._noteFault('EIO_CODECPAR_LEASE_ADOPT_FALLBACK', label, bytes, 0);
  };

  /* Shared admission/record continuation for both entry points. The caller has
   * already obtained an exact, safe byte count; this function validates it
   * again before the one core call so the measured entry point cannot bypass
   * the same gate. */
  CodecparLeases.prototype._admit = function _admit(bytes, generation, label) {
    var displayLabel = boundedLabel(label);
    if (!isCount(bytes)) {
      this._refusedCount += 1;
      this._noteFault('EIO_CODECPAR_LEASE_INVALID', displayLabel, 0, 0);
      throw codedError('EIO_CODECPAR_LEASE_INVALID',
        'codecpar payload admission requires a safe non-negative byte count');
    }

    var acquired;
    /* Boolean sentinel: an admission that throws null/undefined is still a
     * throw and must not be re-read as a structured refusal. */
    var admissionFailed = false;
    try {
      acquired = this._resources.acquire(bytes, label, generation);
    } catch (error) {
      admissionFailed = true;
    }
    if (admissionFailed) {
      this._refusedCount += 1;
      this._noteFault('EIO_CODECPAR_LEASE_REFUSED', displayLabel, bytes, 0);
      throw codedError('EIO_CODECPAR_LEASE_REFUSED',
        'owned-resource admission threw before publishing a handle');
    }

    /* Guard every untrusted field: a throwing ok/handle/release/code getter
     * must become an explicit refusal, never an exception that escapes with a
     * still-charged admission. */
    var okField = safeField(acquired, 'ok');
    var handleField = safeField(acquired, 'handle');
    var handle = handleField.value;
    var releaseField = safeField(handle, 'release');
    var admitted = okField.ok && okField.value === true && handleField.ok &&
      handle !== null && handle !== undefined && releaseField.ok &&
      typeof releaseField.value === 'function';
    if (!admitted) {
      var code = safeErrorCode(acquired, 'EIO_CODECPAR_LEASE_REFUSED');
      var charged = safeChargedBytes(acquired);
      this._refusedCount += 1;
      this._noteFault(code, displayLabel, bytes, charged);
      /* A manager bind failure can publish a real charge without a handle.
       * Keep it visible as stranded diagnostics; only the core may reclaim it,
       * so this module never fabricates a matching credit. */
      if (charged > 0) this._strandedBytes += charged;
      var refusal = codedError(code, charged > 0 ?
        'owned-resource admission retained a charge without a releasable handle' :
        'owned-resource admission refused the codecpar payload reservation');
      refusal.bytes = bytes;
      refusal.chargedBytes = charged;
      throw refusal;
    }

    var record = {
      bytes: bytes,
      label: displayLabel,
      handle: handle,
      commonHandle: false,
      bound: false,
      failed: false,
      released: false,
      releaseAttempted: false,
      inCall: false,
      fault: null
    };
    this._adoptHandle(record, bytes, generation, displayLabel);
    this._liveCount += 1;
    this._liveBytes += bytes;
    this._acquiredCount += 1;
    return makeLease(this, record);
  };

  /* Native entry point: measure with the read-only scanner first, then use the
   * exact same admission/record path as the caller-measured entry point. */
  CodecparLeases.prototype.acquire = function acquire(sourcePtr, generation, label) {
    var displayLabel = boundedLabel(label);
    var bytes = this._measureSource(sourcePtr, displayLabel);
    return this._admit(bytes, generation, label);
  };

  /* Additive caller-measured entry point. It never invokes the scanner and it
   * validates the count before any core call; after that it shares admission,
   * refusal, record shape, counters, poison and release rules with acquire(). */
  CodecparLeases.prototype.acquireMeasured =
    function acquireMeasured(bytes, generation, label) {
      var lease = this._admit(bytes, generation, label);
      this._measuredAcquireCount += 1;
      return lease;
    };

  function resourcesView(resources) {
    if (!resources || typeof resources.snapshot !== 'function') return null;
    var snapshot;
    try {
      snapshot = resources.snapshot();
    } catch (error) {
      return null;
    }
    if (!snapshot || typeof snapshot !== 'object') return null;
    var core = snapshot.core && typeof snapshot.core === 'object' ? snapshot.core : null;
    return Object.freeze({
      liveBytes: scalarOrNull(snapshot.liveBytes),
      liveCount: scalarOrNull(snapshot.liveCount),
      acquiredCount: scalarOrNull(snapshot.acquiredCount),
      refusedCount: scalarOrNull(snapshot.refusedCount),
      faultCount: scalarOrNull(snapshot.faultCount),
      core: core === null ? null : Object.freeze({
        heldBytes: scalarOrNull(core.heldBytes),
        boundBytes: scalarOrNull(core.boundBytes),
        countBound: scalarOrNull(core.countBound),
        countReserved: scalarOrNull(core.countReserved),
        ownedResourceBytes: scalarOrNull(core.ownedResourceBytes),
        countOwnedResources: scalarOrNull(core.countOwnedResources)
      })
    });
  }

  CodecparLeases.prototype.snapshot = function snapshot() {
    return Object.freeze({
      kind: KIND,
      version: VERSION,
      liveCount: this._liveCount,
      liveBytes: this._liveBytes,
      strandedBytes: this._strandedBytes,
      acquiredCount: this._acquiredCount,
      measuredAcquireCount: this._measuredAcquireCount,
      refusedCount: this._refusedCount,
      boundCount: this._boundCount,
      releasedCount: this._releasedCount,
      retainedCount: this._retainedCount,
      releaseFailedCount: this._releaseFailedCount,
      reentrantCount: this._reentrantCount,
      handleAdoptions: this._handleAdoptions,
      handleAdoptFallbacks: this._handleAdoptFallbacks,
      faultCount: this._faultCount,
      faultChargedBytes: this._faultBytes,
      faultsEvicted: this._faultsEvicted,
      lastFault: this._lastFault,
      faults: Object.freeze(this._faults.slice()),
      coverage: COVERAGE,
      resources: resourcesView(this._resources),
      measurement: 'caller-measured-native-payload+nontransactional-core-scan',
      exact: false
    });
  };

  function createCodecparLeases(config) {
    var checked = checkConfig(config);
    var manager = new CodecparLeases(checked.resources, checked.measure, checked.handles);
    /* The factory result is both the native acquirer (leases(sourcePtr,
     * generation, label)) and the diagnostic holder (leases.acquire /
     * leases.measured / leases.snapshot). One manager, one ledger and no extra
     * lease fields. */
    function acquire(sourcePtr, generation, label) {
      return manager.acquire(sourcePtr, generation, label);
    }
    acquire.acquire = acquire;
    function acquireMeasured(bytes, generation, label) {
      return manager.acquireMeasured(bytes, generation, label);
    }
    acquire.measured = acquireMeasured;
    acquire.acquireMeasured = acquireMeasured;
    acquire.snapshot = function snapshot() {
      return manager.snapshot();
    };
    return Object.freeze(acquire);
  }

  return Object.freeze({
    VERSION: VERSION,
    KIND: KIND,
    FAULT_LOG_LIMIT: FAULT_LOG_LIMIT,
    COVERAGE: COVERAGE,
    createCodecparLeases: createCodecparLeases
  });
});
}
}]);
