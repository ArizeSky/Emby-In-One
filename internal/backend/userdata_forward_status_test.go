package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

 // An explicit upstream refusal must keep its own status code. The three
 // userdata forwards used to answer 500 for every error, so an upstream 404
 // or 409 looked like a proxy bug and clients retried a refusal.
func TestUserdataForwardKeepsUpstreamStatus(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName" {
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "tok-a", "User": map[string]any{"Id": "user-a"}})
 			return
 		}
 		switch {
 		case r.Method == http.MethodPost && r.URL.Path == "/Users/user-a/Items/item-a/UserData":
 			// 404/409/422 on purpose: 401/403 would trigger the upstream
 			// client's async auth recovery and race the next request.
 			w.WriteHeader(http.StatusNotFound)
 		case r.Method == http.MethodPost && r.URL.Path == "/Users/user-a/FavoriteItems/item-a":
 			w.WriteHeader(http.StatusConflict)
 		case r.Method == http.MethodDelete && r.URL.Path == "/Users/user-a/FavoriteItems/item-a":
 			w.WriteHeader(http.StatusUnprocessableEntity)
 		default:
 			http.NotFound(w, r)
 		}
	}))
	defer upstream.Close()

	withTempAppConfig(t, singleUpstreamConfig(upstream.URL), func(app *App, handler http.Handler) {
		userToken := createRegularUser(t, handler)
		virtualItem := app.IDStore.GetOrCreateVirtualID("item-a", app.Upstream.Clients()[0].ID)

		rr := doAuthJSON(t, handler, http.MethodPost, "/Users/"+app.Auth.ProxyUserID()+"/Items/"+virtualItem+"/UserData",
			map[string]any{"Played": true}, userToken)
 		if rr.Code != http.StatusNotFound {
 			t.Fatalf("UserData status = %d, want 404 (body=%s)", rr.Code, rr.Body.String())
 		}
 
 		rr = doAuthJSON(t, handler, http.MethodPost, "/Users/"+app.Auth.ProxyUserID()+"/FavoriteItems/"+virtualItem, nil, userToken)
 		if rr.Code != http.StatusConflict {
 			t.Fatalf("favorite add status = %d, want 409 (body=%s)", rr.Code, rr.Body.String())
 		}
 
 		rr = doAuthJSON(t, handler, http.MethodDelete, "/Users/"+app.Auth.ProxyUserID()+"/FavoriteItems/"+virtualItem, nil, userToken)
 		if rr.Code != http.StatusUnprocessableEntity {
 			t.Fatalf("favorite remove status = %d, want 422 (body=%s)", rr.Code, rr.Body.String())
 		}

		// A refusal must not be recorded locally either.
		if progress, _ := app.WatchStore.GetProgress(watchUserID(t, app, "child"), virtualItem); progress != nil {
			t.Fatalf("local watch record written despite the refusal: %#v", progress)
		}
	})
}
