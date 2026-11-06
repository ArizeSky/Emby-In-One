package backend

import (
	"net/url"
	"path"
	"strings"
	"testing"
)

func TestResourceQueryProvenancePreservesAuthoritativeExternalSignedQuery(t *testing.T) {
	raw := "https://cdn.test/file?z=a%20b&api_key=cdn-own&signature=opaque&part=2&part=1"
	target, _ := url.Parse(raw)
	provenance := newResourceQueryProvenance(resourceQueryProvenancePlaybackInfo, raw, "upstream-token", "local-token")
	if provenance == nil {
		t.Fatal("authoritative PlaybackInfo URL did not produce provenance")
	}
	if err := prepareHopQueryWithProvenance(target, false, "current", provenance, "current"); err != nil {
		t.Fatal(err)
	}
	if want := "z=a%20b&api_key=cdn-own&signature=opaque&part=2&part=1"; target.RawQuery != want {
		t.Fatalf("authoritative query changed: %q, want %q", target.RawQuery, want)
	}
}

func TestResourceQueryProvenanceRemovesKnownPrivateValueByteExact(t *testing.T) {
	raw := "https://cdn.test/seg.ts?z=a%20b&api_key=upstream-token&part=2&part=1"
	target, _ := url.Parse(raw)
	provenance := newResourceQueryProvenance(resourceQueryProvenanceManifest, raw, "upstream-token")
	if err := prepareHopQueryWithProvenance(target, false, "current", provenance, "current"); err != nil {
		t.Fatal(err)
	}
	if want := "z=a%20b&part=2&part=1"; target.RawQuery != want {
		t.Fatalf("private filter changed other bytes: %q, want %q", target.RawQuery, want)
	}
}

func TestResourceQueryProvenanceSignedPrivateMutationFails(t *testing.T) {
	raw := "https://cdn.test/seg.ts?api_key=upstream-token&signature=opaque"
	target, _ := url.Parse(raw)
	provenance := newResourceQueryProvenance(resourceQueryProvenanceManifest, raw, "upstream-token")
	err := prepareHopQueryWithProvenance(target, false, "current", provenance, "current")
	preparation, ok := asPreparationError(err)
	if !ok || preparation.Kind != "signed-credential-query" {
		t.Fatalf("expected signed-credential-query, got %v", err)
	}
	if target.RawQuery != "api_key=upstream-token&signature=opaque" {
		t.Fatal("refusal mutated the signed query")
	}
}

func TestResourceQueryProvenanceOnlyAuthorizesExactTarget(t *testing.T) {
	registered := "https://cdn.test/a.ts?api_key=cdn-own&signature=opaque"
	provenance := newResourceQueryProvenance(resourceQueryProvenancePlaybackInfo, registered)
	target, _ := url.Parse("https://cdn.test/b.ts?api_key=client-supplied&signature=opaque")
	err := prepareHopQueryWithProvenance(target, false, "current", provenance, "current")
	preparation, ok := asPreparationError(err)
	if !ok || preparation.Kind != "resource-query-provenance-required" {
		t.Fatalf("provenance authorized a different URL: %v", err)
	}
}

func TestResourceQueryProvenanceRedirectCarriesPrivateValues(t *testing.T) {
	registered := "https://up.test/Videos/item/stream?api_key=old-upstream&sig=opaque"
	provenance := newResourceQueryProvenance(resourceQueryProvenancePlaybackInfo, registered, "old-upstream", "local-token")

	cdnURL := "https://cdn.test/seg.ts?api_key=cdn-own&signature=opaque"
	cdnTarget, _ := url.Parse(cdnURL)
	cdnProvenance := provenance.deriveRedirect(cdnURL, "current")
	if err := prepareHopQueryWithProvenance(cdnTarget, false, "current", cdnProvenance, "current"); err != nil {
		t.Fatal(err)
	}
	if cdnTarget.RawQuery != "api_key=cdn-own&signature=opaque" {
		t.Fatalf("redirect target credential changed: %q", cdnTarget.RawQuery)
	}

	oldURL := "https://cdn.test/seg.ts?api_key=old-upstream&signature=opaque"
	oldTarget, _ := url.Parse(oldURL)
	oldProvenance := provenance.deriveRedirect(oldURL, "current")
	err := prepareHopQueryWithProvenance(oldTarget, false, "current", oldProvenance, "current")
	preparation, ok := asPreparationError(err)
	if !ok || preparation.Kind != "signed-credential-query" {
		t.Fatalf("redirect did not classify the previous private token: %v", err)
	}
}

