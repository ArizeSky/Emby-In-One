package backend

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
)

var (
	errRelativeResourceWithoutBase = errors.New("relative resource without a base URL")
	errUnsupportedResourceScheme   = errors.New("unsupported resource scheme")
	errUnsupportedResourceURL      = errors.New("unsupported resource URL")
	errResourceStoreFull           = errors.New("stream resource store is full")
	errMissingResourceIdentity     = errors.New("missing stream resource identity")
)

func streamResourceUserID(reqCtx *RequestContext) string {
	if reqCtx == nil || reqCtx.ProxyUser == nil {
		return ""
	}
	if reqCtx.ProxyUser.UserID != "" {
		return reqCtx.ProxyUser.UserID
	}
	if reqCtx.ProxyUser.Role == "admin" {
		return "admin"
	}
	return ""
}

// resourceRewriteContext identifies the item a manifest belongs to so every
// rewritten resource can be re-authorized per request (plan §8.6.1).
type resourceRewriteContext struct {
	UserID          string
	VirtualItemID   string
	ServerID        string
	OriginalID      string
	TimelineSession string
	// ManifestKey identifies the authorized fetch before any redirect. Resource
	// resolution still uses the final URL; rotating CDN signatures do not move
	// a stable origin request or resource handle to a different playlist window.
	ManifestKey string
}

// RewriteM3U8ForItemWithResources rewrites fetchable manifest URIs to local
// handles for an authenticated request. Unsupported URI-bearing tags and
// non-http(s) resources fail closed instead of reaching the player directly.
func (a *App) RewriteM3U8ForItemWithResources(content, upstreamBase, proxyToken string, ctx resourceRewriteContext) (string, error) {
	if strings.TrimSpace(ctx.UserID) == "" {
		return "", errMissingResourceIdentity
	}
	baseURL, baseErr := url.Parse(upstreamBase)
	lines := strings.Split(content, "\n")
	type replacement struct{ line, start, end int }
	var replacements []replacement
	var entries []streamResourceEntry
	rolling := false
	ended := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#EXT-X-ENDLIST") {
			ended = true
		}
		if strings.HasPrefix(trimmed, "#EXT-X-MEDIA-SEQUENCE:") || strings.HasPrefix(trimmed, "#EXT-X-PART:") || strings.HasPrefix(trimmed, "#EXT-X-PRELOAD-HINT:") || strings.HasPrefix(trimmed, "#EXT-X-RENDITION-REPORT:") {
			rolling = true
		}
		var attrs []hlsURIAttribute
		if strings.HasPrefix(trimmed, "#") {
			var err error
			attrs, err = extractHLSURIAttributes(line)
			if err != nil {
				return "", err
			}
			if len(attrs) == 0 {
				continue
			}
		}
		resolveAndRecord := func(raw string, start, end int) error {
			resolved, err := url.Parse(strings.TrimSpace(raw))
			if err != nil {
				return err
			}
			if !resolved.IsAbs() {
				if baseErr != nil || baseURL == nil || !baseURL.IsAbs() {
					return errRelativeResourceWithoutBase
				}
				resolved = baseURL.ResolveReference(resolved)
			}
			if !strings.EqualFold(resolved.Scheme, "http") && !strings.EqualFold(resolved.Scheme, "https") {
				return errUnsupportedResourceScheme
			}
			if resolved.Host == "" || resolved.User != nil {
				return errUnsupportedResourceURL
			}
			entries = append(entries, streamResourceEntry{
				UserID: ctx.UserID, VirtualItemID: ctx.VirtualItemID, ServerID: ctx.ServerID,
				OriginalID: ctx.OriginalID, ResourceURL: resolved.String(), TimelineSession: ctx.TimelineSession,
			})
			replacements = append(replacements, replacement{i, start, end})
			return nil
		}
		if len(attrs) > 0 {
			for _, attr := range attrs {
				if err := resolveAndRecord(attr.value, attr.valueStart, attr.valueEnd); err != nil {
					return "", err
				}
			}
			continue
		}
		leading := len(line) - len(strings.TrimLeft(line, " \t\r"))
		start := leading
		end := len(strings.TrimRight(line, " \t\r"))
		if err := resolveAndRecord(trimmed, start, end); err != nil {
			return "", err
		}
	}
	manifestKey := ""
	if rolling && !ended {
		manifestKey = ctx.ManifestKey
		if manifestKey == "" && baseErr == nil && baseURL != nil {
			identity := *baseURL
			identity.Fragment = ""
			manifestKey = "url:" + identity.String()
		}
	}
	ids, ok := a.StreamResources.registerManifest(entries, manifestKey)
	if !ok {
		return "", errResourceStoreFull
	}
	for i := len(replacements) - 1; i >= 0; i-- {
		edit := replacements[i]
		local := "/Videos/" + ctx.VirtualItemID + "/eio-resource/" + ids[i]
		if proxyToken != "" {
			local += "?api_key=" + url.QueryEscape(proxyToken)
		}
		line := lines[edit.line]
		lines[edit.line] = line[:edit.start] + local + line[edit.end:]
	}
	return strings.Join(lines, "\n"), nil
}

