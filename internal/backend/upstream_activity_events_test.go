package backend

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Coverage for the K2 hook: a confirmed 2xx report is counted for the account
// scope the request was sent under, a refused/unknown report never is, an
// account change around the request discards the sample, and the store health
// is observable for K3 instead of being reported as a healthy empty summary.

type upstreamActivityEventFixture struct {
	app         *App
	store       *UpstreamActivityStore
	db          *sqliteDB
	configStore *ConfigStore
	client      *UpstreamClient
	upstream    UpstreamConfig
	clock       *fakeUpstreamActivityClock
}

func newUpstreamActivityEventFixture(t *testing.T) *upstreamActivityEventFixture {
	t.Helper()
	store, db, _ := newUpstreamActivityTestStore(t)
	upstream := UpstreamConfig{
		ID:       "srv-1",
		Name:     "srv",
		URL:      "http://upstream.example",
		Username: "alice",
		APIKey:   "activity-test-key",
	}
	cfg := Config{Upstream: []UpstreamConfig{upstream}}
	configStore := &ConfigStore{config: &cfg}
	app := &App{
		ConfigStore:      configStore,
		UpstreamActivity: store,
		activityTracker:  newUpstreamActivityTracker(),
		sessionEvents:    newSessionEventTracker(),
	}
	clock := newFakeUpstreamActivityClock()
	app.activityTracker.now = func() time.Time { return clock.now }
	app.ensureUpstreamActivityScopes()
	return &upstreamActivityEventFixture{
		app:         app,
		store:       store,
		db:          db,
		configStore: configStore,
		client:      newUpstreamClient(cfg, upstream, 0, nil),
		upstream:    upstream,
		clock:       clock,
	}
}

func (f *upstreamActivityEventFixture) scope() string {
	return UpstreamAccountScope(&f.upstream)
}

func upstreamActivityEventRequest(userID string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/Sessions/Playing/Progress", nil)
	return req.WithContext(context.WithValue(req.Context(), requestContextKey{}, &RequestContext{
		ProxyUser: &tokenInfo{UserID: userID, Role: "user"},
	}))
}

func upstreamActivityEventBody(playSessionID, sessionID string, positionSeconds float64) map[string]any {
	body := map[string]any{
		"ItemId":        "item-1",
		"PlaySessionId": playSessionID,
		"PositionTicks": upstreamActivityTestTicks(positionSeconds),
	}
	if sessionID != "" {
		body["SessionId"] = sessionID
	}
	return body
}

func (f *upstreamActivityEventFixture) begin(userID string, body map[string]any) (*http.Request, upstreamActivityEvent) {
	req := upstreamActivityEventRequest(userID)
	return req, f.app.beginUpstreamActivityEvent(req, f.upstream.ID, f.client, body)
}

func (f *upstreamActivityEventFixture) record(req *http.Request, event upstreamActivityEvent, body map[string]any, kind playbackEventKind, confirmed bool, verdict playbackSessionVerdict) {
	f.app.recordUpstreamActivityEvent(req, event, f.upstream.ID, "item-1", body, kind, confirmed, verdict)
}

func upstreamActivityAllowedVerdict() playbackSessionVerdict {
	return playbackSessionVerdict{Tracked: true, Accepted: true, LocalProgress: true}
}

func (f *upstreamActivityEventFixture) summary(t *testing.T, scope string) (UpstreamActivitySummary, bool) {
	t.Helper()
	summary, found, err := f.store.Read(f.upstream.ID, scope)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	return summary, found
}

func (f *upstreamActivityEventFixture) persistedQualifiedAt(t *testing.T, playSessionID string) time.Time {
	t.Helper()
	key := upstreamActivityTestScopedKey("user", f.upstream.ID, f.scope(), playSessionID)
	f.app.activityTracker.mu.Lock()
	defer f.app.activityTracker.mu.Unlock()
	session := f.app.activityTracker.sessions[key]
	if session == nil {
		return time.Time{}
	}
	return session.persistedQualifiedAt
}

// --- handler-entry coverage -------------------------------------------------
//
// The tests below drive the real /Sessions/Playing* handlers through an
// httptest App instead of calling the K2 helpers directly, so the confirmed
// 2xx gate and the account/session attribution are exercised at the same entry
// point production uses.

