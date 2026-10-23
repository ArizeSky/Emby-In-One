package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// cdnFixture builds a stand-in for a netdisk CDN: a host on an unrelated address
// that serves the film bytes once a player comes asking directly.
type cdnFixture struct {
	srv       *httptest.Server
	proxyHits atomic.Int32
}

func newCDNFixture(t *testing.T) *cdnFixture {
	t.Helper()
	f := &cdnFixture{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Count every hit: with directRedirect the proxy must never come here;
		// only the player or the proxy's internal follow may.
		f.proxyHits.Add(1)
		w.Header().Set("Content-Type", "video/x-matroska")
		w.Header().Set("Accept-Ranges", "bytes")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("cdn-bytes"))
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// strmUpstreamFixture builds a STRM-shaped Emby: its stream endpoint answers with
// a 302 to the given target instead of serving bytes itself.
type strmUpstreamFixture struct {
	srv     *httptest.Server
	streams atomic.Int32
}

func newSTRMUpstream(t *testing.T, redirectTarget string) *strmUpstreamFixture {
	t.Helper()
	f := &strmUpstreamFixture{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-a", "User": map[string]any{"Id": "user-a"}})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/Videos/"):
			f.streams.Add(1)
			w.Header().Set("Location", redirectTarget)
			w.WriteHeader(http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// The generic config builder writes no followRedirects line (defaults are on), so
// the flag is appended to the upstream block directly.
func strmProxyConfig(url string, directRedirect bool) string {
	base := twoUpstreamConfig(url, "")
	if !directRedirect {
		return base
	}
	return strings.Replace(base, "    username: \"u1\"", "    username: \"u1\"\n    directRedirect: true", 1)
}

// TestStreamRedirectPassesExternalTargetToClient is the core directRedirect case:
// a STRM upstream answers with a 302 to an unrelated CDN host, and the proxy hands
// that Location to the player instead of relaying the film through its own upload.
func TestStreamRedirectPassesExternalTargetToClient(t *testing.T) {
	cdn := newCDNFixture(t)
	upstream := newSTRMUpstream(t, cdn.srv.URL+"/film.mkv?sig=abc")

	withTempAppConfig(t, strmProxyConfig(upstream.srv.URL, true), func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		virtualEpisode := app.IDStore.GetOrCreateVirtualID("episode-a", app.Upstream.Clients()[0].ID)

		rr := doJSONRequest(t, handler, http.MethodGet,
			"/Videos/"+virtualEpisode+"/stream.mkv?api_key="+token, nil, "")
		if rr.Code != http.StatusFound {
			t.Fatalf("stream status = %d, want the upstream's 302; body=%s", rr.Code, rr.Body.String())
		}
		if got := rr.Header().Get("Location"); got != cdn.srv.URL+"/film.mkv?sig=abc" {
			t.Fatalf("Location = %q, want the CDN link", got)
		}
		if n := cdn.proxyHits.Load(); n != 0 {
			t.Fatalf("proxy fetched the CDN %d time(s); the player alone must", n)
		}
	})
}

// TestStreamWithoutDirectRedirectStillRelays pins the default: without the flag the
// proxy follows the redirect itself and relays the bytes, exactly as before.
func TestStreamWithoutDirectRedirectStillRelays(t *testing.T) {
	cdn := newCDNFixture(t)
	upstream := newSTRMUpstream(t, cdn.srv.URL+"/film.mkv?sig=abc")

	withTempAppConfig(t, strmProxyConfig(upstream.srv.URL, false), func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		virtualEpisode := app.IDStore.GetOrCreateVirtualID("episode-a", app.Upstream.Clients()[0].ID)

		rr := doJSONRequest(t, handler, http.MethodGet,
			"/Videos/"+virtualEpisode+"/stream.mkv?api_key="+token, nil, "")
		if rr.Code != http.StatusPartialContent {
			t.Fatalf("stream status = %d, want the relayed 206; body=%s", rr.Code, rr.Body.String())
		}
		if got := rr.Body.String(); got != "cdn-bytes" {
			t.Fatalf("stream body = %q, want the relayed CDN bytes", got)
		}
		if n := cdn.proxyHits.Load(); n != 1 {
			t.Fatalf("CDN saw %d hit(s), want exactly the proxy's own fetch", n)
		}
	})
}

// TestStreamRedirectToUpstreamHostStaysInternal guards the leak: a redirect target
// that points back at the upstream's own host is fetched by the proxy and never
// handed to the client, because it would expose the upstream's address.
func TestStreamRedirectToUpstreamHostStaysInternal(t *testing.T) {
	var upstreamURL string
	var streams atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-a", "User": map[string]any{"Id": "user-a"}})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/Videos/"):
			streams.Add(1)
			w.Header().Set("Location", upstreamURL+"/file/episode-a.mkv")
			w.WriteHeader(http.StatusFound)
		case r.Method == http.MethodGet && r.URL.Path == "/file/episode-a.mkv":
			w.Header().Set("Content-Type", "video/x-matroska")
			_, _ = w.Write([]byte("self-hosted-bytes"))
		default:
			http.NotFound(w, r)
		}
	}))
	upstreamURL = upstream.URL
	defer upstream.Close()

	withTempAppConfig(t, strmProxyConfig(upstream.URL, true), func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		virtualEpisode := app.IDStore.GetOrCreateVirtualID("episode-a", app.Upstream.Clients()[0].ID)

		rr := doJSONRequest(t, handler, http.MethodGet,
			"/Videos/"+virtualEpisode+"/stream.mkv?api_key="+token, nil, "")
		if rr.Code != http.StatusOK {
			t.Fatalf("stream status = %d, want the internally followed 200; body=%s", rr.Code, rr.Body.String())
		}
		if got := rr.Body.String(); got != "self-hosted-bytes" {
			t.Fatalf("stream body = %q, want the internally fetched bytes", got)
		}
		if loc := rr.Header().Get("Location"); loc != "" {
			t.Fatalf("Location %q leaked to the client", loc)
		}
	})
}

