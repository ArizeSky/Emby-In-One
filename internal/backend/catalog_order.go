package backend

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

var catalogRandomProcessSeed = newCatalogRandomProcessSeed()

func newCatalogRandomProcessSeed() string {
	var seed [32]byte
	if _, err := rand.Read(seed[:]); err == nil {
		return string(seed[:])
	}
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

func catalogRandomScore(seed, id string) [32]byte {
	return sha256.Sum256([]byte(seed + "\x00" + id))
}

type catalogSortField struct {
	name       string
	descending bool
}

type unsupportedCatalogSortKeyError struct {
	key string
}

func (e unsupportedCatalogSortKeyError) Error() string {
	return fmt.Sprintf("unsupported catalog sort key %q", e.key)
}

func catalogSortFields(query url.Values) ([]catalogSortField, error) {
	sortByKey := queryKey(query, "SortBy")
	if sortByKey == "" {
		return nil, nil
	}
	keys := splitQueryList(strings.Join(query[sortByKey], ","))
	if len(keys) == 0 {
		return nil, nil
	}

	orderKey := queryKey(query, "SortOrder")
	orders := []string(nil)
	if orderKey != "" {
		orders = splitQueryList(strings.Join(query[orderKey], ","))
	}
	fields := make([]catalogSortField, 0, len(keys))
	for i, key := range keys {
		normalized := strings.ToLower(strings.TrimSpace(key))
		switch normalized {
		case "name", "sortname", "productionyear", "year", "datecreated", "communityrating", "seriessortname", "random":
		default:
			return nil, unsupportedCatalogSortKeyError{key: key}
		}

		order := "ascending"
		if len(orders) > 0 {
			orderIndex := min(i, len(orders)-1)
			order = strings.ToLower(orders[orderIndex])
			if order != "ascending" && order != "descending" {
				return nil, fmt.Errorf("unsupported sort order %q", orders[orderIndex])
			}
		}
		fields = append(fields, catalogSortField{name: normalized, descending: order == "descending"})
	}
	return fields, nil
}

func validateCatalogSort(query url.Values) error {
	_, err := catalogSortFields(query)
	return err
}

// sortCatalogItems globally orders the merged, filtered, deduplicated candidate
// set. Random uses an ID-derived pseudorandom key, so paging and upstream arrival
// order do not affect the result. RandomSeed opts into a reproducible sequence;
// without it, the process-random seed keeps a sequence stable for this process.
func sortCatalogItems(items []map[string]any, query url.Values) error {
	fields, err := catalogSortFields(query)
	if err != nil || len(fields) == 0 {
		return err
	}
	seed := catalogRandomProcessSeed
	if key := queryKey(query, "RandomSeed"); key != "" && len(query[key]) > 0 {
		seed = query[key][0]
	}
	sort.SliceStable(items, func(i, j int) bool {
		for _, field := range fields {
			var cmp int
			if field.name == "random" {
				left, right := catalogRandomScore(seed, itemID(items[i])), catalogRandomScore(seed, itemID(items[j]))
				switch {
				case bytes.Compare(left[:], right[:]) < 0:
					cmp = -1
				case bytes.Compare(left[:], right[:]) > 0:
					cmp = 1
				}
			} else {
				cmp = compareCatalogField(items[i], items[j], field.name)
			}
			if cmp == 0 {
				continue
			}
			if field.descending {
				return cmp > 0
			}
			return cmp < 0
		}
		leftID, rightID := itemID(items[i]), itemID(items[j])
		if leftID == rightID {
			return false
		}
		if leftID == "" {
			return false
		}
		if rightID == "" {
			return true
		}
		return leftID < rightID
	})
	return nil
}

func compareCatalogField(left, right map[string]any, key string) int {
	switch key {
	case "name":
		return compareCatalogText(itemText(left, "Name"), itemText(right, "Name"))
	case "sortname":
		return compareCatalogText(itemSortName(left), itemSortName(right))
	case "seriessortname":
		return compareCatalogText(itemText(left, "SeriesSortName"), itemText(right, "SeriesSortName"))
	case "productionyear", "year":
		return compareCatalogNumber(catalogYear(left), catalogYear(right))
	case "communityrating":
		return compareCatalogNumber(itemNumber(left, "CommunityRating"), itemNumber(right, "CommunityRating"))
	case "datecreated":
		return compareCatalogDates(itemText(left, "DateCreated"), itemText(right, "DateCreated"))
	case "random":
		return compareCatalogText(itemID(left), itemID(right))
	default:
		return 0
	}
}

func catalogYear(item map[string]any) float64 {
	if _, ok := item["ProductionYear"]; ok {
		return itemNumber(item, "ProductionYear")
	}
	return itemNumber(item, "Year")
}

func compareCatalogText(left, right string) int {
	left, right = strings.ToLower(left), strings.ToLower(right)
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

func compareCatalogNumber(left, right float64) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

func compareCatalogDates(left, right string) int {
	leftTime, leftOK := parseCatalogDate(left)
	rightTime, rightOK := parseCatalogDate(right)
	if leftOK && rightOK {
		if leftTime.Before(rightTime) {
			return -1
		}
		if leftTime.After(rightTime) {
			return 1
		}
		return 0
	}
	return compareCatalogText(left, right)
}

func parseCatalogDate(value string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func ensureCatalogSortFields(values url.Values) {
	fields, err := catalogSortFields(values)
	if err != nil || len(fields) == 0 {
		return
	}
	key := queryKey(values, "Fields")
	if key == "" {
		key = "Fields"
	}
	existing := splitQueryList(strings.Join(values[key], ","))
	seen := make(map[string]bool, len(existing))
	for _, field := range existing {
		seen[strings.ToLower(field)] = true
	}
	for _, sortField := range fields {
		var requested string
		switch sortField.name {
		case "name":
			requested = "Name"
		case "sortname":
			requested = "SortName"
		case "seriessortname":
			requested = "SeriesSortName"
		case "productionyear", "year":
			requested = "ProductionYear"
		case "datecreated":
			requested = "DateCreated"
		case "communityrating":
			requested = "CommunityRating"
		}
		if requested != "" && !seen[strings.ToLower(requested)] {
			existing = append(existing, requested)
			seen[strings.ToLower(requested)] = true
		}
	}
	values.Set(key, strings.Join(existing, ","))
}

func sortResumeItemsByLastPlayed(items []map[string]any) {
	sort.SliceStable(items, func(i, j int) bool {
		left, right := resumeLastPlayedDate(items[i]), resumeLastPlayedDate(items[j])
		if cmp := compareCatalogDates(left, right); cmp != 0 {
			return cmp > 0
		}
		leftID, rightID := itemID(items[i]), itemID(items[j])
		if leftID == "" || rightID == "" {
			return false
		}
		return leftID < rightID
	})
}

func resumeLastPlayedDate(item map[string]any) string {
	userData, _ := item["UserData"].(map[string]any)
	return itemText(userData, "LastPlayedDate")
}

func writeCatalogSortError(w http.ResponseWriter, err error) {
	var unsupported unsupportedCatalogSortKeyError
	if ok := errors.As(err, &unsupported); ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"message": "Unsupported catalog sort key",
			"code":    "UnsupportedSortKey",
			"key":     unsupported.key,
		})
		return
	}
	writeJSON(w, http.StatusBadRequest, map[string]any{
		"message": "Invalid catalog sort order",
		"code":    "InvalidSortOrder",
	})
}
