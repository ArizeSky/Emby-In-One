package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// prepareURLForTest runs the URL half of the preparation layer the way doRequest
// does: merge the URL's own query with the params first, then prepare.
// It returns the final URL string and the changed carriers.
func prepareURLForTest(t *testing.T, rawURL string, params url.Values, reqCtx *RequestContext, auth upstreamAuthSnapshot, policy outboundIdentityPolicy) (string, []string, error) {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse %q: %v", rawURL, err)
	}
	query := parsed.Query()
	for key, values := range params {
		for _, value := range values {
			query.Add(key, value)
		}
	}
	parsed.RawQuery = query.Encode()
	result, prepErr := prepareOutboundURLWithReport(parsed, reqCtx, auth, policy)
	if prepErr != nil {
		return "", nil, prepErr
	}
	return result.url.String(), result.changed, nil
}

// testPolicy builds the policy for a normal (non-stream) API request.
func testPolicy(businessPath, method string, mode outboundAuthMode) outboundIdentityPolicy {
	return resolveOutboundPolicy(businessPath, method, false, mode, "")
}

// testStreamPolicy builds the policy for a stream request, which is the only
// shape that authenticates through the query string.
func testStreamPolicy(businessPath, method string, mode outboundAuthMode) outboundIdentityPolicy {
	return resolveOutboundPolicy(businessPath, method, true, mode, "")
}

const (
	fixtureTargetUserID = "user-A"
	fixtureTargetToken  = "token-A"
)

func fixtureAuthSnapshot() upstreamAuthSnapshot {
	return upstreamAuthSnapshot{UserID: fixtureTargetUserID, AccessToken: fixtureTargetToken}
}

func TestOutboundIdentityFinalQuery(t *testing.T) {
	auth := fixtureAuthSnapshot()
	reqCtx := fixtureRequestContext(fixtureAliceID)
	policy := testPolicy("/Users/"+fixtureAliceID+"/Items", http.MethodGet, authModeNormal)

	cases := []struct {
		name    string
		rawURL  string
		params  url.Values
		wantQ   map[string]string
		changed bool
	}{
		{
			name:    "user id only in the base url",
			rawURL:  "http://up.test/Users/" + fixtureAliceID + "/Items?UserId=" + fixtureAliceID,
			wantQ:   map[string]string{"UserId": fixtureTargetUserID},
			changed: true,
		},
		{
			name:    "user id only in params",
			rawURL:  "http://up.test/Users/" + fixtureAliceID + "/Items",
			params:  url.Values{"UserId": {fixtureAliceID}},
			wantQ:   map[string]string{"UserId": fixtureTargetUserID},
			changed: true,
		},
		{
			name:    "user id on both sides",
			rawURL:  "http://up.test/Users/" + fixtureAliceID + "/Items?UserId=" + fixtureAliceID,
			params:  url.Values{"UserId": {fixtureAliceID}},
			wantQ:   map[string]string{"UserId": fixtureTargetUserID},
			changed: true,
		},
		{
			name:    "mixed case and duplicate values collapse to one",
			rawURL:  "http://up.test/Users/" + fixtureAliceID + "/Items?USERID=" + fixtureAliceID + "&userid=" + fixtureLegacyID,
			wantQ:   map[string]string{"UserId": fixtureTargetUserID},
			changed: true,
		},
		{
			name:    "empty first value is removed rather than forwarded",
			rawURL:  "http://up.test/Users/" + fixtureAliceID + "/Items?UserId=&UserId=" + fixtureAliceID,
			wantQ:   map[string]string{"UserId": fixtureTargetUserID},
			changed: true,
		},
		{
			name:    "already correct value still ends up exactly once",
			rawURL:  "http://up.test/Users/" + fixtureAliceID + "/Items?UserId=" + fixtureTargetUserID,
			wantQ:   map[string]string{"UserId": fixtureTargetUserID},
			changed: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			finalURL, _, err := prepareURLForTest(t, tc.rawURL, tc.params, reqCtx, auth, policy)
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			parsed, err := url.Parse(finalURL)
			if err != nil {
				t.Fatalf("parse result: %v", err)
			}
			values := parsed.Query()
			for _, key := range userIDVariants(values) {
				if key != "UserId" {
					t.Fatalf("variant %q survived: %s", key, finalURL)
				}
			}
			if len(values["UserId"]) != 1 {
				t.Fatalf("UserId count = %d, want 1: %s", len(values["UserId"]), finalURL)
			}
			for key, want := range tc.wantQ {
				if got := values.Get(key); got != want {
					t.Fatalf("%s = %q, want %q", key, got, want)
				}
			}
			if !strings.Contains(parsed.Path, "/Users/"+fixtureTargetUserID) {
				t.Fatalf("path user segment not normalized: %s", parsed.Path)
			}
		})
	}
}

