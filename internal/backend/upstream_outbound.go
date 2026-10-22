package backend

import (
	"fmt"
	"net/http"
	"time"
)

// redirectPolicy returns the redirect handling for an upstream. Following is the
// default; the returned CheckRedirect still caps the chain at 10 hops (net/http's
// own limit) and, crucially, strips credentials before a redirect that leaves the
// original origin. net/http only drops Authorization/Cookie on a cross-host hop;
// this proxy carries its upstream credential in custom headers (X-Emby-Token,
// X-Emby-Authorization) and in the api_key query, which net/http would otherwise
// forward verbatim to whatever host the upstream points at. When the
// administrator turns following off, the request stops at the upstream's redirect
// and is reported as an upstream failure: the proxy never hands an upstream
// redirect target to the client as a successful response.
func redirectPolicy(follow bool) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if !follow {
			return errUpstreamRedirectNotFollowed
		}
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		origin := via[0].URL
		if req.URL.Host != origin.Host || req.URL.Scheme != origin.Scheme {
			for _, header := range []string{
				"X-Emby-Token", "X-Emby-Authorization", "X-MediaBrowser-Token",
				"Authorization", "Cookie", "Cookie2",
			} {
				req.Header.Del(header)
			}
			if req.URL.RawQuery != "" {
				query := req.URL.Query()
				query.Del("api_key")
				query.Del("ApiKey")
				req.URL.RawQuery = query.Encode()
			}
		}
		return nil
	}
}

// getAccessToken returns the upstream's access token in a thread-safe manner.
func (c *UpstreamClient) getAccessToken() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.AccessToken
}

// healthCheckTimeout bounds a single health-check login. c.timeouts is written once
// at construction, so it needs no lock. Note the http.Client timeout still applies,
// so this can shorten a probe but never extend it past timeouts.API.
func (c *UpstreamClient) healthCheckTimeout() time.Duration {
	return time.Duration(c.timeouts.HealthCheck) * time.Millisecond
}

// loginTimeout bounds a login attempt or an API-key validation for this upstream.
func (c *UpstreamClient) loginTimeout() time.Duration {
	return time.Duration(c.timeouts.Login) * time.Millisecond
}

func (c *UpstreamClient) identityHeaders(reqCtx *RequestContext, identity *ClientIdentityService) http.Header {
	_, headers := c.resolveIdentityHeaders(reqCtx, identity, nil)
	return headers
}

func (c *UpstreamClient) resolveIdentityHeaders(reqCtx *RequestContext, identity *ClientIdentityService, override http.Header) (string, http.Header) {
	if c.Config.SpoofClient == "resident" {
		// A resident client presents its fixed identity for every request. It
		// never consults live request headers, captured tokens, the latest global
		// identity, per-server last-success, or a retry override.
		if c.residentIdentity == nil {
			return "resident-missing", http.Header{}
		}
		return c.residentIdentitySource(), cloneHeader(c.residentIdentity.Headers)
	}
	if c.Config.SpoofClient == "passthrough" {
		if hasPassthroughIdentity(override) {
			return "override", mergePassthroughHeaders(override)
		}
		if identity != nil {
			var live http.Header
			var token string
			if reqCtx != nil {
				live = reqCtx.Headers
				token = reqCtx.ProxyToken
			}
			resolved := identity.ResolvePassthroughHeadersForServer(live, token, c.serverKey)
			return resolved.Source, resolved.Headers
		}
		return "no-identity", http.Header{}
	}
	if c.Config.SpoofClient == "custom" {
		headers := http.Header{}
		profile := map[string]string{
			"User-Agent":            c.Config.CustomUserAgent,
			"X-Emby-Client":         c.Config.CustomClient,
			"X-Emby-Client-Version": c.Config.CustomClientVersion,
			"X-Emby-Device-Name":    c.Config.CustomDeviceName,
			"X-Emby-Device-Id":      c.Config.CustomDeviceId,
		}
		for key, value := range profile {
			if value != "" {
				headers.Set(key, value)
			}
		}
		return "custom", headers
	}
	// No built-in client identity exists any more: an unknown mode (or a
	// passthrough upstream without an identity service) resolves to no
	// identity at all instead of a fabricated client profile.
	return "no-identity", http.Header{}
}

// requestHeaders builds the caller-side header set for one request from the same
// auth snapshot it will be sent with.
func (c *UpstreamClient) requestHeaders(reqCtx *RequestContext, identity *ClientIdentityService) http.Header {
	return c.requestHeadersWithOverride(reqCtx, identity, nil)
}

// requestHeadersWithOverride is requestHeaders for a login retry whose identity
// is pinned by the caller (the captured headers that triggered the retry)
// rather than resolved from the request context.
func (c *UpstreamClient) requestHeadersWithOverride(reqCtx *RequestContext, identity *ClientIdentityService, override http.Header) http.Header {
	if c.Config.SpoofClient == "passthrough" {
		return c.passthroughRequestHeadersWithOverride(reqCtx, identity, override)
	}
	headers := c.identityHeaders(reqCtx, identity)
	if snapshot := c.authSnapshot(); snapshot.AccessToken != "" {
		headers.Set("X-Emby-Token", snapshot.AccessToken)
	}
	return headers
}

// passthroughRequestHeaders resolves the identity a passthrough upstream should
// see and installs that snapshot's credential. The device parameters are kept in
// the compound header so the upstream still receives the client's real device
// identity; its UserId and Token are replaced by the snapshot's own.
func (c *UpstreamClient) passthroughRequestHeaders(reqCtx *RequestContext, identity *ClientIdentityService) http.Header {
	return c.passthroughRequestHeadersWithOverride(reqCtx, identity, nil)
}

func (c *UpstreamClient) passthroughRequestHeadersWithOverride(reqCtx *RequestContext, identity *ClientIdentityService, override http.Header) http.Header {
	_, headers := c.resolveIdentityHeaders(reqCtx, identity, override)
	snapshot := c.authSnapshot()
	if snapshot.AccessToken != "" {
		headers.Set("X-Emby-Token", snapshot.AccessToken)
	}
	compound := headers.Get("X-Emby-Authorization")
	if compound == "" {
		compound = headers.Get("Authorization")
	}
	headers.Del("Authorization")
	headers.Del("X-Emby-Authorization")
	if compound != "" {
		if parsed, ok := parseAuthorizationIdentityStrict(compound); ok {
			headers.Set("X-Emby-Authorization", canonicalAuthorizationHeader(snapshot.UserID,
				parsed["Device"], parsed["DeviceId"], parsed["Version"], parsed["Client"]))
		}
	}
	if headers.Get("X-Emby-Authorization") == "" && snapshot.UserID != "" {
		headers.Set("X-Emby-Authorization", canonicalAuthorizationHeader(snapshot.UserID, "", "", "", ""))
	}
	return headers
}

func (c *UpstreamClient) recordSuccessfulIdentity(identity *ClientIdentityService, source string, headers http.Header) {
	if identity == nil || c.Config.SpoofClient != "passthrough" || !hasPassthroughIdentity(headers) {
		return
	}
	identity.SaveLastSuccess(c.serverKey, headers)
	switch source {
	case "live-request", "captured-token", "captured-latest", "override":
		identity.SaveLatestCapturedHeaders(headers)
	}
}
