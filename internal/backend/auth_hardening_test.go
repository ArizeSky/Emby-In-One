package backend

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
)

var hashedPasswordPattern = regexp.MustCompile(`(?i)^[0-9a-f]{32}:[0-9a-f]{128}$`)

func TestStartupHashesPlaintextPasswordBeforeFirstLogin(t *testing.T) {
	config := strings.Replace(parityConfigWithUpstreams(""), `password: "secret"`, `password: "plain-password"`, 1)
	dir := prepareTempWorkspace(t, config, nil)
	chdirForTest(t, dir)
	_, _ = newTestApp(t)

	raw, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("read config after startup: %v", err)
	}
	parsed, err := parseConfigYAML(string(raw))
	if err != nil {
		t.Fatalf("parse config after startup: %v", err)
	}
	if parsed.Admin.Password == "plain-password" {
		t.Fatalf("admin password remained plaintext after startup")
	}
	if !hashedPasswordPattern.MatchString(parsed.Admin.Password) {
		t.Fatalf("admin password = %q, want hashed salt:digest format", parsed.Admin.Password)
	}
}

func TestColonContainingPlaintextPasswordStillAuthenticates(t *testing.T) {
	config := strings.Replace(parityConfigWithUpstreams(""), `password: "secret"`, `password: "colon:plain"`, 1)
	withTempAppPrepared(t, config, nil, func(app *App, handler http.Handler, dir string) {
		token := loginToken(t, handler, "colon:plain")
		if token == "" {
			t.Fatalf("expected colon plaintext password login to succeed")
		}
	})
}

func TestTokenFileWrittenWith0600OnUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file mode assertion is not reliable on Windows")
	}
	withTempAppPrepared(t, parityConfigWithUpstreams(""), nil, func(app *App, handler http.Handler, dir string) {
		_ = loginToken(t, handler, "secret")
		info, err := os.Stat(filepath.Join(dir, "data", "tokens.json"))
		if err != nil {
			t.Fatalf("stat tokens.json: %v", err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("tokens.json mode = %#o, want 0600", got)
		}
	})
}

func TestConfigSaveUsesAtomicReplace(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("atomic replacement identity assertion is not reliable on Windows")
	}
	dir := prepareTempWorkspace(t, parityConfigWithUpstreams(""), nil)
	chdirForTest(t, dir)
	_, handler := newTestApp(t)
	configPath := filepath.Join(dir, "config.yaml")

	beforeInfo, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat before save: %v", err)
	}

	token := loginToken(t, handler, "secret")
	rr := doJSONRequest(t, handler, "PUT", "/admin/api/settings", map[string]any{"serverName": "Atomic Name"}, token)
	if rr.Code != 200 {
		t.Fatalf("settings update status = %d, body=%s", rr.Code, rr.Body.String())
	}

	afterInfo, err := os.Stat(configPath)
	if err != nil {
		t.Fatalf("stat after save: %v", err)
	}
	if os.SameFile(beforeInfo, afterInfo) {
		t.Fatalf("config file identity did not change; expected atomic replace")
	}

	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config after save: %v", err)
	}
	// yamlStr renders scalars single-quoted; this test never ran on Windows
	// (skipped above), so the stale double-quote expectation only surfaced
	// on the first Linux CI run.
	if !strings.Contains(string(raw), `name: 'Atomic Name'`) {
		t.Fatalf("updated config missing server name: %s", string(raw))
	}
}

// TestWebClientAdminLoginRejected locks in the web-player boundary: the web
// client marks its login with X-EIO-Web-Client, and admin credentials must be
// refused there (the token would carry admin rights) while the admin panel and
// real Emby clients, which never set the marker, keep logging in as before.
func TestWebClientAdminLoginRejected(t *testing.T) {
	withTempAppPrepared(t, parityConfigWithUpstreams(""), nil, func(app *App, handler http.Handler, dir string) {
		req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName",
			bytes.NewBufferString(`{"Username":"admin","Pw":"secret"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-EIO-Web-Client", "1")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("web client admin login status = %d, want 403; body=%s", rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "管理面板") {
			t.Fatalf("rejection should point at the admin panel: %s", rr.Body.String())
		}

		// The admin panel and real clients do not send the marker and still get in.
		if token := loginToken(t, handler, "secret"); token == "" {
			t.Fatalf("admin login without the web marker must still succeed")
		}
	})
}

// Concurrent issuance and revocation must persist consistently: save() used
// to snapshot under a read lock and write the file after releasing it, so a
// save that interleaved with another one could write a stale snapshot —
// losing newly issued tokens or resurrecting revoked ones across a restart.
func TestAuthManagerConcurrentSavesKeepEveryIssuedToken(t *testing.T) {
	withTempAppPrepared(t, parityConfigWithUpstreams(""), nil, func(app *App, handler http.Handler, dir string) {
		m := app.Auth
		const goroutines = 8
		const iterations = 25
		var mu sync.Mutex
		issued := make([]string, 0, goroutines*iterations)
		var failures int
		var wg sync.WaitGroup
		for g := 0; g < goroutines; g++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := 0; i < iterations; i++ {
					resp, ok, err := m.Authenticate("admin", "secret")
					if err != nil || !ok {
						mu.Lock()
						failures++
						mu.Unlock()
						continue
					}
					mu.Lock()
					issued = append(issued, resp["AccessToken"].(string))
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if failures != 0 {
			t.Fatalf("%d concurrent logins failed", failures)
		}

		raw, err := os.ReadFile(filepath.Join(dir, "data", "tokens.json"))
		if err != nil {
			t.Fatalf("read tokens.json: %v", err)
		}
		var persisted map[string]any
		if err := json.Unmarshal(raw, &persisted); err != nil {
			t.Fatalf("tokens.json is not valid JSON after concurrent saves: %v", err)
		}
		for _, token := range issued {
			if _, ok := persisted[token]; !ok {
				t.Fatalf("issued token was lost by a concurrent save: %d of %d persisted", len(persisted)-1, len(issued))
			}
		}

		// Revocation must persist as well: the file holds no tokens afterwards.
		m.RevokeAllTokens()
		raw, err = os.ReadFile(filepath.Join(dir, "data", "tokens.json"))
		if err != nil {
			t.Fatalf("reread tokens.json: %v", err)
		}
		persisted = nil
		if err := json.Unmarshal(raw, &persisted); err != nil {
			t.Fatalf("tokens.json is not valid JSON after revocation: %v", err)
		}
		for key := range persisted {
			if key != "_proxyUserId" {
				t.Fatalf("revoked token %q persisted after RevokeAllTokens", key)
			}
		}
	})
}
