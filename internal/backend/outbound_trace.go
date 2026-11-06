package backend

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

// A trace is generated locally, never accepted from a caller's header. It is an
// opaque correlation ID, not a credential, and never authorizes anything.
type outboundTraceKey struct{}
type outboundTrace struct {
	id   string
	hops atomic.Uint64
}

func newOutboundTrace() *outboundTrace {
	var id [12]byte
	if _, err := rand.Read(id[:]); err != nil {
		return &outboundTrace{id: "unavailable"}
	}
	return &outboundTrace{id: hex.EncodeToString(id[:])}
}

func withOutboundTrace(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if trace, _ := ctx.Value(outboundTraceKey{}).(*outboundTrace); trace == nil {
		ctx = context.WithValue(ctx, outboundTraceKey{}, newOutboundTrace())
	}
	return ctx
}

func (c *UpstreamClient) logHopQueryPolicy(ctx context.Context, target *url.URL, class outboundTargetClass, before string, err error) {
	if c.logger == nil || err == nil && before == target.RawQuery {
		return
	}
	trace, _ := ctx.Value(outboundTraceKey{}).(*outboundTrace)
	if trace == nil {
		return
	}
	action, result := "unchanged", "ready"
	if before != target.RawQuery {
		action = "credential-refreshed"
		if class == outboundTargetExternal {
			action = "private-credential-removed"
		}
	}
	if prep, ok := asPreparationError(err); ok {
		result = prep.Kind
	}
	message := fmt.Sprintf("outbound_policy trace=%s server=%s target=%s origin=%s query_action=%s result=%s",
		trace.id, c.ID, class, diagnosticOrigin(target), action, result)
	if err != nil {
		c.logger.Warnf("%s", message)
	} else {
		c.logger.Infof("%s", message)
	}
}

type outboundDiagnosticTransport struct {
	base    http.RoundTripper
	client  *UpstreamClient
	purpose string
	trace   *outboundTrace
}

func (c *UpstreamClient) diagnosticTransport(ctx context.Context, purpose string) http.RoundTripper {
	if c.logger == nil {
		return c.transport
	}
	trace, _ := ctx.Value(outboundTraceKey{}).(*outboundTrace)
	if trace == nil {
		trace = newOutboundTrace()
	}
	return &outboundDiagnosticTransport{base: c.transport, client: c, purpose: purpose, trace: trace}
}

func diagnosticOrigin(target *url.URL) string {
	if target == nil {
		return "none"
	}
	// Host names and deployment paths are deliberately absent. Stable origin
	// digests distinguish two CDN hops without disclosing a signed URL.
	digest := sha256.Sum256([]byte(canonicalURLOrigin(target)))
	return hex.EncodeToString(digest[:6])
}

func diagnosticRoute(path string) string {
	path = strings.ToLower(path)
	switch {
	case strings.HasSuffix(path, "/users/authenticatebyname"):
		return "password-login"
	case strings.HasSuffix(path, "/playbackinfo"):
		return "playback-info"
	case strings.Contains(path, "/shows/") && strings.HasSuffix(path, "/seasons"):
		return "show-seasons"
	case strings.Contains(path, "/shows/") && strings.HasSuffix(path, "/episodes"):
		return "show-episodes"
	case strings.Contains(path, "/sessions/playing"):
		return "playback-event"
	case strings.Contains(path, "/subtitles/"):
		return "subtitle"
	case strings.Contains(path, "/videos/") || strings.Contains(path, "/audio/"):
		return "media"
	case strings.Contains(path, "/images/"):
		return "image"
	default:
		return "other"
	}
}

func diagnosticError(err error) string {
	if err == nil {
		return "none"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) && netErr.Timeout() {
		return "timeout"
	}
	return "transport"
}

func (d *outboundDiagnosticTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	hop := d.trace.hops.Add(1)
	started := time.Now()
	base := d.base
	if base == nil {
		base = http.DefaultTransport
	}
	response, err := base.RoundTrip(r)
	status, redirect := 0, "none"
	if response != nil {
		status = response.StatusCode
		if isRedirectStatus(status) {
			if location, parseErr := response.Location(); parseErr == nil {
				redirect = string(d.client.classifyOutboundTarget(location)) + ":" + diagnosticOrigin(location)
			} else {
				redirect = "invalid-location"
			}
		}
	}
	headerAuth := r.Header.Get("X-Emby-Token")
	queryAuth, _ := queryTokenLookup(r.URL.Query())
	queryClass := "absent"
	if queryAuth != "" {
		queryClass = "present"
		if headerAuth != "" && queryAuth == headerAuth {
			queryClass = "matches-header"
		} else if headerAuth != "" {
			queryClass = "differs-from-header"
		}
	}
	attempt, source := "none", "none"
	if req := requestContextFrom(r.Context()); req != nil && req.Playback != nil {
		// The generated attempt ID is safe; never include a capability or user ID.
		attempt = req.Playback.AttemptID
		source = diagnosticIdentitySource(req.Playback.Identity.Source)
	}
	message := fmt.Sprintf("outbound trace=%s hop=%d server=%s attempt=%s purpose=%s route=%s target=%s origin=%s method=%s status=%d duration_ms=%d header_auth=%t query_auth=%s identity_source=%s ua=%t client_identity=%t device_identity=%t redirect=%s error=%s",
		d.trace.id, hop, d.client.ID, attempt, d.purpose, diagnosticRoute(r.URL.Path),
		d.client.classifyOutboundTarget(r.URL), diagnosticOrigin(r.URL), r.Method,
		status, time.Since(started).Milliseconds(), headerAuth != "", queryClass, source,
		r.Header.Get("User-Agent") != "", r.Header.Get("X-Emby-Client") != "", r.Header.Get("X-Emby-Device-Id") != "", redirect, diagnosticError(err))
	switch {
	case err != nil && !errors.Is(err, context.Canceled), status >= 400:
		d.client.logger.Warnf("%s", message)
	case isRedirectStatus(status):
		d.client.logger.Infof("%s", message)
	default:
		d.client.logger.Debugf("%s", message)
	}
	return response, err
}
