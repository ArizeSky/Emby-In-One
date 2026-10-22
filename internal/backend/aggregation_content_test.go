package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

func disableAggregation(t *testing.T, handler http.Handler, token, id string) {
	t.Helper()
	rr := doJSONRequest(t, handler, http.MethodPatch, "/admin/api/upstream/"+id+"/aggregation", map[string]any{"aggregationEnabled": false}, token)
	if rr.Code != http.StatusOK {
		t.Fatalf("disable aggregation for %s: status=%d body=%s", id, rr.Code, rr.Body.String())
	}
}

func TestDisabledAggregationContentRoutes(t *testing.T) {
	a := newAggUpstreamStub(t)
	b := newAggUpstreamStub(t)
	withTempAppConfig(t, dualUpstreamConfig(a.server.URL, b.server.URL), func(app *App, handler http.Handler) {
		adminToken := loginTokenAs(t, handler, "admin", "secret")
		idA := app.Upstream.Clients()[0].ID
		idB := app.Upstream.Clients()[1].ID
		if rr := doAuthJSON(t, handler, http.MethodPost, "/admin/api/users", map[string]any{"username": "bob", "password": "bob12345", "allowedServers": []string{idA, idB}}, adminToken); rr.Code != http.StatusCreated {
			t.Fatalf("create user: status=%d body=%s", rr.Code, rr.Body.String())
		}
		bobToken := loginTokenAs(t, handler, "bob", "bob12345")
		disableAggregation(t, handler, adminToken, idA)

		// Views: neither the admin nor an authorized user may query the paused node.
		a.reset()
		b.reset()
		if rr := doAuthJSON(t, handler, http.MethodGet, "/Users/u/Views", nil, adminToken); rr.Code != http.StatusOK {
			t.Fatalf("admin views: status=%d body=%s", rr.Code, rr.Body.String())
		}
		if a.contentHits.Load() != 0 {
			t.Errorf("admin views queried the paused server A %d time(s)", a.contentHits.Load())
		}
		if b.contentHits.Load() == 0 {
			t.Errorf("admin views did not query the enabled server B")
		}

		a.reset()
		b.reset()
		if rr := doAuthJSON(t, handler, http.MethodGet, "/Users/u/Views", nil, bobToken); rr.Code != http.StatusOK {
			t.Fatalf("user views: status=%d body=%s", rr.Code, rr.Body.String())
		}
		if a.contentHits.Load() != 0 {
			t.Errorf("user views queried the paused server A %d time(s)", a.contentHits.Load())
		}

		// Item detail on the paused server: 404, no upstream request.
		a.reset()
		vidA := app.IDStore.GetOrCreateVirtualID("movie-a", idA)
		if rr := doAuthJSON(t, handler, http.MethodGet, "/Items/"+vidA, nil, adminToken); rr.Code != http.StatusNotFound {
			t.Errorf("paused item detail: status=%d, want 404", rr.Code)
		}
		if a.contentHits.Load() != 0 {
			t.Errorf("paused item detail queried server A")
		}

		// Explicit MediaSourceId on the paused server: 403, before any rewrite.
		vidB := app.IDStore.GetOrCreateVirtualID("movie-b", idB)
		msA := app.IDStore.GetOrCreateVirtualID("ms-a", idA)
		if rr := doAuthJSON(t, handler, http.MethodPost, "/Items/"+vidB+"/PlaybackInfo", map[string]any{"MediaSourceId": msA}, adminToken); rr.Code != http.StatusForbidden {
			t.Errorf("explicit paused media source: status=%d, want 403", rr.Code)
		}
	})
}

func TestDisabledPrimaryUsesEnabledCopy(t *testing.T) {
	a := newAggUpstreamStub(t)
	b := newAggUpstreamStub(t)
	withTempAppConfig(t, dualUpstreamConfig(a.server.URL, b.server.URL), func(app *App, handler http.Handler) {
		adminToken := loginTokenAs(t, handler, "admin", "secret")
		idA := app.Upstream.Clients()[0].ID
		idB := app.Upstream.Clients()[1].ID
		vid := app.IDStore.GetOrCreateVirtualID("movie-a", idA)
		app.IDStore.AssociateAdditionalInstance(vid, "movie-b", idB)

		disableAggregation(t, handler, adminToken, idA)
		a.reset()
		b.reset()
		rr := doAuthJSON(t, handler, http.MethodGet, "/Items/"+vid, nil, adminToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("item detail via copy: status=%d body=%s", rr.Code, rr.Body.String())
		}
		if a.contentHits.Load() != 0 {
			t.Errorf("paused primary A was queried")
		}
		if b.contentHits.Load() == 0 {
			t.Errorf("enabled copy B was not queried")
		}
	})
}

