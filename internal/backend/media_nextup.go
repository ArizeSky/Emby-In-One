package backend

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// localNextUpSeriesLimit bounds per-series upstream work; it is not a catalog
// item or local-history scan limit.
const localNextUpSeriesLimit = 5000

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
			setQueryValue(instQuery, "SeriesId", inst.OriginalID)
			setQueryValue(instQuery, "StartIndex", "0")
			setQueryValue(instQuery, "Limit", strconv.Itoa(mergedItemsScanLimit))
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
	timeout := time.Duration(a.ConfigStore.Snapshot().Timeouts.Global) * time.Millisecond
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	r = r.WithContext(ctx)
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
		seriesProgress, err := a.nextUpProgress(reqCtx.ProxyUser.UserID, seriesID)
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
		nextEp, serverID, incomplete := a.fetchNextEpisodeAcrossCopies(r, reqCtx, seriesProgress)
		if nextEp == nil {
			a.writeLocalNextUpPage(w, r, empty, incomplete)
			return
		}
		rewriteResponseIDs(nextEp, serverID, a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
		if !a.strictSanitizedUserDataItems(w, r, []map[string]any{nextEp}) {
			return
		}
		a.writeLocalNextUpPage(w, r, []map[string]any{nextEp}, incomplete)
		return
	}

	seriesList, err := a.WatchStore.GetNextUpSeries(reqCtx.ProxyUser.UserID)
	if err != nil || len(seriesList) == 0 {
		a.writeLocalNextUpPage(w, r, empty, false)
		return
	}
	seriesList = a.foldNextUpSeries(seriesList)
	incomplete := false
	if len(seriesList) > localNextUpSeriesLimit {
		seriesList = seriesList[:localNextUpSeriesLimit]
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
		partial := make([]bool, len(batch))
		var wg sync.WaitGroup
		for i := range batch {
			wg.Add(1)
			go func(i int, progress WatchProgress) {
				defer wg.Done()
				episodes[i], serverIDs[i], partial[i] = a.fetchNextEpisodeAcrossCopies(r, reqCtx, &progress)
			}(i, batch[i])
		}
		wg.Wait()
		for i, nextEp := range episodes {
			incomplete = incomplete || partial[i]
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
	if _, ok := queryInt(pageQuery, "Limit"); !ok || queryValue(pageQuery, "Limit") == "0" {
		setQueryValue(pageQuery, "Limit", "20")
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
	if instances := a.watchSeriesInstances(reqCtx, sp); len(instances) > 0 {
		return instances[0].ServerID, instances[0].OriginalID, instances[0].Client
	}
	return "", "", nil
}
