package backend

import "net/http"

func sameRemotePlaybackAlias(left, right *playbackIdentityEntry) bool {
	return left.upstreamPlaySessionID != "" && left.serverID == right.serverID &&
		left.auth.Principal == right.auth.Principal && left.upstreamPlaySessionID == right.upstreamPlaySessionID
}

func remotePlaybackWriteConflict(left, right *playbackIdentityEntry, operation uint8) bool {
	if operation == 4 { // PlayingItems DELETE addresses an account and item.
		return left.serverID == right.serverID && left.auth.Principal == right.auth.Principal && left.virtualItemID == right.virtualItemID
	}
	return sameRemotePlaybackAlias(left, right)
}

// An upstream can reuse PlaySessionId. The explicit local attempt is sufficient
// to authorize reads, but the upstream stop API cannot distinguish two attempts
// carrying the same alias. Do not send a late write that could stop the newer
// selected attempt. Unselected pending candidates do not supersede anything.
func (s *playbackIdentityStore) newerActiveAlias(binding playbackIdentityBinding, operation uint8) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.entries[binding.AttemptID]
	if current == nil {
		return false
	}
	return s.newerActiveAliasLocked(current, operation)
}

func (s *playbackIdentityStore) newerActiveAliasLocked(current *playbackIdentityEntry, operation uint8) bool {
	now := s.now()
	for _, other := range s.entries {
		if other.attemptID == current.attemptID || other.state != playbackIdentityActive || s.expiredLocked(other, now) {
			continue
		}
		// Remote session cleanup has no local owner/item dimension. Different
		// local sessions and films sharing the upstream account can collide too.
		if remotePlaybackWriteConflict(other, current, operation) && other.activationSequence > current.activationSequence {
			return true
		}
	}
	return false
}

// Activation and cleanup claims share the store lock. A new candidate cannot
// become active while an older cleanup can still affect this remote session.
func (s *playbackIdentityStore) aliasCleanupInFlightLocked(current *playbackIdentityEntry) bool {
	for _, other := range s.entries {
		if other != current {
			for _, operation := range []uint8{1, 2, 4} {
				if other.cleanupInFlight&operation != 0 && remotePlaybackWriteConflict(other, current, operation) {
					return true
				}
			}
		}
	}
	return false
}

func (a *App) rejectStrictSupersededWrite(w http.ResponseWriter, r *http.Request, binding playbackIdentityBinding, stopping bool) bool {
	if !a.PlaybackIdentities.newerActiveAlias(binding, playbackCleanupOperation(r)) {
		return false
	}
	a.rejectStrictSupersededBinding(w, r, binding, stopping)
	return true
}

func (a *App) rejectStrictSupersededBinding(w http.ResponseWriter, r *http.Request, binding playbackIdentityBinding, stopping bool) {
	if stopping {
		a.finishStrictPlaybackAttemptLocally(r, binding)
		_ = a.PlaybackIdentities.fail(binding.AttemptID, binding.Owner)
		a.StreamResources.removeAttempt(binding.AttemptID)
	}
	writeStrictPlaybackError(w, &strictPlaybackError{Status: 409, Kind: "playback-superseded", Message: "a newer attempt shares this upstream cleanup scope; no upstream write was sent"})
}
