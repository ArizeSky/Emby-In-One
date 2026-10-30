package backend

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"strings"
	"testing"
)

func TestResolveDirectStreamResourceURL(t *testing.T) {
	const (
		fakeURL  = "http://127.0.0.1:18096"
		prefixed = fakeURL + "/media/emby"
		itemID   = "item"
	)
	tests := []struct {
		name     string
		raw      string
		base     string
		itemID   string
		wantURL  string
		wantRoot string
		wantOK   bool
	}{
		{
			name:     "relative master keeps path and query",
			raw:      "Videos/item/master.m3u8?LiveStreamId=live&PlaySessionId=ps",
			base:     fakeURL,
			itemID:   itemID,
			wantURL:  fakeURL + "/Videos/item/master.m3u8?LiveStreamId=live&PlaySessionId=ps",
			wantRoot: "Videos",
			wantOK:   true,
		},
		{
			name:     "relative master with configured base prefix",
			raw:      "/media/emby/Videos/item/master.m3u8?LiveStreamId=live&PlaySessionId=ps",
			base:     prefixed,
			itemID:   itemID,
			wantURL:  prefixed + "/Videos/item/master.m3u8?LiveStreamId=live&PlaySessionId=ps",
			wantRoot: "Videos",
			wantOK:   true,
		},
		{
			name:     "relative mp4",
			raw:      "Videos/item/stream.mp4?PlaySessionId=ps",
			base:     fakeURL,
			itemID:   itemID,
			wantURL:  fakeURL + "/Videos/item/stream.mp4?PlaySessionId=ps",
			wantRoot: "Videos",
			wantOK:   true,
		},
		{
			name:     "relative audio",
			raw:      "Audio/item/master.m3u8",
			base:     fakeURL,
			itemID:   itemID,
			wantURL:  fakeURL + "/Audio/item/master.m3u8",
			wantRoot: "Audio",
			wantOK:   true,
		},
		{
			name:     "absolute URL on own origin",
			raw:      prefixed + "/Videos/item/master.m3u8?LiveStreamId=live",
			base:     prefixed,
			itemID:   itemID,
			wantURL:  prefixed + "/Videos/item/master.m3u8?LiveStreamId=live",
			wantRoot: "Videos",
			wantOK:   true,
		},
		{
			name:   "foreign host rejected",
			raw:    "http://evil.example/Videos/item/master.m3u8",
			base:   fakeURL,
			itemID: itemID,
		},
		{
			name:   "protocol relative host rejected",
			raw:    "//evil.example/Videos/item/master.m3u8",
			base:   fakeURL,
			itemID: itemID,
		},
		{
			name:   "wrong item rejected",
			raw:    "Videos/other/master.m3u8",
			base:   fakeURL,
			itemID: itemID,
		},
		{
			name:   "dot segment rejected",
			raw:    "Videos/item/../other/master.m3u8",
			base:   fakeURL,
			itemID: itemID,
		},
		{
			name:   "encoded slash rejected",
			raw:    "Videos/item%2Fmaster.m3u8",
			base:   fakeURL,
			itemID: itemID,
		},
		{
			name:   "encoded dot segment rejected",
			raw:    "Videos/item/%2e%2e/master.m3u8",
			base:   fakeURL,
			itemID: itemID,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := &UpstreamClient{ID: "srv", BaseURL: tc.base, StreamBaseURL: tc.base}
			gotURL, gotRoot, gotOK := resolveDirectStreamResourceURL(tc.raw, client, tc.itemID)
			if gotOK != tc.wantOK || gotURL != tc.wantURL || gotRoot != tc.wantRoot {
				t.Fatalf("resolve(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.raw, gotURL, gotRoot, gotOK, tc.wantURL, tc.wantRoot, tc.wantOK)
			}
		})
	}
}

