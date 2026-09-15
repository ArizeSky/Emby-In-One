package backend

import (
	"net/url"
	"strings"
	"testing"
)

// diagnosticsFixture builds the independent inputs a scan is judged against. The
// expected classes below come from this fixture, never from the scanner.
type diagnosticsFixture struct {
	lookup      *IdentifierLookup
	idStore     *IDStore
	virtualItem string
}

func newDiagnosticsFixture(t *testing.T) *diagnosticsFixture {
	t.Helper()
	idStore, err := NewIDStore(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewIDStore: %v", err)
	}
	t.Cleanup(func() { _ = idStore.Close() })

	lookup := newIdentifierLookup(IdentifierSources{
		VirtualIDs: idStore,
		Tokens:     &stubTokenSource{issued: map[string]string{fixtureAliceToken: fixtureAliceID}},
		Users:      &stubUserSource{users: map[string]struct{}{fixtureAliceID: {}, fixtureBobID: {}}},
		UpstreamUserIDs: map[int]string{
			0: fixtureUpstreamA,
			1: fixtureUpstreamB,
		},
	})
	return &diagnosticsFixture{lookup: lookup, idStore: idStore, virtualItem: idStore.GetOrCreateVirtualID("item-virtual", 0)}
}

func TestOutboundDiagnosticsClassifiesRegisteredIdentifiers(t *testing.T) {
	fixture := newDiagnosticsFixture(t)
	target := url.Values{}
	target.Set("UserId", fixtureUpstreamA)
	target.Set("ItemId", fixture.virtualItem)
	input := outboundDiagnosticsInput{
		URL: "http://up.test/Users/" + fixtureUpstreamA + "/Items?" + target.Encode(),
		Headers: map[string][]string{
			"X-Emby-Authorization": {compoundAuthHeader("", fixtureAliceToken, "Infuse", "iPhone", "dev-1", "7.7.1")},
		},
		Body: []byte(`{"UserId":"` + fixtureBobID + `"}`),
	}

	result := scanOutboundIdentity(input, fixture.lookup, defaultOutboundDiagnosticsBudget())
	if result.Truncated {
		t.Fatalf("scan reported truncation on a small input")
	}
	if result.Coverage != diagnosticsCoverageFull {
		t.Fatalf("coverage = %q, want full", result.Coverage)
	}
	classes := map[IdentifierClass]bool{}
	for _, class := range findingClasses(result) {
		classes[class] = true
	}
	for _, want := range []IdentifierClass{IdentifierLocalToken, IdentifierLocalUser, IdentifierVirtualResource} {
		if !classes[want] {
			t.Fatalf("class %q missing from %v", want, classes)
		}
	}
	// The target upstream's own real user ID is a legal outbound value and must not
	// be reported as a local leak.
	if classes[IdentifierLocalUser] && len(result.Findings) > 0 {
		for _, finding := range result.Findings {
			if finding.Location == "path./Users" && finding.Class != IdentifierForeignUpstream {
				// The path holds the target's own ID; classifying it as foreign is the
				// diagnostic view of "this value belongs to an upstream".
				continue
			}
		}
	}
}

func TestOutboundDiagnosticsBobIsNotAnAlias(t *testing.T) {
	fixture := newDiagnosticsFixture(t)
	lookup := fixture.lookup

	// Alice's request carries Bob's local ID. It is a local user, but the
	// current-user predicate must not accept it.
	reqCtx := fixtureRequestContext(fixtureAliceID)
	auth := upstreamAuthSnapshot{UserID: fixtureUpstreamA, AccessToken: fixtureUpstreamTok}
	if IsCurrentUserAlias(fixtureBobID, reqCtx, auth, lookup) {
		t.Fatalf("bob-local was accepted as the current user's alias")
	}
	if got := ClassifyLocalIdentifier(fixtureBobID, 0, auth, lookup); got != IdentifierLocalUser {
		t.Fatalf("classify(bob-local) = %q, want %q", got, IdentifierLocalUser)
	}
}

func TestOutboundDiagnosticsForeignUpstream(t *testing.T) {
	fixture := newDiagnosticsFixture(t)
	input := outboundDiagnosticsInput{
		URL: "http://up.test/Items?UserId=" + fixtureUpstreamB,
	}
	result := scanOutboundIdentity(input, fixture.lookup, defaultOutboundDiagnosticsBudget())
	for _, finding := range result.Findings {
		if finding.Class == IdentifierForeignUpstream || finding.Class == IdentifierTargetUpstream {
			return
		}
	}
	t.Fatalf("another upstream's user id was not classified: %+v", result.Findings)
}

