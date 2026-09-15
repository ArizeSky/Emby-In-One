package backend

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const panelTestConfig = `server:
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
upstream: []
`

// panelWorkspace returns a working directory with a public/ directory in it, which is
// what makes detectPublicDir() prefer the filesystem over the embedded copy. The
// caller decides what that directory holds.
func panelWorkspace(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(panelTestConfig), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "public"), 0o755); err != nil {
		t.Fatalf("create public dir: %v", err)
	}
	chdirForTest(t, dir)
	return dir
}

// withEmbeddedPanel serves the admin routes from the embedded assets rather than from
// disk, so these tests see the real panel. The working tree here deliberately has a
// public/ directory without an admin.html in it, because that is the only thing
// detectPublicDir() looks for.
func withEmbeddedPanel(t *testing.T, fn func(handler http.Handler)) {
	t.Helper()
	panelWorkspace(t)
	_, handler := newTestApp(t)
	fn(handler)
}

// withDiskPanel reproduces what the install scripts leave behind: public/admin.html
// and public/admin.js are fetched from the release, and public/vendor/ is never
// created because the scripts do not know about it. The markup written here is a
// stand-in, not the real page — these tests are about which server answers a request,
// not about what the file says.
func withDiskPanel(t *testing.T, fn func(handler http.Handler)) {
	t.Helper()
	dir := panelWorkspace(t)
	for name, body := range map[string]string{
		"admin.html": "<!DOCTYPE html><html><body>panel-from-disk</body></html>",
		"admin.js":   "// panel script from disk",
	} {
		if err := os.WriteFile(filepath.Join(dir, "public", name), []byte(body), 0o644); err != nil {
			t.Fatalf("write public/%s: %v", name, err)
		}
	}
	_, handler := newTestApp(t)
	fn(handler)
}

// The panel used to pull Tailwind, Vue, lucide and Inter from public CDNs, and the CSP
// had to name every one of those origins in script-src/style-src/font-src. Self-hosting
// them under public/vendor/ is what allowed the policy to drop all third-party origins
// and both 'unsafe-inline' sources, so these tests pin the result rather than the
// mechanism that produced it.

var (
	adminScriptTag     = regexp.MustCompile(`(?is)<script\b[^>]*>`)
	adminInlineHandler = regexp.MustCompile(`(?i)\son[a-z]+\s*=`)
	adminStyleAttr     = regexp.MustCompile(`(?i)\sstyle\s*=`)
	adminStyleElement  = regexp.MustCompile(`(?i)<style\b`)
	adminAssetRef      = regexp.MustCompile(`(?is)<(?:script|link)\b[^>]*\b(?:src|href)\s*=\s*"([^"]*)"`)
	// Comments are not parsed as markup, so a comment mentioning <style> is not an
	// inline style source. Strip them before scanning the page.
	adminHTMLComment = regexp.MustCompile(`(?s)<!--.*?-->`)
)

func fetchAdminPath(t *testing.T, handler http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200 (body=%s)", path, rr.Code, rr.Body.String())
	}
	return rr
}

func cspDirectives(t *testing.T, header string) map[string]string {
	t.Helper()
	if header == "" {
		t.Fatal("the admin page carries no Content-Security-Policy header")
	}
	out := map[string]string{}
	for _, part := range strings.Split(header, ";") {
		fields := strings.Fields(part)
		if len(fields) == 0 {
			continue
		}
		out[fields[0]] = strings.Join(fields[1:], " ")
	}
	return out
}

