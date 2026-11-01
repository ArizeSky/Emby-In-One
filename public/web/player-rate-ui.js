(function (global) {
  'use strict';

  var RATES = Object.freeze([0.5, 1, 1.25, 1.5, 2]);
  var DEFAULT_RATE = 1;
  var ERR_INVALID = '不支持该播放速度';
  var ERR_PLAYER = '播放器尚未就绪，无法设置播放速度';
  var ERR_SET = '设置播放速度失败，请稍后重试';

  function isRate(rate) {
    return typeof rate === 'number' && isFinite(rate) && RATES.indexOf(rate) !== -1;
  }

  function sameNumber(a, b) {
    return typeof a === 'number' && isFinite(a) && a === b;
  }

  function create(deps) {
    deps = deps || {};
    var vue = deps.Vue || global.Vue;
    if (!vue || typeof vue.ref !== 'function') {
      throw new Error('EIOPlaybackRateUI requires Vue.ref');
    }

    var playbackRate = vue.ref(DEFAULT_RATE);
    var ratePending = vue.ref(false);
    var rateError = vue.ref('');
    var seq = 0;
    var disposed = false;

    function readPlayer() {
      return typeof deps.getPlayer === 'function' ? deps.getPlayer() : null;
    }

    function activeOk() {
      if (disposed) return false;
      return typeof deps.isActive === 'function' ? deps.isActive() === true : true;
    }

    function isCurrent(token, player) {
      return token === seq && activeOk() && player === readPlayer();
    }

    function notifyActivity() {
      if (typeof deps.onActivity === 'function') deps.onActivity();
    }

    async function selectPlaybackRate(rate) {
      if (disposed || !activeOk()) return false;

      if (!isRate(rate)) {
        rateError.value = ERR_INVALID;
        return false;
      }
      if (ratePending.value) return false;

      var player = readPlayer();
      if (!player || typeof player.setPlaybackRate !== 'function') {
        rateError.value = ERR_PLAYER;
        return false;
      }

      var token = ++seq;
      ratePending.value = true;
      rateError.value = '';
      notifyActivity();

      try {
        var actual = await player.setPlaybackRate(rate);
        if (!isCurrent(token, player)) return false;

        var confirmed = player.playbackRate;
        if (!isRate(actual) || actual !== rate || !sameNumber(confirmed, rate)) {
          throw new Error('playback rate not applied');
        }

        playbackRate.value = actual;
        ratePending.value = false;
        if (typeof deps.onSuccess === 'function') deps.onSuccess(actual);
        return true;
      } catch (err) {
        if (isCurrent(token, player)) {
          ratePending.value = false;
          rateError.value = ERR_SET;
        }
        return false;
      } finally {
        if (!disposed && token === seq) {
          ratePending.value = false;
          if (isCurrent(token, player)) notifyActivity();
        }
      }
    }

    function reset() {
      seq += 1;
      ratePending.value = false;
      rateError.value = '';
      playbackRate.value = DEFAULT_RATE;
    }

    function dispose() {
      if (disposed) return;
      disposed = true;
      reset();
    }

    return {
      playbackRate: playbackRate,
      ratePending: ratePending,
      rateError: rateError,
      selectPlaybackRate: selectPlaybackRate,
      reset: reset,
      dispose: dispose
    };
  }

  global.EIOPlaybackRateUI = { create: create, RATES: RATES };
})(typeof globalThis !== 'undefined' ? globalThis : window);
