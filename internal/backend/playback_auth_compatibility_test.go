package backend

import (
	"net/http"
	"testing"
)

func TestPlaybackAuthMetadataRefreshAndPrincipalIsolation(t *testing.T) {
	c := newUpstreamClient(Config{}, UpstreamConfig{ID: "fixture", URL: "https://fixture.invalid", Username: "fixture", SpoofClient: "passthrough"}, 0, nil)
	identity := identitySnapshot{Source: "live-request", Headers: http.Header{"User-Agent": {"fixture-player"}, "X-Emby-Client": {"fixture"}, "X-Emby-Device-Id": {"device"}}}
	c.setOnlineWithIdentity("token-A", "principal-A", identity)
	first := c.authSnapshot()
	identity.Source = "last-success"
	c.setOnlineWithIdentity("token-B", "principal-A", identity)
	second := c.authSnapshot()
	if !first.Compatibility.compatible(second.Compatibility) || first.AccessToken == second.AccessToken || first.Generation >= second.Generation {
		t.Fatal("same principal and identity refresh must retain compatibility, not the old token")
	}
	c.setOnlineWithIdentity("token-C", "principal-B", identity)
	if first.Compatibility.compatible(c.authSnapshot().Compatibility) {
		t.Fatal("changed upstream principal remained compatible")
	}
	identity.Headers.Set("X-Emby-Device-Id", "other-device")
	c.setOnlineWithIdentity("token-D", "principal-A", identity)
	if first.Compatibility.compatible(c.authSnapshot().Compatibility) {
		t.Fatal("changed identity remained compatible")
	}
	c.setOffline("fixture-failure")
	if c.authSnapshot().Compatibility.compatible(first.Compatibility) {
		t.Fatal("offline auth metadata retained permission")
	}
	c.setOnline("legacy-token", "principal-A")
	if c.authSnapshot().Compatibility.compatible(first.Compatibility) {
		t.Fatal("unknown legacy credential implicitly attested identity")
	}
}
