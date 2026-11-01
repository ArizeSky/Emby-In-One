package backend

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Shared fixtures for the theme store tests. All state lives under t.TempDir
// and the existing SQLite helper; no test touches the running application,
// public/, deployment paths or a real upstream.

func themeStoreTestOpenDB(t *testing.T, path string) *sqliteDB {
	t.Helper()
	db, err := openSQLite(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = closeSQLite(db) })
	return db
}

func newThemeStoreTestDB(t *testing.T) *sqliteDB {
	t.Helper()
	return themeStoreTestOpenDB(t, filepath.Join(t.TempDir(), "mappings.db"))
}

func newThemeStoreTestStore(t *testing.T, dataDir string, db *sqliteDB) *ThemeStore {
	t.Helper()
	store, err := NewThemeStore(dataDir, db)
	if err != nil {
		t.Fatalf("NewThemeStore: %v", err)
	}
	if store == nil {
		t.Fatal("NewThemeStore returned nil for a live DB")
	}
	t.Cleanup(func() { _ = store.CloseSafety() })
	return store
}

// themeStoreTestPackage builds a valid package with a preview and one
// referenced background image. The image size changes the content hash, so a
// "new version" of the same theme ID gets a distinct immutable directory.
func themeStoreTestPackage(t *testing.T, themeID string, backgroundSize int) *ThemePackage {
	t.Helper()
	payload := themeTestBuildZip(t,
		themeTestZipEntry{Name: "manifest.json", Data: []byte(themeTestManifestJSON(themeID))},
		themeTestZipEntry{Name: "theme.json", Data: []byte(themeTestThemeJSON("assets/background.png"))},
		themeTestZipEntry{Name: "preview.png", Data: themeTestPNGBytes(t, 8, 8)},
		themeTestZipEntry{Name: "assets/background.png", Data: themeTestPNGBytes(t, backgroundSize, backgroundSize)},
	)
	pkg, err := ParseThemePackage(payload)
	if err != nil {
		t.Fatalf("parse theme test package %s: %v", themeID, err)
	}
	return pkg
}

func installThemeStoreTestPackage(t *testing.T, store *ThemeStore, themeID string, backgroundSize int) ThemeCatalogEntry {
	t.Helper()
	entry, err := store.InstallUpdate(themeStoreTestPackage(t, themeID, backgroundSize), 0)
	if err != nil {
		t.Fatalf("install %s: %v", themeID, err)
	}
	return entry
}

func themeStoreTestSetQueryOnly(t *testing.T, db *sqliteDB, enabled bool) {
	t.Helper()
	value := "OFF"
	if enabled {
		value = "ON"
	}
	// PRAGMA query_only is connection-scoped and is not a database write, so it
	// must be switched under the connection lock directly. Opening a write
	// transaction first (BEGIN IMMEDIATE) would fail while query_only is ON and
	// would make the helper unable to restore the connection.
	if err := db.withWriteLock(func() error {
		return db.exec(`PRAGMA query_only = ` + value)
	}); err != nil {
		t.Fatalf("set query_only %s: %v", value, err)
	}
}

func themeStoreTestRequireFilePrivate(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 || perm&0o111 != 0 {
		t.Fatalf("%s mode %04o must be owner-only and non-executable", path, perm)
	}
}

