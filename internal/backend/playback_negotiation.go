package backend

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// One negotiation owns all pending candidates until registration and response
// preparation succeed. A cancellation/error cannot leave public unbound URLs.
type playbackNegotiation struct {
	owner       playbackIdentityOwner
	pending     []playbackIdentityBinding
	fields      []playbackResourceField
	reservation playbackSlotReservation
	committed   bool
}

func playbackStringField(m map[string]any, key string) string { v, _ := m[key].(string); return v }

func playbackAttemptFromContext(req *RequestContext) string {
	if req != nil && req.Playback != nil {
		return req.Playback.AttemptID
	}
	return ""
}
func playbackCapabilityFromContext(req *RequestContext) string {
	if req != nil {
		return req.PlaybackCredential
	}
	return ""
}

func (a *App) handleStrictPlaybackInfo(w http.ResponseWriter, r *http.Request) {
	a.reclaimPlaybackAttempts()
	req := requestContextFrom(r.Context())
	if req == nil || req.ProxyUser == nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	copyCtx := *req
	negotiation := &playbackNegotiation{owner: newPlaybackIdentityOwner(streamResourceUserID(req), req.ProxyToken)}
	copyCtx.PlaybackNegotiation = negotiation
	r = r.WithContext(context.WithValue(r.Context(), requestContextKey{}, &copyCtx))
	defer func() {
		if !negotiation.committed {
			for _, binding := range negotiation.pending {
				_ = a.PlaybackIdentities.fail(binding.AttemptID, negotiation.owner)
				a.StreamResources.removeAttempt(binding.AttemptID)
				a.PlaybackCapabilities.revokeByAttempt(binding.AttemptID)
			}
		}
	}()
	a.handlePlaybackInfo(w, r)
}

func (a *App) requestPlaybackCandidate(r *http.Request, client *UpstreamClient, originalID string, query url.Values, body map[string]any) (any, error) {
	req := requestContextFrom(r.Context())
	if req == nil || req.PlaybackNegotiation == nil {
		return client.RequestJSON(r.Context(), req, a.Identity, r.Method, "/Items/"+originalID+"/PlaybackInfo", query, body)
	}
	n := req.PlaybackNegotiation
	selected := client.resolveIdentitySnapshot(req, a.Identity, nil)
	auth := client.authSnapshot()
	binding, err := a.PlaybackIdentities.reserve(playbackAttemptRequest{Owner: n.owner, ServerID: client.ID, VirtualItemID: r.PathValue("itemId"), Identity: selected, Principal: auth.Compatibility.Principal, ConfigRevision: auth.Compatibility.ConfigRevision})
	if err != nil {
		return nil, strictPlaybackPreparationError(err)
	}
	n.pending = append(n.pending, binding)
	boundCtx := *req
	boundCtx.Playback = &binding
	payload, err := client.RequestJSON(r.Context(), &boundCtx, a.Identity, r.Method, "/Items/"+originalID+"/PlaybackInfo", query, body)
	if err != nil {
		return nil, err
	}
	data, ok := payload.(map[string]any)
	if !ok {
		return nil, hopQueryError("playback-invalid-response")
	}
	session := playbackStringField(data, "PlaySessionId")
	for index, source := range asItems(map[string]any{"Items": data["MediaSources"]}) {
		candidate := binding
		sourceID := playbackStringField(source, "Id")
		if index > 0 {
			candidate, err = a.PlaybackIdentities.reserve(playbackAttemptRequest{Owner: n.owner, ServerID: client.ID, VirtualItemID: r.PathValue("itemId"), SourceID: sourceID, Identity: binding.Identity, Principal: binding.Auth.Principal, ConfigRevision: binding.Auth.ConfigRevision})
			if err != nil {
				return nil, strictPlaybackPreparationError(err)
			}
			n.pending = append(n.pending, candidate)
		} else if err = a.PlaybackIdentities.setPendingSource(candidate.AttemptID, n.owner, sourceID); err != nil {
			return nil, strictPlaybackPreparationError(err)
		}
		if err = a.PlaybackIdentities.setPendingReservation(candidate.AttemptID, n.owner, n.reservation); err != nil {
			return nil, strictPlaybackPreparationError(err)
		}
		if session != "" {
			if _, err = a.PlaybackIdentities.bindAlias(candidate.AttemptID, n.owner, session); err != nil {
				return nil, strictPlaybackPreparationError(err)
			}
		}
		capability := a.issuePlaybackCapability(candidate.AttemptID, req.ProxyToken)
		if capability == "" {
			return nil, hopQueryError("playback-capacity")
		}
		delete(source, "EioFallbackUrl")
		delete(source, playbackDirectStreamUnavailableKey)
		source["EioAttemptId"] = candidate.AttemptID
		source["EioPlaybackToken"] = capability
		source["EioPlaySessionId"] = session
	}
	return payload, nil
}

