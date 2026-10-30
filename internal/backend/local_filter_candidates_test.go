package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// localFilterStub is a fake upstream for the ID-driven local-filter path. It
// answers /Users/{id}/Items with a configurable library, applies the stub ID and
// user-state predicates before paging, and records every item request so a test
// can prove which physical IDs and windows the proxy actually sent.
type localFilterStub struct {
	server  *httptest.Server
	userID  string
	items   []map[string]any
	respond func(query url.Values) (status int, payload any)

	mu       sync.Mutex
	requests []url.Values
}

func newLocalFilterStub(t *testing.T, userID string, items []map[string]any, respond func(url.Values) (int, any)) *localFilterStub {
	t.Helper()
	stub := &localFilterStub{userID: userID, items: items, respond: respond}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"AccessToken": "tok-" + stub.userID,
				"User":        map[string]any{"Id": stub.userID},
			})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/Items"):
			query := r.URL.Query()
			stub.record(query)
			status := http.StatusOK
			var payload any
			if stub.respond != nil {
				status, payload = stub.respond(query)
			} else {
				page, total := filterStubPage(stub.items, query)
				payload = map[string]any{
					"Items":            toAnySlice(page),
					"TotalRecordCount": total,
					"StartIndex":       0,
				}
			}
			if status != http.StatusOK {
				w.WriteHeader(status)
				return
			}
			if payload != nil {
				_ = json.NewEncoder(w).Encode(payload)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": []any{}, "TotalRecordCount": 0})
		case strings.Contains(r.URL.Path, "/FavoriteItems/"),
			strings.Contains(r.URL.Path, "/UserData"),
			r.URL.Path == "/Sessions/Playing/Progress",
			r.URL.Path == "/Sessions/Playing":
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *localFilterStub) record(query url.Values) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, cloneValues(query))
}

func (s *localFilterStub) requestCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func (s *localFilterStub) recordedRequests() []url.Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]url.Values, len(s.requests))
	for i, request := range s.requests {
		out[i] = cloneValues(request)
	}
	return out
}

// createLocalFilterUser creates a regular user and returns both its login token
// and its proxy-user ID, which is the WatchStore key for direct fixture writes.
func createLocalFilterUser(t *testing.T, handler http.Handler, username, password string) (string, string) {
	t.Helper()
	adminToken := loginTokenAs(t, handler, "admin", "secret")
	rr := doJSONRequest(t, handler, http.MethodPost, "/admin/api/users",
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
		t.Fatalf("created user has no id: %s", rr.Body.String())
	}
	return loginTokenAs(t, handler, username, password), userID
}

func localFilterItem(id, name, parentID string, year int) map[string]any {
	return filterStubItem(id, name, parentID, year, filterStubUserData(false, false, 0))
}

func localFilterFavorite(t *testing.T, app *App, userID, virtualID string) {
	t.Helper()
	if err := app.WatchStore.SetFavorite(userID, virtualID, true); err != nil {
		t.Fatalf("set favorite %s: %v", virtualID, err)
	}
}

func localFilterRequestsHaveBoundedIDs(t *testing.T, requests []url.Values, wantMax int) {
	t.Helper()
	if len(requests) == 0 {
		t.Fatal("no upstream item request was recorded")
	}
	for i, request := range requests {
		ids := strings.TrimSpace(request.Get("Ids"))
		if ids == "" {
			t.Fatalf("request %d has no Ids predicate: %v", i, request)
		}
		parts := strings.Split(ids, ",")
		if len(parts) > wantMax {
			t.Fatalf("request %d sent %d IDs, want <= %d", i, len(parts), wantMax)
		}
		limit, err := strconv.Atoi(request.Get("Limit"))
		if err != nil || limit <= 0 || limit > wantMax {
			t.Fatalf("request %d Limit=%q, want 1..%d", i, request.Get("Limit"), wantMax)
		}
		if limit != len(parts) {
			t.Fatalf("request %d Limit=%d does not match %d requested IDs", i, limit, len(parts))
		}
	}
}

func localFilterIsIncomplete(rr *httptest.ResponseRecorder) bool {
	if rr.Header().Get("X-EIO-Results-Incomplete") == "true" {
		return true
	}
	payload := decodeItemsResponseStatic(rr.Body.Bytes())
	incomplete, _ := payload["EioIncompleteResults"].(bool)
	return incomplete
}

func decodeItemsResponseStatic(body []byte) map[string]any {
	var payload map[string]any
	_ = json.Unmarshal(body, &payload)
	return payload
}

