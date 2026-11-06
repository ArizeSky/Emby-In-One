package backend

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

// Use the real strict mux, capability authentication and live authorization.
func strictPlaybackTestSender(t *testing.T, f *strictEventFixture) func(string, string, map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	capability := f.app.issuePlaybackCapability(f.binding.AttemptID, f.token)
	if capability == "" {
		t.Fatal("issue playback capability")
	}
	mux := http.NewServeMux()
	f.app.registerStrictPlaybackRoutes(mux)
	return func(method, path string, body map[string]any) *httptest.ResponseRecorder {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		target := "/eio/playback/v1/" + f.binding.AttemptID + path + "?api_key=" + url.QueryEscape(capability)
		r := httptest.NewRequest(method, target, bytes.NewReader(encoded))
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, r)
		return rr
	}
}

func requireStrictResponse(t *testing.T, response *httptest.ResponseRecorder, status int) {
	t.Helper()
	if response.Code != status {
		t.Fatalf("status=%d want=%d body=%s", response.Code, status, response.Body.String())
	}
}

func TestPlaybackSettingsReloadPreservesLoginAttestation(t *testing.T) {
	withStrictEventFixture(t, func(f *strictEventFixture) {
		send := strictPlaybackTestSender(t, f)
		before := f.app.Upstream.ClientByID(f.binding.ServerID).authSnapshot()
		if err := f.app.commitConfigSettingsOnly(f.app.ConfigStore.Snapshot()); err != nil {
			t.Fatal(err)
		}
		client := f.app.Upstream.ClientByID(f.binding.ServerID)
		after := client.authSnapshot()
		if !client.IsOnline() || before.AccessToken != after.AccessToken || before.UserID != after.UserID ||
			before.Compatibility != after.Compatibility || before.Generation != after.Generation {
			t.Fatal("settings-only reload did not preserve the complete login tuple")
		}
		requireStrictResponse(t, send("POST", "/Sessions/Playing/Progress", f.eventBody()), 204)

		// Inheriting the attestation must not certify a different configuration.
		changed := f.app.ConfigStore.Snapshot()
		changed.Upstream[0].CustomUserAgent = "changed-fixture-identity"
		if err := f.app.commitConfigSettingsOnly(changed); err != nil {
			t.Fatal(err)
		}
		requireStrictResponse(t, send("POST", "/Sessions/Playing/Progress", f.eventBody()), 409)
		if len(f.upstream.recorded()) != 1 {
			t.Fatal("incompatible configuration sent an upstream report")
		}
	})
}

