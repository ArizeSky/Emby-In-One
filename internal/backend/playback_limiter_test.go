package backend

import (
	"testing"
	"time"
)

func TestPlaybackLimiterAllowsWithinLimit(t *testing.T) {
	l := NewPlaybackLimiter()
	if !l.TryStart("user1", "srv-0", "item-a", 2) {
		t.Error("user1 should be allowed (limit=2, count=0)")
	}
	if !l.TryStart("user2", "srv-0", "item-b", 2) {
		t.Error("user2 should be allowed (limit=2, count=1)")
	}
}

func TestPlaybackLimiterRejectsBeyondLimit(t *testing.T) {
	l := NewPlaybackLimiter()
	if !l.TryStart("user1", "srv-0", "item-a", 1) {
		t.Error("user1 should be allowed")
	}
	if l.TryStart("user2", "srv-0", "item-b", 1) {
		t.Error("user2 should be rejected (limit=1, count=1)")
	}
}

func TestPlaybackLimiterSameUserDoesNotStack(t *testing.T) {
	l := NewPlaybackLimiter()
	if !l.TryStart("user1", "srv-0", "item-a", 1) {
		t.Error("user1 first start should be allowed")
	}
	// Same user, same server, different item → should update, not stack
	if !l.TryStart("user1", "srv-0", "item-b", 1) {
		t.Error("user1 second start (same server) should update existing slot")
	}
	if l.CountForServer("srv-0") != 1 {
		t.Errorf("CountForServer = %d, want 1", l.CountForServer("srv-0"))
	}
}

func TestPlaybackLimiterHeartbeatRefresh(t *testing.T) {
	l := NewPlaybackLimiter()
	l.TryStart("user1", "srv-0", "item-a", 2)

	// Manually set old heartbeat
	l.mu.Lock()
	entry := l.streams[streamKey{UserID: "user1", ServerID: "srv-0"}]
	entry.LastHeartbeat = time.Now().Add(-2 * time.Minute)
	l.mu.Unlock()

	l.Heartbeat("user1", "srv-0")

	l.mu.Lock()
	refreshed := l.streams[streamKey{UserID: "user1", ServerID: "srv-0"}]
	l.mu.Unlock()

	if time.Since(refreshed.LastHeartbeat) > time.Second {
		t.Error("Heartbeat should refresh to now")
	}
}

func TestPlaybackLimiterStopRemoves(t *testing.T) {
	l := NewPlaybackLimiter()
	l.TryStart("user1", "srv-0", "item-a", 1)
	l.Stop("user1", "srv-0")
	if !l.TryStart("user2", "srv-0", "item-b", 1) {
		t.Error("user2 should be allowed after user1 stopped")
	}
}

func TestPlaybackLimiterExpiry(t *testing.T) {
	l := NewPlaybackLimiter()
	l.TryStart("user1", "srv-0", "item-a", 1)

	// Manually set heartbeat to 4 minutes ago
	l.mu.Lock()
	l.streams[streamKey{UserID: "user1", ServerID: "srv-0"}].LastHeartbeat = time.Now().Add(-4 * time.Minute)
	l.mu.Unlock()

	l.Cleanup()

	if !l.TryStart("user2", "srv-0", "item-b", 1) {
		t.Error("user2 should be allowed after expired cleanup")
	}
}

func TestPlaybackLimiterCountForServer(t *testing.T) {
	l := NewPlaybackLimiter()
	l.TryStart("user1", "srv-0", "item-a", 10)
	l.TryStart("user2", "srv-0", "item-b", 10)
	l.TryStart("user3", "srv-1", "item-c", 10)

	if c := l.CountForServer("srv-0"); c != 2 {
		t.Errorf("CountForServer(0) = %d, want 2", c)
	}
	if c := l.CountForServer("srv-1"); c != 1 {
		t.Errorf("CountForServer(1) = %d, want 1", c)
	}
}

func TestPlaybackLimiterZeroMeansNoLimit(t *testing.T) {
	l := NewPlaybackLimiter()
	for i := 0; i < 100; i++ {
		if !l.TryStart("user"+string(rune('A'+i)), "srv-0", "item", 0) {
			t.Fatalf("maxConcurrent=0 should mean no limit, failed at %d", i)
		}
	}
}

func TestPlaybackLimiterDifferentServers(t *testing.T) {
	l := NewPlaybackLimiter()
	// Server 0 full (limit 1)
	l.TryStart("user1", "srv-0", "item-a", 1)
	// Server 1 should still accept
	if !l.TryStart("user2", "srv-1", "item-b", 1) {
		t.Error("different server should have independent limit")
	}
}

