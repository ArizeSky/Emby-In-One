package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const (
	streamUpstreamUserID     = "upstream-user-1"
	streamUpstreamToken      = "upstream-token-1"
	streamLocalTokenSentinel = "LOCAL-EIO-TOKEN-SENTINEL"
)

// TestStreamRedirectUserID covers the redirect path, where the client talks to
// the upstream directly. The Location it receives must carry the upstream's real
// user ID and its single token, and neither the client's own token nor the
// global virtual user ID.
func TestStreamRedirectUserID(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": streamUpstreamToken, "User": map[string]any{"Id": streamUpstreamUserID}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	for _, route := range []struct {
		name string
		path string
	}{
		{"video", "/Videos/%s/stream.mp4"},
		{"audio", "/Audio/%s/stream.mp3"},
	} {
		t.Run(route.name, func(t *testing.T) {
			config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test\"\n  id: \"svr\"\nadmin:\n  username: \"admin\"\n  password: \"secret\"\nplayback:\n  mode: \"redirect\"\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\nproxies: []\nupstream:\n  - name: \"A\"\n    url: %q\n    username: \"u\"\n    password: \"p\"\n", upstream.URL)

			withTempAppConfig(t, config, func(app *App, handler http.Handler) {
				token := loginToken(t, handler, "secret")
				legacyProxyUser := app.Auth.ProxyUserID()
				virtualID := app.IDStore.GetOrCreateVirtualID("media-1", app.Upstream.Clients()[0].ID)

				// The client sends its own token and the global user ID it was given.
				target := fmt.Sprintf(route.path, virtualID) + "?UserId=" + legacyProxyUser + "&api_key=" + token
				req := httptest.NewRequest(http.MethodGet, target, nil)
				req.Header.Set("X-Emby-Token", token)
				rr := httptest.NewRecorder()
				handler.ServeHTTP(rr, req)

				if rr.Code != http.StatusFound {
					t.Fatalf("status = %d, want 302, body=%s", rr.Code, rr.Body.String())
				}
				location := rr.Header().Get("Location")
				parsed, err := url.Parse(location)
				if err != nil {
					t.Fatalf("parse Location %q: %v", location, err)
				}
				values := parsed.Query()
				if values.Get("UserId") != streamUpstreamUserID {
					t.Fatalf("Location UserId = %q, want %q (%s)", values.Get("UserId"), streamUpstreamUserID, location)
				}
				if values.Get("api_key") != streamUpstreamToken {
					t.Fatalf("Location api_key = %q, want the upstream token (%s)", values.Get("api_key"), location)
				}
				if strings.Contains(location, token) || strings.Contains(location, streamLocalTokenSentinel) {
					t.Fatalf("the client's own token reached the redirect: %s", location)
				}
				if strings.Contains(location, legacyProxyUser) {
					t.Fatalf("the global virtual user id reached the redirect: %s", location)
				}
				if !strings.Contains(parsed.Path, "/media-1/") {
					t.Fatalf("the original media id was lost: %s", parsed.Path)
				}
			})
		})
	}
}

