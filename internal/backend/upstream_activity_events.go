package backend

import (
	"net/http"
	"strings"
	"sync"
	"time"
)

// K2 wiring between successful playback reports and the upstream activity
// tracker/store. This file contains no playback behaviour: handlers call it
// after the existing response decision and only when the upstream confirmed the
// report with a 2xx. Nothing here can turn a failed/unknown report into
// confirmed activity, and nothing here changes the HTTP answer, the limiter
// verdict or the local WatchStore rules.

const upstreamActivityPolicyCacheTTL = time.Minute

// upstreamActivityEvent is captured before the upstream request: the local
// receipt order/time (so a slow concurrent request cannot reorder samples) and
// the account scope the request was sent under. The scope is re-verified after
// the request; if the configuration changed meanwhile the event is discarded.
type upstreamActivityEvent struct {
	Token upstreamActivitySampleToken
	Scope string
}

// upstreamActivityScopeGuard remembers which scope was already ensured in the
// store, so a heartbeat does not run the scope write on every event. It is not
// authoritative: the current ConfigStore snapshot is always checked first.
type upstreamActivityScopeGuard struct {
	mu      sync.Mutex
	ensured map[string]string
}

func (g *upstreamActivityScopeGuard) get(serverID string) (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	scope, ok := g.ensured[serverID]
	return scope, ok
}

func (g *upstreamActivityScopeGuard) set(serverID, scope string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.ensured == nil {
		g.ensured = map[string]string{}
	}
	g.ensured[serverID] = scope
}

func (g *upstreamActivityScopeGuard) forget(serverID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.ensured, serverID)
}

type upstreamActivityPolicyCacheEntry struct {
	minWatch  time.Duration
	fetchedAt time.Time
	err       error
}

// upstreamActivityPolicyCache keeps the effective min-watch threshold per
// server for a short time. The threshold is captured when a session is first
// tracked, so a policy edit never recalculates an in-flight session; the cache
// only bounds database reads for new sessions' initial value.
type upstreamActivityPolicyCache struct {
	mu      sync.Mutex
	entries map[string]upstreamActivityPolicyCacheEntry
}

// minWatch returns the threshold for a session. A missing policy row is a
// normal state (documented defaults, nil error); a real read failure returns an
// error so the caller skips this observation instead of silently relaxing a
// configured threshold to 60 seconds.
func (c *upstreamActivityPolicyCache) minWatch(store *UpstreamActivityStore, serverID string, now time.Time) (time.Duration, error) {
	fallback := normalizeUpstreamActivityMinWatch(0)
	if store == nil || !store.Available() {
		return 0, ErrUpstreamActivityUnavailable
	}
	c.mu.Lock()
	entry, ok := c.entries[serverID]
	c.mu.Unlock()
	if ok && now.Sub(entry.fetchedAt) < upstreamActivityPolicyCacheTTL {
		return entry.minWatch, entry.err
	}
	policy, err := store.GetPolicy(serverID)
	if err != nil {
		// Cache the failure for the same short TTL: a broken database must not be
		// hit on every heartbeat, and the default is never substituted for a
		// configured threshold.
		c.store(serverID, 0, err, now)
		return 0, err
	}
	minWatch := time.Duration(policy.MinWatchSeconds) * time.Second
	if minWatch <= 0 {
		minWatch = fallback
	}
	c.store(serverID, minWatch, nil, now)
	return minWatch, nil
}

func (c *upstreamActivityPolicyCache) store(serverID string, minWatch time.Duration, err error, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string]upstreamActivityPolicyCacheEntry{}
	}
	c.entries[serverID] = upstreamActivityPolicyCacheEntry{minWatch: minWatch, fetchedAt: now, err: err}
}

// invalidate drops a cached threshold, e.g. after K3 saves a policy or when the
// account scope changed.
func (c *upstreamActivityPolicyCache) invalidate(serverID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, serverID)
}

// upstreamActivityHealth is the read-only availability state K3 exposes. A
// missing store or a failed write must show as unavailable/stale instead of
// "disabled" or "healthy"; a later success clears the failure.
type upstreamActivityHealth struct {
	mu        sync.Mutex
	failedAt  int64
	lastError string
}

// UpstreamActivityAvailability is the sanitized state of the activity store.
// Message never contains credentials; it is a redacted diagnostic.
type UpstreamActivityAvailability struct {
	Available bool   `json:"available"`
	Message   string `json:"message,omitempty"`
	FailedAt  int64  `json:"failedAt,omitempty"`
}

