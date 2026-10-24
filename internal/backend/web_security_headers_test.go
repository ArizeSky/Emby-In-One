package backend

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// requestWebPath drives the full handler stack (middleware included) for a /web
// request and returns the recorder without requiring a specific status: the
// security headers are applied by the middleware before any handler decides
// the status, so they must be present on 200s and 404s alike.
func requestWebPath(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	return rr
}

// TestWebPlayerSecurityHeaders pins the player document's own hardening: nosniff,
// a same-origin frame policy, and a CSP that names only this origin. The static
// analysis of the client (no inline <script>, no on*= handler, but style="..."
// attributes and an in-DOM Vue template compiled with new Function()) is what
// justifies script-src's 'unsafe-eval' without 'unsafe-inline' and style-src's
// 'unsafe-inline', so those two asymmetries are asserted explicitly.
//
// The blob: grants are pinned to the vendored player's actual needs:
// libmedia's Blob-URL workers (worker-src), its AudioWorklet module
// registered from a Blob URL (script-src) and MSE playback via
// URL.createObjectURL(MediaSource) (media-src). blob: URLs are same-origin by
// construction, so they never name a third-party source.
func TestWebPlayerSecurityHeaders(t *testing.T) {
	withEmbeddedPanel(t, func(handler http.Handler) {
		rr := requestWebPath(t, handler, "/web/")
		if rr.Code != http.StatusOK {
			t.Fatalf("GET /web/ = %d (body=%s)", rr.Code, rr.Body.String())
		}

		if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
		}
		if got := rr.Header().Get("X-Frame-Options"); got != "SAMEORIGIN" {
			t.Fatalf("X-Frame-Options = %q, want SAMEORIGIN", got)
		}
		// The isolation headers belong to the /web routes themselves, not to
		// applyWebSecurityHeaders; assert they still arrive together so a
		// middleware reorder cannot silently drop either half.
		if got := rr.Header().Get("Cross-Origin-Opener-Policy"); got != "same-origin" {
			t.Fatalf("Cross-Origin-Opener-Policy = %q, want same-origin", got)
		}
		if got := rr.Header().Get("Cross-Origin-Embedder-Policy"); got != "require-corp" {
			t.Fatalf("Cross-Origin-Embedder-Policy = %q, want require-corp", got)
		}

		csp := rr.Header().Get("Content-Security-Policy")
		if csp == "" {
			t.Fatal("the web player carries no Content-Security-Policy header")
		}
		if strings.Contains(csp, "http://") || strings.Contains(csp, "https://") {
			t.Fatalf("the CSP names a third-party origin: %q", csp)
		}
		directives := cspDirectives(t, csp)

		scriptSrc := directives["script-src"]
		if !strings.Contains(scriptSrc, "'unsafe-eval'") {
			t.Fatalf("script-src = %q, want 'unsafe-eval' retained for the Vue in-DOM template and wasm compilation", scriptSrc)
		}
		if strings.Contains(scriptSrc, "'unsafe-inline'") {
			t.Fatalf("script-src = %q, want no inline script allowance", scriptSrc)
		}
		if got := directives["style-src"]; !strings.Contains(got, "'unsafe-inline'") {
			t.Fatalf("style-src = %q, want 'unsafe-inline' for the client's style= attributes", got)
		}
		if !strings.Contains(scriptSrc, "blob:") {
			// libmedia registers its AudioWorklet module from a Blob URL, and
			// worklet module loads are governed by script-src.
			t.Fatalf("script-src = %q, want blob: for the AudioWorklet module", scriptSrc)
		}
		for name, want := range map[string]string{
			"default-src":     "'self'",
			"font-src":        "'self'",
			"media-src":       "'self' blob:",
			"connect-src":     "'self'",
			"worker-src":      "'self' blob:",
			"object-src":      "'none'",
			"base-uri":        "'none'",
			"form-action":     "'self'",
			"frame-ancestors": "'self'",
		} {
			if got := directives[name]; got != want {
				t.Fatalf("%s = %q, want %q", name, got, want)
			}
		}
	})
}

// TestWebPlayerSecurityHeadersOnStaticAndMissingAssets checks the headers are
// middleware-level, not a quirk of the document handler: a static asset URL and
// a 404 for a missing file still leave the stack with nosniff and the CSP set.
func TestWebPlayerSecurityHeadersOnStaticAndMissingAssets(t *testing.T) {
	withEmbeddedPanel(t, func(handler http.Handler) {
		for _, path := range []string{"/web/web/web.html", "/web/vendor/does-not-exist.js", "/web/no/such/route"} {
			rr := requestWebPath(t, handler, path)
			if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
				t.Fatalf("GET %s: X-Content-Type-Options = %q, want nosniff", path, got)
			}
			if got := rr.Header().Get("Content-Security-Policy"); got != webPlayerCSP {
				t.Fatalf("GET %s: Content-Security-Policy = %q, want the player CSP", path, got)
			}
			if got := rr.Header().Get("X-Frame-Options"); got != "SAMEORIGIN" {
				t.Fatalf("GET %s: X-Frame-Options = %q, want SAMEORIGIN", path, got)
			}
		}
	})
}

// TestWebPlayerSecurityHeadersStayScopedToWeb guards against the policy leaking
// onto clients that only talk the Emby API: third-party Emby clients fetch the
// API directly and must not be handed the player's document policy; the admin
// panel keeps its own, stricter policy.
func TestWebPlayerSecurityHeadersStayScopedToWeb(t *testing.T) {
	withEmbeddedPanel(t, func(handler http.Handler) {
		rr := requestWebPath(t, handler, "/emby/System/Info/Public")
		if rr.Code != http.StatusOK {
			t.Fatalf("GET /emby/System/Info/Public = %d", rr.Code)
		}
		if got := rr.Header().Get("Content-Security-Policy"); got != "" {
			t.Fatalf("the Emby API carries a CSP = %q, want none", got)
		}
		if got := rr.Header().Get("X-Frame-Options"); got != "" {
			t.Fatalf("the Emby API carries X-Frame-Options = %q, want none", got)
		}

		rr = requestWebPath(t, handler, "/admin/admin.html")
		if rr.Code != http.StatusOK {
			t.Fatalf("GET /admin/admin.html = %d", rr.Code)
		}
		if got := rr.Header().Get("Content-Security-Policy"); got != adminPanelCSP {
			t.Fatalf("the admin panel CSP = %q, want the admin policy (not the player one)", got)
		}
	})
}
