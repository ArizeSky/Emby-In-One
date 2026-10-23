package backend

import (
	"net/http"
	"testing"
)

func withS7MergeApp(t *testing.T, run func(*App)) {
	t.Helper()
	config := "server:\n  port: 8096\n  name: TestServer\nadmin:\n  username: admin\n  password: testpass\nupstream: []\n"
	withTempAppPrepared(t, config, nil, func(app *App, _ http.Handler, _ string) {
		if err := app.ConfigStore.Mutate(func(cfg *Config) error {
			cfg.Upstream = []UpstreamConfig{{ID: "srv-a", Name: "A"}, {ID: "srv-b", Name: "B"}}
			return nil
		}); err != nil {
			t.Fatalf("configure merge sources: %v", err)
		}
		run(app)
	})
}

func s7MergePair(app *App, first, second map[string]any, firstServer, secondServer string) []map[string]any {
	return app.mergeRoundRobinItems([]upstreamItemsResult{
		{ServerID: firstServer, Items: []map[string]any{first}},
		{ServerID: secondServer, Items: []map[string]any{second}},
	}, app.Auth.ProxyUserID())
}

func s7Movie(id, itemType, name string, year int, providers map[string]any) map[string]any {
	item := map[string]any{"Id": id, "Type": itemType, "Name": name}
	if year != 0 {
		item["ProductionYear"] = year
	}
	if providers != nil {
		item["ProviderIds"] = providers
	}
	return item
}

func TestS7MovieDedupUsesTypeScopedStrongIDsOrStrictNameYear(t *testing.T) {
	withS7MergeApp(t, func(app *App) {
		cases := []struct {
			name  string
			first map[string]any
			other map[string]any
			want  int
		}{
			{
				name:  "same TMDB id across sources",
				first: s7Movie("tmdb-a", "Movie", "Film", 2020, map[string]any{"Tmdb": "123"}),
				other: s7Movie("tmdb-b", "Movie", "Film", 2020, map[string]any{"Tmdb": "123"}),
				want:  1,
			},
			{
				name:  "same provider id across media types",
				first: s7Movie("type-a", "Movie", "Film", 2020, map[string]any{"Tmdb": "124"}),
				other: s7Movie("type-b", "Series", "Film", 2020, map[string]any{"Tmdb": "124"}),
				want:  2,
			},
			{
				name:  "same provider id across Movie and Episode",
				first: s7Movie("movie-episode-a", "Movie", "Film", 2020, map[string]any{"Tmdb": "124e"}),
				other: map[string]any{"Id": "movie-episode-b", "Type": "Episode", "ProviderIds": map[string]any{"Tmdb": "124e"}},
				want:  2,
			},
			{
				name:  "same name without years",
				first: s7Movie("yearless-a", "Movie", "Remake", 0, nil),
				other: s7Movie("yearless-b", "Movie", "Remake", 0, nil),
				want:  2,
			},
			{
				name:  "zero production year is missing",
				first: map[string]any{"Id": "year-zero-a", "Type": "Movie", "Name": "Remake", "ProductionYear": 0},
				other: s7Movie("year-zero-b", "Movie", "Remake", 0, nil),
				want:  2,
			},
			{
				name:  "same name and non-empty year",
				first: s7Movie("same-year-a", "Movie", "Film", 2020, nil),
				other: s7Movie("same-year-b", "Movie", "Film", 2020, nil),
				want:  1,
			},
			{
				name:  "same numeric value in different provider namespaces",
				first: s7Movie("namespace-a", "Movie", "Film", 2020, map[string]any{"Tmdb": "125"}),
				other: s7Movie("namespace-b", "Movie", "Film", 2020, map[string]any{"Tvdb": "125"}),
				want:  2,
			},
			{
				name:  "conflicting strong IDs are not bridged by matching title",
				first: s7Movie("conflict-a", "Movie", "Film", 2020, map[string]any{"Tmdb": "126", "Imdb": "tt126a"}),
				other: s7Movie("conflict-b", "Movie", "Film", 2020, map[string]any{"Tmdb": "126", "Imdb": "tt126b"}),
				want:  2,
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got := s7MergePair(app, tc.first, tc.other, "srv-a", "srv-b")
				if len(got) != tc.want {
					t.Fatalf("merged item count = %d, want %d", len(got), tc.want)
				}
			})
		}
	})
}

func s7Episode(id, seriesID, seriesName string, season, episode int) map[string]any {
	return map[string]any{
		"Id": id, "Type": "Episode", "Name": "Episode", "SeriesId": seriesID,
		"SeriesName": seriesName, "ParentIndexNumber": season, "IndexNumber": episode,
	}
}

func TestS7EpisodeDedupRequiresTrustedSeriesGroupAndCoordinates(t *testing.T) {
	withS7MergeApp(t, func(app *App) {
		t.Run("same series name but distinct series identities", func(t *testing.T) {
			first := s7Episode("office-us-ep", "office-us", "The Office", 1, 1)
			second := s7Episode("office-uk-ep", "office-uk", "The Office", 1, 1)
			if got := s7MergePair(app, first, second, "srv-a", "srv-b"); len(got) != 2 {
				t.Fatalf("same series name merged distinct series: got %d items", len(got))
			}
		})

		t.Run("same trusted series but different coordinates", func(t *testing.T) {
			first := s7Episode("coord-1", "shared-series", "The Office", 1, 1)
			second := s7Episode("coord-2", "shared-series", "The Office", 2, 1)
			if got := s7MergePair(app, first, second, "srv-a", "srv-a"); len(got) != 2 {
				t.Fatalf("episodes from different seasons merged: got %d items", len(got))
			}
		})

		t.Run("trusted aliased series and same coordinates", func(t *testing.T) {
			seriesID := app.IDStore.GetOrCreateVirtualID("series-a", "srv-a")
			app.IDStore.AssociateAdditionalInstance(seriesID, "series-b", "srv-b")
			first := s7Episode("same-ep-a", "series-a", "A Show", 1, 2)
			second := s7Episode("same-ep-b", "series-b", "Different display name", 1, 2)
			if got := s7MergePair(app, first, second, "srv-a", "srv-b"); len(got) != 1 {
				t.Fatalf("trusted same-series episode copies did not merge: got %d items", len(got))
			}
		})
	})
}
