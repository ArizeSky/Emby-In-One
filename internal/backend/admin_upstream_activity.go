package backend

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// K3 admin API for the per-upstream viewing/retention policy and the K1/K2
// summaries. It only reads or writes the local SQLite store: saving a policy
// never logs in to, probes, or otherwise contacts an upstream, and the DTO never
// contains the account-scope digest, credentials, or credential hashes.

const (
	// The account is not stable enough to own a summary (for example an entry
	// with no URL/username/API key yet). This is an availability problem, not a
	// policy state, so it must never render as disabled.
	adminUpstreamActivityReasonAccountUnavailable = "account_scope_unavailable"
	adminUpstreamActivityStoreUnavailableMessage  = "activity store unavailable"
	adminUpstreamActivityStoreWriteMessage        = "activity store write failed"
	adminUpstreamActivityConflictMessage          = "保活设置已在别处更新，请刷新后重试"
	adminUpstreamActivityInvalidMessage           = "保活设置参数无效"
	adminUpstreamActivityMaxBodyBytes             = 16 * 1024
)

// adminUpstreamActivityDTO is the only shape the panel sees. Every timestamp is
// RFC3339 or null (never 1970), manual and observed evidence stay separate, and
// serverNow makes a countdown authoritative regardless of the browser clock.
type adminUpstreamActivityDTO struct {
	ServerNow         string   `json:"serverNow"`
	Available         bool     `json:"available"`
	State             string   `json:"state"`
	Reason            string   `json:"reason,omitempty"`
	Message           string   `json:"message,omitempty"`
	Enabled           bool     `json:"enabled"`
	IntervalUnit      string   `json:"intervalUnit"`
	IntervalValue     int      `json:"intervalValue"`
	MinWatchSeconds   int      `json:"minWatchSeconds"`
	Revision          int64    `json:"revision"`
	LastActivityAt    *string  `json:"lastActivityAt"`
	LastQualifiedAt   *string  `json:"lastQualifiedAt"`
	ManualReferenceAt *string  `json:"manualReferenceAt"`
	ReferenceAt       *string  `json:"referenceAt"`
	ReferenceSource   string   `json:"referenceSource,omitempty"`
	DueAt             *string  `json:"dueAt"`
	RemainingRatio    *float64 `json:"remainingRatio"`
}

func adminUpstreamActivityNowRFC3339(nowMillis int64) string {
	if nowMillis <= 0 {
		nowMillis = time.Now().UnixMilli()
	}
	return time.UnixMilli(nowMillis).UTC().Format(time.RFC3339)
}

func adminUpstreamActivityDTOFromView(view UpstreamActivityView, availability UpstreamActivityAvailability, nowMillis int64) adminUpstreamActivityDTO {
	return adminUpstreamActivityDTO{
		ServerNow:         adminUpstreamActivityNowRFC3339(nowMillis),
		Available:         view.Available,
		State:             view.State,
		Reason:            view.Reason,
		Message:           availability.Message,
		Enabled:           view.Enabled,
		IntervalUnit:      view.IntervalUnit,
		IntervalValue:     view.IntervalValue,
		MinWatchSeconds:   view.MinWatchSeconds,
		Revision:          view.Revision,
		LastActivityAt:    UpstreamActivityTimeRFC3339(view.LastActivityAt),
		LastQualifiedAt:   UpstreamActivityTimeRFC3339(view.LastQualifiedAt),
		ManualReferenceAt: UpstreamActivityTimeRFC3339(view.ManualReferenceAt),
		ReferenceAt:       UpstreamActivityTimeRFC3339(view.ReferenceAt),
		ReferenceSource:   view.ReferenceSource,
		DueAt:             UpstreamActivityTimeRFC3339(view.DueAt),
		RemainingRatio:    view.RemainingRatio,
	}
}

