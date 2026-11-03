"use strict";
(self.webpackChunkAVPlayer=self.webpackChunkAVPlayer||[]).push([[90211],{
90197:(module,exports,__webpack_require__)=>{
/*!
 * Independent mux producer lifecycle gate (UMD / CommonJS).
 * This is admission/ownership tracking, NOT a serialized producer lock.
 * Call close() before drain(); drain also waits for an owned mutation token.
 * Only explicit finish() releases ownership, even after invalidation/failure.
 */
(function (root, factory) {
  'use strict';
  if (typeof module === 'object' && module && module.exports) {
    module.exports = factory();
  } else if (typeof define === 'function' && define.amd) {
    define([], factory);
  } else {
    root.EioMuxProducers = factory();
  }
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
  'use strict';

  function fault(code) {
    var error = new Error(code);
    error.code = code;
    return error;
  }

  function labelOf(value) {
    return typeof value === 'string' ? value.slice(0, 128) : '';
  }

  function create() {
    var epoch = 0;
    var closed = false;
    var retired = false;
    var reason = '';
    var nextId = 0;
    var active = new Map();
    var mutation = null;
    var waiters = new Set();

    function idle() {
      return active.size === 0 && mutation === null;
    }

    function notify() {
      if (!idle()) return;
      waiters.forEach(function (waiter) {
        clearTimeout(waiter.timer);
        waiter.resolve(true);
      });
      waiters.clear();
    }

    function close(value) {
      closed = true;
      epoch += 1;
      reason = labelOf(value);
      return true;
    }

    function makeTicket(label, exclusive) {
      var id = ++nextId;
      var admittedEpoch = epoch;
      var finished = false;
      var record = { id: id, label: labelOf(label), epoch: admittedEpoch };
      if (exclusive) mutation = record;
      else active.set(id, record);

      function valid() {
        return !finished && !retired && epoch === admittedEpoch &&
          (exclusive ? closed && mutation === record : !closed);
      }

      function guard() {
        if (!valid()) throw fault('ERR_MUX_PRODUCERS_STALE');
        return true;
      }

      function finish() {
        if (finished) return false;
        finished = true;
        if (exclusive) mutation = null;
        else active.delete(id);
        notify();
        return true;
      }

      return Object.freeze({ valid: valid, guard: guard, finish: finish });
    }

    function enter(label) {
      if (retired) throw fault('ERR_MUX_PRODUCERS_RETIRED');
      if (closed) throw fault('ERR_MUX_PRODUCERS_CLOSED');
      return makeTicket(label, false);
    }

    function beginMutation(label) {
      if (retired) throw fault('ERR_MUX_PRODUCERS_RETIRED');
      if (!closed) throw fault('ERR_MUX_PRODUCERS_NOT_CLOSED');
      if (!idle()) throw fault('ERR_MUX_PRODUCERS_BUSY');
      return makeTicket(label, true);
    }

    function reopen() {
      if (retired) throw fault('ERR_MUX_PRODUCERS_RETIRED');
      if (!closed) throw fault('ERR_MUX_PRODUCERS_NOT_CLOSED');
      if (!idle()) throw fault('ERR_MUX_PRODUCERS_BUSY');
      epoch += 1;
      closed = false;
      reason = '';
      return true;
    }

    function retire() {
      if (retired) return false;
      retired = true;
      close('retired');
      return true;
    }

    // A timeout rejects only this waiter. It never reopens the gate, cancels
    // work, or finishes tickets. A later drain can still observe real release.
    function drain(timeoutMs) {
      if (!closed) return Promise.reject(fault('ERR_MUX_PRODUCERS_NOT_CLOSED'));
      if (timeoutMs === undefined) timeoutMs = 5000;
      if (typeof timeoutMs !== 'number' || !Number.isFinite(timeoutMs) ||
          timeoutMs < 0 || timeoutMs > 2147483647) {
        return Promise.reject(fault('ERR_MUX_PRODUCERS_INVALID_TIMEOUT'));
      }
      if (idle()) return Promise.resolve(true);
      return new Promise(function (resolve, reject) {
        var waiter = { resolve: resolve, timer: null };
        waiter.timer = setTimeout(function () {
          waiters.delete(waiter);
          reject(fault('ERR_MUX_PRODUCERS_DRAIN_TIMEOUT'));
        }, timeoutMs);
        waiters.add(waiter);
      });
    }

    function copyRecord(record) {
      return { id: record.id, label: record.label, epoch: record.epoch };
    }

    function snapshot() {
      return {
        epoch: epoch,
        closed: closed,
        retired: retired,
        reason: reason,
        active: active.size,
        producers: Array.from(active.values(), copyRecord),
        mutation: mutation === null ? null : copyRecord(mutation),
        waiters: waiters.size
      };
    }

    return Object.freeze({
      enter: enter,
      close: close,
      drain: drain,
      reopen: reopen,
      retire: retire,
      beginMutation: beginMutation,
      snapshot: snapshot
    });
  }

  return Object.freeze({ create: create });
});
}
}]);
