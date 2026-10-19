package backend

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// streamRoute describes one media stream endpoint: the upstream path prefix
// ("/Videos" or "/Audio") and the label used in log lines.
type streamRoute struct {
	pathPrefix string
	label      string
}

var (
	videoStreamRoute = streamRoute{pathPrefix: "/Videos", label: "Stream"}
	audioStreamRoute = streamRoute{pathPrefix: "/Audio", label: "Audio stream"}
)

func (a *App) handleVideoProxy(w http.ResponseWriter, r *http.Request) {
	a.proxyStream(w, r, videoStreamRoute)
}

// streamAllowedByRating reports whether the item behind resolved is within maxRating.
// On lookup failure it allows the request: the parental gate must not break
// playback for reasons unrelated to ratings.
func (a *App) streamAllowedByRating(r *http.Request, resolved *routeResolution, maxR int) bool {
	payload, err := resolved.Client.RequestJSON(r.Context(), requestContextFrom(r.Context()), a.Identity,
		http.MethodGet, "/Users/"+resolved.Client.clientUserID()+"/Items/"+resolved.OriginalID,
		url.Values{"Fields": []string{"OfficialRating"}}, nil)
	if err != nil {
		return true
	}
	m, ok := payload.(map[string]any)
	if !ok {
		return true
	}
	return itemAllowedByRating(m, maxR)
}

func (a *App) handleAudioProxy(w http.ResponseWriter, r *http.Request) {
	a.proxyStream(w, r, audioStreamRoute)
}

// proxyStream forwards a /Videos/{id}/... or /Audio/{id}/... request to the upstream
// that owns the item, keeping virtual IDs and the proxy token out of upstream URLs.
func (a *App) proxyStream(w http.ResponseWriter, r *http.Request, route streamRoute) {
	virtualItemID := r.PathValue("itemId")
	query := cloneValues(r.URL.Query())
	if a.Logger != nil {
		// The client's query may carry its own token; log only the safe shape.
		a.Logger.Debugf("%s request: itemId=%s, query=%s", route.label, virtualItemID, formatValuesForLog(query))
	}

	resolved := a.resolveRouteID(virtualItemID)
	if resolved == nil {
		if a.Logger != nil {
			a.Logger.Warnf("%s: itemId=%s not found in mappings", route.label, virtualItemID)
		}
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Item not found"})
		return
	}
	if !a.requireServerAccess(w, r, resolved) {
		return
	}
	// Parental gate: block direct stream requests for items above the user's ceiling.
	// Clients cannot see these items in lists, but a known id must not be playable.
	if maxR := a.maxRatingFor(r); maxR > 0 {
		if !a.streamAllowedByRating(r, resolved, maxR) {
			writeJSON(w, http.StatusForbidden, map[string]any{"message": "Content restricted by parental rating"})
			return
		}
	}

	rest := r.PathValue("rest")
	if rest == "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Stream not found"})
		return
	}
	// Resolve a virtual MediaSourceId embedded in subtitle/attachment paths:
	// /Videos/{itemId}/{mediaSourceId}/Subtitles/...  or  .../Attachments/...
	if slash := strings.IndexByte(rest, '/'); slash > 0 && query.Get("MediaSourceId") == "" {
		if tail := rest[slash:]; strings.HasPrefix(tail, "/Subtitles/") || strings.HasPrefix(tail, "/Attachments/") {
			if a.IDStore.ResolveVirtualID(rest[:slash]) != nil {
				query.Set("MediaSourceId", rest[:slash])
			}
		}
	}
	rest = resolveMediaSourceInPath(rest, a.IDStore)

	client, originalID, ok := a.resolveStreamTarget(w, r, resolved, virtualItemID, query)
	if !ok {
		return
	}
	a.resolvePlaySessionID(query)

	upstreamPath := route.pathPrefix + "/" + originalID + "/" + rest
	reqCtx := requestContextFrom(r.Context())
	if a.Logger != nil {
		a.Logger.Infof("%s: %s/%s/%s -> [%s] %s",
			route.label, route.pathPrefix, virtualItemID, rest, client.Name, upstreamPath)
	}

	// Redirect mode: hand the client a direct upstream stream URL. The URL is
	// prepared by the shared layer, so its identity and its single credential come
	// from one auth snapshot and no local token can leak into it.
	if a.streamPlaybackMode(client) == "redirect" {
		redirectURL, err := client.BuildURL(upstreamPath, query, true, reqCtx)
		if err != nil {
			if !writePreparationError(w, err) {
				writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Failed to prepare upstream stream URL"})
			}
			return
		}
		if a.Logger != nil {
			a.Logger.Debugf("%s redirect: %s/%s/%s -> 302 %s", route.label, route.pathPrefix, virtualItemID, rest, formatOutboundURLForLog(redirectURL))
		}
		http.Redirect(w, r, redirectURL, http.StatusFound)
		return
	}

	a.forwardStream(w, r, client, upstreamPath, query, rest, virtualItemID)
}

