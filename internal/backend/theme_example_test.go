package backend

import (
	"archive/zip"
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This test guards the shipped minimal example package under docs/theme-example.
// It only reads those two JSON files, builds a ZIP in memory (no temp files, no
// app instance, no upstreams) and runs the public package parser, so the
// documentation example cannot drift away from the format it documents.
func TestThemeExamplePackageFromDocs(t *testing.T) {
	root := filepath.Join("..", "..", "docs", "theme-example")
	files := make(map[string][]byte, 2)
	for _, name := range []string{themeEntryManifest, themeEntryTheme} {
		payload, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("read example %s: %v", name, err)
		}
		if len(payload) == 0 {
			t.Fatalf("example %s is empty", name)
		}
		files[name] = payload
	}

	ordered := themeExampleArchive(t, []string{themeEntryManifest, themeEntryTheme}, files)
	pkg, err := ParseThemePackage(ordered)
	if err != nil {
		t.Fatalf("ParseThemePackage(example): %v", err)
	}

	if pkg.Manifest.ThemeAPIVersion != ThemeAPIVersion {
		t.Fatalf("themeApiVersion = %d, want %d", pkg.Manifest.ThemeAPIVersion, ThemeAPIVersion)
	}
	if pkg.Manifest.ID != "eio-example" {
		t.Fatalf("example id = %q, want %q", pkg.Manifest.ID, "eio-example")
	}
	if pkg.Manifest.Version == "" {
		t.Fatal("example manifest has no version")
	}

	if pkg.Theme.Appearance.Radius != ThemeAppearanceRadiusDefault ||
		pkg.Theme.Appearance.Density != ThemeDensityDefault {
		t.Fatalf("example appearance = %+v, want radius %d and density %q",
			pkg.Theme.Appearance, ThemeAppearanceRadiusDefault, ThemeDensityDefault)
	}

	for _, name := range []string{ThemeVariantLight, ThemeVariantDark} {
		variant, ok := pkg.Variant(name)
		if !ok {
			t.Fatalf("example variant %q is missing", name)
		}
		for field, value := range map[string]string{
			"bg0":        variant.Bg0,
			"bg1":        variant.Bg1,
			"bg2":        variant.Bg2,
			"bg3":        variant.Bg3,
			"text1":      variant.Text1,
			"text2":      variant.Text2,
			"text3":      variant.Text3,
			"line":       variant.Line,
			"line2":      variant.Line2,
			"accent":     variant.Accent,
			"accentText": variant.AccentText,
		} {
			if len(value) != 7 || value[0] != '#' || strings.ToUpper(value) != value {
				t.Fatalf("example variant %q token %q = %q, want uppercase literal #RRGGBB",
					name, field, value)
			}
		}
	}
	light, _ := pkg.Variant(ThemeVariantLight)
	dark, _ := pkg.Variant(ThemeVariantDark)
	if light.Bg0 != "#FFFFFF" || light.AccentText != "#0A4FB5" {
		t.Fatalf("example light tokens changed: bg0=%q accentText=%q", light.Bg0, light.AccentText)
	}
	if dark.Bg0 != "#0F1115" || dark.AccentText != "#6EA8FE" {
		t.Fatalf("example dark tokens changed: bg0=%q accentText=%q", dark.Bg0, dark.AccentText)
	}

	// The documented example ships JSON only: no preview and no assets.
	if pkg.Preview != nil {
		t.Fatalf("example unexpectedly has a preview image: %+v", *pkg.Preview)
	}
	if len(pkg.Assets) != 0 {
		t.Fatalf("example unexpectedly has %d asset images", len(pkg.Assets))
	}
	if pkg.Theme.BackgroundImage != "" {
		t.Fatalf("example backgroundImage = %q, want empty", pkg.Theme.BackgroundImage)
	}
	if len(pkg.AllAssets()) != 0 {
		t.Fatalf("example AllAssets() returned %d entries, want 0", len(pkg.AllAssets()))
	}

	// ContentHash must be a full sha256 hex digest and must not depend on archive
	// member order, because it is the immutable resource identity.
	if len(pkg.ContentHash) != 64 {
		t.Fatalf("content hash %q is not a sha256 hex digest", pkg.ContentHash)
	}
	if _, err := hex.DecodeString(pkg.ContentHash); err != nil {
		t.Fatalf("content hash %q is not hex: %v", pkg.ContentHash, err)
	}
	if strings.Trim(pkg.ContentHash, "0") == "" {
		t.Fatal("content hash is all zeroes")
	}
	if pkg.PackageBytes != len(ordered) {
		t.Fatalf("PackageBytes = %d, want %d", pkg.PackageBytes, len(ordered))
	}

	reversed := themeExampleArchive(t, []string{themeEntryTheme, themeEntryManifest}, files)
	again, err := ParseThemePackage(reversed)
	if err != nil {
		t.Fatalf("ParseThemePackage(example, reversed order): %v", err)
	}
	if again.ContentHash != pkg.ContentHash {
		t.Fatalf("content hash depends on archive order: %q != %q",
			again.ContentHash, pkg.ContentHash)
	}
}

// themeExampleArchive writes the given entries into an in-memory ZIP. It never
// adds directory entries, matching the documented flat-root layout.
func themeExampleArchive(t *testing.T, order []string, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for _, name := range order {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatalf("create zip entry %q: %v", name, err)
		}
		if _, err := entry.Write(files[name]); err != nil {
			t.Fatalf("write zip entry %q: %v", name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	return buf.Bytes()
}
