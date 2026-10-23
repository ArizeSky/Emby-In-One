package backend

import (
	"net/url"
	"strings"
)

// The original authorized route is stable across signed CDN redirects. Only
// proxy authentication parameters are excluded; unknown query parameters retain
// their meaning, including track, codec and rendition selection.
func streamManifestWindowKey(path string, query url.Values) string {
	query = cloneValues(query)
	for key := range query {
		switch strings.ToLower(key) {
		case "api_key", "apikey", "x-emby-token":
			delete(query, key)
		}
	}
	return "request:" + (&url.URL{Path: path, RawQuery: query.Encode()}).String()
}
