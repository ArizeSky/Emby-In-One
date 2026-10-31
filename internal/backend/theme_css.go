package backend

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Server-generated theme stylesheet.
//
// The package parser validates 11 literal #RRGGBB tokens per variant; this file
// turns them into CSS custom properties and derives the few extra values the
// client needs (secondary accent, the best black/white label colour for the
// accent, a glow colour, radius and density hints, and the background image
// variable). No selector, declaration or URL from an uploaded package is
// emitted: every rule below is fixed text.
//
// The selectors only match the /web client (body.eio-web) and the isolated
// preview document (body.eio-preview) under an explicit
// html[data-eio-theme="light|dark"] attribute. The /admin panel has neither the
// attribute nor the body class, so it can never inherit a theme, and the
// player's fixed black/white styles are not referenced here at all.

// errThemeCSSInvalid is returned when the caller passes data that did not come
// from a validated package (defence in depth, never a user-facing message).
var errThemeCSSInvalid = errors.New("theme css: invalid input")

// renderThemeCSS builds the immutable stylesheet for one content hash.
func renderThemeCSS(contentHash string, definition ThemeDefinition) ([]byte, error) {
	if !themeAPIHashValid(contentHash) {
		return nil, errThemeCSSInvalid
	}
	light, ok := definition.Variants[ThemeVariantLight]
	if !ok {
		return nil, errThemeCSSInvalid
	}
	dark, ok := definition.Variants[ThemeVariantDark]
	if !ok {
		return nil, errThemeCSSInvalid
	}
	backgroundRule := ""
	if definition.BackgroundImage != "" {
		path, err := themeCSSBackgroundPath(definition.BackgroundImage)
		if err != nil {
			return nil, err
		}
		url := themeAPIResourceURL(contentHash, path)
		backgroundRule = "html[data-eio-theme=\"light\"] body.eio-web,\n" +
			"html[data-eio-theme=\"dark\"] body.eio-web,\n" +
			"html[data-eio-theme=\"light\"] body.eio-preview,\n" +
			"html[data-eio-theme=\"dark\"] body.eio-preview {\n" +
			"  --eio-theme-bg: url(\"" + url + "\");\n" +
			"}\n\n"
	}

	var builder strings.Builder
	builder.Grow(4096)
	builder.WriteString("/* Emby-in-One web theme (generated, immutable).\n")
	builder.WriteString(" * Tokens come from a validated theme package; every derived value and\n")
	builder.WriteString(" * every selector is produced by the server. The player keeps its fixed\n")
	builder.WriteString(" * black/white styles and /admin never matches these selectors.\n")
	builder.WriteString(" */\n\n")
	builder.WriteString(themeCSSVariantBlock(ThemeVariantLight, light, definition.Appearance))
	builder.WriteString(themeCSSVariantBlock(ThemeVariantDark, dark, definition.Appearance))
	builder.WriteString(backgroundRule)
	builder.WriteString(themeCSSPreviewRules())
	return []byte(builder.String()), nil
}

