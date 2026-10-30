package backend

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestCatalogSnapshotKeyIsolation(t *testing.T) {
	client := &UpstreamClient{ID: "server-a", UserID: "upstream-user", AccessToken: "upstream-secret"}
	app := &App{ConfigStore: &ConfigStore{config: &Config{}}}
	ctx := &RequestContext{ProxyUser: &tokenInfo{UserID: "alice", Role: "user", AllowedServers: []string{"server-a"}}, ProxyToken: "proxy-secret", Headers: http.Header{"User-Agent": {"client-a"}}}
	q := url.Values{"SortBy": {"SortName"}, "Fields": {"Overview"}, "ParentId": {"lib"}, "Limit": {"20"}}
	key := func(c *UpstreamClient, rc *RequestContext, path string, query url.Values) string {
		t.Helper()
		k, err := catalogSnapshotKey(app, rc, c, path, query, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(k) != 64 || strings.Contains(k, "secret") {
			t.Fatalf("non-digest key: %q", k)
		}
		return k
	}
	baseline := key(client, ctx, "/Users/%s/Items", q)
	page := cloneValues(q)
	page.Set("StartIndex", "5000")
	page.Set("Limit", "100")
	if key(client, ctx, "/Users/%s/Items", page) != baseline {
		t.Fatal("client page fragmented snapshot")
	}
	for name, mutate := range map[string]func(url.Values){
		"fields": func(v url.Values) { v.Set("Fields", "DateCreated") },
		"filter": func(v url.Values) { v.Set("Filters", "IsFolder") },
		"parent": func(v url.Values) { v.Set("ParentId", "other-lib") },
		"seed":   func(v url.Values) { v.Set("RandomSeed", "another-seed") },
		"search": func(v url.Values) { v.Set("SearchTerm", "other") },
	} {
		changed := cloneValues(q)
		mutate(changed)
		if key(client, ctx, "/Users/%s/Items", changed) == baseline {
			t.Errorf("%s reused unrelated snapshot", name)
		}
	}
	otherUser := *ctx
	otherUser.ProxyUser = &tokenInfo{UserID: "bob", Role: "user", AllowedServers: []string{"server-a"}}
	if key(client, &otherUser, "/Users/%s/Items", q) == baseline {
		t.Error("cross-user snapshot reuse")
	}
	otherHeaders := *ctx
	otherHeaders.Headers = http.Header{"User-Agent": {"client-b"}}
	if key(client, &otherHeaders, "/Users/%s/Items", q) == baseline {
		t.Error("cross-client identity snapshot reuse")
	}
	otherServer := &UpstreamClient{ID: "server-b", UserID: "upstream-user", AccessToken: "upstream-secret"}
	if key(otherServer, ctx, "/Users/%s/Items", q) == baseline {
		t.Error("cross-server snapshot reuse")
	}
	if key(client, ctx, "/Items", q) == baseline {
		t.Error("cross-route snapshot reuse")
	}
	changedPolicy := *ctx
	changedPolicy.ProxyUser = &tokenInfo{UserID: "alice", Role: "user", AllowedServers: []string{"server-a", "server-b"}}
	if key(client, &changedPolicy, "/Users/%s/Items", q) == baseline {
		t.Error("changed grants reused snapshot")
	}
	app.ConfigStore.mu.Lock()
	app.ConfigStore.config.Upstream = []UpstreamConfig{{ID: "new-config"}}
	app.ConfigStore.mu.Unlock()
	if key(client, ctx, "/Users/%s/Items", q) == baseline {
		t.Error("changed configuration reused snapshot")
	}
}
