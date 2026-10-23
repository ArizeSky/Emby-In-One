package backend

import (
	"sort"
	"sync"
	"time"
)

// maxSessionWatchEntries bounds the number of concurrently tracked playback
// sessions. The registry only exists to tell a superseded session apart from
// the live one, so a small bounded set is enough; per design S9 it must never
// become unbounded memory.
const maxSessionWatchEntries = 1024

// sessionWatchEventTTL expires tracked sessions whose last event is older
// than this. Among other things it bounds the damage of a client that never
// sends Stopped: its identity stops filtering some minutes after the last
// progress report.
const sessionWatchEventTTL = 10 * time.Minute

// sessionWatchKey identifies the stream a session event belongs to: one proxy
// user playing one (virtual) item.
type sessionWatchKey struct {
	UserID string
	ItemID string
}

type sessionWatchState struct {
	SessionID string
	LastSeen  time.Time
}

// sessionEventTracker remembers, per (user, item), which playback session is
// currently live. A Sessions/Playing start for the same user+item replaces
// the tracked session, so a late progress/stop event that still carries the
// old identity is detected and not written to the local watch store. The
// tracker orders events only by session identity: it never interprets
// position values or arrival times as event ordering (no monotonic
// conditions), and events without any session identity keep plain
// receive-order semantics.
//
// State is intentionally in-memory only: across a restart the proxy knows no
// session, the first event with a session identity is accepted and tracked,
// and nothing is persisted (cross-restart ordering would need a defined
// persistent sequence field first).
type sessionEventTracker struct {
	mu      sync.Mutex
	entries map[sessionWatchKey]sessionWatchState
	now     func() time.Time // replaceable in tests
}

func newSessionEventTracker() *sessionEventTracker {
	return &sessionEventTracker{entries: map[sessionWatchKey]sessionWatchState{}, now: time.Now}
}

func (t *sessionEventTracker) currentTimeLocked() time.Time {
	if t.now == nil {
		return time.Now()
	}
	return t.now()
}

// evictLocked drops expired entries, then enforces the capacity bound by
// dropping the least recently seen ones first.
func (t *sessionEventTracker) evictLocked(now time.Time) {
	for key, st := range t.entries {
		if now.Sub(st.LastSeen) >= sessionWatchEventTTL {
			delete(t.entries, key)
		}
	}
	if len(t.entries) < maxSessionWatchEntries {
		return
	}
	type entryRef struct {
		key   sessionWatchKey
		state sessionWatchState
	}
	refs := make([]entryRef, 0, len(t.entries))
	for key, st := range t.entries {
		refs = append(refs, entryRef{key: key, state: st})
	}
	sort.Slice(refs, func(i, j int) bool {
		return refs[i].state.LastSeen.Before(refs[j].state.LastSeen)
	})
	// Keep room for the entry the caller is about to insert.
	for i := 0; len(t.entries) >= maxSessionWatchEntries && i < len(refs); i++ {
		delete(t.entries, refs[i].key)
	}
}

// StartSession records the beginning of a playback session for user+item,
// invalidating any older session still tracked for that stream. An empty
// sessionID offers no identity, so nothing is invalidated and the event keeps
// receive-order semantics.
func (t *sessionEventTracker) StartSession(userID, itemID, sessionID string) {
	if t == nil || sessionID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.entries == nil {
		t.entries = map[sessionWatchKey]sessionWatchState{}
	}
	now := t.currentTimeLocked()
	t.evictLocked(now)
	t.entries[sessionWatchKey{UserID: userID, ItemID: itemID}] = sessionWatchState{SessionID: sessionID, LastSeen: now}
}

// AcceptEvent reports whether a progress/stop event carrying sessionID may
// still update the local watch state for user+item. It returns true when
// either side has no session identity (no order information available) or the
// identity matches the session currently tracked for that stream; a different
// identity means the event belongs to a session that a newer start has
// superseded. Accepting an event with no tracked session registers it as the
// live one from now on.
func (t *sessionEventTracker) AcceptEvent(userID, itemID, sessionID string) bool {
	if t == nil || sessionID == "" {
		return true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.entries == nil {
		t.entries = map[sessionWatchKey]sessionWatchState{}
	}
	now := t.currentTimeLocked()
	t.evictLocked(now)
	key := sessionWatchKey{UserID: userID, ItemID: itemID}
	cur, ok := t.entries[key]
	if !ok {
		t.entries[key] = sessionWatchState{SessionID: sessionID, LastSeen: now}
		return true
	}
	if cur.SessionID != sessionID {
		return false
	}
	t.entries[key] = sessionWatchState{SessionID: sessionID, LastSeen: now}
	return true
}
