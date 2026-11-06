package backend

import (
	"net/http"
	"net/url"
	"strings"
)

// Rewrite path segments independently: an Emby source such as mediasource_123
// contains the item ID, but is a different identifier. Replacing substrings in
// the whole URL first destroys that source ID (and can change query values).
func rewriteSubtitleDeliveryURL(raw, originalItem, virtualItem, originalSource, virtualSource, token string) string {
	u, err := url.Parse(raw)
	if err != nil || strings.ContainsAny(u.Path, "\\") {
		return ""
	}
	parts := strings.Split(u.Path, "/")
	root := -1
	for i, part := range parts {
		if part == "." || part == ".." {
			return ""
		}
		if root < 0 && (strings.EqualFold(part, "Videos") || strings.EqualFold(part, "Audio")) {
			root = i
		}
	}
	if root < 0 || len(parts) <= root+2 || parts[root+1] != originalItem {
		return ""
	}
	parts[root] = strings.ToUpper(parts[root][:1]) + strings.ToLower(parts[root][1:])
	parts[root+1] = virtualItem
	if parts[root+2] == originalSource && originalSource != "" {
		parts[root+2] = virtualSource
	} else if parts[root+2] == "mediasource_"+originalItem {
		parts[root+2] = "mediasource_" + virtualItem
	}
	u.Path = "/" + strings.Join(parts[root:], "/")
	u.RawPath = ""
	u.Scheme, u.Host, u.User, u.Fragment = "", "", nil, ""
	query := u.Query()
	stripProxyCredentialQuery(query)
	// Do not depend on a client echoing a PlaySessionId, or on a path source ID
	// being distinguishable from an aggregated item alias.
	if virtualSource != "" {
		setQueryValue(query, "MediaSourceId", virtualSource)
	}
	if token != "" {
		setQueryValue(query, "api_key", token)
	}
	u.RawQuery = query.Encode()
	return u.String()
}

// Some native clients construct mediasource_<virtual item> instead of using
// DeliveryUrl. That identifies the aggregate item, not a selected replica. With
// multiple allowed replicas, guessing the global primary can return a different
// release's subtitle at the same track index. Ask for fresh playback info instead.
func (a *App) requireSubtitleSource(w http.ResponseWriter, r *http.Request, resolved *routeResolution, rest string, query url.Values) bool {
	if !strings.Contains(strings.ToLower("/"+rest), "/subtitles/") {
		return true
	}
	if source := queryValue(query, "MediaSourceId"); source != "" {
		if a.IDStore.ResolveVirtualID(source) != nil {
			return true
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"kind": "subtitle-source-invalid", "message": "Subtitle media source is unknown; refresh playback info"})
		return false
	}
	if session := queryValue(query, "PlaySessionId"); session != "" && a.IDStore.ResolveVirtualID(session) != nil {
		return true
	}
	if len(a.collectAllowedInstances(requestContextFrom(r.Context()), resolved)) <= 1 {
		return true
	}
	writeJSON(w, http.StatusBadRequest, map[string]any{"kind": "subtitle-source-required", "message": "Subtitle source is ambiguous; refresh playback info and keep MediaSourceId or PlaySessionId"})
	return false
}
