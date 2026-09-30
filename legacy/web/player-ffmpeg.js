/* Emby-In-One Web Player — player engine
 *
 * Handles format detection, MSE streaming, WASM software decode, HDR tone
 * mapping, PGS/ASS subtitles, and multi-channel audio with downmix.
 *
 * Loaded as a plain script after app.js. Exposes window.EIOPlayer.
 */

(function () {
  'use strict';

  /* ================= fMP4 parser ================= */

  function parseBoxes(data) {
    const boxes = [];
    let offset = 0;
    while (offset < data.length - 8) {
      let size = (data[offset] << 24) | (data[offset + 1] << 16) | (data[offset + 2] << 8) | data[offset + 3];
      const type = String.fromCharCode(data[offset + 4], data[offset + 5], data[offset + 6], data[offset + 7]);
      if (size === 1) {
        size = 0;
        for (let i = 0; i < 8; i++) size = size * 256 + data[offset + 8 + i];
        offset += 8;
      }
      if (size === 0) size = data.length - offset;
      boxes.push({ type, size, data: data.slice(offset, offset + size) });
      offset += size;
    }
    return boxes;
  }

  function concatUint8(arrays) {
    const total = arrays.reduce((n, a) => n + a.length, 0);
    const out = new Uint8Array(total);
    let off = 0;
    for (const a of arrays) { out.set(a, off); off += a.length; }
    return out;
  }

  function segmentFMp4(data) {
    const boxes = parseBoxes(data);
    const initParts = [];
    const segments = [];
    let current = null;
    for (const box of boxes) {
      if (box.type === 'ftyp' || box.type === 'moov') {
        initParts.push(box.data);
      } else if (box.type === 'styp') {
        // segment type — start of a new segment
        if (current) segments.push(concatUint8(current));
        current = [box.data];
      } else if (box.type === 'moof') {
        if (current) segments.push(concatUint8(current));
        current = [box.data];
      } else if (box.type === 'mdat' && current) {
        current.push(box.data);
      } else if (box.type === 'mfra') {
        // ignore
      }
    }
    if (current) segments.push(concatUint8(current));
    return { init: concatUint8(initParts), segments };
  }

  /* ================= MSE player ================= */

  class MSEPlayer {
    constructor(videoEl) {
      this.video = videoEl;
      this.ms = new MediaSource();
      this.sb = null;
      this.queue = [];
      this.updating = false;
      this.ended = false;
    }

    async init(mimeType) {
      return new Promise((resolve, reject) => {
        this.ms.addEventListener('sourceopen', () => {
          try {
            this.sb = this.ms.addSourceBuffer(mimeType);
            // fMP4 segments carry their own timestamps; 'sequence' mode
            // renumbers them and the buffer ends up with no playable range.
            this.sb.mode = 'segments';
            // updateend must clear the updating flag before flushing again:
            // leaving it set deadlocks the queue after the first append.
            this.sb.addEventListener('updateend', () => { this.updating = false; this._flush(); });
            this.sb.addEventListener('error', (e) => console.error('MSE sourceBuffer error', e));
            resolve();
          } catch (e) { reject(e); }
        }, { once: true });
        this.video.src = URL.createObjectURL(this.ms);
      });
    }

    append(data) {
      this.queue.push(data);
      this._flush();
    }

    _flush() {
      if (this.updating || !this.queue.length || !this.sb || this.sb.updating) return;
      this.updating = true;
      try {
        this.sb.appendBuffer(this.queue.shift());
      } catch (e) {
        console.error('MSE append error', e);
        this.updating = false;
      }
    }

    endOfStream() {
      if (this.ms.readyState === 'open') {
        try { this.ms.endOfStream(); } catch (e) { /* already ended */ }
      }
    }

    destroy() {
      this.queue = [];
      if (this.ms.readyState === 'open') {
        try { this.ms.endOfStream(); } catch (e) {}
      }
      if (this.video.src) URL.revokeObjectURL(this.video.src);
    }
  }

  /* ================= PGS renderer ================= */

  const PGS = {
    PDS: 0x14, ODS: 0x15, PCS: 0x16, WDS: 0x17, END: 0x80,
  };

  class PGSRenderer {
    constructor(canvas) {
      this.canvas = canvas;
      this.ctx = canvas.getContext('2d');
      this.displaySets = new Map(); // pts -> {pcs, wds, pds, ods, rendered}
      this.currentDS = null;
    }

    async load(url) {
      const res = await fetch(url);
      if (!res.ok) throw new Error('PGS fetch failed');
      const data = new Uint8Array(await res.arrayBuffer());
      this._parse(data);
    }

    _parse(data) {
      let off = 0;
      while (off < data.length - 13) {
        if (data[off] !== 0x50 || data[off + 1] !== 0x47) { off++; continue; }
        const pts = ((data[off + 2] << 24) | (data[off + 3] << 16) | (data[off + 4] << 8) | data[off + 5]) >>> 0;
        const type = data[off + 10];
        const size = (data[off + 11] << 8) | data[off + 12];
        const body = data.subarray(off + 13, off + 13 + size);
        const ptsSec = pts / 90000;

        if (!this.displaySets.has(ptsSec)) {
          this.displaySets.set(ptsSec, { pcs: null, wds: [], pds: [], ods: [], end: false });
        }
        const ds = this.displaySets.get(ptsSec);

        switch (type) {
          case PGS.PCS: ds.pcs = this._parsePCS(body); break;
          case PGS.WDS: ds.wds.push(this._parseWDS(body)); break;
          case PGS.PDS: ds.pds.push(this._parsePDS(body)); break;
          case PGS.ODS: ds.ods.push(this._parseODS(body)); break;
          case PGS.END: ds.end = true; break;
        }
        off += 13 + size;
      }
      // Sort by PTS
      this.sortedPts = [...this.displaySets.keys()].sort((a, b) => a - b);
    }

    _parsePCS(d) {
      let off = 0;
      const width = (d[off] << 8) | d[off + 1]; off += 2;
      const height = (d[off] << 8) | d[off + 1]; off += 2;
      off += 1; // frame rate
      off += 2; // composition number
      const state = d[off]; off += 1;
      const paletteUpdate = d[off] & 0x80; off += 1;
      const paletteId = d[off]; off += 1;
      const numObjects = d[off]; off += 1;
      const objects = [];
      for (let i = 0; i < numObjects; i++) {
        const objId = (d[off] << 8) | d[off + 1]; off += 2;
        const winId = d[off]; off += 1;
        const cropped = d[off] & 0x80; off += 1;
        const x = (d[off] << 8) | d[off + 1]; off += 2;
        const y = (d[off] << 8) | d[off + 1]; off += 2;
        let crop = null;
        if (cropped) {
          crop = {
            x: (d[off] << 8) | d[off + 1],
            y: (d[off + 2] << 8) | d[off + 3],
            w: (d[off + 4] << 8) | d[off + 5],
            h: (d[off + 6] << 8) | d[off + 7],
          };
          off += 8;
        }
        objects.push({ objId, winId, x, y, crop });
      }
      return { width, height, state, paletteId, objects };
    }

    _parseWDS(d) {
      let off = 0;
      const numWindows = d[off]; off += 1;
      const windows = [];
      for (let i = 0; i < numWindows; i++) {
        const id = d[off]; off += 1;
        const x = (d[off] << 8) | d[off + 1]; off += 2;
        const y = (d[off] << 8) | d[off + 1]; off += 2;
        const w = (d[off] << 8) | d[off + 1]; off += 2;
        const h = (d[off] << 8) | d[off + 1]; off += 2;
        windows.push({ id, x, y, w, h });
      }
      return windows;
    }

    _parsePDS(d) {
      let off = 0;
      const id = d[off]; off += 1;
      const version = d[off]; off += 1;
      const entries = [];
      while (off + 5 <= d.length) {
        const idx = d[off]; off += 1;
        const y = d[off]; off += 1;
        const cr = d[off]; off += 1;
        const cb = d[off]; off += 1;
        const alpha = d[off]; off += 1;
        entries.push({ idx, y, cr, cb, alpha });
      }
      return { id, version, entries };
    }

    _parseODS(d) {
      let off = 0;
      const objId = (d[off] << 8) | d[off + 1]; off += 2;
      const version = d[off]; off += 1;
      const lastInSeq = d[off] & 0x80; off += 1;
      const dataLen = (d[off] << 16) | (d[off + 1] << 8) | d[off + 2]; off += 3;
      const width = (d[off] << 8) | d[off + 1]; off += 2;
      const height = (d[off] << 8) | d[off + 1]; off += 2;
      const rleData = d.subarray(off, off + dataLen - 4);
      const pixels = this._decodeRLE(rleData, width, height);
      return { objId, version, lastInSeq, width, height, pixels };
    }

    _decodeRLE(data, width, height) {
      const pixels = new Uint8Array(width * height);
      let x = 0, y = 0, off = 0;
      while (off < data.length && y < height) {
        const b = data[off];
        if (b === 0) {
          const next = data[off + 1];
          if (next === 0) { x = 0; y++; off += 2; }
          else if (next < 0x40) { x += next; off += 2; }
          else if (next < 0x80) {
            const count = ((next & 0x3F) << 8) | data[off + 2];
            x += count; off += 3;
          } else if (next < 0xC0) {
            const count = ((next & 0x3F) << 8) | data[off + 2];
            const color = data[off + 3];
            for (let i = 0; i < count && x < width; i++) pixels[y * width + x++] = color;
            off += 4;
          } else {
            const count = ((next & 0x3F) << 16) | (data[off + 2] << 8) | data[off + 3];
            const color = data[off + 4];
            for (let i = 0; i < count && x < width; i++) pixels[y * width + x++] = color;
            off += 5;
          }
        } else {
          if (x < width) pixels[y * width + x++] = b;
          off++;
        }
      }
      return pixels;
    }

    _yuvToRgb(y, cr, cb) {
      const c = y - 16, d = cb - 128, e = cr - 128;
      return [
        Math.max(0, Math.min(255, (298 * c + 409 * e + 128) >> 8)),
        Math.max(0, Math.min(255, (298 * c - 100 * d - 208 * e + 128) >> 8)),
        Math.max(0, Math.min(255, (298 * c + 516 * d + 128) >> 8)),
      ];
    }

    _renderDS(ds) {
      if (!ds.pcs || !ds.pds.length || !ds.ods.length) return null;
      const pcs = ds.pcs;
      const pds = ds.pds.find(p => p.id === pcs.paletteId) || ds.pds[0];
      const palette = new Array(256);
      for (const e of pds.entries) {
        const [r, g, b] = this._yuvToRgb(e.y, e.cr, e.cb);
        palette[e.idx] = [r, g, b, e.alpha];
      }

      const offscreen = document.createElement('canvas');
      offscreen.width = pcs.width;
      offscreen.height = pcs.height;
      const octx = offscreen.getContext('2d');
      const imgData = octx.createImageData(pcs.width, pcs.height);

      for (const obj of pcs.objects) {
        const ods = ds.ods.find(o => o.objId === obj.objId);
        if (!ods) continue;
        const sx = obj.crop ? obj.crop.x : 0;
        const sy = obj.crop ? obj.crop.y : 0;
        const sw = obj.crop ? obj.crop.w : ods.width;
        const sh = obj.crop ? obj.crop.h : ods.height;
        for (let row = 0; row < sh; row++) {
          for (let col = 0; col < sw; col++) {
            const px = ods.pixels[(sy + row) * ods.width + (sx + col)];
            if (px === 0) continue; // transparent
            const [r, g, b, a] = palette[px] || [0, 0, 0, 0];
            const dx = obj.x + col, dy = obj.y + row;
            if (dx >= 0 && dx < pcs.width && dy >= 0 && dy < pcs.height) {
              const di = (dy * pcs.width + dx) * 4;
              imgData.data[di] = r;
              imgData.data[di + 1] = g;
              imgData.data[di + 2] = b;
              imgData.data[di + 3] = a;
            }
          }
        }
      }
      octx.putImageData(imgData, 0, 0);
      return offscreen;
    }

    render(time) {
      if (!this.sortedPts || !this.sortedPts.length) return;
      // Find the display set active at this time
      let activePts = null;
      for (let i = this.sortedPts.length - 1; i >= 0; i--) {
        if (this.sortedPts[i] <= time) { activePts = this.sortedPts[i]; break; }
      }
      if (activePts === null) {
        this.ctx.clearRect(0, 0, this.canvas.width, this.canvas.height);
        this.currentDS = null;
        return;
      }
      if (this.currentDS === activePts) return; // already rendered
      const ds = this.displaySets.get(activePts);
      if (!ds || !ds.end) return;
      const rendered = this._renderDS(ds);
      this.ctx.clearRect(0, 0, this.canvas.width, this.canvas.height);
      if (rendered) {
        // Scale to canvas size
        this.canvas.width = this.canvas.clientWidth * (window.devicePixelRatio || 1);
        this.canvas.height = this.canvas.clientHeight * (window.devicePixelRatio || 1);
        this.ctx.drawImage(rendered, 0, 0, this.canvas.width, this.canvas.height);
      }
      this.currentDS = activePts;
    }

    destroy() {
      this.displaySets.clear();
      this.currentDS = null;
    }
  }

  /* ================= HDR renderer ================= */

  class HDRRenderer {
    constructor(canvas) {
      this.canvas = canvas;
      this.gl = canvas.getContext('webgl', { alpha: false, depth: false, stencil: false, antialias: false });
      this.prog = null;
      this.tex = null;
      this.type = null;
    }

    init(rangeType) {
      this.type = rangeType;
      const gl = this.gl;
      if (!gl) return false;

      const vsSrc = `attribute vec2 aPos; varying vec2 vUV; void main(){ vUV=aPos*0.5+0.5; gl_Position=vec4(aPos,0,1); }`;
      let fsSrc;
      if (rangeType === 'DOVI') {
        fsSrc = `
          precision highp float; varying vec2 vUV; uniform sampler2D tex;
          uniform float lift,gain,gamma; uniform float pivot;
          vec3 pq2linear(vec3 v){ vec3 p=pow(v,vec3(1.0/78.84375)); vec3 n=max(p-0.8359375,vec3(0.0)); vec3 d=18.8515625-18.6875*p; return pow(n/d,vec3(1.0/0.1593017578125)); }
          void main(){ vec4 c=texture2D(tex,vUV); vec3 lin=pq2linear(c.rgb);
            float lum=0.2126*lin.x+0.7152*lin.y+0.0722*lin.z;
            float scaled=(lum-pivot)*gain+pivot; scaled=max(scaled,0.0);
            float t=lum>0.0?scaled/lum:0.0;
            vec3 outCol=lin*t; outCol=pow(outCol,vec3(gamma));
            outCol=mix(outCol*lift,outCol,step(0.0,pivot));
            gl_FragColor=vec4(clamp(outCol,0.0,1.0),c.a); }
        `;
      } else if (rangeType === 'HLG') {
        fsSrc = `
          precision highp float; varying vec2 vUV; uniform sampler2D tex; uniform float peak;
          const float a=0.17883277, b=0.28466892, c=0.55991073;
          vec3 hlg2linear(vec3 v){ vec3 r;
            if(v.x<=0.5)r.x=v.x*v.x/3.0; else r.x=(exp((v.x-c)/a)+b)/12.0;
            if(v.y<=0.5)r.y=v.y*v.y/3.0; else r.y=(exp((v.y-c)/a)+b)/12.0;
            if(v.z<=0.5)r.z=v.z*v.z/3.0; else r.z=(exp((v.z-c)/a)+b)/12.0;
            return r*peak/1000.0; }
          vec3 linear2bt709(vec3 v){ float lum=0.2126*v.x+0.7152*v.y+0.0722*v.z; float t=lum>0.0?pow(lum,0.2)/lum:0.0; return clamp(v*t,0.0,1.0); }
          void main(){ vec4 c=texture2D(tex,vUV); vec3 lin=hlg2linear(c.rgb); gl_FragColor=vec4(linear2bt709(lin),c.a); }
        `;
      } else {
        fsSrc = `
          precision highp float; varying vec2 vUV; uniform sampler2D tex; uniform float peak;
          const float m1=0.1593017578125, m2=78.84375, c1=0.8359375, c2=18.8515625, c3=18.6875;
          vec3 pq2linear(vec3 v){ vec3 p=pow(v,vec3(1.0/m2)); vec3 n=max(p-c1,vec3(0.0)); vec3 d=c2-c3*p; return pow(n/d,vec3(1.0/m1)); }
          vec3 linear2bt709(vec3 v){
            float a=0.2627*v.x+0.6780*v.y+0.0593*v.z;
            float b=a/10000.0*peak;
            float t=1.0/(1.0+b*b*0.05);
            vec3 col=v*t;
            col=pow(col,vec3(1.0/2.4));
            return clamp(col,0.0,1.0);
          }
          void main(){ vec4 c=texture2D(tex,vUV); vec3 lin=pq2linear(c.rgb); vec3 sdr=linear2bt709(lin); gl_FragColor=vec4(sdr,c.a); }
        `;
      }

      const vs = this._compile(gl.VERTEX_SHADER, vsSrc);
      const fs = this._compile(gl.FRAGMENT_SHADER, fsSrc);
      if (!vs || !fs) return false;
      this.prog = gl.createProgram();
      gl.attachShader(this.prog, vs);
      gl.attachShader(this.prog, fs);
      gl.linkProgram(this.prog);
      if (!gl.getProgramParameter(this.prog, gl.LINK_STATUS)) return false;

      gl.useProgram(this.prog);
      const buf = gl.createBuffer();
      gl.bindBuffer(gl.ARRAY_BUFFER, buf);
      gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 1, -1, -1, 1, 1, 1]), gl.STATIC_DRAW);
      const aPos = gl.getAttribLocation(this.prog, 'aPos');
      gl.enableVertexAttribArray(aPos);
      gl.vertexAttribPointer(aPos, 2, gl.FLOAT, false, 0, 0);

      this.tex = gl.createTexture();
      gl.activeTexture(gl.TEXTURE0);
      gl.bindTexture(gl.TEXTURE_2D, this.tex);
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR);
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR);
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
      gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);

      // Set uniforms
      const uPeak = gl.getUniformLocation(this.prog, 'peak');
      if (uPeak) gl.uniform1f(uPeak, 1000.0);
      if (rangeType === 'DOVI') {
        const uLift = gl.getUniformLocation(this.prog, 'lift');
        const uGain = gl.getUniformLocation(this.prog, 'gain');
        const uGamma = gl.getUniformLocation(this.prog, 'gamma');
        const uPivot = gl.getUniformLocation(this.prog, 'pivot');
        if (uLift) gl.uniform1f(uLift, 0.0);
        if (uGain) gl.uniform1f(uGain, 1.0);
        if (uGamma) gl.uniform1f(uGamma, 1.0);
        if (uPivot) gl.uniform1f(uPivot, 0.5);
      }
      return true;
    }

    _compile(type, src) {
      const gl = this.gl;
      const s = gl.createShader(type);
      gl.shaderSource(s, src);
      gl.compileShader(s);
      return gl.getShaderParameter(s, gl.COMPILE_STATUS) ? s : null;
    }

    render(source) {
      const gl = this.gl;
      if (!gl || !this.prog || !source) return;
      const w = source.videoWidth || source.displayWidth || source.width;
      const h = source.videoHeight || source.displayHeight || source.height;
      if (!w || !h) return;
      if (this.canvas.width !== w || this.canvas.height !== h) {
        this.canvas.width = w;
        this.canvas.height = h;
        gl.viewport(0, 0, w, h);
      }
      gl.activeTexture(gl.TEXTURE0);
      gl.bindTexture(gl.TEXTURE_2D, this.tex);
      gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, gl.RGBA, gl.UNSIGNED_BYTE, source);
      gl.drawArrays(gl.TRIANGLE_STRIP, 0, 4);
    }

    destroy() {
      if (this.gl) {
        this.gl.deleteTexture(this.tex);
        this.gl.deleteProgram(this.prog);
        this.gl.getExtension('WEBGL_lose_context')?.loseContext();
      }
    }
  }

  /* ================= Audio engine ================= */

  class AudioEngine {
    constructor() {
      this.ctx = null;
      this.node = null;
      this.gainNode = null;
      this.channels = 2;
      this.sampleRate = 48000;
    }

    async init(channels, sampleRate) {
      this.channels = Math.min(channels, 8);
      this.sampleRate = sampleRate;
      const AudioCtx = window.AudioContext || window.webkitAudioContext;
      this.ctx = new AudioCtx({ sampleRate: this.sampleRate });
      await this.ctx.audioWorklet.addModule(this._workletUrl());
      this.node = new AudioWorkletNode(this.ctx, 'eio-pcm', {
        outputChannelCount: [this.channels],
      });
      this.gainNode = this.ctx.createGain();
      this.node.connect(this.gainNode);
      this.gainNode.connect(this.ctx.destination);
    }

    _workletUrl() {
      const code = `
        class EIOPCM extends AudioWorkletProcessor {
          constructor() {
            super();
            this.queue = [];
            this.buf = null;
            this.idx = 0;
            this.port.onmessage = (e) => {
              if (e.data.type === 'pcm') this.queue.push(e.data.buffer);
              else if (e.data.type === 'flush') { this.queue = []; this.buf = null; this.idx = 0; }
            };
          }
          process(inputs, outputs) {
            const out = outputs[0];
            const n = out[0].length;
            for (let ch = 0; ch < out.length; ch++) {
              const dst = out[ch];
              for (let i = 0; i < n; i++) {
                if (!this.buf || this.idx >= this.buf.length / ${this.channels}) {
                  if (!this.queue.length) { dst[i] = 0; continue; }
                  this.buf = this.queue.shift();
                  this.idx = 0;
                }
                dst[i] = this.buf[this.idx * ${this.channels} + ch] || 0;
              }
            }
            if (this.buf) this.idx += n;
            return true;
          }
        }
        registerProcessor('eio-pcm', EIOPCM);
      `;
      return URL.createObjectURL(new Blob([code], { type: 'application/javascript' }));
    }

    pushPCM(buffer) {
      this.node?.port.postMessage({ type: 'pcm', buffer }, [buffer]);
    }

    flush() {
      this.node?.port.postMessage({ type: 'flush' });
    }

    setVolume(v) {
      if (this.gainNode) this.gainNode.gain.value = v;
    }

    resume() { this.ctx?.resume(); }
    suspend() { this.ctx?.suspend(); }

    destroy() {
      this.node?.disconnect();
      this.gainNode?.disconnect();
      this.ctx?.close();
    }
  }

  /* ================= Player ================= */

  class EIOPlayer {
    constructor(container) {
      this.container = container;
      this.video = null;
      this.hdrCanvas = null;
      this.subCanvas = null;
      this.mode = 'native';
      this.mse = null;
      this.hdrRenderer = null;
      this.pgsRenderer = null;
      this.jassub = null;
      this.audioEngine = null;
      this.ffmpeg = null;
      this.playing = false;
      this.duration = 0;
      this.positionTicks = 0;
      this.onProgress = null;
      this.onEnded = null;
      this.onError = null;
      this._rafId = null;
      this._progressTimer = null;
      this._abort = null;
    }

    async load(url, mediaSource, item) {
      this.duration = item.RunTimeTicks ? item.RunTimeTicks / 10000000 : 0;
      this._abort = new AbortController();

      const container = (mediaSource.Container || '').toLowerCase();
      const vStream = mediaSource.MediaStreams?.find(s => s.Type === 'Video');
      const aStream = mediaSource.MediaStreams?.find(s => s.Type === 'Audio');
      const isHDR = vStream?.VideoRange === 'HDR10' || vStream?.VideoRange === 'HLG' || vStream?.VideoRange === 'DOVI';
      // An HLS transcoding URL (from PlaybackInfo's TranscodingUrl) streams
      // progressively at the upstream instead of downloading the whole file.
      // It must be checked before the container-based branches: the container
      // still says "mkv" while the URL serves an HLS playlist.
      if (this._isHLSUrl(url)) {
        this.mode = 'hls';
        await this._setupHLS(url, mediaSource);
      } else if (this._canNativePlay(container, vStream, aStream)) {
        this.mode = 'native';
        await this._setupNative(url);
      } else if (this._canMSEPlay(container, vStream)) {
        this.mode = 'mse';
        await this._setupMSE(url, mediaSource, isHDR);
      } else {
        this.mode = 'wasm';
        await this._setupWASM(url, mediaSource, isHDR);
      }

      this._startProgressTimer();
    }
    _isHLSUrl(url) {
      return /\.m3u8(\?|$)/i.test(url || '');
    }

    // _setupHLS plays an upstream HLS transcode through MSE. Emby generates
    // segments on the fly, so the media playlist grows until the transcode
    // finishes: the loop re-fetches it, appends every new segment, and starts
    // playback as soon as the first one is buffered. This is what makes
    // 4K/HEVC sources playable in a browser at all — the direct stream would
    // need a full download and a local remux.
    async _setupHLS(url, mediaSource) {
      const signal = this._abort?.signal;
      const fetchText = async (u) => {
        const r = await fetch(u, { signal });
        if (!r.ok) throw new Error('HLS playlist fetch failed: ' + r.status);
        return r.text();
      };
      const fetchBuf = async (u) => {
        const r = await fetch(u, { signal });
        if (!r.ok) throw new Error('HLS segment fetch failed: ' + r.status);
        return r.arrayBuffer();
      };

      // Master playlist: pick the first variant and its codecs. The incoming
      // URL may be proxy-relative ("/Videos/..."), so resolve it against the
      // page origin first: URL resolution and fetch both need an absolute base.
      url = new URL(url, location.origin).toString();
      const master = await fetchText(url);
      let variantUrl = url;
      let codecs = 'avc1.640029,mp4a.40.2';
      const masterLines = master.split('\n').map(l => l.trim());
      for (let i = 0; i < masterLines.length; i++) {
        if (masterLines[i].startsWith('#EXT-X-STREAM-INF')) {
          const c = masterLines[i].match(/CODECS="([^"]+)"/);
          if (c) codecs = c[1];
          for (let j = i + 1; j < masterLines.length; j++) {
            if (masterLines[j] && !masterLines[j].startsWith('#')) {
              variantUrl = new URL(masterLines[j], url).toString();
              break;
            }
          }
          break;
        }
      }

      // Video element + MSE.
      this.video = document.createElement('video');
      this.video.className = 'absolute inset-0 w-full h-full';
      this.video.controls = true;
      this.video.playsInline = true;
      this.container.appendChild(this.video);
      this.mse = new MSEPlayer(this.video);
      await this.mse.init(`video/mp4; codecs="${codecs}"`);

      this.video.addEventListener('timeupdate', () => {
        this.positionTicks = Math.floor(this.video.currentTime * 10000000);
        if (this.pgsRenderer) this.pgsRenderer.render(this.video.currentTime);
      });
      this.video.addEventListener('ended', () => this._onEnded());

      // Stream the media playlist: append the init segment once, then every
      // new segment in order; re-fetch while the transcode is still running.
      let initAppended = false;
      let nextSegment = 0;
      let started = false;
      let finished = false;
      while (!finished) {
        const media = await fetchText(variantUrl);
        const lines = media.split('\n').map(l => l.trim()).filter(Boolean);
        let initUrl = null;
        const segments = [];
        let ended = false;
        for (const line of lines) {
          if (line.startsWith('#EXT-X-MAP:')) {
            const m = line.match(/URI="([^"]+)"/);
            if (m) initUrl = new URL(m[1], variantUrl).toString();
          } else if (line.startsWith('#EXT-X-ENDLIST')) {
            ended = true;
          } else if (line && !line.startsWith('#')) {
            segments.push(new URL(line, variantUrl).toString());
          }
        }
        if (initUrl && !initAppended) {
          this.mse.append(new Uint8Array(await fetchBuf(initUrl)));
          initAppended = true;
        }
        while (nextSegment < segments.length) {
          this.mse.append(new Uint8Array(await fetchBuf(segments[nextSegment])));
          nextSegment++;
          if (!started && nextSegment >= 1) {
            started = true;
            this.video.play().catch(() => {});
            this.playing = true;
          }
        }
        if (ended) {
          this.mse.endOfStream();
          finished = true;
          break;
        }
        // The transcode is still producing segments; poll for more.
        await new Promise(r => setTimeout(r, 2000));
      }
      this._setupSubtitles(mediaSource);
    }

    _canNativePlay(container, vStream, aStream) {
      if (!container || !vStream) return false;
      const okC = ['mp4', 'm4v', 'webm', 'mov'];
      const okV = ['h264', 'avc', 'av1', 'vp9'];
      const okA = ['aac', 'mp3', 'opus', 'vorbis', 'eac3', 'ac3'];
      return okC.includes(container) && okV.includes(vStream.Codec) && (!aStream || okA.includes(aStream.Codec));
    }

    _canMSEPlay(container, vStream) {
      // MSE can handle MP4 with HEVC if the platform supports it
      if (!container || !vStream) return false;
      if (container !== 'mkv' && container !== 'mp4') return false;
      // Check if browser supports HEVC in MSE
      if (vStream.Codec === 'hevc' || vStream.Codec === 'h265') {
        return MediaSource.isTypeSupported('video/mp4; codecs="hev1.1.6.L120.90"') ||
               MediaSource.isTypeSupported('video/mp4; codecs="hvc1.1.6.L120.90"');
      }
      if (vStream.Codec === 'av1') {
        return MediaSource.isTypeSupported('video/mp4; codecs="av01.0.04M.08"');
      }
      return MediaSource.isTypeSupported('video/mp4; codecs="avc1.640028"');
    }

    async _setupNative(url) {
      this.video = document.createElement('video');
      this.video.className = 'absolute inset-0 w-full h-full';
      this.video.controls = true;
      this.video.playsInline = true;
      this.video.src = url;
      this.container.appendChild(this.video);
      this.video.addEventListener('timeupdate', () => {
        this.positionTicks = Math.floor(this.video.currentTime * 10000000);
      });
      this.video.addEventListener('ended', () => this._onEnded());
      this.video.addEventListener('error', (e) => this._onError('Playback error'));
      await this.video.play().catch(() => {});
      this.playing = true;
    }

    async _setupMSE(url, mediaSource, isHDR) {
      // Fetch and remux
      const data = await this._fetchFile(url);
      const fmp4 = await this._remuxToFMp4(data, mediaSource.Container);
      const { init, segments } = segmentFMp4(new Uint8Array(fmp4));

      // Create video element
      this.video = document.createElement('video');
      this.video.className = 'absolute inset-0 w-full h-full';
      this.video.controls = true;
      this.video.playsInline = true;
      this.container.appendChild(this.video);

      // Setup MSE
      this.mse = new MSEPlayer(this.video);
      const codecs = this._getCodecs(mediaSource);
      await this.mse.init(`video/mp4; codecs="${codecs}"`);
      this.mse.append(init);
      for (const seg of segments) this.mse.append(seg);
      this.mse.endOfStream();

      // Setup HDR if needed
      if (isHDR) this._setupHDR(mediaSource);

      // Setup subtitles
      this._setupSubtitles(mediaSource);

      this.video.addEventListener('timeupdate', () => {
        this.positionTicks = Math.floor(this.video.currentTime * 10000000);
        if (this.pgsRenderer) this.pgsRenderer.render(this.video.currentTime);
      });
      this.video.addEventListener('ended', () => this._onEnded());
      await this.video.play().catch(() => {});
      this.playing = true;
    }

    async _setupWASM(url, mediaSource, isHDR) {
      // For HEVC without MSE support, use ffmpeg.wasm to remux to H.264
      // This is CPU-intensive but works everywhere
      this._showLoading('WASM 解码中（首次加载需下载 25MB 模块）...');
      try {
        const data = await this._fetchFile(url);
        const h264 = await this._transcodeToH264(data, mediaSource.Container);
        const blob = new Blob([h264], { type: 'video/mp4' });
        const objUrl = URL.createObjectURL(blob);
        this.video = document.createElement('video');
        this.video.className = 'absolute inset-0 w-full h-full';
        this.video.controls = true;
        this.video.playsInline = true;
        this.video.src = objUrl;
        this.container.appendChild(this.video);
        this._hideLoading();
        this.video.addEventListener('timeupdate', () => {
          this.positionTicks = Math.floor(this.video.currentTime * 10000000);
        });
        this.video.addEventListener('ended', () => this._onEnded());
        await this.video.play().catch(() => {});
        this.playing = true;
      } catch (e) {
        this._hideLoading();
        this._onError('WASM 解码失败：' + e.message);
      }
    }

    _getCodecs(mediaSource) {
      const v = mediaSource.MediaStreams?.find(s => s.Type === 'Video');
      const a = mediaSource.MediaStreams?.find(s => s.Type === 'Audio');
      const parts = [];
      if (v) {
        if (v.Codec === 'hevc' || v.Codec === 'h265') parts.push('hev1.1.6.L120.90');
        else if (v.Codec === 'av1') parts.push('av01.0.04M.08');
        else parts.push('avc1.640028');
      }
      if (a) {
        if (a.Codec === 'aac') parts.push('mp4a.40.2');
        else if (a.Codec === 'ac3') parts.push('ac-3');
        else if (a.Codec === 'eac3') parts.push('ec-3');
        else if (a.Codec === 'mp3') parts.push('mp4a.40.34');
        else parts.push('mp4a.40.2');
      }
      return parts.join(', ');
    }

    async _fetchFile(url) {
      const res = await fetch(url, { signal: this._abort?.signal });
      if (!res.ok) throw new Error('Fetch failed: ' + res.status);
      return new Uint8Array(await res.arrayBuffer());
    }

    async _ensureFFmpeg() {
      if (this.ffmpeg) return this.ffmpeg;
      const { createFFmpeg } = window.FFmpeg || {};
      if (!createFFmpeg) throw new Error('FFmpeg not loaded');
      this.ffmpeg = createFFmpeg({
        log: false,
        corePath: 'vendor/web/ffmpeg-core.js',
        wasmPath: 'vendor/web/ffmpeg-core.wasm',
        workerPath: 'vendor/web/ffmpeg-core.worker.js',
      });
      await this.ffmpeg.load();
      return this.ffmpeg;
    }

    async _remuxToFMp4(data, container) {
      const ffmpeg = await this._ensureFFmpeg();
      const input = 'input.' + (container || 'mkv');
      const output = 'output.mp4';
      await ffmpeg.FS('writeFile', input, data);
      await ffmpeg.run('-i', input, '-c', 'copy', '-movflags', 'frag_keyframe+empty_moov+default_base_moof', '-f', 'mp4', output);
      return ffmpeg.FS('readFile', output);
    }

    async _transcodeToH264(data, container) {
      const ffmpeg = await this._ensureFFmpeg();
      const input = 'input.' + (container || 'mkv');
      const output = 'output.mp4';
      await ffmpeg.FS('writeFile', input, data);
      await ffmpeg.run(
        '-i', input,
        '-c:v', 'libx264', '-preset', 'ultrafast', '-crf', '23',
        '-c:a', 'aac', '-b:a', '192k',
        '-movflags', 'frag_keyframe+empty_moov+default_base_moof',
        '-f', 'mp4', output
      );
      return ffmpeg.FS('readFile', output);
    }

    _setupHDR(mediaSource) {
      const vStream = mediaSource.MediaStreams?.find(s => s.Type === 'Video');
      if (!vStream) return;
      this.hdrCanvas = document.createElement('canvas');
      this.hdrCanvas.className = 'absolute inset-0 w-full h-full pointer-events-none';
      this.hdrCanvas.style.zIndex = '5';
      this.container.appendChild(this.hdrCanvas);
      this.hdrRenderer = new HDRRenderer(this.hdrCanvas);
      this.hdrRenderer.init(vStream.VideoRange);
      // Render loop
      const render = () => {
        if (this.hdrRenderer && this.video && !this.video.paused) {
          this.hdrRenderer.render(this.video);
        }
        this._rafId = requestAnimationFrame(render);
      };
      render();
    }

    _setupSubtitles(mediaSource) {
      const subs = mediaSource.MediaStreams?.filter(s => s.Type === 'Subtitle') || [];
      if (!subs.length) return;
      // Find default subtitle
      const defaultSub = subs.find(s => s.IsDefault) || subs[0];
      if (defaultSub) this.selectSubtitle(defaultSub.Index, mediaSource);
    }

    selectSubtitle(index, mediaSource) {
      // Cleanup existing
      if (this.jassub) { this.jassub.destroy(); this.jassub = null; }
      if (this.pgsRenderer) { this.pgsRenderer.destroy(); this.pgsRenderer = null; }

      if (index < 0) return;
      const track = mediaSource.MediaStreams?.find(s => s.Type === 'Subtitle' && s.Index === index);
      if (!track) return;

      if (track.Codec === 'ass' || track.Codec === 'ssa') {
        const video = this.video;
        if (!video) return;
        this.jassub = new JASSUB({
          video,
          subUrl: track.DeliveryUrl || `/Videos/${this.item?.Id}/${mediaSource.Id}/Subtitles/${index}/Stream.ass`,
          workerUrl: 'vendor/web/jassub-worker.js',
          wasmUrl: 'vendor/web/jassub-worker.wasm',
        });
      } else if (track.Codec === 'PGS') {
        this.subCanvas = document.createElement('canvas');
        this.subCanvas.className = 'absolute inset-0 w-full h-full pointer-events-none';
        this.subCanvas.style.zIndex = '10';
        this.container.appendChild(this.subCanvas);
        this.pgsRenderer = new PGSRenderer(this.subCanvas);
        const url = track.DeliveryUrl || `/Videos/${this.item?.Id}/${mediaSource.Id}/Subtitles/${index}/Stream.sup`;
        this.pgsRenderer.load(url).catch(e => console.error('PGS load failed', e));
      }
    }

    _startProgressTimer() {
      this._progressTimer = setInterval(() => {
        if (this.onProgress && this.positionTicks > 0) {
          this.onProgress(this.positionTicks);
        }
      }, 10000);
    }

    _showLoading(msg) {
      let overlay = this.container.querySelector('.player-loading');
      if (!overlay) {
        overlay = document.createElement('div');
        overlay.className = 'player-loading absolute inset-0 flex items-center justify-center bg-black/80 z-50';
        overlay.innerHTML = `<div class="text-center"><div class="spinner inline-block mb-4"></div><div class="text-white">${msg}</div></div>`;
        this.container.appendChild(overlay);
      }
    }

    _hideLoading() {
      const overlay = this.container.querySelector('.player-loading');
      if (overlay) overlay.remove();
    }

    _onEnded() {
      if (this.onEnded) this.onEnded();
    }

    _onError(msg) {
      if (this.onError) this.onError(msg);
    }

    play() { this.video?.play(); this.playing = true; }
    pause() { this.video?.pause(); this.playing = false; }
    seek(seconds) { if (this.video) this.video.currentTime = seconds; }
    setVolume(v) { if (this.video) this.video.volume = v; if (this.audioEngine) this.audioEngine.setVolume(v); }

    destroy() {
      if (this._rafId) cancelAnimationFrame(this._rafId);
      if (this._progressTimer) clearInterval(this._progressTimer);
      this._abort?.abort();
      this.mse?.destroy();
      this.hdrRenderer?.destroy();
      this.pgsRenderer?.destroy();
      this.jassub?.destroy();
      this.audioEngine?.destroy();
      if (this.video?.src) URL.revokeObjectURL(this.video.src);
      this.container.innerHTML = '';
    }
  }

  window.EIOPlayer = EIOPlayer;
})();
