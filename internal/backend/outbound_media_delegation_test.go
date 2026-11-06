package backend

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Reproduce the observed 302 -> credential deletion -> gateway 401 using the
// real native, opaque-resource and strict negotiation/consumption routes.
func TestMediaGatewayIssuedCredentialPlaysThroughNativeAndStrictRoutes(t *testing.T) {
	for _, signed := range []bool{false, true} {
		name := "unsigned"
		if signed {
			name = "signed"
		}
		t.Run(name, func(t *testing.T) { testMediaGatewayRoutes(t, signed) })
	}
}

func testMediaGatewayRoutes(t *testing.T, signed bool) {
	t.Helper()
	const token = "fixture-gateway-upstream-token"
	rawQuery := "api_key=" + token + "&label=a%20b&part=2&part=1"
	if signed {
		rawQuery += "&signature=opaque%2fsignature"
	}
	hits := 0
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Query().Get("api_key") != token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.RawQuery != rawQuery || r.UserAgent() != "fixture-media-agent" || r.Header.Get("Range") != "bytes=0-3" {
			t.Error("media URL, public UA or Range changed")
		}
		for _, key := range []string{"X-Emby-Token", "Authorization", "X-Emby-Authorization", "X-Emby-Client", "X-Emby-Device-Id", "Cookie"} {
			if r.Header.Get(key) != "" {
				t.Errorf("gateway received private header %s", key)
			}
		}
		w.Header().Set("Content-Length", "4")
		if r.Method != http.MethodHead {
			w.Header().Set("Content-Range", "bytes 0-3/4")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = io.WriteString(w, "data")
		}
	}))
	defer gateway.Close()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": token, "User": map[string]any{"Id": "fixture-user"}})
		case strings.HasSuffix(r.URL.Path, "/PlaybackInfo"):
			_ = json.NewEncoder(w).Encode(map[string]any{"PlaySessionId": "fixture-session", "MediaSources": []any{map[string]any{
				"Id": "source", "Container": "mp4", "DirectStreamUrl": "/Videos/item/stream.mp4",
			}}})
		case r.URL.Path == "/Items/item":
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "item", "Type": "Movie", "OfficialRating": "G"})
		case strings.HasPrefix(r.URL.Path, "/Videos/"):
			if r.Header.Get("X-Emby-Token") != token {
				t.Error("trusted media hop lost authentication")
			}
			w.Header().Set("Location", gateway.URL+"/Videos/item/stream.mp4?"+rawQuery)
			w.WriteHeader(http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	config := playbackLimiterConfig(up.URL) + "    spoofClient: custom\n    customUserAgent: fixture-media-agent\n"
	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		local := createRegularUser(t, handler)
		item := app.IDStore.GetOrCreateVirtualID("item", app.Upstream.Clients()[0].ID)
		consume := func(method, path string) {
			t.Helper()
			r := httptest.NewRequest(method, path, nil)
			r.Header.Set("X-Emby-Token", local)
			r.Header.Set("Range", "bytes=0-3")
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			want := http.StatusPartialContent
			if method == http.MethodHead {
				want = http.StatusOK
			}
			if w.Code != want || method == http.MethodGet && w.Body.String() != "data" {
				t.Fatalf("media %s returned %d, want %d", method, w.Code, want)
			}
		}
		consume(http.MethodGet, "/Videos/"+item+"/stream.mp4")
		for _, prefix := range []string{"", "/eio/playback/v1"} {
			pi := doAuthJSON(t, handler, http.MethodGet, prefix+"/Items/"+item+"/PlaybackInfo", nil, local)
			if pi.Code != http.StatusOK {
				t.Fatalf("negotiation status=%d", pi.Code)
			}
			var info map[string]any
			if err := json.Unmarshal(pi.Body.Bytes(), &info); err != nil {
				t.Fatal(err)
			}
			source := info["MediaSources"].([]any)[0].(map[string]any)
			resource := playbackStringField(source, "DirectStreamUrl")
			if !strings.Contains(resource, "/eio-resource/") || strings.Contains(resource, token) {
				t.Fatal("media source was not registered as an opaque resource")
			}
			consume(http.MethodHead, resource)
			consume(http.MethodGet, resource)
		}
		if hits != 5 {
			t.Fatalf("expected five real gateway requests, got %d", hits)
		}
		found := false
		for _, entry := range app.Logger.Entries(0) {
			if strings.Contains(entry.Message, "query_action=upstream-media-credential-preserved") {
				found = true
				if strings.Contains(entry.Message, token) || strings.Contains(entry.Message, gateway.URL) {
					t.Fatal("delegation diagnostic leaked credentials or URL")
				}
			}
		}
		if !found {
			t.Fatal("explicit media delegation was not logged")
		}
	})
}

