package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLocalResumeScopesSeriesSeasonAndSameServerLibraries(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token", "User": map[string]any{"Id": "user-a"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Items":
			if r.URL.Query().Get("Ids") == "episode-a" {
				_ = json.NewEncoder(w).Encode(map[string]any{"Items": []any{map[string]any{
					"Id": "episode-a", "Name": "Episode A", "Type": "Episode",
					"SeriesId": "series-a", "SeasonId": "season-a", "ParentId": "season-a",
					"ParentIndexNumber": 1, "IndexNumber": 1,
				}}, "TotalRecordCount": 1})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": []any{}, "TotalRecordCount": 0})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/Items/"):
			id := strings.TrimPrefix(r.URL.Path, "/Items/")
			items := map[string]map[string]any{
				"episode-a": {"Id": "episode-a", "Name": "Episode A", "Type": "Episode", "SeriesId": "series-a", "SeasonId": "season-a", "ParentId": "season-a"},
				"season-a":  {"Id": "season-a", "Name": "Season 1", "Type": "Season", "ParentId": "series-a"},
				"series-a":  {"Id": "series-a", "Name": "Series A", "Type": "Series", "ParentId": "lib-a"},
				"lib-a":     {"Id": "lib-a", "Name": "Library A", "Type": "CollectionFolder"},
				"lib-b":     {"Id": "lib-b", "Name": "Library B", "Type": "CollectionFolder"},
			}
			if item := items[id]; item != nil {
				_ = json.NewEncoder(w).Encode(item)
				return
			}
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	withTempAppConfig(t, singleUpstreamConfig(upstream.URL), func(app *App, handler http.Handler) {
		token := createRegularUser(t, handler)
		proxyUserID := watchUserID(t, app, "child")
		serverID := app.Upstream.Clients()[0].ID
		episodeID := app.IDStore.GetOrCreateVirtualID("episode-a", serverID)
		seriesID := app.IDStore.GetOrCreateVirtualID("series-a", serverID)
		seasonID := app.IDStore.GetOrCreateVirtualID("season-a", serverID)
		libraryAID := app.IDStore.GetOrCreateVirtualID("lib-a", serverID)
		libraryBID := app.IDStore.GetOrCreateVirtualID("lib-b", serverID)
		if err := app.WatchStore.RecordProgress(&WatchProgress{
			ProxyUserID: proxyUserID, VirtualItemID: episodeID, ServerID: serverID,
			OriginalItemID: "episode-a", ItemType: "Episode", SeriesVirtualID: seriesID,
			SeriesOriginalID: "series-a", SeriesName: "Series A", ParentIndexNumber: 1,
			IndexNumber: 1, PositionTicks: 100, RuntimeTicks: 1000,
		}); err != nil {
			t.Fatal(err)
		}

		for name, parentID := range map[string]string{
			"series":  seriesID,
			"season":  seasonID,
			"library": libraryAID,
		} {
			rr := doJSONRequest(t, handler, http.MethodGet,
				"/Users/"+app.Auth.ProxyUserID()+"/Items/Resume?ParentId="+parentID, nil, token)
			if rr.Code != http.StatusOK {
				t.Fatalf("%s scope status=%d body=%s", name, rr.Code, rr.Body.String())
			}
			if got := itemNames(t, rr.Body.Bytes()); len(got) != 1 || got[0] != "Episode A" {
				t.Fatalf("%s scope returned %v, want [Episode A] body=%s", name, got, rr.Body.String())
			}
		}

		rr := doJSONRequest(t, handler, http.MethodGet,
			"/Users/"+app.Auth.ProxyUserID()+"/Items/Resume?ParentId="+libraryBID, nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("different same-server library status=%d body=%s", rr.Code, rr.Body.String())
		}
		if got := itemNames(t, rr.Body.Bytes()); len(got) != 0 || itemTotal(t, rr.Body.Bytes()) != 0 {
			t.Fatalf("library B received another library's episode: %v body=%s", got, rr.Body.String())
		}

		rr = doJSONRequest(t, handler, http.MethodGet,
			"/Users/"+app.Auth.ProxyUserID()+"/Items/Resume?ParentId="+libraryAID+"&StartIndex=1&Limit=1", nil, token)
		if rr.Code != http.StatusOK || len(itemNames(t, rr.Body.Bytes())) != 0 {
			t.Fatalf("resume StartIndex was not applied after scope filtering: status=%d body=%s", rr.Code, rr.Body.String())
		}
	})
}
