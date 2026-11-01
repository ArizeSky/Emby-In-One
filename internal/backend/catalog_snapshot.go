package backend

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"
)

// The catalog snapshot store keeps a bounded, resumable prefix of one
// upstream's raw candidate set. It is deliberately app-local: the zero value is
// usable, there is no package-global cache and no background goroutine. A
// scanner integration therefore owns one store per App and calls Fill for each
// equivalent upstream query.
//
// A snapshot is raw upstream data only. It is never a personalized response:
// parental filtering, local state, virtual IDs, sorting and UserData are
// re-applied by the caller on every request, and every returned item map is a
// deep clone so merge/parental code can mutate its copy without poisoning the
// cache.
const (
	catalogSnapshotMaxEntries            = 16
	catalogSnapshotMaxBytes        int64 = 32 << 20
	catalogSnapshotMaxCandidates         = 50000
	catalogSnapshotRoundCandidates       = 5000
	catalogSnapshotTTL                   = 60 * time.Second
)

// Partial reasons are diagnostic only. They never make a prefix complete; a
// caller must key incomplete-result handling off CatalogSnapshotView.Complete.
const (
	catalogSnapshotPartialRoundLimit     = "round-limit"
	catalogSnapshotPartialCandidateLimit = "candidate-limit"
	catalogSnapshotPartialBudget         = "budget-exhausted"
	catalogSnapshotPartialRepeatedPage   = "repeated-page"
	catalogSnapshotPartialByteLimit      = "byte-limit"
	catalogSnapshotPartialExpired        = "expired"
	catalogSnapshotPartialCancelled      = "cancelled"
	catalogSnapshotPartialUpstreamError  = "upstream-error"
	catalogSnapshotPartialBadRequest     = "bad-request"
	catalogSnapshotPartialShortPage      = "short-page"
	catalogSnapshotPartialPageOverflow   = "page-overflow"
	catalogSnapshotPartialNonPaged       = "non-paged-continuation"
)

var (
	// errCatalogSnapshotStoreFull means every entry slot belongs to an
	// in-flight fill. Starting another fill would duplicate work, so the
	// caller should treat this request as a partial result instead.
	errCatalogSnapshotStoreFull = errors.New("catalog snapshot store has no evictable entry")
	// errCatalogSnapshotExpired is internal to the fill loop; the caller
	// receives the retained prefix plus Complete=false.
	errCatalogSnapshotExpired = errors.New("catalog snapshot entry expired")
	// errCatalogSnapshotBudgetExhausted may be returned by a fetcher that
	// consumed the shared catalog request budget.
	errCatalogSnapshotBudgetExhausted = errors.New("catalog snapshot request budget exhausted")
)

// catalogSnapshotSpec describes one upstream's scan shape. The zero value is
// the stock non-paged Emby path (one 5000-row request) and is normalized on use.
type catalogSnapshotSpec struct {
	// Paged asks for narrow sequential windows (pagedScan upstreams).
	Paged bool
	// MaxCandidates is the per-entry resource guard, not an exact total.
	MaxCandidates int
	// RoundCandidates is how many new raw candidates one Fill may add.
	RoundCandidates int
	// InitialWindow is the first request window (200 when Paged, 5000 when not).
	InitialWindow int
	// FallbackWindow is used after a paged upstream rejects the initial window.
	FallbackWindow int
}

func (s catalogSnapshotSpec) normalized() catalogSnapshotSpec {
	if s.MaxCandidates <= 0 {
		s.MaxCandidates = catalogSnapshotMaxCandidates
	}
	if s.RoundCandidates <= 0 {
		s.RoundCandidates = catalogSnapshotRoundCandidates
	}
	if s.InitialWindow <= 0 {
		if s.Paged {
			s.InitialWindow = upstreamPageLimitCeiling
		} else {
			s.InitialWindow = mergedItemsScanLimit
		}
	}
	if s.FallbackWindow <= 0 {
		s.FallbackWindow = fallbackUpstreamPageLimit
	}
	if s.FallbackWindow > s.InitialWindow {
		s.FallbackWindow = s.InitialWindow
	}
	return s
}

