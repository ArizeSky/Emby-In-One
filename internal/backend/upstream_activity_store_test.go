package backend

import (
	"errors"
	"path/filepath"
	"testing"
)

func newUpstreamActivityTestStore(t *testing.T) (*UpstreamActivityStore, *sqliteDB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "upstream-activity.db")
	db, err := openSQLite(path)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	t.Cleanup(func() { _ = closeSQLite(db) })
	store, err := NewUpstreamActivityStore(db)
	if err != nil {
		t.Fatalf("NewUpstreamActivityStore: %v", err)
	}
	if store == nil {
		t.Fatal("NewUpstreamActivityStore returned nil for a live DB")
	}
	return store, db, path
}

func upstreamActivityTestRevisionRange(t *testing.T, revision int64) {
	t.Helper()
	if revision <= 0 || revision > UpstreamActivityMaxRevision {
		t.Fatalf("revision %d outside 1..%d (JS-safe bound)", revision, UpstreamActivityMaxRevision)
	}
}

func TestUpstreamActivityStoreConstructorIdempotentAndNilSafe(t *testing.T) {
	store, db, _ := newUpstreamActivityTestStore(t)
	if !store.Available() {
		t.Fatal("live store must be available")
	}

	// A second constructor on the same shared connection only re-runs the
	// idempotent CREATE IF NOT EXISTS schema.
	second, err := NewUpstreamActivityStore(db)
	if err != nil {
		t.Fatalf("second NewUpstreamActivityStore: %v", err)
	}
	if second == nil {
		t.Fatal("second store is nil")
	}

	// Existing unrelated tables survive schema initialization.
	if err := db.withWriteTx(func() error {
		if err := db.exec(`CREATE TABLE IF NOT EXISTS upstream_activity_test_unrelated (value TEXT)`); err != nil {
			return err
		}
		return db.execParams(`INSERT INTO upstream_activity_test_unrelated (value) VALUES (?)`, "kept")
	}); err != nil {
		t.Fatalf("prepare unrelated table: %v", err)
	}
	third, err := NewUpstreamActivityStore(db)
	if err != nil || third == nil {
		t.Fatalf("third NewUpstreamActivityStore: %v", err)
	}
	if err := db.withReadLock(func() error {
		stmt, err := db.prepare(`SELECT value FROM upstream_activity_test_unrelated`)
		if err != nil {
			return err
		}
		defer stmt.finalize()
		hasRow, err := stmt.step()
		if err != nil || !hasRow {
			return errors.New("unrelated row disappeared")
		}
		if got := stmt.columnText(0); got != "kept" {
			return errors.New("unrelated row changed")
		}
		return nil
	}); err != nil {
		t.Fatalf("unrelated table check: %v", err)
	}

	nilStore, err := NewUpstreamActivityStore(nil)
	if err != nil || nilStore != nil {
		t.Fatalf("nil DB must yield (nil, nil): %v, %v", nilStore, err)
	}

	var missing *UpstreamActivityStore
	if missing.Available() {
		t.Fatal("nil store must not be available")
	}
	if _, _, err := missing.Read("server", "scope"); !errors.Is(err, ErrUpstreamActivityUnavailable) {
		t.Fatalf("nil Read error = %v", err)
	}
	if _, err := missing.GetPolicy("server"); !errors.Is(err, ErrUpstreamActivityUnavailable) {
		t.Fatalf("nil GetPolicy error = %v", err)
	}
	if _, err := missing.EnsureScope("server", "scope", 1); !errors.Is(err, ErrUpstreamActivityUnavailable) {
		t.Fatalf("nil EnsureScope error = %v", err)
	}
	if _, err := missing.Record(UpstreamActivityRecord{ServerID: "server", AccountScope: "scope"}, 1); !errors.Is(err, ErrUpstreamActivityUnavailable) {
		t.Fatalf("nil Record error = %v", err)
	}
	if _, err := missing.SavePolicy("server", "scope", DefaultUpstreamActivityPolicy(), 0, 1); !errors.Is(err, ErrUpstreamActivityUnavailable) {
		t.Fatalf("nil SavePolicy error = %v", err)
	}
	if err := missing.Delete("server"); !errors.Is(err, ErrUpstreamActivityUnavailable) {
		t.Fatalf("nil Delete error = %v", err)
	}
	if _, err := missing.Snapshot(map[string]string{"server": "scope"}); !errors.Is(err, ErrUpstreamActivityUnavailable) {
		t.Fatalf("nil Snapshot error = %v", err)
	}
	if _, err := missing.SnapshotPolicies([]string{"server"}); !errors.Is(err, ErrUpstreamActivityUnavailable) {
		t.Fatalf("nil SnapshotPolicies error = %v", err)
	}
}

