/* Emby-In-One Web player — strict playback identity protocol (client side).
 *
 * Owns the frozen wire contract of the strict playback attempt and nothing
 * else: the negotiation path, the per-candidate attempt fields, the strict
 * resource URL rules, the session-event paths/payloads and the error shape.
 * It holds no player state, performs no navigation and never reconstructs a
 * legacy path.
 *
 * Frozen protocol (parent-owned):
 *  - negotiation: POST /eio/playback/v1/Items/{itemId}/PlaybackInfo through the
 *    existing local api() auth (X-Emby-Token header + JSON body);
 *  - every MediaSource of a strict PlaybackInfo response carries its own
 *    EioAttemptId / EioPlaybackToken / EioPlaySessionId. The candidate's own
 *    EioPlaySessionId is authoritative; the top-level info.PlaySessionId is
 *    not used for the selected source;
 *  - media / subtitle / fallback / transcode URLs are returned already
 *    prefixed with /eio/playback/v1/{attemptId}/ and carry the api_key
 *    capability. The local metadata token is never appended to them and a
 *    missing or foreign URL is a protocol failure, never a rebuilt legacy
 *    path;
 *  - Playing / Progress / Stopped go to
 *    /eio/playback/v1/{attemptId}/Sessions/Playing[...]?api_key={capability}
 *    with the attempt echoed in the payload (EioAttemptId) so the server can
 *    reject a conflicting attempt. The capability is the only credential of a
 *    strict event: no local metadata header is added next to it (the strict
 *    ingress resolves the capability to its parent session itself, and a
 *    header would be the more prominent credential);
 *  - identity failures are {kind:'playback-*', message, retryable}.
 */
