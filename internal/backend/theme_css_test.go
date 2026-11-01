package backend

import (
	"errors"
	"strings"
	"testing"
)

// The generated stylesheet is the only place where a validated token becomes
// CSS. These tests pin the two derived values the client contract depends on
// (--ac-on contrast and --ac2 staying identical to the accent) and the fact
// that a background path which did not come from a validated package is
// rejected instead of being interpolated into url().

func themeCSSTestDefinition(accent string) ThemeDefinition {
	variant := ThemeVariant{
		Bg0:        "#101010",
		Bg1:        "#181818",
		Bg2:        "#202020",
		Bg3:        "#282828",
		Text1:      "#F0F0F0",
		Text2:      "#C0C0C0",
		Text3:      "#909090",
		Line:       "#303030",
		Line2:      "#404040",
		Accent:     accent,
		AccentText: "#0A4FB5",
	}
	return ThemeDefinition{
		Variants: map[string]ThemeVariant{
			ThemeVariantLight: variant,
			ThemeVariantDark:  variant,
		},
		Appearance: ThemeAppearance{
			Radius:  ThemeAppearanceRadiusDefault,
			Density: ThemeDensityComfortable,
		},
	}
}

func TestRenderThemeCSSAccentLabelContrastAndSecondaryAccent(t *testing.T) {
	hash := strings.Repeat("a", 64)
	cases := []struct {
		name   string
		accent string
		on     string
	}{
		{name: "black accent uses white label", accent: "#000000", on: "#FFFFFF"},
		{name: "white accent uses black label", accent: "#FFFFFF", on: "#000000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			css, err := renderThemeCSS(hash, themeCSSTestDefinition(tc.accent))
			if err != nil {
				t.Fatalf("renderThemeCSS: %v", err)
			}
			text := string(css)
			if !strings.Contains(text, "  --ac: "+tc.accent+";\n") {
				t.Fatalf("stylesheet must emit the validated accent %s:\n%s", tc.accent, text)
			}
			if !strings.Contains(text, "  --ac-on: "+tc.on+";\n") {
				t.Fatalf("accent %s must use label %s:\n%s", tc.accent, tc.on, text)
			}
			// The gradient keeps two identical stops so the single --ac-on
			// label colour stays valid across the whole gradient.
			if !strings.Contains(text, "  --ac2: "+tc.accent+";\n") {
				t.Fatalf("--ac2 must stay identical to the accent %s:\n%s", tc.accent, text)
			}
			if !strings.Contains(text, "linear-gradient(135deg, "+tc.accent+", "+tc.accent+")") {
				t.Fatalf("accent gradient must use the accent twice:\n%s", text)
			}
		})
	}
}

func TestRenderThemeCSSRejectsInvalidContentHashAndBackgroundPath(t *testing.T) {
	validHash := strings.Repeat("a", 64)
	base := themeCSSTestDefinition("#0A4FB5")
	withBackground := func(path string) ThemeDefinition {
		definition := base
		definition.BackgroundImage = path
		return definition
	}
	cases := []struct {
		name       string
		hash       string
		definition ThemeDefinition
	}{
		{name: "short hash", hash: strings.Repeat("a", 63), definition: base},
		{name: "non hex hash", hash: strings.Repeat("g", 64), definition: base},
		{name: "empty hash", hash: "", definition: base},
		{name: "parent traversal background", hash: validHash, definition: withBackground("../background.png")},
		{name: "quote in background", hash: validHash, definition: withBackground(`assets/back"ground.png`)},
		{name: "parenthesis in background", hash: validHash, definition: withBackground("assets/back(ground).png")},
		{name: "space in background", hash: validHash, definition: withBackground("assets/back ground.png")},
		{name: "unsupported extension", hash: validHash, definition: withBackground("assets/background.svg")},
		{name: "directory without file", hash: validHash, definition: withBackground("assets/")},
		{name: "javascript scheme", hash: validHash, definition: withBackground("javascript:alert(1)")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			css, err := renderThemeCSS(tc.hash, tc.definition)
			if !errors.Is(err, errThemeCSSInvalid) {
				t.Fatalf("renderThemeCSS error = %v (css=%q), want errThemeCSSInvalid", err, css)
			}
			if css != nil {
				t.Fatalf("rejected definition must not produce stylesheet bytes: %q", css)
			}
		})
	}
}

func TestRenderThemeCSSRequiresBothVariants(t *testing.T) {
	base := themeCSSTestDefinition("#0A4FB5")

	missingDark := base
	missingDark.Variants = map[string]ThemeVariant{ThemeVariantLight: base.Variants[ThemeVariantLight]}
	if _, err := renderThemeCSS(strings.Repeat("a", 64), missingDark); !errors.Is(err, errThemeCSSInvalid) {
		t.Fatalf("missing dark variant error = %v, want errThemeCSSInvalid", err)
	}

	missingLight := base
	missingLight.Variants = map[string]ThemeVariant{ThemeVariantDark: base.Variants[ThemeVariantDark]}
	if _, err := renderThemeCSS(strings.Repeat("a", 64), missingLight); !errors.Is(err, errThemeCSSInvalid) {
		t.Fatalf("missing light variant error = %v, want errThemeCSSInvalid", err)
	}
}
