package backend

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

var fallbackVirtualIDPattern = regexp.MustCompile(`(?i)[a-f0-9]{32}`)

// maxBufferedUpstreamBytes bounds how much of an upstream response the proxy will
// hold in memory when it has to buffer the body (JSON/ID rewriting, HTML error
// detection, error messages). Streaming paths are unaffected. The limit is well
// above any real Emby JSON response but far below what could exhaust the process.
const maxBufferedUpstreamBytes = 32 << 20 // 32 MiB

// readAllLimited reads r fully but refuses to buffer more than limit bytes.
func readAllLimited(r io.Reader, limit int64) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("upstream response exceeds the %d-byte buffer limit", limit)
	}
	return body, nil
}

func (a *App) handleFallbackProxy(w http.ResponseWriter, r *http.Request) {
	reqCtx := requestContextFrom(r.Context())
	// A parental-restricted user must not be handed content or an unknown binary
	// through the generic fallback proxy (plan §8.7). Only explicit control
	// endpoints pass; anything else is refused before any upstream request.
	if maxR, blockUnrated := a.parentalPolicyFor(r); maxR > 0 || blockUnrated {
		if !parentalFallbackAllowed(r.Method, r.URL.Path) {
			writeParentalRouteNotSupported(w)
			return
		}
	}
	// A collection filter that names local per-user state (watched/unplayed/
	// resumable) cannot be honoured by forwarding to the shared upstream
	// account: the upstream would pre-filter with its own shared state, so a
	// UserData overlay can never repair the result set. A restricted user gets
	// an explicit refusal instead of a silently wrong list.
	if isUserRestricted(r) && fallbackCarriesLocalStateFilters(r.URL.Query()) {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"message": "LocalUserStateFilterNotSupportedOnFallback",
			"code":    "LocalUserStateFilterNotSupportedOnFallback",
		})
		return
	}
	targetClient, rewrittenPath, serverID, query, ambiguous := a.resolveFallbackTarget(r, reqCtx)
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

	// Content-Length is deliberately not copied here. Every buffered path below
	// re-serializes the payload (JSON is re-encoded after ID rewriting, HTML errors are
	// replaced), so the upstream length no longer describes what we are about to write.
	// net/http honours an explicitly set Content-Length verbatim: a longer body gets
	// silently truncated and a shorter one leaves the client waiting for bytes that
	// never arrive. Each path below is responsible for its own length: the byte-exact
	// passthrough re-copies it, the buffered paths let net/http compute it.
	// The header set a restricted caller may keep is narrower: the upstream's
	// validators and shared cache policy cannot describe a per-user body.
	fallbackHeaders := []string{"Content-Type", "Content-Range", "Accept-Ranges", "Cache-Control", "ETag", "Last-Modified", "Content-Disposition"}
	if isUserRestricted(r) {
		fallbackHeaders = []string{"Content-Type", "Content-Range", "Accept-Ranges", "Content-Disposition"}
	}
	copySelectedHeaders(w.Header(), resp.Header, fallbackHeaders)
	contentType := resp.Header.Get("Content-Type")

	// For successful responses with binary (non-text, non-JSON) content types,
	// stream directly to avoid buffering large image/audio/video bodies.
	// Text types and error responses are always buffered for ID rewriting / HTML sanitisation.
	mediaLower := strings.ToLower(contentType)
	if resp.StatusCode < http.StatusBadRequest && contentType != "" &&
		!isJSONContentType(contentType) && !strings.HasPrefix(mediaLower, "text/") {
		// io.Copy forwards the upstream bytes untouched, so the upstream length still
		// applies and can be passed through for exact framing.
		if contentLength := resp.Header.Get("Content-Length"); contentLength != "" {
			w.Header().Set("Content-Length", contentLength)
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
		return
	}

	bodyBytes, err := readAllLimited(resp.Body, maxBufferedUpstreamBytes)
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
		rewriteResponseIDs(payload, serverID, a.IDStore, cfg.Server.ID, a.clientFacingUserIDFor(r))
		if err := a.sanitizeResponseUserData(r, payload); err != nil {
			a.writeLocalWatchReadError(w, err)
			return
		}
		if isUserRestricted(r) {
			w.Header().Set("Cache-Control", "private,no-store")
		}
		writeJSON(w, resp.StatusCode, payload)
		return
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(bodyBytes)
}

// fallbackQueryHit records one resolvable virtual ID found in the query string.
// source marks MediaSourceId/PlaySessionId/SessionId, which name a specific
// server's source or session and must stay pinned to that server.
type fallbackQueryHit struct {
	key      string
	index    int
	virtual  string
	resolved *ResolvedID
	source   bool
}

