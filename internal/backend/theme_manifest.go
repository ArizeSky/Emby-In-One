package backend

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Theme package schema (theme API v1).
//
// manifest.json declares package identity, theme.json declares the controlled
// appearance tokens, and PNG/JPEG entries under assets/ (plus an optional
// preview.png/preview.jpg) are the only binary resources. Everything else -
// CSS, HTML, JavaScript, SVG, fonts, remote URLs - is rejected. The parsed
// package is immutable and carries the exact bytes that were validated, so the
// store can persist them without re-parsing user input.

const (
	// ThemeAPIVersion is the only accepted themeApiVersion value.
	ThemeAPIVersion = 1

	// ThemePackageMaxBytes bounds the uploaded ZIP itself ("net package").
	ThemePackageMaxBytes = 1536 * 1024
	// ThemePackageMaxEntries bounds the number of archive entries.
	ThemePackageMaxEntries = 32
	// ThemeEntryMaxUncompressedBytes bounds one inflated entry.
	ThemeEntryMaxUncompressedBytes = 4 << 20
	// ThemePackageMaxUncompressedBytes bounds all inflated entries together.
	ThemePackageMaxUncompressedBytes = 8 << 20
	// ThemeJSONMaxBytes bounds manifest.json and theme.json individually.
	ThemeJSONMaxBytes = 64 << 10
	// ThemeMaxImages bounds preview plus asset images per package.
	ThemeMaxImages = 3
	// ThemeImageMaxDimension bounds image width and height.
	ThemeImageMaxDimension = 4096
	// ThemeImageMaxPixels bounds a single image.
	ThemeImageMaxPixels = 4000000
	// ThemePackageMaxPixels bounds all images of a package together.
	ThemePackageMaxPixels = 8000000
	// ThemeMaxCompressionRatio bounds uncompressed/compressed per entry.
	ThemeMaxCompressionRatio = 100

	// Manifest field length limits, counted in runes where noted.
	ThemeIDMinLength          = 3
	ThemeIDMaxLength          = 48
	ThemeNameMaxLength        = 80
	ThemeAuthorMaxLength      = 80
	ThemeDescriptionMaxLength = 400
	ThemeVersionMaxLength     = 32
)

// ThemeErrorCode classifies every theme package rejection so API handlers can
// map failures to 400/413/415 without string matching.
type ThemeErrorCode string

