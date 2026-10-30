package backend

// playbackSessionIdentity is what a client tells us about the concrete
// playback attempt an event belongs to. Emby clients send a device-scoped
// SessionId and a playback-scoped PlaySessionId; either may be missing.
//
// Identity is used for *slot ownership only*. It never replaces the existing
// per-item session event tracker (watch_event.go), whose SessionId-first
// ordering keeps the local watch store consistent: the tracker identifies one
// "live session per user+item", while a concurrency slot is one unit per
// user+server that may host several item attempts over its lifetime.
//
// Matching rules (tolerant on one missing field, strict on conflicting or
// disjoint ones):
//   - an empty identity matches nothing; callers must apply the conservative
//     anonymous-event rules themselves instead of treating "" as a wildcard;
//   - ownership requires at least one identity field present on *both* sides
//     with the same value. A SessionId-only identity and a PlaySessionId-only
//     identity are disjoint observations, not evidence about the same attempt,
//     even though each field is individually missing on the other side;
//   - two different SessionIds never match;
//   - two different non-empty PlaySessionIds never match, even when the
//     SessionId is identical. That is the same-client replay case: replaying
//     one item keeps the device session but starts a new play session, and a
//     late event of the previous attempt must not act on the new one;
//   - a missing field on one side is not a conflict, so clients that omit
//     PlaySessionId on Stopped still match the session they started through a
//     shared SessionId.
type playbackSessionIdentity struct {
	SessionID     string
	PlaySessionID string
}

// playbackSessionIdentityFromBody reads the identity a session event carries.
// The body must be the pre-translation payload: item/session IDs are rewritten
// to upstream IDs before forwarding, while the identity the client will keep
// using is the one it sent (a virtual PlaySessionId from PlaybackInfo).
func playbackSessionIdentityFromBody(body map[string]any) playbackSessionIdentity {
	identity := playbackSessionIdentity{}
	if sessionID, _ := body["SessionId"].(string); sessionID != "" {
		identity.SessionID = sessionID
	}
	if playSessionID, _ := body["PlaySessionId"].(string); playSessionID != "" {
		identity.PlaySessionID = playSessionID
	}
	return identity
}

// playbackSessionIdentityFromPlaySessionID builds the identity of a
// per-resource stream request, which carries only the playback-scoped
// PlaySessionId. The resulting identity has no SessionId, so it matches a
// claimed session through the tolerant missing-field rule.
func playbackSessionIdentityFromPlaySessionID(playSessionID string) playbackSessionIdentity {
	return playbackSessionIdentity{PlaySessionID: playSessionID}
}

func (i playbackSessionIdentity) empty() bool {
	return i.SessionID == "" && i.PlaySessionID == ""
}

func (i playbackSessionIdentity) matches(other playbackSessionIdentity) bool {
	if i.empty() || other.empty() {
		return false
	}
	sharedSession := i.SessionID != "" && other.SessionID != ""
	sharedPlaySession := i.PlaySessionID != "" && other.PlaySessionID != ""
	if !sharedSession && !sharedPlaySession {
		// Disjoint fields only: nothing observed by both sides proves that the
		// two identities describe the same playback attempt.
		return false
	}
	if sharedSession && i.SessionID != other.SessionID {
		return false
	}
	if sharedPlaySession && i.PlaySessionID != other.PlaySessionID {
		return false
	}
	return true
}

// conflicts reports whether the two identities contradict each other: at least
// one field is present on both sides with a different value. Disjoint fields,
// missing fields and equal shared fields are not a contradiction. Callers use
// this to prove a *different* attempt when they have no positive binding for
// the current one; it is deliberately not the negation of matches (disjoint
// observations are neither proof of the same attempt nor proof of another).
func (i playbackSessionIdentity) conflicts(other playbackSessionIdentity) bool {
	if i.SessionID != "" && other.SessionID != "" && i.SessionID != other.SessionID {
		return true
	}
	if i.PlaySessionID != "" && other.PlaySessionID != "" && i.PlaySessionID != other.PlaySessionID {
		return true
	}
	return false
}
