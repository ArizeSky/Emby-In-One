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
		resolved := a.resolveRouteID(parentID)
		if resolved == nil {
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
			if len(filtered) > 0 {
				a.rewriteItems(filtered, inst.ServerID, a.clientFacingUserIDFor(r))
				writeJSON(w, http.StatusOK, map[string]any{"Items": filtered, "TotalRecordCount": len(filtered), "StartIndex": 0})
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0})
		return
	}
	results := a.fetchItemsAcrossUpstreams(r.Context(), requestContextFrom(r.Context()), "/Users/%s/Items/Resume", query, nil)
	writeJSON(w, http.StatusOK, a.mergedItemsPayload(results, a.clientFacingUserIDFor(r)))
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

	items, err := a.WatchStore.GetResumeItems(reqCtx.ProxyUser.UserID, limit)
	if err != nil || len(items) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0})
		return
	}

	// If ParentId specified, filter to that series only
	if parentID != "" {
		var filtered []WatchProgress
		for _, item := range items {
			if item.SeriesVirtualID == parentID {
				filtered = append(filtered, item)
			}
		}
		items = filtered
		if len(items) == 0 {
			writeJSON(w, http.StatusOK, map[string]any{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0})
			return
		}
	}

	enriched := a.enrichWatchItems(r, reqCtx, items)
	writeJSON(w, http.StatusOK, map[string]any{
		"Items":            toAnySlice(enriched),
		"TotalRecordCount": len(enriched),
		"StartIndex":       0,
	})
}

// enrichWatchItems fetches fresh metadata from upstream for a list of WatchProgress items,
// groups by server, batch-fetches via GET /Items?Ids=..., overlays local UserData.
// When a recorded server is offline, items are remapped to an online OtherInstance via IDStore.
func (a *App) enrichWatchItems(r *http.Request, reqCtx *RequestContext, items []WatchProgress) []map[string]any {
	cfg := a.ConfigStore.Snapshot()

	// Group by server ID, remapping offline servers to online alternatives
	type serverGroup struct {
		originalIDs []string
		watchItems  []WatchProgress
	}
	groups := map[string]*serverGroup{}
	for i := range items {
		serverID, originalID, ok := a.resolveWatchItemServer(&items[i])
		if !ok {
			continue // all instances offline
		}
		items[i].ServerID = serverID
		items[i].OriginalItemID = originalID
		g, exists := groups[serverID]
		if !exists {
			g = &serverGroup{}
			groups[serverID] = g
		}
		g.originalIDs = append(g.originalIDs, originalID)
		g.watchItems = append(g.watchItems, items[i])
	}

	// Fetch metadata per server (all groups now point to online servers)
	fetched := map[string]map[string]any{} // originalID → item metadata
	for serverID, g := range groups {
		client := a.Upstream.ClientByID(serverID)
		if client == nil || !client.IsOnline() {
			continue
		}
		q := url.Values{}
		q.Set("Ids", joinComma(g.originalIDs))
		q.Set("Fields", "BasicSyncInfo,CanDelete,PrimaryImageAspectRatio,Overview,DateCreated,MediaSources,Path,SortName,Studios,Taglines,Genres,CommunityRating,OfficialRating,CumulativeRunTimeTicks,Chapters,ProviderIds")
		payload, err := client.RequestJSON(r.Context(), reqCtx, a.Identity, http.MethodGet, "/Items", q, nil)
		if err != nil {
			continue
		}
		for _, item := range asItems(payload) {
			if id, _ := item["Id"].(string); id != "" {
				fetched[id] = item
			}
		}
	}

	// Build result in original order, overlay local UserData
	var result []map[string]any
	for _, wp := range items {
		item, ok := fetched[wp.OriginalItemID]
		if !ok {
			continue
		}
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
// fetching metadata. If the item's recorded server is offline, it tries to find
// an online OtherInstance via IDStore.
func (a *App) resolveWatchItemServer(wp *WatchProgress) (serverID string, originalItemID string, ok bool) {
	client := a.Upstream.ClientByID(wp.ServerID)
	if client != nil && client.IsOnline() {
		return wp.ServerID, wp.OriginalItemID, true
	}
	resolved := a.IDStore.ResolveVirtualID(wp.VirtualItemID)
	if resolved == nil {
		return "", "", false
	}
	// Try IDStore primary (may differ from wp.ServerID if mapping was updated)
	if resolved.ServerID != wp.ServerID {
		alt := a.Upstream.ClientByID(resolved.ServerID)
		if alt != nil && alt.IsOnline() {
			return resolved.ServerID, resolved.OriginalID, true
		}
	}
	// Try OtherInstances
	for _, other := range resolved.OtherInstances {
		alt := a.Upstream.ClientByID(other.ServerID)
		if alt != nil && alt.IsOnline() {
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
