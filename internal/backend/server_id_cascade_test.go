package backend

import (
	"testing"
)

func TestRemoveByServerIDCascadeClean(t *testing.T) {
	tempDir := t.TempDir()
	store, err := NewIDStore(tempDir, nil)
	if err != nil {
		t.Fatalf("NewIDStore: %v", err)
	}
	defer store.Close()

	// 1. virtA is primarily mapped to srv-A ("movie-1", "srv-A")
	virtA := store.GetOrCreateVirtualID("movie-1", "srv-A")

	// Associate an additional instance from srv-B onto virtA
	store.AssociateAdditionalInstance(virtA, "movie-1-b", "srv-B")

	// 2. virtB is primarily mapped to srv-B ("movie-2", "srv-B")
	virtB := store.GetOrCreateVirtualID("movie-2", "srv-B")

	// Associate an additional instance from srv-A onto virtB
	store.AssociateAdditionalInstance(virtB, "movie-2-a", "srv-A")

	// 3. virtC is purely on srv-C
	virtC := store.GetOrCreateVirtualID("movie-3", "srv-C")

	// Verify all exist before deletion
	if r := store.ResolveVirtualID(virtA); r == nil || len(r.OtherInstances) != 1 {
		t.Fatalf("virtA setup incorrect: %+v", r)
	}
	if r := store.ResolveVirtualID(virtB); r == nil || len(r.OtherInstances) != 1 {
		t.Fatalf("virtB setup incorrect: %+v", r)
	}

	// Delete srv-A
	if err := store.RemoveByServerID("srv-A"); err != nil {
		t.Fatalf("RemoveByServerID(srv-A): %v", err)
	}

	// Verify:
	// a. virtA must be completely gone (primary was on srv-A, and the child instance on srv-B should also be cleaned up!)
	if r := store.ResolveVirtualID(virtA); r != nil {
		t.Errorf("virtA should be removed completely, but got: %+v", r)
	}
	if r := store.ResolveByOriginalID("movie-1"); r != nil {
		t.Errorf("movie-1 on srv-A should not be resolvable, but got: %+v", r)
	}
	if r := store.ResolveByOriginalID("movie-1-b"); r != nil {
		t.Errorf("movie-1-b (orphan additional instance) should not be resolvable, but got: %+v", r)
	}

	// b. virtB must still exist on srv-B, but its additional instance from srv-A must be gone!
	rB := store.ResolveVirtualID(virtB)
	if rB == nil {
		t.Fatalf("virtB should still exist on srv-B")
	}
	if rB.ServerID != "srv-B" || rB.OriginalID != "movie-2" {
		t.Errorf("virtB mismatch: %+v", rB)
	}
	if len(rB.OtherInstances) != 0 {
		t.Errorf("virtB other instances should be empty after srv-A removed, but got: %+v", rB.OtherInstances)
	}

	// c. virtC must be untouched
	rC := store.ResolveVirtualID(virtC)
	if rC == nil || rC.ServerID != "srv-C" {
		t.Errorf("virtC affected unexpectedly: %+v", rC)
	}
}
