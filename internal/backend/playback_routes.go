package backend

import "net/http"

func (a *App) registerStrictPlaybackRoutes(mux *http.ServeMux) {
	for _, method := range []string{"GET", "POST"} {
		mux.HandleFunc(method+" /eio/playback/v1/Items/{itemId}/PlaybackInfo", a.withContext(a.requireAuth(a.handleStrictPlaybackInfo)))
	}
	prefix := "/eio/playback/v1/{attemptID}"
	for _, root := range []string{"Videos", "videos", "Audio", "audio"} {
		handler := a.handleVideoProxy
		if root == "Audio" || root == "audio" {
			handler = a.handleAudioProxy
		}
		mux.HandleFunc("GET "+prefix+"/"+root+"/{itemId}/{rest...}", a.withStrictPlayback(handler))
	}
	mux.HandleFunc("POST "+prefix+"/Sessions/Playing", a.withStrictPlayback(a.handleSessionPlaying))
	mux.HandleFunc("POST "+prefix+"/Sessions/Playing/Progress", a.withStrictPlayback(a.handleSessionPlayingProgress))
	mux.HandleFunc("POST "+prefix+"/Sessions/Playing/Stopped", a.withStrictPlayback(a.handleSessionPlayingStopped))
	mux.HandleFunc("DELETE "+prefix+"/Videos/ActiveEncodings", a.withStrictPlayback(a.handleStrictActiveEncodings))
	mux.HandleFunc("POST "+prefix+"/Users/{userId}/PlayingItems/{itemId}", a.withStrictPlayback(a.handleUserPlayingItemStart))
	mux.HandleFunc("DELETE "+prefix+"/Users/{userId}/PlayingItems/{itemId}", a.withStrictPlayback(a.handleUserPlayingItemStop))
	// Unknown strict resources never fall through to the legacy fallback proxy.
	mux.HandleFunc("/eio/playback/", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 404, map[string]any{"kind": "playback-route-not-found"})
	})
}

func (a *App) handleStrictActiveEncodings(w http.ResponseWriter, r *http.Request) {
	req := requestContextFrom(r.Context())
	if a.rejectStrictSupersededWrite(w, r, *req.Playback, true) {
		return
	}
	client := a.Upstream.ClientByID(req.Playback.ServerID)
	if client == nil {
		w.WriteHeader(409)
		return
	}
	query := strictPlaybackRequestQuery(r)
	query.Del(queryKey(query, "DeviceId"))
	if device := req.Playback.Identity.Headers.Get("X-Emby-Device-Id"); device != "" {
		setQueryValue(query, "DeviceId", device)
	}
	if supplied := queryValue(query, "PlaySessionId"); supplied != "" && supplied != req.Playback.UpstreamPlaySessionID {
		writeJSON(w, 409, map[string]any{"kind": "playback-session-mismatch"})
		return
	}
	setQueryValue(query, "PlaySessionId", req.Playback.UpstreamPlaySessionID)
	if req.Playback.UpstreamPlaySessionID == "" {
		writeJSON(w, 409, map[string]any{"kind": "playback-session-unavailable"})
		return
	}
	if err := a.forwardNoContent(r, client, http.MethodDelete, "/Videos/ActiveEncodings", query, nil); err != nil {
		if !writePreparationError(w, err) {
			writeJSON(w, 502, map[string]any{"kind": "playback-stop-failed"})
		}
		return
	}
	a.confirmStrictPlaybackCleanup(r, *req.Playback)
	a.stopStrictPlaybackBinding(*req.Playback)
	a.finishStrictPlaybackAttemptLocally(r, *req.Playback)
	a.retireStrictPlaybackBinding(*req.Playback)
	w.WriteHeader(http.StatusNoContent)
}
