package backend

import (
	"net/http"
	"net/url"
	"strconv"
)

func (a *App) handleShowsNextUp(w http.ResponseWriter, r *http.Request) {
	reqCtx := requestContextFrom(r.Context())
	// Non-admin users: compute NextUp from local WatchStore
	if a.WatchStore != nil && reqCtx != nil && reqCtx.ProxyUser != nil && reqCtx.ProxyUser.Role != "admin" {
		a.handleLocalNextUp(w, r, reqCtx)
		return
	}
	query := cloneValues(r.URL.Query())
	seriesID := query.Get("SeriesId")
	if seriesID != "" {
		resolved := a.resolveRouteID(seriesID)
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
			instQuery.Set("SeriesId", inst.OriginalID)
			payload, err := inst.Client.RequestJSON(r.Context(), requestContextFrom(r.Context()), a.Identity, http.MethodGet, "/Shows/NextUp", instQuery, nil)
			if err != nil {
				continue
			}
			filtered := filterSeriesItems(asItems(payload), originalIDs)
			if len(filtered) > 0 {
				a.rewriteItems(filtered, inst.ServerIndex, a.clientFacingUserIDFor(r))
				writeJSON(w, http.StatusOK, map[string]any{"Items": filtered, "TotalRecordCount": len(filtered), "StartIndex": 0})
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0})
		return
	}
	results := a.fetchItemsAcrossUpstreams(r.Context(), requestContextFrom(r.Context()), "/Shows/NextUp", query, nil)
	writeJSON(w, http.StatusOK, a.mergedItemsPayload(results, a.clientFacingUserIDFor(r)))
}

