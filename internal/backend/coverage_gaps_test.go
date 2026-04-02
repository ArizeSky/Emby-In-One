package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Phase 4.1: Test multi-instance handleUserItemByID (MediaSource merge)
func TestUserItemByIDMergesMediaSourcesWithoutOrphanMappings(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-a", "User": map[string]any{"Id": "user-a"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Users/user-a/Items/orig-1":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id": "orig-1", "Name": "Movie", "Type": "Movie",
				"MediaSources": []map[string]any{{"Id": "ms-a1", "Name": "Version A"}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer primary.Close()

	secondary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-b", "User": map[string]any{"Id": "user-b"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Users/user-b/Items/orig-2":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id": "orig-2", "Name": "Movie", "Type": "Movie",
				"MediaSources": []map[string]any{{"Id": "ms-b1", "Name": "Version B"}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer secondary.Close()

	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test\"\n  id: \"svr\"\nadmin:\n  username: \"admin\"\n  password: \"secret\"\nplayback:\n  mode: \"proxy\"\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\nproxies: []\nupstream:\n  - name: \"A\"\n    url: %q\n    username: \"u\"\n    password: \"p\"\n  - name: \"B\"\n    url: %q\n    username: \"u\"\n    password: \"p\"\n", primary.URL, secondary.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")

		// Create virtual ID and associate additional instance
		virtualID := app.IDStore.GetOrCreateVirtualID("orig-1", 0)
		app.IDStore.AssociateAdditionalInstance(virtualID, "orig-2", 1)

		// Count mappings before request
		statsBefore := app.IDStore.Stats()

		rr := doJSONRequest(t, handler, http.MethodGet, "/Users/"+app.Auth.ProxyUserID()+"/Items/"+virtualID, nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
		}

		var result map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}

		// Verify MediaSources merged
		sources, ok := result["MediaSources"].([]any)
		if !ok {
			t.Fatalf("MediaSources not array: %T", result["MediaSources"])
		}
		if len(sources) != 2 {
			t.Fatalf("MediaSources count = %d, want 2", len(sources))
		}

		// Verify no excessive orphan mappings (should only create mappings for the 2 MediaSource IDs, not duplicates)
		statsAfter := app.IDStore.Stats()
		newMappings := statsAfter.MappingCount - statsBefore.MappingCount
		// We expect 2 new mappings: ms-a1 and ms-b1. Without the delete-before-rewrite fix, this would be 3+.
		if newMappings > 2 {
			t.Fatalf("orphan mappings detected: created %d new mappings (expected <= 2)", newMappings)
		}
	})
}

// Phase 4.2: Test redirect mode in video proxy
func TestVideoProxyRedirectMode(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok", "User": map[string]any{"Id": "uid"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test\"\n  id: \"svr\"\nadmin:\n  username: \"admin\"\n  password: \"secret\"\nplayback:\n  mode: \"redirect\"\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\nproxies: []\nupstream:\n  - name: \"A\"\n    url: %q\n    username: \"u\"\n    password: \"p\"\n", upstream.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		virtualID := app.IDStore.GetOrCreateVirtualID("video-1", 0)

		req := httptest.NewRequest(http.MethodGet, "/Videos/"+virtualID+"/stream.mp4", nil)
		req.Header.Set("X-Emby-Token", token)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		// Redirect mode should return 302
		if rr.Code != http.StatusFound {
			t.Fatalf("status = %d, want 302, body=%s", rr.Code, rr.Body.String())
		}

		location := rr.Header().Get("Location")
		if location == "" {
			t.Fatal("missing Location header")
		}
		if !strings.Contains(location, "/Videos/video-1/stream.mp4") {
			t.Fatalf("redirect URL does not contain original ID path: %s", location)
		}
		if !strings.Contains(location, "api_key=") {
			t.Fatalf("redirect URL missing api_key: %s", location)
		}
	})
}

