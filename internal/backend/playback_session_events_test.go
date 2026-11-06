package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

// Strict attempt session-event tests.
//
// They run the real handlers against a synthetic upstream with an explicitly
// reserved and activated strict binding, so the strict branch is exercised without
// the capability ingress, a real upstream or a deployed application. Only the
// ingress-owned pieces (capability lookup, owner revalidation, lease) are replaced
// by the fixture; everything the strict branch decides — association checks,
// translation for the bound server, protocol-field stripping and the stop
// lifecycle — runs as it does in production.

type strictEventCall struct {
	method string
	path   string
	query  string
	body   map[string]any
}

type strictEventUpstream struct {
	mu     sync.Mutex
	calls  []strictEventCall
	status int
}

func (u *strictEventUpstream) record(r *http.Request) int {
	call := strictEventCall{method: r.Method, path: r.URL.Path, query: r.URL.RawQuery}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&call.body)
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.calls = append(u.calls, call)
	if u.status == 0 {
		return http.StatusNoContent
	}
	return u.status
}

func (u *strictEventUpstream) setStatus(status int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.status = status
}

func (u *strictEventUpstream) recorded() []strictEventCall {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]strictEventCall(nil), u.calls...)
}

type strictEventFixture struct {
	app      *App
	upstream *strictEventUpstream

	token   string
	user    *tokenInfo
	owner   playbackIdentityOwner
	binding playbackIdentityBinding

	virtualItem   string
	virtualSource string
	otherSource   string
	otherItem     string
}

// strictEventFixtureConfig is a single-upstream configuration: the strict attempt
// is bound to that one server, so any cross-server fan-out would be visible as an
// unexpected upstream call.
const strictEventFixtureConfig = "server:\n  port: 8096\n  name: \"Test Server\"\n  id: \"server-1\"\n\n" +
	"admin:\n  username: \"admin\"\n  password: \"secret\"\n\n" +
	"playback:\n  mode: \"proxy\"\n\n" +
	"timeouts:\n  api: 30000\n  global: 15000\n  login: 10000\n  healthCheck: 10000\n  healthInterval: 60000\n\n" +
	"proxies: []\nupstream:\n  - name: \"A\"\n    url: %q\n    username: \"u1\"\n    password: \"p1\"\n"

func withStrictEventFixture(t *testing.T, fn func(*strictEventFixture)) {
	t.Helper()
	recorder := &strictEventUpstream{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/Users/AuthenticateByName" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"AccessToken": "token-upstream",
				"User":        map[string]any{"Id": "user-upstream"},
			})
			return
		}
		w.WriteHeader(recorder.record(r))
	}))
	defer upstream.Close()

	withTempAppConfig(t, fmt.Sprintf(strictEventFixtureConfig, upstream.URL), func(app *App, handler http.Handler) {
		token := loginToken(t, handler, "secret")
		user := app.Auth.ValidateToken(token)
		if user == nil {
			t.Fatal("admin login token was not accepted")
		}
		client := app.Upstream.ClientByID(app.Upstream.Clients()[0].ID)
		// The binding store requires the sender to present exactly the identity the
		// attempt was created with, so the login identity and the attempt identity
		// are pinned to the same snapshot here.
		snapshot := identitySnapshot{Source: "custom", Headers: http.Header{
			"User-Agent":       {"StrictFixture/1.0"},
			"X-Emby-Client":    {"StrictFixture"},
			"X-Emby-Device-Id": {"strict-fixture-device"},
		}}
		auth := client.authSnapshot()
		client.setOnlineWithIdentity(auth.AccessToken, auth.UserID, snapshot)
		auth = client.authSnapshot()

		virtualItem := app.IDStore.GetOrCreateVirtualID("item-upstream", client.ID)
		virtualSource := app.IDStore.GetOrCreateVirtualID("source-upstream", client.ID)
		owner := newPlaybackIdentityOwner(user.UserID, token)
		binding, err := app.PlaybackIdentities.reserve(playbackAttemptRequest{
			Owner:          owner,
			ServerID:       client.ID,
			VirtualItemID:  virtualItem,
			SourceID:       "source-upstream",
			Identity:       snapshot,
			Principal:      auth.Compatibility.Principal,
			ConfigRevision: auth.Compatibility.ConfigRevision,
		})
		if err != nil {
			t.Fatalf("reserve strict binding: %v", err)
		}
		binding, err = app.PlaybackIdentities.activate(binding.AttemptID, owner, "play-session-upstream")
		if err != nil {
			t.Fatalf("activate strict binding: %v", err)
		}

		fn(&strictEventFixture{
			app:           app,
			upstream:      recorder,
			token:         token,
			user:          user,
			owner:         owner,
			binding:       binding,
			virtualItem:   virtualItem,
			virtualSource: virtualSource,
			otherSource:   app.IDStore.GetOrCreateVirtualID("source-other", client.ID),
			otherItem:     app.IDStore.GetOrCreateVirtualID("item-other", client.ID),
		})
	})
}