// themeCSSVariantBlock emits the token block for one variant.
//
// accent2 is deliberately the same literal accent as --ac. The accent gradient
// has two identical stops, so the black/white --ac-on label colour chosen for
// accent stays valid across the whole gradient. Blending the second stop toward
// text1 would make one end a different background and could silently break the
// contrast guarantee.
func themeCSSVariantBlock(variant string, colors ThemeVariant, appearance ThemeAppearance) string {
	accent2 := colors.Accent
	onAccent := themeCSSBestOnAccent(colors.Accent)
	radius := appearance.Radius
	if radius == 0 {
		radius = ThemeAppearanceRadiusDefault
	}
	buttonRadius := radius - 4
	if buttonRadius < 4 {
		buttonRadius = 4
	}
	densitySpace := 18
	if appearance.Density == ThemeDensityCompact {
		densitySpace = 12
	}
	var builder strings.Builder
	builder.WriteString("html[data-eio-theme=\"" + variant + "\"] body.eio-web,\n")
	builder.WriteString("html[data-eio-theme=\"" + variant + "\"] body.eio-preview {\n")
	builder.WriteString("  --bg-0: " + colors.Bg0 + ";\n")
	builder.WriteString("  --bg-1: " + colors.Bg1 + ";\n")
	builder.WriteString("  --bg-2: " + colors.Bg2 + ";\n")
	builder.WriteString("  --bg-3: " + colors.Bg3 + ";\n")
	builder.WriteString("  --t1: " + colors.Text1 + ";\n")
	builder.WriteString("  --t2: " + colors.Text2 + ";\n")
	builder.WriteString("  --t3: " + colors.Text3 + ";\n")
	builder.WriteString("  --line: " + colors.Line + ";\n")
	builder.WriteString("  --line-2: " + colors.Line2 + ";\n")
	builder.WriteString("  --ac: " + colors.Accent + ";\n")
	builder.WriteString("  --ac2: " + accent2 + ";\n")
	builder.WriteString("  --ac-text: " + colors.AccentText + ";\n")
	builder.WriteString("  --ac-on: " + onAccent + ";\n")
	builder.WriteString("  --ac-grad: linear-gradient(135deg, " + colors.Accent + ", " + accent2 + ");\n")
	builder.WriteString("  --ac-glow: " + themeCSSGlow(colors.Accent) + ";\n")
	builder.WriteString("  --r-card: " + strconv.Itoa(radius) + "px;\n")
	builder.WriteString("  --r-btn: " + strconv.Itoa(buttonRadius) + "px;\n")
	builder.WriteString("  --r-pill: 999px;\n")
	builder.WriteString("  --eio-density-space: " + strconv.Itoa(densitySpace) + "px;\n")
	builder.WriteString("}\n\n")
	return builder.String()
}

// themeCSSBackgroundPath re-checks the validated background reference so a
// quote, backslash or parenthesis can never reach the generated url().
func themeCSSBackgroundPath(path string) (string, error) {
	if !strings.HasPrefix(path, "assets/") || len(path) <= len("assets/") {
		return "", errThemeCSSInvalid
	}
	lower := strings.ToLower(path)
	if !strings.HasSuffix(lower, ".png") && !strings.HasSuffix(lower, ".jpg") && !strings.HasSuffix(lower, ".jpeg") {
		return "", errThemeCSSInvalid
	}
	for i := 0; i < len(path); i++ {
		c := path[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '.', c == '-', c == '_', c == '/':
		default:
			return "", errThemeCSSInvalid
		}
	}
	return path, nil
}