// TestLocalFilterCandidatesFavoriteBeyondScanWindow is the core acceptance case:
// the shared account's 5000-row scan would never reach the tail, but the local
// favorite names the physical ID directly, so the item must come back.
func TestLocalFilterCandidatesFavoriteBeyondScanWindow(t *testing.T) {
	items := make([]map[string]any, 0, 6001)
	for i := 0; i < 6001; i++ {
		items = append(items, localFilterItem(
			fmt.Sprintf("movie-%04d", i),
			fmt.Sprintf("Item-%04d", i),
			"lib-1",
			2000+i%20,
		))
	}
	stub := newLocalFilterStub(t, "user-a", items, nil)

	withTempAppConfig(t, singleUpstreamConfig(stub.server.URL), func(app *App, handler http.Handler) {
		serverID := app.Upstream.Clients()[0].ID
		userToken, userID := createLocalFilterUser(t, handler, "child", "child123")
		tailVirtual := app.IDStore.GetOrCreateVirtualID("movie-6000", serverID)
		localFilterFavorite(t, app, userID, tailVirtual)

		rr := doJSONRequest(t, handler, http.MethodGet,
			"/Users/"+app.Auth.ProxyUserID()+"/Items?Filters=IsFavorite&SortBy=SortName&SortOrder=Ascending",
			nil, userToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
		}
		if got := itemNames(t, rr.Body.Bytes()); !reflect.DeepEqual(got, []string{"Item-6000"}) {
			t.Fatalf("favorite filter returned %v, want [Item-6000]", got)
		}
		if got := itemTotal(t, rr.Body.Bytes()); got != 1 {
			t.Fatalf("TotalRecordCount = %d, want 1", got)
		}
		if localFilterIsIncomplete(rr) {
			t.Fatalf("complete ID batch was marked incomplete: %s", rr.Body.String())
		}

		requests := stub.recordedRequests()
		localFilterRequestsHaveBoundedIDs(t, requests, localFilterBatchLimit)
		if len(requests) != 1 {
			t.Fatalf("got %d upstream requests, want 1 for one local favorite", len(requests))
		}
		if ids := requests[0].Get("Ids"); ids != "movie-6000" {
			t.Fatalf("Ids = %q, want movie-6000", ids)
		}
		if limit := requests[0].Get("Limit"); limit == "5000" {
			t.Fatalf("ID-driven path still asked for the full-library window: %v", requests[0])
		}
	})
}

// TestLocalFilterCandidatesEmptyLocalSetMakesNoRequest keeps the "complete empty"
// path distinct from incompleteness: with no positive local record the proxy must
// not contact any upstream and must not claim the answer is partial.
func TestLocalFilterCandidatesEmptyLocalSetMakesNoRequest(t *testing.T) {
	stub := newLocalFilterStub(t, "user-a", []map[string]any{
		localFilterItem("movie-a", "Alpha", "lib-1", 2001),
	}, nil)

	withTempAppConfig(t, singleUpstreamConfig(stub.server.URL), func(app *App, handler http.Handler) {
		userToken, _ := createLocalFilterUser(t, handler, "child", "child123")

		rr := doJSONRequest(t, handler, http.MethodGet,
			"/Users/"+app.Auth.ProxyUserID()+"/Items?Filters=IsFavorite&SortBy=SortName",
			nil, userToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
		}
		if got := itemTotal(t, rr.Body.Bytes()); got != 0 {
			t.Fatalf("TotalRecordCount = %d, want 0", got)
		}
		if localFilterIsIncomplete(rr) {
			t.Fatalf("empty local set was marked incomplete: %s", rr.Body.String())
		}
		if got := stub.requestCount(); got != 0 {
			t.Fatalf("upstream got %d item requests, want 0 for an empty local set", got)
		}
	})
}

