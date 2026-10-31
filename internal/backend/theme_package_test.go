package backend

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
)

// Shared fixtures and a deterministic raw ZIP builder for the theme package
// parser tests. The builder writes the local header, payload and central
// directory itself so flags, compression methods, declared sizes, CRCs and
// external attributes can be made deliberately inconsistent or hostile.

const themeTestLightVariantJSON = `{"bg0":"#FFFFFF","bg1":"#F5F5F5","bg2":"#E8E8E8","bg3":"#D9D9D9",` +
	`"text1":"#111111","text2":"#444444","text3":"#5A5A5A","line":"#CCCCCC","line2":"#B3B3B3",` +
	`"accent":"#0A4FB5","accentText":"#0A4FB5"}`

const themeTestDarkVariantJSON = `{"bg0":"#0F1115","bg1":"#171A20","bg2":"#1F242C","bg3":"#2A303A",` +
	`"text1":"#F2F4F8","text2":"#C3C9D4","text3":"#9AA3B2","line":"#2F3540","line2":"#3D4552",` +
	`"accent":"#6EA8FE","accentText":"#6EA8FE"}`

// Reordered/lower-case equivalents of the fixtures above. They must hash to the
// same canonical content as the upper-case, differently ordered originals.
const themeTestLightVariantReorderedJSON = `{"accentText":"#0a4fb5","accent":"#0a4fb5","line2":"#b3b3b3",` +
	`"line":"#cccccc","text3":"#5a5a5a","text2":"#444444","text1":"#111111","bg3":"#d9d9d9",` +
	`"bg2":"#e8e8e8","bg1":"#f5f5f5","bg0":"#ffffff"}`

const themeTestDarkVariantReorderedJSON = `{"accentText":"#6ea8fe","accent":"#6ea8fe","line2":"#3d4552",` +
	`"line":"#2f3540","text3":"#9aa3b2","text2":"#c3c9d4","text1":"#f2f4f8","bg3":"#2a303a",` +
	`"bg2":"#1f242c","bg1":"#171a20","bg0":"#0f1115"}`

func themeTestManifestJSON(id string) string {
	return fmt.Sprintf(`{"themeApiVersion":1,"id":%q,"name":"Test Theme","author":"Tester",`+
		`"description":"Theme package used by unit tests","version":"1.0.0"}`, id)
}

func themeTestThemeJSON(backgroundImage string) string {
	background := ""
	if backgroundImage != "" {
		background = fmt.Sprintf(`,"backgroundImage":%q`, backgroundImage)
	}
	return fmt.Sprintf(`{"variants":{"light":%s,"dark":%s}%s}`,
		themeTestLightVariantJSON, themeTestDarkVariantJSON, background)
}

func themeTestVariantsJSON(light, dark string) string {
	return `{"variants":{"light":` + light + `,"dark":` + dark + `}}`
}

// themeTestPNGBytes encodes a deterministic RGBA PNG of the requested size.
func themeTestPNGBytes(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := 0; x < width && x < 16; x++ {
		for y := 0; y < height && y < 16; y++ {
			img.SetRGBA(x, y, color.RGBA{R: 0x20, G: 0x60, B: 0xA0, A: 0xFF})
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, img); err != nil {
		t.Fatalf("encode test PNG %dx%d: %v", width, height, err)
	}
	return buffer.Bytes()
}

// themeTestJPEGBytes encodes a deterministic JPEG of the requested size.
func themeTestJPEGBytes(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for x := 0; x < width && x < 16; x++ {
		for y := 0; y < height && y < 16; y++ {
			img.SetRGBA(x, y, color.RGBA{R: 0x90, G: 0x40, B: 0x10, A: 0xFF})
		}
	}
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatalf("encode test JPEG %dx%d: %v", width, height, err)
	}
	return buffer.Bytes()
}

func themeTestPadJSON(t *testing.T, payload []byte, size int) []byte {
	t.Helper()
	if len(payload) > size {
		t.Fatalf("theme test fixture is %d bytes and cannot be padded to %d", len(payload), size)
	}
	out := make([]byte, size)
	copy(out, payload)
	for index := len(payload); index < size; index++ {
		out[index] = ' '
	}
	return out
}

func themeTestUint64Ptr(value uint64) *uint64 { return &value }
func themeTestUint32Ptr(value uint32) *uint32 { return &value }

