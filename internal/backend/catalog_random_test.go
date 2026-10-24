package backend

import (
	"net/url"
	"reflect"
	"testing"
)

func TestCatalogRandomSortIsCaseInsensitiveAndNotDateCreated(t *testing.T) {
	items := []map[string]any{
		{"Id": "a", "Name": "A", "DateCreated": "2024-01-01T00:00:00Z"},
		{"Id": "b", "Name": "B", "DateCreated": "2024-02-01T00:00:00Z"},
		{"Id": "c", "Name": "C", "DateCreated": "2024-03-01T00:00:00Z"},
		{"Id": "d", "Name": "D", "DateCreated": "2024-04-01T00:00:00Z"},
	}
	query := url.Values{"SortBy": {"rAnDoM"}, "RandomSeed": {"test-seed"}}
	if err := sortCatalogItems(items, query); err != nil {
		t.Fatalf("sortCatalogItems: %v", err)
	}
	got := catalogItemIDs(items)
	if reflect.DeepEqual(got, []string{"a", "b", "c", "d"}) {
		t.Fatalf("Random preserved DateCreated ordering: %v", got)
	}
	if err := validateCatalogSort(url.Values{"SortBy": {"RaNdOm"}}); err != nil {
		t.Fatalf("case-insensitive Random validation: %v", err)
	}
}

func TestCatalogRandomSortIsStableAcrossCandidateOrderAndPages(t *testing.T) {
	first := []map[string]any{{"Id": "a"}, {"Id": "b"}, {"Id": "c"}, {"Id": "d"}, {"Id": "e"}, {"Id": "f"}}
	second := []map[string]any{{"Id": "f"}, {"Id": "d"}, {"Id": "b"}, {"Id": "e"}, {"Id": "c"}, {"Id": "a"}}
	query := url.Values{"SortBy": {"Random"}, "RandomSeed": {"paging-seed"}}
	if err := sortCatalogItems(first, query); err != nil {
		t.Fatal(err)
	}
	if err := sortCatalogItems(second, query); err != nil {
		t.Fatal(err)
	}
	firstIDs, secondIDs := catalogItemIDs(first), catalogItemIDs(second)
	if !reflect.DeepEqual(firstIDs, secondIDs) {
		t.Fatalf("candidate ordering changed Random result: %v vs %v", firstIDs, secondIDs)
	}
	const pageSize = 2
	for start := 0; start < len(firstIDs); start += pageSize {
		end := min(start+pageSize, len(firstIDs))
		if !reflect.DeepEqual(firstIDs[start:end], secondIDs[start:end]) {
			t.Fatalf("page [%d:%d] differs: %v vs %v", start, end, firstIDs[start:end], secondIDs[start:end])
		}
	}
}

func TestCatalogRandomSeedControlsReproducibility(t *testing.T) {
	items := []map[string]any{{"Id": "a"}, {"Id": "b"}, {"Id": "c"}, {"Id": "d"}, {"Id": "e"}}
	sortWithSeed := func(seed string) []string {
		copyItems := append([]map[string]any(nil), items...)
		if err := sortCatalogItems(copyItems, url.Values{"SortBy": {"Random"}, "RandomSeed": {seed}}); err != nil {
			t.Fatalf("sortCatalogItems seed %q: %v", seed, err)
		}
		return catalogItemIDs(copyItems)
	}
	first := sortWithSeed("seed-one")
	if got := sortWithSeed("seed-one"); !reflect.DeepEqual(got, first) {
		t.Fatalf("same seed ordering = %v, want %v", got, first)
	}
	if got := sortWithSeed("seed-two"); reflect.DeepEqual(got, first) {
		t.Fatalf("different seed produced same ordering: %v", got)
	}
}

func TestCatalogRandomSortUnknownKeysRemainErrors(t *testing.T) {
	err := validateCatalogSort(url.Values{"SortBy": {"Random,NotARealKey"}})
	unsupported, ok := err.(unsupportedCatalogSortKeyError)
	if !ok || unsupported.key != "NotARealKey" {
		t.Fatalf("unknown key error = %#v, want unsupported key NotARealKey", err)
	}
}
