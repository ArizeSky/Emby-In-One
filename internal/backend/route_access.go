package backend

import (
	"net/http"
)

// resolveRouteForRequest chooses the first online item instance usable by this
// request. The IDStore remains a global mapping: this only creates a request-
// scoped routeResolution and never changes the virtual ID's primary server.
func (a *App) resolveRouteForRequest(reqCtx *RequestContext, id string) (*routeResolution, int) {
	resolved := a.IDStore.ResolveVirtualID(id)
	if resolved == nil {
		return nil, http.StatusNotFound
	}

	type candidate struct {
		OriginalID string
		ServerID   string
	}
	candidates := make([]candidate, 0, len(resolved.OtherInstances)+1)
	candidates = append(candidates, candidate{OriginalID: resolved.OriginalID, ServerID: resolved.ServerID})
	for _, other := range resolved.OtherInstances {
		candidates = append(candidates, candidate{OriginalID: other.OriginalID, ServerID: other.ServerID})
	}

	hasAuthorizedReplica := false
	for index, current := range candidates {
		if !a.canUseAggregatedServer(reqCtx, current.ServerID) {
			continue
		}
		hasAuthorizedReplica = true
		client := a.Upstream.ClientByID(current.ServerID)
		if client == nil || !client.IsOnline() {
			continue
		}

		others := append([]AdditionalInstance(nil), resolved.OtherInstances...)
		if index > 0 {
			others = make([]AdditionalInstance, 0, len(resolved.OtherInstances))
			others = append(others, AdditionalInstance{OriginalID: resolved.OriginalID, ServerID: resolved.ServerID})
			for _, other := range resolved.OtherInstances {
				if other.OriginalID == current.OriginalID && other.ServerID == current.ServerID {
					continue
				}
				others = append(others, other)
			}
		}
		return &routeResolution{
			OriginalID:     current.OriginalID,
			ServerID:       current.ServerID,
			Client:         client,
			OtherInstances: others,
		}, 0
	}

	if hasAuthorizedReplica {
		return nil, http.StatusServiceUnavailable
	}
	return nil, http.StatusForbidden
}

// writeRouteAccessError emits the stable route-access response shared by
// request-scoped content handlers.
func (a *App) writeRouteAccessError(w http.ResponseWriter, status int) {
	message, code := "Route access unavailable", "RouteAccessUnavailable"
	switch status {
	case http.StatusNotFound:
		message, code = "Item not found", "ItemNotFound"
	case http.StatusForbidden:
		message, code = "Content not available for this account", "ContentNotAvailableForThisAccount"
	case http.StatusServiceUnavailable:
		message, code = "No available replica of this item is online", "NoAvailableReplicaOnline"
	default:
		status = http.StatusInternalServerError
	}
	writeJSON(w, status, map[string]any{"message": message, "code": code})
}
