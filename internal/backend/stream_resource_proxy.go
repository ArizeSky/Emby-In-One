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
)

// resourceRewriteContext identifies the item a manifest belongs to so every
// rewritten resource can be re-authorized per request (plan §8.6.1).
type resourceRewriteContext struct {
	UserID        string
	VirtualItemID string
	ServerID      string
	OriginalID    string
	// ManifestKey identifies the authorized fetch before any redirect. Resource
	// resolution still uses the final URL; rotating CDN signatures do not move
	// a stable origin request or resource handle to a different playlist window.
	ManifestKey string
}

// RewriteM3U8ForItemWithResources rewrites every fetchable URI in a manifest to a
// local eio-resource handle for a restricted user. Unlike RewriteM3U8ForItem it
// also rewrites external and unrecognised URLs, because a restricted user must
// never receive a direct external URL that could bypass the proxy. A URI that
// cannot be resolved to an absolute http(s) URL is a hard error (the caller
// answers 502) rather than an untouched external link.
func (a *App) RewriteM3U8ForItemWithResources(content, upstreamBase, proxyToken string, ctx resourceRewriteContext) (string, error) {
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
		if strings.HasPrefix(trimmed, "#EXT-X-MEDIA-SEQUENCE:") || strings.HasPrefix(trimmed, "#EXT-X-PART:") {
			rolling = true
		}
		raw, start, end := trimmed, 0, len(line)
		if strings.HasPrefix(trimmed, "#") {
			uri, lo, hi, ok := extractQuotedURI(line)
			if !ok {
				continue
			}
			raw, start, end = uri, lo, hi
		}
		resolved, err := url.Parse(strings.TrimSpace(raw))
		if err != nil {
			return "", err
		}
		if !resolved.IsAbs() {
			if baseErr != nil || baseURL == nil || !baseURL.IsAbs() {
				return "", errRelativeResourceWithoutBase
			}
			resolved = baseURL.ResolveReference(resolved)
		}
		if resolved.Scheme != "http" && resolved.Scheme != "https" {
			return "", errUnsupportedResourceScheme
		}
		if resolved.Host == "" || resolved.User != nil {
			return "", errUnsupportedResourceURL
		}
		entries = append(entries, streamResourceEntry{UserID: ctx.UserID, VirtualItemID: ctx.VirtualItemID, ServerID: ctx.ServerID, OriginalID: ctx.OriginalID, ResourceURL: resolved.String()})
		replacements = append(replacements, replacement{i, start, end})
	}
	manifestKey := ""
	if rolling && !ended {
		manifestKey = ctx.ManifestKey
		if manifestKey == "" && baseErr == nil && baseURL != nil {
			identity := *baseURL
			identity.Fragment = ""
			// Query parameters can select audio tracks and renditions. Keep their
			// semantics when no stable pre-redirect identity was supplied.
			manifestKey = "url:" + identity.String()
		}
	}
	ids, ok := a.StreamResources.registerManifest(entries, manifestKey)
	if !ok {
		return "", errResourceStoreFull
	}
	for i, edit := range replacements {
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
	if errors.Is(err, errResourceStoreFull) {
		w.Header().Set("Retry-After", "5")
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"message": "HLS resource capacity exhausted; retry later"})
		return
	}
	writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Failed to rewrite HLS manifest"})
}

// serveStreamResource serves one rewritten HLS resource for a restricted user. It
// re-checks the bound user, the live server grant and the actual instance rating
// on every request (plan §8.6.1), then relays the bytes. An unknown or idle-
// expired handle is a 410; a cross-user handle is a 403.
func (a *App) serveStreamResource(w http.ResponseWriter, r *http.Request, virtualItemID, opaqueID string) {
	reqCtx := requestContextFrom(r.Context())
	entry, ok := a.StreamResources.peek(opaqueID)
	if !ok {
		w.WriteHeader(http.StatusGone)
		return
	}
	if reqCtx == nil || reqCtx.ProxyUser == nil || reqCtx.ProxyUser.UserID != entry.UserID || entry.VirtualItemID != virtualItemID {
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
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	contentType := resp.Header.Get("Content-Type")
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && isPlaylistResponse(contentType, entry.ResourceURL) {
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, maxHLSManifestSize+1))
		if rerr != nil || len(body) > maxHLSManifestSize {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		base := entry.ResourceURL
		if resp.Request != nil && resp.Request.URL != nil {
			base = resp.Request.URL.String()
		}
		token := ""
		if reqCtx != nil {
			token = reqCtx.ProxyToken
		}
		rewritten, rerr := a.RewriteM3U8ForItemWithResources(string(body), base, token, resourceRewriteContext{
			UserID:        entry.UserID,
			VirtualItemID: entry.VirtualItemID,
			ServerID:      entry.ServerID,
			OriginalID:    entry.OriginalID,
			ManifestKey:   "resource:" + opaqueID,
		})
		if rerr != nil {
			writeStreamResourceRewriteError(w, rerr)
			return
		}
		w.Header().Set("Content-Type", "application/x-mpegURL")
		w.WriteHeader(resp.StatusCode)
		_, _ = io.WriteString(w, rewritten)
		return
	}
	copyStreamResponseHeaders(w, resp)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// fetchResourceFollowingRedirects fetches a resource URL, following up to
// maxStreamRedirectHops hops and attaching the upstream credential only on
// same-origin hops (fetchRedirectTarget handles that). Range and conditional
// headers from the client are preserved.
func (a *App) fetchResourceFollowingRedirects(r *http.Request, client *UpstreamClient, rawURL string) (*http.Response, error) {
	headers := streamRequestHeaders(r)
	resp, err := client.fetchRedirectTarget(r.Context(), rawURL, headers)
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
		followed, ferr := client.fetchRedirectTarget(r.Context(), abs.String(), headers)
		if ferr != nil {
			resp.Body.Close()
			return nil, ferr
		}
		resp.Body.Close()
		resp = followed
	}
	return resp, nil
}
