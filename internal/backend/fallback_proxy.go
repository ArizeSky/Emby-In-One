package backend

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

var fallbackVirtualIDPattern = regexp.MustCompile(`(?i)[a-f0-9]{32}`)

func (a *App) handleFallbackProxy(w http.ResponseWriter, r *http.Request) {
	reqCtx := requestContextFrom(r.Context())
	targetClient, rewrittenPath, serverIndex, query, ambiguous := a.resolveFallbackTarget(r, reqCtx)
	if targetClient == nil {
		if ambiguous {
			if a.Logger != nil {
				a.Logger.Warnf("Fallback: ambiguous target for %s %s", r.Method, r.URL.Path)
			}
			writeJSON(w, http.StatusBadRequest, map[string]any{"message": "Ambiguous upstream target"})
			return
		}
		if a.Logger != nil {
			a.Logger.Warnf("Fallback: no upstream available for %s %s", r.Method, r.URL.Path)
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "No upstream servers available"})
		return
	}
	if a.Logger != nil {
		a.Logger.Debugf("Fallback: %s %s → [%s] %s", r.Method, r.URL.Path, targetClient.Name, rewrittenPath)
	}

	// The user segment and the api_key are handled by the shared URL preparation
	// layer in doRequest, which normalizes the current user for supported
	// endpoints and keeps an unclassified value as the client sent it. A blanket
	// string replacement here would also rewrite unrelated text.

	body, err := decodeFallbackBody(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"message": "Invalid request body"})
		return
	}
	resp, err := a.performUpstreamRequest(r, targetClient, r.Method, rewrittenPath, query, body)
	if err != nil {
		if writePreparationError(w, err) {
			return
		}
		if a.Logger != nil {
			a.Logger.Errorf("Fallback error: %s %s - %s", r.Method, r.URL.Path, redactURLInError(err))
		}
		writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Upstream request failed"})
		return
	}
	defer resp.Body.Close()

	copySelectedHeaders(w.Header(), resp.Header, []string{"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Cache-Control", "ETag", "Last-Modified", "Content-Disposition"})
	contentType := resp.Header.Get("Content-Type")

	// For successful responses with binary (non-text, non-JSON) content types,
	// stream directly to avoid buffering large image/audio/video bodies.
	// Text types and error responses are always buffered for ID rewriting / HTML sanitisation.
	mediaLower := strings.ToLower(contentType)
	if resp.StatusCode < http.StatusBadRequest && contentType != "" &&
		!isJSONContentType(contentType) && !strings.HasPrefix(mediaLower, "text/") {
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
		return
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Failed to read upstream response"})
		return
	}
	if resp.StatusCode >= http.StatusBadRequest {
		if a.Logger != nil {
			a.Logger.Debugf("Fallback upstream error: %d for %s %s", resp.StatusCode, r.Method, r.URL.Path)
		}
		if looksLikeHTMLDocument(bodyBytes, contentType) {
			w.Header().Del("Content-Length")
			writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Upstream returned HTML error page"})
			return
		}
	}
	if looksLikeJSON(bodyBytes) {
		var payload any
		if err := json.Unmarshal(bodyBytes, &payload); err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Invalid upstream JSON"})
			return
		}
		cfg := a.ConfigStore.Snapshot()
		rewriteResponseIDs(payload, serverIndex, a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
		writeJSON(w, resp.StatusCode, payload)
		return
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(bodyBytes)
}

