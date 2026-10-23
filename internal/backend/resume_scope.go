package backend

import (
	"net/http"
	"net/url"
	"strings"
)

const resumeAncestorDepthLimit = 16

type resumeContainerScope struct {
	virtualID  string
	kind       string
	instances  map[string]string // server ID -> authorized original container ID
	budget     *catalogRequestBudget
	incomplete bool
}

func (a *App) newResumeContainerScope(r *http.Request, reqCtx *RequestContext, virtualID string) *resumeContainerScope {
	resolved, status := a.resolveRouteForRequest(reqCtx, virtualID)
	if status != 0 || resolved == nil {
		return nil
	}
	instances := a.collectAllowedInstances(reqCtx, resolved)
	byServer := make(map[string]string, len(instances))
	for _, instance := range instances {
		byServer[instance.ServerID] = instance.OriginalID
	}
	if len(byServer) == 0 {
		return nil
	}

	scope := &resumeContainerScope{
		virtualID: virtualID,
		instances: byServer,
		budget:    newCatalogRequestBudget(),
	}
	if reqCtx != nil && reqCtx.Parental != nil && reqCtx.Parental.catalog != nil {
		scope.budget = reqCtx.Parental.catalog
	}
	payload, err := resolved.Client.RequestJSON(r.Context(), reqCtx, a.Identity, http.MethodGet,
		"/Items/"+url.PathEscape(resolved.OriginalID), url.Values{"Fields": {"Type"}}, nil)
	if err == nil {
		if item, ok := payload.(map[string]any); ok {
			scope.kind, _ = item["Type"].(string)
		}
	}
	return scope
}

// resumeItemInScope verifies the requested container against the watched item's
// actual authorized copy. A server ID is never treated as a library ID: folders
// are followed through their concrete ParentId chain on that same server.
func (a *App) resumeItemInScope(r *http.Request, reqCtx *RequestContext, scope *resumeContainerScope, progress WatchProgress, item map[string]any) (bool, bool) {
	serverID, _, ok := a.resolveWatchItemServer(reqCtx, &progress)
	if !ok {
		return false, false
	}
	targetOriginalID, ok := scope.instances[serverID]
	if !ok {
		return false, false
	}
	if scope.kind == "Series" && a.originalIDForServer(scope.virtualID, serverID) == targetOriginalID && progress.SeriesVirtualID == scope.virtualID {
		return true, false
	}

	for _, field := range []string{"SeasonId", "ParentId", "TopParentId"} {
		if a.originalIDForServer(itemText(item, field), serverID) == targetOriginalID {
			return true, false
		}
	}
	if (scope.kind == "Series" || scope.kind == "") && itemText(item, "SeriesId") != "" &&
		a.originalIDForServer(itemText(item, "SeriesId"), serverID) == targetOriginalID {
		return true, false
	}

	client := a.Upstream.ClientByID(serverID)
	if client == nil {
		return false, false
	}
	parentVirtualID := itemText(item, "ParentId")
	visited := make(map[string]bool)
	for depth := 0; depth < resumeAncestorDepthLimit && parentVirtualID != ""; depth++ {
		parentOriginalID := a.originalIDForServer(parentVirtualID, serverID)
		if parentOriginalID == "" {
			return false, false
		}
		if parentOriginalID == targetOriginalID {
			return true, false
		}
		cacheKey := serverID + "\x00" + parentVirtualID
		if visited[cacheKey] {
			return false, false
		}
		visited[cacheKey] = true
		if !scope.budget.tryAcquire() {
			scope.incomplete = true
			return false, true
		}
		payload, err := client.RequestJSON(r.Context(), reqCtx, a.Identity, http.MethodGet,
			"/Items/"+url.PathEscape(parentOriginalID), nil, nil)
		if err != nil {
			scope.incomplete = true
			return false, true
		}
		parent, ok := payload.(map[string]any)
		if !ok {
			return false, false
		}
		rewriteResponseIDs(parent, serverID, a.IDStore, a.ConfigStore.Snapshot().Server.ID, a.clientFacingUserIDFor(r))
		currentVirtualID := itemID(parent)
		if currentVirtualID != "" && a.originalIDForServer(currentVirtualID, serverID) == targetOriginalID {
			return true, false
		}
		parentVirtualID = itemText(parent, "ParentId")
	}
	return false, false
}

func (a *App) originalIDForServer(virtualID, serverID string) string {
	if strings.TrimSpace(virtualID) == "" || a.IDStore == nil {
		return ""
	}
	resolved := a.IDStore.ResolveVirtualID(virtualID)
	if resolved == nil {
		return ""
	}
	if resolved.ServerID == serverID {
		return resolved.OriginalID
	}
	for _, other := range resolved.OtherInstances {
		if other.ServerID == serverID {
			return other.OriginalID
		}
	}
	return ""
}
