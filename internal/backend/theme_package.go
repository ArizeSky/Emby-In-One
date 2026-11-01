package backend

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
	"unicode/utf8"
)

// Parsing of a web theme package ZIP (theme API v1).
//
// The archive is never treated as a directory listing to trust: entry names,
// flags, compression methods, declared sizes and compression ratios are
// validated up front, every entry is then inflated through a limited reader and
// its actual byte count plus CRC is checked. Only the fixed layout
//
//	manifest.json
//	theme.json
//	preview.png | preview.jpg (optional)
//	assets/**.png | .jpg | .jpeg
//
// is accepted, so CSS, HTML, JavaScript, SVG, fonts and remote references can
// never enter the pipeline.

// Package layout constants.
const (
	themeEntryManifest = "manifest.json"
	themeEntryTheme    = "theme.json"
	themeEntryPreview  = "preview.png"
	themeEntryPreviewJ = "preview.jpg"
	themeAssetPrefix   = "assets/"
)

// ParseThemePackage validates a complete theme package and returns its frozen
// representation. Every error is a *ThemePackageError; use
// ThemePackageErrorCode for classification.
func ParseThemePackage(data []byte) (*ThemePackage, error) {
	if len(data) == 0 {
		return nil, themeErrorf(ThemeCodeInvalidPackage, "", "package is empty")
	}
	if len(data) > ThemePackageMaxBytes {
		return nil, themeErrorf(ThemeCodePackageTooLarge, "",
			"package is %d bytes, limit %d", len(data), ThemePackageMaxBytes)
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, themeErrorf(ThemeCodeInvalidPackage, "", "unreadable ZIP archive: %v", err)
	}
	files := reader.File
	if len(files) == 0 {
		return nil, themeErrorf(ThemeCodeMissingEntry, "", "archive contains no entries")
	}
	if len(files) > ThemePackageMaxEntries {
		return nil, themeErrorf(ThemeCodeTooManyEntries, "",
			"archive has %d entries, limit %d", len(files), ThemePackageMaxEntries)
	}

	entries := make(map[string]*zip.File, len(files))
	caseFolded := make(map[string]string, len(files))
	var declaredTotal uint64
	for _, file := range files {
		path, err := validateThemePackagePath(file.Name)
		if err != nil {
			return nil, err
		}
		if _, exists := entries[path]; exists {
			return nil, themeErrorf(ThemeCodePathConflict, path, "duplicate archive entry")
		}
		folded := strings.ToLower(path)
		if previous, exists := caseFolded[folded]; exists {
			return nil, themeErrorf(ThemeCodePathConflict, path, "collides with %q ignoring case", previous)
		}
		if !themePackageEntryAllowed(path) {
			return nil, themeErrorf(ThemeCodeUnexpectedEntry, path,
				"entry is not allowed by the theme package layout")
		}
		if err := validateThemeZipEntry(file, path); err != nil {
			return nil, err
		}
		declaredTotal += file.UncompressedSize64
		entries[path] = file
		caseFolded[folded] = path
	}
	if declaredTotal > ThemePackageMaxUncompressedBytes {
		return nil, themeErrorf(ThemeCodeUncompressedBudget, "",
			"declared uncompressed size %d exceeds limit %d", declaredTotal, ThemePackageMaxUncompressedBytes)
	}

	_, hasPreviewPNG := entries[themeEntryPreview]
	_, hasPreviewJPG := entries[themeEntryPreviewJ]
	if hasPreviewPNG && hasPreviewJPG {
		return nil, themeErrorf(ThemeCodePathConflict, themeEntryPreviewJ,
			"only one of preview.png and preview.jpg may be packaged")
	}
	for _, required := range []string{themeEntryManifest, themeEntryTheme} {
		if _, ok := entries[required]; !ok {
			return nil, themeErrorf(ThemeCodeMissingEntry, required, "required entry is missing")
		}
	}

	// Read every entry (the layout contains only known entries) in sorted order
	// so failures are deterministic, and enforce the aggregate inflation budget
	// on actual bytes instead of trusting the central directory.
	paths := make([]string, 0, len(entries))
	for path := range entries {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	contents := make(map[string][]byte, len(entries))
	var actualTotal uint64
	for _, path := range paths {
		payload, err := readThemeZipEntry(entries[path], path)
		if err != nil {
			return nil, err
		}
		actualTotal += uint64(len(payload))
		if actualTotal > ThemePackageMaxUncompressedBytes {
			return nil, themeErrorf(ThemeCodeUncompressedBudget, path,
				"uncompressed total %d exceeds limit %d", actualTotal, ThemePackageMaxUncompressedBytes)
		}
		contents[path] = payload
	}

	manifest, err := parseThemeManifest(contents[themeEntryManifest])
	if err != nil {
		return nil, err
	}
	definition, err := parseThemeDefinitionForVersion(contents[themeEntryTheme], manifest.ThemeAPIVersion)
	if err != nil {
		return nil, err
	}

	assetPaths := make([]string, 0, len(contents))
	for path := range contents {
		if strings.HasPrefix(path, themeAssetPrefix) {
			assetPaths = append(assetPaths, path)
		}
	}
	sort.Strings(assetPaths)

	// The image-count budget is checked before any image is decoded so an
	// oversized package is rejected without inflating buffers it could never be
	// allowed to keep. preview.png/preview.jpg are mutually exclusive above.
	imageCount := len(assetPaths)
	if _, ok := contents[themeEntryPreview]; ok {
		imageCount++
	}
	if _, ok := contents[themeEntryPreviewJ]; ok {
		imageCount++
	}
	if imageCount > ThemeMaxImages {
		return nil, themeErrorf(ThemeCodeImageBudget, "",
			"package has %d images, limit %d", imageCount, ThemeMaxImages)
	}

	var preview *ThemeAsset
	for _, candidate := range []string{themeEntryPreview, themeEntryPreviewJ} {
		payload, ok := contents[candidate]
		if !ok {
			continue
		}
		asset, err := decodeThemeImageAsset(candidate, payload)
		if err != nil {
			return nil, err
		}
		preview = &asset
		break
	}
	assets := make([]ThemeAsset, 0, len(assetPaths))
	for _, path := range assetPaths {
		asset, err := decodeThemeImageAsset(path, contents[path])
		if err != nil {
			return nil, err
		}
		assets = append(assets, asset)
	}
	var totalPixels int64
	for _, asset := range assets {
		totalPixels += int64(asset.Width) * int64(asset.Height)
	}
	if preview != nil {
		totalPixels += int64(preview.Width) * int64(preview.Height)
	}
	if totalPixels > ThemePackageMaxPixels {
		return nil, themeErrorf(ThemeCodeImageBudget, "",
			"package images total %d pixels, limit %d", totalPixels, ThemePackageMaxPixels)
	}

	// Conservative asset rule: exactly the background image may be packaged.
	// Rejecting unreferenced images keeps the package an appearance payload
	// instead of anonymous public image hosting.
	if definition.BackgroundImage != "" {
		referenced := false
		for _, asset := range assets {
			if asset.Path == definition.BackgroundImage {
				referenced = true
				break
			}
		}
		if !referenced {
			return nil, themeErrorf(ThemeCodeMissingAsset, definition.BackgroundImage,
				"backgroundImage does not reference a packaged image")
		}
	}
	for _, asset := range assets {
		if asset.Path != definition.BackgroundImage {
			return nil, themeErrorf(ThemeCodeUnreferencedAsset, asset.Path,
				"image asset is not referenced by theme.json backgroundImage")
		}
	}

	contentHash, err := themePackageContentHash(manifest, definition, preview, assets)
	if err != nil {
		return nil, err
	}
	return &ThemePackage{
		Manifest:     manifest,
		Theme:        definition,
		Preview:      preview,
		Assets:       assets,
		ContentHash:  contentHash,
		PackageBytes: len(data),
	}, nil
}

// validateThemePackagePath normalizes and validates one archive entry name.
// Only relative ASCII paths with plain segments are accepted, which rules out
// traversal, absolute paths, drive letters, backslashes, NUL bytes, empty
// segments and case-folding surprises in one place.
func validateThemePackagePath(name string) (string, error) {
	if name == "" {
		return "", themeErrorf(ThemeCodeInvalidPath, "", "entry name is empty")
	}
	if len(name) > 255 {
		return "", themeErrorf(ThemeCodeInvalidPath, name, "entry name is too long")
	}
	if !utf8.ValidString(name) {
		return "", themeErrorf(ThemeCodeInvalidPath, name, "entry name is not valid UTF-8")
	}
	if strings.ContainsRune(name, 0) {
		return "", themeErrorf(ThemeCodeInvalidPath, name, "entry name contains a NUL byte")
	}
	if strings.ContainsRune(name, '\\') {
		return "", themeErrorf(ThemeCodeInvalidPath, name, "entry name contains a backslash")
	}
	if strings.HasPrefix(name, "/") {
		return "", themeErrorf(ThemeCodeInvalidPath, name, "absolute entry names are not allowed")
	}
	if strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".") {
		return "", themeErrorf(ThemeCodeInvalidPath, name, "directories and dotted entry names are not allowed")
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '.', c == '-', c == '_', c == '/':
		default:
			return "", themeErrorf(ThemeCodeInvalidPath, name,
				"entry name contains the unsupported character %q", c)
		}
	}
	segments := strings.Split(name, "/")
	for _, segment := range segments {
		if segment == "" {
			return "", themeErrorf(ThemeCodeInvalidPath, name, "entry name has an empty path segment")
		}
		if segment == "." || segment == ".." {
			return "", themeErrorf(ThemeCodeInvalidPath, name, "entry name contains a relative path segment")
		}
	}
	// The character set already excludes ':' so a drive letter cannot appear,
	// but keep the check explicit for reviewers and future char-set changes.
	if len(name) >= 2 && name[1] == ':' {
		return "", themeErrorf(ThemeCodeInvalidPath, name, "entry name contains a drive letter")
	}
	return name, nil
}

