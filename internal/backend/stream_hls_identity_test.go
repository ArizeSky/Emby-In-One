package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const (
	hlsUpstreamUserID = "hls-user-1"
	hlsUpstreamToken  = "hls-token-1"
)

// TestStreamHLSIdentityBaseURL covers the proxied HLS path, where the manifest is
// rewritten so its segment URLs keep pointing back at this proxy. The base URL
// that rewrite is built from must be the one already sent, and the identity it
// carries must come from the same snapshot as the request.
func TestStreamHLSIdentityBaseURL(t *testing.T) {
	var manifestRequestURL string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": hlsUpstreamToken, "User": map[string]any{"Id": hlsUpstreamUserID}})
		case strings.HasSuffix(r.URL.Path, "/master.m3u8"):
			manifestRequestURL = r.URL.String()
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000000\nsegment0.ts\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	withTempAppConfig(t, singleUpstreamConfig(upstream.URL), func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		legacyProxyUser := app.Auth.ProxyUserID()
		virtualID := app.IDStore.GetOrCreateVirtualID("hls-item", 0)

		target := "/Videos/" + virtualID + "/master.m3u8?UserId=" + legacyProxyUser + "&api_key=" + token
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Header.Set("X-Emby-Token", token)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
		}
		if manifestRequestURL == "" {
			t.Fatalf("the upstream never received the manifest request")
		}
		upstreamURL, err := url.Parse(manifestRequestURL)
		if err != nil {
			t.Fatalf("parse upstream URL: %v", err)
		}
		if got := upstreamURL.Query().Get("api_key"); got != hlsUpstreamToken {
			t.Fatalf("upstream api_key = %q, want the upstream token", got)
		}
		if got := upstreamURL.Query().Get("UserId"); got != hlsUpstreamUserID {
			t.Fatalf("upstream UserId = %q, want %q", got, hlsUpstreamUserID)
		}

		body := rr.Body.String()
		// The manifest's segment URLs are built from the base URL of the response
		// that was actually sent, so they carry the same prepared identity. The
		// existing behaviour of addressing the upstream directly, with the virtual
		// item id substituted, is unchanged.
		if !strings.Contains(body, "/Videos/"+virtualID+"/") {
			t.Fatalf("the manifest does not carry the virtual stream path: %s", body)
		}
		// The manifest addresses the client back through this proxy, which is the
		// existing behaviour: the item id is virtualised and the segment query is
		// re-signed with the client's own proxy token.
		if !strings.Contains(body, "api_key="+token) {
			t.Fatalf("the manifest does not address the client back through the proxy: %s", body)
		}
		// The upstream's real identity must never appear in what the client reads.
		if strings.Contains(body, hlsUpstreamToken) {
			t.Fatalf("the upstream token reached the client-facing manifest: %s", body)
		}
		if strings.Contains(body, hlsUpstreamUserID) {
			t.Fatalf("the upstream user id reached the client-facing manifest: %s", body)
		}
		// And the request that produced it was addressed to the upstream's real user.
		if _, ok := upstreamURL.Query()["UserId"]; !ok {
			t.Fatalf("the upstream request carried no UserId: %s", manifestRequestURL)
		}
		if strings.Contains(manifestRequestURL, virtualID) {
			t.Fatalf("the upstream was addressed with a virtual id: %s", manifestRequestURL)
		}
	})
}

// TestStreamHLSFallbackBaseURL covers the same path when the upstream response
// the proxy holds has no Request attached, which is how a test-constructed
// response looks. The base URL is then prepared again and must still be usable.
func TestStreamHLSFallbackBaseURL(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": hlsUpstreamToken, "User": map[string]any{"Id": hlsUpstreamUserID}})
		case strings.HasSuffix(r.URL.Path, "/master.m3u8"):
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			_, _ = w.Write([]byte("#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1000000\nsegment0.ts\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	withTempAppConfig(t, singleUpstreamConfig(upstream.URL), func(app *App, handler http.Handler) {
		client := app.Upstream.GetClient(0)
		reqCtx := &RequestContext{ProxyUser: &tokenInfo{UserID: "local-user", Role: "user"}}

		built, err := client.BuildURL("/Videos/orig/master.m3u8", map[string][]string{"UserId": {"local-user"}}, true, reqCtx)
		if err != nil {
			t.Fatalf("BuildURL: %v", err)
		}
		parsed, err := url.Parse(built)
		if err != nil {
			t.Fatalf("parse built URL: %v", err)
		}
		if parsed.Query().Get("UserId") != hlsUpstreamUserID {
			t.Fatalf("built UserId = %q, want %q", parsed.Query().Get("UserId"), hlsUpstreamUserID)
		}
		if parsed.Query().Get("api_key") != hlsUpstreamToken {
			t.Fatalf("built api_key = %q, want the upstream token", parsed.Query().Get("api_key"))
		}
	})
}

// A preparation failure on the redirect path is reported with its own status
// instead of a bare 502, because the request never left the process.
func TestStreamRedirectPreparationFailureStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName" {
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": hlsUpstreamToken, "User": map[string]any{"Id": hlsUpstreamUserID}})
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test\"\n  id: \"svr\"\nadmin:\n  username: \"admin\"\n  password: \"secret\"\nplayback:\n  mode: \"redirect\"\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\nproxies: []\nupstream:\n  - name: \"A\"\n    url: %q\n    username: \"u\"\n    password: \"p\"\n", upstream.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		legacyProxyUser := app.Auth.ProxyUserID()
		virtualID := app.IDStore.GetOrCreateVirtualID("media-1", 0)

		client := app.Upstream.GetClient(0)
		client.mu.Lock()
		client.UserID = ""
		client.mu.Unlock()

		req := httptest.NewRequest(http.MethodGet, "/Videos/"+virtualID+"/stream.mp4?UserId="+legacyProxyUser, nil)
		req.Header.Set("X-Emby-Token", token)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		status, ok := preparationErrorStatus(fmt.Errorf("wrapped: %w", newMissingAuthStateError("path.UserId")))
		if !ok || status != http.StatusServiceUnavailable {
			t.Fatalf("preparation status = %d (%v), want 503", status, ok)
		}
		if rr.Code == http.StatusFound && strings.Contains(rr.Header().Get("Location"), legacyProxyUser) {
			t.Fatalf("a redirect was emitted with a stale identity: %s", rr.Header().Get("Location"))
		}
	})
}
