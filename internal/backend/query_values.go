package backend

import (
	"net/url"
	"strings"
)

// queryValue reads an Emby parameter case-insensitively. queryKey gives a
// deterministic preference if a client supplies conflicting case variants.
func queryValue(values url.Values, name string) string {
	key := queryKey(values, name)
	if key == "" {
		return ""
	}
	return values.Get(key)
}

// setQueryValue replaces ALL case variants before writing the canonical key.
// url.Values.Set alone would leave Limit and limit in the same upstream request,
// which Emby's case-insensitive parser combines into an invalid scalar value.
func setQueryValue(values url.Values, name, value string) {
	for key := range values {
		if strings.EqualFold(key, name) {
			delete(values, key)
		}
	}
	values.Set(name, value)
}
