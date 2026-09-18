package backend

import (
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
)

// adminHomeLibrariesHidden fetches the admin's own hidden config through the
// API, asserting success on the way.
func adminHomeLibrariesHidden(t *testing.T, handler http.Handler, token string) map[string]any {
	t.Helper()
	rr := doJSONRequest(t, handler, http.MethodGet, "/admin/api/home-libraries", nil, token)
	if rr.Code != http.StatusOK {
		t.Fatalf("get home-libraries status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal home-libraries: %v", err)
	}
	hidden, _ := payload["hidden"].(map[string]any)
	return hidden
}

func hiddenIDCount(hidden map[string]any, serverID string) int {
	ids, _ := hidden[serverID].([]any)
	return len(ids)
}

func TestAdminUpstreamLibrariesEndpoint(t *testing.T) {
	withHomeLibraryApp(t, func(app *App, handler http.Handler, viewsHits *atomic.Int32) {
		token := loginToken(t, handler, "secret")
		serverID := app.Upstream.Clients()[0].ID

		rr := doJSONRequest(t, handler, http.MethodGet, "/admin/api/upstream/"+serverID+"/libraries", nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("libraries status = %d, body=%s", rr.Code, rr.Body.String())
		}
		var libraries []map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &libraries); err != nil {
			t.Fatalf("unmarshal libraries: %v", err)
		}
		if len(libraries) != 2 {
			t.Fatalf("libraries len = %d, want 2: %#v", len(libraries), libraries)
		}
		if libraries[0]["id"] != "lib-1" || libraries[0]["name"] != "Movies" || libraries[0]["collectionType"] != "movies" {
			t.Fatalf("unexpected library entry: %#v", libraries[0])
		}

		// The second call within the TTL must be served from cache: the
		// upstream's Views endpoint is hit exactly once so far.
		rr = doJSONRequest(t, handler, http.MethodGet, "/admin/api/upstream/"+serverID+"/libraries", nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("cached libraries status = %d", rr.Code)
		}
		if got := viewsHits.Load(); got != 1 {
			t.Fatalf("upstream Views hits = %d, want 1 (cache must absorb the second call)", got)
		}

		// refresh=1 bypasses the cache and hits the upstream again.
		rr = doJSONRequest(t, handler, http.MethodGet, "/admin/api/upstream/"+serverID+"/libraries?refresh=1", nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("refreshed libraries status = %d", rr.Code)
		}
		if got := viewsHits.Load(); got != 2 {
			t.Fatalf("upstream Views hits = %d, want 2 after refresh", got)
		}
	})
}

func TestAdminUpstreamLibrariesOffline(t *testing.T) {
	// Offline with no cache at all: the failure is reported.
	withHomeLibraryApp(t, func(app *App, handler http.Handler, viewsHits *atomic.Int32) {
		token := loginToken(t, handler, "secret")
		serverID := app.Upstream.Clients()[0].ID

		app.Upstream.ClientByID(serverID).setOffline("test offline")
		rr := doJSONRequest(t, handler, http.MethodGet, "/admin/api/upstream/"+serverID+"/libraries", nil, token)
		if rr.Code != http.StatusBadGateway {
			t.Fatalf("offline no-cache status = %d, want 502, body=%s", rr.Code, rr.Body.String())
		}
	})

	// Offline with a cached list: the stale entry keeps the panel usable.
	withHomeLibraryApp(t, func(app *App, handler http.Handler, viewsHits *atomic.Int32) {
		token := loginToken(t, handler, "secret")
		serverID := app.Upstream.Clients()[0].ID

		if rr := doJSONRequest(t, handler, http.MethodGet, "/admin/api/upstream/"+serverID+"/libraries", nil, token); rr.Code != http.StatusOK {
			t.Fatalf("warm-up libraries status = %d, body=%s", rr.Code, rr.Body.String())
		}
		app.Upstream.ClientByID(serverID).setOffline("test offline")
		rr := doJSONRequest(t, handler, http.MethodGet, "/admin/api/upstream/"+serverID+"/libraries", nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("offline stale-cache status = %d, want 200, body=%s", rr.Code, rr.Body.String())
		}
		var libraries []map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &libraries); err != nil {
			t.Fatalf("unmarshal stale libraries: %v", err)
		}
		if len(libraries) != 2 {
			t.Fatalf("stale libraries len = %d, want 2", len(libraries))
		}
	})
}

