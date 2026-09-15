package backend

import (
	"testing"
)

// lookupFixture is a fully independent set of stores for the identifier lookup.
// Every value the tests assert on is produced here, never by the predicate under
// test, so a predicate that agrees with its own classifier cannot pass.
type lookupFixture struct {
	idStore     *IDStore
	authStub    *stubTokenSource
	userStub    *stubUserSource
	upstreamIDs map[int]string
	lookup      *IdentifierLookup
}

// stubTokenSource is a stand-in for AuthManager so the lookup contract can be
// tested without issuing real tokens.
type stubTokenSource struct {
	issued map[string]string
}

func (s *stubTokenSource) HasIssuedToken(value string) bool {
	_, ok := s.issued[value]
	return ok
}

func (s *stubTokenSource) UserIDForToken(value string) (string, bool) {
	userID, ok := s.issued[value]
	return userID, ok
}

// stubUserSource is a stand-in for UserStore.
type stubUserSource struct {
	users map[string]struct{}
}

func (s *stubUserSource) ContainsUserID(value string) bool {
	_, ok := s.users[value]
	return ok
}

const (
	fixtureAliceID     = "alice-local"
	fixtureBobID       = "bob-local"
	fixtureLegacyID    = "legacy-admin"
	fixtureAliceToken  = "token-local"
	fixtureUpstreamA   = "user-A"
	fixtureUpstreamB   = "user-B"
	fixtureUnknownID   = "stranger-value"
	fixtureUpstreamTok = "token-A"
)

func newLookupFixture(t *testing.T) *lookupFixture {
	t.Helper()
	idStore, err := NewIDStore(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewIDStore: %v", err)
	}
	t.Cleanup(func() { _ = idStore.Close() })

	fixture := &lookupFixture{
		idStore: idStore,
		authStub: &stubTokenSource{issued: map[string]string{
			fixtureAliceToken: fixtureAliceID,
		}},
		userStub: &stubUserSource{users: map[string]struct{}{
			fixtureAliceID: {},
			fixtureBobID:   {},
		}},
		upstreamIDs: map[int]string{
			0: fixtureUpstreamA,
			1: fixtureUpstreamB,
		},
	}
	fixture.lookup = newIdentifierLookup(IdentifierSources{
		VirtualIDs:      idStore,
		Tokens:          fixture.authStub,
		Users:           fixture.userStub,
		UpstreamUserIDs: fixture.upstreamIDs,
	})
	return fixture
}

// fixtureRequestContext builds the trusted alias set for one request: the
// request's own proxy user plus the legacy global ID older responses used.
func fixtureRequestContext(proxyUserID string) *RequestContext {
	return &RequestContext{
		ProxyUser:         &tokenInfo{UserID: proxyUserID, Username: proxyUserID, Role: "user"},
		LegacyProxyUserID: fixtureLegacyID,
	}
}

