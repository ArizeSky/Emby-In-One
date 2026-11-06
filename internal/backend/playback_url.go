package backend

import (
	"net/http"
	"net/url"
	"path"
	"strings"
)

// This file implements the W02 rewrite of PlaybackInfo DirectStreamUrl values.
//
// The old rewrite threw the upstream URL away and rebuilt a synthetic
// /Videos/{virtual}/stream.{ext} path from its file extension, so a master.m3u8
// direct stream became stream.m3u8 and every required query parameter
// (LiveStreamId, DeviceId, PlaySessionId, ...) was lost.
//
// The rewrite below keeps the *semantics* of the upstream URL by registering the
// validated upstream URL as an opaque resource handle in StreamResources and
// handing the client only this proxy's own route. Nothing about the upstream
// host, path or credentials reaches the client, and the handle is re-authorized
// on every request exactly like the HLS resource handles (see
// serveStreamResource). The client-visible URL keeps a short extension hint so
// clients that sniff the container (and the web player's HLS check) still see
// .m3u8/.mp4 instead of an extension-less opaque id.
//
// A URL that cannot be mapped safely (foreign host, another item, an unexpected
// deployment prefix, dot segments, encoded separators or an opaque form) is not
// handed to the client and no synthetic /stream.<ext> route is fabricated. The
// MediaSource is instead marked with playbackDirectStreamUnavailableKey so a
// client cannot silently fall back to the old extension-built route; the web
// player treats that marker as terminal. The marker is removed again whenever a
// URL is successfully mapped, so an upstream-supplied key can never spoof it.

// playbackDirectStreamUnavailableKey is the explicit fail-closed marker added to
// a MediaSource whose upstream DirectStreamUrl was rejected. It is intentionally
// not an upstream field: the web app uses it to refuse the legacy fallback.
const playbackDirectStreamUnavailableKey = "EioDirectStreamUnavailable"

// playbackDirectStream is one MediaSource's planned DirectStreamUrl rewrite. It
// is collected while the PlaybackInfo response is assembled and applied after
// the whole candidate set is known, so registration is atomic.
type playbackDirectStream struct {
	mediaSource map[string]any
	serverID    string
	originalID  string
	// resourceURL is the validated upstream URL to register server-side. It is
	// empty when the upstream URL cannot be mapped; the MediaSource then loses
	// its DirectStreamUrl and is marked unavailable instead of being rebuilt.
	resourceURL string
	// provenance binds resourceURL to the validated PlaybackInfo answer and the
	// auth snapshot that was current when it was registered.
	provenance *resourceQueryProvenance
	// container is the client-visible extension hint.
	container string
	// routeRoot is the canonical upstream stream root ("Videos" or "Audio") the
	// validated URL was served from, so the local route matches its shape.
	routeRoot string
}

// planPlaybackDirectStream validates an upstream DirectStreamUrl and prepares its
// local rewrite. client is the upstream instance the MediaSource came from.
func planPlaybackDirectStream(mediaSource map[string]any, directURL string, client *UpstreamClient, originalItemID string) playbackDirectStream {
	plan := playbackDirectStream{
		mediaSource: mediaSource,
		originalID:  originalItemID,
		container:   directStreamContainer(directURL, mediaSource),
		routeRoot:   "Videos",
	}
	if client != nil {
		plan.serverID = client.ID
	}
	resourceURL, root, ok := resolveDirectStreamResourceURL(directURL, client, originalItemID)
	if !ok {
		return plan
	}
	plan.resourceURL = resourceURL
	plan.routeRoot = root
	authToken := ""
	if client != nil {
		authToken = client.authSnapshot().AccessToken
	}
	plan.provenance = newResourceQueryProvenance(resourceQueryProvenancePlaybackInfo, resourceURL, authToken)
	return plan
}

