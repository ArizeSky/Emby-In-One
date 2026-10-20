package backend

import "net/http"

// handleItemsPrefixes serves GET /Items/Prefixes, used by clients for the A–Z
// jump index. The upstream returns a plain JSON array, not an {Items:...}
// envelope, so it is proxied as-is instead of going through the generic
// named-array helper (which merges envelopes and rewrites ids).
//
// The index is cosmetic: on any upstream error an empty array is returned so
// the client hides the alphabet bar rather than showing a hard error.
func (a *App) handleItemsPrefixes(w http.ResponseWriter, r *http.Request) {
	reqCtx := requestContextFrom(r.Context())
	clients := a.allowedClients(reqCtx)
	query := cloneValues(r.URL.Query())

	seen := map[string]bool{}
	merged := []any{}
	for _, c := range clients {
		payload, err := c.RequestJSON(r.Context(), reqCtx, a.Identity, http.MethodGet, "/Items/Prefixes", query, nil)
		if err != nil {
			continue
		}
		entries, ok := payload.([]any)
		if !ok {
			continue
		}
		for _, entry := range entries {
			// Prefix entries are objects such as {"Name":"A"} — dedupe across
			// servers so the index does not show duplicates.
			key := ""
			if m, ok := entry.(map[string]any); ok {
				if name, ok := m["Name"].(string); ok {
					key = name
				}
			}
			if key != "" {
				if seen[key] {
					continue
				}
				seen[key] = true
			}
			merged = append(merged, entry)
		}
	}

	writeJSON(w, http.StatusOK, merged)
}
