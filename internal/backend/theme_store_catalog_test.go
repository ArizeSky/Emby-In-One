package backend

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestThemeInstallCatalogResourcesAndCAS(t *testing.T) {
	store := newThemeStoreTestStore(t, t.TempDir(), newThemeStoreTestDB(t))
	first := themeStoreTestPackage(t, "catalog-theme", 8)
	entry, err := store.InstallUpdate(first, 0)
	if err != nil {
		t.Fatalf("first install: %v", err)
	}
	if entry.ID != "catalog-theme" || entry.Builtin || !entry.Enabled || !entry.Available {
		t.Fatalf("unexpected installed entry %#v", entry)
	}
	if entry.Revision <= 0 || entry.Revision > ThemeMaxRevision {
		t.Fatalf("install revision %d outside JS-safe range", entry.Revision)
	}
	if entry.CurrentHash == "" || len(entry.Variants) != 2 || entry.Preview != "preview.png" ||
		entry.BackgroundImage != "assets/background.png" {
		t.Fatalf("incomplete installed entry %#v", entry)
	}
	themeStoreTestRequireFilePrivate(t, store.Root()+"/"+entry.CurrentHash+"/preview.png")
	themeStoreTestRequireFilePrivate(t, store.Root()+"/"+entry.CurrentHash+"/assets/background.png")

	catalog, err := store.Catalog()
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if len(catalog.Themes) != 2 || !catalog.Themes[0].Builtin || catalog.Themes[1].ID != "catalog-theme" {
		t.Fatalf("catalog must contain builtin plus the theme: %#v", catalog.Themes)
	}
	if catalog.Quota.UsedThemes != 1 || catalog.Quota.MaxThemes != ThemeMaxCustomThemes {
		t.Fatalf("unexpected quota %#v", catalog.Quota)
	}
	public, err := store.PublicCatalog()
	if err != nil || len(public.Themes) != 1 || public.Themes[0].Hash != entry.CurrentHash {
		t.Fatalf("PublicCatalog = %#v, %v", public, err)
	}
	site, err := store.Site()
	if err != nil || site.ThemeID != nil || site.Revision <= 0 {
		t.Fatalf("initial site settings = %#v, %v", site, err)
	}

	preview, err := store.ReadAsset(entry.CurrentHash, "preview.png")
	if err != nil {
		t.Fatalf("read preview: %v", err)
	}
	if string(preview.Data) != string(first.Preview.Data) || preview.MIME != "image/png" {
		t.Fatal("preview bytes or MIME changed in storage")
	}
	background, err := store.ReadAsset(entry.CurrentHash, "assets/background.png")
	if err != nil {
		t.Fatalf("read background: %v", err)
	}
	if string(background.Data) != string(first.Assets[0].Data) {
		t.Fatal("background bytes changed in storage")
	}

	if _, err := store.InstallUpdate(first, 0); !errors.Is(err, ErrThemeExists) {
		t.Fatalf("first install of an existing id error = %v, want ErrThemeExists", err)
	}
	second := themeStoreTestPackage(t, "catalog-theme", 11)
	if _, err := store.InstallUpdate(second, entry.Revision+1); !errors.Is(err, ErrThemeRevisionConflict) {
		t.Fatalf("stale update error = %v, want ErrThemeRevisionConflict", err)
	}
	updated, err := store.InstallUpdate(second, entry.Revision)
	if err != nil {
		t.Fatalf("update with current revision: %v", err)
	}
	if updated.CurrentHash == entry.CurrentHash || updated.Revision == entry.Revision ||
		updated.PreviousHash != entry.CurrentHash {
		t.Fatalf("unexpected update result %#v (first %#v)", updated, entry)
	}
	if _, err := store.ReadAsset(entry.CurrentHash, "preview.png"); err != nil {
		t.Fatalf("previous version must stay readable: %v", err)
	}
	if _, err := store.ReadAsset(updated.CurrentHash, "preview.png"); err != nil {
		t.Fatalf("current version must be readable: %v", err)
	}
}

