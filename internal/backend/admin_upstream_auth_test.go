package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// authSwitchUpstream accepts both credential kinds and records which one the
// proxy used, so a test can tell a real switch from a config-only change.
type authSwitchUpstream struct {
	*httptest.Server
	mu   sync.Mutex
	seen []string
}

func newAuthSwitchUpstream(t *testing.T) *authSwitchUpstream {
	t.Helper()
	fixture := &authSwitchUpstream{}
	fixture.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fixture.mu.Lock()
		fixture.seen = append(fixture.seen, r.Method+" "+r.URL.Path)
		fixture.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "password-token", "User": map[string]any{"Id": "password-user"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Users/Me":
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "apikey-user"})
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(fixture.Close)
	return fixture
}

func (f *authSwitchUpstream) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

func (f *authSwitchUpstream) saw(path string) bool {
	for _, entry := range f.requests() {
		if strings.HasSuffix(entry, path) {
			return true
		}
	}
	return false
}

func apiKeyUpstreamConfig(url, key string) string {
	return fmt.Sprintf(`server:
  port: 8096
  name: "Test"
  id: "svr"
admin:
  username: "admin"
  password: "secret"
playback:
  mode: "proxy"
timeouts:
  api: 30000
  global: 15000
  login: 10000
  healthCheck: 10000
  healthInterval: 60000
proxies: []
upstream:
  - name: "A"
    url: %q
    apiKey: %q
`, url, key)
}

// The panel shows one credential kind at a time, so switching between them has
// to be expressible. It used to be impossible: the stored credential was never
// cleared, and a draft carrying both kinds is rejected.
func TestAdminUpstreamAuthTypeSwitch(t *testing.T) {
	t.Run("password to apiKey", func(t *testing.T) {
		upstream := newAuthSwitchUpstream(t)
		withTempAppConfig(t, singleUpstreamConfig(upstream.URL), func(app *App, handler http.Handler) {
			token := loginToken(t, handler, "secret")

			// The panel keeps the username field populated while switching, so the
			// request carries the old credential as well as the declared kind.
			rr := doJSONRequest(t, handler, http.MethodPut, "/admin/api/upstream/0", map[string]any{
				"name": "A", "url": upstream.URL,
				"authType": "apiKey", "apiKey": "NEW-KEY", "username": "u1",
			}, token)
			if rr.Code != http.StatusOK {
				t.Fatalf("switch status = %d, body=%s", rr.Code, rr.Body.String())
			}

			updated := app.ConfigStore.Snapshot().Upstream[0]
			if updated.APIKey != "NEW-KEY" {
				t.Errorf("apiKey = %q, want NEW-KEY", updated.APIKey)
			}
			if updated.Username != "" || updated.Password != "" {
				t.Errorf("username/password = %q/%q, want both cleared", updated.Username, updated.Password)
			}
			if !upstream.saw("/Users/Me") {
				t.Errorf("the switch never validated the API key, upstream saw %v", upstream.requests())
			}
		})
	})

	t.Run("apiKey to password", func(t *testing.T) {
		upstream := newAuthSwitchUpstream(t)
		withTempAppConfig(t, apiKeyUpstreamConfig(upstream.URL, "OLD-KEY"), func(app *App, handler http.Handler) {
			token := loginToken(t, handler, "secret")

			rr := doJSONRequest(t, handler, http.MethodPut, "/admin/api/upstream/0", map[string]any{
				"name": "A", "url": upstream.URL,
				"authType": "password", "username": "u2", "password": "p2",
			}, token)
			if rr.Code != http.StatusOK {
				t.Fatalf("switch status = %d, body=%s", rr.Code, rr.Body.String())
			}

			updated := app.ConfigStore.Snapshot().Upstream[0]
			if updated.APIKey != "" {
				t.Errorf("apiKey = %q, want it cleared", updated.APIKey)
			}
			if updated.Username != "u2" || updated.Password != "p2" {
				t.Errorf("username/password = %q/%q, want u2/p2", updated.Username, updated.Password)
			}
			if !upstream.saw("/Users/AuthenticateByName") {
				t.Errorf("the switch never logged in with the password, upstream saw %v", upstream.requests())
			}
		})
	})
}
