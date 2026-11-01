package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestReviewOldSegmentCannotReclaimNewReservation(t *testing.T) {
	l := NewPlaybackLimiter()
	old := playbackSessionIdentity{SessionID: "device", PlaySessionID: "old-play"}
	l.TryReserve("u", "s", "same-item", 1)
	l.ClaimStarted("u", "s", "same-item", old)
	l.TryReserve("u", "s", "same-item", 1)
	l.EnsureStreamSlot("u", "s", "same-item", 1, old)
	l.StoppedRelease("u", "s", "same-item", old)
	if got := l.CountForServer("s"); got != 1 {
		t.Fatalf("old segment+stop deleted new reservation: count=%d", got)
	}
}

func TestReviewOldItemProgressCannotTouchNewClaim(t *testing.T) {
	l := NewPlaybackLimiter()
	old := playbackSessionIdentity{PlaySessionID: "old"}
	current := playbackSessionIdentity{PlaySessionID: "new"}
	l.TryReserve("u", "s", "old-item", 1)
	l.ClaimStarted("u", "s", "old-item", old)
	l.TryReserve("u", "s", "new-item", 1)
	l.ClaimStarted("u", "s", "new-item", current)
	key := streamKey{UserID: "u", ServerID: "s"}
	sentinel := time.Now().Add(-time.Minute)
	l.streams[key].LastHeartbeat = sentinel
	l.TouchProgress("u", "s", "old-item", old)
	if !l.streams[key].LastHeartbeat.Equal(sentinel) {
		t.Fatal("stale item progress refreshed current claim")
	}
}

func TestReviewDisjointIdentityFieldsDoNotProveOwnership(t *testing.T) {
	a := playbackSessionIdentity{SessionID: "old-device"}
	b := playbackSessionIdentity{PlaySessionID: "new-play"}
	if a.matches(b) {
		t.Fatal("disjoint nonempty identity fields are not evidence of ownership")
	}
}

// TestOwnershipSameItemReplayKeepsNewClaim is the multi-event half of the
// same-item replay case: a newer attempt on the same item owns the unit even
// though the item ID is unchanged, so the previous attempt's progress and stop
// must not refresh or release it.
func TestOwnershipSameItemReplayKeepsNewClaim(t *testing.T) {
	l := NewPlaybackLimiter()
	old := playbackSessionIdentity{SessionID: "device", PlaySessionID: "old-play"}
	current := playbackSessionIdentity{SessionID: "device", PlaySessionID: "new-play"}
	key := streamKey{UserID: "u", ServerID: "s"}

	l.TryReserve("u", "s", "same-item", 1)
	l.ClaimStarted("u", "s", "same-item", old)
	l.TryReserve("u", "s", "same-item", 1)
	if verdict := l.ClaimStarted("u", "s", "same-item", current); !verdict.Accepted {
		t.Fatalf("new same-item attempt was not accepted: %#v", verdict)
	}

	sentinel := time.Now().Add(-time.Minute)
	l.streams[key].LastHeartbeat = sentinel
	if verdict := l.TouchProgress("u", "s", "same-item", old); verdict.LocalProgress {
		t.Fatalf("old attempt progress still writes local state: %#v", verdict)
	}
	if !l.streams[key].LastHeartbeat.Equal(sentinel) {
		t.Fatal("old attempt progress refreshed the new claim")
	}
	if verdict := l.StoppedRelease("u", "s", "same-item", old); verdict.Release {
		t.Fatalf("old attempt stop released the new claim: %#v", verdict)
	}
	if got := l.CountForServer("s"); got != 1 {
		t.Fatalf("old same-item stop deleted the new unit: count=%d", got)
	}

	if verdict := l.TouchProgress("u", "s", "same-item", current); !verdict.Accepted || !verdict.LocalProgress {
		t.Fatalf("current attempt progress was rejected: %#v", verdict)
	}
	if verdict := l.StoppedRelease("u", "s", "same-item", current); !verdict.Release {
		t.Fatalf("current attempt stop did not release its own unit: %#v", verdict)
	}
	if got := l.CountForServer("s"); got != 0 {
		t.Fatalf("count=%d after the owning stop, want 0", got)
	}
}

