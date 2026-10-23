// response_privacy_test.go covers the S4 privacy contract: a restricted
// (non-admin) caller never sees the shared upstream account's watch state, a
// failed local read never degrades into shared state, and media-source file
// layout stays out of restricted detail payloads.
package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

// fallbackPrivacyStub serves a generic JSON endpoint whose payload carries the
// shared upstream account's UserData, plus shared cache validators.
func fallbackPrivacyStub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-a", "User": map[string]any{"Id": "user-a"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Things/X":
			w.Header().Set("ETag", "shared-etag")
			w.Header().Set("Cache-Control", "public,max-age=86400")
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": []map[string]any{{
				"Id":       "item-x",
				"Name":     "Shared Thing",
				"UserData": map[string]any{"Played": true, "IsFavorite": true, "PlaybackPositionTicks": 999, "LastPlayedDate": "2020-01-01"},
			}}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": []any{}})
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestFallbackJSONUserDataIsolated verifies a restricted user gets a rebuilt
// UserData block from the generic fallback JSON path (P1-4) while the admin
// keeps the shared view, and the per-user body is never cacheable under the
// upstream's shared validators.
func TestFallbackJSONUserDataIsolated(t *testing.T) {
	upstream := fallbackPrivacyStub(t)
	withTempAppConfig(t, singleUpstreamConfig(upstream.URL), func(app *App, handler http.Handler) {
		adminToken := loginToken(t, handler, "secret")
		if rr := doJSONRequest(t, handler, http.MethodPost, "/admin/api/users",
			map[string]any{"username": "bob", "password": "bob12345"}, adminToken); rr.Code != http.StatusCreated {
			t.Fatalf("create bob: status=%d", rr.Code)
		}
		bobToken := loginTokenAs(t, handler, "bob", "bob12345")
		clientID := app.Upstream.Clients()[0].ID
		virtual := app.IDStore.GetOrCreateVirtualID("item-x", clientID)

		// Bob favorites locally, then reads the generic fallback JSON route.
		if rr := doJSONRequest(t, handler, http.MethodPost,
			"/Users/"+app.Auth.ProxyUserID()+"/FavoriteItems/"+virtual, nil, bobToken); rr.Code != http.StatusNoContent && rr.Code != http.StatusOK {
			t.Fatalf("favorite add: status=%d", rr.Code)
		}
		rr := doJSONRequest(t, handler, http.MethodGet, "/Things/X", nil, bobToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("bob fallback status=%d body=%s", rr.Code, rr.Body.String())
		}
		ud := itemUserData(t, rr.Body.Bytes(), virtual)
		if played, _ := ud["Played"].(bool); played {
			t.Fatalf("bob kept the upstream played mark: %#v", ud)
		}
		if fav, _ := ud["IsFavorite"].(bool); !fav {
			t.Fatalf("bob lost his own favorite: %#v", ud)
		}
		if cc := rr.Header().Get("Cache-Control"); cc != "private,no-store" {
			t.Fatalf("restricted Cache-Control = %q, want private,no-store", cc)
		}
		if etag := rr.Header().Get("ETag"); etag != "" {
			t.Fatalf("restricted response kept the upstream ETag %q", etag)
		}

		// An isolated second user must not inherit bob's favorite.
		if rr := doJSONRequest(t, handler, http.MethodPost, "/admin/api/users",
			map[string]any{"username": "carol", "password": "carol123"}, adminToken); rr.Code != http.StatusCreated {
			t.Fatalf("create carol: status=%d", rr.Code)
		}
		carolToken := loginTokenAs(t, handler, "carol", "carol123")
		rr = doJSONRequest(t, handler, http.MethodGet, "/Things/X", nil, carolToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("carol fallback status=%d", rr.Code)
		}
		ud = itemUserData(t, rr.Body.Bytes(), virtual)
		if fav, _ := ud["IsFavorite"].(bool); fav {
			t.Fatalf("carol inherited bob's favorite: %#v", ud)
		}

		// The admin passthrough keeps the shared state and validators.
		rr = doJSONRequest(t, handler, http.MethodGet, "/Things/X", nil, adminToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("admin fallback status=%d", rr.Code)
		}
		ud = itemUserData(t, rr.Body.Bytes(), virtual)
		if played, _ := ud["Played"].(bool); !played {
			t.Fatalf("admin lost the upstream played mark: %#v", ud)
		}
		if cc := rr.Header().Get("Cache-Control"); cc != "public,max-age=86400" {
			t.Fatalf("admin Cache-Control = %q, want upstream passthrough", cc)
		}
	})
}

