package backend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// filterStubUpstream serves a small library and — crucially — answers user-state
// filters the way a real upstream does: with the *shared* account's state. That is
// what lets these tests tell "the proxy asked the shared account" apart from "the
// proxy answered from the local record".
type filterStubUpstream struct {
	server *httptest.Server

	mu       sync.Mutex
	itemReqs []url.Values
}

func newFilterStub(t *testing.T, items []map[string]any) *filterStubUpstream {
	t.Helper()
	stub := &filterStubUpstream{}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"AccessToken": "tok-a",
				"User":        map[string]any{"Id": "user-a"},
			})
		case r.Method == http.MethodGet && r.URL.Path == "/Users/user-a/Items":
			query := r.URL.Query()
			stub.recordItemRequest(query)
			page, total := filterStubPage(items, query)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Items":            toAnySlice(page),
				"TotalRecordCount": total,
				"StartIndex":       0,
			})
		case strings.HasSuffix(r.URL.Path, "/FavoriteItems/") || strings.Contains(r.URL.Path, "/FavoriteItems/"):
			w.WriteHeader(http.StatusNoContent)
		case strings.Contains(r.URL.Path, "/UserData"):
			w.WriteHeader(http.StatusNoContent)
		case r.URL.Path == "/Sessions/Playing/Progress" || r.URL.Path == "/Sessions/Playing":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

// filterStubPage applies the user-state filters the way the shared upstream account
// would, then the paging the proxy asked for.
func filterStubPage(items []map[string]any, query url.Values) ([]map[string]any, int) {
	filters := query.Get("Filters")
	matching := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if filterStubMatches(item, filters, query.Get("ParentId")) {
			matching = append(matching, item)
		}
	}
	total := len(matching)
	start := 0
	if raw := query.Get("StartIndex"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			start = parsed
		}
	}
	if start > len(matching) {
		start = len(matching)
	}
	matching = matching[start:]
	if raw := query.Get("Limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed >= 0 && parsed < len(matching) {
			matching = matching[:parsed]
		}
	}
	return matching, total
}

func filterStubMatches(item map[string]any, filters, parentID string) bool {
	if parentID != "" {
		if parent, _ := item["ParentId"].(string); parent != parentID {
			return false
		}
	}
	userData, _ := item["UserData"].(map[string]any)
	for _, value := range strings.Split(filters, ",") {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		switch strings.ToLower(value) {
		case "isfavorite":
			if favorite, _ := userData["IsFavorite"].(bool); !favorite {
				return false
			}
		case "isplayed":
			if played, _ := userData["Played"].(bool); !played {
				return false
			}
		case "isresumable":
			if position, _ := numericInt(userData["PlaybackPositionTicks"]); position <= 0 {
				return false
			}
		}
	}
	return true
}

func (s *filterStubUpstream) recordItemRequest(query url.Values) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.itemReqs = append(s.itemReqs, cloneValues(query))
}

func (s *filterStubUpstream) lastItemRequest(t *testing.T) url.Values {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.itemReqs) == 0 {
		t.Fatal("upstream received no /Users/user-a/Items request")
	}
	return s.itemReqs[len(s.itemReqs)-1]
}

// filterStubItem builds one library entry carrying the shared account's UserData.
func filterStubItem(id, name, parentID string, year int, userData map[string]any) map[string]any {
	return map[string]any{
		"Id":              id,
		"Name":            name,
		"SortName":        name,
		"Type":            "Movie",
		"ParentId":        parentID,
		"ProductionYear":  year,
		"CommunityRating": 7.5,
		"UserData":        userData,
	}
}

func filterStubUserData(favorite, played bool, position int) map[string]any {
	return map[string]any{
		"IsFavorite":            favorite,
		"Played":                played,
		"PlaybackPositionTicks": position,
	}
}

// --- response helpers ---

