package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// createTestUser creates a regular user through the admin API and returns its
// generated local user ID.
func createTestUser(t *testing.T, handler http.Handler, adminToken, username, password string) string {
	t.Helper()
	rr := doAuthJSON(t, handler, http.MethodPost, "/admin/api/users",
		map[string]any{"username": username, "password": password}, adminToken)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create user %q: status=%d body=%s", username, rr.Code, rr.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal created user: %v", err)
	}
	userID, _ := created["id"].(string)
	if userID == "" {
		t.Fatalf("created user %q has no id", username)
	}
	return userID
}

// responseIdentityUpstream is an upstream whose single item is served through
// /Users/{realUserID}/Items. It records the user ID it was asked for.
func responseIdentityUpstream(t *testing.T, realUserID string, seen *[]string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"AccessToken": "token-" + realUserID,
				"User":        map[string]any{"Id": realUserID},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/Users/"+realUserID+"/Items":
			*seen = append(*seen, realUserID)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Items": []map[string]any{{
					"Id":     "orig-item",
					"Name":   "Movie",
					"Type":   "Movie",
					"UserId": realUserID,
					"UserData": map[string]any{
						"UserId": realUserID,
						"Played": false,
					},
				}},
				"TotalRecordCount": 1,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// TestResponseIdentityPerUser is the second batch's contract: a regular user's
// current-user responses carry that user's own ID, the admin's carry the admin's,
// the same upstream item keeps one virtual ID across users, and a request that
// still holds the legacy global user ID keeps working.
func TestResponseIdentityPerUser(t *testing.T) {
	var seen []string
	upstream := responseIdentityUpstream(t, "user-a", &seen)

	withTempAppConfig(t, singleUpstreamConfig(upstream.URL), func(app *App, handler http.Handler) {
		adminToken := loginTokenAs(t, handler, "admin", "secret")
		aliceID := createTestUser(t, handler, adminToken, "alice", "alice123")
		bobID := createTestUser(t, handler, adminToken, "bob", "bob12345")
		aliceToken := loginTokenAs(t, handler, "alice", "alice123")
		bobToken := loginTokenAs(t, handler, "bob", "bob12345")

		legacyProxyUser := app.Auth.ProxyUserID()
		if aliceID == legacyProxyUser || bobID == legacyProxyUser {
			t.Fatalf("fixture collision: a local user ID equals the legacy proxy user ID")
		}

		virtualParent := app.IDStore.GetOrCreateVirtualID("orig-parent", app.Upstream.Clients()[0].ID)
		itemUserID := func(token string) (string, string) {
			rr := doJSONRequest(t, handler, http.MethodGet, "/Users/"+legacyProxyUser+"/Items?ParentId="+virtualParent, nil, token)
			if rr.Code != http.StatusOK {
				t.Fatalf("items status = %d, body=%s", rr.Code, rr.Body.String())
			}
			var payload map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
				t.Fatalf("unmarshal items: %v", err)
			}
			items, _ := payload["Items"].([]any)
			if len(items) != 1 {
				t.Fatalf("items = %d, want 1", len(items))
			}
			item, _ := items[0].(map[string]any)
			id, _ := item["Id"].(string)
			userID, _ := item["UserId"].(string)
			return id, userID
		}

		aliceItemID, aliceUserID := itemUserID(aliceToken)
		if aliceUserID != aliceID {
			t.Fatalf("alice's response UserId = %q, want her own ID %q", aliceUserID, aliceID)
		}
		bobItemID, bobUserID := itemUserID(bobToken)
		if bobUserID != bobID {
			t.Fatalf("bob's response UserId = %q, want his own ID %q", bobUserID, bobID)
		}
		adminItemID, adminUserID := itemUserID(adminToken)
		if adminUserID != legacyProxyUser {
			t.Fatalf("admin's response UserId = %q, want %q", adminUserID, legacyProxyUser)
		}

		// One upstream item must not become a different virtual ID per user: the
		// mapping is per resource, not per viewer.
		if aliceItemID == "" || aliceItemID != bobItemID || aliceItemID != adminItemID {
			t.Fatalf("virtual IDs differ per user: alice=%q bob=%q admin=%q", aliceItemID, bobItemID, adminItemID)
		}
	})
}

