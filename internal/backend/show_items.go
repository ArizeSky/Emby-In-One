package backend

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

func (a *App) fetchShowPages(ctx context.Context, reqCtx *RequestContext, inst seriesInstance, path string, query url.Values, budget *catalogRequestBudget, page func([]map[string]any)) ([]map[string]any, error) {
	query = cloneValues(query)
	setQueryValue(query, "Limit", "200")
	setQueryValue(query, "EnableTotalRecordCount", "true")
	query.Del("StartIndex")
	items := []map[string]any{}
	seen := map[string]bool{}
	for offset := 0; ; {
		if err := ctx.Err(); err != nil {
			return items, err
		}
		setQueryValue(query, "StartIndex", strconv.Itoa(offset))
		if !budget.tryAcquire() {
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
		accepted := []map[string]any{}
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
			accepted = append(accepted, item)
			added++
		}
		if page != nil && len(accepted) > 0 {
			page(accepted)
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
