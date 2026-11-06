package backend

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Playback identity binding store (design §4.2/4.3).
//
// One binding covers one playback attempt: local user/session, upstream instance,
// authorized virtual item/source and the immutable managed client identity chosen
// before PlaybackInfo. It is not keyed by item, device ID or "current upstream
// account", so replays, second devices and multi-candidate aggregation cannot
// overwrite each other. No credential is persisted: the local session token
// becomes a domain-separated digest, the snapshot carries only managed identity
// headers, and auth compatibility is an opaque principal, identity fingerprint and
// config revision.
const (
	playbackIdentityMaxEntries  = 4096
	playbackIdentityMaxPerUser  = 64
	playbackIdentityPendingTTL  = 2 * time.Minute
	playbackIdentityActiveIdle  = 30 * time.Minute
	playbackIdentityTerminalTTL = 2 * time.Minute
	playbackIdentityIDBytes     = 16
	playbackIdentityIDRetries   = 16
)

// Binding lifecycle errors. They are distinct because recovery differs:
// capacity is retryable, terminal means "start a new attempt", stopping means
// the attempt is draining and only idempotent stop/terminal cleanup may run.
var (
	ErrPlaybackIdentityInvalid          = errors.New("playback identity binding: invalid input")
	ErrPlaybackIdentityNotFound         = errors.New("playback identity binding: not found")
	ErrPlaybackIdentityOwnerMismatch    = errors.New("playback identity binding: owner mismatch")
	ErrPlaybackIdentityCapacity         = errors.New("playback identity binding: capacity exhausted")
	ErrPlaybackIdentityStopping         = errors.New("playback identity binding: stopping")
	ErrPlaybackIdentityTerminal         = errors.New("playback identity binding: terminal")
	ErrPlaybackIdentityAuthIncompatible = errors.New("playback identity binding: auth incompatible")
	ErrPlaybackIdentityAliasConflict    = errors.New("playback identity binding: alias conflict")
)

// pending → active → stopping → retired, with failed as the other final state.
// Transitions are idempotent; terminal states are final and never revive.
type playbackIdentityState uint8

const (
	playbackIdentityPending playbackIdentityState = iota
	playbackIdentityActive
	playbackIdentityStopping
	playbackIdentityRetired
	playbackIdentityFailed
)

func (s playbackIdentityState) terminal() bool {
	return s == playbackIdentityRetired || s == playbackIdentityFailed
}

// playbackIdentityOwner associates a binding with one local user and one
// authenticated local session. SessionDigest is a domain-separated SHA-256 of
// the local proxy token; the raw token must never be stored, logged or returned.
type playbackIdentityOwner struct {
	UserID        string
	SessionDigest string
}

const playbackIdentitySessionDigestVersion = "v1"

// newPlaybackIdentityOwner hashes the token immediately. An empty token leaves
// the digest empty so reserve rejects an unauthenticated owner instead of
// matching every other empty digest.
func newPlaybackIdentityOwner(userID string, localSessionToken string) playbackIdentityOwner {
	owner := playbackIdentityOwner{UserID: userID}
	if localSessionToken == "" {
		return owner
	}
	sum := sha256.Sum256([]byte(playbackIdentitySessionDigestVersion + "\x00" + localSessionToken))
	owner.SessionDigest = playbackIdentitySessionDigestVersion + ":" + hex.EncodeToString(sum[:])
	return owner
}

func samePlaybackIdentityOwner(a, b playbackIdentityOwner) bool {
	return a.UserID == b.UserID && samePlaybackIdentitySession(a.SessionDigest, b.SessionDigest)
}

