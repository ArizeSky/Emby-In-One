package backend

import (
	"strings"
	"testing"
)

// A proxy URL can carry credentials in its userinfo. The invalid-proxy log
// line used to print the URL verbatim, putting those credentials into the log
// file. Only the redacted form may be logged.
func TestInvalidProxyURLIsRedactedInLog(t *testing.T) {
	dir := t.TempDir()
	logger := NewLogger(LogConfig{Level: "error", FileLevel: "error", DataDir: dir})
	t.Cleanup(func() { _ = logger.Close() })

	// A URL that fails to parse (control character) but still carries userinfo.
	buildProxyTransport("http://user:secret-pw@proxy.example:8080/\x7f", logger, "srv-0")

	found := false
	for _, entry := range logger.Entries(10) {
		if !strings.Contains(entry.Message, "Invalid proxy URL") {
			continue
		}
		found = true
		if strings.Contains(entry.Message, "secret-pw") {
			t.Fatalf("proxy password leaked into the log: %s", entry.Message)
		}
		if strings.Contains(entry.Message, "user:") {
			t.Fatalf("proxy userinfo leaked into the log: %s", entry.Message)
		}
	}
	if !found {
		t.Fatalf("expected an invalid-proxy log entry, got %d entries", len(logger.Entries(10)))
	}
}

func TestRedactProxyURLForLog(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"http://user:pass@proxy.example:8080", "http://proxy.example:8080"},
		{"https://proxy.example:8080/path?q=1", "https://proxy.example:8080/path?q=1"},
		{"http://user:pass@proxy.example:8080/\x7f", "<redacted-userinfo>@proxy.example:8080/\x7f"},
		{"not a url at all", "<unparsable-url>"},
	}
	for _, tc := range cases {
		if got := redactProxyURLForLog(tc.in); got != tc.want {
			t.Errorf("redactProxyURLForLog(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
