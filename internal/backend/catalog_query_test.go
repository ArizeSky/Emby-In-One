package backend

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"testing"
)

func TestClientRandomCandidateScanIsStableBeforeLocalPaging(t *testing.T) {
	items := make([]map[string]any, 205)
	for i := range items {
		items[i] = map[string]any{"Id": fmt.Sprintf("a-%03d", i), "Name": fmt.Sprintf("Movie %03d", i), "SortName": fmt.Sprintf("Movie %03d", i), "Type": "Movie", "ProductionYear": 2020, "OfficialRating": "PG"}
	}
	first, qA := newCatalogOrderUpstream(t, items)
	defer first.Close()
	second, qB := newCatalogOrderUpstream(t, []map[string]any{
		{"Id": "duplicate", "Name": "Movie 000", "SortName": "Movie 000", "Type": "Movie", "ProductionYear": 2020, "OfficialRating": "PG"},
		{"Id": "extra", "Name": "Movie 205", "SortName": "Movie 205", "Type": "Movie", "ProductionYear": 2020, "OfficialRating": "PG"},
	})
	defer second.Close()
	cfg := upstreamsConfigYAML(upstreamEntryYAML("A", first.URL, true), upstreamEntryYAML("B", second.URL, true))
	withTempAppConfig(t, cfg, func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		base := "/Users/" + app.Auth.ProxyUserID() + "/Items?sortBy=rAnDoM&sortOrder=Ascending&randomSeed=paging&"
		read := func(target string) []map[string]any {
			t.Helper()
			rr := doJSONRequest(t, handler, "GET", target, nil, token)
			if rr.Code != 200 {
				t.Fatalf("Random status=%d body=%s", rr.Code, rr.Body.String())
			}
			var payload map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if payload["TotalRecordCount"] != float64(206) {
				t.Fatalf("dedup total=%v, want 206", payload["TotalRecordCount"])
			}
			return asItems(payload)
		}
		all := read(base + "limit=300")
		page1 := read(base + "limit=100&startIndex=0")
		page2 := read(base + "limit=106&startIndex=100")
		joined := append(catalogItemIDs(page1), catalogItemIDs(page2)...)
		if !reflect.DeepEqual(joined, catalogItemIDs(all)) {
			t.Fatal("Random pages duplicate/omit/reorder candidates")
		}
		want := append([]map[string]any(nil), all...)
		if err := sortCatalogItems(want, url.Values{"SortBy": {"Random"}, "RandomSeed": {"paging"}}); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(catalogItemIDs(want), catalogItemIDs(all)) {
			t.Fatal("response not ordered by requested random seed")
		}
		for _, target := range []string{"/Items?sortBy=Random&randomSeed=paging&limit=300", "/Items?SearchTerm=Movie&sortBy=Random&randomSeed=paging&limit=300"} {
			if got := catalogItemIDs(read(target)); !reflect.DeepEqual(got, catalogItemIDs(all)) {
				t.Fatalf("Random route order differs: %s", target)
			}
		}
		for _, recorder := range []*catalogOrderQueryRecorder{qA, qB} {
			queries := recorder.snapshot()
			if len(queries) < 5 {
				t.Fatalf("not all pages scanned: %d", len(queries))
			}
			for _, q := range queries {
				assertSingleQueryValue(t, q, "SortBy", "SortName")
				assertSingleQueryValue(t, q, "SortOrder", "Ascending")
				if queryKey(q, "RandomSeed") != "" {
					t.Fatalf("local random seed leaked upstream: %v", q)
				}
				limit, err := strconv.Atoi(q.Get("Limit"))
				if err != nil || limit > upstreamPageLimitCeiling {
					t.Fatalf("invalid scan limit: %v", q)
				}
			}
		}
	})
}

