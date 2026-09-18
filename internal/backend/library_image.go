package backend

import (
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strconv"
)

type indexedItem struct {
	Item     map[string]any
	ServerID string
	SortA    int
	SortB    int
}

func (a *App) registerLibraryAndImageRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /Library/VirtualFolders", a.withContext(a.requireAuth(a.handleLibraryVirtualFolders)))
	mux.HandleFunc("GET /Library/SelectableRemoteLibraries", a.withContext(a.requireAuth(a.handleLibrarySelectableRemoteLibraries)))
	mux.HandleFunc("GET /Library/MediaFolders", a.withContext(a.requireAuth(a.handleLibraryMediaFolders)))
	for _, endpoint := range []string{"Genres", "MusicGenres", "Studios", "Persons", "Artists", "Artists/AlbumArtists"} {
		current := endpoint
		mux.HandleFunc("GET /"+current, a.withContext(a.requireAuth(func(w http.ResponseWriter, r *http.Request) {
			a.handleLibraryTaxonomy(w, r, current)
		})))
	}
	mux.HandleFunc("GET /Shows/{seriesId}/Seasons", a.withContext(a.requireAuth(a.handleShowsSeasons)))
	mux.HandleFunc("GET /Shows/{seriesId}/Episodes", a.withContext(a.requireAuth(a.handleShowsEpisodes)))
	mux.HandleFunc("GET /Search/Hints", a.withContext(a.requireAuth(a.handleSearchHints)))
	mux.HandleFunc("GET /Items/{itemId}/Images/{imageType}", a.withContext(a.handleItemImage))
	mux.HandleFunc("GET /Items/{itemId}/Images/{imageType}/{imageIndex}", a.withContext(a.handleItemImage))
	mux.HandleFunc("GET /Users/{userId}/Images/{imageType}", a.withContext(a.handleUserImageNotFound))
	mux.HandleFunc("GET /Users/{userId}/Images/{imageType}/{imageIndex}", a.withContext(a.handleUserImageNotFound))
}

func (a *App) handleLibraryVirtualFolders(w http.ResponseWriter, r *http.Request) {
	a.handleLibraryNamedArray(w, r, "/Library/VirtualFolders")
}

func (a *App) handleLibrarySelectableRemoteLibraries(w http.ResponseWriter, r *http.Request) {
	a.handleLibraryNamedArray(w, r, "/Library/SelectableRemoteLibraries")
}

