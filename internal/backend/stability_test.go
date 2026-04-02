package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Token persistence tests (tokens never expire)
// ---------------------------------------------------------------------------

func TestTokenNeverExpiresRegardlessOfAge(t *testing.T) {
	config := "server:\n  port: 8096\n  name: \"Test\"\n  id: \"svr\"\nadmin:\n  username: \"admin\"\n  password: \"secret\"\nplayback:\n  mode: \"proxy\"\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\nproxies: []\nupstream: []\n"

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")

		// Simulate the token being created a very long time ago (e.g. 30 days)
		app.Auth.mu.Lock()
		if info, ok := app.Auth.tokens[token]; ok {
			info.CreatedAt = time.Now().Add(-30 * 24 * time.Hour).UnixMilli()
			app.Auth.tokens[token] = info
		}
		app.Auth.mu.Unlock()

		// Token should still be valid — tokens never expire
		rr := doJSONRequest(t, handler, http.MethodGet, "/System/Info", nil, token)
		if rr.Code == http.StatusUnauthorized {
			t.Fatalf("token should never expire, but got 401 after 30 days")
		}

		// Validate directly
		result := app.Auth.ValidateToken(token)
		if result == nil {
			t.Fatal("ValidateToken returned nil for a token that should never expire")
		}
	})
}

func TestTokenRevokedOnPasswordChange(t *testing.T) {
	config := "server:\n  port: 8096\n  name: \"Test\"\n  id: \"svr\"\nadmin:\n  username: \"admin\"\n  password: \"secret\"\nplayback:\n  mode: \"proxy\"\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\nproxies: []\nupstream: []\n"

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")

		// Verify token works
		rr := doJSONRequest(t, handler, http.MethodGet, "/System/Info", nil, token)
		if rr.Code == http.StatusUnauthorized {
			t.Fatal("token should be valid before password change")
		}

		// Change password via admin API
		rr = doJSONRequest(t, handler, http.MethodPut, "/admin/api/settings",
			map[string]any{"adminPassword": "newpass123", "currentPassword": "secret"}, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("password change failed: status=%d, body=%s", rr.Code, rr.Body.String())
		}

		// Old token should now be revoked
		rr = doJSONRequest(t, handler, http.MethodGet, "/System/Info", nil, token)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("old token should be revoked after password change, got %d", rr.Code)
		}

		// Login with new password should work
		newToken := loginToken(t, handler, "newpass123")
		if newToken == "" {
			t.Fatal("login with new password failed")
		}
	})
}

func TestRevokeAllTokens(t *testing.T) {
	config := "server:\n  port: 8096\n  name: \"Test\"\n  id: \"svr\"\nadmin:\n  username: \"admin\"\n  password: \"secret\"\nplayback:\n  mode: \"proxy\"\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\nproxies: []\nupstream: []\n"

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		// Create multiple tokens
		token1 := loginToken(t, handler, "secret")
		token2 := loginToken(t, handler, "secret")

		// Both should be valid
		if app.Auth.ValidateToken(token1) == nil {
			t.Fatal("token1 should be valid")
		}
		if app.Auth.ValidateToken(token2) == nil {
			t.Fatal("token2 should be valid")
		}

		// Revoke all
		app.Auth.RevokeAllTokens()

		// Both should now be invalid
		if app.Auth.ValidateToken(token1) != nil {
			t.Fatal("token1 should be revoked after RevokeAllTokens")
		}
		if app.Auth.ValidateToken(token2) != nil {
			t.Fatal("token2 should be revoked after RevokeAllTokens")
		}
	})
}

// ---------------------------------------------------------------------------
// Upstream auth recovery tests
// ---------------------------------------------------------------------------

