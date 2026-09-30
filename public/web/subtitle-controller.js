/* Track selection and subtitle clocks for native video and libmedia. */
(function () {
  'use strict';

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
      this.jassub?.destroy();
      this.pgs?.destroy();
      this.layer?.remove();
      this.jassub = this.pgs = this.layer = this.cues = null;
      this.player.core?.setSubtitleEnable(false);
    }

    _url(track, ext) {
      const { item, mediaSource } = this.player;
      const path = track.DeliveryUrl || `/Videos/${item.Id}/${mediaSource.Id}/Subtitles/${track.Index}/Stream.${ext}`;
      const url = new URL(path, location.origin);
      // All subtitle URLs stay on the authenticated proxy.
      if (url.origin !== location.origin) throw new Error('字幕地址未经过代理');
      url.searchParams.set('api_key', localStorage.getItem('eio_token') || '');
      return url.href;
    }

    async select(index) {
      this.clear();
      if (index < 0 || this.player._destroyed) return;
      const track = this.player.mediaSource.MediaStreams?.find(s => s.Type === 'Subtitle' && s.Index === index);
      if (!track) return;
      const serial = this.serial;
      const core = this.player.core;
      // Embedded text/ASS tracks use libmedia's demux and subtitle renderer.
      const stream = core && !core.isHls() && !track.IsExternal
        && core.getStreams().find(s => s.mediaType?.toLowerCase() === 'subtitle' && s.index === index);
      const codec = (track.Codec || '').toLowerCase();
      if (stream && !['pgs', 'pgssub', 'hdmv_pgs_subtitle'].includes(codec)) {
        if (core.getSelectedSubtitleStreamId() !== stream.id) await core.selectSubtitle(stream.id);
        if (serial === this.serial && !this.player._destroyed) core.setSubtitleEnable(true);
        // Embedded cues may have been consumed while subtitles were disabled.
        // Seeking to the same timestamp repopulates their demux queue.
        if (serial === this.serial && Number(core.currentTime) > 0) await core.seek(core.currentTime);
        return;
      }
      this.abort = new AbortController();
      const signal = this.abort.signal;
      if (['pgs', 'pgssub', 'hdmv_pgs_subtitle'].includes(codec)) {
        this.layer = this._canvas();
        this.pgs = new EIOPGSRenderer(this.layer);
        await this.pgs.load(this._url(track, 'sup'), signal);
      } else {
        const ass = codec === 'ass' || codec === 'ssa';
        const response = await fetch(this._url(track, ass ? 'ass' : 'vtt'), { signal });
        if (!response.ok) throw new Error('HTTP ' + response.status);
        const content = await response.text();
        if (serial !== this.serial || this.player._destroyed) return;
        if (ass) {
          // Native video and HLS external ASS need a separate libass overlay.
          const fontResponse = await fetch('/web/vendor/web/NotoSansSC-Regular.otf', { signal });
          if (!fontResponse.ok) throw new Error('ASS 字体加载失败');
          const font = new Uint8Array(await fontResponse.arrayBuffer());
          if (serial !== this.serial || this.player._destroyed) return;
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
      if (serial === this.serial) this.render(this.player.positionTicks / 10000000);
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
