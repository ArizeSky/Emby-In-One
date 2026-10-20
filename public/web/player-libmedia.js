/* Emby-In-One player: native MP4, streaming libmedia, upstream HLS fallback. */
(function () {
  'use strict';

  class EIOPlayer {
    constructor(container) {
      this.container = container;
      // Vue owns the controls; each engine owns only its separate host.
      this.host = document.createElement('div');
      this.host.className = 'absolute inset-0';
      container.prepend(this.host);
      this.mode = 'native';
      this.playing = false;
      this.duration = 0;
      this.positionTicks = 0;
      this._abort = new AbortController();
      this._destroyed = false;
      this._fallbackUsed = false;
      this._recovering = false;
      this.onProgress = this.onEnded = this.onError = this.onState = null;
    }

    async load(url, mediaSource, item, options = {}) {
      this.mediaSource = mediaSource;
      this.item = item;
      this.options = options;
      this.duration = (item.RunTimeTicks || mediaSource.RunTimeTicks || 0) / 10000000;
      this.url = new URL(url, location.origin).href;
      this.fallbackUrl = options.fallbackUrl && new URL(options.fallbackUrl, location.origin).href;
      this.subtitles = new EIOSubtitles(this);
      try { await this._attempt(this.url); }
      catch (error) {
        this._checkAlive();
        if (!this.fallbackUrl || this.fallbackUrl === this.url) throw error;
        await this._fallback();
      }
      this._checkAlive();
      this._progressTimer = setInterval(() => this.onProgress?.(this.positionTicks), 10000);
      this._decodeTimer = setInterval(() => {
        const core = this.core;
        if (core && !this._recovering) this._requireHardware(core).catch(error => this._runtimeError(error));
      }, 5000);
      const subtitles = mediaSource.MediaStreams?.filter(s => s.Type === 'Subtitle') || [];
      const index = options.subtitleIndex ?? mediaSource.DefaultSubtitleStreamIndex;
      const selected = index == null ? subtitles.find(s => s.IsDefault)?.Index : index;
      if (selected != null) await this.selectSubtitle(selected);
      this._emitState({ loading: false });
    }

    _checkAlive() {
      if (this._destroyed) throw new DOMException('Playback cancelled', 'AbortError');
    }

    _emitState(extra = {}) {
      if (this._destroyed) return;
      this.onState?.({ mode: this.mode, playing: this.playing, time: this.positionTicks / 10000000,
        duration: this.duration, ...extra });
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
      this._emitState({ loading: true, message: this._fallbackUsed ? '正在加载转码流…' : '正在加载媒体…' });
      const pending = this._canNativePlay(url) ? this._native(url) : this._libmedia(url);
      let timer, onAbort;
      const cancelled = new Promise((_, reject) => {
        timer = setTimeout(() => reject(new Error('媒体加载超时')), this.options.loadTimeout || 20000);
        onAbort = () => reject(new DOMException('Playback cancelled', 'AbortError'));
        this._abort.signal.addEventListener('abort', onAbort, { once: true });
      });
      try { await Promise.race([pending, cancelled]); }
      finally {
        clearTimeout(timer);
        this._abort.signal.removeEventListener('abort', onAbort);
      }
    }

    _native(url) {
      this.mode = 'native';
      const video = this.video = document.createElement('video');
      video.controls = false;
      video.playsInline = true;
      video.className = 'absolute inset-0 w-full h-full';
      this.host.append(video);
      const signal = this._abort.signal;
      video.addEventListener('timeupdate', () => this._updateTime(video.currentTime), { signal });
      video.addEventListener('play', () => { this.playing = true; this._emitState(); }, { signal });
      video.addEventListener('pause', () => { this.playing = false; this._emitState(); }, { signal });
      video.addEventListener('ended', () => this.onEnded?.(), { signal });
      return new Promise((resolve, reject) => {
        let ready = false;
        video.addEventListener('error', () => {
          const error = new Error('视频解码失败 (' + (video.error?.code || 0) + ')');
          if (!ready) reject(error); else this._runtimeError(error);
        }, { signal });
        video.addEventListener('loadedmetadata', async () => {
          if (this._destroyed || this.video !== video) return;
          this.duration = Number.isFinite(video.duration) ? video.duration : this.duration;
          ready = true;
          try { await video.play(); }
          catch (error) { if (error.name !== 'NotAllowedError') { reject(error); return; } }
          resolve();
        }, { once: true, signal });
        video.src = url;
      });
    }

    async _libmedia(url) {
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
      let resolveFrame, rejectFrame;
      const firstFrame = new Promise((resolve, reject) => { resolveFrame = resolve; rejectFrame = reject; });
      firstFrame.catch(() => {});
      const current = () => !this._destroyed && this.core === core;
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
      // MSE uses HTMLMediaElement.play(), which resolves once playback starts;
      // libmedia's decoder-only firstVideoRendered event is not emitted there.
      if (core.isMSE()) { started = true; resolveFrame(); }
      core.setSubtitleEnable(false);
      await firstFrame;
      await this._requireHardware(core);
      if (this.options.audioIndex >= 0) await this.selectAudio(this.options.audioIndex);
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

    async _fallback() {
      this._fallbackUsed = true;
      const seconds = this.positionTicks / 10000000;
      this.subtitles?.clear();
      await this._resetEngine();
      this._checkAlive();
      await this._attempt(this.fallbackUrl);
      this._checkAlive();
      if (seconds > 0) await this.seek(seconds);
    }

    async _runtimeError(error) {
      if (this._destroyed || this._recovering) return;
      if (!this._fallbackUsed && this.fallbackUrl && this.fallbackUrl !== this.url) {
        this._recovering = true;
        try {
          await this._fallback();
          if (this.subtitleIndex >= 0) await this.selectSubtitle(this.subtitleIndex);
          this._emitState({ loading: false });
          return;
        } catch (fallbackError) { error = fallbackError; }
        finally { this._recovering = false; }
      }
      if (!this._destroyed) this.onError?.(error?.message || String(error));
    }

    async selectSubtitle(index) {
      this.subtitleIndex = index;
      try { await this.subtitles.select(index); }
      catch (error) {
        if (!this._destroyed && error.name !== 'AbortError') {
          this._emitState({ subtitleError: '字幕加载失败：' + error.message });
        }
      }
    }

    async selectAudio(index) {
      this._checkAlive();
      if (!this.core || this.core.isHls()) return false;
      const stream = this.core.getStreams().find(s => s.mediaType?.toLowerCase() === 'audio' && s.index === index);
      if (!stream) return false;
      if (this.core.getSelectedAudioStreamId() !== stream.id) await this.core.selectAudio(stream.id);
      this.audioIndex = index;
      this.options.audioIndex = index;
      return true;
    }

    async play() {
      this._checkAlive();
      if (this.core) { await this.core.resume(); await this.core.play(); }
      else await this.video?.play();
      this.playing = true;
      this._emitState();
    }
    pause() { return this.core ? this.core.pause() : this.video?.pause(); }
    async seek(seconds) {
      this._checkAlive();
      if (this.core) {
        await this.core.seek(BigInt(Math.round(seconds * 1000)));
        this._checkAlive();
        const actual = this.core.currentTime;
        this._updateTime(actual == null ? seconds : Number(actual) / 1000);
      } else {
        this.video.currentTime = seconds;
        this._updateTime(seconds);
      }
    }
    setVolume(volume) { if (this.core) this.core.setVolume(volume); else if (this.video) this.video.volume = volume; }

    async _resetEngine() {
      this._resize?.disconnect();
      const core = this.core;
      this.core = null;
      if (this.video) {
        this.video.pause();
        this.video.removeAttribute('src');
        this.video.load();
        this.video.remove();
        this.video = null;
      }
      if (core) await core.destroy();
      this.host.replaceChildren();
      this.playing = false;
    }

    destroy() {
      if (this._destroyed) return this._cleanup;
      this._destroyed = true;
      this._abort.abort();
      clearInterval(this._progressTimer);
      clearInterval(this._decodeTimer);
      this.subtitles?.destroy();
      this.host.remove();
      this._cleanup = this._resetEngine().catch(error => console.warn('Player cleanup failed', error));
      return this._cleanup;
    }
  }

  window.EIOPlayer = EIOPlayer;
})();
