package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func uaTestHeaders(ua, client, version, device, deviceID string) http.Header {
	headers := http.Header{}
	headers.Set("User-Agent", ua)
	headers.Set("X-Emby-Client", client)
	headers.Set("X-Emby-Client-Version", version)
	headers.Set("X-Emby-Device-Name", device)
	headers.Set("X-Emby-Device-Id", deviceID)
	return headers
}

func newTestUALibrary(t *testing.T) *UALibraryStore {
	t.Helper()
	store, err := NewUALibraryStore(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewUALibraryStore: %v", err)
	}
	return store
}

func TestUALibraryCaptureBootstrapAndDedupe(t *testing.T) {
	store := newTestUALibrary(t)
	headers := uaTestHeaders("Emby/1.0", "Emby", "1.0", "Dev", "dev-1")
	if err := store.CaptureAuthenticatedUA(headers); err != nil {
		t.Fatalf("capture: %v", err)
	}
	snapshot := store.Snapshot()
	if len(snapshot.Recent) != 1 || len(snapshot.Resident) != 1 || !snapshot.BootstrapCompleted {
		t.Fatalf("first capture must create one recent, one resident and complete bootstrap: %+v", snapshot)
	}
	first := snapshot.Recent[0]

	time.Sleep(2 * time.Millisecond)
	if err := store.CaptureAuthenticatedUA(headers); err != nil {
		t.Fatalf("second capture: %v", err)
	}
	snapshot = store.Snapshot()
	if len(snapshot.Recent) != 1 || len(snapshot.Resident) != 1 {
		t.Fatalf("an identical identity must not add records: recent=%d resident=%d", len(snapshot.Recent), len(snapshot.Resident))
	}
	if !snapshot.Recent[0].LastCapturedAt.After(first.LastCapturedAt) {
		t.Fatalf("an identical capture must advance LastCapturedAt")
	}
}

func TestUALibraryRecentLimitKeepsSingleBootstrapResident(t *testing.T) {
	store := newTestUALibrary(t)
	for i := 0; i < recentUALimit+3; i++ {
		headers := uaTestHeaders("Emby/"+fmt.Sprint(i), "Emby", "1.0", "Dev", "dev-"+fmt.Sprint(i))
		if err := store.CaptureAuthenticatedUA(headers); err != nil {
			t.Fatalf("capture %d: %v", i, err)
		}
	}
	snapshot := store.Snapshot()
	if len(snapshot.Recent) != recentUALimit {
		t.Fatalf("recent = %d, want %d", len(snapshot.Recent), recentUALimit)
	}
	if len(snapshot.Resident) != 1 {
		t.Fatalf("bootstrap must create exactly one resident and no more: %d", len(snapshot.Resident))
	}
	// The newest capture must be first.
	if snapshot.Recent[0].Identity.DeviceID != fmt.Sprintf("dev-%d", recentUALimit+2) {
		t.Fatalf("recent list is not newest-first: %q", snapshot.Recent[0].Identity.DeviceID)
	}
}

