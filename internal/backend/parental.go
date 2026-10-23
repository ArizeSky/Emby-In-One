package backend

import "strings"

// parentalRatingScores mirrors the scores Emby/Jellyfin expose through
// GET /Localization/ParentalRatings. A user's MaxParentalRating is compared
// against these numbers; items scoring higher are hidden.
//
// Source: Emby 4.10.1.0 /Localization/ParentalRatings, cross-checked with
// Jellyfin's MediaBrowser.Model/Entities/ParentalRating.
var parentalRatingScores = map[string]int{
	"TV-Y": 1, "APPROVED": 1, "G": 1, "E": 1, "EC": 1, "TV-G": 1,
	"TV-Y7": 3, "TV-Y7-FV": 4,
	"PG": 5, "TV-PG": 5,
	"PG-13": 7, "T": 7,
	"TV-14": 8,
	"R":     9, "M": 9, "TV-MA": 9,
	"NC-17": 10,
	"AO":    15, "RP": 15, "UR": 15, "X": 15, "XXX": 15,
}

// ratingAliases covers the spellings sources emit for ratings the score table
// already knows; only concrete same-system equivalences map here. Cross-regime
// values (FSK, age-strings of unverified origin) stay unrecognised, and digits
// are never extracted from arbitrary strings to manufacture a score.
var ratingAliases = map[string]string{
	"PG13":        "PG-13",
	"PG 13":       "PG-13",
	"RATED R":     "R",
	"RATED PG":    "PG",
	"RATED PG-13": "PG-13",
	"RATED G":     "G",
}

// ratingScore resolves an OfficialRating string to its parental score.
// Unknown or empty ratings return ok=false.
func ratingScore(rating string) (int, bool) {
	r := strings.ToUpper(strings.TrimSpace(rating))
	if r == "" {
		return 0, false
	}
	if mapped, ok := ratingAliases[r]; ok {
		r = mapped
	}
	if score, ok := parentalRatingScores[r]; ok {
		return score, true
	}
	// Some sources emit "Rated R" / "R (17+)" style strings; match a leading
	// "Rated " prefix or the leading token.
	if prefix, found := strings.CutPrefix(r, "RATED "); found {
		if score, ok := parentalRatingScores[strings.TrimSpace(prefix)]; ok {
			return score, true
		}
	}
	if i := strings.IndexAny(r, " ("); i > 0 {
		if score, ok := parentalRatingScores[strings.TrimSpace(r[:i])]; ok {
			return score, true
		}
	}
	return 0, false
}

// itemAllowedByPolicy reports whether an item passes the user's parental policy.
// maxRating <= 0 means no ceiling; blockUnrated hides items confirmed to carry
// no rating (plan §8.3). A non-empty CustomRating overrides OfficialRating, and
// an unrecognisable override never inherits the OfficialRating's lower score:
// every configured policy refuses it (the per-request path answers the same way
// in decideRating); this synchronous form has no lookup budget, so a missing
// value keeps the unrated convention.
func itemAllowedByPolicy(item map[string]any, maxRating int, blockUnrated bool) bool {
	score, presence := classifyItemRating(item)
	if presence == ratingMissing {
		return !blockUnrated
	}
	return decideRating(score, presence, maxRating, blockUnrated) == parentalAllow
}

// itemAllowedByRating is the rating-only form (blockUnrated = false), kept for
// callers and tests that do not carry the per-user block-unrated flag.
func itemAllowedByRating(item map[string]any, maxRating int) bool {
	return itemAllowedByPolicy(item, maxRating, false)
}

// filterItemsByPolicy drops items that fail the parental policy.
func filterItemsByPolicy(items []map[string]any, maxRating int, blockUnrated bool) []map[string]any {
	if (maxRating <= 0 && !blockUnrated) || len(items) == 0 {
		return items
	}
	kept := make([]map[string]any, 0, len(items))
	for _, it := range items {
		if itemAllowedByPolicy(it, maxRating, blockUnrated) {
			kept = append(kept, it)
		}
	}
	return kept
}

// filterItemsByRating is the rating-only form (blockUnrated = false).
func filterItemsByRating(items []map[string]any, maxRating int) []map[string]any {
	return filterItemsByPolicy(items, maxRating, false)
}

// validParentalRatings is the fixed set of legal MaxParentalRating scores this
// application accepts (plan §8.2): the values the rule table actually uses. It
// is a score, not an age, so values like 6 or 13 are rejected on purpose.
var validParentalRatings = map[int]bool{
	0: true, 1: true, 3: true, 4: true, 5: true,
	7: true, 8: true, 9: true, 10: true, 15: true,
}

// validParentalRating reports whether score is one of the accepted ceilings.
func validParentalRating(score int) bool {
	return validParentalRatings[score]
}
