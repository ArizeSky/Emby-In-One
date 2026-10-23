package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type routeAccessUpstreamStub struct {
	server *httptest.Server
	mu     sync.Mutex
	paths  []string
}

func newRouteAccessUpstreamStub(t *testing.T, name string) *routeAccessUpstreamStub {
	t.Helper()
	stub := &routeAccessUpstreamStub{}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName" {
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-" + name, "User": map[string]any{"Id": "user-" + name}})
			return
		}
		stub.mu.Lock()
		stub.paths = append(stub.paths, r.URL.Path)
		stub.mu.Unlock()

		switch {
		case strings.HasSuffix(r.URL.Path, "/PlaybackInfo"):
			_ = json.NewEncoder(w).Encode(map[string]any{"PlaySessionId": "play-" + name, "MediaSources": []any{}})
		case strings.HasPrefix(r.URL.Path, "/Items/") && !strings.Contains(strings.TrimPrefix(r.URL.Path, "/Items/"), "/"):
			itemID := strings.TrimPrefix(r.URL.Path, "/Items/")
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": itemID, "Name": "copy-" + name, "Type": "Movie", "UserData": map[string]any{}})
		case strings.HasPrefix(r.URL.Path, "/Videos/"):
			_, _ = w.Write([]byte("stream-" + name))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *routeAccessUpstreamStub) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.paths = nil
}

func (s *routeAccessUpstreamStub) recordedPaths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.paths...)
}

func createRouteAccessUser(t *testing.T, handler http.Handler, adminToken, username string, servers []string) string {
	t.Helper()
	created := doAuthJSON(t, handler, http.MethodPost, "/admin/api/users", map[string]any{
		"username":       username,
		"password":       "routepass123",
		"allowedServers": servers,
	}, adminToken)
	if created.Code != http.StatusCreated {
		t.Fatalf("create user %s: status=%d body=%s", username, created.Code, created.Body.String())
	}
	return loginTokenAs(t, handler, username, "routepass123")
}

