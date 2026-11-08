package backend

func (a *App) watchMetadataNeedsRefresh(row *WatchProgress, server, item string) bool {
	if row == nil || row.ItemType == "" || row.ServerID != server || row.OriginalItemID != item {
		return true
	}
	if row.ItemType != "Episode" {
		return false
	}
	if row.SeriesOriginalID == "" || row.SeriesVirtualID == "" {
		return true
	}
	if owns, known := a.IDStore.resourceParentMatches("episode-series", server, item, row.SeriesOriginalID); known {
		return !owns
	}
	pairs := a.IDStore.PhysicalInstances(row.SeriesVirtualID)
	for _, pair := range pairs {
		if pair.ServerID == server && pair.OriginalID == row.SeriesOriginalID {
			return false
		}
	}
	return len(pairs) > 0
}

// watchIdentityGroups caches verified alias representatives for one request.
// Titles are display metadata, never identity evidence.
type watchIdentityGroups struct {
	store *IDStore
	keys  map[string]string
}

func (g *watchIdentityGroups) key(id string) string {
	if key, ok := g.keys[id]; ok {
		return key
	}
	if g.keys == nil {
		g.keys = map[string]string{}
	}
	key := g.store.aliasGroupID(id)
	for _, alias := range g.store.AliasVirtualIDs(id) {
		g.keys[alias] = key
	}
	return key
}

func (g *watchIdentityGroups) seriesKey(progress WatchProgress) string {
	if progress.ItemType == "Episode" && progress.SeriesVirtualID != "" {
		return "series:" + g.key(progress.SeriesVirtualID)
	}
	return "item:" + g.key(progress.VirtualItemID)
}

func laterEpisode(left, right WatchProgress) bool {
	if left.ParentIndexNumber != right.ParentIndexNumber {
		return left.ParentIndexNumber > right.ParentIndexNumber
	}
	if left.IndexNumber != right.IndexNumber {
		return left.IndexNumber > right.IndexNumber
	}
	return left.LastPlayed > right.LastPlayed
}

func (a *App) foldNextUpSeries(rows []WatchProgress) []WatchProgress {
	groups := watchIdentityGroups{store: a.IDStore}
	indices := map[string]int{}
	var folded []WatchProgress
	for _, row := range rows {
		key := groups.seriesKey(row)
		if index, found := indices[key]; found {
			if laterEpisode(row, folded[index]) {
				folded[index] = row
			}
			continue
		}
		indices[key] = len(folded)
		folded = append(folded, row)
	}
	return folded
}

// Validate a remembered physical series against both the virtual group and
// the watched episode's parent. Old records can combine one server's item with
// another server's series ID after a source switch; reads repair the selection
// without rewriting or discarding watch history.
func (a *App) watchSeriesInstances(reqCtx *RequestContext, sp *WatchProgress) []seriesInstance {
	var instances, preferred []seriesInstance
	physical := a.IDStore.PhysicalInstances(sp.SeriesVirtualID)
	if len(physical) == 0 && a.IDStore.ResolveVirtualID(sp.SeriesVirtualID) == nil && sp.SeriesOriginalID != "" {
		physical = []AdditionalInstance{{OriginalID: sp.SeriesOriginalID, ServerID: sp.ServerID}}
	}
	for _, pair := range physical {
		client := a.Upstream.ClientByID(pair.ServerID)
		if client == nil || !client.IsOnline() || !a.canUseAggregatedServer(reqCtx, pair.ServerID) {
			continue
		}
		inst := seriesInstance{OriginalID: pair.OriginalID, ServerID: pair.ServerID, Client: client}
		if pair.ServerID == sp.ServerID {
			owns, known := a.IDStore.resourceParentMatches("episode-series", pair.ServerID, sp.OriginalItemID, pair.OriginalID)
			if owns || !known && pair.OriginalID == sp.SeriesOriginalID {
				preferred = append(preferred, inst)
				continue
			}
		}
		instances = append(instances, inst)
	}
	return append(preferred, instances...)
}
