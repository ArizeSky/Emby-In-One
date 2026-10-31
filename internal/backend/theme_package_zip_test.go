package backend

import (
	"archive/zip"
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// ZIP-level rejection tests: malformed archives, hostile paths, duplicate and
// case-colliding names, unsupported entry types/compression, encrypted entries
// and lying central-directory metadata.

func TestParseThemePackageRejectsInvalidArchives(t *testing.T) {
	if _, err := ParseThemePackage(nil); err == nil {
		t.Fatalf("expected nil package to be rejected")
	} else {
		themeTestRequireCode(t, err, ThemeCodeInvalidPackage)
	}
	if _, err := ParseThemePackage([]byte{}); err == nil {
		t.Fatalf("expected empty package to be rejected")
	} else {
		themeTestRequireCode(t, err, ThemeCodeInvalidPackage)
	}
	if _, err := ParseThemePackage([]byte("not a zip archive")); err == nil {
		t.Fatalf("expected non-ZIP bytes to be rejected")
	} else {
		themeTestRequireCode(t, err, ThemeCodeInvalidPackage)
	}

	valid := themeTestBuildZip(t,
		themeTestZipEntry{Name: "manifest.json", Data: []byte(themeTestManifestJSON("truncated"))},
		themeTestZipEntry{Name: "theme.json", Data: []byte(themeTestThemeJSON(""))},
	)
	for _, cut := range []int{1, 4, 20, len(valid) / 2, len(valid) - 1} {
		if cut <= 0 || cut >= len(valid) {
			continue
		}
		_, err := ParseThemePackage(valid[:cut])
		themeTestRequireCode(t, err, ThemeCodeInvalidPackage)
	}
}

func TestParseThemePackageRejectsUnsafeEntryNames(t *testing.T) {
	names := []string{
		"",
		"../manifest.json",
		"/manifest.json",
		`assets\background.png`,
		"assets//background.png",
		"./manifest.json",
		"assets/./background.png",
		"assets/",
		"manifest.json.",
		"manifest.json\x00",
		"m\u00e4nifest.json",
		"assets/background.png ",
		"assets/../background.png",
		"C:/manifest.json",
		strings.Repeat("a", 256),
	}
	for _, name := range names {
		data := themeTestBuildZip(t, themeTestZipEntry{Name: name, Data: []byte("x")})
		_, err := ParseThemePackage(data)
		if err == nil {
			t.Fatalf("expected entry name %q to be rejected", name)
		}
		themeTestRequireCode(t, err, ThemeCodeInvalidPath)
	}
}

func TestParseThemePackageRejectsEntriesOutsideLayout(t *testing.T) {
	for _, name := range []string{
		"theme.css",
		"theme.js",
		"index.html",
		"assets/app.js",
		"assets/icon.svg",
		"assets/icon.gif",
		"assets/icon.webp",
		"assets/icon.PNG",
		"ASSETS/icon.png",
		"preview.webp",
		"manifest.JSON",
		"nested/manifest.json",
	} {
		data := themeTestBuildZip(t, themeTestZipEntry{Name: name, Data: []byte("x")})
		_, err := ParseThemePackage(data)
		if err == nil {
			t.Fatalf("expected entry %q to be rejected", name)
		}
		themeTestRequireCode(t, err, ThemeCodeUnexpectedEntry)
	}
}

func TestParseThemePackageRejectsDuplicateAndCaseCollidingEntries(t *testing.T) {
	manifest := []byte(themeTestManifestJSON("conflict-theme"))
	definition := []byte(themeTestThemeJSON(""))

	duplicate := themeTestBuildZip(t,
		themeTestZipEntry{Name: "manifest.json", Data: manifest},
		themeTestZipEntry{Name: "theme.json", Data: definition},
		themeTestZipEntry{Name: "manifest.json", Data: manifest},
	)
	_, err := ParseThemePackage(duplicate)
	themeTestRequireCode(t, err, ThemeCodePathConflict)

	caseCollidingManifest := themeTestBuildZip(t,
		themeTestZipEntry{Name: "manifest.json", Data: manifest},
		themeTestZipEntry{Name: "MANIFEST.JSON", Data: manifest},
		themeTestZipEntry{Name: "theme.json", Data: definition},
	)
	_, err = ParseThemePackage(caseCollidingManifest)
	themeTestRequireCode(t, err, ThemeCodePathConflict)

	caseCollidingAsset := themeTestBuildZip(t,
		themeTestZipEntry{Name: "manifest.json", Data: manifest},
		themeTestZipEntry{Name: "theme.json", Data: definition},
		themeTestZipEntry{Name: "assets/background.png", Data: themeTestPNGBytes(t, 2, 2)},
		themeTestZipEntry{Name: "assets/Background.png", Data: themeTestPNGBytes(t, 2, 2)},
	)
	_, err = ParseThemePackage(caseCollidingAsset)
	themeTestRequireCode(t, err, ThemeCodePathConflict)

	bothPreviews := themeTestBuildZip(t,
		themeTestZipEntry{Name: "manifest.json", Data: manifest},
		themeTestZipEntry{Name: "theme.json", Data: definition},
		themeTestZipEntry{Name: "preview.png", Data: themeTestPNGBytes(t, 2, 2)},
		themeTestZipEntry{Name: "preview.jpg", Data: themeTestJPEGBytes(t, 2, 2)},
	)
	_, err = ParseThemePackage(bothPreviews)
	themeTestRequireCode(t, err, ThemeCodePathConflict)
}

func TestParseThemePackageRejectsSpecialEntries(t *testing.T) {
	const unixCreator = 0x0314
	cases := []struct {
		name   string
		mode   uint32
		expect ThemeErrorCode
	}{
		{"directory", 0o040755, ThemeCodeUnsupportedEntry},
		{"symlink", 0o120777, ThemeCodeUnsupportedEntry},
		{"fifo", 0o010644, ThemeCodeUnsupportedEntry},
		{"socket", 0o140644, ThemeCodeUnsupportedEntry},
		{"char device", 0o020644, ThemeCodeUnsupportedEntry},
		{"block device", 0o060644, ThemeCodeUnsupportedEntry},
	}
	for _, testCase := range cases {
		data := themeTestBuildZip(t, themeTestZipEntry{
			Name:           "assets/regular.png",
			Data:           themeTestPNGBytes(t, 2, 2),
			CreatorVersion: unixCreator,
			ExternalAttrs:  testCase.mode << 16,
		})
		_, err := ParseThemePackage(data)
		if err == nil {
			t.Fatalf("expected %s entry to be rejected", testCase.name)
		}
		themeTestRequireCode(t, err, testCase.expect)
	}

	// A regular unix-mode image entry stays acceptable.
	regular := themeTestBuildZip(t,
		themeTestZipEntry{Name: "manifest.json", Data: []byte(themeTestManifestJSON("regular-theme"))},
		themeTestZipEntry{Name: "theme.json", Data: []byte(themeTestThemeJSON(""))},
		themeTestZipEntry{Name: "assets/regular.png", Data: themeTestPNGBytes(t, 2, 2),
			CreatorVersion: unixCreator, ExternalAttrs: 0o100644 << 16},
	)
	// The asset is unreferenced, which is a different contract rule than mode.
	_, err := ParseThemePackage(regular)
	themeTestRequireCode(t, err, ThemeCodeUnreferencedAsset)
}

func TestParseThemePackageRejectsEncryptedEntries(t *testing.T) {
	data := themeTestBuildZip(t, themeTestZipEntry{
		Name:  "manifest.json",
		Data:  []byte(themeTestManifestJSON("encrypted")),
		Flags: 0x1,
	})
	_, err := ParseThemePackage(data)
	themeTestRequireCode(t, err, ThemeCodeEncryptedEntry)
}

func TestParseThemePackageRejectsUnsupportedCompression(t *testing.T) {
	for _, method := range []uint16{12, 14, 93, 99} {
		data := themeTestBuildZip(t, themeTestZipEntry{
			Name:   "manifest.json",
			Data:   []byte(themeTestManifestJSON("method")),
			Method: method,
		})
		_, err := ParseThemePackage(data)
		if err == nil {
			t.Fatalf("expected method %d to be rejected", method)
		}
		themeTestRequireCode(t, err, ThemeCodeUnsupportedCompression)
	}
}

func TestParseThemePackageRejectsLyingChecksumsAndSizes(t *testing.T) {
	manifest := []byte(themeTestManifestJSON("lying-theme"))
	definition := []byte(themeTestThemeJSON(""))

	badCRC := themeTestBuildZip(t,
		themeTestZipEntry{Name: "manifest.json", Data: manifest, DeclaredCRC: themeTestUint32Ptr(0xDEADBEEF)},
		themeTestZipEntry{Name: "theme.json", Data: definition},
	)
	_, err := ParseThemePackage(badCRC)
	themeTestRequireCode(t, err, ThemeCodeChecksum)

	tooSmall := themeTestBuildZip(t,
		themeTestZipEntry{Name: "manifest.json", Data: manifest,
			DeclaredUncompressed: themeTestUint64Ptr(uint64(len(manifest) / 2))},
		themeTestZipEntry{Name: "theme.json", Data: definition},
	)
	_, err = ParseThemePackage(tooSmall)
	themeTestRequireCode(t, err, ThemeCodeChecksum)

	tooLarge := themeTestBuildZip(t,
		themeTestZipEntry{Name: "manifest.json", Data: manifest,
			DeclaredUncompressed: themeTestUint64Ptr(uint64(len(manifest) * 4))},
		themeTestZipEntry{Name: "theme.json", Data: definition},
	)
	_, err = ParseThemePackage(tooLarge)
	themeTestRequireCode(t, err, ThemeCodeChecksum)

	truncatedCompressed := themeTestBuildZip(t,
		themeTestZipEntry{Name: "manifest.json", Data: manifest,
			DeclaredCompressed: themeTestUint64Ptr(uint64(len(manifest) / 4))},
		themeTestZipEntry{Name: "theme.json", Data: definition},
	)
	_, err = ParseThemePackage(truncatedCompressed)
	themeTestRequireCode(t, err, ThemeCodeChecksum)
}

func TestParseThemePackageRejectsZip64OversizedDeclaration(t *testing.T) {
	data := themeTestBuildZip(t, themeTestZipEntry{
		Name:                 "manifest.json",
		Data:                 []byte("{}"),
		Zip64:                true,
		DeclaredUncompressed: themeTestUint64Ptr(5 << 20),
		DeclaredCompressed:   themeTestUint64Ptr(5 << 20),
	})
	_, err := ParseThemePackage(data)
	themeTestRequireCode(t, err, ThemeCodeEntryTooLarge)
}

func TestParseThemePackageAcceptsDataDescriptorEntries(t *testing.T) {
	data := themeTestBuildZip(t,
		themeTestZipEntry{Name: "manifest.json", Data: []byte(themeTestManifestJSON("descriptor-theme")),
			DataDescriptor: true, Method: zip.Deflate},
		themeTestZipEntry{Name: "theme.json", Data: []byte(themeTestThemeJSON("")),
			DataDescriptor: true, Method: zip.Deflate},
	)
	if _, err := ParseThemePackage(data); err != nil {
		t.Fatalf("expected data-descriptor archive to be accepted: %v", err)
	}
}

// TestParseThemePackageAcceptsArchiveZipWriterDescriptors cross-checks the
// parser against a conforming writer instead of only against the hand-built
// fixture above. archive/zip always emits a data descriptor (flag bit 3) for
// non-directory entries, so this separates fixture bugs from parser bugs.
func TestParseThemePackageAcceptsArchiveZipWriterDescriptors(t *testing.T) {
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	for _, entry := range []struct {
		name string
		data []byte
	}{
		{"manifest.json", []byte(themeTestManifestJSON("standard-writer-theme"))},
		{"theme.json", []byte(themeTestThemeJSON(""))},
	} {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		file, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatalf("archive/zip create %q: %v", entry.name, err)
		}
		if _, err := file.Write(entry.data); err != nil {
			t.Fatalf("archive/zip write %q: %v", entry.name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("archive/zip close: %v", err)
	}
	data := buffer.Bytes()

	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("archive/zip output is not readable: %v", err)
	}
	for _, file := range reader.File {
		if file.Flags&0x8 == 0 {
			t.Fatalf("expected archive/zip to emit a data descriptor for %q", file.Name)
		}
	}

	pkg, err := ParseThemePackage(data)
	if err != nil {
		t.Fatalf("expected archive/zip data-descriptor package to be accepted: %v", err)
	}
	if pkg.Manifest.ID != "standard-writer-theme" {
		t.Fatalf("unexpected manifest id %q", pkg.Manifest.ID)
	}
}

func TestParseThemePackageRequiresCoreEntries(t *testing.T) {
	empty := themeTestBuildZip(t)
	_, err := ParseThemePackage(empty)
	themeTestRequireCode(t, err, ThemeCodeMissingEntry)

	manifestOnly := themeTestBuildZip(t,
		themeTestZipEntry{Name: "manifest.json", Data: []byte(themeTestManifestJSON("manifest-only"))},
	)
	_, err = ParseThemePackage(manifestOnly)
	themeTestRequireCode(t, err, ThemeCodeMissingEntry)

	themeOnly := themeTestBuildZip(t,
		themeTestZipEntry{Name: "theme.json", Data: []byte(themeTestThemeJSON(""))},
	)
	_, err = ParseThemePackage(themeOnly)
	themeTestRequireCode(t, err, ThemeCodeMissingEntry)
}

func TestParseThemePackageRejectsTooManyEntries(t *testing.T) {
	entries := make([]themeTestZipEntry, 0, ThemePackageMaxEntries+1)
	for index := 0; index <= ThemePackageMaxEntries; index++ {
		entries = append(entries, themeTestZipEntry{
			Name: fmt.Sprintf("entry-%02d", index),
			Data: []byte("x"),
		})
	}
	data := themeTestBuildZip(t, entries...)
	_, err := ParseThemePackage(data)
	themeTestRequireCode(t, err, ThemeCodeTooManyEntries)
}
