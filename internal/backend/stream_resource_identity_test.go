package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// PlaybackInfo and media bytes must use the same configured identity. A server
// can accept metadata and reject the resulting stream when identity is lost.
func TestPlaybackResourceKeepsServingIdentity(t *testing.T) {
	const agent, device = "fixture-player/1", "fixture-device"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "fixture-upstream-token", "User": map[string]any{"Id": "fixture-user"}})
		case strings.HasSuffix(r.URL.Path, "/PlaybackInfo"):
			_ = json.NewEncoder(w).Encode(map[string]any{"MediaSources": []any{map[string]any{
				"Id": "source", "Container": "mkv", "DirectStreamUrl": "/Videos/original/stream.mkv?PlaySessionId=issued-session",
			}}})
		case strings.HasPrefix(r.URL.Path, "/Videos/"):
			if r.UserAgent() != agent || r.Header.Get("X-Emby-Device-Id") != device || r.Header.Get("X-Emby-Token") != "fixture-upstream-token" {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			if r.URL.Query().Get("PlaySessionId") != "issued-session" {
				t.Error("upstream-issued session was changed")
			}
			w.Header().Set("Content-Length", "4")
			if r.Method != http.MethodHead {
				w.Header().Set("Content-Range", "bytes 0-3/4")
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write([]byte("data"))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	config := singleUpstreamConfig(upstream.URL) + fmt.Sprintf("    spoofClient: custom\n    customUserAgent: %q\n    customDeviceId: %q\n", agent, device)
	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		client := app.Upstream.GetClient(0)
		item := app.IDStore.GetOrCreateVirtualID("original", client.ID)
		pi := doJSONRequest(t, handler, http.MethodGet, "/Items/"+item+"/PlaybackInfo", nil, token)
		if pi.Code != http.StatusOK {
			t.Fatalf("PlaybackInfo status=%d", pi.Code)
		}
		var payload struct {
			MediaSources []struct {
				DirectStreamURL string `json:"DirectStreamUrl"`
			} `json:"MediaSources"`
		}
		if err := json.Unmarshal(pi.Body.Bytes(), &payload); err != nil || len(payload.MediaSources) != 1 {
			t.Fatalf("invalid PlaybackInfo fixture response: %v", err)
		}
		resource := payload.MediaSources[0].DirectStreamURL
		// The ordinary route already preserves the identity. The new opaque
		// route must behave identically for both metadata HEAD and media GET.
		normal := doJSONRequest(t, handler, http.MethodGet, "/Videos/"+item+"/stream.mkv?PlaySessionId=issued-session", nil, token)
		if normal.Code != http.StatusPartialContent {
			t.Fatalf("ordinary stream status=%d", normal.Code)
		}
		for _, method := range []string{http.MethodHead, http.MethodGet} {
			req := httptest.NewRequest(method, resource, nil)
			req.Header.Set("Range", "bytes=0-3")
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)
			want := http.StatusPartialContent
			if method == http.MethodHead {
				want = http.StatusOK
			}
			if rr.Code != want {
				t.Errorf("resource %s status=%d, want %d (ordinary GET=%d)", method, rr.Code, want, normal.Code)
			}
		}
	})
}

