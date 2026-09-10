package store

import (
	"context"
	"sync"
	"time"
)

// Memory is an in-memory JobStore + DedupStore + Ledger, used by tests and
// local development. Mutations are serialized per store, matching the
// transactional contract the Firestore implementation provides per document.
type Memory struct {
	mu     sync.Mutex
	jobs   map[JobKey]Job
	dedup  map[string]time.Time // delivery ID -> expiry
	ledger map[int64]Usage
	// Now is the clock, overridable in tests. Defaults to time.Now.
	Now func() time.Time
}

func NewMemory() *Memory {
	return &Memory{
		jobs:   map[JobKey]Job{},
		dedup:  map[string]time.Time{},
		ledger: map[int64]Usage{},
		Now:    time.Now,
	}
}

func (m *Memory) Mutate(_ context.Context, key JobKey, mutate func(Job) Job) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job := mutate(m.jobs[key])
	m.jobs[key] = job
	return job, nil
}

func (m *Memory) Get(_ context.Context, key JobKey) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.jobs[key], nil
}

func (m *Memory) Seen(_ context.Context, deliveryID string, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.Now()
	if expiry, ok := m.dedup[deliveryID]; ok && now.Before(expiry) {
		return true, nil
	}
	m.dedup[deliveryID] = now.Add(ttl)
	return false, nil
}

func (m *Memory) Forget(_ context.Context, deliveryID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.dedup, deliveryID)
	return nil
}

func (m *Memory) Add(_ context.Context, installationID int64, reviews, tokens int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u := m.ledger[installationID]
	u.Reviews += reviews
	u.Tokens += tokens
	m.ledger[installationID] = u
	return nil
}

func (m *Memory) Total(_ context.Context, installationID int64) (Usage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ledger[installationID], nil
}