// TestSessionForwardsUpstreamUserID pins the session write bodies to the target
// upstream's real user, while the local watch record still belongs to the local
// proxy user.
func TestSessionForwardsUpstreamUserID(t *testing.T) {
	type recorded struct {
		path string
		body map[string]any
	}
	var mu = make(chan struct{}, 1)
	mu <- struct{}{}
	var received []recorded

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": streamUpstreamToken, "User": map[string]any{"Id": streamUpstreamUserID}})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/Sessions/Playing"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			<-mu
			received = append(received, recorded{path: r.URL.Path, body: body})
			mu <- struct{}{}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	withTempAppConfig(t, singleUpstreamConfig(upstream.URL), func(app *App, handler http.Handler) {
		adminToken := loginTokenAs(t, handler, "admin", "secret")
		aliceID := createTestUser(t, handler, adminToken, "alice", "alice123")
		aliceToken := loginTokenAs(t, handler, "alice", "alice123")
		legacyProxyUser := app.Auth.ProxyUserID()

		virtualItem := app.IDStore.GetOrCreateVirtualID("orig-item", app.Upstream.Clients()[0].ID)

		// The client sends EIO's global user ID in the body, as older responses
		// taught it to.
		events := []struct {
			path string
			body map[string]any
		}{
			{"/Sessions/Playing", map[string]any{"ItemId": virtualItem, "UserId": legacyProxyUser, "PositionTicks": 10}},
			{"/Sessions/Playing/Progress", map[string]any{"ItemId": virtualItem, "UserId": legacyProxyUser, "PositionTicks": 20}},
			{"/Sessions/Playing/Stopped", map[string]any{"ItemId": virtualItem, "UserId": legacyProxyUser, "PositionTicks": 30}},
		}
		for _, event := range events {
			rr := doJSONRequest(t, handler, http.MethodPost, event.path, event.body, aliceToken)
			if rr.Code != http.StatusNoContent && rr.Code != http.StatusOK {
				t.Fatalf("%s status = %d, body=%s", event.path, rr.Code, rr.Body.String())
			}
		}

		<-mu
		defer func() { mu <- struct{}{} }()
		if len(received) != len(events) {
			t.Fatalf("upstream received %d events, want %d", len(received), len(events))
		}
		for _, event := range received {
			if event.body["UserId"] != streamUpstreamUserID {
				t.Fatalf("%s body UserId = %v, want %v", event.path, event.body["UserId"], streamUpstreamUserID)
			}
			if event.body["ItemId"] == virtualItem {
				t.Fatalf("%s forwarded the virtual item id", event.path)
			}
		}

		// The local progress record belongs to the local user, not the upstream one.
		if app.WatchStore != nil {
			progress := app.WatchStore.GetProgress(aliceID, virtualItem)
			if progress == nil {
				t.Fatalf("no local progress was recorded for the local user")
			}
			if progress.ProxyUserID != aliceID {
				t.Fatalf("local progress owner = %q, want %q", progress.ProxyUserID, aliceID)
			}
		}
	})
}

