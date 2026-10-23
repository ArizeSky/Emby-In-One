package backend

import (
	"net/http"
	"net/url"
	"sort"
)

func (a *App) fetchNextUpSeason(r *http.Request, rc *RequestContext, client *UpstreamClient, seriesID, serverID string, q url.Values) []map[string]any {
	path := "/Shows/" + seriesID + "/Episodes"
	s := a.parentalSessionFor(r)
	if s.restricted() {
		items, err := a.fetchShowItems(r.Context(), rc, seriesInstance{Client: client, ServerID: serverID, OriginalID: seriesID}, path, q)
		if err != nil {
			s.note(parentalUnconfirmed)
			return nil
		}
		return items
	}
	payload, err := client.RequestJSON(r.Context(), rc, a.Identity, http.MethodGet, path, q, nil)
	if err != nil {
		return nil
	}
	return asItems(payload)
}

// Determine the playback position before filtering; removing the remembered
// episode first would shift the cursor or incorrectly restart the series.
func nextEpisodeCandidates(items []map[string]any, last *WatchProgress) []map[string]any {
	for i, ep := range items {
		season, _ := numericInt(ep["ParentIndexNumber"])
		index, _ := numericInt(ep["IndexNumber"])
		if season == last.ParentIndexNumber && index == last.IndexNumber {
			if !last.Played && last.PositionTicks > 0 {
				return items[i:]
			}
			return items[i+1:]
		}
	}
	candidates := []map[string]any{}
	for _, ep := range items {
		season, _ := numericInt(ep["ParentIndexNumber"])
		index, _ := numericInt(ep["IndexNumber"])
		if season > last.ParentIndexNumber || (season == last.ParentIndexNumber && index > last.IndexNumber) {
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

func (a *App) firstAllowedNextEpisode(r *http.Request, client *UpstreamClient, items []map[string]any) map[string]any {
	const batchSize = 20
	for start := 0; start < len(items); start += batchSize {
		allowed := a.parentalFilterServerItems(r, client, items[start:min(start+batchSize, len(items))])
		if len(allowed) > 0 {
			return allowed[0]
		}
		if r.Context().Err() != nil {
			break
		}
	}
	return nil
}
