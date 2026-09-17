package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// restrictedUpstreamStub serves an upstream that a restricted user must not
// reach. Every request that is not the initial login is counted so tests can
// assert the upstream was never contacted for item content.
func restrictedUpstreamStub(t *testing.T, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName" {
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-b", "User": map[string]any{"Id": "user-b"}})
			return
		}
		hits.Add(1)
		if r.Method == http.MethodGet && r.URL.Path == "/Items/movie-b" {
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "movie-b", "Name": "Movie B", "MediaSources": []any{}})
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/Items/movie-b/Images/Primary" {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("fake-png"))
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func permittedUpstreamStub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-a", "User": map[string]any{"Id": "user-a"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Items/movie-a":
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "movie-a", "Name": "Movie A", "MediaSources": []any{}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// itemRoute is one request that carries a virtual item id to the server owning it.
type itemRoute struct {
	name   string
	method string
	path   string
	body   map[string]any
}

// itemScopedRoutes lists every route that resolves a virtual item id from the
// path, the query, or the JSON body and forwards it to the owning upstream.
func itemScopedRoutes(userID, movieID, seriesID string) []itemRoute {
	return []itemRoute{
		{name: "item detail", method: http.MethodGet, path: "/Items/" + movieID},
		{name: "item similar", method: http.MethodGet, path: "/Items/" + movieID + "/Similar"},
		{name: "item theme media", method: http.MethodGet, path: "/Items/" + movieID + "/ThemeMedia"},
		{name: "item image", method: http.MethodGet, path: "/Items/" + movieID + "/Images/Primary"},
		{name: "playback info", method: http.MethodPost, path: "/Items/" + movieID + "/PlaybackInfo"},
		{name: "video stream", method: http.MethodGet, path: "/Videos/" + movieID + "/stream.mp4"},
		{name: "user items by parent", method: http.MethodGet, path: "/Users/" + userID + "/Items?ParentId=" + seriesID},
		{name: "user items latest", method: http.MethodGet, path: "/Users/" + userID + "/Items/Latest?ParentId=" + seriesID},
		{name: "next up", method: http.MethodGet, path: "/Shows/NextUp?SeriesId=" + seriesID},
		{name: "show seasons", method: http.MethodGet, path: "/Shows/" + seriesID + "/Seasons"},
		{name: "show episodes", method: http.MethodGet, path: "/Shows/" + seriesID + "/Episodes"},
		{name: "user data", method: http.MethodPost, path: "/Users/" + userID + "/Items/" + movieID + "/UserData"},
		{name: "favorite add", method: http.MethodPost, path: "/Users/" + userID + "/FavoriteItems/" + movieID},
		{name: "favorite remove", method: http.MethodDelete, path: "/Users/" + userID + "/FavoriteItems/" + movieID},
		{name: "playing items", method: http.MethodPost, path: "/Users/" + userID + "/PlayingItems/" + movieID},
		{name: "session playing", method: http.MethodPost, path: "/Sessions/Playing", body: map[string]any{"ItemId": movieID}},
		{name: "session progress", method: http.MethodPost, path: "/Sessions/Playing/Progress", body: map[string]any{"ItemId": movieID}},
		{name: "session stopped", method: http.MethodPost, path: "/Sessions/Playing/Stopped", body: map[string]any{"ItemId": movieID}},
	}
}

func TestItemRoutesRejectUpstreamOutsideAllowedServers(t *testing.T) {
	var forbiddenHits atomic.Int32
	forbidden := restrictedUpstreamStub(t, &forbiddenHits)
	permitted := permittedUpstreamStub(t)

	withTempAppConfig(t, dualUpstreamConfig(permitted.URL, forbidden.URL), func(app *App, handler http.Handler) {
		adminToken := loginTokenAs(t, handler, "admin", "secret")
		rr := doAuthJSON(t, handler, http.MethodPost, "/admin/api/users",
			map[string]any{"username": "bob", "password": "bob12345", "allowedServers": []string{app.Upstream.Clients()[0].ID}}, adminToken)
		if rr.Code != http.StatusCreated {
			t.Fatalf("create user: status=%d body=%s", rr.Code, rr.Body.String())
		}
		userToken := loginTokenAs(t, handler, "bob", "bob12345")

		userID := app.Auth.ProxyUserID()
		forbiddenMovie := app.IDStore.GetOrCreateVirtualID("movie-b", app.Upstream.Clients()[1].ID)
		forbiddenSeries := app.IDStore.GetOrCreateVirtualID("series-b", app.Upstream.Clients()[1].ID)
		permittedMovie := app.IDStore.GetOrCreateVirtualID("movie-a", app.Upstream.Clients()[0].ID)

		for _, route := range itemScopedRoutes(userID, forbiddenMovie, forbiddenSeries) {
			rr := doAuthJSON(t, handler, route.method, route.path, route.body, userToken)
			if rr.Code != http.StatusForbidden {
				t.Errorf("%s: status=%d, want 403; body=%s", route.name, rr.Code, rr.Body.String())
			}
		}
		// Checked before the administrator request below, which does reach server B.
		if hits := forbiddenHits.Load(); hits != 0 {
			t.Errorf("forbidden upstream received %d item request(s), want 0", hits)
		}

		// The restricted user keeps access to servers it is allowed to see.
		rr = doAuthJSON(t, handler, http.MethodGet, "/Items/"+permittedMovie, nil, userToken)
		if rr.Code != http.StatusOK {
			t.Errorf("permitted item for restricted user: status=%d body=%s", rr.Code, rr.Body.String())
		}

		// The administrator is not restricted by the new checks.
		rr = doAuthJSON(t, handler, http.MethodGet, "/Items/"+forbiddenMovie, nil, adminToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("admin item detail: status=%d body=%s", rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), forbiddenMovie) {
			t.Errorf("admin item detail should carry the virtual id, body=%s", rr.Body.String())
		}
	})
}

// TestItemImageStaysReachableWithoutToken guards the deliberate exception: image
// URLs are embedded by clients, so only authenticated requests are checked.
func TestItemImageStaysReachableWithoutToken(t *testing.T) {
	var forbiddenHits atomic.Int32
	forbidden := restrictedUpstreamStub(t, &forbiddenHits)
	permitted := permittedUpstreamStub(t)

	withTempAppConfig(t, dualUpstreamConfig(permitted.URL, forbidden.URL), func(app *App, handler http.Handler) {
		forbiddenMovie := app.IDStore.GetOrCreateVirtualID("movie-b", app.Upstream.Clients()[1].ID)
		rr := doAuthJSON(t, handler, http.MethodGet, "/Items/"+forbiddenMovie+"/Images/Primary", nil, "")
		if rr.Code != http.StatusOK {
			t.Fatalf("anonymous image request: status=%d, want 200", rr.Code)
		}
		if body := rr.Body.String(); body != "fake-png" {
			t.Fatalf("anonymous image body = %q, want the upstream bytes", body)
		}
	})
}
