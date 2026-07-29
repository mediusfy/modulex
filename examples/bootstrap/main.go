// Package main demonstrates modulex/app's Run helper together with
// modulex.WithTypedConfig. Together they replace the hand-written
// construct-manager / type-assert config loader / signal-context /
// Init-Start-wait-Stop skeleton that every modulex service entrypoint
// otherwise repeats (compare with examples/quickstart, which shows that
// skeleton written out explicitly) with a single Run call.
//
// Run for a few seconds, or press Ctrl+C to trigger a graceful shutdown:
//
//	go run ./examples/bootstrap
package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/mediusfy/modulex"
	"github.com/mediusfy/modulex/app"
)

// Config is this example's application configuration, wired in via
// modulex.WithTypedConfig instead of a hand-written WithConfigLoader
// closure.
type Config struct {
	Greeting string
}

// GreeterModule resolves its typed config in Init and logs a greeting when
// started.
type GreeterModule struct {
	logger *slog.Logger
	cfg    Config
}

func (m *GreeterModule) Name() string        { return "greeter" }
func (m *GreeterModule) DependsOn() []string { return nil }

func (m *GreeterModule) Init(_ context.Context, reg modulex.Registry) error {
	m.logger = reg.Logger()
	return reg.GetConfig(&m.cfg)
}

func (m *GreeterModule) Start(context.Context) error {
	m.logger.Info(m.cfg.Greeting)
	return nil
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	cfg := Config{Greeting: "hello from modulex/app"}

	err := app.Run(logger, nil, []modulex.Module{&GreeterModule{}},
		app.WithManagerOptions(modulex.WithTypedConfig(cfg)),
	)
	if err != nil {
		logger.Error("application failed", slog.Any("error", err))
		os.Exit(1)
	}
}
