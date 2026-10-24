package backend

import (
	"net/url"
	"strings"
)

func catalogHasRandomSort(query url.Values) bool {
	for _, key := range splitQueryList(strings.Join(query[queryKey(query, "SortBy")], ",")) {
		if strings.EqualFold(key, "Random") {
			return true
		}
	}
	return false
}

// Random belongs to the merged, filtered candidate set, not each upstream page.
// Scanning with an upstream random order can duplicate or omit items across
// pages. The original client query is retained separately for final sorting.
func prepareUpstreamCatalogRandom(query url.Values) {
	if !catalogHasRandomSort(query) {
		return
	}
	setQueryValue(query, "SortBy", "SortName")
	setQueryValue(query, "SortOrder", "Ascending")
	for key := range query {
		if strings.EqualFold(key, "RandomSeed") {
			delete(query, key)
		}
	}
}
