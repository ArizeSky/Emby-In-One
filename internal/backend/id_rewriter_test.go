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
 
 // TestRewriteSkipsNonRouteIDFields pins that non-route ID fields pass through
 // untouched: rewriting them minted a persistent id_mappings row per value that
 // no route could ever resolve back, growing the store without bound.
 // Route-relevant fields (Id, SeriesId, ...) keep being rewritten.
 func TestRewriteSkipsNonRouteIDFields(t *testing.T) {
 	store, err := NewIDStore(t.TempDir(), nil)
 	if err != nil {
 		t.Fatalf("NewIDStore: %v", err)
 	}
 	t.Cleanup(func() { _ = store.Close() })
 
 	payload := map[string]any{
 		"Id":                    "item-1",
 		"SeriesId":              "series-1",
 		"PresentationUniqueKey": "tmdb-12345-series",
 		"RemoteId":              "imdb:tt0000001",
 		"InternalId":            "123456",
 		"StreamId":              "stream-9",
 		"BaseItemId":            "base-7",
 		"CollectionItemId":      "ci-8",
 		"LibraryItemId":         "lib-9",
 	}
 	rewritten := rewriteResponseIDs(payload, "srv-0", store, "proxy-server", "proxy-user").(map[string]any)
 
 	for key, want := range map[string]string{
 		"PresentationUniqueKey": "tmdb-12345-series",
 		"RemoteId":              "imdb:tt0000001",
 		"InternalId":            "123456",
 		"StreamId":              "stream-9",
 		"BaseItemId":            "base-7",
 		"CollectionItemId":      "ci-8",
 		"LibraryItemId":         "lib-9",
 	} {
 		if got, _ := rewritten[key].(string); got != want {
 			t.Fatalf("%s = %q, want the original value %q", key, got, want)
 		}
 	}
 
 	// Route fields are still rewritten and still resolvable.
 	virtualItem, _ := rewritten["Id"].(string)
 	if virtualItem == "" || virtualItem == "item-1" {
 		t.Fatalf("Id = %q, want a virtual ID", virtualItem)
 	}
 	virtualSeries, _ := rewritten["SeriesId"].(string)
 	if virtualSeries == "" || virtualSeries == "series-1" {
 		t.Fatalf("SeriesId = %q, want a virtual ID", virtualSeries)
 	}
 	if resolved := store.ResolveVirtualID(virtualItem); resolved == nil || resolved.OriginalID != "item-1" {
 		t.Fatalf("virtual Id did not resolve back to item-1: %#v", resolved)
 	}
 
 	// Exactly the two route fields were minted, nothing else.
 	if got := store.Stats().MappingCount; got != 2 {
 		t.Fatalf("mapping count = %d, want 2 (Id + SeriesId only)", got)
 	}
 }
