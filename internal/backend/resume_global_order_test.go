package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Explicit order must be applied before client pagination, not to a truncated
// recent-playback prefix. All state lives in withTempAppConfig's temporary DB.
func TestLocalResumeExplicitOrderBeforePagination(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token", "User": map[string]any{"Id": "user-a"}})
		case "/Items":
			var items []any
			for _, id := range strings.Split(r.URL.Query().Get("Ids"), ",") {
				name := map[string]string{"old": "Alpha", "new": "Zulu"}[id]
				if name != "" {
					items = append(items, map[string]any{"Id": id, "Name": name, "SortName": name, "Type": "Movie"})
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": items, "TotalRecordCount": len(items)})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	withTempAppConfig(t, singleUpstreamConfig(upstream.URL), func(app *App, h http.Handler) {
		token := createRegularUser(t, h)
		userID := watchUserID(t, app, "child")
		sid := app.Upstream.Clients()[0].ID
		for i, orig := range []string{"old", "new"} {
			id := app.IDStore.GetOrCreateVirtualID(orig, sid)
			if err := app.WatchStore.RecordProgress(&WatchProgress{ProxyUserID: userID, VirtualItemID: id, ServerID: sid, OriginalItemID: orig, ItemType: "Movie", PositionTicks: 100, RuntimeTicks: 1000}); err != nil {
				t.Fatal(err)
			}
			if err := app.WatchStore.db.execParams("UPDATE user_watch_progress SET last_played = ? WHERE virtual_item_id = ?", 1000+i, id); err != nil {
				t.Fatal(err)
			}
		}
		for _, tc := range []struct {
			query, want string
			total       int
		}{
			{"SortBy=SortName&SortOrder=Ascending&Limit=1", "Alpha", 2},
			{"SortBy=SortName&SortOrder=Descending&Limit=1", "Zulu", 2},
			{"SortBy=SortName&StartIndex=1&Limit=1", "Zulu", 2},
			{"Limit=1", "Zulu", 1},
		} {
			rr := doJSONRequest(t, h, http.MethodGet, "/Users/"+app.Auth.ProxyUserID()+"/Items/Resume?"+tc.query, nil, token)
			if rr.Code != http.StatusOK {
				t.Fatalf("%s: status=%d body=%s", tc.query, rr.Code, rr.Body.String())
			}
			names := itemNames(t, rr.Body.Bytes())
			if len(names) != 1 || names[0] != tc.want || itemTotal(t, rr.Body.Bytes()) != tc.total {
				t.Errorf("%s: names=%v total=%d want %s/%d", tc.query, names, itemTotal(t, rr.Body.Bytes()), tc.want, tc.total)
			}
		}
	})
}
