package backend

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func playbackIdentityTestOwner(userID, token string) playbackIdentityOwner {
	return newPlaybackIdentityOwner(userID, token)
}

func playbackIdentityTestRequest(owner playbackIdentityOwner) playbackAttemptRequest {
	return playbackIdentityTestRequestOn(owner, "server-a")
}

func playbackIdentityTestRequestOn(owner playbackIdentityOwner, serverID string) playbackAttemptRequest {
	return playbackAttemptRequest{
		Owner:         owner,
		ServerID:      serverID,
		VirtualItemID: "virtual-item-1",
		SourceID:      "source-1",
		Identity: identitySnapshot{
			Source: "custom",
			Headers: http.Header{
				"User-Agent":            {"TestClient/1.0"},
				"X-Emby-Client":         {"TestClient"},
				"X-Emby-Client-Version": {"1.0.0"},
				"X-Emby-Device-Name":    {"Test Device"},
				"X-Emby-Device-Id":      {"device-1"},
			},
		},
		Principal:      "upstream-principal-a",
		ConfigRevision: "config-revision-1",
	}
}

func playbackIdentityTestExpectError(t *testing.T, err error, want error) {
	t.Helper()
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestPlaybackIdentityStoreLifecycleAndLease(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	now := start
	store := newPlaybackIdentityStoreWithClock(func() time.Time { return now })
	owner := playbackIdentityTestOwner("user-a", "local-token-a")
	request := playbackIdentityTestRequest(owner)

	reserved, err := store.reserve(request)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if reserved.State != playbackIdentityPending {
		t.Fatalf("state = %v, want pending", reserved.State)
	}
	if reserved.AttemptID == "" {
		t.Fatal("empty attempt id")
	}
	if _, err := hex.DecodeString(reserved.AttemptID); err != nil {
		t.Fatalf("attempt id is not opaque hex: %v", err)
	}
	if strings.Contains(reserved.Owner.SessionDigest, "local-token-a") || reserved.Owner.SessionDigest == "local-token-a" {
		t.Fatal("raw local session token leaked into the binding")
	}
	if !strings.HasPrefix(reserved.Owner.SessionDigest, playbackIdentitySessionDigestVersion+":") {
		t.Fatalf("session digest = %q, want versioned digest", reserved.Owner.SessionDigest)
	}
	if reserved.Auth.IdentityFingerprint != playbackIdentityFingerprint(request.Identity) {
		t.Fatal("identity fingerprint does not match the selected snapshot")
	}
	if reserved.Auth.Principal != request.Principal || reserved.Auth.ConfigRevision != request.ConfigRevision {
		t.Fatal("auth metadata was not recorded")
	}
	if !reserved.CreatedAt.Equal(start) || !reserved.LastUsedAt.Equal(start) || !reserved.StateChangedAt.Equal(start) {
		t.Fatal("pending binding timestamps are not the reservation time")
	}

	now = start.Add(10 * time.Second)
	active, err := store.activate(reserved.AttemptID, owner, "upstream-session-1")
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if active.State != playbackIdentityActive {
		t.Fatalf("state = %v, want active", active.State)
	}
	if active.UpstreamPlaySessionID != "upstream-session-1" {
		t.Fatalf("upstream alias = %q", active.UpstreamPlaySessionID)
	}
	if !active.CreatedAt.Equal(start) || !active.StateChangedAt.Equal(now) || !active.LastUsedAt.Equal(now) {
		t.Fatal("activation timestamps are wrong")
	}

	now = start.Add(20 * time.Second)
	repeated, err := store.activate(reserved.AttemptID, owner, "upstream-session-1")
	if err != nil {
		t.Fatalf("repeat activate: %v", err)
	}
	if !repeated.StateChangedAt.Equal(active.StateChangedAt) || !repeated.LastUsedAt.Equal(active.LastUsedAt) {
		t.Fatal("idempotent activation moved the binding timestamps")
	}

	now = start.Add(20 * time.Minute)
	lease, err := store.lease(reserved.AttemptID, owner)
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	if lease.Binding().AttemptID != reserved.AttemptID || lease.Binding().LeaseCount != 1 {
		t.Fatalf("lease snapshot = %+v", lease.Binding())
	}

	now = start.Add(40 * time.Minute)
	if _, err := store.touch(reserved.AttemptID, owner); err != nil {
		t.Fatalf("touch: %v", err)
	}
	now = start.Add(69 * time.Minute)
	if _, err := store.resolve(reserved.AttemptID, owner); err != nil {
		t.Fatalf("resolve after touch: %v", err)
	}
	if !lease.Release() {
		t.Fatal("first release reported false")
	}
	if lease.Release() {
		t.Fatal("second release reported true")
	}

	now = start.Add(69 * time.Minute)
	if err := store.stop(reserved.AttemptID, owner); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := store.stop(reserved.AttemptID, owner); err != nil {
		t.Fatalf("idempotent stop: %v", err)
	}
	_, err = store.resolve(reserved.AttemptID, owner)
	playbackIdentityTestExpectError(t, err, ErrPlaybackIdentityStopping)
	_, err = store.lease(reserved.AttemptID, owner)
	playbackIdentityTestExpectError(t, err, ErrPlaybackIdentityStopping)
	_, err = store.touch(reserved.AttemptID, owner)
	playbackIdentityTestExpectError(t, err, ErrPlaybackIdentityStopping)
	_, err = store.activate(reserved.AttemptID, owner, "")
	playbackIdentityTestExpectError(t, err, ErrPlaybackIdentityStopping)
	_, err = store.bindAlias(reserved.AttemptID, owner, "local-session-1")
	playbackIdentityTestExpectError(t, err, ErrPlaybackIdentityStopping)
	playbackIdentityTestExpectError(t, store.validateAuth(reserved.AttemptID, owner, active.Auth), ErrPlaybackIdentityStopping)

	if err := store.retire(reserved.AttemptID, owner); err != nil {
		t.Fatalf("retire: %v", err)
	}
	if err := store.retire(reserved.AttemptID, owner); err != nil {
		t.Fatalf("idempotent retire: %v", err)
	}
	_, err = store.resolve(reserved.AttemptID, owner)
	playbackIdentityTestExpectError(t, err, ErrPlaybackIdentityTerminal)
	if err := store.stop(reserved.AttemptID, owner); err != nil {
		t.Fatalf("stop after retire: %v", err)
	}
	if err := store.fail(reserved.AttemptID, owner); err != nil {
		t.Fatalf("fail after retire: %v", err)
	}
	_, err = store.lease(reserved.AttemptID, owner)
	playbackIdentityTestExpectError(t, err, ErrPlaybackIdentityTerminal)

	terminalAt := now
	now = terminalAt.Add(playbackIdentityTerminalTTL - time.Second)
	if got := store.count(); got != 1 {
		t.Fatalf("terminal binding dropped before its retention window: count = %d", got)
	}
	now = terminalAt.Add(playbackIdentityTerminalTTL)
	if got := store.count(); got != 0 {
		t.Fatalf("terminal binding survived its retention window: count = %d", got)
	}
}

func TestPlaybackIdentityStoreOwnerAndSnapshotIsolation(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	store := newPlaybackIdentityStoreWithClock(func() time.Time { return now })
	owner := playbackIdentityTestOwner("user-a", "local-token-a")
	request := playbackIdentityTestRequest(owner)
	request.Identity.Headers.Set("Authorization", "Bearer local-secret")
	request.Identity.Headers.Set("X-Emby-Authorization", `Emby Token="local-secret"`)
	request.Identity.Headers.Set("X-Emby-Token", "local-secret")
	request.Identity.Headers.Set("Cookie", "session=local-secret")
	expectedFingerprint := playbackIdentityFingerprint(request.Identity)

	reserved, err := store.reserve(request)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	request.Identity.Headers.Set("User-Agent", "Mutated/2.0")
	resolved, err := store.resolve(reserved.AttemptID, owner)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := resolved.Identity.Headers.Get("User-Agent"); got != "TestClient/1.0" {
		t.Fatalf("caller mutation changed the stored identity: %q", got)
	}
	if got := resolved.Identity.Headers.Get("X-Emby-Client"); got != "TestClient" {
		t.Fatalf("managed identity header was not stored: %q", got)
	}
	for _, credentialHeader := range []string{"Authorization", "X-Emby-Authorization", "X-Emby-Token", "Cookie"} {
		if got := resolved.Identity.Headers.Get(credentialHeader); got != "" {
			t.Fatalf("credential header %s persisted in the snapshot: %q", credentialHeader, got)
		}
	}

	resolved.Identity.Headers.Set("User-Agent", "Leaked/3.0")
	again, err := store.resolve(reserved.AttemptID, owner)
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if got := again.Identity.Headers.Get("User-Agent"); got != "TestClient/1.0" {
		t.Fatalf("snapshot mutation changed the stored identity: %q", got)
	}
	if again.Auth.IdentityFingerprint != expectedFingerprint {
		t.Fatal("stored identity fingerprint changed")
	}
	if strings.Contains(fmt.Sprintf("%+v", again), "local-token-a") {
		t.Fatal("raw local session token is reachable through the snapshot")
	}

	wrongUser := playbackIdentityTestOwner("user-b", "local-token-a")
	_, err = store.resolve(reserved.AttemptID, wrongUser)
	playbackIdentityTestExpectError(t, err, ErrPlaybackIdentityOwnerMismatch)
	wrongSession := playbackIdentityTestOwner("user-a", "local-token-b")
	_, err = store.resolve(reserved.AttemptID, wrongSession)
	playbackIdentityTestExpectError(t, err, ErrPlaybackIdentityOwnerMismatch)

	if _, err := store.bindAlias(reserved.AttemptID, owner, "session-1"); err != nil {
		t.Fatalf("bind alias: %v", err)
	}
	_, err = store.resolveAlias(reserved.AttemptID, "server-a", "session-1", wrongUser)
	playbackIdentityTestExpectError(t, err, ErrPlaybackIdentityNotFound)

	unauthenticated := playbackIdentityTestRequest(playbackIdentityTestOwner("user-a", ""))
	_, err = store.reserve(unauthenticated)
	playbackIdentityTestExpectError(t, err, ErrPlaybackIdentityInvalid)
	noSource := playbackIdentityTestRequest(owner)
	noSource.Identity.Source = ""
	_, err = store.reserve(noSource)
	playbackIdentityTestExpectError(t, err, ErrPlaybackIdentityInvalid)
}

func TestPlaybackIdentityStoreExpiryWindows(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	now := start
	store := newPlaybackIdentityStoreWithClock(func() time.Time { return now })
	owner := playbackIdentityTestOwner("user-a", "local-token-a")

	pending, err := store.reserve(playbackIdentityTestRequest(owner))
	if err != nil {
		t.Fatalf("reserve pending: %v", err)
	}
	now = start.Add(playbackIdentityPendingTTL - time.Second)
	if _, err := store.resolve(pending.AttemptID, owner); err != nil {
		t.Fatalf("pending expired early: %v", err)
	}
	now = start.Add(playbackIdentityPendingTTL)
	_, err = store.resolve(pending.AttemptID, owner)
	playbackIdentityTestExpectError(t, err, ErrPlaybackIdentityNotFound)
	if got := store.count(); got != 0 {
		t.Fatalf("expired pending count = %d, want 0", got)
	}

	now = start
	active, err := store.reserve(playbackIdentityTestRequest(owner))
	if err != nil {
		t.Fatalf("reserve active: %v", err)
	}
	if _, err := store.activate(active.AttemptID, owner, "session-1"); err != nil {
		t.Fatalf("activate: %v", err)
	}
	lease, err := store.lease(active.AttemptID, owner)
	if err != nil {
		t.Fatalf("lease: %v", err)
	}
	now = start.Add(playbackIdentityActiveIdle)
	_, err = store.resolve(active.AttemptID, owner)
	playbackIdentityTestExpectError(t, err, ErrPlaybackIdentityNotFound)
	if got := store.count(); got != 1 {
		t.Fatalf("leased entry was removed before release: count = %d", got)
	}
	if got := lease.Binding().State; got != playbackIdentityActive {
		t.Fatalf("lease snapshot state = %v, want active", got)
	}
	if !lease.Release() {
		t.Fatal("release reported false")
	}
	if got := store.count(); got != 0 {
		t.Fatalf("released expired entry count = %d, want 0", got)
	}

	now = start
	terminal, err := store.reserve(playbackIdentityTestRequest(owner))
	if err != nil {
		t.Fatalf("reserve terminal: %v", err)
	}
	if _, err := store.activate(terminal.AttemptID, owner, "session-2"); err != nil {
		t.Fatalf("activate terminal: %v", err)
	}
	now = start.Add(time.Minute)
	if err := store.stop(terminal.AttemptID, owner); err != nil {
		t.Fatalf("stop: %v", err)
	}
	now = start.Add(2 * time.Minute)
	if err := store.stop(terminal.AttemptID, owner); err != nil {
		t.Fatalf("idempotent stop: %v", err)
	}
	now = start.Add(time.Minute + playbackIdentityTerminalTTL - time.Second)
	if got := store.count(); got != 1 {
		t.Fatalf("terminal retention ended early: count = %d", got)
	}
	now = start.Add(time.Minute + playbackIdentityTerminalTTL)
	if got := store.count(); got != 0 {
		t.Fatalf("repeated stop extended terminal retention: count = %d", got)
	}
}

func TestPlaybackIdentityStoreCapacity(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	now := start
	owner := playbackIdentityTestOwner("user-a", "local-token-a")
	otherOwner := playbackIdentityTestOwner("user-b", "local-token-b")

	store := newPlaybackIdentityStoreWithClock(func() time.Time { return now })
	store.maxEntries = 1
	store.maxPerUser = playbackIdentityMaxPerUser
	active, err := store.reserve(playbackIdentityTestRequest(owner))
	if err != nil {
		t.Fatalf("reserve active: %v", err)
	}
	if _, err := store.activate(active.AttemptID, owner, "session-1"); err != nil {
		t.Fatalf("activate: %v", err)
	}
	_, err = store.reserve(playbackIdentityTestRequest(owner))
	playbackIdentityTestExpectError(t, err, ErrPlaybackIdentityCapacity)
	if _, err := store.resolve(active.AttemptID, owner); err != nil {
		t.Fatalf("capacity pressure evicted the active binding: %v", err)
	}
	if err := store.fail(active.AttemptID, owner); err != nil {
		t.Fatalf("fail: %v", err)
	}
	_, err = store.reserve(playbackIdentityTestRequest(owner))
	playbackIdentityTestExpectError(t, err, ErrPlaybackIdentityCapacity)
	now = start.Add(playbackIdentityTerminalTTL)
	if _, err := store.reserve(playbackIdentityTestRequest(owner)); err != nil {
		t.Fatalf("terminal retention did not release capacity: %v", err)
	}

	now = start
	pendingStore := newPlaybackIdentityStoreWithClock(func() time.Time { return now })
	pendingStore.maxEntries = 1
	pendingStore.maxPerUser = playbackIdentityMaxPerUser
	if _, err := pendingStore.reserve(playbackIdentityTestRequest(owner)); err != nil {
		t.Fatalf("reserve pending: %v", err)
	}
	now = start.Add(playbackIdentityPendingTTL)
	if _, err := pendingStore.reserve(playbackIdentityTestRequest(owner)); err != nil {
		t.Fatalf("expired pending did not release capacity: %v", err)
	}

	now = start
	userStore := newPlaybackIdentityStoreWithClock(func() time.Time { return now })
	userStore.maxPerUser = 1
	if _, err := userStore.reserve(playbackIdentityTestRequest(owner)); err != nil {
		t.Fatalf("reserve user a: %v", err)
	}
	if _, err := userStore.reserve(playbackIdentityTestRequest(otherOwner)); err != nil {
		t.Fatalf("per-user limit blocked another user: %v", err)
	}
	_, err = userStore.reserve(playbackIdentityTestRequest(owner))
	playbackIdentityTestExpectError(t, err, ErrPlaybackIdentityCapacity)
	_, err = userStore.reserve(playbackIdentityTestRequest(otherOwner))
	playbackIdentityTestExpectError(t, err, ErrPlaybackIdentityCapacity)
}

func TestPlaybackIdentityStoreAliasesAreScopedAndTransferable(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	store := newPlaybackIdentityStoreWithClock(func() time.Time { return now })
	owner := playbackIdentityTestOwner("user-a", "local-token-a")
	otherOwner := playbackIdentityTestOwner("user-b", "local-token-b")

	onA, err := store.reserve(playbackIdentityTestRequestOn(owner, "server-a"))
	if err != nil {
		t.Fatalf("reserve server-a: %v", err)
	}
	onB, err := store.reserve(playbackIdentityTestRequestOn(otherOwner, "server-b"))
	if err != nil {
		t.Fatalf("reserve server-b: %v", err)
	}
	if _, err := store.activate(onA.AttemptID, owner, "shared-session"); err != nil {
		t.Fatalf("activate server-a: %v", err)
	}
	if _, err := store.activate(onB.AttemptID, otherOwner, "shared-session"); err != nil {
		t.Fatalf("activate server-b: %v", err)
	}

	resolvedA, err := store.resolveAlias(onA.AttemptID, "server-a", "shared-session", owner)
	if err != nil || resolvedA.AttemptID != onA.AttemptID {
		t.Fatalf("server-a alias = %+v, %v", resolvedA, err)
	}
	resolvedB, err := store.resolveAlias(onB.AttemptID, "server-b", "shared-session", otherOwner)
	if err != nil || resolvedB.AttemptID != onB.AttemptID {
		t.Fatalf("server-b alias = %+v, %v", resolvedB, err)
	}
	_, err = store.resolveAlias(onA.AttemptID, "server-c", "shared-session", owner)
	playbackIdentityTestExpectError(t, err, ErrPlaybackIdentityNotFound)
	_, err = store.resolveAlias(onA.AttemptID, "server-a", "shared-session", otherOwner)
	playbackIdentityTestExpectError(t, err, ErrPlaybackIdentityNotFound)

	contender, err := store.reserve(playbackIdentityTestRequestOn(owner, "server-a"))
	if err != nil {
		t.Fatalf("reserve contender: %v", err)
	}
	_, err = store.bindAlias(contender.AttemptID, owner, "shared-session")
	if err != nil {
		t.Fatalf("reused session must remain attempt-scoped: %v", err)
	}
	_, err = store.bindAlias(onA.AttemptID, owner, "another-session")
	playbackIdentityTestExpectError(t, err, ErrPlaybackIdentityAliasConflict)
	if _, err := store.bindAlias(onA.AttemptID, owner, "shared-session"); err != nil {
		t.Fatalf("idempotent alias bind: %v", err)
	}

	if err := store.stop(onA.AttemptID, owner); err != nil {
		t.Fatalf("stop onA: %v", err)
	}
	if err := store.retire(onA.AttemptID, owner); err != nil {
		t.Fatalf("retire onA: %v", err)
	}
	if _, err := store.bindAlias(contender.AttemptID, owner, "shared-session"); err != nil {
		t.Fatalf("terminal binding kept its alias: %v", err)
	}
	if _, err := store.activate(contender.AttemptID, owner, ""); err != nil {
		t.Fatalf("activate contender: %v", err)
	}

	now = now.Add(playbackIdentityTerminalTTL)
	if removed := store.cleanup(); removed != 1 {
		t.Fatalf("cleanup removed %d entries, want 1", removed)
	}
	resolvedA, err = store.resolveAlias(contender.AttemptID, "server-a", "shared-session", owner)
	if err != nil || resolvedA.AttemptID != contender.AttemptID {
		t.Fatalf("alias takeover was lost when the terminal binding was pruned: %+v, %v", resolvedA, err)
	}
}

func TestPlaybackIdentityStoreAuthCompatibility(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	store := newPlaybackIdentityStoreWithClock(func() time.Time { return now })
	owner := playbackIdentityTestOwner("user-a", "local-token-a")
	request := playbackIdentityTestRequest(owner)
	transportOnly := playbackIdentityTestRequest(owner)
	transportOnly.Identity.Headers.Set("Range", "bytes=0-1023")
	transportOnly.Identity.Headers.Set("Authorization", "Bearer should-not-be-fingerprinted")
	if playbackIdentityFingerprint(transportOnly.Identity) != playbackIdentityFingerprint(request.Identity) {
		t.Fatal("transport or credential headers changed the identity fingerprint")
	}
	changedIdentity := playbackIdentityTestRequest(owner)
	changedIdentity.Identity.Headers.Set("User-Agent", "OtherClient/9.9")

	binding, err := store.reserve(request)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if err := store.validateAuth(binding.AttemptID, owner, binding.Auth); err != nil {
		t.Fatalf("compatible auth rejected: %v", err)
	}
	if !binding.compatibleAuth(binding.Auth) {
		t.Fatal("snapshot rejected its own auth metadata")
	}
	if (playbackIdentityAuth{}).compatible(playbackIdentityAuth{}) {
		t.Fatal("two unknown auth records were treated as compatible")
	}
	if binding.compatibleAuth(playbackIdentityAuth{}) {
		t.Fatal("unknown current auth metadata was accepted")
	}
	incomplete := binding.Auth
	incomplete.IdentityFingerprint = ""
	if err := store.validateAuth(binding.AttemptID, owner, incomplete); !errors.Is(err, ErrPlaybackIdentityAuthIncompatible) {
		t.Fatalf("incomplete auth metadata was accepted: %v", err)
	}
	if !errors.Is(store.validateAuth(binding.AttemptID, owner, changedIdentityAuth(changedIdentity)), ErrPlaybackIdentityAuthIncompatible) {
		t.Fatal("identity change was accepted")
	}

	for _, mismatch := range []playbackIdentityAuth{
		{Principal: "other-principal", IdentityFingerprint: binding.Auth.IdentityFingerprint, ConfigRevision: binding.Auth.ConfigRevision},
		{Principal: binding.Auth.Principal, IdentityFingerprint: "v1:deadbeef", ConfigRevision: binding.Auth.ConfigRevision},
		{Principal: binding.Auth.Principal, IdentityFingerprint: binding.Auth.IdentityFingerprint, ConfigRevision: "config-revision-2"},
	} {
		if err := store.validateAuth(binding.AttemptID, owner, mismatch); !errors.Is(err, ErrPlaybackIdentityAuthIncompatible) {
			t.Fatalf("auth mismatch accepted: %v", err)
		}
	}

	if err := store.stop(binding.AttemptID, owner); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := store.validateAuth(binding.AttemptID, owner, binding.Auth); !errors.Is(err, ErrPlaybackIdentityStopping) {
		t.Fatalf("auth validation after stop = %v, want stopping", err)
	}
}

func TestPlaybackIdentityStoreStrictIdentityAndAuthMetadata(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	store := newPlaybackIdentityStoreWithClock(func() time.Time { return now })
	owner := playbackIdentityTestOwner("user-a", "local-token-a")

	invalid := []struct {
		name   string
		mutate func(*playbackAttemptRequest)
	}{
		{"missing user agent", func(r *playbackAttemptRequest) { r.Identity.Headers.Del("User-Agent") }},
		{"blank user agent", func(r *playbackAttemptRequest) { r.Identity.Headers.Set("User-Agent", "   ") }},
		{"control user agent", func(r *playbackAttemptRequest) { r.Identity.Headers.Set("User-Agent", "Bad\r\nAgent") }},
		{"missing source", func(r *playbackAttemptRequest) { r.Identity.Source = "" }},
		{"missing principal", func(r *playbackAttemptRequest) { r.Principal = "" }},
		{"missing config revision", func(r *playbackAttemptRequest) { r.ConfigRevision = "" }},
	}
	for _, tc := range invalid {
		request := playbackIdentityTestRequest(owner)
		tc.mutate(&request)
		if _, err := store.reserve(request); !errors.Is(err, ErrPlaybackIdentityInvalid) {
			t.Fatalf("%s: error = %v, want invalid", tc.name, err)
		}
	}
	if got := store.count(); got != 0 {
		t.Fatalf("invalid reservations created %d entries", got)
	}

	binding, err := store.reserve(playbackIdentityTestRequest(owner))
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if !binding.Auth.complete() {
		t.Fatalf("reserve accepted incomplete auth metadata: %+v", binding.Auth)
	}
	active, err := store.activate(binding.AttemptID, owner, "session-1")
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if active.Identity.Source != "custom" || active.Identity.Headers.Get("User-Agent") != "TestClient/1.0" {
		t.Fatalf("activation changed the selected identity: %+v", active.Identity)
	}
	if active.Auth.IdentityFingerprint != binding.Auth.IdentityFingerprint {
		t.Fatal("activation changed the selected identity fingerprint")
	}
}

func changedIdentityAuth(request playbackAttemptRequest) playbackIdentityAuth {
	return playbackIdentityAuth{
		Principal:           request.Principal,
		IdentityFingerprint: playbackIdentityFingerprint(request.Identity),
		ConfigRevision:      request.ConfigRevision,
	}
}
