package service

import (
	"context"
	"fmt"
	"time"

	"github.com/mediusfy/modulex/examples/hexagonal/incident/domain"
	"github.com/mediusfy/modulex/examples/hexagonal/incident/ports"
)

type service struct {
	repo ports.Repository
}

// New instantiates a new incident core service implementing ports.Service.
func New(repo ports.Repository) ports.Service {
	return &service{repo: repo}
}

func (s *service) CreateIncident(ctx context.Context, title, severity string) (*domain.Incident, error) {
	inc := &domain.Incident{
		ID:        fmt.Sprintf("INC-%d", time.Now().UnixNano()),
		Title:     title,
		Severity:  severity,
		CreatedAt: time.Now(),
	}
	if err := s.repo.Save(ctx, inc); err != nil {
		return nil, fmt.Errorf("failed to save incident: %w", err)
	}
	return inc, nil
}

func (s *service) ListIncidents(ctx context.Context) ([]domain.Incident, error) {
	return s.repo.FindAll(ctx)
}
