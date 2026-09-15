package backend

import "strings"

// outboundAuthMode selects which authentication rules a request preparation
// follows. Bootstrap requests legitimately carry no business identity, so they
// must not be blocked by the normal "current user must be known" rule.
type outboundAuthMode string

const (
	authModeNormal           outboundAuthMode = "normal"
	authModePasswordLogin    outboundAuthMode = "passwordLogin"
	authModeAPIKeyValidation                  = "apiKeyValidation"
)

// identityFieldAction is the action the preparation layer takes on one identity
// field of a request.
//
// There is deliberately no "delete" action for unclassified requests: this batch
// never removes a UserId field it does not understand, because removing identity
// from an unknown endpoint changes behavior the proxy cannot validate.
type identityFieldAction string

const (
	// actionNormalizeToCurrent replaces the value with the target upstream's real
	// user ID from this request's auth snapshot.
	actionNormalizeToCurrent identityFieldAction = "normalize-to-current"
	// actionOmitOnBootstrap keeps the field absent for bootstrap requests, which
	// authenticate without a business identity.
	actionOmitOnBootstrap identityFieldAction = "omit-on-bootstrap"
	// actionPassthrough keeps the value as the client sent it.
	actionPassthrough identityFieldAction = "passthrough"
)

// userIDQuerySignal describes how a query UserId value was matched.
type userIDQuerySignal string

const (
	// signalAbsent means no UserId variant was present; nothing is added.
	signalAbsent userIDQuerySignal = "absent"
	// signalCurrentUser means every present value named the current user by one of
	// the trusted aliases.
	signalCurrentUser userIDQuerySignal = "current-user"
	// signalUnknown means at least one present value is not a trusted alias.
	signalUnknown userIDQuerySignal = "unknown"
)

// Path classes reported by the policy and used in the log summary.
const (
	pathClassStatic       = "static"
	pathClassCurrentUser  = "current-user"
	pathClassBootstrap    = "bootstrap"
	pathClassSelfAlias    = "self-alias-compat"
	pathClassUnclassified = "unclassified"
)

// outboundIdentityPolicy is the decision table for one outbound request: which
// fields are normalized, which are left alone, and which authentication rules
// apply. Path, query and body rules are independent.
type outboundIdentityPolicy struct {
	pathClass   string
	queryUserID identityFieldAction
	bodyUserID  identityFieldAction
	authMode    outboundAuthMode
	pathAction  identityFieldAction
	stripAPIKey bool
	// supported marks an endpoint the action table declares as a current-user
	// endpoint. An unsupported request keeps the fallback rules.
	supported bool
	// fallbackRead allows the narrow self-alias compatibility exception for an
	// unclassified GET/HEAD read: a query UserId whose every value is a trusted
	// alias of this request is normalized to the target upstream identity. It is
	// never a body rule, and a value that is not a trusted alias is left alone.
	fallbackRead bool
	// baseURL is the upstream base for this request; its path prefix is never
	// inspected as business path.
	baseURL string
}

// staticUserRoutes are the /Users/* routes whose literal segment is part of the
// route, not a user ID. They are never treated as a /Users/{id} segment.
var staticUserRoutes = map[string]bool{
	"authenticatebyname": true,
	"me":                 true,
	"public":             true,
	"new":                true,
	"configuration":      true,
	"policy":             true,
	"groupingoptions":    true,
}

// isStaticUserRoute reports whether the path's user-position segment is a fixed
// route word.
func isStaticUserRoute(segment string) bool {
	return staticUserRoutes[strings.ToLower(segment)]
}

// userIDPathSegment returns the {id} of a leading /Users/{id} segment of a
// business path.
func userIDPathSegment(businessPath string) (string, bool) {
	const prefix = "/Users/"
	if len(businessPath) < len(prefix) || !strings.EqualFold(businessPath[:len(prefix)], prefix) {
		return "", false
	}
	rest := businessPath[len(prefix):]
	if rest == "" {
		return "", false
	}
	if slash := strings.IndexByte(rest, '/'); slash >= 0 {
		rest = rest[:slash]
	}
	if rest == "" {
		return "", false
	}
	return rest, true
}