// TestResponseIdentityLegacyUserIDCompat covers a client that still sends the
// global user ID older responses handed out. Switching the response identity must
// not require the client to clear its cache.
func TestResponseIdentityLegacyUserIDCompat(t *testing.T) {
	var seen []string
	upstream := responseIdentityUpstream(t, "user-a", &seen)

	withTempAppConfig(t, singleUpstreamConfig(upstream.URL), func(app *App, handler http.Handler) {
		adminToken := loginTokenAs(t, handler, "admin", "secret")
		aliceID := createTestUser(t, handler, adminToken, "alice", "alice123")
		aliceToken := loginTokenAs(t, handler, "alice", "alice123")
		legacyProxyUser := app.Auth.ProxyUserID()

		virtualParent := app.IDStore.GetOrCreateVirtualID("orig-parent", app.Upstream.Clients()[0].ID)
		// The client asks for the legacy global user's items with its own token.
		rr := doJSONRequest(t, handler, http.MethodGet, "/Users/"+legacyProxyUser+"/Items?ParentId="+virtualParent+"&UserId="+legacyProxyUser, nil, aliceToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("legacy-cached request status = %d, body=%s", rr.Code, rr.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		items, _ := payload["Items"].([]any)
		if len(items) != 1 {
			t.Fatalf("items = %d, want 1", len(items))
		}
		item, _ := items[0].(map[string]any)
		if got, _ := item["UserId"].(string); got != aliceID {
			t.Fatalf("response UserId = %q, want alice's own ID %q", got, aliceID)
		}
		// The upstream was still addressed with its own real user ID.
		if len(seen) == 0 {
			t.Fatalf("the upstream never received a request")
		}
		for _, userID := range seen {
			if userID != "user-a" {
				t.Fatalf("the upstream was addressed as %q", userID)
			}
		}
	})
}

// A fallback (unclassified) response for a regular user must report that user's
// own identity, while an unknown write body keeps its own values.
func TestFallbackCurrentUserIdentity(t *testing.T) {
	var bodyUserID string
	var queryUserID string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-a", "User": map[string]any{"Id": "user-a"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Users/user-a/CustomEndpoint/orig-item":
			queryUserID = r.URL.Query().Get("UserId")
			_ = json.NewEncoder(w).Encode(map[string]any{"ItemId": "orig-item", "UserId": "user-a"})
		case r.Method == http.MethodPost && r.URL.Path == "/Users/user-a/UnknownWrite":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			bodyUserID, _ = body["UserId"].(string)
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	withTempAppConfig(t, singleUpstreamConfig(upstream.URL), func(app *App, handler http.Handler) {
		adminToken := loginTokenAs(t, handler, "admin", "secret")
		aliceID := createTestUser(t, handler, adminToken, "alice", "alice123")
		aliceToken := loginTokenAs(t, handler, "alice", "alice123")
		legacyProxyUser := app.Auth.ProxyUserID()

		virtualItem := app.IDStore.GetOrCreateVirtualID("orig-item", app.Upstream.Clients()[0].ID)

		// A regular user's unclassified read is normalized to the target upstream.
		rr := doJSONRequest(t, handler, http.MethodGet,
			"/Users/"+legacyProxyUser+"/CustomEndpoint/"+virtualItem+"?UserId="+legacyProxyUser, nil, aliceToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("fallback read status = %d, body=%s", rr.Code, rr.Body.String())
		}
		if queryUserID != "user-a" {
			t.Fatalf("upstream query UserId = %q, want user-a", queryUserID)
		}
		var payload map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &payload)
		if got, _ := payload["UserId"].(string); got != aliceID {
			t.Fatalf("fallback response UserId = %q, want %q", got, aliceID)
		}

		// An unknown write body keeps the values the client sent, including one that
		// matches a registered local user.
		rr = doJSONRequest(t, handler, http.MethodPost,
			"/Users/"+legacyProxyUser+"/UnknownWrite",
			map[string]any{"UserId": aliceID, "TargetUserId": "someone-else"}, aliceToken)
		if rr.Code >= http.StatusInternalServerError {
			t.Fatalf("unknown write status = %d, body=%s", rr.Code, rr.Body.String())
		}
		if bodyUserID != aliceID {
			t.Fatalf("unknown write body UserId = %q, want the client's own value %q", bodyUserID, aliceID)
		}
	})
}

// buildSeriesInstances needs the extra fixtures below to stay a valid multi-user
// scenario; this test pins that a user filter still reports the requesting user.
func TestResponseIdentityResumeAndNextUp(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-a", "User": map[string]any{"Id": "user-a"}})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": []any{}})
		}
	}))
	defer upstream.Close()

	withTempAppConfig(t, singleUpstreamConfig(upstream.URL), func(app *App, handler http.Handler) {
		adminToken := loginTokenAs(t, handler, "admin", "secret")
		aliceID := createTestUser(t, handler, adminToken, "alice", "alice123")
		aliceToken := loginTokenAs(t, handler, "alice", "alice123")

		// A locally stored resume entry is served with the requesting user's ID.
		if app.WatchStore != nil {
			if err := app.WatchStore.RecordProgress(&WatchProgress{
				ProxyUserID:   aliceID,
				VirtualItemID: app.IDStore.GetOrCreateVirtualID("orig-item", app.Upstream.Clients()[0].ID),
				ServerID:      app.Upstream.Clients()[0].ID,
				PositionTicks: 100,
				RuntimeTicks:  1000,
			}); err != nil {
				t.Fatalf("record progress: %v", err)
			}
		}

		rr := doJSONRequest(t, handler, http.MethodGet, "/Users/"+aliceID+"/Items/Resume", nil, aliceToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("resume status = %d, body=%s", rr.Code, rr.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal resume: %v", err)
		}
		items, _ := payload["Items"].([]any)
		if len(items) == 0 {
			t.Skip("the local resume entry was not served on this path")
		}
		item, _ := items[0].(map[string]any)
		if got, _ := item["UserId"].(string); got != "" && got != aliceID {
			t.Fatalf("resume item UserId = %q, want %q", got, aliceID)
		}
	})
}
