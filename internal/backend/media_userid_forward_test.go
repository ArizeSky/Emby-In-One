package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A client only ever holds EIO's virtual user ID. Emby prefers a UserId carried in the
// query or body over the user the request was authenticated as, so forwarding the virtual
// ID made the upstream look up a user that does not exist there: PlaybackInfo and item
// queries answered 500 (NullReferenceException), and Views came back silently empty.
// These tests pin the upstream-bound value to the upstream's own user ID.
const upstreamUserID = "user-a"

func upstreamStub(t *testing.T, record func(r *http.Request)) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"AccessToken": "upstream-token",
				"User":        map[string]any{"Id": upstreamUserID},
			})
			return
		}
		if record != nil {
			record(r)
		}
		switch r.URL.Path {
		case "/Items/episode-1/PlaybackInfo":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"MediaSources": []map[string]any{{"Id": "ms-1", "Container": "mp4"}},
			})
		case "/Users/" + upstreamUserID + "/Views":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Items": []map[string]any{{"Id": "view-1", "Name": "Movies"}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func TestPlaybackInfoForwardsUpstreamUserID(t *testing.T) {
	var queryUserID, bodyUserID string
	upstream := upstreamStub(t, func(r *http.Request) {
		if r.URL.Path != "/Items/episode-1/PlaybackInfo" {
			return
		}
		queryUserID = r.URL.Query().Get("UserId")
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodyUserID, _ = body["UserId"].(string)
	})

	withTempAppConfig(t, singleUpstreamConfig(upstream.URL), func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		virtualEpisode := app.IDStore.GetOrCreateVirtualID("episode-1", 0)
		proxyUserID := app.Auth.ProxyUserID()

		// The client sends EIO's own user ID, in both the query and the body.
		rr := doJSONRequest(t, handler, http.MethodPost,
			"/Items/"+virtualEpisode+"/PlaybackInfo?UserId="+proxyUserID,
			map[string]any{"UserId": proxyUserID}, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("playback info status = %d, body=%s", rr.Code, rr.Body.String())
		}
		if queryUserID != upstreamUserID {
			t.Fatalf("upstream query UserId = %q, want %q", queryUserID, upstreamUserID)
		}
		if bodyUserID != upstreamUserID {
			t.Fatalf("upstream body UserId = %q, want %q", bodyUserID, upstreamUserID)
		}
	})
}

func TestUserViewsForwardsUpstreamUserID(t *testing.T) {
	var queryUserID string
	upstream := upstreamStub(t, func(r *http.Request) {
		if r.URL.Path == "/Users/"+upstreamUserID+"/Views" {
			queryUserID = r.URL.Query().Get("UserId")
		}
	})

	withTempAppConfig(t, singleUpstreamConfig(upstream.URL), func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		proxyUserID := app.Auth.ProxyUserID()

		rr := doJSONRequest(t, handler, http.MethodGet, "/Users/"+proxyUserID+"/Views?UserId="+proxyUserID, nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("views status = %d, body=%s", rr.Code, rr.Body.String())
		}
		if queryUserID != upstreamUserID {
			t.Fatalf("upstream query UserId = %q, want %q", queryUserID, upstreamUserID)
		}
	})
}