// directStreamContainer derives the client-visible extension hint for a direct
// stream URL, falling back to the MediaSource's declared Container and finally to
// mp4, which is the previous behavior.
func directStreamContainer(directURL string, mediaSource map[string]any) string {
	cleanURL := directURL
	if qIdx := strings.IndexByte(cleanURL, '?'); qIdx >= 0 {
		cleanURL = cleanURL[:qIdx]
	}
	container := strings.TrimPrefix(path.Ext(cleanURL), ".")
	if container == "" {
		if rawContainer, ok := mediaSource["Container"].(string); ok {
			container = strings.Split(rawContainer, ",")[0]
		}
	}
	container = sanitizeStreamExtension(container)
	if container == "" {
		container = "mp4"
	}
	return container
}

// sanitizeStreamExtension keeps a client-visible extension hint to a short
// lowercase token, so a hostile Container value cannot inject path or query
// syntax into the local URL.
func sanitizeStreamExtension(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || len(value) > 8 {
		return ""
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return ""
		}
	}
	return value
}

// resolveDirectStreamResourceURL turns an upstream DirectStreamUrl into an
// absolute upstream URL that this proxy may fetch, and reports the stream root it
// belongs to. It accepts:
//   - a relative URL (the usual Emby form) resolved against the client's live
//     stream base, with the base's own deployment prefix removed first, and
//   - an absolute http(s) URL only when it points at one of the client's own
//     configured origins, so an upstream answer can never turn this proxy into an
//     arbitrary-URL fetcher.
//
// The path must stay inside the item's own /Videos|/Audio/{originalID}/ stream
// route: the resource handle is re-authorized against that item and its rating,
// so a URL that addresses a different item must not be bound to this one.
func resolveDirectStreamResourceURL(raw string, client *UpstreamClient, originalItemID string) (string, string, bool) {
	if client == nil || originalItemID == "" {
		return "", "", false
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Opaque != "" {
		return "", "", false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		if parsed.Host == "" || parsed.User != nil || !client.isSelfURL(raw) {
			return "", "", false
		}
		return validatedDirectStreamTarget(parsed, originalItemID, client)
	case "":
		if parsed.Host != "" || parsed.User != nil {
			// Protocol-relative ("//host/path") and userinfo forms are not
			// relative to the stream base this proxy controls.
			return "", "", false
		}
		resolved, ok := resolveRelativeDirectStreamURL(parsed, client)
		if !ok {
			return "", "", false
		}
		return validatedDirectStreamTarget(resolved, originalItemID, client)
	default:
		return "", "", false
	}
}

// resolveRelativeDirectStreamURL joins an upstream-relative DirectStreamUrl onto
// the client's stream base with the same concatenation rule the proxy uses for
// forwarded stream requests, so the resulting URL addresses the same upstream
// endpoint.
func resolveRelativeDirectStreamURL(parsed *url.URL, client *UpstreamClient) (*url.URL, bool) {
	base := client.StreamBaseURL
	if candidates := client.streamBaseCandidates(); len(candidates) > 0 {
		base = candidates[0]
	}
	baseURL, err := url.Parse(base)
	if err != nil || !baseURL.IsAbs() || baseURL.Host == "" {
		return nil, false
	}
	escaped := parsed.EscapedPath()
	if escaped == "" {
		return nil, false
	}
	if !strings.HasPrefix(escaped, "/") {
		escaped = "/" + escaped
	}
	// An upstream that already answers with its deployment prefix must not get it
	// twice: strip it, because the stream base already carries it.
	if prefix := strings.TrimRight(baseURL.Path, "/"); prefix != "" &&
		strings.HasPrefix(strings.ToLower(escaped), strings.ToLower(prefix)+"/") {
		escaped = escaped[len(prefix):]
	}
	resolved, err := url.Parse(strings.TrimRight(base, "/") + escaped)
	if err != nil || resolved.Host == "" {
		return nil, false
	}
	resolved.RawQuery = parsed.RawQuery
	resolved.Fragment = ""
	return resolved, true
}

// validatedDirectStreamTarget checks that an absolute upstream URL stays inside
// the item's own stream route and returns it with the canonical stream root. A
// configured deployment prefix (the path part of the API or stream base) is
// stripped segment-wise; a hardcoded prefix would accept the wrong deployment
// and reject every other one. The bare /Videos|/Audio route is also accepted,
// because a relative URL is joined onto the stream base and may already carry
// its prefix or not depending on how the upstream answered.
func validatedDirectStreamTarget(target *url.URL, originalItemID string, client *UpstreamClient) (string, string, bool) {
	if target == nil || originalItemID == "" {
		return "", "", false
	}
	escaped := target.EscapedPath()
	lower := strings.ToLower(escaped)
	// Encoded separators and dot segments would change the meaning of the path
	// once an upstream normalizes it; refuse them instead of guessing.
	for _, marker := range []string{"%2f", "%5c", "%2e", "\\"} {
		if strings.Contains(lower, marker) {
			return "", "", false
		}
	}
	segments := strings.Split(escaped, "/")
	if len(segments) < 4 || segments[0] != "" {
		return "", "", false
	}
	for _, segment := range segments[1:] {
		if segment == "" || segment == "." || segment == ".." {
			return "", "", false
		}
	}
	segments = stripDirectStreamBasePath(segments, client)
	if len(segments) < 4 || segments[0] != "" {
		return "", "", false
	}
	root := strings.ToLower(segments[1])
	if root != "videos" && root != "audio" {
		return "", "", false
	}
	itemID, err := url.PathUnescape(segments[2])
	if err != nil || itemID != originalItemID {
		return "", "", false
	}
	for _, segment := range segments[3:] {
		if segment == "" || segment == "." || segment == ".." {
			return "", "", false
		}
	}
	canonicalRoot := strings.ToUpper(root[:1]) + root[1:]
	target.Fragment = ""
	return target.String(), canonicalRoot, true
}

// stripDirectStreamBasePath removes the longest configured base-path prefix, so
// a deployment mounted under /emby (or any administrator-chosen prefix) maps to
// the same item route as one mounted at the origin root. Matching is
// segment-wise and case-insensitive for compatibility with Emby's mixed-case
// routes; the longest match wins when one base is a prefix of another.
func stripDirectStreamBasePath(segments []string, client *UpstreamClient) []string {
	if client == nil {
		return segments
	}
	best := 0
	for _, prefix := range directStreamBasePathPrefixes(client) {
		if len(prefix) == 0 || len(prefix) >= len(segments) || len(prefix) <= best {
			continue
		}
		matched := true
		for i, part := range prefix {
			if !strings.EqualFold(segments[i+1], part) {
				matched = false
				break
			}
		}
		if matched {
			best = len(prefix)
		}
	}
	if best == 0 {
		return segments
	}
	stripped := make([]string, 0, len(segments)-best)
	stripped = append(stripped, segments[0])
	stripped = append(stripped, segments[best+1:]...)
	return stripped
}

// directStreamBasePathPrefixes returns the escaped path segments of every
// configured API and stream base. The live stream-base list is used as well as
// the full configured list, so a temporarily marked-dead line still validates
// the same deployment prefix.
func directStreamBasePathPrefixes(client *UpstreamClient) [][]string {
	bases := make([]string, 0, len(client.StreamBaseURLs)+2)
	bases = append(bases, client.BaseURL, client.StreamBaseURL)
	bases = append(bases, client.StreamBaseURLs...)
	seen := map[string]bool{}
	prefixes := make([][]string, 0, len(bases))
	for _, base := range bases {
		if base == "" {
			continue
		}
		parsed, err := url.Parse(base)
		if err != nil {
			continue
		}
		escaped := strings.TrimRight(parsed.EscapedPath(), "/")
		if escaped == "" {
			continue
		}
		parts := strings.Split(strings.TrimPrefix(escaped, "/"), "/")
		if len(parts) == 0 {
			continue
		}
		valid := true
		for _, part := range parts {
			if part == "" || part == "." || part == ".." {
				valid = false
				break
			}
		}
		if !valid {
			continue
		}
		key := strings.ToLower(strings.Join(parts, "/"))
		if seen[key] {
			continue
		}
		seen[key] = true
		prefixes = append(prefixes, parts)
	}
	return prefixes
}

// directStreamTimelineSession preserves the same server/item/upstream-session
// scope as forwardStream. An upstream-issued URL is authoritative; a request's
// virtual session is translated only for its own serving server.
func (a *App) directStreamTimelineSession(plan playbackDirectStream, virtualItemID, requestSession string) string {
	if attempt := playbackStringField(plan.mediaSource, "EioAttemptId"); attempt != "" {
		return plan.serverID + "\x00" + virtualItemID + "\x00" + attempt
	}
	var session string
	if target, err := url.Parse(plan.resourceURL); err == nil {
		session = queryValue(target.Query(), "PlaySessionId")
	}
	if session == "" {
		session = requestSession
		if session != "" && a.IDStore != nil {
			if resolved := a.IDStore.ResolveVirtualID(session); resolved != nil {
				if resolved.ServerID != plan.serverID {
					return ""
				}
				session = resolved.OriginalID
			}
		}
	}
	if session == "" || plan.serverID == "" || virtualItemID == "" {
		return ""
	}
	return plan.serverID + "\x00" + virtualItemID + "\x00" + session
}

// applyPlaybackDirectStreams registers every mappable resource atomically and
// writes the client-visible DirectStreamUrl of each planned source. A full
// resource store fails closed with 503 and an unmappable upstream URL is
// dropped: the old extension-only rebuild is deliberately not used, because a
// fabricated /stream.<ext> route can select a different upstream endpoint than
// the one the upstream actually issued.
func (a *App) applyPlaybackDirectStreams(w http.ResponseWriter, virtualItemID, proxyToken, timelineSession, userID string, plans []playbackDirectStream) bool {
	if userID == "" {
		for _, plan := range plans {
			delete(plan.mediaSource, "DirectStreamUrl")
			plan.mediaSource[playbackDirectStreamUnavailableKey] = true
		}
		return true
	}
	for _, plan := range plans {
		// Never trust an upstream-supplied marker: only this function decides
		// whether the URL was rejected.
		delete(plan.mediaSource, playbackDirectStreamUnavailableKey)
	}
	entries := make([]streamResourceEntry, 0, len(plans))
	order := make([]int, 0, len(plans))
	for i, plan := range plans {
		if plan.resourceURL == "" {
			continue
		}
		entries = append(entries, streamResourceEntry{
			UserID:          userID,
			VirtualItemID:   virtualItemID,
			ServerID:        plan.serverID,
			OriginalID:      plan.originalID,
			ResourceURL:     plan.resourceURL,
			Provenance:      plan.provenance.withKnownPrivate(proxyToken),
			AttemptID:       playbackStringField(plan.mediaSource, "EioAttemptId"),
			TimelineSession: a.directStreamTimelineSession(plan, virtualItemID, timelineSession),
		})
		order = append(order, i)
	}
	handles := map[int]string{}
	if len(entries) > 0 {
		ids, ok := a.StreamResources.registerAll(entries)
		if !ok {
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Retry-After", "5")
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "Stream resource capacity exhausted; retry later"})
			return false
		}
		for n, id := range ids {
			handles[order[n]] = id
		}
	}
	for i, plan := range plans {
		if id, ok := handles[i]; ok {
			plan.mediaSource["DirectStreamUrl"] = streamResourceClientURL(plan.routeRoot, virtualItemID, id, plan.container, proxyToken)
			continue
		}
		delete(plan.mediaSource, "DirectStreamUrl")
		plan.mediaSource[playbackDirectStreamUnavailableKey] = true
	}
	return true
}

// streamResourceClientURL is the local route for a registered direct-stream
// resource. The extension suffix is a client hint only; the handler strips it
// before looking the handle up.
func streamResourceClientURL(root, virtualItemID, handleID, container, proxyToken string) string {
	if root == "" {
		root = "Videos"
	}
	local := "/" + root + "/" + virtualItemID + "/eio-resource/" + handleID
	if ext := sanitizeStreamExtension(container); ext != "" {
		local += "." + ext
	}
	if proxyToken != "" {
		local += "?api_key=" + url.QueryEscape(proxyToken)
	}
	return local
}
