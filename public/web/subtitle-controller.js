/* Track selection and subtitle clocks for native video and libmedia. */
(function () {
  'use strict';

  const LIMITS = Object.freeze({
    text: 16 * 1024 * 1024,
    pgs: 64 * 1024 * 1024,
    font: 16 * 1024 * 1024,
  });

  // Frozen strict playback prefix. A URL under it is bound to one playback
  // attempt and authenticates with that attempt's capability, not with the
  // local metadata session.
  const STRICT_PLAYBACK_PREFIX = '/eio/playback/v1/';

  function abortError() {
    return new DOMException('Subtitle cancelled', 'AbortError');
  }

  function budgetError(limit) {
    const error = new Error('字幕文件过大（超过 ' + Math.round(limit / 1048576) + ' MiB）');
    error.name = 'SubtitleBudgetError';
    return error;
  }

  function decodeText(bytes) {
    if (typeof TextDecoder === 'function') return new TextDecoder().decode(bytes);
    let text = '';
    for (let offset = 0; offset < bytes.length; offset += 8192) {
      text += String.fromCharCode.apply(null, bytes.subarray(offset, offset + 8192));
    }
    return text;
  }

  function utf8Length(text) {
    let bytes = 0;
    for (const character of text) {
      const code = character.codePointAt(0);
      bytes += code < 0x80 ? 1 : code < 0x800 ? 2 : code < 0x10000 ? 3 : 4;
    }
    return bytes;
  }

  // Stream with a hard budget instead of trusting Content-Length. Unknown
  // lengths and lying headers both stop at the same limit.
  async function readLimited(response, limit, options = {}) {
    const mode = options.mode || 'text';
    const signal = options.signal;
    const declared = Number(response.headers?.get?.('Content-Length')
      ?? response.headers?.['content-length']);
    if (Number.isFinite(declared) && declared > limit) {
      try { await response.body?.cancel?.(); } catch (error) { /* body may already be locked */ }
      throw budgetError(limit);
    }
    if (response.body && typeof response.body.getReader === 'function') {
      const reader = response.body.getReader();
      const chunks = [];
      let total = 0;
      let onAbort;
      const aborted = signal ? new Promise((_, reject) => {
        onAbort = () => reject(abortError());
        if (signal.aborted) onAbort();
        else signal.addEventListener('abort', onAbort, { once: true });
      }) : null;
      try {
        for (;;) {
          const read = reader.read();
          const result = aborted ? await Promise.race([read, aborted]) : await read;
          if (result.done) break;
          const chunk = result.value;
          total += chunk.byteLength;
          if (total > limit) {
            const error = budgetError(limit);
            try { await reader.cancel(error); } catch (cancelError) { /* best effort */ }
            throw error;
          }
          chunks.push(chunk);
        }
      } catch (error) {
        if (error?.name === 'AbortError' || error?.name === 'SubtitleBudgetError') {
          try { await reader.cancel(error); } catch (cancelError) { /* best effort */ }
        }
        throw error;
      } finally {
        if (onAbort) signal.removeEventListener('abort', onAbort);
      }
      const bytes = new Uint8Array(total);
      let offset = 0;
      for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.byteLength; }
      return mode === 'text' ? decodeText(bytes) : bytes;
    }
    // Only non-streaming test doubles reach this branch. Production fetch
    // responses expose a body, so they never buffer an unknown length first.
    if (mode === 'text') {
      const text = await response.text();
      if (utf8Length(text) > limit) throw budgetError(limit);
      return text;
    }
    const data = new Uint8Array(await response.arrayBuffer());
    if (data.byteLength > limit) throw budgetError(limit);
    return data;
  }

  window.EIOReadLimited = readLimited;
  window.EIO_SUBTITLE_LIMITS = LIMITS;

  function parseTime(value) {
    return value.replace(',', '.').split(':').reduce((total, part) => total * 60 + Number(part), 0);
  }

  function parseCues(text) {
    const cues = [];
    for (const block of text.replace(/\r/g, '').split(/\n\s*\n/)) {
      const lines = block.split('\n');
      const timing = lines.findIndex(line => line.includes('-->'));
      if (timing < 0) continue;
      const times = lines[timing].match(/([\d:. ,]+)\s*-->\s*([\d:.,]+)/);
      if (!times) continue;
      const decoder = document.createElement('textarea');
      decoder.innerHTML = lines.slice(timing + 1).join('\n').replace(/<[^>]*>/g, '');
      cues.push({ start: parseTime(times[1].trim()), end: parseTime(times[2]), text: decoder.value });
    }
    return cues;
  }

  class EIOSubtitles {
    constructor(player) {
      this.player = player;
      this.serial = 0;
      this._resize = new ResizeObserver(() => this.resize());
      this._resize.observe(player.container);
    }

    clear() {
      this.serial++;
      this.abort?.abort();
      this._unlinkSignal?.();
      this._unlinkSignal = null;
      this.jassub?.destroy();
      this.pgs?.destroy();
      this.layer?.remove();
      this.jassub = this.pgs = this.layer = this.cues = null;
      this.player.core?.setSubtitleEnable(false);
    }

    _url(track, ext) {
      const { item, mediaSource } = this.player;
      let path = track.DeliveryUrl;
      const attempt = this.player.eioAttempt;
      if (!path) {
        const resource = `/Videos/${encodeURIComponent(item.Id)}/${encodeURIComponent(mediaSource.Id)}/Subtitles/${track.Index}/Stream.${ext}`;
        if (attempt) {
          path = `/eio/playback/v1/${encodeURIComponent(attempt.attemptId)}${resource}?api_key=${encodeURIComponent(attempt.token)}`;
        } else if (mediaSource.EioAttemptId) {
          throw new Error('严格字幕缺少播放尝试，已拒绝旧版路径');
        } else { path = resource; }
      }
      if (attempt) path = window.EIOPlaybackIdentity.strictResourceUrl(attempt, path);
      const url = new URL(path, location.origin);
      // All subtitle URLs stay on the authenticated proxy.
      if (url.origin !== location.origin) throw new Error('字幕地址未经过代理');
      // A strict playback URL already carries the attempt-bound playback
      // capability in api_key. Replacing it with the local metadata token would
      // break the attempt identity, so a strict-prefixed URL keeps its own
      // credential (and fails visibly if the server sent none).
      if (url.pathname.startsWith(STRICT_PLAYBACK_PREFIX)) {
        if (!url.searchParams.get('api_key')) throw new Error('严格字幕地址缺少播放能力凭据');
      } else {
        url.searchParams.set('api_key', localStorage.getItem('eio_token') || '');
      }
      url.searchParams.set('MediaSourceId', mediaSource.Id);
      return url.href;
    }

    _linkSignal(external) {
      const controller = new AbortController();
      this.abort = controller;
      let onAbort = null;
      if (external) {
        onAbort = () => controller.abort();
        if (external.aborted) onAbort();
        else external.addEventListener('abort', onAbort, { once: true });
      }
      // The unlink belongs to this request only. A superseded select may
      // settle after a newer one installed its listener; it must remove its
      // own listener and leave the newer one attached.
      const unlink = () => {
        if (!onAbort) return;
        external.removeEventListener('abort', onAbort);
        onAbort = null;
      };
      this._unlinkSignal = unlink;
      return { signal: controller.signal, unlink };
    }

    async select(index, options = {}) {
      this.clear();
      if (index < 0) return true;
      if (this.player._destroyed) throw abortError();
      const track = this.player.mediaSource.MediaStreams?.find(s => s.Type === 'Subtitle' && s.Index === index);
      if (!track) return false;
      const serial = this.serial;
      const { signal, unlink } = this._linkSignal(options.signal);
      try {
        const core = this.player.core;
        const codec = (track.Codec || '').toLowerCase();
        const isHls = typeof core?.isHls === 'function' ? core.isHls() : !!core?.isHls;
        const stream = core && !isHls && !track.IsExternal
          && core.getStreams().find(s => s.mediaType?.toLowerCase() === 'subtitle' && s.index === index);
        if (stream && !['pgs', 'pgssub', 'hdmv_pgs_subtitle'].includes(codec)) {
          if (core.getSelectedSubtitleStreamId() !== stream.id) await core.selectSubtitle(stream.id);
          if (serial !== this.serial || this.player._destroyed) return false;
          core.setSubtitleEnable(true);
          // Embedded cues may have been consumed while subtitles were disabled.
          // Repositioning goes through the same engine queue as user seeks and
          // reads the current position only when the job executes.
          const refresh = async () => {
            if (serial !== this.serial || this.player._destroyed) return;
            const current = this.player.core;
            if (!current) return;
            const now = current.currentTime;
            if (now != null && Number(now) > 0) await current.seek(now);
          };
          if (typeof this.player._coordinateSeek === 'function') {
            await this.player._coordinateSeek(refresh, { timeout: 8000, label: 'subtitle-seek' });
          } else {
            await refresh();
          }
          return serial === this.serial && !this.player._destroyed;
        }
        if (['pgs', 'pgssub', 'hdmv_pgs_subtitle'].includes(codec)) {
          this.layer = this._canvas();
          this.pgs = new EIOPGSRenderer(this.layer);
          await this.pgs.load(this._url(track, 'sup'), signal);
        } else {
          const ass = codec === 'ass' || codec === 'ssa';
          const response = await fetch(this._url(track, ass ? 'ass' : 'vtt'), { signal });
          if (!response.ok) throw new Error('HTTP ' + response.status);
          const content = await readLimited(response, LIMITS.text, { mode: 'text', signal });
          if (serial !== this.serial || this.player._destroyed) return false;
          if (ass) {
            // Native video and HLS external ASS need a separate libass overlay.
            const fontResponse = await fetch('/web/vendor/web/NotoSansSC-Regular.otf', { signal });
            if (!fontResponse.ok) throw new Error('ASS 字体加载失败');
            const font = await readLimited(fontResponse, LIMITS.font, { mode: 'bytes', signal });
            if (serial !== this.serial || this.player._destroyed) return false;
            this.layer = this._canvas();
            this.jassub = new JASSUB({ canvas: this.layer, subContent: content,
              workerUrl: '/web/vendor/web/jassub-worker.js?v=20260930', wasmUrl: '/web/vendor/web/jassub-worker.wasm?v=1.7.17',
              modernWasmUrl: '/web/vendor/web/jassub-worker-modern.wasm?v=1.7.17',
              fallbackFont: 'Noto Sans SC',
              fonts: [font], availableFonts: {},
              targetFps: 24, libassMemoryLimit: 32 * 1024 * 1024, libassGlyphLimit: 16 * 1024 * 1024,
              onDemandRender: false, offscreenRender: false, useLocalFonts: false });
            const renderer = this.jassub;
            renderer.addEventListener('error', () => {
              if (serial === this.serial) this.player._emitState({ subtitleError: 'ASS 字幕渲染失败' });
            });
            this.resize();
          } else {
            this.cues = parseCues(content);
            this.layer = document.createElement('div');
            this.layer.className = 'player-subtitle-text';
            this.player.container.append(this.layer);
          }
        }
        if (serial !== this.serial || this.player._destroyed) return false;
        this.render(this.player.positionTicks / 10000000);
        return true;
      } finally {
        unlink();
        // Only clear the shared field when it still points at this request;
        // a newer select may already own it.
        if (this._unlinkSignal === unlink) this._unlinkSignal = null;
      }
    }

    _canvas() {
      const canvas = document.createElement('canvas');
      canvas.className = 'absolute inset-0 w-full h-full pointer-events-none';
      canvas.style.zIndex = '10';
      this.player.container.append(canvas);
      return canvas;
    }

    resize() {
      if (this.pgs) this.pgs.render(this.player.positionTicks / 10000000);
      if (!this.jassub || !this.layer) return;
      const video = this.player.mediaSource.MediaStreams?.find(s => s.Type === 'Video');
      const host = this.player.container;
      const ratio = (video?.Width || 16) / (video?.Height || 9);
      const width = Math.min(host.clientWidth, host.clientHeight * ratio);
      const height = width / ratio;
      this.layer.style.width = width + 'px';
      this.layer.style.height = height + 'px';
      this.jassub.resize(width, height, (host.clientHeight - height) / 2, (host.clientWidth - width) / 2,
        !this.player.playing);
    }

    render(seconds) {
      this.pgs?.render(seconds);
      this.jassub?.setCurrentTime(!this.player.playing, seconds, 1);
      if (this.cues && this.layer) {
        this.layer.textContent = this.cues.filter(cue => cue.start <= seconds && seconds < cue.end)
          .map(cue => cue.text).join('\n');
      }
    }

    destroy() { this.clear(); this._resize.disconnect(); }
  }

  window.EIOSubtitles = EIOSubtitles;
})();