func createRegularUser(t *testing.T, handler http.Handler) string {
	t.Helper()
	adminToken := loginTokenAs(t, handler, "admin", "secret")
	rr := doJSONRequest(t, handler, http.MethodPost, "/admin/api/users",
		map[string]any{"username": "child", "password": "child123"}, adminToken)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create user: status=%d body=%s", rr.Code, rr.Body.String())
	}
	return loginTokenAs(t, handler, "child", "child123")
}

func favoriteLocally(t *testing.T, handler http.Handler, app *App, token, virtualID string) {
	t.Helper()
	rr := doJSONRequest(t, handler, http.MethodPost,
		"/Users/"+app.Auth.ProxyUserID()+"/FavoriteItems/"+virtualID, nil, token)
	if rr.Code != http.StatusNoContent && rr.Code != http.StatusOK {
		t.Fatalf("favorite %s: status=%d body=%s", virtualID, rr.Code, rr.Body.String())
	}
}

func markPlayedLocally(t *testing.T, handler http.Handler, app *App, token, virtualID string) {
	t.Helper()
	rr := doJSONRequest(t, handler, http.MethodPost,
		"/Users/"+app.Auth.ProxyUserID()+"/Items/"+virtualID+"/UserData",
		map[string]any{"Played": true}, token)
	if rr.Code != http.StatusNoContent && rr.Code != http.StatusOK {
		t.Fatalf("mark played %s: status=%d body=%s", virtualID, rr.Code, rr.Body.String())
	}
}

func markInProgressLocally(t *testing.T, handler http.Handler, token, virtualID string, positionTicks int) {
	t.Helper()
	rr := doJSONRequest(t, handler, http.MethodPost, "/Sessions/Playing/Progress",
		map[string]any{"ItemId": virtualID, "PositionTicks": positionTicks}, token)
	if rr.Code != http.StatusNoContent && rr.Code != http.StatusOK {
		t.Fatalf("session progress %s: status=%d body=%s", virtualID, rr.Code, rr.Body.String())
	}
}

func decodeItemsResponse(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal items response: %v body=%s", err, body)
	}
	return payload
}

func itemNames(t *testing.T, body []byte) []string {
	t.Helper()
	payload := decodeItemsResponse(t, body)
	raw, _ := payload["Items"].([]any)
	names := make([]string, 0, len(raw))
	for _, entry := range raw {
		item, _ := entry.(map[string]any)
		name, _ := item["Name"].(string)
		names = append(names, name)
	}
	return names
}

func itemTotal(t *testing.T, body []byte) int {
	t.Helper()
	payload := decodeItemsResponse(t, body)
	total, _ := payload["TotalRecordCount"].(float64)
	return int(total)
}

func itemUserDataOf(t *testing.T, body []byte, name string) map[string]any {
	t.Helper()
	payload := decodeItemsResponse(t, body)
	raw, _ := payload["Items"].([]any)
	for _, entry := range raw {
		item, _ := entry.(map[string]any)
		if item["Name"] == name {
			userData, _ := item["UserData"].(map[string]any)
			return userData
		}
	}
	t.Fatalf("item %q missing from response: %s", name, body)
	return nil
}

// --- tests ---

