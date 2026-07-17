package ports

import (
	"context"

	"github.com/mediusfy/modulex/examples/hexagonal/incident/domain"
)

// Repository is the outbound driven port interface for incident data storage.
type Repository interface {
	Save(ctx context.Context, incident *domain.Incident) error
	FindAll(ctx context.Context) ([]domain.Incident, error)
}
