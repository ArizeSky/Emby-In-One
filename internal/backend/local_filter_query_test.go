package backend

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestLocalFilterCandidatesTranslatePersonPredicate(t *testing.T) {
	stub := newLocalFilterStub(t, "user-a", nil, nil)
	withTempAppConfig(t, singleUpstreamConfig(stub.server.URL), func(app *App, _ http.Handler) {
		c := app.Upstream.Clients()[0]
		person := app.IDStore.GetOrCreateVirtualID("person-original", c.ID)
		q, ok := app.localFilterServerQuery(url.Values{"PersonIds": {person}, "Ids": {"media-virtual"}, "IncludeItemTypes": {"Movie"}}, c)
		if !ok || q.Get("PersonIds") != "person-original" || q.Get("Ids") != "" || q.Get("IncludeItemTypes") != "Movie" {
			t.Fatalf("predicate lost or untranslated: %v/%v", q, ok)
		}
	})
}

func TestLocalFilterCandidatesEncodedBatchBound(t *testing.T) {
	ids := make([]string, 100)
	for i := range ids {
		ids[i] = strings.Repeat("中", 15)
	}
	batches := localFilterIDBatches(ids)
	if len(batches) < 2 {
		t.Fatal("encoded byte limit ignored")
	}
	for _, batch := range batches {
		if len(url.QueryEscape(strings.Join(batch, ","))) > localFilterBatchMaxIDBytes {
			t.Fatal("encoded batch exceeds byte limit")
		}
	}
}

func TestLocalFilterCandidatesOversizedIDIsIncompleteWithoutRequest(t *testing.T) {
	stub := newLocalFilterStub(t, "user-a", nil, nil)
	withTempAppConfig(t, singleUpstreamConfig(stub.server.URL), func(app *App, h http.Handler) {
		token, user := createLocalFilterUser(t, h, "child", "child123")
		id := app.IDStore.GetOrCreateVirtualID(strings.Repeat("中", 1500), app.Upstream.Clients()[0].ID)
		localFilterFavorite(t, app, user, id)
		rr := doJSONRequest(t, h, http.MethodGet, "/Users/"+app.Auth.ProxyUserID()+"/Items?Filters=IsFavorite", nil, token)
		if rr.Code != 200 || !localFilterIsIncomplete(rr) {
			t.Fatalf("oversized query not partial: %d %s", rr.Code, rr.Body.String())
		}
		if stub.requestCount() != 0 {
			t.Fatal("oversized ID was sent upstream")
		}
	})
}
