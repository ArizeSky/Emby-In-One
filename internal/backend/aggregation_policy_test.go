package backend

import "testing"

// TestAggregationPolicyCombinesGrantsAndSetting covers the pure policy helper.
// It deliberately does not assert online/offline behaviour: the helper does not
// read online state, and that combination is exercised by the content-route
// tests through the candidate collectors.
func TestAggregationPolicyCombinesGrantsAndSetting(t *testing.T) {
	store := &ConfigStore{config: &Config{
		Upstream: []UpstreamConfig{
			{ID: "s1"},
			{ID: "s2", AggregationDisabled: true},
		},
	}}
	app := &App{ConfigStore: store}

	admin := &RequestContext{ProxyUser: &tokenInfo{Role: "admin"}}
	userAllowedS1 := &RequestContext{ProxyUser: &tokenInfo{Role: "user", AllowedServers: []string{"s1"}}}
	userAllowedS2 := &RequestContext{ProxyUser: &tokenInfo{Role: "user", AllowedServers: []string{"s2"}}}

	cases := []struct {
		name     string
		reqCtx   *RequestContext
		serverID string
		want     bool
	}{
		{"admin enabled", admin, "s1", true},
		{"admin disabled", admin, "s2", false},
		{"user granted enabled", userAllowedS1, "s1", true},
		{"user not granted", userAllowedS1, "s2", false},
		{"user granted but disabled", userAllowedS2, "s2", false},
		{"nil request context", nil, "s1", false},
		{"unknown id", admin, "missing", false},
		{"empty id", admin, "", false},
	}
	for _, tc := range cases {
		if got := app.canUseAggregatedServer(tc.reqCtx, tc.serverID); got != tc.want {
			t.Errorf("%s: canUseAggregatedServer = %v, want %v", tc.name, got, tc.want)
		}
	}

	// A nil store never reports a server as enabled.
	var nilStore *ConfigStore
	if nilStore.aggregationEnabled("s1") {
		t.Errorf("nil store should report aggregation disabled")
	}
}
