package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const captureRegressionUA = "Emby/3.4.0 Android"

func captureRegressionHeaders() http.Header {
	headers := http.Header{}
	headers.Set("User-Agent", captureRegressionUA)
	headers.Set("X-Emby-Client", "Emby for Android")
	headers.Set("X-Emby-Client-Version", "3.4.0")
	headers.Set("X-Emby-Device-Name", "Android")
	headers.Set("X-Emby-Device-Id", "capture-regression-device")
	headers.Set("X-Emby-Authorization", compoundAuthHeader(identitySentinelUserID, identitySentinelToken,
		"Emby for Android", "Android", "capture-regression-device", "3.4.0"))
	headers.Set("X-Emby-Token", identitySentinelToken)
	return headers
}

func loginBrowserToken(t *testing.T, handler http.Handler) string {
	t.Helper()
	rr := doJSONRequest(t, handler, http.MethodPost, "/Users/AuthenticateByName",
		map[string]any{"Username": "admin", "Pw": "secret"}, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("browser login: %d %s", rr.Code, rr.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	token, _ := payload["AccessToken"].(string)
	if token == "" {
		t.Fatal("browser login returned no token")
	}
	return token
}

func newCaptureRegressionUpstream(t *testing.T, status func() int) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	hits := &atomic.Int32{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Users/AuthenticateByName" && r.URL.Path != "/Users/Me" {
			http.NotFound(w, r)
			return
		}
		hits.Add(1)
		if r.Header.Get("User-Agent") != captureRegressionUA || r.Header.Get("X-Emby-Device-Id") != "capture-regression-device" {
			http.Error(w, "missing captured identity", http.StatusBadRequest)
			return
		}
		for _, values := range r.Header {
			if strings.Contains(strings.Join(values, " "), identitySentinelToken) || strings.Contains(strings.Join(values, " "), identitySentinelUserID) {
				http.Error(w, "local credential leaked", http.StatusBadRequest)
				return
			}
		}
		if code := status(); code != http.StatusOK {
			http.Error(w, "upstream rejected authentication", code)
			return
		}
		payload := map[string]any{"Id": "upstream-user"}
		if r.URL.Path == "/Users/AuthenticateByName" {
			payload = map[string]any{"AccessToken": "upstream-token", "User": payload}
		}
		_ = json.NewEncoder(w).Encode(payload)
	}))
	t.Cleanup(upstream.Close)
	return upstream, hits
}

func captureRegressionDraft(url, authType string) map[string]any {
	draft := map[string]any{"name": "Captured", "url": url, "spoofClient": "passthrough", "playbackMode": "proxy"}
	if authType == "apiKey" {
		draft["apiKey"] = "upstream-api-key"
	} else {
		draft["username"], draft["password"] = "upstream-user", "upstream-password"
	}
	return draft
}

func TestPassthroughCreateUsesPreviouslyCapturedClient(t *testing.T) {
	for _, authType := range []string{"password", "apiKey"} {
		for _, separateToken := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/separate-token=%v", authType, separateToken), func(t *testing.T) {
				upstream, hits := newCaptureRegressionUpstream(t, func() int { return http.StatusOK })
				withTempApp(t, func(app *App, handler http.Handler) {
					token := loginTokenWithHeaders(t, handler, "secret", captureRegressionHeaders())
					if separateToken {
						token = loginBrowserToken(t, handler)
					}
					if info := app.Identity.GetInfo(); info == nil || info.UserAgent != captureRegressionUA {
						t.Fatalf("captured client info = %#v", info)
					}
					rr := doJSONRequest(t, handler, http.MethodPost, "/admin/api/upstream",
						captureRegressionDraft(upstream.URL, authType), token)
					if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "warning") {
						t.Fatalf("create must log in without deferral: %d %s", rr.Code, rr.Body.String())
					}
					waitForCondition(t, time.Second, func() bool {
						return app.Upstream.GetClient(0).IsOnline()
					}, "new upstream to log in using the previously captured identity")
					if hits.Load() == 0 {
						t.Fatal("upstream never received authentication")
					}
				})
			})
		}
	}
}

