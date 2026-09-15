package backend

import (
	"net/http"
	"testing"
)

func TestClientIdentityServiceIsTokenScoped(t *testing.T) {
	svc := NewClientIdentityService()
	svc.SetCaptured("token-a", http.Header{
		"User-Agent":            []string{"UA-A"},
		"X-Emby-Client":         []string{"Client-A"},
		"X-Emby-Device-Name":    []string{"Device-A"},
		"X-Emby-Device-Id":      []string{"device-a"},
		"X-Emby-Client-Version": []string{"1.0"},
	})
	svc.SetCaptured("token-b", http.Header{
		"User-Agent":            []string{"UA-B"},
		"X-Emby-Client":         []string{"Client-B"},
		"X-Emby-Device-Name":    []string{"Device-B"},
		"X-Emby-Device-Id":      []string{"device-b"},
		"X-Emby-Client-Version": []string{"2.0"},
	})

	if got := svc.GetCaptured("token-a").Get("User-Agent"); got != "UA-A" {
		t.Fatalf("token-a user agent = %q, want UA-A", got)
	}
	if got := svc.GetCaptured("token-b").Get("User-Agent"); got != "UA-B" {
		t.Fatalf("token-b user agent = %q, want UA-B", got)
	}
	info := svc.GetInfo()
	if info == nil || info.UserAgent != "UA-B" {
		t.Fatalf("latest captured info = %#v, want UA-B", info)
	}
}

func TestResolvePassthroughHeadersPreferenceOrder(t *testing.T) {
	svc := NewClientIdentityService()
	svc.SetCaptured("token-a", http.Header{
		"User-Agent":            []string{"UA-A"},
		"X-Emby-Client":         []string{"Client-A"},
		"X-Emby-Device-Name":    []string{"Device-A"},
		"X-Emby-Device-Id":      []string{"device-a"},
		"X-Emby-Client-Version": []string{"1.0"},
	})

	source, headers := callResolvePassthroughHeadersForServer(t, svc, http.Header{}, "token-a", "")
	if source != "captured-token" {
		t.Fatalf("source = %q, want captured-token", source)
	}
	if got := headers.Get("X-Emby-Client"); got != "Client-A" {
		t.Fatalf("client = %q, want Client-A", got)
	}

	live := http.Header{
		"User-Agent":            []string{"Live-UA"},
		"X-Emby-Client":         []string{"Live-Client"},
		"X-Emby-Device-Name":    []string{"Live Device"},
		"X-Emby-Device-Id":      []string{"live-device"},
		"X-Emby-Client-Version": []string{"9.9"},
	}
	source, headers = callResolvePassthroughHeadersForServer(t, svc, live, "token-a", "")
	if source != "live-request" {
		t.Fatalf("source = %q, want live-request", source)
	}
	if got := headers.Get("X-Emby-Client"); got != "Live-Client" {
		t.Fatalf("client = %q, want Live-Client", got)
	}

	svc.DeleteCaptured("token-a")
	source, headers = callResolvePassthroughHeadersForServer(t, svc, http.Header{}, "token-a", "")
	if source != "infuse-fallback" {
		t.Fatalf("source = %q, want infuse-fallback", source)
	}
	if got := headers.Get("X-Emby-Client"); got != "Infuse" {
		t.Fatalf("fallback client = %q, want Infuse", got)
	}
}

func TestParseAuthorizationIdentity(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   map[string]string
	}{
		{
			name:   "MediaBrowser format",
			header: `MediaBrowser Client="Hills", Device="Pixel 7", DeviceId="abc123", Version="2.0.1", Token="xyz"`,
			want: map[string]string{
				"Client": "Hills", "Device": "Pixel 7",
				"DeviceId": "abc123", "Version": "2.0.1",
			},
		},
		{
			name:   "Emby format",
			header: `Emby UserId="", Client="Infuse", Device="iPhone", DeviceId="dev1", Version="7.7.1"`,
			want: map[string]string{
				"Client": "Infuse", "Device": "iPhone",
				"DeviceId": "dev1", "Version": "7.7.1",
			},
		},
		{
			name:   "unquoted values",
			header: `MediaBrowser Client=TestApp, Device=TestDevice, DeviceId=id1, Version=1.0`,
			want: map[string]string{
				"Client": "TestApp", "Device": "TestDevice",
				"DeviceId": "id1", "Version": "1.0",
			},
		},
		{
			name:   "empty string",
			header: "",
			want:   map[string]string{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseAuthorizationIdentity(tt.header)
			for key, want := range tt.want {
				if got[key] != want {
					t.Errorf("%s = %q, want %q", key, got[key], want)
				}
			}
		})
	}
}

