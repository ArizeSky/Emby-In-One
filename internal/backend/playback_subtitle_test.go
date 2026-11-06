package backend

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestNativeSubtitleDeliveryKeepsSelectedReplica(t *testing.T) {
	var subtitleCalls atomic.Int32
	withReviewUpstreams(t, func(w http.ResponseWriter, r *http.Request) {
		server := r.Header.Get("Test-Server")
		item := "item-" + server
		source := "mediasource_" + item
		switch {
		case strings.HasSuffix(r.URL.Path, "/PlaybackInfo"):
			writeJSON(w, 200, map[string]any{"PlaySessionId": "session-" + server, "MediaSources": []any{map[string]any{
				"Id": source, "Container": "mkv", "DirectStreamUrl": "/Videos/" + item + "/stream.mkv",
				"MediaStreams": []any{map[string]any{"Type": "Subtitle", "Index": 2, "Codec": "ass",
					"DeliveryUrl": "/videos/" + item + "/" + source + "/Subtitles/2/0/Stream.ass?tag=" + item + "&X-Emby-Token=private-fixture"}},
			}}})
		case strings.Contains(r.URL.Path, "/Subtitles/"):
			subtitleCalls.Add(1)
			if server != "b" || r.URL.Path != "/Videos/item-b/mediasource_item-b/Subtitles/2/0/Stream.ass" {
				t.Errorf("subtitle reached wrong replica/path: server=%s path=%s", server, r.URL.Path)
			}
			if token := r.Header.Get("X-Emby-Token"); token != "token-b" {
				t.Error("subtitle did not use serving replica credential")
			}
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("[Script Info]\nTitle: replica-b\n"))
		default:
			writeJSON(w, 200, map[string]any{"Id": item, "OfficialRating": "G"})
		}
	}, func(app *App, h http.Handler, admin string) {
		clients := app.Upstream.Clients()
		item := app.IDStore.GetOrCreateVirtualID("item-a", clients[0].ID)
		app.IDStore.AssociateAdditionalInstance(item, "item-b", clients[1].ID)
		for _, token := range []string{admin, createRouteAccessUser(t, h, admin, "subtitle-viewer", []string{clients[0].ID, clients[1].ID})} {
			rr := doJSONRequest(t, h, "GET", "/Items/"+item+"/PlaybackInfo", nil, token)
			if rr.Code != 200 {
				t.Fatalf("negotiation status=%d body=%s", rr.Code, rr.Body.String())
			}
			var info map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &info); err != nil {
				t.Fatal(err)
			}
			sources := asItems(map[string]any{"Items": info["MediaSources"]})
			if len(sources) != 2 {
				t.Fatalf("sources=%d", len(sources))
			}
			b := sources[1]
			streams := asItems(map[string]any{"Items": b["MediaStreams"]})
			delivery, _ := streams[0]["DeliveryUrl"].(string)
			u, err := url.Parse(delivery)
			if err != nil {
				t.Fatal(err)
			}
			source := b["Id"].(string)
			if u.Path != "/Videos/"+item+"/"+source+"/Subtitles/2/0/Stream.ass" || u.Query().Get("MediaSourceId") != source || u.Query().Get("tag") != "item-b" {
				t.Fatalf("source binding/path/query changed: %s", delivery)
			}
			if strings.Contains(delivery, "private-fixture") || u.Host != "" {
				t.Fatal("upstream credential or origin escaped")
			}
			// Both consuming DeliveryUrl and constructing the native path with
			// the source ID must use B, even though the item primary remains A.
			for _, path := range []string{delivery, "/Videos/" + item + "/" + source + "/Subtitles/2/0/Stream.ass"} {
				rr = doJSONRequest(t, h, "GET", path, nil, token)
				if rr.Code != 200 || !strings.Contains(rr.Body.String(), "replica-b") {
					t.Fatalf("subtitle status=%d body=%s", rr.Code, rr.Body.String())
				}
			}
		}
		// A cached, ambiguous client-constructed URL cannot silently pick A.
		before := subtitleCalls.Load()
		for _, query := range []string{"", "?MediaSourceId=unknown"} {
			rr := doJSONRequest(t, h, "GET", "/Videos/"+item+"/mediasource_"+item+"/Subtitles/2/0/Stream.ass"+query, nil, admin)
			if rr.Code != 400 || !strings.Contains(rr.Body.String(), "subtitle-source-") {
				t.Fatalf("ambiguous subtitle status=%d body=%s", rr.Code, rr.Body.String())
			}
		}
		if subtitleCalls.Load() != before {
			t.Error("ambiguous subtitle contacted an upstream")
		}
		// A native client may keep the selected session instead of MediaSourceId.
		source := app.IDStore.GetOrCreateVirtualID("mediasource_item-b", clients[1].ID)
		rr := doJSONRequest(t, h, "GET", "/Items/"+item+"/PlaybackInfo?MediaSourceId="+source, nil, admin)
		var selected map[string]any
		if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &selected) != nil {
			t.Fatal("selected negotiation failed")
		}
		session, _ := selected["PlaySessionId"].(string)
		rr = doJSONRequest(t, h, "GET", "/Videos/"+item+"/mediasource_"+item+"/Subtitles/2/0/Stream.ass?PlaySessionId="+url.QueryEscape(session), nil, admin)
		if rr.Code != 200 || !strings.Contains(rr.Body.String(), "replica-b") {
			t.Fatalf("session-bound subtitle status=%d body=%s", rr.Code, rr.Body.String())
		}
		// A cached source from B is still subject to current user access.
		onlyA := createRouteAccessUser(t, h, admin, "subtitle-only-a", []string{clients[0].ID})
		before = subtitleCalls.Load()
		rr = doJSONRequest(t, h, "GET", "/Videos/"+item+"/"+source+"/Subtitles/2/0/Stream.ass", nil, onlyA)
		if rr.Code != 403 || subtitleCalls.Load() != before {
			t.Fatalf("unauthorized subtitle status=%d contacted=%v", rr.Code, subtitleCalls.Load() != before)
		}
		// The aggregate can have other global copies while this user has only
		// one usable copy. Native construction is unambiguous in that case.
		onlyB := createRouteAccessUser(t, h, admin, "subtitle-only-b", []string{clients[1].ID})
		rr = doJSONRequest(t, h, "GET", "/Videos/"+item+"/mediasource_"+item+"/Subtitles/2/0/Stream.ass", nil, onlyB)
		if rr.Code != 200 || !strings.Contains(rr.Body.String(), "replica-b") {
			t.Fatalf("single allowed replica subtitle status=%d body=%s", rr.Code, rr.Body.String())
		}
	})
}

