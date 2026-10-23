package backend

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// maxRatingFor returns the requesting proxy user's parental ceiling.
// 0 means "no restriction" (also returned for admins and unknown users).
func (a *App) maxRatingFor(r *http.Request) int {
	if a.UserStore == nil {
		return 0
	}
	uid := a.clientFacingUserIDFor(r)
	if uid == "" {
		return 0
	}
	if u := a.UserStore.Get(uid); u != nil {
		return u.MaxParentalRating
	}
	return 0
}

// parentalPolicyFor returns the requesting user's ceiling and block-unrated flag.
// Both are zero/false for admins and unknown users (no restriction).
func (a *App) parentalPolicyFor(r *http.Request) (int, bool) {
	if a.UserStore == nil {
		return 0, false
	}
	uid := a.clientFacingUserIDFor(r)
	if uid == "" {
		return 0, false
	}
	if u := a.UserStore.Get(uid); u != nil {
		return u.MaxParentalRating, u.BlockUnratedItems
	}
	return 0, false
}

// parentalFilter drops items failing the user's parental policy.
func (a *App) parentalFilter(r *http.Request, items []map[string]any) []map[string]any {
	maxR, blockUnrated := a.parentalPolicyFor(r)
	return filterItemsByPolicy(items, maxR, blockUnrated)
}

// parentalAllow reports whether a single item passes the user's parental policy.
func (a *App) parentalAllow(r *http.Request, item map[string]any) bool {
	maxR, blockUnrated := a.parentalPolicyFor(r)
	return itemAllowedByPolicy(item, maxR, blockUnrated)
}

func (a *App) handleItemsCollection(w http.ResponseWriter, r *http.Request) {
	query := cloneValues(r.URL.Query())
	// Search queries are aggregated too, so the parental filter applies.
	// Only opaque pass-through requests fall back.
	if !hasBatchIDQuery(query) && strings.TrimSpace(query.Get("SearchTerm")) == "" {
		a.handleFallbackProxy(w, r)
		return
	}
	// The candidate set is the ID list the client sent, so a user-state filter can be
	// answered exactly here: no scan limit and no upstream paging to undo.
	filter, localFilter := a.localUserStateIntersect(w, r, query)
	reqCtx := requestContextFrom(r.Context())
	clients := a.allowedClients(reqCtx)
	if len(clients) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0})
		return
	}

	cfg := a.ConfigStore.Snapshot()
	globalTimeout := time.Duration(cfg.Timeouts.Global) * time.Millisecond
	if globalTimeout <= 0 {
		globalTimeout = 15 * time.Second
	}

	tasks := make([]upstreamTask, len(clients))
	for i, client := range clients {
		c := client
		tasks[i] = upstreamTask{
			index: i,
			fn: func(bgCtx context.Context) upstreamItemsResult {
				serverQuery, ok := translateBatchIDQueryForServer(query, c.ID, a.IDStore)
				if !ok {
					return upstreamItemsResult{Err: errBatchQueryNotTranslatable}
				}
				payload, err := c.RequestJSON(bgCtx, reqCtx, a.Identity, http.MethodGet, "/Items", serverQuery, nil)
				if err != nil {
					return upstreamItemsResult{Err: err}
				}
				return upstreamItemsResult{ServerID: c.ID, Items: asItems(payload), Complete: true}
			},
		}
	}

	collected, report := a.aggregateUpstreamsWithReport(r.Context(), aggregationConfig{
		gracePeriod:   time.Duration(cfg.Timeouts.SearchGracePeriod) * time.Millisecond,
		globalTimeout: globalTimeout,
	}, tasks)
	merged := a.mergedItemsPayload(collected, a.clientFacingUserIDFor(r))
	items := asItems(merged)
	items = a.parentalFilterItems(r, items)
	merged["Items"] = toAnySlice(items)
	merged["TotalRecordCount"] = len(items)
	if localFilter {
		kept, recency := a.filterItemsByLocalUserState(r, items, filter)
		localItemSort(kept, r.URL.Query(), recency)
		a.overlayLocalUserDataItems(r, kept)
		payload := map[string]any{
			"Items":            toAnySlice(kept),
			"TotalRecordCount": len(kept),
			"StartIndex":       0,
		}
		if report.Incomplete {
			markIncompleteResults(w, payload)
		}
		writeJSON(w, http.StatusOK, payload)
		return
	}
	a.overlayLocalUserDataItems(r, items)
	if report.Incomplete {
		markIncompleteResults(w, merged)
	}
	writeJSON(w, http.StatusOK, merged)
}

