package backend

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Configured stream origins are trusted independently of the first request's
// origin. Crossing a CDN does not transfer private identity to it, nor prevent
// identity restoration on an explicitly configured origin later in the chain.
func TestAutomaticIdentityConfiguredOriginRoundTrip(t *testing.T) {
	var finalHeaders http.Header
	line := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		finalHeaders = r.Header.Clone()
		_, _ = io.WriteString(w, "data")
	}))
	defer line.Close()
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, name := range []string{"X-Emby-Token", "X-Emby-Client", "X-Emby-Device-Id", "X-Emby-Authorization", "Cookie"} {
			if r.Header.Get(name) != "" {
				t.Errorf("external hop received %s", name)
			}
		}
		http.Redirect(w, r, line.URL+"/file", http.StatusTemporaryRedirect)
	}))
	defer cdn.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, cdn.URL+"/file", http.StatusTemporaryRedirect)
	}))
	defer api.Close()
	client := hopCustomClient(t, api.URL, true)
	client.StreamBaseURLs = []string{api.URL, line.URL}
	resp, err := client.Stream(context.Background(), nil, nil, "/Videos/item/stream.mp4", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for name, want := range map[string]string{"User-Agent": "hop-player/1", "X-Emby-Client": "hop-client", "X-Emby-Device-Id": "hop-device-id", "X-Emby-Token": hopUpstreamToken} {
		if finalHeaders.Get(name) != want {
			t.Errorf("configured return hop lost %s", name)
		}
	}
}
