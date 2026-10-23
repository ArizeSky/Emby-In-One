package backend

import (
	"net/http"
	"net/url"
	"strings"
)

// This file is the read-side companion to parental.go (pure rules) and
// parental_access.go (per-instance authorization). List endpoints receive
// sparse DTOs whose rating fields were never requested, so a plain
// filterItemsByPolicy would read "no rating" as "unrated" and let a restricted
// item through. The helpers here confirm missing ratings (and Season/Episode
// parent inheritance) with bounded batched upstream lookups before an item is
// allowed into a restricted user's response (plan §8.3/§8.4).

const (
	// parentalConfirmBatchSize caps how many item ids one confirming /Items?Ids=
	// request carries (plan §8.3: batch, never a per-item loop over a whole list).
	parentalConfirmBatchSize = 200
	// parentalConfirmRequestBudget bounds the confirming lookups one request may
	// issue across all servers, batches and fallbacks. Unconfirmed items are
	// dropped, not released as unrated.
	parentalConfirmRequestBudget = 50
)

// ratingPresence distinguishes what a DTO itself says about an item's rating.
type ratingPresence int

const (
	// ratingPresent: the DTO carries a recognisable rating; the ceiling applies
	// directly and no upstream lookup is needed.
	ratingPresent ratingPresence = iota
	// ratingMissing: the DTO carries no rating text at all. A sparse list DTO is
	// the usual cause, so this means "unknown here", never "unrated upstream".
	ratingMissing
	// ratingUnrecognized: the DTO carries non-empty rating text this rule table
	// does not know. Treated as unrated/unknown, blocked only by blockUnrated.
	ratingUnrecognized
)

// classifyItemRating reads the rating-relevant fields off one DTO. CustomRating
// keeps its deliberate precedence over OfficialRating (plan §8.3).
func classifyItemRating(item map[string]any) (int, ratingPresence) {
	rating, _ := item["CustomRating"].(string)
	if strings.TrimSpace(rating) == "" {
		rating, _ = item["OfficialRating"].(string)
	}
	if strings.TrimSpace(rating) == "" {
		return 0, ratingMissing
	}
	score, ok := ratingScore(rating)
	if !ok {
		return 0, ratingUnrecognized
	}
	return score, ratingPresent
}

// parentalItemDecision is the outcome of one item's parental check.
type parentalItemDecision int

const (
	parentalAllow       parentalItemDecision = iota
	parentalDeny                             // confirmed above the ceiling / blocked unrated
	parentalUnconfirmed                      // lookup failed or budget exhausted: fail closed
)

// decideRating applies the policy to a confirmed (score, presence) pair.
func decideRating(score int, presence ratingPresence, maxRating int, blockUnrated bool) parentalItemDecision {
	switch presence {
	case ratingPresent:
		if maxRating > 0 && score > maxRating {
			return parentalDeny
		}
		return parentalAllow
	case ratingUnrecognized:
		if blockUnrated {
			return parentalDeny
		}
		return parentalAllow
	default:
		return parentalUnconfirmed
	}
}

// parentalReadSession carries one request's rating-confirmation state: a shared
// memo of already-checked items plus the remaining lookup budget. The memo
// prevents repeated lookups for the same item inside one request; the budget
// bounds the total number of upstream attempts (plan §8.3).
type parentalReadSession struct {
	maxRating    int
	blockUnrated bool
	budget       int
	memo         map[string]parentalItemDecision
}

func newParentalReadSession(maxRating int, blockUnrated bool) *parentalReadSession {
	return &parentalReadSession{
		maxRating:    maxRating,
		blockUnrated: blockUnrated,
		budget:       parentalConfirmRequestBudget,
		memo:         map[string]parentalItemDecision{},
	}
}

// parentalMemoKey identifies one upstream item. The server ID is part of the
// key: the same original id on two servers is two different copies with two
// ratings.
func parentalMemoKey(serverID, originalID string) string {
	return serverID + "\x00" + originalID
}

// takeBudget consumes one upstream attempt, reporting false when exhausted.
func (s *parentalReadSession) takeBudget() bool {
	if s == nil {
		return false
	}
	if s.budget <= 0 {
		return false
	}
	s.budget--
	return true
}

// errParentalBudgetExhausted marks a confirmation abandoned because the
// request's lookup budget ran out. Callers treat it as unconfirmed (fail
// closed), never as unrated.
var errParentalBudgetExhausted = &parentalBudgetError{}

type parentalBudgetError struct{}

func (*parentalBudgetError) Error() string { return "parental rating confirmation budget exhausted" }