func TestSubtitleDeliveryURLUsesSegments(t *testing.T) {
	got := rewriteSubtitleDeliveryURL("https://up.invalid/emby/videos/123/mediasource_123/Subtitles/2/Stream.ass?tag=123&MediaSourceId=old&api_key=upstream", "123", "virtual-item", "mediasource_123", "virtual-source", "local")
	u, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "" || u.Path != "/Videos/virtual-item/virtual-source/Subtitles/2/Stream.ass" || u.Query().Get("tag") != "123" || u.Query().Get("MediaSourceId") != "virtual-source" || u.Query().Get("api_key") != "local" {
		t.Fatalf("unexpected rewrite: %s", got)
	}
	// Some upstreams use mediasource_<item> in the delivery path even when
	// the source's actual Id has a different spelling. Keep both meanings.
	got = rewriteSubtitleDeliveryURL("/Videos/123/mediasource_123/Subtitles/2/Stream.ass", "123", "virtual-item", "source-b", "virtual-source", "local")
	u, _ = url.Parse(got)
	if u.Path != "/Videos/virtual-item/mediasource_virtual-item/Subtitles/2/Stream.ass" || u.Query().Get("MediaSourceId") != "virtual-source" {
		t.Fatalf("lost delivery alias/source binding: %s", got)
	}
}
