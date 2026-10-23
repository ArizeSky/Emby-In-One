package backend

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func (a *App) writeMergedParentalResponse(w http.ResponseWriter, r *http.Request, results []upstreamItemsResult, incomplete bool) {
	results = a.parentalVisibleResults(r, results)
	items := a.mergeRoundRobinItems(results, a.clientFacingUserIDFor(r))
	if err := validateCatalogSort(r.URL.Query()); err != nil {
		writeCatalogSortError(w, err)
		return
	}
	if strings.TrimSpace(firstQueryValue(r.URL.Query(), "SortBy", "sortBy")) != "" {
		if err := sortCatalogItems(items, r.URL.Query()); err != nil {
			writeCatalogSortError(w, err)
			return
		}
	} else if strings.Contains(strings.ToLower(r.URL.Path), "/resume") {
		sortResumeItemsByLastPlayed(items)
	}
	payload := paginateItems(items, r.URL.Query())
	if incomplete {
		markIncompleteResults(w, payload)
	}
	if !a.strictSanitizedUserDataItems(w, r, asItems(payload)) {
		return
	}
	a.writeParentalItemsResponse(w, r, payload)
}

func (a *App) handleRestrictedContainerItems(w http.ResponseWriter, r *http.Request, resolved *routeResolution, query url.Values, filter userStateFilter, localFilter bool) {
	requestMergedCandidateSet(query)
	// The shared scanner performs the one translation from the original virtual IDs.
	if genre := firstQueryValue(r.URL.Query(), "GenreIds", "genreIds", "genreids"); genre != "" {
		query.Set("GenreIds", genre)
	}
	s := a.parentalSessionFor(r)
	rows, complete, truncated, err := a.fetchMergedItemsFromUpstream(r.Context(), requestContextFrom(r.Context()), resolved.Client, "/Users/%s/Items", query, nil, s.catalog)
	if err != nil || (len(rows) == 0 && (!complete || truncated)) {
		writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Failed to load item candidates"})
		return
	}
	items := a.parentalFilterServerItems(r, resolved.Client, rows)
	a.rewriteItems(items, resolved.ServerID, a.clientFacingUserIDFor(r))
	if localFilter {
		var recency map[string]int64
		items, recency = a.filterItemsByLocalUserState(r, items, filter)
		localItemSort(items, r.URL.Query(), recency)
	}
	if !a.strictSanitizedUserDataItems(w, r, items) {
		return
	}
	payload := paginateItems(items, r.URL.Query())
	if !complete || truncated {
		markIncompleteResults(w, payload)
	}
	a.writeParentalItemsResponse(w, r, payload)
}

func (a *App) recordShowDisplaySource(item map[string]any, virtualID, serverID string) {
	originalID, _ := item["_originalId"].(string)
	a.IDStore.SetDisplaySource(virtualID, serverID, originalID)
	recordImageSourceTags(a.IDStore, virtualID, serverID, originalID, item)
	if tag, ok := primaryImageTag(item); ok {
		a.IDStore.SetImageSource(virtualID, "Primary", tag, serverID, originalID)
	}
}

// Identity registration includes denied copies; only visible metadata is filtered.
// This preserves the original virtual ID when the first copy is restricted.
func (a *App) registerShowIdentities(rows []map[string]any, serverID string, episodes bool, identities map[string]string) {
	for _, item := range rows {
		index, ok := numericInt(item["IndexNumber"])
		id, _ := item["Id"].(string)
		if !ok || id == "" {
			continue
		}
		key := strconv.Itoa(index)
		if episodes {
			season, ok := numericInt(item["ParentIndexNumber"])
			if !ok {
				continue
			}
			key = strconv.Itoa(season) + ":" + key
		}
		if virtualID := identities[key]; virtualID != "" {
			a.IDStore.AssociateAdditionalInstance(virtualID, id, serverID)
		} else {
			identities[key] = a.IDStore.GetOrCreateVirtualID(id, serverID)
		}
	}
}

// Register identity facts before filtering, then build the visible metadata
// view only from allowed concrete copies. Never merge a denied DTO's fields
// and subsequently decide the permissions of the entire identity group.
func (a *App) parentalVisibleResults(r *http.Request, results []upstreamItemsResult) []upstreamItemsResult {
	if !a.parentalSessionFor(r).restricted() {
		return results
	}
	facts := make([]upstreamItemsResult, len(results))
	for i, result := range results {
		facts[i] = result
		facts[i].Items = make([]map[string]any, len(result.Items))
		for j, item := range result.Items {
			facts[i].Items[j] = deepCloneMap(item)
		}
	}
	a.mergeRoundRobinItems(facts, a.clientFacingUserIDFor(r))
	visible := make([]upstreamItemsResult, len(results))
	for i, result := range results {
		visible[i] = result
		visible[i].Items = a.parentalFilterServerItems(r, a.Upstream.ClientByID(result.ServerID), result.Items)
	}
	return visible
}
