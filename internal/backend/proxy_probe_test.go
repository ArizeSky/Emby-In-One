package backend

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"
)

func probeLookup(_ context.Context, _ string) ([]net.IPAddr, error) {
	return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}}, nil
}

func TestProxyProbeRejectsUnsafeResolution(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1/", "http://169.254.169.254/", "http://[::1]/", "http://[fe80::1%25eth0]/", "http://224.0.0.1/", "http://0.0.0.0/", "ftp://example.com/", "http://user:pass@example.com/", "http://example.com:65536/"} {
		u, _ := url.Parse(raw)
		if _, err := probeTargetIPs(context.Background(), u, probeLookup); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	u, _ := url.Parse("https://example.com/")
	for _, lookup := range []func(context.Context, string) ([]net.IPAddr, error){
		func(context.Context, string) ([]net.IPAddr, error) { return nil, fmt.Errorf("DNS failure") },
		func(context.Context, string) ([]net.IPAddr, error) { return nil, nil },
		func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("8.8.8.8")}, {IP: net.ParseIP("192.168.1.1")}}, nil
		},
	} {
		if _, err := probeTargetIPs(context.Background(), u, lookup); err == nil {
			t.Error("unsafe lookup accepted")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := probeTargetIPs(ctx, u, probeLookup); err == nil {
		t.Error("canceled context accepted")
	}
}

func TestProxyProbePinsHTTPAndChecksEveryRedirect(t *testing.T) {
	for _, target := range []string{"http://169.254.169.254/", "http://127.0.0.1/", "http://192.168.1.1/", "http://[::1]/", "file:///etc/passwd", "/finish"} {
		t.Run(target, func(t *testing.T) {
			var requests atomic.Int32
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Hostname() != "8.8.8.8" {
					t.Errorf("proxy received unpinned URL: %s", r.URL)
				}
				if requests.Add(1) == 1 {
					w.Header().Set("Location", target)
					w.WriteHeader(302)
					return
				}
				w.WriteHeader(204)
			}))
			defer proxy.Close()
			base, _ := buildProxyTransport(proxy.URL, nil, "test")
			client := &http.Client{Transport: &proxyProbeTransport{base: base.(*http.Transport), lookup: probeLookup}, Timeout: 3 * time.Second}
			response, err := client.Get("http://example.com/start")
			if response != nil {
				response.Body.Close()
			}
			if target == "/finish" {
				if err != nil || requests.Load() != 2 {
					t.Fatalf("public relative redirect failed: %v, calls=%d", err, requests.Load())
				}
			} else if err == nil || requests.Load() != 1 {
				t.Fatalf("unsafe redirect reached proxy: %v, calls=%d", err, requests.Load())
			}
		})
	}
}

func TestProxyProbeRechecksDNSAndFailsClosed(t *testing.T) {
	var requests, lookups atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Location", "/again")
		w.WriteHeader(302)
	}))
	defer proxy.Close()
	base, _ := buildProxyTransport(proxy.URL, nil, "test")
	lookup := func(ctx context.Context, host string) ([]net.IPAddr, error) {
		if lookups.Add(1) == 1 {
			return probeLookup(ctx, host)
		}
		return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
	}
	client := &http.Client{Transport: &proxyProbeTransport{base: base.(*http.Transport), lookup: lookup}, Timeout: 3 * time.Second}
	response, err := client.Get("http://example.com/start")
	if response != nil {
		response.Body.Close()
	}
	if err == nil || requests.Load() != 1 {
		t.Fatalf("DNS rebinding forwarded: %v, calls=%d", err, requests.Load())
	}
}

func TestProxyProbeHTTPSPinsConnectAndPreservesTLSIdentity(t *testing.T) {
	certificateServer := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer certificateServer.Close()
	cert := certificateServer.TLS.Certificates[0]
	targetName := certificateServer.Certificate().DNSNames[0]
	for _, secureProxy := range []bool{false, true} {
		t.Run(fmt.Sprintf("httpsProxy=%v", secureProxy), func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "CONNECT" || r.Host != "8.8.8.8:443" {
					t.Errorf("unexpected CONNECT: %s %s", r.Method, r.Host)
					w.WriteHeader(400)
					return
				}
				conn, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(3 * time.Second))
				fmt.Fprint(conn, "HTTP/1.1 200 Connection Established\r\n\r\n")
				secure := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}})
				if err := secure.Handshake(); err != nil {
					t.Error(err)
					return
				}
				if secure.ConnectionState().ServerName != targetName {
					t.Errorf("incorrect target SNI")
				}
				request, err := http.ReadRequest(bufio.NewReader(secure))
				if err != nil {
					t.Error(err)
					return
				}
				if request.Host != targetName {
					t.Errorf("incorrect target Host: %s", request.Host)
				}
				fmt.Fprint(secure, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
			})
			var proxy *httptest.Server
			if secureProxy {
				proxy = httptest.NewTLSServer(handler)
			} else {
				proxy = httptest.NewServer(handler)
			}
			defer proxy.Close()
			base, _ := buildProxyTransport(proxy.URL, nil, "test")
			transport := base.(*http.Transport)
			transport.TLSClientConfig = certificateServer.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
			client := &http.Client{Transport: &proxyProbeTransport{base: transport, lookup: probeLookup}, Timeout: 5 * time.Second}
			response, err := client.Get("https://" + targetName + "/")
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			body, err := io.ReadAll(response.Body)
			if err != nil || string(body) != "ok" {
				t.Fatalf("TLS probe failed: %q %v", body, err)
			}
		})
	}
}
