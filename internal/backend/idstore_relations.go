package backend

import (
	"sort"
	"strings"
)

// Relations describe physical ownership, independently of display winners and
// virtual aliases. A server can hold several copies of the same episode/series.
type resourceRelationKey struct {
	kind, serverID, resourceID string
}

func (s *IDStore) loadResourceRelations() error {
	s.resourceParents = map[resourceRelationKey]map[string]bool{}
	if s.db == nil {
		return nil
	}
	if err := s.db.exec(`CREATE TABLE IF NOT EXISTS id_resource_parents (
		kind TEXT NOT NULL, server_id TEXT NOT NULL, resource_id TEXT NOT NULL,
		parent_id TEXT NOT NULL, PRIMARY KEY(kind, server_id, resource_id, parent_id)
	)`); err != nil {
		return err
	}
	stmt, err := s.db.prepare(`SELECT kind, server_id, resource_id, parent_id FROM id_resource_parents`)
	if err != nil {
		return err
	}
	defer stmt.finalize()
	for {
		row, err := stmt.step()
		if err != nil || !row {
			return err
		}
		key := resourceRelationKey{stmt.columnText(0), stmt.columnText(1), stmt.columnText(2)}
		if s.resourceParents[key] == nil {
			s.resourceParents[key] = map[string]bool{}
		}
		s.resourceParents[key][stmt.columnText(3)] = true
	}
}

func (s *IDStore) RegisterResourceParent(kind, serverID, resourceID, parentID string) {
	if resourceID == "" || parentID == "" || serverID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := resourceRelationKey{kind, serverID, resourceID}
	if s.resourceParents[key][parentID] {
		return
	}
	if s.db != nil {
		if err := s.db.writeParams(`INSERT OR IGNORE INTO id_resource_parents
			(kind, server_id, resource_id, parent_id) VALUES (?, ?, ?, ?)`, kind, serverID, resourceID, parentID); err != nil {
			if s.logger != nil {
				s.logger.Warnf("idstore: resource ownership could not be persisted: %v", err)
			}
			return // Never expose a binding that will silently change on restart.
		}
	}
	if s.resourceParents == nil {
		s.resourceParents = map[resourceRelationKey]map[string]bool{}
	}
	if s.resourceParents[key] == nil {
		s.resourceParents[key] = map[string]bool{}
	}
	s.resourceParents[key][parentID] = true
}

func (s *IDStore) resourceParentMatches(kind, serverID, resourceID, parentID string) (matches, known bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	parents := s.resourceParents[resourceRelationKey{kind, serverID, resourceID}]
	return parents[parentID], len(parents) > 0
}

func (s *IDStore) resourceParentIDs(kind, serverID, resourceID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var ids []string
	for id := range s.resourceParents[resourceRelationKey{kind, serverID, resourceID}] {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// One aggregated PlaybackInfo has one public PlaySessionId but each source's
// upstream answer owns its own session. Never send the base copy's session to
// the server/copy chosen by the client.
func (s *IDStore) sessionForSource(publicSession, virtualItem, originalItem string, source *ResolvedID) (string, bool) {
	if publicSession == "" {
		return "", true
	}
	ids := s.resourceParentIDs("source-session", source.ServerID, publicSession+"|"+source.OriginalID)
	if len(ids) > 0 {
		return ids[0], len(ids) == 1
	}
	if session := s.ResolveVirtualID(publicSession); session != nil {
		parent, ok := s.ResourceItem(virtualItem, session, "session")
		return session.OriginalID, ok && session.ServerID == source.ServerID && parent == originalItem
	}
	return publicSession, true // Legacy clients may echo an upstream session.
}

// PhysicalInstances includes verified historical aliases, preserving the
// requested ID's preferred copy. Original IDs alone are never alias evidence.
func (s *IDStore) PhysicalInstances(virtualID string) []AdditionalInstance {
	var instances []AdditionalInstance
	for _, alias := range s.AliasVirtualIDs(virtualID) {
		if resolved := s.ResolveVirtualID(alias); resolved != nil {
			instances = appendIfMissing(instances, AdditionalInstance{resolved.OriginalID, resolved.ServerID})
			for _, other := range resolved.OtherInstances {
				instances = appendIfMissing(instances, other)
			}
		}
	}
	return instances
}

// ResourceItem resolves a source/session to its exact physical item. Legacy
// unbound resources remain usable only when that server has one possible copy.
func (s *IDStore) ResourceItem(virtualItem string, resource *ResolvedID, kind string) (string, bool) {
	if resource == nil {
		return "", false
	}
	var candidates, matched []string
	known := false
	for _, inst := range s.PhysicalInstances(virtualItem) {
		if inst.ServerID != resource.ServerID {
			continue
		}
		candidates = append(candidates, inst.OriginalID)
		owns, registered := s.resourceParentMatches(kind, inst.ServerID, resource.OriginalID, inst.OriginalID)
		known = known || registered
		if owns || !registered && kind == "source" && (resource.OriginalID == inst.OriginalID || resource.OriginalID == "mediasource_"+inst.OriginalID) {
			matched = append(matched, inst.OriginalID)
		}
	}
	if len(matched) == 1 {
		return matched[0], true
	}
	if len(matched) == 0 && !known && len(candidates) == 1 {
		return candidates[0], true
	}
	return "", false
}

// Register before rewriting IDs: these edges must always contain original IDs.
func registerResponseRelations(item map[string]any, serverID string, store *IDStore) {
	id, _ := item["Id"].(string)
	series, _ := item["SeriesId"].(string)
	if item["Type"] == "Episode" {
		store.RegisterResourceParent("episode-series", serverID, id, series)
	}
	if item["Type"] == "Season" {
		store.RegisterResourceParent("season", serverID, id, series)
	}
	if season, _ := item["SeasonId"].(string); season != "" {
		store.RegisterResourceParent("season", serverID, season, series)
	}
	for _, source := range asItems(map[string]any{"Items": item["MediaSources"]}) {
		sourceID, _ := source["Id"].(string)
		store.RegisterResourceParent("source", serverID, sourceID, id)
	}
}

// A deterministic representative is used only to group verified aliases; it is
// not a new client-facing ID and does not rewrite existing watch records.
func (s *IDStore) aliasGroupID(id string) string {
	group := id
	for _, alias := range s.AliasVirtualIDs(id) {
		if strings.Compare(alias, group) < 0 {
			group = alias
		}
	}
	return group
}
