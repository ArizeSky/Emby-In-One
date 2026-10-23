package backend

import (
	"net/http"
	"strings"
)

const parentalConfirmBatchSize = 200
const parentalConfirmRequestBudget = 50

type ratingPresence int

const (
	ratingPresent ratingPresence = iota
	ratingMissing                // This may be a sparse DTO, so confirmation is still needed.
	ratingUnrecognized
	ratingUnrated // A complete lookup confirmed that the rating is absent.
)

func classifyItemRating(item map[string]any) (int, ratingPresence) {
	rating, _ := item["CustomRating"].(string)
	if strings.TrimSpace(rating) == "" {
		rating, _ = item["OfficialRating"].(string)
	}
	if strings.TrimSpace(rating) == "" {
		return 0, ratingMissing
	}
	score, ok := ratingScore(rating)
	if !ok {
		return 0, ratingUnrecognized
	}
	return score, ratingPresent
}

type parentalItemDecision int

const (
	parentalAllow parentalItemDecision = iota
	parentalDeny
	parentalUnconfirmed
)

func decideRating(score int, presence ratingPresence, maxRating int, blockUnrated bool) parentalItemDecision {
	switch presence {
	case ratingPresent:
		if maxRating > 0 && score > maxRating {
			return parentalDeny
		}
		return parentalAllow
	case ratingUnrecognized:
		// A value the policy table cannot interpret must not be re-read as a
		// lower OfficialRating: the whole point of an override value is that
		// the local system does not know how strict it is. Any configured
		// policy refuses it; only a no-policy user passes.
		if maxRating > 0 || blockUnrated {
			return parentalDeny
		}
		return parentalAllow
	case ratingUnrated:
		// A complete lookup confirmed that the item carries no rating: only a
		// block-unrated policy hides it, a mere ceiling does not.
		if blockUnrated {
			return parentalDeny
		}
		return parentalAllow
	default:
		return parentalUnconfirmed
	}
}

func isParentalNavigation(item map[string]any) bool {
	switch item["Type"] {
	case "Folder", "CollectionFolder", "UserView", "BoxSet", "Playlist", "Genre", "MusicGenre", "Studio", "Person", "MusicArtist", "MusicAlbum":
		return true
	}
	return false
}

type parentalTarget struct {
	item     map[string]any
	client   *UpstreamClient
	original string
}

// Callers which retain concrete ownership use parentalFilterServerItems;
// this entry point accepts DTOs whose IDs have already been rewritten.
func (a *App) parentalFilterItems(r *http.Request, items []map[string]any) []map[string]any {
	targets := make([]parentalTarget, len(items))
	for i, item := range items {
		targets[i].item = item
		id, _ := item["Id"].(string)
		if resolved := a.resolveRouteID(id); resolved != nil {
			targets[i].client, targets[i].original = resolved.Client, resolved.OriginalID
		}
	}
	return a.parentalFilterTargets(r, targets)
}

func (a *App) parentalFilterServerItems(r *http.Request, client *UpstreamClient, items []map[string]any) []map[string]any {
	targets := make([]parentalTarget, len(items))
	for i, item := range items {
		id, _ := item["Id"].(string)
		targets[i] = parentalTarget{item, client, id}
	}
	return a.parentalFilterTargets(r, targets)
}

func (a *App) parentalFilterTargets(r *http.Request, targets []parentalTarget) []map[string]any {
	s := a.parentalSessionFor(r)
	kept := make([]map[string]any, 0, len(targets))
	if s.restricted() {
		a.prefetchParentalTargets(r, s, targets)
	}
	for _, t := range targets {
		decision := parentalAllow
		if s.restricted() {
			decision = a.parentalTargetDecision(r, s, t)
			s.note(decision)
		}
		if decision == parentalAllow {
			kept = append(kept, t.item)
		}
	}
	return kept
}

