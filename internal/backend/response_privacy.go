// response_privacy.go is the single place the scattered user-state privacy
// decisions get made: which responses must strip the shared upstream account's
// watch state, and how the local per-user view replaces it. The UserData
// rebuild itself lives in userdata_overlay.go.
package backend

import (
	"errors"
	"net/http"
	"time"
)

// privacyMode reports what a JSON response written to the requesting audience
// must do with the shared upstream watch state before it is served.
//
// privacyPassThrough — admins and anonymous callers: the shared upstream
// account's own state is the view this audience legitimately sees, unchanged
// from the pre-privacy behaviour.
//
// privacyLocal — non-admin proxy users: the server answers with that single
// user's own record; no field of the shared account's watch state may survive.
// A nil WatchStore (multi-user disabled, admin console) is not a skip for this
// audience: the clean default is still emitted instead of the shared state.
type privacyMode int

const (
	privacyPassThrough privacyMode = iota
	privacyLocal
)

// responsePrivacyMode resolves the privacy mode once per request.
func (a *App) responsePrivacyMode(r *http.Request) privacyMode {
	reqCtx := requestContextFrom(r.Context())
	if reqCtx == nil || reqCtx.ProxyUser == nil || reqCtx.ProxyUser.Role == "admin" {
		return privacyPassThrough
	}
	return privacyLocal
}

// isUserRestricted reports whether the request must be answered from a single
// user's own view rather than the shared upstream account's (any non-admin
// proxy user, with or without a backing local account).
func isUserRestricted(r *http.Request) bool {
	reqCtx := requestContextFrom(r.Context())
	return reqCtx != nil && reqCtx.ProxyUser != nil && reqCtx.ProxyUser.Role != "admin"
}

// localWatchReadError distinguishes "the WatchStore could not be read" from
// "there is no record": a caller that silently continues on a failed read would
// ship the shared upstream state, which is exactly what the privacy layer must
// never do. A caller answers 502 — the same code the proxy already uses for
// upstream-backed responses it cannot verify.
type localWatchReadError struct{ cause error }

func (e *localWatchReadError) Error() string {
	if e.cause == nil {
		return "local user-state store read failed"
	}
	return "local user-state store read failed: " + e.cause.Error()
}

func (e *localWatchReadError) Unwrap() error { return e.cause }

// asLocalWatchReadError extracts a local-read failure from an error chain.
func asLocalWatchReadError(err error) (*localWatchReadError, bool) {
	var read *localWatchReadError
	if errors.As(err, &read) {
		return read, true
	}
	return nil, false
}

// writeLocalWatchReadError answers 502 with a message that names the cause
// without repeating any URL or token.
func (a *App) writeLocalWatchReadError(w http.ResponseWriter, err error) {
	if a.Logger != nil {
		a.Logger.Warnf("local user-state read failed: %s", redactURLInError(err))
	}
	writeJSON(w, http.StatusBadGateway, map[string]any{"message": "Local user state is unavailable"})
}

// restrictedMediaSourceFields are the MediaSource keys a restricted viewer's
// detail/list payloads must not carry: the local file layout of the upstream
// node, plus the direct-stream / transcoding URLs that name it. Playback that
// genuinely needs a source issues its own PlaybackInfo request, which answers
// with locally routed URLs; removing these keys keeps the item's Container,
// Ids and stream list intact without dropping the whole block.
var restrictedMediaSourceFields = []string{"Path", "DirectStreamUrl", "TranscodingUrl"}

// privacyDetailMediaSources strips the restricted MediaSource fields a detail
// response would otherwise leak to a non-admin viewer, handing back the same
// list so callers do not have to restructure their payloads. Admins and
// anonymous callers keep the shape they already had.
func privacyDetailMediaSources(r *http.Request, sources []map[string]any) []map[string]any {
	if !isUserRestricted(r) {
		return sources
	}
	for _, source := range sources {
		if source == nil {
			continue
		}
		for _, key := range restrictedMediaSourceFields {
			delete(source, key)
		}
	}
	return sources
}

// privacyItemMediaSources is the per-item list form: the MediaSources inside
// each item are cleaned the same way privacyDetailMediaSources does. Admins and
// anonymous callers keep the shape they already had.
func privacyItemMediaSources(r *http.Request, items []map[string]any) {
	if !isUserRestricted(r) {
		return
	}
	for _, item := range items {
		if item == nil {
			continue
		}
		sources, ok := item["MediaSources"].([]any)
		if !ok {
			continue
		}
		cleaned := make([]map[string]any, 0, len(sources))
		for _, raw := range sources {
			source, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			for _, key := range restrictedMediaSourceFields {
				delete(source, key)
			}
			cleaned = append(cleaned, source)
		}
		rebuilt := make([]any, 0, len(cleaned))
		for _, source := range cleaned {
			rebuilt = append(rebuilt, source)
		}
		item["MediaSources"] = rebuilt
	}
}

// lastPlayedUTC converts the WatchStore's millisecond stamp into a UTC time.
// A zero stamp stays the null time, which the formatter leaves out — the
// "never played" case has no date to express.
func lastPlayedUTC(millis int64) time.Time {
	if millis <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(millis).UTC()
}

// embyDateTimeLayout is the DateTime form Emby serialises: ISO with a
// fixed-width fraction and a Z offset.
const embyDateTimeLayout = "2006-01-02T15:04:05.0000000Z07:00"