func TestThemeEnabledSiteDefaultAndDeleteConflicts(t *testing.T) {
	store := newThemeStoreTestStore(t, t.TempDir(), newThemeStoreTestDB(t))
	entry := installThemeStoreTestPackage(t, store, "conflict-theme", 8)
	themeID := entry.ID

	if _, err := store.SetEnabled(ThemeIDBuiltin, false, 0); !errors.Is(err, ErrThemeBuiltin) {
		t.Fatalf("disable builtin error = %v", err)
	}
	if err := store.Delete(ThemeIDBuiltin, 0); !errors.Is(err, ErrThemeBuiltin) {
		t.Fatalf("delete builtin error = %v", err)
	}
	disabled, err := store.SetEnabled(themeID, false, entry.Revision)
	if err != nil || disabled.Enabled {
		t.Fatalf("disable theme = %#v, %v", disabled, err)
	}
	public, err := store.PublicCatalog()
	if err != nil || len(public.Themes) != 0 {
		t.Fatalf("disabled theme must leave the public catalog: %#v, %v", public, err)
	}
	if _, err := store.SetEnabled(themeID, true, entry.Revision); !errors.Is(err, ErrThemeRevisionConflict) {
		t.Fatalf("stale enable error = %v", err)
	}
	enabled, err := store.SetEnabled(themeID, true, disabled.Revision)
	if err != nil || !enabled.Enabled {
		t.Fatalf("re-enable theme = %#v, %v", enabled, err)
	}

	site, err := store.Site()
	if err != nil {
		t.Fatalf("Site: %v", err)
	}
	if _, err := store.SetSiteDefault(&themeID, site.Revision+1); !errors.Is(err, ErrThemeRevisionConflict) {
		t.Fatalf("stale site update error = %v", err)
	}
	site, err = store.SetSiteDefault(&themeID, site.Revision)
	if err != nil || site.ThemeID == nil || *site.ThemeID != themeID {
		t.Fatalf("set site default = %#v, %v", site, err)
	}
	if _, err := store.SetEnabled(themeID, false, enabled.Revision); !errors.Is(err, ErrThemeInUse) {
		t.Fatalf("disable site default error = %v", err)
	}
	if err := store.Delete(themeID, enabled.Revision); !errors.Is(err, ErrThemeInUse) {
		t.Fatalf("delete site default error = %v", err)
	}

	unknown := "missing-theme"
	if _, err := store.SetSiteDefault(&unknown, site.Revision); !errors.Is(err, ErrThemeNotFound) {
		t.Fatalf("unknown site default error = %v", err)
	}
	site, err = store.SetSiteDefault(nil, site.Revision)
	if err != nil || site.ThemeID != nil {
		t.Fatalf("reset site default before disabling = %#v, %v", site, err)
	}
	disabledAgain, err := store.SetEnabled(themeID, false, enabled.Revision)
	if err != nil {
		t.Fatalf("disable for site availability test: %v", err)
	}
	if _, err := store.SetSiteDefault(&themeID, site.Revision); !errors.Is(err, ErrThemeUnavailable) {
		t.Fatalf("disabled site default error = %v", err)
	}
	site, err = store.SetSiteDefault(nil, site.Revision)
	if err != nil || site.ThemeID != nil {
		t.Fatalf("reset site default to builtin = %#v, %v", site, err)
	}
	if err := store.Delete(themeID, disabledAgain.Revision); err != nil {
		t.Fatalf("delete unreferenced theme: %v", err)
	}
	if _, err := store.ReadAsset(entry.CurrentHash, "preview.png"); !errors.Is(err, ErrThemeResourceNotFound) {
		t.Fatalf("deleted theme resource error = %v, want ErrThemeResourceNotFound", err)
	}
	catalog, err := store.Catalog()
	if err != nil || len(catalog.Themes) != 1 || catalog.Themes[0].ID != ThemeIDBuiltin {
		t.Fatalf("catalog after delete = %#v, %v", catalog, err)
	}
}

func TestThemeInstallRejectsReservedIDsWithoutParser(t *testing.T) {
	store := newThemeStoreTestStore(t, t.TempDir(), newThemeStoreTestDB(t))
	for _, id := range []string{ThemeIDBuiltin, "default", "admin", "web"} {
		pkg := &ThemePackage{Manifest: ThemeManifest{
			ThemeAPIVersion: ThemeAPIVersion,
			ID:              id,
			Name:            "Reserved",
			Version:         "1",
		}}
		if _, err := store.InstallUpdate(pkg, 0); !errors.Is(err, ErrThemeInvalid) {
			t.Fatalf("reserved id %q error = %v, want ErrThemeInvalid", id, err)
		}
	}
}

