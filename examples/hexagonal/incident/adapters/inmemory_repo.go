package adapters

import (
	"context"
	"sync"

	"github.com/mediusfy/modulex/examples/hexagonal/incident/domain"
	"github.com/mediusfy/modulex/examples/hexagonal/incident/ports"
)

// InMemoryRepository is a database adapter that implements ports.Repository.
type InMemoryRepository struct {
	mu        sync.RWMutex
	incidents []domain.Incident
}

// NewInMemoryRepository creates a thread-safe local storage adapter.
func NewInMemoryRepository() ports.Repository {
	return &InMemoryRepository{}
}

func (r *InMemoryRepository) Save(ctx context.Context, inc *domain.Incident) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.incidents = append(r.incidents, *inc)
	return nil
}

func (r *InMemoryRepository) FindAll(ctx context.Context) ([]domain.Incident, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	copied := make([]domain.Incident, len(r.incidents))
	copy(copied, r.incidents)
	return copied, nil
}