func samePlaybackIdentitySession(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// playbackIdentityAuth is the credential-free compatibility record: opaque
// upstream account principal, selected-identity fingerprint and configuration
// revision. All three are mandatory; an unknown/empty record never matches,
// including two unknown records (fail closed, not assume compatible).
type playbackIdentityAuth struct {
	Principal           string
	IdentityFingerprint string
	ConfigRevision      string
}

func (a playbackIdentityAuth) complete() bool {
	return a.Principal != "" && a.IdentityFingerprint != "" && a.ConfigRevision != ""
}

// compatible requires complete metadata on both sides and exact equality.
func (a playbackIdentityAuth) compatible(current playbackIdentityAuth) bool {
	if !a.complete() || !current.complete() {
		return false
	}
	return a == current
}

// playbackAttemptRequest is the reservation input for one PlaybackInfo attempt.
// VirtualItemID is the client-facing item ID; SourceID is optional because
// transcode/fallback candidates may not expose a stable media source ID yet.
type playbackAttemptRequest struct {
	Owner          playbackIdentityOwner
	ServerID       string
	VirtualItemID  string
	SourceID       string
	Identity       identitySnapshot
	Principal      string
	ConfigRevision string
}

func (req playbackAttemptRequest) validate() error {
	switch {
	case req.Owner.UserID == "":
		return fmt.Errorf("%w: empty owner user", ErrPlaybackIdentityInvalid)
	case req.Owner.SessionDigest == "":
		return fmt.Errorf("%w: missing hashed local session", ErrPlaybackIdentityInvalid)
	case req.ServerID == "":
		return fmt.Errorf("%w: empty server id", ErrPlaybackIdentityInvalid)
	case req.VirtualItemID == "":
		return fmt.Errorf("%w: empty virtual item id", ErrPlaybackIdentityInvalid)
	case req.Identity.Source == "":
		return fmt.Errorf("%w: missing identity source", ErrPlaybackIdentityInvalid)
	case !validPlaybackIdentityUserAgent(req.Identity.Headers.Get("User-Agent")):
		// Strict Web attempts need a non-empty, legal UA at creation. The store
		// never substitutes a source or a default UA (design F8).
		return fmt.Errorf("%w: incomplete strict identity", ErrPlaybackIdentityInvalid)
	case req.Principal == "":
		return fmt.Errorf("%w: missing auth principal", ErrPlaybackIdentityInvalid)
	case req.ConfigRevision == "":
		return fmt.Errorf("%w: missing config revision", ErrPlaybackIdentityInvalid)
	default:
		return nil
	}
}

// playbackIdentityBinding is an immutable snapshot. Identity is a deep copy, so
// mutating a returned header map never changes the store or another reader.
type playbackIdentityBinding struct {
	AttemptID string
	Owner     playbackIdentityOwner

	ServerID      string
	VirtualItemID string
	SourceID      string

	Identity identitySnapshot
	Auth     playbackIdentityAuth

	UpstreamPlaySessionID string
	SlotReservation       playbackSlotReservation
	CleanupConfirmed      uint8

	State          playbackIdentityState
	CreatedAt      time.Time
	LastUsedAt     time.Time
	StateChangedAt time.Time
	LeaseCount     int
}

func (b playbackIdentityBinding) clone() playbackIdentityBinding {
	cloned := b
	cloned.Identity = clonePlaybackIdentitySnapshot(b.Identity)
	return cloned
}

// compatibleAuth lets a lease holder compare the current auth generation with
// its captured snapshot without touching the store, so a concurrent stop cannot
// invalidate an already authorized send. Unknown metadata fails closed here too.
func (b playbackIdentityBinding) compatibleAuth(current playbackIdentityAuth) bool {
	return b.Auth.compatible(current)
}

type playbackIdentityEntry struct {
	attemptID string
	owner     playbackIdentityOwner

	serverID      string
	virtualItemID string
	sourceID      string

	identity identitySnapshot
	auth     playbackIdentityAuth

	upstreamPlaySessionID string
	slotReservation       playbackSlotReservation
	cleanupConfirmed      uint8
	cleanupInFlight       uint8
	activationSequence    uint64

	state          playbackIdentityState
	createdAt      time.Time
	lastUsedAt     time.Time
	stateChangedAt time.Time

	// leases counts in-flight read references. An expired leased entry is kept
	// until release but can no longer be resolved: deadlines never extend.
	leases int
}

func (e *playbackIdentityEntry) requireWorkable() error {
	switch {
	case e.state.terminal():
		return ErrPlaybackIdentityTerminal
	case e.state == playbackIdentityStopping:
		return ErrPlaybackIdentityStopping
	default:
		return nil
	}
}

func (e *playbackIdentityEntry) snapshot() playbackIdentityBinding {
	return playbackIdentityBinding{
		AttemptID:             e.attemptID,
		Owner:                 e.owner,
		ServerID:              e.serverID,
		VirtualItemID:         e.virtualItemID,
		SourceID:              e.sourceID,
		Identity:              clonePlaybackIdentitySnapshot(e.identity),
		Auth:                  e.auth,
		UpstreamPlaySessionID: e.upstreamPlaySessionID,
		SlotReservation:       e.slotReservation,
		CleanupConfirmed:      e.cleanupConfirmed,
		State:                 e.state,
		CreatedAt:             e.createdAt,
		LastUsedAt:            e.lastUsedAt,
		StateChangedAt:        e.stateChangedAt,
		LeaseCount:            e.leases,
	}
}

// playbackIdentityAliasKey is one server-scoped upstream session alias; the local
// opaque handle is the attempt ID.
type playbackIdentityAliasKey struct {
	AttemptID string
	ServerID  string
	Value     string
}

// playbackIdentityStore is a bounded, mutex-protected in-memory store, never
// persisted: after a restart strict bindings are gone and the player renegotiates.
type playbackIdentityStore struct {
	mu                 sync.Mutex
	entries            map[string]*playbackIdentityEntry
	aliases            map[playbackIdentityAliasKey]string
	perUser            map[string]int
	activationSequence uint64

	maxEntries int
	maxPerUser int

	pendingTTL    time.Duration
	activeIdleTTL time.Duration
	terminalTTL   time.Duration

	now   func() time.Time
	newID func() string
}

func newPlaybackIdentityStore() *playbackIdentityStore {
	return newPlaybackIdentityStoreWithClock(time.Now)
}

// newPlaybackIdentityStoreWithClock lets tests advance time explicitly.
func newPlaybackIdentityStoreWithClock(now func() time.Time) *playbackIdentityStore {
	if now == nil {
		now = time.Now
	}
	return &playbackIdentityStore{
		entries:       map[string]*playbackIdentityEntry{},
		aliases:       map[playbackIdentityAliasKey]string{},
		perUser:       map[string]int{},
		maxEntries:    playbackIdentityMaxEntries,
		maxPerUser:    playbackIdentityMaxPerUser,
		pendingTTL:    playbackIdentityPendingTTL,
		activeIdleTTL: playbackIdentityActiveIdle,
		terminalTTL:   playbackIdentityTerminalTTL,
		now:           now,
		newID:         func() string { return randomHex(playbackIdentityIDBytes) },
	}
}

// reserve creates the pending binding for one PlaybackInfo candidate. Capacity
// failures are retryable; active bindings are never evicted.
func (s *playbackIdentityStore) reserve(req playbackAttemptRequest) (playbackIdentityBinding, error) {
	if err := req.validate(); err != nil {
		return playbackIdentityBinding{}, err
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	if len(s.entries) >= s.maxEntries {
		return playbackIdentityBinding{}, fmt.Errorf("%w: global limit", ErrPlaybackIdentityCapacity)
	}
	if s.perUser[req.Owner.UserID] >= s.maxPerUser {
		return playbackIdentityBinding{}, fmt.Errorf("%w: per-user limit", ErrPlaybackIdentityCapacity)
	}
	attemptID, err := s.newAttemptIDLocked()
	if err != nil {
		return playbackIdentityBinding{}, err
	}
	entry := &playbackIdentityEntry{
		attemptID:     attemptID,
		owner:         req.Owner,
		serverID:      req.ServerID,
		virtualItemID: req.VirtualItemID,
		sourceID:      req.SourceID,
		// Only managed identity headers are stored; compound/auth/token headers
		// are projected away even if a caller handed in raw captured headers.
		identity: normalizePlaybackIdentitySnapshot(req.Identity),
		auth: playbackIdentityAuth{
			Principal:           req.Principal,
			IdentityFingerprint: playbackIdentityFingerprint(req.Identity),
			ConfigRevision:      req.ConfigRevision,
		},
		state:          playbackIdentityPending,
		createdAt:      now,
		lastUsedAt:     now,
		stateChangedAt: now,
	}
	s.entries[attemptID] = entry
	s.perUser[req.Owner.UserID]++
	return entry.snapshot(), nil
}

// activate commits a pending binding after PlaybackInfo returned. The upstream
// PlaySessionId is bound atomically; repeated activation is idempotent.
func (s *playbackIdentityStore) activate(attemptID string, owner playbackIdentityOwner, upstreamPlaySessionID string) (playbackIdentityBinding, error) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	entry, err := s.entryForOwnerLocked(attemptID, owner, now)
	if err != nil {
		return playbackIdentityBinding{}, err
	}
	if err := entry.requireWorkable(); err != nil {
		return playbackIdentityBinding{}, err
	}
	if err := s.bindAliasLocked(entry, upstreamPlaySessionID, now); err != nil {
		return playbackIdentityBinding{}, err
	}
	if s.aliasCleanupInFlightLocked(entry) {
		return playbackIdentityBinding{}, ErrPlaybackIdentityStopping
	}
	if entry.state == playbackIdentityPending {
		s.activationSequence++
		entry.activationSequence = s.activationSequence
		entry.state = playbackIdentityActive
		entry.stateChangedAt = now
		entry.lastUsedAt = now
	}
	return entry.snapshot(), nil
}

// bindAlias records the server-scoped upstream session alias. A different value
// is a conflict because a new attempt must be started instead of moving an
// existing one. Only a terminal/expired holder's alias can be taken over.
func (s *playbackIdentityStore) bindAlias(attemptID string, owner playbackIdentityOwner, upstreamPlaySessionID string) (playbackIdentityBinding, error) {
	if upstreamPlaySessionID == "" {
		return playbackIdentityBinding{}, fmt.Errorf("%w: empty alias", ErrPlaybackIdentityInvalid)
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	entry, err := s.entryForOwnerLocked(attemptID, owner, now)
	if err != nil {
		return playbackIdentityBinding{}, err
	}
	if err := entry.requireWorkable(); err != nil {
		return playbackIdentityBinding{}, err
	}
	if err := s.bindAliasLocked(entry, upstreamPlaySessionID, now); err != nil {
		return playbackIdentityBinding{}, err
	}
	return entry.snapshot(), nil
}

func (s *playbackIdentityStore) bindAliasLocked(entry *playbackIdentityEntry, value string, now time.Time) error {
	if value == "" {
		return nil
	}
	if existing := entry.upstreamPlaySessionID; existing != "" {
		if existing == value {
			return nil
		}
		return fmt.Errorf("%w: attempt already has an alias", ErrPlaybackIdentityAliasConflict)
	}
	key := playbackIdentityAliasKey{AttemptID: entry.attemptID, ServerID: entry.serverID, Value: value}
	entry.upstreamPlaySessionID = value
	s.aliases[key] = entry.attemptID
	return nil
}

// resolveAlias verifies an alias inside an already authenticated attempt. Aliases
// are intentionally not globally unique: upstreams can reuse the same session
// across candidates, devices and replays. No ingress routes by alias alone.
func (s *playbackIdentityStore) resolveAlias(attemptID, serverID string, upstreamPlaySessionID string, owner playbackIdentityOwner) (playbackIdentityBinding, error) {
	if serverID == "" || upstreamPlaySessionID == "" {
		return playbackIdentityBinding{}, fmt.Errorf("%w: invalid alias lookup", ErrPlaybackIdentityInvalid)
	}
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	_, ok := s.aliases[playbackIdentityAliasKey{AttemptID: attemptID, ServerID: serverID, Value: upstreamPlaySessionID}]
	if !ok {
		return playbackIdentityBinding{}, ErrPlaybackIdentityNotFound
	}
	entry := s.entries[attemptID]
	if entry == nil || s.expiredLocked(entry, now) {
		return playbackIdentityBinding{}, ErrPlaybackIdentityNotFound
	}
	if !samePlaybackIdentityOwner(entry.owner, owner) {
		return playbackIdentityBinding{}, ErrPlaybackIdentityNotFound
	}
	if err := entry.requireWorkable(); err != nil {
		return playbackIdentityBinding{}, err
	}
	return entry.snapshot(), nil
}

// resolve reads a binding by attempt ID without renewing it. It is the entry
// point for authorization checks followed by a separate lease or touch.
func (s *playbackIdentityStore) resolve(attemptID string, owner playbackIdentityOwner) (playbackIdentityBinding, error) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	entry, err := s.entryForOwnerLocked(attemptID, owner, now)
	if err != nil {
		return playbackIdentityBinding{}, err
	}
	if err := entry.requireWorkable(); err != nil {
		return playbackIdentityBinding{}, err
	}
	return entry.snapshot(), nil
}

// lease acquires one in-flight read reference and renews the active idle window.
// The holder's snapshot stays usable after a later stop or deadline while new
// readers are rejected; pending leases do not extend the pending deadline.
func (s *playbackIdentityStore) lease(attemptID string, owner playbackIdentityOwner) (*playbackIdentityLease, error) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	entry, err := s.entryForOwnerLocked(attemptID, owner, now)
	if err != nil {
		return nil, err
	}
	if err := entry.requireWorkable(); err != nil {
		return nil, err
	}
	if entry.state == playbackIdentityActive {
		entry.lastUsedAt = now
	}
	entry.leases++
	return &playbackIdentityLease{store: s, attemptID: attemptID, binding: entry.snapshot()}, nil
}

