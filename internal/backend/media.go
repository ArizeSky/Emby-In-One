package backend

import (
	"net/http"
	"net/url"
)

type routeResolution struct {
	OriginalID     string
	ServerIndex    int
	Client         *UpstreamClient
	OtherInstances []AdditionalInstance
}

type upstreamItemsResult struct {
	ServerIndex int
	Items       []map[string]any
}

func (a *App) registerMediaRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /Users/{userId}/Items", a.withContext(a.requireAuth(a.handleUserItems)))
	mux.HandleFunc("GET /Items", a.withContext(a.requireAuth(a.handleItemsCollection)))
	mux.HandleFunc("GET /Users/{userId}/Items/Resume", a.withContext(a.requireAuth(a.handleUserItemsResume)))
	mux.HandleFunc("GET /Users/{userId}/Items/Latest", a.withContext(a.requireAuth(a.handleUserItemsLatest)))
	mux.HandleFunc("GET /Users/{userId}/Items/{itemId}", a.withContext(a.requireAuth(a.handleUserItemByID)))
	mux.HandleFunc("GET /Items/{itemId}", a.withContext(a.requireAuth(a.handleItemByID)))
	mux.HandleFunc("GET /Items/{itemId}/Similar", a.withContext(a.requireAuth(a.handleItemSimilar)))
	mux.HandleFunc("GET /Items/{itemId}/ThemeMedia", a.withContext(a.requireAuth(a.handleItemThemeMedia)))
	mux.HandleFunc("GET /Shows/NextUp", a.withContext(a.requireAuth(a.handleShowsNextUp)))
	mux.HandleFunc("GET /Items/{itemId}/PlaybackInfo", a.withContext(a.requireAuth(a.handlePlaybackInfo)))
	mux.HandleFunc("POST /Items/{itemId}/PlaybackInfo", a.withContext(a.requireAuth(a.handlePlaybackInfo)))
	mux.HandleFunc("GET /Videos/{itemId}/{rest...}", a.withContext(a.requireAuth(a.handleVideoProxy)))
	mux.HandleFunc("GET /Audio/{itemId}/{rest...}", a.withContext(a.requireAuth(a.handleAudioProxy)))
	mux.HandleFunc("DELETE /Videos/ActiveEncodings", a.withContext(a.requireAuth(a.handleDeleteActiveEncodings)))
}

func (a *App) resolveRouteID(id string) *routeResolution {
	resolved := a.IDStore.ResolveVirtualID(id)
	if resolved == nil {
		return nil
	}
	// Try primary instance first
	client := a.Upstream.GetClient(resolved.ServerIndex)
	if client != nil && client.IsOnline() {
		return &routeResolution{
			OriginalID:     resolved.OriginalID,
			ServerIndex:    resolved.ServerIndex,
			Client:         client,
			OtherInstances: append([]AdditionalInstance(nil), resolved.OtherInstances...),
		}
	}
	// Primary offline — try OtherInstances
	for _, other := range resolved.OtherInstances {
		alt := a.Upstream.GetClient(other.ServerIndex)
		if alt != nil && alt.IsOnline() {
			remaining := make([]AdditionalInstance, 0, len(resolved.OtherInstances))
			remaining = append(remaining, AdditionalInstance{
				OriginalID:  resolved.OriginalID,
				ServerIndex: resolved.ServerIndex,
			})
			for _, oi := range resolved.OtherInstances {
				if oi.ServerIndex != other.ServerIndex || oi.OriginalID != other.OriginalID {
					remaining = append(remaining, oi)
				}
			}
			return &routeResolution{
				OriginalID:     other.OriginalID,
				ServerIndex:    other.ServerIndex,
				Client:         alt,
				OtherInstances: remaining,
			}
		}
	}
	return nil
}

// collectAllowedInstances returns the online instances of an item that the
// current user may access. ID mappings are global, so an item's OtherInstances
// can reference upstream servers the user has no permission for; those are
// dropped here so no handler forwards a request to them.
func (a *App) collectAllowedInstances(reqCtx *RequestContext, resolved *routeResolution) []seriesInstance {
	instances := buildSeriesInstances(resolved, a.Upstream)
	if reqCtx == nil || reqCtx.ProxyUser == nil || reqCtx.ProxyUser.AllowedServers == nil {
		return instances
	}
	allowed := instances[:0]
	for _, instance := range instances {
		if a.isServerAllowed(reqCtx, instance.ServerIndex) {
			allowed = append(allowed, instance)
		}
	}
	return allowed
}

func cloneValues(values url.Values) url.Values {
	cloned := url.Values{}
	for key, rawValues := range values {
		cloned[key] = append([]string(nil), rawValues...)
	}
	return cloned
}

func asItems(payload any) []map[string]any {
	switch typed := payload.(type) {
	case []any:
		items := make([]map[string]any, 0, len(typed))
		for _, raw := range typed {
			if item, ok := raw.(map[string]any); ok {
				items = append(items, item)
			}
		}
		return items
	case map[string]any:
		if rawItems, ok := typed["Items"]; ok {
			return asItems(rawItems)
		}
		if rawItems, ok := typed["items"]; ok {
			return asItems(rawItems)
		}
	}
	return []map[string]any{}
}

// rewriteItems rewrites the resource IDs of a list of items and reports the
// current user's own ID as the response identity. clientUserID is passed in
// explicitly so a background aggregation goroutine can never fall back to the
// global admin placeholder.
func (a *App) rewriteItems(items []map[string]any, serverIndex int, clientUserID string) []map[string]any {
	cfg := a.ConfigStore.Snapshot()
	for _, item := range items {
		rewriteResponseIDs(item, serverIndex, a.IDStore, cfg.Server.ID, clientUserID)
	}
	return items
}
