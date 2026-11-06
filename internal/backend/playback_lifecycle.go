package backend

// Reclaim related local tables when admitting a negotiation. Their expirations
// are not upstream stop evidence. In-flight sends hold binding leases and copied
// resource entries; removing admission indexes never changes those snapshots.
func (a *App) reclaimPlaybackAttempts() {
	if a.PlaybackIdentities == nil {
		return
	}
	live, known := map[string]bool{}, map[string]bool{}
	store := a.PlaybackIdentities
	store.mu.Lock()
	store.pruneLocked(store.now())
	for attempt, entry := range store.entries {
		known[attempt] = true
		live[attempt] = entry.state == playbackIdentityPending || entry.state == playbackIdentityActive
	}
	defer store.mu.Unlock()
	if a.StreamResources != nil {
		resources := a.StreamResources
		stale := map[string]bool{}
		resources.mu.Lock()
		for _, entry := range resources.entries {
			if entry.AttemptID != "" && !live[entry.AttemptID] {
				stale[entry.AttemptID] = true
			}
		}
		resources.mu.Unlock()
		for attempt := range stale {
			resources.removeAttempt(attempt)
		}
	}
	if a.PlaybackCapabilities != nil {
		caps := a.PlaybackCapabilities
		var stale []string
		caps.mu.Lock()
		caps.pruneLocked(caps.now())
		for attempt := range caps.byAttempt {
			if !known[attempt] {
				stale = append(stale, attempt)
			}
		}
		caps.mu.Unlock()
		for _, attempt := range stale {
			caps.revokeByAttempt(attempt)
		}
	}
}
