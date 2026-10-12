package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A user token can outlive its account: the admin API revokes tokens when it
// disables one, but tokens.json and the user store are separate files that can
// drift (restored database, failed store init, direct store edits). Such a
// token must be rejected on the next request, and /Users/{userId} must never
// answer it with the administrator's user object — the old fallback handed a
// disabled user the admin profile, including its IsAdministrator flag.
func TestDisabledUserTokenRejectedImmediately(t *testing.T) {
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

		rr := doAuthJSON(t, handler, http.MethodPost, "/admin/api/users",
			map[string]any{"username": "frank", "password": "frank1234"}, adminToken)
		var created map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &created)
		frankID, _ := created["id"].(string)
		if frankID == "" {
			t.Fatalf("no user id in create response: %s", rr.Body.String())
		}

		// Login while enabled and keep the token.
		frankToken := loginTokenAs(t, handler, "frank", "frank1234")

		// Sanity: the enabled user gets its own object.
		rr = doAuthJSON(t, handler, http.MethodGet, fmt.Sprintf("/Users/%s", frankID), nil, frankToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("enabled user object: status=%d body=%s", rr.Code, rr.Body.String())
		}
		var userObj map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &userObj)
		if name, _ := userObj["Name"].(string); name != "frank" {
			t.Fatalf("enabled user object name = %q, want frank", name)
		}

		// Disable the account directly in the store, bypassing the admin API's
		// token revocation: this is the tokens.json / users.db drift state.
		disabled := false
		if err := app.UserStore.Update(frankID, nil, nil, &disabled, nil); err != nil {
			t.Fatalf("disable frank in store: %v", err)
		}

		// The still-held token must now be rejected on the user object route...
		rr = doAuthJSON(t, handler, http.MethodGet, fmt.Sprintf("/Users/%s", frankID), nil, frankToken)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("disabled user object: status=%d body=%s, want 401", rr.Code, rr.Body.String())
		}
		// ...and the body must not be the admin user object.
		var payload map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err == nil {
			if name, _ := payload["Name"].(string); name == "admin" {
				t.Fatalf("disabled user received the admin user object: %s", rr.Body.String())
			}
		}

		// ...and on every other authenticated route.
		rr = doAuthJSON(t, handler, http.MethodGet, "/System/Info", nil, frankToken)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("disabled user system info: status=%d, want 401", rr.Code)
		}

		// The admin token is unaffected.
		rr = doAuthJSON(t, handler, http.MethodGet, "/System/Info", nil, adminToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("admin system info: status=%d, want 200", rr.Code)
		}
	})
}

// Deleting the account behind a token (again bypassing the admin API) must
// cut the token the same way disabling does.
func TestDeletedUserTokenRejectedImmediately(t *testing.T) {
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

		rr := doAuthJSON(t, handler, http.MethodPost, "/admin/api/users",
			map[string]any{"username": "grace", "password": "grace1234"}, adminToken)
		var created map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &created)
		graceID, _ := created["id"].(string)
		if graceID == "" {
			t.Fatalf("no user id in create response: %s", rr.Body.String())
		}

		graceToken := loginTokenAs(t, handler, "grace", "grace1234")

		if err := app.UserStore.Delete(graceID); err != nil {
			t.Fatalf("delete grace in store: %v", err)
		}

		rr = doAuthJSON(t, handler, http.MethodGet, fmt.Sprintf("/Users/%s", graceID), nil, graceToken)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("deleted user object: status=%d body=%s, want 401", rr.Code, rr.Body.String())
		}
		rr = doAuthJSON(t, handler, http.MethodGet, "/System/Info", nil, graceToken)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("deleted user system info: status=%d, want 401", rr.Code)
		}
	})
}
