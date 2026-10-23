package backend

import (
	"net/http"
	"net/url"
)

// maxParentalInheritanceDepth bounds how many parent hops a Season/Episode may
// climb looking for a recognisable rating (plan §8.3: at most 3 parent nodes).
const maxParentalInheritanceDepth = 3

// itemRatingState resolves the parental score an item carries on its own,
// preferring a non-empty CustomRating over OfficialRating (the deliberate
// override rule in plan §8.3). ok is false when the value is empty or not
// recognisable, which is exactly the "unrated / unknown" state.
func itemRatingState(item map[string]any) (score int, ok bool) {
	score, presence := classifyItemRating(item)
	return score, presence == ratingPresent
}

// fetchItemForParentalCheck reads one item's rating-relevant fields from a single
// upstream. A failure here is a query failure (not "unrated"): the caller must
// treat it as unconfirmable rather than allowing playback.
func (a *App) fetchItemForParentalCheck(r *http.Request, client *UpstreamClient, originalID string) (map[string]any, error) {
	payload, err := client.RequestJSON(r.Context(), requestContextFrom(r.Context()), a.Identity,
		http.MethodGet, "/Users/"+client.clientUserID()+"/Items/"+originalID,
		url.Values{"Fields": []string{"OfficialRating,CustomRating,Type,SeriesId,SeasonId,ParentId"}}, nil)
	if err != nil {
		return nil, err
	}
	m, ok := payload.(map[string]any)
	if !ok {
		return nil, errParentalMetadataShape
	}
	return m, nil
}

// errParentalMetadataShape marks a decoded payload that was not an item object.
var errParentalMetadataShape = &parentalMetadataError{}

type parentalMetadataError struct{}

func (*parentalMetadataError) Error() string { return "parental check: unexpected item shape" }

// needsRatingInheritance reports whether an item should climb its parent chain
// when it carries no rating of its own. Only Season/Episode inherit; a Movie with
// no rating is genuinely unrated.
func needsRatingInheritance(item map[string]any) bool {
	typ, _ := item["Type"].(string)
	switch typ {
	case "Season", "Episode":
		return true
	}
	return false
}

// nextParentID picks the next ancestor to inspect for a Season/Episode. Order is
// SeasonId → ParentId → SeriesId, so an Episode reaches its Season before the
// Series. Already-visited ids are skipped, which breaks cycles.
func nextParentID(item map[string]any, visited map[string]bool) string {
	for _, key := range []string{"SeasonId", "ParentId", "SeriesId"} {
		if id, _ := item[key].(string); id != "" && !visited[id] {
			return id
		}
	}
	return ""
}

// inheritParentalScore walks the same-server parent chain (bounded depth) looking
// for a recognisable rating. It never crosses to another server's copy. A parent
// fetch failure is returned as an error so the caller can fail closed.
func (a *App) inheritParentalScore(r *http.Request, client *UpstreamClient, item map[string]any) (score int, ok bool, err error) {
	visited := map[string]bool{}
	if id, _ := item["Id"].(string); id != "" {
		visited[id] = true
	}
	cur := item
	for depth := 0; depth < maxParentalInheritanceDepth; depth++ {
		parentID := nextParentID(cur, visited)
		if parentID == "" {
			break
		}
		visited[parentID] = true
		parent, ferr := a.fetchItemForParentalCheck(r, client, parentID)
		if ferr != nil {
			return 0, false, ferr
		}
		s, presence := classifyItemRating(parent)
		if presence != ratingMissing {
			// Explicit unknown ratings are terminal, just like an item's own
			// override. Only absent ratings may climb to another ancestor.
			return s, presence == ratingPresent, nil
		}
		cur = parent
	}
	return 0, false, nil
}

// checkInstanceParentalAccess decides whether the current user may access one
// concrete upstream instance of an item. It reads the item on that instance,
// inherits a Season/Episode rating from its same-server parents when needed, and
// applies the user's policy. The second return value distinguishes a definite
// deny (confirmed) from a metadata query failure (not confirmed → caller must
// fail closed, e.g. 502). A request with no restriction short-circuits to allow.
func (a *App) checkInstanceParentalAccess(r *http.Request, client *UpstreamClient, originalID string) (allowed bool, confirmed bool) {
	s := a.parentalSessionFor(r)
	if !s.restricted() {
		return true, true
	}
	item, err := a.loadParentalMetadata(r, s, client, originalID)
	if err != nil {
		return false, false
	}
	decision := a.decideConfirmedItem(r, s, client, item)
	return decision == parentalAllow, decision != parentalUnconfirmed
}
