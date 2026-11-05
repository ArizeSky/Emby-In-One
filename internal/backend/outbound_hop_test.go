package backend

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// These tests are the formal regressions for the identity findings in
// docs/web-playback-identity-repair-design.md: one fetch chain must present one
// client identity, a target only receives what its trust class allows, and a
// password-login body never crosses the configured API origin.

const hopUpstreamToken = "HOP-UPSTREAM-TOKEN"

func hopCustomClient(t *testing.T, url string, follow bool) *UpstreamClient {
	t.Helper()
	client := newUpstreamClient(Config{}, UpstreamConfig{
		ID: "hop-server", Name: "hop", URL: url,
		SpoofClient:         "custom",
		CustomUserAgent:     "hop-player/1",
		CustomClient:        "hop-client",
		CustomClientVersion: "1.0",
		CustomDeviceName:    "hop-device",
		CustomDeviceId:      "hop-device-id",
		FollowRedirects:     follow,
	}, 0, nil)
	client.setOnline(hopUpstreamToken, "hop-user")
	return client
}

// hopStreamConfig is strmProxyConfig plus the custom identity fields the tests
// assert on: the generic test config only injects a custom User-Agent.
func hopStreamConfig(url string, directRedirect bool) string {
	config := strmProxyConfig(url, directRedirect)
	return strings.ReplaceAll(config, "    password: \"p1\"\n",
		"    password: \"p1\"\n    customClient: \"hop-client\"\n    customDeviceName: \"hop-device\"\n    customDeviceId: \"hop-device-id\"\n    customClientVersion: \"1.0\"\n")
}

// A cross-origin hop this proxy follows on its own may keep the public UA and
// the transport headers, but no device identity, compound authorization, token
// or cookie — and it must not re-encode a signed query it only had to clean.
func TestAutomaticCrossOriginFollowProjectsPublicHeaders(t *testing.T) {
	signed := "z=last&signature=fixture%2fopaque&label=a%20b&a=first"
	var gotHeaders http.Header
	var gotQuery string
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeaders, gotQuery = r.Header.Clone(), r.URL.RawQuery
		_, _ = io.WriteString(w, "cdn-bytes")
	}))
	defer cdn.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", cdn.URL+"/film.mkv?"+signed)
		w.WriteHeader(http.StatusFound)
	}))
	defer upstream.Close()

	client := hopCustomClient(t, upstream.URL, true)
	resp, err := client.Stream(context.Background(), nil, nil, "/Videos/item/stream.mkv", nil)
	if err != nil {
		t.Fatalf("automatic follow failed: %v", err)
	}
	resp.Body.Close()

	if gotHeaders.Get("User-Agent") != "hop-player/1" {
		t.Errorf("CDN lost the public User-Agent: %q", gotHeaders.Get("User-Agent"))
	}
	for _, name := range []string{
		"X-Emby-Client", "X-Emby-Client-Version", "X-Emby-Device-Name", "X-Emby-Device-Id",
		"X-Emby-Authorization", "X-Emby-Token", "X-MediaBrowser-Token",
		"Authorization", "Cookie",
	} {
		if value := gotHeaders.Get(name); value != "" {
			t.Errorf("CDN received private header %s", name)
		}
	}
	// The credential parameter is gone; every other byte of the signature
	// material — order, lowercase escapes, %20 — is exactly as issued.
	if want := signed; gotQuery != want {
		t.Errorf("CDN query = %q, want the untouched signature %q", gotQuery, want)
	}
}