// TestUserItemsFavoriteFilterUsesLocalState covers the gap the local overlay could
// never close: the shared account decides which items come back at all, so a locally
// favorited item the shared account never favorited was simply unreachable.
func TestUserItemsFavoriteFilterUsesLocalState(t *testing.T) {
	// The shared account favorites Alpha and Bravo; the local user favorites Bravo
	// and Charlie. Charlie is only reachable once the upstream filter stops deciding.
	stub := newFilterStub(t, []map[string]any{
		filterStubItem("movie-a", "Alpha", "lib-1", 2001, filterStubUserData(true, false, 0)),
		filterStubItem("movie-b", "Bravo", "lib-1", 2002, filterStubUserData(true, false, 0)),
		filterStubItem("movie-c", "Charlie", "lib-1", 2003, filterStubUserData(false, false, 0)),
	})

	withTempAppConfig(t, singleUpstreamConfig(stub.server.URL), func(app *App, handler http.Handler) {
		serverID := app.Upstream.Clients()[0].ID
		userToken := createRegularUser(t, handler)
		favoriteLocally(t, handler, app, userToken, app.IDStore.GetOrCreateVirtualID("movie-b", serverID))
		favoriteLocally(t, handler, app, userToken, app.IDStore.GetOrCreateVirtualID("movie-c", serverID))

		rr := doJSONRequest(t, handler, http.MethodGet,
			"/Users/"+app.Auth.ProxyUserID()+"/Items?Filters=IsFavorite&SortBy=SortName&SortOrder=Ascending",
			nil, userToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
		}
		if got := itemNames(t, rr.Body.Bytes()); !reflect.DeepEqual(got, []string{"Bravo", "Charlie"}) {
			t.Fatalf("favorite filter returned %v, want [Bravo Charlie]", got)
		}
		if got := itemTotal(t, rr.Body.Bytes()); got != 2 {
			t.Fatalf("TotalRecordCount = %d, want 2", got)
		}
		// The shared account's marks must not leak onto the surviving items.
		alphaUserData := itemUserDataOf(t, rr.Body.Bytes(), "Bravo")
		if alphaUserData["IsFavorite"] != true {
			t.Fatalf("local favorite missing from the response: %#v", alphaUserData)
		}

		if forwarded := stub.lastItemRequest(t).Get("Filters"); forwarded != "" {
			t.Fatalf("Filters=%q was still forwarded upstream", forwarded)
		}
	})
}

func TestUserItemsPlayedFilterUsesLocalState(t *testing.T) {
	// The shared account has played Alpha; the local user has played Bravo.
	stub := newFilterStub(t, []map[string]any{
		filterStubItem("movie-a", "Alpha", "lib-1", 2001, filterStubUserData(false, true, 0)),
		filterStubItem("movie-b", "Bravo", "lib-1", 2002, filterStubUserData(false, false, 0)),
		filterStubItem("movie-c", "Charlie", "lib-1", 2003, filterStubUserData(false, false, 0)),
	})

	withTempAppConfig(t, singleUpstreamConfig(stub.server.URL), func(app *App, handler http.Handler) {
		serverID := app.Upstream.Clients()[0].ID
		userToken := createRegularUser(t, handler)
		markPlayedLocally(t, handler, app, userToken, app.IDStore.GetOrCreateVirtualID("movie-b", serverID))

		rr := doJSONRequest(t, handler, http.MethodGet,
			"/Users/"+app.Auth.ProxyUserID()+"/Items?Filters=IsPlayed&SortBy=SortName&SortOrder=Ascending",
			nil, userToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
		}
		if got := itemNames(t, rr.Body.Bytes()); !reflect.DeepEqual(got, []string{"Bravo"}) {
			t.Fatalf("played filter returned %v, want [Bravo]", got)
		}
		if userData := itemUserDataOf(t, rr.Body.Bytes(), "Bravo"); userData["Played"] != true {
			t.Fatalf("local played state missing: %#v", userData)
		}
		if forwarded := stub.lastItemRequest(t).Get("Filters"); forwarded != "" {
			t.Fatalf("Filters=%q was still forwarded upstream", forwarded)
		}
	})
}

func TestUserItemsResumableFilterUsesLocalState(t *testing.T) {
	// The shared account has an in-progress item; the local user has a different one.
	stub := newFilterStub(t, []map[string]any{
		filterStubItem("movie-a", "Alpha", "lib-1", 2001, filterStubUserData(false, false, 999)),
		filterStubItem("movie-b", "Bravo", "lib-1", 2002, filterStubUserData(false, false, 0)),
		filterStubItem("movie-c", "Charlie", "lib-1", 2003, filterStubUserData(false, false, 0)),
	})

	withTempAppConfig(t, singleUpstreamConfig(stub.server.URL), func(app *App, handler http.Handler) {
		serverID := app.Upstream.Clients()[0].ID
		userToken := createRegularUser(t, handler)
		markInProgressLocally(t, handler, userToken, app.IDStore.GetOrCreateVirtualID("movie-c", serverID), 5000)

		rr := doJSONRequest(t, handler, http.MethodGet,
			"/Users/"+app.Auth.ProxyUserID()+"/Items?Filters=IsResumable&SortBy=SortName&SortOrder=Ascending",
			nil, userToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
		}
		if got := itemNames(t, rr.Body.Bytes()); !reflect.DeepEqual(got, []string{"Charlie"}) {
			t.Fatalf("resumable filter returned %v, want [Charlie]", got)
		}
		userData := itemUserDataOf(t, rr.Body.Bytes(), "Charlie")
		if position, _ := numericInt(userData["PlaybackPositionTicks"]); position != 5000 {
			t.Fatalf("local position missing: %#v", userData)
		}
		if forwarded := stub.lastItemRequest(t).Get("Filters"); forwarded != "" {
			t.Fatalf("Filters=%q was still forwarded upstream", forwarded)
		}
	})
}

