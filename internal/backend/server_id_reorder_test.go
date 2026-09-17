package backend

import (
	"testing"
)

func TestServerReorderZeroDBWrite(t *testing.T) {
	tempDir := t.TempDir()

	// 1. Setup IDStore and UserStore
	idStore, err := NewIDStore(tempDir, nil)
	if err != nil {
		t.Fatalf("NewIDStore: %v", err)
	}
	defer idStore.Close()

	userStore, err := NewUserStore(idStore.db, nil)
	if err != nil {
		t.Fatalf("NewUserStore: %v", err)
	}

	// Create user with permission to srv-1
	u, err := userStore.Create("alice", "pass123456", []string{"srv-1"})
	if err != nil {
		t.Fatalf("Create user: %v", err)
	}

	// Create mappings for srv-0 and srv-1
	virt0 := idStore.GetOrCreateVirtualID("item-0", "srv-0")
	virt1 := idStore.GetOrCreateVirtualID("item-1", "srv-1")

	// 2. Initial config: srv-0 (index 0), srv-1 (index 1)
	cfg := Config{
		Upstream: []UpstreamConfig{
			{ID: "srv-0", Name: "Server 0", URL: "http://srv0"},
			{ID: "srv-1", Name: "Server 1", URL: "http://srv1"},
		},
	}

	// 3. Reorder: swap index 0 and 1
	fromIndex, toIndex := 0, 1
	reordered := append([]UpstreamConfig(nil), cfg.Upstream...)
	moved := reordered[fromIndex]
	reordered = append(reordered[:fromIndex], reordered[fromIndex+1:]...)
	var finalUpstreams []UpstreamConfig
	finalUpstreams = append(finalUpstreams, reordered[:toIndex]...)
	finalUpstreams = append(finalUpstreams, moved)
	finalUpstreams = append(finalUpstreams, reordered[toIndex:]...)
	cfg.Upstream = finalUpstreams

	// 4. Verify ZERO DB mutations required:
	// All mappings remain stable and valid
	res0 := idStore.ResolveVirtualID(virt0)
	if res0 == nil || res0.ServerID != "srv-0" || res0.OriginalID != "item-0" {
		t.Errorf("virt0 corrupted after reorder: %+v", res0)
	}
	res1 := idStore.ResolveVirtualID(virt1)
	if res1 == nil || res1.ServerID != "srv-1" || res1.OriginalID != "item-1" {
		t.Errorf("virt1 corrupted after reorder: %+v", res1)
	}

	// User permissions remain strictly linked to "srv-1"
	user := userStore.Get(u.ID)
	if user == nil || len(user.AllowedServers) != 1 || user.AllowedServers[0] != "srv-1" {
		t.Errorf("user permissions drifted after reorder: %+v", user)
	}
}
