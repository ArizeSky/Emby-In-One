package backend

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Resource tests cover path safety, runtime integrity (size/SHA/symlink),
// byte quota, concurrent read/delete, and a filesystem publish whose database
// commit fails.

func TestThemeStoreResourceLookupAndDefinitionCopy(t *testing.T) {
	store := newThemeStoreTestStore(t, t.TempDir(), newThemeStoreTestDB(t))
	pkg := themeStoreTestPackage(t, "resource-theme", 8)
	entry, err := store.InstallUpdate(pkg, 0)
	if err != nil {
		t.Fatalf("install resource theme: %v", err)
	}
	if pkg.Preview == nil || len(pkg.Assets) != 1 {
		t.Fatalf("unexpected test package preview/assets: %#v", pkg)
	}

	preview, err := store.ReadAsset(entry.CurrentHash, "preview.png")
	if err != nil {
		t.Fatalf("read preview: %v", err)
	}
	if preview.ContentHash != entry.CurrentHash || preview.Path != "preview.png" ||
		preview.MIME != "image/png" || preview.Bytes != len(pkg.Preview.Data) ||
		preview.SHA256 != pkg.Preview.SHA256 || !bytes.Equal(preview.Data, pkg.Preview.Data) {
		t.Fatalf("preview resource = %#v", preview)
	}
	background, err := store.ReadAsset(entry.CurrentHash, "assets/background.png")
	if err != nil {
		t.Fatalf("read background: %v", err)
	}
	if background.MIME != pkg.Assets[0].MIME || !bytes.Equal(background.Data, pkg.Assets[0].Data) {
		t.Fatalf("background resource = %#v", background)
	}

	for _, name := range []string{
		"",
		"../preview.png",
		"assets/../../preview.png",
		"/preview.png",
		"assets\\background.png",
		"assets/missing.png",
		"manifest.json",
	} {
		if _, err := store.ReadAsset(entry.CurrentHash, name); !errors.Is(err, ErrThemeResourceNotFound) {
			t.Fatalf("ReadAsset(%q) error = %v, want ErrThemeResourceNotFound", name, err)
		}
	}
	if _, err := store.ReadAsset("not-a-content-hash", "preview.png"); !errors.Is(err, ErrThemeResourceNotFound) {
		t.Fatalf("malformed hash error = %v, want ErrThemeResourceNotFound", err)
	}
	if _, err := store.ReadAsset(strings.Repeat("0", 64), "preview.png"); !errors.Is(err, ErrThemeResourceNotFound) {
		t.Fatalf("unknown hash error = %v, want ErrThemeResourceNotFound", err)
	}

	definition, err := store.Definition(entry.CurrentHash)
	if err != nil {
		t.Fatalf("Definition: %v", err)
	}
	light, ok := definition.Variants[ThemeVariantLight]
	if !ok || light == (ThemeVariant{}) {
		t.Fatalf("Definition returned no light variant: %#v", definition)
	}
	originalLight := light
	definition.Variants[ThemeVariantLight] = ThemeVariant{Bg0: "#000000"}
	again, err := store.Definition(entry.CurrentHash)
	if err != nil {
		t.Fatalf("second Definition: %v", err)
	}
	if again.Variants[ThemeVariantLight] != originalLight {
		t.Fatal("Definition returned an aliased map; caller mutation leaked into the store")
	}
	if _, err := store.Definition(strings.Repeat("0", 64)); !errors.Is(err, ErrThemeResourceNotFound) {
		t.Fatalf("unknown Definition hash error = %v, want ErrThemeResourceNotFound", err)
	}
}

