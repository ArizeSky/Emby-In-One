package backend

import (
	"net/http"
	"net/url"
	"testing"
)

// --- Proxy Pool Tests ---

func TestFindProxy(t *testing.T) {
	proxies := []ProxyConfig{
		{ID: "p1", Name: "Japan", URL: "http://proxy1.example.com:8080"},
		{ID: "p2", Name: "US", URL: "http://user:pass@proxy2.example.com:3128"},
	}

	// Found
	p := findProxy(proxies, "p1")
	if p == nil || p.Name != "Japan" {
		t.Fatalf("expected to find proxy 'Japan', got %v", p)
	}

	// Not found
	p = findProxy(proxies, "p99")
	if p != nil {
		t.Fatalf("expected nil for unknown proxy ID, got %v", p)
	}

	// Empty ID
	p = findProxy(proxies, "")
	if p != nil {
		t.Fatalf("expected nil for empty proxy ID, got %v", p)
	}
}

func TestBuildProxyTransport(t *testing.T) {
	// Valid HTTP proxy
	transport, ok := buildProxyTransport("http://proxy.example.com:8080", nil, "TestServer")
	if !ok {
		t.Fatal("expected success for valid http proxy URL")
	}
	ht, isHTTP := transport.(*http.Transport)
	if !isHTTP {
		t.Fatal("expected *http.Transport")
	}
	// Verify proxy function is set by checking it returns the expected proxy URL
	proxyURL, err := ht.Proxy(&http.Request{URL: &url.URL{Scheme: "http", Host: "target.com"}})
	if err != nil {
		t.Fatalf("proxy function error: %v", err)
	}
	if proxyURL == nil || proxyURL.Host != "proxy.example.com:8080" {
		t.Fatalf("expected proxy host 'proxy.example.com:8080', got %v", proxyURL)
	}

	// Valid HTTPS proxy with auth
	transport, ok = buildProxyTransport("https://user:pass@proxy.example.com:3128", nil, "TestServer")
	if !ok {
		t.Fatal("expected success for valid https proxy URL with auth")
	}
	ht, isHTTP = transport.(*http.Transport)
	if !isHTTP {
		t.Fatal("expected *http.Transport")
	}
	proxyURL, err = ht.Proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: "target.com"}})
	if err != nil {
		t.Fatalf("proxy function error: %v", err)
	}
	if proxyURL.User == nil || proxyURL.User.Username() != "user" {
		t.Fatal("expected proxy auth user 'user'")
	}

	// Invalid scheme
	transport, ok = buildProxyTransport("socks5://proxy.example.com:1080", nil, "TestServer")
	if ok {
		t.Fatal("expected failure for socks5 scheme")
	}
	if transport != sharedTransport {
		t.Fatal("expected fallback to sharedTransport")
	}

	// Invalid URL
	transport, ok = buildProxyTransport("://bad-url", nil, "TestServer")
	if ok {
		t.Fatal("expected failure for invalid URL")
	}
	if transport != sharedTransport {
		t.Fatal("expected fallback to sharedTransport")
	}
}

func TestNewUpstreamClientWithProxy(t *testing.T) {
	cfg := Config{
		Proxies: []ProxyConfig{
			{ID: "jp1", Name: "Japan Proxy", URL: "http://proxy.jp:8080"},
		},
		Upstream: []UpstreamConfig{
			{Name: "ServerA", URL: "http://emby-a.example.com", ProxyID: "jp1", Username: "u", Password: "p"},
			{Name: "ServerB", URL: "http://emby-b.example.com", ProxyID: "", Username: "u", Password: "p"},
			{Name: "ServerC", URL: "http://emby-c.example.com", ProxyID: "nonexistent", Username: "u", Password: "p"},
		},
		Timeouts: TimeoutsConfig{API: 30000},
	}

	// ServerA should have per-client proxy transport
	clientA := newUpstreamClient(cfg, cfg.Upstream[0], 0, nil)
	if clientA.transport == sharedTransport {
		t.Fatal("ServerA should have a proxy-specific transport, not sharedTransport")
	}

	// ServerB should use shared transport (no proxy)
	clientB := newUpstreamClient(cfg, cfg.Upstream[1], 1, nil)
	if clientB.transport != sharedTransport {
		t.Fatal("ServerB should use sharedTransport")
	}

	// ServerC references nonexistent proxy, should fall back to shared
	clientC := newUpstreamClient(cfg, cfg.Upstream[2], 2, nil)
	if clientC.transport != sharedTransport {
		t.Fatal("ServerC with nonexistent proxyId should fall back to sharedTransport")
	}
}

// --- priorityMetadata Tests ---

