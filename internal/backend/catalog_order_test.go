package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"sync"
	"testing"
)

func catalogOrderFixture() []map[string]any {
	return []map[string]any{
		{"Id": "id-z", "Name": "Beta", "SortName": "B", "ProductionYear": 2020, "DateCreated": "2024-03-01T00:00:00Z", "SeriesSortName": "Series B", "CommunityRating": 7.0},
		{"Id": "id-a", "Name": "Alpha", "SortName": "A", "ProductionYear": 2022, "DateCreated": "2024-01-01T00:00:00Z", "SeriesSortName": "Series A", "CommunityRating": 8.0},
		{"Id": "id-b", "Name": "Alpha", "SortName": "A", "ProductionYear": 2020, "DateCreated": "2024-02-01T00:00:00Z", "SeriesSortName": "Series B", "CommunityRating": 7.0},
		{"Id": "id-m", "Name": "Gamma", "SortName": "G", "ProductionYear": 2022, "DateCreated": "2024-04-01T00:00:00Z", "SeriesSortName": "Series C", "CommunityRating": 9.0},
	}
}

func catalogItemIDs(items []map[string]any) []string {
	ids := make([]string, len(items))
	for i, item := range items {
		ids[i] = itemID(item)
	}
	return ids
}

func TestCatalogOrderSupportsKeysDirectionsAndMultiKey(t *testing.T) {
	tests := []struct {
		name, sortBy, sortOrder string
		want                    []string
	}{
		{"name ascending", "Name", "Ascending", []string{"id-a", "id-b", "id-z", "id-m"}},
		{"name descending", "Name", "Descending", []string{"id-m", "id-z", "id-a", "id-b"}},
		{"year ascending", "ProductionYear", "Ascending", []string{"id-b", "id-z", "id-a", "id-m"}},
		{"year descending", "Year", "Descending", []string{"id-a", "id-m", "id-b", "id-z"}},
		{"date created ascending", "DateCreated", "Ascending", []string{"id-a", "id-b", "id-z", "id-m"}},
		{"date created descending", "DateCreated", "Descending", []string{"id-m", "id-z", "id-b", "id-a"}},
		{"multiple keys and orders", "ProductionYear,Name", "Ascending,Descending", []string{"id-z", "id-b", "id-m", "id-a"}},
		{"community rating ascending", "CommunityRating", "Ascending", []string{"id-b", "id-z", "id-a", "id-m"}},
		{"community rating descending", "CommunityRating", "Descending", []string{"id-m", "id-a", "id-b", "id-z"}},
		{"series sort name", "SeriesSortName", "Ascending", []string{"id-a", "id-b", "id-z", "id-m"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			items := catalogOrderFixture()
			query := url.Values{"SortBy": {test.sortBy}, "SortOrder": {test.sortOrder}}
			if err := sortCatalogItems(items, query); err != nil {
				t.Fatalf("sortCatalogItems: %v", err)
			}
			if got := catalogItemIDs(items); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("sorted IDs = %v, want %v", got, test.want)
			}
		})
	}
}

func TestCatalogOrderEqualKeysUseStableVirtualID(t *testing.T) {
	items := []map[string]any{
		{"Id": "virtual-z", "Name": "Equal"},
		{"Id": "virtual-a", "Name": "Equal"},
		{"Id": "virtual-m", "Name": "Equal"},
	}
	if err := sortCatalogItems(items, url.Values{"SortBy": {"Name"}, "SortOrder": {"Ascending"}}); err != nil {
		t.Fatal(err)
	}
	want := []string{"virtual-a", "virtual-m", "virtual-z"}
	if got := catalogItemIDs(items); !reflect.DeepEqual(got, want) {
		t.Fatalf("equal-key IDs = %v, want %v", got, want)
	}
}