func TestUALibrarySaveRenameReplace(t *testing.T) {
	store := newTestUALibrary(t)
	h1 := uaTestHeaders("Emby/1.0", "Emby", "1.0", "Dev", "dev-1")
	h2 := uaTestHeaders("Emby/2.0", "Emby", "2.0", "Dev2", "dev-2")
	if err := store.CaptureAuthenticatedUA(h1); err != nil {
		t.Fatal(err)
	}
	if err := store.CaptureAuthenticatedUA(h2); err != nil {
		t.Fatal(err)
	}
	snapshot := store.Snapshot()
	recent2 := snapshot.Recent[0] // h2 is newest
	residentA := snapshot.Resident[0]

	// Save h2 as a second resident.
	second, already, err := store.SaveRecent(saveRecentUAInput{RecentID: recent2.ID, Name: "Second"})
	if err != nil || already {
		t.Fatalf("SaveRecent: err=%v already=%v", err, already)
	}
	if second.Name != "Second" {
		t.Fatalf("name = %q", second.Name)
	}
	// Saving the same recent again is idempotent.
	again, already2, err := store.SaveRecent(saveRecentUAInput{RecentID: recent2.ID})
	if err != nil || !already2 || again.ID != second.ID {
		t.Fatalf("repeat SaveRecent must return the existing record: err=%v already=%v id=%q", err, already2, again.ID)
	}
	// Rename with a stale revision conflicts.
	if _, err := store.RenameResident(renameResidentUAInput{ID: second.ID, Name: "X", ExpectedRevision: second.Revision + 50}); err != errUAConflict {
		t.Fatalf("stale rename err = %v, want conflict", err)
	}
	renamed, err := store.RenameResident(renameResidentUAInput{ID: second.ID, Name: "Renamed", ExpectedRevision: second.Revision})
	if err != nil || renamed.Name != "Renamed" {
		t.Fatalf("rename: err=%v name=%q", err, renamed.Name)
	}

	// residentA holds identity h1; recent2 (identity h2) is already resident as
	// `second`, so replacing residentA with recent2 is a duplicate error.
	if _, err := store.ReplaceResident(replaceResidentUAInput{ResidentID: residentA.ID, RecentID: recent2.ID, ExpectedRevision: residentA.Revision}); err == nil {
		t.Fatalf("expected a duplicate-resident error")
	}
	// Refreshing `second` with its own recent identity keeps the identity version.
	refreshed, err := store.ReplaceResident(replaceResidentUAInput{ResidentID: second.ID, RecentID: recent2.ID, ExpectedRevision: renamed.Revision})
	if err != nil {
		t.Fatalf("same-identity refresh: %v", err)
	}
	if refreshed.IdentityVersion != second.IdentityVersion {
		t.Fatalf("same-identity refresh changed version: %d -> %d", second.IdentityVersion, refreshed.IdentityVersion)
	}
}

func TestUALibraryReplaceSameIdentityKeepsVersion(t *testing.T) {
	store := newTestUALibrary(t)
	headers := uaTestHeaders("Emby/1.0", "Emby", "1.0", "Dev", "dev-1")
	if err := store.CaptureAuthenticatedUA(headers); err != nil {
		t.Fatal(err)
	}
	snapshot := store.Snapshot()
	resident := snapshot.Resident[0]
	recent := snapshot.Recent[0]
	time.Sleep(2 * time.Millisecond)
	replaced, err := store.ReplaceResident(replaceResidentUAInput{ResidentID: resident.ID, RecentID: recent.ID, ExpectedRevision: resident.Revision})
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if replaced.IdentityVersion != resident.IdentityVersion {
		t.Fatalf("same-identity refresh changed identityVersion: %d -> %d", resident.IdentityVersion, replaced.IdentityVersion)
	}
	if replaced.Revision != resident.Revision+1 {
		t.Fatalf("revision did not advance")
	}
}

func TestUALibraryDeleteRevisionGuard(t *testing.T) {
	store := newTestUALibrary(t)
	if err := store.CaptureAuthenticatedUA(uaTestHeaders("Emby/1.0", "Emby", "1.0", "Dev", "dev-1")); err != nil {
		t.Fatal(err)
	}
	resident := store.Snapshot().Resident[0]
	if err := store.DeleteResident(resident.ID, resident.Revision+1); err != errUAConflict {
		t.Fatalf("stale delete err = %v, want conflict", err)
	}
	if err := store.DeleteResident(resident.ID, resident.Revision); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(store.Snapshot().Resident) != 0 {
		t.Fatalf("resident not removed")
	}
	// The bootstrap marker is not reset: a later capture must not auto-create.
	if err := store.CaptureAuthenticatedUA(uaTestHeaders("Emby/3.0", "Emby", "3.0", "Dev3", "dev-3")); err != nil {
		t.Fatal(err)
	}
	snapshot := store.Snapshot()
	if len(snapshot.Resident) != 0 {
		t.Fatalf("a capture after deleting all residents must not auto-create one: %d", len(snapshot.Resident))
	}
	if len(snapshot.Recent) != 2 {
		t.Fatalf("the capture should still enter the recent list: %d", len(snapshot.Recent))
	}
}