func TestIdentifierLookupFixtures(t *testing.T) {
	fixture := newLookupFixture(t)
	virtualItem := fixture.idStore.GetOrCreateVirtualID("item-virtual", 0)
	reqCtx := fixtureRequestContext(fixtureAliceID)
	targetAuth := upstreamAuthSnapshot{UserID: fixtureUpstreamA, AccessToken: fixtureUpstreamTok}

	// Bob is a registered local user, but that must not make him Alice's alias.
	if !fixture.lookup.Match(fixtureBobID).LocalUser {
		t.Fatalf("bob-local should be a local user")
	}
	if IsCurrentUserAlias(fixtureBobID, reqCtx, targetAuth, fixture.lookup) {
		t.Fatalf("bob-local must not count as the current user's alias")
	}
	if got := ClassifyLocalIdentifier(fixtureBobID, 0, targetAuth, fixture.lookup); got != IdentifierLocalUser {
		t.Fatalf("classify(bob-local) = %q, want %q", got, IdentifierLocalUser)
	}

	// The request's own user, the legacy global ID and the target upstream's real
	// ID are the only three trusted aliases.
	for _, alias := range []string{fixtureAliceID, fixtureLegacyID, fixtureUpstreamA} {
		if !IsCurrentUserAlias(alias, reqCtx, targetAuth, fixture.lookup) {
			t.Fatalf("%q should be a trusted current-user alias", alias)
		}
	}
	for _, other := range []string{fixtureUpstreamB, fixtureAliceToken, virtualItem, fixtureUnknownID, ""} {
		if IsCurrentUserAlias(other, reqCtx, targetAuth, fixture.lookup) {
			t.Fatalf("%q must not be a trusted current-user alias", other)
		}
	}

	tokenFacts := fixture.lookup.Match(fixtureAliceToken)
	if !tokenFacts.LocalToken {
		t.Fatalf("token-local should be classified as an issued token")
	}
	if got := ClassifyLocalIdentifier(fixtureAliceToken, 0, targetAuth, fixture.lookup); got != IdentifierLocalToken {
		t.Fatalf("classify(token-local) = %q, want %q", got, IdentifierLocalToken)
	}

	if got := ClassifyLocalIdentifier(virtualItem, 0, targetAuth, fixture.lookup); got != IdentifierVirtualResource {
		t.Fatalf("classify(virtual item) = %q, want %q", got, IdentifierVirtualResource)
	}

	if got := ClassifyLocalIdentifier(fixtureUpstreamA, 0, targetAuth, fixture.lookup); got != IdentifierTargetUpstream {
		t.Fatalf("classify(target upstream id) = %q, want %q", got, IdentifierTargetUpstream)
	}
	if got := ClassifyLocalIdentifier(fixtureUpstreamB, 0, targetAuth, fixture.lookup); got != IdentifierForeignUpstream {
		t.Fatalf("classify(other upstream id) = %q, want %q", got, IdentifierForeignUpstream)
	}
	if got := ClassifyLocalIdentifier(fixtureUnknownID, 0, targetAuth, fixture.lookup); got != IdentifierUnknown {
		t.Fatalf("classify(unknown) = %q, want %q", got, IdentifierUnknown)
	}
}

// A value that two upstreams both call their own user ID must be read as the
// target's when this request targets that upstream. String equality alone cannot
// decide which server a value belongs to.
func TestIdentifierLookupSameValueAcrossUpstreams(t *testing.T) {
	idStore, err := NewIDStore(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewIDStore: %v", err)
	}
	t.Cleanup(func() { _ = idStore.Close() })

	const shared = "same-real-user-id"
	lookup := newIdentifierLookup(IdentifierSources{
		VirtualIDs:      idStore,
		Tokens:          &stubTokenSource{issued: map[string]string{}},
		Users:           &stubUserSource{users: map[string]struct{}{}},
		UpstreamUserIDs: map[int]string{0: shared, 1: shared},
	})

	if got := lookup.Classify(shared, 0, shared); got != IdentifierTargetUpstream {
		t.Fatalf("classify on target server = %q, want %q", got, IdentifierTargetUpstream)
	}
	if got := lookup.Classify(shared, 1, shared); got != IdentifierTargetUpstream {
		t.Fatalf("classify on the other server = %q, want %q", got, IdentifierTargetUpstream)
	}
	if got := lookup.Classify(shared, 2, "third-server-user"); got != IdentifierForeignUpstream {
		t.Fatalf("classify when targeting a third server = %q, want %q", got, IdentifierForeignUpstream)
	}
}

// A source that could not be consulted must not be reported as "confirmed not
// local": the negative fields are only meaningful when coverage is complete.
func TestIdentifierLookupCoverageGap(t *testing.T) {
	lookup := newIdentifierLookup(IdentifierSources{})
	facts := lookup.Match(fixtureAliceID)
	if !facts.CoverageIncomplete {
		t.Fatalf("missing sources should be reported as a coverage gap")
	}
	if facts.LocalUser || facts.LocalToken || facts.VirtualResource {
		t.Fatalf("an uncovered lookup must not claim membership: %+v", facts)
	}
}