func TestAdminHomeLibrariesPatchSemantics(t *testing.T) {
	withHomeLibraryApp(t, func(app *App, handler http.Handler, viewsHits *atomic.Int32) {
		token := loginToken(t, handler, "secret")
		serverID := app.Upstream.Clients()[0].ID

		// Seed: serverID hides lib-1, "other" hides lib-9.
		seed := map[string]any{"hidden": map[string]any{
			serverID: []string{"lib-1"},
			"other":  []string{"lib-9"},
		}}
		if rr := doJSONRequest(t, handler, http.MethodPut, "/admin/api/home-libraries", seed, token); rr.Code != http.StatusOK {
			t.Fatalf("seed status = %d, body=%s", rr.Code, rr.Body.String())
		}

		// Absent key: "other" is not in the payload and must stay untouched,
		// while the present key replaces its own server.
		update := map[string]any{"hidden": map[string]any{serverID: []string{"lib-2", "lib-3"}}}
		if rr := doJSONRequest(t, handler, http.MethodPut, "/admin/api/home-libraries", update, token); rr.Code != http.StatusOK {
			t.Fatalf("update status = %d, body=%s", rr.Code, rr.Body.String())
		}
		hidden := adminHomeLibrariesHidden(t, handler, token)
		if got := hiddenIDCount(hidden, serverID); got != 2 {
			t.Fatalf("present key must replace its server, got %d ids: %#v", got, hidden)
		}
		if got := hiddenIDCount(hidden, "other"); got != 1 {
			t.Fatalf("absent key must be untouched, got %d ids: %#v", got, hidden)
		}

		// Null value behaves like an absent key.
		nullBody := `{"hidden": {"` + serverID + `": null}}`
		if rr := doJSONRequest(t, handler, http.MethodPut, "/admin/api/home-libraries", nullBody, token); rr.Code != http.StatusOK {
			t.Fatalf("null update status = %d, body=%s", rr.Code, rr.Body.String())
		}
		hidden = adminHomeLibrariesHidden(t, handler, token)
		if got := hiddenIDCount(hidden, serverID); got != 2 {
			t.Fatalf("null key must be untouched, got %d ids: %#v", got, hidden)
		}

		// An empty array explicitly clears that server only.
		clearBody := map[string]any{"hidden": map[string]any{serverID: []string{}}}
		if rr := doJSONRequest(t, handler, http.MethodPut, "/admin/api/home-libraries", clearBody, token); rr.Code != http.StatusOK {
			t.Fatalf("clear status = %d, body=%s", rr.Code, rr.Body.String())
		}
		hidden = adminHomeLibrariesHidden(t, handler, token)
		if _, stillThere := hidden[serverID]; stillThere {
			t.Fatalf("empty array must clear the server, got %#v", hidden)
		}
		if got := hiddenIDCount(hidden, "other"); got != 1 {
			t.Fatalf("clearing one server must not touch the other, got %#v", hidden)
		}
	})
}

func TestAdminUsersUpdateHiddenLibraries(t *testing.T) {
	withHomeLibraryApp(t, func(app *App, handler http.Handler, viewsHits *atomic.Int32) {
		token := loginToken(t, handler, "secret")
		serverID := app.Upstream.Clients()[0].ID

		createRR := doJSONRequest(t, handler, http.MethodPost, "/admin/api/users", map[string]any{
			"username": "bob", "password": "password123",
		}, token)
		if createRR.Code != http.StatusCreated {
			t.Fatalf("create user status = %d, body=%s", createRR.Code, createRR.Body.String())
		}
		var created map[string]any
		if err := json.Unmarshal(createRR.Body.Bytes(), &created); err != nil {
			t.Fatalf("unmarshal created user: %v", err)
		}
		userID, _ := created["id"].(string)

		updateRR := doJSONRequest(t, handler, http.MethodPut, "/admin/api/users/"+userID, map[string]any{
			"hiddenLibraries": map[string]any{serverID: []string{"lib-1"}},
		}, token)
		if updateRR.Code != http.StatusOK {
			t.Fatalf("update user status = %d, body=%s", updateRR.Code, updateRR.Body.String())
		}

		listRR := doJSONRequest(t, handler, http.MethodGet, "/admin/api/users", nil, token)
		var users []map[string]any
		if err := json.Unmarshal(listRR.Body.Bytes(), &users); err != nil {
			t.Fatalf("unmarshal users: %v", err)
		}
		found := false
		for _, user := range users {
			if user["id"] != userID {
				continue
			}
			found = true
			hidden, _ := user["hiddenLibraries"].(map[string]any)
			if got := hiddenIDCount(hidden, serverID); got != 1 {
				t.Fatalf("users list must echo hiddenLibraries, got %#v", hidden)
			}
		}
		if !found {
			t.Fatalf("created user missing from users list")
		}

		// Narrowing AllowedServers to an explicit list prunes the hidden
		// config of servers outside it; the fake srv-x entry must disappear.
		if err := app.HiddenLibraries.SetServerHidden(userID, "srv-x", []string{"lib-9"}); err != nil {
			t.Fatalf("SetServerHidden srv-x: %v", err)
		}
		narrowRR := doJSONRequest(t, handler, http.MethodPut, "/admin/api/users/"+userID, map[string]any{
			"allowedServers": []string{serverID},
		}, token)
		if narrowRR.Code != http.StatusOK {
			t.Fatalf("narrow status = %d, body=%s", narrowRR.Code, narrowRR.Body.String())
		}
		if hidden := app.HiddenLibraries.UserHiddenJSON(userID); len(hidden["srv-x"]) != 0 {
			t.Fatalf("narrowing allowed servers must prune srv-x, got %#v", hidden)
		}

		// An empty AllowedServers means "all servers": pruning must not run
		// and the stored config must survive.
		if err := app.HiddenLibraries.SetServerHidden(userID, "srv-y", []string{"lib-8"}); err != nil {
			t.Fatalf("SetServerHidden srv-y: %v", err)
		}
		openRR := doJSONRequest(t, handler, http.MethodPut, "/admin/api/users/"+userID, map[string]any{
			"allowedServers": []string{},
		}, token)
		if openRR.Code != http.StatusOK {
			t.Fatalf("open status = %d, body=%s", openRR.Code, openRR.Body.String())
		}
		if hidden := app.HiddenLibraries.UserHiddenJSON(userID); len(hidden["srv-y"]) != 1 {
			t.Fatalf("empty allowedServers must keep the hidden config, got %#v", hidden)
		}
	})
}

