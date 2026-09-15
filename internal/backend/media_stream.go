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

func (a *App) handleAudioProxy(w http.ResponseWriter, r *http.Request) {
	a.proxyStream(w, r, audioStreamRoute)
}

// proxyStream forwards a /Videos/{id}/... or /Audio/{id}/... request to the upstream
// that owns the item, keeping virtual IDs and the proxy token out of upstream URLs.
func (a *App) proxyStream(w http.ResponseWriter, r *http.Request, route streamRoute) {
	virtualItemID := r.PathValue("itemId")
	query := cloneValues(r.URL.Query())
	if a.Logger != nil {
		a.Logger.Debugf("%s request: itemId=%s, query=%s", route.label, virtualItemID, query.Encode())
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

	rest := r.PathValue("rest")
	if rest == "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Stream not found"})
		return
	}
	// Resolve a virtual MediaSourceId embedded in subtitle/attachment paths:
	// /Videos/{itemId}/{mediaSourceId}/Subtitles/...  or  .../Attachments/...
	rest = resolveMediaSourceInPath(rest, a.IDStore)

	client, originalID, ok := a.resolveStreamTarget(w, r, resolved, virtualItemID, query)
	if !ok {
		return
	}
	a.resolvePlaySessionID(query)

	// Replace the proxy token with the upstream's access token for stream auth.
	query.Del("api_key")
	query.Del("ApiKey")
	token := client.getAccessToken()
	if token != "" {
		query.Set("api_key", token)
	}

	upstreamPath := route.pathPrefix + "/" + originalID + "/" + rest
	if a.Logger != nil {
		a.Logger.Infof("%s: %s/%s/%s → [%s] %s (using token: %v)",
			route.label, route.pathPrefix, virtualItemID, rest, client.Name, upstreamPath, token != "")
	}

	// Redirect mode: hand the client a direct upstream stream URL.
	if a.streamPlaybackMode(client) == "redirect" {
		redirectURL := client.BuildURL(upstreamPath, query, true)
		if a.Logger != nil {
			a.Logger.Debugf("%s redirect: %s/%s/%s → 302 %s", route.label, route.pathPrefix, virtualItemID, rest, redirectURL)
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
	if a.Logger != nil {
		a.Logger.Debugf("Stream request headers: Range=%q, Accept=%q, AE=%q",
			r.Header.Get("Range"), r.Header.Get("Accept"), r.Header.Get("Accept-Encoding"))
	}

	resp, err := client.Stream(r.Context(), reqCtx, a.Identity, upstreamPath, query, streamRequestHeaders(r))
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return // client disconnected or timed out — not a server error
		}
		if a.Logger != nil {
			a.Logger.Errorf("Stream error: itemId=%s upstream=%s: %s", virtualItemID, upstreamPath, err.Error())
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{"message": err.Error()})
		return
	}
	defer resp.Body.Close()

	contentType := resp.Header.Get("Content-Type")
	if isPlaylistResponse(contentType, rest) {
		body, _ := io.ReadAll(resp.Body)
		proxyToken := ""
		if reqCtx != nil {
			proxyToken = reqCtx.ProxyToken
		}
		manifest := RewriteM3U8ForItem(string(body), client.BuildURL(upstreamPath, query, true), virtualItemID, proxyToken)
		w.Header().Set("Content-Type", "application/x-mpegURL")
		_, _ = io.WriteString(w, manifest)
		return
	}

	if a.Logger != nil {
		a.Logger.Infof("Stream upstream response: Status=%d, Type=%q, Len=%s, Encoding=%q, Range=%q",
			resp.StatusCode, contentType, resp.Header.Get("Content-Length"),
			resp.Header.Get("Content-Encoding"), resp.Header.Get("Content-Range"))
	}
	copyStreamResponseHeaders(w, resp)
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
	if mediaSource.ServerIndex == resolved.ServerIndex {
		return resolved.Client, resolved.OriginalID, true
	}

	if !a.isServerAllowed(requestContextFrom(r.Context()), mediaSource.ServerIndex) {
		writeJSON(w, http.StatusForbidden, map[string]any{"message": "Access denied"})
		return nil, "", false
	}
	target := a.Upstream.GetClient(mediaSource.ServerIndex)
	if target == nil || !target.IsOnline() {
		writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Target media source server is unavailable"})
		return nil, "", false
	}
	if a.Logger != nil {
		a.Logger.Infof("Stream: switching to server [%s] for MediaSourceId %s", target.Name, virtualMediaSourceID)
	}
	if !a.switchPlaybackSlot(w, r, mediaSource.ServerIndex, resolved.ServerIndex, virtualItemID) {
		return nil, "", false
	}
	// Prefer this server's own copy of the item when it has one.
	for _, other := range resolved.OtherInstances {
		if other.ServerIndex == mediaSource.ServerIndex {
			return target, other.OriginalID, true
		}
	}
	return target, resolved.OriginalID, true
}