// TestOwnershipVerifiedStopReleases covers the normal path plus the legacy
// anonymous path that must keep working for clients without any identity.
func TestOwnershipVerifiedStopReleases(t *testing.T) {
	l := NewPlaybackLimiter()
	identity := playbackSessionIdentity{SessionID: "device", PlaySessionID: "play"}
	l.TryReserve("u", "s", "item", 1)
	l.ClaimStarted("u", "s", "item", identity)
	if verdict := l.StoppedRelease("u", "s", "item", identity); !verdict.Release {
		t.Fatalf("verified stop did not release: %#v", verdict)
	}
	if got := l.CountForServer("s"); got != 0 {
		t.Fatalf("verified stop left count=%d, want 0", got)
	}

	// A unit created by the legacy stream path has no claim history; its
	// anonymous Stop keeps the historical release semantics.
	if !l.TryStart("v", "s", "item", 1) {
		t.Fatal("legacy stream start rejected")
	}
	l.Stop("v", "s")
	if got := l.CountForServer("s"); got != 0 {
		t.Fatalf("legacy anonymous stop left count=%d, want 0", got)
	}
}

func TestOfflineStoppedHandlerFinishesOnlyOwnedUnit(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName" {
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-a", "User": map[string]any{"Id": "user-a"}})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	withTempAppConfig(t, playbackLimiterConfig(upstream.URL), func(app *App, handler http.Handler) {
		userToken := createRegularUser(t, handler)
		userID := watchUserID(t, app, "child")
		serverID := app.Upstream.Clients()[0].ID
		virtualItem := app.IDStore.GetOrCreateVirtualID("item-a", serverID)
		owned := playbackSessionIdentity{SessionID: "device", PlaySessionID: "play"}

		app.PlaybackLimiter.TryReserve(userID, serverID, virtualItem, 1)
		app.PlaybackLimiter.ClaimStarted(userID, serverID, virtualItem, owned)
		app.Upstream.Clients()[0].setOffline("test offline")

		// A stop that cannot prove ownership must not delete the unit, even
		// though the upstream is unavailable and nothing can be forwarded.
		foreign := map[string]any{
			"ItemId": virtualItem, "SessionId": "device", "PlaySessionId": "other-play",
			"PositionTicks": 12345, "RunTimeTicks": 100000,
		}
		if rr := doAuthJSON(t, handler, http.MethodPost, "/Sessions/Playing/Stopped", foreign, userToken); rr.Code != http.StatusNoContent {
			t.Fatalf("foreign offline stop status = %d, body=%s", rr.Code, rr.Body.String())
		}
		if got := app.PlaybackLimiter.CountForServer(serverID); got != 1 {
			t.Fatalf("foreign offline stop deleted the owned unit: count=%d", got)
		}

		// The owning stop must finish locally: release the unit and keep the
		// last position even though the stop never reached the upstream.
		body := map[string]any{
			"ItemId": virtualItem, "SessionId": owned.SessionID, "PlaySessionId": owned.PlaySessionID,
			"PositionTicks": 12345, "RunTimeTicks": 100000,
		}
		if rr := doAuthJSON(t, handler, http.MethodPost, "/Sessions/Playing/Stopped", body, userToken); rr.Code != http.StatusNoContent {
			t.Fatalf("owned offline stop status = %d, body=%s", rr.Code, rr.Body.String())
		}
		if got := app.PlaybackLimiter.CountForServer(serverID); got != 0 {
			t.Fatalf("owned offline stop stranded the unit: count=%d", got)
		}
		progress, err := app.WatchStore.GetProgress(userID, virtualItem)
		if err != nil || progress == nil {
			t.Fatalf("owned offline stop did not record local progress: progress=%#v err=%v", progress, err)
		}
		if progress.PositionTicks != 12345 {
			t.Fatalf("local position = %d, want 12345", progress.PositionTicks)
		}
	})
}

