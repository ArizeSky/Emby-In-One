package backend

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSeasonsRouteEachSameServerCopyToItsOwnPhysicalParent(t *testing.T) {
	var episodeReads atomic.Int32
	withReviewUpstreams(t, func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		if len(parts) >= 4 && parts[1] == "Shows" {
			series := parts[2]
			season := "season-" + series
			switch parts[3] {
			case "Seasons":
				writeJSON(w, 200, map[string]any{"Items": []any{map[string]any{"Id": season, "Type": "Season", "SeriesId": series, "IndexNumber": 1}}, "TotalRecordCount": 1})
				return
			case "Episodes":
				if r.URL.Query().Get("SeasonId") != season {
					t.Errorf("series %s received another copy's season: %s", series, r.URL.Query().Get("SeasonId"))
				}
				episodeReads.Add(1)
				writeJSON(w, 200, map[string]any{"Items": []any{map[string]any{"Id": "episode-" + series, "Type": "Episode", "SeriesId": series, "SeasonId": season, "ParentIndexNumber": 1, "IndexNumber": 1}}, "TotalRecordCount": 1})
				return
			}
		}
		http.NotFound(w, r)
	}, func(app *App, h http.Handler, token string) {
		sid := app.Upstream.Clients()[0].ID
		series := app.IDStore.GetOrCreateVirtualID("series-main", sid)
		app.IDStore.AssociateAdditionalInstance(series, "series-alt", sid)
		rr := doJSONRequest(t, h, "GET", "/Shows/"+series+"/Seasons", nil, token)
		var page map[string]any
		if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &page) != nil || len(asItems(page)) != 1 {
			t.Fatalf("seasons: %d %s", rr.Code, rr.Body.String())
		}
		season := itemID(asItems(page)[0])
		rr = doJSONRequest(t, h, "GET", "/Shows/"+series+"/Episodes?SeasonId="+season, nil, token)
		if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &page) != nil || len(asItems(page)) != 1 || episodeReads.Load() != 2 {
			t.Fatalf("episodes: %d reads=%d %s", rr.Code, episodeReads.Load(), rr.Body.String())
		}
		episode := itemID(asItems(page)[0])
		if len(app.IDStore.PhysicalInstances(episode)) != 2 {
			t.Fatal("merged episode lost a physical copy")
		}
	})
}

func TestNextUpUsesVerifiedSeriesAliasesAndRejectsStalePhysicalPair(t *testing.T) {
	var reads atomic.Int32
	withReviewUpstreams(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Episodes") {
			if strings.Contains(r.URL.Path, "stale-series") {
				t.Errorf("queried a series ID belonging to another server: %s", r.URL.Path)
			}
			reads.Add(1)
			series := strings.Split(r.URL.Path, "/")[2]
			writeJSON(w, 200, map[string]any{"Items": []any{
				map[string]any{"Id": "ep6-" + series, "Type": "Episode", "SeriesId": series, "ParentIndexNumber": 1, "IndexNumber": 6, "OfficialRating": "G"},
				map[string]any{"Id": "ep7-" + series, "Type": "Episode", "SeriesId": series, "ParentIndexNumber": 1, "IndexNumber": 7, "OfficialRating": "G"},
			}, "TotalRecordCount": 2})
			return
		}
		http.NotFound(w, r)
	}, func(app *App, h http.Handler, admin string) {
		sid := app.Upstream.Clients()[0].ID
		token := createRouteAccessUser(t, h, admin, "next-copy", []string{sid})
		user := app.UserStore.GetByUsername("next-copy")
		old := app.IDStore.GetOrCreateVirtualID("series-old", sid)
		series := app.IDStore.GetOrCreateVirtualID("series-current", sid)
		app.IDStore.AssociateAdditionalInstance(series, "series-old", sid)
		for i, s := range []string{old, series} {
			item := app.IDStore.GetOrCreateVirtualID([]string{"ep5-old", "ep6-current"}[i], sid)
			if err := app.WatchStore.RecordProgress(&WatchProgress{ProxyUserID: user.ID, VirtualItemID: item, ServerID: sid, OriginalItemID: []string{"ep5-old", "ep6-current"}[i], ItemType: "Episode", SeriesVirtualID: s, SeriesOriginalID: "stale-series", SeriesName: []string{"Old title", "Current title"}[i], ParentIndexNumber: 1, IndexNumber: 5 + i, Played: true}); err != nil {
				t.Fatal(err)
			}
		}
		for _, path := range []string{"/Shows/NextUp", "/Shows/NextUp?SeriesId=" + old} {
			rr := doJSONRequest(t, h, "GET", path, nil, token)
			var payload map[string]any
			if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &payload) != nil || len(asItems(payload)) != 1 {
				t.Fatalf("NextUp: %d %s", rr.Code, rr.Body.String())
			}
			n, _ := numericInt(asItems(payload)[0]["IndexNumber"])
			if n != 7 {
				t.Fatalf("next episode=%d", n)
			}
		}
		if reads.Load() != 4 {
			t.Fatalf("expected two physical parents per request, reads=%d", reads.Load())
		}
	})
}

func TestNextEpisodeUsesCoordinatesInsteadOfUpstreamTitleOrder(t *testing.T) {
	rows := []map[string]any{
		{"ParentIndexNumber": 1, "IndexNumber": 10},
		{"ParentIndexNumber": 1, "IndexNumber": 6},
		{"ParentIndexNumber": 1, "IndexNumber": 1},
		{"ParentIndexNumber": 1, "IndexNumber": 7},
	}
	last := &WatchProgress{ParentIndexNumber: 1, IndexNumber: 6, Played: true}
	candidates := nextEpisodeCandidates(rows, last)
	if len(candidates) != 2 || candidates[0]["IndexNumber"] != 7 || candidates[1]["IndexNumber"] != 10 {
		t.Fatalf("wrong successor order: %#v", candidates)
	}
}
