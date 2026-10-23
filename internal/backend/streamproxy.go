package backend

import (
	"errors"
	"net/url"
	"strings"
)

var (
	errUnsupportedHLSURIAttribute = errors.New("unsupported HLS URI attribute")
	errMalformedHLSAttributes     = errors.New("malformed HLS attribute list")
)

type hlsURIAttribute struct {
	name       string
	value      string
	valueStart int
	valueEnd   int
}

var hlsURIAttributesByTag = map[string]map[string]bool{
	"EXT-X-KEY":                {"URI": true},
	"EXT-X-SESSION-KEY":        {"URI": true},
	"EXT-X-MAP":                {"URI": true},
	"EXT-X-MEDIA":              {"URI": true},
	"EXT-X-I-FRAME-STREAM-INF": {"URI": true},
	"EXT-X-IMAGE-STREAM-INF":   {"URI": true},
	"EXT-X-SESSION-DATA":       {"URI": true},
	"EXT-X-PART":               {"URI": true},
	"EXT-X-PRELOAD-HINT":       {"URI": true},
	"EXT-X-RENDITION-REPORT":   {"URI": true},
	"EXT-X-CONTENT-STEERING":   {"SERVER-URI": true},
}

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
	rewritten, err := RewriteM3U8ForItemStrict(content, upstreamBase, proxyItemID, proxyToken)
	if err != nil {
		return content
	}
	return rewritten
}

// RewriteM3U8ForItemStrict rewrites proxy-routable references and rejects
// unknown URI-bearing EXT-X tags instead of silently passing them through.
func RewriteM3U8ForItemStrict(content, upstreamBase, proxyItemID, proxyToken string) (string, error) {
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
		if len(segments) < 3 {
			return stripUpstreamCredentials(resolved), true
		}
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
		if strings.HasPrefix(trimmed, "#") {
			attrs, err := extractHLSURIAttributes(line)
			if err != nil {
				return "", err
			}
			values := make([]string, len(attrs))
			for j, attr := range attrs {
				values[j] = attr.value
				if rewritten, rewritable := rewrite(attr.value); rewritable {
					values[j] = rewritten
				}
			}
			lines[i] = replaceHLSURIAttributeValues(line, attrs, values)
			continue
		}
		if rewritten, ok := rewrite(trimmed); ok {
			lines[i] = rewritten
		}
	}
	return strings.Join(lines, "\n"), nil
}

// extractHLSURIAttributes parses fetchable URI attributes on supported HLS
// tags. The byte ranges address only each value's contents, excluding quotes.
func extractHLSURIAttributes(line string) ([]hlsURIAttribute, error) {
	trimmed := strings.TrimLeft(line, " \t")
	lineOffset := len(line) - len(trimmed)
	if !strings.HasPrefix(trimmed, "#EXT-X-") {
		return nil, nil
	}
	colon := strings.IndexByte(trimmed, ':')
	if colon < 0 {
		return nil, nil
	}
	tag := strings.ToUpper(strings.TrimSpace(trimmed[1:colon]))
	allowed, supported := hlsURIAttributesByTag[tag]
	attrs := make([]hlsURIAttribute, 0, 1)
	pos := lineOffset + colon + 1
	for pos < len(line) {
		segmentStart := pos
		quoted := false
		for pos < len(line) {
			switch line[pos] {
			case '"':
				quoted = !quoted
			case ',':
				if !quoted {
					goto segmentEnd
				}
			}
			pos++
		}
	segmentEnd:
		segment := line[segmentStart:pos]
		if strings.Count(segment, "\"")%2 != 0 {
			return nil, errMalformedHLSAttributes
		}
		if eq := strings.IndexByte(segment, '='); eq >= 0 {
			name := strings.ToUpper(strings.TrimSpace(segment[:eq]))
			isURI := name == "URI" || name == "SERVER-URI" || name == "URL" || strings.HasSuffix(name, "-URI") || strings.HasSuffix(name, "-URL")
			if isURI {
				if !supported || !allowed[name] {
					return nil, errUnsupportedHLSURIAttribute
				}
				valueStart := segmentStart + eq + 1
				for valueStart < pos && (line[valueStart] == ' ' || line[valueStart] == '\t') {
					valueStart++
				}
				if valueStart >= pos || line[valueStart] != '"' {
					return nil, errMalformedHLSAttributes
				}
				contentStart := valueStart + 1
				contentEnd := strings.IndexByte(line[contentStart:pos], '"')
				if contentEnd < 0 || strings.TrimSpace(line[contentStart+contentEnd+1:pos]) != "" {
					return nil, errMalformedHLSAttributes
				}
				attrs = append(attrs, hlsURIAttribute{
					name:       name,
					value:      line[contentStart : contentStart+contentEnd],
					valueStart: contentStart,
					valueEnd:   contentStart + contentEnd,
				})
			}
		}
		if pos == len(line) {
			break
		}
		pos++
	}
	return attrs, nil
}

func replaceHLSURIAttributeValues(line string, attrs []hlsURIAttribute, values []string) string {
	for i := len(attrs) - 1; i >= 0; i-- {
		line = line[:attrs[i].valueStart] + values[i] + line[attrs[i].valueEnd:]
	}
	return line
}

// extractQuotedURI returns the URI contents and its [start,end) byte range,
// excluding both quotes so callers preserve the surrounding HLS syntax.
func extractQuotedURI(line string) (string, int, int, bool) {
	attrs, err := extractHLSURIAttributes(line)
	if err != nil {
		return "", 0, 0, false
	}
	for _, attr := range attrs {
		if attr.name == "URI" {
			return attr.value, attr.valueStart, attr.valueEnd, true
		}
	}
	return "", 0, 0, false
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
