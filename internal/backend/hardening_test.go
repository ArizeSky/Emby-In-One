package backend

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// --- §8.2 atomic policy writes ---

func TestCreateWithPolicyIsAtomic(t *testing.T) {
	s := newTestUserStore(t)
	u, err := s.CreateWithPolicy("alice", "secret", []string{"s1"}, 7, true)
	if err != nil {
		t.Fatalf("CreateWithPolicy: %v", err)
	}
	if u.MaxParentalRating != 7 || !u.BlockUnratedItems {
		t.Fatalf("returned user policy = (%d,%v), want (7,true)", u.MaxParentalRating, u.BlockUnratedItems)
	}
	got := s.Get(u.ID)
	if got == nil || got.MaxParentalRating != 7 || !got.BlockUnratedItems {
		t.Fatalf("persisted policy = %+v, want (7,true)", got)
	}
	// An illegal ceiling is rejected before any row is created.
	if _, err := s.CreateWithPolicy("bob", "secret", nil, 6, false); err == nil {
		t.Fatal("CreateWithPolicy accepted an illegal rating 6")
	}
	if s.GetByUsername("bob") != nil {
		t.Fatal("rejected CreateWithPolicy still created a user")
	}
}

func TestUpdateWithPolicyAtomic(t *testing.T) {
	s := newTestUserStore(t)
	u, err := s.CreateWithPolicy("carol", "secret", nil, 0, false)
	if err != nil {
		t.Fatalf("CreateWithPolicy: %v", err)
	}
	newMax := 9
	newBlock := true
	if err := s.UpdateWithPolicy(u.ID, nil, nil, nil, nil, &newMax, &newBlock); err != nil {
		t.Fatalf("UpdateWithPolicy: %v", err)
	}
	got := s.Get(u.ID)
	if got.MaxParentalRating != 9 || !got.BlockUnratedItems {
		t.Fatalf("policy after update = (%d,%v), want (9,true)", got.MaxParentalRating, got.BlockUnratedItems)
	}
	// Illegal rating rejected, and the stored policy is unchanged.
	bad := 6
	if err := s.UpdateWithPolicy(u.ID, nil, nil, nil, nil, &bad, nil); err == nil {
		t.Fatal("UpdateWithPolicy accepted an illegal rating 6")
	}
	if got := s.Get(u.ID); got.MaxParentalRating != 9 {
		t.Fatalf("rejected update changed rating to %d", got.MaxParentalRating)
	}
	// The plain Update delegate must not touch the policy columns.
	name := "carol2"
	if err := s.Update(u.ID, &name, nil, nil, nil); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := s.Get(u.ID); got.MaxParentalRating != 9 || !got.BlockUnratedItems {
		t.Fatalf("plain Update changed policy to (%d,%v)", got.MaxParentalRating, got.BlockUnratedItems)
	}
}

func TestValidParentalRating(t *testing.T) {
	valid := []int{0, 1, 3, 4, 5, 7, 8, 9, 10, 15}
	for _, v := range valid {
		if !validParentalRating(v) {
			t.Errorf("validParentalRating(%d) = false, want true", v)
		}
	}
	for _, v := range []int{-1, 2, 6, 11, 13, 16, 100} {
		if validParentalRating(v) {
			t.Errorf("validParentalRating(%d) = true, want false", v)
		}
	}
}

// --- §8.3 rating inheritance helpers ---

func TestItemRatingStateCustomOverride(t *testing.T) {
	// CustomRating overrides OfficialRating.
	if score, ok := itemRatingState(map[string]any{"OfficialRating": "PG", "CustomRating": "R"}); !ok || score != 9 {
		t.Fatalf("custom override = (%d,%v), want (9,true)", score, ok)
	}
	// A non-empty but unrecognisable CustomRating is unknown, not a fallback.
	if _, ok := itemRatingState(map[string]any{"OfficialRating": "PG", "CustomRating": "XYZ"}); ok {
		t.Fatal("unrecognisable CustomRating fell back to OfficialRating")
	}
	if score, ok := itemRatingState(map[string]any{"OfficialRating": "PG"}); !ok || score != 5 {
		t.Fatalf("official = (%d,%v), want (5,true)", score, ok)
	}
}

