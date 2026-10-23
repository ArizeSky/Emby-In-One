package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func reviewUpstream() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "up-token", "User": map[string]any{"Id": "up-user"}})
		case strings.HasPrefix(r.URL.Path, "/Videos/") || strings.HasPrefix(r.URL.Path, "/Audio/"):
			w.Write([]byte(r.URL.Path))
		case r.URL.Path == "/Items/Prefixes":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]any{map[string]any{"Name": "A", "Value": r.URL.Query().Get("ParentId")}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": []any{map[string]any{"Id": "movie", "Name": r.URL.Query().Get("GenreIds")}}, "TotalRecordCount": 1})
		}
	}))
}

func TestPRReview22SubtitleAndAttachment(t *testing.T) {
	up := reviewUpstream()
	defer up.Close()
	withTempAppConfig(t, dualUpstreamConfig(up.URL, up.URL), func(app *App, h http.Handler) {
		token := loginToken(t, h, "secret")
		id := app.IDStore.GetOrCreateVirtualID("real-item", app.Upstream.Clients()[0].ID)
		for _, tail := range []string{"Subtitles/0/Stream.srt", "Attachments/0/font.ttf"} {
			rr := doJSONRequest(t, h, http.MethodGet, "/Videos/"+id+"/mediasource_"+id+"/"+tail, nil, token)
			want := "/Videos/real-item/mediasource_real-item/" + tail
			if rr.Code != 200 || rr.Body.String() != want {
				t.Errorf("got status=%d body=%s, want %s", rr.Code, rr.Body.String(), want)
			}
		}
		if got := resolveMediaSourceInPath("mediasource_unknown/Subtitles/0/Stream.srt", app.IDStore); got != "mediasource_unknown/Subtitles/0/Stream.srt" {
			t.Error("unknown id changed")
		}
	})
}

func TestPRReview20LoginFormats(t *testing.T) {
	up := reviewUpstream()
	defer up.Close()
	withTempAppConfig(t, dualUpstreamConfig(up.URL, up.URL), func(app *App, h http.Handler) {
		for _, tc := range []struct {
			name, body string
			status     int
			web        bool
		}{
			{"JSON", `{"Username":"admin","Pw":"secret"}`, 200, false},
			{"form", "username=admin&pw=secret", 200, false},
			{"form-alias", "Username=admin&Password=secret", 200, false},
			{"wrong-password", "username=admin&pw=wrong", 401, false},
			{"malformed", "username=admin&pw=%zz", 400, false},
			{"web-admin", "username=admin&pw=secret", 403, true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", strings.NewReader(tc.body))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				if tc.web {
					req.Header.Set("X-EIO-Web-Client", "1")
				}
				rr := httptest.NewRecorder()
				h.ServeHTTP(rr, req)
				if rr.Code != tc.status {
					t.Errorf("status=%d want=%d", rr.Code, tc.status)
				}
			})
		}
	})
}

func TestPRReview24TokenMustNotReachUpstream(t *testing.T) {
	for _, key := range []string{"X-Emby-Token", "x-emby-token", "X-EMBY-TOKEN"} {
		t.Run(key, func(t *testing.T) {
			got, _, err := prepareURLForTest(t, "http://up.test/Items", url.Values{key: {"LOCAL-SECRET"}}, fixtureRequestContext(fixtureAliceID), fixtureAuthSnapshot(), testPolicy("/Items", http.MethodGet, authModeNormal))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(got, "LOCAL-SECRET") {
				t.Error("local query token remains in final upstream URL")
			}
		})
	}
}

func TestPRReview24LowercaseRoutesAndQueryAuth(t *testing.T) {
	up := reviewUpstream()
	defer up.Close()
	withTempAppConfig(t, dualUpstreamConfig(up.URL, up.URL), func(app *App, h http.Handler) {
		token := loginToken(t, h, "secret")
		id := app.IDStore.GetOrCreateVirtualID("real-item", app.Upstream.Clients()[0].ID)
		for _, root := range []string{"videos", "audio"} {
			rr := doJSONRequest(t, h, http.MethodGet, "/emby/"+root+"/"+id+"/stream.mkv?X-Emby-Token="+token, nil, "")
			if rr.Code != 200 || strings.Contains(rr.Body.String(), id) {
				t.Errorf("%s status=%d body=%s", root, rr.Code, rr.Body.String())
			}
			rr = doJSONRequest(t, h, http.MethodGet, "/"+root+"/"+id+"/stream.mkv", nil, "")
			if rr.Code != 401 {
				t.Errorf("unauthenticated %s status=%d", root, rr.Code)
			}
		}
	})
}

func TestPRReview24GenreWithAndWithoutParent(t *testing.T) {
	up := reviewUpstream()
	defer up.Close()
	withTempAppConfig(t, dualUpstreamConfig(up.URL, up.URL), func(app *App, h http.Handler) {
		token := loginToken(t, h, "secret")
		genre := app.IDStore.GetOrCreateVirtualID("real-genre", app.Upstream.Clients()[0].ID)
		parent := app.IDStore.GetOrCreateVirtualID("real-library", app.Upstream.Clients()[0].ID)
		for _, tc := range []struct{ name, parent string }{{"with-parent", parent}, {"without-parent", ""}} {
			t.Run(tc.name, func(t *testing.T) {
				path := "/Users/" + app.Auth.ProxyUserID() + "/Items?GenreIds=" + genre
				if tc.parent != "" {
					path += "&ParentId=" + tc.parent
				}
				rr := doJSONRequest(t, h, http.MethodGet, path, nil, token)
				if rr.Code != 200 {
					t.Fatalf("status=%d", rr.Code)
				}
				var payload map[string]any
				_ = json.Unmarshal(rr.Body.Bytes(), &payload)
				for _, item := range asItems(payload) {
					if item["Name"] != "real-genre" {
						t.Error("upstream received untranslated virtual genre id")
					}
				}
			})
		}
	})
}

func TestPRReview24PrefixesMustTranslateParent(t *testing.T) {
	up := reviewUpstream()
	defer up.Close()
	withTempAppConfig(t, dualUpstreamConfig(up.URL, up.URL), func(app *App, h http.Handler) {
		token := loginToken(t, h, "secret")
		parent := app.IDStore.GetOrCreateVirtualID("real-library", app.Upstream.Clients()[0].ID)
		rr := doJSONRequest(t, h, http.MethodGet, "/Items/Prefixes?ParentId="+parent, nil, token)
		if rr.Code != 200 {
			t.Fatalf("status=%d", rr.Code)
		}
		var entries []map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &entries)
		if len(entries) == 0 {
			t.Fatal("missing prefixes")
		}
		if entries[0]["Value"] != "real-library" {
			t.Error("prefix endpoint forwarded virtual ParentId")
		}
	})
}