// TestUserItemsFilterPaginationAndTotalCount pins the paging contract: the page is
// cut from the *local* result, and the total describes that result, not the
// upstream's own count.
func TestUserItemsFilterPaginationAndTotalCount(t *testing.T) {
	stub := newFilterStub(t, []map[string]any{
		filterStubItem("movie-a", "Alpha", "lib-1", 2001, filterStubUserData(false, false, 0)),
		filterStubItem("movie-b", "Bravo", "lib-1", 2002, filterStubUserData(false, false, 0)),
		filterStubItem("movie-c", "Charlie", "lib-1", 2003, filterStubUserData(false, false, 0)),
		filterStubItem("movie-d", "Delta", "lib-1", 2004, filterStubUserData(false, false, 0)),
		filterStubItem("movie-e", "Echo", "lib-1", 2005, filterStubUserData(false, false, 0)),
	})

	withTempAppConfig(t, singleUpstreamConfig(stub.server.URL), func(app *App, handler http.Handler) {
		serverID := app.Upstream.Clients()[0].ID
		userToken := createRegularUser(t, handler)
		for _, original := range []string{"movie-a", "movie-b", "movie-c", "movie-d", "movie-e"} {
			favoriteLocally(t, handler, app, userToken, app.IDStore.GetOrCreateVirtualID(original, serverID))
		}

		rr := doJSONRequest(t, handler, http.MethodGet,
			"/Users/"+app.Auth.ProxyUserID()+"/Items?Filters=IsFavorite&SortBy=SortName&SortOrder=Ascending&StartIndex=2&Limit=2",
			nil, userToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
		}
		if got := itemNames(t, rr.Body.Bytes()); !reflect.DeepEqual(got, []string{"Charlie", "Delta"}) {
			t.Fatalf("page returned %v, want [Charlie Delta]", got)
		}
		if got := itemTotal(t, rr.Body.Bytes()); got != 5 {
			t.Fatalf("TotalRecordCount = %d, want 5", got)
		}
	})
}

// TestUserItemsFilterScopedByParentId keeps a filtered list inside the container the
// client asked for: favorites from another library must not leak in.
func TestUserItemsFilterScopedByParentId(t *testing.T) {
	stub := newFilterStub(t, []map[string]any{
		filterStubItem("movie-a", "Alpha", "lib-1", 2001, filterStubUserData(false, false, 0)),
		filterStubItem("movie-b", "Bravo", "lib-1", 2002, filterStubUserData(false, false, 0)),
		filterStubItem("movie-c", "Charlie", "lib-2", 2003, filterStubUserData(false, false, 0)),
	})

	withTempAppConfig(t, singleUpstreamConfig(stub.server.URL), func(app *App, handler http.Handler) {
		serverID := app.Upstream.Clients()[0].ID
		userToken := createRegularUser(t, handler)
		favoriteLocally(t, handler, app, userToken, app.IDStore.GetOrCreateVirtualID("movie-a", serverID))
		favoriteLocally(t, handler, app, userToken, app.IDStore.GetOrCreateVirtualID("movie-c", serverID))

		libraryID := app.IDStore.GetOrCreateVirtualID("lib-1", serverID)
		rr := doJSONRequest(t, handler, http.MethodGet,
			"/Users/"+app.Auth.ProxyUserID()+"/Items?ParentId="+libraryID+"&Filters=IsFavorite&SortBy=SortName&SortOrder=Ascending",
			nil, userToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
		}
		if got := itemNames(t, rr.Body.Bytes()); !reflect.DeepEqual(got, []string{"Alpha"}) {
			t.Fatalf("filtered library returned %v, want [Alpha]", got)
		}
		if got := itemTotal(t, rr.Body.Bytes()); got != 1 {
			t.Fatalf("TotalRecordCount = %d, want 1", got)
		}

		forwarded := stub.lastItemRequest(t)
		if forwarded.Get("ParentId") != "lib-1" {
			t.Fatalf("ParentId = %q, want lib-1", forwarded.Get("ParentId"))
		}
		if forwarded.Get("Filters") != "" {
			t.Fatalf("Filters=%q was still forwarded upstream", forwarded.Get("Filters"))
		}
	})
}

