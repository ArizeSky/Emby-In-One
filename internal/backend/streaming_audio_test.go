package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestAudioProxyAndActiveEncodings(t *testing.T) {
	var deletedPlaySession atomic.Value

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-a", "User": map[string]any{"Id": "user-a"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Audio/song-a/stream.mp3":
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write([]byte("audio-stream"))
		case r.Method == http.MethodGet && r.URL.Path == "/Audio/song-a/universal":
			w.Header().Set("Content-Type", "audio/aac")
			_, _ = w.Write([]byte("audio-universal"))
		case r.Method == http.MethodDelete && r.URL.Path == "/Videos/ActiveEncodings":
			deletedPlaySession.Store(r.URL.Query().Get("PlaySessionId"))
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test Server\"\n  id: \"server-1\"\n\nadmin:\n  username: \"admin\"\n  password: \"secret\"\n\nplayback:\n  mode: \"proxy\"\n\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\n\nproxies: []\nupstream:\n  - name: \"A\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n", upstream.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		virtualSong := app.IDStore.GetOrCreateVirtualID("song-a", app.Upstream.Clients()[0].ID)
		virtualPlaySession := app.IDStore.GetOrCreateVirtualID("play-a", app.Upstream.Clients()[0].ID)

		rr := doJSONRequest(t, handler, http.MethodGet, "/Audio/"+virtualSong+"/stream.mp3", nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("audio stream status = %d, body=%s", rr.Code, rr.Body.String())
		}
		if rr.Body.String() != "audio-stream" {
			t.Fatalf("unexpected audio stream body: %q", rr.Body.String())
		}
		if got := rr.Header().Get("Content-Type"); got != "audio/mpeg" {
			t.Fatalf("unexpected audio content type: %q", got)
		}

		req := httptest.NewRequest(http.MethodGet, "/emby/Audio/"+virtualSong+"/universal?api_key="+token, nil)
		rr = httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("audio universal status = %d, body=%s", rr.Code, rr.Body.String())
		}
		if rr.Body.String() != "audio-universal" {
			t.Fatalf("unexpected audio universal body: %q", rr.Body.String())
		}

		rr = doJSONRequest(t, handler, http.MethodDelete, "/Videos/ActiveEncodings?PlaySessionId="+virtualPlaySession, nil, token)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("active encodings delete status = %d, body=%s", rr.Code, rr.Body.String())
		}
		if got, _ := deletedPlaySession.Load().(string); got != "play-a" {
			t.Fatalf("PlaySessionId translation = %q, want play-a", got)
		}
	})
}