func (a *App) resolveFallbackTarget(r *http.Request, reqCtx *RequestContext) (*UpstreamClient, string, int, url.Values, bool) {
	query := cloneValues(r.URL.Query())
	rewrittenPath := r.URL.Path
	serverIndex := -1
	var targetClient *UpstreamClient

	for _, candidate := range fallbackVirtualIDPattern.FindAllString(r.URL.Path, -1) {
		if resolved := a.IDStore.ResolveVirtualID(candidate); resolved != nil {
			// Try primary
			if a.isServerAllowed(reqCtx, resolved.ServerIndex) {
				if client := a.Upstream.GetClient(resolved.ServerIndex); client != nil && client.IsOnline() {
					targetClient = client
					serverIndex = resolved.ServerIndex
					rewrittenPath = strings.ReplaceAll(rewrittenPath, candidate, resolved.OriginalID)
					break
				}
			}
			// Primary offline — try OtherInstances
			for _, other := range resolved.OtherInstances {
				if a.isServerAllowed(reqCtx, other.ServerIndex) {
					if client := a.Upstream.GetClient(other.ServerIndex); client != nil && client.IsOnline() {
						targetClient = client
						serverIndex = other.ServerIndex
						rewrittenPath = strings.ReplaceAll(rewrittenPath, candidate, other.OriginalID)
						break
					}
				}
			}
			if targetClient != nil {
				break
			}
		}
	}

	if rewritten, idx, found := rewriteIDQueryValues(query, a.IDStore); found {
		query = url.Values(rewritten)
		if targetClient == nil {
			if a.isServerAllowed(reqCtx, idx) {
				if client := a.Upstream.GetClient(idx); client != nil && client.IsOnline() {
					targetClient = client
					serverIndex = idx
				}
			}
		}
	} else {
		query = url.Values(rewritten)
	}

	if targetClient == nil {
		online := a.allowedClients(reqCtx)
		if len(online) == 0 {
			return nil, rewrittenPath, serverIndex, query, false
		}
		if len(online) > 1 {
			return nil, rewrittenPath, serverIndex, query, true
		}
		targetClient = online[0]
		serverIndex = targetClient.ServerIndex
	}
	return targetClient, rewrittenPath, serverIndex, query, false
}

// decodeFallbackBody reads the client body once. A JSON-declared body is decoded
// into a structure; anything else keeps its original bytes untouched, because
// trimming or re-encoding a non-JSON payload would corrupt binary or signed
// content. The trim is used only to answer "was this body empty?".
func decodeFallbackBody(r *http.Request) (any, error) {
	if r.Body == nil || r.Method == http.MethodGet || r.Method == http.MethodHead {
		return nil, nil
	}
	defer r.Body.Close()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	if !isExplicitJSONContentType(r.Header.Get("Content-Type")) {
		return &rawRequestBody{data: raw, contentType: r.Header.Get("Content-Type")}, nil
	}
	var payload any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

func (a *App) performUpstreamRequest(r *http.Request, client *UpstreamClient, method, path string, query url.Values, body any) (*http.Response, error) {
	reqCtx := requestContextFrom(r.Context())
	return client.doRequest(r.Context(), reqCtx, method, path, query, body, client.requestHeaders(reqCtx, a.Identity), false)
}

func isJSONContentType(contentType string) bool {
	if contentType == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = contentType
	}
	return strings.Contains(strings.ToLower(mediaType), "json")
}

func isExplicitJSONContentType(contentType string) bool {
	return isJSONContentType(contentType)
}

func copySelectedHeaders(dst, src http.Header, keys []string) {
	for _, key := range keys {
		if value := src.Get(key); value != "" {
			dst.Set(key, value)
		}
	}
}

func looksLikeJSON(body []byte) bool {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return false
	}
	return trimmed[0] == '{' || trimmed[0] == '['
}

func looksLikeHTMLDocument(body []byte, contentType string) bool {
	mediaType := strings.ToLower(strings.TrimSpace(contentType))
	if mediaType != "" {
		parsed, _, err := mime.ParseMediaType(mediaType)
		if err == nil {
			mediaType = parsed
		}
		if mediaType == "text/html" || mediaType == "application/xhtml+xml" {
			return true
		}
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return false
	}
	leading := strings.ToLower(string(trimmed[:minInt(len(trimmed), 32)]))
	return strings.HasPrefix(leading, "<!doctype") || strings.HasPrefix(leading, "<html")
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
