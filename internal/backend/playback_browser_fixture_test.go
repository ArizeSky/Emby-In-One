package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Opt-in test-only listener for an external Chromium driver. No application
// server is started. All state is t.TempDir and every upstream is httptest.
// EIO_IDENTITY_BROWSER_MEDIA points to an ffmpeg-generated synthetic MP4;
// EIO_IDENTITY_BROWSER_URL is a temporary output file for the loopback URL.
func TestStrictPlaybackBrowserFixture(t *testing.T) {
	media := os.Getenv("EIO_IDENTITY_BROWSER_MEDIA")
	urlFile := os.Getenv("EIO_IDENTITY_BROWSER_URL")
	if media == "" || urlFile == "" {
		t.Skip("opt-in isolated Chromium fixture")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	hits, events := 0, 0
	cleanups := 0
	token := "browser-token-a"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": token, "User": map[string]any{"Id": "browser-user"}})
		case strings.HasSuffix(r.URL.Path, "/PlaybackInfo"):
			if r.UserAgent() != "synthetic-identity-A" {
				t.Error("negotiation identity drifted")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"PlaySessionId": "browser-session", "MediaSources": []any{map[string]any{"Id": "browser-source", "Container": "mp4", "DirectStreamUrl": "/Videos/browser-item/stream.mp4?api_key=browser-token-a", "MediaStreams": []any{map[string]any{"Type": "Video", "Codec": "h264", "Index": 0}, map[string]any{"Type": "Audio", "Codec": "aac", "Index": 1}}}}})
		case r.URL.Path == "/Items/browser-item":
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "browser-item", "Type": "Movie", "OfficialRating": "G"})
		case r.URL.Path == "/Videos/browser-item/stream.mp4":
			hits++
			if r.UserAgent() != "synthetic-identity-A" || r.Header.Get("X-Emby-Device-Id") != "synthetic-device-A" || r.Header.Get("X-Emby-Token") != token {
				t.Error("browser media identity/auth drifted")
			}
			http.ServeFile(w, r, media)
		case r.Method == "DELETE" && r.URL.Path == "/Videos/ActiveEncodings":
			cleanups++
			if r.UserAgent() != "synthetic-identity-A" || r.Header.Get("X-Emby-Token") != token || r.URL.Query().Get("DeviceId") != "synthetic-device-A" || r.URL.Query().Get("PlaySessionId") != "browser-session" {
				t.Error("browser cleanup identity/auth drifted")
			}
			w.WriteHeader(204)
		case strings.HasPrefix(r.URL.Path, "/Sessions/Playing"):
			events++
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if strings.HasSuffix(r.URL.Path, "/Stopped") {
				for _, key := range []string{"IsPaused", "CanSeek", "PlayMethod", "EventName", "PlaybackRate"} {
					if _, present := body[key]; present {
						t.Errorf("Stopped carried progress field %s", key)
					}
				}
			} else {
				if _, ok := body["IsPaused"].(bool); !ok {
					t.Error("missing actual pause state")
				}
				if body["CanSeek"] != true || body["PlayMethod"] != "DirectStream" || body["IsMuted"] != true || body["VolumeLevel"] != float64(0) {
					t.Errorf("invalid measured playback state: %v", body)
				}
				if rate, ok := body["PlaybackRate"].(float64); !ok || rate != 1 && rate != 1.5 {
					t.Error("missing confirmed playback rate")
				}
			}
			if r.UserAgent() != "synthetic-identity-A" || r.Header.Get("X-Emby-Token") != token {
				t.Error("browser event identity/auth drifted")
			}
			w.WriteHeader(204)
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	withTempAppPrepared(t, playbackLimiterConfig(up.URL), func(dir string) {
		for _, asset := range []string{"web", "vendor"} {
			if err := os.Symlink(filepath.Join(root, "public", asset), filepath.Join(dir, "public", asset)); err != nil {
				t.Fatal(err)
			}
		}
	}, func(app *App, handler http.Handler, _ string) {
		local := createRegularUser(t, handler)
		client := app.Upstream.ClientByID(app.Upstream.Clients()[0].ID)
		client.mu.Lock()
		client.Config.SpoofClient = "passthrough"
		client.mu.Unlock()
		a := identitySnapshot{Source: "captured-latest", Headers: http.Header{"User-Agent": {"synthetic-identity-A"}, "X-Emby-Client": {"fixture"}, "X-Emby-Device-Id": {"synthetic-device-A"}}}
		app.Identity.SaveLatestCapturedHeaders(a.Headers)
		app.Identity.SaveLastSuccess(client.serverKey, a.Headers)
		client.setOnlineWithIdentity(token, "browser-user", a)
		item := app.IDStore.GetOrCreateVirtualID("browser-item", client.ID)
		outcome := make(chan string, 1)
		bridge := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
			w.Header().Set("Cross-Origin-Embedder-Policy", "require-corp")
			switch r.URL.Path {
			case "/fixture.html":
				html, err := os.ReadFile(filepath.Join(root, "tests/web-playback-identity-browser.html"))
				if err != nil {
					t.Error(err)
					w.WriteHeader(500)
					return
				}
				w.Header().Set("Content-Type", "text/html")
				_, _ = fmt.Fprint(w, strings.NewReplacer("__LOCAL_TOKEN__", local, "__ITEM_ID__", item).Replace(string(html)))
			case "/fixture/reset":
				app.Identity.SaveLatestCapturedHeaders(a.Headers)
				app.Identity.SaveLastSuccess(client.serverKey, a.Headers)
				mu.Lock()
				token = "browser-token-a"
				client.setOnlineWithIdentity(token, "browser-user", a)
				mu.Unlock()
				w.WriteHeader(204)
			case "/fixture/refresh":
				// Update the global capture to B while retaining compatible auth acquired
				// with A. Only new attempts may observe the new capture.
				b := http.Header{"User-Agent": {"synthetic-identity-B"}, "X-Emby-Client": {"fixture-B"}, "X-Emby-Device-Id": {"synthetic-device-B"}}
				app.Identity.SaveLatestCapturedHeaders(b)
				app.Identity.SaveLastSuccess(client.serverKey, b)
				mu.Lock()
				token = "browser-token-refreshed"
				client.setOnlineWithIdentity(token, "browser-user", a)
				mu.Unlock()
				w.WriteHeader(204)
			case "/fixture/report":
				select {
				case outcome <- r.URL.Query().Get("result"):
				default:
				}
				w.WriteHeader(204)
			default:
				handler.ServeHTTP(w, r)
			}
		})
		server := httptest.NewServer(bridge)
		defer server.Close()
		if err := os.WriteFile(urlFile, []byte(server.URL+"/fixture.html"), 0600); err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		deadline := time.After(150 * time.Second)
		for len(seen) < 3 {
			select {
			case result := <-outcome:
				if !strings.HasPrefix(result, "pass-") {
					t.Fatalf("Chromium synthetic fixture failed: %s", result)
				}
				seen[result] = true
			case <-deadline:
				t.Fatal("Chromium driver did not report all paths before bounded deadline")
			}
		}
		mu.Lock()
		defer mu.Unlock()
		if hits < 3 || events < 9 || cleanups != 3 {
			t.Fatalf("browser paths not exercised: media=%d events=%d cleanups=%d", hits, events, cleanups)
		}
	})
}