type upstreamActivityHandlerUpstream struct {
	server *httptest.Server
	// progressStatus is the answer for /Sessions/Playing/Progress. A negative
	// value drops the connection without an HTTP answer (transport unknown).
	progressStatus atomic.Int32
	mu             sync.Mutex
	hook           func()
}

func newUpstreamActivityHandlerUpstream(t *testing.T) *upstreamActivityHandlerUpstream {
	t.Helper()
	upstream := &upstreamActivityHandlerUpstream{}
	upstream.progressStatus.Store(http.StatusNoContent)
	upstream.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"AccessToken":"activity-handler-token","User":{"Id":"upstream-user"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/Sessions/Playing":
			upstream.runHook()
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/Sessions/Playing/Progress":
			status := upstream.progressStatus.Load()
			if status < 0 {
				if hijacker, ok := w.(http.Hijacker); ok {
					if conn, _, err := hijacker.Hijack(); err == nil {
						_ = conn.Close()
						return
					}
				}
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(int(status))
		case r.Method == http.MethodPost && r.URL.Path == "/Sessions/Playing/Stopped":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.server.Close)
	return upstream
}

func (u *upstreamActivityHandlerUpstream) setStartedHook(hook func()) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.hook = hook
}

func (u *upstreamActivityHandlerUpstream) runHook() {
	u.mu.Lock()
	hook := u.hook
	u.hook = nil
	u.mu.Unlock()
	if hook != nil {
		hook()
	}
}

func upstreamActivityHandlerTarget(t *testing.T, app *App) (string, string) {
	t.Helper()
	clients := app.Upstream.Clients()
	if len(clients) == 0 {
		t.Fatal("no upstream client")
	}
	return clients[0].ID, UpstreamAccountScope(&clients[0].Config)
}

func upstreamActivityHandlerBody(itemID, playSessionID, sessionID string, positionSeconds float64) map[string]any {
	body := map[string]any{
		"ItemId":        itemID,
		"PlaySessionId": playSessionID,
		"PositionTicks": upstreamActivityTestTicks(positionSeconds),
	}
	if sessionID != "" {
		body["SessionId"] = sessionID
	}
	return body
}

func doUpstreamActivitySessionEvent(t *testing.T, handler http.Handler, token, path string, body map[string]any) {
	t.Helper()
	rr := doAuthJSON(t, handler, http.MethodPost, path, body, token)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("%s status = %d, body=%s", path, rr.Code, rr.Body.String())
	}
}

func readUpstreamActivityHandlerSummary(t *testing.T, app *App, serverID, scope string) UpstreamActivitySummary {
	t.Helper()
	summary, _, err := app.UpstreamActivity.Read(serverID, scope)
	if err != nil {
		t.Fatalf("read activity summary: %v", err)
	}
	return summary
}

func requireUpstreamActivitySummaryTime(t *testing.T, summary UpstreamActivitySummary, activity, qualified *int64) {
	t.Helper()
	if activity == nil || summary.LastActivityAt == nil || *summary.LastActivityAt != *activity {
		t.Fatalf("lastActivityAt = %v, want %v", summary.LastActivityAt, activity)
	}
	if qualified == nil || summary.LastQualifiedAt == nil || *summary.LastQualifiedAt != *qualified {
		t.Fatalf("lastQualifiedAt = %v, want %v", summary.LastQualifiedAt, qualified)
	}
}

