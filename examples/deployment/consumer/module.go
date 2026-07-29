// Package consumer is a dependent feature module that consumes the notification
// service. It is intentionally small so the monolith and remote examples can
// share the same business code while wiring different implementations.
package consumer

import (
	"context"

	"github.com/mediusfy/modulex"
	"github.com/mediusfy/modulex/examples/deployment/notification/ports"
)

// Module depends on the notification service.
type Module struct {
	svc ports.Service
}

// NewModule creates a consumer module.
func NewModule() *Module {
	return &Module{}
}

func (m *Module) Name() string { return "consumer" }

func (m *Module) DependsOn() []string { return []string{"notification"} }

// Init resolves the notification service port.
func (m *Module) Init(ctx context.Context, reg modulex.Registry) error {
	svc, err := modulex.Resolve(reg, ports.ServiceKey)
	if err != nil {
		return err
	}
	m.svc = svc
	return nil
}

// Start implements modulex.Startable. It sends a notification to demonstrate
// that the dependency is wired correctly.
func (m *Module) Start(ctx context.Context) error {
	return m.svc.Send(ctx, "hello from consumer")
}
