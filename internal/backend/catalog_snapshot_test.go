package backend

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type catalogSnapshotTestCall struct {
	offset int
	limit  int
}

type catalogSnapshotTestRecorder struct {
	mu    sync.Mutex
	calls []catalogSnapshotTestCall
}

func (r *catalogSnapshotTestRecorder) record(offset, limit int) {
	r.mu.Lock()
	r.calls = append(r.calls, catalogSnapshotTestCall{offset: offset, limit: limit})
	r.mu.Unlock()
}

func (r *catalogSnapshotTestRecorder) snapshot() []catalogSnapshotTestCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]catalogSnapshotTestCall(nil), r.calls...)
}

// catalogSnapshotTestItems builds distinct raw upstream rows with nested maps
// and slices so a clone test can poison every level.
func catalogSnapshotTestItems(count int) []map[string]any {
	items := make([]map[string]any, count)
	for i := range items {
		id := fmt.Sprintf("m%d", i)
		items[i] = map[string]any{
			"Id":   id,
			"Name": fmt.Sprintf("Movie %d", i),
			"Type": "Movie",
			"ProviderIds": map[string]any{
				"Tmdb": fmt.Sprintf("t%d", i),
			},
			"MediaSources": []any{map[string]any{
				"Id":        "ms-" + id,
				"Name":      id + ".mp4",
				"Container": "mp4",
			}},
		}
	}
	return items
}

func catalogSnapshotTestPage(items []map[string]any, offset, limit int) catalogSnapshotPage {
	total := len(items)
	start := offset
	if start > total {
		start = total
	}
	end := start + limit
	if end > total {
		end = total
	}
	return catalogSnapshotPage{
		Items:    append([]map[string]any(nil), items[start:end]...),
		Total:    total,
		HasTotal: true,
	}
}

// TestCatalogSnapshotStockRoundContinues covers the stock (non-paged) upstream:
// the first Fill stops after the 5000-candidate round, the next Fill continues
// from that offset, and a complete entry is reused without another fetch.
func TestCatalogSnapshotStockRoundContinues(t *testing.T) {
	items := catalogSnapshotTestItems(6001)
	recorder := &catalogSnapshotTestRecorder{}
	fetch := func(_ context.Context, offset, limit int) (catalogSnapshotPage, error) {
		recorder.record(offset, limit)
		return catalogSnapshotTestPage(items, offset, limit), nil
	}
	store := &catalogSnapshotStore{}
	key := "stock-round-key"

	first, err := store.Fill(context.Background(), key, catalogSnapshotSpec{}, fetch)
	if err != nil {
		t.Fatalf("first Fill: %v", err)
	}
	if first.Complete {
		t.Fatalf("first Fill complete=true, want partial")
	}
	if len(first.Items) != 5000 || first.NextOffset != 5000 {
		t.Fatalf("first Fill items=%d next=%d, want 5000/5000", len(first.Items), first.NextOffset)
	}
	if first.PartialReason != catalogSnapshotPartialNonPaged {
		t.Fatalf("first Fill partialReason=%q, want %q", first.PartialReason, catalogSnapshotPartialNonPaged)
	}

	second, err := store.Fill(context.Background(), key, catalogSnapshotSpec{}, fetch)
	if err != nil {
		t.Fatalf("second Fill: %v", err)
	}
	if !second.Complete {
		t.Fatalf("second Fill complete=false reason=%q, want complete", second.PartialReason)
	}
	if len(second.Items) != 6001 || second.NextOffset != 6001 {
		t.Fatalf("second Fill items=%d next=%d, want 6001/6001", len(second.Items), second.NextOffset)
	}

	third, err := store.Fill(context.Background(), key, catalogSnapshotSpec{}, fetch)
	if err != nil {
		t.Fatalf("third Fill: %v", err)
	}
	if !third.Complete || len(third.Items) != 6001 {
		t.Fatalf("third Fill complete=%v items=%d, want complete/6001", third.Complete, len(third.Items))
	}

	calls := recorder.snapshot()
	if len(calls) != 2 {
		t.Fatalf("fetch calls=%d, want 2: %+v", len(calls), calls)
	}
	if calls[0] != (catalogSnapshotTestCall{offset: 0, limit: 5000}) {
		t.Fatalf("fetch[0]=%+v, want {0 5000}", calls[0])
	}
	if calls[1] != (catalogSnapshotTestCall{offset: 5000, limit: 5000}) {
		t.Fatalf("fetch[1]=%+v, want {5000 5000}", calls[1])
	}
}

