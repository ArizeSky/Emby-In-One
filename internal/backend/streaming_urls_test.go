package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// configWithStreamingURLs builds a single-upstream config whose stream bases are
// the given ordered list (flow-sequence syntax, as renderConfigYAML writes it).
func configWithStreamingURLs(url string, streamingURLs ...string) string {
	base := singleUpstreamConfig(url)
	if len(streamingURLs) == 0 {
		return base
	}
	if len(streamingURLs) == 1 {
		return base + fmt.Sprintf("    streamingUrl: %q\n", streamingURLs[0])
	}
	quoted := make([]string, 0, len(streamingURLs))
	for _, s := range streamingURLs {
		quoted = append(quoted, "'"+s+"'")
	}
	return base + "    streamingUrls: [" + strings.Join(quoted, ", ") + "]\n"
}

// streamingUpstream answers login and counts stream requests per listening
// address, so a test can tell which stream base served a segment.
func streamingUpstream(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "stream-token", "User": map[string]any{"Id": "stream-user"}})
		case strings.HasPrefix(r.URL.Path, "/Videos/"):
			if strings.HasSuffix(r.URL.Path, ".m3u8") {
				w.Header().Set("Content-Type", "application/x-mpegURL")
				_, _ = w.Write([]byte("#EXTM3U\nsegment1.ts\n"))
				return
			}
			hits.Add(1)
			_, _ = w.Write([]byte("segment-body"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

// A dead primary stream base must not stop playback when a fallback exists: the
// stream request is retried against the next base in order.
func TestStreamFailoverToSecondBase(t *testing.T) {
	upstream, hits := streamingUpstream(t)
	// 127.0.0.1:1 refuses connections immediately.
	config := configWithStreamingURLs(upstream.URL, "http://127.0.0.1:1", upstream.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		client := app.Upstream.GetClient(0)
		if len(client.StreamBaseURLs) != 2 {
			t.Fatalf("stream bases = %v, want 2 entries", client.StreamBaseURLs)
		}
		token := loginToken(t, handler, "secret")
		virtual := app.IDStore.GetOrCreateVirtualID("episode-1", client.ID)

		req := httptest.NewRequest(http.MethodGet, "/Videos/"+virtual+"/segment1.ts?api_key="+token, nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
		}
		if rr.Body.String() != "segment-body" {
			t.Fatalf("unexpected segment body: %q", rr.Body.String())
		}
		if hits.Load() != 1 {
			t.Fatalf("upstream segment hits = %d, want 1", hits.Load())
		}
		// The failed primary must now be marked dead so the next request goes
		// straight to the live fallback.
		candidates := client.streamBaseCandidates()
		if len(candidates) != 1 || candidates[0] != upstream.URL {
			t.Fatalf("candidates after failover = %v, want only the live base", candidates)
		}
	})
}

// A connect failure on the only stream base is reported to the client as 502
// and does not loop or panic.
func TestStreamFailoverSingleBaseStillErrors(t *testing.T) {
	upstream, _ := streamingUpstream(t)
	config := configWithStreamingURLs(upstream.URL, "http://127.0.0.1:1")

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		virtual := app.IDStore.GetOrCreateVirtualID("episode-1", app.Upstream.GetClient(0).ID)

		req := httptest.NewRequest(http.MethodGet, "/Videos/"+virtual+"/segment1.ts?api_key="+token, nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusBadGateway {
			t.Fatalf("status = %d, want 502, body=%s", rr.Code, rr.Body.String())
		}
	})
}

// A 404 from the upstream is an HTTP answer, not a dead line: no failover is
// attempted and no base is marked dead.
func TestStreamFailoverDoesNotTriggerOnHTTPStatus(t *testing.T) {
	var statusHits atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "stream-token", "User": map[string]any{"Id": "stream-user"}})
		case strings.HasPrefix(r.URL.Path, "/Videos/"):
			statusHits.Add(1)
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	fallback, fallbackHits := streamingUpstream(t)
	config := configWithStreamingURLs(upstream.URL, upstream.URL, fallback.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		client := app.Upstream.GetClient(0)
		token := loginToken(t, handler, "secret")
		virtual := app.IDStore.GetOrCreateVirtualID("episode-1", client.ID)

		req := httptest.NewRequest(http.MethodGet, "/Videos/"+virtual+"/segment1.ts?api_key="+token, nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (the upstream's own answer)", rr.Code)
		}
		if statusHits.Load() != 1 {
			t.Fatalf("primary hits = %d, want 1 (no retry after an HTTP response)", statusHits.Load())
		}
		if fallbackHits.Load() != 0 {
			t.Fatalf("fallback hits = %d, want 0 (failover must not fire on an HTTP status)", fallbackHits.Load())
		}
		if candidates := client.streamBaseCandidates(); len(candidates) != 2 {
			t.Fatalf("candidates = %v, want both bases still live", candidates)
		}
	})
}