// TestStreamRedirectMultiHopExternalTargetIsPassed covers the 115-style chain:
// hop 1 bounces to the upstream's own host (a signer or gateway), hop 2 leaves
// for the external CDN. The proxy fetches the internal hop, then hands the CDN
// Location to the player — relaying the film would eat the proxy's upload.
func TestStreamRedirectMultiHopExternalTargetIsPassed(t *testing.T) {
	cdn := newCDNFixture(t)
	var upstreamURL string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-a", "User": map[string]any{"Id": "user-a"}})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/Videos/"):
			w.Header().Set("Location", upstreamURL+"/gate/file.mkv")
			w.WriteHeader(http.StatusFound)
		case r.Method == http.MethodGet && r.URL.Path == "/gate/file.mkv":
			w.Header().Set("Location", cdn.srv.URL+"/film.mkv?sig=hop2")
			w.WriteHeader(http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	upstreamURL = upstream.URL
	defer upstream.Close()

	withTempAppConfig(t, strmProxyConfig(upstream.URL, true), func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		virtualEpisode := app.IDStore.GetOrCreateVirtualID("episode-a", app.Upstream.Clients()[0].ID)

		rr := doJSONRequest(t, handler, http.MethodGet,
			"/Videos/"+virtualEpisode+"/stream.mkv?api_key="+token, nil, "")
		if rr.Code != http.StatusFound {
			t.Fatalf("stream status = %d, want the final hop's 302; body=%s", rr.Code, rr.Body.String())
		}
		if got := rr.Header().Get("Location"); got != cdn.srv.URL+"/film.mkv?sig=hop2" {
			t.Fatalf("Location = %q, want the CDN link from hop 2", got)
		}
		if n := cdn.proxyHits.Load(); n != 0 {
			t.Fatalf("proxy fetched the CDN %d time(s); the player alone must", n)
		}
	})
}

// TestApplyAdminUpstreamInputDirectRedirect pins the panel round-trip: the create
// and update paths both honour an explicit flag and leave the stored value alone
// when the field is absent, so a panel save without the checkbox does not reset it.
func TestApplyAdminUpstreamInputDirectRedirect(t *testing.T) {
	yes, no := true, false

	dst := &UpstreamConfig{}
	applyAdminUpstreamInput(dst, adminUpstreamInput{DirectRedirect: &yes}, true)
	if !dst.DirectRedirect {
		t.Fatal("create with directRedirect=true must store the flag")
	}

	applyAdminUpstreamInput(dst, adminUpstreamInput{DirectRedirect: &no}, false)
	if dst.DirectRedirect {
		t.Fatal("update with directRedirect=false must clear the flag")
	}

	applyAdminUpstreamInput(dst, adminUpstreamInput{}, false)
	if dst.DirectRedirect {
		t.Fatal("update without the field must leave the stored flag untouched")
	}
}

// TestApplyAdminUpstreamInputPagedScan mirrors the directRedirect roundtrip for
// the per-upstream pagedScan switch: create stores it, update can clear it, and
// a panel save without the field leaves the yaml-set value alone.
func TestApplyAdminUpstreamInputPagedScan(t *testing.T) {
	yes, no := true, false

	dst := &UpstreamConfig{}
	applyAdminUpstreamInput(dst, adminUpstreamInput{PagedScan: &yes}, true)
	if !dst.PagedScan {
		t.Fatal("create with pagedScan=true must store the flag")
	}

	applyAdminUpstreamInput(dst, adminUpstreamInput{PagedScan: &no}, false)
	if dst.PagedScan {
		t.Fatal("update with pagedScan=false must clear the flag")
	}

	applyAdminUpstreamInput(dst, adminUpstreamInput{}, false)
	if dst.PagedScan {
		t.Fatal("update without the field must leave the stored flag untouched")
	}
}
