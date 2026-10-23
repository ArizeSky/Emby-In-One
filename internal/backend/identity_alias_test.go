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
