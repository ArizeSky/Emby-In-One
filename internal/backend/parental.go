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

// itemAllowedByRating reports whether an item is within maxRating.
// maxRating <= 0 means "no restriction".
// Items without a recognisable rating are allowed (matches Emby's behaviour
// when BlockUnratedItems is empty, which is the default).
func itemAllowedByRating(item map[string]any, maxRating int) bool {
	if maxRating <= 0 {
		return true
	}
	rating, _ := item["OfficialRating"].(string)
	if rating == "" {
		rating, _ = item["CustomRating"].(string)
	}
	score, ok := ratingScore(rating)
	if !ok {
		return true
	}
	return score <= maxRating
}

// filterItemsByRating drops items whose OfficialRating exceeds maxRating.
// The input slice is returned unchanged when maxRating is 0.
func filterItemsByRating(items []map[string]any, maxRating int) []map[string]any {
	if maxRating <= 0 || len(items) == 0 {
		return items
	}
	kept := make([]map[string]any, 0, len(items))
	for _, it := range items {
		if itemAllowedByRating(it, maxRating) {
			kept = append(kept, it)
		}
	}
	return kept
}