func TestOutboundIdentityApiKeyHandling(t *testing.T) {
	auth := fixtureAuthSnapshot()
	reqCtx := fixtureRequestContext(fixtureAliceID)

	// api_key arrives under several spellings from both the params and the base
	// URL. Every one of them must be gone, and none of them may be replaced: a
	// normal API request authenticates with the header set.
	rawURL := "http://up.test/Users/" + fixtureAliceID + "/Items?api_key=" + fixtureAliceToken + "&ApiKey=" + fixtureAliceToken
	params := url.Values{"API_KEY": {fixtureAliceToken}}

	normalPolicy := testPolicy("/Users/"+fixtureAliceID+"/Items", http.MethodGet, authModeNormal)
	finalURL, _, err := prepareURLForTest(t, rawURL, params, reqCtx, auth, normalPolicy)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if got := apiKeyValues(finalURL); len(got) != 0 {
		t.Fatalf("a normal request carried an api_key %v: %s", got, finalURL)
	}
	for _, secret := range []string{fixtureAliceToken, fixtureTargetToken} {
		if strings.Contains(finalURL, secret) {
			t.Fatalf("a token reached the normal request URL: %s", finalURL)
		}
	}

	// A stream request is the one shape that authenticates through the query, so
	// there the upstream token is written — exactly once, from this snapshot.
	streamPolicy := testStreamPolicy("/Videos/"+fixtureAliceID+"/master.m3u8", http.MethodGet, authModeNormal)
	streamURL, _, err := prepareURLForTest(t, rawURL, params, reqCtx, auth, streamPolicy)
	if err != nil {
		t.Fatalf("prepare stream: %v", err)
	}
	got := apiKeyValues(streamURL)
	if len(got) != 1 || got[0] != fixtureTargetToken {
		t.Fatalf("stream api_key = %v, want exactly [%s] (%s)", got, fixtureTargetToken, streamURL)
	}
	if strings.Contains(streamURL, fixtureAliceToken) {
		t.Fatalf("the local token reached the stream URL: %s", streamURL)
	}
	// No api_key spelling other than the canonical one survives.
	parsed, _ := url.Parse(streamURL)
	for key := range parsed.Query() {
		if strings.EqualFold(key, "api_key") && key != "api_key" {
			t.Fatalf("api_key spelling %q survived: %s", key, streamURL)
		}
	}
}

// apiKeyValues returns every api_key value in a prepared URL, whatever its case.
func apiKeyValues(rawURL string) []string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	var out []string
	for key, values := range parsed.Query() {
		if strings.EqualFold(key, "api_key") {
			out = append(out, values...)
		}
	}
	return out
}

func TestOutboundIdentityAbsentUserID(t *testing.T) {
	auth := fixtureAuthSnapshot()
	reqCtx := fixtureRequestContext(fixtureAliceID)
	policy := testPolicy("/Users/"+fixtureAliceID+"/Items", http.MethodGet, authModeNormal)

	finalURL, _, err := prepareURLForTest(t, "http://up.test/Users/"+fixtureAliceID+"/Items?Limit=10", nil, reqCtx, auth, policy)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	parsed, _ := url.Parse(finalURL)
	if len(userIDVariants(parsed.Query())) != 0 {
		t.Fatalf("a UserId the client never sent was added: %s", finalURL)
	}
	if parsed.Query().Get("Limit") != "10" {
		t.Fatalf("unrelated query changed: %s", finalURL)
	}

	// A nil params map and a nil body must not panic, and must not invent fields.
	if _, err := prepareOutboundURL(nil, reqCtx, auth, policy); err == nil {
		t.Fatalf("a nil URL should be reported as an unparsable input")
	}
	preparedBody, err := prepareOutboundBody(nil, reqCtx, auth, policy)
	if err != nil || preparedBody != nil {
		t.Fatalf("nil body = %v, %v; want nil, nil", preparedBody, err)
	}
	// A body without a UserId keeps it absent.
	body := map[string]any{"Name": "x"}
	prepared, err := prepareOutboundBody(body, reqCtx, auth, policy)
	if err != nil {
		t.Fatalf("body: %v", err)
	}
	if _, ok := prepared.(map[string]any)["UserId"]; ok {
		t.Fatalf("UserId injected into a body that did not carry one")
	}
}

