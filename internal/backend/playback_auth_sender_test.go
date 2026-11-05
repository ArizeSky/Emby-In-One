package backend

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Exercise actual API/media/resource senders with an internally authorized
// binding. This does not claim the HTTP strict ingress has been wired yet.
func TestPlaybackBoundSendersKeepIdentityAndRefreshCredentials(t *testing.T) {
	hits := 0
	wantToken := "token-A"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.UserAgent() != "fixture-A" || r.Header.Get("X-Emby-Device-Id") != "device-A" {
			t.Error("bound sender changed identity")
		}
		if r.Header.Get("X-Emby-Token") != wantToken {
			t.Error("bound sender used stale token")
		}
		if key := r.URL.Query().Get("api_key"); key != "" && key != wantToken {
			t.Error("URL/header auth snapshots diverged")
		}
		_, _ = io.WriteString(w, "{}")
	}))
	defer up.Close()
	c := newUpstreamClient(Config{}, UpstreamConfig{ID: "server", URL: up.URL, SpoofClient: "passthrough", FollowRedirects: true}, 0, nil)
	a := identitySnapshot{Source: "last-success", Headers: http.Header{"User-Agent": {"fixture-A"}, "X-Emby-Client": {"fixture-A"}, "X-Emby-Device-Id": {"device-A"}}}
	b := identitySnapshot{Source: "captured-latest", Headers: http.Header{"User-Agent": {"fixture-B"}, "X-Emby-Client": {"fixture-B"}, "X-Emby-Device-Id": {"device-B"}}}
	c.setOnlineWithIdentity(wantToken, "principal", a)
	auth := c.authSnapshot()
	owner := newPlaybackIdentityOwner("owner", "local-session")
	store := newPlaybackIdentityStore()
	binding, err := store.reserve(playbackAttemptRequest{Owner: owner, ServerID: c.ID, VirtualItemID: "item", Identity: a, Principal: auth.Compatibility.Principal, ConfigRevision: auth.Compatibility.ConfigRevision})
	if err != nil {
		t.Fatal(err)
	}
	binding, err = store.activate(binding.AttemptID, owner, "session")
	if err != nil {
		t.Fatal(err)
	}
	reqCtx := &RequestContext{Playback: &binding}
	service := NewClientIdentityService()
	service.SaveLastSuccess(c.serverKey, b.Headers)
	for _, token := range []string{"token-A", "token-refreshed"} {
		wantToken = token
		c.setOnlineWithIdentity(token, "principal", a)
		if _, err := c.RequestJSON(context.Background(), reqCtx, service, "GET", "/Items/item/PlaybackInfo", nil, nil); err != nil {
			t.Fatal(err)
		}
		resp, err := c.Stream(context.Background(), reqCtx, service, "/Videos/item/stream.mp4", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		ctx := context.WithValue(context.Background(), requestContextKey{}, reqCtx)
		resp, err = c.fetchResourceRedirectTarget(ctx, "GET", up.URL+"/Videos/item/stream.mp4?api_key=old", b.Headers)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}
	if hits != 6 {
		t.Fatalf("hits=%d", hits)
	}
	c.setOnlineWithIdentity("incompatible-token", "principal", b)
	_, err = c.Stream(context.Background(), reqCtx, service, "/Videos/item/stream.mp4", nil)
	prep, ok := asPreparationError(err)
	if !ok || prep.Kind != "playback-auth-incompatible" || hits != 6 {
		t.Fatalf("incompatible send was not rejected before network: %v", err)
	}
}