func TestContainsChinese(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"Hello World", false},
		{"", false},
		{"这是一部好电影", true},
		{"A movie about 功夫", true},
		{"日本語テスト", true}, // Contains kanji in CJK Unified range
		{"한국어", false},   // Korean outside CJK Unified range
	}
	for _, tc := range tests {
		result := containsChinese(tc.input)
		if result != tc.expected {
			t.Errorf("containsChinese(%q) = %v, want %v", tc.input, result, tc.expected)
		}
	}
}

func TestIsBetterMetadata(t *testing.T) {
	cfg := Config{
		Upstream: []UpstreamConfig{
			{Name: "Server0", PriorityMetadata: false},
			{Name: "Server1", PriorityMetadata: true},
			{Name: "Server2", PriorityMetadata: false},
		},
	}

	// Test 1: priorityMetadata flag wins
	existing := map[string]any{"Overview": "A long English overview text here"}
	candidate := map[string]any{"Overview": "Short"}
	if !isBetterMetadata(existing, 0, candidate, 1, cfg) {
		t.Error("server with priorityMetadata=true should win regardless of overview")
	}

	// Test 2: Chinese overview wins over English
	existing = map[string]any{"Overview": "A very long English overview that is longer than the Chinese one"}
	candidate = map[string]any{"Overview": "一部好电影"}
	if !isBetterMetadata(existing, 0, candidate, 2, cfg) {
		t.Error("Chinese overview should beat English overview")
	}

	// Reverse: English should not beat Chinese
	if isBetterMetadata(candidate, 2, existing, 0, cfg) {
		t.Error("English overview should not beat Chinese overview")
	}

	// Test 3: Longer overview wins (both same language)
	existing = map[string]any{"Overview": "Short"}
	candidate = map[string]any{"Overview": "A much longer overview with more detail"}
	if !isBetterMetadata(existing, 0, candidate, 2, cfg) {
		t.Error("longer overview should win when no Chinese difference")
	}

	// Test 4: Lower server index wins (all else equal)
	existing = map[string]any{"Overview": "Same"}
	candidate = map[string]any{"Overview": "Same"}
	if isBetterMetadata(existing, 0, candidate, 2, cfg) {
		t.Error("higher server index should not beat lower server index when all else equal")
	}
	if !isBetterMetadata(existing, 2, candidate, 0, cfg) {
		t.Error("lower server index should win when all else equal")
	}

	// Test 5: Both have priorityMetadata, fall through to Chinese
	cfg2 := Config{
		Upstream: []UpstreamConfig{
			{Name: "S0", PriorityMetadata: true},
			{Name: "S1", PriorityMetadata: true},
		},
	}
	existing = map[string]any{"Overview": "English text"}
	candidate = map[string]any{"Overview": "中文简介"}
	if !isBetterMetadata(existing, 0, candidate, 1, cfg2) {
		t.Error("when both have priorityMetadata, Chinese should still win")
	}
}

func TestMergedItemsPayloadPriorityMetadata(t *testing.T) {
	config := `
server:
  port: 8096
  name: TestServer
admin:
  username: admin
  password: testpass
upstream: []
`
	withTempAppPrepared(t, config, nil, func(app *App, handler http.Handler, dir string) {
		// Server 0: no priority, English overview
		// Server 1: priorityMetadata=true, shorter overview
		app.ConfigStore.Mutate(func(cfg *Config) error {
			cfg.Upstream = []UpstreamConfig{
				{Name: "S0", PriorityMetadata: false},
				{Name: "S1", PriorityMetadata: true},
			}
			return nil
		})

		results := []upstreamItemsResult{
			{
				ServerIndex: 0,
				Items: []map[string]any{
					{
						"Id":             "orig-0",
						"Type":           "Movie",
						"Name":           "Test Movie",
						"Overview":       "Long English overview",
						"ProductionYear": 2024,
						"ProviderIds":    map[string]any{"Tmdb": "12345"},
					},
				},
			},
			{
				ServerIndex: 1,
				Items: []map[string]any{
					{
						"Id":             "orig-1",
						"Type":           "Movie",
						"Name":           "Test Movie",
						"Overview":       "优先元数据",
						"ProductionYear": 2024,
						"ProviderIds":    map[string]any{"Tmdb": "12345"},
					},
				},
			},
		}

		payload := app.mergedItemsPayload(results)
		items, _ := payload["Items"].([]any)
		if len(items) != 1 {
			t.Fatalf("expected 1 merged item, got %d", len(items))
		}

		item := items[0].(map[string]any)
		overview, _ := item["Overview"].(string)
		if overview != "优先元数据" {
			t.Errorf("expected overview from priorityMetadata server, got %q", overview)
		}
	})
}
