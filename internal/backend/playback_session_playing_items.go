package backend

import (
	"net/http"
	"net/url"
	"strings"
)

// Strict PlayingItems start/cleanup (design §4.4, native strict API surface).
//
// Emby clients announce a per-item "now playing" state through
// POST /Users/{userId}/PlayingItems/{itemId} and release it through DELETE on the
// same path. The strict form of both requests is answered here: the strict ingress
// (withStrictPlayback) has already proved the capability, the local session owner
// and the leased binding, so this handler only has to attribute the request to the
// binding's own server and item.
//
// Rules kept from the legacy handlers, plus the strict additions:
//   - exactly one upstream is addressed (the bound server) — no candidate fan-out,
//     no aggregation-filtered re-resolution, no fallback to another replica;
//   - the {userId} path segment is never an identity source: the upstream path uses
//     the bound client's own account, like the legacy handler;
//   - live account grants are re-checked, and a start additionally passes the same
//     parental gate the strict stream path uses. A cleanup stays possible after a
//     narrowing (and never consults the aggregation switch), because a stop must
//     not be blocked by content policy;
//   - a forward that did not succeed is never answered with the idempotent 204: the
//     client must be able to retry the cleanup instead of seeing a manufactured ack.

const (
	strictPlaybackKindStopFailed          = "playback-stop-failed"
	strictPlaybackKindEventUnconfirmed    = "playback-event-unconfirmed"
	strictPlaybackKindIdentityUnavailable = "playback-identity-unavailable"
)

// strictPlaybackRequestQuery drops every local protocol or credential parameter
// from a strict request's query. The upstream must never receive the local
// capability, a local attempt ID or a local metadata token, whatever the client
// appended to the URL.
func strictPlaybackRequestQuery(r *http.Request) url.Values {
	query := cloneValues(r.URL.Query())
	stripProxyCredentialQuery(query)
	for key := range query {
		if strings.HasPrefix(strings.ToLower(key), "eio") {
			query.Del(key)
		}
	}
	return query
}

// handleStrictUserPlayingItem answers the strict form of POST/DELETE
// /Users/{userId}/PlayingItems/{itemId}. start distinguishes POST (announce "now
// playing") from DELETE (release the slot).
func (a *App) handleStrictUserPlayingItem(w http.ResponseWriter, r *http.Request, binding playbackIdentityBinding, start bool) {
	if r.PathValue("userId") != a.clientFacingUserIDFor(r) {
		writeStrictPlaybackError(w, &strictPlaybackError{Status: 403, Kind: "playback-user-mismatch", Message: "playing-item route belongs to another local user"})
		return
	}
	if a.IDStore == nil || binding.ServerID == "" || binding.VirtualItemID == "" {
		writeStrictPlaybackError(w, &strictPlaybackError{
			Status: http.StatusConflict, Kind: strictPlaybackKindIdentityUnavailable,
			Message: "playback attempt is unavailable; renegotiate", Retryable: true,
		})
		return
	}
	if item := r.PathValue("itemId"); item != binding.VirtualItemID {
		writeStrictPlaybackError(w, strictPlaybackEventFailure(strictPlaybackKindItemMismatch, "the playback item does not belong to this attempt", false))
		return
	}
	originalItem, ok := translateVirtualIDForServer(binding.VirtualItemID, binding.ServerID, a.IDStore)
	if !ok {
		writeStrictPlaybackError(w, strictPlaybackEventFailure(strictPlaybackKindItemMismatch, "the playback item has no source on the bound server", false))
		return
	}
	// A playing-item request of the attempt may name its own media source; another
	// candidate or an unknown value describes a different playback and is refused.
	query := strictPlaybackRequestQuery(r)
	if mediaSourceID := queryValue(query, "MediaSourceId"); mediaSourceID != "" {
		originalSource, ok := a.strictPlaybackSourceForBinding(binding, mediaSourceID)
		if !ok {
			writeStrictPlaybackError(w, strictPlaybackEventFailure(strictPlaybackKindSourceMismatch, "the media source does not belong to this playback attempt", false))
			return
		}
		setQueryValue(query, "MediaSourceId", originalSource)
	}
	if session := queryValue(query, "PlaySessionId"); session != "" {
		upstreamSession, failure := a.strictPlaybackSessionForBinding(binding, session)
		if failure != nil {
			writeStrictPlaybackError(w, failure)
			return
		}
		setQueryValue(query, "PlaySessionId", upstreamSession)
	}
	// Live grants stay authoritative for an in-flight attempt. The cleanup keeps
	// the legacy pure-grant rule of session_cleanup.go: aggregation is not consulted,
	// so a server that was switched off can still release its slot.
	if !a.isServerAllowed(requestContextFrom(r.Context()), binding.ServerID) {
		writeJSON(w, http.StatusForbidden, map[string]any{"message": "Access denied"})
		return
	}
	if a.rejectStrictSupersededWrite(w, r, binding, !start) {
		return
	}
	if !start {
		release, ok := a.beginStrictPlaybackCleanup(w, r, binding)
		if !ok {
			return
		}
		defer release()
		defer a.finishStrictPlaybackAttemptLocally(r, binding)
	} else if _, err := a.PlaybackIdentities.resolve(binding.AttemptID, binding.Owner); err != nil {
		writeStrictPlaybackError(w, strictPlaybackErrorFor(err))
		return
	}
	client := a.Upstream.ClientByID(binding.ServerID)
	if client == nil || !client.IsOnline() {
		writeStrictPlaybackError(w, &strictPlaybackError{
			Status: http.StatusConflict, Kind: strictPlaybackKindSourceUnavailable,
			Message: "the playback source is not available; renegotiate", Retryable: true,
		})
		return
	}
	if start {
		// Same gate as proxyStream: only a configured parental policy is enforced, and
		// an unconfirmable rating fails closed.
		if maxRating, blockUnrated := a.parentalPolicyFor(r); maxRating > 0 || blockUnrated {
			allowed, confirmed := a.checkInstanceParentalAccess(r, client, originalItem)
			if !allowed {
				if !confirmed {
					writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Cannot verify content rating"})
				} else {
					writeJSON(w, http.StatusForbidden, map[string]any{"message": "Content restricted by parental rating"})
				}
				return
			}
		}
	}
	method, kind := http.MethodPost, strictPlaybackKindEventUnconfirmed
	if !start {
		method, kind = http.MethodDelete, strictPlaybackKindStopFailed
	}
	path := "/Users/" + client.clientUserID() + "/PlayingItems/" + originalItem
	if err := a.forwardNoContent(r, client, method, path, query, nil); err != nil {
		if status, ok := preparationErrorStatus(err); ok {
			writeJSON(w, status, preparationErrorBody(err))
			return
		}
		if a.Logger != nil {
			a.Logger.Warnf("strict PlayingItems %s upstream error (server %s): %v", method, binding.ServerID, redactURLInError(err))
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"kind":      kind,
			"message":   "the upstream did not confirm the playing-item request; retry",
			"retryable": true,
		})
		return
	}
	if !start {
		a.confirmStrictPlaybackCleanup(r, binding)
		a.retireStrictPlaybackBinding(binding)
	}
	w.WriteHeader(http.StatusNoContent)
}
