package backend

import "sort"

// Identifier sources. Every source is queried inside its own store under that
// store's lock; the lookup never copies a store's contents.
type virtualIDSource interface {
	ContainsVirtualID(value string) bool
}

type issuedTokenSource interface {
	HasIssuedToken(value string) bool
}

type localUserSource interface {
	ContainsUserID(value string) bool
}

// IdentifierSources is the read-only view of the signing/registration stores that
// the identity predicates share. A nil source means "not available in this
// process" (for example an IDStore that failed to open), which is reported as a
// coverage gap rather than as "confirmed not local".
type IdentifierSources struct {
	VirtualIDs virtualIDSource
	Tokens     issuedTokenSource
	Users      localUserSource
	// UpstreamUserIDs maps a server index to that upstream's real user ID. It is a
	// diagnostic hint: only the target snapshot decides the outbound identity.
	UpstreamUserIDs map[int]string
}

// IdentifierFacts is a classified view of one value against the signing and
// registration sources.
type IdentifierFacts struct {
	LocalUser       bool
	LocalToken      bool
	VirtualResource bool
	// UpstreamServers lists the upstreams whose real user ID equals the value,
	// sorted ascending.
	UpstreamServers []int
	// CoverageIncomplete is true when a source could not be consulted, so the
	// negative fields above are not proof that the value is unknown.
	CoverageIncomplete bool
}

// IdentifierLookup answers membership questions for both identity predicates.
// It holds a shared view of the source stores so neither predicate needs its own
// scan of IDStore, AuthManager or UserStore.
type IdentifierLookup struct {
	sources IdentifierSources
}

func newIdentifierLookup(sources IdentifierSources) *IdentifierLookup {
	return &IdentifierLookup{sources: sources}
}

// Match reports the structured facts behind a value.
func (l *IdentifierLookup) Match(value string) IdentifierFacts {
	facts := IdentifierFacts{}
	if value == "" {
		return facts
	}
	if l.sources.VirtualIDs == nil || l.sources.Tokens == nil || l.sources.Users == nil {
		facts.CoverageIncomplete = true
	}
	if l.sources.VirtualIDs != nil {
		facts.VirtualResource = l.sources.VirtualIDs.ContainsVirtualID(value)
	}
	if l.sources.Tokens != nil {
		facts.LocalToken = l.sources.Tokens.HasIssuedToken(value)
	}
	if l.sources.Users != nil {
		facts.LocalUser = l.sources.Users.ContainsUserID(value)
	}
	for serverIndex, userID := range l.sources.UpstreamUserIDs {
		if userID != "" && userID == value {
			facts.UpstreamServers = append(facts.UpstreamServers, serverIndex)
		}
	}
	sort.Ints(facts.UpstreamServers)
	return facts
}

// IdentifierClass is the single classification both predicates report. The
// classes are mutually exclusive, in this priority order:
//
//	local-user       a proxy user registered in UserStore
//	local-token      a proxy token issued by AuthManager
//	virtual-resource a virtual ID issued by IDStore
//	target-upstream  the target upstream's own real user ID (a legal outbound value)
//	foreign-upstream only another upstream's real user ID
//	unknown          none of the above
type IdentifierClass string

const (
	IdentifierLocalUser       IdentifierClass = "local-user"
	IdentifierLocalToken      IdentifierClass = "local-token"
	IdentifierVirtualResource IdentifierClass = "virtual-resource"
	IdentifierTargetUpstream  IdentifierClass = "target-upstream"
	IdentifierForeignUpstream IdentifierClass = "foreign-upstream"
	IdentifierUnknown         IdentifierClass = "unknown"
)

// ClassifyLocalIdentifier names what a value is with respect to the local stores
// and the current target upstream. targetServerIndex is the upstream this request
// is being prepared for; targetUserID is that upstream's real user ID from the
// auth snapshot taken for this request.
//
// A value that matches the target is always reported as target-upstream even when
// another upstream issues the same string: the target wins over string equality.
func (l *IdentifierLookup) Classify(value string, targetServerIndex int, targetUserID string) IdentifierClass {
	facts := l.Match(value)
	if targetUserID != "" && value == targetUserID {
		return IdentifierTargetUpstream
	}
	if facts.LocalUser {
		return IdentifierLocalUser
	}
	if facts.LocalToken {
		return IdentifierLocalToken
	}
	if facts.VirtualResource {
		return IdentifierVirtualResource
	}
	for _, serverIndex := range facts.UpstreamServers {
		if serverIndex != targetServerIndex {
			return IdentifierForeignUpstream
		}
	}
	return IdentifierUnknown
}

// IsTargetUpstreamValue reports whether value is the target upstream's own real
// user ID. The target snapshot is authoritative; other upstreams' snapshots are
// only diagnostic and never decide this.
func (l *IdentifierLookup) IsTargetUpstreamValue(value string, targetUserID string) bool {
	return value != "" && value == targetUserID
}

// upstreamUserIDs collects the real user ID of every configured upstream for the
// diagnostic half of the lookup. It is read once per request context so a
// request never walks the pool per predicate call.
func (a *App) upstreamUserIDsByServer() map[int]string {
	if a.Upstream == nil {
		return nil
	}
	clients := a.Upstream.Clients()
	if len(clients) == 0 {
		return nil
	}
	out := make(map[int]string, len(clients))
	for i := range clients {
		serverIndex := clients[i].serverIndexValue()
		userID := clients[i].clientUserID()
		if userID != "" {
			out[serverIndex] = userID
		}
	}
	return out
}

// newRequestIdentifierLookup builds the shared read-only view for one request.
// It never writes the current target into the shared RequestContext: several
// upstreams are prepared concurrently from one request context.
func (a *App) newRequestIdentifierLookup() *IdentifierLookup {
	return newIdentifierLookup(IdentifierSources{
		VirtualIDs:      a.IDStore,
		Tokens:          a.Auth,
		Users:           a.UserStore,
		UpstreamUserIDs: a.upstreamUserIDsByServer(),
	})
}

// IsCurrentUserAlias reports whether value names the current user for this
// request. Only the request's own trusted ProxyUser.UserID, the legacy global
// proxy user ID used in older responses, and the target upstream's real user ID
// qualify. A value that merely happens to be registered locally does not.
func IsCurrentUserAlias(value string, reqCtx *RequestContext, targetAuth upstreamAuthSnapshot, lookup *IdentifierLookup) bool {
	if value == "" {
		return false
	}
	if targetAuth.UserID != "" && value == targetAuth.UserID {
		return true
	}
	if reqCtx != nil {
		if reqCtx.ProxyUser != nil && reqCtx.ProxyUser.UserID != "" && value == reqCtx.ProxyUser.UserID {
			return true
		}
		if reqCtx.LegacyProxyUserID != "" && value == reqCtx.LegacyProxyUserID {
			return true
		}
	}
	return false
}

// ClassifyLocalIdentifier classifies value against the shared lookup view. It is
// the reporting counterpart of IsCurrentUserAlias and shares the same source
// queries, but its allowed set is wider: every class is a legal answer.
func ClassifyLocalIdentifier(value string, targetServerIndex int, targetAuth upstreamAuthSnapshot, lookup *IdentifierLookup) IdentifierClass {
	if lookup == nil {
		return IdentifierUnknown
	}
	if targetAuth.UserID != "" && value == targetAuth.UserID {
		return IdentifierTargetUpstream
	}
	return lookup.Classify(value, targetServerIndex, targetAuth.UserID)
}
