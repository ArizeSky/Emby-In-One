package backend

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (a *App) handleItemsCollection(w http.ResponseWriter, r *http.Request) {
	query := cloneValues(r.URL.Query())
	if !hasBatchIDQuery(query) {
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
				return upstreamItemsResult{ServerID: c.ID, Items: asItems(payload)}
			},
		}
	}

	collected := a.aggregateUpstreams(r.Context(), aggregationConfig{
		gracePeriod:   time.Duration(cfg.Timeouts.SearchGracePeriod) * time.Millisecond,
		globalTimeout: globalTimeout,
	}, tasks)
	merged := a.mergedItemsPayload(collected, a.clientFacingUserIDFor(r))
	items := asItems(merged)
	if localFilter {
		kept, recency := a.filterItemsByLocalUserState(r, items, filter)
		localItemSort(kept, r.URL.Query(), recency)
		a.overlayLocalUserDataItems(r, kept)
		writeJSON(w, http.StatusOK, map[string]any{
			"Items":            toAnySlice(kept),
			"TotalRecordCount": len(kept),
			"StartIndex":       0,
		})
		return
	}
	a.overlayLocalUserDataItems(r, items)
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
	results := a.fetchItemsAcrossUpstreams(r.Context(), requestContextFrom(r.Context()), "/Users/%s/Items", query, nil)
	merged := a.mergedItemsPayload(results, a.clientFacingUserIDFor(r))
	if items, ok := merged["Items"].([]any); ok {
		asMaps := make([]map[string]any, 0, len(items))
		for _, item := range items {
			if m, ok := item.(map[string]any); ok {
				asMaps = append(asMaps, m)
			}
		}
		if localFilter {
			kept, recency := a.filterItemsByLocalUserState(r, asMaps, filter)
			localItemSort(kept, r.URL.Query(), recency)
			a.overlayLocalUserDataItems(r, kept)
			writeJSON(w, http.StatusOK, paginateItems(kept, r.URL.Query()))
			return
		}
		if len(asMaps) >= mergedItemsScanLimit {
			a.warnTruncatedMerge(len(asMaps))
		}
		a.overlayLocalUserDataItems(r, asMaps)
		writeJSON(w, http.StatusOK, paginateItems(asMaps, r.URL.Query()))
		return
	}
	writeJSON(w, http.StatusOK, merged)
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
					return upstreamItemsResult{Err: err}
				}
				items := asItems(payload)
				a.rewriteItems(items, c.ID, a.clientFacingUserIDFor(r))
				return upstreamItemsResult{ServerID: c.ID, Items: items}
			},
		}
	}

	collected := a.aggregateUpstreams(r.Context(), aggregationConfig{
		gracePeriod:   time.Duration(cfg.Timeouts.LatestGracePeriod) * time.Millisecond,
		globalTimeout: globalTimeout,
	}, tasks)
	allItems := make([]map[string]any, 0)
	for _, result := range collected {
		allItems = append(allItems, result.Items...)
	}
	a.overlayLocalUserDataItems(r, allItems)
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
		cfg := a.ConfigStore.Snapshot()
		rewriteResponseIDs(payload, resolved.ServerID, a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
		a.overlayLocalUserData(r, r.PathValue("itemId"), payload)
		writeJSON(w, http.StatusOK, payload)
		return
	}

	var base map[string]any
	var baseServerID string
	allMediaSources := []map[string]any{}

	cfg := a.ConfigStore.Snapshot()
	globalTimeout := time.Duration(cfg.Timeouts.Global) * time.Millisecond
	if globalTimeout <= 0 {
		globalTimeout = 15 * time.Second
	}
	bgCtx, bgCancel := context.WithTimeout(context.Background(), globalTimeout)

	type instanceResult struct {
		serverID     string
		data         map[string]any
		mediaSources []map[string]any
	}
	resultCh := make(chan *instanceResult, len(instances))

	for _, inst := range instances {
		go func(si seriesInstance) {
			query := cloneValues(r.URL.Query())
			query.Set("UserId", si.Client.clientUserID())
			payload, err := si.Client.RequestJSON(bgCtx, requestContextFrom(r.Context()), a.Identity, http.MethodGet, "/Users/"+si.Client.clientUserID()+"/Items/"+si.OriginalID, query, nil)
			if err != nil {
				resultCh <- nil
				return
			}
			data, ok := payload.(map[string]any)
			if !ok {
				resultCh <- nil
				return
			}
			var mediaSources []map[string]any
			for _, raw := range asItems(map[string]any{"Items": data["MediaSources"]}) {
				ms := deepCloneMap(raw)
				if originalID, _ := ms["Id"].(string); originalID != "" {
					ms["Id"] = a.IDStore.GetOrCreateVirtualID(originalID, si.ServerID)
				}
				if client := a.Upstream.ClientByID(si.ServerID); client != nil {
					name, _ := ms["Name"].(string)
					if name == "" {
						name = "Version"
					}
					ms["Name"] = name + " [" + client.Name + "]"
				}
				mediaSources = append(mediaSources, ms)
			}
			resultCh <- &instanceResult{serverID: si.ServerID, data: data, mediaSources: mediaSources}
		}(inst)
	}

	gracePeriod := time.Duration(cfg.Timeouts.MetadataGracePeriod) * time.Millisecond
	var graceTimer <-chan time.Time
	received := 0

	for received < len(instances) {
		select {
		case res := <-resultCh:
			received++
			if res != nil {
				if base == nil {
					base = deepCloneMap(res.data)
					baseServerID = res.serverID
				}
				allMediaSources = append(allMediaSources, res.mediaSources...)
				if graceTimer == nil && gracePeriod > 0 {
					graceTimer = time.After(gracePeriod)
				}
			}
		case <-graceTimer:
			goto metadataDone
		case <-r.Context().Done():
			goto metadataDone
		}
	}
