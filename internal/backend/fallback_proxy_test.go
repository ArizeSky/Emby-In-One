package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFallbackProxyRoutesUnknownPathByVirtualIDs(t *testing.T) {
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token-a", "User": map[string]any{"Id": "user-a"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Users/user-a/CustomEndpoint/item-a":
			if r.URL.Query().Get("ParentId") != "parent-a" {
				t.Fatalf("ParentId not translated, got %q", r.URL.Query().Get("ParentId"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ItemId":   "item-a",
				"ParentId": "parent-a",
				"UserId":   "user-a",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer primary.Close()

	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test Server\"\n  id: \"server-1\"\n\nadmin:\n  username: \"admin\"\n  password: \"secret\"\n\nplayback:\n  mode: \"proxy\"\n\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\n\nproxies: []\nupstream:\n  - name: \"A\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n", primary.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		virtualItem := app.IDStore.GetOrCreateVirtualID("item-a", 0)
		virtualParent := app.IDStore.GetOrCreateVirtualID("parent-a", 0)
		proxyUser := app.Auth.ProxyUserID()

		rr := doJSONRequest(t, handler, http.MethodGet, "/Users/"+proxyUser+"/CustomEndpoint/"+virtualItem+"?ParentId="+virtualParent, nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("fallback status = %d, body=%s", rr.Code, rr.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal fallback response: %v", err)
		}
		if payload["ItemId"] == "item-a" || payload["ParentId"] == "parent-a" {
			t.Fatalf("fallback ids not rewritten: %#v", payload)
		}
		if payload["UserId"] != proxyUser {
			t.Fatalf("fallback user id = %q, want proxy user %q", payload["UserId"], proxyUser)
		}
	})
}
