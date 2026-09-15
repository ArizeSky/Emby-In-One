package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFallbackConvertsHTMLUpstreamErrorToJSON502(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-a", "User": map[string]any{"Id": "user-a"}})
		case r.Method == http.MethodGet && r.URL.Path == "/CustomHTML/item-a":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("<!DOCTYPE html><html><body>Cloudflare</body></html>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	config := parityConfigWithUpstreams(fmt.Sprintf("  - name: \"A\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n", upstream.URL))

	withTempAppPrepared(t, config, nil, func(app *App, handler http.Handler, dir string) {
		token := loginToken(t, handler, "secret")
		virtualID := app.IDStore.GetOrCreateVirtualID("item-a", 0)
		rr := doJSONRequest(t, handler, http.MethodGet, "/CustomHTML/"+virtualID, nil, token)
		if rr.Code != http.StatusBadGateway {
			t.Fatalf("fallback status = %d, want 502 body=%s", rr.Code, rr.Body.String())
		}
		if got := rr.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
			t.Fatalf("content-type = %q, want application/json", got)
		}
		body := rr.Body.String()
		if strings.Contains(strings.ToLower(body), "<html") || strings.Contains(body, "Cloudflare") {
			t.Fatalf("expected upstream html to be sanitized, got %q", body)
		}
		var payload map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal fallback html payload: %v body=%s", err, rr.Body.String())
		}
		if payload["message"] != "Upstream returned HTML error page" {
			t.Fatalf("unexpected html fallback payload: %#v", payload)
		}
	})
}

func TestFallbackDoesNotBlockSVGOrXMLResponses(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-a", "User": map[string]any{"Id": "user-a"}})
		case r.Method == http.MethodGet && r.URL.Path == "/CustomHTML/item-a/Images/Primary":
			w.Header().Set("Content-Type", "image/svg+xml")
			_, _ = w.Write([]byte("<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	config := parityConfigWithUpstreams(fmt.Sprintf("  - name: \"A\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n", upstream.URL))

	withTempAppPrepared(t, config, nil, func(app *App, handler http.Handler, dir string) {
		token := loginToken(t, handler, "secret")
		virtualID := app.IDStore.GetOrCreateVirtualID("item-a", 0)
		rr := doJSONRequest(t, handler, http.MethodGet, "/CustomHTML/"+virtualID+"/Images/Primary", nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("svg fallback status = %d, want 200 body=%s", rr.Code, rr.Body.String())
		}
		if got := rr.Header().Get("Content-Type"); !strings.Contains(got, "image/svg+xml") {
			t.Fatalf("content-type = %q, want image/svg+xml", got)
		}
		if body := rr.Body.String(); body != "<svg xmlns=\"http://www.w3.org/2000/svg\"></svg>" {
			t.Fatalf("unexpected svg body: %q", body)
		}
	})
}
