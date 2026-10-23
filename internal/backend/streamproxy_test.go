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

func TestRewriteM3U8QuotedURIAttributesPreserveSyntaxAndCoverage(t *testing.T) {
	input := strings.Join([]string{
		"#EXTM3U",
		`#EXT-X-KEY:METHOD=AES-128,URI="key.bin",IV=0x1234`,
		`#EXT-X-MAP:URI="init.mp4",BYTERANGE="720@0"`,
		`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio",URI="audio.m3u8"`,
		`#EXT-X-I-FRAME-STREAM-INF:BANDWIDTH=128000,URI="iframe.m3u8"`,
		`#EXT-X-SESSION-DATA:DATA-ID="com.example",URI="session.json"`,
		`#EXT-X-PART:DURATION=0.5,URI="part.m4s",INDEPENDENT=YES`,
		`#EXT-X-PRELOAD-HINT:TYPE=PART,URI="next.m4s"`,
		`#EXT-X-RENDITION-REPORT:URI="rendition.m3u8",LAST-MSN=12`,
		`#EXT-X-CONTENT-STEERING:SERVER-URI="steering.json",PATHWAY-ID="cdn-a"`,
	}, "\n")
	want := strings.Join([]string{
		"#EXTM3U",
		`#EXT-X-KEY:METHOD=AES-128,URI="/Videos/virtual-item/key.bin?api_key=proxy-token",IV=0x1234`,
		`#EXT-X-MAP:URI="/Videos/virtual-item/init.mp4?api_key=proxy-token",BYTERANGE="720@0"`,
		`#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="audio",URI="/Videos/virtual-item/audio.m3u8?api_key=proxy-token"`,
		`#EXT-X-I-FRAME-STREAM-INF:BANDWIDTH=128000,URI="/Videos/virtual-item/iframe.m3u8?api_key=proxy-token"`,
		`#EXT-X-SESSION-DATA:DATA-ID="com.example",URI="/Videos/virtual-item/session.json?api_key=proxy-token"`,
		`#EXT-X-PART:DURATION=0.5,URI="/Videos/virtual-item/part.m4s?api_key=proxy-token",INDEPENDENT=YES`,
		`#EXT-X-PRELOAD-HINT:TYPE=PART,URI="/Videos/virtual-item/next.m4s?api_key=proxy-token"`,
		`#EXT-X-RENDITION-REPORT:URI="/Videos/virtual-item/rendition.m3u8?api_key=proxy-token",LAST-MSN=12`,
		`#EXT-X-CONTENT-STEERING:SERVER-URI="/Videos/virtual-item/steering.json?api_key=proxy-token",PATHWAY-ID="cdn-a"`,
	}, "\n")
	got, err := RewriteM3U8ForItemStrict(input, "https://upstream.example/Videos/123/master.m3u8", "virtual-item", "proxy-token")
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if got != want {
		t.Fatalf("rewritten manifest syntax mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestRewriteM3U8RejectsUnknownURIAttributeTag(t *testing.T) {
	input := `#EXT-X-UNKNOWN:URI="https://attacker.example/playlist.m3u8"`
	if _, err := RewriteM3U8ForItemStrict(input, "https://upstream.example/master.m3u8", "item", "token"); err != errUnsupportedHLSURIAttribute {
		t.Fatalf("unknown URI-bearing tag error = %v, want %v", err, errUnsupportedHLSURIAttribute)
	}
}