// catalogSnapshotPage is one raw upstream page as returned by a fetcher. The
// fetcher owns HTTP and the shared catalog budget; the store owns offsets,
// windows, deduplication, caps and persistence.
type catalogSnapshotPage struct {
	Items           []map[string]any
	Total           int
	HasTotal        bool
	BadRequest      bool
	BudgetExhausted bool
}

// catalogSnapshotFetchFunc asks the upstream for one page at offset/limit.
type catalogSnapshotFetchFunc func(ctx context.Context, offset, limit int) (catalogSnapshotPage, error)

// catalogSnapshotView is the raw prefix handed to the scanner. Items are deep
// clones; mutating them is safe and expected (merge and parental stages do).
type catalogSnapshotView struct {
	Items           []map[string]any
	NextOffset      int
	EffectiveWindow int
	Complete        bool
	PartialReason   string
	ExpiresAt       time.Time
}

type catalogSnapshotEntry struct {
	key             string
	seq             uint64
	items           []map[string]any
	bytes           int64
	nextOffset      int
	effectiveWindow int
	complete        bool
	partialReason   string
	expiresAt       time.Time
	seenPages       map[string]struct{}
	fill            *catalogSnapshotFill
}

type catalogSnapshotFill struct {
	entry *catalogSnapshotEntry
	done  chan struct{}

	endOffset       int
	effectiveWindow int
	complete        bool
	partialReason   string
	expiresAt       time.Time
}

// catalogSnapshotStore is the zero-value-usable app-local cache. Tests may
// override now/limits; production code uses the constants above.
type catalogSnapshotStore struct {
	mu         sync.Mutex
	entries    map[string]*catalogSnapshotEntry
	bytes      int64
	seq        uint64
	now        func() time.Time
	maxEntries int
	maxBytes   int64
	ttl        time.Duration
}

type catalogSnapshotClaim struct {
	entry  *catalogSnapshotEntry
	fill   *catalogSnapshotFill
	leader bool
	reuse  bool
}

func (s *catalogSnapshotStore) nowTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// ensureLocked installs defaults lazily so the zero value works.
func (s *catalogSnapshotStore) ensureLocked() {
	if s.entries == nil {
		s.entries = make(map[string]*catalogSnapshotEntry)
	}
	if s.maxEntries <= 0 {
		s.maxEntries = catalogSnapshotMaxEntries
	}
	if s.maxBytes <= 0 {
		s.maxBytes = catalogSnapshotMaxBytes
	}
	if s.ttl <= 0 {
		s.ttl = catalogSnapshotTTL
	}
}

