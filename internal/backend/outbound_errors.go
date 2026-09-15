package backend

import (
	"errors"
	"fmt"
	"net/http"
)

// outboundPreparationError is returned while preparing a request that never left
// the process. It carries a category and the offending field name only: the
// message must never repeat credentials, a body, or a full URL.
type outboundPreparationError struct {
	Status  int
	Kind    string
	Field   string
	Message string
}

func (e *outboundPreparationError) Error() string {
	if e.Field != "" {
		return fmt.Sprintf("outbound request not prepared: %s (%s)", e.Kind, e.Field)
	}
	return fmt.Sprintf("outbound request not prepared: %s", e.Kind)
}

func newClientInputPreparationError(kind, field string) *outboundPreparationError {
	return &outboundPreparationError{
		Status:  http.StatusBadRequest,
		Kind:    kind,
		Field:   field,
		Message: "request could not be prepared for the upstream",
	}
}

// newMissingAuthStateError reports that a normal request needs the current user's
// upstream identity and this upstream has none yet. It is not the client's fault,
// so it maps to 503 and never sends a mixed or stale identity.
func newMissingAuthStateError(field string) *outboundPreparationError {
	return &outboundPreparationError{
		Status:  http.StatusServiceUnavailable,
		Kind:    "missing-upstream-auth-state",
		Field:   field,
		Message: "upstream authentication is not ready",
	}
}

// asPreparationError extracts a preparation error from an error chain.
func asPreparationError(err error) (*outboundPreparationError, bool) {
	var prep *outboundPreparationError
	if errors.As(err, &prep) {
		return prep, true
	}
	return nil, false
}

// preparationErrorStatus maps a preparation error to the HTTP status to report.
// Errors that are not preparation errors are not mapped here: callers keep their
// existing network/upstream handling for those.
func preparationErrorStatus(err error) (int, bool) {
	prep, ok := asPreparationError(err)
	if !ok {
		return 0, false
	}
	return prep.Status, true
}

// writePreparationError reports a preparation error with its own status, and
// returns false for any other error so the caller keeps its existing handling.
// The response body names the category and field, never the value.
func writePreparationError(w http.ResponseWriter, err error) bool {
	prep, ok := asPreparationError(err)
	if !ok {
		return false
	}
	writeJSON(w, prep.Status, preparationErrorBody(err))
	return true
}

// preparationErrorBody is the client-visible body for a preparation error: the
// category and the field name, never the value, the body or the URL.
func preparationErrorBody(err error) map[string]any {
	prep, ok := asPreparationError(err)
	if !ok {
		return map[string]any{"message": "request could not be prepared"}
	}
	return map[string]any{
		"message": prep.Message,
		"kind":    prep.Kind,
		"field":   prep.Field,
	}
}

// errUpstreamRedirectNotFollowed is returned in place of following an upstream
// redirect when the administrator turned redirect following off for that
// upstream. It carries no URL of its own: the request URL this proxy builds can
// contain the upstream token, and net/http wraps this error in a *url.Error that
// already includes the original request URL.
var errUpstreamRedirectNotFollowed = errors.New("upstream redirect not followed (followRedirects is off)")

// redactedError replaces an error's message with a version whose embedded URLs
// have their credentials removed, while keeping the original error reachable
// through Unwrap so errors.Is and errors.As still work.
//
// It exists because a transport error from net/http is built from the request
// URL, and a stream request authenticates through its query string, so that URL
// holds the upstream token; handlers surface upstream errors to the client. (A
// refused redirect is not this case: net/http builds that one from the redirect
// target, which is the upstream's own Location value.)
type redactedError struct {
	err error
}

func (e *redactedError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return redactURLInError(e.err)
}

func (e *redactedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}
