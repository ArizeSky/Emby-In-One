package backend

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"hash"
	"net"
	"net/url"
	"strings"
)

// Account-range fingerprint for upstream activity statistics.
//
// The fingerprint must stay stable across cosmetic configuration edits
// (display name, ordering, user agent, proxy, streaming URLs) and must change
// when the actual upstream account changes (API base, auth mode, username, API
// key rotation). It must never be reversible and never contain a password or a
// short-lived login token: those are excluded on purpose, so a password change
// or login token refresh does not discard observed playback history.
const upstreamAccountScopeVersion = "v1"

// UpstreamAccountScope returns an opaque, versioned SHA-256 digest identifying
// the account range of cfg. An empty string means no stable account identity is
// configured yet; callers must treat that as scope-unavailable instead of
// sharing one bucket across unrelated servers.
func UpstreamAccountScope(cfg *UpstreamConfig) string {
	if cfg == nil {
		return ""
	}
	origin := normalizeUpstreamActivityOrigin(cfg.URL)
	authMode, credentialDigest := upstreamAccountAuthMaterial(cfg.Username, cfg.APIKey)
	username := strings.TrimSpace(cfg.Username)
	if origin == "" && username == "" && credentialDigest == "" {
		return ""
	}
	digest := sha256.New()
	writeUpstreamScopeField(digest, upstreamAccountScopeVersion)
	writeUpstreamScopeField(digest, origin)
	writeUpstreamScopeField(digest, authMode)
	writeUpstreamScopeField(digest, username)
	writeUpstreamScopeField(digest, credentialDigest)
	return upstreamAccountScopeVersion + ":" + hex.EncodeToString(digest.Sum(nil))
}

// SameUpstreamAccountScope compares two fingerprints without leaking where they
// differ. Empty fingerprints never match, so an unknown scope cannot inherit
// another server's stored activity.
func SameUpstreamAccountScope(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// upstreamAccountAuthMaterial classifies the configured authentication and,
// for API-key auth only, hashes the key. The key never enters the scope in
// plaintext and the password is never part of it.
func upstreamAccountAuthMaterial(username, apiKey string) (mode, credentialDigest string) {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey != "" {
		sum := sha256.Sum256([]byte(apiKey))
		return "apikey", "sha256:" + hex.EncodeToString(sum[:])
	}
	if strings.TrimSpace(username) != "" {
		return "password", ""
	}
	return "none", ""
}

// normalizeUpstreamActivityOrigin canonicalizes the API base URL: lowercase
// scheme/host, default port removed, trailing slashes trimmed, credentials,
// query and fragment dropped. An unparseable value falls back to the trimmed
// raw string so the fingerprint is still deterministic.
func normalizeUpstreamActivityOrigin(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	parsed, err := url.Parse(trimmed)
	if err != nil || parsed.Host == "" {
		return strings.TrimRight(trimmed, "/")
	}
	scheme := strings.ToLower(parsed.Scheme)
	host := stripUpstreamActivityDefaultPort(scheme, strings.ToLower(parsed.Host))
	path := strings.TrimRight(parsed.Path, "/")
	return scheme + "://" + host + path
}

func stripUpstreamActivityDefaultPort(scheme, host string) string {
	hostname, port, err := net.SplitHostPort(host)
	if err != nil {
		return host
	}
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		if strings.Contains(hostname, ":") {
			return "[" + hostname + "]"
		}
		return hostname
	}
	return host
}

// writeUpstreamScopeField writes a length-prefixed field so concatenation
// ambiguities cannot produce identical digests for different identities.
func writeUpstreamScopeField(digest hash.Hash, value string) {
	_, _ = fmt.Fprintf(digest, "%d:", len(value))
	_, _ = digest.Write([]byte(value))
}