func TestStrictCrossItemAliasCleanupCannotStopNewAttempt(t *testing.T) {
	withStrictEventFixture(t, func(f *strictEventFixture) {
		b := f.binding
		newer, err := f.app.PlaybackIdentities.reserve(playbackAttemptRequest{
			Owner:    newPlaybackIdentityOwner("another-local-viewer", "another-local-session"),
			ServerID: b.ServerID, VirtualItemID: f.otherItem, SourceID: b.SourceID,
			Identity: b.Identity, Principal: b.Auth.Principal, ConfigRevision: b.Auth.ConfigRevision,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.app.PlaybackIdentities.activate(newer.AttemptID, newer.Owner, b.UpstreamPlaySessionID); err != nil {
			t.Fatal(err)
		}
		send := strictPlaybackTestSender(t, f)
		requireStrictResponse(t, send("DELETE", "/Videos/ActiveEncodings", nil), 409)
		if len(f.upstream.recorded()) != 0 {
			t.Fatal("old cleanup reached the reused upstream session")
		}
		if _, err = f.app.PlaybackIdentities.resolve(newer.AttemptID, newer.Owner); err != nil {
			t.Fatal("old cleanup invalidated the new attempt")
		}
	})
}

func TestStrictPlayingItemsCleanupCannotClearNewSession(t *testing.T) {
	withStrictEventFixture(t, func(f *strictEventFixture) {
		b := f.binding
		candidate, err := f.app.PlaybackIdentities.reserve(playbackAttemptRequest{
			Owner: b.Owner, ServerID: b.ServerID, VirtualItemID: b.VirtualItemID,
			Identity: b.Identity, Principal: b.Auth.Principal, ConfigRevision: b.Auth.ConfigRevision,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.app.PlaybackIdentities.activate(candidate.AttemptID, candidate.Owner, "different-upstream-session"); err != nil {
			t.Fatal(err)
		}
		send := strictPlaybackTestSender(t, f)
		requireStrictResponse(t, send("DELETE", "/Users/"+f.user.UserID+"/PlayingItems/"+f.virtualItem, nil), 409)
		if len(f.upstream.recorded()) != 0 {
			t.Fatal("an item-scoped cleanup cleared another attempt with a different session")
		}
	})
}

func TestStrictCleanupSurvivesAggregationDisableButNotRevocation(t *testing.T) {
	withStrictEventFixture(t, func(f *strictEventFixture) {
		// A regular account exercises live per-server grants, including replay
		// authorization after an upstream cleanup has already been confirmed.
		f.token = createRegularUser(t, f.app.Handler())
		f.user = f.app.Auth.ValidateToken(f.token)
		f.owner = newPlaybackIdentityOwner(f.user.UserID, f.token)
		b := f.binding
		binding, err := f.app.PlaybackIdentities.reserve(playbackAttemptRequest{
			Owner: f.owner, ServerID: b.ServerID, VirtualItemID: b.VirtualItemID,
			SourceID: b.SourceID, Identity: b.Identity, Principal: b.Auth.Principal, ConfigRevision: b.Auth.ConfigRevision,
		})
		if err != nil {
			t.Fatal(err)
		}
		f.binding, err = f.app.PlaybackIdentities.activate(binding.AttemptID, f.owner, b.UpstreamPlaySessionID)
		if err != nil {
			t.Fatal(err)
		}
		send := strictPlaybackTestSender(t, f)
		if err := f.app.ConfigStore.Mutate(func(cfg *Config) error {
			cfg.Upstream[0].AggregationDisabled = true
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		requireStrictResponse(t, send("GET", "/Videos/"+f.virtualItem+"/stream.mp4", nil), 403)
		paths := []struct{ method, path string }{
			{"POST", "/Sessions/Playing/Stopped"},
			{"DELETE", "/Videos/ActiveEncodings"},
			{"DELETE", "/Users/" + f.user.UserID + "/PlayingItems/" + f.virtualItem},
		}
		for _, cleanup := range paths {
			requireStrictResponse(t, send(cleanup.method, cleanup.path, f.eventBody()), 204)
			requireStrictResponse(t, send(cleanup.method, cleanup.path, f.eventBody()), 204)
		}
		if len(f.upstream.recorded()) != len(paths) {
			t.Fatal("cleanup was blocked, repeated, or falsely acknowledged")
		}
		// An empty server list means unrestricted access in the existing store.
		otherGrants := []string{"another-server"}
		if err := f.app.UserStore.Update(f.user.UserID, nil, nil, nil, &otherGrants); err != nil {
			t.Fatal(err)
		}
		requireStrictResponse(t, send("POST", "/Sessions/Playing/Stopped", f.eventBody()), 403)
		f.app.Auth.RevokeToken(f.token)
		requireStrictResponse(t, send("POST", "/Sessions/Playing/Stopped", f.eventBody()), 401)
	})
}

func TestStrictFailedCleanupClosesPlaybackButAllowsRetry(t *testing.T) {
	withStrictEventFixture(t, func(f *strictEventFixture) {
		send := strictPlaybackTestSender(t, f)
		// Rejected association data must not stop an otherwise valid attempt.
		invalid := f.eventBody()
		invalid["ItemId"] = f.otherItem
		requireStrictResponse(t, send("POST", "/Sessions/Playing/Stopped", invalid), 409)
		if _, err := f.app.PlaybackIdentities.resolve(f.binding.AttemptID, f.owner); err != nil {
			t.Fatal("invalid stop changed lifecycle")
		}
		f.upstream.setStatus(503)
		requireStrictResponse(t, send("POST", "/Sessions/Playing/Stopped", f.eventBody()), 502)
		if _, err := f.app.PlaybackIdentities.resolve(f.binding.AttemptID, f.owner); !errors.Is(err, ErrPlaybackIdentityStopping) {
			t.Fatalf("unconfirmed stop left playback usable: %v", err)
		}
		for _, event := range []string{"/Sessions/Playing", "/Sessions/Playing/Progress"} {
			requireStrictResponse(t, send("POST", event, f.eventBody()), 409)
		}
		requireStrictResponse(t, send("GET", "/Videos/"+f.virtualItem+"/stream.mp4", nil), 409)
		if len(f.upstream.recorded()) != 1 {
			t.Fatal("new playback operations reached upstream after stop began")
		}
		f.upstream.setStatus(204)
		requireStrictResponse(t, send("POST", "/Sessions/Playing/Stopped", f.eventBody()), 204)
		requireStrictResponse(t, send("POST", "/Sessions/Playing/Stopped", f.eventBody()), 204)
		if len(f.upstream.recorded()) != 2 {
			t.Fatal("retry was not sent once, or duplicate ACK was sent upstream")
		}
	})
}

type strictCleanupBlockingTransport struct {
	base    http.RoundTripper
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *strictCleanupBlockingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == "/Sessions/Playing/Stopped" {
		s.once.Do(func() { close(s.entered) })
		select {
		case <-s.release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	}
	return s.base.RoundTrip(r)
}

func TestStrictStopInFlightRejectsNewReportsAndDuplicateStop(t *testing.T) {
	withStrictEventFixture(t, func(f *strictEventFixture) {
		client := f.app.Upstream.ClientByID(f.binding.ServerID)
		blocker := &strictCleanupBlockingTransport{base: client.transport, entered: make(chan struct{}), release: make(chan struct{})}
		client.transport = blocker
		var releaseOnce sync.Once
		release := func() { releaseOnce.Do(func() { close(blocker.release) }) }
		defer release()
		send := strictPlaybackTestSender(t, f)
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() { done <- send("POST", "/Sessions/Playing/Stopped", f.eventBody()) }()
		select {
		case <-blocker.entered:
		case <-time.After(5 * time.Second):
			t.Fatal("stop did not reach the isolated transport")
		}
		// A candidate cannot activate between the cleanup's conflict check and
		// the remote send, even when its alias is shared by another item/user.
		b := f.binding
		candidate, err := f.app.PlaybackIdentities.reserve(playbackAttemptRequest{
			Owner: b.Owner, ServerID: b.ServerID, VirtualItemID: f.otherItem,
			Identity: b.Identity, Principal: b.Auth.Principal, ConfigRevision: b.Auth.ConfigRevision,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.app.PlaybackIdentities.activate(candidate.AttemptID, candidate.Owner, b.UpstreamPlaySessionID); !errors.Is(err, ErrPlaybackIdentityStopping) {
			t.Fatalf("candidate activated during conflicting cleanup: %v", err)
		}
		requireStrictResponse(t, send("POST", "/Sessions/Playing/Progress", f.eventBody()), 409)
		requireStrictResponse(t, send("POST", "/Sessions/Playing/Stopped", f.eventBody()), 409)
		if len(f.upstream.recorded()) != 0 {
			t.Fatal("a new report escaped while cleanup was in flight")
		}
		release()
		select {
		case response := <-done:
			requireStrictResponse(t, response, 204)
		case <-time.After(5 * time.Second):
			t.Fatal("stop did not complete")
		}
		if len(f.upstream.recorded()) != 1 {
			t.Fatal("duplicate stop reached upstream")
		}
		if _, err = f.app.PlaybackIdentities.activate(candidate.AttemptID, candidate.Owner, b.UpstreamPlaySessionID); err != nil {
			t.Fatalf("completed cleanup still blocks the next candidate: %v", err)
		}
	})
}

func TestPlaybackCleanupUsesActivationOrderAndBoundedRetry(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	store := newPlaybackIdentityStoreWithClock(func() time.Time { return now })
	req := playbackIdentityTestRequest(playbackIdentityTestOwner("viewer", "local-session"))
	first, err := store.reserve(req)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.reserve(req)
	if err != nil {
		t.Fatal(err)
	}
	// Negotiate first, activate last: creation time cannot prove which playback
	// currently owns the upstream session. The fake clock stays identical.
	if _, err = store.activate(second.AttemptID, second.Owner, "reused-session"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.activate(first.AttemptID, first.Owner, "reused-session"); err != nil {
		t.Fatal(err)
	}
	if _, err = store.beginCleanup(second.AttemptID, second.Owner, 2); !errors.Is(err, ErrPlaybackIdentityAliasConflict) {
		t.Fatalf("creation order overrode the actual selected attempt: %v", err)
	}
	release, err := store.beginCleanup(first.AttemptID, first.Owner, 1)
	if err != nil {
		t.Fatal(err)
	}
	release()
	deadline := now.Add(store.terminalTTL)
	now = now.Add(time.Minute)
	release, err = store.beginCleanup(first.AttemptID, first.Owner, 1)
	if err != nil {
		t.Fatal(err)
	}
	release()
	now = deadline
	if _, err = store.leaseCleanup(first.AttemptID, first.Owner); !errors.Is(err, ErrPlaybackIdentityNotFound) {
		t.Fatalf("cleanup retry extended stopping retention: %v", err)
	}
}
