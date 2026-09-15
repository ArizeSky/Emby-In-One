package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestSessionRoutesTranslateIDsAndBroadcastCapabilities(t *testing.T) {
	var playingBody atomic.Value
	var progressBody atomic.Value
	var stoppedBody atomic.Value
	var capabilitiesA atomic.Int32
	var capabilitiesB atomic.Int32
	var capabilitiesFullA atomic.Int32
	var capabilitiesFullB atomic.Int32

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-a", "User": map[string]any{"Id": "user-a"}})
		case r.Method == http.MethodPost && r.URL.Path == "/Sessions/Playing":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			playingBody.Store(body)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/Sessions/Playing/Progress":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			progressBody.Store(body)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/Sessions/Playing/Stopped":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			stoppedBody.Store(body)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/Sessions/Capabilities":
			capabilitiesA.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/Sessions/Capabilities/Full":
			capabilitiesFullA.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer primary.Close()

	secondary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-b", "User": map[string]any{"Id": "user-b"}})
		case r.Method == http.MethodPost && r.URL.Path == "/Sessions/Capabilities":
			capabilitiesB.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/Sessions/Capabilities/Full":
			capabilitiesFullB.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer secondary.Close()

	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test Server\"\n  id: \"server-1\"\n\nadmin:\n  username: \"admin\"\n  password: \"secret\"\n\nplayback:\n  mode: \"proxy\"\n\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\n\nproxies: []\nupstream:\n  - name: \"A\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n  - name: \"B\"\n    url: %q\n    username: \"u2\"\n    password: \"p2\"\n", primary.URL, secondary.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		virtualItem := app.IDStore.GetOrCreateVirtualID("item-a", 0)
		virtualMediaSource := app.IDStore.GetOrCreateVirtualID("ms-a", 0)
		virtualPlaySession := app.IDStore.GetOrCreateVirtualID("play-a", 0)

		body := map[string]any{
			"ItemId":        virtualItem,
			"MediaSourceId": virtualMediaSource,
			"PlaySessionId": virtualPlaySession,
			"PositionTicks": 12345,
		}

		for _, route := range []string{"/Sessions/Playing", "/Sessions/Playing/Progress", "/Sessions/Playing/Stopped"} {
			rr := doJSONRequest(t, handler, http.MethodPost, route, body, token)
			if rr.Code != http.StatusNoContent {
				t.Fatalf("%s status = %d, body=%s", route, rr.Code, rr.Body.String())
			}
		}

		assertForwardedIDs := func(name string, value atomic.Value) {
			raw := value.Load()
			payload, ok := raw.(map[string]any)
			if !ok {
				t.Fatalf("%s payload missing: %#v", name, raw)
			}
			if payload["ItemId"] != "item-a" || payload["MediaSourceId"] != "ms-a" || payload["PlaySessionId"] != "play-a" {
				t.Fatalf("%s payload not translated: %#v", name, payload)
			}
		}
		assertForwardedIDs("playing", playingBody)
		assertForwardedIDs("progress", progressBody)
		assertForwardedIDs("stopped", stoppedBody)

		rr := doJSONRequest(t, handler, http.MethodPost, "/Sessions/Capabilities?Id=session-1", map[string]any{"SupportsMediaControl": true}, token)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("capabilities status = %d, body=%s", rr.Code, rr.Body.String())
		}
		rr = doJSONRequest(t, handler, http.MethodPost, "/Sessions/Capabilities/Full", map[string]any{"PlayableMediaTypes": []string{"Video"}}, token)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("capabilities full status = %d, body=%s", rr.Code, rr.Body.String())
		}
		if capabilitiesA.Load() != 1 || capabilitiesB.Load() != 1 || capabilitiesFullA.Load() != 1 || capabilitiesFullB.Load() != 1 {
			t.Fatalf("unexpected capabilities counts: A=%d B=%d fullA=%d fullB=%d", capabilitiesA.Load(), capabilitiesB.Load(), capabilitiesFullA.Load(), capabilitiesFullB.Load())
		}
	})
}

