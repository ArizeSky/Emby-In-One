package backend

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"strings"
	"time"
)

// UpstreamActivitySummary is the per-server account-bound playback summary.
// Timestamps are UTC Unix milliseconds; nil means "no evidence yet" and must be
// rendered as null, never as 1970. AccountScope is the internal fingerprint and
// is never serialized to clients.
type UpstreamActivitySummary struct {
	ServerID        string `json:"-"`
	AccountScope    string `json:"-"`
	LastActivityAt  *int64 `json:"lastActivityAt"`
	LastQualifiedAt *int64 `json:"lastQualifiedAt"`
	UpdatedAt       int64  `json:"-"`
}

// UpstreamActivityRecord is one confirmed-activity write. At least one
// timestamp is required; both are merged with max() against stored values so
// late or replayed samples cannot move the summary backwards.
type UpstreamActivityRecord struct {
	ServerID        string
	AccountScope    string
	LastActivityAt  *int64
	LastQualifiedAt *int64
}

// UpstreamActivityStore persists policies and activity summaries in the shared
// SQLite handle owned by IDStore. It opens no connection of its own and never
// holds the connection lock while doing non-database work.
type UpstreamActivityStore struct {
	db *sqliteDB
}

const upstreamActivitySchema = `
	CREATE TABLE IF NOT EXISTS upstream_activity_policy (
		server_id TEXT PRIMARY KEY,
		enabled INTEGER NOT NULL DEFAULT 0,
		interval_unit TEXT NOT NULL DEFAULT 'days',
		interval_value INTEGER NOT NULL DEFAULT 30,
		min_watch_seconds INTEGER NOT NULL DEFAULT 60,
		manual_reference_at INTEGER,
		manual_scope TEXT NOT NULL DEFAULT '',
		revision INTEGER NOT NULL DEFAULT 0
	);
	CREATE TABLE IF NOT EXISTS upstream_activity_summary (
		server_id TEXT PRIMARY KEY,
		account_scope TEXT NOT NULL,
		last_activity_at INTEGER,
		last_qualified_at INTEGER,
		updated_at INTEGER NOT NULL DEFAULT 0
	);
`

// NewUpstreamActivityStore initializes the activity tables on the shared DB and
// returns the store. A nil DB (SQLite unavailable / minimal App) yields a nil
// store so every call site stays nil-safe; schema creation is idempotent and
// does not touch existing tables.
func NewUpstreamActivityStore(db *sqliteDB) (*UpstreamActivityStore, error) {
	if db == nil {
		return nil, nil
	}
	if err := db.withWriteTx(func() error { return db.exec(upstreamActivitySchema) }); err != nil {
		return nil, fmt.Errorf("upstream activity store schema: %w", err)
	}
	return &UpstreamActivityStore{db: db}, nil
}

// Available reports whether a working store is wired. Closed handles are still
// reported as available here and fail explicitly on use, so callers never turn
// a failure into a disabled/healthy state.
func (s *UpstreamActivityStore) Available() bool {
	return s != nil && s.db != nil
}

func (s *UpstreamActivityStore) ensureDB() error {
	if s == nil || s.db == nil {
		return ErrUpstreamActivityUnavailable
	}
	return nil
}

func upstreamActivityNow(nowMillis int64) int64 {
	if nowMillis <= 0 {
		return time.Now().UnixMilli()
	}
	return nowMillis
}

func validateUpstreamActivityServerScope(serverID, scope string) error {
	if strings.TrimSpace(serverID) == "" {
		return fmt.Errorf("%w: empty server id", ErrUpstreamActivityInvalid)
	}
	if strings.TrimSpace(scope) == "" {
		return fmt.Errorf("%w: empty account scope", ErrUpstreamActivityInvalid)
	}
	return nil
}

func validateUpstreamActivityMillis(name string, value *int64) error {
	if value != nil && *value <= 0 {
		return fmt.Errorf("%w: %s must be a positive UTC time", ErrUpstreamActivityInvalid, name)
	}
	return nil
}

