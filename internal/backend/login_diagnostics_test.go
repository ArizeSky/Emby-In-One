package backend

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The local login diagnostic must classify a request, not quote it. These
// sentinels are the values the old "Login attempt" / "Login headers" lines
// printed; they are distinctive enough that a substring search over the whole
// log buffer is conclusive.
const (
	// loginDiagnosticsAccount is the administrator name the test config sets.
	loginDiagnosticsAccount = "diagnostic-admin-account"
	// loginDiagnosticsUnknown is an account name no local store knows.
	loginDiagnosticsUnknown = "diagnostic-unknown-account"
	// loginDiagnosticsUA is the client User-Agent the login presents.
	loginDiagnosticsUA = "DiagnosticSecretUA/9.9"
	// loginDiagnosticsClient is the client name the login presents.
	loginDiagnosticsClient = "DiagnosticSecretClient"
	// loginDiagnosticsDevice is the device name the login presents.
	loginDiagnosticsDevice = "DiagnosticSecretDevice"
	// loginDiagnosticsDeviceID is the DeviceId the login presents.
	loginDiagnosticsDeviceID = "diagnostic-secret-device-id"
)

func loginDiagnosticsSentinels() []string {
	return []string{
		loginDiagnosticsAccount,
		loginDiagnosticsUnknown,
		loginDiagnosticsUA,
		loginDiagnosticsClient,
		loginDiagnosticsDevice,
		loginDiagnosticsDeviceID,
	}
}

func loginDiagnosticsTestConfig() string {
	return "server:\n  port: 8096\n  name: \"Test Server\"\n  id: \"server-1\"\n\n" +
		"admin:\n  username: \"" + loginDiagnosticsAccount + "\"\n  password: \"secret\"\n\n" +
		"playback:\n  mode: \"proxy\"\n\n" +
		"timeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\n\n" +
		"dataDir: \"data\"\n\nproxies: []\nupstream: []\n"
}