const (
	// ThemeCodeInvalidPackage covers unreadable archives and internal failures.
	ThemeCodeInvalidPackage ThemeErrorCode = "invalid_package"
	// ThemeCodePackageTooLarge means the ZIP exceeds ThemePackageMaxBytes.
	ThemeCodePackageTooLarge ThemeErrorCode = "package_too_large"
	// ThemeCodeTooManyEntries means the archive has more than
	// ThemePackageMaxEntries entries.
	ThemeCodeTooManyEntries ThemeErrorCode = "too_many_entries"
	// ThemeCodeEntryTooLarge means one entry exceeds
	// ThemeEntryMaxUncompressedBytes.
	ThemeCodeEntryTooLarge ThemeErrorCode = "entry_too_large"
	// ThemeCodeUncompressedBudget means all entries together exceed
	// ThemePackageMaxUncompressedBytes.
	ThemeCodeUncompressedBudget ThemeErrorCode = "uncompressed_budget_exceeded"
	// ThemeCodeCompressionRatio means an entry exceeds
	// ThemeMaxCompressionRatio.
	ThemeCodeCompressionRatio ThemeErrorCode = "compression_ratio_exceeded"
	// ThemeCodeInvalidPath means an entry name is unsafe (traversal, absolute,
	// NUL, backslash, drive letter, non-ASCII, empty segment).
	ThemeCodeInvalidPath ThemeErrorCode = "invalid_path"
	// ThemeCodePathConflict means duplicate or case-colliding entry names.
	ThemeCodePathConflict ThemeErrorCode = "path_conflict"
	// ThemeCodeUnsupportedEntry covers directories, links, special files and
	// any name outside the frozen package layout.
	ThemeCodeUnsupportedEntry ThemeErrorCode = "unsupported_entry"
	// ThemeCodeUnsupportedCompression means the entry is not stored/deflated.
	ThemeCodeUnsupportedCompression ThemeErrorCode = "unsupported_compression"
	// ThemeCodeEncryptedEntry means the ZIP encryption flag is set.
	ThemeCodeEncryptedEntry ThemeErrorCode = "encrypted_entry"
	// ThemeCodeChecksum means CRC or declared-size validation failed, i.e. the
	// ZIP headers lied or the payload is corrupt.
	ThemeCodeChecksum ThemeErrorCode = "checksum_mismatch"
	// ThemeCodeUnexpectedEntry means the entry name is not part of the format.
	ThemeCodeUnexpectedEntry ThemeErrorCode = "unexpected_entry"
	// ThemeCodeMissingEntry means manifest.json, theme.json or a referenced
	// asset is absent.
	ThemeCodeMissingEntry ThemeErrorCode = "missing_entry"
	// ThemeCodeMissingAsset means theme.json references an image that is not
	// packaged.
	ThemeCodeMissingAsset ThemeErrorCode = "missing_asset"
	// ThemeCodeUnreferencedAsset means a packaged image is not referenced by
	// theme.json. The conservative rule is to reject such packages so an
	// upload cannot be used as anonymous image hosting.
	ThemeCodeUnreferencedAsset ThemeErrorCode = "unreferenced_asset"
	// ThemeCodeJSONTooLarge means a JSON file exceeds ThemeJSONMaxBytes.
	ThemeCodeJSONTooLarge ThemeErrorCode = "json_too_large"
	// ThemeCodeInvalidJSON means the payload is not valid JSON.
	ThemeCodeInvalidJSON ThemeErrorCode = "invalid_json"
	// ThemeCodeJSONType means a field has the wrong JSON type or is null.
	ThemeCodeJSONType ThemeErrorCode = "json_type"
	// ThemeCodeJSONUnknownField means an undeclared field is present.
	ThemeCodeJSONUnknownField ThemeErrorCode = "json_unknown_field"
	// ThemeCodeJSONDuplicateField means a key appears twice in one object.
	ThemeCodeJSONDuplicateField ThemeErrorCode = "json_duplicate_field"
	// ThemeCodeJSONTrailingData means data follows the top-level object.
	ThemeCodeJSONTrailingData ThemeErrorCode = "json_trailing_data"
	// ThemeCodeInvalidManifest covers manifest schema/value violations.
	ThemeCodeInvalidManifest ThemeErrorCode = "invalid_manifest"
	// ThemeCodeInvalidTheme covers theme.json schema/value violations.
	ThemeCodeInvalidTheme ThemeErrorCode = "invalid_theme"
	// ThemeCodeInvalidColor means a token is not a literal #RRGGBB value.
	ThemeCodeInvalidColor ThemeErrorCode = "invalid_color"
	// ThemeCodeContrast means the variant fails the contrast contract.
	ThemeCodeContrast ThemeErrorCode = "contrast_insufficient"
	// ThemeCodeInvalidImage means the image is truncated, undecodable, or its
	// extension does not match its content.
	ThemeCodeInvalidImage ThemeErrorCode = "invalid_image"
	// ThemeCodeImageTooLarge means one image exceeds the dimension/pixel caps.
	ThemeCodeImageTooLarge ThemeErrorCode = "image_too_large"
	// ThemeCodeImageBudget means the package has too many images or the total
	// pixel budget is exceeded.
	ThemeCodeImageBudget ThemeErrorCode = "image_budget_exceeded"
)

// ThemePackageError is the typed rejection returned by ParseThemePackage and
// its helpers. Path is the archive entry or JSON file involved, when known.
type ThemePackageError struct {
	Code   ThemeErrorCode
	Path   string
	Detail string
}

func (e *ThemePackageError) Error() string {
	if e.Path != "" {
		return fmt.Sprintf("theme package %s (%s): %s", e.Code, e.Path, e.Detail)
	}
	return fmt.Sprintf("theme package %s: %s", e.Code, e.Detail)
}

// ThemePackageErrorCode returns the classification of err, or "" when err is
// not a theme package error.
func ThemePackageErrorCode(err error) ThemeErrorCode {
	var packageErr *ThemePackageError
	if errors.As(err, &packageErr) {
		return packageErr.Code
	}
	return ""
}

func themeErrorf(code ThemeErrorCode, path, format string, args ...any) error {
	return &ThemePackageError{Code: code, Path: path, Detail: fmt.Sprintf(format, args...)}
}

