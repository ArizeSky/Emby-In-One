package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestIntegrationReviewMetadataMustKeepChineseOverview(t *testing.T) {
	existing := map[string]any{"Overview": "这是已经选择的中文简介"}
	candidate := map[string]any{"Overview": "English", "ImageTags": map[string]any{"Primary": "poster-b"}}
	cfg := Config{Upstream: []UpstreamConfig{{ID: "a"}, {ID: "b"}}}
	if isBetterMetadata(existing, "a", candidate, "b", cfg) {
		t.Fatal("poster-bearing English DTO replaces Chinese metadata")
	}
}

func TestIntegrationReviewCustomRatingMustOverrideOfficial(t *testing.T) {
	item := map[string]any{"OfficialRating": "PG", "CustomRating": "R"}
	if itemAllowedByRating(item, 7) {
		t.Fatal("administrator's R CustomRating ignored in favour of PG OfficialRating")
	}
}

func TestIntegrationReviewImageAuthAndCacheBoundary(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "up-token", "User": map[string]any{"Id": "up-user"}})
		case strings.HasPrefix(r.URL.Path, "/Items/missing/Images/"):
			http.NotFound(w, r)
		case strings.HasPrefix(r.URL.Path, "/Items/good/Images/"):
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write([]byte("JPEG"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	withTempAppConfig(t, strmProxyConfig(up.URL, false), func(app *App, h http.Handler) {
		token := loginToken(t, h, "secret")
		good := app.IDStore.GetOrCreateVirtualID("good", app.Upstream.Clients()[0].ID)
		missing := app.IDStore.GetOrCreateVirtualID("missing", app.Upstream.Clients()[0].ID)
		for _, tc := range []struct {
			name, id, tok, cache string
			status               int
		}{
			{"invalid-token", good, "invalid", "no-store", 401},
			{"authenticated-success", good, token, "private, no-store", 200},
			{"anonymous-success", good, "", "public, max-age=86400", 200},
			{"anonymous-upstream-404", missing, "", "no-store", 404},
		} {
			t.Run(tc.name, func(t *testing.T) {
				rr := doJSONRequest(t, h, http.MethodGet, "/Items/"+tc.id+"/Images/Primary", nil, tc.tok)
				if rr.Code != tc.status || rr.Header().Get("Cache-Control") != tc.cache {
					t.Errorf("status=%d cache=%q, want status=%d cache=%q", rr.Code, rr.Header().Get("Cache-Control"), tc.status, tc.cache)
				}
			})
		}
	})
}

func TestIntegrationReviewImageRedirectLoopMustBe502(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Users/AuthenticateByName" {
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "up-token", "User": map[string]any{"Id": "up-user"}})
			return
		}
		w.Header().Set("Location", r.URL.Path)
		w.WriteHeader(http.StatusFound)
	}))
	defer up.Close()
	withTempAppConfig(t, strmProxyConfig(up.URL, true), func(app *App, h http.Handler) {
		id := app.IDStore.GetOrCreateVirtualID("film", app.Upstream.Clients()[0].ID)
		rr := doJSONRequest(t, h, http.MethodGet, "/Items/"+id+"/Images/Primary", nil, "")
		if rr.Code != 502 || rr.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("redirect loop status=%d location=%q cache=%q, want 502/no-store", rr.Code, rr.Header().Get("Location"), rr.Header().Get("Cache-Control"))
		}
	})
}

func TestIntegrationReviewCrossSourcePosterActuallyDelivered(t *testing.T) {
	makeUp := func(poster bool) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/Users/AuthenticateByName" {
				_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "up-token", "User": map[string]any{"Id": "up-user"}})
				return
			}
			if strings.Contains(r.URL.Path, "/Images/") && poster {
				w.Header().Set("Content-Type", "image/jpeg")
				_, _ = w.Write([]byte("POSTER-B"))
				return
			}
			http.NotFound(w, r)
		}))
	}
	a := makeUp(false)
	defer a.Close()
	b := makeUp(true)
	defer b.Close()
	withTempAppConfig(t, twoUpstreamConfig(a.URL, b.URL), func(app *App, h http.Handler) {
		token := loginToken(t, h, "secret")
		first := windowMovie("a-film", "123", "Same film", 2020)
		first["Overview"] = "中文简介"
		second := windowMovie("b-film", "123", "Same film", 2020)
		second["Overview"] = "English"
		second["ImageTags"] = map[string]any{"Primary": "poster-b"}
		got := app.mergeRoundRobinItems([]upstreamItemsResult{
			{ServerID: app.Upstream.Clients()[0].ID, Items: []map[string]any{first}},
			{ServerID: app.Upstream.Clients()[1].ID, Items: []map[string]any{second}},
		}, app.Auth.ProxyUserID())
		if len(got) != 1 {
			t.Fatalf("got %d items", len(got))
		}
		id, _ := got[0]["Id"].(string)
		rr := doJSONRequest(t, h, http.MethodGet, "/Items/"+id+"/Images/Primary?tag=poster-b", nil, token)
		if rr.Code != 200 || rr.Body.String() != "POSTER-B" {
			t.Errorf("chosen poster not delivered: status=%d body=%q", rr.Code, rr.Body.String())
		}
	})
}

