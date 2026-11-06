package backend

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// Account configuration and the actual authenticated upstream user both identify
// the principal. API-key rotation is conservatively a new scope until a future
// explicit principal proof permits migration; token-only password refresh is not.
func playbackAuthPrincipal(config UpstreamConfig, userID string) string {
	if userID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte("playback-principal-v1\x00" + UpstreamAccountScope(&config) + "\x00" + userID))
	return hex.EncodeToString(sum[:])
}

func (c *UpstreamClient) playbackConfigRevision() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return playbackConfigRevisionLocked(c)
}

// Caller holds c.mu. Configuration and credential permission are linearized at
// authSnapshot: sends already authorized there may finish; later snapshots fail.
func playbackConfigRevisionLocked(c *UpstreamClient) string {
	cfg := c.Config
	revision := uint64(0)
	if c.residentIdentity != nil {
		revision = c.residentIdentity.IdentityVersion
	}
	data, _ := json.Marshal([]any{cfg.SpoofClient, cfg.UAProfileID, revision, cfg.CustomUserAgent, cfg.CustomClient, cfg.CustomClientVersion, cfg.CustomDeviceName, cfg.CustomDeviceId})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Successful login is the only point that can attest which identity acquired a
// credential. Recording metadata separately from setOnline would allow a sender
// to observe token B with login A's fingerprint, so publication uses one lock.
func (c *UpstreamClient) setOnlineWithIdentity(token, userID string, identity identitySnapshot) {
	c.mu.Lock()
	cfg := c.Config
	compatibility := playbackIdentityAuth{Principal: playbackAuthPrincipal(cfg, userID), ConfigRevision: playbackConfigRevisionLocked(c), IdentityFingerprint: playbackIdentityFingerprint(identity)}
	if !validPlaybackIdentityUserAgent(identity.Headers.Get("User-Agent")) {
		compatibility = playbackIdentityAuth{}
	}
	defer c.mu.Unlock()
	c.authGeneration++
	c.AccessToken, c.UserID = token, userID
	c.Online, c.LastError = true, ""
	c.authCompatibility = compatibility
}

// validatePlaybackAuth validates the very snapshot the sender will use for
// URL/body/header preparation. It never fetches credentials again. The ingress
// owns leases/route/owner checks; this is the final identity/auth boundary.
func (c *UpstreamClient) validatePlaybackAuth(reqCtx *RequestContext, auth upstreamAuthSnapshot) error {
	if reqCtx == nil || reqCtx.Playback == nil {
		return nil
	}
	binding := reqCtx.Playback
	if binding.ServerID != c.ID || binding.Auth.ConfigRevision != auth.ConfigRevision ||
		auth.Compatibility.Principal != auth.ConfiguredPrincipal ||
		!binding.compatibleAuth(auth.Compatibility) || auth.AccessToken == "" ||
		playbackIdentityFingerprint(binding.Identity) != auth.Compatibility.IdentityFingerprint {
		return &outboundPreparationError{Status: 409, Kind: "playback-auth-incompatible", Field: "attempt", Message: "playback identity requires renegotiation"}
	}
	return nil
}
