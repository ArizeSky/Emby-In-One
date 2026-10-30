package backend

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// cloneCatalogRawItems returns a deep copy of raw upstream items. The scanner's
// merge, image carryover and parental stages mutate maps and nested slices, so
// returning the cached maps directly would let one request poison every later
// request (plan Step 3).
func cloneCatalogRawItems(items []map[string]any) []map[string]any {
	if items == nil {
		return nil
	}
	out := make([]map[string]any, len(items))
	for i, item := range items {
		out[i] = cloneCatalogRawMap(item)
	}
	return out
}

func cloneCatalogRawMap(item map[string]any) map[string]any {
	if item == nil {
		return nil
	}
	out := make(map[string]any, len(item))
	for key, value := range item {
		out[key] = cloneCatalogRawValue(value)
	}
	return out
}

func cloneCatalogRawValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneCatalogRawMap(typed)
	case []any:
		if typed == nil {
			return []any(nil)
		}
		out := make([]any, len(typed))
		for i, entry := range typed {
			out[i] = cloneCatalogRawValue(entry)
		}
		return out
	case []map[string]any:
		return cloneCatalogRawItems(typed)
	case []string:
		return append([]string(nil), typed...)
	case []int:
		return append([]int(nil), typed...)
	case []int64:
		return append([]int64(nil), typed...)
	case []float64:
		return append([]float64(nil), typed...)
	case []bool:
		return append([]bool(nil), typed...)
	default:
		// JSON-decoded scalars (string, float64, bool, nil, json.Number) are
		// immutable. Unknown values are kept as-is rather than reflect-cloned:
		// the upstream decoder never produces mutable pointers here.
		return value
	}
}

// catalogSnapshotPageFingerprint identifies page membership, not the whole raw
// payload, so an upstream that ignores StartIndex is detected even when metadata
// in the repeated items changes between calls. Pages without IDs fall back to a
// canonical JSON hash.
func catalogSnapshotPageFingerprint(items []map[string]any) string {
	hash := sha256.New()
	ids := make([]string, len(items))
	allIDs := true
	for i, item := range items {
		id, _ := item["Id"].(string)
		if id == "" {
			allIDs = false
			break
		}
		ids[i] = id
	}
	if allIDs {
		hash.Write([]byte("ids\n"))
		for _, id := range ids {
			hash.Write([]byte(id))
			hash.Write([]byte{0})
		}
	} else {
		raw, err := json.Marshal(items)
		if err != nil {
			raw = []byte("unserializable")
		}
		hash.Write([]byte("json\n"))
		hash.Write(raw)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

// catalogSnapshotPayloadBytes is the serialized raw size used by the 32 MiB
// store-wide guard. It intentionally measures the JSON form the cache retains,
// not Go map overhead.
func catalogSnapshotPayloadBytes(items []map[string]any) int64 {
	if len(items) == 0 {
		return 0
	}
	raw, err := json.Marshal(items)
	if err != nil {
		// Refusing a page we cannot measure is safer than admitting it to a
		// bounded store with an unknown size.
		return 1<<62 - 1
	}
	return int64(len(raw))
}