// strictEventRequest builds a strict session-event request whose context carries
// the leased binding, exactly as the strict ingress publishes it.
func (f *strictEventFixture) strictEventRequest(t *testing.T, method, suffix string, body map[string]any) *http.Request {
	t.Helper()
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			t.Fatalf("encode event body: %v", err)
		}
	}
	target := "/eio/playback/v1/" + f.binding.AttemptID + "/Sessions/Playing" + suffix
	req := httptest.NewRequest(method, target, bytes.NewReader(payload))
	return f.withBinding(req)
}

func (f *strictEventFixture) withBinding(req *http.Request) *http.Request {
	binding := f.binding
	reqCtx := &RequestContext{
		ProxyUser:   f.user,
		ProxyToken:  f.token,
		Playback:    &binding,
		Identifiers: f.app.newRequestIdentifierLookup(),
	}
	return req.WithContext(context.WithValue(req.Context(), requestContextKey{}, reqCtx))
}

func (f *strictEventFixture) eventBody() map[string]any {
	return map[string]any{
		"ItemId":        f.virtualItem,
		"MediaSourceId": f.virtualSource,
		"PlaySessionId": "play-session-upstream",
		"EioAttemptId":  f.binding.AttemptID,
		"PositionTicks": float64(900000000),
	}
}

// TestStrictSessionEventForwardsBoundServerIdentifiers pins the strict body
// contract: the client's virtual ItemId/MediaSourceId are translated for the bound
// server only, the upstream session is the candidate's own raw session, and the
// local protocol fields never leave the process.
func TestStrictSessionEventForwardsBoundServerIdentifiers(t *testing.T) {
	withStrictEventFixture(t, func(f *strictEventFixture) {
		rr := httptest.NewRecorder()
		f.app.handleSessionPlayingProgress(rr, f.strictEventRequest(t, http.MethodPost, "/Progress", f.eventBody()))
		if rr.Code != http.StatusNoContent {
			t.Fatalf("strict progress status = %d, want 204 (body %s)", rr.Code, rr.Body.String())
		}
		calls := f.upstream.recorded()
		if len(calls) != 1 {
			t.Fatalf("upstream calls = %d, want 1", len(calls))
		}
		call := calls[0]
		if call.method != http.MethodPost || call.path != "/Sessions/Playing/Progress" {
			t.Fatalf("forwarded %s %s, want POST /Sessions/Playing/Progress", call.method, call.path)
		}
		if got := call.body["ItemId"]; got != "item-upstream" {
			t.Fatalf("ItemId = %v, want the bound server's original item", got)
		}
		if got := call.body["MediaSourceId"]; got != "source-upstream" {
			t.Fatalf("MediaSourceId = %v, want the bound server's original source", got)
		}
		if got := call.body["PlaySessionId"]; got != "play-session-upstream" {
			t.Fatalf("PlaySessionId = %v, want the candidate's own upstream session", got)
		}
		for _, key := range []string{"EioAttemptId", "EioPlaybackToken", "EioPlaySessionId"} {
			if value, ok := call.body[key]; ok {
				t.Fatalf("local protocol field %s reached the upstream: %v", key, value)
			}
		}
	})
}

