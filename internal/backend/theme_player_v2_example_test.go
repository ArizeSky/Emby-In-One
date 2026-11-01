package backend

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestThemePlayerV2DocumentedExample guards the user-facing example shipped in
// docs/theme-example-v2: it must parse as a valid Theme API v2 package and
// render the four progress CSS variables with the documented defaults.
func TestThemePlayerV2DocumentedExample(t *testing.T) {
	manifest, err := os.ReadFile(filepath.Join("..", "..", "docs", "theme-example-v2", "manifest.json"))
	if err != nil {
		t.Fatalf("read example manifest: %v", err)
	}
	theme, err := os.ReadFile(filepath.Join("..", "..", "docs", "theme-example-v2", "theme.json"))
	if err != nil {
		t.Fatalf("read example theme: %v", err)
	}

	archive := themePlayerV1GoldenZIP(t, string(manifest), string(theme))
	pkg, err := ParseThemePackage(archive)
	if err != nil {
		t.Fatalf("ParseThemePackage(documented example): %v", err)
	}

	if pkg.Manifest.ThemeAPIVersion != 2 {
		t.Fatalf("Manifest.ThemeAPIVersion = %v, want 2", pkg.Manifest.ThemeAPIVersion)
	}
	if pkg.Manifest.ID != "eio-player-theme-example" {
		t.Fatalf("Manifest.ID = %q, want %q", pkg.Manifest.ID, "eio-player-theme-example")
	}

	themePlayerV2TestAssertControls(t, pkg.Theme.PlayerControls, themePlayerV2TestLowerColors)

	if len(pkg.ContentHash) == 0 {
		t.Fatal("ContentHash is empty for the documented example")
	}

	css, err := renderThemeCSS(pkg.ContentHash, pkg.Theme)
	if err != nil {
		t.Fatalf("renderThemeCSS(documented example): %v", err)
	}
	if got := strings.Count(string(css), "--eio-progress-"); got < 4 {
		t.Fatalf("rendered CSS declares %d --eio-progress- variables, want 4:\n%s", got, css)
	}
}