// TestUpstreamActivityHandlerConfirmsOnly2xx drives Started/Progress through
// the real handlers: confirmed 2xx reports refresh the summary, while an
// upstream 4xx and a transport-unknown failure do not.
func TestUpstreamActivityHandlerConfirmsOnly2xx(t *testing.T) {
	upstream := newUpstreamActivityHandlerUpstream(t)
	withTempAppConfig(t, singleUpstreamConfig(upstream.server.URL), func(app *App, handler http.Handler) {
		clock := newFakeUpstreamActivityClock()
		app.activityTracker.now = func() time.Time { return clock.now }
		token := loginToken(t, handler, "secret")
		serverID, scope := upstreamActivityHandlerTarget(t, app)
		itemID := app.IDStore.GetOrCreateVirtualID("handler-item", serverID)
		playSessionID := app.IDStore.GetOrCreateVirtualID("handler-play", serverID)

		body := upstreamActivityHandlerBody(itemID, playSessionID, "session-1", 0)
		doUpstreamActivitySessionEvent(t, handler, token, "/Sessions/Playing", body)
		summary := readUpstreamActivityHandlerSummary(t, app, serverID, scope)
		if summary.LastActivityAt == nil {
			t.Fatal("confirmed Started did not refresh activity")
		}

		clock.advance(30 * time.Second)
		body = upstreamActivityHandlerBody(itemID, playSessionID, "session-1", 30)
		doUpstreamActivitySessionEvent(t, handler, token, "/Sessions/Playing/Progress", body)
		clock.advance(30 * time.Second)
		body = upstreamActivityHandlerBody(itemID, playSessionID, "session-1", 60)
		doUpstreamActivitySessionEvent(t, handler, token, "/Sessions/Playing/Progress", body)

		summary = readUpstreamActivityHandlerSummary(t, app, serverID, scope)
		if summary.LastActivityAt == nil || summary.LastQualifiedAt == nil {
			t.Fatalf("confirmed Progress did not qualify the session: %#v", summary)
		}
		activity, qualified := *summary.LastActivityAt, *summary.LastQualifiedAt

		// An explicit 4xx refusal is not a confirmed report.
		upstream.progressStatus.Store(http.StatusBadRequest)
		clock.advance(2 * time.Minute)
		body = upstreamActivityHandlerBody(itemID, playSessionID, "session-1", 180)
		doUpstreamActivitySessionEvent(t, handler, token, "/Sessions/Playing/Progress", body)
		requireUpstreamActivitySummaryTime(t, readUpstreamActivityHandlerSummary(t, app, serverID, scope), &activity, &qualified)

		// A dropped connection has an unknown outcome, not a confirmation.
		upstream.progressStatus.Store(-1)
		clock.advance(2 * time.Minute)
		body = upstreamActivityHandlerBody(itemID, playSessionID, "session-1", 300)
		doUpstreamActivitySessionEvent(t, handler, token, "/Sessions/Playing/Progress", body)
		requireUpstreamActivitySummaryTime(t, readUpstreamActivityHandlerSummary(t, app, serverID, scope), &activity, &qualified)
	})
}

// TestUpstreamActivityHandlerFailedReportResetsBaseline makes the unconfirmed
// gap count as a hole: a failed/unknown report between two confirmed samples
// drops the old baseline, so the next confirmed sample cannot credit the
// interval that was never confirmed. Already credited time is kept.
func TestUpstreamActivityHandlerFailedReportResetsBaseline(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int32
	}{
		{name: "4xx", status: http.StatusBadRequest},
		{name: "transport-unknown", status: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := newUpstreamActivityHandlerUpstream(t)
			withTempAppConfig(t, singleUpstreamConfig(upstream.server.URL), func(app *App, handler http.Handler) {
				clock := newFakeUpstreamActivityClock()
				app.activityTracker.now = func() time.Time { return clock.now }
				token := loginToken(t, handler, "secret")
				serverID, scope := upstreamActivityHandlerTarget(t, app)
				itemID := app.IDStore.GetOrCreateVirtualID("handler-item", serverID)
				playSessionID := app.IDStore.GetOrCreateVirtualID("handler-play", serverID)

				body := upstreamActivityHandlerBody(itemID, playSessionID, "session-1", 0)
				doUpstreamActivitySessionEvent(t, handler, token, "/Sessions/Playing", body)
				clock.advance(30 * time.Second)
				body = upstreamActivityHandlerBody(itemID, playSessionID, "session-1", 30)
				doUpstreamActivitySessionEvent(t, handler, token, "/Sessions/Playing/Progress", body)

				// 30s are credited; the next sample fails at t=50s.
				upstream.progressStatus.Store(tc.status)
				clock.advance(20 * time.Second)
				body = upstreamActivityHandlerBody(itemID, playSessionID, "session-1", 50)
				doUpstreamActivitySessionEvent(t, handler, token, "/Sessions/Playing/Progress", body)

				// A confirmed sample at t=60s must rebuild the baseline instead of
				// adding the 10s since the failed sample (which would reach 60s).
				upstream.progressStatus.Store(http.StatusNoContent)
				clock.advance(10 * time.Second)
				body = upstreamActivityHandlerBody(itemID, playSessionID, "session-1", 60)
				doUpstreamActivitySessionEvent(t, handler, token, "/Sessions/Playing/Progress", body)
				if summary := readUpstreamActivityHandlerSummary(t, app, serverID, scope); summary.LastQualifiedAt != nil {
					t.Fatalf("unconfirmed interval qualified the session: %#v", summary)
				}

				// Another confirmed 30s of real playback reaches the threshold.
				clock.advance(30 * time.Second)
				body = upstreamActivityHandlerBody(itemID, playSessionID, "session-1", 90)
				doUpstreamActivitySessionEvent(t, handler, token, "/Sessions/Playing/Progress", body)
				if summary := readUpstreamActivityHandlerSummary(t, app, serverID, scope); summary.LastQualifiedAt == nil {
					t.Fatal("confirmed playback after the gap did not qualify the session")
				}
			})
		})
	}
}

