package backend

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The T3 theme management page lives in public/admin-theme.js, loaded by
// admin.html before admin.js. These tests cover the packaging and policy
// contract only (the JS state machine is covered by tests/admin-theme.test.cjs
// and the admin.js wiring by tests/admin-panel.test.cjs):
//
//   - the module is embedded, served from /admin/ with a JavaScript MIME type
//     under the existing self-only CSP, and loaded before app script;
//   - an installed layout that only has admin.html/admin.js on disk still gets
//     the module through the per-file embedded fallback;
//   - a disk copy still wins, like every other panel asset;
//   - the single preview iframe stays sandboxed without scripts, and the admin
//     page never links a theme stylesheet into its own DOM.
//
// The admin page must not apply theme CSS to itself: previews are a separate
// generated document, embedded only through the sandboxed iframe.

func TestAdminThemePanelLoadsModuleBeforeAppScript(t *testing.T) {
	withEmbeddedPanel(t, func(handler http.Handler) {
		page := fetchAdminPath(t, handler, "/admin/admin.html").Body.String()

		var sources []string
		for _, match := range adminPanelScriptSrc.FindAllStringSubmatch(page, -1) {
			sources = append(sources, match[1])
		}
		themeAt, appAt := -1, -1
		for i, src := range sources {
			switch src {
			case "admin-theme.js":
				themeAt = i
			case "admin.js":
				appAt = i
			}
		}
		if themeAt < 0 {
			t.Fatalf("admin.html does not load admin-theme.js (it loads %v)", sources)
		}
		if appAt < 0 {
			t.Fatalf("admin.html does not load admin.js (it loads %v)", sources)
		}
		if themeAt > appAt {
			t.Fatalf("admin-theme.js is loaded after admin.js (%v); EIOAdminTheme must be defined first", sources)
		}
		for _, src := range sources {
			if strings.Contains(src, "//") || strings.Contains(src, ":") {
				t.Fatalf("admin.html loads %q from another origin; every asset must be self-hosted", src)
			}
		}
	})
}

// TestAdminThemeModuleServedFromEmbeddedPanel is the regression guard for the
// embed directive: adding the module without listing it in public/embed.go
// leaves every binary-only deployment with a panel that cannot manage themes.
func TestAdminThemeModuleServedFromEmbeddedPanel(t *testing.T) {
	withEmbeddedPanel(t, func(handler http.Handler) {
		rr := fetchAdminPath(t, handler, "/admin/admin-theme.js")
		body := rr.Body.String()
		if strings.TrimSpace(body) == "" {
			t.Fatal("/admin/admin-theme.js served an empty body")
		}
		if !strings.Contains(body, "EIOAdminTheme") {
			t.Fatalf("/admin/admin-theme.js does not expose the expected global: %.200s", body)
		}
		if !strings.Contains(body, "application/zip") {
			t.Fatalf("/admin/admin-theme.js does not upload raw ZIP bodies: %.200s", body)
		}
		if got := rr.Header().Get("Content-Type"); !strings.Contains(got, "javascript") {
			t.Fatalf("/admin/admin-theme.js Content-Type = %q, want it to contain %q", got, "javascript")
		}
		if got := rr.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Fatalf("/admin/admin-theme.js X-Content-Type-Options = %q, want nosniff", got)
		}
		csp := rr.Header().Get("Content-Security-Policy")
		if csp != adminPanelCSP {
			t.Fatalf("/admin/admin-theme.js CSP = %q, want the admin panel policy", csp)
		}
		if !strings.Contains(csp, "script-src 'self'") {
			t.Fatalf("the admin CSP %q no longer allows same-origin scripts", csp)
		}
		if strings.Contains(csp, "http://") || strings.Contains(csp, "https://") {
			t.Fatalf("the admin CSP %q names a third-party origin for the module", csp)
		}
	})
}

// TestAdminThemeModuleFallsBackToEmbeddedCopyWhenDiskLacksIt reproduces the
// release-install layout: public/ holds admin.html and admin.js only.
func TestAdminThemeModuleFallsBackToEmbeddedCopyWhenDiskLacksIt(t *testing.T) {
	withDiskPanel(t, func(handler http.Handler) {
		page := fetchAdminPath(t, handler, "/admin/admin.html").Body.String()
		if !strings.Contains(page, "panel-from-disk") {
			t.Fatalf("admin.html did not come from the public/ directory on disk: %.200s", page)
		}
		rr := fetchAdminPath(t, handler, "/admin/admin-theme.js")
		body := rr.Body.String()
		if !strings.Contains(body, "EIOAdminTheme") {
			t.Fatalf("the embedded theme module was not served when the disk copy is missing: %.200s", body)
		}
		if got := rr.Header().Get("Content-Type"); !strings.Contains(got, "javascript") {
			t.Fatalf("/admin/admin-theme.js Content-Type = %q, want it to contain %q", got, "javascript")
		}
	})
}

// TestAdminThemeModulePrefersDiskCopy keeps the developer override working for
// the new module the same way it does for admin.html/admin.js.
func TestAdminThemeModulePrefersDiskCopy(t *testing.T) {
	dir := panelWorkspace(t)
	if err := os.WriteFile(filepath.Join(dir, "public", "admin.html"),
		[]byte("<!DOCTYPE html><html><body>panel-from-disk</body></html>"), 0o644); err != nil {
		t.Fatalf("write public/admin.html: %v", err)
	}
	const diskModule = "// panel theme module from disk\n"
	if err := os.WriteFile(filepath.Join(dir, "public", "admin-theme.js"), []byte(diskModule), 0o644); err != nil {
		t.Fatalf("write public/admin-theme.js: %v", err)
	}
	_, handler := newTestApp(t)

	body := fetchAdminPath(t, handler, "/admin/admin-theme.js").Body.String()
	if strings.TrimSpace(body) != strings.TrimSpace(diskModule) {
		t.Fatalf("/admin/admin-theme.js did not come from the public/ directory on disk: %.200s", body)
	}
}

// TestAdminThemePanelKeepsPreviewSandboxedAndItselfUnthemed pins the two rules
// that keep theme content out of the admin document:
//
//   - the only preview surface is a same-origin iframe sandboxed with
//     allow-same-origin and *without* allow-scripts, so package markup can never
//     run in the panel's origin;
//   - the panel itself never links an installed theme stylesheet, so an
//     uploaded CSS cannot restyle the admin UI.
func TestAdminThemePanelKeepsPreviewSandboxedAndItselfUnthemed(t *testing.T) {
	withEmbeddedPanel(t, func(handler http.Handler) {
		page := fetchAdminPath(t, handler, "/admin/admin.html").Body.String()

		if !strings.Contains(page, `sandbox="allow-same-origin"`) {
			t.Fatal("admin.html preview iframe is not sandboxed with allow-same-origin")
		}
		if strings.Contains(page, "allow-scripts") {
			t.Fatal("admin.html grants allow-scripts to the preview iframe; generated preview documents carry no scripts")
		}
		if match := regexp.MustCompile(`(?i)<link\b[^>]*\bhref\s*=\s*"[^"]*\/web\/themes\/`).FindString(page); match != "" {
			t.Fatalf("admin.html links an installed theme stylesheet into the panel: %s", match)
		}
		if !strings.Contains(page, "1.5 MiB") {
			t.Fatal("admin.html does not state the 1.5 MiB package limit")
		}
		if !strings.Contains(page, "公开资源") {
			t.Fatal("admin.html does not warn that installed theme resources are public")
		}
	})
}
