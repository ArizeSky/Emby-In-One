package backend

import (
	"encoding/binary"
	"math"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Some Emby HLS transcode jobs restart at zero when seeking to an ungenerated
// segment. Sequentially generated segments retain their absolute timestamps.
// A demuxer that builds its timeline from tfdt therefore lands on a zero-based
// timeline after a seek, and the
// seek either never completes or completes at the wrong position.
//
// The proxy is the only place that sees both the playlist and every segment,
// so it restores the timeline: each media segment's tfdt values are shifted so
// the segment starts at the position its playlist assigns it. Tracks that
// already agree with the playlist (a continuous, spec-compliant source) are
// left byte-identical, and any structure the parser does not recognise passes
// through untouched. The rewrite is strictly in place — no box size changes —
// so response lengths and range framing stay valid.

// hlsSegmentPositions is the per-session cache of media segment start
// positions (seconds) parsed from the playlist served for that session.
type hlsSegmentPositions struct {
	stored    time.Time
	sequence  uint64
	positions map[string]float64
}

var hlsPositionCache = struct {
	sync.Mutex
	entries  map[string]*hlsSegmentPositions
	sequence uint64
}{
	entries: map[string]*hlsSegmentPositions{},
}

const (
	hlsPositionCacheLimit = 256
	hlsPositionCacheTTL   = 30 * time.Minute
)

// storeHLSSegmentPositions parses a media playlist and remembers where each
// of its segments starts. Master playlists and playlists without media
// segments produce nothing and are ignored; a missing session id disables
// timeline restoration for that playlist.
func storeHLSSegmentPositions(playSessionID, playlistRest, manifest string) {
	// A sliding live playlist does not start at movie time zero. Discontinuity
	// and byte-range playlists need additional timeline/range state.
	if !strings.Contains(manifest, "#EXT-X-ENDLIST") || strings.Contains(manifest, "#EXT-X-DISCONTINUITY") || strings.Contains(manifest, "#EXT-X-BYTERANGE") {
		return
	}
	positions := parseM3USegmentPositions(manifest)
	if playSessionID == "" || len(positions) == 0 {
		return
	}
	hlsPositionCache.Lock()
	defer hlsPositionCache.Unlock()
	entry := hlsPositionCache.entries[playSessionID]
	if entry == nil {
		entry = &hlsSegmentPositions{positions: map[string]float64{}}
		hlsPositionCache.entries[playSessionID] = entry
	}
	entry.stored = time.Now()
	hlsPositionCache.sequence++
	entry.sequence = hlsPositionCache.sequence
	for segment, position := range positions {
		entry.positions[hlsSegmentPath(segment, playlistRest)] = position
	}
	for key, entry := range hlsPositionCache.entries {
		if time.Since(entry.stored) > hlsPositionCacheTTL {
			delete(hlsPositionCache.entries, key)
		}
	}
	for len(hlsPositionCache.entries) > hlsPositionCacheLimit {
		var oldest string
		var oldestSequence uint64
		for key, entry := range hlsPositionCache.entries {
			if oldest == "" || entry.sequence < oldestSequence {
				oldest, oldestSequence = key, entry.sequence
			}
		}
		delete(hlsPositionCache.entries, oldest)
	}
}

// hlsSegmentStartPosition reports the playlist position of the media segment
// named by a stream path such as "hls1/main/600.mp4".
func hlsSegmentStartPosition(playSessionID, rest string) (float64, bool) {
	name := hlsSegmentPath(rest, "")
	if playSessionID == "" || name == "" {
		return 0, false
	}
	hlsPositionCache.Lock()
	defer hlsPositionCache.Unlock()
	entry, ok := hlsPositionCache.entries[playSessionID]
	if !ok || time.Since(entry.stored) > hlsPositionCacheTTL {
		delete(hlsPositionCache.entries, playSessionID)
		return 0, false
	}
	position, ok := entry.positions[name]
	if ok {
		entry.stored = time.Now() // idle TTL: an actively playing movie stays valid
		hlsPositionCache.sequence++
		entry.sequence = hlsPositionCache.sequence
	}
	return position, ok
}

// Keep rendition directories: audio/0.mp4 and video/0.mp4 are different segments.
func hlsSegmentPath(raw, playlistRest string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Path == "" {
		return ""
	}
	p := u.Path
	if strings.HasPrefix(p, "/") {
		parts := strings.Split(strings.TrimPrefix(p, "/"), "/")
		if len(parts) >= 3 && (strings.EqualFold(parts[0], "Videos") || strings.EqualFold(parts[0], "Audio")) {
			p = strings.Join(parts[2:], "/")
		}
	} else if playlistRest != "" {
		p = path.Join(path.Dir(playlistRest), p)
	}
	return strings.TrimPrefix(path.Clean(p), "/")
}

// parseM3USegmentPositions accumulates #EXTINF durations and maps each media
// segment path to its playlist start position in seconds. Init segments
// (#EXT-X-MAP) and master-playlist variants are not media segments: no
// #EXTINF precedes them, so they are skipped naturally.
func parseM3USegmentPositions(manifest string) map[string]float64 {
	positions := map[string]float64{}
	var position, pending float64
	var havePending bool
	for _, line := range strings.Split(manifest, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			continue
		case strings.HasPrefix(trimmed, "#EXTINF:"):
			value := strings.TrimPrefix(trimmed, "#EXTINF:")
			if idx := strings.IndexByte(value, ','); idx >= 0 {
				value = value[:idx]
			}
			if duration, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil && duration > 0 && !math.IsNaN(duration) && !math.IsInf(duration, 0) {
				pending, havePending = duration, true
			} else {
				return map[string]float64{} // cannot infer later positions safely
			}
		case strings.HasPrefix(trimmed, "#"):
			// Other tags and comments do not advance the timeline.
		case havePending:
			if u, err := url.Parse(trimmed); err == nil && u.Path != "" {
				positions[u.Path] = position
				position += pending
				if math.IsInf(position, 0) {
					return map[string]float64{}
				}
			}
			havePending = false
		}
	}
	return positions
}

