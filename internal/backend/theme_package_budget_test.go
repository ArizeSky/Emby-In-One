package backend

import (
	"archive/zip"
	"bytes"
	"testing"
)

// Budget tests for the strict package contract: 1.5 MiB ZIP, 32 entries,
// 4 MiB per entry, 8 MiB total, compression ratio 100, 64 KiB JSON, at most
// three images, 4M pixels per image, 8M pixels per package and 4096 px sides.

func TestParseThemePackageSizeBudget(t *testing.T) {
	oversized := make([]byte, ThemePackageMaxBytes+1)
	_, err := ParseThemePackage(oversized)
	themeTestRequireCode(t, err, ThemeCodePackageTooLarge)

	// Exactly at the limit is still parsed (and then rejected as a bad ZIP, not
	// as oversized).
	atLimit := make([]byte, ThemePackageMaxBytes)
	_, err = ParseThemePackage(atLimit)
	themeTestRequireCode(t, err, ThemeCodeInvalidPackage)
}

func TestParseThemePackageJSONSizeBudget(t *testing.T) {
	manifest := []byte(themeTestManifestJSON("json-budget"))
	definition := []byte(themeTestThemeJSON(""))

	atLimitManifest := themeTestBuildZip(t,
		themeTestZipEntry{Name: "manifest.json",
			Data: themeTestPadJSON(t, manifest, ThemeJSONMaxBytes)},
		themeTestZipEntry{Name: "theme.json", Data: definition},
	)
	if _, err := ParseThemePackage(atLimitManifest); err != nil {
		t.Fatalf("expected JSON exactly at the limit to be accepted: %v", err)
	}

	overLimitManifest := themeTestBuildZip(t,
		themeTestZipEntry{Name: "manifest.json",
			Data: themeTestPadJSON(t, manifest, ThemeJSONMaxBytes+1)},
		themeTestZipEntry{Name: "theme.json", Data: definition},
	)
	_, err := ParseThemePackage(overLimitManifest)
	themeTestRequireCode(t, err, ThemeCodeJSONTooLarge)

	overLimitDefinition := themeTestBuildZip(t,
		themeTestZipEntry{Name: "manifest.json", Data: manifest},
		themeTestZipEntry{Name: "theme.json",
			Data: themeTestPadJSON(t, definition, ThemeJSONMaxBytes+1)},
	)
	_, err = ParseThemePackage(overLimitDefinition)
	themeTestRequireCode(t, err, ThemeCodeJSONTooLarge)
}

func TestParseThemePackageEntrySizeBudget(t *testing.T) {
	declared := uint64(ThemeEntryMaxUncompressedBytes) + 1
	data := themeTestBuildZip(t, themeTestZipEntry{
		Name:                 "manifest.json",
		Data:                 []byte("{}"),
		DeclaredUncompressed: themeTestUint64Ptr(declared),
		DeclaredCompressed:   themeTestUint64Ptr(declared/ThemeMaxCompressionRatio + 1),
	})
	_, err := ParseThemePackage(data)
	themeTestRequireCode(t, err, ThemeCodeEntryTooLarge)
}

func TestParseThemePackageAggregateSizeBudget(t *testing.T) {
	// Three entries each declaring 3 MiB with a compression ratio just below the
	// limit: the total of 9 MiB must be rejected even though every single entry
	// looks acceptable.
	const entrySize = 3 << 20
	declaredCompressed := uint64(entrySize/ThemeMaxCompressionRatio) + 1
	if uint64(entrySize) > uint64(ThemeMaxCompressionRatio)*declaredCompressed {
		t.Fatalf("test fixture declares a compression ratio above the limit")
	}
	data := themeTestBuildZip(t,
		themeTestZipEntry{Name: "manifest.json", Data: []byte("x"),
			DeclaredUncompressed: themeTestUint64Ptr(entrySize),
			DeclaredCompressed:   themeTestUint64Ptr(declaredCompressed)},
		themeTestZipEntry{Name: "theme.json", Data: []byte("x"),
			DeclaredUncompressed: themeTestUint64Ptr(entrySize),
			DeclaredCompressed:   themeTestUint64Ptr(declaredCompressed)},
		themeTestZipEntry{Name: "assets/x.png", Data: []byte("x"),
			DeclaredUncompressed: themeTestUint64Ptr(entrySize),
			DeclaredCompressed:   themeTestUint64Ptr(declaredCompressed)},
	)
	_, err := ParseThemePackage(data)
	themeTestRequireCode(t, err, ThemeCodeUncompressedBudget)
}

func TestParseThemePackageCompressionRatioBudget(t *testing.T) {
	zeros := make([]byte, 20<<10)
	data := themeTestBuildZip(t, themeTestZipEntry{
		Name:   "manifest.json",
		Data:   zeros,
		Method: zip.Deflate,
	})
	_, err := ParseThemePackage(data)
	themeTestRequireCode(t, err, ThemeCodeCompressionRatio)
}

func TestParseThemePackageImageCountBudget(t *testing.T) {
	image := themeTestPNGBytes(t, 4, 4)
	data := themeTestBuildZip(t,
		themeTestZipEntry{Name: "manifest.json", Data: []byte(themeTestManifestJSON("too-many-images"))},
		themeTestZipEntry{Name: "theme.json", Data: []byte(themeTestThemeJSON("assets/a.png"))},
		themeTestZipEntry{Name: "preview.png", Data: image},
		themeTestZipEntry{Name: "assets/a.png", Data: image},
		themeTestZipEntry{Name: "assets/b.png", Data: image},
		themeTestZipEntry{Name: "assets/c.png", Data: image},
	)
	_, err := ParseThemePackage(data)
	themeTestRequireCode(t, err, ThemeCodeImageBudget)
}