// handleLocalNextUp computes NextUp from local WatchStore for non-admin users.
// For each series the user has watched, queries upstream for the next unwatched episode.
func (a *App) handleLocalNextUp(w http.ResponseWriter, r *http.Request, reqCtx *RequestContext) {
	query := cloneValues(r.URL.Query())
	cfg := a.ConfigStore.Snapshot()
	empty := map[string]any{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0}

	// If SeriesId specified, only compute for that series
	seriesID := query.Get("SeriesId")
	if seriesID != "" {
		resolved := a.resolveRouteID(seriesID)
		if resolved == nil {
			writeJSON(w, http.StatusOK, empty)
			return
		}
		if !a.requireServerAccess(w, r, resolved) {
			return
		}
		// Get the user's highest played episode for this series
		seriesProgress := a.WatchStore.GetNextUpForSeries(reqCtx.ProxyUser.UserID, seriesID)
		if seriesProgress == nil {
			writeJSON(w, http.StatusOK, empty)
			return
		}
		nextEp := a.fetchNextEpisode(r, reqCtx, resolved.Client, resolved.OriginalID, resolved.ServerIndex, seriesProgress)
		if nextEp == nil {
			writeJSON(w, http.StatusOK, empty)
			return
		}
		rewriteResponseIDs(nextEp, resolved.ServerIndex, a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
		a.overlayLocalUserDataItems(r, []map[string]any{nextEp})
		writeJSON(w, http.StatusOK, map[string]any{"Items": []any{nextEp}, "TotalRecordCount": 1, "StartIndex": 0})
		return
	}

	// No SeriesId: compute for all series the user is watching
	seriesList, err := a.WatchStore.GetNextUpSeries(reqCtx.ProxyUser.UserID)
	if err != nil || len(seriesList) == 0 {
		writeJSON(w, http.StatusOK, empty)
		return
	}

	var nextUpItems []map[string]any
	limit := 20
	if l, ok := queryInt(query, "Limit"); ok && l > 0 {
		limit = l
	}

	for _, sp := range seriesList {
		if len(nextUpItems) >= limit {
			break
		}
		if sp.SeriesOriginalID == "" {
			continue
		}
		serverIdx, seriesOrigID, client := a.resolveSeriesServer(&sp)
		if client == nil {
			continue
		}
		nextEp := a.fetchNextEpisode(r, reqCtx, client, seriesOrigID, serverIdx, &sp)
		if nextEp != nil {
			rewriteResponseIDs(nextEp, serverIdx, a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
			if id, _ := nextEp["Id"].(string); id != "" {
				a.overlayLocalUserData(r, id, nextEp)
			}
			nextUpItems = append(nextUpItems, nextEp)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"Items":            toAnySlice(nextUpItems),
		"TotalRecordCount": len(nextUpItems),
		"StartIndex":       0,
	})
}

// resolveSeriesServer returns an online server/client for a series watch entry.
// If the recorded server is offline, it tries OtherInstances via IDStore.
func (a *App) resolveSeriesServer(sp *WatchProgress) (serverIndex int, seriesOriginalID string, client *UpstreamClient) {
	c := a.Upstream.GetClient(sp.ServerIndex)
	if c != nil && c.IsOnline() {
		return sp.ServerIndex, sp.SeriesOriginalID, c
	}
	if sp.SeriesVirtualID == "" {
		return 0, "", nil
	}
	resolved := a.IDStore.ResolveVirtualID(sp.SeriesVirtualID)
	if resolved == nil {
		return 0, "", nil
	}
	if resolved.ServerIndex != sp.ServerIndex {
		alt := a.Upstream.GetClient(resolved.ServerIndex)
		if alt != nil && alt.IsOnline() {
			return resolved.ServerIndex, resolved.OriginalID, alt
		}
	}
	for _, other := range resolved.OtherInstances {
		alt := a.Upstream.GetClient(other.ServerIndex)
		if alt != nil && alt.IsOnline() {
			return other.ServerIndex, other.OriginalID, alt
		}
	}
	return 0, "", nil
}

// fetchNextEpisode queries the upstream for the next unwatched episode after
// the user's last played episode in a series.
func (a *App) fetchNextEpisode(r *http.Request, reqCtx *RequestContext, client *UpstreamClient, seriesOriginalID string, serverIndex int, lastPlayed *WatchProgress) map[string]any {
	q := url.Values{}
	q.Set("Fields", "BasicSyncInfo,CanDelete,PrimaryImageAspectRatio,Overview,DateCreated,MediaSources,Path,SortName,Studios,Taglines,Genres,CommunityRating,OfficialRating,CumulativeRunTimeTicks,Chapters,ProviderIds")
	q.Set("UserId", client.clientUserID())
	q.Set("Season", strconv.Itoa(lastPlayed.ParentIndexNumber))
	q.Set("SortBy", "SortName")
	q.Set("SortOrder", "Ascending")

	payload, err := client.RequestJSON(r.Context(), reqCtx, a.Identity, http.MethodGet, "/Shows/"+seriesOriginalID+"/Episodes", q, nil)
	if err != nil {
		return nil
	}
	items := asItems(payload)
	// Find the first episode after the user's last played one
	foundCurrent := false
	for _, ep := range items {
		parentIdx, _ := numericInt(ep["ParentIndexNumber"])
		idx, _ := numericInt(ep["IndexNumber"])

		if parentIdx == lastPlayed.ParentIndexNumber && idx == lastPlayed.IndexNumber {
			foundCurrent = true
			// If the current episode is not fully played yet (still in progress),
			// it IS the next-up item (user hasn't finished it)
			if !lastPlayed.Played && lastPlayed.PositionTicks > 0 {
				return ep
			}
			continue
		}
		if foundCurrent {
			return ep
		}
		// Handle case where episodes are in a later season
		if parentIdx > lastPlayed.ParentIndexNumber {
			return ep
		}
	}

	// If we didn't find a next episode in this season and the current one was played,
	// try the next season
	if lastPlayed.Played {
		q2 := url.Values{}
		q2.Set("Fields", q.Get("Fields"))
		q2.Set("UserId", client.clientUserID())
		q2.Set("Season", strconv.Itoa(lastPlayed.ParentIndexNumber+1))
		q2.Set("SortBy", "SortName")
		q2.Set("SortOrder", "Ascending")
		q2.Set("Limit", "1")

		payload2, err2 := client.RequestJSON(r.Context(), reqCtx, a.Identity, http.MethodGet, "/Shows/"+seriesOriginalID+"/Episodes", q2, nil)
		if err2 == nil {
			items2 := asItems(payload2)
			if len(items2) > 0 {
				return items2[0]
			}
		}
	}
	return nil
}