func TestOutboundIdentitySemanticPath(t *testing.T) {
	auth := fixtureAuthSnapshot()
	reqCtx := fixtureRequestContext(fixtureAliceID)

	t.Run("virtual user segment is replaced", func(t *testing.T) {
		policy := testPolicy("/Users/"+fixtureAliceID+"/Items", http.MethodGet, authModeNormal)
		finalURL, changed, err := prepareURLForTest(t, "http://up.test/Users/"+fixtureAliceID+"/Items", nil, reqCtx, auth, policy)
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		if !strings.HasSuffix(mustParseURL(t, finalURL).Path, "/Users/"+fixtureTargetUserID+"/Items") {
			t.Fatalf("final URL path = %s", mustParseURL(t, finalURL).Path)
		}
		if outboundChangeSummary(changed) != carrierPath {
			t.Fatalf("changed = %v, want the path carrier", changed)
		}
	})

	t.Run("legacy alias is replaced", func(t *testing.T) {
		policy := testPolicy("/Users/"+fixtureLegacyID+"/Items", http.MethodGet, authModeNormal)
		finalURL, _, err := prepareURLForTest(t, "http://up.test/Users/"+fixtureLegacyID+"/Items", nil, reqCtx, auth, policy)
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		if strings.Contains(finalURL, fixtureLegacyID) {
			t.Fatalf("legacy alias survived: %s", finalURL)
		}
	})

	t.Run("static routes keep their literal segment", func(t *testing.T) {
		for _, path := range []string{"/Users/Me", "/Users/Public", "/Users/New", "/Users/Me/FavoriteItems"} {
			policy := testPolicy(path, http.MethodGet, authModeNormal)
			finalURL, _, err := prepareURLForTest(t, "http://up.test"+path, nil, reqCtx, auth, policy)
			if err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			if !strings.Contains(finalURL, path) {
				t.Fatalf("%s was rewritten to %s", path, finalURL)
			}
		}
	})

	t.Run("the same text elsewhere in the path is untouched", func(t *testing.T) {
		// A deployment prefix and an unrelated path segment that happens to contain
		// the user ID must not be rewritten.
		path := "/emby/" + fixtureAliceID + "/Users/" + fixtureAliceID + "/Items"
		policy := resolveOutboundPolicy("/Users/"+fixtureAliceID+"/Items", http.MethodGet, false, authModeNormal, "http://up.test/emby/"+fixtureAliceID)
		finalURL, _, err := prepareURLForTest(t, "http://up.test"+path, nil, reqCtx, auth, policy)
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		wantPrefix := "/emby/" + fixtureAliceID + "/Users/" + fixtureTargetUserID + "/Items"
		if got := mustParseURL(t, finalURL).Path; got != wantPrefix {
			t.Fatalf("final URL path = %s, want %s", got, wantPrefix)
		}
	})

	t.Run("an unclassified user segment passes through", func(t *testing.T) {
		// The path table only lists the declared current-user shapes. An endpoint
		// outside it keeps whatever segment the client sent, including another local
		// user's ID.
		unclassified := testPolicy("/Users/"+fixtureBobID+"/SomethingElse", http.MethodGet, authModeNormal)
		finalURL, _, err := prepareURLForTest(t, "http://up.test/Users/"+fixtureBobID+"/SomethingElse", nil, reqCtx, auth, unclassified)
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		if !strings.Contains(mustParseURL(t, finalURL).Path, "/Users/"+fixtureBobID+"/") {
			t.Fatalf("an unclassified user segment was rewritten: %s", finalURL)
		}
	})

	t.Run("a declared current-user path normalizes its segment", func(t *testing.T) {
		declared := testPolicy("/Users/"+fixtureBobID+"/Items", http.MethodGet, authModeNormal)
		finalURL, _, err := prepareURLForTest(t, "http://up.test/Users/"+fixtureBobID+"/Items", nil, reqCtx, auth, declared)
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		if !strings.Contains(mustParseURL(t, finalURL).Path, "/Users/"+fixtureTargetUserID+"/") {
			t.Fatalf("a declared current-user path did not normalize its segment: %s", finalURL)
		}
	})
}

