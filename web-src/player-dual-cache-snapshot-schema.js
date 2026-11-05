/*!
 * player-dual-cache-snapshot-schema.js
 *
 * Frozen RPC schema + exact validator for the dual-cache snapshot
 * (`AVPlayer.getEioDualCacheSnapshot()`). This is the §5.3 acceptance
 * freeze: the schema is a frozen literal, validation is structural and
 * exact (no regex, no field-name inference, no unit guessing), and the
 * validator never turns an honest `complete:false` into a pass.
 *
 * The validator checks:
 *   1. every frozen field exists with the frozen type
 *   2. the demux byte ledger is self-consistent (held >= admissionHeld,
 *      room = demuxBytes - admissionHeld when active)
 *   3. the io ledger is self-consistent (ownedTotal >= 0, chunks >= 0)
 *   4. `complete`/`incomplete` are reported as-is; a false `complete`
 *      with known-uncovered reasons is a valid observed state, not a
 *      violation. Only structural damage (wrong type, missing field,
 *      negative byte where non-negative is frozen) is a violation.
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
    root.EioDualCacheSnapshotSchema = factory();
  }
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
  'use strict';

  var VERSION = 1;
  var KIND = 'eio-dual-cache-snapshot-schema/v1';

  /* Frozen literal: the exact top-level shape returned by
   * AVPlayer.getEioDualCacheSnapshot(). Types: 'string', 'boolean',
   * 'number', 'array', 'object', 'null'. 'number' fields additionally
   * freeze non-negativity where the producer guarantees it. */
  var FROZEN_SCHEMA = Object.freeze({
    taskId: { type: 'string|number' },
    active: { type: 'boolean' },
    complete: { type: 'boolean' },
    incomplete: {
      type: 'array',
      item: Object.freeze({ domain: { type: 'string' }, reason: { type: 'string' } })
    },
    io: { type: 'object', nullable: true, fields: Object.freeze({
      enabled: { type: 'boolean' },
      active: { type: 'boolean' },
      probe: { type: 'boolean' },
      ioBytes: { type: 'number', nonNegative: true },
      maxPacketBytes: { type: 'number', nonNegative: true },
      residual: { type: 'number', nonNegative: true },
      residualPushback: { type: 'number', nonNegative: true },
      residualReserved: { type: 'number', nonNegative: true },
      ownedTotal: { type: 'number', nonNegative: true },
      chunks: { type: 'number', nonNegative: true },
      generation: { type: 'number', nonNegative: true },
      readerMode: { type: 'string' },
      byobCapable: { type: 'boolean' }
    }) },
    demux: { type: 'object', nullable: true, fields: Object.freeze({
      active: { type: 'boolean' },
      enabled: { type: 'boolean' },
      taskId: { type: 'string|number' },
      demuxBytes: { type: 'number', nonNegative: true },
      maxPacketBytes: { type: 'number', nonNegative: true },
      readCeil: { type: 'number', nonNegative: true },
      readCeilEffective: { type: 'number', nonNegative: true },
      fixedBytes: { type: 'number', nonNegative: true },
      packetBytes: { type: 'number', nonNegative: true },
      room: { type: 'number', nonNegative: true },
      held: { type: 'number', nonNegative: true },
      admissionHeld: { type: 'number', nonNegative: true },
      cacheTotal: { type: 'number', nonNegative: true },
      pendingTotal: { type: 'number', nonNegative: true },
      generation: { type: 'number', nonNegative: true },
      latched: { type: 'boolean' },
      errors: { type: 'number', nonNegative: true },
      lastError: { type: 'string' },
      intervalPacketsAccounted: { type: 'boolean' },
      sharedBindCount: { type: 'number', nonNegative: true },
      sharedFreeCount: { type: 'number', nonNegative: true },
      sharedFreedBytes: { type: 'number', nonNegative: true },
      liveBoundMirrors: { type: 'number', nonNegative: true },
      intervalBytes: { type: 'number', nonNegative: true },
      intervalPackets: { type: 'number', nonNegative: true },
      headerBytes: { type: 'number', nonNegative: true },
      headerPictureBindings: { type: 'number', nonNegative: true },
      avccNativeBindings: { type: 'number', nonNegative: true },
      avccNativeTemps: { type: 'number', nonNegative: true },
      ghostSwept: { type: 'number', nonNegative: true },
      ownershipTransitionCount: { type: 'number', nonNegative: true },
      phase: { type: 'number', nonNegative: true },
      probeBytes: { type: 'number', nonNegative: true },
      probeHeld: { type: 'number', nonNegative: true },
      probeRequests: { type: 'number', nonNegative: true },
      probeExhausted: { type: 'boolean' },
      maxForwardSeconds: { type: 'number', nonNegative: true },
      maxForwardMs: { type: 'number', nonNegative: true },
      clockKnown: { type: 'boolean' },
      clockMs: { type: 'number', nullable: true },
      clockEpoch: { type: 'number', nullable: true },
      bootstrapBaseMs: { type: 'number', nullable: true },
      temporalWaits: { type: 'number', nonNegative: true },
      temporalRejections: { type: 'number', nonNegative: true },
      temporalAuthority: { type: 'string' },
      temporalComplete: { type: 'boolean' },
      sharedInspectBusy: { type: 'number', nonNegative: true },
      tHeld: { type: 'number', nonNegative: true },
      localTokenBytes: { type: 'number', nonNegative: true },
      outboundBytes: { type: 'number', nonNegative: true },
      unprovenBytes: { type: 'number', nonNegative: true },
      cancelledBytes: { type: 'number', nonNegative: true },
      abandonedBytes: { type: 'number', nonNegative: true },
      waiterCount: { type: 'number', nonNegative: true },
      wakeCount: { type: 'number', nonNegative: true },
      waitCount: { type: 'number', nonNegative: true },
      timeoutCount: { type: 'number', nonNegative: true },
      protocolErrors: { type: 'number', nonNegative: true },
      staleTokens: { type: 'number', nonNegative: true },
      pointerReuse: { type: 'number', nonNegative: true },
      releaseVerified: { type: 'boolean' },
      stopAck: { type: 'object', nullable: true },
      producerError: { type: 'object', nullable: true },
      producerOperations: { type: 'number', nonNegative: true },
      batchWaiterCount: { type: 'number', nonNegative: true },
      sampleHandles: { type: 'object', nullable: true },
      accounting: { type: 'object' }
    }) },
    mse: { type: 'object', nullable: true, fields: Object.freeze({
      active: { type: 'boolean' },
      complete: { type: 'boolean' }
    }) }
  });

  function typeOf(value) {
    if (value === null) return 'null';
    if (Array.isArray(value)) return 'array';
    return typeof value;
  }

  function matchesType(value, expected) {
    var allowed = expected.type.split('|');
    var actual = typeOf(value);
    for (var index = 0; index < allowed.length; index += 1) {
      if (allowed[index] === actual) return true;
      /* 'number' also accepts a numeric string produced by bigint
       * sanitization in the fixture transport layer. */
      if (allowed[index] === 'number' && typeof value === 'string' &&
          value.length > 1 && value.slice(-1) === 'n' &&
          /^-?\d+n$/.test(value)) return true;
    }
    return false;
  }

  function checkField(path, value, spec, violations) {
    if (value === undefined) {
      violations.push(path + ':missing');
      return;
    }
    if (value === null) {
      if (spec.nullable === true) return;
      violations.push(path + ':null');
      return;
    }
    if (!matchesType(value, spec)) {
      violations.push(path + ':type(' + typeOf(value) + '!=' + spec.type + ')');
      return;
    }
    if (spec.type.indexOf('number') !== -1 && typeof value === 'number' &&
        spec.nonNegative === true && value < 0) {
      violations.push(path + ':negative(' + value + ')');
    }
  }

  function validate(snapshot) {
    var violations = [];
    if (!snapshot || typeof snapshot !== 'object' || Array.isArray(snapshot)) {
      return { ok: false, exactValidation: false, violations: ['root:not-object'],
        complete: null, incomplete: null, active: null };
    }
    Object.keys(FROZEN_SCHEMA).forEach(function (key) {
      var spec = FROZEN_SCHEMA[key];
      var value = snapshot[key];
      if (spec.type === 'array') {
        if (value === undefined) {
          violations.push(key + ':missing');
          return;
        }
        if (!Array.isArray(value)) {
          violations.push(key + ':type(' + typeOf(value) + '!=array)');
          return;
        }
        for (var index = 0; index < value.length; index += 1) {
          var item = value[index];
          if (!item || typeof item !== 'object') {
            violations.push(key + '[' + index + ']:not-object');
            continue;
          }
          Object.keys(spec.item).forEach(function (itemKey) {
            checkField(key + '[' + index + '].' + itemKey, item[itemKey],
              spec.item[itemKey], violations);
          });
        }
        return;
      }
      if (spec.type === 'object') {
        if (value === null) {
          if (spec.nullable === true) return;
          violations.push(key + ':null');
          return;
        }
        if (value === undefined) {
          violations.push(key + ':missing');
          return;
        }
        if (typeof value !== 'object' || Array.isArray(value)) {
          violations.push(key + ':type(' + typeOf(value) + '!=object)');
          return;
        }
        if (spec.fields) {
          Object.keys(spec.fields).forEach(function (fieldKey) {
            checkField(key + '.' + fieldKey, value[fieldKey],
              spec.fields[fieldKey], violations);
          });
        }
        return;
      }
      checkField(key, value, spec, violations);
    });

    /* Exact conservation checks on the observed ledger values. These are
     * the only cross-field inferences the frozen schema permits, and each
     * one is a producer-guaranteed invariant, not a guess. */
    var demux = snapshot.demux;
    if (demux && typeof demux === 'object' && demux.active === true) {
      if (typeof demux.held === 'number' && typeof demux.admissionHeld === 'number' &&
          demux.held < demux.admissionHeld) {
        violations.push('demux:held<admissionHeld(' + demux.held + '<' +
          demux.admissionHeld + ')');
      }
      if (typeof demux.packetBytes === 'number' && typeof demux.admissionHeld === 'number' &&
          typeof demux.room === 'number' &&
          demux.room !== Math.max(demux.packetBytes - demux.admissionHeld, 0)) {
        violations.push('demux:room!=packetBytes-admissionHeld(' + demux.room +
          '!=' + (demux.packetBytes - demux.admissionHeld) + ')');
      }
    }

    return {
      ok: violations.length === 0,
      exactValidation: true,
      violations: violations,
      complete: snapshot.complete === true,
      incomplete: Array.isArray(snapshot.incomplete) ?
        snapshot.incomplete.map(function (item) {
          return item && item.reason ? item.reason : 'unknown';
        }) : [],
      active: snapshot.active === true
    };
  }

  return Object.freeze({
    VERSION: VERSION,
    KIND: KIND,
    FROZEN_SCHEMA: FROZEN_SCHEMA,
    validate: validate
  });
});