func TestThemeUpdateWithIdenticalPackageKeepsOneDirectory(t *testing.T) {
	store := newThemeStoreTestStore(t, t.TempDir(), newThemeStoreTestDB(t))
	pkg := themeStoreTestPackage(t, "same-theme", 8)
	entry, err := store.InstallUpdate(pkg, 0)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	hashes, err := themeStoreScanThemeDirs(store.Root())
	if err != nil || len(hashes) != 1 {
		t.Fatalf("theme dirs after install = %v, %v", hashes, err)
	}
	updated, err := store.InstallUpdate(pkg, entry.Revision)
	if err != nil {
		t.Fatalf("identical update: %v", err)
	}
	if updated.CurrentHash != entry.CurrentHash || updated.PreviousHash != "" ||
		updated.Revision == entry.Revision {
		t.Fatalf("identical update result = %#v", updated)
	}
	hashes, err = themeStoreScanThemeDirs(store.Root())
	if err != nil || len(hashes) != 1 {
		t.Fatalf("identical update created extra directories: %v, %v", hashes, err)
	}
}

func TestThemeDeleteResetsPreferencesWithFreshRevision(t *testing.T) {
	db := newThemeStoreTestDB(t)
	store := newThemeStoreTestStore(t, t.TempDir(), db)
	entry := installThemeStoreTestPackage(t, store, "prefs-theme", 8)
	themeID := entry.ID
	preference, err := store.GetPreference("user-a")
	if err != nil {
		t.Fatalf("seed preference: %v", err)
	}
	saved, err := store.SavePreference("user-a", &themeID, ThemeModeSystem, preference.Revision)
	if err != nil || saved.ThemeID == nil || *saved.ThemeID != themeID {
		t.Fatalf("save preference = %#v, %v", saved, err)
	}
	if err := store.Delete(themeID, entry.Revision); err != nil {
		t.Fatalf("delete theme: %v", err)
	}
	reset, err := store.GetPreference("user-a")
	if err != nil {
		t.Fatalf("preference after theme delete: %v", err)
	}
	if reset.ThemeID == nil || *reset.ThemeID != ThemeIDBuiltin {
		t.Fatalf("preference after delete = %#v, want explicit %q", reset, ThemeIDBuiltin)
	}
	if stored := themeStoreTestStoredPreferenceThemeID(t, db, "user-a"); stored != ThemeIDBuiltin {
		t.Fatalf("stored preference theme_id = %q, want %q", stored, ThemeIDBuiltin)
	}
	if reset.Revision <= 0 || reset.Revision == saved.Revision {
		t.Fatalf("preference revision was not refreshed (was %d, now %d)", saved.Revision, reset.Revision)
	}
	if _, err := store.SavePreference("user-a", nil, ThemeModeDark, saved.Revision); !errors.Is(err, ErrThemeRevisionConflict) {
		t.Fatalf("stale save after delete error = %v, want ErrThemeRevisionConflict", err)
	}
	if _, err := store.SavePreference("user-a", &themeID, ThemeModeDark, reset.Revision); !errors.Is(err, ErrThemeNotFound) {
		t.Fatalf("deleted theme save error = %v, want ErrThemeNotFound", err)
	}
}

