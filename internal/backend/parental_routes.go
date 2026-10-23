package backend

import (
	"net/http"
	"regexp"
	"strings"
)

// routeClass classifies a request that reached the generic fallback proxy so a
// parental-restricted user can be handled safely (plan §8.7).
type routeClass int

const (
	routeUnknown routeClass = iota // not recognised: deny for restricted users
	routeControl                   // non-content compatibility/control endpoint: allow
	routeContent                   // known content read/download: deny here (dedicated handlers own it)
)

// parentalControlRoutes lists method+path patterns that are safe non-content
// control endpoints for a restricted user. Every entry from plan §8.7 that must
// not be caught by the default deny is listed explicitly. Matching is on the
// lower-cased path with any /emby prefix removed, so casing and the compatibility
// prefix cannot slip a content route past classification.
var parentalControlRoutes = []struct {
	method  string
	pattern *regexp.Regexp
}{
	{"get", regexp.MustCompile(`^/system/ping$`)},
	{"post", regexp.MustCompile(`^/system/ping$`)},
	{"get", regexp.MustCompile(`^/system/(info|info/public|endpoint)$`)},
	{"get", regexp.MustCompile(`^/branding/`)},
	{"get", regexp.MustCompile(`^/localization/`)},
	{"post", regexp.MustCompile(`^/sessions/playing(/progress|/stopped)?$`)},
	{"post", regexp.MustCompile(`^/sessions/capabilities(/full)?$`)},
	{"post", regexp.MustCompile(`^/sessions/logout$`)},
	{"get", regexp.MustCompile(`^/users/public$`)},
	{"get", regexp.MustCompile(`^/users/[^/]+$`)},
	{"get", regexp.MustCompile(`^/users/[^/]+/configuration$`)},
	{"post", regexp.MustCompile(`^/users/[^/]+/configuration$`)},
	{"get", regexp.MustCompile(`^/users/[^/]+/policy$`)},
	{"post", regexp.MustCompile(`^/users/[^/]+/policy$`)},
	{"get", regexp.MustCompile(`^/users/[^/]+/playingitems/`)},
	{"post", regexp.MustCompile(`^/users/[^/]+/playingitems/`)},
	{"get", regexp.MustCompile(`^/users/[^/]+/userdata`)},
	{"post", regexp.MustCompile(`^/users/[^/]+/userdata`)},
	{"get", regexp.MustCompile(`^/users/[^/]+/favoriteitems/`)},
	{"post", regexp.MustCompile(`^/users/[^/]+/favoriteitems/`)},
	{"get", regexp.MustCompile(`^/displaypreferences/`)},
	{"post", regexp.MustCompile(`^/displaypreferences/`)},
}

// normalizeParentalPath lower-cases a path and drops the /emby compatibility
// prefix so the classification table matches regardless of casing or prefix.
func normalizeParentalPath(path string) string {
	lower := strings.ToLower(path)
	if strings.HasPrefix(lower, "/emby/") {
		lower = strings.TrimPrefix(lower, "/emby")
	}
	return lower
}

// classifyParentalRoute returns the class of a request reaching the fallback
// proxy. Anything not explicitly recognised as control is unknown/content and is
// denied for a restricted user rather than proxied.
func classifyParentalRoute(method, path string) routeClass {
	m := strings.ToLower(method)
	p := normalizeParentalPath(path)
	for _, rule := range parentalControlRoutes {
		if rule.method == m && rule.pattern.MatchString(p) {
			return routeControl
		}
	}
	return routeUnknown
}

// parentalFallbackAllowed reports whether a restricted user may pass through the
// fallback proxy for this request. Only explicit control endpoints are allowed;
// everything else is refused so a raw item id or an unknown binary download can
// never be served to a restricted user by accident.
func parentalFallbackAllowed(method, path string) bool {
	return classifyParentalRoute(method, path) == routeControl
}

// writeParentalRouteNotSupported refuses a fallback request for a restricted
// user with a stable machine-readable reason (plan §8.7).
func writeParentalRouteNotSupported(w http.ResponseWriter) {
	writeJSON(w, http.StatusForbidden, map[string]any{
		"message": "ContentRouteNotSupportedWithParentalControl",
		"code":    "ContentRouteNotSupportedWithParentalControl",
	})
}