// TestStrictSessionEventRejectsForeignAssociations covers the frozen 409 protocol:
// a strict event that does not describe the bound attempt is rejected and never
// reaches an upstream.
func TestStrictSessionEventRejectsForeignAssociations(t *testing.T) {
	withStrictEventFixture(t, func(f *strictEventFixture) {
		mutate := func(change func(map[string]any)) map[string]any {
			body := f.eventBody()
			change(body)
			return body
		}
		cases := []struct {
			name string
			body map[string]any
			kind string
		}{
			{"missing attempt", mutate(func(b map[string]any) { delete(b, "EioAttemptId") }), strictPlaybackKindAttemptMissing},
			{"foreign attempt", mutate(func(b map[string]any) { b["EioAttemptId"] = "another-attempt" }), strictPlaybackKindAttemptMismatch},
			{"foreign item", mutate(func(b map[string]any) { b["ItemId"] = f.otherItem }), strictPlaybackKindItemMismatch},
			{"raw item id", mutate(func(b map[string]any) { b["ItemId"] = "item-upstream" }), strictPlaybackKindItemMismatch},
			{"missing session", mutate(func(b map[string]any) { delete(b, "PlaySessionId") }), strictPlaybackKindSessionMismatch},
			{"foreign session", mutate(func(b map[string]any) { b["PlaySessionId"] = "play-session-other" }), strictPlaybackKindSessionMismatch},
			{"local attempt as session", mutate(func(b map[string]any) { b["PlaySessionId"] = f.binding.AttemptID }), strictPlaybackKindSessionMismatch},
			{"foreign source", mutate(func(b map[string]any) { b["MediaSourceId"] = f.otherSource }), strictPlaybackKindSourceMismatch},
			{"unknown source", mutate(func(b map[string]any) { b["MediaSourceId"] = "source-unknown" }), strictPlaybackKindSourceMismatch},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				rr := httptest.NewRecorder()
				f.app.handleSessionPlaying(rr, f.strictEventRequest(t, http.MethodPost, "", tc.body))
				if rr.Code != http.StatusConflict {
					t.Fatalf("status = %d, want 409 (body %s)", rr.Code, rr.Body.String())
				}
				var payload map[string]any
				if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
					t.Fatalf("decode error body %q: %v", rr.Body.String(), err)
				}
				if payload["kind"] != tc.kind {
					t.Fatalf("kind = %v, want %s (body %s)", payload["kind"], tc.kind, rr.Body.String())
				}
			})
		}
		if calls := f.upstream.recorded(); len(calls) != 0 {
			t.Fatalf("rejected strict events reached the upstream: %d calls", len(calls))
		}
	})
}

