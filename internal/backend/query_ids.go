package backend

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const maxBatchIDCount = 2000

var errInvalidBatchIDQuery = errors.New("invalid batch ID query")

type batchIDQueryTranslation struct {
	Query    url.Values
	Complete bool
	Empty    bool
	Err      error
}

func hasBatchIDQuery(values url.Values) bool {
	for key := range values {
		if isBatchIDQueryKey(key) {
			return true
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

func translateVirtualIDForServer(id string, serverID string, idStore *IDStore) (string, bool) {
	resolved := idStore.ResolveVirtualID(id)
	if resolved == nil {
		return id, true
	}
	if resolved.ServerID == serverID {
		return resolved.OriginalID, true
	}
	for _, other := range resolved.OtherInstances {
		if other.ServerID == serverID {
			return other.OriginalID, true
		}
	}
	return "", false
}

// validateBatchIDQuery validates the client-supplied candidate list before any
// per-server translation. Empty IDs and oversized lists are client errors, not
// partial aggregation failures.
func validateBatchIDQuery(values url.Values) error {
	foundBatchKey := false
	count := 0
	for key, rawValues := range values {
		if !isBatchIDQueryKey(key) {
			continue
		}
		foundBatchKey = true
		for _, raw := range rawValues {
			if strings.TrimSpace(raw) == "" {
				continue
			}
			for _, part := range strings.Split(raw, ",") {
				if strings.TrimSpace(part) == "" {
					return errInvalidBatchIDQuery
				}
				count++
				if count > maxBatchIDCount {
					return errInvalidBatchIDQuery
				}
			}
		}
	}
	if foundBatchKey && count == 0 {
		return errInvalidBatchIDQuery
	}
	return nil
}

// translateBatchIDQueryForServer translates a batch candidate set without
// conflating a valid, server-local empty result with malformed input. Empty is
// complete: this server simply owns none of the requested virtual IDs.
func translateBatchIDQueryForServer(values url.Values, serverID string, idStore *IDStore) batchIDQueryTranslation {
	if err := validateBatchIDQuery(values); err != nil {
		return batchIDQueryTranslation{Err: err}
	}
	cloned := cloneValues(values)
	foundBatchKey := false
	for key, rawValues := range cloned {
		if !isBatchIDQueryKey(key) {
			continue
		}
		foundBatchKey = true
		translatedValues := make([]string, 0, len(rawValues))
		for _, raw := range rawValues {
			parts := strings.Split(raw, ",")
			translatedParts := make([]string, 0, len(parts))
			for _, part := range parts {
				part = strings.TrimSpace(part)
				if part == "" {
					continue
				}
				translated, ok := translateVirtualIDForServer(part, serverID, idStore)
				if ok {
					translatedParts = append(translatedParts, translated)
				}
			}
			if len(translatedParts) > 0 {
				translatedValues = append(translatedValues, strings.Join(translatedParts, ","))
			}
		}
		cloned[key] = translatedValues
	}
	if foundBatchKey && !hasTranslatedBatchID(cloned) {
		return batchIDQueryTranslation{Complete: true, Empty: true}
	}
	return batchIDQueryTranslation{Query: cloned, Complete: true}
}

func hasTranslatedBatchID(values url.Values) bool {
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

func invalidBatchIDResponse(w http.ResponseWriter) {
	writeJSON(w, http.StatusBadRequest, map[string]any{
		"message": fmt.Sprintf("Batch ID query must contain at most %d non-empty IDs", maxBatchIDCount),
		"code":    "InvalidBatchIDQuery",
	})
}
