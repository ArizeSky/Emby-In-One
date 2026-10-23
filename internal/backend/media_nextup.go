package backend

import (
	"net/http"
	"net/url"
	"strconv"
	"sync"
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
	results, incomplete := a.fetchItemsAcrossUpstreams(r.Context(), requestContextFrom(r.Context()), "/Shows/NextUp", query, nil)
	a.writeMergedParentalResponse(w, r, results, incomplete)
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
			a.writeParentalItemsResponse(w, r, empty)
			return
		}
		if !a.requireServerAccess(w, r, resolved) {
			return
		}
		// Get the user's highest played episode for this series
		seriesProgress, err := a.WatchStore.GetNextUpForSeries(reqCtx.ProxyUser.UserID, seriesID)
		if err != nil {
			// Same rule as the userdata overlay: a failed read is not "no record".
			if a.Logger != nil {
				a.Logger.Warnf("WatchStore next-up read error: %s", redactURLInError(err))
			}
			a.writeParentalItemsResponse(w, r, empty)
			return
		}
		if seriesProgress == nil {
			a.writeParentalItemsResponse(w, r, empty)
			return
		}
		nextEp := a.fetchNextEpisode(r, reqCtx, resolved.Client, resolved.OriginalID, resolved.ServerID, seriesProgress)
		if nextEp == nil {
			a.writeParentalItemsResponse(w, r, empty)
			return
		}
		rewriteResponseIDs(nextEp, resolved.ServerID, a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
		a.overlayLocalUserDataItems(r, []map[string]any{nextEp})
		a.writeParentalItemsResponse(w, r, map[string]any{"Items": []any{nextEp}, "TotalRecordCount": 1, "StartIndex": 0})
		return
	}

	// No SeriesId: compute for all series the user is watching
	seriesList, err := a.WatchStore.GetNextUpSeries(reqCtx.ProxyUser.UserID)
	if err != nil || len(seriesList) == 0 {
		a.writeParentalItemsResponse(w, r, empty)
		return
	}
	if len(seriesList) > localFilterScanLimit {
		seriesList = seriesList[:localFilterScanLimit]
		markIncompleteResultsHeader(w)
	}

	var nextUpItems []map[string]any
	limit := 20
	if l, ok := queryInt(query, "Limit"); ok && l > 0 {
		limit = l
	}

	// nextUpBatch bounds both the concurrency and the over-fetch: series are
	// fetched in waves of at most this many, in list order, and the wave stops
	// once the client's Limit is satisfied. The requests used to run strictly
	// serially, so a user with N in-progress series waited N round trips
	// before the first item rendered.
	nextUpBatch := 8
	if limit < nextUpBatch {
		nextUpBatch = limit
	}

	for start := 0; start < len(seriesList) && len(nextUpItems) < limit; start += nextUpBatch {
		if r.Context().Err() != nil {
			markIncompleteResultsHeader(w)
			break
		}
		end := start + nextUpBatch
		if end > len(seriesList) {
			end = len(seriesList)
		}
		batch := seriesList[start:end]
		episodes := make([]map[string]any, len(batch))
		serverIDs := make([]string, len(batch))
		var wg sync.WaitGroup
		for i := range batch {
			if batch[i].SeriesOriginalID == "" {
				continue
			}
			serverID, seriesOrigID, client := a.resolveSeriesServer(reqCtx, &batch[i])
			if client == nil {
				continue
			}
			serverIDs[i] = serverID
			wg.Add(1)
			go func(i int, client *UpstreamClient, seriesOrigID, serverID string, sp WatchProgress) {
				defer wg.Done()
				episodes[i] = a.fetchNextEpisode(r, reqCtx, client, seriesOrigID, serverID, &sp)
			}(i, client, seriesOrigID, serverID, batch[i])
		}
		wg.Wait()
		// Rewrite and overlay sequentially after the wave: the result order
		// stays the series order the store returned.
		for i, nextEp := range episodes {
			if nextEp == nil || len(nextUpItems) >= limit {
				continue
			}
			rewriteResponseIDs(nextEp, serverIDs[i], a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
			if id, _ := nextEp["Id"].(string); id != "" {
				a.overlayLocalUserData(r, id, nextEp)
			}
			nextUpItems = append(nextUpItems, nextEp)
		}
	}

	a.writeParentalItemsResponse(w, r, map[string]any{
		"Items":            toAnySlice(nextUpItems),
		"TotalRecordCount": len(nextUpItems),
		"StartIndex":       0,
	})
}

// resolveSeriesServer returns an online, aggregation-enabled, permitted
// server/client for a series watch entry. If the recorded server is offline or
// paused, it tries the IDStore primary and OtherInstances.
func (a *App) resolveSeriesServer(reqCtx *RequestContext, sp *WatchProgress) (serverID string, seriesOriginalID string, client *UpstreamClient) {
	c := a.Upstream.ClientByID(sp.ServerID)
	if c != nil && c.IsOnline() && a.canUseAggregatedServer(reqCtx, sp.ServerID) {
		return sp.ServerID, sp.SeriesOriginalID, c
	}
	if sp.SeriesVirtualID == "" {
		return "", "", nil
	}
	resolved := a.IDStore.ResolveVirtualID(sp.SeriesVirtualID)
	if resolved == nil {
		return "", "", nil
	}
	if resolved.ServerID != sp.ServerID {
		alt := a.Upstream.ClientByID(resolved.ServerID)
		if alt != nil && alt.IsOnline() && a.canUseAggregatedServer(reqCtx, resolved.ServerID) {
			return resolved.ServerID, resolved.OriginalID, alt
		}
	}
	for _, other := range resolved.OtherInstances {
		alt := a.Upstream.ClientByID(other.ServerID)
		if alt != nil && alt.IsOnline() && a.canUseAggregatedServer(reqCtx, other.ServerID) {
			return other.ServerID, other.OriginalID, alt
		}
	}
	return "", "", nil
}

// fetchNextEpisode queries the upstream for the next unwatched episode after
// the user's last played episode in a series.
func (a *App) fetchNextEpisode(r *http.Request, reqCtx *RequestContext, client *UpstreamClient, seriesOriginalID string, serverID string, lastPlayed *WatchProgress) map[string]any {
	q := url.Values{}
	q.Set("Fields", "BasicSyncInfo,CanDelete,PrimaryImageAspectRatio,Overview,DateCreated,MediaSources,Path,SortName,Studios,Taglines,Genres,CommunityRating,OfficialRating,CustomRating,Type,SeriesId,SeasonId,ParentId,CumulativeRunTimeTicks,Chapters,ProviderIds")
	q.Set("UserId", client.clientUserID())
	q.Set("Season", strconv.Itoa(lastPlayed.ParentIndexNumber))
	q.Set("SortBy", "SortName")
	q.Set("SortOrder", "Ascending")

	items := a.fetchNextUpSeason(r, reqCtx, client, seriesOriginalID, serverID, q)
	if next := a.firstAllowedNextEpisode(r, client, nextEpisodeCandidates(items, lastPlayed)); next != nil {
		return next
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
		if !a.parentalSessionFor(r).restricted() {
			q2.Set("Limit", "1")
		}
		return a.firstAllowedNextEpisode(r, client, a.fetchNextUpSeason(r, reqCtx, client, seriesOriginalID, serverID, q2))
	}
	return nil
}