// adminUpstreamActivityView sanitizes a stored policy before computing the
// view. A manual reference saved under another account must never leak into the
// current one: the cycle is retained, the manual baseline is dropped.
func adminUpstreamActivityView(policy UpstreamActivityPolicy, summary UpstreamActivitySummary, scope string, nowMillis int64, available bool, availability UpstreamActivityAvailability) (UpstreamActivityView, UpstreamActivityAvailability) {
	if policy.ManualReferenceAt != nil && (scope == "" || !SameUpstreamAccountScope(policy.ManualScope, scope)) {
		policy.ManualReferenceAt = nil
		policy.ManualScope = ""
	}
	view := ComputeUpstreamActivityView(policy, summary, nowMillis, available)
	if scope == "" {
		view.Available = false
		view.State = UpstreamActivityStateUnavailable
		view.Reason = adminUpstreamActivityReasonAccountUnavailable
		availability = UpstreamActivityAvailability{Available: false, Message: "account scope unavailable"}
	}
	return view, availability
}

func (a *App) upstreamActivityStoreAvailable() bool {
	return a != nil && a.UpstreamActivity != nil && a.UpstreamActivity.Available()
}

// ensureAdminUpstreamActivityScopeLocked initializes the current account scope
// only when the guard shows it changed. The caller holds adminMutationMu, so the
// scope it read from the current config cannot be replaced by an older snapshot
// while EnsureScope is writing.
func (a *App) ensureAdminUpstreamActivityScopeLocked(serverID, scope string, nowMillis int64) error {
	if a == nil || strings.TrimSpace(serverID) == "" || scope == "" {
		return ErrUpstreamActivityUnavailable
	}
	if !a.upstreamActivityStoreAvailable() {
		return ErrUpstreamActivityUnavailable
	}
	if ensured, ok := a.activityScopes.get(serverID); ok && SameUpstreamAccountScope(ensured, scope) {
		return nil
	}
	if _, err := a.UpstreamActivity.EnsureScope(serverID, scope, nowMillis); err != nil {
		a.activityHealth.markFailed(serverID, upstreamActivityHealthScope, err)
		a.logUpstreamActivityProblem("scope-init", serverID, err)
		return err
	}
	a.activityScopes.set(serverID, scope)
	a.activityHealth.markAvailable(serverID, upstreamActivityHealthScope)
	a.activityPolicies.invalidate(serverID)
	return nil
}

// handleAdminUpstreamActivityGet returns the policy, the observed summary and
// the authoritative server-computed state for one node. A store/read failure is
// a 200 with available=false and a clear diagnostic, never disabled/healthy.
func (a *App) handleAdminUpstreamActivityGet(w http.ResponseWriter, r *http.Request) {
	nowMillis := time.Now().UnixMilli()

	a.adminMutationMu.Lock()
	cfg := a.ConfigStore.Snapshot()
	_, upstream, ok := parsePathUpstream(r, &cfg)
	if !ok {
		a.adminMutationMu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Server not found"})
		return
	}
	serverID := upstream.ID
	scope := UpstreamAccountScope(upstream)
	var ensureErr error
	if serverID != "" && scope != "" {
		ensureErr = a.ensureAdminUpstreamActivityScopeLocked(serverID, scope, nowMillis)
	}
	policy := DefaultUpstreamActivityPolicy()
	summary := UpstreamActivitySummary{}
	storeAvailable := a.upstreamActivityStoreAvailable()
	readErr := error(nil)
	if ensureErr == nil && storeAvailable && serverID != "" && scope != "" {
		stored, err := a.UpstreamActivity.GetPolicy(serverID)
		if err != nil {
			readErr = err
		} else {
			policy = stored
			summary, _, err = a.UpstreamActivity.Read(serverID, scope)
			if err != nil {
				readErr = err
			}
		}
		if readErr != nil {
			a.activityHealth.markFailed(serverID, upstreamActivityHealthRead, readErr)
			a.logUpstreamActivityProblem("read", serverID, readErr)
		} else {
			// A successful read only clears this server's read marker. It must
			// not turn an earlier summary write failure into healthy data.
			a.activityHealth.markAvailable(serverID, upstreamActivityHealthRead)
		}
	}
	// The policy/summary read must stay inside the config lock: otherwise a
	// concurrent account switch could pair this response's old cfg/scope with
	// the new account's policy revision, and an old form would then look current.
	// The read is local and bounded (one policy + one summary), not media work.
	a.adminMutationMu.Unlock()

	availability := a.activityHealth.serverAvailability(storeAvailable, serverID)
	available := ensureErr == nil && readErr == nil && storeAvailable && availability.Available
	if serverID == "" || scope == "" {
		available = false
	}
	view, availability := adminUpstreamActivityView(policy, summary, scope, nowMillis, available, availability)
	writeJSON(w, http.StatusOK, adminUpstreamActivityDTOFromView(view, availability, nowMillis))
}

