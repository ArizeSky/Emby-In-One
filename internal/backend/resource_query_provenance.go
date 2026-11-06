package backend

import (
	"net/url"
	"strings"
)

// resourceQueryProvenance records why one registered resource URL may be sent
// as issued, and which credential values already belonged to this proxy or its
// upstream when it was registered. It is the explicit authority the outbound
// query policy needs before it can preserve an otherwise ambiguous external
// credential such as a CDN's own api_key.
//
// A value is bound to the exact URL it was registered from. It is immutable once
// an entry holds it; callers that learn another private value derive a copy for
// that request instead of editing the registered value. Values in private are
// never logged, rendered into a response, or written to an error.
type resourceQueryProvenanceSource uint8

const (
	resourceQueryProvenanceUnattested resourceQueryProvenanceSource = iota
	// resourceQueryProvenancePlaybackInfo is a DirectStreamUrl taken from the
	// validated PlaybackInfo answer of the selected upstream instance.
	resourceQueryProvenancePlaybackInfo
	// resourceQueryProvenanceManifest is a URI from a manifest this proxy fetched
	// with an authenticated upstream request.
	resourceQueryProvenanceManifest
	// resourceQueryProvenanceRedirect is a Location issued by an authoritative
	// upstream response while following an already authorized fetch.
	resourceQueryProvenanceRedirect
)

func (s resourceQueryProvenanceSource) String() string {
	switch s {
	case resourceQueryProvenancePlaybackInfo:
		return "playback-info"
	case resourceQueryProvenanceManifest:
		return "manifest"
	case resourceQueryProvenanceRedirect:
		return "redirect"
	default:
		return "unattested"
	}
}

type resourceQueryProvenance struct {
	source  resourceQueryProvenanceSource
	origin  string
	rawURL  string
	private []string
}

// newResourceQueryProvenance builds an authoritative binding for one exact URL.
// An unattested source, an unparsable URL or a URL without an http(s) origin
// yields nil, which the outbound policy treats as the conservative unregistered
// case.
func newResourceQueryProvenance(source resourceQueryProvenanceSource, rawURL string, private ...string) *resourceQueryProvenance {
	if source == resourceQueryProvenanceUnattested {
		return nil
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return nil
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return nil
	}
	origin := canonicalURLOrigin(parsed)
	if origin == "" {
		return nil
	}
	return &resourceQueryProvenance{
		source:  source,
		origin:  origin,
		rawURL:  rawURL,
		private: compactResourceQueryPrivate(private),
	}
}

// resourceQueryPrivateTokens collects the credential values a registration or
// hop knows are private without exposing them through RequestContext. The
// playback capability is included so it can never be forwarded upstream, and
// the parent local session token is included so it can never pass as a CDN
// credential.
func resourceQueryPrivateTokens(reqCtx *RequestContext, upstreamToken string) []string {
	if reqCtx == nil {
		return compactResourceQueryPrivate([]string{upstreamToken})
	}
	return compactResourceQueryPrivate([]string{
		upstreamToken,
		reqCtx.ProxyToken,
		reqCtx.PlaybackCredential,
	})
}

func requestContextResourceQueryProvenance(reqCtx *RequestContext) *resourceQueryProvenance {
	if reqCtx == nil {
		return nil
	}
	return reqCtx.ResourceQueryProvenance
}

func compactResourceQueryPrivate(values []string) []string {
	seen := make(map[string]bool, len(values))
	compacted := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		compacted = append(compacted, value)
	}
	return compacted
}

func (p *resourceQueryProvenance) attested() bool {
	return p != nil && p.source != resourceQueryProvenanceUnattested
}

// matches requires the exact registered URL, not only its origin: provenance for
// one resource must never authorize a query supplied by the client for another.
func (p *resourceQueryProvenance) matches(target *url.URL) bool {
	if !p.attested() || target == nil {
		return false
	}
	return p.rawURL == target.String() && p.origin == canonicalURLOrigin(target)
}

func (p *resourceQueryProvenance) knowsPrivate(value string) bool {
	if p == nil || value == "" {
		return false
	}
	for _, secret := range p.private {
		if secret == value {
			return true
		}
	}
	return false
}

// withKnownPrivate returns an immutable-enough request-scoped copy that also
// treats values as private. The registered value itself is never extended, so a
// capability or session token learned later cannot change what the entry means.
func (p *resourceQueryProvenance) withKnownPrivate(values ...string) *resourceQueryProvenance {
	if p == nil {
		return nil
	}
	combined := compactResourceQueryPrivate(append(append([]string(nil), p.private...), values...))
	if len(combined) == len(p.private) {
		return p
	}
	copy := *p
	copy.private = combined
	return &copy
}

// deriveRedirect carries the registration's private values onto a Location, so
// an old upstream token still in the redirect target is removed while a new
// signature the CDN issued for itself is preserved.
func (p *resourceQueryProvenance) deriveRedirect(rawURL string, extraPrivate ...string) *resourceQueryProvenance {
	private := append([]string(nil), extraPrivate...)
	if p != nil {
		private = append(append([]string(nil), p.private...), private...)
	}
	return newResourceQueryProvenance(resourceQueryProvenanceRedirect, rawURL, private...)
}

// String is deliberately free of the URL query and credential values so a
// diagnostic that formats a provenance cannot turn into a secret leak.
func (p *resourceQueryProvenance) String() string {
	if p == nil {
		return "resource-query-provenance:none"
	}
	return "resource-query-provenance:" + p.source.String() + ":" + p.origin
}
