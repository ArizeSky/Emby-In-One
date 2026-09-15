package backend

import (
	"sync"
	"time"
)

const playbackHeartbeatTimeout = 3 * time.Minute

type streamKey struct {
	UserID      string
	ServerIndex int
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
func (l *PlaybackLimiter) TryStart(userID string, serverIndex int, itemID string, maxConcurrent int) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	if maxConcurrent <= 0 {
		return true
	}

	key := streamKey{UserID: userID, ServerIndex: serverIndex}

	// Same user on same server: update rather than stack
	if existing, ok := l.streams[key]; ok {
		existing.ItemID = itemID
		existing.LastHeartbeat = time.Now()
		return true
	}

	// Count active streams for this server (exclude expired)
	now := time.Now()
	count := 0
	for k, entry := range l.streams {
		if k.ServerIndex == serverIndex && now.Sub(entry.LastHeartbeat) < playbackHeartbeatTimeout {
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
func (l *PlaybackLimiter) Heartbeat(userID string, serverIndex int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	key := streamKey{UserID: userID, ServerIndex: serverIndex}
	if entry, ok := l.streams[key]; ok {
		entry.LastHeartbeat = time.Now()
	}
}

// Stop removes a user's stream record on a server.
func (l *PlaybackLimiter) Stop(userID string, serverIndex int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	delete(l.streams, streamKey{UserID: userID, ServerIndex: serverIndex})
}

// CountForServer returns the number of active (non-expired) streams on a server.
func (l *PlaybackLimiter) CountForServer(serverIndex int) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	count := 0
	for k, entry := range l.streams {
		if k.ServerIndex == serverIndex && now.Sub(entry.LastHeartbeat) < playbackHeartbeatTimeout {
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
