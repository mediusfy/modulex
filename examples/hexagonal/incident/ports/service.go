package ports

import (
	"context"

	"github.com/mediusfy/modulex"
	"github.com/mediusfy/modulex/examples/hexagonal/incident/domain"
)

// Service is the inbound driving port interface for the incident core domain.
type Service interface {
	CreateIncident(ctx context.Context, title, severity string) (*domain.Incident, error)
	ListIncidents(ctx context.Context) ([]domain.Incident, error)
}

// ServiceKey is the typed service-locator key used to register and resolve
// incident.Service implementations.
var ServiceKey = modulex.NewKey[Service]("incident.Service")
