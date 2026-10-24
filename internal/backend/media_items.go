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

var errBatchQueryNoItems = errors.New("batch query has no IDs for this upstream")

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
	if rc := requestContextFrom(r.Context()); rc != nil && rc.Parental != nil {
		return rc.Parental.maxRating, rc.Parental.blockUnrated
	}
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
	if catalogHasRandomSort(query) {
		if err := validateCatalogSort(query); err != nil {
			writeCatalogSortError(w, err)
			return
		}
	}
	batchQuery := hasBatchIDQuery(query)
	searchQuery := strings.TrimSpace(queryValue(query, "SearchTerm")) != ""
	if !batchQuery && !searchQuery && !catalogHasRandomSort(query) {
		a.handleFallbackProxy(w, r)
		return
	}
	if batchQuery {
		if err := validateBatchIDQuery(query); err != nil {
			invalidBatchIDResponse(w)
			return
		}
	}
	// A batch query has a closed candidate set. SearchTerm uses the bounded catalog
	// scanner instead, so the client page is never forwarded to each upstream.
	var filter userStateFilter
	var localFilter bool
	if batchQuery {
		filter, localFilter = a.localUserStateIntersect(w, r, query)
	} else {
		filter, localFilter = a.prepareLocalUserFilter(w, r, query)
		requestMergedCandidateSet(query)
	}
	if !localFilter {
		if err := validateCatalogSort(r.URL.Query()); err != nil {
			writeCatalogSortError(w, err)
			return
		}
		ensureCatalogSortFields(query)
	}
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

	var collected []upstreamItemsResult
	var incomplete bool
	if batchQuery {
		// Batch IDs are the complete candidate set, but StartIndex/Limit are still
		// applied once after merging. Do not let an upstream page this set first.
		scanQuery := cloneValues(query)
		prepareUpstreamCatalogRandom(scanQuery)
		setQueryValue(scanQuery, "StartIndex", "0")
		setQueryValue(scanQuery, "Limit", strconv.Itoa(maxBatchIDCount))
		tasks := make([]upstreamTask, len(clients))
		for i, client := range clients {
			c := client
			tasks[i] = upstreamTask{
				index: i,
				fn: func(bgCtx context.Context) upstreamItemsResult {
					translated := translateBatchIDQueryForServer(scanQuery, c.ID, a.IDStore)
					if translated.Err != nil {
						return upstreamItemsResult{Err: translated.Err}
					}
					if translated.Empty {
						return upstreamItemsResult{ServerID: c.ID, Complete: true}
					}
					payload, err := c.RequestJSON(bgCtx, reqCtx, a.Identity, http.MethodGet, "/Items", translated.Query, nil)
					if err != nil {
						return upstreamItemsResult{Err: err}
					}
					return upstreamItemsResult{ServerID: c.ID, Items: asItems(payload), Complete: true}
				},
			}
		}
		var report aggregationReport
		collected, report = a.aggregateUpstreamsWithReport(r.Context(), aggregationConfig{
			gracePeriod:   time.Duration(cfg.Timeouts.SearchGracePeriod) * time.Millisecond,
			globalTimeout: globalTimeout,
		}, tasks)
		incomplete = report.Incomplete
	} else {
		collected, incomplete = a.fetchItemsAcrossUpstreams(r.Context(), reqCtx, "/Items", query, nil)
	}
	collected = a.parentalVisibleResults(r, collected)
	merged := a.mergedItemsPayload(collected, a.clientFacingUserIDFor(r))
	items := asItems(merged)
	if !a.parentalResponseReady(w, r, merged) {
		return
	}
	incomplete = incomplete || w.Header().Get("X-EIO-Results-Incomplete") == "true"
	if localFilter {
		kept, recency := a.filterItemsByLocalUserState(r, items, filter)
		localItemSort(kept, r.URL.Query(), recency)
		if !a.strictSanitizedUserDataItems(w, r, kept) {
			return
		}
		payload := paginateItems(kept, r.URL.Query())
		if incomplete {
			markIncompleteResults(w, payload)
		}
		writeJSON(w, http.StatusOK, payload)
		return
	}
	if err := sortCatalogItems(items, r.URL.Query()); err != nil {
		writeCatalogSortError(w, err)
		return
	}
	if !a.strictSanitizedUserDataItems(w, r, items) {
		return
	}
	payload := paginateItems(items, r.URL.Query())
	if incomplete {
		markIncompleteResults(w, payload)
	}
	writeJSON(w, http.StatusOK, payload)
}