func TestThemeStoreResourceReadRejectsTamperedFileAndSymlink(t *testing.T) {
	store := newThemeStoreTestStore(t, t.TempDir(), newThemeStoreTestDB(t))
	entry := installThemeStoreTestPackage(t, store, "tamper-theme", 8)
	previewPath := filepath.Join(store.Root(), entry.CurrentHash, "preview.png")
	original, err := os.ReadFile(previewPath)
	if err != nil {
		t.Fatalf("read stored preview: %v", err)
	}

	// Same length, different bytes: the size check passes and the SHA-256
	// digest must reject the read.
	tampered := bytes.Repeat([]byte{0x7f}, len(original))
	if err := os.WriteFile(previewPath, tampered, 0o600); err != nil {
		t.Fatalf("tamper preview: %v", err)
	}
	if _, err := store.ReadAsset(entry.CurrentHash, "preview.png"); !errors.Is(err, ErrThemeCorrupt) {
		t.Fatalf("tampered resource error = %v, want ErrThemeCorrupt", err)
	}

	if err := os.WriteFile(previewPath, original, 0o600); err != nil {
		t.Fatalf("restore preview: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, original, 0o600); err != nil {
		t.Fatalf("write outside target: %v", err)
	}
	if err := os.Remove(previewPath); err != nil {
		t.Fatalf("remove preview before symlink: %v", err)
	}
	if err := os.Symlink(outside, previewPath); err != nil {
		t.Skipf("symlinks are unavailable on this platform: %v", err)
	}
	if _, err := store.ReadAsset(entry.CurrentHash, "preview.png"); !errors.Is(err, ErrThemeCorrupt) {
		t.Fatalf("symlinked resource error = %v, want ErrThemeCorrupt", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("outside symlink target was touched: %v", err)
	}
	if _, err := store.ReadAsset(entry.CurrentHash, "assets/background.png"); err != nil {
		t.Fatalf("unrelated resource became unreadable: %v", err)
	}
}

func TestThemeStoreByteQuotaRejectsBeforePublishing(t *testing.T) {
	store := newThemeStoreTestStore(t, t.TempDir(), newThemeStoreTestDB(t))
	previousLimit := themeStoreMaxBytes
	themeStoreMaxBytes = 1
	defer func() { themeStoreMaxBytes = previousLimit }()

	if _, err := store.InstallUpdate(themeStoreTestPackage(t, "byte-quota-theme", 8), 0); !errors.Is(err, ErrThemeQuota) {
		t.Fatalf("install over byte quota error = %v, want ErrThemeQuota", err)
	}
	catalog, err := store.Catalog()
	if err != nil {
		t.Fatalf("Catalog after quota rejection: %v", err)
	}
	if catalog.Quota.UsedThemes != 0 {
		t.Fatalf("quota rejection left %d catalog themes", catalog.Quota.UsedThemes)
	}
	hashes, err := themeStoreScanThemeDirs(store.Root())
	if err != nil {
		t.Fatalf("scan after quota rejection: %v", err)
	}
	if len(hashes) != 0 {
		t.Fatalf("quota rejection left resource directories: %v", hashes)
	}
}

func TestThemeStoreConcurrentReadAndDelete(t *testing.T) {
	store := newThemeStoreTestStore(t, t.TempDir(), newThemeStoreTestDB(t))
	pkg := themeStoreTestPackage(t, "concurrent-theme", 8)
	entry, err := store.InstallUpdate(pkg, 0)
	if err != nil {
		t.Fatalf("install concurrent theme: %v", err)
	}
	if pkg.Preview == nil {
		t.Fatal("test package has no preview")
	}

	const readers = 4
	const iterations = 40
	start := make(chan struct{})
	errs := make(chan error, readers)
	var wait sync.WaitGroup
	for index := 0; index < readers; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			for attempt := 0; attempt < iterations; attempt++ {
				resource, err := store.ReadAsset(entry.CurrentHash, "preview.png")
				if err != nil {
					if errors.Is(err, ErrThemeResourceNotFound) {
						continue
					}
					errs <- err
					return
				}
				if !bytes.Equal(resource.Data, pkg.Preview.Data) {
					errs <- errors.New("concurrent read returned altered preview bytes")
					return
				}
			}
		}()
	}
	close(start)
	if err := store.Delete(entry.ID, entry.Revision); err != nil {
		t.Fatalf("delete during reads: %v", err)
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent read failed: %v", err)
	}
	if _, err := store.ReadAsset(entry.CurrentHash, "preview.png"); !errors.Is(err, ErrThemeResourceNotFound) {
		t.Fatalf("read after delete error = %v, want ErrThemeResourceNotFound", err)
	}
}

func TestThemeStorePublishCommitFailureKeepsOldCurrent(t *testing.T) {
	dataDir := t.TempDir()
	db := newThemeStoreTestDB(t)
	store := newThemeStoreTestStore(t, dataDir, db)

	firstPkg := themeStoreTestPackage(t, "publish-failure-theme", 8)
	first, err := store.InstallUpdate(firstPkg, 0)
	if err != nil {
		t.Fatalf("install first version: %v", err)
	}
	secondPkg := themeStoreTestPackage(t, "publish-failure-theme", 13)
	if secondPkg.ContentHash == first.CurrentHash {
		t.Fatal("test fixtures must produce distinct content hashes")
	}

	themeStoreTestSetQueryOnly(t, db, true)
	_, installErr := store.InstallUpdate(secondPkg, first.Revision)
	themeStoreTestSetQueryOnly(t, db, false)
	if installErr == nil {
		t.Fatal("database commit failure was reported as a successful publish")
	}

	catalog, err := store.Catalog()
	if err != nil {
		t.Fatalf("Catalog after failed publish: %v", err)
	}
	entry, found := themeCatalogEntry(catalog, first.ID)
	if !found || !entry.Available || entry.CurrentHash != first.CurrentHash ||
		entry.PreviousHash != "" || entry.Revision != first.Revision {
		t.Fatalf("failed publish changed the stored current version: %#v", catalog.Themes)
	}
	hashes, err := themeStoreScanThemeDirs(store.Root())
	if err != nil || len(hashes) != 1 || filepath.Base(hashes[0]) != first.CurrentHash {
		t.Fatalf("failed publish left staged resources: %v, %v", hashes, err)
	}
	resource, err := store.ReadAsset(first.CurrentHash, "preview.png")
	if err != nil || !bytes.Equal(resource.Data, firstPkg.Preview.Data) {
		t.Fatalf("old current resource after failed publish = %#v, %v", resource, err)
	}

	updated, err := store.InstallUpdate(secondPkg, first.Revision)
	if err != nil {
		t.Fatalf("publish after restoring the database: %v", err)
	}
	if updated.CurrentHash != secondPkg.ContentHash || updated.PreviousHash != first.CurrentHash {
		t.Fatalf("recovered publish result = %#v", updated)
	}
}

func themeCatalogEntry(catalog ThemeCatalog, themeID string) (ThemeCatalogEntry, bool) {
	for _, entry := range catalog.Themes {
		if entry.ID == themeID {
			return entry, true
		}
	}
	return ThemeCatalogEntry{}, false
}
