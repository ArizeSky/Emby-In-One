package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestWatchHistoryIsolationForRegularUsers verifies that non-admin proxy users
// get per-user watch history: local Resume, Played status, and Favorites.
func TestWatchHistoryIsolationForRegularUsers(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"AccessToken": "tok-a",
				"User":        map[string]any{"Id": "user-a"},
			})
		// Session endpoints: accept and return OK
		case r.Method == http.MethodPost && r.URL.Path == "/Sessions/Playing":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/Sessions/Playing/Progress":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/Sessions/Playing/Stopped":
			w.WriteHeader(http.StatusNoContent)
		// UserData endpoint: return upstream UserData (shared for all users)
		case r.URL.Path == "/Users/user-a/Items/movie-a/UserData":
			if r.Method == http.MethodPost {
				_ = json.NewEncoder(w).Encode(map[string]any{
					"PlaybackPositionTicks": 0,
					"Played":               true,
					"IsFavorite":            false,
				})
			}
		// Favorite endpoints
		case r.Method == http.MethodPost && r.URL.Path == "/Users/user-a/FavoriteItems/movie-a":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"PlaybackPositionTicks": 0,
				"Played":               false,
				"IsFavorite":            true,
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/Users/user-a/FavoriteItems/movie-a":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"PlaybackPositionTicks": 0,
				"Played":               false,
				"IsFavorite":            false,
			})
		// Item detail endpoint: return item with UserData embedded
		case r.Method == http.MethodGet && r.URL.Path == "/Users/user-a/Items/movie-a":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":   "movie-a",
				"Name": "Test Movie",
				"Type": "Movie",
				"UserData": map[string]any{
					"PlaybackPositionTicks": 0,
					"Played":               false,
					"IsFavorite":            false,
				},
			})
		// Resume endpoint (upstream): returns upstream shared resume
		case r.Method == http.MethodGet && r.URL.Path == "/Users/user-a/Items/Resume":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Items":            []any{},
				"TotalRecordCount": 0,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	config := singleUpstreamConfig(upstream.URL)
	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		// Login as admin and create a regular user
		adminToken := loginTokenAs(t, handler, "admin", "secret")

		rr := doAuthJSON(t, handler, http.MethodPost, "/admin/api/users",
			map[string]any{"username": "child", "password": "child123"}, adminToken)
		if rr.Code != http.StatusCreated {
			t.Fatalf("create user: status=%d body=%s", rr.Code, rr.Body.String())
		}

		// Login as regular user
		childToken := loginTokenAs(t, handler, "child", "child123")

		// Get virtual item ID for upstream movie-a
		virtualMovieID := app.IDStore.GetOrCreateVirtualID("movie-a", 0)

		// ---- Test 1: Session progress is recorded in WatchStore ----
		rr = doAuthJSON(t, handler, http.MethodPost, "/Sessions/Playing",
			map[string]any{"ItemId": virtualMovieID, "PositionTicks": 10000}, childToken)
		if rr.Code != http.StatusNoContent && rr.Code != http.StatusOK {
			t.Fatalf("session/playing: status=%d body=%s", rr.Code, rr.Body.String())
		}

		rr = doAuthJSON(t, handler, http.MethodPost, "/Sessions/Playing/Progress",
			map[string]any{"ItemId": virtualMovieID, "PositionTicks": 25000}, childToken)
		if rr.Code != http.StatusNoContent && rr.Code != http.StatusOK {
			t.Fatalf("session/progress: status=%d body=%s", rr.Code, rr.Body.String())
		}

		// Verify progress is recorded in WatchStore
		var childUserID string
		for _, u := range app.UserStore.List() {
			if u.Username == "child" {
				childUserID = u.ID
				break
			}
		}
		if childUserID == "" {
			t.Fatalf("child user not found in UserStore")
		}

		progress := app.WatchStore.GetProgress(childUserID, virtualMovieID)
		if progress == nil {
			t.Fatalf("progress not recorded in WatchStore")
		}
		if progress.PositionTicks != 25000 {
			t.Fatalf("PositionTicks = %d, want 25000", progress.PositionTicks)
		}

		// ---- Test 2: Admin does NOT have local progress ----
		adminProgress := app.WatchStore.GetProgress("admin", virtualMovieID)
		if adminProgress != nil {
			t.Fatalf("admin should not have local watch progress")
		}

		// ---- Test 3: Mark played via UserData endpoint ----
		rr = doAuthJSON(t, handler, http.MethodPost,
			fmt.Sprintf("/Users/proxy-user-id/Items/%s/UserData", virtualMovieID),
			map[string]any{"Played": true}, childToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("userdata POST: status=%d body=%s", rr.Code, rr.Body.String())
		}

		progress = app.WatchStore.GetProgress(childUserID, virtualMovieID)
		if progress == nil || !progress.Played {
			t.Fatalf("Played should be true after UserData POST")
		}

		// ---- Test 4: Set favorite ----
		rr = doAuthJSON(t, handler, http.MethodPost,
			fmt.Sprintf("/Users/proxy-user-id/FavoriteItems/%s", virtualMovieID),
			nil, childToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("favorite add: status=%d body=%s", rr.Code, rr.Body.String())
		}

		progress = app.WatchStore.GetProgress(childUserID, virtualMovieID)
		if progress == nil || !progress.IsFavorite {
			t.Fatalf("IsFavorite should be true after favorite add")
		}

		// ---- Test 5: Remove favorite ----
		rr = doAuthJSON(t, handler, http.MethodDelete,
			fmt.Sprintf("/Users/proxy-user-id/FavoriteItems/%s", virtualMovieID),
			nil, childToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("favorite remove: status=%d body=%s", rr.Code, rr.Body.String())
		}

		progress = app.WatchStore.GetProgress(childUserID, virtualMovieID)
		if progress == nil || progress.IsFavorite {
			t.Fatalf("IsFavorite should be false after favorite remove")
		}

		// ---- Test 6: User deletion clears watch data ----
		var deleteUserID string
		for _, u := range app.UserStore.List() {
			if u.Username == "child" {
				deleteUserID = u.ID
				break
			}
		}
		rr = doAuthJSON(t, handler, http.MethodDelete,
			"/admin/api/users/"+deleteUserID, nil, adminToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("delete user: status=%d body=%s", rr.Code, rr.Body.String())
		}

		progress = app.WatchStore.GetProgress(deleteUserID, virtualMovieID)
		if progress != nil {
			t.Fatalf("watch data should be cleared after user deletion")
		}
	})
}