func (a *App) handleUserItems(w http.ResponseWriter, r *http.Request) {
	query := cloneValues(r.URL.Query())
	if catalogHasRandomSort(query) {
		if err := validateCatalogSort(query); err != nil {
			writeCatalogSortError(w, err)
			return
		}
	}
	if hasBatchIDQuery(query) && validateBatchIDQuery(query) != nil {
		invalidBatchIDResponse(w)
		return
	}
	parentID := firstQueryValue(query, "ParentId", "parentId", "parentid")
	if parentID != "" && parentID != "0" && parentID != "root" {
		resolved, routeStatus := a.resolveRouteForRequest(requestContextFrom(r.Context()), parentID)
		if routeStatus != 0 {
			writeJSON(w, http.StatusOK, map[string]any{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0})
			return
		}
		setQueryValue(query, "ParentId", resolved.OriginalID)
		query.Del("parentId")
		query.Del("parentid")
		// Drilling into a genre sends GenreIds=<virtual genre id>. The upstream has
		// no idea what that id is, silently ignores the filter and returns the whole
		// library, so every genre looked identical. Translate it per upstream and
		// answer empty when the id belongs to a different server.
		if translated, ok := a.translateTaxonomyIDs(firstQueryValue(query, "GenreIds", "genreIds", "genreids"), resolved.ServerID); ok {
			setQueryValue(query, "GenreIds", translated)
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
		if a.parentalSessionFor(r).restricted() || catalogHasRandomSort(r.URL.Query()) {
			a.handleRestrictedContainerItems(w, r, resolved, query, filter, localFilter)
			return
		}
		payload, err := resolved.Client.RequestJSON(r.Context(), requestContextFrom(r.Context()), a.Identity, http.MethodGet, "/Users/"+resolved.Client.clientUserID()+"/Items", query, nil)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"message": err.Error()})
			return
		}
		items := a.parentalFilterServerItems(r, resolved.Client, asItems(payload))
		a.rewriteItems(items, resolved.ServerID, a.clientFacingUserIDFor(r))
		// Sparse list DTOs may omit the rating fields entirely; the unified
		// filter confirms missing ratings (with parent inheritance) instead of
		// reading "absent" as "unrated" (plan §8.3/§8.4).
		if !a.parentalResponseReady(w, r, payload) {
			return
		}
		if m, ok := payload.(map[string]any); ok {
			m["Items"] = toAnySlice(items)
			if a.parentalSessionFor(r).restricted() {
				m["TotalRecordCount"] = len(items)
			}
		}
		if localFilter {
			kept, recency := a.filterItemsByLocalUserState(r, items, filter)
			localItemSort(kept, r.URL.Query(), recency)
			if !a.strictSanitizedUserDataItems(w, r, kept) {
				return
			}
			writeJSON(w, http.StatusOK, paginateItems(kept, r.URL.Query()))
			return
		}
		if !a.strictSanitizedUserDataItems(w, r, items) {
			return
		}
		writeJSON(w, http.StatusOK, payload)
		return
	}
	filter, localFilter := a.prepareLocalUserFilter(w, r, query)
	if !localFilter {
		if err := validateCatalogSort(r.URL.Query()); err != nil {
			writeCatalogSortError(w, err)
			return
		}
		ensureCatalogSortFields(query)
	}
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
	results = a.parentalVisibleResults(r, results)
	merged := a.mergedItemsPayload(results, a.clientFacingUserIDFor(r))
	if !a.parentalResponseReady(w, r, merged) {
		return
	}
	incomplete = incomplete || w.Header().Get("X-EIO-Results-Incomplete") != ""
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
		if localFilter {
			kept, recency := a.filterItemsByLocalUserState(r, asMaps, filter)
			localItemSort(kept, r.URL.Query(), recency)
			if !a.strictSanitizedUserDataItems(w, r, kept) {
				return
			}
			a.writeItemsResponse(w, paginateItems(kept, r.URL.Query()), incomplete)
			return
		}
		if len(asMaps) >= mergedItemsScanLimit {
			a.warnTruncatedMerge(len(asMaps))
		}
		if err := sortCatalogItems(asMaps, r.URL.Query()); err != nil {
			writeCatalogSortError(w, err)
			return
		}
		if !a.strictSanitizedUserDataItems(w, r, asMaps) {
			return
		}
		a.writeItemsResponse(w, paginateItems(asMaps, r.URL.Query()), incomplete)
		return
	}
	a.writeItemsResponse(w, merged, incomplete)
}

