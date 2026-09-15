package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ---------- resolveRouteID fallback ----------

func TestResolveRouteIDFallbackToOtherInstance(t *testing.T) {
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-a", "User": map[string]any{"Id": "user-a"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srvA.Close()
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-b", "User": map[string]any{"Id": "user-b"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srvB.Close()

	config := dualUpstreamConfig(srvA.URL, srvB.URL)
	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		// Create virtual ID on server 0, associate server 1 as OtherInstance
		virtualID := app.IDStore.GetOrCreateVirtualID("movie-a", 0)
		app.IDStore.AssociateAdditionalInstance(virtualID, "movie-b", 1)

		// Both online: primary wins
		res := app.resolveRouteID(virtualID)
		if res == nil {
			t.Fatal("resolveRouteID returned nil with both servers online")
		}
		if res.ServerIndex != 0 || res.OriginalID != "movie-a" {
			t.Fatalf("expected server=0 orig=movie-a, got server=%d orig=%s", res.ServerIndex, res.OriginalID)
		}

		// Set server 0 offline
		app.Upstream.GetClient(0).setOffline("test offline")

		res2 := app.resolveRouteID(virtualID)
		if res2 == nil {
			t.Fatal("resolveRouteID returned nil after primary offline")
		}
		if res2.ServerIndex != 1 || res2.OriginalID != "movie-b" {
			t.Fatalf("fallback: expected server=1 orig=movie-b, got server=%d orig=%s", res2.ServerIndex, res2.OriginalID)
		}

		// Both offline: nil
		app.Upstream.GetClient(1).setOffline("test offline")
		res3 := app.resolveRouteID(virtualID)
		if res3 != nil {
			t.Fatal("resolveRouteID should return nil when all servers offline")
		}
	})
}

// ---------- resolveWatchItemServer fallback ----------

func TestResolveWatchItemServerFallback(t *testing.T) {
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-a", "User": map[string]any{"Id": "user-a"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srvA.Close()
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-b", "User": map[string]any{"Id": "user-b"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srvB.Close()

	config := dualUpstreamConfig(srvA.URL, srvB.URL)
	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		virtualID := app.IDStore.GetOrCreateVirtualID("movie-a", 0)
		app.IDStore.AssociateAdditionalInstance(virtualID, "movie-b", 1)

		wp := &WatchProgress{
			VirtualItemID:  virtualID,
			ServerIndex:    0,
			OriginalItemID: "movie-a",
		}

		// Primary online: returns server 0
		idx, origID, ok := app.resolveWatchItemServer(wp)
		if !ok || idx != 0 || origID != "movie-a" {
			t.Fatalf("primary online: idx=%d origID=%s ok=%v", idx, origID, ok)
		}

		// Set server 0 offline
		app.Upstream.GetClient(0).setOffline("test offline")

		idx2, origID2, ok2 := app.resolveWatchItemServer(wp)
		if !ok2 {
			t.Fatal("fallback failed: resolveWatchItemServer returned ok=false")
		}
		if idx2 != 1 || origID2 != "movie-b" {
			t.Fatalf("fallback: expected server=1 orig=movie-b, got server=%d orig=%s", idx2, origID2)
		}

		// Both offline
		app.Upstream.GetClient(1).setOffline("test offline")
		_, _, ok3 := app.resolveWatchItemServer(wp)
		if ok3 {
			t.Fatal("all offline: resolveWatchItemServer should return ok=false")
		}
	})
}

// ---------- resolveSeriesServer fallback ----------

