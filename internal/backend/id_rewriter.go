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

func rewriteResponseIDs(value any, serverID string, idStore *IDStore, proxyServerID, proxyUserID string) any {
	switch typed := value.(type) {
	case []any:
		for i := range typed {
			typed[i] = rewriteResponseIDs(typed[i], serverID, idStore, proxyServerID, proxyUserID)
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
						typed[key] = idStore.GetOrCreateVirtualID(text, serverID)
					}
				}
				continue
			case "SessionId", "PlaySessionId":
				if text, ok := raw.(string); ok && text != "" {
					typed[key] = idStore.GetOrCreateVirtualID(text, serverID)
				}
				continue
			case "ImageTags", "BackdropImageTags", "ParentBackdropImageTags", "ImageBlurHashes":
				continue
			case "UserData":
				if block, ok := raw.(map[string]any); ok {
					if itemID, ok := block["ItemId"].(string); ok && itemID != "" {
						block["ItemId"] = idStore.GetOrCreateVirtualID(itemID, serverID)
					}
				}
				// The UserData block carries a single ID field (ItemId); recursing into it would
				// rewrite the virtual ID we just minted a second time (Node reference does the same).
				continue
			}

			if _, ok := simpleIDFields[key]; ok {
				if text, ok := raw.(string); ok && text != "" && text != "0" {
					typed[key] = idStore.GetOrCreateVirtualID(text, serverID)
				}
				continue
			}

			if key == "Trickplay" {
				if block, ok := raw.(map[string]any); ok {
					rewritten := map[string]any{}
					for oldKey, blockValue := range block {
						rewritten[idStore.GetOrCreateVirtualID(oldKey, serverID)] = blockValue
					}
					typed[key] = rewritten
				}
				continue
			}

			typed[key] = rewriteResponseIDs(raw, serverID, idStore, proxyServerID, proxyUserID)
		}
		return typed
	default:
		return value
	}
}

func rewriteIDQueryValues(values map[string][]string, idStore *IDStore) (map[string][]string, string, bool) {
	if values == nil {
		return map[string][]string{}, "", false
	}
	out := map[string][]string{}
	serverID := ""
	serverDetected := false
	for key, rawValues := range values {
		cloned := append([]string(nil), rawValues...)
		for i, raw := range cloned {
			if _, ok := simpleIDFields[key]; ok || key == "PlaySessionId" || key == "SessionId" || strings.EqualFold(key, "MediaSourceId") {
				if resolved := idStore.ResolveVirtualID(raw); resolved != nil {
					cloned[i] = resolved.OriginalID
					if !serverDetected {
						serverID = resolved.ServerID
						serverDetected = true
					}
				}
			}
		}
		out[key] = cloned
	}
	return out, serverID, serverDetected
}
