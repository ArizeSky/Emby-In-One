package backend

import (
	"errors"
	"testing"
)

// Preference tests cover the positive seeded revision, rejection of a zero
// expected revision, ABA after delete/recreate, explicit deletion, and the
// reset of references to a deleted custom theme.

func TestThemeStorePreferenceSeedRevisionAndCAS(t *testing.T) {
	store := newThemeStoreTestStore(t, t.TempDir(), newThemeStoreTestDB(t))

	if _, err := store.GetPreference(""); !errors.Is(err, ErrThemeInvalid) {
		t.Fatalf("empty user id error = %v, want ErrThemeInvalid", err)
	}

	seeded, err := store.GetPreference("user-a")
	if err != nil {
		t.Fatalf("seed preference: %v", err)
	}
	if seeded.UserID != "user-a" || seeded.ThemeID != nil || seeded.Mode != ThemeModeDefault {
		t.Fatalf("unexpected seeded preference %#v", seeded)
	}
	if seeded.Revision <= 0 || seeded.Revision > ThemeMaxRevision {
		t.Fatalf("seeded revision %d is not a positive JS-safe value", seeded.Revision)
	}
	again, err := store.GetPreference("user-a")
	if err != nil || again.Revision != seeded.Revision {
		t.Fatalf("second seed changed revision: %#v, %v (first %#v)", again, err, seeded)
	}

	if _, err := store.SavePreference("user-a", nil, ThemeModeLight, 0); !errors.Is(err, ErrThemeRevisionConflict) {
		t.Fatalf("zero expected revision error = %v, want ErrThemeRevisionConflict", err)
	}
	if _, err := store.SavePreference("user-a", nil, ThemeModeLight, -1); !errors.Is(err, ErrThemeRevisionConflict) {
		t.Fatalf("negative expected revision error = %v, want ErrThemeRevisionConflict", err)
	}
	if _, err := store.SavePreference("user-a", nil, "sepia", seeded.Revision); !errors.Is(err, ErrThemeInvalid) {
		t.Fatalf("invalid mode error = %v, want ErrThemeInvalid", err)
	}
	invalidID := "../escape"
	if _, err := store.SavePreference("user-a", &invalidID, ThemeModeLight, seeded.Revision); !errors.Is(err, ErrThemeInvalid) {
		t.Fatalf("invalid theme id error = %v, want ErrThemeInvalid", err)
	}

	saved, err := store.SavePreference("user-a", nil, ThemeModeLight, seeded.Revision)
	if err != nil {
		t.Fatalf("save preference: %v", err)
	}
	if saved.ThemeID != nil || saved.Mode != ThemeModeLight || saved.Revision == seeded.Revision {
		t.Fatalf("unexpected saved preference %#v", saved)
	}
	if _, err := store.SavePreference("user-a", nil, ThemeModeDark, seeded.Revision); !errors.Is(err, ErrThemeRevisionConflict) {
		t.Fatalf("stale expected revision error = %v, want ErrThemeRevisionConflict", err)
	}

	entry := installThemeStoreTestPackage(t, store, "preference-theme", 8)
	withTheme, err := store.SavePreference("user-a", &entry.ID, ThemeModeSystem, saved.Revision)
	if err != nil {
		t.Fatalf("save custom theme preference: %v", err)
	}
	if withTheme.ThemeID == nil || *withTheme.ThemeID != entry.ID || withTheme.Mode != ThemeModeSystem {
		t.Fatalf("unexpected custom preference %#v", withTheme)
	}
	loaded, err := store.GetPreference("user-a")
	if err != nil || loaded.Revision != withTheme.Revision || loaded.ThemeID == nil ||
		*loaded.ThemeID != entry.ID || loaded.Mode != ThemeModeSystem {
		t.Fatalf("stored preference = %#v, %v (saved %#v)", loaded, err, withTheme)
	}
	unknown := "missing-theme"
	if _, err := store.SavePreference("user-a", &unknown, ThemeModeDark, loaded.Revision); !errors.Is(err, ErrThemeNotFound) {
		t.Fatalf("unknown theme preference error = %v, want ErrThemeNotFound", err)
	}
}

