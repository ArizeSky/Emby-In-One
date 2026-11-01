package backend

import (
	"strconv"
	"strings"
)

// dedupFacts carries identity evidence collected for one media item. Provider
// identifiers are namespaced by provider, and their lookup keys also include the
// media type so a Movie id cannot collide with a Series/Episode id.
type dedupFacts struct {
	typ               string
	providers         map[string]string
	name              string // lower-cased and trimmed
	year              string // empty when the upstream did not send one
	episodeSignatures []episodeDedupSignature
}

type episodeDedupSignature struct {
	season       int
	episode      int
	seriesGroups map[string]struct{}
}

func dedupFactsOf(item map[string]any) dedupFacts {
	f := dedupFacts{}
	f.typ, _ = item["Type"].(string)
	f.name, _ = item["Name"].(string)
	f.name = strings.ToLower(strings.TrimSpace(f.name))
	if y, ok := numericInt(item["ProductionYear"]); ok && y > 0 {
		f.year = strconv.Itoa(y)
	}
	if providerIDs, ok := item["ProviderIds"].(map[string]any); ok {
		f.providers = make(map[string]string, len(providerIDs))
		for provider, rawID := range providerIDs {
			id, ok := rawID.(string)
			provider = strings.ToLower(strings.TrimSpace(provider))
			id = strings.TrimSpace(id)
			if ok && provider != "" && id != "" {
				f.providers[provider] = id
			}
		}
	}
	return f
}

func providerBucketKey(facts dedupFacts, provider, providerID string) string {
	return strings.ToLower(strings.TrimSpace(facts.typ)) + "\x00" + provider + "\x00" + providerID
}

// providerIdentityMatch returns whether the records share a strong provider ID
// and whether their strong evidence conflicts. If both sides have provider IDs
// but no same-provider match, they are not allowed to fall through to title/year
// heuristics (including same-number IDs from different provider namespaces).
func providerIdentityMatch(a, b dedupFacts) (matched, conflict bool) {
	if a.typ != b.typ {
		return false, true
	}
	if len(a.providers) == 0 || len(b.providers) == 0 {
		return false, false
	}
	matched = false
	for provider, aID := range a.providers {
		if bID, ok := b.providers[provider]; ok {
			if aID != bID {
				return false, true
			}
			matched = true
		}
	}
	if matched {
		return true, false
	}
	return false, true
}

func movieSeriesMatch(a, b dedupFacts) bool {
	if a.typ != b.typ || (a.typ != "Movie" && a.typ != "Series") {
		return false
	}
	if matched, conflict := providerIdentityMatch(a, b); matched {
		return true
	} else if conflict {
		return false
	}
	return a.name != "" && a.name == b.name && a.year != "" && a.year == b.year
}

func episodeSeriesSignature(item map[string]any, serverID string, store *IDStore) (episodeDedupSignature, bool) {
	seriesID, _ := item["SeriesId"].(string)
	seriesID = strings.TrimSpace(seriesID)
	season, hasSeason := numericInt(item["ParentIndexNumber"])
	episode, hasEpisode := numericInt(item["IndexNumber"])
	if store == nil || serverID == "" || seriesID == "" || !hasSeason || !hasEpisode || season < 0 || episode < 0 {
		return episodeDedupSignature{}, false
	}
	seriesVirtualID := store.GetOrCreateVirtualID(seriesID, serverID)
	aliases := store.AliasVirtualIDs(seriesVirtualID)
	groups := make(map[string]struct{}, len(aliases)+1)
	groups[seriesVirtualID] = struct{}{}
	for _, alias := range aliases {
		groups[alias] = struct{}{}
	}
	return episodeDedupSignature{season: season, episode: episode, seriesGroups: groups}, true
}

func episodeSignaturesMatch(a, b []episodeDedupSignature) bool {
	for _, left := range a {
		for _, right := range b {
			if left.season != right.season || left.episode != right.episode {
				continue
			}
			for group := range left.seriesGroups {
				if _, ok := right.seriesGroups[group]; ok {
					return true
				}
			}
		}
	}
	return false
}

