package backend

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const timelinePlaylist = "#EXTM3U\n#EXTINF:3,\n0.mp4\n#EXTINF:3,\n1.mp4\n#EXT-X-ENDLIST\n"

func TestHLSTimelineCacheRenditionsAndEviction(t *testing.T) {
	storeHLSSegmentPositions("renditions", "video/main.m3u8", timelinePlaylist)
	storeHLSSegmentPositions("renditions", "audio/main.m3u8", strings.Replace(timelinePlaylist, "#EXTINF:3,", "#EXTINF:2,", -1))
	for rest, want := range map[string]float64{"video/1.mp4": 3, "audio/1.mp4": 2} {
		if got, ok := hlsSegmentStartPosition("renditions", rest); !ok || got != want {
			t.Fatalf("%s: %v %v", rest, got, ok)
		}
	}
	for i := 0; i <= hlsPositionCacheLimit; i++ {
		storeHLSSegmentPositions(fmt.Sprintf("capacity-%d", i), "main.m3u8", timelinePlaylist)
	}
	if _, ok := hlsSegmentStartPosition(fmt.Sprintf("capacity-%d", hlsPositionCacheLimit), "1.mp4"); !ok {
		t.Fatal("new session was lost when the cache reached capacity")
	}
	if _, ok := hlsSegmentStartPosition("capacity-0", "1.mp4"); ok {
		t.Fatal("oldest entry was not evicted")
	}
}

func TestHLSTimelineCacheTTLAndUnsupportedPlaylists(t *testing.T) {
	storeHLSSegmentPositions("ttl-test", "main.m3u8", timelinePlaylist)
	hlsPositionCache.Lock()
	hlsPositionCache.entries["ttl-test"].stored = time.Now().Add(-hlsPositionCacheTTL - time.Second)
	hlsPositionCache.Unlock()
	if _, ok := hlsSegmentStartPosition("ttl-test", "1.mp4"); ok {
		t.Fatal("expired timeline was used")
	}
	for i, manifest := range []string{strings.ReplaceAll(timelinePlaylist, "#EXT-X-ENDLIST", ""), "#EXT-X-DISCONTINUITY\n" + timelinePlaylist, "#EXT-X-BYTERANGE:100@0\n" + timelinePlaylist} {
		key := fmt.Sprintf("unsupported-%d", i)
		storeHLSSegmentPositions(key, "main.m3u8", manifest)
		if _, ok := hlsSegmentStartPosition(key, "1.mp4"); ok {
			t.Fatal("unsupported timeline was cached")
		}
	}
	storeHLSSegmentPositions("absolute-path", "video/main.m3u8", strings.ReplaceAll(timelinePlaylist, "1.mp4", "/Videos/item/video/1.mp4?x=1"))
	if got, ok := hlsSegmentStartPosition("absolute-path", "video/1.mp4"); !ok || got != 3 {
		t.Fatal("absolute segment path did not match")
	}
}

func TestFMP4TimelinePreservesAVOffsetAndIndex(t *testing.T) {
	segment := testSegment(0, 4410) // audio starts 100 ms after video
	if !rewriteFMP4Timeline(segment, 300) {
		t.Fatal("not rewritten")
	}
	if got := tfdtValueAt(t, segment, 2); got != 300*44100+4410 {
		t.Fatalf("audio offset lost: %d", got)
	}
	forEachBox(segment, 0, len(segment), func(kind string, start, end int) bool {
		if kind == "sidx" {
			scale := binary.BigEndian.Uint32(segment[start+8:])
			if got := binary.BigEndian.Uint32(segment[start+12:]); got != 300*scale {
				t.Fatalf("sidx presentation time = %d", got)
			}
		}
		return true
	})
	before := bytes.Clone(segment)
	if rewriteFMP4Timeline(segment, 300) || !bytes.Equal(segment, before) {
		t.Fatal("rewrite was not idempotent")
	}
}

func TestFMP4TimelineOverflowIsAtomicAndPositiveOriginSurvives(t *testing.T) {
	segment := testSegment(0, mathMaxUint32-100)
	before := bytes.Clone(segment)
	if rewriteFMP4Timeline(segment, 3) || !bytes.Equal(before, segment) {
		t.Fatal("only one track was rewritten on overflow")
	}
	segment = testSegment(90*12800, 90*44100)
	before = bytes.Clone(segment)
	if rewriteFMP4Timeline(segment, 3) || !bytes.Equal(before, segment) {
		t.Fatal("valid positive time origin was reset")
	}
}

const mathMaxUint32 = uint64(1<<32 - 1)

type failingSegmentReader struct{ sent bool }

