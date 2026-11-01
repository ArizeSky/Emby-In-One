package backend

import (
	"sort"
	"sync"
	"time"
)

// Bounded in-memory session tracker for upstream viewing statistics.
//
// It answers one question only: for one upstream play session, how much real
// playback time has been observed through confirmed progress reports, and when
// was the last valid observation? It never decides whether an event may be
// counted (the handlers already checked authorization, the playback limiter
// verdict and the session-event tracker) and it never talks to the database.
//
// Design boundaries (execution contract §4):
//   - the first accepted sample with a position only establishes a baseline;
//   - credit is bounded by the monotonic elapsed time between samples, so a
//     position jump or a fast-forward can never invent viewing time;
//   - an interval longer than upstreamActivityMaxSampleGap is not compensated;
//   - pause, rewind, seek and missing positions reset the baseline instead of
//     accumulating;
//   - samples are ordered by the local receive sequence assigned before the
//     upstream request, so a slow concurrent request completing later cannot
//     turn an older position into a newer observation;
//   - the threshold is captured when the session is first tracked, so a later
//     policy change never retroactively qualifies an existing session;
//   - at most upstreamActivityMaxSessions sessions, TTL eviction is lazy, and no
//     goroutine is started.
const (
	upstreamActivityMaxSessions = 1024
	upstreamActivitySessionTTL  = 10 * time.Minute
	// upstreamActivityMaxSampleGap is the longest interval between two samples
	// that may still be credited. A longer gap is a hole in the evidence, not
	// watched time.
	upstreamActivityMaxSampleGap = 90 * time.Second
	// upstreamActivitySeekFactor/upstreamActivitySeekSlack define the bounded
	// tolerance for distinguishing a seek from normal playback: a position
	// advance above elapsed*factor+slack is a jump, not viewing.
	upstreamActivitySeekFactor = 4
	upstreamActivitySeekSlack  = 5 * time.Second
	// upstreamActivityQualifiedRefresh is how often a qualified session may
	// extend its stored last-qualified time beyond the first qualification.
	upstreamActivityQualifiedRefresh = 60 * time.Second
	// upstreamActivityPersistInterval is the minimum interval between two
	// summary writes for one server, so a heartbeat never writes on every
	// event. A Stopped event always flushes.
	upstreamActivityPersistInterval = 60 * time.Second
	// Emby reports positions in 100ns ticks.
	upstreamActivityTicksPerSecond int64 = 10_000_000
)

// upstreamActivitySessionKey identifies one play session on one upstream
// account for one local user. PlaySessionID is the translated (upstream)
// playback-scoped identifier: SessionId alone is device-scoped and must not
// merge two separate plays. AccountScope is part of the key so a new account
// that happens to reuse a PlaySessionId can never inherit the previous
// account's accumulation.
type upstreamActivitySessionKey struct {
	UserID        string
	ServerID      string
	AccountScope  string
	PlaySessionID string
}

// upstreamActivityFlushKey scopes the last-activity write throttle to one
// account, so an old account's throttle window cannot suppress the first write
// of a freshly configured account.
type upstreamActivityFlushKey struct {
	ServerID     string
	AccountScope string
}

// upstreamActivitySampleToken is created when a session event is received,
// before the upstream request is sent. The sequence orders events by local
// receipt, not by network completion.
type upstreamActivitySampleToken struct {
	Key        upstreamActivitySessionKey
	Seq        uint64
	ReceivedAt time.Time
}

// upstreamActivitySample is one confirmed (2xx) session event offered to the
// tracker.
type upstreamActivitySample struct {
	Token         upstreamActivitySampleToken
	Kind          playbackEventKind
	PositionTicks *int64
	Paused        bool
}

// upstreamActivityObservation tells the caller what became due for persistence.
// Nil timestamps mean "nothing to write". Remove is set for an accepted Stop and
// is applied after the caller attempted the flush.
type upstreamActivityObservation struct {
	Key             upstreamActivitySessionKey
	LastActivityAt  *time.Time
	LastQualifiedAt *time.Time
	Qualified       bool
	Stop            bool
}