func themeStoreTestStoredPreferenceThemeID(t *testing.T, db *sqliteDB, userID string) string {
	t.Helper()
	themeID := ""
	err := db.withReadLock(func() error {
		stmt, err := db.prepare(`SELECT theme_id FROM theme_preferences WHERE user_id = ?`)
		if err != nil {
			return err
		}
		defer stmt.finalize()
		if err := stmt.bindAll(userID); err != nil {
			return err
		}
		hasRow, err := stmt.step()
		if err != nil {
			return err
		}
		if hasRow {
			themeID = stmt.columnText(0)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("read stored preference for %q: %v", userID, err)
	}
	return themeID
}

func TestThemeStoreConstructorRequiresUsableDatabaseAndRoot(t *testing.T) {
	db := newThemeStoreTestDB(t)
	if store, err := NewThemeStore(t.TempDir(), nil); err == nil || store != nil {
		t.Fatalf("nil database must fail explicitly, got store=%v err=%v", store, err)
	} else if !errors.Is(err, ErrThemeStoreUnavailable) {
		t.Fatalf("nil database error = %v, want ErrThemeStoreUnavailable", err)
	}

	dataDir := filepath.Join(t.TempDir(), "data-is-a-file")
	if err := os.WriteFile(dataDir, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("prepare data dir file: %v", err)
	}
	if store, err := NewThemeStore(dataDir, db); err == nil || store != nil {
		t.Fatalf("unusable root must fail explicitly, got store=%v err=%v", store, err)
	}

	store := newThemeStoreTestStore(t, t.TempDir(), db)
	if !store.Available() {
		t.Fatal("live store must be available")
	}
	if store.Root() == "" || !strings.HasSuffix(store.Root(), string(os.PathSeparator)+ThemeStoreDirName) {
		t.Fatalf("unexpected theme root %q", store.Root())
	}
	if info, err := os.Stat(store.Root()); err != nil || !info.IsDir() {
		t.Fatalf("theme root is not a directory: %v", err)
	}
}

func TestThemeStoreNilAndCloseSafety(t *testing.T) {
	var nilStore *ThemeStore
	if _, err := nilStore.Catalog(); !errors.Is(err, ErrThemeStoreUnavailable) {
		t.Fatalf("nil Catalog error = %v", err)
	}
	if _, err := nilStore.GetPreference("user-a"); !errors.Is(err, ErrThemeStoreUnavailable) {
		t.Fatalf("nil GetPreference error = %v", err)
	}
	if err := nilStore.DeleteUserPreferences("user-a"); !errors.Is(err, ErrThemeStoreUnavailable) {
		t.Fatalf("nil DeleteUserPreferences error = %v", err)
	}
	if err := nilStore.CloseSafety(); !errors.Is(err, ErrThemeStoreUnavailable) {
		t.Fatalf("nil CloseSafety error = %v", err)
	}
	if nilStore.Available() {
		t.Fatal("nil store must not be available")
	}

	db := newThemeStoreTestDB(t)
	store := newThemeStoreTestStore(t, t.TempDir(), db)
	if err := store.CloseSafety(); err != nil {
		t.Fatalf("first CloseSafety: %v", err)
	}
	if store.Available() {
		t.Fatal("closed store must not be available")
	}
	if err := store.CloseSafety(); !errors.Is(err, ErrThemeStoreClosed) {
		t.Fatalf("second CloseSafety error = %v, want ErrThemeStoreClosed", err)
	}
	if _, err := store.Catalog(); !errors.Is(err, ErrThemeStoreClosed) {
		t.Fatalf("Catalog after close error = %v", err)
	}
	if _, err := store.GetPreference("user-a"); !errors.Is(err, ErrThemeStoreClosed) {
		t.Fatalf("GetPreference after close error = %v", err)
	}
	// Closing the store must not close the shared database owned by IDStore.
	if err := db.withReadLock(func() error { return nil }); err != nil {
		t.Fatalf("shared database was closed by the store: %v", err)
	}
}

func TestNextThemeRevisionIsPositiveJSSafeAndChanges(t *testing.T) {
	previous := int64(0)
	for i := 0; i < 200; i++ {
		next := nextThemeRevision(previous)
		if next <= 0 || next > ThemeMaxRevision {
			t.Fatalf("revision %d outside 1..%d", next, ThemeMaxRevision)
		}
		if next == previous {
			t.Fatalf("revision did not change from %d", previous)
		}
		previous = next
	}
	for _, start := range []int64{ThemeMaxRevision, ThemeMaxRevision - 1, 1} {
		next := nextThemeRevision(start)
		if next <= 0 || next > ThemeMaxRevision || next == start {
			t.Fatalf("revision %d from %d is invalid", next, start)
		}
	}
}

func TestThemeStoreReadOnlyDatabaseFailsWithoutFakeSuccess(t *testing.T) {
	db := newThemeStoreTestDB(t)
	store := newThemeStoreTestStore(t, t.TempDir(), db)
	themeStoreTestSetQueryOnly(t, db, true)
	defer themeStoreTestSetQueryOnly(t, db, false)

	if _, err := store.InstallUpdate(themeStoreTestPackage(t, "readonly-theme", 8), 0); err == nil {
		t.Fatal("install against a read-only database must fail")
	}
	catalog, err := store.Catalog()
	if err != nil {
		t.Fatalf("Catalog on read-only database: %v", err)
	}
	if catalog.Quota.UsedThemes != 0 {
		t.Fatalf("failed install increased theme count to %d", catalog.Quota.UsedThemes)
	}
	hashes, err := themeStoreScanThemeDirs(store.Root())
	if err != nil {
		t.Fatalf("scan theme dirs: %v", err)
	}
	if len(hashes) != 0 {
		t.Fatalf("failed install left resource directories: %v", hashes)
	}
	if _, err := store.GetPreference("user-a"); err == nil {
		t.Fatal("preference seed against a read-only database must fail")
	}
}

func TestThemeStoreReadOnlyConstructorFailsExplicitly(t *testing.T) {
	db := newThemeStoreTestDB(t)
	themeStoreTestSetQueryOnly(t, db, true)
	defer themeStoreTestSetQueryOnly(t, db, false)
	if store, err := NewThemeStore(t.TempDir(), db); err == nil || store != nil {
		t.Fatalf("read-only DB must not produce a usable store, got store=%v err=%v", store, err)
	}
}
