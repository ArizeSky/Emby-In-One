package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestAPIKeyUpstreamValidatesThroughUsersMe covers the API-key login path: the key
// is sent as X-Emby-Token to /Users/Me and the returned user id becomes the
// upstream user id used by every later per-user request.
func TestAPIKeyUpstreamValidatesThroughUsersMe(t *testing.T) {
	seenToken := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/Users/Me" {
			http.NotFound(w, r)
			return
		}
		select {
		case seenToken <- r.Header.Get("X-Emby-Token"):
		default:
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": "user-key", "Name": "Key User"})
	}))
	defer srv.Close()

	withTempApp(t, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		rr := doJSONRequest(t, handler, http.MethodPost, "/admin/api/upstream", map[string]any{
			"name":   "keyed",
			"url":    srv.URL,
			"apiKey": "api-key-value",
		}, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("create api-key upstream: status=%d body=%s", rr.Code, rr.Body.String())
		}

		// commitConfig re-logins asynchronously, so wait for the client to settle.
		waitForCondition(t, 3*time.Second, func() bool {
			clients := app.Upstream.Clients()
			return len(clients) == 1 && clients[0].IsOnline()
		}, "api-key upstream to come online")

		clients := app.Upstream.Clients()
		if clients[0].UserID != "user-key" {
			t.Fatalf("upstream UserID = %q, want user-key", clients[0].UserID)
		}

		select {
		case got := <-seenToken:
			if got != "api-key-value" {
				t.Fatalf("validation sent X-Emby-Token %q, want the configured API key", got)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("upstream /Users/Me was never called")
		}
	})
}

// TestAdminUpstreamValidationDoesNotReflectUpstreamBody keeps the admin API from
// turning an admin-supplied URL into a read primitive: only the status is
// reported, the upstream body stays in the log.
func TestAdminUpstreamValidationDoesNotReflectUpstreamBody(t *testing.T) {
	const secret = "SECRET-UPSTREAM-BODY"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(secret))
	}))
	defer srv.Close()

	withTempApp(t, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		rr := doJSONRequest(t, handler, http.MethodPost, "/admin/api/upstream", map[string]any{
			"name":   "rejecting",
			"url":    srv.URL,
			"apiKey": "k1",
		}, token)
		if rr.Code != http.StatusBadGateway {
			t.Fatalf("create with a rejecting upstream: status=%d body=%s", rr.Code, rr.Body.String())
		}
		body := rr.Body.String()
		if strings.Contains(body, secret) {
			t.Fatalf("admin response reflected the upstream body: %s", body)
		}
		if !strings.Contains(body, "401") {
			t.Fatalf("admin response should still report the upstream status, got: %s", body)
		}
	})
}
