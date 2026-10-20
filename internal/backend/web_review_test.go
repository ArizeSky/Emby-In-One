package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func withReviewUpstreams(t *testing.T, upstream http.HandlerFunc, run func(*App, http.Handler, string)) {
	t.Helper()
	servers := []*httptest.Server{}
	for _, name := range []string{"a", "b"} {
		name := name
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/Users/AuthenticateByName" {
				writeJSON(w, 200, map[string]any{"AccessToken": "token-" + name, "User": map[string]any{"Id": "user-" + name}})
				return
			}
			r.Header.Set("Test-Server", name)
			upstream(w, r)
		}))
		servers = append(servers, server)
		defer server.Close()
	}
	config := fmt.Sprintf(`server:
  port: 8096
  name: Test
  id: local
admin:
  username: admin
  password: secret
playback:
  mode: proxy
timeouts:
  api: 2000
  global: 3000
  login: 2000
  healthInterval: 60000
upstream:
  - name: A
    url: %q
    username: u1
    password: p1
  - name: B
    url: %q
    username: u2
    password: p2
`, servers[0].URL, servers[1].URL)
	withTempAppConfig(t, config, func(app *App, handler http.Handler) { run(app, handler, loginToken(t, handler, "secret")) })
}

func TestSelectedVersionOwnsPlaybackSessionAndSubtitle(t *testing.T) {
	var callsA atomic.Int32
	var callsB atomic.Int32
	withReviewUpstreams(t, func(w http.ResponseWriter, r *http.Request) {
		server := r.Header.Get("Test-Server")
		if strings.HasSuffix(r.URL.Path, "/PlaybackInfo") {
			if server == "a" {
				callsA.Add(1)
			} else {
				callsB.Add(1)
			}
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if r.URL.Path != "/Items/item-b/PlaybackInfo" || body["MediaSourceId"] != "ms-b" || body["AudioStreamIndex"] != float64(3) {
				t.Errorf("wrong selected-version request: %s %#v", r.URL.Path, body)
			}
			writeJSON(w, 200, map[string]any{"PlaySessionId": "session-b", "MediaSources": []any{map[string]any{"Id": "ms-b", "Name": "1080p", "DirectStreamUrl": "/Videos/item-b/stream.mp4"}}})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/Sessions/Playing") {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if server != "b" || body["ItemId"] != "item-b" || body["MediaSourceId"] != "ms-b" || body["PlaySessionId"] != "session-b" {
				t.Errorf("session misrouted: %s %#v", server, body)
			}
			w.WriteHeader(204)
			return
		}
		if r.URL.Path == "/Videos/item-b/ms-b/Subtitles/4/Stream.vtt" {
			if server != "b" || r.URL.Query().Get("MediaSourceId") != "ms-b" {
				t.Errorf("subtitle misrouted: %s %s", server, r.URL.RawQuery)
			}
			_, _ = w.Write([]byte("WEBVTT\n"))
			return
		}
		http.NotFound(w, r)
	}, func(app *App, handler http.Handler, token string) {
		clients := app.Upstream.Clients()
		item := app.IDStore.GetOrCreateVirtualID("item-a", clients[0].ID)
		app.IDStore.AssociateAdditionalInstance(item, "item-b", clients[1].ID)
		source := app.IDStore.GetOrCreateVirtualID("ms-b", clients[1].ID)
		rr := doJSONRequest(t, handler, "POST", "/Items/"+item+"/PlaybackInfo", map[string]any{"MediaSourceId": source, "AudioStreamIndex": 3}, token)
		if rr.Code != 200 {
			t.Fatalf("playback: %d %s", rr.Code, rr.Body.String())
		}
		var info map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &info)
		session, _ := info["PlaySessionId"].(string)
		if mapped := app.IDStore.ResolveVirtualID(session); mapped == nil || mapped.ServerID != clients[1].ID {
			t.Fatalf("wrong session mapping: %#v", mapped)
		}
		media := asItems(map[string]any{"Items": info["MediaSources"]})
		if len(media) != 1 || media[0]["Name"] != "1080p (B)" || media[0]["Id"] != source {
			t.Fatalf("versions: %#v", media)
		}
		for _, suffix := range []string{"", "/Progress", "/Stopped"} {
			rr = doJSONRequest(t, handler, "POST", "/Sessions/Playing"+suffix, map[string]any{"ItemId": item, "MediaSourceId": source, "PlaySessionId": session}, token)
			if rr.Code != 204 {
				t.Fatalf("session%s: %d %s", suffix, rr.Code, rr.Body.String())
			}
		}
		rr = doJSONRequest(t, handler, "GET", "/Videos/"+item+"/"+source+"/Subtitles/4/Stream.vtt", nil, token)
		if rr.Code != 200 || rr.Body.String() != "WEBVTT\n" {
			t.Fatalf("subtitle: %d %s", rr.Code, rr.Body.String())
		}
		if callsA.Load() != 0 || callsB.Load() != 1 {
			t.Fatalf("playback requests A=%d B=%d", callsA.Load(), callsB.Load())
		}
	})
}

