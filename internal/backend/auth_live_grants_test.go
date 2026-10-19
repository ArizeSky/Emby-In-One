package backend

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A token snapshots AllowedServers at login and never expires on its own, so
// narrowing a user's server access has to take effect on the very next request
// rather than at the next login. This pins the per-request live-grants refresh.
func TestNarrowingAllowedServersAppliesToExistingToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	withTempAppConfig(t, singleUpstreamConfig(srv.URL), func(app *App, _ http.Handler) {
		if app.UserStore == nil {
			t.Fatalf("UserStore is nil; multi-user must be enabled for this test")
		}
		alice, err := app.UserStore.Create("alice", "alice123", []string{"srv-a", "srv-b"})
		if err != nil {
			t.Fatalf("create user: %v", err)
		}
		_, token, err := app.Auth.AuthenticateUser(alice)
		if err != nil {
			t.Fatalf("issue token: %v", err)
		}

		// capture runs the auth middleware for the issued token and returns the
		// request context it built, which is what every handler sees.
		capture := func() *RequestContext {
			var got *RequestContext
			h := app.withContext(func(w http.ResponseWriter, r *http.Request) {
				got = requestContextFrom(r.Context())
			})
			req := httptest.NewRequest(http.MethodGet, "/Users/"+alice.ID+"/Views", nil)
			req.Header.Set("X-Emby-Token", token)
			h(httptest.NewRecorder(), req)
			return got
		}

		first := capture()
		if first == nil || first.ProxyUser == nil {
			t.Fatalf("token was rejected before any change")
		}
		if len(first.ProxyUser.AllowedServers) != 2 {
			t.Fatalf("initial grants = %v, want [srv-a srv-b]", first.ProxyUser.AllowedServers)
		}

		// Admin narrows alice to one server; the already-issued token must follow.
		narrowed := []string{"srv-a"}
		if err := app.UserStore.Update(alice.ID, nil, nil, nil, &narrowed); err != nil {
			t.Fatalf("narrow grants: %v", err)
		}
		second := capture()
		if second == nil || second.ProxyUser == nil {
			t.Fatalf("token was rejected after narrowing")
		}
		if len(second.ProxyUser.AllowedServers) != 1 || second.ProxyUser.AllowedServers[0] != "srv-a" {
			t.Fatalf("grants after narrowing = %v, want [srv-a]", second.ProxyUser.AllowedServers)
		}

		// Widening must apply live too.
		widened := []string{"srv-a", "srv-b", "srv-c"}
		if err := app.UserStore.Update(alice.ID, nil, nil, nil, &widened); err != nil {
			t.Fatalf("widen grants: %v", err)
		}
		if third := capture(); third == nil || third.ProxyUser == nil || len(third.ProxyUser.AllowedServers) != 3 {
			t.Fatalf("grants after widening = %v, want three servers", third.ProxyUser.AllowedServers)
		}

		// Disabling the account still rejects the token outright.
		disabled := false
		if err := app.UserStore.Update(alice.ID, nil, nil, &disabled, nil); err != nil {
			t.Fatalf("disable: %v", err)
		}
		if fourth := capture(); fourth != nil && fourth.ProxyUser != nil {
			t.Fatalf("disabled user's token was still accepted")
		}
	})
}
