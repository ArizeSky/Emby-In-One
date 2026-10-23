package backend

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

func (a *App) handleUserItemsResume(w http.ResponseWriter, r *http.Request) {
	reqCtx := requestContextFrom(r.Context())
	// Non-admin users: serve resume from local WatchStore
	if a.WatchStore != nil && reqCtx != nil && reqCtx.ProxyUser != nil && reqCtx.ProxyUser.Role != "admin" {
		a.handleLocalResume(w, r, reqCtx)
		return
	}
	query := cloneValues(r.URL.Query())
	parentID := firstQueryValue(query, "ParentId", "parentId", "parentid")
	if parentID != "" {
		resolved, routeStatus := a.resolveRouteForRequest(reqCtx, parentID)
		if routeStatus != 0 {
			writeJSON(w, http.StatusOK, map[string]any{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0})
			return
		}
		if !a.requireServerAccess(w, r, resolved) {
			return
		}
		instances := a.collectAllowedInstances(requestContextFrom(r.Context()), resolved)
		originalIDs := map[string]struct{}{}
		for _, inst := range instances {
			originalIDs[inst.OriginalID] = struct{}{}
		}
		for _, inst := range instances {
			instQuery := cloneValues(query)
			instQuery.Set("ParentId", inst.OriginalID)
			instQuery.Set("UserId", inst.Client.clientUserID())
			instQuery.Del("parentId")
			instQuery.Del("parentid")
			payload, err := inst.Client.RequestJSON(r.Context(), requestContextFrom(r.Context()), a.Identity, http.MethodGet, "/Users/"+inst.Client.clientUserID()+"/Items/Resume", instQuery, nil)
			if err != nil {
				continue
			}
			filtered := filterSeriesItems(asItems(payload), originalIDs)
			filtered = a.parentalFilterServerItems(r, inst.Client, filtered)
			if len(filtered) > 0 {
				a.rewriteItems(filtered, inst.ServerID, a.clientFacingUserIDFor(r))
				a.writeParentalItemsResponse(w, r, map[string]any{"Items": filtered, "TotalRecordCount": len(filtered), "StartIndex": 0})
				return
			}
		}
		a.writeParentalItemsResponse(w, r, map[string]any{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0})
		return
	}
	results, incomplete := a.fetchItemsAcrossUpstreams(r.Context(), requestContextFrom(r.Context()), "/Users/%s/Items/Resume", query, nil)
	a.writeMergedParentalResponse(w, r, results, incomplete)
}

// handleLocalResume serves resume items from local WatchStore for non-admin users.
// It fetches item metadata from upstream servers to build complete responses.
func (a *App) handleLocalResume(w http.ResponseWriter, r *http.Request, reqCtx *RequestContext) {
	query := cloneValues(r.URL.Query())
	parentID := firstQueryValue(query, "ParentId", "parentId", "parentid")
	limit := 20
	if l, ok := queryInt(query, "Limit"); ok && l > 0 {
		limit = l
	}

	// Scan the deduped resume list in batches, keeping only candidates that pass
	// the ParentId filter and still resolve to an enabled, online, permitted
	// instance. Doing this before the enrich step means a run of recent records
	// that all live on paused servers no longer hides later playable entries
	// behind the client's Limit.
	const batchSize = 100
	var enriched []map[string]any
	truncated := false
	for offset := 0; offset < localFilterScanLimit; offset += batchSize {
		if r.Context().Err() != nil {
			truncated = true
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
			if parentID != "" && page[i].SeriesVirtualID != parentID {
				continue
			}
			if _, _, ok := a.resolveWatchItemServer(reqCtx, &page[i]); !ok {
				continue
			}
			candidates = append(candidates, page[i])
		}
		enriched = append(enriched, a.enrichWatchItems(r, reqCtx, candidates)...)
		if len(enriched) >= limit {
			enriched = enriched[:limit]
			break
		}
		if len(page) < batchSize {
			break
		}
		if offset+batchSize >= localFilterScanLimit && len(page) == batchSize {
			truncated = true
		}
	}
	if truncated && a.Logger != nil {
		a.Logger.Warnf("local resume scan reached %d records: results may be truncated", localFilterScanLimit)
	}

	payload := map[string]any{
		"Items":            toAnySlice(enriched),
		"TotalRecordCount": len(enriched),
		"StartIndex":       0,
	}
	if truncated {
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