func TestUpstreamActivityStorePersistenceAcrossReopen(t *testing.T) {
	store, db, path := newUpstreamActivityTestStore(t)
	ensure := int64(1_700_000_000_000)
	activity := ensure + 1000
	qualified := ensure + 2000
	scope := "v1:persisted-scope"

	if _, err := store.EnsureScope("server-1", scope, ensure); err != nil {
		t.Fatalf("EnsureScope: %v", err)
	}
	if _, err := store.Record(UpstreamActivityRecord{
		ServerID: "server-1", AccountScope: scope,
		LastActivityAt: &activity, LastQualifiedAt: &qualified,
	}, activity); err != nil {
		t.Fatalf("Record: %v", err)
	}
	policy := upstreamActivityTestPolicy()
	policy.ManualReferenceAt = &qualified
	initial, err := store.GetPolicy("server-1")
	if err != nil {
		t.Fatalf("initial policy: %v", err)
	}
	saved, err := store.SavePolicy("server-1", scope, policy, initial.Revision, qualified)
	if err != nil {
		t.Fatalf("SavePolicy: %v", err)
	}
	upstreamActivityTestRevisionRange(t, saved.Revision)

	if err := closeSQLite(db); err != nil {
		t.Fatalf("close: %v", err)
	}
	reopenedDB, err := openSQLite(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = closeSQLite(reopenedDB) })
	reopened, err := NewUpstreamActivityStore(reopenedDB)
	if err != nil || reopened == nil {
		t.Fatalf("reopen store: %v", err)
	}

	summary, found, err := reopened.Read("server-1", scope)
	if err != nil || !found {
		t.Fatalf("Read after reopen = %v/%v: %v", found, summary, err)
	}
	if summary.LastActivityAt == nil || *summary.LastActivityAt != activity {
		t.Fatalf("lastActivityAt not persisted: %+v", summary)
	}
	if summary.LastQualifiedAt == nil || *summary.LastQualifiedAt != qualified {
		t.Fatalf("lastQualifiedAt not persisted: %+v", summary)
	}
	stored, err := reopened.GetPolicy("server-1")
	if err != nil {
		t.Fatalf("GetPolicy after reopen: %v", err)
	}
	if !stored.Enabled || stored.Revision != saved.Revision || stored.ManualReferenceAt == nil || *stored.ManualReferenceAt != qualified {
		t.Fatalf("policy not persisted: %+v", stored)
	}
	if !SameUpstreamAccountScope(stored.ManualScope, scope) {
		t.Fatal("manual reference scope not persisted")
	}
}

