package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestParentalSessionPartialAndDeniedLists(t *testing.T) {
	up := thirdReviewUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Items" || strings.HasSuffix(r.URL.Path, "/Items/missing") {
			w.WriteHeader(500)
			return
		}
		items := []any{map[string]any{"Id": "good", "Type": "Movie", "OfficialRating": "PG"}, map[string]any{"Id": "missing", "Type": "Movie"}}
		if r.URL.Query().Get("SearchTerm") == "denied" {
			items = []any{map[string]any{"Id": "adult", "Type": "Movie", "OfficialRating": "R"}}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Items": items, "TotalRecordCount": len(items)})
	})
	withTempAppConfig(t, upstreamsConfigYAML(upstreamEntryYAML("A", up.URL, false)), func(a *App, h http.Handler) {
		u, token := review23User(t, a)
		rr := doJSONRequest(t, h, "GET", "/Users/"+u.ID+"/Items", nil, token)
		payload := thirdReviewPayload(t, rr)
		if rr.Code != 200 || len(asItems(payload)) != 1 || payload["EioIncompleteResults"] != true || rr.Header().Get("X-EIO-Results-Incomplete") != "true" {
			t.Fatalf("unsafe partial response: %d %s", rr.Code, rr.Body.String())
		}
		rr = doJSONRequest(t, h, "GET", "/Users/"+u.ID+"/Items?SearchTerm=denied", nil, token)
		if rr.Code != 200 || len(asItems(thirdReviewPayload(t, rr))) != 0 || rr.Header().Get("X-EIO-Results-Incomplete") != "" {
			t.Fatalf("confirmed denies must produce a complete empty response: %d %s", rr.Code, rr.Body.String())
		}
	})
}

func TestParentalSessionConcurrentMemoBudgetAndOwnership(t *testing.T) {
	var calls atomic.Int32
	up := thirdReviewUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/Items/") {
			calls.Add(1)
		}
		id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		_ = json.NewEncoder(w).Encode(map[string]any{"Id": id, "Type": "Series", "OfficialRating": "PG"})
	})
	withTempAppConfig(t, dualUpstreamConfig(up.URL, up.URL), func(a *App, _ http.Handler) {
		clients := a.Upstream.Clients()
		s := newParentalReadSession(7, true)
		r := httptest.NewRequest("GET", "/", nil)
		r = r.WithContext(context.WithValue(r.Context(), requestContextKey{}, &RequestContext{Parental: s, Headers: http.Header{}}))
		var wg sync.WaitGroup
		for i := 0; i < 32; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := a.loadParentalMetadata(r, s, clients[0], "parent"); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		if calls.Load() != 1 {
			t.Fatalf("concurrent parent lookups = %d, want 1", calls.Load())
		}
		if _, err := a.loadParentalMetadata(r, s, clients[1], "parent"); err != nil {
			t.Fatal(err)
		}
		if calls.Load() != 2 {
			t.Fatal("metadata shared across concrete upstream clients")
		}
		for i := 0; i < parentalConfirmRequestBudget+5; i++ {
			_, _ = a.loadParentalMetadata(r, s, clients[0], fmt.Sprint("item", i))
		}
		if calls.Load() != parentalConfirmRequestBudget {
			t.Fatalf("request budget escaped: %d calls", calls.Load())
		}
		if _, err := a.loadParentalMetadata(r, newParentalReadSession(7, true), clients[0], "parent"); err != nil {
			t.Fatal(err)
		}
		if calls.Load() != parentalConfirmRequestBudget+1 {
			t.Fatal("metadata leaked into another HTTP session")
		}
	})
}

func TestParentalSessionNavigationAndUnknownOverride(t *testing.T) {
	s := newParentalReadSession(7, true)
	a := &App{}
	r := httptest.NewRequest("GET", "/", nil)
	if got := a.parentalTargetDecision(r, s, parentalTarget{item: map[string]any{"Type": "CollectionFolder"}}); got != parentalAllow {
		t.Fatalf("navigation hidden: %v", got)
	}
	if got := a.parentalTargetDecision(r, s, parentalTarget{item: map[string]any{"Type": "Episode", "CustomRating": "UNKNOWN", "OfficialRating": "PG", "SeriesId": "parent"}}); got != parentalDeny {
		t.Fatalf("unknown override ignored: %v", got)
	}
}

func TestParentalSessionNestedListsShareBudget(t *testing.T) {
	var calls atomic.Int32
	up := thirdReviewUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/ThemeMedia") {
			payload := map[string]any{}
			for group, key := range []string{"ThemeVideosResult", "ThemeSongsResult", "SoundtrackSongsResult"} {
				items := []any{}
				if group == 0 {
					items = append(items, map[string]any{"Id": "safe", "Type": "Movie", "OfficialRating": "PG"})
				}
				for i := 0; i < 20; i++ {
					items = append(items, map[string]any{"Id": fmt.Sprintf("g%d-item%d", group, i), "Type": "Movie"})
				}
				payload[key] = map[string]any{"Items": items, "TotalRecordCount": len(items)}
			}
			_ = json.NewEncoder(w).Encode(payload)
			return
		}
		if r.URL.Path == "/Items" || strings.Contains(r.URL.Path, "/Items/") {
			calls.Add(1)
		}
		w.WriteHeader(500)
	})
	withTempAppConfig(t, upstreamsConfigYAML(upstreamEntryYAML("A", up.URL, false)), func(a *App, h http.Handler) {
		_, token := review23User(t, a)
		id := a.IDStore.GetOrCreateVirtualID("parent", a.Upstream.Clients()[0].ID)
		rr := doJSONRequest(t, h, "GET", "/Items/"+id+"/ThemeMedia", nil, token)
		if rr.Code != 200 || thirdReviewPayload(t, rr)["EioIncompleteResults"] != true {
			t.Fatalf("nested partial response: %d %s", rr.Code, rr.Body.String())
		}
		if got := calls.Load(); got != parentalConfirmRequestBudget {
			t.Fatalf("nested lists made %d metadata requests, want shared budget %d", got, parentalConfirmRequestBudget)
		}
	})
}