// FIX-03, the benign half: with capacity available, a user whose entry expired must be
// able to start again. Cleanup only runs every 30 minutes, so between the expiry and
// the sweep the stale entry is still in the map, and the owner must have it replaced
// rather than refreshed in place — see
// TestPlaybackLimiterExpiredEntriesDoNotWedgeCapacity for the case where the two
// behaviours actually diverge.
func TestPlaybackLimiterExpiredEntryIsReclaimed(t *testing.T) {
	l := NewPlaybackLimiter()
	if !l.TryStart("user-a", "srv-0", "item-a", 1) {
		t.Fatal("user-a first start should be allowed")
	}
	// Age user-a's entry past the heartbeat timeout without running Cleanup.
	l.mu.Lock()
	l.streams[streamKey{UserID: "user-a", ServerID: "srv-0"}].LastHeartbeat = time.Now().Add(-playbackHeartbeatTimeout - time.Minute)
	l.mu.Unlock()

	if got := l.CountForServer("srv-0"); got != 0 {
		t.Fatalf("CountForServer(0) = %d, want 0 (stale entries are not active streams)", got)
	}

	// user-a restarts on the same server. The stale entry must be dropped and replaced
	// rather than refreshed in place, so the stream is genuinely counted again.
	if !l.TryStart("user-a", "srv-0", "item-a2", 1) {
		t.Fatal("user-a's stale entry should be dropped and the new stream allowed (live count was 0)")
	}
	l.mu.Lock()
	entry := l.streams[streamKey{UserID: "user-a", ServerID: "srv-0"}]
	l.mu.Unlock()
	if entry == nil {
		t.Fatal("user-a's stream entry missing after TryStart")
	}
	if entry.ItemID != "item-a2" {
		t.Errorf("entry ItemID = %q, want the new item %q", entry.ItemID, "item-a2")
	}
	if time.Since(entry.LastHeartbeat) > time.Second {
		t.Error("replaced entry should carry a fresh heartbeat, not the stale timestamp")
	}
	if got := l.CountForServer("srv-0"); got != 1 {
		t.Fatalf("CountForServer(0) = %d, want 1 once the slot is genuinely in use", got)
	}

	// Capacity is now honestly full, so another user cannot slip in.
	if l.TryStart("user-b", "srv-0", "item-b", 1) {
		t.Error("user-b must be rejected: user-a holds the only slot with a live heartbeat")
	}
	if got := l.CountForServer("srv-0"); got != 1 {
		t.Errorf("CountForServer(0) = %d, want 1 after the rejected attempt", got)
	}
}

// Counting already skips stale entries, so the number itself was never wrong — the
// defect was that TryStart never consulted it for a user who already had an entry. The
// consequence is not exceeding maxConcurrent but an accounting split from reality: a
// stale entry still occupies a key in the map and is skipped by the count, so a server
// with spare capacity refuses the users who need it. Here the only live stream goes
// away and both users are still turned down for a server that has nobody watching it.
// (The stale entry is only ever reached by its own user, so the bypass cannot push a
// server past its limit; it wedges capacity that is actually free.)
func TestPlaybackLimiterExpiredEntriesDoNotWedgeCapacity(t *testing.T) {
	l := NewPlaybackLimiter()
	if !l.TryStart("user-a", "srv-0", "item-a", 1) {
		t.Fatal("user-a first start should be allowed")
	}
	if l.TryStart("user-b", "srv-0", "item-b", 1) {
		t.Fatal("user-b must be rejected: the limit is 1 and user-a holds it")
	}

	// user-a stops watching, but its entry stays behind until the 30-minute Cleanup
	// sweep and both users now hold an expired entry for the same server.
	l.Stop("user-a", "srv-0")
	now := time.Now()
	for _, user := range []string{"user-a", "user-b"} {
		l.mu.Lock()
		l.streams[streamKey{UserID: user, ServerID: "srv-0"}] = &streamEntry{
			ItemID:        "stale",
			LastHeartbeat: now.Add(-playbackHeartbeatTimeout - time.Minute),
		}
		l.mu.Unlock()
	}

	if got := l.CountForServer("srv-0"); got != 0 {
		t.Fatalf("CountForServer(0) = %d, want 0: nobody is watching", got)
	}

	// With the whole server free, somebody must be able to start watching. Before the
	// fix both users hit the "same user, update rather than stack" branch, were handed
	// a fresh heartbeat, and left the stale entries of the *other* user in place — the
	// server reported 0 active streams while refusing every request.
	if !l.TryStart("user-a", "srv-0", "item-a2", 1) {
		t.Fatalf("user-a must be allowed: CountForServer reports %d, the server is free", l.CountForServer("srv-0"))
	}
	if got := l.CountForServer("srv-0"); got != 1 {
		t.Fatalf("CountForServer(0) = %d, want 1 after user-a reclaimed the free slot", got)
	}
	// The reclaimed slot is genuinely counted, so it now blocks the next user.
	if l.TryStart("user-b", "srv-0", "item-b2", 1) {
		t.Error("user-b must be rejected: user-a reclaimed the only slot with a live heartbeat")
	}
	if got := l.CountForServer("srv-0"); got != 1 {
		t.Errorf("CountForServer(0) = %d, want 1 after the rejected attempt", got)
	}
}
