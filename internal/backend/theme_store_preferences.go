package backend

import (
	"fmt"
)

// Per-user theme preferences.
//
// userID is only a store key: HTTP handlers must derive it from the
// authenticated request context, never from a body field. The store keeps no
// token or upstream credential. Preferences are never returned to anonymous
// callers.
//
// GetPreference atomically materializes the row with a random non-zero
// revision. SavePreference then always requires a positive expected revision,
// so deleting and recreating a preference row cannot make an old form valid
// again (ABA), and a zero/replayed expectation is always rejected.

func themeStoreReadPreference(db *sqliteDB, userID string) (ThemePreference, bool, error) {
	stmt, err := db.prepare(`SELECT user_id, theme_id, mode, revision FROM theme_preferences WHERE user_id = ?`)
	if err != nil {
		return ThemePreference{}, false, err
	}
	defer stmt.finalize()
	if err := stmt.bindAll(userID); err != nil {
		return ThemePreference{}, false, err
	}
	hasRow, err := stmt.step()
	if err != nil {
		return ThemePreference{}, false, err
	}
	if !hasRow {
		return ThemePreference{}, false, nil
	}
	preference := ThemePreference{
		UserID:   stmt.columnText(0),
		Mode:     stmt.columnText(2),
		Revision: stmt.columnInt64(3),
	}
	if themeID := stmt.columnText(1); themeID != "" {
		value := themeID
		preference.ThemeID = &value
	}
	return preference, true, nil
}

func themeStoreWritePreference(db *sqliteDB, preference ThemePreference) error {
	themeID := ""
	if preference.ThemeID != nil {
		themeID = *preference.ThemeID
	}
	return db.execParams(`INSERT OR REPLACE INTO theme_preferences (user_id, theme_id, mode, revision)
		VALUES (?, ?, ?, ?)`, preference.UserID, themeID, preference.Mode, preference.Revision)
}

// GetPreference returns the user's appearance preference, seeding the default
// row atomically on first access. A missing row is never reported as revision
// 0, because 0 would let a first-create expectation bypass CAS.
func (s *ThemeStore) GetPreference(userID string) (ThemePreference, error) {
	if s == nil {
		return ThemePreference{}, ErrThemeStoreUnavailable
	}
	if err := validateThemeStoreUserID(userID); err != nil {
		return ThemePreference{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.liveLocked(); err != nil {
		return ThemePreference{}, err
	}
	var preference ThemePreference
	err := s.db.withWriteTx(func() error {
		existing, found, err := themeStoreReadPreference(s.db, userID)
		if err != nil {
			return err
		}
		if found {
			if !validThemeMode(existing.Mode) || existing.Revision <= 0 ||
				existing.Revision > ThemeMaxRevision || existing.UserID != userID {
				return fmt.Errorf("%w: stored preference row is invalid", ErrThemeCorrupt)
			}
			preference = existing
			return nil
		}
		preference = ThemePreference{
			UserID:   userID,
			Mode:     ThemeModeDefault,
			Revision: nextThemeRevision(0),
		}
		return themeStoreWritePreference(s.db, preference)
	})
	if err != nil {
		return ThemePreference{}, fmt.Errorf("load theme preference: %w", err)
	}
	return cloneThemePreference(preference), nil
}

// SavePreference updates one authenticated user's appearance selection. The
// row must already exist (GetPreference seeded it) and expectedRevision must be
// a positive revision that matches it; zero, negative and stale values are
// conflicts. themeID nil follows the site default, "builtin" selects the
// builtin theme, and a custom theme must be installed, enabled and available.
func (s *ThemeStore) SavePreference(userID string, themeID *string, mode string, expectedRevision int64) (ThemePreference, error) {
	if s == nil {
		return ThemePreference{}, ErrThemeStoreUnavailable
	}
	if err := validateThemeStoreUserID(userID); err != nil {
		return ThemePreference{}, err
	}
	if !validThemeMode(mode) {
		return ThemePreference{}, fmt.Errorf("%w: mode must be %q, %q or %q",
			ErrThemeInvalid, ThemeModeLight, ThemeModeDark, ThemeModeSystem)
	}
	if expectedRevision <= 0 || expectedRevision > ThemeMaxRevision {
		return ThemePreference{}, fmt.Errorf("%w: expected revision must be a positive opaque value", ErrThemeRevisionConflict)
	}
	normalized, err := themeStoreNormalizePreferenceThemeID(themeID)
	if err != nil {
		return ThemePreference{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.liveLocked(); err != nil {
		return ThemePreference{}, err
	}
	if normalized != nil && *normalized != ThemeIDBuiltin {
		entry := s.entries[*normalized]
		if entry == nil {
			return ThemePreference{}, fmt.Errorf("%w: %s", ErrThemeNotFound, *normalized)
		}
		if !entry.row.Enabled || !entry.available || entry.current == nil {
			return ThemePreference{}, fmt.Errorf("%w: %s", ErrThemeUnavailable, *normalized)
		}
	}
	var saved ThemePreference
	err = s.db.withWriteTx(func() error {
		existing, found, err := themeStoreReadPreference(s.db, userID)
		if err != nil {
			return err
		}
		if !found {
			return fmt.Errorf("%w: preference row was not initialized", ErrThemeRevisionConflict)
		}
		if existing.Revision != expectedRevision {
			return fmt.Errorf("%w: preference revision changed", ErrThemeRevisionConflict)
		}
		saved = ThemePreference{
			UserID:   userID,
			ThemeID:  normalized,
			Mode:     mode,
			Revision: nextThemeRevision(existing.Revision),
		}
		return themeStoreWritePreference(s.db, saved)
	})
	if err != nil {
		return ThemePreference{}, fmt.Errorf("save theme preference: %w", err)
	}
	return cloneThemePreference(saved), nil
}

// DeleteUserPreferences removes one user's preference row for integration with
// user deletion. It is idempotent; the next GetPreference creates a fresh row
// with a new random revision.
func (s *ThemeStore) DeleteUserPreferences(userID string) error {
	if s == nil {
		return ErrThemeStoreUnavailable
	}
	if err := validateThemeStoreUserID(userID); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.liveLocked(); err != nil {
		return err
	}
	if err := s.db.writeParams(`DELETE FROM theme_preferences WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("delete theme preferences: %w", err)
	}
	return nil
}

// themeStoreResetPreferencesForTheme resets every preference that references a
// deleted theme to the explicit builtin theme and bumps each revision, all
// inside the caller's transaction. A nil preference ("follow the site default")
// is left untouched. Callers hold the shared database write lock.
func themeStoreResetPreferencesForTheme(db *sqliteDB, themeID string) error {
	stmt, err := db.prepare(`SELECT user_id, revision FROM theme_preferences WHERE theme_id = ?`)
	if err != nil {
		return err
	}
	if err := stmt.bindAll(themeID); err != nil {
		stmt.finalize()
		return err
	}
	type userRevision struct {
		userID   string
		revision int64
	}
	rows := make([]userRevision, 0)
	for {
		hasRow, err := stmt.step()
		if err != nil {
			stmt.finalize()
			return err
		}
		if !hasRow {
			break
		}
		rows = append(rows, userRevision{userID: stmt.columnText(0), revision: stmt.columnInt64(1)})
	}
	stmt.finalize()
	for _, row := range rows {
		if err := db.execParams(`UPDATE theme_preferences SET theme_id = ?, revision = ? WHERE user_id = ? AND theme_id = ?`,
			ThemeIDBuiltin, nextThemeRevision(row.revision), row.userID, themeID); err != nil {
			return err
		}
	}
	return nil
}
