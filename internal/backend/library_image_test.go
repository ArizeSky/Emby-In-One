package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestShowsSeasonsMergeAndPreserveAdditionalInstances(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-a", "User": map[string]any{"Id": "user-a"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Shows/series-a/Seasons":
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": []map[string]any{
				{"Id": "season-a1", "IndexNumber": 1, "Name": "Season 1A"},
				{"Id": "season-a2", "IndexNumber": 2, "Name": "Season 2A"},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer primary.Close()

	secondary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-b", "User": map[string]any{"Id": "user-b"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Shows/series-b/Seasons":
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": []map[string]any{
				{"Id": "season-b1", "IndexNumber": 1, "Name": "Season 1B"},
				{"Id": "season-b3", "IndexNumber": 3, "Name": "Season 3B"},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer secondary.Close()

	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test Server\"\n  id: \"server-1\"\n\nadmin:\n  username: \"admin\"\n  password: \"secret\"\n\nplayback:\n  mode: \"proxy\"\n\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\n\nproxies: []\nupstream:\n  - name: \"A\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n  - name: \"B\"\n    url: %q\n    username: \"u2\"\n    password: \"p2\"\n", primary.URL, secondary.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		virtualSeries := app.IDStore.GetOrCreateVirtualID("series-a", app.Upstream.Clients()[0].ID)
		app.IDStore.AssociateAdditionalInstance(virtualSeries, "series-b", app.Upstream.Clients()[1].ID)

		rr := doJSONRequest(t, handler, http.MethodGet, "/Shows/"+virtualSeries+"/Seasons", nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("seasons status = %d, body=%s", rr.Code, rr.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal seasons: %v", err)
		}
		items, _ := payload["Items"].([]any)
		if len(items) != 3 {
			t.Fatalf("season count = %d, want 3 payload=%#v", len(items), payload)
		}
		season1 := items[0].(map[string]any)
		season1ID, _ := season1["Id"].(string)
		if season1ID == "" || season1ID == "season-a1" {
			t.Fatalf("expected virtual season id, got %#v", season1)
		}
		resolved := app.IDStore.ResolveVirtualID(season1ID)
		if resolved == nil || len(resolved.OtherInstances) != 1 || resolved.OtherInstances[0].OriginalID != "season-b1" {
			t.Fatalf("season additional instances not preserved: %#v", resolved)
		}
	})
}

func TestShowsEpisodesTranslateSeasonIDAndDeduplicate(t *testing.T) {
	var primarySeasonID atomic.Value
	var secondarySeasonID atomic.Value

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-a", "User": map[string]any{"Id": "user-a"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Shows/series-a/Episodes":
			primarySeasonID.Store(r.URL.Query().Get("SeasonId"))
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": []map[string]any{
				{"Id": "ep-a1", "SeriesId": "series-a", "ParentId": "season-a1", "ParentIndexNumber": 1, "IndexNumber": 1, "Source": "primary"},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer primary.Close()

	secondary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-b", "User": map[string]any{"Id": "user-b"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Shows/series-b/Episodes":
			secondarySeasonID.Store(r.URL.Query().Get("SeasonId"))
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": []map[string]any{
				{"Id": "ep-b1", "SeriesId": "series-b", "ParentId": "season-b1", "ParentIndexNumber": 1, "IndexNumber": 1, "Source": "secondary-dup"},
				{"Id": "ep-b2", "SeriesId": "series-b", "ParentId": "season-b1", "ParentIndexNumber": 1, "IndexNumber": 2, "Source": "secondary-unique"},
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer secondary.Close()

	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test Server\"\n  id: \"server-1\"\n\nadmin:\n  username: \"admin\"\n  password: \"secret\"\n\nplayback:\n  mode: \"proxy\"\n\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\n\nproxies: []\nupstream:\n  - name: \"A\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n  - name: \"B\"\n    url: %q\n    username: \"u2\"\n    password: \"p2\"\n", primary.URL, secondary.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		virtualSeries := app.IDStore.GetOrCreateVirtualID("series-a", app.Upstream.Clients()[0].ID)
		app.IDStore.AssociateAdditionalInstance(virtualSeries, "series-b", app.Upstream.Clients()[1].ID)
		virtualSeason := app.IDStore.GetOrCreateVirtualID("season-a1", app.Upstream.Clients()[0].ID)
		app.IDStore.AssociateAdditionalInstance(virtualSeason, "season-b1", app.Upstream.Clients()[1].ID)

		rr := doJSONRequest(t, handler, http.MethodGet, "/Shows/"+virtualSeries+"/Episodes?SeasonId="+virtualSeason, nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("episodes status = %d, body=%s", rr.Code, rr.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal episodes: %v", err)
		}
		items, _ := payload["Items"].([]any)
		if len(items) != 2 {
			t.Fatalf("episode count = %d, want 2 payload=%#v", len(items), payload)
		}
		if got, _ := primarySeasonID.Load().(string); got != "season-a1" {
			t.Fatalf("primary season translation = %q, want season-a1", got)
		}
		if got, _ := secondarySeasonID.Load().(string); got != "season-b1" {
			t.Fatalf("secondary season translation = %q, want season-b1", got)
		}
		first := items[0].(map[string]any)
		firstID, _ := first["Id"].(string)
		resolved := app.IDStore.ResolveVirtualID(firstID)
		if resolved == nil || len(resolved.OtherInstances) != 1 || resolved.OtherInstances[0].OriginalID != "ep-b1" {
			t.Fatalf("episode duplicate association missing: %#v", resolved)
		}
	})
}