func TestIntegrationReviewPagedCatalogMustNotLoseToGrace(t *testing.T) {
	fast := (&narrowWindowServer{maxWindow: 5000, movies: []map[string]any{windowMovie("fast", "1", "Fast movie", 2020)}}).start(t, "user-a")
	movies := make([]map[string]any, 250)
	for i := range movies {
		movies[i] = windowMovie(fmt.Sprint(i), fmt.Sprint(1000+i), fmt.Sprintf("Slow movie %d", i), 2020)
	}
	n := &narrowWindowServer{maxWindow: 200, movies: movies}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Users/AuthenticateByName" {
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "up-token", "User": map[string]any{"Id": "user-b"}})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/Items") {
			time.Sleep(40 * time.Millisecond)
			n.serveItems(w, r)
			return
		}
		http.NotFound(w, r)
	}))
	defer slow.Close()
	cfg := strings.Replace(twoUpstreamConfigPaged(fast.URL, slow.URL, false, true), "  global: 15000", "  global: 1000\n  searchGracePeriod: 20", 1)
	withTempAppConfig(t, cfg, func(app *App, h http.Handler) {
		token := loginToken(t, h, "secret")
		rr := doJSONRequest(t, h, http.MethodGet, "/Users/"+app.Auth.ProxyUserID()+"/Items?Limit=500", nil, token)
		var payload any
		_ = json.Unmarshal(rr.Body.Bytes(), &payload)
		if rr.Code != 200 || len(asItems(payload)) != 251 {
			t.Errorf("paged catalog discarded by grace: status=%d items=%d, want 200 with 251 items", rr.Code, len(asItems(payload)))
		}
		time.Sleep(200 * time.Millisecond)
	})
}

func TestIntegrationReviewTokenActuallyObservedByUpstream(t *testing.T) {
	observed := make(chan string, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Users/AuthenticateByName" {
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "up-token", "User": map[string]any{"Id": "up-user"}})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/Videos/") {
			observed <- r.URL.Query().Get("X-Emby-Token")
			_, _ = w.Write([]byte("VIDEO"))
			return
		}
		http.NotFound(w, r)
	}))
	defer up.Close()
	withTempAppConfig(t, upstreamsConfigYAML(upstreamEntryYAML("A", up.URL, false)), func(app *App, h http.Handler) {
		token := loginToken(t, h, "secret")
		id := app.IDStore.GetOrCreateVirtualID("film", app.Upstream.Clients()[0].ID)
		rr := doJSONRequest(t, h, http.MethodGet, "/Videos/"+id+"/stream.mkv?X-Emby-Token="+token, nil, "")
		if rr.Code != 200 {
			t.Fatalf("status=%d", rr.Code)
		}
		if got := <-observed; got != "" {
			t.Error("real upstream received the local token in X-Emby-Token query")
		}
	})
}

func TestIntegrationReviewCrossSourceSubtitleMustUseActualItem(t *testing.T) {
	up := reviewUpstream()
	defer up.Close()
	withTempAppConfig(t, dualUpstreamConfig(up.URL, up.URL), func(app *App, h http.Handler) {
		token := loginToken(t, h, "secret")
		a, b := app.Upstream.Clients()[0].ID, app.Upstream.Clients()[1].ID
		item := app.IDStore.GetOrCreateVirtualID("item-a", a)
		app.IDStore.AssociateAdditionalInstance(item, "item-b", b)
		source := app.IDStore.GetOrCreateVirtualID("source-b", b)
		rr := doJSONRequest(t, h, http.MethodGet, "/Videos/"+item+"/mediasource_"+item+"/Subtitles/0/Stream.srt?MediaSourceId="+source, nil, token)
		want := "/Videos/item-b/mediasource_item-b/Subtitles/0/Stream.srt"
		if rr.Code != 200 || rr.Body.String() != want {
			t.Errorf("status=%d path=%q, want %q", rr.Code, rr.Body.String(), want)
		}
	})
}

func TestIntegrationReviewMergedDetailMustRespectRating(t *testing.T) {
	up := review23Upstream("R", false)
	defer up.Close()
	withTempAppConfig(t, dualUpstreamConfig(up.URL, up.URL), func(app *App, h http.Handler) {
		user, token := review23User(t, app)
		id := app.IDStore.GetOrCreateVirtualID("film", app.Upstream.Clients()[0].ID)
		app.IDStore.AssociateAdditionalInstance(id, "film", app.Upstream.Clients()[1].ID)
		rr := doJSONRequest(t, h, http.MethodGet, "/Users/"+user.ID+"/Items/"+id, nil, token)
		if rr.Code != 403 {
			t.Errorf("merged R-rated detail status=%d, want 403", rr.Code)
		}
	})
}