func TestOfflineStoppedHandlerProtectsUnitWithoutWatchStore(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName" {
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-a", "User": map[string]any{"Id": "user-a"}})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	withTempAppConfig(t, playbackLimiterConfig(upstream.URL), func(app *App, handler http.Handler) {
		userToken := createRegularUser(t, handler)
		userID := watchUserID(t, app, "child")
		serverID := app.Upstream.Clients()[0].ID
		virtualItem := app.IDStore.GetOrCreateVirtualID("item-a", serverID)
		owned := playbackSessionIdentity{SessionID: "device", PlaySessionID: "play"}

		app.PlaybackLimiter.TryReserve(userID, serverID, virtualItem, 1)
		app.PlaybackLimiter.ClaimStarted(userID, serverID, virtualItem, owned)
		app.WatchStore = nil
		app.Upstream.Clients()[0].setOffline("test offline")

		foreign := map[string]any{"ItemId": virtualItem, "SessionId": "device", "PlaySessionId": "other-play"}
		if rr := doAuthJSON(t, handler, http.MethodPost, "/Sessions/Playing/Stopped", foreign, userToken); rr.Code != http.StatusNoContent {
			t.Fatalf("foreign offline stop status = %d", rr.Code)
		}
		if got := app.PlaybackLimiter.CountForServer(serverID); got != 1 {
			t.Fatalf("unit protection depended on the watch store: count=%d", got)
		}

		body := map[string]any{"ItemId": virtualItem, "SessionId": owned.SessionID, "PlaySessionId": owned.PlaySessionID}
		if rr := doAuthJSON(t, handler, http.MethodPost, "/Sessions/Playing/Stopped", body, userToken); rr.Code != http.StatusNoContent {
			t.Fatalf("owned offline stop status = %d", rr.Code)
		}
		if got := app.PlaybackLimiter.CountForServer(serverID); got != 0 {
			t.Fatalf("owned offline stop stranded the unit: count=%d", got)
		}
	})
}

// TestReviewLateStartedCannotReclaimNewReservation is the parent-review hole:
// a late Started of the superseded attempt carries the same item ID and the
// same device SessionId, so only the PlaySessionId tells the two attempts
// apart. It must not re-claim the unit a newer reservation owns.
func TestReviewLateStartedCannotReclaimNewReservation(t *testing.T) {
	l := NewPlaybackLimiter()
	old := playbackSessionIdentity{SessionID: "device", PlaySessionID: "old-play"}
	current := playbackSessionIdentity{SessionID: "device", PlaySessionID: "new-play"}
	key := streamKey{UserID: "u", ServerID: "s"}

	l.TryReserve("u", "s", "same-item", 1)
	if verdict := l.ClaimStarted("u", "s", "same-item", old); !verdict.Accepted {
		t.Fatalf("first start not accepted: %#v", verdict)
	}
	l.TryReserve("u", "s", "same-item", 1)

	if verdict := l.ClaimStarted("u", "s", "same-item", old); verdict.Accepted || verdict.LocalProgress {
		t.Fatalf("late superseded start re-claimed the new reservation: %#v", verdict)
	}
	if l.streams[key].liveClaim() {
		t.Fatalf("superseded identity owns the new reservation: %#v", l.streams[key].Claim)
	}

	// The current attempt's own start must still be able to claim the unit.
	if verdict := l.ClaimStarted("u", "s", "same-item", current); !verdict.Accepted || !verdict.LocalProgress {
		t.Fatalf("legitimate new start was rejected: %#v", verdict)
	}
	if !l.streams[key].liveClaim() || !l.streams[key].Claim.matches(current) {
		t.Fatalf("new attempt does not own the unit: %#v", l.streams[key])
	}
	if verdict := l.StoppedRelease("u", "s", "same-item", old); verdict.Release {
		t.Fatalf("superseded stop released the new attempt's unit: %#v", verdict)
	}
	if verdict := l.StoppedRelease("u", "s", "same-item", current); !verdict.Release {
		t.Fatalf("owning stop did not release: %#v", verdict)
	}
}

