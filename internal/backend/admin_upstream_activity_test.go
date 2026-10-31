package backend

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// K3 admin API tests drive the real routes through httptest and a real SQLite
// store in t.TempDir. The upstreams are local httptest stubs; the activity
// handlers must never contact them.

type adminUpstreamActivityFixture struct {
	app     *App
	handler http.Handler
	token   string
	serverA *aggUpstreamStub
	serverB *aggUpstreamStub
}

func adminUpstreamActivityTestConfig(urlA, urlB string) string {
	return fmt.Sprintf(`server:
  port: 8096
  name: "Test Server"
  id: "server-1"

admin:
  username: "admin"
  password: "secret"

playback:
  mode: "proxy"

timeouts:
  api: 30000
  global: 15000
  login: 10000
  healthCheck: 10000
  healthInterval: 600000

proxies: []
upstream:
  - id: "srv-a"
    name: "A"
    url: %q
    username: "alice"
    password: "alice-pass"
  - id: "srv-b"
    name: "B"
    url: %q
    username: "bob"
    password: "bob-pass"
`, urlA, urlB)
}

func newAdminUpstreamActivityFixture(t *testing.T) *adminUpstreamActivityFixture {
	t.Helper()
	serverA := newAggUpstreamStub(t)
	serverB := newAggUpstreamStub(t)
	dir := prepareTempWorkspace(t, adminUpstreamActivityTestConfig(serverA.server.URL, serverB.server.URL), nil)
	chdirForTest(t, dir)
	app, handler := newTestApp(t)
	if app.UpstreamActivity == nil {
		t.Fatal("activity store missing from a live App")
	}
	token := loginTokenAs(t, handler, "admin", "secret")
	serverA.reset()
	serverB.reset()
	return &adminUpstreamActivityFixture{
		app:     app,
		handler: handler,
		token:   token,
		serverA: serverA,
		serverB: serverB,
	}
}

func (f *adminUpstreamActivityFixture) activityPath(serverID string) string {
	return "/admin/api/upstream/" + serverID + "/activity"
}

func (f *adminUpstreamActivityFixture) scope(t *testing.T, serverID string) string {
	t.Helper()
	cfg := f.app.ConfigStore.Snapshot()
	for i := range cfg.Upstream {
		if cfg.Upstream[i].ID == serverID {
			return UpstreamAccountScope(&cfg.Upstream[i])
		}
	}
	t.Fatalf("upstream %q not found in config", serverID)
	return ""
}

func (f *adminUpstreamActivityFixture) getActivity(t *testing.T, serverID string) (int, adminUpstreamActivityDTO) {
	t.Helper()
	rr := doJSONRequest(t, f.handler, http.MethodGet, f.activityPath(serverID), nil, f.token)
	if rr.Code != http.StatusOK {
		return rr.Code, adminUpstreamActivityDTO{}
	}
	var dto adminUpstreamActivityDTO
	if err := json.Unmarshal(rr.Body.Bytes(), &dto); err != nil {
		t.Fatalf("unmarshal activity response: %v (body=%s)", err, rr.Body.String())
	}
	return rr.Code, dto
}

func (f *adminUpstreamActivityFixture) putActivity(t *testing.T, serverID string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return doJSONRequest(t, f.handler, http.MethodPut, f.activityPath(serverID), body, f.token)
}

func decodeAdminUpstreamActivityResponse(t *testing.T, rr *httptest.ResponseRecorder) adminUpstreamActivityDTO {
	t.Helper()
	var dto adminUpstreamActivityDTO
	if err := json.Unmarshal(rr.Body.Bytes(), &dto); err != nil {
		t.Fatalf("unmarshal activity response: %v (body=%s)", err, rr.Body.String())
	}
	return dto
}

func adminUpstreamActivityBody(revision int64) map[string]any {
	return map[string]any{
		"enabled":           true,
		"intervalUnit":      "days",
		"intervalValue":     30,
		"minWatchSeconds":   60,
		"manualReferenceAt": nil,
		"expectedRevision":  revision,
	}
}

func adminUpstreamActivityCloneBody(body map[string]any) map[string]any {
	clone := make(map[string]any, len(body))
	for key, value := range body {
		clone[key] = value
	}
	return clone
}

func adminUpstreamActivityTime(t *testing.T, label, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("%s = %q is not RFC3339: %v", label, value, err)
	}
	return parsed
}

func adminUpstreamActivityFindEntry(t *testing.T, rr *httptest.ResponseRecorder, serverID string) map[string]any {
	t.Helper()
	var list []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal upstream list: %v (body=%s)", err, rr.Body.String())
	}
	for _, entry := range list {
		if entry["id"] == serverID {
			return entry
		}
	}
	t.Fatalf("upstream %q not found in list: %s", serverID, rr.Body.String())
	return nil
}

