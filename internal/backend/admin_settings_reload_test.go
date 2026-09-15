package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The timeouts that live on an upstream client are read while it is built, so a
// settings save has to rebuild the pool. Before that, the panel reported success
// and the running clients kept the old values until a restart.
//
// healthInterval rides on the same rebuild rather than on the client field the
// loop reads: Reload restarts the health-check loop from the config it is handed,
// so the interval it observes is the one just saved.
func TestAdminSettingsRebuildTheLiveClients(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName" {
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-1", "User": map[string]any{"Id": "uid-1"}})
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	withTempAppConfig(t, singleUpstreamConfig(upstream.URL), func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		before := app.Upstream.GetClient(0).snapshot()
		if !before.Online {
			t.Fatalf("the upstream never came online, so the rebuild would prove nothing")
		}

		rr := doJSONRequest(t, handler, http.MethodPut, "/admin/api/settings", map[string]any{
			"timeouts": map[string]any{
				"api":            12345,
				"login":          23456,
				"healthCheck":    34567,
				"healthInterval": 45678,
			},
		}, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("settings status = %d, body=%s", rr.Code, rr.Body.String())
		}

		live := app.Upstream.GetClient(0).snapshot()
		for _, tc := range []struct {
			name string
			got  int
			want int
		}{
			{"api", live.timeouts.API, 12345},
			{"login", live.timeouts.Login, 23456},
			{"healthCheck", live.timeouts.HealthCheck, 34567},
			{"healthInterval", live.timeouts.HealthInterval, 45678},
		} {
			if tc.got != tc.want {
				t.Errorf("live client %s timeout = %d, want %d", tc.name, tc.got, tc.want)
			}
		}

		// The settings page is not where credentials change: the rebuild must
		// carry the session over instead of signing the upstream out.
		if !live.Online {
			t.Fatalf("the settings save took the upstream offline (lastError=%q)", live.LastError)
		}
		if live.AccessToken != before.AccessToken || live.UserID != before.UserID {
			t.Fatalf("the rebuild replaced the credentials: token %q→%q, user %q→%q",
				before.AccessToken, live.AccessToken, before.UserID, live.UserID)
		}
	})
}

// A settings save that changes nothing about the timeouts still leaves the pool
// usable: rebuilding is not allowed to drop the upstream on its own.
func TestAdminSettingsSurviveAnUnrelatedChange(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName" {
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-1", "User": map[string]any{"Id": "uid-1"}})
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	withTempAppConfig(t, singleUpstreamConfig(upstream.URL), func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		rr := doJSONRequest(t, handler, http.MethodPut, "/admin/api/settings",
			map[string]any{"serverName": "Renamed"}, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("settings status = %d, body=%s", rr.Code, rr.Body.String())
		}
		if got := app.ConfigStore.Snapshot().Server.Name; got != "Renamed" {
			t.Fatalf("server name = %q, want Renamed", got)
		}
		if !app.Upstream.GetClient(0).IsOnline() {
			t.Fatalf("a rename took the upstream offline")
		}
	})
}
