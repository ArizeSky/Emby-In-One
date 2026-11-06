/* Emby playback DTOs populated from confirmed Web player state.
 * API contract: https://dev.emby.media/reference/RestAPI/PlaystateService/postSessionsPlayingProgress.html
 * PlaybackStopInfo is a separate DTO: do not copy progress-only fields into it.
 * Client UA/Device identity belongs to the backend's per-upstream policy.
 */
(function () {
  'use strict';

  const PROGRESS_FIELDS = ['PositionTicks', 'IsPaused', 'CanSeek', 'RunTimeTicks',
    'IsMuted', 'VolumeLevel', 'PlayMethod', 'PlaybackRate', 'AudioStreamIndex', 'SubtitleStreamIndex'];
  const METHODS = ['DirectPlay', 'DirectStream', 'Transcode'];
  const CHANGES = [
    ['IsPaused', 'StateChange'], ['PlayMethod', 'QualityChange'],
    ['PlaybackRate', 'PlaybackRateChange'], ['AudioStreamIndex', 'AudioTrackChange'],
    ['SubtitleStreamIndex', 'SubtitleTrackChange'], ['IsMuted', 'VolumeChange'],
    ['VolumeLevel', 'VolumeChange'],
  ];

  function snapshot(player) {
    // A missing optional engine measurement stays absent. The small fallback
    // also supports adapters that only expose the common player interface.
    const measured = player.playbackState || {};
    return { PositionTicks: player.positionTicks, IsPaused: !player.playing,
      PlaybackRate: player.playbackRate, ...measured };
  }

  function validField(key, value) {
    if (['IsPaused', 'CanSeek', 'IsMuted'].includes(key)) return typeof value === 'boolean';
    if (key === 'PlayMethod') return METHODS.includes(value);
    if (key === 'PlaybackRate') return typeof value === 'number' && Number.isFinite(value) && value > 0;
    if (!Number.isSafeInteger(value)) return false;
    if (key === 'VolumeLevel') return value >= 0 && value <= 100;
    if (['AudioStreamIndex', 'SubtitleStreamIndex'].includes(key)) return value >= -1;
    return value >= 0;
  }

  function payload(player, event, extra = {}) {
    const state = { ...snapshot(player), ...extra };
    const fields = event === 'Stopped' ? ['PositionTicks'] : PROGRESS_FIELDS;
    const result = {};
    for (const key of fields) if (validField(key, state[key])) result[key] = state[key];
    if (event === 'Progress') result.EventName = extra.EventName || 'TimeUpdate';
    return result;
  }

  function watch(player, notify) {
    let active = false;
    let previous = null;
    function start() { previous = payload(player, 'Progress'); active = true; }
    function stop() { active = false; }
    function observe() {
      // Decoder replacement has transient empty/paused state. Report the
      // restored engine once recovery completes, not those intermediate values.
      if (!active || player._recovering || player._destroyed) return;
      const current = payload(player, 'Progress');
      const change = CHANGES.find(([key]) => current[key] !== undefined && current[key] !== previous[key]);
      previous = current;
      if (!change) return;
      const eventName = change[0] === 'IsPaused'
        ? (current.IsPaused ? 'Pause' : 'Unpause') : change[1];
      notify({ EventName: eventName });
    }
    return { start, stop, observe };
  }

  window.EIOPlaybackReporting = { payload, watch };
})();
