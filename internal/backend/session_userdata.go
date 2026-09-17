package backend

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
)

func (a *App) registerSessionAndUserStateRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /Sessions/Playing", a.withContext(a.requireAuth(a.handleSessionPlaying)))
	mux.HandleFunc("POST /Sessions/Playing/Progress", a.withContext(a.requireAuth(a.handleSessionPlayingProgress)))
	mux.HandleFunc("POST /Sessions/Playing/Stopped", a.withContext(a.requireAuth(a.handleSessionPlayingStopped)))
	mux.HandleFunc("POST /Sessions/Capabilities", a.withContext(a.requireAuth(a.handleSessionsCapabilities)))
	mux.HandleFunc("POST /Sessions/Capabilities/Full", a.withContext(a.requireAuth(a.handleSessionsCapabilitiesFull)))
	mux.HandleFunc("POST /Users/{userId}/PlayingItems/{itemId}", a.withContext(a.requireAuth(a.handleUserPlayingItemStart)))
	mux.HandleFunc("DELETE /Users/{userId}/PlayingItems/{itemId}", a.withContext(a.requireAuth(a.handleUserPlayingItemStop)))
	mux.HandleFunc("POST /Users/{userId}/Items/{itemId}/UserData", a.withContext(a.requireAuth(a.handleUserItemUserData)))
	mux.HandleFunc("POST /Users/{userId}/FavoriteItems/{itemId}", a.withContext(a.requireAuth(a.handleFavoriteItemAdd)))
	mux.HandleFunc("DELETE /Users/{userId}/FavoriteItems/{itemId}", a.withContext(a.requireAuth(a.handleFavoriteItemRemove)))
}

