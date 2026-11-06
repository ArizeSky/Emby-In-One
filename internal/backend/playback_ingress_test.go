package backend

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestPlaybackCapabilityExpiryAndRevoke(t *testing.T) {
	now := time.Unix(1000, 0)
	store := newPlaybackCapabilityStoreWithClock(func() time.Time { return now })
	cap, err := store.issue("attempt", "parent")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.lookup(cap, "other"); ok {
		t.Fatal("capability crossed attempt boundary")
	}
	if parent, ok := store.lookup(cap, "attempt"); !ok || parent != "parent" {
		t.Fatal("capability did not resolve its own parent")
	}
	now = now.Add(playbackCapabilityIdleTTL + time.Second)
	if _, ok := store.lookup(cap, "attempt"); ok || store.count() != 0 {
		t.Fatal("expired capability survived")
	}
	cap, err = store.issue("attempt", "parent")
	if err != nil {
		t.Fatal(err)
	}
	if store.revokeByAttempt("attempt") != 1 {
		t.Fatal("revoke failed")
	}
	if _, ok := store.lookup(cap, "attempt"); ok {
		t.Fatal("revoked capability survived")
	}
}

func TestStrictIngressRevalidatesParentAndRestartNeverDowngrades(t *testing.T) {
	withStrictEventFixture(t, func(f *strictEventFixture) {
		cap := f.app.issuePlaybackCapability(f.binding.AttemptID, f.token)
		if cap == "" {
			t.Fatal("capability issuance failed")
		}
		dispatched := 0
		handler := f.app.withStrictPlayback(func(w http.ResponseWriter, r *http.Request) { dispatched++; w.WriteHeader(204) })
		path := "/eio/playback/v1/" + f.binding.AttemptID + "/Sessions/Playing?api_key=" + url.QueryEscape(cap)
		run := func(path string) int {
			r := httptest.NewRequest("POST", path, nil)
			r.Header.Set("X-Emby-Token", f.token) // local metadata token must not shadow capability
			w := httptest.NewRecorder()
			handler(w, r)
			return w.Code
		}
		if run(path) != 204 || dispatched != 1 {
			t.Fatal("valid capability did not dispatch")
		}
		if !f.app.Auth.RevokeToken(f.token) {
			t.Fatal("failed to revoke parent session")
		}
		if run(path) != 401 || dispatched != 1 {
			t.Fatal("revoked parent continued authorizing capability")
		}
		f.app.PlaybackCapabilities = newPlaybackCapabilityStore()
		if run(path) != 401 || dispatched != 1 {
			t.Fatal("restart downgraded a strict request")
		}
	})
}

func TestStrictLateStopWithReusedAliasDoesNotStopNewAttempt(t *testing.T) {
	withStrictEventFixture(t, func(f *strictEventFixture) {
		b := f.binding
		newer, err := f.app.PlaybackIdentities.reserve(playbackAttemptRequest{Owner: b.Owner, ServerID: b.ServerID, VirtualItemID: b.VirtualItemID, SourceID: b.SourceID, Identity: b.Identity, Principal: b.Auth.Principal, ConfigRevision: b.Auth.ConfigRevision})
		if err != nil {
			t.Fatal(err)
		}
		newer, err = f.app.PlaybackIdentities.activate(newer.AttemptID, b.Owner, b.UpstreamPlaySessionID)
		if err != nil {
			t.Fatal(err)
		}
		rr := httptest.NewRecorder()
		f.app.handleSessionPlayingStopped(rr, f.strictEventRequest(t, "POST", "/Stopped", f.eventBody()))
		if rr.Code != 409 || len(f.upstream.recorded()) != 0 {
			t.Fatal("late old stop reached shared upstream session or falsely acknowledged it")
		}
		if _, err := f.app.PlaybackIdentities.resolve(newer.AttemptID, b.Owner); err != nil {
			t.Fatal("late stop destroyed newer binding")
		}
	})
}
