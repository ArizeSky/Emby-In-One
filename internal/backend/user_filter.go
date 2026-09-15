package backend

import (
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The upstream answers item queries with the *shared* account's user state, so a
// filter like IsFavorite returns someone else's favorites — and worse, it narrows
// the candidate set, so an item the local user favorited but the shared account
// never did cannot come back at all. The proxy answers the filters it can compute
// from the local WatchStore itself.
const (
	filterIsFavorite  = "isfavorite"
	filterIsPlayed    = "isplayed"
	filterIsResumable = "isresumable"
	filterIsUnplayed  = "isunplayed"
)

// localFilterScanLimit caps how many upstream items one locally-answered filter may
// pull. The shared account's filter cannot narrow the candidate set — that is the
// whole problem — so the proxy asks for the unfiltered set and intersects it
// locally. Past this many items the page is served from a truncated candidate set,
// which is logged.
const localFilterScanLimit = 5000

// filterNoticeHeader carries the "this filter is not isolated per user" hint to
// callers that can read response headers. Emby clients ignore unknown headers.
const filterNoticeHeader = "X-Emby-In-One-Filter-Notice"

// noticeInterval throttles the operator hints so paging through a list cannot flood
// the log with the same line.
const noticeInterval = time.Minute

// localSortFields are the item fields the local sort reads. A client that sorts by a
// field it never asked for would otherwise get a page sorted by missing values.
var localSortFields = []string{"SortName", "DateCreated", "ProductionYear", "CommunityRating"}

// userStateFilter is the parsed Filters query parameter.
type userStateFilter struct {
	favorite  bool
	played    bool
	resumable bool
	// passthrough keeps every filter value the proxy cannot answer locally; it stays
	// in the upstream query so the client's intent is never silently dropped.
	passthrough []string
	// notices names the user-state values that had to be passed through, so the
	// operator can be told that they still follow the shared account.
	notices []string
}

// active reports whether any part of the filter is answered locally.
func (f userStateFilter) active() bool { return f.favorite || f.played || f.resumable }

// parseUserStateFilter reads the Filters query parameter. Contradictory or unknown
// values are kept verbatim in passthrough.
func parseUserStateFilter(values url.Values) userStateFilter {
	var f userStateFilter
	key := queryKey(values, "Filters")
	if key == "" {
		return f
	}
	seen := map[string]bool{}
	for _, raw := range values[key] {
		for _, part := range strings.Split(raw, ",") {
			value := strings.TrimSpace(part)
			if value == "" {
				continue
			}
			normalized := strings.ToLower(value)
			if seen[normalized] {
				continue
			}
			seen[normalized] = true
			switch normalized {
			case filterIsFavorite:
				f.favorite = true
			case filterIsPlayed:
				f.played = true
			case filterIsResumable:
				f.resumable = true
			default:
				f.passthrough = append(f.passthrough, value)
				if isUnlocalizedUserFilter(normalized) {
					f.notices = append(f.notices, value)
				}
			}
		}
	}
	// IsPlayed and IsUnplayed contradict each other. Leaving both upstream lets the
	// upstream resolve them its own way; letting the local predicate decide would
	// only fight the upstream answer.
	if f.played && containsFilter(f.passthrough, filterIsUnplayed) {
		f.played = false
	}
	return f
}

// isUnlocalizedUserFilter names the user-state filters the local store cannot
// answer. IsUnplayed is a complement (unwatched = everything minus watched) and the
// local records only hold what the user touched; Likes/Dislikes are not recorded at
// all, and IsFavoriteOrLiked includes those likes.
func isUnlocalizedUserFilter(normalized string) bool {
	switch normalized {
	case filterIsUnplayed, "likes", "dislikes", "isfavoriteorliked":
		return true
	default:
		return false
	}
}

func containsFilter(values []string, normalized string) bool {
	for _, value := range values {
		if strings.EqualFold(value, normalized) {
			return true
		}
	}
	return false
}

// stripFrom rewrites the Filters values in place, dropping the ones answered
// locally and keeping the rest under their original spelling.
func (f userStateFilter) stripFrom(values url.Values) {
	key := queryKey(values, "Filters")
	if key == "" {
		return
	}
	values.Del(key)
	if len(f.passthrough) > 0 {
		values.Set(key, strings.Join(f.passthrough, ","))
	}
}

// localUserStateFilter parses the request's filters and emits the hint for the ones
// the proxy cannot answer per user. Admins are left out: the shared account's state
// is their own state, so the hint would be misleading.
func (a *App) localUserStateFilter(w http.ResponseWriter, r *http.Request, values url.Values) userStateFilter {
	f := parseUserStateFilter(values)
	if len(f.notices) > 0 && isRegularProxyUser(r) {
		a.hintUnlocalizedFilter(w, f.notices)
	}
	return f
}

// prepareLocalUserFilter rewrites an upstream item query for a locally-answered
// filter. It returns true only when the caller must serve the response differently:
// the localizable filters are removed and the upstream is asked for the whole
// candidate set instead of the shared account's page, so the caller has to intersect
// the response locally and then sort and page it itself.
func (a *App) prepareLocalUserFilter(w http.ResponseWriter, r *http.Request, values url.Values) (userStateFilter, bool) {
	f := a.localUserStateFilter(w, r, values)
	if a.WatchStore == nil || !isRegularProxyUser(r) || !f.active() {
		return f, false
	}
	f.stripFrom(values)
	// What comes back is a candidate set, not the answer.
	values.Set("StartIndex", "0")
	values.Set("Limit", strconv.Itoa(localFilterScanLimit))
	ensureSortFields(values)
	return f, true
}

// localUserStateIntersect is the variant for endpoints whose candidate set is fixed
// by the request itself (GET /Items?Ids=...). The filters are still stripped so the
// shared account cannot hide the items, but the paging is left alone.
func (a *App) localUserStateIntersect(w http.ResponseWriter, r *http.Request, values url.Values) (userStateFilter, bool) {
	f := a.localUserStateFilter(w, r, values)
	if a.WatchStore == nil || !isRegularProxyUser(r) || !f.active() {
		return f, false
	}
	f.stripFrom(values)
	ensureSortFields(values)
	return f, true
}

// isRegularProxyUser reports whether the request carries a non-admin proxy user,
// the only callers with a local record of their own.
func isRegularProxyUser(r *http.Request) bool {
	reqCtx := requestContextFrom(r.Context())
	return reqCtx != nil && reqCtx.ProxyUser != nil && reqCtx.ProxyUser.Role != "admin"
}

// hintUnlocalizedFilter tells the operator that a filter still follows the shared
// upstream account. The response is the only channel a client can see, so the hint
// is a header plus a throttled log line for the panel.
func (a *App) hintUnlocalizedFilter(w http.ResponseWriter, values []string) {
	if len(values) == 0 {
		return
	}
	joined := strings.Join(values, ",")
	if w != nil {
		w.Header().Set(filterNoticeHeader, joined+" is not isolated per user")
	}
	if a.Logger == nil || !a.noticeThrottle.allow(joined) {
		return
	}
	a.Logger.Warnf("Filters=%s is not computed per user: it still follows the shared upstream account", joined)
}

// noticeThrottle keeps one paging session from flooding the log with the same hint.
// Each App owns one, so tests and restarts start clean.
type noticeThrottle struct {
	mu     sync.Mutex
	lastAt map[string]time.Time
}

func (t *noticeThrottle) allow(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.lastAt == nil {
		t.lastAt = map[string]time.Time{}
	}
	now := time.Now()
	if last, ok := t.lastAt[key]; ok && now.Sub(last) < noticeInterval {
		return false
	}
	t.lastAt[key] = now
	return true
}

// localUserStateRows returns the local records satisfying every locally-computed
// predicate, keyed by virtual item ID.
func (a *App) localUserStateRows(proxyUserID string, f userStateFilter) map[string]WatchProgress {
	var rows []WatchProgress
	var err error
	// Start from the narrowest set the store can produce, then apply the rest in
	// memory — every row carries all three fields.
	switch {
	case f.favorite:
		rows, err = a.WatchStore.GetFavoriteItems(proxyUserID)
	case f.resumable:
		rows, err = a.WatchStore.GetResumableItems(proxyUserID)
	default:
		rows, err = a.WatchStore.GetPlayedItems(proxyUserID)
	}
	if err != nil {
		if a.Logger != nil {
			a.Logger.Warnf("local user-state filter read failed: %s", redactURLInError(err))
		}
		return nil
	}
	matched := make(map[string]WatchProgress, len(rows))
	for _, row := range rows {
		if !satisfiesUserState(row, f) {
			continue
		}
		matched[row.VirtualItemID] = row
	}
	return matched
}

func satisfiesUserState(p WatchProgress, f userStateFilter) bool {
	if f.favorite && !p.IsFavorite {
		return false
	}
	if f.played && !p.Played {
		return false
	}
	if f.resumable && !(p.PositionTicks > 0 && !p.Played) {
		return false
	}
	return true
}

// filterItemsByLocalUserState keeps the upstream candidates the local record matches.
// Every item that survives has already been rewritten to virtual IDs, so the local
// record keyed by virtual ID applies directly. It also returns each survivor's
// last-played stamp, which is what the local sort falls back to.
func (a *App) filterItemsByLocalUserState(r *http.Request, items []map[string]any, f userStateFilter) ([]map[string]any, map[string]int64) {
	reqCtx := requestContextFrom(r.Context())
	if reqCtx == nil || reqCtx.ProxyUser == nil || a.WatchStore == nil {
		return items, nil
	}
	if len(items) >= localFilterScanLimit && a.Logger != nil {
		a.Logger.Warnf("local user-state filter scanned %d upstream items (limit %d): the result may be truncated",
			len(items), localFilterScanLimit)
	}
	matched := a.localUserStateRows(reqCtx.ProxyUser.UserID, f)
	kept := make([]map[string]any, 0, len(items))
	recency := make(map[string]int64, len(matched))
	for _, item := range items {
		id := itemID(item)
		row, ok := matched[id]
		if !ok {
			continue
		}
		recency[id] = row.LastPlayed
		kept = append(kept, item)
	}
	return kept, recency
}

// localItemSort re-orders a locally filtered page. The metadata batch that feeds it
// comes back in no meaningful order, so the client's SortBy has to be applied here.
// Only a few keys exist in the local records; any other key degrades to the user's
// own last-played order, which is also the tiebreaker for the keys that are known.
func localItemSort(items []map[string]any, query url.Values, recency map[string]int64) {
	order := strings.TrimSpace(query.Get("SortOrder"))
	// Absent SortOrder means most-recently-touched first, the only sensible default
	// for a list the user asked to see by their own state.
	ascending := strings.EqualFold(order, "Ascending")
	sortItemsByRecency(items, recency, ascending)

	primary := ""
	if keys := splitQueryList(query.Get("SortBy")); len(keys) > 0 {
		primary = keys[0]
	}
	less, supported := localItemLess(primary, strings.EqualFold(order, "Descending"))
	if !supported {
		return
	}
	// A stable second pass over the recency order gives the primary key with recency
	// as its tiebreaker.
	sort.SliceStable(items, func(x, y int) bool { return less(items[x], items[y]) })
}

func sortItemsByRecency(items []map[string]any, recency map[string]int64, ascending bool) {
	sort.SliceStable(items, func(x, y int) bool {
		left := recency[itemID(items[x])]
		right := recency[itemID(items[y])]
		if left == right {
			return false
		}
		if ascending {
			return left < right
		}
		return left > right
	})
}

// localItemLess returns a comparator for a SortBy key, or false when the key is one
// the local records cannot sort by.
func localItemLess(key string, descending bool) (func(a, b map[string]any) bool, bool) {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "sortname":
		return textLess(itemSortName, descending), true
	case "name":
		return textLess(func(m map[string]any) string { return itemText(m, "Name") }, descending), true
	case "datecreated":
		return textLess(func(m map[string]any) string { return itemText(m, "DateCreated") }, descending), true
	case "productionyear":
		return numberLess(func(m map[string]any) float64 { return itemNumber(m, "ProductionYear") }, descending), true
	case "communityrating":
		return numberLess(func(m map[string]any) float64 { return itemNumber(m, "CommunityRating") }, descending), true
	default:
		return nil, false
	}
}