func (a *App) handleUserItems(w http.ResponseWriter, r *http.Request) {
	query := cloneValues(r.URL.Query())
	parentID := firstQueryValue(query, "ParentId", "parentId", "parentid")
	if parentID != "" && parentID != "0" && parentID != "root" {
		resolved := a.resolveRouteID(parentID)
		if resolved == nil {
			writeJSON(w, http.StatusOK, map[string]any{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0})
			return
		}
		if !a.requireServerAccess(w, r, resolved) {
			return
		}
		query.Set("ParentId", resolved.OriginalID)
		query.Del("parentId")
		query.Del("parentid")
		// Drilling into a genre sends GenreIds=<virtual genre id>. The upstream has
		// no idea what that id is, silently ignores the filter and returns the whole
		// library, so every genre looked identical. Translate it per upstream and
		// answer empty when the id belongs to a different server.
		if translated, ok := a.translateTaxonomyIDs(firstQueryValue(query, "GenreIds", "genreIds", "genreids"), resolved.ServerID); ok {
			query.Set("GenreIds", translated)
			query.Del("genreIds")
			query.Del("genreids")
		} else if firstQueryValue(query, "GenreIds", "genreIds", "genreids") != "" {
			writeJSON(w, http.StatusOK, map[string]any{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0})
			return
		}
		// A user-state filter is answered from the local record: the upstream still
		// scopes the query to the requested container, but it no longer decides which
		// of that container's items the user sees.
		filter, localFilter := a.prepareLocalUserFilter(w, r, query)
		payload, err := resolved.Client.RequestJSON(r.Context(), requestContextFrom(r.Context()), a.Identity, http.MethodGet, "/Users/"+resolved.Client.clientUserID()+"/Items", query, nil)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"message": err.Error()})
			return
		}
		items := a.rewriteItems(asItems(payload), resolved.ServerID, a.clientFacingUserIDFor(r))
		// Sparse list DTOs may omit the rating fields entirely; the unified
		// filter confirms missing ratings (with parent inheritance) instead of
		// reading "absent" as "unrated" (plan §8.3/§8.4).
		items = a.parentalFilterItems(r, items)
		if m, ok := payload.(map[string]any); ok {
			m["Items"] = toAnySlice(items)
			m["TotalRecordCount"] = len(items)
		}
		if localFilter {
			kept, recency := a.filterItemsByLocalUserState(r, items, filter)
			localItemSort(kept, r.URL.Query(), recency)
			a.overlayLocalUserDataItems(r, kept)
			writeJSON(w, http.StatusOK, paginateItems(kept, r.URL.Query()))
			return
		}
		a.overlayLocalUserDataItems(r, items)
		writeJSON(w, http.StatusOK, payload)
		return
	}
	filter, localFilter := a.prepareLocalUserFilter(w, r, query)
	if !localFilter {
		requestMergedCandidateSet(query)
	}
	results, incomplete := a.fetchItemsAcrossUpstreams(r.Context(), requestContextFrom(r.Context()), "/Users/%s/Items", query, nil)
	// Root-level listings include the library views themselves; some clients
	// browse the root through this endpoint instead of /Users/{id}/Views, so
	// hidden libraries must be dropped here too. Content items are never
	// touched — dropHiddenLibraryViews matches on the library item types only.
	if hidden := a.hiddenLibrariesFor(requestContextFrom(r.Context())); len(hidden) > 0 {
		dropHiddenLibraryViews(results, hidden)
	}
	merged := a.mergedItemsPayload(results, a.clientFacingUserIDFor(r))
	if items, ok := merged["Items"].([]any); ok {
		asMaps := make([]map[string]any, 0, len(items))
		for _, item := range items {
			if m, ok := item.(map[string]any); ok {
				asMaps = append(asMaps, m)
			}
		}
		// Root-level listings must honor the user's parental ceiling too: a
		// restricted user must not see an over-ceiling item just because they
		// browsed the root instead of a library (plan §8.4). Sparse DTOs are
		// confirmed through the unified filter, not treated as unrated.
		asMaps = a.parentalFilterItems(r, asMaps)
		if localFilter {
			kept, recency := a.filterItemsByLocalUserState(r, asMaps, filter)
			localItemSort(kept, r.URL.Query(), recency)
			a.overlayLocalUserDataItems(r, kept)
			a.writeItemsResponse(w, paginateItems(kept, r.URL.Query()), incomplete)
			return
		}
		if len(asMaps) >= mergedItemsScanLimit {
			a.warnTruncatedMerge(len(asMaps))
		}
		a.overlayLocalUserDataItems(r, asMaps)
		a.writeItemsResponse(w, paginateItems(asMaps, r.URL.Query()), incomplete)
		return
	}
	a.writeItemsResponse(w, merged, incomplete)
}

func (a *App) handleUserItemsLatest(w http.ResponseWriter, r *http.Request) {
	query := cloneValues(r.URL.Query())
	parentID := query.Get("ParentId")
	if parentID != "" {
		resolved := a.resolveRouteID(parentID)
		if resolved == nil {
			writeJSON(w, http.StatusOK, []any{})
			return
		}
		if !a.requireServerAccess(w, r, resolved) {
			return
		}
		query.Set("ParentId", resolved.OriginalID)
		query.Set("UserId", resolved.Client.clientUserID())
		payload, err := resolved.Client.RequestJSON(r.Context(), requestContextFrom(r.Context()), a.Identity, http.MethodGet, "/Users/"+resolved.Client.clientUserID()+"/Items/Latest", query, nil)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"message": err.Error()})
			return
		}
		items := asItems(payload)
		a.rewriteItems(items, resolved.ServerID, a.clientFacingUserIDFor(r))
		a.overlayLocalUserDataItems(r, items)
		items = a.parentalFilterItems(r, items)
		writeJSON(w, http.StatusOK, items)
		return
	}
	reqCtx := requestContextFrom(r.Context())
	clients := a.allowedClients(reqCtx)
	cfg := a.ConfigStore.Snapshot()
	globalTimeout := time.Duration(cfg.Timeouts.Global) * time.Millisecond
	if globalTimeout <= 0 {
		globalTimeout = 15 * time.Second
	}

	tasks := make([]upstreamTask, len(clients))
	for i, client := range clients {
		c := client
		tasks[i] = upstreamTask{
			index: i,
			fn: func(bgCtx context.Context) upstreamItemsResult {
				instQuery := cloneValues(query)
				instQuery.Set("UserId", c.clientUserID())
				payload, err := c.RequestJSON(bgCtx, reqCtx, a.Identity, http.MethodGet, "/Users/"+c.clientUserID()+"/Items/Latest", instQuery, nil)
				if err != nil {
					return upstreamItemsResult{Err: err, Complete: false}
				}
				items := asItems(payload)
				a.rewriteItems(items, c.ID, a.clientFacingUserIDFor(r))
				return upstreamItemsResult{ServerID: c.ID, Items: items, Complete: true}
			},
		}
	}

	collected, report := a.aggregateUpstreamsWithReport(r.Context(), aggregationConfig{
		gracePeriod:   time.Duration(cfg.Timeouts.LatestGracePeriod) * time.Millisecond,
		globalTimeout: globalTimeout,
	}, tasks)
	allItems := make([]map[string]any, 0)
	for _, result := range collected {
		allItems = append(allItems, result.Items...)
	}
	allItems = a.parentalFilterItems(r, allItems)
	a.overlayLocalUserDataItems(r, allItems)
	if report.Incomplete {
		markIncompleteResultsHeader(w)
	}
	writeJSON(w, http.StatusOK, allItems)
}

