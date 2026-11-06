package backend

import "net/http"

// Keep upstream resource URLs out of browser-facing query rewriting. In
// particular a subtitle or transcode signature belongs to its original URL, not
// to a virtual-ID path with a playback capability spliced into it.
type playbackResourceField struct {
	target     map[string]any
	key        string
	plan       playbackDirectStream
	attempt    string
	capability string
}

func (a *App) captureStrictPlaybackFields(r *http.Request, source map[string]any, client *UpstreamClient, original string) error {
	req := requestContextFrom(r.Context())
	if req == nil || req.PlaybackNegotiation == nil {
		return nil
	}
	capture := func(target map[string]any, key string) error {
		raw := playbackStringField(target, key)
		if raw == "" {
			return nil
		}
		plan := planPlaybackDirectStream(source, raw, client, original)
		if plan.resourceURL == "" {
			return hopQueryError("playback-unbound-url")
		}
		req.PlaybackNegotiation.fields = append(req.PlaybackNegotiation.fields, playbackResourceField{target: target, key: key, plan: plan, attempt: playbackStringField(source, "EioAttemptId"), capability: playbackStringField(source, "EioPlaybackToken")})
		return nil
	}
	if err := capture(source, "TranscodingUrl"); err != nil {
		return err
	}
	for _, stream := range asItems(map[string]any{"Items": source["MediaStreams"]}) {
		if err := capture(stream, "DeliveryUrl"); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) registerStrictPlaybackFields(r *http.Request, sources []map[string]any) error {
	req := requestContextFrom(r.Context())
	n := req.PlaybackNegotiation
	used := map[string]bool{}
	for _, source := range sources {
		used[playbackStringField(source, "EioAttemptId")] = true
	}
	fields := []playbackResourceField{}
	entries := []streamResourceEntry{}
	for _, field := range n.fields {
		if !used[field.attempt] {
			continue
		}
		fields = append(fields, field)
		entries = append(entries, streamResourceEntry{UserID: streamResourceUserID(req), VirtualItemID: r.PathValue("itemId"), ServerID: field.plan.serverID, OriginalID: field.plan.originalID, ResourceURL: field.plan.resourceURL, AttemptID: field.attempt, TimelineSession: field.plan.serverID + "\x00" + r.PathValue("itemId") + "\x00" + field.attempt, Provenance: field.plan.provenance.withKnownPrivate(req.ProxyToken, field.capability)})
	}
	if len(entries) == 0 {
		return nil
	}
	handles, ok := a.StreamResources.registerAll(entries)
	if !ok {
		return strictPlaybackPreparationError(ErrPlaybackIdentityCapacity)
	}
	for index, field := range fields {
		// strictPlaybackURL is subsequently applied once by the commit phase.
		field.target[field.key] = streamResourceClientURL(field.plan.routeRoot, r.PathValue("itemId"), handles[index], field.plan.container, field.capability)
	}
	return nil
}
