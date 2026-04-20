package backend

import (
	"fmt"
	"path/filepath"
	"testing"
)

func newTestWatchStore(t *testing.T) *WatchStore {
	t.Helper()
	dir := t.TempDir()
	logger := NewLogger(LogConfig{Level: "error", FileLevel: "error", DataDir: dir})
	t.Cleanup(func() { logger.Close() })
	db, err := openSQLite(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = closeSQLite(db) })
	ws, err := NewWatchStore(db, logger)
	if err != nil {
		t.Fatalf("new watch store: %v", err)
	}
	return ws
}

func TestWatchStoreRecordAndGetProgress(t *testing.T) {
	ws := newTestWatchStore(t)

	p := &WatchProgress{
		ProxyUserID:    "user1",
		VirtualItemID:  "item1",
		ServerIndex:    0,
		OriginalItemID: "orig1",
		ItemType:       "Movie",
		Name:           "Test Movie",
		PositionTicks:  50000,
		RuntimeTicks:   100000,
	}
	if err := ws.RecordProgress(p); err != nil {
		t.Fatalf("record progress: %v", err)
	}

	got := ws.GetProgress("user1", "item1")
	if got == nil {
		t.Fatalf("expected progress, got nil")
	}
	if got.Name != "Test Movie" {
		t.Fatalf("Name = %q, want %q", got.Name, "Test Movie")
	}
	if got.PositionTicks != 50000 {
		t.Fatalf("PositionTicks = %d, want 50000", got.PositionTicks)
	}
	if got.RuntimeTicks != 100000 {
		t.Fatalf("RuntimeTicks = %d, want 100000", got.RuntimeTicks)
	}

	// Different user sees nothing
	if ws.GetProgress("user2", "item1") != nil {
		t.Fatalf("user2 should not see user1's progress")
	}
}

func TestWatchStoreUpsertPreservesMetadata(t *testing.T) {
	ws := newTestWatchStore(t)

	// First write with full metadata
	p := &WatchProgress{
		ProxyUserID:    "user1",
		VirtualItemID:  "item1",
		ServerIndex:    0,
		OriginalItemID: "orig1",
		ItemType:       "Episode",
		SeriesName:     "The Show",
		Name:           "Pilot",
		PositionTicks:  1000,
		RuntimeTicks:   50000,
	}
	ws.RecordProgress(p)

	// Second write with only position update (empty metadata fields)
	p2 := &WatchProgress{
		ProxyUserID:   "user1",
		VirtualItemID: "item1",
		ServerIndex:   0,
		PositionTicks: 3000,
	}
	ws.RecordProgress(p2)

	got := ws.GetProgress("user1", "item1")
	if got.Name != "Pilot" {
		t.Fatalf("metadata lost: Name = %q, want %q", got.Name, "Pilot")
	}
	if got.SeriesName != "The Show" {
		t.Fatalf("metadata lost: SeriesName = %q", got.SeriesName)
	}
	if got.PositionTicks != 3000 {
		t.Fatalf("PositionTicks = %d, want 3000", got.PositionTicks)
	}
}

func TestWatchStoreUpdatePosition(t *testing.T) {
	ws := newTestWatchStore(t)

	ws.RecordProgress(&WatchProgress{
		ProxyUserID: "user1", VirtualItemID: "item1",
		PositionTicks: 1000, RuntimeTicks: 50000,
	})

	ws.UpdatePosition("user1", "item1", 2500, 50000)

	got := ws.GetProgress("user1", "item1")
	if got.PositionTicks != 2500 {
		t.Fatalf("PositionTicks = %d, want 2500", got.PositionTicks)
	}
}

func TestWatchStoreMarkPlayed(t *testing.T) {
	ws := newTestWatchStore(t)

	ws.RecordProgress(&WatchProgress{
		ProxyUserID: "user1", VirtualItemID: "item1",
		PositionTicks: 1000,
	})

	ws.MarkPlayed("user1", "item1", true)

	got := ws.GetProgress("user1", "item1")
	if !got.Played {
		t.Fatalf("Played = false, want true")
	}

	ws.MarkPlayed("user1", "item1", false)
	got = ws.GetProgress("user1", "item1")
	if got.Played {
		t.Fatalf("Played = true, want false")
	}
}