// themePackageEntryAllowed reports whether path belongs to the frozen layout.
func themePackageEntryAllowed(path string) bool {
	switch path {
	case themeEntryManifest, themeEntryTheme, themeEntryPreview, themeEntryPreviewJ:
		return true
	}
	if !strings.HasPrefix(path, themeAssetPrefix) || len(path) == len(themeAssetPrefix) {
		return false
	}
	_, ok := themeImageMIMEForPath(path)
	return ok
}

// validateThemeZipEntry checks entry metadata before any inflation happens.
func validateThemeZipEntry(file *zip.File, path string) error {
	if file.Flags&0x1 != 0 {
		return themeErrorf(ThemeCodeEncryptedEntry, path, "encrypted entries are not supported")
	}
	mode := file.Mode()
	if mode&(os.ModeSymlink|os.ModeDevice|os.ModeNamedPipe|os.ModeSocket|os.ModeCharDevice|os.ModeDir) != 0 || mode.IsDir() {
		return themeErrorf(ThemeCodeUnsupportedEntry, path,
			"directories, symbolic links and special files are not allowed")
	}
	switch file.Method {
	case zip.Store, zip.Deflate:
	default:
		return themeErrorf(ThemeCodeUnsupportedCompression, path,
			"compression method %d is not supported", file.Method)
	}
	if file.UncompressedSize64 > ThemeEntryMaxUncompressedBytes {
		return themeErrorf(ThemeCodeEntryTooLarge, path,
			"declared uncompressed size %d exceeds limit %d", file.UncompressedSize64, ThemeEntryMaxUncompressedBytes)
	}
	if file.UncompressedSize64 > 0 {
		if file.CompressedSize64 == 0 {
			return themeErrorf(ThemeCodeCompressionRatio, path,
				"declared uncompressed size %d with zero compressed size", file.UncompressedSize64)
		}
		if file.UncompressedSize64 > uint64(ThemeMaxCompressionRatio)*file.CompressedSize64 {
			return themeErrorf(ThemeCodeCompressionRatio, path,
				"declared compression ratio exceeds %d", ThemeMaxCompressionRatio)
		}
	}
	return nil
}