// TestUserItemsUnplayedFilterPassesThroughAndHints covers the filter the proxy
// cannot answer: it stays upstream, and the operator gets told that it still
// follows the shared account.
func TestUserItemsUnplayedFilterPassesThroughAndHints(t *testing.T) {
	stub := newFilterStub(t, []map[string]any{
		filterStubItem("movie-a", "Alpha", "lib-1", 2001, filterStubUserData(false, true, 0)),
		filterStubItem("movie-b", "Bravo", "lib-1", 2002, filterStubUserData(false, false, 0)),
	})

	withTempAppConfig(t, singleUpstreamConfig(stub.server.URL), func(app *App, handler http.Handler) {
		userToken := createRegularUser(t, handler)

		rr := doJSONRequest(t, handler, http.MethodGet,
			"/Users/"+app.Auth.ProxyUserID()+"/Items?Filters=IsUnplayed&Limit=50", nil, userToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
		}

		forwarded := stub.lastItemRequest(t)
		if forwarded.Get("Filters") != "IsUnplayed" {
			t.Fatalf("Filters = %q, want IsUnplayed forwarded untouched", forwarded.Get("Filters"))
		}
		// Nothing here localizes the filter, so the upstream's answer is served as-is.
		// (The paging window is the proxy's business on this path and is covered by the
		// merged-path paging tests, so it is not asserted here.)
		if got := itemNames(t, rr.Body.Bytes()); !reflect.DeepEqual(got, []string{"Alpha", "Bravo"}) {
			t.Fatalf("unplayed filter returned %v, want the upstream answer [Alpha Bravo]", got)
		}
		if notice := rr.Header().Get(filterNoticeHeader); !strings.Contains(notice, "IsUnplayed") {
			t.Fatalf("missing filter notice header, got %q", notice)
		}
		if !filterNoticeLogged(app, "IsUnplayed") {
			t.Fatalf("no warning logged for the un-isolated filter; entries: %#v", app.Logger.Entries(0))
		}
	})
}

// TestUserItemsUnplayedFilterHintsRegularUsersOnly keeps the hint off the admin's
// screen: for an admin the shared account's state *is* their own, so calling it
// "not isolated per user" would be misleading.
func TestUserItemsUnplayedFilterHintsRegularUsersOnly(t *testing.T) {
	stub := newFilterStub(t, []map[string]any{
		filterStubItem("movie-a", "Alpha", "lib-1", 2001, filterStubUserData(false, false, 0)),
	})

	withTempAppConfig(t, singleUpstreamConfig(stub.server.URL), func(app *App, handler http.Handler) {
		adminToken := loginToken(t, handler, "secret")
		rr := doJSONRequest(t, handler, http.MethodGet,
			"/Users/"+app.Auth.ProxyUserID()+"/Items?Filters=IsUnplayed", nil, adminToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
		}
		if notice := rr.Header().Get(filterNoticeHeader); notice != "" {
			t.Fatalf("admin got a filter notice header: %q", notice)
		}
	})
}