func (a *App) handleLibraryNamedArray(w http.ResponseWriter, r *http.Request, upstreamPath string) {
	reqCtx := requestContextFrom(r.Context())
	onlineClients := a.allowedClients(reqCtx)
	cfg := a.ConfigStore.Snapshot()
	multiSource := len(onlineClients) > 1
	hidden := a.hiddenLibrariesFor(reqCtx)

	groups := fanOutClients(onlineClients, func(c *UpstreamClient) []map[string]any {
		payload, err := c.RequestJSON(r.Context(), reqCtx, a.Identity, http.MethodGet, upstreamPath, cloneValues(r.URL.Query()), nil)
		if err != nil {
			return nil
		}
		items := asItems(payload)
		items = filterHiddenLibraryItems(items, c.ID, hidden)
		for _, item := range items {
			rewriteResponseIDs(item, c.ID, a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
			if multiSource {
				if name, _ := item["Name"].(string); name != "" {
					item["Name"] = name + " (" + c.Name + ")"
				}
			}
		}
		return items
	})
	writeJSON(w, http.StatusOK, flattenItems(groups))
}

func (a *App) handleLibraryMediaFolders(w http.ResponseWriter, r *http.Request) {
	reqCtx := requestContextFrom(r.Context())
	clients := a.allowedClients(reqCtx)
	cfg := a.ConfigStore.Snapshot()
	hidden := a.hiddenLibrariesFor(reqCtx)

	groups := fanOutClients(clients, func(c *UpstreamClient) []map[string]any {
		payload, err := c.RequestJSON(r.Context(), reqCtx, a.Identity, http.MethodGet, "/Library/MediaFolders", cloneValues(r.URL.Query()), nil)
		if err != nil {
			return nil
		}
		items := asItems(payload)
		items = filterHiddenLibraryItems(items, c.ID, hidden)
		for _, item := range items {
			rewriteResponseIDs(item, c.ID, a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
		}
		return items
	})
	results := flattenItems(groups)
	writeJSON(w, http.StatusOK, map[string]any{"Items": toAnySlice(results), "TotalRecordCount": len(results), "StartIndex": 0})
}

func (a *App) handleLibraryTaxonomy(w http.ResponseWriter, r *http.Request, endpoint string) {
	reqCtx := requestContextFrom(r.Context())
	clients := a.allowedClients(reqCtx)
	cfg := a.ConfigStore.Snapshot()

	groups := fanOutClients(clients, func(c *UpstreamClient) []map[string]any {
		query := cloneValues(r.URL.Query())
		query.Set("UserId", c.clientUserID())
		if hasBatchIDQuery(query) {
			translated, ok := translateBatchIDQueryForServer(query, c.ID, a.IDStore)
			if !ok {
				return nil
			}
			query = translated
		}
		payload, err := c.RequestJSON(r.Context(), reqCtx, a.Identity, http.MethodGet, "/"+endpoint, query, nil)
		if err != nil {
			return nil
		}
		items := asItems(payload)
		for _, item := range items {
			rewriteResponseIDs(item, c.ID, a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
		}
		return items
	})
	results := flattenItems(groups)
	writeJSON(w, http.StatusOK, map[string]any{"Items": toAnySlice(results), "TotalRecordCount": len(results), "StartIndex": 0})
}
func (a *App) handleShowsSeasons(w http.ResponseWriter, r *http.Request) {
	resolved := a.resolveRouteID(r.PathValue("seriesId"))
	if resolved == nil {
		writeJSON(w, http.StatusOK, map[string]any{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0})
		return
	}
	if !a.requireServerAccess(w, r, resolved) {
		return
	}
	instances := a.collectAllowedInstances(requestContextFrom(r.Context()), resolved)
	cfg := a.ConfigStore.Snapshot()
	merged := map[int]*indexedItem{}
	unknown := []indexedItem{}
	for _, inst := range instances {
		query := cloneValues(r.URL.Query())
		query.Set("UserId", inst.Client.clientUserID())
		payload, err := inst.Client.RequestJSON(r.Context(), requestContextFrom(r.Context()), a.Identity, http.MethodGet, "/Shows/"+inst.OriginalID+"/Seasons", query, nil)
		if err != nil {
			continue
		}
		for _, item := range asItems(payload) {
			season := deepCloneMap(item)
			idx, ok := numericInt(season["IndexNumber"])
			if !ok {
				originalID, _ := season["Id"].(string)
				season["_originalId"] = originalID
				if originalID != "" {
					season["Id"] = a.IDStore.GetOrCreateVirtualID(originalID, inst.ServerID)
				}
				unknown = append(unknown, indexedItem{Item: season, ServerID: inst.ServerID})
				continue
			}
			if existing, found := merged[idx]; found {
				virtualID, _ := existing.Item["Id"].(string)
				if virtualID == "" {
					if originalID, _ := existing.Item["_originalId"].(string); originalID != "" {
						virtualID = a.IDStore.GetOrCreateVirtualID(originalID, existing.ServerID)
					}
				}
				if originalID, _ := season["Id"].(string); virtualID != "" && originalID != "" {
					a.IDStore.AssociateAdditionalInstance(virtualID, originalID, inst.ServerID)
				}
				// Check if candidate has better metadata; if so, replace
				if isBetterMetadata(existing.Item, existing.ServerID, season, inst.ServerID, cfg) {
					season["_originalId"], _ = season["Id"].(string)
					season["Id"] = virtualID
					existing.Item = season
					existing.ServerID = inst.ServerID
				}
				continue
			}
			originalID, _ := season["Id"].(string)
			season["_originalId"] = originalID
			season["Id"] = a.IDStore.GetOrCreateVirtualID(originalID, inst.ServerID)
			merged[idx] = &indexedItem{Item: season, ServerID: inst.ServerID, SortA: idx}
		}
	}
	keys := make([]int, 0, len(merged))
	for idx := range merged {
		keys = append(keys, idx)
	}
	sort.Ints(keys)
	items := make([]map[string]any, 0, len(keys)+len(unknown))
	for _, idx := range keys {
		item := merged[idx].Item
		preservedID, _ := item["Id"].(string)
		delete(item, "_originalId")
		delete(item, "Id")
		rewriteResponseIDs(item, merged[idx].ServerID, a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
		item["Id"] = preservedID
		items = append(items, item)
	}
	for _, entry := range unknown {
		preservedID, _ := entry.Item["Id"].(string)
		delete(entry.Item, "_originalId")
		delete(entry.Item, "Id")
		rewriteResponseIDs(entry.Item, entry.ServerID, a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
		entry.Item["Id"] = preservedID
		items = append(items, entry.Item)
	}
	// Seasons and episodes carry per-user UserData: for a regular user the local
	// record wins over the shared upstream account's state.
	a.overlayLocalUserDataItems(r, items)
	writeJSON(w, http.StatusOK, map[string]any{"Items": toAnySlice(items), "TotalRecordCount": len(items), "StartIndex": 0})
}

func (a *App) handleShowsEpisodes(w http.ResponseWriter, r *http.Request) {
	resolved := a.resolveRouteID(r.PathValue("seriesId"))
	if resolved == nil {
		writeJSON(w, http.StatusOK, map[string]any{"Items": []any{}, "TotalRecordCount": 0, "StartIndex": 0})
		return
	}
	if !a.requireServerAccess(w, r, resolved) {
		return
	}
	instances := a.collectAllowedInstances(requestContextFrom(r.Context()), resolved)
	cfg := a.ConfigStore.Snapshot()
	merged := map[string]*indexedItem{}
	var unkeyed []indexedItem
	for _, inst := range instances {
		query := cloneValues(r.URL.Query())
		query.Set("UserId", inst.Client.clientUserID())
		if seasonID := query.Get("SeasonId"); seasonID != "" {
			if resolvedSeason := a.IDStore.ResolveVirtualID(seasonID); resolvedSeason != nil {
				mapped := ""
				if resolvedSeason.ServerID == inst.ServerID {
					mapped = resolvedSeason.OriginalID
				} else {
					for _, other := range resolvedSeason.OtherInstances {
						if other.ServerID == inst.ServerID {
							mapped = other.OriginalID
							break
						}
					}
				}
				if mapped != "" {
					query.Set("SeasonId", mapped)
				} else {
					continue
				}
			}
		}
		payload, err := inst.Client.RequestJSON(r.Context(), requestContextFrom(r.Context()), a.Identity, http.MethodGet, "/Shows/"+inst.OriginalID+"/Episodes", query, nil)
		if err != nil {
			continue
		}
		for _, item := range asItems(payload) {
			episode := deepCloneMap(item)
			seasonNum, okSeason := numericInt(episode["ParentIndexNumber"])
			episodeNum, okEpisode := numericInt(episode["IndexNumber"])
			if !okSeason || !okEpisode {
				originalID, _ := episode["Id"].(string)
				episode["_originalId"] = originalID
				episode["Id"] = a.IDStore.GetOrCreateVirtualID(originalID, inst.ServerID)
				unkeyed = append(unkeyed, indexedItem{Item: episode, ServerID: inst.ServerID})
				continue
			}
			key := strconv.Itoa(seasonNum) + ":" + strconv.Itoa(episodeNum)
			if existing, found := merged[key]; found {
				virtualID, _ := existing.Item["Id"].(string)
				if virtualID == "" {
					if originalID, _ := existing.Item["_originalId"].(string); originalID != "" {
						virtualID = a.IDStore.GetOrCreateVirtualID(originalID, existing.ServerID)
					}
				}
				if originalID, _ := episode["Id"].(string); virtualID != "" && originalID != "" {
					a.IDStore.AssociateAdditionalInstance(virtualID, originalID, inst.ServerID)
				}
				// Check if candidate has better metadata; if so, replace
				if isBetterMetadata(existing.Item, existing.ServerID, episode, inst.ServerID, cfg) {
					episode["_originalId"], _ = episode["Id"].(string)
					episode["Id"] = virtualID
					existing.Item = episode
					existing.ServerID = inst.ServerID
				}
				continue
			}
			originalID, _ := episode["Id"].(string)
			episode["_originalId"] = originalID
			episode["Id"] = a.IDStore.GetOrCreateVirtualID(originalID, inst.ServerID)
			merged[key] = &indexedItem{Item: episode, ServerID: inst.ServerID, SortA: seasonNum, SortB: episodeNum}
		}
	}
	entries := make([]indexedItem, 0, len(merged))
	for _, entry := range merged {
		entries = append(entries, *entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].SortA != entries[j].SortA {
			return entries[i].SortA < entries[j].SortA
		}
		return entries[i].SortB < entries[j].SortB
	})
	items := make([]map[string]any, 0, len(entries)+len(unkeyed))
	for _, entry := range entries {
		preservedID, _ := entry.Item["Id"].(string)
		delete(entry.Item, "_originalId")
		delete(entry.Item, "Id")
		rewriteResponseIDs(entry.Item, entry.ServerID, a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
		entry.Item["Id"] = preservedID
		items = append(items, entry.Item)
	}
	for _, entry := range unkeyed {
		preservedID, _ := entry.Item["Id"].(string)
		delete(entry.Item, "_originalId")
		delete(entry.Item, "Id")
		rewriteResponseIDs(entry.Item, entry.ServerID, a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
		entry.Item["Id"] = preservedID
		items = append(items, entry.Item)
	}
	// Seasons and episodes carry per-user UserData: for a regular user the local
	// record wins over the shared upstream account's state.
	a.overlayLocalUserDataItems(r, items)
	writeJSON(w, http.StatusOK, map[string]any{"Items": toAnySlice(items), "TotalRecordCount": len(items), "StartIndex": 0})
}

func (a *App) handleSearchHints(w http.ResponseWriter, r *http.Request) {
	reqCtx := requestContextFrom(r.Context())
	clients := a.allowedClients(reqCtx)

	// A failed client returns nil so its group is simply absent from the merge.
	perClient := fanOutClients(clients, func(c *UpstreamClient) *upstreamItemsResult {
		query := cloneValues(r.URL.Query())
		query.Set("UserId", c.clientUserID())
		payload, err := c.RequestJSON(r.Context(), reqCtx, a.Identity, http.MethodGet, "/Search/Hints", query, nil)
		if err != nil {
			return nil
		}
		block, ok := payload.(map[string]any)
		if !ok {
			return nil
		}
		items := asItems(map[string]any{"Items": block["SearchHints"]})
		if len(items) == 0 {
			items = asItems(payload)
		}
		return &upstreamItemsResult{ServerID: c.ID, Items: items}
	})

	collected := make([]upstreamItemsResult, 0, len(perClient))
	for _, result := range perClient {
		if result != nil {
			collected = append(collected, *result)
		}
	}
	merged := a.mergeRoundRobinItems(collected, a.clientFacingUserIDFor(r))

	a.overlayLocalUserDataItems(r, merged)
	writeJSON(w, http.StatusOK, map[string]any{
		"SearchHints":      toAnySlice(merged),
		"TotalRecordCount": len(merged),
	})
}

func (a *App) handleItemImage(w http.ResponseWriter, r *http.Request) {
	resolved := a.resolveRouteID(r.PathValue("itemId"))
	if resolved == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	// Image URLs are embedded by clients and stay reachable without a token, so
	// the server access check only applies to authenticated requests.
	if reqCtx := requestContextFrom(r.Context()); reqCtx != nil && reqCtx.ProxyUser != nil && !a.isServerAllowed(reqCtx, resolved.ServerID) {
		writeJSON(w, http.StatusForbidden, map[string]any{"message": "Access denied"})
		return
	}
	imagePath := "/Items/" + resolved.OriginalID + "/Images/" + r.PathValue("imageType")
	if imageIndex := r.PathValue("imageIndex"); imageIndex != "" {
		imagePath += "/" + imageIndex
	}
	imageQuery := cloneValues(r.URL.Query())
	imageQuery.Del("api_key")
	imageQuery.Del("ApiKey")
	resp, err := resolved.Client.Stream(r.Context(), requestContextFrom(r.Context()), a.Identity, imagePath, imageQuery)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Cache-Control", "public, max-age=86400")
	for _, header := range []string{"Content-Type", "Content-Length", "ETag", "Last-Modified", "Content-Range"} {
		if value := resp.Header.Get(header); value != "" {
			w.Header().Set(header, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (a *App) handleUserImageNotFound(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusNotFound)
}

func numericInt(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		return int(typed), true
	case float32:
		return int(typed), true
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		if err == nil {
			return int(parsed), true
		}
	case string:
		parsed, err := strconv.Atoi(typed)
		if err == nil {
			return parsed, true
		}
	}
	return 0, false
}

func toAnySlice(items []map[string]any) []any {
	out := make([]any, len(items))
	for i, item := range items {
		out[i] = item
	}
	return out
}
