package backend

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// compoundAuthHeader builds an Emby-style compound authorization header.
func compoundAuthHeader(userID, token, client, device, deviceID, version string) string {
	built := "Emby "
	parts := []string{}
	for _, field := range []struct{ name, value string }{
		{"UserId", userID}, {"Token", token}, {"Client", client},
		{"Device", device}, {"DeviceId", deviceID}, {"Version", version},
	} {
		if field.value == "" {
			continue
		}
		parts = append(parts, field.name+"="+quoteHeaderParam(field.value))
	}
	return built + strings.Join(parts, ", ")
}

const (
	identitySentinelToken  = "LOCAL-PROXY-TOKEN-SENTINEL"
	identitySentinelUserID = "LOCAL-PROXY-USERID-SENTINEL"
)

// TestPassthroughAuthorizationSanitized covers every source a passthrough
// identity can come from. None of them may carry the client's own token or user
// ID into the header set that goes upstream, and the device identity the client
// sent has to survive.
func TestPassthroughAuthorizationSanitized(t *testing.T) {
	liveHeaders := []struct {
		name    string
		headers http.Header
	}{
		{
			name: "compound header with user id and token",
			headers: http.Header{
				"X-Emby-Authorization": {compoundAuthHeader(identitySentinelUserID, identitySentinelToken, "Infuse", "iPhone", "dev-1", "7.7.1")},
			},
		},
		{
			name: "compound header with user id only",
			headers: http.Header{
				"X-Emby-Authorization": {compoundAuthHeader(identitySentinelUserID, "", "Infuse", "iPhone", "dev-1", "7.7.1")},
			},
		},
		{
			name: "compound header with token only",
			headers: http.Header{
				"X-Emby-Authorization": {compoundAuthHeader("", identitySentinelToken, "Infuse", "iPhone", "dev-1", "7.7.1")},
			},
		},
		{
			name: "compound header with neither",
			headers: http.Header{
				"X-Emby-Authorization": {compoundAuthHeader("", "", "Infuse", "iPhone", "dev-1", "7.7.1")},
			},
		},
		{
			name: "separate device headers with an independent token",
			headers: http.Header{
				"X-Emby-Client":         {"Infuse"},
				"X-Emby-Device-Name":    {"iPhone"},
				"X-Emby-Device-Id":      {"dev-1"},
				"X-Emby-Client-Version": {"7.7.1"},
				"X-Emby-Token":          {identitySentinelToken},
			},
		},
		{
			name: "a title-cased authorization credential",
			headers: http.Header{
				"Authorization": {compoundAuthHeader(identitySentinelUserID, identitySentinelToken, "Infuse", "iPhone", "dev-1", "7.7.1")},
			},
		},
	}

	for _, tc := range liveHeaders {
		t.Run(tc.name, func(t *testing.T) {
			headers := mergePassthroughHeaders(tc.headers)
			for _, key := range []string{"X-Emby-Authorization", "Authorization"} {
				value := headers.Get(key)
				if value == "" {
					continue
				}
				if strings.Contains(value, identitySentinelToken) || strings.Contains(value, identitySentinelUserID) {
					t.Fatalf("%s carried a local credential: %q", key, value)
				}
				parsed, ok := parseAuthorizationIdentityStrict(value)
				if !ok {
					t.Fatalf("%s is not parseable: %q", key, value)
				}
				if parsed["Token"] != "" || parsed["UserId"] != "" {
					t.Fatalf("%s kept identity parameters: %q", key, value)
				}
			}
			if headers.Get("X-Emby-Client") != "Infuse" {
				t.Fatalf("device identity changed: %q", headers.Get("X-Emby-Client"))
			}
			if headers.Get("X-Emby-Device-Id") != "dev-1" {
				t.Fatalf("device id changed: %q", headers.Get("X-Emby-Device-Id"))
			}
		})
	}

	// Every resolution source goes through the same cleanup, not only the newest
	// login.
	svc := NewClientIdentityService()
	svc.SetCaptured(identitySentinelToken, http.Header{
		"X-Emby-Client":         {"Infuse"},
		"X-Emby-Device-Name":    {"iPhone"},
		"X-Emby-Device-Id":      {"dev-1"},
		"X-Emby-Client-Version": {"7.7.1"},
		"X-Emby-Authorization":  {compoundAuthHeader(identitySentinelUserID, identitySentinelToken, "Infuse", "iPhone", "dev-1", "7.7.1")},
	})
	svc.SaveLastSuccess("server-key", http.Header{
		"X-Emby-Client":         {"Infuse"},
		"X-Emby-Device-Name":    {"iPhone"},
		"X-Emby-Device-Id":      {"dev-1"},
		"X-Emby-Client-Version": {"7.7.1"},
		"X-Emby-Authorization":  {compoundAuthHeader(identitySentinelUserID, identitySentinelToken, "Infuse", "iPhone", "dev-1", "7.7.1")},
	})

	for name, source := range map[string]ResolvedPassthroughHeaders{
		"captured-token": svc.ResolvePassthroughHeadersForServer(http.Header{}, identitySentinelToken, "server-key"),
		"last-success":   svc.ResolvePassthroughHeadersForServer(http.Header{}, "", "server-key"),
		"latest":         svc.ResolvePassthroughHeadersForServer(http.Header{}, "", "unknown-server"),
	} {
		value := source.Headers.Get("X-Emby-Authorization")
		if value == "" {
			continue
		}
		if strings.Contains(value, identitySentinelToken) || strings.Contains(value, identitySentinelUserID) {
			t.Fatalf("%s carried a local credential: %q", name, value)
		}
	}
	for name, headers := range map[string]http.Header{
		"captured":        svc.GetCaptured(identitySentinelToken),
		"last-success":    svc.GetLastSuccess("server-key"),
		"latest-captured": svc.GetLatestCaptured(),
	} {
		for _, key := range []string{"X-Emby-Authorization", "Authorization", "X-Emby-Token"} {
			if strings.Contains(headers.Get(key), identitySentinelToken) || strings.Contains(headers.Get(key), identitySentinelUserID) {
				t.Fatalf("a persisted %s header kept the credential: %q", name, headers.Get(key))
			}
		}
	}
}

