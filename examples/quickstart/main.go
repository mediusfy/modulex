// Package main is a minimal runnable example of Modulex lifecycle orchestration.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"github.com/mediusfy/modulex"
)

// LoggerService is a trivial service registered and resolved by typed key.
type LoggerService struct{}

func (LoggerService) Log(msg string) { fmt.Println(msg) }

var loggerKey = modulex.NewKey[LoggerService]("quickstart.Logger")

// LoggerModule registers a shared logger service.
type LoggerModule struct{}

func (m *LoggerModule) Name() string        { return "logger" }
func (m *LoggerModule) DependsOn() []string { return nil }
func (m *LoggerModule) Init(ctx context.Context, reg modulex.Registry) error {
	return modulex.Provide(reg, loggerKey, LoggerService{})
}

// GreeterModule depends on the logger service.
type GreeterModule struct {
	logger LoggerService
}

func (m *GreeterModule) Name() string        { return "greeter" }
func (m *GreeterModule) DependsOn() []string { return []string{"logger"} }
func (m *GreeterModule) Start(ctx context.Context) error {
	m.logger.Log("greeter started")
	return nil
}
func (m *GreeterModule) Init(ctx context.Context, reg modulex.Registry) error {
	logger, err := modulex.Resolve(reg, loggerKey)
	if err != nil {
		return err
	}
	m.logger = logger
	return nil
}

func main() {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	manager, err := modulex.NewManager(modulex.WithLogger(logger))
	if err != nil {
		panic(err)
	}

	if err := manager.RegisterModule(&LoggerModule{}); err != nil {
		panic(err)
	}
	if err := manager.RegisterModule(&GreeterModule{}); err != nil {
		panic(err)
	}

	ctx := context.Background()
	if err := manager.InitModules(ctx); err != nil {
		panic(err)
	}
	if err := manager.StartModules(ctx); err != nil {
		panic(err)
	}
	if err := manager.StopModules(ctx); err != nil {
		panic(err)
	}
}
