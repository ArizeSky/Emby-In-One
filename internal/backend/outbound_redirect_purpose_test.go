package backend

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPasswordLoginSameOriginBodyAndFollowPolicy(t *testing.T) {
	for _, status := range []int{307, 308} {
		for _, follow := range []bool{true, false} {
			t.Run(http.StatusText(status)+map[bool]string{true: "/follow", false: "/stop"}[follow], func(t *testing.T) {
				hits := 0
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/Users/AuthenticateByName" {
						w.Header().Set("Location", "/login-complete")
						w.WriteHeader(status)
						return
					}
					hits++
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error("login body lost")
					}
					if r.Method != "POST" || body["Username"] != "fixture-user" || body["Pw"] != "fixture-password" {
						t.Error("same-origin login changed method/body")
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "fixture-token", "User": map[string]any{"Id": "fixture-user"}})
				}))
				defer up.Close()
				c := newUpstreamClient(Config{}, UpstreamConfig{URL: up.URL, Username: "fixture-user", Password: "fixture-password", SpoofClient: "custom", CustomUserAgent: "fixture-agent", FollowRedirects: follow}, 0, nil)
				c.Login(context.Background(), nil, nil)
				if c.IsOnline() != follow {
					t.Fatalf("online=%v follow=%v", c.IsOnline(), follow)
				}
				if follow && hits != 1 || !follow && hits != 0 {
					t.Fatalf("unexpected next-hop hits %d", hits)
				}
			})
		}
	}
}

func TestResourceMediaRedirectMethodsAndRange(t *testing.T) {
	for _, status := range []int{307, 308} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			t.Run(http.StatusText(status)+"/"+method, func(t *testing.T) {
				hits := 0
				cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					hits++
					if r.Method != method || r.Header.Get("Range") != "bytes=0-3" {
						t.Error("media redirect changed method/range")
					}
					if r.URL.RawQuery != "z=1&signature=a%2fb&x=a%20b" {
						t.Error("signed raw query changed")
					}
					if r.Header.Get("X-Emby-Token") != "" || r.Header.Get("X-Emby-Device-Id") != "" {
						t.Error("CDN got private identity")
					}
					w.Header().Set("Content-Length", "4")
					if method == http.MethodGet {
						_, _ = io.WriteString(w, "data")
					}
				}))
				defer cdn.Close()
				up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Location", cdn.URL+"/file?z=1&signature=a%2fb&x=a%20b")
					w.WriteHeader(status)
				}))
				defer up.Close()
				c := hopCustomClient(t, up.URL, true)
				req := httptest.NewRequest(method, "/local", nil)
				req.Header.Set("Range", "bytes=0-3")
				resp, err := (&App{}).fetchResourceFollowingRedirects(req, c, up.URL+"/Videos/item/stream.mp4")
				if err != nil {
					t.Fatal(err)
				}
				payload, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil || hits != 1 || method == http.MethodGet && string(payload) != "data" || method == http.MethodHead && len(payload) != 0 {
					t.Fatalf("method=%s hits=%d len=%d err=%v", method, hits, len(payload), err)
				}
			})
		}
	}
}

func TestPasswordLoginBodylessHopCannotReacquirePassword(t *testing.T) {
	var body string
	var method string
	var api *httptest.Server
	external := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Error("303 did not convert to GET")
		}
		http.Redirect(w, r, api.URL+"/complete", http.StatusTemporaryRedirect)
	}))
	defer external.Close()
	api = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/complete" {
			payload, _ := io.ReadAll(r.Body)
			body = string(payload)
			method = r.Method
			_, _ = io.WriteString(w, "{}")
			return
		}
		http.Redirect(w, r, external.URL+"/convert", http.StatusSeeOther)
	}))
	defer api.Close()
	c := hopCustomClient(t, api.URL, true)
	resp, err := c.doRequestForMode(context.Background(), nil, http.MethodPost, "/Users/AuthenticateByName", nil, map[string]any{"Username": "fixture-user", "Pw": "fixture-password"}, c.identityHeaders(nil, nil), false, authModePasswordLogin)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if method != http.MethodGet || strings.Contains(body, "fixture") || body != "" {
		t.Fatal("bodyless round-trip restored sensitive body")
	}
}