// Phase 4.3: Test session playing ID translation
func TestSessionPlayingTranslatesVirtualIDs(t *testing.T) {
	var receivedBody map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok", "User": map[string]any{"Id": "uid"}})
		case r.Method == http.MethodPost && r.URL.Path == "/Sessions/Playing":
			defer r.Body.Close()
			_ = json.NewDecoder(r.Body).Decode(&receivedBody)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test\"\n  id: \"svr\"\nadmin:\n  username: \"admin\"\n  password: \"secret\"\nplayback:\n  mode: \"proxy\"\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\nproxies: []\nupstream:\n  - name: \"A\"\n    url: %q\n    username: \"u\"\n    password: \"p\"\n", upstream.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		virtualItemID := app.IDStore.GetOrCreateVirtualID("real-item-1", 0)
		virtualMediaID := app.IDStore.GetOrCreateVirtualID("real-ms-1", 0)

		body := map[string]any{
			"ItemId":        virtualItemID,
			"MediaSourceId": virtualMediaID,
		}
		rr := doJSONRequest(t, handler, http.MethodPost, "/Sessions/Playing", body, token)

		// Should forward to upstream (204 or 200)
		if rr.Code != http.StatusNoContent && rr.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
		}

		// Verify upstream received original IDs, not virtual ones
		if receivedBody != nil {
			if itemID, _ := receivedBody["ItemId"].(string); itemID == virtualItemID {
				t.Fatalf("upstream received virtual ItemId %q instead of original", itemID)
			}
		}
	})
}

// Phase 4.4: Test resolveMediaSourceInPath for subtitle paths
func TestSubtitlePathResolvesMediaSourceID(t *testing.T) {
	var receivedPath string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok", "User": map[string]any{"Id": "uid"}})
		case strings.Contains(r.URL.Path, "/Videos/") && strings.Contains(r.URL.Path, "/Subtitles/"):
			receivedPath = r.URL.Path
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("subtitle content"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test\"\n  id: \"svr\"\nadmin:\n  username: \"admin\"\n  password: \"secret\"\nplayback:\n  mode: \"proxy\"\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\nproxies: []\nupstream:\n  - name: \"A\"\n    url: %q\n    username: \"u\"\n    password: \"p\"\n", upstream.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		virtualItemID := app.IDStore.GetOrCreateVirtualID("real-video", 0)
		virtualMSID := app.IDStore.GetOrCreateVirtualID("real-mediasource", 0)

		req := httptest.NewRequest(http.MethodGet, "/Videos/"+virtualItemID+"/"+virtualMSID+"/Subtitles/0/Stream.srt", nil)
		req.Header.Set("X-Emby-Token", token)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		// Should proxy the subtitle (200) or at least not 404 due to unresolved IDs
		if rr.Code == http.StatusNotFound && receivedPath == "" {
			t.Fatalf("subtitle request not forwarded upstream, status=%d", rr.Code)
		}

		// If forwarded, verify the path has original IDs
		if receivedPath != "" {
			if strings.Contains(receivedPath, virtualItemID) {
				t.Fatalf("upstream received virtual item ID in path: %s", receivedPath)
			}
			if strings.Contains(receivedPath, virtualMSID) {
				t.Fatalf("upstream received virtual mediasource ID in path: %s", receivedPath)
			}
		}
	})
}

