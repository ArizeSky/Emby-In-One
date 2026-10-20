package backend

import (
	"net/url"
	"strings"
)

// streamBusinessPath locates the item's stream root inside a resolved segment
// path: the first "/Videos/" or "/Audio/" boundary. Everything before it is the
// upstream's own deployment prefix (a reverse-proxy path such as "/emby") and
// must not survive into the client-facing path, because this proxy's routes are
// rooted at "/". The match is case-insensitive because Emby serves its
// transcoding manifests from a lowercase /videos/ root while direct-stream
// URLs use /Videos/; the returned slice keeps the original casing so only the
// item id segment is replaced downstream.
func streamBusinessPath(p string) (string, bool) {
	lower := strings.ToLower(p)
	for _, root := range []string{"/videos/", "/audio/"} {
		if idx := strings.Index(lower, root); idx >= 0 {
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
	rewrite := func(raw string) (string, bool) {
		resolved, err := url.Parse(raw)
		if err != nil {
			return "", false
		}
		if !resolved.IsAbs() && baseErr == nil {
			resolved = baseURL.ResolveReference(resolved)
		}
		businessPath, ok := streamBusinessPath(resolved.Path)
		if !ok {
			return stripUpstreamCredentials(resolved), true
		}
		segments := strings.Split(strings.TrimPrefix(businessPath, "/"), "/")
		// [Videos|Audio, itemId, rest...] — a path without the rest cannot name a
		// stream of this item, so it is left alone like any unroutable line.
		if len(segments) < 3 {
			return stripUpstreamCredentials(resolved), true
		}
		// The stream root is normalised to the capitalised form this proxy's
		// routes are registered under: an upstream that spells it /videos/
		// would otherwise hand the client a path only the fallback proxy sees.
		segments[0] = canonicalStreamRoot(segments[0])
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
		return rewritten, true
	}
	lines := strings.Split(content, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// #EXT-X-MAP:URI="0.mp4" names the fMP4 init segment. It is a comment
		// line, but its URI must be rewritten exactly like a segment line —
		// otherwise the player fetches it without the proxy token and the
		// upstream answers 401 before the first frame can ever decode.
		if strings.HasPrefix(trimmed, "#EXT-X-MAP:") {
			if uri, start, end, ok := extractQuotedURI(line); ok {
				if rewritten, rewritable := rewrite(uri); rewritable {
					lines[i] = line[:start] + rewritten + line[end:]
				}
			}
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if rewritten, ok := rewrite(trimmed); ok {
			lines[i] = rewritten
		}
	}
	return strings.Join(lines, "\n")
}

// extractQuotedURI finds the URI="..." attribute of an #EXT-X-MAP line and
// returns the quoted value plus its byte offsets in the original line, so the
// attribute can be replaced in place without touching the rest of the tag.
func extractQuotedURI(line string) (string, int, int, bool) {
	idx := strings.Index(line, `URI="`)
	if idx < 0 {
		return "", 0, 0, false
	}
	start := idx + len(`URI="`)
	end := strings.IndexByte(line[start:], '"')
	if end < 0 {
		return "", 0, 0, false
	}
	return line[start : start+end], start, start + end + 1, true
}

// canonicalStreamRoot capitalises a manifest's stream root segment ("videos"
// becomes "Videos") so the rewritten line matches this proxy's own routes.
func canonicalStreamRoot(root string) string {
	if root == "" {
		return root
	}
	lower := strings.ToLower(root)
	if lower == "videos" || lower == "audio" {
		return strings.ToUpper(root[:1]) + root[1:]
	}
	return root
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
