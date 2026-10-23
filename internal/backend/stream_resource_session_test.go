package backend

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestResourceSessionLargeVODAndCapacityIsolation(t *testing.T) {
	s := newStreamResourceStore()
	now := time.Unix(1700000000, 0)
	s.now = func() time.Time { return now }
	var first, tail string
	for user := 0; user < 7; user++ {
		entries := make([]streamResourceEntry, 2400)
		for i := range entries {
			entries[i] = streamResourceEntry{UserID: fmt.Sprint(user), VirtualItemID: "movie", ServerID: "server", OriginalID: "original", ResourceURL: fmt.Sprintf("https://cdn/%d.ts", i)}
		}
		ids, ok := s.registerAll(entries)
		if user == 6 {
			if ok || s.size() != 14400 {
				t.Fatalf("capacity admission ok=%v size=%d", ok, s.size())
			}
			continue
		}
		if !ok {
			t.Fatalf("session %d rejected", user)
		}
		if user == 0 {
			first, tail = ids[0], ids[len(ids)-1]
		} else if ids[0] == first {
			t.Fatal("cross-user handle reused")
		}
	}
	for minute := 20; minute <= 160; minute += 20 {
		now = time.Unix(1700000000, 0).Add(time.Duration(minute) * time.Minute)
		if _, ok := s.get(first); !ok {
			t.Fatal("active session expired")
		}
	}
	if _, ok := s.peek(tail); !ok {
		t.Fatal("unread long VOD tail expired")
	}
	// Other users' idle sessions are removed, never the active VOD's tail.
	if s.size() != 2400 {
		t.Fatalf("idle cleanup size=%d", s.size())
	}
}

func TestResourceDeniedRequestDoesNotRenewSession(t *testing.T) {
	for _, wrongItem := range []bool{false, true} {
		s := newStreamResourceStore()
		start := time.Unix(1700000000, 0)
		now := start
		s.now = func() time.Time { return now }
		id, _ := s.put(streamResourceEntry{UserID: "owner", VirtualItemID: "movie", ResourceURL: "https://cdn/seg"})
		now = start.Add(29 * time.Minute)
		user, item := "other-user", "movie"
		if wrongItem {
			user, item = "owner", "other-movie"
		}
		r := httptest.NewRequest("GET", "/", nil)
		r = r.WithContext(context.WithValue(r.Context(), requestContextKey{}, &RequestContext{ProxyUser: &tokenInfo{UserID: user}}))
		w := httptest.NewRecorder()
		(&App{StreamResources: s}).serveStreamResource(w, r, item, id)
		if w.Code != 403 {
			t.Fatalf("status=%d", w.Code)
		}
		now = start.Add(31 * time.Minute)
		if _, ok := s.peek(id); ok {
			t.Fatal("denied HTTP request renewed lease")
		}
	}
}

func TestResourceRollingWindowCatchupAndAtomicValidation(t *testing.T) {
	a := &App{StreamResources: newStreamResourceStore()}
	now := time.Unix(1700000000, 0)
	a.StreamResources.now = func() time.Time { return now }
	ctx := resourceRewriteContext{UserID: "user", VirtualItemID: "movie", ServerID: "server", OriginalID: "original"}
	old, err := a.RewriteM3U8ForItemWithResources("#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:1\na.ts", "https://cdn/live.m3u8", "", ctx)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimPrefix(strings.Split(old, "\n")[2], "/Videos/movie/eio-resource/")
	now = now.Add(time.Minute)
	if _, err := a.RewriteM3U8ForItemWithResources("#EXTM3U\n#EXT-X-MEDIA-SEQUENCE:2\nb.ts", "https://cdn/live.m3u8", "", ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.StreamResources.peek(id); !ok {
		t.Fatal("catchup resource removed immediately")
	}
	now = now.Add(streamResourceCatchupGrace + time.Second)
	if _, ok := a.StreamResources.peek(id); ok {
		t.Fatal("retired live resource outlived grace")
	}
	if a.StreamResources.size() != 1 {
		t.Fatalf("rolling size=%d", a.StreamResources.size())
	}
	_, err = a.RewriteM3U8ForItemWithResources("#EXTM3U\nnew.ts\njavascript:bad", "https://cdn/live.m3u8", "", ctx)
	if err == nil || a.StreamResources.size() != 1 {
		t.Fatalf("invalid manifest leaked records: err=%v size=%d", err, a.StreamResources.size())
	}
}

func TestResourceConcurrentManifestReuse(t *testing.T) {
	a := &App{StreamResources: newStreamResourceStore()}
	a.StreamResources.max = 2
	ctx := resourceRewriteContext{UserID: "user", VirtualItemID: "movie", ServerID: "server", OriginalID: "original"}
	var wg sync.WaitGroup
	outputs := make(chan string, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := a.RewriteM3U8ForItemWithResources("#EXTM3U\na.ts\nb.ts", "https://cdn/vod.m3u8", "", ctx)
			if err != nil {
				t.Errorf("rewrite: %v", err)
				return
			}
			outputs <- out
		}()
	}
	wg.Wait()
	close(outputs)
	previous := ""
	for out := range outputs {
		if previous != "" && previous != out {
			t.Error("concurrent registration changed handles")
		}
		previous = out
	}
	if a.StreamResources.size() != 2 {
		t.Fatalf("size=%d", a.StreamResources.size())
	}
}

