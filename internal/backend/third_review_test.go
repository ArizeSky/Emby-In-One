package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func thirdReviewPayload(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode status %d body %s: %v", rr.Code, rr.Body.String(), err)
	}
	return payload
}

func thirdReviewUpstream(t *testing.T, fn func(http.ResponseWriter, *http.Request)) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Users/AuthenticateByName" {
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "fixture-up-token", "User": map[string]any{"Id": "up-user"}})
			return
		}
		fn(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

func TestThirdReviewConfirmedUnratedRemainsVisible(t *testing.T) {
	item := map[string]any{"Id": "unrated", "Name": "Allowed unrated movie", "Type": "Movie"}
	up := thirdReviewUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Items/unrated") {
			_ = json.NewEncoder(w).Encode(item)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Items": []any{item}, "TotalRecordCount": 1})
	})
	withTempAppConfig(t, upstreamsConfigYAML(upstreamEntryYAML("A", up.URL, false)), func(a *App, h http.Handler) {
		u, tok := review23User(t, a)
		if u.BlockUnratedItems {
			t.Fatal("fixture unexpectedly blocks unrated items")
		}
		rr := doJSONRequest(t, h, "GET", "/Users/"+u.ID+"/Items", nil, tok)
		if rr.Code != 200 || len(asItems(thirdReviewPayload(t, rr))) != 1 {
			t.Errorf("confirmed unrated movie hidden with BlockUnratedItems=false: status=%d body=%s", rr.Code, rr.Body.String())
		}
	})
}

func TestThirdReviewCommonParentIsMemoizedAndComplete(t *testing.T) {
	var parentCalls atomic.Int32
	items := make([]any, 80)
	for i := range items {
		items[i] = map[string]any{"Id": fmt.Sprint("ep", i), "Name": fmt.Sprint("Episode ", i), "Type": "Episode", "SeriesId": "series", "ParentIndexNumber": 1, "IndexNumber": i + 1}
	}
	up := thirdReviewUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Items/series") {
			parentCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "series", "Type": "Series", "OfficialRating": "PG"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Items": items, "TotalRecordCount": 80})
	})
	withTempAppConfig(t, upstreamsConfigYAML(upstreamEntryYAML("A", up.URL, false)), func(a *App, h http.Handler) {
		u, tok := review23User(t, a)
		rr := doJSONRequest(t, h, "GET", "/Users/"+u.ID+"/Items", nil, tok)
		payload := thirdReviewPayload(t, rr)
		if got := len(asItems(payload)); got != 80 {
			t.Errorf("one shared PG parent should confirm all 80 episodes, got=%d parent requests=%d status=%d incomplete=%q", got, parentCalls.Load(), rr.Code, rr.Header().Get("X-EIO-Results-Incomplete"))
		}
		if got := parentCalls.Load(); got != 1 {
			t.Errorf("same parent fetched %d times instead of memoized once", got)
		}
	})
}

func TestThirdReviewUnconfirmedCatalogMustNotClaimCompleteEmpty(t *testing.T) {
	up := thirdReviewUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Items" || strings.HasSuffix(r.URL.Path, "/Items/sparse") {
			w.WriteHeader(500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Items": []any{map[string]any{"Id": "sparse", "Name": "Unknown", "Type": "Movie"}}, "TotalRecordCount": 1})
	})
	withTempAppConfig(t, upstreamsConfigYAML(upstreamEntryYAML("A", up.URL, false)), func(a *App, h http.Handler) {
		u, tok := review23User(t, a)
		rr := doJSONRequest(t, h, "GET", "/Users/"+u.ID+"/Items", nil, tok)
		if rr.Code != 502 {
			t.Errorf("all necessary rating lookups failed, status=%d body=%s incomplete=%q; want 502", rr.Code, rr.Body.String(), rr.Header().Get("X-EIO-Results-Incomplete"))
		}
	})
}

func TestThirdReviewUnrestrictedParentPaginationKeepsTotal(t *testing.T) {
	up := thirdReviewUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"Items": []any{map[string]any{"Id": "a", "Type": "Movie"}, map[string]any{"Id": "b", "Type": "Movie"}}, "TotalRecordCount": 100, "StartIndex": 0})
	})
	withTempAppConfig(t, upstreamsConfigYAML(upstreamEntryYAML("A", up.URL, false)), func(a *App, h http.Handler) {
		tok := loginToken(t, h, "secret")
		parent := a.IDStore.GetOrCreateVirtualID("library", a.Upstream.Clients()[0].ID)
		rr := doJSONRequest(t, h, "GET", "/Users/"+a.ConfigStore.Snapshot().Server.ID+"/Items?ParentId="+parent+"&Limit=2", nil, tok)
		payload := thirdReviewPayload(t, rr)
		total, _ := numericInt(payload["TotalRecordCount"])
		if rr.Code != 200 || total != 100 {
			t.Errorf("unrestricted page total changed: status=%d total=%d body=%s", rr.Code, total, rr.Body.String())
		}
	})
}