func TestOutboundIdentityForeignUser(t *testing.T) {
	auth := fixtureAuthSnapshot()
	reqCtx := fixtureRequestContext(fixtureAliceID)

	t.Run("a supported endpoint normalizes to the target even when another upstream owns the value", func(t *testing.T) {
		policy := testPolicy("/Users/"+fixtureAliceID+"/Items", http.MethodGet, authModeNormal)
		finalURL, _, err := prepareURLForTest(t,
			"http://up.test/Users/"+fixtureAliceID+"/Items?UserId="+fixtureUpstreamB, nil, reqCtx, auth, policy)
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		if strings.Contains(finalURL, fixtureUpstreamB) {
			t.Fatalf("the other upstream's user id was forwarded: %s", finalURL)
		}
		if !strings.Contains(finalURL, "UserId="+fixtureTargetUserID) {
			t.Fatalf("final URL = %s", finalURL)
		}
	})

	t.Run("an unclassified endpoint keeps a foreign value", func(t *testing.T) {
		policy := testPolicy("/Some/Unknown/Endpoint", http.MethodGet, authModeNormal)
		finalURL, _, err := prepareURLForTest(t,
			"http://up.test/Some/Unknown/Endpoint?UserId="+fixtureUpstreamB, nil, reqCtx, auth, policy)
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		if !strings.Contains(finalURL, "UserId="+fixtureUpstreamB) {
			t.Fatalf("an unclassified foreign value was rewritten: %s", finalURL)
		}
	})

	t.Run("an unclassified read normalizes a pure self alias", func(t *testing.T) {
		policy := testPolicy("/Some/Unknown/Endpoint", http.MethodGet, authModeNormal)
		finalURL, _, err := prepareURLForTest(t,
			"http://up.test/Some/Unknown/Endpoint?UserId="+fixtureAliceID, nil, reqCtx, auth, policy)
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		if !strings.Contains(finalURL, "UserId="+fixtureTargetUserID) {
			t.Fatalf("fallback read did not normalize the alias: %s", finalURL)
		}
	})

	t.Run("an unclassified read with mixed known and unknown values keeps them", func(t *testing.T) {
		policy := testPolicy("/Some/Unknown/Endpoint", http.MethodGet, authModeNormal)
		finalURL, _, err := prepareURLForTest(t,
			"http://up.test/Some/Unknown/Endpoint?UserId="+fixtureAliceID+"&UserId="+fixtureBobID, nil, reqCtx, auth, policy)
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		if !strings.Contains(finalURL, fixtureBobID) {
			t.Fatalf("a mixed unclassified query was rewritten: %s", finalURL)
		}
	})

	t.Run("a write is never rewritten by the read exception", func(t *testing.T) {
		policy := testPolicy("/Some/Unknown/Write", http.MethodPost, authModeNormal)
		body := map[string]any{"UserId": fixtureAliceID, "TargetUserId": fixtureBobID}
		prepared, err := prepareOutboundBody(body, reqCtx, auth, policy)
		if err != nil {
			t.Fatalf("body: %v", err)
		}
		preparedMap, _ := prepared.(map[string]any)
		if preparedMap["UserId"] != fixtureAliceID {
			t.Fatalf("an unclassified write body was rewritten: %#v", preparedMap)
		}
		if preparedMap["TargetUserId"] != fixtureBobID {
			t.Fatalf("an authorization target was rewritten: %#v", preparedMap)
		}
	})
}

