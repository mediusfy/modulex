// Package main demonstrates a monolithic Modulex deployment: every feature
// module is registered in-process and dependencies are wired directly to local
// implementations.
package main

import (
	"context"
	"io"
	"log/slog"

	gochi "github.com/go-chi/chi/v5"

	"github.com/mediusfy/modulex"
	modulexchi "github.com/mediusfy/modulex/chi"
	"github.com/mediusfy/modulex/examples/deployment/consumer"
	"github.com/mediusfy/modulex/examples/deployment/notification"
)

func main() {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := gochi.NewRouter()

	mgr := modulex.NewManager(nil, logger, nil)
	if err := modulexchi.RegisterRouter(mgr, router); err != nil {
		logger.Error("failed to register router", slog.Any("error", err))
		return
	}

	if err := mgr.RegisterModule(notification.NewModule(logger)); err != nil {
		logger.Error("failed to register notification module", slog.Any("error", err))
		return
	}
	if err := mgr.RegisterModule(consumer.NewModule()); err != nil {
		logger.Error("failed to register consumer module", slog.Any("error", err))
		return
	}

	ctx := context.Background()
	if err := mgr.InitModules(ctx); err != nil {
		logger.Error("failed to init modules", slog.Any("error", err))
		return
	}
	if err := mgr.StartModules(ctx); err != nil {
		logger.Error("failed to start modules", slog.Any("error", err))
		return
	}
	if err := mgr.StopModules(ctx); err != nil {
		logger.Error("failed to stop modules", slog.Any("error", err))
		return
	}
}
