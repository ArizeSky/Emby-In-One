package backend

import (
	"net/http"
	"strings"
)

// Independent cleanup operations must never ACK one another. SessionStopped
// may finish the attempt before the browser sends ActiveEncodings/PlayingItems.
// Terminal bindings admit only these bounded cleanup leases, never new reads or
// Playing/Progress, and only a real successful send records its own ACK bit.
func playbackCleanupOperation(r *http.Request) uint8 {
	if r == nil || r.URL == nil {
		return 0
	}
	path := strings.ToLower(r.URL.Path)
	if r.Method == http.MethodPost && strings.HasSuffix(path, "/sessions/playing/stopped") {
		return 1
	}
	if r.Method == http.MethodDelete {
		if strings.HasSuffix(path, "/videos/activeencodings") {
			return 2
		}
		if strings.Contains(path, "/users/") && strings.Contains(path, "/playingitems/") {
			return 4
		}
	}
	return 0
}

func (s *playbackIdentityStore) leaseCleanup(attempt string, owner playbackIdentityOwner) (*playbackIdentityLease, error) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneLocked(now)
	entry, err := s.entryForOwnerLocked(attempt, owner, now)
	if err != nil {
		return nil, err
	}
	if entry.state != playbackIdentityRetired {
		return nil, ErrPlaybackIdentityTerminal
	}
	entry.leases++
	return &playbackIdentityLease{store: s, attemptID: attempt, binding: entry.snapshot()}, nil
}

func (a *App) confirmStrictPlaybackCleanup(r *http.Request, binding playbackIdentityBinding) {
	operation := playbackCleanupOperation(r)
	if operation == 0 || a.PlaybackIdentities == nil {
		return
	}
	store := a.PlaybackIdentities
	store.mu.Lock()
	if entry, err := store.entryForOwnerLocked(binding.AttemptID, binding.Owner, store.now()); err == nil {
		entry.cleanupConfirmed |= operation
	}
	store.mu.Unlock()
}

func (a *App) validateStrictCleanupReplay(w http.ResponseWriter, r *http.Request, binding playbackIdentityBinding) bool {
	if playbackCleanupOperation(r) != 1 {
		return true
	}
	payload, err := decodeOptionalJSON(r)
	body, ok := asBodyMap(payload)
	if err != nil || !ok {
		writeStrictPlaybackError(w, strictPlaybackEventFailure(strictPlaybackKindBodyInvalid, "invalid repeated stop body", false))
		return false
	}
	if _, failure := a.strictPlaybackEventFor(binding, body, playbackEventStopped); failure != nil {
		writeStrictPlaybackError(w, failure)
		return false
	}
	return true
}
