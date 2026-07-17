// Package main is a standalone consumer of Modulex in its own module, used to
// verify that importing only the core "github.com/mediusfy/modulex" package
// does not pull in any integration adapter (chi, nats, rabbitmq, watermill,
// otel) as a build dependency. See ../../scripts/check-consumer-boundary.sh
// and the "Ensure consumers compile without unused integration dependencies"
// item in docs/planning/library-readiness-checklist.md.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/mediusfy/modulex"
)

type greeterService struct{}

func (greeterService) Greet() string { return "hello from an external consumer" }

var greeterKey = modulex.NewKey[greeterService]("external.Greeter")

type greeterModule struct{}

func (m *greeterModule) Name() string        { return "greeter" }
func (m *greeterModule) DependsOn() []string { return nil }
func (m *greeterModule) Init(_ context.Context, reg modulex.Registry) error {
	return modulex.Provide(reg, greeterKey, greeterService{})
}

func main() {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager, err := modulex.NewManager(nil, logger, nil)
	if err != nil {
		panic(err)
	}

	if err := manager.RegisterModule(&greeterModule{}); err != nil {
		panic(err)
	}

	ctx := context.Background()
	if err := manager.InitModules(ctx); err != nil {
		panic(err)
	}
	defer func() { _ = manager.StopModules(ctx) }()

	svc, err := modulex.Resolve(manager, greeterKey)
	if err != nil {
		panic(err)
	}
	fmt.Println(svc.Greet())
}
