package backend

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
)

func (a *App) handleVideoProxy(w http.ResponseWriter, r *http.Request) {
	virtualItemID := r.PathValue("itemId")
	query := cloneValues(r.URL.Query())
	if a.Logger != nil {
		a.Logger.Debugf("Stream request: itemId=%s, query=%s", virtualItemID, query.Encode())
	}
	resolved := a.resolveRouteID(virtualItemID)
	if resolved == nil {
		if a.Logger != nil {
			a.Logger.Warnf("Stream: itemId=%s not found in mappings", virtualItemID)
		}
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Item not found"})
		return
	}
	if !a.isServerAllowed(requestContextFrom(r.Context()), resolved.ServerIndex) {
		writeJSON(w, http.StatusForbidden, map[string]any{"message": "Access denied"})
		return
	}
	rest := r.PathValue("rest")
	if rest == "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Stream not found"})
		return
	}
	// Resolve virtual MediaSourceId in subtitle/attachment paths:
	// /Videos/{itemId}/{mediaSourceId}/Subtitles/...  or  .../Attachments/...
	rest = resolveMediaSourceInPath(rest, a.IDStore)
	actualClient := resolved.Client
	actualOriginalID := resolved.OriginalID

	virtualMediaSourceID := query.Get("MediaSourceId")
	if virtualMediaSourceID != "" {
		if msResolved := a.IDStore.ResolveVirtualID(virtualMediaSourceID); msResolved != nil {
			query.Set("MediaSourceId", msResolved.OriginalID)
			if msResolved.ServerIndex != resolved.ServerIndex {
				if !a.isServerAllowed(requestContextFrom(r.Context()), msResolved.ServerIndex) {
					writeJSON(w, http.StatusForbidden, map[string]any{"message": "Access denied"})
					return
				}
				client := a.Upstream.GetClient(msResolved.ServerIndex)
				if client == nil || !client.IsOnline() {
					writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Target media source server is unavailable"})
					return
				}
				if a.Logger != nil {
					a.Logger.Infof("Stream: switching to server [%s] for MediaSourceId %s", client.Name, virtualMediaSourceID)
				}
				{
					// Enforce concurrent playback limit on the target server
					if reqCtx := requestContextFrom(r.Context()); reqCtx != nil && reqCtx.ProxyUser != nil && reqCtx.ProxyUser.Role != "admin" && a.PlaybackLimiter != nil {
						cfg := a.ConfigStore.Snapshot()
						if msResolved.ServerIndex >= 0 && msResolved.ServerIndex < len(cfg.Upstream) {
							maxConcurrent := cfg.Upstream[msResolved.ServerIndex].MaxConcurrent
							if !a.PlaybackLimiter.TryStart(reqCtx.ProxyUser.UserID, msResolved.ServerIndex, virtualItemID, maxConcurrent) {
								writeJSON(w, http.StatusTooManyRequests, map[string]any{"message": "已达到最大同时播放数限制"})
								return
							}
							a.PlaybackLimiter.Stop(reqCtx.ProxyUser.UserID, resolved.ServerIndex)
						}
					}
					actualClient = client
					// Update actualOriginalID to match this server's copy of the item
					found := false
					for _, other := range resolved.OtherInstances {
						if other.ServerIndex == msResolved.ServerIndex {
							actualOriginalID = other.OriginalID
							found = true
							break
						}
					}
					if !found && msResolved.ServerIndex == resolved.ServerIndex {
						actualOriginalID = resolved.OriginalID
						found = true
					}
				}
			}
		} else {
			if a.Logger != nil {
				a.Logger.Warnf("Stream: MediaSourceId %s cannot be resolved to any server", virtualMediaSourceID)
			}
		}
	}

	if playSessionID := query.Get("PlaySessionId"); playSessionID != "" {
		if psResolved := a.IDStore.ResolveVirtualID(playSessionID); psResolved != nil {
			query.Set("PlaySessionId", psResolved.OriginalID)
		}
	}
	// Replace proxy token with upstream's access token for stream auth
	query.Del("api_key")
	query.Del("ApiKey")
	token := actualClient.getAccessToken()
	if token != "" {
		query.Set("api_key", token)
	}
	upstreamPath := "/Videos/" + actualOriginalID + "/" + rest
	if a.Logger != nil {
		a.Logger.Infof("Stream: /Videos/%s/%s → [%s] %s (using token: %v)",
			virtualItemID, rest, actualClient.Name, upstreamPath, token != "")
	}
	// Redirect mode: return 302 to upstream stream URL
	playbackMode := actualClient.Config.PlaybackMode
	if playbackMode == "" {
		playbackMode = a.ConfigStore.Snapshot().Playback.Mode
	}
	if playbackMode == "redirect" {
		redirectURL := actualClient.BuildURL(upstreamPath, query, true)
		if a.Logger != nil {
			a.Logger.Debugf("Stream redirect: /Videos/%s/%s → 302 %s", virtualItemID, rest, redirectURL)
		}
		http.Redirect(w, r, redirectURL, http.StatusFound)
		return
	}
	// Forward Range and Accept headers from client for seeking / partial content
	streamHeaders := http.Header{}
	for _, h := range []string{"Range", "Accept", "Accept-Encoding", "Accept-Language"} {
		if v := r.Header.Get(h); v != "" {
			streamHeaders.Set(h, v)
		}
	}
	if a.Logger != nil {
		a.Logger.Debugf("Stream request headers: Range=%q, Accept=%q, AE=%q",
			r.Header.Get("Range"), r.Header.Get("Accept"), r.Header.Get("Accept-Encoding"))
	}
	resp, err := actualClient.Stream(r.Context(), requestContextFrom(r.Context()), a.Identity, upstreamPath, query, streamHeaders)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return // client disconnected or timed out — not a server error
		}
		if a.Logger != nil {
			a.Logger.Errorf("Stream error: /Videos/%s/%s: %s", virtualItemID, rest, err.Error())
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{"message": err.Error()})
		return
	}
	defer resp.Body.Close()
	contentType := resp.Header.Get("Content-Type")
	isM3U8 := strings.Contains(contentType, "mpegurl") || strings.HasSuffix(strings.ToLower(rest), ".m3u8")
	if isM3U8 {
		body, _ := io.ReadAll(resp.Body)
		proxyToken := ""
		if reqCtx := requestContextFrom(r.Context()); reqCtx != nil {
			proxyToken = reqCtx.ProxyToken
		}
		manifest := RewriteM3U8ForItem(string(body), actualClient.BuildURL(upstreamPath, query, true), virtualItemID, proxyToken)
		w.Header().Set("Content-Type", "application/x-mpegURL")
		_, _ = io.WriteString(w, manifest)
		return
	}

	if a.Logger != nil {
		a.Logger.Infof("Stream upstream response: Status=%d, Type=%q, Len=%s, Encoding=%q, Range=%q",
			resp.StatusCode, contentType, resp.Header.Get("Content-Length"),
			resp.Header.Get("Content-Encoding"), resp.Header.Get("Content-Range"))
	}

	for _, header := range []string{
		"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges",
		"Cache-Control", "ETag", "Last-Modified", "Transfer-Encoding",
		"Content-Disposition", "Content-Encoding", "Date", "Server",
	} {
		if value := resp.Header.Get(header); value != "" {
			if strings.EqualFold(header, "Transfer-Encoding") && strings.Contains(strings.ToLower(value), "chunked") {
				continue
			}
			w.Header().Set(header, value)
		}
	}

	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (a *App) handleAudioProxy(w http.ResponseWriter, r *http.Request) {
	virtualItemID := r.PathValue("itemId")
	query := cloneValues(r.URL.Query())
	if a.Logger != nil {
		a.Logger.Debugf("Audio stream request: itemId=%s, query=%s", virtualItemID, query.Encode())
	}
	resolved := a.resolveRouteID(virtualItemID)
	if resolved == nil {
		if a.Logger != nil {
			a.Logger.Warnf("Audio stream: itemId=%s not found in mappings", virtualItemID)
		}
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Item not found"})
		return
	}
	if !a.isServerAllowed(requestContextFrom(r.Context()), resolved.ServerIndex) {
		writeJSON(w, http.StatusForbidden, map[string]any{"message": "Access denied"})
		return
	}
	rest := r.PathValue("rest")
	if rest == "" {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Stream not found"})
		return
	}
	// Resolve virtual MediaSourceId in subtitle/attachment paths
	rest = resolveMediaSourceInPath(rest, a.IDStore)
	actualClient := resolved.Client
	actualOriginalID := resolved.OriginalID

	virtualMediaSourceID := query.Get("MediaSourceId")
	if virtualMediaSourceID != "" {
		if msResolved := a.IDStore.ResolveVirtualID(virtualMediaSourceID); msResolved != nil {
			query.Set("MediaSourceId", msResolved.OriginalID)
			if msResolved.ServerIndex != resolved.ServerIndex {
				if !a.isServerAllowed(requestContextFrom(r.Context()), msResolved.ServerIndex) {
					writeJSON(w, http.StatusForbidden, map[string]any{"message": "Access denied"})
					return
				}
				client := a.Upstream.GetClient(msResolved.ServerIndex)
				if client == nil || !client.IsOnline() {
					writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Target media source server is unavailable"})
					return
				}
				if a.Logger != nil {
					a.Logger.Infof("Audio stream: switching to server [%s] for MediaSourceId %s", client.Name, virtualMediaSourceID)
				}
				{
					// Enforce concurrent playback limit on the target server
					if reqCtx := requestContextFrom(r.Context()); reqCtx != nil && reqCtx.ProxyUser != nil && reqCtx.ProxyUser.Role != "admin" && a.PlaybackLimiter != nil {
						cfg := a.ConfigStore.Snapshot()
						if msResolved.ServerIndex >= 0 && msResolved.ServerIndex < len(cfg.Upstream) {
							maxConcurrent := cfg.Upstream[msResolved.ServerIndex].MaxConcurrent
							if !a.PlaybackLimiter.TryStart(reqCtx.ProxyUser.UserID, msResolved.ServerIndex, virtualItemID, maxConcurrent) {
								writeJSON(w, http.StatusTooManyRequests, map[string]any{"message": "已达到最大同时播放数限制"})
								return
							}
							a.PlaybackLimiter.Stop(reqCtx.ProxyUser.UserID, resolved.ServerIndex)
						}
					}
					actualClient = client
					found := false
					for _, other := range resolved.OtherInstances {
						if other.ServerIndex == msResolved.ServerIndex {
							actualOriginalID = other.OriginalID
							found = true
							break
						}
					}
					if !found && msResolved.ServerIndex == resolved.ServerIndex {
						actualOriginalID = resolved.OriginalID
						found = true
					}
				}
			}
		}
	}

	if playSessionID := query.Get("PlaySessionId"); playSessionID != "" {
		if psResolved := a.IDStore.ResolveVirtualID(playSessionID); psResolved != nil {
			query.Set("PlaySessionId", psResolved.OriginalID)
		}
	}
	// Replace proxy token with upstream's access token for stream auth
	query.Del("api_key")
	query.Del("ApiKey")
	if token := actualClient.getAccessToken(); token != "" {
		query.Set("api_key", token)
	}
	upstreamPath := "/Audio/" + actualOriginalID + "/" + rest
	if a.Logger != nil {
		a.Logger.Infof("Stream: /Audio/%s/%s → [%s] %s", virtualItemID, rest, actualClient.Name, upstreamPath)
	}
	// Redirect mode: return 302 to upstream stream URL
	playbackMode := actualClient.Config.PlaybackMode
	if playbackMode == "" {
		playbackMode = a.ConfigStore.Snapshot().Playback.Mode
	}
	if playbackMode == "redirect" {
		redirectURL := actualClient.BuildURL(upstreamPath, query, true)
		if a.Logger != nil {
			a.Logger.Debugf("Stream redirect: /Audio/%s/%s → 302 %s", virtualItemID, rest, redirectURL)
		}
		http.Redirect(w, r, redirectURL, http.StatusFound)
		return
	}
	// Forward Range and Accept headers from client for seeking / partial content
	streamHeaders := http.Header{}
	for _, h := range []string{"Range", "Accept", "Accept-Encoding", "Accept-Language"} {
		if v := r.Header.Get(h); v != "" {
			streamHeaders.Set(h, v)
		}
	}
	resp, err := actualClient.Stream(r.Context(), requestContextFrom(r.Context()), a.Identity, upstreamPath, query, streamHeaders)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return // client disconnected or timed out — not a server error
		}
		if a.Logger != nil {
			a.Logger.Errorf("Stream error: /Audio/%s/%s: %s", virtualItemID, rest, err.Error())
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{"message": err.Error()})
		return
	}
	defer resp.Body.Close()
	contentType := resp.Header.Get("Content-Type")
	isM3U8 := strings.Contains(contentType, "mpegurl") || strings.HasSuffix(strings.ToLower(rest), ".m3u8")
	if isM3U8 {
		body, _ := io.ReadAll(resp.Body)
		proxyToken := ""
		if reqCtx := requestContextFrom(r.Context()); reqCtx != nil {
			proxyToken = reqCtx.ProxyToken
		}
		manifest := RewriteM3U8ForItem(string(body), actualClient.BuildURL(upstreamPath, query, true), virtualItemID, proxyToken)
		w.Header().Set("Content-Type", "application/x-mpegURL")
		_, _ = io.WriteString(w, manifest)
		return
	}

	if a.Logger != nil {
		a.Logger.Infof("Audio stream upstream response: Status=%d, Type=%q, Len=%s",
			resp.StatusCode, contentType, resp.Header.Get("Content-Length"))
	}

	for _, header := range []string{
		"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges",
		"Cache-Control", "ETag", "Last-Modified", "Transfer-Encoding",
		"Content-Disposition", "Content-Encoding", "Date", "Server",
	} {
		if value := resp.Header.Get(header); value != "" {
			if strings.EqualFold(header, "Transfer-Encoding") && strings.Contains(strings.ToLower(value), "chunked") {
				continue
			}
			w.Header().Set(header, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (a *App) handleDeleteActiveEncodings(w http.ResponseWriter, r *http.Request) {
	query := cloneValues(r.URL.Query())
	serverIndex := -1
	if playSessionID := query.Get("PlaySessionId"); playSessionID != "" {
		if resolved := a.IDStore.ResolveVirtualID(playSessionID); resolved != nil {
			query.Set("PlaySessionId", resolved.OriginalID)
			serverIndex = resolved.ServerIndex
		}
	}
	if serverIndex >= 0 {
		if !a.isServerAllowed(requestContextFrom(r.Context()), serverIndex) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if client := a.Upstream.GetClient(serverIndex); client != nil && client.IsOnline() {
			_ = a.forwardNoContent(r, client, http.MethodDelete, "/Videos/ActiveEncodings", query, nil)
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	for _, client := range a.allowedClients(requestContextFrom(r.Context())) {
		_ = a.forwardNoContent(r, client, http.MethodDelete, "/Videos/ActiveEncodings", query, nil)
	}
	w.WriteHeader(http.StatusNoContent)
}