// forwardStream performs the upstream request and copies the response back,
// rewriting HLS manifests so their URLs keep pointing at this proxy.
func (a *App) forwardStream(w http.ResponseWriter, r *http.Request, client *UpstreamClient, upstreamPath string, query url.Values, rest, virtualItemID string) {
	reqCtx := requestContextFrom(r.Context())
	timelineSession := ""
	if session := query.Get("PlaySessionId"); session != "" {
		timelineSession = client.ID + "\x00" + virtualItemID + "\x00" + session
	}
	if a.Logger != nil {
		a.Logger.Debugf("Stream request headers: Range=%q, Accept=%q, AE=%q",
			r.Header.Get("Range"), r.Header.Get("Accept"), r.Header.Get("Accept-Encoding"))
	}

	reqHeaders := streamRequestHeaders(r)
	// A manifest is always read and rewritten, so never ask the upstream to compress
	// it: a gzip-compressed body would be rewritten as opaque garbage.
	if strings.HasSuffix(strings.ToLower(rest), ".m3u8") {
		reqHeaders.Set("Accept-Encoding", "identity")
	}
	resp, err := client.Stream(r.Context(), reqCtx, a.Identity, upstreamPath, query, reqHeaders)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return // client disconnected or timed out — not a server error
		}
		if a.Logger != nil {
			a.Logger.Errorf("Stream error: itemId=%s upstream=%s: %s", virtualItemID, upstreamPath, redactURLInError(err))
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{"message": err.Error()})
		return
	}
	defer resp.Body.Close()

	contentType := resp.Header.Get("Content-Type")
	// A playlist is only rewritten when the upstream actually served one. A non-2xx
	// answer (or an error page that happens to look like a manifest) is passed through
	// verbatim with its real status, so a 404/500 is not masked as a 200.
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && isPlaylistResponse(contentType, rest) &&
		resp.Header.Get("Content-Encoding") == "" {
		// A manifest read that fails mid-body must not be served as a truncated
		// 200 playlist: the player would seek into missing segments. Same rule
		// as the segment path below.
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxHLSManifestSize+1))
		if readErr != nil {
			if a.Logger != nil {
				a.Logger.Errorf("Stream manifest read failed: itemId=%s upstream=%s: %s",
					virtualItemID, upstreamPath, redactURLInError(readErr))
			}
			writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Failed to read HLS manifest"})
			return
		}
		if len(body) > maxHLSManifestSize {
			if a.Logger != nil {
				a.Logger.Errorf("Stream manifest too large: itemId=%s upstream=%s: %d bytes",
					virtualItemID, upstreamPath, len(body))
			}
			writeJSON(w, http.StatusBadGateway, map[string]any{"message": "HLS manifest exceeds size limit"})
			return
		}
		proxyToken := ""
		if reqCtx != nil {
			proxyToken = reqCtx.ProxyToken
		}
		// The manifest's own response URL is the prepared one already sent, so the
		// base URL needs no second authentication read. A test-constructed response
		// has no Request, and falls back to preparing the URL once more.
		baseURL := ""
		if resp.Request != nil && resp.Request.URL != nil {
			baseURL = resp.Request.URL.String()
		} else {
			prepared, err := client.BuildURL(upstreamPath, query, true, reqCtx)
			if err != nil {
				if !writePreparationError(w, err) {
					writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Failed to prepare upstream stream URL"})
				}
				return
			}
			baseURL = prepared
		}
		manifest := RewriteM3U8ForItem(string(body), baseURL, virtualItemID, proxyToken)
		// Remember where each media segment starts so proxied segments can have
		// their fMP4 timeline restored to the playlist position (see
		// rewriteHLSSegmentBody).
		storeHLSSegmentPositions(timelineSession, rest, manifest)
		w.Header().Set("Content-Type", "application/x-mpegURL")
		w.WriteHeader(resp.StatusCode)
		_, _ = io.WriteString(w, manifest)
		return
	}

	if a.Logger != nil {
		a.Logger.Infof("Stream upstream response: Status=%d, Type=%q, Len=%s, Encoding=%q, Range=%q",
			resp.StatusCode, contentType, resp.Header.Get("Content-Length"),
			resp.Header.Get("Content-Encoding"), resp.Header.Get("Content-Range"))
	}
	body, rewritten, readErr := rewriteHLSSegmentBody(r, resp, timelineSession, rest)
	if readErr != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Failed to read HLS segment"})
		return
	}
	copyStreamResponseHeaders(w, resp)
	if rewritten {
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
		return
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// resolveStreamTarget returns the client and the original item id to stream from.
// A MediaSourceId that lives on another upstream switches the target, subject to the
// same server permission and concurrency rules as the primary instance.
func (a *App) resolveStreamTarget(w http.ResponseWriter, r *http.Request, resolved *routeResolution, virtualItemID string, query url.Values) (*UpstreamClient, string, bool) {
	virtualMediaSourceID := query.Get("MediaSourceId")
	if virtualMediaSourceID == "" {
		return resolved.Client, resolved.OriginalID, true
	}
	mediaSource := a.IDStore.ResolveVirtualID(virtualMediaSourceID)
	if mediaSource == nil {
		if a.Logger != nil {
			a.Logger.Warnf("Stream: MediaSourceId %s cannot be resolved to any server", virtualMediaSourceID)
		}
		return resolved.Client, resolved.OriginalID, true
	}
	query.Set("MediaSourceId", mediaSource.OriginalID)
	if mediaSource.ServerID == resolved.ServerID {
		return resolved.Client, resolved.OriginalID, true
	}

	if !a.isServerAllowed(requestContextFrom(r.Context()), mediaSource.ServerID) {
		writeJSON(w, http.StatusForbidden, map[string]any{"message": "Access denied"})
		return nil, "", false
	}
	target := a.Upstream.ClientByID(mediaSource.ServerID)
	if target == nil || !target.IsOnline() {
		writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Target media source server is unavailable"})
		return nil, "", false
	}
	if a.Logger != nil {
		a.Logger.Infof("Stream: switching to server [%s] for MediaSourceId %s", target.Name, virtualMediaSourceID)
	}
	if !a.switchPlaybackSlot(w, r, mediaSource.ServerID, resolved.ServerID, virtualItemID) {
		return nil, "", false
	}
	// Prefer this server's own copy of the item when it has one.
	for _, other := range resolved.OtherInstances {
		if other.ServerID == mediaSource.ServerID {
			return target, other.OriginalID, true
		}
	}
	return target, resolved.OriginalID, true
}

// switchPlaybackSlot enforces the concurrency limit on the server that will serve
// the stream and releases the previously held slot. Writes 429 and returns false
// when the target server is already at its limit.
func (a *App) switchPlaybackSlot(w http.ResponseWriter, r *http.Request, targetID, previousID string, virtualItemID string) bool {
	reqCtx := requestContextFrom(r.Context())
	if a.PlaybackLimiter == nil || reqCtx == nil || reqCtx.ProxyUser == nil || reqCtx.ProxyUser.Role == "admin" {
		return true
	}
	cfg := a.ConfigStore.Snapshot()
	var maxConcurrent int
	found := false
	for _, u := range cfg.Upstream {
		if u.ID == targetID {
			maxConcurrent = u.MaxConcurrent
			found = true
			break
		}
	}
	if !found {
		return true
	}
	if !a.PlaybackLimiter.TryStart(reqCtx.ProxyUser.UserID, targetID, virtualItemID, maxConcurrent) {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"message": "已达到最大同时播放数限制"})
		return false
	}
	a.PlaybackLimiter.Stop(reqCtx.ProxyUser.UserID, previousID)
	return true
}

