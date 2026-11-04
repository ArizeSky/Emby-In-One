/*!
 * EIO player buffer policy - pure policy and accounting helpers (UMD).
 *
 * Exposed as `window.EIOBufferPolicy` (or `globalThis` outside a browser) and as
 * `module.exports`. This file is deliberately inert: no DOM, no timers, no
 * network, no storage and no engine calls. It never pauses a video, never
 * changes playback time and never applies a playback rate. It only decides
 * whether an existing pipeline may keep filling and whether a byte reservation
 * may be taken.
 *
 * Two independent pieces live here:
 *  - createBudget(): a per-instance reservation ledger. A caller reserves bytes
 *    for a key before fetching, commits the real size afterwards, and releases
 *    the key when the data is no longer owned. The ledger never invents bytes;
 *    a failed call leaves every counter untouched. `maxBytes` bounds only the
 *    accounted payload/compressed bytes: it does not account for the Map entry,
 *    key strings or browser heap overhead. `maxEntries` is the separate
 *    metadata limit that bounds how many keys may be owned at once, including
 *    zero-byte reservations, so metadata cannot grow without bound;
 *  - createFillGate(): a latched forward-buffer gate. Reaching the high water
 *    mark blocks time-based filling until the low water mark is reached, so the
 *    player fills to the high mark instead of oscillating around a single
 *    threshold. Byte/quota blocking is independent and is never bypassed by a
 *    low forward buffer.
 *
 * Neither function is wired into the player adapter yet. The real pipeline keeps
 * its current heuristics until an adapter change is made and reviewed; passing
 * these tests only proves the pure policy contract.
 */