// TestPersistedIdentityStripsLegacyCredentials loads a capture file written by an
// older version, which stored the compound authorization header verbatim, and
// checks that its credentials are neither forwarded nor written back.
func TestPersistedIdentityStripsLegacyCredentials(t *testing.T) {
	dir := t.TempDir()
	legacyCompound := compoundAuthHeader(identitySentinelUserID, identitySentinelToken, "Infuse", "iPhone", "dev-1", "7.7.1")
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	payload := map[string]any{
		"latestCaptured": map[string]any{
			"headers": map[string]any{
				"X-Emby-Client":         []string{"Infuse"},
				"X-Emby-Client-Version": []string{"7.7.1"},
				"X-Emby-Device-Name":    []string{"iPhone"},
				"X-Emby-Device-Id":      []string{"dev-1"},
				"X-Emby-Authorization":  []string{legacyCompound},
				"Authorization":         []string{legacyCompound},
				"X-Emby-Token":          []string{identitySentinelToken},
			},
			"capturedAt": "2024-01-01T00:00:00Z",
		},
		"lastSuccessByServer": map[string]any{
			"server-key": map[string]any{
				"headers": map[string]any{
					"X-Emby-Client":        []string{"Infuse"},
					"X-Emby-Device-Id":     []string{"dev-1"},
					"X-Emby-Authorization": []string{legacyCompound},
					"X-Emby-Token":         []string{identitySentinelToken},
				},
				"capturedAt": "2024-01-01T00:00:00Z",
			},
		},
	}
	writeLegacyCapturedFile(t, dir, payload)

	persistence := NewIdentityPersistence(filepath.Join(dir, "data"), nil)
	svc := newClientIdentityService(persistence)

	for name, headers := range map[string]http.Header{
		"latest":       svc.GetLatestCaptured(),
		"last-success": svc.GetLastSuccess("server-key"),
	} {
		for _, key := range []string{"X-Emby-Authorization", "Authorization", "X-Emby-Token"} {
			if value := headers.Get(key); strings.Contains(value, identitySentinelToken) || strings.Contains(value, identitySentinelUserID) {
				t.Fatalf("a loaded %s header kept a legacy credential: %q", name, value)
			}
		}
		if headers.Get("X-Emby-Device-Id") != "dev-1" {
			t.Fatalf("device information was lost on load: %#v", headers)
		}
	}

	// The loaded identity must not be usable as a credential either.
	source := svc.ResolvePassthroughHeadersForServer(http.Header{}, "", "server-key")
	if strings.Contains(source.Headers.Get("X-Emby-Authorization"), identitySentinelToken) {
		t.Fatalf("a loaded identity forwarded a legacy credential: %q", source.Headers.Get("X-Emby-Authorization"))
	}

	// Re-saving the same identity must not write the credential back to disk.
	svc.SaveLastSuccess("server-key", svc.GetLastSuccess("server-key"))
	raw, readErr := os.ReadFile(filepath.Join(dir, "data", "captured-headers.json"))
	if readErr != nil {
		t.Fatalf("read back: %v", readErr)
	}
	if strings.Contains(string(raw), identitySentinelToken) || strings.Contains(string(raw), identitySentinelUserID) {
		t.Fatalf("a credential was written back to the capture file")
	}
}

