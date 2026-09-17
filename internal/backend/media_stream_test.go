package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// TestVideoStreamSwitchesToMediaSourceServer covers the crossover where the named
// media source lives on another upstream: the stream must be served by that server,
// using that server's own copy of the item id and its access token.
func TestVideoStreamSwitchesToMediaSourceServer(t *testing.T) {
	var primaryStreams atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-a", "User": map[string]any{"Id": "user-a"}})
		default:
			if strings.HasPrefix(r.URL.Path, "/Videos/") {
				primaryStreams.Add(1)
			}
			http.NotFound(w, r)
		}
	}))
	defer primary.Close()

	var secondaryRequest atomic.Value
	secondary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-b", "User": map[string]any{"Id": "user-b"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Videos/episode-b/stream.mkv":
			secondaryRequest.Store(r.URL.Path + "|" + r.URL.Query().Get("api_key"))
			w.Header().Set("Content-Type", "video/x-matroska")
			_, _ = w.Write([]byte("secondary-stream"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer secondary.Close()

	withTempAppConfig(t, dualUpstreamConfig(primary.URL, secondary.URL), func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		virtualEpisode := app.IDStore.GetOrCreateVirtualID("episode-a", app.Upstream.Clients()[0].ID)
		virtualMediaSource := app.IDStore.GetOrCreateVirtualID("ms-b", app.Upstream.Clients()[1].ID)
		// Server B holds its own copy of the same episode.
		app.IDStore.AssociateAdditionalInstance(virtualEpisode, "episode-b", app.Upstream.Clients()[1].ID)

		rr := doJSONRequest(t, handler, http.MethodGet,
			"/Videos/"+virtualEpisode+"/stream.mkv?MediaSourceId="+virtualMediaSource+"&api_key="+token, nil, "")
		if rr.Code != http.StatusOK {
			t.Fatalf("stream status = %d, body=%s", rr.Code, rr.Body.String())
		}
		if got := rr.Body.String(); got != "secondary-stream" {
			t.Fatalf("stream body = %q, want the secondary server's bytes", got)
		}
		if got, _ := secondaryRequest.Load().(string); got != "/Videos/episode-b/stream.mkv|tok-b" {
			t.Fatalf("secondary request = %q, want this server's item id and access token", got)
		}
		if n := primaryStreams.Load(); n != 0 {
			t.Fatalf("primary received %d stream request(s), want 0", n)
		}
	})
}

func TestVideoStreamFailsExplicitlyWhenMediaSourceTargetServerIsUnavailable(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-a", "User": map[string]any{"Id": "user-a"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Items/episode-a/PlaybackInfo":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"MediaSources": []map[string]any{{
					"Id":        "ms-b",
					"Container": "mkv",
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
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-b", "User": map[string]any{"Id": "user-b"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer secondary.Close()

	withTempAppConfig(t, dualUpstreamConfig(primary.URL, secondary.URL), func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		virtualEpisode := app.IDStore.GetOrCreateVirtualID("episode-a", app.Upstream.Clients()[0].ID)
		virtualMS := app.IDStore.GetOrCreateVirtualID("ms-b", app.Upstream.Clients()[1].ID)
		app.Upstream.GetClient(1).setOffline("test offline")

		rr := doJSONRequest(t, handler, http.MethodGet, "/Videos/"+virtualEpisode+"/stream.mkv?MediaSourceId="+virtualMS+"&api_key="+token, nil, "")
		if rr.Code == http.StatusOK {
			t.Fatalf("stream unexpectedly succeeded: %s", rr.Body.String())
		}
	})
}
