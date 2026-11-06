package backend

import (
	"net/http"
	"strings"
)

// Strict streams never consult the global active-source fallback. Authorization
// still runs on every request against the actual configured serving instance.
func (a *App) proxyStrictPlaybackStream(w http.ResponseWriter, r *http.Request, route streamRoute) {
	req := requestContextFrom(r.Context())
	binding := req.Playback
	item := r.PathValue("itemId")
	if item != binding.VirtualItemID {
		writeJSON(w, 409, map[string]any{"kind": "playback-item-mismatch"})
		return
	}
	if !a.isServerAllowed(req, binding.ServerID) || !a.canUseAggregatedServer(req, binding.ServerID) {
		w.WriteHeader(403)
		return
	}
	client := a.Upstream.ClientByID(binding.ServerID)
	original, ok := translateVirtualIDForServer(item, binding.ServerID, a.IDStore)
	if !ok || client == nil || !client.IsOnline() {
		writeJSON(w, 409, map[string]any{"kind": "playback-source-unavailable"})
		return
	}
	query := strictPlaybackRequestQuery(r)
	if source := queryValue(query, "MediaSourceId"); source != "" {
		translated, ok := a.strictPlaybackSourceForBinding(*binding, source)
		if !ok || binding.SourceID != "" && translated != binding.SourceID {
			writeJSON(w, 409, map[string]any{"kind": "playback-source-mismatch"})
			return
		}
		setQueryValue(query, "MediaSourceId", translated)
	} else if binding.SourceID != "" {
		setQueryValue(query, "MediaSourceId", binding.SourceID)
	}
	if session := queryValue(query, "PlaySessionId"); session != "" {
		if resolved := a.IDStore.ResolveVirtualID(session); resolved != nil {
			if resolved.ServerID != binding.ServerID {
				w.WriteHeader(409)
				return
			}
			session = resolved.OriginalID
		}
		if binding.UpstreamPlaySessionID != "" && session != binding.UpstreamPlaySessionID {
			writeJSON(w, 409, map[string]any{"kind": "playback-session-mismatch"})
			return
		}
		setQueryValue(query, "PlaySessionId", session)
	} else if binding.UpstreamPlaySessionID != "" {
		setQueryValue(query, "PlaySessionId", binding.UpstreamPlaySessionID)
	}
	allowed, confirmed := a.checkInstanceParentalAccess(r, client, original)
	if !allowed {
		if !confirmed {
			w.WriteHeader(502)
		} else {
			w.WriteHeader(403)
		}
		return
	}
	rest := r.PathValue("rest")
	if rest == "" || strings.Contains(rest, "..") {
		w.WriteHeader(400)
		return
	}
	if head, tail, found := strings.Cut(rest, "/"); found && (strings.HasPrefix(tail, "Subtitles/") || strings.HasPrefix(tail, "Attachments/")) {
		pathSource := head
		if resolved := a.IDStore.ResolveVirtualID(head); resolved != nil {
			if resolved.ServerID != binding.ServerID {
				w.WriteHeader(409)
				return
			}
			pathSource = resolved.OriginalID
		}
		if binding.SourceID != "" && pathSource != binding.SourceID {
			writeJSON(w, 409, map[string]any{"kind": "playback-source-mismatch"})
			return
		}
	}
	rest = resolveMediaSourceInPathForServer(rest, a.IDStore, client.ID, original)
	// Strict attempts stay proxied even when native requests use direct mode:
	// browser-direct fetches cannot present the selected upstream identity.
	a.forwardStream(w, r, client, route.pathPrefix+"/"+original+"/"+rest, query, rest, item, original)
}