func adminUpstreamActivityObject(t *testing.T, entry map[string]any) map[string]any {
	t.Helper()
	activity, ok := entry["activity"].(map[string]any)
	if !ok || activity == nil {
		t.Fatalf("list entry has no activity object: %#v", entry)
	}
	return activity
}

func TestAdminUpstreamActivityAuthorizationAndNotFound(t *testing.T) {
	f := newAdminUpstreamActivityFixture(t)
	path := f.activityPath("srv-a")
	body := adminUpstreamActivityBody(1)

	for _, request := range []struct {
		method string
		body   any
	}{
		{http.MethodGet, nil},
		{http.MethodPut, body},
	} {
		rr := doJSONRequest(t, f.handler, request.method, path, request.body, "")
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("%s without token = %d, want 401", request.method, rr.Code)
		}
	}

	create := doJSONRequest(t, f.handler, http.MethodPost, "/admin/api/users", map[string]any{
		"username": "regular", "password": "password123",
	}, f.token)
	if create.Code != http.StatusCreated {
		t.Fatalf("create regular user = %d, body=%s", create.Code, create.Body.String())
	}
	userToken := loginTokenAs(t, f.handler, "regular", "password123")
	for _, request := range []struct {
		method string
		body   any
	}{
		{http.MethodGet, nil},
		{http.MethodPut, body},
	} {
		rr := doJSONRequest(t, f.handler, request.method, path, request.body, userToken)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("%s as regular user = %d, want 403", request.method, rr.Code)
		}
	}

	if rr := doJSONRequest(t, f.handler, http.MethodGet, f.activityPath("missing"), nil, f.token); rr.Code != http.StatusNotFound {
		t.Fatalf("GET unknown activity = %d, want 404", rr.Code)
	}
	if rr := f.putActivity(t, "missing", body); rr.Code != http.StatusNotFound {
		t.Fatalf("PUT unknown activity = %d, want 404", rr.Code)
	}
}

