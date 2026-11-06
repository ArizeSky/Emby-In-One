package backend

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Strict playback HTTP ingress (design §4.7) and its playback capability store.
//
// The dedicated negotiation entry (/eio/playback/v1/Items/{itemId}/PlaybackInfo)
// runs under the ordinary local auth every other API route uses. For each
// PlaybackInfo candidate it reserves one binding and answers the opaque attempt ID
// plus a capability:
//
//	capability = "eio_play_v1_" + 48 lowercase hex characters
//
// The capability is a playback-scoped bearer secret, stored by digest only in this
// file's own mutex-protected store together with the attempt ID and the original
// authenticated local session that negotiated it. Neither value is logged, echoed
// in an error or persisted; the store is memory-only, so a restart invalidates
// every strict attempt and the player renegotiates. An unknown capability fails
// closed and never falls back to the legacy ordinary-token path, and the legacy
// `withContext` path rejects the prefix outright, so a restart cannot downgrade a
// strict attempt and a capability cannot authenticate a metadata route.
//
// Strict consumer routes (/eio/playback/v1/{attemptID}/...) authenticate with the
// capability only. The ingress then:
//
//  1. resolves the capability for the attempt ID carried in the path,
//  2. revalidates the stored parent session (Auth.ValidateToken plus live grants),
//     so a revoked token, disabled account or narrowed grants apply immediately,
//  3. rebuilds the binding owner from that session and leases the binding, which
//     enforces the user/session ownership captured at negotiation time,
//  4. publishes Playback, PlaybackCredential (the raw capability, for derived
//     resource URLs) and ProxyToken (the parent session) in the request context,
//  5. on the first strict request of a still-pending candidate, checks the live
//     server grant and the serving instance's parental policy, claims the frozen
//     slot reservation (non-stop requests only) and activates the candidate.
//
// Route shape, ID translation and upstream dispatch stay in the existing handlers:
// this file only decides whether a strict request may reach them. The only
// lifecycle transition here is that pending→active activation; a stop is applied
// by the session worker only after the upstream acknowledged it, so a failed stop
// stays retryable. The only idempotent stop answer here is for an already finished
// attempt: a local acknowledgment, never a fabricated upstream ACK.

const (
	// playbackCapabilityPrefix marks a playback-scoped capability. The legacy
	// ingress must refuse any token with this prefix, whatever the store holds.
	playbackCapabilityPrefix = "eio_play_v1_"
	// playbackCapabilityTokenBytes is the entropy behind the 48 hex characters.
	playbackCapabilityTokenBytes = 24
	// playbackCapabilityTokenHex is the fixed hex length after the prefix.
	playbackCapabilityTokenHex = playbackCapabilityTokenBytes * 2
	// playbackCapabilityDigestVersion domain-separates the stored digest.
	playbackCapabilityDigestVersion = "v1"
	// playbackCapabilityMaxEntries matches the binding store's global capacity.
	playbackCapabilityMaxEntries = playbackIdentityMaxEntries
	// playbackCapabilityMaxPerAttempt bounds re-negotiations of one attempt so a
	// retry loop cannot grow the store without re-reserving a binding.
	playbackCapabilityMaxPerAttempt = 8
	// playbackCapabilityIdleTTL is a sliding window: a capability is dropped after
	// a period of non-use, and in any case never resolves once its binding expires.
	playbackCapabilityIdleTTL = playbackIdentityActiveIdle
	// playbackCapabilityIDRetries bounds collision retries while issuing.
	playbackCapabilityIDRetries = 8
)

// playbackIngressPrefix is the frozen strict consumer prefix.
const playbackIngressPrefix = "/eio/playback/v1/"

