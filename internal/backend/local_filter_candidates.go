package backend

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// localFilterBatchLimit bounds how many physical upstream IDs one ID-driven
// catalog attempt may request. A local filter answers from the user's own
// records, so the candidate set is known and does not need the 5000-row library
// scan the old path used: asking for an ID batch must never turn into a request
// for the whole library.
const localFilterBatchLimit = 100

// localFilterBatchMaxIDBytes bounds the encoded ID list independently of the
// item count. IDs are usually short, but a modified upstream can issue long
// ones; an unbounded comma list would eventually make the URL unusable.
const localFilterBatchMaxIDBytes = 3000

// localFilterTarget is one allowed upstream plus the physical IDs the local
// positive set resolves to on that upstream.
type localFilterTarget struct {
	client *UpstreamClient
	ids    []string
}

// fetchLocalFilterCandidates collects the upstream items that back the local
// user-state filter. It is the ID-driven replacement for the bounded full
// library scan: the positive local records (favorite/played/resumable, with
// their AND semantics) decide which physical IDs are requested, and every
// resolved virtual ID is translated through the IDStore instead of trusting the
// skeleton fields on a WatchStore row.
//
// The returned results are raw upstream DTOs in per-server order; the caller
// still owns parental visibility, merge/dedup, ID rewriting, local sorting,
// UserData sanitising and pagination. A true second return value means the
// candidate set is a lower bound (unresolved local IDs, an upstream error, a
// suspicious response, a truncated total or an exhausted request budget), so
// the caller must attach the incomplete-results marker. Missing requested IDs
// are not by themselves incomplete: they may be deleted, filtered or
// inaccessible on that upstream, and their local state must be preserved.
func (a *App) fetchLocalFilterCandidates(r *http.Request, query url.Values, pathTemplate, onlyServerID string, f userStateFilter) ([]upstreamItemsResult, bool) {
	if a == nil || a.IDStore == nil || a.WatchStore == nil || r == nil || !f.active() || !isRegularProxyUser(r) {
		return nil, false
	}
	reqCtx := requestContextFrom(r.Context())
	if reqCtx == nil || reqCtx.ProxyUser == nil {
		return nil, false
	}
	// This variant is error-aware on purpose: an unreadable local record is not
	// the same answer as an empty one, and must not turn a database failure into
	// a complete empty candidate set.
	matched, err := a.localFilterStateRows(reqCtx.ProxyUser.UserID, f)
	if err != nil {
		if a.Logger != nil {
			a.Logger.Warnf("local user-state filter read failed: %s", redactURLInError(err))
		}
		return nil, true
	}
	if len(matched) == 0 {
		// No positive local record: the answer is a complete empty set and no
		// upstream is contacted at all.
		return nil, false
	}

	targets, unresolved := a.localFilterTargets(reqCtx, matched, onlyServerID)
	base := cloneValues(query)
	// Remove the filters the proxy answers itself, keeping every passthrough
	// value and every other directory predicate exactly as the client sent it.
	f.stripFrom(base)
	ensureSortFields(base)
	if predicates := localFilterClientIDPredicates(base); len(predicates) > 0 {
		targets = a.intersectLocalFilterTargets(targets, predicates)
	}
	if len(targets) == 0 {
		return nil, unresolved
	}

	cfg := a.ConfigStore.Snapshot()
	globalTimeout := time.Duration(cfg.Timeouts.Global) * time.Millisecond
	if globalTimeout <= 0 {
		globalTimeout = 15 * time.Second
	}
	// The catalog budget is shared with the parental metadata session when one
	// exists; every ID batch attempt draws one token from it.
	budget := newCatalogRequestBudget()
	if reqCtx.Parental != nil {
		budget = reqCtx.Parental.catalog
	}

	tasks := make([]upstreamTask, 0, len(targets))
	for i, target := range targets {
		task := target
		tasks = append(tasks, upstreamTask{
			index: i,
			fn: func(ctx context.Context) upstreamItemsResult {
				return a.fetchLocalFilterTarget(ctx, reqCtx, task, pathTemplate, base, budget)
			},
		})
	}
	// Each target may issue several sequential ID batches, like a paged scan, so
	// wait for every task (bounded by the global timeout) instead of returning a
	// fast server's page without the slow server's tail.
	results, report := a.aggregateUpstreamsWithReport(r.Context(), aggregationConfig{
		globalTimeout: globalTimeout,
	}, tasks)
	return results, unresolved || report.Incomplete
}

