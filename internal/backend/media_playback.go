package backend

import (
	"encoding/json"
	"net/http"
	"net/url"
	"path"
	"strings"
)

func (a *App) handlePlaybackInfo(w http.ResponseWriter, r *http.Request) {
	resolved := a.resolveRouteID(r.PathValue("itemId"))
	if resolved == nil {
		if a.Logger != nil {
			a.Logger.Warnf("PlaybackInfo: itemId=%s not found in mappings", r.PathValue("itemId"))
		}
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Item not found"})
		return
	}
	reqCtx := requestContextFrom(r.Context())
	if !a.isServerAllowed(reqCtx, resolved.ServerIndex) {
		writeJSON(w, http.StatusForbidden, map[string]any{"message": "Access denied"})
		return
	}
	// Concurrent playback limit check for regular users
	if reqCtx != nil && reqCtx.ProxyUser != nil && reqCtx.ProxyUser.Role != "admin" && a.PlaybackLimiter != nil {
		cfg := a.ConfigStore.Snapshot()
		if resolved.ServerIndex >= 0 && resolved.ServerIndex < len(cfg.Upstream) {
			maxConcurrent := cfg.Upstream[resolved.ServerIndex].MaxConcurrent
			if !a.PlaybackLimiter.TryStart(reqCtx.ProxyUser.UserID, resolved.ServerIndex, r.PathValue("itemId"), maxConcurrent) {
				writeJSON(w, http.StatusTooManyRequests, map[string]any{"message": "已达到最大同时播放数限制"})
				return
			}
		}
	}
	instances := a.collectItemInstances(resolved)
	if a.Logger != nil {
		a.Logger.Debugf("PlaybackInfo: itemId=%s → server=[%s] originalId=%s, instances=%d",
			r.PathValue("itemId"), resolved.Client.Name, resolved.OriginalID, len(instances))
	}
	query := cloneValues(r.URL.Query())
	body := map[string]any{}
	if r.Method == http.MethodPost && r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	if mediaSourceID, ok := body["MediaSourceId"].(string); ok {
		if msResolved := a.IDStore.ResolveVirtualID(mediaSourceID); msResolved != nil {
			body["MediaSourceId"] = msResolved.OriginalID
		}
	}
	if mediaSourceID := query.Get("MediaSourceId"); mediaSourceID != "" {
		if msResolved := a.IDStore.ResolveVirtualID(mediaSourceID); msResolved != nil {
			query.Set("MediaSourceId", msResolved.OriginalID)
		}
	}

	// Remove proxy token from query before forwarding to upstream
	query.Del("api_key")
	query.Del("ApiKey")

	var base map[string]any
	allMediaSources := []map[string]any{}
	for _, inst := range instances {
		payload, err := inst.Client.RequestJSON(r.Context(), requestContextFrom(r.Context()), a.Identity, r.Method, "/Items/"+inst.OriginalID+"/PlaybackInfo", query, body)
		if err != nil {
			if a.Logger != nil {
				a.Logger.Warnf("PlaybackInfo: server=[%s] originalId=%s failed: %v", inst.Client.Name, inst.OriginalID, err)
			}
			continue
		}
		data, ok := payload.(map[string]any)
		if !ok {
			continue
		}
		if base == nil {
			base = deepCloneMap(data)
		}
		for _, raw := range asItems(map[string]any{"Items": data["MediaSources"]}) {
			mediaSource := deepCloneMap(raw)
			originalMSID, _ := mediaSource["Id"].(string)
			virtualMSID := originalMSID
			if originalMSID != "" {
				virtualMSID = a.IDStore.GetOrCreateVirtualID(originalMSID, inst.ServerIndex)
				mediaSource["Id"] = virtualMSID
			}
			if directURL, ok := mediaSource["DirectStreamUrl"].(string); ok && directURL != "" {
				// Store original full URL for redirect mode
				a.IDStore.SetMediaSourceStreamURL(virtualMSID, resolveAbsoluteStreamURL(inst.Client.StreamBaseURL, directURL))

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
				// Store original full URL for redirect mode
				a.IDStore.SetMediaSourceStreamURL(virtualMSID+"_transcode", resolveAbsoluteStreamURL(inst.Client.StreamBaseURL, transcodingURL))

				parsed, err := url.Parse(transcodingURL)
				if err == nil {
					if parsed.Scheme == "" {
						parsed, _ = url.Parse(inst.Client.StreamBaseURL + transcodingURL)
					}
					proxyPath := parsed.Path
					proxyPath = strings.Replace(proxyPath, "/Videos/"+inst.OriginalID+"/", "/Videos/"+r.PathValue("itemId")+"/", 1)
					proxyPath = strings.Replace(proxyPath, "/Audio/"+inst.OriginalID+"/", "/Audio/"+r.PathValue("itemId")+"/", 1)
					queryValues := parsed.Query()
					queryValues.Del("api_key")
					queryValues.Del("ApiKey")
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
			a.IDStore.SetActiveStream(virtualItemID, resolved.ServerIndex)
		}
	}
	if base == nil {
		if a.Logger != nil {
			a.Logger.Errorf("PlaybackInfo: all upstream requests failed for itemId=%s", r.PathValue("itemId"))
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Failed to fetch playback info from upstream"})
		return
	}
	if a.Logger != nil {
		a.Logger.Debugf("PlaybackInfo: returning %d MediaSources for itemId=%s", len(allMediaSources), r.PathValue("itemId"))
	}
	cfg := a.ConfigStore.Snapshot()
	// Rewrite top-level fields (excluding MediaSources which were already virtualised per-server above)
	delete(base, "MediaSources")
	rewriteResponseIDs(base, resolved.ServerIndex, a.IDStore, cfg.Server.ID, a.Auth.ProxyUserID())
	base["MediaSources"] = make([]any, 0, len(allMediaSources))
	for _, mediaSource := range allMediaSources {
		base["MediaSources"] = append(base["MediaSources"].([]any), mediaSource)
	}
	writeJSON(w, http.StatusOK, base)
}

func deepCloneMap(source map[string]any) map[string]any {
	encoded, _ := json.Marshal(source)
	var decoded map[string]any
	_ = json.Unmarshal(encoded, &decoded)
	return decoded
}

// resolveAbsoluteStreamURL resolves a possibly-relative stream URL against a base URL.
func resolveAbsoluteStreamURL(base, rawURL string) string {
	if strings.HasPrefix(rawURL, "http://") || strings.HasPrefix(rawURL, "https://") {
		return rawURL
	}
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(rawURL, "/")
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
	return rest
}