func TestUpstreamActivityStoreScopeIsolationAndMaxTimestamps(t *testing.T) {
	store, _, _ := newUpstreamActivityTestStore(t)
	now := int64(1_700_000_000_000)
	scopeA := "v1:scope-a"
	scopeB := "v1:scope-b"
	activity := now + 1000
	qualified := now + 2000

	if _, err := store.EnsureScope("server-a", scopeA, now); err != nil {
		t.Fatalf("ensure A: %v", err)
	}
	if _, err := store.EnsureScope("server-b", scopeB, now); err != nil {
		t.Fatalf("ensure B: %v", err)
	}

	// A late event for B's account asks to write A: rejected, nothing changes.
	if _, err := store.Record(UpstreamActivityRecord{
		ServerID: "server-a", AccountScope: scopeB, LastActivityAt: &activity,
	}, activity); !errors.Is(err, ErrUpstreamActivityScopeMismatch) {
		t.Fatalf("cross-account record error = %v", err)
	}
	summary, found, err := store.Read("server-a", scopeA)
	if err != nil || !found || summary.LastActivityAt != nil || summary.LastQualifiedAt != nil {
		t.Fatalf("rejected write polluted server A: %+v found=%v err=%v", summary, found, err)
	}

	// A record for a server that never ensured its scope is rejected too.
	if _, err := store.Record(UpstreamActivityRecord{
		ServerID: "server-unknown", AccountScope: scopeA, LastActivityAt: &activity,
	}, activity); !errors.Is(err, ErrUpstreamActivityScopeMismatch) {
		t.Fatalf("unknown server record error = %v", err)
	}

	if _, err := store.Record(UpstreamActivityRecord{
		ServerID: "server-a", AccountScope: scopeA,
		LastActivityAt: &activity, LastQualifiedAt: &qualified,
	}, activity); err != nil {
		t.Fatalf("record A: %v", err)
	}
	// Older/replayed timestamps never move the summary backwards.
	olderActivity := now - 10
	olderQualified := now - 20
	stale, err := store.Record(UpstreamActivityRecord{
		ServerID: "server-a", AccountScope: scopeA,
		LastActivityAt: &olderActivity, LastQualifiedAt: &olderQualified,
	}, now-5)
	if err != nil {
		t.Fatalf("stale record: %v", err)
	}
	if stale.LastActivityAt == nil || *stale.LastActivityAt != activity {
		t.Fatalf("lastActivityAt went backwards: %+v", stale)
	}
	if stale.LastQualifiedAt == nil || *stale.LastQualifiedAt != qualified {
		t.Fatalf("lastQualifiedAt went backwards: %+v", stale)
	}
	if stale.UpdatedAt != activity {
		t.Fatalf("updatedAt went backwards: %d", stale.UpdatedAt)
	}

	// Reads and snapshots are scope-bound: an old scope sees no data.
	if _, found, err := store.Read("server-a", scopeB); err != nil || found {
		t.Fatalf("old-scope read = found=%v err=%v", found, err)
	}
	snapshot, err := store.Snapshot(map[string]string{
		"server-a": scopeA,
		"server-b": scopeA, // wrong expected scope must hide B
	})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if _, ok := snapshot["server-a"]; !ok {
		t.Fatal("snapshot missing server A")
	}
	if _, ok := snapshot["server-b"]; ok {
		t.Fatal("snapshot exposed server B under the wrong scope")
	}
}

