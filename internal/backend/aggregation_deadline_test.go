package backend

import (
	"context"
	"testing"
	"time"
)

func TestAggregationDeadlinePreservesCompletedResults(t *testing.T) {
	done := make(chan struct{})
	defer func() { <-done }()
	started := time.Now()
	results, report := (&App{}).aggregateUpstreamsWithReport(context.Background(), aggregationConfig{globalTimeout: 40 * time.Millisecond}, []upstreamTask{
		{index: 0, fn: func(context.Context) upstreamItemsResult {
			return upstreamItemsResult{ServerID: "fast", Complete: true}
		}},
		{index: 1, fn: func(context.Context) upstreamItemsResult {
			defer close(done)
			time.Sleep(180 * time.Millisecond)
			return upstreamItemsResult{ServerID: "slow", Complete: true}
		}},
	})
	if elapsed := time.Since(started); elapsed > 140*time.Millisecond {
		t.Fatalf("collector waited for cancellation-ignoring task: %v", elapsed)
	}
	if len(results) != 1 || results[0].ServerID != "fast" {
		t.Fatalf("completed results=%+v", results)
	}
	if report.Expected != 2 || report.Received != 1 || report.TimedOut != 1 || report.Failed != 0 || !report.Incomplete || report.GraceExpired {
		t.Fatalf("report=%+v", report)
	}
}

func TestAggregationConfigOrderIndependentOfArrival(t *testing.T) {
	results, report := (&App{}).aggregateUpstreamsWithReport(context.Background(), aggregationConfig{globalTimeout: time.Second}, []upstreamTask{
		{index: 0, fn: func(context.Context) upstreamItemsResult {
			time.Sleep(20 * time.Millisecond)
			return upstreamItemsResult{ServerID: "first", Complete: true}
		}},
		{index: 1, fn: func(context.Context) upstreamItemsResult {
			return upstreamItemsResult{ServerID: "second", Complete: true}
		}},
	})
	if len(results) != 2 || results[0].ServerID != "first" || results[1].ServerID != "second" || report.Incomplete {
		t.Fatalf("results=%+v report=%+v", results, report)
	}
}