func (a *App) handleUserItemByID(w http.ResponseWriter, r *http.Request) {
	resolved := a.resolveRouteID(r.PathValue("itemId"))
	if resolved == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Item not found"})
		return
	}
	reqCtx := requestContextFrom(r.Context())
	if !a.requireServerAccess(w, r, resolved) {
		return
	}
	instances := a.collectAllowedInstances(reqCtx, resolved)
	if len(instances) <= 1 {
		query := cloneValues(r.URL.Query())
		query.Set("UserId", resolved.Client.clientUserID())
		payload, err := resolved.Client.RequestJSON(r.Context(), requestContextFrom(r.Context()), a.Identity, http.MethodGet, "/Users/"+resolved.Client.clientUserID()+"/Items/"+resolved.OriginalID, query, nil)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"message": err.Error()})
			return
		}
		if item, ok := payload.(map[string]any); ok {
			a.discoverItemCopies(r, r.PathValue("itemId"), item)
			instances = a.collectAllowedInstances(reqCtx, a.resolveRouteID(r.PathValue("itemId")))
			if len(instances) > 1 {
				a.handleMergedItemDetail(w, r, instances)
				return
			}
		}
		cfg := a.ConfigStore.Snapshot()
		annotateItemSources(payload, resolved.Client)
		rewriteResponseIDs(payload, resolved.ServerID, a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
		a.overlayLocalUserData(r, r.PathValue("itemId"), payload)
		// The detail DTO can be sparse (an Episode carries no rating of its
		// own); check the owning instance with parent inheritance instead of
		// trusting the response fields (plan §8.3/§8.5).
		if !a.parentalDetailAllowed(w, r, resolved.Client, resolved.OriginalID) {
			return
		}
		writeJSON(w, http.StatusOK, payload)
		return
	}

	a.handleMergedItemDetail(w, r, instances)
}

func (a *App) handleItemByID(w http.ResponseWriter, r *http.Request) {
	resolved := a.resolveRouteID(r.PathValue("itemId"))
	if resolved == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Item not found"})
		return
	}
	if !a.requireServerAccess(w, r, resolved) {
		return
	}
	payload, err := resolved.Client.RequestJSON(r.Context(), requestContextFrom(r.Context()), a.Identity, http.MethodGet, "/Items/"+resolved.OriginalID, cloneValues(r.URL.Query()), nil)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"message": err.Error()})
		return
	}
	cfg := a.ConfigStore.Snapshot()
	rewriteResponseIDs(payload, resolved.ServerID, a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
	a.overlayLocalUserData(r, r.PathValue("itemId"), payload)
	if !a.parentalDetailAllowed(w, r, resolved.Client, resolved.OriginalID) {
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (a *App) handleItemSimilar(w http.ResponseWriter, r *http.Request) {
	resolved := a.resolveRouteID(r.PathValue("itemId"))
	if resolved == nil {
		writeJSON(w, http.StatusOK, map[string]any{"Items": []any{}, "TotalRecordCount": 0})
		return
	}
	if !a.requireServerAccess(w, r, resolved) {
		return
	}
	query := cloneValues(r.URL.Query())
	query.Set("UserId", resolved.Client.clientUserID())
	payload, err := resolved.Client.RequestJSON(r.Context(), requestContextFrom(r.Context()), a.Identity, http.MethodGet, "/Items/"+resolved.OriginalID+"/Similar", query, nil)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"message": err.Error()})
		return
	}
	cfg := a.ConfigStore.Snapshot()
	rewriteResponseIDs(payload, resolved.ServerID, a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
	// The recommendation list is content: filter it through the unified
	// parental read path before it reaches the client (plan §8.4).
	if m, ok := payload.(map[string]any); ok {
		a.parentalFilterPayloadItems(r, m)
	}
	a.overlayLocalUserDataItems(r, asItems(payload))
	writeJSON(w, http.StatusOK, payload)
}

