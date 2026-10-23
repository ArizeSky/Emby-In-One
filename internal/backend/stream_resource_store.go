package backend

import (
	"sync"
	"time"
)

const (
	// maxStreamResourceEntries bounds the server-side opaque resource map. It is a
	// capacity budget, not a concurrency promise: roughly six 2-hour VOD sessions
	// at ~2400 resources each fit, and LL-HLS or multi-audio sessions use more.
	maxStreamResourceEntries = 16384
	// streamResourceIdleTTL is the idle lease for a resource session. Any authorized
	// request for a resource in the session renews it, so a VOD's unread tail stays
	// valid as long as the session keeps playing (plan §8.6.1).
	streamResourceIdleTTL = 30 * time.Minute
)

// streamResourceEntry is one server-side record of an external HLS resource that
// a restricted user may fetch only through this proxy. It stores the URL and its
// ownership, never video bytes.
type streamResourceEntry struct {
	UserID        string
	VirtualItemID string
	ServerID      string
	OriginalID    string
	ResourceURL   string
	ExpiresAt     time.Time
}

// streamResourceStore maps random opaque ids to their resource records. Records
// are bounded and leased; there is no public HMAC/long-token URL (plan §8.6.1).
type streamResourceStore struct {
	mu      sync.Mutex
	entries map[string]*streamResourceEntry
	max     int
	now     func() time.Time
}

func newStreamResourceStore() *streamResourceStore {
	return &streamResourceStore{
		entries: map[string]*streamResourceEntry{},
		max:     maxStreamResourceEntries,
		now:     time.Now,
	}
}

// pruneLocked drops idle-expired records. The caller holds s.mu.
func (s *streamResourceStore) pruneLocked(now time.Time) {
	for id, e := range s.entries {
		if now.After(e.ExpiresAt) {
			delete(s.entries, id)
		}
	}
}

// put registers one resource and returns its opaque id. It reports ok=false when
// the store is at capacity even after pruning idle sessions, so the caller can
// answer 503 instead of emitting a half manifest.
func (s *streamResourceStore) put(e streamResourceEntry) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.pruneLocked(now)
	if len(s.entries) >= s.max {
		return "", false
	}
	id := randomHex(16)
	e.ExpiresAt = now.Add(streamResourceIdleTTL)
	s.entries[id] = &e
	return id, true
}

// get returns the record for id, renewing its session lease. An unknown or
// idle-expired handle returns ok=false, which the caller answers with 410.
func (s *streamResourceStore) get(id string) (streamResourceEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	e, ok := s.entries[id]
	if !ok {
		return streamResourceEntry{}, false
	}
	if now.After(e.ExpiresAt) {
		delete(s.entries, id)
		return streamResourceEntry{}, false
	}
	e.ExpiresAt = now.Add(streamResourceIdleTTL)
	return *e, true
}

// size reports the current record count (used by tests).
func (s *streamResourceStore) size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.entries)
}