// resolveFallbackTarget picks the upstream for a generic passthrough request.
// Unlike the old one-shot rewriteIDQueryValues target inference, every known
// virtual ID in the path and query is validated against the chosen server, and
// a paused or unauthorized server is never used to explain an ID that belongs
// to it. Only a request with no known target at all keeps the old default
// (single online node, or ambiguous when several are available).
func (a *App) resolveFallbackTarget(r *http.Request, reqCtx *RequestContext) (*UpstreamClient, string, string, url.Values, bool) {
	query := cloneValues(r.URL.Query())
	rewrittenPath := r.URL.Path

	var pathIDs []string
	for _, candidate := range fallbackVirtualIDPattern.FindAllString(r.URL.Path, -1) {
		if a.IDStore.ResolveVirtualID(candidate) != nil {
			pathIDs = append(pathIDs, candidate)
		}
	}

	keys := make([]string, 0, len(query))
	for key := range query {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var queryHits []fallbackQueryHit
	for _, key := range keys {
		isSource := key == "PlaySessionId" || key == "SessionId" || strings.EqualFold(key, "MediaSourceId")
		_, isResource := simpleIDFields[key]
		if !isSource && !isResource {
			continue
		}
		for i, raw := range query[key] {
			resolved := a.IDStore.ResolveVirtualID(raw)
			if resolved == nil {
				continue
			}
			queryHits = append(queryHits, fallbackQueryHit{key: key, index: i, virtual: raw, resolved: resolved, source: isSource})
		}
	}

	if len(pathIDs) == 0 && len(queryHits) == 0 {
		// No known target: preserve the original default selection.
		online := a.allowedClients(reqCtx)
		if len(online) == 0 {
			return nil, rewrittenPath, "", query, false
		}
		if len(online) > 1 {
			return nil, rewrittenPath, "", query, true
		}
		return online[0], rewrittenPath, online[0].ID, query, false
	}

	// A source/session ID pins the target to the server that owns it, and that
	// server must itself be enabled and online; we never re-interpret such an ID
	// on a different server.
	forcedServer := ""
	for _, hit := range queryHits {
		if !hit.source {
			continue
		}
		if !a.serverUsableForAggregation(reqCtx, hit.resolved.ServerID) {
			return nil, rewrittenPath, "", query, false
		}
		if forcedServer != "" && forcedServer != hit.resolved.ServerID {
			return nil, rewrittenPath, "", query, false
		}
		forcedServer = hit.resolved.ServerID
	}

	target := forcedServer
	if target == "" {
		// Choose the first enabled, online, permitted server (in config order)
		// that can translate every known resource ID to itself.
		cfg := a.ConfigStore.Snapshot()
		for _, upstream := range cfg.Upstream {
			if !a.serverUsableForAggregation(reqCtx, upstream.ID) {
				continue
			}
			if a.serverCanTranslateFallbackIDs(upstream.ID, pathIDs, queryHits) {
				target = upstream.ID
				break
			}
		}
		if target == "" {
			return nil, rewrittenPath, "", query, false
		}
	}

	if !a.serverCanTranslateFallbackIDs(target, pathIDs, queryHits) {
		return nil, rewrittenPath, "", query, false
	}

	for _, id := range pathIDs {
		if original, ok := translateVirtualIDForServer(id, target, a.IDStore); ok {
			rewrittenPath = strings.ReplaceAll(rewrittenPath, id, original)
		}
	}
	for _, hit := range queryHits {
		if original, ok := translateVirtualIDForServer(hit.virtual, target, a.IDStore); ok {
			query[hit.key][hit.index] = original
		}
	}

	client := a.Upstream.ClientByID(target)
	if client == nil || !client.IsOnline() {
		return nil, rewrittenPath, "", query, false
	}
	return client, rewrittenPath, target, query, false
}

// serverUsableForAggregation reports whether a server may serve content for this
// request: aggregation on, account grant present, and currently online.
func (a *App) serverUsableForAggregation(reqCtx *RequestContext, serverID string) bool {
	if !a.canUseAggregatedServer(reqCtx, serverID) {
		return false
	}
	client := a.Upstream.ClientByID(serverID)
	return client != nil && client.IsOnline()
}

// serverCanTranslateFallbackIDs reports whether every known path and query ID
// can be translated to the given server.
func (a *App) serverCanTranslateFallbackIDs(serverID string, pathIDs []string, queryHits []fallbackQueryHit) bool {
	for _, id := range pathIDs {
		if _, ok := translateVirtualIDForServer(id, serverID, a.IDStore); !ok {
			return false
		}
	}
	for _, hit := range queryHits {
		if _, ok := translateVirtualIDForServer(hit.virtual, serverID, a.IDStore); !ok {
			return false
		}
	}
	return true
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

// playbackLimiterKey reports the key the concurrent-playback limiter uses for this
// request, or ok == false when no slot applies: admins are exempt, the limiter may be
// disabled, and a request without a resolved proxy user or server cannot be counted.
// TryStart and the failure paths that must undo it share this predicate so the two can
// never drift apart and leave a slot taken that nothing releases.
func (a *App) playbackLimiterKey(reqCtx *RequestContext, r *http.Request, serverID string) (userID string, ok bool) {
	if reqCtx == nil || reqCtx.ProxyUser == nil || reqCtx.ProxyUser.Role == "admin" {
		return "", false
	}
	if a.PlaybackLimiter == nil || serverID == "" {
		return "", false
	}
	cfg := a.ConfigStore.Snapshot()
	found := false
	for _, u := range cfg.Upstream {
		if u.ID == serverID {
			found = true
			break
		}
	}
	if !found {
		return "", false
	}
	return reqCtx.ProxyUser.UserID, true
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

// localStateFilterTokens are Filters= values that name per-user watch state.
var localStateFilterTokens = []string{"isplayed", "isunplayed", "isresumable", "hasplayed"}

// fallbackCarriesLocalStateFilters reports whether a passthrough query asks for
// local per-user watch filtering, which the shared-account upstream cannot
// honour for a restricted user.
func fallbackCarriesLocalStateFilters(query url.Values) bool {
	for _, raw := range query["Filters"] {
		for _, token := range strings.Split(strings.ToLower(raw), ",") {
			token = strings.TrimSpace(token)
			for _, match := range localStateFilterTokens {
				if token == match {
					return true
				}
			}
		}
	}
	return false
}