func TestUpstreamActivityStoreScopeChangeClearsOldAccount(t *testing.T) {
	store, _, _ := newUpstreamActivityTestStore(t)
	now := int64(1_700_000_000_000)
	scopeA := "v1:scope-a"
	scopeB := "v1:scope-b"
	activity := now + 1000
	manual := now - 1000

	if _, err := store.EnsureScope("server-1", scopeA, now); err != nil {
		t.Fatalf("ensure A: %v", err)
	}
	if _, err := store.Record(UpstreamActivityRecord{
		ServerID: "server-1", AccountScope: scopeA, LastActivityAt: &activity, LastQualifiedAt: &activity,
	}, activity); err != nil {
		t.Fatalf("record A: %v", err)
	}
	policy := upstreamActivityTestPolicy()
	policy.IntervalUnit = UpstreamActivityUnitMonths
	policy.IntervalValue = 6
	policy.ManualReferenceAt = &manual
	initialA, err := store.GetPolicy("server-1")
	if err != nil {
		t.Fatalf("initial policy A: %v", err)
	}
	savedA, err := store.SavePolicy("server-1", scopeA, policy, initialA.Revision, now)
	if err != nil {
		t.Fatalf("save policy A: %v", err)
	}
	upstreamActivityTestRevisionRange(t, savedA.Revision)

	// Account range changes: observations and the manual reference must not
	// survive, but the reminder cycle itself does.
	later := now + 5000
	if _, err := store.EnsureScope("server-1", scopeB, later); err != nil {
		t.Fatalf("ensure B: %v", err)
	}
	summary, found, err := store.Read("server-1", scopeB)
	if err != nil || !found {
		t.Fatalf("read B: %v/%v", found, err)
	}
	if summary.LastActivityAt != nil || summary.LastQualifiedAt != nil {
		t.Fatalf("old account observations leaked into new scope: %+v", summary)
	}
	if summary.UpdatedAt != later {
		t.Fatalf("new summary updatedAt = %d, want %d", summary.UpdatedAt, later)
	}
	if _, found, _ := store.Read("server-1", scopeA); found {
		t.Fatal("old scope still readable after account change")
	}

	policyB, err := store.GetPolicy("server-1")
	if err != nil {
		t.Fatalf("get policy B: %v", err)
	}
	if !policyB.Enabled || policyB.IntervalUnit != UpstreamActivityUnitMonths || policyB.IntervalValue != 6 {
		t.Fatalf("cycle not retained across account change: %+v", policyB)
	}
	if policyB.ManualReferenceAt != nil || policyB.ManualScope != "" {
		t.Fatalf("manual reference survived account change: %+v", policyB)
	}
	if policyB.Revision == savedA.Revision {
		t.Fatal("scope change must regenerate the revision even without a manual reference")
	}
	upstreamActivityTestRevisionRange(t, policyB.Revision)

	// Old-account late writes and stale forms are rejected, new ones work.
	if _, err := store.Record(UpstreamActivityRecord{
		ServerID: "server-1", AccountScope: scopeA, LastActivityAt: &activity,
	}, later); !errors.Is(err, ErrUpstreamActivityScopeMismatch) {
		t.Fatalf("old-account late write error = %v", err)
	}
	newPolicy := policy
	newPolicy.ManualReferenceAt = nil
	if _, err := store.SavePolicy("server-1", scopeB, newPolicy, savedA.Revision, later); !errors.Is(err, ErrUpstreamActivityRevisionConflict) {
		t.Fatalf("stale-scope form error = %v", err)
	}
	if _, err := store.SavePolicy("server-1", scopeA, newPolicy, policyB.Revision, later); !errors.Is(err, ErrUpstreamActivityScopeMismatch) {
		t.Fatalf("old-scope form error = %v", err)
	}
	if _, err := store.Record(UpstreamActivityRecord{
		ServerID: "server-1", AccountScope: scopeB, LastActivityAt: &activity,
	}, later); err != nil {
		t.Fatalf("new-scope record: %v", err)
	}
}

func TestUpstreamActivityStoreEnsureSameScopeKeepsEvidence(t *testing.T) {
	store, _, _ := newUpstreamActivityTestStore(t)
	now := int64(1_700_000_000_000)
	scope := "v1:stable-scope"
	activity := now + 1000

	if _, err := store.EnsureScope("server-1", scope, now); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	initial, err := store.GetPolicy("server-1")
	if err != nil {
		t.Fatalf("initial policy: %v", err)
	}
	upstreamActivityTestRevisionRange(t, initial.Revision)
	if initial.Enabled {
		t.Fatal("initialized policy must keep reminders disabled")
	}
	if _, err := store.Record(UpstreamActivityRecord{
		ServerID: "server-1", AccountScope: scope, LastActivityAt: &activity,
	}, activity); err != nil {
		t.Fatalf("record: %v", err)
	}

	// Restart / config reload re-runs EnsureScope for the same account.
	summary, err := store.EnsureScope("server-1", scope, now+10000)
	if err != nil {
		t.Fatalf("re-ensure: %v", err)
	}
	if summary.LastActivityAt == nil || *summary.LastActivityAt != activity {
		t.Fatalf("same-scope ensure discarded evidence: %+v", summary)
	}
	after, err := store.GetPolicy("server-1")
	if err != nil {
		t.Fatalf("policy after re-ensure: %v", err)
	}
	if after.Revision != initial.Revision {
		t.Fatalf("same-scope ensure changed revision %d -> %d", initial.Revision, after.Revision)
	}

	// Two servers initialized with the same scope get independent opaque
	// revisions (not derived from the scope string).
	if _, err := store.EnsureScope("server-2", scope, now); err != nil {
		t.Fatalf("ensure server-2: %v", err)
	}
	second, err := store.GetPolicy("server-2")
	if err != nil {
		t.Fatalf("policy server-2: %v", err)
	}
	upstreamActivityTestRevisionRange(t, second.Revision)
	if second.Revision == initial.Revision {
		t.Fatal("revisions look derived from the shared scope instead of random")
	}
}