func TestNextParentIDBreaksCycles(t *testing.T) {
	item := map[string]any{"Id": "e1", "SeasonId": "s1", "ParentId": "s1", "SeriesId": "ser1"}
	visited := map[string]bool{"e1": true}
	if got := nextParentID(item, visited); got != "s1" {
		t.Fatalf("nextParentID = %q, want s1", got)
	}
	visited["s1"] = true
	if got := nextParentID(item, visited); got != "ser1" {
		t.Fatalf("nextParentID after s1 = %q, want ser1", got)
	}
	visited["ser1"] = true
	if got := nextParentID(item, visited); got != "" {
		t.Fatalf("nextParentID after all visited = %q, want empty", got)
	}
}

func TestNeedsRatingInheritance(t *testing.T) {
	if !needsRatingInheritance(map[string]any{"Type": "Episode"}) || !needsRatingInheritance(map[string]any{"Type": "Season"}) {
		t.Fatal("Season/Episode should inherit")
	}
	if needsRatingInheritance(map[string]any{"Type": "Movie"}) {
		t.Fatal("Movie should not inherit")
	}
}

// --- §6.2 structured errors, flags, report ---

func TestUpstreamResponseErrorNoURL(t *testing.T) {
	err := &upstreamResponseError{StatusCode: 400, Method: "GET", Path: "/Items", Detail: "bad request"}
	msg := err.Error()
	if strings.Contains(msg, "http://") || strings.Contains(msg, "https://") {
		t.Fatalf("error message leaked a URL: %q", msg)
	}
	if !isBadRequestError(err) {
		t.Fatal("isBadRequestError should be true for a 400 upstreamResponseError")
	}
	if isBadRequestError(&upstreamResponseError{StatusCode: 500}) {
		t.Fatal("isBadRequestError should be false for a 500")
	}
	if isBadRequestError(errors.New("plain error")) {
		t.Fatal("isBadRequestError should be false for a non-structured error")
	}
}

func TestTotalRecordCount(t *testing.T) {
	payload := map[string]any{"TotalRecordCount": float64(250)}
	if total, ok := totalRecordCount(payload); !ok || total != 250 {
		t.Fatalf("totalRecordCount = (%d,%v), want (250,true)", total, ok)
	}
	if _, ok := totalRecordCount(map[string]any{"Items": []any{}}); ok {
		t.Fatal("totalRecordCount should report false without the field")
	}
}

func TestCatalogBudget(t *testing.T) {
	b := newCatalogRequestBudget()
	for i := 0; i < 100; i++ {
		if !b.tryAcquire() {
			t.Fatalf("budget exhausted early at %d", i)
		}
	}
	if b.tryAcquire() {
		t.Fatal("budget should be exhausted after 100 attempts")
	}
}

// --- §8.7 fallback route classification ---

func TestClassifyParentalRoute(t *testing.T) {
	control := []struct{ method, path string }{
		{"GET", "/System/Ping"},
		{"POST", "/Sessions/Playing/Progress"},
		{"POST", "/Sessions/Capabilities/Full"},
		{"GET", "/Users/abc/Configuration"},
		{"POST", "/Users/abc/Policy"},
		{"POST", "/Users/abc/UserData"},
		{"GET", "/emby/System/Ping"},
	}
	for _, c := range control {
		if classifyParentalRoute(c.method, c.path) != routeControl {
			t.Errorf("classify(%s %s) != control", c.method, c.path)
		}
		if !parentalFallbackAllowed(c.method, c.path) {
			t.Errorf("parentalFallbackAllowed(%s %s) = false, want true", c.method, c.path)
		}
	}
	deny := []struct{ method, path string }{
		{"GET", "/Items/123"},
		{"GET", "/Videos/123/stream.mp4"},
		{"GET", "/Sessions"}, // a GET that could leak content
	}
	for _, c := range deny {
		if parentalFallbackAllowed(c.method, c.path) {
			t.Errorf("parentalFallbackAllowed(%s %s) = true, want false", c.method, c.path)
		}
	}
}

