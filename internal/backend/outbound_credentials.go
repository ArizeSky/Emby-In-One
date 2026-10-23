package backend

import (
	"net/url"
	"strings"
)

// isProxyCredentialQueryKey reports whether a query parameter name carries a
// proxy or upstream credential rather than business data. Matching is
// case-insensitive so api_key, ApiKey, x-emby-token, X-Emby-Token and any other
// casing of those names are treated as the same credential.
func isProxyCredentialQueryKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "api_key", "apikey", "x-emby-token":
		return true
	default:
		return false
	}
}

// stripProxyCredentialQuery removes every proxy credential parameter from values,
// whatever its spelling or how many times it appears, and reports whether anything
// was removed together with the first credential value seen (kept only for
// comparison, never logged). It is the single classification the client→upstream
// and response-URL cleanup paths share, so a new credential spelling only has to be
// added here.
func stripProxyCredentialQuery(values url.Values) (bool, string) {
	changed := false
	token := ""
	for key := range values {
		if !isProxyCredentialQueryKey(key) {
			continue
		}
		if !changed && len(values[key]) > 0 {
			token = values[key][0]
		}
		changed = true
		values.Del(key)
	}
	return changed, token
}
