package backend

// aggregationEnabled reports whether the upstream with the given persistent ID
// participates in content aggregation. The config is the single source of
// truth: a nil store, an empty ID and an unknown ID all mean "not available".
func (s *ConfigStore) aggregationEnabled(serverID string) bool {
	if s == nil || serverID == "" {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.config == nil {
		return false
	}
	for _, upstream := range s.config.Upstream {
		if upstream.ID == serverID {
			return !upstream.AggregationDisabled
		}
	}
	return false
}

// isServerAggregationEnabled reads the aggregation switch from the ConfigStore.
// It never reads client.Config.AggregationDisabled, so a stale client pointer or
// snapshot cannot keep answering with an old value.
func (a *App) isServerAggregationEnabled(serverID string) bool {
	return a.ConfigStore.aggregationEnabled(serverID)
}

// canUseAggregatedServer is the combined content predicate: the server must be
// switched on for aggregation and the current request must hold an account
// grant for it. Online status is still checked by the candidate collectors.
func (a *App) canUseAggregatedServer(reqCtx *RequestContext, serverID string) bool {
	return a.isServerAggregationEnabled(serverID) && a.isServerAllowed(reqCtx, serverID)
}