func (a *App) parentalTargetDecision(r *http.Request, s *parentalReadSession, t parentalTarget) parentalItemDecision {
	if isParentalNavigation(t.item) {
		return parentalAllow
	}
	score, presence := classifyItemRating(t.item)
	if presence != ratingMissing {
		return decideRating(score, presence, s.maxRating, s.blockUnrated)
	}
	if t.client == nil || t.original == "" {
		return parentalUnconfirmed
	}
	meta, err := a.loadParentalMetadata(r, s, t.client, t.original)
	if err != nil {
		return parentalUnconfirmed
	}
	return a.decideConfirmedItem(r, s, t.client, meta)
}

func (a *App) decideConfirmedItem(r *http.Request, s *parentalReadSession, client *UpstreamClient, meta map[string]any) parentalItemDecision {
	if isParentalNavigation(meta) {
		return parentalAllow
	}
	score, presence := classifyItemRating(meta)
	if presence == ratingMissing {
		presence = ratingUnrated
		if needsRatingInheritance(meta) {
			var err error
			score, presence, err = a.inheritParentalScoreBudgeted(r, s, client, meta)
			if err != nil {
				return parentalUnconfirmed
			}
		}
	}
	return decideRating(score, presence, s.maxRating, s.blockUnrated)
}

func (a *App) inheritParentalScoreBudgeted(r *http.Request, s *parentalReadSession, client *UpstreamClient, item map[string]any) (int, ratingPresence, error) {
	visited := map[string]bool{}
	if id, _ := item["Id"].(string); id != "" {
		visited[id] = true
	}
	for depth := 0; depth < maxParentalInheritanceDepth; depth++ {
		parentID := nextParentID(item, visited)
		if parentID == "" {
			break
		}
		visited[parentID] = true
		parent, err := a.loadParentalMetadata(r, s, client, parentID)
		if err != nil {
			return 0, ratingMissing, err
		}
		score, presence := classifyItemRating(parent)
		if presence != ratingMissing {
			return score, presence, nil
		}
		item = parent
	}
	return 0, ratingUnrated, nil
}

// Called before writing a list response. Query failure is not a confirmed empty
// library; successful safe partial results explicitly carry an incomplete flag.
func (a *App) parentalResponseReady(w http.ResponseWriter, r *http.Request, payload any) bool {
	s := a.parentalSessionFor(r)
	s.mu.Lock()
	allowed, unconfirmed := s.allowed, s.unconfirmed
	s.mu.Unlock()
	if unconfirmed == 0 {
		return true
	}
	if allowed == 0 {
		writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Cannot verify content rating"})
		return false
	}
	if m, ok := payload.(map[string]any); ok {
		markIncompleteResults(w, m)
	} else {
		markIncompleteResultsHeader(w)
	}
	return true
}

func (a *App) writeParentalItemsResponse(w http.ResponseWriter, r *http.Request, payload map[string]any) {
	if a.parentalResponseReady(w, r, payload) {
		if w.Header().Get("X-EIO-Results-Incomplete") == "true" {
			markIncompleteResults(w, payload)
		}
		writeJSON(w, http.StatusOK, payload)
	}
}

func (a *App) parentalFilterServerPayloadItems(r *http.Request, client *UpstreamClient, payload map[string]any) {
	for _, key := range []string{"Items", "SearchHints"} {
		if raw, ok := payload[key].([]any); ok {
			kept := a.parentalFilterServerItems(r, client, asItems(map[string]any{"Items": raw}))
			payload[key] = toAnySlice(kept)
			if a.parentalSessionFor(r).restricted() {
				payload["TotalRecordCount"] = len(kept)
			}
		}
	}
	for _, key := range []string{"ThemeVideosResult", "ThemeSongsResult", "SoundtrackSongsResult"} {
		if nested, ok := payload[key].(map[string]any); ok {
			a.parentalFilterServerPayloadItems(r, client, nested)
		}
	}
}

func (a *App) parentalDetailAllowed(w http.ResponseWriter, r *http.Request, client *UpstreamClient, originalID string) bool {
	allowed, confirmed := a.checkInstanceParentalAccess(r, client, originalID)
	if allowed {
		return true
	}
	if !confirmed {
		writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Cannot verify content rating"})
	} else {
		writeJSON(w, http.StatusForbidden, map[string]any{"message": "Content restricted by parental rating"})
	}
	return false
}
