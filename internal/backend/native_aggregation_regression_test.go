package backend

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestSameServerSelectedCopyIsPreserved(t *testing.T) {
	withReviewUpstreams(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/PlaybackInfo") {
			if r.URL.Path != "/Items/item-alt/PlaybackInfo" {
				t.Errorf("selected alternate version reached %s; want /Items/item-alt/PlaybackInfo", r.URL.Path)
			}
			writeJSON(w, 200, map[string]any{"PlaySessionId": "play-alt", "MediaSources": []any{map[string]any{"Id": "mediasource_item-alt", "Container": "mkv"}}})
			return
		}
		if strings.Contains(r.URL.Path, "/Subtitles/") {
			if r.URL.Path != "/Videos/item-alt/mediasource_item-alt/Subtitles/2/0/Stream.ass" {
				t.Errorf("selected alternate subtitle reached %s; want item-alt + its own source", r.URL.Path)
			}
			_, _ = w.Write([]byte("[Script Info]\nTitle: alternate\n"))
			return
		}
		writeJSON(w, 200, map[string]any{"OfficialRating": "G"})
	}, func(app *App, handler http.Handler, token string) {
		server := app.Upstream.Clients()[0].ID
		item := app.IDStore.GetOrCreateVirtualID("item-main", server)
		app.IDStore.AssociateAdditionalInstance(item, "item-alt", server)
		source := app.IDStore.GetOrCreateVirtualID("mediasource_item-alt", server)
		rr := doJSONRequest(t, handler, "POST", "/Items/"+item+"/PlaybackInfo", map[string]any{"MediaSourceId": source}, token)
		if rr.Code != 200 {
			t.Fatalf("playback status=%d", rr.Code)
		}
		rr = doJSONRequest(t, handler, "GET", "/Videos/"+item+"/"+source+"/Subtitles/2/0/Stream.ass", nil, token)
		if rr.Code != 200 {
			t.Fatalf("subtitle status=%d", rr.Code)
		}
	})
}

