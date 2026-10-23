package backend

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestValidateTimeoutsRanges(t *testing.T) {
	valid := TimeoutsConfig{
		API: 30000, Global: 15000, Login: 10000, HealthCheck: 10000, HealthInterval: 60000,
		SearchGracePeriod: 3000, MetadataGracePeriod: 3000, LatestGracePeriod: 0,
	}
	if err := validateTimeouts(valid); err != nil {
		t.Fatalf("shipped defaults must validate: %v", err)
	}
	if err := validateTimeouts(TimeoutsConfig{
		API: 1, Global: 1, Login: 1, HealthCheck: 1, HealthInterval: 1,
		SearchGracePeriod: 0, MetadataGracePeriod: 0, LatestGracePeriod: 0,
	}); err != nil {
		t.Fatalf("lower bounds and disabled grace periods must validate: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*TimeoutsConfig)
		field  string
	}{
		{"api overflows time.Duration", func(c *TimeoutsConfig) { c.API = 9223372036855 }, "timeouts.api"},
		{"api past the cap", func(c *TimeoutsConfig) { c.API = maxTimeoutMillis + 1 }, "timeouts.api"},
		{"api zero", func(c *TimeoutsConfig) { c.API = 0 }, "timeouts.api"},
		{"api negative", func(c *TimeoutsConfig) { c.API = -1 }, "timeouts.api"},
		{"global negative", func(c *TimeoutsConfig) { c.Global = -5 }, "timeouts.global"},
		{"login past the cap", func(c *TimeoutsConfig) { c.Login = maxTimeoutMillis + 1 }, "timeouts.login"},
		{"healthCheck past the cap", func(c *TimeoutsConfig) { c.HealthCheck = maxTimeoutMillis + 1 }, "timeouts.healthCheck"},
		{"healthInterval past the cap", func(c *TimeoutsConfig) { c.HealthInterval = maxIntervalMillis + 1 }, "timeouts.healthInterval"},
		{"search grace negative", func(c *TimeoutsConfig) { c.SearchGracePeriod = -1 }, "timeouts.searchGracePeriod"},
		{"search grace past the cap", func(c *TimeoutsConfig) { c.SearchGracePeriod = maxGraceMillis + 1 }, "timeouts.searchGracePeriod"},
		{"metadata grace past the cap", func(c *TimeoutsConfig) { c.MetadataGracePeriod = maxGraceMillis + 1 }, "timeouts.metadataGracePeriod"},
		{"latest grace past the cap", func(c *TimeoutsConfig) { c.LatestGracePeriod = maxGraceMillis + 1 }, "timeouts.latestGracePeriod"},
	}
	for _, tc := range cases {
		cfg := valid
		tc.mutate(&cfg)
		err := validateTimeouts(cfg)
		if err == nil {
			t.Errorf("%s: expected a validation error", tc.name)
			continue
		}
		if !strings.Contains(err.Error(), tc.field) {
			t.Errorf("%s: error %q should name %s", tc.name, err.Error(), tc.field)
		}
	}
}

// TestAdminSettingsRejectsOutOfRangeTimeout keeps a mistyped timeout out of the
// config entirely: no partial write, no silently disabled request timeout.
func TestAdminSettingsRejectsOutOfRangeTimeout(t *testing.T) {
	withTempApp(t, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		before := app.ConfigStore.Snapshot().Timeouts

		rr := doJSONRequest(t, handler, http.MethodPut, "/admin/api/settings",
			map[string]any{"timeouts": map[string]any{"api": 999999999999999}}, token)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "timeouts.api") {
			t.Fatalf("error should name the offending field: %s", rr.Body.String())
		}
		if got := app.ConfigStore.Snapshot().Timeouts; got != before {
			t.Fatalf("rejected update changed the config: %+v", got)
		}
	})
}

func TestAdminSettingsRejectsUnparseableTimeoutsAtomically(t *testing.T) {
	cases := []struct {
		name, body, field string
	}{
		{"fractional value", `{"serverName":"must-not-save","timeouts":{"api":1.5}}`, "timeouts.api"},
		{"exponent value", `{"timeouts":{"api":1e3}}`, "timeouts.api"},
		{"beyond int64", `{"timeouts":{"api":9223372036854775808}}`, "timeouts.api"},
		{"past milliseconds cap", `{"timeouts":{"api":3600001}}`, "timeouts.api"},
		{"negative value", `{"timeouts":{"api":-1}}`, "timeouts.api"},
		{"zero positive timeout", `{"timeouts":{"api":0}}`, "timeouts.api"},
		{"negative grace period", `{"timeouts":{"searchGracePeriod":-1}}`, "timeouts.searchGracePeriod"},
		{"empty string", `{"timeouts":{"api":""}}`, "timeouts.api"},
		{"null value", `{"timeouts":{"api":null}}`, "timeouts.api"},
	}
	withTempApp(t, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		before := app.ConfigStore.Snapshot()
		beforeDisk, err := os.ReadFile(before.Path)
		if err != nil {
			t.Fatalf("read initial config: %v", err)
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				rr := doJSONRequest(t, handler, http.MethodPut, "/admin/api/settings", tc.body, token)
				if rr.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
				}
				if !strings.Contains(rr.Body.String(), tc.field) {
					t.Fatalf("error should name %s: %s", tc.field, rr.Body.String())
				}
				got := app.ConfigStore.Snapshot()
				if got.Server.Name != before.Server.Name || got.Timeouts != before.Timeouts {
					t.Fatalf("rejected update partially changed config: before=%+v after=%+v", before, got)
				}
				afterDisk, err := os.ReadFile(before.Path)
				if err != nil {
					t.Fatalf("read config after rejection: %v", err)
				}
				if string(afterDisk) != string(beforeDisk) {
					t.Fatal("rejected update changed the persisted config")
				}
			})
		}
	})
}

