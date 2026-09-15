package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// seriesUserDataStub serves one series whose season and episode carry the shared
// upstream account's UserData, so tests can tell upstream state from local state.
func seriesUserDataStub(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-a", "User": map[string]any{"Id": "user-a"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Shows/series-a/Seasons":
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": []map[string]any{{
				"Id": "season-a", "Name": "Season 1", "IndexNumber": 1, "Type": "Season",
				"UserData": map[string]any{"Played": true, "IsFavorite": true, "PlaybackPositionTicks": 999},
			}}})
		case r.Method == http.MethodGet && r.URL.Path == "/Shows/series-a/Episodes":
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": []map[string]any{{
				"Id": "episode-a", "Name": "Episode 1", "ParentIndexNumber": 1, "IndexNumber": 1, "Type": "Episode",
				"UserData": map[string]any{"Played": true, "IsFavorite": true, "PlaybackPositionTicks": 999},
			}}})
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/FavoriteItems/"):
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// itemUserData pulls one item's UserData out of a list response.
func itemUserData(t *testing.T, body []byte, itemID string) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal list response: %v", err)
	}
	items, _ := payload["Items"].([]any)
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok || item["Id"] != itemID {
			continue
		}
		userData, ok := item["UserData"].(map[string]any)
		if !ok {
			t.Fatalf("item %s carries no UserData: %#v", itemID, item)
		}
		return userData
	}
	t.Fatalf("item %s is missing from the response: %s", itemID, body)
	return nil
}

// TestSeriesRoutesOverlayLocalUserState covers the season and episode lists: they
// used to pass the shared upstream account's UserData straight through, so a regular
// user saw someone else's played/favorite marks on the pages where they matter most.
func TestSeriesRoutesOverlayLocalUserState(t *testing.T) {
	upstream := seriesUserDataStub(t)

	withTempAppConfig(t, singleUpstreamConfig(upstream.URL), func(app *App, handler http.Handler) {
		adminToken := loginToken(t, handler, "secret")
		rr := doJSONRequest(t, handler, http.MethodPost, "/admin/api/users",
			map[string]any{"username": "bob", "password": "bob123"}, adminToken)
		if rr.Code != http.StatusCreated {
			t.Fatalf("create user: status=%d body=%s", rr.Code, rr.Body.String())
		}
		userToken := loginTokenAs(t, handler, "bob", "bob123")

		virtualSeries := app.IDStore.GetOrCreateVirtualID("series-a", 0)
		virtualSeason := app.IDStore.GetOrCreateVirtualID("season-a", 0)
		virtualEpisode := app.IDStore.GetOrCreateVirtualID("episode-a", 0)

		// A regular user with no local record must not inherit the upstream marks.
		rr = doJSONRequest(t, handler, http.MethodGet, "/Shows/"+virtualSeries+"/Seasons", nil, userToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("seasons status = %d body=%s", rr.Code, rr.Body.String())
		}
		seasonUserData := itemUserData(t, rr.Body.Bytes(), virtualSeason)
		if seasonUserData["Played"] != false || seasonUserData["IsFavorite"] != false {
			t.Fatalf("season kept upstream marks for a regular user: %#v", seasonUserData)
		}
		if position, _ := seasonUserData["PlaybackPositionTicks"].(float64); position != 0 {
			t.Fatalf("season kept the upstream position: %#v", seasonUserData)
		}

		// Favoriting locally must show up on the episode list.
		rr = doJSONRequest(t, handler, http.MethodPost,
			"/Users/"+app.Auth.ProxyUserID()+"/FavoriteItems/"+virtualEpisode, nil, userToken)
		if rr.Code != http.StatusNoContent && rr.Code != http.StatusOK {
			t.Fatalf("favorite add: status=%d body=%s", rr.Code, rr.Body.String())
		}
		rr = doJSONRequest(t, handler, http.MethodGet, "/Shows/"+virtualSeries+"/Episodes", nil, userToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("episodes status = %d body=%s", rr.Code, rr.Body.String())
		}
		episodeUserData := itemUserData(t, rr.Body.Bytes(), virtualEpisode)
		if episodeUserData["IsFavorite"] != true {
			t.Fatalf("local favorite missing from the episode list: %#v", episodeUserData)
		}
		if episodeUserData["Played"] != false {
			t.Fatalf("played state must stay local: %#v", episodeUserData)
		}

		// The administrator keeps the upstream view untouched.
		rr = doJSONRequest(t, handler, http.MethodGet, "/Shows/"+virtualSeries+"/Seasons", nil, adminToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("admin seasons status = %d body=%s", rr.Code, rr.Body.String())
		}
		adminUserData := itemUserData(t, rr.Body.Bytes(), virtualSeason)
		if adminUserData["Played"] != true || adminUserData["IsFavorite"] != true {
			t.Fatalf("admin should see the upstream UserData: %#v", adminUserData)
		}
	})
}
