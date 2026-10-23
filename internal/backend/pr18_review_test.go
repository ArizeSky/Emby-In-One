package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPR18ReviewFallbackWindowMustPersist(t *testing.T) {
	movies := make([]map[string]any, 250)
	for i := range movies {
		movies[i] = windowMovie(fmt.Sprintf("m%d", i), fmt.Sprint(i), fmt.Sprintf("Movie %d", i), 2020)
	}
	up := (&narrowWindowServer{movies: movies, maxWindow: 100}).start(t, "user-a")
	withTempAppConfig(t, twoUpstreamConfigPaged(up.URL, "", true, false), func(app *App, h http.Handler) {
		token := loginToken(t, h, "secret")
		rr := doJSONRequest(t, h, http.MethodGet, "/Users/"+app.Auth.ProxyUserID()+"/Items?Limit=500", nil, token)
		var p any
		_ = json.Unmarshal(rr.Body.Bytes(), &p)
		if got := len(asItems(p)); got != 250 {
			t.Errorf("got %d of 250 items after fallback", got)
		}
	})
}

func TestPR18ReviewDedupTypesMustStaySeparate(t *testing.T) {
	up := (&narrowWindowServer{maxWindow: 5000}).start(t, "user-a")
	withTempAppConfig(t, twoUpstreamConfig(up.URL, ""), func(app *App, h http.Handler) {
		sid := app.Upstream.Clients()[0].ID
		movie := windowMovie("movie", "123", "Movie", 2020)
		series := windowMovie("series", "123", "Series", 2021)
		series["Type"] = "Series"
		got := app.mergeRoundRobinItems([]upstreamItemsResult{{ServerID: sid, Items: []map[string]any{movie, series}}}, app.Auth.ProxyUserID())
		if len(got) != 2 {
			t.Errorf("movie and series sharing numeric TMDB id merged: got %d", len(got))
		}
	})
}

func TestPR18ReviewDedupMustRetainKnownFacts(t *testing.T) {
	up := (&narrowWindowServer{maxWindow: 5000}).start(t, "user-a")
	withTempAppConfig(t, twoUpstreamConfig(up.URL, ""), func(app *App, h http.Handler) {
		sid := app.Upstream.Clients()[0].ID
		first := windowMovie("unknown", "", "Same name", 2020)
		first["ImageTags"] = map[string]any{"Primary": "poster"}
		second := windowMovie("known-a", "100", "Same name", 2020)
		third := windowMovie("known-b", "200", "Same name", 2020)
		got := app.mergeRoundRobinItems([]upstreamItemsResult{{ServerID: sid, Items: []map[string]any{first, second, third}}}, app.Auth.ProxyUserID())
		if len(got) != 2 {
			t.Errorf("conflicting TMDB ids bridged by unknown metadata: got %d", len(got))
		}
	})
}

func TestPR18ReviewInternalHopMustKeepAuth(t *testing.T) {
	var upURL string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "up-token", "User": map[string]any{"Id": "up-user"}})
		case "/Videos/film/stream.mkv":
			http.Redirect(w, r, upURL+"/protected", 302)
		case "/protected":
			if r.Header.Get("X-Emby-Token") != "up-token" && r.URL.Query().Get("api_key") != "up-token" {
				w.WriteHeader(401)
				return
			}
			w.Write([]byte("VIDEO"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer up.Close()
	upURL = up.URL
	for _, direct := range []bool{false, true} {
		t.Run(fmt.Sprint(direct), func(t *testing.T) {
			withTempAppConfig(t, strmProxyConfig(up.URL, direct), func(app *App, h http.Handler) {
				token := loginToken(t, h, "secret")
				id := app.IDStore.GetOrCreateVirtualID("film", app.Upstream.Clients()[0].ID)
				rr := doJSONRequest(t, h, http.MethodGet, "/Videos/"+id+"/stream.mkv", nil, token)
				if rr.Code != 200 {
					t.Errorf("same-origin authenticated redirect failed: status=%d", rr.Code)
				}
			})
		})
	}
}
