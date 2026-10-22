package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func parityConfigWithUpstreams(upstreams string) string {
	return "server:\n  port: 8096\n  name: \"Test Server\"\n  id: \"server-1\"\n\nadmin:\n  username: \"admin\"\n  password: \"secret\"\n\nplayback:\n  mode: \"proxy\"\n\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 100\n\ndataDir: \"data\"\n\nproxies: []\nupstream:\n" + upstreams
}

func passthroughFixtureHeaders(client string) map[string]any {
	base := strings.ReplaceAll(strings.ToLower(client), " ", "-")
	return map[string]any{
		"User-Agent":            []string{client + " UA"},
		"X-Emby-Client":         []string{client},
		"X-Emby-Client-Version": []string{"1.0.0"},
		"X-Emby-Device-Name":    []string{client + " Device"},
		"X-Emby-Device-Id":      []string{base + "-device"},
		"Accept":                []string{"application/json"},
		"Accept-Language":       []string{"zh-CN"},
	}
}

func stablePassthroughKey(rawURL, name string) string {
	return strings.TrimRight(rawURL, "/") + "|" + name + "|passthrough"
}

func writeCapturedHeadersFixture(t *testing.T, dir string, payload map[string]any) {
	t.Helper()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir data dir: %v", err)
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatalf("marshal captured headers fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "captured-headers.json"), encoded, 0o644); err != nil {
		t.Fatalf("write captured headers fixture: %v", err)
	}
}

func callResolvePassthroughHeadersForServer(t *testing.T, svc *ClientIdentityService, live http.Header, token, serverKey string) (string, http.Header) {
	t.Helper()
	method := reflect.ValueOf(svc).MethodByName("ResolvePassthroughHeadersForServer")
	if !method.IsValid() {
		t.Fatalf("ClientIdentityService.ResolvePassthroughHeadersForServer is missing")
	}
	results := method.Call([]reflect.Value{reflect.ValueOf(live), reflect.ValueOf(token), reflect.ValueOf(serverKey)})
	switch len(results) {
	case 1:
		resolved := results[0]
		source := resolved.FieldByName("Source")
		headers := resolved.FieldByName("Headers")
		if !source.IsValid() || !headers.IsValid() {
			t.Fatalf("ResolvePassthroughHeadersForServer return value missing Source/Headers fields")
		}
		resultHeaders, ok := headers.Interface().(http.Header)
		if !ok {
			t.Fatalf("ResolvePassthroughHeadersForServer headers type = %T, want http.Header", headers.Interface())
		}
		return source.String(), resultHeaders
	case 2:
		source, _ := results[0].Interface().(string)
		resultHeaders, ok := results[1].Interface().(http.Header)
		if !ok {
			t.Fatalf("ResolvePassthroughHeadersForServer headers type = %T, want http.Header", results[1].Interface())
		}
		return source, resultHeaders
	default:
		t.Fatalf("ResolvePassthroughHeadersForServer returned %d values, want 1 or 2", len(results))
		return "", nil
	}
}

