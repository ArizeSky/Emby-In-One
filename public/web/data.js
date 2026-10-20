/* Shared data operations for the Web client. */
(function () {
  'use strict';
  const items = payload => Array.isArray(payload) ? payload : (payload?.Items || []);
  function query(path, params) {
    const url = new URL(path, location.origin);
    for (const [key, value] of Object.entries(params)) {
      if (value != null && value !== '') url.searchParams.set(key, value);
    }
    return url.pathname + url.search;
  }
  async function all(api, path, params = {}, options = {}) {
    const results = [], seen = new Set();
    let offset = 0;
    while (true) {
      const payload = await api(query(path, { ...params, Limit: 200, StartIndex: offset }), options);
      const batch = items(payload);
      if (!batch.length) {
        if (offset < (payload.TotalRecordCount ?? 0)) throw new Error('列表分页结果不完整，请重试');
        break;
      }
      let added = 0;
      for (const item of batch) {
        if (seen.has(item.Id)) continue;
        seen.add(item.Id); results.push(item); added++;
      }
      offset += batch.length;
      if (offset >= (payload.TotalRecordCount ?? Infinity) || batch.length < 200 && payload.TotalRecordCount == null) break;
      if (!added) throw new Error('列表分页未推进，请重试');
    }
    return results;
  }
  function episodeOrder(a, b) {
    return (a.ParentIndexNumber ?? 0) - (b.ParentIndexNumber ?? 0)
      || (a.IndexNumber ?? 0) - (b.IndexNumber ?? 0) || String(a.Id).localeCompare(String(b.Id));
  }
  function defaultAudio(source) {
    return source?.DefaultAudioStreamIndex ?? source?.MediaStreams?.find(s => s.Type === 'Audio' && s.IsDefault)?.Index
      ?? source?.MediaStreams?.find(s => s.Type === 'Audio')?.Index ?? -1;
  }
  function defaultSubtitle(source) {
    return source?.DefaultSubtitleStreamIndex ?? source?.MediaStreams?.find(s => s.Type === 'Subtitle' && s.IsDefault)?.Index ?? -1;
  }
  function deviceProfile() {
    return { MaxStreamingBitrate: 120000000, MaxStaticBitrate: 120000000,
      DirectPlayProfiles: [{ Container: 'mp4,m4v', Type: 'Video', VideoCodec: 'h264,avc', AudioCodec: 'aac,mp3' }],
      TranscodingProfiles: [
        { Container: 'mp4', Type: 'Video', VideoCodec: 'h264', AudioCodec: 'aac', Protocol: 'hls' },
        { Container: 'ts', Type: 'Video', VideoCodec: 'h264', AudioCodec: 'aac', Protocol: 'hls' } ],
      CodecProfiles: [], SubtitleProfiles: [{ Format: 'vtt', Method: 'External' }, { Format: 'ass', Method: 'External' }] };
  }
  window.EIOData = { items, query, all, episodeOrder, defaultAudio, defaultSubtitle, deviceProfile };
})();
