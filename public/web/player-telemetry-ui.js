/*!
 * EIO player telemetry HUD - reactive view model (UMD).
 *
 * Exposed as `window.EIOPlayerTelemetry` (or `globalThis` outside a browser)
 * and as `module.exports`. Pure view state: the engine snapshot that app.js
 * already forwards to `playerUI.acceptState` is handed to `update()` once, and
 * the module exposes
 * the three HUD texts plus the layered progress geometry. No DOM, no timer, no
 * network, no storage and - deliberately - no visibility state: the HUD lives
 * inside the existing control layer, so `playerUI.controlsVisible` alone
 * decides whether it is on screen.
 *
 * Rules enforced here (see docs/web-player-progress-theme-execution-plan.md):
 *  - the blue "played" layer follows the real engine position (`snapshot.time`)
 *    only; an optimistic seek preview must never move it;
 *  - `epoch` is the engine generation. It only increases, so a lower epoch is a
 *    stale sample and the whole frame is dropped without ordering or sorting.
 *    A newer epoch discards the previous ranges/rate/source first, so a new
 *    generation never inherits the old buffer, rate or source bitrate;
 *  - an explicitly unknown or malformed sample clears the previous value: an
 *    unknown metric renders as the em dash, never as a fake 0. A valid 0 keeps
 *    rendering as 0;
 *  - the receive rate is independent of the paused flag, because a paused
 *    player can still be receiving media;
 *  - a snapshot that simply omits a telemetry field (for example a
 *    subtitle-only state emission) keeps the last real sample instead of
 *    blanking the HUD.
 */