// TestAdminPanelCSPKeepsEverySourceOnThisOrigin covers the policy itself: no remote
// origin to load a script from, and no inline escape hatch to run one with.
func TestAdminPanelCSPKeepsEverySourceOnThisOrigin(t *testing.T) {
	withEmbeddedPanel(t, func(handler http.Handler) {
		rr := fetchAdminPath(t, handler, "/admin/admin.html")
		csp := rr.Header().Get("Content-Security-Policy")

		if strings.Contains(csp, "http://") || strings.Contains(csp, "https://") {
			t.Fatalf("the CSP still names a third-party origin: %q", csp)
		}
		directives := cspDirectives(t, csp)

		if got := directives["script-src"]; strings.Contains(got, "'unsafe-inline'") {
			t.Fatalf("script-src still allows inline script: %q", got)
		}
		if got := directives["style-src"]; got != "'self'" {
			t.Fatalf("style-src = %q, want 'self'", got)
		}
		if got := directives["font-src"]; got != "'self'" {
			t.Fatalf("font-src = %q, want 'self'", got)
		}
		// Kept on purpose: the panel mounts an in-DOM template and vue.global.prod.js
		// compiles it with new Function() at runtime.
		if got := directives["script-src"]; !strings.Contains(got, "'unsafe-eval'") {
			t.Fatalf("script-src = %q, want 'unsafe-eval' retained for the Vue in-DOM template", got)
		}
		for _, name := range []string{"object-src", "base-uri"} {
			if got := directives[name]; got != "'none'" {
				t.Fatalf("%s = %q, want 'none'", name, got)
			}
		}
		// Dropped on purpose: browsers removed the filter, and the legacy Safari
		// auditor it enabled introduced bypasses of its own.
		if got := rr.Header().Get("X-XSS-Protection"); got != "" {
			t.Fatalf("X-XSS-Protection = %q, want the header gone", got)
		}
	})
}

// TestAdminPanelPageKeepsToSelfHostedAssets covers the other half of the bargain: the
// tightened policy is only correct while the page really has no inline script, inline
// handler, inline style attribute or inline <style> element.
func TestAdminPanelPageKeepsToSelfHostedAssets(t *testing.T) {
	withEmbeddedPanel(t, func(handler http.Handler) {
		page := adminHTMLComment.ReplaceAllString(
			fetchAdminPath(t, handler, "/admin/admin.html").Body.String(), "")

		for _, tag := range adminScriptTag.FindAllString(page, -1) {
			if !strings.Contains(tag, "src=") {
				t.Fatalf("admin.html carries an inline <script>, which script-src 'self' forbids: %s", tag)
			}
		}
		if found := adminInlineHandler.FindString(page); found != "" {
			t.Fatalf("admin.html carries the inline handler %q, which script-src 'self' forbids", found)
		}
		if found := adminStyleAttr.FindString(page); found != "" {
			t.Fatalf("admin.html carries the inline attribute %q, which style-src 'self' forbids", found)
		}
		if found := adminStyleElement.FindString(page); found != "" {
			t.Fatalf("admin.html carries an inline <style> element, which style-src 'self' forbids")
		}

		refs := map[string]bool{}
		for _, match := range adminAssetRef.FindAllStringSubmatch(page, -1) {
			refs[match[1]] = true
		}
		for _, want := range []string{
			"vendor/tailwind.css",
			"vendor/inter.css",
			"vendor/vue.global.prod.js",
			"vendor/lucide.min.js",
		} {
			if !refs[want] {
				t.Fatalf("admin.html does not load %s (it loads %v)", want, refs)
			}
		}
		for ref := range refs {
			if strings.Contains(ref, "//") || strings.Contains(ref, ":") {
				t.Fatalf("admin.html loads %q from another origin; every asset must be self-hosted", ref)
			}
		}
	})
}

// TestAdminPanelVendorAssetsAreServedWithUsableTypes guards the MIME types: the panel
// is served with X-Content-Type-Options: nosniff, so a script or stylesheet that comes
// back as the wrong type is refused by the browser and the panel stops working.
func TestAdminPanelVendorAssetsAreServedWithUsableTypes(t *testing.T) {
	withEmbeddedPanel(t, func(handler http.Handler) {
		for path, wantType := range map[string]string{
			"/admin/vendor/tailwind.css":       "text/css",
			"/admin/vendor/inter.css":          "text/css",
			"/admin/vendor/vue.global.prod.js": "javascript",
			"/admin/vendor/lucide.min.js":      "javascript",
			"/admin/vendor/inter-latin.woff2":  "font/woff2",
		} {
			rr := fetchAdminPath(t, handler, path)
			if rr.Body.Len() == 0 {
				t.Fatalf("%s served an empty body", path)
			}
			if got := rr.Header().Get("Content-Type"); !strings.Contains(got, wantType) {
				t.Fatalf("%s Content-Type = %q, want it to contain %q", path, got, wantType)
			}
		}
	})
}

