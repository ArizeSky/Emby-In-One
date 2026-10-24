package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func assertSingleQueryValue(t *testing.T, values url.Values, name, want string) bool {
	t.Helper()
	count := 0
	got := ""
	for key, list := range values {
		if strings.EqualFold(key, name) {
			count += len(list)
			if len(list) > 0 {
				got = list[0]
			}
		}
	}
	if count != 1 || got != want {
		t.Errorf("query %s: count=%d value=%q, want one %q (query=%v)", name, count, got, want, values)
		return false
	}
	return true
}

func TestClientQueryCaseInsensitiveReadAndReplace(t *testing.T) {
	if got := queryValue(url.Values{"": {"not-a-source"}}, "MediaSourceId"); got != "" {
		t.Fatalf("missing key matched empty-name parameter: %q", got)
	}
	values := url.Values{"limit": {"20"}, "LIMIT": {"99"}, "Limit": {"2"}, "startindex": {"1"}, "Other": {"unchanged"}}
	for i := 0; i < 50; i++ {
		if queryValue(values, "Limit") != "2" {
			t.Fatal("canonical key must take deterministic precedence")
		}
	}
	setQueryValue(values, "Limit", "5000")
	assertSingleQueryValue(t, values, "Limit", "5000")
	if values.Get("Other") != "unchanged" {
		t.Fatal("unrelated query changed")
	}
	page := paginateItems(catalogOrderFixture(), url.Values{"limit": {"2"}, "startindex": {"1"}})
	if page["StartIndex"] != 1 || len(asItems(page)) != 2 {
		t.Fatalf("lowercase paging ignored: %#v", page)
	}
	if n, ok := queryInt(url.Values{"lImIt": {"3"}}, "Limit"); !ok || n != 3 {
		t.Fatalf("queryInt=%d,%v", n, ok)
	}
	onlyVariants := url.Values{"limit": {"20"}, "LIMIT": {"99"}}
	for i := 0; i < 50; i++ {
		if queryValue(onlyVariants, "Limit") != "99" {
			t.Fatal("case-variant preference depends on map order")
		}
	}
}

// Emby's parser merges differently cased scalars. This reproduces the actual
// Hills limit=20 request that previously became Limit=5000&limit=20 and failed.
func TestClientLatestLowercaseWindowIsReplacedAndPagedLocally(t *testing.T) {
	items := []map[string]any{
		{"Id": "a", "Name": "Alpha", "DateCreated": "2024-01-01"},
		{"Id": "b", "Name": "Bravo", "DateCreated": "2024-02-01"},
		{"Id": "c", "Name": "Charlie", "DateCreated": "2024-03-01"},
		{"Id": "d", "Name": "Delta", "DateCreated": "2024-04-01"},
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/Users/AuthenticateByName" {
			writeJSON(w, 200, map[string]any{"AccessToken": "upstream-token", "User": map[string]any{"Id": "user"}})
			return
		}
		if r.URL.Path != "/Users/user/Items/Latest" {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		valid := assertSingleQueryValue(t, q, "Limit", "5000")
		valid = assertSingleQueryValue(t, q, "StartIndex", "0") && valid
		valid = assertSingleQueryValue(t, q, "UserId", "user") && valid
		if queryKey(q, "ParentId") != "" {
			valid = assertSingleQueryValue(t, q, "ParentId", "library") && valid
		}
		if !valid {
			http.Error(w, "Input string was not in a correct format: 5000,20", 500)
			return
		}
		writeJSON(w, 200, items)
	}))
	defer upstream.Close()
	withTempAppConfig(t, singleUpstreamConfig(upstream.URL), func(app *App, handler http.Handler) {
		admin := loginToken(t, handler, "secret")
		serverID := app.Upstream.Clients()[0].ID
		ordinary := createRouteAccessUser(t, handler, admin, "latest-user", []string{serverID})
		parent := app.IDStore.GetOrCreateVirtualID("library", serverID)
		for _, token := range []string{admin, ordinary} {
			for _, scope := range []string{"", "&parentId=" + parent} {
				for _, window := range []string{"&limit=2&startIndex=1", "&Limit=2&limit=2&StartIndex=1&startindex=1"} {
					target := "/Users/" + app.Auth.ProxyUserID() + "/Items/Latest?userid=client-virtual" + scope + window
					rr := doJSONRequest(t, handler, http.MethodGet, target, nil, token)
					if rr.Code != 200 {
						t.Fatalf("Latest %s: %d %s", target, rr.Code, rr.Body.String())
					}
					var got []map[string]any
					if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
						t.Fatal(err)
					}
					if len(got) != 2 {
						t.Fatalf("Latest page length=%d: %#v", len(got), got)
					}
					if names := []string{itemText(got[0], "Name"), itemText(got[1], "Name")}; !reflect.DeepEqual(names, []string{"Charlie", "Bravo"}) {
						t.Fatalf("Latest page=%#v", got)
					}
				}
			}
		}
	})
}

