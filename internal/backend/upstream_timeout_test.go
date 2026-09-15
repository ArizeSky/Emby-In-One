package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// slowUpstreamStub answers login (and optionally /Users/Me) after delay, so a
// timeout shorter than that must fail while a longer one succeeds.
func slowUpstreamStub(t *testing.T, delay time.Duration, withUsersMe bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			time.Sleep(delay)
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-a", "User": map[string]any{"Id": "user-a"}})
		case withUsersMe && r.Method == http.MethodGet && r.URL.Path == "/Users/Me":
			time.Sleep(delay)
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "user-a", "Name": "User A"})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// replaceInConfig substitutes a value in a config fixture and fails loudly when the
// pattern is gone, so a stale fixture cannot silently turn a test into a no-op.
func replaceInConfig(t *testing.T, config, old, replacement string) string {
	t.Helper()
	if !strings.Contains(config, old) {
		t.Fatalf("config fixture is missing %q", old)
	}
	return strings.Replace(config, old, replacement, 1)
}

// TestHealthCheckHonoursHealthCheckTimeout pins the wiring: a health-check probe is
// bounded by timeouts.healthCheck, not by timeouts.api.
func TestHealthCheckHonoursHealthCheckTimeout(t *testing.T) {
	const probeDelay = 400 * time.Millisecond
	upstream := slowUpstreamStub(t, probeDelay, false)

	cases := []struct {
		name        string
		healthCheck string
		wantOnline  bool
	}{
		{"timeout shorter than the upstream needs", "50", false},
		{"timeout longer than the upstream needs", "5000", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config := replaceInConfig(t, singleUpstreamConfig(upstream.URL),
				"  healthCheck: 10000", "  healthCheck: "+tc.healthCheck)

			withTempAppConfig(t, config, func(app *App, handler http.Handler) {
				client := app.Upstream.GetClient(0)
				if client == nil {
					t.Fatalf("upstream client missing")
				}
				// The startup login uses timeouts.api and succeeds, so force the
				// client offline to make the health-check path run.
				client.setOffline("test offline")

				start := time.Now()
				app.Upstream.runHealthCheckCycle(context.Background())
				elapsed := time.Since(start)

				if got := client.IsOnline(); got != tc.wantOnline {
					t.Fatalf("online = %v, want %v (probe took %s)", got, tc.wantOnline, elapsed)
				}
				if !tc.wantOnline && elapsed >= probeDelay {
					t.Fatalf("probe waited %s, so the healthCheck timeout was not applied", elapsed)
				}
			})
		})
	}
}

// TestLoginHonoursLoginTimeout covers the password login path.
func TestLoginHonoursLoginTimeout(t *testing.T) {
	const loginDelay = 400 * time.Millisecond
	upstream := slowUpstreamStub(t, loginDelay, false)

	cases := []struct {
		name       string
		login      string
		wantOnline bool
	}{
		{"timeout shorter than the upstream needs", "50", false},
		{"timeout longer than the upstream needs", "5000", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config := replaceInConfig(t, singleUpstreamConfig(upstream.URL),
				"  login: 10000", "  login: "+tc.login)

			withTempAppConfig(t, config, func(app *App, handler http.Handler) {
				client := app.Upstream.GetClient(0)
				if client == nil {
					t.Fatalf("upstream client missing")
				}
				client.setOffline("test offline")

				start := time.Now()
				client.Login(context.Background(), nil, app.Identity)
				elapsed := time.Since(start)

				if got := client.IsOnline(); got != tc.wantOnline {
					t.Fatalf("online = %v, want %v (login took %s)", got, tc.wantOnline, elapsed)
				}
				if !tc.wantOnline && elapsed >= loginDelay {
					t.Fatalf("login waited %s, so the login timeout was not applied", elapsed)
				}
			})
		})
	}
}

// TestAPIKeyValidationHonoursLoginTimeout covers the other login path: an API-key
// upstream is validated through GET /Users/Me, which must obey timeouts.login too.
func TestAPIKeyValidationHonoursLoginTimeout(t *testing.T) {
	const validationDelay = 400 * time.Millisecond
	upstream := slowUpstreamStub(t, validationDelay, true)

	cases := []struct {
		name       string
		login      string
		wantOnline bool
	}{
		{"timeout shorter than the upstream needs", "50", false},
		{"timeout longer than the upstream needs", "5000", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config := replaceInConfig(t, singleUpstreamConfig(upstream.URL),
				"    username: \"u1\"\n    password: \"p1\"", "    apiKey: \"k1\"")
			config = replaceInConfig(t, config, "  login: 10000", "  login: "+tc.login)

			withTempAppConfig(t, config, func(app *App, handler http.Handler) {
				client := app.Upstream.GetClient(0)
				if client == nil {
					t.Fatalf("upstream client missing")
				}
				// Startup validates the key, so the login timeout decides whether the
				// upstream comes up at all.
				if got := client.IsOnline(); got != tc.wantOnline {
					t.Fatalf("online = %v, want %v", got, tc.wantOnline)
				}
			})
		})
	}
}

// TestLogTimeoutNoticeForTighterTimeouts covers the upgrade hint: installs whose
// config still carries the old defaults get one line explaining the new behaviour.
func TestLogTimeoutNoticeForTighterTimeouts(t *testing.T) {
	logger, _ := newTestLogger(t, 1<<20, 1)
	const needle = "Timeouts are now enforced"

	logTimeoutNotice(logger, TimeoutsConfig{API: 30000, Login: 30000, HealthCheck: 30000})
	if got := countEntriesContaining(logger, needle); got != 0 {
		t.Fatalf("matching timeouts must not warn, got %d entries", got)
	}

	logTimeoutNotice(logger, TimeoutsConfig{API: 30000, Login: 10000, HealthCheck: 10000})
	if got := countEntriesContaining(logger, needle); got != 1 {
		t.Fatalf("tighter timeouts must warn exactly once, got %d entries", got)
	}
}

func countEntriesContaining(logger *Logger, needle string) int {
	count := 0
	for _, entry := range logger.Entries(0) {
		if strings.Contains(entry.Message, needle) {
			count++
		}
	}
	return count
}