func (a *App) handleItemThemeMedia(w http.ResponseWriter, r *http.Request) {
	resolved := a.resolveRouteID(r.PathValue("itemId"))
	if resolved == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ThemeVideosResult":     map[string]any{"Items": []any{}, "TotalRecordCount": 0},
			"ThemeSongsResult":      map[string]any{"Items": []any{}, "TotalRecordCount": 0},
			"SoundtrackSongsResult": map[string]any{"Items": []any{}, "TotalRecordCount": 0},
		})
		return
	}
	if !a.requireServerAccess(w, r, resolved) {
		return
	}
	query := cloneValues(r.URL.Query())
	query.Set("UserId", resolved.Client.clientUserID())
	payload, err := resolved.Client.RequestJSON(r.Context(), requestContextFrom(r.Context()), a.Identity, http.MethodGet, "/Items/"+resolved.OriginalID+"/ThemeMedia", query, nil)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"message": err.Error()})
		return
	}
	cfg := a.ConfigStore.Snapshot()
	rewriteResponseIDs(payload, resolved.ServerID, a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
	// Theme videos/songs are content items nested under three result keys;
	// every nested list is filtered before the payload is returned (plan §8.4).
	if m, ok := payload.(map[string]any); ok {
		a.parentalFilterPayloadItems(r, m)
	}
	writeJSON(w, http.StatusOK, payload)
}

func (a *App) fetchItemsAcrossUpstreams(ctx context.Context, reqCtx *RequestContext, pathTemplate string, query url.Values, body any) ([]upstreamItemsResult, bool) {
	clients := a.allowedClients(reqCtx)
	if len(clients) == 0 {
		return nil, false
	}
	cfg := a.ConfigStore.Snapshot()
	globalTimeout := time.Duration(cfg.Timeouts.Global) * time.Millisecond
	if globalTimeout <= 0 {
		globalTimeout = 15 * time.Second
	}

	// One shared request budget for the whole catalog query (plan §6.2): every
	// page request against every server draws from it, so a large multi-server
	// library cannot fan out into unbounded upstream traffic.
	budget := newCatalogRequestBudget()

	tasks := make([]upstreamTask, len(clients))
	for i, client := range clients {
		c := client
		tasks[i] = upstreamTask{
			index: i,
			fn: func(bgCtx context.Context) upstreamItemsResult {
				items, complete, truncated, err := a.fetchMergedItemsFromUpstream(bgCtx, reqCtx, c, pathTemplate, query, body, budget)
				if errors.Is(err, errTaxonomyQueryNotTranslatable) {
					// The filter names a genre this server cannot translate; skip it
					// rather than querying the whole library unfiltered. Not a failure.
					return upstreamItemsResult{ServerID: c.ID, Complete: true}
				}
				if err != nil {
					return upstreamItemsResult{Err: err, Complete: false}
				}
				return upstreamItemsResult{ServerID: c.ID, Items: items, Complete: complete, Truncated: truncated}
			},
		}
	}

	grace := time.Duration(cfg.Timeouts.SearchGracePeriod) * time.Millisecond
	// A pagedScan upstream answers in several sequential pages; an early grace
	// return would hand back only the fast servers' first page and silently drop
	// the paging server's tail (plan §6.2). Wait for every task (bounded by the
	// global timeout) whenever any participating upstream pages.
	for _, client := range clients {
		if client.Config.PagedScan {
			grace = 0
			break
		}
	}
	results, report := a.aggregateUpstreamsWithReport(ctx, aggregationConfig{
		gracePeriod:   grace,
		globalTimeout: globalTimeout,
	}, tasks)
	return results, report.Incomplete
}

// getItemKey generates a deduplication key for an item.
// Returns empty string if the item cannot be deduplicated.
func getItemKey(item map[string]any) string {
	// Priority 1: TMDB ID
	if providerIDs, ok := item["ProviderIds"].(map[string]any); ok {
		if tmdb, ok := providerIDs["Tmdb"].(string); ok && tmdb != "" {
			return "tmdb:" + tmdb
		}
	}
	// Priority 2: Movie/Series by Name + Year
	itemType, _ := item["Type"].(string)
	if itemType == "Movie" || itemType == "Series" {
		name, _ := item["Name"].(string)
		year := ""
		if y, ok := numericInt(item["ProductionYear"]); ok {
			year = strconv.Itoa(y)
		}
		return "name:" + strings.ToLower(name) + ":" + year
	}
	// Priority 3: Episode by SeriesName + Season:Episode
	if itemType == "Episode" {
		seriesName, _ := item["SeriesName"].(string)
		parentIdx, okP := numericInt(item["ParentIndexNumber"])
		idx, okE := numericInt(item["IndexNumber"])
		if seriesName != "" && okP && okE {
			return "ep:" + strings.ToLower(seriesName) + ":S" + strconv.Itoa(parentIdx) + "E" + strconv.Itoa(idx)
		}
	}
	return ""
}

// dedupFacts carries the attributes a Movie/Series match is allowed on. Servers
// disagree about metadata coverage: the same title comes back from one upstream
// with a Tmdb id and from another without one, or one side has a year and the
// other does not. Strict key equality (getItemKey) misses those pairs — a copy
// keyed "tmdb:535167" can never collide with its own "name:流浪地球:2019" twin —
// so the merger matches on facts instead and only refuses clearly different items.
type dedupFacts struct {
	tmdb string
	name string // lower-cased
	year string // "" when the upstream did not send one
	typ  string // "Movie" or "Series"
}

func dedupFactsOf(item map[string]any) dedupFacts {
	f := dedupFacts{}
	f.typ, _ = item["Type"].(string)
	f.name, _ = item["Name"].(string)
	f.name = strings.ToLower(f.name)
	if y, ok := numericInt(item["ProductionYear"]); ok {
		f.year = strconv.Itoa(y)
	}
	if providerIDs, ok := item["ProviderIds"].(map[string]any); ok {
		f.tmdb, _ = providerIDs["Tmdb"].(string)
	}
	return f
}