func TestUAAgeBoundaries(t *testing.T) {
	now := time.Now()
	known, old, days := uaAge(now.Add(-oldUAThreshold), now)
	if !known || old || days != 30 {
		t.Fatalf("exactly 30 days must be known, not old, 30 days: known=%v old=%v days=%d", known, old, days)
	}
	_, old, _ = uaAge(now.Add(-oldUAThreshold-time.Second), now)
	if !old {
		t.Fatalf("one second past 30 days must be old")
	}
	known, old, _ = uaAge(time.Time{}, now)
	if known || old {
		t.Fatalf("zero capture time must be unknown and not old")
	}
	// A future time clamps to 0 days.
	_, _, days = uaAge(now.Add(48*time.Hour), now)
	if days != 0 {
		t.Fatalf("future capture time days = %d, want 0", days)
	}
}

func TestUALibraryPersistenceReload(t *testing.T) {
	dir := t.TempDir()
	store, err := NewUALibraryStore(dir, nil)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if err := store.CaptureAuthenticatedUA(uaTestHeaders("Emby/1.0", "Emby", "1.0", "Dev", "dev-1")); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewUALibraryStore(dir, nil)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	snapshot := reloaded.Snapshot()
	if len(snapshot.Recent) != 1 || len(snapshot.Resident) != 1 || !snapshot.BootstrapCompleted {
		t.Fatalf("reloaded library lost state: %+v", snapshot)
	}
}

func TestUALibraryCorruptFileUnavailable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ua-library.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := NewUALibraryStore(dir, nil)
	if err == nil {
		t.Fatalf("expected an error for a corrupt library")
	}
	if store.Available() {
		t.Fatalf("a corrupt library must report unavailable")
	}
	if _, _, saveErr := store.SaveRecent(saveRecentUAInput{}); saveErr != errUALibraryUnavailable {
		t.Fatalf("mutation on unavailable library err = %v, want unavailable", saveErr)
	}
	raw, readErr := os.ReadFile(path)
	if readErr != nil || string(raw) != "{not json" {
		t.Fatalf("the corrupt file must be preserved untouched: %q err=%v", raw, readErr)
	}
}

func TestUALibraryCaptureSkipsIncompleteIdentity(t *testing.T) {
	store := newTestUALibrary(t)
	headers := http.Header{}
	headers.Set("User-Agent", "curl/8.0")
	if err := store.CaptureAuthenticatedUA(headers); err != nil {
		t.Fatalf("capture: %v", err)
	}
	if len(store.Snapshot().Recent) != 0 {
		t.Fatalf("an incomplete identity must not be captured")
	}
}

