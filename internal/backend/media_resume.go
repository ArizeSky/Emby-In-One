package backend

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (a *App) handleUserItemsResume(w http.ResponseWriter, r *http.Request) {
	reqCtx := requestContextFrom(r.Context())
	// Non-admin users: serve resume from local WatchStore
	if a.WatchStore != nil && reqCtx != nil && reqCtx.ProxyUser != nil && reqCtx.ProxyUser.Role != "admin" {
		a.handleLocalResume(w, r, reqCtx)
		return
	}
	query := cloneValues(r.URL.Query())
	if err := validateCatalogSort(query); err != nil {
		writeCatalogSortError(w, err)
		return
	}
	parentID := firstQueryValue(query, "ParentId", "parentId", "parentid")
	if parentID != "" && parentID != "0" && !strings.EqualFold(parentID, "root") {
		scope := a.newResumeContainerScope(r, reqCtx, parentID)
		if scope == nil {
			writeJSON(w, http.StatusOK, map[string]any{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0})
			return
		}
		clients := a.allowedClients(reqCtx)
		originalIDs := map[string]struct{}{}
		for _, originalID := range scope.instances {
			originalIDs[originalID] = struct{}{}
		}
		cfg := a.ConfigStore.Snapshot()
		globalTimeout := time.Duration(cfg.Timeouts.Global) * time.Millisecond
		if globalTimeout <= 0 {
			globalTimeout = 15 * time.Second
		}
		tasks := make([]upstreamTask, 0, len(scope.instances))
		for index, client := range clients {
			c := client
			originalID, ok := scope.instances[c.ID]
			if !ok {
				continue
			}
			tasks = append(tasks, upstreamTask{
				index: index,
				fn: func(bgCtx context.Context) upstreamItemsResult {
					instQuery := cloneValues(query)
					instQuery.Set("ParentId", originalID)
					items, complete, truncated, err := a.fetchMergedItemsFromUpstream(bgCtx, reqCtx, c, "/Users/%s/Items/Resume", instQuery, nil, scope.budget)
					if err != nil && len(items) == 0 {
						return upstreamItemsResult{Err: err}
					}
					if scope.kind == "Series" || scope.kind == "" {
						items = filterSeriesItems(items, originalIDs)
					}
					return upstreamItemsResult{ServerID: c.ID, Items: items, Complete: complete && err == nil, Truncated: truncated || err != nil}
				},
			})
		}
		grace := time.Duration(cfg.Timeouts.SearchGracePeriod) * time.Millisecond
		for _, client := range clients {
			if client.Config.PagedScan {
				grace = 0
				break
			}
		}
		results, report := a.aggregateUpstreamsWithReport(r.Context(), aggregationConfig{gracePeriod: grace, globalTimeout: globalTimeout}, tasks)
		a.writeMergedParentalResponse(w, r, results, report.Incomplete)
		return
	}
	results, incomplete := a.fetchItemsAcrossUpstreams(r.Context(), reqCtx, "/Users/%s/Items/Resume", query, nil)
	a.writeMergedParentalResponse(w, r, results, incomplete)
}

// handleLocalResume serves bounded WatchStore candidate pages with enriched metadata.
// Container scope and client pagination are applied before writing the response.
func (a *App) handleLocalResume(w http.ResponseWriter, r *http.Request, reqCtx *RequestContext) {
	query := cloneValues(r.URL.Query())
	parentID := firstQueryValue(query, "ParentId", "parentId", "parentid")
	var scope *resumeContainerScope
	if parentID != "" && parentID != "0" && !strings.EqualFold(parentID, "root") {
		scope = a.newResumeContainerScope(r, reqCtx, parentID)
		if scope == nil {
			writeJSON(w, http.StatusOK, map[string]any{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0})
			return
		}
	}
	if err := validateCatalogSort(query); err != nil {
		writeCatalogSortError(w, err)
		return
	}

	startIndex := 0
	if start, ok := queryInt(query, "StartIndex"); ok && start > 0 {
		startIndex = start
	}
	limit := 20
	if requested, ok := queryInt(query, "Limit"); ok && requested > 0 {
		limit = requested
	}
	pageQuery := cloneValues(r.URL.Query())
	if _, ok := queryInt(pageQuery, "Limit"); !ok || query.Get("Limit") == "0" {
		pageQuery.Set("Limit", strconv.Itoa(limit))
	}
	desiredEnd := startIndex + limit
	const batchSize = 100
	var enriched []map[string]any
	incomplete := false
	for offset := 0; offset < localFilterScanLimit; offset += batchSize {
		if r.Context().Err() != nil {
			incomplete = true
			break
		}
		page, err := a.WatchStore.GetResumeItemsPage(reqCtx.ProxyUser.UserID, batchSize, offset)
		if err != nil {
			if a.Logger != nil {
				a.Logger.Warnf("WatchStore resume page read error: %s", redactURLInError(err))
			}
			writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Failed to read resume history"})
			return
		}
		if len(page) == 0 {
			break
		}
		var candidates []WatchProgress
		for i := range page {
			if scope != nil && scope.kind == "Series" && page[i].SeriesVirtualID != parentID {
				continue
			}
			if _, _, ok := a.resolveWatchItemServer(reqCtx, &page[i]); !ok {
				continue
			}
			candidates = append(candidates, page[i])
		}
		batchItems := a.enrichWatchItems(r, reqCtx, candidates)
		progressByID := make(map[string]WatchProgress, len(candidates))
		for _, candidate := range candidates {
			progressByID[candidate.VirtualItemID] = candidate
		}
		for _, item := range batchItems {
			if scope != nil {
				progress, ok := progressByID[itemID(item)]
				if !ok {
					continue
				}
				matched, uncertain := a.resumeItemInScope(r, reqCtx, scope, progress, item)
				if uncertain {
					incomplete = true
				}
				if !matched {
					continue
				}
			}
			enriched = append(enriched, item)
		}
		if desiredEnd > 0 && len(enriched) >= desiredEnd {
			enriched = enriched[:desiredEnd]
			if len(page) == batchSize {
				// A full WatchStore page can have a tail that was not scanned;
				// report only the confirmed count as a lower bound.
				incomplete = true
			}
			break
		}
		if len(page) < batchSize {
			break
		}
		if offset+batchSize >= localFilterScanLimit && len(page) == batchSize {
			incomplete = true
		}
	}
	if scope != nil && scope.incomplete {
		incomplete = true
	}
	if err := sortCatalogItems(enriched, query); err != nil {
		writeCatalogSortError(w, err)
		return
	}
	payload := paginateItems(enriched, pageQuery)
	if incomplete {
		markIncompleteResults(w, payload)
	}
	a.writeParentalItemsResponse(w, r, payload)
}

