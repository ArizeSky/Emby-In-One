package backend

import (
	"net/http"
	"net/url"
	"sort"
	"strings"
)

// Library-scoped listings intentionally keep their source's content. Discover
// exact provider matches on detail so a copy outside that library can still
// contribute versions and episodes, without a whole-catalog scan.
func (a *App) discoverItemCopies(r *http.Request, virtualID string, item map[string]any) {
	kind, _ := item["Type"].(string)
	if kind != "Series" && kind != "Movie" && kind != "Episode" {
		return
	}
	providers, _ := item["ProviderIds"].(map[string]any)
	terms := []string{}
	for key, value := range providers {
		if id, ok := value.(string); ok && id != "" {
			terms = append(terms, strings.ToLower(key)+"."+id)
		}
	}
	if len(terms) == 0 {
		return
	}
	sort.Strings(terms)
	reqCtx := requestContextFrom(r.Context())
	known := map[string]bool{}
	for _, inst := range a.collectAllowedInstances(reqCtx, a.resolveRouteID(virtualID)) {
		known[inst.ServerID] = true
	}
	for _, client := range a.allowedClients(reqCtx) {
		if known[client.ID] {
			continue
		}
		query := url.Values{"AnyProviderIdEquals": {strings.Join(terms, ",")}, "IncludeItemTypes": {kind}, "Recursive": {"true"}, "Fields": {"ProviderIds"}, "Limit": {"100"}}
		payload, err := client.RequestJSON(r.Context(), reqCtx, a.Identity, http.MethodGet, "/Users/"+client.clientUserID()+"/Items", query, nil)
		if err != nil {
			continue
		}
		for _, candidate := range asItems(payload) {
			if candidate["Type"] != kind || !matchingProvider(providers, candidate) {
				continue
			}
			if id, _ := candidate["Id"].(string); id != "" {
				a.IDStore.AssociateAdditionalInstance(virtualID, id, client.ID)
			}
		}
	}
}

func matchingProvider(providers map[string]any, candidate map[string]any) bool {
	other, _ := candidate["ProviderIds"].(map[string]any)
	for key, value := range providers {
		id, ok := value.(string)
		if !ok || id == "" {
			continue
		}
		for otherKey, otherValue := range other {
			if strings.EqualFold(key, otherKey) && id == otherValue {
				return true
			}
		}
	}
	return false
}