// TestUpstreamActivityHandlerRejectsSupersededSession proves the stale-session
// decision at the handler entry: progress carrying an older SessionId cannot
// extend the statistics of the live session.
func TestUpstreamActivityHandlerRejectsSupersededSession(t *testing.T) {
	upstream := newUpstreamActivityHandlerUpstream(t)
	withTempAppConfig(t, singleUpstreamConfig(upstream.server.URL), func(app *App, handler http.Handler) {
		clock := newFakeUpstreamActivityClock()
		app.activityTracker.now = func() time.Time { return clock.now }
		token := loginToken(t, handler, "secret")
		serverID, scope := upstreamActivityHandlerTarget(t, app)
		itemID := app.IDStore.GetOrCreateVirtualID("handler-item", serverID)
		oldPlay := app.IDStore.GetOrCreateVirtualID("handler-play-old", serverID)
		newPlay := app.IDStore.GetOrCreateVirtualID("handler-play-new", serverID)

		body := upstreamActivityHandlerBody(itemID, oldPlay, "session-old", 0)
		doUpstreamActivitySessionEvent(t, handler, token, "/Sessions/Playing", body)
		clock.advance(60 * time.Second)
		body = upstreamActivityHandlerBody(itemID, oldPlay, "session-old", 60)
		doUpstreamActivitySessionEvent(t, handler, token, "/Sessions/Playing/Progress", body)

		summary := readUpstreamActivityHandlerSummary(t, app, serverID, scope)
		if summary.LastActivityAt == nil || summary.LastQualifiedAt == nil {
			t.Fatalf("old session did not qualify before supersession: %#v", summary)
		}
		activity, qualified := *summary.LastActivityAt, *summary.LastQualifiedAt

		body = upstreamActivityHandlerBody(itemID, newPlay, "session-new", 0)
		doUpstreamActivitySessionEvent(t, handler, token, "/Sessions/Playing", body)
		clock.advance(60 * time.Second)
		body = upstreamActivityHandlerBody(itemID, oldPlay, "session-old", 120)
		doUpstreamActivitySessionEvent(t, handler, token, "/Sessions/Playing/Progress", body)
		requireUpstreamActivitySummaryTime(t, readUpstreamActivityHandlerSummary(t, app, serverID, scope), &activity, &qualified)

		// The live session can still update the summary: the stale rejection is
		// not a blanket freeze.
		clock.advance(60 * time.Second)
		body = upstreamActivityHandlerBody(itemID, newPlay, "session-new", 60)
		doUpstreamActivitySessionEvent(t, handler, token, "/Sessions/Playing/Progress", body)
		updated := readUpstreamActivityHandlerSummary(t, app, serverID, scope)
		if updated.LastActivityAt == nil || *updated.LastActivityAt == activity {
			t.Fatalf("live session did not refresh activity: %#v", updated)
		}
	})
}

