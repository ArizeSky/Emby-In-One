package backend

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// mergedItemsScanLimit caps how many items one merged (no ParentId) item query may pull
// from each upstream.
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

// fetchMergedItemsFromUpstream collects one upstream's candidate set.
//
// By default the upstream is asked for the whole scan budget (mergedItemsScanLimit
// rows) in one request, which every stock Emby server accepts. An upstream with
// pagedScan set in its config is the exception: a modified Emby deployment answers a
// wider window with a bare 400, which would make every merged query fail on that
// server. Those are pulled with a per-request window capped at
// upstreamPageLimitCeiling, following StartIndex until the upstream runs out of rows
// or the scan budget is reached. An upstream that still refuses the capped window is
// retried once at fallbackUpstreamPageLimit, and pages already collected are kept, so
// a server with a lower ceiling is narrowed rather than dropped from the merge.
func (a *App) fetchMergedItemsFromUpstream(ctx context.Context, reqCtx *RequestContext, c *UpstreamClient, pathTemplate string, query url.Values, body any, budget *catalogRequestBudget) (items []map[string]any, complete bool, truncated bool, err error) {
	path := strings.Replace(pathTemplate, "%s", c.clientUserID(), 1)
	collected := make([]map[string]any, 0, upstreamPageLimitCeiling)

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

	if !c.Config.PagedScan {
		directQuery, queryErr := buildQuery(0, mergedItemsScanLimit)
		if errors.Is(queryErr, errBatchQueryNoItems) || errors.Is(queryErr, errTaxonomyQueryNotTranslatable) {
			return []map[string]any{}, true, false, nil
		}
		if queryErr != nil {
			return nil, false, false, queryErr
		}
		if !budget.tryAcquire() {
			return nil, false, true, nil
		}
		payload, requestErr := c.RequestJSON(ctx, reqCtx, a.Identity, http.MethodGet, path, directQuery, body)
		if requestErr != nil {
			return nil, false, true, requestErr
		}
		items := asItems(payload)
		if total, hasTotal := totalRecordCount(payload); hasTotal && len(items) < total {
			return items, false, true, nil
		}
		if len(items) >= mergedItemsScanLimit {
			return items, true, true, nil
		}
		return items, true, false, nil
	}

	effectiveCeiling := upstreamPageLimitCeiling
	offset := 0
	for len(collected) < mergedItemsScanLimit {
		window := min(effectiveCeiling, mergedItemsScanLimit-len(collected))
		pageQuery, queryErr := buildQuery(offset, window)
		if errors.Is(queryErr, errBatchQueryNoItems) || errors.Is(queryErr, errTaxonomyQueryNotTranslatable) {
			return []map[string]any{}, true, false, nil
		}
		if queryErr != nil {
			return collected, false, true, queryErr
		}
		if !budget.tryAcquire() {
			return collected, false, true, nil
		}
		payload, requestErr := c.RequestJSON(ctx, reqCtx, a.Identity, http.MethodGet, path, pageQuery, body)
		if requestErr != nil {
			if len(collected) == 0 && window > fallbackUpstreamPageLimit && isBadRequestError(requestErr) {
				if !budget.tryAcquire() {
					return nil, false, true, nil
				}
				retryQuery, retryQueryErr := buildQuery(0, fallbackUpstreamPageLimit)
				if retryQueryErr != nil {
					return nil, false, true, retryQueryErr
				}
				retryPayload, retryErr := c.RequestJSON(ctx, reqCtx, a.Identity, http.MethodGet, path, retryQuery, body)
				if retryErr != nil {
					return nil, false, true, retryErr
				}
				page := asItems(retryPayload)
				collected = append(collected, page...)
				offset = len(collected)
				effectiveCeiling = fallbackUpstreamPageLimit
				if len(page) < fallbackUpstreamPageLimit {
					if total, hasTotal := totalRecordCount(retryPayload); hasTotal && offset < total {
						return collected, false, true, nil
					}
					return collected, true, false, nil
				}
				continue
			}
			return collected, false, true, requestErr
		}

		page := asItems(payload)
		total, hasTotal := totalRecordCount(payload)
		if len(page) == 0 {
			if hasTotal && offset < total {
				return collected, false, true, nil
			}
			break
		}
		collected = append(collected, page...)
		offset = len(collected)
		if len(page) < window {
			if hasTotal && offset < total {
				continue
			}
			break
		}
	}

	if len(collected) >= mergedItemsScanLimit {
		return collected, true, true, nil
	}
	return collected, true, false, nil
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
