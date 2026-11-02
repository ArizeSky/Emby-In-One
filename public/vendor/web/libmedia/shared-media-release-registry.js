"use strict";
/* Direct last-free domain registry. */
(self.webpackChunkAVPlayer=self.webpackChunkAVPlayer||[]).push([[90204],{
90188:(module,exports,__webpack_require__)=>{
/*!
 * player-shared-media-release.js
 *
 * Standalone registry facade for the direct last-free path. It is a pure
 * metadata manager: it owns no allocator, performs no malloc/free, performs no
 * network access and reads no global "current task" state. The only non-local
 * effect is the synchronous scratch-word exchange in a shared ownership table
 * that proves two descriptor tables are the same physical memory.
 *
 * Wiring contract (caller, not this module):
 *   - prepareDataFree({ data, buffer }) is called synchronously in the true
 *     non-pool atomic-last-reference branch BEFORE the payload free.
 *   - the original allocator free runs unchanged.
 *   - commitDataFree(opaque) is called only after that free returned normally,
 *     in the same realm and call.
 *   - dataFreeFailed(opaque) is called when the original allocator free threw.
 *
 * This module therefore neither observes nor performs a real payload free.
 * Calling its hooks by hand is not proof that a last-free hook is installed.
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
    root.EioSharedMediaRelease = factory();
  }
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
  'use strict';

  var MAX_DOMAINS = 64;
  var MAX_DOMAIN_ID_LENGTH = 200;
  /* Core header words 17..20 are unused by ownership VERSION 1 (the helper uses
   * 0..16). One word per actor slot keeps concurrently registering actors from
   * sharing a scratch word; a table with a smaller header is refused. */
  var SCRATCH_INDEX_BASE = 17;
  var MARKER_BASE = 0x7e10;
  var HEADER_WORDS = 24;
  var DOMAIN_API_VERSION = 1;
  /* Core header indices and entry layout used only by the conservative
   * all-slots-FREE check below (ownership VERSION 1). */
  var HEADER_MAX_ENTRIES = 3;
  var HEADER_ENTRY_WORDS = 4;
  var HEADER_ENTRIES_OFFSET = 5;
  var ENTRY_STATE = 0;
  var ENTRY_FREE = 0;

  var CODES = Object.freeze({
    DOMAIN_API: 'EIO_RELEASE_DOMAIN_API',
    INVALID_DESCRIPTOR: 'EIO_DOMAIN_INVALID_DESCRIPTOR',
    RETIRED: 'EIO_DOMAIN_RETIRED',
    DUPLICATE_TABLE: 'EIO_DOMAIN_DUPLICATE_TABLE',
    TABLE_MISMATCH: 'EIO_DOMAIN_TABLE_MISMATCH',
    REGISTRY_FULL: 'EIO_DOMAIN_REGISTRY_FULL',
    UNKNOWN: 'EIO_DOMAIN_UNKNOWN',
    LOOKUP_FAILED: 'EIO_RELEASE_LOOKUP_FAILED',
    AMBIGUOUS_DOMAIN: 'EIO_RELEASE_AMBIGUOUS_DOMAIN',
    PREPARE_FAILED: 'EIO_RELEASE_PREPARE_FAILED',
    COMMIT_FAILED: 'EIO_RELEASE_COMMIT_FAILED',
    ALLOCATOR_FAILED: 'EIO_RELEASE_ALLOCATOR_FAILED',
    TICKET_INVALID: 'EIO_RELEASE_TICKET_INVALID'
  });

  function codedError(code, message) {
    var error = new Error(message || code);
    error.code = code;
    return error;
  }

  function isSharedTable(value) {
    if (!value) return false;
    try {
      return value instanceof SharedArrayBuffer ||
        Object.prototype.toString.call(value) === '[object SharedArrayBuffer]';
    } catch (error) {
      return false;
    }
  }

  function hasSharedAtomics() {
    return typeof SharedArrayBuffer === 'function' &&
      typeof Atomics === 'object' && Atomics !== null &&
      typeof Atomics.load === 'function' &&
      typeof Atomics.store === 'function';
  }

  function validateDomainApi(domainApi) {
    if (!hasSharedAtomics()) {
      throw codedError('unsupported', 'SharedArrayBuffer and Atomics are required');
    }
    if (!domainApi || typeof domainApi !== 'object') {
      throw codedError(CODES.DOMAIN_API,
        'createReleaseRegistry requires the verified module90187 domain facade');
    }
    if (typeof domainApi.attachDomainDescriptor !== 'function') {
      throw codedError(CODES.DOMAIN_API,
        'domainApi.attachDomainDescriptor must be the module90187 verifier');
    }
    if (domainApi.VERSION !== DOMAIN_API_VERSION) {
      throw codedError(CODES.DOMAIN_API,
        'domainApi.VERSION must be the verified ownership VERSION ' + DOMAIN_API_VERSION);
    }
    var layout = null;
    try {
      layout = domainApi.LAYOUT;
    } catch (error) {
      layout = null;
    }
    if (!layout || typeof layout !== 'object' || layout.headerWords !== HEADER_WORDS) {
      throw codedError(CODES.DOMAIN_API,
        'domainApi.LAYOUT.headerWords must be ' + HEADER_WORDS);
    }
  }

  function readDomainId(descriptor) {
    var domainId = null;
    try {
      domainId = descriptor && descriptor.domainId;
    } catch (error) {
      domainId = null;
    }
    if (typeof domainId !== 'string' || domainId.length === 0 ||
        domainId.length > MAX_DOMAIN_ID_LENGTH) {
      throw codedError(CODES.INVALID_DESCRIPTOR,
        'domainId must be a non-empty string of at most ' + MAX_DOMAIN_ID_LENGTH + ' characters');
    }
    return domainId;
  }

  function tableOf(controller) {
    try {
      var descriptor = controller && controller.descriptor;
      return descriptor && descriptor.sab ? descriptor.sab : null;
    } catch (error) {
      return null;
    }
  }

  /* Physical alias proof, not a byte/config comparison. Write a marker through
   * the candidate table into one spare core header word, read it back through
   * the registered table, and always restore the candidate word to zero. Both
   * words must be zero first so a concurrent probe/corruption fails closed and
   * no existing bytes are charged or changed. */
  function aliasesTable(candidateSab, registeredSab, actorSlot) {
    if (!isSharedTable(candidateSab) || !isSharedTable(registeredSab)) return false;
    var index = SCRATCH_INDEX_BASE + actorSlot;
    var candidateView;
    var registeredView;
    try {
      candidateView = new Int32Array(candidateSab);
      registeredView = new Int32Array(registeredSab);
    } catch (error) {
      return false;
    }
    if (candidateView.length <= index || registeredView.length <= index) return false;
    try {
      if (Atomics.load(candidateView, index) !== 0) return false;
      if (Atomics.load(registeredView, index) !== 0) return false;
    } catch (error) {
      return false;
    }
    var marker = MARKER_BASE + actorSlot;
    try {
      Atomics.store(candidateView, index, marker);
    } catch (error) {
      return false;
    }
    try {
      return Atomics.load(registeredView, index) === marker;
    } catch (error) {
      return false;
    } finally {
      try {
        Atomics.store(candidateView, index, 0);
      } catch (ignored) {
        /* The candidate table is unusable; nothing can be restored. */
      }
    }
  }

  /* The four charge counters are separate atomics, not a transaction: a
   * zero-byte record moves RESERVED -> BOUND (and CLOSING -> FREE) while every
   * counter still reads 0. After the caller-provided quiescent predicates
   * pass, re-check the verified controller's physical table geometry and scan
   * EVERY slot STATE atomically; only all-FREE is empty. Callers have stopped
   * new reserve/bind and a retired domain can only move live records towards
   * FREE, so a conservative non-empty answer is always safe. This performs no
   * allocation, charge, credit or table clear. Unknown or inconsistent
   * geometry fails closed and keeps the entry. */
  function tableSlotsAllFree(controller) {
    var descriptor;
    try {
      descriptor = controller && controller.descriptor;
    } catch (error) {
      return false;
    }
    if (!descriptor || typeof descriptor !== 'object') return false;
    var sab = descriptor.sab;
    var maxEntries = descriptor.maxEntries;
    var entriesOffset = descriptor.entriesOffset;
    var entryWords = descriptor.entryWords;
    if (!isSharedTable(sab)) return false;
    if (typeof maxEntries !== 'number' || !isFinite(maxEntries) ||
        Math.floor(maxEntries) !== maxEntries || maxEntries <= 0) {
      return false;
    }
    if (typeof entriesOffset !== 'number' || !isFinite(entriesOffset) ||
        Math.floor(entriesOffset) !== entriesOffset || entriesOffset < 0) {
      return false;
    }
    if (typeof entryWords !== 'number' || !isFinite(entryWords) ||
        Math.floor(entryWords) !== entryWords || entryWords <= 0) {
      return false;
    }
    var view;
    try {
      view = new Int32Array(sab);
    } catch (error) {
      return false;
    }
    var totalWords = entriesOffset + maxEntries * entryWords;
    if (!isFinite(totalWords) || totalWords > view.length) return false;
    try {
      /* The descriptor must describe the table that was actually verified;
       * mismatched geometry is unknown geometry, not empty. */
      if (Atomics.load(view, HEADER_MAX_ENTRIES) !== maxEntries ||
          Atomics.load(view, HEADER_ENTRY_WORDS) !== entryWords ||
          Atomics.load(view, HEADER_ENTRIES_OFFSET) !== entriesOffset) {
        return false;
      }
      for (var index = 0; index < maxEntries; index += 1) {
        if (Atomics.load(view, entriesOffset + index * entryWords + ENTRY_STATE) !== ENTRY_FREE) {
          return false;
        }
      }
    } catch (error) {
      return false;
    }
    return true;
  }

  function controllerEmpty(controller) {
    var snapshot;
    try {
      snapshot = controller.snapshot();
    } catch (error) {
      return false;
    }
    if (!snapshot || typeof snapshot !== 'object') return false;
    if (!(snapshot.heldBytes === 0 &&
      snapshot.countReserved === 0 &&
      snapshot.countBound === 0 &&
      snapshot.countClosing === 0)) {
      return false;
    }
    return tableSlotsAllFree(controller);
  }

  function createReleaseRegistry(domainApi) {
    validateDomainApi(domainApi);

    var entries = new Map();
    var tickets = new WeakMap();
    var faultCount = 0;
    var lastFault = null;

    function recordGlobalFault(code) {
      faultCount += 1;
      lastFault = code;
    }

    function recordDomainFault(entry, code) {
      entry.faultCount += 1;
      entry.lastFault = code;
    }

    function cleanupRetiredEmpty() {
      var removable = [];
      entries.forEach(function (entry) {
        if (entry.retired === true && controllerEmpty(entry.controller)) {
          removable.push(entry.domainId);
        }
      });
      for (var index = 0; index < removable.length; index += 1) {
        entries.delete(removable[index]);
      }
    }

    function registerDomain(descriptor, actorSlot) {
      var domainId = readDomainId(descriptor);
      var existing = entries.get(domainId);
      if (existing && existing.retired === true) {
        throw codedError(CODES.RETIRED,
          'domain ' + domainId + ' was retired; a retired domain id cannot be re-registered');
      }
      if (!existing && entries.size >= MAX_DOMAINS) {
        throw codedError(CODES.REGISTRY_FULL,
          'release registry already holds the maximum of ' + MAX_DOMAINS + ' domains');
      }
      /* The module90187 verifier proves descriptor kind/version, shared memory
       * domain, nonce and actor scratch before anything is retained here. */
      var controller = domainApi.attachDomainDescriptor(descriptor, actorSlot);
      var candidateTable = tableOf(controller);
      if (!candidateTable) {
        throw codedError(CODES.TABLE_MISMATCH,
          'the verified controller exposes no shared ownership table');
      }
      if (existing) {
        if (!aliasesTable(candidateTable, tableOf(existing.controller), actorSlot)) {
          throw codedError(CODES.TABLE_MISMATCH,
            'domain ' + domainId + ' is already registered for a different physical table');
        }
        return existing.controller;
      }
      var duplicate = null;
      entries.forEach(function (entry) {
        if (duplicate) return;
        if (aliasesTable(candidateTable, tableOf(entry.controller), actorSlot)) {
          duplicate = entry;
        }
      });
      if (duplicate) {
        throw codedError(CODES.DUPLICATE_TABLE,
          'the physical table is already registered as domain ' + duplicate.domainId);
      }
      entries.set(domainId, {
        domainId: domainId,
        controller: controller,
        retired: false,
        faultCount: 0,
        lastFault: null
      });
      return controller;
    }

    function unregisterDomain(domainId) {
      var entry = typeof domainId === 'string' ? entries.get(domainId) : null;
      if (!entry) return { ok: false, code: CODES.UNKNOWN };
      /* Quiescence is the caller's lifecycle assertion: this module can only
       * read the shared predicate, never prove that actors stopped. */
      if (controllerEmpty(entry.controller) === true) {
        entries.delete(entry.domainId);
        return { ok: true, retained: false };
      }
      entry.retired = true;
      return { ok: true, retained: true };
    }

    function prepareDataFree(payload) {
      try {
        var matches = [];
        var lookupFailed = false;
        entries.forEach(function (entry) {
          if (lookupFailed) return;
          var result;
          try {
            result = entry.controller.lookup(payload);
          } catch (error) {
            lookupFailed = true;
            return;
          }
          if (!result || typeof result !== 'object' || result.ok !== true) {
            lookupFailed = true;
            return;
          }
          if (result.found === true) {
            matches.push({ entry: entry, controller: entry.controller });
          }
        });
        if (lookupFailed) {
          recordGlobalFault(CODES.LOOKUP_FAILED);
          return null;
        }
        if (matches.length === 0) return null;
        if (matches.length > 1) {
          recordGlobalFault(CODES.AMBIGUOUS_DOMAIN);
          return null;
        }
        var match = matches[0];
        var prepared;
        try {
          prepared = match.controller.prepareFinalFree(payload);
        } catch (error) {
          recordDomainFault(match.entry, CODES.PREPARE_FAILED);
          return null;
        }
        if (!prepared || prepared.ok !== true || !prepared.ticket ||
            typeof prepared.ticket !== 'object') {
          recordDomainFault(match.entry, CODES.PREPARE_FAILED);
          return null;
        }
        /* The public object is deliberately empty: it carries no pointer,
         * nonce or controller field, and only its exact identity in this
         * private WeakMap is redeemable. */
        var opaque = Object.freeze({});
        tickets.set(opaque, {
          controller: match.controller,
          inner: prepared.ticket,
          entry: match.entry
        });
        return opaque;
      } catch (error) {
        recordGlobalFault(CODES.PREPARE_FAILED);
        return null;
      }
    }

    function commitDataFree(opaque) {
      if (opaque === null || opaque === undefined) {
        return { ok: true, tracked: false };
      }
      try {
        var record = tickets.get(opaque);
        if (!record) return { ok: false, code: CODES.TICKET_INVALID };
        /* Consume before the controller call so a throwing/refusing commit can
         * never be replayed for a second credit. */
        tickets.delete(opaque);
        var committed;
        try {
          committed = record.controller.commitFinalFree(record.inner, { dataFreed: true });
        } catch (error) {
          recordDomainFault(record.entry, CODES.COMMIT_FAILED);
          return { ok: false, code: CODES.COMMIT_FAILED };
        }
        if (!committed || committed.ok !== true) {
          recordDomainFault(record.entry, CODES.COMMIT_FAILED);
          return { ok: false, code: CODES.COMMIT_FAILED };
        }
        cleanupRetiredEmpty();
        return { ok: true, tracked: true, releasedBytes: committed.releasedBytes };
      } catch (error) {
        recordGlobalFault(CODES.COMMIT_FAILED);
        return { ok: false, code: CODES.COMMIT_FAILED };
      }
    }

    function dataFreeFailed(opaque) {
      if (opaque === null || opaque === undefined) {
        return { ok: true, tracked: false };
      }
      try {
        var record = tickets.get(opaque);
        if (!record) {
          /* No valid original-free-failure evidence was handed in, so this is
           * an invalid ticket, not a domain allocator fault. */
          recordGlobalFault(CODES.TICKET_INVALID);
          return { ok: false, code: CODES.TICKET_INVALID };
        }
        tickets.delete(opaque);
        /* Keep the CLOSING/held charge: no credit, no table clear, no retry. */
        recordDomainFault(record.entry, CODES.ALLOCATOR_FAILED);
        return { ok: true, tracked: true };
      } catch (error) {
        recordGlobalFault(CODES.TICKET_INVALID);
        return { ok: false, code: CODES.TICKET_INVALID };
      }
    }

    function getReleaseRegistrySnapshot() {
      var domains = [];
      entries.forEach(function (entry) {
        domains.push(Object.freeze({
          domainId: entry.domainId,
          retired: entry.retired === true,
          faultCount: entry.faultCount,
          lastFault: entry.lastFault,
          ownership: entry.controller.snapshot()
        }));
      });
      /* Counters are read one by one: this is a snapshot, not a transaction,
       * and it exposes no aggregate that claims to be exact. */
      return Object.freeze({
        registeredDomains: entries.size,
        faultCount: faultCount,
        lastFault: lastFault,
        domains: Object.freeze(domains)
      });
    }

    return Object.freeze({
      registerDomain: registerDomain,
      unregisterDomain: unregisterDomain,
      prepareDataFree: prepareDataFree,
      commitDataFree: commitDataFree,
      dataFreeFailed: dataFreeFailed,
      getReleaseRegistrySnapshot: getReleaseRegistrySnapshot
    });
  }

  return Object.freeze({ createReleaseRegistry: createReleaseRegistry });
});
/*__EIO_SHARED_MEDIA_RELEASE_FACTORY_END__*/
}
}]);