// TestUpstreamActivityHandlerAccountSwitchDuringRequest keeps a confirmed
// report from being written when the configured upstream account changes while
// the request is in flight.
func TestUpstreamActivityHandlerAccountSwitchDuringRequest(t *testing.T) {
	upstream := newUpstreamActivityHandlerUpstream(t)
	withTempAppConfig(t, singleUpstreamConfig(upstream.server.URL), func(app *App, handler http.Handler) {
		clock := newFakeUpstreamActivityClock()
		app.activityTracker.now = func() time.Time { return clock.now }
		token := loginToken(t, handler, "secret")
		serverID, oldScope := upstreamActivityHandlerTarget(t, app)
		itemID := app.IDStore.GetOrCreateVirtualID("handler-item", serverID)
		playSessionID := app.IDStore.GetOrCreateVirtualID("handler-play", serverID)

		upstream.setStartedHook(func() {
			cfg := app.ConfigStore.Snapshot()
			if len(cfg.Upstream) == 0 {
				return
			}
			cfg.Upstream[0].APIKey = "rotated-handler-key"
			app.ConfigStore.Replace(cfg)
		})

		body := upstreamActivityHandlerBody(itemID, playSessionID, "session-1", 0)
		doUpstreamActivitySessionEvent(t, handler, token, "/Sessions/Playing", body)

		if summary := readUpstreamActivityHandlerSummary(t, app, serverID, oldScope); summary.LastActivityAt != nil || summary.LastQualifiedAt != nil {
			t.Fatalf("old account received activity after a switch: %#v", summary)
		}
		if got := app.activityTracker.sessionCount(); got != 0 {
			t.Fatalf("tracker sessions after a discarded account switch = %d, want 0", got)
		}
		cfg := app.ConfigStore.Snapshot()
		if len(cfg.Upstream) == 0 {
			t.Fatal("config lost its upstream")
		}
		if newScope := UpstreamAccountScope(&cfg.Upstream[0]); newScope == oldScope {
			t.Fatal("rotated API key did not change the account scope")
		}
	})
}

// TestUpstreamActivityDeleteDoesNotClearForeignFailure guards the per-server
// store-health state: deleting one node must not clear another node's failure.
func TestUpstreamActivityDeleteDoesNotClearForeignFailure(t *testing.T) {
	f := newUpstreamActivityEventFixture(t)
	f.app.activityHealth.markFailed("other-server", upstreamActivityHealthSummary, errors.New("activity database failure"))

	f.app.purgeUpstreamActivityServer(f.upstream.ID)

	if got := f.app.activityHealth.serverAvailability(true, "other-server"); got.Available || got.Message == "" {
		t.Fatalf("delete success cleared a foreign failure: %#v", got)
	}
	if got := f.app.activityHealth.serverAvailability(true, f.upstream.ID); !got.Available {
		t.Fatalf("delete success left the deleted node marked failed: %#v", got)
	}
}

// TestUpstreamActivityHookWritesConfirmedQualification: Started+two regular
// progress reports across the default 60s threshold persist the observation.
func TestUpstreamActivityHookWritesConfirmedQualification(t *testing.T) {
	f := newUpstreamActivityEventFixture(t)
	verdict := upstreamActivityAllowedVerdict()

	req, event := f.begin("user", upstreamActivityEventBody("ps-1", "", 0))
	if event.Scope == "" || event.Token.Seq == 0 {
		t.Fatalf("captured event = %#v, want scope and receipt sequence", event)
	}
	f.record(req, event, upstreamActivityEventBody("ps-1", "", 0), playbackEventStarted, true, verdict)

	f.clock.advance(30 * time.Second)
	req, event = f.begin("user", upstreamActivityEventBody("ps-1", "", 30))
	f.record(req, event, upstreamActivityEventBody("ps-1", "", 30), playbackEventProgress, true, verdict)

	f.clock.advance(30 * time.Second)
	qualifiedAt := f.clock.now
	req, event = f.begin("user", upstreamActivityEventBody("ps-1", "", 60))
	f.record(req, event, upstreamActivityEventBody("ps-1", "", 60), playbackEventProgress, true, verdict)

	summary, found := f.summary(t, f.scope())
	if !found {
		t.Fatal("summary missing after a confirmed report")
	}
	if summary.LastActivityAt == nil || *summary.LastActivityAt != qualifiedAt.UTC().UnixMilli() {
		t.Fatalf("lastActivityAt = %v, want %d", summary.LastActivityAt, qualifiedAt.UTC().UnixMilli())
	}
	if summary.LastQualifiedAt == nil || *summary.LastQualifiedAt != qualifiedAt.UTC().UnixMilli() {
		t.Fatalf("lastQualifiedAt = %v, want %d", summary.LastQualifiedAt, qualifiedAt.UTC().UnixMilli())
	}
}