func TestThirdReviewLocalWatchRoutesRespectNewPolicy(t *testing.T) {
	adult := map[string]any{"Id": "adult", "Name": "RESTRICTED-WATCH-HISTORY", "Type": "Movie", "OfficialRating": "R"}
	episode := map[string]any{"Id": "next", "Name": "RESTRICTED-NEXT-UP", "Type": "Episode", "OfficialRating": "R", "SeriesId": "series", "ParentIndexNumber": 1, "IndexNumber": 2}
	up := thirdReviewUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Episodes") {
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": []any{episode}, "TotalRecordCount": 1})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/Items/series") {
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "series", "Type": "Series", "OfficialRating": "R"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Items": []any{adult}, "TotalRecordCount": 1})
	})
	withTempAppConfig(t, upstreamsConfigYAML(upstreamEntryYAML("A", up.URL, false)), func(a *App, h http.Handler) {
		u, tok := review23User(t, a)
		sid := a.Upstream.Clients()[0].ID
		movie := a.IDStore.GetOrCreateVirtualID("adult", sid)
		series := a.IDStore.GetOrCreateVirtualID("series", sid)
		old := a.IDStore.GetOrCreateVirtualID("old", sid)
		for _, p := range []*WatchProgress{
			{ProxyUserID: u.ID, VirtualItemID: movie, ServerID: sid, OriginalItemID: "adult", ItemType: "Movie", PositionTicks: 10, RuntimeTicks: 1000},
			{ProxyUserID: u.ID, VirtualItemID: old, ServerID: sid, OriginalItemID: "old", ItemType: "Episode", SeriesVirtualID: series, SeriesOriginalID: "series", SeriesName: "Restricted Series", ParentIndexNumber: 1, IndexNumber: 1, Played: true},
		} {
			if err := a.WatchStore.RecordProgress(p); err != nil {
				t.Fatal(err)
			}
		}
		for _, tc := range []struct{ name, path string }{
			{"resume", "/Users/" + u.ID + "/Items/Resume"},
			{"next-up", "/Shows/NextUp?SeriesId=" + series},
		} {
			t.Run(tc.name, func(t *testing.T) {
				rr := doJSONRequest(t, h, "GET", tc.path, nil, tok)
				if strings.Contains(rr.Body.String(), "RESTRICTED-") {
					t.Errorf("new parental restriction ignored by %s: status=%d body=%s", tc.name, rr.Code, rr.Body.String())
				}
			})
		}
	})
}

func TestThirdReviewAllowedSiblingSurvivesDisplayPriority(t *testing.T) {
	makeUp := func(rating, overview string) *httptest.Server {
		return thirdReviewUpstream(t, func(w http.ResponseWriter, r *http.Request) {
			item := windowMovie("film", "123", "Same Film", 2020)
			item["OfficialRating"], item["Overview"] = rating, overview
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": []any{item}, "TotalRecordCount": 1})
		})
	}
	aUp, bUp := makeUp("PG", "English overview"), makeUp("R", "中文优选资料")
	withTempAppConfig(t, dualUpstreamConfig(aUp.URL, bUp.URL), func(a *App, h http.Handler) {
		u, tok := review23User(t, a)
		rr := doJSONRequest(t, h, "GET", "/Users/"+u.ID+"/Items", nil, tok)
		if got := len(asItems(thirdReviewPayload(t, rr))); rr.Code != 200 || got != 1 {
			t.Errorf("allowed PG copy vanished because denied R copy won display metadata: status=%d body=%s", rr.Code, rr.Body.String())
		}
	})
}