// EnsureScope is called with the current configuration's account fingerprint
// (never from an event payload). The summary and the policy's revision are the
// only things touched:
//
//   - same scope: observations are kept; a missing default policy row is
//     initialized with an opaque positive revision.
//   - changed scope: the summary is replaced in the same transaction, the
//     manual reference is cleared and the revision is regenerated even when no
//     manual reference existed. A stale form from the old account therefore
//     cannot save over the new one. The cycle itself is retained.
//
// Revisions are random positive values rather than a caller-visible counter, so
// deleting and recreating a server cannot restart at 0 and resurrect an old
// form (ABA).
func (s *UpstreamActivityStore) EnsureScope(serverID, scope string, nowMillis int64) (UpstreamActivitySummary, error) {
	if err := s.ensureDB(); err != nil {
		return UpstreamActivitySummary{}, err
	}
	if err := validateUpstreamActivityServerScope(serverID, scope); err != nil {
		return UpstreamActivitySummary{}, err
	}
	now := upstreamActivityNow(nowMillis)

	var summary UpstreamActivitySummary
	err := s.db.withWriteTx(func() error {
		existing, found, err := s.readSummaryLocked(serverID)
		if err != nil {
			return err
		}
		if found && SameUpstreamAccountScope(existing.AccountScope, scope) {
			summary = existing
			return s.ensureDefaultPolicyLocked(serverID)
		}
		if found {
			if err := s.db.execParams(`DELETE FROM upstream_activity_summary WHERE server_id = ?`, serverID); err != nil {
				return err
			}
		}
		if err := s.bumpPolicyForScopeChangeLocked(serverID); err != nil {
			return err
		}
		if err := s.db.execParams(`
			INSERT INTO upstream_activity_summary (server_id, account_scope, last_activity_at, last_qualified_at, updated_at)
			VALUES (?, ?, NULL, NULL, ?)
		`, serverID, scope, now); err != nil {
			return err
		}
		summary = UpstreamActivitySummary{ServerID: serverID, AccountScope: scope, UpdatedAt: now}
		return nil
	})
	if err != nil {
		return UpstreamActivitySummary{}, err
	}
	return summary, nil
}

// ensureDefaultPolicyLocked materializes the default policy row for a server
// that has none yet. The opaque revision makes the row's existence observable
// to stale forms instead of an implicit revision 0.
func (s *UpstreamActivityStore) ensureDefaultPolicyLocked(serverID string) error {
	if _, found, err := s.readPolicyLocked(serverID); err != nil || found {
		return err
	}
	policy := DefaultUpstreamActivityPolicy()
	policy.Revision = nextUpstreamActivityRevision(0)
	return s.writePolicyLocked(serverID, policy, false)
}

// bumpPolicyForScopeChangeLocked clears the old account's manual reference and
// always regenerates the revision, so every scope transition invalidates old
// settings forms even when no manual reference was ever set.
func (s *UpstreamActivityStore) bumpPolicyForScopeChangeLocked(serverID string) error {
	policy, found, err := s.readPolicyLocked(serverID)
	if err != nil {
		return err
	}
	if !found {
		defaults := DefaultUpstreamActivityPolicy()
		defaults.Revision = nextUpstreamActivityRevision(0)
		return s.writePolicyLocked(serverID, defaults, false)
	}
	policy.ManualReferenceAt = nil
	policy.ManualScope = ""
	policy.Revision = nextUpstreamActivityRevision(policy.Revision)
	return s.writePolicyLocked(serverID, policy, true)
}

