package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func finalReviewServer(t *testing.T) *httptest.Server {
	t.Helper()
	adult := map[string]any{"Id": "adult", "Name": "SECRET-R", "Type": "Movie", "OfficialRating": "R"}
	episode := map[string]any{"Id": "episode", "Name": "EPISODE-OF-R", "Type": "Episode", "SeriesId": "series"}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "up-token", "User": map[string]any{"Id": "up-user"}})
		case r.URL.Path == "/Search/Hints":
			_ = json.NewEncoder(w).Encode(map[string]any{"SearchHints": []any{adult}, "TotalRecordCount": 1})
		case strings.HasSuffix(r.URL.Path, "/ThemeMedia"):
			_ = json.NewEncoder(w).Encode(map[string]any{"ThemeVideosResult": map[string]any{"Items": []any{adult}, "TotalRecordCount": 1}})
		case strings.HasSuffix(r.URL.Path, "/Similar") || strings.HasSuffix(r.URL.Path, "/Episodes") || strings.HasSuffix(r.URL.Path, "/Seasons"):
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": []any{adult}, "TotalRecordCount": 1})
		case strings.HasSuffix(r.URL.Path, "/Items/episode"):
			_ = json.NewEncoder(w).Encode(episode)
		case strings.HasSuffix(r.URL.Path, "/Items/series"):
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "series", "Type": "Series", "OfficialRating": "R"})
		case strings.HasSuffix(r.URL.Path, "/Items/adult"):
			_ = json.NewEncoder(w).Encode(adult)
		case strings.HasPrefix(r.URL.Path, "/Videos/"):
			_, _ = w.Write([]byte("VIDEO"))
		case strings.HasSuffix(r.URL.Path, "/Items"):
			// Typical sparse list payload: rating not requested/delivered.
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": []any{map[string]any{"Id": "adult", "Name": "SECRET-R", "Type": "Movie"}, episode}, "TotalRecordCount": 2})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFinalReviewParentalReadRoutes(t *testing.T) {
	up := finalReviewServer(t)
	withTempAppConfig(t, upstreamsConfigYAML(upstreamEntryYAML("A", up.URL, false)), func(app *App, h http.Handler) {
		u, tok := review23User(t, app)
		sid := app.Upstream.Clients()[0].ID
		parent := app.IDStore.GetOrCreateVirtualID("parent", sid)
		episode := app.IDStore.GetOrCreateVirtualID("episode", sid)
		for _, tc := range []struct {
			name, path string
			want       int
		}{
			{"sparse-root-list", "/Users/" + u.ID + "/Items", 200},
			{"search-hints", "/Search/Hints?SearchTerm=SECRET", 200},
			{"similar", "/Items/" + parent + "/Similar", 200},
			{"theme-media", "/Items/" + parent + "/ThemeMedia", 200},
			{"seasons", "/Shows/" + parent + "/Seasons", 200},
			{"episodes", "/Shows/" + parent + "/Episodes", 200},
			{"episode-detail-inherits", "/Items/" + episode, 403},
		} {
			t.Run(tc.name, func(t *testing.T) {
				rr := doJSONRequest(t, h, http.MethodGet, tc.path, nil, tok)
				if rr.Code != tc.want {
					t.Errorf("status=%d want=%d", rr.Code, tc.want)
				}
				if rr.Code == 200 && (strings.Contains(rr.Body.String(), "SECRET-R") || strings.Contains(rr.Body.String(), "EPISODE-OF-R")) {
					t.Errorf("restricted metadata still returned on %s: %s", tc.name, rr.Body.String())
				}
			})
		}
		t.Run("stream-inherits-correctly", func(t *testing.T) {
			rr := doJSONRequest(t, h, http.MethodGet, "/Videos/"+episode+"/stream.mkv", nil, tok)
			if rr.Code != 403 {
				t.Errorf("status=%d want=403", rr.Code)
			}
		})
	})
}

func TestFinalReviewMergedDetailMustDropDeniedSource(t *testing.T) {
	makeUp := func(rating string, delay time.Duration) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/Users/AuthenticateByName" {
				_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "up-token", "User": map[string]any{"Id": "up-user"}})
				return
			}
			time.Sleep(delay)
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "film", "Name": "FILM", "Type": "Movie", "OfficialRating": rating, "MediaSources": []any{map[string]any{"Id": "source-" + rating, "Name": "SOURCE-" + rating}}})
		}))
	}
	a := makeUp("PG", 0)
	defer a.Close()
	b := makeUp("R", 30*time.Millisecond)
	defer b.Close()
	withTempAppConfig(t, dualUpstreamConfig(a.URL, b.URL), func(app *App, h http.Handler) {
		u, tok := review23User(t, app)
		id := app.IDStore.GetOrCreateVirtualID("film", app.Upstream.Clients()[0].ID)
		app.IDStore.AssociateAdditionalInstance(id, "film", app.Upstream.Clients()[1].ID)
		rr := doJSONRequest(t, h, http.MethodGet, "/Users/"+u.ID+"/Items/"+id, nil, tok)
		if rr.Code != 200 {
			t.Fatalf("status=%d", rr.Code)
		}
		if strings.Contains(rr.Body.String(), "SOURCE-R") {
			t.Errorf("denied R source disclosed through allowed PG detail: %s", rr.Body.String())
		}
	})
}

