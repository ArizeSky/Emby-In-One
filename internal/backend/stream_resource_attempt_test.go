package backend

import "testing"

func TestStreamResourceAttemptDedupIsolation(t *testing.T) {
	store := newStreamResourceStore()
	base := streamResourceEntry{UserID: "owner", VirtualItemID: "item", ServerID: "server", OriginalID: "original", TimelineSession: "reused-session", ResourceURL: "https://fixture.invalid/reused", AttemptID: "attempt-A"}
	a, ok := store.registerManifest([]streamResourceEntry{base, base}, "window")
	if !ok || a[0] != a[1] {
		t.Fatal("same attempt must deduplicate")
	}
	base.AttemptID = "attempt-B"
	b, ok := store.registerManifest([]streamResourceEntry{base}, "window")
	if !ok || a[0] == b[0] {
		t.Fatal("reused upstream session and URL crossed attempt boundary")
	}
}
