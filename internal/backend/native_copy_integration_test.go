package backend

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

// Opaque source IDs deliberately contain no item ID: ownership must come from
// actual upstream DTOs, including two versions on one server.
func TestNativeOpaqueVersionsKeepMediaSubtitleSessionAndSeriesTogether(t *testing.T) {
	const subtitle = "[Events]\nDialogue: 0,0:00:02.00,0:00:04.00,Default,,0,0,0,,alternate release\n"
	var events, media atomic.Int32
	withReviewUpstreams(t, func(w http.ResponseWriter, r *http.Request) {
		item := "item-main"
		if strings.Contains(r.URL.Path, "item-alt") {
			item = "item-alt"
		}
		source := "opaque-" + item
		ms := map[string]any{"Id": source, "Container": "mkv", "DirectStreamUrl": "/Videos/" + item + "/stream.mkv?MediaSourceId=" + source + "&PlaySessionId=play-" + item,
			"MediaStreams": []any{map[string]any{"Type": "Subtitle", "Index": 2, "Codec": "ass", "DeliveryUrl": "/Videos/" + item + "/" + source + "/Subtitles/2/0/Stream.ass"}}}
		switch {
		case strings.HasSuffix(r.URL.Path, "/PlaybackInfo"):
			writeJSON(w, 200, map[string]any{"PlaySessionId": "play-" + item, "MediaSources": []any{ms}})
		case strings.Contains(r.URL.Path, "/Subtitles/"):
			if r.URL.Path != "/Videos/item-alt/opaque-item-alt/Subtitles/2/0/Stream.ass" || r.URL.Query().Get("MediaSourceId") != "opaque-item-alt" {
				t.Errorf("wrong subtitle copy: %s %s", r.URL.Path, r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(subtitle))
		case strings.HasPrefix(r.URL.Path, "/Videos/"):
			media.Add(1)
			if item != "item-alt" || r.URL.Query().Get("PlaySessionId") != "play-item-alt" {
				t.Errorf("wrong media copy/session: %s %s", r.URL.Path, r.URL.RawQuery)
			}
			_, _ = w.Write([]byte("alternate media"))
		case strings.HasPrefix(r.URL.Path, "/Sessions/Playing"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["ItemId"] != "item-alt" || body["MediaSourceId"] != "opaque-item-alt" || body["PlaySessionId"] != "play-item-alt" {
				t.Errorf("hybrid event: %#v", body)
			}
			events.Add(1)
			w.WriteHeader(204)
		case strings.Contains(r.URL.Path, "/PlayingItems/"):
			if !strings.HasSuffix(r.URL.Path, "/item-alt") || r.URL.Query().Get("PlaySessionId") != "play-item-alt" {
				t.Errorf("wrong PlayingItems target: %s %s", r.URL.Path, r.URL.RawQuery)
			}
			events.Add(1)
			w.WriteHeader(204)
		default:
			writeJSON(w, 200, map[string]any{"Id": item, "Type": "Episode", "OfficialRating": "G", "SeriesId": "series-" + item, "SeriesName": "Series", "ParentIndexNumber": 1, "IndexNumber": 6, "RunTimeTicks": 1000000, "MediaSources": []any{ms}})
		}
	}, func(app *App, h http.Handler, admin string) {
		sid := app.Upstream.Clients()[0].ID
		token := createRouteAccessUser(t, h, admin, "copy-viewer", []string{sid})
		user := app.UserStore.GetByUsername("copy-viewer")
		item := app.IDStore.GetOrCreateVirtualID("item-main", sid)
		app.IDStore.AssociateAdditionalInstance(item, "item-alt", sid)
		series := app.IDStore.GetOrCreateVirtualID("series-item-main", sid)
		app.IDStore.AssociateAdditionalInstance(series, "series-item-alt", sid)
		// Simulate a historical record whose item/server already switched while its
		// remembered series still belongs to a different upstream.
		if err := app.WatchStore.RecordProgress(&WatchProgress{ProxyUserID: user.ID, VirtualItemID: item, ServerID: sid, OriginalItemID: "item-alt", ItemType: "Episode", SeriesVirtualID: series, SeriesOriginalID: "unrelated-old-series", PositionTicks: 50, RuntimeTicks: 1000000}); err != nil {
			t.Fatal(err)
		}
		detail := doJSONRequest(t, h, "GET", "/Users/"+user.ID+"/Items/"+item, nil, token)
		var dto map[string]any
		if detail.Code != 200 || json.Unmarshal(detail.Body.Bytes(), &dto) != nil {
			t.Fatalf("detail: %d %s", detail.Code, detail.Body.String())
		}
		versions := asItems(map[string]any{"Items": dto["MediaSources"]})
		if len(versions) != 2 {
			t.Fatalf("versions=%d", len(versions))
		}
		alt := versions[1]
		source := alt["Id"].(string)
		delivery := asItems(map[string]any{"Items": alt["MediaStreams"]})[0]["DeliveryUrl"].(string)
		if rr := doJSONRequest(t, h, "GET", delivery, nil, token); rr.Code != 200 || rr.Body.String() != subtitle {
			t.Fatalf("detail subtitle: %d %s", rr.Code, rr.Body.String())
		}
		info := doJSONRequest(t, h, "GET", "/Items/"+item+"/PlaybackInfo", nil, token)
		if info.Code != 200 || json.Unmarshal(info.Body.Bytes(), &dto) != nil {
			t.Fatalf("negotiation: %d", info.Code)
		}
		publicSession := dto["PlaySessionId"].(string) // Base copy's public session, alt copy selected below.
		versions = asItems(map[string]any{"Items": dto["MediaSources"]})
		for _, path := range []string{versions[1]["DirectStreamUrl"].(string), "/Videos/" + item + "/stream.mkv?MediaSourceId=" + source + "&PlaySessionId=" + publicSession} {
			rr := doJSONRequest(t, h, "GET", path, nil, token)
			if rr.Code != 200 || rr.Body.String() != "alternate media" {
				t.Fatalf("media: %d %s", rr.Code, rr.Body.String())
			}
		}
		body := map[string]any{"ItemId": item, "MediaSourceId": source, "PlaySessionId": publicSession, "PositionTicks": 12345}
		for _, suffix := range []string{"", "/Progress", "/Stopped"} {
			if rr := doJSONRequest(t, h, "POST", "/Sessions/Playing"+suffix, body, token); rr.Code != 204 {
				t.Fatalf("event%s: %d %s", suffix, rr.Code, rr.Body.String())
			}
		}
		for _, method := range []string{"POST", "DELETE"} {
			rr := doJSONRequest(t, h, method, "/Users/"+user.ID+"/PlayingItems/"+item+"?MediaSourceId="+source+"&PlaySessionId="+publicSession, nil, token)
			if rr.Code != 204 {
				t.Fatalf("PlayingItems: %d %s", rr.Code, rr.Body.String())
			}
		}
		progress, err := app.WatchStore.GetProgress(user.ID, item)
		if err != nil || progress == nil || progress.OriginalItemID != "item-alt" || progress.SeriesOriginalID != "series-item-alt" {
			t.Fatalf("stale series metadata: %#v err=%v", progress, err)
		}
		if media.Load() != 2 || events.Load() != 5 {
			t.Fatalf("media=%d events=%d", media.Load(), events.Load())
		}
		// A session-only native subtitle request must recover an opaque source,
		// not fabricate mediasource_<item> when the selected session names it.
		rr := doJSONRequest(t, h, "GET", "/Items/"+item+"/PlaybackInfo?MediaSourceId="+source, nil, token)
		if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &dto) != nil {
			t.Fatalf("selected PI: %d", rr.Code)
		}
		rr = doJSONRequest(t, h, "GET", "/Videos/"+item+"/mediasource_"+item+"/Subtitles/2/0/Stream.ass?PlaySessionId="+dto["PlaySessionId"].(string), nil, token)
		if rr.Code != 200 || rr.Body.String() != subtitle {
			t.Fatalf("session-only opaque subtitle: %d %s", rr.Code, rr.Body.String())
		}
		for _, method := range []string{"POST", "DELETE"} {
			rr = doJSONRequest(t, h, method, "/Users/"+user.ID+"/PlayingItems/"+item+"?PlaySessionId="+dto["PlaySessionId"].(string), nil, token)
			if rr.Code != 204 {
				t.Fatalf("session-only PlayingItems: %d %s", rr.Code, rr.Body.String())
			}
		}
		if events.Load() != 7 {
			t.Fatalf("session-only PlayingItems not forwarded: events=%d", events.Load())
		}
		wrong := app.IDStore.GetOrCreateVirtualID("opaque-item-main", sid)
		conflict := "/Videos/" + item + "/" + wrong + "/Subtitles/2/0/Stream.ass?MediaSourceId=" + source
		if rr := doJSONRequest(t, h, "GET", conflict, nil, token); rr.Code != 400 {
			t.Fatalf("contradictory subtitle selectors: %d", rr.Code)
		}
	})
}

func TestResourceOwnershipSurvivesRestartAndServerRemoval(t *testing.T) {
	dir := t.TempDir()
	store, err := NewIDStore(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	item := store.GetOrCreateVirtualID("main", "server")
	store.AssociateAdditionalInstance(item, "alt", "server")
	source := store.GetOrCreateVirtualID("opaque", "server")
	store.RegisterResourceParent("source", "server", "opaque", "alt")
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewIDStore(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if got, ok := store.ResourceItem(item, store.ResolveVirtualID(source), "source"); !ok || got != "alt" {
		t.Fatalf("restart ownership=%q,%v", got, ok)
	}
	// A source whose spelling suggests a copy cannot override known ownership.
	store.RegisterResourceParent("source", "server", "mediasource_main", "unrelated")
	if _, ok := store.ResourceItem(item, &ResolvedID{ServerID: "server", OriginalID: "mediasource_main"}, "source"); ok {
		t.Fatal("source spelling overrode registered ownership")
	}
	if err = store.RemoveByServerID("server"); err != nil {
		t.Fatal(err)
	}
	if len(store.resourceParentIDs("source", "server", "opaque")) != 0 {
		t.Fatal("removed server retained source ownership")
	}
}

func TestStrictSameServerVersionUsesFrozenPhysicalItem(t *testing.T) {
	withReviewUpstreams(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/PlaybackInfo"):
			item := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/Items/"), "/PlaybackInfo")
			writeJSON(w, 200, map[string]any{"PlaySessionId": "session-" + item, "MediaSources": []any{map[string]any{"Id": "opaque-" + item, "Container": "mkv", "DirectStreamUrl": "/Videos/" + item + "/stream.mkv"}}})
		case strings.HasPrefix(r.URL.Path, "/Videos/"):
			if r.URL.Path != "/Videos/alt/stream.mkv" {
				t.Errorf("strict media selected wrong physical item: %s", r.URL.Path)
			}
			_, _ = w.Write([]byte("alternate"))
		case strings.HasPrefix(r.URL.Path, "/Sessions/Playing"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["ItemId"] != "alt" || body["MediaSourceId"] != "opaque-alt" || body["PlaySessionId"] != "session-alt" {
				t.Errorf("strict hybrid event: %#v", body)
			}
			w.WriteHeader(204)
		default:
			writeJSON(w, 200, map[string]any{"OfficialRating": "G"})
		}
	}, func(app *App, h http.Handler, token string) {
		sid := app.Upstream.Clients()[0].ID
		item := app.IDStore.GetOrCreateVirtualID("main", sid)
		app.IDStore.AssociateAdditionalInstance(item, "alt", sid)
		rr := doAuthJSON(t, h, "GET", "/eio/playback/v1/Items/"+item+"/PlaybackInfo", nil, token)
		var info map[string]any
		if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &info) != nil {
			t.Fatalf("strict PI: %d %s", rr.Code, rr.Body.String())
		}
		sources := asItems(map[string]any{"Items": info["MediaSources"]})
		if len(sources) != 2 {
			t.Fatalf("strict sources=%d", len(sources))
		}
		source := sources[1]
		rr = doAuthJSON(t, h, "GET", source["DirectStreamUrl"].(string), nil, token)
		if rr.Code != 200 || rr.Body.String() != "alternate" {
			t.Fatalf("strict stream: %d %s", rr.Code, rr.Body.String())
		}
		attempt := playbackStringField(source, "EioAttemptId")
		cap := playbackStringField(source, "EioPlaybackToken")
		body := map[string]any{"ItemId": item, "MediaSourceId": source["Id"], "PlaySessionId": "session-alt", "EioAttemptId": attempt}
		rr = doAuthJSON(t, h, "POST", "/eio/playback/v1/"+attempt+"/Sessions/Playing?api_key="+url.QueryEscape(cap), body, token)
		if rr.Code != 204 {
			t.Fatalf("strict event: %d %s", rr.Code, rr.Body.String())
		}
	})
}
