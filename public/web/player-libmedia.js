/* Emby-In-One player: native MP4, streaming libmedia, upstream HLS fallback. */
(function () {
  'use strict';

  function abortError(message) {
    return new DOMException(message || 'Playback cancelled', 'AbortError');
  }

  // Telemetry data modules (EIOPlayerBuffer/EIOPlayerProgress) are optional page
  // scripts. A deployment without them, a blocked script or a global mismatch
  // keeps explicit unknown values instead of a guess.
  function telemetryRoot() {
    if (typeof window !== 'undefined' && window) return window;
    if (typeof globalThis !== 'undefined' && globalThis) return globalThis;
    return null;
  }

  function telemetryApi(name) {
    try {
      const root = telemetryRoot();
      if (!root) return null;
      const api = root[name];
      return api && typeof api === 'object' ? api : null;
    } catch (error) { return null; }
  }

  // Buffered telemetry is bounded exactly like the data modules: more than 128
  // entries is unknown, not a truncated list.
  const MAX_BUFFER_RANGES = 128;

  function finiteEpoch(value) {
    return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0 ? value : null;
  }

  function unknownBuffered(epoch) {
    return { status: 'unknown', ranges: [], epoch: epoch };
  }

  // Copies a snapshot's ranges into plain numbers. Anything malformed means the
  // answer is unknown, never a truncated or partially trusted range list.
  function copyRanges(ranges) {
    if (!Array.isArray(ranges)) return null;
    if (ranges.length > MAX_BUFFER_RANGES) return null;
    const out = [];
    for (let index = 0; index < ranges.length; index++) {
      const item = ranges[index];
      if (!item || typeof item !== 'object') return null;
      const start = item.start;
      const end = item.end;
      if (typeof start !== 'number' || typeof end !== 'number') return null;
      if (!Number.isFinite(start) || !Number.isFinite(end)) return null;
      if (start < 0 || end < start) return null;
      out.push({ start: start, end: end });
    }
    return out;
  }

  // Every engine seek is bounded. A caller may pick a smaller or larger
  // per-call budget, but a missing/non-positive/non-finite value falls back to
  // the interactive default and nothing may exceed the hard upper bound.
  const DEFAULT_SEEK_TIMEOUT_MS = 10000;
  const MAX_SEEK_TIMEOUT_MS = 60000;

  function normalizeSeekTimeout(value, fallback) {
    const fallbackValue = Number(fallback);
    const safeFallback = Number.isFinite(fallbackValue) && fallbackValue > 0
      ? fallbackValue : DEFAULT_SEEK_TIMEOUT_MS;
    const candidate = Number(value);
    const base = Number.isFinite(candidate) && candidate > 0 ? candidate : safeFallback;
    return Math.min(base, MAX_SEEK_TIMEOUT_MS);
  }

  // Labels are diagnostics only, so an unexpected value is replaced instead of
  // being copied into an error or a log.
  function normalizeSeekLabel(value) {
    return typeof value === 'string' && /^[A-Za-z][A-Za-z0-9_-]{0,31}$/.test(value)
      ? value : 'seek';
  }

  class EIOPlayer {
    constructor(container) {
      this.container = container;
      // Vue owns the controls; each engine owns only its separate host.
      this.host = document.createElement('div');
      this.host.className = 'absolute inset-0';
      container.prepend(this.host);
      this.mode = 'native';
      this.core = null;
      this.video = null;
      this.playing = false;
      this.duration = 0;
      this.positionTicks = 0;
      this._abort = new AbortController();
      this._destroyed = false;
      this._fallbackUsed = false;
      this._recovering = false;
      // User intent is player state, not engine state: replacing the engine
      // during a runtime fallback must not reset volume or resume playback
      // that the user had explicitly paused.
      this._volume = 1;
      this._pauseRequested = false;
      this._engineGeneration = 0;
      this._seekQueue = null;
      this._bumpEngineGeneration();
      this._subtitleAbort = null;
      this._subtitleTimer = null;
      this._subtitleSerial = 0;
      this._defaultSubtitle = null;
      // Sticky teardown latch. Once an old decoder fails to release within the
      // deadline, creating another engine would leak a live decoder (and its
      // canvas/audio device) behind the new one, so it stays blocked until the
      // orphaned destroy actually settles.
      this._engineWedged = null;
      this._teardownToken = null;
      this._cleanup = null;
      this.audioError = null;
      this.onProgress = this.onEnded = this.onError = this.onState = null;
      this._bindTelemetryEvents();
    }

    async load(url, mediaSource, item, options = {}) {
      this.mediaSource = mediaSource;
      this.item = item;
      this.options = options;
      this.duration = (item.RunTimeTicks || mediaSource.RunTimeTicks || 0) / 10000000;
      this.url = new URL(url, location.origin).href;
      this.fallbackUrl = options.fallbackUrl && new URL(options.fallbackUrl, location.origin).href;
      this._assertEngineUsable();
      this.subtitles = new EIOSubtitles(this);
      try { await this._attempt(this.url); }
      catch (error) {
        this._checkAlive();
        if (!this.fallbackUrl || this.fallbackUrl === this.url) throw error;
        await this._fallback();
      }
      this._checkAlive();
      // A player can be loaded again after a wedged engine finally releases.
      // Replace the previous monitoring pair instead of orphaning it, otherwise
      // destroy() only clears the newest timers and the old interval keeps the
      // host process alive forever.
      clearInterval(this._progressTimer);
      clearInterval(this._decodeTimer);
      this._progressTimer = setInterval(() => this.onProgress?.(this.positionTicks), 10000);
      this._decodeTimer = setInterval(() => {
        const core = this.core;
        if (core && !this._recovering) this._requireHardware(core).catch(error => this._runtimeError(error));
      }, 5000);
      const subtitles = mediaSource.MediaStreams?.filter(s => s.Type === 'Subtitle') || [];
      const index = options.subtitleIndex ?? mediaSource.DefaultSubtitleStreamIndex;
      const selected = index == null ? subtitles.find(s => s.IsDefault)?.Index : index;
      // Default subtitles use their own deadline and signal. They must never
      // hold up a playable picture or turn a slow font/track fetch into a
      // whole-page playback failure.
      if (selected != null) this._startDefaultSubtitle(selected);
      this._emitState({ loading: false });
    }

    _checkAlive() {
      if (this._destroyed) throw abortError();
    }

    // Cleanup-gate interface for the host app: a wedged engine means the old
    // decoder is still alive, so no new engine may be created on this player.
    isEngineWedged() {
      return this._engineWedged != null;
    }

    get engineTeardown() {
      if (!this._engineWedged) return { released: true, wedged: false, error: null, since: null, generation: null };
      const { error, since, generation } = this._engineWedged;
      return { released: false, wedged: true, error, since, generation };
    }

    _assertEngineUsable() {
      if (!this._engineWedged) return;
      const error = new Error('旧播放引擎仍未释放：' + (this._engineWedged.error?.message || '未知原因'));
      error.name = 'EngineTeardownError';
      error.wedged = true;
      throw error;
    }

    _wedgeEngine(error) {
      // Never overwrite the first latch: the earliest failure carries the
      // original diagnostics and the generation that still holds resources.
      if (this._engineWedged) return;
      this._engineWedged = { error, since: Date.now(), generation: this._engineGeneration, token: this._teardownToken };
    }

    async _releaseCore(_core) {
      const timeout = Number(this.options?.destroyTimeout ?? this.options?.engineTeardownTimeout) || 4000;
      const token = {};
      this._teardownToken = token;
      // libmedia cannot cancel a pending destroy. Keep a handler on it so a
      // late rejection never surfaces unhandled, and clear the latch only when
      // the orphaned teardown really finishes.
      const pending = Promise.resolve().then(() => _core.destroy());
      pending.catch(() => {});
      pending.then(() => {
        if (this._teardownToken === token) this._teardownToken = null;
        if (this._engineWedged?.token === token) this._engineWedged = null;
      }, () => {
        if (this._teardownToken === token) this._teardownToken = null;
      });
      let timer;
      const guard = new Promise((_, reject) => {
        timer = setTimeout(() => {
          const error = new Error('旧解码器销毁超时（' + timeout + 'ms）');
          error.name = 'EngineTeardownError';
          error.wedged = true;
          error.generation = this._engineGeneration;
          reject(error);
        }, timeout);
        timer.unref?.();
      });
      try { return await Promise.race([pending, guard]); }
      finally { clearTimeout(timer); }
    }

    _bumpEngineGeneration() {
      this._engineGeneration++;
      this._seekQueue?.controller.abort();
      // A queue belongs to one engine generation. Replacing it guarantees a
      // hung seek from the old core cannot block seeks on the new core.
      this._seekQueue = {
        generation: this._engineGeneration,
        tail: Promise.resolve(),
        controller: new AbortController(),
      };
      return this._engineGeneration;
    }

    _isCurrentEngine(core, video, generation) {
      return !this._destroyed && generation === this._engineGeneration
        && this.core === core && this.video === video;
    }

    _isCurrentQueue(queue, generation) {
      return !this._destroyed && queue === this._seekQueue
        && generation === this._engineGeneration;
    }

    async _awaitBounded(value, timeout, signal, metadata = {}) {
      const promise = Promise.resolve(value);
      const timeoutMs = normalizeSeekTimeout(timeout, DEFAULT_SEEK_TIMEOUT_MS);
      const label = normalizeSeekLabel(metadata.label);
      const attempt = metadata.attempt || null;
      // The underlying seek cannot be cancelled in libmedia. Attach a handler
      // so a late rejection after timeout/abort never surfaces unhandled. A
      // timeout does not abort the core: it only marks this adapter attempt so
      // a late completion cannot write the old seek position into the UI.
      promise.catch(() => {});
      let timer, onAbort;
      const guard = new Promise((_, reject) => {
        timer = setTimeout(() => {
          if (attempt) attempt.timedOut = true;
          const error = new Error('跳转超时');
          error.name = 'TimeoutError';
          error.label = label;
          error.timeoutMs = timeoutMs;
          reject(error);
        }, timeoutMs);
        timer.unref?.();
        onAbort = () => reject(abortError());
        if (signal.aborted) onAbort();
        else signal.addEventListener('abort', onAbort, { once: true });
      });
      try { return await Promise.race([promise, guard]); }
      finally {
        clearTimeout(timer);
        signal.removeEventListener('abort', onAbort);
      }
    }

    // Every engine seek, including subtitle cue repopulation, goes through
    // this per-generation serial queue. Jobs read current engine state when
    // they execute, not when they are queued.
    _coordinateSeek(job, options = {}) {
      const queue = this._seekQueue;
      if (!queue || this._destroyed) return Promise.reject(abortError());
      const generation = queue.generation;
      const timeout = normalizeSeekTimeout(options.timeout,
        normalizeSeekTimeout(this.options?.seekTimeout, DEFAULT_SEEK_TIMEOUT_MS));
      const label = normalizeSeekLabel(options.label);
      const run = async () => {
        if (!this._isCurrentQueue(queue, generation)) throw abortError();
        const attempt = {
          core: this.core,
          video: this.video,
          generation,
          signal: queue.controller.signal,
          timedOut: false,
        };
        const result = job(attempt);
        return this._awaitBounded(result, timeout, queue.controller.signal, { label, attempt });
      };
      const result = queue.tail.then(run, run);
      queue.tail = result.then(() => {}, () => {});
      return result;
    }

    _emitState(extra = {}) {
      if (this._destroyed || !this.onState) return;
      // Frozen telemetry fields are written last so a caller-supplied `extra`
      // can never replace a measured value with an assumed one.
      this.onState?.({ mode: this.mode, playing: this.playing, time: this.positionTicks / 10000000,
        duration: this.duration, ...extra,
        playbackRate: this.playbackRate,
        buffered: this._bufferedSnapshot(),
        sourceBitrateMbps: this._sourceBitrateMbps(),
        receiveRate: this._receiveRate() });
    }

    // Buffered ranges keep growing while playback is paused and neither progress
    // nor durationchange bubbles, so one capture listener per event on the owned
    // host refreshes the state. It is installed once in the constructor, adds no
    // timer and is removed with the player's abort signal.
    _bindTelemetryEvents() {
      const host = this.host;
      if (!host || typeof host.addEventListener !== 'function') return;
      const refresh = event => this._onTelemetryEvent(event);
      for (const type of ['progress', 'loadedmetadata', 'durationchange']) {
        host.addEventListener(type, refresh, { capture: true, signal: this._abort.signal });
      }
    }

    _onTelemetryEvent(event) {
      if (this._destroyed) return;
      const host = this.host;
      if (!host || typeof host.contains !== 'function') return;
      const target = event && event.target;
      if (!target || target === host) return;
      // Only media elements owned by this host may refresh this player's state.
      if (host.contains(target) !== true) return;
      this._emitState();
    }

    // Thin wiring to the optional EIOPlayerBuffer module. A missing module or a
    // throwing snapshot must never disturb playback: it degrades to an explicit
    // unknown, never to a fake known-empty range list.
    _bufferedSnapshot() {
      let epoch = null;
      try {
        epoch = finiteEpoch(this._engineGeneration);
        const api = telemetryApi('EIOPlayerBuffer');
        if (!api || typeof api.snapshot !== 'function') return unknownBuffered(epoch);
        const snapshot = api.snapshot(this);
        if (!snapshot || typeof snapshot !== 'object') return unknownBuffered(epoch);
        if (snapshot.status !== 'ok') return unknownBuffered(epoch);
        // The real helper always stamps its captured generation. An explicit
        // epoch must match this engine; a stale one is unknown and may never be
        // rewritten with the current generation. Only an old/fake adapter that
        // omits the property keeps the compatibility fallback.
        if ('epoch' in snapshot) {
          const snapshotEpoch = finiteEpoch(snapshot.epoch);
          if (snapshotEpoch === null || snapshotEpoch !== epoch) return unknownBuffered(epoch);
        }
        const ranges = copyRanges(snapshot.ranges);
        if (!ranges) return unknownBuffered(epoch);
        // Range getters can run arbitrary code. If they swap the engine while
        // the list is copied, the copied ranges belong to the old decoder and
        // must not be reported as a current ok snapshot.
        const currentEpoch = finiteEpoch(this._engineGeneration);
        if (currentEpoch !== epoch) return unknownBuffered(currentEpoch);
        return { status: 'ok', ranges: ranges, epoch: epoch };
      } catch (error) {
        return unknownBuffered(epoch);
      }
    }

    // Source bitrate: only the selected MediaSource `Bitrate` (bits/s) through the
    // progress module, which divides by 1e6. No bandwidth or download estimate.
    _sourceBitrateMbps() {
      try {
        const api = telemetryApi('EIOPlayerProgress');
        if (!api || typeof api.getSourceBitrateMbps !== 'function') return null;
        const mbps = api.getSourceBitrateMbps(this.mediaSource);
        return typeof mbps === 'number' && Number.isFinite(mbps) && mbps > 0 ? mbps : null;
      } catch (error) { return null; }
    }

    // Frozen receiving placeholder. The real receiving sample has not passed its
    // PoC gate, so this stays unknown: IO byte counters, tick bandwidth or bitrate
    // never stand in for delivered response bytes, and unknown is never 0.
    _receiveRate() {
      let epoch = null;
      try { epoch = finiteEpoch(this._engineGeneration); } catch (error) { epoch = null; }
      return { status: 'unknown', bytesPerSec: null, epoch: epoch, sampledAt: null };
    }

    _canNativePlay(url) {
      if (/\.m3u8(?:\?|$)/i.test(url)) return false;
      const streams = this.mediaSource.MediaStreams || [];
      const video = streams.find(s => s.Type === 'Video');
      const audio = streams.find(s => s.Type === 'Audio');
      const container = (this.mediaSource.Container || '').toLowerCase();
      const defaultIndex = this.mediaSource.DefaultAudioStreamIndex ?? streams.find(s => s.Type === 'Audio' && s.IsDefault)?.Index ?? audio?.Index;
      if (this.options.audioIndex >= 0 && streams.filter(s => s.Type === 'Audio').length > 1) return false;
      if (this.options.audioIndex >= 0 && this.options.audioIndex !== defaultIndex) return false;
      return ['mp4', 'm4v'].includes(container) && ['h264', 'avc'].includes(video?.Codec?.toLowerCase())
        && (!audio || ['aac', 'mp3'].includes(audio.Codec?.toLowerCase()));
    }

    async _attempt(url) {
      this._checkAlive();
      this._assertEngineUsable();
      this._emitState({ loading: true, message: this._fallbackUsed ? '正在加载转码流…' : '正在加载媒体…' });
      const pending = this._canNativePlay(url) ? this._native(url) : this._libmedia(url);
      const attemptGeneration = this._engineGeneration;
      // A timed-out or failed attempt must invalidate its own generation: the
      // old core.load()/play() continuation may settle later and must not
      // resurrect an engine that the user or watchdog already abandoned.
      const invalidate = () => {
        if (!this._destroyed && attemptGeneration === this._engineGeneration) this._bumpEngineGeneration();
      };
      let timer, onAbort;
      const cancelled = new Promise((_, reject) => {
        timer = setTimeout(() => {
          invalidate();
          reject(new Error('媒体加载超时'));
        }, this.options.loadTimeout || 20000);
        onAbort = () => reject(abortError());
        this._abort.signal.addEventListener('abort', onAbort, { once: true });
      });
      try { await Promise.race([pending, cancelled]); }
      catch (error) { invalidate(); throw error; }
      finally {
        clearTimeout(timer);
        this._abort.signal.removeEventListener('abort', onAbort);
      }
    }

    _native(url) {
      const generation = this._bumpEngineGeneration();
      this.mode = 'native';
      const video = this.video = document.createElement('video');
      video.controls = false;
      video.playsInline = true;
      video.volume = this._volume;
      video.className = 'absolute inset-0 w-full h-full';
      this.host.append(video);
      const signal = this._abort.signal;
      const current = () => this._isCurrentEngine(null, video, generation);
      video.addEventListener('timeupdate', () => {
        if (current()) this._updateTime(video.currentTime);
      }, { signal });
      video.addEventListener('play', () => {
        if (current()) { this.playing = true; this._emitState(); }
      }, { signal });
      video.addEventListener('pause', () => {
        if (current()) { this.playing = false; this._emitState(); }
      }, { signal });
      video.addEventListener('ended', () => { if (current()) this.onEnded?.(); }, { signal });
      return new Promise((resolve, reject) => {
        let ready = false;
        video.addEventListener('error', () => {
          if (!current()) return;
          const error = new Error('视频解码失败 (' + (video.error?.code || 0) + ')');
          if (!ready) reject(error); else this._runtimeError(error);
        }, { signal });
        video.addEventListener('loadedmetadata', async () => {
          if (!current()) return;
          this.duration = Number.isFinite(video.duration) ? video.duration : this.duration;
          ready = true;
          try {
            if (this.playbackRate !== 1) await this.setPlaybackRate(this.playbackRate);
            if (!current()) return;
            await video.play();
          } catch (error) {
            if (!current()) return;
            if (error.name !== 'NotAllowedError') { reject(error); return; }
          }
          if (current()) resolve();
        }, { once: true, signal });
        video.src = url;
      });
    }

    async _libmedia(url) {
      const generation = this._bumpEngineGeneration();
      this.mode = 'libmedia';
      const core = this.core = new AVPlayer({
        container: this.host,
        wasmBaseUrl: new URL('/web/vendor/web/libmedia/wasm', location.origin).href,
        enableWebCodecs: true,
        enableHardware: true,
        enableWorker: true,
        // libmedia checks MIME support before this callback. Prefer browser
        // playback whenever possible, leaving decoded canvas for other codecs.
        checkUseMSE: () => true,
        preLoadTime: 2,
      });
      core.setVolume?.(this._volume);
      let resolveFrame, rejectFrame;
      const firstFrame = new Promise((resolve, reject) => { resolveFrame = resolve; rejectFrame = reject; });
      firstFrame.catch(() => {});
      const current = () => this._isCurrentEngine(core, null, generation);
      let started = false;
      core.on('firstVideoRendered', () => { if (current()) { started = true; resolveFrame(); } });
      core.on('firstAudioRendered', () => {
        if (current() && !core.hasVideo()) { started = true; resolveFrame(); }
      });
      core.on('error', error => {
        if (!current()) return;
        if (!started) rejectFrame(error); else this._runtimeError(error);
      });
      core.on('time', ms => { if (current()) this._updateTime(Number(ms) / 1000); });
      core.on('played', () => { if (current()) { this.playing = true; this._emitState(); } });
      core.on('paused', () => { if (current()) { this.playing = false; this._emitState(); } });
      core.on('ended', () => { if (current()) this.onEnded?.(); });
      this._resize = new ResizeObserver(() => {
        if (current()) core.resize(this.host.clientWidth, this.host.clientHeight);
      });
      this._resize.observe(this.host);
      const ext = /\.m3u8(?:\?|$)/i.test(url) ? 'm3u8' : (this.mediaSource.Container || 'mkv');
      await core.load(url, { ext, maxProbeDuration: 2 });
      if (!current()) return;
      const duration = Number(core.getDuration()) / 1000;
      if (duration > 0) this.duration = duration;
      await core.play({ audio: true, video: true, subtitle: true });
      if (!current()) return;
      if (this.playbackRate !== 1) {
        await this.setPlaybackRate(this.playbackRate);
        if (!current()) return;
      }
      // MSE uses HTMLMediaElement.play(), which resolves once playback starts;
      // libmedia's decoder-only firstVideoRendered event is not emitted there.
      if (core.isMSE()) { started = true; resolveFrame(); }
      core.setSubtitleEnable(false);
      await firstFrame;
      if (!current()) return;
      await this._requireHardware(core);
      if (!current()) return;
      if (this.options.audioIndex >= 0) {
        // W09: never treat the requested index as applied just because the
        // call returned. A boolean false leaves the preference untouched and
        // surfaces a diagnostic audioError for the app/recovery path.
        const audioApplied = await this.selectAudio(this.options.audioIndex);
        if (!current()) return;
        if (!audioApplied && !core.isHls() && !this.audioError) this._audioUnavailable('音轨无法在本地引擎中应用');
      }
      // A deep link may reach play() without a browser user gesture.
      if (!core.isMSE() && core.isSuspended()) { this.playing = false; this._emitState(); }
    }

    _updateTime(seconds) {
      this.positionTicks = Math.round(seconds * 10000000);
      this.subtitles?.render(seconds);
      this._emitState();
    }

    async _requireHardware(core) {
      if (this._destroyed || this.core !== core || core.isMSE()) return;
      const stream = core.getStreams().find(s => s.mediaType?.toLowerCase() === 'video');
      const size = stream?.codecparProxy;
      if (!size || size.width * size.height <= 1920 * 1080) return;
      // v1.3.1 exposes the decoder pipeline's actual choice. isConfigSupported
      // alone cannot tell whether a decoder subsequently fell back to software.
      const tasks = await core.VideoDecoderThread?.getTasksInfo?.();
      if (this.core !== core || this._destroyed) return;
      if (!tasks?.length || tasks.some(task => !task.hardware)) {
        throw new Error('高分辨率视频缺少硬件解码，请使用兼容播放');
      }
    }

    _cancelSubtitleWork() {
      this._subtitleSerial++;
      this._subtitleAbort?.abort();
      this._subtitleAbort = null;
      if (this._subtitleTimer != null) {
        clearTimeout(this._subtitleTimer);
        this._subtitleTimer = null;
      }
    }

    _audioUnavailable(message) {
      this.audioError = message;
      this._emitState({ audioError: message });
    }

    async _fallback() {
      this._fallbackUsed = true;
      const seconds = this.positionTicks / 10000000;
      // Capture user intent before replacing the engine. The new engine gets
      // its own default state, so restoring only the timestamp would resume
      // audio that the user had paused and reset the volume.
      const volume = this._volume;
      const paused = this._pauseRequested;
      this._cancelSubtitleWork();
      this.subtitles?.clear();
      await this._resetEngine();
      this._checkAlive();
      await this._attempt(this.fallbackUrl);
      this._checkAlive();
      this.setVolume(volume);
      if (seconds > 0) await this.seek(seconds);
      this._checkAlive();
      if (paused) await this.pause();
    }

    async _runtimeError(error) {
      if (this._destroyed || this._recovering) return;
      if (!this._fallbackUsed && this.fallbackUrl && this.fallbackUrl !== this.url) {
        this._recovering = true;
        try {
          await this._fallback();
          // Re-select subtitles in the background. A slow or broken subtitle
          // must not keep a recovered picture in the loading/error path.
          if (this.subtitleIndex >= 0) this._startDefaultSubtitle(this.subtitleIndex);
          this._emitState({ loading: false });
          return;
        } catch (fallbackError) { error = fallbackError; }
        finally { this._recovering = false; }
      }
      if (!this._destroyed) this.onError?.(error?.message || String(error));
    }

    _startDefaultSubtitle(index) {
      const generation = this._engineGeneration;
      const pending = this.selectSubtitle(index, { timeout: this.options.subtitleTimeout ?? 8000 });
      // selectSubtitle reports ordinary failures through onState itself.
      // Keep this chain handled even when destroy/fallback aborts it.
      this._defaultSubtitle = pending.then(() => {}, error => {
        if (error?.name === 'AbortError' || this._destroyed || generation !== this._engineGeneration) return;
        this._emitState({ subtitleError: '字幕加载失败：' + (error?.message || String(error)) });
      });
    }

    async selectSubtitle(index, options = {}) {
      this._checkAlive();
      const normalized = Number(index);
      const generation = this._engineGeneration;
      const requestSerial = ++this._subtitleSerial;
      this._subtitleAbort?.abort();
      const controller = new AbortController();
      this._subtitleAbort = controller;
      const timeout = Number(options.timeout ?? this.options?.subtitleTimeout) || 8000;
      const current = () => !this._destroyed && generation === this._engineGeneration
        && requestSerial === this._subtitleSerial && this._subtitleAbort === controller;
      let timedOut = false;
      let localTimer;
      const timeoutGuard = new Promise((_, reject) => {
        localTimer = setTimeout(() => {
          timedOut = true;
          controller.abort();
          reject(new Error('字幕加载超时'));
        }, timeout);
        localTimer.unref?.();
        this._subtitleTimer = localTimer;
      });
      const request = Promise.resolve().then(() => this.subtitles.select(normalized, { signal: controller.signal }));
      request.catch(() => {});
      try {
        const result = await Promise.race([request, timeoutGuard]);
        // Generation alone is insufficient: a superseded request on the same
        // engine may finish late and must not rewrite subtitleIndex/onState.
        if (!current()) throw abortError();
        if (result === false) {
          // A missing/unsupported track is a real selection failure. Do not
          // leave the UI or subtitleIndex claiming it was applied.
          if (normalized >= 0) this._emitState({ subtitleError: '字幕轨道不可用' });
          return false;
        }
        this.subtitleIndex = normalized;
        this._emitState({ subtitleIndex: normalized });
        return true;
      } catch (error) {
        if (!current()) throw abortError();
        if (timedOut) {
          this._emitState({ subtitleError: '字幕加载超时' });
          return false;
        }
        if (error?.name === 'AbortError') throw error;
        this._emitState({ subtitleError: '字幕加载失败：' + (error?.message || String(error)) });
        return false;
      } finally {
        clearTimeout(localTimer);
        if (this._subtitleTimer === localTimer) this._subtitleTimer = null;
        if (this._subtitleAbort === controller) this._subtitleAbort = null;
      }
    }

    async selectAudio(index) {
      this._checkAlive();
      const core = this.core;
      const generation = this._engineGeneration;
      // HLS audio is baked by the upstream request. The server may already
      // have applied it, so false is not a client-side failure here.
      if (!core || core.isHls()) return false;
      const stream = core.getStreams().find(s => s.mediaType?.toLowerCase() === 'audio' && s.index === index);
      if (!stream) {
        this._audioUnavailable('音轨轨道不存在或无法应用');
        return false;
      }
      if (core.getSelectedAudioStreamId() !== stream.id) await core.selectAudio(stream.id);
      if (!this._isCurrentEngine(core, null, generation)) throw abortError();
      const applied = core.getSelectedAudioStreamId?.();
      if (applied != null && applied !== stream.id) {
        this._audioUnavailable('音轨未能切换到所选轨道');
        return false;
      }
      this.audioIndex = index;
      this.options.audioIndex = index;
      this.audioError = null;
      return true;
    }

    get playbackRate() {
      return this._playbackRate ?? 1;
    }

    // A vendor write may throw (or read back wrong) after it already changed
    // the engine. Restore the previous rate at most once on the same engine
    // that is still current: no await, no retry loop and never a rollback on a
    // replaced generation. A failed restore reports false instead of pretending
    // the old rate was re-applied.
    _restorePlaybackRate(core, video, generation, previous) {
      const current = () => this._isCurrentEngine(core, video, generation);
      if (!current()) return false;
      try {
        if (core) core.setPlaybackRate(previous);
        else video.playbackRate = previous;
        if (!current()) return false;
        const restored = core ? core.getPlaybackRate() : video.playbackRate;
        return current() && restored === previous;
      } catch (error) { return false; }
    }

    async setPlaybackRate(rate) {
      this._checkAlive();
      const allowed = [0.5, 1, 1.25, 1.5, 2];
      if (typeof rate !== 'number' || !allowed.includes(rate)) {
        throw new TypeError('不支持的播放速度');
      }
      const generation = this._engineGeneration;
      const core = this.core;
      const video = this.video;
      // Fixed API checks run before any engine write.
      if (!core && !video) throw new Error('播放器尚未就绪');
      // Only explicit live evidence rejects: an infinite stream/duration is live,
      // while a missing or zero duration is normal for an ordinary movie.
      if (this.mediaSource?.IsInfiniteStream === true || (video && video.duration === Infinity)) {
        throw new Error('直播不支持切换播放速度');
      }
      if (core) {
        if (core.isLive?.()) throw new Error('直播不支持切换播放速度');
        if (typeof core.setPlaybackRate !== 'function' || typeof core.getPlaybackRate !== 'function') {
          throw new Error('当前引擎不支持切换播放速度');
        }
      }
      // The vendor setter/getter are synchronous. `previous` is the last
      // confirmed rate and is only used to undo a partial write.
      const previous = this.playbackRate;
      const current = () => this._isCurrentEngine(core, video, generation);
      let actual;
      try {
        if (core) {
          core.setPlaybackRate(rate);
          actual = core.getPlaybackRate();
        } else {
          video.playbackRate = rate;
          actual = video.playbackRate;
        }
      } catch (error) {
        if (current()) this._restorePlaybackRate(core, video, generation, previous);
        throw error;
      }
      if (!current()) throw abortError();
      if (actual !== rate) {
        // A mismatched readback is not applied state: restore once, but always
        // keep the fixed diagnostic even when the restore cannot confirm.
        this._restorePlaybackRate(core, video, generation, previous);
        throw new Error('无法应用播放速度');
      }
      // Only a confirmed request updates the cache; failures keep the last
      // confirmed rate instead of claiming the requested one.
      this._playbackRate = actual;
      this._emitState();
      return actual;
    }

    async play() {
      this._checkAlive();
      const generation = this._engineGeneration;
      const core = this.core;
      const video = this.video;
      if (core) {
        await core.resume();
        if (!this._isCurrentEngine(core, null, generation)) throw abortError();
        await core.play();
      } else if (video) {
        await video.play();
      } else {
        throw new Error('播放器尚未就绪');
      }
      if (!this._isCurrentEngine(core, video, generation)) throw abortError();
      this._pauseRequested = false;
      this.playing = true;
      this._emitState();
    }

    async pause() {
      this._pauseRequested = true;
      const generation = this._engineGeneration;
      const core = this.core;
      const video = this.video;
      if (core) await core.pause();
      else if (video) video.pause();
      else return;
      if (!this._isCurrentEngine(core, video, generation)) throw abortError();
      this.playing = false;
      this._emitState();
    }

    async seek(seconds, options = {}) {
      this._checkAlive();
      const target = Number(seconds);
      const timeout = normalizeSeekTimeout(options.timeout,
        normalizeSeekTimeout(this.options?.seekTimeout, DEFAULT_SEEK_TIMEOUT_MS));
      const label = normalizeSeekLabel(options.label);
      return this._coordinateSeek(async (attempt) => {
        if (this._destroyed || attempt.generation !== this._engineGeneration) throw abortError();
        if (attempt.core) {
          const core = attempt.core;
          await core.seek(BigInt(Math.round(target * 1000)));
          // The core seek cannot be cancelled. If its bounded adapter wait
          // already timed out, the late completion must not rewrite progress.
          if (attempt.timedOut) return;
          // A late seek from a replaced core must not write its progress into
          // the new engine's timeline.
          if (!this._isCurrentEngine(core, null, attempt.generation)) throw abortError();
          const actual = core.currentTime;
          this._updateTime(actual == null ? target : Number(actual) / 1000);
        } else if (attempt.video) {
          const video = attempt.video;
          if (this.video !== video) throw abortError();
          video.currentTime = target;
          if (attempt.timedOut) return;
          if (this.video === video) this._updateTime(target);
        } else {
          throw abortError();
        }
      }, { timeout, label });
    }

    setVolume(volume) {
      const value = Math.max(0, Math.min(1, Number(volume)));
      this._volume = Number.isFinite(value) ? value : 1;
      if (this.core) this.core.setVolume?.(this._volume);
      else if (this.video) this.video.volume = this._volume;
    }

    async _resetEngine() {
      this._resize?.disconnect();
      this._resize = null;
      const core = this.core;
      const video = this.video;
      this.core = null;
      this.video = null;
      // Invalidate queued/running seeks before destroying the old decoder.
      this._bumpEngineGeneration();
      if (video) {
        video.pause();
        video.removeAttribute('src');
        video.load();
        video.remove();
      }
      let teardownError = null;
      if (core) {
        try { await this._releaseCore(core); }
        catch (error) { teardownError = error; this._wedgeEngine(error); }
      }
      this.host.replaceChildren();
      this.playing = false;
      // Report the deadline failure instead of pretending cleanup succeeded;
      // the host cleanup gate must not create a new engine over a live decoder.
      if (teardownError) throw teardownError;
    }

    destroy() {
      if (this._destroyed) return this._cleanup || Promise.reject(this._engineWedged?.error || abortError());
      this._destroyed = true;
      this._abort.abort();
      clearInterval(this._progressTimer);
      clearInterval(this._decodeTimer);
      this._cancelSubtitleWork();
      this.subtitles?.destroy();
      this.host.remove();
      // The returned promise stays diagnosably rejected when the old decoder
      // outlives its deadline. The extra handler only logs; it does not replace
      // the promise the cleanup gate waits on.
      this._cleanup = this._resetEngine();
      this._cleanup.catch(error => console.warn('Player cleanup failed', error));
      return this._cleanup;
    }
  }

  window.EIOPlayer = EIOPlayer;
})();
