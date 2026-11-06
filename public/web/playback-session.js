/* Emby-In-One Web player — playback session (pure extraction from app.js).
 *
 * Owns the /play lifecycle that used to be inlined in app.js: the router entry
 * point, engine instance + ownership generation, the playerUI / rateUI /
 * telemetry adapters, version and track switching, subtitle selection, and the
 * scrub / volume / fullscreen controls.
 *
 * `create(deps)` is pure with respect to the page module: it receives the Vue
 * reactive factories, the api helpers, the page refs, the data/orchestrator
 * helpers and the toast sink, and returns the exact identifiers app.js
 * destructures. It reads no module-level page state; the guards, catches and
 * generation checks keep the previous inlined ownership semantics.
 *
 * Playback identity is strict (parent-frozen protocol; the wire contract lives
 * in public/web/playback-identity.js). Negotiation, media / subtitle /
 * fallback / transcode URLs and Playing / Progress / Stopped are bound to the
 * selected MediaSource's own attempt. There is no legacy URL rebuild and no
 * local-token fallback, and an identity protocol failure is observable instead
 * of being swallowed by an empty catch.
 */
(function () {
  'use strict';

  function create(deps = {}) {
    const {
      ref, computed, reactive, nextTick,
      api, safeError,
      allItems, episodeOrder, defaultAudio, audioTrackLabel, fmtTicks,
      isPartialResults, createPendingSelection, createEpisodeQueue,
      compatibleStream, createCleanupGate,
      page, navPage, toasts,
      playbackFeatures, refreshPlaybackFeatures, dualCacheBudget,
      openItem, refreshIcons,
      DETAIL_FIELDS, STARTUP_SEEK_TIMEOUT_MS,
    } = deps;

    /* ---- player ---- */
    const playerContainer = ref(null);
    // Template-visible liveness flag: the raw `player` variable is a plain
    // local and must never be referenced from the template (it would read as
    // undefined there and permanently disable the seek bar).
    const playerAlive = ref(false);
    const playerMode = ref('native');
    const playItemId = ref('');
    const playingItem = ref(null);
    const playSrc = ref(null);
    const playMediaSourceId = ref(null);
    const wasmPlaying = ref(false);
    const wasmTime = ref(0);
    const wasmDuration = ref(0);
    const wasmTimeHuman = computed(() => fmtTicks(wasmTime.value * 10000000));
    const wasmDurationHuman = computed(() => fmtTicks(wasmDuration.value * 10000000));
    const wasmSubTrack = ref(-1);
    const playerMessage = ref('');
    const playVersions = ref([]);
    const playAudioIndex = ref(-1);
    const episodeQueue = ref([]);
    const episodeQueueLoading = ref(false);
    const episodeQueuePartial = ref(false);
    const queueError = ref('');
    const trackBusy = ref(false);
    const subtitleBusy = ref(false);
    const playerVolume = ref(1);
    const lastNonZeroVolume = ref(1);
    const seekPreview = ref(null);
    let lastSubtitleIndex = -1;

    const previousEpisode = computed(() => {
      const i = episodeQueue.value.findIndex(e => e.Id === playingItem.value?.Id);
      return i > 0 ? episodeQueue.value[i - 1] : null;
    });
    const nextEpisode = computed(() => {
      const i = episodeQueue.value.findIndex(e => e.Id === playingItem.value?.Id);
      return i >= 0 ? episodeQueue.value[i + 1] || null : null;
    });
    const audioTracks = computed(() => playSrc.value?.MediaStreams?.filter(s => s.Type === 'Audio') || []);
    const subtitleTracks = computed(() => playSrc.value?.MediaStreams?.filter(s => s.Type === 'Subtitle') || []);

    const versionOptions = computed(() => (playVersions.value || []).map(s => ({ value: s.Id, label: s.Name || '默认版本' })));
    const audioOptions = computed(() => audioTracks.value.map(s => ({ value: s.Index, label: audioTrackLabel(s) })));
    const subtitleOptions = computed(() => [{ value: -1, label: '关闭字幕' },
      ...subtitleTracks.value.map(s => ({ value: s.Index, label: audioTrackLabel(s) }))]);

    const playerLoadingText = computed(() => {
      if (playerUI.operation === 'version') return '正在切换版本，进度已保留…';
      if (playerUI.operation === 'audio') return '正在切换音轨，进度已保留…';
      return playerMessage.value || '正在加载播放信息…';
    });
    const seekTitle = computed(() => '跳转至 ' + fmtTicks((seekPreview.value ?? wasmTime.value) * 10000000));
    const seekPercent = computed(() => {
      const dur = wasmDuration.value;
      const value = seekPreview.value ?? wasmTime.value;
      if (!dur || !Number.isFinite(dur) || dur <= 0) return 0;
      return Math.max(0, Math.min(100, (value / dur) * 100));
    });

    let player = null;
    let playSessionId = null;
    // Consumed synchronously by startPlayerFromRoute so a failed or
    // superseded request never leaks its source/options into a later play.
    const pendingSelection = createPendingSelection();
    const engineCleanup = createCleanupGate();
    let playbackStarted = false;
    let playerItemId = null;
    let playbackGeneration = 0;
    let playbackRequest = null;
    let playerReturnRoute = '#/home';
    let seekCommitTimer = null;
    let seekTarget = null;
    let seekInFlight = null;      // pending engine-seek target: stale time events must not move the thumb
    let seekInFlightTimer = null;
    let subtitleAttempt = null;
    let subtitleSwitchSerial = 0;
    let startupSubtitleError = null;
    // A player implementation may resolve the default subtitle in the
    // background (package B) and report it through onState. Keep that report
    // separate from the startup snapshot so it is not overwritten below.
    let reportedSubtitleIndex = null;
    let lastAudioError = '';

    const playerUI = reactive(EIOPlayerUI.create({
      getContainer: () => playerContainer.value,
      getPlayer: () => player,
      isActive: () => navPage.value === 'player' && page.value === 'app',
      actions: {
        togglePause: () => wasmTogglePause(),
        seekBy: delta => seekBy(delta),
        adjustVolume: delta => changeVolume(playerVolume.value + delta),
        toggleMute: () => toggleMute(),
        toggleFullscreen: () => toggleFullscreen(),
        toggleSubtitle: () => toggleSubtitleKey(),
        exit: () => exitPlayer(),
      },
    }));

    // Playback-rate helper (public/web/player-rate-ui.js). Same lifecycle as
    // playerUI: one instance per setup, fed the shared engine snapshot, no
    // timers of its own.
    const rateUI = EIOPlaybackRateUI.create({
      getPlayer: () => player,
      isActive: () => navPage.value === 'player' && page.value === 'app',
      onActivity: () => playerUI.activity(),
      onSuccess: () => playerUI.closeMenu(),
    });

    /* ---- player telemetry HUD ---- */
    // Pure view model for the top-right HUD. It is created once per setup,
    // consumes the same engine snapshot as playerUI.acceptState, owns no
    // timer and no visibility state, and never touches the seek preview.
    // The module is optional for setup: a stale cache or a blocked script
    // must not become a ReferenceError that aborts the whole page. A missing
    // factory degrades to a static "unknown" HUD (no state, no algorithm
    // copied here), so update/reset stay harmless no-ops.
    const playerTelemetry = (typeof EIOPlayerTelemetry === 'object' && EIOPlayerTelemetry !== null &&
      typeof EIOPlayerTelemetry.create === 'function')
      ? EIOPlayerTelemetry.create({ reactive })
      : { update() {}, reset() {}, layers: null, receiveText: '\u2014', sourceText: '\u2014', bufferText: '\u2014' };

    /* ---- strict playback identity ---- */
    // playback-identity.js owns the wire contract (paths, attempt fields, the
    // capability and the error shape). A missing module is a hard, observable
    // failure: this session never rebuilds a legacy URL or a local-token
    // request to keep playing.
    function protocol() {
      const mod = typeof EIOPlaybackIdentity === 'object' && EIOPlaybackIdentity !== null
        ? EIOPlaybackIdentity : null;
      if (!mod
        || typeof mod.negotiationPath !== 'function'
        || typeof mod.attemptFromMediaSource !== 'function'
        || typeof mod.strictResourceUrl !== 'function'
        || typeof mod.strictMediaSource !== 'function'
        || typeof mod.postSessionEvent !== 'function'
        || typeof mod.deleteActiveEncodings !== 'function'
        || typeof mod.isPlaybackProtocolError !== 'function'
        || typeof mod.createError !== 'function') {
        throw new Error('播放身份协议模块不可用，已拒绝旧版播放路径');
      }
      return mod;
    }

    function isIdentityFailure(error) {
      const mod = typeof EIOPlaybackIdentity === 'object' && EIOPlaybackIdentity !== null
        ? EIOPlaybackIdentity : null;
      if (mod && typeof mod.isPlaybackProtocolError === 'function') return mod.isPlaybackProtocolError(error);
      return !!(error && (error.name === 'PlaybackProtocolError'
        || (typeof error.kind === 'string' && error.kind.startsWith('playback-'))));
    }

    // One strict session event for the instance's own attempt. The module
    // stamps PlaySessionId (the candidate's own session, not the shared
    // top-level one) and EioAttemptId, so a late or conflicting attempt cannot
    // be sent by mistake.
    function reportPlaybackEvent(instance, event, extra, options = {}) {
      const attempt = instance.eioAttempt;
      if (!attempt) return Promise.reject(new Error('播放尝试缺失，无法上报播放事件'));
      let identity;
      try { identity = protocol(); } catch (error) { return Promise.reject(error); }
      if (event === 'Stopped') instance.eioReporting?.stop();
      const payload = {
        ItemId: instance.eioItemId ?? playerItemId,
        MediaSourceId: instance.mediaSource?.Id ?? attempt.mediaSourceId,
        ...EIOPlaybackReporting.payload(instance, event, extra),
      };
      return identity.postSessionEvent({
        attempt, event, payload,
        signal: options.signal,
        keepalive: options.keepalive === true,
      });
    }

    // An identity protocol failure while reporting is never an empty catch: it
    // becomes exactly one observable failure per attempt (the existing error
    // card, with the existing retry context) and never a false ACK. Ordinary
    // transient reporting failures keep the previous best-effort semantics.
    function reportEventFailure(instance, error) {
      if (!isIdentityFailure(error)) return;
      if (instance.eioIdentityFailureReported) return;
      instance.eioIdentityFailureReported = true;
      handlePlaybackFailure(error, instance.eioGeneration,
        buildRetryContext(playingItem.value, playMediaSourceId.value, {},
          instance.positionTicks / 10000000, !instance.playing));
    }

    // A rejected stop has no retry path (the player is already being torn
    // down): record it without credentials and leave reclamation to the
    // server's bounded schedule instead of resurrecting the attempt.
    function reportStopFailure(error) {
      if (!isIdentityFailure(error)) return;
      console.warn('[EIO] playback stop rejected by identity protocol',
        { kind: String(error.kind || ''), retryable: error.retryable === true });
    }

    // Unload stop: the browser guarantees no delivery, so this is a single
    // keepalive POST carrying the instance's own capability + attempt. Failure
    // is reported once and never retried; the server still reclaims.
    function cleanupEncodings(instance, keepalive = false) {
      if (!instance?.eioAttempt?.playSessionId || instance.eioEncodingStopSent) return;
      instance.eioEncodingStopSent = true;
      protocol().deleteActiveEncodings({attempt:instance.eioAttempt,keepalive})
        .catch(reportStopFailure);
    }
    function sendUnloadStop() {
      const instance = player;
      if (!instance || instance.eioUnloadStopSent) return;
      instance.eioUnloadStopSent = true;
      cleanupEncodings(instance, true);
      reportPlaybackEvent(instance, 'Stopped',
        { PositionTicks: instance.positionTicks }, { keepalive: true })
        .catch(error => {
          if (!isIdentityFailure(error)) return;
          console.warn('[EIO] unload playback stop rejected by identity protocol',
            { kind: String(error.kind || '') });
        });
    }

    if (typeof window !== 'undefined' && typeof window.addEventListener === 'function') {
      window.addEventListener('pagehide', sendUnloadStop);
    }

    /* ---- playback entry ---- */
    let playItemSerial = 0; // bumped per playItem call to detect a superseded Series resolution

    async function playItem(item, mediaSourceId, options = {}) {
      // Bumping a serial lets a Series resolution that is still awaiting notice
      // that a newer play request superseded it. Without it, a slow NextUp for
      // episode A can resolve after the user already picked episode B and
      // overwrite the newer selection.
      const serial = ++playItemSerial;
      if (navPage.value !== 'player') playerReturnRoute = window.location.hash || '#/home';
      // A series has no media sources of its own: PlaybackInfo against a
      // series fails upstream. Resolve to NextUp or the first episode.
      if (item?.Type === 'Series') {
        const resolution = await resolveSeriesEpisode(item.Id);
        if (serial !== playItemSerial) return; // superseded by a newer playItem call
        if (!resolution.episode) {
          // A partial episode list must not be turned into a silent "first
          // episode" guess: the ordering of an incomplete list is not the
          // real episode order.
          if (resolution.partial) {
            toasts.push({ message: '剧集列表未完整，请在详情页明确选择要播放的集', kind: 'error' });
            openItem(item.Id);
          } else {
            toasts.push({ message: '该剧集没有可播放的集', kind: 'error' });
          }
          return;
        }
        item = resolution.episode; options = {}; mediaSourceId = null;
      }
      playItemId.value = item.Id;
      playingItem.value = item;
      pendingSelection.set(mediaSourceId, options);
      const target = '#/play/' + item.Id;
      if (window.location.hash === target) {
        await startPlayerFromRoute(item.Id, true);
      } else {
        window.location.hash = target;
      }
    }

    async function resolveSeriesEpisode(seriesId) {
      const uid = localStorage.getItem('eio_user_id');
      try {
        const next = await api(`/Shows/NextUp?SeriesId=${seriesId}&UserId=${uid}&Limit=1`);
        if (next.Items?.length) return { episode: next.Items[0], partial: false };
      } catch (e) { console.error('NextUp', e); }
      try {
        const eps = await allItems(api, `/Shows/${seriesId}/Episodes`, { UserId: uid, Fields: 'PrimaryImageAspectRatio,RunTimeTicks,SeriesId' });
        return { episode: eps.sort(episodeOrder)[0] || null, partial: false };
      } catch (e) {
        if (isPartialResults(e)) return { episode: null, partial: true };
        console.error('resolveSeriesEpisode', e); return { episode: null, partial: false };
      }
    }

    function buildRetryContext(item, mediaSourceId, options, seconds, paused) {
      return {
        item, mediaSourceId: mediaSourceId || null,
        audioIndex: options?.audioIndex ?? playAudioIndex.value,
        subtitleIndex: options?.subtitleIndex ?? wasmSubTrack.value,
        startSeconds: seconds ?? options?.startSeconds,
        paused: paused ?? options?.paused,
        forceTranscode: options?.forceTranscode,
        playbackRate: options?.playbackRate ?? rateUI.playbackRate.value,
      };
    }

    function handlePlaybackFailure(error, generation, retryContext) {
      if (generation !== playbackGeneration) return;
      if (error?.name === 'AbortError') return;
      const busy = error?.status === 429;
      const summary = safeError(error);
      stopPlayer();
      playerUI.fail(busy ? 'busy' : 'failed',
        busy ? '服务器并发已满' : (summary ? '无法加载播放内容：' + summary : '无法加载播放内容'),
        retryContext);
      refreshIcons();
    }

    // startPlayerFromRoute is the single entry point that begins playback.
    async function startPlayerFromRoute(id, force) {
      if (!id || !localStorage.getItem('eio_token')) return;
      if (!force && playItemId.value === id && playerItemId === id) return;
      const operation = playerUI.operation; // preserve an in-flight switch across stopPlayer
      stopPlayer();
      const generation = playbackGeneration;
      playbackRequest = new AbortController();
      const signal = playbackRequest.signal;
      // Consume the router handoff immediately: a failure, a route switch or
      // a superseded request can never reuse this source/options (W08).
      const selection = pendingSelection.consume();
      const sourceId = selection.mediaSourceId;
      const options = selection.options;
      playerMessage.value = '';
      playerUI.begin(operation || 'loading');
      playerUI.attach();
      seekPreview.value = null;
      let item = playingItem.value && playingItem.value.Id === id ? playingItem.value : null;
      try {
        if (!item?.MediaSources?.length) {
          item = await api(`/Users/${localStorage.getItem('eio_user_id')}/Items/${id}?Fields=${DETAIL_FIELDS}`, { signal });
          if (generation !== playbackGeneration) return;
          playingItem.value = item;
        }
        playItemId.value = id;
        playVersions.value = item.MediaSources || [];
        await nextTick(); // the player container only exists once navPage renders
        if (generation !== playbackGeneration) return;
        // Re-attach now that the container actually exists: the attach() near
        // the top of this function ran before Vue rendered the player page,
        // so the container listeners were skipped. attach() detaches first,
        // so this is idempotent and re-binds the window/document listeners.
        playerUI.attach();
        // The full-series queue is navigation-only: issue it alongside the
        // play negotiation (PlaybackInfo goes on the wire first, since it is
        // the critical path) and never await it before engine start (W03).
        // Its failures stay inside the queue UI state.
        const playback = startPlayback(item, sourceId, generation, signal, options);
        loadEpisodeQueue(item, signal, () => generation === playbackGeneration);
        await playback;
      } catch (e) {
        handlePlaybackFailure(e, generation, buildRetryContext(item || playingItem.value, sourceId, options));
      } finally {
        if (generation === playbackGeneration) pendingSelection.clear();
      }
    }

    // The queue reports its own loading/partial/error state; start() in the
    // orchestrator never rejects, so a slow or failing series list cannot
    // block or fail the video.
    const episodeQueueLoader = createEpisodeQueue({
      load: (item, signal) => allItems(api, `/Shows/${item.SeriesId}/Episodes`, {
        UserId: localStorage.getItem('eio_user_id'), Fields: 'SeriesId,RunTimeTicks,UserData' }, { signal }),
      succeed: (episodes, meta) => {
        episodeQueue.value = episodes.slice().sort(episodeOrder);
        episodeQueuePartial.value = !meta.complete;
        episodeQueueLoading.value = false;
        queueError.value = meta.complete ? '' : '相邻剧集列表可能不完整，请手动选择要播放的集';
      },
      fail: () => {
        episodeQueue.value = [];
        episodeQueuePartial.value = true;
        episodeQueueLoading.value = false;
        queueError.value = '相邻剧集加载失败，可重试';
      },
      abort: () => { episodeQueueLoading.value = false; },
    });

    function loadEpisodeQueue(item, signal, isValid) {
      episodeQueueLoader.invalidate();
      queueError.value = '';
      episodeQueuePartial.value = false;
      if (!item?.SeriesId) {
        episodeQueue.value = [];
        episodeQueueLoading.value = false;
        return Promise.resolve();
      }
      episodeQueueLoading.value = true;
      return episodeQueueLoader.start(item, signal, isValid);
    }

    function retryEpisodeQueue() {
      toasts.push({ message: '正在重试相邻剧集…' });
      return loadEpisodeQueue(playingItem.value, playbackRequest?.signal,
        () => player === null || playerItemId === playingItem.value?.Id);
    }

    // A browser DeviceProfile lets the upstream choose direct vs transcode.
    // The strict entry path is the protocol marker; local api() auth stays the
    // existing one.
    function requestPlaybackInfo(item, mediaSourceId, signal, options) {
      return api(protocol().negotiationPath(item.Id), {
        method: 'POST', signal,
        body: JSON.stringify({
          UserId: localStorage.getItem('eio_user_id'),
          MediaSourceId: mediaSourceId || item.MediaSources?.[0]?.Id,
          DeviceProfile: EIOData.deviceProfile(), AutoOpenLiveStream: true,
          MaxStreamingBitrate: 120000000,
          AudioStreamIndex: options.audioIndex >= 0 ? options.audioIndex : undefined,
          SubtitleStreamIndex: options.subtitleIndex,
          EnableDirectPlay: options.forceTranscode ? false : undefined,
          EnableDirectStream: options.forceTranscode ? false : undefined,
          EnableTranscoding: options.forceTranscode ? true : undefined,
        }),
      });
    }

    function createPlaybackInstance(item, generation, attempt) {
      const instance = new EIOPlayer(playerContainer.value);
      player = instance;
      playerAlive.value = true;
      // The selected candidate's own attempt and session. Never the top-level
      // info.PlaySessionId (which belongs to whichever candidate the server
      // picked first) and never a shared/global session.
      instance.eioAttempt = attempt;
      instance.eioItemId = item.Id;
      instance.eioGeneration = generation;
      instance.sessionId = attempt.playSessionId;
      instance.eioReporting = EIOPlaybackReporting.watch(instance, extra => {
        if (player !== instance || generation !== playbackGeneration) return;
        reportPlaybackEvent(instance, 'Progress', extra)
          .catch(error => reportEventFailure(instance, error));
      });
      playSessionId = attempt.playSessionId;
      playerItemId = item.Id;
      startupSubtitleError = null;
      reportedSubtitleIndex = null;
      lastAudioError = '';
      instance.onState = (state) => {
        if (player !== instance || generation !== playbackGeneration) return;
        playerTelemetry.update(state);
        playerMode.value = state.mode;
        wasmPlaying.value = state.playing;
        // While an engine seek is pending, stale time events would snap the
        // progress thumb back to the pre-seek position; hold the target instead.
        if (seekInFlight == null || Math.abs(state.time - seekInFlight) < 1) wasmTime.value = state.time;
        wasmDuration.value = state.duration;
        if (state.message) playerMessage.value = state.message;
        // Package B resolves the default subtitle without blocking load and
        // reports the applied track asynchronously. Consume it here instead
        // of relying only on the value captured right after load().
        if (Number.isInteger(state.subtitleIndex) && state.subtitleIndex >= -1) {
          reportedSubtitleIndex = state.subtitleIndex;
          wasmSubTrack.value = state.subtitleIndex;
          if (state.subtitleIndex >= 0) lastSubtitleIndex = state.subtitleIndex;
        }
        if (state.audioError) handleAudioError(instance, generation, state.audioError);
        if (state.subtitleError) handleSubtitleError(instance, generation, state.subtitleError);
        playerUI.acceptState(state);
        instance.eioReporting.observe();
      };
      instance.onProgress = (ticks) => {
        if (player !== instance || generation !== playbackGeneration || !playbackStarted) return;
        reportPlaybackEvent(instance, 'Progress',
          { PositionTicks: ticks, IsPaused: !instance.playing })
          .catch(error => reportEventFailure(instance, error));
      };
      instance.onEnded = () => { if (player === instance) exitPlayer(); };
      instance.onError = (msg) => {
        if (player !== instance || generation !== playbackGeneration) return;
        handlePlaybackFailure(new Error(msg), generation,
          buildRetryContext(playingItem.value, playMediaSourceId.value, {},
            instance.positionTicks / 10000000, !instance.playing));
      };
      return instance;
    }

    // A startup subtitle failure must not be mistaken for a live track. The
    // generation check keeps a stale instance from touching the new player.
    async function syncStartupSubtitle(instance, generation) {
      if (startupSubtitleError && startupSubtitleError.instance === instance
          && startupSubtitleError.generation === generation) {
        await attemptSubtitleSelection(instance, -1, generation);
        wasmSubTrack.value = -1;
        if (player === instance && generation === playbackGeneration) {
          toasts.push({ message: '默认字幕加载失败，可重新选择', kind: 'error' });
        }
      } else if (reportedSubtitleIndex != null) {
        wasmSubTrack.value = reportedSubtitleIndex;
        if (reportedSubtitleIndex >= 0) lastSubtitleIndex = reportedSubtitleIndex;
      } else {
        wasmSubTrack.value = instance.subtitleIndex ?? -1;
        if (wasmSubTrack.value >= 0) lastSubtitleIndex = wasmSubTrack.value;
      }
      startupSubtitleError = null;
    }

    async function startPlayback(item, mediaSourceId, generation, signal, options = {}) {
      const identity = protocol();
      const info = await requestPlaybackInfo(item, mediaSourceId, signal, options);
      if (generation !== playbackGeneration) return;
      if (!info || !Array.isArray(info.MediaSources) || !info.MediaSources.length) {
        throw identity.createError('playback-info-invalid', '播放信息服务未返回可用媒体源', true);
      }
      const selected = info.MediaSources.find(s => s.Id === mediaSourceId) || info.MediaSources[0];
      const resolved = identity.attemptFromMediaSource(selected,
        { itemId: item.Id, mediaSourceId: selected.Id });
      if (!resolved.ok) throw resolved.error;
      const attempt = resolved.attempt;
      playMediaSourceId.value = selected.Id;
      playSrc.value = identity.strictMediaSource(attempt, selected);
      playAudioIndex.value = options.audioIndex >= 0 ? options.audioIndex : defaultAudio(playSrc.value);

      // A forced compatibility retry must come from the transcode URL of this
      // PlaybackInfo response; the original stream is never used instead.
      // Every URL below is server-issued for this attempt: a missing URL is a
      // visible protocol failure, never a rebuilt legacy path.
      let streamUrl;
      let fallbackUrl = null;
      if (options.forceTranscode) {
        const compatible = compatibleStream(playSrc.value);
        if (!compatible.ok) throw identity.createError('playback-url-missing', compatible.message, false);
        streamUrl = identity.strictResourceUrl(attempt, compatible.url);
      } else {
        // The strict negotiation also returns EioFallbackUrl when the upstream
        // has no DirectStreamUrl: it is already prefixed with the attempt and
        // carries the capability, so the client never constructs a legacy
        // /Videos/{id}/stream.ext path itself.
        const directStreamUrl = playSrc.value.DirectStreamUrl || playSrc.value.EioFallbackUrl;
        if (!directStreamUrl) {
          throw identity.createError('playback-url-missing',
            playSrc.value.EioDirectStreamUnavailable === true
              ? '上游未提供可安全代理的直连流地址'
              : '播放服务未返回严格的直连流地址，已拒绝回退旧版路径',
            false);
        }
        streamUrl = identity.strictResourceUrl(attempt, directStreamUrl);
        fallbackUrl = playSrc.value.TranscodingUrl
          ? identity.strictResourceUrl(attempt, playSrc.value.TranscodingUrl)
          : null;
      }
      // The previous generation's cleanup gets a bounded budget; a cleanup
      // that overruns is surfaced instead of stacking another decoder.
      const cleanup = await engineCleanup.waitForIdle(
        Number(window.EIO_ENGINE_CLEANUP_BUDGET_MS) || undefined);
      if (generation !== playbackGeneration) return;
      if (!cleanup.ok) {
        throw new Error(cleanup.reason === 'timeout'
          ? '上一个播放器尚未释放资源，请稍后重试'
          : '上一个播放器释放资源失败，请刷新页面释放资源后重试');
      }

      // First-play race guard: the login-time refresh is not awaited and
      // may not have landed yet. Give a bounded refresh a chance to run
      // before reading the flag below; on timeout or failure the current
      // value (baseline false) is kept and playback is never blocked.
      await Promise.race([
        refreshPlaybackFeatures(),
        new Promise((resolve) => setTimeout(resolve, 800)),
      ]);
      const instance = createPlaybackInstance(item, generation, attempt);
      playbackStarted = false;
      await instance.load(streamUrl, playSrc.value, item, {
        fallbackUrl,
        playMethod: options.forceTranscode ? 'Transcode' : 'DirectStream',
        audioIndex: playAudioIndex.value, subtitleIndex: options.subtitleIndex,
        ...(playbackFeatures.dualCache ? { eioDualCache: {
          enabled: true,
          ...dualCacheBudget(),
        } } : {}),
      });
      if (player !== instance || generation !== playbackGeneration) return;
      await syncStartupSubtitle(instance, generation);
      const seconds = options.startSeconds ?? (item.UserData?.PlaybackPositionTicks || 0) / 10000000;
      if (seconds > 0 && seconds < instance.duration) {
        try {
          await instance.seek(seconds, { timeout: STARTUP_SEEK_TIMEOUT_MS, label: 'resume' });
        } catch (error) {
          // Only the current generation may report this startup budget. The
          // marker and numbers are static/safe: the original error, URL,
          // item id and credentials never reach the log. The rejection is
          // rethrown so the existing failure cleanup/destroy path still runs.
          if (generation === playbackGeneration && error?.name === 'TimeoutError') {
            console.warn('[EIO] startup resume seek timeout', {
              timeoutMs: STARTUP_SEEK_TIMEOUT_MS,
              targetSeconds: Math.round(seconds),
            });
          }
          throw error;
        }
      }
      // Speed is user state like volume: apply it before the paused branch
      // so a version switch or compatibility retry keeps the chosen rate.
      if (options.playbackRate != null && options.playbackRate !== 1) {
        await rateUI.selectPlaybackRate(options.playbackRate);
      }
      if (player !== instance || generation !== playbackGeneration) return;
      if (options.paused) await instance.pause();
      // Volume is user state: a compatibility retry or version switch must
      // not reset it to the engine default.
      instance.setVolume(playerVolume.value);
      // A protocol rejection of Playing is observable: it propagates to the
      // entry point, which reports it through handlePlaybackFailure, so the
      // session never claims a playback the attempt was not allowed to start.
      // An ordinary transport failure keeps the previous best-effort
      // reporting semantics (it does not fail an already loaded stream).
      await reportPlaybackEvent(instance, 'Playing',
        { PositionTicks: instance.positionTicks, IsPaused: !instance.playing }, { signal });
      if (player === instance && generation === playbackGeneration) {
        playbackStarted = true;
        instance.eioReporting.start();
        playerUI.end();
        playerUI.acceptState({ playing: instance.playing, loading: false });
      }
      refreshIcons();
    }

    function stopPlayer() {
      playbackGeneration++;
      rateUI.reset();
      // Leaves and reloads must not keep a previous generation's buffer or
      // rate on screen: the HUD returns to its unknown dashes.
      playerTelemetry.reset();
      playbackStarted = false;
      startupSubtitleError = null;
      reportedSubtitleIndex = null;
      lastAudioError = '';
      episodeQueueLoader.invalidate();
      episodeQueueLoading.value = false;
      playbackRequest?.abort(); playbackRequest = null;
      if (seekCommitTimer != null) { clearTimeout(seekCommitTimer); seekCommitTimer = null; }
      if (seekInFlightTimer != null) { clearTimeout(seekInFlightTimer); seekInFlightTimer = null; }
      seekTarget = null; seekInFlight = null; seekPreview.value = null;
      engineSeekSerial++; engineSeekBusy = false; engineSeekQueued = null;
      playerMessage.value = '';
      playerAlive.value = false;
      playerUI.closeMenu();
      if (!player) return;
      const instance = player;
      // A late stop captures this instance's own attempt: a newer negotiation
      // may already have bound a different attempt, which must never receive
      // the old stop. Only a protocol rejection is recorded; an ordinary
      // transport failure during teardown keeps the previous semantics.
      cleanupEncodings(instance);
      reportPlaybackEvent(instance, 'Stopped', { PositionTicks: instance.positionTicks })
        .catch(error => reportStopFailure(error));
      // Destroy is async: track it so the next engine waits within budget for
      // the previous generation instead of overlapping decoders (W13).
      engineCleanup.track(instance.destroy());
      player = null; playerItemId = null; playSessionId = null;
    }

    function exitPlayer() {
      stopPlayer();
      pendingSelection.clear();
      // A failed destroy is a real resource leak: leaving the player must not
      // lift the cleanup barrier, otherwise re-entering would create another
      // decoder while the old one is still alive. Only a page reload can
      // recover that state; a still-pending cleanup recovers when it settles.
      playerUI.reset();
      playerUI.detach();
      window.location.hash = playerReturnRoute.startsWith('#/play/')
        ? '#/item/' + (playingItem.value?.SeriesId || playingItem.value?.Id) : playerReturnRoute;
    }

    // `playerUI` is wrapped in reactive(): the browser unwraps its refs while
    // the VM harness keeps the ref object. Accept either shape so the error
    // context is never lost between them.
    function playerError() {
      const current = playerUI.error;
      return current && typeof current === 'object' && 'value' in current ? current.value : current;
    }

    function retryPlayback(compatible = false) {
      const context = playerError()?.retryContext;
      const item = context?.item || playingItem.value;
      if (!item) return;
      playerUI.reset();
      // Compatibility playback keeps the selected source, resume position,
      // pause intent, volume and track preferences, but re-negotiates with
      // direct play/stream disabled and only the refreshed TranscodingUrl.
      playItem(item, compatible
        ? (playSrc.value?.Id || context?.mediaSourceId || null)
        : (context?.mediaSourceId || null), {
        startSeconds: context?.startSeconds, paused: context?.paused,
        audioIndex: context?.audioIndex, subtitleIndex: context?.subtitleIndex,
        // The failed selection is consumed: a plain retry negotiates the
        // normal profile again, while "兼容播放" is the explicit forced
        // transcode (A2/W08). Otherwise 重试 would silently repeat a failing
        // forced transcode.
        forceTranscode: compatible ? true : undefined,
      });
    }

    function selectErrorSource(id) {
      const context = playerError()?.retryContext;
      if (context) context.mediaSourceId = id;
    }

    /* ---- track / version switching ---- */
    async function switchVersion(id) {
      if (!player || id === playMediaSourceId.value) return;
      if (playerUI.busy || trackBusy.value) return;
      const instance = player;
      trackBusy.value = true;
      playerUI.begin('version');
      try {
        await playItem(playingItem.value, id, { startSeconds: instance.positionTicks / 10000000,
          paused: !instance.playing, playbackRate: rateUI.playbackRate.value });
      } catch (e) {
        toasts.push({ message: '版本切换失败：' + safeError(e), kind: 'error' });
      } finally {
        trackBusy.value = false;
      }
    }

    async function selectAudio(index) {
      if (!player || index === playAudioIndex.value) return;
      if (playerUI.busy || trackBusy.value) return;
      const instance = player;
      trackBusy.value = true;
      try {
        let switched = false;
        try { switched = await instance.selectAudio(index); } catch (e) { switched = false; }
        if (switched) { playAudioIndex.value = index; playerUI.end(); return; }
        playerUI.begin('audio');
        await playItem(playingItem.value, playMediaSourceId.value, { audioIndex: index,
          subtitleIndex: wasmSubTrack.value, startSeconds: instance.positionTicks / 10000000,
          paused: !instance.playing, forceTranscode: !!instance.core?.isHls?.(),
          playbackRate: rateUI.playbackRate.value });
      } catch (e) {
        toasts.push({ message: '音轨切换失败：' + safeError(e), kind: 'error' });
      } finally {
        trackBusy.value = false;
      }
    }

    /* ---- subtitles ---- */
    function handleAudioError(instance, generation, message) {
      if (player !== instance || generation !== playbackGeneration) return;
      const text = safeError(message) || '音轨加载失败';
      // onState repeats for progress/play events; report one failure once.
      if (text === lastAudioError) return;
      lastAudioError = text;
      toasts.push({ message: text, kind: 'error' });
    }

    function handleSubtitleError(instance, generation, message) {
      if (player !== instance || generation !== playbackGeneration) return;
      const text = safeError(message) || '字幕加载失败';
      if (subtitleAttempt && subtitleAttempt.instance === instance && subtitleAttempt.generation === generation) {
        subtitleAttempt.failed = true; subtitleAttempt.message = text;
        return;
      }
      // Playback is already running: the error must be visible now, not
      // stored in a startup variable nobody reads anymore (W11).
      if (playbackStarted) {
        wasmSubTrack.value = -1;
        toasts.push({ message: text, kind: 'error' });
        return;
      }
      startupSubtitleError = { instance, generation, message: text };
    }

    // selectSubtitle() resolves to a boolean: true only when the client
    // actually applied (or closed) the track. `undefined` means the engine
    // did not confirm the result and is not treated as success.
    async function attemptSubtitleSelection(instance, index, generation) {
      const attempt = { instance, generation, serial: ++subtitleSwitchSerial, failed: false, message: '' };
      subtitleAttempt = attempt;
      let applied;
      try {
        applied = await instance.selectSubtitle(index);
      } catch (e) {
        if (attempt.instance === instance && attempt.generation === generation) { attempt.failed = true; attempt.message = safeError(e); }
      }
      if (subtitleAttempt === attempt) subtitleAttempt = null;
      if (!attempt.failed && applied !== true) {
        attempt.failed = true;
        if (!attempt.message) attempt.message = '字幕引擎未确认选择结果';
      }
      const ok = !attempt.failed && instance === player && generation === playbackGeneration;
      return { ok, message: attempt.message };
    }

    async function selectSubtitle(idx) {
      const instance = player;
      if (!instance || !playSrc.value || subtitleBusy.value) return;
      const oldIndex = wasmSubTrack.value;
      if (idx === oldIndex) return;
      subtitleBusy.value = true;
      const generation = playbackGeneration;
      try {
        const result = await attemptSubtitleSelection(instance, idx, generation);
        if (instance !== player || generation !== playbackGeneration) return;
        if (result.ok) {
          wasmSubTrack.value = idx;
          if (idx >= 0) lastSubtitleIndex = idx;
          return;
        }
        // Failed: restore the previously confirmed track, else turn subtitles off.
        const restore = await attemptSubtitleSelection(instance, oldIndex, generation);
        if (instance !== player || generation !== playbackGeneration) return;
        if (restore.ok) { wasmSubTrack.value = oldIndex; return; }
        const closed = await attemptSubtitleSelection(instance, -1, generation);
        if (instance !== player || generation !== playbackGeneration) return;
        wasmSubTrack.value = -1;
        toasts.push({ message: closed.ok ? '字幕切换失败，已关闭字幕' : '字幕切换失败', kind: 'error' });
      } finally {
        if (generation === playbackGeneration) subtitleBusy.value = false;
        refreshIcons();
      }
    }

    function toggleSubtitleKey() {
      if (!subtitleTracks.value.length) return;
      if (wasmSubTrack.value >= 0) { lastSubtitleIndex = wasmSubTrack.value; selectSubtitle(-1); }
      else if (lastSubtitleIndex >= 0) selectSubtitle(lastSubtitleIndex);
    }

    /* ---- progress / volume / shortcuts ---- */
    // A cancelled drag (pointercancel) must not be committed by the change
    // event some browsers still fire afterwards; the next input event
    // starts a new interaction and clears the flag.
    let scrubCancelled = false;
    function previewSeek(value) { scrubCancelled = false; seekPreview.value = Number(value); }

    // The browser fires pointerup before change on a range slider. Clearing the
    // preview on pointerup would let Vue rewrite the input value to the stale
    // engine time before change reads it, snapping the thumb back to the
    // pre-drag position. Commit from the authoritative preview instead; the
    // change event that follows re-commits the same value harmlessly.
    function endScrub() {
      if (seekPreview.value == null) return;
      commitScrubValue(seekPreview.value);
    }

    function cancelScrub() { scrubCancelled = true; seekPreview.value = null; }

    function onWasmSeek(value) {
      if (scrubCancelled) { scrubCancelled = false; return; }
      const seconds = seekPreview.value != null ? seekPreview.value : parseFloat(value);
      commitScrubValue(seconds);
    }

    function commitScrubValue(seconds) {
      if (!Number.isFinite(seconds)) return;
      seekPreview.value = null;
      wasmTime.value = seconds;
      commitSeek(seconds);
    }

    function commitSeek(seconds) {
      seekTarget = seconds;
      if (seekCommitTimer != null) return;
      seekCommitTimer = setTimeout(() => {
        seekCommitTimer = null;
        const target = seekTarget; seekTarget = null;
        if (player) runEngineSeek(target);
      }, 0);
    }

    // Seeks are serialized against the engine: while core.seek() runs, the
    // engine rejects further seeks ("player is seeking now"), so a newer
    // target is queued and applied when the in-flight seek settles. This
    // keeps a quick second drag from being silently dropped.
    let engineSeekBusy = false;
    let engineSeekQueued = null;
    let engineSeekSerial = 0;

    async function runEngineSeek(target) {
      if (!player) return;
      if (engineSeekBusy) { engineSeekQueued = target; seekInFlight = target; return; }
      const instance = player;
      const serial = ++engineSeekSerial;
      const current = () => player === instance && engineSeekSerial === serial;
      engineSeekBusy = true;
      seekInFlight = target;
      if (seekInFlightTimer != null) clearTimeout(seekInFlightTimer);
      const seekWatchdogMs = Number(window.EIO_SEEK_WATCHDOG_MS) || 8000;
      seekInFlightTimer = setTimeout(() => {
        if (!current()) return;
        // The engine seek never settled. Release the queue so later seeks
        // are attempted instead of being queued forever; if the engine is
        // still stuck it will reject the next seek with its own error,
        // which the catch above surfaces. Stay silent — the seek may land.
        seekInFlight = null;
        seekInFlightTimer = null;
        engineSeekBusy = false;
        engineSeekQueued = null;
      }, seekWatchdogMs);
      try {
        await instance.seek(target);
        if (current() && playbackStarted) reportPlaybackEvent(instance, 'Progress', { EventName: 'TimeUpdate' })
          .catch(error => reportEventFailure(instance, error));
      } catch (e) {
        if (current() && e?.name !== 'AbortError') toasts.push({ message: '跳转失败：' + safeError(e), kind: 'error' });
      } finally {
        if (!current()) return;
        const queued = engineSeekQueued; engineSeekQueued = null;
        engineSeekBusy = false;
        if (queued == null) {
          if (seekInFlightTimer != null) { clearTimeout(seekInFlightTimer); seekInFlightTimer = null; }
          seekInFlight = null;
        } else {
          if (queued !== target) runEngineSeek(queued);
          else {
            if (seekInFlightTimer != null) clearTimeout(seekInFlightTimer);
            seekInFlightTimer = null; seekInFlight = null;
          }
        }
      }
    }

    function seekBy(delta) {
      if (!player) return;
      const duration = wasmDuration.value;
      if (!duration || !Number.isFinite(duration) || duration <= 0) return;
      const base = seekPreview.value ?? wasmTime.value;
      const target = Math.max(0, Math.min(duration, base + Number(delta)));
      seekPreview.value = null;
      wasmTime.value = target;
      commitSeek(target);
    }

    function changeVolume(value) {
      const volume = Math.max(0, Math.min(1, Number(value) || 0));
      playerVolume.value = volume;
      if (volume > 0) lastNonZeroVolume.value = volume;
      player?.setVolume(volume);
    }
    function toggleMute() {
      if (playerVolume.value > 0) { lastNonZeroVolume.value = playerVolume.value; changeVolume(0); }
      else changeVolume(lastNonZeroVolume.value || 1);
    }
    function toggleFullscreen() {
      const result = document.fullscreenElement ? document.exitFullscreen() : playerContainer.value?.requestFullscreen();
      result?.catch(e => toasts.push({ message: '无法进入全屏：' + safeError(e), kind: 'error' }));
    }
    function wasmTogglePause() {
      if (!player) return;
      Promise.resolve(player.playing ? player.pause() : player.play())
        .catch(e => toasts.push({ message: '播放失败：' + safeError(e), kind: 'error' }));
    }

    return {
      // Template state and derived view models.
      playerContainer, playerAlive, playerMode, playItemId, playingItem, playSrc, playMediaSourceId,
      wasmPlaying, wasmTime, wasmDuration, wasmTimeHuman, wasmDurationHuman, wasmSubTrack, playerMessage,
      playVersions, playAudioIndex, episodeQueue, episodeQueueLoading, episodeQueuePartial, queueError,
      trackBusy, subtitleBusy, playerVolume, lastNonZeroVolume, seekPreview,
      previousEpisode, nextEpisode, audioTracks, subtitleTracks, versionOptions, audioOptions,
      subtitleOptions, playerLoadingText, seekTitle, seekPercent,
      // Adapters (UI state, rate selection, telemetry HUD).
      playerUI, rateUI, playerTelemetry,
      // Entry / teardown / retry.
      playItem, startPlayerFromRoute, stopPlayer, exitPlayer, retryPlayback, selectErrorSource,
      retryEpisodeQueue, switchVersion, selectAudio, selectSubtitle,
      // Transport controls driven by the template and playerUI actions.
      previewSeek, endScrub, cancelScrub, onWasmSeek, changeVolume, toggleMute, toggleFullscreen,
      wasmTogglePause, seekBy, toggleSubtitleKey,
      // Shared router handoff: the page lifecycle clears it on leaves/logout.
      pendingSelection,
    };
  }

  window.EIOPlaybackSession = { create };
})();