func TestPlaybackDirectStreamOpaqueResourceRoute(t *testing.T) {
	type seenRequest struct {
		method, path, query string
	}
	var masterRequests []seenRequest
	var segmentRange string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"AccessToken": "upstream-secret-token",
				"User":        map[string]any{"Id": "upstream-user"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/Items/original/PlaybackInfo":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"MediaSources": []map[string]any{
					{"Id": "ms-master", "Container": "m3u8", "DirectStreamUrl": "Videos/original/master.m3u8?LiveStreamId=live&PlaySessionId=ps"},
					{"Id": "ms-mp4", "Container": "mp4", "DirectStreamUrl": "Videos/original/stream.mp4?PlaySessionId=ps"},
				},
			})
		case r.URL.Path == "/Videos/original/master.m3u8":
			masterRequests = append(masterRequests, seenRequest{r.Method, r.URL.Path, r.URL.RawQuery})
			w.Header().Set("Content-Type", "application/x-mpegURL")
			_, _ = io.WriteString(w, "#EXTM3U\n#EXTINF:4,\nseg.ts\n")
		case r.URL.Path == "/Videos/original/stream.mp4":
			segmentRange = r.Header.Get("Range")
			w.Header().Set("Accept-Ranges", "bytes")
			if segmentRange == "bytes=2-5" {
				w.Header().Set("Content-Range", "bytes 2-5/10")
				w.Header().Set("Content-Length", "4")
				w.WriteHeader(http.StatusPartialContent)
				_, _ = io.WriteString(w, "2345")
				return
			}
			w.Header().Set("Content-Length", "10")
			_, _ = io.WriteString(w, "0123456789")
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	withTempAppConfig(t, singleUpstreamConfig(upstream.URL), func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		item := app.IDStore.GetOrCreateVirtualID("original", app.Upstream.Clients()[0].ID)

		pi := doJSONRequest(t, handler, http.MethodGet, "/Items/"+item+"/PlaybackInfo", nil, token)
		if pi.Code != http.StatusOK {
			t.Fatalf("PlaybackInfo status=%d body=%s", pi.Code, pi.Body.String())
		}
		if body := pi.Body.String(); strings.Contains(body, upstream.URL) || strings.Contains(body, "upstream-secret-token") {
			t.Fatalf("PlaybackInfo leaked the upstream host or token: %s", body)
		}
		var payload struct {
			MediaSources []struct {
				DirectStreamURL string `json:"DirectStreamUrl"`
			} `json:"MediaSources"`
		}
		if err := json.Unmarshal(pi.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal PlaybackInfo: %v", err)
		}
		var masterURL, rangeURL string
		for _, source := range payload.MediaSources {
			switch {
			case strings.Contains(source.DirectStreamURL, ".m3u8?"):
				masterURL = source.DirectStreamURL
			case strings.Contains(source.DirectStreamURL, ".mp4?"):
				rangeURL = source.DirectStreamURL
			}
		}
		for _, local := range []string{masterURL, rangeURL} {
			if !strings.HasPrefix(local, "/Videos/"+item+"/eio-resource/") || !strings.Contains(local, "api_key="+token) {
				t.Fatalf("direct stream is not an authenticated local resource route: %q", local)
			}
			if strings.Contains(local, upstream.URL) || strings.Contains(local, "upstream-secret-token") {
				t.Fatalf("local route leaked upstream host or token: %q", local)
			}
		}

		get := doJSONRequest(t, handler, http.MethodGet, masterURL, nil, "")
		if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), "#EXTM3U") {
			t.Fatalf("master GET status=%d body=%s", get.Code, get.Body.String())
		}
		if strings.Contains(get.Body.String(), upstream.URL) || strings.Contains(get.Body.String(), "upstream-secret-token") {
			t.Fatalf("master response leaked upstream host or token: %s", get.Body.String())
		}
		if len(masterRequests) != 1 || masterRequests[0].method != http.MethodGet ||
			masterRequests[0].path != "/Videos/original/master.m3u8" ||
			masterRequests[0].query != "LiveStreamId=live&PlaySessionId=ps" {
			t.Fatalf("upstream master request = %+v, want original path and query", masterRequests)
		}

		headReq := httptest.NewRequest(http.MethodHead, masterURL, nil)
		headRR := httptest.NewRecorder()
		handler.ServeHTTP(headRR, headReq)
		if headRR.Code != http.StatusOK || headRR.Body.Len() != 0 {
			t.Fatalf("master HEAD status=%d body=%q", headRR.Code, headRR.Body.String())
		}
		if len(masterRequests) != 2 || masterRequests[1].method != http.MethodHead {
			t.Fatalf("upstream methods = %+v, want HEAD not GET", masterRequests)
		}

		rangeReq := httptest.NewRequest(http.MethodGet, rangeURL, nil)
		rangeReq.Header.Set("Range", "bytes=2-5")
		rangeRR := httptest.NewRecorder()
		handler.ServeHTTP(rangeRR, rangeReq)
		if rangeRR.Code != http.StatusPartialContent || rangeRR.Body.String() != "2345" ||
			rangeRR.Header().Get("Content-Range") != "bytes 2-5/10" || segmentRange != "bytes=2-5" {
			t.Fatalf("range response status=%d body=%q contentRange=%q upstreamRange=%q",
				rangeRR.Code, rangeRR.Body.String(), rangeRR.Header().Get("Content-Range"), segmentRange)
		}
	})
}

