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

func TestLibraryFolderEndpointsMergeAndTagSources(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-a", "User": map[string]any{"Id": "user-a"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Library/VirtualFolders":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"Id": "vf-a", "Name": "Movies"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Library/SelectableRemoteLibraries":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"Id": "sr-a", "Name": "Remote Movies"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Library/MediaFolders":
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": []map[string]any{{"Id": "mf-a", "Name": "Folder A"}}, "TotalRecordCount": 1})
		default:
			http.NotFound(w, r)
		}
	}))
	defer primary.Close()

	secondary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-b", "User": map[string]any{"Id": "user-b"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Library/VirtualFolders":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"Id": "vf-b", "Name": "Movies"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Library/SelectableRemoteLibraries":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"Id": "sr-b", "Name": "Remote Movies"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Library/MediaFolders":
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": []map[string]any{{"Id": "mf-b", "Name": "Folder B"}}, "TotalRecordCount": 1})
		default:
			http.NotFound(w, r)
		}
	}))
	defer secondary.Close()

	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test Server\"\n  id: \"server-1\"\n\nadmin:\n  username: \"admin\"\n  password: \"secret\"\n\nplayback:\n  mode: \"proxy\"\n\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\n\nproxies: []\nupstream:\n  - name: \"A\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n  - name: \"B\"\n    url: %q\n    username: \"u2\"\n    password: \"p2\"\n", primary.URL, secondary.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")

		for _, route := range []string{"/Library/VirtualFolders", "/Library/SelectableRemoteLibraries"} {
			rr := doJSONRequest(t, handler, http.MethodGet, route, nil, token)
			if rr.Code != http.StatusOK {
				t.Fatalf("%s status = %d, body=%s", route, rr.Code, rr.Body.String())
			}
			var items []map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &items); err != nil {
				t.Fatalf("unmarshal %s: %v", route, err)
			}
			if len(items) != 2 {
				t.Fatalf("%s count = %d, want 2", route, len(items))
			}
			joined := items[0]["Name"].(string) + " | " + items[1]["Name"].(string)
			if !strings.Contains(joined, "(A)") || !strings.Contains(joined, "(B)") {
				t.Fatalf("expected tagged names for %s, got %q", route, joined)
			}
			if items[0]["Id"] == "vf-a" || items[0]["Id"] == "sr-a" {
				t.Fatalf("expected rewritten ids for %s, got %#v", route, items[0])
			}
		}

		rr := doJSONRequest(t, handler, http.MethodGet, "/Library/MediaFolders", nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("media folders status = %d, body=%s", rr.Code, rr.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal media folders: %v", err)
		}
		items, _ := payload["Items"].([]any)
		if len(items) != 2 {
			t.Fatalf("media folders count = %d, want 2 payload=%#v", len(items), payload)
		}
	})
}

func TestLibraryTaxonomyEndpointsMergeAndInjectUserID(t *testing.T) {
	var genresUserA atomic.Value
	var genresUserB atomic.Value
	var albumArtistsUserA atomic.Value
	var albumArtistsUserB atomic.Value

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-a", "User": map[string]any{"Id": "user-a"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Genres":
			genresUserA.Store(r.URL.Query().Get("UserId"))
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": []map[string]any{{"Id": "genre-a", "Name": "Drama"}}, "TotalRecordCount": 1})
		case r.Method == http.MethodGet && r.URL.Path == "/Artists/AlbumArtists":
			albumArtistsUserA.Store(r.URL.Query().Get("UserId"))
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": []map[string]any{{"Id": "artist-a", "Name": "Artist A"}}, "TotalRecordCount": 1})
		default:
			http.NotFound(w, r)
		}
	}))
	defer primary.Close()

	secondary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-b", "User": map[string]any{"Id": "user-b"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Genres":
			genresUserB.Store(r.URL.Query().Get("UserId"))
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": []map[string]any{{"Id": "genre-b", "Name": "Comedy"}}, "TotalRecordCount": 1})
		case r.Method == http.MethodGet && r.URL.Path == "/Artists/AlbumArtists":
			albumArtistsUserB.Store(r.URL.Query().Get("UserId"))
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": []map[string]any{{"Id": "artist-b", "Name": "Artist B"}}, "TotalRecordCount": 1})
		default:
			http.NotFound(w, r)
		}
	}))
	defer secondary.Close()

	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test Server\"\n  id: \"server-1\"\n\nadmin:\n  username: \"admin\"\n  password: \"secret\"\n\nplayback:\n  mode: \"proxy\"\n\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\n\nproxies: []\nupstream:\n  - name: \"A\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n  - name: \"B\"\n    url: %q\n    username: \"u2\"\n    password: \"p2\"\n", primary.URL, secondary.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")

		for _, route := range []string{"/Genres", "/Artists/AlbumArtists"} {
			rr := doJSONRequest(t, handler, http.MethodGet, route, nil, token)
			if rr.Code != http.StatusOK {
				t.Fatalf("%s status = %d, body=%s", route, rr.Code, rr.Body.String())
			}
			var payload map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
				t.Fatalf("unmarshal %s: %v", route, err)
			}
			items, _ := payload["Items"].([]any)
			if len(items) != 2 {
				t.Fatalf("%s count = %d, want 2 payload=%#v", route, len(items), payload)
			}
		}

		if got, _ := genresUserA.Load().(string); got != "user-a" {
			t.Fatalf("genres user A = %q, want user-a", got)
		}
		if got, _ := genresUserB.Load().(string); got != "user-b" {
			t.Fatalf("genres user B = %q, want user-b", got)
		}
		if got, _ := albumArtistsUserA.Load().(string); got != "user-a" {
			t.Fatalf("album artists user A = %q, want user-a", got)
		}
		if got, _ := albumArtistsUserB.Load().(string); got != "user-b" {
			t.Fatalf("album artists user B = %q, want user-b", got)
		}
	})
}