func TestNormalizeCapturedHeadersExtractsFromAuthorization(t *testing.T) {
	input := http.Header{
		"User-Agent":           []string{"Hills/2.0 Android"},
		"X-Emby-Authorization": []string{`MediaBrowser Client="Hills", Device="Pixel 7", DeviceId="abc123", Version="2.0.1", Token="xyz"`},
	}
	got := normalizeCapturedHeaders(input)
	if got.Get("X-Emby-Client") != "Hills" {
		t.Errorf("X-Emby-Client = %q, want Hills", got.Get("X-Emby-Client"))
	}
	if got.Get("X-Emby-Device-Name") != "Pixel 7" {
		t.Errorf("X-Emby-Device-Name = %q, want Pixel 7", got.Get("X-Emby-Device-Name"))
	}
	if got.Get("X-Emby-Device-Id") != "abc123" {
		t.Errorf("X-Emby-Device-Id = %q, want abc123", got.Get("X-Emby-Device-Id"))
	}
	if got.Get("X-Emby-Client-Version") != "2.0.1" {
		t.Errorf("X-Emby-Client-Version = %q, want 2.0.1", got.Get("X-Emby-Client-Version"))
	}
}

func TestNormalizeCapturedHeadersDoesNotOverrideExplicitHeaders(t *testing.T) {
	input := http.Header{
		"User-Agent":            []string{"Hills/2.0 Android"},
		"X-Emby-Client":         []string{"ExplicitClient"},
		"X-Emby-Device-Name":    []string{"ExplicitDevice"},
		"X-Emby-Device-Id":      []string{"explicit-id"},
		"X-Emby-Client-Version": []string{"9.9"},
		"X-Emby-Authorization":  []string{`MediaBrowser Client="Hills", Device="Pixel 7", DeviceId="abc123", Version="2.0.1"`},
	}
	got := normalizeCapturedHeaders(input)
	if got.Get("X-Emby-Client") != "ExplicitClient" {
		t.Errorf("explicit header overridden: X-Emby-Client = %q", got.Get("X-Emby-Client"))
	}
	if got.Get("X-Emby-Device-Name") != "ExplicitDevice" {
		t.Errorf("explicit header overridden: X-Emby-Device-Name = %q", got.Get("X-Emby-Device-Name"))
	}
}

func TestPassthroughEndToEndWithAuthorizationOnly(t *testing.T) {
	svc := NewClientIdentityService()
	svc.SetCaptured("token-hills", http.Header{
		"User-Agent":           []string{"Hills/2.0 Android"},
		"X-Emby-Authorization": []string{`MediaBrowser Client="Hills", Device="Pixel 7", DeviceId="abc123", Version="2.0.1", Token="xyz"`},
	})
	source, headers := callResolvePassthroughHeadersForServer(t, svc, http.Header{}, "token-hills", "")
	if source != "captured-token" {
		t.Fatalf("source = %q, want captured-token", source)
	}
	if got := headers.Get("X-Emby-Client"); got != "Hills" {
		t.Errorf("X-Emby-Client = %q, want Hills", got)
	}
	if got := headers.Get("X-Emby-Device-Name"); got != "Pixel 7" {
		t.Errorf("X-Emby-Device-Name = %q, want Pixel 7", got)
	}
	if got := headers.Get("X-Emby-Device-Id"); got != "abc123" {
		t.Errorf("X-Emby-Device-Id = %q, want abc123", got)
	}
	if got := headers.Get("User-Agent"); got != "Hills/2.0 Android" {
		t.Errorf("User-Agent = %q, want Hills/2.0 Android", got)
	}
}
