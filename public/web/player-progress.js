/*!
 * EIO player progress - pure data module (UMD).
 *
 * Exposed as `window.EIOPlayerProgress` (globalThis/self when no window exists)
 * and as `module.exports`. No DOM, no network, no timer and no storage access:
 * plain data in, plain data out.
 */
(function (root, factory) {
  'use strict';
  var api = factory();
  if (typeof module === 'object' && module !== null && module.exports) {
    module.exports = api;
  }
  if (root && typeof root === 'object') {
    root.EIOPlayerProgress = api;
  }
}(
  // `window` first: a sandbox (vm) or worker may expose a different globalThis
  // than the page's window, and the player reads these APIs off `window`.
  typeof window !== 'undefined' ? window :
    (typeof globalThis !== 'undefined' ? globalThis : (typeof self !== 'undefined' ? self : this)),
  function () {
    'use strict';

    // Hard cap on normalised segments. Exceeding it yields null (unknown); the
    // list is never silently truncated.
    var MAX_RANGES = 128;
    var UNKNOWN_TEXT = '\u2014';
    var PLAYED_COLOR = 'var(--eio-progress-played, #3b82f6)';
    var BUFFERED_COLOR = 'var(--eio-progress-buffered, #f3f4f6)';
    var TRACK_COLOR = 'var(--eio-progress-track, #111827)';

    // Native finite numbers only: strings, NaN, Infinity and objects fail.
    function isFiniteNumber(value) {
      return typeof value === 'number' && Number.isFinite(value);
    }

    // A duration can clamp only when finite and strictly positive.
    function resolveLimit(duration) {
      return isFiniteNumber(duration) && duration > 0 ? duration : null;
    }

    function percentOf(numerator, denominator) {
      if (!isFiniteNumber(numerator) || numerator <= 0 ||
          !isFiniteNumber(denominator) || denominator <= 0) {
        return 0;
      }
      var percent = (numerator / denominator) * 100;
      if (!isFiniteNumber(percent) || percent <= 0) return 0;
      if (percent >= 100) return 100;
      return Math.round(percent * 1000) / 1000;
    }

    // Reads one `[start, end]` / `{ start, end }` entry; null when malformed.
    // Both values must be native finite numbers >= 0 and not reversed.
    function readRange(entry) {
      var start;
      var end;
      if (Array.isArray(entry)) {
        if (entry.length !== 2) return null;
        start = entry[0];
        end = entry[1];
      } else if (entry !== null && typeof entry === 'object') {
        start = entry.start;
        end = entry.end;
      } else {
        return null;
      }
      if (!isFiniteNumber(start) || !isFiniteNumber(end)) return null;
      if (start < 0 || end < 0) return null;
      if (end < start) return null;
      return { start: start, end: end };
    }

    /**
     * Normalises a buffered range list: sorted, merged, clamped to `duration`.
     * @param {Array} input `[{ start, end }]` or `[[start, end]]`.
     * @param {number} [duration] finite positive value used for clamping.
     * @returns {Array|null} `[]` = known empty, `null` = unknown or invalid.
     */
    function normalizeRanges(input, duration) {
      if (!Array.isArray(input)) return null;
      if (input.length > MAX_RANGES) return null;
      var limit = resolveLimit(duration);
      var segments = [];
      var index;
      for (index = 0; index < input.length; index += 1) {
        var range = readRange(input[index]);
        if (range === null) return null;
        var start = range.start;
        var end = range.end;
        if (limit !== null) {
          if (start > limit) start = limit;
          if (end > limit) end = limit;
        }
        // Segments emptied by clamping (or already degenerate) hold nothing.
        if (end <= start) continue;
        segments.push({ start: start, end: end });
      }
      if (segments.length === 0) return [];
      segments.sort(function (left, right) {
        if (left.start !== right.start) return left.start - right.start;
        return left.end - right.end;
      });
      var merged = [];
      for (index = 0; index < segments.length; index += 1) {
        var current = segments[index];
        var last = merged[merged.length - 1];
        if (last && current.start <= last.end) {
          // Overlapping or touching segments collapse into one.
          if (current.end > last.end) last.end = current.end;
        } else {
          merged.push({ start: current.start, end: current.end });
        }
      }
      return merged.length > MAX_RANGES ? null : merged;
    }

    function intersectTwo(left, right) {
      var out = [];
      var i = 0;
      var j = 0;
      while (i < left.length && j < right.length) {
        var a = left[i];
        var b = right[j];
        var start = a.start > b.start ? a.start : b.start;
        var end = a.end < b.end ? a.end : b.end;
        if (end > start) {
          var last = out[out.length - 1];
          if (last && start <= last.end) {
            if (end > last.end) last.end = end;
          } else {
            out.push({ start: start, end: end });
          }
        }
        if (a.end < b.end) i += 1;
        else if (b.end < a.end) j += 1;
        else { i += 1; j += 1; }
      }
      return out;
    }

    /**
     * Intersects the buffered ranges of one or more active tracks, segment-wise.
     * @param {Array} groups one range list per active track.
     * @param {number} [duration] finite positive value used for clamping.
     * @returns {Array|null} `null` when there is no track or any track is
     *   unknown, otherwise the intersection (`[]` = no common buffer).
     */
    function intersectRanges(groups, duration) {
      if (!Array.isArray(groups) || groups.length === 0) return null;
      if (groups.length > MAX_RANGES) return null;
      var normalized = [];
      var sawEmpty = false;
      var index;
      // Validate every track first: any unknown track wins over a known-empty
      // one regardless of order. Only then may an empty group short-circuit.
      for (index = 0; index < groups.length; index += 1) {
        var group = normalizeRanges(groups[index], duration);
        if (group === null) return null;
        if (group.length === 0) sawEmpty = true;
        normalized.push(group);
      }
      if (sawEmpty) return [];
      var result = normalized[0];
      for (index = 1; index < normalized.length; index += 1) {
        result = intersectTwo(result, normalized[index]);
        if (result.length === 0) return [];
        if (result.length > MAX_RANGES) return null;
      }
      return result;
    }

    /**
     * Buffered seconds playable forward from `currentTime` inside the segment
     * holding that position. Never crosses a gap; a segment end reports 0.
     * @returns {number|null} `null` for unknown ranges or an invalid position.
     */
    function continuousBufferedSeconds(ranges, currentTime, duration) {
      if (!isFiniteNumber(currentTime) || currentTime < 0) return null;
      var limit = resolveLimit(duration);
      if (limit !== null && currentTime > limit) return null;
      var normalized = normalizeRanges(ranges, duration);
      if (normalized === null) return null;
      var index;
      for (index = 0; index < normalized.length; index += 1) {
        var range = normalized[index];
        if (range.start > currentTime) break;
        if (currentTime <= range.end) {
          var remaining = range.end - currentTime;
          return remaining > 0 ? remaining : 0;
        }
      }
      return 0;
    }

    // Only `MediaSource.Bitrate` (bits per second) is trusted; anything else is
    // unknown. Result is megabits per second.
    function getSourceBitrateMbps(mediaSource) {
      if (mediaSource === null || typeof mediaSource !== 'object') return null;
      var bitrate = mediaSource.Bitrate;
      if (!isFiniteNumber(bitrate) || bitrate <= 0) return null;
      return bitrate / 1e6;
    }

    // Decimal MB/s (1e6 bytes per second). 0 is valid. Only an explicit `ok`
    // sample status yields a number; every other or missing status is unknown,
    // so a stale rate can never be shown. Paused state is decided upstream.
    function formatReceiveRate(bytesPerSec, status) {
      if (status !== 'ok') return UNKNOWN_TEXT;
      if (!isFiniteNumber(bytesPerSec) || bytesPerSec < 0) return UNKNOWN_TEXT;
      return (bytesPerSec / 1e6).toFixed(1) + ' MB/s';
    }

    function formatSourceBitrate(mbps) {
      if (!isFiniteNumber(mbps) || mbps <= 0) return UNKNOWN_TEXT;
      return mbps.toFixed(1) + ' Mbps';
    }

    function formatBufferedSeconds(seconds) {
      if (!isFiniteNumber(seconds) || seconds < 0) return UNKNOWN_TEXT;
      return Math.floor(seconds) + 's';
    }

    /**
     * Builds the layered progress background.
     *
     * Only numbers and the three fixed CSS custom properties are emitted, so no
     * caller supplied string can reach a style value. Layer order is
     * bottom-to-top: track, buffered (white islands), played (blue). Every
     * buffered island is kept separately so gaps stay visibly unbuffered; the
     * buffer is never collapsed into a contiguous prefix.
     * @param {{currentTime: number, duration: number, ranges: Array}} options
     * @returns {{track: Object, buffered: (Object|null), played: Object}|null}
     *   `null` for an unusable duration or position; `buffered: null` means the
     *   buffer is unknown while played is still rendered. A known buffer is
     *   `{ backgroundColor, segments: [{ startPercent, widthPercent }] }` with
     *   at most 128 segments; `segments: []` means nothing is buffered. All
     *   percentages are numbers in [0, 100]; the UI appends the percent unit.
     */
    function progressLayers(options) {
      if (options === null || typeof options !== 'object') return null;
      var duration = options.duration;
      if (!isFiniteNumber(duration) || duration <= 0) return null;
      var currentTime = options.currentTime;
      if (!isFiniteNumber(currentTime) || currentTime < 0) return null;
      // Played progress always follows the real position, never a preview.
      var time = currentTime > duration ? duration : currentTime;
      // Normalise once: every real buffered island is preserved as its own
      // segment, so a later island cannot be painted as part of a prefix.
      var pieces = normalizeRanges(options.ranges, duration);
      var buffered = null;
      if (pieces !== null) {
        buffered = {
          backgroundColor: BUFFERED_COLOR,
          segments: []
        };
        var index;
        for (index = 0; index < pieces.length; index += 1) {
          var piece = pieces[index];
          buffered.segments.push({
            startPercent: percentOf(piece.start, duration),
            widthPercent: percentOf(piece.end - piece.start, duration)
          });
        }
      }
      return {
        track: { backgroundColor: TRACK_COLOR },
        buffered: buffered,
        played: {
          backgroundColor: PLAYED_COLOR,
          widthPercent: percentOf(time, duration)
        }
      };
    }

    return Object.freeze({
      normalizeRanges: normalizeRanges,
      intersectRanges: intersectRanges,
      continuousBufferedSeconds: continuousBufferedSeconds,
      getSourceBitrateMbps: getSourceBitrateMbps,
      formatReceiveRate: formatReceiveRate,
      formatSourceBitrate: formatSourceBitrate,
      formatBufferedSeconds: formatBufferedSeconds,
      progressLayers: progressLayers
    });
  }
));
