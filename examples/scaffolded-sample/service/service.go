package service

import (
	"context"
	"fmt"
	"time"

	"github.com/mediusfy/modulex/examples/scaffolded-sample/domain"
	"github.com/mediusfy/modulex/examples/scaffolded-sample/ports"
)

// service implements ports.Service via constructor-injected dependencies —
// the generated default. See ports.ServiceKey's doc comment for the
// optional typed-service-location alternative.
type service struct {
	repo ports.Repository
}

// New constructs a ports.Service using constructor injection: the caller
// supplies its ports.Repository dependency directly, rather than resolving
// it from a registry. module.go's Init calls New exactly like this; prefer
// the same pattern anywhere else this Service is needed.
func New(repo ports.Repository) ports.Service {
	return &service{repo: repo}
}

func (s *service) CreateScaffoldedSample(ctx context.Context, name string) (*domain.ScaffoldedSample, error) {
	item := &domain.ScaffoldedSample{
		ID:        fmt.Sprintf("SCAFFOLDED_SAMPLE-%d", time.Now().UnixNano()),
		Name:      name,
		CreatedAt: time.Now(),
	}
	if err := s.repo.Save(ctx, item); err != nil {
		return nil, fmt.Errorf("failed to save scaffolded-sample: %w", err)
	}
	return item, nil
}

func (s *service) ListScaffoldedSamples(ctx context.Context) ([]domain.ScaffoldedSample, error) {
	return s.repo.FindAll(ctx)
}