// TestCrossServerSessionRoutesToItemIdServer verifies that when ItemId
// belongs to server A and MediaSourceId belongs to server B, session events
// are forwarded to server A (ItemId's server) with each ID independently
// resolved to its own server's original value.
func TestCrossServerSessionRoutesToItemIdServer(t *testing.T) {
	var playingBodyA atomic.Value
	var progressBodyA atomic.Value
	var stoppedBodyA atomic.Value
	var hitB atomic.Int32

	serverA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-a", "User": map[string]any{"Id": "user-a"}})
		case r.Method == http.MethodPost && r.URL.Path == "/Sessions/Playing":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			playingBodyA.Store(body)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/Sessions/Playing/Progress":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			progressBodyA.Store(body)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/Sessions/Playing/Stopped":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			stoppedBodyA.Store(body)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/Sessions/Capabilities":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/Sessions/Capabilities/Full":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer serverA.Close()

	serverB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-b", "User": map[string]any{"Id": "user-b"}})
		case r.Method == http.MethodPost && (r.URL.Path == "/Sessions/Playing" || r.URL.Path == "/Sessions/Playing/Progress" || r.URL.Path == "/Sessions/Playing/Stopped"):
			hitB.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/Sessions/Capabilities":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/Sessions/Capabilities/Full":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	defer serverB.Close()

	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test\"\n  id: \"srv\"\n\nadmin:\n  username: \"admin\"\n  password: \"secret\"\n\nplayback:\n  mode: \"proxy\"\n\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\n\nproxies: []\nupstream:\n  - name: \"A\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n  - name: \"B\"\n    url: %q\n    username: \"u2\"\n    password: \"p2\"\n", serverA.URL, serverB.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")

		// ItemId belongs to server A (index 0), MediaSourceId to server B (index 1)
		virtualItem := app.IDStore.GetOrCreateVirtualID("item-on-a", 0)
		virtualMS := app.IDStore.GetOrCreateVirtualID("ms-on-b", 1)
		virtualPlay := app.IDStore.GetOrCreateVirtualID("play-on-b", 1)

		body := map[string]any{
			"ItemId":        virtualItem,
			"MediaSourceId": virtualMS,
			"PlaySessionId": virtualPlay,
			"PositionTicks": 54321,
		}

		for _, route := range []string{"/Sessions/Playing", "/Sessions/Playing/Progress", "/Sessions/Playing/Stopped"} {
			rr := doJSONRequest(t, handler, http.MethodPost, route, body, token)
			if rr.Code != http.StatusNoContent {
				t.Fatalf("%s status = %d, body=%s", route, rr.Code, rr.Body.String())
			}
		}

		// All three events must reach server A (ItemId's server)
		for _, tc := range []struct {
			name string
			val  atomic.Value
		}{
			{"playing", playingBodyA},
			{"progress", progressBodyA},
			{"stopped", stoppedBodyA},
		} {
			raw := tc.val.Load()
			payload, ok := raw.(map[string]any)
			if !ok {
				t.Fatalf("%s payload not received on server A: %#v", tc.name, raw)
			}
			// ItemId independently resolved to server A's original
			if payload["ItemId"] != "item-on-a" {
				t.Fatalf("%s ItemId = %v, want item-on-a", tc.name, payload["ItemId"])
			}
			// MediaSourceId independently resolved to server B's original
			if payload["MediaSourceId"] != "ms-on-b" {
				t.Fatalf("%s MediaSourceId = %v, want ms-on-b", tc.name, payload["MediaSourceId"])
			}
			// PlaySessionId independently resolved to server B's original
			if payload["PlaySessionId"] != "play-on-b" {
				t.Fatalf("%s PlaySessionId = %v, want play-on-b", tc.name, payload["PlaySessionId"])
			}
		}

		// Server B must NOT receive any session events
		if hitB.Load() != 0 {
			t.Fatalf("server B received %d session events, want 0", hitB.Load())
		}
	})
}