func TestUpstreamActivityStorePolicyCASAndValidation(t *testing.T) {
	store, _, _ := newUpstreamActivityTestStore(t)
	now := int64(1_700_000_000_000)
	scope := "v1:policy-scope"
	manual := now - 1000
	future := now + 1

	if _, err := store.EnsureScope("server-1", scope, now); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	defaults, err := store.GetPolicy("server-1")
	if err != nil {
		t.Fatalf("default policy: %v", err)
	}
	if defaults.Revision == 0 {
		t.Fatal("an ensured server must have an opaque non-zero policy revision")
	}
	upstreamActivityTestRevisionRange(t, defaults.Revision)

	policy := upstreamActivityTestPolicy()
	policy.ManualReferenceAt = &manual
	// Revision 0 means "before scope initialization": it can never overwrite
	// the materialized policy row.
	if _, err := store.SavePolicy("server-1", scope, policy, 0, now); !errors.Is(err, ErrUpstreamActivityRevisionConflict) {
		t.Fatalf("zero-revision save error = %v", err)
	}
	first, err := store.SavePolicy("server-1", scope, policy, defaults.Revision, now)
	if err != nil {
		t.Fatalf("first save: %v", err)
	}
	upstreamActivityTestRevisionRange(t, first.Revision)
	if first.Revision == defaults.Revision {
		t.Fatal("save must change the revision")
	}
	stored, err := store.GetPolicy("server-1")
	if err != nil || stored.Revision != first.Revision || stored.ManualReferenceAt == nil || *stored.ManualReferenceAt != manual {
		t.Fatalf("stored policy = %+v, err=%v", stored, err)
	}
	if !SameUpstreamAccountScope(stored.ManualScope, scope) {
		t.Fatal("manual reference must be bound to the current scope")
	}

	// Stale form is rejected without changing anything.
	if _, err := store.SavePolicy("server-1", scope, policy, defaults.Revision, now); !errors.Is(err, ErrUpstreamActivityRevisionConflict) {
		t.Fatalf("stale save error = %v", err)
	}
	unchanged, err := store.GetPolicy("server-1")
	if err != nil || unchanged.Revision != first.Revision || unchanged.ManualReferenceAt == nil {
		t.Fatalf("conflicting save changed policy: %+v err=%v", unchanged, err)
	}

	// Clearing the manual reference is a normal mutation with a fresh revision.
	cleared := policy
	cleared.ManualReferenceAt = nil
	second, err := store.SavePolicy("server-1", scope, cleared, first.Revision, now)
	if err != nil {
		t.Fatalf("clear manual: %v", err)
	}
	upstreamActivityTestRevisionRange(t, second.Revision)
	if second.Revision == first.Revision {
		t.Fatal("mutation must change the revision")
	}
	if second.ManualReferenceAt != nil || second.ManualScope != "" {
		t.Fatalf("manual reference not cleared: %+v", second)
	}

	// Validation failures leave the revision untouched.
	badFuture := cleared
	badFuture.ManualReferenceAt = &future
	if _, err := store.SavePolicy("server-1", scope, badFuture, second.Revision, now); !errors.Is(err, ErrUpstreamActivityInvalid) {
		t.Fatalf("future manual error = %v", err)
	}
	badInterval := cleared
	badInterval.IntervalValue = 0
	if _, err := store.SavePolicy("server-1", scope, badInterval, second.Revision, now); !errors.Is(err, ErrUpstreamActivityInvalid) {
		t.Fatalf("bad interval error = %v", err)
	}
	badMinWatch := cleared
	badMinWatch.MinWatchSeconds = 1
	if _, err := store.SavePolicy("server-1", scope, badMinWatch, second.Revision, now); !errors.Is(err, ErrUpstreamActivityInvalid) {
		t.Fatalf("bad minWatchSeconds error = %v", err)
	}
	if _, err := store.SavePolicy("server-1", scope, cleared, -1, now); !errors.Is(err, ErrUpstreamActivityInvalid) {
		t.Fatalf("negative revision error = %v", err)
	}
	still, err := store.GetPolicy("server-1")
	if err != nil || still.Revision != second.Revision {
		t.Fatalf("validation failure changed revision: %+v err=%v", still, err)
	}

	// Unknown servers and wrong scopes are scope mismatches, not silent writes.
	if _, err := store.SavePolicy("server-unknown", scope, cleared, 0, now); !errors.Is(err, ErrUpstreamActivityScopeMismatch) {
		t.Fatalf("unknown server save error = %v", err)
	}
	if _, err := store.SavePolicy("server-1", "v1:other-scope", cleared, second.Revision, now); !errors.Is(err, ErrUpstreamActivityScopeMismatch) {
		t.Fatalf("wrong scope save error = %v", err)
	}
}