// TestUpstreamActivityHookIgnoresUnconfirmedOrRejected: an unknown/refused
// outcome and a limiter-rejected attempt never create statistics.
func TestUpstreamActivityHookIgnoresUnconfirmedOrRejected(t *testing.T) {
	f := newUpstreamActivityEventFixture(t)

	req, event := f.begin("user", upstreamActivityEventBody("ps-1", "", 0))
	// Transport unknown / 4xx: confirmed stays false in the handler.
	f.record(req, event, upstreamActivityEventBody("ps-1", "", 0), playbackEventStarted, false, upstreamActivityAllowedVerdict())
	if summary, _ := f.summary(t, f.scope()); summary.LastActivityAt != nil || summary.LastQualifiedAt != nil {
		t.Fatalf("unconfirmed report was counted: %#v", summary)
	}

	req, event = f.begin("user", upstreamActivityEventBody("ps-2", "", 0))
	f.record(req, event, upstreamActivityEventBody("ps-2", "", 0), playbackEventStarted, true, playbackSessionVerdict{Tracked: true, Accepted: false})
	if summary, _ := f.summary(t, f.scope()); summary.LastActivityAt != nil {
		t.Fatalf("limiter-rejected report was counted: %#v", summary)
	}
	if got := f.app.activityTracker.sessionCount(); got != 0 {
		t.Fatalf("tracker sessions = %d, want 0", got)
	}
}

// TestUpstreamActivityHookRejectsStaleSession: when no unit is tracked, the
// session-event tracker still decides whether the event is stale.
func TestUpstreamActivityHookRejectsStaleSessionEvent(t *testing.T) {
	f := newUpstreamActivityEventFixture(t)
	f.app.sessionEvents.StartSession("user", "item-1", "session-new")

	body := upstreamActivityEventBody("ps-old", "session-old", 0)
	req, event := f.begin("user", body)
	if f.app.upstreamActivityEventAllowed(req, "item-1", body, playbackEventProgress, playbackSessionVerdict{LocalProgress: true}) {
		t.Fatal("superseded session event must not be allowed")
	}
	f.record(req, event, body, playbackEventProgress, true, playbackSessionVerdict{LocalProgress: true})
	if summary, _ := f.summary(t, f.scope()); summary.LastActivityAt != nil {
		t.Fatalf("superseded session was counted: %#v", summary)
	}
}

// TestUpstreamActivityHookDiscardsClientThatNoLongerMatchesConfig: a request
// carrying an old client is not attributed to the account in the config.
func TestUpstreamActivityHookDiscardsClientThatNoLongerMatchesConfig(t *testing.T) {
	f := newUpstreamActivityEventFixture(t)
	oldClient := f.client
	updated := f.upstream
	updated.APIKey = "rotated-key"
	f.configStore.Replace(Config{Upstream: []UpstreamConfig{updated}})

	body := upstreamActivityEventBody("ps-1", "", 0)
	_, event := f.begin("user", body)
	if event.Scope != "" || event.Token.Seq != 0 {
		t.Fatalf("stale client captured an event: %#v", event)
	}
	if got := f.app.activityTracker.sessionCount(); got != 0 {
		t.Fatalf("tracker sessions = %d, want 0", got)
	}

	// Switching to the current client works again and uses the new scope.
	f.client = newUpstreamClient(Config{Upstream: []UpstreamConfig{updated}}, updated, 0, nil)
	_, event = f.begin("user", body)
	if event.Scope == "" || event.Scope == UpstreamAccountScope(&oldClient.Config) {
		t.Fatalf("event scope = %q, want the rotated account scope", event.Scope)
	}
}