// TestFallbackLocalStateFilterRefused verifies a restricted caller asking the
// fallback proxy to pre-filter by local per-user state gets an explicit
// refusal instead of a list pre-filtered with the shared account's state.
func TestFallbackLocalStateFilterRefused(t *testing.T) {
	upstream := fallbackPrivacyStub(t)
	withTempAppConfig(t, singleUpstreamConfig(upstream.URL), func(app *App, handler http.Handler) {
		adminToken := loginToken(t, handler, "secret")
		if rr := doJSONRequest(t, handler, http.MethodPost, "/admin/api/users",
			map[string]any{"username": "bob", "password": "bob12345"}, adminToken); rr.Code != http.StatusCreated {
			t.Fatalf("create bob: status=%d", rr.Code)
		}
		bobToken := loginTokenAs(t, handler, "bob", "bob12345")

		for _, filters := range []string{"IsPlayed", "isunplayed", "IsPlayed,Videos", "Videos,IsResumable"} {
			rr := doJSONRequest(t, handler, http.MethodGet, "/Things?Filters="+filters, nil, bobToken)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("Filters=%s: status=%d body=%s, want 400", filters, rr.Code, rr.Body.String())
			}
		}
		// A request without local-state filters still passes through.
		if rr := doJSONRequest(t, handler, http.MethodGet, "/Things?Filters=Videos", nil, bobToken); rr.Code != http.StatusOK {
			t.Fatalf("non-local Filters=Videos: status=%d", rr.Code)
		}
		// The admin is served by the upstream and keeps the passthrough route.
		if rr := doJSONRequest(t, handler, http.MethodGet, "/Things?Filters=IsPlayed", nil, adminToken); rr.Code != http.StatusOK {
			t.Fatalf("admin Filters=IsPlayed: status=%d", rr.Code)
		}
	})
}

// TestPrivacyMediaSourcesRestrictedStripped verifies detail-style MediaSources
// lose Path/DirectStreamUrl/TranscodingUrl for restricted viewers and keep the
// container/track data, while admins pass through untouched.
func TestPrivacyMediaSourcesRestrictedStripped(t *testing.T) {
	sources := []map[string]any{{
		"Id":              "src-1",
		"Container":       "mkv",
		"Path":            "/data/movies/film.mkv",
		"DirectStreamUrl": "/Videos/src-1/stream.mkv",
		"TranscodingUrl":  "/Videos/src-1/master.m3u8",
	}}

	admin := &http.Request{}
	kept := privacyDetailMediaSources(admin, sources)
	if kept[0]["Path"] == nil {
		t.Fatal("admin must keep Path")
	}

	restrictedReq := (&http.Request{}).WithContext(context.WithValue(context.Background(), requestContextKey{}, &RequestContext{ProxyUser: &tokenInfo{Role: "user"}}))
	cleaned := privacyDetailMediaSources(restrictedReq, sources)
	if cleaned[0]["Path"] != nil {
		t.Fatalf("restricted Path survived: %#v", cleaned[0])
	}
	if cleaned[0]["DirectStreamUrl"] != nil || cleaned[0]["TranscodingUrl"] != nil {
		t.Fatalf("restricted direct/transcoding urls survived: %#v", cleaned[0])
	}
	if cleaned[0]["Container"] != "mkv" {
		t.Fatalf("container data must survive: %#v", cleaned[0])
	}
}

// TestPrivacyReadFailureNeverShipsSharedState verifies a failed local read
// answers 502 instead of degrading into the shared upstream state.
func TestPrivacyReadFailureNeverShipsSharedState(t *testing.T) {
	dir := t.TempDir()
	db, err := openSQLite(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	ws, err := NewWatchStore(db, nil)
	if err != nil {
		t.Fatalf("new watch store: %v", err)
	}
	// Close the backing store before the request: GetProgress must error.
	if err := closeSQLite(db); err != nil {
		t.Fatalf("close db: %v", err)
	}

	app := &App{WatchStore: ws}
	restrictedReq := httptest.NewRequest(http.MethodGet, "/Things/X", nil)
	restrictedReq = restrictedReq.WithContext(context.WithValue(restrictedReq.Context(), requestContextKey{}, &RequestContext{ProxyUser: &tokenInfo{Role: "user", UserID: "u1"}}))

	payload := map[string]any{
		"UserData": map[string]any{"Played": true, "IsFavorite": true, "PlaybackPositionTicks": 42},
	}
	rr := httptest.NewRecorder()
	if ok := app.strictSanitizedUserData(rr, restrictedReq, "virtual-1", payload); ok {
		t.Fatal("a failed local read must not report success")
	}
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("read failure status = %d, want 502", rr.Code)
	}
	// The list form follows the same contract.
	payloadList := []map[string]any{{
		"Id":       "virtual-1",
		"UserData": map[string]any{"Played": true, "PlaybackPositionTicks": 42},
	}}
	rr = httptest.NewRecorder()
	if ok := app.strictSanitizedUserDataItems(rr, restrictedReq, payloadList); ok {
		t.Fatal("a failed local read must not report success")
	}
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("list read failure status = %d, want 502", rr.Code)
	}
}
