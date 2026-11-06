'use strict';
/* Focused tests for the strict playback identity helper (public/web/playback-identity.js).
 *
 * Covers the frozen wire contract the playback session relies on: the
 * negotiation path, the per-candidate attempt triple, the strict resource URL
 * rules (attempt prefix, capability preserved, local metadata token rejected,
 * legacy URL refused) and the session-event path/payload/error classification.
 * The DOM/Vue player lifecycle is covered by the existing session suites.
 */
const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const SOURCE = path.join(__dirname, '..', 'public', 'web', 'playback-identity.js');

function load() {
  const storage = new Map([['eio_token', 'local-token']]);
  const context = {
    URL, console, Math, JSON, Promise, setTimeout, clearTimeout,
    location: { origin: 'http://localhost' },
    localStorage: { getItem: key => (storage.has(key) ? storage.get(key) : null) },
  };
  context.window = context;
  vm.createContext(context);
  vm.runInContext(fs.readFileSync(SOURCE, 'utf8'), context);
  return context.EIOPlaybackIdentity;
}

const source = extra => ({
  Id: 'ms1',
  EioAttemptId: 'b1a2c3',
  EioPlaybackToken: 'eio_play_v1_' + 'a'.repeat(48),
  EioPlaySessionId: 'candidate-session',
  ...extra,
});

test('negotiation path is the frozen strict entry', () => {
  const identity = load();
  assert.equal(identity.negotiationPath('item-1'),
    '/eio/playback/v1/Items/item-1/PlaybackInfo');
  assert.throws(() => identity.negotiationPath(''), error => error.kind === 'playback-info-invalid');
});

test('attempt triple is required and per candidate', () => {
  const identity = load();
  const legacy = identity.attemptFromMediaSource({ Id: 'ms1', DirectStreamUrl: '/Videos/x' });
  assert.equal(legacy.ok, false);
  assert.equal(legacy.error.kind, 'playback-attempt-missing');
  assert.equal(identity.isPlaybackProtocolError(legacy.error), true);

  assert.equal(identity.attemptFromMediaSource(source({ EioPlaybackToken: '' })).error.kind,
    'playback-attempt-invalid');
  const noSession = identity.attemptFromMediaSource(source({ EioPlaySessionId: '' }));
  assert.equal(noSession.ok, true, 'upstream may omit its alias; opaque attempt still scopes the playback');
  assert.equal(noSession.attempt.playSessionId, '');
  const missingSession = identity.attemptFromMediaSource(source({ EioPlaySessionId: undefined }));
  assert.equal(missingSession.ok, false);
  assert.equal(missingSession.error.kind, 'playback-session-missing');

  const resolved = identity.attemptFromMediaSource(source(), { itemId: 'item-1' });
  assert.equal(resolved.ok, true);
  assert.equal(resolved.attempt.attemptId, 'b1a2c3');
  assert.equal(resolved.attempt.playSessionId, 'candidate-session');
  assert.equal(resolved.attempt.mediaSourceId, 'ms1');
});

test('strict resource URLs stay on the attempt and keep the capability', () => {
  const identity = load();
  const attempt = identity.attemptFromMediaSource(source()).attempt;

  // Already bound by the negotiation: unchanged, capability kept.
  const bound = '/eio/playback/v1/b1a2c3/Videos/item-1/stream.mp4?api_key=' + encodeURIComponent(attempt.token);
  assert.equal(identity.strictResourceUrl(attempt, bound), bound);
  assert.throws(() => identity.strictResourceUrl(attempt,
    '/eio/playback/v1/b1a2c3/Videos/item-1/stream.mp4?api_key=other-capability'),
  error => error.kind === 'playback-resource-credential');

  // Bound but without a credential: the attempt capability is added.
  assert.equal(
    identity.strictResourceUrl(attempt, '/eio/playback/v1/b1a2c3/Videos/item-1/master.m3u8'),
    '/eio/playback/v1/b1a2c3/Videos/item-1/master.m3u8?api_key=' + encodeURIComponent(attempt.token));

  // Legacy / foreign URLs are refused instead of being rebuilt.
  assert.throws(() => identity.strictResourceUrl(attempt, '/Videos/item-1/stream.mp4'),
    error => error.kind === 'playback-url-attempt-mismatch');
  assert.throws(() => identity.strictResourceUrl(attempt, '/eio/playback/v1/other/Videos/x'),
    error => error.kind === 'playback-url-attempt-mismatch');
  // The local metadata token is never an acceptable credential here.
  assert.throws(() => identity.strictResourceUrl(attempt,
    '/eio/playback/v1/b1a2c3/Videos/item-1/stream.mp4?api_key=local-token'),
  error => error.kind === 'playback-resource-credential');
  assert.throws(() => identity.strictResourceUrl(attempt, 'https://other.invalid/eio/playback/v1/b1a2c3/x'),
    error => error.kind === 'playback-url-not-strict');
});

