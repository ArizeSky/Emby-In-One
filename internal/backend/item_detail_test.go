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

func TestUserItemsLatestSupportsParentRoutingAndCrossServerMerge(t *testing.T) {
	var primaryParent atomic.Value
	var secondaryCalls atomic.Int32

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-a", "User": map[string]any{"Id": "user-a"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Users/user-a/Items/Latest":
			primaryParent.Store(r.URL.Query().Get("ParentId"))
			_ = json.NewEncoder(w).Encode([]map[string]any{{"Id": "latest-a", "ParentId": "parent-a", "Name": "Latest A"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer primary.Close()

	secondary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-b", "User": map[string]any{"Id": "user-b"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Users/user-b/Items/Latest":
			secondaryCalls.Add(1)
			_ = json.NewEncoder(w).Encode([]map[string]any{{"Id": "latest-b", "ParentId": "parent-b", "Name": "Latest B"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer secondary.Close()

	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test Server\"\n  id: \"server-1\"\n\nadmin:\n  username: \"admin\"\n  password: \"secret\"\n\nplayback:\n  mode: \"proxy\"\n\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\n\nproxies: []\nupstream:\n  - name: \"A\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n  - name: \"B\"\n    url: %q\n    username: \"u2\"\n    password: \"p2\"\n", primary.URL, secondary.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		virtualParent := app.IDStore.GetOrCreateVirtualID("parent-a", app.Upstream.Clients()[0].ID)

		rr := doJSONRequest(t, handler, http.MethodGet, "/Users/"+app.Auth.ProxyUserID()+"/Items/Latest?ParentId="+virtualParent, nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("latest parent status = %d, body=%s", rr.Code, rr.Body.String())
		}
		var items []map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &items); err != nil {
			t.Fatalf("unmarshal latest parent: %v", err)
		}
		if len(items) != 1 {
			t.Fatalf("latest parent count = %d, want 1", len(items))
		}
		if got, _ := primaryParent.Load().(string); got != "parent-a" {
			t.Fatalf("ParentId translation = %q, want parent-a", got)
		}
		if items[0]["Id"] == "latest-a" || items[0]["ParentId"] == "parent-a" {
			t.Fatalf("latest parent ids not rewritten: %#v", items[0])
		}

		rr = doJSONRequest(t, handler, http.MethodGet, "/Users/"+app.Auth.ProxyUserID()+"/Items/Latest", nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("latest merged status = %d, body=%s", rr.Code, rr.Body.String())
		}
		items = nil
		if err := json.Unmarshal(rr.Body.Bytes(), &items); err != nil {
			t.Fatalf("unmarshal latest merged: %v", err)
		}
		if len(items) != 2 {
			t.Fatalf("latest merged count = %d, want 2 payload=%s", len(items), rr.Body.String())
		}
		if secondaryCalls.Load() == 0 {
			t.Fatalf("expected secondary latest to be queried")
		}
	})
}

func TestUserItemDetailMergesMediaSourcesAcrossInstances(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-a", "User": map[string]any{"Id": "user-a"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Users/user-a/Items/item-a":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":           "item-a",
				"Name":         "Merged Item",
				"MediaSources": []map[string]any{{"Id": "ms-a", "Name": "Primary"}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer primary.Close()

	secondary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-b", "User": map[string]any{"Id": "user-b"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Users/user-b/Items/item-b":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":           "item-b",
				"Name":         "Merged Item Alt",
				"MediaSources": []map[string]any{{"Id": "ms-b", "Name": "Secondary"}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer secondary.Close()

	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test Server\"\n  id: \"server-1\"\n\nadmin:\n  username: \"admin\"\n  password: \"secret\"\n\nplayback:\n  mode: \"proxy\"\n\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\n\nproxies: []\nupstream:\n  - name: \"A\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n  - name: \"B\"\n    url: %q\n    username: \"u2\"\n    password: \"p2\"\n", primary.URL, secondary.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		virtualItem := app.IDStore.GetOrCreateVirtualID("item-a", app.Upstream.Clients()[0].ID)
		app.IDStore.AssociateAdditionalInstance(virtualItem, "item-b", app.Upstream.Clients()[1].ID)

		rr := doJSONRequest(t, handler, http.MethodGet, "/Users/"+app.Auth.ProxyUserID()+"/Items/"+virtualItem, nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("user item detail status = %d, body=%s", rr.Code, rr.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal item detail: %v", err)
		}
		if payload["Id"] == "item-a" {
			t.Fatalf("item detail id not rewritten: %#v", payload)
		}
		mediaSources, _ := payload["MediaSources"].([]any)
		if len(mediaSources) != 2 {
			t.Fatalf("media source count = %d, want 2 payload=%#v", len(mediaSources), payload)
		}
		names := []string{}
		for _, raw := range mediaSources {
			ms := raw.(map[string]any)
			if ms["Id"] == "ms-a" || ms["Id"] == "ms-b" {
				t.Fatalf("media source id not rewritten: %#v", ms)
			}
			names = append(names, ms["Name"].(string))
		}
		joined := strings.Join(names, " | ")
		if !strings.Contains(joined, "[A]") || !strings.Contains(joined, "[B]") {
			t.Fatalf("expected source-tagged media source names, got %q", joined)
		}
	})
}

func TestItemDetailRelatedRoutesRewriteIDs(t *testing.T) {
	var similarUserID atomic.Value
	var themeUserID atomic.Value

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-a", "User": map[string]any{"Id": "user-a"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Items/item-a":
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "item-a", "ParentId": "parent-a", "Name": "Item A"})
		case r.Method == http.MethodGet && r.URL.Path == "/Items/item-a/Similar":
			similarUserID.Store(r.URL.Query().Get("UserId"))
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": []map[string]any{{"Id": "similar-a", "ParentId": "item-a"}}, "TotalRecordCount": 1})
		case r.Method == http.MethodGet && r.URL.Path == "/Items/item-a/ThemeMedia":
			themeUserID.Store(r.URL.Query().Get("UserId"))
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ThemeVideosResult":     map[string]any{"Items": []map[string]any{{"Id": "theme-video-a"}}, "TotalRecordCount": 1},
				"ThemeSongsResult":      map[string]any{"Items": []map[string]any{{"Id": "theme-song-a"}}, "TotalRecordCount": 1},
				"SoundtrackSongsResult": map[string]any{"Items": []map[string]any{}, "TotalRecordCount": 0},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test Server\"\n  id: \"server-1\"\n\nadmin:\n  username: \"admin\"\n  password: \"secret\"\n\nplayback:\n  mode: \"proxy\"\n\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\n\nproxies: []\nupstream:\n  - name: \"A\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n", upstream.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		virtualItem := app.IDStore.GetOrCreateVirtualID("item-a", app.Upstream.Clients()[0].ID)

		rr := doJSONRequest(t, handler, http.MethodGet, "/Items/"+virtualItem, nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("item by id status = %d, body=%s", rr.Code, rr.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal item by id: %v", err)
		}
		if payload["Id"] == "item-a" || payload["ParentId"] == "parent-a" {
			t.Fatalf("item by id ids not rewritten: %#v", payload)
		}

		rr = doJSONRequest(t, handler, http.MethodGet, "/Items/"+virtualItem+"/Similar", nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("similar status = %d, body=%s", rr.Code, rr.Body.String())
		}
		payload = map[string]any{}
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal similar: %v", err)
		}
		items, _ := payload["Items"].([]any)
		if len(items) != 1 {
			t.Fatalf("similar item count = %d, want 1", len(items))
		}
		if got, _ := similarUserID.Load().(string); got != "user-a" {
			t.Fatalf("similar UserId query = %q, want user-a", got)
		}
		similarItem := items[0].(map[string]any)
		if similarItem["Id"] == "similar-a" || similarItem["ParentId"] == "item-a" {
			t.Fatalf("similar ids not rewritten: %#v", similarItem)
		}

		rr = doJSONRequest(t, handler, http.MethodGet, "/Items/"+virtualItem+"/ThemeMedia", nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("theme media status = %d, body=%s", rr.Code, rr.Body.String())
		}
		payload = map[string]any{}
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal theme media: %v", err)
		}
		if got, _ := themeUserID.Load().(string); got != "user-a" {
			t.Fatalf("theme media UserId query = %q, want user-a", got)
		}
		videos := payload["ThemeVideosResult"].(map[string]any)["Items"].([]any)
		if videos[0].(map[string]any)["Id"] == "theme-video-a" {
			t.Fatalf("theme video ids not rewritten: %#v", payload)
		}

		rr = doJSONRequest(t, handler, http.MethodGet, "/Items/not-a-virtual/ThemeMedia", nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("theme media empty status = %d, body=%s", rr.Code, rr.Body.String())
		}
		payload = map[string]any{}
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal theme media empty: %v", err)
		}
		if payload["ThemeVideosResult"].(map[string]any)["TotalRecordCount"].(float64) != 0 {
			t.Fatalf("expected empty theme media payload, got %#v", payload)
		}
	})
}
