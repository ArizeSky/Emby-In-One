package backend

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// Tests for the fixed player progress sample inside the isolated preview
// document. The sample is literal markup plus a checked-in same-origin
// stylesheet: no script, no inline style, no theme string, and no CSP change.
// The v1 immutable stylesheet stays out of it entirely (its own golden test in
// theme_player_v1_golden_test.go still pins the bytes).

// themePreviewPlayerCSSTestPath is the checked-in preview stylesheet. Assertions
// read this source instead of restating its values, so the file and the test
// cannot drift apart.
const themePreviewPlayerCSSTestPath = "../../public/web/theme-player-preview.css"

// themePreviewPlayerCSSTestSource reads the preview stylesheet from the worktree.
func themePreviewPlayerCSSTestSource(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(themePreviewPlayerCSSTestPath)
	if err != nil {
		t.Fatalf("read %s: %v", themePreviewPlayerCSSTestPath, err)
	}
	if len(data) == 0 {
		t.Fatalf("%s is empty", themePreviewPlayerCSSTestPath)
	}
	return string(data)
}

// themePreviewPlayerCSSTestStore installs the v1 golden theme rewritten to
// themeApiVersion 2 with the given playerControls block.
func themePreviewPlayerCSSTestStore(t *testing.T, block string) (*ThemeStore, ThemeCatalogEntry) {
	t.Helper()
	themeJSON := themePlayerV1GoldenThemeJSON
	if block != "" {
		themeJSON = themePlayerV2TestThemeWithBlock(t, themeJSON, block)
	}
	pkg, err := ParseThemePackage(themePlayerV1GoldenZIP(t, themePlayerV2TestManifest(t, ThemeAPIVersionLatest), themeJSON))
	if err != nil {
		t.Fatalf("ParseThemePackage(v2 preview theme): %v", err)
	}
	store := newThemeStoreTestStore(t, t.TempDir(), newThemeStoreTestDB(t))
	entry, err := store.InstallUpdate(pkg, 0)
	if err != nil {
		t.Fatalf("install v2 preview theme: %v", err)
	}
	if entry.CurrentHash == "" {
		t.Fatal("installed preview theme has no content hash")
	}
	return store, entry
}

// themePreviewPlayerCSSTestDocument renders the preview document for a hash.
func themePreviewPlayerCSSTestDocument(t *testing.T, store *ThemeStore, hash, mode string) *httptest.ResponseRecorder {
	t.Helper()
	app := &App{ThemeStore: store}
	req := httptest.NewRequest(http.MethodGet, "/web/themes/"+hash+"/preview.html?mode="+mode, nil)
	req.SetPathValue("hash", hash)
	rr := httptest.NewRecorder()
	app.handleThemePreviewDocument(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("preview %s status = %d, want 200 (body=%s)", mode, rr.Code, rr.Body.String())
	}
	return rr
}

// TestThemePreviewPlayerSampleIsStaticMarkup pins the fixed markup, the fixed
// same-origin stylesheet link and the unchanged preview CSP.
func TestThemePreviewPlayerSampleIsStaticMarkup(t *testing.T) {
	store, entry := themePreviewPlayerCSSTestStore(t, themePlayerV2TestLowerBlock)
	rr := themePreviewPlayerCSSTestDocument(t, store, entry.CurrentHash, ThemeModeDark)
	body := rr.Body.String()

	// CSP and framing stay exactly the frozen preview policy: the new sample
	// adds no script, no inline style and no extra source.
	if got := rr.Header().Get("Content-Security-Policy"); got != themePreviewCSP {
		t.Fatalf("CSP = %q, want the frozen preview policy %q", got, themePreviewCSP)
	}
	if got := rr.Header().Get("X-Frame-Options"); got != "SAMEORIGIN" {
		t.Fatalf("X-Frame-Options = %q, want SAMEORIGIN", got)
	}

	wantMarkup := []string{
		`<link rel="stylesheet" href="` + themePreviewPlayerCSSURL + `">`,
		`class="eio-preview-player"`,
		`aria-label=`,
		`eio-preview-player-rail`,
		`eio-preview-player-buffered`,
		`eio-preview-player-played`,
		`eio-preview-player-thumb`,
	}
	for _, needle := range wantMarkup {
		if !strings.Contains(body, needle) {
			t.Fatalf("preview document lacks %q: %q", needle, body)
		}
	}
	// The static figures live in the accessible label, not in the stylesheet
	// runtime, because the sample never animates or reads media state.
	for _, needle := range []string{"30%", "65%", "100%"} {
		if !strings.Contains(body, needle) {
			t.Fatalf("preview player label lacks the static figure %q", needle)
		}
	}
	if got := strings.Count(body, `class="eio-preview-player"`); got != 1 {
		t.Fatalf("preview player sample appears %d times, want exactly 1", got)
	}

	// No script, no inline style, no inline event handler, no javascript: sink.
	lower := strings.ToLower(body)
	for _, forbidden := range []string{"<script", "javascript:", " style=", "onload=", "onerror=", "onclick="} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("preview document must not contain %q", forbidden)
		}
	}
}