// touch renews the active idle window for a heartbeat/progress/segment. A pending
// touch is a no-op; touch never revives stopping or terminal bindings.
func (s *playbackIdentityStore) touch(attemptID string, owner playbackIdentityOwner) (playbackIdentityBinding, error) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	entry, err := s.entryForOwnerLocked(attemptID, owner, now)
	if err != nil {
		return playbackIdentityBinding{}, err
	}
	if err := entry.requireWorkable(); err != nil {
		return playbackIdentityBinding{}, err
	}
	if entry.state == playbackIdentityActive {
		entry.lastUsedAt = now
	}
	return entry.snapshot(), nil
}

// validateAuth checks the currently effective auth/identity generation. The
// store never rotates credentials: an incompatible generation is an explicit
// failure for bounded renegotiation, not a silent identity switch.
func (s *playbackIdentityStore) validateAuth(attemptID string, owner playbackIdentityOwner, current playbackIdentityAuth) error {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	entry, err := s.entryForOwnerLocked(attemptID, owner, now)
	if err != nil {
		return err
	}
	if err := entry.requireWorkable(); err != nil {
		return err
	}
	if !entry.auth.compatible(current) {
		return ErrPlaybackIdentityAuthIncompatible
	}
	return nil
}

// stop moves the attempt into the draining state. It is idempotent and is the
// only operation accepted afterwards: new reads, activation, alias binds and auth
// checks are rejected. The first transition time is frozen, so a repeated stop
// never extends terminal retention.
func (s *playbackIdentityStore) stop(attemptID string, owner playbackIdentityOwner) error {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	entry, err := s.entryForOwnerLocked(attemptID, owner, now)
	if err != nil {
		return err
	}
	if entry.state == playbackIdentityPending || entry.state == playbackIdentityActive {
		entry.state = playbackIdentityStopping
		entry.stateChangedAt = now
	}
	return nil
}