func TestThemeDeleteDatabaseFailureKeepsThemePreferencesAndResources(t *testing.T) {
	dataDir := t.TempDir()
	db := newThemeStoreTestDB(t)
	store := newThemeStoreTestStore(t, dataDir, db)
	entry := installThemeStoreTestPackage(t, store, "delete-db-failure", 8)
	seeded, err := store.GetPreference("user-a")
	if err != nil {
		t.Fatalf("seed preference: %v", err)
	}
	saved, err := store.SavePreference("user-a", &entry.ID, ThemeModeSystem, seeded.Revision)
	if err != nil || saved.ThemeID == nil || *saved.ThemeID != entry.ID {
		t.Fatalf("save preference = %#v, %v", saved, err)
	}
	siteBefore, err := store.Site()
	if err != nil {
		t.Fatalf("Site before failed delete: %v", err)
	}
	if err := db.withWriteLock(func() error {
		return db.exec(`CREATE TRIGGER theme_delete_abort BEFORE DELETE ON theme_catalog
			BEGIN SELECT RAISE(ABORT, 'injected delete failure'); END`)
	}); err != nil {
		t.Fatalf("install delete-abort trigger: %v", err)
	}

	if err := store.Delete(entry.ID, entry.Revision); err == nil {
		t.Fatal("database delete failure was reported as success")
	}

	catalog, err := store.Catalog()
	if err != nil {
		t.Fatalf("Catalog after failed delete: %v", err)
	}
	kept, found := themeCatalogEntry(catalog, entry.ID)
	if !found || !kept.Available || kept.Revision != entry.Revision || kept.CurrentHash != entry.CurrentHash {
		t.Fatalf("failed delete changed the catalog entry: %#v", catalog.Themes)
	}
	siteAfter, err := store.Site()
	if err != nil || siteAfter.Revision != siteBefore.Revision ||
		(siteAfter.ThemeID == nil) != (siteBefore.ThemeID == nil) {
		t.Fatalf("failed delete changed site settings: %#v, %v (was %#v)", siteAfter, err, siteBefore)
	}
	preference, err := store.GetPreference("user-a")
	if err != nil {
		t.Fatalf("preference after failed delete: %v", err)
	}
	if preference.ThemeID == nil || *preference.ThemeID != entry.ID ||
		preference.Mode != ThemeModeSystem || preference.Revision != saved.Revision {
		t.Fatalf("failed delete changed the preference: %#v (was %#v)", preference, saved)
	}
	if stored := themeStoreTestStoredPreferenceThemeID(t, db, "user-a"); stored != entry.ID {
		t.Fatalf("stored preference theme_id = %q, want %q", stored, entry.ID)
	}
	if _, err := store.ReadAsset(entry.CurrentHash, "preview.png"); err != nil {
		t.Fatalf("failed delete removed the resource: %v", err)
	}

	if err := store.CloseSafety(); err != nil {
		t.Fatalf("close before restart: %v", err)
	}
	reopened := newThemeStoreTestStore(t, dataDir, db)
	restarted, err := reopened.Catalog()
	if err != nil {
		t.Fatalf("Catalog after restart: %v", err)
	}
	kept, found = themeCatalogEntry(restarted, entry.ID)
	if !found || !kept.Available || kept.Revision != entry.Revision || kept.CurrentHash != entry.CurrentHash {
		t.Fatalf("restart did not preserve the theme after a failed delete: %#v", restarted.Themes)
	}
	if _, err := reopened.ReadAsset(entry.CurrentHash, "preview.png"); err != nil {
		t.Fatalf("restart lost the resource after a failed delete: %v", err)
	}
}

func TestThemeDeleteReadOnlyDatabaseKeepsThemeAndResources(t *testing.T) {
	dataDir := t.TempDir()
	db := newThemeStoreTestDB(t)
	store := newThemeStoreTestStore(t, dataDir, db)
	entry := installThemeStoreTestPackage(t, store, "delete-readonly", 8)
	seeded, err := store.GetPreference("user-a")
	if err != nil {
		t.Fatalf("seed preference: %v", err)
	}
	saved, err := store.SavePreference("user-a", &entry.ID, ThemeModeDark, seeded.Revision)
	if err != nil {
		t.Fatalf("save preference: %v", err)
	}

	themeStoreTestSetQueryOnly(t, db, true)
	defer themeStoreTestSetQueryOnly(t, db, false)
	if err := store.Delete(entry.ID, entry.Revision); err == nil {
		t.Fatal("delete against a read-only database was reported as success")
	}
	themeStoreTestSetQueryOnly(t, db, false)

	catalog, err := store.Catalog()
	if err != nil {
		t.Fatalf("Catalog after read-only delete: %v", err)
	}
	kept, found := themeCatalogEntry(catalog, entry.ID)
	if !found || !kept.Available || kept.Revision != entry.Revision || kept.CurrentHash != entry.CurrentHash {
		t.Fatalf("read-only delete changed the catalog entry: %#v", catalog.Themes)
	}
	preference, err := store.GetPreference("user-a")
	if err != nil || preference.ThemeID == nil || *preference.ThemeID != entry.ID ||
		preference.Revision != saved.Revision {
		t.Fatalf("read-only delete changed the preference: %#v, %v (was %#v)", preference, err, saved)
	}
	if _, err := store.ReadAsset(entry.CurrentHash, "preview.png"); err != nil {
		t.Fatalf("read-only delete removed the resource: %v", err)
	}

	if err := store.CloseSafety(); err != nil {
		t.Fatalf("close before restart: %v", err)
	}
	reopened := newThemeStoreTestStore(t, dataDir, db)
	restarted, err := reopened.Catalog()
	if err != nil {
		t.Fatalf("Catalog after restart: %v", err)
	}
	kept, found = themeCatalogEntry(restarted, entry.ID)
	if !found || !kept.Available || kept.Revision != entry.Revision {
		t.Fatalf("restart did not preserve the theme after read-only delete: %#v", restarted.Themes)
	}
	if _, err := reopened.ReadAsset(entry.CurrentHash, "preview.png"); err != nil {
		t.Fatalf("restart lost the resource after read-only delete: %v", err)
	}
}