type upstreamActivitySession struct {
	key        upstreamActivitySessionKey
	lastSeen   time.Time
	lastSeq    uint64
	stopped    bool
	minWatch   time.Duration
	hasBase    bool
	baseAt     time.Time
	basePos    int64
	accumulate time.Duration
	qualified  bool
	// lastValidAt is the receipt time of the last sample that actually added
	// valid viewing time. A paused/seeked Stop never advances it, so a final
	// flush cannot extend the qualified time past the playback evidence.
	lastValidAt          time.Time
	persistedQualifiedAt time.Time
	qualifiedAttemptAt   time.Time
}

// upstreamActivityTracker is nil-safe: every method tolerates a nil receiver so
// a minimal App (tests, no SQLite) can call it unconditionally.
type upstreamActivityTracker struct {
	mu sync.Mutex
	// sessions is keyed by user+server+upstream PlaySessionId.
	sessions map[upstreamActivitySessionKey]*upstreamActivitySession
	// activityFlushedAt throttles last-activity writes per server+account.
	// Sessions without a PlaySessionId have no session state, so this is the
	// only bounded place to remember the last write attempt.
	activityFlushedAt map[upstreamActivityFlushKey]time.Time
	// ended keeps a bounded tombstone for stopped play sessions. A stopped
	// session may be evicted from sessions when a new one needs the slot; the
	// tombstone still rejects a late Progress of that PlaySessionId instead of
	// recreating the session and resuming accumulation.
	ended   map[upstreamActivitySessionKey]time.Time
	nextSeq uint64
	now     func() time.Time // replaceable in tests
}

func newUpstreamActivityTracker() *upstreamActivityTracker {
	return &upstreamActivityTracker{
		sessions:          map[upstreamActivitySessionKey]*upstreamActivitySession{},
		activityFlushedAt: map[upstreamActivityFlushKey]time.Time{},
		ended:             map[upstreamActivitySessionKey]time.Time{},
		now:               time.Now,
	}
}

func (t *upstreamActivityTracker) currentTime() time.Time {
	if t == nil || t.now == nil {
		return time.Now()
	}
	return t.now()
}

// Begin assigns the local receipt order and time of one session event. It must
// run before the upstream request, so concurrent events are ordered by arrival
// instead of by whichever response completes first. The caller binds the
// account scope into Token.Key.AccountScope after the pre-request snapshot
// check; keeping that outside the lock preserves receipt ordering. An empty
// server or user yields the zero token, which Observe ignores.
func (t *upstreamActivityTracker) Begin(userID, serverID, playSessionID string) upstreamActivitySampleToken {
	if t == nil || userID == "" || serverID == "" {
		return upstreamActivitySampleToken{}
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.nextSeq++
	return upstreamActivitySampleToken{
		Key: upstreamActivitySessionKey{
			UserID:        userID,
			ServerID:      serverID,
			PlaySessionID: playSessionID,
		},
		Seq:        t.nextSeq,
		ReceivedAt: t.currentTime(),
	}
}

// evictExpiredLocked drops sessions and throttling marks whose last evidence is
// older than the TTL. It runs lazily from Observe/Begin, so no goroutine or
// timer is required.
func (t *upstreamActivityTracker) evictExpiredLocked(now time.Time) {
	for key, session := range t.sessions {
		if now.Sub(session.lastSeen) >= upstreamActivitySessionTTL {
			delete(t.sessions, key)
		}
	}
	for key, at := range t.activityFlushedAt {
		if now.Sub(at) >= upstreamActivitySessionTTL {
			delete(t.activityFlushedAt, key)
		}
	}
	for key, at := range t.ended {
		if now.Sub(at) >= upstreamActivitySessionTTL {
			delete(t.ended, key)
		}
	}
}

// makeRoomLocked frees one slot for a newly tracked session. Stopped sessions
// are safe to drop: their final flush was already attempted. A live session is
// never reused or dropped, so a saturated tracker degrades to activity-only
// statistics instead of mixing two streams into one session.
func (t *upstreamActivityTracker) makeRoomLocked() bool {
	if len(t.sessions) < upstreamActivityMaxSessions {
		return true
	}
	type stoppedRef struct {
		key  upstreamActivitySessionKey
		seen time.Time
	}
	refs := make([]stoppedRef, 0, len(t.sessions))
	for key, session := range t.sessions {
		if session.stopped {
			refs = append(refs, stoppedRef{key: key, seen: session.lastSeen})
		}
	}
	if len(refs) == 0 {
		return false
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].seen.Before(refs[j].seen) })
	for _, ref := range refs {
		if len(t.sessions) < upstreamActivityMaxSessions {
			break
		}
		delete(t.sessions, ref.key)
	}
	return len(t.sessions) < upstreamActivityMaxSessions
}

