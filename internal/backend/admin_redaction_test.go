package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdminStatusRedactsUpstreamURLAndUserID(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-a", "User": map[string]any{"Id": "user-a"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	config := parityConfigWithUpstreams(fmt.Sprintf("  - name: \"A\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n", upstream.URL))
	withTempAppPrepared(t, config, nil, func(app *App, handler http.Handler, dir string) {
		token := loginToken(t, handler, "secret")
		rr := doJSONRequest(t, handler, http.MethodGet, "/admin/api/status", nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("status code = %d, body=%s", rr.Code, rr.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal status payload: %v", err)
		}
		upstreamList, _ := payload["upstream"].([]any)
		if len(upstreamList) != 1 {
			t.Fatalf("upstream list len = %d, want 1 payload=%#v", len(upstreamList), payload)
		}
		entry := upstreamList[0].(map[string]any)
		if urlVal, ok := entry["url"].(string); ok {
			if strings.Contains(urlVal, "@") {
				t.Fatalf("status upstream leaked credentials in url: %s", urlVal)
			}
		}
		if _, ok := entry["userId"]; ok {
			t.Fatalf("status upstream leaked userId: %#v", entry)
		}
	})
}

func TestAdminUpstreamListRedactsCredentialBearingURL(t *testing.T) {
	upstream := httptest.NewServer(http.NotFoundHandler())
	defer upstream.Close()
	credentialURL := strings.Replace(upstream.URL, "http://", "http://user:secret@", 1)
	config := parityConfigWithUpstreams(fmt.Sprintf("  - name: \"A\"\n    url: %q\n    apiKey: \"api-key\"\n", credentialURL))

	withTempAppPrepared(t, config, nil, func(app *App, handler http.Handler, dir string) {
		token := loginToken(t, handler, "secret")
		rr := doJSONRequest(t, handler, http.MethodGet, "/admin/api/upstream", nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("upstream list code = %d, body=%s", rr.Code, rr.Body.String())
		}
		var payload []map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal upstream list: %v", err)
		}
		if len(payload) != 1 {
			t.Fatalf("upstream list len = %d, want 1", len(payload))
		}
		rawURL, _ := payload[0]["url"].(string)
		if strings.Contains(rawURL, "secret") || strings.Contains(rawURL, "user:") {
			t.Fatalf("upstream url not redacted: %q", rawURL)
		}
	})
}

func TestAdminProxyListMasksProxyPassword(t *testing.T) {
	withTempAppPrepared(t, parityConfigWithUpstreams(""), nil, func(app *App, handler http.Handler, dir string) {
		token := loginToken(t, handler, "secret")
		createRR := doJSONRequest(t, handler, http.MethodPost, "/admin/api/proxies", map[string]any{
			"name": "Proxy A",
			"url":  "http://user:secret@proxy.example:8080",
		}, token)
		if createRR.Code != http.StatusOK {
			t.Fatalf("create proxy code = %d, body=%s", createRR.Code, createRR.Body.String())
		}
		listRR := doJSONRequest(t, handler, http.MethodGet, "/admin/api/proxies", nil, token)
		if listRR.Code != http.StatusOK {
			t.Fatalf("proxy list code = %d, body=%s", listRR.Code, listRR.Body.String())
		}
		var proxies []map[string]any
		if err := json.Unmarshal(listRR.Body.Bytes(), &proxies); err != nil {
			t.Fatalf("unmarshal proxies list: %v", err)
		}
		if len(proxies) != 1 {
			t.Fatalf("proxy count = %d, want 1", len(proxies))
		}
		rawURL, _ := proxies[0]["url"].(string)
		if strings.Contains(rawURL, "secret") || strings.Contains(rawURL, "user:") {
			t.Fatalf("proxy url not masked: %q", rawURL)
		}
	})
}

func TestAdminResponsesIncludeSecurityHeaders(t *testing.T) {
	withTempAppPrepared(t, parityConfigWithUpstreams(""), nil, func(app *App, handler http.Handler, dir string) {
		token := loginToken(t, handler, "secret")

		apiReq := httptest.NewRequest(http.MethodGet, "/admin/api/status", nil)
		apiReq.Header.Set("X-Emby-Token", token)
		apiRR := httptest.NewRecorder()
		handler.ServeHTTP(apiRR, apiReq)
		if apiRR.Code != http.StatusOK {
			t.Fatalf("admin api status code = %d, body=%s", apiRR.Code, apiRR.Body.String())
		}
		for key, want := range map[string]string{
			"X-Content-Type-Options": "nosniff",
			"X-Frame-Options":        "SAMEORIGIN",
		} {
			if got := apiRR.Header().Get(key); got != want {
				t.Fatalf("admin api header %s = %q, want %q", key, got, want)
			}
		}

		pageReq := httptest.NewRequest(http.MethodGet, "/admin/admin.html", nil)
		pageRR := httptest.NewRecorder()
		handler.ServeHTTP(pageRR, pageReq)
		if pageRR.Code != http.StatusOK {
			t.Fatalf("admin page code = %d, body=%s", pageRR.Code, pageRR.Body.String())
		}
		for key, want := range map[string]string{
			"X-Content-Type-Options": "nosniff",
			"X-Frame-Options":        "SAMEORIGIN",
		} {
			if got := pageRR.Header().Get(key); got != want {
				t.Fatalf("admin page header %s = %q, want %q", key, got, want)
			}
		}
	})
}
