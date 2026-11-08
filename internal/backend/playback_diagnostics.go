package backend

import "net/http"

func playbackTraceID(r *http.Request) string {
	if trace, _ := r.Context().Value(outboundTraceKey{}).(*outboundTrace); trace != nil {
		return trace.id
	}
	return "none"
}

// One start, media or negotiation line records the concrete copy and track
// indices. No credentials, signed URLs, raw payloads or device headers appear.
func (a *App) logPlaybackSelection(r *http.Request, event, item, server, original, source string, body map[string]any) {
	if a.Logger == nil {
		return
	}
	audio, subtitles := -1, -1
	if value, ok := numericInt(body["AudioStreamIndex"]); ok {
		audio = value
	}
	if value, ok := numericInt(body["SubtitleStreamIndex"]); ok {
		subtitles = value
	}
	a.Logger.Infof("playback_selection trace=%s event=%s item=%s server=%s original=%s source=%s audio_index=%d subtitle_index=%d", playbackTraceID(r), event, item, server, original, source, audio, subtitles)
}