func TestPassthroughCaptureSurvivesRestartBeforeUpstreamLogin(t *testing.T) {
	upstream, _ := newCaptureRegressionUpstream(t, func() int { return http.StatusOK })
	withTempApp(t, func(app *App, handler http.Handler) {
		token := loginTokenWithHeaders(t, handler, "secret", captureRegressionHeaders())
		dataDir := app.ConfigStore.Snapshot().DataDir
		raw, err := os.ReadFile(filepath.Join(dataDir, "captured-headers.json"))
		if err != nil {
			t.Fatalf("capture must persist before any upstream login: %v", err)
		}
		if strings.Contains(string(raw), identitySentinelToken) || strings.Contains(string(raw), identitySentinelUserID) {
			t.Fatal("capture file contains local credentials")
		}
		if err := app.Close(); err != nil {
			t.Fatal(err)
		}
		restarted, handler2 := newTestApp(t)
		if restarted.Auth.ValidateToken(token) == nil {
			t.Fatal("local login token was not restored")
		}
		if !restarted.Identity.HasCapturedHeaders("new-server") {
			t.Fatal("captured identity was not restored")
		}
		rr := doJSONRequest(t, handler2, http.MethodPost, "/admin/api/upstream",
			captureRegressionDraft(upstream.URL, "password"), token)
		if rr.Code != http.StatusOK || strings.Contains(rr.Body.String(), "warning") {
			t.Fatalf("create after restart: %d %s", rr.Code, rr.Body.String())
		}
		waitForCondition(t, time.Second, func() bool {
			return restarted.Upstream.GetClient(0).IsOnline()
		}, "restored identity to authenticate without another client login")
	})
}

func TestPassthroughValidationAcceptsLiveIdentityWithoutStoredCapture(t *testing.T) {
	upstream, hits := newCaptureRegressionUpstream(t, func() int { return http.StatusOK })
	withTempApp(t, func(app *App, handler http.Handler) {
		cfg := app.ConfigStore.Snapshot()
		draft := UpstreamConfig{Name: "Live", URL: upstream.URL, Username: "u", Password: "p", SpoofClient: "passthrough"}
		normalizeUpstream(&draft, 0, &cfg)
		result, err := app.validateUpstreamConnectivity(cfg, draft, 0, &RequestContext{Headers: captureRegressionHeaders()})
		if err != nil || !result.Online || result.Warning != "" || hits.Load() != 1 {
			t.Fatalf("live validation = %#v, err=%v, auth calls=%d", result, err, hits.Load())
		}
	})
}

func TestPassthroughAuthRejectionIsNotMissingIdentity(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		for _, authType := range []string{"password", "apiKey"} {
			t.Run(fmt.Sprintf("%d/%s", code, authType), func(t *testing.T) {
				upstream, hits := newCaptureRegressionUpstream(t, func() int { return code })
				withTempApp(t, func(app *App, handler http.Handler) {
					loginTokenWithHeaders(t, handler, "secret", captureRegressionHeaders())
					token := loginBrowserToken(t, handler)
					rr := doJSONRequest(t, handler, http.MethodPost, "/admin/api/upstream",
						captureRegressionDraft(upstream.URL, authType), token)
					if rr.Code != http.StatusBadGateway || !strings.Contains(rr.Body.String(), fmt.Sprint(code)) {
						t.Fatalf("must surface upstream rejection: %d %s", rr.Code, rr.Body.String())
					}
					if strings.Contains(rr.Body.String(), passthroughDeferredWarning) || hits.Load() != 1 {
						t.Fatalf("rejection misreported or login skipped: %s, calls=%d", rr.Body.String(), hits.Load())
					}
					if len(app.ConfigStore.Snapshot().Upstream) != 0 {
						t.Fatal("rejected upstream must not be committed as a deferred login")
					}
				})
			})
		}
	}
}