// TestUserItemsFilterLeavesAdminsAlone keeps the upstream view byte-identical for the
// administrator, who has no local record to prefer.
func TestUserItemsFilterLeavesAdminsAlone(t *testing.T) {
	stub := newFilterStub(t, []map[string]any{
		filterStubItem("movie-a", "Alpha", "lib-1", 2001, filterStubUserData(true, true, 0)),
		filterStubItem("movie-b", "Bravo", "lib-1", 2002, filterStubUserData(true, false, 0)),
		filterStubItem("movie-c", "Charlie", "lib-1", 2003, filterStubUserData(false, false, 0)),
	})

	withTempAppConfig(t, singleUpstreamConfig(stub.server.URL), func(app *App, handler http.Handler) {
		adminToken := loginToken(t, handler, "secret")

		rr := doJSONRequest(t, handler, http.MethodGet,
			"/Users/"+app.Auth.ProxyUserID()+"/Items?Filters=IsFavorite&Limit=5", nil, adminToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
		}
		if got := itemNames(t, rr.Body.Bytes()); !reflect.DeepEqual(got, []string{"Alpha", "Bravo"}) {
			t.Fatalf("admin saw %v, want the upstream favorites [Alpha Bravo]", got)
		}

		forwarded := stub.lastItemRequest(t)
		if forwarded.Get("Filters") != "IsFavorite" {
			t.Fatalf("Filters = %q, want it forwarded untouched for an admin", forwarded.Get("Filters"))
		}
		// The paging window is not asserted here: it belongs to the merged path and is
		// covered by the paging tests. What matters is that no local filtering ran.
		if notice := rr.Header().Get(filterNoticeHeader); notice != "" {
			t.Fatalf("admin got a filter notice header: %q", notice)
		}
	})
}

// TestUserItemsUnknownFilterValuesPassThrough makes sure stripping the localizable
// values never silently drops the rest of the client's filter list.
func TestUserItemsUnknownFilterValuesPassThrough(t *testing.T) {
	stub := newFilterStub(t, []map[string]any{
		filterStubItem("movie-a", "Alpha", "lib-1", 2001, filterStubUserData(true, false, 0)),
		filterStubItem("movie-b", "Bravo", "lib-1", 2002, filterStubUserData(false, false, 0)),
		filterStubItem("movie-c", "Charlie", "lib-1", 2003, filterStubUserData(false, false, 0)),
	})

	withTempAppConfig(t, singleUpstreamConfig(stub.server.URL), func(app *App, handler http.Handler) {
		serverID := app.Upstream.Clients()[0].ID
		userToken := createRegularUser(t, handler)
		favoriteLocally(t, handler, app, userToken, app.IDStore.GetOrCreateVirtualID("movie-b", serverID))
		favoriteLocally(t, handler, app, userToken, app.IDStore.GetOrCreateVirtualID("movie-c", serverID))

		rr := doJSONRequest(t, handler, http.MethodGet,
			"/Users/"+app.Auth.ProxyUserID()+"/Items?Filters=IsFolder,IsFavorite&SortBy=SortName&SortOrder=Ascending",
			nil, userToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
		}
		if got := itemNames(t, rr.Body.Bytes()); !reflect.DeepEqual(got, []string{"Bravo", "Charlie"}) {
			t.Fatalf("filter returned %v, want [Bravo Charlie]", got)
		}
		if forwarded := stub.lastItemRequest(t).Get("Filters"); forwarded != "IsFolder" {
			t.Fatalf("Filters = %q, want the unknown value kept", forwarded)
		}
	})
}

