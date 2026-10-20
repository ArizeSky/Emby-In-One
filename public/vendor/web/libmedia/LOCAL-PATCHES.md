# Local patches to libmedia 1.3.1

`26.avplayer.js`, MSEPipeline.afterSeek: fill audio and video independently until
both streams have buffered the requested seek position. The upstream loop pulled
one packet from each stream per iteration and stopped when either stream reached
the target. With AAC packets shorter than video frames, audio lagged behind;
seeking was then clamped to the end of the shorter audio buffer. Paused seeks and
version changes consequently returned to an earlier position.

The patch preserves each stream's next packet and existing EOF handling. Single
stream media still fills its only stream. `avplayer.js` adds `v=rc2-seek3` to chunk
URLs in the main and generated worker loaders to invalidate cached chunks.
No codec or WASM resource is changed.

Verified in Chromium with an eight-second H.264 MP4 containing two AAC tracks:
pause, seek to 3.25 seconds, preserve that position when changing versions, and
switch audio while paused. Keep this patch when replacing the vendored bundle,
or verify that a newer upstream implementation already fixes it.

`26.avplayer.js`, OMovFormat.updateCurrentFragment: when a single-track fragment
receives a next DTS that is earlier than or equal to the pending sample DTS,
finalize that sample using the existing codec cadence fallback. Do not subtract
the two timestamps: restarted Emby HLS jobs can move audio backwards, and a
negative duration becomes a huge unsigned trun/tfhd duration. Chromium then
reports about 97,391 extra seconds of AAC audio and playback stalls despite a
valid video buffer. Forward DTS still uses the real delta; multi-track muxing
keeps its original behavior. The patch does not change packet timestamps.

`tests/web-mse-duration.test.cjs` executes the actual vendored finalizer with
observed backward DTS, duplicate DTS, audio/video cadence, and forward deltas.
Keep both local patches when updating libmedia.
