package backend

import (
	"os"
	"path/filepath"
	"testing"
)

func newTestUserStore(t *testing.T) *UserStore {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := openSQLite(dbPath)
	if err != nil {
		t.Fatalf("openSQLite: %v", err)
	}
	_ = db.exec(`PRAGMA journal_mode = WAL`)
	store, err := NewUserStore(db, nil)
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}
	t.Cleanup(func() {
		_ = closeSQLite(db)
		_ = os.RemoveAll(dir)
	})
	return store
}

func TestUserStoreCreateAndAuthenticate(t *testing.T) {
	store := newTestUserStore(t)

	user, err := store.Create("alice", "password123", []string{"srv-0", "srv-1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if user.Username != "alice" {
		t.Errorf("username = %q, want alice", user.Username)
	}
	if len(user.ID) != 32 {
		t.Errorf("ID length = %d, want 32", len(user.ID))
	}

	// Correct password
	authed := store.Authenticate("alice", "password123")
	if authed == nil {
		t.Fatal("Authenticate with correct password returned nil")
	}
	if authed.ID != user.ID {
		t.Errorf("authed.ID = %s, want %s", authed.ID, user.ID)
	}

	// Wrong password
	if store.Authenticate("alice", "wrong") != nil {
		t.Error("Authenticate with wrong password should return nil")
	}

	// Case-insensitive username
	if store.Authenticate("Alice", "password123") == nil {
		t.Error("Authenticate should be case-insensitive")
	}

	// Duplicate username
	_, err = store.Create("alice", "other", nil)
	if err == nil {
		t.Error("Create duplicate username should fail")
	}

	// Case-insensitive duplicate
	_, err = store.Create("ALICE", "other", nil)
	if err == nil {
		t.Error("Create case-insensitive duplicate should fail")
	}
}

func TestUserStoreAllowedServers(t *testing.T) {
	store := newTestUserStore(t)

	user, err := store.Create("bob", "pass", []string{"srv-0", "srv-2"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(user.AllowedServers) != 2 || user.AllowedServers[0] != "srv-0" || user.AllowedServers[1] != "srv-2" {
		t.Errorf("AllowedServers = %v, want [srv-0 srv-2]", user.AllowedServers)
	}

	// Update to only server 1
	newServers := []string{"srv-1"}
	if err := store.Update(user.ID, nil, nil, nil, &newServers); err != nil {
		t.Fatalf("Update: %v", err)
	}
	got := store.Get(user.ID)
	if len(got.AllowedServers) != 1 || got.AllowedServers[0] != "srv-1" {
		t.Errorf("after update AllowedServers = %v, want [srv-1]", got.AllowedServers)
	}
}

func TestUserStoreDeleteCascade(t *testing.T) {
	store := newTestUserStore(t)

	user, err := store.Create("charlie", "pass", []string{"srv-0", "srv-1", "srv-2"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := store.Delete(user.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if store.Get(user.ID) != nil {
		t.Error("Get after delete should return nil")
	}
	if store.GetByUsername("charlie") != nil {
		t.Error("GetByUsername after delete should return nil")
	}
	if store.Authenticate("charlie", "pass") != nil {
		t.Error("Authenticate after delete should return nil")
	}
}

func TestUserStoreDisableUser(t *testing.T) {
	store := newTestUserStore(t)

	user, err := store.Create("dave", "pass", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Disable
	disabled := false
	if err := store.Update(user.ID, nil, nil, &disabled, nil); err != nil {
		t.Fatalf("Update disable: %v", err)
	}
	if store.Authenticate("dave", "pass") != nil {
		t.Error("Authenticate disabled user should return nil")
	}

	// Enable
	enabled := true
	if err := store.Update(user.ID, nil, nil, &enabled, nil); err != nil {
		t.Fatalf("Update enable: %v", err)
	}
	if store.Authenticate("dave", "pass") == nil {
		t.Error("Authenticate re-enabled user should succeed")
	}
}

func TestUserStoreRemoveServerGrants(t *testing.T) {
	store := newTestUserStore(t)

	user, err := store.Create("eve", "pass", []string{"srv-0", "srv-1", "srv-2"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Delete server "srv-1" → should become ["srv-0", "srv-2"]
	if err := store.RemoveServerGrants("srv-1"); err != nil {
		t.Fatalf("RemoveServerGrants: %v", err)
	}

	got := store.Get(user.ID)
	if len(got.AllowedServers) != 2 {
		t.Fatalf("after remove AllowedServers length = %d, want 2", len(got.AllowedServers))
	}
	if got.AllowedServers[0] != "srv-0" || got.AllowedServers[1] != "srv-2" {
		t.Errorf("after remove AllowedServers = %v, want [srv-0 srv-2]", got.AllowedServers)
	}
}

func TestUserStoreServerIDStable(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := openSQLite(dbPath)
	if err != nil {
		t.Fatalf("openSQLite: %v", err)
	}
	_ = db.exec(`PRAGMA journal_mode = WAL`)
	store, err := NewUserStore(db, nil)
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}

	carol, err := store.Create("carol", "pass", []string{"srv-0", "srv-2"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	unrestricted, err := store.Create("dave", "pass", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got := store.Get(carol.ID)
	if len(got.AllowedServers) != 2 || got.AllowedServers[0] != "srv-0" || got.AllowedServers[1] != "srv-2" {
		t.Fatalf("AllowedServers = %v, want [srv-0 srv-2]", got.AllowedServers)
	}
	if dave := store.Get(unrestricted.ID); dave.AllowedServers != nil {
		t.Fatalf("a user with no restrictions gained %v", dave.AllowedServers)
	}

	_ = closeSQLite(db)
	db2, err := openSQLite(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer closeSQLite(db2)
	_ = db2.exec(`PRAGMA journal_mode = WAL`)
	store2, err := NewUserStore(db2, nil)
	if err != nil {
		t.Fatalf("NewUserStore reopen: %v", err)
	}
	reloaded := store2.Get(carol.ID)
	if reloaded == nil {
		t.Fatal("carol disappeared after reopen")
	}
	if len(reloaded.AllowedServers) != 2 || reloaded.AllowedServers[0] != "srv-0" || reloaded.AllowedServers[1] != "srv-2" {
		t.Fatalf("stored AllowedServers = %v, want [srv-0 srv-2]", reloaded.AllowedServers)
	}
}

func TestUserStoreList(t *testing.T) {
	store := newTestUserStore(t)

	_, _ = store.Create("user1", "pass", nil)
	_, _ = store.Create("user2", "pass", []string{"srv-0"})
	_, _ = store.Create("user3", "pass", []string{"srv-0", "srv-1"})

	list := store.List()
	if len(list) != 3 {
		t.Errorf("List length = %d, want 3", len(list))
	}
}

func TestUserStorePersistence(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := openSQLite(dbPath)
	if err != nil {
		t.Fatalf("openSQLite: %v", err)
	}
	_ = db.exec(`PRAGMA journal_mode = WAL`)
	store, err := NewUserStore(db, nil)
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}

	user, err := store.Create("persist_user", "pass123", []string{"srv-0", "srv-2"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	savedID := user.ID
	_ = closeSQLite(db)

	// Reopen
	db2, err := openSQLite(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer closeSQLite(db2)
	_ = db2.exec(`PRAGMA journal_mode = WAL`)
	store2, err := NewUserStore(db2, nil)
	if err != nil {
		t.Fatalf("NewUserStore reopen: %v", err)
	}

	loaded := store2.Get(savedID)
	if loaded == nil {
		t.Fatal("persisted user not found after reopen")
	}
	if loaded.Username != "persist_user" {
		t.Errorf("username = %q, want persist_user", loaded.Username)
	}
	if len(loaded.AllowedServers) != 2 || loaded.AllowedServers[0] != "srv-0" || loaded.AllowedServers[1] != "srv-2" {
		t.Errorf("AllowedServers = %v, want [srv-0 srv-2]", loaded.AllowedServers)
	}
}

func TestUserStoreUpdateUsername(t *testing.T) {
	store := newTestUserStore(t)

	user, _ := store.Create("oldname", "pass", nil)

	newName := "newname"
	if err := store.Update(user.ID, &newName, nil, nil, nil); err != nil {
		t.Fatalf("Update username: %v", err)
	}

	if store.GetByUsername("oldname") != nil {
		t.Error("old username should not resolve")
	}
	got := store.GetByUsername("newname")
	if got == nil {
		t.Fatal("new username should resolve")
	}
	if got.ID != user.ID {
		t.Errorf("ID mismatch after rename")
	}
}

func TestUserStoreUpdatePassword(t *testing.T) {
	store := newTestUserStore(t)

	user, _ := store.Create("pwuser", "oldpass", nil)

	newPass := "newpass"
	if err := store.Update(user.ID, nil, &newPass, nil, nil); err != nil {
		t.Fatalf("Update password: %v", err)
	}

	if store.Authenticate("pwuser", "oldpass") != nil {
		t.Error("old password should not work")
	}
	if store.Authenticate("pwuser", "newpass") == nil {
		t.Error("new password should work")
	}
}