func TestAdminSettingsAcceptsZeroGracePeriod(t *testing.T) {
	withTempApp(t, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		before := app.ConfigStore.Snapshot()

		rr := doJSONRequest(t, handler, http.MethodPut, "/admin/api/settings",
			`{"serverName":"Saved Settings","timeouts":{"api":45000,"searchGracePeriod":0,"metadataGracePeriod":0}}`, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
		}
		got := app.ConfigStore.Snapshot()
		if got.Server.Name != "Saved Settings" || got.Timeouts.API != 45000 || got.Timeouts.SearchGracePeriod != 0 || got.Timeouts.MetadataGracePeriod != 0 ||
			got.Timeouts.Global != before.Timeouts.Global || got.Timeouts.Login != before.Timeouts.Login || got.Timeouts.HealthCheck != before.Timeouts.HealthCheck || got.Timeouts.HealthInterval != before.Timeouts.HealthInterval {
			t.Fatalf("valid settings were not applied (including omitted-field preservation): %+v", got)
		}
		disk, err := os.ReadFile(got.Path)
		if err != nil {
			t.Fatalf("read persisted config: %v", err)
		}
		if !strings.Contains(string(disk), "api: 45000") {
			t.Fatalf("valid settings were not persisted: %s", disk)
		}
	})
}

// TestValidateUpstreamDraftRejectsLinkLocalTargets is the regression for the
// proxied-upstream SSRF gap: when an upstream is reached through a forward proxy,
// the transport dialer only ever resolves the proxy address, so a link-local
// (cloud metadata) target would never be screened. validateUpstreamDraft now
// rejects such targets directly.
func TestValidateUpstreamDraftRejectsLinkLocalTargets(t *testing.T) {
	base := UpstreamConfig{
		Name:         "A",
		PlaybackMode: "proxy",
		SpoofClient:  "passthrough",
		Username:     "u",
		Password:     "p",
	}
	linkLocal := base
	linkLocal.URL = "http://169.254.169.254/latest/meta-data"
	if err := validateUpstreamDraft(context.Background(), linkLocal); err == nil {
		t.Fatal("expected a link-local upstream URL to be rejected")
	}

	linkLocalStream := base
	linkLocalStream.URL = "http://emby.example.com"
	linkLocalStream.StreamingURLs = []string{"http://[fe80::1]/streams"}
	if err := validateUpstreamDraft(context.Background(), linkLocalStream); err == nil {
		t.Fatal("expected a link-local streaming URL to be rejected")
	}

	// Loopback and private addresses stay allowed: self-hosted Emby commonly runs
	// on the same host or LAN, and the upstream dialer policy matches.
	loopback := base
	loopback.URL = "http://127.0.0.1:8096"
	if err := validateUpstreamDraft(context.Background(), loopback); err != nil {
		t.Fatalf("loopback upstream should be allowed, got %v", err)
	}
	private := base
	private.URL = "http://192.168.1.10:8096"
	if err := validateUpstreamDraft(context.Background(), private); err != nil {
		t.Fatalf("private upstream should be allowed, got %v", err)
	}
}

// TestValidateUpstreamDraftAllowsBlankPassword locks in the credential
// semantics the panel form documents: the upstream account is required, the
// password is optional (Emby permits blank-password accounts), and exactly one
// of username / apiKey must be present.
func TestValidateUpstreamDraftAllowsBlankPassword(t *testing.T) {
	base := UpstreamConfig{
		Name:         "A",
		URL:          "http://127.0.0.1:8096",
		PlaybackMode: "proxy",
		SpoofClient:  "passthrough",
	}
	blankPassword := base
	blankPassword.Username = "u"
	if err := validateUpstreamDraft(context.Background(), blankPassword); err != nil {
		t.Fatalf("username with blank password should validate, got %v", err)
	}

	noCredential := base
	if err := validateUpstreamDraft(context.Background(), noCredential); err == nil {
		t.Fatal("expected a draft with neither username nor apiKey to be rejected")
	}

	both := base
	both.Username = "u"
	both.APIKey = "key"
	if err := validateUpstreamDraft(context.Background(), both); err == nil {
		t.Fatal("expected a draft carrying both username and apiKey to be rejected")
	}
}
