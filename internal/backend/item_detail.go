package backend

import (
	"context"
	"net/http"
	"sync"
	"time"
)

func (a *App) handleMergedItemDetail(w http.ResponseWriter, r *http.Request, instances []seriesInstance) {
	var base map[string]any
	var baseServerID string
	allMediaSources := []map[string]any{}

	cfg := a.ConfigStore.Snapshot()
	globalTimeout := time.Duration(cfg.Timeouts.Global) * time.Millisecond
	if globalTimeout <= 0 {
		globalTimeout = 15 * time.Second
	}
	bgCtx, bgCancel := context.WithTimeout(r.Context(), globalTimeout)
	defer bgCancel()

	type instanceResult struct {
		index        int
		serverID     string
		data         map[string]any
		mediaSources []map[string]any
		allowed      bool
		confirmed    bool
	}
	resultCh := make(chan *instanceResult, len(instances))
	ordered := make([]*instanceResult, len(instances))
	var workers sync.WaitGroup
	defer func() { bgCancel(); workers.Wait() }()

	for index, inst := range instances {
		workers.Add(1)
		go func(index int, si seriesInstance) {
			defer workers.Done()
			// Per-instance parental gate BEFORE this copy contributes anything
			// to the response (plan §8.5): a denied instance's metadata and
			// MediaSources must not leak through an allowed sibling, and an
			// unconfirmable rating must not be released as unrated.
			allowed, confirmed := a.checkInstanceParentalAccess(r.WithContext(bgCtx), si.Client, si.OriginalID)
			if !allowed {
				resultCh <- &instanceResult{serverID: si.ServerID, allowed: false, confirmed: confirmed}
				return
			}
			query := cloneValues(r.URL.Query())
			query.Set("UserId", si.Client.clientUserID())
			payload, err := si.Client.RequestJSON(bgCtx, requestContextFrom(r.Context()), a.Identity, http.MethodGet, "/Users/"+si.Client.clientUserID()+"/Items/"+si.OriginalID, query, nil)
			if err != nil {
				resultCh <- nil
				return
			}
			data, ok := payload.(map[string]any)
			if !ok {
				resultCh <- nil
				return
			}
			registerResponseRelations(data, si.ServerID, a.IDStore)
			var mediaSources []map[string]any
			for _, raw := range asItems(map[string]any{"Items": data["MediaSources"]}) {
				ms := deepCloneMap(raw)
				if originalID, _ := ms["Id"].(string); originalID != "" {
					virtualSource := a.IDStore.GetOrCreateVirtualID(originalID, si.ServerID)
					ms["Id"] = virtualSource
					token := ""
					if rc := requestContextFrom(r.Context()); rc != nil {
						token = rc.ProxyToken
					}
					rewriteMediaSourceSubtitles(ms, si.OriginalID, r.PathValue("itemId"), originalID, virtualSource, token)
				}
				if client := a.Upstream.ClientByID(si.ServerID); client != nil {
					annotateMediaSource(ms, client)
				}
				mediaSources = append(mediaSources, ms)
			}
			resultCh <- &instanceResult{index: index, serverID: si.ServerID, data: data, mediaSources: mediaSources, allowed: true, confirmed: true}
		}(index, inst)
	}

	received := 0
	failed := 0
	denied := 0
	unconfirmed := 0

	for received < len(instances) {
		select {
		case res := <-resultCh:
			received++
			if res == nil {
				failed++
				continue
			}
			if !res.allowed {
				if res.confirmed {
					denied++
				} else {
					unconfirmed++
				}
				continue
			}
			ordered[res.index] = res
		case <-bgCtx.Done():
			goto metadataDone
		}
	}
metadataDone:
	bgCancel()
	// Keep version order stable across varying response times, including two
	// copies on the same server. Track indices always belong to their version.
	for _, res := range ordered {
		if res == nil {
			continue
		}
		if base == nil || isBetterMetadata(base, baseServerID, res.data, res.serverID, cfg) {
			base, baseServerID = deepCloneMap(res.data), res.serverID
		}
		allMediaSources = append(allMediaSources, res.mediaSources...)
	}
	if base == nil {
		// Every instance was either denied by policy or could not be checked.
		// A definite deny is a 403; only unconfirmable/unreachable metadata is
		// a 502 (plan §8.5).
		if denied == len(instances) && received == len(instances) {
			writeJSON(w, http.StatusForbidden, map[string]any{"message": "Content restricted by parental rating"})
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Upstream request failed"})
		return
	}
	// Restrict a non-admin viewer's MediaSources: the file path and upstream
	// direct URLs are intentionally separate from the container/track data
	// the client needs to issue its own PlaybackInfo call.
	mediaSources := make([]any, 0, len(allMediaSources))
	for _, mediaSource := range privacyDetailMediaSources(r, allMediaSources) {
		mediaSources = append(mediaSources, mediaSource)
	}
	// delete-and-restore: prevent rewriteResponseIDs from double-wrapping
	// already-virtualised MediaSource IDs and creating orphan mappings
	delete(base, "MediaSources")
	rewriteResponseIDs(base, baseServerID, a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
	base["Id"] = r.PathValue("itemId")
	base["MediaSources"] = mediaSources
	base["EioIncompleteSources"] = received < len(instances) || failed > 0 || unconfirmed > 0
	// 与其他出口一致的严格契约：本地读取失败必须 502，不得退回共享态。
	if !a.strictSanitizedUserData(w, r, r.PathValue("itemId"), base) {
		return
	}
	// Each contributing instance was already checked with inheritance. Checking
	// the sparse response DTO again would discard that confirmed parent rating.
	writeJSON(w, http.StatusOK, base)
}
