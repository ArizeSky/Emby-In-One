package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func review23Upstream(rating string, failLookup bool) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		item := map[string]any{"Id": "film", "Name": "Restricted film", "Type": "Movie", "OfficialRating": rating}
		switch {
		case r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "up-token", "User": map[string]any{"Id": "up-user"}})
		case strings.HasPrefix(r.URL.Path, "/Videos/"):
			w.Write([]byte("VIDEO"))
		case strings.HasSuffix(r.URL.Path, "/Items/film") || r.URL.Path == "/Items/film":
			if failLookup {
				w.WriteHeader(500)
				return
			}
			_ = json.NewEncoder(w).Encode(item)
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": []any{item}, "TotalRecordCount": 1})
		}
	}))
}

func review23User(t *testing.T, app *App) (*User, string) {
	t.Helper()
	u, err := app.UserStore.Create("child", "password123", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = app.UserStore.SetMaxParentalRating(u.ID, 7); err != nil {
		t.Fatal(err)
	}
	u = app.UserStore.Get(u.ID)
	_, token, err := app.Auth.AuthenticateUser(u)
	if err != nil {
		t.Fatal(err)
	}
	return u, token
}

func TestPR23ReviewListAndDetailCoverage(t *testing.T) {
	up := review23Upstream("R", false)
	defer up.Close()
	withTempAppConfig(t, dualUpstreamConfig(up.URL, up.URL), func(app *App, h http.Handler) {
		u, token := review23User(t, app)
		id := app.IDStore.GetOrCreateVirtualID("film", app.Upstream.Clients()[0].ID)
		parent := app.IDStore.GetOrCreateVirtualID("library", app.Upstream.Clients()[0].ID)
		for _, tc := range []struct {
			name, path string
			status     int
			list       bool
		}{
			{"scoped-list", "/Users/" + u.ID + "/Items?ParentId=" + parent, 200, true},
			{"root-list", "/Users/" + u.ID + "/Items", 200, true},
			{"user-detail", "/Users/" + u.ID + "/Items/" + id, 403, false},
			{"plain-detail", "/Items/" + id, 403, false},
			{"direct-stream", "/Videos/" + id + "/stream.mkv", 403, false},
		} {
			t.Run(tc.name, func(t *testing.T) {
				rr := doJSONRequest(t, h, http.MethodGet, tc.path, nil, token)
				if rr.Code != tc.status {
					t.Errorf("status=%d want=%d", rr.Code, tc.status)
				}
				if tc.list {
					var p any
					_ = json.Unmarshal(rr.Body.Bytes(), &p)
					if len(asItems(p)) != 0 {
						t.Error("restricted item still visible")
					}
				}
			})
		}
	})
}

func TestPR23ReviewSelectedSourceMustBeChecked(t *testing.T) {
	a := review23Upstream("PG", false)
	defer a.Close()
	b := review23Upstream("R", false)
	defer b.Close()
	withTempAppConfig(t, dualUpstreamConfig(a.URL, b.URL), func(app *App, h http.Handler) {
		_, token := review23User(t, app)
		id := app.IDStore.GetOrCreateVirtualID("film", app.Upstream.Clients()[0].ID)
		source := app.IDStore.GetOrCreateVirtualID("source-b", app.Upstream.Clients()[1].ID)
		app.IDStore.AssociateAdditionalInstance(id, "film", app.Upstream.Clients()[1].ID)
		rr := doJSONRequest(t, h, http.MethodGet, "/Videos/"+id+"/stream.mkv?MediaSourceId="+source, nil, token)
		if rr.Code != 403 {
			t.Errorf("selected R-rated source played: status=%d body=%s", rr.Code, rr.Body.String())
		}
	})
}

func TestPR23ReviewLookupFailureMustNotAllowPlayback(t *testing.T) {
	up := review23Upstream("R", true)
	defer up.Close()
	withTempAppConfig(t, dualUpstreamConfig(up.URL, up.URL), func(app *App, h http.Handler) {
		_, token := review23User(t, app)
		id := app.IDStore.GetOrCreateVirtualID("film", app.Upstream.Clients()[0].ID)
		rr := doJSONRequest(t, h, http.MethodGet, "/Videos/"+id+"/stream.mkv", nil, token)
		if rr.Code == 200 {
			t.Error("lookup failure allowed playback")
		}
	})
}