func TestAdminUpstreamActivityGetPutRoundTrip(t *testing.T) {
	f := newAdminUpstreamActivityFixture(t)
	const serverID = "srv-a"
	f.serverA.reset()
	f.serverB.reset()

	status, initial := f.getActivity(t, serverID)
	if status != http.StatusOK {
		t.Fatalf("GET activity = %d", status)
	}
	if !initial.Available || initial.State != UpstreamActivityStateDisabled {
		t.Fatalf("initial view = %+v, want available disabled", initial)
	}
	if initial.Revision <= 0 || initial.Revision > UpstreamActivityMaxRevision {
		t.Fatalf("initial revision %d outside JS-safe range", initial.Revision)
	}
	if initial.LastActivityAt != nil || initial.LastQualifiedAt != nil || initial.ManualReferenceAt != nil ||
		initial.ReferenceAt != nil || initial.DueAt != nil || initial.RemainingRatio != nil {
		t.Fatalf("empty summary must serialize every time as null: %+v", initial)
	}
	if initial.Enabled || initial.IntervalUnit != UpstreamActivityUnitDays || initial.IntervalValue != 30 || initial.MinWatchSeconds != 60 {
		t.Fatalf("initial policy not documented defaults: %+v", initial)
	}
	adminUpstreamActivityTime(t, "serverNow", initial.ServerNow)

	manual := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	body := adminUpstreamActivityBody(initial.Revision)
	body["manualReferenceAt"] = manual.Format(time.RFC3339)

	// Seed the K2 threshold cache so the save is proven to invalidate it.
	f.app.activityPolicies.store(serverID, time.Minute, nil, time.Now())
	rr := f.putActivity(t, serverID, body)
	if rr.Code != http.StatusOK {
		t.Fatalf("PUT activity = %d, body=%s", rr.Code, rr.Body.String())
	}
	saved := decodeAdminUpstreamActivityResponse(t, rr)
	if !saved.Available || !saved.Enabled || saved.State != UpstreamActivityStateNormal {
		t.Fatalf("saved view = %+v, want available normal", saved)
	}
	if saved.Revision == initial.Revision || saved.Revision <= 0 || saved.Revision > UpstreamActivityMaxRevision {
		t.Fatalf("saved revision = %d, initial = %d", saved.Revision, initial.Revision)
	}
	if saved.ManualReferenceAt == nil || saved.ReferenceAt == nil || saved.DueAt == nil || saved.RemainingRatio == nil {
		t.Fatalf("saved view missing manual schedule: %+v", saved)
	}
	if got := adminUpstreamActivityTime(t, "manualReferenceAt", *saved.ManualReferenceAt); !got.Equal(manual) {
		t.Fatalf("manualReferenceAt = %s, want %s", got, manual)
	}
	if saved.ReferenceSource != UpstreamActivityReferenceManual {
		t.Fatalf("referenceSource = %q, want manual", saved.ReferenceSource)
	}
	adminUpstreamActivityTime(t, "dueAt", *saved.DueAt)
	adminUpstreamActivityTime(t, "serverNow", saved.ServerNow)

	f.app.activityPolicies.mu.Lock()
	_, cached := f.app.activityPolicies.entries[serverID]
	f.app.activityPolicies.mu.Unlock()
	if cached {
		t.Fatal("saving a policy must invalidate the cached min-watch threshold")
	}

	stored, err := f.app.UpstreamActivity.GetPolicy(serverID)
	if err != nil {
		t.Fatalf("GetPolicy after save: %v", err)
	}
	if stored.Revision != saved.Revision || !stored.Enabled || stored.ManualReferenceAt == nil {
		t.Fatalf("stored policy does not match response: %+v", stored)
	}
	if !SameUpstreamAccountScope(stored.ManualScope, f.scope(t, serverID)) {
		t.Fatal("manual reference was not bound to the current account scope")
	}

	// The old revision is rejected instead of overwriting the newer policy.
	if rr := f.putActivity(t, serverID, body); rr.Code != http.StatusConflict {
		t.Fatalf("stale revision PUT = %d, want 409, body=%s", rr.Code, rr.Body.String())
	}
	_, afterConflict := f.getActivity(t, serverID)
	if !afterConflict.Available || afterConflict.Revision != saved.Revision {
		t.Fatalf("stale form made the node unavailable or changed its revision: %+v", afterConflict)
	}

	// A complete PUT with manualReferenceAt:null clears the manual baseline.
	clear := adminUpstreamActivityBody(saved.Revision)
	rr = f.putActivity(t, serverID, clear)
	if rr.Code != http.StatusOK {
		t.Fatalf("clear manual PUT = %d, body=%s", rr.Code, rr.Body.String())
	}
	cleared := decodeAdminUpstreamActivityResponse(t, rr)
	if cleared.ManualReferenceAt != nil || cleared.ReferenceAt != nil || cleared.State != UpstreamActivityStateUnknown {
		t.Fatalf("manual reference was not cleared: %+v", cleared)
	}

	if got := f.serverA.loginHits.Load() + f.serverA.contentHits.Load() + f.serverB.loginHits.Load() + f.serverB.contentHits.Load(); got != 0 {
		t.Fatalf("activity GET/PUT made %d upstream request(s), want 0", got)
	}
}