// movieSeriesMatch reports whether candidate and existing plausibly describe the
// same title. Distinct non-empty Tmdb ids always mean different titles; otherwise
// the name must agree and the years must not contradict (a missing year on either
// side keeps the match possible — that asymmetry is exactly what broke merging).
func movieSeriesMatch(a, b dedupFacts) bool {
	if a.tmdb != "" && b.tmdb != "" && a.tmdb != b.tmdb {
		return false
	}
	if a.name == "" || a.name != b.name {
		return false
	}
	if a.typ != b.typ {
		return false
	}
	return a.year == "" || b.year == "" || a.year == b.year
}

// containsChinese checks if a string contains CJK Unified Ideographs (U+4E00–U+9FA5).
func containsChinese(s string) bool {
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FA5 {
			return true
		}
	}
	return false
}

// isBetterMetadata returns true if the candidate item from candidateServerID has
// better metadata than the existing item from existingServerID, using a 5-level
// priority: priorityMetadata flag → Primary image present → Chinese in Overview
// → longer Overview → earlier upstream order.
func isBetterMetadata(existing map[string]any, existingServerID string, candidate map[string]any, candidateServerID string, cfg Config) bool {
	// 1. priorityMetadata flag
	existingPriority := false
	candidatePriority := false
	existingOrder := len(cfg.Upstream)
	candidateOrder := len(cfg.Upstream)
	for idx, u := range cfg.Upstream {
		if u.ID == existingServerID {
			existingPriority = u.PriorityMetadata
			existingOrder = idx
		}
		if u.ID == candidateServerID {
			candidatePriority = u.PriorityMetadata
			candidateOrder = idx
		}
	}
	if candidatePriority && !existingPriority {
		return true
	}
	if existingPriority && !candidatePriority {
		return false
	}

	// 2. Chinese in Overview. Four-level rule (plan §6.4): priorityMetadata →
	// Chinese Overview → longer Overview → server order. A poster never reorders
	// the metadata choice; a missing Primary image is filled separately below.
	existingOverview, _ := existing["Overview"].(string)
	candidateOverview, _ := candidate["Overview"].(string)
	hasChinese1 := containsChinese(existingOverview)
	hasChinese2 := containsChinese(candidateOverview)
	if hasChinese2 && !hasChinese1 {
		return true
	}
	if hasChinese1 && !hasChinese2 {
		return false
	}

	// 3. Longer Overview
	if len(candidateOverview) > len(existingOverview) {
		return true
	}
	if len(existingOverview) > len(candidateOverview) {
		return false
	}

	// 4. Lower server index (order in cfg.Upstream)
	return candidateOrder < existingOrder
}

// hasPrimaryImage reports whether the item carries a Primary image tag, the
// marker Emby sets for a poster. Both the string-hash form and the boolean form
// appear in the wild, so accept either.
func hasPrimaryImage(item map[string]any) bool {
	tags, ok := item["ImageTags"].(map[string]any)
	if !ok {
		return false
	}
	switch v := tags["Primary"].(type) {
	case string:
		return v != ""
	case bool:
		return v
	}
	return false
}

func (a *App) mergedItemsPayload(results []upstreamItemsResult, clientUserID string) map[string]any {
	merged := a.mergeRoundRobinItems(results, clientUserID)
	return map[string]any{
		"Items":            toAnySlice(merged),
		"TotalRecordCount": len(merged),
		"StartIndex":       0,
	}
}

