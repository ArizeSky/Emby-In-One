package backend

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// --- helpers ---

func loginTokenAs(t *testing.T, handler http.Handler, username, password string) string {
	t.Helper()
	body := fmt.Sprintf(`{"Username":%q,"Pw":%q}`, username, password)
	req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "TestUA/1.0")
	req.Header.Set("X-Emby-Client", "TestClient")
	req.Header.Set("X-Emby-Client-Version", "1.2.3")
	req.Header.Set("X-Emby-Device-Name", "Test Device")
	req.Header.Set("X-Emby-Device-Id", "device-1")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("login as %q: status=%d body=%s", username, rr.Code, rr.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal login response: %v", err)
	}
	token, _ := payload["AccessToken"].(string)
	if token == "" {
		t.Fatalf("no AccessToken for user %q", username)
	}
	return token
}

func doAuthJSON(t *testing.T, handler http.Handler, method, path string, body any, token string) *httptest.ResponseRecorder {
	t.Helper()
	var reqBody *bytes.Buffer
	if body != nil {
		b, _ := json.Marshal(body)
		reqBody = bytes.NewBuffer(b)
	} else {
		reqBody = &bytes.Buffer{}
	}
	req := httptest.NewRequest(method, path, reqBody)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("X-Emby-Token", token)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func singleUpstreamConfig(url string) string {
	return fmt.Sprintf(`server:
  port: 8096
  name: "Test"
  id: "svr"
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
  healthInterval: 60000
proxies: []
upstream:
  - name: "A"
    url: %q
    username: "u1"
    password: "p1"
`, url)
}

func dualUpstreamConfig(url1, url2 string) string {
	return fmt.Sprintf(`server:
  port: 8096
  name: "Test"
  id: "svr"
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
  healthInterval: 60000
proxies: []
upstream:
  - name: "A"
    url: %q
    username: "u1"
    password: "p1"
  - name: "B"
    url: %q
    username: "u2"
    password: "p2"
`, url1, url2)
}

// --- tests ---

func TestMultiUserLoginAndAdminAPI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-a", "User": map[string]any{"Id": "user-a"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	withTempAppConfig(t, singleUpstreamConfig(srv.URL), func(app *App, handler http.Handler) {
		adminToken := loginTokenAs(t, handler, "admin", "secret")

		// 1. Create a regular user
		rr := doAuthJSON(t, handler, http.MethodPost, "/admin/api/users",
			map[string]any{"username": "alice", "password": "alice123"}, adminToken)
		if rr.Code != http.StatusCreated {
			t.Fatalf("create user: status=%d body=%s", rr.Code, rr.Body.String())
		}
		var created map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &created)
		userID, _ := created["id"].(string)
		if userID == "" {
			t.Fatalf("created user missing id")
		}

		// 2. List users
		rr = doAuthJSON(t, handler, http.MethodGet, "/admin/api/users", nil, adminToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("list users: status=%d", rr.Code)
		}
		var users []map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &users)
		if len(users) != 1 {
			t.Fatalf("expected 1 user, got %d", len(users))
		}
		if users[0]["username"] != "alice" {
			t.Fatalf("username = %v, want alice", users[0]["username"])
		}

		// 3. Login as regular user
		userToken := loginTokenAs(t, handler, "alice", "alice123")
		if userToken == "" {
			t.Fatalf("regular user login failed")
		}

		// 4. Regular user cannot access admin API
		rr = doAuthJSON(t, handler, http.MethodGet, "/admin/api/status", nil, userToken)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("regular user admin access: status=%d, want 403", rr.Code)
		}

		// 5. Update user (disable)
		rr = doAuthJSON(t, handler, http.MethodPut, "/admin/api/users/"+userID,
			map[string]any{"enabled": false}, adminToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("update user: status=%d", rr.Code)
		}

		// 6. Disabled user cannot login
		body := `{"Username":"alice","Pw":"alice123"}`
		req := httptest.NewRequest(http.MethodPost, "/Users/AuthenticateByName", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Emby-Client", "TestClient")
		req.Header.Set("X-Emby-Client-Version", "1.0")
		req.Header.Set("X-Emby-Device-Name", "Test")
		req.Header.Set("X-Emby-Device-Id", "d1")
		rr = httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("disabled user login: status=%d, want 401", rr.Code)
		}

		// 7. Delete user
		rr = doAuthJSON(t, handler, http.MethodDelete, "/admin/api/users/"+userID, nil, adminToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("delete user: status=%d", rr.Code)
		}
		rr = doAuthJSON(t, handler, http.MethodGet, "/admin/api/users", nil, adminToken)
		_ = json.Unmarshal(rr.Body.Bytes(), &users)
		if len(users) != 0 {
			t.Fatalf("expected 0 users after delete, got %d", len(users))
		}
	})
}

