package backend

import (
	"context"
	"sync"
	"time"
)

// aggregationConfig controls the behavior of parallel upstream aggregation.
type aggregationConfig struct {
	gracePeriod   time.Duration // 0 = wait for all
	globalTimeout time.Duration // hard deadline for background goroutines (prevents zombie goroutines)
}

// indexedResult pairs a server index with its upstream fetch result.
type indexedResult struct {
	index  int
	result upstreamItemsResult
}

// aggregateUpstreams runs tasks in parallel with grace period and background completion.
// It returns partial results immediately when the grace period expires,
// then continues draining remaining results in the background for ID mapping only.
func (a *App) aggregateUpstreams(parentCtx context.Context, cfg aggregationConfig, tasks []upstreamTask) []upstreamItemsResult {
	if len(tasks) == 0 {
		return nil
	}

	// Background context: not tied to the HTTP request, so goroutines survive
	// after the handler returns. Uses globalTimeout as hard deadline.
	bgTimeout := cfg.globalTimeout
	if bgTimeout <= 0 {
		bgTimeout = 15 * time.Second
	}
	bgCtx, bgCancel := context.WithTimeout(context.Background(), bgTimeout)

	resultCh := make(chan indexedResult, len(tasks))

	for _, task := range tasks {
		go func(t upstreamTask) {
			res := t.fn(bgCtx)
			resultCh <- indexedResult{index: t.index, result: res}
		}(task)
	}

	// Collect results with optional grace period
	var graceTimer <-chan time.Time
	collected := make([]upstreamItemsResult, 0, len(tasks))
	received := 0

	for received < len(tasks) {
		select {
		case res := <-resultCh:
			received++
			if res.result.Err == nil {
				collected = append(collected, res.result)
			}
			if graceTimer == nil && len(collected) > 0 && cfg.gracePeriod > 0 {
				graceTimer = time.After(cfg.gracePeriod)
			}
		case <-graceTimer:
			if a.Logger != nil && received < len(tasks) {
				a.Logger.Debugf("Aggregation grace period expired: %d/%d servers responded", received, len(tasks))
			}
			a.drainBackgroundResults(bgCtx, bgCancel, resultCh, len(tasks)-received)
			return collected
		case <-parentCtx.Done():
			a.drainBackgroundResults(bgCtx, bgCancel, resultCh, len(tasks)-received)
			return collected
		}
	}

	// All tasks completed within grace period
	bgCancel()
	return collected
}

// drainBackgroundResults continues collecting late results in a background goroutine.
// It only performs ID mapping (GetOrCreateVirtualID + AssociateAdditionalInstance),
// not constructing client-facing responses.
func (a *App) drainBackgroundResults(bgCtx context.Context, bgCancel context.CancelFunc, ch <-chan indexedResult, remaining int) {
	if remaining <= 0 {
		bgCancel()
		return
	}
	go func() {
		defer bgCancel()
		drained := 0
		for drained < remaining {
			select {
			case res := <-ch:
				drained++
				if len(res.result.Items) > 0 {
					a.registerBackgroundIDs(res.result)
				}
			case <-bgCtx.Done():
				return
			}
		}
	}()
}

// registerBackgroundIDs creates ID mappings for items from a late-arriving server.
// This ensures that the next search/detail request can find these items.
func (a *App) registerBackgroundIDs(result upstreamItemsResult) {
	cfg := a.ConfigStore.Snapshot()
	for _, item := range result.Items {
		if originalID, ok := item["Id"].(string); ok && originalID != "" {
			a.IDStore.GetOrCreateVirtualID(originalID, result.ServerID)
		}
		rewriteResponseIDs(item, result.ServerID, a.IDStore, cfg.Server.ID, a.Auth.ProxyUserID())
	}
}

// upstreamTask represents a single upstream fetch to be run in parallel.
type upstreamTask struct {
	index int
	fn    func(ctx context.Context) upstreamItemsResult
}

// fanOutClients runs fn for every client in parallel and returns the results in the
// input order, so callers can merge deterministically.
func fanOutClients[T any](clients []*UpstreamClient, fn func(*UpstreamClient) T) []T {
	results := make([]T, len(clients))
	var wg sync.WaitGroup
	for i, client := range clients {
		wg.Add(1)
		go func(index int, c *UpstreamClient) {
			defer wg.Done()
			results[index] = fn(c)
		}(i, client)
	}
	wg.Wait()
	return results
}

// flattenItems concatenates per-client item slices, preserving client order.
func flattenItems(groups [][]map[string]any) []map[string]any {
	out := make([]map[string]any, 0)
	for _, group := range groups {
		out = append(out, group...)
	}
	return out
}
