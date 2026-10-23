package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestParentalResumeMetadataKeepsServerOwnership(t *testing.T) {
	makeUp := func(name string) *httptest.Server {
		return thirdReviewUpstream(t, func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": []any{map[string]any{"Id": "same-id", "Name": name, "Type": "Movie", "OfficialRating": "PG"}}})
		})
	}
	one, two := makeUp("A"), makeUp("B")
	withTempAppConfig(t, dualUpstreamConfig(one.URL, two.URL), func(a *App, _ http.Handler) {
		_, token := review23User(t, a)
		clients := a.Upstream.Clients()
		records := []WatchProgress{}
		for _, client := range clients {
			records = append(records, WatchProgress{VirtualItemID: a.IDStore.GetOrCreateVirtualID("same-id", client.ID), ServerID: client.ID, OriginalItemID: "same-id", PositionTicks: 10})
		}
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("X-Emby-Token", token)
		a.withContext(func(_ http.ResponseWriter, r *http.Request) {
			rows := a.enrichWatchItems(r, requestContextFrom(r.Context()), records)
			if len(rows) != 2 || rows[0]["Name"] != "A" || rows[1]["Name"] != "B" {
				t.Fatalf("same upstream ID mixed metadata between servers: %v", rows)
			}
		})(httptest.NewRecorder(), r)
	})
}

func TestParentalResumeRefillsAfterDeniedPage(t *testing.T) {
	var batches atomic.Int32
	var fail atomic.Bool
	up := thirdReviewUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Items" {
			http.NotFound(w, r)
			return
		}
		batches.Add(1)
		if fail.Load() {
			w.WriteHeader(500)
			return
		}
		rows := []any{}
		for _, id := range strings.Split(r.URL.Query().Get("Ids"), ",") {
			rating := "R"
			if id == "allowed" {
				rating = "PG"
			}
			rows = append(rows, map[string]any{"Id": id, "Name": id, "Type": "Movie", "OfficialRating": rating})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Items": rows, "TotalRecordCount": len(rows)})
	})
	withTempAppConfig(t, upstreamsConfigYAML(upstreamEntryYAML("A", up.URL, false)), func(a *App, h http.Handler) {
		u, token := review23User(t, a)
		sid := a.Upstream.Clients()[0].ID
		for i := 0; i < 101; i++ {
			id := fmt.Sprint("adult", i)
			if i == 0 {
				id = "allowed"
			}
			vid := a.IDStore.GetOrCreateVirtualID(id, sid)
			if err := a.WatchStore.RecordProgress(&WatchProgress{ProxyUserID: u.ID, VirtualItemID: vid, ServerID: sid, OriginalItemID: id, ItemType: "Movie", PositionTicks: 10, RuntimeTicks: 1000}); err != nil {
				t.Fatal(err)
			}
		}
		if err := a.WatchStore.db.execParams("UPDATE user_watch_progress SET last_played = CASE WHEN original_item_id = 'allowed' THEN 1 ELSE 1000 END WHERE proxy_user_id = ?", u.ID); err != nil {
			t.Fatal(err)
		}
		rr := doJSONRequest(t, h, "GET", "/Users/"+u.ID+"/Items/Resume?Limit=1", nil, token)
		rows := asItems(thirdReviewPayload(t, rr))
		if rr.Code != 200 || len(rows) != 1 || rows[0]["Name"] != "allowed" || batches.Load() != 2 {
			t.Fatalf("resume did not refill beyond the first denied page: %d %s batches=%d", rr.Code, rr.Body.String(), batches.Load())
		}
		fail.Store(true)
		rr = doJSONRequest(t, h, "GET", "/Users/"+u.ID+"/Items/Resume?Limit=1", nil, token)
		if rr.Code != 502 {
			t.Fatalf("failed resume metadata disguised as empty history: %d %s", rr.Code, rr.Body.String())
		}
	})
}

