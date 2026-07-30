package adapters

import (
	"context"
	"fmt"
	"sync"

	"github.com/mediusfy/modulex/examples/scaffolded-sample/domain"
	"github.com/mediusfy/modulex/examples/scaffolded-sample/ports"
)

// InMemoryRepository is a database adapter implementing ports.Repository,
// suitable for local development and tests. It also exposes Closed(),
// which is not part of ports.Repository — it lets tests verify (via
// modtest.AssertResourceOwnership) that module.go's Stop releases it. See
// docs/planning/scaffolding-and-test-harness-guide.md.
type InMemoryRepository struct {
	mu     sync.RWMutex
	items  []domain.ScaffoldedSample
	closed bool
}

// NewInMemoryRepository creates a thread-safe local storage adapter.
func NewInMemoryRepository() *InMemoryRepository {
	return &InMemoryRepository{}
}

func (r *InMemoryRepository) Save(ctx context.Context, item *domain.ScaffoldedSample) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return fmt.Errorf("scaffolded-sample repository is closed")
	}
	r.items = append(r.items, *item)
	return nil
}

func (r *InMemoryRepository) FindAll(ctx context.Context) ([]domain.ScaffoldedSample, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	copied := make([]domain.ScaffoldedSample, len(r.items))
	copy(copied, r.items)
	return copied, nil
}

// Close marks the repository closed, releasing it. Safe to call more than
// once.
func (r *InMemoryRepository) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	return nil
}

// Closed reports whether Close has been called. It satisfies
// modtest.ResourceOwner structurally, without this package importing
// modtest.
func (r *InMemoryRepository) Closed() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.closed
}

var _ ports.Repository = (*InMemoryRepository)(nil)
