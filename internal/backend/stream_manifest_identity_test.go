package backend

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

func hlsWindowResourceID(t *testing.T, body string) string {
	t.Helper()
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "/Videos/v/eio-resource/") {
			return strings.TrimPrefix(strings.Split(line, "?")[0], "/Videos/v/eio-resource/")
		}
	}
	t.Fatalf("no resource in rewritten playlist: %s", body)
	return ""
}

func TestHLSWindowSignedRedirectsRetireOnlyTheirOwnTrack(t *testing.T) {
	a := &App{StreamResources: newStreamResourceStore()}
	now := time.Unix(1700000000, 0)
	a.StreamResources.now = func() time.Time { return now }
	ctx := resourceRewriteContext{UserID: "u", VirtualItemID: "v", ServerID: "s", OriginalID: "o"}
	rewrite := func(track, signature, segment, token string) string {
		t.Helper()
		ctx.ManifestKey = streamManifestWindowKey("/Videos/o/live.m3u8", url.Values{"track": {track}, "api_key": {token}})
		body, err := a.RewriteM3U8ForItemWithResources("#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:1\n"+segment, "https://cdn.example/live.m3u8?signature="+signature, "", ctx)
		if err != nil {
			t.Fatal(err)
		}
		return hlsWindowResourceID(t, body)
	}
	oldAudio := rewrite("audio", "old-signature", "audio1.ts", "proxy-token-1")
	video := rewrite("video", "video-signature", "video.ts", "proxy-token-1")
	now = now.Add(time.Second)
	audio := rewrite("audio", "rotated-signature", "audio2.ts", "proxy-token-2")
	now = now.Add(streamResourceCatchupGrace + time.Second)
	if _, ok := a.StreamResources.peek(oldAudio); ok {
		t.Fatal("stable audio window did not retire its old segment after CDN signature rotation")
	}
	for _, id := range []string{audio, video} {
		if _, ok := a.StreamResources.peek(id); !ok {
			t.Fatal("current sibling track resource retired")
		}
	}
}

func TestHLSWindowCapacityIsAtomicAndReclaimed(t *testing.T) {
	a := &App{StreamResources: newStreamResourceStore()}
	a.StreamResources.max = 3
	now := time.Unix(1700000000, 0)
	a.StreamResources.now = func() time.Time { return now }
	ctx := resourceRewriteContext{UserID: "u", VirtualItemID: "v", ServerID: "s", OriginalID: "o"}
	for i := 0; i < 3; i++ {
		if _, err := a.RewriteM3U8ForItemWithResources("#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:1\nshared.ts", fmt.Sprintf("https://cdn/live.m3u8?track=%d", i), "", ctx); err != nil {
			t.Fatal(err)
		}
	}
	if a.StreamResources.size() != 1 || a.StreamResources.manifestCount != 3 {
		t.Fatal("fixture did not share a resource across three distinct windows")
	}
	_, err := a.RewriteM3U8ForItemWithResources("#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:1\nnew.ts", "https://cdn/live.m3u8?track=4", "", ctx)
	if err != errResourceStoreFull || a.StreamResources.size() != 1 || a.StreamResources.manifestCount != 3 {
		t.Fatalf("window capacity changed state before rejecting: %v", err)
	}
	now = now.Add(streamResourceIdleTTL + time.Second)
	if _, err := a.RewriteM3U8ForItemWithResources("#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:1\nnew.ts", "https://cdn/live.m3u8?track=4", "", ctx); err != nil {
		t.Fatal(err)
	}
	if a.StreamResources.size() != 1 || a.StreamResources.manifestCount != 1 {
		t.Fatal("expired sessions did not release window capacity")
	}
}