// themeTestZipEntry describes one hand-built archive entry. Zero Method means
// ZIP store, zero CreatorVersion means a plain MS-DOS entry.
type themeTestZipEntry struct {
	Name                 string
	Data                 []byte
	Method               uint16
	Flags                uint16
	DataDescriptor       bool
	Zip64                bool
	DeclaredUncompressed *uint64
	DeclaredCompressed   *uint64
	DeclaredCRC          *uint32
	CreatorVersion       uint16
	ExternalAttrs        uint32
}

type themeTestBuiltEntry struct {
	name           string
	creator        uint16
	flags          uint16
	method         uint16
	checksum       uint32
	compressed32   uint32
	uncompressed32 uint32
	external       uint32
	extra          []byte
	offset         uint32
}

// themeTestBuildZip serializes entries into a ZIP archive without using
// archive/zip's writer, so tests can declare sizes/CRCs that differ from the
// payload and can set encryption flags and unix modes.
func themeTestBuildZip(t *testing.T, entries ...themeTestZipEntry) []byte {
	t.Helper()
	var archive bytes.Buffer
	built := make([]themeTestBuiltEntry, 0, len(entries))
	for _, entry := range entries {
		if len(entry.Name) > 0xFFFF {
			t.Fatalf("theme test zip: entry name is too long: %d bytes", len(entry.Name))
		}
		method := entry.Method
		if method == 0 {
			method = zip.Store
		}
		payload := entry.Data
		if method == zip.Deflate {
			var compressed bytes.Buffer
			writer, err := flate.NewWriter(&compressed, flate.BestCompression)
			if err != nil {
				t.Fatalf("theme test zip: create deflate writer: %v", err)
			}
			if _, err := writer.Write(entry.Data); err != nil {
				t.Fatalf("theme test zip: write deflate payload: %v", err)
			}
			if err := writer.Close(); err != nil {
				t.Fatalf("theme test zip: close deflate payload: %v", err)
			}
			payload = compressed.Bytes()
		}
		checksum := crc32.ChecksumIEEE(entry.Data)
		if entry.DeclaredCRC != nil {
			checksum = *entry.DeclaredCRC
		}
		uncompressed := uint64(len(entry.Data))
		if entry.DeclaredUncompressed != nil {
			uncompressed = *entry.DeclaredUncompressed
		}
		compressedSize := uint64(len(payload))
		if entry.DeclaredCompressed != nil {
			compressedSize = *entry.DeclaredCompressed
		}
		if !entry.Zip64 && (uncompressed > 0xFFFFFFFF || compressedSize > 0xFFFFFFFF) {
			t.Fatalf("theme test zip: entry %q requires zip64, which this helper only "+
				"supports through the Zip64 flag", entry.Name)
		}
		flags := entry.Flags
		if entry.DataDescriptor {
			flags |= 0x8
		}
		creator := entry.CreatorVersion
		if creator == 0 {
			creator = 20
		}
		compressed32 := uint32(compressedSize)
		uncompressed32 := uint32(uncompressed)
		var extra []byte
		if entry.Zip64 {
			extra = make([]byte, 20)
			binary.LittleEndian.PutUint16(extra[0:], 0x0001)
			binary.LittleEndian.PutUint16(extra[2:], 16)
			binary.LittleEndian.PutUint64(extra[4:], uncompressed)
			binary.LittleEndian.PutUint64(extra[12:], compressedSize)
			compressed32 = 0xFFFFFFFF
			uncompressed32 = 0xFFFFFFFF
		}
		// A data descriptor only zeroes the *local* header copy. The central
		// directory always carries the real CRC and sizes, exactly like
		// archive/zip and every other conforming writer do.
		localCRC := checksum
		localCompressed32 := compressed32
		localUncompressed32 := uncompressed32
		if entry.DataDescriptor {
			localCRC = 0
			localCompressed32 = 0
			localUncompressed32 = 0
		}
		offset := uint32(archive.Len())
		local := make([]byte, 30)
		binary.LittleEndian.PutUint32(local[0:], 0x04034b50)
		binary.LittleEndian.PutUint16(local[4:], 20)
		binary.LittleEndian.PutUint16(local[6:], flags)
		binary.LittleEndian.PutUint16(local[8:], method)
		binary.LittleEndian.PutUint16(local[10:], 0)
		binary.LittleEndian.PutUint16(local[12:], 0x21)
		binary.LittleEndian.PutUint32(local[14:], localCRC)
		binary.LittleEndian.PutUint32(local[18:], localCompressed32)
		binary.LittleEndian.PutUint32(local[22:], localUncompressed32)
		binary.LittleEndian.PutUint16(local[26:], uint16(len(entry.Name)))
		binary.LittleEndian.PutUint16(local[28:], uint16(len(extra)))
		archive.Write(local)
		archive.WriteString(entry.Name)
		archive.Write(extra)
		archive.Write(payload)
		if entry.DataDescriptor {
			descriptor := make([]byte, 16)
			binary.LittleEndian.PutUint32(descriptor[0:], 0x08074b50)
			binary.LittleEndian.PutUint32(descriptor[4:], checksum)
			binary.LittleEndian.PutUint32(descriptor[8:], uint32(compressedSize))
			binary.LittleEndian.PutUint32(descriptor[12:], uint32(uncompressed))
			archive.Write(descriptor)
		}
		built = append(built, themeTestBuiltEntry{
			name:           entry.Name,
			creator:        creator,
			flags:          flags,
			method:         method,
			checksum:       checksum,
			compressed32:   compressed32,
			uncompressed32: uncompressed32,
			external:       entry.ExternalAttrs,
			extra:          extra,
			offset:         offset,
		})
	}
	centralOffset := uint32(archive.Len())
	for _, entry := range built {
		header := make([]byte, 46)
		binary.LittleEndian.PutUint32(header[0:], 0x02014b50)
		binary.LittleEndian.PutUint16(header[4:], entry.creator)
		binary.LittleEndian.PutUint16(header[6:], 20)
		binary.LittleEndian.PutUint16(header[8:], entry.flags)
		binary.LittleEndian.PutUint16(header[10:], entry.method)
		binary.LittleEndian.PutUint16(header[12:], 0)
		binary.LittleEndian.PutUint16(header[14:], 0x21)
		binary.LittleEndian.PutUint32(header[16:], entry.checksum)
		binary.LittleEndian.PutUint32(header[20:], entry.compressed32)
		binary.LittleEndian.PutUint32(header[24:], entry.uncompressed32)
		binary.LittleEndian.PutUint16(header[28:], uint16(len(entry.name)))
		binary.LittleEndian.PutUint16(header[30:], uint16(len(entry.extra)))
		binary.LittleEndian.PutUint16(header[32:], 0)
		binary.LittleEndian.PutUint16(header[34:], 0)
		binary.LittleEndian.PutUint16(header[36:], 0)
		binary.LittleEndian.PutUint32(header[38:], entry.external)
		binary.LittleEndian.PutUint32(header[42:], entry.offset)
		archive.Write(header)
		archive.WriteString(entry.name)
		archive.Write(entry.extra)
	}
	centralSize := uint32(archive.Len()) - centralOffset
	end := make([]byte, 22)
	binary.LittleEndian.PutUint32(end[0:], 0x06054b50)
	binary.LittleEndian.PutUint16(end[4:], 0)
	binary.LittleEndian.PutUint16(end[6:], 0)
	binary.LittleEndian.PutUint16(end[8:], uint16(len(built)))
	binary.LittleEndian.PutUint16(end[10:], uint16(len(built)))
	binary.LittleEndian.PutUint32(end[12:], centralSize)
	binary.LittleEndian.PutUint32(end[16:], centralOffset)
	binary.LittleEndian.PutUint16(end[20:], 0)
	archive.Write(end)
	return archive.Bytes()
}

