// Package main demonstrates a monolithic Modulex deployment: every feature
// module is registered in-process and dependencies are wired directly to local
// implementations.
package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"os/signal"
	"syscall"

	gochi "github.com/go-chi/chi/v5"

	"github.com/mediusfy/modulex"
	modulexchi "github.com/mediusfy/modulex/chi"
	"github.com/mediusfy/modulex/examples/deployment/consumer"
	"github.com/mediusfy/modulex/examples/deployment/notification"
)

func main() {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	router := gochi.NewRouter()

	mgr, err := modulex.NewManager(modulex.WithLogger(logger))
	if err != nil {
		logger.Error("failed to create manager", slog.Any("error", err))
		return
	}
	if err := modulexchi.RegisterRouter(mgr, router); err != nil {
		logger.Error("failed to register router", slog.Any("error", err))
		return
	}

	if err := mgr.RegisterModule(notification.NewModule()); err != nil {
		logger.Error("failed to register notification module", slog.Any("error", err))
		return
	}
	if err := mgr.RegisterModule(consumer.NewModule()); err != nil {
		logger.Error("failed to register consumer module", slog.Any("error", err))
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := mgr.InitModules(ctx); err != nil {
		logger.Error("failed to init modules", slog.Any("error", err))
		return
	}
	if err := mgr.StartModules(ctx); err != nil {
		logger.Error("failed to start modules", slog.Any("error", err))
		return
	}

	server := &http.Server{Addr: ":8080", Handler: router}
	go func() {
		logger.Info("starting monolith server on :8080")
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server exited", slog.Any("error", err))
		}
	}()

	<-ctx.Done()
	logger.Info("shutdown signal received, stopping server and modules")

	// Detach from the (now-canceled) signal context so the shutdown below
	// runs to completion instead of being cancelled by the same signal.
	shutdownCtx := context.WithoutCancel(ctx)
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("failed to shut down server", slog.Any("error", err))
	}
	if err := mgr.StopModules(shutdownCtx); err != nil {
		logger.Error("failed to stop modules", slog.Any("error", err))
	}
}