// TestUpstreamActivityHookDiscardsAccountChangeDuringRequest: the scope is
// captured before the request; changing the account before the success hook
// must not write the old observation into the new account.
func TestUpstreamActivityHookDiscardsAccountChangeDuringRequest(t *testing.T) {
	f := newUpstreamActivityEventFixture(t)
	oldScope := f.scope()
	body := upstreamActivityEventBody("ps-1", "", 0)
	req, event := f.begin("user", body)
	if event.Scope != oldScope {
		t.Fatalf("captured scope = %q, want %q", event.Scope, oldScope)
	}

	updated := f.upstream
	updated.URL = "http://other-upstream.example"
	f.configStore.Replace(Config{Upstream: []UpstreamConfig{updated}})

	f.record(req, event, body, playbackEventStarted, true, upstreamActivityAllowedVerdict())
	if summary, _ := f.summary(t, oldScope); summary.LastActivityAt != nil {
		t.Fatalf("old-account observation was written after an account change: %#v", summary)
	}
	if got := f.app.activityTracker.sessionCount(); got != 0 {
		t.Fatalf("tracker sessions = %d, want 0", got)
	}
}

// TestUpstreamActivityAvailabilityAndFailureRecovery: a missing store is
// unavailable, a failed write reports unavailable without marking the sample
// flushed, and a later success clears the state.
func TestUpstreamActivityAvailabilityAndFailureRecovery(t *testing.T) {
	if got := (&App{}).UpstreamActivityAvailability(); got.Available {
		t.Fatalf("App without store reported available: %#v", got)
	}

	f := newUpstreamActivityEventFixture(t)
	verdict := upstreamActivityAllowedVerdict()
	body := upstreamActivityEventBody("ps-1", "", 0)
	req, event := f.begin("user", body)
	f.record(req, event, body, playbackEventStarted, true, verdict)

	f.clock.advance(30 * time.Second)
	body = upstreamActivityEventBody("ps-1", "", 30)
	req, event = f.begin("user", body)
	f.record(req, event, body, playbackEventProgress, true, verdict)

	f.clock.advance(30 * time.Second)
	body = upstreamActivityEventBody("ps-1", "", 60)
	req, event = f.begin("user", body)
	f.record(req, event, body, playbackEventProgress, true, verdict)
	persisted := f.persistedQualifiedAt(t, "ps-1")
	if persisted.IsZero() {
		t.Fatal("qualified time was not marked persisted after a successful write")
	}

	// Break the shared connection: the next refresh must fail without being
	// remembered as flushed.
	if err := closeSQLite(f.db); err != nil {
		t.Fatalf("close sqlite: %v", err)
	}
	f.clock.advance(60 * time.Second)
	body = upstreamActivityEventBody("ps-1", "", 120)
	req, event = f.begin("user", body)
	f.record(req, event, body, playbackEventProgress, true, verdict)
	if got := f.persistedQualifiedAt(t, "ps-1"); !got.Equal(persisted) {
		t.Fatalf("failed write advanced the persisted marker: %v -> %v", persisted, got)
	}
	if got := f.app.UpstreamActivityAvailability(); got.Available || got.Message == "" {
		t.Fatalf("failed write availability = %#v, want unavailable with a message", got)
	}

	// A working store with the same scope recovers the availability state.
	freshStore, _, _ := newUpstreamActivityTestStore(t)
	if _, err := freshStore.EnsureScope(f.upstream.ID, f.scope(), time.Now().UnixMilli()); err != nil {
		t.Fatalf("EnsureScope on recovery store: %v", err)
	}
	f.app.UpstreamActivity = freshStore
	f.clock.advance(60 * time.Second)
	body = upstreamActivityEventBody("ps-1", "", 180)
	req, event = f.begin("user", body)
	f.record(req, event, body, playbackEventProgress, true, verdict)
	if got := f.app.UpstreamActivityAvailability(); !got.Available {
		t.Fatalf("successful write did not clear availability: %#v", got)
	}
}