// isPlaybackCapabilityToken reports whether token is syntactically a playback
// capability. It is deliberately cheap and value-independent: callers use it to
// route a request away from the legacy ordinary-token path, not to authorise it.
func isPlaybackCapabilityToken(token string) bool {
	if !strings.HasPrefix(token, playbackCapabilityPrefix) {
		return false
	}
	if len(token) != len(playbackCapabilityPrefix)+playbackCapabilityTokenHex {
		return false
	}
	for i := len(playbackCapabilityPrefix); i < len(token); i++ {
		c := token[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// playbackCapabilityDigest binds a capability to exactly one attempt ID, so a
// token captured from one attempt cannot be replayed under another attempt.
func playbackCapabilityDigest(attemptID, token string) string {
	sum := sha256.Sum256([]byte(playbackCapabilityDigestVersion + "\x00" + attemptID + "\x00" + token))
	return hex.EncodeToString(sum[:])
}

// playbackCapabilityEntry is one issued capability. Only the digest is a key;
// ParentToken is the original authenticated local session, kept in memory so every
// strict request can revalidate Auth.ValidateToken and the live grants, and never
// logged, returned or persisted.
type playbackCapabilityEntry struct {
	attemptID   string
	parentToken string
	lastUsedAt  time.Time
}

// playbackCapabilityStore is a bounded, mutex-protected, memory-only store. It is
// deliberately separate from both the auth token map and the naming-binding store:
// a capability is neither an ordinary local session nor a binding, and mixing them
// is what would let a capability authenticate a metadata route.
type playbackCapabilityStore struct {
	mu        sync.Mutex
	byDigest  map[string]*playbackCapabilityEntry
	byAttempt map[string][]string

	maxEntries    int
	maxPerAttempt int
	idleTTL       time.Duration

	now func() time.Time
}

func newPlaybackCapabilityStore() *playbackCapabilityStore {
	return newPlaybackCapabilityStoreWithClock(time.Now)
}

// newPlaybackCapabilityStoreWithClock lets tests advance expiry explicitly.
func newPlaybackCapabilityStoreWithClock(now func() time.Time) *playbackCapabilityStore {
	if now == nil {
		now = time.Now
	}
	return &playbackCapabilityStore{
		byDigest:      map[string]*playbackCapabilityEntry{},
		byAttempt:     map[string][]string{},
		maxEntries:    playbackCapabilityMaxEntries,
		maxPerAttempt: playbackCapabilityMaxPerAttempt,
		idleTTL:       playbackCapabilityIdleTTL,
		now:           now,
	}
}

// issue mints one capability for an already reserved attempt. Capacity failures
// are retryable and reported as binding capacity: the caller must fail the
// negotiation instead of publishing a candidate with no credential.
func (s *playbackCapabilityStore) issue(attemptID, parentToken string) (string, error) {
	if attemptID == "" || parentToken == "" {
		return "", fmt.Errorf("%w: capability needs an attempt and a parent session", ErrPlaybackIdentityInvalid)
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	if len(s.byDigest) >= s.maxEntries {
		return "", fmt.Errorf("%w: capability store limit", ErrPlaybackIdentityCapacity)
	}
	if len(s.byAttempt[attemptID]) >= s.maxPerAttempt {
		return "", fmt.Errorf("%w: per-attempt capability limit", ErrPlaybackIdentityCapacity)
	}
	var token, digest string
	for attempt := 0; attempt < playbackCapabilityIDRetries; attempt++ {
		token = playbackCapabilityPrefix + randomHex(playbackCapabilityTokenBytes)
		digest = playbackCapabilityDigest(attemptID, token)
		if s.byDigest[digest] == nil {
			break
		}
		digest = ""
	}
	if digest == "" {
		return "", fmt.Errorf("%w: capability generation failed", ErrPlaybackIdentityInvalid)
	}
	s.byDigest[digest] = &playbackCapabilityEntry{
		attemptID:   attemptID,
		parentToken: parentToken,
		lastUsedAt:  now,
	}
	s.byAttempt[attemptID] = append(s.byAttempt[attemptID], digest)
	return token, nil
}

// lookup resolves a presented capability for the attempt the request addresses and
// returns the stored parent session. A wrong attempt, unknown or expired token all
// report the same miss; only a match renews the idle window.
func (s *playbackCapabilityStore) lookup(token, attemptID string) (string, bool) {
	if s == nil || attemptID == "" || !isPlaybackCapabilityToken(token) {
		return "", false
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	entry := s.byDigest[playbackCapabilityDigest(attemptID, token)]
	if entry == nil {
		return "", false
	}
	if subtle.ConstantTimeCompare([]byte(entry.attemptID), []byte(attemptID)) != 1 {
		return "", false
	}
	if entry.parentToken == "" {
		return "", false
	}
	entry.lastUsedAt = now
	return entry.parentToken, true
}

// revokeByAttempt drops every capability of one attempt. It belongs to attempt
// finalization (retire/fail/rollback): capability expiry must follow the binding,
// and a terminal attempt must not keep a usable credential alive.
func (s *playbackCapabilityStore) revokeByAttempt(attemptID string) int {
	if s == nil || attemptID == "" {
		return 0
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	digests := append([]string(nil), s.byAttempt[attemptID]...)
	removed := 0
	for _, digest := range digests {
		if entry := s.byDigest[digest]; entry != nil {
			s.removeLocked(digest, entry)
			removed++
		}
	}
	delete(s.byAttempt, attemptID)
	return removed
}

// cleanup removes idle capabilities and returns how many were dropped. Every
// operation prunes first, so correctness never depends on the ticker.
func (s *playbackCapabilityStore) cleanup() int {
	if s == nil {
		return 0
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pruneLocked(now)
}

func (s *playbackCapabilityStore) count() int {
	if s == nil {
		return 0
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	return len(s.byDigest)
}

func (s *playbackCapabilityStore) pruneLocked(now time.Time) int {
	removed := 0
	for digest, entry := range s.byDigest {
		if now.Sub(entry.lastUsedAt) < s.idleTTL {
			continue
		}
		s.removeLocked(digest, entry)
		removed++
	}
	return removed
}

func (s *playbackCapabilityStore) removeLocked(digest string, entry *playbackCapabilityEntry) {
	delete(s.byDigest, digest)
	if entry == nil {
		return
	}
	digests := s.byAttempt[entry.attemptID]
	for i, candidate := range digests {
		if candidate != digest {
			continue
		}
		digests = append(digests[:i], digests[i+1:]...)
		break
	}
	if len(digests) == 0 {
		delete(s.byAttempt, entry.attemptID)
		return
	}
	s.byAttempt[entry.attemptID] = digests
}

// issuePlaybackCapability mints the capability handed to the client for one
// reserved candidate. An empty result means the attempt must not be published:
// callers fail the negotiation instead of exposing an unauthenticated URL.
func (a *App) issuePlaybackCapability(attemptID, parentToken string) string {
	if a == nil || a.PlaybackCapabilities == nil {
		return ""
	}
	token, err := a.PlaybackCapabilities.issue(attemptID, parentToken)
	if err != nil {
		return ""
	}
	return token
}

// strictPlaybackError is the frozen error protocol of the strict path: a stable
// kind, an HTTP status and an explicit retryability. It never carries an attempt
// ID, capability, token or upstream detail.
type strictPlaybackError struct {
	Status    int
	Kind      string
	Message   string
	Retryable bool
}

func (e *strictPlaybackError) Error() string {
	if e == nil {
		return "strict playback: error"
	}
	return "strict playback " + e.Kind + ": " + e.Message
}

var (
	errStrictCapabilityRequired = &strictPlaybackError{Status: http.StatusUnauthorized, Kind: "playback-capability-required", Message: "a playback capability is required for this route"}
	errStrictCapabilityInvalid  = &strictPlaybackError{Status: http.StatusUnauthorized, Kind: "playback-capability-invalid", Message: "playback capability is invalid or expired; renegotiate"}
	errStrictSessionRequired    = &strictPlaybackError{Status: http.StatusUnauthorized, Kind: "playback-session-required", Message: "the authenticated local session is no longer valid"}
	errStrictIngressUnavailable = &strictPlaybackError{Status: http.StatusServiceUnavailable, Kind: "playback-ingress-unavailable", Message: "playback ingress is unavailable", Retryable: true}
	errStrictAttemptRequired    = &strictPlaybackError{Status: http.StatusNotFound, Kind: "playback-attempt-required", Message: "strict playback request has no valid attempt id"}
	errStrictServerForbidden    = &strictPlaybackError{Status: http.StatusForbidden, Kind: "playback-server-forbidden", Message: "playback attempt is not available to this account"}
	errStrictStopPending        = &strictPlaybackError{Status: http.StatusConflict, Kind: "playback-stop-pending", Message: "playback stop is still being applied; retry", Retryable: true}
	errStrictSourceUnavailable  = &strictPlaybackError{Status: http.StatusConflict, Kind: "playback-source-unavailable", Message: "playback source is unavailable; renegotiate", Retryable: true}
	errStrictRatingUnconfirmed  = &strictPlaybackError{Status: http.StatusBadGateway, Kind: "playback-rating-unconfirmed", Message: "content rating could not be verified", Retryable: true}
	errStrictRatingForbidden    = &strictPlaybackError{Status: http.StatusForbidden, Kind: "playback-rating-forbidden", Message: "content restricted by parental rating"}
)

// strictPlaybackErrorFor maps binding lifecycle errors to the frozen protocol.
// Not-found stays retryable because the attempt can be renegotiated; stopping and
// terminal are distinct so the player stops the old attempt instead of retrying it.
func strictPlaybackErrorFor(err error) *strictPlaybackError {
	switch {
	case errors.Is(err, ErrPlaybackIdentityNotFound):
		return &strictPlaybackError{
			Status: http.StatusNotFound, Kind: "playback-binding-not-found",
			Message: "playback attempt is no longer available; renegotiate", Retryable: true,
		}
	case errors.Is(err, ErrPlaybackIdentityOwnerMismatch):
		return &strictPlaybackError{
			Status: http.StatusForbidden, Kind: "playback-identity-owner-mismatch",
			Message: "playback attempt does not belong to this session",
		}
	case errors.Is(err, ErrPlaybackIdentityStopping):
		return &strictPlaybackError{
			Status: http.StatusConflict, Kind: "playback-stopping",
			Message: "playback attempt is stopping",
		}
	case errors.Is(err, ErrPlaybackIdentityTerminal):
		return &strictPlaybackError{
			Status: http.StatusConflict, Kind: "playback-terminal",
			Message: "playback attempt has ended; start a new attempt", Retryable: true,
		}
	case errors.Is(err, ErrPlaybackIdentityCapacity):
		return &strictPlaybackError{
			Status: http.StatusServiceUnavailable, Kind: "playback-capacity",
			Message: "playback capacity exhausted; retry later", Retryable: true,
		}
	default:
		return &strictPlaybackError{
			Status: http.StatusConflict, Kind: "playback-identity-unavailable",
			Message: "playback identity is unavailable; renegotiate", Retryable: true,
		}
	}
}

func writeStrictPlaybackError(w http.ResponseWriter, failure *strictPlaybackError) {
	if failure == nil {
		failure = errStrictIngressUnavailable
	}
	payload := map[string]any{"kind": failure.Kind, "message": failure.Message}
	if failure.Retryable {
		payload["retryable"] = true
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, failure.Status, payload)
}

// writeStrictPlaybackStopAcknowledged answers a stop for an attempt that has
// already finished: the upstream stop was acknowledged earlier (that is what moved
// the binding to its terminal state) and local cleanup already happened. Nothing is
// sent upstream for it and no new upstream confirmation is claimed. An attempt that
// is only draining is not answered here: its stop is not yet known to be
// acknowledged, so it stays retryable instead of being reported as done.
func writeStrictPlaybackStopAcknowledged(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}

// strictPlaybackCapabilityToken extracts the capability from any accepted carrier.
// Unlike extractToken it only accepts capability-shaped values, so an ordinary
// local session is never treated as a playback credential and an ordinary
// X-Emby-Token header cannot shadow the capability in a media/HLS child URL.
func strictPlaybackCapabilityToken(r *http.Request) string {
	if r == nil {
		return ""
	}
	if r.URL != nil {
		if token, _ := queryTokenLookup(r.URL.Query()); isPlaybackCapabilityToken(token) {
			return token
		}
	}
	if token := r.Header.Get("X-Emby-Token"); isPlaybackCapabilityToken(token) {
		return token
	}
	for _, name := range []string{"X-Emby-Authorization", "Authorization"} {
		header := r.Header.Get(name)
		if header == "" {
			continue
		}
		if token := extractTokenFromAuthHeader(header); isPlaybackCapabilityToken(token) {
			return token
		}
	}
	return ""
}

// withStrictPlayback authenticates a /eio/playback/v1/... request with its
// playback capability and publishes the leased binding in the request context.
// It wraps the existing handlers, so route-level authorization and upstream
// behaviour are unchanged.
func (a *App) withStrictPlayback(next http.HandlerFunc) http.HandlerFunc {
	if next == nil {
		next = func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }
	}
	return func(w http.ResponseWriter, r *http.Request) {
		attemptID, ok := strictPlaybackAttemptID(r)
		if !ok {
			writeStrictPlaybackError(w, errStrictAttemptRequired)
			return
		}
		// Only a capability authenticates this path. An ordinary local token (even
		// a valid one) is not converted into a strict attempt: there is no fallback
		// from a missing/expired capability to the legacy protocol.
		capability := strictPlaybackCapabilityToken(r)
		if capability != "" && !strictPlaybackAssociationsMatch(r, attemptID, capability) {
			writeStrictPlaybackError(w, &strictPlaybackError{Status: 409, Kind: "playback-association-conflict", Message: "conflicting attempt or credential markers"})
			return
		}
		if capability == "" {
			writeStrictPlaybackError(w, errStrictCapabilityRequired)
			return
		}
		if a.Auth == nil || a.PlaybackIdentities == nil || a.PlaybackCapabilities == nil {
			writeStrictPlaybackError(w, errStrictIngressUnavailable)
			return
		}
		parentToken, ok := a.PlaybackCapabilities.lookup(capability, attemptID)
		if !ok {
			writeStrictPlaybackError(w, errStrictCapabilityInvalid)
			return
		}
		proxyUser := a.validateStrictParentSession(parentToken)
		if proxyUser == nil {
			writeStrictPlaybackError(w, errStrictSessionRequired)
			return
		}
		reqCtx := a.newStrictPlaybackContext(r, proxyUser, parentToken, capability)
		// The owner is rebuilt from the revalidated session, so the binding store
		// itself proves that this capability belongs to the user and local session
		// that negotiated the attempt.
		owner := newPlaybackIdentityOwner(streamResourceUserID(reqCtx), parentToken)
		cleanup := strictPlaybackStopRequest(r)
		var lease *playbackIdentityLease
		var err error
		if cleanup {
			lease, err = a.PlaybackIdentities.leaseCleanup(attemptID, owner)
		} else {
			lease, err = a.PlaybackIdentities.lease(attemptID, owner)
		}
		if err != nil {
			if cleanup && errors.Is(err, ErrPlaybackIdentityTerminal) {
				writeStrictPlaybackError(w, &strictPlaybackError{Status: 409, Kind: "playback-failed", Message: "attempt ended locally without upstream stop confirmation"})
			} else {
				writeStrictPlaybackError(w, strictPlaybackErrorFor(err))
			}
			return
		}
		defer lease.Release()
		binding := lease.Binding()
		if !a.strictBindingAssociationsMatch(r, binding) {
			writeStrictPlaybackError(w, &strictPlaybackError{Status: 409, Kind: "playback-association-conflict", Message: "conflicting playback associations"})
			return
		}
		if !a.isServerAllowed(reqCtx, binding.ServerID) || !cleanup && !a.strictPlaybackServerAllowed(reqCtx, binding.ServerID) {
			writeStrictPlaybackError(w, errStrictServerForbidden)
			return
		}
		if operation := playbackCleanupOperation(r); operation != 0 && binding.CleanupConfirmed&operation != 0 {
			if !a.validateStrictCleanupReplay(w, r, binding) {
				return
			}
			writeStrictPlaybackStopAcknowledged(w)
			return
		}
		client := a.Upstream.ClientByID(binding.ServerID)
		if client == nil && !cleanup {
			writeStrictPlaybackError(w, errStrictSourceUnavailable)
			return
		}
		boundContext := *reqCtx
		boundContext.Playback = &binding
		if authErr := func() error {
			if cleanup {
				return nil
			}
			return client.validatePlaybackAuth(&boundContext, client.authSnapshot())
		}(); authErr != nil {
			writePreparationError(w, authErr)
			return
		}
		if binding.State == playbackIdentityPending && !cleanup {
			// Candidates are published pending and only become active on their first
			// strict request, so a candidate the client never used can never hold an
			// active idle slot. The live grant check runs before the activation, so a
			// revoked account cannot activate a reserved candidate.
			if !a.strictPlaybackServerAllowed(reqCtx, binding.ServerID) {
				writeStrictPlaybackError(w, errStrictServerForbidden)
				return
			}
			// Cleanup never activates a candidate or acquires a playback slot.
			if !a.claimStrictPlaybackSlotForRequest(w, r, reqCtx, binding) {
				return
			}
			activated, activateErr := a.PlaybackIdentities.activate(attemptID, owner, "")
			if activateErr != nil {
				// A concurrent cleanup can prevent activation after the slot claim.
				// Release only this candidate's unit, never another attempt's slot.
				a.finishStrictPlaybackAttemptLocally(r.WithContext(context.WithValue(r.Context(), requestContextKey{}, reqCtx)), binding)
				writeStrictPlaybackError(w, strictPlaybackErrorFor(activateErr))
				return
			}
			binding = activated
		}
		reqCtx.Playback = &binding
		next(w, r.WithContext(context.WithValue(r.Context(), requestContextKey{}, reqCtx)))
	}
}

// strictPlaybackServerAllowed is the live access predicate for a strict attempt's
// server: the account grant (re-read from the user store above) and the current
// aggregation switch. It is checked before activating a pending candidate and
// for new playback operations. Cleanup checks the account grant alone.
func (a *App) strictPlaybackServerAllowed(reqCtx *RequestContext, serverID string) bool {
	if serverID == "" {
		return false
	}
	return a.isServerAllowed(reqCtx, serverID) && a.canUseAggregatedServer(reqCtx, serverID)
}

// claimStrictPlaybackSlotForRequest applies a pending candidate's frozen
// reservation to the concurrency limiter. The strict request context is installed
// first because the limiter path performs role/authorization checks on it; the
// serving instance and the item's original ID are resolved and its parental policy
// is enforced before the slot is claimed. It writes its own response (including the
// limiter's 429) and returns false when the request must not continue.
func (a *App) claimStrictPlaybackSlotForRequest(w http.ResponseWriter, r *http.Request, reqCtx *RequestContext, binding playbackIdentityBinding) bool {
	strictRequest := r.WithContext(context.WithValue(r.Context(), requestContextKey{}, reqCtx))
	client := a.Upstream.ClientByID(binding.ServerID)
	if client == nil || !client.IsOnline() {
		writeStrictPlaybackError(w, errStrictSourceUnavailable)
		return false
	}
	originalID, ok := translateVirtualIDForServer(binding.VirtualItemID, binding.ServerID, a.IDStore)
	if !ok || originalID == "" {
		writeStrictPlaybackError(w, errStrictSourceUnavailable)
		return false
	}
	allowed, confirmed := a.checkInstanceParentalAccess(strictRequest, client, originalID)
	if !confirmed {
		writeStrictPlaybackError(w, errStrictRatingUnconfirmed)
		return false
	}
	if !allowed {
		writeStrictPlaybackError(w, errStrictRatingForbidden)
		return false
	}
	return a.claimStrictPlaybackSlot(w, strictRequest, binding)
}

// validateStrictParentSession re-reads the ordinary local session behind a
// capability. A capability is a playback-scoped handle, never a substitute for the
// authenticated session or its grants: a revoked token, a disabled/deleted user or
// narrowed server grants must fail closed here on the very next request.
func (a *App) validateStrictParentSession(parentToken string) *tokenInfo {
	if parentToken == "" {
		return nil
	}
	info := a.Auth.ValidateToken(parentToken)
	if info == nil {
		return nil
	}
	if info.Role != "user" {
		return info
	}
	if a.UserStore == nil {
		return nil
	}
	live, ok := a.UserStore.LiveGrants(info.UserID)
	if !ok {
		return nil
	}
	info.AllowedServers = live
	return info
}

// newStrictPlaybackContext mirrors withContext for the strict path, with the
// capability and the parent session kept separate: PlaybackCredential is the
// playback credential the client presented, ProxyToken is the ordinary local
// session used for ownership checks and diagnostics.
func (a *App) newStrictPlaybackContext(r *http.Request, proxyUser *tokenInfo, parentToken, capability string) *RequestContext {
	maxRating, blockUnrated := 0, false
	if proxyUser != nil && proxyUser.Role == "user" && a.UserStore != nil {
		if user := a.UserStore.Get(proxyUser.UserID); user != nil {
			maxRating, blockUnrated = user.MaxParentalRating, user.BlockUnratedItems
		}
	}
	return &RequestContext{
		Headers:            r.Header.Clone(),
		ProxyToken:         parentToken,
		ProxyUser:          proxyUser,
		PlaybackCredential: capability,
		LegacyProxyUserID:  a.Auth.ProxyUserID(),
		Identifiers:        a.newRequestIdentifierLookup(),
		Parental:           newParentalReadSession(maxRating, blockUnrated),
	}
}

// strictPlaybackAttemptID reads the attempt ID from the frozen route shape. The
// mux pattern supplies it as {attemptID}; the path parse keeps the check
// authoritative even when a handler is reached without that path value.
func strictPlaybackAttemptID(r *http.Request) (string, bool) {
	if r == nil || r.URL == nil {
		return "", false
	}
	if id := r.PathValue("attemptID"); validPlaybackAttemptID(id) {
		return id, true
	}
	path := r.URL.Path
	if !strings.HasPrefix(path, playbackIngressPrefix) {
		return "", false
	}
	id := path[len(playbackIngressPrefix):]
	if slash := strings.IndexByte(id, '/'); slash >= 0 {
		id = id[:slash]
	}
	if !validPlaybackAttemptID(id) {
		return "", false
	}
	return id, true
}

// validPlaybackAttemptID validates shape only. The capability digest lookup is the
// authority; this check just keeps non-attempt paths away from the store.
func validPlaybackAttemptID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if c <= 0x20 || c >= 0x7f || c == '/' || c == '\\' {
			return false
		}
	}
	return true
}

// strictPlaybackStopRequest reports whether the request asks to end playback. It is
// decided from the frozen strict route shape and method, never from a client header,
// so a streaming request cannot be mistaken for a stop. The suffixes are the local
// routes the strict pattern mirrors: POST .../Sessions/Playing/Stopped and
// DELETE .../Videos/ActiveEncodings.
func strictPlaybackStopRequest(r *http.Request) bool {
	return playbackCleanupOperation(r) != 0
}

func strictPlaybackAssociationsMatch(r *http.Request, attempt, capability string) bool {
	for key, values := range r.URL.Query() {
		expected := ""
		switch {
		case isProxyCredentialQueryKey(key), strings.EqualFold(key, "EioPlaybackToken"):
			expected = capability
		case strings.EqualFold(key, "EioAttemptId"):
			expected = attempt
		default:
			continue
		}
		for _, value := range values {
			if value != expected {
				return false
			}
		}
	}
	return true
}
