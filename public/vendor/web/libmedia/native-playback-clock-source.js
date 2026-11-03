"use strict";
(self.webpackChunkAVPlayer=self.webpackChunkAVPlayer||[]).push([[90209],{
90195:(module,exports,__webpack_require__)=>{
(function (root, factory) {
  'use strict';
  if (typeof module === 'object' && module.exports) module.exports = factory();
  else root.EioNativePlaybackClockSource = factory();
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
  'use strict';

  function create(options) {
    if (!options || !options.writer || typeof options.isCurrent !== 'function' ||
        typeof options.readNativeState !== 'function') throw new TypeError('native clock source dependencies required');
    var writer = options.writer;
    var disposed = false;
    var pending = false;
    var targetMs = null;
    var confirmedMs = null;
    var reason = 'NO_CONFIRMED_SAMPLE';
    var accepted = 0;
    var rejected = 0;

    function millis(seconds) {
      if (typeof seconds !== 'number' || !Number.isFinite(seconds) || seconds < 0) return null;
      var value = Math.floor(seconds * 1000);
      return Number.isSafeInteger(value) ? value : null;
    }

    function current() {
      try { return !disposed && options.isCurrent() === true; }
      catch (_) { return false; }
    }

    function beginSeek(seconds) {
      if (!current()) return false;
      writer.invalidate();
      pending = true;
      targetMs = millis(seconds);
      confirmedMs = null;
      reason = 'SEEK_GAP';
      return true;
    }

    function setTarget(seconds) {
      if (!current() || !pending) return false;
      targetMs = millis(seconds);
      reason = 'TARGET_PENDING';
      return targetMs !== null;
    }

    function reject(value) {
      rejected += 1;
      reason = value;
      return false;
    }

    function sample(kind) {
      if (!current()) return reject('STALE_SOURCE');
      if (kind !== 'timeupdate' && kind !== 'pause' && kind !== 'seeked') return reject('INVALID_KIND');
      var epoch = writer.epoch();
      var state;
      try { state = options.readNativeState(); }
      catch (_) { return reject('NATIVE_READ_FAILED'); }
      if (!state || !current() || epoch !== writer.epoch()) return reject('STALE_SOURCE');
      var position = millis(state.seconds);
      if (position === null) return reject('INVALID_POSITION');
      if (state.seeking !== false) return reject('SEEKING');
      if (!Number.isInteger(state.readyState) || state.readyState < 3) return reject('NOT_READY');
      if (typeof state.paused !== 'boolean') return reject('INVALID_STATE');
      if (kind !== 'timeupdate') {
        // Pause/seeked never create or raise an anchor. A later timeupdate must
        // independently confirm movement beyond any programmatically set target.
        if (pending || confirmedMs === null) return reject('NO_CONFIRMED_SAMPLE');
        if (kind === 'pause' && !state.paused) return reject('INVALID_STATE');
        if (position < confirmedMs || position > confirmedMs + 50) return reject('REAFFIRM_ONLY');
        if (!writer.publish(confirmedMs, epoch, state.paused)) return reject('STALE_EPOCH');
      } else {
        if (pending && (targetMs === null || position <= targetMs)) return reject('TARGET_ECHO');
        if (confirmedMs !== null && position < confirmedMs) return reject('BACKWARDS_POSITION');
        if (!writer.publish(position, epoch, state.paused)) return reject('STALE_EPOCH');
        confirmedMs = position;
        pending = false;
        targetMs = null;
      }
      accepted += 1;
      reason = 'CONFIRMED';
      return true;
    }

    function seeking() {
      if (!current()) return false;
      if (pending) return true;
      var state;
      try { state = options.readNativeState(); }
      catch (_) { return false; }
      return beginSeek(state && state.seconds);
    }

    function dispose() {
      if (disposed) return;
      options.keepWriter === true ? writer.invalidate() : writer.close();
      disposed = true;
      confirmedMs = null;
      targetMs = null;
      reason = 'DISPOSED';
    }

    return Object.freeze({ sample: sample, beginSeek: beginSeek, setTarget: setTarget,
      seeking: seeking, dispose: dispose, snapshot: function () {
        return Object.freeze({ known: confirmedMs !== null && current(), mediaMs: confirmedMs,
          pendingTarget: pending, targetMs: targetMs, epoch: writer.epoch(),
          accepted: accepted, rejected: rejected, reason: reason,
          complete: false, admissionAuthority: false });
      } });
  }

  return Object.freeze({ create: create });
});
}
}]);
