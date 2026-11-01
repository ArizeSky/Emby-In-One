package backend

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"strings"
)

// Image validation for theme packages.
//
// Only PNG and JPEG are accepted. The fixed MIME type is derived from the
// decoded content, not from the request or the archive header, and it must
// agree with the documented file extension. Dimensions are checked from
// DecodeConfig first, then the image is fully decoded inside the pixel budget
// so truncated or malicious payloads cannot reach the store. SVG, GIF, WebP and
// any other format are rejected.

const (
	themeMIMEPNG  = "image/png"
	themeMIMEJPEG = "image/jpeg"
)

var (
	themePNGMagic  = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	themeJPEGMagic = []byte{0xff, 0xd8, 0xff}
)

// themeImageMIMEForPath maps an allowed file extension to its fixed MIME type.
func themeImageMIMEForPath(path string) (string, bool) {
	switch {
	case strings.HasSuffix(path, ".png"):
		return themeMIMEPNG, true
	case strings.HasSuffix(path, ".jpg"), strings.HasSuffix(path, ".jpeg"):
		return themeMIMEJPEG, true
	}
	return "", false
}

// themeImageContentMIME sniffs the real container format from magic bytes.
func themeImageContentMIME(payload []byte) string {
	switch {
	case bytes.HasPrefix(payload, themePNGMagic):
		return themeMIMEPNG
	case bytes.HasPrefix(payload, themeJPEGMagic):
		return themeMIMEJPEG
	}
	return ""
}

// decodeThemeImageAsset validates one image entry and returns its descriptor.
func decodeThemeImageAsset(path string, payload []byte) (ThemeAsset, error) {
	expected, ok := themeImageMIMEForPath(path)
	if !ok {
		return ThemeAsset{}, themeErrorf(ThemeCodeUnexpectedEntry, path,
			"only .png, .jpg and .jpeg images are allowed")
	}
	if len(payload) == 0 {
		return ThemeAsset{}, themeErrorf(ThemeCodeInvalidImage, path, "image is empty")
	}
	actual := themeImageContentMIME(payload)
	if actual == "" {
		return ThemeAsset{}, themeErrorf(ThemeCodeInvalidImage, path,
			"image is neither PNG nor JPEG")
	}
	if actual != expected {
		return ThemeAsset{}, themeErrorf(ThemeCodeInvalidImage, path,
			"file extension does not match %s content", actual)
	}
	width, height, err := themeImageConfig(actual, payload)
	if err != nil {
		return ThemeAsset{}, themeErrorf(ThemeCodeInvalidImage, path, "cannot decode image header: %v", err)
	}
	if width <= 0 || height <= 0 {
		return ThemeAsset{}, themeErrorf(ThemeCodeInvalidImage, path,
			"image has invalid dimensions %dx%d", width, height)
	}
	if width > ThemeImageMaxDimension || height > ThemeImageMaxDimension {
		return ThemeAsset{}, themeErrorf(ThemeCodeImageTooLarge, path,
			"image dimensions %dx%d exceed limit %d", width, height, ThemeImageMaxDimension)
	}
	if int64(width)*int64(height) > ThemeImageMaxPixels {
		return ThemeAsset{}, themeErrorf(ThemeCodeImageTooLarge, path,
			"image is %d pixels, limit %d", int64(width)*int64(height), ThemeImageMaxPixels)
	}
	if err := themeDecodeImageFully(actual, payload, width, height); err != nil {
		return ThemeAsset{}, err
	}
	sum := sha256.Sum256(payload)
	return ThemeAsset{
		Path:   path,
		MIME:   actual,
		Width:  width,
		Height: height,
		Bytes:  len(payload),
		SHA256: hex.EncodeToString(sum[:]),
		Data:   payload,
	}, nil
}

func themeImageConfig(mimeType string, payload []byte) (int, int, error) {
	switch mimeType {
	case themeMIMEPNG:
		config, err := png.DecodeConfig(bytes.NewReader(payload))
		if err != nil {
			return 0, 0, err
		}
		return config.Width, config.Height, nil
	case themeMIMEJPEG:
		config, err := jpeg.DecodeConfig(bytes.NewReader(payload))
		if err != nil {
			return 0, 0, err
		}
		return config.Width, config.Height, nil
	}
	return 0, 0, fmt.Errorf("unsupported image type %q", mimeType)
}

// themeDecodeImageFully performs the bounded full decode and requires the
// decoded bounds to agree with the header, so a lying header is rejected.
func themeDecodeImageFully(mimeType string, payload []byte, width, height int) error {
	var decoded image.Image
	var err error
	switch mimeType {
	case themeMIMEPNG:
		decoded, err = png.Decode(bytes.NewReader(payload))
	case themeMIMEJPEG:
		decoded, err = jpeg.Decode(bytes.NewReader(payload))
	default:
		return themeErrorf(ThemeCodeInvalidImage, "", "unsupported image type %q", mimeType)
	}
	if err != nil {
		return themeErrorf(ThemeCodeInvalidImage, "", "cannot decode image: %v", err)
	}
	bounds := decoded.Bounds()
	if bounds.Dx() != width || bounds.Dy() != height {
		return themeErrorf(ThemeCodeInvalidImage, "",
			"decoded image is %dx%d but its header declared %dx%d", bounds.Dx(), bounds.Dy(), width, height)
	}
	return nil
}