test('subtitle DeliveryUrls are normalized without mutating the response', () => {
  const identity = load();
  const attempt = identity.attemptFromMediaSource(source()).attempt;
  const original = source({
    MediaStreams: [
      { Type: 'Audio', Index: 1 },
      { Type: 'Subtitle', Index: 2,
        DeliveryUrl: '/eio/playback/v1/b1a2c3/Videos/item-1/Subtitles/2/Stream.vtt' },
    ],
  });
  const normalized = identity.strictMediaSource(attempt, original);
  assert.equal(normalized.MediaStreams[1].DeliveryUrl,
    '/eio/playback/v1/b1a2c3/Videos/item-1/Subtitles/2/Stream.vtt?api_key=' + encodeURIComponent(attempt.token));
  assert.equal(original.MediaStreams[1].DeliveryUrl,
    '/eio/playback/v1/b1a2c3/Videos/item-1/Subtitles/2/Stream.vtt', 'the response is not mutated');
  assert.throws(() => identity.strictMediaSource(attempt, source({
    MediaStreams: [{ Type: 'Subtitle', Index: 2, DeliveryUrl: '/Videos/item-1/Subtitles/2/Stream.vtt' }],
  })), error => error.kind === 'playback-url-attempt-mismatch');
});

test('session events carry the capability, the attempt and the candidate session', async () => {
  const identity = load();
  const attempt = identity.attemptFromMediaSource(source()).attempt;
  assert.equal(
    identity.sessionEventPath(attempt, 'Progress'),
    '/eio/playback/v1/b1a2c3/Sessions/Playing/Progress?api_key=' + encodeURIComponent(attempt.token));

  const calls = [];
  const fetchImpl = async (url, init) => {
    calls.push({ url, init });
    return { ok: true, status: 204 };
  };
  const ok = await identity.postSessionEvent({
    attempt, event: 'Stopped', payload: { ItemId: 'item-1', PositionTicks: 42 }, fetch: fetchImpl,
  });
  assert.equal(ok.ok, true);
  const call = calls[0];
  assert.equal(call.url, '/eio/playback/v1/b1a2c3/Sessions/Playing/Stopped?api_key='
    + encodeURIComponent(attempt.token));
  const body = JSON.parse(call.init.body);
  assert.equal(body.EioAttemptId, 'b1a2c3');
  assert.equal(body.PlaySessionId, 'candidate-session');
  assert.equal(body.ItemId, 'item-1');
  assert.equal(body.PositionTicks, 42);
  assert.equal((call.init.headers || {})['X-Emby-Token'], undefined,
    'the local metadata token is not sent next to the capability');
});

test('only an explicit playback-* rejection is a protocol failure', async () => {
  const identity = load();
  const attempt = identity.attemptFromMediaSource(source()).attempt;

  const rejected = await identity.postSessionEvent({
    attempt, event: 'Progress', payload: {},
    fetch: async () => ({
      ok: false, status: 409,
      text: async () => JSON.stringify({ kind: 'playback-attempt-mismatch', message: 'other attempt' }),
    }),
  }).then(() => null, error => error);
  assert.equal(rejected.kind, 'playback-attempt-mismatch');
  assert.equal(rejected.retryable, false);

  const retryable = await identity.postSessionEvent({
    attempt, event: 'Playing', payload: {},
    fetch: async () => ({
      ok: false, status: 504,
      text: async () => JSON.stringify({ kind: 'playback-capacity', retryable: true }),
    }),
  }).then(() => null, error => error);
  assert.equal(retryable.kind, 'playback-capacity');
  assert.equal(retryable.retryable, true);

  // Ordinary transient reporting failure: no throw, no false ACK.
  const transient = await identity.postSessionEvent({
    attempt, event: 'Progress', payload: {},
    fetch: async () => ({ ok: false, status: 500, text: async () => 'upstream failed' }),
  });
  assert.equal(transient.ok, false);
  assert.equal(transient.transient, true);

  const offline = await identity.postSessionEvent({
    attempt, event: 'Progress', payload: {},
    fetch: async () => { throw new TypeError('network'); },
  });
  assert.equal(offline.transient, true);
});
