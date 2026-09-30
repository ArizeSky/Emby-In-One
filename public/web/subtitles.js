/* Subtitle overlays shared by native video and libmedia. */
(function () {
  'use strict';
  const PGS = {
    PDS: 0x14, ODS: 0x15, PCS: 0x16, WDS: 0x17, END: 0x80,
  };

  class PGSRenderer {
    constructor(canvas) {
      this.canvas = canvas;
      this.ctx = canvas.getContext('2d');
      this.displaySets = new Map(); // pts -> {pcs, wds, pds, ods, rendered}
      this.currentDS = null;
      this.palettes = new Map();
      this.objects = new Map();
      this.fragments = new Map();
    }

    async load(url, signal) {
      const res = await fetch(url, { signal });
      if (!res.ok) throw new Error('PGS fetch failed');
      const data = new Uint8Array(await res.arrayBuffer());
      this._parse(data);
    }

    _parse(data) {
      let off = 0;
      let ds = null;
      while (off <= data.length - 13) {
        if (data[off] !== 0x50 || data[off + 1] !== 0x47) { off++; continue; }
        const pts = ((data[off + 2] << 24) | (data[off + 3] << 16) | (data[off + 4] << 8) | data[off + 5]) >>> 0;
        const type = data[off + 10];
        const size = (data[off + 11] << 8) | data[off + 12];
        if (off + 13 + size > data.length) throw new Error('Truncated PGS segment');
        const body = data.subarray(off + 13, off + 13 + size);
        const ptsSec = pts / 90000;

        switch (type) {
          case PGS.PCS: {
            const pcs = this._parsePCS(body);
            if (pcs.state === 0x80) { this.palettes.clear(); this.objects.clear(); this.fragments.clear(); }
            ds = { pcs, wds: [], pds: [], ods: [], end: false };
            this.displaySets.set(ptsSec, ds);
            break;
          }
          case PGS.WDS: if (ds) ds.wds.push(this._parseWDS(body)); break;
          case PGS.PDS: {
            const palette = this._parsePDS(body);
            const entries = new Map((this.palettes.get(palette.id)?.entries || []).map(e => [e.idx, e]));
            for (const entry of palette.entries) entries.set(entry.idx, entry);
            this.palettes.set(palette.id, { ...palette, entries: [...entries.values()] });
            break;
          }
          case PGS.ODS: {
            const object = this._parseODS(body);
            if (object) this.objects.set(object.objId, object);
            break;
          }
          case PGS.END:
            if (ds) { ds.pds = [...this.palettes.values()]; ds.ods = [...this.objects.values()]; ds.end = true; }
            break;
        }
        off += 13 + size;
      }
      // Sort by PTS
      this.sortedPts = [...this.displaySets.keys()].sort((a, b) => a - b);
    }

    _parsePCS(d) {
      if (d.length < 11) throw new Error('Truncated PGS composition');
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
      if (!width || !height || width * height > 16777216) throw new Error('Invalid PGS composition size');
      for (let i = 0; i < numObjects; i++) {
        if (off + 8 > d.length) throw new Error('Truncated PGS composition object');
        const objId = (d[off] << 8) | d[off + 1]; off += 2;
        const winId = d[off]; off += 1;
        const cropped = d[off] & 0x80; off += 1;
        const x = (d[off] << 8) | d[off + 1]; off += 2;
        const y = (d[off] << 8) | d[off + 1]; off += 2;
        let crop = null;
        if (cropped) {
          if (off + 8 > d.length) throw new Error('Truncated PGS crop');
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
      if (!d.length || d.length < 1 + d[0] * 9) throw new Error('Truncated PGS window');
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
      if (d.length < 2 || (d.length - 2) % 5) throw new Error('Truncated PGS palette');
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
      if (d.length < 4) throw new Error('Truncated PGS object');
      const objId = (d[0] << 8) | d[1];
      const flags = d[3];
      let offset = 4;
      if (flags & 0x80) {
        if (d.length < 11) throw new Error('Truncated PGS object header');
        const length = ((d[4] << 16) | (d[5] << 8) | d[6]) - 4;
        const width = (d[7] << 8) | d[8];
        const height = (d[9] << 8) | d[10];
        if (length < 0 || !width || !height || width * height > 16777216) throw new Error('Invalid PGS object size');
        this.fragments.set(objId, { width, height, data: new Uint8Array(length), offset: 0 });
        offset = 11;
      }
      const fragment = this.fragments.get(objId);
      if (!fragment || fragment.offset + d.length - offset > fragment.data.length) throw new Error('Invalid PGS object fragment');
      fragment.data.set(d.subarray(offset), fragment.offset);
      fragment.offset += d.length - offset;
      if (!(flags & 0x40)) return null;
      if (fragment.offset !== fragment.data.length) throw new Error('Incomplete PGS object');
      const pixels = this._decodeRLE(fragment.data, fragment.width, fragment.height);
      this.fragments.delete(objId);
      return { objId, width: fragment.width, height: fragment.height, pixels };
    }

    _decodeRLE(data, width, height) {
      const pixels = new Uint8Array(width * height);
      let x = 0, y = 0, off = 0;
      while (off < data.length && y < height) {
        const b = data[off];
        if (b === 0) {
          if (off + 2 > data.length) throw new Error('Truncated PGS run');
          const next = data[off + 1];
          const bytes = next < 0x40 ? 2 : next < 0xC0 ? 3 : 4;
          if (off + bytes > data.length) throw new Error('Truncated PGS run');
          if (next === 0) { x = 0; y++; off += 2; }
          else if (next < 0x40) { x += next; off += 2; }
          else if (next < 0x80) {
            const count = ((next & 0x3F) << 8) | data[off + 2];
            x += count; off += 3;
          } else if (next < 0xC0) {
            const count = next & 0x3F;
            const color = data[off + 2];
            for (let i = 0; i < count && x < width; i++) pixels[y * width + x++] = color;
            off += 3;
          } else {
            const count = ((next & 0x3F) << 8) | data[off + 2];
            const color = data[off + 3];
            for (let i = 0; i < count && x < width; i++) pixels[y * width + x++] = color;
            off += 4;
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
      if (!ds.pcs?.objects.length || !ds.pds.length || !ds.ods.length) return null;
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
      const width = Math.round(this.canvas.clientWidth * (window.devicePixelRatio || 1));
      const height = Math.round(this.canvas.clientHeight * (window.devicePixelRatio || 1));
      if (this.canvas.width !== width || this.canvas.height !== height) {
        this.canvas.width = width;
        this.canvas.height = height;
        this.currentDS = null;
      }
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
        const scale = Math.min(this.canvas.width / rendered.width, this.canvas.height / rendered.height);
        const width = rendered.width * scale, height = rendered.height * scale;
        this.ctx.drawImage(rendered, (this.canvas.width - width) / 2, (this.canvas.height - height) / 2, width, height);
      }
      this.currentDS = activePts;
    }

    destroy() {
      this.displaySets.clear();
      this.palettes.clear();
      this.objects.clear();
      this.fragments.clear();
      this.currentDS = null;
      this.ctx.clearRect(0, 0, this.canvas.width, this.canvas.height);
    }
  }


  window.EIOPGSRenderer = PGSRenderer;
})();