func TestAdminUpstreamActivityRejectsStrictJSON(t *testing.T) {
	f := newAdminUpstreamActivityFixture(t)
	const serverID = "srv-a"
	_, initial := f.getActivity(t, serverID)
	base := adminUpstreamActivityBody(initial.Revision)
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)

	without := func(key string) map[string]any {
		body := adminUpstreamActivityCloneBody(base)
		delete(body, key)
		return body
	}
	with := func(key string, value any) map[string]any {
		body := adminUpstreamActivityCloneBody(base)
		body[key] = value
		return body
	}

	invalid := []struct {
		name string
		body any
	}{
		{"empty body", ""},
		{"json null", "null"},
		{"json array", "[]"},
		{"missing enabled", without("enabled")},
		{"missing intervalUnit", without("intervalUnit")},
		{"missing intervalValue", without("intervalValue")},
		{"missing minWatchSeconds", without("minWatchSeconds")},
		{"missing manualReferenceAt", without("manualReferenceAt")},
		{"missing expectedRevision", without("expectedRevision")},
		{"unknown field", with("unknown", true)},
		{"enabled wrong type", with("enabled", "yes")},
		{"enabled null", with("enabled", nil)},
		{"intervalUnit null", with("intervalUnit", nil)},
		{"unknown interval unit", with("intervalUnit", "weird-secret-value")},
		{"intervalValue fractional", with("intervalValue", 1.5)},
		{"intervalValue zero", with("intervalValue", 0)},
		{"intervalValue too large", with("intervalValue", 3651)},
		{"minWatchSeconds too small", with("minWatchSeconds", 9)},
		{"minWatchSeconds too large", with("minWatchSeconds", 3601)},
		{"manualReferenceAt wrong type", with("manualReferenceAt", 123)},
		{"manualReferenceAt bad string", with("manualReferenceAt", "secret-time")},
		{"manualReferenceAt future", with("manualReferenceAt", future)},
		{"expectedRevision wrong type", with("expectedRevision", "1")},
		{"expectedRevision fractional", with("expectedRevision", 1.5)},
		{"expectedRevision negative", with("expectedRevision", -1)},
		{"expectedRevision too large", with("expectedRevision", UpstreamActivityMaxRevision+1)},
	}
	for _, tc := range invalid {
		rr := f.putActivity(t, serverID, tc.body)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400, body=%s", tc.name, rr.Code, rr.Body.String())
		}
		if strings.Contains(rr.Body.String(), "alice-pass") ||
			strings.Contains(rr.Body.String(), "weird-secret-value") ||
			strings.Contains(rr.Body.String(), "secret-time") {
			t.Fatalf("%s: error leaked a client field value: %s", tc.name, rr.Body.String())
		}
	}

	encoded, err := json.Marshal(base)
	if err != nil {
		t.Fatalf("marshal base body: %v", err)
	}
	if rr := f.putActivity(t, serverID, string(encoded)+" {}"); rr.Code != http.StatusBadRequest {
		t.Fatalf("trailing JSON status = %d, want 400, body=%s", rr.Code, rr.Body.String())
	}
	duplicate := strings.Replace(string(encoded), `"enabled":true`, `"enabled":true,"enabled":false`, 1)
	if duplicate == string(encoded) {
		t.Fatal("failed to build a duplicate-key fixture")
	}
	if rr := f.putActivity(t, serverID, duplicate); rr.Code != http.StatusBadRequest {
		t.Fatalf("duplicate key status = %d, want 400, body=%s", rr.Code, rr.Body.String())
	}
	oversized := string(encoded) + strings.Repeat(" ", adminUpstreamActivityMaxBodyBytes)
	if rr := f.putActivity(t, serverID, oversized); rr.Code != http.StatusBadRequest {
		t.Fatalf("oversized body status = %d, want 400, body=%s", rr.Code, rr.Body.String())
	}

	_, after := f.getActivity(t, serverID)
	if after.Revision != initial.Revision {
		t.Fatalf("rejected input changed the policy revision: %d -> %d", initial.Revision, after.Revision)
	}

	// Nullable manualReferenceAt and a complete object are accepted.
	if rr := f.putActivity(t, serverID, base); rr.Code != http.StatusOK {
		t.Fatalf("valid complete PUT = %d, body=%s", rr.Code, rr.Body.String())
	}
}

func TestAdminUpstreamActivityAccountScopeSwitchInvalidatesForm(t *testing.T) {
	f := newAdminUpstreamActivityFixture(t)
	const serverID = "srv-a"
	_, initial := f.getActivity(t, serverID)
	manual := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	body := adminUpstreamActivityBody(initial.Revision)
	body["manualReferenceAt"] = manual.Format(time.RFC3339)
	rr := f.putActivity(t, serverID, body)
	if rr.Code != http.StatusOK {
		t.Fatalf("seed policy = %d, body=%s", rr.Code, rr.Body.String())
	}
	oldSaved := decodeAdminUpstreamActivityResponse(t, rr)
	oldScope := f.scope(t, serverID)

	activity := time.Now().UTC().Add(-90 * time.Minute).UnixMilli()
	qualified := time.Now().UTC().Add(-60 * time.Minute).UnixMilli()
	if _, err := f.app.UpstreamActivity.Record(UpstreamActivityRecord{
		ServerID: serverID, AccountScope: oldScope, LastActivityAt: &activity, LastQualifiedAt: &qualified,
	}, time.Now().UnixMilli()); err != nil {
		t.Fatalf("record old-scope summary: %v", err)
	}

	cfg := f.app.ConfigStore.Snapshot()
	cfg.Upstream[0].APIKey = "rotated-activity-key"
	f.app.ConfigStore.Replace(cfg)
	newScope := f.scope(t, serverID)
	if SameUpstreamAccountScope(oldScope, newScope) {
		t.Fatal("rotating the API key did not change the account scope")
	}

	_, after := f.getActivity(t, serverID)
	if after.Revision == oldSaved.Revision {
		t.Fatal("account scope change did not regenerate the policy revision")
	}
	if after.ManualReferenceAt != nil || after.LastActivityAt != nil || after.LastQualifiedAt != nil ||
		after.ReferenceAt != nil {
		t.Fatalf("old account evidence leaked into the new scope: %+v", after)
	}
	if !after.Enabled || after.IntervalUnit != UpstreamActivityUnitDays || after.IntervalValue != 30 || after.MinWatchSeconds != 60 {
		t.Fatalf("scope change must retain the cycle but clear manual evidence: %+v", after)
	}
	if after.State != UpstreamActivityStateUnknown {
		t.Fatalf("new scope without reference state = %s, want unknown", after.State)
	}

	stale := adminUpstreamActivityBody(oldSaved.Revision)
	stale["manualReferenceAt"] = manual.Format(time.RFC3339)
	if rr := f.putActivity(t, serverID, stale); rr.Code != http.StatusConflict {
		t.Fatalf("old form after scope change = %d, want 409, body=%s", rr.Code, rr.Body.String())
	}

	fresh := adminUpstreamActivityBody(after.Revision)
	fresh["manualReferenceAt"] = manual.Format(time.RFC3339)
	if rr := f.putActivity(t, serverID, fresh); rr.Code != http.StatusOK {
		t.Fatalf("new-scope form = %d, body=%s", rr.Code, rr.Body.String())
	}
	stored, err := f.app.UpstreamActivity.GetPolicy(serverID)
	if err != nil {
		t.Fatalf("GetPolicy after new-scope save: %v", err)
	}
	if !SameUpstreamAccountScope(stored.ManualScope, newScope) || stored.ManualReferenceAt == nil {
		t.Fatalf("new-scope manual reference not bound correctly: %+v", stored)
	}
}