func decodeOptionalJSON(r *http.Request) (any, error) {
	if r.Body == nil {
		return nil, nil
	}
	defer r.Body.Close()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, nil
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func (a *App) translateSessionBodyIDs(body map[string]any) (string, bool) {
	// Resolve each ID independently to its OWN server's original value.
	// This matches the Node.js reference implementation where each virtual ID
	// is translated to its own server's original regardless of the target server.
	//
	// Target server priority: ItemId → MediaSourceId → PlaySessionId → ActiveStream.
	// This ensures session events are routed to the server that owns the episode
	// (ItemId), not the server that owns the media source. The upstream Emby
	// associates resume/progress data with ItemId, so the ItemId must be valid
	// on the target server.

	type resolvedID struct {
		OriginalID string
		ServerID   string
	}

	resolutions := map[string]*resolvedID{} // key → resolved
	for _, key := range []string{"ItemId", "MediaSourceId", "PlaySessionId"} {
		text, _ := body[key].(string)
		if text == "" {
			continue
		}
		resolved := a.IDStore.ResolveVirtualID(text)
		if resolved == nil {
			resolved = a.IDStore.ResolveByOriginalID(text)
		}
		if resolved != nil {
			resolutions[key] = &resolvedID{
				OriginalID: resolved.OriginalID,
				ServerID:   resolved.ServerID,
			}
			body[key] = resolved.OriginalID
		}
	}

	// Determine target server: prefer ItemId's server (matches Node.js)
	serverID := ""
	for _, key := range []string{"ItemId", "MediaSourceId", "PlaySessionId"} {
		if r, ok := resolutions[key]; ok {
			serverID = r.ServerID
			break
		}
	}

	if serverID == "" {
		// Last resort: check which server last served a PlaybackInfo for ItemId
		if itemID, _ := body["ItemId"].(string); itemID != "" {
			if idx, ok := a.IDStore.GetActiveStream(itemID); ok {
				serverID = idx
			}
		}
	}

	if a.Logger != nil {
		a.Logger.Debugf("Session translation: TargetServer=%s, MediaSourceId=%v, ItemId=%v, PlaySessionId=%v",
			serverID, body["MediaSourceId"], body["ItemId"], body["PlaySessionId"])
	}
	return serverID, serverID != ""
}

// recordSessionToWatchStore writes playback progress to the local WatchStore
// for non-admin users. virtualItemID is the pre-translation virtual ID.
// isStopped indicates whether the playback has ended (Stopped event).
func (a *App) recordSessionToWatchStore(r *http.Request, virtualItemID string, body map[string]any, serverID string, isStopped bool) {
	if a.WatchStore == nil || virtualItemID == "" {
		return
	}
	reqCtx := requestContextFrom(r.Context())
	if reqCtx == nil || reqCtx.ProxyUser == nil || reqCtx.ProxyUser.Role == "admin" {
		return
	}
	positionTicks, _ := numericInt64(body["PositionTicks"])
	runtimeTicks, _ := numericInt64(body["RunTimeTicks"])
	originalItemID, _ := body["ItemId"].(string)

	p := &WatchProgress{
		ProxyUserID:    reqCtx.ProxyUser.UserID,
		VirtualItemID:  virtualItemID,
		ServerID:       serverID,
		OriginalItemID: originalItemID,
		PositionTicks:  positionTicks,
		RuntimeTicks:   runtimeTicks,
	}

	// Auto-mark played if stopped near end (>= 90% of runtime)
	if isStopped && runtimeTicks > 0 && positionTicks > 0 {
		ratio := float64(positionTicks) / float64(runtimeTicks)
		if ratio >= 0.90 {
			p.Played = true
			p.PositionTicks = 0
		}
	}

	// Enrich with item metadata if not already stored
	existing := a.WatchStore.GetProgress(reqCtx.ProxyUser.UserID, virtualItemID)
	if existing == nil || existing.ItemType == "" {
		a.enrichWatchProgressMetadata(r, reqCtx, p, originalItemID, serverID)
	}

	if err := a.WatchStore.RecordProgress(p); err != nil {
		if a.Logger != nil {
			a.Logger.Warnf("WatchStore record error: %s", redactURLInError(err))
		}
	}
}

// enrichWatchProgressMetadata fetches item details from upstream and populates
// metadata fields on the WatchProgress (type, series info, name, etc.).
func (a *App) enrichWatchProgressMetadata(r *http.Request, reqCtx *RequestContext, p *WatchProgress, originalItemID string, serverID string) {
	client := a.Upstream.ClientByID(serverID)
	if client == nil || !client.IsOnline() || originalItemID == "" {
		return
	}
	q := url.Values{}
	q.Set("Fields", "ProviderIds")
	payload, err := client.RequestJSON(r.Context(), reqCtx, a.Identity, http.MethodGet, "/Users/"+client.clientUserID()+"/Items/"+originalItemID, q, nil)
	if err != nil {
		return
	}
	item, ok := payload.(map[string]any)
	if !ok {
		return
	}
	p.ItemType, _ = item["Type"].(string)
	p.Name, _ = item["Name"].(string)
	if year, ok := numericInt(item["ProductionYear"]); ok {
		p.ProductionYear = year
	}
	if providerIDs, ok := item["ProviderIds"].(map[string]any); ok {
		p.ProviderTmdb, _ = providerIDs["Tmdb"].(string)
	}
	if rt, ok := numericInt64(item["RunTimeTicks"]); ok && rt > 0 && p.RuntimeTicks == 0 {
		p.RuntimeTicks = rt
	}

	// Episode-specific: series info
	if p.ItemType == "Episode" {
		p.SeriesName, _ = item["SeriesName"].(string)
		if parentIdx, ok := numericInt(item["ParentIndexNumber"]); ok {
			p.ParentIndexNumber = parentIdx
		}
		if idx, ok := numericInt(item["IndexNumber"]); ok {
			p.IndexNumber = idx
		}
		// Map seriesId to virtual
		if seriesID, _ := item["SeriesId"].(string); seriesID != "" {
			p.SeriesOriginalID = seriesID
			p.SeriesVirtualID = a.IDStore.GetOrCreateVirtualID(seriesID, serverID)
		}
	}
}

// numericInt64 extracts an int64 from a JSON number (float64) or returns 0.
func numericInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(math.Round(n)), true
	case int64:
		return n, true
	case int:
		return int64(n), true
	}
	return 0, false
}

func (a *App) translateMediaSourceQuery(values url.Values) {
	if mediaSourceID := values.Get("MediaSourceId"); mediaSourceID != "" {
		if resolved := a.IDStore.ResolveVirtualID(mediaSourceID); resolved != nil {
			values.Set("MediaSourceId", resolved.OriginalID)
		}
	}
}

