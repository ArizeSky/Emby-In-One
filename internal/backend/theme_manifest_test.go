package backend

import (
	"strings"
	"testing"
)

// Strict JSON and field-rule tests for manifest.json and theme.json.

func themeTestManifestRaw(apiVersion, id, name, author, description, version string) string {
	return `{"themeApiVersion":` + apiVersion + `,"id":` + id + `,"name":` + name +
		`,"author":` + author + `,"description":` + description + `,"version":` + version + `}`
}

func themeTestDefinitionJSON(variants, appearance, background string) string {
	out := `{"variants":` + variants
	if appearance != "" {
		out += `,"appearance":` + appearance
	}
	if background != "" {
		out += `,"backgroundImage":` + background
	}
	return out + "}"
}

func themeTestVariantPair(light, dark string) string {
	return `{"light":` + light + `,"dark":` + dark + `}`
}

func TestParseThemeManifestValid(t *testing.T) {
	payload := themeTestManifestJSON("valid-theme")
	manifest, err := parseThemeManifest([]byte(payload))
	if err != nil {
		t.Fatalf("expected valid manifest, got error: %v", err)
	}
	if manifest.ID != "valid-theme" || manifest.Name != "Test Theme" ||
		manifest.Author != "Tester" || manifest.Version != "1.0.0" {
		t.Fatalf("unexpected manifest %#v", manifest)
	}
	if manifest.ThemeAPIVersion != ThemeAPIVersion {
		t.Fatalf("unexpected themeApiVersion %d", manifest.ThemeAPIVersion)
	}

	// Empty author and description are allowed; maximum rune lengths are
	// inclusive.
	maxManifest := themeTestManifestRaw("1", `"`+strings.Repeat("a", ThemeIDMaxLength)+`"`,
		`"`+strings.Repeat("n", ThemeNameMaxLength)+`"`,
		`"`+strings.Repeat("a", ThemeAuthorMaxLength)+`"`,
		`"`+strings.Repeat("d", ThemeDescriptionMaxLength)+`"`,
		`"`+strings.Repeat("v", ThemeVersionMaxLength)+`"`)
	if _, err := parseThemeManifest([]byte(maxManifest)); err != nil {
		t.Fatalf("expected maximum-length manifest to be accepted: %v", err)
	}
	minManifest := themeTestManifestRaw("1", `"abc"`, `"n"`, `""`, `""`, `"1"`)
	if _, err := parseThemeManifest([]byte(minManifest)); err != nil {
		t.Fatalf("expected minimum-length manifest to be accepted: %v", err)
	}
}