func TestAdminUpstreamActivityReadHoldsConfigLockAcrossScopeSwitch(t *testing.T) {
	f := newAdminUpstreamActivityFixture(t)
	const serverID = "srv-a"
	_, initial := f.getActivity(t, serverID)
	manual := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	body := adminUpstreamActivityBody(initial.Revision)
	body["manualReferenceAt"] = manual.Format(time.RFC3339)
	rr := f.putActivity(t, serverID, body)
	if rr.Code != http.StatusOK {
		t.Fatalf("seed policy = %d, body=%s", rr.Code, rr.Body.String())
	}
	oldSaved := decodeAdminUpstreamActivityResponse(t, rr)

	db := f.app.IDStore.DB()
	db.mu.Lock()
	type readResult struct {
		status int
		body   []byte
	}
	result := make(chan readResult, 1)
	go func() {
		req := httptest.NewRequest(http.MethodGet, f.activityPath(serverID), nil)
		req.Header.Set("X-Emby-Token", f.token)
		recorder := httptest.NewRecorder()
		f.handler.ServeHTTP(recorder, req)
		result <- readResult{status: recorder.Code, body: recorder.Body.Bytes()}
	}()
	// Let the handler capture the old cfg/scope and block in the store read.
	time.Sleep(100 * time.Millisecond)
	cfg := f.app.ConfigStore.Snapshot()
	cfg.Upstream[0].APIKey = "rotated-during-read"
	f.app.ConfigStore.Replace(cfg)
	db.mu.Unlock()

	first := <-result
	if first.status != http.StatusOK {
		t.Fatalf("in-flight GET status = %d, body=%s", first.status, first.body)
	}
	var blocked adminUpstreamActivityDTO
	if err := json.Unmarshal(first.body, &blocked); err != nil {
		t.Fatalf("unmarshal in-flight GET: %v", err)
	}
	if blocked.Revision != oldSaved.Revision {
		t.Fatalf("in-flight GET mixed old cfg with a newer revision: got %d want %d", blocked.Revision, oldSaved.Revision)
	}
	if blocked.ManualReferenceAt == nil || blocked.ReferenceSource != UpstreamActivityReferenceManual {
		t.Fatalf("in-flight GET lost the old scope evidence: %+v", blocked)
	}

	// A later request sees the new account, clears the manual reference and
	// advances the revision; the old form must then fail closed.
	_, after := f.getActivity(t, serverID)
	if after.Revision == oldSaved.Revision || after.ManualReferenceAt != nil {
		t.Fatalf("fresh GET did not switch scope: %+v", after)
	}
	stale := adminUpstreamActivityBody(oldSaved.Revision)
	stale["manualReferenceAt"] = manual.Format(time.RFC3339)
	if rr := f.putActivity(t, serverID, stale); rr.Code != http.StatusConflict {
		t.Fatalf("old revision after blocked read = %d, want 409, body=%s", rr.Code, rr.Body.String())
	}
}