func TestParentalNextUpRefillsAfterDeniedEpisode(t *testing.T) {
	up := thirdReviewUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/Episodes") {
			http.NotFound(w, r)
			return
		}
		rows := []any{}
		for i := 1; i <= 3; i++ {
			rating := "R"
			if i == 3 {
				rating = "PG"
			}
			rows = append(rows, map[string]any{"Id": fmt.Sprint("ep", i), "Name": fmt.Sprint("Episode", i), "Type": "Episode", "SeriesId": "series", "OfficialRating": rating, "ParentIndexNumber": 1, "IndexNumber": i})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Items": rows, "TotalRecordCount": len(rows)})
	})
	withTempAppConfig(t, upstreamsConfigYAML(upstreamEntryYAML("A", up.URL, false)), func(a *App, h http.Handler) {
		u, token := review23User(t, a)
		sid := a.Upstream.Clients()[0].ID
		series := a.IDStore.GetOrCreateVirtualID("series", sid)
		old := a.IDStore.GetOrCreateVirtualID("ep1", sid)
		if err := a.WatchStore.RecordProgress(&WatchProgress{ProxyUserID: u.ID, VirtualItemID: old, ServerID: sid, OriginalItemID: "ep1", ItemType: "Episode", SeriesVirtualID: series, SeriesOriginalID: "series", SeriesName: "Series", ParentIndexNumber: 1, IndexNumber: 1, Played: true}); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"/Shows/NextUp?SeriesId=" + series + "&Limit=1", "/Shows/NextUp?Limit=1"} {
			rr := doJSONRequest(t, h, "GET", path, nil, token)
			rows := asItems(thirdReviewPayload(t, rr))
			if rr.Code != 200 || len(rows) != 1 || rows[0]["Name"] != "Episode3" {
				t.Fatalf("next-up failed to refill: %s => %d %s", path, rr.Code, rr.Body.String())
			}
		}
	})
}

func TestParentalContainerFiltersBeforePagination(t *testing.T) {
	up := thirdReviewUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		rows := []any{}
		for i := 0; i < 4; i++ {
			rating := "R"
			if i >= 2 {
				rating = "PG"
			}
			rows = append(rows, map[string]any{"Id": fmt.Sprint("film", i), "Name": fmt.Sprint("Film", i), "Type": "Movie", "OfficialRating": rating})
		}
		start, _ := queryInt(r.URL.Query(), "StartIndex")
		limit, _ := queryInt(r.URL.Query(), "Limit")
		if start > len(rows) {
			start = len(rows)
		}
		end := len(rows)
		if limit > 0 {
			end = min(start+limit, end)
		}
		total := len(rows)
		if r.URL.Query().Get("SearchTerm") == "partial" {
			total = 100
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Items": rows[start:end], "TotalRecordCount": total, "StartIndex": start})
	})
	withTempAppConfig(t, upstreamsConfigYAML(upstreamEntryYAML("A", up.URL, false)), func(a *App, h http.Handler) {
		u, token := review23User(t, a)
		parent := a.IDStore.GetOrCreateVirtualID("library", a.Upstream.Clients()[0].ID)
		for _, suffix := range []string{"", "&SearchTerm=partial"} {
			rr := doJSONRequest(t, h, "GET", "/Users/"+u.ID+"/Items?ParentId="+parent+"&Limit=1&StartIndex=1"+suffix, nil, token)
			payload := thirdReviewPayload(t, rr)
			rows := asItems(payload)
			total, _ := numericInt(payload["TotalRecordCount"])
			if rr.Code != 200 || len(rows) != 1 || rows[0]["Name"] != "Film3" || total != 2 {
				t.Fatalf("incorrect filtered page: %d %s", rr.Code, rr.Body.String())
			}
			if suffix != "" && payload["EioIncompleteResults"] != true {
				t.Fatalf("truncated candidates claimed a complete total: %s", rr.Body.String())
			}
		}
	})
}
