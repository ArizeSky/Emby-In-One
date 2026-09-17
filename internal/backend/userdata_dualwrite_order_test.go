package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// watchUserID resolves the UserStore id (the key WatchStore uses) from a username.
func watchUserID(t *testing.T, app *App, username string) string {
	t.Helper()
	for _, u := range app.UserStore.List() {
		if u.Username == username {
			return u.ID
		}
	}
	t.Fatalf("user %q not found in UserStore", username)
	return ""
}

// FIX-11: handleUserItemUserData wrote the local MarkPlayed record before forwarding
// to the upstream. When the forward failed the handler answered 500 while the local
// "played" flag had already been persisted, so the two sides stayed permanently out of
// sync and the user's watched state depended on which view asked. handleFavoriteItemAdd
// and handleFavoriteItemRemove already dual-write after a successful forward; this
// aligns UserData with them.
func TestUserDataDualWriteHappensAfterForward(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName" {
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-a", "User": map[string]any{"Id": "user-a"}})
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/Users/user-a/Items/item-a/UserData" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	withTempAppConfig(t, singleUpstreamConfig(upstream.URL), func(app *App, handler http.Handler) {
		userToken := createRegularUser(t, handler)
		virtualItem := app.IDStore.GetOrCreateVirtualID("item-a", app.Upstream.Clients()[0].ID)

		rr := doAuthJSON(t, handler, http.MethodPost, "/Users/"+app.Auth.ProxyUserID()+"/Items/"+virtualItem+"/UserData",
			map[string]any{"Played": true}, userToken)
		if rr.Code != http.StatusInternalServerError {
			t.Fatalf("UserData status = %d, want 500 (body=%s)", rr.Code, rr.Body.String())
		}

		// The upstream rejected the change, so nothing may have been recorded locally.
		if progress := app.WatchStore.GetProgress(watchUserID(t, app, "child"), virtualItem); progress != nil {
			t.Fatalf("local watch record written despite a failed forward: %#v", progress)
		}
		played, err := app.WatchStore.GetPlayedItems(watchUserID(t, app, "child"))
		if err != nil {
			t.Fatalf("GetPlayedItems: %v", err)
		}
		if len(played) != 0 {
			t.Fatalf("local played list is not empty after a failed forward: %#v", played)
		}
	})
}

// The success path must still dual-write, and the local record must carry the played
// flag. Without this the fix could simply delete the dual-write.
func TestUserDataDualWriteStillHappensOnSuccess(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName" {
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-a", "User": map[string]any{"Id": "user-a"}})
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/Users/user-a/Items/item-a/UserData" {
			_ = json.NewEncoder(w).Encode(map[string]any{"Played": true, "ItemId": "item-a"})
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	withTempAppConfig(t, singleUpstreamConfig(upstream.URL), func(app *App, handler http.Handler) {
		userToken := createRegularUser(t, handler)
		virtualItem := app.IDStore.GetOrCreateVirtualID("item-a", app.Upstream.Clients()[0].ID)

		rr := doAuthJSON(t, handler, http.MethodPost, "/Users/"+app.Auth.ProxyUserID()+"/Items/"+virtualItem+"/UserData",
			map[string]any{"Played": true}, userToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("UserData status = %d, want 200 (body=%s)", rr.Code, rr.Body.String())
		}

		progress := app.WatchStore.GetProgress(watchUserID(t, app, "child"), virtualItem)
		if progress == nil {
			t.Fatal("no local watch record after a successful forward")
		}
		if !progress.Played {
			t.Fatalf("local record should be marked played: %#v", progress)
		}
		played, err := app.WatchStore.GetPlayedItems(watchUserID(t, app, "child"))
		if err != nil {
			t.Fatalf("GetPlayedItems: %v", err)
		}
		if len(played) != 1 || played[0].VirtualItemID != virtualItem {
			t.Fatalf("played list = %#v, want exactly %q", played, virtualItem)
		}
	})
}

// Forward failures must not leave a skeleton row behind either: MarkPlayed inserts a
// record with server_index = 0 when none exists, which previously happened before the
// upstream was even contacted.
func TestUserDataFailedForwardDoesNotCreateSkeletonRecord(t *testing.T) {
	var attempts int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName" {
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-a", "User": map[string]any{"Id": "user-a"}})
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/Users/user-a/Items/item-a/UserData" {
			atomic.AddInt32(&attempts, 1)
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	withTempAppConfig(t, singleUpstreamConfig(upstream.URL), func(app *App, handler http.Handler) {
		userToken := createRegularUser(t, handler)
		virtualItem := app.IDStore.GetOrCreateVirtualID("item-a", app.Upstream.Clients()[0].ID)

		for i := 0; i < 3; i++ {
			rr := doAuthJSON(t, handler, http.MethodPost, "/Users/"+app.Auth.ProxyUserID()+"/Items/"+virtualItem+"/UserData",
				map[string]any{"Played": true}, userToken)
			if rr.Code != http.StatusInternalServerError {
				t.Fatalf("attempt %d: status = %d, want 500", i, rr.Code)
			}
		}
		if got := atomic.LoadInt32(&attempts); got != 3 {
			t.Fatalf("upstream saw %d forward(s), want 3", got)
		}
		if progress := app.WatchStore.GetProgress(watchUserID(t, app, "child"), virtualItem); progress != nil {
			t.Fatalf("skeleton record created by failed forwards: %#v", progress)
		}
	})
}
