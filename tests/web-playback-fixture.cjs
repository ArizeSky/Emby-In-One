'use strict';
/* Strict playback-response fixtures for the /web session suites.
 *
 * The production session only accepts the strict playback protocol
 * (public/web/playback-identity.js): every PlaybackInfo candidate carries its
 * own EioAttemptId / EioPlaybackToken / EioPlaySessionId, every media,
 * fallback, transcode and subtitle URL is already scoped to that attempt and
 * carries the api_key capability, and session events use the scoped route.
 * This helper emulates that backend response shape for tests so the fixtures
 * exercise the real strict path instead of a legacy reply the client rejects.
 *
 * Each call to playbackInfo() starts a new negotiation and therefore issues
 * fresh attempt ids (including retries), matching the production reservation
 * of one attempt per candidate. The source's own values (Id, Container,
 * MediaStreams, EioDirectStreamUnavailable, ...) are kept as-is so existing
 * assertions still read the fixture data, not a rewritten shape.
 */

const PROTOCOL_PREFIX = '/eio/playback/v1';
const ATTEMPT_FIELD = 'EioAttemptId';
const TOKEN_FIELD = 'EioPlaybackToken';
const SESSION_FIELD = 'EioPlaySessionId';
const BOUND_URL_FIELDS = ['DirectStreamUrl', 'EioFallbackUrl', 'TranscodingUrl'];
const SESSION_EVENT_PATH = /^\/eio\/playback\/v1\/[^/]+\/Sessions\/Playing(?:\/(?:Progress|Stopped))?$/;

let serial = 0;

function nextAttempt(playSessionId) {
  serial += 1;
  const attemptId = 'fixture-attempt-' + serial;
  return {
    attemptId,
    // Only URL-safe characters, so the capability survives the query verbatim.
    token: 'eio_play_v1_' + attemptId.replace(/[^A-Za-z0-9]/g, '') + 'a'.repeat(48),
    playSessionId,
  };
}

// Prefixes one server-issued URL onto the attempt and stamps the capability,
// mirroring the backend's strictPlaybackURL. An already scoped fixture URL is
// left untouched so a helper call cannot double-prefix it.
function bindUrl(value, attempt) {
  if (typeof value !== 'string' || !value) return value;
  if (value.startsWith(PROTOCOL_PREFIX + '/')) return value;
  const resolved = new URL(value, 'http://fixture.local');
  resolved.searchParams.delete('api_key');
  resolved.searchParams.set('api_key', attempt.token);
  return PROTOCOL_PREFIX + '/' + attempt.attemptId + resolved.pathname + resolved.search;
}

// Copies one candidate: keeps every original field (including the unavailable
// marker) and rewrites only the attempt triple and the strict resource URLs.
function bindSource(source, attempt) {
  if (!source || typeof source !== 'object') return source;
  const bound = { ...source };
  for (const field of BOUND_URL_FIELDS) {
    if (typeof source[field] === 'string' && source[field]) {
      bound[field] = bindUrl(source[field], attempt);
    }
  }
  if (Array.isArray(source.MediaStreams)) {
    bound.MediaStreams = source.MediaStreams.map(stream => {
      if (!stream || typeof stream !== 'object') return stream;
      if (typeof stream.DeliveryUrl !== 'string' || !stream.DeliveryUrl) return stream;
      return { ...stream, DeliveryUrl: bindUrl(stream.DeliveryUrl, attempt) };
    });
  }
  bound[ATTEMPT_FIELD] = attempt.attemptId;
  bound[TOKEN_FIELD] = attempt.token;
  bound[SESSION_FIELD] = attempt.playSessionId;
  return bound;
}

// Emulates one strict PlaybackInfo response: pass the source data (or an array
// of candidates) and the upstream top-level session. Every candidate gets its
// own fresh attempt, so a retry negotiation is a different attempt too.
function playbackInfo(sourceData, session = 'sess') {
  const list = Array.isArray(sourceData) ? sourceData : [sourceData];
  return {
    MediaSources: list.map(source => bindSource(source, nextAttempt(session))),
    PlaySessionId: session,
  };
}

function isSessionEvent(pathname) {
  return SESSION_EVENT_PATH.test(String(pathname || ''));
}

// True when the engine URL is already scoped to one strict attempt and carries
// the capability, i.e. the client never rebuilt a legacy /Videos path.
function isStrictUrl(value, pathSuffix) {
  if (typeof value !== 'string') return false;
  const resolved = new URL(value, 'http://fixture.local');
  const prefix = PROTOCOL_PREFIX + '/';
  if (!resolved.pathname.startsWith(prefix)) return false;
  const rest = resolved.pathname.slice(prefix.length);
  const slash = rest.indexOf('/');
  if (slash <= 0) return false; // no attempt segment
  if (pathSuffix && rest.slice(slash) !== pathSuffix) return false;
  return resolved.searchParams.has('api_key');
}

module.exports = {
  PROTOCOL_PREFIX,
  ATTEMPT_FIELD,
  TOKEN_FIELD,
  SESSION_FIELD,
  playbackInfo,
  bindSource,
  bindUrl,
  isSessionEvent,
  isStrictUrl,
};