// TestCatalogSnapshotReturnedItemsAreCloned proves one request cannot poison the
// cached raw maps, including nested maps and slices.
func TestCatalogSnapshotReturnedItemsAreCloned(t *testing.T) {
	items := catalogSnapshotTestItems(4)
	recorder := &catalogSnapshotTestRecorder{}
	fetch := func(_ context.Context, offset, limit int) (catalogSnapshotPage, error) {
		recorder.record(offset, limit)
		return catalogSnapshotTestPage(items, offset, limit), nil
	}
	store := &catalogSnapshotStore{}
	spec := catalogSnapshotSpec{RoundCandidates: 2}
	key := "clone-key"

	first, err := store.Fill(context.Background(), key, spec, fetch)
	if err != nil {
		t.Fatalf("first Fill: %v", err)
	}
	if len(first.Items) != 2 || first.Complete {
		t.Fatalf("first Fill items=%d complete=%v, want 2/false", len(first.Items), first.Complete)
	}

	poison := first.Items[0]
	poison["Name"] = "poisoned"
	poison["ProviderIds"].(map[string]any)["Tmdb"] = "poisoned"
	poison["MediaSources"].([]any)[0].(map[string]any)["Name"] = "poisoned"
	first.Items[0] = map[string]any{"Id": "poisoned"}

	second, err := store.Fill(context.Background(), key, spec, fetch)
	if err != nil {
		t.Fatalf("second Fill: %v", err)
	}
	if !second.Complete || len(second.Items) != 4 {
		t.Fatalf("second Fill complete=%v items=%d, want true/4", second.Complete, len(second.Items))
	}
	assertCatalogSnapshotTestItem(t, second.Items[0], 0)
	assertCatalogSnapshotTestItem(t, second.Items[1], 1)

	second.Items[2]["Name"] = "poisoned"
	second.Items[2]["ProviderIds"].(map[string]any)["Tmdb"] = "poisoned"
	reused, err := store.Fill(context.Background(), key, spec, fetch)
	if err != nil {
		t.Fatalf("reuse Fill: %v", err)
	}
	if !reused.Complete || len(reused.Items) != 4 {
		t.Fatalf("reuse Fill complete=%v items=%d, want true/4", reused.Complete, len(reused.Items))
	}
	assertCatalogSnapshotTestItem(t, reused.Items[2], 2)
	if calls := recorder.snapshot(); len(calls) != 2 {
		t.Fatalf("fetch calls=%d, want 2 after reuse", len(calls))
	}
}

func assertCatalogSnapshotTestItem(t *testing.T, item map[string]any, index int) {
	t.Helper()
	id := fmt.Sprintf("m%d", index)
	if got := item["Id"]; got != id {
		t.Fatalf("item[%d].Id=%v, want %s", index, got, id)
	}
	if got := item["Name"]; got != fmt.Sprintf("Movie %d", index) {
		t.Fatalf("item[%d].Name=%v, want Movie %d", index, got, index)
	}
	providers, _ := item["ProviderIds"].(map[string]any)
	if got := providers["Tmdb"]; got != fmt.Sprintf("t%d", index) {
		t.Fatalf("item[%d].ProviderIds.Tmdb=%v, want t%d", index, got, index)
	}
	sources, _ := item["MediaSources"].([]any)
	if len(sources) != 1 {
		t.Fatalf("item[%d].MediaSources len=%d, want 1", index, len(sources))
	}
	source, _ := sources[0].(map[string]any)
	if got := source["Name"]; got != id+".mp4" {
		t.Fatalf("item[%d].MediaSources[0].Name=%v, want %s.mp4", index, got, id)
	}
}

