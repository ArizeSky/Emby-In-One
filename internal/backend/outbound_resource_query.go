package backend

import (
	"net/http"
	"net/url"
	"strings"
)

// prepareHopQuery leaves opaque URLs byte-exact. Only configured Emby business
// routes may refresh stale authentication. An external ambiguous credential is
// rejected rather than guessed to be either ours or a CDN's own api_key.
// Known token values provide evidence for safe filtering of unsigned external
// queries; signed credential mutations always require renegotiation.
func prepareHopQuery(target *url.URL, trusted bool, token string, known ...string) error {
	return prepareHopQueryWithProvenance(target, trusted, token, nil, known...)
}

// prepareHopQueryWithProvenance is prepareHopQuery plus explicit registration
// authority. Provenance only applies to the exact URL it was bound to, so an
// external query can preserve the target's own same-name credential while a
// client-supplied or redirected-away URL stays conservative. Known private
// values are still removed (or, on a managed trusted route, refreshed), except
// for an exact media URL credential explicitly delegated by a trusted response.
func prepareHopQueryWithProvenance(target *url.URL, trusted bool, token string, provenance *resourceQueryProvenance, known ...string) error {
	if target.RawQuery == "" {
		return nil
	}
	if !provenance.matches(target) {
		provenance = nil
	}
	fields := strings.Split(target.RawQuery, "&")
	signed := false
	for _, field := range fields {
		key, _, _ := strings.Cut(field, "=")
		decoded, err := url.QueryUnescape(key)
		if err != nil {
			return hopQueryError("invalid-resource-query")
		}
		switch strings.ToLower(decoded) {
		case "sig", "signature", "hmac", "hash", "policy", "key-pair-id", "x-amz-signature", "x-goog-signature", "expires", "expire", "sv", "se":
			signed = true
		}
	}
	managed := trusted && managedEmbyResourcePath(target.Path)
	kept := make([]string, 0, len(fields))
	changed := false
	for _, field := range fields {
		key, value, _ := strings.Cut(field, "=")
		decoded, _ := url.QueryUnescape(key)
		if !isProxyCredentialQueryKey(decoded) {
			kept = append(kept, field)
			continue
		}
		val, err := url.QueryUnescape(value)
		if err != nil {
			return hopQueryError("invalid-resource-query")
		}
		if trusted && val == token && token != "" {
			kept = append(kept, field)
			continue
		}
		if !trusted && provenance != nil && provenance.delegated != "" && val == provenance.delegated {
			// The trusted media response explicitly delegated this URL credential.
			// Keep its original bytes, including signatures; never install a header.
			kept = append(kept, field)
			continue
		}
		private := strings.EqualFold(decoded, "x-emby-token") || provenance.knowsPrivate(val)
		for _, secret := range known {
			if secret != "" && secret == val {
				private = true
			}
		}
		if !trusted && !private {
			if !provenance.attested() {
				// Without registration provenance this cannot safely be stripped OR sent.
				return hopQueryError("resource-query-provenance-required")
			}
			// The URL was issued by the authoritative resource answer itself, so
			// this is the target's own credential. Keep every byte, including a
			// signature that covers it.
			kept = append(kept, field)
			continue
		}
		if signed {
			return hopQueryError("signed-credential-query")
		}
		if trusted && !managed {
			return hopQueryError("resource-query-provenance-required")
		}
		changed = true
		if trusted && token != "" {
			kept = append(kept, key+"="+url.QueryEscape(token))
		}
	}
	if changed {
		target.RawQuery = strings.Join(kept, "&")
	}
	return nil
}

func managedEmbyResourcePath(path string) bool {
	// A deployment prefix is allowed, but arbitrary same-origin file endpoints
	// are not reinterpreted as mutable Emby authentication routes.
	path = strings.ToLower(path)
	for _, root := range []string{"/videos/", "/audio/", "/items/"} {
		if at := strings.Index(path, root); at >= 0 {
			rest := strings.TrimPrefix(path[at:], root)
			id, tail, ok := strings.Cut(rest, "/")
			if ok && id != "" && tail != "" {
				return true
			}
		}
	}
	return false
}

func hopQueryError(kind string) *outboundPreparationError {
	return &outboundPreparationError{Status: http.StatusBadGateway, Kind: kind, Field: "query", Message: "upstream resource URL requires renegotiation"}
}
