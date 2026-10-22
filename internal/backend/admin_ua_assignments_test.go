package backend

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// TestCommitConfigForUAChangesKeepsStateOnWriteFailure proves a failed config
// write publishes nothing: the in-memory binding is unchanged and no client is
// swapped (execution plan §7.3).
func TestCommitConfigForUAChangesKeepsStateOnWriteFailure(t *testing.T) {
	withTempApp(t, func(app *App, _ http.Handler) {
		if err := app.UALibrary.CaptureAuthenticatedUA(uaTestHeaders("Emby/1.0", "Emby", "1.0", "Dev", "dev-1")); err != nil {
			t.Fatalf("capture: %v", err)
		}
		profile := app.UALibrary.Snapshot().Resident[0]
		cfg := app.ConfigStore.Snapshot()
		cfg.Upstream = append(cfg.Upstream, UpstreamConfig{
			ID: "srv-a", Name: "A", URL: "http://127.0.0.1:1", Username: "u", Password: "p",
			PlaybackMode: "proxy", SpoofClient: "passthrough",
		})
		if err := app.ConfigStore.SaveAndReplace(cfg); err != nil {
			t.Fatalf("seed: %v", err)
		}

		// Point the config path under a regular file so the atomic write fails.
		notDir := filepath.Join(t.TempDir(), "not-a-dir")
		if err := os.WriteFile(notDir, []byte("x"), 0o644); err != nil {
			t.Fatalf("blocker: %v", err)
		}
		if err := app.ConfigStore.Mutate(func(c *Config) error { c.Path = filepath.Join(notDir, "config.yaml"); return nil }); err != nil {
			t.Fatalf("mutate path: %v", err)
		}

		next := app.ConfigStore.Snapshot()
		next.Upstream[0].SpoofClient = "resident"
		next.Upstream[0].UAProfileID = profile.ID
		if _, err := app.commitConfigForUAChanges(next, []string{"srv-a"}); err == nil {
			t.Fatalf("a failed config write must return an error")
		}
		after := app.ConfigStore.Snapshot()
		if after.Upstream[0].SpoofClient != "passthrough" || after.Upstream[0].UAProfileID != "" {
			t.Fatalf("in-memory binding changed after a failed write: %+v", after.Upstream[0])
		}
	})
}

// TestResidentReplaceReconnectsBoundServers proves an identity change swaps the
// pool client and re-logs the bound server, while a same-identity refresh
// touches no server (execution plan §7.3, UA-09/UA-10).
func TestResidentReplaceReconnectsBoundServers(t *testing.T) {
	withTempApp(t, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret") // bootstraps a resident identity
		profile := app.UALibrary.Snapshot().Resident[0]

		create := map[string]any{
			"name": "ResidentSrv", "url": "http://127.0.0.1:1", "authType": "password",
			"username": "u", "password": "p", "playbackMode": "proxy",
			"spoofClient": "resident", "uaProfileId": profile.ID,
			"uaExpectedProfileRevision": profile.Revision,
		}
		rr := doJSONRequest(t, handler, http.MethodPost, "/admin/api/upstream", create, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("create: status=%d body=%s", rr.Code, rr.Body.String())
		}
		var createResp struct {
			AffectedServerIDs []string `json:"affectedServerIds"`
		}
		_ = json.Unmarshal(rr.Body.Bytes(), &createResp)
		if len(createResp.AffectedServerIDs) != 1 {
			t.Fatalf("create response missing affectedServerIds: %s", rr.Body.String())
		}
		serverID := createResp.AffectedServerIDs[0]

		// A second, different identity becomes the most recent capture.
		if err := app.UALibrary.CaptureAuthenticatedUA(uaTestHeaders("Emby/2.0", "Emby", "2.0", "Dev2", "dev-2")); err != nil {
			t.Fatalf("capture 2: %v", err)
		}
		recent := app.UALibrary.Snapshot().Recent[0]

		before := app.Upstream.ClientByID(serverID)
		rr = doJSONRequest(t, handler, http.MethodPost, "/admin/api/ua/resident/"+profile.ID+"/replace", map[string]any{
			"recentId": recent.ID, "expectedRevision": profile.Revision,
		}, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("replace: status=%d body=%s", rr.Code, rr.Body.String())
		}
		var resp struct {
			AffectedServerIDs     []string `json:"affectedServerIds"`
			ReconnectingServerIDs []string `json:"reconnectingServerIds"`
		}
		_ = json.Unmarshal(rr.Body.Bytes(), &resp)
		if len(resp.AffectedServerIDs) != 1 || resp.AffectedServerIDs[0] != serverID {
			t.Fatalf("affectedServerIds = %v, want [%s]", resp.AffectedServerIDs, serverID)
		}
		if len(resp.ReconnectingServerIDs) != 1 {
			t.Fatalf("reconnectingServerIds = %v, want the bound server", resp.ReconnectingServerIDs)
		}
		after := app.Upstream.ClientByID(serverID)
		if after == before {
			t.Fatalf("an identity change must swap the pool client")
		}
		if after.residentIdentity == nil || after.residentIdentity.IdentityVersion != 2 {
			t.Fatalf("replacement client did not fix the new identity version")
		}

		// Replacing with the same identity again: date refresh only, no reconnect.
		updated := app.UALibrary.Snapshot().Resident[0]
		rr = doJSONRequest(t, handler, http.MethodPost, "/admin/api/ua/resident/"+profile.ID+"/replace", map[string]any{
			"recentId": recent.ID, "expectedRevision": updated.Revision,
		}, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("same-identity replace: status=%d body=%s", rr.Code, rr.Body.String())
		}
		var resp2 struct {
			AffectedServerIDs []string `json:"affectedServerIds"`
		}
		_ = json.Unmarshal(rr.Body.Bytes(), &resp2)
		if len(resp2.AffectedServerIDs) != 0 {
			t.Fatalf("same-identity replace must not report affected servers: %v", resp2.AffectedServerIDs)
		}
	})
}
