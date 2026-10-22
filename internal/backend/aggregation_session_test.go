package backend

import (
	"net/http"
	"testing"
)

// TestPausedServerStillAcceptsSessionCleanup pins the cleanup exception: a
// server whose aggregation is switched off rejects a new play start, but must
// still accept progress/stop/PlayingItems-DELETE/encoding cleanup so an already
// running playback can be released.
func TestPausedServerStillAcceptsSessionCleanup(t *testing.T) {
	a := newAggUpstreamStub(t)
	b := newAggUpstreamStub(t)
	withTempAppConfig(t, dualUpstreamConfig(a.server.URL, b.server.URL), func(app *App, handler http.Handler) {
		adminToken := loginTokenAs(t, handler, "admin", "secret")
		idA := app.Upstream.Clients()[0].ID
		vidA := app.IDStore.GetOrCreateVirtualID("movie-a", idA)
		disableAggregation(t, handler, adminToken, idA)

		// A new play start on the paused server is refused.
		a.reset()
		if rr := doAuthJSON(t, handler, http.MethodPost, "/Sessions/Playing", map[string]any{"ItemId": vidA}, adminToken); rr.Code != http.StatusForbidden {
			t.Errorf("new playing on paused server: status=%d, want 403", rr.Code)
		}
		if a.contentHits.Load() != 0 {
			t.Errorf("rejected play start still reached the paused server")
		}

		// Progress and stop are still forwarded.
		a.reset()
		if rr := doAuthJSON(t, handler, http.MethodPost, "/Sessions/Playing/Progress", map[string]any{"ItemId": vidA, "PositionTicks": 100}, adminToken); rr.Code != http.StatusNoContent {
			t.Errorf("progress on paused server: status=%d, want 204", rr.Code)
		}
		if a.contentHits.Load() == 0 {
			t.Errorf("progress was not forwarded to the paused server")
		}

		a.reset()
		if rr := doAuthJSON(t, handler, http.MethodPost, "/Sessions/Playing/Stopped", map[string]any{"ItemId": vidA}, adminToken); rr.Code != http.StatusNoContent {
			t.Errorf("stopped on paused server: status=%d, want 204", rr.Code)
		}
		if a.contentHits.Load() == 0 {
			t.Errorf("stop was not forwarded to the paused server")
		}

		// PlayingItems DELETE releases the slot through the cleanup path.
		a.reset()
		if rr := doAuthJSON(t, handler, http.MethodDelete, "/Users/u/PlayingItems/"+vidA, nil, adminToken); rr.Code != http.StatusNoContent {
			t.Errorf("playing-items delete on paused server: status=%d, want 204", rr.Code)
		}
		if a.contentHits.Load() == 0 {
			t.Errorf("cleanup DELETE was not forwarded to the paused server")
		}

		// Encoding cleanup without a PlaySessionId still reaches the paused server.
		a.reset()
		b.reset()
		if rr := doAuthJSON(t, handler, http.MethodDelete, "/Videos/ActiveEncodings", nil, adminToken); rr.Code != http.StatusNoContent {
			t.Errorf("active encodings cleanup: status=%d, want 204", rr.Code)
		}
		if a.contentHits.Load() == 0 {
			t.Errorf("active encodings cleanup skipped the paused server")
		}
	})
}