func TestOutboundIdentityMissingAuthState(t *testing.T) {
	reqCtx := fixtureRequestContext(fixtureAliceID)
	empty := upstreamAuthSnapshot{}

	policy := testPolicy("/Users/"+fixtureAliceID+"/Items", http.MethodGet, authModeNormal)
	_, _, err := prepareURLForTest(t, "http://up.test/Users/"+fixtureAliceID+"/Items", nil, reqCtx, empty, policy)
	status, ok := preparationErrorStatus(err)
	if !ok || status != http.StatusServiceUnavailable {
		t.Fatalf("missing auth state = %v (status %d, ok %v), want 503", err, status, ok)
	}

	// The path segment is always present on a declared current-user path, so an
	// empty snapshot is reported there. A query field the client never sent is not
	// an error on its own. An endpoint with no identity rule needs no snapshot.
	unclassified := testPolicy("/System/Info/Public", http.MethodGet, authModeNormal)
	if _, _, err := prepareURLForTest(t, "http://up.test/System/Info/Public?Limit=1", nil, reqCtx, empty, unclassified); err != nil {
		t.Fatalf("an endpoint with no identity rule should not need auth state: %v", err)
	}

	// Bootstrap requests are exempt: an empty user ID is expected there.
	loginPolicy := testPolicy("/Users/AuthenticateByName", http.MethodPost, authModePasswordLogin)
	if _, err := prepareOutboundURL(mustParseURL(t, "http://up.test/Users/AuthenticateByName"), reqCtx, empty, loginPolicy); err != nil {
		t.Fatalf("password login should not require auth state: %v", err)
	}
	apiKeyPolicy := testPolicy("/Users/Me", http.MethodGet, authModeAPIKeyValidation)
	if _, err := prepareOutboundURL(mustParseURL(t, "http://up.test/Users/Me"), reqCtx, empty, apiKeyPolicy); err != nil {
		t.Fatalf("api key validation should not require auth state: %v", err)
	}
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return parsed
}

func TestOutboundIdentityDetachedContext(t *testing.T) {
	// An aggregation background task runs with context.Background(), so the
	// identity cannot be recovered from the context value: it must come from the
	// explicit reqCtx the caller passed.
	reqCtx := fixtureRequestContext(fixtureAliceID)

	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("UserId"); got != fixtureTargetUserID {
			t.Errorf("upstream UserId = %q, want %q", got, fixtureTargetUserID)
		}
		if !strings.HasSuffix(r.URL.Path, "/Users/"+fixtureTargetUserID+"/Items") {
			t.Errorf("upstream path = %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"Items": []any{}})
	}))
	defer stub.Close()

	client := newUpstreamClient(Config{}, UpstreamConfig{Name: "A", URL: stub.URL}, 0, nil)
	client.setOnline(fixtureTargetToken, fixtureTargetUserID)

	// context.Background() has no request context value, so requestContextFrom
	// returns nil and only the explicit reqCtx can supply the identity.
	params := url.Values{"UserId": {fixtureAliceID}}
	resp, err := client.doRequest(context.Background(), reqCtx, http.MethodGet, "/Users/"+fixtureAliceID+"/Items", params, nil, nil, false)
	if err != nil {
		t.Fatalf("doRequest: %v", err)
	}
	defer resp.Body.Close()
}

