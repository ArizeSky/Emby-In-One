package backend

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// ssrfPolicy selects which resolved destination addresses a dialer refuses.
// The zero value allows every destination.
type ssrfPolicy struct {
	blockLoopback    bool
	blockPrivate     bool
	blockLinkLocal   bool
	blockUnspecified bool
}

var (
	// strictSSRFPolicy refuses every address that is not reachable from the
	// public internet. Used for admin-supplied probe targets.
	strictSSRFPolicy = ssrfPolicy{blockLoopback: true, blockPrivate: true, blockLinkLocal: true, blockUnspecified: true}

	// upstreamSSRFPolicy only refuses link-local and unspecified addresses,
	// which is where cloud instance metadata services (169.254.169.254) live.
	// Loopback and RFC1918 stay reachable because self-hosted Emby servers
	// commonly run on the same host or the local network.
	upstreamSSRFPolicy = ssrfPolicy{blockLinkLocal: true, blockUnspecified: true}
)

func (p ssrfPolicy) blocks(ip net.IP) bool {
	switch {
	case ip.IsLoopback():
		return p.blockLoopback
	case ip.IsPrivate():
		return p.blockPrivate
	case ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast():
		return p.blockLinkLocal
	case ip.IsUnspecified():
		return p.blockUnspecified
	}
	return false
}

func (p ssrfPolicy) reason(ip net.IP) string {
	switch {
	case ip.IsLoopback():
		return "loopback"
	case ip.IsPrivate():
		return "private"
	case ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast():
		return "link-local"
	default:
		return "reserved"
	}
}

// resolveTarget resolves addr's host and rejects it when any resolved address is
// blocked by the policy. IP literals are returned without touching the network.
func (p ssrfPolicy) resolveTarget(ctx context.Context, addr string) ([]net.IPAddr, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("ssrf check: invalid address %q", addr)
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("ssrf check: DNS resolve failed for %q: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("ssrf check: %q resolved to no address", host)
	}
	for _, ipAddr := range ips {
		if p.blocks(ipAddr.IP) {
			return nil, fmt.Errorf("ssrf check: resolved IP %s for %q is %s", ipAddr.IP, host, p.reason(ipAddr.IP))
		}
	}
	return ips, nil
}

// dialValidated resolves addr once, validates every resolved address against the
// policy, and then dials the validated addresses in order. Resolving once and
// dialing only validated addresses closes the DNS-rebinding window where a host
// validates against one answer and connects to a different one. Trying each
// validated address in turn keeps the multi-address failover that net.Dialer
// would otherwise provide.
func dialValidated(policy ssrfPolicy, base *net.Dialer) func(ctx context.Context, network, addr string) (net.Conn, error) {
	if base == nil {
		base = &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("ssrf check: invalid address %q", addr)
		}
		ips, err := policy.resolveTarget(ctx, addr)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, ipAddr := range ips {
			conn, dialErr := base.DialContext(ctx, network, net.JoinHostPort(ipAddr.IP.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		return nil, lastErr
	}
}

// validatingDialer checks the destination against the policy and then dials the
// validated addresses. Used for upstream connections.
func validatingDialer(policy ssrfPolicy) func(ctx context.Context, network, addr string) (net.Conn, error) {
	// Matches the connect/keep-alive timeouts of http.DefaultTransport.
	return dialValidated(policy, &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second})
}

// pinnedDialer is kept for the strict policy used by admin probe targets; it now
// shares the single-resolution implementation with validatingDialer.
func pinnedDialer(policy ssrfPolicy) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return dialValidated(policy, &net.Dialer{Timeout: 10 * time.Second})
}

// upstreamTransportDialer guards upstream connections against link-local targets.
func upstreamTransportDialer() func(ctx context.Context, network, addr string) (net.Conn, error) {
	return validatingDialer(upstreamSSRFPolicy)
}

// ssrfSafeDialer returns a DialContext function that checks resolved IP addresses
// against private/reserved ranges before allowing the connection. This prevents
// DNS rebinding attacks where a hostname resolves differently between validation
// and connection time.
func ssrfSafeDialer() func(ctx context.Context, network, addr string) (net.Conn, error) {
	return pinnedDialer(strictSSRFPolicy)
}

// blockedTargetReason reports why host cannot be used as a probe target and which
// address range it falls into ("loopback", "private", "link-local", "reserved"),
// or ("", false) when the strict policy allows it.
// blockedTargetReason reports whether host resolves to an address blocked by the
// given policy and which range it falls into ("loopback", "private", "link-local",
// "reserved"), or ("", false) when the policy allows it. It is a pre-validation
// used to reject a bad target before any transport is built; the dialer above
// re-checks at connect time against a single DNS answer, so this pre-check is a
// filter, not the enforcement point.
func (p ssrfPolicy) blockedTargetReason(host string) (string, bool) {
	h := host
	if hp, _, err := net.SplitHostPort(host); err == nil {
		h = hp
	}
	ips, err := net.LookupIP(h)
	if err != nil {
		ip := net.ParseIP(h)
		if ip == nil {
			return "", false
		}
		ips = []net.IP{ip}
	}
	for _, ip := range ips {
		if p.blocks(ip) {
			return p.reason(ip), true
		}
	}
	return "", false
}

// blockedTargetReason keeps the original strict-policy signature used by the admin
// proxy-test handler.
func blockedTargetReason(host string) (string, bool) {
	return strictSSRFPolicy.blockedTargetReason(host)
}

// wrapTransportWithSSRFCheck wraps an existing RoundTripper (typically a proxy transport)
// with a SSRF-safe dialer that validates resolved IPs at connection time.
func wrapTransportWithSSRFCheck(rt http.RoundTripper) http.RoundTripper {
	dial := ssrfSafeDialer()
	if t, ok := rt.(*http.Transport); ok {
		clone := t.Clone()
		clone.DialContext = dial
		return clone
	}
	// Fallback: create a fresh transport with the safe dialer
	return &http.Transport{
		DialContext:           dial,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}
