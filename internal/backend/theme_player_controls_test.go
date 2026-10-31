package backend

import (
	"encoding/json"
	"strings"
	"testing"
)

// Unit tests for the theme API v2 playerControls block: strict parsing,
// canonical lower-case colors, contrast rules, render-time defaults and deep
// copies.

func themeTestPlayerControlsJSON(played, buffered, track, thumb string) string {
	return `{"played":"` + played + `","buffered":"` + buffered + `","track":"` + track +
		`","thumb":"` + thumb + `"}`
}

// themeTestPlayerControlsDefaultJSON is the documented default palette.
func themeTestPlayerControlsDefaultJSON() string {
	return themeTestPlayerControlsJSON(ThemePlayerDefaultPlayed, ThemePlayerDefaultBuffered,
		ThemePlayerDefaultTrack, ThemePlayerDefaultThumb)
}

func themeTestPlayerControls(t *testing.T, payload string) *ThemePlayerControls {
	t.Helper()
	controls, err := parseThemePlayerControls(json.RawMessage(payload), "theme.json")
	if err != nil {
		t.Fatalf("parseThemePlayerControls(%s): %v", payload, err)
	}
	return controls
}

func TestParseThemePlayerControlsValidNormalizesLowercase(t *testing.T) {
	controls, err := parseThemePlayerControls(
		json.RawMessage(themeTestPlayerControlsJSON("#3B82F6", "#F3F4F6", "#111827", "#3B82F6")),
		"theme.json")
	if err != nil {
		t.Fatalf("parseThemePlayerControls: %v", err)
	}
	played, buffered, track, thumb, ok := themePlayerControlValues(controls)
	if !ok {
		t.Fatal("parsed block is incomplete")
	}
	want := [4]string{ThemePlayerDefaultPlayed, ThemePlayerDefaultBuffered,
		ThemePlayerDefaultTrack, ThemePlayerDefaultThumb}
	got := [4]string{played, buffered, track, thumb}
	if got != want {
		t.Fatalf("normalized colors = %v, want %v", got, want)
	}

	// Re-marshalling a v2 definition must produce canonical lower-case values
	// and never inject defaults for an absent block.
	encoded, err := json.Marshal(ThemeDefinition{
		Variants:       map[string]ThemeVariant{ThemeVariantLight: {}, ThemeVariantDark: {}},
		Appearance:     ThemeAppearance{Radius: ThemeAppearanceRadiusDefault, Density: ThemeDensityDefault},
		PlayerControls: controls,
	})
	if err != nil {
		t.Fatalf("marshal definition: %v", err)
	}
	if !strings.Contains(string(encoded), `"playerControls":{"played":"#3b82f6"`) {
		t.Fatalf("encoded definition lacks canonical playerControls: %s", encoded)
	}
	v1Encoded, err := json.Marshal(ThemeDefinition{Variants: map[string]ThemeVariant{}})
	if err != nil {
		t.Fatalf("marshal v1 definition: %v", err)
	}
	if strings.Contains(string(v1Encoded), "playerControls") {
		t.Fatalf("nil playerControls leaked into JSON: %s", v1Encoded)
	}
}