// loadAdminUpstreamActivityList prepares the activity object for every row of
// the upstream list. It holds adminMutationMu through the scope ensure and both
// batch reads (summaries and policies), so the returned rows cannot mix an old
// cfg/scope with a new account's policy revision; there is no per-node query.
func (a *App) loadAdminUpstreamActivityList() (Config, []adminUpstreamActivityDTO) {
	nowMillis := time.Now().UnixMilli()

	a.adminMutationMu.Lock()
	cfg := a.ConfigStore.Snapshot()
	ensureErrs := make(map[string]error, len(cfg.Upstream))
	for i := range cfg.Upstream {
		serverID := cfg.Upstream[i].ID
		scope := UpstreamAccountScope(&cfg.Upstream[i])
		if strings.TrimSpace(serverID) == "" || scope == "" {
			continue
		}
		if err := a.ensureAdminUpstreamActivityScopeLocked(serverID, scope, nowMillis); err != nil {
			ensureErrs[serverID] = err
		}
	}
	storeAvailable := a.upstreamActivityStoreAvailable()
	readIDs := make([]string, 0, len(cfg.Upstream))
	scopeByServer := make(map[string]string, len(cfg.Upstream))
	for i := range cfg.Upstream {
		serverID := cfg.Upstream[i].ID
		scope := UpstreamAccountScope(&cfg.Upstream[i])
		if strings.TrimSpace(serverID) == "" || scope == "" || ensureErrs[serverID] != nil {
			continue
		}
		readIDs = append(readIDs, serverID)
		scopeByServer[serverID] = scope
	}

	var summaries map[string]UpstreamActivitySummary
	var policies map[string]UpstreamActivityPolicy
	var readErr error
	if storeAvailable && len(readIDs) > 0 {
		summaries, readErr = a.UpstreamActivity.Snapshot(scopeByServer)
		if readErr == nil {
			policies, readErr = a.UpstreamActivity.SnapshotPolicies(readIDs)
		}
		if readErr != nil {
			for _, serverID := range readIDs {
				a.activityHealth.markFailed(serverID, upstreamActivityHealthRead, readErr)
				a.logUpstreamActivityProblem("read", serverID, readErr)
			}
		} else {
			for _, serverID := range readIDs {
				a.activityHealth.markAvailable(serverID, upstreamActivityHealthRead)
			}
		}
	}
	// Keep the batch reads inside the same config lock that ensured the scopes.
	// Releasing it earlier would allow an account switch to mix the old cfg/scope
	// with the new account's policy revision, which an old settings form could
	// then submit as if it were current. Both snapshots are local bounded reads.
	a.adminMutationMu.Unlock()

	out := make([]adminUpstreamActivityDTO, 0, len(cfg.Upstream))
	for i := range cfg.Upstream {
		serverID := cfg.Upstream[i].ID
		scope := UpstreamAccountScope(&cfg.Upstream[i])
		policy := DefaultUpstreamActivityPolicy()
		if stored, ok := policies[serverID]; ok {
			policy = stored
		}
		summary := UpstreamActivitySummary{}
		if stored, ok := summaries[serverID]; ok {
			summary = stored
		}
		availability := a.activityHealth.serverAvailability(storeAvailable, serverID)
		available := storeAvailable && scope != "" && ensureErrs[serverID] == nil && readErr == nil && availability.Available
		view, availability := adminUpstreamActivityView(policy, summary, scope, nowMillis, available, availability)
		out = append(out, adminUpstreamActivityDTOFromView(view, availability, nowMillis))
	}
	return cfg, out
}