// TestLocalFilterCandidatesBatchesAreBounded checks the 100-ID batch ceiling and
// the matching Limit, plus that every batch's items still reach the response.
func TestLocalFilterCandidatesBatchesAreBounded(t *testing.T) {
	const total = 250
	items := make([]map[string]any, 0, total)
	for i := 0; i < total; i++ {
		items = append(items, localFilterItem(
			fmt.Sprintf("batch-%03d", i),
			fmt.Sprintf("Batch-%03d", i),
			"lib-1",
			2001,
		))
	}
	stub := newLocalFilterStub(t, "user-a", items, nil)

	withTempAppConfig(t, singleUpstreamConfig(stub.server.URL), func(app *App, handler http.Handler) {
		serverID := app.Upstream.Clients()[0].ID
		userToken, userID := createLocalFilterUser(t, handler, "child", "child123")
		for i := 0; i < total; i++ {
			virtualID := app.IDStore.GetOrCreateVirtualID(fmt.Sprintf("batch-%03d", i), serverID)
			localFilterFavorite(t, app, userID, virtualID)
		}

		rr := doJSONRequest(t, handler, http.MethodGet,
			"/Users/"+app.Auth.ProxyUserID()+"/Items?Filters=IsFavorite&SortBy=SortName&SortOrder=Ascending",
			nil, userToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
		}
		if got := itemTotal(t, rr.Body.Bytes()); got != total {
			t.Fatalf("TotalRecordCount = %d, want %d", got, total)
		}
		if got := len(itemNames(t, rr.Body.Bytes())); got != total {
			t.Fatalf("returned %d items, want %d", got, total)
		}
		if localFilterIsIncomplete(rr) {
			t.Fatalf("bounded batches were marked incomplete: %s", rr.Body.String())
		}

		requests := stub.recordedRequests()
		localFilterRequestsHaveBoundedIDs(t, requests, localFilterBatchLimit)
		if len(requests) < 3 {
			t.Fatalf("got %d batches for %d IDs, want at least 3", len(requests), total)
		}
	})
}

// TestLocalFilterCandidatesUnknownVirtualIDNotBroadcast proves an unresolvable
// local record never turns into a raw upstream ID request; it can only make the
// answer incomplete.
func TestLocalFilterCandidatesUnknownVirtualIDNotBroadcast(t *testing.T) {
	stub := newLocalFilterStub(t, "user-a", []map[string]any{
		localFilterItem("movie-a", "Alpha", "lib-1", 2001),
	}, nil)

	withTempAppConfig(t, singleUpstreamConfig(stub.server.URL), func(app *App, handler http.Handler) {
		userToken, userID := createLocalFilterUser(t, handler, "child", "child123")
		// A state row whose virtual ID was never registered in the IDStore can only
		// be a stale/unknown alias. Broadcasting "ghost-unknown-id" could match an
		// unrelated upstream item by coincidence.
		localFilterFavorite(t, app, userID, "ghost-unknown-id")

		rr := doJSONRequest(t, handler, http.MethodGet,
			"/Users/"+app.Auth.ProxyUserID()+"/Items?Filters=IsFavorite&SortBy=SortName",
			nil, userToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
		}
		if got := itemTotal(t, rr.Body.Bytes()); got != 0 {
			t.Fatalf("TotalRecordCount = %d, want 0", got)
		}
		if !localFilterIsIncomplete(rr) {
			t.Fatalf("unresolved virtual ID must be incomplete: %s", rr.Body.String())
		}
		if got := stub.requestCount(); got != 0 {
			t.Fatalf("upstream got %d requests, want 0: unknown virtual ID was broadcast", got)
		}
	})
}