// activityOnlyObservation reports confirmed activity for a stream that has no
// reliable play-session identity (or no free session slot). It never produces a
// qualified timestamp: without a session there is no bounded accumulation.
func (t *upstreamActivityTracker) activityOnlyObservation(token upstreamActivitySampleToken, kind playbackEventKind) upstreamActivityObservation {
	observation := upstreamActivityObservation{Key: token.Key}
	if kind == playbackEventStopped || t.activityFlushDueLocked(token.Key, token.ReceivedAt) {
		at := token.ReceivedAt
		observation.LastActivityAt = &at
	}
	return observation
}

// activityFlushDueLocked reserves the per-server+account activity write slot.
// The reservation is taken even when the write later fails, so a broken
// database is retried at the normal interval instead of on every heartbeat;
// the success/persisted state remains the tracker's separate marker.
func (t *upstreamActivityTracker) activityFlushDueLocked(key upstreamActivitySessionKey, now time.Time) bool {
	if t.activityFlushedAt == nil {
		t.activityFlushedAt = map[upstreamActivityFlushKey]time.Time{}
	}
	flushKey := upstreamActivityFlushKey{ServerID: key.ServerID, AccountScope: key.AccountScope}
	if last, ok := t.activityFlushedAt[flushKey]; ok && now.Sub(last) < upstreamActivityPersistInterval {
		return false
	}
	t.activityFlushedAt[flushKey] = now
	return true
}

// Observe applies one confirmed event. It returns what the caller should persist
// and whether the session ended. Rejected (out-of-order) samples return an empty
// observation: an older position must not be treated as a new observation just
// because its upstream response arrived later.
func (t *upstreamActivityTracker) Observe(sample upstreamActivitySample, minWatch time.Duration) upstreamActivityObservation {
	if t == nil || sample.Token.Seq == 0 || sample.Token.ReceivedAt.IsZero() {
		return upstreamActivityObservation{}
	}
	token := sample.Token
	now := t.currentTime()

	t.mu.Lock()
	defer t.mu.Unlock()
	if t.sessions == nil {
		t.sessions = map[upstreamActivitySessionKey]*upstreamActivitySession{}
	}
	t.evictExpiredLocked(now)

	if token.Key.PlaySessionID == "" {
		return t.activityOnlyObservation(token, sample.Kind)
	}

	session := t.sessions[token.Key]
	if session == nil {
		// A stopped PlaySessionId must not be resurrected by a late sample, even
		// after its tombstone session was evicted for capacity.
		if endedAt, ok := t.ended[token.Key]; ok && now.Sub(endedAt) < upstreamActivitySessionTTL {
			return upstreamActivityObservation{Key: token.Key}
		}
		if !t.makeRoomLocked() {
			return t.activityOnlyObservation(token, sample.Kind)
		}
		session = &upstreamActivitySession{
			key:      token.Key,
			minWatch: normalizeUpstreamActivityMinWatch(minWatch),
		}
		t.sessions[token.Key] = session
	}
	// Strictly newer receipt only: duplicates and older concurrent requests are
	// not new viewing evidence.
	if token.Seq <= session.lastSeq || session.stopped {
		return upstreamActivityObservation{Key: token.Key}
	}
	session.lastSeq = token.Seq
	session.lastSeen = token.ReceivedAt

	t.accumulateLocked(session, sample)

	threshold := session.minWatch
	if threshold <= 0 {
		threshold = normalizeUpstreamActivityMinWatch(0)
	}
	if !session.qualified && session.accumulate >= threshold && !session.lastValidAt.IsZero() {
		session.qualified = true
	}

	observation := upstreamActivityObservation{
		Key:       token.Key,
		Qualified: session.qualified,
	}
	if sample.Kind == playbackEventStopped || t.activityFlushDueLocked(token.Key, token.ReceivedAt) {
		at := token.ReceivedAt
		observation.LastActivityAt = &at
	}
	if session.qualified && session.lastValidAt.After(session.persistedQualifiedAt) {
		// The first qualification is written immediately; later refreshes are
		// rate-limited, except a Stop which must save the final valid time.
		force := sample.Kind == playbackEventStopped
		if force || session.qualifiedAttemptAt.IsZero() ||
			token.ReceivedAt.Sub(session.qualifiedAttemptAt) >= upstreamActivityQualifiedRefresh {
			at := session.lastValidAt
			observation.LastQualifiedAt = &at
			session.qualifiedAttemptAt = token.ReceivedAt
		}
	}
	if sample.Kind == playbackEventStopped {
		observation.Stop = true
	}
	return observation
}

