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
