package backend

import (
	"context"
	"net/http"
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
		serverID     string
		data         map[string]any
		mediaSources []map[string]any
		allowed      bool
		confirmed    bool
	}
	resultCh := make(chan *instanceResult, len(instances))

	for _, inst := range instances {
		go func(si seriesInstance) {
			// Per-instance parental gate BEFORE this copy contributes anything
			// to the response (plan §8.5): a denied instance's metadata and
			// MediaSources must not leak through an allowed sibling, and an
			// unconfirmable rating must not be released as unrated.
			allowed, confirmed := a.checkInstanceParentalAccess(r, si.Client, si.OriginalID)
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
			var mediaSources []map[string]any
			for _, raw := range asItems(map[string]any{"Items": data["MediaSources"]}) {
				ms := deepCloneMap(raw)
				if originalID, _ := ms["Id"].(string); originalID != "" {
					ms["Id"] = a.IDStore.GetOrCreateVirtualID(originalID, si.ServerID)
				}
				if client := a.Upstream.ClientByID(si.ServerID); client != nil {
					annotateMediaSource(ms, client)
				}
				mediaSources = append(mediaSources, ms)
			}
			resultCh <- &instanceResult{serverID: si.ServerID, data: data, mediaSources: mediaSources, allowed: true, confirmed: true}
		}(inst)
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
			// Base selection follows the four-level metadata rule
			// (priorityMetadata → Chinese Overview → longer Overview →
			// configured order), not network completion order, so the same
			// group shows the same metadata in list and detail views
			// (plan §6.4/§8.5).
			if base == nil || isBetterMetadata(base, baseServerID, res.data, res.serverID, cfg) {
				if base == nil {
					base = deepCloneMap(res.data)
				} else {
					base = deepCloneMap(res.data)
				}
				baseServerID = res.serverID
			}
			allMediaSources = append(allMediaSources, res.mediaSources...)
		case <-bgCtx.Done():
			goto metadataDone
		}
	}
metadataDone:
	bgCancel()
	if base == nil {
		// Every instance was either denied by policy or could not be checked.
		// A definite deny is a 403; only unconfirmable/unreachable metadata is
		// a 502 (plan §8.5).
		if denied > 0 {
			writeJSON(w, http.StatusForbidden, map[string]any{"message": "Content restricted by parental rating"})
			return
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Upstream request failed"})
		return
	}
	mediaSources := make([]any, 0, len(allMediaSources))
	for _, mediaSource := range allMediaSources {
		mediaSources = append(mediaSources, mediaSource)
	}
	// delete-and-restore: prevent rewriteResponseIDs from double-wrapping
	// already-virtualised MediaSource IDs and creating orphan mappings
	delete(base, "MediaSources")
	rewriteResponseIDs(base, baseServerID, a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
	base["Id"] = r.PathValue("itemId")
	base["MediaSources"] = mediaSources
	base["EioIncompleteSources"] = received < len(instances) || failed > 0 || unconfirmed > 0
	a.overlayLocalUserData(r, r.PathValue("itemId"), base)
	if maxR, blockUnrated := a.parentalPolicyFor(r); (maxR > 0 || blockUnrated) && !itemAllowedByPolicy(base, maxR, blockUnrated) {
		writeJSON(w, http.StatusForbidden, map[string]any{"message": "Content restricted by parental rating"})
		return
	}
	writeJSON(w, http.StatusOK, base)
}