func TestUpstreamAuthRecovery(t *testing.T) {
	var authFails atomic.Int32
	authFails.Store(1) // first non-login request returns 401
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok", "User": map[string]any{"Id": "uid"}})
		case r.URL.Path == "/Users/Me":
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "uid"})
		default:
			if authFails.Load() > 0 {
				authFails.Add(-1)
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": []any{}, "TotalRecordCount": 0})
		}
	}))
	defer upstream.Close()

	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test\"\n  id: \"svr\"\nadmin:\n  username: \"admin\"\n  password: \"secret\"\nplayback:\n  mode: \"proxy\"\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 0\nproxies: []\nupstream:\n  - name: \"A\"\n    url: %q\n    username: \"u\"\n    password: \"p\"\n", upstream.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")

		// First request will get 401 from upstream, which triggers recovery
		rr := doJSONRequest(t, handler, http.MethodGet, "/Users/"+app.Auth.ProxyUserID()+"/Views", nil, token)
		// This first request may fail (502) because upstream returned 401
		_ = rr

		// Wait for async recovery to complete
		time.Sleep(500 * time.Millisecond)

		// After recovery, upstream should be back online
		client := app.Upstream.GetClient(0)
		if client == nil {
			t.Fatal("upstream client not found")
		}
		if !client.IsOnline() {
			t.Fatalf("upstream client should be online after recovery, lastError=%q", client.LastError)
		}
	})
}

func TestUpstreamRecoveryDebounce(t *testing.T) {
	var loginCount atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			loginCount.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok", "User": map[string]any{"Id": "uid"}})
		case r.URL.Path == "/Users/Me":
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "uid"})
		default:
			// Always return 401 to trigger maximum recovery attempts
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
		}
	}))
	defer upstream.Close()

	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test\"\n  id: \"svr\"\nadmin:\n  username: \"admin\"\n  password: \"secret\"\nplayback:\n  mode: \"proxy\"\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 0\nproxies: []\nupstream:\n  - name: \"A\"\n    url: %q\n    username: \"u\"\n    password: \"p\"\n", upstream.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")

		// Reset login count after initial setup login
		loginCount.Store(0)

		// Fire 5 requests rapidly — all will get 401 from upstream
		for i := 0; i < 5; i++ {
			doJSONRequest(t, handler, http.MethodGet, "/Users/"+app.Auth.ProxyUserID()+"/Views", nil, token)
		}

		// Wait for any async goroutines
		time.Sleep(500 * time.Millisecond)

		// Should have triggered at most 1 recovery login (debounce = 30s)
		count := loginCount.Load()
		if count > 2 { // allow 1-2 (race between goroutines before debounce kicks in)
			t.Fatalf("recovery triggered %d logins, expected <=2 (debounce should prevent flood)", count)
		}
	})
}

func TestLoginPathDoesNotTriggerRecovery(t *testing.T) {
	var loginCount atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			loginCount.Add(1)
			// Return 401 to simulate login failure
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test\"\n  id: \"svr\"\nadmin:\n  username: \"admin\"\n  password: \"secret\"\nplayback:\n  mode: \"proxy\"\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 0\nproxies: []\nupstream:\n  - name: \"A\"\n    url: %q\n    username: \"u\"\n    password: \"p\"\n", upstream.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		// The initial LoginAll during NewApp will call AuthenticateByName
		// which returns 401 — but this should NOT trigger recovery (it's a login path)
		time.Sleep(200 * time.Millisecond)

		// loginCount should be exactly 1 (the initial login attempt, no recovery loop)
		count := loginCount.Load()
		if count > 1 {
			t.Fatalf("login failure triggered %d login attempts, expected 1 (no recovery loop)", count)
		}
	})
}

func TestIsUpstreamLoginPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/Users/AuthenticateByName", true},
		{"/Users/Me", true},
		{"/Users/abc123/Views", false},
		{"/Items", false},
		{"/System/Info", false},
		{"/Videos/abc/stream.mp4", false},
	}
	for _, tt := range tests {
		if got := isUpstreamLoginPath(tt.path); got != tt.want {
			t.Errorf("isUpstreamLoginPath(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}