func strictPlaybackURL(raw, attempt, capability string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host != "" || parsed.Scheme != "" || !strings.HasPrefix(parsed.Path, "/") {
		return "", hopQueryError("playback-unbound-url")
	}
	parsed.Path = "/eio/playback/v1/" + attempt + parsed.Path
	parsed.RawPath = ""
	fields := strings.Split(parsed.RawQuery, "&")
	kept := make([]string, 0, len(fields)+1)
	for _, field := range fields {
		if field == "" {
			continue
		}
		key, _, _ := strings.Cut(field, "=")
		decoded, decodeErr := url.QueryUnescape(key)
		if decodeErr != nil {
			return "", hopQueryError("invalid-resource-query")
		}
		if !isProxyCredentialQueryKey(decoded) {
			kept = append(kept, field)
		}
	}
	kept = append(kept, "api_key="+url.QueryEscape(capability))
	parsed.RawQuery = strings.Join(kept, "&")
	return parsed.String(), nil
}

func (a *App) commitPlaybackNegotiation(r *http.Request, sources []map[string]any) error {
	req := requestContextFrom(r.Context())
	if req == nil || req.PlaybackNegotiation == nil {
		return nil
	}
	n := req.PlaybackNegotiation
	if len(sources) == 0 {
		return hopQueryError("playback-unbound-source")
	}
	for _, source := range sources {
		attempt, cap := playbackStringField(source, "EioAttemptId"), playbackStringField(source, "EioPlaybackToken")
		if attempt == "" || cap == "" {
			return hopQueryError("playback-unbound-source")
		}
		for _, key := range []string{"DirectStreamUrl", "TranscodingUrl"} {
			if raw := playbackStringField(source, key); raw != "" {
				bound, err := strictPlaybackURL(raw, attempt, cap)
				if err != nil {
					return err
				}
				source[key] = bound
			}
		}
		if playbackStringField(source, "DirectStreamUrl") == "" && source[playbackDirectStreamUnavailableKey] != true {
			fallback := "/Videos/" + r.PathValue("itemId") + "/stream." + directStreamContainer("", source)
			bound, err := strictPlaybackURL(fallback, attempt, cap)
			if err != nil {
				return err
			}
			source["EioFallbackUrl"] = bound
		}
		if streams, ok := source["MediaStreams"].([]any); ok {
			for _, raw := range streams {
				if stream, ok := raw.(map[string]any); ok {
					if delivery := playbackStringField(stream, "DeliveryUrl"); delivery != "" {
						bound, err := strictPlaybackURL(delivery, attempt, cap)
						if err != nil {
							return err
						}
						stream["DeliveryUrl"] = bound
					}
				}
			}
		}
		// Http Path is not a media fallback; don't leak the upstream's address.
		if source["Protocol"] == "Http" {
			delete(source, "Path")
		}
	}
	used := map[string]bool{}
	for _, source := range sources {
		used[playbackStringField(source, "EioAttemptId")] = true
	}
	for _, binding := range n.pending {
		if !used[binding.AttemptID] {
			_ = a.PlaybackIdentities.fail(binding.AttemptID, n.owner)
			continue
		}
		if _, err := a.PlaybackIdentities.resolve(binding.AttemptID, n.owner); err != nil {
			return strictPlaybackPreparationError(err)
		}
		// Unselected candidates remain pending with the short TTL. Strict ingress
		// activates only the candidate that is actually used.
	}
	if r.Context().Err() != nil {
		return r.Context().Err()
	}
	n.committed = true
	return nil
}

func strictPlaybackPreparationError(err error) *outboundPreparationError {
	kind, status := "playback-identity-unavailable", http.StatusConflict
	if err == ErrPlaybackIdentityCapacity {
		kind, status = "playback-capacity", http.StatusServiceUnavailable
	}
	return &outboundPreparationError{Status: status, Kind: kind, Field: "attempt", Message: "playback identity requires renegotiation"}
}
