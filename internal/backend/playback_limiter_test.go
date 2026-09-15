package backend

import (
	"testing"
	"time"
)

func TestPlaybackLimiterAllowsWithinLimit(t *testing.T) {
	l := NewPlaybackLimiter()
	if !l.TryStart("user1", 0, "item-a", 2) {
		t.Error("user1 should be allowed (limit=2, count=0)")
	}
	if !l.TryStart("user2", 0, "item-b", 2) {
		t.Error("user2 should be allowed (limit=2, count=1)")
	}
}

func TestPlaybackLimiterRejectsBeyondLimit(t *testing.T) {
	l := NewPlaybackLimiter()
	if !l.TryStart("user1", 0, "item-a", 1) {
		t.Error("user1 should be allowed")
	}
	if l.TryStart("user2", 0, "item-b", 1) {
		t.Error("user2 should be rejected (limit=1, count=1)")
	}
}

func TestPlaybackLimiterSameUserDoesNotStack(t *testing.T) {
	l := NewPlaybackLimiter()
	if !l.TryStart("user1", 0, "item-a", 1) {
		t.Error("user1 first start should be allowed")
	}
	// Same user, same server, different item → should update, not stack
	if !l.TryStart("user1", 0, "item-b", 1) {
		t.Error("user1 second start (same server) should update existing slot")
	}
	if l.CountForServer(0) != 1 {
		t.Errorf("CountForServer = %d, want 1", l.CountForServer(0))
	}
}

func TestPlaybackLimiterHeartbeatRefresh(t *testing.T) {
	l := NewPlaybackLimiter()
	l.TryStart("user1", 0, "item-a", 2)

	// Manually set old heartbeat
	l.mu.Lock()
	entry := l.streams[streamKey{UserID: "user1", ServerIndex: 0}]
	entry.LastHeartbeat = time.Now().Add(-2 * time.Minute)
	l.mu.Unlock()

	l.Heartbeat("user1", 0)

	l.mu.Lock()
	refreshed := l.streams[streamKey{UserID: "user1", ServerIndex: 0}]
	l.mu.Unlock()

	if time.Since(refreshed.LastHeartbeat) > time.Second {
		t.Error("Heartbeat should refresh to now")
	}
}

func TestPlaybackLimiterStopRemoves(t *testing.T) {
	l := NewPlaybackLimiter()
	l.TryStart("user1", 0, "item-a", 1)
	l.Stop("user1", 0)
	if !l.TryStart("user2", 0, "item-b", 1) {
		t.Error("user2 should be allowed after user1 stopped")
	}
}

func TestPlaybackLimiterExpiry(t *testing.T) {
	l := NewPlaybackLimiter()
	l.TryStart("user1", 0, "item-a", 1)

	// Manually set heartbeat to 4 minutes ago
	l.mu.Lock()
	l.streams[streamKey{UserID: "user1", ServerIndex: 0}].LastHeartbeat = time.Now().Add(-4 * time.Minute)
	l.mu.Unlock()

	l.Cleanup()

	if !l.TryStart("user2", 0, "item-b", 1) {
		t.Error("user2 should be allowed after expired cleanup")
	}
}

func TestPlaybackLimiterCountForServer(t *testing.T) {
	l := NewPlaybackLimiter()
	l.TryStart("user1", 0, "item-a", 10)
	l.TryStart("user2", 0, "item-b", 10)
	l.TryStart("user3", 1, "item-c", 10)

	if c := l.CountForServer(0); c != 2 {
		t.Errorf("CountForServer(0) = %d, want 2", c)
	}
	if c := l.CountForServer(1); c != 1 {
		t.Errorf("CountForServer(1) = %d, want 1", c)
	}
}

func TestPlaybackLimiterZeroMeansNoLimit(t *testing.T) {
	l := NewPlaybackLimiter()
	for i := 0; i < 100; i++ {
		if !l.TryStart("user"+string(rune('A'+i)), 0, "item", 0) {
			t.Fatalf("maxConcurrent=0 should mean no limit, failed at %d", i)
		}
	}
}

func TestPlaybackLimiterDifferentServers(t *testing.T) {
	l := NewPlaybackLimiter()
	// Server 0 full (limit 1)
	l.TryStart("user1", 0, "item-a", 1)
	// Server 1 should still accept
	if !l.TryStart("user2", 1, "item-b", 1) {
		t.Error("different server should have independent limit")
	}
}
