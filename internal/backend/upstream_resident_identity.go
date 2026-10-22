package backend

import (
	"net/http"
	"strconv"
)

// residentIdentitySnapshot is the fixed identity a resident-mode client presents
// for its whole lifetime. It is resolved once at construction from the UA library
// and never re-read per request, so a later library edit cannot silently change a
// live client's identity mid-session.
type residentIdentitySnapshot struct {
	ProfileID       string
	IdentityVersion uint64
	Headers         http.Header
}

func newResidentIdentitySnapshot(profile ResidentUA) *residentIdentitySnapshot {
	return &residentIdentitySnapshot{
		ProfileID:       profile.ID,
		IdentityVersion: profile.IdentityVersion,
		Headers:         uaIdentityHeaders(profile.Identity),
	}
}

// residentSnapshotFromLibrary resolves the fixed identity for a resident-mode
// upstream from the process-wide library. It returns nil when the library is
// unavailable or the referenced profile is missing.
func residentSnapshotFromLibrary(profileID string) *residentIdentitySnapshot {
	if profileID == "" {
		return nil
	}
	library := activeUALibrary()
	if library == nil || !library.Available() {
		return nil
	}
	profile, ok := library.ResidentByID(profileID)
	if !ok {
		return nil
	}
	return newResidentIdentitySnapshot(profile)
}

// applyResidentIdentity forces the seven managed identity fields back onto an
// outbound header set, so Stream extra headers or a retry override cannot replace
// a resident identity. It only runs for resident-mode clients with a snapshot.
func (c *UpstreamClient) applyResidentIdentity(headers http.Header) {
	if c == nil || c.Config.SpoofClient != "resident" || c.residentIdentity == nil {
		return
	}
	for _, key := range capturedHeaderKeys {
		if value := c.residentIdentity.Headers.Get(key); value != "" {
			headers.Set(key, value)
		} else {
			headers.Del(key)
		}
	}
}

func (c *UpstreamClient) residentIdentitySource() string {
	if c == nil || c.residentIdentity == nil {
		return "resident-missing"
	}
	return "resident:" + c.residentIdentity.ProfileID + ":" + strconv.FormatUint(c.residentIdentity.IdentityVersion, 10)
}

// newResidentIdentityMissingError reports a resident upstream whose referenced
// identity is unavailable. It is a preparation error, so no request is sent
// without an identity.
func newResidentIdentityMissingError() *outboundPreparationError {
	return &outboundPreparationError{
		Status:  http.StatusServiceUnavailable,
		Kind:    "resident-identity-missing",
		Field:   "uaProfileId",
		Message: "常驻 UA 引用缺失或身份库不可用",
	}
}

// residentIdentityChanged reports whether moving from old to next changes a
// resident identity, in which case the new client must not inherit the old
// session's credentials.
func residentIdentityChanged(old, next *UpstreamClient) bool {
	if next == nil || next.Config.SpoofClient != "resident" {
		return false
	}
	if old == nil || old.residentIdentity == nil || next.residentIdentity == nil {
		return true
	}
	return old.residentIdentity.ProfileID != next.residentIdentity.ProfileID ||
		old.residentIdentity.IdentityVersion != next.residentIdentity.IdentityVersion
}
