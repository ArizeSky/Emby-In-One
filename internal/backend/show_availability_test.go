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

func TestShowListsKeepAvailableCopyWhenSiblingFails(t *testing.T) {
	var primaryFailed atomic.Bool
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Users/AuthenticateByName" {
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-a", "User": map[string]any{"Id": "user-a"}})
			return
		}
		if primaryFailed.Load() {
			w.WriteHeader(503)
			return
		}
		item := map[string]any{"Id": "season-a", "Type": "Season", "IndexNumber": 1, "SeriesId": "series-a"}
		if strings.HasSuffix(r.URL.Path, "/Episodes") {
			item = map[string]any{"Id": "episode-a", "Type": "Episode", "ParentIndexNumber": 1, "IndexNumber": 3, "SeriesId": "series-a", "SeasonId": "season-a"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Items": []any{item}, "TotalRecordCount": 1})
	}))
	defer primary.Close()
	secondary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Users/AuthenticateByName" {
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-b", "User": map[string]any{"Id": "user-b"}})
			return
		}
		w.WriteHeader(404)
	}))
	defer secondary.Close()
	withTempAppConfig(t, dualUpstreamConfig(primary.URL, secondary.URL), func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		first, second := app.Upstream.Clients()[0], app.Upstream.Clients()[1]
		series := app.IDStore.GetOrCreateVirtualID("series-a", first.ID)
		app.IDStore.AssociateAdditionalInstance(series, "series-b", second.ID)
		for _, suffix := range []string{"Seasons", "Episodes"} {
			rr := doJSONRequest(t, handler, "GET", "/Shows/"+series+"/"+suffix, nil, token)
			var payload map[string]any
			if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &payload) != nil {
				t.Fatalf("available %s was hidden: status=%d body=%s", suffix, rr.Code, rr.Body.String())
			}
			if len(asItems(payload)) != 1 || rr.Header().Get("X-EIO-Results-Incomplete") != "true" || payload["EioIncompleteResults"] != true {
				t.Fatalf("partial %s did not retain data and mark its lower-bound total", suffix)
			}
			id := playbackStringField(asItems(payload)[0], "Id")
			mapping := app.IDStore.ResolveVirtualID(id)
			if mapping == nil || mapping.ServerID != first.ID || len(mapping.OtherInstances) != 0 {
				t.Fatal("failed sibling was invented as a serving instance")
			}
		}
		primaryFailed.Store(true)
		for _, suffix := range []string{"Seasons", "Episodes"} {
			rr := doJSONRequest(t, handler, "GET", "/Shows/"+series+"/"+suffix, nil, token)
			if rr.Code != 502 || !strings.Contains(rr.Body.String(), "show-list-unavailable") {
				t.Fatal("all failed instances became a successful empty list")
			}
		}
	})
}

func TestShowListKeepsEarlierPagesAfterLaterPageFailure(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Users/AuthenticateByName" {
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-a", "User": map[string]any{"Id": "user-a"}})
			return
		}
		if r.URL.Query().Get("StartIndex") != "0" {
			w.WriteHeader(503)
			return
		}
		items := make([]map[string]any, 200)
		for i := range items {
			items[i] = map[string]any{"Id": fmt.Sprint("episode-", i), "Type": "Episode", "SeriesId": "series-a", "ParentIndexNumber": 1, "IndexNumber": i + 1}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Items": items, "TotalRecordCount": 250})
	}))
	defer up.Close()
	withTempAppConfig(t, singleUpstreamConfig(up.URL), func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		series := app.IDStore.GetOrCreateVirtualID("series-a", app.Upstream.Clients()[0].ID)
		rr := doJSONRequest(t, handler, "GET", "/Shows/"+series+"/Episodes?StartIndex=198&Limit=5", nil, token)
		var payload map[string]any
		if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &payload) != nil {
			t.Fatalf("confirmed page was discarded: status=%d", rr.Code)
		}
		if len(asItems(payload)) != 2 || payload["TotalRecordCount"] != float64(200) || payload["EioIncompleteResults"] != true {
			t.Fatal("partial paging lost the known prefix or claimed the upstream's unconfirmed total")
		}
	})
}