// TestWatchHistoryAdminKeepsUpstream verifies that admin users still get
// upstream data without local WatchStore interception.
func TestWatchHistoryAdminKeepsUpstream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"AccessToken": "tok-a",
				"User":        map[string]any{"Id": "user-a"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/Users/user-a/Items/Resume":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Items": []any{
					map[string]any{"Id": "movie-a", "Name": "Upstream Movie", "Type": "Movie"},
				},
				"TotalRecordCount": 1,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	config := singleUpstreamConfig(upstream.URL)
	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		adminToken := loginToken(t, handler, "secret")

		// Admin resume should fetch from upstream
		rr := doAuthJSON(t, handler, http.MethodGet, "/Users/proxy-user-id/Items/Resume?Limit=10", nil, adminToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("resume: status=%d body=%s", rr.Code, rr.Body.String())
		}

		var payload map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &payload)
		items, _ := payload["Items"].([]any)
		if len(items) != 1 {
			t.Fatalf("admin resume: expected 1 item, got %d", len(items))
		}
	})
}

// TestWatchHistoryOverlayOnItemDetail verifies that item detail responses
// for non-admin users have their UserData overlaid with local WatchStore values.
func TestWatchHistoryOverlayOnItemDetail(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"AccessToken": "tok-a",
				"User":        map[string]any{"Id": "user-a"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/Users/user-a/Items/movie-a":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":   "movie-a",
				"Name": "Test Movie",
				"Type": "Movie",
				"UserData": map[string]any{
					"PlaybackPositionTicks": float64(0),
					"Played":               false,
					"IsFavorite":            false,
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	config := singleUpstreamConfig(upstream.URL)
	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		adminToken := loginTokenAs(t, handler, "admin", "secret")

		// Create a regular user
		rr := doAuthJSON(t, handler, http.MethodPost, "/admin/api/users",
			map[string]any{"username": "child", "password": "child123"}, adminToken)
		if rr.Code != http.StatusCreated {
			t.Fatalf("create user: status=%d", rr.Code)
		}

		childToken := loginTokenAs(t, handler, "child", "child123")

		// Find user ID
		var childUserID string
		for _, u := range app.UserStore.List() {
			if u.Username == "child" {
				childUserID = u.ID
				break
			}
		}

		virtualMovieID := app.IDStore.GetOrCreateVirtualID("movie-a", 0)

		// Pre-populate WatchStore with some progress + favorite
		app.WatchStore.RecordProgress(&WatchProgress{
			ProxyUserID:    childUserID,
			VirtualItemID:  virtualMovieID,
			ServerIndex:    0,
			OriginalItemID: "movie-a",
			PositionTicks:  42000,
			RuntimeTicks:   100000,
			Played:         true,
			IsFavorite:     true,
		})

		// Fetch item detail as child user
		rr = doAuthJSON(t, handler, http.MethodGet,
			fmt.Sprintf("/Users/proxy-user-id/Items/%s", virtualMovieID), nil, childToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("item detail: status=%d body=%s", rr.Code, rr.Body.String())
		}

		var itemDetail map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &itemDetail)

		ud, ok := itemDetail["UserData"].(map[string]any)
		if !ok {
			t.Fatalf("missing UserData in response: %#v", itemDetail)
		}

		// Verify local WatchStore values are overlaid
		posTicks, _ := ud["PlaybackPositionTicks"].(float64)
		if int64(posTicks) != 42000 {
			t.Fatalf("PlaybackPositionTicks = %v, want 42000", ud["PlaybackPositionTicks"])
		}
		played, _ := ud["Played"].(bool)
		if !played {
			t.Fatalf("Played = %v, want true", ud["Played"])
		}
		isFav, _ := ud["IsFavorite"].(bool)
		if !isFav {
			t.Fatalf("IsFavorite = %v, want true", ud["IsFavorite"])
		}
	})
}

