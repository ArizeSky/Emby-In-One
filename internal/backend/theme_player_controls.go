package backend

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Theme API v2 shared player progress palette.
//
// themeApiVersion 1 is frozen: its field set, canonical hash and generated
// stylesheet stay byte-identical, so the optional top-level "playerControls"
// block is only accepted for themeApiVersion 2. For v2 the block itself is
// optional (a package may only retheme the page), but once it is present all
// four colors must be present and valid: a partial block is rejected instead of
// being silently completed with defaults.
//
// Defaults are a render-time fallback only. They are never written into a
// stored definition, never serialized into the DTO and never part of the
// canonical hash, so "not configured" stays distinguishable from "configured to
// the default color".
//
// Canonical form of this block is lower-case #rrggbb (unlike the frozen v1
// variant tokens, which stay upper-case) so v2 hashes are stable across letter
// casing without touching the v1 identity.

const (
	// ThemePlayerDefaultPlayed/Buffered/Track/Thumb are the fixed fallback
	// palette used when a v2 package omits playerControls. The combination is
	// contrast-reviewed (played/buffered 3.34, played/track 4.82,
	// buffered/track 16.12, thumb/track 4.82).
	ThemePlayerDefaultPlayed   = "#3b82f6"
	ThemePlayerDefaultBuffered = "#f3f4f6"
	ThemePlayerDefaultTrack    = "#111827"
	ThemePlayerDefaultThumb    = "#3b82f6"

	// ThemePlayerContrastMinimum is the required WCAG ratio between
	// played/buffered/track (every pair) and between thumb and track. thumb is
	// not compared against played: the client draws a fixed two-tone outline
	// for the thumb, and the default palette intentionally reuses played as
	// thumb.
	ThemePlayerContrastMinimum = 3.0
)

// ThemePlayerControls is the optional v2 player palette shared by light and
// dark mode (the player background stays fixed dark; a theme cannot lighten
// it). Every field is a pointer so "unset" can never be confused with a real
// color, and omitempty keeps v1 output byte-identical.
type ThemePlayerControls struct {
	Played   *string `json:"played,omitempty"`
	Buffered *string `json:"buffered,omitempty"`
	Track    *string `json:"track,omitempty"`
	Thumb    *string `json:"thumb,omitempty"`
}

// themePlayerControlFields is the frozen field order of the block.
var themePlayerControlFields = []string{"played", "buffered", "track", "thumb"}

// themePlayerPalette is the fully resolved palette handed to the stylesheet
// renderer. It only exists in memory while a response is built.
type themePlayerPalette struct {
	Played   string
	Buffered string
	Track    string
	Thumb    string
}

// themePlayerPaletteFor resolves a definition to concrete colors, applying the
// fixed defaults for an absent (or, defensively, incomplete) block. It never
// mutates the definition.
func themePlayerPaletteFor(definition ThemeDefinition) themePlayerPalette {
	palette := themePlayerPalette{
		Played:   ThemePlayerDefaultPlayed,
		Buffered: ThemePlayerDefaultBuffered,
		Track:    ThemePlayerDefaultTrack,
		Thumb:    ThemePlayerDefaultThumb,
	}
	controls := definition.PlayerControls
	if controls == nil {
		return palette
	}
	if controls.Played != nil {
		palette.Played = *controls.Played
	}
	if controls.Buffered != nil {
		palette.Buffered = *controls.Buffered
	}
	if controls.Track != nil {
		palette.Track = *controls.Track
	}
	if controls.Thumb != nil {
		palette.Thumb = *controls.Thumb
	}
	return palette
}

// cloneThemePlayerControls returns an independent deep copy; nil stays nil.
func cloneThemePlayerControls(controls *ThemePlayerControls) *ThemePlayerControls {
	if controls == nil {
		return nil
	}
	out := &ThemePlayerControls{}
	if controls.Played != nil {
		value := *controls.Played
		out.Played = &value
	}
	if controls.Buffered != nil {
		value := *controls.Buffered
		out.Buffered = &value
	}
	if controls.Track != nil {
		value := *controls.Track
		out.Track = &value
	}
	if controls.Thumb != nil {
		value := *controls.Thumb
		out.Thumb = &value
	}
	return out
}

// themePlayerControlValues dereferences a controls block. ok is false when the
// block is nil or any field is unset.
func themePlayerControlValues(controls *ThemePlayerControls) (played, buffered, track, thumb string, ok bool) {
	if controls == nil ||
		controls.Played == nil || controls.Buffered == nil ||
		controls.Track == nil || controls.Thumb == nil {
		return "", "", "", "", false
	}
	return *controls.Played, *controls.Buffered, *controls.Track, *controls.Thumb, true
}

