package backend

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestOutboundDiagnosticsCorrelatesRedirectAndMediaDenialWithoutSecrets(t *testing.T) {
	const token = "fixture-private-upstream-token"
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Emby-Token") != "" || r.URL.Query().Get("api_key") != "" {
			t.Error("external hop received upstream credential")
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer cdn.Close()
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", cdn.URL+"/file?api_key="+token+"&other=fixture-signed-material")
		w.WriteHeader(http.StatusFound)
	}))
	defer up.Close()
	logger := NewLogger(LogConfig{DataDir: t.TempDir(), Level: "error", FileLevel: "info"})
	t.Cleanup(func() { _ = logger.Close() })
	c := newUpstreamClient(Config{}, UpstreamConfig{ID: "server-test", Name: "fixture-account-name", URL: up.URL, SpoofClient: "custom", CustomUserAgent: "fixture-private-agent", CustomDeviceId: "fixture-private-device", FollowRedirects: true}, 0, logger)
	c.setOnline(token, "fixture-private-user")
	app := &App{Logger: logger}
	handler := app.loggingMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Ordinary API redirects still strip private credentials. Media-specific
		// delegation is covered separately with a gateway that requires its URL.
		resp, err := c.doRequest(r.Context(), nil, http.MethodGet, "/System/Info", nil, nil, c.identityHeaders(nil, nil), false)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
	}))
	request := httptest.NewRequest("GET", "/Videos/item/stream.mkv", nil)
	request.Header.Set("X-Eio-Trace-Id", "fixture-untrusted-client-trace")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, request)
	trace := rr.Header().Get("X-Eio-Trace-Id")
	if rr.Code != 401 || len(trace) != 24 || trace == request.Header.Get("X-Eio-Trace-Id") {
		t.Fatal("media denial was not returned with a generated trace")
	}
	var diagnostics []string
	for _, entry := range logger.Entries(0) {
		if strings.HasPrefix(entry.Message, "outbound") {
			diagnostics = append(diagnostics, entry.Message)
			if !strings.Contains(entry.Message, "trace="+trace) {
				t.Fatal("redirect or policy step lost the inbound correlation")
			}
		}
	}
	joined := strings.Join(diagnostics, "\n")
	for _, required := range []string{"hop=1", "hop=2", "status=302", "status=401", "target=trusted-origin", "target=external", "query_action=private-credential-removed", "header_auth=false query_auth=absent"} {
		if !strings.Contains(joined, required) {
			t.Errorf("missing diagnostic %q", required)
		}
	}
	for _, forbidden := range []string{token, "fixture-signed-material", "fixture-private-agent", "fixture-private-device", "fixture-private-user", "fixture-account-name", up.URL, cdn.URL} {
		if strings.Contains(joined, forbidden) {
			t.Error("outbound diagnostic leaked a sensitive value")
		}
	}
	file, err := os.ReadFile(logger.filePath)
	if err != nil || !strings.Contains(string(file), "status=302") || !strings.Contains(string(file), "status=401") || !strings.Contains(string(file), "[trace:"+trace+"]") {
		t.Fatal("default INFO file logging did not retain redirect, denial, and correlation")
	}
}

func TestResourceDiagnosticsKeepsTraceAcrossManualFollow(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Videos/item/stream.mkv" {
			w.Header().Set("Location", "/Videos/item/final.mkv")
			w.WriteHeader(307)
			return
		}
		w.WriteHeader(429)
	}))
	defer up.Close()
	logger := NewLogger(LogConfig{DataDir: t.TempDir(), Level: "error"})
	t.Cleanup(func() { _ = logger.Close() })
	c := newUpstreamClient(Config{}, UpstreamConfig{ID: "server-test", URL: up.URL, SpoofClient: "custom", CustomUserAgent: "fixture-player"}, 0, logger)
	c.setOnline("fixture-token", "fixture-user")
	trace := newOutboundTrace()
	ctx := context.WithValue(context.Background(), outboundTraceKey{}, trace)
	app := &App{}
	r := httptest.NewRequest("HEAD", "/Videos/item/eio-resource/resource", nil).WithContext(ctx)
	resp, err := app.fetchResourceFollowingRedirects(r, c, up.URL+"/Videos/item/stream.mkv")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != 429 {
		t.Fatal("resource status changed")
	}
	var lines []string
	for _, entry := range logger.Entries(0) {
		if strings.HasPrefix(entry.Message, "outbound ") {
			lines = append(lines, entry.Message)
			if !strings.Contains(entry.Message, "trace="+trace.id) || !strings.Contains(entry.Message, "method=HEAD") {
				t.Fatal("manual hop lost trace or HEAD semantics")
			}
		}
	}
	if len(lines) != 2 || !strings.Contains(lines[0], "status=307") || !strings.Contains(lines[1], "hop=2") || !strings.Contains(lines[1], "status=429") {
		t.Fatal("manual redirect did not record both network hops")
	}
}