func TestParseThemeManifestStrictJSON(t *testing.T) {
	valid := themeTestManifestJSON("valid-theme")
	cases := []struct {
		name    string
		payload string
		want    ThemeErrorCode
	}{
		{"empty", "", ThemeCodeInvalidJSON},
		{"invalid utf8", "\xff\xfe", ThemeCodeInvalidJSON},
		{"top-level array", "[]", ThemeCodeJSONType},
		{"top-level string", `"manifest"`, ThemeCodeJSONType},
		{"top-level null", "null", ThemeCodeJSONType},
		{"trailing object", valid + "{}", ThemeCodeJSONTrailingData},
		{"trailing garbage", valid + "x", ThemeCodeJSONTrailingData},
		{"duplicate id", `{"themeApiVersion":1,"id":"a","name":"n","author":"","description":"","version":"1","id":"b"}`,
			ThemeCodeJSONDuplicateField},
		{"unknown field", strings.TrimSuffix(valid, "}") + `,"homepage":"https://example.invalid"}`,
			ThemeCodeJSONUnknownField},
		{"missing themeApiVersion",
			`{"id":"valid-theme","name":"n","author":"","description":"","version":"1"}`,
			ThemeCodeInvalidManifest},
		{"missing id",
			`{"themeApiVersion":1,"name":"n","author":"","description":"","version":"1"}`,
			ThemeCodeInvalidManifest},
		{"missing name",
			`{"themeApiVersion":1,"id":"valid-theme","author":"","description":"","version":"1"}`,
			ThemeCodeInvalidManifest},
		{"missing author",
			`{"themeApiVersion":1,"id":"valid-theme","name":"n","description":"","version":"1"}`,
			ThemeCodeInvalidManifest},
		{"missing description",
			`{"themeApiVersion":1,"id":"valid-theme","name":"n","author":"","version":"1"}`,
			ThemeCodeInvalidManifest},
		{"missing version",
			`{"themeApiVersion":1,"id":"valid-theme","name":"n","author":"","description":""}`,
			ThemeCodeInvalidManifest},
		{"apiVersion string", themeTestManifestRaw(`"1"`, `"valid-theme"`, `"n"`, `""`, `""`, `"1"`),
			ThemeCodeJSONType},
		{"apiVersion float", themeTestManifestRaw(`1.0`, `"valid-theme"`, `"n"`, `""`, `""`, `"1"`),
			ThemeCodeJSONType},
		{"apiVersion null", themeTestManifestRaw(`null`, `"valid-theme"`, `"n"`, `""`, `""`, `"1"`),
			ThemeCodeJSONType},
		{"apiVersion zero", themeTestManifestRaw(`0`, `"valid-theme"`, `"n"`, `""`, `""`, `"1"`),
			ThemeCodeInvalidManifest},
		{"apiVersion three", themeTestManifestRaw(`3`, `"valid-theme"`, `"n"`, `""`, `""`, `"1"`),
			ThemeCodeInvalidManifest},
		{"id number", themeTestManifestRaw(`1`, `42`, `"n"`, `""`, `""`, `"1"`), ThemeCodeJSONType},
		{"id null", themeTestManifestRaw(`1`, `null`, `"n"`, `""`, `""`, `"1"`), ThemeCodeJSONType},
		{"name null", themeTestManifestRaw(`1`, `"valid-theme"`, `null`, `""`, `""`, `"1"`),
			ThemeCodeJSONType},
		{"description object",
			themeTestManifestRaw(`1`, `"valid-theme"`, `"n"`, `""`, `{}`, `"1"`), ThemeCodeJSONType},
		{"version array",
			themeTestManifestRaw(`1`, `"valid-theme"`, `"n"`, `""`, `""`, `[]`), ThemeCodeJSONType},
		{"name with control character",
			themeTestManifestRaw(`1`, `"valid-theme"`, `"bad\u0007name"`, `""`, `""`, `"1"`),
			ThemeCodeInvalidManifest},
		{"name with bidi override",
			themeTestManifestRaw(`1`, `"valid-theme"`, `"bad\u202ename"`, `""`, `""`, `"1"`),
			ThemeCodeInvalidManifest},
		{"name with padding", themeTestManifestRaw(`1`, `"valid-theme"`, `" n"`, `""`, `""`, `"1"`),
			ThemeCodeInvalidManifest},
	}
	for _, testCase := range cases {
		_, err := parseThemeManifest([]byte(testCase.payload))
		if err == nil {
			t.Fatalf("%s: expected rejection", testCase.name)
		}
		themeTestRequireCode(t, err, testCase.want)
	}
}

func TestValidateThemeID(t *testing.T) {
	valid := []string{"abc", "a1b", "abc-def", "a--b", "012", strings.Repeat("a", ThemeIDMaxLength)}
	for _, id := range valid {
		if err := validateThemeID(id); err != nil {
			t.Fatalf("expected id %q to be valid: %v", id, err)
		}
	}
	invalid := []string{
		"",
		"ab",
		strings.Repeat("a", ThemeIDMaxLength+1),
		"Abc",
		"ABC",
		"abc_def",
		"abc def",
		"-abc",
		"abc-",
		"abc.",
		"builtin",
		"default",
		"admin",
		"web",
		"m\u00fcnchen",
		"abc/def",
	}
	for _, id := range invalid {
		if err := validateThemeID(id); err == nil {
			t.Fatalf("expected id %q to be rejected", id)
		} else {
			themeTestRequireCode(t, err, ThemeCodeInvalidManifest)
		}
	}
}