func TestResolvePassthroughHeadersSupportsFiveLevelChain(t *testing.T) {
	upstreamA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "offline", http.StatusUnauthorized)
	}))
	defer upstreamA.Close()
	upstreamB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "offline", http.StatusUnauthorized)
	}))
	defer upstreamB.Close()

	config := parityConfigWithUpstreams(fmt.Sprintf("  - name: \"A\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n    spoofClient: \"passthrough\"\n  - name: \"B\"\n    url: %q\n    username: \"u2\"\n    password: \"p2\"\n    spoofClient: \"passthrough\"\n", upstreamA.URL, upstreamB.URL))

	withTempAppPrepared(t, config, func(dir string) {
		writeCapturedHeadersFixture(t, dir, map[string]any{
			"version": 1,
			"latestCaptured": map[string]any{
				"headers":    passthroughFixtureHeaders("Latest Client"),
				"capturedAt": "2026-03-23T00:00:00Z",
			},
			"lastSuccessByServer": map[string]any{
				stablePassthroughKey(upstreamA.URL, "A"): map[string]any{
					"headers":    passthroughFixtureHeaders("Server Last Client"),
					"capturedAt": "2026-03-23T00:00:01Z",
				},
			},
		})
	}, func(app *App, handler http.Handler, dir string) {
		app.Identity.SetCaptured("token-a", http.Header{
			"User-Agent":            []string{"Token Client UA"},
			"X-Emby-Client":         []string{"Token Client"},
			"X-Emby-Client-Version": []string{"2.0.0"},
			"X-Emby-Device-Name":    []string{"Token Device"},
			"X-Emby-Device-Id":      []string{"token-device"},
		})

		source, headers := callResolvePassthroughHeadersForServer(t, app.Identity, http.Header{
			"User-Agent":            []string{"Live Client UA"},
			"X-Emby-Client":         []string{"Live Client"},
			"X-Emby-Client-Version": []string{"3.0.0"},
			"X-Emby-Device-Name":    []string{"Live Device"},
			"X-Emby-Device-Id":      []string{"live-device"},
		}, "token-a", stablePassthroughKey(upstreamA.URL, "A"))
		if source != "live-request" || headers.Get("X-Emby-Client") != "Live Client" {
			t.Fatalf("live-request resolution = (%q, %q), want live-request / Live Client", source, headers.Get("X-Emby-Client"))
		}

		source, headers = callResolvePassthroughHeadersForServer(t, app.Identity, http.Header{}, "token-a", stablePassthroughKey(upstreamA.URL, "A"))
		if source != "captured-token" || headers.Get("X-Emby-Client") != "Token Client" {
			t.Fatalf("captured-token resolution = (%q, %q), want captured-token / Token Client", source, headers.Get("X-Emby-Client"))
		}

		source, headers = callResolvePassthroughHeadersForServer(t, app.Identity, http.Header{}, "missing-token", stablePassthroughKey(upstreamA.URL, "A"))
		if source != "last-success" || headers.Get("X-Emby-Client") != "Server Last Client" {
			t.Fatalf("last-success resolution = (%q, %q), want last-success / Server Last Client", source, headers.Get("X-Emby-Client"))
		}

		source, headers = callResolvePassthroughHeadersForServer(t, app.Identity, http.Header{}, "missing-token", stablePassthroughKey(upstreamB.URL, "B"))
		if source != "captured-latest" || headers.Get("X-Emby-Client") != "Token Client" {
			t.Fatalf("captured-latest resolution = (%q, %q), want captured-latest / Token Client", source, headers.Get("X-Emby-Client"))
		}

		empty := NewClientIdentityService()
		source, headers = callResolvePassthroughHeadersForServer(t, empty, http.Header{}, "", "missing-server")
		if source != "no-identity" || headers.Get("X-Emby-Client") != "" {
			t.Fatalf("no-identity resolution = (%q, %q), want no-identity / empty", source, headers.Get("X-Emby-Client"))
		}
	})
}

func TestLoginAllUsesServerLastSuccessHeadersForPassthrough(t *testing.T) {
	var seenClient string
	var mu sync.Mutex
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			mu.Lock()
			seenClient = r.Header.Get("X-Emby-Client")
			mu.Unlock()
			if seenClient != "Persisted Client" {
				http.Error(w, "missing persisted client", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-a", "User": map[string]any{"Id": "user-a"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	config := parityConfigWithUpstreams(fmt.Sprintf("  - name: \"A\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n    spoofClient: \"passthrough\"\n", upstream.URL))

	withTempAppPrepared(t, config, func(dir string) {
		writeCapturedHeadersFixture(t, dir, map[string]any{
			"version": 1,
			"lastSuccessByServer": map[string]any{
				stablePassthroughKey(upstream.URL, "A"): map[string]any{
					"headers":    passthroughFixtureHeaders("Persisted Client"),
					"capturedAt": "2026-03-23T00:00:00Z",
				},
			},
		})
	}, func(app *App, handler http.Handler, dir string) {
		waitForCondition(t, time.Second, func() bool {
			client := app.Upstream.GetClient(0)
			return client != nil && client.IsOnline()
		}, "passthrough upstream to come online from persisted last-success")
		mu.Lock()
		defer mu.Unlock()
		if seenClient != "Persisted Client" {
			t.Fatalf("login used client %q, want Persisted Client", seenClient)
		}
	})
}