func (a *App) handleUserItemsLatest(w http.ResponseWriter, r *http.Request) {
	query := cloneValues(r.URL.Query())
	if err := validateCatalogSort(query); err != nil {
		writeCatalogSortError(w, err)
		return
	}
	parentID := firstQueryValue(query, "ParentId", "parentId", "parentid")
	if parentID != "" {
		resolved, routeStatus := a.resolveRouteForRequest(requestContextFrom(r.Context()), parentID)
		if routeStatus != 0 || resolved == nil {
			writeJSON(w, http.StatusOK, []any{})
			return
		}
		setQueryValue(query, "ParentId", resolved.OriginalID)
		setQueryValue(query, "UserId", resolved.Client.clientUserID())
		setQueryValue(query, "StartIndex", "0")
		setQueryValue(query, "Limit", strconv.Itoa(mergedItemsScanLimit))
		prepareUpstreamCatalogRandom(query)
		payload, err := resolved.Client.RequestJSON(r.Context(), requestContextFrom(r.Context()), a.Identity, http.MethodGet, "/Users/"+resolved.Client.clientUserID()+"/Items/Latest", query, nil)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"message": err.Error()})
			return
		}
		items := a.parentalFilterServerItems(r, resolved.Client, asItems(payload))
		a.rewriteItems(items, resolved.ServerID, a.clientFacingUserIDFor(r))
		if err := sortLatestItems(items, r.URL.Query()); err != nil {
			writeCatalogSortError(w, err)
			return
		}
		if !a.strictSanitizedUserDataItems(w, r, items) || !a.parentalResponseReady(w, r, nil) {
			return
		}
		writeJSON(w, http.StatusOK, pageItemsArray(items, r.URL.Query()))
		return
	}
	if err := validateCatalogSort(query); err != nil {
		writeCatalogSortError(w, err)
		return
	}
	ensureCatalogSortFields(query)
	requestMergedCandidateSet(query)
	latestGrace := time.Duration(a.ConfigStore.Snapshot().Timeouts.LatestGracePeriod) * time.Millisecond
	results, incomplete := a.fetchItemsAcrossUpstreamsWithGrace(r.Context(), requestContextFrom(r.Context()), "/Users/%s/Items/Latest", query, nil, &latestGrace)
	results = a.parentalVisibleResults(r, results)
	for i := range results {
		results[i].Items = a.parentalFilterServerItems(r, a.Upstream.ClientByID(results[i].ServerID), results[i].Items)
	}
	allItems := a.mergeRoundRobinItems(results, a.clientFacingUserIDFor(r))
	if err := sortLatestItems(allItems, r.URL.Query()); err != nil {
		writeCatalogSortError(w, err)
		return
	}
	if !a.parentalResponseReady(w, r, nil) {
		return
	}
	paged := pageItemsArray(allItems, r.URL.Query())
	if !a.strictSanitizedUserDataItems(w, r, asItems(paged)) {
		return
	}
	if incomplete || w.Header().Get("X-EIO-Results-Incomplete") == "true" {
		markIncompleteResultsHeader(w)
	}
	writeJSON(w, http.StatusOK, paged)
}

func sortLatestItems(items []map[string]any, query url.Values) error {
	if strings.TrimSpace(firstQueryValue(query, "SortBy", "sortBy")) == "" {
		return sortCatalogItems(items, url.Values{"SortBy": {"DateCreated"}, "SortOrder": {"Descending"}})
	}
	return sortCatalogItems(items, query)
}

func pageItemsArray(items []map[string]any, query url.Values) []any {
	pageQuery := cloneValues(query)
	if queryKey(pageQuery, "Limit") == "" {
		setQueryValue(pageQuery, "Limit", "20")
	}
	page := paginateItems(items, pageQuery)
	if result, ok := page["Items"].([]any); ok {
		return result
	}
	return []any{}
}