// TestLocalFilterCandidatesClientIDPredicatesIntersect covers explicit client
// narrowing: Ids and ItemIds are separate predicates, so both must hold. A union
// would widen the result and is the regression this test guards.
func TestLocalFilterCandidatesClientIDPredicatesIntersect(t *testing.T) {
	stub := newLocalFilterStub(t, "user-a", []map[string]any{
		localFilterItem("pick-a", "Alpha", "lib-1", 2001),
		localFilterItem("pick-b", "Bravo", "lib-1", 2002),
		localFilterItem("pick-c", "Charlie", "lib-1", 2003),
	}, nil)

	withTempAppConfig(t, singleUpstreamConfig(stub.server.URL), func(app *App, handler http.Handler) {
		serverID := app.Upstream.Clients()[0].ID
		userToken, userID := createLocalFilterUser(t, handler, "child", "child123")
		virtual := map[string]string{}
		for _, original := range []string{"pick-a", "pick-b", "pick-c"} {
			virtual[original] = app.IDStore.GetOrCreateVirtualID(original, serverID)
			localFilterFavorite(t, app, userID, virtual[original])
		}

		// Ids narrows the local set to Bravo and Charlie.
		target := "/Users/" + app.Auth.ProxyUserID() + "/Items?Filters=IsFavorite&SortBy=SortName&Ids=" +
			url.QueryEscape(virtual["pick-b"]+","+virtual["pick-c"])
		rr := doJSONRequest(t, handler, http.MethodGet, target, nil, userToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("Ids status = %d body=%s", rr.Code, rr.Body.String())
		}
		if got := itemNames(t, rr.Body.Bytes()); !reflect.DeepEqual(got, []string{"Bravo", "Charlie"}) {
			t.Fatalf("Ids filter returned %v, want [Bravo Charlie]", got)
		}

		// Ids -> {Bravo, Charlie} AND ItemIds -> {Charlie}: only Charlie remains.
		overlap := "/Users/" + app.Auth.ProxyUserID() + "/Items?Filters=IsFavorite&SortBy=SortName&Ids=" +
			url.QueryEscape(virtual["pick-b"]+","+virtual["pick-c"]) + "&ItemIds=" + url.QueryEscape(virtual["pick-c"])
		rr = doJSONRequest(t, handler, http.MethodGet, overlap, nil, userToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("Ids+ItemIds status = %d body=%s", rr.Code, rr.Body.String())
		}
		if got := itemNames(t, rr.Body.Bytes()); !reflect.DeepEqual(got, []string{"Charlie"}) {
			t.Fatalf("Ids+ItemIds filter returned %v, want [Charlie] (intersection)", got)
		}

		// Disjoint predicates match nothing. A union would return Bravo again.
		before := stub.requestCount()
		disjoint := "/Users/" + app.Auth.ProxyUserID() + "/Items?Filters=IsFavorite&SortBy=SortName&Ids=" +
			url.QueryEscape(virtual["pick-b"]) + "&ItemIds=" + url.QueryEscape(virtual["pick-c"])
		rr = doJSONRequest(t, handler, http.MethodGet, disjoint, nil, userToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("disjoint status = %d body=%s", rr.Code, rr.Body.String())
		}
		if got := itemTotal(t, rr.Body.Bytes()); got != 0 {
			t.Fatalf("disjoint Ids/ItemIds TotalRecordCount = %d, want 0", got)
		}
		if got := stub.requestCount(); got != before {
			t.Fatalf("disjoint predicates still asked upstream (%d new requests)", got-before)
		}
	})
}

// TestLocalFilterCandidatesContainerOnlyTargetServer keeps the container path
// scoped: a ParentId owned by server A must never query server B, even when the
// local favorite resolves to both instances.
func TestLocalFilterCandidatesContainerOnlyTargetServer(t *testing.T) {
	a := newLocalFilterStub(t, "user-a", []map[string]any{
		localFilterItem("movie-a", "Alpha", "lib-1", 2001),
		localFilterItem("movie-b", "Bravo", "lib-1", 2002),
	}, nil)
	b := newLocalFilterStub(t, "user-b", []map[string]any{
		localFilterItem("movie-b", "Bravo", "lib-1", 2002),
	}, nil)

	withTempAppConfig(t, dualUpstreamConfig(a.server.URL, b.server.URL), func(app *App, handler http.Handler) {
		idA := app.Upstream.Clients()[0].ID
		idB := app.Upstream.Clients()[1].ID
		userToken, userID := createLocalFilterUser(t, handler, "child", "child123")
		container := app.IDStore.GetOrCreateVirtualID("lib-1", idA)
		itemA := app.IDStore.GetOrCreateVirtualID("movie-a", idA)
		itemB := app.IDStore.GetOrCreateVirtualID("movie-b", idB)
		localFilterFavorite(t, app, userID, itemA)
		localFilterFavorite(t, app, userID, itemB)

		target := "/Users/" + app.Auth.ProxyUserID() + "/Items?Filters=IsFavorite&SortBy=SortName&ParentId=" + url.QueryEscape(container)
		rr := doJSONRequest(t, handler, http.MethodGet, target, nil, userToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
		}
		if got := itemNames(t, rr.Body.Bytes()); !reflect.DeepEqual(got, []string{"Alpha"}) {
			t.Fatalf("container filter returned %v, want [Alpha] from server A only", got)
		}
		if got := b.requestCount(); got != 0 {
			t.Fatalf("server B got %d requests, want 0 for an A-owned container", got)
		}
		if got := a.requestCount(); got != 1 {
			t.Fatalf("server A got %d requests, want 1", got)
		}
		forwarded := a.recordedRequests()[0]
		if forwarded.Get("ParentId") != "lib-1" {
			t.Fatalf("ParentId = %q, want translated lib-1", forwarded.Get("ParentId"))
		}
	})
}