// TestUpstreamActivityPurgeRemovesEverything: deleting a node removes the
// summary, the policy, the scope cache and every in-memory session.
func TestUpstreamActivityPurgeRemovesEverything(t *testing.T) {
	f := newUpstreamActivityEventFixture(t)
	body := upstreamActivityEventBody("ps-1", "", 0)
	req, event := f.begin("user", body)
	f.record(req, event, body, playbackEventStarted, true, upstreamActivityAllowedVerdict())
	if summary, found := f.summary(t, f.scope()); !found || summary.LastActivityAt == nil {
		t.Fatalf("expected stored activity before delete: %#v found=%v", summary, found)
	}
	if _, ok := f.app.activityScopes.get(f.upstream.ID); !ok {
		t.Fatal("scope should have been ensured")
	}

	f.app.purgeUpstreamActivityServer(f.upstream.ID)

	if _, found := f.summary(t, f.scope()); found {
		t.Fatal("summary survived node deletion")
	}
	if policy, err := f.store.GetPolicy(f.upstream.ID); err != nil || policy.Revision != 0 {
		t.Fatalf("policy survived node deletion: %#v err=%v", policy, err)
	}
	if got := f.app.activityTracker.sessionCount(); got != 0 {
		t.Fatalf("tracker sessions after purge = %d, want 0", got)
	}
	if _, ok := f.app.activityScopes.get(f.upstream.ID); ok {
		t.Fatal("scope cache survived node deletion")
	}
}

// TestUpstreamActivityPausedStopDoesNotExtendInHook keeps the hook honest: a
// paused Stop writes activity but never a qualified timestamp beyond evidence.
func TestUpstreamActivityPausedStopDoesNotExtendInHook(t *testing.T) {
	f := newUpstreamActivityEventFixture(t)
	verdict := upstreamActivityAllowedVerdict()
	body := upstreamActivityEventBody("ps-1", "", 0)
	req, event := f.begin("user", body)
	f.record(req, event, body, playbackEventStarted, true, verdict)

	f.clock.advance(60 * time.Second)
	body = upstreamActivityEventBody("ps-1", "", 60)
	req, event = f.begin("user", body)
	f.record(req, event, body, playbackEventProgress, true, verdict)
	trackerKey := upstreamActivityTestScopedKey("user", f.upstream.ID, f.scope(), "ps-1")
	f.app.activityTracker.mu.Lock()
	session := f.app.activityTracker.sessions[trackerKey]
	var persisted time.Time
	if session != nil {
		persisted = session.persistedQualifiedAt
	}
	f.app.activityTracker.mu.Unlock()
	if persisted.IsZero() {
		t.Fatal("session did not qualify")
	}

	f.clock.advance(10 * time.Minute)
	body = upstreamActivityEventBody("ps-1", "", 60)
	body["IsPaused"] = true
	req, event = f.begin("user", body)
	f.record(req, event, body, playbackEventStopped, true, verdict)

	if summary, _ := f.summary(t, f.scope()); summary.LastQualifiedAt == nil || *summary.LastQualifiedAt != persisted.UTC().UnixMilli() {
		t.Fatalf("paused Stop extended lastQualifiedAt: %#v (persisted %d)", summary.LastQualifiedAt, persisted.UTC().UnixMilli())
	}
	if got := f.app.activityTracker.sessionCount(); got != 1 {
		// The stopped session stays as a bounded tombstone until TTL eviction.
		t.Fatalf("stopped session count = %d, want 1", got)
	}
}

// TestUpstreamActivityHookSkipsOnPolicyReadError: an unreadable threshold never
// falls back to 60s (which could qualify a session configured for 3600s); the
// observation is skipped and the store state is reported unavailable.
func TestUpstreamActivityHookSkipsOnPolicyReadError(t *testing.T) {
	f := newUpstreamActivityEventFixture(t)
	if err := closeSQLite(f.db); err != nil {
		t.Fatalf("close sqlite: %v", err)
	}
	body := upstreamActivityEventBody("ps-1", "", 0)
	req, event := f.begin("user", body)
	if event.Scope == "" {
		t.Fatal("scope capture must not require the database")
	}
	f.record(req, event, body, playbackEventStarted, true, upstreamActivityAllowedVerdict())

	if got := f.app.activityTracker.sessionCount(); got != 0 {
		t.Fatalf("tracker sessions after a policy failure = %d, want 0", got)
	}
	availability := f.app.UpstreamActivityAvailability()
	if availability.Available || availability.Message == "" {
		t.Fatalf("policy failure availability = %#v, want unavailable", availability)
	}
}