// retire finalizes a drained attempt. Terminal states are final, so a late
// retire never converts failed into retired and never revives the binding.
func (s *playbackIdentityStore) retire(attemptID string, owner playbackIdentityOwner) error {
	return s.finish(attemptID, owner, playbackIdentityRetired)
}

// fail records the failure terminal state. Like retire it is idempotent.
func (s *playbackIdentityStore) fail(attemptID string, owner playbackIdentityOwner) error {
	return s.finish(attemptID, owner, playbackIdentityFailed)
}

func (s *playbackIdentityStore) finish(attemptID string, owner playbackIdentityOwner, state playbackIdentityState) error {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	entry, err := s.entryForOwnerLocked(attemptID, owner, now)
	if err != nil {
		return err
	}
	if entry.state.terminal() {
		return nil
	}
	entry.state = state
	entry.stateChangedAt = now
	return nil
}

// cleanup removes expired unleased bindings and returns how many were removed.
// Every operation already prunes first, so correctness does not depend on it.
func (s *playbackIdentityStore) cleanup() int {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pruneLocked(now)
}

func (s *playbackIdentityStore) count() int {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	return len(s.entries)
}

func (s *playbackIdentityStore) entryForOwnerLocked(attemptID string, owner playbackIdentityOwner, now time.Time) (*playbackIdentityEntry, error) {
	entry := s.entries[attemptID]
	if entry == nil {
		return nil, ErrPlaybackIdentityNotFound
	}
	if !samePlaybackIdentityOwner(entry.owner, owner) {
		return nil, ErrPlaybackIdentityOwnerMismatch
	}
	if s.expiredLocked(entry, now) {
		return nil, ErrPlaybackIdentityNotFound
	}
	return entry, nil
}

