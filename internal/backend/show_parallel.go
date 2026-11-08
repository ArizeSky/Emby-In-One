package backend

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"
)

const showConcurrentRequests = 8

type showInstancePage struct {
	instance seriesInstance
	rows     []map[string]any
}

type showPageEvent struct {
	index int
	rows  []map[string]any
	done  bool
	err   error
}

// Request-owned, bounded parallel reads. Pages are published as soon as they
// arrive, so a later timeout cannot erase their prefix. Only the handler merges
// identities or filters policy; workers never mutate response state.
func (a *App) collectShowPages(r *http.Request, instances []seriesInstance, suffix string, base url.Values) ([]showInstancePage, showListAvailability) {
	cfg := a.ConfigStore.Snapshot()
	timeout := time.Duration(cfg.Timeouts.Global) * time.Millisecond
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()
	reqCtx := requestContextFrom(r.Context())
	budget := newCatalogRequestBudget()
	slots := make(chan struct{}, showConcurrentRequests)
	if reqCtx != nil && reqCtx.Parental != nil {
		budget = reqCtx.Parental.catalog
		slots = reqCtx.Parental.showSlots
	}
	// There can be at most 100 page events plus one terminal event per task.
	events := make(chan showPageEvent, 100+len(instances))
	jobs := make(chan int, len(instances))
	for i := range instances {
		jobs <- i
	}
	close(jobs)
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait() }()
	for worker := 0; worker < min(showConcurrentRequests, len(instances)); worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				select {
				case slots <- struct{}{}:
				case <-ctx.Done():
					events <- showPageEvent{index: index, done: true, err: ctx.Err()}
					continue
				}
				inst := instances[index]
				query, skip, err := a.showInstanceQuery(ctx, reqCtx, inst, instances, base, budget)
				if err == nil && !skip {
					_, err = a.fetchShowPages(ctx, reqCtx, inst, "/Shows/"+inst.OriginalID+"/"+suffix, query, budget, func(rows []map[string]any) {
						events <- showPageEvent{index: index, rows: rows}
					})
				}
				<-slots
				events <- showPageEvent{index: index, done: true, err: err}
			}
		}()
	}
	pages := make([]showInstancePage, len(instances))
	for i, inst := range instances {
		pages[i].instance = inst
	}
	state := showListAvailability{}
	received := 0
	var grace *time.Timer
	var graceC <-chan time.Time
	defer func() {
		if grace != nil {
			grace.Stop()
		}
	}()
	apply := func(event showPageEvent) {
		pages[event.index].rows = append(pages[event.index].rows, event.rows...)
		if len(event.rows) > 0 {
			state.confirmed = true
			if grace == nil && cfg.Timeouts.SearchGracePeriod > 0 {
				grace = time.NewTimer(time.Duration(cfg.Timeouts.SearchGracePeriod) * time.Millisecond)
				graceC = grace.C
			}
		}
		if event.done {
			received++
			state.confirmed = state.confirmed || event.err == nil
			state.incomplete = state.incomplete || event.err != nil
			if event.err != nil && a.Logger != nil {
				status := 0
				if response, ok := asUpstreamResponseError(event.err); ok {
					status = response.StatusCode
				}
				a.Logger.Warnf("show_list trace=%s server=%s route=show-%s status=%d retained=%d incomplete=true error=%s", playbackTraceID(r), instances[event.index].ServerID, suffix, status, len(pages[event.index].rows), redactURLInError(event.err))
			}
		}
	}
	for received < len(instances) {
		select {
		case event := <-events:
			apply(event)
		case <-ctx.Done():
			state.incomplete = true
			goto drain
		case <-graceC:
			state.incomplete = true
			goto drain
		}
	}
	return pages, state
drain:
	cancel()
	workers.Wait()
	// Keep already queued pages even if the deadline won the select race.
	for {
		select {
		case event := <-events:
			apply(event)
		default:
			return pages, state
		}
	}
}

// Choose a season under this *physical series*, not the first season mapping
// on the same server. Legacy cached mappings are confirmed when ambiguous.
func (a *App) showInstanceQuery(ctx context.Context, reqCtx *RequestContext, inst seriesInstance, instances []seriesInstance, base url.Values, budget *catalogRequestBudget) (url.Values, bool, error) {
	query := cloneValues(base)
	setQueryValue(query, "UserId", inst.Client.clientUserID())
	season := queryValue(query, "SeasonId")
	if season == "" {
		return query, false, nil
	}
	var candidates []AdditionalInstance
	for _, pair := range a.IDStore.PhysicalInstances(season) {
		if pair.ServerID == inst.ServerID {
			candidates = append(candidates, pair)
		}
	}
	copies := 0
	for _, candidate := range instances {
		if candidate.ServerID == inst.ServerID {
			copies++
		}
	}
	for _, pair := range candidates {
		owns, known := a.IDStore.resourceParentMatches("season", pair.ServerID, pair.OriginalID, inst.OriginalID)
		if owns || !known && copies == 1 && len(candidates) == 1 {
			setQueryValue(query, "SeasonId", pair.OriginalID)
			return query, false, nil
		}
		if known {
			continue
		}
		if !budget.tryAcquire() {
			return nil, false, errParentalBudgetExhausted
		}
		payload, err := inst.Client.RequestJSON(ctx, reqCtx, a.Identity, http.MethodGet, "/Items/"+url.PathEscape(pair.OriginalID), url.Values{"Fields": {"SeriesId"}}, nil)
		if err != nil {
			return nil, false, err
		}
		if item, ok := payload.(map[string]any); ok && itemText(item, "SeriesId") != "" {
			a.IDStore.RegisterResourceParent("season", pair.ServerID, pair.OriginalID, itemText(item, "SeriesId"))
			if itemText(item, "SeriesId") == inst.OriginalID {
				setQueryValue(query, "SeasonId", pair.OriginalID)
				return query, false, nil
			}
		} else {
			return nil, false, fmt.Errorf("season parent could not be confirmed")
		}
	}
	return nil, true, nil
}
