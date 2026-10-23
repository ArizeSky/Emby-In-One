package backend

import (
	"net/http"
	"net/url"
)

type watchMetadataKey struct{ server, original string }

func (a *App) fetchWatchMetadata(r *http.Request, reqCtx *RequestContext, groups map[string][]string) map[watchMetadataKey]map[string]any {
	// Fetch metadata per server (all groups now point to online servers)
	fetched := map[watchMetadataKey]map[string]any{}
	session := a.parentalSessionFor(r)
	for serverID, ids := range groups {
		if !a.canUseAggregatedServer(reqCtx, serverID) {
			continue
		}
		client := a.Upstream.ClientByID(serverID)
		if client == nil || !client.IsOnline() {
			continue
		}
		for start := 0; start < len(ids); start += parentalConfirmBatchSize {
			end := min(start+parentalConfirmBatchSize, len(ids))
			if session.restricted() && !session.catalog.tryAcquire() {
				session.note(parentalUnconfirmed)
				break
			}
			q := url.Values{"Ids": {joinComma(ids[start:end])}, "UserId": {client.clientUserID()}}
			q.Set("Fields", "BasicSyncInfo,CanDelete,PrimaryImageAspectRatio,Overview,DateCreated,MediaSources,Path,SortName,Studios,Taglines,Genres,CommunityRating,OfficialRating,CustomRating,Type,SeriesId,SeasonId,ParentId,CumulativeRunTimeTicks,Chapters,ProviderIds")
			payload, err := client.RequestJSON(r.Context(), reqCtx, a.Identity, http.MethodGet, "/Items", q, nil)
			if err != nil {
				if session.restricted() {
					session.note(parentalUnconfirmed)
				}
				continue
			}
			requested, seen := map[string]bool{}, map[string]bool{}
			for _, id := range ids[start:end] {
				requested[id] = true
			}
			var rows []map[string]any
			for _, item := range asItems(payload) {
				id, _ := item["Id"].(string)
				if requested[id] {
					rows = append(rows, item)
					seen[id] = true
				}
			}
			if session.restricted() && len(seen) < len(requested) {
				session.note(parentalUnconfirmed)
			}
			for _, item := range a.parentalFilterServerItems(r, client, rows) {
				id, _ := item["Id"].(string)
				fetched[watchMetadataKey{serverID, id}] = item
			}
		}
	}

	return fetched
}
