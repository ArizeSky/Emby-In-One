package backend

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The UA library is the persistent store of captured client identities. It is
// deliberately independent of captured-headers.json: that file is a disposable
// passthrough fallback cache, while this one holds the administrator's resident
// identities and must never be silently discarded.
const (
	// recentUALimit is how many recent full identities are kept.
	recentUALimit = 10
	// oldUAThreshold is the age after which a resident identity is flagged as
	// old. Exactly this duration does NOT flag it.
	oldUAThreshold = 30 * 24 * time.Hour
	// maxUAFieldBytes bounds a single captured identity field. A header line far
	// longer than this is a mistake or an attack, not a real client value.
	maxUAFieldBytes = 4096
	// maxUANameRunes bounds a resident display name, in runes.
	maxUANameRunes = 80
)

// Sentinel errors let the HTTP layer map a store failure to the right status
// code without string matching.
var (
	errUALibraryUnavailable = errors.New("UA 身份库不可用")
	errUANotFound           = errors.New("UA 记录不存在")
	errUAConflict           = errors.New("UA 记录已被修改，请刷新后重试")
	errUAInUse              = errors.New("常驻 UA 正在被服务器引用")
)

// uaDuplicateResidentError reports that an update would create a second resident
// record with an identity that is already saved under another ID.
type uaDuplicateResidentError struct{ ExistingID string }

func (e *uaDuplicateResidentError) Error() string {
	return "该身份已存在于另一条常驻 UA（" + e.ExistingID + "），请直接改绑该记录"
}

// UAIdentity is the full client identity this feature manages. It is more than a
// User-Agent string: it is everything a real client presents so an upstream sees
// a stable, consistent client. No credential is ever part of it.
type UAIdentity struct {
	UserAgent      string `json:"userAgent"`
	Client         string `json:"client"`
	ClientVersion  string `json:"clientVersion"`
	DeviceName     string `json:"deviceName"`
	DeviceID       string `json:"deviceId"`
	Accept         string `json:"accept,omitempty"`
	AcceptLanguage string `json:"acceptLanguage,omitempty"`
}

// RecentUA is one entry in the rolling recent-capture list.
type RecentUA struct {
	ID              string     `json:"id"`
	Fingerprint     string     `json:"fingerprint"`
	Identity        UAIdentity `json:"identity"`
	FirstCapturedAt time.Time  `json:"firstCapturedAt"`
	LastCapturedAt  time.Time  `json:"lastCapturedAt"`
}

// ResidentUA is an administrator-owned, long-lived identity. IdentityVersion
// changes only when the identity fields actually change; Revision changes on any
// edit (rename included) and is the optimistic-concurrency token.
type ResidentUA struct {
	ID                 string     `json:"id"`
	Name               string     `json:"name"`
	Identity           UAIdentity `json:"identity"`
	Fingerprint        string     `json:"fingerprint"`
	IdentityCapturedAt time.Time  `json:"identityCapturedAt"`
	CreatedAt          time.Time  `json:"createdAt"`
	UpdatedAt          time.Time  `json:"updatedAt"`
	Revision           uint64     `json:"revision"`
	IdentityVersion    uint64     `json:"identityVersion"`
}

type uaLibrarySnapshot struct {
	Version            int          `json:"version"`
	BootstrapCompleted bool         `json:"bootstrapCompleted"`
	Recent             []RecentUA   `json:"recent"`
	Resident           []ResidentUA `json:"resident"`
}

func (s uaLibrarySnapshot) clone() uaLibrarySnapshot {
	out := uaLibrarySnapshot{
		Version:            s.Version,
		BootstrapCompleted: s.BootstrapCompleted,
		Recent:             make([]RecentUA, len(s.Recent)),
		Resident:           make([]ResidentUA, len(s.Resident)),
	}
	copy(out.Recent, s.Recent)
	copy(out.Resident, s.Resident)
	return out
}

// UALibraryStore owns the in-memory snapshot and the on-disk file. Every mutation
// runs under mu across "clone -> modify -> persist -> publish" so a failed write
// never publishes a state the caller was told succeeded.
type UALibraryStore struct {
	mu       sync.Mutex
	path     string
	logger   *Logger
	snapshot uaLibrarySnapshot
	loadErr  error
}

var activeUALibraryState struct {
	mu    sync.RWMutex
	store *UALibraryStore
}

func setActiveUALibrary(store *UALibraryStore) {
	activeUALibraryState.mu.Lock()
	defer activeUALibraryState.mu.Unlock()
	activeUALibraryState.store = store
}