// loginDiagnosticsRequest posts a credential check that carries a full client
// identity. Passing a header with no values removes it, so a subtest can model a
// client that presents no Emby identity at all.
func loginDiagnosticsRequest(t *testing.T, handler http.Handler, username, password string, headers http.Header) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"Username": username, "Pw": password})
	if err != nil {
		t.Fatalf("marshal login body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", loginDiagnosticsUA)
	req.Header.Set("X-Emby-Client", loginDiagnosticsClient)
	req.Header.Set("X-Emby-Client-Version", "9.9.9")
	req.Header.Set("X-Emby-Device-Name", loginDiagnosticsDevice)
	req.Header.Set("X-Emby-Device-Id", loginDiagnosticsDeviceID)
	for key, values := range headers {
		req.Header.Del(key)
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func findLoginDiagnosticsEntry(entries []LogEntry, prefix string) *LogEntry {
	for i := range entries {
		if strings.HasPrefix(entries[i].Message, prefix) {
			return &entries[i]
		}
	}
	return nil
}

// requireLoginDiagnosticEntry asserts that exactly the expected event was logged
// with every listed token present.
func requireLoginDiagnosticEntry(t *testing.T, entries []LogEntry, prefix string, wants ...string) *LogEntry {
	t.Helper()
	entry := findLoginDiagnosticsEntry(entries, prefix)
	if entry == nil {
		t.Fatalf("no %q log entry, got %#v", prefix, entries)
	}
	for _, want := range wants {
		if !strings.Contains(entry.Message, want) {
			t.Fatalf("%q entry is missing %q: %s", prefix, want, entry.Message)
		}
	}
	return entry
}

func assertLoginDiagnosticsCarryNoIdentity(t *testing.T, entries []LogEntry) {
	t.Helper()
	for _, entry := range entries {
		for _, sentinel := range loginDiagnosticsSentinels() {
			if strings.Contains(entry.Message, sentinel) {
				t.Fatalf("log entry repeats the client identity %q: %s", sentinel, entry.Message)
			}
		}
		// A login line may name its fields, but only with enum or presence
		// values: no field may be followed by a captured value.
		if !strings.Contains(entry.Message, "purpose="+loginDiagnosticPurpose) {
			continue
		}
		for _, forbidden := range []string{"user=", "client=", "device=", "UA=", "version="} {
			if strings.Contains(entry.Message, forbidden) {
				t.Fatalf("login entry still quotes a request value via %q: %s", forbidden, entry.Message)
			}
		}
	}
}

func TestLoginDiagnosticsClassifyWithoutQuotingIdentity(t *testing.T) {
	config := loginDiagnosticsTestConfig()

	t.Run("admin login from a real client", func(t *testing.T) {
		withTempAppPrepared(t, config, nil, func(app *App, handler http.Handler, _ string) {
			rr := loginDiagnosticsRequest(t, handler, loginDiagnosticsAccount, "secret", nil)
			if rr.Code != http.StatusOK {
				t.Fatalf("login status = %d, body=%s", rr.Code, rr.Body.String())
			}
			entries := app.Logger.Entries(0)
			assertLoginDiagnosticsCarryNoIdentity(t, entries)

			requireLoginDiagnosticEntry(t, entries, "Login attempt:",
				"purpose=local-login", "target=local-proxy", "source=client-identity")
			// The identity capture still happens and still writes the UA library;
			// only its log output is a pair of counters.
			requireLoginDiagnosticEntry(t, entries, "Admin identity captured:",
				"purpose=local-login", "target=local-ua-library", "source=client-identity",
				"records=1", "revision=1")
			requireLoginDiagnosticEntry(t, entries, "Login success:",
				"purpose=local-login", "target=local-proxy", "source=client-identity", "role=admin")

			if info := app.Identity.GetInfo(); info == nil || info.UserAgent != loginDiagnosticsUA {
				t.Fatalf("administrator identity capture changed: %#v", info)
			}
			recent := app.UALibrary.Snapshot().Recent
			if len(recent) != 1 || recent[0].Identity.DeviceID != loginDiagnosticsDeviceID {
				t.Fatalf("UA library no longer records the captured identity: %#v", recent)
			}
		})
	})

	t.Run("debug line reports presence only", func(t *testing.T) {
		withTempAppPrepared(t, config, nil, func(app *App, handler http.Handler, _ string) {
			if rr := loginDiagnosticsRequest(t, handler, loginDiagnosticsAccount, "secret", nil); rr.Code != http.StatusOK {
				t.Fatalf("login status = %d, body=%s", rr.Code, rr.Body.String())
			}
			entries := app.Logger.Entries(0)
			assertLoginDiagnosticsCarryNoIdentity(t, entries)
			requireLoginDiagnosticEntry(t, entries, "Login identity shape:",
				"ua=present", "client=present", "deviceId=present", "deviceName=present", "version=present")
		})
	})

	t.Run("failed login", func(t *testing.T) {
		withTempAppPrepared(t, config, nil, func(app *App, handler http.Handler, _ string) {
			rr := loginDiagnosticsRequest(t, handler, loginDiagnosticsUnknown, "wrong", nil)
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("login status = %d, body=%s", rr.Code, rr.Body.String())
			}
			entries := app.Logger.Entries(0)
			assertLoginDiagnosticsCarryNoIdentity(t, entries)
			entry := requireLoginDiagnosticEntry(t, entries, "Login failed:",
				"purpose=local-login", "target=local-proxy", "source=client-identity", "code=invalid-credentials")
			if entry.Level != "warn" {
				t.Fatalf("failed login level = %q, want warn", entry.Level)
			}
		})
	})

	t.Run("admin web client rejection", func(t *testing.T) {
		withTempAppPrepared(t, config, nil, func(app *App, handler http.Handler, _ string) {
			rr := loginDiagnosticsRequest(t, handler, loginDiagnosticsAccount, "secret",
				http.Header{"X-EIO-Web-Client": {"1"}})
			if rr.Code != http.StatusForbidden {
				t.Fatalf("login status = %d, body=%s", rr.Code, rr.Body.String())
			}
			entries := app.Logger.Entries(0)
			assertLoginDiagnosticsCarryNoIdentity(t, entries)
			requireLoginDiagnosticEntry(t, entries, "Login rejected:",
				"purpose=local-login", "target=local-proxy", "source=web-client", "code=admin-web-client-forbidden")
			if findLoginDiagnosticsEntry(entries, "Admin identity captured:") != nil {
				t.Fatalf("a rejected login must not capture an identity: %#v", entries)
			}
		})
	})

	t.Run("login without any client identity", func(t *testing.T) {
		withTempAppPrepared(t, config, nil, func(app *App, handler http.Handler, _ string) {
			rr := loginDiagnosticsRequest(t, handler, loginDiagnosticsAccount, "secret", http.Header{
				"User-Agent":            nil,
				"X-Emby-Client":         nil,
				"X-Emby-Client-Version": nil,
				"X-Emby-Device-Name":    nil,
				"X-Emby-Device-Id":      nil,
			})
			if rr.Code != http.StatusOK {
				t.Fatalf("login status = %d, body=%s", rr.Code, rr.Body.String())
			}
			entries := app.Logger.Entries(0)
			assertLoginDiagnosticsCarryNoIdentity(t, entries)
			requireLoginDiagnosticEntry(t, entries, "Login attempt:",
				"purpose=local-login", "target=local-proxy", "source=no-identity")
			requireLoginDiagnosticEntry(t, entries, "Login success:",
				"purpose=local-login", "source=no-identity", "role=admin")
			requireLoginDiagnosticEntry(t, entries, "Login identity shape:",
				"ua=absent", "client=absent", "deviceId=absent", "deviceName=absent", "version=absent")
			if findLoginDiagnosticsEntry(entries, "Admin identity captured:") != nil {
				t.Fatalf("a request with no client identity must not be captured: %#v", entries)
			}
		})
	})

	t.Run("rate limited login", func(t *testing.T) {
		withTempAppPrepared(t, config, nil, func(app *App, handler http.Handler, _ string) {
			for i := 0; i < loginMaxFailures; i++ {
				if rr := loginDiagnosticsRequest(t, handler, loginDiagnosticsUnknown, "wrong", nil); rr.Code != http.StatusUnauthorized {
					t.Fatalf("failure %d status = %d, body=%s", i, rr.Code, rr.Body.String())
				}
			}
			rr := loginDiagnosticsRequest(t, handler, loginDiagnosticsUnknown, "wrong", nil)
			if rr.Code != http.StatusTooManyRequests {
				t.Fatalf("rate limited status = %d, body=%s", rr.Code, rr.Body.String())
			}
			entries := app.Logger.Entries(0)
			assertLoginDiagnosticsCarryNoIdentity(t, entries)
			requireLoginDiagnosticEntry(t, entries, "Login rate limited:",
				"purpose=local-login", "target=local-proxy", "code=login-rate-limited")
		})
	})
}