// A same-origin hop is served by the upstream itself: it keeps the resolved
// identity, the current credential and the URL exactly as issued.
func TestAutomaticSameOriginFollowKeepsIdentityAndCredential(t *testing.T) {
	var gotHeaders http.Header
	var gotQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/Videos/"):
			w.Header().Set("Location", "/file/film.mkv?api_key="+hopUpstreamToken+"&tag=x&p=a%2fb")
			w.WriteHeader(http.StatusFound)
		case r.URL.Path == "/file/film.mkv":
			gotHeaders, gotQuery = r.Header.Clone(), r.URL.RawQuery
			_, _ = io.WriteString(w, "bytes")
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	client := hopCustomClient(t, upstream.URL, true)
	resp, err := client.Stream(context.Background(), nil, nil, "/Videos/item/stream.mkv", nil)
	if err != nil {
		t.Fatalf("same-origin follow failed: %v", err)
	}
	resp.Body.Close()

	for name, want := range map[string]string{
		"User-Agent":            "hop-player/1",
		"X-Emby-Client":         "hop-client",
		"X-Emby-Device-Id":      "hop-device-id",
		"X-Emby-Token":          hopUpstreamToken,
		"X-Emby-Client-Version": "1.0",
	} {
		if got := gotHeaders.Get(name); got != want {
			t.Errorf("same-origin hop %s = %q, want %q", name, got, want)
		}
	}
	if want := "api_key=" + hopUpstreamToken + "&tag=x&p=a%2fb"; gotQuery != want {
		t.Errorf("same-origin hop query = %q, want %q", gotQuery, want)
	}
}

// The saved upstream password must never be re-sent to a target outside the API
// origin. 307/308 preserve method and body, so the next hop is refused before it
// is sent, and the failure is reported without the body, the credentials or the
// external target.
func TestPasswordLoginCrossOriginRedirectRefused(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var hits atomic.Int32
			var leakedBody atomic.Bool
			external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				payload, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
				if strings.Contains(string(payload), "fixture-password") {
					leakedBody.Store(true)
				}
				_, _ = io.WriteString(w, "{}")
			}))
			defer external.Close()
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", external.URL+"/login-target?api_key="+hopUpstreamToken)
				w.WriteHeader(status)
			}))
			defer upstream.Close()

			client := newUpstreamClient(Config{}, UpstreamConfig{
				ID: "login-server", Name: "login", URL: upstream.URL,
				Username: "fixture-user", Password: "fixture-password",
				SpoofClient: "custom", CustomUserAgent: "hop-player/1",
				FollowRedirects: true,
			}, 0, nil)
			client.Login(context.Background(), nil, nil)

			if hits.Load() != 0 {
				t.Fatalf("the untrusted target received %d request(s)", hits.Load())
			}
			if leakedBody.Load() {
				t.Fatal("the login body reached the untrusted target")
			}
			if client.IsOnline() {
				t.Fatal("a refused login was reported as online")
			}
			lastError := client.snapshot().LastError
			if !strings.Contains(lastError, "login-redirect-untrusted") {
				t.Fatalf("login failure = %q, want the desensitized redirect error", lastError)
			}
			for _, secret := range []string{"fixture-password", "fixture-user", hopUpstreamToken, external.URL} {
				if strings.Contains(lastError, secret) {
					t.Fatalf("login failure leaked %q: %s", secret, lastError)
				}
			}
		})
	}
}

// The restriction is about leaving the API origin, not about redirects: a login
// the API answers with a same-origin 307 still completes.
func TestPasswordLoginSameOriginRedirectCompletes(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Users/AuthenticateByName":
			w.Header().Set("Location", "/Users/AuthenticateByName/v2")
			w.WriteHeader(http.StatusTemporaryRedirect)
		case "/Users/AuthenticateByName/v2":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"AccessToken": hopUpstreamToken,
				"User":        map[string]any{"Id": "hop-user"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	client := newUpstreamClient(Config{}, UpstreamConfig{
		ID: "login-server", Name: "login", URL: upstream.URL,
		Username: "fixture-user", Password: "fixture-password",
		SpoofClient: "custom", CustomUserAgent: "hop-player/1",
		FollowRedirects: true,
	}, 0, nil)
	client.Login(context.Background(), nil, nil)

	if !client.IsOnline() {
		t.Fatalf("same-origin login redirect did not complete: %s", client.snapshot().LastError)
	}
	if got := client.getAccessToken(); got != hopUpstreamToken {
		t.Fatalf("access token = %q, want the redirected login's token", got)
	}
}

// A redirect that already converted the POST into a bodyless GET is not blocked:
// the password is gone, so the existing target and credential rules apply.
func TestPasswordLoginConvertedToGetFollowsWithoutBody(t *testing.T) {
	var method string
	var body atomic.Bool
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		payload, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		body.Store(len(payload) != 0)
		_, _ = io.WriteString(w, "{}")
	}))
	defer external.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", external.URL+"/login-target")
		w.WriteHeader(http.StatusSeeOther)
	}))
	defer upstream.Close()

	client := newUpstreamClient(Config{}, UpstreamConfig{
		ID: "login-server", Name: "login", URL: upstream.URL,
		Username: "fixture-user", Password: "fixture-password",
		SpoofClient: "custom", CustomUserAgent: "hop-player/1",
		FollowRedirects: true,
	}, 0, nil)
	client.Login(context.Background(), nil, nil)

	if method != http.MethodGet {
		t.Fatalf("converted hop method = %q, want GET", method)
	}
	if body.Load() {
		t.Fatal("the converted hop carried a body")
	}
}

