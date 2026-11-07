# libmedia 1.3.1

Pinned player distribution: `@libmedia/avplayer@1.3.1`, UMD entry and the
webpack chunks from its `dist/umd` directory that remain after the dead-code
pruning below. Keep the chunks beside avplayer.js.

WASM files come from
https://github.com/zhaohappy/libmedia/tree/v1.3.1/dist and are served locally.
The subset contains h264, hevc, aac, opus, eac3, ac3, mp3, flac,
pcm, dca (DTS), vorbis, resample, scale, and stretchpitch. Each module includes
baseline and SIMD variants selected automatically by libmedia. Unreachable atomic variants
have been removed.

Removed from the upstream subset: av1, vp9 and vp8 decoders (9 wasm files,
about 6 MB). With MSE preferred, Chromium natively decodes these codecs, so the
wasm fallback for them is unreachable in this deployment.

Pruned demux chunks (deleted, not present on disk): 239 (RTSP demux, 47KB),
261 (DASH io-loader, 26KB), 641 (IVF demux, 3KB), 794/462/199 (raw
h264/h265/vvc demux, 21KB total). These paths are unreachable here: the backend only serves http(s) (no rtsp://, no MPD), first-byte probing
never selects these formats, and Emby does not report IVF or raw-stream
containers. The `.e(239)`-style dynamic chunk references inside avplayer.js are
left untouched: webpack loads chunks on demand and the untriggered paths never
request the deleted files.

License: LGPL-3.0, see COPYING.LGPLv3 and the pinned upstream source. Decoders
are fetched on demand; the entire WASM subset is not downloaded at startup.

The player adapter prefers MSE whenever libmedia's MIME support check passes,
with WebCodecs hardware acceleration enabled otherwise. Its high-resolution
software-decode safeguard depends on
the pinned 1.3.1 decoder pipeline's `getTasksInfo()` API; verify this integration
when upgrading the library.