func TestMediaGatewayCannotExtendCredentialDelegation(t *testing.T) {
	var finalQuery string
	final := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		finalQuery = r.URL.RawQuery
		_, _ = io.WriteString(w, "data")
	}))
	defer final.Close()
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api_key") != hopUpstreamToken {
			t.Error("trusted upstream's issued URL credential was removed")
		}
		w.Header().Set("Location", final.URL+"/file?api_key="+hopUpstreamToken+"&part=1")
		w.WriteHeader(302)
	}))
	defer gateway.Close()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", gateway.URL+"/file?api_key="+hopUpstreamToken)
		w.WriteHeader(302)
	}))
	defer up.Close()
	c := hopCustomClient(t, up.URL, true)
	for _, mode := range []string{"automatic", "resource", "legacy-manual"} {
		var resp *http.Response
		var err error
		switch mode {
		case "resource":
			r := httptest.NewRequest("GET", "/Videos/item/local", nil)
			resp, err = (&App{}).fetchResourceFollowingRedirects(r, c, up.URL+"/Videos/item/stream.mp4")
		case "legacy-manual":
			snapshot := c.resolveIdentitySnapshot(nil, nil, nil)
			resp, err = c.fetchRedirectTarget(context.Background(), nil, up.URL+"/Videos/item/stream.mp4", snapshot, nil)
			for hop := 0; err == nil && isRedirectStatus(resp.StatusCode) && hop < 3; hop++ {
				target, parseErr := resp.Location()
				if parseErr != nil {
					t.Fatal(parseErr)
				}
				resp.Body.Close()
				resp, err = c.fetchMediaRedirectTarget(context.Background(), nil, target.String(), snapshot, nil, resp.Request)
			}
		default:
			resp, err = c.Stream(context.Background(), nil, nil, "/Videos/item/stream.mp4", nil)
		}
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if finalQuery != "part=1" {
			t.Fatal("external response extended upstream-credential delegation")
		}
	}
}

func TestMediaDelegationRejectsLocalSecretsAndTransportDowngrade(t *testing.T) {
	c := hopCustomClient(t, "https://up.test", true)
	for _, test := range []struct {
		name   string
		target string
		ctx    *RequestContext
	}{
		{"local-session", "https://gateway.test/file", &RequestContext{ProxyToken: hopUpstreamToken}},
		{"playback-capability", "https://gateway.test/file", &RequestContext{PlaybackCredential: hopUpstreamToken}},
		{"https-downgrade", "http://gateway.test/file", nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := httptest.NewRequest("GET", "https://up.test/Videos/item/stream", nil)
			source.Header.Set("X-Emby-Token", hopUpstreamToken)
			target, _ := url.Parse(test.target + "?api_key=" + hopUpstreamToken + "&part=1")
			p := c.mediaRedirectProvenance(nil, source, target, test.ctx, true)
			if err := prepareHopQueryWithProvenance(target, false, hopUpstreamToken, p, hopUpstreamToken); err != nil {
				t.Fatal(err)
			}
			if target.RawQuery != "part=1" {
				t.Fatal("ineligible media URL preserved a private credential")
			}
		})
	}
}
