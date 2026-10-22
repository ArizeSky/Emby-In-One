package backend

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func findUpstreamEntry(t *testing.T, handler http.Handler, token, id string) map[string]any {
	t.Helper()
	rr := doJSONRequest(t, handler, http.MethodGet, "/admin/api/upstream", nil, token)
	if rr.Code != http.StatusOK {
		t.Fatalf("list upstream: status=%d body=%s", rr.Code, rr.Body.String())
	}
	var list []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal upstream list: %v", err)
	}
	for _, entry := range list {
		if entry["id"] == id {
			return entry
		}
	}
	t.Fatalf("upstream %q not found in list", id)
	return nil
}

func TestAdminAggregationAuthorizationAndInput(t *testing.T) {
	a := newAggUpstreamStub(t)
	b := newAggUpstreamStub(t)
	withTempAppConfig(t, dualUpstreamConfig(a.server.URL, b.server.URL), func(app *App, handler http.Handler) {
		adminToken := loginTokenAs(t, handler, "admin", "secret")
		idA := app.Upstream.Clients()[0].ID
		path := "/admin/api/upstream/" + idA + "/aggregation"

		if rr := doJSONRequest(t, handler, http.MethodPatch, path, map[string]any{"aggregationEnabled": false}, ""); rr.Code != http.StatusUnauthorized {
			t.Fatalf("no token: status=%d, want 401", rr.Code)
		}
		if rr := doAuthJSON(t, handler, http.MethodPost, "/admin/api/users", map[string]any{"username": "bob", "password": "bob12345"}, adminToken); rr.Code != http.StatusCreated {
			t.Fatalf("create user: status=%d body=%s", rr.Code, rr.Body.String())
		}
		bobToken := loginTokenAs(t, handler, "bob", "bob12345")
		if rr := doJSONRequest(t, handler, http.MethodPatch, path, map[string]any{"aggregationEnabled": false}, bobToken); rr.Code != http.StatusForbidden {
			t.Fatalf("regular user: status=%d, want 403", rr.Code)
		}
		for _, body := range []any{map[string]any{}, "null", map[string]any{"aggregationEnabled": "yes"}} {
			if rr := doJSONRequest(t, handler, http.MethodPatch, path, body, adminToken); rr.Code != http.StatusBadRequest {
				t.Errorf("bad body %v: status=%d, want 400", body, rr.Code)
			}
		}
		if rr := doJSONRequest(t, handler, http.MethodPatch, "/admin/api/upstream/missing/aggregation", map[string]any{"aggregationEnabled": false}, adminToken); rr.Code != http.StatusNotFound {
			t.Fatalf("unknown id: status=%d, want 404", rr.Code)
		}
	})
}