// localFilterTargets resolves the local virtual IDs through the IDStore and
// groups the known physical IDs per allowed, online upstream. Unknown virtual
// IDs are skipped: forwarding them as raw upstream IDs would leak the local
// record to an unrelated server and could return an item by coincidence.
func (a *App) localFilterTargets(reqCtx *RequestContext, matched map[string]WatchProgress, onlyServerID string) ([]localFilterTarget, bool) {
	allowed := map[string]*UpstreamClient{}
	order := make([]*UpstreamClient, 0)
	for _, client := range a.allowedClients(reqCtx) {
		if onlyServerID != "" && client.ID != onlyServerID {
			continue
		}
		allowed[client.ID] = client
		order = append(order, client)
	}
	if len(order) == 0 {
		return nil, false
	}

	perServer := map[string]map[string]struct{}{}
	unresolved := false
	for virtualID := range matched {
		resolved := a.IDStore.ResolveVirtualID(virtualID)
		if resolved == nil {
			unresolved = true
			continue
		}
		pairs := make([]AdditionalInstance, 0, len(resolved.OtherInstances)+1)
		pairs = append(pairs, AdditionalInstance{OriginalID: resolved.OriginalID, ServerID: resolved.ServerID})
		pairs = append(pairs, resolved.OtherInstances...)
		for _, pair := range pairs {
			if pair.OriginalID == "" {
				unresolved = true
				continue
			}
			if _, ok := allowed[pair.ServerID]; !ok {
				continue
			}
			if perServer[pair.ServerID] == nil {
				perServer[pair.ServerID] = map[string]struct{}{}
			}
			perServer[pair.ServerID][pair.OriginalID] = struct{}{}
		}
	}

	targets := make([]localFilterTarget, 0, len(order))
	for _, client := range order {
		ids := make([]string, 0, len(perServer[client.ID]))
		for id := range perServer[client.ID] {
			ids = append(ids, id)
		}
		if len(ids) == 0 {
			continue
		}
		sort.Strings(ids)
		targets = append(targets, localFilterTarget{client: client, ids: ids})
	}
	return targets, unresolved
}

// localFilterStateRows is the error-aware counterpart of localUserStateRows.
// The existing helper logs a read failure and returns an empty map, which would
// make a database error indistinguishable from "the user has no favorites";
// this path must instead report the incomplete candidate set to the caller.
func (a *App) localFilterStateRows(proxyUserID string, f userStateFilter) (map[string]WatchProgress, error) {
	var rows []WatchProgress
	var err error
	switch {
	case f.favorite:
		rows, err = a.WatchStore.GetFavoriteItems(proxyUserID)
	case f.resumable:
		rows, err = a.WatchStore.GetResumableItems(proxyUserID)
	default:
		rows, err = a.WatchStore.GetPlayedItems(proxyUserID)
	}
	if err != nil {
		return nil, err
	}
	matched := make(map[string]WatchProgress, len(rows))
	seen := map[string]bool{}
	for _, row := range rows {
		if seen[row.VirtualItemID] {
			continue
		}
		merged, err := a.localWatchState(proxyUserID, row.VirtualItemID)
		if err != nil {
			return nil, err
		}
		aliases := []string{row.VirtualItemID}
		if a.IDStore != nil {
			aliases = a.IDStore.AliasVirtualIDs(row.VirtualItemID)
		}
		for _, id := range aliases {
			seen[id] = true
			if merged != nil && satisfiesUserState(*merged, f) {
				matched[id] = *merged
			}
		}
	}
	return matched, nil
}