// The login-body restriction must not touch media reads: a 307 to a CDN is still
// followed, still relays the bytes, and still carries no upstream credential.
func TestMediaCrossOriginRedirectStillFollowed(t *testing.T) {
	var cdnHeaders http.Header
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cdnHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "video/x-matroska")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "cdn-bytes")
	}))
	defer cdn.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-a", "User": map[string]any{"Id": "user-a"}})
		case strings.HasPrefix(r.URL.Path, "/Videos/"):
			w.Header().Set("Location", cdn.URL+"/film.mkv?sig=abc")
			w.WriteHeader(http.StatusTemporaryRedirect)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	// directRedirect off: the proxy follows the redirect itself, which is the
	// path the login restriction must leave alone.
	withTempAppConfig(t, strmProxyConfig(upstream.URL, false), func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		virtualItem := app.IDStore.GetOrCreateVirtualID("episode-a", app.Upstream.Clients()[0].ID)
		rr := doJSONRequest(t, handler, http.MethodGet, "/Videos/"+virtualItem+"/stream.mkv", nil, token)
		if rr.Code != http.StatusOK || rr.Body.String() != "cdn-bytes" {
			t.Fatalf("media follow status=%d body=%q", rr.Code, rr.Body.String())
		}
		if got := cdnHeaders.Get("X-Emby-Token"); got != "" {
			t.Fatal("CDN received the upstream token")
		}
		if got := cdnHeaders.Get("X-Emby-Device-Id"); got != "" {
			t.Fatal("CDN received the device identity")
		}
	})
}

// The media handler follows same-origin redirects itself. Its second hop must
// present the identity resolved for the first one instead of degrading to the Go
// default User-Agent.
func TestManualStreamRedirectKeepsResolvedIdentity(t *testing.T) {
	var first, last http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-a", "User": map[string]any{"Id": "user-a"}})
		case strings.HasPrefix(r.URL.Path, "/Videos/"):
			first = r.Header.Clone()
			w.Header().Set("Location", "/file/episode.mp4")
			w.WriteHeader(http.StatusFound)
		case r.URL.Path == "/file/episode.mp4":
			last = r.Header.Clone()
			_, _ = io.WriteString(w, "bytes")
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	withTempAppConfig(t, hopStreamConfig(upstream.URL, true), func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		virtualItem := app.IDStore.GetOrCreateVirtualID("episode-a", app.Upstream.Clients()[0].ID)
		rr := doJSONRequest(t, handler, http.MethodGet, "/Videos/"+virtualItem+"/stream.mp4", nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("manual follow status=%d body=%q", rr.Code, rr.Body.String())
		}
		if last == nil {
			t.Fatal("the same-origin follow never reached the upstream")
		}
		for name, want := range map[string]string{
			"User-Agent":       "TestClient/1.0",
			"X-Emby-Client":    first.Get("X-Emby-Client"),
			"X-Emby-Device-Id": first.Get("X-Emby-Device-Id"),
			"X-Emby-Token":     "tok-a",
		} {
			if want == "" {
				t.Fatalf("first hop did not carry %s", name)
			}
			if got := last.Get(name); got != want {
				t.Errorf("follow hop %s = %q, want %q", name, got, want)
			}
		}
	})
}