// streamPlaybackMode returns the effective playback mode for an upstream, falling
// back to the global setting.
func (a *App) streamPlaybackMode(client *UpstreamClient) string {
	if mode := client.Config.PlaybackMode; mode != "" {
		return mode
	}
	return a.ConfigStore.Snapshot().Playback.Mode
}

// resolvePlaySessionID rewrites a virtual PlaySessionId in place and reports the
// upstream server that owns it.
func (a *App) resolvePlaySessionID(query url.Values) (string, bool) {
	playSessionID := query.Get("PlaySessionId")
	if playSessionID == "" {
		return "", false
	}
	resolved := a.IDStore.ResolveVirtualID(playSessionID)
	if resolved == nil {
		return "", false
	}
	query.Set("PlaySessionId", resolved.OriginalID)
	return resolved.ServerID, true
}

// streamRequestHeaders forwards the client headers needed for seeking and
// partial content.
func streamRequestHeaders(r *http.Request) http.Header {
	headers := http.Header{}
	for _, name := range []string{"Range", "Accept", "Accept-Encoding", "Accept-Language"} {
		if value := r.Header.Get(name); value != "" {
			headers.Set(name, value)
		}
	}
	return headers
}

func isPlaylistResponse(contentType, rest string) bool {
	return strings.Contains(contentType, "mpegurl") || strings.HasSuffix(strings.ToLower(rest), ".m3u8")
}

