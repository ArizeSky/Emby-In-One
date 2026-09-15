package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const (
	redirectUpstreamToken = "REDIRECT-UPSTREAM-TOKEN"
	redirectUpstreamUser  = "redirect-user-1"
)

// configWithFollowRedirects is singleUpstreamConfig with an explicit
// followRedirects value, which singleUpstreamConfig never emits.
func configWithFollowRedirects(url string, follow bool) string {
	return fmt.Sprintf(`server:
  port: 8096
  name: "Test"
  id: "svr"
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
proxies: []
upstream:
  - name: "A"
    url: %q
    username: "u1"
    password: "p1"
    followRedirects: %v
`, url, follow)
}

// configWithDeadStreamingURL points an upstream's stream base at an address
// nothing listens on, so a stream request fails at connect time.
func configWithDeadStreamingURL(url, streamingURL string) string {
	base := configWithFollowRedirects(url, true)
	return base + fmt.Sprintf("    streamingUrl: %q\n", streamingURL)
}

// redirectingUpstream answers the login, redirects the item request to
// /redirected, and answers that path with the payload the tests look for.
func redirectingUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"AccessToken": redirectUpstreamToken,
				"User":        map[string]any{"Id": redirectUpstreamUser},
			})
		case r.URL.Path == "/redirected":
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "orig-item", "Name": "from-redirect", "Type": "Movie"})
		default:
			// An upstream that moved its content elsewhere.
			http.Redirect(w, r, "/redirected", http.StatusFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// Following is the shipped default. A configuration that does not mention the
// key must keep following, exactly as it did before the setting was wired up.
func TestUpstreamRedirectFollowedByDefault(t *testing.T) {
	upstream := redirectingUpstream(t)

	withTempAppConfig(t, singleUpstreamConfig(upstream.URL), func(app *App, handler http.Handler) {
		client := app.Upstream.GetClient(0)
		if client == nil {
			t.Fatalf("upstream 0 is missing")
		}
		if !client.Config.FollowRedirects {
			t.Fatalf("a configuration that omits followRedirects must default to following")
		}

		token := loginToken(t, handler, "secret")
		virtualItem := app.IDStore.GetOrCreateVirtualID("orig-item", 0)
		rr := doJSONRequest(t, handler, http.MethodGet, "/Items/"+virtualItem, nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "from-redirect") {
			t.Fatalf("the redirect was not followed: %s", rr.Body.String())
		}
	})
}

// Turning following off stops the request at the upstream's redirect. The proxy
// reports an upstream failure and never hands the redirect target to the client
// as a successful response.
func TestUpstreamRedirectNotFollowed(t *testing.T) {
	upstream := redirectingUpstream(t)

	withTempAppConfig(t, configWithFollowRedirects(upstream.URL, false), func(app *App, handler http.Handler) {
		client := app.Upstream.GetClient(0)
		if client == nil {
			t.Fatalf("upstream 0 is missing")
		}
		if client.Config.FollowRedirects {
			t.Fatalf("followRedirects: false was not parsed")
		}

		token := loginToken(t, handler, "secret")
		virtualItem := app.IDStore.GetOrCreateVirtualID("orig-item", 0)
		rr := doJSONRequest(t, handler, http.MethodGet, "/Items/"+virtualItem, nil, token)

		if rr.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502, body=%s", rr.Code, rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), "from-redirect") {
			t.Fatalf("the redirect target was served to the client: %s", rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), redirectUpstreamToken) {
			t.Fatalf("the upstream token reached the client: %s", rr.Body.String())
		}
	})
}

// A stream request is the one shape that carries the upstream token in its query
// string, so it is where an upstream failure could hand that credential to the
// client. A refused redirect is not that case: net/http builds the error from the
// redirect *target*, not from the request this proxy sent. What matters here is
// that the redirect is not served as a stream.
func TestUpstreamRedirectNotFollowedStreamIsAnError(t *testing.T) {
	upstream := redirectingUpstream(t)

	withTempAppConfig(t, configWithFollowRedirects(upstream.URL, false), func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		virtualItem := app.IDStore.GetOrCreateVirtualID("orig-item", 0)

		req := httptest.NewRequest(http.MethodGet, "/Videos/"+virtualItem+"/master.m3u8", nil)
		req.Header.Set("X-Emby-Token", token)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502, body=%s", rr.Code, rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), "from-redirect") {
			t.Fatalf("the redirect target was served as a stream: %s", rr.Body.String())
		}
	})
}

// A transport failure is the case that does carry the request this proxy sent:
// net/http builds it from the request URL, which for a stream request holds the
// upstream token. The error reaches the client, so it has to be redacted first.
func TestUpstreamConnectFailureDoesNotLeakToken(t *testing.T) {
	upstream := redirectingUpstream(t)
	// Nothing listens here, so the stream request fails to connect.
	config := configWithDeadStreamingURL(upstream.URL, "http://127.0.0.1:1")

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		virtualItem := app.IDStore.GetOrCreateVirtualID("orig-item", 0)

		req := httptest.NewRequest(http.MethodGet, "/Videos/"+virtualItem+"/master.m3u8", nil)
		req.Header.Set("X-Emby-Token", token)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502, body=%s", rr.Code, rr.Body.String())
		}
		body := rr.Body.String()
		if strings.Contains(body, redirectUpstreamToken) {
			t.Fatalf("the upstream token reached the client: %s", body)
		}
		if !strings.Contains(body, "api_key=[redacted") && !strings.Contains(body, "[redacted") {
			t.Fatalf("the request URL was not redacted in the client-facing error: %s", body)
		}
		// The URL is still useful for diagnosis: the path survives.
		if !strings.Contains(body, "/Videos/orig-item/master.m3u8") {
			t.Fatalf("the error lost the request path: %s", body)
		}
	})
}

// The refused redirect stays identifiable through the redaction wrapper, and the
// wrapper keeps errors.Is working for the cancellation checks handlers rely on.
func TestUpstreamRedirectPolicyShape(t *testing.T) {
	if redirectPolicy(true) != nil {
		t.Fatalf("following must be delegated to net/http by leaving CheckRedirect nil")
	}
	check := redirectPolicy(false)
	if check == nil {
		t.Fatalf("not following must install a CheckRedirect")
	}
	err := check(nil, nil)
	if !errors.Is(err, errUpstreamRedirectNotFollowed) {
		t.Fatalf("CheckRedirect error = %v, want errUpstreamRedirectNotFollowed", err)
	}

	wrapped := &redactedError{err: fmt.Errorf("Get %q: %w", "http://up.test/Items?api_key="+redirectUpstreamToken, errUpstreamRedirectNotFollowed)}
	if !errors.Is(wrapped, errUpstreamRedirectNotFollowed) {
		t.Fatalf("the redaction wrapper broke errors.Is")
	}
	if strings.Contains(wrapped.Error(), redirectUpstreamToken) {
		t.Fatalf("the redaction wrapper kept a credential: %s", wrapped.Error())
	}
	// The parameter name may remain; only its value has to be gone.
	t.Logf("redacted message: %s", wrapped.Error())

	// A cancellation still has to be recognisable through the wrapper: the stream
	// handler decides whether to answer the client based on it.
	cancelErr := &redactedError{err: fmt.Errorf("stream: %w", context.Canceled)}
	if !errors.Is(cancelErr, context.Canceled) {
		t.Fatalf("the redaction wrapper hid a cancellation")
	}
}