func TestAdminAggregationDoesNotReloadOrLogin(t *testing.T) {
	a := newAggUpstreamStub(t)
	b := newAggUpstreamStub(t)
	withTempAppConfig(t, dualUpstreamConfig(a.server.URL, b.server.URL), func(app *App, handler http.Handler) {
		adminToken := loginTokenAs(t, handler, "admin", "secret")
		idA := app.Upstream.Clients()[0].ID
		clientA := app.Upstream.ClientByID(idA)
		tokenBefore, userBefore, onlineBefore := clientA.AccessToken, clientA.UserID, clientA.IsOnline()
		a.reset()

		if rr := doJSONRequest(t, handler, http.MethodPatch, "/admin/api/upstream/"+idA+"/aggregation", map[string]any{"aggregationEnabled": false}, adminToken); rr.Code != http.StatusOK {
			t.Fatalf("toggle: status=%d body=%s", rr.Code, rr.Body.String())
		}
		clientA2 := app.Upstream.ClientByID(idA)
		if clientA2 != clientA {
			t.Fatalf("toggling rebuilt the upstream pool (client pointer changed)")
		}
		if clientA2.AccessToken != tokenBefore || clientA2.UserID != userBefore || clientA2.IsOnline() != onlineBefore {
			t.Fatalf("toggling changed the client session state")
		}
		if a.loginHits.Load() != 0 {
			t.Fatalf("toggling triggered %d upstream login(s), want 0", a.loginHits.Load())
		}
		if entry := findUpstreamEntry(t, handler, adminToken, idA); entry["aggregationEnabled"] != false {
			t.Fatalf("list entry aggregationEnabled = %v, want false", entry["aggregationEnabled"])
		}
		// Status endpoint reads the same authoritative config.
		rr := doJSONRequest(t, handler, http.MethodGet, "/admin/api/status", nil, adminToken)
		var status struct {
			Upstream []map[string]any `json:"upstream"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &status); err != nil {
			t.Fatalf("unmarshal status: %v", err)
		}
		for _, entry := range status.Upstream {
			if entry["id"] == idA {
				if entry["aggregationEnabled"] != false {
					t.Fatalf("status entry aggregationEnabled = %v, want false", entry["aggregationEnabled"])
				}
				return
			}
		}
		t.Fatalf("server %q missing from status", idA)
	})
}

func TestAdminAggregationWriteFailureKeepsOldState(t *testing.T) {
	a := newAggUpstreamStub(t)
	b := newAggUpstreamStub(t)
	withTempAppConfig(t, dualUpstreamConfig(a.server.URL, b.server.URL), func(app *App, handler http.Handler) {
		adminToken := loginTokenAs(t, handler, "admin", "secret")
		idA := app.Upstream.Clients()[0].ID
		if !app.isServerAggregationEnabled(idA) {
			t.Fatalf("server should start enabled")
		}
		// Point the config path under a regular file so the atomic write fails.
		notDir := filepath.Join(t.TempDir(), "not-a-dir")
		if err := os.WriteFile(notDir, []byte("x"), 0o644); err != nil {
			t.Fatalf("write blocker file: %v", err)
		}
		app.ConfigStore.Mutate(func(c *Config) error { c.Path = filepath.Join(notDir, "config.yaml"); return nil })

		rr := doJSONRequest(t, handler, http.MethodPatch, "/admin/api/upstream/"+idA+"/aggregation", map[string]any{"aggregationEnabled": false}, adminToken)
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("write failure: status=%d, want 500; body=%s", rr.Code, rr.Body.String())
		}
		if !app.isServerAggregationEnabled(idA) {
			t.Fatalf("in-memory aggregation state changed after a failed write")
		}
	})
}

func TestAdminAggregationSurvivesEditAndReorder(t *testing.T) {
	a := newAggUpstreamStub(t)
	b := newAggUpstreamStub(t)
	withTempAppConfig(t, dualUpstreamConfig(a.server.URL, b.server.URL), func(app *App, handler http.Handler) {
		adminToken := loginTokenAs(t, handler, "admin", "secret")
		idA := app.Upstream.Clients()[0].ID
		idB := app.Upstream.Clients()[1].ID

		if rr := doJSONRequest(t, handler, http.MethodPatch, "/admin/api/upstream/"+idA+"/aggregation", map[string]any{"aggregationEnabled": false}, adminToken); rr.Code != http.StatusOK {
			t.Fatalf("disable A: status=%d body=%s", rr.Code, rr.Body.String())
		}
		// A stale edit form echoes aggregationEnabled=true; the normal edit must not
		// overwrite the switch that the dedicated endpoint owns.
		if rr := doJSONRequest(t, handler, http.MethodPut, "/admin/api/upstream/"+idA, map[string]any{"name": "A-renamed", "aggregationEnabled": true}, adminToken); rr.Code != http.StatusOK {
			t.Fatalf("edit A: status=%d body=%s", rr.Code, rr.Body.String())
		}
		if entry := findUpstreamEntry(t, handler, adminToken, idA); entry["aggregationEnabled"] != false {
			t.Fatalf("edit overwrote the aggregation switch: got %v", entry["aggregationEnabled"])
		}
		// Reorder must keep the switch bound to the persistent ID.
		if rr := doJSONRequest(t, handler, http.MethodPost, "/admin/api/upstream/reorder", map[string]any{"fromIndex": 0, "toIndex": 1}, adminToken); rr.Code != http.StatusOK {
			t.Fatalf("reorder: status=%d body=%s", rr.Code, rr.Body.String())
		}
		if entry := findUpstreamEntry(t, handler, adminToken, idA); entry["aggregationEnabled"] != false {
			t.Fatalf("after reorder, A aggregationEnabled = %v, want false", entry["aggregationEnabled"])
		}
		if entry := findUpstreamEntry(t, handler, adminToken, idB); entry["aggregationEnabled"] != true {
			t.Fatalf("after reorder, B aggregationEnabled = %v, want true", entry["aggregationEnabled"])
		}
	})
}

func TestAdminAggregationConcurrentMutations(t *testing.T) {
	a := newAggUpstreamStub(t)
	b := newAggUpstreamStub(t)
	withTempAppConfig(t, dualUpstreamConfig(a.server.URL, b.server.URL), func(app *App, handler http.Handler) {
		adminToken := loginTokenAs(t, handler, "admin", "secret")
		idA := app.Upstream.Clients()[0].ID
		idB := app.Upstream.Clients()[1].ID

		var wg sync.WaitGroup
		statuses := make([]int, 2)
		wg.Add(2)
		go func() {
			defer wg.Done()
			statuses[0] = doJSONRequest(t, handler, http.MethodPatch, "/admin/api/upstream/"+idA+"/aggregation", map[string]any{"aggregationEnabled": false}, adminToken).Code
		}()
		go func() {
			defer wg.Done()
			statuses[1] = doJSONRequest(t, handler, http.MethodPatch, "/admin/api/upstream/"+idB+"/aggregation", map[string]any{"aggregationEnabled": false}, adminToken).Code
		}()
		wg.Wait()
		for i, code := range statuses {
			if code != http.StatusOK {
				t.Fatalf("concurrent toggle %d: status=%d, want 200", i, code)
			}
		}
		if entry := findUpstreamEntry(t, handler, adminToken, idA); entry["aggregationEnabled"] != false {
			t.Fatalf("A toggle lost: aggregationEnabled = %v", entry["aggregationEnabled"])
		}
		if entry := findUpstreamEntry(t, handler, adminToken, idB); entry["aggregationEnabled"] != false {
			t.Fatalf("B toggle lost: aggregationEnabled = %v", entry["aggregationEnabled"])
		}
	})
}