func TestAuthenticateByNameRetriesOfflinePassthroughServersWithCapturedHeaders(t *testing.T) {
	var attempts atomic.Int32
	var seenClient atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			attempts.Add(1)
			client := r.Header.Get("X-Emby-Client")
			seenClient.Store(client)
			if client != "Recovered Client" {
				http.Error(w, "need live client identity", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-a", "User": map[string]any{"Id": "user-a"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	config := parityConfigWithUpstreams(fmt.Sprintf("  - name: \"A\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n    spoofClient: \"passthrough\"\n", upstream.URL))

	withTempAppPrepared(t, config, nil, func(app *App, handler http.Handler, dir string) {
		client := app.Upstream.GetClient(0)
		if client == nil {
			t.Fatalf("missing upstream client")
		}
		if client.IsOnline() {
			t.Fatalf("passthrough upstream should start offline before captured login")
		}

		loginTokenWithHeaders(t, handler, "secret", http.Header{
			"User-Agent":            []string{"Recovered Client UA"},
			"X-Emby-Client":         []string{"Recovered Client"},
			"X-Emby-Client-Version": []string{"8.0.0"},
			"X-Emby-Device-Name":    []string{"Recovered Device"},
			"X-Emby-Device-Id":      []string{"recovered-device"},
		})

		waitForCondition(t, time.Second, func() bool {
			return client.IsOnline()
		}, "offline passthrough upstream to retry after login capture")
		if attempts.Load() < 1 {
			t.Fatalf("authenticate attempts = %d, want retry after captured login", attempts.Load())
		}
		if got, _ := seenClient.Load().(string); got != "Recovered Client" {
			t.Fatalf("retry used client %q, want Recovered Client", got)
		}
	})
}

func TestPersistedLastSuccessRestoresAfterRestart(t *testing.T) {
	var attempts atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			attempts.Add(1)
			if r.Header.Get("X-Emby-Client") != "Restart Client" {
				http.Error(w, "need restart client identity", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-a", "User": map[string]any{"Id": "user-a"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	config := parityConfigWithUpstreams(fmt.Sprintf("  - name: \"A\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n    spoofClient: \"passthrough\"\n", upstream.URL))

	dir := prepareTempWorkspace(t, config, nil)
	chdirForTest(t, dir)

	app1, handler1 := newTestApp(t)
	client1 := app1.Upstream.GetClient(0)
	if client1 == nil {
		t.Fatalf("missing upstream client in first app")
	}
	if client1.IsOnline() {
		t.Fatalf("passthrough upstream should start offline before first captured login")
	}

	loginTokenWithHeaders(t, handler1, "secret", http.Header{
		"User-Agent":            []string{"Restart Client UA"},
		"X-Emby-Client":         []string{"Restart Client"},
		"X-Emby-Client-Version": []string{"9.0.0"},
		"X-Emby-Device-Name":    []string{"Restart Device"},
		"X-Emby-Device-Id":      []string{"restart-device"},
	})
	// Online 标志置位与捕获文件落盘不在同一瞬间：recordSuccessfulIdentity
	// 先让上游上线，随后才同步写 captured-headers.json，存在微小窗口。
	// 把文件存在性并入等待条件，避免在线后立刻 stat 偶发扑空。
	capturedPath := filepath.Join(dir, "data", "captured-headers.json")
	waitForCondition(t, 5*time.Second, func() bool {
		if !client1.IsOnline() {
			return false
		}
		_, err := os.Stat(capturedPath)
		return err == nil
	}, "first app passthrough upstream to come online and persist captured headers")
	_ = app1.Close()

	app2, _ := newTestApp(t)
	client2 := app2.Upstream.GetClient(0)
	if client2 == nil {
		t.Fatalf("missing upstream client in second app")
	}
	waitForCondition(t, time.Second, func() bool {
		return client2.IsOnline()
	}, "second app passthrough upstream to restore from persisted last-success")
	if attempts.Load() < 2 {
		t.Fatalf("authenticate attempts = %d, want restart to trigger persisted relogin", attempts.Load())
	}
}

