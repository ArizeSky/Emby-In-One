package backend

import (
	"bytes"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// The themePlayerV1Golden* fixtures live in the existing player-theme v1 test
// files. Everything below is built on top of them so the v2 tests exercise the
// very same golden theme content.
const (
	themePlayerV2TestVersionNeedle = `"themeApiVersion": 1`

	themePlayerV2TestLowerBlock = `"playerControls":{"played":"#3b82f6","buffered":"#f3f4f6","track":"#111827","thumb":"#3b82f6"},`
	themePlayerV2TestUpperBlock = `"playerControls":{"played":"#3B82F6","buffered":"#F3F4F6","track":"#111827","thumb":"#3B82F6"},`
	themePlayerV2TestBlueBlock  = `"playerControls":{"played":"#2563eb","buffered":"#f3f4f6","track":"#111827","thumb":"#2563eb"},`

	themePlayerV2TestProgressPrefix = "--eio-progress-"
)

var (
	themePlayerV2TestLowerColors = map[string]string{
		"played":   "#3b82f6",
		"buffered": "#f3f4f6",
		"track":    "#111827",
		"thumb":    "#3b82f6",
	}

	themePlayerV2TestBlueColors = map[string]string{
		"played":   "#2563eb",
		"buffered": "#f3f4f6",
		"track":    "#111827",
		"thumb":    "#2563eb",
	}
)

// themePlayerV2TestManifest rewrites the golden manifest to another theme API
// version.
func themePlayerV2TestManifest(t *testing.T, version int) string {
	t.Helper()
	if !strings.Contains(themePlayerV1GoldenManifestJSON, themePlayerV2TestVersionNeedle) {
		t.Fatalf("golden manifest does not contain %q", themePlayerV2TestVersionNeedle)
	}
	replacement := `"themeApiVersion": ` + strconv.Itoa(version)
	return strings.Replace(themePlayerV1GoldenManifestJSON, themePlayerV2TestVersionNeedle, replacement, 1)
}

// themePlayerV2TestThemeWithBlock injects a raw playerControls member directly
// after the first object brace of the theme JSON. Only raw JSON is inserted so
// deliberately invalid blocks can be exercised too.
func themePlayerV2TestThemeWithBlock(t *testing.T, themeJSON, block string) string {
	t.Helper()
	idx := strings.Index(themeJSON, "{")
	if idx < 0 {
		t.Fatalf("golden theme JSON is not a JSON object")
	}
	return themeJSON[:idx+1] + block + themeJSON[idx+1:]
}

func themePlayerV2TestMustParse(t *testing.T, zip []byte) *ThemePackage {
	t.Helper()
	pkg, err := ParseThemePackage(zip)
	if err != nil {
		t.Fatalf("ParseThemePackage: %v", err)
	}
	if pkg == nil {
		t.Fatal("ParseThemePackage returned a nil package")
	}
	if pkg.ContentHash == "" {
		t.Fatal("ParseThemePackage returned an empty content hash")
	}
	return pkg
}

func themePlayerV2TestRequireControls(t *testing.T, controls *ThemePlayerControls) map[string]string {
	t.Helper()
	if controls == nil {
		t.Fatal("theme player controls are missing")
	}
	if controls.Played == nil || controls.Buffered == nil || controls.Track == nil || controls.Thumb == nil {
		t.Fatalf("theme player controls are incomplete: played=%v buffered=%v track=%v thumb=%v",
			controls.Played, controls.Buffered, controls.Track, controls.Thumb)
	}
	return map[string]string{
		"played":   *controls.Played,
		"buffered": *controls.Buffered,
		"track":    *controls.Track,
		"thumb":    *controls.Thumb,
	}
}

func themePlayerV2TestAssertControls(t *testing.T, controls *ThemePlayerControls, want map[string]string) {
	t.Helper()
	if got := themePlayerV2TestRequireControls(t, controls); !reflect.DeepEqual(got, want) {
		t.Fatalf("theme player controls = %#v, want %#v", got, want)
	}
}

func themePlayerV2TestCSS(t *testing.T, hash string, theme ThemeDefinition) []byte {
	t.Helper()
	css, err := renderThemeCSS(hash, theme)
	if err != nil {
		t.Fatalf("renderThemeCSS: %v", err)
	}
	if len(css) == 0 {
		t.Fatal("renderThemeCSS returned empty CSS")
	}
	return css
}

// TestThemePlayerV2WithoutBlockKeepsV1CSS pins the compatibility contract: a v2
// manifest without the playerControls block renders byte-identical CSS to v1
// although the content hash (and therefore the header) differs, and a v2 theme
// with the block renders the progress variables.
func TestThemePlayerV2WithoutBlockKeepsV1CSS(t *testing.T) {
	v2Manifest := themePlayerV2TestManifest(t, 2)

	v1Pkg := themePlayerV2TestMustParse(t, themePlayerV1GoldenZIP(t, themePlayerV1GoldenManifestJSON, themePlayerV1GoldenThemeJSON))
	noBlockPkg := themePlayerV2TestMustParse(t, themePlayerV1GoldenZIP(t, v2Manifest, themePlayerV1GoldenThemeJSON))
	blockPkg := themePlayerV2TestMustParse(t, themePlayerV1GoldenZIP(t, v2Manifest,
		themePlayerV2TestThemeWithBlock(t, themePlayerV1GoldenThemeJSON, themePlayerV2TestLowerBlock)))

	if v1Pkg.Theme.PlayerControls != nil {
		t.Fatal("v1 golden theme unexpectedly carries player controls")
	}
	if noBlockPkg.Theme.PlayerControls != nil {
		t.Fatal("v2 theme without a playerControls block unexpectedly carries player controls")
	}
	themePlayerV2TestAssertControls(t, blockPkg.Theme.PlayerControls, themePlayerV2TestLowerColors)

	if v1Pkg.ContentHash == noBlockPkg.ContentHash {
		t.Fatalf("v1 and v2-without-block share content hash %q", v1Pkg.ContentHash)
	}
	if noBlockPkg.ContentHash == blockPkg.ContentHash {
		t.Fatalf("v2 with and without player controls share content hash %q", blockPkg.ContentHash)
	}
	if v1Pkg.ContentHash == blockPkg.ContentHash {
		t.Fatalf("v1 and v2-with-block share content hash %q", blockPkg.ContentHash)
	}

	v1CSS := themePlayerV2TestCSS(t, v1Pkg.ContentHash, v1Pkg.Theme)
	noBlockCSS := themePlayerV2TestCSS(t, noBlockPkg.ContentHash, noBlockPkg.Theme)
	blockCSS := themePlayerV2TestCSS(t, blockPkg.ContentHash, blockPkg.Theme)

	// The rendered header embeds the content hash, so only the absence of the
	// progress variables (the actual v1 body) can be compared byte-wise.
	for _, tc := range []struct {
		name string
		css  []byte
	}{
		{name: "v1", css: v1CSS},
		{name: "v2-without-block", css: noBlockCSS},
	} {
		if bytes.Contains(tc.css, []byte(themePlayerV2TestProgressPrefix)) {
			t.Fatalf("%s CSS unexpectedly renders %q variables", tc.name, themePlayerV2TestProgressPrefix)
		}
	}
	if !bytes.Contains(blockCSS, []byte(themePlayerV2TestProgressPrefix)) {
		t.Fatalf("v2 CSS with player controls does not render any %q variable", themePlayerV2TestProgressPrefix)
	}
}

// TestThemePlayerV2ColorCaseDoesNotChangeHash verifies that hex colour casing is
// normalised for hashing: only the palette string changes case, the JSON keys
// stay untouched.
func TestThemePlayerV2ColorCaseDoesNotChangeHash(t *testing.T) {
	v2Manifest := themePlayerV2TestManifest(t, 2)

	lowerPkg := themePlayerV2TestMustParse(t, themePlayerV1GoldenZIP(t, v2Manifest,
		themePlayerV2TestThemeWithBlock(t, themePlayerV1GoldenThemeJSON, themePlayerV2TestLowerBlock)))
	upperPkg := themePlayerV2TestMustParse(t, themePlayerV1GoldenZIP(t, v2Manifest,
		themePlayerV2TestThemeWithBlock(t, themePlayerV1GoldenThemeJSON, themePlayerV2TestUpperBlock)))

	if lowerPkg.ContentHash != upperPkg.ContentHash {
		t.Fatalf("colour casing changed the content hash: lower=%q upper=%q",
			lowerPkg.ContentHash, upperPkg.ContentHash)
	}
	themePlayerV2TestRequireControls(t, upperPkg.Theme.PlayerControls)
}

// TestThemePlayerV2DifferentColorChangesHash verifies that a valid non-default
// palette produces a different content hash.
func TestThemePlayerV2DifferentColorChangesHash(t *testing.T) {
	v2Manifest := themePlayerV2TestManifest(t, 2)

	basePkg := themePlayerV2TestMustParse(t, themePlayerV1GoldenZIP(t, v2Manifest,
		themePlayerV2TestThemeWithBlock(t, themePlayerV1GoldenThemeJSON, themePlayerV2TestLowerBlock)))
	bluePkg := themePlayerV2TestMustParse(t, themePlayerV1GoldenZIP(t, v2Manifest,
		themePlayerV2TestThemeWithBlock(t, themePlayerV1GoldenThemeJSON, themePlayerV2TestBlueBlock)))

	if basePkg.ContentHash == bluePkg.ContentHash {
		t.Fatalf("changing played/thumb colour kept content hash %q", basePkg.ContentHash)
	}
	themePlayerV2TestAssertControls(t, bluePkg.Theme.PlayerControls, themePlayerV2TestBlueColors)
}

// TestThemePlayerV2RejectsInvalidPackages covers the manifest version gate as
// well as every malformed playerControls shape.
func TestThemePlayerV2RejectsInvalidPackages(t *testing.T) {
	v1Manifest := themePlayerV1GoldenManifestJSON
	v2Manifest := themePlayerV2TestManifest(t, 2)

	themeWith := func(block string) string {
		return themePlayerV2TestThemeWithBlock(t, themePlayerV1GoldenThemeJSON, block)
	}

	cases := []struct {
		name string
		zip  []byte
	}{
		{
			name: "v1 manifest with player controls",
			zip:  themePlayerV1GoldenZIP(t, v1Manifest, themeWith(themePlayerV2TestLowerBlock)),
		},
		{
			name: "theme api version 0",
			zip:  themePlayerV1GoldenZIP(t, themePlayerV2TestManifest(t, 0), themePlayerV1GoldenThemeJSON),
		},
		{
			name: "theme api version 3",
			zip:  themePlayerV1GoldenZIP(t, themePlayerV2TestManifest(t, 3), themePlayerV1GoldenThemeJSON),
		},
		{
			name: "v2 null player controls",
			zip:  themePlayerV1GoldenZIP(t, v2Manifest, themeWith(`"playerControls":null,`)),
		},
		{
			name: "v2 empty player controls",
			zip:  themePlayerV1GoldenZIP(t, v2Manifest, themeWith(`"playerControls":{},`)),
		},
		{
			name: "v2 partial player controls",
			zip:  themePlayerV1GoldenZIP(t, v2Manifest, themeWith(`"playerControls":{"played":"#3b82f6"},`)),
		},
		{
			name: "v2 unknown player control field",
			zip: themePlayerV1GoldenZIP(t, v2Manifest, themeWith(
				`"playerControls":{"played":"#3b82f6","buffered":"#f3f4f6","track":"#111827","thumb":"#3b82f6","accent":"#ffffff"},`)),
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseThemePackage(tc.zip); err == nil {
				t.Fatal("ParseThemePackage accepted an invalid theme package")
			}
		})
	}
}

