package backend

import (
	"context"
	"net/http"
	"strings"
	"time"
)

// streamLivenessProbePath is the path liveness probes are issued against. It is
// deliberately inside the /Videos/ route family the line is actually used for:
// a line whose reverse proxy only forwards /Videos/ and /Audio/ (a common
// self-hosted split-tunnel setup) answers 403/404 here, and that answer is
// exactly what proves the whole line — proxy hop included — is up. The probe
// never depends on any Emby endpoint being exposed at the root.
const streamLivenessProbePath = "/Videos/probe"

// streamProbeTimeout bounds one liveness probe. It shares the health-check
// timeout when one is configured and falls back to 10s otherwise.
func (c *UpstreamClient) streamProbeTimeout() time.Duration {
	if c.timeouts.HealthCheck > 0 {
		return time.Duration(c.timeouts.HealthCheck) * time.Millisecond
	}
	return 10 * time.Second
}

// probeStreamBases checks every configured stream base of one upstream and
// updates its liveness marks. A base is alive when an HTTP response of any
// status comes back: 403/404 still proves DNS, TCP, TLS and the reverse-proxy
// hop all work. Only connect-level failures (DNS, TCP, TLS, timeout) mark a
// base dead.
//
// The probe uses the client's own transport (including its outbound proxy) but
// no credentials: the request carries no token, so nothing is exposed to a line
// that might not even be the administrator's own.
func (c *UpstreamClient) probeStreamBases(ctx context.Context) {
	bases := c.streamBasesForProbing()
	if len(bases) == 0 {
		return
	}
	client := &http.Client{
		Transport:     c.transport,
		Timeout:       c.streamProbeTimeout(),
		CheckRedirect: redirectPolicy(c.Config.FollowRedirects),
	}
	for _, base := range bases {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if probeOneStreamBase(ctx, client, strings.TrimRight(base, "/")+streamLivenessProbePath) {
			c.markStreamBaseAlive(base)
			if c.logger != nil {
				c.logger.Debugf("[%s] Stream line alive: %s", c.Name, base)
			}
		} else {
			c.markStreamBaseFailed(base)
			if c.logger != nil {
				c.logger.Warnf("[%s] Stream line probe failed, marked dead for %s: %s",
					c.Name, streamFailureCooldown, base)
			}
		}
	}
}

// streamBasesForProbing returns the bases worth probing: only those that differ
// from the API base (the API base is already covered by the login health check)
// and only when more than one base exists at all — with a single base there is
// no fallback to select, so probing would only add noise.
func (c *UpstreamClient) streamBasesForProbing() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if len(c.StreamBaseURLs) <= 1 {
		return nil
	}
	bases := make([]string, 0, len(c.StreamBaseURLs))
	for _, base := range c.StreamBaseURLs {
		if base != c.BaseURL {
			bases = append(bases, base)
		}
	}
	return bases
}

// probeOneStreamBase issues one liveness request. Any HTTP response means the
// line is up; only a transport error means it is down. The URL is built from
// the administrator-configured base plus a fixed path, so it carries no
// credentials.
func probeOneStreamBase(ctx context.Context, client *http.Client, probeURL string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "Emby-In-One-Liveness/1.0")
	resp, err := client.Do(req) // CodeQL: intentional liveness probe to admin-configured stream base
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return true
}
