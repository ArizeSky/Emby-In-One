package backend

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func newTestHiddenLibraryStore(t *testing.T) (*HiddenLibraryStore, string) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := openSQLite(dbPath)
	if err != nil {
		t.Fatalf("openSQLite: %v", err)
	}
	_ = db.exec(`PRAGMA journal_mode = WAL`)
	store, err := NewHiddenLibraryStore(db, nil)
	if err != nil {
		t.Fatalf("NewHiddenLibraryStore: %v", err)
	}
	t.Cleanup(func() {
		_ = closeSQLite(db)
		_ = os.RemoveAll(dir)
	})
	return store, dbPath
}

func hiddenHas(store *HiddenLibraryStore, userID, serverID, libraryID string) bool {
	servers := store.HiddenForUser(userID)
	if servers == nil {
		return false
	}
	libraries, ok := servers[serverID]
	if !ok {
		return false
	}
	_, hidden := libraries[libraryID]
	return hidden
}

func TestHiddenLibraryStoreSetAndRead(t *testing.T) {
	store, _ := newTestHiddenLibraryStore(t)

	if err := store.SetServerHidden("user-1", "srv-a", []string{"lib-1", "lib-2"}); err != nil {
		t.Fatalf("SetServerHidden: %v", err)
	}
	if err := store.SetServerHidden("user-1", "srv-b", []string{"lib-3"}); err != nil {
		t.Fatalf("SetServerHidden: %v", err)
	}
	if err := store.SetServerHidden("user-2", "srv-a", []string{"lib-9"}); err != nil {
		t.Fatalf("SetServerHidden: %v", err)
	}

	for _, tc := range []struct{ userID, serverID, libraryID string }{
		{"user-1", "srv-a", "lib-1"},
		{"user-1", "srv-a", "lib-2"},
		{"user-1", "srv-b", "lib-3"},
		{"user-2", "srv-a", "lib-9"},
	} {
		if !hiddenHas(store, tc.userID, tc.serverID, tc.libraryID) {
			t.Errorf("expected %s/%s/%s to be hidden", tc.userID, tc.serverID, tc.libraryID)
		}
	}
	if hiddenHas(store, "user-1", "srv-a", "lib-3") {
		t.Errorf("lib-3 must not be hidden on srv-a for user-1")
	}
	if hiddenHas(store, "user-3", "srv-a", "lib-1") {
		t.Errorf("unknown user must have nothing hidden")
	}
}

func TestHiddenLibraryStorePatchClear(t *testing.T) {
	store, _ := newTestHiddenLibraryStore(t)

	if err := store.SetServerHidden("user-1", "srv-a", []string{"lib-1"}); err != nil {
		t.Fatalf("SetServerHidden: %v", err)
	}
	if err := store.SetServerHidden("user-1", "srv-a", []string{}); err != nil {
		t.Fatalf("SetServerHidden clear: %v", err)
	}
	if hidden := store.HiddenForUser("user-1"); hidden != nil && len(hidden["srv-a"]) > 0 {
		t.Fatalf("empty list must clear the pair, got %#v", hidden)
	}

	// Clearing one server must not touch the user's other servers.
	if err := store.SetServerHidden("user-1", "srv-b", []string{"lib-2"}); err != nil {
		t.Fatalf("SetServerHidden: %v", err)
	}
	if err := store.SetServerHidden("user-1", "srv-a", []string{"lib-1"}); err != nil {
		t.Fatalf("SetServerHidden: %v", err)
	}
	if err := store.SetServerHidden("user-1", "srv-a", nil); err != nil {
		t.Fatalf("SetServerHidden nil clear: %v", err)
	}
	if !hiddenHas(store, "user-1", "srv-b", "lib-2") {
		t.Fatalf("clearing srv-a must keep srv-b")
	}
}

func TestHiddenLibraryStorePersistsAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := openSQLite(dbPath)
	if err != nil {
		t.Fatalf("openSQLite: %v", err)
	}
	store, err := NewHiddenLibraryStore(db, nil)
	if err != nil {
		t.Fatalf("NewHiddenLibraryStore: %v", err)
	}
	if err := store.SetServerHidden(adminVisibilityUserID, "srv-a", []string{"lib-1"}); err != nil {
		t.Fatalf("SetServerHidden: %v", err)
	}
	if err := closeSQLite(db); err != nil {
		t.Fatalf("closeSQLite: %v", err)
	}

	db2, err := openSQLite(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = closeSQLite(db2) }()
	reopened, err := NewHiddenLibraryStore(db2, nil)
	if err != nil {
		t.Fatalf("NewHiddenLibraryStore reopen: %v", err)
	}
	if !hiddenHas(reopened, adminVisibilityUserID, "srv-a", "lib-1") {
		t.Fatalf("hidden config must survive a reopen")
	}
}

func TestHiddenLibraryStoreRemoveServer(t *testing.T) {
	store, _ := newTestHiddenLibraryStore(t)

	for _, tc := range []struct{ userID, serverID string }{
		{"user-1", "srv-a"}, {"user-1", "srv-b"}, {"user-2", "srv-a"},
	} {
		if err := store.SetServerHidden(tc.userID, tc.serverID, []string{"lib-1"}); err != nil {
			t.Fatalf("SetServerHidden: %v", err)
		}
	}
	if err := store.RemoveServer("srv-a"); err != nil {
		t.Fatalf("RemoveServer: %v", err)
	}
	if hiddenHas(store, "user-1", "srv-a", "lib-1") || hiddenHas(store, "user-2", "srv-a", "lib-1") {
		t.Fatalf("srv-a records must be gone for every user")
	}
	if !hiddenHas(store, "user-1", "srv-b", "lib-1") {
		t.Fatalf("srv-b records must survive")
	}
}

