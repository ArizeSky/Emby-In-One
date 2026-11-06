package backend

import (
	"net/http"
)

// Strict attempt session-event integration (design §4.4 / §4.7).
//
// The strict ingress (playback_ingress.go) authenticates /eio/playback/v1/...
// with the attempt capability, revalidates the local session, verifies the
// binding owner and leases the binding, then publishes it in
// RequestContext.Playback. That binding is the only thing this file trusts about
// the client: the ItemId / MediaSourceId / PlaySessionId / EioAttemptId values in
// the body are association *hints* that must agree with it. Nothing in the body
// can select another local user, upstream server, item candidate or attempt, and
// no body value is ever turned into a global ID lookup — the legacy
// translateSessionBodyIDs fallback (global alias resolution plus "last server
// that served this item") stays on the legacy path only.
//
// This file therefore:
//   - validates a strict event against the binding and answers the frozen
//     playback-* error protocol (409) instead of degrading to the legacy route;
//   - rewrites the body for exactly the binding's server: ItemId and MediaSourceId
//     become that server's upstream IDs, PlaySessionId becomes the candidate's own
//     upstream session, and every local protocol field (EioAttemptId,
//     EioPlaybackToken, EioPlaySessionId) is stripped before the body leaves the
//     process;
//   - keys the local watch-ordering tracker and the concurrency-slot identity on
//     the attempt ID, never on the upstream PlaySessionId, because one upstream
//     session value can be reused by several sources and by a later attempt;
//   - reuses the existing authorization, WatchStore, limiter and upstream-activity
//     semantics unchanged;
//   - closes admission before sending a validated stop, and records retirement
//     and its independent ACK only after the upstream accepts that cleanup.
//
// The legacy handlers keep their behaviour byte for byte; this module is reached
// only when the ingress published a binding.

// Frozen strict error kinds used by the session-event path. They are additions to
// the kinds declared in playback_ingress.go and follow the same shape
// ({kind, message, retryable}).
const (
	strictPlaybackKindBodyInvalid       = "playback-event-invalid"
	strictPlaybackKindAttemptMissing    = "playback-attempt-missing"
	strictPlaybackKindAttemptMismatch   = "playback-attempt-mismatch"
	strictPlaybackKindItemMismatch      = "playback-item-mismatch"
	strictPlaybackKindSessionMissing    = "playback-session-unavailable"
	strictPlaybackKindSessionMismatch   = "playback-session-mismatch"
	strictPlaybackKindSourceMismatch    = "playback-source-mismatch"
	strictPlaybackKindSourceUnavailable = "playback-source-unavailable"
	strictPlaybackKindStopUnconfirmed   = "playback-stop-unconfirmed"
)

// strictPlaybackEventFailure builds one frozen protocol error for a rejected
// strict event. Every rejection of a strict body is a 409: the attempt exists and
// is authorized, but the payload does not describe it.
func strictPlaybackEventFailure(kind, message string, retryable bool) *strictPlaybackError {
	return &strictPlaybackError{
		Status:    http.StatusConflict,
		Kind:      kind,
		Message:   message,
		Retryable: retryable,
	}
}

// strictPlaybackBindingFor returns the leased binding the strict ingress
// published. Its presence is the only switch between the strict and the legacy
// path: a request without it keeps the legacy behaviour unchanged.
func strictPlaybackBindingFor(reqCtx *RequestContext) (playbackIdentityBinding, bool) {
	if reqCtx == nil || reqCtx.Playback == nil {
		return playbackIdentityBinding{}, false
	}
	return *reqCtx.Playback, true
}

// strictPlaybackSlotIdentity is the concurrency-slot and watch-ordering identity
// of one strict attempt. The attempt ID is carried in both fields on purpose: it
// is the only value that proves which playback a start/progress/stop belongs to.
// A device SessionId is shared by every attempt of that device and an upstream may
// reuse one PlaySessionId for several sources and for a later attempt, so neither
// can tell a superseded attempt from the live one.
//
// Negotiation binds the reservation of a candidate to the same value (see
// PlaybackLimiter.BindReservationPlaySession), so a strict start claims exactly the
// unit its own attempt reserved.
func strictPlaybackSlotIdentity(binding playbackIdentityBinding) playbackSessionIdentity {
	if binding.AttemptID == "" {
		return playbackSessionIdentity{}
	}
	return playbackSessionIdentity{SessionID: binding.AttemptID, PlaySessionID: binding.AttemptID}
}

