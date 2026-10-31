package backend

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Theme catalog persistence and catalog-level mutations.
//
// The database is the source of truth for installations and revisions; the
// resource tree holds only immutable, hash-named image files. Filesystem and
// database updates are not one transaction: a failed publish keeps the old
// current version, leaves at most a safely purged orphan directory, and never
// reports success. Every write re-checks the expected revision inside the
// short database transaction.

const themeStoreSchema = `
	CREATE TABLE IF NOT EXISTS theme_catalog (
		theme_id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		author TEXT NOT NULL,
		description TEXT NOT NULL,
		version TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1,
		current_hash TEXT NOT NULL,
		previous_hash TEXT NOT NULL DEFAULT '',
		revision INTEGER NOT NULL,
		installed_at INTEGER NOT NULL DEFAULT 0
	);
	CREATE TABLE IF NOT EXISTS theme_versions (
		content_hash TEXT PRIMARY KEY,
		theme_id TEXT NOT NULL,
		manifest_json TEXT NOT NULL,
		theme_json TEXT NOT NULL,
		preview_json TEXT NOT NULL DEFAULT '',
		assets_json TEXT NOT NULL,
		package_bytes INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX IF NOT EXISTS idx_theme_versions_theme ON theme_versions(theme_id);
	CREATE TABLE IF NOT EXISTS theme_site (
		id INTEGER PRIMARY KEY CHECK (id = 1),
		theme_id TEXT NOT NULL DEFAULT '',
		revision INTEGER NOT NULL
	);
	CREATE TABLE IF NOT EXISTS theme_preferences (
		user_id TEXT PRIMARY KEY,
		theme_id TEXT NOT NULL DEFAULT '',
		mode TEXT NOT NULL,
		revision INTEGER NOT NULL
	);
`