metadataDone:
	bgCancel()
	if base == nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Upstream request failed"})
		return
	}
	mediaSources := make([]any, 0, len(allMediaSources))
	for _, mediaSource := range allMediaSources {
		mediaSources = append(mediaSources, mediaSource)
	}
	// delete-and-restore: prevent rewriteResponseIDs from double-wrapping
	// already-virtualised MediaSource IDs and creating orphan mappings
	delete(base, "MediaSources")
	rewriteResponseIDs(base, baseServerID, a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
	base["MediaSources"] = mediaSources
	a.overlayLocalUserData(r, r.PathValue("itemId"), base)
	writeJSON(w, http.StatusOK, base)
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
	writeJSON(w, http.StatusOK, payload)
}

func (a *App) fetchItemsAcrossUpstreams(ctx context.Context, reqCtx *RequestContext, pathTemplate string, query url.Values, body any) []upstreamItemsResult {
	clients := a.allowedClients(reqCtx)
	if len(clients) == 0 {
		return nil
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
				serverQuery := cloneValues(query)
				// Clients only ever hold EIO's virtual user ID, which no upstream knows.
				serverQuery.Set("UserId", c.clientUserID())
				if hasBatchIDQuery(serverQuery) {
					translated, ok := translateBatchIDQueryForServer(serverQuery, c.ID, a.IDStore)
					if !ok {
						return upstreamItemsResult{Err: errBatchQueryNotTranslatable}
					}
					serverQuery = translated
				}
				payload, err := c.RequestJSON(bgCtx, reqCtx, a.Identity, http.MethodGet, strings.Replace(pathTemplate, "%s", c.clientUserID(), 1), serverQuery, body)
				if err != nil {
					return upstreamItemsResult{Err: err}
				}
				return upstreamItemsResult{ServerID: c.ID, Items: asItems(payload)}
			},
		}
	}

	return a.aggregateUpstreams(ctx, aggregationConfig{
		gracePeriod:   time.Duration(cfg.Timeouts.SearchGracePeriod) * time.Millisecond,
		globalTimeout: globalTimeout,
	}, tasks)
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
// better metadata than the existing item from existingServerID, using the V1.2
// 4-level priority: priorityMetadata flag → Chinese in Overview → longer
// Overview → earlier upstream order.
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

	// 2. Chinese in Overview
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

func (a *App) mergedItemsPayload(results []upstreamItemsResult, clientUserID string) map[string]any {
	merged := a.mergeRoundRobinItems(results, clientUserID)
	return map[string]any{
		"Items":            toAnySlice(merged),
		"TotalRecordCount": len(merged),
		"StartIndex":       0,
	}
}

// mergeRoundRobinItems interleaves items from every server in turn, deduplicating by
// getItemKey: the first copy keeps the virtual id and later copies are registered as
// additional instances of it, replacing the display item only when their metadata is
// better.
func (a *App) mergeRoundRobinItems(results []upstreamItemsResult, clientUserID string) []map[string]any {
	cfg := a.ConfigStore.Snapshot()
	merged := make([]map[string]any, 0)
	type seenEntry struct {
		virtualID   string
		mergedIndex int // position in merged slice
		serverID    string
	}
	seen := map[string]*seenEntry{} // dedupKey → entry

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
			key := getItemKey(item)
			originalID, _ := item["Id"].(string)
			if key == "" || originalID == "" {
				rewriteResponseIDs(item, result.ServerID, a.IDStore, cfg.Server.ID, clientUserID)
				merged = append(merged, item)
				continue
			}

			if entry, found := seen[key]; found {
				// Duplicate: associate as an additional instance of the same item.
				a.IDStore.AssociateAdditionalInstance(entry.virtualID, originalID, result.ServerID)
				if isBetterMetadata(merged[entry.mergedIndex], entry.serverID, item, result.ServerID, cfg) {
					delete(item, "Id")
					rewriteResponseIDs(item, result.ServerID, a.IDStore, cfg.Server.ID, clientUserID)
					item["Id"] = entry.virtualID
					merged[entry.mergedIndex] = item
					entry.serverID = result.ServerID
				}
				continue
			}

			// First occurrence: keep the virtual ID, rewrite everything else.
			virtualID := a.IDStore.GetOrCreateVirtualID(originalID, result.ServerID)
			seen[key] = &seenEntry{virtualID: virtualID, mergedIndex: len(merged), serverID: result.ServerID}
			delete(item, "Id")
			rewriteResponseIDs(item, result.ServerID, a.IDStore, cfg.Server.ID, clientUserID)
			item["Id"] = virtualID
			merged = append(merged, item)
		}
	}

	return merged
}

// mergedItemsScanLimit caps how many items one merged (no ParentId) item query may pull
// from each upstream.
const mergedItemsScanLimit = 5000

// requestMergedCandidateSet asks the upstreams for the candidate set instead of the
// client's page. Paging belongs to the proxy on this path: the answers are merged and
// deduplicated before the page is cut, so a window forwarded upstream would be cut
// twice — once there, once by paginateItems — and the merged total would be the page
// size rather than the library size, telling the client there is no page 2.
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
