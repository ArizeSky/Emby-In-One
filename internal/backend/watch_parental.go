package backend

import "sort"

// Compare coordinates rather than relying on upstream SortName order.
func nextEpisodeCandidates(items []map[string]any, last *WatchProgress) []map[string]any {
	candidates := []map[string]any{}
	for _, ep := range items {
		season, _ := numericInt(ep["ParentIndexNumber"])
		index, _ := numericInt(ep["IndexNumber"])
		if season > last.ParentIndexNumber || season == last.ParentIndexNumber &&
			(index > last.IndexNumber || index == last.IndexNumber && !last.Played && last.PositionTicks > 0) {
			candidates = append(candidates, ep)
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		si, _ := numericInt(candidates[i]["ParentIndexNumber"])
		sj, _ := numericInt(candidates[j]["ParentIndexNumber"])
		if si != sj {
			return si < sj
		}
		ei, _ := numericInt(candidates[i]["IndexNumber"])
		ej, _ := numericInt(candidates[j]["IndexNumber"])
		return ei < ej
	})
	return candidates
}
