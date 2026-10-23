package backend

import (
	"sync"
	"time"
)

const (
	maxStreamResourceEntries = 16384
	streamResourceIdleTTL    = 30 * time.Minute
	// A resource removed from a rolling playlist remains fetchable while a
	// lagging LL-HLS client catches up. VOD resources are retained for the session.
	streamResourceCatchupGrace = 2 * time.Minute
)

type streamResourceEntry struct {
	UserID        string
	VirtualItemID string
	ServerID      string
	OriginalID    string
	ResourceURL   string
	ExpiresAt     time.Time
	pinned        bool
	retireAt      time.Time
}

type resourceSessionKey struct{ user, item, server, original string }

func (e streamResourceEntry) sessionKey() resourceSessionKey {
	return resourceSessionKey{e.UserID, e.VirtualItemID, e.ServerID, e.OriginalID}
}

type resourceSession struct {
	expires   time.Time
	handles   map[string]bool
	byURL     map[string]string
	manifests map[string]map[string]bool
}
type streamResourceStore struct {
	mu            sync.Mutex
	entries       map[string]*streamResourceEntry
	sessions      map[resourceSessionKey]*resourceSession
	manifestCount int
	max           int
	now           func() time.Time
}

func newStreamResourceStore() *streamResourceStore {
	return &streamResourceStore{entries: map[string]*streamResourceEntry{}, sessions: map[resourceSessionKey]*resourceSession{}, max: maxStreamResourceEntries, now: time.Now}
}
func (s *streamResourceStore) removeLocked(id string) {
	e := s.entries[id]
	if e == nil {
		return
	}
	if session := s.sessions[e.sessionKey()]; session != nil {
		delete(session.handles, id)
		if session.byURL[e.ResourceURL] == id {
			delete(session.byURL, e.ResourceURL)
		}
	}
	delete(s.entries, id)
}
func (s *streamResourceStore) pruneLocked(now time.Time) {
	for key, session := range s.sessions {
		if !now.Before(session.expires) {
			for id := range session.handles {
				delete(s.entries, id)
			}
			s.manifestCount -= len(session.manifests)
			delete(s.sessions, key)
		}
	}
	for id, e := range s.entries {
		if !e.retireAt.IsZero() && !now.Before(e.retireAt) {
			s.removeLocked(id)
		}
	}
}

// registerAll reserves the entire batch or nothing. Identical URLs within a
// playback session reuse handles; different users/items/sources never do.
func (s *streamResourceStore) registerAll(entries []streamResourceEntry) ([]string, bool) {
	return s.registerManifest(entries, "")
}
func (s *streamResourceStore) registerManifest(entries []streamResourceEntry, rollingManifest string) ([]string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.pruneLocked(now)
	type resourceKey struct {
		session resourceSessionKey
		url     string
	}
	pending := map[resourceKey]bool{}
	needed := 0
	for _, e := range entries {
		key := resourceKey{e.sessionKey(), e.ResourceURL}
		if e.ResourceURL == "" {
			needed++
			continue
		}
		if session := s.sessions[key.session]; session != nil && session.byURL[key.url] != "" {
			continue
		}
		if !pending[key] {
			pending[key] = true
			needed++
		}
	}
	if len(s.entries)+needed > s.max {
		return nil, false
	}
	// Distinct query variants may reuse the same resource URL. Bound window
	// records too, before mutating either the handles or the existing windows.
	if rollingManifest != "" {
		newWindows := map[resourceSessionKey]bool{}
		for _, entry := range entries {
			key := entry.sessionKey()
			if session := s.sessions[key]; session == nil || session.manifests[rollingManifest] == nil {
				newWindows[key] = true
			}
		}
		if s.manifestCount+len(newWindows) > s.max {
			return nil, false
		}
	}
	ids := make([]string, len(entries))
	current := map[resourceSessionKey]map[string]bool{}
	for i, e := range entries {
		key := e.sessionKey()
		session := s.sessions[key]
		if session == nil {
			session = &resourceSession{handles: map[string]bool{}, byURL: map[string]string{}, manifests: map[string]map[string]bool{}}
			s.sessions[key] = session
		}
		id := session.byURL[e.ResourceURL]
		if e.ResourceURL == "" || id == "" {
			for {
				id = randomHex(16)
				if s.entries[id] == nil {
					break
				}
			}
			copy := e
			s.entries[id] = &copy
			session.handles[id] = true
			if e.ResourceURL != "" {
				session.byURL[e.ResourceURL] = id
			}
		}
		entry := s.entries[id]
		if rollingManifest == "" {
			entry.pinned = true
		}
		entry.retireAt = time.Time{}
		session.expires = now.Add(streamResourceIdleTTL)
		entry.ExpiresAt = session.expires
		ids[i] = id
		if current[key] == nil {
			current[key] = map[string]bool{}
		}
		current[key][id] = true
	}
	if rollingManifest != "" {
		// Replace only this playlist's window; sibling audio/video playlists retain
		// their own references. No window changes are committed on capacity failure.
		for key, members := range current {
			session := s.sessions[key]
			old := session.manifests[rollingManifest]
			if old == nil {
				s.manifestCount++
			}
			session.manifests[rollingManifest] = members
			for id := range old {
				e := s.entries[id]
				if e == nil || e.pinned || members[id] {
					continue
				}
				referenced := false
				for _, refs := range session.manifests {
					referenced = referenced || refs[id]
				}
				if !referenced {
					e.retireAt = now.Add(streamResourceCatchupGrace)
				}
			}
		}
	}
	return ids, true
}
func (s *streamResourceStore) put(e streamResourceEntry) (string, bool) {
	ids, ok := s.registerAll([]streamResourceEntry{e})
	if !ok {
		return "", false
	}
	return ids[0], true
}
func (s *streamResourceStore) lookupLocked(id string, renew bool) (streamResourceEntry, bool) {
	e := s.entries[id]
	if e == nil {
		return streamResourceEntry{}, false
	}
	now := s.now()
	session := s.sessions[e.sessionKey()]
	if session == nil || !now.Before(session.expires) || (!e.retireAt.IsZero() && !now.Before(e.retireAt)) {
		s.pruneLocked(now)
		return streamResourceEntry{}, false
	}
	if renew {
		session.expires = now.Add(streamResourceIdleTTL)
	}
	copy := *e
	copy.ExpiresAt = session.expires
	return copy, true
}

// peek never renews a lease: handlers must finish authorization first.
func (s *streamResourceStore) peek(id string) (streamResourceEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lookupLocked(id, false)
}
func (s *streamResourceStore) touch(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.lookupLocked(id, true)
	return ok
}

// get is the store-level authorized lookup used by lifecycle tests. HTTP
// handlers use peek + touch so rejected requests cannot extend any session.
func (s *streamResourceStore) get(id string) (streamResourceEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lookupLocked(id, true)
}
func (s *streamResourceStore) size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(s.now())
	return len(s.entries)
}