func TestWatchStoreMarkPlayedUpsertsWithoutPriorRecord(t *testing.T) {
	ws := newTestWatchStore(t)

	ws.MarkPlayed("user1", "item1", true)

	got := ws.GetProgress("user1", "item1")
	if got == nil {
		t.Fatalf("expected progress record after MarkPlayed")
	}
	if !got.Played {
		t.Fatalf("Played = false, want true")
	}
}

func TestWatchStoreSetFavorite(t *testing.T) {
	ws := newTestWatchStore(t)

	// SetFavorite should upsert even without prior record
	ws.SetFavorite("user1", "item1", true)

	got := ws.GetProgress("user1", "item1")
	if got == nil {
		t.Fatalf("expected progress record after SetFavorite")
	}
	if !got.IsFavorite {
		t.Fatalf("IsFavorite = false, want true")
	}

	ws.SetFavorite("user1", "item1", false)
	got = ws.GetProgress("user1", "item1")
	if got.IsFavorite {
		t.Fatalf("IsFavorite = true, want false")
	}
}

func TestWatchStoreGetResumeItems(t *testing.T) {
	ws := newTestWatchStore(t)

	// Item with position > 0 and not played → resume candidate
	ws.RecordProgress(&WatchProgress{
		ProxyUserID: "user1", VirtualItemID: "item1",
		PositionTicks: 100, RuntimeTicks: 1000,
	})
	// Item fully played → should NOT appear in resume
	ws.RecordProgress(&WatchProgress{
		ProxyUserID: "user1", VirtualItemID: "item2",
		PositionTicks: 900, RuntimeTicks: 1000, Played: true,
	})
	// Item with no position → should NOT appear in resume
	ws.RecordProgress(&WatchProgress{
		ProxyUserID: "user1", VirtualItemID: "item3",
		PositionTicks: 0, RuntimeTicks: 1000,
	})

	items, err := ws.GetResumeItems("user1", 10)
	if err != nil {
		t.Fatalf("GetResumeItems: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("len = %d, want 1", len(items))
	}
	if items[0].VirtualItemID != "item1" {
		t.Fatalf("resume item = %q, want item1", items[0].VirtualItemID)
	}
}

func TestRecordProgressUpsertUpdatesPlayedAndFavorite(t *testing.T) {
	ws := newTestWatchStore(t)

	// 1. Insert initial record (played=false, favorite=false, position=1000)
	if err := ws.RecordProgress(&WatchProgress{
		ProxyUserID: "user1", VirtualItemID: "ep1",
		ServerIndex: 0, OriginalItemID: "orig-ep1",
		PositionTicks: 1000, RuntimeTicks: 10000,
	}); err != nil {
		t.Fatalf("initial insert: %v", err)
	}

	got := ws.GetProgress("user1", "ep1")
	if got == nil {
		t.Fatal("expected initial record")
	}
	if got.Played {
		t.Fatal("initial insert should have Played=false")
	}

	// 2. Upsert with played=true (simulating stopped event at 95%)
	if err := ws.RecordProgress(&WatchProgress{
		ProxyUserID: "user1", VirtualItemID: "ep1",
		ServerIndex: 0, OriginalItemID: "orig-ep1",
		PositionTicks: 0, RuntimeTicks: 10000, Played: true,
	}); err != nil {
		t.Fatalf("upsert with played=true: %v", err)
	}

	got = ws.GetProgress("user1", "ep1")
	if !got.Played {
		t.Fatal("Played should be true after upsert with played=true")
	}

	// 3. Another progress event with played=false should NOT revert played=true
	if err := ws.RecordProgress(&WatchProgress{
		ProxyUserID: "user1", VirtualItemID: "ep1",
		ServerIndex: 0, OriginalItemID: "orig-ep1",
		PositionTicks: 500, RuntimeTicks: 10000, Played: false,
	}); err != nil {
		t.Fatalf("progress event: %v", err)
	}

	got = ws.GetProgress("user1", "ep1")
	if !got.Played {
		t.Fatal("Played should remain true after progress event with played=false")
	}
}

func TestWatchStoreGetFavoriteItems(t *testing.T) {
	ws := newTestWatchStore(t)

	ws.SetFavorite("user1", "item1", true)
	ws.SetFavorite("user1", "item2", true)
	ws.SetFavorite("user1", "item3", false)

	items, err := ws.GetFavoriteItems("user1")
	if err != nil {
		t.Fatalf("GetFavoriteItems: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("len = %d, want 2", len(items))
	}
}

func TestWatchStoreNextUpDeduplication(t *testing.T) {
	ws := newTestWatchStore(t)

	// S01E01 played
	ws.RecordProgress(&WatchProgress{
		ProxyUserID: "user1", VirtualItemID: "ep1",
		ItemType: "Episode", SeriesName: "Show A",
		SeriesVirtualID: "series1",
		ParentIndexNumber: 1, IndexNumber: 1,
		Played: true,
	})
	// S01E02 played
	ws.RecordProgress(&WatchProgress{
		ProxyUserID: "user1", VirtualItemID: "ep2",
		ItemType: "Episode", SeriesName: "Show A",
		SeriesVirtualID: "series1",
		ParentIndexNumber: 1, IndexNumber: 2,
		Played: true,
	})
	// Different series S01E01 in progress
	ws.RecordProgress(&WatchProgress{
		ProxyUserID: "user1", VirtualItemID: "ep3",
		ItemType: "Episode", SeriesName: "Show B",
		SeriesVirtualID: "series2",
		ParentIndexNumber: 1, IndexNumber: 1,
		PositionTicks: 500,
	})

	results, err := ws.GetNextUpSeries("user1")
	if err != nil {
		t.Fatalf("GetNextUpSeries: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("len = %d, want 2 (one per series)", len(results))
	}
	// Show A: highest episode is ep2 (S01E02)
	for _, r := range results {
		if r.SeriesName == "Show A" && r.IndexNumber != 2 {
			t.Fatalf("Show A: IndexNumber = %d, want 2", r.IndexNumber)
		}
		if r.SeriesName == "Show B" && r.IndexNumber != 1 {
			t.Fatalf("Show B: IndexNumber = %d, want 1", r.IndexNumber)
		}
	}
}

func TestWatchStoreGetNextUpForSeries(t *testing.T) {
	ws := newTestWatchStore(t)

	ws.RecordProgress(&WatchProgress{
		ProxyUserID: "user1", VirtualItemID: "ep1",
		ItemType: "Episode", SeriesVirtualID: "series1",
		ParentIndexNumber: 1, IndexNumber: 3,
		Played: true,
	})

	got := ws.GetNextUpForSeries("user1", "series1")
	if got == nil {
		t.Fatalf("expected result, got nil")
	}
	if got.IndexNumber != 3 {
		t.Fatalf("IndexNumber = %d, want 3", got.IndexNumber)
	}

	// Non-existent series
	if ws.GetNextUpForSeries("user1", "nonexistent") != nil {
		t.Fatalf("expected nil for non-existent series")
	}
}

func TestWatchStoreDeleteUser(t *testing.T) {
	ws := newTestWatchStore(t)

	ws.RecordProgress(&WatchProgress{
		ProxyUserID: "user1", VirtualItemID: "item1", PositionTicks: 100,
	})
	ws.RecordProgress(&WatchProgress{
		ProxyUserID: "user2", VirtualItemID: "item2", PositionTicks: 200,
	})

	ws.DeleteUser("user1")

	if ws.GetProgress("user1", "item1") != nil {
		t.Fatalf("user1 data should be deleted")
	}
	if ws.GetProgress("user2", "item2") == nil {
		t.Fatalf("user2 data should remain")
	}
}

func TestWatchStoreIsolation(t *testing.T) {
	ws := newTestWatchStore(t)

	// Both users interact with the same item
	ws.RecordProgress(&WatchProgress{
		ProxyUserID: "user1", VirtualItemID: "item1",
		PositionTicks: 1000, RuntimeTicks: 5000,
	})
	ws.RecordProgress(&WatchProgress{
		ProxyUserID: "user2", VirtualItemID: "item1",
		PositionTicks: 2000, RuntimeTicks: 5000,
	})

	p1 := ws.GetProgress("user1", "item1")
	p2 := ws.GetProgress("user2", "item1")

	if p1.PositionTicks != 1000 {
		t.Fatalf("user1 position = %d, want 1000", p1.PositionTicks)
	}
	if p2.PositionTicks != 2000 {
		t.Fatalf("user2 position = %d, want 2000", p2.PositionTicks)
	}

	// Mark played for user1 only
	ws.MarkPlayed("user1", "item1", true)

	p1 = ws.GetProgress("user1", "item1")
	p2 = ws.GetProgress("user2", "item1")
	if !p1.Played {
		t.Fatalf("user1 should be played")
	}
	if p2.Played {
		t.Fatalf("user2 should NOT be played")
	}

	// Favorites are also isolated
	ws.SetFavorite("user2", "item1", true)
	p1 = ws.GetProgress("user1", "item1")
	p2 = ws.GetProgress("user2", "item1")
	if p1.IsFavorite {
		t.Fatalf("user1 should NOT have favorite")
	}
	if !p2.IsFavorite {
		t.Fatalf("user2 should have favorite")
	}
}

func TestMigratePlayedStatus(t *testing.T) {
	dir := t.TempDir()
	logger := NewLogger(LogConfig{Level: "error", FileLevel: "error", DataDir: dir})
	t.Cleanup(func() { logger.Close() })
	db, err := openSQLite(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = closeSQLite(db) })

	// First NewWatchStore creates table + runs migration (no-op on empty table)
	ws, err := NewWatchStore(db, logger)
	if err != nil {
		t.Fatalf("new watch store: %v", err)
	}

	// Insert dirty data: ep-1 is 100% watched but played=0
	if err := ws.RecordProgress(&WatchProgress{
		ProxyUserID: "user1", VirtualItemID: "ep-1",
		ServerIndex: 0, OriginalItemID: "orig-1",
		ItemType: "Episode", SeriesName: "三体",
		PositionTicks: 100, RuntimeTicks: 100,
		Played: false, LastPlayed: 1000,
	}); err != nil {
		t.Fatal(err)
	}
	// ep-2 is 50% watched, played=0 (correct, should NOT be migrated)
	if err := ws.RecordProgress(&WatchProgress{
		ProxyUserID: "user1", VirtualItemID: "ep-2",
		ServerIndex: 0, OriginalItemID: "orig-2",
		ItemType: "Episode", SeriesName: "三体",
		PositionTicks: 50, RuntimeTicks: 100,
		Played: false, LastPlayed: 2000,
	}); err != nil {
		t.Fatal(err)
	}

	// Re-invoke NewWatchStore to trigger migration on existing dirty data
	ws2, err := NewWatchStore(db, logger)
	if err != nil {
		t.Fatalf("second NewWatchStore: %v", err)
	}

	// ep-1: should have been migrated to played=true (100% >= 90%)
	p1 := ws2.GetProgress("user1", "ep-1")
	if p1 == nil || !p1.Played {
		t.Errorf("ep-1: expected played=true (auto-migrated), got played=%v", p1 != nil && p1.Played)
	}
	// ep-2: should remain played=false (50% < 90%)
	p2 := ws2.GetProgress("user1", "ep-2")
	if p2 == nil || p2.Played {
		t.Errorf("ep-2: expected played=false, got played=%v", p2 != nil && p2.Played)
	}
}

func TestGetResumeItemsSeriesGrouping(t *testing.T) {
	dir := t.TempDir()
	logger := NewLogger(LogConfig{Level: "error", FileLevel: "error", DataDir: dir})
	t.Cleanup(func() { logger.Close() })
	db, err := openSQLite(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = closeSQLite(db) })
	ws, err := NewWatchStore(db, logger)
	if err != nil {
		t.Fatalf("new watch store: %v", err)
	}

	// 三体 E1-E5: played (won't appear in resume)
	for i := 1; i <= 5; i++ {
		_ = ws.RecordProgress(&WatchProgress{
			ProxyUserID: "user1", VirtualItemID: fmt.Sprintf("ep-%d", i),
			ServerIndex: 0, OriginalItemID: fmt.Sprintf("orig-%d", i),
			ItemType: "Episode", SeriesName: "三体", SeriesVirtualID: "series-1",
			ParentIndexNumber: 1, IndexNumber: i,
			PositionTicks: 100, RuntimeTicks: 100, Played: true,
		})
	}
	// 三体 E6: 50% progress, not played
	_ = ws.RecordProgress(&WatchProgress{
		ProxyUserID: "user1", VirtualItemID: "ep-6",
		ServerIndex: 0, OriginalItemID: "orig-6",
		ItemType: "Episode", SeriesName: "三体", SeriesVirtualID: "series-1",
		ParentIndexNumber: 1, IndexNumber: 6,
		PositionTicks: 50, RuntimeTicks: 100, Played: false,
	})
	// 三体 E7: 50% progress, not played, more recent
	_ = ws.RecordProgress(&WatchProgress{
		ProxyUserID: "user1", VirtualItemID: "ep-7",
		ServerIndex: 0, OriginalItemID: "orig-7",
		ItemType: "Episode", SeriesName: "三体", SeriesVirtualID: "series-1",
		ParentIndexNumber: 1, IndexNumber: 7,
		PositionTicks: 50, RuntimeTicks: 100, Played: false,
	})
	// Movie A: 50% progress
	_ = ws.RecordProgress(&WatchProgress{
		ProxyUserID: "user1", VirtualItemID: "movie-a",
		ServerIndex: 0, OriginalItemID: "orig-ma",
		ItemType: "Movie", Name: "你的名字",
		PositionTicks: 50, RuntimeTicks: 100, Played: false,
	})
	// Movie B: 30% progress
	_ = ws.RecordProgress(&WatchProgress{
		ProxyUserID: "user1", VirtualItemID: "movie-b",
		ServerIndex: 0, OriginalItemID: "orig-mb",
		ItemType: "Movie", Name: "铃芽之旅",
		PositionTicks: 30, RuntimeTicks: 100, Played: false,
	})

	// Set explicit last_played values for deterministic ordering
	// RecordProgress uses time.Now(), so we override via raw SQL
	db.execParams(`UPDATE user_watch_progress SET last_played = ? WHERE virtual_item_id = ?`, 8000, "movie-a")
	db.execParams(`UPDATE user_watch_progress SET last_played = ? WHERE virtual_item_id = ?`, 7000, "ep-7")
	db.execParams(`UPDATE user_watch_progress SET last_played = ? WHERE virtual_item_id = ?`, 6000, "ep-6")
	db.execParams(`UPDATE user_watch_progress SET last_played = ? WHERE virtual_item_id = ?`, 5000, "movie-b")

	items, err := ws.GetResumeItems("user1", 20)
	if err != nil {
		t.Fatal(err)
	}

	// Expected order by last_played DESC: Movie A (8000), 三体 E7 (7000), Movie B (5000)
	// 三体: only E7 appears (latest), E6 is grouped out
	// E1-E5: played=true, excluded by position>0 && played=0 filter
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d: %+v", len(items), items)
	}
	if items[0].VirtualItemID != "movie-a" {
		t.Errorf("items[0]: expected movie-a, got %s", items[0].VirtualItemID)
	}
	if items[1].VirtualItemID != "ep-7" {
		t.Errorf("items[1]: expected ep-7 (三体 latest), got %s", items[1].VirtualItemID)
	}
	if items[2].VirtualItemID != "movie-b" {
		t.Errorf("items[2]: expected movie-b, got %s", items[2].VirtualItemID)
	}
}
