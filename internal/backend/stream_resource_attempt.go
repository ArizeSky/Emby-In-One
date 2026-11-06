package backend

// removeAttempt is rollback/local reclamation, not an upstream stop ACK.
// Existing request holders have copied entries and own their binding leases.
func (s *streamResourceStore) removeAttempt(attempt string) {
	if s == nil || attempt == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, session := range s.sessions {
		if key.attempt != attempt {
			continue
		}
		for id := range session.handles {
			delete(s.entries, id)
		}
		s.manifestCount -= len(session.manifests)
		delete(s.sessions, key)
	}
}