func TestAdminUpstreamActivityListIncludesBatchData(t *testing.T) {
	f := newAdminUpstreamActivityFixture(t)
	_, aInitial := f.getActivity(t, "srv-a")
	_, bInitial := f.getActivity(t, "srv-b")

	manual := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	bodyA := adminUpstreamActivityBody(aInitial.Revision)
	bodyA["manualReferenceAt"] = manual.Format(time.RFC3339)
	rr := f.putActivity(t, "srv-a", bodyA)
	if rr.Code != http.StatusOK {
		t.Fatalf("save srv-a policy = %d, body=%s", rr.Code, rr.Body.String())
	}
	savedA := decodeAdminUpstreamActivityResponse(t, rr)

	bodyB := adminUpstreamActivityBody(bInitial.Revision)
	bodyB["enabled"] = false
	bodyB["manualReferenceAt"] = nil
	rr = f.putActivity(t, "srv-b", bodyB)
	if rr.Code != http.StatusOK {
		t.Fatalf("save srv-b policy = %d, body=%s", rr.Code, rr.Body.String())
	}
	savedB := decodeAdminUpstreamActivityResponse(t, rr)

	activityA := time.Now().UTC().Add(-30 * time.Minute).UnixMilli()
	qualifiedA := time.Now().UTC().Add(-60 * time.Minute).UnixMilli()
	if _, err := f.app.UpstreamActivity.Record(UpstreamActivityRecord{
		ServerID: "srv-a", AccountScope: f.scope(t, "srv-a"), LastActivityAt: &activityA, LastQualifiedAt: &qualifiedA,
	}, time.Now().UnixMilli()); err != nil {
		t.Fatalf("record srv-a summary: %v", err)
	}
	activityB := time.Now().UTC().Add(-20 * time.Minute).UnixMilli()
	if _, err := f.app.UpstreamActivity.Record(UpstreamActivityRecord{
		ServerID: "srv-b", AccountScope: f.scope(t, "srv-b"), LastActivityAt: &activityB,
	}, time.Now().UnixMilli()); err != nil {
		t.Fatalf("record srv-b summary: %v", err)
	}

	f.serverA.reset()
	f.serverB.reset()
	listRR := doJSONRequest(t, f.handler, http.MethodGet, "/admin/api/upstream", nil, f.token)
	if listRR.Code != http.StatusOK {
		t.Fatalf("GET upstream list = %d, body=%s", listRR.Code, listRR.Body.String())
	}
	entryA := adminUpstreamActivityFindEntry(t, listRR, "srv-a")
	activityAJSON := adminUpstreamActivityObject(t, entryA)
	if activityAJSON["available"] != true || activityAJSON["state"] != UpstreamActivityStateNormal {
		t.Fatalf("srv-a activity = %#v", activityAJSON)
	}
	if int64(activityAJSON["revision"].(float64)) != savedA.Revision {
		t.Fatalf("srv-a list revision = %v, want %d", activityAJSON["revision"], savedA.Revision)
	}
	if activityAJSON["referenceSource"] != UpstreamActivityReferenceObserved {
		t.Fatalf("srv-a referenceSource = %v, want observed", activityAJSON["referenceSource"])
	}
	if activityAJSON["lastActivityAt"] == nil || activityAJSON["lastQualifiedAt"] == nil || activityAJSON["manualReferenceAt"] == nil {
		t.Fatalf("srv-a list lost manual/observed evidence: %#v", activityAJSON)
	}
	if activityAJSON["dueAt"] == nil || activityAJSON["remainingRatio"] == nil {
		t.Fatalf("srv-a list missing schedule: %#v", activityAJSON)
	}
	adminUpstreamActivityTime(t, "srv-a serverNow", activityAJSON["serverNow"].(string))
	for _, forbidden := range []string{"accountScope", "scope", "apiKey", "password", "hash"} {
		if _, ok := activityAJSON[forbidden]; ok {
			t.Fatalf("activity list leaked %q: %#v", forbidden, activityAJSON)
		}
	}

	entryB := adminUpstreamActivityFindEntry(t, listRR, "srv-b")
	activityBJSON := adminUpstreamActivityObject(t, entryB)
	if activityBJSON["available"] != true || activityBJSON["state"] != UpstreamActivityStateDisabled {
		t.Fatalf("srv-b activity = %#v", activityBJSON)
	}
	if int64(activityBJSON["revision"].(float64)) != savedB.Revision {
		t.Fatalf("srv-b list revision = %v, want %d", activityBJSON["revision"], savedB.Revision)
	}
	if activityBJSON["lastActivityAt"] == nil || activityBJSON["lastQualifiedAt"] != nil || activityBJSON["manualReferenceAt"] != nil {
		t.Fatalf("srv-b observed/manual split wrong: %#v", activityBJSON)
	}

	// A failure on one node must not make the other node unavailable in the
	// same batch response.
	f.app.activityHealth.markFailed("srv-b", upstreamActivityHealthSummary, errors.New("srv-b summary failed"))
	listRR = doJSONRequest(t, f.handler, http.MethodGet, "/admin/api/upstream", nil, f.token)
	if listRR.Code != http.StatusOK {
		t.Fatalf("GET upstream list with one failed node = %d", listRR.Code)
	}
	activityAJSON = adminUpstreamActivityObject(t, adminUpstreamActivityFindEntry(t, listRR, "srv-a"))
	activityBJSON = adminUpstreamActivityObject(t, adminUpstreamActivityFindEntry(t, listRR, "srv-b"))
	if activityAJSON["available"] != true {
		t.Fatalf("srv-b failure made srv-a unavailable: %#v", activityAJSON)
	}
	if activityBJSON["available"] != false || activityBJSON["state"] != UpstreamActivityStateUnavailable {
		t.Fatalf("srv-b failure was not isolated: %#v", activityBJSON)
	}

	if got := f.serverA.loginHits.Load() + f.serverA.contentHits.Load() + f.serverB.loginHits.Load() + f.serverB.contentHits.Load(); got != 0 {
		t.Fatalf("activity list made %d upstream request(s), want 0", got)
	}
}

