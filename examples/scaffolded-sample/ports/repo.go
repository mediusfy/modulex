package ports

import (
	"context"

	"github.com/mediusfy/modulex/examples/scaffolded-sample/domain"
)

// Repository is the outbound driven port interface for scaffolded-sample data storage.
type Repository interface {
	Save(ctx context.Context, item *domain.ScaffoldedSample) error
	FindAll(ctx context.Context) ([]domain.ScaffoldedSample, error)
}