func TestUserItemsFilterSortsLocally(t *testing.T) {
	stub := newFilterStub(t, []map[string]any{
		filterStubItem("movie-a", "Alpha", "lib-1", 2001, filterStubUserData(false, false, 0)),
		filterStubItem("movie-b", "Bravo", "lib-1", 2003, filterStubUserData(false, false, 0)),
		filterStubItem("movie-c", "Charlie", "lib-1", 2002, filterStubUserData(false, false, 0)),
	})

	withTempAppConfig(t, singleUpstreamConfig(stub.server.URL), func(app *App, handler http.Handler) {
		serverID := app.Upstream.Clients()[0].ID
		userToken := createRegularUser(t, handler)
		for _, original := range []string{"movie-a", "movie-b", "movie-c"} {
			favoriteLocally(t, handler, app, userToken, app.IDStore.GetOrCreateVirtualID(original, serverID))
		}

		rr := doJSONRequest(t, handler, http.MethodGet,
			"/Users/"+app.Auth.ProxyUserID()+"/Items?Filters=IsFavorite&SortBy=ProductionYear&SortOrder=Descending",
			nil, userToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
		}
		if got := itemNames(t, rr.Body.Bytes()); !reflect.DeepEqual(got, []string{"Bravo", "Charlie", "Alpha"}) {
			t.Fatalf("sorted page = %v, want [Bravo Charlie Alpha]", got)
		}
	})
}

// TestUserItemsFilterAsksForSortFields guards the local sort's inputs: a client that
// never asked for Fields would otherwise get an page sorted by nothing.
func TestUserItemsFilterAsksForSortFields(t *testing.T) {
	stub := newFilterStub(t, []map[string]any{
		filterStubItem("movie-a", "Alpha", "lib-1", 2001, filterStubUserData(false, false, 0)),
	})

	withTempAppConfig(t, singleUpstreamConfig(stub.server.URL), func(app *App, handler http.Handler) {
		serverID := app.Upstream.Clients()[0].ID
		userToken := createRegularUser(t, handler)
		favoriteLocally(t, handler, app, userToken, app.IDStore.GetOrCreateVirtualID("movie-a", serverID))

		rr := doJSONRequest(t, handler, http.MethodGet,
			"/Users/"+app.Auth.ProxyUserID()+"/Items?Filters=IsFavorite&Fields=Overview", nil, userToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
		}
		fields := stub.lastItemRequest(t).Get("Fields")
		for _, needed := range []string{"Overview", "SortName", "DateCreated", "ProductionYear", "CommunityRating"} {
			if !strings.Contains(fields, needed) {
				t.Fatalf("Fields = %q, want it to include %s", fields, needed)
			}
		}
	})
}

func filterNoticeLogged(app *App, value string) bool {
	for _, entry := range app.Logger.Entries(0) {
		if entry.Level == "warn" && strings.Contains(entry.Message, value) {
			return true
		}
	}
	return false
}

func TestWatchStoreGetPlayedAndResumableItems(t *testing.T) {
	ws := newTestWatchStore(t)

	rows := []*WatchProgress{
		{ProxyUserID: "user1", VirtualItemID: "played-1", Played: true, PositionTicks: 0},
		{ProxyUserID: "user1", VirtualItemID: "in-progress-1", Played: false, PositionTicks: 4200},
		{ProxyUserID: "user1", VirtualItemID: "untouched-1", Played: false, PositionTicks: 0},
		{ProxyUserID: "user2", VirtualItemID: "played-2", Played: true},
	}
	for _, row := range rows {
		if err := ws.RecordProgress(row); err != nil {
			t.Fatalf("record %s: %v", row.VirtualItemID, err)
		}
	}

	played, err := ws.GetPlayedItems("user1")
	if err != nil {
		t.Fatalf("GetPlayedItems: %v", err)
	}
	if got := watchItemIDs(played); !reflect.DeepEqual(got, []string{"played-1"}) {
		t.Fatalf("GetPlayedItems = %v, want [played-1]", got)
	}

	resumable, err := ws.GetResumableItems("user1")
	if err != nil {
		t.Fatalf("GetResumableItems: %v", err)
	}
	if got := watchItemIDs(resumable); !reflect.DeepEqual(got, []string{"in-progress-1"}) {
		t.Fatalf("GetResumableItems = %v, want [in-progress-1]", got)
	}
}

func watchItemIDs(rows []WatchProgress) []string {
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.VirtualItemID)
	}
	return ids
}
