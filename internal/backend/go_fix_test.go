package backend

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAdminUpstreamCreateRejectsOfflineDraft(t *testing.T) {
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer broken.Close()

	withTempApp(t, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")

		rr := doJSONRequest(t, handler, http.MethodPost, "/admin/api/upstream", map[string]any{
			"name":     "Broken",
			"url":      broken.URL,
			"username": "u1",
			"password": "p1",
		}, token)
		if rr.Code == http.StatusOK {
			t.Fatalf("create upstream unexpectedly succeeded: %s", rr.Body.String())
		}
		if got := len(app.ConfigStore.Snapshot().Upstream); got != 0 {
			t.Fatalf("upstream config count = %d, want 0", got)
		}
		if got := len(app.Upstream.Clients()); got != 0 {
			t.Fatalf("runtime upstream count = %d, want 0", got)
		}
	})
}

func TestAdminUpstreamUpdateRejectsOfflineDraftAndKeepsExistingClient(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-a", "User": map[string]any{"Id": "user-a"}})
		case r.Method == http.MethodGet && r.URL.Path == "/System/Info":
			_ = json.NewEncoder(w).Encode(map[string]any{"Version": "ok"})
		case r.Method == http.MethodGet && r.URL.Path == "/Users/user-a/Views":
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": []map[string]any{{"Id": "view-a", "Name": "Library A"}}, "TotalRecordCount": 1})
		default:
			http.NotFound(w, r)
		}
	}))
	defer good.Close()

	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer broken.Close()

	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test Server\"\n  id: \"server-1\"\n\nadmin:\n  username: \"admin\"\n  password: \"secret\"\n\nplayback:\n  mode: \"proxy\"\n\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\n\nproxies: []\nupstream:\n  - name: \"A\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n", good.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		before := app.ConfigStore.Snapshot().Upstream[0]

		rr := doJSONRequest(t, handler, http.MethodPut, "/admin/api/upstream/0", map[string]any{
			"name":     "Broken",
			"url":      broken.URL,
			"username": "u2",
			"password": "p2",
		}, token)
		if rr.Code == http.StatusOK {
			t.Fatalf("update upstream unexpectedly succeeded: %s", rr.Body.String())
		}

		after := app.ConfigStore.Snapshot().Upstream[0]
		if after.URL != before.URL || after.Username != before.Username || after.Name != before.Name {
			t.Fatalf("upstream config mutated after failed update: before=%#v after=%#v", before, after)
		}
		clients := app.Upstream.Clients()
		if len(clients) != 1 || !clients[0].Online || clients[0].BaseURL != strings.TrimRight(good.URL, "/") {
			t.Fatalf("runtime client mutated after failed update: %#v", clients)
		}
	})
}

func TestAdminSettingsUpdateKeepsUpstreamOnlineAfterCommit(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-a", "User": map[string]any{"Id": "user-a"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test Server\"\n  id: \"server-1\"\n\nadmin:\n  username: \"admin\"\n  password: \"secret\"\n\nplayback:\n  mode: \"proxy\"\n\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\n\nproxies: []\nupstream:\n  - name: \"A\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n", upstream.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")

		statusBefore := doJSONRequest(t, handler, http.MethodGet, "/admin/api/status", nil, token)
		if statusBefore.Code != http.StatusOK {
			t.Fatalf("status before = %d body=%s", statusBefore.Code, statusBefore.Body.String())
		}
		var before map[string]any
		if err := json.Unmarshal(statusBefore.Body.Bytes(), &before); err != nil {
			t.Fatalf("unmarshal status before: %v", err)
		}
		if before["upstreamOnline"].(float64) != 1 {
			t.Fatalf("upstream online before = %#v", before)
		}

		settingsRR := doJSONRequest(t, handler, http.MethodPut, "/admin/api/settings", map[string]any{
			"serverName": "Renamed Server",
		}, token)
		if settingsRR.Code != http.StatusOK {
			t.Fatalf("settings update status = %d body=%s", settingsRR.Code, settingsRR.Body.String())
		}

		statusAfter := doJSONRequest(t, handler, http.MethodGet, "/admin/api/status", nil, token)
		if statusAfter.Code != http.StatusOK {
			t.Fatalf("status after = %d body=%s", statusAfter.Code, statusAfter.Body.String())
		}
		var after map[string]any
		if err := json.Unmarshal(statusAfter.Body.Bytes(), &after); err != nil {
			t.Fatalf("unmarshal status after: %v", err)
		}
		if after["upstreamOnline"].(float64) != 1 {
			t.Fatalf("upstream online after = %#v", after)
		}
	})
}