// nextUpstreamActivityRevision returns a positive opaque revision different
// from the previous one. Random values prevent ABA when a server row is deleted
// and recreated. crypto/rand failure only falls back to a monotonic bump.
func nextUpstreamActivityRevision(previous int64) int64 {
	var buffer [8]byte
	if _, err := rand.Read(buffer[:]); err == nil {
		// 52 random bits keep revisions within 2^53-1, exactly representable as
		// a JavaScript number; 0 is not a usable revision.
		value := int64(binary.BigEndian.Uint64(buffer[:]) & ((1 << 52) - 1))
		if value > 0 && value != previous {
			return value
		}
	}
	value := previous + 1
	if value <= 0 || value > UpstreamActivityMaxRevision {
		value = 1
	}
	return value
}

// Read returns the summary only when it belongs to the expected current scope.
// A missing row or an old-scope row is reported as not available rather than as
// empty-but-healthy data.
func (s *UpstreamActivityStore) Read(serverID, scope string) (UpstreamActivitySummary, bool, error) {
	if err := s.ensureDB(); err != nil {
		return UpstreamActivitySummary{}, false, err
	}
	var summary UpstreamActivitySummary
	var found bool
	err := s.db.withReadLock(func() error {
		var err error
		summary, found, err = s.readSummaryLocked(serverID)
		return err
	})
	if err != nil {
		return UpstreamActivitySummary{}, false, err
	}
	if !found || !SameUpstreamAccountScope(summary.AccountScope, scope) {
		return UpstreamActivitySummary{}, false, nil
	}
	return summary, true, nil
}

// Snapshot reads all summaries under one connection read lock and returns only
// rows matching the expected scope map. Admin listings use this instead of one
// query per upstream.
func (s *UpstreamActivityStore) Snapshot(scopeByServer map[string]string) (map[string]UpstreamActivitySummary, error) {
	if err := s.ensureDB(); err != nil {
		return nil, err
	}
	summaries := make(map[string]UpstreamActivitySummary, len(scopeByServer))
	if len(scopeByServer) == 0 {
		return summaries, nil
	}
	err := s.db.withReadLock(func() error {
		stmt, err := s.db.prepare(`
			SELECT server_id, account_scope, COALESCE(last_activity_at, 0), COALESCE(last_qualified_at, 0), updated_at
			FROM upstream_activity_summary
		`)
		if err != nil {
			return err
		}
		defer stmt.finalize()
		for {
			hasRow, err := stmt.step()
			if err != nil {
				return err
			}
			if !hasRow {
				return nil
			}
			row := scanUpstreamActivitySummary(stmt)
			expected, ok := scopeByServer[row.ServerID]
			if ok && SameUpstreamAccountScope(row.AccountScope, expected) {
				summaries[row.ServerID] = row
			}
		}
	})
	if err != nil {
		return nil, err
	}
	return summaries, nil
}

// Record merges confirmed activity for the expected scope. It rejects records
// for a server whose current scope differs (or was never ensured): a late event
// from an old account must be dropped, not written over the new account.
func (s *UpstreamActivityStore) Record(record UpstreamActivityRecord, nowMillis int64) (UpstreamActivitySummary, error) {
	if err := s.ensureDB(); err != nil {
		return UpstreamActivitySummary{}, err
	}
	if err := validateUpstreamActivityServerScope(record.ServerID, record.AccountScope); err != nil {
		return UpstreamActivitySummary{}, err
	}
	if record.LastActivityAt == nil && record.LastQualifiedAt == nil {
		return UpstreamActivitySummary{}, fmt.Errorf("%w: no activity timestamps", ErrUpstreamActivityInvalid)
	}
	if err := validateUpstreamActivityMillis("lastActivityAt", record.LastActivityAt); err != nil {
		return UpstreamActivitySummary{}, err
	}
	if err := validateUpstreamActivityMillis("lastQualifiedAt", record.LastQualifiedAt); err != nil {
		return UpstreamActivitySummary{}, err
	}
	now := upstreamActivityNow(nowMillis)

	var summary UpstreamActivitySummary
	err := s.db.withWriteTx(func() error {
		existing, found, err := s.readSummaryLocked(record.ServerID)
		if err != nil {
			return err
		}
		if !found || !SameUpstreamAccountScope(existing.AccountScope, record.AccountScope) {
			return ErrUpstreamActivityScopeMismatch
		}
		if record.LastActivityAt != nil && (existing.LastActivityAt == nil || *record.LastActivityAt > *existing.LastActivityAt) {
			existing.LastActivityAt = cloneMillis(record.LastActivityAt)
		}
		if record.LastQualifiedAt != nil && (existing.LastQualifiedAt == nil || *record.LastQualifiedAt > *existing.LastQualifiedAt) {
			existing.LastQualifiedAt = cloneMillis(record.LastQualifiedAt)
		}
		if now > existing.UpdatedAt {
			existing.UpdatedAt = now
		}
		if err := s.writeSummaryLocked(existing); err != nil {
			return err
		}
		summary = existing
		return nil
	})
	if err != nil {
		return UpstreamActivitySummary{}, err
	}
	return summary, nil
}

