package backend

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func prepareTempWorkspace(t *testing.T, config string, prepare func(dir string)) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(config), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "public"), 0o755); err != nil {
		t.Fatalf("create public dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "public", "admin.html"), []byte("<html>admin</html>"), 0o644); err != nil {
		t.Fatalf("write admin html: %v", err)
	}
	if prepare != nil {
		prepare(dir)
	}
	return dir
}

func chdirForTest(t *testing.T, dir string) {
	t.Helper()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir temp dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
}

func newTestApp(t *testing.T) (*App, http.Handler) {
	t.Helper()
	app, err := NewApp()
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	t.Cleanup(func() { _ = app.Close() })
	return app, app.Handler()
}

func withTempAppPrepared(t *testing.T, config string, prepare func(dir string), fn func(app *App, handler http.Handler, dir string)) {
	t.Helper()
	dir := prepareTempWorkspace(t, config, prepare)
	chdirForTest(t, dir)
	app, handler := newTestApp(t)
	fn(app, handler, dir)
}

func loginTokenWithHeaders(t *testing.T, handler http.Handler, password string, headers http.Header) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", bytes.NewBufferString(`{"Username":"admin","Pw":"`+password+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "TestUA/1.0")
	req.Header.Set("X-Emby-Client", "TestClient")
	req.Header.Set("X-Emby-Client-Version", "1.2.3")
	req.Header.Set("X-Emby-Device-Name", "Test Device")
	req.Header.Set("X-Emby-Device-Id", "device-1")
	for key, values := range headers {
		req.Header.Del(key)
		for _, value := range values {
			req.Header.Add(key, value)
		}
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("login status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal login payload: %v", err)
	}
	token, _ := payload["AccessToken"].(string)
	if token == "" {
		t.Fatalf("missing access token: %#v", payload)
	}
	return token
}

func waitForCondition(t *testing.T, timeout time.Duration, check func() bool, description string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition not met before timeout: %s", description)
}
