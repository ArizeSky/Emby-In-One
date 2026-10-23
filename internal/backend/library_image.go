package backend

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
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
	mux.HandleFunc("GET /Items/Prefixes", a.withContext(a.requireAuth(a.handleItemsPrefixes)))
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
	if hasBatchIDQuery(r.URL.Query()) && validateBatchIDQuery(r.URL.Query()) != nil {
		invalidBatchIDResponse(w)
		return
	}
	clients := a.allowedClients(reqCtx)
	cfg := a.ConfigStore.Snapshot()
	groups := fanOutClients(clients, func(c *UpstreamClient) []map[string]any {
		query := cloneValues(r.URL.Query())
		query.Set("UserId", c.clientUserID())
		// Taxonomies scoped to a library arrive with a *virtual* ParentId. Translate it
		// per upstream and skip servers that do not own that library, otherwise the
		// upstream cannot resolve the id and the genre/studio tab renders empty.
		if pid := firstQueryValue(query, "ParentId", "parentId", "parentid"); pid != "" {
			resolvedParent, status := a.resolveRouteForRequest(reqCtx, pid)
			if status != 0 || resolvedParent.ServerID != c.ID {
				return nil
			}
			query.Set("ParentId", resolvedParent.OriginalID)
			query.Del("parentId")
			query.Del("parentid")
		}
		if hasBatchIDQuery(query) {
			translated := translateBatchIDQueryForServer(query, c.ID, a.IDStore)
			if translated.Err != nil || translated.Empty {
				return nil
			}
			query = translated.Query
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
	resolved, routeStatus := a.resolveRouteForRequest(requestContextFrom(r.Context()), r.PathValue("seriesId"))
	if routeStatus != 0 {
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
	identities := map[string]string{}
	for _, inst := range instances {
		query := cloneValues(r.URL.Query())
		query.Set("UserId", inst.Client.clientUserID())
		rows, err := a.fetchShowItems(r.Context(), requestContextFrom(r.Context()), inst, "/Shows/"+inst.OriginalID+"/Seasons", query)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Failed to load complete show list; please retry"})
			return
		}
		a.registerShowIdentities(rows, inst.ServerID, false, identities)
		rows = a.parentalFilterServerItems(r, inst.Client, rows)
		for _, item := range rows {
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
		a.recordShowDisplaySource(item, preservedID, merged[idx].ServerID)
		delete(item, "_originalId")
		delete(item, "Id")
		rewriteResponseIDs(item, merged[idx].ServerID, a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
		item["Id"] = preservedID
		items = append(items, item)
	}
	for _, entry := range unknown {
		preservedID, _ := entry.Item["Id"].(string)
		a.recordShowDisplaySource(entry.Item, preservedID, entry.ServerID)
		delete(entry.Item, "_originalId")
		delete(entry.Item, "Id")
		rewriteResponseIDs(entry.Item, entry.ServerID, a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
		entry.Item["Id"] = preservedID
		items = append(items, entry.Item)
	}
	if !a.strictSanitizedUserDataItems(w, r, items) {
		return
	}
	payload := paginateItems(items, r.URL.Query())
	if a.parentalResponseReady(w, r, payload) {
		writeJSON(w, http.StatusOK, payload)
	}
}

func (a *App) handleShowsEpisodes(w http.ResponseWriter, r *http.Request) {
	resolved, routeStatus := a.resolveRouteForRequest(requestContextFrom(r.Context()), r.PathValue("seriesId"))
	if routeStatus != 0 {
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
	identities := map[string]string{}
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
		rows, err := a.fetchShowItems(r.Context(), requestContextFrom(r.Context()), inst, "/Shows/"+inst.OriginalID+"/Episodes", query)
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Failed to load complete show list; please retry"})
			return
		}
		a.registerShowIdentities(rows, inst.ServerID, true, identities)
		rows = a.parentalFilterServerItems(r, inst.Client, rows)
		for _, item := range rows {
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
		a.recordShowDisplaySource(entry.Item, preservedID, entry.ServerID)
		delete(entry.Item, "_originalId")
		delete(entry.Item, "Id")
		rewriteResponseIDs(entry.Item, entry.ServerID, a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
		entry.Item["Id"] = preservedID
		items = append(items, entry.Item)
	}
	for _, entry := range unkeyed {
		preservedID, _ := entry.Item["Id"].(string)
		a.recordShowDisplaySource(entry.Item, preservedID, entry.ServerID)
		delete(entry.Item, "_originalId")
		delete(entry.Item, "Id")
		rewriteResponseIDs(entry.Item, entry.ServerID, a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
		entry.Item["Id"] = preservedID
		items = append(items, entry.Item)
	}
	if !a.strictSanitizedUserDataItems(w, r, items) {
		return
	}
	payload := paginateItems(items, r.URL.Query())
	if a.parentalResponseReady(w, r, payload) {
		writeJSON(w, http.StatusOK, payload)
	}
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
	collected = a.parentalVisibleResults(r, collected)
	merged := a.mergeRoundRobinItems(collected, a.clientFacingUserIDFor(r))

	if !a.strictSanitizedUserDataItems(w, r, merged) {
		return
	}
	payload := map[string]any{
		"SearchHints":      toAnySlice(merged),
		"TotalRecordCount": len(merged),
	}
	if a.parentalResponseReady(w, r, payload) {
		writeJSON(w, http.StatusOK, payload)
	}
}

// imageSourceUsable reports whether a recorded display/image source may still
// serve bytes for this request: the client must exist and be online, the server
// must currently participate in aggregation (a node paused by an administrator
// is never queried, not even anonymously), and an authenticated request must
// hold a server grant for it. Anonymous image requests keep their deliberate
// public reachability (plan §8.8) but still respect the aggregation switch.
func (a *App) imageSourceUsable(reqCtx *RequestContext, authenticated bool, serverID string) bool {
	client := a.Upstream.ClientByID(serverID)
	if client == nil || !client.IsOnline() {
		return false
	}
	if !a.isServerAggregationEnabled(serverID) {
		return false
	}
	if authenticated && !a.isServerAllowed(reqCtx, serverID) {
		return false
	}
	return true
}

func (a *App) handleItemImage(w http.ResponseWriter, r *http.Request) {
	reqCtx := requestContextFrom(r.Context())
	// Distinguish "no credentials" from "credentials present but invalid or
	// disabled". Anonymous image requests stay public (existing behaviour), but a
	// request that carried a token which no longer validates must not be silently
	// downgraded to anonymous — that would let a revoked or disabled user keep
	// reading images. See plan §8.8.
	authenticated := reqCtx != nil && reqCtx.ProxyUser != nil
	if reqCtx != nil && reqCtx.ProxyToken != "" && reqCtx.ProxyUser == nil {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	var resolved *routeResolution
	if authenticated {
		var routeStatus int
		resolved, routeStatus = a.resolveRouteForRequest(reqCtx, r.PathValue("itemId"))
		if routeStatus != 0 {
			w.Header().Set("Cache-Control", "no-store")
			if routeStatus == http.StatusNotFound {
				w.WriteHeader(http.StatusNotFound)
			} else {
				a.writeRouteAccessError(w, routeStatus)
			}
			return
		}
	} else {
		resolved = a.resolveRouteID(r.PathValue("itemId"))
		if resolved == nil {
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusNotFound)
			return
		}
	}
	// the server access check only applies to authenticated requests.
	if authenticated && !a.isServerAllowed(reqCtx, resolved.ServerID) {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusForbidden, map[string]any{"message": "Access denied"})
		return
	}
	imageType := r.PathValue("imageType")
	type imageCandidate struct{ serverID, originalID string }
	var candidates []imageCandidate
	seen := make(map[string]bool)
	appendCandidate := func(serverID, originalID string) {
		if serverID == "" || originalID == "" {
			return
		}
		key := serverID + "\x00" + originalID
		if seen[key] {
			return
		}
		seen[key] = true
		candidates = append(candidates, imageCandidate{serverID: serverID, originalID: originalID})
	}
	tag := r.URL.Query().Get("tag")
	tagOwner := imageCandidate{}
	hasTagOwner := false
	if tag != "" {
		if src, ok := a.IDStore.ResolveImageSource(r.PathValue("itemId"), imageType, tag); ok {
			tagOwner = imageCandidate{serverID: src.ServerID, originalID: src.OriginalID}
			hasTagOwner = true
			appendCandidate(src.ServerID, src.OriginalID)
		}
	}
	if src, ok := a.IDStore.ResolveDisplaySource(r.PathValue("itemId")); ok {
		appendCandidate(src.ServerID, src.OriginalID)
	}
	appendCandidate(resolved.ServerID, resolved.OriginalID)
	for _, src := range resolved.OtherInstances {
		appendCandidate(src.ServerID, src.OriginalID)
	}

	var resp *http.Response
	parentalDenied := false
	for _, candidate := range candidates {
		if r.Context().Err() != nil {
			return
		}
		client := a.Upstream.ClientByID(candidate.serverID)
		if !a.imageSourceUsable(reqCtx, authenticated, candidate.serverID) || client == nil {
			continue
		}
		if authenticated {
			allowed, confirmed := a.checkInstanceParentalAccess(r, client, candidate.originalID)
			if r.Context().Err() != nil {
				return
			}
			if !confirmed {
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			if !allowed {
				parentalDenied = true
				continue
			}
		}
		imagePath := "/Items/" + candidate.originalID + "/Images/" + imageType
		if imageIndex := r.PathValue("imageIndex"); imageIndex != "" {
			imagePath += "/" + imageIndex
		}
		imageQuery := cloneValues(r.URL.Query())
		stripProxyCredentialQuery(imageQuery)
		if !hasTagOwner || candidate != tagOwner {
			// A tag belongs to its recorded source only. Reusing it on another
			// copy can turn a valid fallback into a stale 404 or validator hit.
			imageQuery.Del("tag")
		}
		var err error
		resp, err = client.Stream(r.Context(), reqCtx, a.Identity, imagePath, imageQuery)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		if isRedirectStatus(resp.StatusCode) && !client.Config.FollowRedirects {
			resp.Body.Close()
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		for hop := 0; isRedirectStatus(resp.StatusCode) && hop < maxStreamRedirectHops; hop++ {
			location := resp.Header.Get("Location")
			if location == "" {
				break
			}
			var base *url.URL
			if resp.Request != nil {
				base = resp.Request.URL
			}
			abs, rerr := resolveRedirectTarget(base, location)
			if rerr != nil || abs == nil {
				break
			}
			followed, ferr := client.fetchRedirectTarget(r.Context(), abs.String(), nil)
			if ferr != nil {
				resp.Body.Close()
				if errors.Is(ferr, context.Canceled) || errors.Is(ferr, context.DeadlineExceeded) {
					return
				}
				w.Header().Set("Cache-Control", "no-store")
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			resp.Body.Close()
			resp = followed
		}
		if isRedirectStatus(resp.StatusCode) {
			resp.Body.Close()
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		if resp.StatusCode == http.StatusNotFound {
			resp.Body.Close()
			resp = nil
			continue
		}
		break
	}
	if resp == nil {
		w.Header().Set("Cache-Control", "no-store")
		if parentalDenied {
			writeJSON(w, http.StatusForbidden, map[string]any{"message": "Access denied"})
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
		return
	}
	defer resp.Body.Close()
	// A 304 is reached only after current source, grant and rating checks.
	switch {
	case resp.StatusCode >= 400:
		w.Header().Set("Cache-Control", "no-store")
	case authenticated:
		w.Header().Set("Cache-Control", "private, no-store")
	default:
		w.Header().Set("Cache-Control", "public, max-age=0, must-revalidate")
	}
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