func TestParseThemePackageImagePixelBudget(t *testing.T) {
	// Three images of 3M pixels each: within the per-image cap, above the 8M
	// package cap.
	image := themeTestPNGBytes(t, 2000, 1500)
	data := themeTestBuildZip(t,
		themeTestZipEntry{Name: "manifest.json", Data: []byte(themeTestManifestJSON("pixel-budget"))},
		themeTestZipEntry{Name: "theme.json", Data: []byte(themeTestThemeJSON("assets/a.png"))},
		themeTestZipEntry{Name: "preview.png", Data: image},
		themeTestZipEntry{Name: "assets/a.png", Data: image},
		themeTestZipEntry{Name: "assets/b.png", Data: image},
	)
	_, err := ParseThemePackage(data)
	themeTestRequireCode(t, err, ThemeCodeImageBudget)
}

func TestParseThemePackageImageDimensionBudget(t *testing.T) {
	for _, size := range [][2]int{{ThemeImageMaxDimension + 1, 1}, {1, ThemeImageMaxDimension + 1}} {
		data := themeTestBuildZip(t,
			themeTestZipEntry{Name: "manifest.json", Data: []byte(themeTestManifestJSON("dimension-budget"))},
			themeTestZipEntry{Name: "theme.json", Data: []byte(themeTestThemeJSON(""))},
			themeTestZipEntry{Name: "preview.png", Data: themeTestPNGBytes(t, size[0], size[1])},
		)
		_, err := ParseThemePackage(data)
		themeTestRequireCode(t, err, ThemeCodeImageTooLarge)
	}
}

func TestParseThemePackageImagePixelPerImageBudget(t *testing.T) {
	// 4096x1000 passes the side cap but exceeds the 4M pixel per-image cap.
	data := themeTestBuildZip(t,
		themeTestZipEntry{Name: "manifest.json", Data: []byte(themeTestManifestJSON("per-image-pixels"))},
		themeTestZipEntry{Name: "theme.json", Data: []byte(themeTestThemeJSON(""))},
		themeTestZipEntry{Name: "preview.png", Data: themeTestPNGBytes(t, 4096, 1000)},
	)
	_, err := ParseThemePackage(data)
	themeTestRequireCode(t, err, ThemeCodeImageTooLarge)
}

func TestParseThemePackageImageBoundaryAccepted(t *testing.T) {
	// Exactly 4M pixels is allowed.
	data := themeTestBuildZip(t,
		themeTestZipEntry{Name: "manifest.json", Data: []byte(themeTestManifestJSON("pixel-boundary"))},
		themeTestZipEntry{Name: "theme.json", Data: []byte(themeTestThemeJSON(""))},
		themeTestZipEntry{Name: "preview.jpg", Data: themeTestJPEGBytes(t, 2000, 2000)},
	)
	pkg, err := ParseThemePackage(data)
	if err != nil {
		t.Fatalf("expected exactly 4M pixels to be accepted: %v", err)
	}
	if pkg.Preview == nil || pkg.Preview.Width != 2000 || pkg.Preview.Height != 2000 {
		t.Fatalf("unexpected preview %#v", pkg.Preview)
	}

	// Exactly 4096 wide is allowed.
	wide := themeTestBuildZip(t,
		themeTestZipEntry{Name: "manifest.json", Data: []byte(themeTestManifestJSON("wide-boundary"))},
		themeTestZipEntry{Name: "theme.json", Data: []byte(themeTestThemeJSON(""))},
		themeTestZipEntry{Name: "preview.png", Data: themeTestPNGBytes(t, ThemeImageMaxDimension, 1)},
	)
	if _, err := ParseThemePackage(wide); err != nil {
		t.Fatalf("expected 4096x1 preview to be accepted: %v", err)
	}
}

func TestThemePackageBudgetConstants(t *testing.T) {
	if ThemePackageMaxBytes != 1536*1024 {
		t.Fatalf("unexpected package byte budget %d", ThemePackageMaxBytes)
	}
	if ThemePackageMaxEntries != 32 {
		t.Fatalf("unexpected entry budget %d", ThemePackageMaxEntries)
	}
	if ThemeEntryMaxUncompressedBytes != 4<<20 {
		t.Fatalf("unexpected per-entry byte budget %d", ThemeEntryMaxUncompressedBytes)
	}
	if ThemePackageMaxUncompressedBytes != 8<<20 {
		t.Fatalf("unexpected package byte budget %d", ThemePackageMaxUncompressedBytes)
	}
	if ThemeJSONMaxBytes != 64<<10 {
		t.Fatalf("unexpected JSON byte budget %d", ThemeJSONMaxBytes)
	}
	if ThemeMaxImages != 3 {
		t.Fatalf("unexpected image count budget %d", ThemeMaxImages)
	}
	if ThemeImageMaxDimension != 4096 {
		t.Fatalf("unexpected image dimension budget %d", ThemeImageMaxDimension)
	}
	if ThemeImageMaxPixels != 4000000 || ThemePackageMaxPixels != 8000000 {
		t.Fatalf("unexpected pixel budgets %d/%d", ThemeImageMaxPixels, ThemePackageMaxPixels)
	}
	if ThemeMaxCompressionRatio != 100 {
		t.Fatalf("unexpected compression ratio budget %d", ThemeMaxCompressionRatio)
	}
	if !bytes.Equal([]byte(themeTestPadJSON(t, []byte("{}"), 8)), []byte("{}      ")) {
		t.Fatalf("test helper did not pad JSON with spaces")
	}
}
