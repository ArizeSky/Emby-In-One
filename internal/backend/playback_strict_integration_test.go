package backend

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// Exercise the real router and middleware, not just an internally installed
// RequestContext. All network requests terminate in this isolated fake upstream.
func TestStrictPlaybackNegotiationResourcesEventsAndRefresh(t *testing.T) {
	mediaHits, events := 0, 0
	cleanupHits := 0
	wantToken := "tok-a"
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-a", "User": map[string]any{"Id": "user-a"}})
		case r.URL.Path == "/Items/item-a/PlaybackInfo":
			_ = json.NewEncoder(w).Encode(map[string]any{"PlaySessionId": "session-a", "MediaSources": []any{map[string]any{"Id": "source-a", "Container": "m3u8", "DirectStreamUrl": "/Videos/item-a/master.m3u8?api_key=tok-a"}}})
		case r.URL.Path == "/Items/item-a":
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "item-a", "Type": "Movie", "OfficialRating": "G"})
		case strings.HasPrefix(r.URL.Path, "/Videos/item-a/"):
			mediaHits++
			if r.UserAgent() != "TestClient/1.0" || r.Header.Get("X-Emby-Token") != wantToken {
				t.Error("media lost negotiated identity or refreshed authentication")
			}
			if strings.HasSuffix(r.URL.Path, ".m3u8") {
				w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
				_, _ = io.WriteString(w, "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXTINF:1,\nseg.ts\n#EXT-X-ENDLIST\n")
			} else {
				_, _ = w.Write([]byte{1, 2, 3, 4})
			}
		case r.Method == "DELETE" && r.URL.Path == "/Videos/ActiveEncodings":
			cleanupHits++
			if r.URL.Query().Get("PlaySessionId") != "session-a" || r.Header.Get("X-Emby-Token") != wantToken {
				t.Error("cleanup left its own attempt/auth snapshot")
			}
			w.WriteHeader(204)
		case strings.HasPrefix(r.URL.Path, "/Sessions/Playing"):
			events++
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["EioAttemptId"] != nil || body["ItemId"] != "item-a" || body["PlaySessionId"] != "session-a" {
				t.Error("strict session event not scoped/translated")
			}
			if r.Header.Get("X-Emby-Token") != wantToken || r.UserAgent() != "TestClient/1.0" {
				t.Error("event identity/auth drifted")
			}
			w.WriteHeader(204)
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	withTempAppConfig(t, playbackLimiterConfig(up.URL), func(app *App, handler http.Handler) {
		token := createRegularUser(t, handler)
		c := app.Upstream.ClientByID(app.Upstream.Clients()[0].ID)
		item := app.IDStore.GetOrCreateVirtualID("item-a", c.ID)
		negotiation := doAuthJSON(t, handler, "GET", "/eio/playback/v1/Items/"+item+"/PlaybackInfo", nil, token)
		if negotiation.Code != 200 {
			t.Fatalf("strict negotiation status=%d body=%s", negotiation.Code, negotiation.Body.String())
		}
		var info map[string]any
		if err := json.Unmarshal(negotiation.Body.Bytes(), &info); err != nil {
			t.Fatal(err)
		}
		source := info["MediaSources"].([]any)[0].(map[string]any)
		attempt, cap := playbackStringField(source, "EioAttemptId"), playbackStringField(source, "EioPlaybackToken")
		media := playbackStringField(source, "DirectStreamUrl")
		if attempt == "" || cap == "" || !strings.HasPrefix(media, "/eio/playback/v1/"+attempt+"/") {
			t.Fatal("source was not bound")
		}
		blocked := doAuthJSON(t, handler, "GET", "/Videos/"+item+"/stream.ts?api_key="+url.QueryEscape(cap), nil, token)
		if blocked.Code != 401 || mediaHits != 0 {
			t.Fatal("capability downgraded to legacy")
		}
		wrong := strings.Replace(media, attempt, "wrong-attempt", 1)
		if rr := doAuthJSON(t, handler, "GET", wrong, nil, token); rr.Code < 400 {
			t.Fatal("capability crossed attempt boundary")
		}
		playlist := doAuthJSON(t, handler, "GET", media, nil, token)
		if playlist.Code != 200 {
			t.Fatalf("playlist status=%d body=%s", playlist.Code, playlist.Body.String())
		}
		var segment string
		for _, line := range strings.Split(playlist.Body.String(), "\n") {
			if strings.HasPrefix(line, "/eio/playback/v1/") {
				segment = line
			}
		}
		if segment == "" || !strings.Contains(segment, attempt) {
			t.Fatal("derived resource lost attempt")
		}
		// Refresh uses identical attested principal+identity, not a frozen token.
		wantToken = "tok-refreshed"
		c.setOnlineWithIdentity(wantToken, "user-a", c.resolveIdentitySnapshot(nil, app.Identity, nil))
		rr := doAuthJSON(t, handler, "GET", segment, nil, token)
		if rr.Code != 200 {
			t.Fatalf("segment after refresh status=%d", rr.Code)
		}
		event := map[string]any{"ItemId": item, "MediaSourceId": source["Id"], "PlaySessionId": "session-a", "EioAttemptId": attempt, "PositionTicks": int64(100)}
		eventPath := "/eio/playback/v1/" + attempt + "/Sessions/Playing"
		for _, suffix := range []string{"", "/Progress", "/Stopped"} {
			rr = doAuthJSON(t, handler, "POST", eventPath+suffix+"?api_key="+url.QueryEscape(cap), event, token)
			if rr.Code != 204 {
				t.Fatalf("event %s status=%d body=%s", suffix, rr.Code, rr.Body.String())
			}
		}
		if events != 3 || mediaHits != 2 {
			t.Fatalf("unexpected upstream request counts: media=%d events=%d", mediaHits, events)
		}
		rr = doAuthJSON(t, handler, "POST", eventPath+"/Stopped?api_key="+url.QueryEscape(cap), event, token)
		if rr.Code != 204 || events != 3 {
			t.Fatal("duplicate stop was not locally idempotent")
		}
		cleanupPath := "/eio/playback/v1/" + attempt + "/Videos/ActiveEncodings?api_key=" + url.QueryEscape(cap)
		for index := 0; index < 2; index++ {
			response := doAuthJSON(t, handler, "DELETE", cleanupPath, nil, token)
			if response.Code != 204 || cleanupHits != 1 {
				t.Fatal("session stop fabricated an encoding-cleanup ACK, or duplicate cleanup was resent")
			}
		}
		if rr = doAuthJSON(t, handler, "GET", segment, nil, token); rr.Code < 400 {
			t.Fatal("terminal attempt still served new media")
		}
	})
}
