package backend

import (
	"net/http"
	"net/url"
)

func annotateMediaSource(source map[string]any, client *UpstreamClient) {
	name, _ := source["Name"].(string)
	if name == "" {
		name = "默认版本"
	}
	source["Name"] = name + " (" + client.Name + ")"
	source["EioServerName"] = client.Name
	source["EioServerId"] = client.ID
}

func annotateItemSources(payload any, client *UpstreamClient) {
	if item, ok := payload.(map[string]any); ok {
		for _, source := range asItems(map[string]any{"Items": item["MediaSources"]}) {
			annotateMediaSource(source, client)
		}
	}
}

// A selected version owns both the playback request and its session. Merely
// translating its id while querying every server mixes unrelated sessions.
func (a *App) selectedPlaybackSource(w http.ResponseWriter, r *http.Request, resolved *routeResolution, body map[string]any, query url.Values) (*routeResolution, bool) {
	id, _ := body["MediaSourceId"].(string)
	if id == "" {
		id = query.Get("MediaSourceId")
	}
	source := a.IDStore.ResolveVirtualID(id)
	if source == nil {
		return resolved, true
	}
	if !a.canUseAggregatedServer(requestContextFrom(r.Context()), source.ServerID) {
		writeJSON(w, http.StatusForbidden, map[string]any{"message": "Access denied"})
		return nil, false
	}
	client := a.Upstream.ClientByID(source.ServerID)
	if client == nil || !client.IsOnline() {
		writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Selected media source server is unavailable"})
		return nil, false
	}
	itemID, ok := translateVirtualIDForServer(r.PathValue("itemId"), source.ServerID, a.IDStore)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "Selected version does not belong to this item"})
		return nil, false
	}
	if _, exists := body["MediaSourceId"]; exists {
		body["MediaSourceId"] = source.OriginalID
	}
	if query.Get("MediaSourceId") != "" {
		query.Set("MediaSourceId", source.OriginalID)
	}
	return &routeResolution{OriginalID: itemID, ServerID: source.ServerID, Client: client}, true
}
