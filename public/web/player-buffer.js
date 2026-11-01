/*
 * EIOPlayerBuffer: pure synchronous snapshot of buffered media ranges.
 * Reads player/core/host/video state once, never changes the DOM, never
 * installs listeners or timers, and never mutates the player.
 */
(function (root, factory) {
  'use strict';
  if (typeof module === 'object' && module.exports) {
    module.exports = factory(require('./player-progress.js'));
  } else {
    root.EIOPlayerBuffer = factory(root.EIOPlayerProgress);
  }
})(
  // `window` first so the browser global and the vm/worker globalThis can differ.
  typeof window !== 'undefined' ? window :
    (typeof globalThis !== 'undefined' ? globalThis : this),
  function (progress) {
  'use strict';

  var MAX_TRACKS = 2;
  var MAX_RANGES = 128;

  function isNum(value) {
    return typeof value === 'number' && isFinite(value);
  }

  function failed(epoch) {
    return { status: 'unknown', ranges: [], epoch: epoch };
  }

  function epochOf(player) {
    if (!player || typeof player !== 'object') return null;
    return isNum(player._engineGeneration) ? player._engineGeneration : null;
  }

  function unchanged(player, id) {
    return player._engineGeneration === id.rawGeneration &&
      player.core === id.core &&
      player.video === id.video &&
      player.host === id.host;
  }

  function normalize(list) {
    if (!progress || typeof progress.normalizeRanges !== 'function') return null;
    var normalized = progress.normalizeRanges(list);
    if (!Array.isArray(normalized)) return null;
    var out = [];
    for (var i = 0; i < normalized.length; i++) {
      var item = normalized[i];
      if (!item || !isNum(item.start) || !isNum(item.end)) return null;
      out.push({ start: item.start, end: item.end });
    }
    return out;
  }

  function clampToDuration(list, duration) {
    if (!isNum(duration) || duration <= 0) return list;
    var out = [];
    for (var i = 0; i < list.length; i++) {
      var start = Math.min(list[i].start, duration);
      var end = Math.min(list[i].end, duration);
      if (end > start) out.push({ start: start, end: end });
    }
    return out;
  }

  function readTimeRanges(buffered) {
    if (!buffered || typeof buffered !== 'object') {
      throw new TypeError('unsupported TimeRanges');
    }
    var length = buffered.length;
    if (!Number.isFinite(length) || !Number.isInteger(length) ||
        length < 0 || length > MAX_RANGES) {
      throw new TypeError('unsupported TimeRanges length');
    }
    var out = [];
    if (length === 0) return out;
    if (typeof buffered.start !== 'function' || typeof buffered.end !== 'function') {
      throw new TypeError('unsupported TimeRanges accessors');
    }
    for (var i = 0; i < length; i++) {
      var start = buffered.start(i);
      var end = buffered.end(i);
      if (!isNum(start) || !isNum(end) || start < 0 || end < 0 || end < start) {
        throw new TypeError('invalid TimeRanges segment');
      }
      out.push({ start: start, end: end });
    }
    return out;
  }

  function hasMetadata(element) {
    var readyState = element.readyState;
    if (!isNum(readyState) || readyState !== Math.floor(readyState)) return false;
    return readyState >= 1 && readyState <= 4;
  }

  function readNative(id) {
    var video = id.video;
    if (!video || typeof video !== 'object') return null;
    if (typeof id.host.contains !== 'function') return null;
    if (id.host.contains(video) !== true) return null;
    if (!hasMetadata(video)) return null;
    return readTimeRanges(video.buffered);
  }

  function readMse(id) {
    var core = id.core;
    if (!core || typeof core.isMSE !== 'function') return null;
    if (core.isMSE() !== true) return null;
    var host = id.host;
    if (typeof host.querySelectorAll !== 'function') return null;
    var nodes = host.querySelectorAll('video,audio');
    if (!nodes || typeof nodes !== 'object') return null;
    var length = nodes.length;
    if (!Number.isSafeInteger(length) || length < 1 || length > MAX_TRACKS) return null;
    if (!progress || typeof progress.intersectRanges !== 'function') return null;
    var lists = [];
    for (var i = 0; i < length; i++) {
      var element = nodes[i];
      if (!element || typeof element !== 'object') return null;
      if (host.contains(element) !== true) return null;
      if (!hasMetadata(element)) return null;
      lists.push(readTimeRanges(element.buffered));
    }
    var merged = progress.intersectRanges(lists);
    return Array.isArray(merged) ? merged : null;
  }

  function collect(player) {
    if (player._destroyed) return null;
    var host = player.host;
    if (!host || typeof host.contains !== 'function') return null;
    var id = {
      rawGeneration: player._engineGeneration,
      core: player.core,
      video: player.video,
      host: host
    };
    var ranges;
    if (player.mode === 'native') ranges = readNative(id);
    else if (player.mode === 'libmedia') ranges = readMse(id);
    else return null;
    if (!ranges) return null;
    var duration = player.duration;
    if (player._destroyed) return null;
    if (!unchanged(player, id)) return null;
    var normalized = normalize(ranges);
    if (!normalized) return null;
    return clampToDuration(normalized, duration);
  }

  function snapshot(player) {
    var epoch = null;
    var ranges;
    try {
      epoch = epochOf(player);
      if (!player || typeof player !== 'object') return failed(epoch);
      ranges = collect(player);
    } catch (error) {
      return failed(epoch);
    }
    if (!ranges) return failed(epoch);
    var current;
    try {
      current = epochOf(player);
    } catch (error) {
      return failed(epoch);
    }
    if (current !== epoch) return failed(epoch);
    return { status: 'ok', ranges: ranges, epoch: epoch };
  }

  return { snapshot: snapshot, MAX_TRACKS: MAX_TRACKS, MAX_RANGES: MAX_RANGES };
});
