package backend

import "net/http"

// handleUserPlayingItemCleanup releases a PlayingItems slot. Unlike the new
// content paths it must not go through the aggregation-filtered resolver: a
// server whose aggregation was switched off still has to be able to stop and
// release the playback slot it started. It keeps the pure account-grant check
// and never queries for new content.
func (a *App) handleUserPlayingItemCleanup(w http.ResponseWriter, r *http.Request) {
	itemID := r.PathValue("itemId")
	resolved := a.IDStore.ResolveVirtualID(itemID)
	if resolved == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Item not found"})
		return
	}
	targetServer := resolved.ServerID
	originalItemID := resolved.OriginalID
	query := cloneValues(r.URL.Query())
	if ms := queryValue(query, "MediaSourceId"); ms != "" {
		if source := a.IDStore.ResolveVirtualID(ms); source != nil {
			mapped, ok := translateVirtualIDForServer(itemID, source.ServerID, a.IDStore)
			if !ok {
				// A stop event must not interpret an unrelated item across servers.
				writeJSON(w, http.StatusBadRequest, map[string]any{"message": "Selected version does not belong to this item"})
				return
			}
			targetServer = source.ServerID
			originalItemID = mapped
			setQueryValue(query, "MediaSourceId", source.OriginalID)
		}
	}
	// Pure authorization: aggregation is deliberately not checked here.
	if !a.isServerAllowed(requestContextFrom(r.Context()), targetServer) {
		writeJSON(w, http.StatusForbidden, map[string]any{"message": "Access denied"})
		return
	}
	client := a.Upstream.ClientByID(targetServer)
	if client == nil || !client.IsOnline() {
		// Offline or missing: release the local slot by answering 204, never
		// switch to a backup server for a stop event.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	path := "/Users/" + client.clientUserID() + "/PlayingItems/" + originalItemID
	_ = a.forwardNoContent(r, client, http.MethodDelete, path, query, nil)
	w.WriteHeader(http.StatusNoContent)
}
