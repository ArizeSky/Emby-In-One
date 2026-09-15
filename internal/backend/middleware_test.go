package backend

import (
	"net/http"
	"strings"
	"testing"
)

// adminAuditLines returns the buffered audit lines written by the admin API hook.
func adminAuditLines(app *App) []string {
	var lines []string
	for _, entry := range app.Logger.Entries(0) {
		if strings.HasPrefix(entry.Message, `admin "`) {
			lines = append(lines, entry.Message)
		}
	}
	return lines
}

// TestAdminAPIAuditLogsStateChangingCalls covers the trail the panel used to leave
// nowhere: who changed what, and which attempts were rejected.
func TestAdminAPIAuditLogsStateChangingCalls(t *testing.T) {
	withTempApp(t, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		before := len(adminAuditLines(app))

		rr := doJSONRequest(t, handler, http.MethodPost, "/admin/api/upstream",
			map[string]any{"name": "x", "url": "not-a-url", "password": "super-secret"}, token)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", rr.Code)
		}

		lines := adminAuditLines(app)
		if len(lines) != before+1 {
			t.Fatalf("audit lines = %d, want %d: %v", len(lines), before+1, lines)
		}
		line := lines[len(lines)-1]
		if !strings.Contains(line, `admin "admin":`) {
			t.Fatalf("audit line should name the acting account: %q", line)
		}
		if !strings.Contains(line, "POST /admin/api/upstream") || !strings.Contains(line, "400") {
			t.Fatalf("audit line should carry method, path and status: %q", line)
		}
		if strings.Contains(line, "super-secret") {
			t.Fatalf("audit line leaked the request body: %q", line)
		}
	})
}

func TestAdminAPIAuditSkipsSuccessfulReadsButLogsRejections(t *testing.T) {
	withTempApp(t, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		before := len(adminAuditLines(app))

		rr := doJSONRequest(t, handler, http.MethodGet, "/admin/api/status", nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rr.Code)
		}
		if got := len(adminAuditLines(app)); got != before {
			t.Fatalf("successful read should stay out of the audit trail: %v", adminAuditLines(app))
		}

		rr = doJSONRequest(t, handler, http.MethodGet, "/admin/api/status", nil, "not-a-real-token")
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rr.Code)
		}
		lines := adminAuditLines(app)
		if len(lines) != before+1 {
			t.Fatalf("rejected read was not logged: %v", lines)
		}
		if !strings.Contains(lines[len(lines)-1], "unknown token") {
			t.Fatalf("audit line should flag the unusable token: %q", lines[len(lines)-1])
		}
	})
}

func TestAdminAPIAuditRecordsNonAdminActor(t *testing.T) {
	withTempApp(t, func(app *App, handler http.Handler) {
		adminToken := loginToken(t, handler, "secret")
		rr := doJSONRequest(t, handler, http.MethodPost, "/admin/api/users",
			map[string]any{"username": "bob", "password": "bob123"}, adminToken)
		if rr.Code != http.StatusCreated {
			t.Fatalf("create user: status=%d body=%s", rr.Code, rr.Body.String())
		}
		userToken := loginTokenAs(t, handler, "bob", "bob123")
		before := len(adminAuditLines(app))

		rr = doJSONRequest(t, handler, http.MethodPost, "/admin/api/users",
			map[string]any{"username": "mallory", "password": "x"}, userToken)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403", rr.Code)
		}
		lines := adminAuditLines(app)
		if len(lines) != before+1 {
			t.Fatalf("rejected non-admin write was not logged: %v", lines)
		}
		line := lines[len(lines)-1]
		if !strings.Contains(line, `admin "bob (role:user)":`) {
			t.Fatalf("audit line should name the account and its role: %q", line)
		}
	})
}
