package backend

import (
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

func TestTokenNeverExpiresRegardlessOfAge(t *testing.T) {
	config := "server:\n  port: 8096\n  name: \"Test\"\n  id: \"svr\"\nadmin:\n  username: \"admin\"\n  password: \"secret\"\nplayback:\n  mode: \"proxy\"\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\nproxies: []\nupstream: []\n"

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")

		// Set creation time to 365 days ago
		app.Auth.mu.Lock()
		if info, ok := app.Auth.tokens[token]; ok {
			info.CreatedAt = time.Now().Add(-365 * 24 * time.Hour).UnixMilli()
			app.Auth.tokens[token] = info
		}
		app.Auth.mu.Unlock()

		// Token should still validate (never expires)
		info := app.Auth.ValidateToken(token)
		if info == nil {
			t.Fatal("expected token to still be valid after 365 days")
		}
	})
}

func TestRevokeAllTokens(t *testing.T) {
	config := "server:\n  port: 8096\n  name: \"Test\"\n  id: \"svr\"\nadmin:\n  username: \"admin\"\n  password: \"secret\"\nplayback:\n  mode: \"proxy\"\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\nproxies: []\nupstream: []\n"

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token1 := loginToken(t, handler, "secret")
		token2 := loginToken(t, handler, "secret")

		// Both tokens should be valid
		if app.Auth.ValidateToken(token1) == nil {
			t.Fatal("token1 should be valid before revoke")
		}
		if app.Auth.ValidateToken(token2) == nil {
			t.Fatal("token2 should be valid before revoke")
		}

		// Revoke all
		app.Auth.RevokeAllTokens()

		// Both tokens should now be invalid
		if app.Auth.ValidateToken(token1) != nil {
			t.Fatal("token1 should be invalid after RevokeAllTokens")
		}
		if app.Auth.ValidateToken(token2) != nil {
			t.Fatal("token2 should be invalid after RevokeAllTokens")
		}
	})
}

func TestTokenRevokedOnPasswordChange(t *testing.T) {
	config := "server:\n  port: 8096\n  name: \"Test\"\n  id: \"svr\"\nadmin:\n  username: \"admin\"\n  password: \"secret\"\nplayback:\n  mode: \"proxy\"\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\nproxies: []\nupstream: []\n"

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		if app.Auth.ValidateToken(token) == nil {
			t.Fatal("token should be valid before password change")
		}

		// Change password via admin settings endpoint
		body := map[string]any{
			"currentPassword": "secret",
			"adminPassword":   "newPassword123",
		}
		rr := doJSONRequest(t, handler, http.MethodPut, "/admin/api/settings", body, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("password change failed: status=%d, body=%s", rr.Code, rr.Body.String())
		}

		// Old token should now be invalid
		if app.Auth.ValidateToken(token) != nil {
			t.Fatal("old token should be revoked after password change")
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
		{"/Users/123/Items", false},
		{"/System/Info/Public", false},
		{"/Items/abc", false},
		{"", false},
	}
	for _, tt := range tests {
		got := isUpstreamLoginPath(tt.path)
		if got != tt.want {
			t.Errorf("isUpstreamLoginPath(%q) = %v, want %v", tt.path, got, tt.want)
		}
	}
}

func TestUpstreamRecoveryDebounce(t *testing.T) {
	var callCount atomic.Int32
	client := &UpstreamClient{
		Name:   "test-server",
		Config: UpstreamConfig{Name: "test"},
	}
	pool := &UpstreamPool{}

	// Simulate the debounce check directly
	client.onAuthError = func(c *UpstreamClient) {
		c.recoveryMu.Lock()
		if time.Since(c.lastRecovery) < recoveryDebounce {
			c.recoveryMu.Unlock()
			return
		}
		c.lastRecovery = time.Now()
		c.recoveryMu.Unlock()
		callCount.Add(1)
	}
	_ = pool

	// First call should go through
	client.onAuthError(client)
	if callCount.Load() != 1 {
		t.Fatalf("first call should increment counter, got %d", callCount.Load())
	}

	// Immediate second call should be debounced
	client.onAuthError(client)
	if callCount.Load() != 1 {
		t.Fatalf("second call should be debounced, got %d", callCount.Load())
	}
}

func TestHasCapturedHeaders(t *testing.T) {
	svc := NewClientIdentityService()
	defer svc.Clear()

	serverKey := "http://test|Server1|passthrough"

	// No captured headers initially
	if svc.HasCapturedHeaders(serverKey) {
		t.Fatal("should have no captured headers initially")
	}

	// After saving last-success, should return true
	svc.SaveLastSuccess(serverKey, http.Header{
		"X-Emby-Client":    {"Infuse"},
		"X-Emby-Device-Id": {"device-1"},
	})
	if !svc.HasCapturedHeaders(serverKey) {
		t.Fatal("should have captured headers after SaveLastSuccess")
	}
}

func TestHasCapturedHeadersViaTokenEntry(t *testing.T) {
	svc := NewClientIdentityService()
	defer svc.Clear()

	serverKey := "http://other|Server2|passthrough"

	// No captured headers for this server
	if svc.HasCapturedHeaders(serverKey) {
		t.Fatal("should have no captured headers initially")
	}

	// SetCaptured for a token alone is NOT enough — need latestCaptured or lastSuccess
	svc.SetCaptured("some-token", http.Header{
		"X-Emby-Client":    {"Infuse"},
		"X-Emby-Device-Id": {"device-2"},
	})
	if svc.HasCapturedHeaders(serverKey) {
		t.Fatal("token-only entries should not satisfy HasCapturedHeaders")
	}

	// SaveLatestCapturedHeaders should make it return true
	svc.SaveLatestCapturedHeaders(http.Header{
		"X-Emby-Client":    {"Infuse"},
		"X-Emby-Device-Id": {"device-2"},
	})
	if !svc.HasCapturedHeaders(serverKey) {
		t.Fatal("should detect captured headers via latestCaptured")
	}
}