// SnapshotPolicies reads all requested policies under one connection read lock
// (no per-server query/N+1) and fills defaults for servers without a stored
// row. Admin listings can pair this with Snapshot.
func (s *UpstreamActivityStore) SnapshotPolicies(serverIDs []string) (map[string]UpstreamActivityPolicy, error) {
	if err := s.ensureDB(); err != nil {
		return nil, err
	}
	policies := make(map[string]UpstreamActivityPolicy, len(serverIDs))
	requested := make(map[string]struct{}, len(serverIDs))
	for _, serverID := range serverIDs {
		if strings.TrimSpace(serverID) == "" {
			continue
		}
		requested[serverID] = struct{}{}
		policies[serverID] = DefaultUpstreamActivityPolicy()
	}
	if len(requested) == 0 {
		return policies, nil
	}
	err := s.db.withReadLock(func() error {
		stmt, err := s.db.prepare(upstreamActivityPolicySelect)
		if err != nil {
			return err
		}
		defer stmt.finalize()
		for {
			hasRow, err := stmt.step()
			if err != nil {
				return err
			}
			if !hasRow {
				return nil
			}
			serverID := stmt.columnText(0)
			if _, ok := requested[serverID]; !ok {
				continue
			}
			policies[serverID] = scanUpstreamActivityPolicy(stmt)
		}
	})
	if err != nil {
		return nil, err
	}
	return policies, nil
}

// GetPolicy returns the stored policy or the documented defaults when none was
// saved yet.
func (s *UpstreamActivityStore) GetPolicy(serverID string) (UpstreamActivityPolicy, error) {
	if err := s.ensureDB(); err != nil {
		return DefaultUpstreamActivityPolicy(), err
	}
	policy := DefaultUpstreamActivityPolicy()
	err := s.db.withReadLock(func() error {
		stored, found, err := s.readPolicyLocked(serverID)
		if err != nil {
			return err
		}
		if found {
			policy = stored
		}
		return nil
	})
	if err != nil {
		return DefaultUpstreamActivityPolicy(), err
	}
	return policy, nil
}

