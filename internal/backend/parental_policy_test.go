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

func TestDecideRatingUnrecognizedDiffersFromUnrated(t *testing.T) {
	// A ceiling alone still hides a value the policy table cannot interpret.
	if got := decideRating(0, ratingUnrecognized, 7, false); got != parentalDeny {
		t.Errorf("unrecognized at a ceiling = %v, want deny", got)
	}
	if got := decideRating(0, ratingUnrecognized, 0, true); got != parentalDeny {
		t.Errorf("unrecognized with blockUnrated = %v, want deny", got)
	}
	if got := decideRating(0, ratingUnrecognized, 0, false); got != parentalAllow {
		t.Errorf("unrecognized for a no-policy user = %v, want allow", got)
	}
	// A confirmed-unrated item obeys only the block flag.
	if got := decideRating(0, ratingUnrated, 7, false); got != parentalAllow {
		t.Errorf("unrated at a mere ceiling = %v, want allow", got)
	}
	if got := decideRating(0, ratingUnrated, 7, true); got != parentalDeny {
		t.Errorf("unrated with blockUnrated = %v, want deny", got)
	}
	if got := decideRating(0, ratingMissing, 7, false); got != parentalUnconfirmed {
		t.Errorf("missing = %v, want unconfirmed (no downgrade)", got)
	}
}

func TestItemAllowedByPolicyUnrecognizedOverride(t *testing.T) {
	unknown := map[string]any{"OfficialRating": "PG", "CustomRating": "LOCAL-REGIME-UNKNOWN"}
	if itemAllowedByPolicy(unknown, 7, false) {
		t.Error("unrecognised CustomRating must not inherit the lower OfficialRating at a ceiling")
	}
	if itemAllowedByPolicy(unknown, 0, true) {
		t.Error("blockUnrated alone must refuse an unrecognised override")
	}
	if !itemAllowedByPolicy(unknown, 0, false) {
		t.Error("a no-policy user keeps passing unrecognised values")
	}
}

func TestRatingScoreConcreteAliases(t *testing.T) {
	cases := map[string]int{"PG13": 7, "pg 13": 7, "PG-13": 7, "Rated R": 9, "RATED PG-13": 7, "R (17+)": 9, "fsk": 0}
	for rating, want := range cases {
		got, ok := ratingScore(rating)
		if want == 0 {
			if ok {
				t.Errorf("ratingScore(%q) = %d, want unknown", rating, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("ratingScore(%q) = %d,%v want %d", rating, got, ok, want)
		}
	}
	// Cross-regime values are never scored by scraping digits.
	if _, ok := ratingScore("FSK 18"); ok {
		t.Error("cross-regime value must stay unknown")
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