func TestThemeStorePreferenceDeleteIsIdempotentAndRejectsOldRevision(t *testing.T) {
	store := newThemeStoreTestStore(t, t.TempDir(), newThemeStoreTestDB(t))

	other, err := store.GetPreference("user-other")
	if err != nil {
		t.Fatalf("seed other preference: %v", err)
	}
	seeded, err := store.GetPreference("user-a")
	if err != nil {
		t.Fatalf("seed preference: %v", err)
	}
	saved, err := store.SavePreference("user-a", nil, ThemeModeLight, seeded.Revision)
	if err != nil {
		t.Fatalf("save preference: %v", err)
	}
	if err := store.DeleteUserPreferences("user-a"); err != nil {
		t.Fatalf("delete preference: %v", err)
	}
	if err := store.DeleteUserPreferences("user-a"); err != nil {
		t.Fatalf("second delete must be idempotent: %v", err)
	}
	if err := store.DeleteUserPreferences(""); !errors.Is(err, ErrThemeInvalid) {
		t.Fatalf("delete invalid user error = %v, want ErrThemeInvalid", err)
	}
	unchanged, err := store.GetPreference("user-other")
	if err != nil || unchanged.Revision != other.Revision || unchanged.ThemeID != nil {
		t.Fatalf("deleting one user changed another: %#v, %v (was %#v)", unchanged, err, other)
	}

	recreated, err := store.GetPreference("user-a")
	if err != nil {
		t.Fatalf("recreate preference: %v", err)
	}
	if recreated.Revision <= 0 || recreated.Revision == saved.Revision || recreated.ThemeID != nil ||
		recreated.Mode != ThemeModeDefault {
		t.Fatalf("recreated preference %#v did not start from a fresh revision (was %#v)", recreated, saved)
	}
	if _, err := store.SavePreference("user-a", nil, ThemeModeDark, saved.Revision); !errors.Is(err, ErrThemeRevisionConflict) {
		t.Fatalf("deleted revision was accepted again (ABA): %v", err)
	}
	if _, err := store.SavePreference("user-a", nil, ThemeModeDark, recreated.Revision); err != nil {
		t.Fatalf("save with recreated revision: %v", err)
	}
}

func TestThemeDeleteResetsPersonalReferencesToBuiltinAndKeepsFollowSiteNil(t *testing.T) {
	store := newThemeStoreTestStore(t, t.TempDir(), newThemeStoreTestDB(t))
	entry := installThemeStoreTestPackage(t, store, "delete-preference-theme", 8)

	follow, err := store.GetPreference("user-follow")
	if err != nil {
		t.Fatalf("seed follow-site preference: %v", err)
	}
	custom, err := store.GetPreference("user-custom")
	if err != nil {
		t.Fatalf("seed custom preference: %v", err)
	}
	saved, err := store.SavePreference("user-custom", &entry.ID, ThemeModeSystem, custom.Revision)
	if err != nil || saved.ThemeID == nil || *saved.ThemeID != entry.ID {
		t.Fatalf("save custom preference = %#v, %v", saved, err)
	}

	if err := store.Delete(entry.ID, entry.Revision); err != nil {
		t.Fatalf("delete referenced theme: %v", err)
	}

	followAfter, err := store.GetPreference("user-follow")
	if err != nil {
		t.Fatalf("load follow-site preference: %v", err)
	}
	if followAfter.ThemeID != nil || followAfter.Revision != follow.Revision {
		t.Fatalf("follow-site preference changed on theme delete: %#v (was %#v)", followAfter, follow)
	}

	customAfter, err := store.GetPreference("user-custom")
	if err != nil {
		t.Fatalf("load reset preference: %v", err)
	}
	if customAfter.ThemeID == nil || *customAfter.ThemeID != ThemeIDBuiltin {
		t.Fatalf("deleted custom reference = %#v, want explicit %q", customAfter, ThemeIDBuiltin)
	}
	if stored := themeStoreTestStoredPreferenceThemeID(t, store.db, "user-custom"); stored != ThemeIDBuiltin {
		t.Fatalf("stored preference theme_id = %q, want %q", stored, ThemeIDBuiltin)
	}
	if customAfter.Mode != ThemeModeSystem {
		t.Fatalf("reset preference lost its mode: %#v", customAfter)
	}
	if customAfter.Revision <= 0 || customAfter.Revision == saved.Revision {
		t.Fatalf("reset preference revision was not refreshed: %#v (was %#v)", customAfter, saved)
	}
	if _, err := store.SavePreference("user-custom", nil, ThemeModeSystem, saved.Revision); !errors.Is(err, ErrThemeRevisionConflict) {
		t.Fatalf("stale preference saved over reset: %v, want ErrThemeRevisionConflict", err)
	}
	if _, err := store.SavePreference("user-custom", &entry.ID, ThemeModeSystem, customAfter.Revision); !errors.Is(err, ErrThemeNotFound) {
		t.Fatalf("deleted theme can still be selected: %v", err)
	}
}
