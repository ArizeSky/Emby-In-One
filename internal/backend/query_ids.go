package backend

import (
	"net/url"
	"strings"
)

const maxBatchIDCount = 2000

func hasBatchIDQuery(values url.Values) bool {
	for key, rawValues := range values {
		if !isBatchIDQueryKey(key) {
			continue
		}
		for _, raw := range rawValues {
			if strings.TrimSpace(raw) != "" {
				return true
			}
		}
	}
	return false
}

func isBatchIDQueryKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "ids", "itemids", "personids":
		return true
	default:
		return false
	}
}

func translateVirtualIDForServer(id string, serverIndex int, idStore *IDStore) (string, bool) {
	resolved := idStore.ResolveVirtualID(id)
	if resolved == nil {
		return id, true
	}
	if resolved.ServerIndex == serverIndex {
		return resolved.OriginalID, true
	}
	for _, other := range resolved.OtherInstances {
		if other.ServerIndex == serverIndex {
			return other.OriginalID, true
		}
	}
	return "", false
}

func translateBatchIDQueryForServer(values url.Values, serverIndex int, idStore *IDStore) (url.Values, bool) {
	cloned := cloneValues(values)
	for key, rawValues := range cloned {
		if !isBatchIDQueryKey(key) {
			continue
		}
		translatedValues := make([]string, 0, len(rawValues))
		for _, raw := range rawValues {
			parts := strings.Split(raw, ",")
			if len(parts) > maxBatchIDCount {
				return nil, false
			}
			translatedParts := make([]string, 0, len(parts))
			for _, part := range parts {
				part = strings.TrimSpace(part)
				if part == "" {
					continue
				}
				translated, ok := translateVirtualIDForServer(part, serverIndex, idStore)
				if !ok {
					continue
				}
				translatedParts = append(translatedParts, translated)
			}
			if len(translatedParts) == 0 {
				return nil, false
			}
			translatedValues = append(translatedValues, strings.Join(translatedParts, ","))
		}
		cloned[key] = translatedValues
	}
	return cloned, true
}