// Phase 4.5: Test token persistence (tokens never expire, only removed by revocation)
func TestTokenNeverExpires(t *testing.T) {
	config := "server:\n  port: 8096\n  name: \"Test\"\n  id: \"svr\"\nadmin:\n  username: \"admin\"\n  password: \"secret\"\nplayback:\n  mode: \"proxy\"\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\nproxies: []\nupstream: []\n"

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")

		// Token should work immediately
		rr := doJSONRequest(t, handler, http.MethodGet, "/System/Info/Public", nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("valid token rejected: status=%d", rr.Code)
		}

		// Set token creation time to far in the past (simulating months of inactivity)
		app.Auth.mu.Lock()
		if info, ok := app.Auth.tokens[token]; ok {
			info.CreatedAt = time.Now().Add(-90 * 24 * time.Hour).UnixMilli()
			app.Auth.tokens[token] = info
		}
		app.Auth.mu.Unlock()

		// Token should still be accepted — tokens never expire
		rr = doJSONRequest(t, handler, http.MethodGet, "/Users/"+app.Auth.ProxyUserID()+"/Views", nil, token)
		if rr.Code == http.StatusUnauthorized {
			t.Fatalf("token should never expire, but got 401")
		}

		// After explicit revocation, token should be rejected
		app.Auth.RevokeToken(token)
		rr = doJSONRequest(t, handler, http.MethodGet, "/Users/"+app.Auth.ProxyUserID()+"/Views", nil, token)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("revoked token accepted: status=%d, want 401", rr.Code)
		}
	})
}

// Phase 4 bonus: Test login rate limiting
func TestLoginRateLimiting(t *testing.T) {
	config := "server:\n  port: 8096\n  name: \"Test\"\n  id: \"svr\"\nadmin:\n  username: \"admin\"\n  password: \"secret\"\nplayback:\n  mode: \"proxy\"\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\nproxies: []\nupstream: []\n"

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		// Make loginMaxFailures failed attempts
		for i := 0; i < loginMaxFailures; i++ {
			rr := doJSONRequest(t, handler, http.MethodPost, "/Users/AuthenticateByName",
				map[string]any{"Username": "admin", "Pw": "wrong"}, "")
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("attempt %d: status=%d, want 401", i+1, rr.Code)
			}
		}

		// Next attempt should be rate limited
		rr := doJSONRequest(t, handler, http.MethodPost, "/Users/AuthenticateByName",
			map[string]any{"Username": "admin", "Pw": "wrong"}, "")
		if rr.Code != http.StatusTooManyRequests {
			t.Fatalf("rate limit not enforced: status=%d, want 429", rr.Code)
		}

		// Even correct password should be blocked
		rr = doJSONRequest(t, handler, http.MethodPost, "/Users/AuthenticateByName",
			map[string]any{"Username": "admin", "Pw": "secret"}, "")
		if rr.Code != http.StatusTooManyRequests {
			t.Fatalf("rate limit bypassed with correct password: status=%d, want 429", rr.Code)
		}
	})
}

// Phase 4 bonus: Test pagination
func TestPaginateItems(t *testing.T) {
	items := make([]map[string]any, 10)
	for i := range items {
		items[i] = map[string]any{"Id": fmt.Sprintf("item-%d", i)}
	}

	tests := []struct {
		name       string
		startIndex string
		limit      string
		wantCount  int
		wantStart  int
		wantTotal  int
	}{
		{"no pagination", "", "", 10, 0, 10},
		{"start at 3", "3", "", 7, 3, 10},
		{"limit 5", "", "5", 5, 0, 10},
		{"start 2 limit 3", "2", "3", 3, 2, 10},
		{"start beyond end", "20", "", 0, 10, 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := make(map[string][]string)
			if tt.startIndex != "" {
				q["StartIndex"] = []string{tt.startIndex}
			}
			if tt.limit != "" {
				q["Limit"] = []string{tt.limit}
			}
			result := paginateItems(items, q)
			gotItems := result["Items"].([]any)
			if len(gotItems) != tt.wantCount {
				t.Fatalf("items count = %d, want %d", len(gotItems), tt.wantCount)
			}
			if result["StartIndex"].(int) != tt.wantStart {
				t.Fatalf("StartIndex = %v, want %d", result["StartIndex"], tt.wantStart)
			}
			if result["TotalRecordCount"].(int) != tt.wantTotal {
				t.Fatalf("TotalRecordCount = %v, want %d", result["TotalRecordCount"], tt.wantTotal)
			}
		})
	}
}