func episodeMatch(a, b dedupFacts) bool {
	if a.typ != "Episode" || b.typ != "Episode" {
		return false
	}
	if matched, conflict := providerIdentityMatch(a, b); matched {
		return true
	} else if conflict {
		return false
	}
	return episodeSignaturesMatch(a.episodeSignatures, b.episodeSignatures)
}

type dedupSeenEntry struct {
	virtualID   string
	mergedIndex int
	serverID    string
	originalID  string
	facts       dedupFacts
}

func episodeSignatureBucketKey(group string, signature episodeDedupSignature) string {
	return group + "\x00" + strconv.Itoa(signature.season) + "\x00" + strconv.Itoa(signature.episode)
}

func appendSeenEntry(entries []*dedupSeenEntry, candidate *dedupSeenEntry) []*dedupSeenEntry {
	for _, entry := range entries {
		if entry == candidate {
			return entries
		}
	}
	return append(entries, candidate)
}

func mergeDedupFacts(dst *dedupFacts, src dedupFacts) {
	if dst.providers == nil && len(src.providers) != 0 {
		dst.providers = map[string]string{}
	}
	for provider, providerID := range src.providers {
		if _, exists := dst.providers[provider]; !exists {
			dst.providers[provider] = providerID
		}
	}
	if dst.name == "" {
		dst.name = src.name
	}
	if dst.year == "" {
		dst.year = src.year
	}
	dst.episodeSignatures = append(dst.episodeSignatures, src.episodeSignatures...)
}

// containsChinese checks if a string contains CJK Unified Ideographs (U+4E00–U+9FA5).
func containsChinese(s string) bool {
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FA5 {
			return true
		}
	}
	return false
}

// isBetterMetadata returns true if the candidate item from candidateServerID has
// better metadata than the existing item from existingServerID, using a 5-level
// priority: priorityMetadata flag → Primary image present → Chinese in Overview
// → longer Overview → earlier upstream order.
func isBetterMetadata(existing map[string]any, existingServerID string, candidate map[string]any, candidateServerID string, cfg Config) bool {
	// 1. priorityMetadata flag
	existingPriority := false
	candidatePriority := false
	existingOrder := len(cfg.Upstream)
	candidateOrder := len(cfg.Upstream)
	for idx, u := range cfg.Upstream {
		if u.ID == existingServerID {
			existingPriority = u.PriorityMetadata
			existingOrder = idx
		}
		if u.ID == candidateServerID {
			candidatePriority = u.PriorityMetadata
			candidateOrder = idx
		}
	}
	if candidatePriority && !existingPriority {
		return true
	}
	if existingPriority && !candidatePriority {
		return false
	}

	// 2. Chinese in Overview. Four-level rule (plan §6.4): priorityMetadata →
	// Chinese Overview → longer Overview → server order. A poster never reorders
	// the metadata choice; a missing Primary image is filled separately below.
	existingOverview, _ := existing["Overview"].(string)
	candidateOverview, _ := candidate["Overview"].(string)
	hasChinese1 := containsChinese(existingOverview)
	hasChinese2 := containsChinese(candidateOverview)
	if hasChinese2 && !hasChinese1 {
		return true
	}
	if hasChinese1 && !hasChinese2 {
		return false
	}

	// 3. Longer Overview
	if len(candidateOverview) > len(existingOverview) {
		return true
	}
	if len(existingOverview) > len(candidateOverview) {
		return false
	}

	// 4. Lower server index (order in cfg.Upstream)
	return candidateOrder < existingOrder
}

// hasPrimaryImage reports whether the item carries a Primary image tag, the
// marker Emby sets for a poster. Both the string-hash form and the boolean form
// appear in the wild, so accept either.
func hasPrimaryImage(item map[string]any) bool {
	tags, ok := item["ImageTags"].(map[string]any)
	if !ok {
		return false
	}
	switch v := tags["Primary"].(type) {
	case string:
		return v != ""
	case bool:
		return v
	}
	return false
}

func (a *App) mergedItemsPayload(results []upstreamItemsResult, clientUserID string) map[string]any {
	merged := a.mergeRoundRobinItems(results, clientUserID)
	return map[string]any{
		"Items":            toAnySlice(merged),
		"TotalRecordCount": len(merged),
		"StartIndex":       0,
	}
}

