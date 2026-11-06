package backend

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// Show pages must be merged before applying the client's offset. Otherwise a
// short copy on one server hides later episodes available on another server.
func (a *App) fetchShowItems(ctx context.Context, reqCtx *RequestContext, inst seriesInstance, path string, query url.Values) ([]map[string]any, error) {
	query = cloneValues(query)
	setQueryValue(query, "Limit", "200")
	setQueryValue(query, "EnableTotalRecordCount", "true")
	query.Del("StartIndex")
	items := []map[string]any{}
	seen := map[string]bool{}
	for offset := 0; ; {
		setQueryValue(query, "StartIndex", strconv.Itoa(offset))
		if reqCtx != nil && reqCtx.Parental != nil && reqCtx.Parental.restricted() && !reqCtx.Parental.catalog.tryAcquire() {
			return items, errParentalBudgetExhausted
		}
		payload, err := inst.Client.RequestJSON(ctx, reqCtx, a.Identity, http.MethodGet, path, query, nil)
		if err != nil {
			return items, err
		}
		batch := asItems(payload)
		if len(batch) == 0 {
			if block, ok := payload.(map[string]any); ok {
				if total, ok := numericInt(block["TotalRecordCount"]); ok && offset < total {
					return items, fmt.Errorf("upstream show page is incomplete")
				}
			}
			return items, nil
		}
		added := 0
		for _, item := range batch {
			id, _ := item["Id"].(string)
			if id == "" {
				return items, fmt.Errorf("upstream show item has no id")
			}
			if id != "" && seen[id] {
				continue
			}
			seen[id] = true
			items = append(items, item)
			added++
		}
		offset += len(batch)
		if block, ok := payload.(map[string]any); ok {
			if total, ok := numericInt(block["TotalRecordCount"]); ok && offset >= total {
				return items, nil
			}
		}
		if len(batch) < 200 {
			if block, ok := payload.(map[string]any); !ok || block["TotalRecordCount"] == nil {
				return items, nil
			}
		}
		if added == 0 {
			return items, fmt.Errorf("upstream show pagination did not advance")
		}
	}
}

type showListAvailability struct {
	confirmed  bool
	incomplete bool
}

// A stale/deleted copy on one instance cannot erase another instance's known
// seasons or episodes. The merged total remains a lower bound on any failure.
func (a *App) fetchAvailableShowItems(r *http.Request, inst seriesInstance, path string, query url.Values, state *showListAvailability) []map[string]any {
	rows, err := a.fetchShowItems(r.Context(), requestContextFrom(r.Context()), inst, path, query)
	state.confirmed = state.confirmed || err == nil || len(rows) > 0
	if err != nil {
		state.incomplete = true
		if a.Logger != nil {
			status := 0
			if response, ok := asUpstreamResponseError(err); ok {
				status = response.StatusCode
			}
			trace := "none"
			if requestTrace, _ := r.Context().Value(outboundTraceKey{}).(*outboundTrace); requestTrace != nil {
				trace = requestTrace.id
			}
			a.Logger.Warnf("show_list trace=%s server=%s route=%s status=%d retained=%d incomplete=true", trace, inst.ServerID, diagnosticRoute(path), status, len(rows))
		}
	}
	return rows
}

func (a *App) writeAvailableShowItems(w http.ResponseWriter, r *http.Request, items []map[string]any, state showListAvailability) {
	if state.incomplete && !state.confirmed {
		writeJSON(w, http.StatusBadGateway, map[string]any{"kind": "show-list-unavailable", "message": "No upstream could confirm the show list; please retry"})
		return
	}
	if !a.strictSanitizedUserDataItems(w, r, items) {
		return
	}
	payload := paginateItems(items, r.URL.Query())
	if state.incomplete {
		markIncompleteResults(w, payload)
	}
	if a.parentalResponseReady(w, r, payload) {
		writeJSON(w, http.StatusOK, payload)
	}
}