func TestParseThemePlayerControlsRejectsMalformedBlocks(t *testing.T) {
	defaults := themeTestPlayerControlsDefaultJSON()
	cases := []struct {
		name    string
		payload string
		want    ThemeErrorCode
	}{
		{"null block", `null`, ThemeCodeJSONType},
		{"empty object", `{}`, ThemeCodeInvalidTheme},
		{"missing played", `{"buffered":"#f3f4f6","track":"#111827","thumb":"#3b82f6"}`, ThemeCodeInvalidTheme},
		{"missing thumb", `{"played":"#3b82f6","buffered":"#f3f4f6","track":"#111827"}`, ThemeCodeInvalidTheme},
		{"unknown field", strings.Replace(defaults, `"played"`, `"hover":"#3b82f6","played"`, 1),
			ThemeCodeJSONUnknownField},
		{"duplicate field", strings.Replace(defaults, `"played"`, `"played":"#3b82f6","played"`, 1),
			ThemeCodeJSONDuplicateField},
		{"null field", strings.Replace(defaults, `"#3b82f6"`, `null`, 1), ThemeCodeJSONType},
		{"number field", strings.Replace(defaults, `"#3b82f6"`, `16512`, 1), ThemeCodeJSONType},
		{"short hex", strings.Replace(defaults, `"#3b82f6"`, `"#3b8"`, 1), ThemeCodeInvalidColor},
		{"alpha hex", strings.Replace(defaults, `"#3b82f6"`, `"#3b82f6ff"`, 1), ThemeCodeInvalidColor},
		{"rgb function", strings.Replace(defaults, `"#3b82f6"`, `"rgb(59,130,246)"`, 1), ThemeCodeInvalidColor},
		{"css variable", strings.Replace(defaults, `"#3b82f6"`, `"var(--accent)"`, 1), ThemeCodeInvalidColor},
		{"named color", strings.Replace(defaults, `"#3b82f6"`, `"blue"`, 1), ThemeCodeInvalidColor},
		{"non hex digit", strings.Replace(defaults, `"#3b82f6"`, `"#3b82fg"`, 1), ThemeCodeInvalidColor},
		{"top-level array", `[]`, ThemeCodeJSONType},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := parseThemePlayerControls(json.RawMessage(testCase.payload), "theme.json")
			if err == nil {
				t.Fatalf("payload %s was accepted", testCase.payload)
			}
			if code := ThemePackageErrorCode(err); code != testCase.want {
				t.Fatalf("error code = %q, want %q (%v)", code, testCase.want, err)
			}
		})
	}
}

func TestPlayerControlsContrastContract(t *testing.T) {
	// The documented default palette must pass.
	if _, err := parseThemePlayerControls(
		json.RawMessage(themeTestPlayerControlsDefaultJSON()), "theme.json"); err != nil {
		t.Fatalf("default palette rejected: %v", err)
	}

	cases := []struct {
		name     string
		played   string
		buffered string
		track    string
		thumb    string
	}{
		{"played equals track", "#111827", "#f3f4f6", "#111827", "#3b82f6"},
		{"played equals buffered", "#3b82f6", "#3b82f6", "#111827", "#3b82f6"},
		{"buffered equals track", "#3b82f6", "#111827", "#111827", "#3b82f6"},
		{"thumb equals track", "#3b82f6", "#f3f4f6", "#111827", "#111827"},
		{"dark thumb on dark track", "#3b82f6", "#f3f4f6", "#111827", "#1f2937"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := parseThemePlayerControls(json.RawMessage(themeTestPlayerControlsJSON(
				testCase.played, testCase.buffered, testCase.track, testCase.thumb)), "theme.json")
			if err == nil {
				t.Fatal("low-contrast palette was accepted")
			}
			if code := ThemePackageErrorCode(err); code != ThemeCodeContrast {
				t.Fatalf("error code = %q, want %q (%v)", code, ThemeCodeContrast, err)
			}
		})
	}

	// thumb may equal played: the client draws a fixed outline, and the default
	// palette does exactly that.
	if _, err := parseThemePlayerControls(json.RawMessage(themeTestPlayerControlsJSON(
		"#3b82f6", "#f3f4f6", "#111827", "#3b82f6")), "theme.json"); err != nil {
		t.Fatalf("thumb equal to played must be allowed: %v", err)
	}
}