func TestFinalReviewActiveHLSKeepsUnreadTail(t *testing.T) {
	s := newStreamResourceStore()
	now := time.Unix(1700000000, 0)
	s.now = func() time.Time { return now }
	entry := streamResourceEntry{UserID: "u", VirtualItemID: "v", ServerID: "s", OriginalID: "o", ResourceURL: "https://cdn/head"}
	first, _ := s.put(entry)
	entry.ResourceURL = "https://cdn/tail"
	tail, _ := s.put(entry)
	for _, minute := range []int{10, 20, 29, 39} {
		now = time.Unix(1700000000, 0).Add(time.Duration(minute) * time.Minute)
		if _, ok := s.get(first); !ok {
			t.Fatal("active resource expired")
		}
	}
	if _, ok := s.get(tail); !ok {
		t.Fatal("unread tail expired after 30m despite continuously active same playback session")
	}
}

func TestFinalReviewHLSManifestRegistrationAtomic(t *testing.T) {
	app := &App{StreamResources: newStreamResourceStore()}
	app.StreamResources.max = 1
	_, err := app.RewriteM3U8ForItemWithResources(strings.Join([]string{"#EXTM3U", "a.ts", "b.ts"}, string(rune(10))), "https://cdn/master.m3u8", "tok", resourceRewriteContext{UserID: "u", VirtualItemID: "v", ServerID: "s", OriginalID: "o"})
	if err == nil {
		t.Fatal("capacity failure expected")
	}
	if got := app.StreamResources.size(); got != 0 {
		t.Errorf("failed manifest leaked %d reserved handles; want atomic rollback", got)
	}
}

func TestFinalReviewHLSRepeatedManifestReusesHandles(t *testing.T) {
	app := &App{StreamResources: newStreamResourceStore()}
	ctx := resourceRewriteContext{UserID: "u", VirtualItemID: "v", ServerID: "s", OriginalID: "o"}
	content := strings.Join([]string{"#EXTM3U", "a.ts", "b.ts"}, string(rune(10)))
	first, err := app.RewriteM3U8ForItemWithResources(content, "https://cdn/master.m3u8", "tok", ctx)
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.RewriteM3U8ForItemWithResources(content, "https://cdn/master.m3u8", "tok", ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || app.StreamResources.size() != 2 {
		t.Errorf("identical manifest allocated new handles: size=%d want 2", app.StreamResources.size())
	}
}

func TestFinalReviewFullResourceMapReturns503(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "up-token", "User": map[string]any{"Id": "up-user"}})
		case strings.Contains(r.URL.Path, "/Items/film"):
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "film", "Type": "Movie", "OfficialRating": "PG"})
		case strings.HasPrefix(r.URL.Path, "/Videos/"):
			w.Header().Set("Content-Type", "application/x-mpegurl")
			_, _ = w.Write([]byte(strings.Join([]string{"#EXTM3U", "a.ts", "b.ts"}, string(rune(10)))))
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	withTempAppConfig(t, upstreamsConfigYAML(upstreamEntryYAML("A", up.URL, false)), func(app *App, h http.Handler) {
		_, tok := review23User(t, app)
		id := app.IDStore.GetOrCreateVirtualID("film", app.Upstream.Clients()[0].ID)
		app.StreamResources.max = 1
		rr := doJSONRequest(t, h, http.MethodGet, "/Videos/"+id+"/master.m3u8", nil, tok)
		if rr.Code != 503 || rr.Header().Get("Retry-After") == "" {
			t.Errorf("status=%d Retry-After=%q, want 503 + retry guidance", rr.Code, rr.Header().Get("Retry-After"))
		}
	})
}

func TestFinalReviewPausedImageSourceNeverFetched(t *testing.T) {
	a := newAggUpstreamStub(t)
	b := newAggUpstreamStub(t)
	withTempAppConfig(t, dualUpstreamConfig(a.server.URL, b.server.URL), func(app *App, h http.Handler) {
		tok := loginToken(t, h, "secret")
		aid, bid := app.Upstream.Clients()[0].ID, app.Upstream.Clients()[1].ID
		id := app.IDStore.GetOrCreateVirtualID("movie-a", aid)
		app.IDStore.AssociateAdditionalInstance(id, "movie-b", bid)
		app.IDStore.SetDisplaySource(id, bid, "movie-b")
		app.IDStore.SetImageSource(id, "Primary", "poster-b", bid, "movie-b")
		disableAggregation(t, h, tok, bid)
		for _, auth := range []string{"", tok} {
			b.reset()
			rr := doJSONRequest(t, h, http.MethodGet, "/Items/"+id+"/Images/Primary?tag=poster-b", nil, auth)
			if hits := b.contentHits.Load(); hits != 0 {
				t.Errorf("paused image source queried %d time(s), status=%d", hits, rr.Code)
			}
		}
	})
}