// themeStoreAssetDescriptor is the persisted, validated descriptor of one
// image. data is only populated while a package is being installed and is
// never serialized.
type themeStoreAssetDescriptor struct {
	Path   string `json:"path"`
	MIME   string `json:"mime"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Bytes  int    `json:"bytes"`
	SHA256 string `json:"sha256"`

	data []byte
}

func (a themeStoreAssetDescriptor) themeAsset() ThemeAsset {
	return ThemeAsset{
		Path:   a.Path,
		MIME:   a.MIME,
		Width:  a.Width,
		Height: a.Height,
		Bytes:  a.Bytes,
		SHA256: a.SHA256,
	}
}

// themeStoreVersion is one immutable installed version. ContentHash is always
// recomputed by the store from the validated metadata; it is never taken from
// caller input or a request path.
type themeStoreVersion struct {
	ContentHash  string
	ThemeID      string
	Manifest     ThemeManifest
	Theme        ThemeDefinition
	Preview      *themeStoreAssetDescriptor
	Assets       []themeStoreAssetDescriptor
	PackageBytes int64
}

func (v *themeStoreVersion) allDescriptors() []themeStoreAssetDescriptor {
	if v == nil {
		return nil
	}
	out := make([]themeStoreAssetDescriptor, 0, len(v.Assets)+1)
	out = append(out, v.Assets...)
	if v.Preview != nil {
		out = append(out, *v.Preview)
	}
	return out
}

// themeStoreCatalogRow mirrors one theme_catalog row. Empty hashes/IDs mean
// "absent"; theme IDs themselves are never empty.
type themeStoreCatalogRow struct {
	ID           string
	Name         string
	Author       string
	Description  string
	Version      string
	Enabled      bool
	CurrentHash  string
	PreviousHash string
	Revision     int64
	InstalledAt  int64
}

// themeStoreEntry is the in-memory catalog entry plus its recoverable versions.
type themeStoreEntry struct {
	row       themeStoreCatalogRow
	current   *themeStoreVersion
	previous  *themeStoreVersion
	available bool
	problem   string
}

func themeStoreRowEnabled(value bool) int {
	if value {
		return 1
	}
	return 0
}

// initSchemaAndSite creates the idempotent schema and materializes the site
// singleton with a non-zero random revision, so every admin form starts from a
// real revision and cannot overwrite a deleted/recreated row with 0 (ABA).
func (s *ThemeStore) initSchemaAndSite() error {
	var site ThemeSiteSettings
	err := s.db.withWriteTx(func() error {
		if err := s.db.exec(themeStoreSchema); err != nil {
			return err
		}
		existing, found, err := themeStoreReadSite(s.db)
		if err != nil {
			return err
		}
		if found {
			site = existing
			return nil
		}
		site = ThemeSiteSettings{Revision: nextThemeRevision(0)}
		return themeStoreWriteSite(s.db, site)
	})
	if err != nil {
		return fmt.Errorf("theme store schema: %w", err)
	}
	s.site = site
	return nil
}

func themeStoreReadCatalogRows(db *sqliteDB) ([]themeStoreCatalogRow, error) {
	stmt, err := db.prepare(`SELECT theme_id, name, author, description, version, enabled,
		current_hash, previous_hash, revision, installed_at FROM theme_catalog ORDER BY theme_id`)
	if err != nil {
		return nil, err
	}
	defer stmt.finalize()
	rows := make([]themeStoreCatalogRow, 0)
	for {
		hasRow, err := stmt.step()
		if err != nil {
			return nil, err
		}
		if !hasRow {
			break
		}
		rows = append(rows, themeStoreCatalogRow{
			ID:           stmt.columnText(0),
			Name:         stmt.columnText(1),
			Author:       stmt.columnText(2),
			Description:  stmt.columnText(3),
			Version:      stmt.columnText(4),
			Enabled:      stmt.columnInt(5) != 0,
			CurrentHash:  stmt.columnText(6),
			PreviousHash: stmt.columnText(7),
			Revision:     stmt.columnInt64(8),
			InstalledAt:  stmt.columnInt64(9),
		})
	}
	return rows, nil
}

func themeStoreReadCatalogRow(db *sqliteDB, themeID string) (themeStoreCatalogRow, bool, error) {
	stmt, err := db.prepare(`SELECT theme_id, name, author, description, version, enabled,
		current_hash, previous_hash, revision, installed_at FROM theme_catalog WHERE theme_id = ?`)
	if err != nil {
		return themeStoreCatalogRow{}, false, err
	}
	defer stmt.finalize()
	if err := stmt.bindAll(themeID); err != nil {
		return themeStoreCatalogRow{}, false, err
	}
	hasRow, err := stmt.step()
	if err != nil {
		return themeStoreCatalogRow{}, false, err
	}
	if !hasRow {
		return themeStoreCatalogRow{}, false, nil
	}
	return themeStoreCatalogRow{
		ID:           stmt.columnText(0),
		Name:         stmt.columnText(1),
		Author:       stmt.columnText(2),
		Description:  stmt.columnText(3),
		Version:      stmt.columnText(4),
		Enabled:      stmt.columnInt(5) != 0,
		CurrentHash:  stmt.columnText(6),
		PreviousHash: stmt.columnText(7),
		Revision:     stmt.columnInt64(8),
		InstalledAt:  stmt.columnInt64(9),
	}, true, nil
}

func themeStoreUpsertCatalogRow(db *sqliteDB, row themeStoreCatalogRow) error {
	return db.execParams(`INSERT OR REPLACE INTO theme_catalog
		(theme_id, name, author, description, version, enabled, current_hash, previous_hash, revision, installed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ID, row.Name, row.Author, row.Description, row.Version,
		themeStoreRowEnabled(row.Enabled), row.CurrentHash, row.PreviousHash,
		row.Revision, row.InstalledAt)
}

func themeStoreDeleteCatalogRow(db *sqliteDB, themeID string) error {
	return db.execParams(`DELETE FROM theme_catalog WHERE theme_id = ?`, themeID)
}