// TestAdminPanelIsServedFromTheDirectoryURL pins the entry point the panel is reached
// by. The address bar used to end up on /admin/admin.html — a nested path that named
// the file and repeated "admin" — because the markup's asset references are relative
// and only resolve under a directory URL. Answering /admin/ itself keeps that property
// without the second "admin", so the redirect target and the served page are asserted
// together here: changing one without the other breaks the panel's stylesheet.
func TestAdminPanelIsServedFromTheDirectoryURL(t *testing.T) {
	withEmbeddedPanel(t, func(handler http.Handler) {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/admin", nil))
		if rr.Code != http.StatusFound {
			t.Fatalf("GET /admin = %d, want 302 (body=%s)", rr.Code, rr.Body.String())
		}
		if got := rr.Header().Get("Location"); got != "/admin/" {
			t.Fatalf("GET /admin redirected to %q, want /admin/", got)
		}

		// http.FileServer answers a directory with a listing under the same 200, so the
		// assertion has to be about which of the two pages came back, not the status.
		body := fetchAdminPath(t, handler, "/admin/").Body.String()
		if !strings.Contains(body, "<title>Emby-in-One 管理面板</title>") {
			t.Fatalf("GET /admin/ answered with something other than the panel: %.200s", body)
		}
	})
}

// TestAdminPanelDirectoryURLPrefersTheDiskCopy is TestAdminPanelPrefersTheDiskCopyWhen-
// ItExists reached through the entry point users hit: serving the panel from the
// directory must not route around the developer override (edit public/admin.html,
// reload, no rebuild).
func TestAdminPanelDirectoryURLPrefersTheDiskCopy(t *testing.T) {
	withDiskPanel(t, func(handler http.Handler) {
		if body := fetchAdminPath(t, handler, "/admin/").Body.String(); !strings.Contains(body, "panel-from-disk") {
			t.Fatalf("GET /admin/ did not come from the public/ directory on disk: %s", body)
		}
	})
}

// TestAdminPanelServesVendoredAssetsWhenDiskCopyLacksThem is the regression guard for
// the installed layout. public/ on disk used to win outright, so an install that had
// admin.html and admin.js but no public/vendor/ served a panel whose stylesheet and
// scripts 404ed — a blank page with no clue in the browser. Every vendored asset has
// to keep resolving to the embedded copy.
func TestAdminPanelServesVendoredAssetsWhenDiskCopyLacksThem(t *testing.T) {
	withDiskPanel(t, func(handler http.Handler) {
		page := fetchAdminPath(t, handler, "/admin/admin.html").Body.String()
		if !strings.Contains(page, "panel-from-disk") {
			t.Fatalf("admin.html did not come from the public/ directory on disk: %s", page)
		}

		for path, wantType := range map[string]string{
			"/admin/vendor/tailwind.css":       "text/css",
			"/admin/vendor/inter.css":          "text/css",
			"/admin/vendor/vue.global.prod.js": "javascript",
			"/admin/vendor/lucide.min.js":      "javascript",
			"/admin/vendor/inter-latin.woff2":  "font/woff2",
		} {
			rr := fetchAdminPath(t, handler, path)
			if rr.Body.Len() == 0 {
				t.Fatalf("%s served an empty body", path)
			}
			if got := rr.Header().Get("Content-Type"); !strings.Contains(got, wantType) {
				t.Fatalf("%s Content-Type = %q, want it to contain %q", path, got, wantType)
			}
		}
	})
}