// themeCSSPreviewRules is the fixed layout of the isolated preview document.
// It only uses the theme variables above and never opens a theme-controlled
// selector; the panel below is deliberately opaque.
func themeCSSPreviewRules() string {
	return `body.eio-preview {
  margin: 0;
  min-height: 100vh;
  font-family: system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
  background: var(--bg-0);
  color: var(--t1);
}
body.eio-preview .eio-preview-shell { display: flex; min-height: 100vh; }
body.eio-preview .eio-preview-nav {
  flex: 0 0 176px; display: flex; flex-direction: column; gap: 6px;
  padding: 18px 12px; background: var(--bg-1); border-right: 1px solid var(--line);
}
body.eio-preview .eio-preview-brand {
  display: flex; align-items: center; gap: 10px; padding: 4px 8px 16px;
  font-weight: 700; font-size: 15px;
}
body.eio-preview .eio-preview-mark {
  width: 30px; height: 30px; border-radius: var(--r-btn); display: grid; place-items: center;
  background: var(--ac-grad); color: var(--ac-on); font-size: 13px;
}
body.eio-preview .eio-preview-nav-item {
  display: flex; align-items: center; min-height: 38px; padding: 8px 10px;
  border-radius: var(--r-btn); color: var(--t2); font-size: 13px;
}
body.eio-preview .eio-preview-nav-item-active {
  background: var(--ac); color: var(--ac-on); font-weight: 600;
}
body.eio-preview .eio-preview-main {
  flex: 1 1 auto; min-width: 0; padding: 26px 24px;
  background-image: var(--eio-theme-bg, none);
  background-size: cover; background-position: center;
}
body.eio-preview .eio-preview-titlebar {
  display: flex; align-items: center; gap: 12px; margin-bottom: 18px;
}
body.eio-preview .eio-preview-titlebar h1 { margin: 0; font-size: 21px; letter-spacing: -.01em; }
body.eio-preview .eio-preview-badge {
  padding: 3px 9px; border-radius: var(--r-pill); font-size: 11px;
  background: var(--bg-2); color: var(--ac-text); border: 1px solid var(--line-2);
}
body.eio-preview .eio-preview-grid {
  display: grid; grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: var(--eio-density-space); margin-bottom: var(--eio-density-space);
}
body.eio-preview .eio-preview-card {
  border-radius: var(--r-card); overflow: hidden;
  background: var(--bg-1); border: 1px solid var(--line);
}
body.eio-preview .eio-preview-poster {
  aspect-ratio: 16 / 9; display: grid; place-items: center;
  background: var(--bg-3); color: var(--t3); font-size: 12px;
}
body.eio-preview .eio-preview-poster img { width: 100%; height: 100%; object-fit: cover; display: block; }
body.eio-preview .eio-preview-card-body { padding: 10px 12px 12px; }
body.eio-preview .eio-preview-card-title { font-size: 13px; font-weight: 600; margin-bottom: 3px; }
body.eio-preview .eio-preview-card-sub { font-size: 12px; color: var(--t2); }
body.eio-preview .eio-preview-panel {
  margin-top: var(--eio-density-space); padding: 16px;
  border-radius: var(--r-card); background: var(--bg-2); border: 1px solid var(--line);
}
body.eio-preview .eio-preview-panel p { margin: 0 0 6px; font-size: 13px; }
body.eio-preview .eio-preview-panel p.eio-preview-sub { margin: 0; color: var(--t2); }
body.eio-preview .eio-preview-link { color: var(--ac-text); text-decoration: none; font-size: 13px; font-weight: 600; }
@media (max-width: 640px) {
  body.eio-preview .eio-preview-nav { display: none; }
  body.eio-preview .eio-preview-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); }
}
`
}

// themeCSSGlow renders rgba() from the accent token.
func themeCSSGlow(accent string) string {
	r, g, b, ok := themeCSSHexRGB(accent)
	if !ok {
		return "rgba(0, 0, 0, 0.32)"
	}
	return fmt.Sprintf("rgba(%d, %d, %d, 0.32)", r, g, b)
}

// themeCSSBestOnAccent picks the label color with the higher contrast ratio on
// the accent background: the contract derives --ac-on from accent alone instead
// of reusing accentText.
func themeCSSBestOnAccent(accent string) string {
	onBlack := themeCSSContrast("#000000", accent)
	onWhite := themeCSSContrast("#FFFFFF", accent)
	if onBlack >= onWhite {
		return "#000000"
	}
	return "#FFFFFF"
}

// themeCSSContrast returns the WCAG relative-luminance contrast ratio.
func themeCSSContrast(foreground, background string) float64 {
	lighter := themeCSSLuminance(foreground)
	darker := themeCSSLuminance(background)
	if lighter < darker {
		lighter, darker = darker, lighter
	}
	return (lighter + 0.05) / (darker + 0.05)
}

// themeCSSLuminance returns the WCAG relative luminance of #RRGGBB.
func themeCSSLuminance(hex string) float64 {
	r, g, b, ok := themeCSSHexRGB(hex)
	if !ok {
		return 0
	}
	channel := func(value uint8) float64 {
		v := float64(value) / 255
		if v <= 0.03928 {
			return v / 12.92
		}
		return math.Pow((v+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(r) + 0.7152*channel(g) + 0.0722*channel(b)
}

// themeCSSHexRGB parses a normalized #RRGGBB value.
func themeCSSHexRGB(hex string) (uint8, uint8, uint8, bool) {
	if len(hex) != 7 || hex[0] != '#' {
		return 0, 0, 0, false
	}
	value, err := strconv.ParseUint(hex[1:], 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return uint8(value >> 16), uint8(value >> 8), uint8(value), true
}
