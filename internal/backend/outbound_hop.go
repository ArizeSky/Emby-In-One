package backend

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// This file is the single outbound-hop policy: how one request in a fetch chain
// is classified, which headers it may carry to that class of target, and which
// part of its query string is a credential. The automatic redirect policy
// (net/http CheckRedirect), the media handler's manual follow and the opaque
// resource fetch all project through here, so a hop repaired in one path cannot
// stay broken in another.

// outboundTargetClass names how one outbound target relates to the upstream's
// configured trust boundary. The decision uses the canonical origin — scheme,
// host and effective port — never the host alone, so an HTTPS upstream never
// extends credential trust to a plaintext URL that merely shares its host.
type outboundTargetClass string

const (
	// outboundTargetTrusted is one of the origins this upstream answers on (its
	// API base or a configured stream line). It may receive the selected client
	// identity and this request's current credential snapshot.
	outboundTargetTrusted outboundTargetClass = "trusted-origin"
	// outboundTargetExternal is every other http(s) target, a CDN included. It
	// receives the public header set only: no device identity, no compound
	// authorization, no token, no cookie.
	outboundTargetExternal outboundTargetClass = "external"
	// outboundTargetRejected is a target that must not be requested at all.
	outboundTargetRejected outboundTargetClass = "rejected"
)

// errUnsupportedOutboundTarget reports a hop whose target cannot be requested:
// an unparsable or non-http(s) URL, a URL without a host, or one carrying
// userinfo. It is built without the target so no URL reaches a log line.
var errUnsupportedOutboundTarget = errors.New("unsupported outbound target")

// outboundPublicHeaderNames is the complete set of header names a non-trusted
// target may receive: the client's transport headers plus the public
// User-Agent. Projection copies this allow-list instead of deleting known
// private headers, so a header added later cannot leak by omission.
var outboundPublicHeaderNames = []string{
	"User-Agent",
	"Range",
	"Accept",
	"Accept-Encoding",
	"Accept-Language",
	"If-Range",
	"If-None-Match",
	"If-Modified-Since",
	"If-Match",
}

// projectPublicOutboundHeaders returns the header set an external target may
// see: the public User-Agent plus the transport headers needed for media
// reads. Device identity, compound authorization, tokens and cookies are
// dropped. A Referer whose query carries a credential parameter has that
// parameter removed, because net/http builds it from the previous hop's URL.
func projectPublicOutboundHeaders(src http.Header) http.Header {
	projected := http.Header{}
	if src == nil {
		return projected
	}
	for _, name := range outboundPublicHeaderNames {
		if values := src.Values(name); len(values) > 0 {
			projected[name] = append([]string(nil), values...)
		}
	}
	if referer := projected.Get("Referer"); referer != "" {
		projected.Set("Referer", redactCredentialQueryInURL(referer))
	}
	return projected
}

// mergePublicOutboundHeaders copies only the public transport headers of src
// onto dst. Caller-supplied transport headers must never replace the identity
// fields projected from the resolved snapshot.
func mergePublicOutboundHeaders(dst, src http.Header) {
	if dst == nil || src == nil {
		return
	}
	for _, name := range outboundPublicHeaderNames {
		if name == "User-Agent" {
			continue // public for a CDN, but never a transport override
		}
		if values := src.Values(name); len(values) > 0 {
			dst[http.CanonicalHeaderKey(name)] = append([]string(nil), values...)
		}
	}
}

// redactCredentialQueryInURL returns rawURL with known credential query
// parameters removed and every other byte of the query preserved. An
// unparsable value is returned unchanged: a header must never be the path that
// turns an unparsable URL into a differently encoded one.
func redactCredentialQueryInURL(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.RawQuery == "" {
		return rawURL
	}
	rewritten, changed := rewriteRawQueryCredentials(parsed.RawQuery, "")
	if !changed {
		return rawURL
	}
	parsed.RawQuery = rewritten
	return parsed.String()
}

// classifyOutboundTarget classifies one target URL for this upstream.
func (c *UpstreamClient) classifyOutboundTarget(target *url.URL) outboundTargetClass {
	if target == nil || target.User != nil || target.Host == "" {
		return outboundTargetRejected
	}
	switch strings.ToLower(target.Scheme) {
	case "http", "https":
	default:
		return outboundTargetRejected
	}
	origin := canonicalURLOrigin(target)
	if origin == "" {
		return outboundTargetRejected
	}
	for _, self := range c.selfOrigins() {
		if origin == self {
			return outboundTargetTrusted
		}
	}
	return outboundTargetExternal
}

