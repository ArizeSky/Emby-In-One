// session_watch_event_test.go covers S9: playback fields govern what changes,
// a missing/invalid position never overwrites the stored value, explicit 0 is
// a real position, and a superseded session's late event is dropped.
package backend

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func s9RequestBody(fields map[string]any) map[string]any {
	body := map[string]any{"ItemId": "item-1"}
	for k, v := range fields {
		body[k] = v
	}
	return body
}

func s9App(t *testing.T) (*App, string) {
	t.Helper()
	ws := newTestWatchStore(t)
	app := &App{WatchStore: ws, sessionEvents: newSessionEventTracker()}
	return app, "user-1"
}

func s9Request(userID string) *http.Request {
	req := httptest.NewRequest("POST", "/Sessions/Playing/Progress", nil)
	req = req.WithContext(context.WithValue(req.Context(), requestContextKey{}, &RequestContext{ProxyUser: &tokenInfo{Role: "user", UserID: userID}}))
	return req
}

func s9Progress(t *testing.T, app *App, userID string) *WatchProgress {
	t.Helper()
	p, err := app.WatchStore.GetProgress(userID, "item-1")
	if err != nil {
		t.Fatalf("GetProgress: %v", err)
	}
	return p
}

// TestMissingPositionDoesNotOverwriteSeed: a progress event without
// PositionTicks keeps the stored value instead of resetting it to zero.
func TestMissingPositionDoesNotOverwriteSeed(t *testing.T) {
	app, user := s9App(t)
	req := s9Request(user)
	mustSetPosition(t, app, user, 5000)

	app.recordSessionToWatchStore(req, "item-1", s9RequestBody(map[string]any{
		"RunTimeTicks": int64(3_600_000_000),
	}), "", false)
	if got := s9Progress(t, app, user); got == nil || got.PositionTicks != 5000 {
		t.Fatalf("missing position must keep the stored value, got %#v", got)
	}
}

func mustSetPosition(t *testing.T, app *App, user string, position int64) {
	t.Helper()
	if err := app.WatchStore.RecordProgress(&WatchProgress{
		ProxyUserID: user, VirtualItemID: "item-1", PositionTicks: position,
	}); err != nil {
		t.Fatalf("seed position: %v", err)
	}
}

// TestExplicitZeroPositionApplies: an explicit 0 is a real position (rewind /
// restart), not an absent field.
func TestExplicitZeroPositionApplies(t *testing.T) {
	app, user := s9App(t)
	req := s9Request(user)
	mustSetPosition(t, app, user, 5000)

	app.recordSessionToWatchStore(req, "item-1", s9RequestBody(map[string]any{
		"PositionTicks": float64(0),
	}), "", false)
	if got := s9Progress(t, app, user); got == nil || got.PositionTicks != 0 {
		t.Fatalf("explicit 0 must apply, got %#v", got)
	}
}

// TestNegativePositionRejected validates shape over value: a negative or
// malformed value is absent data, not "position 0".
func TestNegativePositionRejected(t *testing.T) {
	app, user := s9App(t)
	req := s9Request(user)
	mustSetPosition(t, app, user, 5000)

	app.recordSessionToWatchStore(req, "item-1", s9RequestBody(map[string]any{
		"PositionTicks": "not a number",
	}), "", false)
	if got := s9Progress(t, app, user); got == nil || got.PositionTicks != 5000 {
		t.Fatalf("malformed position must be ignored, got %#v", got)
	}
	app.recordSessionToWatchStore(req, "item-1", s9RequestBody(map[string]any{
		"PositionTicks": -42,
	}), "", false)
	if got := s9Progress(t, app, user); got == nil || got.PositionTicks != 5000 {
		t.Fatalf("negative position must be ignored, got %#v", got)
	}
}

// TestStoppedNearEndCompletes: a valid stop >= 90% of runtime still marks the
// item played and resets the position (existing rule, S9 keeps it).
func TestStoppedNearEndCompletes(t *testing.T) {
	app, user := s9App(t)
	req := s9Request(user)
	if err := app.WatchStore.RecordProgress(&WatchProgress{
		ProxyUserID: user, VirtualItemID: "item-1",
		PositionTicks: 950, RuntimeTicks: 1000,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	app.recordSessionToWatchStore(req, "item-1", s9RequestBody(map[string]any{
		"PositionTicks": float64(950),
		"RunTimeTicks":  float64(1000),
	}), "", true)
	got := s9Progress(t, app, user)
	if got == nil || !got.Played || got.PositionTicks != 0 {
		t.Fatalf("stop near end must complete: %#v", got)
	}
}

// TestStoppedWithoutPositionKeepsState: a Stopped event with no position ends
// the playback but must not zero the stored position (plan S9).
func TestStoppedWithoutPositionKeepsState(t *testing.T) {
	app, user := s9App(t)
	req := s9Request(user)
	mustSetPosition(t, app, user, 500)

	app.recordSessionToWatchStore(req, "item-1", s9RequestBody(nil), "", true)
	if got := s9Progress(t, app, user); got == nil || got.PositionTicks != 500 {
		t.Fatalf("stop without position must keep the stored position: %#v", got)
	}
}

// TestSupersededSessionGate: a later start registers a new session; a late
// progress event still carrying the old session id is dropped.
func TestSupersededSessionGate(t *testing.T) {
	app, user := s9App(t)
	req := s9Request(user)
	mustSetPosition(t, app, user, 500)

	app.sessionEvents.StartSession(user, "item-1", "sess-A")
	if !app.sessionEvents.AcceptEvent(user, "item-1", "sess-A") {
		t.Fatal("the live session must pass")
	}
	app.sessionEvents.StartSession(user, "item-1", "sess-B")
	if app.sessionEvents.AcceptEvent(user, "item-1", "sess-A") {
		t.Fatal("a superseded session must be refused")
	}
	app.recordSessionToWatchStore(req, "item-1", s9RequestBody(map[string]any{
		"SessionId":     "sess-A",
		"PositionTicks": float64(0),
	}), "", false)
	if got := s9Progress(t, app, user); got == nil || got.PositionTicks != 500 {
		t.Fatalf("late event from superseded session must not overwrite: %#v", got)
	}
	app.recordSessionToWatchStore(req, "item-1", s9RequestBody(map[string]any{
		"SessionId":     "sess-B",
		"PositionTicks": float64(120),
	}), "", false)
	if got := s9Progress(t, app, user); got == nil || got.PositionTicks != 120 {
		t.Fatalf("live session event must apply: %#v", got)
	}
}

// TestSessionTrackerExpiryAndBound: entries expire on TTL and capacity stays
// bounded by least-recently-seen eviction.
func TestSessionTrackerExpiryAndBound(t *testing.T) {
	now := time.Now()
	tracker := &sessionEventTracker{entries: map[sessionWatchKey]sessionWatchState{}, now: func() time.Time { return now }}
	tracker.StartSession("u", "i", "s1")
	now = now.Add(sessionWatchEventTTL + time.Minute)
	if !tracker.AcceptEvent("u", "i", "s2") {
		t.Fatal("after TTL expiry the next event registers as the live session")
	}
	if tracker.entries[sessionWatchKey{"u", "i"}].SessionID != "s2" {
		t.Fatalf("live session not refreshed: %#v", tracker.entries)
	}
	for i := 0; i < maxSessionWatchEntries; i++ {
		tracker.StartSession("u", "item-"+strconv.Itoa(i), "sess-"+strconv.Itoa(i))
	}
	if len(tracker.entries) > maxSessionWatchEntries {
		t.Fatalf("entry count %#v exceeds the bound", len(tracker.entries))
	}
}