// mergeRoundRobinItems interleaves items from every server in turn, deduplicating
// Movies/Series by fact-compatible match (tmdb id when both sides carry one, name
// with non-contradicting year otherwise) and Episodes by series+season+episode:
// the first copy keeps the virtual id and later copies are registered as
// additional instances of it, replacing the display item only when their metadata
// is better.
func (a *App) mergeRoundRobinItems(results []upstreamItemsResult, clientUserID string) []map[string]any {
	cfg := a.ConfigStore.Snapshot()
	merged := make([]map[string]any, 0)
	type seenEntry struct {
		virtualID   string
		mergedIndex int // position in merged slice
		serverID    string
		originalID  string // this entry's own original id on serverID
		facts       dedupFacts
	}
	// Buckets for O(1) lookup: by tmdb id and by lower-cased name. Name buckets
	// hold every candidate with that name; year/type compatibility is checked per
	// entry because a bare name is not unique across a whole library.
	seenTmdb := map[string]*seenEntry{}
	seenName := map[string][]*seenEntry{}
	seenEpisode := map[string]*seenEntry{}
	var entries []*seenEntry

	maxLen := 0
	for _, result := range results {
		if len(result.Items) > maxLen {
			maxLen = len(result.Items)
		}
	}

	for i := 0; i < maxLen; i++ {
		for _, result := range results {
			if i >= len(result.Items) {
				continue
			}
			item := result.Items[i]
			originalID, _ := item["Id"].(string)
			if originalID == "" {
				rewriteResponseIDs(item, result.ServerID, a.IDStore, cfg.Server.ID, clientUserID)
				merged = append(merged, item)
				continue
			}

			itemType, _ := item["Type"].(string)
			var entry *seenEntry
			var episodeKey string
			facts := dedupFactsOf(item)
			switch itemType {
			case "Movie", "Series":
				if facts.tmdb != "" {
					entry = seenTmdb[tmdbBucketKey(facts)]
				}
				if entry == nil {
					for _, e := range seenName[facts.name] {
						if movieSeriesMatch(facts, e.facts) {
							entry = e
							break
						}
					}
				}
			case "Episode":
				episodeKey = getItemKey(item)
				if episodeKey != "" {
					entry = seenEpisode[episodeKey]
				}
			}

			if entry != nil {
				// Duplicate: associate as an additional instance of the same item.
				a.IDStore.AssociateAdditionalInstance(entry.virtualID, originalID, result.ServerID)
				base := merged[entry.mergedIndex]
				baseHasImage := hasPrimaryImage(base)
				candidateHasImage := hasPrimaryImage(item)
				oldServerID := entry.serverID
				oldOriginalID := entry.originalID
				if isBetterMetadata(base, entry.serverID, item, result.ServerID, cfg) {
					delete(item, "Id")
					rewriteResponseIDs(item, result.ServerID, a.IDStore, cfg.Server.ID, clientUserID)
					item["Id"] = entry.virtualID
					merged[entry.mergedIndex] = item
					entry.serverID = result.ServerID
					entry.originalID = originalID
					// The new base lost a poster the old base had: keep the new
					// (better) metadata but carry the poster over (plan §6.4.1).
					if !candidateHasImage && baseHasImage {
						if tag, ok := primaryImageTag(base); ok {
							copyPrimaryImageFields(item, base)
							a.IDStore.SetImageSource(entry.virtualID, "Primary", tag, oldServerID, oldOriginalID)
						}
					}
				} else if !baseHasImage && candidateHasImage {
					// Keep the richer-metadata base; fill only its missing Primary
					// image from this copy without switching any other field.
					if tag, ok := primaryImageTag(item); ok {
						copyPrimaryImageFields(base, item)
						a.IDStore.SetImageSource(entry.virtualID, "Primary", tag, result.ServerID, originalID)
					}
				}
				// Identity facts accumulate regardless of which copy is displayed
				// (plan §6.3): a known TMDB/year from any copy joins the group, so a
				// later conflicting id cannot be bridged through the group's blank
				// slot, and a poster-only fill never discards the group facts.
				if facts.tmdb != "" {
					if entry.facts.tmdb == "" {
						entry.facts.tmdb = facts.tmdb
					}
					if entry.facts.tmdb == facts.tmdb {
						seenTmdb[tmdbBucketKey(entry.facts)] = entry
					}
				}
				if entry.facts.year == "" && facts.year != "" {
					entry.facts.year = facts.year
				}
				continue
			}

			// First occurrence: keep the virtual ID, rewrite everything else.
			virtualID := a.IDStore.GetOrCreateVirtualID(originalID, result.ServerID)
			entry = &seenEntry{virtualID: virtualID, mergedIndex: len(merged), serverID: result.ServerID, originalID: originalID, facts: facts}
			entries = append(entries, entry)
			switch itemType {
			case "Movie", "Series":
				if facts.tmdb != "" {
					seenTmdb[tmdbBucketKey(facts)] = entry
				}
				seenName[facts.name] = append(seenName[facts.name], entry)
			case "Episode":
				if episodeKey != "" {
					seenEpisode[episodeKey] = entry
				}
			}
			delete(item, "Id")
			rewriteResponseIDs(item, result.ServerID, a.IDStore, cfg.Server.ID, clientUserID)
			item["Id"] = virtualID
			merged = append(merged, item)
		}
	}

	// Record each group's display-winning instance so the image handler can route
	// the group's non-Primary image types (Backdrop/Logo/Thumb) to the server that
	// actually owns those tags, instead of the IDStore primary that may not
	// (plan §6.4.2). Every non-Primary tag the displayed DTO advertises is also
	// recorded per-tag: a later merge can change the display winner while clients
	// still hold this DTO's tags, and those tags must keep routing to the instance
	// that owns the bytes. Primary is skipped here because the poster carryover
	// above already records its exact owner.
	for _, entry := range entries {
		a.IDStore.SetDisplaySource(entry.virtualID, entry.serverID, entry.originalID)
		if entry.mergedIndex >= 0 && entry.mergedIndex < len(merged) {
			recordImageSourceTags(a.IDStore, entry.virtualID, entry.serverID, entry.originalID, merged[entry.mergedIndex])
		}
	}

	return merged
}

// tmdbBucketKey scopes a TMDB id to its media type so Movie 123 and Series 123
// never share a dedup bucket (plan §6.3). A missing type never reaches this path.
func tmdbBucketKey(facts dedupFacts) string {
	return "tmdb:" + facts.typ + ":" + facts.tmdb
}

// primaryImageTag returns the Primary poster hash when the item carries a usable
// string tag. A boolean-only marker has no hash and cannot form an image URL.
func primaryImageTag(item map[string]any) (string, bool) {
	tags, ok := item["ImageTags"].(map[string]any)
	if !ok {
		return "", false
	}
	if tag, ok := tags["Primary"].(string); ok && tag != "" {
		return tag, true
	}
	return "", false
}

// copyPrimaryImageFields copies only the Primary poster fields from src into dst,
// keeping dst's own other ImageTags entries and every other field untouched
// (plan §6.4.1: never overwrite the display record's identity/metadata).
func copyPrimaryImageFields(dst, src map[string]any) {
	srcTags, ok := src["ImageTags"].(map[string]any)
	if !ok {
		return
	}
	primary, ok := srcTags["Primary"].(string)
	if !ok || primary == "" {
		return
	}
	dstTags, ok := dst["ImageTags"].(map[string]any)
	if !ok {
		dstTags = map[string]any{}
		dst["ImageTags"] = dstTags
	}
	dstTags["Primary"] = primary
	if ar, ok := src["PrimaryImageAspectRatio"]; ok {
		dst["PrimaryImageAspectRatio"] = ar
	}
}

