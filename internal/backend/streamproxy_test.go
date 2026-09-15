package backend

import (
	"strings"
	"testing"
)

// TestRewriteM3U8ForItemRewritesPathsAndTokens pins the playlist rewriting the stream
// proxy depends on: segment URLs must come back pointing at this proxy, carrying a
// proxy token instead of the upstream api_key.
func TestRewriteM3U8ForItemRewritesPathsAndTokens(t *testing.T) {
	input := "#EXTM3U\nsegment1.ts\nhttps://cdn.example/Videos/123/hls1/main/seg.ts?foo=1&api_key=upstream\n"
	output := RewriteM3U8ForItem(input, "https://upstream.example/Videos/123/master.m3u8", "virtual-item", "proxy-token")

	wantRelative := "https://upstream.example/Videos/virtual-item/segment1.ts?api_key=proxy-token"
	if !strings.Contains(output, wantRelative) {
		t.Fatalf("relative segment not rewritten to %q: %s", wantRelative, output)
	}
	wantAbsolute := "https://cdn.example/Videos/virtual-item/hls1/main/seg.ts"
	if !strings.Contains(output, wantAbsolute) {
		t.Fatalf("absolute segment path not rewritten to %q: %s", wantAbsolute, output)
	}
	if strings.Contains(output, "api_key=upstream") {
		t.Fatalf("upstream api_key should have been replaced: %s", output)
	}
	if !strings.Contains(output, "#EXTM3U") {
		t.Fatalf("playlist header should be preserved: %s", output)
	}
}