func TestSaveLatestCaptured_RepairsCorruptFile(t *testing.T) {
	dir := t.TempDir()
	p := NewIdentityPersistence(dir, nil)

	path := filepath.Join(dir, "captured-headers.json")
	if err := os.WriteFile(path, []byte("{invalid json"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := p.SaveLatestCaptured(http.Header{"X-Emby-Client": {"Infuse"}}, ""); err != nil {
		t.Fatalf("save should repair a corrupt file, got %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot identityPersistenceSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatalf("repaired file is not valid JSON: %v", err)
	}
	if snapshot.LatestCaptured == nil || snapshot.LatestCaptured.Headers.Get("X-Emby-Client") != "Infuse" {
		t.Fatalf("repaired file lost the saved entry: %s", raw)
	}
}

func TestSaveLastSuccess_RepairsCorruptFile(t *testing.T) {
	dir := t.TempDir()
	p := NewIdentityPersistence(dir, nil)

	path := filepath.Join(dir, "captured-headers.json")
	if err := os.WriteFile(path, []byte("{invalid json"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := p.SaveLastSuccess("server-key", http.Header{"X-Emby-Client": {"Infuse"}}, ""); err != nil {
		t.Fatalf("save should repair a corrupt file, got %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot identityPersistenceSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatalf("repaired file is not valid JSON: %v", err)
	}
	if persisted, ok := snapshot.LastSuccessByServer["server-key"]; !ok || persisted.Headers.Get("X-Emby-Client") != "Infuse" {
		t.Fatalf("repaired file lost the saved entry: %s", raw)
	}
}

// TestCapturedHeadersFileUsesPrivateFileMode keeps the captured passthrough identity
// (which carries client Authorization headers) as restricted as tokens.json.
func TestCapturedHeadersFileUsesPrivateFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file mode assertion is not reliable on Windows")
	}
	dir := t.TempDir()
	p := NewIdentityPersistence(dir, nil)
	if err := p.SaveLastSuccess("server-key", http.Header{"X-Emby-Client": {"Infuse"}}, ""); err != nil {
		t.Fatalf("save last success: %v", err)
	}

	info, err := os.Stat(filepath.Join(dir, "captured-headers.json"))
	if err != nil {
		t.Fatalf("stat captured-headers.json: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("captured-headers.json mode = %#o, want 0600", got)
	}
}

// TestLoadTightensLegacyCapturedHeadersFile covers files written world-readable by
// older builds and by the install scripts, which chmod every file to 644.
func TestLoadTightensLegacyCapturedHeadersFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file mode assertion is not reliable on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "captured-headers.json")
	if err := os.WriteFile(path, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod fixture: %v", err)
	}

	p := NewIdentityPersistence(dir, nil)
	if _, err := p.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat captured-headers.json: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode after load = %#o, want 0600", got)
	}
}

// spoofedInfuseFixtureHeaders reproduces what the removed Infuse merge base
// used to stamp onto every passthrough identity it persisted.
func spoofedInfuseFixtureHeaders() map[string]any {
	return map[string]any{
		"User-Agent":            []string{"Infuse/7.7.1 (iPhone; iOS 17.4.1; Scale/3.00)"},
		"X-Emby-Client":         []string{"Infuse"},
		"X-Emby-Client-Version": []string{"7.7.1"},
		"X-Emby-Device-Name":    []string{"iPhone"},
		"X-Emby-Device-Id":      []string{"infuse-spoof-id"},
	}
}

// TestLoadDropsSpoofedInfuseEntries covers the one-shot cleanup of historical
// pollution: entries the removed Infuse merge base wrote with its fixed
// "infuse-spoof-id" device ID are dropped when captured-headers.json is
// loaded, so they can neither re-login nor count as a captured identity.
func TestLoadDropsSpoofedInfuseEntries(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-a", "User": map[string]any{"Id": "user-a"}})
	}))
	defer upstream.Close()

	config := parityConfigWithUpstreams(fmt.Sprintf("  - name: \"A\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n    spoofClient: \"passthrough\"\n", upstream.URL))
	serverKey := stablePassthroughKey(upstream.URL, "A")

	withTempAppPrepared(t, config, func(dir string) {
		writeCapturedHeadersFixture(t, dir, map[string]any{
			"version":        1,
			"latestCaptured": map[string]any{"headers": spoofedInfuseFixtureHeaders(), "capturedAt": "2026-03-23T00:00:00Z"},
			"lastSuccessByServer": map[string]any{
				serverKey: map[string]any{"headers": spoofedInfuseFixtureHeaders(), "capturedAt": "2026-03-23T00:00:01Z"},
			},
		})
	}, func(app *App, handler http.Handler, dir string) {
		if got := app.Identity.GetLatestCaptured().Get("X-Emby-Device-Id"); got == "infuse-spoof-id" {
			t.Fatalf("spoofed latestCaptured survived the load cleanup (device id %q)", got)
		}
		if got := app.Identity.GetLastSuccess(serverKey).Get("X-Emby-Device-Id"); got == "infuse-spoof-id" {
			t.Fatalf("spoofed last-success survived the load cleanup (device id %q)", got)
		}
		if app.Identity.HasCapturedHeaders(serverKey) {
			t.Fatalf("a spoofed persisted identity must not count as a captured identity")
		}
		client := app.Upstream.GetClient(0)
		if client == nil {
			t.Fatalf("missing upstream client")
		}
		if client.IsOnline() {
			t.Fatalf("upstream must not log in from a spoofed persisted identity")
		}
	})
}

