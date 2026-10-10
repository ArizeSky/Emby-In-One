package backend

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// The helpers below build minimal fMP4 segments in Emby's transcode shape:
// styp + one sidx per track + moof(traf(tfhd, tfdt)) + mdat.

func testBox(boxType string, payload []byte) []byte {
	buf := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(buf, uint32(len(buf)))
	copy(buf[4:], boxType)
	copy(buf[8:], payload)
	return buf
}

func testSidxBox(referenceID, timescale uint32) []byte {
	payload := make([]byte, 24)
	payload[0] = 0 // version 0
	binary.BigEndian.PutUint32(payload[4:], referenceID)
	binary.BigEndian.PutUint32(payload[8:], timescale)
	return testBox("sidx", payload)
}

func testTfhdBox(trackID uint32) []byte {
	payload := make([]byte, 8)
	binary.BigEndian.PutUint32(payload[4:], trackID)
	return testBox("tfhd", payload)
}

func testTfdtBox(version byte, value uint64) []byte {
	if version == 1 {
		payload := make([]byte, 12)
		payload[0] = 1
		binary.BigEndian.PutUint64(payload[4:], value)
		return testBox("tfdt", payload)
	}
	payload := make([]byte, 8)
	binary.BigEndian.PutUint32(payload[4:], uint32(value))
	return testBox("tfdt", payload)
}

func testTrafBox(trackID uint32, tfdtVersion byte, tfdtValue uint64) []byte {
	return testBox("traf", append(testTfhdBox(trackID), testTfdtBox(tfdtVersion, tfdtValue)...))
}

func testMoofBox(tracks ...[]byte) []byte {
	var payload []byte
	for _, track := range tracks {
		payload = append(payload, track...)
	}
	return testBox("moof", payload)
}

func testSegment(videoTfdt, audioTfdt uint64) []byte {
	segment := append(testBox("styp", make([]byte, 8)), testSidxBox(1, 12800)...)
	segment = append(segment, testSidxBox(2, 44100)...)
	segment = append(segment, testMoofBox(
		testTrafBox(1, 0, videoTfdt),
		testTrafBox(2, 0, audioTfdt),
	)...)
	return append(segment, testBox("mdat", make([]byte, 16))...)
}

// tfdtValueAt finds the tfdt value of the given track in a synthetic segment.
// tfdtValueAt finds the last tfdt value of the given track in a synthetic segment.
func tfdtValueAt(t *testing.T, segment []byte, trackID uint32) uint64 {
	t.Helper()
	var value uint64
	found := false
	forEachBox(segment, 0, len(segment), func(boxType string, payloadStart, payloadEnd int) bool {
		if boxType != "moof" {
			return true
		}
		forEachBox(segment, payloadStart, payloadEnd, func(child string, childStart, childEnd int) bool {
			if child != "traf" {
				return true
			}
			var id uint32
			forEachBox(segment, childStart, childEnd, func(sub string, subStart, subEnd int) bool {
				if sub == "tfhd" {
					id = binary.BigEndian.Uint32(segment[subStart+4:])
				}
				if sub == "tfdt" && id == trackID {
					if segment[subStart] == 1 {
						value = binary.BigEndian.Uint64(segment[subStart+4:])
					} else {
						value = uint64(binary.BigEndian.Uint32(segment[subStart+4:]))
					}
					found = true
				}
				return true
			})
			return true
		})
		return true
	})
	if !found {
		t.Fatalf("no tfdt found for track %d", trackID)
	}
	return value
}

// An Emby-style segment whose tfdt restarts at zero must be shifted so both
// tracks start at the position the playlist assigns the segment.
func TestRewriteFMP4TimelineShiftsZeroBasedSegment(t *testing.T) {
	segment := testSegment(0, 0)
	if !rewriteFMP4Timeline(segment, 9.0) {
		t.Fatalf("expected the segment to be rewritten")
	}
	if got := tfdtValueAt(t, segment, 1); got != 9*12800 {
		t.Fatalf("video tfdt = %d, want %d", got, 9*12800)
	}
	if got := tfdtValueAt(t, segment, 2); got != 9*44100 {
		t.Fatalf("audio tfdt = %d, want %d", got, 9*44100)
	}
}

// A segment that already carries the playlist position must pass through
// byte-identical, and so must one that is merely close to it (a small
// intentional audio/video offset).
func TestRewriteFMP4TimelineLeavesContinuousSegmentAlone(t *testing.T) {
	segment := testSegment(9*12800, 9*44100)
	before := append([]byte(nil), segment...)
	if rewriteFMP4Timeline(segment, 9.0) {
		t.Fatalf("a continuous segment must not be rewritten")
	}
	if !bytes.Equal(before, segment) {
		t.Fatalf("a continuous segment must stay byte-identical")
	}

	offset := testSegment(9*12800, 9*44100-4410) // audio 0.1s early
	before = append([]byte(nil), offset...)
	if rewriteFMP4Timeline(offset, 9.0) {
		t.Fatalf("a sub-second offset must not be rewritten")
	}
	if !bytes.Equal(before, offset) {
		t.Fatalf("a sub-second offset must stay byte-identical")
	}
}

