package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestItemsResumeFallsBackAcrossSeriesInstances(t *testing.T) {
	var primaryCalls atomic.Int32
	var secondaryCalls atomic.Int32

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"AccessToken": "token-a",
				"User":        map[string]any{"Id": "user-a"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/Users/user-a/Items/Resume":
			primaryCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Items": []map[string]any{{
					"Id":                "wrong-ep-1",
					"SeriesId":          "other-series",
					"ParentId":          "other-season",
					"GrandparentId":     "other-series",
					"SeriesName":        "Re:Zero",
					"ParentIndexNumber": 1,
					"IndexNumber":       1,
					"Source":            "wrong-primary",
				}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer primary.Close()

	secondary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"AccessToken": "token-b",
				"User":        map[string]any{"Id": "user-b"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/Users/user-b/Items/Resume":
			secondaryCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Items": []map[string]any{{
					"Id":                "right-ep-2",
					"SeriesId":          "series-b",
					"ParentId":          "season-b",
					"GrandparentId":     "series-b",
					"SeriesName":        "Ruri no Houseki",
					"ParentIndexNumber": 1,
					"IndexNumber":       2,
					"Source":            "secondary",
				}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer secondary.Close()

	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test Server\"\n  id: \"server-1\"\n\nadmin:\n  username: \"admin\"\n  password: \"secret\"\n\nplayback:\n  mode: \"proxy\"\n\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\n\nproxies: []\nupstream:\n  - name: \"A\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n  - name: \"B\"\n    url: %q\n    username: \"u2\"\n    password: \"p2\"\n", primary.URL, secondary.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		virtualSeries := app.IDStore.GetOrCreateVirtualID("series-a", app.Upstream.Clients()[0].ID)
		app.IDStore.AssociateAdditionalInstance(virtualSeries, "series-b", app.Upstream.Clients()[1].ID)

		rr := doJSONRequest(t, handler, http.MethodGet, "/Users/"+app.Auth.ProxyUserID()+"/Items/Resume?ParentId="+virtualSeries, nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("resume status = %d, body=%s", rr.Code, rr.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal resume: %v", err)
		}
		items, _ := payload["Items"].([]any)
		if len(items) != 1 {
			t.Fatalf("resume items len = %d, want 1 payload=%#v", len(items), payload)
		}
		item := items[0].(map[string]any)
		if item["Source"] != "secondary" {
			t.Fatalf("unexpected resume source: %#v", item)
		}
		if item["Id"] == "right-ep-2" || item["SeriesId"] == "series-b" {
			t.Fatalf("expected rewritten virtual ids, got %#v", item)
		}
		if primaryCalls.Load() != 1 || secondaryCalls.Load() != 1 {
			t.Fatalf("unexpected upstream call counts: primary=%d secondary=%d", primaryCalls.Load(), secondaryCalls.Load())
		}
	})
}

func TestShowsNextUpUsesPrimarySeriesResultWithoutQueryingSecondary(t *testing.T) {
	var primaryCalls atomic.Int32
	var secondaryCalls atomic.Int32

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"AccessToken": "token-a",
				"User":        map[string]any{"Id": "user-a"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/Shows/NextUp":
			primaryCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Items": []map[string]any{{
					"Id":            "next-2",
					"SeriesId":      "series-a",
					"ParentId":      "season-a",
					"GrandparentId": "series-a",
					"IndexNumber":   2,
					"Source":        "primary",
				}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer primary.Close()

	secondary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"AccessToken": "token-b",
				"User":        map[string]any{"Id": "user-b"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/Shows/NextUp":
			secondaryCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Items": []map[string]any{{
					"Id":            "next-bad",
					"SeriesId":      "series-b",
					"ParentId":      "season-b",
					"GrandparentId": "series-b",
					"IndexNumber":   9,
					"Source":        "secondary",
				}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer secondary.Close()

	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test Server\"\n  id: \"server-1\"\n\nadmin:\n  username: \"admin\"\n  password: \"secret\"\n\nplayback:\n  mode: \"proxy\"\n\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\n\nproxies: []\nupstream:\n  - name: \"A\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n  - name: \"B\"\n    url: %q\n    username: \"u2\"\n    password: \"p2\"\n", primary.URL, secondary.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		virtualSeries := app.IDStore.GetOrCreateVirtualID("series-a", app.Upstream.Clients()[0].ID)
		app.IDStore.AssociateAdditionalInstance(virtualSeries, "series-b", app.Upstream.Clients()[1].ID)

		rr := doJSONRequest(t, handler, http.MethodGet, "/Shows/NextUp?SeriesId="+virtualSeries, nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("nextup status = %d, body=%s", rr.Code, rr.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal nextup: %v", err)
		}
		items, _ := payload["Items"].([]any)
		if len(items) != 1 {
			t.Fatalf("nextup items len = %d, want 1 payload=%#v", len(items), payload)
		}
		item := items[0].(map[string]any)
		if item["Source"] != "primary" {
			t.Fatalf("unexpected nextup source: %#v", item)
		}
		if primaryCalls.Load() != 1 || secondaryCalls.Load() != 0 {
			t.Fatalf("unexpected nextup upstream call counts: primary=%d secondary=%d", primaryCalls.Load(), secondaryCalls.Load())
		}
	})
}