// SavePolicy writes a validated policy using optimistic concurrency. The
// expected revision must match the stored one; a server ensured by the current
// config always has a non-zero opaque revision, so revision 0 only means the
// settings form predates scope initialization. A stale form or an old-account
// write returns ErrUpstreamActivityScopeMismatch /
// ErrUpstreamActivityRevisionConflict without changing anything.
func (s *UpstreamActivityStore) SavePolicy(serverID, scope string, policy UpstreamActivityPolicy, expectedRevision, nowMillis int64) (UpstreamActivityPolicy, error) {
	if err := s.ensureDB(); err != nil {
		return policy, err
	}
	if err := validateUpstreamActivityServerScope(serverID, scope); err != nil {
		return policy, err
	}
	if expectedRevision < 0 {
		return policy, fmt.Errorf("%w: negative revision", ErrUpstreamActivityInvalid)
	}
	now := upstreamActivityNow(nowMillis)
	normalized, err := NormalizeUpstreamActivityPolicy(policy, now)
	if err != nil {
		return policy, err
	}

	var saved UpstreamActivityPolicy
	err = s.db.withWriteTx(func() error {
		summary, found, err := s.readSummaryLocked(serverID)
		if err != nil {
			return err
		}
		if !found || !SameUpstreamAccountScope(summary.AccountScope, scope) {
			return ErrUpstreamActivityScopeMismatch
		}
		stored, policyFound, err := s.readPolicyLocked(serverID)
		if err != nil {
			return err
		}
		if !policyFound {
			// EnsureScope always materializes a policy row. Saving from an
			// implicit revision 0 would reintroduce the delete/recreate ABA.
			return fmt.Errorf("%w: policy not initialized", ErrUpstreamActivityRevisionConflict)
		}
		currentRevision := stored.Revision
		if currentRevision != expectedRevision {
			return fmt.Errorf("%w: expected revision %d, current %d", ErrUpstreamActivityRevisionConflict, expectedRevision, currentRevision)
		}
		saved = normalized
		saved.ManualScope = ""
		if saved.ManualReferenceAt != nil {
			saved.ManualScope = scope
		}
		// Opaque positive revision: never a counter that could replay old forms
		// after a delete/recreate cycle (ABA).
		saved.Revision = nextUpstreamActivityRevision(currentRevision)
		return s.writePolicyLocked(serverID, saved, true)
	})
	if err != nil {
		return policy, err
	}
	return saved, nil
}

// Delete removes a deleted server's policy and summary atomically. It is keyed
// only by server ID, so reordering or renaming never triggers it.
func (s *UpstreamActivityStore) Delete(serverID string) error {
	if err := s.ensureDB(); err != nil {
		return err
	}
	if strings.TrimSpace(serverID) == "" {
		return fmt.Errorf("%w: empty server id", ErrUpstreamActivityInvalid)
	}
	return s.db.withWriteTx(func() error {
		if err := s.db.execParams(`DELETE FROM upstream_activity_policy WHERE server_id = ?`, serverID); err != nil {
			return err
		}
		return s.db.execParams(`DELETE FROM upstream_activity_summary WHERE server_id = ?`, serverID)
	})
}

// readSummaryLocked expects the caller to hold the connection lock (read lock
// or write transaction); it must never take the lock itself.
func (s *UpstreamActivityStore) readSummaryLocked(serverID string) (UpstreamActivitySummary, bool, error) {
	stmt, err := s.db.prepare(`
		SELECT server_id, account_scope, COALESCE(last_activity_at, 0), COALESCE(last_qualified_at, 0), updated_at
		FROM upstream_activity_summary WHERE server_id = ?
	`)
	if err != nil {
		return UpstreamActivitySummary{}, false, err
	}
	defer stmt.finalize()
	if err := stmt.bindAll(serverID); err != nil {
		return UpstreamActivitySummary{}, false, err
	}
	hasRow, err := stmt.step()
	if err != nil || !hasRow {
		return UpstreamActivitySummary{}, false, err
	}
	return scanUpstreamActivitySummary(stmt), true, nil
}

func scanUpstreamActivitySummary(stmt *sqliteStmt) UpstreamActivitySummary {
	summary := UpstreamActivitySummary{
		ServerID:     stmt.columnText(0),
		AccountScope: stmt.columnText(1),
		UpdatedAt:    stmt.columnInt64(4),
	}
	if value := stmt.columnInt64(2); value > 0 {
		summary.LastActivityAt = &value
	}
	if value := stmt.columnInt64(3); value > 0 {
		summary.LastQualifiedAt = &value
	}
	return summary
}

