package backend

import "net/http"

// An upstream can reuse PlaySessionId. The explicit local attempt is sufficient
// to authorize reads, but the upstream stop API cannot distinguish two attempts
// carrying the same alias. Do not send a late write that could stop the newer
// selected attempt. Unselected pending candidates do not supersede anything.
func (s *playbackIdentityStore) newerActiveAlias(binding playbackIdentityBinding) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.entries[binding.AttemptID]
	if current == nil {
		return false
	}
	now := s.now()
	for _, other := range s.entries {
		if other.attemptID == current.attemptID || other.state != playbackIdentityActive || s.expiredLocked(other, now) {
			continue
		}
		if other.serverID == current.serverID && samePlaybackIdentityOwner(other.owner, current.owner) && other.virtualItemID == current.virtualItemID && other.upstreamPlaySessionID == current.upstreamPlaySessionID && other.createdAt.After(current.createdAt) {
			return true
		}
	}
	return false
}

func (a *App) rejectStrictSupersededWrite(w http.ResponseWriter, r *http.Request, binding playbackIdentityBinding, stopping bool) bool {
	if !a.PlaybackIdentities.newerActiveAlias(binding) {
		return false
	}
	if stopping {
		a.finishStrictPlaybackAttemptLocally(r, binding)
		_ = a.PlaybackIdentities.fail(binding.AttemptID, binding.Owner)
		a.StreamResources.removeAttempt(binding.AttemptID)
	}
	writeStrictPlaybackError(w, &strictPlaybackError{Status: 409, Kind: "playback-superseded", Message: "a newer attempt uses this upstream session; no upstream write was sent"})
	return true
}