func TestCatalogSearchPaginatesAfterCrossServerMergeAndDedup(t *testing.T) {
	first, firstQueries := newCatalogOrderUpstream(t, []map[string]any{
		{"Id": "alpha-a", "Name": "Alpha", "SortName": "Alpha", "Type": "Movie", "ProductionYear": 2020},
		{"Id": "charlie", "Name": "Charlie", "SortName": "Charlie", "Type": "Movie", "ProductionYear": 2020},
	})
	defer first.Close()
	second, secondQueries := newCatalogOrderUpstream(t, []map[string]any{
		{"Id": "bravo", "Name": "Bravo", "SortName": "Bravo", "Type": "Movie", "ProductionYear": 2020},
		{"Id": "alpha-b", "Name": "Alpha", "SortName": "Alpha", "Type": "Movie", "ProductionYear": 2020},
		{"Id": "delta", "Name": "Delta", "SortName": "Delta", "Type": "Movie", "ProductionYear": 2020},
	})
	defer second.Close()

	withTempAppConfig(t, dualUpstreamConfig(first.URL, second.URL), func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		base := "/Items?SearchTerm=movie&SortBy=Name&SortOrder=Ascending&Limit=2&"
		firstPage := doJSONRequest(t, handler, http.MethodGet, base+"StartIndex=0", nil, token)
		if firstPage.Code != http.StatusOK {
			t.Fatalf("first page status=%d body=%s", firstPage.Code, firstPage.Body.String())
		}
		if got, want := itemNames(t, firstPage.Body.Bytes()), []string{"Alpha", "Bravo"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("first page names = %v, want %v", got, want)
		}
		if got := itemTotal(t, firstPage.Body.Bytes()); got != 4 {
			t.Fatalf("first page total = %d, want 4 distinct candidates", got)
		}

		secondPage := doJSONRequest(t, handler, http.MethodGet, base+"StartIndex=2", nil, token)
		if secondPage.Code != http.StatusOK {
			t.Fatalf("second page status=%d body=%s", secondPage.Code, secondPage.Body.String())
		}
		if got, want := itemNames(t, secondPage.Body.Bytes()), []string{"Charlie", "Delta"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("second page names = %v, want %v", got, want)
		}
		if got := itemTotal(t, secondPage.Body.Bytes()); got != 4 {
			t.Fatalf("second page total = %d, want 4 distinct candidates", got)
		}

		rootBase := "/Users/" + app.Auth.ProxyUserID() + "/Items?SortBy=Name&SortOrder=Ascending&Limit=2&StartIndex="
		rootFirst := doJSONRequest(t, handler, http.MethodGet, rootBase+"0", nil, token)
		if rootFirst.Code != http.StatusOK || !reflect.DeepEqual(itemNames(t, rootFirst.Body.Bytes()), []string{"Alpha", "Bravo"}) {
			t.Fatalf("root first page status=%d body=%s", rootFirst.Code, rootFirst.Body.String())
		}
		if got := itemTotal(t, rootFirst.Body.Bytes()); got != 4 {
			t.Fatalf("root first page total=%d, want 4", got)
		}
		rootSecond := doJSONRequest(t, handler, http.MethodGet, rootBase+"2", nil, token)
		if rootSecond.Code != http.StatusOK || !reflect.DeepEqual(itemNames(t, rootSecond.Body.Bytes()), []string{"Charlie", "Delta"}) {
			t.Fatalf("root second page status=%d body=%s", rootSecond.Code, rootSecond.Body.String())
		}
		if got := itemTotal(t, rootSecond.Body.Bytes()); got != 4 {
			t.Fatalf("root second page total=%d, want 4", got)
		}

		for name, queries := range map[string][]url.Values{"first": firstQueries.snapshot(), "second": secondQueries.snapshot()} {
			if len(queries) != 4 {
				t.Fatalf("%s source received %d requests, want two Items and two User Items scans", name, len(queries))
			}
			for _, query := range queries {
				if query.Get("StartIndex") != "0" || query.Get("Limit") != strconv.Itoa(mergedItemsScanLimit) {
					t.Errorf("%s source got client window instead of scan window: %v", name, query)
				}
			}
		}
	})
}

func TestLatestAggregatesDeduplicatesSortsAndPaginates(t *testing.T) {
	first, _ := newCatalogOrderUpstream(t, []map[string]any{
		{"Id": "one", "Name": "One", "Type": "Movie", "ProductionYear": 2020, "DateCreated": "2024-01-01T00:00:00Z"},
		{"Id": "shared-a", "Name": "Shared", "Type": "Movie", "ProductionYear": 2020, "DateCreated": "2024-04-01T00:00:00Z"},
	})
	defer first.Close()
	second, _ := newCatalogOrderUpstream(t, []map[string]any{
		{"Id": "two", "Name": "Two", "Type": "Movie", "ProductionYear": 2020, "DateCreated": "2024-03-01T00:00:00Z"},
		{"Id": "shared-b", "Name": "Shared", "Type": "Movie", "ProductionYear": 2020, "DateCreated": "2024-04-01T00:00:00Z"},
		{"Id": "three", "Name": "Three", "Type": "Movie", "ProductionYear": 2020, "DateCreated": "2024-02-01T00:00:00Z"},
	})
	defer second.Close()
	withTempAppConfig(t, dualUpstreamConfig(first.URL, second.URL), func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		path := "/Users/" + app.Auth.ProxyUserID() + "/Items/Latest?Limit=2&StartIndex="
		readNames := func(target string) []string {
			t.Helper()
			rr := doJSONRequest(t, handler, http.MethodGet, target, nil, token)
			if rr.Code != http.StatusOK {
				t.Fatalf("Latest status=%d body=%s", rr.Code, rr.Body.String())
			}
			var items []map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &items); err != nil {
				t.Fatalf("unmarshal Latest array: %v body=%s", err, rr.Body.String())
			}
			names := make([]string, len(items))
			for i := range items {
				names[i] = itemText(items[i], "Name")
			}
			return names
		}
		if got, want := readNames(path+"0"), []string{"Shared", "Two"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("Latest first page = %v, want %v", got, want)
		}
		if got, want := readNames(path+"2"), []string{"Three", "One"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("Latest second page = %v, want %v", got, want)
		}
	})
}
func TestCatalogSortRejectsUnknownKeyWith400(t *testing.T) {
	withTempApp(t, func(_ *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		rr := doJSONRequest(t, handler, http.MethodGet, "/Items?SearchTerm=movie&SortBy=Random&Limit=2", nil, token)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400: %s", rr.Code, rr.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if payload["code"] != "UnsupportedSortKey" || payload["key"] != "Random" {
			t.Fatalf("unsupported sort response = %#v", payload)
		}
	})
}