// --- §8.6.1 stream resource store + rewrite ---

func TestStreamResourceStoreLifecycle(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	s := newStreamResourceStore()
	s.now = func() time.Time { return base }
	id, ok := s.put(streamResourceEntry{UserID: "u1", VirtualItemID: "v1", ServerID: "s1", OriginalID: "o1", ResourceURL: "https://cdn/x.ts"})
	if !ok || id == "" {
		t.Fatal("put failed")
	}
	if e, ok := s.get(id); !ok || e.ResourceURL != "https://cdn/x.ts" {
		t.Fatalf("get = (%+v,%v)", e, ok)
	}
	// Idle expiry: past the TTL the handle is gone.
	s.now = func() time.Time { return base.Add(2 * streamResourceIdleTTL) }
	if _, ok := s.get(id); ok {
		t.Fatal("expired handle should not resolve")
	}
}

func TestStreamResourceStoreCapacity(t *testing.T) {
	s := newStreamResourceStore()
	s.max = 1
	if _, ok := s.put(streamResourceEntry{UserID: "u"}); !ok {
		t.Fatal("first put should succeed")
	}
	if _, ok := s.put(streamResourceEntry{UserID: "u"}); ok {
		t.Fatal("second put should fail at capacity")
	}
}

func TestRewriteM3U8ForItemWithResources(t *testing.T) {
	app := &App{StreamResources: newStreamResourceStore()}
	content := strings.Join([]string{
		"#EXTM3U",
		`#EXT-X-KEY:METHOD=AES-128,URI="key.bin"`,
		`#EXT-X-MAP:URI="init.mp4"`,
		"#EXTINF:6,",
		"seg1.ts",
		"https://cdn.example.com/seg2.ts",
	}, "\n")
	ctx := resourceRewriteContext{UserID: "u1", VirtualItemID: "vid", ServerID: "s1", OriginalID: "o1"}
	out, err := app.RewriteM3U8ForItemWithResources(content, "https://up.example.com/videos/1/master.m3u8", "tok", ctx)
	if err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if strings.Contains(out, "cdn.example.com") || strings.Contains(out, "key.bin") || strings.Contains(out, "seg1.ts") {
		t.Fatalf("rewrite left an external/unrewritten resource:\n%s", out)
	}
	if !strings.Contains(out, "/Videos/vid/eio-resource/") {
		t.Fatalf("rewrite did not produce a local resource URL:\n%s", out)
	}
	// Four resources: key, init, seg1, seg2.
	if got := app.StreamResources.size(); got != 4 {
		t.Fatalf("registered %d resources, want 4", got)
	}
	// An unsupported scheme is a hard error, not an untouched link.
	if _, err := app.RewriteM3U8ForItemWithResources("javascript:alert(1)", "https://up/", "", ctx); err == nil {
		t.Fatal("expected an error for a javascript: resource")
	}
}

// --- §6.4 display-source routing ---

func TestDisplaySourceRecord(t *testing.T) {
	store := &IDStore{displaySources: map[string]imageSourceEntry{}}
	store.SetDisplaySource("vid", "srv", "orig")
	got, ok := store.ResolveDisplaySource("vid")
	if !ok || got.ServerID != "srv" || got.OriginalID != "orig" {
		t.Fatalf("ResolveDisplaySource = (%+v,%v)", got, ok)
	}
	if _, ok := store.ResolveDisplaySource("missing"); ok {
		t.Fatal("unknown display source should not resolve")
	}
}