// handleAdminUpstreamActivityPut stores the complete policy after strict input
// validation. The current config snapshot, scope ensure, optimistic-revision
// save and cache invalidation happen under one short adminMutationMu section,
// so an account switch cannot let an old form overwrite the new scope.
func (a *App) handleAdminUpstreamActivityPut(w http.ResponseWriter, r *http.Request) {
	nowMillis := time.Now().UnixMilli()
	policy, expectedRevision, err := decodeAdminUpstreamActivityInput(r, nowMillis)
	if err != nil {
		// Fixed text: a malformed body may contain an attacker-chosen value that
		// must never be reflected back or logged as part of an error.
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": adminUpstreamActivityInvalidMessage})
		return
	}

	var serverID, scope string
	var saved UpstreamActivityPolicy
	var summary UpstreamActivitySummary
	var ensureErr, saveErr, readErr error

	a.adminMutationMu.Lock()
	cfg := a.ConfigStore.Snapshot()
	_, upstream, ok := parsePathUpstream(r, &cfg)
	if !ok {
		a.adminMutationMu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "Server not found"})
		return
	}
	serverID = upstream.ID
	scope = UpstreamAccountScope(upstream)
	if strings.TrimSpace(serverID) == "" || scope == "" {
		a.adminMutationMu.Unlock()
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "上游账号范围不可用，无法保存保活设置"})
		return
	}
	ensureErr = a.ensureAdminUpstreamActivityScopeLocked(serverID, scope, nowMillis)
	if ensureErr == nil {
		saved, saveErr = a.UpstreamActivity.SavePolicy(serverID, scope, policy, expectedRevision, nowMillis)
		if saveErr != nil {
			if adminUpstreamActivityIsStoreFailure(saveErr) {
				a.activityHealth.markFailed(serverID, upstreamActivityHealthPolicy, saveErr)
				a.logUpstreamActivityProblem("policy-save", serverID, saveErr)
			}
		} else {
			// Only the policy write succeeded. Do not clear a summary failure.
			a.activityHealth.markAvailable(serverID, upstreamActivityHealthPolicy)
			a.activityPolicies.invalidate(serverID)
			summary, _, readErr = a.UpstreamActivity.Read(serverID, scope)
			if readErr != nil {
				a.activityHealth.markFailed(serverID, upstreamActivityHealthRead, readErr)
				a.logUpstreamActivityProblem("read", serverID, readErr)
			} else {
				a.activityHealth.markAvailable(serverID, upstreamActivityHealthRead)
			}
		}
	}
	a.adminMutationMu.Unlock()

	if ensureErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": adminUpstreamActivityStoreUnavailableMessage})
		return
	}
	if saveErr != nil {
		writeAdminUpstreamActivitySaveError(w, saveErr)
		return
	}

	storeAvailable := a.upstreamActivityStoreAvailable()
	availability := a.activityHealth.serverAvailability(storeAvailable, serverID)
	available := storeAvailable && readErr == nil && availability.Available
	view, availability := adminUpstreamActivityView(saved, summary, scope, nowMillis, available, availability)
	writeJSON(w, http.StatusOK, adminUpstreamActivityDTOFromView(view, availability, nowMillis))
}

func writeAdminUpstreamActivitySaveError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrUpstreamActivityInvalid):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": adminUpstreamActivityInvalidMessage})
	case errors.Is(err, ErrUpstreamActivityRevisionConflict), errors.Is(err, ErrUpstreamActivityScopeMismatch):
		writeJSON(w, http.StatusConflict, map[string]any{"error": adminUpstreamActivityConflictMessage, "conflict": true})
	case errors.Is(err, ErrUpstreamActivityUnavailable):
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": adminUpstreamActivityStoreUnavailableMessage})
	default:
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": adminUpstreamActivityStoreWriteMessage})
	}
}