// rewriteRawQueryCredentials rewrites known credential parameters in a raw
// query string without re-encoding anything else: parameter order, repeated
// keys, escaping and space encoding of every other field stay byte-for-byte.
// A signature that covers the untouched query therefore keeps verifying.
//
// With a non-empty token the first credential field is rewritten to that token
// (the same auth snapshot the headers carry) and duplicates are dropped; with
// an empty token every credential field is removed. Nothing is added when the
// query carries no credential field at all, so a signed URL without one is
// passed through unchanged.
func rewriteRawQueryCredentials(rawQuery, token string) (string, bool) {
	if rawQuery == "" {
		return rawQuery, false
	}
	parts := strings.Split(rawQuery, "&")
	rewritten := make([]string, 0, len(parts))
	changed := false
	written := false
	for _, part := range parts {
		key := part
		value := ""
		if index := strings.IndexByte(part, '='); index >= 0 {
			key = part[:index]
			value = part[index+1:]
		}
		decodedKey, err := url.QueryUnescape(key)
		if err != nil || !isProxyCredentialQueryKey(decodedKey) {
			rewritten = append(rewritten, part)
			continue
		}
		if token == "" {
			changed = true
			continue
		}
		if written {
			changed = true
			continue
		}
		written = true
		if decodedValue, err := url.QueryUnescape(value); err == nil && decodedValue == token {
			rewritten = append(rewritten, part)
			continue
		}
		rewritten = append(rewritten, key+"="+url.QueryEscape(token))
		changed = true
	}
	if !changed {
		return rawQuery, false
	}
	return strings.Join(rewritten, "&"), true
}

// applyOutboundHopAuth installs the authentication carrier for one hop of a
// fetch from the same snapshot its URL and body were prepared with.
func (c *UpstreamClient) applyOutboundHopAuth(headers http.Header, reqCtx *RequestContext, auth upstreamAuthSnapshot) http.Header {
	return prepareOutboundHeaders(headers, reqCtx, auth, authModeNormal)
}

// outboundHop is one request of a fetch chain: the identity resolved when the
// chain started, the client's transport headers, and the purpose of the
// request. Credentials are deliberately not part of it: they are taken from the
// current auth snapshot on every hop, so a token refresh continues the same
// attempt while the client identity stays fixed.
type outboundHop struct {
	Method    string
	Headers   http.Header
	Transport http.Header
	// Provenance is the explicit authority for a resource URL registered from an
	// authenticated upstream answer. It is used only when it matches this hop's
	// exact URL; nil keeps the conservative unregistered policy.
	Provenance *resourceQueryProvenance
}

