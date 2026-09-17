package backend

import (
	"sync"
	"time"
)

const playbackHeartbeatTimeout = 3 * time.Minute

type streamKey struct {
	UserID   string
	ServerID string
}

type streamEntry struct {
	ItemID        string
	LastHeartbeat time.Time
}

type PlaybackLimiter struct {
	mu      sync.Mutex
	streams map[streamKey]*streamEntry
}

func NewPlaybackLimiter() *PlaybackLimiter {
	return &PlaybackLimiter{
		streams: make(map[streamKey]*streamEntry),
	}
}

// TryStart attempts to register a playback stream. Returns true if allowed.
// maxConcurrent <= 0 means no limit. Same user on same server updates rather than stacking.
func (l *PlaybackLimiter) TryStart(userID string, serverID string, itemID string, maxConcurrent int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	if maxConcurrent <= 0 {
		return true
	}

	key := streamKey{UserID: userID, ServerID: serverID}

	// Same user on same server: update rather than stack — but only while the entry
	// is still live. Cleanup runs every 30 minutes, so an entry whose heartbeat has
	// already expired can still be sitting in the map. Reusing it here would return
	// true before the capacity check below and hand the caller a fresh heartbeat for
	// free, while the stale entry was excluded from the count. Drop it and let the
	// request be evaluated as a new stream instead.
	if existing, ok := l.streams[key]; ok {
		if time.Since(existing.LastHeartbeat) >= playbackHeartbeatTimeout {
			delete(l.streams, key)
		} else {
			existing.ItemID = itemID
			existing.LastHeartbeat = time.Now()
			return true
		}
	}

	// Count active streams for this server (exclude expired)
	now := time.Now()
	count := 0
	for k, entry := range l.streams {
		if k.ServerID == serverID && now.Sub(entry.LastHeartbeat) < playbackHeartbeatTimeout {
			count++
		}
	}

	if count >= maxConcurrent {
		return false
	}

	l.streams[key] = &streamEntry{
		ItemID:        itemID,
		LastHeartbeat: now,
	}
	return true
}

// Heartbeat refreshes the last heartbeat time for a user's stream on a server.
func (l *PlaybackLimiter) Heartbeat(userID string, serverID string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	key := streamKey{UserID: userID, ServerID: serverID}
	if entry, ok := l.streams[key]; ok {
		entry.LastHeartbeat = time.Now()
	}
}

// Stop removes a user's stream record on a server.
func (l *PlaybackLimiter) Stop(userID string, serverID string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	delete(l.streams, streamKey{UserID: userID, ServerID: serverID})
}

// CountForServer returns the number of active (non-expired) streams on a server.
func (l *PlaybackLimiter) CountForServer(serverID string) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	count := 0
	for k, entry := range l.streams {
		if k.ServerID == serverID && now.Sub(entry.LastHeartbeat) < playbackHeartbeatTimeout {
			count++
		}
	}
	return count
}

// Cleanup removes all expired stream entries.
func (l *PlaybackLimiter) Cleanup() {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	for key, entry := range l.streams {
		if now.Sub(entry.LastHeartbeat) >= playbackHeartbeatTimeout {
			delete(l.streams, key)
		}
	}
}