func (h *upstreamActivityHealth) markFailed(err error) {
	message := "activity store write failed"
	if err != nil {
		message = redactURLInError(err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failedAt = time.Now().UnixMilli()
	h.lastError = message
}

func (h *upstreamActivityHealth) markAvailable() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failedAt = 0
	h.lastError = ""
}

func (h *upstreamActivityHealth) availability(storeAvailable bool) UpstreamActivityAvailability {
	if !storeAvailable {
		return UpstreamActivityAvailability{Available: false, Message: "activity store unavailable"}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failedAt != 0 {
		return UpstreamActivityAvailability{Available: false, Message: h.lastError, FailedAt: h.failedAt}
	}
	return UpstreamActivityAvailability{Available: true}
}

// UpstreamActivityAvailability reports whether confirmed viewing statistics can
// currently be read/written. K3 uses it to return "unavailable" instead of a
// healthy empty summary when the shared database or a write failed.
func (a *App) UpstreamActivityAvailability() UpstreamActivityAvailability {
	if a == nil {
		return UpstreamActivityAvailability{Available: false, Message: "activity store unavailable"}
	}
	return a.activityHealth.availability(a.UpstreamActivity.Available())
}

// beginUpstreamActivityEvent captures everything that must be bound to the
// request before it reaches the network: the local receipt order/time and the
// account scope of the client that will carry it. An empty Scope means the
// event will not be counted (store unavailable, stale client or no stable
// account identity).
func (a *App) beginUpstreamActivityEvent(r *http.Request, serverID string, client *UpstreamClient, body map[string]any) upstreamActivityEvent {
	if a == nil || a.activityTracker == nil || a.UpstreamActivity == nil || !a.UpstreamActivity.Available() {
		return upstreamActivityEvent{}
	}
	if r == nil || client == nil || strings.TrimSpace(serverID) == "" {
		return upstreamActivityEvent{}
	}
	reqCtx := requestContextFrom(r.Context())
	if reqCtx == nil || reqCtx.ProxyUser == nil || reqCtx.ProxyUser.UserID == "" {
		return upstreamActivityEvent{}
	}
	playSessionID, _ := body["PlaySessionId"].(string)
	// Ordering first: this is the local receive order, independent of how long
	// the scope check or the upstream request takes.
	token := a.activityTracker.Begin(reqCtx.ProxyUser.UserID, serverID, playSessionID)
	scope := a.captureUpstreamActivityScope(serverID, client)
	if scope == "" {
		return upstreamActivityEvent{}
	}
	// The account scope belongs to the session key: a reused PlaySessionId on a
	// new account must not inherit the old account's accumulation.
	token.Key.AccountScope = scope
	return upstreamActivityEvent{Token: token, Scope: scope}
}

// captureUpstreamActivityScope reads the current configuration and the client's
// locked configuration under the short admin mutation lock and returns the
// account scope only when they agree. It performs no database write and holds
// no lock across the network.
func (a *App) captureUpstreamActivityScope(serverID string, client *UpstreamClient) string {
	if a == nil || a.ConfigStore == nil || client == nil {
		return ""
	}
	a.adminMutationMu.Lock()
	defer a.adminMutationMu.Unlock()
	cfg := a.ConfigStore.Snapshot()
	index := upstreamConfigIndexByID(&cfg, serverID)
	if index < 0 {
		return ""
	}
	current := UpstreamAccountScope(&cfg.Upstream[index])
	clientCfg := client.ConfigSnapshot()
	if !SameUpstreamAccountScope(current, UpstreamAccountScope(&clientCfg)) {
		// The client that will carry this request no longer matches the current
		// account: the event belongs to the old account and must not be counted.
		a.activityPolicies.invalidate(serverID)
		a.logUpstreamActivityProblem("scope-changed", serverID, nil)
		return ""
	}
	return current
}

// confirmUpstreamActivityScope re-checks the captured scope after the network
// request and ensures it in the store. A configuration change during the
// request makes the captured scope stale, so the event is discarded instead of
// writing an old account's observation under the new one.
func (a *App) confirmUpstreamActivityScope(serverID, capturedScope string) (string, bool) {
	if a == nil || a.ConfigStore == nil || capturedScope == "" {
		return "", false
	}
	if a.UpstreamActivity == nil || !a.UpstreamActivity.Available() {
		return "", false
	}
	a.adminMutationMu.Lock()
	defer a.adminMutationMu.Unlock()
	cfg := a.ConfigStore.Snapshot()
	index := upstreamConfigIndexByID(&cfg, serverID)
	if index < 0 {
		return "", false
	}
	if !SameUpstreamAccountScope(currentUpstreamAccountScope(&cfg, index), capturedScope) {
		a.activityPolicies.invalidate(serverID)
		a.logUpstreamActivityProblem("scope-changed", serverID, nil)
		return "", false
	}
	if ensured, ok := a.activityScopes.get(serverID); ok && SameUpstreamAccountScope(ensured, capturedScope) {
		return capturedScope, true
	}
	if _, err := a.UpstreamActivity.EnsureScope(serverID, capturedScope, time.Now().UnixMilli()); err != nil {
		a.activityHealth.markFailed(err)
		a.logUpstreamActivityProblem("scope-init", serverID, err)
		return "", false
	}
	// A scope change may accompany a policy edit; never reuse a threshold cached
	// for the previous account range.
	if previous, ok := a.activityScopes.get(serverID); !ok || !SameUpstreamAccountScope(previous, capturedScope) {
		a.activityPolicies.invalidate(serverID)
	}
	a.activityScopes.set(serverID, capturedScope)
	return capturedScope, true
}

func currentUpstreamAccountScope(cfg *Config, index int) string {
	if cfg == nil || index < 0 || index >= len(cfg.Upstream) {
		return ""
	}
	return UpstreamAccountScope(&cfg.Upstream[index])
}

func upstreamConfigIndexByID(cfg *Config, serverID string) int {
	if cfg == nil || serverID == "" {
		return -1
	}
	for i := range cfg.Upstream {
		if cfg.Upstream[i].ID == serverID {
			return i
		}
	}
	return -1
}

// upstreamActivityEventAllowed applies the existing authorization decisions to
// statistics: a tracked unit must have accepted the event, and an untracked
// unit only allows a Progress/Stopped the session-event tracker does not
// consider superseded. A Started may supersede an older session, so it is
// accepted exactly when the limiter allowed it (or no unit is tracked).
func (a *App) upstreamActivityEventAllowed(r *http.Request, virtualItemID string, body map[string]any, kind playbackEventKind, verdict playbackSessionVerdict) bool {
	if verdict.Tracked {
		return verdict.Accepted
	}
	if kind == playbackEventStarted {
		return true
	}
	reqCtx := requestContextFrom(r.Context())
	if reqCtx == nil || reqCtx.ProxyUser == nil {
		return false
	}
	return a.sessionEvents.AcceptEvent(reqCtx.ProxyUser.UserID, virtualItemID, sessionIdentityID(body))
}

// discardUpstreamActivityEvent drops the pending baseline of a report that did
// not get a confirmed 2xx answer. It is also called by the session handlers'
// early 4xx paths, before they return the unchanged client response.
func (a *App) discardUpstreamActivityEvent(event upstreamActivityEvent) {
	if a == nil || a.activityTracker == nil || event.Token.Seq == 0 {
		return
	}
	a.activityTracker.Discard(event.Token)
}

// recordUpstreamActivityEvent is the thin hook called by the session handlers
// after the upstream answered 2xx. confirmed is false for preparation failures
// and transport errors (unknown outcome); refused 4xx answers call the discard
// hook before their early return. A false confirmed value never contributes
// statistics, and it breaks the evidence chain so the next confirmed sample
// cannot credit the unconfirmed interval from the old baseline.
func (a *App) recordUpstreamActivityEvent(r *http.Request, event upstreamActivityEvent, serverID, virtualItemID string, body map[string]any, kind playbackEventKind, confirmed bool, verdict playbackSessionVerdict) {
	if a == nil || a.activityTracker == nil || event.Token.Seq == 0 || event.Scope == "" {
		return
	}
	if !confirmed {
		a.discardUpstreamActivityEvent(event)
		return
	}
	if !a.upstreamActivityEventAllowed(r, virtualItemID, body, kind, verdict) {
		return
	}
	if _, ok := a.confirmUpstreamActivityScope(serverID, event.Scope); !ok {
		return
	}

	minWatch, err := a.upstreamActivityMinWatch(serverID)
	if err != nil {
		// Never relax a configured threshold on a read failure: skip this
		// observation and surface the store problem.
		if kind == playbackEventStopped {
			// A confirmed Stop still ends the in-memory session even when its
			// statistics cannot be read/written.
			a.activityTracker.Finish(event.Token.Key)
		}
		a.activityHealth.markFailed(err)
		a.logUpstreamActivityProblem("policy", serverID, err)
		return
	}
	sample := upstreamActivitySample{
		Token:         event.Token,
		Kind:          kind,
		PositionTicks: upstreamActivityPositionTicks(body),
		Paused:        upstreamActivityPaused(body),
	}
	observation := a.activityTracker.Observe(sample, minWatch)
	if observation.Stop {
		// Even when no write is due, an accepted Stop ends the session; older
		// samples arriving afterwards must not recreate it.
		defer a.activityTracker.Finish(event.Token.Key)
	}
	if observation.LastActivityAt == nil && observation.LastQualifiedAt == nil {
		return
	}

	record := UpstreamActivityRecord{
		ServerID:        serverID,
		AccountScope:    event.Scope,
		LastActivityAt:  upstreamActivityMillis(observation.LastActivityAt),
		LastQualifiedAt: upstreamActivityMillis(observation.LastQualifiedAt),
	}
	if _, err := a.UpstreamActivity.Record(record, time.Now().UnixMilli()); err != nil {
		// Failed writes are never remembered as flushed: roll back the
		// throttling reservation so the next heartbeat retries, and advance the
		// persisted marker only through MarkPersisted on success.
		a.activityTracker.MarkPersistFailed(event.Token.Key, observation)
		a.activityHealth.markFailed(err)
		a.logUpstreamActivityProblem("persist", serverID, err)
		return
	}
	a.activityHealth.markAvailable()
	a.activityTracker.MarkPersisted(event.Token.Key, observation.LastQualifiedAt)
}

// upstreamActivityMinWatch returns the threshold for a session that is about to
// be tracked. Existing sessions keep the value captured at their start.
func (a *App) upstreamActivityMinWatch(serverID string) (time.Duration, error) {
	if a == nil {
		return 0, ErrUpstreamActivityUnavailable
	}
	return a.activityPolicies.minWatch(a.UpstreamActivity, serverID, time.Now())
}

// ensureUpstreamActivityScopes initializes the summary/policy rows for the
// current configuration at App start. It holds adminMutationMu so no config
// commit can interleave between the snapshot and the EnsureScope write; a
// failure is logged and never blocks App startup or playback.
func (a *App) ensureUpstreamActivityScopes() {
	if a == nil || a.ConfigStore == nil || a.UpstreamActivity == nil || !a.UpstreamActivity.Available() {
		return
	}
	a.adminMutationMu.Lock()
	defer a.adminMutationMu.Unlock()
	cfg := a.ConfigStore.Snapshot()
	now := time.Now().UnixMilli()
	failed := false
	for i := range cfg.Upstream {
		serverID := cfg.Upstream[i].ID
		if strings.TrimSpace(serverID) == "" {
			continue
		}
		scope := UpstreamAccountScope(&cfg.Upstream[i])
		if scope == "" {
			continue
		}
		// The snapshot was taken under adminMutationMu, so this write cannot
		// overwrite a scope from a newer configuration.
		if _, err := a.UpstreamActivity.EnsureScope(serverID, scope, now); err != nil {
			failed = true
			a.activityHealth.markFailed(err)
			a.logUpstreamActivityProblem("scope-init", serverID, err)
			continue
		}
		a.activityScopes.set(serverID, scope)
		a.activityPolicies.invalidate(serverID)
	}
	if !failed {
		// Availability is only cleared when the whole required operation
		// succeeded; a partial failure stays visible to K3.
		a.activityHealth.markAvailable()
	}
}

// purgeUpstreamActivityServer removes a deleted node's statistics and every
// in-memory trace of it. The config commit already removed the server, so an
// old request confirming its scope afterwards is rejected by the current-config
// check; this function makes sure the deleted server cannot keep accumulating.
func (a *App) purgeUpstreamActivityServer(serverID string) {
	if a == nil || strings.TrimSpace(serverID) == "" {
		return
	}
	if a.UpstreamActivity != nil {
		if err := a.UpstreamActivity.Delete(serverID); err != nil {
			a.activityHealth.markFailed(err)
			a.logUpstreamActivityProblem("delete", serverID, err)
		}
	}
	if a.activityTracker != nil {
		a.activityTracker.ForgetServer(serverID)
	}
	a.activityScopes.forget(serverID)
	a.activityPolicies.invalidate(serverID)
}

// logUpstreamActivityProblem emits a throttled, redacted diagnostic. Logging
// must never spam per heartbeat and must never contain credentials.
func (a *App) logUpstreamActivityProblem(kind, serverID string, err error) {
	if a == nil || a.Logger == nil {
		return
	}
	if !a.noticeThrottle.allow("upstream-activity:" + kind + ":" + serverID) {
		return
	}
	detail := "state changed during the request"
	if err != nil {
		detail = redactURLInError(err)
	}
	a.Logger.Warnf("upstream activity %s failed for server %s: %s", kind, serverID, detail)
}

func upstreamActivityPositionTicks(body map[string]any) *int64 {
	if body == nil {
		return nil
	}
	value, ok := numericInt64(body["PositionTicks"])
	if !ok || value < 0 {
		return nil
	}
	return &value
}

func upstreamActivityPaused(body map[string]any) bool {
	if body == nil {
		return false
	}
	if paused, ok := body["IsPaused"].(bool); ok {
		return paused
	}
	if paused, ok := body["Paused"].(bool); ok {
		return paused
	}
	return false
}

func upstreamActivityMillis(value *time.Time) *int64 {
	if value == nil || value.IsZero() {
		return nil
	}
	millis := value.UTC().UnixMilli()
	if millis <= 0 {
		return nil
	}
	return &millis
}