// parentalFilterItems filters a list of rewritten DTOs (virtual "Id" present)
// through the requesting user's parental policy. Items carrying a recognisable
// rating are decided from the DTO alone; items without rating text are
// confirmed against their owning upstream (batched, budgeted, with parent
// inheritance) before being allowed. Unconfirmed items are dropped — a failed
// lookup is never "unrated" (plan §8.3/§8.4).
func (a *App) parentalFilterItems(r *http.Request, items []map[string]any) []map[string]any {
	maxRating, blockUnrated := a.parentalPolicyFor(r)
	if (maxRating <= 0 && !blockUnrated) || len(items) == 0 {
		return items
	}
	session := newParentalReadSession(maxRating, blockUnrated)
	kept := make([]map[string]any, 0, len(items))
	pending := make([]map[string]any, 0, len(items))
	for _, item := range items {
		score, presence := classifyItemRating(item)
		if presence != ratingMissing {
			if decideRating(score, presence, maxRating, blockUnrated) == parentalAllow {
				kept = append(kept, item)
			}
			continue
		}
		pending = append(pending, item)
	}
	if len(pending) == 0 {
		return kept
	}
	allowed := a.confirmItemsRatings(r, session, pending)
	for i, item := range pending {
		if allowed[i] == parentalAllow {
			kept = append(kept, item)
		}
	}
	return kept
}

// confirmItemsRatings resolves the rating of every sparse item, returning the
// decision per pending index. Items are grouped by owning server and confirmed
// with batched /Items?Ids= requests; an item whose batch answer is still
// rating-less gets one detail fallback, and Season/Episode items inherit along
// their same-server parent chain (plan §8.3).
func (a *App) confirmItemsRatings(r *http.Request, session *parentalReadSession, items []map[string]any) map[int]parentalItemDecision {
	out := make(map[int]parentalItemDecision, len(items))
	type target struct {
		index     int
		serverID  string
		original  string
		client    *UpstreamClient
		confirmed bool
	}
	targets := make([]target, 0, len(items))
	for i := range items {
		out[i] = parentalUnconfirmed
		id, _ := items[i]["Id"].(string)
		if id == "" {
			// No resolvable identity: the rating cannot be confirmed, so the
			// item stays unconfirmed and is dropped by the caller.
			continue
		}
		resolved := a.resolveRouteID(id)
		if resolved == nil {
			continue
		}
		targets = append(targets, target{index: i, serverID: resolved.ServerID, original: resolved.OriginalID, client: resolved.Client})
	}
	// Serve every target from the session memo first.
	for i := range targets {
		t := &targets[i]
		key := parentalMemoKey(t.serverID, t.original)
		if decision, ok := session.memo[key]; ok {
			out[t.index] = decision
			t.confirmed = true
		}
	}
	// Group the unresolved targets by server and confirm them in batches.
	unresolved := map[string][]*target{}
	for i := range targets {
		t := &targets[i]
		if !t.confirmed {
			unresolved[t.serverID] = append(unresolved[t.serverID], t)
		}
	}
	for serverID, group := range unresolved {
		client := a.Upstream.ClientByID(serverID)
		if client == nil || !client.IsOnline() {
			continue
		}
		for start := 0; start < len(group); start += parentalConfirmBatchSize {
			end := start + parentalConfirmBatchSize
			if end > len(group) {
				end = len(group)
			}
			batch := group[start:end]
			if !session.takeBudget() {
				break
			}
			ids := make([]string, 0, len(batch))
			for _, t := range batch {
				ids = append(ids, t.original)
			}
			q := url.Values{}
			q.Set("Ids", strings.Join(ids, ","))
			q.Set("UserId", client.clientUserID())
			q.Set("Fields", "OfficialRating,CustomRating,Type,SeriesId,SeasonId,ParentId")
			payload, err := client.RequestJSON(r.Context(), requestContextFrom(r.Context()), a.Identity, http.MethodGet, "/Items", q, nil)
			if err != nil {
				continue
			}
			fetched := map[string]map[string]any{}
			for _, item := range asItems(payload) {
				if id, _ := item["Id"].(string); id != "" {
					fetched[id] = item
				}
			}
			for _, t := range batch {
				meta, ok := fetched[t.original]
				if !ok {
					// The batch answer omitted this id: not "unrated", keep it
					// queued for the detail fallback below.
					continue
				}
				decision := a.decideConfirmedItem(r, session, client, meta)
				session.memo[parentalMemoKey(t.serverID, t.original)] = decision
				out[t.index] = decision
				t.confirmed = true
			}
		}
	}
	// Detail fallback for items the batch could not resolve (sparse batch
	// answers, servers without batch support). Bounded by the same budget.
	for i := range targets {
		t := &targets[i]
		if t.confirmed {
			continue
		}
		if !session.takeBudget() {
			break
		}
		meta, err := a.fetchItemForParentalCheck(r, t.client, t.original)
		if err != nil {
			continue
		}
		decision := a.decideConfirmedItem(r, session, t.client, meta)
		session.memo[parentalMemoKey(t.serverID, t.original)] = decision
		out[t.index] = decision
	}
	return out
}