func TestThemeDeleteCleanupFailureIsLogicalSuccessAndQuotaPending(t *testing.T) {
	dataDir := t.TempDir()
	db := newThemeStoreTestDB(t)
	store := newThemeStoreTestStore(t, dataDir, db)
	entry := installThemeStoreTestPackage(t, store, "delete-cleanup-failure", 8)
	before, err := store.Catalog()
	if err != nil {
		t.Fatalf("Catalog before delete: %v", err)
	}
	usedBefore := before.Quota.UsedBytes
	if usedBefore <= 0 {
		t.Fatalf("test fixture has no resource bytes: %#v", before.Quota)
	}

	originalRemove := themeStoreDeleteRemoveTree
	themeStoreDeleteRemoveTree = func(root, path string) error {
		return errors.New("injected physical cleanup failure")
	}
	defer func() { themeStoreDeleteRemoveTree = originalRemove }()
	if err := store.Delete(entry.ID, entry.Revision); err != nil {
		t.Fatalf("committed delete failed because physical cleanup failed: %v", err)
	}
	themeStoreDeleteRemoveTree = originalRemove

	if _, err := store.ReadAsset(entry.CurrentHash, "preview.png"); !errors.Is(err, ErrThemeResourceNotFound) {
		t.Fatalf("logically deleted resource error = %v, want ErrThemeResourceNotFound", err)
	}
	catalog, err := store.Catalog()
	if err != nil {
		t.Fatalf("Catalog after cleanup failure: %v", err)
	}
	if _, found := themeCatalogEntry(catalog, entry.ID); found {
		t.Fatalf("logically deleted theme is still in the catalog: %#v", catalog.Themes)
	}
	if catalog.Quota.UsedThemes != 0 || catalog.Quota.UsedBytes != usedBefore {
		t.Fatalf("cleanup failure quota = %#v, want used themes 0 and %d pending bytes", catalog.Quota, usedBefore)
	}
	pending := false
	for _, problem := range catalog.Health.Problems {
		if strings.Contains(problem, "cleanup pending") {
			pending = true
			break
		}
	}
	if !pending {
		t.Fatalf("health did not report pending cleanup: %#v", catalog.Health)
	}
	hashes, err := themeStoreScanThemeDirs(store.Root())
	if err != nil || len(hashes) != 1 {
		t.Fatalf("failed cleanup did not leave the strict orphan for recovery: %v, %v", hashes, err)
	}

	if err := store.CloseSafety(); err != nil {
		t.Fatalf("close before restart cleanup: %v", err)
	}
	reopened := newThemeStoreTestStore(t, dataDir, db)
	restarted, err := reopened.Catalog()
	if err != nil {
		t.Fatalf("Catalog after restart cleanup: %v", err)
	}
	if restarted.Quota.UsedBytes != 0 {
		t.Fatalf("restart cleanup left quota bytes: %#v", restarted.Quota)
	}
	if restarted.Health.OrphanDirs < 1 {
		t.Fatalf("restart cleanup did not report the reclaimed orphan: %#v", restarted.Health)
	}
	hashes, err = themeStoreScanThemeDirs(reopened.Root())
	if err != nil || len(hashes) != 0 {
		t.Fatalf("restart did not clean the strict orphan: %v, %v", hashes, err)
	}
	if _, err := reopened.ReadAsset(entry.CurrentHash, "preview.png"); !errors.Is(err, ErrThemeResourceNotFound) {
		t.Fatalf("restart served a logically deleted resource: %v", err)
	}
}

func TestThemeQuotaCustomCountAndReuseAfterDelete(t *testing.T) {
	store := newThemeStoreTestStore(t, t.TempDir(), newThemeStoreTestDB(t))
	entries := make([]ThemeCatalogEntry, 0, ThemeMaxCustomThemes)
	for index := 0; index < ThemeMaxCustomThemes; index++ {
		entries = append(entries, installThemeStoreTestPackage(t, store, fmt.Sprintf("quota-%02d", index), 8))
	}
	if _, err := store.InstallUpdate(themeStoreTestPackage(t, "quota-over", 8), 0); !errors.Is(err, ErrThemeQuota) {
		t.Fatalf("21st theme error = %v, want ErrThemeQuota", err)
	}
	if err := store.Delete(entries[0].ID, entries[0].Revision); err != nil {
		t.Fatalf("delete to free a quota slot: %v", err)
	}
	if _, err := store.InstallUpdate(themeStoreTestPackage(t, "quota-over", 8), 0); err != nil {
		t.Fatalf("install after freeing a slot: %v", err)
	}
}