func (a *App) handleUserItemByID(w http.ResponseWriter, r *http.Request) {
	resolved, routeStatus := a.resolveRouteForRequest(requestContextFrom(r.Context()), r.PathValue("itemId"))
	if routeStatus != 0 {
		a.writeRouteAccessError(w, routeStatus)
		return
	}
	reqCtx := requestContextFrom(r.Context())
	if !a.requireServerAccess(w, r, resolved) {
		return
	}
	instances := a.collectAllowedInstances(reqCtx, resolved)
	if len(instances) <= 1 {
		query := cloneValues(r.URL.Query())
		setQueryValue(query, "UserId", resolved.Client.clientUserID())
		payload, err := resolved.Client.RequestJSON(r.Context(), requestContextFrom(r.Context()), a.Identity, http.MethodGet, "/Users/"+resolved.Client.clientUserID()+"/Items/"+resolved.OriginalID, query, nil)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"message": err.Error()})
			return
		}
		if item, ok := payload.(map[string]any); ok {
			a.discoverItemCopies(r, r.PathValue("itemId"), item)
			refreshed, refreshStatus := a.resolveRouteForRequest(reqCtx, r.PathValue("itemId"))
			if refreshStatus == 0 {
				instances = a.collectAllowedInstances(reqCtx, refreshed)
			}
			if len(instances) > 1 {
				a.handleMergedItemDetail(w, r, instances)
				return
			}
		}
		cfg := a.ConfigStore.Snapshot()
		annotateItemSources(payload, resolved.Client)
		rewriteResponseIDs(payload, resolved.ServerID, a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
		if !a.strictSanitizedUserData(w, r, r.PathValue("itemId"), payload) {
			return
		}
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
	resolved, routeStatus := a.resolveRouteForRequest(requestContextFrom(r.Context()), r.PathValue("itemId"))
	if routeStatus != 0 {
		a.writeRouteAccessError(w, routeStatus)
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
	if !a.strictSanitizedUserData(w, r, r.PathValue("itemId"), payload) {
		return
	}
	if !a.parentalDetailAllowed(w, r, resolved.Client, resolved.OriginalID) {
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (a *App) handleItemSimilar(w http.ResponseWriter, r *http.Request) {
	resolved, routeStatus := a.resolveRouteForRequest(requestContextFrom(r.Context()), r.PathValue("itemId"))
	if routeStatus != 0 {
		if routeStatus != http.StatusNotFound {
			a.writeRouteAccessError(w, routeStatus)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"Items": []any{}, "TotalRecordCount": 0})
		return
	}
	if !a.requireServerAccess(w, r, resolved) {
		return
	}
	query := cloneValues(r.URL.Query())
	setQueryValue(query, "UserId", resolved.Client.clientUserID())
	payload, err := resolved.Client.RequestJSON(r.Context(), requestContextFrom(r.Context()), a.Identity, http.MethodGet, "/Items/"+resolved.OriginalID+"/Similar", query, nil)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"message": err.Error()})
		return
	}
	cfg := a.ConfigStore.Snapshot()
	// The recommendation list is content: filter it through the unified
	// parental read path before it reaches the client (plan §8.4).
	if m, ok := payload.(map[string]any); ok {
		a.parentalFilterServerPayloadItems(r, resolved.Client, m)
	}
	rewriteResponseIDs(payload, resolved.ServerID, a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
	if !a.parentalResponseReady(w, r, payload) {
		return
	}
	if !a.strictSanitizedUserDataItems(w, r, asItems(payload)) {
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (a *App) handleItemThemeMedia(w http.ResponseWriter, r *http.Request) {
	resolved, routeStatus := a.resolveRouteForRequest(requestContextFrom(r.Context()), r.PathValue("itemId"))
	if routeStatus != 0 {
		if routeStatus != http.StatusNotFound {
			a.writeRouteAccessError(w, routeStatus)
			return
		}
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
	setQueryValue(query, "UserId", resolved.Client.clientUserID())
	payload, err := resolved.Client.RequestJSON(r.Context(), requestContextFrom(r.Context()), a.Identity, http.MethodGet, "/Items/"+resolved.OriginalID+"/ThemeMedia", query, nil)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"message": err.Error()})
		return
	}
	cfg := a.ConfigStore.Snapshot()
	// Theme videos/songs are content items nested under three result keys;
	// every nested list is filtered before the payload is returned (plan §8.4).
	if m, ok := payload.(map[string]any); ok {
		a.parentalFilterServerPayloadItems(r, resolved.Client, m)
	}
	rewriteResponseIDs(payload, resolved.ServerID, a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
	if !a.parentalResponseReady(w, r, payload) {
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (a *App) fetchItemsAcrossUpstreams(ctx context.Context, reqCtx *RequestContext, pathTemplate string, query url.Values, body any) ([]upstreamItemsResult, bool) {
	return a.fetchItemsAcrossUpstreamsWithGrace(ctx, reqCtx, pathTemplate, query, body, nil)
}

func (a *App) fetchItemsAcrossUpstreamsWithGrace(ctx context.Context, reqCtx *RequestContext, pathTemplate string, query url.Values, body any, graceOverride *time.Duration) ([]upstreamItemsResult, bool) {
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
	if reqCtx != nil && reqCtx.Parental != nil {
		budget = reqCtx.Parental.catalog
	}

	tasks := make([]upstreamTask, len(clients))
	for i, client := range clients {
		c := client
		tasks[i] = upstreamTask{
			index: i,
			fn: func(bgCtx context.Context) upstreamItemsResult {
				items, complete, truncated, err := a.fetchMergedItemsFromUpstream(bgCtx, reqCtx, c, pathTemplate, query, body, budget)
				if errors.Is(err, errTaxonomyQueryNotTranslatable) || errors.Is(err, errBatchQueryNoItems) {
					// A translated filter with no IDs for this upstream is a complete
					// empty result, not an upstream failure.
					return upstreamItemsResult{ServerID: c.ID, Complete: true}
				}
				if err != nil && len(items) > 0 {
					// Keep a confirmed prefix if a later scan page failed; the report
					// still marks this server incomplete.
					return upstreamItemsResult{ServerID: c.ID, Items: items, Truncated: true}
				}
				if err != nil {
					return upstreamItemsResult{Err: err, Complete: false}
				}
				return upstreamItemsResult{ServerID: c.ID, Items: items, Complete: complete, Truncated: truncated}
			},
		}
	}

	grace := time.Duration(cfg.Timeouts.SearchGracePeriod) * time.Millisecond
	if graceOverride != nil {
		grace = *graceOverride
	}
	if reqCtx != nil && reqCtx.Parental != nil && reqCtx.Parental.restricted() {
		grace = 0
	}
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

// dedupFacts carries identity evidence collected for one media item. Provider
// identifiers are namespaced by provider, and their lookup keys also include the
// media type so a Movie id cannot collide with a Series/Episode id.
type dedupFacts struct {
	typ               string
	providers         map[string]string
	name              string // lower-cased and trimmed
	year              string // empty when the upstream did not send one
	episodeSignatures []episodeDedupSignature
}

type episodeDedupSignature struct {
	season       int
	episode      int
	seriesGroups map[string]struct{}
}

func dedupFactsOf(item map[string]any) dedupFacts {
	f := dedupFacts{}
	f.typ, _ = item["Type"].(string)
	f.name, _ = item["Name"].(string)
	f.name = strings.ToLower(strings.TrimSpace(f.name))
	if y, ok := numericInt(item["ProductionYear"]); ok && y > 0 {
		f.year = strconv.Itoa(y)
	}
	if providerIDs, ok := item["ProviderIds"].(map[string]any); ok {
		f.providers = make(map[string]string, len(providerIDs))
		for provider, rawID := range providerIDs {
			id, ok := rawID.(string)
			provider = strings.ToLower(strings.TrimSpace(provider))
			id = strings.TrimSpace(id)
			if ok && provider != "" && id != "" {
				f.providers[provider] = id
			}
		}
	}
	return f
}

func providerBucketKey(facts dedupFacts, provider, providerID string) string {
	return strings.ToLower(strings.TrimSpace(facts.typ)) + "\x00" + provider + "\x00" + providerID
}

// providerIdentityMatch returns whether the records share a strong provider ID
// and whether their strong evidence conflicts. If both sides have provider IDs
// but no same-provider match, they are not allowed to fall through to title/year
// heuristics (including same-number IDs from different provider namespaces).
func providerIdentityMatch(a, b dedupFacts) (matched, conflict bool) {
	if a.typ != b.typ {
		return false, true
	}
	if len(a.providers) == 0 || len(b.providers) == 0 {
		return false, false
	}
	matched = false
	for provider, aID := range a.providers {
		if bID, ok := b.providers[provider]; ok {
			if aID != bID {
				return false, true
			}
			matched = true
		}
	}
	if matched {
		return true, false
	}
	return false, true
}

func movieSeriesMatch(a, b dedupFacts) bool {
	if a.typ != b.typ || (a.typ != "Movie" && a.typ != "Series") {
		return false
	}
	if matched, conflict := providerIdentityMatch(a, b); matched {
		return true
	} else if conflict {
		return false
	}
	return a.name != "" && a.name == b.name && a.year != "" && a.year == b.year
}

func episodeSeriesSignature(item map[string]any, serverID string, store *IDStore) (episodeDedupSignature, bool) {
	seriesID, _ := item["SeriesId"].(string)
	seriesID = strings.TrimSpace(seriesID)
	season, hasSeason := numericInt(item["ParentIndexNumber"])
	episode, hasEpisode := numericInt(item["IndexNumber"])
	if store == nil || serverID == "" || seriesID == "" || !hasSeason || !hasEpisode || season < 0 || episode < 0 {
		return episodeDedupSignature{}, false
	}
	seriesVirtualID := store.GetOrCreateVirtualID(seriesID, serverID)
	aliases := store.AliasVirtualIDs(seriesVirtualID)
	groups := make(map[string]struct{}, len(aliases)+1)
	groups[seriesVirtualID] = struct{}{}
	for _, alias := range aliases {
		groups[alias] = struct{}{}
	}
	return episodeDedupSignature{season: season, episode: episode, seriesGroups: groups}, true
}

func episodeSignaturesMatch(a, b []episodeDedupSignature) bool {
	for _, left := range a {
		for _, right := range b {
			if left.season != right.season || left.episode != right.episode {
				continue
			}
			for group := range left.seriesGroups {
				if _, ok := right.seriesGroups[group]; ok {
					return true
				}
			}
		}
	}
	return false
}

func episodeMatch(a, b dedupFacts) bool {
	if a.typ != "Episode" || b.typ != "Episode" {
		return false
	}
	if matched, conflict := providerIdentityMatch(a, b); matched {
		return true
	} else if conflict {
		return false
	}
	return episodeSignaturesMatch(a.episodeSignatures, b.episodeSignatures)
}

type dedupSeenEntry struct {
	virtualID   string
	mergedIndex int
	serverID    string
	originalID  string
	facts       dedupFacts
}

func episodeSignatureBucketKey(group string, signature episodeDedupSignature) string {
	return group + "\x00" + strconv.Itoa(signature.season) + "\x00" + strconv.Itoa(signature.episode)
}

func appendSeenEntry(entries []*dedupSeenEntry, candidate *dedupSeenEntry) []*dedupSeenEntry {
	for _, entry := range entries {
		if entry == candidate {
			return entries
		}
	}
	return append(entries, candidate)
}

func mergeDedupFacts(dst *dedupFacts, src dedupFacts) {
	if dst.providers == nil && len(src.providers) != 0 {
		dst.providers = map[string]string{}
	}
	for provider, providerID := range src.providers {
		if _, exists := dst.providers[provider]; !exists {
			dst.providers[provider] = providerID
		}
	}
	if dst.name == "" {
		dst.name = src.name
	}
	if dst.year == "" {
		dst.year = src.year
	}
	dst.episodeSignatures = append(dst.episodeSignatures, src.episodeSignatures...)
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

// mergeRoundRobinItems interleaves results while deduplicating only on
// type-scoped strong provider IDs, strict name+year fallback, or a trusted
// episode-series identity plus season/episode coordinates.
func (a *App) mergeRoundRobinItems(results []upstreamItemsResult, clientUserID string) []map[string]any {
	cfg := a.ConfigStore.Snapshot()
	merged := make([]map[string]any, 0)
	seenProviders := map[string][]*dedupSeenEntry{}
	seenName := map[string][]*dedupSeenEntry{}
	seenEpisode := map[string][]*dedupSeenEntry{}
	var entries []*dedupSeenEntry

	addEntryIndexes := func(entry *dedupSeenEntry) {
		for provider, providerID := range entry.facts.providers {
			key := providerBucketKey(entry.facts, provider, providerID)
			seenProviders[key] = appendSeenEntry(seenProviders[key], entry)
		}
		if (entry.facts.typ == "Movie" || entry.facts.typ == "Series") && entry.facts.name != "" {
			seenName[entry.facts.name] = appendSeenEntry(seenName[entry.facts.name], entry)
		}
		if entry.facts.typ == "Episode" {
			for _, signature := range entry.facts.episodeSignatures {
				for group := range signature.seriesGroups {
					key := episodeSignatureBucketKey(group, signature)
					seenEpisode[key] = appendSeenEntry(seenEpisode[key], entry)
				}
			}
		}
	}

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

			facts := dedupFactsOf(item)
			if facts.typ == "Episode" {
				if signature, ok := episodeSeriesSignature(item, result.ServerID, a.IDStore); ok {
					facts.episodeSignatures = append(facts.episodeSignatures, signature)
				}
			}

			var candidates []*dedupSeenEntry
			candidateSet := map[*dedupSeenEntry]struct{}{}
			addCandidates := func(bucket []*dedupSeenEntry) {
				for _, candidate := range bucket {
					if _, exists := candidateSet[candidate]; exists {
						continue
					}
					candidateSet[candidate] = struct{}{}
					candidates = append(candidates, candidate)
				}
			}
			switch facts.typ {
			case "Movie", "Series":
				for provider, providerID := range facts.providers {
					addCandidates(seenProviders[providerBucketKey(facts, provider, providerID)])
				}
				addCandidates(seenName[facts.name])
			case "Episode":
				for provider, providerID := range facts.providers {
					addCandidates(seenProviders[providerBucketKey(facts, provider, providerID)])
				}
				for _, signature := range facts.episodeSignatures {
					for group := range signature.seriesGroups {
						addCandidates(seenEpisode[episodeSignatureBucketKey(group, signature)])
					}
				}
			}

			var entry *dedupSeenEntry
			for _, candidate := range candidates {
				matches := false
				switch facts.typ {
				case "Movie", "Series":
					matches = movieSeriesMatch(facts, candidate.facts)
				case "Episode":
					matches = episodeMatch(facts, candidate.facts)
				}
				if matches {
					entry = candidate
					break
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
					if !candidateHasImage && baseHasImage {
						if tag, ok := primaryImageTag(base); ok {
							copyPrimaryImageFields(item, base)
							a.IDStore.SetImageSource(entry.virtualID, "Primary", tag, oldServerID, oldOriginalID)
						}
					}
				} else if !baseHasImage && candidateHasImage {
					if tag, ok := primaryImageTag(item); ok {
						copyPrimaryImageFields(base, item)
						a.IDStore.SetImageSource(entry.virtualID, "Primary", tag, result.ServerID, originalID)
					}
				}
				// Retain every newly verified provider/signature fact so a later
				// incomplete copy cannot bridge two conflicting identity groups.
				mergeDedupFacts(&entry.facts, facts)
				addEntryIndexes(entry)
				continue
			}

			// First occurrence: keep the virtual ID, rewrite everything else.
			virtualID := a.IDStore.GetOrCreateVirtualID(originalID, result.ServerID)
			entry = &dedupSeenEntry{virtualID: virtualID, mergedIndex: len(merged), serverID: result.ServerID, originalID: originalID, facts: facts}
			entries = append(entries, entry)
			addEntryIndexes(entry)
			delete(item, "Id")
			rewriteResponseIDs(item, result.ServerID, a.IDStore, cfg.Server.ID, clientUserID)
			item["Id"] = virtualID
			merged = append(merged, item)
		}
	}

	// Record each group's display-winning instance so image routing only uses the
	// upstream that actually owns the advertised image tags.
	for _, entry := range entries {
		a.IDStore.SetDisplaySource(entry.virtualID, entry.serverID, entry.originalID)
		if entry.mergedIndex >= 0 && entry.mergedIndex < len(merged) {
			recordImageSourceTags(a.IDStore, entry.virtualID, entry.serverID, entry.originalID, merged[entry.mergedIndex])
		}
	}

	return merged
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
		prepareUpstreamCatalogRandom(pageQuery)
		setQueryValue(pageQuery, "UserId", c.clientUserID())
		if hasBatchIDQuery(pageQuery) {
			translated := translateBatchIDQueryForServer(pageQuery, c.ID, a.IDStore)
			if translated.Err != nil {
				return nil, translated.Err
			}
			if translated.Empty {
				return nil, errBatchQueryNoItems
			}
			pageQuery = translated.Query
		}
		if genre := firstQueryValue(pageQuery, "GenreIds", "genreIds", "genreids"); genre != "" {
			translated, ok := a.translateTaxonomyIDs(genre, c.ID)
			if !ok {
				return nil, errTaxonomyQueryNotTranslatable
			}
			setQueryValue(pageQuery, "GenreIds", translated)
			pageQuery.Del("genreIds")
			pageQuery.Del("genreids")
		}
		setQueryValue(pageQuery, "StartIndex", strconv.Itoa(startIndex))
		setQueryValue(pageQuery, "Limit", strconv.Itoa(window))
		return pageQuery, nil
	}

	if !c.Config.PagedScan {
		directQuery, queryErr := buildQuery(0, mergedItemsScanLimit)
		if errors.Is(queryErr, errBatchQueryNoItems) || errors.Is(queryErr, errTaxonomyQueryNotTranslatable) {
			return []map[string]any{}, true, false, nil
		}
		if queryErr != nil {
			return nil, false, false, queryErr
		}
		if !budget.tryAcquire() {
			return nil, false, true, nil
		}
		payload, requestErr := c.RequestJSON(ctx, reqCtx, a.Identity, http.MethodGet, path, directQuery, body)
		if requestErr != nil {
			return nil, false, true, requestErr
		}
		items := asItems(payload)
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
		window := min(effectiveCeiling, mergedItemsScanLimit-len(collected))
		pageQuery, queryErr := buildQuery(offset, window)
		if errors.Is(queryErr, errBatchQueryNoItems) || errors.Is(queryErr, errTaxonomyQueryNotTranslatable) {
			return []map[string]any{}, true, false, nil
		}
		if queryErr != nil {
			return collected, false, true, queryErr
		}
		if !budget.tryAcquire() {
			return collected, false, true, nil
		}
		payload, requestErr := c.RequestJSON(ctx, reqCtx, a.Identity, http.MethodGet, path, pageQuery, body)
		if requestErr != nil {
			if len(collected) == 0 && window > fallbackUpstreamPageLimit && isBadRequestError(requestErr) {
				if !budget.tryAcquire() {
					return nil, false, true, nil
				}
				retryQuery, retryQueryErr := buildQuery(0, fallbackUpstreamPageLimit)
				if retryQueryErr != nil {
					return nil, false, true, retryQueryErr
				}
				retryPayload, retryErr := c.RequestJSON(ctx, reqCtx, a.Identity, http.MethodGet, path, retryQuery, body)
				if retryErr != nil {
					return nil, false, true, retryErr
				}
				page := asItems(retryPayload)
				collected = append(collected, page...)
				offset = len(collected)
				effectiveCeiling = fallbackUpstreamPageLimit
				if len(page) < fallbackUpstreamPageLimit {
					if total, hasTotal := totalRecordCount(retryPayload); hasTotal && offset < total {
						return collected, false, true, nil
					}
					return collected, true, false, nil
				}
				continue
			}
			return collected, false, true, requestErr
		}

		page := asItems(payload)
		total, hasTotal := totalRecordCount(payload)
		if len(page) == 0 {
			if hasTotal && offset < total {
				return collected, false, true, nil
			}
			break
		}
		collected = append(collected, page...)
		offset = len(collected)
		if len(page) < window {
			if hasTotal && offset < total {
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
	setQueryValue(query, "StartIndex", "0")
	setQueryValue(query, "Limit", strconv.Itoa(mergedItemsScanLimit))
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
	if s := queryValue(query, "StartIndex"); s != "" {
		if v, err := strconv.Atoi(s); err == nil && v >= 0 {
			startIndex = v
		}
	}
	if startIndex > len(merged) {
		startIndex = len(merged)
	}
	merged = merged[startIndex:]
	if lim := queryValue(query, "Limit"); lim != "" {
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
		if value := queryValue(values, key); value != "" {
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
