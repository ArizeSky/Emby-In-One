/* Emby-In-One Web client — playback orchestration helpers (V1.4.4-rc3).
 *
 * Owns the parts of the /play entry flow that must stay out of the already
 * oversized app.js: the pending source/options handoff, the non-blocking
 * episode queue, partial-result detection and the bounded engine-cleanup
 * budget. These helpers hold pure state only; app.js supplies fetching and
 * reactive setters.
 *
 * Work package A acceptance:
 *  - the queue never blocks PlaybackInfo / engine start and can never reject;
 *  - pending selection is consumed synchronously by the entry that uses it;
 *  - a compatibility retry uses only the refreshed TranscodingUrl;
 *  - an engine destroy that overruns its budget is a visible failure instead
 *    of silently stacking decoders.
 */
(function () {
  'use strict';

  const DEFAULT_CLEANUP_BUDGET_MS = 5000;

  // Work package E reports a partial merged candidate set as a recognisable
  // error that still carries the usable items. Single point of truth so every
  // call site agrees on the contract.
  function isPartialResults(error) {
    return !!error && error.name === 'PartialResultsError' && Array.isArray(error.items);
  }

  function partialItems(error) {
    return isPartialResults(error) ? error.items.slice() : [];
  }

  // The router entry stores the chosen source/options for the next play
  // request. startPlayerFromRoute consumes them synchronously so a failed or
  // superseded attempt can never leak its selection into a later request.
  function createPendingSelection() {
    let mediaSourceId = null;
    let options = {};
    return {
      set(id, next) { mediaSourceId = id || null; options = next || {}; },
      peek() { return { mediaSourceId, options }; },
      consume() {
        const current = { mediaSourceId, options };
        mediaSourceId = null;
        options = {};
        return current;
      },
      clear() { mediaSourceId = null; options = {}; },
    };
  }

  // Episode queue: navigation only. start() never rejects — playback does not
  // depend on the full-series list — and a late result is dropped unless its
  // serial, the abort signal and the caller's validity check still match.
  // A partial list keeps the loaded episodes but is never reported as the
  // complete ordered queue.
  function createEpisodeQueue(handlers = {}) {
    let serial = 0;
    return {
      invalidate() { serial++; },
      serial() { return serial; },
      async start(item, signal, isValid) {
        const token = ++serial;
        const alive = () => token === serial && !signal?.aborted && (!isValid || isValid());
        try {
          const episodes = await handlers.load(item, signal);
          if (!alive()) return { token, stale: true };
          handlers.succeed(episodes, { complete: true });
          return { token, episodes, complete: true };
        } catch (error) {
          if (!alive()) return { token, stale: true };
          if (error?.name === 'AbortError') { handlers.abort?.(); return { token, aborted: true }; }
          if (isPartialResults(error)) {
            const items = partialItems(error);
            handlers.succeed(items, { complete: false, error });
            return { token, episodes: items, complete: false, error };
          }
          handlers.fail(error);
          return { token, error };
        }
      },
    };
  }

  // A forced compatibility retry must use the TranscodingUrl returned by this
  // PlaybackInfo call. The original stream path is never a substitute, so a
  // response without a transcode URL fails visibly instead of replaying the
  // same direct file.
  function compatibleStream(mediaSource) {
    const url = mediaSource?.TranscodingUrl;
    if (!url) {
      return { ok: false, message: '上游未提供可用的兼容播放（转码）流，无法强制转码。' };
    }
    return { ok: true, url, fallbackUrl: null };
  }

  // Bounded engine cleanup coordination. app.js tracks every destroy() promise
  // before a new engine may be created; waitForIdle() gives the previous
  // generation a budget and reports a timeout/failure instead of quietly
  // creating another decoder.
  //
  // A destroy() that rejects is a real failure (the engine reports a wedged
  // teardown): the decoder was not released. That is a permanent barrier, not
  // a one-off warning — every later waitForIdle keeps failing. UI navigation
  // (leaving the player, going back to the item page, logging out) must never
  // lift it: the decoder from the old page is still alive, so allowing a new
  // engine would leak another decoder. A cleanup that only hangs keeps its
  // outstanding entry; when that promise eventually settles the gate can make
  // progress again, but a rejection never recovers without a page reload.
  function createCleanupGate(options = {}) {
    const budgetMs = Number(options.budgetMs) || DEFAULT_CLEANUP_BUDGET_MS;
    const setTimer = options.setTimeout || setTimeout;
    const clearTimer = options.clearTimeout || clearTimeout;
    const warn = options.warn || (error => console.warn('Player cleanup failed', error));
    let chain = Promise.resolve();
    let outstanding = 0;
    let failure = null; // permanent unsafe barrier: { error }

    function track(cleanup) {
      outstanding++;
      const settled = Promise.resolve(cleanup).then(
        () => ({ ok: true }),
        error => {
          // Keep the first failure: later cleanup noise must not mask it.
          if (!failure) failure = { error };
          warn(error);
          return { ok: false, error };
        },
      ).then(result => { outstanding--; return result; });
      chain = chain.then(() => settled);
      return settled;
    }

    function failed(reason = 'failed') {
      return { ok: false, reason, error: failure?.error, outstanding };
    }

    function waitOutstanding(budget) {
      if (!outstanding) return Promise.resolve({ ok: true, outstanding: 0 });
      return new Promise(resolve => {
        let done = false;
        const timer = setTimer(() => finish({ ok: false, reason: 'timeout', outstanding }), budget);
        function finish(result) {
          if (done) return;
          done = true;
          clearTimer(timer);
          resolve(result);
        }
        function check() {
          if (done) return;
          if (outstanding > 0) { chain.then(check); return; }
          finish({ ok: true, outstanding: 0 });
        }
        chain.then(check);
      });
    }

    async function waitForIdle(budget = budgetMs) {
      if (failure) return failed();
      const result = await waitOutstanding(Number(budget) || budgetMs);
      // A timeout keeps its outstanding entry: the barrier stays up and the
      // next attempt waits again instead of starting a second decoder.
      if (!result.ok) return result;
      if (failure) return failed();
      return result;
    }

    return {
      track,
      waitForIdle,
      get outstanding() { return outstanding; },
      get unsafe() { return !!failure; },
    };
  }

  window.EIOPlayback = {
    DEFAULT_CLEANUP_BUDGET_MS,
    isPartialResults,
    partialItems,
    createPendingSelection,
    createEpisodeQueue,
    compatibleStream,
    createCleanupGate,
  };
})();
