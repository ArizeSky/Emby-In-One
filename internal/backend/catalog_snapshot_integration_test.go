package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
)

// TestCatalogSnapshotTailReachable is the end-to-end shape of the problem: a
// stock upstream has more candidates than one scan round, so the first narrow
// client request returns a partial prefix and the follow-up request for a high
// StartIndex must continue the same snapshot instead of claiming the library
// ends at the first round.
func TestCatalogSnapshotTailReachable(t *testing.T) {
	const total = 6001
	movies := make([]map[string]any, total)
	for i := range movies {
		movies[i] = windowMovie(fmt.Sprintf("m%d", i), fmt.Sprintf("%d", i+1), fmt.Sprintf("Movie %d", i), 2020)
	}
	var upstreamItems atomic.Int64
	upstream := (&narrowWindowServer{
		maxWindow: 5000,
		movies:    movies,
		requests:  &upstreamItems,
		mu:        make(chan struct{}, 1),
	}).start(t, "user-a")

	withTempAppConfig(t, twoUpstreamConfig(upstream.URL, ""), func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		itemsPath := "/Users/" + app.Auth.ProxyUserID() + "/Items"

		first := doJSONRequest(t, handler, http.MethodGet, itemsPath+"?Limit=20", nil, token)
		if first.Code != http.StatusOK {
			t.Fatalf("first request status=%d body=%s", first.Code, first.Body.String())
		}
		var firstPayload map[string]any
		if err := json.Unmarshal(first.Body.Bytes(), &firstPayload); err != nil {
			t.Fatalf("decode first response: %v", err)
		}
		firstItems := asItems(firstPayload)
		firstTotal, hasFirstTotal := totalRecordCount(firstPayload)
		if len(firstItems) != 20 {
			t.Fatalf("first request items=%d, want 20", len(firstItems))
		}
		if !hasFirstTotal || firstTotal != 5000 {
			t.Fatalf("first request total=%d hasTotal=%v, want the 5000-candidate lower bound", firstTotal, hasFirstTotal)
		}
		if first.Header().Get("X-EIO-Results-Incomplete") != "true" {
			t.Fatalf("first request not marked incomplete: headers=%v", first.Header())
		}
		if got := upstreamItems.Load(); got != 1 {
			t.Fatalf("upstream item requests after first proxy request=%d, want 1", got)
		}

		second := doJSONRequest(t, handler, http.MethodGet, itemsPath+"?StartIndex=5000&Limit=20", nil, token)
		if second.Code != http.StatusOK {
			t.Fatalf("second request status=%d body=%s", second.Code, second.Body.String())
		}
		var secondPayload map[string]any
		if err := json.Unmarshal(second.Body.Bytes(), &secondPayload); err != nil {
			t.Fatalf("decode second response: %v", err)
		}
		secondItems := asItems(secondPayload)
		secondTotal, hasSecondTotal := totalRecordCount(secondPayload)
		if len(secondItems) != 20 {
			t.Fatalf("second request items=%d, want 20 tail items", len(secondItems))
		}
		if !hasSecondTotal || secondTotal != total {
			t.Fatalf("second request total=%d hasTotal=%v, want the complete %d", secondTotal, hasSecondTotal, total)
		}
		if second.Header().Get("X-EIO-Results-Incomplete") != "" || secondPayload["EioIncompleteResults"] == true {
			t.Fatalf("second request still marked incomplete: headers=%v payload=%v", second.Header(), secondPayload["EioIncompleteResults"])
		}
		if got := upstreamItems.Load(); got != 2 {
			t.Fatalf("upstream item requests after continuation=%d, want 2", got)
		}

		firstIDs := make(map[string]struct{}, len(firstItems))
		for _, item := range firstItems {
			if id, ok := item["Id"].(string); ok {
				firstIDs[id] = struct{}{}
			}
		}
		for _, item := range secondItems {
			id, _ := item["Id"].(string)
			if _, overlap := firstIDs[id]; overlap {
				t.Fatalf("tail page re-served head item %q; snapshot did not continue", id)
			}
		}
	})
}
