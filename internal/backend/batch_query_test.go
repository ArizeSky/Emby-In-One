package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestItemsBatchQueryTranslatesDedupedMovieIDsAcrossServers(t *testing.T) {
	var primaryIDs atomic.Value
	var secondaryIDs atomic.Value

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-a", "User": map[string]any{"Id": "user-a"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Items":
			primaryIDs.Store(r.URL.Query().Get("Ids"))
			items := []map[string]any{}
			if r.URL.Query().Get("Ids") == "movie-a" {
				items = append(items, map[string]any{
					"Id":             "movie-a",
					"Type":           "Movie",
					"Name":           "Movie A",
					"ProductionYear": 2024,
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": items, "TotalRecordCount": len(items), "StartIndex": 0})
		default:
			http.NotFound(w, r)
		}
	}))
	defer primary.Close()

	secondary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-b", "User": map[string]any{"Id": "user-b"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Items":
			secondaryIDs.Store(r.URL.Query().Get("Ids"))
			items := []map[string]any{}
			if r.URL.Query().Get("Ids") == "movie-b" {
				items = append(items, map[string]any{
					"Id":             "movie-b",
					"Type":           "Movie",
					"Name":           "Movie A",
					"ProductionYear": 2024,
				})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": items, "TotalRecordCount": len(items), "StartIndex": 0})
		default:
			http.NotFound(w, r)
		}
	}))
	defer secondary.Close()

	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test Server\"\n  id: \"server-1\"\n\nadmin:\n  username: \"admin\"\n  password: \"secret\"\n\nplayback:\n  mode: \"proxy\"\n\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\n\nproxies: []\nupstream:\n  - name: \"A\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n  - name: \"B\"\n    url: %q\n    username: \"u2\"\n    password: \"p2\"\n", primary.URL, secondary.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		virtualMovie := app.IDStore.GetOrCreateVirtualID("movie-a", 0)
		app.IDStore.AssociateAdditionalInstance(virtualMovie, "movie-b", 1)

		rr := doJSONRequest(t, handler, http.MethodGet, "/Items?Ids="+virtualMovie, nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("items batch status = %d, body=%s", rr.Code, rr.Body.String())
		}

		var payload map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal items batch: %v", err)
		}

		items, _ := payload["Items"].([]any)
		if len(items) != 1 {
			t.Fatalf("items batch count = %d, want 1 payload=%#v", len(items), payload)
		}
		item := items[0].(map[string]any)
		if item["Id"] == "movie-a" || item["Id"] == "movie-b" || item["Id"] == "" {
			t.Fatalf("expected rewritten movie id, got %#v", item)
		}
		if got, _ := primaryIDs.Load().(string); got != "movie-a" {
			t.Fatalf("primary Ids query = %q, want movie-a", got)
		}
		if got, _ := secondaryIDs.Load().(string); got != "movie-b" {
			t.Fatalf("secondary Ids query = %q, want movie-b", got)
		}
	})
}

func TestPersonsQueryTranslatesCommaSeparatedVirtualIDs(t *testing.T) {
	var upstreamIDs atomic.Value
	var upstreamUserID atomic.Value

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-a", "User": map[string]any{"Id": "user-a"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Persons":
			upstreamIDs.Store(r.URL.Query().Get("Ids"))
			upstreamUserID.Store(r.URL.Query().Get("UserId"))
			items := []map[string]any{}
			if r.URL.Query().Get("Ids") == "person-a,person-b" {
				items = append(items,
					map[string]any{"Id": "person-a", "Name": "Actor A", "Type": "Person"},
					map[string]any{"Id": "person-b", "Name": "Actor B", "Type": "Person"},
				)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": items, "TotalRecordCount": len(items), "StartIndex": 0})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test Server\"\n  id: \"server-1\"\n\nadmin:\n  username: \"admin\"\n  password: \"secret\"\n\nplayback:\n  mode: \"proxy\"\n\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\n\nproxies: []\nupstream:\n  - name: \"A\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n", upstream.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		virtualPersonA := app.IDStore.GetOrCreateVirtualID("person-a", 0)
		virtualPersonB := app.IDStore.GetOrCreateVirtualID("person-b", 0)

		rr := doJSONRequest(t, handler, http.MethodGet, "/Persons?Ids="+virtualPersonA+","+virtualPersonB, nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("persons status = %d, body=%s", rr.Code, rr.Body.String())
		}

		var payload map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal persons: %v", err)
		}

		items, _ := payload["Items"].([]any)
		if len(items) != 2 {
			t.Fatalf("persons count = %d, want 2 payload=%#v", len(items), payload)
		}
		for _, raw := range items {
			item := raw.(map[string]any)
			if item["Id"] == "person-a" || item["Id"] == "person-b" || item["Id"] == "" {
				t.Fatalf("expected rewritten person id, got %#v", item)
			}
		}
		if got, _ := upstreamIDs.Load().(string); got != "person-a,person-b" {
			t.Fatalf("persons Ids query = %q, want person-a,person-b", got)
		}
		if got, _ := upstreamUserID.Load().(string); got != "user-a" {
			t.Fatalf("persons UserId query = %q, want user-a", got)
		}
	})
}

func TestTranslateBatchIDQuery_RejectsOversized(t *testing.T) {
	store, _ := NewIDStore("", nil)
	defer store.Close()

	values := url.Values{}
	ids := make([]string, maxBatchIDCount+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("id-%d", i)
	}
	values.Set("Ids", strings.Join(ids, ","))
	_, ok := translateBatchIDQueryForServer(values, 0, store)
	if ok {
		t.Error("expected oversized batch to be rejected")
	}
}

func TestTranslateBatchIDQuery_AcceptsWithinLimit(t *testing.T) {
	store, _ := NewIDStore("", nil)
	defer store.Close()

	values := url.Values{}
	ids := make([]string, 10)
	for i := range ids {
		ids[i] = fmt.Sprintf("id-%d", i)
	}
	values.Set("Ids", strings.Join(ids, ","))
	_, ok := translateBatchIDQueryForServer(values, 0, store)
	if !ok {
		t.Error("expected batch within limit to be accepted")
	}
}
