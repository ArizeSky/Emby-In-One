package backend

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func withTempApp(t *testing.T, fn func(app *App, handler http.Handler)) {
	t.Helper()
	config := "server:\n  port: 8096\n  name: \"Test Server\"\n  id: \"server-1\"\n\nadmin:\n  username: \"admin\"\n  password: \"secret\"\n\nplayback:\n  mode: \"proxy\"\n\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\n\nproxies: []\nupstream: []\n"
	withTempAppConfig(t, config, fn)
}

func withTempAppConfig(t *testing.T, config string, fn func(app *App, handler http.Handler)) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(config), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "public"), 0o755); err != nil {
		t.Fatalf("create public dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "public", "admin.html"), []byte("<html>admin</html>"), 0o644); err != nil {
		t.Fatalf("write admin html: %v", err)
	}

	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })

	app, err := NewApp()
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })

	fn(app, app.Handler())
}

func doJSONRequest(t *testing.T, handler http.Handler, method, target string, body any, token string) *httptest.ResponseRecorder {
	t.Helper()
	var payload []byte
	switch v := body.(type) {
	case nil:
		payload = nil
	case string:
		payload = []byte(v)
	default:
		var err error
		payload, err = json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
	}
	req := httptest.NewRequest(method, target, bytes.NewReader(payload))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("X-Emby-Token", token)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func loginToken(t *testing.T, handler http.Handler, password string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", bytes.NewBufferString(`{"Username":"admin","Pw":"`+password+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "TestUA/1.0")
	req.Header.Set("X-Emby-Client", "TestClient")
	req.Header.Set("X-Emby-Client-Version", "1.2.3")
	req.Header.Set("X-Emby-Device-Name", "Test Device")
	req.Header.Set("X-Emby-Device-Id", "device-1")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("login status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal login payload: %v", err)
	}
	token, _ := payload["AccessToken"].(string)
	if token == "" {
		t.Fatalf("missing access token: %#v", payload)
	}
	return token
}

func TestHandlerSupportsEmbyPrefixAndClientCapture(t *testing.T) {
	withTempApp(t, func(app *App, handler http.Handler) {
		req := httptest.NewRequest(http.MethodGet, "/emby/System/Info/Public", nil)
		req.Header.Set("Origin", "http://evil.test")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("public info status = %d", rr.Code)
		}
		if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "*" {
			t.Fatalf("public info ACAO = %q, want *", got)
		}

		token := loginToken(t, handler, "secret")

		infoReq := httptest.NewRequest(http.MethodGet, "/admin/api/client-info", nil)
		infoReq.Header.Set("X-Emby-Token", token)
		infoRR := httptest.NewRecorder()
		handler.ServeHTTP(infoRR, infoReq)
		if infoRR.Code != http.StatusOK {
			t.Fatalf("client-info status = %d, body=%s", infoRR.Code, infoRR.Body.String())
		}
		var info map[string]any
		if err := json.Unmarshal(infoRR.Body.Bytes(), &info); err != nil {
			t.Fatalf("unmarshal client-info: %v", err)
		}
		if info["client"] != "TestClient" || info["deviceName"] != "Test Device" {
			t.Fatalf("unexpected client info: %#v", info)
		}
	})
}

func TestAdminCorsSameOriginOnly(t *testing.T) {
	withTempApp(t, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")

		crossReq := httptest.NewRequest(http.MethodGet, "/admin/api/status", nil)
		crossReq.Host = "example.test"
		crossReq.Header.Set("Origin", "http://evil.test")
		crossReq.Header.Set("X-Emby-Token", token)
		crossRR := httptest.NewRecorder()
		handler.ServeHTTP(crossRR, crossReq)
		if got := crossRR.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Fatalf("cross-origin admin ACAO = %q, want empty", got)
		}

		sameReq := httptest.NewRequest(http.MethodGet, "/admin/api/status", nil)
		sameReq.Host = "example.test"
		sameReq.Header.Set("Origin", "http://example.test")
		sameReq.Header.Set("X-Emby-Token", token)
		sameRR := httptest.NewRecorder()
		handler.ServeHTTP(sameRR, sameReq)
		if got := sameRR.Header().Get("Access-Control-Allow-Origin"); got != "http://example.test" {
			t.Fatalf("same-origin admin ACAO = %q, want same origin", got)
		}
	})
}

func TestAdminSettingsUpstreamAndProxyCrud(t *testing.T) {
	makeUpstream := func(token, userID string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
				_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": token, "User": map[string]any{"Id": userID}})
			default:
				http.NotFound(w, r)
			}
		}))
	}

	upstreamA := makeUpstream("token-a", "user-a")
	defer upstreamA.Close()
	upstreamB := makeUpstream("token-b", "user-b")
	defer upstreamB.Close()

	withTempApp(t, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")

		settingsRR := doJSONRequest(t, handler, http.MethodPut, "/admin/api/settings", map[string]any{
			"serverName":      "Go Server",
			"adminPassword":   "newsecret",
			"currentPassword": "secret",
			"timeouts": map[string]any{
				"api": 12345,
			},
		}, token)
		if settingsRR.Code != http.StatusOK {
			t.Fatalf("settings update status = %d, body=%s", settingsRR.Code, settingsRR.Body.String())
		}

		oldLoginReq := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", bytes.NewBufferString(`{"Username":"admin","Pw":"secret"}`))
		oldLoginReq.Header.Set("Content-Type", "application/json")
		oldLoginRR := httptest.NewRecorder()
		handler.ServeHTTP(oldLoginRR, oldLoginReq)
		if oldLoginRR.Code != http.StatusUnauthorized {
			t.Fatalf("old password login status = %d, want 401", oldLoginRR.Code)
		}

		token = loginToken(t, handler, "newsecret")

		createUpstreamRR := doJSONRequest(t, handler, http.MethodPost, "/admin/api/upstream", map[string]any{
			"name":     "A",
			"url":      upstreamA.URL,
			"username": "u1",
			"password": "p1",
		}, token)
		if createUpstreamRR.Code != http.StatusOK {
			t.Fatalf("create upstream status = %d, body=%s", createUpstreamRR.Code, createUpstreamRR.Body.String())
		}
		createUpstreamRR2 := doJSONRequest(t, handler, http.MethodPost, "/admin/api/upstream", map[string]any{
			"name":     "B",
			"url":      upstreamB.URL,
			"username": "u2",
			"password": "p2",
		}, token)
		if createUpstreamRR2.Code != http.StatusOK {
			t.Fatalf("create upstream2 status = %d, body=%s", createUpstreamRR2.Code, createUpstreamRR2.Body.String())
		}

		updateUpstreamRR := doJSONRequest(t, handler, http.MethodPut, "/admin/api/upstream/0", map[string]any{
			"followRedirects": false,
			"spoofClient":     "passthrough",
		}, token)
		if updateUpstreamRR.Code != http.StatusOK {
			t.Fatalf("update upstream status = %d, body=%s", updateUpstreamRR.Code, updateUpstreamRR.Body.String())
		}

		listRR := doJSONRequest(t, handler, http.MethodGet, "/admin/api/upstream", nil, token)
		if listRR.Code != http.StatusOK {
			t.Fatalf("list upstream status = %d", listRR.Code)
		}
		var list []map[string]any
		if err := json.Unmarshal(listRR.Body.Bytes(), &list); err != nil {
			t.Fatalf("unmarshal upstream list: %v", err)
		}
		if len(list) != 2 {
			t.Fatalf("upstream count = %d, want 2", len(list))
		}
		if list[0]["followRedirects"] != false || list[0]["spoofClient"] != "passthrough" {
			t.Fatalf("unexpected updated upstream entry: %#v", list[0])
		}

		reorderRR := doJSONRequest(t, handler, http.MethodPost, "/admin/api/upstream/reorder", map[string]any{"fromIndex": 0, "toIndex": 1}, token)
		if reorderRR.Code != http.StatusOK {
			t.Fatalf("reorder upstream status = %d, body=%s", reorderRR.Code, reorderRR.Body.String())
		}
		listRR = doJSONRequest(t, handler, http.MethodGet, "/admin/api/upstream", nil, token)
		if err := json.Unmarshal(listRR.Body.Bytes(), &list); err != nil {
			t.Fatalf("unmarshal reordered upstream list: %v", err)
		}
		if list[0]["name"] != "B" || list[1]["name"] != "A" {
			t.Fatalf("unexpected reorder result: %#v", list)
		}

		deleteRR := doJSONRequest(t, handler, http.MethodDelete, "/admin/api/upstream/0", nil, token)
		if deleteRR.Code != http.StatusOK {
			t.Fatalf("delete upstream status = %d, body=%s", deleteRR.Code, deleteRR.Body.String())
		}
		listRR = doJSONRequest(t, handler, http.MethodGet, "/admin/api/upstream", nil, token)
		if err := json.Unmarshal(listRR.Body.Bytes(), &list); err != nil {
			t.Fatalf("unmarshal upstream list after delete: %v", err)
		}
		if len(list) != 1 || list[0]["name"] != "A" {
			t.Fatalf("unexpected list after delete: %#v", list)
		}

		proxyCreateRR := doJSONRequest(t, handler, http.MethodPost, "/admin/api/proxies", map[string]any{
			"name": "Proxy 1",
			"url":  "https://proxy.example",
		}, token)
		if proxyCreateRR.Code != http.StatusOK {
			t.Fatalf("create proxy status = %d, body=%s", proxyCreateRR.Code, proxyCreateRR.Body.String())
		}
		var proxy map[string]any
		if err := json.Unmarshal(proxyCreateRR.Body.Bytes(), &proxy); err != nil {
			t.Fatalf("unmarshal proxy create: %v", err)
		}
		proxyID, _ := proxy["ID"].(string)
		if proxyID == "" {
			proxyID, _ = proxy["id"].(string)
		}
		if proxyID == "" {
			t.Fatalf("missing proxy id: %#v", proxy)
		}

		proxyListRR := doJSONRequest(t, handler, http.MethodGet, "/admin/api/proxies", nil, token)
		if proxyListRR.Code != http.StatusOK {
			t.Fatalf("list proxies status = %d", proxyListRR.Code)
		}
		var proxies []map[string]any
		if err := json.Unmarshal(proxyListRR.Body.Bytes(), &proxies); err != nil {
			t.Fatalf("unmarshal proxies list: %v", err)
		}
		if len(proxies) != 1 {
			t.Fatalf("proxy count = %d, want 1", len(proxies))
		}

		proxyDeleteReq := httptest.NewRequest(http.MethodDelete, "/admin/api/proxies/"+proxyID, nil)
		proxyDeleteReq.Header.Set("X-Emby-Token", token)
		proxyDeleteRR := httptest.NewRecorder()
		handler.ServeHTTP(proxyDeleteRR, proxyDeleteReq)
		if proxyDeleteRR.Code != http.StatusOK {
			t.Fatalf("delete proxy status = %d, body=%s", proxyDeleteRR.Code, proxyDeleteRR.Body.String())
		}
	})
}

func TestAdminLogsEndpoints(t *testing.T) {
	withTempApp(t, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")

		logsRR := doJSONRequest(t, handler, http.MethodGet, "/admin/api/logs?limit=10", nil, token)
		if logsRR.Code != http.StatusOK {
			t.Fatalf("logs status = %d", logsRR.Code)
		}
		var entries []map[string]any
		if err := json.Unmarshal(logsRR.Body.Bytes(), &entries); err != nil {
			t.Fatalf("unmarshal logs: %v", err)
		}
		if len(entries) == 0 {
			t.Fatalf("expected at least one log entry")
		}

		downloadReq := httptest.NewRequest(http.MethodGet, "/admin/api/logs/download", nil)
		downloadReq.Header.Set("X-Emby-Token", token)
		downloadRR := httptest.NewRecorder()
		handler.ServeHTTP(downloadRR, downloadReq)
		if downloadRR.Code != http.StatusOK {
			t.Fatalf("log download status = %d", downloadRR.Code)
		}

		clearRR := doJSONRequest(t, handler, http.MethodDelete, "/admin/api/logs", nil, token)
		if clearRR.Code != http.StatusOK {
			t.Fatalf("clear logs status = %d, body=%s", clearRR.Code, clearRR.Body.String())
		}
	})
}

func TestSystemInfoPublicUsesRequestHostForLocalAddress(t *testing.T) {
	withTempApp(t, func(app *App, handler http.Handler) {
		req := httptest.NewRequest(http.MethodGet, "/System/Info/Public", nil)
		req.Host = "internal.example:8096"
		req.Header.Set("X-Forwarded-Proto", "https")
		req.Header.Set("X-Forwarded-Host", "media.example.com")
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("public info status = %d, body=%s", rr.Code, rr.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal public info: %v", err)
		}
		if payload["LocalAddress"] != "https://media.example.com" {
			t.Fatalf("LocalAddress = %#v, want https://media.example.com", payload["LocalAddress"])
		}
	})
}

func TestFaviconReturns204(t *testing.T) {
	withTempApp(t, func(app *App, handler http.Handler) {
		req := httptest.NewRequest(http.MethodGet, "/favicon.ico", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("favicon status = %d, want 204 body=%s", rr.Code, rr.Body.String())
		}
		if rr.Body.Len() != 0 {
			t.Fatalf("favicon body length = %d, want 0", rr.Body.Len())
		}
	})
}
