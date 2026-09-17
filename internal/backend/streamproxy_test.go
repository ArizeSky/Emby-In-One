package backend

import (
	"strings"
	"testing"
)

// TestRewriteM3U8ForItemRewritesPathsAndTokens pins the playlist rewriting the stream
// proxy depends on: segment URLs must come back as proxy-relative paths that carry
// the virtual item id and a proxy token instead of the upstream api_key. No host —
// upstream, CDN or localhost — may appear in what the client reads, because the
// client resolves the paths against the proxy it fetched the manifest from.
func TestRewriteM3U8ForItemRewritesPathsAndTokens(t *testing.T) {
	input := "#EXTM3U\nsegment1.ts\nhttps://cdn.example/Videos/123/hls1/main/seg.ts?foo=1&api_key=upstream\n"
	output := RewriteM3U8ForItem(input, "https://upstream.example/Videos/123/master.m3u8?api_key=upstream", "virtual-item", "proxy-token")

	wantRelative := "/Videos/virtual-item/segment1.ts?api_key=proxy-token"
	if !strings.Contains(output, wantRelative) {
		t.Fatalf("relative segment not rewritten to %q: %s", wantRelative, output)
	}
	wantAbsolute := "/Videos/virtual-item/hls1/main/seg.ts?api_key=proxy-token&foo=1"
	if !strings.Contains(output, wantAbsolute) {
		t.Fatalf("absolute segment not rewritten to %q: %s", wantAbsolute, output)
	}
	if strings.Contains(output, "upstream.example") || strings.Contains(output, "cdn.example") {
		t.Fatalf("a host survived into the client-facing manifest: %s", output)
	}
	if strings.Contains(output, "api_key=upstream") {
		t.Fatalf("upstream api_key should have been replaced: %s", output)
	}
	if !strings.Contains(output, "#EXTM3U") {
		t.Fatalf("playlist header should be preserved: %s", output)
	}
}

// A line the proxy cannot route (no /Videos/{id} or /Audio/{id} shape) is passed
// through with the upstream credential removed — never with the token intact.
func TestRewriteM3U8ForItemStripsCredentialsFromUnroutableLines(t *testing.T) {
	input := "#EXTM3U\nhttps://cdn.example/assets/seg.ts?api_key=upstream&sig=1\n"
	output := RewriteM3U8ForItem(input, "https://upstream.example/Videos/123/master.m3u8", "virtual-item", "proxy-token")

	want := "https://cdn.example/assets/seg.ts?sig=1"
	if !strings.Contains(output, want) {
		t.Fatalf("unroutable line should keep its host and non-credential query: %s", output)
	}
	if strings.Contains(output, "api_key") {
		t.Fatalf("upstream api_key must not reach the client: %s", output)
	}
}

// An upstream deployed under a path prefix (a reverse proxy exposing /emby) must
// not leak that prefix into the proxy-relative output: this proxy's stream routes
// are rooted at "/".
func TestRewriteM3U8ForItemDropsUpstreamPathPrefix(t *testing.T) {
	input := "#EXTM3U\nhls1/main/seg.ts\n"
	output := RewriteM3U8ForItem(input, "https://upstream.example/emby/Videos/123/master.m3u8", "virtual-item", "proxy-token")

	want := "/Videos/virtual-item/hls1/main/seg.ts?api_key=proxy-token"
	if !strings.Contains(output, want) {
		t.Fatalf("prefix not dropped or segment not rewritten: %s", output)
	}
	if strings.Contains(output, "/emby/") {
		t.Fatalf("the upstream deployment prefix survived: %s", output)
	}
}
