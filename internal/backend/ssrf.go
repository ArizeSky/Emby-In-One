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

// validatingDialer checks the destination against the policy and then dials the
// original address, so net.Dialer keeps trying every resolved address instead of
// being pinned to the first one.
func validatingDialer(policy ssrfPolicy) func(ctx context.Context, network, addr string) (net.Conn, error) {
	// Matches the connect/keep-alive timeouts of http.DefaultTransport.
	base := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		if _, err := policy.resolveTarget(ctx, addr); err != nil {
			return nil, err
		}
		return base.DialContext(ctx, network, addr)
	}
}

// pinnedDialer checks the destination against the policy and connects to the
// first validated address, so a hostname cannot resolve to a permitted address
// during validation and to a blocked one at connect time (DNS rebinding).
func pinnedDialer(policy ssrfPolicy) func(ctx context.Context, network, addr string) (net.Conn, error) {
	base := &net.Dialer{Timeout: 10 * time.Second}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		_, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("ssrf check: invalid address %q", addr)
		}
		ips, err := policy.resolveTarget(ctx, addr)
		if err != nil {
			return nil, err
		}
		return base.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}
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
func blockedTargetReason(host string) (string, bool) {
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
		if strictSSRFPolicy.blocks(ip) {
			return strictSSRFPolicy.reason(ip), true
		}
	}
	return "", false
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
