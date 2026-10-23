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

// maxStreamRedirectHops bounds the per-hop redirect walk for directRedirect: each
// hop is re-evaluated against the self-host rules, and a chain that never reaches
// an external target or real bytes is cut off as a bad gateway.
const maxStreamRedirectHops = 5

func (a *App) handleVideoProxy(w http.ResponseWriter, r *http.Request) {
	a.proxyStream(w, r, videoStreamRoute)
}

// itemRatingAllowedOnServer reports whether the item is within the user's
// parental policy on the given server. The second result is "confirmed": false
// when the rating (or an inherited parent rating) could not be read, so callers
// deny with 502 instead of allowing playback they could not verify (plan §8.6).
// It delegates to the inheritance-aware access check in parental_access.go.
func (a *App) itemRatingAllowedOnServer(r *http.Request, client *UpstreamClient, originalID string) (bool, bool) {
	return a.checkInstanceParentalAccess(r, client, originalID)
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

	rest := r.PathValue("rest")
	if rest == "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Stream not found"})
		return
	}
	// A rewritten external HLS resource for a restricted user arrives as
	// /Videos/<virtualItemID>/eio-resource/<opaque>. It is served from the
	// server-side opaque map, never appended to an upstream path (plan §8.6.1).
	if strings.HasPrefix(rest, "eio-resource/") {
		a.serveStreamResource(w, r, virtualItemID, strings.TrimPrefix(rest, "eio-resource/"))
		return
	}
	// Resolve a virtual MediaSourceId embedded in subtitle/attachment paths:
	// /Videos/{itemId}/{mediaSourceId}/Subtitles/...  or  .../Attachments/...
	// This only records a source-selection hint. The actual path rewrite is
	// deferred until the real target server and its original item id are known,
	// so a cross-source MediaSourceId is rewritten for the server that serves it.
	if slash := strings.IndexByte(rest, '/'); slash > 0 && query.Get("MediaSourceId") == "" {
		if tail := rest[slash:]; strings.HasPrefix(tail, "/Subtitles/") || strings.HasPrefix(tail, "/Attachments/") {
			if a.IDStore.ResolveVirtualID(rest[:slash]) != nil {
				query.Set("MediaSourceId", rest[:slash])
			}
		}
	}

	client, originalID, ok := a.resolveStreamTarget(w, r, resolved, virtualItemID, query)
	if !ok {
		return
	}
	// Parental gate on the ACTUAL source that will serve the bytes, before any
	// playback-slot change (plan §8.6): a restricted user may not play a source
	// above their ceiling even when the primary copy is allowed, and an
	// unconfirmable rating must not fall through to playback.
	if maxR, blockUnrated := a.parentalPolicyFor(r); maxR > 0 || blockUnrated {
		allowed, confirmed := a.itemRatingAllowedOnServer(r, client, originalID)
		if !confirmed {
			writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Cannot verify content rating"})
			return
		}
		if !allowed {
			writeJSON(w, http.StatusForbidden, map[string]any{"message": "Content restricted by parental rating"})
			return
		}
	}
	// Authorization passed: commit the playback-slot switch to the target server.
	if client.ID != resolved.ServerID {
		if !a.switchPlaybackSlot(w, r, client.ID, resolved.ServerID, virtualItemID) {
			return
		}
	}
	rest = resolveMediaSourceInPathForServer(rest, a.IDStore, client.ID, originalID)
	a.resolvePlaySessionID(query)

	upstreamPath := route.pathPrefix + "/" + originalID + "/" + rest
	reqCtx := requestContextFrom(r.Context())
	if a.Logger != nil {
		a.Logger.Infof("%s: %s/%s/%s -> [%s] %s",
			route.label, route.pathPrefix, virtualItemID, rest, client.Name, upstreamPath)
	}

	// Redirect mode: hand the client a direct upstream stream URL. A restricted
	// user is always forced through the proxy (plan §8.6): the client must never
	// receive an upstream Location, so redirect mode is bypassed for them.
	redirectMax, redirectBlock := a.parentalPolicyFor(r)
	restrictedPlayback := redirectMax > 0 || redirectBlock
	if a.streamPlaybackMode(client) == "redirect" && !restrictedPlayback {
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

	a.forwardStream(w, r, client, upstreamPath, query, rest, virtualItemID, originalID)
}

