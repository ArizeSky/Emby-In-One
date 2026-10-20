package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// The local NextUp computation used to query one series at a time, so a user
// with N in-progress series waited N upstream round trips before the first
// item rendered. The series are now fetched in bounded parallel waves.
func TestLocalNextUpFetchesSeriesConcurrently(t *testing.T) {
	var inFlight atomic.Int32
	var maxInFlight atomic.Int32

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-a", "User": map[string]any{"Id": "user-a"}})
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/Shows/") && strings.HasSuffix(r.URL.Path, "/Episodes"):
			current := inFlight.Add(1)
			for {
				observed := maxInFlight.Load()
				if current <= observed || maxInFlight.CompareAndSwap(observed, current) {
					break
				}
			}
			time.Sleep(60 * time.Millisecond)
			inFlight.Add(-1)
			series := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/Users/user-a/Shows/"), "/Episodes")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Items": []map[string]any{{
					"Id":               "next-" + series,
					"SeriesId":         series,
					"ParentIndexNumber": 1,
					"IndexNumber":      2,
				}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	withTempAppConfig(t, singleUpstreamConfig(upstream.URL), func(app *App, handler http.Handler) {
		userToken := createRegularUser(t, handler)
		userID := watchUserID(t, app, "child")
		serverID := app.Upstream.Clients()[0].ID

		// Four in-progress series, each with S1E1 played.
		for i := 1; i <= 4; i++ {
			series := fmt.Sprintf("series-%d", i)
			err := app.WatchStore.RecordProgress(&WatchProgress{
				ProxyUserID:       userID,
				VirtualItemID:     fmt.Sprintf("ep-1-%d", i),
				ServerID:          serverID,
				OriginalItemID:    fmt.Sprintf("orig-ep-1-%d", i),
				ItemType:          "Episode",
				SeriesVirtualID:   series,
				SeriesOriginalID:  series,
				SeriesName:        fmt.Sprintf("Series %d", i),
				ParentIndexNumber: 1,
				IndexNumber:       1,
				Played:            true,
			})
			if err != nil {
				t.Fatalf("seed series %d: %v", i, err)
			}
		}

		rr := doAuthJSON(t, handler, http.MethodGet, "/Shows/NextUp", nil, userToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("nextup status = %d, body=%s", rr.Code, rr.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal nextup: %v", err)
		}
		items, _ := payload["Items"].([]any)
		if len(items) != 4 {
			t.Fatalf("nextup items len = %d, want 4 (payload=%#v)", len(items), payload)
		}

		if got := maxInFlight.Load(); got < 2 {
			t.Fatalf("series were fetched serially: max concurrent episode requests = %d, want >= 2", got)
		}
	})
}