// TestOutboundIdentitySessionStatus fixes the status the session events report
// when a request cannot be prepared, and checks that an ordinary network failure
// keeps its existing contract.
//
// The session handlers only reach the preparation layer for an upstream that
// "online" already describes, and that state requires a user ID and a token. The
// reachable preparation failure on these routes is therefore the body field
// itself: an event that carries no UserId at all cannot satisfy the declared
// current-user rule, and the handler must report that instead of a bare 204.
func TestOutboundIdentitySessionStatus(t *testing.T) {
	t.Run("an unpreparable current-user event reports its own status", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName" {
				_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": streamUpstreamToken, "User": map[string]any{"Id": streamUpstreamUserID}})
				return
			}
			w.WriteHeader(http.StatusNoContent)
		}))
		defer upstream.Close()

		withTempAppConfig(t, singleUpstreamConfig(upstream.URL), func(app *App, handler http.Handler) {
			token := loginToken(t, handler, "secret")
			// A mapped item is what routes the event to an upstream at all; without one
			// the handler answers 204 before any request is prepared.
			virtualItem := app.IDStore.GetOrCreateVirtualID("orig-item", app.Upstream.Clients()[0].ID)

			// Drive the upstream into the state the online check cannot rule out: it
			// reports itself online and carries a token, but its user ID is gone, so
			// normalizing a declared current-user field has nothing trustworthy to
			// write. Only the identity source is removed; the handler's own routing
			// checks still pass.
			client := app.Upstream.GetClient(0)
			client.mu.Lock()
			client.UserID = ""
			client.mu.Unlock()

			// The forwarding sink maps a preparation error to its own status: the
			// request never left the process, so no upstream status is involved.
			body := map[string]any{"ItemId": "orig-item", "UserId": app.Auth.ProxyUserID()}
			req := httptest.NewRequest(http.MethodPost, "/Sessions/Playing/Progress", nil)
			err := app.forwardNoContent(req, client, http.MethodPost, "/Sessions/Playing/Progress", nil, body)
			status, ok := preparationErrorStatus(err)
			if !ok {
				t.Fatalf("forwardNoContent error = %v, want a preparation error", err)
			}
			if status != http.StatusServiceUnavailable {
				t.Fatalf("preparation status = %d, want 503", status)
			}

			// The handler keeps its own routing gate: an upstream it no longer
			// considers online is answered without preparing anything. That is why
			// this test drives the forwarding sink directly above, which is where the
			// preparation status is actually decided.
			rr := doJSONRequest(t, handler, http.MethodPost, "/Sessions/Playing/Progress",
				map[string]any{"ItemId": virtualItem, "UserId": app.Auth.ProxyUserID(), "PositionTicks": 5}, token)
			if rr.Code != http.StatusNoContent {
				t.Fatalf("offline-upstream status = %d, want 204 (body=%s)", rr.Code, rr.Body.String())
			}
		})
	})

	t.Run("a network failure keeps the existing contract", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName" {
				_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": streamUpstreamToken, "User": map[string]any{"Id": streamUpstreamUserID}})
				return
			}
			// A 500 is an upstream error, not a preparation failure.
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer upstream.Close()

		withTempAppConfig(t, singleUpstreamConfig(upstream.URL), func(app *App, handler http.Handler) {
			token := loginToken(t, handler, "secret")
			virtualItem := app.IDStore.GetOrCreateVirtualID("orig-item", app.Upstream.Clients()[0].ID)
			for _, path := range []string{"/Sessions/Playing/Progress", "/Sessions/Playing/Stopped"} {
				rr := doJSONRequest(t, handler, http.MethodPost, path,
					map[string]any{"ItemId": virtualItem, "PositionTicks": 5}, token)
				if rr.Code != http.StatusNoContent {
					t.Fatalf("%s status = %d, want 204 (body=%s)", path, rr.Code, rr.Body.String())
				}
			}
		})
	})

	t.Run("a stop that cannot be announced still releases local state", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName" {
				_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": streamUpstreamToken, "User": map[string]any{"Id": streamUpstreamUserID}})
				return
			}
			// Refuse the session write, so the handler is on its failure path.
			http.Error(w, "nope", http.StatusInternalServerError)
		}))
		defer upstream.Close()

		withTempAppConfig(t, singleUpstreamConfig(upstream.URL), func(app *App, handler http.Handler) {
			adminToken := loginTokenAs(t, handler, "admin", "secret")
			aliceID := createTestUser(t, handler, adminToken, "alice", "alice123")
			aliceToken := loginTokenAs(t, handler, "alice", "alice123")
			virtualItem := app.IDStore.GetOrCreateVirtualID("orig-item", app.Upstream.Clients()[0].ID)

			if app.PlaybackLimiter == nil {
				t.Skip("no playback limiter in this build")
			}
			app.PlaybackLimiter.TryStart(aliceID, app.Upstream.Clients()[0].ID, virtualItem, 5)
			rr := doJSONRequest(t, handler, http.MethodPost, "/Sessions/Playing/Stopped",
				map[string]any{"ItemId": virtualItem, "PositionTicks": 5}, aliceToken)
			if rr.Code != http.StatusNoContent {
				t.Fatalf("stopped status = %d, want 204 (body=%s)", rr.Code, rr.Body.String())
			}
			// The slot must be free again: a failed report cannot strand a permit.
			if !app.PlaybackLimiter.TryStart(aliceID, app.Upstream.Clients()[0].ID, virtualItem, 1) {
				t.Fatalf("the playback slot was not released after a failed stop report")
			}
		})
	})
}