// strictPlaybackEvent is one validated strict session event.
//
// Upstream is the body sent to the binding's server. Local is the same body with
// the local attempt-scoped identity and without the upstream session value; it is
// only used by the local watch-store, tracker and activity decisions, so a reused
// upstream session can never make a late event look like the live one.
type strictPlaybackEvent struct {
	Binding  playbackIdentityBinding
	Kind     playbackEventKind
	Identity playbackSessionIdentity
	Upstream map[string]any
	Local    map[string]any
}

// strictPlaybackEventFor validates a client body against the binding and builds
// both the upstream and the local form of the event.
func (a *App) strictPlaybackEventFor(binding playbackIdentityBinding, body map[string]any, kind playbackEventKind) (*strictPlaybackEvent, *strictPlaybackError) {
	if binding.AttemptID == "" || binding.ServerID == "" || binding.VirtualItemID == "" || a.IDStore == nil {
		return nil, strictPlaybackEventFailure(strictPlaybackKindIdentityUnavailable, "playback attempt is unavailable; renegotiate", true)
	}
	// The body must state which attempt it belongs to. A strict event that omits
	// the attempt is rejected instead of being attributed to the bound one: the
	// omission is exactly the "missing marker" case the strict protocol forbids.
	attempt := playbackStringField(body, "EioAttemptId")
	switch attempt {
	case "":
		return nil, strictPlaybackEventFailure(strictPlaybackKindAttemptMissing, "the playback event carries no playback attempt", false)
	case binding.AttemptID:
	default:
		return nil, strictPlaybackEventFailure(strictPlaybackKindAttemptMismatch, "the playback event belongs to another playback attempt", false)
	}
	// ItemId must be the bound virtual item. A virtual ID of another item, a raw
	// upstream ID or an empty value is not repaired into one here.
	if item := playbackStringField(body, "ItemId"); item != binding.VirtualItemID {
		return nil, strictPlaybackEventFailure(strictPlaybackKindItemMismatch, "the playback event does not belong to this playback item", false)
	}
	upstreamSession, failure := a.strictPlaybackSessionForBinding(binding, playbackStringField(body, "PlaySessionId"))
	if failure != nil {
		return nil, failure
	}
	originalItem, ok := translateVirtualIDForServer(binding.VirtualItemID, binding.ServerID, a.IDStore)
	if !ok {
		return nil, strictPlaybackEventFailure(strictPlaybackKindItemMismatch, "the playback item has no source on the bound server", false)
	}

	upstream := make(map[string]any, len(body)+1)
	for key, value := range body {
		upstream[key] = value
	}
	for _, key := range [...]string{"EioAttemptId", "EioPlaybackToken", "EioPlaySessionId"} {
		delete(upstream, key)
	}
	upstream["ItemId"] = originalItem
	upstream["PlaySessionId"] = upstreamSession
	if upstreamSession == "" {
		delete(upstream, "PlaySessionId")
	}
	if raw, present := body["MediaSourceId"]; present {
		mediaSourceID, _ := raw.(string)
		if mediaSourceID == "" {
			delete(upstream, "MediaSourceId")
		} else {
			originalSource, ok := a.strictPlaybackSourceForBinding(binding, mediaSourceID)
			if !ok {
				return nil, strictPlaybackEventFailure(strictPlaybackKindSourceMismatch, "the media source does not belong to this playback attempt", false)
			}
			upstream["MediaSourceId"] = originalSource
		}
	}

	local := make(map[string]any, len(upstream))
	for key, value := range upstream {
		local[key] = value
	}
	delete(local, "PlaySessionId")
	local["SessionId"] = binding.AttemptID

	return &strictPlaybackEvent{
		Binding:  binding,
		Kind:     kind,
		Identity: strictPlaybackSlotIdentity(binding),
		Upstream: upstream,
		Local:    local,
	}, nil
}