func TestValidateThemeVersion(t *testing.T) {
	for _, version := range []string{"1", "1.0.0", "v1.0", "1.0.0-rc1", "2024.05.01", strings.Repeat("9", ThemeVersionMaxLength)} {
		if err := validateThemeVersion(version); err != nil {
			t.Fatalf("expected version %q to be valid: %v", version, err)
		}
	}
	for _, version := range []string{
		"",
		strings.Repeat("9", ThemeVersionMaxLength+1),
		"1.0 beta",
		"-1",
		"1-",
		".1",
		"1.",
		"1+2",
		"1_2",
		"v1/2",
		"1.0:beta",
	} {
		if err := validateThemeVersion(version); err == nil {
			t.Fatalf("expected version %q to be rejected", version)
		} else {
			themeTestRequireCode(t, err, ThemeCodeInvalidManifest)
		}
	}
}

func TestValidateThemeTextField(t *testing.T) {
	if err := validateThemeTextField(ThemeCodeInvalidManifest, "name", "Plain", 1, 10); err != nil {
		t.Fatalf("expected plain text to be valid: %v", err)
	}
	if err := validateThemeTextField(ThemeCodeInvalidManifest, "name", "\u4e2d\u6587", 1, 10); err != nil {
		t.Fatalf("expected CJK text to be valid: %v", err)
	}
	if err := validateThemeTextField(ThemeCodeInvalidManifest, "name", "\U0001F600", 1, 1); err != nil {
		t.Fatalf("expected a single emoji rune to be valid: %v", err)
	}
	cases := []struct {
		name  string
		value string
		min   int
		max   int
	}{
		{"empty below minimum", "", 1, 10},
		{"too long", strings.Repeat("a", 11), 1, 10},
		{"control character", "a\x00b", 0, 10},
		{"newline", "a\nb", 0, 10},
		{"bidi formatting", "a\u202eb", 0, 10},
		{"leading whitespace", " a", 0, 10},
		{"trailing whitespace", "a ", 0, 10},
		{"invalid utf8", string([]byte{0xff}), 0, 10},
	}
	for _, testCase := range cases {
		err := validateThemeTextField(ThemeCodeInvalidManifest, "name", testCase.value, testCase.min, testCase.max)
		if err == nil {
			t.Fatalf("%s: expected rejection", testCase.name)
		}
		themeTestRequireCode(t, err, ThemeCodeInvalidManifest)
	}
}

func TestDecodeThemeJSONObjectLimits(t *testing.T) {
	if _, err := decodeThemeJSONObject([]byte(`{"a":1}`), "test.json", 7); err != nil {
		t.Fatalf("expected JSON exactly at the limit: %v", err)
	}
	_, err := decodeThemeJSONObject([]byte(`{"a":1}`), "test.json", 6)
	themeTestRequireCode(t, err, ThemeCodeJSONTooLarge)

	cases := []struct {
		name    string
		payload string
		want    ThemeErrorCode
	}{
		{"duplicate key", `{"a":1,"a":2}`, ThemeCodeJSONDuplicateField},
		{"trailing value", `{"a":1} {}`, ThemeCodeJSONTrailingData},
		{"trailing garbage", `{"a":1}x`, ThemeCodeJSONTrailingData},
		{"unterminated", `{"a":1`, ThemeCodeInvalidJSON},
		{"array root", `[1]`, ThemeCodeJSONType},
		{"invalid utf8", "{\"a\":\"\xff\"}", ThemeCodeInvalidJSON},
	}
	for _, testCase := range cases {
		_, err := decodeThemeJSONObject([]byte(testCase.payload), "test.json", 1024)
		if err == nil {
			t.Fatalf("%s: expected rejection", testCase.name)
		}
		themeTestRequireCode(t, err, testCase.want)
	}
}