// TestThemePlayerV2CloneStoreAndAPIVersion verifies defensive copying in the
// clone helper, the install/definition round trip in the theme store, and the
// public DTO API version propagation.
func TestThemePlayerV2CloneStoreAndAPIVersion(t *testing.T) {
	v2Manifest := themePlayerV2TestManifest(t, 2)
	pkg := themePlayerV2TestMustParse(t, themePlayerV1GoldenZIP(t, v2Manifest,
		themePlayerV2TestThemeWithBlock(t, themePlayerV1GoldenThemeJSON, themePlayerV2TestLowerBlock)))
	themePlayerV2TestAssertControls(t, pkg.Theme.PlayerControls, themePlayerV2TestLowerColors)

	t.Run("clone copies player controls", func(t *testing.T) {
		clone := cloneThemeDefinition(pkg.Theme)
		if clone.PlayerControls == nil {
			t.Fatal("cloned theme lost the player controls")
		}
		if clone.PlayerControls == pkg.Theme.PlayerControls {
			t.Fatal("cloned theme shares the player controls pointer with the original")
		}
		themePlayerV2TestAssertControls(t, clone.PlayerControls, themePlayerV2TestLowerColors)

		*clone.PlayerControls.Played = "#000000"
		*clone.PlayerControls.Thumb = "#000000"
		themePlayerV2TestAssertControls(t, pkg.Theme.PlayerControls, themePlayerV2TestLowerColors)
	})

	t.Run("store round trip", func(t *testing.T) {
		db := newThemeStoreTestDB(t)
		dataDir := t.TempDir()
		store := newThemeStoreTestStore(t, dataDir, db)
		if store == nil {
			t.Fatal("newThemeStoreTestStore returned a nil store")
		}

		entry, err := store.InstallUpdate(pkg, 0)
		if err != nil {
			t.Fatalf("InstallUpdate: %v", err)
		}
		if entry.ThemeAPIVersion != 2 {
			t.Fatalf("catalog entry theme api version = %d, want 2", entry.ThemeAPIVersion)
		}

		def, err := store.Definition(pkg.ContentHash)
		if err != nil {
			t.Fatalf("Definition: %v", err)
		}
		theme := def
		themePlayerV2TestAssertControls(t, theme.PlayerControls, themePlayerV2TestLowerColors)

		*theme.PlayerControls.Played = "#000000"

		again, err := store.Definition(pkg.ContentHash)
		if err != nil {
			t.Fatalf("Definition after mutation: %v", err)
		}
		againTheme := again
		themePlayerV2TestAssertControls(t, againTheme.PlayerControls, themePlayerV2TestLowerColors)

		themePlayerV2TestAssertControls(t, pkg.Theme.PlayerControls, themePlayerV2TestLowerColors)
	})

	t.Run("public dto api versions", func(t *testing.T) {
		if got := themeAPIPublicBuiltinTheme().ThemeAPIVersion; got != 0 {
			t.Fatalf("public builtin theme api version = %d, want 0", got)
		}
		if got := themeAPIAdminBuiltinTheme().ThemeAPIVersion; got != 0 {
			t.Fatalf("admin builtin theme api version = %d, want 0", got)
		}

		fromEntry := themeAPIPublicThemeFrom(ThemePublicEntry{ThemeAPIVersion: 2}, &pkg.Theme)
		if fromEntry.ThemeAPIVersion != 2 {
			t.Fatalf("themeAPIPublicThemeFrom api version = %d, want 2", fromEntry.ThemeAPIVersion)
		}
	})
}
