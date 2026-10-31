package backend

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Web theme package tokens (theme API v1).
//
// A package may only supply literal #RRGGBB color tokens plus a tiny, fixed set
// of layout hints. Shadows, overlays, gradients and every CSS selector are
// derived by the server and the web client, so no CSS text from an uploaded
// package is ever executed. Contrast requirements are part of the contract: a
// package that ships unreadable text tokens is rejected instead of installed.

const (
	// ThemeVariantLight and ThemeVariantDark are the only supported variants.
	// A custom theme package must provide both.
	ThemeVariantLight = "light"
	ThemeVariantDark  = "dark"

	// ThemeDensityCompact and ThemeDensityComfortable are the only supported
	// layout densities.
	ThemeDensityCompact     = "compact"
	ThemeDensityComfortable = "comfortable"

	// ThemeAppearanceRadiusMin/Max and ThemeAppearanceRadiusDefault bound the
	// panel corner radius in CSS pixels.
	ThemeAppearanceRadiusMin     = 4
	ThemeAppearanceRadiusMax     = 24
	ThemeAppearanceRadiusDefault = 14

	// ThemeDensityDefault is applied when theme.json omits appearance.
	ThemeDensityDefault = ThemeDensityComfortable
)

// Contrast requirements (WCAG 2.x relative-luminance ratio).
const (
	// ThemeContrastPrimaryText is the minimum ratio between text1 and bg0/bg1.
	ThemeContrastPrimaryText = 4.5
	// ThemeContrastSecondaryText is the minimum ratio for text2 and text3
	// against bg0/bg1.
	ThemeContrastSecondaryText = 3.0
	// ThemeContrastAccentText is the minimum ratio between accentText and bg0/bg1.
	// accentText is the page-level emphasis/link color; button labels derive
	// their own black/white foreground from accent instead of using this token.
	ThemeContrastAccentText = 4.5
)

// themeVariantColorFields is the frozen token order of a variant. Validation,
// documentation and the canonical hash all rely on this single list.
var themeVariantColorFields = []string{
	"bg0", "bg1", "bg2", "bg3",
	"text1", "text2", "text3",
	"line", "line2",
	"accent", "accentText",
}

// themeReservedIDs are names that the builtin theme or the router already
// occupies; a package may never claim them.
var themeReservedIDs = map[string]struct{}{
	"builtin": {},
	"default": {},
	"admin":   {},
	"web":     {},
}

// normalizeThemeColor accepts only a literal "#RRGGBB" value and returns it in
// upper case so the canonical hash is stable across letter casing.
func normalizeThemeColor(raw string) (string, error) {
	if len(raw) != 7 || raw[0] != '#' {
		return "", fmt.Errorf("value must be a literal #RRGGBB color")
	}
	value := strings.ToUpper(raw[1:])
	for i := 0; i < len(value); i++ {
		if !isThemeHexDigit(value[i]) {
			return "", fmt.Errorf("value must be a literal #RRGGBB color")
		}
	}
	return "#" + value, nil
}

func isThemeHexDigit(c byte) bool {
	return (c >= '0' && c <= '9') || (c >= 'A' && c <= 'F')
}

// themeColorRGB splits an already normalized #RRGGBB value.
func themeColorRGB(hex string) (uint8, uint8, uint8) {
	value, err := strconv.ParseUint(hex[1:], 16, 32)
	if err != nil {
		return 0, 0, 0
	}
	return uint8(value >> 16), uint8(value >> 8), uint8(value)
}

// themeColorLuminance returns the WCAG relative luminance of a #RRGGBB color.
func themeColorLuminance(hex string) float64 {
	channel := func(c uint8) float64 {
		v := float64(c) / 255
		if v <= 0.03928 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	r, g, b := themeColorRGB(hex)
	return 0.2126*channel(r) + 0.7152*channel(g) + 0.0722*channel(b)
}

// themeContrastRatio returns the WCAG contrast ratio between two #RRGGBB
// colors. Callers must pass normalized values.
func themeContrastRatio(foreground, background string) float64 {
	lighter := themeColorLuminance(foreground)
	darker := themeColorLuminance(background)
	if lighter < darker {
		lighter, darker = darker, lighter
	}
	return (lighter + 0.05) / (darker + 0.05)
}

// themeContrastCheck is one required foreground/background pair.
type themeContrastCheck struct {
	foreground string
	background string
	minimum    float64
}

// checkThemeVariantContrast enforces the frozen contrast contract of a variant.
func checkThemeVariantContrast(variant string, v ThemeVariant) error {
	checks := []themeContrastCheck{
		{v.Text1, v.Bg0, ThemeContrastPrimaryText},
		{v.Text1, v.Bg1, ThemeContrastPrimaryText},
		{v.Text2, v.Bg0, ThemeContrastSecondaryText},
		{v.Text2, v.Bg1, ThemeContrastSecondaryText},
		{v.Text3, v.Bg0, ThemeContrastSecondaryText},
		{v.Text3, v.Bg1, ThemeContrastSecondaryText},
		{v.AccentText, v.Bg0, ThemeContrastAccentText},
		{v.AccentText, v.Bg1, ThemeContrastAccentText},
	}
	for _, check := range checks {
		ratio := themeContrastRatio(check.foreground, check.background)
		// A tiny epsilon keeps exactly-at-threshold colors from failing on
		// floating point rounding.
		if ratio+1e-9 < check.minimum {
			return themeErrorf(ThemeCodeContrast, "theme.json",
				"variant %q: %s on %s contrast ratio %.2f is below %.2f",
				variant, check.foreground, check.background, ratio, check.minimum)
		}
	}
	return nil
}

// validThemeDensity reports whether density is one of the frozen enums.
func validThemeDensity(density string) bool {
	return density == ThemeDensityCompact || density == ThemeDensityComfortable
}