// A validation rejection or a stale form is not a store failure: it must not
// poison the availability state for later reads.
func adminUpstreamActivityIsStoreFailure(err error) bool {
	if err == nil {
		return false
	}
	return !errors.Is(err, ErrUpstreamActivityInvalid) &&
		!errors.Is(err, ErrUpstreamActivityRevisionConflict) &&
		!errors.Is(err, ErrUpstreamActivityScopeMismatch)
}

// --- strict JSON input ------------------------------------------------------

var adminUpstreamActivityInputKeys = map[string]struct{}{
	"enabled":           {},
	"intervalUnit":      {},
	"intervalValue":     {},
	"minWatchSeconds":   {},
	"manualReferenceAt": {},
	"expectedRevision":  {},
}

var adminUpstreamActivityRequiredKeys = []string{
	"enabled",
	"intervalUnit",
	"intervalValue",
	"minWatchSeconds",
	"manualReferenceAt",
	"expectedRevision",
}

// decodeAdminUpstreamActivityInput accepts exactly one complete JSON object.
// Every field must be present, unknown/trailing fields are rejected, nullable
// fields are only manualReferenceAt, and the policy is normalized (ranges, UTC
// calendar, no future manual reference) before any store write.
func decodeAdminUpstreamActivityInput(r *http.Request, nowMillis int64) (UpstreamActivityPolicy, int64, error) {
	if r == nil || r.Body == nil {
		return UpstreamActivityPolicy{}, 0, fmt.Errorf("%w: empty request body", ErrUpstreamActivityInvalid)
	}
	defer r.Body.Close()
	body, err := io.ReadAll(io.LimitReader(r.Body, adminUpstreamActivityMaxBodyBytes+1))
	if err != nil {
		return UpstreamActivityPolicy{}, 0, fmt.Errorf("%w: unreadable body", ErrUpstreamActivityInvalid)
	}
	if len(body) > adminUpstreamActivityMaxBodyBytes {
		return UpstreamActivityPolicy{}, 0, fmt.Errorf("%w: body too large", ErrUpstreamActivityInvalid)
	}
	fields, err := decodeAdminUpstreamActivityObject(body)
	if err != nil {
		return UpstreamActivityPolicy{}, 0, err
	}
	for key := range fields {
		if _, ok := adminUpstreamActivityInputKeys[key]; !ok {
			return UpstreamActivityPolicy{}, 0, fmt.Errorf("%w: unknown field %q", ErrUpstreamActivityInvalid, key)
		}
	}
	for _, key := range adminUpstreamActivityRequiredKeys {
		if _, ok := fields[key]; !ok {
			return UpstreamActivityPolicy{}, 0, fmt.Errorf("%w: missing field %q", ErrUpstreamActivityInvalid, key)
		}
	}

	enabled, err := adminUpstreamActivityBool(fields["enabled"], "enabled")
	if err != nil {
		return UpstreamActivityPolicy{}, 0, err
	}
	intervalUnit, err := adminUpstreamActivityString(fields["intervalUnit"], "intervalUnit")
	if err != nil {
		return UpstreamActivityPolicy{}, 0, err
	}
	intervalValue, err := adminUpstreamActivityInt(fields["intervalValue"], "intervalValue")
	if err != nil {
		return UpstreamActivityPolicy{}, 0, err
	}
	minWatchSeconds, err := adminUpstreamActivityInt(fields["minWatchSeconds"], "minWatchSeconds")
	if err != nil {
		return UpstreamActivityPolicy{}, 0, err
	}
	manualReferenceAt, err := adminUpstreamActivityNullableTime(fields["manualReferenceAt"])
	if err != nil {
		return UpstreamActivityPolicy{}, 0, err
	}
	expectedRevision, err := adminUpstreamActivityInt64(fields["expectedRevision"], "expectedRevision")
	if err != nil {
		return UpstreamActivityPolicy{}, 0, err
	}
	if expectedRevision < 0 || expectedRevision > UpstreamActivityMaxRevision {
		return UpstreamActivityPolicy{}, 0, fmt.Errorf("%w: expectedRevision out of range", ErrUpstreamActivityInvalid)
	}

	policy := UpstreamActivityPolicy{
		Enabled:           enabled,
		IntervalUnit:      intervalUnit,
		IntervalValue:     intervalValue,
		MinWatchSeconds:   minWatchSeconds,
		ManualReferenceAt: manualReferenceAt,
	}
	normalized, err := NormalizeUpstreamActivityPolicy(policy, nowMillis)
	if err != nil {
		return UpstreamActivityPolicy{}, 0, err
	}
	return normalized, expectedRevision, nil
}