func TestAdminUpstreamActivityPolicySaveKeepsSummaryFailure(t *testing.T) {
	f := newAdminUpstreamActivityFixture(t)
	const serverID = "srv-a"
	_, initial := f.getActivity(t, serverID)
	f.app.activityHealth.markFailed(serverID, upstreamActivityHealthSummary, errors.New("summary write failed"))

	manual := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	body := adminUpstreamActivityBody(initial.Revision)
	body["manualReferenceAt"] = manual.Format(time.RFC3339)
	rr := f.putActivity(t, serverID, body)
	if rr.Code != http.StatusOK {
		t.Fatalf("policy save with a summary failure = %d, body=%s", rr.Code, rr.Body.String())
	}
	saved := decodeAdminUpstreamActivityResponse(t, rr)
	if saved.Available || saved.State != UpstreamActivityStateUnavailable || !strings.Contains(saved.Message, "summary write failed") {
		t.Fatalf("policy save cleared a summary failure: %+v", saved)
	}
	if saved.Revision == initial.Revision {
		t.Fatal("policy was not saved despite the unrelated summary failure")
	}

	_, after := f.getActivity(t, serverID)
	if after.Available || after.State != UpstreamActivityStateUnavailable || after.Revision != saved.Revision {
		t.Fatalf("successful read cleared a summary write failure: %+v", after)
	}

	f.app.activityHealth.markAvailable(serverID, upstreamActivityHealthSummary)
	_, recovered := f.getActivity(t, serverID)
	if !recovered.Available || recovered.State != UpstreamActivityStateNormal {
		t.Fatalf("recovered summary stayed unavailable: %+v", recovered)
	}

	// A policy write failure is not cleared by successful reads either; only a
	// successful policy write makes the node available again.
	f.app.activityHealth.markFailed(serverID, upstreamActivityHealthPolicy, errors.New("policy write failed"))
	_, policyFailed := f.getActivity(t, serverID)
	if policyFailed.Available {
		t.Fatalf("successful read cleared a policy write failure: %+v", policyFailed)
	}
	if rr := f.putActivity(t, serverID, adminUpstreamActivityBody(recovered.Revision)); rr.Code != http.StatusOK {
		t.Fatalf("policy retry after a write failure = %d, body=%s", rr.Code, rr.Body.String())
	}
	_, policyRecovered := f.getActivity(t, serverID)
	if !policyRecovered.Available {
		t.Fatalf("successful policy write did not clear its failure: %+v", policyRecovered)
	}
}