func TestLibraryDetailDiscoversOtherCopyAndPagesCompleteEpisodes(t *testing.T) {
	var failPage atomic.Bool
	withReviewUpstreams(t, func(w http.ResponseWriter, r *http.Request) {
		server := r.Header.Get("Test-Server")
		switch {
		case strings.HasSuffix(r.URL.Path, "/Items/series-"+server):
			writeJSON(w, 200, map[string]any{"Id": "series-" + server, "Type": "Series", "ProviderIds": map[string]any{"Tmdb": "42"}})
		case r.URL.Path == "/Users/user-b/Items":
			if !strings.Contains(r.URL.Query().Get("AnyProviderIdEquals"), "tmdb.42") {
				t.Error("discovery must use provider filter")
			}
			writeJSON(w, 200, map[string]any{"Items": []any{map[string]any{"Id": "series-b", "Type": "Series", "ProviderIds": map[string]any{"Tmdb": "42"}}, map[string]any{"Id": "unrelated", "Type": "Series", "ProviderIds": map[string]any{"Tmdb": "99"}}}})
		case strings.HasSuffix(r.URL.Path, "/Episodes"):
			count := 2
			if server == "b" {
				count = 5
			}
			offset, _ := strconv.Atoi(r.URL.Query().Get("StartIndex"))
			if server == "b" && offset == 2 && failPage.Load() {
				http.Error(w, "page failed", 502)
				return
			}
			items := []any{}
			for i := offset; i < count && i < offset+2; i++ {
				items = append(items, map[string]any{"Id": fmt.Sprintf("ep-%s-%d", server, i+1), "ParentIndexNumber": 1, "IndexNumber": i + 1, "SeriesId": "series-" + server})
			}
			writeJSON(w, 200, map[string]any{"Items": items, "TotalRecordCount": count})
		default:
			http.NotFound(w, r)
		}
	}, func(app *App, handler http.Handler, token string) {
		series := app.IDStore.GetOrCreateVirtualID("series-a", app.Upstream.Clients()[0].ID)
		rr := doJSONRequest(t, handler, "GET", "/Users/local/Items/"+series, nil, token)
		if rr.Code != 200 {
			t.Fatalf("detail: %d %s", rr.Code, rr.Body.String())
		}
		mapping := app.IDStore.ResolveVirtualID(series)
		if len(mapping.OtherInstances) != 1 || mapping.OtherInstances[0].OriginalID != "series-b" {
			t.Fatalf("copies: %#v", mapping)
		}
		rr = doJSONRequest(t, handler, "GET", "/Shows/"+series+"/Episodes?StartIndex=2&Limit=2", nil, token)
		var page map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &page)
		items := asItems(page)
		if rr.Code != 200 || page["TotalRecordCount"] != float64(5) || len(items) != 2 || items[0]["IndexNumber"] != float64(3) || items[1]["IndexNumber"] != float64(4) {
			t.Fatalf("merged pagination: %d %#v", rr.Code, page)
		}
		failPage.Store(true)
		rr = doJSONRequest(t, handler, "GET", "/Shows/"+series+"/Episodes", nil, token)
		if rr.Code != 502 {
			t.Fatalf("failed page must not masquerade as a complete list: %d %s", rr.Code, rr.Body.String())
		}
	})
}