// forwardStream performs the upstream request and copies the response back,
// rewriting HLS manifests so their URLs keep pointing at this proxy.
func (a *App) forwardStream(w http.ResponseWriter, r *http.Request, client *UpstreamClient, upstreamPath string, query url.Values, rest, virtualItemID, originalID string) {
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
	defer func() {
		if resp != nil {
			resp.Body.Close()
		}
	}()

	// Redirect handling (plan §7.2 matrix). A restricted user is always proxied:
	// the client never receives an upstream Location, so an external CDN link can
	// only be relayed here. For an unrestricted user the configured switches
	// decide whether a safe external redirect is handed to the player.
	maxR, blockUnrated := a.parentalPolicyFor(r)
	restricted := maxR > 0 || blockUnrated
	directEffective := client.Config.DirectRedirect && !restricted
	followEffective := client.Config.FollowRedirects
	if isRedirectStatus(resp.StatusCode) {
		if directEffective {
			// Per-hop: an internal hop is fetched here, a safe external hop is
			// handed to the player — but only when following is enabled. With
			// followRedirects off, needing an internal follow is an explicit 502.
			handled := false
			for hop := 0; hop < maxStreamRedirectHops && resp != nil && isRedirectStatus(resp.StatusCode); hop++ {
				target, follow := client.resolveStreamRedirectTarget(resp, r.Host)
				if !follow {
					if a.Logger != nil {
						a.Logger.Infof("Stream redirect: itemId=%s -> %d %s (direct to client, hop %d)", virtualItemID, resp.StatusCode, formatOutboundURLForLog(target), hop)
					}
					copyStreamResponseHeaders(w, resp)
					w.Header().Set("Location", target)
					w.WriteHeader(resp.StatusCode)
					handled = true
					break
				}
				if !followEffective {
					writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Upstream redirect requires internal follow but followRedirects is disabled"})
					return
				}
				if target == "" {
					writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Upstream redirect without a target"})
					return
				}
				followed, ferr := client.fetchRedirectTarget(r.Context(), target, streamRequestHeaders(r))
				if ferr != nil {
					if errors.Is(ferr, context.Canceled) || errors.Is(ferr, context.DeadlineExceeded) {
						return
					}
					if a.Logger != nil {
						a.Logger.Errorf("Stream redirect follow failed: itemId=%s: %s", virtualItemID, redactURLInError(ferr))
					}
					writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Failed to follow upstream redirect"})
					return
				}
				if resp != nil {
					resp.Body.Close()
				}
				resp = followed
			}
			if handled {
				return
			}
			if resp != nil && isRedirectStatus(resp.StatusCode) {
				writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Too many upstream redirects"})
				return
			}
		} else {
			// Proxy-only path: the user is restricted, or directRedirect is off and
			// net/http already stopped at the redirect because following is off.
			// The proxy must fetch the target itself; if following is disabled, a
			// redirect that needs following is a 502.
			if !followEffective {
				writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Upstream redirect not followed (followRedirects is off)"})
				return
			}
			for hop := 0; hop < maxStreamRedirectHops && resp != nil && isRedirectStatus(resp.StatusCode); hop++ {
				target, _ := client.resolveStreamRedirectTarget(resp, r.Host)
				if target == "" {
					writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Upstream redirect without a target"})
					return
				}
				followed, ferr := client.fetchRedirectTarget(r.Context(), target, streamRequestHeaders(r))
				if ferr != nil {
					if errors.Is(ferr, context.Canceled) || errors.Is(ferr, context.DeadlineExceeded) {
						return
					}
					if a.Logger != nil {
						a.Logger.Errorf("Stream redirect follow failed: itemId=%s: %s", virtualItemID, redactURLInError(ferr))
					}
					writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Failed to follow upstream redirect"})
					return
				}
				if resp != nil {
					resp.Body.Close()
				}
				resp = followed
			}
			if resp != nil && isRedirectStatus(resp.StatusCode) {
				writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Too many upstream redirects"})
				return
			}
		}
	}

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
		var manifest string
		if rmax, rblock := a.parentalPolicyFor(r); rmax > 0 || rblock {
			// A restricted user must fetch every manifest resource through the local
			// authorized entry, including external URLs (plan §8.6.1).
			userID := ""
			if reqCtx != nil && reqCtx.ProxyUser != nil {
				userID = reqCtx.ProxyUser.UserID
			}
			rewritten, rerr := a.RewriteM3U8ForItemWithResources(string(body), baseURL, proxyToken, resourceRewriteContext{
				UserID:        userID,
				VirtualItemID: virtualItemID,
				ServerID:      client.ID,
				OriginalID:    originalID,
				ManifestKey:   streamManifestWindowKey(upstreamPath, query),
			})
			if rerr != nil {
				if a.Logger != nil {
					a.Logger.Errorf("Stream resource rewrite failed: itemId=%s: %v", virtualItemID, rerr)
				}
				writeStreamResourceRewriteError(w, rerr)
				return
			}
			manifest = rewritten
		} else {
			manifest = RewriteM3U8ForItem(string(body), baseURL, virtualItemID, proxyToken)
		}
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
	// Reject a paused or unauthorized source before rewriting the query and
	// before the same-server fast return, otherwise a same-server MediaSourceId
	// would bypass the aggregation switch entirely.
	if !a.canUseAggregatedServer(requestContextFrom(r.Context()), mediaSource.ServerID) {
		writeJSON(w, http.StatusForbidden, map[string]any{"message": "Access denied"})
		return nil, "", false
	}
	query.Set("MediaSourceId", mediaSource.OriginalID)
	if mediaSource.ServerID == resolved.ServerID {
		return resolved.Client, resolved.OriginalID, true
	}
	target := a.Upstream.ClientByID(mediaSource.ServerID)
	if target == nil || !target.IsOnline() {
		writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Target media source server is unavailable"})
		return nil, "", false
	}
	// Attribute the cross-source MediaSourceId to this item before touching the
	// playback slot: a source on another server only proves it belongs to this movie
	// when the item has a known copy there. Otherwise playing it would silently
	// substitute the current item's id on a server that never had it.
	originalID := ""
	for _, other := range resolved.OtherInstances {
		if other.ServerID == mediaSource.ServerID {
			originalID = other.OriginalID
			break
		}
	}
	if originalID == "" {
		if a.Logger != nil {
			a.Logger.Warnf("Stream: MediaSourceId %s is on server [%s] with no matching copy of the item", virtualMediaSourceID, target.Name)
		}
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "MediaSource does not belong to this item"})
		return nil, "", false
	}
	if a.Logger != nil {
		a.Logger.Infof("Stream: switching to server [%s] for MediaSourceId %s", target.Name, virtualMediaSourceID)
	}
	return target, originalID, true
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
		// Cleanup broadcast: a paused server must still be able to release an
		// active encoding, so iterate every online client and keep the pure
		// account-grant check instead of the aggregation-filtered list.
		for _, client := range a.Upstream.OnlineClients() {
			if a.isServerAllowed(requestContextFrom(r.Context()), client.ID) {
				_ = a.forwardNoContent(r, client, http.MethodDelete, "/Videos/ActiveEncodings", query, nil)
			}
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
