package backend

import (
	"fmt"
	"testing"
)

func TestRemoveByServerIDPromotesSurvivorAndRehomesWatchState(t *testing.T) {
	dir := t.TempDir()
	store, err := NewIDStore(dir, nil)
	if err != nil {
		t.Fatalf("NewIDStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	watch, err := NewWatchStore(store.DB(), nil)
	if err != nil {
		t.Fatalf("NewWatchStore: %v", err)
	}

	virtualID := store.GetOrCreateVirtualID("movie-a", "srv-a")
	store.GetOrCreateVirtualID("movie-b", "srv-b") // independently published alias
	store.AssociateAdditionalInstance(virtualID, "movie-b", "srv-b")
	seriesID := store.GetOrCreateVirtualID("series-a", "srv-a")
	store.AssociateAdditionalInstance(seriesID, "series-b", "srv-b")
	if err := watch.RecordProgress(&WatchProgress{
		ProxyUserID: "user", VirtualItemID: virtualID, ServerID: "srv-a", OriginalItemID: "movie-a",
		ItemType: "Episode", SeriesVirtualID: seriesID, SeriesOriginalID: "series-a",
		PositionTicks: 1234, RuntimeTicks: 9000, Played: false,
	}); err != nil {
		t.Fatalf("RecordProgress: %v", err)
	}
	if err := watch.SetFavorite("user", virtualID, true); err != nil {
		t.Fatalf("SetFavorite: %v", err)
	}
	store.SetImageSource(virtualID, "Primary", "poster-a", "srv-a", "movie-a")
	store.SetImageSource(virtualID, "Primary", "poster-b", "srv-b", "movie-b")
	store.SetImageSource(virtualID, "Backdrop", "backdrop-b", "srv-b", "movie-b")
	store.SetDisplaySource(virtualID, "srv-a", "movie-a")
	store.SetActiveStream(virtualID, "srv-a")

	if err := store.RemoveByServerID("srv-a"); err != nil {
		t.Fatalf("RemoveByServerID: %v", err)
	}
	resolved := store.ResolveVirtualID(virtualID)
	if resolved == nil || resolved.ServerID != "srv-b" || resolved.OriginalID != "movie-b" {
		t.Fatalf("primary copy was not promoted under the existing virtual ID: %+v", resolved)
	}
	if got := store.ResolveVirtualID(seriesID); got == nil || got.ServerID != "srv-b" || got.OriginalID != "series-b" {
		t.Fatalf("series identity was not promoted: %+v", got)
	}
	if got := store.AliasVirtualIDs(virtualID); len(got) != 2 {
		t.Fatalf("published aliases were not preserved: %v", got)
	}
	if got := store.ResolveByOriginalID("movie-a"); got != nil {
		t.Fatalf("removed upstream ID still resolves: %+v", got)
	}
	progress, err := watch.GetProgress("user", virtualID)
	if err != nil || progress == nil {
		t.Fatalf("GetProgress = (%+v, %v)", progress, err)
	}
	if progress.ServerID != "srv-b" || progress.OriginalItemID != "movie-b" || progress.SeriesOriginalID != "series-b" ||
		progress.PositionTicks != 1234 || !progress.IsFavorite {
		t.Fatalf("watch state did not follow surviving identity: %+v", progress)
	}
	if _, ok := store.ResolveImageSource(virtualID, "Primary", "poster-a"); ok {
		t.Fatal("image route still references deleted server")
	}
	if got, ok := store.ResolveImageSource(virtualID, "Primary", "poster-b"); !ok || got.ServerID != "srv-b" || got.OriginalID != "movie-b" {
		t.Fatalf("surviving poster source = (%+v, %v)", got, ok)
	}
	if got, ok := store.ResolveImageSource(virtualID, "Backdrop", "backdrop-b"); !ok || got.ServerID != "srv-b" {
		t.Fatalf("surviving backdrop source = (%+v, %v)", got, ok)
	}
	if _, ok := store.ResolveDisplaySource(virtualID); ok {
		t.Fatal("display source still references deleted server")
	}
	if got, ok := store.GetActiveStream(virtualID); ok {
		t.Fatalf("deleted server remained an active stream hint: %q", got)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("close before restart: %v", err)
	}
	store, err = NewIDStore(dir, nil)
	if err != nil {
		t.Fatalf("restart NewIDStore: %v", err)
	}
	watch, err = NewWatchStore(store.DB(), nil)
	if err != nil {
		t.Fatalf("restart NewWatchStore: %v", err)
	}
	resolved = store.ResolveVirtualID(virtualID)
	if resolved == nil || resolved.ServerID != "srv-b" || resolved.OriginalID != "movie-b" {
		t.Fatalf("restart lost promoted identity: %+v", resolved)
	}
	if got := store.AliasVirtualIDs(virtualID); len(got) != 2 {
		t.Fatalf("restart lost aliases: %v", got)
	}
	progress, err = watch.GetProgress("user", virtualID)
	if err != nil || progress == nil || progress.ServerID != "srv-b" || progress.PositionTicks != 1234 || !progress.IsFavorite {
		t.Fatalf("restart lost rehomed watch state: state=%+v err=%v", progress, err)
	}
}

func TestRemoveByServerIDFailureLeavesMemoryAndDatabaseUnchanged(t *testing.T) {
	dir := t.TempDir()
	store, err := NewIDStore(dir, nil)
	if err != nil {
		t.Fatalf("NewIDStore: %v", err)
	}
	defer store.Close()
	watch, err := NewWatchStore(store.DB(), nil)
	if err != nil {
		t.Fatalf("NewWatchStore: %v", err)
	}
	virtualID := store.GetOrCreateVirtualID("movie-a", "srv-a")
	store.AssociateAdditionalInstance(virtualID, "movie-b", "srv-b")
	if err := watch.RecordProgress(&WatchProgress{
		ProxyUserID: "user", VirtualItemID: virtualID, ServerID: "srv-a", OriginalItemID: "movie-a", PositionTicks: 55,
	}); err != nil {
		t.Fatalf("RecordProgress: %v", err)
	}
	store.SetImageSource(virtualID, "Primary", "poster-a", "srv-a", "movie-a")
	store.SetActiveStream(virtualID, "srv-a")
	if err := store.DB().exec(fmt.Sprintf(`
		CREATE TRIGGER reject_server_promotion BEFORE UPDATE OF server_id ON id_mappings
		WHEN OLD.virtual_id = '%s' BEGIN SELECT RAISE(ABORT, 'forced promotion failure'); END;`, virtualID)); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}

	if err := store.RemoveByServerID("srv-a"); err == nil {
		t.Fatal("expected transaction failure")
	}
	resolved := store.ResolveVirtualID(virtualID)
	if resolved == nil || resolved.ServerID != "srv-a" || resolved.OriginalID != "movie-a" || len(resolved.OtherInstances) != 1 || resolved.OtherInstances[0].ServerID != "srv-b" {
		t.Fatalf("failed removal mutated in-memory identity: %+v", resolved)
	}
	if got, ok := store.ResolveImageSource(virtualID, "Primary", "poster-a"); !ok || got.ServerID != "srv-a" {
		t.Fatalf("failed removal mutated image hints: source=%+v ok=%v", got, ok)
	}
	if got, ok := store.GetActiveStream(virtualID); !ok || got != "srv-a" {
		t.Fatalf("failed removal mutated active-stream hint: server=%q ok=%v", got, ok)
	}
	progress, err := watch.GetProgress("user", virtualID)
	if err != nil || progress == nil || progress.ServerID != "srv-a" || progress.OriginalItemID != "movie-a" || progress.PositionTicks != 55 {
		t.Fatalf("failed transaction changed watch row: state=%+v err=%v", progress, err)
	}

	// A second store proves the rolled-back state is also the durable state.
	diskStore, err := NewIDStore(dir, nil)
	if err != nil {
		t.Fatalf("reload store after rollback: %v", err)
	}
	defer diskStore.Close()
	diskResolved := diskStore.ResolveVirtualID(virtualID)
	if diskResolved == nil || diskResolved.ServerID != "srv-a" || len(diskResolved.OtherInstances) != 1 || diskResolved.OtherInstances[0].ServerID != "srv-b" {
		t.Fatalf("failed transaction changed durable identity: %+v", diskResolved)
	}
	diskWatch, err := NewWatchStore(diskStore.DB(), nil)
	if err != nil {
		t.Fatalf("reload watch store after rollback: %v", err)
	}
	diskProgress, err := diskWatch.GetProgress("user", virtualID)
	if err != nil || diskProgress == nil || diskProgress.ServerID != "srv-a" || diskProgress.OriginalItemID != "movie-a" {
		t.Fatalf("failed transaction changed durable watch row: state=%+v err=%v", diskProgress, err)
	}
}

func TestRemoveByServerIDSecondaryKeepsPrimary(t *testing.T) {
	store, err := NewIDStore(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewIDStore: %v", err)
	}
	defer store.Close()
	virtualID := store.GetOrCreateVirtualID("movie-a", "srv-a")
	store.AssociateAdditionalInstance(virtualID, "movie-b", "srv-b")
	if err := store.RemoveByServerID("srv-b"); err != nil {
		t.Fatalf("RemoveByServerID: %v", err)
	}
	resolved := store.ResolveVirtualID(virtualID)
	if resolved == nil || resolved.ServerID != "srv-a" || resolved.OriginalID != "movie-a" || len(resolved.OtherInstances) != 0 {
		t.Fatalf("deleting a secondary changed the primary: %+v", resolved)
	}
}
