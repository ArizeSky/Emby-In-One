package backend

import (
	"encoding/json"
	"net/http"
	"net/url"
	"path"
	"strings"
)

func (a *App) handlePlaybackInfo(w http.ResponseWriter, r *http.Request) {
	resolved, routeStatus := a.resolveRouteForRequest(requestContextFrom(r.Context()), r.PathValue("itemId"))
	if routeStatus != 0 {
		if routeStatus == http.StatusNotFound && a.Logger != nil {
			a.Logger.Warnf("PlaybackInfo: itemId=%s not found in mappings", r.PathValue("itemId"))
		}
		a.writeRouteAccessError(w, routeStatus)
		return
	}
	reqCtx := requestContextFrom(r.Context())
	if !a.requireServerAccess(w, r, resolved) {
		return
	}
	query := cloneValues(r.URL.Query())
	body := map[string]any{}
	if r.Method == http.MethodPost && r.Body != nil {
		// A malformed body must not be silently dropped: forwarding an empty body
		// upstream would produce a confusing result. Report it as a bad request.
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": "Invalid request body"})
			return
		}
	}
	var selected bool
	resolved, selected = a.selectedPlaybackSource(w, r, resolved, body, query)
	if !selected {
		return
	}
	// Concurrent playback limit check for regular users
	if limiterUserID, ok := a.playbackLimiterKey(reqCtx, r, resolved.ServerID); ok {
		var maxConcurrent int
		for _, u := range a.ConfigStore.Snapshot().Upstream {
			if u.ID == resolved.ServerID {
				maxConcurrent = u.MaxConcurrent
				break
			}
		}
		if !a.PlaybackLimiter.TryStart(limiterUserID, resolved.ServerID, r.PathValue("itemId"), maxConcurrent) {
			writeJSON(w, http.StatusTooManyRequests, map[string]any{"message": "已达到最大同时播放数限制"})
			return
		}
	}
	instances := a.collectAllowedInstances(reqCtx, resolved)
	if a.Logger != nil {
		a.Logger.Debugf("PlaybackInfo: itemId=%s → server=[%s] originalId=%s, instances=%d",
			r.PathValue("itemId"), resolved.Client.Name, resolved.OriginalID, len(instances))
	}

	// Remove proxy token from query before forwarding to upstream
	stripProxyCredentialQuery(query)

	var base map[string]any
	baseServerID := resolved.ServerID
	allMediaSources := []map[string]any{}
	maxR, blockUnrated := a.parentalPolicyFor(r)
	restricted := maxR > 0 || blockUnrated
	_, hasBodySelection := body["MediaSourceId"]
	explicitSelected := hasBodySelection || query.Get("MediaSourceId") != ""
	selectedServerID := resolved.ServerID
	anyDenied := false
	anyUnconfirmed := false
	releaseSlot := func() {
		if limiterUserID, ok := a.playbackLimiterKey(reqCtx, r, resolved.ServerID); ok {
			a.PlaybackLimiter.Stop(limiterUserID, resolved.ServerID)
		}
	}
	for _, inst := range instances {
		// Per-instance parental check (plan §8.4): an explicit selection of a
		// denied instance is a hard 403 (not masked by an allowed sibling), an
		// unconfirmable rating fails closed with 502, and otherwise denied
		// instances are simply dropped from the returned MediaSources.
		if restricted {
			allowed, confirmed := a.checkInstanceParentalAccess(r, inst.Client, inst.OriginalID)
			if !allowed {
				if explicitSelected && inst.ServerID == selectedServerID {
					releaseSlot()
					if !confirmed {
						writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Cannot verify content rating"})
					} else {
						writeJSON(w, http.StatusForbidden, map[string]any{"message": "Content restricted by parental rating"})
					}
					return
				}
				if confirmed {
					anyDenied = true
				} else {
					anyUnconfirmed = true
				}
				continue
			}
		}
		// The client only ever holds EIO's virtual user ID, and Emby prefers a UserId in the
		// query or body over the one the request was authenticated as. Forwarding the virtual
		// ID made the upstream look up a user that does not exist there and answer 500
		// (NullReferenceException), which surfaced as a 502 to the client.
		instQuery := cloneValues(query)
		instQuery.Set("UserId", inst.Client.clientUserID())
		instBody := deepCloneMap(body)
		instBody["UserId"] = inst.Client.clientUserID()
		payload, err := inst.Client.RequestJSON(r.Context(), requestContextFrom(r.Context()), a.Identity, r.Method, "/Items/"+inst.OriginalID+"/PlaybackInfo", instQuery, instBody)
		if err != nil {
			if a.Logger != nil {
				a.Logger.Warnf("PlaybackInfo: server=[%s] originalId=%s failed: %s", inst.Client.Name, inst.OriginalID, redactURLInError(err))
			}
			continue
		}
		data, ok := payload.(map[string]any)
		if !ok {
			continue
		}
		if base == nil {
			base = deepCloneMap(data)
			baseServerID = inst.ServerID
		}
		for _, raw := range asItems(map[string]any{"Items": data["MediaSources"]}) {
			mediaSource := deepCloneMap(raw)
			annotateMediaSource(mediaSource, inst.Client)
			originalMSID, _ := mediaSource["Id"].(string)
			virtualMSID := originalMSID
			if originalMSID != "" {
				virtualMSID = a.IDStore.GetOrCreateVirtualID(originalMSID, inst.ServerID)
				mediaSource["Id"] = virtualMSID
			}
			if directURL, ok := mediaSource["DirectStreamUrl"].(string); ok && directURL != "" {
				// Extract container from URL, stripping query string first
				// Node.js uses regex /\.([a-z0-9]+)(?:\?|$)/i — path.Ext doesn't stop at '?'
				cleanURL := directURL
				if qIdx := strings.IndexByte(cleanURL, '?'); qIdx >= 0 {
					cleanURL = cleanURL[:qIdx]
				}
				container := strings.TrimPrefix(path.Ext(cleanURL), ".")
				if container == "" {
					if rawContainer, ok := mediaSource["Container"].(string); ok {
						container = rawContainer
					}
				}
				if container == "" {
					container = "mp4"
				}
				proxyURL := url.Values{}
				proxyURL.Set("MediaSourceId", virtualMSID)
				proxyURL.Set("Static", "true")
				if reqCtx := requestContextFrom(r.Context()); reqCtx != nil && reqCtx.ProxyToken != "" {
					proxyURL.Set("api_key", reqCtx.ProxyToken)
				}
				mediaSource["DirectStreamUrl"] = "/Videos/" + r.PathValue("itemId") + "/stream." + container + "?" + proxyURL.Encode()
			}
			if transcodingURL, ok := mediaSource["TranscodingUrl"].(string); ok && transcodingURL != "" {
				parsed, err := url.Parse(transcodingURL)
				if err == nil {
					if parsed.Scheme == "" {
						parsed, _ = url.Parse(inst.Client.StreamBaseURL + transcodingURL)
					}
					proxyPath := parsed.Path
					// Emby serves transcoding manifests from a lowercase
					// /videos/ root while direct-stream URLs use /Videos/;
					// the item id swap must catch both spellings so the
					// client-facing URL always carries the virtual id this
					// proxy can route.
					proxyPath = replacePathItemID(proxyPath, "videos", inst.OriginalID, r.PathValue("itemId"))
					proxyPath = replacePathItemID(proxyPath, "audio", inst.OriginalID, r.PathValue("itemId"))
					queryValues := parsed.Query()
					stripProxyCredentialQuery(queryValues)
					if originalMSID != "" {
						queryValues.Set("MediaSourceId", virtualMSID)
					}
					if reqCtx := requestContextFrom(r.Context()); reqCtx != nil && reqCtx.ProxyToken != "" {
						queryValues.Set("api_key", reqCtx.ProxyToken)
					}
					mediaSource["TranscodingUrl"] = proxyPath + "?" + queryValues.Encode()
				}
			}
			// Rewrite MediaSource.Path for Http protocol and MediaStreams[].DeliveryUrl
			if protocol, _ := mediaSource["Protocol"].(string); protocol == "Http" {
				if msPath, ok := mediaSource["Path"].(string); ok && msPath != "" {
					msPath = strings.ReplaceAll(msPath, inst.OriginalID, r.PathValue("itemId"))
					if originalMSID != "" && originalMSID != inst.OriginalID {
						msPath = strings.ReplaceAll(msPath, originalMSID, virtualMSID)
					}
					mediaSource["Path"] = msPath
				}
			}
			if rawStreams, ok := mediaSource["MediaStreams"].([]any); ok {
				for _, rawStream := range rawStreams {
					if stream, ok := rawStream.(map[string]any); ok {
						if deliveryURL, ok := stream["DeliveryUrl"].(string); ok && deliveryURL != "" {
							deliveryURL = strings.ReplaceAll(deliveryURL, inst.OriginalID, r.PathValue("itemId"))
							if originalMSID != "" && originalMSID != inst.OriginalID {
								deliveryURL = strings.ReplaceAll(deliveryURL, originalMSID, virtualMSID)
							}
							if parsed, err := url.Parse(deliveryURL); err == nil {
								queryValues := parsed.Query()
								stripProxyCredentialQuery(queryValues)
								if reqCtx != nil && reqCtx.ProxyToken != "" {
									queryValues.Set("api_key", reqCtx.ProxyToken)
								}
								parsed.RawQuery = queryValues.Encode()
								deliveryURL = parsed.String()
							}
							stream["DeliveryUrl"] = deliveryURL
						}
					}
				}
			}
			allMediaSources = append(allMediaSources, mediaSource)
		}
	}
	// Record which server served this virtual item so Sessions/Playing routes back correctly
	if len(allMediaSources) > 0 {
		if virtualItemID := r.PathValue("itemId"); virtualItemID != "" {
			a.IDStore.SetActiveStream(virtualItemID, baseServerID)
		}
	}
	if base == nil {
		if a.Logger != nil {
			a.Logger.Errorf("PlaybackInfo: all upstream requests failed for itemId=%s", r.PathValue("itemId"))
		}
		// Nothing will play, so release the slot TryStart reserved above. Clients retry
		// PlaybackInfo on failure, and holding the slot kept the user counted against
		// the server's capacity for the full heartbeat timeout while they could not
		// actually start a stream.
		if limiterUserID, ok := a.playbackLimiterKey(reqCtx, r, resolved.ServerID); ok {
			a.PlaybackLimiter.Stop(limiterUserID, resolved.ServerID)
		}
		if anyDenied && !anyUnconfirmed {
			writeJSON(w, http.StatusForbidden, map[string]any{"message": "Content restricted by parental rating"})
		} else {
			writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Failed to fetch playback info from upstream"})
		}
		return
	}
	if a.Logger != nil {
		a.Logger.Debugf("PlaybackInfo: returning %d MediaSources for itemId=%s", len(allMediaSources), r.PathValue("itemId"))
	}
	cfg := a.ConfigStore.Snapshot()
	// Rewrite top-level fields (excluding MediaSources which were already virtualised per-server above)
	delete(base, "MediaSources")
	rewriteResponseIDs(base, baseServerID, a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
	base["MediaSources"] = make([]any, 0, len(allMediaSources))
	for _, mediaSource := range privacyDetailMediaSources(r, allMediaSources) {
		base["MediaSources"] = append(base["MediaSources"].([]any), mediaSource)
	}
	writeJSON(w, http.StatusOK, base)
}

