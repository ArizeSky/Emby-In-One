package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// narrowWindowServer models a modified Emby deployment that refuses an item window wider
// than maxWindow with a bare 400 instead of clamping it, which is how the deployment this
// was found on behaves. It reports every window it was asked for so a test can assert the
// proxy never exceeds the ceiling.
type narrowWindowServer struct {
	libraryID   string
	libraryName string
	collType    string
	movies      []map[string]any
	maxWindow   int

	requests   *atomic.Int64
	widestSeen *atomic.Int64
	mu         chan struct{}
	windows    []int
}

func (n *narrowWindowServer) start(t *testing.T, userID string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Users/AuthenticateByName"):
			writeJSON(w, http.StatusOK, map[string]any{
				"AccessToken": "tok-" + userID,
				"User":        map[string]any{"Id": userID, "Name": userID},
			})
			return
		case strings.HasSuffix(r.URL.Path, "/System/Info/Public"):
			writeJSON(w, http.StatusOK, map[string]any{"Id": "srv-" + userID, "ServerName": userID})
			return
		case strings.HasSuffix(r.URL.Path, "/Users/"+userID+"/Views"):
			writeJSON(w, http.StatusOK, map[string]any{
				"Items": []any{map[string]any{
					"Id": n.libraryID, "Name": n.libraryName,
					"Type": "CollectionFolder", "CollectionType": n.collType,
				}},
				"TotalRecordCount": 1,
			})
			return
		case strings.HasSuffix(r.URL.Path, "/Users/"+userID+"/Items"):
			n.serveItems(w, r)
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "no route " + r.URL.Path})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func (n *narrowWindowServer) serveItems(w http.ResponseWriter, r *http.Request) {
	if n.requests != nil {
		n.requests.Add(1)
	}
	window, _ := strconv.Atoi(r.URL.Query().Get("Limit"))
	start, _ := strconv.Atoi(r.URL.Query().Get("StartIndex"))
	if n.mu != nil {
		n.mu <- struct{}{}
		n.windows = append(n.windows, window)
		<-n.mu
	}
	if n.widestSeen != nil && int64(window) > n.widestSeen.Load() {
		n.widestSeen.Store(int64(window))
	}
	if window > n.maxWindow {
		// A bare 400 with no body, exactly like the server this was found on.
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	filtered := n.movies
	if term := r.URL.Query().Get("SearchTerm"); term != "" {
		filtered = nil
		for _, m := range n.movies {
			if name, _ := m["Name"].(string); strings.Contains(name, term) {
				filtered = append(filtered, m)
			}
		}
	}
	if start > len(filtered) {
		start = len(filtered)
	}
	end := start + window
	if end > len(filtered) {
		end = len(filtered)
	}
	page := filtered[start:end]
	if page == nil {
		page = []map[string]any{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"Items":            toAnySlice(page),
		"TotalRecordCount": len(filtered),
		"StartIndex":       start,
	})
}

func (n *narrowWindowServer) seenWindows() []int {
	if n.mu == nil {
		return nil
	}
	n.mu <- struct{}{}
	defer func() { <-n.mu }()
	out := make([]int, len(n.windows))
	copy(out, n.windows)
	return out
}

// upstreamEntryYAML renders one upstream list entry; pagedScan marks the upstream as
// one that needs the narrow-window paged scan.
func upstreamEntryYAML(name, url string, pagedScan bool) string {
	s := fmt.Sprintf("  - name: %q\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n", name, url)
	if pagedScan {
		s += "    pagedScan: true\n"
	}
	return s
}

// twoUpstreamConfig builds a proxy config with one or two upstreams. An empty secondURL
// leaves the proxy with a single upstream, which is the shape the paging tests use when
// only one server is under test.
func twoUpstreamConfig(firstURL, secondURL string) string {
	return upstreamsConfigYAML(upstreamEntryYAML("A", firstURL, false),
		upstreamEntryYAML("B", secondURL, false))
}

// twoUpstreamConfigPaged is twoUpstreamConfig with per-upstream pagedScan flags.
func twoUpstreamConfigPaged(firstURL, secondURL string, firstPaged, secondPaged bool) string {
	return upstreamsConfigYAML(upstreamEntryYAML("A", firstURL, firstPaged),
		upstreamEntryYAML("B", secondURL, secondPaged))
}

func upstreamsConfigYAML(entries ...string) string {
	upstreams := strings.Join(entries, "")
	return fmt.Sprintf(`server:
  port: 8096
  name: "Test Server"
  id: "server-1"

admin:
  username: "admin"
  password: "secret"

playback:
  mode: "proxy"

timeouts:
  api: 30000
  global: 15000
  login: 10000
  healthCheck: 10000
  healthInterval: 60000

proxies: []
upstream:
%s`, upstreams)
}

func windowMovie(id, tmdb, name string, year int) map[string]any {
	return map[string]any{
		"Id": id, "Name": name, "Type": "Movie", "ProductionYear": year,
		"ProviderIds":  map[string]any{"Tmdb": tmdb},
		"MediaSources": []any{map[string]any{"Id": "ms-" + id, "Name": name + ".mp4", "Container": "mp4"}},
	}
}

// TestMergedQueryStaysUnderUpstreamWindowCeiling is the core regression: a merged query
// must not ask a pagedScan upstream for more rows in one request than that upstream
// accepts. Without the pagedScan mark the proxy asks for mergedItemsScanLimit (5000) in
// a single request; a server that refuses a wider window answers 400 and the search
// comes back with nothing but the other upstreams' rows.
func TestMergedQueryStaysUnderUpstreamWindowCeiling(t *testing.T) {
	var aReqs, bReqs, aWidest, bWidest atomic.Int64
	// 450 movies on the narrow server, so a single 200-row window cannot cover it and
	// paging has to kick in for the tail to be reachable.
	bMovies := make([]map[string]any, 0, 450)
	for i := 0; i < 450; i++ {
		bMovies = append(bMovies, windowMovie(
			fmt.Sprintf("m_%d", 900000+i), fmt.Sprintf("%d", 900000+i),
			fmt.Sprintf("B独有片 %03d", i), 2020+i%5))
	}
	a := &narrowWindowServer{
		libraryID: "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6", libraryName: "电影", collType: "movies",
		movies: []map[string]any{
			windowMovie("1111111111111111111111111111aaaa", "1491920", "功夫女足", 2026),
		},
		maxWindow: 100000, requests: &aReqs, widestSeen: &aWidest,
	}
	b := &narrowWindowServer{
		libraryID: "view_华语电影", libraryName: "华语电影", collType: "movies",
		movies: bMovies, maxWindow: 200, requests: &bReqs, widestSeen: &bWidest,
		mu: make(chan struct{}, 1),
	}
	srvA := a.start(t, "user-a")
	srvB := b.start(t, "user-b")

	withTempAppConfig(t, twoUpstreamConfigPaged(srvA.URL, srvB.URL, false, true), func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		uid := app.Auth.ProxyUserID()

		rr := doJSONRequest(t, handler, http.MethodGet,
			"/Users/"+uid+"/Items?SearchTerm=B%E7%8B%AC%E6%9C%89%E7%89%87&Limit=500", nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("search status = %d, want 200; body=%s", rr.Code, rr.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal search: %v", err)
		}
		items, _ := payload["Items"].([]any)
		if len(items) == 0 {
			t.Fatalf("search returned nothing; the narrow upstream's rows never arrived (bReqs=%d, widest=%d)\nwindows: %v",
				bReqs.Load(), bWidest.Load(), b.seenWindows())
		}
		if got := bWidest.Load(); got > 200 {
			t.Errorf("proxy asked the pagedScan upstream for a window of %d, above the 200 it accepts", got)
		}
		if bReqs.Load() < 2 {
			t.Errorf("narrow upstream saw %d request(s); a 450-row library at 200 per page needs several", bReqs.Load())
		}
		// The healthy upstream keeps the default: one wide request for the whole set.
		if aReqs.Load() != 1 {
			t.Errorf("the unmarked upstream saw %d request(s), want a single wide request", aReqs.Load())
		}
		// Every page of the narrow upstream's rows must be reachable, not just the
		// first 200: the last movie only shows up once the paging has walked past it.
		names := map[string]bool{}
		for _, raw := range items {
			m, _ := raw.(map[string]any)
			if n, _ := m["Name"].(string); n != "" {
				names[n] = true
			}
		}
		if !names["B独有片 000"] || !names["B独有片 449"] {
			t.Errorf("paging did not reach the whole library; first=%v last=%v (total rows seen=%d)",
				names["B独有片 000"], names["B独有片 449"], len(names))
		}
	})
}

// TestMergedQueryNarrowsToLowerCeilingInsteadOfDroppingServer covers an upstream whose
// ceiling is below the default: the first attempt is refused, so the proxy retries once
// with a smaller window instead of discarding that server's rows.
func TestMergedQueryNarrowsToLowerCeilingInsteadOfDroppingServer(t *testing.T) {
	var aReqs, bReqs, widest atomic.Int64
	a := &narrowWindowServer{
		libraryID: "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6", libraryName: "电影", collType: "movies",
		movies: []map[string]any{
			windowMovie("1111111111111111111111111111aaaa", "1491920", "功夫女足", 2026),
		},
		maxWindow: 100000, requests: &aReqs, widestSeen: &widest,
	}
	// Ceiling of 50: below both the 200 default and the 100 fallback window, so this
	// server can only be narrowed, never satisfied.
	b := &narrowWindowServer{
		libraryID: "view_华语电影", libraryName: "华语电影", collType: "movies",
		movies: []map[string]any{
			windowMovie("m_1", "1767429", "魔根", 2024),
		},
		maxWindow: 50, requests: &bReqs, widestSeen: &widest, mu: make(chan struct{}, 1),
	}
	srvA := a.start(t, "user-a")
	srvB := b.start(t, "user-b")

	withTempAppConfig(t, twoUpstreamConfigPaged(srvA.URL, srvB.URL, false, true), func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		uid := app.Auth.ProxyUserID()

		// A server that refuses every window must not take the other one down with it.
		rr := doJSONRequest(t, handler, http.MethodGet,
			"/Users/"+uid+"/Items?SearchTerm=%E5%8A%9F%E5%A4%AB%E5%A5%B3%E8%B6%B3&Limit=50", nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 even when one upstream refuses every window; body=%s",
				rr.Code, rr.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		items, _ := payload["Items"].([]any)
		if len(items) == 0 {
			t.Fatalf("the reachable upstream returned nothing (aReqs=%d, bReqs=%d)", aReqs.Load(), bReqs.Load())
		}
		if aReqs.Load() == 0 {
			t.Errorf("the healthy upstream was never asked (aReqs=%d)", aReqs.Load())
		}
		// The refused server is retried, not silently skipped on the first failure.
		if bReqs.Load() < 2 {
			t.Errorf("refused upstream saw %d request(s); the narrower second attempt is missing", bReqs.Load())
		}
	})
}

// TestPagedFetchStopsAtUpstreamEnd guards the loop bound: a server with fewer rows than
// the window must not be asked again for a page that does not exist.
func TestPagedFetchStopsAtUpstreamEnd(t *testing.T) {
	var reqs atomic.Int64
	single := &narrowWindowServer{
		libraryID: "view_华语电影", libraryName: "华语电影", collType: "movies",
		movies: []map[string]any{
			windowMovie("m_1", "1", "唯一一部", 2024),
		},
		maxWindow: 200, requests: &reqs, mu: make(chan struct{}, 1),
	}
	srv := single.start(t, "user-a")

	withTempAppConfig(t, twoUpstreamConfigPaged(srv.URL, "", true, false), func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		uid := app.Auth.ProxyUserID()

		rr := doJSONRequest(t, handler, http.MethodGet,
			"/Users/"+uid+"/Items?SearchTerm=%E5%94%AF%E4%B8%80%E4%B8%80%E9%83%A8&Limit=50", nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		items, _ := payload["Items"].([]any)
		if len(items) != 1 {
			t.Fatalf("got %d items, want the single row this server holds; body=%s windows=%v",
				len(items), rr.Body.String(), single.seenWindows())
		}
		if reqs.Load() != 1 {
			t.Errorf("a 1-row library drew %d request(s); the loop should stop on the short first page", reqs.Load())
		}
	})
}

// TestUnmarkedUpstreamGetsOneWideRequest guards the default: an upstream without
// pagedScan must be asked for the whole scan budget in a single request, exactly the
// way stock Emby servers were always asked. Paging must stay opt-in, otherwise every
// healthy server silently pays for the one deployment that needs it.
func TestUnmarkedUpstreamGetsOneWideRequest(t *testing.T) {
	var reqs, widest atomic.Int64
	srv := (&narrowWindowServer{
		libraryID: "view_华语电影", libraryName: "华语电影", collType: "movies",
		movies: []map[string]any{
			windowMovie("m_1", "1767429", "魔根", 2024),
		},
		maxWindow: 100000, requests: &reqs, widestSeen: &widest,
	}).start(t, "user-a")

	withTempAppConfig(t, twoUpstreamConfig(srv.URL, ""), func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		uid := app.Auth.ProxyUserID()

		rr := doJSONRequest(t, handler, http.MethodGet,
			"/Users/"+uid+"/Items?SearchTerm=%E9%AD%94%E6%A0%B9&Limit=50", nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		items, _ := payload["Items"].([]any)
		if len(items) != 1 {
			t.Fatalf("got %d items, want the single row this server holds; body=%s", len(items), rr.Body.String())
		}
		if reqs.Load() != 1 {
			t.Errorf("an unmarked upstream drew %d request(s), want exactly one wide request", reqs.Load())
		}
		if got := widest.Load(); got != mergedItemsScanLimit {
			t.Errorf("unmarked upstream was asked for a window of %d, want the full %d scan budget in one request",
				got, mergedItemsScanLimit)
		}
	})
}