// TestWatchHistoryNoLeakFromUpstreamAdmin verifies that when a regular user
// has no local watch progress, the upstream admin's UserData is cleared
// (Played=false, position=0) instead of leaking to the user.
func TestUserDataPlayedStillUpdatesLocalStateWhenUpstreamFails(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"AccessToken": "tok-a",
				"User":        map[string]any{"Id": "user-a"},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/Users/user-a/Items/movie-a/UserData":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("<!DOCTYPE html><html><body>Cloudflare</body></html>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	config := parityConfigWithUpstreams(fmt.Sprintf("  - name: \"A\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n", upstream.URL))
	withTempAppPrepared(t, config, nil, func(app *App, handler http.Handler, dir string) {
		adminToken := loginTokenAs(t, handler, "admin", "secret")
		rr := doAuthJSON(t, handler, http.MethodPost, "/admin/api/users", map[string]any{"username": "child", "password": "child123"}, adminToken)
		if rr.Code != http.StatusCreated {
			t.Fatalf("create user: status=%d body=%s", rr.Code, rr.Body.String())
		}
		childToken := loginTokenAs(t, handler, "child", "child123")
		childInfo := app.Auth.ValidateToken(childToken)
		if childInfo == nil {
			t.Fatal("child token validation returned nil")
		}
		childUserID := childInfo.UserID

		virtualMovieID := app.IDStore.GetOrCreateVirtualID("movie-a", 0)
		rr = doAuthJSON(t, handler, http.MethodPost,
			fmt.Sprintf("/Users/%s/Items/%s/UserData", app.Auth.ProxyUserID(), virtualMovieID),
			map[string]any{"Played": true}, childToken)
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("userdata POST status=%d body=%s", rr.Code, rr.Body.String())
		}

		progress := app.WatchStore.GetProgress(childUserID, virtualMovieID)
		if progress == nil || !progress.Played {
			t.Fatalf("Played should still be true locally after upstream failure")
		}
	})
}

func TestWatchHistoryNoLeakFromUpstreamAdmin(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"AccessToken": "tok-a",
				"User":        map[string]any{"Id": "user-a"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/Users/user-a/Items/movie-a":
			// Admin has watched this movie — upstream returns Played=true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":   "movie-a",
				"Name": "Test Movie",
				"Type": "Movie",
				"UserData": map[string]any{
					"PlaybackPositionTicks": float64(50000),
					"Played":               true,
					"IsFavorite":            true,
					"PlayedPercentage":      float64(80),
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	config := singleUpstreamConfig(upstream.URL)
	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		adminToken := loginTokenAs(t, handler, "admin", "secret")

		rr := doAuthJSON(t, handler, http.MethodPost, "/admin/api/users",
			map[string]any{"username": "child", "password": "child123"}, adminToken)
		if rr.Code != http.StatusCreated {
			t.Fatalf("create user: status=%d", rr.Code)
		}

		childToken := loginTokenAs(t, handler, "child", "child123")
		virtualMovieID := app.IDStore.GetOrCreateVirtualID("movie-a", 0)

		// Do NOT add any local watch progress for child user

		// Fetch item detail as child user
		rr = doAuthJSON(t, handler, http.MethodGet,
			fmt.Sprintf("/Users/proxy-user-id/Items/%s", virtualMovieID), nil, childToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("item detail: status=%d body=%s", rr.Code, rr.Body.String())
		}

		var itemDetail map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &itemDetail)

		ud, ok := itemDetail["UserData"].(map[string]any)
		if !ok {
			t.Fatalf("missing UserData in response: %#v", itemDetail)
		}

		// Admin's data must NOT leak to child user
		played, _ := ud["Played"].(bool)
		if played {
			t.Fatalf("Played = true, admin data leaked to regular user")
		}
		isFav, _ := ud["IsFavorite"].(bool)
		if isFav {
			t.Fatalf("IsFavorite = true, admin data leaked to regular user")
		}
		posTicks, _ := ud["PlaybackPositionTicks"].(float64)
		if posTicks != 0 {
			t.Fatalf("PlaybackPositionTicks = %v, want 0 (admin data leaked)", posTicks)
		}
	})
}