(function () {
  'use strict';

  const PROTOCOL_PREFIX = '/eio/playback/v1';
  const ATTEMPT_FIELD = 'EioAttemptId';
  const TOKEN_FIELD = 'EioPlaybackToken';
  const SESSION_FIELD = 'EioPlaySessionId';
  const EVENTS = ['Playing', 'Progress', 'Stopped'];
  const MAX_ERROR_BODY = 4096;

  // Every identity failure is one recognisable shape: name + kind + retryable,
  // so the session can tell it apart from an ordinary transport failure and
  // never swallow it in an empty catch.
  function createError(kind, message, retryable) {
    const error = new Error(message || '播放身份协议失败');
    error.name = 'PlaybackProtocolError';
    error.kind = String(kind || 'playback-failed');
    error.retryable = retryable === true;
    return error;
  }

  function isPlaybackProtocolError(error) {
    if (!error) return false;
    if (error.name === 'PlaybackProtocolError') return true;
    return typeof error.kind === 'string' && error.kind.startsWith('playback-');
  }

  // An attempt id becomes one URL path segment. Anything that would change the
  // segment shape (slash, dot segment, control character) is rejected instead
  // of being escaped into a longer path.
  function isAttemptId(value) {
    return typeof value === 'string' && value.length > 0 && value.length <= 200
      && /^[A-Za-z0-9._~-]+$/.test(value) && value !== '.' && value !== '..';
  }

  // The capability travels in the query string only. It must not need escaping
  // in a way that could hide a second parameter.
  function isCapability(value) {
    return typeof value === 'string' && value.length > 0 && value.length <= 2048
      && !/[\s\u0000-\u001f\u007f]/.test(value) && !/[&#?/\\%]/.test(value);
  }

  // The candidate's own upstream session is echoed in the JSON payload, so it
  // only has to be a JSON-safe (empty when upstream omitted its alias) string.
  function isSessionId(value) {
    return typeof value === 'string' && value.length <= 512
      && !/[\u0000-\u001f\u007f]/.test(value);
  }

  function currentOrigin() {
    return typeof location !== 'undefined' && location && typeof location.origin === 'string'
      ? location.origin : null;
  }

  function localMetadataToken() {
    try {
      return typeof localStorage !== 'undefined' && localStorage ? localStorage.getItem('eio_token') : null;
    } catch (error) { return null; }
  }

  function negotiationPath(itemId) {
    if (typeof itemId !== 'string' || !itemId) {
      throw createError('playback-info-invalid', '播放条目标识无效', false);
    }
    return `${PROTOCOL_PREFIX}/Items/${encodeURIComponent(itemId)}/PlaybackInfo`;
  }

  // Reads the attempt of one PlaybackInfo candidate. Strict: a response
  // without the attempt triple is a failure, not a legacy candidate.
  function attemptFromMediaSource(source, context = {}) {
    if (!source || typeof source !== 'object') {
      return { ok: false, error: createError('playback-attempt-missing', '播放信息服务未返回媒体源', false) };
    }
    const attemptId = source[ATTEMPT_FIELD];
    const token = source[TOKEN_FIELD];
    const playSessionId = source[SESSION_FIELD];
    if (attemptId == null && token == null && playSessionId == null) {
      return { ok: false, error: createError('playback-attempt-missing',
        '播放信息服务未返回严格播放尝试（EioAttemptId），已拒绝旧版播放路径', false) };
    }
    if (!isAttemptId(attemptId)) {
      return { ok: false, error: createError('playback-attempt-invalid', '播放尝试标识无效', false) };
    }
    if (!isCapability(token)) {
      return { ok: false, error: createError('playback-attempt-invalid', '播放能力凭据缺失或无效', false) };
    }
    if (!isSessionId(playSessionId)) {
      // Without the candidate's own session no strict event can be matched to
      // this attempt; the server answers playback-session-unavailable, so the
      // attempt has to be renegotiated rather than played through a shared one.
      return { ok: false, error: createError('playback-session-missing',
        '播放尝试缺少候选会话（EioPlaySessionId），请重新协商', true) };
    }
    return {
      ok: true,
      attempt: {
        attemptId,
        token,
        playSessionId,
        itemId: context.itemId ?? null,
        mediaSourceId: context.mediaSourceId ?? (typeof source.Id === 'string' ? source.Id : null),
      },
    };
  }

  // Resolves one server-issued resource URL of this attempt. The returned value
  // is a same-origin path+query for the engine. It keeps an api_key the server
  // already put there, adds the attempt capability only when the query carries
  // none, and rejects a URL that would send the local metadata token.
  function strictResourceUrl(attempt, value, options = {}) {
    if (!attempt || !isAttemptId(attempt.attemptId) || !isCapability(attempt.token)) {
      throw createError('playback-attempt-missing', '缺少有效的播放尝试，无法解析严格资源地址', false);
    }
    if (typeof value !== 'string' || !value) {
      throw createError('playback-url-missing', '播放服务未返回严格资源地址，已拒绝回退旧版路径', false);
    }
    const origin = options.origin !== undefined ? options.origin : currentOrigin();
    let url;
    try {
      url = new URL(value, origin || undefined);
    } catch (error) {
      throw createError('playback-url-not-strict', '播放资源地址无效', false);
    }
    if (origin && url.origin !== origin) {
      throw createError('playback-url-not-strict', '严格播放资源地址必须同源', false);
    }
    const prefix = `${PROTOCOL_PREFIX}/${attempt.attemptId}/`;
    if (!url.pathname.startsWith(prefix)) {
      throw createError('playback-url-attempt-mismatch', '播放资源地址不属于当前播放尝试', false);
    }
    const localToken = options.localToken !== undefined ? options.localToken : localMetadataToken();
    const current = url.searchParams.get('api_key');
    if (current && localToken && current === localToken) {
      throw createError('playback-resource-credential', '严格播放资源地址携带了本地元数据凭据', false);
    }
    if (current && current !== attempt.token) {
      throw createError('playback-resource-credential', '播放资源凭据不属于当前尝试', false);
    }
    if (!current) url.searchParams.set('api_key', attempt.token);
    return url.pathname + url.search;
  }

  // Normalizes the subtitle DeliveryUrls of one candidate onto the same attempt
  // before the engine sees the source. Never mutates the response in place.
  function strictMediaSource(attempt, source, options = {}) {
    if (!source || typeof source !== 'object') return source;
    const streams = Array.isArray(source.MediaStreams) ? source.MediaStreams : null;
    if (!streams || !streams.length) return source;
    let changed = false;
    const next = streams.map(stream => {
      if (!stream || stream.Type !== 'Subtitle' || !stream.DeliveryUrl) return stream;
      const deliveryUrl = strictResourceUrl(attempt, stream.DeliveryUrl, options);
      if (deliveryUrl === stream.DeliveryUrl) return stream;
      changed = true;
      return { ...stream, DeliveryUrl: deliveryUrl };
    });
    return changed ? { ...source, MediaStreams: next } : source;
  }

  function sessionEventPath(attempt, event) {
    if (!attempt || !isAttemptId(attempt.attemptId) || !isCapability(attempt.token)) {
      throw createError('playback-attempt-missing', '缺少有效的播放尝试，无法上报播放事件', false);
    }
    if (!EVENTS.includes(event)) {
      throw createError('playback-event-invalid', '未知的播放事件', false);
    }
    const suffix = event === 'Playing' ? '' : '/' + event;
    return `${PROTOCOL_PREFIX}/${attempt.attemptId}/Sessions/Playing${suffix}`
      + `?api_key=${encodeURIComponent(attempt.token)}`;
  }

  async function readErrorDetail(response) {
    if (!response || typeof response.text !== 'function') return null;
    try {
      const text = await response.text();
      if (!text || text.length > MAX_ERROR_BODY) return null;
      const parsed = JSON.parse(text);
      return parsed && typeof parsed === 'object' ? parsed : null;
    } catch (error) {
      return null;
    }
  }

  function deriveRetryable(status, detail) {
    if (detail && typeof detail.retryable === 'boolean') return detail.retryable;
    return Number(status) >= 500 || Number(status) === 429;
  }

  // Posts one session event. The attempt is stamped here, so a caller cannot
  // forget it or send another attempt's id. An explicit playback-* kind in the
  // rejection body becomes a protocol error; every other response/transport
  // failure stays an ordinary transient reporting failure.
  async function postSessionEvent(options = {}) {
    const { attempt, event, payload, signal } = options;
    const path = sessionEventPath(attempt, event);
    // The capability in the query is the only credential of a strict event. The
    // local metadata token is deliberately not attached as a header: strict
    // requests are authenticated by the capability alone, and the server keeps
    // the parent session it negotiated the attempt under.
    const init = {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(Object.assign({}, payload, {
        PlaySessionId: attempt.playSessionId,
        [ATTEMPT_FIELD]: attempt.attemptId,
      })),
    };
    return sendStrictRequest(path, init, options);
  }

  function deleteActiveEncodings(options = {}) {
    const path = sessionEventPath(options.attempt, 'Stopped')
      .replace('/Sessions/Playing/Stopped?', '/Videos/ActiveEncodings?');
    return sendStrictRequest(path, {method:'DELETE'}, options);
  }

  async function sendStrictRequest(path, init, options) {
    const signal = options.signal;
    const fetchImpl = options.fetch || (typeof fetch === 'function' ? fetch : null);
    if (!fetchImpl) throw createError('playback-event-unsupported', '当前环境无法上报播放事件', false);
    if (signal) init.signal = signal;
    if (options.keepalive === true) init.keepalive = true;
    let response;
    try {
      response = await fetchImpl(path, init);
    } catch (error) {
      if (error && error.name === 'AbortError') throw error;
      // Transport failure of a reporting request: keep the previous
      // best-effort semantics. The caller never sees a false success.
      return { ok: false, transient: true, error };
    }
    if (response && response.ok) return { ok: true, status: response.status };
    const status = response ? response.status : 0;
    const detail = await readErrorDetail(response);
    if (detail && typeof detail.kind === 'string' && detail.kind.startsWith('playback-')) {
      const message = typeof detail.message === 'string' && detail.message
        ? detail.message : '播放身份协议拒绝了本次播放事件';
      throw createError(detail.kind, message, deriveRetryable(status, detail));
    }
    return { ok: false, transient: true, status };
  }

  window.EIOPlaybackIdentity = {
    PROTOCOL_PREFIX,
    ATTEMPT_FIELD,
    TOKEN_FIELD,
    SESSION_FIELD,
    EVENTS,
    createError,
    isPlaybackProtocolError,
    isAttemptId,
    isCapability,
    negotiationPath,
    attemptFromMediaSource,
    strictResourceUrl,
    strictMediaSource,
    sessionEventPath,
    postSessionEvent,
    deleteActiveEncodings,
  };
})();
