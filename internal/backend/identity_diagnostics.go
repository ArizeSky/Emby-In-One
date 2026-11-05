package backend

import "strings"

// Only fixed source categories belong in diagnostics; resident sources also
// contain a configured profile identifier, which must not escape into logs.
func diagnosticIdentitySource(source string) string {
	if strings.HasPrefix(source, "resident:") {
		return "resident"
	}
	switch source {
	case "resident-missing", "override", "live-request", "captured-token", "last-success", "captured-latest", "no-identity", "custom":
		return source
	default:
		return "unknown"
	}
}