// recordImageSourceTags records per-tag ownership for every non-Primary image
// the displayed DTO advertises (plan §6.4.2). The Primary tag is managed
// explicitly by the poster carryover, which knows the exact owning instance, so
// it is skipped here. Backdrop tags arrive as an array and are recorded
// individually so an indexed request with a tag still routes to the owner.
func recordImageSourceTags(store *IDStore, virtualID, serverID, originalID string, item map[string]any) {
	if tags, ok := item["ImageTags"].(map[string]any); ok {
		for imageType, raw := range tags {
			tag, _ := raw.(string)
			if imageType == "" || imageType == "Primary" || tag == "" {
				continue
			}
			store.SetImageSource(virtualID, imageType, tag, serverID, originalID)
		}
	}
	if backdrops, ok := item["BackdropImageTags"].([]any); ok {
		for _, raw := range backdrops {
			tag, _ := raw.(string)
			if tag == "" {
				continue
			}
			store.SetImageSource(virtualID, "Backdrop", tag, serverID, originalID)
		}
	}
}

// mergedItemsScanLimit caps how many items one merged (no ParentId) item query may pull
// from each upstream.
const mergedItemsScanLimit = 5000

// upstreamPageLimitCeiling is the per-request item window used for an upstream marked
// pagedScan. A modified Emby deployment can answer a wider window with a bare 400
// instead of clamping it, which would make the whole merged query fail on that server
// and leave the client with the other upstreams only. Requests to such a server are
// therefore issued with this window, and the rest of the candidate set is pulled page
// by page.
const upstreamPageLimitCeiling = 200

// fallbackUpstreamPageLimit is the window used for a second attempt after a pagedScan
// upstream refused even the capped window. It is narrow enough for a server with a
// lower ceiling than the default to still answer instead of being dropped from the
// merge.
const fallbackUpstreamPageLimit = 100

// fetchMergedItemsFromUpstream collects one upstream's candidate set.
//
// By default the upstream is asked for the whole scan budget (mergedItemsScanLimit
// rows) in one request, which every stock Emby server accepts. An upstream with
// pagedScan set in its config is the exception: a modified Emby deployment answers a
// wider window with a bare 400, which would make every merged query fail on that
// server. Those are pulled with a per-request window capped at
// upstreamPageLimitCeiling, following StartIndex until the upstream runs out of rows
// or the scan budget is reached. An upstream that still refuses the capped window is
// retried once at fallbackUpstreamPageLimit, and pages already collected are kept, so
// a server with a lower ceiling is narrowed rather than dropped from the merge.
func (a *App) fetchMergedItemsFromUpstream(ctx context.Context, reqCtx *RequestContext, c *UpstreamClient, pathTemplate string, query url.Values, body any, budget *catalogRequestBudget) (items []map[string]any, complete bool, truncated bool, err error) {
	path := strings.Replace(pathTemplate, "%s", c.clientUserID(), 1)
	collected := make([]map[string]any, 0, upstreamPageLimitCeiling)

	buildQuery := func(startIndex, window int) (url.Values, error) {
		pageQuery := cloneValues(query)
		// Clients only ever hold EIO's virtual user ID, which no upstream knows.
		pageQuery.Set("UserId", c.clientUserID())
		if hasBatchIDQuery(pageQuery) {
			translated, ok := translateBatchIDQueryForServer(pageQuery, c.ID, a.IDStore)
			if !ok {
				return nil, errBatchQueryNotTranslatable
			}
			pageQuery = translated
		}
		// GenreIds drilled from a virtual genre must be translated per server. A
		// genre that belongs to another server is not a reason to query this one
		// without the filter (that would return the whole library), so it is skipped
		// as "not translatable for this server".
		if genre := firstQueryValue(pageQuery, "GenreIds", "genreIds", "genreids"); genre != "" {
			translated, ok := a.translateTaxonomyIDs(genre, c.ID)
			if !ok {
				return nil, errTaxonomyQueryNotTranslatable
			}
			pageQuery.Set("GenreIds", translated)
			pageQuery.Del("genreIds")
			pageQuery.Del("genreids")
		}
		pageQuery.Set("StartIndex", strconv.Itoa(startIndex))
		pageQuery.Set("Limit", strconv.Itoa(window))
		return pageQuery, nil
	}

	if !c.Config.PagedScan {
		// The default: one wide request, the way the upstream would be asked if the
		// proxy were not merging. Paging is opt-in per upstream for servers that
		// refuse a wide window.
		directQuery, qErr := buildQuery(0, mergedItemsScanLimit)
		if qErr != nil {
			return nil, false, false, qErr
		}
		if !budget.tryAcquire() {
			return nil, false, true, nil
		}
		payload, reqErr := c.RequestJSON(ctx, reqCtx, a.Identity, http.MethodGet, path, directQuery, body)
		if reqErr != nil {
			return nil, false, true, reqErr
		}
		items := asItems(payload)
		// A trusted TotalRecordCount reveals a truncated wide answer: the server
		// capped the page below the total (plan §6.2 item 6).
		if total, hasTotal := totalRecordCount(payload); hasTotal && len(items) < total {
			return items, false, true, nil
		}
		if len(items) >= mergedItemsScanLimit {
			return items, true, true, nil
		}
		return items, true, false, nil
	}

	effectiveCeiling := upstreamPageLimitCeiling
	offset := 0
	for len(collected) < mergedItemsScanLimit {
		window := effectiveCeiling
		if remaining := mergedItemsScanLimit - len(collected); remaining < window {
			window = remaining
		}
		pageQuery, qErr := buildQuery(offset, window)
		if qErr != nil {
			return nil, false, false, qErr
		}
		if !budget.tryAcquire() {
			// Budget exhausted: stop issuing requests and report the tail as
			// truncated rather than treating unqueried items as absent.
			return collected, false, true, nil
		}
		payload, reqErr := c.RequestJSON(ctx, reqCtx, a.Identity, http.MethodGet, path, pageQuery, body)
		if reqErr != nil {
			// A server with a ceiling below the default fails the very first page. One
			// retry with a smaller window tells "refused because the window was too
			// wide" apart from a real outage. Only a structured 400 triggers it.
			if len(collected) == 0 && window > fallbackUpstreamPageLimit && isBadRequestError(reqErr) {
				if !budget.tryAcquire() {
					return nil, false, true, nil
				}
				retryQuery, rqErr := buildQuery(0, fallbackUpstreamPageLimit)
				if rqErr != nil {
					return nil, false, false, rqErr
				}
				retryPayload, retryErr := c.RequestJSON(ctx, reqCtx, a.Identity, http.MethodGet, path, retryQuery, body)
				if retryErr != nil {
					return nil, false, true, reqErr
				}
				items := asItems(retryPayload)
				// The smaller window worked: keep it for the following pages instead
				// of going back to the ceiling and being refused again (plan §6.2).
				effectiveCeiling = fallbackUpstreamPageLimit
				if len(items) < fallbackUpstreamPageLimit {
					return items, true, false, nil
				}
				collected = append(collected, items...)
				offset = len(collected)
				continue
			}
			// Mid-scan failure: do not pretend the partial set is complete.
			return nil, false, true, reqErr
		}

		items := asItems(payload)
		total, hasTotal := totalRecordCount(payload)
		if len(items) == 0 {
			if hasTotal && offset < total {
				// A known remaining tail was not delivered: mark incomplete.
				return collected, false, true, nil
			}
			break
		}
		collected = append(collected, items...)
		offset = len(collected)
		if len(items) < window {
			if hasTotal && offset < total {
				// Short page but the upstream still owes rows; keep scanning.
				continue
			}
			break
		}
	}

	if len(collected) >= mergedItemsScanLimit {
		return collected, true, true, nil
	}
	return collected, true, false, nil
}