// ThemeManifest is the validated manifest.json of a theme package.
type ThemeManifest struct {
	ThemeAPIVersion int    `json:"themeApiVersion"`
	ID              string `json:"id"`
	Name            string `json:"name"`
	Author          string `json:"author"`
	Description     string `json:"description"`
	Version         string `json:"version"`
}

// ThemeVariant holds the 11 frozen #RRGGBB tokens of one variant. Values are
// normalized to upper case by the parser.
type ThemeVariant struct {
	Bg0        string `json:"bg0"`
	Bg1        string `json:"bg1"`
	Bg2        string `json:"bg2"`
	Bg3        string `json:"bg3"`
	Text1      string `json:"text1"`
	Text2      string `json:"text2"`
	Text3      string `json:"text3"`
	Line       string `json:"line"`
	Line2      string `json:"line2"`
	Accent     string `json:"accent"`
	AccentText string `json:"accentText"`
}

// ThemeAppearance is the controlled layout hint set. Defaults are applied by
// the parser, so the zero value never reaches consumers.
type ThemeAppearance struct {
	Radius  int    `json:"radius"`
	Density string `json:"density"`
}

// ThemeDefinition is the validated theme.json. Variants always contains both
// ThemeVariantLight and ThemeVariantDark. BackgroundImage is empty when the
// package ships no background asset, otherwise it is a package-relative
// "assets/..." path that is guaranteed to exist in Assets.
type ThemeDefinition struct {
	Variants        map[string]ThemeVariant `json:"variants"`
	Appearance      ThemeAppearance         `json:"appearance"`
	BackgroundImage string                  `json:"backgroundImage"`
}