func TestSearchHintsAggregatesAndRewritesIDs(t *testing.T) {
	var primaryUserID atomic.Value
	var secondaryUserID atomic.Value

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-a", "User": map[string]any{"Id": "user-a"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Search/Hints":
			primaryUserID.Store(r.URL.Query().Get("UserId"))
			_ = json.NewEncoder(w).Encode(map[string]any{"SearchHints": []map[string]any{{"Id": "series-a", "Name": "Hint A"}}, "TotalRecordCount": 1})
		default:
			http.NotFound(w, r)
		}
	}))
	defer primary.Close()

	secondary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-b", "User": map[string]any{"Id": "user-b"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Search/Hints":
			secondaryUserID.Store(r.URL.Query().Get("UserId"))
			_ = json.NewEncoder(w).Encode(map[string]any{"SearchHints": []map[string]any{{"Id": "series-b", "Name": "Hint B"}}, "TotalRecordCount": 1})
		default:
			http.NotFound(w, r)
		}
	}))
	defer secondary.Close()

	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test Server\"\n  id: \"server-1\"\n\nadmin:\n  username: \"admin\"\n  password: \"secret\"\n\nplayback:\n  mode: \"proxy\"\n\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\n\nproxies: []\nupstream:\n  - name: \"A\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n  - name: \"B\"\n    url: %q\n    username: \"u2\"\n    password: \"p2\"\n", primary.URL, secondary.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		rr := doJSONRequest(t, handler, http.MethodGet, "/Search/Hints?SearchTerm=ruri", nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("search hints status = %d, body=%s", rr.Code, rr.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal search hints: %v", err)
		}
		hints, _ := payload["SearchHints"].([]any)
		if len(hints) != 2 {
			t.Fatalf("hint count = %d, want 2 payload=%#v", len(hints), payload)
		}
		if payload["TotalRecordCount"].(float64) != 2 {
			t.Fatalf("unexpected total count: %#v", payload)
		}
		first := hints[0].(map[string]any)
		if id, _ := first["Id"].(string); id == "series-a" || id == "series-b" || id == "" {
			t.Fatalf("expected rewritten search hint id, got %#v", first)
		}
		if got, _ := primaryUserID.Load().(string); got != "user-a" {
			t.Fatalf("primary UserId query = %q, want user-a", got)
		}
		if got, _ := secondaryUserID.Load().(string); got != "user-b" {
			t.Fatalf("secondary UserId query = %q, want user-b", got)
		}
	})
}

func TestImageProxyStreamsBytesAndSupportsEmbyPrefix(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-a", "User": map[string]any{"Id": "user-a"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Items/item-a/Images/Primary/0":
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("jpeg-bytes"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test Server\"\n  id: \"server-1\"\n\nadmin:\n  username: \"admin\"\n  password: \"secret\"\n\nplayback:\n  mode: \"proxy\"\n\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\n\nproxies: []\nupstream:\n  - name: \"A\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n", upstream.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		virtualItem := app.IDStore.GetOrCreateVirtualID("item-a", app.Upstream.Clients()[0].ID)

		req := httptest.NewRequest(http.MethodGet, "/emby/Items/"+virtualItem+"/Images/Primary/0?api_key="+token, nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("image status = %d, body=%s", rr.Code, rr.Body.String())
		}
		if got := rr.Header().Get("Cache-Control"); !strings.Contains(got, "max-age=86400") {
			t.Fatalf("unexpected cache-control: %q", got)
		}
		if got := rr.Header().Get("Content-Type"); got != "image/jpeg" {
			t.Fatalf("unexpected image content type: %q", got)
		}
		if rr.Body.String() != "jpeg-bytes" {
			t.Fatalf("unexpected image body: %q", rr.Body.String())
		}
	})
}