// TestCatalogSnapshotPagedFallbackWindowPreserved covers a paged upstream that
// rejects the 200 window: the 100 fallback must survive the round boundary and
// the next Fill must keep using it to reach the tail.
func TestCatalogSnapshotPagedFallbackWindowPreserved(t *testing.T) {
	items := catalogSnapshotTestItems(250)
	recorder := &catalogSnapshotTestRecorder{}
	fetch := func(_ context.Context, offset, limit int) (catalogSnapshotPage, error) {
		recorder.record(offset, limit)
		if limit > 100 {
			return catalogSnapshotPage{BadRequest: true}, nil
		}
		return catalogSnapshotTestPage(items, offset, limit), nil
	}
	store := &catalogSnapshotStore{}
	spec := catalogSnapshotSpec{Paged: true, RoundCandidates: 200}
	key := "paged-fallback-key"

	first, err := store.Fill(context.Background(), key, spec, fetch)
	if err != nil {
		t.Fatalf("first Fill: %v", err)
	}
	if first.Complete || len(first.Items) != 200 || first.NextOffset != 200 {
		t.Fatalf("first Fill complete=%v items=%d next=%d, want false/200/200", first.Complete, len(first.Items), first.NextOffset)
	}
	if first.EffectiveWindow != 100 {
		t.Fatalf("first Fill effectiveWindow=%d, want 100", first.EffectiveWindow)
	}
	if first.PartialReason != catalogSnapshotPartialRoundLimit {
		t.Fatalf("first Fill partialReason=%q, want %q", first.PartialReason, catalogSnapshotPartialRoundLimit)
	}

	second, err := store.Fill(context.Background(), key, spec, fetch)
	if err != nil {
		t.Fatalf("second Fill: %v", err)
	}
	if !second.Complete || len(second.Items) != 250 || second.NextOffset != 250 {
		t.Fatalf("second Fill complete=%v items=%d next=%d, want true/250/250", second.Complete, len(second.Items), second.NextOffset)
	}
	if second.EffectiveWindow != 100 {
		t.Fatalf("second Fill effectiveWindow=%d, want preserved 100", second.EffectiveWindow)
	}

	calls := recorder.snapshot()
	if len(calls) != 4 {
		t.Fatalf("fetch calls=%d, want 4: %+v", len(calls), calls)
	}
	if calls[0] != (catalogSnapshotTestCall{offset: 0, limit: 200}) {
		t.Fatalf("first request=%+v, want the rejected 200 window", calls[0])
	}
	for _, call := range calls[1:] {
		if call.limit > 100 {
			t.Fatalf("request after fallback=%+v, want limit <= 100", call)
		}
	}
}