// TestReviewBoundPlaySessionProvesNewAttempt covers the precise rule: the
// PlaySessionId a PlaybackInfo response handed out is positive proof of the
// current attempt, so a PlaybackInfo renegotiation that keeps the same
// PlaySessionId can still be claimed by its own start.
func TestReviewBoundPlaySessionProvesNewAttempt(t *testing.T) {
	l := NewPlaybackLimiter()
	identity := playbackSessionIdentity{SessionID: "device", PlaySessionID: "play"}
	key := streamKey{UserID: "u", ServerID: "s"}

	l.TryReserve("u", "s", "same-item", 1)
	l.ClaimStarted("u", "s", "same-item", identity)
	reservation, ok := l.TryReserve("u", "s", "same-item", 1)
	if !ok || !reservation.valid() {
		t.Fatalf("second reservation = %#v, ok=%v", reservation, ok)
	}
	l.BindReservationPlaySession(reservation, "play")
	if l.streams[key].liveClaim() {
		t.Fatal("a new reservation must freeze the previous claim until the new attempt starts")
	}
	if verdict := l.ClaimStarted("u", "s", "same-item", identity); !verdict.Accepted || !verdict.LocalProgress {
		t.Fatalf("bound PlaySessionId did not prove the current attempt: %#v", verdict)
	}
	if !l.streams[key].liveClaim() {
		t.Fatal("bound attempt did not become the live claim")
	}
	if verdict := l.StoppedRelease("u", "s", "same-item", identity); !verdict.Release {
		t.Fatalf("owning stop did not release: %#v", verdict)
	}
}

// TestReviewProgressCannotReclaimFrozenClaim pins the conservative rule for the
// weaker event: progress never takes a unit back from the reservation that
// superseded it, and never refreshes it.
func TestReviewProgressCannotReclaimFrozenClaim(t *testing.T) {
	l := NewPlaybackLimiter()
	old := playbackSessionIdentity{SessionID: "device", PlaySessionID: "old-play"}
	current := playbackSessionIdentity{SessionID: "device", PlaySessionID: "new-play"}
	key := streamKey{UserID: "u", ServerID: "s"}

	l.TryReserve("u", "s", "same-item", 1)
	l.ClaimStarted("u", "s", "same-item", old)
	reservation, _ := l.TryReserve("u", "s", "same-item", 1)
	l.BindReservationPlaySession(reservation, current.PlaySessionID)
	sentinel := time.Now().Add(-time.Minute)
	l.streams[key].LastHeartbeat = sentinel

	for _, identity := range []playbackSessionIdentity{old, current} {
		if verdict := l.TouchProgress("u", "s", "same-item", identity); verdict.Accepted || verdict.LocalProgress {
			t.Fatalf("progress re-claimed a frozen claim (%#v): %#v", identity, verdict)
		}
	}
	if !l.streams[key].LastHeartbeat.Equal(sentinel) {
		t.Fatal("progress refreshed a unit whose claim is frozen")
	}
	if l.streams[key].liveClaim() {
		t.Fatal("progress made a frozen claim live")
	}
	// The start of the bound attempt still owns the unit, then releases it.
	if verdict := l.ClaimStarted("u", "s", "same-item", current); !verdict.Accepted {
		t.Fatalf("bound start rejected: %#v", verdict)
	}
	if verdict := l.StoppedRelease("u", "s", "same-item", current); !verdict.Release {
		t.Fatalf("owning stop did not release: %#v", verdict)
	}
}