func TestAdminUpstreamActivityHealthIsolatedByServerAndKind(t *testing.T) {
	var health upstreamActivityHealth
	health.markFailed("srv-a", upstreamActivityHealthSummary, errors.New("summary failure"))
	health.markFailed("srv-b", upstreamActivityHealthPolicy, errors.New("policy failure"))

	if got := health.serverAvailability(true, "srv-a"); got.Available || got.Message == "" {
		t.Fatalf("srv-a availability = %#v", got)
	}
	if got := health.serverAvailability(true, "srv-b"); got.Available || got.Message == "" {
		t.Fatalf("srv-b availability = %#v", got)
	}
	health.markAvailable("srv-a", upstreamActivityHealthPolicy)
	if got := health.serverAvailability(true, "srv-a"); got.Available {
		t.Fatalf("unrelated policy success cleared srv-a summary failure: %#v", got)
	}
	health.markAvailable("srv-a", upstreamActivityHealthSummary)
	if got := health.serverAvailability(true, "srv-a"); !got.Available {
		t.Fatalf("srv-a did not recover after its own summary success: %#v", got)
	}
	if got := health.serverAvailability(true, "srv-b"); got.Available {
		t.Fatalf("srv-a recovery cleared srv-b: %#v", got)
	}
	health.markAvailable("srv-b", upstreamActivityHealthPolicy)
	if got := health.availability(true); !got.Available {
		t.Fatalf("global availability did not recover: %#v", got)
	}
	health.markFailed("srv-a", upstreamActivityHealthRead, errors.New("read failure"))
	if got := health.availability(true); got.Available {
		t.Fatalf("global availability hid a read failure: %#v", got)
	}
	health.markAvailable("srv-a", upstreamActivityHealthRead)
	health.forgetServer("srv-b")
	if got := health.serverAvailability(true, "srv-b"); !got.Available {
		t.Fatalf("forgetServer left srv-b failed: %#v", got)
	}
	if got := health.serverAvailability(false, "srv-a"); got.Available || got.Message == "" {
		t.Fatalf("missing store reported available: %#v", got)
	}
}

func TestAdminUpstreamActivityClosedStoreReportsUnavailable(t *testing.T) {
	f := newAdminUpstreamActivityFixture(t)
	const serverID = "srv-a"
	_, initial := f.getActivity(t, serverID)

	if err := closeSQLite(f.app.IDStore.DB()); err != nil {
		t.Fatalf("close sqlite: %v", err)
	}
	f.serverA.reset()
	f.serverB.reset()

	status, after := f.getActivity(t, serverID)
	if status != http.StatusOK {
		t.Fatalf("GET activity after close = %d, want 200 with available=false", status)
	}
	if after.Available || after.State != UpstreamActivityStateUnavailable || after.Message == "" {
		t.Fatalf("closed store GET = %+v, want unavailable with a message", after)
	}

	listRR := doJSONRequest(t, f.handler, http.MethodGet, "/admin/api/upstream", nil, f.token)
	if listRR.Code != http.StatusOK {
		t.Fatalf("GET upstream list after close = %d", listRR.Code)
	}
	entry := adminUpstreamActivityFindEntry(t, listRR, serverID)
	activity := adminUpstreamActivityObject(t, entry)
	if activity["available"] != false || activity["state"] != UpstreamActivityStateUnavailable || activity["message"] == "" {
		t.Fatalf("closed store list activity = %#v", activity)
	}

	if rr := f.putActivity(t, serverID, adminUpstreamActivityBody(initial.Revision)); rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("PUT activity after close = %d, want 503, body=%s", rr.Code, rr.Body.String())
	}
	if got := f.serverA.loginHits.Load() + f.serverA.contentHits.Load() + f.serverB.loginHits.Load() + f.serverB.contentHits.Load(); got != 0 {
		t.Fatalf("closed-store activity calls made %d upstream request(s), want 0", got)
	}
}

func TestAdminUpstreamActivityMissingStoreReportsUnavailable(t *testing.T) {
	f := newAdminUpstreamActivityFixture(t)
	const serverID = "srv-a"
	_, initial := f.getActivity(t, serverID)
	f.app.UpstreamActivity = nil
	f.serverA.reset()
	f.serverB.reset()

	status, after := f.getActivity(t, serverID)
	if status != http.StatusOK {
		t.Fatalf("GET activity without store = %d, want 200 with available=false", status)
	}
	if after.Available || after.State != UpstreamActivityStateUnavailable || after.Message == "" {
		t.Fatalf("missing store GET = %+v, want unavailable with a message", after)
	}

	listRR := doJSONRequest(t, f.handler, http.MethodGet, "/admin/api/upstream", nil, f.token)
	if listRR.Code != http.StatusOK {
		t.Fatalf("GET upstream list without store = %d", listRR.Code)
	}
	entry := adminUpstreamActivityFindEntry(t, listRR, serverID)
	activity := adminUpstreamActivityObject(t, entry)
	if activity["available"] != false || activity["state"] != UpstreamActivityStateUnavailable {
		t.Fatalf("missing store list activity = %#v", activity)
	}

	if rr := f.putActivity(t, serverID, adminUpstreamActivityBody(initial.Revision)); rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("PUT activity without store = %d, want 503, body=%s", rr.Code, rr.Body.String())
	}
	if got := f.serverA.loginHits.Load() + f.serverA.contentHits.Load() + f.serverB.loginHits.Load() + f.serverB.contentHits.Load(); got != 0 {
		t.Fatalf("missing-store activity calls made %d upstream request(s), want 0", got)
	}
}
