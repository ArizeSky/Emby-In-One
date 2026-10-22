package backend

import "net/http"

// adminAggregationInput carries the explicit target state. It is a pointer so a
// missing, null or wrongly-typed value can be rejected instead of silently
// defaulting to a state the caller never asked for.
type adminAggregationInput struct {
	AggregationEnabled *bool `json:"aggregationEnabled"`
}

// handleAdminUpstreamAggregation flips one server's aggregation switch. It only
// touches the persisted flag: it never reloads the upstream pool, re-logs in or
// probes the connection, so an offline server or one with an uncollected
// identity can still be toggled. The server is addressed by its stable ID; the
// index fallback that parsePathUpstream accepts is deliberately not used here.
func (a *App) handleAdminUpstreamAggregation(w http.ResponseWriter, r *http.Request) {
	var body adminAggregationInput
	if err := decodeJSONBody(r, &body); err != nil || body.AggregationEnabled == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "aggregationEnabled must be a boolean",
		})
		return
	}
	cfg := a.ConfigStore.Snapshot()
	id := r.PathValue("id")
	index := -1
	for i := range cfg.Upstream {
		if cfg.Upstream[i].ID == id {
			index = i
			break
		}
	}
	if index < 0 {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Server not found"})
		return
	}
	disabled := !*body.AggregationEnabled
	if cfg.Upstream[index].AggregationDisabled != disabled {
		cfg.Upstream[index].AggregationDisabled = disabled
		if err := a.ConfigStore.SaveAndReplace(cfg); err != nil {
			if a.Logger != nil {
				a.Logger.Errorf("save upstream aggregation: %s", redactURLInError(err))
			}
			writeJSON(w, http.StatusInternalServerError, map[string]any{
				"error": "Failed to save aggregation setting",
			})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "id": id, "aggregationEnabled": *body.AggregationEnabled,
	})
}
