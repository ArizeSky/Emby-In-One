package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestStrictPlaybackSignedFieldsAndRollback(t *testing.T) {
	const signed = "Signature=A%2fb+B&x=%20&x=+&Key-Pair-Id=fixture"
	requests := 0
	empty := false
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-a", "User": map[string]any{"Id": "user-a"}})
		case strings.HasSuffix(r.URL.Path, "/PlaybackInfo"):
			sources := []any{}
			if !empty {
				sources = append(sources, map[string]any{"Id": "source-a", "Container": "mp4", "TranscodingUrl": "/Videos/item-a/master.m3u8?" + signed, "MediaStreams": []any{map[string]any{"Type": "Subtitle", "Index": 1, "Codec": "webvtt", "DeliveryUrl": "/Videos/item-a/source-a/Subtitles/1/Stream.vtt?" + signed}}})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"MediaSources": sources})
		case r.URL.Path == "/Items/item-a":
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "item-a", "Type": "Movie", "OfficialRating": "G"})
		case strings.HasPrefix(r.URL.Path, "/Videos/item-a/"):
			requests++
			if r.URL.RawQuery != signed {
				t.Error("signed upstream query bytes were modified")
			}
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("synthetic resource"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	withTempAppConfig(t, playbackLimiterConfig(up.URL), func(app *App, handler http.Handler) {
		token := createRegularUser(t, handler)
		item := app.IDStore.GetOrCreateVirtualID("item-a", app.Upstream.Clients()[0].ID)
		path := "/eio/playback/v1/Items/" + item + "/PlaybackInfo"
		rr := doAuthJSON(t, handler, "GET", path, nil, token)
		if rr.Code != 200 {
			t.Fatalf("negotiation status=%d body=%s", rr.Code, rr.Body.String())
		}
		var info map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &info)
		source := info["MediaSources"].([]any)[0].(map[string]any)
		subtitle := source["MediaStreams"].([]any)[0].(map[string]any)
		for _, local := range []string{playbackStringField(source, "TranscodingUrl"), playbackStringField(subtitle, "DeliveryUrl")} {
			if strings.Contains(local, "Signature") || !strings.Contains(local, "/eio-resource/") {
				t.Fatal("signed URL was exposed instead of registered")
			}
			if response := doAuthJSON(t, handler, "GET", local, nil, token); response.Code != 200 {
				t.Fatalf("registered signed resource status=%d", response.Code)
			}
		}
		if requests != 2 {
			t.Fatal("signed resource paths not actually exercised")
		}
		empty = true
		rr = doAuthJSON(t, handler, "GET", path, nil, token)
		if rr.Code != 502 || !strings.Contains(rr.Body.String(), "playback-unbound-source") {
			t.Fatal("empty strict response was published as successful negotiation")
		}
		empty = false
		app.StreamResources.max = 1
		before := app.PlaybackCapabilities.count()
		rr = doAuthJSON(t, handler, "GET", path, nil, token)
		if rr.Code != 503 {
			t.Fatalf("capacity failure status=%d", rr.Code)
		}
		if app.PlaybackCapabilities.count() != before {
			t.Fatal("failed registration leaked candidate capability")
		}
	})
}
