package backend

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Startup recovery.
//
// The database is the source of truth for the catalog, but the filesystem can
// be ahead of or behind it after a crash (rename done, commit not; commit done,
// directory deletion not). Recovery:
//
//   - loads and re-validates every referenced version descriptor;
//   - keeps the current version when healthy;
//   - promotes the previous version when the current files are missing/corrupt;
//   - marks the theme unavailable (and resets the site default to builtin) when
//     neither version is recoverable, while keeping the catalog row for admin
//     repair;
//   - prunes unreferenced version rows and strict hash/staging orphans without
//     following symlinks, and never deletes unknown files.
//
// It runs once, before the store is published to other goroutines, so it takes
// no store lock; all database access is short and bounded.

type themeStoreRecoveryUpdate struct {
	row              themeStoreCatalogRow
	originalRevision int64
}

// recoverOnOpen rebuilds the in-memory catalog and repairs crash leftovers.
func (s *ThemeStore) recoverOnOpen() error {
	var (
		rows       []themeStoreCatalogRow
		allHashes  []string
		versionMap map[string]*themeStoreVersion
		site       ThemeSiteSettings
		siteFound  bool
	)
	corrupt := make(map[string]error)
	versionMap = make(map[string]*themeStoreVersion)

	err := s.db.withReadLock(func() error {
		var err error
		rows, err = themeStoreReadCatalogRows(s.db)
		if err != nil {
			return err
		}
		allHashes, err = themeStoreReadVersionHashes(s.db)
		if err != nil {
			return err
		}
		for _, hash := range allHashes {
			version, found, err := themeStoreReadVersion(s.db, hash)
			if err != nil {
				if errors.Is(err, ErrThemeCorrupt) {
					corrupt[hash] = err
					continue
				}
				return err
			}
			if found {
				versionMap[hash] = version
			}
		}
		site, siteFound, err = themeStoreReadSite(s.db)
		return err
	})
	if err != nil {
		return fmt.Errorf("theme store recovery read: %w", err)
	}
	if !siteFound {
		// initSchemaAndSite guarantees the row; a missing row here means the
		// database was replaced underneath us. Fail explicitly.
		return fmt.Errorf("%w: site settings row is missing", ErrThemeStoreUnavailable)
	}

	entries := make(map[string]*themeStoreEntry, len(rows))
	updates := make(map[string]themeStoreRecoveryUpdate)
	referenced := make(map[string]struct{})
	missing := 0
	recovered := 0

	for _, original := range rows {
		row := original
		entry := &themeStoreEntry{row: row}
		current := s.recoveryVersion(versionMap, corrupt, row.ID, row.CurrentHash)
		previous := s.recoveryVersion(versionMap, corrupt, row.ID, row.PreviousHash)
		if current != nil {
			if err := themeStoreVersionFilesValid(s.root, current); err != nil {
				corrupt[current.ContentHash] = err
				noteThemeRecoveryProblem(s, "theme %s current version %s is unusable: %v",
					row.ID, current.ContentHash, err)
				current = nil
			}
		}
		if previous != nil {
			if err := themeStoreVersionFilesValid(s.root, previous); err != nil {
				corrupt[previous.ContentHash] = err
				noteThemeRecoveryProblem(s, "theme %s previous version %s is unusable: %v",
					row.ID, previous.ContentHash, err)
				previous = nil
			}
		}
		switch {
		case current != nil:
			entry.available = true
			entry.current = current
			entry.previous = previous
			referenced[current.ContentHash] = struct{}{}
			if previous != nil {
				referenced[previous.ContentHash] = struct{}{}
			} else if row.PreviousHash != "" {
				row.PreviousHash = ""
				updates[row.ID] = themeStoreRecoveryUpdate{row: row, originalRevision: original.Revision}
			}
		case previous != nil:
			// The live version is gone but the previous version is intact:
			// promote it and give clients a fresh revision so a stale page
			// reloads the resources.
			row.CurrentHash = previous.ContentHash
			row.PreviousHash = ""
			row.Revision = nextThemeRevision(original.Revision)
			entry.available = true
			entry.current = previous
			entry.previous = nil
			entry.row = row
			referenced[previous.ContentHash] = struct{}{}
			updates[row.ID] = themeStoreRecoveryUpdate{row: row, originalRevision: original.Revision}
			recovered++
			noteThemeRecoveryProblem(s, "theme %s was rolled back to previous version %s", row.ID, previous.ContentHash)
		default:
			row.CurrentHash = ""
			row.PreviousHash = ""
			entry.available = false
			entry.current = nil
			entry.previous = nil
			entry.row = row
			entry.problem = "theme resources are unavailable"
			updates[row.ID] = themeStoreRecoveryUpdate{row: row, originalRevision: original.Revision}
			missing++
			noteThemeRecoveryProblem(s, "theme %s has no recoverable version", row.ID)
		}
		entry.row = row
		entries[row.ID] = entry
	}

	// Reset a site default that points at a missing or unavailable theme back
	// to the builtin theme with a fresh revision.
	nextSite := cloneThemeSiteSettings(site)
	siteReset := false
	if site.ThemeID != nil {
		target := entries[*site.ThemeID]
		if target == nil || !target.available || target.current == nil {
			nextSite = ThemeSiteSettings{Revision: nextThemeRevision(site.Revision)}
			siteReset = true
			recovered++
			noteThemeRecoveryProblem(s, "site default %s is unavailable; reset to builtin", *site.ThemeID)
		}
	}

	// Only referenced versions survive; every other catalog/version row is
	// pruned below so a crashed delete cannot be resurrected.
	prune := make([]string, 0)
	for _, hash := range allHashes {
		if _, keep := referenced[hash]; !keep {
			prune = append(prune, hash)
		}
	}
	sort.Strings(prune)

	err = s.db.withWriteTx(func() error {
		catalogIDs := make([]string, 0, len(updates))
		for id := range updates {
			catalogIDs = append(catalogIDs, id)
		}
		sort.Strings(catalogIDs)
		for _, id := range catalogIDs {
			update := updates[id]
			stored, found, err := themeStoreReadCatalogRow(s.db, id)
			if err != nil {
				return err
			}
			if !found || stored.Revision != update.originalRevision {
				noteThemeRecoveryProblem(s, "theme %s changed during recovery; keeping stored row", id)
				continue
			}
			if err := themeStoreUpsertCatalogRow(s.db, update.row); err != nil {
				return err
			}
		}
		if siteReset {
			stored, found, err := themeStoreReadSite(s.db)
			if err != nil {
				return err
			}
			if found && stored.Revision == site.Revision {
				if err := themeStoreWriteSite(s.db, nextSite); err != nil {
					return err
				}
			} else {
				nextSite = stored
			}
		}
		for _, hash := range prune {
			if err := themeStoreDeleteVersion(s.db, hash); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("theme store recovery write: %w", err)
	}

	s.entries = entries
	s.versions = make(map[string]*themeStoreVersion, len(referenced))
	for hash := range referenced {
		if version := versionMap[hash]; version != nil {
			s.versions[hash] = version
		}
	}
	s.site = cloneThemeSiteSettings(nextSite)
	s.health.Available = true
	s.health.Recovered = recovered
	s.health.Missing = missing

	purgedDirs, purgedBytes, purgeErr := s.themeStorePurgeUnreferencedLocked()
	if purgeErr != nil {
		noteThemeRecoveryProblem(s, "theme orphan cleanup failed: %v", purgeErr)
	}
	s.health.OrphanDirs = purgedDirs
	s.health.OrphanBytes = purgedBytes
	return nil
}

// recoveryVersion returns the validated version for a referenced hash, or nil
// when the hash is empty, unknown, corrupt or belongs to another theme.
func (s *ThemeStore) recoveryVersion(versions map[string]*themeStoreVersion, corrupt map[string]error, themeID, hash string) *themeStoreVersion {
	if hash == "" {
		return nil
	}
	if err, bad := corrupt[hash]; bad {
		noteThemeRecoveryProblem(s, "theme %s version %s metadata is invalid: %v", themeID, hash, err)
		return nil
	}
	version := versions[hash]
	if version == nil {
		noteThemeRecoveryProblem(s, "theme %s references missing version %s", themeID, hash)
		return nil
	}
	if version.ThemeID != themeID {
		noteThemeRecoveryProblem(s, "theme %s version %s belongs to theme %s", themeID, hash, version.ThemeID)
		return nil
	}
	return version
}

func noteThemeRecoveryProblem(s *ThemeStore, format string, args ...any) {
	if s == nil {
		return
	}
	s.noteProblemLocked(format, args...)
}

// themeStoreScanThemeDirs is a small diagnostic helper used by tests and
// recovery to list strict version directories currently on disk.
func themeStoreScanThemeDirs(root string) ([]string, error) {
	items, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	hashes := make([]string, 0)
	for _, item := range items {
		name := item.Name()
		if themeStoreHashName(name) {
			hashes = append(hashes, filepath.Join(root, name))
		}
	}
	sort.Strings(hashes)
	return hashes, nil
}
