package backend

import (
	"strconv"
	"testing"
	"time"
)

// fakeUpstreamActivityClock drives the tracker without sleeping. The tracker
// only uses time.Time arithmetic, so the fake clock exercises the same
// monotonic elapsed logic as time.Now (wall+monotonic) in production.
type fakeUpstreamActivityClock struct {
	now time.Time
}

func newFakeUpstreamActivityClock() *fakeUpstreamActivityClock {
	return &fakeUpstreamActivityClock{now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
}

func (c *fakeUpstreamActivityClock) advance(d time.Duration) {
	c.now = c.now.Add(d)
}

func (c *fakeUpstreamActivityClock) tracker() *upstreamActivityTracker {
	t := newUpstreamActivityTracker()
	t.now = func() time.Time { return c.now }
	return t
}

func upstreamActivityTestTicks(seconds float64) int64 {
	return int64(seconds * float64(upstreamActivityTicksPerSecond))
}

func upstreamActivityTestPos(seconds float64) *int64 {
	ticks := upstreamActivityTestTicks(seconds)
	return &ticks
}

func upstreamActivityTestKey(user, server, playSession string) upstreamActivitySessionKey {
	return upstreamActivitySessionKey{UserID: user, ServerID: server, PlaySessionID: playSession}
}

func upstreamActivityTestScopedKey(user, server, scope, playSession string) upstreamActivitySessionKey {
	return upstreamActivitySessionKey{UserID: user, ServerID: server, AccountScope: scope, PlaySessionID: playSession}
}

// event builds a token at the current fake time, exactly like the handler does
// before the upstream request, and offers it to the tracker.
func (c *fakeUpstreamActivityClock) event(t *upstreamActivityTracker, key upstreamActivitySessionKey, kind playbackEventKind, position *int64, paused bool, minWatch time.Duration) upstreamActivityObservation {
	token := t.Begin(key.UserID, key.ServerID, key.PlaySessionID)
	token.Key.AccountScope = key.AccountScope
	sample := upstreamActivitySample{
		Token:         token,
		Kind:          kind,
		PositionTicks: position,
		Paused:        paused,
	}
	return t.Observe(sample, minWatch)
}

func (c *fakeUpstreamActivityClock) eventAt(t *upstreamActivityTracker, key upstreamActivitySessionKey, kind playbackEventKind, position *int64, paused bool, minWatch time.Duration, advance time.Duration) upstreamActivityObservation {
	c.advance(advance)
	return c.event(t, key, kind, position, paused, minWatch)
}

func upstreamActivityTestAccumulated(t *testing.T, tracker *upstreamActivityTracker, key upstreamActivitySessionKey) time.Duration {
	t.Helper()
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	session := tracker.sessions[key]
	if session == nil {
		return 0
	}
	return session.accumulate
}

func requireNoQualifiedTime(t *testing.T, observation upstreamActivityObservation) {
	t.Helper()
	if observation.LastQualifiedAt != nil {
		t.Fatalf("unexpected qualified write at %v", *observation.LastQualifiedAt)
	}
}

func requireQualifiedTime(t *testing.T, observation upstreamActivityObservation, want time.Time) {
	t.Helper()
	if observation.LastQualifiedAt == nil {
		t.Fatal("expected a qualified timestamp")
	}
	if !observation.LastQualifiedAt.Equal(want) {
		t.Fatalf("qualified time = %v, want %v", *observation.LastQualifiedAt, want)
	}
}

// TestUpstreamActivityStartedOnlyDoesNotQualify: one Started event, however
// long the client waits, only establishes a baseline.
func TestUpstreamActivityStartedOnlyDoesNotQualify(t *testing.T) {
	clock := newFakeUpstreamActivityClock()
	tracker := clock.tracker()
	key := upstreamActivityTestKey("user", "server", "play-1")

	observation := clock.event(tracker, key, playbackEventStarted, upstreamActivityTestPos(0), false, 60*time.Second)
	requireNoQualifiedTime(t, observation)
	if got := upstreamActivityTestAccumulated(t, tracker, key); got != 0 {
		t.Fatalf("Started credited %v, want 0", got)
	}

	// No second sample: waiting alone never adds viewing time.
	clock.advance(10 * time.Minute)
	if got := upstreamActivityTestAccumulated(t, tracker, key); got != 0 {
		t.Fatalf("idle session credited %v, want 0", got)
	}
}

// TestUpstreamActivityProgressPairsQualifyAndRefresh is the normal 1x case: two
// forward samples cross a 60s threshold, the write is immediate, the next
// refresh waits for the 60s interval, and a Stop always flushes.
func TestUpstreamActivityProgressPairsQualifyAndRefresh(t *testing.T) {
	clock := newFakeUpstreamActivityClock()
	tracker := clock.tracker()
	key := upstreamActivityTestKey("user", "server", "play-1")
	const minWatch = 60 * time.Second

	clock.event(tracker, key, playbackEventStarted, upstreamActivityTestPos(0), false, minWatch)
	first := clock.eventAt(tracker, key, playbackEventProgress, upstreamActivityTestPos(30), false, minWatch, 30*time.Second)
	requireNoQualifiedTime(t, first)

	qualifiedAt := clock.now.Add(30 * time.Second)
	second := clock.eventAt(tracker, key, playbackEventProgress, upstreamActivityTestPos(60), false, minWatch, 30*time.Second)
	requireQualifiedTime(t, second, qualifiedAt)
	tracker.MarkPersisted(key, second.LastQualifiedAt)
	if got := upstreamActivityTestAccumulated(t, tracker, key); got != 60*time.Second {
		t.Fatalf("accumulated = %v, want 60s", got)
	}

	// Only 10s later: real valid evidence exists, but the refresh is throttled.
	tooSoon := clock.eventAt(tracker, key, playbackEventProgress, upstreamActivityTestPos(70), false, minWatch, 10*time.Second)
	requireNoQualifiedTime(t, tooSoon)

	// At the 60s mark the new valid time is written.
	refreshAt := clock.now.Add(50 * time.Second)
	refresh := clock.eventAt(tracker, key, playbackEventProgress, upstreamActivityTestPos(130), false, minWatch, 50*time.Second)
	requireQualifiedTime(t, refresh, refreshAt)
	tracker.MarkPersisted(key, refresh.LastQualifiedAt)

	// A Stop with a valid forward sample flushes even inside the interval.
	stopAt := clock.now.Add(5 * time.Second)
	stop := clock.eventAt(tracker, key, playbackEventStopped, upstreamActivityTestPos(135), false, minWatch, 5*time.Second)
	requireQualifiedTime(t, stop, stopAt)
	if !stop.Stop {
		t.Fatal("Stop observation must mark the session ended")
	}
	tracker.Finish(key)

	// A late sample of the finished session is not new evidence.
	late := clock.eventAt(tracker, key, playbackEventProgress, upstreamActivityTestPos(180), false, minWatch, 45*time.Second)
	requireNoQualifiedTime(t, late)
	if late.LastActivityAt != nil {
		t.Fatalf("finished session accepted a late activity write at %v", *late.LastActivityAt)
	}
}

// TestUpstreamActivityPauseResumeDoesNotBackfill: pause invalidates the
// baseline, and the first sample after resume only rebuilds it.
func TestUpstreamActivityPauseResumeDoesNotBackfill(t *testing.T) {
	clock := newFakeUpstreamActivityClock()
	tracker := clock.tracker()
	key := upstreamActivityTestKey("user", "server", "play-1")
	const minWatch = 60 * time.Second

	clock.event(tracker, key, playbackEventStarted, upstreamActivityTestPos(0), false, minWatch)
	clock.eventAt(tracker, key, playbackEventProgress, upstreamActivityTestPos(30), false, minWatch, 30*time.Second)

	// Paused report at the same position: no credit, baseline cleared.
	paused := clock.eventAt(tracker, key, playbackEventProgress, upstreamActivityTestPos(30), true, minWatch, 30*time.Second)
	requireNoQualifiedTime(t, paused)

	// Resume sample: baseline only, no backfill for the paused interval.
	clock.eventAt(tracker, key, playbackEventProgress, upstreamActivityTestPos(30), false, minWatch, 40*time.Second)
	if got := upstreamActivityTestAccumulated(t, tracker, key); got != 30*time.Second {
		t.Fatalf("paused interval credited: accumulated = %v, want 30s", got)
	}

	// Normal play after resume counts again.
	clock.eventAt(tracker, key, playbackEventProgress, upstreamActivityTestPos(40), false, minWatch, 10*time.Second)
	if got := upstreamActivityTestAccumulated(t, tracker, key); got != 40*time.Second {
		t.Fatalf("resumed playback accumulated = %v, want 40s", got)
	}
}

// TestUpstreamActivitySeekRewindAndFastForward: a jump is discarded and
// rebuilds the baseline, a rewind is discarded, and a fast-forward can never
// credit more than the real elapsed time.
func TestUpstreamActivitySeekRewindAndFastForward(t *testing.T) {
	clock := newFakeUpstreamActivityClock()
	tracker := clock.tracker()
	key := upstreamActivityTestKey("user", "server", "play-1")
	const minWatch = time.Hour

	clock.event(tracker, key, playbackEventStarted, upstreamActivityTestPos(0), false, minWatch)
	clock.eventAt(tracker, key, playbackEventProgress, upstreamActivityTestPos(10), false, minWatch, 10*time.Second)

	// 3x position advance over 10s is below the seek tolerance but capped to
	// the real elapsed time.
	clock.eventAt(tracker, key, playbackEventProgress, upstreamActivityTestPos(40), false, minWatch, 10*time.Second)
	if got := upstreamActivityTestAccumulated(t, tracker, key); got != 20*time.Second {
		t.Fatalf("fast-forward accumulated = %v, want 20s (real elapsed only)", got)
	}

	// A large jump is a seek: no credit.
	clock.eventAt(tracker, key, playbackEventProgress, upstreamActivityTestPos(900), false, minWatch, 10*time.Second)
	if got := upstreamActivityTestAccumulated(t, tracker, key); got != 20*time.Second {
		t.Fatalf("seek credited: accumulated = %v, want 20s", got)
	}

	// The sample after the seek rebuilds the baseline; the following one counts.
	clock.eventAt(tracker, key, playbackEventProgress, upstreamActivityTestPos(910), false, minWatch, 10*time.Second)
	if got := upstreamActivityTestAccumulated(t, tracker, key); got != 30*time.Second {
		t.Fatalf("post-seek sample accumulated = %v, want 30s", got)
	}

	// Rewind: discarded and rebuilt.
	clock.eventAt(tracker, key, playbackEventProgress, upstreamActivityTestPos(300), false, minWatch, 10*time.Second)
	if got := upstreamActivityTestAccumulated(t, tracker, key); got != 30*time.Second {
		t.Fatalf("rewind credited: accumulated = %v, want 30s", got)
	}
	clock.eventAt(tracker, key, playbackEventProgress, upstreamActivityTestPos(310), false, minWatch, 10*time.Second)
	if got := upstreamActivityTestAccumulated(t, tracker, key); got != 40*time.Second {
		t.Fatalf("post-rewind sample accumulated = %v, want 40s", got)
	}
}

// TestUpstreamActivityLongGapAndMissingPosition: a heartbeat hole is not
// compensated, and a sample without PositionTicks cannot accumulate.
func TestUpstreamActivityLongGapAndMissingPosition(t *testing.T) {
	clock := newFakeUpstreamActivityClock()
	tracker := clock.tracker()
	key := upstreamActivityTestKey("user", "server", "play-1")
	const minWatch = time.Hour

	clock.event(tracker, key, playbackEventStarted, upstreamActivityTestPos(0), false, minWatch)

	// 91s gap: no credit, baseline rebuilt at the new sample.
	clock.eventAt(tracker, key, playbackEventProgress, upstreamActivityTestPos(91), false, minWatch, 91*time.Second)
	if got := upstreamActivityTestAccumulated(t, tracker, key); got != 0 {
		t.Fatalf("long gap credited %v, want 0", got)
	}

	// Normal sample after the rebuild counts.
	clock.eventAt(tracker, key, playbackEventProgress, upstreamActivityTestPos(101), false, minWatch, 10*time.Second)
	if got := upstreamActivityTestAccumulated(t, tracker, key); got != 10*time.Second {
		t.Fatalf("accumulated = %v, want 10s", got)
	}

	// Missing position clears the baseline instead of bridging the gap.
	clock.eventAt(tracker, key, playbackEventProgress, nil, false, minWatch, 10*time.Second)
	clock.eventAt(tracker, key, playbackEventProgress, upstreamActivityTestPos(200), false, minWatch, 10*time.Second)
	if got := upstreamActivityTestAccumulated(t, tracker, key); got != 10*time.Second {
		t.Fatalf("missing position credited: accumulated = %v, want 10s", got)
	}
	clock.eventAt(tracker, key, playbackEventProgress, upstreamActivityTestPos(210), false, minWatch, 10*time.Second)
	if got := upstreamActivityTestAccumulated(t, tracker, key); got != 20*time.Second {
		t.Fatalf("accumulated = %v, want 20s", got)
	}
}

// TestUpstreamActivityOutOfOrderReceiptIsIgnored: a request received earlier
// but completed later must not become the newer observation.
func TestUpstreamActivityOutOfOrderReceiptIsIgnored(t *testing.T) {
	clock := newFakeUpstreamActivityClock()
	tracker := clock.tracker()
	key := upstreamActivityTestKey("user", "server", "play-1")
	const minWatch = time.Hour

	clock.event(tracker, key, playbackEventStarted, upstreamActivityTestPos(0), false, minWatch)

	// Older request received at t=10 but offered to the tracker first.
	clock.advance(10 * time.Second)
	older := upstreamActivitySample{
		Token:         tracker.Begin(key.UserID, key.ServerID, key.PlaySessionID),
		Kind:          playbackEventProgress,
		PositionTicks: upstreamActivityTestPos(10),
	}
	// A newer request is received at t=20 and completes before the older one.
	clock.advance(10 * time.Second)
	newer := clock.event(tracker, key, playbackEventProgress, upstreamActivityTestPos(20), false, minWatch)
	if newer.LastQualifiedAt != nil {
		t.Fatal("unexpected qualification")
	}
	if got := upstreamActivityTestAccumulated(t, tracker, key); got != 20*time.Second {
		t.Fatalf("forward sample accumulated = %v, want 20s", got)
	}

	// The older request completes now; its smaller sequence is rejected and the
	// baseline stays at the newer sample.
	stale := tracker.Observe(older, minWatch)
	if stale.LastActivityAt != nil || stale.LastQualifiedAt != nil {
		t.Fatalf("stale observation wrote data: %#v", stale)
	}
	if got := upstreamActivityTestAccumulated(t, tracker, key); got != 20*time.Second {
		t.Fatalf("stale sample changed accumulation to %v, want 20s", got)
	}
	clock.eventAt(tracker, key, playbackEventProgress, upstreamActivityTestPos(30), false, minWatch, 10*time.Second)
	if got := upstreamActivityTestAccumulated(t, tracker, key); got != 30*time.Second {
		t.Fatalf("accumulated after stale sample = %v, want 30s", got)
	}
}

// TestUpstreamActivityDuplicateAndNoSessionIdentity: duplicate samples add
// nothing, and a stream without PlaySessionId only contributes activity.
func TestUpstreamActivityDuplicateAndNoSessionIdentity(t *testing.T) {
	clock := newFakeUpstreamActivityClock()
	tracker := clock.tracker()
	key := upstreamActivityTestKey("user", "server", "play-1")
	const minWatch = time.Hour

	clock.event(tracker, key, playbackEventStarted, upstreamActivityTestPos(0), false, minWatch)
	clock.eventAt(tracker, key, playbackEventProgress, upstreamActivityTestPos(20), false, minWatch, 20*time.Second)
	clock.eventAt(tracker, key, playbackEventProgress, upstreamActivityTestPos(20), false, minWatch, 10*time.Second)
	if got := upstreamActivityTestAccumulated(t, tracker, key); got != 20*time.Second {
		t.Fatalf("duplicate credited: accumulated = %v, want 20s", got)
	}

	// Advance past the per-server activity throttle so the sessionless event
	// reports its activity.
	clock.advance(61 * time.Second)
	noSession := upstreamActivityTestKey("user", "server", "")
	observation := clock.event(tracker, noSession, playbackEventStarted, upstreamActivityTestPos(0), false, minWatch)
	if observation.LastActivityAt == nil {
		t.Fatal("first confirmed event must report activity")
	}
	requireNoQualifiedTime(t, observation)
	if got := tracker.sessionCount(); got != 1 {
		t.Fatalf("session count = %d, want 1 (no session for an empty PlaySessionId)", got)
	}
	for i := 0; i < 5; i++ {
		observation = clock.eventAt(tracker, noSession, playbackEventProgress, upstreamActivityTestPos(float64(i+1)*60), false, minWatch, 60*time.Second)
		requireNoQualifiedTime(t, observation)
	}
}

// TestUpstreamActivityActivityIsThrottled: the last-activity timestamp is not
// written on every heartbeat, but a Stop always flushes.
func TestUpstreamActivityActivityIsThrottled(t *testing.T) {
	clock := newFakeUpstreamActivityClock()
	tracker := clock.tracker()
	key := upstreamActivityTestKey("user", "server", "play-1")
	const minWatch = time.Hour

	first := clock.event(tracker, key, playbackEventStarted, upstreamActivityTestPos(0), false, minWatch)
	if first.LastActivityAt == nil || !first.LastActivityAt.Equal(clock.now) {
		t.Fatalf("first activity = %v, want %v", first.LastActivityAt, clock.now)
	}
	soon := clock.eventAt(tracker, key, playbackEventProgress, upstreamActivityTestPos(10), false, minWatch, 10*time.Second)
	if soon.LastActivityAt != nil {
		t.Fatalf("activity written again after 10s: %v", *soon.LastActivityAt)
	}
	dueAt := clock.now.Add(50 * time.Second)
	due := clock.eventAt(tracker, key, playbackEventProgress, upstreamActivityTestPos(60), false, minWatch, 50*time.Second)
	if due.LastActivityAt == nil || !due.LastActivityAt.Equal(dueAt) {
		t.Fatalf("activity at 60s = %v, want %v", due.LastActivityAt, dueAt)
	}
	stopAt := clock.now.Add(5 * time.Second)
	stop := clock.eventAt(tracker, key, playbackEventStopped, upstreamActivityTestPos(65), false, minWatch, 5*time.Second)
	if stop.LastActivityAt == nil || !stop.LastActivityAt.Equal(stopAt) {
		t.Fatalf("Stop activity = %v, want %v", stop.LastActivityAt, stopAt)
	}
}

// TestUpstreamActivityPausedStopDoesNotExtendQualified: the final flush writes
// the last sample that proved playback, not the Stop receipt time.
func TestUpstreamActivityPausedStopDoesNotExtendQualified(t *testing.T) {
	clock := newFakeUpstreamActivityClock()
	tracker := clock.tracker()
	key := upstreamActivityTestKey("user", "server", "play-1")
	const minWatch = 30 * time.Second

	clock.event(tracker, key, playbackEventStarted, upstreamActivityTestPos(0), false, minWatch)
	qualifiedAt := clock.now.Add(30 * time.Second)
	qualified := clock.eventAt(tracker, key, playbackEventProgress, upstreamActivityTestPos(30), false, minWatch, 30*time.Second)
	requireQualifiedTime(t, qualified, qualifiedAt)
	tracker.MarkPersisted(key, qualified.LastQualifiedAt)

	// Paused Stop much later: no new valid evidence, so nothing to extend.
	stop := clock.eventAt(tracker, key, playbackEventStopped, upstreamActivityTestPos(30), true, minWatch, 10*time.Minute)
	requireNoQualifiedTime(t, stop)
	if stop.LastActivityAt == nil {
		t.Fatal("Stop must still report activity")
	}
	if !stop.Stop {
		t.Fatal("Stop must end the session")
	}
}

// TestUpstreamActivityThresholdCapturedAtSessionStart: a later threshold change
// never retroactively qualifies an existing session; a new session uses it.
func TestUpstreamActivityThresholdCapturedAtSessionStart(t *testing.T) {
	clock := newFakeUpstreamActivityClock()
	tracker := clock.tracker()
	oldKey := upstreamActivityTestKey("user", "server", "play-old")

	clock.event(tracker, oldKey, playbackEventStarted, upstreamActivityTestPos(0), false, 60*time.Second)
	clock.eventAt(tracker, oldKey, playbackEventProgress, upstreamActivityTestPos(30), false, 60*time.Second, 30*time.Second)
	// Lowering the threshold only applies to sessions started afterwards.
	lowered := clock.eventAt(tracker, oldKey, playbackEventProgress, upstreamActivityTestPos(35), false, 10*time.Second, 5*time.Second)
	requireNoQualifiedTime(t, lowered)

	newKey := upstreamActivityTestKey("user", "server", "play-new")
	clock.event(tracker, newKey, playbackEventStarted, upstreamActivityTestPos(0), false, 10*time.Second)
	qualifiedAt := clock.now.Add(10 * time.Second)
	qualified := clock.eventAt(tracker, newKey, playbackEventProgress, upstreamActivityTestPos(10), false, 10*time.Second, 10*time.Second)
	requireQualifiedTime(t, qualified, qualifiedAt)
}

// TestUpstreamActivityUsersAccumulateIndependently: two local users sharing one
// upstream account do not pool their threshold progress.
func TestUpstreamActivityUsersAccumulateIndependently(t *testing.T) {
	clock := newFakeUpstreamActivityClock()
	tracker := clock.tracker()
	userA := upstreamActivityTestKey("user-a", "server", "play-1")
	userB := upstreamActivityTestKey("user-b", "server", "play-1")
	const minWatch = 30 * time.Second

	clock.event(tracker, userA, playbackEventStarted, upstreamActivityTestPos(0), false, minWatch)
	clock.event(tracker, userB, playbackEventStarted, upstreamActivityTestPos(0), false, minWatch)
	clock.eventAt(tracker, userA, playbackEventProgress, upstreamActivityTestPos(20), false, minWatch, 20*time.Second)
	clock.eventAt(tracker, userB, playbackEventProgress, upstreamActivityTestPos(15), false, minWatch, 25*time.Second)

	qualifiedAt := clock.now.Add(10 * time.Second)
	a := clock.eventAt(tracker, userA, playbackEventProgress, upstreamActivityTestPos(30), false, minWatch, 10*time.Second)
	requireQualifiedTime(t, a, qualifiedAt)
	b := clock.eventAt(tracker, userB, playbackEventProgress, upstreamActivityTestPos(20), false, minWatch, 15*time.Second)
	requireNoQualifiedTime(t, b)
}

// TestUpstreamActivityTTLAndCapacity: lazy eviction frees expired sessions, and
// a saturated tracker degrades to activity-only instead of reusing a live one.
func TestUpstreamActivityTTLAndCapacity(t *testing.T) {
	clock := newFakeUpstreamActivityClock()
	tracker := clock.tracker()
	const minWatch = time.Hour

	key := upstreamActivityTestKey("user", "server", "play-1")
	clock.event(tracker, key, playbackEventStarted, upstreamActivityTestPos(0), false, minWatch)
	if got := tracker.sessionCount(); got != 1 {
		t.Fatalf("session count = %d, want 1", got)
	}
	clock.advance(upstreamActivitySessionTTL)
	other := upstreamActivityTestKey("user", "server", "play-2")
	clock.event(tracker, other, playbackEventStarted, upstreamActivityTestPos(0), false, minWatch)
	if got := tracker.sessionCount(); got != 1 {
		t.Fatalf("expired session not evicted: count = %d, want 1", got)
	}

	// Fill the tracker with live sessions, then verify a new one is not tracked.
	fullClock := newFakeUpstreamActivityClock()
	full := fullClock.tracker()
	for i := 0; i < upstreamActivityMaxSessions; i++ {
		fullKey := upstreamActivityTestKey("user", "server", "fill-"+strconv.Itoa(i))
		fullClock.event(full, fullKey, playbackEventStarted, upstreamActivityTestPos(0), false, minWatch)
	}
	if got := full.sessionCount(); got != upstreamActivityMaxSessions {
		t.Fatalf("session count = %d, want %d", got, upstreamActivityMaxSessions)
	}
	overflowKey := upstreamActivityTestKey("user", "server", "overflow")
	fullClock.advance(61 * time.Second)
	observation := fullClock.event(full, overflowKey, playbackEventStarted, upstreamActivityTestPos(0), false, minWatch)
	if observation.LastActivityAt == nil {
		t.Fatal("overflow event must still report activity")
	}
	requireNoQualifiedTime(t, observation)
	if got := full.sessionCount(); got != upstreamActivityMaxSessions {
		t.Fatalf("capacity exceeded: count = %d, want %d", got, upstreamActivityMaxSessions)
	}
}

// TestUpstreamActivityForgetServerAndNilSafety: deletion drops in-memory state,
// and a nil tracker is a no-op everywhere.
func TestUpstreamActivityForgetServerAndNilSafety(t *testing.T) {
	clock := newFakeUpstreamActivityClock()
	tracker := clock.tracker()
	key := upstreamActivityTestKey("user", "server-a", "play-1")
	clock.event(tracker, key, playbackEventStarted, upstreamActivityTestPos(0), false, time.Hour)
	if got := tracker.sessionCount(); got != 1 {
		t.Fatalf("session count = %d, want 1", got)
	}
	tracker.ForgetServer("server-a")
	if got := tracker.sessionCount(); got != 0 {
		t.Fatalf("session count after ForgetServer = %d, want 0", got)
	}
	if _, ok := tracker.activityFlushedAt[upstreamActivityFlushKey{ServerID: "server-a"}]; ok {
		t.Fatal("activity throttle mark survived ForgetServer")
	}

	var missing *upstreamActivityTracker
	if token := missing.Begin("user", "server", "play"); token.Seq != 0 {
		t.Fatalf("nil Begin token = %#v", token)
	}
	if observation := missing.Observe(upstreamActivitySample{}, time.Minute); observation.LastActivityAt != nil || observation.LastQualifiedAt != nil {
		t.Fatalf("nil Observe = %#v", observation)
	}
	missing.MarkPersisted(key, &clock.now)
	missing.Finish(key)
	missing.ForgetServer("server")
	if got := missing.sessionCount(); got != 0 {
		t.Fatalf("nil sessionCount = %d", got)
	}
}

// TestUpstreamActivityAccountScopeIsolation: a new account reusing the same
// PlaySessionId neither inherits accumulation nor the old account's activity
// write throttle.
func TestUpstreamActivityAccountScopeIsolation(t *testing.T) {
	clock := newFakeUpstreamActivityClock()
	tracker := clock.tracker()
	const minWatch = 60 * time.Second
	oldKey := upstreamActivityTestScopedKey("user", "server", "scope-old", "play-1")
	newKey := upstreamActivityTestScopedKey("user", "server", "scope-new", "play-1")

	oldStart := clock.event(tracker, oldKey, playbackEventStarted, upstreamActivityTestPos(0), false, minWatch)
	clock.eventAt(tracker, oldKey, playbackEventProgress, upstreamActivityTestPos(30), false, minWatch, 30*time.Second)
	if got := upstreamActivityTestAccumulated(t, tracker, oldKey); got != 30*time.Second {
		t.Fatalf("old-account accumulation = %v, want 30s", got)
	}

	// The new account starts immediately; its first activity is not suppressed
	// by the old account's throttle window.
	newStart := clock.event(tracker, newKey, playbackEventStarted, upstreamActivityTestPos(0), false, minWatch)
	if oldStart.LastActivityAt == nil || newStart.LastActivityAt == nil {
		t.Fatalf("per-account activity throttle failed: old=%v new=%v", oldStart.LastActivityAt, newStart.LastActivityAt)
	}
	if got := upstreamActivityTestAccumulated(t, tracker, newKey); got != 0 {
		t.Fatalf("new account inherited accumulation: %v", got)
	}

	clock.eventAt(tracker, newKey, playbackEventProgress, upstreamActivityTestPos(30), false, minWatch, 30*time.Second)
	if got := upstreamActivityTestAccumulated(t, tracker, newKey); got != 30*time.Second {
		t.Fatalf("new-account accumulation = %v, want 30s", got)
	}
	qualifiedAt := clock.now.Add(30 * time.Second)
	qualified := clock.eventAt(tracker, newKey, playbackEventProgress, upstreamActivityTestPos(60), false, minWatch, 30*time.Second)
	requireQualifiedTime(t, qualified, qualifiedAt)
}