// decodeAdminUpstreamActivityObject accepts exactly one top-level object, rejects
// duplicate keys, and rejects trailing JSON. It works on the already bounded
// body, so the caller cannot be made to read an unbounded field value.
func decodeAdminUpstreamActivityObject(body []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("%w: invalid JSON body", ErrUpstreamActivityInvalid)
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return nil, fmt.Errorf("%w: body must be a JSON object", ErrUpstreamActivityInvalid)
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("%w: invalid JSON object", ErrUpstreamActivityInvalid)
		}
		key, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("%w: invalid JSON object key", ErrUpstreamActivityInvalid)
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, fmt.Errorf("%w: duplicate field %q", ErrUpstreamActivityInvalid, key)
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return nil, fmt.Errorf("%w: invalid JSON field %q", ErrUpstreamActivityInvalid, key)
		}
		fields[key] = raw
	}
	if _, err := decoder.Token(); err != nil {
		return nil, fmt.Errorf("%w: invalid JSON object", ErrUpstreamActivityInvalid)
	}
	var extra json.RawMessage
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: unexpected trailing JSON", ErrUpstreamActivityInvalid)
	}
	return fields, nil
}

func adminUpstreamActivityRawNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func adminUpstreamActivityBool(raw json.RawMessage, name string) (bool, error) {
	if adminUpstreamActivityRawNull(raw) {
		return false, fmt.Errorf("%w: %s must be a boolean", ErrUpstreamActivityInvalid, name)
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, fmt.Errorf("%w: %s must be a boolean", ErrUpstreamActivityInvalid, name)
	}
	return value, nil
}

func adminUpstreamActivityInt(raw json.RawMessage, name string) (int, error) {
	if adminUpstreamActivityRawNull(raw) {
		return 0, fmt.Errorf("%w: %s must be an integer", ErrUpstreamActivityInvalid, name)
	}
	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, fmt.Errorf("%w: %s must be an integer", ErrUpstreamActivityInvalid, name)
	}
	return value, nil
}

func adminUpstreamActivityInt64(raw json.RawMessage, name string) (int64, error) {
	if adminUpstreamActivityRawNull(raw) {
		return 0, fmt.Errorf("%w: %s must be an integer", ErrUpstreamActivityInvalid, name)
	}
	var value int64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, fmt.Errorf("%w: %s must be an integer", ErrUpstreamActivityInvalid, name)
	}
	return value, nil
}

func adminUpstreamActivityString(raw json.RawMessage, name string) (string, error) {
	if adminUpstreamActivityRawNull(raw) {
		return "", fmt.Errorf("%w: %s must be a string", ErrUpstreamActivityInvalid, name)
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("%w: %s must be a string", ErrUpstreamActivityInvalid, name)
	}
	return value, nil
}

func adminUpstreamActivityNullableTime(raw json.RawMessage) (*int64, error) {
	if adminUpstreamActivityRawNull(raw) {
		return nil, nil
	}
	value, err := adminUpstreamActivityString(raw, "manualReferenceAt")
	if err != nil {
		return nil, fmt.Errorf("%w: manualReferenceAt must be an RFC3339 string or null", ErrUpstreamActivityInvalid)
	}
	millis, err := ParseUpstreamActivityTimeRFC3339(value)
	if err != nil {
		return nil, err
	}
	return millis, nil
}