// The liveness probe treats any HTTP response — including 403 from a
// split-tunnel line that only forwards /Videos/ — as alive, and only a
// connect-level failure as dead. A dead base is skipped by redirect-mode base
// selection; when every base is dead the full list is still returned.
func TestStreamProbeLivenessSemantics(t *testing.T) {
	upstream, _ := streamingUpstream(t)
	// A "split tunnel" that answers 403 for everything: alive by probe rules.
	var probeHits atomic.Int64
	splitTunnel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probeHits.Add(1)
		w.WriteHeader(http.StatusForbidden)
	}))
	defer splitTunnel.Close()

	config := configWithStreamingURLs(upstream.URL, "http://127.0.0.1:1", splitTunnel.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		client := app.Upstream.GetClient(0)
		client.probeStreamBases(context.Background())

		candidates := client.streamBaseCandidates()
		if len(candidates) != 1 || candidates[0] != splitTunnel.URL {
			t.Fatalf("candidates = %v, want only the split-tunnel base (dead primary pruned, 403 line alive)", candidates)
		}
		if probeHits.Load() == 0 {
			t.Fatalf("the split-tunnel line was never probed")
		}

		// Mark the 403 line dead too: with everything marked dead the full
		// ordered list must come back — a wrong verdict must not strand the
		// upstream with no base at all.
		client.markStreamBaseFailed(splitTunnel.URL)
		if all := client.streamBaseCandidates(); len(all) != 2 {
			t.Fatalf("all-dead candidates = %v, want the full ordered list", all)
		}

		// After the cooldown the dead mark expires and the base is a candidate
		// again alongside the live one.
		client.mu.Lock()
		client.streamFailures["http://127.0.0.1:1"] = time.Now().Add(-2 * streamFailureCooldown)
		client.mu.Unlock()
		client.markStreamBaseAlive(splitTunnel.URL)
		if revived := client.streamBaseCandidates(); len(revived) != 2 {
			t.Fatalf("post-cooldown candidates = %v, want both bases", revived)
		}
	})
}

// Redirect mode picks the first live stream base for its 302 target, skipping
// bases the liveness marks say are dead.
func TestRedirectModeUsesFirstLiveBase(t *testing.T) {
	upstream, _ := streamingUpstream(t)
	dead := "http://127.0.0.1:1"
	config := configWithStreamingURLs(upstream.URL, dead, upstream.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		client := app.Upstream.GetClient(0)
		client.Config.PlaybackMode = "redirect"
		client.markStreamBaseFailed(dead)

		redirectURL, err := client.BuildURL("/Videos/orig/segment.ts", nil, true, nil)
		if err != nil {
			t.Fatalf("BuildURL: %v", err)
		}
		if !strings.HasPrefix(redirectURL, upstream.URL+"/Videos/orig/segment.ts") {
			t.Fatalf("redirect URL = %q, want the live base %s", redirectURL, upstream.URL)
		}
	})
}

// The manifest a proxied HLS playback hands to the client must address every
// segment back through this proxy: proxy-relative paths, virtual item id, proxy
// token — and no upstream host anywhere, even when a streamingUrl distinct from
// the API address is configured.
func TestHLSManifestRoutesSegmentsThroughProxyWithStreamingURL(t *testing.T) {
	upstream, _ := streamingUpstream(t)
	config := configWithStreamingURLs(upstream.URL, upstream.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		client := app.Upstream.GetClient(0)
		virtual := app.IDStore.GetOrCreateVirtualID("episode-1", client.ID)

		req := httptest.NewRequest(http.MethodGet, "/Videos/"+virtual+"/master.m3u8?api_key="+token, nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
		}
		body := rr.Body.String()
		want := "/Videos/" + virtual + "/segment1.ts?api_key=" + token
		if !strings.Contains(body, want) {
			t.Fatalf("manifest missing proxy-relative segment %q: %s", want, body)
		}
		if strings.Contains(body, "127.0.0.1") {
			t.Fatalf("a host survived into the client-facing manifest: %s", body)
		}

		// The rewritten segment must actually play through the proxy.
		segment := httptest.NewRequest(http.MethodGet, want, nil)
		segmentRR := httptest.NewRecorder()
		handler.ServeHTTP(segmentRR, segment)
		if segmentRR.Code != http.StatusOK || segmentRR.Body.String() != "segment-body" {
			t.Fatalf("segment via proxy: status=%d body=%q", segmentRR.Code, segmentRR.Body.String())
		}
	})
}

// The ordered list survives a save/load round trip, the legacy single-value key
// folds into the list, and duplicates and blanks are dropped.
func TestStreamingURLsConfigRoundTrip(t *testing.T) {
	cfg, err := parseConfigYAML("server:\n  port: 8096\nadmin:\n  username: a\n  password: b\nplayback:\n  mode: proxy\nupstream:\n  - name: A\n    url: 'http://api.example'\n    username: u\n    password: p\n    streamingUrl: 'http://legacy.example/'\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	normalizeUpstream(&cfg.Upstream[0], 0, cfg)
	if len(cfg.Upstream) != 1 || len(cfg.Upstream[0].StreamingURLs) != 1 || cfg.Upstream[0].StreamingURLs[0] != "http://legacy.example" {
		t.Fatalf("legacy key not migrated: %+v", cfg.Upstream[0].StreamingURLs)
	}

	cfg2, err := parseConfigYAML("server:\n  port: 8096\nadmin:\n  username: a\n  password: b\nplayback:\n  mode: proxy\nupstream:\n  - name: A\n    url: 'http://api.example'\n    username: u\n    password: p\n    streamingUrls: ['http://a.example', 'http://a.example', '', 'http://b.example/']\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	normalizeUpstream(&cfg2.Upstream[0], 0, cfg2)
	want := []string{"http://a.example", "http://b.example"}
	if fmt.Sprint(cfg2.Upstream[0].StreamingURLs) != fmt.Sprint(want) {
		t.Fatalf("normalized list = %v, want %v", cfg2.Upstream[0].StreamingURLs, want)
	}

	rendered := renderConfigYAML(cfg2)
	if !strings.Contains(rendered, "streamingUrls: ['http://a.example', 'http://b.example']") {
		t.Fatalf("rendered config lost the ordered list:\n%s", rendered)
	}
	reloaded, err := parseConfigYAML(rendered)
	if err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if fmt.Sprint(reloaded.Upstream[0].StreamingURLs) != fmt.Sprint(want) {
		t.Fatalf("round-trip list = %v, want %v", reloaded.Upstream[0].StreamingURLs, want)
	}
}