func TestPlaybackInfoAndMasterPlaylistProxy(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"AccessToken": "upstream-token",
				"User":        map[string]any{"Id": "user-a"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/Items/episode-1/PlaybackInfo":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"MediaSources": []map[string]any{{
					"Id":             "ms-1",
					"Container":      "ts",
					"TranscodingUrl": "/Videos/episode-1/master.m3u8?MediaSourceId=ms-1&api_key=upstream-token",
				}},
				"PlaySessionId": "play-1",
			})
		case r.Method == http.MethodGet && r.URL.Path == "/Videos/episode-1/master.m3u8":
			w.Header().Set("Content-Type", "application/x-mpegURL")
			_, _ = w.Write([]byte("#EXTM3U\nsegment1.ts\nhttps://cdn.example/Videos/episode-1/hls1/main/seg.ts?foo=1&api_key=upstream-token\n"))
		case r.Method == http.MethodGet && r.URL.Path == "/Videos/episode-1/segment1.ts":
			_, _ = w.Write([]byte("segment-one"))
		case r.Method == http.MethodGet && r.URL.Path == "/Videos/episode-1/hls1/main/seg.ts":
			_, _ = w.Write([]byte("segment-two"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test Server\"\n  id: \"server-1\"\n\nadmin:\n  username: \"admin\"\n  password: \"secret\"\n\nplayback:\n  mode: \"proxy\"\n\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\n\nproxies: []\nupstream:\n  - name: \"A\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n", upstream.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		virtualEpisode := app.IDStore.GetOrCreateVirtualID("episode-1", app.Upstream.Clients()[0].ID)

		playbackRR := doJSONRequest(t, handler, http.MethodGet, "/Items/"+virtualEpisode+"/PlaybackInfo", nil, token)
		if playbackRR.Code != http.StatusOK {
			t.Fatalf("playback info status = %d, body=%s", playbackRR.Code, playbackRR.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(playbackRR.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal playback info: %v", err)
		}
		mediaSources, _ := payload["MediaSources"].([]any)
		if len(mediaSources) != 1 {
			t.Fatalf("media source count = %d payload=%#v", len(mediaSources), payload)
		}
		ms := mediaSources[0].(map[string]any)
		transcodingURL, _ := ms["TranscodingUrl"].(string)
		if !strings.Contains(transcodingURL, virtualEpisode) || !strings.Contains(transcodingURL, "api_key="+token) {
			t.Fatalf("unexpected transcoding url: %q", transcodingURL)
		}
		mediaSourceID, _ := ms["Id"].(string)
		if mediaSourceID == "" || mediaSourceID == "ms-1" {
			t.Fatalf("expected virtual media source id, got %q", mediaSourceID)
		}

		playlistReq := httptest.NewRequest(http.MethodGet, transcodingURL, nil)
		playlistRR := httptest.NewRecorder()
		handler.ServeHTTP(playlistRR, playlistReq)
		if playlistRR.Code != http.StatusOK {
			t.Fatalf("playlist status = %d, body=%s", playlistRR.Code, playlistRR.Body.String())
		}
		body := playlistRR.Body.String()
		if strings.Contains(strings.ToLower(body), "localhost") || strings.Contains(body, "127.0.0.1") {
			t.Fatalf("playlist should not contain a host: %s", body)
		}
		if !strings.Contains(body, "/Videos/"+virtualEpisode+"/segment1.ts?api_key="+token) {
			t.Fatalf("playlist missing rewritten relative segment path: %s", body)
		}
		// A segment that arrived as an absolute CDN URL must also come back as a
		// proxy-relative path carrying the proxy token — never the upstream host.
		if !strings.Contains(body, "/Videos/"+virtualEpisode+"/hls1/main/seg.ts?") || strings.Contains(body, "cdn.example") {
			t.Fatalf("playlist missing rewritten absolute segment path or still has upstream host: %s", body)
		}
		if strings.Contains(body, "api_key=upstream-token") {
			t.Fatalf("playlist still has upstream token: %s", body)
		}

		segmentRR := doJSONRequest(t, handler, http.MethodGet, "/Videos/"+virtualEpisode+"/segment1.ts?api_key="+token, nil, "")
		if segmentRR.Code != http.StatusOK {
			t.Fatalf("segment status = %d, body=%s", segmentRR.Code, segmentRR.Body.String())
		}
		if segmentRR.Body.String() != "segment-one" {
			t.Fatalf("unexpected segment body: %q", segmentRR.Body.String())
		}
	})
}

func TestPlaybackInfoRewritesSubtitleDeliveryURLForASSTracks(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"AccessToken": "upstream-token",
				"User":        map[string]any{"Id": "user-a"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/Items/episode-1/PlaybackInfo":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"MediaSources": []map[string]any{{
					"Id": "ms-1",
					"MediaStreams": []map[string]any{{
						"Type":        "Subtitle",
						"Codec":       "ass",
						"DeliveryUrl": "/Videos/episode-1/ms-1/Subtitles/1/Stream.ass?api_key=upstream-token&tag=1",
					}},
				}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test Server\"\n  id: \"server-1\"\n\nadmin:\n  username: \"admin\"\n  password: \"secret\"\n\nplayback:\n  mode: \"proxy\"\n\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\n\nproxies: []\nupstream:\n  - name: \"A\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n", upstream.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		virtualEpisode := app.IDStore.GetOrCreateVirtualID("episode-1", app.Upstream.Clients()[0].ID)

		playbackRR := doJSONRequest(t, handler, http.MethodGet, "/Items/"+virtualEpisode+"/PlaybackInfo", nil, token)
		if playbackRR.Code != http.StatusOK {
			t.Fatalf("playback info status = %d, body=%s", playbackRR.Code, playbackRR.Body.String())
		}

		var payload map[string]any
		if err := json.Unmarshal(playbackRR.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal playback info: %v", err)
		}

		mediaSources, _ := payload["MediaSources"].([]any)
		if len(mediaSources) != 1 {
			t.Fatalf("media source count = %d payload=%#v", len(mediaSources), payload)
		}

		ms := mediaSources[0].(map[string]any)
		mediaSourceID, _ := ms["Id"].(string)
		if mediaSourceID == "" || mediaSourceID == "ms-1" {
			t.Fatalf("expected virtual media source id, got %q", mediaSourceID)
		}

		streams, _ := ms["MediaStreams"].([]any)
		if len(streams) != 1 {
			t.Fatalf("subtitle stream count = %d payload=%#v", len(streams), ms)
		}

		stream := streams[0].(map[string]any)
		deliveryURL, _ := stream["DeliveryUrl"].(string)
		parsed, err := url.Parse(deliveryURL)
		if err != nil {
			t.Fatalf("parse delivery url %q: %v", deliveryURL, err)
		}

		if parsed.Path != "/Videos/"+virtualEpisode+"/"+mediaSourceID+"/Subtitles/1/Stream.ass" {
			t.Fatalf("unexpected subtitle delivery path: %q", parsed.Path)
		}
		if parsed.Query().Get("api_key") != token {
			t.Fatalf("subtitle delivery api_key = %q, want %q", parsed.Query().Get("api_key"), token)
		}
		if parsed.Query().Get("tag") != "1" {
			t.Fatalf("subtitle delivery tag = %q, want 1", parsed.Query().Get("tag"))
		}
		if strings.Contains(deliveryURL, "upstream-token") {
			t.Fatalf("subtitle delivery url still exposes upstream token: %q", deliveryURL)
		}
	})
}
