package backend

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"
)

// The dual-cache switch is a global admin setting that must survive the full
// round trip: PUT /admin/api/settings persists it, GET reports it back, the
// YAML round trip keeps it, and the authenticated web endpoint
// /System/PlaybackFeatures exposes exactly the persisted value. The zero
// value (an old config without the key) must stay off so existing deployments
// keep baseline playback.
func TestPlaybackDualCacheSwitchRoundTrip(t *testing.T) {
	withTempAppConfig(t, singleUpstreamConfig("http://upstream.invalid"), func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")

		// Default: absent key means off, both on the API and on the web endpoint.
		rr := doJSONRequest(t, handler, http.MethodGet, "/admin/api/settings", nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("settings GET status = %d, body=%s", rr.Code, rr.Body.String())
		}
		var initial struct {
			DualCache bool `json:"dualCache"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &initial); err != nil {
			t.Fatalf("settings GET body: %v", err)
		}
		if initial.DualCache {
			t.Fatalf("dualCache default = true, want false")
		}
		webRR := doJSONRequest(t, handler, http.MethodGet, "/System/PlaybackFeatures", nil, token)
		if webRR.Code != http.StatusOK {
			t.Fatalf("PlaybackFeatures status = %d, body=%s", webRR.Code, webRR.Body.String())
		}
		var webInitial struct {
			DualCache bool `json:"dualCache"`
		}
		if err := json.Unmarshal(webRR.Body.Bytes(), &webInitial); err != nil {
			t.Fatalf("PlaybackFeatures body: %v", err)
		}
		if webInitial.DualCache {
			t.Fatalf("web dualCache default = true, want false")
		}

		// Turn it on.
		rr = doJSONRequest(t, handler, http.MethodPut, "/admin/api/settings", map[string]any{
			"dualCache": true,
		}, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("settings PUT status = %d, body=%s", rr.Code, rr.Body.String())
		}

		cfg := app.ConfigStore.Snapshot()
		if !cfg.Playback.DualCacheEnabled {
			t.Fatalf("config dualCache after PUT = false, want true")
		}

		// The persisted YAML must round trip the flag.
		reparsed := parseConfigYAMLOrDie(t, app)
		if !reparsed.Playback.DualCacheEnabled {
			t.Fatalf("dualCache lost across the YAML round trip")
		}

		// Both read endpoints report the persisted value.
		rr = doJSONRequest(t, handler, http.MethodGet, "/admin/api/settings", nil, token)
		var after struct {
			DualCache bool `json:"dualCache"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &after); err != nil {
			t.Fatalf("settings GET body: %v", err)
		}
		if !after.DualCache {
			t.Fatalf("settings dualCache after PUT = false, want true")
		}
		webRR = doJSONRequest(t, handler, http.MethodGet, "/System/PlaybackFeatures", nil, token)
		var webAfter struct {
			DualCache bool `json:"dualCache"`
		}
		if err := json.Unmarshal(webRR.Body.Bytes(), &webAfter); err != nil {
			t.Fatalf("PlaybackFeatures body: %v", err)
		}
		if !webAfter.DualCache {
			t.Fatalf("web dualCache after PUT = false, want true")
		}

		// Turn it back off.
		rr = doJSONRequest(t, handler, http.MethodPut, "/admin/api/settings", map[string]any{
			"dualCache": false,
		}, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("settings PUT (off) status = %d, body=%s", rr.Code, rr.Body.String())
		}
		if app.ConfigStore.Snapshot().Playback.DualCacheEnabled {
			t.Fatalf("config dualCache after off PUT = true, want false")
		}
	})
}

// A hand-edited config with playback.dualCache: true must parse to the enabled
// flag, and the legacy shape without the key must stay disabled.
func TestParsePlaybackDualCacheYAML(t *testing.T) {
	cfg, err := parseConfigYAML("server:\n  port: 8096\nadmin:\n  username: a\n  password: b\nplayback:\n  mode: proxy\n  dualCache: true\nupstream:\n  - name: A\n    url: 'http://api.example'\n    username: u\n    password: p\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !cfg.Playback.DualCacheEnabled {
		t.Fatalf("dualCache: true parsed as disabled")
	}

	legacy, err := parseConfigYAML("server:\n  port: 8096\nadmin:\n  username: a\n  password: b\nplayback:\n  mode: proxy\nupstream:\n  - name: A\n    url: 'http://api.example'\n    username: u\n    password: p\n")
	if err != nil {
		t.Fatalf("parse legacy: %v", err)
	}
	if legacy.Playback.DualCacheEnabled {
		t.Fatalf("legacy config without dualCache parsed as enabled")
	}
}

func parseConfigYAMLOrDie(t *testing.T, app *App) *Config {
	t.Helper()
	raw, err := os.ReadFile(app.ConfigStore.Snapshot().Path)
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	cfg, err := parseConfigYAML(string(raw))
	if err != nil {
		t.Fatalf("re-parse persisted config: %v", err)
	}
	return cfg
}