// TestReviewStreamSlotNeedsBoundPlaySession pins that a per-resource segment
// cannot recapture a frozen claim either: only the PlaySessionId the
// reservation handed out can claim the unit, and a stale same-item segment
// must not.
func TestReviewStreamSlotNeedsBoundPlaySession(t *testing.T) {
	l := NewPlaybackLimiter()
	old := playbackSessionIdentity{PlaySessionID: "old-play"}
	current := playbackSessionIdentity{PlaySessionID: "new-play"}
	key := streamKey{UserID: "u", ServerID: "s"}

	l.TryReserve("u", "s", "same-item", 1)
	l.ClaimStarted("u", "s", "same-item", playbackSessionIdentity{SessionID: "device", PlaySessionID: "old-play"})
	reservation, _ := l.TryReserve("u", "s", "same-item", 1)
	l.BindReservationPlaySession(reservation, current.PlaySessionID)

	if _, ok := l.EnsureStreamSlot("u", "s", "same-item", 1, old); !ok {
		t.Fatal("stale segment must still be served")
	}
	if l.streams[key].liveClaim() {
		t.Fatalf("stale segment recaptured the frozen claim: %#v", l.streams[key].Claim)
	}
	if _, ok := l.EnsureStreamSlot("u", "s", "same-item", 1, current); !ok {
		t.Fatal("current segment was refused")
	}
	if !l.streams[key].liveClaim() || !l.streams[key].Claim.matches(current) {
		t.Fatalf("current segment did not claim the reservation: %#v", l.streams[key])
	}
	if verdict := l.StoppedRelease("u", "s", "same-item", old); verdict.Release {
		t.Fatalf("superseded stop released the segment-owned unit: %#v", verdict)
	}
	if verdict := l.StoppedRelease("u", "s", "same-item", current); !verdict.Release {
		t.Fatalf("owning stop did not release: %#v", verdict)
	}
}

// TestReviewLateStartedCannotClaimNewPlaybackInfo is the handler-level proof:
// the reservation is bound to the PlaySessionId the PlaybackInfo response hands
// out, and the superseded attempt's late Started (same item, old
// PlaySessionId) cannot take that new unit.
func TestReviewLateStartedCannotClaimNewPlaybackInfo(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-a", "User": map[string]any{"Id": "user-a"}})
		case strings.HasSuffix(r.URL.Path, "/PlaybackInfo"):
			playSessionID := "play-a"
			if calls.Add(1) > 1 {
				playSessionID = "play-b"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"PlaySessionId": playSessionID,
				"MediaSources":  []any{map[string]any{"Id": "ms-a", "Name": "1080p"}},
			})
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer upstream.Close()

	withTempAppConfig(t, playbackLimiterConfig(upstream.URL), func(app *App, handler http.Handler) {
		userToken := createRegularUser(t, handler)
		userID := watchUserID(t, app, "child")
		serverID := app.Upstream.Clients()[0].ID
		virtualItem := app.IDStore.GetOrCreateVirtualID("item-a", serverID)

		playbackInfo := func() string {
			rr := doAuthJSON(t, handler, http.MethodGet, "/Items/"+virtualItem+"/PlaybackInfo", nil, userToken)
			if rr.Code != http.StatusOK {
				t.Fatalf("playback info status = %d, body=%s", rr.Code, rr.Body.String())
			}
			var info map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &info); err != nil {
				t.Fatalf("decode playback info: %v", err)
			}
			playSessionID, _ := info["PlaySessionId"].(string)
			if playSessionID == "" {
				t.Fatalf("playback info carried no PlaySessionId: %#v", info)
			}
			return playSessionID
		}
		started := func(playSessionID string) {
			body := map[string]any{"ItemId": virtualItem, "SessionId": "device", "PlaySessionId": playSessionID}
			if rr := doAuthJSON(t, handler, http.MethodPost, "/Sessions/Playing", body, userToken); rr.Code != http.StatusNoContent {
				t.Fatalf("started status = %d, body=%s", rr.Code, rr.Body.String())
			}
		}

		oldPlaySession := playbackInfo()
		started(oldPlaySession)
		key := streamKey{UserID: userID, ServerID: serverID}
		if !app.PlaybackLimiter.streams[key].liveClaim() {
			t.Fatal("first attempt did not claim the unit")
		}

		currentPlaySession := playbackInfo()
		// The superseded attempt's late Started keeps the same item and device,
		// and only its PlaySessionId differs.
		if oldPlaySession == currentPlaySession {
			t.Fatalf("upstream returned the same PlaySessionId twice: %q", oldPlaySession)
		}
		sentinel := time.Now().Add(-time.Minute)
		app.PlaybackLimiter.streams[key].LastHeartbeat = sentinel
		started(oldPlaySession)
		lateEntry := app.PlaybackLimiter.streams[key]
		if lateEntry.liveClaim() || lateEntry.ClaimVersion == lateEntry.Version {
			t.Fatalf("late start of the superseded attempt claimed the new reservation: %#v", lateEntry)
		}
		if lateEntry.Claim.PlaySessionID != oldPlaySession {
			t.Fatalf("late start replaced the frozen claim: %#v", lateEntry.Claim)
		}
		if !lateEntry.LastHeartbeat.Equal(sentinel) || !lateEntry.Reserved {
			t.Fatalf("late start refreshed or un-reserved the new reservation: %#v", lateEntry)
		}

		started(currentPlaySession)
		if entry := app.PlaybackLimiter.streams[key]; !entry.liveClaim() || entry.Claim.PlaySessionID != currentPlaySession {
			t.Fatalf("current attempt did not claim its reservation: %#v", entry)
		}
		if got := app.PlaybackLimiter.CountForServer(serverID); got != 1 {
			t.Fatalf("CountForServer = %d, want 1", got)
		}
	})
}

