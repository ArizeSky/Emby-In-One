package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// A Web request supplies local auth only. Verify the configured mode wins on
// negotiation, resource reads and events, even when another capture is present.
func TestStrictWebPlaybackUsesUpstreamIdentityMode(t *testing.T) {
	for _, mode := range []string{"passthrough", "custom", "resident"} {
		t.Run(mode, func(t *testing.T) {
			wantUA := ""
			hits := map[string]int{}
			up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/Users/AuthenticateByName" {
					_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "fixture-token", "User": map[string]any{"Id": "fixture-user"}})
					return
				}
				if wantUA != "" {
					if r.UserAgent() != wantUA || r.Header.Get("X-Emby-Device-Id") != "fixture-"+mode {
						t.Errorf("%s: configured %s identity was not preserved", r.URL.Path, mode)
					}
					if r.Header.Get("X-Emby-Token") != "fixture-token" {
						t.Error("upstream credential changed")
					}
				}
				switch {
				case strings.HasSuffix(r.URL.Path, "/PlaybackInfo"):
					hits["negotiation"]++
					_ = json.NewEncoder(w).Encode(map[string]any{"PlaySessionId": "fixture-session", "MediaSources": []any{map[string]any{"Id": "fixture-source", "Container": "mkv", "DirectStreamUrl": "/Videos/fixture-item/stream.mkv"}}})
				case r.URL.Path == "/Items/fixture-item":
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": "fixture-item", "Type": "Movie", "OfficialRating": "G"})
				case strings.HasPrefix(r.URL.Path, "/Videos/fixture-item/"):
					hits["media"]++
					_, _ = w.Write([]byte("synthetic"))
				case strings.HasPrefix(r.URL.Path, "/Sessions/Playing"):
					hits["events"]++
					var b map[string]any
					if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					if b["EioAttemptId"] != nil || b["ItemId"] != "fixture-item" || b["MediaSourceId"] != "fixture-source" || b["PlaySessionId"] != "fixture-session" {
						t.Error("event scope was not translated")
					}
					if !strings.HasSuffix(r.URL.Path, "/Stopped") && (b["PlaybackRate"] != 1.5 || b["PlayMethod"] != "DirectStream" || b["IsPaused"] != true) {
						t.Error("reported engine state was not preserved")
					}
					w.WriteHeader(204)
				default:
					http.NotFound(w, r)
				}
			}))
			defer up.Close()
			withTempAppConfig(t, playbackLimiterConfig(up.URL), func(app *App, h http.Handler) {
				c := app.Upstream.ClientByID(app.Upstream.Clients()[0].ID)
				headers := http.Header{"User-Agent": {"fixture/" + mode}, "X-Emby-Client": {"fixture"}, "X-Emby-Device-Id": {"fixture-" + mode}}
				c.mu.Lock()
				c.Config.SpoofClient = mode
				c.Config.CustomUserAgent = headers.Get("User-Agent")
				c.Config.CustomClient = "fixture"
				c.Config.CustomDeviceId = headers.Get("X-Emby-Device-Id")
				if mode == "resident" {
					c.residentIdentity = &residentIdentitySnapshot{ProfileID: "fixture-resident", IdentityVersion: 1, Headers: headers}
				}
				c.mu.Unlock()
				wrong := http.Header{"User-Agent": {"unrelated-capture"}, "X-Emby-Client": {"unrelated"}, "X-Emby-Device-Id": {"unrelated-device"}}
				app.Identity.SaveLatestCapturedHeaders(wrong)
				if mode == "passthrough" {
					app.Identity.SaveLastSuccess(c.serverKey, headers)
				} else {
					app.Identity.SaveLastSuccess(c.serverKey, wrong)
				}
				c.setOnlineWithIdentity("fixture-token", "fixture-user", c.resolveIdentitySnapshot(nil, app.Identity, nil))
				wantUA = headers.Get("User-Agent")
				token := createRegularUser(t, h)
				item := app.IDStore.GetOrCreateVirtualID("fixture-item", c.ID)
				rr := doAuthJSON(t, h, "POST", "/eio/playback/v1/Items/"+item+"/PlaybackInfo", map[string]any{"DeviceProfile": map[string]any{"MaxStreamingBitrate": 120000000}}, token)
				if rr.Code != 200 {
					t.Fatalf("negotiation %d: %s", rr.Code, rr.Body.String())
				}
				var info map[string]any
				if err := json.Unmarshal(rr.Body.Bytes(), &info); err != nil {
					t.Fatal(err)
				}
				source := info["MediaSources"].([]any)[0].(map[string]any)
				media := playbackStringField(source, "DirectStreamUrl")
				if rr = doAuthJSON(t, h, "GET", media, nil, token); rr.Code != 200 {
					t.Fatalf("media %d", rr.Code)
				}
				attempt, cap := playbackStringField(source, "EioAttemptId"), playbackStringField(source, "EioPlaybackToken")
				event := map[string]any{"EioAttemptId": attempt, "ItemId": item, "MediaSourceId": source["Id"], "PlaySessionId": "fixture-session", "PositionTicks": 10000000, "IsPaused": true, "PlaybackRate": 1.5, "PlayMethod": "DirectStream", "EventName": "Pause"}
				for _, suffix := range []string{"", "/Progress", "/Stopped"} {
					delete(event, "EventName")
					if suffix == "/Progress" {
						event["EventName"] = "Pause"
					}
					if suffix == "/Stopped" {
						for _, key := range []string{"PlaybackRate", "PlayMethod", "IsPaused"} {
							delete(event, key)
						}
					}
					rr = doAuthJSON(t, h, "POST", "/eio/playback/v1/"+attempt+"/Sessions/Playing"+suffix+"?api_key="+url.QueryEscape(cap), event, token)
					if rr.Code != 204 {
						t.Fatalf("event %s %d: %s", suffix, rr.Code, rr.Body.String())
					}
				}
				if hits["negotiation"] != 1 || hits["media"] != 1 || hits["events"] != 3 {
					t.Fatalf("incomplete runtime coverage: %v", hits)
				}
			})
		})
	}
}