// TestLocalFilterCandidatesDiscardsOutOfBatchIDs requires the proxy to ignore
// returned IDs it never requested and to report the shape violation.
func TestLocalFilterCandidatesDiscardsOutOfBatchIDs(t *testing.T) {
	stub := newLocalFilterStub(t, "user-a", nil, func(query url.Values) (int, any) {
		requested := strings.Split(query.Get("Ids"), ",")
		items := make([]map[string]any, 0, len(requested)+1)
		for _, id := range requested {
			items = append(items, localFilterItem(id, "Wanted", "lib-1", 2001))
		}
		items = append(items, localFilterItem("movie-not-requested", "Smuggled", "lib-1", 2002))
		return http.StatusOK, map[string]any{
			"Items":            toAnySlice(items),
			"TotalRecordCount": len(items),
		}
	})

	withTempAppConfig(t, singleUpstreamConfig(stub.server.URL), func(app *App, handler http.Handler) {
		serverID := app.Upstream.Clients()[0].ID
		userToken, userID := createLocalFilterUser(t, handler, "child", "child123")
		localFilterFavorite(t, app, userID, app.IDStore.GetOrCreateVirtualID("movie-a", serverID))

		rr := doJSONRequest(t, handler, http.MethodGet,
			"/Users/"+app.Auth.ProxyUserID()+"/Items?Filters=IsFavorite&SortBy=SortName",
			nil, userToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
		}
		if got := itemNames(t, rr.Body.Bytes()); !reflect.DeepEqual(got, []string{"Wanted"}) {
			t.Fatalf("filter returned %v, want only the requested [Wanted]", got)
		}
		if !localFilterIsIncomplete(rr) {
			t.Fatalf("out-of-batch ID must mark the answer incomplete: %s", rr.Body.String())
		}
	})
}

// TestLocalFilterCandidatesHTTPErrorIncomplete proves an upstream failure is a
// partial answer, never a silent complete-empty set.
func TestLocalFilterCandidatesHTTPErrorIncomplete(t *testing.T) {
	stub := newLocalFilterStub(t, "user-a", nil, func(url.Values) (int, any) {
		return http.StatusInternalServerError, nil
	})

	withTempAppConfig(t, singleUpstreamConfig(stub.server.URL), func(app *App, handler http.Handler) {
		serverID := app.Upstream.Clients()[0].ID
		userToken, userID := createLocalFilterUser(t, handler, "child", "child123")
		localFilterFavorite(t, app, userID, app.IDStore.GetOrCreateVirtualID("movie-a", serverID))

		rr := doJSONRequest(t, handler, http.MethodGet,
			"/Users/"+app.Auth.ProxyUserID()+"/Items?Filters=IsFavorite&SortBy=SortName",
			nil, userToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
		}
		if got := itemTotal(t, rr.Body.Bytes()); got != 0 {
			t.Fatalf("TotalRecordCount = %d, want 0 for a failed batch", got)
		}
		if !localFilterIsIncomplete(rr) {
			t.Fatalf("upstream error must mark the answer incomplete: %s", rr.Body.String())
		}
	})
}

// TestLocalFilterCandidatesStateReadErrorIncomplete guards the review finding:
// localUserStateRows turns a database failure into an empty map, which would make
// the ID-driven helper answer a *complete* empty set. The error-aware read must
// instead report incompleteness.
func TestLocalFilterCandidatesStateReadErrorIncomplete(t *testing.T) {
	stub := newLocalFilterStub(t, "user-a", []map[string]any{
		localFilterItem("movie-a", "Alpha", "lib-1", 2001),
	}, nil)

	withTempAppConfig(t, singleUpstreamConfig(stub.server.URL), func(app *App, handler http.Handler) {
		serverID := app.Upstream.Clients()[0].ID
		userToken, userID := createLocalFilterUser(t, handler, "child", "child123")
		localFilterFavorite(t, app, userID, app.IDStore.GetOrCreateVirtualID("movie-a", serverID))
		if err := app.WatchStore.db.exec("DROP TABLE user_watch_progress"); err != nil {
			t.Fatalf("drop watch table: %v", err)
		}

		rr := doJSONRequest(t, handler, http.MethodGet,
			"/Users/"+app.Auth.ProxyUserID()+"/Items?Filters=IsFavorite&SortBy=SortName",
			nil, userToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
		}
		if !localFilterIsIncomplete(rr) {
			t.Fatalf("state read error must mark the answer incomplete: %s", rr.Body.String())
		}
		if got := stub.requestCount(); got != 0 {
			t.Fatalf("upstream got %d requests after a state read error, want 0", got)
		}
	})
}