// normalizeThemePlayerColor accepts only a literal "#RRGGBB" value and returns
// it in lower case. This is the v2 player-block canonical form; the v1 variant
// tokens keep their own (upper-case) normalization and are not touched.
func normalizeThemePlayerColor(raw string) (string, error) {
	if len(raw) != 7 || raw[0] != '#' {
		return "", fmt.Errorf("value must be a literal #RRGGBB color")
	}
	value := strings.ToLower(raw[1:])
	for i := 0; i < len(value); i++ {
		c := value[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return "", fmt.Errorf("value must be a literal #RRGGBB color")
		}
	}
	return "#" + value, nil
}

// parseThemePlayerControls validates the optional v2 playerControls block.
// null, an empty object, an unknown field, a partial block and non-string or
// non-#RRGGBB values are all rejected; nothing is defaulted here.
func parseThemePlayerControls(raw json.RawMessage, path string) (*ThemePlayerControls, error) {
	fields, err := decodeThemeJSONObject(raw, path, ThemeJSONMaxBytes)
	if err != nil {
		return nil, err
	}
	if err := rejectThemeJSONUnknownFields(fields, themePlayerControlFields, path); err != nil {
		return nil, err
	}
	controls := &ThemePlayerControls{}
	for _, name := range themePlayerControlFields {
		rawColor, ok := fields[name]
		if !ok {
			return nil, themeErrorf(ThemeCodeInvalidTheme, path,
				"playerControls.%s is required when playerControls is present", name)
		}
		value, err := themeJSONRawString(rawColor, "playerControls."+name, path)
		if err != nil {
			return nil, err
		}
		normalized, err := normalizeThemePlayerColor(value)
		if err != nil {
			return nil, themeErrorf(ThemeCodeInvalidColor, path,
				"playerControls.%s: %s", name, err.Error())
		}
		switch name {
		case "played":
			controls.Played = &normalized
		case "buffered":
			controls.Buffered = &normalized
		case "track":
			controls.Track = &normalized
		case "thumb":
			controls.Thumb = &normalized
		}
	}
	if err := checkThemePlayerControlsContrast(controls); err != nil {
		return nil, err
	}
	return controls, nil
}

// checkThemePlayerControlsContrast enforces the v2 contrast contract:
// played/buffered/track pairwise at least ThemePlayerContrastMinimum, thumb
// against track at least ThemePlayerContrastMinimum. Values must already be
// normalized lower-case #rrggbb.
func checkThemePlayerControlsContrast(controls *ThemePlayerControls) error {
	played, buffered, track, thumb, ok := themePlayerControlValues(controls)
	if !ok {
		return themeErrorf(ThemeCodeInvalidTheme, "theme.json",
			"playerControls must define played, buffered, track and thumb")
	}
	checks := []themeContrastCheck{
		{played, buffered, ThemePlayerContrastMinimum},
		{played, track, ThemePlayerContrastMinimum},
		{buffered, track, ThemePlayerContrastMinimum},
		// thumb needs no outline against played (fixed client outline), but it
		// must stay distinguishable from the rail it sits on.
		{thumb, track, ThemePlayerContrastMinimum},
	}
	for _, check := range checks {
		ratio := themeContrastRatio(check.foreground, check.background)
		if ratio+1e-9 < check.minimum {
			return themeErrorf(ThemeCodeContrast, "theme.json",
				"playerControls: %s on %s contrast ratio %.2f is below %.2f",
				check.foreground, check.background, ratio, check.minimum)
		}
	}
	return nil
}

// themeStoreValidatePlayerControls re-validates a persisted playerControls
// block: values must be canonical lower-case #rrggbb (no upper-case variants,
// no defaults injected) and must satisfy the contrast contract.
func themeStoreValidatePlayerControls(controls *ThemePlayerControls) error {
	if controls == nil {
		return nil
	}
	played, buffered, track, thumb, ok := themePlayerControlValues(controls)
	if !ok {
		return fmt.Errorf("%w: playerControls must define all four colors", ErrThemeInvalid)
	}
	values := []struct {
		name  string
		value string
	}{
		{"played", played},
		{"buffered", buffered},
		{"track", track},
		{"thumb", thumb},
	}
	for _, entry := range values {
		normalized, err := normalizeThemePlayerColor(entry.value)
		if err != nil {
			return fmt.Errorf("%w: playerControls.%s is not a literal #RRGGBB color", ErrThemeInvalid, entry.name)
		}
		if normalized != entry.value {
			return fmt.Errorf("%w: playerControls.%s is not canonical lower-case #rrggbb", ErrThemeInvalid, entry.name)
		}
	}
	if err := checkThemePlayerControlsContrast(controls); err != nil {
		return fmt.Errorf("%w: %v", ErrThemeInvalid, err)
	}
	return nil
}

// themeStoreValidatePlayerControlsForVersion enforces that only
// themeApiVersion 2 may carry the block.
func themeStoreValidatePlayerControlsForVersion(controls *ThemePlayerControls, apiVersion int) error {
	if controls == nil {
		return nil
	}
	if apiVersion != ThemeAPIVersionLatest {
		return fmt.Errorf("%w: playerControls requires themeApiVersion %d",
			ErrThemeInvalid, ThemeAPIVersionLatest)
	}
	return themeStoreValidatePlayerControls(controls)
}
