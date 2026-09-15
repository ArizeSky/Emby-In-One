package backend

import (
	"fmt"
	"net/http"
	"testing"
)

// The stub upstream these tests use (newFilterStub, in user_filter_test.go) records
// every query it receives and pages its library the way a real server would, which is
// exactly what the paging contract needs to be pinned against.

// paginationStubLibrary builds a library of n movies named Movie001..MovieNNN.
func paginationStubLibrary(n int) []map[string]any {
	items := make([]map[string]any, 0, n)
	for i := 1; i <= n; i++ {
		items = append(items, filterStubItem(
			fmt.Sprintf("movie-%03d", i),
			fmt.Sprintf("Movie%03d", i),
			"lib-1",
			2000+i%20,
			filterStubUserData(false, false, 0),
		))
	}
	return items
}

// TestUserItemsPaginationDoesNotDoubleApplyStartIndex pins the paging contract of the
// merged path (no ParentId): the client's StartIndex and Limit are forwarded to the
// upstream *and* applied again to the already-paged result, so an offset past the
// upstream page size walks off the end of a page that was already cut.
func TestUserItemsPaginationDoesNotDoubleApplyStartIndex(t *testing.T) {
	const librarySize = 200
	stub := newFilterStub(t, paginationStubLibrary(librarySize))

	withTempAppConfig(t, singleUpstreamConfig(stub.server.URL), func(app *App, handler http.Handler) {
		adminToken := loginToken(t, handler, "secret")
		rr := doJSONRequest(t, handler, http.MethodGet,
			"/Users/"+app.Auth.ProxyUserID()+"/Items?StartIndex=100&Limit=50", nil, adminToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
		}

		forwarded := stub.lastItemRequest(t)
		names := itemNames(t, rr.Body.Bytes())
		if len(names) != 50 {
			t.Fatalf("page holds %d items, want 50 (upstream was asked for %s): %v",
				len(names), forwarded.Encode(), names)
		}
		if names[0] != "Movie101" || names[len(names)-1] != "Movie150" {
			t.Fatalf("page = [%s..%s], want [Movie101..Movie150]", names[0], names[len(names)-1])
		}
		if total := itemTotal(t, rr.Body.Bytes()); total != librarySize {
			t.Fatalf("TotalRecordCount = %d, want %d (upstream was asked for %s)",
				total, librarySize, forwarded.Encode())
		}
		payload := decodeItemsResponse(t, rr.Body.Bytes())
		if start, _ := payload["StartIndex"].(float64); int(start) != 100 {
			t.Fatalf("StartIndex = %v, want 100", payload["StartIndex"])
		}
	})
}

// TestUserItemsPaginationReportsLibraryTotal covers the first page, where the offset
// bug hides: the merged path reports the size of the page it fetched as the library
// total, so a client is told there is no second page to ask for.
func TestUserItemsPaginationReportsLibraryTotal(t *testing.T) {
	const librarySize = 200
	stub := newFilterStub(t, paginationStubLibrary(librarySize))

	withTempAppConfig(t, singleUpstreamConfig(stub.server.URL), func(app *App, handler http.Handler) {
		adminToken := loginToken(t, handler, "secret")
		rr := doJSONRequest(t, handler, http.MethodGet,
			"/Users/"+app.Auth.ProxyUserID()+"/Items?StartIndex=0&Limit=50", nil, adminToken)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
		}

		forwarded := stub.lastItemRequest(t)
		names := itemNames(t, rr.Body.Bytes())
		if len(names) != 50 {
			t.Fatalf("first page holds %d items, want 50 (upstream was asked for %s)",
				len(names), forwarded.Encode())
		}
		if total := itemTotal(t, rr.Body.Bytes()); total != librarySize {
			t.Fatalf("TotalRecordCount = %d, want %d (upstream was asked for %s)",
				total, librarySize, forwarded.Encode())
		}
	})
}