func TestClientScopedAndLatestRandomUseLocalOrder(t *testing.T) {
	items := []map[string]any{}
	for i := 0; i < 8; i++ {
		items = append(items, map[string]any{"Id": fmt.Sprint("item-", i), "Name": fmt.Sprint("Movie ", i), "SortName": fmt.Sprint("Movie ", i), "OfficialRating": "PG", "DateCreated": "2024-01-01", "Type": "Movie", "ParentId": "library"})
	}
	up, queries := newCatalogOrderUpstream(t, items)
	defer up.Close()
	withTempAppConfig(t, singleUpstreamConfig(up.URL), func(app *App, handler http.Handler) {
		admin := loginToken(t, handler, "secret")
		server := app.Upstream.Clients()[0].ID
		parent := app.IDStore.GetOrCreateVirtualID("library", server)
		_, child := review23User(t, app)
		for _, token := range []string{admin, child} {
			for _, route := range []string{"/Items?ParentId=" + parent, "/Items/Latest?ParentId=" + parent, "/Items/Latest?Unused=true"} {
				target := "/Users/" + app.Auth.ProxyUserID() + route + "&sortBy=Random&randomSeed=scoped&limit=3&startIndex=2"
				rr := doJSONRequest(t, handler, "GET", target, nil, token)
				if rr.Code != 200 {
					t.Fatalf("%s: %d %s", target, rr.Code, rr.Body.String())
				}
				var payload any
				if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
					t.Fatal(err)
				}
				got := asItems(payload)
				all := make([]map[string]any, len(items))
				for i, item := range items {
					all[i] = deepCloneMap(item)
					all[i]["Id"] = app.IDStore.GetOrCreateVirtualID(itemID(item), server)
				}
				if err := sortCatalogItems(all, url.Values{"SortBy": {"Random"}, "RandomSeed": {"scoped"}}); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(catalogItemIDs(got), catalogItemIDs(all[2:5])) {
					t.Fatalf("scoped Random not applied locally: %s got=%v want=%v", route, catalogItemIDs(got), catalogItemIDs(all[2:5]))
				}
			}
		}
		for _, q := range queries.snapshot() {
			assertSingleQueryValue(t, q, "SortBy", "SortName")
			assertSingleQueryValue(t, q, "SortOrder", "Ascending")
		}
	})
}

func TestClientRandomRespectsLocalStateFilterAndParentalDenial(t *testing.T) {
	items := []map[string]any{
		{"Id": "allowed-a", "Name": "A", "OfficialRating": "PG", "Type": "Movie"},
		{"Id": "allowed-b", "Name": "B", "OfficialRating": "PG", "Type": "Movie"},
		{"Id": "denied", "Name": "Denied", "OfficialRating": "R", "Type": "Movie"},
	}
	up, _ := newCatalogOrderUpstream(t, items)
	defer up.Close()
	withTempAppConfig(t, singleUpstreamConfig(up.URL), func(app *App, handler http.Handler) {
		user, token := review23User(t, app)
		server := app.Upstream.Clients()[0].ID
		ids := []string{}
		for _, item := range items {
			ids = append(ids, app.IDStore.GetOrCreateVirtualID(itemID(item), server))
		}
		for _, id := range ids {
			if err := app.WatchStore.SetFavorite(user.ID, id, true); err != nil {
				t.Fatal(err)
			}
		}
		target := "/Users/" + app.Auth.ProxyUserID() + "/Items?Filters=IsFavorite&sortBy=Random&randomSeed=local&limit=20"
		rr := doJSONRequest(t, handler, "GET", target, nil, token)
		if rr.Code != 200 {
			t.Fatalf("local Random: %d %s", rr.Code, rr.Body.String())
		}
		var payload any
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		got := asItems(payload)
		if len(got) != 2 {
			t.Fatalf("parental filter lost: %#v", got)
		}
		want := []map[string]any{{"Id": ids[0]}, {"Id": ids[1]}}
		if err := sortCatalogItems(want, url.Values{"SortBy": {"Random"}, "RandomSeed": {"local"}}); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(catalogItemIDs(got), catalogItemIDs(want)) {
			t.Fatalf("Random fell back to local recency: %v", catalogItemIDs(got))
		}
	})
}

func TestClientLowercaseSourceKeepsParentalFailClosed(t *testing.T) {
	for _, tc := range []struct {
		rating string
		fail   bool
		status int
	}{{"R", false, 403}, {"PG", true, 502}} {
		a := review23Upstream("PG", false)
		b := review23Upstream(tc.rating, tc.fail)
		withTempAppConfig(t, dualUpstreamConfig(a.URL, b.URL), func(app *App, handler http.Handler) {
			_, token := review23User(t, app)
			clients := app.Upstream.Clients()
			item := app.IDStore.GetOrCreateVirtualID("film", clients[0].ID)
			app.IDStore.AssociateAdditionalInstance(item, "film", clients[1].ID)
			source := app.IDStore.GetOrCreateVirtualID("source-b", clients[1].ID)
			for _, path := range []string{"/Items/" + item + "/PlaybackInfo", "/Videos/" + item + "/stream.mkv"} {
				rr := doJSONRequest(t, handler, "GET", path+"?mediaSourceId="+source, nil, token)
				if rr.Code != tc.status {
					t.Fatalf("parental source gate: %s status=%d want=%d body=%s", path, rr.Code, tc.status, rr.Body.String())
				}
			}
		})
		a.Close()
		b.Close()
	}
}