func TestResolveSeriesServerFallback(t *testing.T) {
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-a", "User": map[string]any{"Id": "user-a"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srvA.Close()
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-b", "User": map[string]any{"Id": "user-b"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srvB.Close()

	config := dualUpstreamConfig(srvA.URL, srvB.URL)
	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		seriesVID := app.IDStore.GetOrCreateVirtualID("series-a", 0)
		app.IDStore.AssociateAdditionalInstance(seriesVID, "series-b", 1)

		sp := &WatchProgress{
			SeriesVirtualID:  seriesVID,
			SeriesOriginalID: "series-a",
			ServerIndex:      0,
		}

		// Primary online
		idx, origID, client := app.resolveSeriesServer(sp)
		if client == nil || idx != 0 || origID != "series-a" {
			t.Fatalf("primary online: idx=%d origID=%s client=%v", idx, origID, client)
		}

		// Server 0 offline → fallback to 1
		app.Upstream.GetClient(0).setOffline("test offline")
		idx2, origID2, client2 := app.resolveSeriesServer(sp)
		if client2 == nil {
			t.Fatal("fallback failed: resolveSeriesServer returned nil client")
		}
		if idx2 != 1 || origID2 != "series-b" {
			t.Fatalf("fallback: expected server=1 orig=series-b, got server=%d orig=%s", idx2, origID2)
		}

		// Both offline
		app.Upstream.GetClient(1).setOffline("test offline")
		_, _, client3 := app.resolveSeriesServer(sp)
		if client3 != nil {
			t.Fatal("all offline: resolveSeriesServer should return nil client")
		}
	})
}

// ---------- End-to-end: Resume items with offline fallback ----------

func TestResumeEndpointOfflineFallback(t *testing.T) {
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-a", "User": map[string]any{"Id": "user-a"}})
		case r.Method == http.MethodPost && r.URL.Path == "/Sessions/Playing":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/Sessions/Playing/Progress":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/Sessions/Playing/Stopped":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srvA.Close()

	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-b", "User": map[string]any{"Id": "user-b"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Items":
			// Return item metadata for movie-b
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Items": []any{
					map[string]any{
						"Id":       "movie-b",
						"Name":     "Test Movie",
						"Type":     "Movie",
						"UserData": map[string]any{"PlaybackPositionTicks": 0, "Played": false},
					},
				},
				"TotalRecordCount": 1,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srvB.Close()

	config := dualUpstreamConfig(srvA.URL, srvB.URL)
	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		// Setup: create regular user
		adminToken := loginTokenAs(t, handler, "admin", "secret")
		rr := doAuthJSON(t, handler, http.MethodPost, "/admin/api/users",
			map[string]any{"username": "child", "password": "child123"}, adminToken)
		if rr.Code != http.StatusCreated {
			t.Fatalf("create user: status=%d body=%s", rr.Code, rr.Body.String())
		}
		childToken := loginTokenAs(t, handler, "child", "child123")

		// Create virtual ID and associate to both servers
		virtualID := app.IDStore.GetOrCreateVirtualID("movie-a", 0)
		app.IDStore.AssociateAdditionalInstance(virtualID, "movie-b", 1)

		// Record watch progress on server 0 via session
		rr = doAuthJSON(t, handler, http.MethodPost, "/Sessions/Playing",
			map[string]any{"ItemId": virtualID, "PositionTicks": 50000}, childToken)
		if rr.Code != http.StatusNoContent && rr.Code != http.StatusOK {
			t.Fatalf("session/playing: status=%d", rr.Code)
		}
		rr = doAuthJSON(t, handler, http.MethodPost, "/Sessions/Playing/Progress",
			map[string]any{"ItemId": virtualID, "PositionTicks": 50000}, childToken)
		if rr.Code != http.StatusNoContent && rr.Code != http.StatusOK {
			t.Fatalf("session/progress: status=%d", rr.Code)
		}

		// Now set server 0 offline
		app.Upstream.GetClient(0).setOffline("test offline")

		// Fetch resume endpoint — should fall back to server 1
		rr = doAuthJSON(t, handler, http.MethodGet, "/Users/proxy-uid/Items/Resume", nil, childToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("resume status=%d body=%s", rr.Code, rr.Body.String())
		}

		var payload map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal resume: %v", err)
		}
		items, _ := payload["Items"].([]any)
		if len(items) == 0 {
			t.Fatal("resume returned 0 items after primary offline — fallback did not work")
		}

		// Verify the returned item has overlaid local UserData
		item0, _ := items[0].(map[string]any)
		ud, _ := item0["UserData"].(map[string]any)
		ticks, _ := ud["PlaybackPositionTicks"].(float64)
		if ticks != 50000 {
			t.Fatalf("expected PositionTicks=50000, got %v", ticks)
		}
	})
}