// TestAPIKeyPassthroughDefersLoginUntilCapturedIdentity locks in decision 1A
// for the API-key path: without a captured client identity the upstream stays
// offline and no request leaves (LoginAll and the health check both skip), and
// once the admin seeds a real client identity the retry validates /Users/Me
// with exactly that identity and records it for every later request.
func TestAPIKeyPassthroughDefersLoginUntilCapturedIdentity(t *testing.T) {
	var attempts atomic.Int32
	var seenClient atomic.Value
	var seenDeviceID atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/Users/Me" {
			http.NotFound(w, r)
			return
		}
		attempts.Add(1)
		seenClient.Store(r.Header.Get("X-Emby-Client"))
		seenDeviceID.Store(r.Header.Get("X-Emby-Device-Id"))
		if r.Header.Get("X-Emby-Client") == "" {
			http.Error(w, "no client identity", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": "user-key", "Name": "Key User"})
	}))
	defer upstream.Close()

	config := parityConfigWithUpstreams(fmt.Sprintf("  - name: \"A\"\n    url: %q\n    apiKey: \"key-1\"\n    spoofClient: \"passthrough\"\n", upstream.URL))

	withTempAppPrepared(t, config, nil, func(app *App, handler http.Handler, dir string) {
		client := app.Upstream.GetClient(0)
		if client == nil {
			t.Fatalf("missing upstream client")
		}
		// The health loop runs every 100ms in this config: give it a few
		// cycles to prove it keeps skipping instead of logging in bare.
		time.Sleep(300 * time.Millisecond)
		if attempts.Load() != 0 {
			t.Fatalf("/Users/Me was called %d times before any capture, want 0", attempts.Load())
		}
		if client.IsOnline() {
			t.Fatalf("API-key passthrough upstream must stay offline before a captured identity exists")
		}

		loginTokenWithHeaders(t, handler, "secret", http.Header{
			"User-Agent":            []string{"Recovered Client UA"},
			"X-Emby-Client":         []string{"Recovered Client"},
			"X-Emby-Client-Version": []string{"8.0.0"},
			"X-Emby-Device-Name":    []string{"Recovered Device"},
			"X-Emby-Device-Id":      []string{"recovered-device"},
		})

		waitForCondition(t, 2*time.Second, func() bool {
			return client.IsOnline()
		}, "API-key passthrough upstream to validate with the captured identity")
		if got, _ := seenClient.Load().(string); got != "Recovered Client" {
			t.Fatalf("/Users/Me used client %q, want the captured Recovered Client", got)
		}
		if got, _ := seenDeviceID.Load().(string); got != "recovered-device" {
			t.Fatalf("/Users/Me used device id %q, want the captured recovered-device", got)
		}
	})
}
