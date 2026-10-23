package backend

import (
	"net/http"
	"net/url"
	"strconv"
	"sync"
)

func (a *App) handleShowsNextUp(w http.ResponseWriter, r *http.Request) {
	if err := validateCatalogSort(r.URL.Query()); err != nil {
		writeCatalogSortError(w, err)
		return
	}
	reqCtx := requestContextFrom(r.Context())
	if a.WatchStore != nil && reqCtx != nil && reqCtx.ProxyUser != nil && reqCtx.ProxyUser.Role != "admin" {
		a.handleLocalNextUp(w, r, reqCtx)
		return
	}
	query := cloneValues(r.URL.Query())
	seriesID := query.Get("SeriesId")
	if seriesID != "" {
		resolved, routeStatus := a.resolveRouteForRequest(reqCtx, seriesID)
		if routeStatus != 0 {
			writeJSON(w, http.StatusOK, map[string]any{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0})
			return
		}
		if !a.requireServerAccess(w, r, resolved) {
			return
		}
		instances := a.collectAllowedInstances(reqCtx, resolved)
		originalIDs := map[string]struct{}{}
		for _, inst := range instances {
			originalIDs[inst.OriginalID] = struct{}{}
		}
		for _, inst := range instances {
			instQuery := cloneValues(query)
			instQuery.Set("SeriesId", inst.OriginalID)
			instQuery.Set("StartIndex", "0")
			instQuery.Set("Limit", strconv.Itoa(mergedItemsScanLimit))
			payload, err := inst.Client.RequestJSON(r.Context(), reqCtx, a.Identity, http.MethodGet, "/Shows/NextUp", instQuery, nil)
			if err != nil {
				continue
			}
			filtered := filterSeriesItems(asItems(payload), originalIDs)
			filtered = a.parentalFilterServerItems(r, inst.Client, filtered)
			if len(filtered) > 0 {
				a.rewriteItems(filtered, inst.ServerID, a.clientFacingUserIDFor(r))
				if err := sortCatalogItems(filtered, r.URL.Query()); err != nil {
					writeCatalogSortError(w, err)
					return
				}
				if !a.strictSanitizedUserDataItems(w, r, filtered) {
					return
				}
				a.writeParentalItemsResponse(w, r, paginateItems(filtered, r.URL.Query()))
				return
			}
		}
		a.writeParentalItemsResponse(w, r, paginateItems(nil, r.URL.Query()))
		return
	}
	results, incomplete := a.fetchItemsAcrossUpstreams(r.Context(), reqCtx, "/Shows/NextUp", query, nil)
	a.writeMergedParentalResponse(w, r, results, incomplete)
}

// handleLocalNextUp computes NextUp from local WatchStore for non-admin users.
// The WatchStore order is retained unless the client requests an explicit catalog sort.
func (a *App) handleLocalNextUp(w http.ResponseWriter, r *http.Request, reqCtx *RequestContext) {
	query := cloneValues(r.URL.Query())
	if err := validateCatalogSort(query); err != nil {
		writeCatalogSortError(w, err)
		return
	}
	cfg := a.ConfigStore.Snapshot()
	empty := []map[string]any{}
	seriesID := query.Get("SeriesId")
	if seriesID != "" {
		resolved, routeStatus := a.resolveRouteForRequest(reqCtx, seriesID)
		if routeStatus != 0 {
			a.writeLocalNextUpPage(w, r, empty, false)
			return
		}
		if !a.requireServerAccess(w, r, resolved) {
			return
		}
		seriesProgress, err := a.WatchStore.GetNextUpForSeries(reqCtx.ProxyUser.UserID, seriesID)
		if err != nil {
			if a.Logger != nil {
				a.Logger.Warnf("WatchStore next-up read error: %s", redactURLInError(err))
			}
			a.writeLocalNextUpPage(w, r, empty, false)
			return
		}
		if seriesProgress == nil {
			a.writeLocalNextUpPage(w, r, empty, false)
			return
		}
		nextEp := a.fetchNextEpisode(r, reqCtx, resolved.Client, resolved.OriginalID, resolved.ServerID, seriesProgress)
		if nextEp == nil {
			a.writeLocalNextUpPage(w, r, empty, false)
			return
		}
		rewriteResponseIDs(nextEp, resolved.ServerID, a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
		if !a.strictSanitizedUserDataItems(w, r, []map[string]any{nextEp}) {
			return
		}
		a.writeLocalNextUpPage(w, r, []map[string]any{nextEp}, false)
		return
	}

	seriesList, err := a.WatchStore.GetNextUpSeries(reqCtx.ProxyUser.UserID)
	if err != nil || len(seriesList) == 0 {
		a.writeLocalNextUpPage(w, r, empty, false)
		return
	}
	incomplete := false
	if len(seriesList) > localFilterScanLimit {
		seriesList = seriesList[:localFilterScanLimit]
		incomplete = true
	}
	startIndex := 0
	if start, ok := queryInt(query, "StartIndex"); ok && start > 0 {
		startIndex = start
	}
	limit := 20
	if requested, ok := queryInt(query, "Limit"); ok && requested > 0 {
		limit = requested
	}
	desiredEnd := startIndex + limit
	fullOrder := len(splitQueryList(firstQueryValue(query, "SortBy", "sortBy"))) > 0
	nextUpBatch := 8
	if !fullOrder && limit < nextUpBatch {
		nextUpBatch = limit
	}
	var nextUpItems []map[string]any
	for start := 0; start < len(seriesList); start += nextUpBatch {
		if !fullOrder && desiredEnd > 0 && len(nextUpItems) >= desiredEnd {
			if start < len(seriesList) {
				incomplete = true
			}
			break
		}
		if r.Context().Err() != nil {
			incomplete = true
			break
		}
		end := min(start+nextUpBatch, len(seriesList))
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
			go func(i int, client *UpstreamClient, seriesOrigID, serverID string, progress WatchProgress) {
				defer wg.Done()
				episodes[i] = a.fetchNextEpisode(r, reqCtx, client, seriesOrigID, serverID, &progress)
			}(i, client, seriesOrigID, serverID, batch[i])
		}
		wg.Wait()
		for i, nextEp := range episodes {
			if nextEp == nil {
				continue
			}
			rewriteResponseIDs(nextEp, serverIDs[i], a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
			if id := itemID(nextEp); id != "" && !a.strictSanitizedUserData(w, r, id, nextEp) {
				continue
			}
			nextUpItems = append(nextUpItems, nextEp)
		}
		if !fullOrder && desiredEnd > 0 && len(nextUpItems) >= desiredEnd && end < len(seriesList) {
			incomplete = true
			break
		}
	}
	a.writeLocalNextUpPage(w, r, nextUpItems, incomplete)
}

func (a *App) writeLocalNextUpPage(w http.ResponseWriter, r *http.Request, items []map[string]any, incomplete bool) {
	if err := sortCatalogItems(items, r.URL.Query()); err != nil {
		writeCatalogSortError(w, err)
		return
	}
	pageQuery := cloneValues(r.URL.Query())
	if _, ok := queryInt(pageQuery, "Limit"); !ok || pageQuery.Get("Limit") == "0" {
		pageQuery.Set("Limit", "20")
	}
	payload := paginateItems(items, pageQuery)
	if incomplete {
		markIncompleteResults(w, payload)
	}
	a.writeParentalItemsResponse(w, r, payload)
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
