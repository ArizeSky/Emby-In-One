package backend

import (
	"net/http"
	"net/url"
	"testing"
)

// TestQueryTokenLookupCoversCaseVariants verifies that the shared query-token
// helper accepts every casing a client may send for X-Emby-Token / api_key and
// that precedence is stable: api_key wins over X-Emby-Token, and the original
// spellings win over later-seen casings.
func TestQueryTokenLookupCoversCaseVariants(t *testing.T) {
	cases := []struct {
		raw     string
		wantTok string
		wantKey string
	}{
		{"api_key=aaa", "aaa", "api_key"},
		{"ApiKey=aaa", "aaa", "ApiKey"},
		{"apikey=aaa", "aaa", "apikey"},
		{"X-Emby-Token=bbb", "bbb", "X-Emby-Token"},
		{"x-emby-token=bbb", "bbb", "x-emby-token"},
		{"X-EMBY-TOKEN=bbb", "bbb", "X-EMBY-TOKEN"},
		{"x-Emby-token=bbb", "bbb", "x-Emby-token"},
		{"API_KEY=ccc", "ccc", "API_KEY"},
	}
	for _, tc := range cases {
		values, err := url.ParseQuery(tc.raw)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.raw, err)
		}
		tok, key := queryTokenLookup(values)
		if tok != tc.wantTok || key != tc.wantKey {
			t.Errorf("queryTokenLookup(%q) = (%q,%q), want (%q,%q)", tc.raw, tok, key, tc.wantTok, tc.wantKey)
		}
	}
}

// TestQueryTokenLookupPrecedence confirms api_key outranks X-Emby-Token when both
// are present, matching the historical extractToken order.
func TestQueryTokenLookupPrecedence(t *testing.T) {
	values := url.Values{}
	values.Set("X-Emby-Token", "from-x")
	values.Set("api_key", "from-api")
	tok, key := queryTokenLookup(values)
	if tok != "from-api" || key != "api_key" {
		t.Fatalf("precedence = (%q,%q), want api_key to win", tok, key)
	}
}

// TestExtractTokenMixedCaseQuery verifies extractToken itself (not just the
// helper) picks up a mixed-case X-Emby-Token query parameter.
func TestExtractTokenMixedCaseQuery(t *testing.T) {
	r := &http.Request{URL: &url.URL{RawQuery: "x-EmBy-ToKen=mixed"}}
	r.Header = http.Header{}
	if got := extractToken(r); got != "mixed" {
		t.Fatalf("extractToken = %q, want mixed", got)
	}
	if src := identifyTokenSource(r); src != "query:x-EmBy-ToKen" {
		t.Fatalf("identifyTokenSource = %q, want query:x-EmBy-ToKen", src)
	}
}

// TestExtractTokenHeaderBeatsQuery keeps the original header-over-query priority.
func TestExtractTokenHeaderBeatsQuery(t *testing.T) {
	r := &http.Request{URL: &url.URL{RawQuery: "api_key=from-query"}}
	r.Header = http.Header{}
	r.Header.Set("X-Emby-Token", "from-header")
	if got := extractToken(r); got != "from-header" {
		t.Fatalf("extractToken = %q, want from-header", got)
	}
}