type catalogOrderQueryRecorder struct {
	mu      sync.Mutex
	queries []url.Values
}

func (r *catalogOrderQueryRecorder) add(query url.Values) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queries = append(r.queries, cloneValues(query))
}

func (r *catalogOrderQueryRecorder) snapshot() []url.Values {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]url.Values, len(r.queries))
	for i := range r.queries {
		out[i] = cloneValues(r.queries[i])
	}
	return out
}

func newCatalogOrderUpstream(t *testing.T, items []map[string]any) (*httptest.Server, *catalogOrderQueryRecorder) {
	t.Helper()
	recorder := &catalogOrderQueryRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token", "User": map[string]any{"Id": "user"}})
		case r.Method == http.MethodGet && (r.URL.Path == "/Items" || r.URL.Path == "/Users/user/Items" || r.URL.Path == "/Users/user/Items/Latest"):
			query := r.URL.Query()
			recorder.add(query)
			page, total := filterStubPage(items, query)
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": toAnySlice(page), "TotalRecordCount": total, "StartIndex": 0})
		default:
			http.NotFound(w, r)
		}
	}))
	return server, recorder
}

func TestCatalogSearchKeepsConfirmedCandidatesAndMarksPageFailure(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName":
			_ = json.NewEncoder(w).Encode(map[string]any{"AccessToken": "token", "User": map[string]any{"Id": "user"}})
		case r.Method == http.MethodGet && r.URL.Path == "/Items":
			if r.URL.Query().Get("StartIndex") != "0" {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			items := make([]map[string]any, upstreamPageLimitCeiling)
			for i := range items {
				items[i] = map[string]any{"Id": fmt.Sprintf("movie-%03d", i), "Name": fmt.Sprintf("Movie%03d", i), "Type": "Movie", "ProductionYear": 2020}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Items": toAnySlice(items), "TotalRecordCount": 400})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	withTempAppConfig(t, upstreamsConfigYAML(upstreamEntryYAML("A", upstream.URL, true)), func(_ *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		rr := doJSONRequest(t, handler, http.MethodGet, "/Items?SearchTerm=movie&SortBy=Name&Limit=10&StartIndex=0", nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		if got, want := itemNames(t, rr.Body.Bytes()), []string{"Movie000", "Movie001", "Movie002", "Movie003", "Movie004", "Movie005", "Movie006", "Movie007", "Movie008", "Movie009"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("partial scan page = %v, want %v", got, want)
		}
		if got := itemTotal(t, rr.Body.Bytes()); got != upstreamPageLimitCeiling {
			t.Fatalf("partial scan total=%d, want confirmed lower bound %d", got, upstreamPageLimitCeiling)
		}
		if rr.Header().Get("X-EIO-Results-Incomplete") != "true" {
			t.Fatalf("page failure missing incomplete header: %s", rr.Header())
		}
	})
}

func TestCatalogSearchTruncationReportsCandidateLowerBound(t *testing.T) {
	items := make([]map[string]any, mergedItemsScanLimit+1000)
	for i := range items {
		items[i] = map[string]any{
			"Id":             fmt.Sprintf("movie-%05d", i),
			"Name":           fmt.Sprintf("Movie%05d", i),
			"SortName":       fmt.Sprintf("Movie%05d", i),
			"Type":           "Movie",
			"ProductionYear": 2020,
		}
	}
	upstream, _ := newCatalogOrderUpstream(t, items)
	defer upstream.Close()
	withTempAppConfig(t, singleUpstreamConfig(upstream.URL), func(_ *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		rr := doJSONRequest(t, handler, http.MethodGet, "/Items?SearchTerm=movie&SortBy=Name&SortOrder=Ascending&Limit=2", nil, token)
		if rr.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
		}
		if got, want := itemNames(t, rr.Body.Bytes()), []string{"Movie00000", "Movie00001"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("truncated sorted page = %v, want %v", got, want)
		}
		if got := itemTotal(t, rr.Body.Bytes()); got != mergedItemsScanLimit {
			t.Fatalf("truncated total=%d, want candidate lower bound %d", got, mergedItemsScanLimit)
		}
		if rr.Header().Get("X-EIO-Results-Incomplete") != "true" {
			t.Fatalf("scan cap missing incomplete header: %s", rr.Header())
		}
	})
}