(function (root, factory) {
  'use strict';
  var progress = (typeof module === 'object' && module !== null && module.exports)
    ? require('./player-progress.js')
    : (root && root.EIOPlayerProgress);
  var api = factory(progress);
  if (typeof module === 'object' && module !== null && module.exports) {
    module.exports = api;
  }
  if (root && typeof root === 'object') {
    root.EIOPlayerTelemetry = api;
  }
}(
  typeof window !== 'undefined'
    ? window
    : (typeof globalThis !== 'undefined' ? globalThis : (typeof self !== 'undefined' ? self : this)),
  function (progress) {
    'use strict';

    var STATUS_OK = 'ok';
    var STATUS_UNKNOWN = 'unknown';
    var UNKNOWN_TEXT = '\u2014';

    function isNumber(value) {
      return typeof value === 'number' && Number.isFinite(value);
    }

    function has(object, key) {
      return Object.prototype.hasOwnProperty.call(object, key);
    }

    // An engine generation marker: a safe non-negative integer or nothing.
    // Server revision / timestamp values are deliberately not consulted.
    function readEpoch(value) {
      return Number.isSafeInteger(value) && value >= 0 ? value : null;
    }

    // One frame owns one generation. The buffer marker wins because it also
    // carries the ranges; the receive sample is only a fallback for a frame
    // that has no usable buffer marker.
    function readFrameEpoch(snapshot) {
      var buffered = has(snapshot, 'buffered') ? snapshot.buffered : null;
      if (buffered !== null && typeof buffered === 'object') {
        var bufferedEpoch = readEpoch(buffered.epoch);
        if (bufferedEpoch !== null) return bufferedEpoch;
      }
      var rate = has(snapshot, 'receiveRate') ? snapshot.receiveRate : null;
      if (rate !== null && typeof rate === 'object') {
        return readEpoch(rate.epoch);
      }
      return null;
    }

    // Telemetry present without a readable generation marker cannot be ordered
    // against the accepted epoch, so it may refresh its own fields but must not
    // move the position or source bitrate of that epoch. A legacy pure-time or
    // subtitle-only snapshot carries neither field and stays supported.
    function hasUnorderedTelemetry(snapshot) {
      var buffered = has(snapshot, 'buffered') ? snapshot.buffered : null;
      if (buffered !== null && typeof buffered === 'object') return true;
      var rate = has(snapshot, 'receiveRate') ? snapshot.receiveRate : null;
      return rate !== null && typeof rate === 'object';
    }

    function initialState() {
      return {
        epoch: null,              // accepted engine generation
        time: null,               // real position in seconds
        duration: null,           // media duration in seconds
        ranges: null,             // null = unknown buffer, [] = known empty
        receiveStatus: STATUS_UNKNOWN,
        receiveBytes: null,       // bytes per second
        sourceMbps: null          // source bitrate in Mbps
      };
    }

    // The data module is loaded immediately before this one (see web.html). If a
    // stale cached page ever loads them out of order, the HUD degrades to dashes
    // instead of throwing inside the engine's state callback.
    function isLinked(api) {
      if (!api || typeof api !== 'object') return false;
      var required = ['normalizeRanges', 'continuousBufferedSeconds', 'progressLayers',
        'formatReceiveRate', 'formatSourceBitrate', 'formatBufferedSeconds'];
      for (var index = 0; index < required.length; index += 1) {
        if (typeof api[required[index]] !== 'function') return false;
      }
      return true;
    }

    /**
     * @param {{reactive?: Function}} [deps] `reactive` is Vue's reactive()
     *   factory; the module wraps its own state with it so templates track the
     *   getters below. Called without it the module stays plain (fake tests).
     */
    function create(deps) {
      var reactive = deps && typeof deps.reactive === 'function' ? deps.reactive : null;
      var linked = isLinked(progress);
      var state = initialState();
      if (reactive) state = reactive(state);

      function clearBuffer() { state.ranges = null; }
      function clearRate() {
        state.receiveStatus = STATUS_UNKNOWN;
        state.receiveBytes = null;
      }
      function clearSource() { state.sourceMbps = null; }

      /**
       * Generation gate.
       * @returns {boolean|null} `false` = stale (drop the sample), `true` =
       *   applicable, `null` = no usable epoch marker (treated as unknown).
       */
      function acceptEpoch(epoch) {
        if (epoch === null) return null;
        if (state.epoch === null) { state.epoch = epoch; return true; }
        if (epoch < state.epoch) return false;
        if (epoch > state.epoch) {
          // A new engine generation never inherits the old ranges, rate or
          // source bitrate. This happens before time/source are applied.
          state.epoch = epoch;
          clearBuffer();
          clearRate();
          clearSource();
        }
        return true;
      }

      function applyTime(snapshot) {
        state.time = isNumber(snapshot.time) && snapshot.time >= 0 ? snapshot.time : null;
        state.duration = isNumber(snapshot.duration) && snapshot.duration > 0 ? snapshot.duration : null;
      }

      function applyBuffered(snapshot) {
        if (!has(snapshot, 'buffered')) return; // no new sample: keep the last one
        var buffered = snapshot.buffered;
        if (buffered === null || typeof buffered !== 'object') { clearBuffer(); return; }
        var accepted = acceptEpoch(readEpoch(buffered.epoch));
        if (accepted === false) return; // stale generation
        if (accepted === null || buffered.status !== STATUS_OK) { clearBuffer(); return; }
        // normalizeRanges returns null for any malformed segment, so a bad
        // sample becomes "unknown" rather than a partial prefix buffer.
        var ranges = linked ? progress.normalizeRanges(buffered.ranges) : null;
        state.ranges = Array.isArray(ranges) ? ranges : null;
      }

      function applyReceive(snapshot) {
        if (!has(snapshot, 'receiveRate')) return;
        var rate = snapshot.receiveRate;
        if (rate === null || typeof rate !== 'object') { clearRate(); return; }
        // `sampledAt` is not reinterpreted here: rebuilding the measuring
        // window is the adapter's job, this module only shows declared samples.
        var accepted = acceptEpoch(readEpoch(rate.epoch));
        if (accepted === false) return;
        if (accepted === null || rate.status !== STATUS_OK ||
            !isNumber(rate.bytesPerSec) || rate.bytesPerSec < 0) {
          clearRate();
          return;
        }
        state.receiveStatus = STATUS_OK;
        state.receiveBytes = rate.bytesPerSec;
      }

      function applySource(snapshot) {
        if (!has(snapshot, 'sourceBitrateMbps')) return;
        var mbps = snapshot.sourceBitrateMbps;
        state.sourceMbps = isNumber(mbps) && mbps > 0 ? mbps : null;
      }

      /**
       * Applies one engine snapshot. A non-object snapshot clears everything;
       * individual fields are validated and unknown values clear the old ones.
       */
      function update(snapshot) {
        if (snapshot === null || typeof snapshot !== 'object') { reset(); return; }
        var frameEpoch = readFrameEpoch(snapshot);
        if (frameEpoch !== null) {
          // Reject the whole frame before it can touch time or source: the old
          // buffer/rate gates alone let a stale frame mix old buffering with a
          // new position/source. A new epoch is accepted and cleared here too.
          if (acceptEpoch(frameEpoch) === false) return;
        } else if (hasUnorderedTelemetry(snapshot)) {
          // Unreadable generation marker: only the telemetry fields themselves
          // may become unknown; position and source stay with the last ordered
          // frame. This prevents a low epoch disguised as an invalid integer
          // from bypassing the gate.
          applyBuffered(snapshot);
          applyReceive(snapshot);
          return;
        }
        applyTime(snapshot);
        // Buffered first: when it announces a new generation it drops the old
        // rate, and a rate sample of that same generation then re-applies.
        applyBuffered(snapshot);
        applyReceive(snapshot);
        applySource(snapshot);
      }

      function reset() {
        var fresh = initialState();
        state.epoch = fresh.epoch;
        state.time = fresh.time;
        state.duration = fresh.duration;
        state.ranges = fresh.ranges;
        state.receiveStatus = fresh.receiveStatus;
        state.receiveBytes = fresh.receiveBytes;
        state.sourceMbps = fresh.sourceMbps;
      }

      var api = {
        update: update,
        reset: reset,
        // Layered progress geometry (track / white islands / blue played) or
        // null while the duration or position is unusable.
        get layers() {
          if (!linked) return null;
          return progress.progressLayers({
            currentTime: state.time,
            duration: state.duration,
            ranges: state.ranges
          });
        },
        get receiveText() {
          if (!linked) return UNKNOWN_TEXT;
          return progress.formatReceiveRate(state.receiveBytes, state.receiveStatus);
        },
        get sourceText() {
          if (!linked) return UNKNOWN_TEXT;
          return progress.formatSourceBitrate(state.sourceMbps);
        },
        // Continuous seconds from the real position inside its own segment;
        // two islands are never added together.
        get bufferText() {
          if (!linked) return UNKNOWN_TEXT;
          return progress.formatBufferedSeconds(
            progress.continuousBufferedSeconds(state.ranges, state.time, state.duration));
        }
      };

      return reactive ? reactive(api) : api;
    }

    return Object.freeze({ create: create });
  }
));