func TestUserStateRoutesResolveIDsAndRewriteJSONResponses(t *testing.T) {
	var playingStartMediaSource atomic.Value
	var playingStopMediaSource atomic.Value
	var userDataBody atomic.Value

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-a", "User": map[string]any{"Id": "user-a"}})
		case r.Method == http.MethodPost && r.URL.Path == "/Users/user-a/PlayingItems/item-a":
			playingStartMediaSource.Store(r.URL.Query().Get("MediaSourceId"))
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && r.URL.Path == "/Users/user-a/PlayingItems/item-a":
			playingStopMediaSource.Store(r.URL.Query().Get("MediaSourceId"))
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/Users/user-a/Items/item-a/UserData":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			userDataBody.Store(body)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ItemId": "item-a",
				"Played": true,
				"UserId": "user-a",
			})
		case r.Method == http.MethodPost && r.URL.Path == "/Users/user-a/FavoriteItems/item-a":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ItemId": "item-a",
				"UserData": map[string]any{
					"ItemId":     "item-a",
					"IsFavorite": true,
				},
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/Users/user-a/FavoriteItems/item-a":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ItemId": "item-a",
				"UserData": map[string]any{
					"ItemId":     "item-a",
					"IsFavorite": false,
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test Server\"\n  id: \"server-1\"\n\nadmin:\n  username: \"admin\"\n  password: \"secret\"\n\nplayback:\n  mode: \"proxy\"\n\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\n\nproxies: []\nupstream:\n  - name: \"A\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n", upstream.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		virtualItem := app.IDStore.GetOrCreateVirtualID("item-a", 0)
		virtualMediaSource := app.IDStore.GetOrCreateVirtualID("ms-a", 0)

		rr := doJSONRequest(t, handler, http.MethodPost, "/Users/"+app.Auth.ProxyUserID()+"/PlayingItems/"+virtualItem+"?MediaSourceId="+virtualMediaSource, nil, token)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("playing start status = %d, body=%s", rr.Code, rr.Body.String())
		}
		rr = doJSONRequest(t, handler, http.MethodDelete, "/Users/"+app.Auth.ProxyUserID()+"/PlayingItems/"+virtualItem+"?MediaSourceId="+virtualMediaSource, nil, token)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("playing stop status = %d, body=%s", rr.Code, rr.Body.String())
		}
		if got, _ := playingStartMediaSource.Load().(string); got != "ms-a" {
			t.Fatalf("playing start MediaSourceId = %q, want ms-a", got)
		}
		if got, _ := playingStopMediaSource.Load().(string); got != "ms-a" {
			t.Fatalf("playing stop MediaSourceId = %q, want ms-a", got)
		}

		rr = doJSONRequest(t, handler, http.MethodPost, "/Users/"+app.Auth.ProxyUserID()+"/Items/"+virtualItem+"/UserData", map[string]any{"Played": true}, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("userdata status = %d, body=%s", rr.Code, rr.Body.String())
		}
		if raw := userDataBody.Load(); raw == nil {
			t.Fatalf("userdata body missing")
		}
		var payload map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal userdata response: %v", err)
		}
		if payload["ItemId"] == "item-a" || payload["UserId"] == "user-a" {
			t.Fatalf("userdata ids not rewritten: %#v", payload)
		}

		rr = doJSONRequest(t, handler, http.MethodPost, "/Users/"+app.Auth.ProxyUserID()+"/FavoriteItems/"+virtualItem, nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("favorite post status = %d, body=%s", rr.Code, rr.Body.String())
		}
		payload = map[string]any{}
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal favorite response: %v", err)
		}
		if payload["ItemId"] == "item-a" {
			t.Fatalf("favorite post item id not rewritten: %#v", payload)
		}
		userData, _ := payload["UserData"].(map[string]any)
		if userData["ItemId"] == "item-a" {
			t.Fatalf("favorite post nested item id not rewritten: %#v", payload)
		}

		rr = doJSONRequest(t, handler, http.MethodDelete, "/Users/"+app.Auth.ProxyUserID()+"/FavoriteItems/"+virtualItem, nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("favorite delete status = %d, body=%s", rr.Code, rr.Body.String())
		}
		payload = map[string]any{}
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal favorite delete response: %v", err)
		}
		userData, _ = payload["UserData"].(map[string]any)
		if userData["ItemId"] == "item-a" {
			t.Fatalf("favorite delete nested item id not rewritten: %#v", payload)
		}
	})
}
