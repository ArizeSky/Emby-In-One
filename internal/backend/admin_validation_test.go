package backend

import (
	"net/http"
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

func TestAdminSettingsAcceptsZeroGracePeriod(t *testing.T) {
	withTempApp(t, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")

		rr := doJSONRequest(t, handler, http.MethodPut, "/admin/api/settings",
			map[string]any{"timeouts": map[string]any{"searchGracePeriod": 0, "metadataGracePeriod": 0}}, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
		}
		got := app.ConfigStore.Snapshot().Timeouts
		if got.SearchGracePeriod != 0 || got.MetadataGracePeriod != 0 {
			t.Fatalf("zero (disabled) grace periods were not applied: %+v", got)
		}
	})
}
