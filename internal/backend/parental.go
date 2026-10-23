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

// ratingScore resolves an OfficialRating string to its parental score.
// Unknown or empty ratings return ok=false.
func ratingScore(rating string) (int, bool) {
	r := strings.ToUpper(strings.TrimSpace(rating))
	if r == "" {
		return 0, false
	}
	if score, ok := parentalRatingScores[r]; ok {
		return score, true
	}
	// Some sources emit "R (17+)" style strings; match the leading token.
	if i := strings.IndexAny(r, " ("); i > 0 {
		if score, ok := parentalRatingScores[strings.TrimSpace(r[:i])]; ok {
			return score, true
		}
	}
	return 0, false
}

// itemAllowedByPolicy reports whether an item passes the user's parental policy.
// maxRating <= 0 means no ceiling; blockUnrated hides items with no recognisable
// rating (plan §8.3). A non-empty CustomRating overrides OfficialRating.
func itemAllowedByPolicy(item map[string]any, maxRating int, blockUnrated bool) bool {
	rating, _ := item["CustomRating"].(string)
	if strings.TrimSpace(rating) == "" {
		rating, _ = item["OfficialRating"].(string)
	}
	score, ok := ratingScore(rating)
	if !ok {
		// Unrated/unknown: blocked only when the user asked to block unrated items.
		return !blockUnrated
	}
	if maxRating > 0 && score > maxRating {
		return false
	}
	return true
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