// enrichWatchItems fetches fresh metadata from upstream for a list of WatchProgress items,
// groups by server, batch-fetches via GET /Items?Ids=..., overlays local UserData.
// When a recorded server is offline, items are remapped to an online OtherInstance via IDStore.
func (a *App) enrichWatchItems(r *http.Request, reqCtx *RequestContext, items []WatchProgress) []map[string]any {
	cfg := a.ConfigStore.Snapshot()

	// Group by server ID, remapping offline servers to online alternatives
	groups := map[string][]string{}
	for i := range items {
		serverID, originalID, ok := a.resolveWatchItemServer(reqCtx, &items[i])
		if !ok {
			continue // all instances offline
		}
		items[i].ServerID = serverID
		items[i].OriginalItemID = originalID
		groups[serverID] = append(groups[serverID], originalID)
	}

	fetched := a.fetchWatchMetadata(r, reqCtx, groups)

	// Build result in original order, overlay local UserData
	var result []map[string]any
	for _, wp := range items {
		metadata, ok := fetched[watchMetadataKey{wp.ServerID, wp.OriginalItemID}]
		if !ok {
			continue
		}
		item := deepCloneMap(metadata)
		// Rewrite upstream IDs to virtual
		rewriteResponseIDs(item, wp.ServerID, a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
		// Overlay local UserData
		ud, _ := item["UserData"].(map[string]any)
		if ud == nil {
			ud = map[string]any{}
		}
		ud["PlaybackPositionTicks"] = wp.PositionTicks
		ud["Played"] = wp.Played
		ud["IsFavorite"] = wp.IsFavorite
		item["UserData"] = ud
		result = append(result, item)
	}
	return result
}

// resolveWatchItemServer returns the serverID and originalItemID to use for
// fetching metadata. A recorded server that is offline or has aggregation
// paused is skipped, and the item is remapped to an enabled, online, permitted
// copy via IDStore.
func (a *App) resolveWatchItemServer(reqCtx *RequestContext, wp *WatchProgress) (serverID string, originalItemID string, ok bool) {
	client := a.Upstream.ClientByID(wp.ServerID)
	if client != nil && client.IsOnline() && a.canUseAggregatedServer(reqCtx, wp.ServerID) {
		return wp.ServerID, wp.OriginalItemID, true
	}
	resolved := a.IDStore.ResolveVirtualID(wp.VirtualItemID)
	if resolved == nil {
		return "", "", false
	}
	// Try IDStore primary (may differ from wp.ServerID if mapping was updated)
	if resolved.ServerID != wp.ServerID {
		alt := a.Upstream.ClientByID(resolved.ServerID)
		if alt != nil && alt.IsOnline() && a.canUseAggregatedServer(reqCtx, resolved.ServerID) {
			return resolved.ServerID, resolved.OriginalID, true
		}
	}
	// Try OtherInstances
	for _, other := range resolved.OtherInstances {
		alt := a.Upstream.ClientByID(other.ServerID)
		if alt != nil && alt.IsOnline() && a.canUseAggregatedServer(reqCtx, other.ServerID) {
			return other.ServerID, other.OriginalID, true
		}
	}
	return "", "", false
}

// joinComma joins strings with commas.
func joinComma(ss []string) string {
	if len(ss) == 0 {
		return ""
	}
	result := ss[0]
	for _, s := range ss[1:] {
		result += "," + s
	}
	return result
}

// queryInt extracts an integer from a url.Values query parameter.
func queryInt(values url.Values, key string) (int, bool) {
	s := strings.TrimSpace(values.Get(key))
	if s == "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}