// activeUALibrary returns the process-wide UA library, if one has been created.
// The pool uses it to resolve resident identities without threading the store
// through every client constructor. There is still exactly one instance.
func activeUALibrary() *UALibraryStore {
	activeUALibraryState.mu.RLock()
	defer activeUALibraryState.mu.RUnlock()
	return activeUALibraryState.store
}

// Available reports whether the library loaded cleanly. When it did not, the
// passthrough and custom modes keep working; only UA management and resident
// binding are unavailable.
func (s *UALibraryStore) Available() bool { return s != nil && s.loadErr == nil }

func (s *UALibraryStore) unavailableErr() error {
	if s == nil {
		return errUALibraryUnavailable
	}
	return s.loadErr
}

// Snapshot returns a deep copy so callers cannot mutate the live state.
func (s *UALibraryStore) Snapshot() uaLibrarySnapshot {
	if s == nil {
		return uaLibrarySnapshot{Version: 1}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot.clone()
}

// ResidentByID returns a copy of one resident identity.
func (s *UALibraryStore) ResidentByID(id string) (ResidentUA, bool) {
	if s == nil || strings.TrimSpace(id) == "" {
		return ResidentUA{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return ResidentUA{}, false
	}
	for _, resident := range s.snapshot.Resident {
		if resident.ID == id {
			return resident, true
		}
	}
	return ResidentUA{}, false
}

// uaIdentityFromHeaders extracts the managed identity fields from a request. It
// reuses normalizeCapturedHeaders so only the seven allowed fields survive, then
// rejects anything that is not a usable real client identity.
func uaIdentityFromHeaders(headers http.Header) (UAIdentity, error) {
	normalized := normalizeCapturedHeaders(headers)
	if !isRealClientIdentity(normalized) {
		return UAIdentity{}, errors.New("not a real client identity")
	}
	identity := UAIdentity{
		UserAgent:      normalized.Get("User-Agent"),
		Client:         normalized.Get("X-Emby-Client"),
		ClientVersion:  normalized.Get("X-Emby-Client-Version"),
		DeviceName:     normalized.Get("X-Emby-Device-Name"),
		DeviceID:       normalized.Get("X-Emby-Device-Id"),
		Accept:         normalized.Get("Accept"),
		AcceptLanguage: normalized.Get("Accept-Language"),
	}
	if strings.TrimSpace(identity.UserAgent) == "" {
		return UAIdentity{}, errors.New("empty user agent")
	}
	for _, value := range []string{
		identity.UserAgent, identity.Client, identity.ClientVersion,
		identity.DeviceName, identity.DeviceID, identity.Accept, identity.AcceptLanguage,
	} {
		if strings.ContainsAny(value, "\r\n") {
			return UAIdentity{}, errors.New("identity field contains a line break")
		}
		if len(value) > maxUAFieldBytes {
			return UAIdentity{}, errors.New("identity field is too long")
		}
	}
	return identity, nil
}

// uaIdentityHeaders rebuilds the outbound header set for an identity. Only
// non-empty fields are set, so a stored identity never invents a value the
// client did not send.
func uaIdentityHeaders(identity UAIdentity) http.Header {
	headers := http.Header{}
	set := func(key, value string) {
		if value != "" {
			headers.Set(key, value)
		}
	}
	set("User-Agent", identity.UserAgent)
	set("X-Emby-Client", identity.Client)
	set("X-Emby-Client-Version", identity.ClientVersion)
	set("X-Emby-Device-Name", identity.DeviceName)
	set("X-Emby-Device-Id", identity.DeviceID)
	set("Accept", identity.Accept)
	set("Accept-Language", identity.AcceptLanguage)
	return headers
}

// uaIdentityFingerprint hashes the seven identity fields with length prefixes so
// that no two different identities can collide by concatenation ambiguity. It
// covers only identity, never time, name, token or source IP.
func uaIdentityFingerprint(identity UAIdentity) string {
	hasher := sha256.New()
	var lengthBuf [8]byte
	for _, value := range []string{
		identity.UserAgent, identity.Client, identity.ClientVersion,
		identity.DeviceName, identity.DeviceID, identity.Accept, identity.AcceptLanguage,
	} {
		binary.BigEndian.PutUint64(lengthBuf[:], uint64(len(value)))
		hasher.Write(lengthBuf[:])
		hasher.Write([]byte(value))
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

// uaAge reports whether a capture time is known and, if so, whether it is older
// than the threshold. Exactly oldUAThreshold is not old. A future time is shown
// as 0 days rather than a negative age.
func uaAge(capturedAt, now time.Time) (known, old bool, days int) {
	if capturedAt.IsZero() {
		return false, false, 0
	}
	diff := now.Sub(capturedAt)
	if diff < 0 {
		diff = 0
	}
	days = int(diff / (24 * time.Hour))
	old = diff > oldUAThreshold
	return true, old, days
}

func newUAID() string { return randomHex(16) }

func truncateRunes(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max])
}

// defaultResidentName derives a display name from the client and device name.
func defaultResidentName(identity UAIdentity) string {
	var parts []string
	client := strings.TrimSpace(identity.Client)
	device := strings.TrimSpace(identity.DeviceName)
	if client != "" {
		parts = append(parts, client)
	}
	if device != "" && !strings.EqualFold(device, client) {
		parts = append(parts, device)
	}
	name := strings.TrimSpace(strings.Join(parts, " "))
	if name == "" {
		name = "常驻 UA"
	}
	return truncateRunes(name, maxUANameRunes)
}

func validateUAName(name string) error {
	length := len([]rune(strings.TrimSpace(name)))
	if length < 1 || length > maxUANameRunes {
		return &httpError{message: "常驻 UA 名称长度必须为 1-80 个字符"}
	}
	return nil
}

// CaptureAuthenticatedUA records one real administrator client login. It is the
// only writer of the recent list and the only place the first-capture bootstrap
// runs. An incomplete identity is skipped silently; only a persistence failure is
// returned, and it never blocks the login that triggered the capture.
func (s *UALibraryStore) CaptureAuthenticatedUA(headers http.Header) error {
	if s == nil {
		return nil
	}
	identity, err := uaIdentityFromHeaders(headers)
	if err != nil {
		return nil
	}
	fingerprint := uaIdentityFingerprint(identity)
	now := time.Now().UTC()

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return s.loadErr
	}

	next := s.snapshot.clone()

	existingIndex := -1
	for i := range next.Recent {
		if next.Recent[i].Fingerprint == fingerprint {
			existingIndex = i
			break
		}
	}
	if existingIndex >= 0 {
		entry := next.Recent[existingIndex]
		entry.LastCapturedAt = now
		next.Recent = append(next.Recent[:existingIndex], next.Recent[existingIndex+1:]...)
		next.Recent = append([]RecentUA{entry}, next.Recent...)
	} else {
		entry := RecentUA{
			ID:              newUAID(),
			Fingerprint:     fingerprint,
			Identity:        identity,
			FirstCapturedAt: now,
			LastCapturedAt:  now,
		}
		next.Recent = append([]RecentUA{entry}, next.Recent...)
		if len(next.Recent) > recentUALimit {
			next.Recent = next.Recent[:recentUALimit]
		}
	}

	if !next.BootstrapCompleted {
		resident := ResidentUA{
			ID:                 newUAID(),
			Name:               defaultResidentName(identity),
			Identity:           identity,
			Fingerprint:        fingerprint,
			IdentityCapturedAt: now,
			CreatedAt:          now,
			UpdatedAt:          now,
			Revision:           1,
			IdentityVersion:    1,
		}
		next.Resident = append(next.Resident, resident)
		next.BootstrapCompleted = true
	}

	if err := s.persistLocked(next); err != nil {
		s.warnf("save ua library failed: %v", err)
		return err
	}
	s.snapshot = next
	return nil
}

type saveRecentUAInput struct {
	RecentID string
	Name     string
}

// SaveRecent copies a recent capture into a new resident record. If an identical
// identity is already resident it returns that record with alreadySaved=true and
// changes nothing.
func (s *UALibraryStore) SaveRecent(input saveRecentUAInput) (ResidentUA, bool, error) {
	if s == nil {
		return ResidentUA{}, false, errUALibraryUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return ResidentUA{}, false, errUALibraryUnavailable
	}

	next := s.snapshot.clone()
	index := -1
	for i := range next.Recent {
		if next.Recent[i].ID == input.RecentID {
			index = i
			break
		}
	}
	if index < 0 {
		return ResidentUA{}, false, errUANotFound
	}
	recent := next.Recent[index]
	for _, resident := range next.Resident {
		if resident.Fingerprint == recent.Fingerprint {
			return resident, true, nil
		}
	}

	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = defaultResidentName(recent.Identity)
	}
	if err := validateUAName(name); err != nil {
		return ResidentUA{}, false, err
	}

	now := time.Now().UTC()
	resident := ResidentUA{
		ID:                 newUAID(),
		Name:               name,
		Identity:           recent.Identity,
		Fingerprint:        recent.Fingerprint,
		IdentityCapturedAt: recent.LastCapturedAt,
		CreatedAt:          now,
		UpdatedAt:          now,
		Revision:           1,
		IdentityVersion:    1,
	}
	next.Resident = append(next.Resident, resident)
	if err := s.persistLocked(next); err != nil {
		return ResidentUA{}, false, err
	}
	s.snapshot = next
	return resident, false, nil
}

