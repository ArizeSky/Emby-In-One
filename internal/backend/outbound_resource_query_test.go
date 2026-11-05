package backend

import (
	"net/url"
	"testing"
)

func TestHopQueryRefusesAmbiguousAndSignedCredentialMutation(t *testing.T) {
	for _, tc := range []struct {
		raw     string
		trusted bool
		kind    string
	}{
		{"https://cdn.test/file?api_key=third-party&signature=opaque", false, "resource-query-provenance-required"},
		{"https://up.test/Videos/item/stream.mp4?api_key=stale&signature=opaque", true, "signed-credential-query"},
		{"https://up.test/file?api_key=stale", true, "resource-query-provenance-required"},
		{"https://cdn.test/file?api%zz=x", false, "invalid-resource-query"},
	} {
		u, _ := url.Parse(tc.raw)
		original := u.RawQuery
		err := prepareHopQuery(u, tc.trusted, "current", "current")
		prep, ok := asPreparationError(err)
		if !ok || prep.Kind != tc.kind {
			t.Fatalf("expected %s, got %v", tc.kind, err)
		}
		if u.RawQuery != original {
			t.Fatal("refusal mutated query")
		}
	}
}

func TestHopQueryFiltersKnownPrivateValueWithoutReencoding(t *testing.T) {
	u, _ := url.Parse("https://cdn.test/file?z=a%20b&api_key=current&part=2&part=1")
	if err := prepareHopQuery(u, false, "current", "current"); err != nil {
		t.Fatal(err)
	}
	if u.RawQuery != "z=a%20b&part=2&part=1" {
		t.Fatal("non-credential query bytes changed")
	}
}
