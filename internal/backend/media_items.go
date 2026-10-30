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