// TestStrictStoppedRetiresOnlyAfterUpstreamConfirmation pins the stop lifecycle: a
// forward that did not succeed closes playback admission without an acknowledgment,
// leaves cleanup retryable, and only a confirmed stop retires it.
func TestStrictStoppedRetiresOnlyAfterUpstreamConfirmation(t *testing.T) {
	withStrictEventFixture(t, func(f *strictEventFixture) {
		body := f.eventBody()
		body["PositionTicks"] = float64(0)

		f.upstream.setStatus(http.StatusInternalServerError)
		rr := httptest.NewRecorder()
		f.app.handleSessionPlayingStopped(rr, f.strictEventRequest(t, http.MethodPost, "/Stopped", body))
		if rr.Code != http.StatusBadGateway {
			t.Fatalf("unconfirmed stop status = %d, want 502 (body %s)", rr.Code, rr.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode error body %q: %v", rr.Body.String(), err)
		}
		if payload["kind"] != strictPlaybackKindStopUnconfirmed {
			t.Fatalf("unconfirmed stop kind = %v, want %s", payload["kind"], strictPlaybackKindStopUnconfirmed)
		}
		if _, err := f.app.PlaybackIdentities.resolve(f.binding.AttemptID, f.owner); !errors.Is(err, ErrPlaybackIdentityStopping) {
			t.Fatalf("a refused stop must keep cleanup retryable and reject playback: %v", err)
		}

		f.upstream.setStatus(0)
		rr = httptest.NewRecorder()
		f.app.handleSessionPlayingStopped(rr, f.strictEventRequest(t, http.MethodPost, "/Stopped", body))
		if rr.Code != http.StatusNoContent {
			t.Fatalf("confirmed stop status = %d, want 204 (body %s)", rr.Code, rr.Body.String())
		}
		if _, err := f.app.PlaybackIdentities.resolve(f.binding.AttemptID, f.owner); !errors.Is(err, ErrPlaybackIdentityTerminal) {
			t.Fatalf("confirmed stop left the attempt resolvable: %v", err)
		}
	})
}

// TestStrictPlayingItemsStayOnBoundServer covers the strict PlayingItems form: one
// upstream only, the bound server's identifiers, no local credential, and no
// manufactured acknowledgment for a cleanup that failed.
func TestStrictPlayingItemsStayOnBoundServer(t *testing.T) {
	withStrictEventFixture(t, func(f *strictEventFixture) {
		target := "/eio/playback/v1/" + f.binding.AttemptID + "/Users/virtual-user/PlayingItems/" + f.virtualItem +
			"?MediaSourceId=" + url.QueryEscape(f.virtualSource) +
			"&PlaySessionId=play-session-upstream&api_key=eio_play_v1_fixture"
		req := httptest.NewRequest(http.MethodPost, target, nil)
		req.SetPathValue("itemId", f.virtualItem)
		req.SetPathValue("userId", f.user.UserID)
		rr := httptest.NewRecorder()
		f.app.handleUserPlayingItemStart(rr, f.withBinding(req))
		if rr.Code != http.StatusNoContent {
			t.Fatalf("strict playing-item start status = %d (body %s)", rr.Code, rr.Body.String())
		}
		calls := f.upstream.recorded()
		if len(calls) != 1 {
			t.Fatalf("upstream calls = %d, want 1", len(calls))
		}
		if calls[0].method != http.MethodPost || calls[0].path != "/Users/user-upstream/PlayingItems/item-upstream" {
			t.Fatalf("forwarded %s %s", calls[0].method, calls[0].path)
		}
		values, err := url.ParseQuery(calls[0].query)
		if err != nil {
			t.Fatalf("parse forwarded query %q: %v", calls[0].query, err)
		}
		if got := values.Get("MediaSourceId"); got != "source-upstream" {
			t.Fatalf("forwarded MediaSourceId = %q, want the bound server's source", got)
		}
		if got := values.Get("PlaySessionId"); got != "play-session-upstream" {
			t.Fatalf("forwarded PlaySessionId = %q, want the candidate's upstream session", got)
		}
		if got := values.Get("api_key"); got != "" {
			t.Fatalf("local capability was forwarded upstream: %q", got)
		}

		// A cleanup for another item is refused without contacting the upstream.
		foreign := httptest.NewRequest(http.MethodDelete, "/eio/playback/v1/"+f.binding.AttemptID+"/Users/virtual-user/PlayingItems/"+f.otherItem, nil)
		foreign.SetPathValue("itemId", f.otherItem)
		foreign.SetPathValue("userId", f.user.UserID)
		rr = httptest.NewRecorder()
		f.app.handleUserPlayingItemStop(rr, f.withBinding(foreign))
		if rr.Code != http.StatusConflict {
			t.Fatalf("foreign-item cleanup status = %d, want 409", rr.Code)
		}

		// A cleanup the upstream did not accept stays retryable instead of looking
		// acknowledged.
		f.upstream.setStatus(http.StatusInternalServerError)
		cleanup := httptest.NewRequest(http.MethodDelete, "/eio/playback/v1/"+f.binding.AttemptID+"/Users/virtual-user/PlayingItems/"+f.virtualItem, nil)
		cleanup.SetPathValue("itemId", f.virtualItem)
		cleanup.SetPathValue("userId", f.user.UserID)
		rr = httptest.NewRecorder()
		f.app.handleUserPlayingItemStop(rr, f.withBinding(cleanup))
		if rr.Code != http.StatusBadGateway {
			t.Fatalf("failed cleanup status = %d, want 502 (body %s)", rr.Code, rr.Body.String())
		}
		var payload map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatalf("decode cleanup error body %q: %v", rr.Body.String(), err)
		}
		if payload["kind"] != strictPlaybackKindStopFailed {
			t.Fatalf("failed cleanup kind = %v, want %s", payload["kind"], strictPlaybackKindStopFailed)
		}
	})
}