// accumulateLocked applies the baseline/seek/pause rules for one accepted
// sample and, when the sample proves real forward playback, adds the monotonic
// elapsed time (never the raw position difference) to the session.
func (t *upstreamActivityTracker) accumulateLocked(session *upstreamActivitySession, sample upstreamActivitySample) {
	at := sample.Token.ReceivedAt
	position, hasPosition := upstreamActivitySamplePosition(sample)
	if !hasPosition || sample.Paused {
		// Missing position and paused reports are not evidence of viewing: the
		// next usable sample only rebuilds the baseline.
		session.hasBase = false
		return
	}
	if !session.hasBase {
		session.hasBase = true
		session.baseAt = at
		session.basePos = position
		return
	}

	elapsed := at.Sub(session.baseAt)
	deltaTicks := position - session.basePos
	switch {
	case elapsed <= 0:
		// Same clock instant: nothing can have been watched yet.
		session.baseAt = at
		session.basePos = position
	case elapsed > upstreamActivityMaxSampleGap:
		// A hole in the heartbeat evidence: no compensation, rebuild.
		session.baseAt = at
		session.basePos = position
	case deltaTicks < 0:
		// Rewind: rebuild without credit.
		session.baseAt = at
		session.basePos = position
	case deltaTicks == 0:
		// No forward progress (buffering, duplicate). Keep the baseline so the
		// elapsed bound still applies to the next advance.
	default:
		delta := upstreamActivityTicksToDuration(deltaTicks)
		if delta > elapsed*upstreamActivitySeekFactor+upstreamActivitySeekSlack {
			// Seek/position jump: not viewing, rebuild the baseline.
			session.baseAt = at
			session.basePos = position
			return
		}
		credit := delta
		if credit > elapsed {
			// Fast-forward: at most the real elapsed time is viewing.
			credit = elapsed
		}
		if credit > 0 {
			session.accumulate += credit
			session.lastValidAt = at
		}
		session.baseAt = at
		session.basePos = position
	}
}

// Discard resets the pending evidence interval of one session after an
// unconfirmed report (explicit 4xx refusal or unknown transport outcome). It
// never undoes already credited accumulation, never writes activity and never
// qualifies a session: it only prevents the next confirmed sample from
// bridging the unconfirmed gap from the old baseline. Advancing lastSeq also
// keeps a late, older response from being credited after the failure.
func (t *upstreamActivityTracker) Discard(token upstreamActivitySampleToken) {
	if t == nil || token.Seq == 0 || token.Key.PlaySessionID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	session := t.sessions[token.Key]
	if session == nil || session.stopped || token.Seq <= session.lastSeq {
		return
	}
	session.lastSeq = token.Seq
	session.hasBase = false
}

