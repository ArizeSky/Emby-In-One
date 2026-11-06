package backend

import (
	"errors"
	"net/http"
	"strings"
)

var errPlaybackCleanupConfirmed = errors.New("playback cleanup already confirmed")

// Independent cleanup operations must never ACK one another. SessionStopped
// may finish the attempt before the browser sends ActiveEncodings/PlayingItems.
// Stopping/retired bindings admit only bounded cleanup leases, never new reads
// or Playing/Progress. Only a successful send records its own ACK bit.
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
	if entry.state == playbackIdentityFailed {
		return nil, ErrPlaybackIdentityTerminal
	}
	entry.leases++
	return &playbackIdentityLease{store: s, attemptID: attempt, binding: entry.snapshot()}, nil
}

// beginCleanup closes admission before any upstream send, without inventing an
// ACK. Each operation has one in-flight sender; failures release that claim so
// cleanup can be retried without reopening playback or extending retention.
func (s *playbackIdentityStore) beginCleanup(attempt string, owner playbackIdentityOwner, operation uint8) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, err := s.entryForOwnerLocked(attempt, owner, s.now())
	if err != nil {
		return nil, err
	}
	if entry.state == playbackIdentityFailed {
		return nil, ErrPlaybackIdentityTerminal
	}
	if entry.cleanupConfirmed&operation != 0 {
		return nil, errPlaybackCleanupConfirmed
	}
	if s.newerActiveAliasLocked(entry, operation) {
		return nil, ErrPlaybackIdentityAliasConflict
	}
	if operation == 0 || entry.cleanupInFlight&operation != 0 {
		return nil, ErrPlaybackIdentityStopping
	}
	if entry.state == playbackIdentityPending || entry.state == playbackIdentityActive {
		entry.state, entry.stateChangedAt = playbackIdentityStopping, s.now()
	}
	entry.cleanupInFlight |= operation
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		entry.cleanupInFlight &^= operation
	}, nil
}

func (a *App) beginStrictPlaybackCleanup(w http.ResponseWriter, r *http.Request, binding playbackIdentityBinding) (func(), bool) {
	release, err := a.PlaybackIdentities.beginCleanup(binding.AttemptID, binding.Owner, playbackCleanupOperation(r))
	if err != nil {
		if err == errPlaybackCleanupConfirmed {
			// The handler already validated the body/route. Another sender may
			// have confirmed this operation since the ingress took its snapshot.
			writeStrictPlaybackStopAcknowledged(w)
		} else if err == ErrPlaybackIdentityStopping {
			writeStrictPlaybackError(w, errStrictStopPending)
		} else if err == ErrPlaybackIdentityAliasConflict {
			a.rejectStrictSupersededBinding(w, r, binding, true)
		} else {
			writeStrictPlaybackError(w, strictPlaybackErrorFor(err))
		}
		return nil, false
	}
	return release, true
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
