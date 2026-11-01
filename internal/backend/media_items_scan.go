package backend

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// mergedItemsScanLimit is the initial stock-upstream window and per-fill row
// budget. Snapshot continuation may collect additional rows on later requests.
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

// fetchMergedItemsFromUpstream collects one upstream's raw candidate prefix.
//
// The bounded resumable state lives in the App-local catalogSnapshotStore. This
// method only adapts the existing upstream request (query preparation, shared
// budget, HTTP and response decoding) into a page fetcher, then hands the store
// the resulting raw prefix. The first Fill of an equivalent query collects at
// most the 5000-candidate round; a later equivalent query continues from the
// retained offset. An upstream marked pagedScan is walked in 200-row windows
// (100 after a 400 fallback), and every incomplete prefix stays marked
// incomplete for the caller.
func (a *App) fetchMergedItemsFromUpstream(ctx context.Context, reqCtx *RequestContext, c *UpstreamClient, pathTemplate string, query url.Values, body any, budget *catalogRequestBudget) (items []map[string]any, complete bool, truncated bool, err error) {
	path := strings.Replace(pathTemplate, "%s", c.clientUserID(), 1)
	paged := c.Config.PagedScan

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

	fetchPage := func(pageCtx context.Context, offset, limit int) (catalogSnapshotPage, error) {
		pageQuery, queryErr := buildQuery(offset, limit)
		if errors.Is(queryErr, errBatchQueryNoItems) || errors.Is(queryErr, errTaxonomyQueryNotTranslatable) {
			return catalogSnapshotPage{Items: []map[string]any{}, Total: 0, HasTotal: true}, nil
		}
		if queryErr != nil {
			return catalogSnapshotPage{}, queryErr
		}
		if !budget.tryAcquire() {
			return catalogSnapshotPage{BudgetExhausted: true}, nil
		}
		payload, requestErr := c.RequestJSON(pageCtx, reqCtx, a.Identity, http.MethodGet, path, pageQuery, body)
		if requestErr != nil {
			if paged && isBadRequestError(requestErr) {
				return catalogSnapshotPage{BadRequest: true}, nil
			}
			return catalogSnapshotPage{}, requestErr
		}
		page := asItems(payload)
		total, hasTotal := totalRecordCount(payload)
		return catalogSnapshotPage{Items: page, Total: total, HasTotal: hasTotal}, nil
	}

	key, keyErr := catalogSnapshotKey(a, reqCtx, c, pathTemplate, query, body)
	if keyErr != nil {
		return nil, false, true, keyErr
	}
	view, fillErr := a.catalogSnapshots.Fill(ctx, key, catalogSnapshotSpec{Paged: paged}, fetchPage)
	return view.Items, view.Complete, !view.Complete, fillErr
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

// warnTruncatedMerge reports a partial candidate set, whose total is a lower
// bound until a later continuation finishes the scan.
func (a *App) warnTruncatedMerge(count int) {
	if a.Logger == nil || !a.noticeThrottle.allow("merged-scan") {
		return
	}
	a.Logger.Warnf("merged item query has %d confirmed candidates: results are incomplete; the total is a lower bound and later equivalent requests may continue the scan", count)
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
