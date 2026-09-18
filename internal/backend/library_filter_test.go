package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// homeLibraryUpstreamStub serves one upstream with two libraries (lib-1
// "Movies", lib-2 "Shows") through every listing endpoint, plus a root item
// list mixing a library view with a content item. The returned counter tracks
// how often the upstream's Views endpoint was hit, so cache behaviour can be
// asserted.
func homeLibraryUpstreamStub(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	viewsHits := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-a", "User": map[string]any{"Id": "user-a"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Users/user-a/Views":
			viewsHits.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": []map[string]any{
				{"Id": "lib-1", "Name": "Movies", "CollectionType": "movies"},
				{"Id": "lib-2", "Name": "Shows", "CollectionType": "tvshows"},
			}, "TotalRecordCount": 2})
		case r.Method == http.MethodGet && r.URL.Path == "/Library/MediaFolders":
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": []map[string]any{
				{"Id": "lib-1", "Name": "Movies"},
				{"Id": "lib-2", "Name": "Shows"},
			}})
		case r.Method == http.MethodGet && (r.URL.Path == "/Library/VirtualFolders" || r.URL.Path == "/Library/SelectableRemoteLibraries"):
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{"Id": "lib-1", "Name": "Movies"},
				{"Id": "lib-2", "Name": "Shows"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/Users/user-a/Items":
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": []map[string]any{
				{"Id": "lib-1", "Name": "Movies", "Type": "UserView"},
				{"Id": "movie-1", "Name": "Hidden Library Movie", "Type": "Movie"},
			}, "TotalRecordCount": 2})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, viewsHits
}

func withHomeLibraryApp(t *testing.T, fn func(app *App, handler http.Handler, viewsHits *atomic.Int32)) {
	t.Helper()
	upstream, viewsHits := homeLibraryUpstreamStub(t)
	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test Server\"\n  id: \"server-1\"\n\nadmin:\n  username: \"admin\"\n  password: \"secret\"\n\nplayback:\n  mode: \"proxy\"\n\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\n\nproxies: []\nupstream:\n  - name: \"A\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n", upstream.URL)
	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		fn(app, handler, viewsHits)
	})
}

// hideMovieLibrary hides lib-1 for the administrator, the identity every test
// request logs in as.
func hideMovieLibrary(t *testing.T, app *App) {
	t.Helper()
	if err := app.HiddenLibraries.SetServerHidden(adminVisibilityUserID, app.Upstream.Clients()[0].ID, []string{"lib-1"}); err != nil {
		t.Fatalf("SetServerHidden: %v", err)
	}
}

func itemNamesFromPayload(t *testing.T, body []byte) []string {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	rawItems, _ := payload["Items"].([]any)
	return itemNamesFromSlice(t, rawItems)
}

func itemNamesFromSlice(t *testing.T, rawItems []any) []string {
	t.Helper()
	names := make([]string, 0, len(rawItems))
	for _, raw := range rawItems {
		if item, ok := raw.(map[string]any); ok {
			if name, _ := item["Name"].(string); name != "" {
				names = append(names, name)
			}
		}
	}
	return names
}

func hasName(names []string, want string) bool {
	for _, name := range names {
		if name == want {
			return true
		}
	}
	return false
}

func TestUserViewsHiddenLibraryFiltered(t *testing.T) {
	withHomeLibraryApp(t, func(app *App, handler http.Handler, viewsHits *atomic.Int32) {
		token := loginToken(t, handler, "secret")
		hideMovieLibrary(t, app)

		rr := doJSONRequest(t, handler, http.MethodGet, "/Users/"+app.Auth.ProxyUserID()+"/Views", nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("views status = %d, body=%s", rr.Code, rr.Body.String())
		}
		names := itemNamesFromPayload(t, rr.Body.Bytes())
		if hasName(names, "Movies") {
			t.Fatalf("hidden library must not be listed, got %v", names)
		}
		if !hasName(names, "Shows") {
			t.Fatalf("visible library must stay listed, got %v", names)
		}
	})
}