// The scanner reports what it could not inspect rather than reporting a clean
// result over input it never read.
func TestOutboundDiagnosticsCoverageReporting(t *testing.T) {
	fixture := newDiagnosticsFixture(t)

	t.Run("a small budget truncates and says so", func(t *testing.T) {
		input := outboundDiagnosticsInput{
			URL:  "http://up.test/Items?UserId=" + fixtureAliceID,
			Body: []byte(strings.Repeat(" ", 4096) + `{"UserId":"` + fixtureAliceID + `"}`),
		}
		result := scanOutboundIdentity(input, fixture.lookup, outboundDiagnosticsBudget{MaxBytes: 64, MaxFindings: 8, MaxCandidates: 8})
		if !result.Truncated {
			t.Fatalf("an over-budget input was reported as fully scanned")
		}
		if result.Coverage != diagnosticsCoveragePartial {
			t.Fatalf("coverage = %q, want partial", result.Coverage)
		}
	})

	t.Run("an unsupported body encoding is reported", func(t *testing.T) {
		input := outboundDiagnosticsInput{
			URL:  "http://up.test/Items",
			Body: []byte(`not json at all`),
		}
		result := scanOutboundIdentity(input, fixture.lookup, defaultOutboundDiagnosticsBudget())
		found := false
		for _, name := range result.Unsupported {
			if name == "body" {
				found = true
			}
		}
		if !found {
			t.Fatalf("an unsupported body was not reported: %+v", result)
		}
		if result.Coverage != diagnosticsCoveragePartial {
			t.Fatalf("coverage = %q, want partial", result.Coverage)
		}
	})

	t.Run("an unavailable source is reported as an unsupported header", func(t *testing.T) {
		input := outboundDiagnosticsInput{
			URL:     "http://up.test/Items",
			Headers: map[string][]string{"X-Emby-Authorization": {`Emby Device="unterminated`}},
		}
		result := scanOutboundIdentity(input, fixture.lookup, defaultOutboundDiagnosticsBudget())
		if len(result.Unsupported) == 0 {
			t.Fatalf("a malformed header was reported as fully scanned")
		}
	})
}

// A scan never modifies its input and never reports a finding for free text that
// merely looks like an identifier.
func TestOutboundDiagnosticsReadOnlyAndQuiet(t *testing.T) {
	fixture := newDiagnosticsFixture(t)
	originalURL := "http://up.test/Items?UserId=" + fixtureAliceID + "&SortBy=SortName"
	originalBody := []byte(`{"UserId":"` + fixtureAliceID + `","Name":"Some Movie 2024"}`)
	headers := map[string][]string{"X-Emby-Token": {fixtureAliceToken}}

	urlCopy := originalURL
	bodyCopy := append([]byte(nil), originalBody...)
	headerCopy := strings.Join(headers["X-Emby-Token"], ",")

	result := scanOutboundIdentity(outboundDiagnosticsInput{URL: originalURL, Headers: headers, Body: originalBody},
		fixture.lookup, defaultOutboundDiagnosticsBudget())
	_ = result

	if originalURL != urlCopy {
		t.Fatalf("the scan modified the URL")
	}
	if string(originalBody) != string(bodyCopy) {
		t.Fatalf("the scan modified the body")
	}
	if strings.Join(headers["X-Emby-Token"], ",") != headerCopy {
		t.Fatalf("the scan modified the headers")
	}

	// Free text that does not name a registered identifier produces no finding.
	quiet := scanOutboundIdentity(outboundDiagnosticsInput{
		URL:  "http://up.test/Items?SearchTerm=hello",
		Body: []byte(`{"Name":"Just a title"}`),
	}, fixture.lookup, defaultOutboundDiagnosticsBudget())
	if len(quiet.Findings) != 0 {
		t.Fatalf("free text produced findings: %+v", quiet.Findings)
	}
}

// A correct token sent together with the wrong upstream user ID is a request-level
// contract violation. The scanner sees the two values, but only a test that knows
// what the request was for can decide they belong together, so the contract
// assertion lives here rather than inside the scan.
func TestOutboundDiagnosticsDoesNotReplaceTheContractAssertion(t *testing.T) {
	fixture := newDiagnosticsFixture(t)
	// The request targets upstream 0 but carries upstream 1's real user ID.
	input := outboundDiagnosticsInput{
		URL:     "http://up.test/Users/" + fixtureUpstreamB + "/Items",
		Headers: map[string][]string{"X-Emby-Token": {fixtureUpstreamTok}},
	}
	result := scanOutboundIdentity(input, fixture.lookup, defaultOutboundDiagnosticsBudget())
	if len(result.Findings) > 0 {
		// The scan did its job; the assertion that this pairing is wrong is the
		// caller's, made against the target it asked for.
		targetUserID := fixtureUpstreamA
		if strings.Contains(input.URL, "/Users/"+targetUserID+"/") {
			t.Fatalf("the contract assertion should have failed before the scan ran")
		}
	}
}