func TestFinalReviewRedirectRejectsUnconfiguredPlaintext(t *testing.T) {
	observed := make(chan string, 1)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		observed <- r.Header.Get("X-Emby-Token")
		w.WriteHeader(200)
	}))
	defer up.Close()
	c := &UpstreamClient{BaseURL: strings.Replace(up.URL, "http://", "https://", 1), AccessToken: "up-secret", transport: http.DefaultTransport}
	resp, err := c.fetchRedirectTarget(context.Background(), up.URL+"/file", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if got := <-observed; got != "" {
		t.Error("upstream credential sent to unconfigured HTTP origin sharing HTTPS host:port")
	}
}

func TestFinalReviewRedirectConcurrentAuth(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer up.Close()
	c := &UpstreamClient{BaseURL: up.URL, transport: http.DefaultTransport}
	c.setOnline("token-a", "u")
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100000; i++ {
			c.setOnline(fmt.Sprint(i), "u")
		}
	}()
	for i := 0; i < 10; i++ {
		resp, err := c.fetchRedirectTarget(context.Background(), up.URL+"/file", nil)
		if err == nil {
			resp.Body.Close()
		}
	}
	wg.Wait()
}

func TestFinalReviewKnownGroupKeepsWatchAliases(t *testing.T) {
	up := finalReviewServer(t)
	withTempAppConfig(t, dualUpstreamConfig(up.URL, up.URL), func(app *App, h http.Handler) {
		u, _ := review23User(t, app)
		aid, bid := app.Upstream.Clients()[0].ID, app.Upstream.Clients()[1].ID
		va := app.IDStore.GetOrCreateVirtualID("a", aid)
		vb := app.IDStore.GetOrCreateVirtualID("b", bid)
		if err := app.WatchStore.SetFavorite(u.ID, vb, true); err != nil {
			t.Fatal(err)
		}
		merged := app.mergeRoundRobinItems([]upstreamItemsResult{
			{ServerID: aid, Items: []map[string]any{windowMovie("a", "123", "Film", 2020)}},
			{ServerID: bid, Items: []map[string]any{windowMovie("b", "123", "Film", 2020)}},
		}, u.ID)
		if len(merged) != 1 {
			t.Fatalf("items=%d", len(merged))
		}
		gotID, _ := merged[0]["Id"].(string)
		if gotID != va {
			t.Fatalf("unexpected fixture canonical")
		}
		merged[0]["UserData"] = map[string]any{"IsFavorite": false}
		req := httptest.NewRequest("GET", "/", nil)
		req = req.WithContext(context.WithValue(req.Context(), requestContextKey{}, &RequestContext{ProxyUser: &tokenInfo{Role: "user", UserID: u.ID}}))
		app.overlayLocalUserData(req, gotID, merged[0])
		data, _ := merged[0]["UserData"].(map[string]any)
		if favorite, _ := data["IsFavorite"].(bool); !favorite {
			t.Errorf("pre-existing favorite under alias vanished from canonical DTO: %v", data)
		}
	})
}

func TestFinalReviewAggregateGlobalDeadline(t *testing.T) {
	app := &App{}
	started := time.Now()
	_, report := app.aggregateUpstreamsWithReport(context.Background(), aggregationConfig{globalTimeout: 20 * time.Millisecond}, []upstreamTask{
		{index: 0, fn: func(ctx context.Context) upstreamItemsResult {
			time.Sleep(120 * time.Millisecond)
			return upstreamItemsResult{Complete: true}
		}},
	})
	if elapsed := time.Since(started); elapsed > 80*time.Millisecond {
		t.Errorf("global deadline 20ms did not bound aggregate: elapsed=%v report=%+v", elapsed, report)
	}
}

func TestFinalReviewCompleteBatchDoesNotClaimIncomplete(t *testing.T) {
	up := review23Upstream("PG", false)
	defer up.Close()
	withTempAppConfig(t, upstreamsConfigYAML(upstreamEntryYAML("A", up.URL, false)), func(app *App, h http.Handler) {
		tok := loginToken(t, h, "secret")
		id := app.IDStore.GetOrCreateVirtualID("film", app.Upstream.Clients()[0].ID)
		rr := doJSONRequest(t, h, http.MethodGet, "/Items?Ids="+id, nil, tok)
		if rr.Code != 200 {
			t.Fatalf("status=%d", rr.Code)
		}
		if rr.Header().Get("X-EIO-Results-Incomplete") != "" {
			t.Errorf("successful complete single batch falsely marked incomplete: %s", rr.Body.String())
		}
	})
}
