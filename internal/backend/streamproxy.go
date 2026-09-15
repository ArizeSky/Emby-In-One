package backend

import (
	"net/url"
	"strings"
)

func RewriteM3U8ForItem(content, upstreamBase, proxyItemID, proxyToken string) string {
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		resolved, err := url.Parse(trimmed)
		if err != nil {
			continue
		}
		if !resolved.IsAbs() {
			baseURL, err := url.Parse(upstreamBase)
			if err != nil {
				continue
			}
			resolved = baseURL.ResolveReference(resolved)
		}
		segments := strings.Split(strings.TrimPrefix(resolved.Path, "/"), "/")
		if len(segments) >= 2 && (segments[0] == "Videos" || segments[0] == "Audio") {
			segments[1] = proxyItemID
			resolved.Path = "/" + strings.Join(segments, "/")
		}
		params, _ := url.ParseQuery(resolved.RawQuery)
		delete(params, "api_key")
		delete(params, "ApiKey")
		if proxyToken != "" {
			params.Set("api_key", proxyToken)
		}
		resolved.RawQuery = params.Encode()
		lines[i] = resolved.String()
	}
	return strings.Join(lines, "\n")
}
