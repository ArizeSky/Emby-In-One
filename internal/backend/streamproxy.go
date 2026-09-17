package backend

import (
	"net/url"
	"strings"
)

// streamBusinessPath locates the item's stream root inside a resolved segment
// path: the first "/Videos/" or "/Audio/" boundary. Everything before it is the
// upstream's own deployment prefix (a reverse-proxy path such as "/emby") and
// must not survive into the client-facing path, because this proxy's routes are
// rooted at "/".
func streamBusinessPath(p string) (string, bool) {
	for _, root := range []string{"/Videos/", "/Audio/"} {
		if idx := strings.Index(p, root); idx >= 0 {
			return p[idx:], true
		}
	}
	return "", false
}

// RewriteM3U8ForItem rewrites an HLS manifest so every segment line routes back
// through this proxy: a proxy-relative path carrying the virtual item id and the
// client's own proxy token. The client resolves those paths against the proxy
// origin it fetched the manifest from, so no upstream host — and no localhost
// guess — is ever baked into what the client reads.
//
// upstreamBase is the prepared URL the manifest was fetched from; it is used
// only to resolve relative segment references and to find the upstream's path
// prefix. Its query (which carries the upstream token) never reaches the output:
// resolution takes the query from the segment line, and every rewritten line
// has its api_key replaced with the proxy token.
//
// A line whose path has no /Videos/{id} or /Audio/{id} shape cannot be routed
// by this proxy; it is passed through with upstream credentials stripped rather
// than rewritten into a URL that could not work.
func RewriteM3U8ForItem(content, upstreamBase, proxyItemID, proxyToken string) string {
	baseURL, baseErr := url.Parse(upstreamBase)
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
		if !resolved.IsAbs() && baseErr == nil {
			resolved = baseURL.ResolveReference(resolved)
		}
		businessPath, ok := streamBusinessPath(resolved.Path)
		if !ok {
			lines[i] = stripUpstreamCredentials(resolved)
			continue
		}
		segments := strings.Split(strings.TrimPrefix(businessPath, "/"), "/")
		// [Videos|Audio, itemId, rest...] — a path without the rest cannot name a
		// stream of this item, so it is left alone like any unroutable line.
		if len(segments) < 3 {
			lines[i] = stripUpstreamCredentials(resolved)
			continue
		}
		segments[1] = proxyItemID
		params, _ := url.ParseQuery(resolved.RawQuery)
		delete(params, "api_key")
		delete(params, "ApiKey")
		if proxyToken != "" {
			params.Set("api_key", proxyToken)
		}
		rewritten := "/" + strings.Join(segments, "/")
		if len(params) > 0 {
			rewritten += "?" + params.Encode()
		}
		lines[i] = rewritten
	}
	return strings.Join(lines, "\n")
}

// stripUpstreamCredentials removes api_key parameters from a manifest line this
// proxy cannot route. The upstream token must not reach the client even on a
// line that is passed through unchanged.
func stripUpstreamCredentials(resolved *url.URL) string {
	params, err := url.ParseQuery(resolved.RawQuery)
	if err != nil {
		return resolved.String()
	}
	if _, ok := params["api_key"]; !ok {
		if _, ok := params["ApiKey"]; !ok {
			return resolved.String()
		}
	}
	delete(params, "api_key")
	delete(params, "ApiKey")
	resolved.RawQuery = params.Encode()
	return resolved.String()
}