func TestHiddenLibraryStoreRemoveUser(t *testing.T) {
	store, _ := newTestHiddenLibraryStore(t)

	if err := store.SetServerHidden("user-1", "srv-a", []string{"lib-1"}); err != nil {
		t.Fatalf("SetServerHidden: %v", err)
	}
	if err := store.SetServerHidden("user-2", "srv-a", []string{"lib-2"}); err != nil {
		t.Fatalf("SetServerHidden: %v", err)
	}
	if err := store.RemoveUser("user-1"); err != nil {
		t.Fatalf("RemoveUser: %v", err)
	}
	if hiddenHas(store, "user-1", "srv-a", "lib-1") {
		t.Fatalf("user-1 records must be gone")
	}
	if !hiddenHas(store, "user-2", "srv-a", "lib-2") {
		t.Fatalf("user-2 records must survive")
	}
}

func TestHiddenLibraryStorePruneUserServers(t *testing.T) {
	store, _ := newTestHiddenLibraryStore(t)

	if err := store.SetServerHidden("user-1", "srv-a", []string{"lib-1"}); err != nil {
		t.Fatalf("SetServerHidden: %v", err)
	}
	if err := store.SetServerHidden("user-1", "srv-b", []string{"lib-2"}); err != nil {
		t.Fatalf("SetServerHidden: %v", err)
	}
	keep := func(serverID string) bool { return serverID == "srv-a" }
	if err := store.PruneUserServers("user-1", keep); err != nil {
		t.Fatalf("PruneUserServers: %v", err)
	}
	if !hiddenHas(store, "user-1", "srv-a", "lib-1") {
		t.Fatalf("srv-a must be kept")
	}
	if hiddenHas(store, "user-1", "srv-b", "lib-2") {
		t.Fatalf("srv-b must be pruned")
	}

	// A keep-all predicate must be a no-op, mirroring the guard callers apply
	// for an empty AllowedServers ("all servers").
	if err := store.SetServerHidden("user-1", "srv-b", []string{"lib-2"}); err != nil {
		t.Fatalf("SetServerHidden: %v", err)
	}
	if err := store.PruneUserServers("user-1", func(string) bool { return true }); err != nil {
		t.Fatalf("PruneUserServers keep-all: %v", err)
	}
	if !hiddenHas(store, "user-1", "srv-a", "lib-1") || !hiddenHas(store, "user-1", "srv-b", "lib-2") {
		t.Fatalf("keep-all must preserve everything")
	}
}

func TestVisibilityUserIDAdminMapping(t *testing.T) {
	if got := visibilityUserID(nil); got != "" {
		t.Errorf("nil token maps to empty, got %q", got)
	}
	if got := visibilityUserID(&tokenInfo{UserID: "u-1", Role: "user"}); got != "u-1" {
		t.Errorf("user token maps to its ID, got %q", got)
	}
	if got := visibilityUserID(&tokenInfo{UserID: "proxy-legacy", Role: "admin"}); got != adminVisibilityUserID {
		t.Errorf("admin token maps to the reserved constant, got %q", got)
	}
}

func TestHiddenLibraryStoreUserHiddenJSON(t *testing.T) {
	store, _ := newTestHiddenLibraryStore(t)

	if err := store.SetServerHidden("user-1", "srv-a", []string{"lib-2", "lib-1"}); err != nil {
		t.Fatalf("SetServerHidden: %v", err)
	}
	got := store.UserHiddenJSON("user-1")
	ids, ok := got["srv-a"]
	if !ok || len(ids) != 2 || ids[0] != "lib-1" || ids[1] != "lib-2" {
		t.Fatalf("expected sorted [lib-1 lib-2], got %#v", got)
	}
	if empty := store.UserHiddenJSON("nobody"); len(empty) != 0 {
		t.Fatalf("unknown user yields empty map, got %#v", empty)
	}
}

func TestHiddenLibraryStoreConcurrentReadWrite(t *testing.T) {
	store, _ := newTestHiddenLibraryStore(t)

	if err := store.SetServerHidden("victim", "srv-a", []string{"lib-init"}); err != nil {
		t.Fatalf("SetServerHidden: %v", err)
	}

	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			userID := "user-" + string(rune('0'+worker))
			for i := 0; i < 50; i++ {
				if err := store.SetServerHidden(userID, "srv-a", []string{"lib-" + string(rune('0'+i%10))}); err != nil {
					t.Errorf("SetServerHidden: %v", err)
					return
				}
			}
		}(w)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			servers := store.HiddenForUser("victim")
			if servers == nil {
				continue
			}
			// Readers only observe published snapshots: every inner map must
			// be intact, never half-mutated.
			for _, libraries := range servers {
				if libraries == nil {
					t.Errorf("nil library set observed mid-write")
					return
				}
			}
		}
	}()
	wg.Wait()

	if !hiddenHas(store, "victim", "srv-a", "lib-init") {
		t.Fatalf("writer workers must not clobber other users' entries")
	}
}
