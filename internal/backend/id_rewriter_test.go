package backend

import "testing"

// TestRewriteUserDataItemIdMatchesId pins the invariant that a UserData block's ItemId
// maps to the same virtual ID as the sibling top-level Id. The block used to fall through
// to the generic recursion, which minted a second virtual ID for the already-rewritten
// value and left an orphan "virtual id -> virtual id" mapping behind.
func TestRewriteUserDataItemIdMatchesId(t *testing.T) {
	store, err := NewIDStore(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewIDStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	payload := map[string]any{
		"Id":       "orig-item-1",
		"UserData": map[string]any{"ItemId": "orig-item-1", "Played": true},
	}
	out := rewriteResponseIDs(payload, "srv-0", store, "proxy-server", "proxy-user").(map[string]any)

	userData, ok := out["UserData"].(map[string]any)
	if !ok {
		t.Fatalf("UserData = %#v, want map", out["UserData"])
	}
	if out["Id"] != userData["ItemId"] {
		t.Fatalf("Id and UserData.ItemId diverged after rewrite: %v vs %v", out["Id"], userData["ItemId"])
	}
	if !userData["Played"].(bool) {
		t.Fatalf("UserData.Played was not preserved: %#v", userData)
	}

	// A single upstream ID must produce exactly one mapping, never an orphan.
	if got := store.Stats().MappingCount; got != 1 {
		t.Fatalf("mapping count = %d, want 1 (orphan mapping minted)", got)
	}
	if resolved := store.ResolveVirtualID(userData["ItemId"].(string)); resolved == nil || resolved.OriginalID != "orig-item-1" {
		t.Fatalf("UserData.ItemId did not resolve to the original ID: %#v", resolved)
	}
}