(function (root, factory) {
  'use strict';
  var api = factory();
  if (typeof module === 'object' && module !== null && module.exports) {
    module.exports = api;
  }
  if (root && typeof root === 'object') {
    root.EIOBufferPolicy = api;
  }
}(
  // `window` first: a sandbox or worker may expose a different globalThis than
  // the page's window, and the web player reads these APIs off `window`.
  typeof window !== 'undefined' ? window :
    (typeof globalThis !== 'undefined' ? globalThis : (typeof self !== 'undefined' ? self : this)),
  function () {
    'use strict';

    var DEFAULT_MAX_BYTES = 300000000;
    // Metadata bound: the byte cap does not pay for Map entries or key strings.
    var DEFAULT_MAX_ENTRIES = 65536;
    var DEFAULT_MAX_FORWARD_SECONDS = 120;
    var DEFAULT_RESUME_FORWARD_SECONDS = 90;
    var RESUME_BYTE_FRACTION = 0.8;
    var RESUME_TIME_FRACTION = 0.75;
    // Bounds key size so a caller cannot pin a huge id string in the ledger.
    var MAX_KEY_LENGTH = 200;

    function isFiniteNumber(value) {
      return typeof value === 'number' && Number.isFinite(value);
    }

    function isSafeInteger(value) {
      return typeof value === 'number' && Number.isSafeInteger(value);
    }

    function isObject(value) {
      return value !== null && typeof value === 'object' && !Array.isArray(value);
    }

    function invalidConfig(name) {
      return new RangeError('EIOBufferPolicy: invalid ' + name);
    }

    // An omitted option takes its default; a supplied option must be valid.
    function readSafeIntegerOption(options, name, fallback) {
      var value = options[name] === undefined ? fallback : options[name];
      if (!isSafeInteger(value) || value < 0) throw invalidConfig(name);
      return value;
    }

    // The entry cap must be positive: a zero cap would make the ledger useless
    // and is almost certainly a configuration mistake, not an empty budget.
    function readPositiveSafeIntegerOption(options, name, fallback) {
      var value = options[name] === undefined ? fallback : options[name];
      if (!isSafeInteger(value) || value <= 0) throw invalidConfig(name);
      return value;
    }

    function readSecondsOption(options, name, fallback) {
      var value = options[name] === undefined ? fallback : options[name];
      if (!isFiniteNumber(value) || value < 0) throw invalidConfig(name);
      return value;
    }

    function readOptions(options, name) {
      if (options === undefined) return {};
      if (!isObject(options)) throw invalidConfig(name + ' options');
      return options;
    }

    // The default low water mark follows a smaller configured high mark so the
    // pair stays ordered; with the default high mark this is exactly 90s.
    function defaultResumeForwardSeconds(maxForwardSeconds) {
      return Math.min(
        DEFAULT_RESUME_FORWARD_SECONDS,
        maxForwardSeconds * RESUME_TIME_FRACTION
      );
    }

    // Shared configuration is one policy, so an inverted low/high pair is
    // rejected instead of silently disabling the latch.
    function assertOrderedPair(low, high, lowName) {
      if (low > high) throw invalidConfig(lowName);
    }

    function isValidKey(key) {
      return typeof key === 'string' && key.length > 0 && key.length <= MAX_KEY_LENGTH;
    }

    /**
     * Creates an isolated byte budget ledger.
     *
     * `maxForwardSeconds`, `resumeBytes` and `resumeForwardSeconds` are accepted
     * and validated so the same policy configuration can be shared with the
     * fill gate and a future pipeline adapter; this ledger only accounts bytes
     * and owned keys.
     * The shared pair must stay ordered: `resumeBytes <= maxBytes` and
     * `resumeForwardSeconds <= maxForwardSeconds`, otherwise a `RangeError` is
     * thrown.
     *
     * @param {Object} [options]
     * @param {number} [options.maxBytes=300000000] non-negative safe integer cap
     *   on accounted bytes only; it excludes Map/browser heap overhead.
     * @param {number} [options.maxEntries=65536] positive safe integer cap on
     *   owned keys. Zero-byte reservations count against this metadata limit.
     * @param {number} [options.maxForwardSeconds=120] accepted policy value.
     * @param {number} [options.resumeBytes=floor(0.8 * maxBytes)] accepted policy
     *   value, derived from the effective `maxBytes`.
     * @param {number} [options.resumeForwardSeconds=min(90, 0.75 *
     *   maxForwardSeconds)] accepted policy value, derived from the effective
     *   `maxForwardSeconds`; the default is 90s only at the default high mark.
     * @param {number} [options.generation=0] non-negative safe integer.
     * @returns {Object} frozen method set: reserve/commit/release/reset/snapshot.
     */
    function createBudget(options) {
      var opts = readOptions(options, 'budget');
      var maxBytes = readSafeIntegerOption(opts, 'maxBytes', DEFAULT_MAX_BYTES);
      var maxEntries = readPositiveSafeIntegerOption(
        opts,
        'maxEntries',
        DEFAULT_MAX_ENTRIES
      );
      // Validated for the shared policy config even though this ledger does not
      // use time thresholds itself.
      var maxForwardSeconds = readSecondsOption(
        opts,
        'maxForwardSeconds',
        DEFAULT_MAX_FORWARD_SECONDS
      );
      var resumeBytes = readSafeIntegerOption(
        opts,
        'resumeBytes',
        Math.floor(maxBytes * RESUME_BYTE_FRACTION)
      );
      var resumeForwardSeconds = readSecondsOption(
        opts,
        'resumeForwardSeconds',
        defaultResumeForwardSeconds(maxForwardSeconds)
      );
      assertOrderedPair(resumeBytes, maxBytes, 'resumeBytes');
      assertOrderedPair(
        resumeForwardSeconds,
        maxForwardSeconds,
        'resumeForwardSeconds'
      );
      var generation = readSafeIntegerOption(opts, 'generation', 0);

      // key -> { reserved, held, open }. `open` stays false after commit so a
      // second commit cannot credit the same reservation twice.
      var entries = new Map();
      var reservedBytes = 0;
      var heldBytes = 0;

      function totalBytes() {
        return reservedBytes + heldBytes;
      }

      // A call is only accepted for the current generation. Both stale and
      // future generations are ignored without touching the ledger.
      function isCurrentGeneration(value) {
        return isSafeInteger(value) && value === generation;
      }

      function reserve(key, bytes, requestedGeneration) {
        if (!isValidKey(key)) return false;
        if (!isSafeInteger(bytes) || bytes < 0) return false;
        if (!isCurrentGeneration(requestedGeneration)) return false;
        if (entries.has(key)) return false;
        // The entry cap is a separate metadata bound: a zero-byte reservation
        // still owns a Map entry, so it cannot bypass this check.
        if (entries.size >= maxEntries) return false;
        // Compare against the remaining space instead of adding first, so a
        // huge request cannot overflow the safe-integer range.
        if (bytes > maxBytes - totalBytes()) return false;
        entries.set(key, { reserved: bytes, held: 0, open: true });
        reservedBytes += bytes;
        return true;
      }

      function commit(key, actualBytes, requestedGeneration) {
        if (!isValidKey(key)) return false;
        if (!isSafeInteger(actualBytes) || actualBytes < 0) return false;
        if (!isCurrentGeneration(requestedGeneration)) return false;
        var entry = entries.get(key);
        if (!entry || !entry.open) return false;
        if (actualBytes > entry.reserved) return false;
        // Move the reservation to held bytes. A smaller actual size refunds the
        // difference; the ledger never enlarges the original reservation.
        reservedBytes -= entry.reserved;
        heldBytes += actualBytes;
        entry.held += actualBytes;
        entry.reserved = 0;
        entry.open = false;
        return true;
      }

      function release(key, requestedGeneration) {
        if (!isValidKey(key)) return false;
        if (!isCurrentGeneration(requestedGeneration)) return false;
        var entry = entries.get(key);
        if (!entry) return false;
        reservedBytes -= entry.reserved;
        heldBytes -= entry.held;
        entries.delete(key);
        return true;
      }

      function reset(nextGeneration) {
        if (!isSafeInteger(nextGeneration) || nextGeneration < 0 ||
            nextGeneration <= generation) {
          throw invalidConfig('reset generation');
        }
        generation = nextGeneration;
        entries.clear();
        reservedBytes = 0;
        heldBytes = 0;
      }

      function snapshot() {
        // A fresh frozen plain object per call: no live entry or map reference
        // can leak through the snapshot.
        return Object.freeze({
          generation: generation,
          maxBytes: maxBytes,
          reservedBytes: reservedBytes,
          heldBytes: heldBytes,
          totalBytes: reservedBytes + heldBytes,
          // Owned keys, including zero-byte and committed entries. The byte
          // counters never include Map/browser heap overhead.
          entryCount: entries.size
        });
      }

      return Object.freeze({
        reserve: reserve,
        commit: commit,
        release: release,
        reset: reset,
        snapshot: snapshot
      });
    }

    /**
     * Creates an isolated latched forward-buffer gate.
     *
     * Time blocking is latched: `forwardSeconds >= maxForwardSeconds` blocks,
     * and only `forwardSeconds <= resumeForwardSeconds` releases. Values between
     * the two thresholds keep the current state, so filling continues up to the
     * high mark instead of stopping at the first threshold crossed.
     *
     * `bytesBlocked`/`quotaBlocked` are sampled on every call and can release
     * independently, but they are never bypassed by a low forward buffer. An
     * unknown forward sample blocks with reason `unknown` and leaves the time
     * latch untouched.
     *
     * @param {Object} [options]
     * @param {number} [options.maxForwardSeconds=120] finite non-negative high mark.
     * @param {number} [options.resumeForwardSeconds=min(90, 0.75 *
     *   maxForwardSeconds)] finite non-negative low mark, derived from the
     *   effective high mark and required to be less than or equal to it. The
     *   default is 90s only at the default high mark; for example a 20s high
     *   mark defaults to a 15s low mark.
     * @returns {Object} frozen update/reset method set.
     */
    function createFillGate(options) {
      var opts = readOptions(options, 'fill gate');
      var maxForwardSeconds = readSecondsOption(
        opts,
        'maxForwardSeconds',
        DEFAULT_MAX_FORWARD_SECONDS
      );
      var resumeForwardSeconds = readSecondsOption(
        opts,
        'resumeForwardSeconds',
        defaultResumeForwardSeconds(maxForwardSeconds)
      );
      assertOrderedPair(
        resumeForwardSeconds,
        maxForwardSeconds,
        'resumeForwardSeconds'
      );
      var generation = 0;
      var timeBlocked = false;

      function result(filling, reason) {
        return Object.freeze({ filling: filling, reason: reason });
      }

      function update(sample) {
        if (!isObject(sample)) return result(false, 'unknown');
        var sampleGeneration = sample.generation === undefined ? 0 : sample.generation;
        if (!isSafeInteger(sampleGeneration) || sampleGeneration !== generation) {
          return result(false, 'staleGeneration');
        }
        var forwardSeconds = sample.forwardSeconds;
        if (!isFiniteNumber(forwardSeconds) || forwardSeconds < 0) {
          // Unknown samples block, but they never clear a latched time block:
          // only a known low sample may do that.
          return result(false, 'unknown');
        }

        if (forwardSeconds >= maxForwardSeconds) {
          timeBlocked = true;
        } else if (forwardSeconds <= resumeForwardSeconds) {
          timeBlocked = false;
        }

        // Any truthy block flag blocks: a falsey value is the explicit release.
        // A low forward buffer can never override these independent blockers.
        if (sample.bytesBlocked) return result(false, 'bytesBlocked');
        if (sample.quotaBlocked) return result(false, 'quotaBlocked');
        if (timeBlocked) return result(false, 'timeBlocked');
        return result(true, 'filling');
      }

      function reset(nextGeneration) {
        if (!isSafeInteger(nextGeneration) || nextGeneration < 0 ||
            nextGeneration <= generation) {
          throw invalidConfig('reset generation');
        }
        generation = nextGeneration;
        timeBlocked = false;
      }

      return Object.freeze({
        update: update,
        reset: reset
      });
    }

    return Object.freeze({
      DEFAULT_MAX_BYTES: DEFAULT_MAX_BYTES,
      DEFAULT_MAX_ENTRIES: DEFAULT_MAX_ENTRIES,
      DEFAULT_MAX_FORWARD_SECONDS: DEFAULT_MAX_FORWARD_SECONDS,
      createBudget: createBudget,
      createFillGate: createFillGate
    });
  }
));