func TestOutboundIdentityPerUpstream(t *testing.T) {
	// The same body and query go to two upstreams at once. Each must come out with
	// its own identity and token, and the caller's data must be unchanged.
	type record struct {
		userID string
		token  string
		path   string
	}
	var mu sync.Mutex
	records := map[string]record{}

	newStub := func(userID, token string) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			records[userID] = record{userID: r.URL.Query().Get("UserId"), token: r.Header.Get("X-Emby-Token"), path: r.URL.Path}
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": []any{}})
		}))
	}
	stubA := newStub("user-A", "token-A")
	defer stubA.Close()
	stubB := newStub("user-B", "token-B")
	defer stubB.Close()

	clientA := newUpstreamClient(Config{}, UpstreamConfig{Name: "A", URL: stubA.URL}, 0, nil)
	clientA.setOnline("token-A", "user-A")
	clientB := newUpstreamClient(Config{}, UpstreamConfig{Name: "B", URL: stubB.URL}, 1, nil)
	clientB.setOnline("token-B", "user-B")

	reqCtx := fixtureRequestContext(fixtureAliceID)
	sharedBody := map[string]any{"UserId": fixtureAliceID, "ItemId": "virtual-item"}
	sharedParams := url.Values{"UserId": {fixtureAliceID}}

	var wg sync.WaitGroup
	for _, client := range []*UpstreamClient{clientA, clientB} {
		wg.Add(1)
		go func(c *UpstreamClient) {
			defer wg.Done()
			resp, err := c.doRequest(context.Background(), reqCtx, http.MethodPost, "/Sessions/Playing", sharedParams, sharedBody, nil, false)
			if err != nil {
				t.Errorf("doRequest: %v", err)
				return
			}
			defer resp.Body.Close()
		}(client)
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if records["user-A"].userID != "user-A" || records["user-A"].token != "token-A" {
		t.Fatalf("server A saw %+v", records["user-A"])
	}
	if records["user-B"].userID != "user-B" || records["user-B"].token != "token-B" {
		t.Fatalf("server B saw %+v", records["user-B"])
	}
	if sharedBody["UserId"] != fixtureAliceID {
		t.Fatalf("the caller's body was modified: %#v", sharedBody)
	}
	if sharedParams.Get("UserId") != fixtureAliceID {
		t.Fatalf("the caller's params were modified: %v", sharedParams)
	}
}

func TestOutboundRawBodyPreserved(t *testing.T) {
	reqCtx := fixtureRequestContext(fixtureAliceID)
	auth := fixtureAuthSnapshot()

	raw := []byte("  {\"UserId\":\"" + fixtureAliceID + "\"}  ")
	policy := testPolicy("/Sessions/Playing", http.MethodPost, authModeNormal)

	// A body declared as something other than JSON keeps its bytes exactly,
	// including leading and trailing whitespace.
	nonJSON := rawRequestBody{data: raw, contentType: "application/x-www-form-urlencoded"}
	prepared, err := prepareOutboundBody(nonJSON, reqCtx, auth, policy)
	if err != nil {
		t.Fatalf("body: %v", err)
	}
	typed, ok := prepared.(rawRequestBody)
	if !ok {
		t.Fatalf("body type = %T", prepared)
	}
	if string(typed.data) != string(raw) {
		t.Fatalf("raw bytes changed: %q", string(typed.data))
	}
	if typed.contentType != "application/x-www-form-urlencoded" {
		t.Fatalf("content type changed: %q", typed.contentType)
	}

	// The same payload declared as JSON is decoded and normalized instead.
	jsonBody := rawRequestBody{data: raw, contentType: "application/json"}
	prepared, err = prepareOutboundBody(jsonBody, reqCtx, auth, policy)
	if err != nil {
		t.Fatalf("json body: %v", err)
	}
	asMap, ok := prepared.(map[string]any)
	if !ok {
		t.Fatalf("json body type = %T", prepared)
	}
	if asMap["UserId"] != fixtureTargetUserID {
		t.Fatalf("json body UserId = %v, want %v", asMap["UserId"], fixtureTargetUserID)
	}

	// A JSON body that is not an object is left as it is rather than being forced
	// into one.
	arrayBody := rawRequestBody{data: []byte(`[1,2,3]`), contentType: "application/json"}
	prepared, err = prepareOutboundBody(arrayBody, reqCtx, auth, policy)
	if err != nil {
		t.Fatalf("array body: %v", err)
	}
	if _, ok := prepared.(rawRequestBody); !ok {
		t.Fatalf("a non-object JSON root was converted to %T", prepared)
	}
}

func TestOutboundBodyNestedFieldsUntouched(t *testing.T) {
	reqCtx := fixtureRequestContext(fixtureAliceID)
	auth := fixtureAuthSnapshot()
	policy := testPolicy("/Sessions/Playing", http.MethodPost, authModeNormal)

	body := map[string]any{
		"UserId": fixtureAliceID,
		"Nested": map[string]any{"UserId": fixtureAliceID},
		"Items":  []any{map[string]any{"UserId": fixtureAliceID}},
	}
	prepared, err := prepareOutboundBody(body, reqCtx, auth, policy)
	if err != nil {
		t.Fatalf("body: %v", err)
	}
	asMap, _ := prepared.(map[string]any)
	if asMap["UserId"] != fixtureTargetUserID {
		t.Fatalf("top-level UserId = %v", asMap["UserId"])
	}
	if nested := asMap["Nested"].(map[string]any); nested["UserId"] != fixtureAliceID {
		t.Fatalf("a nested UserId was rewritten: %#v", nested)
	}
	if item := asMap["Items"].([]any)[0].(map[string]any); item["UserId"] != fixtureAliceID {
		t.Fatalf("a nested array UserId was rewritten: %#v", item)
	}
	// The caller's own map is shared with the copy, but its top-level field is not
	// overwritten in place.
	if body["UserId"] != fixtureAliceID {
		t.Fatalf("the caller's body was modified in place")
	}
}

