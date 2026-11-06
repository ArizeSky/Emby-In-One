package backend

import "net/http"

// A redirect is a new send boundary. Recheck the attempt against the very auth
// snapshot installed on that hop; never revive via[0]'s expired credential.
func (c *UpstreamClient) strictPlaybackRedirectPolicy(policy func(*http.Request, []*http.Request) error, reqCtx *RequestContext) func(*http.Request, []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		auth := c.authSnapshot()
		if err := c.validatePlaybackAuth(reqCtx, auth); err != nil {
			return err
		}
		if err := policy(req, via); err != nil {
			return err
		}
		// API writes cannot replay their body onto a media origin or CDN.
		if len(via) > 0 && via[0].Method != http.MethodGet && via[0].Method != http.MethodHead && canonicalURLOrigin(req.URL) != canonicalURLOrigin(via[0].URL) {
			return &outboundPreparationError{Status: 502, Kind: "playback-write-redirect-untrusted", Field: "body", Message: "playback write redirect refused"}
		}
		class := c.classifyOutboundTarget(req.URL)
		if class == outboundTargetTrusted {
			previous := ""
			if len(via) > 0 {
				previous = via[0].Header.Get("X-Emby-Token")
			}
			if err := prepareHopQuery(req.URL, true, auth.AccessToken, previous); err != nil {
				return err
			}
			contentType := req.Header.Get("Content-Type")
			req.Header = c.applyOutboundHopAuth(reqCtx.Playback.Identity.Headers.Clone(), reqCtx, auth)
			if len(via) > 0 {
				mergePublicOutboundHeaders(req.Header, via[0].Header)
			}
			if contentType != "" {
				req.Header.Set("Content-Type", contentType)
			}
		}
		return nil
	}
}