func TestUpstreamActivityStoreDeleteAndRecreateRejectsABA(t *testing.T) {
	store, _, _ := newUpstreamActivityTestStore(t)
	now := int64(1_700_000_000_000)
	scope := "v1:aba-scope"
	activity := now + 1000

	if _, err := store.EnsureScope("server-1", scope, now); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	oldPolicy, err := store.GetPolicy("server-1")
	if err != nil {
		t.Fatalf("old policy: %v", err)
	}
	upstreamActivityTestRevisionRange(t, oldPolicy.Revision)
	if _, err := store.Record(UpstreamActivityRecord{
		ServerID: "server-1", AccountScope: scope, LastActivityAt: &activity,
	}, activity); err != nil {
		t.Fatalf("record: %v", err)
	}

	if err := store.Delete("server-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, found, err := store.Read("server-1", scope); err != nil || found {
		t.Fatalf("read after delete = found=%v err=%v", found, err)
	}
	deletedPolicy, err := store.GetPolicy("server-1")
	if err != nil {
		t.Fatalf("policy after delete: %v", err)
	}
	if deletedPolicy.Revision != 0 || deletedPolicy.Enabled {
		t.Fatalf("deleted server policy = %+v", deletedPolicy)
	}
	if err := store.Delete("server-1"); err != nil {
		t.Fatalf("second delete: %v", err)
	}

	// Recreating a server with the same ID and scope gets a new opaque
	// revision. A form captured before deletion must not save over it.
	if _, err := store.EnsureScope("server-1", scope, now+5000); err != nil {
		t.Fatalf("re-ensure: %v", err)
	}
	newPolicy, err := store.GetPolicy("server-1")
	if err != nil {
		t.Fatalf("new policy: %v", err)
	}
	upstreamActivityTestRevisionRange(t, newPolicy.Revision)
	if newPolicy.Revision == oldPolicy.Revision {
		t.Fatal("delete/recreate reused the old revision (ABA)")
	}
	if _, err := store.SavePolicy("server-1", scope, upstreamActivityTestPolicy(), oldPolicy.Revision, now+5000); !errors.Is(err, ErrUpstreamActivityRevisionConflict) {
		t.Fatalf("stale post-delete form error = %v", err)
	}
	if _, found, err := store.Read("server-1", scope); err != nil || !found {
		t.Fatalf("recreated summary unavailable: found=%v err=%v", found, err)
	}
	recreated, found, err := store.Read("server-1", scope)
	if err != nil || !found || recreated.LastActivityAt != nil {
		t.Fatalf("deleted evidence returned after recreate: %+v found=%v err=%v", recreated, found, err)
	}
}