// forEachBox walks the ISO-BMFF boxes in body[start:end] and calls fn with
// each box's type and payload range. It stops early when fn returns false and
// ignores trailing bytes that are not a complete well-formed box.
func forEachBox(body []byte, start, end int, fn func(boxType string, payloadStart, payloadEnd int) bool) {
	offset := start
	for offset+8 <= end {
		size := int(binary.BigEndian.Uint32(body[offset:]))
		header := 8
		if size == 1 {
			if offset+16 > end {
				return
			}
			size64 := binary.BigEndian.Uint64(body[offset+8:])
			if size64 > uint64(end-offset) {
				return
			}
			size = int(size64)
			header = 16
		} else if size == 0 {
			size = end - offset
		}
		if size < header || offset+size > end {
			return
		}
		if !fn(string(body[offset+4:offset+8]), offset+header, offset+size) {
			return
		}
		offset += size
	}
}

// rewriteFMP4Timeline shifts every track's tfdt (baseMediaDecodeTime) values
// so the segment's media starts at positionSecs. Track timescales are read
// from the segment's own sidx boxes (reference_ID matches the tfhd track_ID).
// The first track establishes one common forward shift for all tracks and
// their sidx presentation times, preserving their relative A/V offset. Sources
// that already match or have a positive origin pass through byte-identical.
// Returns true when any value was rewritten.
func rewriteFMP4Timeline(body []byte, positionSecs float64) bool {
	if positionSecs < 0 || math.IsNaN(positionSecs) || math.IsInf(positionSecs, 0) {
		return false
	}
	timescales, fields, order := collectFMP4Timeline(body)
	// Apply one common time shift to both tracks, preserving their A/V offset.
	// A source with a positive timestamp origin must not be forced back to zero.
	shiftSecs := 0.0
	for _, trackID := range order {
		if timescale := timescales[trackID]; timescale > 0 {
			shiftSecs = positionSecs - float64(fields[trackID][0].value)/float64(timescale)
			break
		}
	}
	if shiftSecs < 1 {
		return false
	}
	type pendingWrite struct {
		field fmp4TimeField
		value uint64
	}
	var writes []pendingWrite
	for _, trackID := range order {
		timescale, ok := timescales[trackID]
		if !ok {
			return false // cannot shift just one track safely
		}
		trackFields := fields[trackID]
		units := math.Round(shiftSecs * float64(timescale))
		if units >= math.Exp2(64) {
			return false
		}
		shift := uint64(units)
		// Validate the entire segment before writing, including index timestamps.
		for _, field := range trackFields {
			if shift > math.MaxUint64-field.value || (field.version == 0 && field.value+shift > math.MaxUint32) {
				return false
			}
			writes = append(writes, pendingWrite{field, field.value + shift})
		}
		valid := true
		forEachBox(body, 0, len(body), func(kind string, start, end int) bool {
			if kind != "sidx" || end-start < 20 || binary.BigEndian.Uint32(body[start+4:]) != trackID {
				return true
			}
			version := body[start]
			field := fmp4TimeField{offset: start + 12, version: version}
			if version == 0 {
				field.value = uint64(binary.BigEndian.Uint32(body[field.offset:]))
			} else if version == 1 && end-start >= 28 {
				field.value = binary.BigEndian.Uint64(body[field.offset:])
			} else {
				valid = false
				return false
			}
			if shift > math.MaxUint64-field.value || (version == 0 && field.value+shift > math.MaxUint32) {
				valid = false
				return false
			}
			writes = append(writes, pendingWrite{field, field.value + shift})
			return true
		})
		if !valid {
			return false
		}
	}
	for _, write := range writes {
		if write.field.version == 0 {
			binary.BigEndian.PutUint32(body[write.field.offset:], uint32(write.value))
		} else {
			binary.BigEndian.PutUint64(body[write.field.offset:], write.value)
		}
	}
	return len(writes) > 0
}