// strictPlaybackSessionForBinding returns the upstream session the event is
// forwarded with: the candidate's own alias. The client echoes that alias
// (EioPlaySessionId); the client-facing virtual form of the same server-scoped
// session is accepted as the same value, and everything else — another
// candidate's session, another server's session, a local attempt ID or an unknown
// value — is rejected instead of being translated globally or guessed. The value
// sent upstream is always the alias, never a local identifier.
func (a *App) strictPlaybackSessionForBinding(binding playbackIdentityBinding, session string) (string, *strictPlaybackError) {
	alias := binding.UpstreamPlaySessionID
	if session == alias {
		return alias, nil
	}
	if session != "" && a.IDStore != nil {
		if resolved := a.IDStore.ResolveVirtualID(session); resolved != nil &&
			resolved.ServerID == binding.ServerID && resolved.OriginalID == alias {
			return alias, nil
		}
	}
	return "", strictPlaybackEventFailure(strictPlaybackKindSessionMismatch, "the playback event carries another playback session", false)
}

// strictPlaybackSourceForBinding maps the client's virtual MediaSourceId onto the
// bound server and checks it against the source the attempt froze.
//
// binding.SourceID is the raw upstream source ID, so the comparison happens after
// translation only: the virtual ID is never compared with a raw value. An empty
// frozen source means the candidate could not name one, in which case only the
// server and the item's copy on that server are provable. A source of another
// server or item is a different playback, not a detail to repair.
func (a *App) strictPlaybackSourceForBinding(binding playbackIdentityBinding, mediaSourceID string) (string, bool) {
	if mediaSourceID == "" || a.IDStore == nil {
		return "", false
	}
	if binding.SourceID == "" {
		return "", false
	}
	if mediaSourceID == binding.SourceID {
		return binding.SourceID, true
	}
	resolved := a.IDStore.ResolveVirtualID(mediaSourceID)
	if resolved == nil || resolved.ServerID != binding.ServerID {
		return "", false
	}
	if _, ok := translateVirtualIDForServer(binding.VirtualItemID, binding.ServerID, a.IDStore); !ok {
		// The item has no copy on the source's server, so the source cannot be one
		// of this item's candidates there.
		return "", false
	}
	if binding.SourceID != "" && resolved.OriginalID != binding.SourceID {
		return "", false
	}
	return resolved.OriginalID, true
}

// strictPlaybackEventPath is the upstream endpoint of one event kind.
func strictPlaybackEventPath(kind playbackEventKind) string {
	switch kind {
	case playbackEventStarted:
		return "/Sessions/Playing"
	case playbackEventProgress:
		return "/Sessions/Playing/Progress"
	default:
		return "/Sessions/Playing/Stopped"
	}
}

// strictPlaybackEventLabel names the event for diagnostics.
func strictPlaybackEventLabel(kind playbackEventKind) string {
	switch kind {
	case playbackEventStarted:
		return "Playing"
	case playbackEventProgress:
		return "Playing/Progress"
	default:
		return "Playing/Stopped"
	}
}

// writeStrictPlaybackStopUnconfirmed reports a stop whose upstream forward did not
// succeed. The attempt admits cleanup retries only;
// the answer never claims an accepted stop.
func writeStrictPlaybackStopUnconfirmed(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusBadGateway, map[string]any{
		"kind":      strictPlaybackKindStopUnconfirmed,
		"message":   "the upstream did not confirm the stop; retry",
		"retryable": true,
	})
}

