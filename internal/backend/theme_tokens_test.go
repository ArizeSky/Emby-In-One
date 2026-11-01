package backend

import (
	"math"
	"strings"
	"testing"
)

// Token normalization and contrast contract tests.

func TestNormalizeThemeColor(t *testing.T) {
	valid := map[string]string{
		"#FFFFFF": "#FFFFFF",
		"#ffffff": "#FFFFFF",
		"#a1b2c3": "#A1B2C3",
		"#000000": "#000000",
		"#0A4FB5": "#0A4FB5",
	}
	for raw, want := range valid {
		got, err := normalizeThemeColor(raw)
		if err != nil {
			t.Fatalf("expected %q to be valid: %v", raw, err)
		}
		if got != want {
			t.Fatalf("normalizeThemeColor(%q) = %q, want %q", raw, got, want)
		}
	}

	invalid := []string{
		"",
		"#",
		"#fff",
		"#ffff",
		"#ffffffff",
		"ffffff",
		" #FFFFFF",
		"#FFFFFF ",
		"#GGGGGG",
		"#12345G",
		"rgb(0,0,0)",
		"rgba(0,0,0,0.5)",
		"hsl(0,0%,0%)",
		"var(--bg0)",
		"url(https://example.invalid/x.png)",
		"linear-gradient(#000,#fff)",
		"#FFFFFF;",
		"red",
		"transparent",
	}
	for _, raw := range invalid {
		if _, err := normalizeThemeColor(raw); err == nil {
			t.Fatalf("expected %q to be rejected", raw)
		}
	}
}

func TestThemeColorLuminance(t *testing.T) {
	if got := themeColorLuminance("#000000"); math.Abs(got) > 1e-9 {
		t.Fatalf("expected black luminance 0, got %v", got)
	}
	if got := themeColorLuminance("#FFFFFF"); math.Abs(got-1) > 1e-9 {
		t.Fatalf("expected white luminance 1, got %v", got)
	}
	if themeColorLuminance("#FFFFFF") <= themeColorLuminance("#777777") {
		t.Fatalf("expected lighter color to have higher luminance")
	}
	r, g, b := themeColorRGB("#0A4FB5")
	if r != 0x0A || g != 0x4F || b != 0xB5 {
		t.Fatalf("themeColorRGB returned %02X%02X%02X", r, g, b)
	}
}

func TestThemeContrastRatio(t *testing.T) {
	if got := themeContrastRatio("#000000", "#FFFFFF"); math.Abs(got-21) > 1e-6 {
		t.Fatalf("expected 21:1 for black on white, got %v", got)
	}
	if got := themeContrastRatio("#FFFFFF", "#000000"); math.Abs(got-21) > 1e-6 {
		t.Fatalf("expected contrast to be symmetric, got %v", got)
	}
	if got := themeContrastRatio("#123456", "#123456"); math.Abs(got-1) > 1e-9 {
		t.Fatalf("expected 1:1 for identical colors, got %v", got)
	}
}

func TestCheckThemeVariantContrast(t *testing.T) {
	light, err := parseThemeVariant([]byte(themeTestLightVariantJSON), ThemeVariantLight)
	if err != nil {
		t.Fatalf("expected fixture light variant to pass contrast: %v", err)
	}
	if err := checkThemeVariantContrast(ThemeVariantLight, light); err != nil {
		t.Fatalf("expected fixture light variant to pass contrast: %v", err)
	}
	dark, err := parseThemeVariant([]byte(themeTestDarkVariantJSON), ThemeVariantDark)
	if err != nil {
		t.Fatalf("expected fixture dark variant to pass contrast: %v", err)
	}

	lowText := light
	lowText.Text1 = "#7A7A7A"
	if err := checkThemeVariantContrast(ThemeVariantLight, lowText); err == nil {
		t.Fatalf("expected low-contrast primary text to be rejected")
	} else {
		themeTestRequireCode(t, err, ThemeCodeContrast)
	}

	lowSecondary := dark
	lowSecondary.Text2 = "#2C3038"
	if err := checkThemeVariantContrast(ThemeVariantDark, lowSecondary); err == nil {
		t.Fatalf("expected low-contrast secondary text to be rejected")
	} else {
		themeTestRequireCode(t, err, ThemeCodeContrast)
	}

	lowAccentText := light
	lowAccentText.AccentText = "#F4F4F4"
	if err := checkThemeVariantContrast(ThemeVariantLight, lowAccentText); err == nil {
		t.Fatalf("expected low-contrast accent link text to be rejected")
	} else {
		themeTestRequireCode(t, err, ThemeCodeContrast)
	}
}

func TestValidThemeDensity(t *testing.T) {
	for _, density := range []string{ThemeDensityCompact, ThemeDensityComfortable} {
		if !validThemeDensity(density) {
			t.Fatalf("expected %q to be a valid density", density)
		}
	}
	for _, density := range []string{"", "Compact", "cozy", "comfortable ", "compact "} {
		if validThemeDensity(density) {
			t.Fatalf("expected %q to be rejected", density)
		}
	}
}

func TestThemeVariantColorFieldsFrozenOrder(t *testing.T) {
	want := []string{
		"bg0", "bg1", "bg2", "bg3",
		"text1", "text2", "text3",
		"line", "line2",
		"accent", "accentText",
	}
	if len(themeVariantColorFields) != len(want) {
		t.Fatalf("expected %d color fields, got %d", len(want), len(themeVariantColorFields))
	}
	seen := make(map[string]struct{}, len(want))
	for index, name := range want {
		if themeVariantColorFields[index] != name {
			t.Fatalf("color field %d is %q, want %q", index, themeVariantColorFields[index], name)
		}
		if _, duplicate := seen[name]; duplicate {
			t.Fatalf("duplicate color field %q", name)
		}
		seen[name] = struct{}{}
	}
	if len(themeDefinitionFields) == 0 {
		t.Fatalf("expected frozen theme.json field list")
	}
	joined := strings.Join(themeDefinitionFields, ",")
	for _, field := range []string{"variants", "appearance", "backgroundImage"} {
		if !strings.Contains(joined, field) {
			t.Fatalf("theme.json field list is missing %q", field)
		}
	}
	for _, reserved := range []string{"builtin", "default", "admin", "web"} {
		if _, ok := themeReservedIDs[reserved]; !ok {
			t.Fatalf("expected %q to be reserved", reserved)
		}
	}
}