func TestResolveRouteForRequestAuthorizationFallback(t *testing.T) {
	stubA := newRouteAccessUpstreamStub(t, "a")
	stubB := newRouteAccessUpstreamStub(t, "b")
	withTempAppConfig(t, dualUpstreamConfig(stubA.server.URL, stubB.server.URL), func(app *App, handler http.Handler) {
		adminToken := loginTokenAs(t, handler, "admin", "secret")
		serverA, serverB := app.Upstream.Clients()[0].ID, app.Upstream.Clients()[1].ID
		userToken := createRouteAccessUser(t, handler, adminToken, "only-b", []string{serverB})

		virtualID := app.IDStore.GetOrCreateVirtualID("movie-a", serverA)
		app.IDStore.AssociateAdditionalInstance(virtualID, "movie-b", serverB)
		stubA.reset()
		stubB.reset()

		response := doAuthJSON(t, handler, http.MethodGet, "/Items/"+virtualID, nil, userToken)
		if response.Code != http.StatusOK {
			t.Fatalf("detail status=%d body=%s, want 200 from authorized copy B", response.Code, response.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode detail: %v", err)
		}
		if payload["Name"] != "copy-b" {
			t.Fatalf("detail Name=%v, want copy-b", payload["Name"])
		}
		if got := stubB.recordedPaths(); !containsExactPath(got, "/Items/movie-b") {
			t.Fatalf("server B requests=%v, want /Items/movie-b", got)
		}
		if got := stubA.recordedPaths(); containsExactPath(got, "/Items/movie-a") {
			t.Fatalf("unauthorized server A was queried: %v", got)
		}
		if global := app.IDStore.ResolveVirtualID(virtualID); global.ServerID != serverA || global.OriginalID != "movie-a" {
			t.Fatalf("global mapping changed: %#v, want primary A/movie-a", global)
		}
	})
}

func TestResolveRouteForRequestPrimaryOfflineFallback(t *testing.T) {
	stubA := newRouteAccessUpstreamStub(t, "a")
	stubB := newRouteAccessUpstreamStub(t, "b")
	withTempAppConfig(t, dualUpstreamConfig(stubA.server.URL, stubB.server.URL), func(app *App, handler http.Handler) {
		adminToken := loginTokenAs(t, handler, "admin", "secret")
		serverA, serverB := app.Upstream.Clients()[0].ID, app.Upstream.Clients()[1].ID
		userToken := createRouteAccessUser(t, handler, adminToken, "both-servers", []string{serverA, serverB})

		virtualID := app.IDStore.GetOrCreateVirtualID("movie-a", serverA)
		app.IDStore.AssociateAdditionalInstance(virtualID, "movie-b", serverB)
		app.Upstream.GetClient(0).setOffline("test offline")
		stubA.reset()
		stubB.reset()

		response := doAuthJSON(t, handler, http.MethodGet, "/Items/"+virtualID, nil, userToken)
		if response.Code != http.StatusOK {
			t.Fatalf("detail status=%d body=%s, want 200 from online copy B", response.Code, response.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode detail: %v", err)
		}
		if payload["Name"] != "copy-b" {
			t.Fatalf("detail Name=%v, want copy-b", payload["Name"])
		}
		if got := stubB.recordedPaths(); !containsExactPath(got, "/Items/movie-b") {
			t.Fatalf("server B requests=%v, want /Items/movie-b", got)
		}
		if got := stubA.recordedPaths(); containsExactPath(got, "/Items/movie-a") {
			t.Fatalf("offline server A was queried: %v", got)
		}
	})
}

func TestResolveRouteForRequestStatusReasons(t *testing.T) {
	stubA := newRouteAccessUpstreamStub(t, "a")
	stubB := newRouteAccessUpstreamStub(t, "b")
	withTempAppConfig(t, dualUpstreamConfig(stubA.server.URL, stubB.server.URL), func(app *App, _ http.Handler) {
		serverA, serverB := app.Upstream.Clients()[0].ID, app.Upstream.Clients()[1].ID
		virtualID := app.IDStore.GetOrCreateVirtualID("movie-a", serverA)
		app.IDStore.AssociateAdditionalInstance(virtualID, "movie-b", serverB)

		noGrant := &RequestContext{ProxyUser: &tokenInfo{Role: "user", AllowedServers: []string{"unrelated"}}}
		if resolved, status := app.resolveRouteForRequest(noGrant, virtualID); resolved != nil || status != http.StatusForbidden {
			t.Fatalf("no authorized replica: resolution=%#v status=%d, want nil/403", resolved, status)
		}

		allOffline := &RequestContext{ProxyUser: &tokenInfo{Role: "user", AllowedServers: []string{serverA, serverB}}}
		app.Upstream.GetClient(0).setOffline("test offline")
		app.Upstream.GetClient(1).setOffline("test offline")
		if resolved, status := app.resolveRouteForRequest(allOffline, virtualID); resolved != nil || status != http.StatusServiceUnavailable {
			t.Fatalf("authorized replicas offline: resolution=%#v status=%d, want nil/503", resolved, status)
		}
		if resolved, status := app.resolveRouteForRequest(allOffline, "unknown-item"); resolved != nil || status != http.StatusNotFound {
			t.Fatalf("unknown ID: resolution=%#v status=%d, want nil/404", resolved, status)
		}

		for _, tc := range []struct {
			status  int
			message string
			code    string
		}{
			{http.StatusNotFound, "Item not found", "ItemNotFound"},
			{http.StatusForbidden, "Content not available for this account", "ContentNotAvailableForThisAccount"},
			{http.StatusServiceUnavailable, "No available replica of this item is online", "NoAvailableReplicaOnline"},
		} {
			recorder := httptest.NewRecorder()
			app.writeRouteAccessError(recorder, tc.status)
			var body map[string]any
			if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode status %d error response: %v", tc.status, err)
			}
			if recorder.Code != tc.status || body["message"] != tc.message || body["code"] != tc.code {
				t.Errorf("error response status=%d body=%v, want status=%d message=%q code=%q", recorder.Code, body, tc.status, tc.message, tc.code)
			}
		}
	})
}

func TestRouteAccessExplicitMediaSourceDoesNotFallback(t *testing.T) {
	stubA := newRouteAccessUpstreamStub(t, "a")
	stubB := newRouteAccessUpstreamStub(t, "b")
	withTempAppConfig(t, dualUpstreamConfig(stubA.server.URL, stubB.server.URL), func(app *App, handler http.Handler) {
		adminToken := loginTokenAs(t, handler, "admin", "secret")
		serverA, serverB := app.Upstream.Clients()[0].ID, app.Upstream.Clients()[1].ID
		deniedToken := createRouteAccessUser(t, handler, adminToken, "only-b", []string{serverB})
		bothToken := createRouteAccessUser(t, handler, adminToken, "both-servers", []string{serverA, serverB})

		virtualID := app.IDStore.GetOrCreateVirtualID("movie-a", serverA)
		app.IDStore.AssociateAdditionalInstance(virtualID, "movie-b", serverB)
		mediaSourceID := app.IDStore.GetOrCreateVirtualID("source-a", serverA)
		streamTarget := "/Videos/" + virtualID + "/stream.mp4?MediaSourceId=" + mediaSourceID

		assertRejectedWithoutUpstream := func(name, token string, want int) {
			t.Helper()
			stubA.reset()
			stubB.reset()
			playback := doAuthJSON(t, handler, http.MethodPost, "/Items/"+virtualID+"/PlaybackInfo", map[string]any{"MediaSourceId": mediaSourceID}, token)
			if playback.Code != want {
				t.Errorf("%s PlaybackInfo status=%d body=%s, want %d", name, playback.Code, playback.Body.String(), want)
			}
			if paths := routeMediaRequests(stubA.recordedPaths(), stubB.recordedPaths()); len(paths) != 0 {
				t.Errorf("%s PlaybackInfo unexpectedly queried upstream: %v", name, paths)
			}

			stubA.reset()
			stubB.reset()
			stream := doAuthJSON(t, handler, http.MethodGet, streamTarget, nil, token)
			if stream.Code != want {
				t.Errorf("%s stream status=%d body=%s, want %d", name, stream.Code, stream.Body.String(), want)
			}
			if paths := routeMediaRequests(stubA.recordedPaths(), stubB.recordedPaths()); len(paths) != 0 {
				t.Errorf("%s stream unexpectedly queried upstream: %v", name, paths)
			}
		}

		assertRejectedWithoutUpstream("unauthorized explicit A", deniedToken, http.StatusForbidden)
		app.Upstream.GetClient(0).setOffline("test offline")
		assertRejectedWithoutUpstream("offline explicit A", bothToken, http.StatusServiceUnavailable)
	})
}

func TestRouteAccessUnknownParentIDEmptyList(t *testing.T) {
	stubA := newRouteAccessUpstreamStub(t, "a")
	stubB := newRouteAccessUpstreamStub(t, "b")
	withTempAppConfig(t, dualUpstreamConfig(stubA.server.URL, stubB.server.URL), func(_ *App, handler http.Handler) {
		adminToken := loginTokenAs(t, handler, "admin", "secret")
		response := doAuthJSON(t, handler, http.MethodGet, "/Users/u/Items?ParentId=unknown-parent", nil, adminToken)
		if response.Code != http.StatusOK {
			t.Fatalf("unknown ParentId status=%d body=%s, want empty 200", response.Code, response.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode empty list: %v", err)
		}
		items, ok := payload["Items"].([]any)
		if !ok || len(items) != 0 || payload["TotalRecordCount"] != float64(0) {
			t.Fatalf("unknown ParentId payload=%v, want empty Items and count 0", payload)
		}
	})
}

func containsExactPath(paths []string, want string) bool {
	for _, path := range paths {
		if path == want {
			return true
		}
	}
	return false
}

func routeMediaRequests(pathsA, pathsB []string) []string {
	var requests []string
	for _, path := range append(pathsA, pathsB...) {
		if strings.Contains(path, "/Items/") || strings.HasPrefix(path, "/Items/") || strings.HasPrefix(path, "/Videos/") {
			requests = append(requests, path)
		}
	}
	return requests
}