func TestResidentIdentityResolution(t *testing.T) {
	store := newTestUALibrary(t)
	if err := store.CaptureAuthenticatedUA(uaTestHeaders("Emby/1.0", "Emby", "1.0", "Dev", "dev-1")); err != nil {
		t.Fatal(err)
	}
	profile := store.Snapshot().Resident[0]

	cfg := Config{Timeouts: TimeoutsConfig{API: 30000, Login: 30000, HealthCheck: 30000}}
	upstream := UpstreamConfig{
		ID: "u1", Name: "A", URL: "http://emby.example.com",
		PlaybackMode: "proxy", SpoofClient: "resident", UAProfileID: profile.ID,
	}
	client := newUpstreamClient(cfg, upstream, 0, nil)
	if client.residentIdentity == nil {
		t.Fatalf("resident client did not resolve its fixed identity snapshot")
	}
	source, headers := client.resolveIdentityHeaders(nil, nil, nil)
	if headers.Get("X-Emby-Device-Id") != "dev-1" {
		t.Fatalf("resident identity device id = %q, want dev-1", headers.Get("X-Emby-Device-Id"))
	}
	if headers.Get("User-Agent") != "Emby/1.0" {
		t.Fatalf("resident identity UA = %q", headers.Get("User-Agent"))
	}
	if source == "" || source == "resident-missing" {
		t.Fatalf("resident source = %q", source)
	}

	// A missing profile reference resolves to no identity and a preparation error.
	upstream.UAProfileID = "does-not-exist"
	missing := newUpstreamClient(cfg, upstream, 0, nil)
	if missing.residentIdentity != nil {
		t.Fatalf("a missing profile must not resolve a snapshot")
	}
	if _, err := missing.doRequestForMode(nil, nil, http.MethodGet, "/Users/Me", nil, nil, nil, false, authModeNormal); err == nil {
		t.Fatalf("a resident request with a missing profile must return a preparation error")
	}
}

func TestReloadDropsSessionWhenLeavingResident(t *testing.T) {
	store := newTestUALibrary(t)
	if err := store.CaptureAuthenticatedUA(uaTestHeaders("Emby/1.0", "Emby", "1.0", "Dev", "dev-1")); err != nil {
		t.Fatal(err)
	}
	profile := store.Snapshot().Resident[0]

	cfg := Config{Timeouts: TimeoutsConfig{API: 30000, Login: 30000, HealthCheck: 30000}}
	cfg.Upstream = []UpstreamConfig{{
		ID: "u1", Name: "A", URL: "http://127.0.0.1:1", Username: "u", Password: "p",
		PlaybackMode: "proxy", SpoofClient: "resident", UAProfileID: profile.ID,
	}}
	pool := NewUpstreamPool(cfg, nil)
	client := pool.ClientByID("u1")
	if client == nil {
		t.Fatalf("pool did not build the resident client")
	}
	client.mu.Lock()
	client.AccessToken = "resident-token"
	client.UserID = "resident-user"
	client.Online = true
	client.mu.Unlock()

	// Switch the same server back to passthrough and reload.
	next := cfg
	next.Upstream = append([]UpstreamConfig(nil), cfg.Upstream...)
	next.Upstream[0].SpoofClient = "passthrough"
	next.Upstream[0].UAProfileID = ""
	pool.Reload(next)

	replaced := pool.ClientByID("u1")
	if replaced == client {
		t.Fatalf("reload must replace the client instance on an identity change")
	}
	if replaced.AccessToken != "" || replaced.UserID != "" || replaced.Online {
		t.Fatalf("leaving resident mode inherited the old session (token=%q user=%q online=%v)",
			replaced.AccessToken, replaced.UserID, replaced.Online)
	}
}

func TestUALibraryReplaceChangesIdentity(t *testing.T) {
	store := newTestUALibrary(t)
	h1 := uaTestHeaders("Emby/1.0", "Emby", "1.0", "Dev", "dev-1")
	h2 := uaTestHeaders("Emby/2.0", "Emby", "2.0", "Dev2", "dev-2")
	if err := store.CaptureAuthenticatedUA(h1); err != nil {
		t.Fatal(err)
	}
	if err := store.CaptureAuthenticatedUA(h2); err != nil {
		t.Fatal(err)
	}
	snapshot := store.Snapshot()
	resident := snapshot.Resident[0] // identity h1
	recentB := snapshot.Recent[0]    // identity h2
	replaced, err := store.ReplaceResident(replaceResidentUAInput{ResidentID: resident.ID, RecentID: recentB.ID, ExpectedRevision: resident.Revision})
	if err != nil {
		t.Fatalf("replace: %v", err)
	}
	if replaced.IdentityVersion != resident.IdentityVersion+1 {
		t.Fatalf("identityVersion = %d, want %d", replaced.IdentityVersion, resident.IdentityVersion+1)
	}
	if replaced.Fingerprint != recentB.Fingerprint {
		t.Fatalf("replace did not adopt the new fingerprint")
	}
	if replaced.ID != resident.ID || replaced.Name != resident.Name {
		t.Fatalf("replace must keep the resident id and name")
	}
}