// themeTestRequireCode asserts that err is a theme package error with the
// expected classification and returns it for further assertions.
func themeTestRequireCode(t *testing.T, err error, want ThemeErrorCode) *ThemePackageError {
	t.Helper()
	if err == nil {
		t.Fatalf("expected theme package error %q, got nil", want)
	}
	var packageErr *ThemePackageError
	if !errors.As(err, &packageErr) {
		t.Fatalf("expected *ThemePackageError, got %T: %v", err, err)
	}
	if packageErr.Code != want {
		t.Fatalf("expected error code %q, got %q (%v)", want, packageErr.Code, err)
	}
	return packageErr
}

// --- valid packages ---

func TestParseThemePackageValidPNG(t *testing.T) {
	background := themeTestPNGBytes(t, 6, 5)
	preview := themeTestPNGBytes(t, 4, 3)
	data := themeTestBuildZip(t,
		themeTestZipEntry{Name: "manifest.json", Data: []byte(themeTestManifestJSON("test-theme-01"))},
		themeTestZipEntry{Name: "theme.json", Data: []byte(themeTestThemeJSON("assets/background.png"))},
		themeTestZipEntry{Name: "preview.png", Data: preview},
		themeTestZipEntry{Name: "assets/background.png", Data: background},
	)
	pkg, err := ParseThemePackage(data)
	if err != nil {
		t.Fatalf("expected valid package, got error: %v", err)
	}
	if pkg.Manifest.ThemeAPIVersion != ThemeAPIVersion {
		t.Fatalf("unexpected themeApiVersion %d", pkg.Manifest.ThemeAPIVersion)
	}
	if pkg.Manifest.ID != "test-theme-01" {
		t.Fatalf("unexpected id %q", pkg.Manifest.ID)
	}
	if pkg.PackageBytes != len(data) {
		t.Fatalf("expected PackageBytes %d, got %d", len(data), pkg.PackageBytes)
	}
	if len(pkg.ContentHash) != 64 {
		t.Fatalf("expected 64 hex characters of content hash, got %q", pkg.ContentHash)
	}
	if _, err := hex.DecodeString(pkg.ContentHash); err != nil {
		t.Fatalf("content hash is not hex: %v", err)
	}
	if len(pkg.Assets) != 1 {
		t.Fatalf("expected 1 asset, got %d", len(pkg.Assets))
	}
	if pkg.Preview == nil || pkg.Preview.Path != "preview.png" {
		t.Fatalf("unexpected preview %#v", pkg.Preview)
	}
	light, ok := pkg.Variant(ThemeVariantLight)
	if !ok || light.Bg0 != "#FFFFFF" || light.AccentText != "#0A4FB5" {
		t.Fatalf("unexpected light variant %#v (found=%v)", light, ok)
	}
	if _, ok := pkg.Variant(ThemeVariantDark); !ok {
		t.Fatalf("dark variant must always be present")
	}
	if _, ok := pkg.Variant("sepia"); ok {
		t.Fatalf("unknown variant must not resolve")
	}
	if pkg.Theme.Appearance.Radius != ThemeAppearanceRadiusDefault ||
		pkg.Theme.Appearance.Density != ThemeDensityDefault {
		t.Fatalf("unexpected default appearance %#v", pkg.Theme.Appearance)
	}
	asset, ok := pkg.AssetByPath("assets/background.png")
	if !ok {
		t.Fatalf("background asset not found")
	}
	if asset.MIME != themeMIMEPNG || asset.Width != 6 || asset.Height != 5 {
		t.Fatalf("unexpected background asset %#v", asset)
	}
	if asset.Bytes != len(background) || !bytes.Equal(asset.Data, background) {
		t.Fatalf("background asset bytes were not preserved")
	}
	sum := sha256.Sum256(background)
	if asset.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("unexpected background sha256 %q", asset.SHA256)
	}
	if _, ok := pkg.AssetByPath(""); ok {
		t.Fatalf("empty path must not resolve")
	}
	all := pkg.AllAssets()
	if len(all) != 2 || all[0].Path != "assets/background.png" || all[1].Path != "preview.png" {
		t.Fatalf("unexpected AllAssets ordering %#v", all)
	}
}

