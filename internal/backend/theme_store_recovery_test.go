package backend

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Recovery tests exercise the startup paths: a corrupt current version falls
// back to an intact previous version, two unusable versions make the theme
// unavailable and reset the site default to builtin, and strict orphan cleanup
// never follows links or deletes unknown entries.

func TestThemeStoreRecoveryPromotesPreviousWhenCurrentIsGone(t *testing.T) {
	dataDir := t.TempDir()
	db := themeStoreTestOpenDB(t, filepath.Join(t.TempDir(), "mappings.db"))
	store := newThemeStoreTestStore(t, dataDir, db)

	first := installThemeStoreTestPackage(t, store, "recover-theme", 8)
	secondPkg := themeStoreTestPackage(t, "recover-theme", 13)
	second, err := store.InstallUpdate(secondPkg, first.Revision)
	if err != nil {
		t.Fatalf("install second version: %v", err)
	}
	if second.CurrentHash != secondPkg.ContentHash || second.PreviousHash != first.CurrentHash {
		t.Fatalf("unexpected second version %#v (first %#v)", second, first)
	}
	root := store.Root()
	if err := store.CloseSafety(); err != nil {
		t.Fatalf("close before recovery: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(root, second.CurrentHash)); err != nil {
		t.Fatalf("remove current version directory: %v", err)
	}

	reopened := newThemeStoreTestStore(t, dataDir, db)
	catalog, err := reopened.Catalog()
	if err != nil {
		t.Fatalf("Catalog after recovery: %v", err)
	}
	entry, found := themeCatalogEntry(catalog, first.ID)
	if !found {
		t.Fatalf("theme disappeared from the catalog: %#v", catalog.Themes)
	}
	if !entry.Available || entry.CurrentHash != first.CurrentHash || entry.PreviousHash != "" {
		t.Fatalf("recovery did not promote the previous version: %#v", entry)
	}
	if entry.Revision <= 0 || entry.Revision == second.Revision {
		t.Fatalf("promoted revision %d was not refreshed (was %d)", entry.Revision, second.Revision)
	}
	if catalog.Health.Recovered < 1 {
		t.Fatalf("recovery health did not report the rollback: %#v", catalog.Health)
	}
	resource, err := reopened.ReadAsset(first.CurrentHash, "preview.png")
	if err != nil || len(resource.Data) == 0 {
		t.Fatalf("promoted previous resource is unreadable: %#v, %v", resource, err)
	}
	if _, err := reopened.ReadAsset(second.CurrentHash, "preview.png"); !errors.Is(err, ErrThemeResourceNotFound) {
		t.Fatalf("lost current version is still served: %v", err)
	}
	hashes, err := themeStoreScanThemeDirs(root)
	if err != nil || len(hashes) != 1 || filepath.Base(hashes[0]) != first.CurrentHash {
		t.Fatalf("recovery left stale version directories: %v, %v", hashes, err)
	}
}

func TestThemeStoreRecoveryUnavailableThemeResetsSiteToBuiltin(t *testing.T) {
	dataDir := t.TempDir()
	db := themeStoreTestOpenDB(t, filepath.Join(t.TempDir(), "mappings.db"))
	store := newThemeStoreTestStore(t, dataDir, db)

	first := installThemeStoreTestPackage(t, store, "unusable-theme", 8)
	secondPkg := themeStoreTestPackage(t, "unusable-theme", 13)
	second, err := store.InstallUpdate(secondPkg, first.Revision)
	if err != nil {
		t.Fatalf("install second version: %v", err)
	}
	site, err := store.Site()
	if err != nil {
		t.Fatalf("read site before delete: %v", err)
	}
	site, err = store.SetSiteDefault(&second.ID, site.Revision)
	if err != nil || site.ThemeID == nil || *site.ThemeID != second.ID {
		t.Fatalf("set site default = %#v, %v", site, err)
	}
	root := store.Root()
	if err := store.CloseSafety(); err != nil {
		t.Fatalf("close before recovery: %v", err)
	}
	for _, hash := range []string{second.CurrentHash, first.CurrentHash} {
		if err := os.RemoveAll(filepath.Join(root, hash)); err != nil {
			t.Fatalf("remove version directory %s: %v", hash, err)
		}
	}

	reopened := newThemeStoreTestStore(t, dataDir, db)
	catalog, err := reopened.Catalog()
	if err != nil {
		t.Fatalf("Catalog after double failure: %v", err)
	}
	entry, found := themeCatalogEntry(catalog, first.ID)
	if !found {
		t.Fatalf("unusable theme row was dropped: %#v", catalog.Themes)
	}
	if entry.Available || entry.CurrentHash != "" || entry.PreviousHash != "" || entry.Problem == "" {
		t.Fatalf("unusable theme was not marked unavailable: %#v", entry)
	}
	if catalog.Health.Missing < 1 {
		t.Fatalf("recovery health did not report the missing theme: %#v", catalog.Health)
	}
	recoveredSite, err := reopened.Site()
	if err != nil {
		t.Fatalf("Site after recovery: %v", err)
	}
	if recoveredSite.ThemeID != nil {
		t.Fatalf("site default still points at an unusable theme: %#v", recoveredSite)
	}
	if recoveredSite.Revision <= 0 || recoveredSite.Revision == site.Revision {
		t.Fatalf("site revision %d was not refreshed (was %d)", recoveredSite.Revision, site.Revision)
	}
	public, err := reopened.PublicCatalog()
	if err != nil || len(public.Themes) != 0 {
		t.Fatalf("PublicCatalog after double failure = %#v, %v", public, err)
	}
	for _, hash := range []string{first.CurrentHash, second.CurrentHash} {
		if _, err := reopened.ReadAsset(hash, "preview.png"); !errors.Is(err, ErrThemeResourceNotFound) {
			t.Fatalf("version %s is still served: %v", hash, err)
		}
	}
	hashes, err := themeStoreScanThemeDirs(root)
	if err != nil || len(hashes) != 0 {
		t.Fatalf("unusable versions left directories: %v, %v", hashes, err)
	}
}

func TestThemeStoreRecoveryCleansStrictOrphansWithoutFollowingLinks(t *testing.T) {
	dataDir := t.TempDir()
	root := filepath.Join(dataDir, ThemeStoreDirName)
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("prepare theme root: %v", err)
	}

	orphanHash := strings.Repeat("a", 64)
	staging := ".staging-leftover"
	trash := ".trash-leftover"
	for _, name := range []string{orphanHash, staging, trash} {
		directory := filepath.Join(root, name)
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatalf("create orphan %s: %v", name, err)
		}
		if err := os.WriteFile(filepath.Join(directory, "payload"), []byte("orphan"), 0o600); err != nil {
			t.Fatalf("write orphan %s payload: %v", name, err)
		}
	}

	outside := t.TempDir()
	sentinel := filepath.Join(outside, "keep.txt")
	if err := os.WriteFile(sentinel, []byte("outside"), 0o600); err != nil {
		t.Fatalf("write outside sentinel: %v", err)
	}
	hashLink := filepath.Join(root, strings.Repeat("b", 64))
	if err := os.Symlink(outside, hashLink); err != nil {
		t.Skipf("symlinks are unavailable on this platform: %v", err)
	}
	unknownLink := filepath.Join(root, "unknown-link")
	if err := os.Symlink(outside, unknownLink); err != nil {
		t.Fatalf("create unknown symlink: %v", err)
	}
	keep := filepath.Join(root, "keep-me")
	if err := os.Mkdir(keep, 0o700); err != nil {
		t.Fatalf("create unknown directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(keep, "keep.txt"), []byte("keep"), 0o600); err != nil {
		t.Fatalf("write unknown directory file: %v", err)
	}

	store := newThemeStoreTestStore(t, dataDir, themeStoreTestOpenDB(t, filepath.Join(t.TempDir(), "mappings.db")))
	catalog, err := store.Catalog()
	if err != nil {
		t.Fatalf("Catalog after orphan cleanup: %v", err)
	}
	if catalog.Health.OrphanDirs != 4 {
		t.Fatalf("orphan cleanup removed %d entries, want 4: %#v", catalog.Health.OrphanDirs, catalog.Health)
	}
	if catalog.Health.OrphanBytes <= 0 {
		t.Fatalf("orphan cleanup reported no reclaimed bytes: %#v", catalog.Health)
	}
	for _, name := range []string{orphanHash, staging, trash, strings.Repeat("b", 64)} {
		if _, err := os.Lstat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("strict orphan %q was not removed: %v", name, err)
		}
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("cleanup followed a symlink and touched its target: %v", err)
	}
	if _, err := os.Lstat(unknownLink); err != nil {
		t.Fatalf("cleanup removed an unknown symlink instead of reporting it: %v", err)
	}
	if _, err := os.Stat(filepath.Join(keep, "keep.txt")); err != nil {
		t.Fatalf("cleanup removed an unknown directory: %v", err)
	}
	if len(catalog.Health.Problems) == 0 {
		t.Fatal("cleanup did not report the unknown entries")
	}
}