func writeLegacyCapturedFile(t *testing.T, dir string, payload map[string]any) {
	t.Helper()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "captured-headers.json"), encoded, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

// TestAuthorizationParserBoundaries pins the tokenizer's behaviour on the shapes
// a plain comma split gets wrong.
func TestAuthorizationParserBoundaries(t *testing.T) {
	cases := []struct {
		name    string
		header  string
		wantKey string
		wantVal string
		wantOK  bool
	}{
		{
			name:    "a quoted value containing a comma",
			header:  `Emby Device="Living Room, TV", Client="Infuse"`,
			wantKey: "Device",
			wantVal: "Living Room, TV",
			wantOK:  true,
		},
		{
			name:    "an escaped quote inside a quoted value",
			header:  `Emby Device="He said \"hi\"", Client="Infuse"`,
			wantKey: "Device",
			wantVal: `He said "hi"`,
			wantOK:  true,
		},
		{
			name:   "an unterminated quoted value is rejected",
			header: `Emby Device="Broken`,
			wantOK: false,
		},
		{
			name:    "an unquoted value",
			header:  `Emby UserId=abc, Client=Infuse`,
			wantKey: "UserId",
			wantVal: "abc",
			wantOK:  true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parsed, ok := parseAuthorizationIdentityStrict(tc.header)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (%v)", ok, tc.wantOK, parsed)
			}
			if !tc.wantOK {
				return
			}
			if parsed[tc.wantKey] != tc.wantVal {
				t.Fatalf("%s = %q, want %q", tc.wantKey, parsed[tc.wantKey], tc.wantVal)
			}
		})
	}
}

// TestCapturedHeadersPersistDeviceOnly asserts the stored shape itself, not just
// the value read back through an accessor.
func TestCapturedHeadersPersistDeviceOnly(t *testing.T) {
	svc := NewClientIdentityService()
	svc.SetCaptured(identitySentinelToken, http.Header{
		"X-Emby-Client":        {"Infuse"},
		"X-Emby-Device-Id":     {"dev-1"},
		"X-Emby-Authorization": {compoundAuthHeader(identitySentinelUserID, identitySentinelToken, "Infuse", "iPhone", "dev-1", "7.7.1")},
		"X-Emby-Token":         {identitySentinelToken},
		"Authorization":        {"Bearer " + identitySentinelToken},
	})
	captured := svc.GetCaptured(identitySentinelToken)
	for _, key := range []string{"X-Emby-Token", "Authorization"} {
		if captured.Get(key) != "" {
			t.Fatalf("stored header %s = %q, want it dropped", key, captured.Get(key))
		}
	}
	if captured.Get("X-Emby-Device-Id") != "dev-1" {
		t.Fatalf("device id lost: %#v", captured)
	}
	header := captured.Get("X-Emby-Authorization")
	if header != "" {
		parsed, ok := parseAuthorizationIdentityStrict(header)
		if !ok {
			t.Fatalf("stored compound header is unparseable: %q", header)
		}
		if parsed["Token"] != "" || parsed["UserId"] != "" {
			t.Fatalf("stored compound header kept identity: %q", header)
		}
	}
}

// TestCapturedHeaderSanitizationDoesNotChangeRequestState checks that sanitizing
// a copy never mutates the headers the caller still owns.
func TestCapturedHeaderSanitizationDoesNotChangeRequestState(t *testing.T) {
	original := http.Header{
		"X-Emby-Authorization": {compoundAuthHeader(identitySentinelUserID, identitySentinelToken, "Infuse", "iPhone", "dev-1", "7.7.1")},
	}
	before := original.Get("X-Emby-Authorization")
	_ = normalizeCapturedHeaders(original)
	_ = mergePassthroughHeaders(original)
	if original.Get("X-Emby-Authorization") != before {
		t.Fatalf("a sanitizer modified the input headers in place")
	}
}