// handleStrictSessionEvent runs one strict Playing/Progress/Stopped event. The
// ingress already proved the owner, the capability and the binding lifecycle, so
// this function only has to prove that the payload belongs to that attempt, to
// attribute it to the bound server and to keep the local bookkeeping consistent
// with the existing (legacy) semantics.
func (a *App) handleStrictSessionEvent(w http.ResponseWriter, r *http.Request, binding playbackIdentityBinding, kind playbackEventKind) {
	payload, err := decodeOptionalJSON(r)
	if err != nil {
		writeStrictPlaybackError(w, strictPlaybackEventFailure(strictPlaybackKindBodyInvalid, "the playback event body is not valid JSON", false))
		return
	}
	body, ok := asBodyMap(payload)
	if !ok {
		writeStrictPlaybackError(w, strictPlaybackEventFailure(strictPlaybackKindBodyInvalid, "the playback event payload is not an object", false))
		return
	}
	event, failure := a.strictPlaybackEventFor(binding, body, kind)
	if failure != nil {
		writeStrictPlaybackError(w, failure)
		return
	}
	if !a.strictPlaybackEventAllowed(r, event) {
		writeJSON(w, http.StatusForbidden, map[string]any{"message": "Access denied"})
		return
	}
	if a.rejectStrictSupersededWrite(w, r, binding, kind == playbackEventStopped) {
		return
	}
	if kind == playbackEventStopped {
		release, ok := a.beginStrictPlaybackCleanup(w, r, binding)
		if !ok {
			return
		}
		defer release()
	} else if _, err := a.PlaybackIdentities.resolve(binding.AttemptID, binding.Owner); err != nil {
		writeStrictPlaybackError(w, strictPlaybackErrorFor(err))
		return
	}
	client := a.Upstream.ClientByID(binding.ServerID)
	if client == nil || !client.IsOnline() {
		a.finishStrictPlaybackWithoutUpstream(w, r, event)
		return
	}
	activityEvent := a.beginUpstreamActivityEvent(r, binding.ServerID, client, map[string]any{"PlaySessionId": binding.AttemptID})
	if err := a.forwardNoContent(r, client, http.MethodPost, strictPlaybackEventPath(kind), nil, event.Upstream); err != nil {
		a.discardUpstreamActivityEvent(activityEvent)
		a.finishStrictPlaybackFailedForward(w, r, event, err)
		return
	}
	if kind == playbackEventStopped {
		a.confirmStrictPlaybackCleanup(r, binding)
	}
	verdict := a.applyStrictPlaybackEvent(r, event)
	a.recordUpstreamActivityEvent(r, activityEvent, binding.ServerID, binding.VirtualItemID, event.Local, kind, true, verdict)
	if kind == playbackEventStopped {
		a.retireStrictPlaybackBinding(binding)
	}
	w.WriteHeader(http.StatusNoContent)
}

// strictPlaybackEventAllowed keeps exactly the legacy route predicate for the
// event kind: a start must be allowed to aggregate the bound server, a
// progress/stop only needs the server grant. Narrowing a user's grants therefore
// applies to an in-flight strict attempt on its very next event.
func (a *App) strictPlaybackEventAllowed(r *http.Request, event *strictPlaybackEvent) bool {
	reqCtx := requestContextFrom(r.Context())
	if event.Kind == playbackEventStarted {
		return a.canUseAggregatedServer(reqCtx, event.Binding.ServerID)
	}
	return a.isServerAllowed(reqCtx, event.Binding.ServerID)
}

// applyStrictPlaybackEvent applies the concurrency-slot ownership rules and, when
// the event may be attributed to the live attempt, the local watch state. Both go
// through the existing helpers, so a strict event cannot bypass the slot state
// machine or the watch-store ordering rules.
func (a *App) applyStrictPlaybackEvent(r *http.Request, event *strictPlaybackEvent) playbackSessionVerdict {
	if event.Kind == playbackEventStopped {
		// finishStoppedSession applies the ownership-checked release itself.
		return a.finishStoppedSession(r, event.Binding.VirtualItemID, event.Binding.ServerID, event.Local, event.Identity, true)
	}
	// A report already in flight when stop began may complete, but may not
	// reclaim a released slot or overwrite the final local position.
	if _, err := a.PlaybackIdentities.resolve(event.Binding.AttemptID, event.Binding.Owner); err != nil {
		return playbackSessionVerdict{}
	}
	verdict := a.applySessionEvent(r, event.Binding.ServerID, event.Binding.VirtualItemID, event.Identity, event.Kind)
	if verdict.LocalProgress {
		a.recordSessionToWatchStore(r, event.Binding.VirtualItemID, event.Local, event.Binding.ServerID, false)
	}
	if event.Kind == playbackEventStarted && (verdict.Accepted || !verdict.Tracked) {
		// Only a start the limiter permitted may become the tracked live attempt, so
		// the accepted attempt's own progress is not mistaken for superseded data.
		if reqCtx := requestContextFrom(r.Context()); reqCtx != nil && reqCtx.ProxyUser != nil {
			a.sessionEvents.StartSession(reqCtx.ProxyUser.UserID, event.Binding.VirtualItemID, sessionIdentityID(event.Local))
		}
	}
	return verdict
}