func TestClientLowercaseSourceRoutesPlaybackAndRangeToSelectedServer(t *testing.T) {
	withReviewUpstreams(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Test-Server") != "b" {
			t.Errorf("explicit source fell back to A: %s", r.URL.Path)
			http.Error(w, "wrong server", 500)
			return
		}
		q := r.URL.Query()
		if !assertSingleQueryValue(t, q, "MediaSourceId", "ms-b") {
			http.Error(w, "mediaSource null", 400)
			return
		}
		switch r.URL.Path {
		case "/Items/item-b/PlaybackInfo":
			assertSingleQueryValue(t, q, "UserId", "user-b")
			writeJSON(w, 200, map[string]any{"MediaSources": []any{map[string]any{"Id": "ms-b", "Container": "mkv"}}})
		case "/Videos/item-b/stream.mkv":
			assertSingleQueryValue(t, q, "PlaySessionId", "session-b")
			if r.Header.Get("Range") != "bytes=0-1023" {
				t.Errorf("Range lost: %q", r.Header.Get("Range"))
			}
			if q.Get("api_key") != "token-b" {
				t.Error("wrong upstream credential")
			}
			w.Header().Set("Content-Type", "video/x-matroska")
			w.Header().Set("Content-Range", "bytes 0-1023/4096")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte(strings.Repeat("x", 1024)))
		default:
			http.NotFound(w, r)
		}
	}, func(app *App, handler http.Handler, admin string) {
		clients := app.Upstream.Clients()
		item := app.IDStore.GetOrCreateVirtualID("item-a", clients[0].ID)
		app.IDStore.AssociateAdditionalInstance(item, "item-b", clients[1].ID)
		source := app.IDStore.GetOrCreateVirtualID("ms-b", clients[1].ID)
		session := app.IDStore.GetOrCreateVirtualID("session-b", clients[1].ID)
		ordinary := createRouteAccessUser(t, handler, admin, "playback-user", []string{clients[0].ID, clients[1].ID})
		for _, token := range []string{admin, ordinary} {
			for _, key := range []string{"MediaSourceId", "mediaSourceId", "mediasourceid"} {
				selection := key + "=" + source + "&MEDIASOURCEID=" + source
				rr := doJSONRequest(t, handler, "GET", "/Items/"+item+"/PlaybackInfo?userid=virtual&"+selection, nil, token)
				if rr.Code != 200 {
					t.Fatalf("PlaybackInfo %s: %d %s", key, rr.Code, rr.Body.String())
				}
				req := httptest.NewRequest("GET", "/Videos/"+item+"/stream.mkv?Static=true&"+selection+"&playSessionId="+session, nil)
				req.Header.Set("X-Emby-Token", token)
				req.Header.Set("Range", "bytes=0-1023")
				rangeResponse := httptest.NewRecorder()
				handler.ServeHTTP(rangeResponse, req)
				if rangeResponse.Code != 206 || rangeResponse.Body.Len() != 1024 {
					t.Fatalf("stream %s: %d len=%d body=%s", key, rangeResponse.Code, rangeResponse.Body.Len(), rangeResponse.Body.String())
				}
			}
		}
	})
}

func TestClientLowercaseExplicitSourceCannotBypassServerAccess(t *testing.T) {
	a := newRouteAccessUpstreamStub(t, "a")
	b := newRouteAccessUpstreamStub(t, "b")
	withTempAppConfig(t, dualUpstreamConfig(a.server.URL, b.server.URL), func(app *App, handler http.Handler) {
		admin := loginToken(t, handler, "secret")
		serverA, serverB := app.Upstream.Clients()[0].ID, app.Upstream.Clients()[1].ID
		onlyB := createRouteAccessUser(t, handler, admin, "source-denied", []string{serverB})
		both := createRouteAccessUser(t, handler, admin, "source-offline", []string{serverA, serverB})
		item := app.IDStore.GetOrCreateVirtualID("movie-a", serverA)
		app.IDStore.AssociateAdditionalInstance(item, "movie-b", serverB)
		source := app.IDStore.GetOrCreateVirtualID("source-a", serverA)
		for _, tc := range []struct {
			token  string
			status int
		}{{onlyB, 403}, {both, 503}} {
			if tc.status == 503 {
				app.Upstream.GetClient(0).setOffline("test offline")
			}
			for _, key := range []string{"mediaSourceId", "mediasourceid", "MEDIASOURCEID"} {
				for _, path := range []string{"/Items/" + item + "/PlaybackInfo", "/Videos/" + item + "/stream.mp4"} {
					a.reset()
					b.reset()
					rr := doAuthJSON(t, handler, "GET", path+"?"+key+"="+source, nil, tc.token)
					if rr.Code != tc.status {
						t.Fatalf("%s: status=%d want=%d body=%s", path, rr.Code, tc.status, rr.Body.String())
					}
					if paths := routeMediaRequests(a.recordedPaths(), b.recordedPaths()); len(paths) != 0 {
						t.Fatalf("denied/offline source queried upstream: %v", paths)
					}
				}
			}
		}
	})
}