func TestAnonymousImagesRespectAggregationSetting(t *testing.T) {
	a := newAggUpstreamStub(t)
	b := newAggUpstreamStub(t)
	withTempAppConfig(t, dualUpstreamConfig(a.server.URL, b.server.URL), func(app *App, handler http.Handler) {
		adminToken := loginTokenAs(t, handler, "admin", "secret")
		idA := app.Upstream.Clients()[0].ID
		idB := app.Upstream.Clients()[1].ID
		vidA := app.IDStore.GetOrCreateVirtualID("movie-a", idA)

		rr := doAuthJSON(t, handler, http.MethodGet, "/Items/"+vidA+"/Images/Primary", nil, "")
		if rr.Code != http.StatusOK || rr.Body.String() != "png" {
			t.Fatalf("anonymous image on enabled server: status=%d body=%q", rr.Code, rr.Body.String())
		}
		disableAggregation(t, handler, adminToken, idA)
		if rr := doAuthJSON(t, handler, http.MethodGet, "/Items/"+vidA+"/Images/Primary", nil, ""); rr.Code != http.StatusNotFound {
			t.Errorf("anonymous image on paused server: status=%d, want 404", rr.Code)
		}

		// Authenticated but unauthorized request against an enabled server stays 403.
		if rr := doAuthJSON(t, handler, http.MethodPost, "/admin/api/users", map[string]any{"username": "eve", "password": "eve12345", "allowedServers": []string{idA}}, adminToken); rr.Code != http.StatusCreated {
			t.Fatalf("create eve: status=%d body=%s", rr.Code, rr.Body.String())
		}
		eveToken := loginTokenAs(t, handler, "eve", "eve12345")
		vidB := app.IDStore.GetOrCreateVirtualID("movie-b", idB)
		if rr := doAuthJSON(t, handler, http.MethodGet, "/Items/"+vidB+"/Images/Primary", nil, eveToken); rr.Code != http.StatusForbidden {
			t.Errorf("unauthorized image: status=%d, want 403", rr.Code)
		}
	})
}

func TestDisabledKnownFallbackDoesNotSelectUnrelatedServer(t *testing.T) {
	a := newAggUpstreamStub(t)
	b := newAggUpstreamStub(t)
	withTempAppConfig(t, dualUpstreamConfig(a.server.URL, b.server.URL), func(app *App, handler http.Handler) {
		adminToken := loginTokenAs(t, handler, "admin", "secret")
		idA := app.Upstream.Clients()[0].ID
		idB := app.Upstream.Clients()[1].ID
		disableAggregation(t, handler, adminToken, idA)

		vidA := app.IDStore.GetOrCreateVirtualID("movie-a", idA)
		a.reset()
		b.reset()
		if rr := doAuthJSON(t, handler, http.MethodGet, "/Something/"+vidA, nil, adminToken); rr.Code != http.StatusServiceUnavailable {
			t.Errorf("fallback with paused old id: status=%d, want 503", rr.Code)
		}
		if b.contentHits.Load() != 0 {
			t.Errorf("fallback contacted unrelated server B for A's old id")
		}

		vidB := app.IDStore.GetOrCreateVirtualID("movie-b", idB)
		a.reset()
		b.reset()
		if rr := doAuthJSON(t, handler, http.MethodGet, "/Something/"+vidA+"/"+vidB, nil, adminToken); rr.Code != http.StatusServiceUnavailable {
			t.Errorf("fallback with mixed ids: status=%d, want 503", rr.Code)
		}
		if b.contentHits.Load() != 0 {
			t.Errorf("fallback with mixed ids contacted B")
		}
	})
}

