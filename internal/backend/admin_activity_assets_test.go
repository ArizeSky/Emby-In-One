package backend

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The K4 viewing/retention UI lives in public/admin-activity.js, which
// admin.html loads before admin.js. These tests cover the packaging contract
// only (the JS behaviour lives in tests/admin-activity.test.cjs):
//
//   - the module is embedded in the binary and served from /admin/ with a
//     JavaScript MIME type under the existing self-only CSP;
//   - the page loads it before admin.js;
//   - an installed layout that only has admin.html/admin.js on disk (the
//     release-install asset list) still gets the module through the per-file
//     embedded fallback instead of a 404 that would break the whole panel;
//   - a disk copy still wins when one exists, like every other panel asset.

var adminPanelScriptSrc = regexp.MustCompile(`(?is)<script\b[^>]*\bsrc\s*=\s*"([^"]*)"`)

func TestAdminActivityPanelLoadsModuleBeforeAppScript(t *testing.T) {
	withEmbeddedPanel(t, func(handler http.Handler) {
		page := fetchAdminPath(t, handler, "/admin/admin.html").Body.String()

		var sources []string
		for _, match := range adminPanelScriptSrc.FindAllStringSubmatch(page, -1) {
			sources = append(sources, match[1])
		}
		activityAt, appAt := -1, -1
		for i, src := range sources {
			switch src {
			case "admin-activity.js":
				activityAt = i
			case "admin.js":
				appAt = i
			}
		}
		if activityAt < 0 {
			t.Fatalf("admin.html does not load admin-activity.js (it loads %v)", sources)
		}
		if appAt < 0 {
			t.Fatalf("admin.html does not load admin.js (it loads %v)", sources)
		}
		if activityAt > appAt {
			t.Fatalf("admin-activity.js is loaded after admin.js (%v); the module must be defined first", sources)
		}
		for _, src := range sources {
			if strings.Contains(src, "//") || strings.Contains(src, ":") {
				t.Fatalf("admin.html loads %q from another origin; every asset must be self-hosted", src)
			}
		}
	})
}

// TestAdminActivityModuleServedFromEmbeddedPanel is the regression guard for the
// embed directive: adding the module without listing it in public/embed.go leaves
// every binary-only deployment with a panel that throws before it mounts.
func TestAdminActivityModuleServedFromEmbeddedPanel(t *testing.T) {
	withEmbeddedPanel(t, func(handler http.Handler) {
		rr := fetchAdminPath(t, handler, "/admin/admin-activity.js")
		body := rr.Body.String()
		if strings.TrimSpace(body) == "" {
			t.Fatal("/admin/admin-activity.js served an empty body")
		}
		if !strings.Contains(body, "EIOAdminActivity") {
			t.Fatalf("/admin/admin-activity.js does not expose the expected global: %.200s", body)
		}
		if got := rr.Header().Get("Content-Type"); !strings.Contains(got, "javascript") {
			t.Fatalf("/admin/admin-activity.js Content-Type = %q, want it to contain %q", got, "javascript")
		}
		if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Fatalf("/admin/admin-activity.js X-Content-Type-Options = %q, want nosniff", got)
		}
		// The script is same-origin, so the panel policy has to admit it without
		// naming any other origin or relaxing style/object/base rules.
		csp := rr.Header().Get("Content-Security-Policy")
		if csp != adminPanelCSP {
			t.Fatalf("/admin/admin-activity.js CSP = %q, want the admin panel policy", csp)
		}
		if !strings.Contains(csp, "script-src 'self'") {
			t.Fatalf("the admin CSP %q no longer allows same-origin scripts", csp)
		}
		if strings.Contains(csp, "http://") || strings.Contains(csp, "https://") {
			t.Fatalf("the admin CSP %q names a third-party origin for the module", csp)
		}
	})
}

// TestAdminActivityModuleFallsBackToEmbeddedCopyWhenDiskLacksIt reproduces the
// release-install layout: public/ holds admin.html and admin.js only. The module
// must come from the embedded FS, otherwise the installed panel loads markup that
// references a 404 script and stops at EIOAdminActivity.
func TestAdminActivityModuleFallsBackToEmbeddedCopyWhenDiskLacksIt(t *testing.T) {
	withDiskPanel(t, func(handler http.Handler) {
		page := fetchAdminPath(t, handler, "/admin/admin.html").Body.String()
		if !strings.Contains(page, "panel-from-disk") {
			t.Fatalf("admin.html did not come from the public/ directory on disk: %.200s", page)
		}
		rr := fetchAdminPath(t, handler, "/admin/admin-activity.js")
		body := rr.Body.String()
		if strings.Contains(body, "// panel script from disk") {
			t.Fatal("/admin/admin-activity.js was answered by the admin.js stand-in")
		}
		if !strings.Contains(body, "EIOAdminActivity") {
			t.Fatalf("the embedded module was not served when the disk copy is missing: %.200s", body)
		}
		if got := rr.Header().Get("Content-Type"); !strings.Contains(got, "javascript") {
			t.Fatalf("/admin/admin-activity.js Content-Type = %q, want it to contain %q", got, "javascript")
		}
	})
}

// TestAdminActivityModulePrefersDiskCopy keeps the developer override working for
// the new module the same way it does for admin.html/admin.js.
func TestAdminActivityModulePrefersDiskCopy(t *testing.T) {
	dir := panelWorkspace(t)
	// detectPublicDir() only treats public/ as the override root once it holds an
	// admin.html, so the disk-preference fixture needs that stand-in too (the
	// markup itself is irrelevant; this test is about which server answers).
	if err := os.WriteFile(filepath.Join(dir, "public", "admin.html"),
		[]byte("<!DOCTYPE html><html><body>panel-from-disk</body></html>"), 0o644); err != nil {
		t.Fatalf("write public/admin.html: %v", err)
	}
	const diskModule = "// panel activity module from disk\n"
	if err := os.WriteFile(filepath.Join(dir, "public", "admin-activity.js"), []byte(diskModule), 0o644); err != nil {
		t.Fatalf("write public/admin-activity.js: %v", err)
	}
	_, handler := newTestApp(t)

	body := fetchAdminPath(t, handler, "/admin/admin-activity.js").Body.String()
	if strings.TrimSpace(body) != strings.TrimSpace(diskModule) {
		t.Fatalf("/admin/admin-activity.js did not come from the public/ directory on disk: %.200s", body)
	}
}
