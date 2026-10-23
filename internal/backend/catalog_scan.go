package backend

import (
	"net/http"
	"sync"
)

// catalogRequestBudget caps how many real upstream HTTP attempts one catalog
// request may make across all servers and all pages (plan §6.2). Every page
// request draws one token; when the budget is exhausted the scanner stops
// issuing requests and the caller marks the result incomplete rather than
// treating unqueried items as absent.
type catalogRequestBudget struct {
	mu        sync.Mutex
	remaining int
}

// newCatalogRequestBudget returns the default per-request catalog budget.
func newCatalogRequestBudget() *catalogRequestBudget {
	return &catalogRequestBudget{remaining: 100}
}

// tryAcquire consumes one attempt token, reporting false when the budget is
// exhausted. A nil budget never limits (used by callers that do not scan).
func (b *catalogRequestBudget) tryAcquire() bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.remaining <= 0 {
		return false
	}
	b.remaining--
	return true
}

// totalRecordCount reads a trusted TotalRecordCount from an upstream envelope
// before it is flattened, so a short page that still owes rows is not mistaken
// for the end of the list (plan §6.2 item 6).
func totalRecordCount(payload any) (int, bool) {
	block, ok := payload.(map[string]any)
	if !ok {
		return 0, false
	}
	raw, ok := block["TotalRecordCount"]
	if !ok {
		return 0, false
	}
	return numericInt(raw)
}

// isBadRequestError reports whether err is a structured upstream 400, the only
// signal that justifies the pagedScan window fallback (plan §6.2 item 3). It
// never string-matches the error text.
func isBadRequestError(err error) bool {
	resp, ok := asUpstreamResponseError(err)
	return ok && resp.StatusCode == http.StatusBadRequest
}

// markIncompleteResults attaches the incomplete-results marker to an object
// response body and its header (plan §6.2 item 9).
func markIncompleteResults(w http.ResponseWriter, payload map[string]any) {
	w.Header().Set("X-EIO-Results-Incomplete", "true")
	if payload != nil {
		payload["EioIncompleteResults"] = true
	}
}

// markIncompleteResultsHeader sets only the header, for array-shaped responses.
func markIncompleteResultsHeader(w http.ResponseWriter) {
	w.Header().Set("X-EIO-Results-Incomplete", "true")
}

// writeItemsResponse writes an item-list object response, attaching the
// incomplete-results marker when the merged candidate set was partial.
func (a *App) writeItemsResponse(w http.ResponseWriter, payload map[string]any, incomplete bool) {
	if incomplete {
		markIncompleteResults(w, payload)
	}
	writeJSON(w, http.StatusOK, payload)
}
