package backend

import (
	"fmt"
	"net/http"
)

// A response may contain multiple media sources. Freeze each source into its own
// candidate while still pending; the credential-free identity/auth snapshot is
// identical to the one used before that upstream PlaybackInfo call.
func (s *playbackIdentityStore) setPendingSource(attempt string, owner playbackIdentityOwner, source string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, err := s.entryForOwnerLocked(attempt, owner, s.now())
	if err != nil {
		return err
	}
	if entry.state != playbackIdentityPending || entry.sourceID != "" && entry.sourceID != source {
		return fmt.Errorf("%w: candidate source frozen", ErrPlaybackIdentityInvalid)
	}
	entry.sourceID = source
	return nil
}

func (a *App) claimStrictPlaybackSlot(w http.ResponseWriter, r *http.Request, binding playbackIdentityBinding) bool {
	if a.PlaybackLimiter == nil {
		return true
	}
	if binding.State == playbackIdentityPending {
		a.PlaybackLimiter.BindReservationPlaySession(binding.SlotReservation, binding.AttemptID)
	}
	clone := r.Clone(r.Context())
	copiedURL := *r.URL
	clone.URL = &copiedURL
	query := clone.URL.Query()
	setQueryValue(query, "PlaySessionId", binding.AttemptID)
	clone.URL.RawQuery = query.Encode()
	return a.switchPlaybackSlot(w, clone, binding.ServerID, binding.SlotReservation.serverID, binding.VirtualItemID)
}

func (s *playbackIdentityStore) setPendingReservation(attempt string, owner playbackIdentityOwner, reservation playbackSlotReservation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, err := s.entryForOwnerLocked(attempt, owner, s.now())
	if err != nil {
		return err
	}
	if entry.state != playbackIdentityPending {
		return ErrPlaybackIdentityInvalid
	}
	entry.slotReservation = reservation
	return nil
}
