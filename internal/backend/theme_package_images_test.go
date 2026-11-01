package backend

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// Image validation tests: content-derived MIME, extension agreement, full
// decode, dimension and pixel caps. PNG/JPEG are the only accepted formats.

func TestDecodeThemeImageAssetValidPNG(t *testing.T) {
	payload := themeTestPNGBytes(t, 5, 3)
	asset, err := decodeThemeImageAsset("assets/background.png", payload)
	if err != nil {
		t.Fatalf("expected valid PNG asset, got error: %v", err)
	}
	if asset.Path != "assets/background.png" || asset.MIME != themeMIMEPNG {
		t.Fatalf("unexpected asset descriptor %#v", asset)
	}
	if asset.Width != 5 || asset.Height != 3 {
		t.Fatalf("unexpected dimensions %dx%d", asset.Width, asset.Height)
	}
	if asset.Bytes != len(payload) {
		t.Fatalf("unexpected byte count %d", asset.Bytes)
	}
	if !bytes.Equal(asset.Data, payload) {
		t.Fatalf("asset bytes were not preserved")
	}
	sum := sha256.Sum256(payload)
	if asset.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatalf("unexpected sha256 %q", asset.SHA256)
	}
}

func TestDecodeThemeImageAssetValidJPEG(t *testing.T) {
	payload := themeTestJPEGBytes(t, 6, 4)
	for _, path := range []string{"assets/photo.jpg", "assets/photo.jpeg", "preview.jpg"} {
		asset, err := decodeThemeImageAsset(path, payload)
		if err != nil {
			t.Fatalf("expected %q to be accepted: %v", path, err)
		}
		if asset.MIME != themeMIMEJPEG || asset.Width != 6 || asset.Height != 4 {
			t.Fatalf("unexpected asset descriptor for %q: %#v", path, asset)
		}
	}
}

func TestDecodeThemeImageAssetRejectsBadPaths(t *testing.T) {
	png := themeTestPNGBytes(t, 2, 2)
	for _, path := range []string{
		"assets/icon.gif",
		"assets/icon.webp",
		"assets/icon.svg",
		"assets/icon.PNG",
		"assets/icon",
		"preview.webp",
		"theme.json",
	} {
		_, err := decodeThemeImageAsset(path, png)
		if err == nil {
			t.Fatalf("expected path %q to be rejected", path)
		}
		themeTestRequireCode(t, err, ThemeCodeUnexpectedEntry)
	}
}

func TestDecodeThemeImageAssetRejectsBadContent(t *testing.T) {
	png := themeTestPNGBytes(t, 4, 4)
	jpeg := themeTestJPEGBytes(t, 4, 4)
	cases := []struct {
		name string
		path string
		data []byte
	}{
		{"empty", "assets/x.png", nil},
		{"gif named png", "assets/x.png", append([]byte("GIF89a"), make([]byte, 16)...)},
		{"svg named png", "assets/x.png", []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`)},
		{"webp named png", "assets/x.png", append([]byte("RIFF\x00\x00\x00\x00WEBP"), make([]byte, 8)...)},
		{"png named jpg", "assets/x.jpg", png},
		{"jpeg named png", "assets/x.png", jpeg},
		{"jpeg named jpeg is fine", "", nil},
		{"png signature only", "assets/x.png", png[:8]},
		{"truncated png", "assets/x.png", png[:len(png)/2]},
		{"truncated jpeg", "assets/x.jpg", jpeg[:len(jpeg)/2]},
		{"text", "assets/x.jpeg", []byte("not an image at all")},
	}
	for _, testCase := range cases {
		if testCase.path == "" {
			continue
		}
		_, err := decodeThemeImageAsset(testCase.path, testCase.data)
		if err == nil {
			t.Fatalf("expected %s to be rejected", testCase.name)
		}
		themeTestRequireCode(t, err, ThemeCodeInvalidImage)
	}
}

func TestThemeImageContentMIME(t *testing.T) {
	if got := themeImageContentMIME(themeTestPNGBytes(t, 1, 1)); got != themeMIMEPNG {
		t.Fatalf("expected PNG magic to be detected, got %q", got)
	}
	if got := themeImageContentMIME(themeTestJPEGBytes(t, 1, 1)); got != themeMIMEJPEG {
		t.Fatalf("expected JPEG magic to be detected, got %q", got)
	}
	for _, payload := range [][]byte{
		nil,
		[]byte("GIF89a"),
		[]byte("BM"),
		{0x89, 'P', 'N', 'G'},
	} {
		if got := themeImageContentMIME(payload); got != "" {
			t.Fatalf("expected no MIME for %q, got %q", payload, got)
		}
	}
}

func TestDecodeThemeImageAssetDimensionAndPixelCaps(t *testing.T) {
	// 4096 on one side is allowed.
	if _, err := decodeThemeImageAsset("assets/wide.png",
		themeTestPNGBytes(t, ThemeImageMaxDimension, 1)); err != nil {
		t.Fatalf("expected 4096x1 image to be accepted: %v", err)
	}
	// 4097 on one side is rejected.
	_, err := decodeThemeImageAsset("assets/wide.png",
		themeTestPNGBytes(t, ThemeImageMaxDimension+1, 1))
	themeTestRequireCode(t, err, ThemeCodeImageTooLarge)
	_, err = decodeThemeImageAsset("assets/tall.png",
		themeTestPNGBytes(t, 1, ThemeImageMaxDimension+1))
	themeTestRequireCode(t, err, ThemeCodeImageTooLarge)

	// Exactly 4M pixels is allowed; one pixel over is not.
	if _, err := decodeThemeImageAsset("assets/limit.png",
		themeTestPNGBytes(t, 2000, 2000)); err != nil {
		t.Fatalf("expected exactly 4M pixels to be accepted: %v", err)
	}
	_, err = decodeThemeImageAsset("assets/over.png",
		themeTestPNGBytes(t, 4096, 1000))
	themeTestRequireCode(t, err, ThemeCodeImageTooLarge)
}

func TestDecodeThemeImageAssetRejectsHeaderContentMismatch(t *testing.T) {
	// A PNG payload whose file extension claims JPEG fails before decoding.
	_, err := decodeThemeImageAsset("assets/mismatch.jpeg", themeTestPNGBytes(t, 2, 2))
	themeTestRequireCode(t, err, ThemeCodeInvalidImage)

	// Extension and magic agree but the tail is garbage: the header parses while
	// the full decode fails.
	png := themeTestPNGBytes(t, 16, 16)
	corrupt := append([]byte{}, png[:len(png)-4]...)
	corrupt = append(corrupt, 0x00, 0x00, 0x00, 0x00)
	_, err = decodeThemeImageAsset("assets/corrupt.png", corrupt)
	themeTestRequireCode(t, err, ThemeCodeInvalidImage)
}