// expiryTime is the state-specific deadline. Leases only defer removal, never
// extend validity.
func (s *playbackIdentityStore) expiryTime(entry *playbackIdentityEntry) time.Time {
	switch entry.state {
	case playbackIdentityPending:
		return entry.createdAt.Add(s.pendingTTL)
	case playbackIdentityActive:
		return entry.lastUsedAt.Add(s.activeIdleTTL)
	default:
		return entry.stateChangedAt.Add(s.terminalTTL)
	}
}

func (s *playbackIdentityStore) expiredLocked(entry *playbackIdentityEntry, now time.Time) bool {
	return !now.Before(s.expiryTime(entry))
}

func (s *playbackIdentityStore) pruneLocked(now time.Time) int {
	removed := 0
	for attemptID, entry := range s.entries {
		if entry.leases > 0 {
			continue
		}
		if now.Before(s.expiryTime(entry)) {
			continue
		}
		s.removeLocked(attemptID)
		removed++
	}
	return removed
}

func (s *playbackIdentityStore) removeLocked(attemptID string) {
	entry := s.entries[attemptID]
	if entry == nil {
		return
	}
	delete(s.entries, attemptID)
	if count := s.perUser[entry.owner.UserID]; count <= 1 {
		delete(s.perUser, entry.owner.UserID)
	} else {
		s.perUser[entry.owner.UserID] = count - 1
	}
	if entry.upstreamPlaySessionID != "" {
		key := playbackIdentityAliasKey{AttemptID: entry.attemptID, ServerID: entry.serverID, Value: entry.upstreamPlaySessionID}
		// A newer attempt may own the alias now; only drop our own index entry.
		if s.aliases[key] == attemptID {
			delete(s.aliases, key)
		}
	}
}

