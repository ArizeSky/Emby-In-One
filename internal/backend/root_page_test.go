package backend

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRootPageHasWebAndAdminEntries pins the root landing page: it is public,
// offers both fixed entry points, drops the outdated "Emby clients only" copy,
// and never carries a token. System Info stays JSON.
func TestRootPageHasWebAndAdminEntries(t *testing.T) {
	withTempApp(t, func(app *App, handler http.Handler) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("root status = %d, want 200", rr.Code)
		}
		body := rr.Body.String()
		for _, want := range []string{`href="/web/"`, `href="/admin/"`, "Emby-In-One", "Emby 客户端可使用当前地址连接"} {
			if !strings.Contains(body, want) {
				t.Errorf("root page missing %q\n%s", want, body)
			}
		}
		for _, bad := range []string{"此地址仅供 Emby 客户端使用", "token=", "api_key="} {
			if strings.Contains(body, bad) {
				t.Errorf("root page should not contain %q", bad)
			}
		}

		infoReq := httptest.NewRequest(http.MethodGet, "/System/Info/Public", nil)
		infoRR := httptest.NewRecorder()
		handler.ServeHTTP(infoRR, infoReq)
		if ct := infoRR.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Fatalf("System/Info/Public Content-Type = %q, want JSON", ct)
		}
	})
}