func (r *failingSegmentReader) Read(p []byte) (int, error) {
	if r.sent {
		return 0, io.ErrUnexpectedEOF
	}
	r.sent = true
	return copy(p, "partial"), nil
}
func (r *failingSegmentReader) Close() error { return nil }

func TestHLSSegmentFailedReadIsNotSuccessfulPartialBody(t *testing.T) {
	storeHLSSegmentPositions("read-error", "main.m3u8", timelinePlaylist)
	resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: &failingSegmentReader{}, ContentLength: -1}
	if _, ok, err := rewriteHLSSegmentBody(httptest.NewRequest("GET", "/", nil), resp, "read-error", "1.mp4"); ok || err == nil {
		t.Fatal("partial failed read was returned as successful")
	}
}

func TestHLSTimelineProxyResolvesVirtualSessionEndToEnd(t *testing.T) {
	original := testMoofBox(testTrafBox(1, 1, 0))
	original = append(testSidxBox(1, 12800), original...)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "upstream-token", "User": map[string]any{"Id": "upstream-user"}})
		case strings.HasSuffix(r.URL.Path, "/main.m3u8"):
			w.Header().Set("Content-Type", "application/x-mpegURL")
			_, _ = io.WriteString(w, strings.ReplaceAll(timelinePlaylist, ".mp4", ".mp4?PlaySessionId=raw-session"))
		case strings.HasSuffix(r.URL.Path, "/1.mp4"):
			if r.URL.Query().Get("PlaySessionId") != "raw-session" {
				t.Error("virtual session leaked upstream")
			}
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Content-Length", fmt.Sprint(len(original)))
			_, _ = w.Write(original)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	withTempAppConfig(t, singleUpstreamConfig(upstream.URL), func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		serverID := app.Upstream.Clients()[0].ID
		item := app.IDStore.GetOrCreateVirtualID("original-item", serverID)
		session := app.IDStore.GetOrCreateVirtualID("raw-session", serverID)
		prefix := "/Videos/" + item + "/video/"
		rr := doJSONRequest(t, handler, "GET", prefix+"main.m3u8?PlaySessionId="+session+"&api_key="+token, nil, "")
		if rr.Code != 200 {
			t.Fatalf("manifest status=%d", rr.Code)
		}
		rr = doJSONRequest(t, handler, "GET", prefix+"1.mp4?PlaySessionId="+session+"&api_key="+token, nil, "")
		if rr.Code != 200 || rr.Body.Len() != len(original) {
			t.Fatalf("segment status/length=%d/%d", rr.Code, rr.Body.Len())
		}
		if got := tfdtValueAt(t, rr.Body.Bytes(), 1); got != 3*12800 {
			t.Fatalf("64-bit tfdt=%d", got)
		}
		if tfdtValueAt(t, original, 1) != 0 {
			t.Fatal("upstream fixture was mutated")
		}
	})
}

func TestHLSManifestFailedReadReturnsBadGateway(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "upstream-token", "User": map[string]any{"Id": "upstream-user"}})
		case strings.HasSuffix(r.URL.Path, "/main.m3u8"):
			w.Header().Set("Content-Type", "application/x-mpegURL")
			// Declare more bytes than the handler writes: the connection closes
			// mid-body, so the proxy's manifest read fails with ErrUnexpectedEOF.
			w.Header().Set("Content-Length", "1024")
			_, _ = io.WriteString(w, "#EXTM3U\n#EXTINF:3,\n0.mp4\n")
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	withTempAppConfig(t, singleUpstreamConfig(upstream.URL), func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		serverID := app.Upstream.Clients()[0].ID
		item := app.IDStore.GetOrCreateVirtualID("original-item", serverID)
		rr := doJSONRequest(t, handler, "GET", "/Videos/"+item+"/video/main.m3u8?api_key="+token, nil, "")
		if rr.Code != http.StatusBadGateway {
			t.Fatalf("manifest status=%d body=%q", rr.Code, rr.Body.String())
		}
	})
}

func TestHLSManifestOversizedBodyReturnsBadGateway(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "upstream-token", "User": map[string]any{"Id": "upstream-user"}})
		case strings.HasSuffix(r.URL.Path, "/main.m3u8"):
			w.Header().Set("Content-Type", "application/x-mpegURL")
			_, _ = io.WriteString(w, strings.Repeat("#EXTM3U\n", maxHLSManifestSize/8+2))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	withTempAppConfig(t, singleUpstreamConfig(upstream.URL), func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		serverID := app.Upstream.Clients()[0].ID
		item := app.IDStore.GetOrCreateVirtualID("original-item", serverID)
		rr := doJSONRequest(t, handler, "GET", "/Videos/"+item+"/video/main.m3u8?api_key="+token, nil, "")
		if rr.Code != http.StatusBadGateway {
			t.Fatalf("manifest status=%d", rr.Code)
		}
	})
}
