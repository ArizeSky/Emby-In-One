package backend

import (
	"strings"
	"testing"
)

// TestConfigRoundTripPreservesQuotedValues pins the writer and the hand-rolled
// parser against each other: values containing quotes, colons, hashes, or outer
// whitespace must survive a save/load cycle unchanged.
func TestConfigRoundTripPreservesQuotedValues(t *testing.T) {
	values := []string{
		"plain",
		"with space",
		"with: colon",
		"with # hash",
		"trailing'quote",
		"'leading-quote",
		"both'quotes'",
		`double"quote`,
		`mixed'"quotes`,
		"'''",
		" lead",
		"trail ",
		"中文'引号",
		"",
	}

	for _, value := range values {
		cfg := Config{
			Server: ServerConfig{Port: 8096, Name: "srv-" + value, ID: "server-id"},
			Admin:  AdminConfig{Username: "admin", Password: value},
			Proxies: []ProxyConfig{{
				ID: "p1", Name: "proxy-" + value, URL: "http://proxy.example:8080",
			}},
			Upstream: []UpstreamConfig{{
				Name: "up-" + value, URL: "http://emby.example:8096",
				Username: "user-" + value, Password: value,
			}},
		}

		parsed, err := parseConfigYAML(renderConfigYAML(&cfg))
		if err != nil {
			t.Fatalf("parse rendered config: %v", err)
		}
		if parsed.Admin.Password != value {
			t.Errorf("admin password %q round-tripped to %q", value, parsed.Admin.Password)
		}
		if want := "srv-" + value; parsed.Server.Name != want {
			t.Errorf("server name %q round-tripped to %q", want, parsed.Server.Name)
		}
		if len(parsed.Proxies) != 1 {
			t.Fatalf("proxy count = %d, want 1", len(parsed.Proxies))
		}
		if want := "proxy-" + value; parsed.Proxies[0].Name != want {
			t.Errorf("proxy name %q round-tripped to %q", want, parsed.Proxies[0].Name)
		}
		if len(parsed.Upstream) != 1 {
			t.Fatalf("upstream count = %d, want 1", len(parsed.Upstream))
		}
		if want := "up-" + value; parsed.Upstream[0].Name != want {
			t.Errorf("upstream name %q round-tripped to %q", want, parsed.Upstream[0].Name)
		}
		if want := "user-" + value; parsed.Upstream[0].Username != want {
			t.Errorf("upstream username %q round-tripped to %q", want, parsed.Upstream[0].Username)
		}
		if parsed.Upstream[0].Password != value {
			t.Errorf("upstream password %q round-tripped to %q", value, parsed.Upstream[0].Password)
		}
	}
}

// TestLoadConfigStoreRejectsOutOfRangeTimeout covers the hand-edited config file:
// a timeout large enough to overflow time.Duration has to fail startup loudly
// instead of silently disabling the request timeout.
func TestLoadConfigStoreRejectsOutOfRangeTimeout(t *testing.T) {
	config := strings.Replace(parityConfigWithUpstreams(""), "  api: 30000", "  api: 999999999999999", 1)
	dir := prepareTempWorkspace(t, config, nil)
	chdirForTest(t, dir)

	_, err := LoadConfigStore()
	if err == nil {
		t.Fatalf("expected an out-of-range timeout to be rejected at startup")
	}
	if !strings.Contains(err.Error(), "timeouts.api") {
		t.Fatalf("error should name the offending field: %v", err)
	}
}

// TestParseStringValueKeepsUnbalancedQuotes documents the choice to leave input
// that is not a quoted scalar alone instead of stripping a stray quote.
func TestParseStringValueKeepsUnbalancedQuotes(t *testing.T) {
	cases := map[string]string{
		"'open":       "'open",
		"close'":      "close'",
		"'":           "'",
		"plain":       "plain",
		"null":        "",
		"'quoted'":    "quoted",
		`"double"`:    "double",
		"''":          "",
		"'it''s'":     "it's",
		"'a # b'":     "a # b",
		"no-quote #x": "no-quote #x",
	}
	for input, want := range cases {
		if got := parseStringValue(input); got != want {
			t.Errorf("parseStringValue(%q) = %q, want %q", input, got, want)
		}
	}
}