// decideConfirmedItem decides one item from a full metadata record, inheriting a
// Season/Episode rating from its same-server parents when the item itself
// carries none. A parent-chain lookup failure is unconfirmed, never unrated.
func (a *App) decideConfirmedItem(r *http.Request, session *parentalReadSession, client *UpstreamClient, meta map[string]any) parentalItemDecision {
	score, presence := classifyItemRating(meta)
	if presence == ratingMissing && needsRatingInheritance(meta) {
		parentScore, parentPresence, err := a.inheritParentalScoreBudgeted(r, session, client, meta)
		if err != nil {
			return parentalUnconfirmed
		}
		score, presence = parentScore, parentPresence
	}
	return decideRating(score, presence, session.maxRating, session.blockUnrated)
}

// inheritParentalScoreBudgeted walks the same-server parent chain like
// inheritParentalScore, but shares the request's lookup budget so a list of
// episodes under one series cannot fan out into unbounded parent queries.
func (a *App) inheritParentalScoreBudgeted(r *http.Request, session *parentalReadSession, client *UpstreamClient, item map[string]any) (int, ratingPresence, error) {
	visited := map[string]bool{}
	if id, _ := item["Id"].(string); id != "" {
		visited[id] = true
	}
	cur := item
	for depth := 0; depth < maxParentalInheritanceDepth; depth++ {
		parentID := nextParentID(cur, visited)
		if parentID == "" {
			break
		}
		visited[parentID] = true
		if !session.takeBudget() {
			return 0, ratingMissing, errParentalBudgetExhausted
		}
		parent, err := a.fetchItemForParentalCheck(r, client, parentID)
		if err != nil {
			return 0, ratingMissing, err
		}
		parentScore, parentPresence := classifyItemRating(parent)
		if parentPresence == ratingPresent {
			return parentScore, ratingPresent, nil
		}
		if parentPresence == ratingUnrecognized {
			// An explicit but unknown parent rating is a real unrated signal.
			return parentScore, ratingUnrecognized, nil
		}
		cur = parent
	}
	return 0, ratingMissing, nil
}

// parentalFilterPayloadItems filters every item list found in a passthrough
// payload envelope. Emby nests item lists under different keys (Items,
// SearchHints, ThemeVideosResult.Items, ...); the known content keys are
// filtered in place so a restricted user never receives an unconfirmed item
// regardless of which envelope the upstream used (plan §8.4 route table).
func (a *App) parentalFilterPayloadItems(r *http.Request, payload map[string]any) {
	if payload == nil {
		return
	}
	maxRating, blockUnrated := a.parentalPolicyFor(r)
	if maxRating <= 0 && !blockUnrated {
		return
	}
	for _, key := range []string{"Items", "SearchHints"} {
		if raw, ok := payload[key].([]any); ok {
			items := make([]map[string]any, 0, len(raw))
			for _, entry := range raw {
				if m, ok := entry.(map[string]any); ok {
					items = append(items, m)
				}
			}
			kept := a.parentalFilterItems(r, items)
			payload[key] = toAnySlice(kept)
		}
	}
	for _, key := range []string{"ThemeVideosResult", "ThemeSongsResult", "SoundtrackSongsResult"} {
		nested, ok := payload[key].(map[string]any)
		if !ok {
			continue
		}
		if raw, ok := nested["Items"].([]any); ok {
			items := make([]map[string]any, 0, len(raw))
			for _, entry := range raw {
				if m, ok := entry.(map[string]any); ok {
					items = append(items, m)
				}
			}
			kept := a.parentalFilterItems(r, items)
			nested["Items"] = toAnySlice(kept)
		}
	}
}

// parentalDetailAllowed guards one item detail response. The DTO may itself be
// sparse, so the check runs against the owning instance through the
// inheritance-aware per-instance check instead of the response fields.
func (a *App) parentalDetailAllowed(w http.ResponseWriter, r *http.Request, client *UpstreamClient, originalID string) bool {
	maxRating, blockUnrated := a.parentalPolicyFor(r)
	if maxRating <= 0 && !blockUnrated {
		return true
	}
	allowed, confirmed := a.checkInstanceParentalAccess(r, client, originalID)
	if !allowed {
		if !confirmed {
			writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Cannot verify content rating"})
		} else {
			writeJSON(w, http.StatusForbidden, map[string]any{"message": "Content restricted by parental rating"})
		}
		return false
	}
	return true
}