func TestResourceQueryProvenanceWithKnownPrivateDoesNotMutateRegistration(t *testing.T) {
	raw := "https://cdn.test/seg.ts?api_key=cdn-own"
	registered := newResourceQueryProvenance(resourceQueryProvenanceManifest, raw, "local-token")
	scoped := registered.withKnownPrivate("capability")
	if scoped == nil || !scoped.knowsPrivate("capability") {
		t.Fatal("request-scoped copy did not learn the capability")
	}
	if registered.knowsPrivate("capability") {
		t.Fatal("request-scoped capability changed the immutable registration")
	}
	if strings.Contains(registered.String(), "local-token") || strings.Contains(registered.String(), "cdn-own") {
		t.Fatal("provenance diagnostic exposed a credential or query")
	}
}

func TestRedirectProvenancePreservesTargetOwnCredential(t *testing.T) {
	target, _ := url.Parse("https://cdn.test/seg.ts?api_key=cdn-own&signature=opaque")
	var previous *resourceQueryProvenance
	provenance := previous.deriveRedirect(target.String(), "upstream-token")
	if err := prepareHopQueryWithProvenance(target, false, "current", provenance, "upstream-token"); err != nil {
		t.Fatal(err)
	}
	if target.RawQuery != "api_key=cdn-own&signature=opaque" {
		t.Fatalf("authoritative redirect query changed: %q", target.RawQuery)
	}

	privateTarget, _ := url.Parse("https://cdn.test/seg.ts?api_key=upstream-token&part=2")
	privateProvenance := previous.deriveRedirect(privateTarget.String(), "upstream-token")
	if err := prepareHopQueryWithProvenance(privateTarget, false, "current", privateProvenance, "upstream-token"); err != nil {
		t.Fatal(err)
	}
	if privateTarget.RawQuery != "part=2" {
		t.Fatalf("redirect private credential survived: %q", privateTarget.RawQuery)
	}
}

func TestResourceQueryProvenanceDoesNotBypassTrustedBusinessPolicy(t *testing.T) {
	signedURL := "https://up.test/Videos/item/stream.mp4?api_key=old-upstream&signature=opaque"
	signedTarget, _ := url.Parse(signedURL)
	signedProvenance := newResourceQueryProvenance(resourceQueryProvenancePlaybackInfo, signedURL, "old-upstream")
	err := prepareHopQueryWithProvenance(signedTarget, true, "current", signedProvenance, "current")
	preparation, ok := asPreparationError(err)
	if !ok || preparation.Kind != "signed-credential-query" {
		t.Fatalf("trusted signed business query was mutated: %v", err)
	}

	refreshURL := "https://up.test/Videos/item/stream.mp4?api_key=old-upstream&PlaySessionId=s1"
	refreshTarget, _ := url.Parse(refreshURL)
	refreshProvenance := newResourceQueryProvenance(resourceQueryProvenancePlaybackInfo, refreshURL, "old-upstream")
	if err := prepareHopQueryWithProvenance(refreshTarget, true, "current", refreshProvenance, "current"); err != nil {
		t.Fatal(err)
	}
	if refreshTarget.RawQuery != "api_key=current&PlaySessionId=s1" {
		t.Fatalf("trusted business auth was not refreshed: %q", refreshTarget.RawQuery)
	}
}

func TestManifestRegistrationBindsResourceQueryProvenance(t *testing.T) {
	app := &App{StreamResources: newStreamResourceStore()}
	rewritten, err := app.RewriteM3U8ForItemWithResources(
		"https://cdn.test/seg.ts?api_key=cdn-own&signature=opaque\n",
		"https://cdn.test/master.m3u8",
		"local-token",
		resourceRewriteContext{
			UserID: "u", VirtualItemID: "v", ServerID: "s", OriginalID: "o",
			Capability: "capability",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	local, err := url.Parse(strings.TrimSpace(rewritten))
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := app.StreamResources.peek(streamResourceHandleID(path.Base(local.Path)))
	if !ok {
		t.Fatal("manifest resource was not registered")
	}
	provenance := entry.Provenance
	if !provenance.attested() || !provenance.knowsPrivate("local-token") || !provenance.knowsPrivate("capability") {
		t.Fatal("manifest registration did not bind local private values")
	}
	target, _ := url.Parse(entry.ResourceURL)
	if !provenance.matches(target) {
		t.Fatal("provenance does not match its registered resource")
	}
	if err := prepareHopQueryWithProvenance(target, false, "current", provenance, "current"); err != nil {
		t.Fatal(err)
	}
	if target.RawQuery != "api_key=cdn-own&signature=opaque" {
		t.Fatalf("manifest's authoritative CDN query changed: %q", target.RawQuery)
	}
}