func TestParseThemePackageValidJPEG(t *testing.T) {
	background := themeTestJPEGBytes(t, 7, 4)
	data := themeTestBuildZip(t,
		themeTestZipEntry{Name: "manifest.json", Data: []byte(themeTestManifestJSON("jpeg-theme"))},
		themeTestZipEntry{Name: "theme.json", Data: []byte(themeTestThemeJSON("assets/art.jpeg"))},
		themeTestZipEntry{Name: "preview.jpg", Data: themeTestJPEGBytes(t, 3, 2)},
		themeTestZipEntry{Name: "assets/art.jpeg", Data: background},
	)
	pkg, err := ParseThemePackage(data)
	if err != nil {
		t.Fatalf("expected valid JPEG package, got error: %v", err)
	}
	if pkg.Preview == nil || pkg.Preview.MIME != themeMIMEJPEG || pkg.Preview.Path != "preview.jpg" {
		t.Fatalf("unexpected preview %#v", pkg.Preview)
	}
	asset, ok := pkg.AssetByPath("assets/art.jpeg")
	if !ok || asset.MIME != themeMIMEJPEG || asset.Width != 7 || asset.Height != 4 {
		t.Fatalf("unexpected background asset %#v", asset)
	}
}

func TestParseThemePackageWithoutImages(t *testing.T) {
	data := themeTestBuildZip(t,
		themeTestZipEntry{Name: "manifest.json", Data: []byte(themeTestManifestJSON("plain-theme"))},
		themeTestZipEntry{Name: "theme.json", Data: []byte(themeTestThemeJSON(""))},
	)
	pkg, err := ParseThemePackage(data)
	if err != nil {
		t.Fatalf("expected valid image-less package, got error: %v", err)
	}
	if pkg.Preview != nil || len(pkg.Assets) != 0 {
		t.Fatalf("expected no images, got preview=%#v assets=%d", pkg.Preview, len(pkg.Assets))
	}
	if pkg.Theme.BackgroundImage != "" {
		t.Fatalf("expected empty backgroundImage, got %q", pkg.Theme.BackgroundImage)
	}
}