// sendOutboundHop performs exactly one request against rawURL, projecting the
// headers and credentials its target may receive, and never following a
// redirect (the caller re-evaluates every hop).
func (c *UpstreamClient) sendOutboundHop(ctx context.Context, reqCtx *RequestContext, rawURL string, hop outboundHop) (*http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if c == nil {
		return nil, errUnsupportedOutboundTarget
	}
	if c.ConfigSnapshot().SpoofClient == "resident" && c.residentIdentity == nil {
		return nil, newResidentIdentityMissingError()
	}
	target, err := url.Parse(rawURL)
	if err != nil {
		return nil, &redactedError{err: errUnsupportedOutboundTarget}
	}
	class := c.classifyOutboundTarget(target)
	if class == outboundTargetRejected {
		return nil, errUnsupportedOutboundTarget
	}
	method := hop.Method
	if method == "" {
		method = http.MethodGet
	}
	if method != http.MethodGet && method != http.MethodHead {
		return nil, errUnsupportedOutboundTarget
	}
	if reqCtx == nil {
		reqCtx = requestContextFrom(ctx)
	}
	provenance := hop.Provenance
	if !provenance.matches(target) {
		provenance = nil
	}
	if reqCtx != nil && provenance == nil && reqCtx.ResourceQueryProvenance.matches(target) {
		provenance = reqCtx.ResourceQueryProvenance
	}

	auth := c.authSnapshot()
	if err := c.validatePlaybackAuth(reqCtx, auth); err != nil {
		return nil, err
	}
	if reqCtx != nil && reqCtx.Playback != nil {
		pinned := cloneHeader(reqCtx.Playback.Identity.Headers)
		mergePublicOutboundHeaders(pinned, hop.Headers)
		hop.Headers = pinned
	}
	headers := cloneHeader(hop.Headers)
	c.applyResidentIdentity(headers)
	mergePublicOutboundHeaders(headers, hop.Transport)
	switch class {
	case outboundTargetTrusted:
		headers = c.applyOutboundHopAuth(headers, reqCtx, auth)
	case outboundTargetExternal:
		headers = projectPublicOutboundHeaders(headers)
	}
	if err := prepareHopQueryWithProvenance(target, class == outboundTargetTrusted, auth.AccessToken, provenance, auth.AccessToken, hop.Headers.Get("X-Emby-Token")); err != nil {
		return nil, err
	}

	request, err := http.NewRequestWithContext(ctx, method, target.String(), nil)
	if err != nil {
		return nil, &redactedError{err: errUnsupportedOutboundTarget}
	}
	for key, values := range headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	// One hop on purpose: the caller decides what the next hop means. It may be
	// answered here (an internal line) or handed to the player (a CDN link).
	client := &http.Client{
		Transport:     c.transport,
		Timeout:       0,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, doErr := client.Do(request)
	if doErr != nil {
		return nil, &redactedError{err: doErr}
	}
	return resp, nil
}

// redirectPolicyFor builds the CheckRedirect for a fetch this proxy follows on
// its own. Every hop is projected for its target: a hop on the origin the chain
// started from keeps the full identity and credential set, every other hop gets
// the public header set and no upstream credential.
//
// mode identifies the purpose of the request. A password login carries the
// administrator's saved upstream credentials in its body; those bytes cannot be
// removed by deleting a header, so such a request is refused before a hop that
// would resend the body to a target outside the origin the login started from.
func (c *UpstreamClient) redirectPolicyFor(follow bool, mode outboundAuthMode) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if !follow {
			return errUpstreamRedirectNotFollowed
		}
		if len(via) >= 10 {
			return fmt.Errorf("stopped after 10 redirects")
		}
		if req == nil || req.URL == nil || len(via) == 0 || via[0] == nil {
			return errUnsupportedOutboundTarget
		}
		class := c.classifyOutboundTarget(req.URL)
		if class == outboundTargetRejected {
			return errUnsupportedOutboundTarget
		}
		api, err := url.Parse(c.BaseURL)
		loginTrusted := err == nil && canonicalURLOrigin(api) != "" && canonicalURLOrigin(req.URL) == canonicalURLOrigin(api)
		if !loginTrusted && outboundRedirectCarriesSensitiveBody(mode, req) {
			return errUpstreamLoginRedirectUntrusted
		}
		token := via[0].Header.Get("X-Emby-Token")
		reqCtx := requestContextFrom(req.Context())
		private := resourceQueryPrivateTokens(reqCtx, token)
		provenance := requestContextResourceQueryProvenance(reqCtx).deriveRedirect(req.URL.String(), private...)
		if err := prepareHopQueryWithProvenance(req.URL, class == outboundTargetTrusted, token, provenance, private...); err != nil {
			return err
		}
		if class == outboundTargetTrusted {
			// Go copies the initial header map for redirects but may strip
			// Authorization after an external hop. Re-project the original,
			// already-prepared identity rather than trusting the previous hop.
			req.Header = cloneHeader(via[0].Header)
			if req.Body == nil || req.Body == http.NoBody {
				req.Header.Del("Content-Type")
				req.Header.Del("Content-Length")
			}
			return nil
		}
		req.Header = projectPublicOutboundHeaders(via[0].Header)
		return nil
	}
}

// outboundRedirectCarriesSensitiveBody reports whether the next hop would
// resend a request body that must never leave its origin. The check is on the
// purpose of the request, not on the final path or on a field search inside the
// body, so a future form encoding is covered by the same rule; a redirect that
// already converted the method to a bodyless GET is not blocked by it.
func outboundRedirectCarriesSensitiveBody(mode outboundAuthMode, req *http.Request) bool {
	if mode != authModePasswordLogin {
		return false
	}
	if req.Body != nil && req.Body != http.NoBody {
		return true
	}
	// net/http refuses to replay a body without GetBody and fails the redirect
	// itself; a declared non-zero length is still a body that must not be sent.
	return req.ContentLength != 0
}
