package backend

// hiddenLibrariesFor resolves the current request's hidden-library map
// (serverID → libraryID set). A nil store — the database being unavailable —
// means the feature is disabled and nothing is filtered anywhere.
func (a *App) hiddenLibrariesFor(reqCtx *RequestContext) map[string]map[string]struct{} {
	if a.HiddenLibraries == nil {
		return nil
	}
	return a.HiddenLibraries.HiddenForRequest(reqCtx)
}

// isHiddenLibraryItem reports whether the item is a library the user has
// hidden on this upstream. It must run before rewriteResponseIDs: the item's
// Id is still the upstream's original library ID there, which is the key the
// hidden config stores.
func isHiddenLibraryItem(hidden map[string]map[string]struct{}, serverID string, item map[string]any) bool {
	if len(hidden) == 0 {
		return false
	}
	libraryID, _ := item["Id"].(string)
	if libraryID == "" {
		return false
	}
	libraries, ok := hidden[serverID]
	if !ok {
		return false
	}
	_, isHidden := libraries[libraryID]
	return isHidden
}

// filterHiddenLibraryItems drops hidden libraries from a library listing.
// The input slice is freshly built per request, so filtering happens in place.
func filterHiddenLibraryItems(items []map[string]any, serverID string, hidden map[string]map[string]struct{}) []map[string]any {
	if len(hidden) == 0 {
		return items
	}
	kept := items[:0]
	for _, item := range items {
		if isHiddenLibraryItem(hidden, serverID, item) {
			continue
		}
		kept = append(kept, item)
	}
	return kept
}

// isLibraryViewType reports whether an item type names a library entry — the
// root-level UserView/CollectionFolder items some clients list through
// /Users/{id}/Items instead of /Users/{id}/Views.
func isLibraryViewType(itemType string) bool {
	return itemType == "UserView" || itemType == "CollectionFolder"
}

// dropHiddenLibraryViews filters library-type entries out of merged item
// results. Content items are never touched: only a UserView/CollectionFolder
// whose Id is hidden for its server is dropped, so the same filter is safe
// for recursive full-library queries too.
func dropHiddenLibraryViews(results []upstreamItemsResult, hidden map[string]map[string]struct{}) {
	for i := range results {
		result := &results[i]
		if result.Err != nil || len(result.Items) == 0 {
			continue
		}
		kept := result.Items[:0]
		for _, item := range result.Items {
			itemType, _ := item["Type"].(string)
			if isLibraryViewType(itemType) && isHiddenLibraryItem(hidden, result.ServerID, item) {
				continue
			}
			kept = append(kept, item)
		}
		result.Items = kept
	}
}