func TestLogsDownloadSupportsQueryTokenAuth(t *testing.T) {
	withTempApp(t, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		app.Logger.Infof("log line for query token test")

		req := httptest.NewRequest(http.MethodGet, "/admin/api/logs/download?api_key="+token, nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("download logs status = %d body=%s", rr.Code, rr.Body.String())
		}
		if ct := rr.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
			t.Fatalf("content-type = %q, want text/plain; charset=utf-8", ct)
		}
		if disp := rr.Header().Get("Content-Disposition"); !strings.Contains(disp, "emby-in-one.log") {
			t.Fatalf("content-disposition = %q, want attachment filename", disp)
		}
		if !strings.Contains(rr.Body.String(), "log line for query token test") {
			t.Fatalf("downloaded logs missing expected line: %q", rr.Body.String())
		}
	})
}

func TestLogsDeleteClearsBufferAndFile(t *testing.T) {
	withTempApp(t, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		app.Logger.Infof("line before clear")

		before := doJSONRequest(t, handler, http.MethodGet, "/admin/api/logs?limit=10", nil, token)
		if before.Code != http.StatusOK {
			t.Fatalf("logs before clear status = %d body=%s", before.Code, before.Body.String())
		}
		if !strings.Contains(before.Body.String(), "line before clear") {
			t.Fatalf("logs before clear missing expected line: %s", before.Body.String())
		}

		clearRR := doJSONRequest(t, handler, http.MethodDelete, "/admin/api/logs", nil, token)
		if clearRR.Code != http.StatusOK {
			t.Fatalf("clear logs status = %d body=%s", clearRR.Code, clearRR.Body.String())
		}

		after := doJSONRequest(t, handler, http.MethodGet, "/admin/api/logs?limit=10", nil, token)
		if after.Code != http.StatusOK {
			t.Fatalf("logs after clear status = %d body=%s", after.Code, after.Body.String())
		}
		if strings.Contains(after.Body.String(), "line before clear") {
			t.Fatalf("logs after clear still contain old line: %s", after.Body.String())
		}
	})
}

func TestAdminLogsDownloadDoesNotLeakNonAdminUser(t *testing.T) {
	withTempApp(t, func(app *App, handler http.Handler) {
		app.Logger.Infof("log line for non-admin test")
		if app.UserStore == nil {
			t.Fatalf("UserStore unexpectedly nil")
		}
		user, err := app.UserStore.Create("demo", "pass123", nil)
		if err != nil {
			t.Fatalf("create user: %v", err)
		}
		response, _, err := app.Auth.AuthenticateUser(user)
		if err != nil {
			t.Fatalf("authenticate user: %v", err)
		}
		token, _ := response["AccessToken"].(string)
		if token == "" {
			t.Fatalf("missing access token in response: %#v", response)
		}
		req := httptest.NewRequest(http.MethodGet, "/admin/api/logs/download?api_key="+token, nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 body=%s", rr.Code, rr.Body.String())
		}
	})
}

