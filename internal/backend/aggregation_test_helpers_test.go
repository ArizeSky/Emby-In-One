package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// aggUpstreamStub is a minimal Emby-shaped upstream used by the aggregation
// tests. It answers login so the pool comes up online, serves just enough
// content for the routes under test, and counts login vs content requests so a
// test can assert a paused server was never contacted for content.
type aggUpstreamStub struct {
	server      *httptest.Server
	contentHits atomic.Int32
	loginHits   atomic.Int32
}

func newAggUpstreamStub(t *testing.T) *aggUpstreamStub {
	t.Helper()
	s := &aggUpstreamStub{}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName" {
			s.loginHits.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok", "User": map[string]any{"Id": "user"}})
			return
		}
		s.contentHits.Add(1)
		p := r.URL.Path
		switch {
		case strings.HasSuffix(p, "/Views"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Items":            []any{map[string]any{"Id": "lib", "Name": "Lib", "Type": "CollectionFolder"}},
				"TotalRecordCount": 1,
			})
		case strings.HasSuffix(p, "/Items/Latest"):
			_ = json.NewEncoder(w).Encode([]any{})
		case strings.Contains(p, "/Images/"):
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("png"))
		case p == "/Items" || strings.HasSuffix(p, "/Items"):
			ids := r.URL.Query().Get("Ids")
			items := make([]any, 0)
			if ids != "" {
				for _, id := range strings.Split(ids, ",") {
					items = append(items, map[string]any{
						"Id": id, "Name": "Item " + id, "Type": "Movie",
						"UserData": map[string]any{},
					})
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": items, "TotalRecordCount": len(items)})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id": "item", "Name": "Item", "Type": "Movie",
				"MediaSources": []any{map[string]any{"Id": "ms"}},
			})
		}
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *aggUpstreamStub) reset() {
	s.contentHits.Store(0)
	s.loginHits.Store(0)
}