func TestAdminUAResidentLifecycle(t *testing.T) {
	withTempApp(t, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret") // bootstraps a resident identity

		rr := doJSONRequest(t, handler, http.MethodGet, "/admin/api/ua", nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("ua list status = %d body=%s", rr.Code, rr.Body.String())
		}
		var list struct {
			Resident []struct {
				ID       string `json:"id"`
				Revision uint64 `json:"revision"`
			} `json:"resident"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
			t.Fatalf("decode ua list: %v", err)
		}
		if len(list.Resident) != 1 {
			t.Fatalf("expected one bootstrap resident, got %d", len(list.Resident))
		}
		profileID := list.Resident[0].ID
		revision := list.Resident[0].Revision

		// Create a server bound to the resident identity.
		rr = doJSONRequest(t, handler, http.MethodPost, "/admin/api/upstream", map[string]any{
			"name": "ResidentSrv", "url": "http://127.0.0.1:1",
			"authType": "password", "username": "u", "password": "p",
			"playbackMode": "proxy", "spoofClient": "resident",
			"uaProfileId": profileID, "uaExpectedProfileRevision": revision,
		}, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("resident create status = %d body=%s", rr.Code, rr.Body.String())
		}
		var createResp struct {
			AffectedServerIDs []string `json:"affectedServerIds"`
		}
		_ = json.Unmarshal(rr.Body.Bytes(), &createResp)
		if len(createResp.AffectedServerIDs) != 1 {
			t.Fatalf("create response missing affectedServerIds: %s", rr.Body.String())
		}
		serverID := createResp.AffectedServerIDs[0]

		cfg := app.ConfigStore.Snapshot()
		if len(cfg.Upstream) != 1 || cfg.Upstream[0].SpoofClient != "resident" || cfg.Upstream[0].UAProfileID != profileID {
			t.Fatalf("resident binding not persisted: %+v", cfg.Upstream)
		}

		// Deleting a bound resident is refused with the binding list.
		rr = doJSONRequest(t, handler, http.MethodDelete, "/admin/api/ua/resident/"+profileID, map[string]any{"expectedRevision": revision}, token)
		if rr.Code != http.StatusConflict {
			t.Fatalf("delete bound resident status = %d, want 409; body=%s", rr.Code, rr.Body.String())
		}

		// Release the server, restoring passthrough.
		rr = doJSONRequest(t, handler, http.MethodPost, "/admin/api/ua/resident/"+profileID+"/assignments", map[string]any{
			"releaseServerIds":        []string{serverID},
			"expectedProfileRevision": revision,
			"expectedBindings":        []map[string]any{{"serverId": serverID, "spoofClient": "resident", "uaProfileId": profileID}},
		}, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("assignment release status = %d body=%s", rr.Code, rr.Body.String())
		}
		cfg = app.ConfigStore.Snapshot()
		if cfg.Upstream[0].SpoofClient != "passthrough" || cfg.Upstream[0].UAProfileID != "" {
			t.Fatalf("release did not restore passthrough: %+v", cfg.Upstream[0])
		}

		// Now the resident is unreferenced and can be deleted.
		rr = doJSONRequest(t, handler, http.MethodDelete, "/admin/api/ua/resident/"+profileID, map[string]any{"expectedRevision": revision}, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("delete unreferenced resident status = %d body=%s", rr.Code, rr.Body.String())
		}
		if len(app.UALibrary.Snapshot().Resident) != 0 {
			t.Fatalf("resident not deleted")
		}
	})
}