// TestThemePreviewPlayerCSSCarriesFrozenVariablesAndDefaults checks the
// stylesheet itself: the four frozen variable names with the contract defaults,
// the fixed 30%/65% sample widths and the fixed black/white thumb outline.
func TestThemePreviewPlayerCSSCarriesFrozenVariablesAndDefaults(t *testing.T) {
	css := themePreviewPlayerCSSTestSource(t)

	declarations := []string{
		"--eio-progress-played: " + ThemePlayerDefaultPlayed + ";",
		"--eio-progress-buffered: " + ThemePlayerDefaultBuffered + ";",
		"--eio-progress-track: " + ThemePlayerDefaultTrack + ";",
		"--eio-progress-thumb: " + ThemePlayerDefaultThumb + ";",
	}
	for _, declaration := range declarations {
		if !strings.Contains(css, declaration) {
			t.Fatalf("preview stylesheet lacks default declaration %q", declaration)
		}
	}
	// Fallbacks stay attached to the consuming declarations too, so a broken or
	// missing theme.css still renders a visible sample.
	for _, variable := range []string{"played", "buffered", "track", "thumb"} {
		if !strings.Contains(css, "var(--eio-progress-"+variable+", ") {
			t.Fatalf("preview stylesheet does not consume --eio-progress-%s with a fallback", variable)
		}
	}

	// Static sample geometry: full track, 65% buffered, 30% played.
	if !strings.Contains(css, "width: 65%;") {
		t.Fatal("preview stylesheet lacks the static 65% buffered width")
	}
	if !strings.Contains(css, "width: 30%;") {
		t.Fatal("preview stylesheet lacks the static 30% played width")
	}
	if !strings.Contains(css, "left: 30%;") {
		t.Fatal("preview stylesheet does not place the thumb at the 30% played position")
	}

	// The thumb outline is fixed: a white ring plus an outer black ring, so a
	// theme can recolor the thumb but never erase its boundary.
	if !strings.Contains(css, "border: 2px solid #ffffff;") {
		t.Fatal("preview stylesheet lacks the fixed white thumb ring")
	}
	if !strings.Contains(css, "box-shadow: 0 0 0 1px #000000;") {
		t.Fatal("preview stylesheet lacks the fixed outer black thumb ring")
	}

	// The stylesheet is inert: it loads nothing, runs nothing and reaches no
	// theme page variable, so subtitle text and the page background stay the
	// theme's business (or, for the control bar, fixed).
	for _, forbidden := range []string{"url(", "@import", "<", "javascript:", "--bg-0", "--t1", "var(--bg", "var(--ac"} {
		if strings.Contains(css, forbidden) {
			t.Fatalf("preview stylesheet must not contain %q", forbidden)
		}
	}
	if !strings.Contains(css, "background: #1f2937;") {
		t.Fatal("preview stylesheet lacks the fixed dark control surface")
	}
}

// TestThemePreviewPlayerCSSLinkIsServedByWebStaticHandler proves the link target
// resolves through the existing /web static handler with no route change: the
// handler is rooted at public/, so public/web/<name> is served as /web/web/<name>.
func TestThemePreviewPlayerCSSLinkIsServedByWebStaticHandler(t *testing.T) {
	css := themePreviewPlayerCSSTestSource(t)
	app := &App{}
	req := httptest.NewRequest(http.MethodGet, themePreviewPlayerCSSURL, nil)
	rr := httptest.NewRecorder()
	app.webFileServer().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200", themePreviewPlayerCSSURL, rr.Code)
	}
	if got := rr.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/css") {
		t.Fatalf("GET %s Content-Type = %q, want text/css", themePreviewPlayerCSSURL, got)
	}
	if body := rr.Body.String(); body != css {
		t.Fatalf("served stylesheet differs from %s", themePreviewPlayerCSSTestPath)
	}
}

// TestThemePreviewPlayerSampleCarriesNoThemeStrings keeps the sample free of
// package-controlled text: neither the v1 golden colors nor a v2 palette may be
// inlined into the generated document.
func TestThemePreviewPlayerSampleCarriesNoThemeStrings(t *testing.T) {
	cases := []struct {
		name  string
		block string
	}{
		{"v2 without playerControls", ""},
		{"v2 with blue playerControls", themePlayerV2TestBlueBlock},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, entry := themePreviewPlayerCSSTestStore(t, tc.block)
			body := themePreviewPlayerCSSTestDocument(t, store, entry.CurrentHash, ThemeModeLight).Body.String()
			for _, forbidden := range []string{
				"#2563eb", "#3b82f6", "#f3f4f6", "#111827",
				"playerControls", "eio-v1-golden", "<style", "style=",
			} {
				if strings.Contains(body, forbidden) {
					t.Fatalf("preview document leaked %q from the theme package", forbidden)
				}
			}
		})
	}
}

// TestThemePreviewPlayerSampleLeavesV1CSSToItsGolden keeps the frozen v1
// stylesheet free of the preview player domain: the sample's rules live in the
// checked-in stylesheet, never in generated v1 CSS.
func TestThemePreviewPlayerSampleLeavesV1CSSToItsGolden(t *testing.T) {
	pkg := themePlayerV2TestMustParse(t, themePlayerV1GoldenZIP(t, themePlayerV1GoldenManifestJSON, themePlayerV1GoldenThemeJSON))
	css := string(themePlayerV2TestCSS(t, pkg.ContentHash, pkg.Theme))
	for _, forbidden := range []string{"eio-preview-player", themePlayerV2TestProgressPrefix, themePreviewPlayerCSSURL} {
		if strings.Contains(css, forbidden) {
			t.Fatalf("v1 generated CSS gained %q", forbidden)
		}
	}
	// The existing preview layout rules must still be present and unchanged in
	// kind: the v1 block still styles the fixed preview shell.
	if !strings.Contains(css, "body.eio-preview .eio-preview-panel") {
		t.Fatal("v1 generated CSS lost the fixed preview layout rules")
	}
}
