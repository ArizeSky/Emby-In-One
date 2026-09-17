package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// TestRedirectPlaybackModeWarnsOnSave covers the warning the panel needs before it can let
// an administrator pick direct playback: that mode puts the shared upstream account's access
// token into a 302 the client follows, so every user who can start playback can read it and
// reach the upstream directly, outside this proxy's access rules.
func TestRedirectPlaybackModeWarnsOnSave(t *testing.T) {
	upstream := newAuthSwitchUpstream(t)
	config := fmt.Sprintf(`server:
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
    username: "u1"
    password: "p1"
    playbackMode: "redirect"
`, upstream.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")

		save := func(mode string) map[string]any {
			t.Helper()
			rr := doJSONRequest(t, handler, http.MethodPut, "/admin/api/upstream/0", map[string]any{
				"name": "A", "url": upstream.URL, "authType": "password",
				"username": "u1", "password": "p1", "playbackMode": mode,
			}, token)
			if rr.Code != http.StatusOK {
				t.Fatalf("save playbackMode=%s: status=%d body=%s", mode, rr.Code, rr.Body.String())
			}
			var payload map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			return payload
		}

		payload := save("redirect")
		warning, _ := payload["warning"].(string)
		if warning == "" {
			t.Fatalf("redirect mode was saved with no warning: %v", payload)
		}
		if warning != redirectCredentialWarning {
			t.Fatalf("warning = %q, want %q", warning, redirectCredentialWarning)
		}
		if payload := save("proxy"); payload["warning"] != nil {
			t.Fatalf("proxy mode should not warn: %v", payload["warning"])
		}
	})
}

// TestRedirectWarningSurvivesASuccessfulProbe pins the half the panel cannot check: direct
// playback leaks the credential precisely when the upstream is reachable, so a warning that
// only appeared for an offline upstream would be shown exactly when it does not apply.
func TestRedirectWarningSurvivesASuccessfulProbe(t *testing.T) {
	redirect := UpstreamConfig{Name: "A", PlaybackMode: "redirect"}
	proxy := UpstreamConfig{Name: "A", PlaybackMode: "proxy"}

	if got := withRedirectWarning(redirect, upstreamValidationResult{Online: true}); got.Warning != redirectCredentialWarning {
		t.Fatalf("online redirect probe: warning = %q, want %q", got.Warning, redirectCredentialWarning)
	}
	if got := withRedirectWarning(proxy, upstreamValidationResult{Online: true}); got.Warning != "" {
		t.Fatalf("proxy probe should not warn: %q", got.Warning)
	}
	// An existing warning is kept, not replaced: the passthrough deferral notice and this one
	// can both be true of the same upstream.
	got := withRedirectWarning(redirect, upstreamValidationResult{Warning: passthroughDeferredWarning})
	if !strings.Contains(got.Warning, passthroughDeferredWarning) || !strings.Contains(got.Warning, redirectCredentialWarning) {
		t.Fatalf("composed warning = %q, want both notices", got.Warning)
	}
}