// TestReviewRejectedStartedKeepsTrackedSession is the handler-level regression
// for the session-identity tracker: a Started the limiter refuses (it belongs to
// the attempt a newer claim superseded) must not become the tracked live
// session. Registering it anyway made every later progress of the accepted
// attempt look superseded, so the local watch store silently stopped receiving
// the new position.
func TestReviewRejectedStartedKeepsTrackedSession(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName" {
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-a", "User": map[string]any{"Id": "user-a"}})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	withTempAppConfig(t, playbackLimiterConfig(upstream.URL), func(app *App, handler http.Handler) {
		userToken := createRegularUser(t, handler)
		userID := watchUserID(t, app, "child")
		serverID := app.Upstream.Clients()[0].ID
		virtualItem := app.IDStore.GetOrCreateVirtualID("item-a", serverID)

		start := func(identity map[string]any) {
			body := map[string]any{"ItemId": virtualItem}
			for key, value := range identity {
				body[key] = value
			}
			if rr := doAuthJSON(t, handler, http.MethodPost, "/Sessions/Playing", body, userToken); rr.Code != http.StatusNoContent {
				t.Fatalf("started status = %d, body=%s", rr.Code, rr.Body.String())
			}
		}
		trackedSession := func() string {
			app.sessionEvents.mu.Lock()
			defer app.sessionEvents.mu.Unlock()
			return app.sessionEvents.entries[sessionWatchKey{UserID: userID, ItemID: virtualItem}].SessionID
		}

		// The newer attempt owns the unit through its reservation and its start.
		app.PlaybackLimiter.TryReserve(userID, serverID, virtualItem, 1)
		start(map[string]any{"SessionId": "device-new", "PlaySessionId": "play-new"})
		if got := trackedSession(); got != "device-new" {
			t.Fatalf("accepted start tracked %q, want device-new", got)
		}

		// The superseded attempt's late start carries the same item but a
		// different session identity, so the limiter refuses it. It must not
		// replace the tracked session either.
		start(map[string]any{"SessionId": "device-old", "PlaySessionId": "play-old"})
		if got := trackedSession(); got != "device-new" {
			t.Fatalf("rejected start replaced the tracked session with %q", got)
		}
		if got := app.PlaybackLimiter.CountForServer(serverID); got != 1 {
			t.Fatalf("rejected start changed the unit count: %d", got)
		}

		// The accepted attempt's own progress must still reach the local store.
		progressBody := map[string]any{
			"ItemId": virtualItem, "SessionId": "device-new", "PlaySessionId": "play-new",
			"PositionTicks": 5000, "RunTimeTicks": 100000,
		}
		if rr := doAuthJSON(t, handler, http.MethodPost, "/Sessions/Playing/Progress", progressBody, userToken); rr.Code != http.StatusNoContent {
			t.Fatalf("progress status = %d, body=%s", rr.Code, rr.Body.String())
		}
		progress, err := app.WatchStore.GetProgress(userID, virtualItem)
		if err != nil || progress == nil {
			t.Fatalf("accepted progress was dropped: progress=%#v err=%v", progress, err)
		}
		if progress.PositionTicks != 5000 {
			t.Fatalf("local position = %d, want 5000", progress.PositionTicks)
		}
	})
}