func TestResourceRedirectIdentityAndCDNIsolation(t *testing.T) {
	for _, mode := range []string{"resident", "passthrough"} {
		t.Run(mode, func(t *testing.T) {
			identity := http.Header{
				"User-Agent": {"fixture-player/1"}, "X-Emby-Client": {"fixture-client"},
				"X-Emby-Device-Id": {"fixture-device"}, "X-Emby-Device-Name": {"fixture-device-name"},
				"X-Emby-Client-Version": {"1"},
			}
			cdnHits, selfHits := 0, 0
			cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				cdnHits++
				for _, name := range []string{"Authorization", "X-Emby-Authorization", "X-Emby-Token", "X-MediaBrowser-Token", "Cookie", "X-Emby-Device-Id", "X-Emby-Client"} {
					if r.Header.Get(name) != "" {
						t.Errorf("CDN received private header %s", name)
					}
				}
				if r.UserAgent() != identity.Get("User-Agent") || r.Header.Get("Range") != "bytes=0-3" || r.URL.RawQuery != "signature=fixture%2Fopaque&part=1" {
					t.Error("CDN lost public UA, Range or original signed query")
				}
				w.Header().Set("Content-Length", "4")
				if r.Method != http.MethodHead {
					w.Header().Set("Content-Range", "bytes 0-3/4")
					w.WriteHeader(http.StatusPartialContent)
					_, _ = io.WriteString(w, "data")
				}
			}))
			defer cdn.Close()
			var origin string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				selfHits++
				for name := range identity {
					if r.Header.Get(name) != identity.Get(name) {
						t.Errorf("self-origin lost %s in %s mode", name, mode)
					}
				}
				if r.Header.Get("X-Emby-Token") != "fixture-upstream-token" || !strings.Contains(r.Header.Get("X-Emby-Authorization"), "fixture-upstream-user") || strings.Contains(r.Header.Get("X-Emby-Authorization"), "local-proxy-token") {
					t.Error("self-origin did not receive canonical upstream authentication")
				}
				if r.URL.Path == "/Videos/original/stream.mkv" {
					w.Header().Set("Location", origin+"/sign?session=issued")
				} else {
					w.Header().Set("Location", cdn.URL+"/data?signature=fixture%2Fopaque&part=1")
				}
				w.WriteHeader(http.StatusFound)
			}))
			origin = upstream.URL
			defer upstream.Close()
			client := newUpstreamClient(Config{}, UpstreamConfig{ID: "fixture-server", URL: origin, SpoofClient: mode}, 0, nil)
			client.setOnline("fixture-upstream-token", "fixture-upstream-user")
			if mode == "resident" {
				client.residentIdentity = &residentIdentitySnapshot{Headers: identity}
			}
			service := NewClientIdentityService()
			service.SetCaptured("local-proxy-token", identity)
			app := &App{Identity: service}
			for _, method := range []string{http.MethodHead, http.MethodGet} {
				req := httptest.NewRequest(method, "/local-resource", nil)
				req.Header.Set("Range", "bytes=0-3")
				req = req.WithContext(context.WithValue(req.Context(), requestContextKey{}, &RequestContext{
					ProxyToken: "local-proxy-token", Headers: http.Header{"User-Agent": {"browser/1"}},
				}))
				resp, err := app.fetchResourceFollowingRedirects(req, client, origin+"/Videos/original/stream.mkv")
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				want := http.StatusOK
				if method == http.MethodGet {
					want = http.StatusPartialContent
				}
				if err != nil || resp.StatusCode != want || (method == http.MethodGet && string(body) != "data") || (method == http.MethodHead && len(body) != 0) {
					t.Errorf("redirected %s status=%d bytes=%d error=%v", method, resp.StatusCode, len(body), err)
				}
			}
			if selfHits != 4 || cdnHits != 2 {
				t.Errorf("unexpected redirect requests: self=%d CDN=%d", selfHits, cdnHits)
			}
		})
	}
}

func TestResourceMissingResidentIdentityDoesNotSendRequest(t *testing.T) {
	requests := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer upstream.Close()
	client := newUpstreamClient(Config{}, UpstreamConfig{URL: upstream.URL, SpoofClient: "resident"}, 0, nil)
	client.residentIdentity = nil
	_, err := client.fetchResourceRedirectTarget(context.Background(), http.MethodGet, client.BaseURL+"/media", nil)
	preparation, ok := asPreparationError(err)
	if !ok || preparation.Status != http.StatusServiceUnavailable || preparation.Kind != "resident-identity-missing" {
		t.Fatalf("missing identity did not fail before transport: %v", err)
	}
	if requests != 0 {
		t.Fatal("missing identity sent a request")
	}
}