func TestUpstreamDeleteCleansHidden(t *testing.T) {
	withHomeLibraryApp(t, func(app *App, handler http.Handler, viewsHits *atomic.Int32) {
		token := loginToken(t, handler, "secret")
		serverID := app.Upstream.Clients()[0].ID

		if err := app.HiddenLibraries.SetServerHidden(adminVisibilityUserID, serverID, []string{"lib-1"}); err != nil {
			t.Fatalf("SetServerHidden: %v", err)
		}
		deleteRR := doJSONRequest(t, handler, http.MethodDelete, "/admin/api/upstream/"+serverID, nil, token)
		if deleteRR.Code != http.StatusOK {
			t.Fatalf("delete upstream status = %d, body=%s", deleteRR.Code, deleteRR.Body.String())
		}
		if hidden := app.HiddenLibraries.UserHiddenJSON(adminVisibilityUserID); len(hidden) != 0 {
			t.Fatalf("deleting an upstream must clean its hidden records, got %#v", hidden)
		}
		if _, cached := app.libraryCache.getStale(serverID); cached {
			t.Fatalf("deleting an upstream must drop its cached library list")
		}
	})
}

func TestUserDeleteCleansHidden(t *testing.T) {
	withHomeLibraryApp(t, func(app *App, handler http.Handler, viewsHits *atomic.Int32) {
		token := loginToken(t, handler, "secret")

		createRR := doJSONRequest(t, handler, http.MethodPost, "/admin/api/users", map[string]any{
			"username": "eve", "password": "password123",
		}, token)
		if createRR.Code != http.StatusCreated {
			t.Fatalf("create user status = %d, body=%s", createRR.Code, createRR.Body.String())
		}
		var created map[string]any
		if err := json.Unmarshal(createRR.Body.Bytes(), &created); err != nil {
			t.Fatalf("unmarshal created user: %v", err)
		}
		userID, _ := created["id"].(string)

		if err := app.HiddenLibraries.SetServerHidden(userID, "srv-a", []string{"lib-1"}); err != nil {
			t.Fatalf("SetServerHidden: %v", err)
		}
		deleteRR := doJSONRequest(t, handler, http.MethodDelete, "/admin/api/users/"+userID, nil, token)
		if deleteRR.Code != http.StatusOK {
			t.Fatalf("delete user status = %d, body=%s", deleteRR.Code, deleteRR.Body.String())
		}
		if hidden := app.HiddenLibraries.UserHiddenJSON(userID); len(hidden) != 0 {
			t.Fatalf("deleting a user must clean their hidden records, got %#v", hidden)
		}
	})
}

func TestHomeLibrariesRequiresAdmin(t *testing.T) {
	withHomeLibraryApp(t, func(app *App, handler http.Handler, viewsHits *atomic.Int32) {
		token := loginToken(t, handler, "secret")
		serverID := app.Upstream.Clients()[0].ID

		createRR := doJSONRequest(t, handler, http.MethodPost, "/admin/api/users", map[string]any{
			"username": "mallory", "password": "password123",
		}, token)
		if createRR.Code != http.StatusCreated {
			t.Fatalf("create user status = %d, body=%s", createRR.Code, createRR.Body.String())
		}

		userToken := loginTokenAs(t, handler, "mallory", "password123")
		for _, target := range []struct {
			method, path string
		}{
			{http.MethodGet, "/admin/api/upstream/" + serverID + "/libraries"},
			{http.MethodGet, "/admin/api/home-libraries"},
			{http.MethodPut, "/admin/api/home-libraries"},
		} {
			rr := doJSONRequest(t, handler, target.method, target.path, map[string]any{"hidden": map[string]any{}}, userToken)
			if rr.Code != http.StatusForbidden {
				t.Fatalf("%s %s as regular user = %d, want 403", target.method, target.path, rr.Code)
			}
		}
	})
}