type renameResidentUAInput struct {
	ID               string
	Name             string
	ExpectedRevision uint64
}

// RenameResident changes only the display name, UpdatedAt and Revision. It never
// touches the identity or its capture time, so it never forces a reconnect.
func (s *UALibraryStore) RenameResident(input renameResidentUAInput) (ResidentUA, error) {
	if s == nil {
		return ResidentUA{}, errUALibraryUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return ResidentUA{}, errUALibraryUnavailable
	}

	next := s.snapshot.clone()
	index := -1
	for i := range next.Resident {
		if next.Resident[i].ID == input.ID {
			index = i
			break
		}
	}
	if index < 0 {
		return ResidentUA{}, errUANotFound
	}
	if next.Resident[index].Revision != input.ExpectedRevision {
		return ResidentUA{}, errUAConflict
	}
	name := strings.TrimSpace(input.Name)
	if err := validateUAName(name); err != nil {
		return ResidentUA{}, err
	}
	next.Resident[index].Name = name
	next.Resident[index].UpdatedAt = time.Now().UTC()
	next.Resident[index].Revision++
	if err := s.persistLocked(next); err != nil {
		return ResidentUA{}, err
	}
	s.snapshot = next
	return next.Resident[index], nil
}

type replaceResidentUAInput struct {
	ResidentID       string
	RecentID         string
	ExpectedRevision uint64
}