// intersectLocalFilterTargets applies every client-supplied ID predicate to
// the local candidate set. Ids and ItemIds are separate predicates: when both
// are present the local item must satisfy both, never their union. The
// comparison happens on physical IDs per server, so a virtual ID that resolves
// to an additional instance still matches; an ID for another server never
// widens the request.
func (a *App) intersectLocalFilterTargets(targets []localFilterTarget, predicates []map[string]struct{}) []localFilterTarget {
	out := make([]localFilterTarget, 0, len(targets))
	for _, target := range targets {
		allowed := make([]map[string]struct{}, len(predicates))
		for i, predicate := range predicates {
			allowed[i] = make(map[string]struct{}, len(predicate))
			for id := range predicate {
				if translated, ok := translateVirtualIDForServer(id, target.client.ID, a.IDStore); ok {
					allowed[i][translated] = struct{}{}
				}
			}
		}
		kept := make([]string, 0, len(target.ids))
		for _, id := range target.ids {
			matched := true
			for _, set := range allowed {
				if _, ok := set[id]; !ok {
					matched = false
					break
				}
			}
			if matched {
				kept = append(kept, id)
			}
		}
		if len(kept) == 0 {
			continue
		}
		out = append(out, localFilterTarget{client: target.client, ids: kept})
	}
	return out
}

// localFilterClientIDPredicates reads the client's Ids and ItemIds predicates
// as separate sets. An absent key contributes no predicate; a present but empty
// key is an empty predicate and therefore matches nothing. Casing variants of
// the same key are one predicate. PersonIds is a different predicate and is
// deliberately left untouched.
func localFilterClientIDPredicates(values url.Values) []map[string]struct{} {
	grouped := map[string]map[string]struct{}{}
	order := make([]string, 0, 2)
	for key, rawValues := range values {
		name := strings.ToLower(strings.TrimSpace(key))
		if name != "ids" && name != "itemids" {
			continue
		}
		set, ok := grouped[name]
		if !ok {
			set = map[string]struct{}{}
			grouped[name] = set
			order = append(order, name)
		}
		for _, raw := range rawValues {
			for _, part := range strings.Split(raw, ",") {
				if id := strings.TrimSpace(part); id != "" {
					set[id] = struct{}{}
				}
			}
		}
	}
	sort.Strings(order)
	predicates := make([]map[string]struct{}, 0, len(order))
	for _, name := range order {
		predicates = append(predicates, grouped[name])
	}
	return predicates
}

// localFilterServerQuery prepares the parts of the query that are identical for
// every ID batch on one upstream: the per-server UserId, the translated
// ParentId and taxonomy IDs, the preserved passthrough filters, Fields and
// PersonIds. It reports false when a taxonomy ID belongs to another server, in
// which case this server owns none of the requested candidates and must not be
// queried without the filter.
func (a *App) localFilterServerQuery(base url.Values, client *UpstreamClient) (url.Values, bool) {
	query := cloneValues(base)
	prepareUpstreamCatalogRandom(query)
	setQueryValue(query, "UserId", client.clientUserID())
	if parent := firstQueryValue(query, "ParentId", "parentId", "parentid"); parent != "" && parent != "0" && parent != "root" {
		if a.IDStore.ResolveVirtualID(parent) != nil {
			translated, ok := translateVirtualIDForServer(parent, client.ID, a.IDStore)
			if !ok {
				return nil, false
			}
			setQueryValue(query, "ParentId", translated)
			query.Del("parentId")
			query.Del("parentid")
		}
	}
	if genre := firstQueryValue(query, "GenreIds", "genreIds", "genreids"); genre != "" {
		translated, ok := a.translateTaxonomyIDs(genre, client.ID)
		if !ok {
			return nil, false
		}
		setQueryValue(query, "GenreIds", translated)
		query.Del("genreIds")
		query.Del("genreids")
	}
	// Replace the client's batch predicate with the intersected local batch. The
	// PersonIds predicate names people, not media items, and stays verbatim.
	for key := range query {
		if isBatchIDQueryKey(key) && !strings.EqualFold(key, "PersonIds") {
			delete(query, key)
		}
	}
	return query, true
}

