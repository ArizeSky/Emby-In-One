package backend

import (
	"net/http"
	"net/url"
	"strconv"
)

func (a *App) nextUpProgress(userID, seriesID string) (*WatchProgress, error) {
	var best *WatchProgress
	for _, alias := range a.IDStore.AliasVirtualIDs(seriesID) {
		row, err := a.WatchStore.GetNextUpForSeries(userID, alias)
		if err != nil {
			return nil, err
		}
		if row != nil && (best == nil || laterEpisode(*row, *best)) {
			best = row
		}
	}
	return best, nil
}

// Fetch the verified copies together, keeping each DTO paired with its actual
// server. A stale remembered series ID or a slow copy cannot hide a healthy one.
func (a *App) fetchNextEpisodeAcrossCopies(r *http.Request, reqCtx *RequestContext, progress *WatchProgress) (map[string]any, string, bool) {
	// Reconcile watched state across item aliases before deciding whether the
	// remembered coordinate is resumable or its successor is next.
	current := *progress
	if reqCtx != nil && reqCtx.ProxyUser != nil {
		state, err := a.localUserState(reqCtx.ProxyUser.UserID, privacyItem{virtualID: current.VirtualItemID, aliases: a.privacyAliases(current.VirtualItemID)})
		if err != nil {
			return nil, "", true
		}
		if state != nil {
			current.Played, current.PositionTicks = state.Played, state.PositionTicks
		}
	}
	progress = &current
	instances := a.watchSeriesInstances(reqCtx, progress)
	if len(instances) == 0 {
		return nil, "", true
	}
	q := url.Values{
		"Fields": {"BasicSyncInfo,PrimaryImageAspectRatio,Overview,MediaSources,SortName,OfficialRating,CustomRating,Type,SeriesId,SeasonId,ParentId,ProviderIds"},
		"Season": {strconv.Itoa(progress.ParentIndexNumber)}, "SortBy": {"SortName"}, "SortOrder": {"Ascending"},
	}
	pages, state := a.collectShowPages(r, instances, "Episodes", q)
	item, server := a.chooseNextEpisode(r, pages, progress)
	if item == nil && progress.Played && !state.incomplete {
		q.Set("Season", strconv.Itoa(progress.ParentIndexNumber+1))
		pages, state = a.collectShowPages(r, instances, "Episodes", q)
		item, server = a.chooseNextEpisode(r, pages, progress)
	}
	return item, server, state.incomplete
}

func (a *App) chooseNextEpisode(r *http.Request, pages []showInstancePage, progress *WatchProgress) (map[string]any, string) {
	var best map[string]any
	var server string
	identities := map[string]string{}
	cfg := a.ConfigStore.Snapshot()
	for _, page := range pages {
		inst := page.instance
		a.registerShowIdentities(page.rows, inst.ServerID, true, identities)
		// Confirm only upcoming coordinates, in bounded batches. Rechecking an
		// entire watched season can exhaust the metadata budget before next-up.
		candidates := nextEpisodeCandidates(page.rows, progress)
		for start := 0; start < len(candidates); start += 20 {
			rows := a.parentalFilterServerItems(r, inst.Client, candidates[start:min(start+20, len(candidates))])
			if len(rows) == 0 {
				if r.Context().Err() != nil {
					break
				}
				continue
			}
			candidate := rows[0]
			season, _ := numericInt(candidate["ParentIndexNumber"])
			episode, _ := numericInt(candidate["IndexNumber"])
			bestSeason, _ := numericInt(best["ParentIndexNumber"])
			bestEpisode, _ := numericInt(best["IndexNumber"])
			if best == nil || season < bestSeason || season == bestSeason && episode < bestEpisode ||
				season == bestSeason && episode == bestEpisode && isBetterMetadata(best, server, candidate, inst.ServerID, cfg) {
				best, server = deepCloneMap(candidate), inst.ServerID
			}
			break
		}
	}
	return best, server
}