// writeSummaryLocked updates one summary row inside the caller's transaction.
func (s *UpstreamActivityStore) writeSummaryLocked(summary UpstreamActivitySummary) error {
	assignments := make([]string, 0, 3)
	args := make([]any, 0, 4)
	appendUpstreamActivityTimeAssignment(&assignments, &args, "last_activity_at", summary.LastActivityAt)
	appendUpstreamActivityTimeAssignment(&assignments, &args, "last_qualified_at", summary.LastQualifiedAt)
	assignments = append(assignments, "updated_at = ?")
	args = append(args, summary.UpdatedAt, summary.ServerID)
	return s.db.execParams(`
		UPDATE upstream_activity_summary SET `+strings.Join(assignments, ", ")+`
		WHERE server_id = ?
	`, args...)
}

func appendUpstreamActivityTimeAssignment(assignments *[]string, args *[]any, column string, value *int64) {
	if value == nil {
		*assignments = append(*assignments, column+" = NULL")
		return
	}
	*assignments = append(*assignments, column+" = ?")
	*args = append(*args, *value)
}

func (s *UpstreamActivityStore) readPolicyLocked(serverID string) (UpstreamActivityPolicy, bool, error) {
	stmt, err := s.db.prepare(upstreamActivityPolicySelect + ` WHERE server_id = ?`)
	if err != nil {
		return UpstreamActivityPolicy{}, false, err
	}
	defer stmt.finalize()
	if err := stmt.bindAll(serverID); err != nil {
		return UpstreamActivityPolicy{}, false, err
	}
	hasRow, err := stmt.step()
	if err != nil || !hasRow {
		return UpstreamActivityPolicy{}, false, err
	}
	return scanUpstreamActivityPolicy(stmt), true, nil
}

const upstreamActivityPolicySelect = `
	SELECT server_id, enabled, interval_unit, interval_value, min_watch_seconds,
	       COALESCE(manual_reference_at, 0), manual_scope, revision
	FROM upstream_activity_policy
`

func scanUpstreamActivityPolicy(stmt *sqliteStmt) UpstreamActivityPolicy {
	policy := UpstreamActivityPolicy{
		Enabled:         stmt.columnInt(1) != 0,
		IntervalUnit:    stmt.columnText(2),
		IntervalValue:   stmt.columnInt(3),
		MinWatchSeconds: stmt.columnInt(4),
		ManualScope:     stmt.columnText(6),
		Revision:        stmt.columnInt64(7),
	}
	if value := stmt.columnInt64(5); value > 0 {
		policy.ManualReferenceAt = &value
	}
	return policy
}

// writePolicyLocked inserts or replaces one policy row inside the caller's
// transaction, preserving the account scope of the manual reference.
func (s *UpstreamActivityStore) writePolicyLocked(serverID string, policy UpstreamActivityPolicy, exists bool) error {
	manualSQL, manualArgs := upstreamActivityNullableMillisSQL(policy.ManualReferenceAt)
	if exists {
		args := []any{
			boolToInt(policy.Enabled), policy.IntervalUnit, policy.IntervalValue, policy.MinWatchSeconds,
		}
		args = append(args, manualArgs...)
		args = append(args, policy.ManualScope, policy.Revision, serverID)
		return s.db.execParams(`
			UPDATE upstream_activity_policy SET
				enabled = ?, interval_unit = ?, interval_value = ?, min_watch_seconds = ?,
				manual_reference_at = `+manualSQL+`, manual_scope = ?, revision = ?
			WHERE server_id = ?
		`, args...)
	}
	args := []any{serverID, boolToInt(policy.Enabled), policy.IntervalUnit, policy.IntervalValue, policy.MinWatchSeconds}
	args = append(args, manualArgs...)
	args = append(args, policy.ManualScope, policy.Revision)
	return s.db.execParams(`
		INSERT INTO upstream_activity_policy (
			server_id, enabled, interval_unit, interval_value, min_watch_seconds,
			manual_reference_at, manual_scope, revision
		) VALUES (?, ?, ?, ?, ?, `+manualSQL+`, ?, ?)
	`, args...)
}

func upstreamActivityNullableMillisSQL(value *int64) (string, []any) {
	if value == nil {
		return "NULL", nil
	}
	return "?", []any{*value}
}
