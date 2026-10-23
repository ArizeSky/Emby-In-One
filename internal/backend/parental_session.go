package backend

import (
	"errors"
	"net/http"
	"sync"
)

var errParentalBudgetExhausted = errors.New("parental metadata budget exhausted")

// The actual client scopes metadata to its account/generation. These raw
// records only live for this request; permissions are never cached globally.
type parentalMetadataKey struct {
	client *UpstreamClient
	id     string
}

type parentalMetadataRecord struct {
	item     map[string]any
	complete bool
	err      error
}

type parentalReadSession struct {
	maxRating    int
	blockUnrated bool
	mu           sync.Mutex
	budget       int
	catalog      *catalogRequestBudget
	metadata     map[parentalMetadataKey]parentalMetadataRecord
	loading      map[parentalMetadataKey]chan struct{}
	slots        chan struct{}
	allowed      int
	unconfirmed  int
}

func newParentalReadSession(maxRating int, blockUnrated bool) *parentalReadSession {
	return &parentalReadSession{
		maxRating: maxRating, blockUnrated: blockUnrated,
		budget: parentalConfirmRequestBudget, catalog: newCatalogRequestBudget(),
		metadata: map[parentalMetadataKey]parentalMetadataRecord{},
		loading:  map[parentalMetadataKey]chan struct{}{}, slots: make(chan struct{}, 4),
	}
}

func (s *parentalReadSession) restricted() bool { return s.maxRating > 0 || s.blockUnrated }

func (a *App) parentalSessionFor(r *http.Request) *parentalReadSession {
	if rc := requestContextFrom(r.Context()); rc != nil && rc.Parental != nil {
		return rc.Parental
	}
	maxR, block := a.parentalPolicyFor(r)
	return newParentalReadSession(maxR, block)
}

func (s *parentalReadSession) takeBudget() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.budget <= 0 || !s.catalog.tryAcquire() {
		return false
	}
	s.budget--
	return true
}

func (s *parentalReadSession) note(decision parentalItemDecision) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if decision == parentalAllow {
		s.allowed++
	} else if decision == parentalUnconfirmed {
		s.unconfirmed++
	}
}

func (s *parentalReadSession) cached(key parentalMetadataKey) (parentalMetadataRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.metadata[key]
	return v, ok
}

func (s *parentalReadSession) store(key parentalMetadataKey, value parentalMetadataRecord) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.metadata[key]; ok && old.complete {
		return
	}
	if len(s.metadata) < 20000 {
		s.metadata[key] = value
	}
}

func (a *App) loadParentalMetadata(r *http.Request, s *parentalReadSession, client *UpstreamClient, id string) (map[string]any, error) {
	key := parentalMetadataKey{client, id}
	s.mu.Lock()
	if value, ok := s.metadata[key]; ok && value.complete {
		s.mu.Unlock()
		return value.item, value.err
	}
	if done := s.loading[key]; done != nil {
		s.mu.Unlock()
		select {
		case <-done:
			return a.loadParentalMetadata(r, s, client, id)
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
	}
	if len(s.metadata)+len(s.loading) >= 20000 {
		s.mu.Unlock()
		return nil, errParentalBudgetExhausted
	}
	done := make(chan struct{})
	s.loading[key] = done
	s.mu.Unlock()
	item, err := a.fetchBudgetedParentalDetail(r, s, client, id)
	s.mu.Lock()
	s.metadata[key] = parentalMetadataRecord{item: item, complete: true, err: err}
	delete(s.loading, key)
	close(done)
	s.mu.Unlock()
	return item, err
}

func (a *App) fetchBudgetedParentalDetail(r *http.Request, s *parentalReadSession, client *UpstreamClient, id string) (map[string]any, error) {
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-r.Context().Done():
		return nil, r.Context().Err()
	}
	if !s.takeBudget() {
		return nil, errParentalBudgetExhausted
	}
	item, err := a.fetchItemForParentalCheck(r, client, id)
	if err == nil {
		if got, _ := item["Id"].(string); got != id {
			err = errParentalMetadataShape
		}
	}
	return item, err
}
