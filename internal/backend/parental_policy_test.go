package backend

import "testing"

func TestItemAllowedByPolicy(t *testing.T) {
	// Unrated items are allowed unless the user asked to block unrated content.
	unrated := map[string]any{"Name": "no rating"}
	if !itemAllowedByPolicy(unrated, 7, false) {
		t.Error("unrated item must be allowed when blockUnrated is false")
	}
	if itemAllowedByPolicy(unrated, 7, true) {
		t.Error("unrated item must be blocked when blockUnrated is true")
	}
	// A non-empty CustomRating overrides OfficialRating (plan §8.3).
	both := map[string]any{"OfficialRating": "PG", "CustomRating": "R"}
	if itemAllowedByPolicy(both, 7, false) {
		t.Error("CustomRating R must override OfficialRating PG at ceiling 7")
	}
	// A rated item within the ceiling passes even when blockUnrated is set.
	if !itemAllowedByPolicy(map[string]any{"OfficialRating": "PG"}, 7, true) {
		t.Error("rated PG item must pass at ceiling 7 even when blockUnrated")
	}
	// A rated item above the ceiling fails.
	if itemAllowedByPolicy(map[string]any{"OfficialRating": "R"}, 7, false) {
		t.Error("R item must fail at ceiling 7")
	}
}

func TestUserStoreBlockUnratedRoundtrip(t *testing.T) {
	store := newTestUserStore(t)
	u, err := store.Create("child", "password123", nil)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.SetParentalPolicy(u.ID, 7, true); err != nil {
		t.Fatalf("SetParentalPolicy: %v", err)
	}
	got := store.Get(u.ID)
	if got == nil || got.MaxParentalRating != 7 || !got.BlockUnratedItems {
		t.Fatalf("policy not stored: %#v", got)
	}
	// SetMaxParentalRating must change only the ceiling, not the block flag.
	if err := store.SetMaxParentalRating(u.ID, 5); err != nil {
		t.Fatalf("SetMaxParentalRating: %v", err)
	}
	got = store.Get(u.ID)
	if got.MaxParentalRating != 5 || !got.BlockUnratedItems {
		t.Fatalf("SetMaxParentalRating disturbed block flag: %#v", got)
	}
}