// A segment with several moof boxes keeps the spacing between its fragments:
// every tfdt of the track shifts by the same delta.
func TestRewriteFMP4TimelineShiftsEveryFragment(t *testing.T) {
	segment := append(testBox("styp", make([]byte, 8)), testSidxBox(1, 12800)...)
	segment = append(segment, testMoofBox(testTrafBox(1, 0, 0))...)
	segment = append(segment, testMoofBox(testTrafBox(1, 0, 75000))...)
	segment = append(segment, testBox("mdat", make([]byte, 16))...)

	if !rewriteFMP4Timeline(segment, 9.0) {
		t.Fatalf("expected the segment to be rewritten")
	}
	// tfdtValueAt reports the track's last fragment; it must keep the 75000-unit
	// spacing above the shifted first fragment (9s * 12800).
	if got := tfdtValueAt(t, segment, 1); got != 9*12800+75000 {
		t.Fatalf("fragment tfdt = %d, want %d", got, 9*12800+75000)
	}
}

// A 32-bit tfdt that would overflow keeps its original value: the segment is
// passed through rather than corrupted.
func TestRewriteFMP4TimelineSkipsOn32BitOverflow(t *testing.T) {
	segment := testSegment(0, 0)
	if rewriteFMP4Timeline(segment, 400000.0) { // 400000*12800 > 2^32
		t.Fatalf("an overflowing rewrite must be skipped")
	}
	if got := tfdtValueAt(t, segment, 1); got != 0 {
		t.Fatalf("video tfdt = %d, want the original 0", got)
	}
}

// A segment without sidx boxes (a plain fMP4 source) has no timescale source
// and must be left alone.
func TestRewriteFMP4TimelineIgnoresSegmentWithoutSidx(t *testing.T) {
	segment := testMoofBox(testTrafBox(1, 0, 0))
	before := append([]byte(nil), segment...)
	if rewriteFMP4Timeline(segment, 9.0) {
		t.Fatalf("a segment without sidx must not be rewritten")
	}
	if !bytes.Equal(before, segment) {
		t.Fatalf("a segment without sidx must stay byte-identical")
	}
}

// The playlist parser maps every media segment to its accumulated position and
// skips the init segment and master-playlist variants.
func TestParseM3USegmentPositions(t *testing.T) {
	manifest := "#EXTM3U\n" +
		"#EXT-X-PLAYLIST-TYPE:VOD\n" +
		"#EXT-X-MAP:URI=\"hls1/main/init.mp4?x=1\"\n" +
		"#EXTINF:3.0000, nodesc\n" +
		"/Videos/item/hls1/main/0.mp4?PlaySessionId=s\n" +
		"#EXTINF:3.0000, nodesc\n" +
		"hls1/main/1.mp4\n" +
		"#EXTINF:1.9300, nodesc\n" +
		"hls1/main/2.mp4\n"
	positions := parseM3USegmentPositions(manifest)
	want := map[string]float64{"/Videos/item/hls1/main/0.mp4": 0, "hls1/main/1.mp4": 3, "hls1/main/2.mp4": 6}
	if len(positions) != len(want) {
		t.Fatalf("positions = %v, want %v", positions, want)
	}
	for name, position := range want {
		if got, ok := positions[name]; !ok || got != position {
			t.Fatalf("position[%q] = %v (%v), want %v", name, got, ok, position)
		}
	}
}

// A master playlist has no media segments and must produce nothing.
func TestParseM3USegmentPositionsIgnoresMasterPlaylist(t *testing.T) {
	manifest := "#EXTM3U\n#EXT-X-STREAM-INF:BANDWIDTH=1\n/Videos/item/main.m3u8?x=1\n"
	if positions := parseM3USegmentPositions(manifest); len(positions) != 0 {
		t.Fatalf("master playlist produced positions: %v", positions)
	}
}

// The session cache round-trips positions from a stored playlist to a segment
// path, and an unknown session or segment reports nothing.
func TestHLSSegmentPositionCache(t *testing.T) {
	manifest := "#EXTM3U\n#EXTINF:3.0000, nodesc\nhls1/main/0.mp4\n#EXTINF:3.0000, nodesc\nhls1/main/1.mp4\n#EXT-X-ENDLIST\n"
	storeHLSSegmentPositions("session-cache-test", "main.m3u8", manifest)

	if position, ok := hlsSegmentStartPosition("session-cache-test", "hls1/main/1.mp4?PlaySessionId=x"); !ok || position != 3 {
		t.Fatalf("position = %v (%v), want 3", position, ok)
	}
	if _, ok := hlsSegmentStartPosition("session-cache-test", "hls1/main/init.mp4"); ok {
		t.Fatalf("the init segment is not a media segment")
	}
	if _, ok := hlsSegmentStartPosition("other-session", "hls1/main/1.mp4"); ok {
		t.Fatalf("an unknown session must report no position")
	}
	if _, ok := hlsSegmentStartPosition("", "hls1/main/1.mp4"); ok {
		t.Fatalf("a missing session id must report no position")
	}
}
