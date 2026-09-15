package backend

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestUpstreamPolicyAllowsLocalNetworksAndBlocksLinkLocal(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "192.168.1.10", "172.16.0.1", "8.8.8.8", "::1"} {
		if upstreamSSRFPolicy.blocks(net.ParseIP(raw)) {
			t.Errorf("upstream policy must allow %s so self-hosted Emby stays reachable", raw)
		}
	}
	for _, raw := range []string{"169.254.169.254", "fe80::1", "0.0.0.0"} {
		if !upstreamSSRFPolicy.blocks(net.ParseIP(raw)) {
			t.Errorf("upstream policy must block %s", raw)
		}
	}
}

func TestStrictPolicyBlocksEveryReservedRange(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "10.0.0.1", "192.168.1.1", "172.16.0.1", "169.254.169.254", "0.0.0.0", "::1"} {
		if !strictSSRFPolicy.blocks(net.ParseIP(raw)) {
			t.Errorf("strict policy must block %s", raw)
		}
	}
}

// TestBlockedTargetReasonIsReportedPerRange covers the reason string that the
// admin API turns into user-facing guidance.
func TestBlockedTargetReasonIsReportedPerRange(t *testing.T) {
	cases := []struct{ host, reason string }{
		{"127.0.0.1", "loopback"},
		{"::1", "loopback"},
		{"10.0.0.1", "private"},
		{"192.168.1.1", "private"},
		{"172.16.0.1", "private"},
		{"169.254.169.254", "link-local"},
		{"fe80::1", "link-local"},
		{"0.0.0.0", "reserved"},
	}
	for _, tc := range cases {
		reason, blocked := blockedTargetReason(tc.host)
		if !blocked {
			t.Errorf("%s: want blocked", tc.host)
			continue
		}
		if reason != tc.reason {
			t.Errorf("%s: reason = %q, want %q", tc.host, reason, tc.reason)
		}
	}
	if reason, blocked := blockedTargetReason("8.8.8.8"); blocked {
		t.Errorf("public address must stay allowed, got reason %q", reason)
	}
}

func TestUpstreamDialerRefusesLinkLocalBeforeConnecting(t *testing.T) {
	dial := upstreamTransportDialer()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	start := time.Now()
	_, err := dial(ctx, "tcp", "169.254.169.254:80")
	if err == nil {
		t.Fatalf("dial to a link-local address must fail")
	}
	if !strings.Contains(err.Error(), "link-local") {
		t.Fatalf("dial error = %v, want a link-local rejection", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("rejection took %s; the address check must run before dialing", elapsed)
	}

	// Loopback upstreams must keep working: local Emby is the common deployment.
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer local.Close()
	conn, err := dial(ctx, "tcp", strings.TrimPrefix(local.URL, "http://"))
	if err != nil {
		t.Fatalf("loopback dial must stay allowed: %v", err)
	}
	_ = conn.Close()
}

func TestAdminUpstreamCreateRejectsLinkLocalTarget(t *testing.T) {
	withTempApp(t, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		rr := doJSONRequest(t, handler, http.MethodPost, "/admin/api/upstream", map[string]any{
			"name":     "metadata",
			"url":      "http://169.254.169.254/latest/meta-data/",
			"username": "u1",
			"password": "p1",
		}, token)
		if rr.Code != http.StatusBadGateway {
			t.Fatalf("create link-local upstream: status=%d body=%s", rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "link-local") {
			t.Fatalf("error should name the rejected range, got: %s", rr.Body.String())
		}
		if got := len(app.ConfigStore.Snapshot().Upstream); got != 0 {
			t.Fatalf("upstream config count = %d, want 0", got)
		}
	})
}

// TestProxyTestSSRFBlocksPrivateIP keeps the probe endpoint public-only and
// checks that each refusal names the target and explains the range, so the admin
// knows to add the server as an upstream instead.
func TestProxyTestSSRFBlocksPrivateIP(t *testing.T) {
	cases := []struct {
		name    string
		target  string
		host    string
		keyword string
	}{
		{"link-local metadata", "http://169.254.169.254/latest/meta-data/", "169.254.169.254", "链路本地"},
		{"private network", "http://192.168.1.10:8096/", "192.168.1.10", "内网"},
		{"loopback", "http://127.0.0.1:8096/", "127.0.0.1", "内网"},
	}

	withTempAppPrepared(t, parityConfigWithUpstreams(""), nil, func(app *App, handler http.Handler, dir string) {
		token := loginToken(t, handler, "secret")
		for _, tc := range cases {
			rr := doJSONRequest(t, handler, http.MethodPost, "/admin/api/proxies/test", map[string]any{
				"proxyUrl":  "http://1.2.3.4:8080",
				"targetUrl": tc.target,
			}, token)
			if rr.Code != http.StatusBadRequest {
				t.Errorf("%s: status=%d, want 400; body=%s", tc.name, rr.Code, rr.Body.String())
				continue
			}
			body := rr.Body.String()
			if !strings.Contains(body, tc.host) {
				t.Errorf("%s: message should name the target %s, got: %s", tc.name, tc.host, body)
			}
			if !strings.Contains(body, tc.keyword) {
				t.Errorf("%s: message should name the range (%s), got: %s", tc.name, tc.keyword, body)
			}
		}
	})
}