// TestAdminPanelPrefersTheDiskCopyWhenItExists pins the other direction: the fallback
// must not take over files the directory really does hold, or the developer override
// (edit public/admin.html, reload, no rebuild) would quietly stop working.
func TestAdminPanelPrefersTheDiskCopyWhenItExists(t *testing.T) {
	withDiskPanel(t, func(handler http.Handler) {
		if body := fetchAdminPath(t, handler, "/admin/admin.js").Body.String(); !strings.Contains(body, "panel script from disk") {
			t.Fatalf("admin.js did not come from the public/ directory on disk: %s", body)
		}
	})
}

// TestAdminPanelDiskFallbackRefusesBackslashPaths pins the one rule the lookup adds
// on top of what the standard library already enforces.
//
// An end-to-end traversal test would be theatre here: for a request path of
// "/..\\..\\panel-secret.txt" three standard-library layers each stop it
// before anything is served, whichever way the disk branch is written - filepath.Join
// cleans the path it is given, net/http's containsDotDot treats a backslash as a
// separator (so ServeFile answers 400 "invalid URL path"), and http.Dir refuses to open
// a name containing a backslash. A test asserting "the secret was not served" therefore
// passes no matter what this code does, which is worse than no test.
//
// What is falsifiable is hasDiskFile's own contract, and only because the layout below
// puts the escape target exactly where the lookup would land: dir is two levels under
// root, so ".." twice reaches root, and root/panel-secret.txt really exists. Drop the
// guard and the backslash case below returns true instead of false.
func TestAdminPanelDiskFallbackRefusesBackslashPaths(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "work", "public")
	if err := os.MkdirAll(filepath.Join(dir, "vendor"), 0o755); err != nil {
		t.Fatalf("create vendor dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "admin.html"), []byte("panel"), 0o644); err != nil {
		t.Fatalf("write admin.html: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "panel-secret.txt"), []byte("out-of-bounds"), 0o644); err != nil {
		t.Fatalf("write escape target: %v", err)
	}

	for _, tc := range []struct {
		name    string
		path    string
		wantHit bool
	}{
		{"file on disk", "/admin.html", true},
		{"file that is not there", "/vendor/tailwind.css", false},
		{"directory without index.html", "/vendor", false},
		{"slash traversal already collapsed", "/../admin.html", true},
		{"backslash traversal", "/..\\..\\panel-secret.txt", false},
		{"backslash without dots", "/vendor\\admin.html", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hasDiskFile(dir, tc.path); got != tc.wantHit {
				t.Fatalf("hasDiskFile(%q) = %v, want %v", tc.path, got, tc.wantHit)
			}
		})
	}
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return wd
}

// TestAdminPanelNamesTheUpstreamRedirectSetting guards the wording of the
// followRedirects control. The setting decides whether an upstream's
// 301/302/303/307/308 response is followed, which is the opposite direction from
// the playback mode's "直连模式 (302)", where this proxy is the one answering
// with a 302. A label that names only the status code leaves the reader unable to
// tell the two apart, so the label has to name the action and the direction.
// adminFollowRedirectsLabel captures the label text of the control bound to
// serverForm.followRedirects.
var adminFollowRedirectsLabel = regexp.MustCompile(`<label[^>]*>([^<]*)</label><select v-model="serverForm\.followRedirects"`)

func TestAdminPanelNamesTheUpstreamRedirectSetting(t *testing.T) {
	withEmbeddedPanel(t, func(handler http.Handler) {
		page := fetchAdminPath(t, handler, "/admin/admin.html").Body.String()

		label := adminFollowRedirectsLabel.FindStringSubmatch(page)
		if label == nil {
			t.Fatalf("admin.html no longer has a label bound to serverForm.followRedirects")
		}
		text := label[1]
		if !strings.Contains(text, "跟随") {
			t.Fatalf("the followRedirects label %q does not say what happens to the redirect", text)
		}
		if strings.Contains(text, "302") {
			t.Fatalf("the followRedirects label %q names only one status code", text)
		}
		if strings.Contains(text, "自动") && !strings.Contains(text, "上游") {
			t.Fatalf("the followRedirects label %q does not say who redirects", text)
		}
	})
}