func TestPlaybackSessionIdentityConflicts(t *testing.T) {
	same := playbackSessionIdentity{SessionID: "device", PlaySessionID: "play"}
	tests := []struct {
		name  string
		left  playbackSessionIdentity
		right playbackSessionIdentity
		want  bool
	}{
		{"equal", same, same, false},
		{"both empty", playbackSessionIdentity{}, playbackSessionIdentity{}, false},
		{"disjoint fields", playbackSessionIdentity{SessionID: "device"}, playbackSessionIdentity{PlaySessionID: "play"}, false},
		{"same session only", playbackSessionIdentity{SessionID: "device"}, same, false},
		{"same play session only", playbackSessionIdentity{PlaySessionID: "play"}, same, false},
		{"different session", playbackSessionIdentity{SessionID: "other", PlaySessionID: "play"}, same, true},
		{"different play session", playbackSessionIdentity{SessionID: "device", PlaySessionID: "other"}, same, true},
		{"one missing on each side", playbackSessionIdentity{SessionID: "device"}, playbackSessionIdentity{SessionID: "device"}, false},
	}
	for _, tc := range tests {
		if got := tc.left.conflicts(tc.right); got != tc.want {
			t.Fatalf("%s: conflicts = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestReviewBoundReservationRejectsAnonymousRefresh covers the parent-review
// hole: a bound reservation without a claim must not be refreshed or written to
// local state by an anonymous Started/Progress, just like an anonymous stop.
func TestReviewBoundReservationRejectsAnonymousRefresh(t *testing.T) {
	l := NewPlaybackLimiter()
	reservation, ok := l.TryReserve("u", "s", "item", 1)
	if !ok {
		t.Fatal("reservation rejected")
	}
	l.BindReservationPlaySession(reservation, "play")
	entry := l.streams[streamKey{UserID: "u", ServerID: "s"}]
	sentinel := time.Now().Add(-time.Minute)
	entry.LastHeartbeat = sentinel

	for name, verdict := range map[string]playbackSessionVerdict{
		"started":  l.ClaimStarted("u", "s", "item", playbackSessionIdentity{}),
		"progress": l.TouchProgress("u", "s", "item", playbackSessionIdentity{}),
	} {
		if verdict.Accepted || verdict.LocalProgress {
			t.Fatalf("anonymous %s bypassed the reservation binding: %#v", name, verdict)
		}
	}
	if !entry.LastHeartbeat.Equal(sentinel) || !entry.Reserved {
		t.Fatalf("anonymous event refreshed the bound reservation: %#v", entry)
	}
	if verdict := l.ClaimStarted("u", "s", "item", playbackSessionIdentity{PlaySessionID: "play"}); !verdict.Accepted {
		t.Fatalf("bound PlaySessionId did not claim the reservation: %#v", verdict)
	}
}

// TestReviewReleaseSwitchedSourceRejectsFrozenClaim covers the parent-review
// hole: a late segment matching an old frozen claim must not delete the source
// unit a newer reservation now owns, even when the target version makes the
// refVersion check pass.
func TestReviewReleaseSwitchedSourceRejectsFrozenClaim(t *testing.T) {
	l := NewPlaybackLimiter()
	old := playbackSessionIdentity{SessionID: "device", PlaySessionID: "old-play"}
	key := streamKey{UserID: "u", ServerID: "source"}

	l.TryReserve("u", "source", "item", 1)
	l.ClaimStarted("u", "source", "item", old)
	reservation, ok := l.TryReserve("u", "source", "item", 1)
	if !ok {
		t.Fatal("replacement reservation rejected")
	}
	if l.ReleaseSwitchedSource("u", "source", "item", old, reservation.version) {
		t.Fatal("stale segment deleted the replacement reservation through its frozen claim")
	}
	if entry := l.streams[key]; entry == nil || !entry.Reserved {
		t.Fatalf("replacement reservation was lost: %#v", entry)
	}

	// The replacement's own bound PlaySessionId can still release the source.
	l.BindReservationPlaySession(reservation, "new-play")
	if !l.ReleaseSwitchedSource("u", "source", "item", playbackSessionIdentity{PlaySessionID: "new-play"}, reservation.version) {
		t.Fatal("current bound PlaySessionId could not release the switched source")
	}
}