// replacePathItemID swaps the item id segment of a /Videos/{id}/ or /Audio/{id}/
// path, matching the stream root case-insensitively (Emby mixes /Videos/ and
// /videos/ between its direct-stream and transcoding URLs). The rewritten path
// always carries the canonical capitalised root, because this proxy's own
// stream routes are registered as /Videos/ and /Audio/ — a lowercase root would
// fall through to the fallback proxy and fail to resolve.
func replacePathItemID(path, root, fromID, toID string) string {
	if fromID == "" || toID == "" || fromID == toID {
		return path
	}
	root = strings.ToLower(root)
	canonical := strings.ToUpper(root[:1]) + root[1:]
	lower := strings.ToLower(path)
	idx := strings.Index(lower, "/"+root+"/")
	if idx < 0 {
		return path
	}
	idStart := idx + len(root) + 2
	idEnd := strings.IndexByte(path[idStart:], '/')
	if idEnd < 0 {
		return path
	}
	if path[idStart:idStart+idEnd] != fromID {
		return path
	}
	return path[:idx+1] + canonical + "/" + toID + path[idStart+idEnd:]
}

func deepCloneMap(source map[string]any) map[string]any {
	encoded, _ := json.Marshal(source)
	var decoded map[string]any
	_ = json.Unmarshal(encoded, &decoded)
	return decoded
}

