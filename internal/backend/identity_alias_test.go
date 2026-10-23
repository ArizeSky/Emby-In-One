package backend

import (
	"net/http"
	"testing"
)

func TestIdentityAliasesMergeProgressAndFavoriteRemoval(t *testing.T) {
	up := newAggUpstreamStub(t)
	withTempAppConfig(t, dualUpstreamConfig(up.server.URL, up.server.URL), func(a *App, _ http.Handler) {
		first, second := a.Upstream.Clients()[0].ID, a.Upstream.Clients()[1].ID
		va := a.IDStore.GetOrCreateVirtualID("shared", first)
		vb := a.IDStore.GetOrCreateVirtualID("copy", second)
		unrelated := a.IDStore.GetOrCreateVirtualID("shared", second)
		a.IDStore.AssociateAdditionalInstance(va, "copy", second)
		aliases := a.IDStore.AliasVirtualIDs(vb)
		if len(aliases) != 2 || containsString(aliases, unrelated) {
			t.Fatalf("aliases = %v", aliases)
		}
		if err := a.WatchStore.RecordProgress(&WatchProgress{ProxyUserID: "user", VirtualItemID: va, PositionTicks: 100, RuntimeTicks: 1000}); err != nil {
			t.Fatal(err)
		}
		if err := a.WatchStore.SetFavorite("user", vb, true); err != nil {
			t.Fatal(err)
		}
		matched := a.localUserStateRows("user", userStateFilter{favorite: true})
		if _, ok := matched[va]; !ok {
			t.Fatal("canonical ID missing from alias favorite filter")
		}
		if _, ok := matched[unrelated]; ok {
			t.Fatal("unrelated ID entered favorite filter")
		}
		p, err := a.localWatchState("user", va)
		if err != nil || p == nil || !p.IsFavorite || p.PositionTicks != 100 {
			t.Fatalf("state=%+v err=%v", p, err)
		}
		if err := a.WatchStore.SetFavoriteAliases("user", aliases, false); err != nil {
			t.Fatal(err)
		}
		p, err = a.localWatchState("user", vb)
		if err != nil || p == nil || p.IsFavorite || p.PositionTicks != 100 {
			t.Fatalf("removed state=%+v err=%v", p, err)
		}
		other, err := a.localWatchState("another-user", va)
		if err != nil || other != nil {
			t.Fatalf("cross-user state=%+v err=%v", other, err)
		}
	})
}

func TestIdentityAliasesSurviveRestart(t *testing.T) {
	dir := t.TempDir()
	s, err := NewIDStore(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	va, vb := s.GetOrCreateVirtualID("a", "A"), s.GetOrCreateVirtualID("b", "B")
	s.AssociateAdditionalInstance(va, "b", "B")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = NewIDStore(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, id := range []string{va, vb} {
		aliases := s.AliasVirtualIDs(id)
		if len(aliases) != 2 || !containsString(aliases, va) || !containsString(aliases, vb) {
			t.Fatalf("restarted aliases=%v", aliases)
		}
	}
}

func TestIdentityAliasFavoriteTransactionRollback(t *testing.T) {
	s, err := NewIDStore(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ws, err := NewWatchStore(s.DB(), nil)
	if err != nil {
		t.Fatal(err)
	}
	aliases := []string{"old-public-id", "new-public-id"}
	if err := ws.SetFavoriteAliases("user", aliases, true); err != nil {
		t.Fatal(err)
	}
	if err := s.DB().exec(`CREATE TRIGGER reject_alias_update BEFORE INSERT ON user_watch_progress
  WHEN NEW.virtual_item_id = 'new-public-id' BEGIN SELECT RAISE(ABORT, 'forced alias write failure'); END;`); err != nil {
		t.Fatal(err)
	}
	if err := ws.SetFavoriteAliases("user", aliases, false); err == nil {
		t.Fatal("expected forced transaction error")
	}
	for _, id := range aliases {
		p, err := ws.GetProgress("user", id)
		if err != nil || p == nil || !p.IsFavorite {
			t.Fatalf("partial favorite mutation: %s state=%+v err=%v", id, p, err)
		}
	}
}