func TestPlaybackDirectStreamFailClosedAtResourceRoute(t *testing.T) {
	t.Run("capacity returns 503", func(t *testing.T) {
		app := &App{StreamResources: newStreamResourceStore()}
		app.StreamResources.max = 0
		plans := []playbackDirectStream{{
			mediaSource: map[string]any{"DirectStreamUrl": "http://upstream.invalid/Videos/original/master.m3u8"},
			serverID:    "srv", originalID: "original",
			resourceURL: "http://127.0.0.1:9/Videos/original/master.m3u8?LiveStreamId=live",
			container:   "m3u8", routeRoot: "Videos",
		}}
		w := httptest.NewRecorder()
		if app.applyPlaybackDirectStreams(w, "virtual", "proxy-token", "", "user", plans) {
			t.Fatal("capacity exhaustion was not reported")
		}
		if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "5" ||
			w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("capacity response status=%d headers=%v", w.Code, w.Header())
		}
		if app.StreamResources.size() != 0 {
			t.Fatalf("failed registration leaked %d handles", app.StreamResources.size())
		}
	})

	t.Run("wrong user gets 403", func(t *testing.T) {
		store := newStreamResourceStore()
		id, ok := store.put(streamResourceEntry{
			UserID: "owner", VirtualItemID: "movie", ServerID: "srv", OriginalID: "original",
			ResourceURL: "http://127.0.0.1:9/Videos/original/master.m3u8",
		})
		if !ok {
			t.Fatal("failed to register resource")
		}
		r := httptest.NewRequest(http.MethodGet, "/Videos/movie/eio-resource/"+id+".m3u8", nil)
		r = r.WithContext(context.WithValue(r.Context(), requestContextKey{}, &RequestContext{
			ProxyUser: &tokenInfo{UserID: "other"},
		}))
		w := httptest.NewRecorder()
		(&App{StreamResources: store}).serveStreamResource(w, r, "movie", id+".m3u8")
		if w.Code != http.StatusForbidden {
			t.Fatalf("wrong-user resource status=%d, want 403", w.Code)
		}
	})
}

func TestPlaybackDirectStreamTimelineIsolation(t *testing.T) {
	app := &App{StreamResources: newStreamResourceStore()}
	plans := []playbackDirectStream{
		{mediaSource: map[string]any{}, serverID: "a", originalID: "item", resourceURL: "http://upstream.invalid/Videos/item/master.m3u8?PlaySessionId=ps-a", container: "m3u8"},
		{mediaSource: map[string]any{}, serverID: "b", originalID: "item", resourceURL: "http://upstream.invalid/Videos/item/master.m3u8?playsessionid=ps-b", container: "m3u8"},
		{mediaSource: map[string]any{}, serverID: "a", originalID: "item", resourceURL: "http://upstream.invalid/Videos/item/stream.mp4", container: "mp4"},
	}
	if !app.applyPlaybackDirectStreams(httptest.NewRecorder(), "virtual", "proxy", "", "user", plans) {
		t.Fatal("registration failed")
	}
	for i, want := range []string{"a\x00virtual\x00ps-a", "b\x00virtual\x00ps-b", ""} {
		local, _ := plans[i].mediaSource["DirectStreamUrl"].(string)
		parsed, err := url.Parse(local)
		if err != nil {
			t.Fatal(err)
		}
		id := streamResourceHandleID(path.Base(parsed.Path))
		entry, ok := app.StreamResources.peek(id)
		if !ok || entry.TimelineSession != want {
			t.Fatalf("entry %d timeline=%q ok=%v, want %q", i, entry.TimelineSession, ok, want)
		}
	}
	if got := app.directStreamTimelineSession(plans[0], "other-item", "request-ps"); got != "a\x00other-item\x00ps-a" {
		t.Fatalf("URL session must win and item scope must differ: %q", got)
	}
	store, err := NewIDStore(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app.IDStore = store
	virtualSession := store.GetOrCreateVirtualID("request-ps", "a")
	if got := app.directStreamTimelineSession(plans[2], "virtual", virtualSession); got != "a\x00virtual\x00request-ps" {
		t.Fatalf("virtual request session not translated: %q", got)
	}
	foreign := plans[2]
	foreign.serverID = "b"
	if got := app.directStreamTimelineSession(foreign, "virtual", virtualSession); got != "" {
		t.Fatalf("cross-server request session used: %q", got)
	}
}
