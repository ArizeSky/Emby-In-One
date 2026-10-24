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
	// A restricted user must not see an unfiltered A–Z index or its counts: they
	// could reveal titles the user may not browse. The list and search still work;
	// only this cosmetic index is withheld (a deliberate compatibility trade-off).
	if maxR, blockUnrated := a.parentalPolicyFor(r); maxR > 0 || blockUnrated {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	clients := a.allowedClients(reqCtx)
	query := cloneValues(r.URL.Query())
	parentID := firstQueryValue(query, "ParentId", "parentId", "parentid")

	seen := map[string]bool{}
	merged := []any{}
	for _, c := range clients {
		serverQuery := cloneValues(query)
		if parentID != "" && parentID != "0" && parentID != "root" {
			translated, ok := translateVirtualIDForServer(parentID, c.ID, a.IDStore)
			if !ok {
				// This server does not own the requested parent container; sending it
				// the virtual parent id would be meaningless.
				continue
			}
			setQueryValue(serverQuery, "ParentId", translated)
			serverQuery.Del("parentId")
			serverQuery.Del("parentid")
		}
		payload, err := c.RequestJSON(r.Context(), reqCtx, a.Identity, http.MethodGet, "/Items/Prefixes", serverQuery, nil)
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
