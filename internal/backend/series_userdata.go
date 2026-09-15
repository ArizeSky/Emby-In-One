package backend

type seriesInstance struct {
	OriginalID  string
	ServerIndex int
	Client      *UpstreamClient
}

func buildSeriesInstances(resolved *routeResolution, upstream *UpstreamPool) []seriesInstance {
	instances := []seriesInstance{}
	if resolved == nil || resolved.Client == nil || !resolved.Client.Online {
		return instances
	}
	instances = append(instances, seriesInstance{
		OriginalID:  resolved.OriginalID,
		ServerIndex: resolved.ServerIndex,
		Client:      resolved.Client,
	})
	for _, other := range resolved.OtherInstances {
		client := upstream.GetClient(other.ServerIndex)
		if client == nil || !client.Online {
			continue
		}
		duplicate := false
		for _, existing := range instances {
			if existing.ServerIndex == other.ServerIndex && existing.OriginalID == other.OriginalID {
				duplicate = true
				break
			}
		}
		if !duplicate {
			instances = append(instances, seriesInstance{
				OriginalID:  other.OriginalID,
				ServerIndex: other.ServerIndex,
				Client:      client,
			})
		}
	}
	return instances
}

func belongsToSeries(item map[string]any, originalIDs map[string]struct{}) bool {
	if item == nil {
		return false
	}
	for _, key := range []string{"SeriesId", "ParentId", "GrandparentId"} {
		if text, ok := item[key].(string); ok {
			if _, found := originalIDs[text]; found {
				return true
			}
		}
	}
	return false
}

func filterSeriesItems(items []map[string]any, originalIDs map[string]struct{}) []map[string]any {
	filtered := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if belongsToSeries(item, originalIDs) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}