// readThemeZipEntry inflates one entry through a hard byte limit and verifies
// CRC plus the declared size, so a lying central directory is detected.
func readThemeZipEntry(file *zip.File, path string) ([]byte, error) {
	reader, err := file.Open()
	if err != nil {
		return nil, themeErrorf(ThemeCodeInvalidPackage, path, "cannot open entry: %v", err)
	}
	limit := int64(ThemeEntryMaxUncompressedBytes) + 1
	payload, readErr := io.ReadAll(io.LimitReader(reader, limit))
	closeErr := reader.Close()
	if readErr != nil {
		if errors.Is(readErr, zip.ErrChecksum) || errors.Is(readErr, zip.ErrFormat) ||
			errors.Is(readErr, io.ErrUnexpectedEOF) {
			return nil, themeErrorf(ThemeCodeChecksum, path,
				"entry failed checksum or length validation: %v", readErr)
		}
		return nil, themeErrorf(ThemeCodeInvalidPackage, path, "cannot read entry: %v", readErr)
	}
	if closeErr != nil {
		return nil, themeErrorf(ThemeCodeChecksum, path, "entry failed checksum validation: %v", closeErr)
	}
	if len(payload) > ThemeEntryMaxUncompressedBytes {
		return nil, themeErrorf(ThemeCodeEntryTooLarge, path,
			"entry inflates beyond limit %d", ThemeEntryMaxUncompressedBytes)
	}
	if uint64(len(payload)) != file.UncompressedSize64 {
		return nil, themeErrorf(ThemeCodeChecksum, path,
			"declared size %d does not match %d actual bytes", file.UncompressedSize64, len(payload))
	}
	return payload, nil
}