type fmp4TimeField struct {
	offset  int // byte offset of baseMediaDecodeTime within body
	version byte
	value   uint64
}

func collectFMP4Timeline(body []byte) (map[uint32]uint32, map[uint32][]fmp4TimeField, []uint32) {
	timescales := map[uint32]uint32{}
	forEachBox(body, 0, len(body), func(boxType string, payloadStart, payloadEnd int) bool {
		if boxType == "sidx" && payloadEnd-payloadStart >= 12 {
			referenceID := binary.BigEndian.Uint32(body[payloadStart+4:])
			timescale := binary.BigEndian.Uint32(body[payloadStart+8:])
			if timescale > 0 {
				timescales[referenceID] = timescale
			}
		}
		return true
	})
	if len(timescales) == 0 {
		return timescales, nil, nil
	}

	fields := map[uint32][]fmp4TimeField{}
	var order []uint32
	forEachBox(body, 0, len(body), func(boxType string, payloadStart, payloadEnd int) bool {
		if boxType != "moof" {
			return true
		}
		forEachBox(body, payloadStart, payloadEnd, func(child string, childStart, childEnd int) bool {
			if child != "traf" {
				return true
			}
			var trackID uint32
			haveTrack := false
			forEachBox(body, childStart, childEnd, func(sub string, subStart, subEnd int) bool {
				if sub == "tfhd" && subEnd-subStart >= 8 {
					trackID = binary.BigEndian.Uint32(body[subStart+4:])
					haveTrack = true
				}
				return true
			})
			if !haveTrack {
				return true
			}
			forEachBox(body, childStart, childEnd, func(sub string, subStart, subEnd int) bool {
				if sub != "tfdt" || subEnd-subStart < 4 {
					return true
				}
				version := body[subStart]
				var value uint64
				switch version {
				case 0:
					if subEnd-subStart < 8 {
						return true
					}
					value = uint64(binary.BigEndian.Uint32(body[subStart+4:]))
				case 1:
					if subEnd-subStart < 12 {
						return true
					}
					value = binary.BigEndian.Uint64(body[subStart+4:])
				default:
					return true
				}
				if _, seen := fields[trackID]; !seen {
					order = append(order, trackID)
				}
				fields[trackID] = append(fields[trackID], fmp4TimeField{offset: subStart + 4, version: version, value: value})
				return true
			})
			return true
		})
		return true
	})

	return timescales, fields, order
}