func TestOutboundIdentityRecovery(t *testing.T) {
	// A missing authentication state has no upstream response, so the debounced
	// recovery callback must be scheduled from the preparation error path itself.
	recovered := make(chan struct{}, 1)
	client := newUpstreamClient(Config{}, UpstreamConfig{Name: "A", URL: "http://up.invalid"}, 0, nil)
	client.onAuthError = func(*UpstreamClient) {
		select {
		case recovered <- struct{}{}:
		default:
		}
	}

	reqCtx := fixtureRequestContext(fixtureAliceID)
	_, err := client.doRequest(context.Background(), reqCtx, http.MethodGet, "/Users/"+fixtureAliceID+"/Items", nil, nil, nil, false)
	if _, ok := preparationErrorStatus(err); !ok {
		t.Fatalf("expected a preparation error, got %v", err)
	}
	select {
	case <-recovered:
	case <-time.After(2 * time.Second):
		t.Fatalf("recovery was never scheduled for a missing auth state")
	}

	// A client-input error must not trigger a reconnect.
	client2 := newUpstreamClient(Config{}, UpstreamConfig{Name: "B", URL: "http://up.invalid"}, 0, nil)
	triggered := make(chan struct{}, 1)
	client2.onAuthError = func(*UpstreamClient) {
		select {
		case triggered <- struct{}{}:
		default:
		}
	}
	client2.setOnline("token", "user")
	if _, err := client2.BuildURL("/Users/%zz/Items", nil, false, nil); err == nil {
		t.Fatalf("expected an unparsable URL error")
	}
	select {
	case <-triggered:
		t.Fatalf("a client-input error triggered a reconnect")
	case <-time.After(200 * time.Millisecond):
	}
}

func TestOutboundIdentityBootstrap(t *testing.T) {
	// The password login must present no user ID and no previous token; the API key
	// validation must keep the configured key while its user ID is still unknown.
	loginHeaders := http.Header{}
	loginHeaders.Set("X-Emby-Client", "Infuse")
	loginHeaders.Set("X-Emby-Device-Name", "iPhone")
	loginHeaders.Set("X-Emby-Device-Id", "device-1")
	loginHeaders.Set("X-Emby-Client-Version", "7.7.1")
	loginHeaders.Set("X-Emby-Token", "stale-token")
	loginHeaders.Set("X-Emby-Authorization", `Emby UserId="`+fixtureAliceID+`", Token="`+fixtureAliceToken+`", Client="Infuse", Device="iPhone", DeviceId="device-1", Version="7.7.1"`)

	prepared := prepareOutboundHeaders(loginHeaders, nil, upstreamAuthSnapshot{}, authModePasswordLogin)
	if prepared.Get("X-Emby-Token") != "" {
		t.Fatalf("the login request carried a token: %q", prepared.Get("X-Emby-Token"))
	}
	authHeader := prepared.Get("X-Emby-Authorization")
	if strings.Contains(authHeader, fixtureAliceID) || strings.Contains(authHeader, fixtureAliceToken) {
		t.Fatalf("the login request carried a local identity: %q", authHeader)
	}
	if !strings.Contains(authHeader, `Client="Infuse"`) {
		t.Fatalf("the login request lost its device identity: %q", authHeader)
	}

	apiKeyHeaders := http.Header{}
	apiKeyHeaders.Set("X-Emby-Token", "configured-api-key")
	prepared = prepareOutboundHeaders(apiKeyHeaders, nil, upstreamAuthSnapshot{AccessToken: "configured-api-key"}, authModeAPIKeyValidation)
	if prepared.Get("X-Emby-Token") != "configured-api-key" {
		t.Fatalf("the configured API key was dropped: %q", prepared.Get("X-Emby-Token"))
	}

	// A normal request gets the snapshot's identity and nothing from the client.
	normal := prepareOutboundHeaders(loginHeaders, nil, fixtureAuthSnapshot(), authModeNormal)
	if normal.Get("X-Emby-Token") != fixtureTargetToken {
		t.Fatalf("normal request token = %q, want %q", normal.Get("X-Emby-Token"), fixtureTargetToken)
	}
	normalAuth := normal.Get("X-Emby-Authorization")
	if strings.Contains(normalAuth, fixtureAliceID) || strings.Contains(normalAuth, fixtureAliceToken) {
		t.Fatalf("a local identity survived in the normal request: %q", normalAuth)
	}
	if !strings.Contains(normalAuth, `UserId="`+fixtureTargetUserID+`"`) {
		t.Fatalf("the normal request does not carry the snapshot identity: %q", normalAuth)
	}
	if normal.Get("X-Emby-Client") != "Infuse" || normal.Get("X-Emby-Device-Id") != "device-1" {
		t.Fatalf("device identity changed: %q %q", normal.Get("X-Emby-Client"), normal.Get("X-Emby-Device-Id"))
	}
}