// A full map is retryable capacity pressure, not an upstream protocol failure.
func writeStreamResourceRewriteError(w http.ResponseWriter, err error) {
	w.Header().Set("Cache-Control", "no-store")
	if errors.Is(err, errResourceStoreFull) {
		w.Header().Set("Retry-After", "5")
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "HLS resource capacity exhausted; retry later"})
		return
	}
	writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Failed to rewrite HLS manifest"})
}

// serveStreamResource re-checks the bound user, live server grant and actual
// instance rating for every authenticated resource request.
func (a *App) serveStreamResource(w http.ResponseWriter, r *http.Request, virtualItemID, opaqueID string) {
	opaqueID = streamResourceHandleID(opaqueID)
	reqCtx := requestContextFrom(r.Context())
	entry, ok := a.StreamResources.peek(opaqueID)
	if !ok {
		w.WriteHeader(http.StatusGone)
		return
	}
	if streamResourceUserID(reqCtx) == "" || streamResourceUserID(reqCtx) != entry.UserID || entry.VirtualItemID != virtualItemID {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	if !a.isServerAllowed(reqCtx, entry.ServerID) || !a.canUseAggregatedServer(reqCtx, entry.ServerID) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	client := a.Upstream.ClientByID(entry.ServerID)
	if client == nil || !client.IsOnline() {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	allowed, confirmed := a.checkInstanceParentalAccess(r, client, entry.OriginalID)
	if !allowed {
		if !confirmed {
			w.WriteHeader(http.StatusBadGateway)
		} else {
			w.WriteHeader(http.StatusForbidden)
		}
		return
	}
	if !a.StreamResources.touch(opaqueID) {
		w.WriteHeader(http.StatusGone)
		return
	}
	resp, err := a.fetchResourceFollowingRedirects(r, client, entry.ResourceURL)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		}
		if writePreparationError(w, err) {
			return
		}
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	contentType := resp.Header.Get("Content-Type")
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && isPlaylistResponse(contentType, entry.ResourceURL) {
		if r.Method == http.MethodHead {
			// HEAD must answer with the manifest headers only; the body is never
			// buffered or rewritten downstream.
			copyStreamResponseHeaders(w, resp)
			w.WriteHeader(resp.StatusCode)
			return
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, maxHLSManifestSize+1))
		if rerr != nil || len(body) > maxHLSManifestSize {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		base := entry.ResourceURL
		if resp.Request != nil && resp.Request.URL != nil {
			base = resp.Request.URL.String()
		}
		rewritten, rerr := a.RewriteM3U8ForItemWithResources(string(body), base, reqCtx.ProxyToken, resourceRewriteContext{
			UserID:          entry.UserID,
			VirtualItemID:   entry.VirtualItemID,
			ServerID:        entry.ServerID,
			OriginalID:      entry.OriginalID,
			TimelineSession: entry.TimelineSession,
			ManifestKey:     "resource:" + opaqueID,
		})
		if rerr != nil {
			writeStreamResourceRewriteError(w, rerr)
			return
		}
		storeHLSSegmentPositions(entry.TimelineSession, "", rewritten)
		w.Header().Set("Content-Type", "application/x-mpegURL")
		w.Header().Del("Content-Length")
		w.WriteHeader(resp.StatusCode)
		_, _ = io.WriteString(w, rewritten)
		return
	}
	if r.Method == http.MethodHead {
		copyStreamResponseHeaders(w, resp)
		w.WriteHeader(resp.StatusCode)
		return
	}
	resourceURL, _ := url.Parse(entry.ResourceURL)
	originalPath := ""
	if resourceURL != nil {
		originalPath = resourceURL.Path
	}
	body, rewritten, readErr := rewriteHLSSegmentBody(r, resp, entry.TimelineSession, "eio-resource/"+opaqueID, originalPath)
	if readErr != nil {
		w.WriteHeader(http.StatusBadGateway)
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

// streamResourceHandleID strips the cosmetic extension suffix from a resource
// route. The suffix exists only so clients can sniff the container; the opaque
// handle itself is hex, so a trailing safe extension is never part of it.
func streamResourceHandleID(raw string) string {
	if dot := strings.LastIndexByte(raw, '.'); dot > 0 {
		if sanitizeStreamExtension(raw[dot+1:]) != "" {
			return raw[:dot]
		}
	}
	return raw
}

// fetchResourceRedirectTarget is the method-preserving variant of
// UpstreamClient.fetchRedirectTarget: resource requests keep HEAD and Range
// semantics instead of always issuing a GET. Credential rules are identical —
// the upstream's own token is attached only on a canonical self-origin hop.
func (c *UpstreamClient) fetchResourceRedirectTarget(ctx context.Context, method, rawURL string, extraHeaders http.Header) (*http.Response, error) {
	if c.Config.SpoofClient == "resident" && c.residentIdentity == nil {
		return nil, newResidentIdentityMissingError()
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return nil, err
	}
	// Resource URLs bypass doRequestOnce, but self-origin media still needs the
	// same configured identity and consistent credential snapshot as API calls.
	// On a CDN hop retain the public UA and transport headers only.
	headers := http.Header{}
	if c.isSelfURL(rawURL) {
		headers = cloneHeader(extraHeaders)
		c.applyResidentIdentity(headers)
		headers = prepareOutboundHeaders(headers, requestContextFrom(ctx), c.authSnapshot(), authModeNormal)
	} else {
		for _, name := range []string{"User-Agent", "Range", "Accept", "Accept-Encoding", "Accept-Language"} {
			if values := extraHeaders.Values(name); len(values) > 0 {
				headers[name] = append([]string(nil), values...)
			}
		}
	}
	for key, values := range headers {
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	client := &http.Client{Transport: c.transport, Timeout: 0, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, doErr := client.Do(req)
	if doErr != nil {
		return nil, &redactedError{err: doErr}
	}
	return resp, nil
}

// fetchResourceFollowingRedirects follows bounded redirects and only attaches
// upstream credentials on same-origin hops. Range headers are preserved.
func (a *App) fetchResourceFollowingRedirects(r *http.Request, client *UpstreamClient, rawURL string) (*http.Response, error) {
	headers := client.identityHeaders(requestContextFrom(r.Context()), a.Identity)
	for key, values := range streamRequestHeaders(r) {
		headers[key] = values
	}
	method := http.MethodGet
	if r.Method == http.MethodHead {
		// A HEAD must reach the upstream as HEAD; sending a GET on behalf of a
		// HEAD client and closing its body unread wastes the full response.
		method = http.MethodHead
	}
	resp, err := client.fetchResourceRedirectTarget(r.Context(), method, rawURL, headers)
	if err != nil {
		return nil, err
	}
	for hop := 0; isRedirectStatus(resp.StatusCode) && hop < maxStreamRedirectHops; hop++ {
		location := resp.Header.Get("Location")
		if location == "" {
			break
		}
		var base *url.URL
		if resp.Request != nil {
			base = resp.Request.URL
		}
		abs, rerr := resolveRedirectTarget(base, location)
		if rerr != nil || abs == nil {
			break
		}
		followed, ferr := client.fetchResourceRedirectTarget(r.Context(), method, abs.String(), headers)
		if ferr != nil {
			resp.Body.Close()
			return nil, ferr
		}
		resp.Body.Close()
		resp = followed
	}
	return resp, nil
}