func TestResourceRewritePreservesURIAttributeSyntax(t *testing.T) {
	a := &App{StreamResources: newStreamResourceStore()}
	manifest := strings.Join([]string{
		"#EXTM3U",
		`#EXT-X-KEY:METHOD=AES-128,URI="key.bin",IV=0x1234`,
		`#EXT-X-MAP:URI="init.mp4",BYTERANGE="720@0"`,
		`#EXT-X-I-FRAME-STREAM-INF:BANDWIDTH=128000,URI="iframe.m3u8"`,
		`#EXT-X-SESSION-DATA:DATA-ID="com.example",URI="session.json"`,
		`#EXT-X-PART:DURATION=0.5,URI="part.m4s",INDEPENDENT=YES`,
		`#EXT-X-PRELOAD-HINT:TYPE=PART,URI="next.m4s"`,
		`#EXT-X-RENDITION-REPORT:URI="rendition.m3u8",LAST-MSN=12`,
		`#EXT-X-CONTENT-STEERING:SERVER-URI="steering.json",PATHWAY-ID="cdn-a"`,
	}, "\n")
	got, err := a.RewriteM3U8ForItemWithResources(manifest, "https://cdn.example/Videos/123/master.m3u8", "proxy-token", resourceRewriteContext{
		UserID: "admin-id", VirtualItemID: "movie", ServerID: "server", OriginalID: "original",
	})
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	lines := strings.Split(got, "\n")
	tests := []struct {
		line   int
		prefix string
		suffix string
	}{
		{1, `#EXT-X-KEY:METHOD=AES-128,URI="`, `",IV=0x1234`},
		{2, `#EXT-X-MAP:URI="`, `",BYTERANGE="720@0"`},
		{3, `#EXT-X-I-FRAME-STREAM-INF:BANDWIDTH=128000,URI="`, `"`},
		{4, `#EXT-X-SESSION-DATA:DATA-ID="com.example",URI="`, `"`},
		{5, `#EXT-X-PART:DURATION=0.5,URI="`, `",INDEPENDENT=YES`},
		{6, `#EXT-X-PRELOAD-HINT:TYPE=PART,URI="`, `"`},
		{7, `#EXT-X-RENDITION-REPORT:URI="`, `",LAST-MSN=12`},
		{8, `#EXT-X-CONTENT-STEERING:SERVER-URI="`, `",PATHWAY-ID="cdn-a"`},
	}
	for _, tc := range tests {
		line := lines[tc.line]
		attrs, parseErr := extractHLSURIAttributes(line)
		if parseErr != nil || len(attrs) != 1 {
			t.Fatalf("line %q parsed as %#v, %v", line, attrs, parseErr)
		}
		attr := attrs[0]
		if line[:attr.valueStart] != tc.prefix || line[attr.valueEnd:] != tc.suffix {
			t.Fatalf("attribute boundaries damaged: %q", line)
		}
		if !strings.HasPrefix(attr.value, "/Videos/movie/eio-resource/") || !strings.HasSuffix(attr.value, "?api_key=proxy-token") {
			t.Fatalf("URI not rewritten to an authenticated local handle: %q", attr.value)
		}
	}
}

func TestResourceRewriteRejectsUnsupportedURIsAndRequiresStableIdentity(t *testing.T) {
	a := &App{StreamResources: newStreamResourceStore()}
	ctx := resourceRewriteContext{UserID: "admin", VirtualItemID: "movie", ServerID: "server", OriginalID: "original"}
	if _, err := a.RewriteM3U8ForItemWithResources(`#EXT-X-UNKNOWN:URI="https://cdn.example/list.m3u8"`, "https://cdn.example/master.m3u8", "", ctx); err != errUnsupportedHLSURIAttribute {
		t.Fatalf("unsupported tag error = %v", err)
	}
	if _, err := a.RewriteM3U8ForItemWithResources("skd://key", "https://cdn.example/master.m3u8", "", ctx); err != errUnsupportedResourceScheme {
		t.Fatalf("unsupported scheme error = %v", err)
	}
	adminContext := &RequestContext{ProxyUser: &tokenInfo{Role: "admin"}}
	if got := streamResourceUserID(adminContext); got != "admin" || got != streamResourceUserID(adminContext) {
		t.Fatalf("admin resource identity = %q, want stable admin identity", got)
	}
	if _, err := a.RewriteM3U8ForItemWithResources("segment.ts", "https://cdn.example/master.m3u8", "", resourceRewriteContext{}); err != errMissingResourceIdentity {
		t.Fatalf("missing identity error = %v", err)
	}
}

func TestResourceCapacityErrorIsRetryable(t *testing.T) {
	w := httptest.NewRecorder()
	writeStreamResourceRewriteError(w, errResourceStoreFull)
	if w.Code != 503 || w.Header().Get("Retry-After") != "5" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("capacity response = status %d, headers %#v", w.Code, w.Header())
	}
}
