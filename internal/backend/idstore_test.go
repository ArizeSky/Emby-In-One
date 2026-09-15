package backend

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestIDStorePersistsAdditionalInstances(t *testing.T) {
	dir := t.TempDir()
	logger := NewLogger(LogConfig{Level: "error", FileLevel: "error", DataDir: dir})
	t.Cleanup(func() { _ = logger.Close() })

	store1, err := NewIDStore(dir, logger)
	if err != nil {
		t.Fatalf("create store1: %v", err)
	}
	virtualID := store1.GetOrCreateVirtualID("series-a", 0)
	store1.AssociateAdditionalInstance(virtualID, "series-b", 1)
	_ = store1.Close()

	store2, err := NewIDStore(dir, logger)
	if err != nil {
		t.Fatalf("create store2: %v", err)
	}
	t.Cleanup(func() { _ = store2.Close() })

	resolved := store2.ResolveVirtualID(virtualID)
	if resolved == nil || len(resolved.OtherInstances) != 1 {
		t.Fatalf("resolved = %#v, want one additional instance", resolved)
	}
	if resolved.OtherInstances[0].OriginalID != "series-b" || resolved.OtherInstances[0].ServerIndex != 1 {
		t.Fatalf("additional instance = %#v", resolved.OtherInstances[0])
	}

	if _, err := filepath.Abs(filepath.Join(dir, "mappings.db")); err != nil {
		t.Fatalf("db path err: %v", err)
	}
}

func TestIDStoreUpdatesAdditionalInstancesOnShiftAndDelete(t *testing.T) {
	dir := t.TempDir()
	logger := NewLogger(LogConfig{Level: "error", FileLevel: "error", DataDir: dir})
	t.Cleanup(func() { _ = logger.Close() })

	store, err := NewIDStore(dir, logger)
	if err != nil {
		t.Fatalf("create store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	virtualID := store.GetOrCreateVirtualID("series-a", 0)
	store.AssociateAdditionalInstance(virtualID, "series-c", 2)
	store.ShiftServerIndices(1)

	resolved := store.ResolveVirtualID(virtualID)
	if resolved.OtherInstances[0].ServerIndex != 1 {
		t.Fatalf("shifted server index = %d, want 1", resolved.OtherInstances[0].ServerIndex)
	}

	store.RemoveByServerIndex(1)
	resolved = store.ResolveVirtualID(virtualID)
	if len(resolved.OtherInstances) != 0 {
		t.Fatalf("other instances = %#v, want empty", resolved.OtherInstances)
	}
}

// TestDatabaseFileUsesPrivateMode keeps the state database owner-only: it holds user
// password hashes and per-user watch history, and sqlite creates it with the umask.
func TestDatabaseFileUsesPrivateMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file mode assertion is not reliable on Windows")
	}
	dir := t.TempDir()
	store, err := NewIDStore(dir, nil)
	if err != nil {
		t.Fatalf("NewIDStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	info, err := os.Stat(filepath.Join(dir, "mappings.db"))
	if err != nil {
		t.Fatalf("stat mappings.db: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mappings.db mode = %#o, want 0600", got)
	}
}

func TestEvictExpiredStreamState_CleansActiveStreamServer(t *testing.T) {
	store, _ := NewIDStore("", nil)
	defer store.Close()

	store.SetActiveStream("vid-1", 0)
	store.SetActiveStream("vid-2", 1)

	// Manually expire one entry
	store.mu.Lock()
	store.activeStreamServer["vid-1"] = activeStreamEntry{
		ServerIndex: 0,
		CreatedAt:   time.Now().Add(-5 * time.Hour),
	}
	store.mu.Unlock()

	store.evictExpiredStreamState()

	if _, ok := store.GetActiveStream("vid-1"); ok {
		t.Error("expired activeStream entry was not evicted")
	}
	if _, ok := store.GetActiveStream("vid-2"); !ok {
		t.Error("non-expired activeStream entry was incorrectly evicted")
	}
}