// switchPlaybackSlot enforces the concurrency limit on the server that will serve
// the stream and releases the previously held slot. Writes 429 and returns false
// when the target server is already at its limit.
func (a *App) switchPlaybackSlot(w http.ResponseWriter, r *http.Request, targetIndex, previousIndex int, virtualItemID string) bool {
	reqCtx := requestContextFrom(r.Context())
	if a.PlaybackLimiter == nil || reqCtx == nil || reqCtx.ProxyUser == nil || reqCtx.ProxyUser.Role == "admin" {
		return true
	}
	cfg := a.ConfigStore.Snapshot()
	if targetIndex < 0 || targetIndex >= len(cfg.Upstream) {
		return true
	}
	if !a.PlaybackLimiter.TryStart(reqCtx.ProxyUser.UserID, targetIndex, virtualItemID, cfg.Upstream[targetIndex].MaxConcurrent) {
		writeJSON(w, http.StatusTooManyRequests, map[string]any{"message": "已达到最大同时播放数限制"})
		return false
	}
	a.PlaybackLimiter.Stop(reqCtx.ProxyUser.UserID, previousIndex)
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
func (a *App) resolvePlaySessionID(query url.Values) (int, bool) {
	playSessionID := query.Get("PlaySessionId")
	if playSessionID == "" {
		return 0, false
	}
	resolved := a.IDStore.ResolveVirtualID(playSessionID)
	if resolved == nil {
		return 0, false
	}
	query.Set("PlaySessionId", resolved.OriginalID)
	return resolved.ServerIndex, true
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
// transfer encoding is skipped so the proxy sets its own framing.
var streamResponseHeaders = []string{
	"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges",
	"Cache-Control", "ETag", "Last-Modified", "Transfer-Encoding",
	"Content-Disposition", "Content-Encoding", "Date", "Server",
}

func copyStreamResponseHeaders(w http.ResponseWriter, resp *http.Response) {
	for _, name := range streamResponseHeaders {
		value := resp.Header.Get(name)
		if value == "" {
			continue
		}
		if strings.EqualFold(name, "Transfer-Encoding") && strings.Contains(strings.ToLower(value), "chunked") {
			continue
		}
		w.Header().Set(name, value)
	}
}

func (a *App) handleDeleteActiveEncodings(w http.ResponseWriter, r *http.Request) {
	query := cloneValues(r.URL.Query())
	serverIndex, found := a.resolvePlaySessionID(query)
	if !found {
		for _, client := range a.allowedClients(requestContextFrom(r.Context())) {
			_ = a.forwardNoContent(r, client, http.MethodDelete, "/Videos/ActiveEncodings", query, nil)
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !a.isServerAllowed(requestContextFrom(r.Context()), serverIndex) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if client := a.Upstream.GetClient(serverIndex); client != nil && client.IsOnline() {
		_ = a.forwardNoContent(r, client, http.MethodDelete, "/Videos/ActiveEncodings", query, nil)
	}
	w.WriteHeader(http.StatusNoContent)
}
