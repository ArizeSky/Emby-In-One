package backend

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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
		virtualItem := app.IDStore.GetOrCreateVirtualID("item-a", app.Upstream.Clients()[0].ID)
		virtualParent := app.IDStore.GetOrCreateVirtualID("parent-a", app.Upstream.Clients()[0].ID)
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

// FIX-02: the buffered fallback paths re-serialize the payload after ID rewriting,
// so the upstream Content-Length no longer describes the bytes we write. net/http
// trusts an explicitly set Content-Length, which silently truncates an over-long body
// (and hangs the client on a short one). This test needs a real server and a real
// http.Client: httptest.ResponseRecorder never applies Content-Length framing, so it
// cannot observe the defect at all.
//
// The upstream body is deliberately larger than 2048 bytes so net/http cannot fall
// back to sniffing a small body, and it carries an explicit Content-Length.
func TestFallbackProxyDoesNotForwardStaleContentLength(t *testing.T) {
	const upstreamLength = 3000
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "upstream-token", "User": map[string]any{"Id": "user-a"}})
		// doRequest normalizes the user segment, so the upstream sees the client-facing
		// user id resolved to the upstream account rather than the virtual one.
		case r.Method == http.MethodGet && r.URL.Path == "/Users/user-a/CustomEndpoint/item-a":
			// Pad with spaces: still valid JSON, and the trailing whitespace survives
			// both encode and decode. Total body is exactly upstreamLength bytes.
			payload := `{"Id":"item-a","Pad":"` + strings.Repeat(" ", upstreamLength-len(`{"Id":"item-a","Pad":""}`)) + `"}`
			if len(payload) != upstreamLength {
				t.Errorf("fixture length = %d, want %d", len(payload), upstreamLength)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			_, _ = io.WriteString(w, payload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	config := fmt.Sprintf("server:\n  port: 8096\n  name: \"Test Server\"\n  id: \"server-1\"\n\nadmin:\n  username: \"admin\"\n  password: \"secret\"\n\nplayback:\n  mode: \"proxy\"\n\ntimeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\n\nproxies: []\nupstream:\n  - name: \"A\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n", upstream.URL)

	withTempAppConfig(t, config, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		proxyUser := app.Auth.ProxyUserID()
		// Reserve the virtual ID up front so the assertion below knows what the
		// rewritten body must contain. GetOrCreateVirtualID("item-a", 0) is exactly
		// what the fallback path resolves through.
		virtualItem := app.IDStore.GetOrCreateVirtualID("item-a", app.Upstream.Clients()[0].ID)

		gateway := httptest.NewServer(handler)
		defer gateway.Close()

		req, err := http.NewRequest(http.MethodGet, gateway.URL+"/Users/"+proxyUser+"/CustomEndpoint/"+virtualItem, nil)
		if err != nil {
			t.Fatalf("build request: %v", err)
		}
		req.Header.Set("X-Emby-Token", token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("gateway request failed (Content-Length framing broke the body): %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			raw, _ := io.ReadAll(resp.Body)
			t.Fatalf("status = %d, want 200 (content-type=%q body=%q)", resp.StatusCode, resp.Header.Get("Content-Type"), raw)
		}

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read body (Content-Length announced more bytes than were sent): %v", err)
		}

		// The header, when present, must describe the body we actually wrote.
		if declared := resp.Header.Get("Content-Length"); declared != "" && declared != strconv.Itoa(len(body)) {
			t.Errorf("Content-Length header = %s but body is %d bytes", declared, len(body))
		}

		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("body is not complete JSON (%d bytes): %v", len(body), err)
		}
		if payload["Id"] == "item-a" {
			t.Errorf("Id was not rewritten: %#v", payload["Id"])
		}
		if payload["Id"] != virtualItem {
			t.Errorf("Id = %v, want the virtual id %q", payload["Id"], virtualItem)
		}
		if pad, _ := payload["Pad"].(string); len(pad) != upstreamLength-len(`{"Id":"item-a","Pad":""}`) {
			t.Errorf("payload was truncated: Pad is %d bytes, want %d", len(pad), upstreamLength-len(`{"Id":"item-a","Pad":""}`))
		}
	})
}