func TestClientPlaybackInfoPreservesOnlyRewrittenLocalURLs(t *testing.T) {
	withReviewUpstreams(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Items/item-b/PlaybackInfo" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, 200, map[string]any{"MediaSources": []any{
			map[string]any{"Id": "ms-b", "Container": "mkv", "Path": "/private/movie.mkv",
				"DirectStreamUrl": "https://upstream.invalid/Videos/item-b/stream.mkv?api_key=upstream-secret",
				"TranscodingUrl":  "https://upstream.invalid/videos/item-b/master.m3u8?mediaSourceId=ms-b&ApiKey=upstream-secret",
				"MediaStreams":    []any{map[string]any{"Type": "Subtitle", "DeliveryUrl": "https://upstream.invalid/videos/item-b/ms-b/Subtitles/0/Stream.srt?X-Emby-Token=upstream-secret"}}},
			map[string]any{"Id": "malformed", "TranscodingUrl": "http://[bad", "Path": "/private/bad"},
			map[string]any{"Id": "unrouted", "TranscodingUrl": "https://upstream.invalid/not-a-proxy-route?api_key=upstream-secret"},
		}})
	}, func(app *App, handler http.Handler, admin string) {
		clients := app.Upstream.Clients()
		item := app.IDStore.GetOrCreateVirtualID("item-a", clients[0].ID)
		app.IDStore.AssociateAdditionalInstance(item, "item-b", clients[1].ID)
		source := app.IDStore.GetOrCreateVirtualID("ms-b", clients[1].ID)
		ordinary := createRouteAccessUser(t, handler, admin, "safe-urls", []string{clients[0].ID, clients[1].ID})
		for _, tc := range []struct {
			token      string
			restricted bool
		}{{admin, false}, {ordinary, true}} {
			rr := doJSONRequest(t, handler, "GET", "/Items/"+item+"/PlaybackInfo?mediaSourceId="+source, nil, tc.token)
			if rr.Code != 200 {
				t.Fatalf("PlaybackInfo: %d %s", rr.Code, rr.Body.String())
			}
			var payload map[string]any
			if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			sources := asItems(map[string]any{"Items": payload["MediaSources"]})
			if len(sources) != 3 {
				t.Fatalf("sources=%#v", sources)
			}
			first := sources[0]
			for _, field := range []string{"DirectStreamUrl", "TranscodingUrl"} {
				raw, _ := first[field].(string)
				if !isLocalPlaybackURL(raw, item) {
					t.Fatalf("lost or unsafe %s=%q", field, raw)
				}
				parsed, _ := url.Parse(raw)
				assertSingleQueryValue(t, parsed.Query(), "MediaSourceId", source)
				if parsed.Query().Get("api_key") != tc.token {
					t.Fatalf("proxy credential missing in %s", field)
				}
			}
			streams := asItems(map[string]any{"Items": first["MediaStreams"]})
			if len(streams) != 1 || !isLocalPlaybackURL(fmt.Sprint(streams[0]["DeliveryUrl"]), item) {
				t.Fatalf("subtitle URL=%#v", streams)
			}
			if tc.restricted {
				if first["Path"] != nil || sources[1]["TranscodingUrl"] != nil || sources[2]["TranscodingUrl"] != nil {
					t.Fatalf("restricted unsafe fields=%#v", sources)
				}
				if strings.Contains(rr.Body.String(), "upstream-secret") || strings.Contains(rr.Body.String(), "upstream.invalid") || strings.Contains(rr.Body.String(), "/private/") {
					t.Fatalf("upstream privacy leak: %s", rr.Body.String())
				}
			} else if first["Path"] != "/private/movie.mkv" {
				t.Fatal("admin path behavior changed")
			}
		}
	})
	restricted := httptest.NewRequest("GET", "/", nil)
	restricted = restricted.WithContext(context.WithValue(restricted.Context(), requestContextKey{}, &RequestContext{ProxyUser: &tokenInfo{Role: "user"}}))
	for _, bad := range []string{"https://evil.invalid/Videos/item/stream.mkv", "//evil.invalid/Videos/item/stream.mkv", "/Videos/other/stream.mkv", "/Videos/item/../secret", "/Videos/item/%2e%2e/secret", "/Videos/item/stream.mkv#fragment", "/Videos/item/\\evil"} {
		source := map[string]any{"DirectStreamUrl": bad, "Path": "/secret"}
		privacyPlaybackMediaSources(restricted, []map[string]any{source}, "item")
		if source["DirectStreamUrl"] != nil || source["Path"] != nil {
			t.Fatalf("unsafe URL survived: %#v", source)
		}
	}
}