// Fill returns the current raw prefix for key, starting or joining one bounded
// fill when the entry is missing, unfinished or expired. A complete fresh entry
// is reused without calling fetch. Concurrent callers for the same key wait for
// the same fill and receive its result; they never extend it with more pages.
func (s *catalogSnapshotStore) Fill(ctx context.Context, key string, spec catalogSnapshotSpec, fetch catalogSnapshotFetchFunc) (catalogSnapshotView, error) {
	if key == "" {
		return catalogSnapshotView{}, errors.New("catalog snapshot: empty key")
	}
	if fetch == nil {
		return catalogSnapshotView{}, errors.New("catalog snapshot: nil fetcher")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	spec = spec.normalized()

	s.mu.Lock()
	s.ensureLocked()
	claim, err := s.claimLocked(key, spec, s.nowTime())
	s.mu.Unlock()
	if err != nil {
		return catalogSnapshotView{}, err
	}
	switch {
	case claim.reuse:
		return s.viewForEntry(claim.entry), nil
	case claim.leader:
		return s.runFill(ctx, claim.entry, claim.fill, spec, fetch)
	default:
		return s.waitFill(ctx, claim.fill)
	}
}

func (s *catalogSnapshotStore) claimLocked(key string, spec catalogSnapshotSpec, now time.Time) (catalogSnapshotClaim, error) {
	s.purgeExpiredLocked(now)
	if e := s.entries[key]; e != nil {
		// An in-flight fill is always joined, even when it has just crossed
		// its absolute deadline: replacing it would start duplicate upstream
		// work for the same key.
		if e.fill != nil {
			return catalogSnapshotClaim{entry: e, fill: e.fill}, nil
		}
		if now.Before(e.expiresAt) {
			if e.complete {
				return catalogSnapshotClaim{entry: e, reuse: true}, nil
			}
			fill := &catalogSnapshotFill{entry: e, done: make(chan struct{})}
			e.partialReason = ""
			e.fill = fill
			return catalogSnapshotClaim{entry: e, fill: fill, leader: true}, nil
		}
		s.removeEntryLocked(e)
	}

	for len(s.entries) >= s.maxEntries {
		if !s.evictOneLocked(now, nil) {
			return catalogSnapshotClaim{}, errCatalogSnapshotStoreFull
		}
	}
	s.seq++
	e := &catalogSnapshotEntry{
		key:       key,
		seq:       s.seq,
		expiresAt: now.Add(s.ttl),
		seenPages: make(map[string]struct{}),
	}
	if spec.Paged {
		e.effectiveWindow = spec.InitialWindow
	}
	fill := &catalogSnapshotFill{entry: e, done: make(chan struct{})}
	e.fill = fill
	s.entries[key] = e
	return catalogSnapshotClaim{entry: e, fill: fill, leader: true}, nil
}

func (s *catalogSnapshotStore) waitFill(ctx context.Context, fill *catalogSnapshotFill) (catalogSnapshotView, error) {
	select {
	case <-fill.done:
		return s.viewForFill(fill), nil
	case <-ctx.Done():
		return catalogSnapshotView{}, ctx.Err()
	}
}

func (s *catalogSnapshotStore) runFill(ctx context.Context, e *catalogSnapshotEntry, fill *catalogSnapshotFill, spec catalogSnapshotSpec, fetch catalogSnapshotFetchFunc) (catalogSnapshotView, error) {
	var fillErr error
	roundAdded := 0
	fallbackTried := false
	for {
		if ctxErr := ctx.Err(); ctxErr != nil {
			s.markPartial(e, catalogSnapshotPartialCancelled)
			fillErr = ctxErr
			break
		}
		offset, limit, window, room, roundLeft, expired := s.nextPageRequest(e, spec, roundAdded)
		if expired {
			s.markPartial(e, catalogSnapshotPartialExpired)
			break
		}
		if room <= 0 {
			s.markPartial(e, catalogSnapshotPartialCandidateLimit)
			break
		}
		if roundLeft <= 0 {
			s.markPartial(e, catalogSnapshotPartialRoundLimit)
			break
		}
		if limit <= 0 {
			s.markPartial(e, catalogSnapshotPartialRoundLimit)
			break
		}

		page, fetchErr := fetch(ctx, offset, limit)
		if fetchErr != nil {
			switch {
			case errors.Is(fetchErr, errCatalogSnapshotBudgetExhausted):
				s.markPartial(e, catalogSnapshotPartialBudget)
			case ctx.Err() != nil:
				s.markPartial(e, catalogSnapshotPartialCancelled)
			default:
				s.markPartial(e, catalogSnapshotPartialUpstreamError)
			}
			fillErr = fetchErr
			break
		}
		if page.BudgetExhausted {
			s.markPartial(e, catalogSnapshotPartialBudget)
			break
		}
		if page.BadRequest {
			if spec.Paged && !fallbackTried && window > spec.FallbackWindow {
				fallbackTried = true
				s.lowerWindow(e, spec.FallbackWindow)
				continue
			}
			s.markPartial(e, catalogSnapshotPartialBadRequest)
			break
		}

		items := page.Items
		overflowed := len(items) > limit
		if overflowed {
			items = items[:limit]
		}
		if len(items) == 0 {
			if page.HasTotal && offset < page.Total {
				s.markPartial(e, catalogSnapshotPartialShortPage)
			} else {
				s.markComplete(e)
			}
			break
		}

		fingerprint := catalogSnapshotPageFingerprint(items)
		added, repeated, storageReason := s.appendPage(e, items, fingerprint, spec)
		if repeated {
			s.markPartial(e, catalogSnapshotPartialRepeatedPage)
			break
		}
		if storageReason != "" {
			s.markPartial(e, storageReason)
			break
		}
		roundAdded += added

		if page.HasTotal && e.nextOffset >= page.Total {
			s.markComplete(e)
			break
		}
		if overflowed {
			s.markPartial(e, catalogSnapshotPartialPageOverflow)
			break
		}

		if !spec.Paged {
			// A stock upstream answers one 5000-row request. A short page that
			// still owes rows is a partial prefix; the next Fill continues
			// from the retained offset.
			if !page.HasTotal && len(items) < limit {
				s.markComplete(e)
			} else {
				s.markPartial(e, catalogSnapshotPartialNonPaged)
			}
			break
		}

		if len(items) < limit {
			if page.HasTotal && e.nextOffset < page.Total {
				// Keep probing within the round; a later page may fill the
				// shortfall. Repeated-page fingerprints stop a bad upstream.
				continue
			}
			s.markComplete(e)
			break
		}
	}
	s.finishFill(e, fill)
	return s.viewForFill(fill), fillErr
}

// nextPageRequest snapshots the fields the fill loop needs without holding the
// store lock while HTTP runs.
func (s *catalogSnapshotStore) nextPageRequest(e *catalogSnapshotEntry, spec catalogSnapshotSpec, roundAdded int) (offset, limit, window, room, roundLeft int, expired bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.nowTime().Before(e.expiresAt) {
		return 0, 0, 0, 0, 0, true
	}
	offset = e.nextOffset
	room = spec.MaxCandidates - e.nextOffset
	roundLeft = spec.RoundCandidates - roundAdded
	window = e.effectiveWindow
	if !spec.Paged {
		window = spec.InitialWindow
	} else if window <= 0 {
		window = spec.InitialWindow
	}
	limit = window
	if roundLeft < limit {
		limit = roundLeft
	}
	if room < limit {
		limit = room
	}
	return offset, limit, window, room, roundLeft, false
}

func (s *catalogSnapshotStore) lowerWindow(e *catalogSnapshotEntry, window int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if window > 0 && window < e.effectiveWindow {
		e.effectiveWindow = window
	}
}

// appendPage clones the page, records its fingerprint and retains it only when
// the store-wide byte budget can be met by evicting other non-filling entries.
func (s *catalogSnapshotStore) appendPage(e *catalogSnapshotEntry, items []map[string]any, fingerprint string, spec catalogSnapshotSpec) (int, bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.nowTime()
	if !now.Before(e.expiresAt) {
		return 0, false, catalogSnapshotPartialExpired
	}
	if _, seen := e.seenPages[fingerprint]; seen {
		return 0, true, ""
	}
	room := spec.MaxCandidates - len(e.items)
	if room <= 0 {
		return 0, false, catalogSnapshotPartialCandidateLimit
	}
	if len(items) > room {
		items = items[:room]
	}
	pageBytes := catalogSnapshotPayloadBytes(items)
	if pageBytes > s.maxBytes {
		return 0, false, catalogSnapshotPartialByteLimit
	}
	for s.bytes+pageBytes > s.maxBytes {
		if !s.evictOneLocked(now, e) {
			return 0, false, catalogSnapshotPartialByteLimit
		}
	}
	cloned := cloneCatalogRawItems(items)
	e.items = append(e.items, cloned...)
	e.bytes += pageBytes
	e.nextOffset = len(e.items)
	e.seenPages[fingerprint] = struct{}{}
	s.bytes += pageBytes
	return len(cloned), false, ""
}

func (s *catalogSnapshotStore) markPartial(e *catalogSnapshotEntry, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e.complete {
		return
	}
	e.partialReason = reason
}

func (s *catalogSnapshotStore) markComplete(e *catalogSnapshotEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e.complete = true
	e.partialReason = ""
}

func (s *catalogSnapshotStore) finishFill(e *catalogSnapshotEntry, fill *catalogSnapshotFill) {
	s.mu.Lock()
	if e.fill == fill {
		now := s.nowTime()
		if !now.Before(e.expiresAt) {
			e.complete = false
			e.partialReason = catalogSnapshotPartialExpired
		}
		fill.endOffset = len(e.items)
		fill.effectiveWindow = e.effectiveWindow
		fill.complete = e.complete
		fill.partialReason = e.partialReason
		fill.expiresAt = e.expiresAt
		e.fill = nil
		if !now.Before(e.expiresAt) {
			s.removeEntryLocked(e)
		}
	}
	s.mu.Unlock()
	close(fill.done)
}

func (s *catalogSnapshotStore) viewForFill(fill *catalogSnapshotFill) catalogSnapshotView {
	s.mu.Lock()
	raw := append([]map[string]any(nil), fill.entry.items...)
	if fill.endOffset < len(raw) {
		raw = raw[:fill.endOffset]
	}
	view := catalogSnapshotView{
		NextOffset:      fill.endOffset,
		EffectiveWindow: fill.effectiveWindow,
		Complete:        fill.complete,
		PartialReason:   fill.partialReason,
		ExpiresAt:       fill.expiresAt,
	}
	s.mu.Unlock()
	view.Items = cloneCatalogRawItems(raw)
	return view
}

func (s *catalogSnapshotStore) viewForEntry(e *catalogSnapshotEntry) catalogSnapshotView {
	s.mu.Lock()
	raw := append([]map[string]any(nil), e.items...)
	view := catalogSnapshotView{
		NextOffset:      len(raw),
		EffectiveWindow: e.effectiveWindow,
		Complete:        e.complete,
		PartialReason:   e.partialReason,
		ExpiresAt:       e.expiresAt,
	}
	s.mu.Unlock()
	view.Items = cloneCatalogRawItems(raw)
	return view
}

// purgeExpiredLocked drops expired idle entries. An expired entry that is
// currently filling is retained until its fill finishes so a second fill for
// the same key cannot start in parallel.
func (s *catalogSnapshotStore) purgeExpiredLocked(now time.Time) {
	for _, e := range s.entries {
		if e.fill == nil && !now.Before(e.expiresAt) {
			s.removeEntryLocked(e)
		}
	}
}

func (s *catalogSnapshotStore) removeEntryLocked(e *catalogSnapshotEntry) {
	current, ok := s.entries[e.key]
	if !ok || current != e {
		return
	}
	delete(s.entries, e.key)
	s.bytes -= e.bytes
	if s.bytes < 0 {
		s.bytes = 0
	}
}

// evictOneLocked deterministically removes one non-filling entry. Expired
// entries go first, then complete entries, then partial idle entries; within a
// class the earliest absolute expiry wins, with insertion order as the tie
// breaker. A filling entry is never evicted: it is the only writer of its
// state, and dropping it would let a duplicate fill start.
func (s *catalogSnapshotStore) evictOneLocked(now time.Time, protect *catalogSnapshotEntry) bool {
	var expired, complete, partial *catalogSnapshotEntry
	for _, e := range s.entries {
		if e == protect || e.fill != nil {
			continue
		}
		switch {
		case !now.Before(e.expiresAt):
			expired = earlierCatalogSnapshotEntry(expired, e)
		case e.complete:
			complete = earlierCatalogSnapshotEntry(complete, e)
		default:
			partial = earlierCatalogSnapshotEntry(partial, e)
		}
	}
	candidate := expired
	if candidate == nil {
		candidate = complete
	}
	if candidate == nil {
		candidate = partial
	}
	if candidate == nil {
		return false
	}
	s.removeEntryLocked(candidate)
	return true
}

func earlierCatalogSnapshotEntry(current, candidate *catalogSnapshotEntry) *catalogSnapshotEntry {
	if current == nil {
		return candidate
	}
	if candidate.expiresAt.Before(current.expiresAt) {
		return candidate
	}
	if candidate.expiresAt.Equal(current.expiresAt) && candidate.seq < current.seq {
		return candidate
	}
	return current
}

// len and byteSize exist for bounded-storage tests and diagnostics. They take
// the store lock so a scan cannot observe a half-applied page.
func (s *catalogSnapshotStore) len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}

func (s *catalogSnapshotStore) byteSize() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bytes
}

// catalogSnapshotCanonicalQuery removes the proxy-owned paging parameters from
// a client query. An internal Limit=5000 window is therefore not part of the
// key, while Fields, filters, sort order and RandomSeed remain distinct.
func catalogSnapshotCanonicalQuery(query url.Values) string {
	cloned := cloneValues(query)
	for key := range cloned {
		if strings.EqualFold(key, "StartIndex") || strings.EqualFold(key, "Limit") {
			delete(cloned, key)
		}
	}
	return cloned.Encode()
}
