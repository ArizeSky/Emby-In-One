package backend

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

func isMediaFetchPath(path string) bool {
	path = strings.ToLower(path)
	return strings.Contains(path, "/videos/") || strings.Contains(path, "/audio/")
}

func (c *UpstreamClient) fetchMediaRedirectTarget(ctx context.Context, reqCtx *RequestContext, rawURL string, snapshot identitySnapshot, transport http.Header, source *http.Request) (*http.Response, error) {
	target, err := url.Parse(rawURL)
	if err != nil {
		return nil, errUnsupportedOutboundTarget
	}
	provenance := c.mediaRedirectProvenance(requestContextResourceQueryProvenance(reqCtx), source, target, reqCtx, true)
	return c.sendOutboundHop(ctx, reqCtx, rawURL, outboundHop{
		Method: http.MethodGet, Headers: snapshot.Headers, Transport: transport, Provenance: provenance,
	})
}

// mediaRedirectProvenance distinguishes a credential that a trusted upstream
// explicitly put in a media Location from credentials copied by our proxy.
// Authority is exact-URL and single-hop: an external response cannot extend it,
// and the receiving origin never becomes trusted for headers or login bodies.
func (c *UpstreamClient) mediaRedirectProvenance(previous *resourceQueryProvenance, source *http.Request, target *url.URL, reqCtx *RequestContext, media bool) *resourceQueryProvenance {
	if target == nil {
		return nil
	}
	token := ""
	if source != nil {
		token = source.Header.Get("X-Emby-Token")
	}
	private := resourceQueryPrivateTokens(reqCtx, token)
	private = append(private, c.authSnapshot().AccessToken)
	provenance := previous.deriveRedirect(target.String(), private...)
	if !media || source == nil || provenance == nil || token == "" ||
		(source.Method != http.MethodGet && source.Method != http.MethodHead) ||
		(source.Body != nil && source.Body != http.NoBody) || source.ContentLength > 0 ||
		c.classifyOutboundTarget(source.URL) != outboundTargetTrusted ||
		c.classifyOutboundTarget(target) != outboundTargetExternal {
		return provenance
	}
	if source.URL.Scheme == "https" && target.Scheme != "https" {
		return provenance
	}
	if reqCtx != nil && (token == reqCtx.ProxyToken || token == reqCtx.PlaybackCredential) {
		return provenance
	}
	// Only a value already in the issued URL can be delegated. We do not add,
	// refresh, or reconstruct credentials on an external origin.
	for key, values := range target.Query() {
		if !isProxyCredentialQueryKey(key) {
			continue
		}
		for _, value := range values {
			if value == token {
				provenance.delegated = token
				if trace, _ := source.Context().Value(outboundTraceKey{}).(*outboundTrace); trace != nil && c.logger != nil {
					c.logger.Infof("outbound_policy trace=%s server=%s target=external origin=%s query_action=upstream-media-credential-preserved result=ready", trace.id, c.ID, diagnosticOrigin(target))
				}
				return provenance
			}
		}
	}
	return provenance
}
