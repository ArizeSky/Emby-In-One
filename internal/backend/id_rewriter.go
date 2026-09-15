package backend

import "strings"

var simpleIDFields = map[string]struct{}{
	"Id":                    {},
	"ItemId":                {},
	"ParentId":              {},
	"SeriesId":              {},
	"SeasonId":              {},
	"MediaSourceId":         {},
	"PlaylistItemId":        {},
	"DisplayPreferencesId":  {},
	"ParentLogoItemId":      {},
	"ParentBackdropItemId":  {},
	"ParentThumbItemId":     {},
	"ChannelId":             {},
	"AlbumId":               {},
	"ArtistId":              {},
	"PlaylistId":            {},
	"CollectionId":          {},
	"BoxSetId":              {},
	"ThemeSongId":           {},
	"ThemeVideoId":          {},
	"InternalId":            {},
	"TopParentId":           {},
	"BaseItemId":            {},
	"CollectionItemId":      {},
	"LiveStreamId":          {},
	"LibraryItemId":         {},
	"PresentationUniqueKey": {},
	"RemoteId":              {},
	"StreamId":              {},
}

func rewriteResponseIDs(value any, serverIndex int, idStore *IDStore, proxyServerID, proxyUserID string) any {
	switch typed := value.(type) {
	case []any:
		for i := range typed {
			typed[i] = rewriteResponseIDs(typed[i], serverIndex, idStore, proxyServerID, proxyUserID)
		}
		return typed
	case map[string]any:
		for key, raw := range typed {
			switch key {
			case "ServerId":
				if _, ok := raw.(string); ok {
					typed[key] = proxyServerID
				}
				continue
			case "UserId":
				if text, ok := raw.(string); ok {
					if proxyUserID != "" {
						typed[key] = proxyUserID
					} else if text != "" {
						typed[key] = idStore.GetOrCreateVirtualID(text, serverIndex)
					}
				}
				continue
			case "SessionId", "PlaySessionId":
				if text, ok := raw.(string); ok && text != "" {
					typed[key] = idStore.GetOrCreateVirtualID(text, serverIndex)
				}
				continue
			case "ImageTags", "BackdropImageTags", "ParentBackdropImageTags", "ImageBlurHashes":
				continue
			case "UserData":
				if block, ok := raw.(map[string]any); ok {
					if itemID, ok := block["ItemId"].(string); ok && itemID != "" {
						block["ItemId"] = idStore.GetOrCreateVirtualID(itemID, serverIndex)
					}
				}
			}

			if _, ok := simpleIDFields[key]; ok {
				if text, ok := raw.(string); ok && text != "" && text != "0" {
					typed[key] = idStore.GetOrCreateVirtualID(text, serverIndex)
				}
				continue
			}

			if key == "Trickplay" {
				if block, ok := raw.(map[string]any); ok {
					rewritten := map[string]any{}
					for oldKey, blockValue := range block {
						rewritten[idStore.GetOrCreateVirtualID(oldKey, serverIndex)] = blockValue
					}
					typed[key] = rewritten
				}
				continue
			}

			typed[key] = rewriteResponseIDs(raw, serverIndex, idStore, proxyServerID, proxyUserID)
		}
		return typed
	default:
		return value
	}
}

func rewriteIDQueryValues(values map[string][]string, idStore *IDStore) (map[string][]string, int, bool) {
	if values == nil {
		return map[string][]string{}, 0, false
	}
	out := map[string][]string{}
	serverIndex := 0
	serverDetected := false
	for key, rawValues := range values {
		cloned := append([]string(nil), rawValues...)
		for i, raw := range cloned {
			if _, ok := simpleIDFields[key]; ok || key == "PlaySessionId" || key == "SessionId" || strings.EqualFold(key, "MediaSourceId") {
				if resolved := idStore.ResolveVirtualID(raw); resolved != nil {
					cloned[i] = resolved.OriginalID
					if !serverDetected {
						serverIndex = resolved.ServerIndex
						serverDetected = true
					}
				}
			}
		}
		out[key] = cloned
	}
	return out, serverIndex, serverDetected
}