// localFilterIDBatches partitions physical IDs into bounded requests. Both the
// count and the encoded ID-list size are capped; a single over-long ID is still
// emitted alone so the loop always makes progress.
func localFilterIDBatches(ids []string) [][]string {
	batches := make([][]string, 0, (len(ids)+localFilterBatchLimit-1)/localFilterBatchLimit)
	for start := 0; start < len(ids); {
		batch := make([]string, 0, min(localFilterBatchLimit, len(ids)-start))
		encoded := 0
		for start < len(ids) && len(batch) < localFilterBatchLimit {
			add := len(ids[start])
			if len(batch) > 0 {
				add++
			}
			if len(batch) > 0 && encoded+add > localFilterBatchMaxIDBytes {
				break
			}
			batch = append(batch, ids[start])
			encoded += add
			start++
		}
		batches = append(batches, batch)
	}
	return batches
}

// fetchLocalFilterTarget issues the ID batches for one upstream. A confirmed
// prefix survives a later batch failure; the result is then incomplete instead
// of claiming that the unqueried IDs are absent.
func (a *App) fetchLocalFilterTarget(ctx context.Context, reqCtx *RequestContext, target localFilterTarget, pathTemplate string, base url.Values, budget *catalogRequestBudget) upstreamItemsResult {
	result := upstreamItemsResult{
		ServerID: target.client.ID,
		Items:    make([]map[string]any, 0, len(target.ids)),
		Complete: true,
	}
	serverQuery, ok := a.localFilterServerQuery(base, target.client)
	if !ok {
		// The taxonomy names another server's ID: this server's contribution is a
		// complete empty set, not a failure worth querying for.
		return result
	}
	path := strings.Replace(pathTemplate, "%s", target.client.clientUserID(), 1)
	for _, batch := range localFilterIDBatches(target.ids) {
		pageQuery := cloneValues(serverQuery)
		pageQuery.Set("Ids", strings.Join(batch, ","))
		setQueryValue(pageQuery, "StartIndex", "0")
		setQueryValue(pageQuery, "Limit", strconv.Itoa(len(batch)))
		if !budget.tryAcquire() {
			result.Complete = false
			result.Truncated = true
			return result
		}
		payload, err := target.client.RequestJSON(ctx, reqCtx, a.Identity, http.MethodGet, path, pageQuery, nil)
		if err != nil {
			if len(result.Items) > 0 {
				result.Complete = false
				result.Truncated = true
				return result
			}
			return upstreamItemsResult{ServerID: target.client.ID, Err: err}
		}
		items, suspicious, short := validateLocalFilterBatch(payload, batch)
		result.Items = append(result.Items, items...)
		if suspicious || short {
			result.Complete = false
			result.Truncated = true
		}
	}
	return result
}

// validateLocalFilterBatch keeps only the requested physical IDs. A returned ID
// that was never requested is a shape violation (usually an upstream that
// ignored the Ids filter), even when the requested items are also present, so
// the batch is marked suspicious and the caller has to report incompleteness.
// An upstream total that promises more rows than the batch can contain is
// likewise a lower bound, while missing requested IDs alone are tolerated.
func validateLocalFilterBatch(payload any, requested []string) (items []map[string]any, suspicious bool, short bool) {
	wanted := make(map[string]struct{}, len(requested))
	for _, id := range requested {
		wanted[id] = struct{}{}
	}
	seen := map[string]struct{}{}
	for _, item := range asItems(payload) {
		id, _ := item["Id"].(string)
		if id == "" {
			suspicious = true
			continue
		}
		if _, ok := wanted[id]; !ok {
			suspicious = true
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			suspicious = true
			continue
		}
		seen[id] = struct{}{}
		items = append(items, item)
	}
	if total, ok := totalRecordCount(payload); ok {
		if total > len(requested) || total < len(items) {
			suspicious = true
		}
		if total > len(items) {
			short = true
		}
	}
	return items, suspicious, short
}