// ThemeAsset is one validated binary resource. MIME is the content-derived
// fixed MIME type (never sniffed at serve time), SHA256 is the hex digest of
// the exact bytes in Data, and Data aliases the validated archive payload; it
// must be treated as immutable.
type ThemeAsset struct {
	Path   string `json:"path"`
	MIME   string `json:"mime"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
	Data   []byte `json:"-"`
}

// ThemePackage is the frozen result of ParseThemePackage. Assets is sorted by
// path and excludes Preview; exactly one of Preview/Assets entries may be
// referenced by Theme.BackgroundImage, and every asset is referenced.
//
// ContentHash is the sha256 of the canonical manifest/theme/asset descriptor
// and is the only identity the store may use for immutable resource paths.
type ThemePackage struct {
	Manifest     ThemeManifest   `json:"manifest"`
	Theme        ThemeDefinition `json:"theme"`
	Preview      *ThemeAsset     `json:"preview,omitempty"`
	Assets       []ThemeAsset    `json:"assets"`
	ContentHash  string          `json:"contentHash"`
	PackageBytes int             `json:"packageBytes"`
}

// Variant returns the named validated variant.
func (p *ThemePackage) Variant(name string) (ThemeVariant, bool) {
	if p == nil || p.Theme.Variants == nil {
		return ThemeVariant{}, false
	}
	variant, ok := p.Theme.Variants[name]
	return variant, ok
}

// AssetByPath returns the referenced background asset by package path.
func (p *ThemePackage) AssetByPath(path string) (ThemeAsset, bool) {
	if p == nil || path == "" {
		return ThemeAsset{}, false
	}
	for _, asset := range p.Assets {
		if asset.Path == path {
			return asset, true
		}
	}
	return ThemeAsset{}, false
}

// AllAssets returns the deterministic asset list: background assets sorted by
// path, then the optional preview image.
func (p *ThemePackage) AllAssets() []ThemeAsset {
	if p == nil {
		return nil
	}
	out := make([]ThemeAsset, 0, len(p.Assets)+1)
	out = append(out, p.Assets...)
	if p.Preview != nil {
		out = append(out, *p.Preview)
	}
	return out
}

// JSON field sets. Unknown fields are rejected against exactly these lists.
var (
	themeManifestFields   = []string{"themeApiVersion", "id", "name", "author", "description", "version"}
	themeDefinitionFields = []string{"variants", "appearance", "backgroundImage"}
	themeAppearanceFields = []string{"radius", "density"}
	themeVariantNames     = []string{ThemeVariantLight, ThemeVariantDark}
)

// decodeThemeJSONObject parses exactly one JSON object, rejecting duplicate
// keys, non-object roots and trailing data. The payload must already be bounded
// by the caller, but the limit is re-checked here.
func decodeThemeJSONObject(data []byte, path string, maxBytes int) (map[string]json.RawMessage, error) {
	if len(data) > maxBytes {
		return nil, themeErrorf(ThemeCodeJSONTooLarge, path, "JSON is %d bytes, limit %d", len(data), maxBytes)
	}
	if !utf8.Valid(data) {
		return nil, themeErrorf(ThemeCodeInvalidJSON, path, "JSON is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return nil, themeErrorf(ThemeCodeInvalidJSON, path, "invalid JSON: %v", err)
	}
	if delim, ok := token.(json.Delim); !ok || delim != '{' {
		return nil, themeErrorf(ThemeCodeJSONType, path, "top-level value must be a JSON object")
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, themeErrorf(ThemeCodeInvalidJSON, path, "invalid JSON object: %v", err)
		}
		key, ok := keyToken.(string)
		if !ok {
			return nil, themeErrorf(ThemeCodeInvalidJSON, path, "invalid JSON object key")
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, themeErrorf(ThemeCodeJSONDuplicateField, path, "duplicate field %q", key)
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, themeErrorf(ThemeCodeInvalidJSON, path, "invalid JSON value for %q: %v", key, err)
		}
		fields[key] = raw
	}
	if _, err := decoder.Token(); err != nil {
		return nil, themeErrorf(ThemeCodeInvalidJSON, path, "unterminated JSON object: %v", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, themeErrorf(ThemeCodeJSONTrailingData, path, "unexpected trailing JSON value")
		}
		return nil, themeErrorf(ThemeCodeJSONTrailingData, path, "unexpected trailing data: %v", err)
	}
	return fields, nil
}

func rejectThemeJSONUnknownFields(fields map[string]json.RawMessage, allowed []string, path string) error {
	permitted := make(map[string]struct{}, len(allowed))
	for _, name := range allowed {
		permitted[name] = struct{}{}
	}
	unknown := make([]string, 0, len(fields))
	for name := range fields {
		if _, ok := permitted[name]; !ok {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return themeErrorf(ThemeCodeJSONUnknownField, path, "unknown field %q", unknown[0])
	}
	return nil
}

func themeJSONRawIsNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func themeJSONRawString(raw json.RawMessage, name, path string) (string, error) {
	if themeJSONRawIsNull(raw) {
		return "", themeErrorf(ThemeCodeJSONType, path, "%s must be a string", name)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", themeErrorf(ThemeCodeJSONType, path, "%s must be a string", name)
	}
	return value, nil
}

func themeJSONRawInt(raw json.RawMessage, name, path string) (int, error) {
	if themeJSONRawIsNull(raw) {
		return 0, themeErrorf(ThemeCodeJSONType, path, "%s must be an integer", name)
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, themeErrorf(ThemeCodeJSONType, path, "%s must be an integer", name)
	}
	return value, nil
}

func requireThemeJSONField(fields map[string]json.RawMessage, key, path string, code ThemeErrorCode) (json.RawMessage, error) {
	raw, ok := fields[key]
	if !ok {
		return nil, themeErrorf(code, path, "missing required field %q", key)
	}
	return raw, nil
}

// parseThemeManifest validates manifest.json.
func parseThemeManifest(data []byte) (ThemeManifest, error) {
	const path = "manifest.json"
	fields, err := decodeThemeJSONObject(data, path, ThemeJSONMaxBytes)
	if err != nil {
		return ThemeManifest{}, err
	}
	if err := rejectThemeJSONUnknownFields(fields, themeManifestFields, path); err != nil {
		return ThemeManifest{}, err
	}
	rawVersion, err := requireThemeJSONField(fields, "themeApiVersion", path, ThemeCodeInvalidManifest)
	if err != nil {
		return ThemeManifest{}, err
	}
	apiVersion, err := themeJSONRawInt(rawVersion, "themeApiVersion", path)
	if err != nil {
		return ThemeManifest{}, err
	}
	if apiVersion != ThemeAPIVersion {
		return ThemeManifest{}, themeErrorf(ThemeCodeInvalidManifest, path,
			"unsupported themeApiVersion %d, supported version is %d", apiVersion, ThemeAPIVersion)
	}
	manifest := ThemeManifest{ThemeAPIVersion: apiVersion}
	textFields := []struct {
		key   string
		value *string
	}{
		{"id", &manifest.ID},
		{"name", &manifest.Name},
		{"author", &manifest.Author},
		{"description", &manifest.Description},
		{"version", &manifest.Version},
	}
	for _, field := range textFields {
		raw, err := requireThemeJSONField(fields, field.key, path, ThemeCodeInvalidManifest)
		if err != nil {
			return ThemeManifest{}, err
		}
		value, err := themeJSONRawString(raw, field.key, path)
		if err != nil {
			return ThemeManifest{}, err
		}
		*field.value = value
	}
	if err := validateThemeManifest(manifest); err != nil {
		return ThemeManifest{}, err
	}
	return manifest, nil
}

func validateThemeManifest(manifest ThemeManifest) error {
	const path = "manifest.json"
	if manifest.ThemeAPIVersion != ThemeAPIVersion {
		return themeErrorf(ThemeCodeInvalidManifest, path, "unsupported themeApiVersion %d", manifest.ThemeAPIVersion)
	}
	if err := validateThemeID(manifest.ID); err != nil {
		return err
	}
	if err := validateThemeTextField(ThemeCodeInvalidManifest, "name", manifest.Name, 1, ThemeNameMaxLength); err != nil {
		return err
	}
	if err := validateThemeTextField(ThemeCodeInvalidManifest, "author", manifest.Author, 0, ThemeAuthorMaxLength); err != nil {
		return err
	}
	if err := validateThemeTextField(ThemeCodeInvalidManifest, "description", manifest.Description, 0, ThemeDescriptionMaxLength); err != nil {
		return err
	}
	return validateThemeVersion(manifest.Version)
}

func validateThemeID(id string) error {
	const path = "manifest.json"
	if len(id) < ThemeIDMinLength || len(id) > ThemeIDMaxLength {
		return themeErrorf(ThemeCodeInvalidManifest, path,
			"id must be %d..%d characters", ThemeIDMinLength, ThemeIDMaxLength)
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case c == '-':
			if i == 0 || i == len(id)-1 {
				return themeErrorf(ThemeCodeInvalidManifest, path, "id must start and end with a letter or digit")
			}
		default:
			return themeErrorf(ThemeCodeInvalidManifest, path,
				"id may only contain lowercase letters, digits and '-', found %q", c)
		}
	}
	if _, reserved := themeReservedIDs[id]; reserved {
		return themeErrorf(ThemeCodeInvalidManifest, path, "id %q is reserved", id)
	}
	return nil
}

func validateThemeVersion(version string) error {
	const path = "manifest.json"
	if len(version) < 1 || len(version) > ThemeVersionMaxLength {
		return themeErrorf(ThemeCodeInvalidManifest, path, "version must be 1..%d characters", ThemeVersionMaxLength)
	}
	for i := 0; i < len(version); i++ {
		c := version[i]
		alnum := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !alnum && c != '.' && c != '-' {
			return themeErrorf(ThemeCodeInvalidManifest, path,
				"version may only contain letters, digits, '.' and '-', found %q", c)
		}
		if i == 0 || i == len(version)-1 {
			if !alnum {
				return themeErrorf(ThemeCodeInvalidManifest, path, "version must start and end with a letter or digit")
			}
		}
	}
	return nil
}

// validateThemeTextField enforces UTF-8, printable text (no control or bidi
// formatting characters) and rune-length bounds. It is intentionally strict:
// these strings are rendered as plain text in the admin UI and the web client.
func validateThemeTextField(code ThemeErrorCode, name, value string, minRunes, maxRunes int) error {
	const path = "manifest.json"
	if !utf8.ValidString(value) {
		return themeErrorf(code, path, "%s must be valid UTF-8", name)
	}
	if value != strings.TrimSpace(value) {
		return themeErrorf(code, path, "%s must not have leading or trailing whitespace", name)
	}
	length := utf8.RuneCountInString(value)
	if length < minRunes || length > maxRunes {
		return themeErrorf(code, path, "%s must be %d..%d characters", name, minRunes, maxRunes)
	}
	for _, r := range value {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return themeErrorf(code, path, "%s must not contain control or formatting characters", name)
		}
	}
	return nil
}

// parseThemeDefinition validates theme.json.
func parseThemeDefinition(data []byte) (ThemeDefinition, error) {
	const path = "theme.json"
	fields, err := decodeThemeJSONObject(data, path, ThemeJSONMaxBytes)
	if err != nil {
		return ThemeDefinition{}, err
	}
	if err := rejectThemeJSONUnknownFields(fields, themeDefinitionFields, path); err != nil {
		return ThemeDefinition{}, err
	}
	rawVariants, err := requireThemeJSONField(fields, "variants", path, ThemeCodeInvalidTheme)
	if err != nil {
		return ThemeDefinition{}, err
	}
	variants, err := parseThemeVariants(rawVariants)
	if err != nil {
		return ThemeDefinition{}, err
	}
	appearance := ThemeAppearance{Radius: ThemeAppearanceRadiusDefault, Density: ThemeDensityDefault}
	if raw, ok := fields["appearance"]; ok {
		appearance, err = parseThemeAppearance(raw)
		if err != nil {
			return ThemeDefinition{}, err
		}
	}
	backgroundImage := ""
	if raw, ok := fields["backgroundImage"]; ok {
		backgroundImage, err = themeJSONRawString(raw, "backgroundImage", path)
		if err != nil {
			return ThemeDefinition{}, err
		}
		if backgroundImage != "" {
			cleaned, err := validateThemePackagePath(backgroundImage)
			if err != nil {
				return ThemeDefinition{}, underlyingThemePathError(err, path, "backgroundImage")
			}
			if cleaned != backgroundImage || !strings.HasPrefix(cleaned, "assets/") {
				return ThemeDefinition{}, themeErrorf(ThemeCodeInvalidTheme, path,
					"backgroundImage must be a normalized path under assets/")
			}
			if _, ok := themeImageMIMEForPath(cleaned); !ok {
				return ThemeDefinition{}, themeErrorf(ThemeCodeInvalidTheme, path,
					"backgroundImage must be a .png, .jpg or .jpeg asset")
			}
		}
	}
	return ThemeDefinition{Variants: variants, Appearance: appearance, BackgroundImage: backgroundImage}, nil
}

// underlyingThemePathError rewraps a path rejection with the JSON context while
// keeping the original error code.
func underlyingThemePathError(err error, path, field string) error {
	code := ThemePackageErrorCode(err)
	if code == "" {
		code = ThemeCodeInvalidTheme
	}
	var packageErr *ThemePackageError
	detail := err.Error()
	if errors.As(err, &packageErr) {
		detail = packageErr.Detail
	}
	return themeErrorf(code, path, "%s: %s", field, detail)
}

func parseThemeVariants(raw json.RawMessage) (map[string]ThemeVariant, error) {
	const path = "theme.json"
	fields, err := decodeThemeJSONObject(raw, path, ThemeJSONMaxBytes)
	if err != nil {
		return nil, err
	}
	if err := rejectThemeJSONUnknownFields(fields, themeVariantNames, path); err != nil {
		return nil, err
	}
	variants := make(map[string]ThemeVariant, len(themeVariantNames))
	for _, name := range themeVariantNames {
		rawVariant, ok := fields[name]
		if !ok {
			return nil, themeErrorf(ThemeCodeInvalidTheme, path, "variants.%s is required", name)
		}
		variant, err := parseThemeVariant(rawVariant, name)
		if err != nil {
			return nil, err
		}
		variants[name] = variant
	}
	return variants, nil
}

func parseThemeVariant(raw json.RawMessage, variantName string) (ThemeVariant, error) {
	const path = "theme.json"
	fields, err := decodeThemeJSONObject(raw, path, ThemeJSONMaxBytes)
	if err != nil {
		return ThemeVariant{}, err
	}
	if err := rejectThemeJSONUnknownFields(fields, themeVariantColorFields, path); err != nil {
		return ThemeVariant{}, err
	}
	colors := make(map[string]string, len(themeVariantColorFields))
	for _, name := range themeVariantColorFields {
		rawColor, ok := fields[name]
		if !ok {
			return ThemeVariant{}, themeErrorf(ThemeCodeInvalidTheme, path,
				"variants.%s.%s is required", variantName, name)
		}
		value, err := themeJSONRawString(rawColor, "variants."+variantName+"."+name, path)
		if err != nil {
			return ThemeVariant{}, err
		}
		normalized, err := normalizeThemeColor(value)
		if err != nil {
			return ThemeVariant{}, themeErrorf(ThemeCodeInvalidColor, path,
				"variants.%s.%s: %s", variantName, name, err.Error())
		}
		colors[name] = normalized
	}
	variant := ThemeVariant{
		Bg0:        colors["bg0"],
		Bg1:        colors["bg1"],
		Bg2:        colors["bg2"],
		Bg3:        colors["bg3"],
		Text1:      colors["text1"],
		Text2:      colors["text2"],
		Text3:      colors["text3"],
		Line:       colors["line"],
		Line2:      colors["line2"],
		Accent:     colors["accent"],
		AccentText: colors["accentText"],
	}
	if err := checkThemeVariantContrast(variantName, variant); err != nil {
		return ThemeVariant{}, err
	}
	return variant, nil
}

func parseThemeAppearance(raw json.RawMessage) (ThemeAppearance, error) {
	const path = "theme.json"
	fields, err := decodeThemeJSONObject(raw, path, ThemeJSONMaxBytes)
	if err != nil {
		return ThemeAppearance{}, err
	}
	if err := rejectThemeJSONUnknownFields(fields, themeAppearanceFields, path); err != nil {
		return ThemeAppearance{}, err
	}
	appearance := ThemeAppearance{Radius: ThemeAppearanceRadiusDefault, Density: ThemeDensityDefault}
	if rawRadius, ok := fields["radius"]; ok {
		radius, err := themeJSONRawInt(rawRadius, "appearance.radius", path)
		if err != nil {
			return ThemeAppearance{}, err
		}
		if radius < ThemeAppearanceRadiusMin || radius > ThemeAppearanceRadiusMax {
			return ThemeAppearance{}, themeErrorf(ThemeCodeInvalidTheme, path,
				"appearance.radius must be %d..%d", ThemeAppearanceRadiusMin, ThemeAppearanceRadiusMax)
		}
		appearance.Radius = radius
	}
	if rawDensity, ok := fields["density"]; ok {
		density, err := themeJSONRawString(rawDensity, "appearance.density", path)
		if err != nil {
			return ThemeAppearance{}, err
		}
		if !validThemeDensity(density) {
			return ThemeAppearance{}, themeErrorf(ThemeCodeInvalidTheme, path,
				"appearance.density must be %q or %q", ThemeDensityCompact, ThemeDensityComfortable)
		}
		appearance.Density = density
	}
	return appearance, nil
}

// themePackageFingerprint is the canonical descriptor that is hashed into
// ThemePackage.ContentHash. Field order is frozen; map values are encoded by
// encoding/json in sorted key order, so the digest is independent of archive
// order, JSON key order and color letter casing.
type themePackageFingerprint struct {
	Canonical string                  `json:"canonical"`
	Manifest  ThemeManifest           `json:"manifest"`
	Theme     ThemeDefinition         `json:"theme"`
	Preview   *themeAssetFingerprint  `json:"preview"`
	Assets    []themeAssetFingerprint `json:"assets"`
}

type themeAssetFingerprint struct {
	Path   string `json:"path"`
	MIME   string `json:"mime"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`
}

func themeAssetFingerprintOf(asset ThemeAsset) themeAssetFingerprint {
	return themeAssetFingerprint{
		Path:   asset.Path,
		MIME:   asset.MIME,
		Width:  asset.Width,
		Height: asset.Height,
		Bytes:  asset.Bytes,
		SHA256: asset.SHA256,
	}
}

func themePackageContentHash(manifest ThemeManifest, definition ThemeDefinition, preview *ThemeAsset, assets []ThemeAsset) (string, error) {
	fingerprint := themePackageFingerprint{
		Canonical: "eio-web-theme-package/1",
		Manifest:  manifest,
		Theme:     definition,
		Assets:    make([]themeAssetFingerprint, 0, len(assets)),
	}
	if preview != nil {
		value := themeAssetFingerprintOf(*preview)
		fingerprint.Preview = &value
	}
	for _, asset := range assets {
		fingerprint.Assets = append(fingerprint.Assets, themeAssetFingerprintOf(asset))
	}
	encoded, err := json.Marshal(fingerprint)
	if err != nil {
		return "", themeErrorf(ThemeCodeInvalidPackage, "", "canonical theme encoding failed: %v", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