// ReplaceResident explicitly replaces a resident identity from a recent capture.
// It keeps the resident ID, name and server bindings. IdentityVersion only
// changes when the identity fields actually change.
func (s *UALibraryStore) ReplaceResident(input replaceResidentUAInput) (ResidentUA, error) {
	if s == nil {
		return ResidentUA{}, errUALibraryUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return ResidentUA{}, errUALibraryUnavailable
	}

	next := s.snapshot.clone()
	residentIndex := -1
	for i := range next.Resident {
		if next.Resident[i].ID == input.ResidentID {
			residentIndex = i
			break
		}
	}
	if residentIndex < 0 {
		return ResidentUA{}, errUANotFound
	}
	recentIndex := -1
	for i := range next.Recent {
		if next.Recent[i].ID == input.RecentID {
			recentIndex = i
			break
		}
	}
	if recentIndex < 0 {
		return ResidentUA{}, errUANotFound
	}
	if next.Resident[residentIndex].Revision != input.ExpectedRevision {
		return ResidentUA{}, errUAConflict
	}

	recent := next.Recent[recentIndex]
	target := &next.Resident[residentIndex]
	if recent.Fingerprint != target.Fingerprint {
		for i := range next.Resident {
			if i != residentIndex && next.Resident[i].Fingerprint == recent.Fingerprint {
				return ResidentUA{}, &uaDuplicateResidentError{ExistingID: next.Resident[i].ID}
			}
		}
		target.Identity = recent.Identity
		target.Fingerprint = recent.Fingerprint
		target.IdentityCapturedAt = recent.LastCapturedAt
		target.IdentityVersion++
	} else {
		target.IdentityCapturedAt = recent.LastCapturedAt
	}
	target.UpdatedAt = time.Now().UTC()
	target.Revision++

	if err := s.persistLocked(next); err != nil {
		return ResidentUA{}, err
	}
	s.snapshot = next
	return *target, nil
}

// DeleteResident removes a resident record when the revision matches. Server
// reference protection lives in the application layer, which knows the config.
func (s *UALibraryStore) DeleteResident(id string, expectedRevision uint64) error {
	if s == nil {
		return errUALibraryUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return errUALibraryUnavailable
	}

	next := s.snapshot.clone()
	index := -1
	for i := range next.Resident {
		if next.Resident[i].ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		return errUANotFound
	}
	if next.Resident[index].Revision != expectedRevision {
		return errUAConflict
	}
	next.Resident = append(next.Resident[:index], next.Resident[index+1:]...)
	if err := s.persistLocked(next); err != nil {
		return err
	}
	s.snapshot = next
	return nil
}
