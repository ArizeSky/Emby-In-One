package backend

import (
	"context"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// upstreamLibrariesTTL bounds how long a fetched library list stays fresh.
// Library lists change rarely; the cache keeps the admin panel's checkbox
// modal from waiting on every upstream each time it opens.
const upstreamLibrariesTTL = 10 * time.Minute

type cachedLibraryList struct {
	libraries []map[string]any
	expiresAt time.Time
}

// upstreamLibraryCache memoizes per-server library lists for the admin panel.
type upstreamLibraryCache struct {
	mu      sync.Mutex
	entries map[string]cachedLibraryList
}

func newUpstreamLibraryCache() *upstreamLibraryCache {
	return &upstreamLibraryCache{entries: map[string]cachedLibraryList{}}
}

func (c *upstreamLibraryCache) get(serverID string) ([]map[string]any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[serverID]
	if !ok || time.Now().After(entry.expiresAt) {
		return nil, false
	}
	return entry.libraries, true
}

// getStale returns a cached list regardless of age, so an offline upstream
// can still be configured from its last known library list.
func (c *upstreamLibraryCache) getStale(serverID string) ([]map[string]any, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[serverID]
	if !ok {
		return nil, false
	}
	return entry.libraries, true
}

func (c *upstreamLibraryCache) set(serverID string, libraries []map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[serverID] = cachedLibraryList{libraries: libraries, expiresAt: time.Now().Add(upstreamLibrariesTTL)}
}

func (c *upstreamLibraryCache) invalidate(serverID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, serverID)
}

// invalidateUpstreamLibraryCache drops the cached list after a server's
// connection details changed or the server was deleted.
func (a *App) invalidateUpstreamLibraryCache(serverID string) {
	if a.libraryCache != nil && serverID != "" {
		a.libraryCache.invalidate(serverID)
	}
}

// fetchUpstreamLibraries returns one upstream's libraries as
// [{id, name, collectionType}] with original (pre-virtualization) IDs,
// serving from the TTL cache unless refresh is set.
func (a *App) fetchUpstreamLibraries(ctx context.Context, reqCtx *RequestContext, client *UpstreamClient, refresh bool) ([]map[string]any, error) {
	if a.libraryCache != nil && !refresh {
		if libraries, ok := a.libraryCache.get(client.ID); ok {
			return libraries, nil
		}
	}
	userID := client.clientUserID()
	params := url.Values{"UserId": []string{userID}}
	payload, err := client.RequestJSON(ctx, reqCtx, a.Identity, http.MethodGet, "/Users/"+userID+"/Views", params, nil)
	if err != nil {
		return nil, err
	}
	libraries := make([]map[string]any, 0, 8)
	for _, item := range asItems(payload) {
		id, _ := item["Id"].(string)
		if id == "" {
			continue
		}
		name, _ := item["Name"].(string)
		collectionType, _ := item["CollectionType"].(string)
		libraries = append(libraries, map[string]any{
			"id":             id,
			"name":           name,
			"collectionType": collectionType,
		})
	}
	if a.libraryCache != nil {
		a.libraryCache.set(client.ID, libraries)
	}
	return libraries, nil
}

// handleAdminUpstreamLibraries serves one upstream's library list for the
// admin panel's home-library checkboxes. An offline upstream falls back to
// the stale cache; with no cache at all the failure is reported.
func (a *App) handleAdminUpstreamLibraries(w http.ResponseWriter, r *http.Request) {
	cfg := a.ConfigStore.Snapshot()
	index, existing, ok := parsePathUpstream(r, &cfg)
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	client := a.Upstream.ClientByID(existing.ID)
	if client == nil {
		client = a.Upstream.GetClient(index)
	}
	if client == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Server not found"})
		return
	}
	if client.IsOnline() {
		refresh := r.URL.Query().Get("refresh") == "1"
		libraries, err := a.fetchUpstreamLibraries(r.Context(), requestContextFrom(r.Context()), client, refresh)
		if err == nil {
			writeJSON(w, http.StatusOK, libraries)
			return
		}
		if a.libraryCache != nil {
			if stale, cached := a.libraryCache.getStale(client.ID); cached {
				writeJSON(w, http.StatusOK, stale)
				return
			}
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": "上游库列表获取失败：" + err.Error()})
		return
	}
	if a.libraryCache != nil {
		if stale, cached := a.libraryCache.getStale(client.ID); cached {
			writeJSON(w, http.StatusOK, stale)
			return
		}
	}
	writeJSON(w, http.StatusBadGateway, map[string]any{"error": "服务器离线，无法获取库列表"})
}

// handleAdminHomeLibrariesGet returns the administrator's own hidden config.
func (a *App) handleAdminHomeLibrariesGet(w http.ResponseWriter, r *http.Request) {
	if a.HiddenLibraries == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "数据库不可用，首页库隐藏未启用"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"hidden": a.HiddenLibraries.UserHiddenJSON(adminVisibilityUserID),
	})
}

// handleAdminHomeLibrariesPut applies the administrator's own hidden config
// with patch semantics (see applyHiddenLibrariesPatch).
func (a *App) handleAdminHomeLibrariesPut(w http.ResponseWriter, r *http.Request) {
	if a.HiddenLibraries == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "数据库不可用，首页库隐藏未启用"})
		return
	}
	var body struct {
		Hidden map[string]*[]string `json:"hidden"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "Invalid request body"})
		return
	}
	if body.Hidden == nil {
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
		return
	}
	if err := a.applyHiddenLibrariesPatch(adminVisibilityUserID, body.Hidden); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// applyHiddenLibrariesPatch applies the hidden-library save contract: a key
// present with an array (empty included) replaces that server's list, an
// absent or null key leaves the stored data untouched — which is how an
// offline server's config survives a save from the panel.
func (a *App) applyHiddenLibrariesPatch(userID string, hidden map[string]*[]string) error {
	for serverID, ids := range hidden {
		if serverID == "" || ids == nil {
			continue
		}
		if err := a.HiddenLibraries.SetServerHidden(userID, serverID, *ids); err != nil {
			return err
		}
	}
	return nil
}

// hiddenLibrariesJSONFor shapes a user's stored config for the admin users
// list. A nil store reports an empty config rather than failing the listing.
func (a *App) hiddenLibrariesJSONFor(userID string) map[string][]string {
	if a.HiddenLibraries == nil {
		return map[string][]string{}
	}
	return a.HiddenLibraries.UserHiddenJSON(userID)
}