func TestUpstreamActivityStoreSnapshotPolicies(t *testing.T) {
	store, _, _ := newUpstreamActivityTestStore(t)
	now := int64(1_700_000_000_000)
	scopeA := "v1:snapshot-a"
	scopeB := "v1:snapshot-b"
	manual := now - 1000

	if _, err := store.EnsureScope("server-a", scopeA, now); err != nil {
		t.Fatalf("ensure A: %v", err)
	}
	if _, err := store.EnsureScope("server-b", scopeB, now); err != nil {
		t.Fatalf("ensure B: %v", err)
	}
	policyB := upstreamActivityTestPolicy()
	policyB.IntervalUnit = UpstreamActivityUnitMonths
	policyB.IntervalValue = 3
	policyB.ManualReferenceAt = &manual
	initialB, err := store.GetPolicy("server-b")
	if err != nil {
		t.Fatalf("initial policy B: %v", err)
	}
	if _, err := store.SavePolicy("server-b", scopeB, policyB, initialB.Revision, now); err != nil {
		t.Fatalf("save B: %v", err)
	}

	policies, err := store.SnapshotPolicies([]string{"server-a", "server-b", "server-missing", "", "server-a"})
	if err != nil {
		t.Fatalf("SnapshotPolicies: %v", err)
	}
	if len(policies) != 3 {
		t.Fatalf("snapshot size = %d, want 3: %+v", len(policies), policies)
	}
	if policy, ok := policies["server-a"]; !ok || policy.Enabled {
		t.Fatalf("server-a default policy = %+v/%v", policy, ok)
	}
	if policy, ok := policies["server-b"]; !ok || !policy.Enabled || policy.IntervalUnit != UpstreamActivityUnitMonths || policy.IntervalValue != 3 {
		t.Fatalf("server-b stored policy = %+v/%v", policy, ok)
	}
	if policy, ok := policies["server-missing"]; !ok || policy.Revision != 0 || policy.IntervalUnit != UpstreamActivityUnitDays {
		t.Fatalf("missing server default policy = %+v/%v", policy, ok)
	}
	revisions := map[int64]bool{}
	for _, policy := range policies {
		if policy.Revision != 0 {
			upstreamActivityTestRevisionRange(t, policy.Revision)
			if revisions[policy.Revision] {
				t.Fatalf("duplicate revision %d across servers", policy.Revision)
			}
			revisions[policy.Revision] = true
		}
	}
}

func TestUpstreamActivityStoreRevisionBounds(t *testing.T) {
	previous := int64(0)
	for i := 0; i < 1000; i++ {
		next := nextUpstreamActivityRevision(previous)
		upstreamActivityTestRevisionRange(t, next)
		if next == previous {
			t.Fatalf("revision did not change from %d", previous)
		}
		if next > 1<<53-1 {
			t.Fatalf("revision %d no longer fits a JS number", next)
		}
		previous = next
	}
}

func TestUpstreamActivityStoreClosedDatabaseFailsExplicitly(t *testing.T) {
	store, db, _ := newUpstreamActivityTestStore(t)
	if err := closeSQLite(db); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, _, err := store.Read("server", "scope"); err == nil || errors.Is(err, ErrUpstreamActivityScopeMismatch) {
		t.Fatalf("closed read error = %v", err)
	}
	if _, err := store.Record(UpstreamActivityRecord{
		ServerID: "server", AccountScope: "scope",
		LastActivityAt: func() *int64 { value := int64(1); return &value }(),
	}, 1); err == nil {
		t.Fatal("closed store record unexpectedly succeeded")
	}
	if _, err := store.GetPolicy("server"); err == nil {
		t.Fatal("closed store policy read unexpectedly succeeded")
	}
}