func TestPassthroughCapturedIdentityAllowsBackgroundRetry(t *testing.T) {
	for _, recovery := range []string{"health-check", "manual-reconnect"} {
		t.Run(recovery, func(t *testing.T) {
			var accept atomic.Bool
			upstream, _ := newCaptureRegressionUpstream(t, func() int {
				if accept.Load() {
					return http.StatusOK
				}
				return http.StatusUnauthorized
			})
			withTempApp(t, func(app *App, handler http.Handler) {
				token := loginBrowserToken(t, handler)
				rr := doJSONRequest(t, handler, http.MethodPost, "/admin/api/upstream",
					captureRegressionDraft(upstream.URL, "password"), token)
				if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), passthroughDeferredWarning) {
					t.Fatalf("missing-identity save: %d %s", rr.Code, rr.Body.String())
				}
				app.Upstream.stopHealthChecks()
				loginTokenWithHeaders(t, handler, "secret", captureRegressionHeaders())
				client := app.Upstream.GetClient(0)
				waitForCondition(t, time.Second, func() bool {
					return strings.HasPrefix(client.snapshot().LastError, "401 ")
				}, "first captured login to fail")
				accept.Store(true)
				if recovery == "health-check" {
					app.Upstream.runHealthCheckCycle(context.Background())
				} else {
					app.Upstream.Reconnect(0)
				}
				if !client.IsOnline() {
					t.Fatalf("%s did not reuse captured identity: %s", recovery, client.snapshot().LastError)
				}
			})
		})
	}
}

func TestClientCapturePublishesAndPersistsBeforeNotification(t *testing.T) {
	persistence := NewIdentityPersistence(t.TempDir(), nil)
	svc := newClientIdentityService(persistence)
	notified := false
	svc.RegisterCaptureListener(func(token string, headers http.Header) {
		notified = true
		resolved := svc.ResolvePassthroughHeadersForServer(nil, "", "new-server")
		if !svc.HasCapturedHeaders("new-server") || resolved.Headers.Get("User-Agent") != captureRegressionUA {
			t.Error("capture was not published before notifying listeners")
		}
		snapshot, err := persistence.Load()
		if err != nil || snapshot.LatestCaptured == nil || snapshot.LatestCaptured.Headers.Get("User-Agent") != captureRegressionUA {
			t.Errorf("capture was not persisted before notification: %#v, %v", snapshot, err)
		}
	})
	svc.SetCaptured("client-token", captureRegressionHeaders())
	if !notified {
		t.Fatal("capture listener not notified")
	}
}

func TestClientCaptureDoesNotReplaceRealIdentityWithBrowserOrInvalidHeaders(t *testing.T) {
	for name, headers := range map[string]http.Header{
		"browser":      {"User-Agent": {"Mozilla/5.0 Chrome/126.0"}},
		"bearer":       {"User-Agent": {"Mozilla/5.0"}, "Authorization": {"Bearer local-token"}},
		"malformed":    {"User-Agent": {"Mozilla/5.0"}, "X-Emby-Authorization": {`Emby Client="unterminated`}},
		"legacy-spoof": {"User-Agent": {"Infuse/1.0"}, "X-Emby-Device-Id": {"infuse-spoof-id"}},
		"empty":        {},
	} {
		t.Run(name, func(t *testing.T) {
			svc := NewClientIdentityService()
			svc.SetCaptured("real-token", captureRegressionHeaders())
			notifications := 0
			svc.RegisterCaptureListener(func(string, http.Header) { notifications++ })
			svc.SetCaptured("invalid-token", headers)
			svc.SaveLatestCapturedHeaders(headers)
			if info := svc.GetInfo(); info == nil || info.UserAgent != captureRegressionUA {
				t.Fatalf("real client info replaced: %#v", info)
			}
			if got := svc.GetLatestCaptured().Get("User-Agent"); got != captureRegressionUA {
				t.Fatalf("global identity replaced: %q", got)
			}
			if len(svc.GetCaptured("invalid-token")) != 0 || notifications != 0 {
				t.Fatal("invalid identity was stored or triggered a retry")
			}
		})
	}
}
