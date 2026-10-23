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
	query.Set("Limit", "200")
	query.Set("EnableTotalRecordCount", "true")
	query.Del("StartIndex")
	items := []map[string]any{}
	seen := map[string]bool{}
	for offset := 0; ; {
		query.Set("StartIndex", strconv.Itoa(offset))
		if reqCtx != nil && reqCtx.Parental != nil && reqCtx.Parental.restricted() && !reqCtx.Parental.catalog.tryAcquire() {
			return nil, errParentalBudgetExhausted
		}
		payload, err := inst.Client.RequestJSON(ctx, reqCtx, a.Identity, http.MethodGet, path, query, nil)
		if err != nil {
			return nil, err
		}
		batch := asItems(payload)
		if len(batch) == 0 {
			if block, ok := payload.(map[string]any); ok {
				if total, ok := numericInt(block["TotalRecordCount"]); ok && offset < total {
					return nil, fmt.Errorf("upstream show page is incomplete")
				}
			}
			return items, nil
		}
		added := 0
		for _, item := range batch {
			id, _ := item["Id"].(string)
			if id == "" {
				return nil, fmt.Errorf("upstream show item has no id")
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
			return nil, fmt.Errorf("upstream show pagination did not advance")
		}
	}
}
