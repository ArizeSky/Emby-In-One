package backend

import (
	"strings"
	"testing"
)

func upstreamActivityScopeTestConfig() *UpstreamConfig {
	return &UpstreamConfig{
		ID:       "upstream-1",
		Name:     "Living Room",
		URL:      "https://Emby.Example.com:443/emby/",
		Username: "viewer",
		Password: "password-one",
		APIKey:   "",
		ProxyID:  "proxy-a",
		StreamingURLs: []string{
			"https://stream.example.com/one/",
		},
		CustomUserAgent:     "Agent/1.0",
		CustomClient:        "Client",
		CustomClientVersion: "1.0",
		CustomDeviceName:    "Device",
		MaxConcurrent:       2,
	}
}

func TestUpstreamAccountScopeStableAcrossCosmeticChanges(t *testing.T) {
	base := upstreamActivityScopeTestConfig()
	scope := UpstreamAccountScope(base)
	if scope == "" {
		t.Fatal("configured upstream must have a scope")
	}
	if !strings.HasPrefix(scope, upstreamAccountScopeVersion+":") {
		t.Fatalf("scope missing version prefix: %q", scope)
	}
	if len(scope) != len(upstreamAccountScopeVersion)+1+64 {
		t.Fatalf("scope is not a hex SHA-256 digest: %q", scope)
	}

	changed := *base
	changed.ID = "reordered-id"
	changed.Name = "Renamed"
	changed.Password = "password-two" // passwords are excluded on purpose
	changed.ProxyID = "proxy-b"
	changed.StreamingURL = "https://stream.example.com/primary/"
	changed.StreamingURLs = []string{"https://stream.example.com/second/"}
	changed.CustomUserAgent = "Agent/2.0"
	changed.CustomClient = "OtherClient"
	changed.CustomClientVersion = "9.9"
	changed.CustomDeviceName = "OtherDevice"
	changed.CustomDeviceId = "other-device-id"
	changed.SpoofClient = "spoof"
	changed.UAProfileID = "ua-profile"
	changed.FollowRedirects = true
	changed.PriorityMetadata = true
	changed.AggregationDisabled = true
	changed.MaxConcurrent = 8
	changed.PlaybackMode = "transcode"
	changed.DirectRedirect = true
	changed.PagedScan = true
	if got := UpstreamAccountScope(&changed); !SameUpstreamAccountScope(scope, got) {
		t.Fatalf("cosmetic edits changed the account scope:\nbase=%s\nnew =%s", scope, got)
	}

	// URL spelling differences that mean the same API base keep the scope.
	for _, raw := range []string{
		"HTTPS://emby.example.com/emby",
		"https://EMBY.example.com/emby/",
		"https://emby.example.com:443/emby///",
		"  https://emby.example.com/emby  ",
	} {
		cfg := *base
		cfg.URL = raw
		if got := UpstreamAccountScope(&cfg); !SameUpstreamAccountScope(scope, got) {
			t.Fatalf("URL spelling %q changed the scope", raw)
		}
	}

	// Query strings, fragments and userinfo are not part of the account range.
	cfg := *base
	cfg.URL = "https://someone:secret@emby.example.com/emby?token=temporary#fragment"
	if got := UpstreamAccountScope(&cfg); !SameUpstreamAccountScope(scope, got) {
		t.Fatalf("URL query/userinfo changed the scope")
	}
}

func TestUpstreamAccountScopeChangesWithAccountIdentity(t *testing.T) {
	base := upstreamActivityScopeTestConfig()
	scope := UpstreamAccountScope(base)

	cases := []struct {
		name   string
		mutate func(cfg *UpstreamConfig)
	}{
		{"api base", func(cfg *UpstreamConfig) { cfg.URL = "https://emby.example.com/other" }},
		{"host", func(cfg *UpstreamConfig) { cfg.URL = "https://other.example.com/emby/" }},
		{"username", func(cfg *UpstreamConfig) { cfg.Username = "another-viewer" }},
		{"auth mode", func(cfg *UpstreamConfig) { cfg.APIKey = "api-key-one" }},
		{"api key", func(cfg *UpstreamConfig) { cfg.Username = ""; cfg.APIKey = "api-key-one" }},
	}
	for _, tc := range cases {
		cfg := *base
		tc.mutate(&cfg)
		if got := UpstreamAccountScope(&cfg); SameUpstreamAccountScope(scope, got) {
			t.Fatalf("%s change kept the account scope", tc.name)
		}
	}

	// API key rotation resets the range; the key itself never appears.
	keyOne := *base
	keyOne.APIKey = "api-key-one"
	keyTwo := *base
	keyTwo.APIKey = "api-key-two"
	first := UpstreamAccountScope(&keyOne)
	second := UpstreamAccountScope(&keyTwo)
	if first == "" || second == "" || SameUpstreamAccountScope(first, second) {
		t.Fatalf("API key rotation must change the scope: %q vs %q", first, second)
	}
	if strings.Contains(first, "api-key-one") || strings.Contains(second, "api-key-two") {
		t.Fatal("plaintext API key leaked into the scope fingerprint")
	}

	// Same API key with a different username is a different account range.
	otherUser := keyOne
	otherUser.Username = "another-viewer"
	if SameUpstreamAccountScope(first, UpstreamAccountScope(&otherUser)) {
		t.Fatal("username change with the same API key kept the scope")
	}

	// Switching from API key auth to password auth changes the range, but a
	// password change alone does not (covered above).
	passwordAuth := keyOne
	passwordAuth.APIKey = ""
	if SameUpstreamAccountScope(first, UpstreamAccountScope(&passwordAuth)) {
		t.Fatal("auth mode change kept the scope")
	}
}

func TestUpstreamAccountScopeEmptyAndComparison(t *testing.T) {
	if got := UpstreamAccountScope(nil); got != "" {
		t.Fatalf("nil config scope = %q", got)
	}
	if got := UpstreamAccountScope(&UpstreamConfig{}); got != "" {
		t.Fatalf("empty config scope = %q", got)
	}
	urlOnly := UpstreamAccountScope(&UpstreamConfig{URL: "https://emby.example.com/emby"})
	if urlOnly == "" {
		t.Fatal("URL-only config must still produce a stable scope")
	}
	if !SameUpstreamAccountScope(urlOnly, urlOnly) {
		t.Fatal("scope must equal itself")
	}
	if SameUpstreamAccountScope("", "") {
		t.Fatal("unknown scopes must never match")
	}
	if SameUpstreamAccountScope(urlOnly, "") || SameUpstreamAccountScope("", urlOnly) {
		t.Fatal("empty scope must never match a real one")
	}
	if SameUpstreamAccountScope(urlOnly, urlOnly+"x") {
		t.Fatal("different scopes must not match")
	}
}
