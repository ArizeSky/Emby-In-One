# libmedia 1.3.1

Pinned player distribution: `@libmedia/avplayer@1.3.1`, UMD entry and all 31
webpack chunks from its `dist/umd` directory. Keep the chunks beside avplayer.js.

WASM files come from
https://github.com/zhaohappy/libmedia/tree/v1.3.1/dist and are served locally.
The subset contains h264, hevc, av1, vp9, vp8, aac, opus, eac3, ac3, mp3, flac,
pcm, dca (DTS), vorbis, resample, scale, and stretchpitch. Each module includes
baseline, atomic and SIMD variants selected automatically by libmedia.

License: LGPL-3.0, see COPYING.LGPLv3 and the pinned upstream source. Decoders
are fetched on demand; the entire WASM subset is not downloaded at startup.

The player adapter prefers MSE whenever libmedia's MIME support check passes,
with WebCodecs hardware acceleration enabled otherwise. Its high-resolution
software-decode safeguard depends on
the pinned 1.3.1 decoder pipeline's `getTasksInfo()` API; verify this integration
when upgrading the library.
