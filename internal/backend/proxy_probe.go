package backend

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// proxyProbeTransport validates every request, including redirects, then sends
// an IP literal to the forward proxy. The proxy cannot resolve the target again.
// The configured proxy itself uses the existing upstream transport policy.
type proxyProbeTransport struct {
	base   *http.Transport
	lookup func(context.Context, string) ([]net.IPAddr, error)
}

func probeTargetIPs(ctx context.Context, target *url.URL, lookup func(context.Context, string) ([]net.IPAddr, error)) ([]net.IPAddr, error) {
	if target == nil || target.Opaque != "" || target.User != nil || strings.Contains(target.Hostname(), "%") {
		return nil, fmt.Errorf("invalid probe target")
	}
	if err := validateHTTPURL(target.String()); err != nil {
		return nil, err
	}
	if port := target.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("invalid probe target port")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, blockedTargetResolveTimeout)
	defer cancel()
	var ips []net.IPAddr
	if ip := net.ParseIP(target.Hostname()); ip != nil {
		ips = []net.IPAddr{{IP: ip}}
	} else {
		var err error
		ips, err = lookup(ctx, target.Hostname())
		if err != nil {
			return nil, fmt.Errorf("probe target DNS lookup failed")
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("probe target DNS lookup returned no addresses")
	}
	for _, addr := range ips {
		if !addr.IP.IsGlobalUnicast() || strictSSRFPolicy.blocks(addr.IP) || addr.Zone != "" {
			return nil, fmt.Errorf("%s", probeTargetBlockedMessage(target.Hostname(), strictSSRFPolicy.reason(addr.IP)))
		}
	}
	return ips, nil
}

func (p *proxyProbeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ips, err := probeTargetIPs(req.Context(), req.URL, p.lookup)
	if err != nil {
		return nil, err
	}
	pinned := req.Clone(req.Context())
	address := ips[0].IP.String()
	if port := req.URL.Port(); port != "" {
		address = net.JoinHostPort(address, port)
	} else if strings.Contains(address, ":") {
		address = "[" + address + "]"
	}
	pinned.URL.Host = address
	pinned.Host = req.URL.Host
	if req.URL.Scheme == "http" {
		// Go uses Request.Host in an HTTP proxy's absolute request URI. Keep it
		// pinned too: this endpoint tests connectivity (any HTTP status), rather
		// than virtual-host content. HTTPS keeps the original Host and TLS SNI.
		pinned.Host = address
	}
	transport := p.base.Clone()
	transport.DisableKeepAlives = true
	if transport.Proxy != nil {
		proxyURL, err := transport.Proxy(pinned)
		if err != nil {
			return nil, err
		}
		if proxyURL != nil && proxyURL.Scheme == "https" {
			transport.DialTLSContext = probeProxyTLSDialer(p.base, proxyURL.Hostname())
		}
	}
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	}
	transport.TLSClientConfig.ServerName = req.URL.Hostname()
	defer transport.CloseIdleConnections()
	return transport.RoundTrip(pinned)
}

// HTTPS forward proxies need their own TLS identity before the CONNECT tunnel
// establishes a separate TLS session using the target's original DNS name.
func probeProxyTLSDialer(base *http.Transport, host string) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := base.DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		config := &tls.Config{ServerName: host}
		if base.TLSClientConfig != nil {
			config = base.TLSClientConfig.Clone()
			config.ServerName = host
		}
		secure := tls.Client(conn, config)
		if err := secure.HandshakeContext(ctx); err != nil {
			conn.Close()
			return nil, err
		}
		return secure, nil
	}
}