// textLess compares by byte order, which for the ASCII sort names Emby stores is the
// same order the upstream would produce. Items whose field is missing group together
// in their previous (recency) order.
func textLess(value func(map[string]any) string, descending bool) func(a, b map[string]any) bool {
	return func(a, b map[string]any) bool {
		left, right := value(a), value(b)
		if left == right {
			return false
		}
		if descending {
			return left > right
		}
		return left < right
	}
}

func numberLess(value func(map[string]any) float64, descending bool) func(a, b map[string]any) bool {
	return func(a, b map[string]any) bool {
		left, right := value(a), value(b)
		if left == right {
			return false
		}
		if descending {
			return left > right
		}
		return left < right
	}
}

// ensureSortFields asks the upstream for the fields the local sort reads, on top of
// whatever the client already requested.
func ensureSortFields(values url.Values) {
	key := queryKey(values, "Fields")
	if key == "" {
		key = "Fields"
	}
	existing := splitQueryList(values.Get(key))
	seen := make(map[string]bool, len(existing))
	for _, field := range existing {
		seen[strings.ToLower(field)] = true
	}
	for _, field := range localSortFields {
		if !seen[strings.ToLower(field)] {
			existing = append(existing, field)
		}
	}
	values.Set(key, strings.Join(existing, ","))
}

// queryKey returns the spelling a parameter actually uses, or "" when absent.
func queryKey(values url.Values, name string) string {
	for key := range values {
		if strings.EqualFold(key, name) {
			return key
		}
	}
	return ""
}

func splitQueryList(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func itemID(m map[string]any) string {
	id, _ := m["Id"].(string)
	return id
}

func itemText(m map[string]any, key string) string {
	value, _ := m[key].(string)
	return value
}

// itemSortName falls back to Name for the items the upstream returned without the
// SortName field.
func itemSortName(m map[string]any) string {
	if name := itemText(m, "SortName"); name != "" {
		return name
	}
	return itemText(m, "Name")
}

func itemNumber(m map[string]any, key string) float64 {
	switch typed := m[key].(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	default:
		return 0
	}
}