func TestMultiUserContentFiltering(t *testing.T) {
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-a", "User": map[string]any{"Id": "user-a"}})
		case r.URL.Path == "/Users/user-a/Views":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Items": []map[string]any{{"Id": "view-a", "Name": "Library A"}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srvA.Close()

	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-b", "User": map[string]any{"Id": "user-b"}})
		case r.URL.Path == "/Users/user-b/Views":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Items": []map[string]any{{"Id": "view-b", "Name": "Library B"}},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srvB.Close()

	config := dualUpstreamConfig(srvA.URL, srvB.URL)
	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		adminToken := loginTokenAs(t, handler, "admin", "secret")

		// Create user with access only to server 0 (A)
		rr := doAuthJSON(t, handler, http.MethodPost, "/admin/api/users",
			map[string]any{"username": "bob", "password": "bob123", "allowedServers": []int{0}}, adminToken)
		if rr.Code != http.StatusCreated {
			t.Fatalf("create user: status=%d body=%s", rr.Code, rr.Body.String())
		}

		// Login as bob
		userToken := loginTokenAs(t, handler, "bob", "bob123")

		// Admin sees views from both servers
		rr = doAuthJSON(t, handler, http.MethodGet, "/Users/"+app.Auth.ProxyUserID()+"/Views", nil, adminToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("admin views: status=%d", rr.Code)
		}
		var adminViews map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &adminViews)
		adminItems, _ := adminViews["Items"].([]any)
		if len(adminItems) < 2 {
			t.Fatalf("admin should see >=2 views, got %d", len(adminItems))
		}

		// Bob sees views only from server A
		rr = doAuthJSON(t, handler, http.MethodGet, "/Users/"+app.Auth.ProxyUserID()+"/Views", nil, userToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("bob views: status=%d body=%s", rr.Code, rr.Body.String())
		}
		var bobViews map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &bobViews)
		bobItems, _ := bobViews["Items"].([]any)
		if len(bobItems) != 1 {
			t.Fatalf("bob should see 1 view, got %d", len(bobItems))
		}
	})
}

func TestUsersPublicShowsAllEnabledUsers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName" {
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-a", "User": map[string]any{"Id": "user-a"}})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	withTempAppConfig(t, singleUpstreamConfig(srv.URL), func(app *App, handler http.Handler) {
		adminToken := loginTokenAs(t, handler, "admin", "secret")

		// Create two users, one enabled, one disabled
		doAuthJSON(t, handler, http.MethodPost, "/admin/api/users",
			map[string]any{"username": "carol", "password": "carol1"}, adminToken)

		rr := doAuthJSON(t, handler, http.MethodPost, "/admin/api/users",
			map[string]any{"username": "dave", "password": "dave1"}, adminToken)
		var dave map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &dave)
		daveID, _ := dave["id"].(string)

		doAuthJSON(t, handler, http.MethodPut, "/admin/api/users/"+daveID,
			map[string]any{"enabled": false}, adminToken)

		// /Users/Public should list admin + carol (not dave)
		rr = doAuthJSON(t, handler, http.MethodGet, "/Users/Public", nil, "")
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d", rr.Code)
		}
		var publicUsers []map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &publicUsers)

		names := map[string]bool{}
		for _, u := range publicUsers {
			names[u["Name"].(string)] = true
		}
		if !names["admin"] {
			t.Fatalf("admin not in public users list")
		}
		if !names["carol"] {
			t.Fatalf("carol not in public users list")
		}
		if names["dave"] {
			t.Fatalf("disabled dave should not be in public users list")
		}
	})
}

func TestMultiUserDuplicateUsernameRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName" {
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-a", "User": map[string]any{"Id": "user-a"}})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	withTempAppConfig(t, singleUpstreamConfig(srv.URL), func(app *App, handler http.Handler) {
		adminToken := loginTokenAs(t, handler, "admin", "secret")

		// Create first user
		rr := doAuthJSON(t, handler, http.MethodPost, "/admin/api/users",
			map[string]any{"username": "eve", "password": "eve1"}, adminToken)
		if rr.Code != http.StatusCreated {
			t.Fatalf("status=%d", rr.Code)
		}

		// Try creating duplicate
		rr = doAuthJSON(t, handler, http.MethodPost, "/admin/api/users",
			map[string]any{"username": "eve", "password": "eve2"}, adminToken)
		if rr.Code != http.StatusConflict {
			t.Fatalf("duplicate create: status=%d, want 409", rr.Code)
		}

		// Try creating with admin username
		rr = doAuthJSON(t, handler, http.MethodPost, "/admin/api/users",
			map[string]any{"username": "admin", "password": "x"}, adminToken)
		if rr.Code != http.StatusConflict {
			t.Fatalf("admin-name create: status=%d, want 409", rr.Code)
		}
	})
}

func TestMaxConcurrentInUpstreamAPI(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName" {
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-a", "User": map[string]any{"Id": "user-a"}})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	config := fmt.Sprintf(`server:
  port: 8096
  name: "Test"
  id: "svr"
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
  healthInterval: 60000
proxies: []
upstream:
  - name: "A"
    url: %q
    username: "u1"
    password: "p1"
    maxConcurrent: 3
`, srv.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		adminToken := loginTokenAs(t, handler, "admin", "secret")

		// Verify upstream list reports maxConcurrent
		rr := doAuthJSON(t, handler, http.MethodGet, "/admin/api/upstream", nil, adminToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d", rr.Code)
		}
		var upstreams []map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &upstreams)
		if len(upstreams) != 1 {
			t.Fatalf("expected 1 upstream, got %d", len(upstreams))
		}
		mc, _ := upstreams[0]["maxConcurrent"].(float64)
		if mc != 3 {
			t.Fatalf("maxConcurrent = %v, want 3", upstreams[0]["maxConcurrent"])
		}
	})
}