// requestMergedCandidateSet asks the upstreams for the candidate set instead of the
// client's page. Paging belongs to the proxy on this path: the answers are merged and
// deduplicated before the page is cut, so a window forwarded upstream would be cut
// twice — once there, once by paginateItems — and the merged total would be the page
// size rather than the library size, telling the client there is no page 2. The value
// is only a default for the shared query: fetchMergedItemsFromUpstream decides the
// per-request window itself, per upstream.
func requestMergedCandidateSet(query url.Values) {
	query.Set("StartIndex", "0")
	query.Set("Limit", strconv.Itoa(mergedItemsScanLimit))
}

// warnTruncatedMerge reports that the merged candidate set hit the scan cap, so the
// reported total is a lower bound and the tail of the library is unreachable.
func (a *App) warnTruncatedMerge(count int) {
	if a.Logger == nil || !a.noticeThrottle.allow("merged-scan") {
		return
	}
	a.Logger.Warnf("merged item query scanned %d upstream items (limit %d): the total and the tail of the list may be truncated",
		count, mergedItemsScanLimit)
}

func paginateItems(merged []map[string]any, query url.Values) map[string]any {
	totalCount := len(merged)
	startIndex := 0
	if s := query.Get("StartIndex"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v >= 0 {
			startIndex = v
		}
	}
	if startIndex > len(merged) {
		startIndex = len(merged)
	}
	merged = merged[startIndex:]
	if lim := query.Get("Limit"); lim != "" {
		if v, err := strconv.Atoi(lim); err == nil && v >= 0 && v < len(merged) {
			merged = merged[:v]
		}
	}
	return map[string]any{
		"Items":            toAnySlice(merged),
		"TotalRecordCount": totalCount,
		"StartIndex":       startIndex,
	}
}

func firstQueryValue(values url.Values, keys ...string) string {
	for _, key := range keys {
		if value := values.Get(key); value != "" {
			return value
		}
	}
	return ""
}

// translateTaxonomyIDs rewrites a comma-separated list of virtual taxonomy ids
// (genres/studios/persons) into the ids the given upstream understands. It
// reports false when any id is unknown or belongs to another server, so the
// caller can return an empty result instead of an unfiltered library.
func (a *App) translateTaxonomyIDs(raw, serverID string) (string, bool) {
	if strings.TrimSpace(raw) == "" {
		return "", false
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		id := strings.TrimSpace(part)
		if id == "" {
			continue
		}
		resolved := a.IDStore.ResolveVirtualID(id)
		if resolved == nil || resolved.ServerID != serverID {
			return "", false
		}
		out = append(out, resolved.OriginalID)
	}
	if len(out) == 0 {
		return "", false
	}
	return strings.Join(out, ","), true
}
