package backend

// upstreamAuthSnapshot is one consistent read of an upstream's authentication
// state. UserID and AccessToken are written together by setOnline, so reading
// them separately can pair the user ID of one login with the token of another.
// Every outbound request that needs both must take one snapshot instead.
type upstreamAuthSnapshot struct {
	UserID      string
	AccessToken string
}

// authSnapshot reads UserID and AccessToken under a single RLock.
func (c *UpstreamClient) authSnapshot() upstreamAuthSnapshot {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return upstreamAuthSnapshot{UserID: c.UserID, AccessToken: c.AccessToken}
}

// clientUserID returns the upstream's real user ID for single-field reads in
// handlers. Code that needs the user ID and the token together must use
// authSnapshot instead of calling this twice.
func (c *UpstreamClient) clientUserID() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.UserID
}

// serverIndexValue returns the upstream's configured index. ServerIndex is set
// once at construction and never mutated, so no lock is needed.
func (c *UpstreamClient) serverIndexValue() int {
	return c.ServerIndex
}