func TestThemePlayerPaletteForFallsBackAtRenderOnly(t *testing.T) {
	empty := ThemeDefinition{}
	if got := themePlayerPaletteFor(empty); got != (themePlayerPalette{
		Played:   ThemePlayerDefaultPlayed,
		Buffered: ThemePlayerDefaultBuffered,
		Track:    ThemePlayerDefaultTrack,
		Thumb:    ThemePlayerDefaultThumb,
	}) {
		t.Fatalf("nil block palette = %+v", got)
	}
	if empty.PlayerControls != nil {
		t.Fatal("resolving a palette must not populate the definition")
	}

	custom := themeTestPlayerControls(t, themeTestPlayerControlsJSON(
		"#2563eb", "#d1d5db", "#0b1220", "#2563eb"))
	definition := ThemeDefinition{PlayerControls: custom}
	palette := themePlayerPaletteFor(definition)
	if palette.Played != "#2563eb" || palette.Buffered != "#d1d5db" ||
		palette.Track != "#0b1220" || palette.Thumb != "#2563eb" {
		t.Fatalf("custom palette = %+v", palette)
	}

	// A partially filled in-memory block (never produced by the parser) falls
	// back per field instead of rendering an empty var().
	partial := themePlayerControls("#22c55e")
	if got := themePlayerPaletteFor(ThemeDefinition{PlayerControls: partial}); got != (themePlayerPalette{
		Played:   "#22c55e",
		Buffered: ThemePlayerDefaultBuffered,
		Track:    ThemePlayerDefaultTrack,
		Thumb:    ThemePlayerDefaultThumb,
	}) {
		t.Fatalf("partial palette = %+v", got)
	}
}

// themePlayerControls builds a block directly (helper for render tests).
func themePlayerControls(played string) *ThemePlayerControls {
	return &ThemePlayerControls{Played: &played}
}

func TestCloneThemePlayerControlsIsDeep(t *testing.T) {
	if cloneThemePlayerControls(nil) != nil {
		t.Fatal("clone(nil) must stay nil")
	}
	original := themeTestPlayerControls(t, themeTestPlayerControlsDefaultJSON())
	copied := cloneThemePlayerControls(original)
	if copied == original {
		t.Fatal("clone returned the same pointer")
	}
	if *copied.Played != *original.Played || *copied.Thumb != *original.Thumb {
		t.Fatalf("clone values differ: %+v vs %+v", copied, original)
	}
	*copied.Played = "#22c55e"
	*copied.Thumb = "#22c55e"
	if *original.Played != ThemePlayerDefaultPlayed || *original.Thumb != ThemePlayerDefaultThumb {
		t.Fatalf("mutating the clone changed the original: %+v", original)
	}
	if copied.Played == original.Played || copied.Buffered == original.Buffered ||
		copied.Track == original.Track || copied.Thumb == original.Thumb {
		t.Fatal("clone shares field pointers with the original")
	}
}

func TestThemeStoreValidatePlayerControlsCanonicalAndVersion(t *testing.T) {
	canonical := themeTestPlayerControls(t, themeTestPlayerControlsDefaultJSON())
	if err := themeStoreValidatePlayerControls(canonical); err != nil {
		t.Fatalf("canonical block rejected: %v", err)
	}
	if err := themeStoreValidatePlayerControls(nil); err != nil {
		t.Fatalf("nil block rejected: %v", err)
	}

	upper := "#3B82F6"
	notCanonical := cloneThemePlayerControls(canonical)
	notCanonical.Played = &upper
	if err := themeStoreValidatePlayerControls(notCanonical); err == nil {
		t.Fatal("upper-case stored color must be rejected as non-canonical")
	}

	invalid := cloneThemePlayerControls(canonical)
	bad := "#3b8"
	invalid.Track = &bad
	if err := themeStoreValidatePlayerControls(invalid); err == nil {
		t.Fatal("stored short hex must be rejected")
	}

	lowContrast := cloneThemePlayerControls(canonical)
	dark := "#111827"
	lowContrast.Thumb = &dark
	if err := themeStoreValidatePlayerControls(lowContrast); err == nil {
		t.Fatal("stored low-contrast thumb must be rejected")
	}

	if err := themeStoreValidatePlayerControlsForVersion(canonical, ThemeAPIVersion); err == nil {
		t.Fatal("playerControls must be rejected for themeApiVersion 1")
	}
	if err := themeStoreValidatePlayerControlsForVersion(canonical, ThemeAPIVersionLatest); err != nil {
		t.Fatalf("playerControls rejected for the latest version: %v", err)
	}
	if err := themeStoreValidatePlayerControlsForVersion(nil, ThemeAPIVersion); err != nil {
		t.Fatalf("absent block must be accepted for v1: %v", err)
	}
}