// currentUserQueryPaths are the endpoints whose declared UserId query always
// names the current user.
var currentUserQueryPaths = []string{
	"/Items",
	"/Shows/NextUp",
	"/Genres",
	"/MusicGenres",
	"/Studios",
	"/Persons",
	"/Artists",
	"/Search/Hints",
	"/Shows",
	"/Library/MediaFolders",
	"/Library/VirtualFolders",
	"/Library/SelectableMediaFolders",
	"/Library/SelectableRemoteLibraries",
	"/Items/Filters",
	"/Items/Prefixes",
}

// currentUserWritablePaths are the write endpoints whose top-level JSON UserId
// names the current user.
var currentUserWritablePaths = []string{
	"/Sessions/Playing",
	"/Sessions/Playing/Progress",
	"/Sessions/Playing/Stopped",
	"/Sessions/Capabilities",
	"/Sessions/Capabilities/Full",
}

// currentUserPathSuffixes are path shapes under /Users/{id} that carry a
// current-user UserId query.
var currentUserPathSuffixes = []string{
	"/Items",
	"/Items/Resume",
	"/Items/Latest",
	"/Views",
	"/PlayingItems",
	"/UserData",
	"/FavoriteItems",
}

func pathMatchesAny(path string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func pathHasAnySuffix(path string, suffixes []string) bool {
	for _, suffix := range suffixes {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	return false
}

// resolveOutboundPolicy decides the policy for one outbound request.
//
// businessPath is the request path without the upstream base URL's own prefix.
// method and mode select the bootstrap rules; baseURL is used only to keep the
// deployment prefix out of the path rules.
func resolveOutboundPolicy(businessPath string, method string, mode outboundAuthMode, baseURL string) outboundIdentityPolicy {
	if mode == authModePasswordLogin || mode == authModeAPIKeyValidation {
		return outboundIdentityPolicy{
			pathClass:   pathClassBootstrap,
			queryUserID: actionOmitOnBootstrap,
			bodyUserID:  actionOmitOnBootstrap,
			authMode:    mode,
			pathAction:  actionOmitOnBootstrap,
			baseURL:     baseURL,
		}
	}

	policy := outboundIdentityPolicy{
		pathClass:   pathClassUnclassified,
		queryUserID: actionPassthrough,
		bodyUserID:  actionPassthrough,
		authMode:    authModeNormal,
		pathAction:  actionPassthrough,
		stripAPIKey: true,
		baseURL:     baseURL,
	}

	// The path and query rules are independent. A supported endpoint normalizes
	// its query UserId; only an endpoint the action table also declares under
	// /Users/{id} normalizes the path segment. An unclassified path that still has
	// a /Users/{id} segment is not declared: its segment is reported as
	// unclassified and left as the client sent it.
	if segment, ok := userIDPathSegment(businessPath); ok {
		if isStaticUserRoute(segment) {
			policy.pathClass = pathClassStatic
		} else {
			policy.pathClass = pathClassCurrentUser
		}
	}

	if pathMatchesAny(businessPath, currentUserQueryPaths) ||
		pathHasAnySuffix(businessPath, currentUserPathSuffixes) {
		policy.supported = true
		policy.queryUserID = actionNormalizeToCurrent
		policy.pathAction = actionNormalizeToCurrent
	}
	if pathMatchesAny(businessPath, currentUserWritablePaths) {
		policy.supported = true
		policy.queryUserID = actionNormalizeToCurrent
		policy.bodyUserID = actionNormalizeToCurrent
	}
	if strings.HasSuffix(businessPath, "/PlaybackInfo") {
		policy.supported = true
		policy.queryUserID = actionNormalizeToCurrent
		policy.bodyUserID = actionNormalizeToCurrent
		policy.pathAction = actionPassthrough
	}

	// An unclassified GET/HEAD read may still be answered for the current user when
	// every UserId it carries is a trusted alias of this request. Writes and
	// unknown authorization targets are never rewritten this way.
	if !policy.supported {
		switch strings.ToUpper(method) {
		case "GET", "HEAD":
			policy.fallbackRead = true
		}
	}
	if policy.pathClass == pathClassUnclassified && policy.pathAction == actionPassthrough {
		policy.pathClass = pathClassSelfAlias
	}
	return policy
}

// policyLookup returns the request's shared identifier view, or an empty one when
// no request context is available.
func policyLookup(reqCtx *RequestContext) *IdentifierLookup {
	if reqCtx != nil && reqCtx.Identifiers != nil {
		return reqCtx.Identifiers
	}
	return newIdentifierLookup(IdentifierSources{})
}