// --- canonical hash ---

func TestParseThemePackageContentHashIsReproducible(t *testing.T) {
	background := themeTestPNGBytes(t, 8, 6)
	preview := themeTestPNGBytes(t, 3, 2)
	definition := []byte(themeTestThemeJSON("assets/background.png"))
	manifest := []byte(themeTestManifestJSON("stable-theme"))
	first := themeTestBuildZip(t,
		themeTestZipEntry{Name: "manifest.json", Data: manifest},
		themeTestZipEntry{Name: "theme.json", Data: definition},
		themeTestZipEntry{Name: "preview.png", Data: preview},
		themeTestZipEntry{Name: "assets/background.png", Data: background},
	)
	firstPackage, err := ParseThemePackage(first)
	if err != nil {
		t.Fatalf("expected first package to parse: %v", err)
	}

	// Same content, but archive order reversed, deflate compression, data
	// descriptors, JSON keys reordered and colors written in lower case.
	reorderedManifest := fmt.Sprintf(`{"version":"1.0.0","description":"Theme package used by `+
		`unit tests","author":"Tester","name":"Test Theme","id":%q,"themeApiVersion":1}`,
		"stable-theme")
	reorderedDefinition := fmt.Sprintf(`{"backgroundImage":"assets/background.png",`+
		`"variants":{"dark":%s,"light":%s}}`,
		themeTestDarkVariantReorderedJSON, themeTestLightVariantReorderedJSON)
	second := themeTestBuildZip(t,
		themeTestZipEntry{Name: "assets/background.png", Data: background, Method: zip.Deflate, DataDescriptor: true},
		themeTestZipEntry{Name: "preview.png", Data: preview, Method: zip.Deflate},
		themeTestZipEntry{Name: "theme.json", Data: []byte(reorderedDefinition)},
		themeTestZipEntry{Name: "manifest.json", Data: []byte(reorderedManifest), Method: zip.Deflate},
	)
	if bytes.Equal(first, second) {
		t.Fatalf("test fixtures must differ at the archive-byte level")
	}
	secondPackage, err := ParseThemePackage(second)
	if err != nil {
		t.Fatalf("expected reordered package to parse: %v", err)
	}
	if firstPackage.ContentHash != secondPackage.ContentHash {
		t.Fatalf("content hash is not canonical: %q != %q",
			firstPackage.ContentHash, secondPackage.ContentHash)
	}

	// A different asset changes the hash.
	differentAsset := themeTestBuildZip(t,
		themeTestZipEntry{Name: "manifest.json", Data: manifest},
		themeTestZipEntry{Name: "theme.json", Data: definition},
		themeTestZipEntry{Name: "preview.png", Data: preview},
		themeTestZipEntry{Name: "assets/background.png", Data: themeTestPNGBytes(t, 8, 7)},
	)
	thirdPackage, err := ParseThemePackage(differentAsset)
	if err != nil {
		t.Fatalf("expected changed-asset package to parse: %v", err)
	}
	if thirdPackage.ContentHash == firstPackage.ContentHash {
		t.Fatalf("content hash did not change with different asset bytes")
	}

	// A different manifest value changes the hash.
	differentName := themeTestBuildZip(t,
		themeTestZipEntry{Name: "manifest.json", Data: bytes.Replace(manifest,
			[]byte(`"name":"Test Theme"`), []byte(`"name":"Other Theme"`), 1)},
		themeTestZipEntry{Name: "theme.json", Data: definition},
		themeTestZipEntry{Name: "preview.png", Data: preview},
		themeTestZipEntry{Name: "assets/background.png", Data: background},
	)
	fourthPackage, err := ParseThemePackage(differentName)
	if err != nil {
		t.Fatalf("expected renamed package to parse: %v", err)
	}
	if fourthPackage.ContentHash == firstPackage.ContentHash {
		t.Fatalf("content hash did not change with a different manifest name")
	}
}