func themeStoreWriteVersion(db *sqliteDB, version *themeStoreVersion) error {
	manifestJSON, err := json.Marshal(version.Manifest)
	if err != nil {
		return fmt.Errorf("%w: manifest encoding: %v", ErrThemeCorrupt, err)
	}
	themeJSON, err := json.Marshal(version.Theme)
	if err != nil {
		return fmt.Errorf("%w: theme encoding: %v", ErrThemeCorrupt, err)
	}
	previewJSON := ""
	if version.Preview != nil {
		encoded, err := json.Marshal(version.Preview)
		if err != nil {
			return fmt.Errorf("%w: preview encoding: %v", ErrThemeCorrupt, err)
		}
		previewJSON = string(encoded)
	}
	assets := version.Assets
	if assets == nil {
		assets = make([]themeStoreAssetDescriptor, 0)
	}
	assetsJSON, err := json.Marshal(assets)
	if err != nil {
		return fmt.Errorf("%w: asset encoding: %v", ErrThemeCorrupt, err)
	}
	return db.execParams(`INSERT OR REPLACE INTO theme_versions
		(content_hash, theme_id, manifest_json, theme_json, preview_json, assets_json, package_bytes)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		version.ContentHash, version.ThemeID, string(manifestJSON), string(themeJSON),
		previewJSON, string(assetsJSON), version.PackageBytes)
}

func themeStoreReadVersion(db *sqliteDB, contentHash string) (*themeStoreVersion, bool, error) {
	stmt, err := db.prepare(`SELECT content_hash, theme_id, manifest_json, theme_json,
		preview_json, assets_json, package_bytes FROM theme_versions WHERE content_hash = ?`)
	if err != nil {
		return nil, false, err
	}
	defer stmt.finalize()
	if err := stmt.bindAll(contentHash); err != nil {
		return nil, false, err
	}
	hasRow, err := stmt.step()
	if err != nil {
		return nil, false, err
	}
	if !hasRow {
		return nil, false, nil
	}
	version := &themeStoreVersion{
		ContentHash:  stmt.columnText(0),
		ThemeID:      stmt.columnText(1),
		PackageBytes: stmt.columnInt64(6),
	}
	if err := json.Unmarshal([]byte(stmt.columnText(2)), &version.Manifest); err != nil {
		return nil, false, fmt.Errorf("%w: manifest row is invalid: %v", ErrThemeCorrupt, err)
	}
	if err := json.Unmarshal([]byte(stmt.columnText(3)), &version.Theme); err != nil {
		return nil, false, fmt.Errorf("%w: theme row is invalid: %v", ErrThemeCorrupt, err)
	}
	if previewJSON := stmt.columnText(4); strings.TrimSpace(previewJSON) != "" {
		var preview themeStoreAssetDescriptor
		if err := json.Unmarshal([]byte(previewJSON), &preview); err != nil {
			return nil, false, fmt.Errorf("%w: preview row is invalid: %v", ErrThemeCorrupt, err)
		}
		version.Preview = &preview
	}
	if assetsJSON := stmt.columnText(5); strings.TrimSpace(assetsJSON) != "" {
		var assets []themeStoreAssetDescriptor
		if err := json.Unmarshal([]byte(assetsJSON), &assets); err != nil {
			return nil, false, fmt.Errorf("%w: asset rows are invalid: %v", ErrThemeCorrupt, err)
		}
		version.Assets = assets
	}
	if err := themeStoreValidateVersion(version); err != nil {
		return nil, false, err
	}
	return version, true, nil
}

func themeStoreReadVersionHashes(db *sqliteDB) ([]string, error) {
	stmt, err := db.prepare(`SELECT content_hash FROM theme_versions`)
	if err != nil {
		return nil, err
	}
	defer stmt.finalize()
	hashes := make([]string, 0)
	for {
		hasRow, err := stmt.step()
		if err != nil {
			return nil, err
		}
		if !hasRow {
			break
		}
		hashes = append(hashes, stmt.columnText(0))
	}
	sort.Strings(hashes)
	return hashes, nil
}

func themeStoreDeleteVersion(db *sqliteDB, contentHash string) error {
	if contentHash == "" {
		return nil
	}
	return db.execParams(`DELETE FROM theme_versions WHERE content_hash = ?`, contentHash)
}

// themeStoreReadSite reads the singleton row. theme_id ” means builtin.
func themeStoreReadSite(db *sqliteDB) (ThemeSiteSettings, bool, error) {
	stmt, err := db.prepare(`SELECT theme_id, revision FROM theme_site WHERE id = 1`)
	if err != nil {
		return ThemeSiteSettings{}, false, err
	}
	defer stmt.finalize()
	hasRow, err := stmt.step()
	if err != nil {
		return ThemeSiteSettings{}, false, err
	}
	if !hasRow {
		return ThemeSiteSettings{}, false, nil
	}
	settings := ThemeSiteSettings{Revision: stmt.columnInt64(1)}
	if themeID := stmt.columnText(0); themeID != "" && themeID != ThemeIDBuiltin {
		value := themeID
		settings.ThemeID = &value
	}
	return settings, true, nil
}

func themeStoreWriteSite(db *sqliteDB, settings ThemeSiteSettings) error {
	themeID := ""
	if settings.ThemeID != nil {
		themeID = *settings.ThemeID
	}
	return db.execParams(`INSERT OR REPLACE INTO theme_site (id, theme_id, revision) VALUES (1, ?, ?)`,
		themeID, settings.Revision)
}

func themeStoreCatalogEntryFromMirror(entry *themeStoreEntry) ThemeCatalogEntry {
	out := ThemeCatalogEntry{
		ID:          entry.row.ID,
		Name:        entry.row.Name,
		Author:      entry.row.Author,
		Description: entry.row.Description,
		Version:     entry.row.Version,
		Enabled:     entry.row.Enabled,
		Available:   entry.available && entry.current != nil,
		Revision:    entry.row.Revision,
		CurrentHash: entry.row.CurrentHash,
		Variants:    themeStoreThemeVariants(),
		Problem:     entry.problem,
	}
	if entry.previous != nil {
		out.PreviousHash = entry.previous.ContentHash
	}
	if entry.current != nil {
		out.PackageBytes = entry.current.PackageBytes
		out.BackgroundImage = entry.current.Theme.BackgroundImage
		if entry.current.Preview != nil {
			out.Preview = entry.current.Preview.Path
		}
	}
	return out
}

func themeStoreBuiltinCatalogEntry() ThemeCatalogEntry {
	return ThemeCatalogEntry{
		ID:        ThemeIDBuiltin,
		Name:      "Builtin",
		Version:   "1",
		Enabled:   true,
		Builtin:   true,
		Available: true,
		Variants:  themeStoreThemeVariants(),
	}
}

func themeStorePublicEntry(version *themeStoreVersion) ThemePublicEntry {
	out := ThemePublicEntry{
		ID:              version.ThemeID,
		Name:            version.Manifest.Name,
		Author:          version.Manifest.Author,
		Description:     version.Manifest.Description,
		Version:         version.Manifest.Version,
		Hash:            version.ContentHash,
		Variants:        themeStoreThemeVariants(),
		BackgroundImage: version.Theme.BackgroundImage,
	}
	if version.Preview != nil {
		out.Preview = version.Preview.Path
	}
	return out
}

// Catalog returns the administrator catalog, site default, quota and health.
func (s *ThemeStore) Catalog() (ThemeCatalog, error) {
	if s == nil {
		return ThemeCatalog{}, ErrThemeStoreUnavailable
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.liveLocked(); err != nil {
		return ThemeCatalog{}, err
	}
	usedBytes, err := themeStoreDiskUsage(s.root)
	if err != nil {
		return ThemeCatalog{}, fmt.Errorf("theme store usage: %w", err)
	}
	catalog := ThemeCatalog{
		Themes: make([]ThemeCatalogEntry, 0, len(s.entries)+1),
		Site:   cloneThemeSiteSettings(s.site),
		Quota: ThemeQuota{
			MaxThemes:  ThemeMaxCustomThemes,
			UsedThemes: len(s.entries),
			MaxBytes:   themeStoreMaxBytes,
			UsedBytes:  usedBytes,
		},
		Health: ThemeStoreHealth{
			Available:   s.health.Available,
			Recovered:   s.health.Recovered,
			Missing:     s.health.Missing,
			OrphanDirs:  s.health.OrphanDirs,
			OrphanBytes: s.health.OrphanBytes,
			Problems:    append([]string(nil), s.health.Problems...),
		},
	}
	catalog.Themes = append(catalog.Themes, themeStoreBuiltinCatalogEntry())
	for _, id := range s.sortedThemeIDs() {
		catalog.Themes = append(catalog.Themes, themeStoreCatalogEntryFromMirror(s.entries[id]))
	}
	return catalog, nil
}

// PublicCatalog returns only enabled and available themes. The builtin theme is
// implicit and always available, so it is not repeated here.
func (s *ThemeStore) PublicCatalog() (ThemePublicCatalog, error) {
	if s == nil {
		return ThemePublicCatalog{}, ErrThemeStoreUnavailable
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.liveLocked(); err != nil {
		return ThemePublicCatalog{}, err
	}
	catalog := ThemePublicCatalog{Themes: make([]ThemePublicEntry, 0, len(s.entries))}
	for _, id := range s.sortedThemeIDs() {
		entry := s.entries[id]
		if !entry.row.Enabled || !entry.available || entry.current == nil {
			continue
		}
		catalog.Themes = append(catalog.Themes, themeStorePublicEntry(entry.current))
	}
	return catalog, nil
}

// Site returns the anonymous site default and its revision.
func (s *ThemeStore) Site() (ThemeSiteSettings, error) {
	if s == nil {
		return ThemeSiteSettings{}, ErrThemeStoreUnavailable
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if err := s.liveLocked(); err != nil {
		return ThemeSiteSettings{}, err
	}
	return cloneThemeSiteSettings(s.site), nil
}

// Root returns the immutable resource root for diagnostics and lifecycle code.
// It is intentionally not exposed in any public HTTP DTO.
func (s *ThemeStore) Root() string {
	if s == nil {
		return ""
	}
	return s.root
}

// SetSiteDefault updates the singleton site default. nil, "" and "builtin" all
// select the builtin theme and are stored as nil. The theme must be installed,
// enabled and available. The revision is re-checked inside the transaction.
func (s *ThemeStore) SetSiteDefault(themeID *string, expectedRevision int64) (ThemeSiteSettings, error) {
	if s == nil {
		return ThemeSiteSettings{}, ErrThemeStoreUnavailable
	}
	normalized, err := themeStoreNormalizeThemeID(themeID)
	if err != nil {
		return ThemeSiteSettings{}, err
	}
	if expectedRevision < 0 || expectedRevision > ThemeMaxRevision {
		return ThemeSiteSettings{}, fmt.Errorf("%w: expected revision out of range", ErrThemeInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.liveLocked(); err != nil {
		return ThemeSiteSettings{}, err
	}
	if normalized != nil {
		entry := s.entries[*normalized]
		if entry == nil {
			return ThemeSiteSettings{}, fmt.Errorf("%w: %s", ErrThemeNotFound, *normalized)
		}
		if !entry.row.Enabled || !entry.available || entry.current == nil {
			return ThemeSiteSettings{}, fmt.Errorf("%w: %s", ErrThemeUnavailable, *normalized)
		}
	}
	current := cloneThemeSiteSettings(s.site)
	if current.Revision != expectedRevision {
		return ThemeSiteSettings{}, fmt.Errorf("%w: site revision changed", ErrThemeRevisionConflict)
	}
	sameSelection := (current.ThemeID == nil && normalized == nil) ||
		(current.ThemeID != nil && normalized != nil && *current.ThemeID == *normalized)
	if sameSelection {
		return current, nil
	}
	next := ThemeSiteSettings{ThemeID: normalized, Revision: nextThemeRevision(current.Revision)}
	err = s.db.withWriteTx(func() error {
		stored, found, err := themeStoreReadSite(s.db)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("%w: site row disappeared", ErrThemeRevisionConflict)
		}
		if stored.Revision != expectedRevision {
			return fmt.Errorf("%w: site revision changed", ErrThemeRevisionConflict)
		}
		return themeStoreWriteSite(s.db, next)
	})
	if err != nil {
		return ThemeSiteSettings{}, fmt.Errorf("save site default: %w", err)
	}
	s.site = cloneThemeSiteSettings(next)
	return cloneThemeSiteSettings(s.site), nil
}

// SetEnabled enables or disables an installed theme. The builtin theme is
// immutable; disabling the current site default is rejected until the default
// is changed explicitly.
func (s *ThemeStore) SetEnabled(themeID string, enabled bool, expectedRevision int64) (ThemeCatalogEntry, error) {
	if s == nil {
		return ThemeCatalogEntry{}, ErrThemeStoreUnavailable
	}
	if themeID == ThemeIDBuiltin {
		return ThemeCatalogEntry{}, ErrThemeBuiltin
	}
	if err := themeStoreValidCustomID(themeID); err != nil {
		return ThemeCatalogEntry{}, err
	}
	if expectedRevision < 0 || expectedRevision > ThemeMaxRevision {
		return ThemeCatalogEntry{}, fmt.Errorf("%w: expected revision out of range", ErrThemeInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.liveLocked(); err != nil {
		return ThemeCatalogEntry{}, err
	}
	entry := s.entries[themeID]
	if entry == nil {
		return ThemeCatalogEntry{}, fmt.Errorf("%w: %s", ErrThemeNotFound, themeID)
	}
	if entry.row.Revision != expectedRevision {
		return ThemeCatalogEntry{}, fmt.Errorf("%w: theme %s revision changed", ErrThemeRevisionConflict, themeID)
	}
	if !enabled && s.site.ThemeID != nil && *s.site.ThemeID == themeID {
		return ThemeCatalogEntry{}, fmt.Errorf("%w: %s", ErrThemeInUse, themeID)
	}
	if entry.row.Enabled == enabled {
		return themeStoreCatalogEntryFromMirror(entry), nil
	}
	next := entry.row
	next.Enabled = enabled
	next.Revision = nextThemeRevision(entry.row.Revision)
	err := s.db.withWriteTx(func() error {
		stored, found, err := themeStoreReadCatalogRow(s.db, themeID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("%w: %s disappeared", ErrThemeNotFound, themeID)
		}
		if stored.Revision != expectedRevision {
			return fmt.Errorf("%w: theme %s revision changed", ErrThemeRevisionConflict, themeID)
		}
		return themeStoreUpsertCatalogRow(s.db, next)
	})
	if err != nil {
		return ThemeCatalogEntry{}, fmt.Errorf("save theme enabled state: %w", err)
	}
	entry.row = next
	return themeStoreCatalogEntryFromMirror(entry), nil
}

// Delete removes an installed theme. The site default reference blocks deletion
// (409 in the API). Personal preferences referencing the theme are reset to the
// explicit builtin theme with fresh revisions in the same database transaction,
// so a stale client form cannot resurrect the deleted selection. The builtin
// theme is never deletable.
func (s *ThemeStore) Delete(themeID string, expectedRevision int64) error {
	if s == nil {
		return ErrThemeStoreUnavailable
	}
	if themeID == ThemeIDBuiltin {
		return ErrThemeBuiltin
	}
	if err := themeStoreValidCustomID(themeID); err != nil {
		return err
	}
	if expectedRevision < 0 || expectedRevision > ThemeMaxRevision {
		return fmt.Errorf("%w: expected revision out of range", ErrThemeInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.liveLocked(); err != nil {
		return err
	}
	entry := s.entries[themeID]
	if entry == nil {
		return fmt.Errorf("%w: %s", ErrThemeNotFound, themeID)
	}
	if entry.row.Revision != expectedRevision {
		return fmt.Errorf("%w: theme %s revision changed", ErrThemeRevisionConflict, themeID)
	}
	if s.site.ThemeID != nil && *s.site.ThemeID == themeID {
		return fmt.Errorf("%w: %s", ErrThemeInUse, themeID)
	}
	// Commit the database index first. Filesystem and SQLite cannot share one
	// transaction, so the surviving side must be the harmless one: after this
	// transaction the theme is logically deleted (not served and not
	// selectable) even if the directories below still exist. A crash before or
	// during physical cleanup leaves only unreferenced strict hash directories;
	// startup recovery removes them without following links, and Catalog quota
	// keeps counting their bytes until then. Doing it the other way around would
	// let a failed database transaction destroy the only recoverable copy.
	hashes := make([]string, 0, 2)
	if entry.current != nil {
		hashes = append(hashes, entry.current.ContentHash)
	}
	if entry.previous != nil && entry.previous.ContentHash != entry.current.ContentHash {
		hashes = append(hashes, entry.previous.ContentHash)
	}
	err := s.db.withWriteTx(func() error {
		stored, found, err := themeStoreReadCatalogRow(s.db, themeID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("%w: %s disappeared", ErrThemeNotFound, themeID)
		}
		if stored.Revision != expectedRevision {
			return fmt.Errorf("%w: theme %s revision changed", ErrThemeRevisionConflict, themeID)
		}
		site, found, err := themeStoreReadSite(s.db)
		if err != nil {
			return err
		}
		if found && site.ThemeID != nil && *site.ThemeID == themeID {
			return fmt.Errorf("%w: %s", ErrThemeInUse, themeID)
		}
		if err := themeStoreResetPreferencesForTheme(s.db, themeID); err != nil {
			return err
		}
		if err := themeStoreDeleteCatalogRow(s.db, themeID); err != nil {
			return err
		}
		for _, hash := range hashes {
			if err := themeStoreDeleteVersion(s.db, hash); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		// Nothing was removed from disk or from the in-memory index: the theme,
		// its site/preference references and its resources remain exactly as
		// before the failed transaction.
		return fmt.Errorf("delete theme %s: %w", themeID, err)
	}

	// The delete is committed. Drop the logical index before physical cleanup
	// so concurrent readers can only observe 404, never a partially removed
	// tree. Resource reads resolve through this index rather than the path.
	delete(s.entries, themeID)
	for _, hash := range hashes {
		delete(s.versions, hash)
	}
	for _, hash := range hashes {
		if err := themeStoreDeleteRemoveTree(s.root, themeStoreVersionDir(s.root, hash)); err != nil {
			// The logical delete succeeded. Report the pending cleanup explicitly;
			// the bytes below s.root still count toward the Catalog quota because it
			// measures the whole resource tree, not just registered versions.
			s.noteProblemLocked("delete %s: database committed but resource cleanup pending for %s: %v", themeID, hash, err)
		}
	}
	return nil
}
