package backend

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// V1 golden baseline for the theme package pipeline.
//
// The fixture below is written out as literal JSON and hardcodes
// "themeApiVersion": 1 on purpose: it must not follow whichever "latest"
// version constant exists later, otherwise the v1 identity would silently
// drift to a v2 hash. The package ships no preview and no assets.
//
// Two facts are frozen here:
//   - ThemePackage.ContentHash (the canonical theme/package identity), and
//   - sha256 of the stylesheet renderThemeCSS emits for that content hash.
//
// The expected values are only ever filled in from a reviewed run of this
// test; nothing is regenerated or auto-updated from the system under test.
const (
	themePlayerV1GoldenContentHash = "a2feaa78c26ee687d2cbfb8efd173a5873d490b397eef5f017c73bb6d47f6529"
	themePlayerV1GoldenCSS256      = "1e9988462d42f635137f075a34f35ee8a63b1b5998c10415fb8d2ceb4941b511"
)

const themePlayerV1GoldenManifestJSON = `{
  "themeApiVersion": 1,
  "id": "eio-v1-golden",
  "name": "V1 Golden Baseline",
  "author": "",
  "description": "",
  "version": "1.0.0"
}`

const themePlayerV1GoldenThemeJSON = `{
  "variants": {
    "light": {
      "bg0": "#FFFFFF",
      "bg1": "#F5F5F5",
      "bg2": "#E4E9F0",
      "bg3": "#CFD6E2",
      "text1": "#111111",
      "text2": "#444444",
      "text3": "#5A5A5A",
      "line": "#C2CAD6",
      "line2": "#A9B3C2",
      "accent": "#0A4FB5",
      "accentText": "#0A4FB5"
    },
    "dark": {
      "bg0": "#0F1115",
      "bg1": "#171A20",
      "bg2": "#202733",
      "bg3": "#2C3542",
      "text1": "#F2F4F8",
      "text2": "#C3C9D4",
      "text3": "#9AA3B2",
      "line": "#333C4A",
      "line2": "#414B5B",
      "accent": "#6EA8FE",
      "accentText": "#6EA8FE"
    }
  },
  "appearance": {
    "radius": 14,
    "density": "comfortable"
  },
  "backgroundImage": ""
}`

func TestThemePlayerV1GoldenBaseline(t *testing.T) {
	if strings.Contains(themePlayerV1GoldenThemeJSON, "playerControls") {
		t.Fatal("v1 golden fixture must not carry the optional v2 player fields")
	}

	pkg, err := ParseThemePackage(themePlayerV1GoldenZIP(t, themePlayerV1GoldenManifestJSON, themePlayerV1GoldenThemeJSON))
	if err != nil {
		t.Fatalf("ParseThemePackage(v1 golden): %v", err)
	}
	if pkg.Manifest.ThemeAPIVersion != 1 {
		t.Fatalf("themeApiVersion = %d, want literal 1", pkg.Manifest.ThemeAPIVersion)
	}
	if pkg.Manifest.ID != "eio-v1-golden" || pkg.Manifest.Version != "1.0.0" {
		t.Fatalf("manifest identity = %#v", pkg.Manifest)
	}

	// Both variants must be validated and distinct, with the fixtures' literal
	// radius/density preserved by the parser.
	light, ok := pkg.Variant(ThemeVariantLight)
	if !ok {
		t.Fatal("light variant is missing")
	}
	dark, ok := pkg.Variant(ThemeVariantDark)
	if !ok {
		t.Fatal("dark variant is missing")
	}
	if light.Bg0 != "#FFFFFF" || dark.Bg0 != "#0F1115" || light.Bg0 == dark.Bg0 {
		t.Fatalf("variant tokens changed: light.bg0=%q dark.bg0=%q", light.Bg0, dark.Bg0)
	}
	if pkg.Theme.Appearance.Radius != 14 || pkg.Theme.Appearance.Density != "comfortable" {
		t.Fatalf("appearance = %+v, want radius 14 and density comfortable", pkg.Theme.Appearance)
	}

	// No optional media: JSON only, exactly two archive entries.
	if pkg.Preview != nil || len(pkg.Assets) != 0 || len(pkg.AllAssets()) != 0 {
		t.Fatalf("v1 golden unexpectedly has assets: preview=%v assets=%d", pkg.Preview, len(pkg.Assets))
	}
	if pkg.Theme.BackgroundImage != "" {
		t.Fatalf("backgroundImage = %q, want empty", pkg.Theme.BackgroundImage)
	}

	// v1 must keep rejecting the optional player block, so this baseline can
	// never be mistaken for a v2 package.
	withPlayers := strings.Replace(themePlayerV1GoldenThemeJSON, `"variants"`,
		`"playerControls": {"played": "#3B82F6", "buffered": "#F3F4F6", "track": "#111827", "thumb": "#3B82F6"}, "variants"`, 1)
	if strings.Count(withPlayers, "playerControls") != 1 {
		t.Fatal("failed to build the playerControls rejection fixture")
	}
	if _, err := ParseThemePackage(themePlayerV1GoldenZIP(t, themePlayerV1GoldenManifestJSON, withPlayers)); err == nil {
		t.Fatal("themeApiVersion 1 package with playerControls must be rejected")
	}

	css, err := renderThemeCSS(pkg.ContentHash, pkg.Theme)
	if err != nil {
		t.Fatalf("renderThemeCSS(v1 golden): %v", err)
	}
	sum := sha256.Sum256(css)
	css256 := hex.EncodeToString(sum[:])

	if pkg.ContentHash != themePlayerV1GoldenContentHash {
		t.Fatalf("v1 content hash changed: got %s want %s", pkg.ContentHash, themePlayerV1GoldenContentHash)
	}
	if css256 != themePlayerV1GoldenCSS256 {
		t.Fatalf("v1 generated CSS changed: got %s want %s", css256, themePlayerV1GoldenCSS256)
	}
}

// themePlayerV1GoldenZIP builds a flat in-memory archive. It never adds
// directory entries and never touches the filesystem.
func themePlayerV1GoldenZIP(t *testing.T, manifestJSON, themeJSON string) []byte {
	t.Helper()
	files := []struct {
		name string
		body string
	}{
		{themeEntryManifest, manifestJSON},
		{themeEntryTheme, themeJSON},
	}
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for _, file := range files {
		entry, err := writer.Create(file.name)
		if err != nil {
			t.Fatalf("create zip entry %q: %v", file.name, err)
		}
		if _, err := entry.Write([]byte(file.body)); err != nil {
			t.Fatalf("write zip entry %q: %v", file.name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	return buf.Bytes()
}