// The image handler follows redirects on its own too; it lost the identity on
// exactly the same branch.
func TestManualImageRedirectKeepsResolvedIdentity(t *testing.T) {
	var first, last http.Header
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-a", "User": map[string]any{"Id": "user-a"}})
		case strings.Contains(r.URL.Path, "/Images/"):
			first = r.Header.Clone()
			w.Header().Set("Location", "/file/poster.jpg")
			w.WriteHeader(http.StatusFound)
		case r.URL.Path == "/file/poster.jpg":
			last = r.Header.Clone()
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = io.WriteString(w, "fixture-image")
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	withTempAppConfig(t, hopStreamConfig(upstream.URL, true), func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		virtualItem := app.IDStore.GetOrCreateVirtualID("episode-a", app.Upstream.Clients()[0].ID)
		rr := doJSONRequest(t, handler, http.MethodGet, "/Items/"+virtualItem+"/Images/Primary", nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("image follow status=%d body=%q", rr.Code, rr.Body.String())
		}
		if last == nil {
			t.Fatal("the image follow never reached the upstream")
		}
		for name, want := range map[string]string{
			"User-Agent":       "TestClient/1.0",
			"X-Emby-Client":    first.Get("X-Emby-Client"),
			"X-Emby-Device-Id": first.Get("X-Emby-Device-Id"),
			"X-Emby-Token":     "tok-a",
		} {
			if want == "" {
				t.Fatalf("first image hop did not carry %s", name)
			}
			if got := last.Get(name); got != want {
				t.Errorf("image follow hop %s = %q, want %q", name, got, want)
			}
		}
	})
}

// A resource URL issued with an older token must be refreshed from the same auth
// snapshot as its headers, and a signed query without a credential must pass
// through byte-for-byte.
func TestResourceURLCredentialFollowsAuthSnapshot(t *testing.T) {
	var query, headerToken, rawQuery string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.Query().Get("api_key")
		headerToken = r.Header.Get("X-Emby-Token")
		rawQuery = r.URL.RawQuery
		_, _ = io.WriteString(w, "data")
	}))
	defer upstream.Close()

	client := hopCustomClient(t, upstream.URL, true)
	resp, err := client.fetchResourceRedirectTarget(context.Background(), http.MethodGet,
		upstream.URL+"/Videos/item/stream.mp4?api_key=stale-token&PlaySessionId=s1&tag=a%20b", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if query != hopUpstreamToken || headerToken != hopUpstreamToken {
		t.Fatalf("mixed credential snapshots: url=%q header=%q", query, headerToken)
	}
	if want := "api_key=" + hopUpstreamToken + "&PlaySessionId=s1&tag=a%20b"; rawQuery != want {
		t.Fatalf("refresh re-encoded the query: %q, want %q", rawQuery, want)
	}

	signed := "signature=fixture%2fopaque&part=1&x=a%20b"
	resp, err = client.fetchResourceRedirectTarget(context.Background(), http.MethodGet, upstream.URL+"/data?"+signed, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if rawQuery != signed {
		t.Fatalf("signature-only query changed: %q, want %q", rawQuery, signed)
	}
}

func TestRewriteRawQueryCredentials(t *testing.T) {
	cases := []struct {
		name       string
		rawQuery   string
		token      string
		want       string
		wantChange bool
	}{
		{name: "empty", rawQuery: "", token: "t", want: "", wantChange: false},
		{name: "no credential", rawQuery: "a=1&b=2", token: "t", want: "a=1&b=2", wantChange: false},
		{name: "replace in place", rawQuery: "a=1&api_key=x&b=2", token: "t", want: "a=1&api_key=t&b=2", wantChange: true},
		{name: "already current", rawQuery: "api_key=t&b=2", token: "t", want: "api_key=t&b=2", wantChange: false},
		{name: "drop all variants", rawQuery: "b=2&ApiKey=x&api_key=y&c=3", token: "", want: "b=2&c=3", wantChange: true},
		{name: "duplicates keep first", rawQuery: "api_key=x&api_key=y&b=2", token: "t", want: "api_key=t&b=2", wantChange: true},
		{name: "unknown alias kept", rawQuery: "api%zz=x&b=2", token: "t", want: "api%zz=x&b=2", wantChange: false},
		{name: "x-emby-token dropped", rawQuery: "x-emby-token=x&b=2", token: "", want: "b=2", wantChange: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got, changed := rewriteRawQueryCredentials(testCase.rawQuery, testCase.token)
			if got != testCase.want || changed != testCase.wantChange {
				t.Fatalf("rewriteRawQueryCredentials(%q, %q) = (%q, %v), want (%q, %v)",
					testCase.rawQuery, testCase.token, got, changed, testCase.want, testCase.wantChange)
			}
		})
	}
}
