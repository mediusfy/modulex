package ports

import (
	"context"

	"github.com/mediusfy/modulex"
	"github.com/mediusfy/modulex/examples/scaffolded-sample/domain"
)

// Service is the inbound driving port interface for the scaffolded-sample core domain.
type Service interface {
	CreateScaffoldedSample(ctx context.Context, name string) (*domain.ScaffoldedSample, error)
	ListScaffoldedSamples(ctx context.Context) ([]domain.ScaffoldedSample, error)
}

// ServiceKey is the typed service-locator key for ports.Service.
//
// Constructor injection is the generated default: module.go's Init calls
// service.New directly, and other code that needs this module's business
// logic should receive a ports.Service the same way, via a constructor
// parameter. ServiceKey is an OPTIONAL alternative for the narrower case
// where a different module needs to reach this one's Service without
// importing the service package directly — e.g.:
//
//	svc, err := modulex.Resolve(reg, ports.ServiceKey)
//
// Prefer constructor injection unless you have a concrete reason to use
// the locator instead. See
// docs/planning/scaffolding-and-test-harness-guide.md.
var ServiceKey = modulex.NewKey[Service]("scaffolded-sample.Service")
