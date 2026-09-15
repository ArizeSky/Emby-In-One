package backend

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// The omission-equivalence set is deliberately empty in this batch. Every entry
// added later needs a recorded method, route, field, upstream version,
// authentication and input shape plus a response-equivalence test; until then the
// policy must not delete a UserId anywhere.
func TestOutboundIdentityPolicy_OmitSetStaysEmpty(t *testing.T) {
	omitSet := []string{} // the declared empty set
	if len(omitSet) != 0 {
		t.Fatalf("the omission-equivalence set must start empty")
	}

	// No declared current-user endpoint may drop a UserId it was given.
	paths := []struct {
		path   string
		method string
	}{
		{"/Items", http.MethodGet},
		{"/Shows/NextUp", http.MethodGet},
		{"/Users/" + fixtureAliceID + "/Items", http.MethodGet},
		{"/Users/" + fixtureAliceID + "/Views", http.MethodGet},
		{"/Genres", http.MethodGet},
		{"/Search/Hints", http.MethodGet},
		{"/Shows/" + fixtureAliceID + "/Seasons", http.MethodGet},
		{"/Sessions/Playing", http.MethodPost},
		{"/Sessions/Playing/Progress", http.MethodPost},
		{"/Sessions/Playing/Stopped", http.MethodPost},
		{"/Sessions/Capabilities", http.MethodPost},
		{"/Items/" + fixtureAliceID + "/PlaybackInfo", http.MethodPost},
	}
	for _, tc := range paths {
		policy := testPolicy(tc.path, tc.method, authModeNormal)
		if policy.queryUserID == actionOmitOnBootstrap || policy.bodyUserID == actionOmitOnBootstrap {
			t.Fatalf("%s %s falls in the omission set, which must be empty", tc.method, tc.path)
		}
		if !policy.supported {
			t.Fatalf("%s %s is not declared as a current-user endpoint", tc.method, tc.path)
		}
	}
}

// An unknown path must not gain a rule the action table does not declare. The
// default is passthrough for both query and body, and the only exception is the
// self-alias read.
func TestOutboundIdentityPolicy_UnknownPathDefaults(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodDelete} {
		policy := testPolicy("/Some/Unknown/Endpoint", method, authModeNormal)
		if policy.supported {
			t.Fatalf("%s on an unknown path must not be declared supported", method)
		}
		if policy.queryUserID != actionPassthrough {
			t.Fatalf("%s query action = %q, want passthrough", method, policy.queryUserID)
		}
		if policy.bodyUserID != actionPassthrough {
			t.Fatalf("%s body action = %q, want passthrough", method, policy.bodyUserID)
		}
		if policy.pathAction != actionPassthrough {
			t.Fatalf("%s path action = %q, want passthrough", method, policy.pathAction)
		}
		wantFallback := method == http.MethodGet || method == http.MethodHead
		if policy.fallbackRead != wantFallback {
			t.Fatalf("%s fallbackRead = %v, want %v", method, policy.fallbackRead, wantFallback)
		}
		if policy.pathClass != pathClassSelfAlias {
			t.Fatalf("%s path class = %q, want %q", method, policy.pathClass, pathClassSelfAlias)
		}
	}
}

// A UserId-carrying endpoint that is not declared must never be given a 400 for
// the value it carries: the default action is passthrough, and the identity layer
// has no rejection branch for an unclassified value.
func TestOutboundIdentityPolicy_NoRejectionForUnknownValues(t *testing.T) {
	reqCtx := fixtureRequestContext(fixtureAliceID)
	auth := fixtureAuthSnapshot()

	cases := []string{fixtureBobID, fixtureUpstreamB, fixtureAliceToken, fixtureUnknownID, "0", "-1", strings.Repeat("a", 64)}
	for _, value := range cases {
		policy := testPolicy("/Some/Unknown/Endpoint", http.MethodGet, authModeNormal)
		finalURL, _, err := prepareURLForTest(t,
			"http://up.test/Some/Unknown/Endpoint?UserId="+url.QueryEscape(value), nil, reqCtx, auth, policy)
		if err != nil {
			t.Fatalf("value %q was rejected: %v", value, err)
		}
		if !strings.Contains(finalURL, "UserId="+url.QueryEscape(value)) {
			t.Fatalf("value %q was rewritten to %s", value, finalURL)
		}
	}
}

// The exact-alias exception only covers the current request's own aliases. It
// must not grow into "any registered local ID becomes the current user".
func TestOutboundIdentityPolicy_ExactSelfAliasException(t *testing.T) {
	reqCtx := fixtureRequestContext(fixtureAliceID)
	auth := fixtureAuthSnapshot()
	policy := testPolicy("/Some/Unknown/Endpoint", http.MethodGet, authModeNormal)

	aliases := []string{fixtureAliceID, fixtureLegacyID, fixtureTargetUserID}
	for _, alias := range aliases {
		finalURL, _, err := prepareURLForTest(t,
			"http://up.test/Some/Unknown/Endpoint?UserId="+alias, nil, reqCtx, auth, policy)
		if err != nil {
			t.Fatalf("alias %q: %v", alias, err)
		}
		if !strings.Contains(finalURL, "UserId="+fixtureTargetUserID) {
			t.Fatalf("alias %q was not normalized: %s", alias, finalURL)
		}
	}

	notAliases := []string{fixtureBobID, fixtureUpstreamB, fixtureUnknownID}
	for _, value := range notAliases {
		finalURL, _, err := prepareURLForTest(t,
			"http://up.test/Some/Unknown/Endpoint?UserId="+value, nil, reqCtx, auth, policy)
		if err != nil {
			t.Fatalf("value %q: %v", value, err)
		}
		if strings.Contains(finalURL, fixtureTargetUserID) {
			t.Fatalf("value %q was wrongly normalized: %s", value, finalURL)
		}
	}
}

// Bootstrap paths must never be treated as business routes, whatever their
// casing, and their user-position segment must survive verbatim.
func TestOutboundIdentityPolicy_StaticRoutes(t *testing.T) {
	for _, path := range []string{"/Users/AuthenticateByName", "/users/authenticatebyname", "/Users/Me", "/Users/Public", "/Users/New"} {
		policy := testPolicy(path, http.MethodPost, authModeNormal)
		if policy.pathAction != actionPassthrough {
			t.Fatalf("%s path action = %q, want passthrough", path, policy.pathAction)
		}
	}
}