// MarkPersistFailed rolls back the throttling reservations taken by Observe
// when Record did not succeed. The activity write slot and the qualified
// refresh window are therefore never remembered as flushed without a
// successful write; the next heartbeat may retry them. Already persisted
// timestamps and credited accumulation are untouched.
func (t *upstreamActivityTracker) MarkPersistFailed(key upstreamActivitySessionKey, observation upstreamActivityObservation) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if observation.LastActivityAt != nil {
		delete(t.activityFlushedAt, upstreamActivityFlushKey{ServerID: key.ServerID, AccountScope: key.AccountScope})
	}
	if observation.LastQualifiedAt != nil {
		if session := t.sessions[key]; session != nil {
			session.qualifiedAttemptAt = time.Time{}
		}
	}
}

// MarkPersisted records that a qualified timestamp was written successfully.
// The first-qualified/flush throttling only advances on success, so a failed
// write is retried instead of being remembered as flushed.
func (t *upstreamActivityTracker) MarkPersisted(key upstreamActivitySessionKey, qualifiedAt *time.Time) {
	if t == nil || qualifiedAt == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	session := t.sessions[key]
	if session == nil {
		return
	}
	if qualifiedAt.After(session.persistedQualifiedAt) {
		session.persistedQualifiedAt = *qualifiedAt
	}
}

// Finish marks a stopped session. It is kept for the TTL as a tombstone so a
// slow, older Progress of the same play session cannot recreate it and resume
// accumulating after Stop; stopped sessions are the first candidates when a new
// session needs a slot.
func (t *upstreamActivityTracker) Finish(key upstreamActivitySessionKey) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if session := t.sessions[key]; session != nil {
		session.stopped = true
	}
	if t.ended == nil {
		t.ended = map[upstreamActivitySessionKey]time.Time{}
	}
	t.ended[key] = t.currentTime()
	t.evictEndedLocked()
}

// evictEndedLocked bounds the tombstone map by dropping the oldest entries.
func (t *upstreamActivityTracker) evictEndedLocked() {
	if len(t.ended) <= upstreamActivityMaxSessions {
		return
	}
	oldestKey := upstreamActivitySessionKey{}
	var oldest time.Time
	for key, at := range t.ended {
		if oldest.IsZero() || at.Before(oldest) {
			oldestKey, oldest = key, at
		}
	}
	delete(t.ended, oldestKey)
}

// ForgetServer drops every session and throttling mark of a deleted upstream.
// In-memory state is not authoritative, but a deleted node must not keep
// accumulating for a configuration that no longer exists.
func (t *upstreamActivityTracker) ForgetServer(serverID string) {
	if t == nil || serverID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for key := range t.sessions {
		if key.ServerID == serverID {
			delete(t.sessions, key)
		}
	}
	for key := range t.ended {
		if key.ServerID == serverID {
			delete(t.ended, key)
		}
	}
	for key := range t.activityFlushedAt {
		if key.ServerID == serverID {
			delete(t.activityFlushedAt, key)
		}
	}
}

// sessionCount is used by tests to assert the bounded map lifecycle.
func (t *upstreamActivityTracker) sessionCount() int {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.sessions)
}

func normalizeUpstreamActivityMinWatch(minWatch time.Duration) time.Duration {
	if minWatch > 0 {
		return minWatch
	}
	defaults := DefaultUpstreamActivityPolicy()
	if defaults.MinWatchSeconds <= 0 {
		return 60 * time.Second
	}
	return time.Duration(defaults.MinWatchSeconds) * time.Second
}

func upstreamActivityTicksToDuration(ticks int64) time.Duration {
	return time.Duration(float64(ticks) / float64(upstreamActivityTicksPerSecond) * float64(time.Second))
}

func upstreamActivitySamplePosition(sample upstreamActivitySample) (int64, bool) {
	if sample.PositionTicks == nil || *sample.PositionTicks < 0 {
		return 0, false
	}
	return *sample.PositionTicks, true
}