func TestMediaFoldersHiddenLibraryFiltered(t *testing.T) {
	withHomeLibraryApp(t, func(app *App, handler http.Handler, viewsHits *atomic.Int32) {
		token := loginToken(t, handler, "secret")
		hideMovieLibrary(t, app)

		rr := doJSONRequest(t, handler, http.MethodGet, "/Library/MediaFolders", nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("media folders status = %d, body=%s", rr.Code, rr.Body.String())
		}
		names := itemNamesFromPayload(t, rr.Body.Bytes())
		if hasName(names, "Movies") || !hasName(names, "Shows") {
			t.Fatalf("media folders filtering wrong, got %v", names)
		}
	})
}

func TestVirtualFoldersHiddenLibraryFiltered(t *testing.T) {
	withHomeLibraryApp(t, func(app *App, handler http.Handler, viewsHits *atomic.Int32) {
		token := loginToken(t, handler, "secret")
		hideMovieLibrary(t, app)

		for _, path := range []string{"/Library/VirtualFolders", "/Library/SelectableRemoteLibraries"} {
			rr := doJSONRequest(t, handler, http.MethodGet, path, nil, token)
			if rr.Code != http.StatusOK {
				t.Fatalf("%s status = %d, body=%s", path, rr.Code, rr.Body.String())
			}
			var rawItems []any
			if err := json.Unmarshal(rr.Body.Bytes(), &rawItems); err != nil {
				t.Fatalf("unmarshal %s: %v", path, err)
			}
			names := itemNamesFromSlice(t, rawItems)
			if hasName(names, "Movies") || !hasName(names, "Shows") {
				t.Fatalf("%s filtering wrong, got %v", path, names)
			}
		}
	})
}

func TestUserItemsRootDropsHiddenViews(t *testing.T) {
	withHomeLibraryApp(t, func(app *App, handler http.Handler, viewsHits *atomic.Int32) {
		token := loginToken(t, handler, "secret")
		hideMovieLibrary(t, app)

		rr := doJSONRequest(t, handler, http.MethodGet, "/Users/"+app.Auth.ProxyUserID()+"/Items", nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("root items status = %d, body=%s", rr.Code, rr.Body.String())
		}
		names := itemNamesFromPayload(t, rr.Body.Bytes())
		if hasName(names, "Movies") {
			t.Fatalf("hidden library view must be dropped from the root listing, got %v", names)
		}
		if !hasName(names, "Hidden Library Movie") {
			t.Fatalf("content items must never be dropped, got %v", names)
		}
	})
}

func TestUserItemsParentPathUnfiltered(t *testing.T) {
	withHomeLibraryApp(t, func(app *App, handler http.Handler, viewsHits *atomic.Int32) {
		token := loginToken(t, handler, "secret")
		hideMovieLibrary(t, app)

		// Browsing straight into a hidden library via its virtual id stays
		// possible: hiding is a homepage-display refinement, not access control.
		virtualLibrary := app.IDStore.GetOrCreateVirtualID("lib-1", app.Upstream.Clients()[0].ID)
		rr := doJSONRequest(t, handler, http.MethodGet, "/Users/"+app.Auth.ProxyUserID()+"/Items?ParentId="+virtualLibrary, nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("parented items status = %d, body=%s", rr.Code, rr.Body.String())
		}
		names := itemNamesFromPayload(t, rr.Body.Bytes())
		if len(names) == 0 {
			t.Fatalf("parented browse into a hidden library must keep working")
		}
	})
}

func TestHiddenLibrariesDisabledWithoutDB(t *testing.T) {
	withHomeLibraryApp(t, func(app *App, handler http.Handler, viewsHits *atomic.Int32) {
		token := loginToken(t, handler, "secret")
		app.HiddenLibraries = nil // the degraded mode a broken database produces

		rr := doJSONRequest(t, handler, http.MethodGet, "/Users/"+app.Auth.ProxyUserID()+"/Views", nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("views status = %d, body=%s", rr.Code, rr.Body.String())
		}
		names := itemNamesFromPayload(t, rr.Body.Bytes())
		if !hasName(names, "Movies") || !hasName(names, "Shows") {
			t.Fatalf("nil store must disable filtering entirely, got %v", names)
		}
	})
}