// resolveMediaSourceInPath resolves a virtual MediaSourceId embedded as the first path
// segment when followed by /Subtitles/ or /Attachments/ (e.g. "{msId}/Subtitles/0/Stream.srt").
func resolveMediaSourceInPath(rest string, idStore *IDStore) string {
	slash := strings.IndexByte(rest, '/')
	if slash <= 0 {
		return rest
	}
	firstSeg := rest[:slash]
	after := rest[slash:] // includes leading '/'
	if !strings.HasPrefix(after, "/Subtitles/") && !strings.HasPrefix(after, "/Attachments/") {
		return rest
	}
	if resolved := idStore.ResolveVirtualID(firstSeg); resolved != nil {
		return resolved.OriginalID + after
	}
	// Emby clients (notably Android TV) build the subtitle/attachment path as
	// "mediasource_<itemId>" rather than echoing the MediaSourceId we issued.
	// Strip that prefix and resolve the embedded virtual item ID, then re-add
	// it because some upstreams also address their sources as "mediasource_<id>".
	if strings.HasPrefix(firstSeg, "mediasource_") {
		if resolved := idStore.ResolveVirtualID(strings.TrimPrefix(firstSeg, "mediasource_")); resolved != nil {
			return "mediasource_" + resolved.OriginalID + after
		}
	}
	return rest
}