func TestThirdReviewFilterPreservesOrder(t *testing.T) {
	items := []any{map[string]any{"Id": "first", "Name": "First", "Type": "Movie"}, map[string]any{"Id": "second", "Name": "Second", "Type": "Movie", "OfficialRating": "PG"}}
	up := thirdReviewUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("Ids") != "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": []any{map[string]any{"Id": "first", "Type": "Movie", "OfficialRating": "PG"}}, "TotalRecordCount": 1})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Items": items, "TotalRecordCount": 2})
	})
	withTempAppConfig(t, upstreamsConfigYAML(upstreamEntryYAML("A", up.URL, false)), func(a *App, h http.Handler) {
		u, tok := review23User(t, a)
		rr := doJSONRequest(t, h, "GET", "/Users/"+u.ID+"/Items?SortBy=SortName", nil, tok)
		got := asItems(thirdReviewPayload(t, rr))
		if len(got) != 2 {
			t.Fatalf("items=%d body=%s", len(got), rr.Body.String())
		}
		if got[0]["Name"] != "First" {
			t.Errorf("filter moved sparse DTO after known DTO: got=%v then %v", got[0]["Name"], got[1]["Name"])
		}
	})
}

func TestThirdReviewHLSQueryDistinctWindowsStayIndependent(t *testing.T) {
	a := &App{StreamResources: newStreamResourceStore()}
	now := time.Unix(1700000000, 0)
	a.StreamResources.now = func() time.Time { return now }
	ctx := resourceRewriteContext{UserID: "u", VirtualItemID: "v", ServerID: "s", OriginalID: "o"}
	first, err := a.RewriteM3U8ForItemWithResources("#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:1\naudio.ts", "https://cdn/live.m3u8?track=audio", "", ctx)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimPrefix(strings.Split(first, "\n")[2], "/Videos/v/eio-resource/")
	_, err = a.RewriteM3U8ForItemWithResources("#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:1\nvideo.ts", "https://cdn/live.m3u8?track=video", "", ctx)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(streamResourceCatchupGrace + time.Second)
	if _, ok := a.StreamResources.peek(id); !ok {
		t.Error("current audio window retired by a different track's playlist sharing only the URL path")
	}
}

func TestThirdReviewMergedEpisodeUsesInheritedAllowedRating(t *testing.T) {
	up := thirdReviewUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Items/series") {
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "series", "Type": "Series", "OfficialRating": "PG"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": "episode", "Name": "Allowed inherited episode", "Type": "Episode", "SeriesId": "series"})
	})
	withTempAppConfig(t, dualUpstreamConfig(up.URL, up.URL), func(a *App, h http.Handler) {
		u, tok := review23User(t, a)
		if err := a.UserStore.SetParentalPolicy(u.ID, 7, true); err != nil {
			t.Fatal(err)
		}
		clients := a.Upstream.Clients()
		id := a.IDStore.GetOrCreateVirtualID("episode", clients[0].ID)
		a.IDStore.AssociateAdditionalInstance(id, "episode", clients[1].ID)
		rr := doJSONRequest(t, h, "GET", "/Users/"+u.ID+"/Items/"+id, nil, tok)
		if rr.Code != 200 {
			t.Errorf("PG parent was confirmed allowed, but final merged DTO rejected its inherited rating: status=%d body=%s", rr.Code, rr.Body.String())
		}
	})
}

func TestThirdReviewExplicitUnknownRatingCannotInheritToAllowStream(t *testing.T) {
	up := thirdReviewUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Items/series"):
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "series", "Type": "Series", "OfficialRating": "PG"})
		case strings.HasSuffix(r.URL.Path, "/Items/episode"):
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "episode", "Type": "Episode", "CustomRating": "EXPLICIT-UNKNOWN", "OfficialRating": "PG", "SeriesId": "series"})
		case strings.HasPrefix(r.URL.Path, "/Videos/"):
			w.Header().Set("Content-Type", "video/mp4")
			_, _ = w.Write([]byte("RESTRICTED-VIDEO-BYTES"))
		default:
			http.NotFound(w, r)
		}
	})
	withTempAppConfig(t, upstreamsConfigYAML(upstreamEntryYAML("A", up.URL, false)), func(a *App, h http.Handler) {
		u, tok := review23User(t, a)
		if err := a.UserStore.SetParentalPolicy(u.ID, 7, true); err != nil {
			t.Fatal(err)
		}
		id := a.IDStore.GetOrCreateVirtualID("episode", a.Upstream.Clients()[0].ID)
		rr := doJSONRequest(t, h, "GET", "/Videos/"+id+"/stream.mp4", nil, tok)
		if rr.Code != 403 {
			t.Errorf("explicit unknown CustomRating was overridden by PG parent despite BlockUnratedItems=true: status=%d body=%s", rr.Code, rr.Body.String())
		}
	})
}