// --- asset reference rules ---

func TestParseThemePackageAssetReferenceRules(t *testing.T) {
	imageBytes := themeTestPNGBytes(t, 4, 4)
	nested := themeTestBuildZip(t,
		themeTestZipEntry{Name: "manifest.json", Data: []byte(themeTestManifestJSON("nested-theme"))},
		themeTestZipEntry{Name: "theme.json", Data: []byte(themeTestThemeJSON("assets/deep/background.png"))},
		themeTestZipEntry{Name: "assets/deep/background.png", Data: imageBytes},
	)
	if _, err := ParseThemePackage(nested); err != nil {
		t.Fatalf("expected nested asset path to be accepted: %v", err)
	}

	unreferenced := themeTestBuildZip(t,
		themeTestZipEntry{Name: "manifest.json", Data: []byte(themeTestManifestJSON("hosting-theme"))},
		themeTestZipEntry{Name: "theme.json", Data: []byte(themeTestThemeJSON(""))},
		themeTestZipEntry{Name: "assets/extra.png", Data: imageBytes},
	)
	_, err := ParseThemePackage(unreferenced)
	themeTestRequireCode(t, err, ThemeCodeUnreferencedAsset)

	missing := themeTestBuildZip(t,
		themeTestZipEntry{Name: "manifest.json", Data: []byte(themeTestManifestJSON("missing-theme"))},
		themeTestZipEntry{Name: "theme.json", Data: []byte(themeTestThemeJSON("assets/gone.png"))},
	)
	_, err = ParseThemePackage(missing)
	themeTestRequireCode(t, err, ThemeCodeMissingAsset)

	// The background image must live under assets/ and use a PNG/JPEG suffix.
	for _, background := range []string{"preview.png", "/assets/background.png",
		"assets/../background.png", "assets/background.gif"} {
		data := themeTestBuildZip(t,
			themeTestZipEntry{Name: "manifest.json", Data: []byte(themeTestManifestJSON("bad-background"))},
			themeTestZipEntry{Name: "theme.json", Data: []byte(themeTestThemeJSON(background))},
		)
		_, err := ParseThemePackage(data)
		if err == nil {
			t.Fatalf("expected backgroundImage %q to be rejected", background)
		}
		if code := ThemePackageErrorCode(err); code == "" {
			t.Fatalf("expected a typed error for backgroundImage %q, got %v", background, err)
		}
	}
}

// --- error plumbing ---

func TestThemePackageErrorClassification(t *testing.T) {
	if code := ThemePackageErrorCode(nil); code != "" {
		t.Fatalf("expected empty code for nil error, got %q", code)
	}
	if code := ThemePackageErrorCode(errors.New("other")); code != "" {
		t.Fatalf("expected empty code for foreign error, got %q", code)
	}
	err := &ThemePackageError{Code: ThemeCodeInvalidPath, Path: "assets/x.png", Detail: "bad"}
	if !strings.Contains(err.Error(), "invalid_path") || !strings.Contains(err.Error(), "assets/x.png") {
		t.Fatalf("unexpected error text %q", err.Error())
	}
	noPath := &ThemePackageError{Code: ThemeCodeInvalidPackage, Detail: "bad"}
	if strings.Contains(noPath.Error(), "()") {
		t.Fatalf("unexpected empty path in error text %q", noPath.Error())
	}
}