// mergeRoundRobinItems interleaves results while deduplicating only on
// type-scoped strong provider IDs, strict name+year fallback, or a trusted
// episode-series identity plus season/episode coordinates.
func (a *App) mergeRoundRobinItems(results []upstreamItemsResult, clientUserID string) []map[string]any {
	cfg := a.ConfigStore.Snapshot()
	merged := make([]map[string]any, 0)
	seenProviders := map[string][]*dedupSeenEntry{}
	seenName := map[string][]*dedupSeenEntry{}
	seenEpisode := map[string][]*dedupSeenEntry{}
	var entries []*dedupSeenEntry

	addEntryIndexes := func(entry *dedupSeenEntry) {
		for provider, providerID := range entry.facts.providers {
			key := providerBucketKey(entry.facts, provider, providerID)
			seenProviders[key] = appendSeenEntry(seenProviders[key], entry)
		}
		if (entry.facts.typ == "Movie" || entry.facts.typ == "Series") && entry.facts.name != "" {
			seenName[entry.facts.name] = appendSeenEntry(seenName[entry.facts.name], entry)
		}
		if entry.facts.typ == "Episode" {
			for _, signature := range entry.facts.episodeSignatures {
				for group := range signature.seriesGroups {
					key := episodeSignatureBucketKey(group, signature)
					seenEpisode[key] = appendSeenEntry(seenEpisode[key], entry)
				}
			}
		}
	}

	maxLen := 0
	for _, result := range results {
		if len(result.Items) > maxLen {
			maxLen = len(result.Items)
		}
	}

	for i := 0; i < maxLen; i++ {
		for _, result := range results {
			if i >= len(result.Items) {
				continue
			}
			item := result.Items[i]
			originalID, _ := item["Id"].(string)
			if originalID == "" {
				rewriteResponseIDs(item, result.ServerID, a.IDStore, cfg.Server.ID, clientUserID)
				merged = append(merged, item)
				continue
			}

			facts := dedupFactsOf(item)
			if facts.typ == "Episode" {
				if signature, ok := episodeSeriesSignature(item, result.ServerID, a.IDStore); ok {
					facts.episodeSignatures = append(facts.episodeSignatures, signature)
				}
			}

			var candidates []*dedupSeenEntry
			candidateSet := map[*dedupSeenEntry]struct{}{}
			addCandidates := func(bucket []*dedupSeenEntry) {
				for _, candidate := range bucket {
					if _, exists := candidateSet[candidate]; exists {
						continue
					}
					candidateSet[candidate] = struct{}{}
					candidates = append(candidates, candidate)
				}
			}
			switch facts.typ {
			case "Movie", "Series":
				for provider, providerID := range facts.providers {
					addCandidates(seenProviders[providerBucketKey(facts, provider, providerID)])
				}
				addCandidates(seenName[facts.name])
			case "Episode":
				for provider, providerID := range facts.providers {
					addCandidates(seenProviders[providerBucketKey(facts, provider, providerID)])
				}
				for _, signature := range facts.episodeSignatures {
					for group := range signature.seriesGroups {
						addCandidates(seenEpisode[episodeSignatureBucketKey(group, signature)])
					}
				}
			}

			var entry *dedupSeenEntry
			for _, candidate := range candidates {
				matches := false
				switch facts.typ {
				case "Movie", "Series":
					matches = movieSeriesMatch(facts, candidate.facts)
				case "Episode":
					matches = episodeMatch(facts, candidate.facts)
				}
				if matches {
					entry = candidate
					break
				}
			}

			if entry != nil {
				// Duplicate: associate as an additional instance of the same item.
				a.IDStore.AssociateAdditionalInstance(entry.virtualID, originalID, result.ServerID)
				base := merged[entry.mergedIndex]
				baseHasImage := hasPrimaryImage(base)
				candidateHasImage := hasPrimaryImage(item)
				oldServerID := entry.serverID
				oldOriginalID := entry.originalID
				if isBetterMetadata(base, entry.serverID, item, result.ServerID, cfg) {
					delete(item, "Id")
					rewriteResponseIDs(item, result.ServerID, a.IDStore, cfg.Server.ID, clientUserID)
					item["Id"] = entry.virtualID
					merged[entry.mergedIndex] = item
					entry.serverID = result.ServerID
					entry.originalID = originalID
					if !candidateHasImage && baseHasImage {
						if tag, ok := primaryImageTag(base); ok {
							copyPrimaryImageFields(item, base)
							a.IDStore.SetImageSource(entry.virtualID, "Primary", tag, oldServerID, oldOriginalID)
						}
					}
				} else if !baseHasImage && candidateHasImage {
					if tag, ok := primaryImageTag(item); ok {
						copyPrimaryImageFields(base, item)
						a.IDStore.SetImageSource(entry.virtualID, "Primary", tag, result.ServerID, originalID)
					}
				}
				// Retain every newly verified provider/signature fact so a later
				// incomplete copy cannot bridge two conflicting identity groups.
				mergeDedupFacts(&entry.facts, facts)
				addEntryIndexes(entry)
				continue
			}

			// First occurrence: keep the virtual ID, rewrite everything else.
			virtualID := a.IDStore.GetOrCreateVirtualID(originalID, result.ServerID)
			entry = &dedupSeenEntry{virtualID: virtualID, mergedIndex: len(merged), serverID: result.ServerID, originalID: originalID, facts: facts}
			entries = append(entries, entry)
			addEntryIndexes(entry)
			delete(item, "Id")
			rewriteResponseIDs(item, result.ServerID, a.IDStore, cfg.Server.ID, clientUserID)
			item["Id"] = virtualID
			merged = append(merged, item)
		}
	}

	// Record each group's display-winning instance so image routing only uses the
	// upstream that actually owns the advertised image tags.
	for _, entry := range entries {
		a.IDStore.SetDisplaySource(entry.virtualID, entry.serverID, entry.originalID)
		if entry.mergedIndex >= 0 && entry.mergedIndex < len(merged) {
			recordImageSourceTags(a.IDStore, entry.virtualID, entry.serverID, entry.originalID, merged[entry.mergedIndex])
		}
	}

	return merged
}