func TestParseThemeDefinitionValid(t *testing.T) {
	definition, err := parseThemeDefinition([]byte(themeTestThemeJSON("assets/background.png")))
	if err != nil {
		t.Fatalf("expected valid definition, got error: %v", err)
	}
	if definition.BackgroundImage != "assets/background.png" {
		t.Fatalf("unexpected backgroundImage %q", definition.BackgroundImage)
	}
	if len(definition.Variants) != 2 {
		t.Fatalf("expected light and dark variants, got %d", len(definition.Variants))
	}
	if definition.Appearance.Radius != ThemeAppearanceRadiusDefault ||
		definition.Appearance.Density != ThemeDensityDefault {
		t.Fatalf("unexpected appearance defaults %#v", definition.Appearance)
	}

	// Nested JPEG asset paths and explicit appearance bounds are valid.
	payload := themeTestDefinitionJSON(
		themeTestVariantPair(themeTestLightVariantJSON, themeTestDarkVariantJSON),
		`{"radius":24,"density":"compact"}`,
		`"assets/deep/background.jpg"`,
	)
	definition, err = parseThemeDefinition([]byte(payload))
	if err != nil {
		t.Fatalf("expected valid definition with appearance: %v", err)
	}
	if definition.Appearance.Radius != 24 || definition.Appearance.Density != ThemeDensityCompact {
		t.Fatalf("unexpected appearance %#v", definition.Appearance)
	}
	if definition.BackgroundImage != "assets/deep/background.jpg" {
		t.Fatalf("unexpected backgroundImage %q", definition.BackgroundImage)
	}

	// Empty appearance object falls back to defaults.
	payload = themeTestDefinitionJSON(
		themeTestVariantPair(themeTestLightVariantJSON, themeTestDarkVariantJSON), `{}`, "")
	definition, err = parseThemeDefinition([]byte(payload))
	if err != nil {
		t.Fatalf("expected empty appearance object to be accepted: %v", err)
	}
	if definition.Appearance.Radius != ThemeAppearanceRadiusDefault ||
		definition.Appearance.Density != ThemeDensityDefault {
		t.Fatalf("unexpected appearance defaults %#v", definition.Appearance)
	}

	// An explicitly empty backgroundImage is allowed.
	payload = themeTestDefinitionJSON(
		themeTestVariantPair(themeTestLightVariantJSON, themeTestDarkVariantJSON), "", `""`)
	definition, err = parseThemeDefinition([]byte(payload))
	if err != nil {
		t.Fatalf("expected empty backgroundImage to be accepted: %v", err)
	}
	if definition.BackgroundImage != "" {
		t.Fatalf("expected empty backgroundImage, got %q", definition.BackgroundImage)
	}

	// Lower-case colors are normalized to upper case.
	lower := themeTestVariantPair(themeTestLightVariantReorderedJSON, themeTestDarkVariantReorderedJSON)
	definition, err = parseThemeDefinition([]byte(themeTestDefinitionJSON(lower, "", "")))
	if err != nil {
		t.Fatalf("expected lower-case colors to be accepted: %v", err)
	}
	if definition.Variants[ThemeVariantLight].Bg0 != "#FFFFFF" ||
		definition.Variants[ThemeVariantDark].Accent != "#6EA8FE" {
		t.Fatalf("colors were not normalized: %#v", definition.Variants)
	}
}