// finishStrictPlaybackFailedForward handles an event the upstream did not accept.
//
// A preparation failure or an explicit refusal means the event did not happen
// upstream, so no watch state is written for a start/progress and a refused stop
// does not store the position (legacy parity). A transport failure may still have
// been delivered, so the local record is kept. In every case the local concurrency
// unit must not be stranded by a stop. Its stopping binding admits only cleanup
// retries; only a confirmed cleanup may retire it.
func (a *App) finishStrictPlaybackFailedForward(w http.ResponseWriter, r *http.Request, event *strictPlaybackEvent, err error) {
	status, isPreparation := preparationErrorStatus(err)
	_, refused := asUpstreamHTTPError(err)
	if a.Logger != nil && !isPreparation {
		a.Logger.Warnf("strict %s upstream error (server %s): %v",
			strictPlaybackEventLabel(event.Kind), event.Binding.ServerID, redactURLInError(err))
	}
	switch event.Kind {
	case playbackEventStarted, playbackEventProgress:
		if isPreparation {
			// The request never left the process (identity, auth or configuration).
			// Report the frozen protocol status instead of pretending success.
			writeJSON(w, status, preparationErrorBody(err))
			return
		}
		if refused {
			// An explicit non-2xx is a refusal: no local watch state for an event
			// that did not happen. The client keeps the fire-and-forget answer of a
			// session report.
			w.WriteHeader(http.StatusNoContent)
			return
		}
		// Transport failure: the request may have been delivered, so the local
		// record keeps its existing behaviour.
		a.applyStrictPlaybackEvent(r, event)
		w.WriteHeader(http.StatusNoContent)
	default: // playbackEventStopped
		// The client really stopped playing: release the concurrency unit and, when
		// the upstream did not refuse, keep the final position. The attempt is not
		// retired and the answer does not claim an accepted stop.
		a.finishStoppedSession(r, event.Binding.VirtualItemID, event.Binding.ServerID, event.Local, event.Identity, !refused)
		if isPreparation {
			writeJSON(w, status, preparationErrorBody(err))
			return
		}
		writeStrictPlaybackStopUnconfirmed(w)
	}
}

// finishStrictPlaybackWithoutUpstream handles an event whose bound upstream is
// gone. A start/progress that never reached the upstream is not reported as
// accepted; a stop still ends the playback locally (holding the concurrency unit
// would leak it) while the answer still does not claim an upstream confirmation.
func (a *App) finishStrictPlaybackWithoutUpstream(w http.ResponseWriter, r *http.Request, event *strictPlaybackEvent) {
	if event.Kind == playbackEventStopped {
		a.finishStoppedSession(r, event.Binding.VirtualItemID, event.Binding.ServerID, event.Local, event.Identity, true)
		writeStrictPlaybackStopUnconfirmed(w)
		return
	}
	writeStrictPlaybackError(w, &strictPlaybackError{
		Status:    http.StatusConflict,
		Kind:      strictPlaybackKindSourceUnavailable,
		Message:   "the playback source is not available; renegotiate",
		Retryable: true,
	})
}

// retireStrictPlaybackBinding finalizes a drained attempt. Terminal states are
// final, so a repeated retirement never revives the binding.
func (a *App) retireStrictPlaybackBinding(binding playbackIdentityBinding) {
	if a.PlaybackIdentities == nil || binding.AttemptID == "" {
		return
	}
	if err := a.PlaybackIdentities.retire(binding.AttemptID, binding.Owner); err != nil {
		if a.Logger != nil {
			a.Logger.Debugf("strict playback retire for attempt was not recorded: %v", err)
		}
		return
	}
	a.StreamResources.removeAttempt(binding.AttemptID)
}

// finishStrictPlaybackAttemptLocally applies the local end-of-attempt bookkeeping
// without contacting the upstream. The ingress answers a repeated stop of an
// already draining/finished attempt with its own idempotent acknowledgment and owns
// the binding at that point, so this helper lets that path release anything the
// attempt still holds without inventing upstream evidence. It is idempotent and
// writes no watch position, because a repeated stop has no verified payload.
func (a *App) finishStrictPlaybackAttemptLocally(r *http.Request, binding playbackIdentityBinding) {
	a.applySessionEvent(r, binding.ServerID, binding.VirtualItemID, strictPlaybackSlotIdentity(binding), playbackEventStopped)
}