func (s *playbackIdentityStore) newAttemptIDLocked() (string, error) {
	for attempt := 0; attempt < playbackIdentityIDRetries; attempt++ {
		id := s.newID()
		if id == "" {
			continue
		}
		if s.entries[id] == nil {
			return id, nil
		}
	}
	return "", fmt.Errorf("%w: attempt id generation failed", ErrPlaybackIdentityInvalid)
}

// playbackIdentityLease is one in-flight read reference. Release is idempotent.
type playbackIdentityLease struct {
	store     *playbackIdentityStore
	attemptID string
	binding   playbackIdentityBinding
	released  bool
}

// Binding returns the immutable snapshot captured at acquisition, so a
// concurrent stop cannot change an authorized in-flight request.
func (l *playbackIdentityLease) Binding() playbackIdentityBinding {
	if l == nil {
		return playbackIdentityBinding{}
	}
	return l.binding.clone()
}

// Release ends the in-flight reference; later calls are no-ops.
func (l *playbackIdentityLease) Release() bool {
	if l == nil || l.store == nil || l.released {
		return false
	}
	l.released = true
	now := l.store.now()
	l.store.mu.Lock()
	defer l.store.mu.Unlock()
	if entry := l.store.entries[l.attemptID]; entry != nil && entry.leases > 0 {
		entry.leases--
	}
	l.store.pruneLocked(now)
	return true
}