func TestResumePaginationSkipsPausedCandidates(t *testing.T) {
	a := newAggUpstreamStub(t)
	b := newAggUpstreamStub(t)
	withTempAppConfig(t, dualUpstreamConfig(a.server.URL, b.server.URL), func(app *App, handler http.Handler) {
		adminToken := loginTokenAs(t, handler, "admin", "secret")
		idA := app.Upstream.Clients()[0].ID
		idB := app.Upstream.Clients()[1].ID
		if rr := doAuthJSON(t, handler, http.MethodPost, "/admin/api/users", map[string]any{"username": "bob", "password": "bob12345", "allowedServers": []string{idA, idB}}, adminToken); rr.Code != http.StatusCreated {
			t.Fatalf("create user: status=%d body=%s", rr.Code, rr.Body.String())
		}
		var bobID string
		for _, u := range app.UserStore.List() {
			if u.Username == "bob" {
				bobID = u.ID
			}
		}
		if bobID == "" {
			t.Fatalf("bob not found in user store")
		}
		disableAggregation(t, handler, adminToken, idA)

		vidA := app.IDStore.GetOrCreateVirtualID("movie-a", idA)
		vidB := app.IDStore.GetOrCreateVirtualID("movie-b", idB)
		if err := app.WatchStore.RecordProgress(&WatchProgress{ProxyUserID: bobID, VirtualItemID: vidA, ServerID: idA, OriginalItemID: "movie-a", PositionTicks: 100, RuntimeTicks: 1000, ItemType: "Movie"}); err != nil {
			t.Fatalf("record A: %v", err)
		}
		if err := app.WatchStore.RecordProgress(&WatchProgress{ProxyUserID: bobID, VirtualItemID: vidB, ServerID: idB, OriginalItemID: "movie-b", PositionTicks: 200, RuntimeTicks: 1000, ItemType: "Movie"}); err != nil {
			t.Fatalf("record B: %v", err)
		}

		bobToken := loginTokenAs(t, handler, "bob", "bob12345")
		a.reset()
		b.reset()
		rr := doAuthJSON(t, handler, http.MethodGet, "/Users/"+bobID+"/Items/Resume", nil, bobToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("resume: status=%d body=%s", rr.Code, rr.Body.String())
		}
		var payload struct {
			Items []map[string]any `json:"Items"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal resume: %v", err)
		}
		if a.contentHits.Load() != 0 {
			t.Errorf("resume queried the paused server A")
		}
		if len(payload.Items) != 1 || payload.Items[0]["Id"] != vidB {
			t.Fatalf("resume items = %#v, want the single enabled item %s", payload.Items, vidB)
		}

		// The offset-0 wrapper stays compatible with the old helper.
		rows, err := app.WatchStore.GetResumeItems(bobID, 10)
		if err != nil {
			t.Fatalf("GetResumeItems: %v", err)
		}
		if len(rows) != 2 {
			t.Fatalf("GetResumeItems returned %d rows, want 2", len(rows))
		}
	})
}

// TestResumePaginationScansPastPausedCandidates proves the resume scan crosses
// batch boundaries: a full first page of paused records must not bury a later
// playable record behind it (execution plan §5.4).
func TestResumePaginationScansPastPausedCandidates(t *testing.T) {
	a := newAggUpstreamStub(t)
	b := newAggUpstreamStub(t)
	withTempAppConfig(t, dualUpstreamConfig(a.server.URL, b.server.URL), func(app *App, handler http.Handler) {
		adminToken := loginTokenAs(t, handler, "admin", "secret")
		idA := app.Upstream.Clients()[0].ID
		idB := app.Upstream.Clients()[1].ID
		if rr := doAuthJSON(t, handler, http.MethodPost, "/admin/api/users", map[string]any{"username": "dave", "password": "dave12345", "allowedServers": []string{idA, idB}}, adminToken); rr.Code != http.StatusCreated {
			t.Fatalf("create user: status=%d body=%s", rr.Code, rr.Body.String())
		}
		var daveID string
		for _, u := range app.UserStore.List() {
			if u.Username == "dave" {
				daveID = u.ID
			}
		}
		if daveID == "" {
			t.Fatalf("dave not found in user store")
		}
		disableAggregation(t, handler, adminToken, idA)

		// The playable record first, so it sorts OLDEST (last_played is
		// UnixMilli); a full first page of 100 paused records then buries it
		// behind the scan's first batch.
		vidB := app.IDStore.GetOrCreateVirtualID("movie-b", idB)
		if err := app.WatchStore.RecordProgress(&WatchProgress{
			ProxyUserID: daveID, VirtualItemID: vidB, ServerID: idB,
			OriginalItemID: "movie-b", PositionTicks: 200, RuntimeTicks: 1000, ItemType: "Movie",
		}); err != nil {
			t.Fatalf("record B: %v", err)
		}
		time.Sleep(10 * time.Millisecond) // guarantee strictly older last_played

		const pausedCount = 100 // exactly one full batch
		for i := 0; i < pausedCount; i++ {
			vid := app.IDStore.GetOrCreateVirtualID(fmt.Sprintf("paused-%d", i), idA)
			if err := app.WatchStore.RecordProgress(&WatchProgress{
				ProxyUserID: daveID, VirtualItemID: vid, ServerID: idA,
				OriginalItemID: fmt.Sprintf("paused-%d", i),
				PositionTicks: 100, RuntimeTicks: 1000, ItemType: "Movie",
			}); err != nil {
				t.Fatalf("record paused %d: %v", i, err)
			}
		}

		daveToken := loginTokenAs(t, handler, "dave", "dave12345")
		a.reset()
		b.reset()
		rr := doAuthJSON(t, handler, http.MethodGet, "/Users/"+daveID+"/Items/Resume?Limit=5", nil, daveToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("resume: status=%d body=%s", rr.Code, rr.Body.String())
		}
		var payload struct {
			Items []map[string]any `json:"Items"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal resume: %v", err)
		}
		if a.contentHits.Load() != 0 {
			t.Errorf("resume queried the paused server A")
		}
		if len(payload.Items) != 1 || payload.Items[0]["Id"] != vidB {
			t.Fatalf("resume items = %#v, want the single enabled item behind a full paused page", payload.Items)
		}
	})
}
