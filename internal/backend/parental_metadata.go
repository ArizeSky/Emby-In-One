package backend

import (
	"net/http"
	"net/url"
	"strings"
)

// Fill missing targets, then each level of distinct parent IDs in batches.
// Detail fallback and concurrent readers share the same memo and budget.
func (a *App) prefetchParentalTargets(r *http.Request, s *parentalReadSession, targets []parentalTarget) {
	groups := map[*UpstreamClient][]string{}
	for _, t := range targets {
		_, presence := classifyItemRating(t.item)
		if t.client != nil && t.original != "" && presence == ratingMissing && !isParentalNavigation(t.item) {
			groups[t.client] = append(groups[t.client], t.original)
		}
	}
	for client, ids := range groups {
		a.prefetchParentalBatch(r, s, client, ids)
		for depth := 0; depth < maxParentalInheritanceDepth; depth++ {
			parents := []string{}
			for _, id := range ids {
				record, ok := s.cached(parentalMetadataKey{client, id})
				if !ok || record.err != nil {
					continue
				}
				_, presence := classifyItemRating(record.item)
				if presence == ratingMissing && needsRatingInheritance(record.item) {
					if parent := nextParentID(record.item, map[string]bool{id: true}); parent != "" {
						parents = append(parents, parent)
					}
				}
			}
			if len(parents) == 0 {
				break
			}
			a.prefetchParentalBatch(r, s, client, parents)
			ids = parents
		}
	}
}

func (a *App) prefetchParentalBatch(r *http.Request, s *parentalReadSession, client *UpstreamClient, ids []string) {
	unique := make([]string, 0, len(ids))
	seen := map[string]bool{}
	for _, id := range ids {
		if _, ok := s.cached(parentalMetadataKey{client, id}); !ok && !seen[id] {
			unique = append(unique, id)
			seen[id] = true
		}
	}
	for start := 0; start < len(unique); start += parentalConfirmBatchSize {
		end := start + parentalConfirmBatchSize
		if end > len(unique) {
			end = len(unique)
		}
		batch := unique[start:end]
		if !s.takeBudget() {
			return
		}
		select {
		case s.slots <- struct{}{}:
		case <-r.Context().Done():
			return
		}
		payload, err := client.RequestJSON(r.Context(), requestContextFrom(r.Context()), a.Identity, http.MethodGet, "/Items", url.Values{
			"Ids": {strings.Join(batch, ",")}, "UserId": {client.clientUserID()},
			"Fields": {"OfficialRating,CustomRating,Type,SeriesId,SeasonId,ParentId"},
		}, nil)
		<-s.slots
		if err != nil {
			continue // A bounded detail fallback may still confirm the item.
		}
		requested := map[string]bool{}
		for _, id := range batch {
			requested[id] = true
		}
		for _, item := range asItems(payload) {
			id, _ := item["Id"].(string)
			if !requested[id] {
				continue
			}
			_, presence := classifyItemRating(item)
			// Movies with absent ratings require full detail confirmation.
			// A season/episode with its parent identity can use inheritance.
			complete := presence != ratingMissing || (needsRatingInheritance(item) && nextParentID(item, map[string]bool{id: true}) != "")
			s.store(parentalMetadataKey{client, id}, parentalMetadataRecord{item: item, complete: complete})
		}
	}
}