func TestStatusIncludesUpstreamURLField(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-a", "User": map[string]any{"Id": "user-a"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test Server\"\n  id: \"server-1\"\n\nadmin:\n  username: \"admin\"\n  password: \"secret\"\n\nplayback:\n  mode: \"proxy\"\n\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\n\nproxies: []\nupstream:\n  - name: \"My Upstream\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n", upstream.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		rr := doJSONRequest(t, handler, http.MethodGet, "/admin/api/upstream", nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
		}
		var list []map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if len(list) != 1 {
			t.Fatalf("upstream list len = %d, want 1 body=%s", len(list), rr.Body.String())
		}
		if _, ok := list[0]["url"]; !ok {
			t.Fatalf("response missing url field: %#v", list[0])
		}
		if _, ok := list[0]["host"]; ok {
			t.Fatalf("response should not expose legacy host field: %#v", list[0])
		}
		if got := list[0]["url"]; got != upstream.URL {
			t.Fatalf("url field = %#v, want %q", got, upstream.URL)
		}
	})
}

func TestAdminStatusExposesStoredUpstreamOptions(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-a", "User": map[string]any{"Id": "user-a"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	config := fmt.Sprintf(`server:
  port: 8096
  name: "Test Server"
  id: "server-1"

admin:
  username: "admin"
  password: "secret"

playback:
  mode: "proxy"

timeouts:
  api: 30000
  global: 15000
  login: 10000
  healthCheck: 10000
  healthInterval: 60000

proxies:
  - id: "px1"
    name: "Proxy 1"
    url: "http://proxy.test:8080"

upstream:
  - name: "A"
    url: %q
    username: "u1"
    password: "p1"
    playbackMode: "redirect"
    spoofClient: "passthrough"
    followRedirects: false
    proxyId: "px1"
    priorityMetadata: true
`, upstream.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		rr := doJSONRequest(t, handler, http.MethodGet, "/admin/api/upstream", nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
		}
		var list []map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		if len(list) != 1 {
			t.Fatalf("upstream list len = %d, want 1 body=%s", len(list), rr.Body.String())
		}
		if list[0]["playbackMode"] != "redirect" {
			t.Fatalf("playbackMode = %#v, want redirect", list[0]["playbackMode"])
		}
		if list[0]["followRedirects"] != false || list[0]["spoofClient"] != "passthrough" {
			t.Fatalf("followRedirects/spoofClient = %#v", list[0])
		}
		if list[0]["proxyId"] != "px1" || list[0]["priorityMetadata"] != true {
			t.Fatalf("proxyId/priorityMetadata = %#v", list[0])
		}
	})
}

func TestAdminUpstreamCreateAllowsPassthroughWithoutCapturedHeaders(t *testing.T) {
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing live client identity", http.StatusUnauthorized)
	}))
	defer broken.Close()

	withTempApp(t, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		rr := doJSONRequest(t, handler, http.MethodPost, "/admin/api/upstream", map[string]any{
			"name":        "Passthrough",
			"url":         broken.URL,
			"username":    "u1",
			"password":    "p1",
			"spoofClient": "passthrough",
		}, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("create passthrough status = %d, want 200 body=%s", rr.Code, rr.Body.String())
		}
		if got := len(app.ConfigStore.Snapshot().Upstream); got != 1 {
			t.Fatalf("upstream config count = %d, want 1", got)
		}
		var payload map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal create passthrough response: %v", err)
		}
		if online, _ := payload["online"].(bool); online {
			t.Fatalf("passthrough create unexpectedly reported online: %#v", payload)
		}
		if _, ok := payload["warning"]; !ok {
			t.Fatalf("passthrough create response missing warning: %#v", payload)
		}
	})
}

func TestAdminUpstreamCreatePassthroughWithoutCapturedHeadersDoesNotCallUpstream(t *testing.T) {
	var authHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName" {
			authHits.Add(1)
			http.Error(w, "missing live client identity", http.StatusUnauthorized)
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	withTempApp(t, func(app *App, handler http.Handler) {
		app.Identity.Clear()
		token := loginToken(t, handler, "secret")
		app.Identity.Clear()
		rr := doJSONRequest(t, handler, http.MethodPost, "/admin/api/upstream", map[string]any{
			"name":        "Passthrough",
			"url":         upstream.URL,
			"username":    "u1",
			"password":    "p1",
			"spoofClient": "passthrough",
		}, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("create passthrough status = %d, want 200 body=%s", rr.Code, rr.Body.String())
		}
		if authHits.Load() != 0 {
			t.Fatalf("upstream AuthenticateByName hits = %d, want 0", authHits.Load())
		}
	})
}

func TestAdminUpstreamCreatePassthroughIgnoresBrowserOnlyHeaders(t *testing.T) {
	var authHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte{0x9d, 0x00, 0x00})
	}))
	defer upstream.Close()

	withTempApp(t, func(app *App, handler http.Handler) {
		loginReq := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", bytes.NewBufferString(`{"Username":"admin","Pw":"secret"}`))
		loginReq.Header.Set("Content-Type", "application/json")
		loginReq.Header.Set("User-Agent", "Mozilla/5.0")
		loginReq.Header.Set("Accept-Encoding", "br")
		loginRR := httptest.NewRecorder()
		handler.ServeHTTP(loginRR, loginReq)
		if loginRR.Code != http.StatusOK {
			t.Fatalf("browser-style login status = %d body=%s", loginRR.Code, loginRR.Body.String())
		}
		var loginPayload map[string]any
		if err := json.Unmarshal(loginRR.Body.Bytes(), &loginPayload); err != nil {
			t.Fatalf("unmarshal browser-style login response: %v", err)
		}
		token, _ := loginPayload["AccessToken"].(string)
		if token == "" {
			t.Fatalf("browser-style login missing token: %#v", loginPayload)
		}

		payload, err := json.Marshal(map[string]any{
			"name":        "Browser Passthrough",
			"url":         upstream.URL,
			"username":    "u1",
			"password":    "p1",
			"spoofClient": "passthrough",
		})
		if err != nil {
			t.Fatalf("marshal request: %v", err)
		}
		req := httptest.NewRequest(http.MethodPost, "/admin/api/upstream", bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Emby-Token", token)
		req.Header.Set("User-Agent", "Mozilla/5.0")
		req.Header.Set("Accept-Encoding", "br")

		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("create passthrough with browser headers status = %d, want 200 body=%s", rr.Code, rr.Body.String())
		}
		if got := len(app.ConfigStore.Snapshot().Upstream); got != 1 {
			t.Fatalf("upstream config count = %d, want 1", got)
		}
		var response map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatalf("unmarshal create response: %v", err)
		}
		if online, _ := response["online"].(bool); online {
			t.Fatalf("browser-only passthrough unexpectedly reported online: %#v", response)
		}
		if _, ok := response["warning"]; !ok {
			t.Fatalf("browser-only passthrough response missing warning: %#v", response)
		}
		if authHits.Load() != 0 {
			t.Fatalf("browser-only passthrough should not hit upstream before real client capture, got %d", authHits.Load())
		}
	})
}

func TestAdminUpstreamUpdateAllowsPassthroughWithoutCapturedHeaders(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-a", "User": map[string]any{"Id": "user-a"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer good.Close()
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing live client identity", http.StatusUnauthorized)
	}))
	defer broken.Close()

	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test Server\"\n  id: \"server-1\"\n\nadmin:\n  username: \"admin\"\n  password: \"secret\"\n\nplayback:\n  mode: \"proxy\"\n\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\n\nproxies: []\nupstream:\n  - name: \"A\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n", good.URL)
	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		rr := doJSONRequest(t, handler, http.MethodPut, "/admin/api/upstream/0", map[string]any{
			"name":        "Recovered Later",
			"url":         broken.URL,
			"username":    "u2",
			"password":    "p2",
			"spoofClient": "passthrough",
		}, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("update passthrough status = %d, want 200 body=%s", rr.Code, rr.Body.String())
		}
		updated := app.ConfigStore.Snapshot().Upstream[0]
		if updated.URL != broken.URL || updated.SpoofClient != "passthrough" {
			t.Fatalf("passthrough update was not saved: %#v", updated)
		}
		var payload map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal update passthrough response: %v", err)
		}
		if online, _ := payload["online"].(bool); online {
			t.Fatalf("passthrough update unexpectedly reported online: %#v", payload)
		}
		if _, ok := payload["warning"]; !ok {
			t.Fatalf("passthrough update response missing warning: %#v", payload)
		}
	})
}

func TestPassthroughReconnectWithoutCapturedHeadersDoesNotCallUpstream(t *testing.T) {
	var authHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName" {
			authHits.Add(1)
			http.Error(w, "missing live client identity", http.StatusUnauthorized)
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test Server\"\n  id: \"server-1\"\n\nadmin:\n  username: \"admin\"\n  password: \"secret\"\n\nplayback:\n  mode: \"proxy\"\n\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\n\nproxies: []\nupstream:\n  - name: \"Passthrough\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n    spoofClient: \"passthrough\"\n", upstream.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		app.Identity.Clear()
		client := app.Upstream.Reconnect(0)
		if client == nil {
			t.Fatal("Reconnect returned nil client")
		}
		if authHits.Load() != 0 {
			t.Fatalf("upstream AuthenticateByName hits = %d, want 0", authHits.Load())
		}
		if client.Online {
			t.Fatalf("client unexpectedly online after reconnect without identity: %#v", client)
		}
	})
}

func TestPassthroughAuthErrorRecoveryWithoutCapturedHeadersDoesNotCallUpstream(t *testing.T) {
	var authHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			authHits.Add(1)
			http.Error(w, "missing live client identity", http.StatusUnauthorized)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test Server\"\n  id: \"server-1\"\n\nadmin:\n  username: \"admin\"\n  password: \"secret\"\n\nplayback:\n  mode: \"proxy\"\n\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\n\nproxies: []\nupstream:\n  - name: \"Passthrough\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n    spoofClient: \"passthrough\"\n", upstream.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		app.Identity.Clear()
		client := app.Upstream.GetClient(0)
		if client == nil {
			t.Fatal("missing upstream client")
		}
		app.Upstream.handleUpstreamAuthError(client)
		if authHits.Load() != 0 {
			t.Fatalf("upstream AuthenticateByName hits = %d, want 0", authHits.Load())
		}
	})
}

func TestAdminUpstreamUpdatePassthroughWithoutCapturedHeadersDoesNotCallUpstream(t *testing.T) {
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-a", "User": map[string]any{"Id": "user-a"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer good.Close()

	var authHits atomic.Int32
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName" {
			authHits.Add(1)
			http.Error(w, "missing live client identity", http.StatusUnauthorized)
			return
		}
		http.NotFound(w, r)
	}))
	defer blocked.Close()

	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test Server\"\n  id: \"server-1\"\n\nadmin:\n  username: \"admin\"\n  password: \"secret\"\n\nplayback:\n  mode: \"proxy\"\n\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\n\nproxies: []\nupstream:\n  - name: \"A\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n", good.URL)
	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		app.Identity.Clear()
		token := loginToken(t, handler, "secret")
		app.Identity.Clear()
		rr := doJSONRequest(t, handler, http.MethodPut, "/admin/api/upstream/0", map[string]any{
			"name":        "Recovered Later",
			"url":         blocked.URL,
			"username":    "u2",
			"password":    "p2",
			"spoofClient": "passthrough",
		}, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("update passthrough status = %d, want 200 body=%s", rr.Code, rr.Body.String())
		}
		if authHits.Load() != 0 {
			t.Fatalf("upstream AuthenticateByName hits = %d, want 0", authHits.Load())
		}
	})
}

func readAllResponseBody(t *testing.T, resp *http.Response) string {
	t.Helper()
	if resp == nil || resp.Body == nil {
		return ""
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	return string(data)
}