func (a *App) performUpstream(ctx *http.Request, client *UpstreamClient, method, path string, query url.Values, body any) (*http.Response, error) {
	reqCtx := requestContextFrom(ctx.Context())
	return client.doRequest(ctx.Context(), reqCtx, method, path, query, body, client.requestHeaders(reqCtx, a.Identity), false)
}

func readUpstreamJSONOrNoContent(resp *http.Response) (int, any, error) {
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		payload, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, nil, fmt.Errorf("upstream request failed: %s %s", resp.Status, string(bytes.TrimSpace(payload)))
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || resp.StatusCode == http.StatusNoContent {
		return resp.StatusCode, nil, nil
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, payload, nil
}

func (a *App) forwardNoContent(r *http.Request, client *UpstreamClient, method, path string, query url.Values, body any) error {
	resp, err := a.performUpstream(r, client, method, path, query, body)
	if err != nil {
		return err
	}
	_, _, err = readUpstreamJSONOrNoContent(resp)
	return err
}

func (a *App) forwardJSONOrNoContent(r *http.Request, client *UpstreamClient, method, path string, query url.Values, body any) (int, any, error) {
	resp, err := a.performUpstream(r, client, method, path, query, body)
	if err != nil {
		return 0, nil, err
	}
	return readUpstreamJSONOrNoContent(resp)
}

func asBodyMap(payload any) (map[string]any, bool) {
	if payload == nil {
		return map[string]any{}, true
	}
	body, ok := payload.(map[string]any)
	return body, ok
}

func (a *App) handleSessionPlaying(w http.ResponseWriter, r *http.Request) {
	payload, err := decodeOptionalJSON(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "Invalid JSON body"})
		return
	}
	body, ok := asBodyMap(payload)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "Invalid session payload"})
		return
	}
	virtualItemID, _ := body["ItemId"].(string) // capture before translation
	serverID, found := a.translateSessionBodyIDs(body)
	if !found {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "Cannot determine target server"})
		return
	}
	if !a.isServerAllowed(requestContextFrom(r.Context()), serverID) {
		writeJSON(w, http.StatusForbidden, map[string]any{"message": "Access denied"})
		return
	}
	client := a.Upstream.ClientByID(serverID)
	if client == nil || !client.IsOnline() {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Server not found"})
		return
	}
	if err := a.forwardNoContent(r, client, http.MethodPost, "/Sessions/Playing", nil, body); err != nil {
		// A preparation failure means the request never left the process, so the
		// client gets the preparation status and no local progress is recorded for
		// a play event that was never announced upstream.
		if status, ok := preparationErrorStatus(err); ok {
			writeJSON(w, status, preparationErrorBody(err))
			return
		}
		if a.Logger != nil {
			a.Logger.Warnf("Sessions/Playing upstream error (server %s): %v", serverID, redactURLInError(err))
		}
	}
	a.recordSessionToWatchStore(r, virtualItemID, body, serverID, false)
	if a.PlaybackLimiter != nil {
		if reqCtx := requestContextFrom(r.Context()); reqCtx != nil && reqCtx.ProxyUser != nil && reqCtx.ProxyUser.Role != "admin" {
			a.PlaybackLimiter.Heartbeat(reqCtx.ProxyUser.UserID, serverID)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleSessionPlayingProgress(w http.ResponseWriter, r *http.Request) {
	payload, err := decodeOptionalJSON(r)
	if err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	body, ok := asBodyMap(payload)
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	virtualItemID, _ := body["ItemId"].(string)
	serverID, found := a.translateSessionBodyIDs(body)
	if !found {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !a.isServerAllowed(requestContextFrom(r.Context()), serverID) {
		writeJSON(w, http.StatusForbidden, map[string]any{"message": "Access denied"})
		return
	}
	client := a.Upstream.ClientByID(serverID)
	if client == nil || !client.IsOnline() {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := a.forwardNoContent(r, client, http.MethodPost, "/Sessions/Playing/Progress", nil, body); err != nil {
		// Progress used to swallow every error as 204. A preparation failure is the
		// client's or the configuration's, and reporting it is what makes the
		// failure visible instead of looking like a successful report.
		if status, ok := preparationErrorStatus(err); ok {
			writeJSON(w, status, preparationErrorBody(err))
			return
		}
	}
	a.recordSessionToWatchStore(r, virtualItemID, body, serverID, false)
	if a.PlaybackLimiter != nil {
		if reqCtx := requestContextFrom(r.Context()); reqCtx != nil && reqCtx.ProxyUser != nil && reqCtx.ProxyUser.Role != "admin" {
			a.PlaybackLimiter.Heartbeat(reqCtx.ProxyUser.UserID, serverID)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleSessionPlayingStopped(w http.ResponseWriter, r *http.Request) {
	payload, err := decodeOptionalJSON(r)
	if err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	body, ok := asBodyMap(payload)
	if !ok {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	virtualItemID, _ := body["ItemId"].(string)
	serverID, found := a.translateSessionBodyIDs(body)
	if !found {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !a.isServerAllowed(requestContextFrom(r.Context()), serverID) {
		writeJSON(w, http.StatusForbidden, map[string]any{"message": "Access denied"})
		return
	}
	client := a.Upstream.ClientByID(serverID)
	if client == nil || !client.IsOnline() {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := a.forwardNoContent(r, client, http.MethodPost, "/Sessions/Playing/Stopped", nil, body); err != nil {
		if status, ok := preparationErrorStatus(err); ok {
			// The stop still has to release the concurrency slot and record the local
			// progress: leaving them behind would strand a playback permit.
			a.recordSessionToWatchStore(r, virtualItemID, body, serverID, true)
			if a.PlaybackLimiter != nil {
				if reqCtx := requestContextFrom(r.Context()); reqCtx != nil && reqCtx.ProxyUser != nil && reqCtx.ProxyUser.Role != "admin" {
					a.PlaybackLimiter.Stop(reqCtx.ProxyUser.UserID, serverID)
				}
			}
			writeJSON(w, status, preparationErrorBody(err))
			return
		}
	}
	a.recordSessionToWatchStore(r, virtualItemID, body, serverID, true)
	if a.PlaybackLimiter != nil {
		if reqCtx := requestContextFrom(r.Context()); reqCtx != nil && reqCtx.ProxyUser != nil && reqCtx.ProxyUser.Role != "admin" {
			a.PlaybackLimiter.Stop(reqCtx.ProxyUser.UserID, serverID)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleSessionsCapabilities(w http.ResponseWriter, r *http.Request) {
	a.broadcastCapabilities(w, r, "/Sessions/Capabilities", true)
}

func (a *App) handleSessionsCapabilitiesFull(w http.ResponseWriter, r *http.Request) {
	a.broadcastCapabilities(w, r, "/Sessions/Capabilities/Full", false)
}

// broadcastCapabilities relays a capabilities broadcast to every allowed
// upstream. Each upstream gets its own copy of the body, so preparing one
// upstream's identity can never modify the body another upstream will receive.
// A preparation failure is reported with its own status instead of being hidden
// behind the best-effort 204.
func (a *App) broadcastCapabilities(w http.ResponseWriter, r *http.Request, path string, forwardQuery bool) {
	body, err := decodeOptionalJSON(r)
	if err != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	reqCtx := requestContextFrom(r.Context())
	var query url.Values
	if forwardQuery {
		query = cloneValues(r.URL.Query())
	}
	for _, client := range a.allowedClients(reqCtx) {
		if err := a.forwardNoContent(r, client, http.MethodPost, path, query, body); err != nil {
			if status, ok := preparationErrorStatus(err); ok {
				writeJSON(w, status, preparationErrorBody(err))
				return
			}
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleUserPlayingItemStart(w http.ResponseWriter, r *http.Request) {
	a.handleUserPlayingItem(w, r, http.MethodPost)
}

func (a *App) handleUserPlayingItemStop(w http.ResponseWriter, r *http.Request) {
	a.handleUserPlayingItem(w, r, http.MethodDelete)
}

func (a *App) handleUserPlayingItem(w http.ResponseWriter, r *http.Request, method string) {
	resolved := a.resolveRouteID(r.PathValue("itemId"))
	if resolved == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Item not found"})
		return
	}
	if !a.requireServerAccess(w, r, resolved) {
		return
	}
	query := cloneValues(r.URL.Query())
	a.translateMediaSourceQuery(query)
	path := "/Users/" + resolved.Client.clientUserID() + "/PlayingItems/" + resolved.OriginalID
	_ = a.forwardNoContent(r, resolved.Client, method, path, query, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleUserItemUserData(w http.ResponseWriter, r *http.Request) {
	virtualItemID := r.PathValue("itemId")
	resolved := a.resolveRouteID(virtualItemID)
	if resolved == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Item not found"})
		return
	}
	if !a.requireServerAccess(w, r, resolved) {
		return
	}
	body, err := decodeOptionalJSON(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "Invalid JSON body"})
		return
	}
	var playedValue *bool
	if bodyMap, ok := body.(map[string]any); ok {
		if played, ok := bodyMap["Played"].(bool); ok {
			playedValue = &played
		}
	}
	status, payload, err := a.forwardJSONOrNoContent(r, resolved.Client, http.MethodPost, fmt.Sprintf("/Users/%s/Items/%s/UserData", resolved.Client.clientUserID(), resolved.OriginalID), nil, body)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return
	}
	// Dual-write: record played status to WatchStore for non-admin users. This runs
	// only after the upstream accepted the change, matching handleFavoriteItemAdd and
	// handleFavoriteItemRemove. Writing first left the local state permanently ahead of
	// the upstream whenever the forward failed — and MarkPlayed inserts a skeleton row
	// (server_index = 0) even for an item that was never watched.
	if a.WatchStore != nil {
		if reqCtx := requestContextFrom(r.Context()); reqCtx != nil && reqCtx.ProxyUser != nil && reqCtx.ProxyUser.Role != "admin" && playedValue != nil {
			_ = a.WatchStore.MarkPlayed(reqCtx.ProxyUser.UserID, virtualItemID, *playedValue)
		}
	}
	if payload == nil {
		if status == 0 {
			status = http.StatusNoContent
		}
		w.WriteHeader(status)
		return
	}
	// Overlay local UserData for non-admin users
	a.overlayLocalUserData(r, virtualItemID, payload)
	cfg := a.ConfigStore.Snapshot()
	rewriteResponseIDs(payload, resolved.ServerID, a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
	writeJSON(w, status, payload)
}

func (a *App) handleFavoriteItemAdd(w http.ResponseWriter, r *http.Request) {
	virtualItemID := r.PathValue("itemId")
	resolved := a.resolveRouteID(virtualItemID)
	if resolved == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Item not found"})
		return
	}
	if !a.requireServerAccess(w, r, resolved) {
		return
	}
	body, err := decodeOptionalJSON(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "Invalid JSON body"})
		return
	}
	status, payload, err := a.forwardJSONOrNoContent(r, resolved.Client, http.MethodPost, fmt.Sprintf("/Users/%s/FavoriteItems/%s", resolved.Client.clientUserID(), resolved.OriginalID), nil, body)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return
	}
	// Dual-write favorite
	if a.WatchStore != nil {
		if reqCtx := requestContextFrom(r.Context()); reqCtx != nil && reqCtx.ProxyUser != nil && reqCtx.ProxyUser.Role != "admin" {
			_ = a.WatchStore.SetFavorite(reqCtx.ProxyUser.UserID, virtualItemID, true)
		}
	}
	if payload == nil {
		if status == 0 {
			status = http.StatusNoContent
		}
		w.WriteHeader(status)
		return
	}
	a.overlayLocalUserData(r, virtualItemID, payload)
	cfg := a.ConfigStore.Snapshot()
	rewriteResponseIDs(payload, resolved.ServerID, a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
	writeJSON(w, status, payload)
}

func (a *App) handleFavoriteItemRemove(w http.ResponseWriter, r *http.Request) {
	virtualItemID := r.PathValue("itemId")
	resolved := a.resolveRouteID(virtualItemID)
	if resolved == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Item not found"})
		return
	}
	if !a.requireServerAccess(w, r, resolved) {
		return
	}
	body, err := decodeOptionalJSON(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "Invalid JSON body"})
		return
	}
	status, payload, err := a.forwardJSONOrNoContent(r, resolved.Client, http.MethodDelete, fmt.Sprintf("/Users/%s/FavoriteItems/%s", resolved.Client.clientUserID(), resolved.OriginalID), nil, body)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"message": err.Error()})
		return
	}
	// Dual-write favorite removal
	if a.WatchStore != nil {
		if reqCtx := requestContextFrom(r.Context()); reqCtx != nil && reqCtx.ProxyUser != nil && reqCtx.ProxyUser.Role != "admin" {
			_ = a.WatchStore.SetFavorite(reqCtx.ProxyUser.UserID, virtualItemID, false)
		}
	}
	if payload == nil {
		if status == 0 {
			status = http.StatusNoContent
		}
		w.WriteHeader(status)
		return
	}
	a.overlayLocalUserData(r, virtualItemID, payload)
	cfg := a.ConfigStore.Snapshot()
	rewriteResponseIDs(payload, resolved.ServerID, a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
	writeJSON(w, status, payload)
}

// overlayLocalUserData patches the UserData fields in a response payload
// to reflect the local per-user state (for non-admin users only).
// overlayLocalUserDataItems overlays local UserData on each item in a list.
// Items must have an "Id" field with the virtual item ID.
func (a *App) overlayLocalUserDataItems(r *http.Request, items []map[string]any) {
	if a.WatchStore == nil {
		return
	}
	reqCtx := requestContextFrom(r.Context())
	if reqCtx == nil || reqCtx.ProxyUser == nil || reqCtx.ProxyUser.Role == "admin" {
		return
	}
	for _, item := range items {
		if id, _ := item["Id"].(string); id != "" {
			a.overlayLocalUserData(r, id, item)
		}
	}
}

func (a *App) overlayLocalUserData(r *http.Request, virtualItemID string, payload any) {
	if a.WatchStore == nil {
		return
	}
	reqCtx := requestContextFrom(r.Context())
	if reqCtx == nil || reqCtx.ProxyUser == nil || reqCtx.ProxyUser.Role == "admin" {
		return
	}
	progress := a.WatchStore.GetProgress(reqCtx.ProxyUser.UserID, virtualItemID)
	if progress == nil {
		// No local record → clear upstream admin's UserData to avoid leaking
		clearUpstreamUserData(payload)
		return
	}
	m, ok := payload.(map[string]any)
	if !ok {
		return
	}
	// Overlay top-level fields if this IS a UserData object
	if _, hasPlayPos := m["PlaybackPositionTicks"]; hasPlayPos {
		m["PlaybackPositionTicks"] = progress.PositionTicks
		m["Played"] = progress.Played
		m["IsFavorite"] = progress.IsFavorite
	}
	// Overlay nested UserData if present
	if ud, ok := m["UserData"].(map[string]any); ok {
		ud["PlaybackPositionTicks"] = progress.PositionTicks
		ud["Played"] = progress.Played
		ud["IsFavorite"] = progress.IsFavorite
	}
}

// clearUpstreamUserData resets UserData fields to a clean state, preventing
// the upstream admin's watch history from leaking to non-admin users.
func clearUpstreamUserData(payload any) {
	m, ok := payload.(map[string]any)
	if !ok {
		return
	}
	// Top-level UserData fields (when payload IS a UserData object)
	if _, hasPlayPos := m["PlaybackPositionTicks"]; hasPlayPos {
		m["PlaybackPositionTicks"] = 0
		m["Played"] = false
		m["IsFavorite"] = false
		m["PlayedPercentage"] = 0
		delete(m, "LastPlayedDate")
	}
	// Nested UserData (when payload is an item with UserData sub-object)
	if ud, ok := m["UserData"].(map[string]any); ok {
		ud["PlaybackPositionTicks"] = 0
		ud["Played"] = false
		ud["IsFavorite"] = false
		ud["PlayedPercentage"] = 0
		delete(ud, "LastPlayedDate")
	}
}