// primaryImageTag returns the Primary poster hash when the item carries a usable
// string tag. A boolean-only marker has no hash and cannot form an image URL.
func primaryImageTag(item map[string]any) (string, bool) {
	tags, ok := item["ImageTags"].(map[string]any)
	if !ok {
		return "", false
	}
	if tag, ok := tags["Primary"].(string); ok && tag != "" {
		return tag, true
	}
	return "", false
}

// copyPrimaryImageFields copies only the Primary poster fields from src into dst,
// keeping dst's own other ImageTags entries and every other field untouched
// (plan §6.4.1: never overwrite the display record's identity/metadata).
func copyPrimaryImageFields(dst, src map[string]any) {
	srcTags, ok := src["ImageTags"].(map[string]any)
	if !ok {
		return
	}
	primary, ok := srcTags["Primary"].(string)
	if !ok || primary == "" {
		return
	}
	dstTags, ok := dst["ImageTags"].(map[string]any)
	if !ok {
		dstTags = map[string]any{}
		dst["ImageTags"] = dstTags
	}
	dstTags["Primary"] = primary
	if ar, ok := src["PrimaryImageAspectRatio"]; ok {
		dst["PrimaryImageAspectRatio"] = ar
	}
}

// recordImageSourceTags records per-tag ownership for every non-Primary image
// the displayed DTO advertises (plan §6.4.2). The Primary tag is managed
// explicitly by the poster carryover, which knows the exact owning instance, so
// it is skipped here. Backdrop tags arrive as an array and are recorded
// individually so an indexed request with a tag still routes to the owner.
func recordImageSourceTags(store *IDStore, virtualID, serverID, originalID string, item map[string]any) {
	if tags, ok := item["ImageTags"].(map[string]any); ok {
		for imageType, raw := range tags {
			tag, _ := raw.(string)
			if imageType == "" || imageType == "Primary" || tag == "" {
				continue
			}
			store.SetImageSource(virtualID, imageType, tag, serverID, originalID)
		}
	}
	if backdrops, ok := item["BackdropImageTags"].([]any); ok {
		for _, raw := range backdrops {
			tag, _ := raw.(string)
			if tag == "" {
				continue
			}
			store.SetImageSource(virtualID, "Backdrop", tag, serverID, originalID)
		}
	}
}