func TestParseThemeDefinitionStrictJSON(t *testing.T) {
	variants := themeTestVariantPair(themeTestLightVariantJSON, themeTestDarkVariantJSON)
	cases := []struct {
		name    string
		payload string
		want    ThemeErrorCode
	}{
		{"top-level array", "[]", ThemeCodeJSONType},
		{"top-level null", "null", ThemeCodeJSONType},
		{"empty payload", "", ThemeCodeInvalidJSON},
		{"unknown top-level field",
			`{"variants":` + variants + `,"css":"body{color:red}"}`, ThemeCodeJSONUnknownField},
		{"duplicate variants", `{"variants":` + variants + `,"variants":` + variants + `}`,
			ThemeCodeJSONDuplicateField},
		{"trailing data", `{"variants":` + variants + `}x`, ThemeCodeJSONTrailingData},
		{"missing variants", `{"appearance":{}}`, ThemeCodeInvalidTheme},
		{"variants null", `{"variants":null}`, ThemeCodeJSONType},
		{"variants array", `{"variants":[]}`, ThemeCodeJSONType},
		{"missing dark", `{"variants":{"light":` + themeTestLightVariantJSON + `}}`,
			ThemeCodeInvalidTheme},
		{"missing light", `{"variants":{"dark":` + themeTestDarkVariantJSON + `}}`,
			ThemeCodeInvalidTheme},
		{"extra variant", `{"variants":{"light":` + themeTestLightVariantJSON + `,"dark":` +
			themeTestDarkVariantJSON + `,"sepia":` + themeTestLightVariantJSON + `}}`,
			ThemeCodeJSONUnknownField},
		{"variant missing color", `{"variants":{"light":{"bg0":"#FFFFFF"},"dark":` +
			themeTestDarkVariantJSON + `}}`, ThemeCodeInvalidTheme},
		{"variant extra field", `{"variants":{"light":` +
			strings.Replace(themeTestLightVariantJSON, `"bg0"`, `"shadow":"0 0 1px #000","bg0"`, 1) +
			`,"dark":` + themeTestDarkVariantJSON + `}}`, ThemeCodeJSONUnknownField},
		{"variant duplicate color", `{"variants":{"light":` +
			strings.Replace(themeTestLightVariantJSON, `"bg0":"#FFFFFF"`,
				`"bg0":"#FFFFFF","bg0":"#FFFFFF"`, 1) +
			`,"dark":` + themeTestDarkVariantJSON + `}}`, ThemeCodeJSONDuplicateField},
		{"short hex", `{"variants":{"light":` +
			strings.Replace(themeTestLightVariantJSON, `"bg0":"#FFFFFF"`, `"bg0":"#fff"`, 1) +
			`,"dark":` + themeTestDarkVariantJSON + `}}`, ThemeCodeInvalidColor},
		{"rgba function", `{"variants":{"light":` +
			strings.Replace(themeTestLightVariantJSON, `"bg0":"#FFFFFF"`,
				`"bg0":"rgba(0,0,0,0.4)"`, 1) +
			`,"dark":` + themeTestDarkVariantJSON + `}}`, ThemeCodeInvalidColor},
		{"css variable", `{"variants":{"light":` +
			strings.Replace(themeTestLightVariantJSON, `"bg0":"#FFFFFF"`,
				`"bg0":"var(--bg)"`, 1) +
			`,"dark":` + themeTestDarkVariantJSON + `}}`, ThemeCodeInvalidColor},
		{"color number", `{"variants":{"light":` +
			strings.Replace(themeTestLightVariantJSON, `"bg0":"#FFFFFF"`, `"bg0":123`, 1) +
			`,"dark":` + themeTestDarkVariantJSON + `}}`, ThemeCodeJSONType},
		{"color null", `{"variants":{"light":` +
			strings.Replace(themeTestLightVariantJSON, `"bg0":"#FFFFFF"`, `"bg0":null`, 1) +
			`,"dark":` + themeTestDarkVariantJSON + `}}`, ThemeCodeJSONType},
		{"light text contrast", `{"variants":{"light":` +
			strings.Replace(themeTestLightVariantJSON, `"text1":"#111111"`, `"text1":"#777777"`, 1) +
			`,"dark":` + themeTestDarkVariantJSON + `}}`, ThemeCodeContrast},
		{"light secondary contrast", `{"variants":{"light":` +
			strings.Replace(themeTestLightVariantJSON, `"text2":"#444444"`, `"text2":"#AAAAAA"`, 1) +
			`,"dark":` + themeTestDarkVariantJSON + `}}`, ThemeCodeContrast},
		{"dark text contrast", `{"variants":{"light":` + themeTestLightVariantJSON +
			`,"dark":` + strings.Replace(themeTestDarkVariantJSON, `"text1":"#F2F4F8"`,
			`"text1":"#101010"`, 1) + `}}`, ThemeCodeContrast},
		{"accent link contrast", `{"variants":{"light":` +
			strings.Replace(themeTestLightVariantJSON, `"accentText":"#0A4FB5"`, `"accentText":"#F4F4F4"`, 1) +
			`,"dark":` + themeTestDarkVariantJSON + `}}`, ThemeCodeContrast},
		{"appearance null", `{"variants":` + variants + `,"appearance":null}`, ThemeCodeJSONType},
		{"appearance array", `{"variants":` + variants + `,"appearance":[]}`, ThemeCodeJSONType},
		{"appearance unknown field", `{"variants":` + variants + `,"appearance":{"shadow":1}}`,
			ThemeCodeJSONUnknownField},
		{"appearance radius low", `{"variants":` + variants + `,"appearance":{"radius":3}}`,
			ThemeCodeInvalidTheme},
		{"appearance radius high", `{"variants":` + variants + `,"appearance":{"radius":25}}`,
			ThemeCodeInvalidTheme},
		{"appearance radius float", `{"variants":` + variants + `,"appearance":{"radius":14.5}}`,
			ThemeCodeJSONType},
		{"appearance radius string", `{"variants":` + variants + `,"appearance":{"radius":"14"}}`,
			ThemeCodeJSONType},
		{"appearance radius null", `{"variants":` + variants + `,"appearance":{"radius":null}}`,
			ThemeCodeJSONType},
		{"appearance density unknown", `{"variants":` + variants + `,"appearance":{"density":"cozy"}}`,
			ThemeCodeInvalidTheme},
		{"appearance density empty", `{"variants":` + variants + `,"appearance":{"density":""}}`,
			ThemeCodeInvalidTheme},
		{"appearance density case", `{"variants":` + variants + `,"appearance":{"density":"Compact"}}`,
			ThemeCodeInvalidTheme},
		{"background number", `{"variants":` + variants + `,"backgroundImage":42}`,
			ThemeCodeJSONType},
		{"background null", `{"variants":` + variants + `,"backgroundImage":null}`,
			ThemeCodeJSONType},
		{"background at root", `{"variants":` + variants + `,"backgroundImage":"preview.png"}`,
			ThemeCodeInvalidTheme},
		{"background absolute", `{"variants":` + variants + `,"backgroundImage":"/assets/bg.png"}`,
			ThemeCodeInvalidPath},
		{"background traversal", `{"variants":` + variants + `,"backgroundImage":"../assets/bg.png"}`,
			ThemeCodeInvalidPath},
		{"background dot segment", `{"variants":` + variants + `,"backgroundImage":"assets/./bg.png"}`,
			ThemeCodeInvalidPath},
		{"background backslash", `{"variants":` + variants + `,"backgroundImage":"assets\\bg.png"}`,
			ThemeCodeInvalidPath},
		{"background wrong suffix", `{"variants":` + variants + `,"backgroundImage":"assets/bg.gif"}`,
			ThemeCodeInvalidTheme},
	}
	for _, testCase := range cases {
		_, err := parseThemeDefinition([]byte(testCase.payload))
		if err == nil {
			t.Fatalf("%s: expected rejection", testCase.name)
		}
		themeTestRequireCode(t, err, testCase.want)
	}
}

func TestThemeVariantContrastBoundaries(t *testing.T) {
	// 4.5:1 is the primary text threshold. #757575 passes on white, #7A7A7A
	// does not, measured against both bg0 and bg1.
	passLight := strings.Replace(themeTestLightVariantJSON, `"bg1":"#F5F5F5"`, `"bg1":"#FFFFFF"`, 1)
	passLight = strings.Replace(passLight, `"text1":"#111111"`, `"text1":"#757575"`, 1)
	payload := themeTestDefinitionJSON(
		themeTestVariantPair(passLight, themeTestDarkVariantJSON), "", "")
	if _, err := parseThemeDefinition([]byte(payload)); err != nil {
		t.Fatalf("expected 4.6:1 primary text to be accepted: %v", err)
	}

	failLight := strings.Replace(passLight, `"text1":"#757575"`, `"text1":"#7A7A7A"`, 1)
	payload = themeTestDefinitionJSON(
		themeTestVariantPair(failLight, themeTestDarkVariantJSON), "", "")
	_, err := parseThemeDefinition([]byte(payload))
	themeTestRequireCode(t, err, ThemeCodeContrast)
}
