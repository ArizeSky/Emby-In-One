/* Shared data operations for the Web client. */
(function () {
  'use strict';
  const items = payload => Array.isArray(payload) ? payload : (payload?.Items || []);

  // Thrown by all() when a server marked the merged candidate set partial at
  // the current lower bound. `items` carries the usable prefix so callers can
  // render what exists instead of blanking the list; `lowerBound` is the last
  // TotalRecordCount, which is only a lower bound while partial.
  class PartialResultsError extends Error {
    constructor(items, lowerBound) {
      const count = Array.isArray(items) ? items.length : 0;
      const bound = lowerBound != null ? `，至少 ${lowerBound} 项` : '';
      super(`部分结果：已获取 ${count} 项${bound}，列表可能不完整，请稍后重试`);
      this.name = 'PartialResultsError';
      this.items = Array.isArray(items) ? items.slice() : [];
      this.lowerBound = lowerBound ?? null;
      this.partial = true;
    }
  }

  const ALL_PAGE_LIMIT = 200;
  const ALL_MAX_PAGES = 500;
  const ALL_MAX_RESULTS = 100000;

  function query(path, params) {
    const url = new URL(path, location.origin);
    for (const [key, value] of Object.entries(params)) {
      if (value != null && value !== '') url.searchParams.set(key, value);
    }
    return url.pathname + url.search;
  }
  function positiveInt(value, name) {
    if (!Number.isInteger(value) || value < 1) {
      throw new RangeError(`选项 ${name} 必须是有限正整数`);
    }
    return value;
  }
  async function all(api, path, params = {}, options = {}) {
    if (options == null || typeof options !== 'object') {
      throw new TypeError('options 必须是对象');
    }
    const { maxPages = ALL_MAX_PAGES, maxResults = ALL_MAX_RESULTS, ...fetchOptions } = options;
    positiveInt(maxPages, 'maxPages');
    positiveInt(maxResults, 'maxResults');
    const results = [], seen = new Set();
    let offset = 0;
    let partial = false;
    let lowerBound = null;
    let pages = 0;
    const partialError = () => new PartialResultsError(results, lowerBound);

    while (true) {
      // The budget is only exhausted while the listing is known to continue,
      // so the collected items are a genuine lower bound: keep them.
      if (results.length >= maxResults) throw partialError();
      if (++pages > maxPages) {
        if (partial) throw partialError();
        throw new Error('列表分页超过安全上限，请重试');
      }
      const payload = await api(query(path, { ...params, Limit: ALL_PAGE_LIMIT, StartIndex: offset }), fetchOptions);
      const incomplete = payload?.EioIncompleteResults === true;
      if (incomplete) partial = true;
      if (payload?.TotalRecordCount != null) lowerBound = payload.TotalRecordCount;

      const batch = items(payload);
      if (!batch.length) {
        if (offset < (payload?.TotalRecordCount ?? 0)) {
          // A short/empty page before the reported lower bound is either an
          // upstream gap or a partial snapshot; always bounded, never silent.
          // A partial prefix already collected stays usable.
          if (incomplete || partial) throw partialError();
          throw new Error('列表分页结果不完整，请重试');
        }
        // An explicitly complete page confirms the listing; a trailing
        // partial response never claims it is complete.
        if (!incomplete) partial = false;
        break;
      }

      let added = 0, truncated = false;
      for (const item of batch) {
        if (seen.has(item.Id)) continue;
        // Check the budget before every push so a page can never overshoot it.
        if (results.length >= maxResults) { truncated = true; break; }
        seen.add(item.Id); results.push(item); added++;
      }
      // Uncollected items remain, so the prefix is only a lower bound.
      if (truncated) throw partialError();
      if (!added) {
        // A repeated page (or a snapshot shift that only yields duplicates)
        // must not spin. Keep the usable prefix for partial callers.
        if (incomplete || partial) throw partialError();
        throw new Error('列表分页未推进，请重试');
      }
      offset += batch.length;
      const total = payload?.TotalRecordCount;
      const atEnd = total != null ? offset >= total : batch.length < ALL_PAGE_LIMIT;
      if (atEnd) {
        if (!incomplete) partial = false;
        break;
      }
    }
    if (partial) throw partialError();
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
  window.EIOData = { items, query, all, PartialResultsError, episodeOrder, defaultAudio, defaultSubtitle, deviceProfile };
})();