func TestOutboundAuthorizationHeaderEscaping(t *testing.T) {
	// A value containing a quote or a comma must not be able to forge another
	// parameter in the rebuilt header.
	header := canonicalAuthorizationHeader("uid", `Dev"ice, UserId="attacker`, "dev-1", "1.0", "Cli\"ent")
	parsed, ok := parseAuthorizationIdentityStrict(header)
	if !ok {
		t.Fatalf("the rebuilt header is not parseable: %q", header)
	}
	if parsed["UserId"] != "uid" {
		t.Fatalf("UserId = %q, want uid", parsed["UserId"])
	}
	if parsed["Device"] != `Dev"ice, UserId="attacker` {
		t.Fatalf("Device = %q", parsed["Device"])
	}
	if parsed["Client"] != `Cli"ent` {
		t.Fatalf("Client = %q", parsed["Client"])
	}
}

func TestOutboundLogging(t *testing.T) {
	const sentinelToken = "SENTINEL-TOKEN-VALUE"
	const sentinelKey = "SENTINEL-API-KEY"

	logger := NewLogger(LogConfig{DataDir: t.TempDir(), Level: "debug", FileLevel: "debug"})
	defer func() { _ = logger.Close() }()

	format := formatOutboundURLForLog("http://up.test/Users/user-A/Items?UserId=user-A&api_key=" + sentinelKey + "&X-Emby-Token=" + sentinelToken + "&Other=secret")
	if strings.Contains(format, sentinelKey) || strings.Contains(format, sentinelToken) {
		t.Fatalf("a credential reached the log format: %s", format)
	}
	if !strings.Contains(format, "UserId=user-A") {
		t.Fatalf("the route field was dropped: %s", format)
	}

	// A network error that carries a URL with a token must be rewritten.
	redacted := redactURLInError(fmt.Errorf("Get \"http://up.test/Items?api_key=%s\": dial tcp: timeout", sentinelKey))
	if strings.Contains(redacted, sentinelKey) {
		t.Fatalf("a credential survived error redaction: %s", redacted)
	}

	logger.Debugf("outbound %s", format)
	logger.Errorf("outbound failed: %s", redacted)
	entries := logger.Entries(0)
	if len(entries) == 0 {
		t.Fatalf("no log entries were captured")
	}
	for _, entry := range entries {
		if strings.Contains(entry.Message, sentinelKey) || strings.Contains(entry.Message, sentinelToken) {
			t.Fatalf("a credential reached the in-memory log: %s", entry.Message)
		}
	}
	// The on-disk log is a second sink: a credential that only avoids the memory
	// buffer would still be persisted.
	if path := logger.FilePath(); path != "" {
		raw, readErr := os.ReadFile(path)
		if readErr == nil {
			if strings.Contains(string(raw), sentinelKey) || strings.Contains(string(raw), sentinelToken) {
				t.Fatalf("a credential reached the log file")
			}
		}
	}
}