// TestCatalogSnapshotRepeatedPageStops covers an upstream that ignores
// StartIndex and returns the same IDs forever: the fingerprint must stop the
// fill instead of paging until the round budget is spent.
func TestCatalogSnapshotRepeatedPageStops(t *testing.T) {
	pageItems := catalogSnapshotTestItems(200)
	recorder := &catalogSnapshotTestRecorder{}
	fetch := func(_ context.Context, offset, limit int) (catalogSnapshotPage, error) {
		recorder.record(offset, limit)
		return catalogSnapshotPage{Items: pageItems, Total: 5000, HasTotal: true}, nil
	}
	store := &catalogSnapshotStore{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	view, err := store.Fill(ctx, "repeated-page-key", catalogSnapshotSpec{Paged: true}, fetch)
	if err != nil {
		t.Fatalf("Fill: %v", err)
	}
	if view.Complete {
		t.Fatalf("repeated page reported complete")
	}
	if view.PartialReason != catalogSnapshotPartialRepeatedPage {
		t.Fatalf("partialReason=%q, want %q", view.PartialReason, catalogSnapshotPartialRepeatedPage)
	}
	if len(view.Items) != 200 {
		t.Fatalf("retained items=%d, want 200", len(view.Items))
	}
	if calls := recorder.snapshot(); len(calls) != 2 {
		t.Fatalf("fetch calls=%d, want 2 (fingerprint stop)", len(calls))
	}
}

// TestCatalogSnapshotBounds covers the injected clock TTL, the 16-entry cap and
// the byte budget.
func TestCatalogSnapshotBounds(t *testing.T) {
	items := catalogSnapshotTestItems(4)
	page := func(_ context.Context, offset, limit int) (catalogSnapshotPage, error) {
		return catalogSnapshotTestPage(items, offset, limit), nil
	}

	t.Run("ttl refetches after expiry", func(t *testing.T) {
		now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		store := &catalogSnapshotStore{
			ttl: time.Minute,
			now: func() time.Time { return now },
		}
		var calls atomic.Int64
		fetch := func(ctx context.Context, offset, limit int) (catalogSnapshotPage, error) {
			calls.Add(1)
			return page(ctx, offset, limit)
		}
		if view, err := store.Fill(context.Background(), "ttl-key", catalogSnapshotSpec{}, fetch); err != nil || !view.Complete {
			t.Fatalf("initial Fill err=%v complete=%v", err, view.Complete)
		}
		if view, err := store.Fill(context.Background(), "ttl-key", catalogSnapshotSpec{}, fetch); err != nil || !view.Complete {
			t.Fatalf("fresh reuse err=%v complete=%v", err, view.Complete)
		}
		if got := calls.Load(); got != 1 {
			t.Fatalf("fresh reuse fetch calls=%d, want 1", got)
		}
		now = now.Add(time.Minute)
		if view, err := store.Fill(context.Background(), "ttl-key", catalogSnapshotSpec{}, fetch); err != nil || !view.Complete {
			t.Fatalf("expired Fill err=%v complete=%v", err, view.Complete)
		}
		if got := calls.Load(); got != 2 {
			t.Fatalf("expired refetch calls=%d, want 2", got)
		}
	})

	t.Run("entry cap evicts oldest", func(t *testing.T) {
		store := &catalogSnapshotStore{maxEntries: 2}
		var mu sync.Mutex
		counts := map[string]int{}
		fetchFor := func(key string) catalogSnapshotFetchFunc {
			return func(ctx context.Context, offset, limit int) (catalogSnapshotPage, error) {
				mu.Lock()
				counts[key]++
				mu.Unlock()
				return page(ctx, offset, limit)
			}
		}
		for _, key := range []string{"a", "b", "c"} {
			if _, err := store.Fill(context.Background(), key, catalogSnapshotSpec{}, fetchFor(key)); err != nil {
				t.Fatalf("Fill %q: %v", key, err)
			}
		}
		if got := store.len(); got != 2 {
			t.Fatalf("store entries=%d, want 2", got)
		}
		if _, err := store.Fill(context.Background(), "a", catalogSnapshotSpec{}, fetchFor("a")); err != nil {
			t.Fatalf("refill a: %v", err)
		}
		mu.Lock()
		defer mu.Unlock()
		if counts["a"] != 2 {
			t.Fatalf("evicted key a fetch calls=%d, want 2", counts["a"])
		}
		if counts["b"] != 1 || counts["c"] != 1 {
			t.Fatalf("unrelated keys refetched: %+v", counts)
		}
	})

	t.Run("byte budget rejects oversized page", func(t *testing.T) {
		store := &catalogSnapshotStore{maxBytes: 128}
		big := []map[string]any{{"Id": "big", "Name": strings.Repeat("x", 512)}}
		view, err := store.Fill(context.Background(), "byte-key", catalogSnapshotSpec{}, func(_ context.Context, _, _ int) (catalogSnapshotPage, error) {
			return catalogSnapshotPage{Items: big, Total: 1, HasTotal: true}, nil
		})
		if err != nil {
			t.Fatalf("Fill: %v", err)
		}
		if view.Complete {
			t.Fatalf("oversized page reported complete")
		}
		if view.PartialReason != catalogSnapshotPartialByteLimit {
			t.Fatalf("partialReason=%q, want %q", view.PartialReason, catalogSnapshotPartialByteLimit)
		}
		if len(view.Items) != 0 {
			t.Fatalf("retained items=%d, want 0", len(view.Items))
		}
		if got := store.byteSize(); got != 0 {
			t.Fatalf("byteSize=%d, want 0", got)
		}
	})
}

// TestCatalogSnapshotConcurrentFillCoalescesAndCancels covers same-key
// coalescing, a waiter exiting on context cancellation, and leader
// cancellation. It is also the race-detector exercise for the store lock.
func TestCatalogSnapshotConcurrentFillCoalescesAndCancels(t *testing.T) {
	items := catalogSnapshotTestItems(6001)
	spec := catalogSnapshotSpec{}

	t.Run("same key coalesces", func(t *testing.T) {
		store := &catalogSnapshotStore{}
		var calls atomic.Int64
		started := make(chan struct{})
		release := make(chan struct{})
		fetch := func(ctx context.Context, offset, limit int) (catalogSnapshotPage, error) {
			if calls.Add(1) == 1 {
				close(started)
				<-release
			}
			return catalogSnapshotTestPage(items, offset, limit), nil
		}

		const waiters = 6
		views := make([]catalogSnapshotView, waiters+1)
		errs := make([]error, waiters+1)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			views[0], errs[0] = store.Fill(context.Background(), "shared-key", spec, fetch)
		}()
		<-started
		for i := 1; i <= waiters; i++ {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				views[index], errs[index] = store.Fill(context.Background(), "shared-key", spec, fetch)
			}(i)
		}
		time.Sleep(50 * time.Millisecond)
		close(release)
		wg.Wait()

		for i := range views {
			if errs[i] != nil {
				t.Fatalf("Fill %d: %v", i, errs[i])
			}
			if views[i].Complete || len(views[i].Items) != 5000 || views[i].NextOffset != 5000 {
				t.Fatalf("Fill %d complete=%v items=%d next=%d, want false/5000/5000", i, views[i].Complete, len(views[i].Items), views[i].NextOffset)
			}
		}
		if got := calls.Load(); got != 1 {
			t.Fatalf("fetch calls=%d, want 1 coalesced fill", got)
		}
	})

	t.Run("waiter ctx cancel exits", func(t *testing.T) {
		store := &catalogSnapshotStore{}
		started := make(chan struct{})
		release := make(chan struct{})
		var once sync.Once
		fetch := func(_ context.Context, offset, limit int) (catalogSnapshotPage, error) {
			once.Do(func() { close(started) })
			<-release
			return catalogSnapshotTestPage(items, offset, limit), nil
		}
		leaderDone := make(chan error, 1)
		go func() {
			_, err := store.Fill(context.Background(), "waiter-key", spec, fetch)
			leaderDone <- err
		}()
		<-started

		waitCtx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		view, err := store.Fill(waitCtx, "waiter-key", spec, fetch)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("waiter err=%v, want context deadline exceeded", err)
		}
		if len(view.Items) != 0 {
			t.Fatalf("canceled waiter returned %d items, want none", len(view.Items))
		}

		close(release)
		if err := <-leaderDone; err != nil {
			t.Fatalf("leader Fill: %v", err)
		}
	})

	t.Run("leader ctx cancel returns canceled", func(t *testing.T) {
		store := &catalogSnapshotStore{}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		started := make(chan struct{})
		var once sync.Once
		fetch := func(fetchCtx context.Context, _, _ int) (catalogSnapshotPage, error) {
			once.Do(func() { close(started) })
			<-fetchCtx.Done()
			return catalogSnapshotPage{}, fetchCtx.Err()
		}
		type result struct {
			view catalogSnapshotView
			err  error
		}
		done := make(chan result, 1)
		go func() {
			view, err := store.Fill(ctx, "leader-key", spec, fetch)
			done <- result{view: view, err: err}
		}()
		<-started
		cancel()

		got := <-done
		if !errors.Is(got.err, context.Canceled) {
			t.Fatalf("leader err=%v, want context canceled", got.err)
		}
		if got.view.PartialReason != catalogSnapshotPartialCancelled {
			t.Fatalf("partialReason=%q, want %q", got.view.PartialReason, catalogSnapshotPartialCancelled)
		}
	})
}