func TestResumeFoldsVerifiedEpisodeAliases(t *testing.T) {
	withReviewUpstreams(t, func(w http.ResponseWriter, r *http.Request) {
		rows := []any{}
		for _, id := range strings.Split(r.URL.Query().Get("Ids"), ",") {
			if id == "" {
				continue
			}
			series, name, n := "series-new", "New title", 6
			if id == "ep1-old" {
				series, name, n = "series-old", "Old title", 1
			}
			if id == "ep-other" {
				series, name, n = "unrelated-series", "New title", 2
			}
			if id == "ep1-new" {
				n = 1
			}
			rows = append(rows, map[string]any{"Id": id, "Type": "Episode", "SeriesId": series, "SeriesName": name, "Name": "Episode", "IndexNumber": n, "ParentIndexNumber": 1, "OfficialRating": "G", "RunTimeTicks": 1000000})
		}
		writeJSON(w, 200, map[string]any{"Items": rows, "TotalRecordCount": len(rows)})
	}, func(app *App, h http.Handler, admin string) {
		sid := app.Upstream.Clients()[0].ID
		token := createRouteAccessUser(t, h, admin, "resume-audit", []string{sid})
		proxy := app.UserStore.GetByUsername("resume-audit")
		if proxy == nil {
			t.Fatal("user token missing")
		}
		seriesOld := app.IDStore.GetOrCreateVirtualID("series-old", sid)
		seriesNew := app.IDStore.GetOrCreateVirtualID("series-new", sid)
		app.IDStore.AssociateAdditionalInstance(seriesNew, "series-old", sid)
		epOld := app.IDStore.GetOrCreateVirtualID("ep1-old", sid)
		epNew := app.IDStore.GetOrCreateVirtualID("ep1-new", sid)
		app.IDStore.AssociateAdditionalInstance(epNew, "ep1-old", sid)
		ep6 := app.IDStore.GetOrCreateVirtualID("ep6", sid)
		otherSeries := app.IDStore.GetOrCreateVirtualID("unrelated-series", sid)
		otherEpisode := app.IDStore.GetOrCreateVirtualID("ep-other", sid)
		for _, p := range []*WatchProgress{
			{ProxyUserID: proxy.ID, VirtualItemID: epOld, ServerID: sid, OriginalItemID: "ep1-old", ItemType: "Episode", SeriesVirtualID: seriesOld, SeriesName: "Old title", PositionTicks: 100000, RuntimeTicks: 1000000, IndexNumber: 1, ParentIndexNumber: 1},
			{ProxyUserID: proxy.ID, VirtualItemID: epNew, ServerID: sid, OriginalItemID: "ep1-new", ItemType: "Episode", SeriesVirtualID: seriesNew, SeriesName: "New title", Played: true, RuntimeTicks: 1000000, IndexNumber: 1, ParentIndexNumber: 1},
			{ProxyUserID: proxy.ID, VirtualItemID: otherEpisode, ServerID: sid, OriginalItemID: "ep-other", ItemType: "Episode", SeriesVirtualID: otherSeries, SeriesName: "New title", PositionTicks: 100000, RuntimeTicks: 1000000, IndexNumber: 2, ParentIndexNumber: 1},
			{ProxyUserID: proxy.ID, VirtualItemID: ep6, ServerID: sid, OriginalItemID: "ep6", ItemType: "Episode", SeriesVirtualID: seriesNew, SeriesName: "New title", PositionTicks: 100000, RuntimeTicks: 1000000, IndexNumber: 6, ParentIndexNumber: 1},
		} {
			if err := app.WatchStore.RecordProgress(p); err != nil {
				t.Fatal(err)
			}
		}
		rr := doJSONRequest(t, h, "GET", "/Users/"+proxy.ID+"/Items/Resume", nil, token)
		var payload map[string]any
		if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &payload) != nil {
			t.Fatalf("resume status=%d", rr.Code)
		}
		rows := asItems(payload)
		if len(rows) != 2 {
			t.Fatalf("resume returned %d entries; want current episode and unrelated same-title series", len(rows))
		}
		seen := map[string]bool{}
		for _, row := range rows {
			seen[itemID(row)] = true
		}
		if !seen[ep6] || !seen[otherEpisode] {
			t.Fatalf("wrong resume items: %#v", seen)
		}
		rr = doJSONRequest(t, h, "GET", "/Users/"+proxy.ID+"/Items/Resume?StartIndex=1&Limit=1", nil, token)
		if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &payload) != nil || len(asItems(payload)) != 1 || !seen[itemID(asItems(payload)[0])] {
			t.Fatalf("resume pagination: %d %s", rr.Code, rr.Body.String())
		}
	})
}
func TestSlowShowReplicaDoesNotStarveHealthyReplica(t *testing.T) {
	var slow atomic.Bool
	var healthy atomic.Int32
	withReviewUpstreams(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/Episodes") {
			if r.Header.Get("Test-Server") == "a" && slow.Load() {
				<-r.Context().Done()
				return
			}
			healthy.Add(1)
			writeJSON(w, 200, map[string]any{"Items": []any{map[string]any{"Id": "episode-b", "Type": "Episode", "SeriesId": "series-b", "ParentIndexNumber": 1, "IndexNumber": 7}}, "TotalRecordCount": 1})
			return
		}
		writeJSON(w, 200, map[string]any{})
	}, func(app *App, h http.Handler, token string) {
		clients := app.Upstream.Clients()
		series := app.IDStore.GetOrCreateVirtualID("series-a", clients[0].ID)
		app.IDStore.AssociateAdditionalInstance(series, "series-b", clients[1].ID)
		slow.Store(true)
		ctx, cancel := context.WithTimeout(context.Background(), 180*time.Millisecond)
		defer cancel()
		req := httptest.NewRequest("GET", "/Shows/"+series+"/Episodes", nil).WithContext(ctx)
		req.Header.Set("X-Emby-Token", token)
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, req)
		var payload map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &payload)
		if rr.Code != 200 || len(asItems(payload)) == 0 || healthy.Load() == 0 {
			t.Errorf("slow primary starved healthy copy: status=%d healthy requests=%d confirmed episodes=%d", rr.Code, healthy.Load(), len(asItems(payload)))
		}
	})
}