// resolveMediaSourceInPathForServer rewrites the leading MediaSourceId segment of a
// subtitle/attachment path so it addresses the item on the server that will actually
// serve it. It is the server-aware counterpart of resolveMediaSourceInPath: the caller
// resolves the real target server first, then passes that server's id and the item's
// original id there, so a cross-source MediaSourceId ends up on the right server instead
// of the primary's id. Unknown ids are left untouched.
func resolveMediaSourceInPathForServer(rest string, idStore *IDStore, serverID, itemOriginalID string) string {
	slash := strings.IndexByte(rest, '/')
	if slash <= 0 {
		return rest
	}
	firstSeg := rest[:slash]
	after := rest[slash:]
	if !strings.HasPrefix(after, "/Subtitles/") && !strings.HasPrefix(after, "/Attachments/") {
		return rest
	}
	// Exact virtual MediaSourceId match wins over the mediasource_ prefix form.
	if resolved := idStore.ResolveVirtualID(firstSeg); resolved != nil {
		if resolved.ServerID == serverID {
			return resolved.OriginalID + after
		}
		return rest
	}
	if strings.HasPrefix(firstSeg, "mediasource_") {
		if idStore.ResolveVirtualID(strings.TrimPrefix(firstSeg, "mediasource_")) != nil && itemOriginalID != "" {
			return "mediasource_" + itemOriginalID + after
		}
	}
	return rest
}
