package backend

 import (
 	"fmt"
 	"os"
 	"path/filepath"
 	"strings"
 	"testing"
 	"time"
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
 
 // TestConfigStoreConcurrentSavesKeepEveryMutation is the regression for the
 // lost update in Save(): it used to render under a read lock and write the
 // file after releasing it, so a save that interleaved with another one could
 // write a stale render and silently drop the mutations the newer render
 // already contained. The lock is now held across render and write, so the
 // persisted file always matches the in-memory state.
 //
 // The recipe is deterministic: the first save renders and writes a large
 // config (slow), the second save shrinks the config and writes a tiny one
 // (fast). Without the lock held across the first save, the small write lands
 // first and the slow stale render lands last, resurrecting the deleted
 // upstreams on disk.
 func TestConfigStoreConcurrentSavesKeepEveryMutation(t *testing.T) {
 	dir := t.TempDir()
 	path := filepath.Join(dir, "config.yaml")
 	upstreams := make([]UpstreamConfig, 20000)
 	for i := range upstreams {
 		upstreams[i] = UpstreamConfig{
 			ID: fmt.Sprintf("srv-%06d", i), Name: fmt.Sprintf("Server %d", i),
 			URL: fmt.Sprintf("http://emby-%d.example:8096", i),
 			Username: "user", Password: "pass", PlaybackMode: "proxy",
 			SpoofClient: "custom", StreamingURLs: []string{fmt.Sprintf("http://stream-%d.example:8096", i)},
 		}
 	}
 	store := &ConfigStore{config: &Config{
 		Path:     path,
 		Server:   ServerConfig{Port: 8096, Name: "base", ID: "svr"},
 		Admin:    AdminConfig{Username: "admin", Password: "secret"},
 		Playback: PlaybackConfig{Mode: "proxy"},
 		Timeouts: TimeoutsConfig{API: 30000, Global: 15000, Login: 30000, HealthCheck: 30000, HealthInterval: 60000},
 		Upstream: upstreams,
 	}}
 	if err := store.Save(); err != nil {
 		t.Fatalf("initial save: %v", err)
 	}
 
 	// Save A: the large config, slow render and slow write, in flight.
 	saveADone := make(chan error, 1)
 	go func() { saveADone <- store.Save() }()
 	// A is now mid-render (or mid-write) on the large config.
 	time.Sleep(5 * time.Millisecond)
 
 	// Save B: shrink the config, then save — a fast render and a fast write.
 	saveBDone := make(chan error, 1)
 	go func() {
 		if err := store.Mutate(func(cfg *Config) error {
 			cfg.Upstream = cfg.Upstream[:10]
 			return nil
 		}); err != nil {
 			saveBDone <- err
 			return
 		}
 		saveBDone <- store.Save()
 	}()
 
 	if err := <-saveADone; err != nil {
 		t.Fatalf("save A: %v", err)
 	}
 	if err := <-saveBDone; err != nil {
 		t.Fatalf("save B: %v", err)
 	}
 
 	// The persisted file must match the in-memory state: ten upstreams.
 	raw, err := os.ReadFile(path)
 	if err != nil {
 		t.Fatalf("read config: %v", err)
 	}
 	parsed, err := parseConfigYAML(string(raw))
 	if err != nil {
 		t.Fatalf("parse config: %v", err)
 	}
 	if len(parsed.Upstream) != 10 {
 		t.Fatalf("persisted %d upstreams, want 10: the shrink was overwritten by a stale render", len(parsed.Upstream))
 	}
 }