// playbackIdentityFingerprint hashes only the managed identity fields, is
// deterministic across map order, ignores transport headers and returns a digest.
const playbackIdentityFingerprintVersion = "v1"

var playbackIdentityFingerprintHeaders = [...]string{
	"User-Agent",
	"X-Emby-Client",
	"X-Emby-Client-Version",
	"X-Emby-Device-Name",
	"X-Emby-Device-Id",
}

func playbackIdentityFingerprint(snapshot identitySnapshot) string {
	digest := sha256.New()
	writePlaybackIdentityFingerprintField(digest, playbackIdentityFingerprintVersion)
	for _, name := range playbackIdentityFingerprintHeaders {
		writePlaybackIdentityFingerprintField(digest, name)
		writePlaybackIdentityFingerprintField(digest, snapshot.Headers.Get(name))
	}
	return playbackIdentityFingerprintVersion + ":" + hex.EncodeToString(digest.Sum(nil))
}

func writePlaybackIdentityFingerprintField(digest hash.Hash, value string) {
	_, _ = fmt.Fprintf(digest, "%d:", len(value))
	_, _ = digest.Write([]byte(value))
}

func clonePlaybackIdentitySnapshot(snapshot identitySnapshot) identitySnapshot {
	return identitySnapshot{Source: snapshot.Source, Headers: cloneHeader(snapshot.Headers)}
}

// playbackIdentitySnapshotHeaders is the managed identity/behaviour projection
// stored in a binding. Credential carriers (Authorization, X-Emby-Authorization,
// X-Emby-Token, X-MediaBrowser-Token, Cookie, ...) are never copied, so neither
// the stored snapshot nor a diagnostic snapshot can persist a compound/auth token.
var playbackIdentitySnapshotHeaders = [...]string{
	"User-Agent",
	"X-Emby-Client",
	"X-Emby-Client-Version",
	"X-Emby-Device-Name",
	"X-Emby-Device-Id",
	"Accept",
	"Accept-Language",
}

func normalizePlaybackIdentitySnapshot(snapshot identitySnapshot) identitySnapshot {
	headers := http.Header{}
	for _, name := range playbackIdentitySnapshotHeaders {
		if value := snapshot.Headers.Get(name); value != "" {
			headers.Set(name, value)
		}
	}
	return identitySnapshot{Source: snapshot.Source, Headers: headers}
}

// validPlaybackIdentityUserAgent accepts a non-empty header value without control
// bytes. It validates rather than repairs: the store never invents a UA.
func validPlaybackIdentityUserAgent(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] == 0x7f {
			return false
		}
	}
	return true
}