// streamResponseHeaders are the upstream headers worth passing through. Chunked
// transfer encoding is deliberately absent: the proxy re-frames every response
// with its own framing (io.Copy to an http.ResponseWriter), and copying the
// upstream's Transfer-Encoding verbatim would declare framing the body does not
// follow.
var streamResponseHeaders = []string{
	"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges",
	"Cache-Control", "ETag", "Last-Modified",
	"Content-Disposition", "Content-Encoding", "Date", "Server",
}

func copyStreamResponseHeaders(w http.ResponseWriter, resp *http.Response) {
	for _, name := range streamResponseHeaders {
		value := resp.Header.Get(name)
		if value == "" {
			continue
		}
		w.Header().Set(name, value)
	}
}

func (a *App) handleDeleteActiveEncodings(w http.ResponseWriter, r *http.Request) {
	query := cloneValues(r.URL.Query())
	serverID, found := a.resolvePlaySessionID(query)
	if !found {
		for _, client := range a.allowedClients(requestContextFrom(r.Context())) {
			_ = a.forwardNoContent(r, client, http.MethodDelete, "/Videos/ActiveEncodings", query, nil)
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !a.isServerAllowed(requestContextFrom(r.Context()), serverID) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if client := a.Upstream.ClientByID(serverID); client != nil && client.IsOnline() {
		_ = a.forwardNoContent(r, client, http.MethodDelete, "/Videos/ActiveEncodings", query, nil)
	}
	w.WriteHeader(http.StatusNoContent)
}
